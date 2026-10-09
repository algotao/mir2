package gamesvr

import (
	"context"
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/tscale"
)

// spawnLoop 周期性补充怪物。
func (s *Server) spawnLoop() {
	t := time.NewTicker(tickDur(spawnCheckInterval))
	defer t.Stop()
	for range t.C {
		s.refillSpawns()
		// 顺手收尸（原版 `dwMakeGhostTime` 3 分钟，见 `sweepCorpses`）。
		// 挂在这一档（5 秒）而不是怪物 AI 那一档：尸体过期是分钟级的事。
		s.sweepCorpses(time.Now())
	}
}

// monsterLoop 怪物 AI 主循环。
func (s *Server) monsterLoop() {
	t := time.NewTicker(tickDur(monsterTickInterval))
	defer t.Stop()
	for range t.C {
		s.tickMonsters(time.Now())
	}
}

// sessionLeaseLoop notices account takeovers across gamesvr processes and closes stale players.
func (s *Server) sessionLeaseLoop() {
	t := time.NewTicker(sessionLeaseInterval)
	defer t.Stop()
	for now := range t.C {
		s.tickSessionLeases(now)
	}
}

func (s *Server) tickSessionLeases(now time.Time) {
	s.mu.RLock()
	players := make([]*Player, 0, len(s.world.players))
	for _, p := range s.world.players {
		players = append(players, p)
	}
	s.mu.RUnlock()

	for _, p := range players {
		if p.sessionID == 0 || p.Char == nil || p.Char.Account == "" || p.revoked.Load() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), sessionLeaseInterval)
		ok, err := s.store.Sessions().RenewGameLease(ctx, p.Char.Account, p.sessionID, p.Char.Name,
			now, now.Add(gameLeaseDuration))
		cancel()
		if err != nil || !ok {
			if !p.revoked.CompareAndSwap(false, true) {
				continue
			}
			if err != nil {
				log.Printf("%s 游戏租约续期失败，断开连接: %v", p.Char.Name, err)
			} else {
				log.Printf("%s 的游戏会话已被新登录接管，断开旧连接", p.Char.Name)
			}
			// Closing the socket wakes handleConn; its deferred removePlayer flushes
			// the final snapshot before releasing the lease for the replacement.
			if p.conn != nil {
				_ = p.conn.Close()
			}
		}
	}
}

// tickDur 是给周期性 tick 用的时间缩放。
//
// ⚠️ 下限 40ms：倍速再高也不能让 tick 变成忙等（100 倍速下 500ms 的怪物
// tick 若真除到 5ms，单核就会烧在定时器上，反而拖慢整体）。
func tickDur(d time.Duration) time.Duration {
	out := tscale.D(d)
	if out < 40*time.Millisecond {
		return 40 * time.Millisecond
	}
	return out
}

// regenPercent 是每秒回复的最大生命/魔法百分比。
const regenPercent = 5

// regenLoop 每秒给在线角色回复 HP/MP。
//
// 原版由 RecalcAbilitys 里的 HealthRecover/SpellRecover 按 tick 累加，
// 这里简化为固定百分比 + 至少 1 点，保证低等级也不会"回不动"。
func (s *Server) regenLoop() {
	t := time.NewTicker(tickDur(time.Second))
	defer t.Stop()
	for range t.C {
		// 与恢复无关的"个人周期状态"（烈火剑法充能到期）必须**在 noRegen
		// 早退之前**处理：-noregen 只是关恢复，不该关掉充能到期。
		now := time.Now()
		s.tickPlayers(now)
		// 开到点的门关回去（原版引擎每 tick 扫，我们挂在 1 秒这一档上）
		s.tickDoors(now)
		s.regenOnce()
	}
}

// tickPlayers 是每个在线玩家每秒的周期检查（原版挂在 TPlayObject.Run 里）。
func (s *Server) tickPlayers(now time.Time) {
	s.mu.RLock()
	ps := make([]*Player, 0, len(s.world.players))
	for _, p := range s.world.players {
		ps = append(ps, p)
	}
	s.mu.RUnlock()

	for _, p := range ps {
		if p.Char == nil || p.Char.Data == nil {
			continue
		}
		s.tickWarrCharge(p, now)
		s.tickPlayerStatus(p, now)
		s.tickMapHP(p, now)
		s.tickDayChanging(p, now)
		// ⚠️ **只对新协议玩家**：补"站着不动也得看得见走近的实体"这个缺口。
		// 为什么不对所有人做：legacy 客户端会因此收到额外的 SM_TURN/SM_DISAPPEAR，
		// 而 mir2cli 的 e2e 是按包序断言的（详见 tickProtoVision 的说明）。
		s.tickProtoVision(p)
	}
}

// tickPlayerStatus 在状态位**该变了**的时候补一次广播。
//
// ⚠️ 这一条是补一个真 bug：我们的状态位是**按需算**的（`statusBits()` 按当前时刻算
// 石化/中毒是否还在），而客户端只认 `SM_CHARSTATUSCHANGED` 那条包 ⇒
// **石化/中毒到期时如果没别的事情触发广播，客户端会一直停在"被石化"的画面上**
// （麻痹戒指打人之后尤其明显：对方永远动不了的样子）。
//
// 原版是 `m_wStatusTimeArr[i]` 的计时器到点后走 `StatusChanged()`
// （`MakePosion` / `Run` 里的 `DecTime`，ObjBase.pas:22744-22748）。
// 我们这里每秒对一次"算出来的位"与"上次发出去的位"（`Object.Status` 就是那份快照）。
func (s *Server) tickPlayerStatus(p *Player, now time.Time) {
	if p == nil || p.Obj == nil {
		return
	}
	// ⚠️ 这份快照由本人 goroutine 写（换装/吃药/增益）⇒ 持锁读（P1-5）。
	if want := p.statusBits(); want != s.statusBit(p) {
		s.broadcastStatus(p)
	}
}

// tickMapHP 复刻原版 `TPlayObject.Run` 里的**地图级**扣血/加血（ObjBase.pas:6819-6843）：
//
//	if 该图 boDECHP and (距上次 > nDECHPTIME 秒) then
//	  HP := (HP > nDECHPPOINT ? HP - nDECHPPOINT : 0);  HealthSpellChanged();
//	if 该图 boINCHP and (距上次 > nINCHPTIME 秒) then
//	  HP := (HP + nDECHPPOINT < MaxHP ? HP + nDECHPPOINT : MaxHP);  HealthSpellChanged();
//
// ⚠️ 三处照抄、别"修"：
//  1. 加血用的也是 `nDECHPPOINT`（原版没有单独的"加血点"）；
//  2. 扣到 0 **不是死亡**（原版这里没有 `Die` 调用）—— 玩家会停在 0 血等下一下打击；
//  3. 计时器是**每个对象一份**（`m_dwDecHPTick`），不是每张图一份。
//
// 官方出厂 mapinfo 里一张 DECHP/INCHP 图都没有（`grep` 0 命中）⇒ 这条在实际数据上
// 是空转，但它是"地图标记的消费点"，接线才算完整（可用单测驱动）。
func (s *Server) tickMapHP(p *Player, now time.Time) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil ||
		p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	mi := s.mapFlagOf(p.Obj.MapRef())
	if mi == nil {
		return
	}
	// ⚠️ 这条跑在 ticker goroutine 上，而玩家自己的 goroutine 同时在动血
	// ⇒ 扣血走持锁的助手，**组包用的也必须是持锁快照**
	//（原来直接读 `p.Char.Data.Abil` 的字段，是审计 P1-5 记的裸读）。
	if mi.DecHPSet && mi.DecHPPoint > 0 && mi.DecHPTime > 0 &&
		now.Sub(p.decHPAt) >= time.Duration(mi.DecHPTime)*time.Second {
		p.decHPAt = now
		p.addHP(-int64(mi.DecHPPoint))
		if ab := p.abilCopy(); ab != nil {
			s.sendHealthChanged(p, p.Obj.ID, ab.Hp, ab.Mp, ab.MaxHp)
		}
	}
	if mi.IncHPSet && mi.IncHPPoint > 0 && mi.IncHPTime > 0 &&
		now.Sub(p.incHPAt) >= time.Duration(mi.IncHPTime)*time.Second {
		p.incHPAt = now
		p.addHP(int64(mi.DecHPPoint)) // ⚠️ 就是用 nDECHPPOINT（原版如此）
		if ab := p.abilCopy(); ab != nil {
			s.sendHealthChanged(p, p.Obj.ID, ab.Hp, ab.Mp, ab.MaxHp)
		}
	}
}

// regenOnce 执行一次恢复，只给有变化的玩家发包。
func (s *Server) regenOnce() {
	if s.cfg.noRegen {
		return // 仅测试：关掉自然恢复
	}
	s.mu.RLock()
	ps := make([]*Player, 0, len(s.world.players))
	for _, p := range s.world.players {
		ps = append(ps, p)
	}
	s.mu.RUnlock()

	for _, p := range ps {
		// 一次持锁快照定判定与组包（审计 P1-5：原来 `ab := p.Char.Data.Abil`
		// 是裸读，而且发出去的还是那个会变的指针里的值）。
		ab := p.abilCopy()
		if ab == nil {
			continue
		}
		if ab.Hp >= ab.MaxHp && ab.Mp >= ab.MaxMp {
			continue
		}
		hpAdd := ab.MaxHp/regenPercent/20 + 1
		mpAdd := ab.MaxMp/regenPercent/20 + 1
		hp, mp, changed := ab.Hp, ab.Mp, false
		if hp < ab.MaxHp {
			hp = p.addHP(int64(hpAdd))
			changed = true
		}
		if mp < ab.MaxMp {
			mp = p.addMP(int64(mpAdd))
			changed = true
		}
		if changed {
			s.sendHealthChanged(p, p.Obj.ID, hp, mp, ab.MaxHp)
		}
	}
}

// autosaveLoop 定时保存所有在线角色。
func (s *Server) autosaveLoop() {
	t := time.NewTicker(s.cfg.saveInterval)
	defer t.Stop()
	for range t.C {
		s.saveAll()
	}
}

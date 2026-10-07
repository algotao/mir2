// 火墙（SKILL_EARTHFIRE = 22）。
//
// Delphi 源码是 GBK，grep 前要先 `iconv -f GBK -t UTF-8`，否则会被当二进制
// 静默漏掉（本项目已因此踩过：以为原版没实现火墙，实际 Magic.pas:501 有）。
//
// 原版依据：
//
//   - Magic.pas:501      施法分派 → MagMakeFireCross(damage, htime, x, y)
//   - Magic.pas:1135-1165 铺 5 格十字 (x,y-1)(x-1,y)(x,y)(x+1,y)(x,y+1)；
//     每格先 GetEvent，**已有事件就跳过**——不覆盖、不叠加。
//     持续时间 = nHTime * 1000 ms。
//   - Magic.pas:502-508  伤害 = GetAttackPower(GetPower(MPow(um)) + LoWord(MC), …)，
//     时间 = GetPower(10) + (Word(GetRPow(MC)) shr 1) 秒
//     ⇒ **基础 10 秒 + 魔攻上限的一半**，与技能等级无关。
//   - Event.pas:236-259  TFireBurnEvent.Run：每 3000ms 扫本格，
//     `m_OwnBaseObject.IsProperTarget(目标)` 通过 → SendMsg(RM_MAGSTRUCK_MINE,
//     0, m_nDamage)。m_nDamage 是**建墙时算好的固定值**，不再重算攻击力。
//   - ObjBase.pas:4501  RM_MAGSTRUCK_MINE 与 RM_MAGSTRUCK **同一套结算**
//     （GetMagStruckDamage(nil, dmg) 走 AC 减免 → StruckDamage + RM_STRUCK_MAG）。
//     唯一差别：RM_MAGSTRUCK 会让 <50 级怪走路变慢 800~1800ms，_MINE 不触发
//     ⇒ 火墙不会把怪打"僵"。
//   - ObjBase.pas:20190-20230 **第二条伤害路径**：TBaseObject.Walk 每次走动时
//     遍历所在格的 OS_EVENTOBJECT，立刻结算一次。
//     ⇒ 体感是"踩上去马上中一次，之后每 3 秒再中一次"。两条路径都复刻。
//   - Event.pas:172-176 TEvent.Run：超时关闭；墙主死亡/幽灵则把 m_OwnBaseObject
//     置 nil ⇒ 墙**仍留在格子里、仍占格**（新墙铺不上），但两条路径都因 owner
//     为 nil 而不再伤人（ObjBase.pas:20224 判空后根本不取该事件）。
//   - Magic.pas:1140-1145 boDisableInSafeZoneFireCross（Setup 的
//     DisableInSafeZoneFireCross，出厂 False）→ 对应 -wall-no-safezone。
//   - Envir.pas:236-238  `if btType = OS_EVENTOBJECT then begin end;` 是**空分支**
//     ⇒ 原版不把事件下发给客户端，客户端看不见火墙。所以这里是纯服务端区域，
//     **零新协议**（客户端要显示火墙得另做，本项目不做）。
//
// 怪物版火墙（ObjMon3.pas:1064 TFireMonster.Run）是另一套：7 格、20 秒、伤害 10，
// 属于怪物 AI，本项目不实现。
package gamesvr

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/world"
)

const (
	// magicFireWall 是火墙的技能号（Grobal2.pas:1291 SKILL_EARTHFIRE=22）。
	magicFireWall = uint32(22)

	// wallBurnInterval 对应 TFireBurnEvent.Run 的 3000ms 门限。
	wallBurnInterval = 3000 * time.Millisecond
	// wallScanInterval 是扫描周期。原版 TEventManager 每 500ms 调一次
	// Event.Run（Event.pas:22 的 m_dwRunTick 初值 500），真正的结算门限在
	// TFireBurnEvent 自己的 3000ms 里。我们直接存绝对时间，500ms 轮询即可。
	wallScanInterval = 500 * time.Millisecond
)

// wallKey 标识地图上的一格。
//
// 原版 GetEvent(Envir, nX, nY) 按**环境 + 坐标 + 事件类型**匹配
// （Event.pas:154-175），即"同一环境同一格同一类型只容一个事件"。用
// *world.Map 指针当环境标识与原版一致（不同地图的同坐标互不冲突）。
type wallKey struct {
	m    *world.Map
	x, y int
}

// Wall 是铺在一格上的火墙（对应 TFireBurnEvent / ET_FIRE）。
type Wall struct {
	// OwnerID 是铺墙者 ActorId（原版 m_OwnBaseObject）。
	OwnerID uint32
	// EventID 是这条墙发给客户端的**地面事件** id（`SM_SHOWEVENT` → 到期
	// `SM_HIDEEVENT` 按它删）。原版把事件对象指针塞在 Recog 里当 id。
	EventID uint32
	// Damage 是**施法瞬间算好的固定威力**（m_nDamage）。之后每次结算只对它
	// 做 AC 减免，不再重算攻击力——这是原版行为。
	Damage uint32
	// Expire 是关闭时刻（m_dwOpenStartTick + m_dwContinueTime）。
	Expire time.Time
	// Next 是下次周期结算的绝对时刻。
	Next time.Time
	// Inert 表示墙主已失效（死亡/幽灵/下线）。原版把 m_OwnBaseObject 置 nil，
	// 墙仍在格子里、仍占格，但不再伤人。
	Inert bool
}

// wallDur 计算火墙持续时间：GetPower(10) + (Word(GetRPow(MC)) shr 1) 秒。
//
// ⚠️ 之前这里写的是"固定 10 秒 + MC**上限**/2"，与技能等级无关——**那是错的**。
// GetPower(10) 里有 `(btLevel+1)/4` 的等级缩放和火墙自己的 DefPower(3)，
// 所以 0/1/2/3 级分别是 5/8/11/13 秒（官方数据 def=3/3、power=3/3）。
// 另一半是 **GetRPow(MC)**（[下限,上限] 闭区间随机）再 `shr 1` 整除 2，
// 不是取上限——原来固定取上限等于让高魔攻玩家的墙总是最长。
func wallDur(info *data.MagicInfo, level uint32, p *Player) time.Duration {
	seconds := magic.GetPower(10, info, level)
	if p != nil {
		// MC 是 MinMax 打包（Grobal2.pas:736）；GetRPow 自己在高低相等时返回下限。
		seconds += magic.GetRPow(playerPowerAttr(p, magic.PowerAttrMC)) / 2
	}
	return time.Duration(max(0, seconds)) * time.Second
}

// placeWalls 在 (x,y) 铺 5 格十字火墙，返回实际铺下的格数。**调用方持 s.mu**。
//
// 5 格十字 = 上/左/中/右/下（Magic.pas:1146-1165）。
func placeWalls(walls map[wallKey]*Wall, m *world.Map, ownerID uint32,
	x, y int, dmg uint32, now time.Time, life time.Duration, next time.Time,
	// onPlace 在**真的铺下一格**时回调（返回该格的地面事件 id；nil 表示不发）。
	onPlace func(x, y int) uint32) int {

	cells := [5][2]int{
		{x, y - 1},
		{x - 1, y},
		{x, y},
		{x + 1, y},
		{x, y + 1},
	}
	placed := 0
	for _, cell := range cells {
		k := wallKey{m, cell[0], cell[1]}
		// GetEvent 已有事件就跳过：不覆盖、不叠加。
		if walls[k] != nil {
			continue
		}
		// 原版不判地形/边界（AddToMap 对越界格静默失败，但事件仍在列表里、
		// 仍占格）。这里显式跳过越界格：实体永远在界内，越界的墙既打不到人
		// 又会占住格子挡掉后续铺墙，纯属泄漏。
		if !m.InBounds(cell[0], cell[1]) {
			continue
		}
		walls[k] = &Wall{
			OwnerID: ownerID,
			Damage:  dmg,
			Expire:  now.Add(life),
			Next:    next,
		}
		if onPlace != nil {
			walls[k].EventID = onPlace(cell[0], cell[1])
		}
		placed++
	}
	return placed
}

// castWall 铺火墙（Magic.pas:1135 MagMakeFireCross）。
func (s *Server) castWall(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic, targX, targY int) {
	m := p.Obj.MapRef()
	if m == nil {
		return
	}
	// 安全区可禁用（Magic.pas:1143）。出厂值是 False（允许），故默认关这个开关。
	if s.cfg.wallNoSafeZone && s.safeZoneAt(m, targX, targY) {
		s.sysMsg(c, "安全区不允许使用...")
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 伤害：原版在**施法瞬间**用攻击力公式算一次，连同事件一起存下来
	// （Magic.pas:503-507）。减免留到结算时做，所以这里不乘 AC/MAC。
	//
	//	MagMakeFireCross(GetAttackPower(GetPower(MPow) + LoWord(MC),
	//	                                 (HiWord(MC) - LoWord(MC)) + 1),
	//	                  GetPower(10) + (GetRPow(MC) shr 1),   // 时长
	//	                  nTargetX, nTargetY)
	dmg := uint32(max(0, magic.SpellRawPower(info, um.Level, playerPowerAttr(p, magic.PowerAttrMC), 1)))
	dur := wallDur(info, um.Level, p)

	now := time.Now()
	// 周期跟随时间倍速（它属于"节奏类"），墙的寿命不跟随（玩法语义，同 buff 时长）。
	next := now.Add(tscale.D(wallBurnInterval))

	type placedEvent struct {
		x, y int
		id   uint32
	}
	var events []placedEvent
	s.mu.Lock()
	// ⚠️ 每铺一格要发一条 SM_SHOWEVENT（ET_FIRE），否则真客户端画不出火
	//（见 groundevent.go）。但这里是**持写锁**的：只能分配 id（原子计数），
	// 发包必须留到锁外 —— 广播要拿 `s.mu.RLock`，同 goroutine 再 RLock 会**自死锁**。
	placed := placeWalls(s.world.walls, m, p.Obj.ID, targX, targY, dmg, now, dur, next,
		func(cx, cy int) uint32 {
			id := s.nextEventID()
			events = append(events, placedEvent{x: cx, y: cy, id: id})
			return id
		})
	s.mu.Unlock()

	for _, e := range events {
		s.announceGroundEvent(m, e.x, e.y, e.id, evFire, 0)
	}

	if placed == 0 {
		// 原版 MagMakeFireCross 返回 0 → boTrain 不加（不涨修炼点）。
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的火墙未铺下：目标十字五格都已有事件 (%d,%d)", p.Char.Name, targX, targY)
		return
	}

	// 落点特效：原版 DoSpell 统一发一次 RM_MAGICFIRE（目标坐标）。
	// 五格各发一次是我们没必要做的额外流量。
	s.broadcastToViewers(m, targX, targY, func(other *Player) {
		s.send(other.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(targX), uint16(targY), uint16(info.MagicID), "")
	})
	obs.Event("wall_cast", "player", p.Char.Name, "x", targX, "y", targY,
		"cells", placed, "dmg", dmg, "dur_s", int(dur/time.Second))
	log.Printf("%s 铺火墙 (%d,%d) %d 格，威力=%d，持续=%v", p.Char.Name, targX, targY, placed, dmg, dur)
}

// wallLoop 推进所有火墙：到期清理 + 周期结算。
func (s *Server) wallLoop() {
	t := time.NewTicker(tickDur(wallScanInterval))
	defer t.Stop()
	for now := range t.C {
		s.wallTick(now)
		// 其它有寿命的地面事件（挖矿的碎石堆、困魔咒的光幕）也在这里推进：
		// 它们要的粒度都不细（分钟级），没必要再开 goroutine。
		s.groundEventTick(now)
	}
}

// wallTick 扫描全部火墙。锁内只做判定与快照，发包在锁外。
func (s *Server) wallTick(now time.Time) {
	s.mu.Lock()
	var expired []wallKey
	var due []wallKey
	for k, w := range s.world.walls {
		if !now.Before(w.Expire) {
			expired = append(expired, k)
			continue
		}
		if w.Inert {
			continue
		}
		// 墙主失效检测（Event.pas:174-175）
		owner := s.world.players[w.OwnerID]
		if owner == nil {
			// 下线：原版没有"下线"（对象留在地图上），我们没有离线残留实体，
			// 按同一语义置失效。
			w.Inert = true
			continue
		}
		if ab := owner.Char.Data.Abil; ab != nil && ab.Hp == 0 {
			w.Inert = true // 死亡（原版 m_boDeath）
			continue
		}
		if !now.Before(w.Next) {
			due = append(due, k)
			w.Next = now.Add(tscale.D(wallBurnInterval))
		}
	}
	// 到期：先记下要抹掉的地面事件（包要在**锁外**发）
	type goneEvent struct {
		m    *world.Map
		x, y int
		id   uint32
	}
	hides := make([]goneEvent, 0, len(expired))
	for _, k := range expired {
		if w := s.world.walls[k]; w != nil && w.EventID != 0 {
			hides = append(hides, goneEvent{m: k.m, x: k.x, y: k.y, id: w.EventID})
		}
		delete(s.world.walls, k)
	}
	// 锁外结算要用的快照（要发包、要判 PvP）
	type job struct {
		key   wallKey
		owner *Player
		dmg   uint32
	}
	jobs := make([]job, 0, len(due))
	for _, k := range due {
		w := s.world.walls[k]
		if w == nil || w.Inert {
			continue
		}
		jobs = append(jobs, job{key: k, owner: s.world.players[w.OwnerID], dmg: w.Damage})
	}
	s.mu.Unlock()

	if len(expired) > 0 {
		obs.Event("wall_expire", "count", len(expired))
	}
	for _, h := range hides {
		s.hideGroundEvent(h.m, h.x, h.y, h.id)
	}
	for _, j := range jobs {
		if j.owner == nil {
			continue
		}
		s.wallBurn(j.key, j.owner, j.dmg, now)
	}
}

// wallBurnAtCell 是**第二条伤害路径**（ObjBase.pas:20190 TBaseObject.Walk）：
// 实体每次移动后，立刻结算脚下那一格火墙。
//
// 单独存在是因为体感：没有它，玩家要等最多 3 秒才第一次被烧，
// 表现为"火墙没生效"。
func (s *Server) wallBurnAtCell(m *world.Map, x, y int) {
	if m == nil {
		return
	}
	s.mu.RLock()
	w := s.world.walls[wallKey{m, x, y}]
	var owner *Player
	if w != nil && !w.Inert && time.Now().Before(w.Expire) {
		owner = s.world.players[w.OwnerID]
	}
	s.mu.RUnlock()
	if owner == nil {
		return
	}
	// 原版 Walk 每次移动都结算，不看"是不是刚踩上去"。
	s.wallBurn(wallKey{m, x, y}, owner, w.Damage, time.Now())
}

// wallPlayerHit 是一次火墙命中玩家的结果（锁外发包用）。
type wallPlayerHit struct {
	victim        *Player
	hp, maxHP, mp uint32
	dmg           uint32
}

// wallBurn 结算一格火墙对格内所有目标的一次伤害。
//
// 目标筛选是**墙主的 IsProperTarget**（Event.pas:249）：墙主与目标的关系决定
// 能不能烧——安全区、行会、PK 模式全都生效。墙主自己不挨自己的墙
// （IsProperTarget 对自己为假）。
//
// 三段式：RLock 快照 → 锁外判关系 → Lock 改血 → 锁外发包。
// ⚠️ 不能把 canAttackTarget 放进 s.mu 里：它会读行会（自带锁），
// 与 guild 侧形成反向加锁顺序。
func (s *Server) wallBurn(k wallKey, owner *Player, dmg uint32, now time.Time) {
	// ---- ① 锁内快照：本格里有哪些实体，以及它们的防御 ----
	var mons []*entity.Monster
	type plCand struct {
		p   *Player
		mac uint32 // 魔法伤害减的是 MAC（不是 AC）
		red uint32 // 魔法盾减伤百分比
	}
	var pls []plCand

	s.mu.RLock()
	for _, mon := range s.world.monsters {
		if mon.IsDead() || mon.IsNPC || mon.MapRef() != k.m || mon.PosX() != k.x || mon.PosY() != k.y {
			continue
		}
		// 召唤兽不烧自己的主人（原版 IsProperTarget 对自己人同样为假）
		if mon.MasterID != 0 && mon.MasterID == owner.Obj.ID {
			continue
		}
		mons = append(mons, mon)
	}
	for _, p := range s.world.players {
		if p == owner || p.Obj == nil || p.Obj.MapRef() != k.m || p.Obj.PosX() != k.x || p.Obj.PosY() != k.y {
			continue
		}
		if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil || p.hp() == 0 {
			continue
		}
		pls = append(pls, plCand{p: p, mac: s.playerMAC(p), red: p.damageReduction()})
	}
	s.mu.RUnlock()
	if len(mons) == 0 && len(pls) == 0 {
		return
	}

	// ---- ② 锁外判关系（canAttackTarget 不许在 s.mu 内调）----
	mode := attackModeOf(owner)
	hits := pls[:0]
	for _, c := range pls {
		if s.canAttackTarget(owner, c.p, mode, now) {
			hits = append(hits, c)
		}
	}

	// ---- ③ 锁内改血（与 castDamageSpell / monsterStrike 同一约定）----
	type monHit struct {
		m                   *entity.Monster
		id                  uint32
		hp, maxHP, exp, dmg uint32
		died                bool
	}
	var monHits []monHit
	var plHits []wallPlayerHit

	s.mu.Lock()
	for _, mon := range mons {
		// 火墙走的是魔法伤害（RM_MAGSTRUCK_MINE → GetMagStruckDamage），
		// 减的是 **MAC** 且不保底 1；dmg 是建墙时定死的固定威力。
		d := uint32(magic.MagStruckDamage(uint32(mon.Info.MAC), int(dmg)))
		d = uint32(combat.AdjustStruck(mon.Object, int(d), now)) // 红毒：受伤放大
		h := monHit{m: mon, id: mon.ID, maxHP: mon.MaxHP, exp: uint32(mon.Info.Exp), dmg: d}
		_, h.hp, h.died = mon.Hurt(d)
		monHits = append(monHits, h)
	}
	for _, c := range hits {
		// 同上：玩家目标也吃 MAC，不是 AC。
		d := uint32(magic.MagStruckDamage(c.mac, int(dmg)))
		d = uint32(combat.AdjustStruck(c.p.Obj, int(d), now)) // 红毒：受伤放大
		if c.red > 0 {
			d = d * (100 - c.red) / 100
		}
		// ⚠️ 火墙这条跑在 ticker goroutine 上，被烧的人在别的 goroutine 里吃药/挨打
		hp, d := c.p.hurt(d)
		plHits = append(plHits, wallPlayerHit{victim: c.p, hp: hp, maxHP: c.p.maxHP(), mp: c.p.mp(), dmg: d})
	}
	for _, h := range monHits {
		if h.died {
			s.keepCorpseOrRemove(h.m)
		}
	}
	s.mu.Unlock()

	// ---- ④ 锁外发包 ----
	for _, h := range monHits {
		s.broadcastToViewers(k.m, k.x, k.y, func(other *Player) {
			if other.visible.Contains(h.id) {
				s.sendStruck(other, owner.Obj.ID, h.id, h.hp, h.maxHP, h.dmg)
			}
		})
		if !h.died {
			continue
		}
		// 死亡收尾统一走 killMonsterBy（经验/掉落/SM_DEATH/移出索引）
		s.killMonsterBy(owner, h.m, uint16(magicFireWall), k.x, k.y)
		log.Printf("%s 的火墙烧死 %s（伤害 %d）", owner.Char.Name, h.m.Name, h.dmg)
	}
	for _, h := range plHits {
		s.broadcastToViewers(k.m, k.x, k.y, func(o *Player) {
			// ⚠️ 受击者自己也要收到：自己的 visible 集合不含自己
			if o.visible.Contains(h.victim.Obj.ID) || o == h.victim {
				s.sendStruck(o, owner.Obj.ID, h.victim.Obj.ID, h.hp, h.maxHP, h.dmg)
			}
		})
		s.sendHealthChanged(h.victim, h.victim.Obj.ID, h.hp, h.mp, h.maxHP)
		// 被打者获得正当防卫标记（同 spellHitPlayer）
		s.setPKFlag(owner, h.victim, now)
		obs.Event("wall_hit_player", "owner", owner.Char.Name,
			"victim", h.victim.Char.Name, "dmg", h.dmg, "hp", h.hp)
		if h.victim.hp() == 0 {
			s.killPlayerByPlayer(h.victim.conn, owner, h.victim, now)
		}
	}
}

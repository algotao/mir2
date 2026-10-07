// 中毒（绿毒/红毒）与两个技能：施毒术(6)、心灵启示(28)。
//
// 原版依据（都已逐字核对；OpenMir2 交叉核对见每段末尾）：
//
//	MakePosion          ObjBase.pas:22730-22758   时长取长、点数覆盖、状态位变才广播
//	RM_POISON 处理      ObjBase.pas:4599-4617     施毒者 SetLastHiter/SetPKFlag
//	绿毒结算            ObjBase.pas:4255-4268     每 dwPosionDecHealthTime 扣 GreenPoisoningPoint+1
//	红毒放大            ObjBase.pas:22471-22476   StruckDamage：伤害与掉持久 ×(nPosionDamagarmor/10)
//	施毒术(6)           Magic.pas:355-382         CheckAmulet(1,2) + `Random(抗毒+7) <= 6` + Shape 1/2
//	心灵启示(28)        Magic.pas:522-531         `Random(6) <= 等级+3` → 置 ShowHP + RM_DOOPENHEALTH
//	MakeOpenHealth      ObjBase.pas:3606-3612     StatusEx |= STATE_OPENHEATH，SendRefMsg(RM_OPENHEALTH)
//	BreakOpenHealth     ObjBase.pas:3595-3604     到期（:4029-4031）→ SendRefMsg(RM_CLOSEHEALTH)
//
// ⚠️ 两份文档（progress 的"施毒术=不做"、HANDOFF 坑 41）都写错了：它们看的是
// `Magic.pas:318` 那个 **`(* ... *)` 注释掉的旧分支**，而真正的实现在 355-382 行
// （`CheckAmulet` 版）——OpenMir2 的 `MagicManager.cs:228-262` 与它逐行一致。
// 心灵启示同理："依赖客户端血条协议" 说的是**没法验证**，但协议本来就有
// （`SM_OPENHEALTH`=1100 / `SM_CLOSEHEALTH`=1101，客户端 ClMain.pas:4283 按
// `Recog=ActorId, Param=HP, Tag=MaxHP` 解）⇒ 端到端完全可断言。
//
// 配置（官方 `!setup.txt`，本项目惯例：按实测值取常量）：
//
//	AmyOunsulPoint=10       施毒术"每跳伤害"公式的分母
//	PosionDecHealthTime=2500 绿毒结算间隔（ms）
//	PosionDamagarmor=12      红毒受伤倍率分子（12 ⇒ ×1.2）
package gamesvr

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

const (
	// poisonDecHealthTime 绿毒结算间隔（!setup.txt:922）。
	poisonDecHealthTime = 2500 * time.Millisecond
	// amyOunsulPoint 施毒术每跳伤害的分母（!setup.txt:909）。
	amyOunsulPoint = 10
	// 抗毒判定：`Random(抗毒 + 7) <= 6`（Magic.pas:364）。
	poisonResistBase = 7
	poisonResistMax  = 6

	magicAmyOunsul = 6  // 施毒术
	magicShowHP    = 28 // 心灵启示
)

// 中毒的状态位（`StatePoisonGreen` / `StatePoisonRed`）与隐身/护盾那几位
// 同在 internal/entity/buff.go —— 它们共享同一个 `Object.Status`，放一起才不会串位。

// poisonMask 返回对象当前中毒对应的状态位（供 SM_CHARSTATUSCHANGED）。
// ⚠️ 返回 uint32：绿毒那一位是 0x80000000（官方下标 0 落在最高位），int32 装不下。
func poisonMask(o *entity.Object, now time.Time) uint32 {
	if o == nil {
		return 0
	}
	var m uint32
	if o.PoisonActive(entity.PoisonDecHealth, now) {
		m |= entity.StatePoisonGreen
	}
	if o.PoisonActive(entity.PoisonDamageArmor, now) {
		m |= entity.StatePoisonRed
	}
	return m
}

// struckPlayer / struckMonster 把红毒倍率套在**最终伤害**上。
//
// 原版只有一处（`TBaseObject.StruckDamage`，ObjBase.pas:22471-22476），因为它的
// 伤害都汇到那里；我们的伤害有 7 个落点（近战/战士技能/法术打怪/法术打人/
// 怪物打人/PvP/火墙），所以抽成两个助手逐个套 —— 加新伤害途径时**别忘了它**。
//
// ⚠️ 中毒自己的周期伤害**不套**：原版绿毒走 `DamageHealth`（HP 级），
// 不经过 StruckDamage。否则红毒会让绿毒自我放大。
func (s *Server) struckPlayer(v *Player, dmg uint32, now time.Time) uint32 {
	if v == nil || v.Obj == nil {
		return dmg
	}
	return uint32(combat.AdjustStruck(v.Obj, int(dmg), now))
}

func (s *Server) struckMonster(m *entity.Monster, dmg uint32, now time.Time) uint32 {
	if m == nil || m.Object == nil {
		return dmg
	}
	// 挨打会让"肉质量"掉（原版 `TAnimalObject.Struck`：`Dec(m_nMeatQuality, Random(300))`，
	// ObjBase.pas:2807-2811）。魔法那条路另有一份 `dmg * 1000`，见 spell.go。
	s.hitMonsterMeat(m, dmg, false)
	return uint32(combat.AdjustStruck(m.Object, int(dmg), now))
}

// playerByActorID 按 ActorId 找在线玩家（中毒伤害要记到施毒者头上）。
func (s *Server) playerByActorID(id uint32) *Player {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.world.players[id]
}

// playerAtCell 找站在 (x,y) 的玩家（施毒术/心灵启示都可以对玩家放）。
func (s *Server) playerAtCell(m interface{ Name() string }, x, y int, except uint32) *Player {
	_ = m
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.world.players {
		if p.Obj != nil && p.Obj.ID != except && p.Obj.PosX() == x && p.Obj.PosY() == y {
			return p
		}
	}
	return nil
}

// makePoison 施加中毒（原版 TBaseObject.MakePosion）。
//
// 语义要点：
//   - 时长**取长**（已中毒时只延长不缩短）；
//   - 点数**覆盖**（原版 `m_btGreenPoisoningPoint := nPoint` 无条件）；
//   - 只在"状态位发生变化"时广播 `SM_CHARSTATUSCHANGED`；
//   - 对玩家额外发一条"你中毒了"（原版 `sYouPoisoned` 的 SysMsg）。
func (s *Server) makePoison(o *entity.Object, ptype, seconds int, point uint32, src *Player, now time.Time) bool {
	if o == nil || seconds <= 0 {
		return false
	}
	var srcID uint32
	if src != nil && src.Obj != nil {
		srcID = src.Obj.ID
	}
	changed := o.ApplyPoison(ptype, seconds, point, srcID, now)

	victim := s.playerByActorID(o.ID)
	if changed && victim != nil {
		// 状态位变了才广播（原版 `if nOldCharStatus <> m_nCharStatus then StatusChanged()`）
		s.broadcastStatus(victim)
	}
	if victim != nil {
		s.sysMsg(victim.conn, fmt.Sprintf("你中毒了(%d 秒, %d 点)", seconds, point))
	}
	kind := map[int]string{
		entity.PoisonDecHealth:   "绿毒",
		entity.PoisonDamageArmor: "红毒",
	}[ptype]
	who := "系统"
	if src != nil {
		who = src.Char.Name
	}
	log.Printf("%s 使 %s 中了%s：%d 秒、每跳 %d 点", who, o.Name, kind, seconds, point)
	return true
}

// poisonTickLoop 推进中毒与"心灵启示"的到期（每 0.5 秒游戏时间一次）。
//
// ⚠️ 为什么单独一条循环：原版在每个对象的 Run 里按 `m_dwPoisoningTick` 结算，
// 我们的玩家是每秒一条循环（regenLoop）、怪物是 monsterTickInterval，
// 都不能保证 2.5 秒的整数倍 ⇒ 单独一条 0.5 秒粒度的循环最省事也最准。
func (s *Server) poisonTickLoop() {
	t := time.NewTicker(tickDur(500 * time.Millisecond))
	defer t.Stop()
	for now := range t.C {
		s.poisonTick(now)
	}
}

// poisonTick 一次中毒/血条推进。锁的用法与 wallBurn 一致：锁内改血、锁外发包。
func (s *Server) poisonTick(now time.Time) {
	type monHit struct {
		m              *entity.Monster
		id             uint32
		hp, maxHP, dmg uint32
		died           bool
		src            *Player
	}
	type plHit struct {
		p             *Player
		hp, maxHP, mp uint32
		dmg           uint32
		src           *Player
	}

	var monHits []monHit
	var plHits []plHit
	var plStatus []*Player           // 状态位需要广播的玩家
	var showExpired []*entity.Object // 血条到期（玩家与怪物都有）

	s.mu.Lock()
	// ---- 怪物 ----
	for _, mon := range s.world.monsters {
		if mon == nil || mon.IsDead() || mon.IsNPC {
			continue
		}
		expiredTick := false
		// ⚠️ 中毒状态经 `entity.Object` 的访问器读写（自带锁，见 entity/object.go）：
		// 施毒的是**别人**的 goroutine，结算是这里的 ticker。
		if mon.PoisonActive(entity.PoisonDecHealth, now) {
			if !now.Before(mon.PoisonTickAt()) {
				dmg := mon.PoisonPoint() + 1 // 原版 DamageHealth(m_btGreenPoisoningPoint + 1)
				mon.WithPoison(func(ps *entity.PoisonState) {
					ps.TickAt = now.Add(poisonDecHealthTime)
				})
				h := monHit{m: mon, id: mon.ID, maxHP: mon.MaxHP, dmg: dmg}
				_, h.hp, h.died = mon.Hurt(dmg)
				if src := s.world.players[mon.PoisonSrcID()]; src != nil {
					h.src = src
				}
				monHits = append(monHits, h)
			}
		} else if !mon.PoisonUntil(entity.PoisonDecHealth).IsZero() {
			// 绿毒到期：清掉（原版由 m_wStatusTimeArr 倒数到 0 自然结束）
			mon.WithPoison(func(ps *entity.PoisonState) {
				ps.Until[entity.PoisonDecHealth] = time.Time{}
				ps.SrcID = 0
			})
			expiredTick = true
		}
		if !mon.PoisonActive(entity.PoisonDamageArmor, now) &&
			!mon.PoisonUntil(entity.PoisonDamageArmor).IsZero() {
			mon.WithPoison(func(ps *entity.PoisonState) {
				ps.Until[entity.PoisonDamageArmor] = time.Time{}
			})
			expiredTick = true
		}
		if expiredTick {
			log.Printf("%s 的中毒已解除", mon.Name)
		}
		// 心灵启示到期（原版对**所有**对象都在 Run 里判，ObjBase.pas:4029-4031）
		if until := mon.ShowHPUntil(); !until.IsZero() && !now.Before(until) {
			mon.SetShowHPUntil(time.Time{})
			showExpired = append(showExpired, mon.Object)
		}
	}

	// ---- 玩家 ----
	for _, p := range s.world.players {
		if p == nil || p.Obj == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
			continue
		}
		if p.Obj.PoisonActive(entity.PoisonDecHealth, now) && !now.Before(p.Obj.PoisonTickAt()) {
			dmg := p.Obj.PoisonPoint() + 1
			p.Obj.WithPoison(func(ps *entity.PoisonState) {
				ps.TickAt = now.Add(poisonDecHealthTime)
			})
			// ⚠️ 这条跑在 ticker goroutine 上（中毒计时在每秒循环里走），而被毒者
			// 自己的 goroutine 同时在吃药/挨打 ⇒ 判定 + 扣减同锁（见 statelock.go）
			hp, dmg := p.hurt(dmg)
			h := plHit{p: p, hp: hp, maxHP: p.maxHP(), mp: p.mp(), dmg: dmg}
			if src := s.world.players[p.Obj.PoisonSrcID()]; src != nil {
				h.src = src
			}
			plHits = append(plHits, h)
		}
		if !p.Obj.AnyPoisonActive(now) && !p.Obj.PoisonUntil(entity.PoisonDecHealth).IsZero() {
			// 全解毒：广播一次状态位（客户端据此去掉中毒颜色）
			p.Obj.WithPoison(func(ps *entity.PoisonState) {
				ps.Until[entity.PoisonDecHealth] = time.Time{}
				ps.Until[entity.PoisonDamageArmor] = time.Time{}
				ps.SrcID = 0
			})
			plStatus = append(plStatus, p)
			log.Printf("%s 的中毒已解除", p.Char.Name)
		}
		// 心灵启示到期（原版 BreakOpenHealth，ObjBase.pas:4029-4031）
		if until := p.Obj.ShowHPUntil(); !until.IsZero() && !now.Before(until) {
			p.Obj.SetShowHPUntil(time.Time{})
			showExpired = append(showExpired, p.Obj)
		}
	}
	s.mu.Unlock()

	// ---- 锁外发包 ----
	for _, h := range plHits {
		s.sendHealthChanged(h.p, h.p.Obj.ID, h.hp, h.mp, h.maxHP)
		s.broadcastToViewers(h.p.Obj.MapRef(), h.p.Obj.PosX(), h.p.Obj.PosY(), func(o *Player) {
			if o.visible.Contains(h.p.Obj.ID) || o == h.p {
				s.sendStruck(o, playerIDOf(h.src), h.p.Obj.ID, h.hp, h.maxHP, h.dmg)
			}
		})
		s.sysMsg(h.p.conn, fmt.Sprintf("你受到毒伤 %d 点", h.dmg))
		log.Printf("%s 受到毒伤 %d（HP %d/%d）", h.p.Char.Name, h.dmg, h.hp, h.maxHP)
		if h.hp == 0 {
			s.killPlayerByPoison(h.p, h.src)
		}
	}
	for _, h := range monHits {
		s.broadcastToViewers(h.m.MapRef(), h.m.PosX(), h.m.PosY(), func(o *Player) {
			if o.visible.Contains(h.id) {
				s.sendStruck(o, playerIDOf(h.src), h.id, h.hp, h.maxHP, h.dmg)
			}
		})
		log.Printf("%s 受到毒伤 %d（HP %d/%d）", h.m.Name, h.dmg, h.hp, h.maxHP)
		if !h.died {
			continue
		}
		s.mu.Lock()
		s.keepCorpseOrRemove(h.m)
		s.mu.Unlock()
		if h.src != nil {
			s.killMonsterBy(h.src, h.m, uint16(magicAmyOunsul), h.m.PosX(), h.m.PosY())
		}
		log.Printf("%s 被毒死（施毒者 %s）", h.m.Name, nameOrNone(h.src))
	}
	for _, p := range plStatus {
		s.broadcastStatus(p)
	}
	for _, o := range showExpired {
		s.closeHealth(o)
	}
}

// nameOrNone 给日志用。
func nameOrNone(p *Player) string {
	if p == nil {
		return "无"
	}
	return p.Char.Name
}

// killPlayerByPoison 玩家被毒死（没有直接的"击杀者"时按无 PK 罪处理）。
func (s *Server) killPlayerByPoison(p *Player, src *Player) {
	if p == nil {
		return
	}
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(o *Player) {
		if o.visible.Remove(p.Obj.ID) {
			s.sendDeathTo(o, p.Obj.ID, p.Obj.PosX(), p.Obj.PosY(), p.Obj.Facing(), 0)
		}
	})
	s.send(p.conn, proto.SM_NOWDEATH, int32(p.Obj.ID),
		uint16(p.Obj.PosX()), uint16(p.Obj.PosY()), uint16(p.Obj.Facing()), "")
	if src != nil {
		log.Printf("%s 被 %s 的毒毒死", p.Char.Name, src.Char.Name)
	} else {
		log.Printf("%s 被毒死", p.Char.Name)
	}
	s.revive(p.conn, p, src)
}

// openHealth 开目标血条并广播（原版 MakeOpenHealth，ObjBase.pas:3606-3612）。
//
//	SendRefMsg(RM_OPENHEALTH, 0, HP, MaxHP, 0, '')  ⇒ 客户端 SM_OPENHEALTH(1100)
//	（ClMain.pas:4283：`Recog=ActorId, Param=HP, Tag=MaxHP` ⇒ 在该 Actor 头上画血条）
func (s *Server) openHealth(o *entity.Object, hp, maxHP uint32, until time.Time) {
	if o == nil {
		return
	}
	o.SetShowHPUntil(until)
	s.broadcastToViewers(o.MapRef(), o.PosX(), o.PosY(), func(other *Player) {
		if other.visible.Contains(o.ID) || other.Obj == o {
			s.send(other.conn, proto.SM_OPENHEALTH, int32(o.ID),
				uint16(hp), uint16(maxHP), 0, "")
		}
	})
}

// closeHealth 关目标血条（原版 BreakOpenHealth，ObjBase.pas:3595-3604）。
func (s *Server) closeHealth(o *entity.Object) {
	if o == nil {
		return
	}
	o.SetShowHPUntil(time.Time{})
	s.broadcastToViewers(o.MapRef(), o.PosX(), o.PosY(), func(other *Player) {
		if other.visible.Contains(o.ID) || other.Obj == o {
			s.send(other.conn, proto.SM_CLOSEHEALTH, int32(o.ID), 0, 0, 0, "")
		}
	})
}

// poisonResisted 是抗毒判定（Magic.pas:364 `Random(抗毒 + 7) <= 6` ⇒ 不抵抗）。
//
// 抗毒 0（怪物表没有这一列、玩家没戴抗毒装备）⇒ `Random(7) ∈ [0,6]` 恒不抵抗
// ⇒ 必中；抗毒越大越难中（抗毒 7 ⇒ 50%）。
func poisonResisted(anti int) bool {
	if anti < 0 {
		anti = 0
	}
	return delphi.Random(anti+poisonResistBase) > poisonResistMax
}

// poisonTickPoint 是"每跳伤害"：`ROUND(等级/3 * (nPower / AmyOunsulPoint))`。
//
// ⚠️ 原版把 nPower 当**秒数**传进 MakePosion（`nTime := nPower`），
// 这个 ROUND 出来的值才是每跳扣血量（`m_btGreenPoisoningPoint`）。
// OpenMir2 的怪物实现 `MakePosion(DECHEALTH, 60, 3)` 可作旁证。
func poisonTickPoint(level uint32, nPower int) int {
	if amyOunsulPoint <= 0 {
		return 0
	}
	return int(float64(level)/3*float64(nPower)/float64(amyOunsulPoint) + 0.5)
}

// spellTarget 解析施法目标（怪物优先，其次玩家）。
//
// 原版 `DoSpell` 的分派是按坐标取 `IsProperTarget` 的对象（怪物与玩家同一条路）；
// 我们的 CM_SPELL 只带坐标（Series 装不下 ActorId），所以两边都要试。
func (s *Server) spellTarget(p *Player, mp *world.Map, targX, targY int, targID uint32) (*entity.Monster, *Player) {
	s.mu.RLock()
	mon := s.findMonsterAt(mp, targX, targY, targID)
	s.mu.RUnlock()
	if mon != nil {
		return mon, nil
	}
	return nil, s.playerAtCell(nil, targX, targY, p.Obj.ID)
}

// castPoison 施毒术(6)（Magic.pas:355-382）。
//
//	CheckAmulet(1, 2) 已在通用步骤里做过（amuletNeed 里 6 ⇒ 毒药 1 个），
//	这里拿到**被消耗那件毒药**的 Shape：1 = 绿毒、2 = 红毒。
//
//	nPower := GetPower13(40 | 30) + GetRPow(SC) * 2
//	nTime  := nPower                                             ← 原版把 nPower 当"秒数"
//	nPoint := ROUND(等级 / 3 * (nPower / AmyOunsulPoint))         ← 每跳伤害
//	抗毒   ：Random(抗毒 + 7) <= 6 才生效
func (s *Server) castPoison(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic,
	targX, targY int, targID uint32, shape uint16) {

	mp := p.Obj.MapRef()
	mon, victim := s.spellTarget(p, mp, targX, targY, targID)
	if mon == nil && victim == nil {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的施毒术没有目标（%d,%d）", p.Char.Name, targX, targY)
		return
	}

	// 抗毒：怪物表没有抗毒列 ⇒ 取 0（`Random(7) <= 6` 恒真 ⇒ 必中）；
	// 玩家取装备汇总（itemabil.go 的 wAntiPoison —— 这正是此前"只下发不使用"的那一项）。
	anti := 0
	if victim != nil {
		anti = s.playerAddAbil(victim).antiPoison
	}
	if poisonResisted(anti) {
		who := ""
		if mon != nil {
			who = mon.Name
		} else {
			who = victim.Char.Name
		}
		s.sysMsg(c, "目标抵抗了你的毒")
		log.Printf("%s 的施毒术被 %s 抵抗（抗毒 %d）", p.Char.Name, who, anti)
		return
	}

	var nPower int
	switch shape {
	case 1: // 绿毒
		nPower = magic.GetPower13(40, info, um.Level) + magic.GetRPow(playerPowerAttr(p, magic.PowerAttrSC))*2
	case 2: // 红毒
		nPower = magic.GetPower13(30, info, um.Level) + magic.GetRPow(playerPowerAttr(p, magic.PowerAttrSC))*2
	default:
		// 原版 case 里只有 1/2；别的 Shape 过了 CheckAmulet（<=2）但没有分支 ⇒ 不生效
		log.Printf("%s 的施毒术：毒药 Shape=%d 无对应分支", p.Char.Name, shape)
		return
	}
	if nPower < 1 {
		nPower = 1
	}
	seconds := nPower // 原版：nTime := nPower
	point := poisonTickPoint(um.Level, nPower)
	ptype := entity.PoisonDecHealth
	if shape == 2 {
		ptype = entity.PoisonDamageArmor
	}

	now := time.Now()
	if mon != nil {
		s.makePoison(mon.Object, ptype, seconds, uint32(point), p, now)
	} else {
		s.makePoison(victim.Obj, ptype, seconds, uint32(point), p, now)
	}
}

// castShowHP 心灵启示(28)（Magic.pas:522-531）。
//
//	if (目标 <> nil) and not 目标.m_boShowHP then
//	  if Random(6) <= 等级 + 3 then
//	    目标.m_dwShowHPTick     := GetTickCount();
//	    目标.m_dwShowHPInterval := GetPower13(GetRPow(SC) * 2 + 30) * 1000;
//	    目标.SendDelayMsg(..., RM_DOOPENHEALTH, ..., 1500)   ← 1.5 秒后开血条
//
// 我们不做那 1.5 秒延迟（原版是为了配合施法动画）：直接开，语义一样。
func (s *Server) castShowHP(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic,
	targX, targY int, targID uint32) {

	mon, victim := s.spellTarget(p, p.Obj.MapRef(), targX, targY, targID)
	if mon == nil && victim == nil {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的心灵启示没有目标（%d,%d）", p.Char.Name, targX, targY)
		return
	}
	now := time.Now()
	var o *entity.Object
	var hp, maxHP uint32
	if mon != nil {
		o, hp, maxHP = mon.Object, mon.HP, mon.MaxHP
	} else {
		o, hp, maxHP = victim.Obj, victim.hp(), victim.maxHP()
	}
	if now.Before(o.ShowHPUntil()) {
		log.Printf("%s 的心灵启示：%s 的血条已经开着", p.Char.Name, o.Name)
		return
	}
	if delphi.Random(6) > int(um.Level)+3 {
		s.sysMsg(c, "心灵启示失败")
		log.Printf("%s 的心灵启示失败（Random(6) > 等级 %d + 3）", p.Char.Name, um.Level)
		return
	}
	ms := magic.GetPower13(magic.GetRPow(playerPowerAttr(p, magic.PowerAttrSC))*2+30, info, um.Level)
	if ms < 1 {
		ms = 1
	}
	s.openHealth(o, hp, maxHP, now.Add(time.Duration(ms)*time.Second))
	log.Printf("%s 的心灵启示：%s 的血条打开 %d 秒（%d/%d）", p.Char.Name, o.Name, ms, hp, maxHP)
	s.sysMsg(c, fmt.Sprintf("心灵启示：%s 的HP为 %d/%d", o.Name, hp, maxHP))
}

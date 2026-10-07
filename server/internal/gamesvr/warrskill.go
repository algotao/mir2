package gamesvr

import (
	"log"
	"math/rand/v2"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/world"
)

// 战士近战系（攻杀/刺杀/半月/烈火/野蛮）的**攻击侧**实现。
//
// ⚠️ 这一族技能在原版里**不在 `Magic.pas` 的 DoSpell 里**——`Magic.pas:264`
// 明确 `if IsWarrSkill(...) then exit`。它们走的是另一条路：
//
//	客户端按技能键 → CM_SPELL(SKILL) → ObjBase.pas:9029 的 case（只做开关/充能）
//	客户端挥砍     → CM_HIT / CM_LONGHIT / CM_WIDEHIT … → AttackDir(wHitMode)
//	                                                      → _Attack 里按模式选目标
//
// 也就是说**模式来自攻击包的消息号**（不是 Tag 里的字段）：
//
//	CM_HIT(3014)     → wHitMode 0  普通
//	CM_HEAVYHIT(3015)→ 1  重击（动画不同，伤害同普通）
//	CM_BIGHIT(3016)  → 2  大力（同上）
//	CM_POWERHIT(3018)→ 3  攻杀剑术（消耗 m_boPowerHit，加 m_nHitPlus）
//	CM_LONGHIT(3019) → 4  刺杀剑术（打**前方第二格**）
//	CM_WIDEHIT(3024) → 5  半月弯刀（打**三个格子**，且要先扣蓝）
//
// 本轮实现 4 / 5（外加 1 / 2 的动画差异）；3 / 7 / 9 需要三个"充能"标志，
// 见文件末尾的说明。

// 攻击模式编号（对应 AttackDir 的 wHitMode）。
const (
	hitNormal = 0
	hitHeavy  = 1 // 重击
	hitBig    = 2 // 大力
	hitPower  = 3 // 攻杀剑术（消费充能标志 m_boPowerHit）
	hitLong   = 4 // 刺杀剑术
	hitWide   = 5 // 半月弯刀
	hitFire   = 7 // 烈火剑法（消费充能标志 m_boFireHitSkill）
)

// hitModeOf 把攻击包的消息号翻成攻击模式（ObjBase.pas:8851-8856）。
func hitModeOf(ident uint16) int {
	switch ident {
	case proto.CM_HEAVYHIT:
		return hitHeavy
	case proto.CM_BIGHIT:
		return hitBig
	case proto.CM_POWERHIT:
		return hitPower
	case proto.CM_LONGHIT:
		return hitLong
	case proto.CM_WIDEHIT:
		return hitWide
	case proto.CM_FIREHIT:
		return hitFire
	}
	return hitNormal
}

// swingIdent 返回挥砍动画（`AttackDir` 末尾 `case wHitMode of`，ObjBase.pas:18845-18856）。
//
// ⚠️ 学没学会技能会影响动画：没学会刺杀/半月时原版把 wIdent 保持为 RM_HIT，
// 所以"没学技能却按了远攻键"在客户端看到的仍是普通挥砍。
//
// ⚠️ 攻杀(3)/烈火(7) 还要看**出刀前的充能快照**（`boPowerHit`/`boFireHit`，
// ObjBase.pas:18826-18827）：没充能时同样退回普通挥砍动画，标志则在 `_Attack`
// 里被消费掉（所以"这一刀有没有特效"与"这一刀吃没吃到加成"用的是同一个快照）。
func swingIdent(mode int, hasErgum, hasBanwol, powerHit, fireHit bool) uint16 {
	switch mode {
	case hitHeavy:
		return proto.SM_HEAVYHIT
	case hitBig:
		return proto.SM_BIGHIT
	case hitPower:
		if powerHit {
			// RM_SPELL2 → SM_POWERHIT（ObjBase.pas:5354）
			return proto.SM_POWERHIT
		}
	case hitFire:
		// RM_FIREHIT → SM_FIREHIT（ObjBase.pas:5390）
		if fireHit {
			return proto.SM_FIREHIT
		}
	case hitLong:
		if hasErgum {
			return proto.SM_LONGHIT
		}
	case hitWide:
		if hasBanwol {
			return proto.SM_WIDEHIT
		}
	}
	return proto.SM_HIT
}

// ---------- 攻杀剑术(7) / 烈火剑法(26) 的"充能"机制 ----------
//
// 这两个技能都不是"按下就打"，而是先攒出一次强化攻击：
//
//	攻杀剑术：**服务端自动积累**（ObjBase.pas:8863-8876，紧跟攻击包分派之后）。
//	          每挥一刀 `Dec(m_btAttackSkillCount)`，计数落到
//	          `m_btAttackSkillPointCount` 时置 `m_boPowerHit` 并通知客户端
//	          （`SendSocket(nil, '+PWR')`）；count <= 0 时重掷下一轮。
//	          初始值来自 RecalcAbilitys（学技能/升级时）：
//	          `count := 7 - btLevel`、`point := Random(count)`。
//	          消费：下一次 mode=3 的攻击 `Inc(nPower, m_nHitPlus)`，
//	          其中 `m_nHitPlus := DEFHIT + btLevel`（加了攻杀才非 0）。
//
//	烈火剑法：按技能键（CM_SPELL 26）**主动点燃**：10 秒冷却
//	          （AllowFireHitSkill，ObjBase.pas:9784），点燃即 `m_boFireHitSkill := True`；
//	          20 秒内不打出去就作废（ObjBase.pas:6427 的 Run 分支）。
//	          消费：下一次 mode=7 的攻击
//	          `nPower += ROUND(nPower/100 * (m_nHitDouble*10))`，
//	          其中 `m_nHitDouble := 4 + btLevel*4` ⇒ 0 级 +40%、3 级 +160%。
//
// ⚠️ 两处容易做错的地方：
//
//  1. 标志在 `AttackDir` 开头**快照**、在 `_Attack` 里消费。动画看快照，
//     所以"这一刀有没有特效"必须用**出刀前**的状态。
//  2. **打空也消费**（原版 `AttackTarget = nil` 分支同样清标志；烈火还会刷新
//     `m_dwLatestFireHitTick`，注释原文"Jacky 防止砍空刀刀烈火"）。
//     所以消费必须发生在**找目标之前**。

// powerHitCycleBase 是攻杀充能周期的基数（ObjBase.pas:8866 `7 - btLevel`）。
const powerHitCycleBase = 7

// fireHitArmGap 是烈火的点燃冷却（AllowFireHitSkill：10 秒内不能重复点燃）。
//
// ⚠️ 它是**节奏类**时间，实际比较时过 `tscale.D`（同施法冷却）。
const fireHitArmGap = 10 * time.Second

// fireHitLife 是点燃后的有效期（ObjBase.pas:6427：超过 20 秒作废）。
//
// ⚠️ 它是**状态时长**，**不**随倍速缩放（同增益持续时间）。
const fireHitLife = 20 * time.Second

// powerHitPlus 是攻杀的加攻（RecalcAbilitys：`m_nHitPlus := DEFHIT + btLevel`）。
//
// 没学（或已学会但等级 0）时分别是 0 / DEFHIT——注意原版**学了就有**（0 级 +5）。
func powerHitPlus(p *Player) int {
	um := userMagicOf(p, 7)
	if um == nil {
		return 0
	}
	return entity.DefHit + int(um.Level)
}

// fireHitPercent 是烈火的加成百分比
// （RecalcAbilitys：`m_nHitDouble := 4 + btLevel*4`，命中时乘 `nHitDouble*10`）。
func fireHitPercent(p *Player) int {
	um := userMagicOf(p, 26)
	if um == nil {
		return 0
	}
	return (4 + int(um.Level)*4) * 10
}

// warBonus 是一次攻击的"充能加成"，由 consumeWarrCharge 产出、打完即失效。
type warBonus struct {
	plus    int // 攻杀：加攻（作用在**未减防**的威力上）
	firePct int // 烈火：百分比加成
}

// apply 把加成作用在未减防的威力上（原版把 nPower 改完再进减防）。
func (b warBonus) apply(power int) int {
	if b.plus != 0 {
		power += b.plus
	}
	if b.firePct != 0 {
		power += delphi.Round(float64(power) * float64(b.firePct) / 100)
	}
	return power
}

// isZero 报告这次攻击有没有吃到充能加成。
func (b warBonus) isZero() bool { return b.plus == 0 && b.firePct == 0 }

// name 是用于日志/事件的技能名。
func (b warBonus) name() string {
	switch {
	case b.plus != 0:
		return "攻杀剑术"
	case b.firePct != 0:
		return "烈火剑法"
	}
	return ""
}

// consumeWarrCharge 消费这一刀对应的充能标志并返回加成。
//
// ⚠️ 必须在**找目标之前**调用（打空也消费，见文件内说明）。
func (s *Server) consumeWarrCharge(p *Player, mode int) warBonus {
	var b warBonus
	switch mode {
	case hitPower:
		if p.powerHit {
			p.powerHit = false
			b.plus = powerHitPlus(p)
		}
	case hitFire:
		// "Jacky 禁止双烈火"：消费即刷新计时（ObjBase.pas:22131）——
		// 这一步与 ticker 的"到期作废"共用 fireMu（见 statelock.go 第二节）。
		if p.consumeFireCharge(time.Now()) {
			b.firePct = fireHitPercent(p)
		}
	}
	return b
}

// chargeAfterAttack 是每次挥砍后的攻杀充能（ObjBase.pas:8863-8876）。
//
// 条件：学过攻杀剑术 + **武器耐久 > 0**。
func (s *Server) chargeAfterAttack(p *Player) {
	um := userMagicOf(p, 7)
	if um == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	if w := s.equipAt(p, 1); w == nil || w.Dura == 0 {
		return
	}
	if !p.powerHitInit {
		// 对应 RecalcAbilitys 里的初始化（学技能 / 升级时重算）
		p.powerHitInit = true
		p.powerHitCount = powerHitCycleBase - int(um.Level)
		p.powerHitPoint = delphi.Random(p.powerHitCount)
	}
	p.powerHitCount--
	if p.powerHitPoint == p.powerHitCount {
		p.powerHit = true
		// 原版此处 `SendSocket(nil, '+PWR')` 通知客户端"充能好了"（客户端据此
		// 改发 CM_POWERHIT）。这条 raw 通道我们不下发，见 docs/progress.md §2。
		log.Printf("%s 的攻杀剑术已充能（第 %d 刀，加攻 +%d）",
			p.Char.Name, p.powerHitPoint, powerHitPlus(p))
	}
	if p.powerHitCount <= 0 {
		p.powerHitCount = powerHitCycleBase - int(um.Level)
		p.powerHitPoint = delphi.Random(p.powerHitCount)
	}
}

// armFireHit 处理"按下烈火剑法技能键"（AllowFireHitSkill，ObjBase.pas:9784-9791）。
//
// 原版顺序值得照抄：**先点燃（并刷新 10 秒计时）再判蓝**——蓝不够时不扣蓝、
// 也不通知客户端，但标志已经置位了。这里同样保留（只补一句日志说明）。
func (s *Server) armFireHit(c net.Conn, p *Player, um *pb.UserMagic, info *data.MagicInfo) {
	now := time.Now()
	// ⚠️ 点燃冷却是**节奏类**时间，跟倍速缩放（同施法冷却）；
	// 而下面的 20 秒有效期是**状态时长**，不缩放（同增益持续时间的约定，
	// 见 buff.go 的 addBuffFor 注释）。
	//
	// "判冷却 + 置位"必须是**一次原子操作**：tickWarrCharge 在 ticker 上同时
	// 读写这两个字段（见 statelock.go 第二节）。
	if !p.armFireCharge(now, tscale.D(fireHitArmGap)) {
		// String.ini `FireSpiritsFail`
		s.sysMsg(c, "凝结内力失败")
		log.Printf("%s 点燃烈火剑法被拒：还在 %.1f 秒冷却内（原版回\"凝结内力失败\"）",
			p.Char.Name, tscale.D(fireHitArmGap).Seconds())
		return
	}
	// String.ini `FireSpiritsSummoned`
	s.sysMsg(c, "您的武器因精神火球而炙热...")

	cost := magic.SpellPoint(info, um)
	abil := p.Char.Data.Abil
	if abil.Mp < cost {
		log.Printf("%s 点燃烈火剑法但 MP 不足（需 %d，有 %d）：标志已置位、未扣蓝（原版如此）",
			p.Char.Name, cost, abil.Mp)
		return
	}
	if cost > 0 {
		p.addMP(-int64(cost)) // 扣蓝（持锁）
		s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)
	}
	log.Printf("%s 点燃烈火剑法（技能等级 %d，加成 +%d%%，MP-%d）",
		p.Char.Name, um.Level, fireHitPercent(p), cost)
}

// warrSkillKey 处理"按下战士近战技能键"（原版 DoSpell 里 IsWarrSkill 的那一支）。
//
// 返回 true 表示已处理：调用方直接返回，**不再走通用施法流程**（不扣通用 MP、
// 不广播 SM_SPELL、不练技）——原版这些都不发生（`IsWarrSkill` 直接 exit）。
//
// 当前接了两个：
//
//	烈火剑法(26)：点燃（10 秒冷却 + 20 秒有效期）
//	攻杀剑术(7)：**按键什么都不做**，充能完全由服务端按挥砍次数自动积累
//	野蛮冲撞(27)：3 秒冷却 + 扣蓝 → 把正前方目标推开并自己跟进去
//
// 原版 IsWarrSkill 的完整名单（Magic.pas:208-211）：
//
//	3 基本剑术、4 精神力战法、7 攻杀剑术、12 刺杀剑术、
//	25 半月弯刀、26 烈火剑法、27 野蛮冲撞（+ CROSSMOON/TWINBLADE）
//
// 其中 12/25 的按键在战斗侧接（走 CM_LONGHIT/CM_WIDEHIT 攻击包，见 handleAttack），
// 3/4 是纯被动（加命中，见 hitpoint.go）。
func (s *Server) warrSkillKey(c net.Conn, p *Player, magicID uint32, um *pb.UserMagic,
	info *data.MagicInfo, dir int) bool {
	switch magicID {
	case 26: // 烈火剑法
		s.armFireHit(c, p, um, info)
		return true
	case 27: // 野蛮冲撞
		s.castMotaebo(c, p, um, info, dir)
		return true
	case 7: // 攻杀剑术：纯充能，按键无副作用
		return true
	case 3, 4: // 基本剑术 / 精神力战法：纯被动（加命中），按键无副作用
		return true
	}
	return false
}

// trainWarrCharge 是"消费充能并命中"之后的练技（`_Attack` 里 `bo21` 的两个分支）。
//
// ⚠️ 两个技能的增量**不一样**，照原版：
//
//	攻杀剑术（ObjBase.pas:22299）：`TrainSkill(..., Random(3) + 1)`
//	烈火剑法（ObjBase.pas:22355）：`TrainSkill(..., 1)`
func (s *Server) trainWarrCharge(c net.Conn, p *Player, mode int) {
	switch mode {
	case hitPower:
		if info := s.data.tables.Magics.GetByID(7); info != nil {
			if um := userMagicOf(p, 7); um != nil {
				s.trainMagicN(c, p, info, um, 1+rand.IntN(3))
			}
		}
	case hitFire:
		if info := s.data.tables.Magics.GetByID(26); info != nil {
			if um := userMagicOf(p, 26); um != nil {
				s.trainMagicN(c, p, info, um, 1)
			}
		}
	}
}

// tickWarrCharge 是充能的到期检查（原版挂在 Run 里，ObjBase.pas:6425-6433）。
//
// ⚠️ 它跑在 regenLoop 的 ticker goroutine 上，而点燃/消费发生在**玩家自己的
// goroutine** 上 ⇒ 状态位的读写走 `expireFireCharge`（持 fireMu），
// 发包在锁外（见 statelock.go 第二节）。
func (s *Server) tickWarrCharge(p *Player, now time.Time) {
	if !p.expireFireCharge(now, fireHitLife) {
		return
	}
	// String.ini `SpiritsGone`
	s.send(p.conn, proto.SM_SYSMESSAGE, 0, 0, 0, 0, "精神火球消失!")
	log.Printf("%s 的烈火剑法充能作废（点燃后 %s 没打出去）", p.Char.Name, fireHitLife)
}

// wideAttackDirs 是半月弯刀的三个方向偏移。
//
// 原版是配置项 `g_Config.WideAttack = (7, 1, 2)`（M2Share.pas:1645），
// OpenMir2 的 `GameSvrConf.cs:1330` 同样是 { 7, 1, 2 }。
//
// ⚠️ 它**不含正前方**（dir+0）。客户端的射程判定却要求"正前方有目标 + 弧形任一格
// 也有目标"（ClMain.pas:2019 `TargetInSwordWideAttackRange`）——服务端与客户端
// 不一致是原版如此，别"顺手修正"。
var wideAttackDirs = [3]int{7, 1, 2}

// swordLongPowerRate 是刺杀剑术的威力百分比（!setup.txt `SwordLongPowerRate=100`）。
const swordLongPowerRate = 100

// swordLongPower 是刺杀剑术的威力（_Attack，ObjBase.pas:22165）：
//
//	nSecPwr := ROUND(nPower / (btTrainLv + 2) * (btLevel + 2))
//	再乘 SwordLongPowerRate/100（在 SwordLongAttack 里，ObjBase.pas:22106）
//
// btTrainLv 硬编码 3 ⇒ 分母 5。所以 0 级只有 40% 威力、3 级才满威力。
func swordLongPower(nPower int, level uint32) int {
	p := delphi.Round(float64(nPower) / float64(entity.MagicMaxLevel+2) * float64(level+2))
	return delphi.Round(float64(p) * swordLongPowerRate / 100)
}

// swordWidePower 是半月弯刀的**每目标**威力（_Attack，ObjBase.pas:22194）：
//
//	nSecPwr := ROUND(nPower / (btTrainLv + 10) * (btLevel + 2))
//
// 分母 13 ⇒ 单目标很弱（0 级约 15%、3 级约 38%），但它一次打三格。
func swordWidePower(nPower int, level uint32) int {
	return delphi.Round(float64(nPower) / float64(entity.MagicMaxLevel+10) * float64(level+2))
}

// userMagicOf 取玩家已学会的某个技能（没学返回 nil）。
func userMagicOf(p *Player, id uint32) *pb.UserMagic {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return nil
	}
	for _, um := range p.Char.Data.Magics {
		if um != nil && um.MagicId == id {
			return um
		}
	}
	return nil
}

// rollAttack 掷出**未减防**的攻击力（原版 `GetAttackPower`，ObjBase.pas:2416-2437）。
//
// 与 rollDamage 拆开是因为刺杀/半月要"先按技能等级缩放威力、再减目标防御"。
//
// luck 是攻击者的 `m_nLuck`（幸运为正、诅咒为负，见 `playerLuck`）：
//
//	if m_nLuck > 0 then
//	  if Random(10 - _MIN(9, m_nLuck)) = 0 then Result := nBasePower + nPower   // 直接取上限
//	  else Result := nBasePower + Random(nPower + 1)
//	else
//	  Result := nBasePower + Random(nPower + 1);
//	  if (m_nLuck < 0) and (Random(10 - _MAX(0, -m_nLuck)) = 0) then Result := nBasePower;  // 取下限
//
// ⚠️ 幸运 ≥ 9 时 `Random(1) = 0` **恒成立** ⇒ 每一刀都是上限伤害（反之诅咒 ≤ −9 恒下限）。
// 这是原版"幸运必出上限"的确切语义，也是 e2e 能确定性断言的原因。
func rollAttack(minAtk, maxAtk uint32, luck int) int {
	nPower := int(maxAtk) - int(minAtk)
	if nPower < 0 {
		nPower = 0
	}
	if luck > 0 {
		if delphi.Random(10-minInt(9, luck)) == 0 {
			return int(minAtk) + nPower // 上限
		}
	} else if luck < 0 {
		if delphi.Random(10-maxInt(0, -luck)) == 0 {
			return int(minAtk) // 下限
		}
	}
	if nPower > 0 {
		return int(minAtk) + delphi.Random(nPower+1)
	}
	return int(minAtk)
}

// minInt / maxInt 是给 `_MIN`/`_MAX` 用的极小助手（Go 1.21 起有内置 min/max，
// 这里显式写出以免读代码时误以为是新内建）。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// applyArmor 是物理减伤（原版 GetHitStruckDamage）：掷 AC 后相减，**保底 1**。
func applyArmor(power int, ac uint32) uint32 {
	acLo, acHi := uint32(proto.UnpackLo(ac)), uint32(proto.UnpackHi(ac))
	if acHi < acLo {
		acHi = acLo
	}
	armor := acLo
	if acHi > acLo {
		armor = acLo + uint32(delphi.Random(int(acHi-acLo+1)))
	}
	if power <= int(armor) {
		return 1
	}
	return uint32(power) - armor
}

// monsterAt2 / playerAtLocked 是 monsterAt / playerAt 的"调用方已持 s.mu"版本。
//
// ⚠️ 不能直接调 monsterAt/playerAt：它们内部取 s.mu.RLock，而 RWMutex
// **不可重入**（持 Lock 再 RLock 会死锁，本项目踩过）。
func (s *Server) monsterAt2(m *world.Map, x, y int) *entity.Monster {
	for _, mon := range s.world.monsters {
		if !mon.IsDead() && mon.MapRef() == m && mon.PosX() == x && mon.PosY() == y {
			return mon
		}
	}
	return nil
}

func (s *Server) playerAtLocked(m *world.Map, x, y int) *Player {
	for _, o := range s.world.index.InRange(x, y, 0) {
		q, ok := o.(*Player)
		if !ok || q.Obj.MapRef() != m || q.Obj.PosX() != x || q.Obj.PosY() != y {
			continue
		}
		if q.Char != nil && q.Char.Data != nil && q.Char.Data.Abil != nil &&
			q.hp() == 0 {
			continue
		}
		return q
	}
	return nil
}

// hitMonster 对怪物结算一次近战伤害并广播受击/死亡。
//
// 与 handleAttack 的单格路径保持同一套包序（先 SM_STRUCK，死亡再 SM_DEATH）；
// 复活/击杀结算复用 killMonsterBy（经验 + 掉落），避免三处各写一份。
func (s *Server) hitMonster(p *Player, m *entity.Monster, dmg uint32) {
	// 红毒：目标受伤放大（原版 StruckDamage）。战士技能的公共落点都在这里。
	dmg = s.struckMonster(m, dmg, time.Now())
	// 打了城堡单位 ⇒ 进 2 分钟仇恨窗口（原版 TGuardUnit.Struck，见 guard.go）
	s.markCastleAggro(p.Obj, m, time.Now())
	// 宠物跟打主人的目标（IsAttackTarget）
	p.combatTargetID = m.ID
	died := m.Damage(dmg)
	s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(other *Player) {
		if other.visible.Contains(m.ID) {
			s.sendStruck(other, m.ID, m.HP, m.MaxHP, dmg)
			obs.Event("attack_hit", "player", p.Char.Name, "monster", m.ID,
				"monster_name", m.Name, "dmg", dmg, "hp", m.HP, "max_hp", m.MaxHP)
		}
	})
	if !died {
		return
	}
	s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(other *Player) {
		if other.visible.Remove(m.ID) {
			s.send(other.conn, proto.SM_DEATH, int32(m.ID), uint16(m.PosX()), uint16(m.PosY()),
				uint16(m.Facing()), "")
		}
	})
	s.mu.Lock()
	s.keepCorpseOrRemove(m)
	s.mu.Unlock()

	log.Printf("%s 用近战技能击杀 %s (ActorId=%d) 伤害=%d 经验+%d",
		p.Char.Name, m.Name, m.ID, dmg, m.Info.Exp)
	s.killMonsterBy(p, m, 0, m.PosX(), m.PosY())
	s.wearWeapon(p)
}

// attackSwordLong 刺杀剑术：打**正前方第二格**（SwordLongAttack，ObjBase.pas:22101）。
//
//	威力 = ROUND(攻击力 / 5 * (技能等级+2)) * SwordLongPowerRate/100
//	目标 = GetNextPosition(自己, 面向, **2**) —— 第一格有没有东西**不影响**
//
// 返回值 = "这一刀算不算打出去了"：原版只要第二格**有对象**就返回 True
// （哪怕它不是合法目标），于是 `boLimitSwordLong` 的降级回普通挥砍不会触发；
// 只有第二格为空才返回 False。`!setup.txt` 里 `LimitSwordLong=0`，降级本就关着。
//
// ⚠️ 对**玩家**目标不套用威力缩放（直接走 attackPlayer 的普通近战路径）——
// 原版是要缩放的，这里属已知简化，见 warrskill 的收尾说明。
func (s *Server) attackSwordLong(p *Player, um *pb.UserMagic, dir uint8,
	minAtk, maxAtk uint32) bool {

	tx := p.Obj.PosX() + int(entity.DirDelta[dir][0])*2
	ty := p.Obj.PosY() + int(entity.DirDelta[dir][1])*2

	s.mu.RLock()
	victim := s.playerAtLocked(p.Obj.MapRef(), tx, ty)
	mon := s.monsterAt2(p.Obj.MapRef(), tx, ty)
	s.mu.RUnlock()

	if victim == nil && mon == nil {
		return false
	}
	if victim != nil {
		// mode 4/5 不吃充能加成（那是 mode 3/7 的事），传零值。
		s.attackPlayer(p.conn, p, victim, dir, warBonus{})
		return true
	}
	if mon.IsCastleUnit() && !s.canHitCastleUnit(p, mon) {
		return true
	}
	// 打空判定（与普通挥砍同一处，ObjBase.pas:22241）：刺杀同样要过命中/敏捷。
	if combat.Misses(s.playerHitPoint(p), combat.MonsterHitPoint(mon), combat.MonsterSpeedPoint(mon)) {
		log.Printf("%s 的刺杀剑术打空（命中 %d < Random(敏捷 %d)，目标 %s）",
			p.Char.Name, s.playerHitPoint(p), combat.MonsterSpeedPoint(mon), mon.Name)
		return true // 第二格有对象就算"打出去了"（原版这里返回 True）
	}
	power := swordLongPower(rollAttack(minAtk, maxAtk, s.playerLuck(p)), um.Level)
	if power <= 0 {
		return true
	}
	dmg := applyArmor(power, uint32(proto.PackMinMax(mon.Info.AC, mon.Info.AC)))
	// 命中日志：e2e 要能区分"没打中（第二格空）"与"技能没放"。
	log.Printf("%s 刺杀剑术命中 %s（技能等级 %d，威力 %d，伤害 %d）",
		p.Char.Name, mon.Name, um.Level, power, dmg)
	obs.Event("sword_long", "player", p.Char.Name, "monster", mon.Name,
		"power", power, "dmg", dmg, "level", int(um.Level))
	s.hitMonster(p, mon, dmg)
	return true
}

// attackSwordWide 半月弯刀：打三个格子（SwordWideAttack，ObjBase.pas:22113）。
//
//	① 先扣蓝（AttackDir 开头，ObjBase.pas:18789）：
//	   `btDefSpell + ROUND(wSpell/4*(level+1))`；原版只判 MP>0 就扣（会扣成 0），
//	   我们要求"够扣"，避免出现负蓝。
//	② 威力 = ROUND(攻击力 / 13 * (技能等级+2))，**每个目标各自减防**
//	③ 三个方向 = (面向+7)、(面向+1)、(面向+2)，各取相邻一格
//
// 返回值 = 有没有命中任何目标（用于决定动画号）。
func (s *Server) attackSwordWide(p *Player, um *pb.UserMagic, dir uint8,
	minAtk, maxAtk uint32) bool {

	abil := p.Char.Data.Abil
	info := s.data.tables.Magics.GetByID(25)
	if info == nil || abil.Mp == 0 {
		return false // 没蓝 ⇒ 原版退回普通挥砍（wHitMode := RM_HIT）
	}
	cost := magic.SpellPoint(info, um)
	if abil.Mp < cost {
		return false
	}
	p.addMP(-int64(cost)) // 扣蓝（持锁）
	s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)

	now := time.Now()
	hitAny := false
	hitCount := 0
	missed := 0 // 打空的目标数（只用于日志：辨"没打中"与"没放出来"）
	for _, off := range wideAttackDirs {
		d := uint8((int(dir) + off) % 8)
		tx := p.Obj.PosX() + int(entity.DirDelta[d][0])
		ty := p.Obj.PosY() + int(entity.DirDelta[d][1])

		s.mu.RLock()
		victim := s.playerAtLocked(p.Obj.MapRef(), tx, ty)
		mon := s.monsterAt2(p.Obj.MapRef(), tx, ty)
		s.mu.RUnlock()

		if victim != nil {
			// 见 attackSwordLong 的说明：对玩家不套威力缩放。
			if s.canAttackTarget(p, victim, attackModeOf(p), now) {
				s.attackPlayer(p.conn, p, victim, d, warBonus{})
			}
			hitAny = true
			continue
		}
		if mon == nil || (mon.IsCastleUnit() && !s.canHitCastleUnit(p, mon)) {
			continue
		}
		// 打空判定：每个目标各判一次（原版 `_Attack` 就是这么走的）
		if combat.Misses(s.playerHitPoint(p), combat.MonsterHitPoint(mon), combat.MonsterSpeedPoint(mon)) {
			missed++
			continue
		}
		power := swordWidePower(rollAttack(minAtk, maxAtk, s.playerLuck(p)), um.Level)
		if power <= 0 {
			continue
		}
		s.hitMonster(p, mon, applyArmor(power, uint32(proto.PackMinMax(mon.Info.AC, mon.Info.AC))))
		hitAny = true
		hitCount++
	}
	log.Printf("%s 的半月弯刀命中 %d 个目标、打空 %d 个（技能等级 %d，MP-%d，方向 %d）",
		p.Char.Name, hitCount, missed, um.Level, cost, dir)
	obs.Event("sword_wide", "player", p.Char.Name, "dir", int(dir),
		"hit", hitAny, "targets", hitCount, "mp", abil.Mp)
	return hitAny
}

// ---------- 野蛮冲撞(27) ----------
//
// 原版 `SKILL_MOOTEBO`（ObjBase.pas:9113-9145）→ `DoMotaebo`（ObjBase.pas:21703-21824）。
// 与另外几个战士技能的关键差别：**方向不是自己的朝向**，而是客户端发来的
// `Recog` 低 16 位（`SendSpellMsg(CM_SPELL, 自身朝向, 0, 技能号, 0)`，
// 服务端 `m_btDirection := nTargetX`）。

// motaeboCooldown 是野蛮冲撞的冷却（原版 `m_dwDoMotaeboTick`：3 秒）。
//
// ⚠️ 节奏类时间 ⇒ 比较时过 `tscale.D`（同施法冷却）。
const motaeboCooldown = 3 * time.Second

// motaeboSteps 是"能推进几格"的循环上界（原版 `_MAX(2, nMagicLevel + 1)`）。
func motaeboSteps(magicLevel int) int { return max(2, magicLevel+1) }

// motaeboPasses 是 CanMotaebo 的概率门（ObjBase.pas:21710）：
//
//	Random(20) < 技能等级*4 + 6 + 等级差
func motaeboPasses(magicLevel, levelDiff int) bool {
	return delphi.Random(20) < magicLevel*4+6+levelDiff
}

// castMotaebo 处理"按下野蛮冲撞技能键"（SKILL_MOOTEBO，ObjBase.pas:9113-9145）。
//
// 顺序照原版：**先判冷却、再判蓝、扣蓝、再撞**；只有撞动了才练技。
func (s *Server) castMotaebo(c net.Conn, p *Player, um *pb.UserMagic, info *data.MagicInfo, dir int) {
	now := time.Now()
	if now.Sub(p.motaeboAt) <= tscale.D(motaeboCooldown) {
		log.Printf("%s 的野蛮冲撞还在 %.1f 秒冷却内（不扣蓝）",
			p.Char.Name, tscale.D(motaeboCooldown).Seconds())
		return
	}
	p.motaeboAt = now

	d := uint8(dir) & 7
	// 朝向属于 s.mu 域（statelock.go 第二节）。
	s.turnPlayer(p, d)

	cost := magic.SpellPoint(info, um)
	abil := p.Char.Data.Abil
	if abil.Mp < cost {
		log.Printf("%s 的野蛮冲撞 MP 不足（需 %d，有 %d）", p.Char.Name, cost, abil.Mp)
		return
	}
	if cost > 0 {
		p.addMP(-int64(cost)) // 扣蓝（持锁）
		s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)
	}

	if s.doMotaebo(c, p, um.Level, d) {
		// 原版：`if DoMotaebo(...) then ... TrainSkill(UserMagic, Random(3) + 1)`
		s.trainMagicN(c, p, info, um, 1+rand.IntN(3))
	}
}

// rushShot 是自己被"冲"出去的一段位移（锁外广播 SM_RUSH 用）。
type rushShot struct {
	id   uint32
	x, y int
	dir  uint8
}

// backstepShot 是某个对象被推开一格（锁外广播 SM_BACKSTEP 用）。
type backstepShot struct {
	id   uint32
	x, y int
	dir  uint8
	m    *world.Map
}

// doMotaebo 是 `DoMotaebo`（ObjBase.pas:21703-21824）的移植。
//
// 原版是两段（别合并）：
//
//	① 正前方**有对象**：最多推 `max(2, 等级+1)` 轮，每轮
//	     - `CanMotaebo`：等级**高于**目标 + 目标未定身 + `motaeboPasses` + IsProperTarget
//	     - 等级 >= 3 时把**前方第二格**的对象也推一格
//	     - 目标推开一格（`CharPushed(dir,1)`），推不动就停
//	     - 自己跟进占住目标让出的格子并广播 `SM_RUSH`，`n24--`（伤害档位）
//	② 正前方**没人**：自己一路往前冲（`SM_RUSH`），`n28--` 记成功步数；
//	   前方可走但被占 ⇒ `n28 := 0`（这一步不算、也不自伤）；彻底走不动 ⇒ `bo35`
//
// 结算顺序也照原版：先打**最后推到的对象** → 判 `bo35`（`SM_RUSHKUNG` + "冲撞力不够!"）
// → 最后 `n28 > 0` 时**自伤**。
//
// ⚠️ 每一步都用"锁内取快照 → 锁外判关系 → 锁内落子"三段式：
// `canAttackTarget` 会读行会/组队（各自带锁），持 `s.mu` 调它是**明令禁止**的
// （见 wall.go:298 的三段式注释）。
func (s *Server) doMotaebo(c net.Conn, p *Player, magicLevel uint32, dir uint8) bool {
	lv := int(magicLevel)
	n24, n28 := lv+1, lv+1
	bo35 := true // 没推动/冲不出去 ⇒ SM_RUSHKUNG
	ok := false
	now := time.Now()
	steps := motaeboSteps(lv)

	var rushed []rushShot
	var backs []*backstepShot
	var last *pushCandidate

	// frontTile 给出正前方一格的坐标（(x,y) 由当前站位 + 朝向推出）。
	step := func() (int, int) {
		d := entity.DirDelta[dir]
		return p.Obj.PosX() + int(d[0]), p.Obj.PosY() + int(d[1])
	}
	// forwardSelf 是"自己跟进一格"（原版 MoveToMovingObject 成功那一支）。
	// 调用方持 s.mu。返回是否真的移动了。
	forwardSelf := func() bool {
		nx, ny := step()
		if !p.Obj.MapRef().CanWalk(nx, ny) || s.cellOccupied(p.Obj.MapRef(), nx, ny, p.Obj) {
			return false
		}
		p.Obj.SetPos(p.Obj.MapRef(), nx, ny)
		s.world.index.Update(p)
		rushed = append(rushed, rushShot{id: p.Obj.ID, x: nx, y: ny, dir: dir})
		return true
	}
	// snapshot 取正前方一格的对象（锁内只读索引，不再加锁）。
	snapshot := func(offset int) *pushCandidate {
		d := entity.DirDelta[dir]
		return s.targetAtLocked(p.Obj.MapRef(),
			p.Obj.PosX()+int(d[0])*offset, p.Obj.PosY()+int(d[1])*offset, p)
	}

	// 先看一眼正前方（决定走哪一支）
	s.mu.Lock()
	first := snapshot(1)
	s.mu.Unlock()

	if first != nil {
		for i := 0; i <= steps; i++ {
			s.mu.Lock()
			fr := snapshot(1)
			s.mu.Unlock()
			if fr == nil {
				break
			}
			n28 = 0 // 有目标 ⇒ 不结算自伤（原版就在这里清掉）
			if !s.canMotaebo(p, *fr, lv, now) {
				break
			}
			if lv >= 3 {
				s.mu.Lock()
				second := snapshot(2)
				s.mu.Unlock()
				if second != nil && s.canMotaebo(p, *second, lv, now) {
					s.mu.Lock()
					if bs := s.pushOneTile(*second, dir); bs != nil {
						backs = append(backs, bs)
					}
					s.mu.Unlock()
				}
			}
			s.mu.Lock()
			bs := s.pushOneTile(*fr, dir)
			moved := false
			if bs != nil {
				moved = forwardSelf()
			}
			s.mu.Unlock()
			if bs != nil && !moved {
				// 目标推开了、自己却没跟进：只可能是自己那一侧被挡（理论上目标
				// 让出的格子一定是空的，出这条就说明有别的实体挤进来了）
				log.Printf("%s 的野蛮冲撞：目标已推开但自己没跟进（站位 (%d,%d) 方向 %d）",
					p.Char.Name, p.Obj.PosX(), p.Obj.PosY(), dir)
			}
			if bs == nil {
				break // 目标推不动：原版 Break（bo35 保持 true）
			}
			backs = append(backs, bs)
			if moved {
				bo35 = false
				ok = true
			}
			last = fr
			n24--
		}
	} else {
		bo35 = false
		for i := 0; i <= steps; i++ {
			s.mu.Lock()
			moved := forwardSelf()
			walkable := p.Obj.MapRef().CanWalk(step())
			s.mu.Unlock()
			if moved {
				n28--
				continue
			}
			if walkable {
				// 前方可走但被占：原版把 n28 清 0（不结算自伤），也不 break
				n28 = 0
				continue
			}
			bo35 = true
			break
		}
	}

	// ---- 锁外广播 ----
	for _, r := range rushed {
		s.broadcastToViewers(p.Obj.MapRef(), r.x, r.y, func(o *Player) {
			if o == p || o.visible.Contains(r.id) {
				s.send(o.conn, proto.SM_RUSH, int32(r.id),
					uint16(r.x), uint16(r.y), uint16(r.dir), "")
			}
		})
	}
	for _, b := range backs {
		s.broadcastToViewers(b.m, b.x, b.y, func(o *Player) {
			s.send(o.conn, proto.SM_BACKSTEP, int32(b.id),
				uint16(b.x), uint16(b.y), uint16(b.dir), "")
		})
	}

	// ---- 伤害：先打最后推到的那个对象 ----
	if last != nil {
		if n24 < 0 {
			n24 = 0
		}
		raw := delphi.Random((n24+1)*10) + (n24+1)*10
		s.strikeMotaebo(c, p, *last, raw, now)
	}
	// ---- 推不动：SM_RUSHKUNG + "冲撞力不够!" ----
	if bo35 {
		s.mu.RLock()
		fx, fy := step()
		s.mu.RUnlock()
		s.broadcastToViewers(p.Obj.MapRef(), fx, fy, func(o *Player) {
			if o == p || o.visible.Contains(p.Obj.ID) {
				s.send(o.conn, proto.SM_RUSHKUNG, int32(p.Obj.ID),
					uint16(fx), uint16(fy), uint16(dir), "")
			}
		})
		// String.ini `MateDoTooweak`
		s.sysMsg(c, "冲撞力不够!")
	}
	// ---- 自伤（只在"正前方没人、纯往前冲"那一支结算）----
	if n28 > 0 {
		if n24 < 0 {
			n24 = 0
		}
		s.selfDamage(c, p, delphi.Random(n24*10)+(n24+1)*3)
	}

	log.Printf("%s 的野蛮冲撞：方向 %d，自己推进 %d 格，推开 %d 个目标，冲劲不足=%v（技能等级 %d）",
		p.Char.Name, dir, len(rushed), len(backs), bo35, lv)
	obs.Event("motaebo", "player", p.Char.Name, "dir", int(dir), "level", lv,
		"self_steps", len(rushed), "pushed", len(backs), "weak", bo35)
	return ok
}

// stepCoords 返回 (x,y) 沿 dir 走一格后的坐标。
func stepCoords(x, y int, dir uint8) (int, int) {
	d := entity.DirDelta[dir]
	return x + int(d[0]), y + int(d[1])
}

// canMotaebo 是原版 DoMotaebo 里的内嵌 `CanMotaebo`（ObjBase.pas:21704-21715）：
//
//	等级**高于**目标 + 目标未定身 + `motaeboPasses` + IsProperTarget
//
// ⚠️ 内部会调 `canAttackTarget`（读行会/组队），**不许在持 s.mu 时调用**。
func (s *Server) canMotaebo(p *Player, cand pushCandidate, lv int, now time.Time) bool {
	my := int(p.level())
	if my <= cand.level {
		return false
	}
	// m_boStickMode：被"困魔咒"定住的对象推不动
	if cand.mon != nil && cand.mon.Seized(now) {
		return false
	}
	if !motaeboPasses(lv, my-cand.level) {
		return false
	}
	if cand.pl != nil && !s.canAttackTarget(p, cand.pl, attackModeOf(p), now) {
		return false
	}
	return true
}

// targetAtLocked 取 (x,y) 上的对象（玩家优先，与近战目标查找一致）。
//
// ⚠️ **调用方持 s.mu**：只用空间索引本身，不再取锁（`sync.RWMutex` 不可重入）。
func (s *Server) targetAtLocked(m *world.Map, x, y int, self *Player) *pushCandidate {
	for _, o := range s.world.index.InRange(x, y, 0) {
		q, ok := o.(*Player)
		if !ok || q == self || q.Obj.MapRef() != m || q.Obj.PosX() != x || q.Obj.PosY() != y {
			continue
		}
		if q.Char == nil || q.Char.Data == nil || q.Char.Data.Abil == nil ||
			q.hp() == 0 {
			continue
		}
		return &pushCandidate{id: q.Obj.ID, obj: q.Obj, pl: q,
			level: int(q.level()), x: x, y: y}
	}
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		mo, ok := o.(*entity.Monster)
		if !ok || mo.MapRef() != m || mo.PosX() != x || mo.PosY() != y || !mo.Alive || mo.Info == nil {
			continue
		}
		if mo.IsNPC || mo.CastleKind != "" {
			continue
		}
		return &pushCandidate{id: mo.ID, obj: mo.Object, mon: mo,
			level: int(mo.Info.Level), x: x, y: y}
	}
	return nil
}

// pushOneTile 把 cand 沿 dir 推开一格（原版 `TBaseObject.CharPushed(dir, 1)`）。
//
// ⚠️ **调用方持 s.mu**：它改坐标、朝向，并且**必须同步空间索引**——
// 索引不同步会让被推的对象在 `monsterAt`/`playerAt`（按坐标比对）里"消失"：
// 索引里还挂在旧格子上，坐标却已经变了，比对永远不匹配。
// （抗拒火环此前就漏了这一步，本轮一并补上。）
//
// 推不动返回 nil。
func (s *Server) pushOneTile(cand pushCandidate, dir uint8) *backstepShot {
	back := uint8((int(dir) + 4) % 8)
	nx, ny := stepCoords(cand.obj.PosX(), cand.obj.PosY(), dir)
	if !cand.obj.MapRef().CanWalk(nx, ny) ||
		s.cellOccupied(cand.obj.MapRef(), nx, ny, cand.obj) {
		// 推不动的原因要留痕：地形不可走 / 目标格已被别人占（排查"冲劲不足"
		// 与"抗拒火环推开 0 格"时，这两条完全看不到）。
		log.Printf("推不动 ActorId=%d：(%d,%d) → (%d,%d) 可走=%v 被占=%v",
			cand.id, cand.obj.PosX(), cand.obj.PosY(), nx, ny,
			cand.obj.MapRef().CanWalk(nx, ny), s.cellOccupied(cand.obj.MapRef(), nx, ny, cand.obj))
		return nil
	}
	cand.obj.SetPlace(cand.obj.MapRef(), nx, ny, back)
	switch {
	case cand.mon != nil:
		s.world.monsterIdx.Update(cand.mon)
		// 原版 CharPushed 里 `m_dwWalkTick := m_dwWalkTick + 800`（被推后停一下）
		cand.mon.DelayMove(tscale.D(800 * time.Millisecond))
	case cand.pl != nil:
		s.world.index.Update(cand.pl)
	}
	return &backstepShot{id: cand.id, x: nx, y: ny, dir: back, m: cand.obj.MapRef()}
}

// strikeMotaebo 结算"撞到目标"的伤害（ObjBase.pas:21786-21800）。
//
// 伤害公式与普通挥砍**不同**（`Random((n24+1)*10) + (n24+1)*10`），
// 所以不能走 attackPlayer / rollDamage。
func (s *Server) strikeMotaebo(c net.Conn, p *Player, cand pushCandidate, raw int, now time.Time) {
	if cand.mon != nil && cand.mon.Info != nil {
		dmg := applyArmor(raw, uint32(proto.PackMinMax(cand.mon.Info.AC, cand.mon.Info.AC)))
		log.Printf("%s 的野蛮冲撞撞到 %s：威力 %d → 伤害 %d",
			p.Char.Name, cand.mon.Name, raw, dmg)
		s.hitMonster(p, cand.mon, dmg)
		return
	}
	if cand.pl != nil && cand.pl.Char != nil && cand.pl.Char.Data != nil {
		dmg := applyArmor(raw, abilityFromPB(cand.pl.Char.Data.Abil).AC)
		log.Printf("%s 的野蛮冲撞撞到 %s：威力 %d → 伤害 %d",
			p.Char.Name, cand.pl.Char.Name, raw, dmg)
		s.damagePlayer(c, p, cand.pl, dmg, now)
	}
}

// selfDamage 结算野蛮冲撞的自伤（原版末尾 `StruckDamage` 落在自己身上）。
func (s *Server) selfDamage(c net.Conn, p *Player, raw int) {
	if p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	dmg := applyArmor(raw, abilityFromPB(p.abilCopy()).AC)
	hp := p.addHP(-int64(dmg)) // 自伤（持锁；夹到 0）
	maxHP := p.maxHP()
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(o *Player) {
		if o == p || o.visible.Contains(p.Obj.ID) {
			s.sendStruck(o, p.Obj.ID, hp, maxHP, dmg)
		}
	})
	log.Printf("%s 野蛮冲撞冲劲不足，自伤 %d（HP %d/%d）", p.Char.Name, dmg, hp, maxHP)
	if hp == 0 {
		// 复活戒指同样拦（原版在 Run 里对所有致死途径都判）
		if s.tryRevivalRing(p) {
			return
		}
		// 与"被怪打死"走同一条路（SM_NOWDEATH + 回城 + 死亡掉落）
		s.send(c, proto.SM_NOWDEATH, int32(p.Obj.ID),
			uint16(p.Obj.PosX()), uint16(p.Obj.PosY()), uint16(p.Obj.Facing()), "")
		s.revive(c, p, nil)
	}
}

// 野生守卫（`RC_GUARD(11)` **大刀卫士** / `RC_ARCHERGUARD(112)` **弓箭守卫**）的"该打谁"。
//
// 口径有**三份**，互相不完全一致。这里按"用户给的说明为准、与引擎冲突处标注"取：
//
//	A. 引擎 `TSuperGuard`（大刀，`ObjGuard.pas:87-126`）：红名 **或** 一切怪
//	   （`race >= RC_MONSTER(80)` 且非任务怪）；`m_boAttackPet := True` ⇒ **连别人的宝宝也打**。
//	B. 引擎 `TGuardUnit.IsProperTarget`（`ObjMon2.pas:874-880`，弓箭守卫继承它）：
//	   红名 **或** 打过我的（`m_LastHiter`）**或** 正在打弓箭守卫(112)的。
//	C. `docs/g.md`（用户给的说明）：大刀打怪 + 宝宝（与 A 同）；弓箭守卫"只打怪物、红名、
//	   攻击者，**不主动打宝宝**"—— 比 B 宽（一切怪）、比 B 窄（不主动打宝宝）。
//
// **取法**：大刀 = A；弓箭守卫 = C ∪ B（C 说了"打怪"，B 那两条只会多打"该打的"，
// 不会漏掉用户要的）。三处刻意的偏离都在注释里点名。
//
// ⚠️ 优先级是**分档**的（`docs/g.md` 的统一规则：红名 > 怪物 > 主动攻击自己的白名），
// 引擎里没有分档（它是"遍历视野取第一个命中"）—— 分档让行为可复现，不依赖 map 遍历顺序。
package gamesvr

import (
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
)

// 优先级档（越小越先打）。同档取最近。
const (
	guardTierRed     = iota // 0：红名玩家（PKLevel >= 2，见 pvp.RedNameLevel）
	guardTierMonster        // 1：怪物 / 召唤宝宝
	guardTierAngry          // 2：主动攻击过这只守卫的（白名玩家 / 宝宝）
)

// guardTarget 是野生守卫挑出来的一个目标 —— 玩家与怪**二选一**。
type guardTarget struct {
	player  *Player
	monster *entity.Monster
	tier    int
	dist    int
}

// id 取目标的 ActorId（用来回写 `Monster.TargetID`）。
func (t *guardTarget) id() uint32 {
	if t == nil {
		return 0
	}
	if t.player != nil {
		return t.player.Obj.ID
	}
	if t.monster != nil {
		return t.monster.ID
	}
	return 0
}

// guardPick 给一只**野生守卫**挑目标：人 + 怪一起看（原版的 `m_VisibleActors` 就是两者都看）。
//
// 每拍**从头重算**（不沿用上一拍）⇒ 目标跑出视野、红名洗白、怪死了都会自动丢。
// **调用方持 s.mu**。
func (s *Server) guardPick(m *entity.Monster) *guardTarget {
	if m == nil || m.Info == nil {
		return nil
	}
	// 大刀卫士：连"召唤宝宝"一起打（A：`m_boAttackPet = True`；C 也写了"主动杀骷髅、神兽"）
	blade := m.Info.Race == entity.RcGuard

	var best *guardTarget
	better := func(t *guardTarget) {
		if best == nil || t.tier < best.tier || (t.tier == best.tier && t.dist < best.dist) {
			best = t
		}
	}

	// ---- 玩家：红名（档 0）或"打过我的"（档 2）----
	for _, p := range s.world.players {
		if p == nil || p.Obj == nil || p.Obj.MapRef() != m.MapRef() {
			continue
		}
		if p.Obj.ID == m.ID {
			continue
		}
		// ⚠️ 这里**不查死亡**：与原来那段玩家扫描一致（本项目没有`玩家已死`这个谓词，
		// 死人很快就退出视野/离图）。要加的话在 `Player` 上开一个，别在这里自己比 HP。
		// 隐身：怪物看不见（隐身术的效果就在这类地方体现）
		if p.hasBuff(entity.BuffInvisible) {
			continue
		}
		d := m.Distance(p.Obj.PosX(), p.Obj.PosY())
		if d > m.ViewRange {
			continue
		}
		tier := -1
		switch {
		case p.isRedName():
			tier = guardTierRed // 红名最高档（原版 `PKLevel >= 2`）
		case p.Obj.ID == m.LastHiterID:
			tier = guardTierAngry // 打过我的白名：还手（`docs/g.md` 第 4 条 / 引擎 ①）
		}
		if tier < 0 {
			continue
		}
		better(&guardTarget{player: p, tier: tier, dist: d})
	}

	// ---- 怪：怪物（档 1）/ 宝宝（档 1，仅大刀）/ 打过我的（档 2）----
	for _, o := range s.world.monsters {
		if o == nil || o == m || o.Info == nil || o.IsDead() {
			continue
		}
		if o.MapRef() != m.MapRef() {
			continue
		}
		race := o.Info.Race
		// 非战斗单位（NPC / 大刀 / 弓箭警察…）不打 —— 引擎的排除项（`ObjMon2.pas:866-868`）。
		// 同类守卫（大刀↔弓箭守卫）也不打：**刻意偏离**（引擎只排除 10..49，112 不在里面，
		// 按字面大刀会去打弓箭守卫 —— 那是引擎的坑，不是我们要的）。
		if entity.IsNonCombatant(race) || entity.IsGuardRace(race) {
			continue
		}
		d := m.Distance(o.PosX(), o.PosY())
		if d > m.ViewRange {
			continue
		}
		tier := -1
		switch {
		case o.ID == m.LastHiterID:
			tier = guardTierAngry // 打过我的：两个守卫都还手（引擎 ① / C 的"攻击者"）
		case o.MasterID != 0 && !blade:
			tier = -1 // 召唤宝宝：弓箭守卫**不主动打**（C 第 4 条，与大刀最大的区别）
		case race >= entity.RcMonster, o.MasterID != 0:
			// 怪物（race >= 80）或召唤宝宝（骷髅/神兽/法师宝宝，种族各异）都算"怪物"档。
			// ⚠️ 动物（50..79：鸡/鹿…）**不打** —— 引擎的 `race >= RC_MONSTER(80)` 就是这个意思。
			tier = guardTierMonster
		}
		if tier < 0 {
			continue
		}
		better(&guardTarget{monster: o, tier: tier, dist: d})
	}
	// 回写 `Monster.TargetID`：它是"锁定"的**可观测点**（e2e 的 guard-lock 断言、
	// 以及别的判据里"正在打谁"都读它）。
	if best == nil {
		m.TargetID = 0
	} else {
		m.TargetID = best.id()
	}
	return best
}

// guardAttackMonster 守卫打怪的一次结算。**调用方持 s.mu**。
//
// 伤害复用"怪打怪"那条（`monsterVsMonster`：`DC..DCMax − AC` + 红毒放大），
// 只多两件事：
//   - 弓箭守卫（race 112）**不判命中**（原版箭无虚发，见 `monsterVsMonster`）；
//   - 把"谁打的我"记到怪身上（`LastHiterID`）—— 别的怪/守卫的判据也读它。
func (s *Server) guardAttackMonster(m, target *entity.Monster, now time.Time) petHit {
	// 大刀走 `_Attack`（有命中判定）；弓箭守卫没有
	checkHit := m.Info == nil || m.Info.Race != entity.RcArcherGuard
	h := s.monsterVsMonster(m, target, now, checkHit)
	target.LastHiterID = m.ID
	return h
}

// applyGuardHits 把守卫打怪的结果发出去（**锁外**）。
//
// 与 `applyPetHits` 的差别：守卫**没有主人** ⇒ 不发经验、不掉落（玩家打守卫才有经验，
// 那是怪物数据里的 `exp`）。死亡收尾与别处一致：锁内已经记了死亡，尸骨由 AI 那遍清。
func (s *Server) applyGuardHits(hits []petHit) {
	for _, h := range hits {
		if h.dmg == 0 {
			continue
		}
		s.broadcastToViewers(h.target.MapRef(), h.target.PosX(), h.target.PosY(), func(o *Player) {
			if o.visible.Contains(h.target.ID) {
				s.sendStruck(o, h.pet.ID, h.target.ID, h.hp, h.maxHP, h.dmg)
			}
		})
		log.Printf("守卫 %s 打了 %s %d 点（HP %d/%d）", h.pet.Name, h.target.Name, h.dmg, h.hp, h.maxHP)
	}
}

package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// stoneTestPlayers 造"戴麻痹戒指的攻击者"与"受害者"两个玩家。
func stoneTestPlayers(t *testing.T) (*Server, *Player, *Player) {
	t.Helper()
	s := testSlaveServer()
	s.social.guilds = nil // 本用例不碰行会
	if s.world.index == nil {
		s.world.index = world.NewSpatialIndex(32)
	}
	// ⚠️ 地图必须给：石化后要 `broadcastStatus`（广播 SM_CHARSTATUSCHANGED），
	// 它会按坐标找视野内的玩家 ⇒ 没有地图就是空指针。
	m := world.Generate("stonetest", 50, 50, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	s.world.maps = mm
	attacker := newTestPlayer(1, "打人的", entity.JobWarr)
	victim := newTestPlayer(2, "挨打的", entity.JobWarr)
	attacker.Obj, victim.Obj = entity.NewObject(1, "", m, 0, 0, 0, 0), entity.NewObject(2, "", m, 0, 0, 0, 0)
	attacker.Char.Name, victim.Char.Name = "打人的", "挨打的"
	attacker.equipSpecials.paralysis = true
	return s, attacker, victim
}

// TestParalysisRingOnPlayer 麻痹戒指对**玩家**同样生效（原版 `_Attack` 的
// `AttackTarget` 是 `TBaseObject`，ObjBase.pas:22265）：
// 抽签命中 ⇒ 对方被石化 + 状态位出现 `StateStone` + 收到"你中毒了"那条文案。
func TestParalysisRingOnPlayer(t *testing.T) {
	s, attacker, victim := stoneTestPlayers(t)
	restore := withRnd(func(n int) int {
		if n == attackPoisonRate { // Random(抗毒0 + 5)
			return 0
		}
		return 1
	})
	defer restore()

	if !s.paralysisPlayerOnHit(attacker, victim) {
		t.Fatal("抽签命中时应返回 true")
	}
	if !victim.Obj.Stoned(time.Now()) {
		t.Error("命中后受害者应处于石化状态")
	}
	if victim.statusBits()&entity.StateStone == 0 {
		t.Error("石化后状态位里应出现 StateStone（原版 MakePosion 会 StatusChanged）")
	}

	// 没抽中 ⇒ 不石化
	victim.Obj.SetStoneUntil(time.Time{})
	restore2 := withRnd(func(int) int { return 1 })
	defer restore2()
	if s.paralysisPlayerOnHit(attacker, victim) {
		t.Error("没抽中时不该石化")
	}
	if victim.Obj.Stoned(time.Now()) {
		t.Error("没抽中时不该有石化状态")
	}
}

// TestParalysisRingRequiresRing 没戴戒指（`m_boParalysis` 为假）就不该有任何效果。
func TestParalysisRingRequiresRing(t *testing.T) {
	s, attacker, victim := stoneTestPlayers(t)
	attacker.equipSpecials.paralysis = false
	restore := withRnd(func(int) int { return 0 })
	defer restore()
	if s.paralysisPlayerOnHit(attacker, victim) {
		t.Error("没戴麻痹戒指不该石化目标")
	}
	if victim.Obj.Stoned(time.Now()) {
		t.Error("没戴戒指不该有石化状态")
	}
}

// TestStoneExtendsOnly 石化只**延长**不缩短（原版 `MakePosion`：
// `if m_wStatusTimeArr[nType] < nTime then ... := nTime`，ObjBase.pas:22738-22744）。
func TestStoneExtendsOnly(t *testing.T) {
	o := &entity.Object{}
	o.Stone(5 * time.Second)
	long := o.StoneUntil()
	o.Stone(1 * time.Second) // 更短 ⇒ 不该缩短
	if o.StoneUntil() != long {
		t.Errorf("更短的石化不该缩短剩余时间：%v → %v", long, o.StoneUntil())
	}
	if !o.Stoned(time.Now()) {
		t.Error("刚石化应处于石化状态")
	}
	if o.Stoned(time.Now().Add(6 * time.Second)) {
		t.Error("6 秒后应已解除")
	}
}

// TestMonsterCantActWhileStoned 石化的怪物不能行动（原版怪物 AI 里
// `m_wStatusTimeArr[POISON_STONE] = 0` 是攻击/移动的前置条件，
// ObjMon.pas:423 / ObjAxeMon.pas:133）；麻痹戒指打怪走的也是这条路。
func TestMonsterCantActWhileStoned(t *testing.T) {
	mon := newTestMonster(9, "鸡", 10)
	now := time.Now()
	if !mon.CanAct(now.Add(time.Hour)) {
		t.Fatal("没石化时（间隔足够）应可以行动")
	}
	mon.Stone(5 * time.Second)
	if mon.CanAct(now) {
		t.Error("石化期间不该可以行动")
	}
	// 用一个"无论如何都超过移动间隔"的时刻，专测石化这一维
	if !mon.CanAct(now.Add(time.Hour)) {
		t.Error("石化过期后应恢复行动")
	}
}

// TestParalysisOnMonsterUsesStone 打怪的麻痹走 `Object.Stone`（原版 POISON_STONE），
// 而不是困魔咒的 `Seize`（`m_boHolySeize`，被打会解除）——两者语义不同。
func TestParalysisOnMonsterUsesStone(t *testing.T) {
	s, attacker, _ := stoneTestPlayers(t)
	mon := newTestMonster(9, "鸡", 10)
	restore := withRnd(func(int) int { return 0 })
	defer restore()
	if !s.paralysisOnHit(attacker, mon) {
		t.Fatal("抽签命中时应返回 true")
	}
	if !mon.Stoned(time.Now()) {
		t.Error("怪应被石化（Object.Stone）")
	}
	if !mon.SeizedUntil.IsZero() {
		t.Error("不该动 SeizedUntil：那是困魔咒，被攻击会解除，与石化不是一回事")
	}
}

// TestStoneStatusClearsOnExpiry 石化到期后状态位要**自己清掉并广播**。
//
// ⚠️ 这条补的是一个真 bug：我们的状态位是按需算的（`statusBits()` 按当前时刻算），
// 而客户端只认 SM_CHARSTATUSCHANGED ⇒ 到期时若没别的事触发广播，
// 客户端会一直停在"被石化"的画面上（麻痹戒指打人之后尤其明显）。
func TestStoneStatusClearsOnExpiry(t *testing.T) {
	s, p, _ := makeDrugTestServer(t)

	p.Obj.Stone(2 * time.Second)
	s.broadcastStatus(p) // 石化播出去
	if uint32(p.Obj.StatusBits())&entity.StateStone == 0 {
		t.Fatalf("石化该进状态位，实际 0x%08X", p.Obj.StatusBits())
	}
	// 把到期时刻拨到过去 ⇒ 下一秒的 tick 应该把状态位清掉
	p.Obj.SetStoneUntil(time.Now().Add(-time.Second))
	s.tickPlayerStatus(p, time.Now())
	if uint32(p.Obj.StatusBits()) != 0 {
		t.Errorf("石化到期后状态位该清空，实际 0x%08X", p.Obj.StatusBits())
	}
}

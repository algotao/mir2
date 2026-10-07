package gamesvr

import (
	"github.com/algotao/mir2/server/internal/delphi"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// TestKillUndeadChance 圣言术即死概率公式（Magic.pas:918）。
//
//	Random(100) < (技能等级 shl 3) - 技能等级 + 15 + (施法者等级 - 目标等级)
func TestKillUndeadChance(t *testing.T) {
	cases := []struct {
		skillLv, plLv, monLv int
		want                 int
	}{
		{0, 7, 5, 15 + 2}, // 0 级：0*7+15+2
		{1, 7, 5, 7 + 15 + 2},
		{3, 30, 20, 21 + 15 + 10},
		{3, 10, 20, 21 + 15 - 10}, // 等级落后 → 概率反而低
	}
	for _, c := range cases {
		if got := killUndeadChance(c.skillLv, c.plLv, c.monLv); got != c.want {
			t.Errorf("killUndeadChance(%d,%d,%d) = %d, 期望 %d",
				c.skillLv, c.plLv, c.monLv, got, c.want)
		}
	}
}

// TestKillUndeadLevelLimit 即死等级上限 50（g_Config.nMagTurnUndeadLevel 出厂值）。
func TestKillUndeadLevelLimit(t *testing.T) {
	if killUndeadLevelOK(49) != true {
		t.Error("49 级应可被即死")
	}
	// ⚠️ 边界是**严格小于**：原版是 `if Target.Level < nMagTurnUndeadLevel`
	if killUndeadLevelOK(50) != false {
		t.Error("50 级不应可被即死（原版是 < 50，不是 <= 50）")
	}
	if killUndeadLevelOK(51) != false {
		t.Error("51 级不应可被即死")
	}
}

// TestCanPassCurtain 困魔咒等级压制（Magic.pas:1234）。
func TestCanPassCurtain(t *testing.T) {
	cases := []struct {
		roll, plLv, monLv int
		want              bool
	}{
		{1, 10, 5, true},   // 1+9=10 > 5
		{4, 10, 13, false}, // 4+9=13 不 > 13
		{4, 10, 12, true},  // 13 > 12
		{1, 1, 5, false},   // 等级太低
	}
	for _, c := range cases {
		if got := canPassCurtain(c.roll, c.plLv, c.monLv); got != c.want {
			t.Errorf("canPassCurtain(%d,%d,%d) = %v, 期望 %v",
				c.roll, c.plLv, c.monLv, got, c.want)
		}
	}
}

// withSweepRnd 让 Random(n) 依次返回 0..n-1 循环，用于扫满整个取值域。
//
// 这几个概率技能的偏差全是"区间端点差 1"（见 docs/HANDOFF.md 坑 58/74），
// 只有把域里每个值都走一遍才咬得住——固定取上界/下界各只覆盖一个点。
func withSweepRnd() func() {
	i := 0
	return withFixedRnd(func(n int) int {
		if n <= 0 {
			return 0
		}
		v := i % n
		i++
		return v
	})
}

// countTrue 反复调用 f 共 n 次，数返回 true 的次数。
func countTrue(n int, f func() bool) int {
	c := 0
	for i := 0; i < n; i++ {
		if f() {
			c++
		}
	}
	return c
}

// TestSpaceMoveProbability 瞬息移动的发动率（Magic.pas:956）。
//
// 原版 `Random(11) < 技能等级*2 + 4`：0/1/3 级 = 4/11、6/11、10/11。
// ⚠️ 写成 `1+Random(11)` 会各少一档（3/11、5/11、9/11）——这就是
// e2e `skill2-space` 偶发失败的根因（本测试在修复前必红）。
func TestSpaceMoveProbability(t *testing.T) {
	defer withSweepRnd()()
	for _, c := range []struct{ magicLevel, want int }{
		{0, 4}, {1, 6}, {2, 8}, {3, 10},
	} {
		got := countTrue(11, func() bool { return spaceMoveSucceeds(c.magicLevel) })
		if got != c.want {
			t.Errorf("技能等级 %d：11 次抽签成功 %d 次，期望 %d（Random(11) < %d）",
				c.magicLevel, got, c.want, c.magicLevel*2+4)
		}
	}
}

// TestTurnUndeadLevelPass 圣言术等级压制的抽签域（Magic.pas:911）。
//
// 原版 `Random(2) + (施法者等级-1) > 目标等级`，Random(2) ∈ {0,1}。
func TestTurnUndeadLevelPass(t *testing.T) {
	defer withSweepRnd()()
	// pl=5,mon=4：roll=0 → 4>4 假、roll=1 → 5>4 真 ⇒ 2 次里过 1 次。
	// 写成 1+Random(2) 则 roll∈{1,2}，两次都过 ⇒ 2 次。
	if got := countTrue(2, func() bool { return turnUndeadLevelPass(5, 4) }); got != 1 {
		t.Errorf("(pl=5,mon=4) 过 %d/2，期望 1（1+Random(2) 会变成 2）", got)
	}
	// pl=1,mon=0：roll=0 → 0>0 假、roll=1 → 1>0 真 ⇒ 1 次。
	if got := countTrue(2, func() bool { return turnUndeadLevelPass(1, 0) }); got != 1 {
		t.Errorf("(pl=1,mon=0) 过 %d/2，期望 1", got)
	}
}

// TestKillUndeadRollSweep 圣言术即死掷骰的域（Magic.pas:918）。
//
// 原版 `Random(100) < chance`，掷骰域是 0..99。
func TestKillUndeadRollSweep(t *testing.T) {
	defer withSweepRnd()()
	// chance=100：100 个掷骰值应全部命中。写成 1+Random(100) 只有 99 个。
	if got := countTrue(100, func() bool { _, ok := killUndeadRoll(100, 0); return ok }); got != 100 {
		t.Errorf("chance=100 命中 %d/100，期望 100（1+Random(100) 会变成 99）", got)
	}
	// chance=1：只有掷出 0 才中。
	if got := countTrue(100, func() bool { _, ok := killUndeadRoll(1, 0); return ok }); got != 1 {
		t.Errorf("chance=1 命中 %d/100，期望 1", got)
	}
	// 目标等级达到上限（50）：原版先判 `等级 < 50`，一个都不中。
	if got := countTrue(100, func() bool {
		_, ok := killUndeadRoll(100, killUndeadMaxLevel)
		return ok
	}); got != 0 {
		t.Errorf("目标等级 = 上限时命中 %d/100，期望 0", got)
	}
}

// TestCanPassCurtainDrawDomain 困魔咒抽签的取值域必须是 Random(4)（Magic.pas:1234）。
func TestCanPassCurtainDrawDomain(t *testing.T) {
	defer withSweepRnd()()
	// pl=10,mon=9：roll+9 > 9 ⇔ roll>0 ⇒ {1,2,3} 三个值过 = 3/4。
	// 写成 1+Random(4)（域 1..4）则 4/4 全过。
	got := countTrue(4, func() bool { return canPassCurtain(delphi.Random(4), 10, 9) })
	if got != 3 {
		t.Errorf("Random(4) 域下过 %d/4，期望 3（1+Random(4) 会变成 4）", got)
	}
}

// TestHolyCurtainSeconds 定身时长 = GetPower13(40) + GetRPow(SC)×3。
//
// 原版 Magic.pas:462：`MagMakeHolyCurtain(PlayObject, GetPower13(40) + GetRPow(SC) * 3, ...)`。
// ⚠️ 用的是 **GetRPow(SC)**（[下限,上限] 闭区间随机），不是"固定取上限"——
// 之前我们取 SC 上限，等于让高道击玩家的定身时间系统性偏长。
func TestHolyCurtainSeconds(t *testing.T) {
	// 固定 Random 取上界，GetRPow([lo,hi]) 才会稳定等于 hi。
	defer withFixedRnd(fixedMax)()
	if got := holyCurtainSeconds(20, packMM(0, 0)); got != 20 {
		t.Errorf("SC=0 时定身 = %d 秒, 期望 20（就是 GetPower13(40) 的 0 级值）", got)
	}
	if got := holyCurtainSeconds(20, packMM(4, 10)); got != 20+30 {
		t.Errorf("SC=[4,10] 定身 = %d 秒, 期望 %d", got, 20+30)
	}
	// 高低相等时 GetRPow 直接返回该值，不摇随机。
	if got := holyCurtainSeconds(0, packMM(7, 7)); got != 21 {
		t.Errorf("SC=[7,7] 定身 = %d 秒, 期望 21", got)
	}
	if got := holyCurtainSeconds(-5, packMM(0, 0)); got != 0 {
		t.Errorf("负威力应夹到 0，得到 %d", got)
	}
}

// TestSeizeBlocksMove 定身必须让 CanAct 返回 false（不能移动）。
//
// 接入点是 entity.Monster.CanAct —— 追击（StepToward 前）与游荡（Wonder 前）
// 都查它，所以这一条覆盖了"定身期间完全不动"。
func TestSeizeBlocksMove(t *testing.T) {
	m := &entity.Monster{
		Object: entity.NewObject(1, "", nil, 10, 10, 0, 0),
		Info:   nil,
	}
	// moveInterval 为 0 时，未定身的怪 CanAct 恒为 true
	now := time.Now()
	if !m.CanAct(now) {
		t.Fatal("未定身的怪应当可以行动")
	}
	m.Seize(10 * time.Second)
	if m.CanAct(now) {
		t.Error("定身期间 CanAct 应为 false（不能移动）")
	}
	if !m.Seized(now) {
		t.Error("Seized 应为 true")
	}
	// 到期后恢复
	later := now.Add(11 * time.Second)
	if m.Seized(later) {
		t.Error("定身到期后 Seized 应为 false")
	}
	if !m.CanAct(later) {
		t.Error("定身到期后应恢复行动")
	}
}

// TestRandomWalkableNear 随机落点必须落在可走格上（瞬息移动的落点约束）。
func TestRandomWalkableNear(t *testing.T) {
	m := world.Generate("curtain", 30, 30, true)
	// 中心可走 → 原样返回
	if x, y, ok := randomWalkableNear(m, 15, 15, 5); !ok || x != 15 || y != 15 {
		t.Errorf("可走中心应原样返回，得到 (%d,%d) ok=%v", x, y, ok)
	}
	// 角落附近也必须返回一个可走格
	if _, _, ok := randomWalkableNear(m, 0, 0, 12); !ok {
		t.Error("角落附近应能找到可走格")
	}
	if x, y, ok := randomWalkableNear(m, 0, 0, 12); ok && !m.CanWalk(x, y) {
		t.Errorf("返回的落点 (%d,%d) 不可走", x, y)
	}
	// nil 地图不 panic
	if _, _, ok := randomWalkableNear(nil, 1, 1, 3); ok {
		t.Error("nil 地图应返回 false")
	}
}

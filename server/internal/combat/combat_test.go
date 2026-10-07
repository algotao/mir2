package combat

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
)

func TestMonsterHitSpeedPoint(t *testing.T) {
	mk := func(hit, speed uint16) *entity.Monster {
		return &entity.Monster{Info: &data.MonsterInfo{Hit: hit, Speed: speed}}
	}
	if got := MonsterHitPoint(mk(3, 10)); got != 3 {
		t.Errorf("命中 = %d，期望 3", got)
	}
	if got := MonsterSpeedPoint(mk(3, 10)); got != 10 {
		t.Errorf("敏捷 = %d，期望 10", got)
	}
	if got := MonsterSpeedPoint(mk(0, 300)); got != 44 {
		t.Errorf("敏捷 300 应被截断成 44（Byte 语义），实际 %d", got)
	}
	if got := MonsterHitPoint(nil); got != 0 {
		t.Errorf("nil 怪物应返回 0，实际 %d", got)
	}
}

// TestMeleeMisses 守住打空判定本身（ObjBase.pas:22241-22246）。
//
//	if 目标.命中点 > 0 then
//	  if (攻击者.命中 < Random(目标.敏捷)) then nPower := 0;
func TestMeleeMisses(t *testing.T) {
	// 抽签源可注入：把 Random(15) 固定成各种值，逐点验边界。
	withRnd := func(v int) func() {
		old := delphi.RandN
		delphi.RandN = func(int) int { return v }
		return func() { delphi.RandN = old }
	}

	// ① 目标的"命中点"为 0 ⇒ 永远不判（原版拿它当"目标是不是有效活物"的代理）
	if Misses(1, 0, 15) {
		t.Error("目标命中点为 0 时不该判打空")
	}

	// ② 攻击者命中 5 vs 目标敏捷 15：Random(15) 取 0..14
	for r := 0; r < 15; r++ {
		restore := withRnd(r)
		got := Misses(5, 3, 15)
		restore()
		want := r > 5 // 5 < r
		if got != want {
			t.Errorf("命中 5 vs 敏捷 15、Random=%d：打空=%v，期望 %v", r, got, want)
		}
	}
	// ③ 命中 14 vs 敏捷 10 ⇒ Random(10) 最大 9，永远打不中空
	for r := 0; r < 10; r++ {
		restore := withRnd(r)
		got := Misses(14, 3, 10)
		restore()
		if got {
			t.Errorf("命中 14 对敏捷 10（Random=%d）不该打空", r)
		}
	}
	// ④ 鸡(hit=3) 打玩家(敏捷 15)：命中率 4/15 ≈ 27%
	miss := 0
	for r := 0; r < 15; r++ {
		restore := withRnd(r)
		if Misses(3, 5, 15) {
			miss++
		}
		restore()
	}
	if miss != 11 {
		t.Errorf("鸡打玩家的打空次数 = %d，期望 11（Random(15) 里 >3 的那些）", miss)
	}
}

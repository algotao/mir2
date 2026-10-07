package gamesvr

import (
	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/delphi"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
)

// TestApplyPoisonMerge 守住 MakePosion 的两条合并规则（ObjBase.pas:22737-22756）：
//
//	时长**取长**（`if m_wStatusTimeArr[t] < nTime then := nTime`）
//	点数**覆盖**（`m_btGreenPoisoningPoint := nPoint` 无条件）
func TestApplyPoisonMerge(t *testing.T) {
	now := time.Now()
	o := &entity.Object{}

	// 第一次：10 秒、每跳 3
	if changed := o.ApplyPoison(entity.PoisonDecHealth, 10, 3, 111, now); !changed {
		t.Error("首次中毒应算「状态位变化」")
	}
	if !o.PoisonActive(entity.PoisonDecHealth, now.Add(9*time.Second)) {
		t.Error("10 秒的毒应在 9 秒时仍生效")
	}
	if o.PoisonActive(entity.PoisonDecHealth, now.Add(11*time.Second)) {
		t.Error("10 秒的毒在 11 秒时不该还生效")
	}

	// 第二次更短：时长不该被缩短（取长），但点数要覆盖
	if changed := o.ApplyPoison(entity.PoisonDecHealth, 5, 9, 111, now); changed {
		t.Error("已中毒时再中，状态位没变化 ⇒ 不该报 changed（原版只有状态位变才 StatusChanged）")
	}
	if o.PoisonPoint() != 9 {
		t.Errorf("每跳点数 = %d，期望 9（点数无条件覆盖）", o.PoisonPoint())
	}
	if !o.PoisonActive(entity.PoisonDecHealth, now.Add(9*time.Second)) {
		t.Error("短毒不该把长毒的 10 秒缩短")
	}

	// 第三次更长：延长
	o.ApplyPoison(entity.PoisonDecHealth, 30, 1, 111, now)
	if !o.PoisonActive(entity.PoisonDecHealth, now.Add(29*time.Second)) {
		t.Error("长毒应把时长延长到 30 秒")
	}
}

// TestPoisonMaskAndStruckMul 守住状态位与红毒倍率。
func TestPoisonMaskAndStruckMul(t *testing.T) {
	now := time.Now()
	o := &entity.Object{}
	if m := poisonMask(o, now); m != 0 {
		t.Errorf("没中毒时状态位 = %d，期望 0", m)
	}
	o.ApplyPoison(entity.PoisonDecHealth, 10, 1, 1, now)
	if m := poisonMask(o, now); m != entity.StatePoisonGreen {
		t.Errorf("绿毒状态位 = %d，期望 %d", m, entity.StatePoisonGreen)
	}
	o.ApplyPoison(entity.PoisonDamageArmor, 10, 0, 1, now)
	if m := poisonMask(o, now); m != entity.StatePoisonGreen|entity.StatePoisonRed {
		t.Errorf("绿+红状态位 = %d，期望 %d", m, entity.StatePoisonGreen|entity.StatePoisonRed)
	}
	// 红毒倍率 = PosionDamagarmor/10 = 1.2，且用 Delphi 的 Round（四舍五入）
	if got := combat.StruckMul(o, now); got != 1.2 {
		t.Errorf("红毒倍率 = %v，期望 1.2", got)
	}
	for _, c := range []struct{ in, want int }{
		{10, 12}, // 12.0
		{11, 13}, // 13.2 → 13
		{5, 6},   // 6.0
		{1, 1},   // 1.2 → 1
		{0, 0},
	} {
		if got := combat.AdjustStruck(o, c.in, now.Add(20*time.Second)); got != c.in {
			t.Errorf("红毒过期后伤害不该被放大：%d → %d", c.in, got)
		}
		if got := combat.AdjustStruck(o, c.in, now); got != c.want {
			t.Errorf("红毒放大 %d → %d，期望 %d", c.in, got, c.want)
		}
	}
	// 没中毒时倍率恒 1
	plain := &entity.Object{}
	if got := combat.AdjustStruck(plain, 10, now); got != 10 {
		t.Errorf("没中毒时伤害 = %d，期望 10", got)
	}
}

// TestPoisonResisted 守住抗毒判定（Magic.pas:364 `Random(抗毒 + 7) <= 6`）。
func TestPoisonResisted(t *testing.T) {
	old := delphi.RandN
	defer func() { delphi.RandN = old }()

	// 抗毒 0：Random(7) ∈ [0,6] ⇒ 永远不抵抗（这就是"怪必中"的原因）
	for roll := 0; roll <= 6; roll++ {
		delphi.RandN = func(int) int { return roll }
		if poisonResisted(0) {
			t.Errorf("抗毒 0、掷出 %d 时不该抵抗", roll)
		}
	}
	// 掷出 7 就抵抗（抗毒 > 0 时会掷到）
	delphi.RandN = func(int) int { return 7 }
	if !poisonResisted(1) {
		t.Error("Random(8) 掷出 7 应抵抗")
	}
	// 边界：恰好 6 不抵抗、7 抵抗
	delphi.RandN = func(int) int { return 6 }
	if poisonResisted(100) {
		t.Error("掷出 6 不该抵抗（判据是 >6）")
	}
}

// TestPoisonTickPoint 守住"每跳伤害"公式：
//
//	nPoint := ROUND(btLevel / 3 * (nPower / AmyOunsulPoint))
//	（AmyOunsulPoint 官方 !setup.txt = 10）
func TestPoisonTickPoint(t *testing.T) {
	for _, c := range []struct {
		level  uint32
		nPower int
		want   int
	}{
		{3, 40, 4},  // 1 * 4.0
		{3, 30, 3},  // 1 * 3.0（红毒）
		{0, 40, 0},  // 0 级：等级项为 0
		{1, 40, 1},  // 1/3 * 4 = 1.33 → 1
		{2, 40, 3},  // 2/3 * 4 = 2.67 → 3
		{9, 40, 12}, // 3 * 4.0
	} {
		if got := poisonTickPoint(c.level, c.nPower); got != c.want {
			t.Errorf("等级 %d、威力 %d ⇒ 每跳 %d，期望 %d", c.level, c.nPower, got, c.want)
		}
	}
}

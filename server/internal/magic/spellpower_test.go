package magic

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
)

// packMM 把 MinMax 打成原版的 Word（Lo=下限，Hi=上限）。
func packMM(lo, hi uint16) uint32 { return uint32(lo) | uint32(hi)<<16 }

// withFixedRnd 把随机源换成确定性实现，返回还原函数（这些公式的分支全靠固定输入才验得动）。
func withFixedRnd(f func(int) int) func() {
	old := delphi.RandN
	delphi.RandN = f
	return func() { delphi.RandN = old }
}

func fixedZero(int) int { return 0 }

func fixedMax(n int) int { return n - 1 }

func TestGetPowerMatchesDelphi(t *testing.T) {
	defer withFixedRnd(fixedZero)()

	// 火球术(1)：官方数据 def 2/2、power 8/8 ⇒ 两个 Random 恒 0。
	// 原版：ROUND(8/4*(lvl+1)) + (2 + Random(0))
	info := &data.MagicInfo{MagicID: 1, DefPower: 2, DefMaxPower: 2, Power: 8, MaxPower: 8}
	for _, c := range []struct {
		level uint32
		want  int
	}{{0, 4}, {1, 6}, {2, 8}, {3, 10}} {
		if got := GetPower(8, info, c.level); got != c.want {
			t.Errorf("火球术 %d 级: GetPower = %d, 期望 %d", c.level, got, c.want)
		}
	}

	// 治愈术(2)：def 0/0、power 14/20。
	// ⚠️ 旧实现有"def==0 就退化用 Power/MaxPower"的兜底，等于 0 级就吃满级治疗量；
	// 原版没这层，0 级只有 ROUND(14/4)=4 点。固定 Random(0) ⇒ roll=14。
	heal := &data.MagicInfo{MagicID: 2, DefPower: 0, DefMaxPower: 0, Power: 14, MaxPower: 20}
	if got := MPow(heal); got != 14 {
		t.Errorf("MPow(治愈术) = %d, 期望 14（Random(6) 取 0）", got)
	}
	// 2 级：ROUND(14/4*3) = ROUND(10.5) = **10**（银行家舍入，10 是偶数）。
	// 用 math.Round 会得到 11 —— 这就是必须用 delphi.Round 的原因。
	for _, c := range []struct {
		level uint32
		want  int
	}{{0, 4}, {1, 7}, {2, 10}, {3, 14}} {
		if got := GetPower(MPow(heal), heal, c.level); got != c.want {
			t.Errorf("治愈术 %d 级: GetPower = %d, 期望 %d", c.level, got, c.want)
		}
	}
}

func TestGetPowerDefRandomIsHalfOpen(t *testing.T) {
	// Delphi 的 Random(n) 是 [0,n)：DefPower + Random(DefMaxPower-DefPower)
	// **永远取不到 DefMaxPower**。这条守住"别把它写成闭区间"。
	defer withFixedRnd(fixedMax)()
	info := &data.MagicInfo{MagicID: 24, DefPower: 10, DefMaxPower: 30, Power: 10, MaxPower: 30}

	// 3 级：ROUND(roll/4*4)=roll，加 DefPower+Random(20)=10+19=29。
	for _, roll := range []int{10, 20, 29} {
		got := GetPower(roll, info, 3)
		if want := roll + 10 + 19; got != want {
			t.Errorf("GetPower(%d) = %d, 期望 %d", roll, got, want)
		}
		if got >= roll+30 {
			t.Errorf("GetPower(%d) = %d 达到了 DefMaxPower（应为半开区间）", roll, got)
		}
	}
}

func TestGetPower13TwoThirdsScales(t *testing.T) {
	// 困魔咒用 GetPower13(40)：d10=40/3 固定、d18=40-40/3 按技能等级缩放。
	defer withFixedRnd(fixedZero)()
	info := &data.MagicInfo{MagicID: 16}

	// d10 = 13.333..., d18 = 26.666...
	//  0 级: ROUND(6.667 + 13.333) = 20
	//  1 级: ROUND(13.333 + 13.333) = ROUND(26.67) = 27
	//  2 级: ROUND(20 + 13.333) = ROUND(33.33) = 33
	//  3 级: ROUND(26.667 + 13.333) = 40
	for _, c := range []struct {
		level uint32
		want  int
	}{{0, 20}, {1, 27}, {2, 33}, {3, 40}} {
		if got := GetPower13(40, info, c.level); got != c.want {
			t.Errorf("GetPower13(40) %d 级 = %d, 期望 %d", c.level, got, c.want)
		}
	}

	// ⚠️ 对照：GetPower 同样吃 40 但**整体**缩放，0 级只有 10。
	// 两条公式在低等级差一倍，别混用。
	if got := GetPower(40, info, 0); got != 10 {
		t.Errorf("GetPower(40) 0 级 = %d, 期望 10（与 GetPower13 不同）", got)
	}
}

func TestGetRPowClosedInterval(t *testing.T) {
	// GetRPow 是**闭区间** [Lo,Hi]（Random(Hi-Lo+1)），与 Random 的半开区间不同。
	defer withFixedRnd(fixedMax)()
	if got := GetRPow(packMM(5, 20)); got != 20 {
		t.Errorf("GetRPow([5,20]) = %d, 期望 20（闭区间能取到 Hi）", got)
	}
	if got := GetRPow(packMM(20, 5)); got != 20 {
		t.Errorf("GetRPow([20,5]) = %d, 期望 20（Hi<=Lo 时直接取 Lo）", got)
	}
	if got := GetRPow(packMM(7, 7)); got != 7 {
		t.Errorf("GetRPow([7,7]) = %d, 期望 7", got)
	}
	if got := GetRPow(0); got != 0 {
		t.Errorf("GetRPow(0) = %d, 期望 0", got)
	}
}

func TestAttackPowerSpread(t *testing.T) {
	// GetAttackPower(base, nPower) = base + Random(nPower+1)（闭区间）。
	defer withFixedRnd(fixedMax)()
	if got := AttackPower(10, 0); got != 10 {
		t.Errorf("AttackPower(10,0) = %d, 期望 10（Random(1)=0）", got)
	}
	if got := AttackPower(10, 4); got != 14 {
		t.Errorf("AttackPower(10,4) = %d, 期望 14（Random(5) 上界 4）", got)
	}
	if got := AttackPower(10, -3); got != 10 {
		t.Errorf("AttackPower(10,-3) = %d, 期望 10（负 spread 归零，不 panic）", got)
	}
}

func TestMagStruckDamageUsesMACAndNoFloor(t *testing.T) {
	// ⚠️ 魔法伤害吃 **MAC** 且**没有保底 1**（保底 1 是物理那条路 GetHitStruckDamage）。
	// 旧实现拿 rollDamage(lo,hi,AC) 顶替，表现为"高魔防怪也有 1 点伤害"。
	defer withFixedRnd(fixedMax)()
	if got := MagStruckDamage(0, 7); got != 7 {
		t.Errorf("无魔防时 MagStruckDamage = %d, 期望 7", got)
	}
	// 固定 Random 取上界 ⇒ MAC 摇到 5，伤害 10-5=5。
	if got := MagStruckDamage(packMM(3, 5), 10); got != 5 {
		t.Errorf("MAC[3,5] 伤害 10 = %d, 期望 5（10-5）", got)
	}
	if got := MagStruckDamage(packMM(8, 12), 10); got != 0 {
		t.Errorf("MAC[8,12] 伤害 10 = %d, 期望 **0**（原版允许 0 伤害）", got)
	}
}

func TestSpellRawPowerEndToEnd(t *testing.T) {
	defer withFixedRnd(fixedZero)()
	// 火球术 0 级、MC=[2,6]：GetPower(8)=4，加 LoWord=2 ⇒ 6，
	// spread=(6-2)+1=5，Random(6)=0 ⇒ nPower=6。
	info := &data.MagicInfo{MagicID: 1, DefPower: 2, DefMaxPower: 2, Power: 8, MaxPower: 8}
	if got := SpellRawPower(info, 0, packMM(2, 6), 1); got != 6 {
		t.Errorf("SpellRawPower = %d, 期望 6", got)
	}

	// 治愈术 0 级、SC=[4,10]：GetPower(14)=4，加 4*2=8 ⇒ 12，
	// spread=(10-4)*2+1=13，Random(14)=0 ⇒ 12。
	heal := &data.MagicInfo{MagicID: 2, DefPower: 0, DefMaxPower: 0, Power: 14, MaxPower: 20}
	if got := HealRawPower(heal, 0, packMM(4, 10)); got != 12 {
		t.Errorf("HealRawPower = %d, 期望 12", got)
	}
}

func TestHealSpreadDiffersFromDamage(t *testing.T) {
	// 守住"治愈的 spread 是 (Hi-Lo)*2+1、伤害是 (Hi-Lo)+1"这个差异。
	// 合成一个函数会让高道击的治疗量系统性偏低。
	defer withFixedRnd(fixedMax)()
	heal := &data.MagicInfo{MagicID: 2, Power: 14, MaxPower: 20}
	dmg := &data.MagicInfo{MagicID: 1, DefPower: 2, DefMaxPower: 2, Power: 8, MaxPower: 8}
	sc := packMM(10, 20)
	// 治愈 3 级：MPow = 14+Random(6)=19 ⇒ GetPower(19)=19，加 10*2=20 得 39，
	// spread=(20-10)*2+1=21，Random(22) 上界 21 ⇒ 60
	if got := HealRawPower(heal, 3, sc); got != 60 {
		t.Errorf("HealRawPower = %d, 期望 60", got)
	}
	// 伤害 3 级 MC=[10,20]：GetPower(8)=10 + 10 = 20，spread=(20-10)+1=11，Random(12)=11 ⇒ 31
	if got := SpellRawPower(dmg, 3, sc, 1); got != 31 {
		t.Errorf("SpellRawPower = %d, 期望 31", got)
	}
}

func TestMagAnimalDiscount(t *testing.T) {
	// ObjBase.pas:4576：动物目标 nPower := Round(nPower / 1.2)
	for _, c := range []struct{ in, want int }{
		{10, 8}, {11, 9}, {12, 10}, {13, 11}, {100, 83}, {1, 1},
	} {
		if got := MagAnimalDiscount(c.in); got != c.want {
			t.Errorf("MagAnimalDiscount(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

func TestMagUndeadBonus(t *testing.T) {
	// Magic.pas:401：雷电术打不死系 nPower := ROUND(nPower * 1.5)
	// 11*1.5 = 16.5 ⇒ 银行家舍入得 **16**（16 是偶数），不是 17。
	for _, c := range []struct{ in, want int }{{10, 15}, {11, 16}, {1, 2}, {3, 4}} {
		if got := MagUndeadBonus(c.in); got != c.want {
			t.Errorf("MagUndeadBonus(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

func TestSpellPowerAttrOf(t *testing.T) {
	// 逐个核对 Magic.pas 的 case：法师系用 MC，灵魂火符(13) 用 SC。
	for _, id := range []uint16{1, 5, 9, 10, 11, 22, 23, 24, 33} {
		if got := AttrOf(id); got != PowerAttrMC {
			t.Errorf("技能 %d 用属性 = %v, 期望 MC", id, got)
		}
	}
	if got := AttrOf(13); got != PowerAttrSC {
		t.Errorf("灵魂火符 13 用属性 = %v, 期望 SC（道士系）", got)
	}
	if got := AttrOf(2); got != PowerAttrNone {
		t.Errorf("治愈术 2 用属性 = %v, 期望 None（走 HealRawPower）", got)
	}
}

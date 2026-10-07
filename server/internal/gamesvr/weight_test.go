package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// wItem 造一个带重量的物品模板（负重系统全靠 StdItem.Weight）。
func wItem(idx int32, name string, stdMode, weight uint8, ac, mac data.MinMax) *data.StdItem {
	return &data.StdItem{
		Index: idx, Name: name, StdMode: stdMode, Weight: weight,
		AC: ac, MAC: mac,
	}
}

// weightTestServer 造一个带物品表的最小 Server + 玩家。
func weightTestServer(t *testing.T, items ...*data.StdItem) (*Server, *Player) {
	t.Helper()
	set, err := data.NewStdItemSet(items)
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	p := newTestPlayer(1, "负重测试", entity.JobWarr)
	p.Char.Data.HumItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	return &Server{data: dataState{tables: &data.Tables{Items: set}}}, p
}

// TestBaseMaxWeights 守住 RecalcLevel 的三个上限基数（ObjBase.pas:1885-1930）。
//
// ⚠️ 两个最容易照抄错的点都在这里钉住：
//  1. `Round((nLevel / 4) * nLevel)` 的 `/` 是 **Delphi 实数除** ⇒ 等价 lv²/4。
//     用 level=7 验证：49/4 = 12.25 → 12（若误写成整除 7/4=1，会得 7 ⇒ 总量 57 而非 62）。
//  2. Delphi 的 `Round` 是 **银行家舍入**：lv=5 的 25/50 = 0.5 → **0**（不是 1）。
func TestBaseMaxWeights(t *testing.T) {
	cases := []struct {
		job        uint32
		level      uint32
		w, wear, h int
	}{
		// 战士：lv²/3、lv²/20、lv²/13
		{entity.JobWarr, 1, 50, 15, 12},
		{entity.JobWarr, 7, 50 + 16, 15 + 2, 12 + 4},
		{entity.JobWarr, 50, 50 + 833, 15 + 125, 12 + 192},
		// 法师：lv²/5、lv²/100、lv²/90
		{entity.JobWizard, 7, 50 + 10, 15 + 0, 12 + 1},
		// 道士：lv²/4、lv²/50、lv²/42（★ 实数除的关键用例）
		{entity.JobTaos, 7, 50 + 12, 15 + 1, 12 + 1},
	}
	for _, c := range cases {
		w, wear, h := baseMaxWeights(c.level, c.job)
		if w != c.w || wear != c.wear || h != c.h {
			t.Errorf("职业 %d 等级 %d：得到 %d/%d/%d，期望 %d/%d/%d",
				c.job, c.level, w, wear, h, c.w, c.wear, c.h)
		}
	}
	// 银行家舍入：道士 lv=5 ⇒ 25/50 = 0.5 ⇒ Round=0（普通四舍五入会得 1）
	if _, wear, _ := baseMaxWeights(5, entity.JobTaos); wear != 15 {
		t.Errorf("道士 lv=5 的佩带上限 = %d，期望 15（Delphi Round(0.5)=0，银行家舍入）", wear)
	}
}

// TestWeightStdMode62 守住负重类首饰的上限加成（ItmUnit.pas:626-631）：
//
//	HandWeight += AC2;  Weight += MAC;  WearWeight += MAC2
func TestWeightStdMode62(t *testing.T) {
	srv, p := weightTestServer(t,
		wItem(1, "力量腰带", 62, 9, data.MinMax{Min: 1, Max: 3}, data.MinMax{Min: 5, Max: 7}),
	)
	p.Char.Data.Abil.Level = 10
	p.Char.Data.HumItems[proto.SlotBelt] = itemUser(1, nil)

	w, wear, hand := srv.playerAddAbil(p).weight, srv.playerAddAbil(p).wearWeight, srv.playerAddAbil(p).handWeight
	if w != 5 || wear != 7 || hand != 3 {
		t.Fatalf("StdMode 62 加成 = Weight %d / WearWeight %d / HandWeight %d，期望 5/7/3", w, wear, hand)
	}
	_, _, _, maxW, maxWear, maxHand := srv.playerWeightInfo(p)
	bw, bwear, bhand := baseMaxWeights(10, entity.JobWarr)
	if maxW != bw+5 || maxWear != bwear+7 || maxHand != bhand+3 {
		t.Errorf("三个上限 = %d/%d/%d，期望 %d/%d/%d",
			maxW, maxWear, maxHand, bw+5, bwear+7, bhand+3)
	}
}

// TestWeightMuscleRing 守住超负载戒指（Shape 119）：三个上限**各自翻倍**，
// 且必须在装备加成**之后**（ObjBase.pas:3459-3464 —— `Inc(x, x)`）。
func TestWeightMuscleRing(t *testing.T) {
	muscle := wItem(2, "超负载戒指", 22, 1, data.MinMax{}, data.MinMax{})
	// ⚠️ 超负载戒指的判定看的是 **Shape 119**（`m_boMuscleRing`），不是 StdMode
	muscle.Shape = 119
	srv, p := weightTestServer(t,
		wItem(1, "布衣(男)", 10, 6, data.MinMax{}, data.MinMax{}),
		muscle,
		wItem(3, "力量腰带", 62, 9, data.MinMax{Min: 1, Max: 3}, data.MinMax{Min: 5, Max: 7}),
	)
	p.Char.Data.Abil.Level = 10
	// 先只戴腰带：上限 = 基数 + 装备加成
	p.Char.Data.HumItems[proto.SlotBelt] = itemUser(3, nil)
	srv.refreshSpecials(p)
	_, _, _, base1, _, _ := srv.playerWeightInfo(p)
	bw, _, _ := baseMaxWeights(10, entity.JobWarr)
	if base1 != bw+5 {
		t.Fatalf("未戴超负载戒指时 MaxWeight = %d，期望 %d", base1, bw+5)
	}
	// 戴上超负载戒指：应为**加成后**再翻倍 = (基数+5)*2
	p.Char.Data.HumItems[proto.SlotRingL] = itemUser(2, nil)
	srv.refreshSpecials(p)
	_, _, _, doubled, _, _ := srv.playerWeightInfo(p)
	if doubled != (bw+5)*2 {
		t.Errorf("戴超负载戒指后 MaxWeight = %d，期望 %d（(基数%d + 5) × 2）",
			doubled, (bw+5)*2, bw)
	}
}

// TestWeightBagAndWearSplit 守住三种"当前负重"的来源：
//
//	背包负重 = Σ 背包物品（RecalcBagWeight，:18533）
//	手负重   = 武器槽
//	佩带负重 = 其余已穿戴（含衣服）
func TestWeightBagAndWearSplit(t *testing.T) {
	srv, p := weightTestServer(t,
		wItem(1, "木剑", 5, 4, data.MinMax{}, data.MinMax{}),
		wItem(2, "布衣(男)", 10, 6, data.MinMax{}, data.MinMax{}),
		wItem(3, "金创药(小量)", 0, 1, data.MinMax{}, data.MinMax{}),
	)
	p.Char.Data.Abil.Level = 10
	p.Char.Data.BagItems[0] = itemUser(3, nil) // 药 1
	p.Char.Data.BagItems[1] = itemUser(3, nil) // 药 1
	p.Char.Data.HumItems[proto.SlotWeapon] = itemUser(1, nil)
	p.Char.Data.HumItems[proto.SlotDress] = itemUser(2, nil)

	bag, wear, hand, _, _, _ := srv.playerWeightInfo(p)
	if bag != 2 {
		t.Errorf("背包负重 = %d，期望 2（两瓶药各 1；已穿戴的不算进背包）", bag)
	}
	if hand != 4 {
		t.Errorf("手负重 = %d，期望 4（武器）", hand)
	}
	if wear != 6 {
		t.Errorf("佩带负重 = %d，期望 6（衣服）", wear)
	}

	// applyWeights 要写回存档（客户端属性包直接读这些字段）
	srv.applyWeights(p)
	a := p.Char.Data.Abil
	if a.Weight != 2 || a.WearWeight != 6 || a.HandWeight != 4 {
		t.Errorf("写回后 = %d/%d/%d，期望 2/6/4", a.Weight, a.WearWeight, a.HandWeight)
	}
	if a.MaxWeight == 0 || a.MaxWearWeight == 0 || a.MaxHandWeight == 0 {
		t.Errorf("三个上限不该是 0：%d/%d/%d", a.MaxWeight, a.MaxWearWeight, a.MaxHandWeight)
	}
}

// TestPickBlockedByWeight 守住拾取判定（IsAddWeightAvailable，:2085-2091）与文案。
func TestPickBlockedByWeight(t *testing.T) {
	srv, p := weightTestServer(t,
		wItem(1, "金创药(小量)", 0, 1, data.MinMax{}, data.MinMax{}),
		wItem(2, "重甲", 10, 200, data.MinMax{}, data.MinMax{}),
	)
	p.Char.Data.Abil.Level = 1
	// 1 级战士：MaxWeight = 50
	if ok, over := srv.weightAllowed(p, 49); !ok || over != 0 {
		t.Errorf("49 点负重应允许（上限 50），得到 ok=%v over=%d", ok, over)
	}
	if ok, over := srv.weightAllowed(p, 60); ok || over != 10 {
		t.Errorf("60 点负重应拒绝且超出 10，得到 ok=%v over=%d", ok, over)
	}
	if !srv.pickBlockedByWeight(p, 2) {
		t.Error("200 重量的物品应被负重挡住")
	}
	if srv.pickBlockedByWeight(p, 1) {
		t.Error("1 重量的物品不该被挡")
	}
}

// TestTakeOnBlockedByWeight 守住佩带判定（ClientTakeOnItems，:22985-22998）：
//
//	武器槽 ⇒ 单件重量比 **手负重**
//	其余槽 ⇒ 新物品 + **该槽原有物品** 比 **佩带负重**
func TestTakeOnBlockedByWeight(t *testing.T) {
	srv, p := weightTestServer(t,
		wItem(1, "铁剑", 5, 30, data.MinMax{}, data.MinMax{}),
		wItem(2, "重衣", 10, 20, data.MinMax{}, data.MinMax{}),
		wItem(3, "轻衣", 10, 5, data.MinMax{}, data.MinMax{}),
	)
	p.Char.Data.Abil.Level = 1
	// 1 级战士：MaxHandWeight = 12、MaxWearWeight = 15
	if msg := srv.takeOnBlockedByWeight(p, srv.data.tables.Items.Get(0), proto.SlotWeapon); msg != msgHandWeightNot {
		t.Errorf("武器重 30 > 手负重 12 应回 %q，得到 %q", msgHandWeightNot, msg)
	}
	if msg := srv.takeOnBlockedByWeight(p, srv.data.tables.Items.Get(2), proto.SlotDress); msg != "" {
		t.Errorf("轻衣重 5 ≤ 佩带 15 应允许，得到 %q", msg)
	}
	// 该槽原有物品也要算：已有 5 重的衣服，再穿 20 重的 ⇒ 5+20 > 15 ⇒ 拒绝
	p.Char.Data.HumItems[proto.SlotDress] = itemUser(3, nil)
	if msg := srv.takeOnBlockedByWeight(p, srv.data.tables.Items.Get(1), proto.SlotDress); msg != msgWearWeightNot {
		t.Errorf("换上重衣（5+20 > 15）应回 %q，得到 %q", msgWearWeightNot, msg)
	}
}

// TestEquipRaisesMaxWeight 守住"装备 StdMode 62 会抬高上限"这条端到端链路：
// 上限随装备变化后，applyWeights 写回、且 SM_ABILITY 能拿到新值。
func TestEquipRaisesMaxWeight(t *testing.T) {
	srv, p := weightTestServer(t,
		wItem(1, "力量腰带", 62, 9, data.MinMax{Min: 1, Max: 5}, data.MinMax{Min: 8, Max: 8}),
	)
	p.Char.Data.Abil.Level = 20
	srv.refreshSpecials(p)
	srv.applyWeights(p)
	before := p.Char.Data.Abil.MaxWeight

	p.Char.Data.HumItems[proto.SlotBelt] = itemUser(1, nil)
	srv.refreshSpecials(p)
	srv.applyWeights(p)
	after := p.Char.Data.Abil.MaxWeight
	if after != before+8 {
		t.Errorf("戴力量腰带后 MaxWeight = %d，期望 %d（+MAC=8）", after, before+8)
	}
	ab := abilityFromPB(p.Char.Data.Abil)
	if uint32(ab.MaxWeight) != after {
		t.Errorf("属性包里的 MaxWeight = %d，期望 %d", ab.MaxWeight, after)
	}
}

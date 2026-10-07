package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// wuItem 造物品模板（Index 必须等于切片位置 + 1，见 NewStdItemSet）。
func wuItem(idx int32, name string, stdMode uint8, dc data.MinMax) *data.StdItem {
	return &data.StdItem{Index: idx, Name: name, StdMode: stdMode, DC: dc}
}

// wuNewBag 给玩家一个空背包。
func wuNewBag(p *Player) {
	p.Char.Data.BagItems = make([]*pb.UserItem, entity.MaxBagSize)
}

// wuBagHas 报告背包里还有没有 MakeIndex == mk 的物品。
//
// ⚠️ 不能只看某一格：`takeBagItem` 保持"紧凑前缀 + 空尾巴"，删掉中间一格
// 后面的会左移过来。
func wuBagHas(p *Player, mk int32) bool {
	for _, it := range p.Char.Data.BagItems {
		if it != nil && it.MakeIndex == mk {
			return true
		}
	}
	return false
}

// TestCalcUpgradePointsBlackStone 黑铁矿石折算：
//
//	btDura := Round(min(5,n) + min(5,n) * ((nDura/n) / 5.0))
//
// 一块 Dura=20000（千分制 ⇒ 品质 20）⇒ Round(1 + 1*(20/5)) = 5。
func TestCalcUpgradePointsBlackStone(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	wuNewBag(p)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 7, Dura: 20000}

	dc, sc, mc, dur := s.calcUpgradePoints(p)
	if dur != 5 {
		t.Errorf("btDura = %d，期望 5（1 块品质 20 的黑铁）", dur)
	}
	if dc != 0 || sc != 0 || mc != 0 {
		t.Errorf("没有首饰时三系点数应为 0，实际 DC=%d SC=%d MC=%d", dc, sc, mc)
	}
	if wuBagHas(p, 7) {
		t.Error("黑铁矿石该被收走")
	}
}

// TestCalcUpgradePointsTopFiveBlackStones 黑铁按**耐久从大到小取前 5 块**。
//
// 品质 6/5/4/3/2/1 ⇒ 前五 = 6+5+4+3+2 = 20，n = 5
// ⇒ Round(5 + 5*((20/5)/5)) = Round(5 + 5*0.8) = 9。
func TestCalcUpgradePointsTopFiveBlackStones(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	wuNewBag(p)
	for i, quality := range []uint32{6, 5, 4, 3, 2, 1} {
		p.Char.Data.BagItems[i] = &pb.UserItem{
			Index: 1, MakeIndex: int32(i + 1), Dura: quality * 1000,
		}
	}
	_, _, _, dur := s.calcUpgradePoints(p)
	if dur != 9 {
		t.Errorf("btDura = %d，期望 9（6 块里取品质最高的 5 块：6+5+4+3+2）", dur)
	}
}

// TestCalcUpgradePointsAccessory 首饰按 StdMode 取 DC/SC/MC 的"最大 + 次大"：
//
//	btDc := nDcMin div 5 + nDcMax div 3
//
// 两件项链（StdMode 19，DC 5-10 ⇒ 15；DC 3-6 ⇒ 9）⇒ 15/5 + 9/3 = 6。
func TestCalcUpgradePointsAccessory(t *testing.T) {
	acc1 := wuItem(1, "测试项链", 19, data.MinMax{Min: 5, Max: 10})
	acc2 := wuItem(2, "测试戒指", 23, data.MinMax{Min: 3, Max: 6})
	s, p := eatTestServer(t, acc1, acc2)
	wuNewBag(p)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 11}
	p.Char.Data.BagItems[1] = &pb.UserItem{Index: 2, MakeIndex: 12}

	dc, _, _, _ := s.calcUpgradePoints(p)
	if dc != 6 {
		t.Errorf("btDc = %d，期望 6（15/5 + 9/3）", dc)
	}
	if wuBagHas(p, 11) || wuBagHas(p, 12) {
		t.Error("首饰该被收走")
	}
}

// TestCalcUpgradePointsKeepsOtherItems 非黑铁、非首饰的物品**不动**。
func TestCalcUpgradePointsKeepsOtherItems(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	potion := wuItem(2, "金创药(小量)", 0, data.MinMax{})
	s, p := eatTestServer(t, stone, potion)
	wuNewBag(p)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 7, Dura: 5000}
	p.Char.Data.BagItems[1] = &pb.UserItem{Index: 2, MakeIndex: 8, Dura: 3, DuraMax: 3}

	s.calcUpgradePoints(p)
	if !wuBagHas(p, 8) {
		t.Error("药品不该被收走")
	}
}

// TestRollUpgradedWeaponEqualPointsQuirk 钉住原版那个**反直觉**的行为：
// 三个属性分支是三个**独立 if**，三系点数全相等时（典型的：一块首饰都没放）
// 三个条件同时成立 ⇒ DC、MC、SC 依次执行，**最后 SC 的结果留下**。
//
// 全 0 + 抽签必中 ⇒ 最终 `btValue[10] = 30`（SC +1），而不是 10（DC +1）。
func TestRollUpgradedWeaponEqualPointsQuirk(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	w := &pb.UserItem{Index: 1, Dura: 1000, DuraMax: 10000, Value: make([]byte, entity.ItemValueLen)}
	up := &weaponUpgrade{dc: 0, sc: 0, mc: 0, dura: 20}

	restore := withRnd(func(int) int { return 0 }) // 所有抽签都"中"
	defer restore()
	s.rollUpgradedWeapon(p, w, up)

	if got := btValueAt(w, btValueUpgrade); got != 30 {
		t.Errorf("三系全 0 时 btValue[10] = %d，期望 30（三个分支都跑、SC 最后覆盖）", got)
	}
	// 耐久：dura=20 ≥ 18 ⇒ Random(20-18)=0 ⇒ 落在 `8..255` ⇒ +4000
	if w.DuraMax != 14000 {
		t.Errorf("DuraMax = %d，期望 14000（+4000）", w.DuraMax)
	}
}

// TestRollUpgradedWeaponFailMeansBroken 抽不中 ⇒ `btValue[10] = 1` ⇒ 下一刀破碎。
func TestRollUpgradedWeaponFailMeansBroken(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	w := &pb.UserItem{Index: 1, Dura: 1000, DuraMax: 10000, Value: make([]byte, entity.ItemValueLen)}
	up := &weaponUpgrade{dura: 16} // 16..17 不落在任何耐久分支 ⇒ 耐久不变

	restore := withRnd(func(int) int { return 99 }) // 99 < n10(=10) 恒不成立
	defer restore()
	s.rollUpgradedWeapon(p, w, up)

	if got := btValueAt(w, btValueUpgrade); got != 1 {
		t.Errorf("抽不中时 btValue[10] = %d，期望 1（破碎）", got)
	}
	if w.DuraMax != 10000 {
		t.Errorf("dura=16 时耐久不该变，实际 DuraMax=%d", w.DuraMax)
	}
}

// TestRollUpgradedWeaponBadDura 黑铁太少（btDura ≤ 8）⇒ 武器**掉耐久上限**：
// `if DuraMax > 3000 then Dec(3000) else DuraMax shr 1`（ObjNpc.pas:1288-1295）。
func TestRollUpgradedWeaponBadDura(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	restore := withRnd(func(int) int { return 99 })
	defer restore()

	w := &pb.UserItem{Index: 1, Dura: 9000, DuraMax: 10000, Value: make([]byte, entity.ItemValueLen)}
	s.rollUpgradedWeapon(p, w, &weaponUpgrade{dura: 0})
	if w.DuraMax != 7000 {
		t.Errorf("DuraMax = %d，期望 7000（-3000）", w.DuraMax)
	}

	// DuraMax ≤ 3000 ⇒ 减半
	w2 := &pb.UserItem{Index: 1, Dura: 3000, DuraMax: 3000, Value: make([]byte, entity.ItemValueLen)}
	s.rollUpgradedWeapon(p, w2, &weaponUpgrade{dura: 5})
	if w2.DuraMax != 1500 {
		t.Errorf("DuraMax = %d，期望 1500（3000 shr 1）", w2.DuraMax)
	}
	// Dura 被夹到 DuraMax
	w3 := &pb.UserItem{Index: 1, Dura: 3000, DuraMax: 3000, Value: make([]byte, entity.ItemValueLen)}
	s.rollUpgradedWeapon(p, w3, &weaponUpgrade{dura: 0})
	if w3.Dura != w3.DuraMax {
		t.Errorf("Dura(%d) 该被夹到 DuraMax(%d)", w3.Dura, w3.DuraMax)
	}
}

// TestSettleWeaponUpgrade 试刀结算：
//
//	mark 10..13 ⇒ btValue[0] += mark-9（DC 上限）
//	mark 1      ⇒ 武器破碎（从装备栏摘掉）
//	三系和 ≥ 20 ⇒ 也破碎（原版 `nUpgradeWeaponMaxPoint`）
//
// 结算后 `btValue[10] := 0`。
func TestSettleWeaponUpgrade(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)

	// ① 成功：DC +2（mark 11）
	w := &pb.UserItem{Index: 1, Value: make([]byte, entity.ItemValueLen)}
	setBtValue(w, btValueUpgrade, 11)
	p.Char.Data.HumItems[proto.SlotWeapon] = w
	s.settleWeaponUpgrade(p)
	if got := btValueAt(w, 0); got != 2 {
		t.Errorf("DC 加值 = %d，期望 2（mark 11 ⇒ +2）", got)
	}
	if got := btValueAt(w, btValueUpgrade); got != 0 {
		t.Errorf("结算后标记该清零，实际 %d", got)
	}
	if p.Char.Data.HumItems[proto.SlotWeapon] == nil || p.Char.Data.HumItems[proto.SlotWeapon].Index == 0 {
		t.Error("成功时武器不该消失")
	}

	// ② 失败（mark 1）⇒ 破碎、摘掉
	w2 := &pb.UserItem{Index: 1, Value: make([]byte, entity.ItemValueLen)}
	setBtValue(w2, btValueUpgrade, 1)
	p.Char.Data.HumItems[proto.SlotWeapon] = w2
	s.settleWeaponUpgrade(p)
	if p.Char.Data.HumItems[proto.SlotWeapon].Index != 0 {
		t.Error("mark=1 时武器该破碎（从装备栏摘掉）")
	}

	// ③ 三系和到上限 ⇒ 即使抽中成功也碎
	w3 := &pb.UserItem{Index: 1, Value: make([]byte, entity.ItemValueLen)}
	setBtValue(w3, btValueUpgrade, 10)
	setBtValue(w3, 0, byte(upgradeWeaponMaxPoint))
	p.Char.Data.HumItems[proto.SlotWeapon] = w3
	s.settleWeaponUpgrade(p)
	if p.Char.Data.HumItems[proto.SlotWeapon].Index != 0 {
		t.Errorf("三系和 ≥ %d 时该破碎", upgradeWeaponMaxPoint)
	}

	// ④ 没有标记 ⇒ 什么都不做
	w4 := &pb.UserItem{Index: 1, Value: make([]byte, entity.ItemValueLen)}
	p.Char.Data.HumItems[proto.SlotWeapon] = w4
	s.settleWeaponUpgrade(p)
	if p.Char.Data.HumItems[proto.SlotWeapon].Index == 0 {
		t.Error("没有待结算标记时不该动武器")
	}
}

// TestUpgradeSlotAndFind 修炼列表的存取（按角色名找）。
func TestUpgradeSlotAndFind(t *testing.T) {
	stone := wuItem(1, blackStoneName, 43, data.MinMax{})
	s, p := eatTestServer(t, stone)
	const npcID = uint32(42)
	if len(s.upgradeSlot(npcID)) != 0 {
		t.Fatal("初始应为空")
	}
	s.upgradeSet(npcID, []*weaponUpgrade{{userName: p.Char.Name}})
	if got := findUpgrade(s.upgradeSlot(npcID), p.Char.Name); got != 0 {
		t.Errorf("应找到第 0 条，实际 %d", got)
	}
	if got := findUpgrade(s.upgradeSlot(npcID), "别人"); got != -1 {
		t.Errorf("不该找到别人的记录，实际 %d", got)
	}
	s.upgradeSet(npcID, nil)
	if len(s.upgradeSlot(npcID)) != 0 {
		t.Error("置空后应删除该 NPC 的键")
	}
}

// TestBodyLuckLevel 钉住 `m_nBodyLuckLevel` 的换算与夹取（ObjBase.pas:2387-2390）。
func TestBodyLuckLevel(t *testing.T) {
	cases := []struct {
		bodyLuck float64
		want     int
	}{
		{0, 0},
		{bodyLuckUnit, 1},
		{2.5 * bodyLuckUnit, 2},
		{99 * bodyLuckUnit, 5},    // 上限 +5
		{-99 * bodyLuckUnit, -10}, // 下限 -10
	}
	for _, c := range cases {
		d := &pb.CharacterData{BodyLuck: c.bodyLuck}
		if got := bodyLuckLevel(d); got != c.want {
			t.Errorf("bodyLuckLevel(%v) = %d，期望 %d", c.bodyLuck, got, c.want)
		}
	}
	if bodyLuckLevel(nil) != 0 {
		t.Error("nil 数据应返回 0")
	}
}

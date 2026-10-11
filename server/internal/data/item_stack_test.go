package data

import "testing"

// TestStackLimitByStdMode 可堆叠判定**只看 StdMode** —— 肉(40)与矿石(43)除外。
//
// ⚠️ 这条钉的是两个真实踩过的坑（2026-10-11）：
//
//  1. 把 40/43 当可堆叠 ⇒ 卖肉走"只卖 1 个、Dura-1"：**肉不消失、钱照加**
//     （用户报的 P0，服务端侧见 `TestShopSellMeatRemovesIt`）；
//  2. 反过来"看 `DuraMax > 1` 就不可堆叠" ⇒ **误伤 25=护身符/药粉**：它们的
//     `Dura` 是**个数×100**（`CheckAmulet` 按 `ROUND(Dura/100)` 判数量），
//     `DuraMax` 高达 5000~20000，但它们确实可堆叠。
//
// 所以判据只能是 StdMode 这张表。
func TestStackLimitByStdMode(t *testing.T) {
	cases := []struct {
		name      string
		mode      uint8
		duraMax   uint32
		stackable bool
	}{
		{"药水", 0, 0, true},
		{"卷轴", 3, 0, true},
		{"护身符(个数×100，DuraMax 很大但可堆叠)", 25, 10000, true},
		{"灰色药粉", 25, 5000, true},
		{"护身符(大)", 25, 20000, true},
		{"药包", 31, 1, true},
		{"金币/特殊", 41, 1, true},
		{"材料", 42, 1, true},
		{"肉（Dura=质量）", 40, 4000, false},
		{"鸡肉（Dura=质量）", 40, 4000, false},
		{"矿石（Dura=纯度）", 43, 10000, false},
		{"武器", 5, 10000, false},
		{"衣服", 10, 10000, false},
	}
	for _, c := range cases {
		it := &StdItem{StdMode: c.mode, DuraMax: c.duraMax}
		got := it.StackLimit() > 0
		if got != c.stackable {
			t.Errorf("%s（StdMode=%d DuraMax=%d）：可堆叠=%v，期望 %v",
				c.name, c.mode, c.duraMax, got, c.stackable)
		}
	}
}

package data

import "testing"

// 物品表的**一致性 lint**（架构方向 ③）。
//
// 为什么要它：**P0（卖肉不扣包）本质是数据语义错** —— 肉 `DuraMax=4000` 却被
// 当成可堆叠（`UserItem.Dura` = 数量），而挖肉往 `Dura` 里写的是**肉质量** ⇒
// 卖出走"只卖 1 个、质量 -1"那条路：**肉不消失、钱照加**。
//
// 那种错**代码上看不出来**（逻辑自洽），只有把「数据」和「规则的假设」放一起
// 比对才现形 ⇒ 让它变成一条**会失败的测试**，而不是等人踩。
//
// ⚠️ 这里只报**自相矛盾**的组合；"这样配行不行"由设计决定，lint 不管。

// TestStdItemsLint 扫整张物品表，把"规则会误读"的组合挑出来。
func TestStdItemsLint(t *testing.T) {
	tb, err := LoadDir(seedDir)
	if err != nil {
		t.Fatalf("LoadDir(%q): %v", seedDir, err)
	}
	items := tb.Items.items
	if len(items) == 0 {
		t.Fatal("物品表是空的 —— 数据没加载？")
	}

	var stackedWithDura, pricedNoName, noName int
	for i, it := range items {
		// ① 可堆叠物不该有"耐久"语义：`Dura` 一旦被当成数量，`DuraMax > 1` 就意味着
		//    同一个字段被两种含义抢（数量 vs 耐久/质量/纯度）⇒ 正是 P0 那个坑。
		//
		//    ⚠️ **护身符/药粉（StdMode 25）是已知且合法的例外**：它们的 `Dura` 是
		//    「个数×100」（`amulet.go` 的 `CheckAmulet` / `UseAmulet`），确实可堆叠。
		if it.StackLimit() > 0 && it.DuraMax > 1 && it.StdMode != 25 {
			stackedWithDura++
			t.Errorf(
				"物品 idx=%d %q：StdMode=%d 可堆叠，但 DuraMax=%d ⇒ `Dura` 到底是数量还是耐久？"+
					"（肉/矿石那种「质量当数量」的坑，见 P0）",
				it.Index, it.Name, it.StdMode, it.DuraMax,
			)
		}

		// ② 有卖价却没名字 ⇒ 卖窗/商店会显示一行空白，玩家不知道自己在买什么。
		if it.Price > 0 && it.Name == "" {
			pricedNoName++
			t.Errorf("物品 idx=%d：有卖价 %d 却没有名字", it.Index, it.Price)
		}
		if it.Name == "" {
			noName++
		}
		_ = i
	}

	t.Logf("扫了 %d 件物品：可堆叠却带耐久 %d 件、有价无名 %d 件、无名共 %d 件",
		len(items), stackedWithDura, pricedNoName, noName)
}

// TestStackableModesHaveItems `stackableModes` 里列的每一类，表里都该真的有东西
// —— 免得改了规则却没人发现那一类是空的（= 规则写错了 / 数据没配）。
func TestStackableModesHaveItems(t *testing.T) {
	tb, err := LoadDir(seedDir)
	if err != nil {
		t.Fatalf("LoadDir(%q): %v", seedDir, err)
	}
	seen := map[uint8]int{}
	for _, it := range tb.Items.items {
		if it.StackLimit() > 0 {
			seen[it.StdMode]++
		}
	}
	if len(seen) == 0 {
		t.Fatal("一件可堆叠的物品都没有 ⇒ `StackLimit()` 或数据有问题")
	}
	for mode := range stackableModes {
		if seen[mode] == 0 {
			// 只提示不算错：某类暂时没配数据是可能的
			t.Logf("提示：StdMode=%d 在 stackableModes 里，但表里没有这类物品", mode)
		}
	}
	t.Logf("可堆叠的类别及件数：%v", seen)
}

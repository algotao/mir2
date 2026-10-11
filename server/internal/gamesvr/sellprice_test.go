package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// TestSellPriceFollowsQuality 肉(40)/矿石(43) 的卖价要**按质量打折**
// （原版 `GetUserItemPrice` + `GetSellItemPrice`，`ObjNpc.pas:1838-1878 / 2127-2130`）。
//
// 官方公式（`Dura <= DuraMax`）：
//
//	n10 = _MAX(2, Round(原价 - 原价/2/DuraMax*(DuraMax-Dura)))
//	卖价 = Round(n10 / 2)
//
// ⚠️ 2026-10-11 之前我们一律"原价/2" ⇒ 挖到的低质量肉也卖满价。
func TestSellPriceFollowsQuality(t *testing.T) {
	meat := &data.StdItem{StdMode: 40, DuraMax: 4000}
	meat.Price = 100

	// ① 满质量（Dura = DuraMax）⇒ 不打折：100/2 = 50
	if got := sellPriceOf(meat, &pb.UserItem{Dura: 4000, DuraMax: 4000}); got != 50 {
		t.Errorf("满质量肉卖价 = %d，期望 50", got)
	}
	// ② 半质量（Dura = 2000）⇒ 砍掉原价/2 的一半：n10 = 100-25 = 75 ⇒ 38
	//    （Round(75/2) = 38）
	if got := sellPriceOf(meat, &pb.UserItem{Dura: 2000, DuraMax: 4000}); got != 38 {
		t.Errorf("半质量肉卖价 = %d，期望 38", got)
	}
	// ③ 质量 0 ⇒ n10 = max(2, 50) = 50 ⇒ 25（砍到原价的一半，官方的 _MAX(2,…)）
	if got := sellPriceOf(meat, &pb.UserItem{Dura: 0, DuraMax: 4000}); got != 25 {
		t.Errorf("零质量肉卖价 = %d，期望 25", got)
	}

	// ④ 矿石：DuraMax 不足 10000 的按 10000 算（官方 `ObjNpc.pas:1869`）
	ore := &data.StdItem{StdMode: 43, DuraMax: 5000}
	ore.Price = 200
	// 纯度 5000、但下限抬到 10000 ⇒ n10 = 200 - 200/2/10000*5000 = 200-50 = 150 ⇒ 75
	if got := sellPriceOf(ore, &pb.UserItem{Dura: 5000, DuraMax: 5000}); got != 75 {
		t.Errorf("矿石卖价 = %d，期望 75（DuraMax 抬到 10000 后算）", got)
	}

	// ⑤ 普通物品不受影响（原价/2）
	sword := &data.StdItem{StdMode: 5, DuraMax: 10000}
	sword.Price = 300
	if got := sellPriceOf(sword, &pb.UserItem{Dura: 100, DuraMax: 10000}); got != 150 {
		t.Errorf("武器卖价 = %d，期望 150（不看质量）", got)
	}

	// ⑥ DuraMax = 0 ⇒ 官方外层判据不成立 ⇒ 不打折（也不会除零）
	if got := sellPriceOf(meat, &pb.UserItem{Dura: 10, DuraMax: 0}); got != 50 {
		t.Errorf("DuraMax=0 时卖价 = %d，期望 50（不打折，且不能除零）", got)
	}
}

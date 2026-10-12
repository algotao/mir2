package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/itemid"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 卖/修/用/穿都**按实例号认物**（`itemByMakeIndex`）⇒ 一个号只能对应一件东西。
//
// ⚠️ 这条钉的是 2026-10-12 用户报的「售出物品时，取包裹物品总是取第一格，
// 而无论鼠标选哪一格」：老存档里两件东西共用一个 `MakeIndex` 时，
// `itemByMakeIndex` 只会命中靠前那件（客户端每帧按号重绑手上的物品也一样）
// ⇒ 点第 N 格，卖掉的却是第 1 格那件。
func Test撞号的物品卖出后取的是点的那件(t *testing.T) {
	s := testSlaveServer()
	p := dealTestPlayer(1, "甲")
	// 包里三件东西，后两件与第一件**撞号**（重启归零的计数器发出来的）
	p.Char.Data.BagItems = []*pb.UserItem{
		{Index: 1, MakeIndex: 11, Dura: 1, DuraMax: 1},
		{Index: 2, MakeIndex: 11, Dura: 1, DuraMax: 1},
		{Index: 3, MakeIndex: 11, Dura: 1, DuraMax: 1},
	}
	// 修之前：按号找 ⇒ 只能找到第 0 格（这就是用户看到的"总是第一格"）
	if slot, _, ok := s.itemByMakeIndex(p, 11, "", false); slot != 0 || !ok {
		t.Fatalf("撞号时按号取物 = 槽 %d（ok=%v），该是第 0 格 —— 正是报的那个表现", slot, ok)
	}

	// 进世界时自检（`joinWorld`）⇒ 后两件换新号
	if n := itemid.Heal(p.Char.Data, &s.itemSeq); n != 2 {
		t.Fatalf("修了 %d 件，应为 2", n)
	}
	want := p.Char.Data.BagItems[2].MakeIndex
	if slot, _, ok := s.itemByMakeIndex(p, want, "", false); slot != 2 || !ok {
		t.Errorf("按第 3 件的号 %d 取物 = 槽 %d（ok=%v），应为 2", want, slot, ok)
	}
	// 第一件没被动 ⇒ 它的号还能指回它自己
	if slot, _, ok := s.itemByMakeIndex(p, 11, "", false); slot != 0 || !ok {
		t.Errorf("第 1 件的号不该变，实得槽 %d（ok=%v）", slot, ok)
	}
}

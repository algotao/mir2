package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// TestAmuletNeed 哪些技能要护身符、要几个（Magic.pas 的外层 case 13..19 / case 30）。
func TestAmuletNeed(t *testing.T) {
	need := map[uint32]int{13: 1, 14: 1, 15: 1, 16: 1, 17: 1, 18: 1, 19: 1, 30: 5}
	for id, want := range need {
		n, nType, ok := amuletNeed(id)
		if !ok {
			t.Errorf("技能 %d 应需要护身符", id)
			continue
		}
		if n != want {
			t.Errorf("技能 %d 需要 %d 个，期望 %d", id, n, want)
		}
		if nType != amuletTypeCharm {
			t.Errorf("技能 %d 类型 = %d，期望护身符类(%d)", id, nType, amuletTypeCharm)
		}
	}
	// 不需要的：治愈术2 / 火球1 / 火墙22 / 瞬移21 / 群疗29 / 圣言32
	for _, id := range []uint32{1, 2, 5, 9, 11, 22, 21, 29, 32, 33} {
		if _, _, ok := amuletNeed(id); ok {
			t.Errorf("技能 %d 不该要求护身符", id)
		}
	}
}

// TestAmuletCountOf Dura/100 = 个数（CheckAmulet 的 ROUND(Dura/100)）。
func TestAmuletCountOf(t *testing.T) {
	cases := []struct {
		dura uint32
		want int
	}{
		{0, 0}, {49, 0}, {50, 1}, {99, 1}, {100, 1},
		{149, 1}, {150, 2}, {10000, 100}, // 护身符满量 = 100 个
	}
	for _, c := range cases {
		if got := amuletCountOf(c.dura); got != c.want {
			t.Errorf("amuletCountOf(%d) = %d, 期望 %d", c.dura, got, c.want)
		}
	}
}

// TestItemStackAmuletCount 护身符报给客户端的**个数**要走 `ROUND(Dura/100)`。
//
// ⚠️ 护身符/毒药（StdMode 25）的 `Dura` 是"个数×100"（`CheckAmulet` 按
// `ROUND(Dura/100)` 判、`UseAmulet` 每次扣 100），`DuraMax` 是 10000（=100 个）。
// 直接把 Dura 当个数报出去 ⇒ 客户端显示"10000 个护身符"（2026-10-11 审计发现）。
func TestItemStackAmuletCount(t *testing.T) {
	amulet := wuItem(1, "护身符", amuletStdMode, data.MinMax{})
	amulet.DuraMax = 10000 // 官方：= 100 个
	s, p := butchTestServer(t, amulet)
	wuNewBag(p)
	p.Char.Data.BagItems[1] = &pb.UserItem{Index: 1, MakeIndex: 55, Dura: 10000, DuraMax: 10000}
	st, ok := s.itemStack(p.Char.Data.BagItems[1])
	if !ok {
		t.Fatal("该能构造 ItemStack")
	}
	if st.GetCount() != 100 {
		t.Errorf("护身符个数 = %d，期望 100（Dura 10000 / 100）", st.GetCount())
	}
	// 用掉一些：剩 3750 ⇒ 38 个（ROUND(37.5) = 38）
	p.Char.Data.BagItems[1].Dura = 3750
	st, _ = s.itemStack(p.Char.Data.BagItems[1])
	if st.GetCount() != 38 {
		t.Errorf("护身符个数 = %d，期望 38（ROUND(3750/100)）", st.GetCount())
	}
}

package gamesvr

import "testing"

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

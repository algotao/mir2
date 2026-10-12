package itemid

import (
	"testing"

	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

func TestNext从一号开始且单调递增(t *testing.T) {
	var s Seq
	if got := s.Next(); got != 1 {
		t.Fatalf("第一个号 = %d，应为 1", got)
	}
	if got := s.Next(); got != 2 {
		t.Fatalf("第二个号 = %d，应为 2", got)
	}
	if s.Water() != 2 {
		t.Errorf("水位 = %d，应为 2", s.Water())
	}
}

// 水位**只能涨**：读到存档里已经用到 500 ⇒ 之后发的号必须大于 500，
// 否则重启后重新从 1 发号就会和存档里的撞号（用户 2026-10-12 那条的根因）。
func TestObserve把水位抬起来(t *testing.T) {
	var s Seq
	s.Next() // 水位 1
	s.Observe(500)
	if got := s.Next(); got != 501 {
		t.Fatalf("抬到 500 之后发的号 = %d，应为 501", got)
	}
	// 再 Observe 一个更小的 ⇒ 不该把水位拽回去
	s.Observe(10)
	if got := s.Next(); got != 502 {
		t.Errorf("Observe 只涨不跌，实得 %d", got)
	}
}

// 老存档里"两件东西一个号"：修完之后每件一个号，且**没撞号的那些不动**。
func TestHeal换掉重复与空号(t *testing.T) {
	var s Seq
	d := &pb.CharacterData{
		HumItems: []*pb.UserItem{
			{Index: 1, MakeIndex: 7}, // 唯一 ⇒ 不动
			nil,                      // 空槽
			{Index: 2, MakeIndex: 0}, // 没号 ⇒ 补
		},
		BagItems: []*pb.UserItem{
			{Index: 3, MakeIndex: 9}, // 第一件（客户端/服务端按号找只会命中它）
			{Index: 4, MakeIndex: 9}, // ⚠️ 与上一件撞号
			{Index: 5, MakeIndex: 9}, // ⚠️ 同上
		},
		StorageItems: []*pb.UserItem{{Index: 6, MakeIndex: 3}},
	}
	fixed := Heal(d, &s)
	if fixed != 3 {
		t.Fatalf("修了 %d 件，应为 3（1 个空号 + 2 个重复）", fixed)
	}
	if d.HumItems[0].MakeIndex != 7 {
		t.Errorf("已唯一的号被改了：%d", d.HumItems[0].MakeIndex)
	}
	seen := map[int32]bool{}
	all := append(append([]*pb.UserItem{}, d.HumItems...), d.BagItems...)
	all = append(all, d.StorageItems...)
	for _, it := range all {
		if it == nil {
			continue
		}
		if it.MakeIndex == 0 {
			t.Fatal("还有没号的物品")
		}
		if seen[it.MakeIndex] {
			t.Fatalf("实例号 %d 仍然重复", it.MakeIndex)
		}
		seen[it.MakeIndex] = true
	}
	// 换的新号必须大于存档里已有的最大号（7/9/3）⇒ 不会和"下一件新东西"再撞
	for _, it := range []*pb.UserItem{d.HumItems[2], d.BagItems[1], d.BagItems[2]} {
		if it.MakeIndex <= 9 {
			t.Errorf("新号 %d 不大于已有最大号 9", it.MakeIndex)
		}
	}
}

func TestHeal空数据不动(t *testing.T) {
	var s Seq
	if n := Heal(nil, &s); n != 0 {
		t.Errorf("nil 数据 ⇒ 0，实得 %d", n)
	}
	if n := Heal(&pb.CharacterData{}, &s); n != 0 {
		t.Errorf("空角色 ⇒ 0，实得 %d", n)
	}
}

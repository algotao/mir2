package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/storage"
)

// 选角槽位的三条语义（用户 2026-10-10 第 1 条 + 之后那次"列表不出现人物了"）：
//
//  1. 删掉 1 号位 ⇒ **2 号位不动**（不能左移）；
//  2. 新建的角色 **补最小的空位**（不是排到最后）；
//  3. 槽位**永远在 0..MaxChrSlots-1 之内** —— 曾经按"行号（含已删除）"现算，
//     已删除的行会一直占位，把活着的角色顶出可显示范围 ⇒ 选角界面一个人都不画。
func TestCharacterSlotsStayPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "charslot.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	cs := st.Characters()
	ctx := context.Background()

	mk := func(name string) *storage.Character {
		c := &storage.Character{
			Account: "u1",
			Name:    name,
			Job:     0,
			Level:   1,
		}
		if err := cs.Create(ctx, c); err != nil {
			t.Fatalf("建号 %s: %v", name, err)
		}
		return c
	}

	a := mk("甲")
	b := mk("乙")
	slotOf := func(id int64) int {
		slots, err := cs.ListByAccountWithSlots(ctx, "u1")
		if err != nil {
			t.Fatalf("列角色: %v", err)
		}
		for _, s := range slots {
			if s.Char.ID == id {
				return s.Slot
			}
		}
		t.Fatalf("id=%d 不在列表里", id)
		return -1
	}
	if got, want := slotOf(a.ID), 0; got != want {
		t.Fatalf("甲该在 0 号位，实得 %d", got)
	}
	if got, want := slotOf(b.ID), 1; got != want {
		t.Fatalf("乙该在 1 号位，实得 %d", got)
	}

	// ① 删掉 0 号位 ⇒ 1 号位**不动**
	if err := cs.MarkDeleted(ctx, a.ID); err != nil {
		t.Fatalf("删号: %v", err)
	}
	if got, want := slotOf(b.ID), 1; got != want {
		t.Errorf("删掉 0 号位后，乙该还在 1 号位，实得 %d", got)
	}
	// 列表里只剩乙（空的 0 号位不返回）
	slots, err := cs.ListByAccountWithSlots(ctx, "u1")
	if err != nil {
		t.Fatalf("列角色: %v", err)
	}
	if len(slots) != 1 || slots[0].Slot != 1 {
		t.Errorf("删号后该只剩[槽位1=乙]，实得 %+v", slots)
	}

	// ② 新建的角色**补最小的空位**（0 号位），乙仍然不动
	c := mk("丙")
	if got, want := slotOf(c.ID), 0; got != want {
		t.Errorf("新角色该补 0 号位，实得 %d", got)
	}
	if got, want := slotOf(b.ID), 1; got != want {
		t.Errorf("乙该一直在 1 号位，实得 %d", got)
	}

	// ③ 槽位必须在可显示范围内（顶出去就画不出人了）
	for _, s := range []*storage.Character{b, c} {
		if s.Slot < 0 || s.Slot >= storage.MaxChrSlots {
			t.Errorf("%s 的槽位 %d 超出 0..%d", s.Name, s.Slot, storage.MaxChrSlots-1)
		}
	}
}

package entity

import (
	"sort"
	"testing"
)

func setOf(ids ...uint32) map[uint32]struct{} {
	m := make(map[uint32]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

func sorted(v []uint32) []uint32 {
	out := append([]uint32(nil), v...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestViewTrackerEnter 首次看到 → 全部算进入。
func TestViewTrackerEnter(t *testing.T) {
	v := NewViewTracker()
	entered, left := v.Update(setOf(1, 2, 3))
	if !equal(sorted(entered), []uint32{1, 2, 3}) {
		t.Errorf("entered = %v, want [1 2 3]", sorted(entered))
	}
	if len(left) != 0 {
		t.Errorf("left = %v, want 空", left)
	}
}

// TestViewTrackerStable 视野不变 → 无事件（增量同步的核心）。
func TestViewTrackerStable(t *testing.T) {
	v := NewViewTracker()
	v.Update(setOf(1, 2))
	entered, left := v.Update(setOf(1, 2))
	if len(entered) != 0 || len(left) != 0 {
		t.Errorf("无变化时应无事件: entered=%v left=%v", entered, left)
	}
}

// TestViewTrackerMixed 进出同时发生。
func TestViewTrackerMixed(t *testing.T) {
	v := NewViewTracker()
	v.Update(setOf(1, 2, 3))
	entered, left := v.Update(setOf(3, 4))
	if !equal(sorted(entered), []uint32{4}) {
		t.Errorf("entered = %v, want [4]", sorted(entered))
	}
	if !equal(sorted(left), []uint32{1, 2}) {
		t.Errorf("left = %v, want [1 2]", sorted(left))
	}
	if !v.Contains(3) || !v.Contains(4) {
		t.Error("当前视野应包含 3 和 4")
	}
	if v.Contains(1) {
		t.Error("1 应已离开视野")
	}
}

// TestViewTrackerLeaveAll 全部离开。
func TestViewTrackerLeaveAll(t *testing.T) {
	v := NewViewTracker()
	v.Update(setOf(1, 2))
	entered, left := v.Update(setOf())
	if len(entered) != 0 {
		t.Errorf("entered = %v", entered)
	}
	if !equal(sorted(left), []uint32{1, 2}) {
		t.Errorf("left = %v, want [1 2]", sorted(left))
	}
	if len(v.Seen()) != 0 {
		t.Error("视野应为空")
	}
}

// TestViewTrackerNilUpdate 传 nil 不应 panic，且视为全部离开。
func TestViewTrackerNilUpdate(t *testing.T) {
	v := NewViewTracker()
	v.Update(setOf(7))
	_, left := v.Update(nil)
	if !equal(sorted(left), []uint32{7}) {
		t.Errorf("left = %v, want [7]", sorted(left))
	}
	if v.Seen() == nil {
		t.Error("内部集合不应为 nil（避免后续写入 panic）")
	}
}

// TestViewTrackerClear 下线场景：清空并返回原可见者。
func TestViewTrackerClear(t *testing.T) {
	v := NewViewTracker()
	v.Update(setOf(5, 6))
	got := sorted(v.Clear())
	if !equal(got, []uint32{5, 6}) {
		t.Errorf("Clear = %v, want [5 6]", got)
	}
	if len(v.Seen()) != 0 {
		t.Error("Clear 后应为空")
	}
	if again := v.Clear(); len(again) != 0 {
		t.Error("重复 Clear 应返回空")
	}
}

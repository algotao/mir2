package world

import "testing"

type testObj struct {
	x, y int
	id   int
}

func (t *testObj) Pos() (int, int) { return t.x, t.y }

func TestSpatialIndexAddRemove(t *testing.T) {
	idx := NewSpatialIndex(32)
	o := &testObj{x: 5, y: 5, id: 1}

	idx.Add(o)
	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1", idx.Len())
	}
	if got := idx.InRange(5, 5, 3); len(got) != 1 {
		t.Errorf("InRange = %d, want 1", len(got))
	}
	// 远处不应命中（跨 chunk）
	if got := idx.InRange(500, 500, 3); len(got) != 0 {
		t.Errorf("远处 InRange = %d, want 0", len(got))
	}

	idx.Remove(o)
	if idx.Len() != 0 {
		t.Errorf("Remove 后 Len = %d", idx.Len())
	}
	if len(idx.chunks) != 0 {
		t.Errorf("空 chunk 应被清理, 剩余 %d", len(idx.chunks))
	}
}

// TestSpatialIndexUpdate 校验对象移动后能正确换 chunk。
//
// ⚠️ 这里守住的是 P1-8 那个 bug：Update 的旧签名是 `Update(o, oldX, oldY)`，
// 而移动方的调用点全都写成"移动后再取坐标"，新旧块恒等 ⇒ 提前返回 ⇒
// 怪物跨块后索引永远停在旧块。现在旧块由索引自己记（`at`），调用方只报"它动了"。
func TestSpatialIndexUpdate(t *testing.T) {
	idx := NewSpatialIndex(32)
	o := &testObj{x: 5, y: 5, id: 1}
	idx.Add(o)

	// 同 chunk 内移动：登记块不变
	o.x, o.y = 20, 20
	idx.Update(o)
	if idx.Len() != 1 {
		t.Fatalf("同块移动后 Len = %d", idx.Len())
	}

	// 跨 chunk 移动：旧的要清掉，新的能查到
	o.x, o.y = 200, 200
	idx.Update(o)
	if idx.Len() != 1 {
		t.Fatalf("跨块移动后 Len = %d, want 1", idx.Len())
	}
	if got := idx.InRange(200, 200, 3); len(got) != 1 {
		t.Errorf("新位置查不到: %d", len(got))
	}
	if got := idx.InRange(20, 20, 3); len(got) != 0 {
		t.Errorf("旧位置仍有残留: %d", len(got))
	}

	// 跨块后 Remove：必须按**登记块**摘除（按当前坐标算块会摘不掉 ⇒ 残留）
	o.x, o.y = 900, 900
	idx.Remove(o) // 故意不先 Update
	if idx.Len() != 0 || len(idx.chunks) != 0 {
		t.Fatalf("移动后未 Update 就 Remove：Len=%d chunks=%d，期望 0/0",
			idx.Len(), len(idx.chunks))
	}
	if got := idx.InRange(200, 200, 3); len(got) != 0 {
		t.Errorf("Remove 后旧块仍有残留: %d", len(got))
	}
}

// TestSpatialIndexReAdd 守住"同一对象重复登记不留双份"：
// 登录/切图路径是 `Remove + Add`，但任何漏掉 Remove 的路径都不该让对象
// 同时出现在两个块里（否则视野查询会重复命中、Remove 只摘一份）。
func TestSpatialIndexReAdd(t *testing.T) {
	idx := NewSpatialIndex(32)
	o := &testObj{x: 5, y: 5, id: 1}
	idx.Add(o)
	o.x, o.y = 100, 100
	idx.Add(o) // 没 Remove 就再 Add

	if idx.Len() != 1 {
		t.Fatalf("重复 Add 后 Len = %d, want 1", idx.Len())
	}
	if got := idx.InRange(5, 5, 3); len(got) != 0 {
		t.Errorf("旧块残留: %d", len(got))
	}
	if got := idx.InRange(100, 100, 3); len(got) != 1 {
		t.Errorf("新块查不到: %d", len(got))
	}
	idx.Remove(o)
	if idx.Len() != 0 || len(idx.chunks) != 0 {
		t.Fatalf("Remove 后 Len=%d chunks=%d", idx.Len(), len(idx.chunks))
	}
}

// TestSpatialIndexRange 校验范围查询覆盖多个 chunk。
func TestSpatialIndexRange(t *testing.T) {
	idx := NewSpatialIndex(32)
	// 散布在多个 chunk
	points := [][2]int{{5, 5}, {40, 5}, {5, 40}, {40, 40}, {70, 5}}
	for i, p := range points {
		idx.Add(&testObj{x: p[0], y: p[1], id: i})
	}
	if idx.Len() != 5 {
		t.Fatalf("Len = %d, want 5", idx.Len())
	}

	// 中心 (35,35) 半径 40 应覆盖前 4 个（第 5 个在 x=70，距离 35 <= 40 也应覆盖）
	got := idx.InRange(35, 35, 40)
	if len(got) != 5 {
		t.Errorf("InRange(35,35,40) = %d, want 5", len(got))
	}
	// 小半径只覆盖附近
	got = idx.InRange(5, 5, 3)
	if len(got) != 1 {
		t.Errorf("InRange(5,5,3) = %d, want 1", len(got))
	}
}

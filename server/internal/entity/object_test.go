package entity

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/world"
)

// TestDirDelta 校验八方向的坐标增量。
//
// y 轴向下增大，故 DirUp 的 dy = -1（与屏幕坐标系一致）。
func TestDirDelta(t *testing.T) {
	cases := []struct {
		dir    uint8
		dx, dy int
	}{
		{DirUp, 0, -1},
		{DirUpRight, 1, -1},
		{DirRight, 1, 0},
		{DirDownRight, 1, 1},
		{DirDown, 0, 1},
		{DirDownLeft, -1, 1},
		{DirLeft, -1, 0},
		{DirUpLeft, -1, -1},
	}
	for _, c := range cases {
		if got := DirDelta[c.dir]; got[0] != c.dx || got[1] != c.dy {
			t.Errorf("DirDelta[%d] = %v, want (%d,%d)", c.dir, got, c.dx, c.dy)
		}
	}
}

func TestObjectMove(t *testing.T) {
	// border=true：四周一圈阻挡，内部 8x8 可走
	m := world.Generate("t", 10, 10, true)
	o := &Object{ID: 1, mapRef: m, posX: 5, posY: 5}

	if !o.MoveTo(DirRight) {
		t.Fatal("应能向右移动")
	}
	if o.PosX() != 6 || o.PosY() != 5 || o.Facing() != DirRight {
		t.Errorf("移动后 = (%d,%d) dir=%d", o.PosX(), o.PosY(), o.Facing())
	}

	// 走到边界：x=8 再向右是 x=9（阻挡）
	o.SetPos(o.MapRef(), 8, 5)
	if o.MoveTo(DirRight) {
		t.Error("不应能走出边界")
	}
	if o.PosX() != 8 {
		t.Errorf("失败后坐标不应改变: %d", o.PosX())
	}
}

func TestObjectTurn(t *testing.T) {
	o := &Object{ID: 1, facing: DirUp}
	if !o.Turn(DirDown) || o.Facing() != DirDown {
		t.Error("转向失败")
	}
	if o.Turn(9) {
		t.Error("非法方向应返回 false")
	}
}

// TestCanMoveIllegalDir 校验非法方向不会移动。
func TestCanMoveIllegalDir(t *testing.T) {
	m := world.Generate("t", 10, 10, true)
	o := &Object{ID: 1, mapRef: m, posX: 5, posY: 5}

	if _, _, ok := o.CanMove(9); ok {
		t.Error("非法方向不应可移动")
	}
	if o.MoveTo(200) { // uint8 下 200 仍越界
		t.Error("越界方向不应移动")
	}
	if o.PosX() != 5 || o.PosY() != 5 {
		t.Errorf("坐标不应改变: (%d,%d)", o.PosX(), o.PosY())
	}
}

// TestDistance 校验切比雪夫距离（传奇用正方形视野，不是欧氏距离）。
func TestDistance(t *testing.T) {
	o := &Object{posX: 10, posY: 10}
	cases := []struct {
		x, y int
		want int
	}{
		{10, 10, 0},
		{13, 10, 3}, // 纯 x
		{10, 15, 5}, // 纯 y
		{13, 14, 4}, // 对角：max(3,4)
		{20, 10, 10},
	}
	for _, c := range cases {
		if got := o.Distance(c.x, c.y); got != c.want {
			t.Errorf("Distance(%d,%d) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

// TestMoveLimiter 校验基于时间戳的移动限流。
func TestMoveLimiter(t *testing.T) {
	l := NewMoveLimiter()
	now := time.Now()

	if !l.Allow(false, now) {
		t.Fatal("首次走应允许")
	}
	// 立刻再走：间隔 0 < 600ms
	if l.Allow(false, now.Add(100*time.Millisecond)) {
		t.Error("间隔过短不应允许走")
	}
	// ⚠️ 走跑**共用**同一个时间戳（原版 `m_dwMoveTick`，`ObjBase.pas:9521/9604`）：
	// 走一步之后紧接着跑也要等满间隔 —— 否则"走→跑→走"交替等于变速齿轮。
	if l.Allow(true, now.Add(100*time.Millisecond)) {
		t.Error("刚走过就不该允许跑（走跑共用 m_dwMoveTick）")
	}
	// 跑同样要等满 600ms（原版 `dwRunIntervalTime` 也是 600）
	if !l.Allow(true, now.Add(700*time.Millisecond)) {
		t.Error("间隔足够应允许跑")
	}
	// 走够 600ms 后可以再走
	if !l.Allow(false, now.Add(1400*time.Millisecond)) {
		t.Error("间隔足够应允许走")
	}

	l.Reset()
	if !l.Allow(false, now.Add(701*time.Millisecond)) {
		t.Error("Reset 后应允许")
	}
}

package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
)

// TestDirFromToNeighbourhood 穷举八邻域。
//
// ⚠️ 这是"往哪个方向推"的**唯一**决定点，错一位就会把怪推进墙里/推反方向。
// 顺序必须与 entity.DirDelta 一致（0 上、1 右上、…、7 左上），
// 也就是原版 DR_UP/DR_UPRIGHT/…/DR_UPLEFT（M2Share.pas:3504-3511）。
func TestDirFromToNeighbourhood(t *testing.T) {
	// 自己站在 (10,10)，逐个检查八邻域。
	want := map[[2]int]int{
		{10, 9}:  entity.DirUp,        // 上：y 减小
		{11, 9}:  entity.DirUpRight,   // 右上
		{11, 10}: entity.DirRight,     // 右
		{11, 11}: entity.DirDownRight, // 右下
		{10, 11}: entity.DirDown,      // 下
		{9, 11}:  entity.DirDownLeft,  // 左下
		{9, 10}:  entity.DirLeft,      // 左
		{9, 9}:   entity.DirUpLeft,    // 左上
	}
	for pos, dir := range want {
		if got := dirFromTo(10, 10, pos[0], pos[1]); got != dir {
			t.Errorf("dirFromTo(10,10,%d,%d) = %d，期望 %d", pos[0], pos[1], got, dir)
		}
	}
	// 同格：原版 Result 初值是 DR_DOWN（4）。
	if got := dirFromTo(10, 10, 10, 10); got != entity.DirDown {
		t.Errorf("同格 = %d，期望 %d（原版初值 DR_DOWN）", got, entity.DirDown)
	}
}

// TestDirFromToAlignment 守住两处"轴对齐吸附"（M2Share.pas:3496-3502）。
//
//	if abs(sY - dy) > 2 then if (sX >= dx - 1) and (sX <= dx + 1) then flagx := 0;
//	if abs(sX - dx) > 2 then if (sY >  dy - 1) and (sY <= dy + 1) then flagy := 0;
//
// ⚠️ **两条的比较符不对称**：上一条是 `>=`，下一条是 `>`（严格大于）。
// 照抄时很容易写成一样的，那会让"正好差一格"的情形偏向斜向。
func TestDirFromToAlignment(t *testing.T) {
	// Δy=5（>2）、Δx=1（在 ±1 内）⇒ flagX 吸成 0 ⇒ 正下方。
	//（没有这条吸附会得到 DR_DOWNRIGHT）
	if got := dirFromTo(10, 10, 11, 15); got != entity.DirDown {
		t.Errorf("(10,10)→(11,15) = %d，期望 %d（Δy>2 且 Δx=1 ⇒ 吸成垂直）", got, entity.DirDown)
	}
	// 真正触发第二条吸附的样本：sY=12、dy=11 ⇒ `12 > 11-1` 成立 ⇒ flagY 吸成 0。
	//（没有它会得到 DR_UPRIGHT）
	if got := dirFromTo(11, 12, 15, 11); got != entity.DirRight {
		t.Errorf("(11,12)→(15,11) = %d，期望 %d（Δx>2 且 sY>dy-1 ⇒ 吸成水平）", got, entity.DirRight)
	}
	// ⚠️ 不对称 quirk 的回归守卫：sY=10、dy=11 ⇒ `10 > 10` **不成立** ⇒ 不吸附。
	// 若把 `>` 写成 `>=`，这里会变成 DR_RIGHT(2)。
	if got := dirFromTo(10, 10, 15, 11); got != entity.DirDownRight {
		t.Errorf("(10,10)→(15,11) = %d，期望 %d（sY=dy-1 ⇒ 原版 `>` 不成立，不吸附）",
			got, entity.DirDownRight)
	}
	// 两轴都远 ⇒ 都不吸附 ⇒ 斜向。
	if got := dirFromTo(10, 10, 15, 15); got != entity.DirDownRight {
		t.Errorf("(10,10)→(15,15) = %d，期望 %d", got, entity.DirDownRight)
	}
}

// TestInvisibleSeconds 隐身时长 = getPower13(30) + GetRPow(SC)*3。
//
// 技能 18/19 的 power/defPower 全是 0，所以 getPower13(30) 只由技能等级决定：
// d10=10、d18=20 ⇒ round(20/4*(Lv+1) + 10) + 0 = **5*(Lv+1)+10**。
// SC 传 0 ⇒ GetRPow 不摇随机（高低相等），结果完全确定。
//
// ⚠️ 别把它改成"固定 60 秒"：隐身 0 级 = 15 秒、3 级 = 30 秒，
// 固定值会让高技能等级白练（也与我们原来的硬编码 60 秒不同）。
func TestInvisibleSeconds(t *testing.T) {
	info := &data.MagicInfo{Name: "隐身术"}
	for _, c := range []struct {
		level uint32
		want  int
	}{{0, 15}, {1, 20}, {2, 25}, {3, 30}} {
		if got := invisibleSeconds(info, c.level, 0); got != c.want {
			t.Errorf("invisibleSeconds(level=%d, SC=0) = %d，期望 %d", c.level, got, c.want)
		}
	}
	// SC 非 0 时加上 GetRPow(SC)*3（SC 是打包的 [Lo,Hi]，取 Lo..Hi）。
	sc := uint32(0x000A0005) // Lo=5, Hi=10
	for i := 0; i < 20; i++ {
		got := invisibleSeconds(info, 0, sc)
		if got < 15+5*3 || got > 15+10*3 {
			t.Fatalf("invisibleSeconds(0, SC=5..10) = %d，应在 [30, 45]", got)
		}
	}
}

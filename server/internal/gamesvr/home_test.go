package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/chargen"
)

// 新角色的出生点：**两个新手村随机二选一**（原版 1.76 口径，见 `docs/use.md`）。
//
// ⚠️ 这条测试挡的是两次弯路（都在 D-38 → D-40 里记着）：
//
//  1. "取安全点表第一条" ⇒ 新号**永远**落在边界村 (289,618)；
//
//  2. "按配置挑离 Home 最近的一条" ⇒ 新号**永远**落在银杏谷 (650,631)。
//
//     原版其实是**随机**，两个村都得能出现。
func TestNewCharHomeIsRandomVillage(t *testing.T) {
	s, _ := butchTestServer(t)
	s.data.defaultMapID = "0"
	// 与 `-home-points` 的默认值一致（`run.go` 的 mustHomePoints 解析出来的形状）
	s.data.homePoints = []chargen.Home{
		{Map: "0", X: 650, Y: 631}, // 银杏山谷
		{Map: "0", X: 289, Y: 618}, // 边界村
	}

	seen := map[uint32]int{}
	for i := 0; i < 200; i++ {
		h := s.newCharHome()
		if h.Map != "0" {
			t.Fatalf("出生地图该是 0，实得 %q", h.Map)
		}
		in650 := h.X == 650 && h.Y == 631
		in289 := h.X == 289 && h.Y == 618
		if !in650 && !in289 {
			t.Fatalf("出生点落在候选之外：%+v", h)
		}
		seen[h.X]++
	}
	if seen[650] == 0 || seen[289] == 0 {
		t.Errorf("200 次里两个新手村都该出现过（真随机），实得 %v", seen)
	}

	// 只留一个候选 ⇒ **确定**（把出生点钉死用于调试/复现）
	s.data.homePoints = []chargen.Home{{Map: "0", X: 650, Y: 631}}
	for i := 0; i < 5; i++ {
		if h := s.newCharHome(); h.X != 650 || h.Y != 631 {
			t.Fatalf("单候选时必须确定，实得 %+v", h)
		}
	}

	// 候选为空 ⇒ 只给地图号（`chargen` 那边对 (0,0) 有自己的兜底）
	s.data.homePoints = nil
	if h := s.newCharHome(); h.Map != "0" || h.X != 0 || h.Y != 0 {
		t.Errorf("没有候选时该原样返回地图号，实得 %+v", h)
	}
}

// ⚠️ `-home-points` 的**解析**测试在 `chargen`（`TestParseHomePoints`）—— 那份解析
// 是两条建角路共用的；这里只管"解析出来的候选怎么用"（上面那条）。

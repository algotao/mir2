package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
)

// 新角色的出生点取的是**离配置 Home 最近的安全点**（原版 `GetHomePoint` 的等价物，
// `ObjBase.pas:9885-9919`），而不是"表里第一条"。
//
// ⚠️ 这条正是 2026-10-08 用户报的那件事：默认 Home 指到银杏谷那片 (650,631)，
// 但早先的实现取表首条 ⇒ 新号被扔在 (289,618)（比奇省城里），不是他要的新手村。
func TestNewCharHomePicksNearestSafePoint(t *testing.T) {
	s, _ := butchTestServer(t)
	// 测试环境没装 StartPoint 表 ⇒ 手动装**文件里那两条**（`server/data/envir/StartPoint.txt`）
	s.data.startPoints = []*data.StartPoint{
		{MapID: "0", X: 289, Y: 618},
		{MapID: "0", X: 650, Y: 631},
		{MapID: "2", X: 503, Y: 483},
	}
	s.data.defaultMapID = "0"
	s.data.homeX, s.data.homeY = 650, 631 // `-home-x/-home-y` 的默认值

	home := s.newCharHome()
	if home.Map != "0" || home.X != 650 || home.Y != 631 {
		t.Fatalf("默认 Home 下新号该落在 0(650,631)=银杏谷，实得 %s(%d,%d)",
			home.Map, home.X, home.Y)
	}

	// 换成靠老点的 Home ⇒ 应当挑回 (289,618)：证明是"按配置挑"，不是写死某个点
	s.data.homeX, s.data.homeY = 300, 600
	if home = s.newCharHome(); home.X != 289 || home.Y != 618 {
		t.Fatalf("Home 指到 (300,600) 时该挑 (289,618)，实得 (%d,%d)", home.X, home.Y)
	}

	// 该图一条安全点都没有 ⇒ 保留默认地图、坐标交给调用方兜底（不 panic、不越界）
	s.data.startPoints = []*data.StartPoint{{MapID: "2", X: 503, Y: 483}}
	if home = s.newCharHome(); home.Map != "0" || home.X != 0 || home.Y != 0 {
		t.Fatalf("该图没有安全点时该原样返回，实得 %s(%d,%d)", home.Map, home.X, home.Y)
	}
}

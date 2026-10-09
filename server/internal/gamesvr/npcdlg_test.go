package gamesvr

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/script"
)

func TestCommunityElseActScriptParsedSeparately(t *testing.T) {
	// ⚠️ 原先用的是 `Hsender10-H010.txt`（幻境 H010 上的脚本）—— 幻境不是 1.76 的图，
	// 配置按 1.76 裁过之后那个文件不在了（见 docs/decisions.md D-57）。改用同在
	// 比奇省（地图 0）的 `9BIsender-0.txt`：`[@jump]` 里同样是 `#act`/`#elseact`
	// 各带一次随机传送，正好守住"两个分支分开解析"这件事。
	path := filepath.Join("..", "..", "data", "envir", "market_def", "9BIsender-0.txt")
	sc, err := script.ParseFile(path)
	if err != nil {
		t.Fatalf("解析社区脚本 %s: %v", path, err)
	}
	l := sc.Label("jump")
	if l == nil || len(l.Acts) != 2 || l.Acts[0] != "timerecall 60" || l.Acts[1] != "map B101" ||
		len(l.ElseActs) != 2 || l.ElseActs[0] != "timerecall 60" || l.ElseActs[1] != "map B102" {
		t.Fatalf("社区脚本 [@jump] 的随机传送分支解析错误: %+v", l)
	}
}

func TestShowLabelElseActs(t *testing.T) {
	cases := []struct {
		name string
		roll int
		want int64
	}{
		{name: "condition true", roll: 0, want: 1},
		{name: "condition false", roll: 1, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
			sc, err := script.Parse("branch", strings.NewReader(`[@main]
#if
RANDOM 2
#act
givegold 1
#elseact
givegold 2
`))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			oldRandomSource := script.RandomSource
			script.RandomSource = func(int) int { return tc.roll }
			defer func() { script.RandomSource = oldRandomSource }()

			s.showLabel(nil, p, sc, sc.Label("main"))
			if got := p.gold(); got != tc.want {
				t.Fatalf("金币 = %d，期望只执行对应分支后为 %d", got, tc.want)
			}
		})
	}
}

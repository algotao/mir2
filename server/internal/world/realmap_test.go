package world

import (
	"os"
	"path/filepath"
	"testing"
)

// realMapDir 是 1.76 官方地图数据目录。
//
// 数据已**随仓库分发**（data/map，来自官方 1.76 数据包），所以这里用
// 包目录相对的仓库内路径，不再依赖任何外部 checkout。
var realMapDir = filepath.Join("..", "..", "data", "map")

// TestParseRealMapFiles 用真实 .map 文件校验解析器。
//
// 验证点：文件头字段（尺寸、标题）与格数据能被正确读出，
// 且可通行比例落在合理区间——若列主序/行主序搞反或 BlockBit 取错，
// 可走比例会异常（接近 0% 或 100%）。
func TestParseRealMapFiles(t *testing.T) {
	if _, err := os.Stat(realMapDir); err != nil {
		t.Skipf("跳过：真实地图数据不可用 (%v)", err)
	}

	cases := []struct {
		file       string
		wantW      int
		wantH      int
		wantTitle  string
		minWalkPct int // 可通行格的最小百分比
		maxWalkPct int
	}{
		{"0.map", 700, 700, "Legend of mir", 20, 95},
		{"0100.map", 15, 18, "Legend of mir", 10, 95},
	}
	for _, c := range cases {
		path := filepath.Join(realMapDir, c.file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", c.file, err)
		}
		m, err := Parse(c.file, data)
		if err != nil {
			t.Fatalf("解析 %s: %v", c.file, err)
		}
		if m.Width() != c.wantW || m.Height() != c.wantH {
			t.Errorf("%s 尺寸 = %dx%d, want %dx%d",
				c.file, m.Width(), m.Height(), c.wantW, c.wantH)
		}
		if m.Header.Title != c.wantTitle {
			t.Errorf("%s 标题 = %q, want %q", c.file, m.Header.Title, c.wantTitle)
		}

		walk, total := 0, m.Width()*m.Height()
		for i := range m.Cells {
			if m.Cells[i].CanWalk() {
				walk++
			}
		}
		pct := walk * 100 / total
		if pct < c.minWalkPct || pct > c.maxWalkPct {
			t.Errorf("%s 可通行 %d%% (%d/%d)，超出 %d~%d%% —— 可能是行列主序或 BlockBit 解析错误",
				c.file, pct, walk, total, c.minWalkPct, c.maxWalkPct)
		}
		t.Logf("%s: %dx%d 标题=%q 可通行 %d%%", c.file, m.Width(), m.Height(), m.Header.Title, pct)
	}
}

// TestParseAllRealMaps 遍历整个目录，确保没有解析失败的文件。
//
// 605 张图里可能有不同版本/异常文件，这个测试用来兜底。
func TestParseAllRealMaps(t *testing.T) {
	if _, err := os.Stat(realMapDir); err != nil {
		t.Skipf("跳过：真实地图数据不可用 (%v)", err)
	}
	entries, err := os.ReadDir(realMapDir)
	if err != nil {
		t.Fatalf("列目录: %v", err)
	}
	ok, bad := 0, 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".map" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(realMapDir, e.Name()))
		if err != nil {
			t.Errorf("读取 %s: %v", e.Name(), err)
			bad++
			continue
		}
		if _, err := Parse(e.Name(), data); err != nil {
			if bad < 10 { // 只报前 10 个，避免刷屏
				t.Errorf("解析 %s 失败: %v", e.Name(), err)
			}
			bad++
			continue
		}
		ok++
	}
	t.Logf("真实地图解析：成功 %d，失败 %d", ok, bad)
	if ok == 0 {
		t.Fatal("没有任何地图解析成功")
	}
}

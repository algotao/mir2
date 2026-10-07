package script

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realDir 是 1.76 官方 NPC 脚本目录。
//
// 数据已随仓库分发（data/envir/market_def），用包目录相对路径。
var realDir = filepath.Join("..", "..", "data", "envir", "market_def")

const sample = `;科目  肉块
(@trading @buy @sell)
%100
+40

[@main]
欢迎光临,有什么事情需要我帮忙吗？\ \
 \
<打开/@trading> 交易市场\
<买/@buy>肉\
<退出/@exit>

[@sell]
高价买入品质好的肉。\
 <继续/@main>

[@h2]
#if
#act
mapmove H001 73 67
`

func TestParseBasic(t *testing.T) {
	s, err := Parse("sample", strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Name != "sample" {
		t.Errorf("Name = %q", s.Name)
	}
	if !strings.Contains(s.CmdLine, "@buy") {
		t.Errorf("CmdLine = %q, 应含 @buy", s.CmdLine)
	}

	main := s.Label("main")
	if main == nil {
		t.Fatal("缺少 [@main] 段")
	}
	if !strings.Contains(main.Say, "欢迎光临") {
		t.Errorf("Say = %q", main.Say)
	}
	// ⚠️ 续行符与尾部反斜杠必须被去掉
	if strings.Contains(main.Say, "\\") {
		t.Errorf("Say 残留续行符: %q", main.Say)
	}
	if len(main.Links) != 3 {
		t.Fatalf("选项数 = %d, want 3: %+v", len(main.Links), main.Links)
	}
	if main.Links[1].Text != "买" || main.Links[1].Label != "buy" {
		t.Errorf("选项[1] = %+v", main.Links[1])
	}

	sell := s.Label("sell")
	if sell == nil || !strings.Contains(sell.Say, "高价买入") {
		t.Errorf("[@sell] 解析异常: %+v", sell)
	}

	h2 := s.Label("h2")
	if h2 == nil {
		t.Fatal("缺少 [@h2] 段")
	}
	if len(h2.Acts) != 1 || h2.Acts[0] != "mapmove H001 73 67" {
		t.Errorf("Acts = %+v, want [mapmove H001 73 67]", h2.Acts)
	}
}

func TestParseElseActsSeparately(t *testing.T) {
	s, err := Parse("branch", strings.NewReader(`[@main]
#if
RANDOM 2
#act
map B101
#elseact
map B102
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	l := s.Label("main")
	if len(l.Conds) != 1 || l.Conds[0] != "RANDOM 2" {
		t.Fatalf("Conds = %+v", l.Conds)
	}
	if len(l.Acts) != 1 || l.Acts[0] != "map B101" {
		t.Fatalf("Acts = %+v", l.Acts)
	}
	if len(l.ElseActs) != 1 || l.ElseActs[0] != "map B102" {
		t.Fatalf("ElseActs = %+v", l.ElseActs)
	}
}

func TestEntry(t *testing.T) {
	s, err := Parse("sample", strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if e := s.Entry(); e == nil || e.Name != "main" {
		t.Fatalf("Entry = %+v, want main", e)
	}

	// 没有 @main 时退回第一个段
	s2, err := Parse("x", strings.NewReader("[@foo]\nhi\n"))
	if err != nil {
		t.Fatal(err)
	}
	if e := s2.Entry(); e.Name != "foo" {
		t.Errorf("Entry = %q, want foo", e.Name)
	}
}

// TestParseRealScripts 用真实的 market_def 全量解析，确认没有解析失败。
func TestParseRealScripts(t *testing.T) {
	if _, err := os.Stat(realDir); err != nil {
		t.Skipf("跳过：真实脚本数据不可用 (%v)", err)
	}
	entries, err := os.ReadDir(realDir)
	if err != nil {
		t.Fatal(err)
	}
	ok, bad, withMain := 0, 0, 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		s, err := ParseFile(filepath.Join(realDir, e.Name()))
		if err != nil {
			bad++
			continue
		}
		ok++
		if _, has := s.Labels["main"]; has {
			withMain++
		}
	}
	t.Logf("真实脚本：成功 %d，失败 %d，含 [@main] %d", ok, bad, withMain)
	if ok == 0 {
		t.Fatal("没有任何脚本解析成功")
	}
	if bad > ok/2 {
		t.Errorf("失败过多：%d/%d", bad, ok+bad)
	}
}

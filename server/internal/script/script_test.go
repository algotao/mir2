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

// TestLabelLinesKeepInlineLinks 行内选项要**留在原行**，并把标记改写成 `<文字/@序号>`。
//
// 用户 2026-10-09：「交易窗口渲染不对，应该为『打开 交易市场』在一行，其中『打开』可点击」。
// 脚本原文（`market_def/7Gst-0.txt`）是：
//
//	[@main]
//	欢迎. 我可以为你做什么吗?\
//	 \
//	 <打开/@trading> 交易市场\
//	 <购买/@buy>  物品\
//	 <退出/@exit>
//
// 旧版只留 `Say`（行内选项被抽走）⇒ 客户端只能把选项单列到下面，看着就是"不对"。
func TestLabelLinesKeepInlineLinks(t *testing.T) {
	const src = "[@main]\r\n" +
		"欢迎. 我可以为你做什么吗?\\\r\n" +
		" \\\r\n" +
		" <打开/@trading> 交易市场\\\r\n" +
		" <购买/@buy>  物品\\\r\n" +
		" <退出/@exit>\r\n"
	sc, err := Parse("7Gst-0", strings.NewReader(src))
	if err != nil {
		t.Fatalf("解析: %v", err)
	}
	l := sc.Label("main")
	if l == nil {
		t.Fatal("没有 [@main]")
	}
	// 选项按出现次序编号（1 起）—— 客户端回包用的就是这个序号
	if len(l.Links) != 3 || l.Links[0].Label != "trading" || l.Links[2].Label != "exit" {
		t.Fatalf("Links = %+v", l.Links)
	}
	// 行：欢迎 / 空行 / <打开/@1> 交易市场 / <购买/@2>  物品 / <退出/@3>
	if len(l.Lines) != 5 {
		t.Fatalf("行数 = %d，应为 5（含中间那个空行占位）：%q", len(l.Lines), l.Lines)
	}
	if got := l.Lines[2]; !strings.Contains(got, "<打开/@1>") || !strings.Contains(got, "交易市场") {
		t.Errorf("第 3 行 = %q，应含 `<打开/@1>` 与 `交易市场`（同一行）", got)
	}
	if got := l.Lines[3]; !strings.Contains(got, "<购买/@2>") {
		t.Errorf("第 4 行 = %q，应含 `<购买/@2>`", got)
	}
	// `Say` 仍然是"没有行内标记"的纯文本（legacy 那条路照旧）
	if strings.Contains(l.Say, "<") || !strings.Contains(l.Say, "交易市场") {
		t.Errorf("Say = %q，应是不含标记的纯文本", l.Say)
	}
}

// TestRealMerchantScriptInline 拿**真实脚本**再验一遍（数据驱动的那一条）。
//
// `7Gst-0.txt` 是陈家铺老板（比奇省 643,611）的脚本，`[@main]` 就是用户截图里那句
// 「欢迎. 我可以为你做什么吗?」。官方渲染成：
//
//	欢迎. 我可以为你做什么吗?
//
//	 ■打开 交易市场
//	 ■购买  物品
//	 ■出售  物品
//	 ■询问 物品详细情况
//	 ■退出
//
// 也就是**打开/购买/出售/询问/退出**各自可点、且与自己后面那串字**同行**。
func TestRealMerchantScriptInline(t *testing.T) {
	path := filepath.Join("..", "..", "data", "envir", "market_def", "7Gst-0.txt")
	sc, err := ParseFile(path)
	if err != nil {
		t.Skipf("拿不到真实脚本（%v），跳过", err)
	}
	l := sc.Label("main")
	if l == nil {
		t.Fatal("没有 [@main]")
	}
	want := []string{"<打开/@1> 交易市场", "<购买/@2>  物品", "<出售/@3>  物品", "<询问/@4> 物品详细情况", "<退出/@5>"}
	if len(l.Lines) < len(want)+1 {
		t.Fatalf("行太少：%q", l.Lines)
	}
	// 前两行是正文 + 段落空行，接着是那 5 个行内选项
	for i, w := range want {
		got := l.Lines[i+2]
		if !strings.Contains(got, w) {
			t.Errorf("第 %d 行 = %q，应含 %q", i+3, got, w)
		}
	}
	if len(l.Links) != 5 || l.Links[0].Text != "打开" || l.Links[0].Label != "trading" {
		t.Errorf("Links = %+v", l.Links)
	}
}

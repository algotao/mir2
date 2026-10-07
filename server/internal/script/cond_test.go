package script

import (
	"testing"
)

// fakeCtx 是测试用的 Context 实现。
type fakeCtx struct {
	gold   int64
	level  int32
	job    int32
	mapStr string
	items  map[string]int
	vars   map[int]int64
	skills map[string]bool
	pk     int32
	guild  string
	// gameTime 是 `DAYTIME` 条件用的相位名（空 ⇒ 默认 DAY）。
	gameTime string
}

func (c *fakeCtx) Gold() int64     { return c.gold }
func (c *fakeCtx) Level() int32    { return c.level }
func (c *fakeCtx) Job() int32      { return c.job }
func (c *fakeCtx) MapName() string { return c.mapStr }
func (c *fakeCtx) GameTimeName() string {
	if c.gameTime == "" {
		return "DAY"
	}
	return c.gameTime
}
func (c *fakeCtx) PKPoint() int32 { return c.pk }
func (c *fakeCtx) CountItem(n string) int {
	if c.items == nil {
		return 0
	}
	return c.items[n]
}
func (c *fakeCtx) Var(n int) int64 {
	if c.vars == nil {
		return 0
	}
	return c.vars[n]
}
func (c *fakeCtx) SetVar(n int, v int64) {
	if c.vars == nil {
		c.vars = map[int]int64{}
	}
	c.vars[n] = v
}
func (c *fakeCtx) GuildName() string {
	if c.items == nil {
		return ""
	}
	return c.guild
}

func (c *fakeCtx) CheckSkill(n string) bool {
	if c.skills == nil {
		return false
	}
	return c.skills[n]
}

func newCtx() *fakeCtx {
	return &fakeCtx{
		gold: 100000, level: 30, job: 1, mapStr: "0", pk: 0,
		items:  map[string]int{"强效太阳水": 6, "金创药": 20},
		vars:   map[int]int64{0: 10, 1: 300},
		skills: map[string]bool{"火球术": true},
	}
}

func TestEvalCheckGold(t *testing.T) {
	ctx := newCtx()
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"checkgold 20000", true, "100000 >= 20000（省略 op 默认 >=）"},
		{"CHECKGOLD > 200000", false, "100000 不够"},
		{"CHECKGOLD < 200000", true, "100000 < 200000"},
		{"CHECKGOLD = 100000", true, "正好相等"},
		{"CHECKGOLD >= 100000", true, "边界"},
		{"CHECKGOLD <> 100000", false, "不等于"},
		{"checkgold n1", true, "P 变量 n1=300"},
		{"CHECKGOLD > n1", true, "100000 > 300"},
	}
	for _, c := range cases {
		if got := EvalCond(c.cond, ctx); got != c.want {
			t.Errorf("%q = %v, 期望 %v（%s）", c.cond, got, c.want, c.why)
		}
	}
}

func TestEvalCheckItem(t *testing.T) {
	ctx := newCtx()
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"checkitem 金创药", true, "20 >= 1（数量缺省 1）"},
		{"CHECKITEM 金创药 10", true, "20 >= 10"},
		{"CHECKITEM 金创药 30", false, "20 < 30"},
		{"CHECKITEM 不存在的东西", false, "没有"},
		{"checkitem 强效太阳水 6", true, "正好 6"},
	}
	for _, c := range cases {
		if got := EvalCond(c.cond, ctx); got != c.want {
			t.Errorf("%q = %v, 期望 %v", c.cond, got, c.want)
		}
	}
}

func TestEvalCheckLevelJobMap(t *testing.T) {
	ctx := newCtx()
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"checklevel 30", true, "正好 30"},
		{"CHECKLEVEL > 29", true, "30 > 29"},
		{"CHECKLEVEL < 30", false, "30 不小于 30"},
		{"CHECKJOB 1", true, "法师"},
		{"CHECKJOB 0", false, "不是战士"},
		{"checkmap 0", true, "在地图 0"},
		{"CHECKMAP 3", false, "不在地图 3"},
		{"CHECKPKPOINT 0", true, "白名"},
		{"CHECKSKILL 火球术", true, "会火球术"},
		{"CHECKSKILL 雷电术", false, "不会"},
	}
	for _, c := range cases {
		if got := EvalCond(c.cond, ctx); got != c.want {
			t.Errorf("%q = %v, 期望 %v", c.cond, got, c.want)
		}
	}
}

func TestEvalVarComparisons(t *testing.T) {
	ctx := newCtx() // n1=10, n2=300
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"check n1 = 10", true, "P 变量相等"},
		{"CHECK n1 > 20", false, "n1=10 不大于 20"},
		{"equal n1 10", true, "EQUAL 糖"},
		{"large n2 300", true, "LARGE 糖"},
		{"small n1 10", true, "SMALL 糖"},
		{"small n1 5", false, "10 不小于等于 5"},
	}
	for _, c := range cases {
		if got := EvalCond(c.cond, ctx); got != c.want {
			t.Errorf("%q = %v, 期望 %v", c.cond, got, c.want)
		}
	}
}

// TestEvalArith 守住 QFunction-0.txt 依赖的算式。
func TestEvalArith(t *testing.T) {
	ctx := newCtx() // n1=10
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"checkgold n1+100", true, "n1+100=110 <= 100000"},
		{"check n1 = 5+5", true, "算式求值"},
		{"check n1 = <$STR(n1)>", true, "<$STR()> 插值"},
		{"check n1*2 = 20", true, "乘法"},
		{"check n1/2 = 5", true, "整除"},
	}
	for _, c := range cases {
		if got := EvalCond(c.cond, ctx); got != c.want {
			t.Errorf("%q = %v, 期望 %v（%s）", c.cond, got, c.want, c.why)
		}
	}
}

func TestEvalRandom(t *testing.T) {
	ctx := newCtx()
	// RandomSource 可注入 → 确定性
	old := RandomSource
	defer func() { RandomSource = old }()

	RandomSource = func(int) int { return 0 }
	if !EvalCond("RANDOM 10", ctx) {
		t.Error("RandomSource 返回 0 时应命中")
	}
	RandomSource = func(n int) int { return n - 1 }
	if EvalCond("RANDOM 10", ctx) {
		t.Error("RandomSource 未命中时应为 false")
	}
	// n<=1 恒真（原版 1..n，n=1 必中）
	RandomSource = func(n int) int { return n - 1 }
	if !EvalCond("RANDOM 1", ctx) {
		t.Error("RANDOM 1 应恒真")
	}
}

func TestEvalCondsAnd(t *testing.T) {
	ctx := newCtx() // 金币 100000，等级 30，法师
	// 多条件是 AND
	if !EvalConds([]string{"CHECKGOLD > 20000", "CHECKLEVEL > 10", "CHECKJOB 1"}, ctx) {
		t.Error("三个条件都应满足")
	}
	if EvalConds([]string{"CHECKGOLD > 20000", "CHECKJOB 0"}, ctx) {
		t.Error("第二个不满足，整体应为 false")
	}
	if !EvalConds(nil, ctx) {
		t.Error("空条件列表应为 true")
	}
}

func TestEvalUnknownCondIsFalse(t *testing.T) {
	ctx := newCtx()
	// 未识别的条件必须为 false（与原版一致），不能静默通过。
	if EvalCond("SOMEUNKNOWNCOND 1", ctx) {
		t.Error("未识别条件应为 false")
	}
	if EvalCond("HEROLEVEL > 10", ctx) {
		t.Error("英雄相关条件未实现，应为 false")
	}
}

func TestSplitArgsKeepsQuotedSpaces(t *testing.T) {
	// 引号内的空格保留、引号外的仍切分 → 3 段
	got := splitArgs(`checknamelist "D:\Mir2\Envir\名单 文件.txt" extra`)
	if len(got) != 3 {
		t.Fatalf("切分结果 = %q, 期望 3 段", got)
	}
	if got[0] != "checknamelist" {
		t.Errorf("第 1 段 = %q", got[0])
	}
	if got[1] != `D:\Mir2\Envir\名单 文件.txt` {
		t.Errorf("引号内应保留空格且去掉引号，实际 %q", got[1])
	}
	if got[2] != "extra" {
		t.Errorf("引号外应正常切分，实际 %q", got[2])
	}
}

// TestDaytimeCondition `DAYTIME SUNRAISE|DAY|SUNSET|NIGHT`（ObjNpc.pas:7013-7030）：
// 比的是引擎的全局游戏时间相位，且**不区分大小写**（原版 CompareText）。
func TestDaytimeCondition(t *testing.T) {
	ctx := &fakeCtx{gameTime: "SUNSET"}
	cases := []struct {
		script string
		want   bool
	}{
		{"DAYTIME SUNSET", true},
		{"DAYTIME sunset", true}, // 不区分大小写
		{"DAYTIME SUNRAISE", false},
		{"DAYTIME DAY", false},
		{"DAYTIME NIGHT", false},
		{"DAYTIME", false}, // 缺参数 ⇒ 不成立（原版也取不到 sParam1）
	}
	for _, c := range cases {
		if got := EvalCond(c.script, ctx); got != c.want {
			t.Errorf("%q ⇒ %v，期望 %v", c.script, got, c.want)
		}
	}
}

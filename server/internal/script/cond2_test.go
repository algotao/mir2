package script

import (
	"testing"
	"time"
)

func TestEvalCond2Attributes(t *testing.T) {
	ctx := newCtx()
	v := Vals{
		HP: 800, MP: 300, DC: 120, MC: 90, SC: 75, Exp: 5000,
		BagFree: 20, MapName: "0", MapHumanCount: 3, MapMonCount: 42,
		SlaveCount: 1,
	}
	cases := []struct {
		cond string
		want bool
		why  string
	}{
		{"CHECKHP 800", true, "等于当前 HP"},
		{"CHECKHP > 500", true, "大于"},
		{"CHECKHP < 500", false, "小于不成立"},
		{"CHECKMP 300", true, "MP"},
		{"CHECKDC 120", true, "DC 取 MinMax 的 Min"},
		{"CHECKMC 90", true, "MC"},
		{"CHECKSC 75", true, "SC"},
		{"CHECKEXP > 4000", true, "经验"},
		{"CHECKBAGSIZE > 10", true, "背包剩余 20 > 10"},
		{"CHECKBAGSIZE < 10", false, "剩余 20 不小于 10"},
		{"CHECKLEVELEX > 10", true, "别名，走 CHECKLEVEL"},
		{"CHECKMAPHUMANCOUNT 0 > 1", true, "地图 0 有 3 人"},
		{"CHECKMAPMONCOUNT 0 > 40", true, "地图 0 有 42 怪"},
		{"CHECKMAPHUMANCOUNT 3 > 1", false, "不是地图 3"},
		{"CHECKSLAVECOUNT 1", true, "1 只召唤兽"},
		{"CHECKVAR n1 = 10", true, "别名，走 CHECK"},
		{"CHECKSIGNMAP 0", true, "当前地图 0"},
		{"CHECKMAPNAME 3", false, "不是地图 3"},
	}
	for _, c := range cases {
		if got := EvalCond2(c.cond, ctx, v); got != c.want {
			t.Errorf("%q = %v, 期望 %v（%s）", c.cond, got, c.want, c.why)
		}
	}
}

// fakeCastle 是 CastleInfo 的测试实现。
type fakeCastle struct {
	underWar  bool
	ownGuild  string
	attacker  string
	member    string
	inWarArea bool
}

func (c fakeCastle) InWarArea(mapName string, x, y int) bool { return c.inWarArea }
func (c fakeCastle) UnderWar() bool                          { return c.underWar }
func (c fakeCastle) IsMember(g string) bool                  { return g != "" && g == c.member }
func (c fakeCastle) IsAttackGuild(g string) bool             { return g != "" && g == c.attacker }
func (c fakeCastle) IsDefenseGuild(g string) bool            { return c.underWar && g == c.ownGuild }

func TestEvalCondCastle(t *testing.T) {
	ctx := newCtx() // 无行会
	ci := fakeCastle{underWar: true, ownGuild: "守会", attacker: "攻会", member: "守会"}

	// 无行会时全部为 false
	for _, cond := range []string{"ISCASTLEGUILD", "ISATTACKGUILD", "ISDEFENSEGUILD"} {
		if EvalCondCastle(cond, ctx, ci) {
			t.Errorf("无行会时 %q 应为 false", cond)
		}
	}
	// ISUNDERWAR / INCASTLEWARAREA 不需要行会
	if !EvalCondCastle("ISUNDERWAR", ctx, ci) {
		t.Error("ISUNDERWAR 应为 true（正在攻城）")
	}
	ci.underWar = false
	if EvalCondCastle("ISUNDERWAR", ctx, ci) {
		t.Error("不在攻城期时 ISUNDERWAR 应为 false")
	}

	// 有行会时分三种关系
	ctx.guild = "守会"
	if !EvalCondCastle("ISCASTLEGUILD", ctx, ci) {
		t.Error("占领方成员应满足 ISCASTLEGUILD")
	}
	if EvalCondCastle("ISDEFENSEGUILD", ctx, ci) {
		t.Error("非攻城期 ISDEFENSEGUILD 应为 false（IsDefenseGuild 带 underWar 门控）")
	}
	ci.underWar = true
	if !EvalCondCastle("ISDEFENSEGUILD", ctx, ci) {
		t.Error("攻城期占领方应满足 ISDEFENSEGUILD")
	}

	ctx.guild = "攻会"
	if !EvalCondCastle("ISATTACKGUILD", ctx, ci) {
		t.Error("宣战方应满足 ISATTACKGUILD")
	}
	if EvalCondCastle("ISCASTLEGUILD", ctx, ci) {
		t.Error("宣战方不是占领方")
	}

	// nil 城堡不 panic
	if EvalCondCastle("ISUNDERWAR", ctx, nil) {
		t.Error("nil 城堡应为 false")
	}
	// INCASTLEWARAREA 看位置标记
	ci.inWarArea = true
	if !EvalCondCastle("INCASTLEWARAREA", ctx, ci) {
		t.Error("在战区内应满足 INCASTLEWARAREA")
	}
}

// TestEvalCondCastleUnderWarNotEmpty 守住"ISUNDERWAR 不用行会信息"。
//
// OpenMir2 的 IsUnderWar 是空壳（整体注释），我们按 Delphi 实现。
func TestEvalCondCastleUnderWarNotEmpty(t *testing.T) {
	ctx := newCtx()
	ci := fakeCastle{underWar: true}
	// 刻意不设 inWarArea：ISUNDERWAR 只看战期标志，与位置无关
	if !EvalCondCastle("ISUNDERWAR", ctx, ci) {
		t.Error("ISUNDERWAR 只依赖 UnderWar，不应要求 inWarArea")
	}
	if EvalCondCastle("INCASTLEWARAREA", ctx, ci) {
		t.Error("INCASTLEWARAREA 依赖位置，不在战区应为 false")
	}
}

var _ = time.Second // 保留 time 引用（后续扩展可能用到）

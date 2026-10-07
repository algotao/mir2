package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/entity"
)

// TestCastleLabelIndex 钉住"标签带编号"那种形态的解析（原版 `@hireguardnow<N>`）。
//
// 编号**原样**交给 `handleCastleRepair` —— 它按原版约定当 **1 起**（内部下标减 1）。
func TestCastleLabelIndex(t *testing.T) {
	cases := []struct {
		label  string
		prefix string
		want   string
		ok     bool
	}{
		{"@hireguardnow1", "@hireguardnow", "1", true},
		{"@hireguardnow12", "@hireguardnow", "12", true},
		{"@hirearchernow3", "@hirearchernow", "3", true},
		{"@hireguardnow", "@hireguardnow", "", false},   // 只有前缀、没编号
		{"@hirearchernow1", "@hireguardnow", "", false}, // 前缀不符
		{"", "@hireguardnow", "", false},
	}
	for _, c := range cases {
		got, ok := castleLabelIndex(c.label, c.prefix)
		if got != c.want || ok != c.ok {
			t.Errorf("castleLabelIndex(%q, %q) = (%q, %v)，期望 (%q, %v)",
				c.label, c.prefix, got, ok, c.want, c.ok)
		}
	}
}

// TestCastlePlayerAllowed 钉住原版 `TCastleOfficial` 最外层的门（ObjNpc.pas:612-616）：
// **只有占领行会的掌门人**能用城堡菜单；没入会 / 行会不是占领方 都不行。
func TestCastlePlayerAllowed(t *testing.T) {
	s, gm := guardTestServer()
	p := newTestPlayer(1, "掌门测试", entity.JobWarr)
	s.world.players[1] = p

	csOther := makeCastle(t, "别的行会", "", false)
	// ① 没入会
	if s.castlePlayerAllowed(nil, p, csOther) {
		t.Error("没入会不该放行")
	}
	// ② 入了会、是掌门，但行会不是占领方
	makeGuild(t, gm, "测试行会", "掌门测试")
	if s.castlePlayerAllowed(nil, p, csOther) {
		t.Error("非占领方行会的掌门不该放行")
	}
	// ③ 占领方行会的掌门 ⇒ 放行
	csOwn := makeCastle(t, "测试行会", "", false)
	if !s.castlePlayerAllowed(nil, p, csOwn) {
		t.Error("占领方行会的掌门应该放行")
	}
}

// TestCastleMaxUnitsSane 顺带钉一下菜单要用的上限（守卫/弓箭手数量），
// 免得菜单生成时列出越界编号。
func TestCastleMaxUnitsSane(t *testing.T) {
	if castle.MaxGuard <= 0 || castle.MaxArcher <= 0 {
		t.Fatalf("守卫/弓箭手上限应 > 0，实际 %d/%d", castle.MaxGuard, castle.MaxArcher)
	}
}

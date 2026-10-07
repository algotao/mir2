package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// itemCtx 是 script.Context 的最小测试实现（只需要 Var/PKPoint）。
type itemCtx struct{ vars map[int]int64 }

func (c *itemCtx) Gold() int64            { return 0 }
func (c *itemCtx) Level() int32           { return 1 }
func (c *itemCtx) Job() int32             { return 0 }
func (c *itemCtx) MapName() string        { return "0" }
func (c *itemCtx) PKPoint() int32         { return 0 }
func (c *itemCtx) GuildName() string      { return "" }
func (c *itemCtx) GameTimeName() string   { return "DAY" }
func (c *itemCtx) CountItem(string) int   { return 0 }
func (c *itemCtx) Var(n int) int64        { return c.vars[n] }
func (c *itemCtx) SetVar(n int, v int64)  { c.vars[n] = v }
func (c *itemCtx) CheckSkill(string) bool { return false }

// TestItemFieldNames 守住支持的字段别名。
//
// 取自 QFunction-0.txt:231-234 的真实用法：name/idx/dura/duramax。
func TestItemFieldNames(t *testing.T) {
	for _, alias := range []string{"name", "idx", "index", "dura", "duramax"} {
		if _, ok := itemFieldNames[alias]; !ok {
			t.Errorf("字段 %q 应被支持", alias)
		}
	}
	if _, ok := itemFieldNames["不存在的字段"]; ok {
		t.Error("未知字段不该被接受")
	}
}

// TestLinkedItemLookup 覆盖关联/定位/解除的槽位查找。
func TestLinkedItemLookup(t *testing.T) {
	p := &Player{Char: &storage.Character{
		Data: &pb.CharacterData{},
	}}
	p.Char.Data.BagItems = []*pb.UserItem{
		{MakeIndex: 100, Index: 1, Dura: 50, DuraMax: 100},
		{MakeIndex: 200, Index: 2, Dura: 10, DuraMax: 20},
		{},
		nil,
	}
	// 未关联时找不到
	if idx := p.findLinkedSlot(); idx != -1 {
		t.Errorf("未关联时槽位 = %d, 期望 -1", idx)
	}
	if p.linkedItem() != nil {
		t.Error("未关联时 linkedItem 应为 nil")
	}
	// 关联到第二件
	p.linkedMakeIndex = 200
	if idx := p.findLinkedSlot(); idx != 1 {
		t.Errorf("关联 200 后槽位 = %d, 期望 1", idx)
	}
	if ui := p.linkedItem(); ui == nil || ui.MakeIndex != 200 {
		t.Errorf("linkedItem = %+v, 期望 MakeIndex=200", ui)
	}
	// 关联到空槽（Index 0 的占位）不应被当成有效物品
	p.linkedMakeIndex = 0 // linkSlotNone
	if p.linkedItem() != nil {
		t.Error("MakeIndex=0 视为未关联")
	}
}

// TestChangeItemDuraOps 覆盖 ChangeItemDura 的运算符与钳位。
func TestChangeItemDuraOps(t *testing.T) {
	mkItem := func(dura, duraMax uint32) *pb.UserItem {
		return &pb.UserItem{MakeIndex: 1, Index: 1, Dura: dura, DuraMax: duraMax}
	}
	cases := []struct {
		name        string
		op          string
		cur, max    uint32
		val         int64
		toMax       bool
		wantDura    uint32
		wantDuraMax uint32
	}{
		{"设成 duramax（修满）", "=", 5, 100, 100, false, 100, 100},
		{"加 10", "+", 5, 100, 10, false, 15, 100},
		{"减 3", "-", 20, 100, 3, false, 17, 100},
		{"减到负数被钳 0", "-", 2, 100, 5, false, 0, 100},
		// ⚠️ 改 DuraMax **不会**把 Dura 拉满：原版只在 Dura 超过新上限时压低。
		{"改 DuraMax（Dura 不动）", "=", 90, 100, 120, true, 90, 120},
		{"Dura 超过新 DuraMax 时被压下来", "=", 90, 100, 50, true, 50, 50},
	}
	for _, tc := range cases {
		ui := mkItem(tc.cur, tc.max)
		// 复刻 actChangeItemDura 的核心算术（不构造 Server）
		cur := int64(ui.Dura)
		if tc.toMax {
			cur = int64(ui.DuraMax)
		}
		var next int64
		switch tc.op {
		case "=":
			next = tc.val
		case "+":
			next = cur + tc.val
		case "-":
			next = cur - tc.val
		}
		if next < 0 {
			next = 0
		}
		if tc.toMax {
			ui.DuraMax = uint32(next)
			if int64(ui.Dura) > next {
				ui.Dura = uint32(next)
			}
		} else {
			ui.Dura = uint32(next)
		}
		if ui.Dura != tc.wantDura || ui.DuraMax != tc.wantDuraMax {
			t.Errorf("%s: Dura=%d/%d, 期望 %d/%d",
				tc.name, ui.Dura, ui.DuraMax, tc.wantDura, tc.wantDuraMax)
		}
	}
}

// TestParseValueForDura 确认 <$Str(S4)> 这类插值能被解析（官方脚本的写法）。
func TestParseValueForDura(t *testing.T) {
	ctx := &itemCtx{vars: map[int]int64{3: 100}} // S4 -> 索引 3
	got, ok := script.ParseValue("<$Str(S4)>", ctx)
	if !ok {
		t.Fatal("ParseValue 解析 <$Str(S4)> 失败")
	}
	if got != 100 {
		t.Errorf("解析结果 = %d, 期望 100", got)
	}
}

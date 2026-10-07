package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
)

// TestStatusBitsMatchOfficial 状态位必须与官方**同构**（审计 §3.5）。
//
// 官方 `TBaseObject.GetCharStatus`（ObjBase.pas:20074-20088）：
// 状态计时器数组下标 i ⇒ `$80000000 shr i`，客户端 `Grobal2.pas:75-88` 给这些下标起了名字。
// 我们以前自排低位（1<<0..1<<4）还把状态塞进 Series ⇒ 真客户端拿到的状态恒为 0。
func TestStatusBitsMatchOfficial(t *testing.T) {
	cases := []struct {
		name  string
		got   uint32
		index int
	}{
		{"绿毒 POISON_DECHEALTH", entity.StatePoisonGreen, 0},
		{"红毒 POISON_DAMAGEARMOR", entity.StatePoisonRed, 1},
		{"石化 POISON_STONE", entity.StateStone, 5},
		{"隐身 STATE_TRANSPARENT", entity.StateInvisible, 8},
		{"防御上升 STATE_DEFENCEUP", entity.StateHolyArmor, 9},
		{"魔御上升 STATE_MAGDEFENCEUP", entity.StateSoulShield, 10},
		{"魔法盾 STATE_BUBBLEDEFENCEUP", entity.StateShield, 11},
	}
	for _, c := range cases {
		want := uint32(0x80000000) >> uint(c.index)
		if c.got != want {
			t.Errorf("%s 的位 = 0x%08X，期望 0x%08X（下标 %d）", c.name, c.got, want, c.index)
		}
	}
	// 位之间不能重叠（否则客户端分不清状态）
	seen := map[uint32]string{}
	for _, c := range cases {
		if prev, dup := seen[c.got]; dup {
			t.Errorf("%s 与 %s 的位相同（0x%08X）", c.name, prev, c.got)
		}
		seen[c.got] = c.name
	}
}

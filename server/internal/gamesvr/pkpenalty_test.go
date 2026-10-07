package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// weaponItem 造一把带 14 字节 Value 的武器（参数按**语义**给：幸运、诅咒）。
func weaponItem(luck, curse byte) *pb.UserItem {
	v := make([]byte, entity.ItemValueLen)
	v[btValueLuckIdx] = luck
	v[btValueCurseIdx] = curse
	return &pb.UserItem{Index: 1, Value: v}
}

func TestAddBodyLuck(t *testing.T) {
	// 单位是 5000（见 bodyLuckUnit 注释），所以杀一次 -500 只是 -0.1 单位。
	const unit = 5000.0
	cases := []struct {
		name  string
		start float64
		d     float64
		want  float64
		hit   bool
	}{
		{"杀一人（-500）", 0, -500, -500, true},
		{"加一点", -500, 100, -400, true},
		{"正向已满 +5 单位", 5 * unit, 10, 5 * unit, false},
		{"反向已满 -10 单位", -10 * unit, -10, -10 * unit, false},
		{"杀 100 次仍未到下限", 0, -500 * 100, -500 * 100, true},
		{"恰好触底后不动", -10 * unit, -500, -10 * unit, false},
		{"单次超大量被 clamp", 0, -60000, -10 * unit, true},
	}
	for _, c := range cases {
		d := &pb.CharacterData{BodyLuck: c.start}
		got := addBodyLuck(d, c.d)
		if got != c.hit {
			t.Errorf("%s: addBodyLuck = %v, 期望 %v", c.name, got, c.hit)
		}
		if got && d.BodyLuck != c.want {
			t.Errorf("%s: BodyLuck = %v, 期望 %v", c.name, d.BodyLuck, c.want)
		}
	}
}

// TestBodyLuckNoChangeWhenOutOfRange 确认越界时不写脏数据。
func TestBodyLuckNoChangeWhenOutOfRange(t *testing.T) {
	d := &pb.CharacterData{BodyLuck: 5 * bodyLuckUnit}
	if addBodyLuck(d, 10) {
		t.Error("正向已满（+5 单位），不该变化")
	}
	if d.BodyLuck != 5*bodyLuckUnit {
		t.Errorf("BodyLuck 被改成了 %v", d.BodyLuck)
	}
}

func TestWeaponValueIndex(t *testing.T) {
	// 索引与语义（2026-10-05 更正）：
	//   btValue[3] → 武器 AC 低位 → **幸运**（Grobal2.pas:547 + FState.pas:4065）
	//   btValue[4] → 武器 MAC 低位 → **诅咒**（Grobal2.pas:548 + FState.pas:4067）
	// 见 ItmUnit.pas:125-126（GetItemAddValue）与 itemabil.go 的说明。
	ui := weaponItem(7, 3)
	if ui.Value[btValueLuckIdx] != 7 {
		t.Errorf("幸运位 = %d, 期望 7", ui.Value[btValueLuckIdx])
	}
	if ui.Value[btValueCurseIdx] != 3 {
		t.Errorf("诅咒位 = %d, 期望 3", ui.Value[btValueCurseIdx])
	}
	if len(ui.Value) != 14 {
		t.Errorf("Value 长度 = %d, 期望 14（原版 btValue[0..13]）", len(ui.Value))
	}
}

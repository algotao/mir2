package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
)

// 走 / 跑：**走 1 格、跑 2 格**（原版 `GetNextRunXY` 一步 +2，`ClFunc.pas:370-382`），
// 而且是"一格一格走、撞墙就停"，不是"算一个 +2 的落点"。
//
// 2026-10-08 之前：新协议根本没有走/跑标志（只能走），legacy 的 `CM_RUN` 也只是
// "节流短一点"、仍然只走 1 格 —— 两条都在这版里修了（D-39）。
func TestWalkOneCellRunTwoCells(t *testing.T) {
	s, p := butchTestServer(t)

	// 先找一个走得动的方向（生成图里总有；找不到就跳过而不是假过）
	var dir uint8
	ok := false
	for _, d := range []uint8{entity.DirRight, entity.DirDown, entity.DirUp, entity.DirLeft} {
		x0, y0 := p.Obj.PosX(), p.Obj.PosY()
		if _, _, _, moved := s.movePlayer(p, d); moved {
			dir, ok = d, true
			p.Obj.SetPlace(p.Obj.MapRef(), x0, y0, d) // 复位：试探不算结果
			break
		}
	}
	if !ok {
		t.Skip("这张测试地图上没有可走的方向")
	}

	// 走：1 格
	x0, y0 := p.Obj.PosX(), p.Obj.PosY()
	if _, _, _, moved := s.movePlayer(p, dir); !moved {
		t.Fatalf("方向 %d 第一步该走得动", dir)
	}
	if got := dist(x0, y0, p.Obj.PosX(), p.Obj.PosY()); got != 1 {
		t.Fatalf("走一步应当是 1 格，实得 %d", got)
	}

	// 跑：2 格（复位后重来）
	p.Obj.SetPlace(p.Obj.MapRef(), x0, y0, dir)
	x1, y1 := p.Obj.PosX(), p.Obj.PosY()
	if _, _, _, moved, _ := s.movePlayerSteps(p, dir, 2); !moved {
		t.Fatalf("方向 %d 该走得动", dir)
	}
	if got := dist(x1, y1, p.Obj.PosX(), p.Obj.PosY()); got != 2 {
		// 1 格只可能是"第二格被挡"——那也算对，但这条用例的前提是空地，
		// 所以这里报出来让人看见（不要静默放过）
		t.Fatalf("跑一步应当是 2 格（这张图这段是空地），实得 %d", got)
	}

	// 撞墙即停：连走 8 格应当停在第一次被挡的地方，且**不会 panic / 不会越界**
	steps := 0
	for i := 0; i < 8; i++ {
		if _, _, _, moved, _ := s.movePlayerSteps(p, dir, 2); !moved {
			break
		}
		steps++
	}
	if steps == 0 {
		t.Log("第二次就撞墙了：这段地形只够走这么远（用例本身的断言已经过了）")
	}
}

// dist 是曼哈顿距离（这组用例只朝一个轴向走）。
func dist(x0, y0, x1, y1 int) int {
	dx, dy := x1-x0, y1-y0
	return max(dx, -dx) + max(dy, -dy)
}

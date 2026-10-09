package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// monsterBehavServer 起一个 50×50 的空地服务端 + 一个满血玩家（坐标由调用方摆）。
//
// 与 `butchTestServer` 同一套脚手架，只是这里还要摆怪，所以单独抽一个。
func monsterBehavServer(t *testing.T, px, py int) (*Server, *Player, *world.Map) {
	t.Helper()
	s, m := flagTestServer(t, "[0 比奇省 0] MINE\n")
	p := newTestPlayer(1, "行为测试", entity.JobWarr)
	p.Obj.SetPlace(m, px, py, 0)
	p.visible = entity.NewViewTracker()
	p.logonDone = true
	s.world.players[1] = p
	return s, p, m
}

// putMonster 把一只怪放进世界（并进空间索引）。
func putMonster(s *Server, mp *world.Map, mon *entity.Monster, x, y int, race uint16) {
	mon.SetPlace(mp, x, y, entity.DirDown)
	mon.Info.Race = race
	if mon.ViewRange == 0 {
		mon.ViewRange = 10
	}
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)
}

// 动物（鸡/鹿：race 50..79）**不还手，只逃跑** —— 原版 `TChickenDeer.Run`
//（`ObjMon.pas:542-598`）。这里钉住"玩家在它西边 ⇒ 它往东走"。
func TestAnimalFleesFromPlayer(t *testing.T) {
	s, _, mp := monsterBehavServer(t, 5, 5)
	mon := newTestMonster(2000, "鸡", 5)
	putMonster(s, mp, mon, 9, 5, entity.RcAnimal+1) // race 51 = 鸡

	before := mon.PosX()
	s.tickMonsters(time.Now())
	if mon.PosX() <= before {
		t.Fatalf("鸡该往**背离玩家**的方向跑：玩家 x=5、鸡原来 x=%d，跑完是 %d",
			before, mon.PosX())
	}
}

// 食人花（race 85 = `StickMonster`）**只在相邻八格咬人**。
//
// 原版 `StickMonster.AttackTarget` 走基类 `GetAttackDir`（只认八邻域），
// `AttackRange = 4` 是"目标跑出 4 格就缩回地下"的阈值，**不是攻击距离**
//（`ObjMon2.pas:174-198/276-281`）。我们原来按 `< 4 格` 判，等于给它装了门 4 格炮。
func TestStickBitesOnlyAdjacent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dist     int
		wantHurt bool
	}{
		{"相邻(1 格)该咬", 1, true},
		{"隔 3 格不该咬", 3, false},
	} {
		s, p, mp := monsterBehavServer(t, 5, 5)
		// 野生怪的"主动选目标"受 `-monster-aggro` 开关管（用例开关，见 `server.go:303`）；
		// 食人花靠的就是那一步选目标 ⇒ 这里显式打开，等价于线上默认。
		s.cfg.aggro = true
		mon := newTestMonster(2001, "食人花", 28)
		// 命中拉满、伤害拉高 ⇒ 只要"咬了"就一定掉血（去掉命中骰子的抖动）
		mon.Info.Hit = 255
		mon.Info.DC, mon.Info.DCMax = 20, 20
		putMonster(s, mp, mon, 5, 5+tc.dist, entity.RcStick)

		hp0 := p.hp()
		s.tickMonsters(time.Now())
		hurt := p.hp() < hp0
		if hurt != tc.wantHurt {
			t.Errorf("%s：距离 %d 格，掉血=%v（期望 %v）", tc.name, tc.dist, hurt, tc.wantHurt)
		}
		if mon.PosX() != 5 || mon.PosY() != 5+tc.dist {
			t.Errorf("%s：食人花不该挪窝（现在 %d,%d）", tc.name, mon.PosX(), mon.PosY())
		}
	}
}

// 移动**不许叠格**（原版 `Envir.CanWalkEx` 的对象阻挡，`Envir.pas:488-540`）。
//
// 甲在 (5,5)、乙在 (5,6)：甲朝下走必然被挡 ⇒ reason=3、位置不动。
func TestMoveBlockedByOtherPlayer(t *testing.T) {
	s, a, mp := monsterBehavServer(t, 5, 5)
	b := newTestPlayer(2, "乙", entity.JobWarr)
	b.Obj.SetPlace(mp, 5, 6, 0)
	b.visible = entity.NewViewTracker()
	s.world.players[2] = b
	s.world.index.Update(b)

	x, y, _, moved, reason := s.movePlayerSteps(a, entity.DirDown, 1)
	if moved {
		t.Fatalf("乙站在 (5,6)，甲不该能走进去（到了 %d,%d）", x, y)
	}
	if reason != 3 {
		t.Errorf("该报阻挡(reason=3)，实得 %d", reason)
	}
	if a.Obj.PosX() != 5 || a.Obj.PosY() != 5 {
		t.Errorf("被挡就该原地不动，现在在 %d,%d", a.Obj.PosX(), a.Obj.PosY())
	}
}

// 越界用 reason=2（原来那条是死枚举，永远只有 3）。
func TestMoveOutOfBoundsReason(t *testing.T) {
	s, p, mp := monsterBehavServer(t, 0, 25)
	p.Obj.SetPlace(mp, 0, 25, 0)
	s.world.index.Update(p)
	if _, _, _, moved, reason := s.movePlayerSteps(p, entity.DirLeft, 1); moved || reason != 2 {
		t.Fatalf("往地图外走该被拒且 reason=2，实得 moved=%v reason=%d", moved, reason)
	}
}

// 跑两步时**一格不让**：原版 `RunTo` 两格都要过、否则整体作废（`ObjBase.pas:9255-9362`）。
func TestRunTwoCellsAllOrNothing(t *testing.T) {
	s, a, mp := monsterBehavServer(t, 5, 5)
	b := newTestPlayer(2, "乙", entity.JobWarr)
	b.Obj.SetPlace(mp, 5, 7, 0) // 第二格被占
	b.visible = entity.NewViewTracker()
	s.world.players[2] = b
	s.world.index.Update(b)

	_, _, _, moved, reason := s.movePlayerSteps(a, entity.DirDown, 2)
	if moved || reason != 3 {
		t.Fatalf("第二格被占 ⇒ 整个跑动作废，实得 moved=%v reason=%d", moved, reason)
	}
	if a.Obj.PosX() != 5 || a.Obj.PosY() != 5 {
		t.Errorf("半跑不该发生：现在在 %d,%d", a.Obj.PosX(), a.Obj.PosY())
	}
}

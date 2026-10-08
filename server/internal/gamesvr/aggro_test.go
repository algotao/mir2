package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
)

// ---------- 选目标：**种族**决定"打不打人"（原版 `Grobal2.pas:1100-1106` 的 RC_*）----------
//
// ⚠️ `tickMonsters` 的选目标这一段**以前完全没有测试** —— 而"新手村被鸡鹿追着打"与
// "野生弓箭守卫追着白名打"两条的根因恰好都在这里（2026-10-08 用户报的）。
// 这组用例把五种情形一次钉住：动物不打、怪物打、守卫只打红名。

// newRaceMonster 造一只**指定种族**的怪，贴在玩家身边（视野内、相邻）。
//
// 距离与视野都刻意放宽（相邻 + ViewRange 20）⇒ 用例里唯一的变量就是**种族**与**PK 值**，
// 不会因为"太远"而白过。
func newRaceMonster(t *testing.T, s *Server, p *Player, id uint32, race uint16) *entity.Monster {
	t.Helper()
	m := newTestMonster(id, "测试怪", 100)
	m.Info.Race = race
	m.SetPlace(p.Obj.MapRef(), p.Obj.PosX()+1, p.Obj.PosY(), entity.DirLeft)
	m.ViewRange = 20
	s.world.monsters[m.ID] = m
	s.world.monsterIdx.Add(m)
	return m
}

func TestRaceDecidesAggro(t *testing.T) {
	cases := []struct {
		name     string
		race     uint16
		pkPoint  int64
		wantLock bool
	}{
		{"鸡（动物 51）不主动打人", entity.RcAnimal + 1, 0, false},
		{"鹿（动物 52）不主动打人", entity.RcAnimal + 2, 0, false},
		{"稻草人（怪物 83）会主动打", 83, 0, true},
		{"弓箭守卫（112）不打白名", entity.RcArcherGuard, 0, false},
		{"弓箭守卫（112）打红名", entity.RcArcherGuard, 200, true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, p := butchTestServer(t)
			s.cfg.aggro = true // 野生怪的默认就是"主动"（`-monster-aggro` 默认 true）
			p.Char.Data.PkPoint = c.pkPoint
			m := newRaceMonster(t, s, p, uint32(1000+i), c.race)

			now := time.Now()
			for k := 0; k < 3; k++ { // 跑几拍：免得"第一拍还没选上"被当成通过
				s.tickMonsters(now.Add(time.Duration(k) * time.Second))
			}
			if got := m.TargetID != 0; got != c.wantLock {
				t.Errorf("race=%d pkPoint=%d：锁定了目标 = %v，期望 %v",
					c.race, c.pkPoint, got, c.wantLock)
			}
		})
	}
}

// 守卫那一对：**同一只怪、同一个位置**，只差玩家的 PK 值 —— 白名不锁、红名锁、洗白再放下。
func TestGuardOnlyTargetsRedName(t *testing.T) {
	s, p := butchTestServer(t)
	s.cfg.aggro = true
	m := newRaceMonster(t, s, p, 2000, entity.RcArcherGuard)

	// 白名（新号就是这个状态）：守卫不该理他
	s.tickMonsters(time.Now())
	if m.TargetID != 0 {
		t.Fatalf("白名玩家不该被守卫锁定（TargetID=%d）", m.TargetID)
	}

	// 红名（`PKLevel 2` = 200 点）：下一拍就该锁上
	p.Char.Data.PkPoint = 200
	s.tickMonsters(time.Now().Add(time.Second))
	if m.TargetID != p.Obj.ID {
		t.Fatalf("红名玩家该被守卫锁定：TargetID=%d 期望 %d", m.TargetID, p.Obj.ID)
	}

	// 又洗白：立刻放下武器（原版每轮都重算 `IsProperTarget`）
	p.Char.Data.PkPoint = 0
	s.tickMonsters(time.Now().Add(2 * time.Second))
	if m.TargetID != 0 {
		t.Fatalf("洗白之后守卫该放下武器（TargetID=%d）", m.TargetID)
	}
}

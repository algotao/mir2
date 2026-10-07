package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

// TestPlayerHitPoint 守住命中/敏捷的数值（RecalcAbilitys，ObjBase.pas:18563-18635）。
//
//	命中 = DEFHIT(5) + 基本剑术(3) 3×等级 + 精神力战法(4) Round(8/3×等级)
//	                + 攻杀剑术(7) 等级
//	敏捷 = DEFSPEED(15)（道士 +3）
func TestPlayerHitPoint(t *testing.T) {
	srv := &Server{}
	mk := func(job uint32, magics ...*pb.UserMagic) *Player {
		return &Player{Char: &storage.Character{Job: job,
			Data: &pb.CharacterData{Job: job, Magics: magics}}}
	}
	um := func(id, lv uint32) *pb.UserMagic { return &pb.UserMagic{MagicId: id, Level: lv} }

	for _, c := range []struct {
		name string
		p    *Player
		want int
	}{
		{"什么都没学 ⇒ 只有基数", mk(entity.JobWarr), entity.DefHit},
		// ⚠️ 等级 0 **不加**：`@grant-all-magics` 给的 0 级技能照样会打空
		{"学了但 0 级 ⇒ 不加", mk(entity.JobWarr, um(3, 0), um(4, 0), um(7, 0)), entity.DefHit},
		{"基本剑术 3 级 ⇒ +9", mk(entity.JobWarr, um(3, 3)), entity.DefHit + 9},
		{"基本剑术 1 级 ⇒ +3", mk(entity.JobWarr, um(3, 1)), entity.DefHit + 3},
		{"精神力战法 1/2/3 级 ⇒ +3/+5/+8", mk(entity.JobWarr, um(4, 1)), entity.DefHit + 3},
		{"攻杀 3 级 ⇒ +3", mk(entity.JobWarr, um(7, 3)), entity.DefHit + 3},
		{"三个都满 ⇒ +20", mk(entity.JobWarr, um(3, 3), um(4, 3), um(7, 3)), entity.DefHit + 9 + 8 + 3},
	} {
		if got := srv.playerHitPoint(c.p); got != c.want {
			t.Errorf("%s: playerHitPoint = %d，期望 %d", c.name, got, c.want)
		}
	}

	if got := srv.playerSpeedPoint(mk(entity.JobWarr)); got != entity.DefSpeed {
		t.Errorf("战士敏捷 = %d，期望 %d", got, entity.DefSpeed)
	}
	if got := srv.playerSpeedPoint(mk(entity.JobTaos)); got != entity.DefSpeed+3 {
		t.Errorf("道士敏捷 = %d，期望 %d（+3）", got, entity.DefSpeed+3)
	}
}

// TestRollMelee 守住"打空返回 0、命中才叠充能加成"。
func TestRollMelee(t *testing.T) {
	srv := &Server{}

	// 打空：返回 (0, true)，且**不**消耗随机数算威力
	restore := func() {}
	old := delphi.RandN
	delphi.RandN = func(int) int { return 14 } // 保证 5 < 14 ⇒ 打空
	power, missed := srv.rollMelee(5, 3, 15, 0, 10, 10, warBonus{plus: 8})
	restore()
	delphi.RandN = old
	if !missed || power != 0 {
		t.Errorf("打空时应返回 (0,true)，实际 (%d,%v)", power, missed)
	}

	// 命中：威力 = 掷骰 + 充能加成
	delphi.RandN = func(int) int { return 0 }
	power, missed = srv.rollMelee(14, 3, 10, 0, 10, 10, warBonus{plus: 8})
	delphi.RandN = old
	if missed {
		t.Error("命中 14 对敏捷 10 不该打空")
	}
	if power != 18 {
		t.Errorf("威力 = %d，期望 18（10 + 攻杀 8）", power)
	}
}

package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
)

// TestLevelForExp 守住"累计经验 → 等级"的反向换算（与 entity.CheckLevelUp 同一张表）。
func TestLevelForExp(t *testing.T) {
	for lv := uint32(1); lv <= 40; lv++ {
		need := entity.NeedExp(lv)
		if got := levelForExp(need); got != lv {
			t.Errorf("经验 %d（%d 级下限）反查出 %d 级", need, lv, got)
		}
		// 差 1 点就应退回上一级
		if lv > 1 {
			if got := levelForExp(need - 1); got != lv-1 {
				t.Errorf("经验 %d（差一点到 %d 级）反查出 %d 级", need-1, lv, got)
			}
		}
	}
	if got := levelForExp(0); got != 1 {
		t.Errorf("0 经验应在 1 级，得到 %d", got)
	}
}

// TestLoseExpWithLevelDrop 守住扣经验/掉级（原版 ObjBase.pas:21166-21190 在我们
// "累计经验"模型下的等价写法）：扣完不足当前等级下限 ⇒ 掉级。
func TestLoseExpWithLevelDrop(t *testing.T) {
	srv := &Server{}
	// 20 级 + 刚好比 20 级下限多 500 点经验
	p := newTestPlayer(1, "输家", entity.JobWarr)
	p.Char.Data.Abil.Level = 20
	p.Char.Data.Abil.Exp = entity.NeedExp(20) + 500

	// 扣 200：还在 20 级
	srv.loseExpWithLevelDrop(p, 200)
	if p.Char.Data.Abil.Level != 20 || p.Char.Data.Abil.Exp != entity.NeedExp(20)+300 {
		t.Errorf("扣 200 后 = %d 级/经验 %d，期望 20 级/%d",
			p.Char.Data.Abil.Level, p.Char.Data.Abil.Exp, entity.NeedExp(20)+300)
	}

	// 再扣 1000：越过 20 级下限 ⇒ 掉到 19 级
	srv.loseExpWithLevelDrop(p, 1000)
	if p.Char.Data.Abil.Level != 19 {
		t.Errorf("扣到不足 20 级下限后应掉到 19 级，得到 %d 级", p.Char.Data.Abil.Level)
	}
	if p.Char.Data.Abil.Exp >= entity.NeedExp(20) {
		t.Errorf("掉级后经验 %d 不该仍在 20 级下限（%d）之上",
			p.Char.Data.Abil.Exp, entity.NeedExp(20))
	}

	// 扣到 0 也不会掉到 1 级以下
	p.Char.Data.Abil.Level = 2
	p.Char.Data.Abil.Exp = 50
	srv.loseExpWithLevelDrop(p, 100000)
	if p.Char.Data.Abil.Level < 1 {
		t.Errorf("等级不该低于 1，得到 %d", p.Char.Data.Abil.Level)
	}
	if p.Char.Data.Abil.Exp != 0 {
		t.Errorf("扣光后经验应为 0，得到 %d", p.Char.Data.Abil.Exp)
	}
}

// TestApplyPKDieRewardDefaultOff 守住"出厂四个开关全关 ⇒ 什么都不发生"。
//
// 原版出厂就是全关（M2Share.pas:1779-1782），官方 !setup.txt:937-944 也全是 0
// ⇒ 默认行为必须与官方一致，别把开关改成默认开。
func TestApplyPKDieRewardDefaultOff(t *testing.T) {
	srv := &Server{}
	srv.pvp.cfg = defaultPVPConfig()
	killer := newTestPlayer(1, "赢家", entity.JobWarr)
	victim := newTestPlayer(2, "输家", entity.JobWarr)
	killer.Char.Data.Abil.Level, killer.Char.Data.Abil.Exp = 30, entity.NeedExp(30)+1000
	victim.Char.Data.Abil.Level, victim.Char.Data.Abil.Exp = 30, entity.NeedExp(30)+1000

	srv.applyPKDieReward(killer, victim)
	if killer.Char.Data.Abil.Level != 30 || victim.Char.Data.Abil.Level != 30 {
		t.Errorf("出厂全关时等级不该变：%d / %d",
			killer.Char.Data.Abil.Level, victim.Char.Data.Abil.Level)
	}
	if killer.Char.Data.Abil.Exp != entity.NeedExp(30)+1000 {
		t.Errorf("出厂全关时经验不该变：%d", killer.Char.Data.Abil.Exp)
	}

	// 打开四个开关后，同样的输入应产生奖惩（验证接线是通的）
	srv.pvp.cfg.cfg.PKDieWinLevel = true
	srv.pvp.cfg.cfg.PKDieLostLevel = true
	srv.applyPKDieReward(killer, victim)
	if killer.Char.Data.Abil.Level != 31 {
		t.Errorf("开关打开后赢家应升到 31 级，得到 %d", killer.Char.Data.Abil.Level)
	}
	if victim.Char.Data.Abil.Level != 29 {
		t.Errorf("开关打开后输家应掉到 29 级，得到 %d", victim.Char.Data.Abil.Level)
	}
}

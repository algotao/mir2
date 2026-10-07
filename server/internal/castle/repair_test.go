package castle

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

func TestDefaultConfigRepairPrices(t *testing.T) {
	c := New(DefaultConfig(), DefaultRecord())
	cfg := c.Config()
	// 数值来自 M2Share.pas:2090-2102
	if cfg.RepairDoorPrice != 2_000_000 {
		t.Errorf("修门费 = %d, 期望 2000000", cfg.RepairDoorPrice)
	}
	if cfg.RepairWallPrice != 500_000 {
		t.Errorf("修墙费 = %d, 期望 500000", cfg.RepairWallPrice)
	}
	if cfg.HireGuardPrice != 300_000 {
		t.Errorf("雇佣守卫费 = %d, 期望 300000", cfg.HireGuardPrice)
	}
	if cfg.HireArcherPrice != 300_000 {
		t.Errorf("雇佣弓手费 = %d, 期望 300000", cfg.HireArcherPrice)
	}
	if cfg.RepairImmunity != 60*time.Second {
		t.Errorf("修缮免疫 = %v, 期望 60s", cfg.RepairImmunity)
	}
}

func TestRepairUnitGateways(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	c := New(DefaultConfig(), DefaultRecord())
	// 未开战
	c.Run(now.Add(-2*time.Hour), Hooks{})

	rich := int64(10_000_000)
	cases := []struct {
		name      string
		kind      storage.CastleUnitKind
		idx       int
		hp, maxHP int
		alive     bool
		struck    time.Duration // 距今多久被打
		gold      int64
		want      int
	}{
		{"城门满血", storage.CastleMainDoor, 0, 10000, 10000, true, 0, rich, RepairAlreadyFull},
		{"城门破损可修", storage.CastleMainDoor, 0, 0, 10000, false, 2 * time.Minute, rich, RepairOK},
		{"左墙破损可修", storage.CastleWall, 0, 0, 5000, false, 2 * time.Minute, rich, RepairOK},
		{"中墙破损可修", storage.CastleWall, 1, 0, 5000, false, 2 * time.Minute, rich, RepairOK},
		{"右墙编号越界", storage.CastleWall, 9, 0, 5000, false, 0, rich, RepairNoSuchUnit},
		{"守卫未雇佣可雇", storage.CastleGuard, 0, 0, 0, false, 0, rich, RepairOK},
		{"守卫编号越界", storage.CastleGuard, 9, 0, 0, false, 0, rich, RepairNoSuchUnit},
		{"弓手编号越界", storage.CastleArcher, 20, 0, 0, false, 0, rich, RepairNoSuchUnit},
		{"钱不够", storage.CastleWall, 0, 0, 5000, false, 2 * time.Minute, 100, RepairNoGold},
	}
	for _, tc := range cases {
		got, _ := c.RepairUnit(tc.kind, tc.idx, tc.hp, tc.maxHP, tc.alive,
			now.Add(-tc.struck), now, tc.gold)
		if got != tc.want {
			t.Errorf("%s: = %d, 期望 %d", tc.name, got, tc.want)
		}
	}
}

// TestRepairUnitStruckImmunity 守住"被攻击 60 秒内不能修"
// （Castle.pas:1158 的 `(GetTickCount - m_dwStruckTick) > 60*1000`）。
func TestRepairUnitStruckImmunity(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	c := New(DefaultConfig(), DefaultRecord())
	rich := int64(10_000_000)

	// 30 秒前被打过 → 免疫
	if got, _ := c.RepairUnit(storage.CastleMainDoor, 0, 0, 10000, false,
		now.Add(-30*time.Second), now, rich); got != RepairTooSoon {
		t.Errorf("30 秒内被攻击 = %d, 期望 %d（免疫）", got, RepairTooSoon)
	}
	// 恰好 60 秒 → 边界。原版是 `> 60*1000` 才允许，所以 60 秒整仍免疫。
	if got, _ := c.RepairUnit(storage.CastleMainDoor, 0, 0, 10000, false,
		now.Add(-60*time.Second), now, rich); got != RepairTooSoon {
		t.Errorf("恰好 60 秒 = %d, 期望 %d（`>60s` 才放行）", got, RepairTooSoon)
	}
	// 61 秒 → 放行
	if got, _ := c.RepairUnit(storage.CastleMainDoor, 0, 0, 10000, false,
		now.Add(-61*time.Second), now, rich); got != RepairOK {
		t.Errorf("61 秒 = %d, 期望 %d", got, RepairOK)
	}
	// 从未被打（零值）→ 放行
	if got, _ := c.RepairUnit(storage.CastleMainDoor, 0, 0, 10000, false,
		time.Time{}, now, rich); got != RepairOK {
		t.Errorf("从未被攻击 = %d, 期望 %d", got, RepairOK)
	}
}

// TestRepairUnitUnderWarForbidden 攻城战中不能修
// （原版 RepairDoor/RepairWall 的 Exit 条件含 m_boUnderWar）。
func TestRepairUnitUnderWarForbidden(t *testing.T) {
	day := time.Date(2026, 10, 3, 0, 0, 0, 0, time.Local)
	now := day.Add(20 * time.Hour) // 开城时刻
	c := New(DefaultConfig(), DefaultRecord())
	c.rec.Attackers = append(c.rec.Attackers, Attacker{GuildName: "攻会", AttackDate: day})
	c.Run(now, Hooks{})
	if !c.UnderWar() {
		t.Fatal("前提不成立：应已开战")
	}
	rich := int64(10_000_000)
	if got, _ := c.RepairUnit(storage.CastleMainDoor, 0, 0, 10000, false,
		now.Add(-2*time.Minute), now.Add(time.Minute), rich); got != RepairNotUnderWar {
		t.Errorf("攻城期中修门 = %d, 期望 %d", got, RepairNotUnderWar)
	}
	// ⚠️ 雇佣**不受**攻城期限制（原版 HireGuard 只查 Guards[i].BaseObject = nil）。
	if got, _ := c.RepairUnit(storage.CastleGuard, 0, 0, 0, false,
		time.Time{}, now.Add(time.Minute), rich); got != RepairOK {
		t.Errorf("攻城期中雇佣守卫 = %d, 期望 %d（原版允许）", got, RepairOK)
	}
}

func TestHireUnit(t *testing.T) {
	c := New(DefaultConfig(), DefaultRecord())
	// 官方配置里守卫 0/1 有坐标，2/3 是 0,0
	before, _ := c.UnitConfig(storage.CastleGuard, 3)
	if before.HP != 0 || before.X != 0 || before.Y != 0 {
		t.Fatalf("前提不成立：守卫3 应是未雇佣的 0,0，实际 %+v", before)
	}
	if !c.HireUnit(storage.CastleGuard, 3, 9999) {
		t.Fatal("HireUnit 应成功")
	}
	after, _ := c.UnitConfig(storage.CastleGuard, 3)
	if after.HP != 9999 {
		t.Errorf("HP = %d, 期望 9999", after.HP)
	}
	// 0,0 会被补一个默认位（城门附近），否则生成不出来
	if after.X == 0 || after.Y == 0 {
		t.Errorf("坐标应被补默认值，实际 (%d,%d)", after.X, after.Y)
	}
	if c.HireUnit(storage.CastleGuard, 99, 9999) {
		t.Error("越界编号不该成功")
	}
}

package entity

import "testing"

// TestNeedExpFallbackMatchesOldFormula 守住"没有官方表时行为不变"。
//
// 旧公式 100·(L-1)²：L2→100、L3→400、L10→8100、L30→84100。
// 改造后这些值必须逐位一致（否则所有既存档角色的等级都会漂移）。
func TestNeedExpFallbackMatchesOldFormula(t *testing.T) {
	SetLevelNeed(nil)
	defer SetLevelNeed(nil)
	for _, c := range []struct {
		level uint32
		want  uint64
	}{{1, 0}, {2, 100}, {3, 400}, {4, 900}, {10, 8100}, {30, 84100}} {
		if got := NeedExp(c.level); got != c.want {
			t.Errorf("NeedExp(%d) = %d，期望 %d（旧公式 100·(L-1)²）", c.level, got, c.want)
		}
	}
	// 逐级差分也要能对上：needAt(L) = 100·(2L-1)。
	for lv := uint32(1); lv <= 10; lv++ {
		if got, want := needAt(lv), 100*(2*uint64(lv)-1); got != want {
			t.Errorf("needAt(%d) = %d，期望 %d", lv, got, want)
		}
	}
}

// TestNeedExpCumulativeFromTable 官方表是**每级**需求，NeedExp 负责累加。
//
// 官方 [Exp] 头部：Level1=100、Level2=200、Level3=300、Level4=400、Level5=600。
// ⇒ 累计：L1=0、L2=100、L3=300、L4=600、L5=1000、L6=1600。
func TestNeedExpCumulativeFromTable(t *testing.T) {
	// 下标即等级：need[1]=100 表示 1→2 需 100。
	table := []uint64{0, 100, 200, 300, 400, 600, 1000}
	SetLevelNeed(table)
	defer SetLevelNeed(nil)

	for _, c := range []struct {
		level uint32
		want  uint64
	}{{1, 0}, {2, 100}, {3, 300}, {4, 600}, {5, 1000}, {6, 1600}} {
		if got := NeedExp(c.level); got != c.want {
			t.Errorf("NeedExp(%d) = %d，期望 %d（官方表累计）", c.level, got, c.want)
		}
	}
	// 客户端要的 MaxExp 是**每级**需求，不是累计。
	if got := LevelNeed(1); got != 100 {
		t.Errorf("LevelNeed(1) = %d，期望 100", got)
	}
	if got := LevelNeed(4); got != 400 {
		t.Errorf("LevelNeed(4) = %d，期望 400", got)
	}
}

// TestLevelProgress 累计 → 级内 的换算（客户端经验条用）。
func TestLevelProgress(t *testing.T) {
	table := []uint64{0, 100, 200, 300}
	SetLevelNeed(table)
	defer SetLevelNeed(nil)

	cases := []struct {
		cumExp uint64
		level  uint32
		want   uint64
	}{
		{0, 1, 0},     // 1 级起点
		{50, 1, 50},   // 1 级内一半
		{100, 2, 0},   // 刚升到 2 级
		{250, 2, 150}, // 2 级内 150/200
		{300, 3, 0},   // 刚升到 3 级
		{50, 5, 0},    // 累计值小于本级起点（脏档）⇒ 夹到 0，不为负
	}
	for _, c := range cases {
		if got := LevelProgress(c.cumExp, c.level); got != c.want {
			t.Errorf("LevelProgress(%d, %d) = %d，期望 %d", c.cumExp, c.level, got, c.want)
		}
	}
}

// TestCheckLevelUpUsesTable 升级判定必须跟着官方表走。
//
// 表下 L2 需 100：给 99 点不升、给 100 点升到 2 级。
func TestCheckLevelUpUsesTable(t *testing.T) {
	table := []uint64{0, 100, 200, 300, 400, 600, 1000}
	SetLevelNeed(table)
	defer SetLevelNeed(nil)

	if res := CheckLevelUp(1, 99); res.Gained != 0 {
		t.Errorf("累计 99 经验不该升级，实际升了 %d 级", res.Gained)
	}
	res := CheckLevelUp(1, 100)
	if res.Gained != 1 || res.NewLevel != 2 {
		t.Fatalf("累计 100 经验应升到 2 级，实际 level=%d gained=%d", res.NewLevel, res.Gained)
	}
	// 一次给足 3 级（600 累计）应连升到 4 级。
	res = CheckLevelUp(1, 600)
	if res.NewLevel != 4 || res.Gained != 3 {
		t.Errorf("累计 600 经验应升到 4 级，实际 level=%d gained=%d", res.NewLevel, res.Gained)
	}
}

// TestTableGapFallsBackPerLevel 表里为 0（官方文件偶有空值）的级退回公式。
func TestTableGapFallsBackPerLevel(t *testing.T) {
	SetLevelNeed([]uint64{0, 100, 0, 300})
	defer SetLevelNeed(nil)
	if got := LevelNeed(1); got != 100 {
		t.Errorf("LevelNeed(1) = %d，期望 100（用表）", got)
	}
	// 2 级在表里是 0 → 退回公式 needAt(2) = 100*(2*2-1) = 300。
	if got := LevelNeed(2); got != 300 {
		t.Errorf("LevelNeed(2) = %d，期望 300（表缺 → 退回公式）", got)
	}
	if got := LevelNeed(3); got != 300 {
		t.Errorf("LevelNeed(3) = %d，期望 300（用表）", got)
	}
}

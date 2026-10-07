package castle

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// ---------- 测试替身 ----------

// fakeGuilds 是 GuildResolver 的测试实现。
type fakeGuilds map[string]*storage.Guild

func (f fakeGuilds) Find(name string) *storage.Guild { return f[name] }

// base 是测试里的"当天零点"。
var base = time.Date(2026, 10, 3, 0, 0, 0, 0, time.Local)

// newTestCastle 造一座测试城堡：占领方 "守会"。
func newTestCastle(t *testing.T) (*Castle, *fakeGuilds) {
	t.Helper()
	rec := DefaultRecord()
	rec.OwnGuild = "守会"
	c := New(DefaultConfig(), rec)
	return c, &fakeGuilds{
		"守会":  {Name: "守会", Allies: []string{"守会盟"}},
		"攻会":  {Name: "攻会", Allies: []string{"攻会盟"}},
		"守会盟": {Name: "守会盟"},
		"攻会盟": {Name: "攻会盟"},
	}
}

// at 返回当天某时某分。
func at(h, m int) time.Time {
	return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// declareForToday 直接把宣战日设成今天（跳过 DeclareDays 的等待）。
func declareForToday(c *Castle, guilds ...string) {
	for _, g := range guilds {
		c.rec.Attackers = append(c.rec.Attackers, Attacker{GuildName: g, AttackDate: base})
	}
}

func TestAddAttackerSetsDateAndDedups(t *testing.T) {
	c, _ := newTestCastle(t)
	now := at(10, 0)

	if !c.AddAttacker("攻会", now) {
		t.Fatal("首次宣战应成功")
	}
	// 预定日 = 宣战日 + DeclareDays(4)（Castle.pas:1228）。
	if got, want := c.Attackers()[0].AttackDate, now.AddDate(0, 0, 4); !got.Equal(want) {
		t.Errorf("预定攻城日 = %v, 期望 %v", got, want)
	}
	if c.AddAttacker("攻会", now) {
		t.Error("重复宣战应被拒绝（原版 InAttackerList）")
	}
	if !c.CancelAttacker("攻会") {
		t.Error("CancelAttacker 应能撤销")
	}
	if len(c.Attackers()) != 0 {
		t.Errorf("撤销后队列应为空，实际 %d 条", len(c.Attackers()))
	}
}

// TestRunStartsWarAtConfiguredHour 覆盖 Castle.pas:644-679 的开门判定。
func TestRunStartsWarAtConfiguredHour(t *testing.T) {
	c, _ := newTestCastle(t)
	declareForToday(c, "攻会")

	var started, ended, soon int
	h := Hooks{
		OnWarStart:   func(*Castle) { started++ },
		OnWarEnd:     func(*Castle) { ended++ },
		OnEndingSoon: func(*Castle, time.Duration) { soon++ },
	}

	c.Run(at(19, 59), h)
	if c.UnderWar() {
		t.Fatal("19:59 不该开战")
	}
	if started != 0 {
		t.Fatalf("开战回调不该被调用，实际 %d 次", started)
	}

	c.Run(at(20, 0), h)
	if !c.UnderWar() {
		t.Fatal("20:00 应开战")
	}
	if started != 1 {
		t.Errorf("开战回调应触发 1 次，实际 %d", started)
	}
	// ★守方也进名单（原版 Castle.pas:670）。
	if got := c.Participants(); len(got) != 2 || got[0] != "攻会" || got[1] != "守会" {
		t.Errorf("本场名单 = %v, 期望 [攻会 守会]", got)
	}
	if !c.StartWar() {
		t.Error("startWar 应已置位")
	}

	// 同一天再跑不会重复开战。
	c.Run(at(20, 30), h)
	if started != 1 {
		t.Errorf("同一天不该重复开战，实际触发 %d 次", started)
	}

	// 到点结束：at(20,0) + 3h = 23:00。
	c.Run(at(23, 1), h)
	if c.UnderWar() {
		t.Fatal("超过攻守期应结束")
	}
	if ended != 1 {
		t.Errorf("结束回调应触发 1 次，实际 %d", ended)
	}
	if len(c.Participants()) != 0 {
		t.Errorf("结束后名单应清空，实际 %v", c.Participants())
	}
}

// TestRunDoesNotStartWithoutDueAttacker 没到期的宣战不开战。
func TestRunDoesNotStartWithoutDueAttacker(t *testing.T) {
	c, _ := newTestCastle(t)
	c.rec.Attackers = append(c.rec.Attackers,
		Attacker{GuildName: "攻会", AttackDate: base.AddDate(0, 0, 4)})

	var started int
	c.Run(at(20, 0), Hooks{OnWarStart: func(*Castle) { started++ }})
	if c.UnderWar() || started != 0 {
		t.Fatal("宣战日未到不该开战")
	}
	if len(c.Attackers()) != 1 {
		t.Errorf("未到期的宣战应保留，实际 %d 条", len(c.Attackers()))
	}
}

// TestRunDropsStaleAttackers 过期未打的宣战要清掉（原版会永久滞留）。
func TestRunDropsStaleAttackers(t *testing.T) {
	c, _ := newTestCastle(t)
	c.rec.Attackers = []Attacker{
		{GuildName: "过期会", AttackDate: base.AddDate(0, 0, -1)},
		{GuildName: "未来会", AttackDate: base.AddDate(0, 0, 4)},
	}
	c.Run(at(20, 0), Hooks{})
	if got := c.Attackers(); len(got) != 1 || got[0].GuildName != "未来会" {
		t.Errorf("宣战队列 = %+v, 期望只留未来会", got)
	}
}

// TestRunDayRollover 跨日重置当日税收并重新允许开战（Castle.pas:636-643）。
func TestRunDayRollover(t *testing.T) {
	c, _ := newTestCastle(t)
	declareForToday(c, "攻会")
	c.Run(at(20, 0), Hooks{})
	if !c.UnderWar() {
		t.Fatal("应已开战")
	}
	c.rec.TodayIncome = 12345

	var dayChanges int
	next := at(20, 0).AddDate(0, 0, 1)
	c.Run(next, Hooks{OnDayChange: func(*Castle) { dayChanges++ }})

	if dayChanges != 1 {
		t.Errorf("跨日回调应触发 1 次，实际 %d", dayChanges)
	}
	if c.TodayIncome() != 0 {
		t.Errorf("跨日后当日税收应清零，实际 %d", c.TodayIncome())
	}
	if c.StartWar() {
		t.Error("跨日后 startWar 应复位")
	}
	if c.UnderWar() {
		t.Error("跨日后不应仍在攻城（攻守期只有 3h，新的一天要重新等宣战）")
	}
}

// TestEndingSoonBroadcastOnce 提前 10 分钟只广播一次（Castle.pas:696-703）。
func TestEndingSoonBroadcastOnce(t *testing.T) {
	c, _ := newTestCastle(t)
	declareForToday(c, "攻会")
	c.Run(at(20, 0), Hooks{})

	var lefts []time.Duration
	h := Hooks{OnEndingSoon: func(_ *Castle, left time.Duration) { lefts = append(lefts, left) }}
	c.Run(at(22, 50), h) // 2h50m，刚跨过 3h-10m 阈值
	c.Run(at(22, 55), h)
	c.Run(at(22, 59), h)
	c.Run(at(23, 1), h)

	if len(lefts) != 1 {
		t.Fatalf("结束预告应只广播 1 次，实际 %d 次（%v）", len(lefts), lefts)
	}
	if lefts[0] > 10*time.Minute {
		t.Errorf("剩余时间 = %v, 应 <= 10m", lefts[0])
	}
}

// ---------- 阵营判定 ----------

func TestFactionPredicates(t *testing.T) {
	c, g := newTestCastle(t)
	declareForToday(c, "攻会")
	c.Run(at(20, 0), Hooks{})

	cases := []struct {
		name                                 string
		attack, attackAlly, defense, defAlly bool
	}{
		{"守会", false, false, true, false},  // 占领方 = 守方
		{"攻会", true, false, false, false},  // 宣战方 = 攻方
		{"攻会盟", false, true, false, false}, // 攻方盟友
		{"守会盟", false, false, false, true}, // 守方盟友
		{"路人", false, false, false, false}, // 无关系
	}
	for _, tc := range cases {
		if got := c.IsAttackGuild(tc.name); got != tc.attack {
			t.Errorf("IsAttackGuild(%q) = %v, 期望 %v", tc.name, got, tc.attack)
		}
		if got := c.IsAttackAllyGuild(*g, tc.name); got != tc.attackAlly {
			t.Errorf("IsAttackAllyGuild(%q) = %v, 期望 %v", tc.name, got, tc.attackAlly)
		}
		if got := c.IsDefenseGuild(tc.name); got != tc.defense {
			t.Errorf("IsDefenseGuild(%q) = %v, 期望 %v", tc.name, got, tc.defense)
		}
		if got := c.IsDefenseAllyGuild(*g, tc.name); got != tc.defAlly {
			t.Errorf("IsDefenseAllyGuild(%q) = %v, 期望 %v", tc.name, got, tc.defAlly)
		}
	}

	// 无主时任何行会都不是占领方成员。
	c.rec.OwnGuild = ""
	if c.IsMember("守会") {
		t.Error("OwnGuild 为空时 IsMember 应为 false")
	}
}

func TestDefenseGuildOnlyDuringWar(t *testing.T) {
	c, _ := newTestCastle(t)
	if c.IsDefenseGuild("守会") {
		t.Error("非攻城期 IsDefenseGuild 应为 false（Castle.pas:905-910）")
	}
	// 注意 IsAttackGuild / IsMasterGuild 在非攻城期也照常返回 true，
	// 那是原版行为（Castle.pas:785-800、912-917），不在这里断言。
}

func TestInWarArea(t *testing.T) {
	c, _ := newTestCastle(t)
	cases := []struct {
		mapName string
		x, y    int
		want    bool
		why     string
	}{
		{"3", 644, 290, true, "战场地图的回城点"},
		{"3", 700, 290, true, "战场地图半径内"},
		{"3", 900, 290, false, "战场地图但超出半径"},
		{"3", 100, 100, false, "战场地图角落"},
		{"0150", 0, 0, true, "皇宫地图无条件算战区"},
		{"D701", 5, 5, true, "密道地图无条件算战区"},
		{"0153", 1, 1, true, "归属地图 0153"},
		{"0", 644, 290, false, "别的地图"},
	}
	for _, tc := range cases {
		if got := c.InWarArea(tc.mapName, tc.x, tc.y); got != tc.want {
			t.Errorf("InWarArea(%q,%d,%d) = %v, 期望 %v（%s）",
				tc.mapName, tc.x, tc.y, got, tc.want, tc.why)
		}
	}
}

func TestHomePosJitter(t *testing.T) {
	c, _ := newTestCastle(t)
	// rnd 返回 0 → HomeX-4, HomeY-4；返回 8 → HomeX+4, HomeY+4
	// （Castle.pas:921-929 是 Home-4 + Random(9)）。
	x, y := c.HomePos(func(int) int { return 0 })
	if x != 640 || y != 286 {
		t.Errorf("HomePos(下限) = (%d,%d), 期望 (640,286)", x, y)
	}
	x, y = c.HomePos(func(int) int { return 8 })
	if x != 648 || y != 294 {
		t.Errorf("HomePos(上限) = (%d,%d), 期望 (648,294)", x, y)
	}
}

// ---------- 攻陷 ----------

func TestCanGetCastle(t *testing.T) {
	c, g := newTestCastle(t)
	declareForToday(c, "攻会")
	start := at(20, 0)
	c.Run(start, Hooks{})

	// 开战 10 分钟内不许攻陷（给守方反应时间）。
	if c.CanGetCastle(*g, "攻会", start.Add(5*time.Minute), []string{"攻会"}) {
		t.Error("开战 5 分钟时不该能攻陷")
	}
	after := start.Add(11 * time.Minute)

	if !c.CanGetCastle(*g, "攻会", after, []string{"攻会", "攻会"}) {
		t.Error("皇宫只有本会活人时应能攻陷")
	}
	if c.CanGetCastle(*g, "攻会", after, []string{"攻会", "守会"}) {
		t.Error("皇宫还有守会的人，不该能攻陷")
	}
	if c.CanGetCastle(*g, "攻会", after, []string{"攻会", ""}) {
		t.Error("皇宫有无行会的人，不该能攻陷（原版 m_MyGuild <> Guild 把空指针也算成不同）")
	}
	if c.CanGetCastle(*g, "守会", after, []string{"守会"}) {
		t.Error("占领方不能自己攻陷自己")
	}
	if c.CanGetCastle(*g, "路人", after, []string{"路人"}) {
		t.Error("未宣战的行会不能攻陷")
	}
	// 攻陷不重置 underWar（原版由调用方按 PalaceCount 判断）。
	if !c.UnderWar() {
		t.Error("攻陷后仍应处于攻城期")
	}
}

func TestGetCastleSwapsOwner(t *testing.T) {
	c, _ := newTestCastle(t)
	declareForToday(c, "攻会")
	start := at(20, 0)
	c.Run(start, Hooks{})

	now := start.Add(11 * time.Minute)
	old := c.GetCastle("攻会", now)
	if old != "守会" {
		t.Errorf("旧占领行会 = %q, 期望 守会", old)
	}
	if c.OwnGuild() != "攻会" {
		t.Errorf("新占领行会 = %q, 期望 攻会", c.OwnGuild())
	}
	if !c.ChangeDate().Equal(now) {
		t.Errorf("换主时间 = %v, 期望 %v", c.ChangeDate(), now)
	}
	// 换主后守方判定要跟着翻。
	if !c.IsDefenseGuild("攻会") {
		t.Error("新占领方应成为守方")
	}
}

// ---------- 税收 ----------

func TestIncRateGoldCaps(t *testing.T) {
	c, _ := newTestCastle(t)
	cfg := c.Config()

	// 5% 抽成（Castle.pas:1030）。
	if got := c.IncRateGold(1000); got != 50 {
		t.Errorf("1000 金币抽税 = %d, 期望 50", got)
	}
	if got := c.IncRateGold(999); got != 49 { // 999*5/100 = 49.95，整数运算截断
		t.Errorf("999 金币抽税 = %d, 期望 49（整数运算）", got)
	}
	if c.TotalGold() != 99 {
		t.Errorf("金库 = %d, 期望 99", c.TotalGold())
	}

	// 单日上限（Castle.pas:1032-1041）。
	c.rec.TodayIncome = cfg.OneDayGoldMax
	if got := c.IncRateGold(1_000_000); got != 0 {
		t.Errorf("单日已满时抽税应为 0，实际 %d", got)
	}
	c.rec.TodayIncome = cfg.OneDayGoldMax - 100
	if got := c.IncRateGold(1_000_000); got != 100 {
		t.Errorf("距单日上限 100 时抽税应为 100，实际 %d", got)
	}

	// 金库上限（Castle.pas:1043-1046）。
	c2, _ := newTestCastle(t)
	c2.SetTotalGold(c2.Config().GoldMax - 10)
	c2.IncRateGold(1_000_000) // 抽 50000，远超剩余的 10
	if c2.TotalGold() != c2.Config().GoldMax {
		t.Errorf("金库应封顶在 %d，实际 %d", c2.Config().GoldMax, c2.TotalGold())
	}

	// 非正数不抽。
	if got := c.IncRateGold(0); got != 0 {
		t.Errorf("抽税(0) = %d, 期望 0", got)
	}
	if got := c.IncRateGold(-100); got != 0 {
		t.Errorf("抽税(-100) = %d, 期望 0", got)
	}
}

func TestWithdrawAndDeposit(t *testing.T) {
	c, _ := newTestCastle(t)
	c.SetTotalGold(100_000)
	rich := Wallet{Gold: 1000, MaxGold: 200_000}

	cases := []struct {
		name    string
		guild   string
		isChief bool
		gold    int64
		want    int
	}{
		{"非掌门", "守会", false, 1000, GoldNoRight},
		{"非占领方掌门", "攻会", true, 1000, GoldNoRight},
		{"金额为 0", "守会", true, 0, GoldBadArgs},
		{"金额为负", "守会", true, -5, GoldBadArgs},
		{"金库不足", "守会", true, 200_000, GoldNoFund},
		{"正常取款", "守会", true, 1000, GoldOK},
	}
	for _, tc := range cases {
		if got := c.WithDrawalGolds(tc.guild, tc.isChief, tc.gold, rich); got != tc.want {
			t.Errorf("取款 %s: = %d, 期望 %d", tc.name, got, tc.want)
		}
	}
	if c.TotalGold() != 99_000 {
		t.Errorf("取款后金库 = %d, 期望 99000", c.TotalGold())
	}

	// 拿不动：携带上限 10000，wallet 已有 1000，取 9500 就超了
	// （金库里 99000 够，所以先撞的是携带上限）。
	poor2 := Wallet{Gold: 1000, MaxGold: 10_000}
	if got := c.WithDrawalGolds("守会", true, 9500, poor2); got != GoldTooMuch {
		t.Errorf("取 9500（携带上限 10000）= %d, 期望 %d", got, GoldTooMuch)
	}

	// 存金（Castle.pas:1103-1134）。
	poor := Wallet{Gold: 500, MaxGold: 200_000}
	if got := c.ReceiptGolds("守会", true, 600, poor); got != GoldNoFund {
		t.Errorf("存 600（只有 500）= %d, 期望 %d", got, GoldNoFund)
	}
	if got := c.ReceiptGolds("守会", true, 400, poor); got != GoldOK {
		t.Errorf("存 400 = %d, 期望 %d", got, GoldOK)
	}
	if c.TotalGold() != 99_400 {
		t.Errorf("存金后金库 = %d, 期望 99400", c.TotalGold())
	}
	// 存到金库上限（GoldMax=10000000）：只剩 600 的空间。
	c.SetTotalGold(c.Config().GoldMax - 600)
	if got := c.ReceiptGolds("守会", true, 601,
		Wallet{Gold: 601, MaxGold: 10_000_000}); got != GoldTooMuch {
		t.Errorf("存到超上限 = %d, 期望 %d", got, GoldTooMuch)
	}
	if got := c.ReceiptGolds("守会", true, 600,
		Wallet{Gold: 600, MaxGold: 10_000_000}); got != GoldOK {
		t.Errorf("正好存满上限 = %d, 期望 %d", got, GoldOK)
	}
	if c.TotalGold() != c.Config().GoldMax {
		t.Errorf("金库 = %d, 期望封顶在 %d", c.TotalGold(), c.Config().GoldMax)
	}
}

func TestCrossDayIncomeReset(t *testing.T) {
	c, _ := newTestCastle(t)
	c.IncRateGold(100_000) // 当日收 5000
	if c.TodayIncome() != 5000 {
		t.Fatalf("当日税收 = %d, 期望 5000", c.TodayIncome())
	}
	c.Run(at(0, 5), Hooks{})
	if c.TodayIncome() != 0 {
		t.Errorf("跨日后当日税收应清零，实际 %d", c.TodayIncome())
	}
	// 金库是累计的，不受跨日影响。
	if c.TotalGold() != 5000 {
		t.Errorf("金库 = %d, 期望 5000（不该被跨日清零）", c.TotalGold())
	}
}

// TestDirtyTracking 变更标记驱动落库。
func TestDirtyTracking(t *testing.T) {
	c, _ := newTestCastle(t)
	c.MarkClean()
	if c.Dirty() {
		t.Fatal("刚构造的城堡不该是脏的")
	}
	c.IncRateGold(1000)
	if !c.Dirty() {
		t.Error("抽税后应为脏")
	}
	c.MarkClean()
	if c.Dirty() {
		t.Error("MarkClean 后不该是脏的")
	}
	c.GetCastle("攻会", at(20, 0))
	if !c.Dirty() {
		t.Error("换主后应为脏")
	}
}

// TestDefaultRecordSanity 出厂配置要与官方 SabukW.txt 一致。
func TestDefaultRecordSanity(t *testing.T) {
	r := DefaultRecord()
	if r.ConfigDir != "0" || r.Name != "沙巴克" {
		t.Errorf("ConfigDir/Name = %q/%q", r.ConfigDir, r.Name)
	}
	if r.MapName != "3" || r.PalaceMap != "0150" || r.SecretMap != "D701" {
		t.Errorf("地图 = %q/%q/%q, 期望 3/0150/D701", r.MapName, r.PalaceMap, r.SecretMap)
	}
	if r.HomeX != 644 || r.HomeY != 290 {
		t.Errorf("回城点 = (%d,%d), 期望 (644,290)", r.HomeX, r.HomeY)
	}
	if r.WarRangeX != 100 || r.WarRangeY != 100 {
		t.Errorf("攻守半径 = (%d,%d), 期望 (100,100)", r.WarRangeX, r.WarRangeY)
	}
	if r.PalaceDoorX != 631 || r.PalaceDoorY != 274 {
		t.Errorf("皇宫门 = (%d,%d), 期望 (631,274)", r.PalaceDoorX, r.PalaceDoorY)
	}
	if len(r.ExtraMaps) != 6 {
		t.Errorf("归属地图应 6 张，实际 %d", len(r.ExtraMaps))
	}

	// 单位：1 城门 + 3 墙 + 12 弓 + 4 守卫 = 20。
	var door, wall, archer, guard int
	for _, u := range r.Units {
		switch u.Kind {
		case storage.CastleMainDoor:
			door++
			if u.Name != "SabukDoor" || u.X != 672 || u.Y != 330 || u.HP != 10000 {
				t.Errorf("城门 = %+v, 期望 SabukDoor(672,330) HP 10000", u)
			}
		case storage.CastleWall:
			wall++
		case storage.CastleArcher:
			archer++
		case storage.CastleGuard:
			guard++
		}
	}
	if door != 1 || wall != 3 || archer != MaxArcher || guard != MaxGuard {
		t.Errorf("单位数 城门/墙/弓/卫 = %d/%d/%d/%d, 期望 1/3/12/4", door, wall, archer, guard)
	}
}

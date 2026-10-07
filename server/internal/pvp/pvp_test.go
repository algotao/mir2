package pvp

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

func TestPKLevelIsPerHundred(t *testing.T) {
	cases := []struct {
		pt   int32
		want int
		red  bool
	}{
		{0, 0, false}, {99, 0, false},
		{100, 1, false}, {199, 1, false},
		{200, 2, true}, {10000, 100, true},
	}
	for _, c := range cases {
		if got := PKLevel(c.pt); got != c.want {
			t.Errorf("PKLevel(%d) = %d, 期望 %d", c.pt, got, c.want)
		}
		if got := IsRedName(c.pt); got != c.red {
			t.Errorf("IsRedName(%d) = %v, 期望 %v", c.pt, got, c.red)
		}
	}
}

func TestIncDecPKPoint(t *testing.T) {
	// 0 → 100 跨 1 级，要刷颜色。
	pt := int32(0)
	if !IncPKPoint(&pt, 100, 10000) {
		t.Error("0→100 应要求刷新颜色")
	}
	// 100 → 200 跨 2 级（红名），也要刷。
	if !IncPKPoint(&pt, 100, 10000) {
		t.Error("100→200 应要求刷新颜色（进入红名）")
	}
	// 200 → 300 还在 2 级，不刷。
	if IncPKPoint(&pt, 100, 10000) {
		t.Error("2 级内部变动不该要求刷新")
	}
	// 上限 clamp。
	big := int32(9999)
	IncPKPoint(&big, 500, 10000)
	if big != 10000 {
		t.Errorf("应 clamp 到 10000，实际 %d", big)
	}
	// 负数 clamp 到 0。
	neg := int32(-5)
	DecPKPoint(&neg, 100)
	if neg != 0 {
		t.Errorf("衰减后应为 0，实际 %d", neg)
	}
	// 衰减跨边界要刷（2 级 → 1 级）。先把点数落到 200。
	two := int32(200)
	if !DecPKPoint(&two, 100) {
		t.Error("2 级 → 1 级应要求刷新")
	}
	// 同级衰减不刷：450(4级) 减 10 仍是 4 级。
	// 注意"1 级衰减"必然跨到 0 级，原版此时**确实**会刷（ObjBase.pas:18898-18900
	// 的条件是"等级变化且原等级在 1..2"，含 1→0）。
	four := int32(450)
	if DecPKPoint(&four, 10) {
		t.Error("同级衰减不该要求刷新")
	}
	if four != 440 {
		t.Errorf("衰减后点数 = %d, 期望 440", four)
	}
}

func TestSetPKFlag(t *testing.T) {
	now := time.Now()
	victim := player(2, 30, "", 0)

	// 普通情况：置位成功。
	if !SetPKFlag(player(1, 30, "", 0), victim, false, false) {
		t.Error("普通被打应置 PK 标记")
	}
	// 已在标记期内不再置位（原版条件之一）。
	if SetPKFlag(player(1, 30, "", 0), victim, false, true) {
		t.Error("已有标记时不该重复置位")
	}
	// FIGHT 区不置位。
	if SetPKFlag(player(1, 30, "", 0), player(3, 30, "", 0), true, false) {
		t.Error("FIGHT 区不该置 PK 标记")
	}
	// 双方都是红名时不置位。
	if SetPKFlag(player(1, 30, "", 500), player(4, 30, "", 500), false, false) {
		t.Error("双方红名不该置 PK 标记")
	}

	// 到期清除。
	victim.PvpFlagUntil = now.Add(-time.Second)
	if !CheckPKStatus(victim, now) {
		t.Error("到期应清除 PK 标记")
	}
	if CheckPKStatus(victim, now) {
		t.Error("已清除不该再报")
	}
	if !IsGoodKilling(victim) == false {
		t.Error("清除后 IsGoodKilling 应为 false")
	}
}

func TestIsGoodKilling(t *testing.T) {
	if IsGoodKilling(&Actor{}) {
		t.Error("无标记时 IsGoodKilling 应为 false")
	}
	if !IsGoodKilling(&Actor{PvpFlag: true}) {
		t.Error("有标记时应视为正当防卫")
	}
}

// TestIsAttackTarget 覆盖攻击模式矩阵（ObjBase.pas:21385-21487）。
func TestIsAttackTarget(t *testing.T) {
	cfg := DefaultConfig()
	atk := player(1, 30, "A", 0)
	victim := player(2, 30, "B", 0)

	cases := []struct {
		name                string
		mode                AttackMode
		target              *Actor
		sameGuild, sameAlly bool
		want                bool
	}{
		{"和平模式打玩家", HamPeace, victim, false, false, false},
		{"全体模式打玩家", HamAll, victim, false, false, true},
		{"编组模式打非同组", HamGroup, victim, false, false, true},
		{"行会模式打非同会", HamGuild, victim, false, false, true},
		{"行会模式打同会", HamGuild, victim, true, false, false},
		{"行会模式打同盟", HamGuild, victim, false, true, false},
		{"只打红名：目标白名", HamPKAttack, victim, false, false, false},
		{"只打红名：目标红名", HamPKAttack, player(3, 30, "C", 200), false, false, true},
		{"打怪（任意模式）", HamPeace, monster(9), false, false, true},
	}
	for _, c := range cases {
		got := IsAttackTarget(cfg, atk, c.target, c.mode, c.sameGuild, c.sameAlly)
		if got != c.want {
			t.Errorf("%s: IsAttackTarget = %v, 期望 %v", c.name, got, c.want)
		}
	}

	// 打自己不行。
	if IsAttackTarget(cfg, atk, atk, HamAll, false, false) {
		t.Error("不该能打自己")
	}
	// 目标在安全区，即使是怪也不打玩家目标。
	safe := player(4, 30, "D", 0)
	safe.InSafeZone = true
	if IsAttackTarget(cfg, atk, safe, HamAll, false, false) {
		t.Error("安全区内的玩家不该被打")
	}
	// 召唤兽与主人同属一方时不互打。
	pet := &Actor{ID: 5, Race: RaceAnimal, Master: atk}
	if IsAttackTarget(cfg, atk, pet, HamAll, false, false) {
		t.Error("不该能打自己的召唤兽")
	}
}

// nowRef 是测试用的"当前时刻"，让传送保护有确定的时间基准。
var nowRef = time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)

// 早已传送完的演员（LastMoveAt 远早于 nowRef）。
func player(id uint32, level uint32, g string, pk int32) *Actor {
	return &Actor{ID: id, Race: RacePlay, Level: level, GuildName: g,
		PkPoint: pk, LastMoveAt: nowRef.Add(-time.Hour)}
}

func monster(id uint32) *Actor {
	return &Actor{ID: id, Race: RaceAnimal, LastMoveAt: nowRef.Add(-time.Hour)}
}

// TestIsAttackTargetModes 覆盖攻击模式矩阵（ObjBase.pas:21385-21487）。
func TestIsAttackTargetModes(t *testing.T) {
	cfg := DefaultConfig()
	atk := player(1, 30, "A", 0)
	victim := player(2, 30, "B", 0)

	cases := []struct {
		name                string
		mode                AttackMode
		target              *Actor
		sameGuild, sameAlly bool
		want                bool
	}{
		{"和平模式打玩家", HamPeace, victim, false, false, false},
		{"全体模式打玩家", HamAll, victim, false, false, true},
		{"编组打非同组", HamGroup, victim, false, false, true},
		{"行会打非同会", HamGuild, victim, false, false, true},
		{"行会打同会", HamGuild, victim, true, false, false},
		{"行会打同盟", HamGuild, victim, false, true, false},
		{"只打红名-白名目标", HamPKAttack, victim, false, false, false},
		{"只打红名-红名目标", HamPKAttack, player(3, 30, "C", 200), false, false, true},
		{"和平模式打怪", HamPeace, monster(9), false, false, true},
	}
	for _, c := range cases {
		if got := IsAttackTarget(cfg, atk, c.target, c.mode, c.sameGuild, c.sameAlly); got != c.want {
			t.Errorf("%s: = %v, 期望 %v", c.name, got, c.want)
		}
	}

	if IsAttackTarget(cfg, atk, atk, HamAll, false, false) {
		t.Error("不该能打自己")
	}
	safe := player(4, 30, "D", 0)
	safe.InSafeZone = true
	if IsAttackTarget(cfg, atk, safe, HamAll, false, false) {
		t.Error("安全区内的玩家不该被打")
	}
	pet := &Actor{ID: 5, Race: RaceAnimal, Master: atk, LastMoveAt: nowRef.Add(-time.Hour)}
	if IsAttackTarget(cfg, atk, pet, HamAll, false, false) {
		t.Error("不该能打自己的召唤兽")
	}
}

// TestIsProtectTargetLevelRules 覆盖等级保护的两个方向
// （ObjBase.pas:21308-21320，**无条件生效**，不受 boPKLevelProtect 影响）。
func TestIsProtectTargetLevelRules(t *testing.T) {
	cfg := DefaultConfig()
	big := player(1, 30, "A", 0)       // 30 级白名
	small := player(2, 5, "B", 0)      // 5 级白名
	redBig := player(3, 30, "C", 200)  // 30 级红名
	redSmall := player(4, 5, "D", 200) // 5 级红名

	if !IsProtectTarget(cfg, big, small, nowRef) {
		t.Error("30 级白名应能打 5 级白名（大号杀小号允许）")
	}
	// ⚠️ 原版**没有**"低级不能打高级"这条：规则 A/B 都只把红名当条件。
	// 低级白名杀高级白名是允许的（只是容易被反杀）。
	if !IsProtectTarget(cfg, small, big, nowRef) {
		t.Error("5 级白名打 30 级白名应允许（原版无此限制）")
	}
	// 规则A：高等级红名不能杀低级白名。
	if IsProtectTarget(cfg, redBig, small, nowRef) {
		t.Error("高等级红名不该杀低级白名（规则A）")
	}
	if !IsProtectTarget(cfg, redBig, redSmall, nowRef) {
		t.Error("高等级红名应能打低级红名（规则A 只拦白名目标）")
	}
	// 规则B：低级白名不能杀高级红名。
	if IsProtectTarget(cfg, small, redBig, nowRef) {
		t.Error("低级白名不该能杀高级红名（规则B）")
	}
	if !IsProtectTarget(cfg, small, redSmall, nowRef) {
		t.Error("低级白名打低级红名应允许（规则B 要求目标等级 > 10）")
	}
	// 同级白名互杀当然允许。
	if !IsProtectTarget(cfg, big, player(9, 30, "Z", 0), nowRef) {
		t.Error("同级白名互杀应允许")
	}
}

// TestIsProtectTargetSafeAndFree 覆盖安全区与自由 PK 区（ObjBase.pas:21262-21264）。
func TestIsProtectTargetSafeAndFree(t *testing.T) {
	cfg := DefaultConfig()
	big := player(1, 30, "A", 0)
	small := player(2, 5, "B", 0)

	inSafe := player(5, 30, "E", 0)
	inSafe.InSafeZone = true
	if IsProtectTarget(cfg, big, inSafe, nowRef) {
		t.Error("目标在安全区不该能打")
	}
	atkSafe := player(6, 30, "F", 0)
	atkSafe.InSafeZone = true
	if IsProtectTarget(cfg, atkSafe, big, nowRef) {
		t.Error("攻击者在安全区不该能打")
	}

	// 自由 PK 区（攻城战）跳过全部等级保护——只��被攻击者的标记决定。
	free := player(7, 5, "G", 0)
	free.InFreePKArea = true
	if !IsProtectTarget(cfg, small, free, nowRef) {
		t.Error("自由 PK 区内应跳过等级保护")
	}
}

// TestIsProtectTargetMoveProtection 覆盖传送后 3 秒保护
// （ObjBase.pas:21322-21323）。这条容易漏：原版就写在 IsProtectTarget 末尾。
func TestIsProtectTargetMoveProtection(t *testing.T) {
	cfg := DefaultConfig()
	atk := player(1, 30, "A", 0)
	victim := player(2, 30, "B", 0)

	// 攻击者刚传送。
	atk.LastMoveAt = nowRef.Add(-2 * time.Second)
	if IsProtectTarget(cfg, atk, victim, nowRef) {
		t.Error("攻击者刚传送 3 秒内不该能打")
	}
	// 目标刚传送。
	atk.LastMoveAt = nowRef.Add(-time.Hour)
	victim.LastMoveAt = nowRef.Add(-2 * time.Second)
	if IsProtectTarget(cfg, atk, victim, nowRef) {
		t.Error("目标刚传送 3 秒内不该能被打")
	}
	// 超过 3 秒恢复。
	victim.LastMoveAt = nowRef.Add(-4 * time.Second)
	if !IsProtectTarget(cfg, atk, victim, nowRef) {
		t.Error("超过 3 秒应可打")
	}
}

func TestRelations(t *testing.T) {
	a := &storage.Guild{Name: "A", Allies: []string{"B"}}
	b := &storage.Guild{Name: "B"}
	c := &storage.Guild{Name: "C", Allies: []string{"A"}}
	d := &storage.Guild{Name: "D"}
	war := &storage.Guild{Name: "E", Wars: []storage.GuildWar{{Name: "F"}}}
	war2 := &storage.Guild{Name: "F", Wars: []storage.GuildWar{{Name: "E"}}}
	oneWay := &storage.Guild{Name: "G", Wars: []storage.GuildWar{{Name: "H"}}}
	h := &storage.Guild{Name: "H", Wars: []storage.GuildWar{{Name: "I"}}}

	// 同会
	if same, _, _ := Relations(a, a, false); !same {
		t.Error("自己与自己应算同会")
	}
	// 同盟是双向的：A 声明了 B，或 B 声明了 A，都算同盟。
	if _, ally, _ := Relations(a, b, false); !ally {
		t.Error("A→B 的同盟应双向生效")
	}
	if _, ally, _ := Relations(c, a, false); !ally {
		t.Error("A→B 的同盟应双向生效（这里是从 B 侧查起）")
	}
	// 无关系
	if same, ally, warRel := Relations(b, d, false); same || ally || warRel {
		t.Error("B 与 D 应无任何关系")
	}
	// 敌对必须双向（对应 IsWarGuild 的双向判定，Guild.pas:414-427）
	if _, _, warRel := Relations(war, war2, false); !warRel {
		t.Error("双向宣战应算敌对")
	}
	if _, _, warRel := Relations(oneWay, h, false); warRel {
		t.Error("单向记录不该算敌对")
	}
	// 安全区不进入行会战关系（ObjBase.pas:2351）
	if _, _, warRel := Relations(war, war2, true); warRel {
		t.Error("安全区内不该进入行会战关系")
	}
	// 空行会
	if same, _, _ := Relations(nil, b, false); same {
		t.Error("空行会不该有同会关系")
	}
}

func TestInSafeZone(t *testing.T) {
	cfg := DefaultConfig()
	spawns := []SpawnPoint{
		{MapID: "0", X: 289, Y: 618}, // 比奇省出生点
		{MapID: "3", X: 330, Y: 270},
	}

	// ① 地图属性 SAFE
	if !InSafeZone(cfg, &ZoneMap{Name: "0162", Safe: true}, 5, 5, nil) {
		t.Error("SAFE 地图任意位置应安全")
	}
	// ③ 出生点半径内
	if !InSafeZone(cfg, &ZoneMap{Name: "0"}, 289, 618, spawns) {
		t.Error("出生点应安全")
	}
	if !InSafeZone(cfg, &ZoneMap{Name: "0"}, 295, 622, spawns) {
		t.Error("出生点半径 10 内应安全")
	}
	if InSafeZone(cfg, &ZoneMap{Name: "0"}, 320, 618, spawns) {
		t.Error("出生点半径外不该安全")
	}
	// 出生点半径不跨地图
	if InSafeZone(cfg, &ZoneMap{Name: "3"}, 289, 618, spawns) {
		t.Error("别图的位置不该被本图出生点覆盖")
	}
	// ② 红名监狱点（原版拿红名监狱当安全区，照抄 ObjBase.pas:21535-21542）
	if !InSafeZone(cfg, &ZoneMap{Name: "3"}, cfg.RedHomeX, cfg.RedHomeY, nil) {
		t.Error("红名监狱点应安全")
	}
	// nil 地图
	if InSafeZone(cfg, nil, 0, 0, nil) {
		t.Error("nil 地图不该判为安全区")
	}
}

// TestIsProperTargetPeaceNotRescued 守住 `if Result then` 守卫
// （ObjBase.pas:21498）。
//
// ⚠️ 这条曾经是个真 bug：漏了守卫后，IsProtectTarget 会把
// 「和平模式拒绝」的 false **覆盖回 true**，和平模式等于完全失效。
// 而端到端用例最初没能发现它——因为攻击落点旁的怪会先发一条
// SM_STRUCK，被按 Recog 过滤掉，看起来像"和平模式挡住了"。
func TestIsProperTargetPeaceNotRescued(t *testing.T) {
	cfg := DefaultConfig()
	atk := player(1, 30, "A", 0)
	victim := player(2, 30, "B", 0)

	// 和平模式：IsAttackTarget=false，IsProtectTarget=true。
	// 加了守卫则整体 false；没加守卫会被覆盖成 true。
	if IsAttackTarget(cfg, atk, victim, HamPeace, false, false) {
		t.Fatal("前提不成立：和平模式不该通过 IsAttackTarget")
	}
	if !IsProtectTarget(cfg, atk, victim, nowRef) {
		t.Fatal("前提不成立：IsProtectTarget 应为 true")
	}
	if IsProperTarget(cfg, atk, victim, HamPeace, false, false, nowRef) {
		t.Error("和平模式必须被拒绝：IsProtectTarget 不能覆盖 IsAttackTarget 的 false")
	}

	// 对照：全体攻击时两者都 true，整体为 true。
	if !IsProperTarget(cfg, atk, victim, HamAll, false, false, nowRef) {
		t.Error("全体攻击应允许")
	}
	// 对照：全体攻击 + 目标在安全区 → IsProtectTarget 覆盖为 false。
	safe := player(3, 30, "C", 0)
	safe.InSafeZone = true
	if IsProperTarget(cfg, atk, safe, HamAll, false, false, nowRef) {
		t.Error("安全区目标应被 IsProtectTarget 覆盖为不可打")
	}
	// 对照：只打红名模式，目标白名 → IsAttackTarget=false，整体 false。
	if IsProperTarget(cfg, atk, victim, HamPKAttack, false, false, nowRef) {
		t.Error("只打红名模式不该打到白名")
	}
}

func TestSuppressDeathDrop(t *testing.T) {
	// 原版 ObjBase.pas:20983 把整段死亡掉落（含扣幸运的 AddBodyLuck）包在
	//
	//	if (not boFIGHTZone) and (not boFIGHT3Zone) and (not m_boAnimal) then
	//
	// 里。官方 mapinfo 中 FIGHT3 = F001-F010（行会战争地图）+ G003/G005
	//（热血足球场），FIGHT = SD000-SD002（小黑屋）。
	cases := []struct {
		name string
		zm   *ZoneMap
		want bool
	}{
		{"nil（地图没有 mapinfo 记录）", nil, false},
		{"普通图", &ZoneMap{Name: "3"}, false},
		{"SAFE 图与掉落无关", &ZoneMap{Name: "0", Safe: true}, false},
		{"QUIZ 图与掉落无关", &ZoneMap{Name: "G004", Quiz: true}, false},
		{"PK 区 FIGHT", &ZoneMap{Name: "SD000", FightZone: true}, true},
		{"行会战争地图 FIGHT3", &ZoneMap{Name: "F006", Fight3Zone: true}, true},
		{"同时带 FIGHT 与 FIGHT3", &ZoneMap{Name: "X", FightZone: true, Fight3Zone: true}, true},
	}
	for _, c := range cases {
		if got := c.zm.SuppressDeathDrop(); got != c.want {
			t.Errorf("%s: SuppressDeathDrop() = %v，期望 %v", c.name, got, c.want)
		}
	}
}

// TestPKDieReward 守住 PK 死亡奖惩的判定（`TPlayObject.PKDie`，ObjBase.pas:21076-21180）。
//
// 三个点必须钉死：
//
//  1. **等级差保护在最前**：赢家高出输家超过 HumanLevelDiffer ⇒ 四项全 0；
//  2. 两对开关是**嵌套**的（等级一段、经验一段）：只开"输家掉"而没开"赢家涨"时，
//     掉级/掉经验**不会发生**；
//  3. 输家**红名**（PKLevel ≥ 2）时掉级量**翻倍**。
func TestPKDieReward(t *testing.T) {
	base := DefaultConfig()
	auto := base
	auto.PKDieWinLevel, auto.PKDieLostLevel = true, true
	auto.PKDieWinExp, auto.PKDieLostExp = true, true

	// 出厂全关 ⇒ 什么都不发生
	if wl, ll, we, le := PKDieReward(base, 30, 30, 0); wl|ll|we|le != 0 {
		t.Errorf("出厂全关时不该有奖惩，得到 %d/%d/%d/%d", wl, ll, we, le)
	}

	// 四个都开、等级相当：赢家 +1 级/+100000 经验，输家 -1 级/-100000 经验
	wl, ll, we, le := PKDieReward(auto, 30, 30, 0)
	if wl != 1 || ll != 1 || we != 100000 || le != 100000 {
		t.Errorf("四项全开：得到 %d/%d/%d/%d，期望 1/1/100000/100000", wl, ll, we, le)
	}

	// ★ 嵌套：只开"输家掉级" ⇒ 一点效果都没有（原版控制流如此）
	only := base
	only.PKDieLostLevel = true
	if wl, ll, _, _ := PKDieReward(only, 30, 30, 0); wl != 0 || ll != 0 {
		t.Errorf("只开 KilledLostLevel 时不该掉级（它嵌在 WinLevel 里），得到 %d/%d", wl, ll)
	}
	onlyExp := base
	onlyExp.PKDieLostExp = true
	if _, _, we, le := PKDieReward(onlyExp, 30, 30, 0); we != 0 || le != 0 {
		t.Errorf("只开 KilledLostExp 时不该掉经验（它嵌在 WinExp 里），得到 %d/%d", we, le)
	}

	// ★ 红名（PKLevel ≥ 2）⇒ 掉级翻倍
	wl, ll, _, _ = PKDieReward(auto, 30, 30, 2)
	if wl != 1 || ll != 2 {
		t.Errorf("红名输家应掉 2 级，得到 win=%d lost=%d", wl, ll)
	}

	// ★ 等级差保护：赢家高出 11 级（> HumanLevelDiffer=10）⇒ 四项全 0
	if wl, ll, we, le := PKDieReward(auto, 41, 30, 0); wl|ll|we|le != 0 {
		t.Errorf("赢家高出 11 级应受保护（全 0），得到 %d/%d/%d/%d", wl, ll, we, le)
	}
	// 恰好高出 10 级（= 阈值，原版是 `>` 不是 `>=`）⇒ 照常结算
	if wl, _, _, _ := PKDieReward(auto, 40, 30, 0); wl != 1 {
		t.Errorf("恰好高出 10 级应照常结算（阈值是 `>`），得到 %d", wl)
	}
	// 低等级赢家打高等级输家 ⇒ 不受保护
	if wl, _, _, _ := PKDieReward(auto, 10, 40, 0); wl != 1 {
		t.Errorf("低等级赢家不该受保护，得到 %d", wl)
	}
}

package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/world"
)

// testMaster 造一个只够当宠物主人的 Player。
func testMaster(id uint32, name string) *Player {
	return &Player{
		Obj:  &entity.Object{ID: id, Name: name},
		Char: &storage.Character{Name: name},
	}
}

// ---------- 诱惑之光的概率门 ----------

func TestTammingGateRoll(t *testing.T) {
	// Magic.pas:783 `Random(4 - 技能等级) = 0`
	// 技能 0/1/2/3 级 → Random(4/3/2/1)，3 级恒 0 ⇒ 必过。
	for _, c := range []struct{ level, denom int }{{0, 4}, {1, 3}, {2, 2}, {3, 1}} {
		// 取下界 ⇒ Random(n)=0 ⇒ 过
		func() {
			defer withFixedRnd(fixedZero)()
			if !tammingGateRoll(c.level) {
				t.Errorf("技能 %d 级: Random(%d) 取 0 时应通过", c.level, c.denom)
			}
		}()
		if c.denom == 1 {
			continue // Random(1) 恒 0，没有"不过"的情况
		}
		// 取上界 ⇒ 非 0 ⇒ 不过
		func() {
			defer withFixedRnd(fixedMax)()
			if tammingGateRoll(c.level) {
				t.Errorf("技能 %d 级: Random(%d) 取 %d 时不应通过", c.level, c.denom, c.denom-1)
			}
		}()
	}
	// 技能等级 >3 时分母 ≤0，Delphi 的 Random(≤0)=0 ⇒ 恒过（别 panic）
	defer withFixedRnd(fixedZero)()
	for _, lv := range []int{4, 5, 100} {
		if !tammingGateRoll(lv) {
			t.Errorf("技能 %d 级（越界）应恒过门 1", lv)
		}
	}
}

func TestTammingLevelGate(t *testing.T) {
	defer withFixedRnd(fixedZero)()
	// Random(21)=0 ⇒ roll = 主人等级
	// 判定：主人等级 + 技能等级*5 > 怪等级 + tammingTargetLevel(1)
	// 13 级法师(技能 0) 对 13 级怪：13 > 14 不成立 ⇒ 失败
	if tammingLevelGate(13, 13, 0) {
		t.Error("13 级对 13 级：13 > 14 不成立，应失败")
	}
	// 技能 3 级时 +15：13+15=28 > 14 ⇒ 过
	if !tammingLevelGate(13, 13, 3) {
		t.Error("13 级 + 技能 3 级：28 > 14，应通过")
	}
	// 硬门槛：怪等级 > 主人等级+2 ⇒ 直接失败（与随机无关）
	for _, c := range []struct{ pl, mon int }{{10, 13}, {10, 20}, {1, 4}} {
		if tammingLevelGate(c.pl, c.mon, 3) {
			t.Errorf("主人 %d 级 / 怪 %d 级：超过 +2 硬门槛，应失败", c.pl, c.mon)
		}
	}
	// 硬门槛：怪等级 > tammingMaxLevel(50)
	if tammingLevelGate(60, 55, 3) {
		t.Error("怪 55 级超过 tammingMaxLevel=50，应失败")
	}
}

func TestTammingHPRollDoubles(t *testing.T) {
	// Magic.pas:816-818
	//   n14 := MaxHP / tammingHPRate(1000)
	//   if n14 <= 2 then n14 := 2 else Inc(n14, n14)   // **翻倍**
	//   Random(n14) = 0 才成功
	//
	// 关键：翻倍是 `n14 + n14`，不是 `Inc(n14)`。用"记录被请求的分母"来验。
	var seen []int
	defer withFixedRnd(func(n int) int {
		seen = append(seen, n)
		return 0
	})()
	_ = tammingHPRoll(500) // 500/1000 = 0 ⇒ ≤2 ⇒ 2
	if len(seen) != 1 || seen[0] != 2 {
		t.Errorf("MaxHP=500 请求的分母 = %v，期望 [2]", seen)
	}

	seen = nil
	_ = tammingHPRoll(6000) // 6000/1000 = 6 ⇒ >2 ⇒ 翻倍 = 12
	if len(seen) != 1 || seen[0] != 12 {
		t.Errorf("MaxHP=6000 请求的分母 = %v，期望 [12]（6*2，不是 7）", seen)
	}

	seen = nil
	_ = tammingHPRoll(2000) // 2 ⇒ ≤2 ⇒ 保持 2（**不**翻倍）
	if len(seen) != 1 || seen[0] != 2 {
		t.Errorf("MaxHP=2000 请求的分母 = %v，期望 [2]（=2 时不翻倍）", seen)
	}

	// 分母 2 时取上界（=1）⇒ 失败；取 0 ⇒ 成功
	func() {
		defer withFixedRnd(fixedMax)()
		if tammingHPRoll(500) {
			t.Error("分母取上界时不应成功")
		}
	}()
	func() {
		defer withFixedRnd(fixedZero)()
		if !tammingHPRoll(500) {
			t.Error("分母取 0 时应成功")
		}
	}()
}

func TestTammingMutinyMinutes(t *testing.T) {
	// Magic.pas:838-841
	//   RandomRange(0, 主人等级) + 60*(主人等级/10) + (技能等级<<2)*5
	//
	// 原版注释的锚点：13 级 → 1h38m、24 级 → 3h24m、30 级 → 4h（技能 0 级时）
	defer withFixedRnd(fixedZero)()
	for _, c := range []struct {
		pl, ml int
		want   int
	}{
		{13, 0, 60 + 0},   // 13/10=1 ⇒ 60 分钟（技能 0 额外 0）
		{24, 0, 120 + 0},  // 24/10=2 ⇒ 120
		{30, 0, 180 + 0},  // 30/10=3 ⇒ 180
		{13, 3, 60 + 60},  // 技能 3：(3<<2)*5 = 60
		{30, 2, 180 + 40}, // 技能 2：(2<<2)*5 = 40
		{0, 0, 0},         // 0 级：(0<<2)*5 = 0
	} {
		if got := tammingMutinyMinutes(c.pl, c.ml); got != c.want {
			t.Errorf("主人 %d 级 + 技能 %d 级 = %d 分钟, 期望 %d", c.pl, c.ml, got, c.want)
		}
	}
	// RandomRange(0, 等级) 取上界时多出"等级"分钟
	defer withFixedRnd(fixedMax)()
	if got := tammingMutinyMinutes(30, 0); got != 180+30 {
		t.Errorf("取上界时应为 %d 分钟，得到 %d", 180+30, got)
	}
}

func TestTammingSpeedCap(t *testing.T) {
	// Magic.pas:846-851：walkCap = 1500 - 技能*200，hitCap = 2000 - 技能*200
	for _, c := range []struct {
		ml        int
		walk, hit int
	}{
		{0, 1500, 2000}, {1, 1300, 1800}, {2, 1100, 1600}, {3, 900, 1400},
	} {
		w, h := tammingSpeedCap(c.ml)
		if w != c.walk || h != c.hit {
			t.Errorf("技能 %d 级: cap = (%d,%d), 期望 (%d,%d)", c.ml, w, h, c.walk, c.hit)
		}
	}
}

func TestCapSpeedOnlySlowsDown(t *testing.T) {
	// CapSpeed 是"只压慢、不加快"：本来就慢的兽不该被改快。
	fast := entity.NewMonster(1, &data.MonsterInfo{
		Name: "快兽", HP: 10, WalkSpeed: 300, AttackSpeed: 400,
	}, nil, 0, 0)
	fast.CapSpeed(1500, 2000) // 分母比现状大 ⇒ 不动
	if got := fast.MoveInterval(); got != 300*time.Millisecond {
		t.Errorf("慢兽被 CapSpeed 加快了: %v", got)
	}
	slow := entity.NewMonster(2, &data.MonsterInfo{
		Name: "慢兽", HP: 10, WalkSpeed: 5000, AttackSpeed: 6000,
	}, nil, 0, 0)
	slow.CapSpeed(1500, 2000)
	if got := slow.MoveInterval(); got != 1500*time.Millisecond {
		t.Errorf("快兽移动间隔 = %v, 期望 1.5s", got)
	}
	if got := slow.AttackInterval(); got != 2000*time.Millisecond {
		t.Errorf("快兽攻击间隔 = %v, 期望 2s", got)
	}
	// 模板不能被改（否则全服同种怪一起变慢）
	if slow.Info.WalkSpeed != 5000 {
		t.Errorf("CapSpeed 改了共享模板 WalkSpeed = %d，期望仍是 5000", slow.Info.WalkSpeed)
	}
	// 封到 200ms 以下时按 200ms 兜底
	tiny := entity.NewMonster(3, &data.MonsterInfo{Name: "x", HP: 1, WalkSpeed: 5000}, nil, 0, 0)
	tiny.CapSpeed(10, 10)
	if got := tiny.MoveInterval(); got != 200*time.Millisecond {
		t.Errorf("过小的封顶 = %v, 期望兜底 200ms", got)
	}
}

// ---------- 判变 / 陪葬 ----------

func TestSlaveShouldDesert(t *testing.T) {
	now := time.Now()
	m := &entity.Monster{MasterID: 42}
	if slaveShouldDesert(m, now) {
		t.Error("RoyaltyUntil 零值应永不判变")
	}
	m.RoyaltyUntil = now.Add(time.Minute)
	if slaveShouldDesert(m, now) {
		t.Error("还没到期不该判变")
	}
	if !slaveShouldDesert(m, now.Add(2*time.Minute)) {
		t.Error("到期后应判变")
	}
	// 没有主人的野生怪不判变
	wild := &entity.Monster{RoyaltyUntil: now.Add(-time.Hour)}
	if slaveShouldDesert(wild, now) {
		t.Error("野生怪不该判变")
	}
}

// ---------- 跟随点 ----------

func TestSlaveBackPosition(t *testing.T) {
	// ObjBase.pas:22583 GetBackPosition：按主人朝向取**反方向**那一格
	const w, h = 20, 20
	for _, c := range []struct {
		dir   uint8
		x, y  int
		wantX int
		wantY int
	}{
		{entity.DirUp, 10, 10, 10, 11},      // 面朝上 ⇒ 身后在下方
		{entity.DirDown, 10, 10, 10, 9},     // 面朝下 ⇒ 身后在上方
		{entity.DirLeft, 10, 10, 11, 10},    // 面朝左 ⇒ 身后在右
		{entity.DirRight, 10, 10, 9, 10},    // 面朝右 ⇒ 身后在左
		{entity.DirUpLeft, 10, 10, 11, 11},  // 斜向同理
		{entity.DirDownRight, 10, 10, 9, 9}, //
		{entity.DirUpRight, 10, 10, 9, 11},  //
		{entity.DirDownLeft, 10, 10, 11, 9}, //
		{entity.DirUp, 10, 19, 10, 19},      // 贴下边界 ⇒ 不越界
		{entity.DirDown, 10, 0, 10, 0},      // 贴上边界 ⇒ 不越界
		{entity.DirLeft, 19, 10, 19, 10},    // 贴右边界 ⇒ 不越界
		{entity.DirRight, 0, 10, 0, 10},     // 贴左边界 ⇒ 不越界
		{entity.DirUpLeft, 19, 19, 19, 19},  // 角落 ⇒ 原地不动
	} {
		gotX, gotY := slaveBackPosition(entity.NewObject(0, "", nil, c.x, c.y, c.dir, 0), w, h)
		if gotX != c.wantX || gotY != c.wantY {
			t.Errorf("朝向 %d @(%d,%d): 身后 = (%d,%d), 期望 (%d,%d)",
				c.dir, c.x, c.y, gotX, gotY, c.wantX, c.wantY)
		}
	}
}

// ---------- 归属登记 ----------

// testSlaveServer 造一个只够跑 bindSlave/unbindSlave 的 Server。
func testSlaveServer() *Server {
	return &Server{
		world: worldState{
			monsters:   map[uint32]*entity.Monster{},
			players:    map[uint32]*Player{},
			monsterIdx: world.NewSpatialIndex(64),
		},
		// ⚠️ 必须初始化：group.Manager 的方法在 nil 接收者上会 panic，
		// 而 inSameGroup 在 PvP 的热路径上（每个玩家每帧都可能走到）。
		social: socialState{groups: group.New()},
	}
}

func newTestMonster(id uint32, name string, hp uint32) *entity.Monster {
	return &entity.Monster{
		Object: &entity.Object{ID: id, Name: name},
		Info:   &data.MonsterInfo{Name: name, HP: hp},
		HP:     hp, MaxHP: hp, Alive: true,
	}
}

func TestBindUnbindSlave(t *testing.T) {
	s := testSlaveServer()
	master := testMaster(100, "主人甲")
	s.world.players[master.Obj.ID] = master
	now := time.Now()

	spec := slaveSummonSpecs[17]
	m1 := newTestMonster(1000, spec.monName, 100)
	m1.HP = 10                   // 原版：只补一半血
	s.world.monsters[m1.ID] = m1 // slaveNamedCount 靠 s.world.monsters 数存活数
	if !s.bindSlave(m1, master, spec, 0, slaveRoyaltySec*time.Second, now) {
		t.Fatal("bindSlave 第一次应成功")
	}
	if m1.MasterID != master.Obj.ID {
		t.Errorf("MasterID = %d, 期望 %d", m1.MasterID, master.Obj.ID)
	}
	if m1.HP != 55 { // 10 + (100-10)/2 = 55
		t.Errorf("HP = %d, 期望 55（原版只补一半）", m1.HP)
	}
	if !m1.NoTame {
		t.Error("召唤物也应置 NoTame（别人诱不走）")
	}
	if m1.Deserted(now) {
		t.Error("刚绑定不该判变")
	}
	if !m1.Deserted(now.Add(slaveRoyaltySec*time.Second + time.Second)) {
		t.Error("10 天后应判变")
	}
	if len(master.Slaves) != 1 {
		t.Errorf("Slaves = %d, 期望 1", len(master.Slaves))
	}

	// 上限：同种最多 spec.maxCount 只
	m2 := newTestMonster(1001, spec.monName, 100)
	s.world.monsters[m2.ID] = m2
	if s.bindSlave(m2, master, spec, 0, time.Hour, now) {
		t.Error("超过同种上限应失败")
	}

	// 不同种可以共存（各按各的上限数）
	dragon := slaveSummonSpecs[30]
	d1 := newTestMonster(1002, dragon.monName, 100)
	s.world.monsters[d1.ID] = d1
	if !s.bindSlave(d1, master, dragon, 0, time.Hour, now) {
		t.Error("另一种召唤物应能召（上限各算各的）")
	}
	if len(master.Slaves) != 2 {
		t.Errorf("Slaves = %d, 期望 2", len(master.Slaves))
	}

	// 解除归属要从主人列表里摘掉自己
	s.unbindSlave(m1)
	if m1.MasterID != 0 {
		t.Error("unbind 后 MasterID 应清零")
	}
	if !m1.NoTame {
		t.Error("判变不清 NoTame（原版只在诱惑成功时置位）")
	}
	if len(master.Slaves) != 1 || master.Slaves[0] != d1.ID {
		t.Errorf("Slaves = %v, 期望只剩 %d", master.Slaves, d1.ID)
	}
	if !m1.RoyaltyUntil.IsZero() {
		t.Error("unbind 应清掉忠诚度计时器")
	}
}

func TestReleaseSlaves(t *testing.T) {
	// 主人下线 ⇒ 宠物全部回收。不回收它们会变成孤儿：
	// 继续被 AI 驱动、继续主动攻击路人、还永久占着数量上限。
	s := testSlaveServer()
	master := testMaster(100, "主人甲")
	s.world.players[master.Obj.ID] = master
	now := time.Now()
	for i, spec := range slaveSummonSpecs {
		m := newTestMonster(uint32(2000+i), spec.monName, 50)
		s.world.monsters[m.ID] = m
		s.bindSlave(m, master, spec, 0, time.Hour, now)
	}
	if len(s.world.monsters) != 2 {
		t.Fatalf("准备 %d 只兽，实际 %d", 2, len(s.world.monsters))
	}
	gone := s.releaseSlaves(master)
	if len(gone) != 2 {
		t.Errorf("回收 %d 只，期望 2", len(gone))
	}
	if len(s.world.monsters) != 0 {
		t.Errorf("世界里还剩 %d 只兽（应清空）", len(s.world.monsters))
	}
	if len(master.Slaves) != 0 {
		t.Errorf("Slaves = %d, 期望清空", len(master.Slaves))
	}
	for _, m := range gone {
		if m.MasterID != 0 {
			t.Errorf("兽 %d 的 MasterID 应清零", m.ID)
		}
	}
}

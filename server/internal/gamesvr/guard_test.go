package gamesvr

import (
	"context"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/storage"
)

// fakeGuardGuildStore 是最简的内存 GuildStore（只为 guard 单测服务）。
type fakeGuardGuildStore struct{ m map[string]*storage.Guild }

func (f *fakeGuardGuildStore) Create(_ context.Context, g *storage.Guild) error {
	f.m[g.Name] = g
	return nil
}

func (f *fakeGuardGuildStore) GetByName(_ context.Context, name string) (*storage.Guild, error) {
	return f.m[name], nil
}

func (f *fakeGuardGuildStore) List(_ context.Context) ([]*storage.Guild, error) {
	out := make([]*storage.Guild, 0, len(f.m))
	for _, g := range f.m {
		out = append(out, g)
	}
	return out, nil
}

func (f *fakeGuardGuildStore) Save(_ context.Context, g *storage.Guild) error {
	f.m[g.Name] = g
	return nil
}

func (f *fakeGuardGuildStore) Delete(_ context.Context, name string) error {
	delete(f.m, name)
	return nil
}

// guardTestServer 造一个带行会管理器的服务器（其余复用既有测试助手）。
func guardTestServer() (*Server, *guild.Manager) {
	gm := guild.NewManager(&fakeGuardGuildStore{m: map[string]*storage.Guild{}})
	s := testSlaveServer()
	s.social.guilds = gm
	return s, gm
}

// makeGuild 建一个行会并把人加进去（掌门 = chief）。
func makeGuild(t *testing.T, gm *guild.Manager, name, chief string, others ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := gm.Create(ctx, name, chief); err != nil {
		t.Fatalf("建会 %s 失败: %v", name, err)
	}
	for _, o := range others {
		if err := gm.AddMember(ctx, name, o); err != nil {
			t.Fatalf("加人 %s 失败: %v", o, err)
		}
	}
}

// makeCastle 造一座城堡；atWar 为真时推状态机进入攻城期。
func makeCastle(t *testing.T, own, attacker string, atWar bool) *castle.Castle {
	t.Helper()
	now := time.Now()
	cfg := castle.Config{StartWarHour: now.Hour()}
	cs := castle.New(cfg, storage.Castle{Name: "沙巴克", MapName: "3", OwnGuild: own})
	if atWar {
		if attacker == "" {
			t.Fatal("要开战必须给一个攻城方行会")
		}
		if !cs.AddAttacker(attacker, now) {
			t.Fatal("登记攻城方失败")
		}
		cs.Run(now, castle.Hooks{})
		if !cs.UnderWar() {
			t.Fatal("状态机应已进入攻城期（StartWarHour = 当前小时）")
		}
	}
	return cs
}

// TestGuardProperTargetNoWar 守住非攻城期的判定（ObjMon2.pas:828-874 的 ①②④⑤）。
func TestGuardProperTargetNoWar(t *testing.T) {
	s, gm := guardTestServer()
	makeGuild(t, gm, "守方会", "会长甲")
	makeGuild(t, gm, "盟友会", "盟主乙")
	makeGuild(t, gm, "攻方会", "攻方丙")

	// 守方与盟友结盟（双向写入照 guild.Manager 的做法）
	cs := makeCastle(t, "守方会", "", false)
	guard := newTestMonster(9001, "守卫", 500)
	guard.CastleKind = storage.CastleGuard

	now := time.Now()

	// ①+②+③+④ 都不成立 ⇒ 无行会路人：不打
	passerby := newTestPlayer(7001, "路人", 0)
	if s.guardProperTarget(cs, guard, passerby, now) {
		t.Error("非攻城期、没打过城堡单位的路人，守卫不该打")
	}

	// ② 打过城堡单位（2 分钟窗口内）⇒ 打
	passerby.Obj.SetCastleAggroUntil(now.Add(guardAggroWindow))
	if !s.guardProperTarget(cs, guard, passerby, now) {
		t.Error("打过城堡单位（窗口内）的人，守卫该打")
	}

	// ② 窗口过期 ⇒ 不打，并且**标记要被清掉**
	passerby.Obj.SetCastleAggroUntil(now.Add(-time.Second))
	if s.guardProperTarget(cs, guard, passerby, now) {
		t.Error("仇恨窗口过期后不该再打")
	}
	if !passerby.Obj.CastleAggroUntil().IsZero() {
		t.Error("窗口过期应清掉标记（原版 bo2B0 := False），否则会长期残留")
	}

	// ① 最后打我的那个人 ⇒ 打（哪怕没有窗口标记）
	hiter := newTestPlayer(7002, "先动手的人", 0)
	guard.LastHiterID = hiter.Obj.ID
	if !s.guardProperTarget(cs, guard, hiter, now) {
		t.Error("最后打过守卫的人，守卫该还击")
	}
	guard.LastHiterID = 0

	// ④ 守方行会成员、没先动手 ⇒ 不打（非攻城期）
	defender := newTestPlayer(7003, "会长甲", 0)
	if s.guardProperTarget(cs, guard, defender, now) {
		t.Error("守方行会成员没先动手，守卫不该打（非攻城期）")
	}
	// 但他是"最后打我的人" ⇒ 打（④ 里的 `if m_LastHiter <> BaseObject` 例外）
	guard.LastHiterID = defender.Obj.ID
	if !s.guardProperTarget(cs, guard, defender, now) {
		t.Error("守方行会成员若先动过手，守卫该还击")
	}
	guard.LastHiterID = 0

	// ⑤ GM（管理员模式）⇒ 不打
	gmPlayer := newTestPlayer(7004, "GM", 0)
	gmPlayer.permission = 10
	gmPlayer.Obj.SetCastleAggroUntil(now.Add(guardAggroWindow)) // 即使打过也不打
	if s.guardProperTarget(cs, guard, gmPlayer, now) {
		t.Error("管理员模式下不该被守卫攻击（原版 m_boAdminMode）")
	}

	// ⑤ 自己（守门人不打自己）—— 用同一 ID 模拟
	self := newTestPlayer(guard.ID, "守卫自己", 0)
	if s.guardProperTarget(cs, guard, self, now) {
		t.Error("守卫不该把自己当目标")
	}
}

// TestGuardProperTargetAtWar 守住攻城期最容易被"优化"错的那条：
//
//	③ 攻城期 ⇒ Result := True
//	④ 守方行会/盟友 ⇒ 又把它**覆盖**成 False（除非他先动过手）
//
// 所以攻城期里守方行会的人走城门下**照样安全** —— 这是原版刻意的行为。
func TestGuardProperTargetAtWar(t *testing.T) {
	s, gm := guardTestServer()
	makeGuild(t, gm, "守方会", "会长甲")
	makeGuild(t, gm, "盟友会", "盟主乙")
	makeGuild(t, gm, "攻方会", "攻方丙")

	// 盟友会与守方会结盟（Manager.AddAlly 写双向）
	if err := gm.AddAlly(context.Background(), "守方会", "盟友会"); err != nil {
		t.Fatalf("结盟失败: %v", err)
	}

	cs := makeCastle(t, "守方会", "攻方会", true)
	guard := newTestMonster(9101, "守卫", 500)
	guard.CastleKind = storage.CastleGuard
	now := time.Now()

	// 攻方 ⇒ 打（③）
	attacker := newTestPlayer(7101, "攻方丙", 0)
	if !s.guardProperTarget(cs, guard, attacker, now) {
		t.Error("攻城期：攻城方该打")
	}
	// 路人也打（③：攻城期人人可打）
	if !s.guardProperTarget(cs, guard, newTestPlayer(7102, "路人", 0), now) {
		t.Error("攻城期：路人也是合法目标（原版 m_boUnderWar ⇒ True）")
	}
	// ④ 守方行会成员没先动手 ⇒ **不打**（覆盖 ③）
	if s.guardProperTarget(cs, guard, newTestPlayer(7103, "会长甲", 0), now) {
		t.Error("攻城期里守方行会成员没先动手，守卫不该打（④ 覆盖 ③）")
	}
	// ④ 盟友会成员没先动手 ⇒ 不打
	if s.guardProperTarget(cs, guard, newTestPlayer(7104, "盟主乙", 0), now) {
		t.Error("攻城期里盟会成员没先动手，守卫不该打")
	}
	// ④+① 守方成员先动过手 ⇒ 打
	defHiter := newTestPlayer(7105, "会长甲", 0)
	guard.LastHiterID = defHiter.Obj.ID
	if !s.guardProperTarget(cs, guard, defHiter, now) {
		t.Error("守方行会成员先动过手，守卫该还击")
	}
}

// TestMarkHiter 守住 `TBaseObject.SetLastHiter` / `TGuardUnit.Struck`（`ObjMon2.pas:817-826`）：
//
//   - **所有**怪都记 `LastHiterID`（"谁打的我"）—— 守卫的"反击攻击者 / 清掉打过我的怪"
//     两条判据都读它（`docs/g.md`）；⚠️ 早先只有城堡单位记，普通怪恒为 0；
//   - 城堡单位**另外**进 2 分钟仇恨窗口；打普通怪不进（这一半是原来就对的）。
func TestMarkHiter(t *testing.T) {
	s, _ := guardTestServer()
	now := time.Now()

	guard := newTestMonster(9201, "守卫", 500)
	guard.CastleKind = storage.CastleGuard
	attacker := newTestPlayer(7201, "打门的人", 0)

	s.markHiter(attacker.Obj, guard, now)
	if guard.LastHiterID != attacker.Obj.ID {
		t.Errorf("守卫应记住最后打他的人，得到 %d", guard.LastHiterID)
	}
	if got := attacker.Obj.CastleAggroUntil(); !got.Equal(now.Add(guardAggroWindow)) {
		t.Errorf("仇恨窗口到期时刻 = %v，期望 %v（2 分钟）", got, now.Add(guardAggroWindow))
	}

	// 打普通怪：**记 ID**（守卫要能反击），但**不进**城堡仇恨窗口
	plain := newTestMonster(9202, "鸡", 5)
	other := newTestPlayer(7202, "打鸡的人", 0)
	s.markHiter(other.Obj, plain, now)
	if plain.LastHiterID != other.Obj.ID {
		t.Errorf("普通怪也该记下\"谁打的我\"（否则大刀/弓箭守卫没法反击）：得到 %d，期望 %d",
			plain.LastHiterID, other.Obj.ID)
	}
	if !other.Obj.CastleAggroUntil().IsZero() {
		t.Error("打普通怪不该进城堡仇恨窗口")
	}
}

// TestIsCastleGuardKind 守住"城门/城墙不参与 AI"这条分流依据。
func TestIsCastleGuardKind(t *testing.T) {
	for k, want := range map[storage.CastleUnitKind]bool{
		storage.CastleGuard:    true,
		storage.CastleArcher:   true,
		storage.CastleMainDoor: false,
		storage.CastleWall:     false,
	} {
		if got := isCastleGuardKind(k); got != want {
			t.Errorf("isCastleGuardKind(%q) = %v，期望 %v", k, got, want)
		}
	}
}

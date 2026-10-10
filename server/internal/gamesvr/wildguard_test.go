package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/storage"
)

// ---------- 野生守卫的"该打谁"（口径：docs/g.md + 原版 ObjGuard.pas / ObjMon2.pas）----------
//
// 四类目标一次钉住：白名玩家不打、红名打、怪打、召唤宝宝看守卫类型；再加两条实打实的行为
// （原地不挪步 + 视野内就开打）与一条"大刀无敌"。

// newGuard 造一只野生守卫（**没有城堡** ⇒ 走野生那条判据），贴在玩家旁边 (dx,dy)。
func newGuard(t *testing.T, s *Server, p *Player, id uint32, race uint16, dx, dy int) *entity.Monster {
	t.Helper()
	g := newTestMonster(id, "守卫", 9999)
	g.Info.Race = race
	// ⚠️ `newTestMonster` 只给 HP，不给命中/攻击力 ⇒ 不补的话"该打掉血"那条会因为
	// **永远打空**（命中 0 < Random(敏捷)）而白过。200/200 = 数据里大刀的固定伤害。
	g.Info.Hit, g.Info.DC, g.Info.DCMax = 200, 200, 200
	g.SetPlace(p.Obj.MapRef(), p.Obj.PosX()+dx, p.Obj.PosY()+dy, entity.DirDown)
	g.ViewRange = 12 // 与 `TArcherGuard` 一致（大刀是 7，见 ObjGuard.pas:71）
	s.world.monsters[g.ID] = g
	s.world.monsterIdx.Add(g)
	return g
}

// addMonster 在玩家旁边放一只普通怪（默认 race 83 = 稻草人那种"怪物"）。
func addMonster(t *testing.T, s *Server, p *Player, id uint32, race uint16, dx, dy int) *entity.Monster {
	t.Helper()
	m := newTestMonster(id, "稻草人", 100)
	m.Info.Race = race
	m.SetPlace(p.Obj.MapRef(), p.Obj.PosX()+dx, p.Obj.PosY()+dy, entity.DirLeft)
	s.world.monsters[m.ID] = m
	s.world.monsterIdx.Add(m)
	return m
}

// 大刀卫士（race 11）**一切怪都打**（原版 `ObjGuard.pas:100` 的 `race >= RC_MONSTER`），
// 但**不打**动物（鸡/鹿）：原版的判据就是 `>= RC_MONSTER(80)`。
func TestBladeTargetsMonstersButNotAnimals(t *testing.T) {
	for _, tc := range []struct {
		name string
		race uint16
		want bool
	}{
		{"稻草人（怪物 83）", 83, true},
		{"鸡（动物 51）", entity.RcAnimal + 1, false},
		{"和平 NPC（15）", entity.RcPeaceNpc, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := butchTestServer(t)
			s.cfg.aggro = true
			g := newGuard(t, s, p, 5001, entity.RcGuard, 0, 1)
			mon := addMonster(t, s, p, 6001, tc.race, 2, 0)

			s.tickMonsters(time.Now())
			if got := g.TargetID == mon.ID; got != tc.want {
				t.Errorf("大刀锁定 %q = %v，期望 %v", tc.name, got, tc.want)
			}
		})
	}
}

// 大刀打召唤宝宝（`docs/g.md`：主动杀骷髅、神兽、法师宝宝 = 当年在大刀旁边练宝宝的原理）；
// **弓箭守卫不主动打宝宝**（同一份说明的第 4 条，两者最大的区别）。
func TestBladeHitsPetsArcherDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		race uint16
		want bool
	}{
		{"大刀（11）打宝宝", entity.RcGuard, true},
		{"弓箭守卫（112）不打宝宝", entity.RcArcherGuard, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := butchTestServer(t)
			s.cfg.aggro = true
			g := newGuard(t, s, p, 5002, tc.race, 0, 1)
			pet := addMonster(t, s, p, 6003, 83, 2, 0)
			pet.MasterID = p.Obj.ID // 有主人 = 召唤宝宝（骷髅/神兽/法师宝宝）

			s.tickMonsters(time.Now())
			if got := g.TargetID == pet.ID; got != tc.want {
				t.Errorf("锁定宝宝 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 宝宝**打过**弓箭守卫 ⇒ 就进判据了（`docs/g.md` 的"攻击者"那一档；引擎的 `m_LastHiter`）。
func TestArcherRetaliatesAgainstPetThatHitIt(t *testing.T) {
	s, p := butchTestServer(t)
	s.cfg.aggro = true
	g := newGuard(t, s, p, 5006, entity.RcArcherGuard, 0, 1)
	pet := addMonster(t, s, p, 6005, 83, 2, 0)
	pet.MasterID = p.Obj.ID

	now := time.Now()
	s.tickMonsters(now)
	if g.TargetID != 0 {
		t.Fatalf("没惹过它的宝宝不该被锁：TargetID=%d", g.TargetID)
	}
	s.markHiter(pet.Object, g, now) // 宝宝打了守卫一下
	s.tickMonsters(now.Add(2 * time.Second))
	if g.TargetID != pet.ID {
		t.Errorf("打过守卫的宝宝该被反击：TargetID=%d，期望 %d", g.TargetID, pet.ID)
	}
}

// 玩家主动攻击守卫 ⇒ 守卫立刻反击（`docs/g.md` 第 4 条）。
func TestGuardRetaliatesAgainstAttacker(t *testing.T) {
	s, p := butchTestServer(t)
	s.cfg.aggro = true
	p.Char.Data.PkPoint = 0 // 白名：平时守卫完全无视
	// 血包：守卫反击时会真打（200/刀），血少了会被打死并移出 players，目标也就跟着丢了
	p.Char.Data.Abil.Hp, p.Char.Data.Abil.MaxHp = 9999, 9999
	g := newGuard(t, s, p, 5003, entity.RcArcherGuard, 0, 1)

	now := time.Now()
	s.tickMonsters(now)
	if g.TargetID != 0 {
		t.Fatalf("白名玩家路过不该被锁：TargetID=%d", g.TargetID)
	}
	s.markHiter(p.Obj, g, now)
	s.tickMonsters(now.Add(time.Second))
	if g.TargetID != p.Obj.ID {
		t.Errorf("打了守卫之后该被反击：TargetID=%d，期望 %d", g.TargetID, p.Obj.ID)
	}
}

// 优先级：**红名 > 怪物**（`docs/g.md` 的统一规则；引擎没有分档，是"遍历取第一个命中"）。
func TestGuardPrefersRedNameOverMonster(t *testing.T) {
	s, p := butchTestServer(t)
	s.cfg.aggro = true
	p.Char.Data.PkPoint = 200 // 红名（pvp.RedNameLevel = 2）
	// 血包：红名会被守卫真打（200/刀）；测试玩家没有连接，被打死后 revive 会空指针
	p.Char.Data.Abil.Hp, p.Char.Data.Abil.MaxHp = 9999, 9999
	g := newGuard(t, s, p, 5004, entity.RcGuard, 0, 1)
	// 怪比红名**更近**：若按"最近"而不是分档，就会锁怪
	addMonster(t, s, p, 6004, 83, 1, 1)

	s.tickMonsters(time.Now())
	if g.TargetID != p.Obj.ID {
		t.Errorf("红名该优先于怪（哪怕怪更近）：TargetID=%d，期望红名 %d", g.TargetID, p.Obj.ID)
	}
}

// 守卫**原地不挪步**，而且**视野内就能打**（远程）：目标在 5 格外，几拍之后守卫没动、
// 玩家在掉血（大刀是固定 200 —— 数据里 `卫士` 的 DC=200）。
func TestGuardStaysPutAndHitsFromRange(t *testing.T) {
	s, p := butchTestServer(t)
	s.cfg.aggro = true
	p.Char.Data.PkPoint = 200
	p.Char.Data.Abil.Hp, p.Char.Data.Abil.MaxHp = 9999, 9999 // 血包（连挨几刀也不死）
	gx, gy := p.Obj.PosX()+5, p.Obj.PosY()
	g := newGuard(t, s, p, 5005, entity.RcGuard, 5, 0)

	hp0 := p.Char.Data.Abil.Hp
	now := time.Now()
	for i := 0; i < 4; i++ {
		s.tickMonsters(now.Add(time.Duration(i) * 3 * time.Second)) // 每次跨过攻击间隔
	}
	if g.PosX() != gx || g.PosY() != gy {
		t.Errorf("守卫不该挪步：从 (%d,%d) 到了 (%d,%d)", gx, gy, g.PosX(), g.PosY())
	}
	if g.TargetID != p.Obj.ID {
		t.Fatalf("视野内的红名该被锁：TargetID=%d", g.TargetID)
	}
	if got := p.Char.Data.Abil.Hp; got >= hp0 {
		t.Errorf("玩家该被打掉血（大刀固定 200）：HP %d → %d", hp0, got)
	}
}

// 大刀卫士**无敌**（`docs/g.md`：玩家无法击杀）；弓箭守卫打得动；城堡单位打得动（攻城要用）。
func TestBladeIsInvincible(t *testing.T) {
	blade := newTestMonster(7001, "大刀卫士", 9999)
	blade.Info.Race = entity.RcGuard
	if _, hp, died := blade.Hurt(99999); hp != 9999 || died {
		t.Errorf("大刀该免疫：HP=%d died=%v", hp, died)
	}

	archer := newTestMonster(7002, "弓箭守卫", 2000)
	archer.Info.Race = entity.RcArcherGuard
	if _, hp, _ := archer.Hurt(500); hp != 1500 {
		t.Errorf("弓箭守卫该照常掉血：HP=%d，期望 1500", hp)
	}

	door := newTestMonster(7003, "城门", 5000)
	door.Info.Race = entity.RcGuard // 就算数据给它的是非战斗种族，城堡单位也不受这条免疫
	door.CastleKind = storage.CastleMainDoor
	if _, hp, _ := door.Hurt(1000); hp != 4000 {
		t.Errorf("城堡单位不该被这条免疫挡住：HP=%d，期望 4000", hp)
	}
}

// TestGuardAttackUsesWalkSpeed 弓箭守卫的出手间隔 = **`WalkSpeed`（500ms）**，
// 不是数据里的 `ATTACK_SPD`（1200ms）。
//
// ⚠️ 这是 2026-10-10 用户报的"守卫只能打掉一点点"的**主因**：原版
// `TArcherGuard.Run` 判的是 `m_nWalkSpeed`（`LocalDB.pas:1351` = `WALK_SPD` = 500）。
// 用 1200ms ⇒ 只有原版 40% 的出手频率。（单箭伤害 20~35 是官方数据，没算错。）
func TestGuardAttackUsesWalkSpeed(t *testing.T) {
	archer := newTestMonster(7004, "弓箭守卫", 2000)
	archer.Info.Race = entity.RcArcherGuard
	archer.Info.WalkSpeed = 500
	archer.Info.AttackSpeed = 1200
	if got := archer.GuardAttackInterval(); got != 500*time.Millisecond {
		t.Errorf("弓箭守卫的出手间隔 = %v，期望 500ms（WALK_SPD）", got)
	}
	// 大刀走 `m_nNextHitTime`（= ATTACK_SPD）⇒ 就是通用那个间隔
	blade := newTestMonster(7005, "大刀卫士", 9999)
	blade.Info.Race = entity.RcGuard
	blade.Info.WalkSpeed = 500
	if got := blade.GuardAttackInterval(); got != blade.AttackInterval() {
		t.Errorf("大刀的出手间隔 = %v，期望通用间隔 %v", got, blade.AttackInterval())
	}
	// 普通怪不受影响（这条函数只给守卫用，但别悄悄改了别人）
	mon := newTestMonster(7006, "多钩猫", 30)
	mon.Info.Race = entity.RcMonster
	if got := mon.GuardAttackInterval(); got != mon.AttackInterval() {
		t.Errorf("普通怪的间隔不该被守卫规则改：%v", got)
	}
}

// TestGuardDamageRules 守卫打怪的两条伤害规则：大刀**固定/无视防御**、弓箭**必中**。
//
// 口径：`docs/use.md`（大刀"一刀 200 固定伤害，无视防御"；弓箭"远程箭矢"）
// + 原版 `TArcherGuard.sub_4A6B30`（`ObjMon2.pas:904-921`，没有命中判定）。
func TestGuardDamageRules(t *testing.T) {
	s, p := butchTestServer(t)
	now := time.Now()

	// ① 大刀：目标 AC 高到"按 AC 减免就该打不动"，但固定伤害 ⇒ 必须照掉 200
	blade := newGuard(t, s, p, 7007, entity.RcGuard, 1, 0)
	blade.Info.DC, blade.Info.DCMax = 200, 200
	victim := addMonster(t, s, p, 7008, entity.RcMonster, 2, 0)
	victim.Info.AC = 150 // 减免后只剩 50 —— 但大刀无视防御
	victim.MaxHP, victim.HP = 1000, 1000

	s.mu.Lock()
	h := s.guardAttackMonster(blade, victim, now)
	s.mu.Unlock()
	if h.dmg != 200 {
		t.Errorf("大刀打怪该是固定 200（无视 AC=150），实得 %d", h.dmg)
	}

	// ② 弓箭守卫：命中判定被跳过 ⇒ 每箭都落在怪身上（怪的敏捷再高也不打空）
	archer := newGuard(t, s, p, 7009, entity.RcArcherGuard, 1, 0)
	archer.Info.Hit, archer.Info.DC, archer.Info.DCMax = 15, 20, 35
	// 敏捷拉满（`MonsterSpeedPoint` 的分母）⇒ 换宠物来打的话几乎全空
	dodgy := addMonster(t, s, p, 7010, entity.RcMonster, 2, 0)
	dodgy.Info.AC, dodgy.Info.Speed = 0, 200
	dodgy.MaxHP, dodgy.HP = 100000, 100000

	s.mu.Lock()
	for i := 0; i < 30; i++ {
		h := s.guardAttackMonster(archer, dodgy, now.Add(time.Duration(i)*time.Second))
		if h.dmg == 0 {
			s.mu.Unlock()
			t.Fatalf("弓箭守卫第 %d 箭打空了 —— 原版它**没有命中判定**（箭无虚发）", i+1)
		}
		if h.dmg < 20 || h.dmg > 35 {
			s.mu.Unlock()
			t.Fatalf("弓箭伤害 %d 不在官方 DC 20~35 之内", h.dmg)
		}
	}
	s.mu.Unlock()
}

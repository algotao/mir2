package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// TestSlaveShouldAttackMonster 守住宠物选怪的每一条判据
// （`TBaseObject.IsAttackTarget` 的怪分支，ObjBase.pas:21332-21375）。
//
// ⚠️ 这是 **§2.4 里记的缺口**：原来只有"主人正在打的那只"被 e2e 覆盖，
// 其余三条（怪正在打主人 / 主人最后挨的那下 / 怪正在打宠物自己）只有代码与注释。
//
// 关键语义：宠物**不会自己找怪打**，它只在这几种"与主人有关"的情况下出手。
func TestSlaveShouldAttackMonster(t *testing.T) {
	mp := world.Generate("pettest", 20, 20, false)

	newMaster := func() *Player {
		p := newTestPlayer(500, "主人", 0)
		p.Obj.SetMapRef(mp)
		return p
	}
	newPet := func(id uint32, master *Player) *entity.Monster {
		m := newTestMonster(id, "变异骷髅", 100)
		m.SetMapRef(mp)
		m.MasterID = master.Obj.ID
		return m
	}
	newTarget := func(id uint32) *entity.Monster {
		m := newTestMonster(id, "半兽人", 50)
		m.SetMapRef(mp)
		return m
	}

	// ① 基础：主人没打它、它也没打主人 ⇒ 不打（宠物不自己找怪）
	master := newMaster()
	pet := newPet(9001, master)
	if slaveShouldAttackMonster(pet, newTarget(9101), master) {
		t.Error("与主人无关的怪，宠物不该主动打（宠物不自己找目标）")
	}

	// ② 主人**正在打**它（近战记录 / 技能锁定）⇒ 打
	tgt := newTarget(9102)
	master.combatTargetID = tgt.ID
	if !slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("主人正在打的怪，宠物该跟打")
	}
	master.combatTargetID = 0
	master.spellTargetID = tgt.ID
	if !slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("主人技能锁定的怪，宠物该跟打")
	}
	master.spellTargetID = 0

	// ③ 它正在打**主人** ⇒ 打
	tgt.TargetID = master.Obj.ID
	if !slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("正在攻击主人的怪，宠物该还击")
	}
	tgt.TargetID = 0

	// ④ 它正在打**宠物自己** ⇒ 打
	tgt.TargetID = pet.ID
	if !slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("正在攻击宠物自己的怪，宠物该还击")
	}
	tgt.TargetID = 0

	// ⑤ 主人最后挨的那一下是它（替主人报仇）⇒ 打
	master.lastHitBy = tgt.ID
	if !slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("主人刚被它打过，宠物该替主人还击")
	}
	master.lastHitBy = 0

	// ⑥ 主人叫宠物休息 / 主人隐身 ⇒ 一律不打（哪怕主人正在打它）
	master.combatTargetID = tgt.ID
	master.slaveRelax = true
	if slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("主人叫休息时宠物不该出手")
	}
	master.slaveRelax = false
	// 直接写 buff 表：addBuff 是 *Server 方法（还会广播状态包），这里不需要
	master.buffs = map[entity.BuffType]buffState{entity.BuffInvisible: {Until: time.Now().Add(time.Minute)}}
	if slaveShouldAttackMonster(pet, tgt, master) {
		t.Error("主人隐身时宠物该停手")
	}
	delete(master.buffs, entity.BuffInvisible)
	master.combatTargetID = 0

	// ⑦ 不打同一主人的另一只宠物、不打被定身的怪、不打 NPC / 自己 / 死怪
	sibling := newTarget(9103)
	sibling.MasterID = master.Obj.ID
	if slaveShouldAttackMonster(pet, sibling, master) {
		t.Error("同一主人的另一只宠物不该被打")
	}
	seized := newTarget(9104)
	seized.SeizedUntil = time.Now().Add(time.Minute)
	if slaveShouldAttackMonster(pet, seized, master) {
		t.Error("被定身的怪不该被打")
	}
	npc := newTarget(9105)
	npc.IsNPC = true
	if slaveShouldAttackMonster(pet, npc, master) {
		t.Error("NPC 不该被打")
	}
	dead := newTarget(9106)
	dead.Alive = false
	if slaveShouldAttackMonster(pet, dead, master) {
		t.Error("死怪不该被打")
	}
	if slaveShouldAttackMonster(pet, pet, master) {
		t.Error("宠物不该把自己当目标")
	}

	// ⑧ 不同地图 ⇒ 不打
	other := world.Generate("pettest2", 20, 20, false)
	far := newTarget(9107)
	far.SetMapRef(other)
	if slaveShouldAttackMonster(pet, far, master) {
		t.Error("不在同一张地图的怪不该被打")
	}

	// ⑨ nil 安全（调用方持锁，崩了整个服务端就没了）
	if slaveShouldAttackMonster(nil, newTarget(9108), master) ||
		slaveShouldAttackMonster(pet, nil, master) ||
		slaveShouldAttackMonster(pet, newTarget(9109), nil) {
		t.Error("nil 入参应返回 false")
	}
}

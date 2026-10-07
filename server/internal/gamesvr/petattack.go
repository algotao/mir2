// 宠物打怪：原版 `TBaseObject.IsAttackTarget`（ObjBase.pas:21332-21375）里
// "有主的怪（race ≥ RC_ANIMAL=50）"那一段。
//
// 逐字（只保留与"打怪"有关的分支，打人那半边已在 slave.go 的 slaveShouldAttack 里）：
//
//	if m_Master <> nil then begin
//	  if (m_Master.m_LastHiter = BaseObject) or
//	     (m_Master.m_ExpHitter = BaseObject) or            // 主人正在打的那只
//	     (m_Master.m_TargetCret = BaseObject) then Result := True;
//	  if BaseObject.m_TargetCret <> nil then
//	    if BaseObject.m_TargetCret = m_Master then Result := True;
//	  if (BaseObject.m_TargetCret = Self) and (BaseObject.m_btRaceServer >= RC_ANIMAL)
//	    then Result := True;                                 // 它正在打我
//	  if BaseObject.m_Master = m_Master then Result := False; // 不打主人的另一只宠物
//	  if BaseObject.m_boHolySeize then Result := False;
//	  if m_Master.m_boSlaveRelax then Result := False;        // 主人叫宠物休息
//	  ...
//
// 我们的对应物（三个都在 `Player` 上，语义见各自注释）：
//
//	m_LastHiter   → Player.lastHitBy   （最后打主人的**怪**）
//	m_TargetCret  → Player.combatTargetID（主人正在打的怪）
//	m_ExpHitter   → Player.spellTargetID（技能锁定的怪，SetTargetCreat）
//
// ⚠️ 这条链路同时是 `GainSlaveExp` 的触发点（原版 ObjBase.pas:20845：
// 只有"击杀者本身是宠物"才涨宠物经验、主人照常分经验）⇒ 见 petexp.go 文件头。
package gamesvr

import (
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/entity"
)

// slaveShouldAttackMonster 判定宠物该不该打这只怪（原版 IsAttackTarget 的怪分支）。
//
// **调用方持 s.mu**。
func slaveShouldAttackMonster(m, target *entity.Monster, master *Player) bool {
	if m == nil || target == nil || master == nil || master.Obj == nil {
		return false
	}
	if target == m || target.IsDead() || target.IsNPC {
		return false
	}
	if target.MapRef() != m.MapRef() {
		return false
	}
	// 主人叫宠物休息（m_Master.m_boSlaveRelax）、主人隐身 ⇒ 停手
	if master.slaveRelax || master.hasBuff(entity.BuffInvisible) {
		return false
	}
	// 不打"同一主人的另一只宠物"（BaseObject.m_Master = m_Master ⇒ False）
	if target.MasterID != 0 && target.MasterID == master.Obj.ID {
		return false
	}
	// 定身的怪不打（BaseObject.m_boHolySeize ⇒ False）
	if !target.SeizedUntil.IsZero() {
		return false
	}

	// 它正在打主人 / 正在打宠物自己
	if target.TargetID != 0 && (target.TargetID == master.Obj.ID || target.TargetID == m.ID) {
		return true
	}
	// 主人最后挨的那一下是它（替主人报仇）
	if master.lastHitBy != 0 && master.lastHitBy == target.ID {
		return true
	}
	// 主人正在打的怪（近战记录 / 技能锁定）
	if master.combatTargetID != 0 && master.combatTargetID == target.ID {
		return true
	}
	if master.spellTargetID != 0 && master.spellTargetID == target.ID {
		return true
	}
	return false
}

// petHit 是宠物打怪的一次结算结果，收集后由 tickMonsters 在**锁外**发包。
type petHit struct {
	pet           *entity.Monster
	master        *Player
	target        *entity.Monster
	hp, maxHP     uint32
	dmg           uint32
	died          bool
	targetLevel   int
	totalKillNeed int
}

// petAttackMonster 结算宠物对一只怪的一次攻击。**调用方持 s.mu**。
//
// 伤害走标准怪物挥砍：命中判定（命中 vs 敏捷）+ `rollDamage(DC..DCMax, AC)`
// （与 monsterStrike 同源，只是目标从玩家换成怪）；红毒照原版 StruckDamage 放大。
func (s *Server) petAttackMonster(m, target *entity.Monster, now time.Time) petHit {
	ret := petHit{pet: m, target: target, hp: target.HP, maxHP: target.MaxHP}
	if target.Info == nil || m.Info == nil {
		return ret
	}
	if combat.Misses(combat.MonsterHitPoint(m), combat.MonsterHitPoint(target), combat.MonsterSpeedPoint(target)) {
		return ret // 打空（不发包，由调用方看 dmg == 0 跳过）
	}
	minAtk, maxAtk := uint32(m.Info.DC), uint32(m.Info.DCMax)
	if maxAtk < minAtk {
		maxAtk = minAtk
	}
	dmg := rollDamage(minAtk, maxAtk, uint32(target.Info.AC))
	dmg = s.struckMonster(target, dmg, now) // 红毒：受伤放大
	if dmg == 0 {
		return ret
	}
	ret.dmg = dmg
	_, ret.hp, ret.died = target.Hurt(dmg)
	// 宠物经验（原版 GainSlaveExp 的触发点就在这里：击杀者是宠物）
	if target.Info != nil {
		ret.targetLevel = int(target.Info.Level)
	}
	if m.Info != nil {
		ret.totalKillNeed = slaveUpKillCount(int(m.Info.Level), int(m.SlaveExpLevel))
	}
	return ret
}

// applyPetHits 把宠物打怪的结果发出去（**锁外**调用）。
//
// 死亡收尾与火墙一致：锁内已移出索引，这里只补经验/掉落/日志。
func (s *Server) applyPetHits(hits []petHit, now time.Time) {
	for _, h := range hits {
		if h.dmg == 0 {
			continue
		}
		s.broadcastToViewers(h.target.MapRef(), h.target.PosX(), h.target.PosY(), func(o *Player) {
			if o.visible.Contains(h.target.ID) {
				s.sendStruck(o, h.pet.ID, h.target.ID, h.hp, h.maxHP, h.dmg)
			}
		})
		log.Printf("%s 的宠物 %s 打了 %s %d 点（HP %d/%d）",
			h.master.Char.Name, h.pet.Name, h.target.Name, h.dmg, h.hp, h.maxHP)
		if !h.died {
			continue
		}
		// 主人拿经验/掉落（原版：`m_ExpHitter.m_Master.CalcGetExp` + GainExp）
		s.killMonsterBy(h.master, h.target, 0, h.target.PosX(), h.target.PosY())
		// 宠物自己涨经验（原版下一行就是 `m_ExpHitter.GainSlaveExp(m_Abil.Level)`）
		log.Printf("%s 的宠物 %s 击杀 %s（宠物经验 +%d，进度 %d/%d）",
			h.master.Char.Name, h.pet.Name, h.target.Name,
			h.targetLevel, h.pet.SlaveKills()+h.targetLevel, h.totalKillNeed)
		s.gainSlaveExp(h.pet, h.targetLevel)
	}
	_ = now
}

// petAttackTarget 给宠物选一只可打的怪（**调用方持 s.mu**）。
//
// 选最近的：先看主人正在打的那只（跟打），再看其它满足判据的。
func (s *Server) petAttackTarget(m *entity.Monster, master *Player) *entity.Monster {
	var best *entity.Monster
	bestScore := 1 << 30
	prefer := master.combatTargetID
	if prefer == 0 {
		prefer = master.spellTargetID
	}
	for _, t := range s.world.monsters {
		if !slaveShouldAttackMonster(m, t, master) {
			continue
		}
		d := absInt(t.PosX()-m.PosX()) + absInt(t.PosY()-m.PosY())
		score := d
		if t.ID == prefer {
			score -= 100 // 主人正在打的那只优先
		}
		if score < bestScore {
			best, bestScore = t, score
		}
	}
	return best
}

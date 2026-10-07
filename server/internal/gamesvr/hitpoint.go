package gamesvr

import (
	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/delphi"
	"net"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// 命中 / 敏捷（原版的"准确度"）体系。
//
// 原版 `_Attack` 里有**真正的打空判定**（`ObjBase.pas:22241-22246`）：
//
//	if AttackTarget.m_btHitPoint > 0 then
//	  if (m_btHitPoint < Random(AttackTarget.m_btSpeedPoint)) then nPower := 0;
//
// 即"攻击者的命中 < Random(目标的敏捷)"就算打空：伤害归 0，但挥砍动作照播
//（客户端只看到没掉血）。**打空不是"目标无效"**——`IsProperTarget` 已经先判过了。
//
// 双方这两个值的来源：
//
//	玩家（`RecalcAbilitys`，ObjBase.pas:18563-18635）
//	  命中 = DEFHIT(5)
//	       + 基本剑术(3) `Round(9/3*等级)`
//	       + 精神力战法(4) `Round(8/3*等级)`
//	       + 攻杀剑术(7)  `Round(3/3*等级)`
//	  敏捷 = DEFSPEED(15)（道士再 +3，ObjBase.pas:18566）
//
//	怪物（UsrEngn.pas:2606-2607）
//	  命中 = `Monster.wHitPoint`、敏捷 = `Monster.wSpeed`
//	  （就是我们怪物表里的 `hit` / `speed`）
//
// ⚠️ **未建模的部分**：原版还有 `m_BonusAbil.Hit div BonusTick.Hit` 与
// `m_BonusAbil.Speed div BonusTick.Speed`——装备的"准确/敏捷"加成
//（武器特例：AC 高位=准确、MAC 高位=速度，见 `data/item.go:96` 的 TODO）。
// 我们的物品表还没拆这两个字段，所以按 0 处理：结果就是**玩家的命中只随技能涨**，
// 与原版（装备堆准确）有细微差别。
//
// ⚠️ 原版这两个字段是 **Byte**，而 `wSpeed`/`wHitPoint` 是 Word ⇒ Delphi 赋值会
// **截断到低 8 位**。我们表里的值都 < 256，直接取用等价；为保持"超了也一样"的
// 语义，取用时统一 `& 0xFF`。

// 命中/敏捷的**基数**与职业号都在 internal/entity（`DefHit`/`DefSpeed`/`Job*`）——
// 它们是模型常识、客户端也要用，不属于服务端内部参数。
// playerHitPoint 返回玩家的命中（`RecalcHitSpeed`，ObjBase.pas:18556-18635）。
//
//	命中 = DEFHIT(5) + 装备的准确（m_AddAbil.wHitPoint）
//	                + 基本剑术(3) 3×等级 + 精神力战法(4) Round(8/3×等级) + 攻杀剑术(7) 等级
//
// ⚠️ 三个技能都是"**等级 > 0 才加**"（原版各自套在 `if btLevel > 0 then` 里）：
// 用 `@grant-all-magics` 拿到 0 级的技能**不加命中**，所以"学了技能但没练级"
// 照样打空——这不是 bug。
//
// ⚠️ 原版那行还有 `m_BonusAbil.Hit div BonusTick.Hit`（客户端"分配点数"）：
// 那套的点数只能由 GM 命令给（升级给点的分支裹在未定义的 `{$IFDEF FOR_ABIL_POINT}`
// 里，且 `GetBonusPoint` 全树不存在）⇒ 恒为 0，我们不建模。见 §2.1。
func (s *Server) playerHitPoint(p *Player) int {
	hit := entity.DefHit + s.playerAddAbil(p).hit
	add := func(magicID uint32, numerator float64) {
		um := userMagicOf(p, magicID)
		if um == nil || um.Level == 0 {
			return
		}
		hit += delphi.Round(numerator / 3 * float64(um.Level))
	}
	add(3, 9) // 基本剑术：Round(9/3*等级) = 3×等级
	add(4, 8) // 精神力战法：Round(8/3*等级)（1→3、2→5、3→8）
	add(7, 3) // 攻杀剑术：Round(3/3*等级) = 等级
	// 虹魔套 3 件齐 ⇒ 准确 +2（`if boHongMoSuite1 and boHongMoSuite2 and boHongMoSuite3
	// then Inc(m_AddAbil.wHitPoint, 2)`，ObjBase.pas:3343-3345）
	if s.playerItemSpecials(p).hongMoPieces >= 3 {
		hit += 2
	}
	return hit
}

// playerSpeedPoint 返回玩家的敏捷（`DEFSPEED(15)` + 装备，道士 +3）。
//
// 装备那一项是 `Inc(m_btSpeedPoint, m_AddAbil.wSpeedPoint)`（ObjBase.pas:3395），
// 只有部分首饰给（StdMode 20/24 的 MAC 高位、52）。
func (s *Server) playerSpeedPoint(p *Player) int {
	sp := entity.DefSpeed + s.playerAddAbil(p).speed
	if p != nil && p.Char != nil && p.Char.Data != nil && p.Char.Data.Job == entity.JobTaos {
		sp += 3
	}
	return sp
}

// playerLuck 返回玩家的幸运（`m_nLuck`，ObjBase.pas:3401-3402）。
//
//	m_nLuck = Σ装备.btLuck − Σ装备.btUnLuck
//
// 它只用在 `GetAttackPower` 里（见 rollAttack）：运气为正时按概率**直接取伤害上限**，
// 为负时按概率**取下限**。武器"被诅咒"就是这里变负的（MakeWeaponUnlock 把
// btValue[4] 加 1 ⇒ MAC 低位 +1 ⇒ 诅咒 +1）。
func (s *Server) playerLuck(p *Player) int {
	a := s.playerAddAbil(p)
	return a.luck - a.unluck
}

// playerSubAbility 是 `SM_SUBABILITY` 要下发的副属性
// （ObjBase.pas:5599-5603 的 SendDefMessage，字段布局见客户端 ClMain.pas:4206）。
type subAbility struct {
	hit, speed                  int
	antiPoison, poisonRecover   int
	healthRecover, spellRecover int
	antiMagic                   int
}

func (s *Server) playerSubAbility(p *Player) subAbility {
	a := s.playerAddAbil(p)
	return subAbility{
		hit:           s.playerHitPoint(p),
		speed:         s.playerSpeedPoint(p),
		antiPoison:    a.antiPoison,
		poisonRecover: a.poisonRecover,
		healthRecover: a.healthRecover,
		spellRecover:  a.spellRecover,
		antiMagic:     a.antiMagic,
	}
}

// sendSubAbility 下发副属性（ObjBase.pas:5599-5603 的 `SendDefMessage(SM_SUBABILITY, …)`）。
//
// 字段布局（客户端 ClMain.pas:4206-4213 按此解）：
//
//	Recog  低字 = 抗魔           Param  低/高字节 = 命中 / 敏捷
//	Tag    低/高字节 = 抗毒 / 解毒恢复
//	Series 低/高字节 = 体力恢复 / 魔法恢复
//
// ⚠️ 顺序别写反：原版用 `MakeWord(A, B)`（A 进**低**字节），我们在别处见过
// "先高后低"的参数顺序，这里不是。
//
// 凡是要重发 SM_ABILITY 的地方都该跟着发它（原版 `RecalcAbilitys` 末尾同时
// `SendMsg(RM_ABILITY)` 与 `SendMsg(RM_SUBABILITY)`）——否则客户端显示的
// 命中/敏捷会停在旧值（换装后尤其明显）。
func (s *Server) sendSubAbility(c net.Conn, p *Player) {
	sa := s.playerSubAbility(p)
	s.send(c, proto.SM_SUBABILITY,
		int32(uint16(sa.antiMagic)),
		uint16(sa.hit)|uint16(sa.speed)<<8,
		uint16(sa.antiPoison)|uint16(sa.poisonRecover)<<8,
		uint16(sa.healthRecover)|uint16(sa.spellRecover)<<8,
		"")
}

// rollMelee 掷出这一刀的**未减防威力**（原版 `_Attack` 的前半段）：
// 先 `GetAttackPower`，再叠加充能加成，最后判打空（打空返回 0）。
//
// 所有近战路径都要经过它，否则"命中/敏捷"只对一部分攻击生效
// （原版这个判定在 `_Attack` 里，对玩家、怪物、怪物打玩家**一视同仁**）。
func (s *Server) rollMelee(attackerHit, targetHitPoint, targetSpeed, attackerLuck int,
	minAtk, maxAtk uint32, b warBonus) (int, bool) {

	if combat.Misses(attackerHit, targetHitPoint, targetSpeed) {
		return 0, true
	}
	return b.apply(rollAttack(minAtk, maxAtk, attackerLuck)), false
}

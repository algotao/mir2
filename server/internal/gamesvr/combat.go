package gamesvr

import (
	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/delphi"
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

func (s *Server) handleAttack(c net.Conn, p *Player, pkt wire.Packet) {
	// 石化/麻痹期间不能出手（与移动门同理：原版靠客户端不打这个包，
	// 我们在服务端也拦一手，见 paralysisPlayerOnHit 的说明）。
	if p.Obj != nil && p.Obj.Stoned(time.Now()) {
		return
	}
	if !p.logonDone {
		return
	}
	mode := hitModeOf(pkt.Head.Ident)
	dir := proto.LoByte(pkt.Head.Tag)
	// 改朝向走 s.mu 域（坐标/朝向的读写规则见 statelock.go 第二节）。
	s.turnPlayer(p, dir)

	// 每挥一刀都推进"攻杀剑术"的自动充能（原版在攻击包分派之后，
	// ObjBase.pas:8863-8876）。用 defer 是因为下面有多条 return 分支
	//（刺杀/半月/落空），而原版对它一视同仁——挂刀就计数。
	defer s.chargeAfterAttack(p)

	hasErgum := userMagicOf(p, 12) != nil  // 刺杀剑术
	hasBanwol := userMagicOf(p, 25) != nil // 半月弯刀

	// 充能快照（原版 `boPowerHit := m_boPowerHit`，ObjBase.pas:18826-18827）：
	// 动画看**出刀前**的状态。
	//
	// ⚠️ fireHit 由 ticker 的 tickWarrCharge 同时读写 ⇒ 读快照（持锁）。
	// powerHit 只有本人 goroutine 动，直接读即可（见 server.go 的字段说明）。
	fireArmed, _ := p.fireChargeSnapshot()
	powerHit := p.powerHit && userMagicOf(p, 7) != nil
	fireHit := fireArmed && userMagicOf(p, 26) != nil
	// 消费（原版在 `_Attack` 里，**打空也消费**），必须在找目标之前。
	bonus := s.consumeWarrCharge(p, mode)

	// 挖矿（原版 `AttackDir` 开头的"挖矿"分支，ObjBase.pas:8819-8852）：
	// **重击包** + 手里是**鹤嘴锄**（Shape=19）+ 正前方那格**不可走** ⇒ 这一下打的是矿脉。
	// 认成挖矿就到此为止（原版也是直接 Exit，不再走 AttackDir）。
	if s.handleMine(p, pkt) {
		return
	}

	// 「试刀」：武器升级的结果在**下一刀砍到东西**时结算（原版 `AttackDir` 开头，
	// ObjBase.pas:18819-18824：`AttackTarget := GetPoseCreate()`，非空且有武器就
	// `CheckWeaponUpgrade()`）。⚠️ 它在**模式分派之前** ⇒ 刺杀/半月同样会触发，
	// 所以我们放在这里，而不是等到单格目标那一段。
	if s.facingHasTarget(p, dir) {
		s.settleWeaponUpgrade(p)
	}

	// 刺杀剑术(mode 4)：目标 = **正前方第二格**（第一格有没有东西不影响）。
	// 原版只要第二格有对象就算"打出去了"（不降级），所以这里没有落空惩罚。
	if mode == hitLong && hasErgum {
		s.broadcastSwing(p, proto.SM_LONGHIT, dir)
		minAtk, maxAtk := s.attackPower(p)
		if !s.attackSwordLong(p, userMagicOf(p, 12), dir, minAtk, maxAtk) {
			log.Printf("%s 刺杀剑术落空（正前方第二格为空）方向=%d", p.Char.Name, dir)
		}
		return
	}
	// 半月弯刀(mode 5)：打三个格子，**先扣蓝**；蓝不够时降级成普通单格攻击
	// （原版 `wHitMode := RM_HIT`）。
	if mode == hitWide && hasBanwol {
		minAtk, maxAtk := s.attackPower(p)
		if s.attackSwordWide(p, userMagicOf(p, 25), dir, minAtk, maxAtk) {
			s.broadcastSwing(p, proto.SM_WIDEHIT, dir)
			return
		}
		mode = hitNormal
	}

	// 广播攻击动作（让周围玩家看到挥砍）。重击/大力只是动画不同，伤害同普通；
	// 攻杀(3)/烈火(7) 在充能时换成对应的强化动画。
	s.broadcastSwing(p, swingIdent(mode, hasErgum, hasBanwol, powerHit, fireHit), dir)

	// 找前方一格的实体：玩家与怪物都算（PvP 与打怪共用一个目标格）。
	tx, ty := p.Obj.PosX()+int(entity.DirDelta[dir][0]), p.Obj.PosY()+int(entity.DirDelta[dir][1])
	victim := s.playerAt(p.Obj.MapRef(), tx, ty)
	if victim != nil {
		s.attackPlayer(c, p, victim, dir, bonus)
		return
	}
	target := s.monsterAt(p.Obj.MapRef(), tx, ty)
	if target == nil {
		log.Printf("攻击落空: 玩家(%d,%d) 方向=%d 目标格(%d,%d)",
			p.Obj.PosX(), p.Obj.PosY(), dir, tx, ty)
		return
	}
	// 城堡单位（城门/城墙）另有准入判定：非攻城期石化不可打，
	// 守方自己人也不能打自己的城墙（TGuardUnit.IsProperTarget，ObjMon2.pas:828-883）。
	if target.IsCastleUnit() && !s.canHitCastleUnit(p, target) {
		return
	}

	minAtk, maxAtk := s.attackPower(p)
	// 掷出**未减防**的威力 → 叠加充能加成（原版顺序：`_Attack` 里先
	// `GetAttackPower` 再 `Inc/ROUND` 加攻杀/烈火，最后才减目标防御），
	// 其间还要过**打空判定**（命中 vs 目标敏捷）。
	power, missed := s.rollMelee(s.playerHitPoint(p), combat.MonsterHitPoint(target),
		combat.MonsterSpeedPoint(target), s.playerLuck(p), minAtk, maxAtk, bonus)
	if missed {
		log.Printf("%s 的攻击打空了（命中 %d < Random(敏捷 %d)，目标 %s）",
			p.Char.Name, s.playerHitPoint(p), combat.MonsterSpeedPoint(target), target.Name)
		return
	}
	// 怪物 AC 是单值（非 Min/Max 打包），故 lo=hi
	dmg := applyArmor(power, uint32(proto.PackMinMax(target.Info.AC, target.Info.AC)))
	if dmg == 0 {
		// **没破防**：原版 `_Attack` 里第二道 `if nPower > 0` 不成立 ⇒ 不发 RM_STRUCK、
		// 不扣血、也不记仇恨（`SetLastHiter` 在 RM_STRUCK 里）。挥砍动画已经在上面
		// `broadcastSwing` 发过了 ⇒ 表现就是"砍上去没反应、也没伤害数字"。
		log.Printf("%s 的攻击没破开 %s 的防御（威力 %d ≤ AC）", p.Char.Name, target.Name, power)
		return
	}
	// 红毒：目标受伤放大（原版 StruckDamage 在**受击方**算）
	dmg = s.struckMonster(target, dmg, time.Now())
	// 打了城堡单位（城门/城墙/守卫）⇒ 进 2 分钟仇恨窗口（TGuardUnit.Struck，见 guard.go）。
	// ⚠️ 放在"打空判定"之后：原版的 Struck 是**挨到这一下**才触发。
	s.markHiter(p.Obj, target, time.Now())
	// 记住"我正在打谁"：宠物据此跟打同一只（IsAttackTarget）
	p.combatTargetID = target.ID
	if !bonus.isZero() {
		log.Printf("%s 的%s命中 %s：威力 %d → 伤害 %d",
			p.Char.Name, bonus.name(), target.Name, power, dmg)
		s.trainWarrCharge(c, p, mode)
	}
	// 记录被攻击时刻：修门/修墙要求被打 60 秒后才能修
	//（Castle.pas:1158 用 m_dwStruckTick）。
	if target.IsCastleUnit() {
		target.StruckMark(time.Now())
	}
	died := target.Damage(dmg)
	if died && target.IsCastleUnit() {
		// 城门被打碎 ⇒ 阻挡格全开（原版 TCastleDoor.Die → SetMapXYFlag(2)）。
		// 城墙打碎没有格标志变化（原版 TWallStructure 不覆盖 Die）。
		s.castleDoorDestroyed(target)
	}

	// 虹魔套吸血 / 麻痹戒指（原版 _Attack 里紧跟在伤害之后，ObjBase.pas:22273 / :22265）。
	// ⚠️ 顺序照原版：先吸血再判麻痹（两者互不影响，但别改顺序免得以后核对时看花眼）。
	s.hongMoLeech(p, target, dmg)
	s.paralysisOnHit(p, target)

	// 受击表现：给能看到怪物的玩家发 SM_STRUCK。
	// ⚠️ 参数语义（MirClient/ClMain.pas:4964）：
	//    Recog=ActorId, Param=HP, Tag=MaxHP, Series=**伤害值**
	//    易错点：伤害在 Series 而非 Tag。
	s.broadcastToViewers(target.MapRef(), target.PosX(), target.PosY(), func(other *Player) {
		if other.visible.Contains(target.ID) {
			s.sendStruck(other, p.Obj.ID, target.ID, target.HP, target.MaxHP, dmg)
			obs.Event("attack_hit", "player", p.Char.Name, "monster", target.ID,
				"monster_name", target.Name, "dmg", dmg, "hp", target.HP,
				"max_hp", target.MaxHP)
		}
	})

	if !died {
		return
	}

	// 死亡：广播 SM_DEATH，结算经验
	// 尸体：**不从视野账本里摘掉**（原版 `Die` 之后对象仍留在图上，
	// 3 分钟后 `MakeGhost → RM_DISAPPEAR` 才收走）。
	//
	// ⚠️ 这里原来是 `visible.Remove(...)` ⇒ 账本里没了它，`sweepCorpses`
	//（`butch.go`）那句 `p.visible.Remove(g.id)` 恒为 false ⇒ **客户端尸体永远不消失**
	//（用户 2026-10-09 问的第 4 条）。留着账本，收尸那条才发得出消失包。
	s.broadcastToViewers(target.MapRef(), target.PosX(), target.PosY(), func(other *Player) {
		if other.visible.Contains(target.ID) {
			s.sendDeathTo(other, target.ID, target.PosX(), target.PosY(), target.Facing(), p.Obj.ID)
		}
	})
	s.mu.Lock()
	s.keepCorpseOrRemove(target)
	s.mu.Unlock()

	// 经验与升级统一走 grantExp（脚本 GIVEEXP 也用同一条路径）
	s.grantExp(p, uint64(target.Info.Exp))
	// 攻击损耗武器耐久（原版按概率扣，这里每次命中 -1，便于观察）
	s.wearWeapon(p)

	log.Printf("%s 击杀 %s (ActorId=%d) 伤害=%d 经验+%d",
		p.Char.Name, target.Name, target.ID, dmg, target.Info.Exp)

	// 掉落：物品散落 + 金币按原版 `ScatterGolds` **撒到地上**（不再直接进腰包）。
	// ⚠️ 金币原先直接 `Gold += gold` ⇒ 地面永远不会出现金币，整条"掉落—拾取"链路缺失。
	s.scatterKillGold(target, p.Obj.ID)
}

// sendSwing 把一次挥砍动画发给**一个**玩家（两条协议各取所需）。
//
// legacy：挥砍消息号本身（SM_HIT/SM_HEAVYHIT/…，Recog=出手者）。
// 新协议：`EntityAction{action = 1..8}`——**与 `AttackAction` 同值**，
// 客户端不必再翻译一层（见 netproto.go 的动作 id 值域说明）。
func (s *Server) sendSwing(to, actor *Player, ident uint16, dir uint8) {
	if to == nil || actor == nil {
		return
	}
	if sink := to.protoOut; sink != nil {
		sink.action(actor.Obj.ID, attackActionOf(ident))
		return
	}
	s.send(to.conn, ident, int32(actor.Obj.ID),
		uint16(actor.Obj.PosX()), uint16(actor.Obj.PosY()), uint16(dir), "")
}

// broadcastSwing 广播一次挥砍动作（给自己 + 视野内的人）。
//
// ⚠️ 我们**先**发挥砍再结算伤害，而原版 `AttackDir` 是先 `_Attack` 再
// `SendAttackMsg`。客户端对两者都只是"收到了就播动画"，包序不影响表现；
// 保持现状以免动到既有的 attack/peer 用例包序断言。
//
// ⚠️ 自己那一份是**单独发**的、不能并进上面那段广播：`p.visible` 只装**别人**
// （不含自己），所以广播里那个 `visible.Contains` 判据天然把自己滤掉了。
func (s *Server) broadcastSwing(p *Player, ident uint16, dir uint8) {
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		if other.visible.Contains(p.Obj.ID) {
			s.sendSwing(other, p, ident, dir)
		}
	})
	s.sendSwing(p, p, ident, dir)
}

// playerIDOf 取玩家的 ActorId（nil 安全：毒源/出手者允许"没有具体的人"）。
func playerIDOf(p *Player) uint32 {
	if p == nil || p.Obj == nil {
		return 0
	}
	return p.Obj.ID
}

// sendStruck 发送受击消息（两条协议各取所需）。
//
// legacy：SM_STRUCK。Recog=受击者, Param=HP, Tag=MaxHP, Series=伤害（ClMain.pas:4964）。
// body 是 TMessageBodyWL，承载攻击者与状态，客户端据此播放受击动作。
// 新协议：`Damage{出手方, 受击者, 伤害}` + `EntityHealth` + `EntityAction(受击)`。
//
// `attackerID` 允许为 0（火墙/毒/脚本这类"没有具体出手者"的伤害）——
// 与 `Death.killer_id` 的约定一致。
func (s *Server) sendStruck(to *Player, attackerID, victimID uint32, hp, maxHP, dmg uint32) {
	if to == nil {
		return
	}
	if sink := to.protoOut; sink != nil {
		sink.damage(attackerID, victimID, int32(dmg), 0)
		sink.health(victimID, hp, maxHP)
		sink.action(victimID, actionHurt)
		return
	}
	wl := proto.MessageBodyWL{Param1: 0, Param2: 0, Tag1: 0, Tag2: int32(dmg)}
	s.send(to.conn, proto.SM_STRUCK, int32(victimID), uint16(hp), uint16(maxHP),
		uint16(dmg), string(wl.Append(nil)))
}

// sendHealthChanged 同步血量/蓝量（SM_HEALTHSPELLCHANGED）。
//
// Recog=ActorId, Param=HP, Tag=MP, Series=MaxHP（ClMain.pas SM_HEALTHSPELLCHANGED）。
//
// ⚠️ 新协议的 `EntityHealth` **只有 hp/max_hp，没有 mp**。所以：
//
//   - 血量照样发 `EntityHealth`；
//   - 若是**自己**，额外补一条完整 `AbilityUpdate`（客户端本来就要它来画血/蓝条）——
//     否则用完技能后蓝条永远不动（`sendHealthChanged` 是所有 mp 变化的唯一出口，
//     见 monsterai/loops/spell 等 23 处调用点）。
//     代价是"每次血量变化多发一条完整能力值"（回血每秒两次），本地开发可以接受。
func (s *Server) sendHealthChanged(to *Player, actorID uint32, hp, mp, maxHP uint32) {
	if to == nil {
		return
	}
	if sink := to.protoOut; sink != nil {
		sink.health(actorID, hp, maxHP)
		if to.Obj != nil && actorID == to.Obj.ID {
			if ab := to.abilCopy(); ab != nil {
				sink.ability(ab, to.gold())
			}
		}
		_ = mp
		return
	}
	s.send(to.conn, proto.SM_HEALTHSPELLCHANGED, int32(actorID),
		uint16(hp), uint16(mp), uint16(maxHP), "")
}

// sendDeathTo 把"某实体死亡"发给**一个**玩家（两条协议各取所需）。
//
// legacy：SM_DEATH（Recog=ActorId，坐标+朝向供客户端就地播死亡动画）。
// 新协议：`EntityAction(死亡)` + `Death{实体, 击杀者}`。
//
// ⚠️ 做成一个函数而不是每处各写一遍分支：SM_DEATH 有 8 个发送点
// （近战/半月/技能/毒/火墙/脚本/宠物/玩家死亡），散着写迟早漏一个，
// 而漏的那个表现为"新协议客户端看见怪凭空消失"（怪没了、死亡动画没有）。
func (s *Server) sendDeathTo(to *Player, id uint32, x, y int, dir uint8, killerID uint32) {
	if to == nil {
		return
	}
	if sink := to.protoOut; sink != nil {
		sink.action(id, actionDeath)
		sink.death(id, killerID)
		return
	}
	s.send(to.conn, proto.SM_DEATH, int32(id), uint16(x), uint16(y), uint16(dir), "")
}

// playerAC 返回玩家防御（装备 AC 累加，打包为 DWord）。
func (s *Server) playerAC(p *Player) uint32 {
	var lo, hi uint32
	// ⚠️ 读属性拿**快照**（本人 goroutine 在改它，见 statelock.go）
	if ab := p.abilCopy(); ab != nil && ab.Ac != nil {
		lo, hi = uint32(ab.Ac.Min), uint32(ab.Ac.Max)
	}
	// 装备的防御来自 `m_AddAbil.wAC`（原版 ObjBase.pas:3412
	// `m_WAbil.AC := MakeLong(LoWord(wAC)+LoWord(m_Abil.AC), HiWord(wAC)+HiWord(m_Abil.AC))`）。
	//
	// ⚠️ 不能"把每件装备的 AC 直接相加"：**武器**的 AC 高/低位是"准确/幸运"、
	// 首饰 20/24 的 AC 高位是"准确"（ItmUnit.pas:556-699）—— 它们不是防御。
	if a := s.playerAddAbil(p); a.acLo > 0 || a.acHi > 0 {
		lo += uint32(a.acLo)
		hi += uint32(a.acHi)
	}
	// 增益只加**物防**（神圣战甲术）。
	// ⚠️ 原来写的是 `ac + mac`，把幽灵盾的魔防也塞进了 AC —— 幽灵盾是
	// MagMakeDefenceArea(..., 1)（魔防），原版两者互不串（Magic.pas:452-461）。
	if ac, _ := p.defenseBonus(); ac > 0 {
		lo += ac
		hi += ac
	}
	return proto.PackMinMax(uint16(lo), uint16(hi))
}

// playerMAC 返回玩家**魔防**（装备 MAC 累加 + 幽灵盾），打包为 DWord。
//
// ⚠️ 与 playerAC 是两条不同的减伤路径：魔法伤害走 GetMagStruckDamage 减 MAC，
// 物理伤害走 GetHitStruckDamage 减 AC（ObjBase.pas:22441 / 22414）。
// 拿 playerAC 去顶替，表现为"高魔防角色照样被魔法打死"。
func (s *Server) playerMAC(p *Player) uint32 {
	var lo, hi uint32
	if ab := p.abilCopy(); ab != nil && ab.Mac != nil {
		lo, hi = uint32(ab.Mac.Min), uint32(ab.Mac.Max)
	}
	// 同 playerAC：走 `m_AddAbil.wMAC`，而不是把每件装备的 MAC 直接相加
	//（武器 MAC 低位是"诅咒"、首饰 19 的 MAC 低位是"诅咒"、高位是"幸运"）。
	if a := s.playerAddAbil(p); a.macLo > 0 || a.macHi > 0 {
		lo += uint32(a.macLo)
		hi += uint32(a.macHi)
	}
	// 幽灵盾只加魔防（神圣战甲加物防，在 playerAC 那边）
	if _, mac := p.defenseBonus(); mac > 0 {
		lo += mac
		hi += mac
	}
	return proto.PackMinMax(uint16(lo), uint16(hi))
}

// magic.AttackPower 返回玩家攻击力下限/上限（徒手 + **全身装备**的 DC）。
//
// 对应 `m_WAbil.DC = m_Abil.DC + m_AddAbil.wDC`（原版 ObjBase.pas:3414），
// 而 `m_AddAbil.wDC` 是**所有**装备累加（ItmUnit.pas:696-698），不只是武器
// —— 带 DC 的项链/手镯（如"白色虎齿项链"+DC）也算，早先只取武器会低一截。
func (s *Server) attackPower(p *Player) (min, max uint32) {
	min, max = 1, 3 // 徒手
	if ab := p.abilCopy(); ab != nil && ab.Dc != nil {
		min, max = uint32(ab.Dc.Min), uint32(ab.Dc.Max)
	}
	if a := s.playerAddAbil(p); a.dcLo > 0 || a.dcHi > 0 {
		min += uint32(a.dcLo)
		max += uint32(a.dcHi)
	}
	if max < min {
		max = min
	}
	return min, max
}

// rollDamage 计算伤害。
//
// 公式（ObjBase.pas:22414 GetHitStruckDamage）：
//
//	攻击力 = [min, max] 随机
//	防御   = [LoWord(AC), HiWord(AC)] 随机
//	伤害   = max(1, 攻击力 - 防御)   ← 保底 1，避免完全免疫导致打不死
func rollDamage(minAtk, maxAtk, ac uint32) uint32 {
	// ⚠️ 走 rollAttack（`GetAttackPower`）而不是自己 rand：全项目的掷骰都该来自
	// 同一个 delphi.Random 源（`delphi.RandN`），否则同一刀在不同路径下分布不一致。
	// 攻击者是怪物 ⇒ 幸运恒 0（怪物没有 m_nLuck）。
	power := uint32(rollAttack(minAtk, maxAtk, 0))
	acLo, acHi := uint32(proto.UnpackLo(ac)), uint32(proto.UnpackHi(ac))
	if acHi < acLo {
		acHi = acLo
	}
	armor := acLo
	if acHi > acLo {
		armor = acLo + uint32(delphi.Random(int(acHi-acLo+1)))
	}
	if power <= armor {
		return 0 // 没破防（原版 `_MAX(0, …)`）；调用方按"打空"处理
	}
	return power - armor
}

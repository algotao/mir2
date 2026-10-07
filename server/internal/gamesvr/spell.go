package gamesvr

import (
	"encoding/binary"
	"github.com/algotao/mir2/server/internal/tscale"
	"log"
	"net"
	"time"

	"math/rand/v2"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// 技能系统。
//
// CM_SPELL=3017 的字段布局（**我方自定义**，对接真客户端时需核对）：
//
//	Recog  = 技能号 MagicID
//	Param  = 目标 x
//	Tag    = 目标 y
//	Series = 目标 ActorId（0 表示无锁定目标，按坐标/自身处理）
//
// ⚠️ 原版客户端有两种发法（ClMain.pas:2393 / :2443），一种走
// SendSpellMsg(ident, dir, 0, magicId, 0)，另一种走 SendMsg 带 targx/targy/targid。
// 真客户端接入前这里必须重新比对，否则技能会全部落空。

// ⚠️ 每条技能的长度是 `MagicBodySize`（见 internal/proto）——
// 客户端按同一个值切分 body，所以它属协议契约、不放在这里。
// 每条内容：magic_id / level / tran_point 各 uint32。

// minSpellInterval 是施法间隔下限（避免连点刷屏）。
const minSpellInterval = 300 * time.Millisecond

// initialMagicsByJob 按职业给出初始技能号：0=战士 1=法师 2=道士。
//
// 战士→基本剑术(3)、法师→火球术(1)、道士→治愈术(2)。
var initialMagicsByJob = [][]uint32{{3}, {1}, {2}}

// damageMagics 是**单体**远程伤害类技能（MagicID）。
//
// 火球术1 大火球5 地狱火9 疾光电影10 雷电术11 灵魂火符13
// 爆裂火焰23 地狱雷光24 冰咆哮33
//
// ⚠️ 火墙22 **不在这里**：它不是单体伤害，而是铺一片地面事件（见 wall.go），
// 早先被列进来是因为"能打人"的粗分——现在由 wallLoop 周期结算 +
// 移动时结算两条路径负责。
var damageMagics = map[uint32]bool{
	1: true, 5: true, 9: true, 10: true, 11: true,
	13: true, 23: true, 24: true, 33: true,
}

// passiveMagics 是被动技能，不允许主动释放。
//
//	基本剑术(3)   被动加命中（`m_btHitPoint`）
//	精神力战法(4) 同样是**被动**加命中（`SKILL_ILKWANG = 4`，Grobal2.pas:1273；
//	              原版 DoSpell 里没有它的分支，加成走 RecalcHitSpeed）
//
// 若不排除，客户端"释放"它们会被服务端白扣 MP（走 default 分支再记一条
// "未实现的技能"日志）——3 早就在表里，4 是 2026-10-06 补的。
var passiveMagics = map[uint32]bool{3: true, 4: true}

// sendMyMagics 下发玩家技能列表（SM_SENDMYMAGIC=211）。
func (s *Server) sendMyMagics(c net.Conn, p *Player) {
	var body []byte
	if p.Char.Data != nil {
		for _, um := range p.Char.Data.Magics {
			var b [proto.MagicBodySize]byte
			binary.LittleEndian.PutUint32(b[0:], um.MagicId)
			binary.LittleEndian.PutUint32(b[4:], um.Level)
			binary.LittleEndian.PutUint32(b[8:], uint32(um.TranPoint))
			body = append(body, b[:]...)
		}
	}
	s.send(c, proto.SM_SENDMYMAGIC, 0, 0, 0, uint16(len(body)/proto.MagicBodySize), string(body))
}

// ensureInitialMagics 首次进游戏时补发初始技能。
//
// 放在这里而不是建角色时写入，是为了让**已存在的旧角色**也能拿到技能，
// 不必做一次存档迁移。
func (s *Server) ensureInitialMagics(p *Player) {
	data0 := p.Char.Data
	if data0 == nil || len(data0.Magics) > 0 {
		return
	}
	if s.cfg.grantAll {
		// 仅测试：授予全部技能，便于验证尚未开放学习的技能（隐身/魔法盾/召唤等）
		for _, info := range s.data.tables.Magics.All() {
			// 给 0 级而非满级：满级的 MP 消耗（如魔法盾 20+30*3）
			// 远超新角色的蓝量，会全部因 MP 不足而失败
			data0.Magics = append(data0.Magics,
				&pb.UserMagic{MagicId: uint32(info.MagicID), Level: 0})
		}
		log.Printf("%s 获得全部 %d 个技能（测试开关）", p.Char.Name, len(data0.Magics))
		return
	}

	job := int(data0.Job)
	if job < 0 || job >= len(initialMagicsByJob) {
		job = 0
	}
	for _, id := range initialMagicsByJob[job] {
		data0.Magics = append(data0.Magics, &pb.UserMagic{MagicId: id, Level: 0})
	}
	log.Printf("%s 获得初始技能 %d 个", p.Char.Name, len(data0.Magics))
}

// decodeSpellMsg 解析 CM_SPELL 的字段布局。
//
// ⚠️ **原版客户端有两条发法，技能号放在不同字段**（都要支持）：
//
//	发法1「自身技能，无目标」ClMain.pas:2393
//	  SendSpellMsg(CM_SPELL, g_MySelf.m_btDir{x}, 0, pcm.Def.wMagicId, 0)
//	  → MakeDefaultMsg(ident, MakeLong(x,y), Loword(target), dir, Hiword(target))
//	    Recog = MakeLong(方向, 0)   Param = 0   Tag = **技能ID**   Series = 0
//
//	发法2「有目标坐标」ClMain.pas:2443
//	  SendMsg(CM_SPELL, targx, targy, tdir, Integer(pmag), targid, ...)
//	  → 动作队列 → SendSpellMsg(Ident, X, Y, Dir, State)
//	    Recog = MakeLong(目标X, 目标Y)   Param = **技能ID**   Tag = 方向   Series = 0
//
//	权威依据：
//	  - MakeDefaultMsg 签名：msg, Recog, param, tag, series（Grobal2.pas:2676）
//	  - TDefaultMessage 布局：Recog:i32 + Ident/Param/Tag/Series:u16 = 12B
//	    （Grobal2.pas:498-504，与我们的 proto.TDefaultMessage 一致）
//	  - 服务端把 DefMsg.Tag 传成 wParam（UsrEngn.pas:1649 → ObjBase.pas:19331 签名）
//
// 所以：**技能号在 Param 或 Tag**，**坐标一律从 Recog 解包**。
//
// ⚠️ 原版**不发目标 ActorId**（1000000+ 装不进 16 位的 Param/Tag/Series），
// 服务端靠坐标找目标 —— 这也是 HANDOFF 里"技能目标一律坐标优先"的根源。
// targID 仍保留为兜底（Series 位在我们自己的协议里曾用作目标 ID）。
func decodeSpellMsg(h proto.DefaultMessage) (magicID uint32, targX, targY int, targID uint32) {
	// 技能号：先 Param（发法2），为 0 则用 Tag（发法1）
	magicID = uint32(h.Param)
	if magicID == 0 {
		magicID = uint32(h.Tag)
	}
	// 坐标：Recog 打包（MakeLong(x,y) = x 低16位 / y 高16位）
	targX = int(uint16(h.Recog))
	targY = int(uint16(h.Recog >> 16))
	if targX == 0 && targY == 0 {
		// 兜底：万一有人按"Param=x, Tag=y"的旧布局发（我们的历史约定 / 调试工具）
		targX, targY = int(h.Param), int(h.Tag)
	}
	return magicID, targX, targY, uint32(h.Series)
}

// handleSpell 处理 CM_SPELL（释放技能）。
func (s *Server) handleSpell(c net.Conn, p *Player, m wire.Packet) {
	magicID, targX, targY, targID := decodeSpellMsg(m.Head)

	data0 := p.Char.Data
	if data0 == nil || data0.Abil == nil {
		return
	}

	// 1. 必须已学会该技能
	var um *pb.UserMagic
	for _, x := range data0.Magics {
		if x != nil && x.MagicId == magicID {
			um = x
			break
		}
	}
	if um == nil {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}
	info := s.data.tables.Magics.GetByID(uint16(magicID))
	if info == nil || passiveMagics[magicID] {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 战士近战系**不走 DoSpell**（原版 Magic.pas:264 `if IsWarrSkill(...) then exit`）：
	// 它们的按键只做"点燃/开关"，MP 与冷却各有自己的一套（见 warrskill.go）。
	//
	// ⚠️ 必须在这里就分流，不能等下面的 switch：通用流程会先按 `spellPoint`
	// 扣一遍 MP 并按 per-magic 冷却计时，而烈火自己也扣 MP（10 秒冷却另算），
	// 晚分流等于扣两次。
	if s.warrSkillKey(c, p, magicID, um, info, targX) {
		return
	}

	// 1.5 护身符/毒药校验（原版 case 13..19 与 case 30 的外层 boSpellFail）
	//
	// ⚠️ 必须**在扣 MP 之前**：原版是先 CheckAmulet 再 GetSpellPoint 扣蓝，
	// 顺序反了会让"没护身符"也扣掉 MP。
	amuletShape, amuletOK := s.requireAmulet(c, p, magicID, info.Name)
	if !amuletOK {
		return
	}

	// 2. MP 与冷却
	abil := data0.Abil
	// ⚠️ 用 spellPoint（= 原版 GetSpellPoint），不是 `Spell + DefSpell*level`：
	// 固定项与等级项是**反**的，见 spellpower.go 的注释。
	mpCost := magic.SpellPoint(info, um)
	if abil.Mp < mpCost {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		// ⚠️ 这条日志不是冗余：MP 不足时技能被**静默丢弃**（客户端只收到一个
		// SM_MAGICFIRE_FAIL，看不出原因）。排查"技能点了没反应"全靠它。
		log.Printf("%s 放 %s(id=%d) 失败：MP 不足 %d/%d（需要 %d）",
			p.Char.Name, info.Name, magicID, abil.Mp, abil.MaxMp, mpCost)
		obs.Event("spell_mp_short", "player", p.Char.Name, "spell", info.Name,
			"spell_id", int(magicID), "mp", abil.Mp, "need", mpCost)
		return
	}
	// 冷却按技能号分别计算；TMagic.Delay 单位是 ms，
	// 另设一个下限避免客户端连点造成刷屏。
	cd := tscale.D(time.Duration(info.Delay) * time.Millisecond)
	if cd < tscale.D(minSpellInterval) {
		cd = tscale.D(minSpellInterval)
	}
	if p.spellLast == nil {
		p.spellLast = make(map[uint32]time.Time)
	}
	if t, ok := p.spellLast[magicID]; ok && time.Since(t) < cd {
		obs.Event("spell_cooling", "player", p.Char.Name, "spell", info.Name,
			"spell_id", int(magicID), "left_ms", int((cd - time.Since(t)).Milliseconds()))
		return // 冷却中：不扣 MP，静默忽略（原版也是直接丢弃）
	}
	p.spellLast[magicID] = time.Now()
	p.addMP(-int64(mpCost)) // 扣蓝（持锁；不够时夹到 0，与原来那句守卫等价）

	// 3. 施法动作广播（让周围玩家看到起手）
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		s.send(other.conn, proto.SM_SPELL, int32(p.Obj.ID), uint16(magicID),
			uint16(targX), uint16(targY), "")
	})

	switch {
	case magicID == magicFireWall:
		// 火墙必须在 damageMagics 之前判：它铺地面事件，不打单体。
		s.castWall(c, p, info, um, targX, targY)
	case magicID == 32: // 圣言术：对不死系怪物的即死技能
		s.castKillUndead(c, p, info, um, targX, targY, targID)
	case magicID == 20: // 诱惑之光：把一只野生怪收为自己的宠物
		s.castTamming(c, p, info, um, targX, targY, targID)
	case magicID == 21: // 瞬息移动：随机传送到自己家的地图
		s.castSpaceMove(c, p, um)
	case magicID == 29: // 群体治愈术：半径 1 友方回血
		s.castGroupHeal(c, p, info, um, targX, targY)
	case magicID == 16: // 困魔咒：半径 1 定身怪物
		s.castHolyCurtain(c, p, info, um, targX, targY)
	case damageMagics[magicID]:
		s.castDamageSpell(c, p, info, um, targX, targY, targID)
	case magicID == 2: // 治愈术
		s.castHealSpell(c, p, info, um)
	case magicID == 8: // 抗拒火环：把身边的敌人推开（MagPushArround）
		s.castRepulsion(p, um)
	case magicID == 19: // 集体隐身术：落点半径 1 的友方隐身（MagMakeGroupTransparent）
		s.castGroupInvisible(p, info, um, targX, targY)
	case magicID == 18: // 隐身术（时长 = GetPower13(30) + GetRPow(SC)*3 秒）
		s.castInvisible(p, info, um)
	case magicID == 31: // 魔法盾：按等级减伤（0 级 20%，每级 +10%）
		s.addBuff(p, entity.BuffMagicShield, 20+10*um.Level)
	case magicID == 14, magicID == 15:
		// 幽灵盾 / 神圣战甲（Magic.pas:452-461）：两者公式**完全一样**，
		// 只是 MagMakeDefenceArea 的最后一个参数不同（1=魔防 / 0=物防）。
		//   nPower := GetAttackPower(GetPower13(60) + LoWord(SC) * 10,
		//                          (HiWord(SC) - LoWord(SC)) + 1)
		sc := playerPowerAttr(p, magic.PowerAttrSC)
		nPower := magic.AttackPower(magic.GetPower13(60, info, um.Level)+int(proto.UnpackLo(sc))*10,
			int(proto.UnpackHi(sc))-int(proto.UnpackLo(sc))+1)
		if magicID == 14 {
			s.addBuff(p, entity.BuffSoulShield, uint32(max(0, nPower)))
		} else {
			s.addBuff(p, entity.BuffHolyArmor, uint32(max(0, nPower)))
		}
	case magicID == magicAmyOunsul: // 施毒术(6)：消耗毒药（绿毒 Shape=1 / 红毒 Shape=2）
		s.castPoison(c, p, info, um, targX, targY, targID, amuletShape)
	case magicID == magicShowHP: // 心灵启示(28)：给目标开血条（SM_OPENHEALTH）
		s.castShowHP(c, p, info, um, targX, targY, targID)
	case slaveSummonSpecs[magicID].monName != "":
		s.castSummon(c, p, magicID, um.Level)
	default:
		// 其余技能尚未实现（施毒术 6 / 心灵启示 28 在 2026-10-05 接上了）
		log.Printf("%s 释放了未实现的技能 %s(id=%d)", p.Char.Name, info.Name, magicID)
	}

	// 4. 修炼：每次成功释放累积修炼点
	s.trainMagic(c, p, info, um)

	// 5. MP 变化同步
	s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)
}

// castDamageSpell 远程伤害技能。
//
// 完整对齐原版链路（伤害技能的 case 在 Magic.pas:280-600，结算在 ObjBase.pas:4565-4590）：
//
//	nPower := GetAttackPower(GetPower(MPow(UserMagic)) + LoWord(attr),
//	                         (HiWord(attr) - LoWord(attr)) + 1)      ← 攻击方算一次
//	// 目标侧 RM_DELAYMAGIC：
//	if Target.GetMagStruckDamage(Attacker, nPower) > 0 then begin       ← 门禁：吃 MAC
//	   if Target 是动物 then nPower := Round(nPower / 1.2);             ← 动物打 0.83 折
//	   if 目标仍在范围内 then Target.SendMsg(RM_MAGSTRUCK, nPower)      ← 真正扣血
//	 // RM_MAGSTRUCK 里再 GetMagStruckDamage(nil, nPower) 一次（这次才是实扣）
//
// ⚠️ 三个别错的地方：减的是 **MAC** 不是 AC；**没有保底 1**（打不动就是 0）；
// 折扣在门禁**之后**，所以"能否命中"按打折前的值判。
func (s *Server) castDamageSpell(c net.Conn, p *Player, info *data.MagicInfo,
	um *pb.UserMagic, targX, targY int, targID uint32) {

	nPower := magic.SpellRawPower(info, um.Level, playerPowerAttr(p, magic.AttrOf(info.MagicID)), 1)

	s.mu.Lock()
	// 目标定位：**坐标优先**。
	//
	// ⚠️ 不能用 targID 优先：CM_SPELL 的 Series 只有 16 位，装不下
	// 1000000+ 的怪物 ActorId（1002627 会被截断成 19587），
	// 按 ID 查会打错怪或整轮轮空。坐标（地图 ≤ 1000）是客户端与服务端
	// 都无损的字段，且与玩家屏幕上看到的目标一致。targID 仅作兜底。
	//
	// ⚠️ 必须内联查找，不能调用 s.monsterAt：它内部会再加 RLock，
	// 而 sync.RWMutex **不可重入**，持 Lock 时再 RLock 会死锁
	// （表现为施法后服务静默卡住，且不会 panic，极难排查）。
	// 玩家目标优先（PvP）：伤害类技能既能打怪也能打人，原版走同一条
	// IsProperTarget 判定（ObjBase.pas:21976）。
	var pTarget *Player
	if spellHitsPlayers(info) {
		for _, o := range s.world.players {
			if o != p && o.Obj != nil && o.Obj.MapRef() == p.Obj.MapRef() &&
				o.Obj.PosX() == targX && o.Obj.PosY() == targY {
				pTarget = o
				break
			}
		}
	}
	if pTarget != nil {
		// 玩家目标：解锁后统一走 PvP 伤害路径（IsProperTarget 判定在里面）。
		s.mu.Unlock()
		s.spellHitPlayer(c, p, pTarget, nPower, info)
		return
	}
	var target *entity.Monster
	for _, mon := range s.world.monsters {
		if !mon.IsDead() && mon.MapRef() == p.Obj.MapRef() && mon.PosX() == targX && mon.PosY() == targY {
			target = mon
			break
		}
	}
	if target == nil && targID != 0 {
		if m := s.world.monsters[targID]; m != nil && !m.IsDead() {
			target = m
		}
	}
	if target == nil {
		s.mu.Unlock()
		log.Printf("%s 施法 %s 未找到目标: 坐标(%d,%d) targID=%d",
			p.Char.Name, info.Name, targX, targY, targID)
		return
	}
	// 记下"主人在打谁"：宠物据此跟打同一只（原版 m_TargetCret / m_ExpHitter）。
	// ⚠️ 必须放在下面的 MAC 门禁**之前**：原版 `SetTargetCreat` 是在**攻击动作**里
	// 设的，跟"这一下打不打得动"无关（打不动也记住了目标）。
	p.combatTargetID = target.ID

	// 门禁：原版用未打折的 nPower 判"打不打得动"（GetMagStruckDamage > 0），
	// 打折与实扣都在其后（ObjBase.pas:4572-4578）。
	if magic.MagStruckDamage(uint32(target.Info.MAC), nPower) <= 0 {
		s.mu.Unlock()
		log.Printf("%s 的 %s 对 %s 无效（威力 %d 不足以破 MAC %d）",
			p.Char.Name, info.Name, target.Name, nPower, target.Info.MAC)
		return
	}
	if magic.IsAnimalTarget(target) {
		nPower = magic.MagAnimalDiscount(nPower)
	}
	// 雷电术(11) 打不死系 ×1.5（Magic.pas:401）
	if info.MagicID == 11 && target.Info.Undead != 0 {
		nPower = magic.MagUndeadBonus(nPower)
	}
	dmg := uint32(magic.MagStruckDamage(uint32(target.Info.MAC), nPower))
	// 红毒：法术伤害同样放大（原版 StruckDamage 不分物理/魔法）
	dmg = s.struckMonster(target, dmg, time.Now())
	// 打了城堡单位 ⇒ 进 2 分钟仇恨窗口（原版 TGuardUnit.Struck，见 guard.go）
	s.markCastleAggro(p.Obj, target, time.Now())
	// ⚠️ 判定 + 扣减同锁（原来在这里自己比 `target.HP`，与别的攻击者并发会丢伤害）
	_, hp, died := target.Hurt(dmg)
	maxHP, exp := target.MaxHP, uint32(target.Info.Exp)
	tName, tID := target.Name, target.ID
	tMap, tx, ty := target.MapRef(), target.PosX(), target.PosY()
	s.mu.Unlock()

	// 飞行特效（SM_MAGICFIRE）：Recog=施法者, Param=x, Tag=y, Series=技能号
	s.broadcastToViewers(tMap, tx, ty, func(other *Player) {
		s.send(other.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID), uint16(tx), uint16(ty), uint16(info.MagicID), "")
	})
	// 伤害表现
	s.broadcastToViewers(tMap, tx, ty, func(other *Player) {
		s.sendStruck(other, p.Obj.ID, tID, hp, maxHP, dmg)
	})

	if !died {
		log.Printf("%s 用 %s 命中 %s 伤害=%d (HP %d/%d)", p.Char.Name, info.Name, tName, dmg, hp, maxHP)
		return
	}
	s.sendDeathTo(p, tID, tx, ty, 0, p.Obj.ID)
	// 经验与升级统一走 grantExp（与打怪、脚本 GIVEEXP 同一条路径）
	s.grantExp(p, uint64(exp))
	log.Printf("%s 用 %s 击杀 %s 伤害=%d 经验+%d", p.Char.Name, info.Name, tName, dmg, exp)
	// 同上：金币撒地上（击杀者是归属人）。
	s.scatterKillGold(target, p.Obj.ID)
}

// castHealSpell 治愈术：给自己回血。
func (s *Server) castHealSpell(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic) {
	// 原版 Magic.pas:308：SC ×2，且 spread 是 (Hi-Lo)*2+1（与伤害技能不同）。
	heal := uint32(max(0, magic.HealRawPower(info, um.Level, playerPowerAttr(p, magic.PowerAttrSC))))
	p.addHP(int64(heal)) // 治愈：夹到上限（持锁）
	// 治愈术也要有特效，否则客户端看不到自己在施法
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		s.send(other.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(p.Obj.PosX()), uint16(p.Obj.PosY()), uint16(info.MagicID), "")
	})
	log.Printf("%s 用 %s 回复 %d HP (%d/%d)", p.Char.Name, info.Name, heal, p.hp(), p.maxHP())
}

// needLevelFor 返回"把技能从 skillLevel 升到 skillLevel+1"所需的角色等级。
//
// 数据来源：Delphi `TMagic.TrainLevel[0..3]`（Grobal2.pas:641，注释"升级需要的等级"），
// 由 LocalDB.pas:374-377 从 SQL 的 NeedL1/NeedL2/NeedL3 装载：
//
//	TrainLevel[0] := NeedL1;  [1] := NeedL2;  [2] := NeedL3;  [3] := NeedL3
//
// 索引是**当前技能等级**（不是目标等级），与原版
// `MagicInfo.TrainLevel[UserMagic.btLevel] <= m_Abil.Level`（Magic.pas:724）一致。
func needLevelFor(info *data.MagicInfo, skillLevel int) int {
	if info == nil {
		return 0
	}
	switch skillLevel {
	case 0:
		return int(info.NeedL1)
	case 1:
		return int(info.NeedL2)
	case 2:
		return int(info.NeedL3)
	}
	return 0
}

// trainNeed 返回当前技能等级升到下一级所需的修炼点。
//
// 对应 Delphi `MaxTrain[0..3]`（Grobal2.pas:642 "锻炼"），装载见 LocalDB.pas:378-381。
func trainNeed(info *data.MagicInfo, skillLevel int) int32 {
	if info == nil {
		return 0
	}
	switch skillLevel {
	case 0:
		return int32(info.L1Train)
	case 1:
		return int32(info.L2Train)
	case 2:
		return int32(info.L3Train)
	}
	return 0
}

// trainStep 推进一次技能修炼，返回 (是否积攒, 是否升级)。
//
// 完整对齐原版两处：
//
//	DoSpell 末尾（Magic.pas:723-730）
//	  if (btLevel < 3) and (boTrain) then
//	    if MagicInfo.TrainLevel[btLevel] <= m_Abil.Level then   ← 等级门槛
//	      TrainSkill(UserMagic, Random(3) + 1);                 ← 增量是**随机 1~3**
//	CheckMagicLevelup（ObjBase.pas:21825）
//	  if MagicInfo.MaxTrain[n10] <= nTranPoint then
//	    Dec(nTranPoint, MaxTrain[n10]);                        ← **保留余量，不是清零**
//	    Inc(btLevel);
//
// 此前我们把增量固定成 +1、升级后清零、且**完全不校验等级**——三处都与原版不符。
// 等级不足时原版连 SM_MAGIC_LVEXP 都不发（整段 if 不进入），故 trained=false。
func trainStep(info *data.MagicInfo, um *pb.UserMagic, playerLevel uint32) (trained, leveled bool) {
	return trainStepN(info, um, playerLevel, 1+rand.IntN(3))
}

// trainStepN 与 trainStep 相同，但增量由调用方给定。
//
// 原版各处的增量**不一样**，别一律用 Random(3)+1：
//
//	DoSpell 末尾（Magic.pas:723）：`TrainSkill(UserMagic, Random(3) + 1)`
//	攻杀剑术消费充能（ObjBase.pas:22299）：`Random(3) + 1`
//	烈火/刺杀/半月（ObjBase.pas:22355、22363…）：固定 `1`
func trainStepN(info *data.MagicInfo, um *pb.UserMagic, playerLevel uint32, points int) (trained, leveled bool) {
	if info == nil || um == nil || um.Level >= entity.MagicMaxLevel || points <= 0 {
		return false, false
	}
	lv := int(um.Level)
	if int(playerLevel) < needLevelFor(info, lv) {
		return false, false
	}
	um.TranPoint += int32(points) // TrainSkill(UserMagic, points)
	need := trainNeed(info, lv)
	if need <= 0 || um.TranPoint < need {
		return true, false
	}
	um.TranPoint -= need // 保留余量：攒 250 点升到 1 级后还剩 50 点
	um.Level++
	return true, true
}

// trainMagic 累积修炼点，达到阈值则升级并下发 SM_MAGIC_LVEXP。
func (s *Server) trainMagic(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic) {
	s.trainMagicN(c, p, info, um, 1+rand.IntN(3))
}

// trainMagicN 是按**指定增量**练技的版本（战士近战系的消费分支用它）。
func (s *Server) trainMagicN(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic, points int) {
	// 技巧项链（Shape 120 ⇒ `m_boFastTrain`）：修炼点 ×3
	//（原版 TBaseObject.TrainSkill，ObjBase.pas:21817-21823 `if m_boFastTrain then
	// nTranPoint := nTranPoint * 3;`）。
	if p != nil && p.equipSpecials.fastTrain {
		points *= 3
	}
	trained, leveled := trainStepN(info, um, p.level(), points)
	if !trained {
		// 等级不足或已满级：原版此时不发任何包
		return
	}
	if !leveled {
		s.send(c, proto.SM_MAGIC_LVEXP, int32(um.MagicId), uint16(um.Level),
			uint16(um.TranPoint), 0, "")
		return
	}
	s.send(c, proto.SM_MAGIC_LVEXP, int32(um.MagicId), uint16(um.Level), 0, 0, "")
	log.Printf("%s 的 %s 修炼到 %d 级（修炼点余 %d）", p.Char.Name, info.Name, um.Level, um.TranPoint)
	s.sendMyMagics(c, p)
}

// ---- 技能威力公式的**玩家侧**包装 ----
//
// 纯计算（MPow/GetPower/GetPower13/GetRPow/SpellRawPower…）都在 internal/magic；
// 这里只留需要读玩家技能等级/属性加成的那一层。
// playerPowerAttr 取玩家的指定攻击属性（0 表示没有该属性）。
func playerPowerAttr(p *Player, attr magic.PowerAttr) uint32 {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return 0
	}
	switch attr {
	case magic.PowerAttrMC:
		if m := p.Char.Data.Abil.Mc; m != nil {
			return proto.PackMinMax(uint16(m.Min), uint16(m.Max))
		}
	case magic.PowerAttrSC:
		if v := p.Char.Data.Abil.Sc; v != nil {
			return proto.PackMinMax(uint16(v.Min), uint16(v.Max))
		}
	}
	return 0
}

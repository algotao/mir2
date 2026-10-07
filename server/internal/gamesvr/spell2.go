// NPC 技能的第二批：圣言术(32) / 瞬息移动(21) / 群体治愈术(29) / 困魔咒(16)。
//
// 与第一批（spell.go 的伤害/治疗/增益/召唤、火墙）分开，因为这几个的共同点是
// **都不是普通伤害**：有即死、有传送、有范围友方判定、有定身。
//
// ⚠️ Delphi 源码是 GBK，grep 前先 `iconv -f GBK -t UTF-8`，否则静默 0 命中
// （见 docs/HANDOFF.md 坑 35）。
//
// 原版依据：
//
//	【圣言术 32】Magic.pas:572 → MagTurnUndead(Magic.pas:901)
//	  if Target.m_boSuperMan or not (Target.m_btLifeAttrib = LA_UNDEAD) then exit;
//	      ↑ 只打不死系（LA_UNDEAD，Grobal2.pas:1320）
//	  TAnimalObject(Target).Struck(BaseObject);   ← 目标立刻反咬一口
//	  if Target.m_TargetCret = nil then begin
//	    Target.m_boRunAwayMode := True;  Target.m_dwRunAwayTime := 10 * 1000;
//	  end;                                   ← 目标转逃跑模式 10 秒
//	  BaseObject.SetTargetCreat(Target);        ← 施法者锁定它
//	  if (Random(2) + (施法者等级-1)) > 目标等级 then        ← 等级压制
//	    if 目标等级 < g_Config.nMagTurnUndeadLevel then       ← 默认 50（M2Share.pas:2080）
//	      if Random(100) < (技能等级 shl 3) - 技能等级 + 15 + (施法者等级-目标等级) then
//	        Target.m_WAbil.HP := 0;                            ← **直接秒**
//
//	【瞬息移动 21】Magic.pas:495 → MagSaceMove(Magic.pas:951)
//	  if Random(11) < 技能等级*2 + 4 then begin
//	    SendRefMsg(RM_SPACEMOVE_FIRE2,...);
//	    BaseObject.MapRandomMove(BaseObject.m_sHomeMap, 1);   ← 传送到**自己家的地图**
//	    if 换图了 then m_boTimeRecall := False;               ← 解除"时=_ 回城"标记
//	  end
//	  ⚠️ 不是"随机瞬移"，是"随机传回家"。传送目标地图取 m_sHomeMap。
//
//	【群体治愈 29】Magic.pas:532 → MagBigHealing(Magic.pas:172)
//	  半径 1（3×3）；对 IsProperFriend 的目标（且 HP<MaxHP）发 RM_MAGHEALING
//
//	【困魔咒 16】Magic.pas:459 → MagMakeHolyCurtain(Magic.pas:1217)
//	  半径 1；条件：种族>=RC_ANIMAL 且 (Random(4)+施法者等级-1) > 怪等级
//	        且 **怪没有主人**（m_Master = nil，不是别人的召唤兽）
//	  OpenHolySeizeMode(nPower * 1000)   ← 定身 nPower 秒
//	  另外铺 8 个 THolyCurtainEvent（纯视觉/存在标记）
//
//	未实现的部分（明确记录，不自创）：
//	  - 圣言术的**逃跑模式 10 秒**（m_boRunAwayMode）：没有接入怪物 AI 的逃跑分支；
//	    "立刻反咬一口"和"即死"都做了，这两个才是可观察的核心行为。
//	  - 困魔咒的 8 个 THolyCurtainEvent 与"名字变褐色 $7D"（RefNameColor）。
//	    事件本来就不下发客户端（Envir.pas:236 的 OS_EVENTOBJECT 分支是空的），
//	    放进去没有任何可观察效果，故不做。
//	  - 困魔咒/圣言术在原版属 case 13-19 那一组，**需要护身符**
//	    （CheckAmulet：U_ARMRINGL 槽 + StdMode=25 + Shape 1/2 + 消耗数量）。
//	    本项目尚无护身符系统（幽灵盾/神圣战甲/召唤骷髅同样没做），保持一致。
package gamesvr

import (
	"log"
	"math/rand/v2"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// killUndeadMaxLevel 是圣言术可即死的怪物等级上限
// （g_Config.nMagTurnUndeadLevel，出厂 50，M2Share.pas:2080）。
const killUndeadMaxLevel = 50

// castKillUndead 圣言术(32)：对不死系怪物的即死技能。
func (s *Server) castKillUndead(c net.Conn, p *Player, info *data.MagicInfo,
	um *pb.UserMagic, targX, targY int, targID uint32) {

	s.mu.Lock()
	mon := s.findMonsterAt(p.Obj.MapRef(), targX, targY, targID)
	s.mu.Unlock()
	if mon == nil {
		// 这个分支原先**完全静默**：客户端只看到 SM_MAGICFIRE_FAIL，
		// 服务端一条日志都没有，排查时无法区分"目标没了"与"等级压制失败"。
		// e2e 的 skill2-undead 曾偶发找不到"圣言术命中"，就是被这里吞掉的。
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的圣言术落空：(%d,%d) 附近没有目标（targID=%d）",
			p.Char.Name, targX, targY, targID)
		return
	}

	// 只打不死系：m_btLifeAttrib = LA_UNDEAD（数据字段 monsters.undead）
	if mon.Info == nil || mon.Info.Undead == 0 {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的圣言术对 %s 无效（非不死系，undead=%d）",
			p.Char.Name, mon.Name, undeadFlag(mon))
		return
	}

	// 等级压制：Random(2) + (施法者等级-1) > 目标等级（Magic.pas:911）
	plLevel := int(p.level())
	monLevel := 0
	if mon.Info != nil {
		monLevel = int(mon.Info.Level)
	}
	if !turnUndeadLevelPass(plLevel, monLevel) {
		// 等级不够：原版 Result 保持 False（不涨修炼点）
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的圣言术对 %s(Lv%d) 等级压制失败（施法者 Lv%d）",
			p.Char.Name, mon.Name, monLevel, plLevel)
		return
	}

	// 落点特效（与其它伤害技能同一条 SM_MAGICFIRE）
	s.broadcastToViewers(mon.MapRef(), mon.PosX(), mon.PosY(), func(o *Player) {
		s.send(o.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(mon.PosX()), uint16(mon.PosY()), uint16(info.MagicID), "")
	})

	// ① 目标立刻反咬一口（TAnimalObject.Struck）——原版无条件执行
	s.mu.Lock()
	monID := mon.ID
	var counterHit *monsterHit
	if dist := mon.Distance(p.Obj.PosX(), p.Obj.PosY()); dist <= 1 && s.cfg.aggro {
		h := s.monsterStrike(mon, p)
		counterHit = &h
	}
	s.mu.Unlock()
	if counterHit != nil {
		s.applyMonsterHit(*counterHit)
	}
	// 施法者锁定该目标（SetTargetCreat）
	s.setSpellTarget(p, monID)

	// ② 即死判定：Random(100) < 技能等级*7 + 15 + (施法者等级-目标等级)
	//    且 目标等级 < killUndeadMaxLevel
	skillLevel := int(um.Level)
	chance := killUndeadChance(skillLevel, plLevel, monLevel)
	// 上限 100：原版是 Random(100) < chance，chance>100 时必中，无需特判
	roll, smite := killUndeadRoll(chance, monLevel)

	log.Printf("%s 圣言术命中 %s(Lv%d, 不死系) 技能等级=%d 概率=%d 掷骰=%d 即死=%v",
		p.Char.Name, mon.Name, monLevel, skillLevel, chance, roll, smite)

	if !smite {
		// 未即死：本次仍算释放成功（原版只在即死时 Result:=True，
		// 这里保守：让修炼点照常累积，与"命中"语义一致）
		obs.Event("kill_undead", "player", p.Char.Name, "monster", mon.Name,
			"monster_id", monID, "smite", false, "chance", chance, "roll", roll)
		return
	}

	// ③ 即死：HP 归零 + 走统一的死亡收尾
	s.mu.Lock()
	mon.Kill()
	s.keepCorpseOrRemove(mon)
	s.mu.Unlock()

	s.broadcastToViewers(mon.MapRef(), mon.PosX(), mon.PosY(), func(o *Player) {
		if o.visible.Contains(monID) {
			// 击杀者不在这条路径的签名里 ⇒ killer 传 0（见 protocol.md §11 的归因待办）
			s.sendDeathTo(o, monID, mon.PosX(), mon.PosY(), 0, 0)
		}
	})
	s.killMonsterBy(p, mon, info.MagicID, mon.PosX(), mon.PosY())
	obs.Event("kill_undead", "player", p.Char.Name, "monster", mon.Name,
		"monster_id", monID, "smite", true, "chance", chance, "roll", roll)
	log.Printf("%s 用圣言术秒掉 %s（等级差 %d，概率 %d%%）", p.Char.Name, mon.Name, plLevel-monLevel, chance)
}

func undeadFlag(m *entity.Monster) uint8 {
	if m == nil || m.Info == nil {
		return 0
	}
	return m.Info.Undead
}

// castSpaceMove 瞬息移动(21)：随机传送到**自己家的地图**。
//
// 原版 MagSaceMove 先判概率 Random(11) < 技能等级*2+4，失败则整个技能无效
// （不传送、也不涨修炼点）。注意它不是"随机瞬移"，目标地图是 m_sHomeMap。
func (s *Server) castSpaceMove(c net.Conn, p *Player, um *pb.UserMagic) {
	// 概率判定（Magic.pas:956）：Random(11) < 技能等级*2 + 4
	if !spaceMoveSucceeds(int(um.Level)) {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 原版换图后会清 m_boTimeRecall（"停留在指定时刻"标记）；
	// 我们没有这个机制，切图本身已经重置了状态。
	home := p.Char.Data.HomeMap
	if home == "" {
		home = p.Obj.MapRef().Name
	}
	// 随机落点：先取出生点，再在附近找一个可走格
	mp, err := s.world.maps.Get(home)
	if err != nil {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的瞬息移动失败：取不到地图 %s（%v）", p.Char.Name, home, err)
		return
	}
	x, y := 0, 0
	if sp := s.startPointOf(home); sp != nil {
		x, y = sp.X, sp.Y
	} else {
		x, y = mp.Width()/2, mp.Height()/2
	}
	// 原版是"随机落点"（MapRandomMove），我们随机找附近可走格；
	// 找不到就退回确定性的螺旋搜索（与 switchMap 同一套兜底）。
	if rx, ry, ok := randomWalkableNear(mp, x, y, 12); ok {
		x, y = rx, ry
	} else if rx, ry, ok := nearestWalkable(mp, x, y, 12); ok {
		x, y = rx, ry
	}
	if err := s.switchMap(c, p, home, x, y); err != nil {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的瞬息移动失败：切图 %s 失败（%v）", p.Char.Name, home, err)
		return
	}
	obs.Event("space_move", "player", p.Char.Name, "map", home, "x", x, "y", y)
	log.Printf("%s 用瞬息移动回到 %s(%d,%d)", p.Char.Name, home, x, y)
}

// castGroupHeal 群体治愈术(29)：半径 1（3×3）对友方回血。
//
// 原版 MagBigHealing(Magic.pas:172)：对 IsProperFriend 且 HP<MaxHP 的目标
// 发 RM_MAGHEALING（延迟 800ms 生效，我们直接生效）。
func (s *Server) castGroupHeal(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic, targX, targY int) {
	// 原版 Magic.pas:533：与治愈术同一个公式（SC×2，spread=(Hi-Lo)*2+1）。
	healMax := uint32(max(0, magic.HealRawPower(info, um.Level, playerPowerAttr(p, magic.PowerAttrSC))))

	// 特效（原版靠 RM_MAGHEALING 的表现，这里给一个落点特效）
	s.broadcastToViewers(p.Obj.MapRef(), targX, targY, func(o *Player) {
		s.send(o.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(targX), uint16(targY), uint16(info.MagicID), "")
	})

	type healHit struct {
		victim        *Player
		hp, maxHP, mp uint32
		healed        uint32
	}
	var hits []healHit

	now := time.Now()
	s.mu.Lock()
	for _, other := range s.world.players {
		if other.Obj == nil || other.Obj.MapRef() != p.Obj.MapRef() {
			continue
		}
		// 半径 1（切比雪夫）：原版 GetMapBaseObjects(...,1) 是 3×3
		if other.Obj.Distance(targX, targY) > 1 {
			continue
		}
		if other.Char == nil || other.Char.Data == nil || other.Char.Data.Abil == nil {
			continue
		}
		if other.hp() >= other.maxHP() {
			continue // 原版：HP<MaxHP 才回
		}
		// IsProperFriend：原版 HAM_ALL/HAM_PEACE 下"所有人都不敌对"⇒都算友方
		if !s.isProperFriend(p, other, now) {
			continue
		}
		// ⚠️ 群体治愈打的是**别人**（这条跑在施法者 goroutine 上，而对方自己的
		// goroutine 同时在吃药/挨打）⇒ "还能回多少 + 加血"必须在同一次持锁里做。
		var healed, hp, maxHP, mp uint32
		other.withAbil(func(ab *pb.Ability) {
			healed = healMax
			if healed > ab.MaxHp-ab.Hp {
				healed = ab.MaxHp - ab.Hp
			}
			ab.Hp += healed
			hp, maxHP, mp = ab.Hp, ab.MaxHp, ab.Mp
		})
		hits = append(hits, healHit{victim: other, hp: hp, maxHP: maxHP, mp: mp, healed: healed})
	}
	s.mu.Unlock()

	if len(hits) == 0 {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的群体治愈无人可治（范围 (%d,%d)）", p.Char.Name, targX, targY)
		return
	}
	for _, h := range hits {
		s.sendHealthChanged(h.victim, h.victim.Obj.ID, h.hp, h.mp, h.maxHP)
		log.Printf("%s 的群体治愈：%s 回复 %d（%d/%d）",
			p.Char.Name, h.victim.Char.Name, h.healed, h.hp, h.maxHP)
	}
	obs.Event("group_heal", "caster", p.Char.Name, "heal", healMax, "targets", len(hits))
}

// isProperFriend 友方判定（对应 ObjBase.pas:24091 IsProperFriend）。
//
// 原版开头就是：
//
//	case m_btAttatckMode of
//	  HAM_ALL:   Result := True;
//	  HAM_PEACE: Result := True;
//	  ...
//
// 也就是说"全体攻击"与"和平"模式下**所有人都不敌对**⇒都算友方
// （群体治疗会给敌人也加血，这是原版行为，不是 bug）。
// 其它模式（行会/编组/只打红名）才看关系。
func (s *Server) isProperFriend(self, other *Player, now time.Time) bool {
	if self == other {
		return true
	}
	switch attackModeOf(self) {
	case pvp.HamAll, pvp.HamPeace:
		return true
	}
	// 关系判定：同门会 / 友盟 / 自己人算友方
	ag := s.social.guilds.Find(s.guildNameOf(self))
	tg := s.social.guilds.Find(s.guildNameOf(other))
	same, ally, _ := pvp.Relations(ag, tg, false)
	return same || ally
}

// castHolyCurtain 困魔咒(16)：半径 1 定身怪物。
//
// 原版 MagMakeHolyCurtain(Magic.pas:1217)：
//   - 目标格必须可走
//   - 半径 1 内的怪：种族>=RC_ANIMAL 且 (Random(4)+施法者等级-1) > 怪等级
//     且 **怪没有主人**（不是别人的召唤兽）
//   - OpenHolySeizeMode(nPower * 1000) 定身
//
// nPower 来自 GetPower13(40) + 道攻上限×3。
func (s *Server) castHolyCurtain(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic, targX, targY int) {
	m := p.Obj.MapRef()
	if m == nil || !m.CanWalk(targX, targY) {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 定身时长：GetPower13(40) + GetRPow(SC)*3（Magic.pas:462），单位秒。
	seconds := holyCurtainSeconds(magic.GetPower13(40, info, um.Level), playerPowerAttr(p, magic.PowerAttrSC))

	s.broadcastToViewers(m, targX, targY, func(o *Player) {
		s.send(o.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(targX), uint16(targY), uint16(info.MagicID), "")
	})

	plLevel := int(p.level())
	type seizeHit struct {
		id   uint32
		name string
	}
	var seized []seizeHit

	s.mu.Lock()
	for _, mon := range s.world.monsters {
		if mon.IsDead() || mon.IsNPC || mon.MapRef() != m {
			continue
		}
		if mon.Distance(targX, targY) > 1 {
			continue
		}
		// 别人的召唤兽定不住（原版 m_Master = nil）
		if mon.MasterID != 0 {
			continue
		}
		monLevel := 0
		if mon.Info != nil {
			monLevel = int(mon.Info.Level)
		}
		// 等级压制：Random(4) + (施法者等级-1) > 怪等级
		if !canPassCurtain(delphi.Random(4), plLevel, monLevel) {
			continue
		}
		mon.Seize(time.Duration(seconds) * time.Second)
		seized = append(seized, seizeHit{id: mon.ID, name: mon.Name})
	}
	s.mu.Unlock()

	if len(seized) == 0 {
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		log.Printf("%s 的困魔咒没定住任何东西（范围 (%d,%d)）", p.Char.Name, targX, targY)
		return
	}
	for _, h := range seized {
		log.Printf("%s 的困魔咒定住 %s（ActorId=%d，%d 秒）", p.Char.Name, h.name, h.id, seconds)
	}
	// 可见的光幕：围着目标铺 8 格 ET_HOLYCURTAIN（原版只在"至少定住一个"时铺，
	// 见 holyCurtainCells 的注释）。⚠️ 必须在 s.mu 之外——这里已经解锁了。
	placed := s.placeHolyCurtain(m, targX, targY, time.Duration(seconds)*time.Second)
	log.Printf("%s 的困魔咒铺下 %d 格光幕（%d 秒）", p.Char.Name, placed, seconds)
	obs.Event("holy_curtain", "player", p.Char.Name, "x", targX, "y", targY,
		"targets", len(seized), "seconds", seconds)
}

// ---------- 公共辅助 ----------

// killUndeadChance 圣言术的即死概率（百分数）。
//
// 原版 Magic.pas:918：
//
//	Random(100) < (技能等级 shl 3) - 技能等级 + 15 + (施法者等级 - 目标等级)
//
// 化简就是 `技能等级*7 + 15 + 等级差`。返回值可能 >100（原版同样是
// `Random(100) < chance`，必然命中，无需特判）。
func killUndeadChance(skillLevel, playerLevel, monLevel int) int {
	return skillLevel*7 + 15 + (playerLevel - monLevel)
}

// turnUndeadLevelPass 抽一次 Random(2) 判定圣言术的等级压制。
//
// 原版 Magic.pas:911：`(Random(2) + (施法者等级-1)) > 目标等级`。
// ⚠️ 抽签必须是 `Random(2)`（[0,2) = {0,1}）——写成 `1+Random(2)` 会把
// 门槛整体抬高 1，成功率凭空偏高一档，见 docs/HANDOFF.md 坑 58/74。
func turnUndeadLevelPass(playerLevel, monLevel int) bool {
	return delphi.Random(2)+(playerLevel-1) > monLevel
}

// killUndeadRoll 抽一次 Random(100) 判定圣言术即死，返回掷骰值与是否即死。
//
// 原版 Magic.pas:916-918：
//
//	目标等级 < nMagTurnUndeadLevel 且 Random(100) < (技能等级*7 + 15 + 等级差)
//
// ⚠️ 同样不能写成 `1+Random(100)`：那会让 0..99 变成 1..100，
// 概率整体少 1%。
func killUndeadRoll(chance, monLevel int) (int, bool) {
	roll := delphi.Random(100)
	return roll, killUndeadLevelOK(monLevel) && roll < chance
}

// killUndeadLevelOK 目标等级是否在圣言术的可即死范围内
// （g_Config.nMagTurnUndeadLevel，出厂 50）。
func killUndeadLevelOK(monLevel int) bool { return monLevel < killUndeadMaxLevel }

// spaceMoveSucceeds 抽一次 Random(11) 判定瞬息移动是否发动。
//
// 原版 Magic.pas:956：`if Random(11) < nLevel * 2 + 4 then`。
// 0 级 = 4/11 ≈ 36%；写 `1+Random(11)` 会变成 3/11 ≈ 27%（坑 58/74）。
func spaceMoveSucceeds(magicLevel int) bool {
	return delphi.Random(11) < magicLevel*2+4
}

// holyCurtainSeconds 困魔咒的定身时长（秒）。
//
// 原版 Magic.pas:462：
//
//	MagMakeHolyCurtain(PlayObject, GetPower13(40) + GetRPow(SC) * 3, x, y)
//	// 内部 OpenHolySeizeMode(nPower * 1000) ⇒ nPower 就是秒数
//
// ⚠️ 用的是 **GetRPow(SC)**（闭区间 [Lo,Hi]）而不是 SC 上限，也不是 GetPower13
// 的完整和——把 GetRPow 换成"固定取上限"会让高道击玩家定身时间明显变长。
func holyCurtainSeconds(power13 int, sc uint32) int {
	return max(0, power13+magic.GetRPow(sc)*3)
}

// canPassCurtain 困魔咒的等级压制判定（Magic.pas:1234）：
// `Random(4) + (施法者等级-1) > 目标等级`。
func canPassCurtain(roll, playerLevel, monLevel int) bool {
	return roll+(playerLevel-1) > monLevel
}

// killMonsterBy 怪物死亡的统一收尾：经验 + 掉落 + 金币通知。
//
// ⚠️ 调用方**必须**已经把怪从 s.world.monsters / monsterIdx 移除并把 HP 归零，
// 这里只做"结算与通知"。广播 SM_DEATH 由调用方按可见范围发（因为要带上
// 死亡时刻的坐标与技能号）。
func (s *Server) killMonsterBy(killer *Player, mon *entity.Monster, magicID uint16, x, y int) {
	if killer == nil || mon == nil || mon.Info == nil {
		return
	}
	s.grantExp(killer, uint64(mon.Info.Exp))
	s.scatterKillGold(mon, killer.Obj.ID)
}

// findMonsterAt 按坐标（优先）或 ActorId 找一只怪。**调用方持 s.mu**。
//
// 坐标优先的理由与 castDamageSpell 相同：CM_SPELL 的 Series 只有 16 位，
// 装不下 1000000+ 的怪物 ActorId。
func (s *Server) findMonsterAt(m *world.Map, targX, targY int, targID uint32) *entity.Monster {
	for _, mon := range s.world.monsters {
		if !mon.IsDead() && mon.MapRef() == m && mon.PosX() == targX && mon.PosY() == targY {
			return mon
		}
	}
	if targID != 0 {
		if mon := s.world.monsters[targID]; mon != nil && !mon.IsDead() {
			return mon
		}
	}
	return nil
}

// setSpellTarget 让施法者锁定一个目标（SetTargetCreat）。
func (s *Server) setSpellTarget(p *Player, targetID uint32) {
	if p == nil {
		return
	}
	s.mu.Lock()
	p.spellTargetID = targetID
	s.mu.Unlock()
}

// randomWalkableNear 在 (x,y) 附近找一个可走格，最多试 radius×radius×4 次。
func randomWalkableNear(m *world.Map, x, y, radius int) (int, int, bool) {
	if m == nil {
		return 0, 0, false
	}
	if m.CanWalk(x, y) {
		return x, y, true
	}
	for i := 0; i < radius*radius*4; i++ {
		dx := rand.IntN(2*radius+1) - radius
		dy := rand.IntN(2*radius+1) - radius
		if m.CanWalk(x+dx, y+dy) {
			return x + dx, y + dy, true
		}
	}
	return 0, 0, false
}

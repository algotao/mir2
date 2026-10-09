package gamesvr

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
)

// 增益（Buff）系统。
//
// 原版用 TBaseObject 的多个布尔字段 + 到期 Tick 表示状态
// （m_boInvisible / m_boMagicShield / 各类 m_dwXXXTime），散落在实体里。
// 这里收敛成一张 map[buff]到期时刻，统一处理过期与状态广播。

// 增益类型（`BuffType`）与状态位（`State*`）都在 internal/entity ——
// 它们是对象自身的状态，`Object.Status` 也在那个包。

// buffDurations 各增益的持续时间。
//
// 原版按技能等级与人物等级计算，这里取固定值，待数据齐全后细化。
var buffDurations = map[entity.BuffType]time.Duration{
	entity.BuffInvisible:   60 * time.Second,
	entity.BuffMagicShield: 60 * time.Second,
	entity.BuffSoulShield:  300 * time.Second,
	entity.BuffHolyArmor:   300 * time.Second,
}

// buffState 是一个生效中的增益。
type buffState struct {
	Until time.Time
	// Bonus 是加成值：防御类为加点，魔法盾为减伤百分比。
	Bonus uint32
}

// addBuff 给玩家加一个增益并广播状态变化（时长取 buffDurations 的默认值）。
func (s *Server) addBuff(p *Player, bt entity.BuffType, bonus uint32) {
	s.addBuffFor(p, bt, bonus, 0)
}

// addBuffFor 同 addBuff，但可以**指定时长**（d <= 0 表示用默认值）。
//
// 需要它的技能：隐身术(18)/集体隐身术(19) —— 原版时长是算出来的
// （`GetPower13(30) + GetRPow(SC)*3` 秒，Magic.pas:476-483），不是固定值。
func (s *Server) addBuffFor(p *Player, bt entity.BuffType, bonus uint32, d time.Duration) {
	if p.buffs == nil {
		p.buffs = make(map[entity.BuffType]buffState)
	}
	// ⚠️ 增益持续时间**不跟随**全局倍速：它是玩法语义（隐身 30 秒就是 30 秒），
	// 缩放会让客户端在收包前就看到 buff 过期（e2e buff 用例直接失败）。
	// 倍速只作用于"节奏类"时间：移动限流、施法冷却、AI tick。
	if d <= 0 {
		var ok bool
		d, ok = buffDurations[bt]
		if !ok {
			d = 60 * time.Second
		}
	}
	p.buffs[bt] = buffState{Until: time.Now().Add(d), Bonus: bonus}
	obs.Event("buff_add", "player", p.Char.Name, "buff", int(bt), "ms_left", d.Milliseconds())
	s.broadcastStatus(p)
}

// hasBuff 判断增益是否生效。过期会自动清除。
//
// ⚠️ 有副作用（会 delete），不要在只读路径上依赖它做幂等判断。
func (p *Player) hasBuff(bt entity.BuffType) bool {
	st, ok := p.buffs[bt]
	if !ok {
		return false
	}
	if time.Now().After(st.Until) {
		delete(p.buffs, bt)
		return false
	}
	return true
}

// removeBuff 移除增益（如隐身被打断）。
func (s *Server) removeBuff(p *Player, bt entity.BuffType) {
	if _, ok := p.buffs[bt]; !ok {
		return
	}
	delete(p.buffs, bt)
	s.broadcastStatus(p)
}

// buffBonus 返回增益的加成值（无则 0），同时清理过期项。
func (p *Player) buffBonus(bt entity.BuffType) uint32 {
	if !p.hasBuff(bt) {
		return 0
	}
	return p.buffs[bt].Bonus
}

// statusBits 计算当前状态位。
//
// ⚠️ 中毒不在这里的 buff 表里（它的状态在 `Object.Poison` 上，且要按"是否到期"算），
// 但两者最终都要进同一条 SM_CHARSTATUSCHANGED 的 Series ⇒ 在这里合并，
// 等价原版的 `m_nCharStatus := GetCharStatus()`。
func (p *Player) statusBits() uint32 {
	var v uint32
	if p.hasBuff(entity.BuffInvisible) {
		v |= entity.StateInvisible
	}
	if p.hasBuff(entity.BuffMagicShield) {
		v |= entity.StateShield
	}
	if p.hasBuff(entity.BuffHolyArmor) {
		v |= entity.StateHolyArmor
	}
	if p.hasBuff(entity.BuffSoulShield) {
		v |= entity.StateSoulShield
	}
	// 隐身戒指（Shape 111 ⇒ `m_boHideMode`）：装备期间一直处于透明状态
	//（原版 :3367-3390 + :8947 `if m_boTransparent and m_boHideMode then 置 STATE_TRANSPARENT`）
	if p.equipSpecials.hideMode {
		v |= entity.StateInvisible
	}
	v |= poisonMask(p.Obj, time.Now())
	// 石化/麻痹（原版 MakePosion(POISON_STONE,…) 会改状态位并 StatusChanged 广播）
	if p.Obj.Stoned(time.Now()) {
		v |= entity.StateStone
	}
	// **红名**：PK 值到红名档（`PKLevel ≥ 2`，与 `death.go` 的掉落判据同一个口径）。
	// 客户端据此把名字画成红色（用户 2026-10-09 第 4 条：玩家一般白名、红名时红）。
	if p.Char != nil && p.Char.Data != nil && pvp.PKLevel(int32(p.Char.Data.PkPoint)) >= 2 {
		v |= entity.StateRedName
	}
	return v
}

// defenseBonus 返回增益带来的防御加成（AC / MAC）。
func (p *Player) defenseBonus() (ac, mac uint32) {
	if p.hasBuff(entity.BuffHolyArmor) {
		ac += p.buffs[entity.BuffHolyArmor].Bonus
	}
	if p.hasBuff(entity.BuffSoulShield) {
		mac += p.buffs[entity.BuffSoulShield].Bonus
	}
	return ac, mac
}

// damageReduction 返回魔法盾带来的减伤百分比（0~100）。
func (p *Player) damageReduction() uint32 {
	if !p.hasBuff(entity.BuffMagicShield) {
		return 0
	}
	r := p.buffs[entity.BuffMagicShield].Bonus
	if r > 100 {
		return 100
	}
	return r
}

// broadcastStatus 广播状态变化（SM_CHARSTATUSCHANGED=657）。
//
// ⚠️ 字段顺序**照官方**（这条以前写反了，见审计 §3.5）：
//
//	`SendUpdateMsg(Self, RM_CHARSTATUSCHANGED, m_nHitSpeed, m_nCharStatus, 0, 0, '')`
//	（ObjBase.pas:3516）—— 注意 `SendUpdateMsg(BaseObject, wIdent, **wParam**: Word,
//	lParam1, lParam2, lParam3: Integer, …)`（:400 / :19434）⇒ 第 3 个参数是
//	**wParam = 攻速**、第 4 个才是 **lParam1 = 32 位状态**。
//	翻译时按 lParam 拆字（:`5966-5974`）：`SendDefMessage(SM_CHARSTATUSCHANGED,
//	BaseObject, LoWord(nParam1), HiWord(nParam1), wParam, '')` ⇒ 最终
//	**Param = 状态低字、Tag = 状态高字、Series = 攻速**。
//	客户端就是这么拼的：`MakeLong(msg.Param, msg.Tag)` 才是状态
//	（ClMain.pas:4492；PlayScene 再把 Series 当挥砍速度）。
//
// 我们早期是 `Param = 攻速、Tag = 状态`（还把状态截成 16 位、塞在 Tag）⇒
// 真客户端拼出来的是"攻速当低位"，状态全错。
func (s *Server) broadcastStatus(p *Player) {
	st := p.statusBits()
	// TCharDesc.Status 是 32 位（Grobal2.pas:719-722）。
	// ⚠️ 这份"上次发出去的位"快照是**跨 goroutine**的：本人 goroutine 在换装/
	// 吃药/上增益时写，ticker（tickPlayerStatus）到期补播时读+比较 ⇒ 走 s.mu
	// （见 statelock.go 第二节）。
	s.setStatusBit(p, st)
	spd := uint16(int16(s.playerHitSpeed(p)))
	s.sendStatusChanged(p.conn, p.Obj.ID, st, spd)
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		if other == p {
			return
		}
		s.sendStatusChanged(other.conn, p.Obj.ID, st, spd)
	})
}

// sendStatusChanged 按官方字段顺序发一条状态变化（Param=低字、Tag=高字、Series=攻速）。
func (s *Server) sendStatusChanged(c net.Conn, id uint32, st uint32, speed uint16) {
	s.send(c, proto.SM_CHARSTATUSCHANGED, int32(id), uint16(st), uint16(st>>16), speed, "")
}

// ---------- 召唤兽 ----------

// castSummon 释放召唤技能：生成一只归属该玩家的召唤兽。
//
// 对应 ObjBase.pas:488-492 的分派 + Magic.pas:769-795 `MagMakeSlave`
// 与 :820-846 `MagMakeSinSuSlave` → `TBaseObject.MakeSlave`（ObjBase.pas:7157）。
//
// 与原版的差异（都是我们数据/能力上的，不是公式上的）：
//   - 原版按角色等级查 `SkeletonArray`/`DragonArray` 换更强的兽，
//     但 GeeM2 的 !setup.txt **没有配置这两张表** ⇒ 官方部署就是基础那只。
//   - 原版召唤兽走 `RecalcAbilitys` 按等级重算属性；我们的 Monster 属性
//     直接取 StdMonster 模板，没有"按主人等级重算"这一步。
//   - 忠诚度 10 天后判变（我们接上了，召唤的兽也会判变——原版就是如此）。
func (s *Server) castSummon(c net.Conn, p *Player, magicID uint32, magicLevel uint32) {
	spec, ok := slaveSummonSpecs[magicID]
	if !ok {
		return
	}
	info := s.data.tables.Monsters.GetByName(spec.monName)
	if info == nil {
		log.Printf("召唤失败：怪物表里没有 %s（技能 %d）", spec.monName, magicID)
		return
	}

	s.mu.Lock()
	// 找主人**前方**一格的可走格（GetFrontPosition，ObjBase.pas:7170）。
	x, y := frontPositionOf(p, info, spec.monName)
	if x < 0 {
		s.mu.Unlock()
		log.Printf("%s 召唤 %s 失败：正前方没有可站的位置", p.Char.Name, spec.monName)
		return
	}
	mon := entity.NewMonster(proto.MonsterIDBase+s.world.monsterSeq.Add(1), info, p.Obj.MapRef(), x, y)
	// 原版 MakeSlave 的完整绑定（含回一半血、10 天忠诚度、NoTame）
	if !s.bindSlave(mon, p, spec, 0, slaveRoyaltySec*time.Second, time.Now()) {
		s.mu.Unlock()
		log.Printf("%s 召唤失败：已有 %d 只%s（上限 %d）",
			p.Char.Name, s.slaveNamedCount(p, spec.monName), spec.monName, spec.maxCount)
		return
	}
	// 宠物等级（原版 MakeSlave：`m_btSlaveMakeLevel := nMakeLevel`、
	// `m_btSlaveExpLevel := nExpLevel`；召唤骷髅这两个都取技能等级，见 Magic.pas:1398-1400）
	mon.SlaveMakeLevel = uint8(magicLevel)
	mon.SlaveExpLevel = uint8(magicLevel)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)
	s.mu.Unlock()

	// 让视野内的玩家（含主人）看到它
	s.broadcastToViewers(mon.MapRef(), mon.PosX(), mon.PosY(), func(other *Player) {
		s.sendMonsterAppear(other, mon)
	})
	// 显示名带 "(主人名)"：宠物与野怪协议同形，靠这个后缀区分（showname.go）
	s.refShowName(mon)
	log.Printf("%s 召唤了 %s (ActorId=%d) 于 (%d,%d)，忠诚度 %d 天",
		p.Char.Name, spec.monName, mon.ID, x, y, slaveRoyaltySec/86400)
}

// frontPositionOf 在主人正前方找一格可走的位置（GetFrontPosition）。
// 返回 (-1,-1) 表示找不到。
//
// ⚠️ 原版 GetFrontPosition（ObjBase.pas sub_004B2790）只取**正前方那一格**，
// 站不下就失败（不会往旁边找）。我们多试几格纯属 conveniences——
// 主人被贴脸堵住时原版是召不出来的，这里放宽一格不至于让技能完全 unusable。
func frontPositionOf(p *Player, info *data.MonsterInfo, name string) (int, int) {
	m := p.Obj.MapRef()
	x, y := p.Obj.PosX(), p.Obj.PosY()
	if m.CanWalk(x, y) {
		return x, y
	}
	for _, d := range [][2]int{{0, 1}, {1, 0}, {0, -1}, {-1, 0}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}} {
		nx, ny := x+d[0], y+d[1]
		if m.CanWalk(nx, ny) {
			return nx, ny
		}
	}
	_ = info
	_ = name
	return -1, -1
}

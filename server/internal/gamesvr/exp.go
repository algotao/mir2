package gamesvr

// 经验与等级的统一入口。
//
// 此前这段"加经验 + 升级判定 + 发包"在两处内联重复
// （main.go 的打怪结算、spell.go 的技能结算），改一处忘另一处是迟早的事。
// 抽到这里后，脚本 GIVEEXP / CHANGELEVEL 也走同一条路径。
//
// 对应原版 TPlayObject.WinExp（ObjBase.pas）+ ActionOfChangeExp
//（ObjNpc.pas:2943）与 ActionOfChangeLevel（ObjNpc.pas:3053）。

import (
	"fmt"
	"log"
	"math"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
)

// groupExpAverage 对应 `HighLevelKillMonFixExp`：为真时组队经验**平均分配**，
// 否则按等级比例。ObjBase.pas:15589-15594：
//
//	if g_Config.boHighLevelKillMonFixExp then WinExp(Round(dwExp / n))
//	else WinExp(Round(dwExp / sumlv * Level));
//
// ⚠️ 取值必须来自官方 **Exps.ini** 的 `[Exp] HighLevelKillMonFixExp=1`
// （M2Share.pas:2136 出厂默认是 False，但官方部署文件写的是 1）。
// 这里此前硬编码 false，属于"按 Delphi 注释里的出厂默认实现"的老毛病
// （同坑 65：!setup/Exps 的实际值优先于源码里的默认值）。
const groupExpAverage = true

// grantExp 给玩家加经验并处理升级。返回实际升到的等级（0 = 没升）。
//
// ⚠️ 经验是**累计值**（abil.Exp），不是增量——CheckLevelUp 按累计值算新等级，
// 所以一次加很多经验可能连升多级。
func (s *Server) grantExp(p *Player, exp uint64) uint32 {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil || exp == 0 {
		return 0
	}
	newLevel := s.grantExpRaw(p, uint32(min(exp, math.MaxUint32)))
	// 组队经验共享（ObjBase.pas:15557-15603 的 GainExp 分支）。
	//
	// ⚠️ 只在**有有效队友**时才走共享；否则原版就是 WinExp(dwExp) 一人全拿。
	// 这里不做"是否共享"的判定，判定与分配都在 group.DistributeExp 里。
	s.shareGroupExp(p, uint32(min(exp, math.MaxUint32)))
	return newLevel
}

// grantExpRaw 只给一个人加经验（不发 SM_WINEXP 之外的东西）。
func (s *Server) grantExpRaw(p *Player, exp uint32) uint32 {
	abil := p.Char.Data.Abil
	abil.Exp += uint64(exp)
	s.send(p.conn, proto.SM_WINEXP, int32(exp), 0, 0, 0, "")

	res := entity.CheckLevelUp(uint32(abil.Level), abil.Exp)
	if res.Gained <= 0 {
		return 0
	}
	s.applyLevelUp(p, res)
	return res.NewLevel
}

// shareGroupExp 把这次击杀的经验按原版规则分给同组的有效成员。
//
// 对应 ObjBase.pas:15564-15595：
//
//	bonus[n] 先放大总经验（n = 有效成员数，>1 才共享），
//	再按等级比例 dwExp/ΣLevel*Level 分（HighLevelKillMonFixExp=1 时改为平均 exp/n）。
//
// ⚠️ 原版的"有效成员"判定只比较**横坐标** |Δx| ≤ 12 —— Y 那行写的是 X
// （15577/15587 两处都是），是原版自己的 bug，我们照抄（见 internal/group）。
//
// 本函数**不加锁**：group.Manager 自带锁，而遍历玩家需要 s.mu，
// 所以先在锁内抓快照再锁外发包。
func (s *Server) shareGroupExp(killer *Player, exp uint32) {
	s.mu.RLock()
	leader := s.social.groups.LeaderOf(killer.Obj.ID)
	ids := s.social.groups.Members(leader)
	if len(ids) <= 1 || killer.Obj.MapRef() == nil {
		s.mu.RUnlock()
		return
	}
	killerMap := killer.Obj.MapRef().Name
	killerX := killer.Obj.PosX()
	type member struct {
		p     *Player
		level uint32
		x     int
		dead  bool
	}
	snap := make([]member, 0, len(ids))
	for _, id := range ids {
		o := s.world.players[id]
		if o == nil || o.Obj == nil || o.Char == nil || o.Char.Data == nil ||
			o.Char.Data.Abil == nil {
			continue
		}
		snap = append(snap, member{
			p:     o,
			level: o.level(),
			x:     o.Obj.PosX(),
			dead:  o.hp() == 0,
		})
	}
	s.mu.RUnlock()

	members := make([]group.ExpMember, 0, len(snap))
	for _, m := range snap {
		// ⚠️ 同图判定：原版比的是 m_PEnvir（地图**对象**），
		// 我们用地图名字符串，语义等价且可比较。
		if m.p.Obj.MapRef() == nil || m.p.Obj.MapRef().Name != killerMap {
			continue
		}
		members = append(members, group.ExpMember{
			ID: m.p.Obj.ID, Level: m.level, Map: killerMap, X: m.x, Dead: m.dead,
		})
	}
	res := group.DistributeExp(killer.Obj.ID, killerMap, killerX, exp, members, groupExpAverage)
	if !res.Shared {
		return
	}

	// 锁外发包与写经验（每个成员只加自己那一份）
	for _, m := range snap {
		share, ok := res.Award[m.p.Obj.ID]
		if !ok || share == 0 {
			continue
		}
		// 地图经验倍率（`EXPRATE(百分比)`，官方 `ObjBase.pas:1834`）：
		// `dwExp := Round((nEXPRATE / 100) * dwExp)`，作用在**杀怪经验**这条链上
		//（PKDie 的奖惩不走这里，别混）。
		if mi := s.mapFlagOf(m.p.Obj.MapRef()); mi != nil && mi.ExpRate > 0 {
			share = uint32(uint64(share) * uint64(mi.ExpRate) / 100)
		}
		s.grantExpRaw(m.p, share)
	}
	obs.Event("group_exp_share", "killer", killer.Char.Name, "valid", res.Valid,
		"mult", res.Multiplier, "total", exp)
	log.Printf("%s 击杀经验 %d 由 %d 人共享（×%.1f）", killer.Char.Name, exp, res.Valid, res.Multiplier)
}

// applyLevelUp 应用升级结果：改等级/上限、回满、发包与日志。
func (s *Server) applyLevelUp(p *Player, res entity.LevelUpResult) {
	c := p.conn
	abil := p.Char.Data.Abil
	abil.Level = res.NewLevel
	// ⚠️ 上限要按"新的等级基数 + 装备加成"重算，不能只 `+= res.HPUp`：
	// 装备加成（含魔血套的 `MaxMP → MaxHP` 挪动）是**依赖等级**的，
	// 而且这样做天然幂等（见 applyEquipHpMp 的说明）。重算后再回满。
	s.applyEquipHpMp(p)
	// 负重上限也是等级的函数（ObjBase.pas RecalcLevel）⇒ 跟着重算
	s.applyWeights(p)
	p.refillHPMP() // 升级回满（持锁）

	// SM_LEVELUP：Param=LoWord(等级)，Tag=HiWord(等级)
	if sink := p.protoOut; sink != nil {
		// 新协议：一条 LevelUp 就把等级 + **完整能力值**都带过去了
		//（legacy 那条要 SM_LEVELUP + SM_ABILITY 两条包）。
		sink.levelUp(res.NewLevel, abil, p.gold())
	} else {
		s.send(c, proto.SM_LEVELUP, int32(p.Obj.ID),
			proto.LoWord(int32(res.NewLevel)), proto.HiWord(int32(res.NewLevel)), 0, "")
	}
	// 属性变化需要重新下发
	// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
	s.applyWeights(p)
	abRaw := abilityFromPB(abil).Bytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
	s.sendSubAbility(c, p)
	log.Printf("%s 升到 %d 级（HP+ %d MP+ %d）", p.Char.Name, res.NewLevel, res.HPUp, res.MPUp)
	obs.Event("level_up", "player", p.Char.Name, "to", res.NewLevel, "exp", abil.Exp,
		"hp_up", res.HPUp, "mp_up", res.MPUp)
}

// baseHpMp 返回某个等级应有的**上限基数**（不含装备）。
//
// 与 setPlayerLevel 里那段累加是同一个公式：`Σ_{lv=1}^{level-1} GrowthFor(lv)`
// （等价原版 RecalcLevel —— 上限是等级的纯函数，不依赖当前值）。
func baseHpMp(level, job uint32) (hp, mp uint32) {
	// ⚠️ 起点是 1 级的**职业初值**（`GrowthFor` 只给"升到下一级的增量"）：
	// 漏掉它会让 1 级角色的上限算成 0（被 clamp 成 1）—— 实测踩过一次，
	// 表现是"上线后 HP 变成 1/1"，一次挂 12 个用例。
	hp, mp = entity.InitialHPMP(job)
	for lv := uint32(1); lv < level; lv++ {
		h, m := entity.GrowthFor(lv)
		hp += h
		mp += m
	}
	return
}

// applyEquipHpMp 重算"等级基数 + 装备加成"，写回存档里的 MaxHp/MaxMp。
//
// 原版（ObjBase.pas:3390-3392 + :3465-3473）：
//
//	m_WAbil.MaxHP := _MIN(High(Word), m_Abil.MaxHP + m_AddAbil.wHP);
//	m_WAbil.MaxMP := _MIN(High(Word), m_Abil.MaxMP + m_AddAbil.wMP);
//	// 魔血套：把 MaxMP 挪给 MaxHP（挪的量 = ΣAniCount，三件齐再 +50）
//	if m_nMoXieSuite > 0 then begin
//	  if m_WAbil.MaxMP <= m_nMoXieSuite then m_nMoXieSuite := m_WAbil.MaxMP - 1;
//	  Dec(m_WAbil.MaxMP, m_nMoXieSuite);
//	  m_WAbil.MaxHP := _MIN(High(Word), m_WAbil.MaxHP + m_nMoXieSuite);
//	end;
//
// ⚠️ 为什么是"重算"而不是"增量地加上去"：
//  1. 全仓库有 58 处直接读 `Abil.MaxHp`（回复、下发包、死亡回满、钳制…），
//     "读的时候现算"要改一大片；
//  2. 上限是**落盘**的，增量写法在"换装备 → 存档 → 重新登录"后会**加成两次**
//     （登录时读到的已经含加成，却又要再加一遍）。
//
// 用"基数（等级的纯函数）+ 当前装备加成"重算，重复调用幂等、落盘也不累积。
func (s *Server) applyEquipHpMp(p *Player) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	abil := p.Char.Data.Abil
	a := s.playerAddAbil(p)
	hpBonus, mpBonus := a.hp, a.mp

	base, baseMP := baseHpMp(uint32(abil.Level), uint32(p.Char.Data.Job))
	// 魔血套：从 MaxMP 挪给 MaxHP。原版禁止把蓝扣光（`suite := MaxMP - 1`）。
	if sp := s.playerItemSpecials(p); sp.moXie > 0 {
		suite := sp.moXie
		if sp.moXiePieces >= 3 {
			suite += 50
		}
		if maxMP := int(baseMP) + mpBonus; suite >= maxMP {
			suite = maxMP - 1
		}
		if suite > 0 {
			mpBonus -= suite
			hpBonus += suite
		}
	}

	// 上限是 Word（原版 `_MIN(High(Word), ...)`）
	clampWord := func(v int) uint32 {
		if v < 1 {
			return 1
		}
		if v > 65535 {
			return 65535
		}
		return uint32(v)
	}
	oldHP, oldMP := abil.MaxHp, abil.MaxMp
	abil.MaxHp = clampWord(int(base) + hpBonus)
	abil.MaxMp = clampWord(int(baseMP) + mpBonus)
	p.clampHPMP() // 上限重算后夹回来（持锁）
	if oldHP != abil.MaxHp || oldMP != abil.MaxMp {
		log.Printf("%s 上限重算：HP %d→%d、MP %d→%d（装备加成 %+d/%+d）",
			p.Char.Name, oldHP, abil.MaxHp, oldMP, abil.MaxMp, hpBonus, mpBonus)
	}
}

// setPlayerLevel 直接设定等级（脚本 CHANGELEVEL 用）。
//
// 对应 ActionOfChangeLevel（ObjNpc.pas:3053）：按等级差补/减属性上限，
// 并把当前值拉到新上限。原版还有"降级不掉属性"的细节，我们简化成
// 上限跟随等级变化、当前值取 min(当前, 新上限)。
func (s *Server) setPlayerLevel(p *Player, level uint32) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	c := p.conn
	abil := p.Char.Data.Abil
	old := uint32(abil.Level)
	if level == 0 || level == old {
		return
	}
	abil.Level = level
	// 上限 = 等级基数 + 装备加成（含魔血套），统一走 applyEquipHpMp 重算
	s.applyEquipHpMp(p)
	s.applyWeights(p)
	p.clampHPMP() // 上限重算后夹回来（持锁）
	// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
	s.applyWeights(p)
	abRaw := abilityFromPB(abil).Bytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
	s.sendSubAbility(c, p)
	s.sendHealthChanged(p, p.Obj.ID, abil.Hp, abil.Mp, abil.MaxHp)
	s.sysMsg(c, fmt.Sprintf("等级已调整为 %d", level))
	log.Printf("%s 等级 %d -> %d（脚本）", p.Char.Name, old, level)
	obs.Event("level_set", "player", p.Char.Name, "from", old, "to", level)
}

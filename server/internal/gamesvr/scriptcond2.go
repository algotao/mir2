package gamesvr

// 第二批脚本条件的 gamesvr 适配：取真实数据喂给 internal/script 的纯判定。
//
// 拆分原则：**判定逻辑在 internal/script（可单测），取数在这里**。
// internal/script 不该依赖 Player / world / entity。

import (
	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/world"
)

// bagCap 是背包格数（与 addToBag 的容量一致）。
const bagCap = 46

// scriptVals 取当前玩家的数值快照。
func (s *Server) scriptVals(p *Player) script.Vals {
	v := script.Vals{BagFree: bagCap}
	d := p.Char.Data
	if d == nil {
		return v
	}
	v.BagFree = bagCap - p.bagLen()
	// ⚠️ 属性整块拿**一次快照**（而不是逐个字段各持一次锁）：见 statelock.go。
	if ab := p.abilCopy(); ab != nil {
		v.HP = int64(ab.Hp)
		v.MP = int64(ab.Mp)
		v.Exp = int64(ab.Exp)
		if ab.Dc != nil {
			v.DC = int64(ab.Dc.Min)
		}
		if ab.Mc != nil {
			v.MC = int64(ab.Mc.Min)
		}
		if ab.Sc != nil {
			v.SC = int64(ab.Sc.Min)
		}
	}
	if p.Obj != nil && p.Obj.MapRef() != nil {
		v.MapName = p.Obj.MapRef().Name
	}
	if p.Obj != nil && p.Obj.MapRef() != nil {
		v.MapHumanCount, v.MapMonCount = s.countMap(p.Obj.MapRef())
	}
	v.SlaveCount = s.countSlave(p)
	v.GroupCount, v.GroupJobCount = s.groupSnapshot(p)
	return v
}

// groupSnapshot 给脚本条件取"我所在队伍的人数与职业分布"。
//
// ⚠️ 人数字段的语义是 `m_GroupOwner.m_GroupMembers.Count`（原版查的是**队长**
// 的名单，ObjNpc.pas:4892），不是"我看到几个人"。没组队时返回全 0，
// 于是 `CHECKGROUPCOUNT < 5` 也为假 —— 与原版 `if m_GroupOwner = nil then Exit` 一致。
func (s *Server) groupSnapshot(p *Player) (count int, byJob [3]int) {
	if p == nil || p.Obj == nil {
		return 0, byJob
	}
	s.mu.RLock()
	ids := s.social.groups.Members(s.social.groups.LeaderOf(p.Obj.ID))
	s.mu.RUnlock()
	count = len(ids)
	for _, id := range ids {
		o := s.world.players[id]
		if o == nil || o.Char == nil || o.Char.Data == nil {
			continue
		}
		job := int(o.Char.Data.Job)
		if job >= 0 && job < len(byJob) {
			byJob[job]++
		}
	}
	return count, byJob
}

// countMap 统计地图上的玩家数与怪物数。
func (s *Server) countMap(mp *world.Map) (humans, mons int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pl := range s.world.players {
		if pl.Obj != nil && pl.Obj.MapRef() == mp {
			humans++
		}
	}
	for _, m := range s.world.monsters {
		if m.MapRef() == mp && !m.IsDead() && !m.IsNPC && !m.IsCastleUnit() {
			mons++
		}
	}
	return humans, mons
}

// countSlave 数召唤兽（CHECKSLAVECOUNT，对应原版 m_SlaveList.Count）。
//
// 直接用 p.Slaves——原版判的也是 `m_SlaveList.Count`（ObjNpc.pas:5509-5512、
// 7072），而全表扫 s.world.monsters 会把"主人已下线、兽还没回收"的孤儿也算进来。
func (s *Server) countSlave(p *Player) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.slaveOf(p))
}

// castleAdapter 把 *castle.Castle 适配成 script.CastleInfo。
//
// internal/script 只需要"我在不在战区/有没有攻/是不是守方"，
// 而 internal/castle 已经有这些方法（对应 Delphi 的
// InCastleWarArea / IsAttackGuild / IsDefenseGuild），所以直接转发。
type castleAdapter struct{ cs *castle.Castle }

func (a castleAdapter) InWarArea(mapName string, x, y int) bool {
	return a.cs.InWarArea(mapName, x, y)
}
func (a castleAdapter) UnderWar() bool               { return a.cs.UnderWar() }
func (a castleAdapter) IsMember(g string) bool       { return a.cs.IsMember(g) }
func (a castleAdapter) IsAttackGuild(g string) bool  { return a.cs.IsAttackGuild(g) }
func (a castleAdapter) IsDefenseGuild(g string) bool { return a.cs.IsDefenseGuild(g) }

// evalCondFull 求值一条条件（含第二批与城堡条件）。
//
// 顺序：先试普通条件，再试第二批，最后试城堡条件。
// 三处都返回 false 的指令就是真未实现。
func (s *Server) evalCondFull(p *Player, cond string) bool {
	ctx := newScriptCtx(s, p)
	if script.EvalCond(cond, ctx) {
		return true
	}
	if script.EvalCond2(cond, ctx, s.scriptVals(p)) {
		return true
	}
	if s.castle.mgr != nil {
		if cs, err := s.castle.mgr.Default(); err == nil {
			if script.EvalCondCastle(cond, ctx, castleAdapter{cs}) {
				return true
			}
		}
	}
	return false
}

// evalLabelCondsFull 是 evalLabelConds 的完整版（走 evalCondFull）。
func (s *Server) evalLabelCondsFull(p *Player, l *script.Label) bool {
	if l == nil || len(l.Conds) == 0 {
		return true
	}
	for _, c := range l.Conds {
		if !s.evalCondFull(p, c) {
			return false
		}
	}
	return true
}

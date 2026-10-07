package gamesvr

// 宠物 / 召唤兽子系统（召唤骷髅 17、召唤神兽 30、诱惑之光 20 的收服物）。
//
// 原版把这些对象当**怪物**处理（TRobotMonster），只是多一个 `m_Master`。
// 所有行为差异都从这一个字段派生（ObjBase.pas:155-170）：
//
//  1. AI 走 ObjMon.pas:450-520 那一段：无目标时**不游荡**，而是走到主人身后
//     （`m_Master.GetBackPosition`）；离主人 >20 格或不同地图就瞬移过去。
//  2. 目标选取走 ObjBase.pas:21330-21370 的宠物分支：**只打**主人的目标、
//     主人被打时的防卫目标，绝不自己乱找人打。
//  3. 主人死亡 ⇒ 宠物陪葬（HP=0），除非配置开了 MasterDieMutiny。
//  4. 忠诚度到期判变（`m_dwMasterRoyaltyTick`），判变后变回普通怪。
//
// 我们此前只有 castSummon 一段"生成"，其余全缺：兽不跟随、主人下线变孤儿、
// 会主动攻击路过的所有人。本文件把这套补齐。
//
// ⚠️ 纪律：判变/陪葬这类**只有等时间过去才看得到**的行为，
// 一律把判定抽成纯函数（slaveShouldDesert / tammingHPRoll 之类）再写单测——
// e2e 等不到 10 天。

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// ---------- 出厂配置（对齐 GeeM2 的 !setup.txt）----------
//
// 逐个核对 /data/git/Mir2-GeeM2/Envir/!setup.txt 与 Delphi
// M2Share.pas:2048-2084 的默认值。**!setup.txt 与 Delphi 出厂默认有两处不同**
// （下面 tammingTargetLevel / tammingHPRate 标了"实际部署"），它们直接决定
// "手测能不能测到"，必须按 !setup.txt 取值而不是按注释里的默认。
const (
	// slaveRoyaltySec 是召唤骷髅/神兽的忠诚度秒数：10 天。
	// （Magic.pas:786、816 `dwRoyaltySec := 10 * 24 * 60 * 60`）
	slaveRoyaltySec = 10 * 24 * 60 * 60

	// slaveSkeletonCount / slaveDragonCount 是同时存在的上限。
	// （M2Share.pas:2062/2066 = 1；!setup.txt 未覆盖 ⇒ 1）
	slaveSkeletonCount = 1
	slaveDragonCount   = 1

	// tammingMaxLevel 是可被诱惑的怪物等级上限。
	// （M2Share.pas:2081 = 50；!setup.txt `MagTammingLevel=50`）
	tammingMaxLevel = 50

	// tammingTargetLevel 是等级门槛里的固定余量：
	// `RandomRange(主人等级, 主人等级+20) + 技能等级*5 > (怪等级 + tammingTargetLevel)`
	//
	// ⚠️ **!setup.txt 里是 1**，不是 M2Share.pas 出厂默认的 10 ——
	// 官方实际部署的配置**宽松得多**（怪等级可以比主人高不少）。
	tammingTargetLevel = 1

	// tammingHPRate 是成功率分母：`n14 := 怪最大HP / tammingHPRate`。
	// 倍率越大越容易。
	// ⚠️ **!setup.txt 里是 `MagTammingTargetHPRate=1000`**，不是默认的 100
	// ⇒ 2000 血以内的怪几乎必成。照 100 实现的话手测几乎不可能成功。
	tammingHPRate = 1000

	// tammingMaxPets 是诱惑之光的宠物数量上限。
	// （Magic.pas:812 用 `g_Config.nMagTammingCount`，出厂 5、!setup.txt 也是 5。
	//  ⚠️ **不是** nMagicLevel+2 —— 那是注释掉的旧值。）
	tammingMaxPets = 5
)

// slaveSummonSpec 是一种召唤物的出厂参数。
type slaveSummonSpec struct {
	// monName 是 StdMonster 名字。
	//
	// ⚠️ 原版默认是 **变异骷髅**（M2Share.pas:2061 `sSkeleton: '变异骷髅'`），
	// 不是"骷髅"。我们之前写的"骷髅"是怪物表里一只毫不相干的普通怪。
	monName  string
	maxCount int
}

// slaveSummonSpecs 技能号 → 召唤物参数。
//
// ⚠️ 原版还有 `SkeletonArray` / `DragonArray` 两张"按角色等级换更强的兽"的
// 升级表（M2Share.pas:9302-9342），**!setup.txt 里没有配置这两张表**，
// 所以官方部署下就是基础那一只。我们不实现升级表。
var slaveSummonSpecs = map[uint32]slaveSummonSpec{
	17: {monName: "变异骷髅", maxCount: slaveSkeletonCount}, // 召唤骷髅
	30: {monName: "神兽", maxCount: slaveDragonCount},     // 召唤神兽
}

// ---------- 归属登记 ----------

// bindSlave 建立主人↔宠物关系并做原版的附带效果。
//
// 对应 ObjBase.pas:7157-7187 `TBaseObject.MakeSlave`：
//
//	MonObj.m_Master := Self;
//	MonObj.m_dwMasterRoyaltyTick := GetTickCount + dwRoyaltySec * 1000;
//	MonObj.m_btSlaveMakeLevel := nMakeLevel;
//	MonObj.RecalcAbilitys;
//	if MonObj.HP < MonObj.MaxHP then HP += (MaxHP - HP) div 2;   // 只补到一半
//	MonObj.RefNameColor;
//	m_SlaveList.Add(MonObj);
//
// ⚠️ 三个容易漏的细节：
//   - **只补一半血**，不是满血。
//   - **清空 TargetID**：野生状态下锁定的目标不该带进宠物状态。
//   - **速度封顶**只对诱惑生效（MakeSlave 不封），见 applyTammingSpeedCap。
//
// **调用方持 s.mu**。返回是否成功（已达上限返回 false）。
func (s *Server) bindSlave(m *entity.Monster, p *Player, spec slaveSummonSpec,
	magicLevel uint32, royalty time.Duration, now time.Time) bool {

	if m == nil || p == nil || p.Obj == nil || m.MasterID != 0 {
		return false
	}
	if s.slaveNamedCount(p, m.Name) >= spec.maxCount {
		return false
	}
	m.MasterID = p.Obj.ID
	m.SlaveMagicLevel = uint8(magicLevel)
	m.RoyaltyUntil = now.Add(royalty)
	m.NoTame = true // 召唤物同样不许被别人诱走
	m.TargetID = 0
	m.BreakCrazy()
	m.HealHalf() // 原版：补到一半（见 entity.Monster.HealHalf 的锁说明）
	p.Slaves = append(p.Slaves, m.ID)
	return true
}

// slaveNamedCount 数某主人名下**指定名字**的存活宠物数。
//
// ⚠️ 必须按名字分别计数：原版 MagMakeSlave 传的是
// `nCount := g_Config.nSkeletonCount`（**每种**的上限），
// 而诱惑之光判的是 `m_SlaveList.Count < 5`（**总数**）。
// 两种口径都存在，所以本函数与 SlaveCount 并存。
//
// **调用方持 s.mu**。
func (s *Server) slaveNamedCount(p *Player, name string) int {
	if p == nil {
		return 0
	}
	n := 0
	for _, id := range p.Slaves {
		if m := s.world.monsters[id]; m != nil && !m.IsDead() && m.MasterID == p.Obj.ID && m.Name == name {
			n++
		}
	}
	return n
}

// slaveCount 返回主人的宠物总数（原版 m_SlaveList.Count）。
// **调用方持 s.mu**。
func slaveCount(p *Player) int {
	if p == nil {
		return 0
	}
	return len(p.Slaves)
}

// unbindSlave 解除宠物的归属关系（判变 / 主人已不在）。
//
// 对应 ObjBase.pas:4069-4094（宠物自己解除时从主人列表里摘掉自己）
// 与 ObjBase.pas:4011-4018（主人清理已死/已判变的宠物）。
//
// ⚠️ 判变**不**重置 NoTame：原版只在诱惑成功时置 m_boNoTame（Magic.pas:852），
// 判变时并没有清掉，所以判变出来的兽仍然"已被驯服"、别人诱不走。
// 看着像原版的疏漏，但照抄——自作主张清掉会让野外的判变兽被反复捡走。
//
// **调用方持 s.mu**。
func (s *Server) unbindSlave(m *entity.Monster) {
	if m == nil || m.MasterID == 0 {
		return
	}
	if p := s.world.players[m.MasterID]; p != nil {
		for i, id := range p.Slaves {
			if id == m.ID {
				p.Slaves = append(p.Slaves[:i], p.Slaves[i+1:]...)
				break
			}
		}
	}
	m.MasterID = 0
	m.RoyaltyUntil = time.Time{}
	m.TargetID = 0
}

// masterOf 返回宠物的主人（可能为 nil）。**调用方持 s.mu**。
func (s *Server) masterOf(m *entity.Monster) *Player {
	if m == nil || m.MasterID == 0 {
		return nil
	}
	return s.world.players[m.MasterID]
}

// slaveOf 返回玩家名下的宠物实体（已剔除已死的）。**调用方持 s.mu**。
func (s *Server) slaveOf(p *Player) []*entity.Monster {
	if p == nil {
		return nil
	}
	out := make([]*entity.Monster, 0, len(p.Slaves))
	for _, id := range p.Slaves {
		if m := s.world.monsters[id]; m != nil && !m.IsDead() {
			out = append(out, m)
		}
	}
	return out
}

// ---------- 主人生命周期 ----------

// releaseSlaves 在主人离开时回收他所有的宠物，返回被移除的兽（锁外广播下线）。
//
// 原版：主人下线/死亡 ⇒ 兽消失（MasterDieMutiny=0 时陪葬，ObjBase.pas:3969-4000）。
// 我们在下线时直接清场——**没有**回收逻辑时这些兽会变成永久孤儿：
// 还在世界里被 tickMonsters 驱动、还会主动攻击路过的所有人、
// 还会一直占着"数量上限"让主人再也召不出来。
//
// 必须在 `delete(s.world.players, id)` **之前**调用（还要靠 p.Obj.ID 找兽）。
// **调用方持 s.mu**。
func (s *Server) releaseSlaves(p *Player) []*entity.Monster {
	if p == nil || len(p.Slaves) == 0 {
		return nil
	}
	gone := make([]*entity.Monster, 0, len(p.Slaves))
	for _, id := range p.Slaves {
		m := s.world.monsters[id]
		if m == nil {
			continue
		}
		delete(s.world.monsters, m.ID)
		s.world.monsterIdx.Remove(m)
		m.MasterID = 0
		gone = append(gone, m)
	}
	p.Slaves = nil
	return gone
}

// ---------- 忠诚度 / 判变 ----------

// slaveShouldDesert 判定宠物忠诚度到期、该解除归属了。
//
// 对应 ObjBase.pas:4069-4078（`m_dwMasterRoyaltyTick` 到期）：
// 判变时 `m_Master := nil`，随后它就是一只普通怪。
func slaveShouldDesert(m *entity.Monster, now time.Time) bool {
	return m != nil && m.IsSlave() && m.Deserted(now)
}

// slavesDyingWithMaster 陪葬该主人的所有宠物（主人死亡时调用）。
//
// ObjBase.pas:3969-4000：
//
//	if 主人死亡 1 秒后 then
//	  if boMasterDieMutiny and 主人有 LastHiter and Random(rate)=0 then 判变
//	  else m_WAbil.HP := 0;                     // 陪葬
//
// GeeM2 的 !setup.txt 里 `MasterDieMutiny=0` ⇒ 恒陪葬。
// 我们没有"主人死亡状态"可供轮询（revive 是立刻发生的），所以在死亡流程里
// 显式调用本函数。
//
// 返回被陪葬的兽（调用方负责广播死亡/移除）。
// **调用方持 s.mu**。
func (s *Server) slavesDyingWithMaster(p *Player) []*entity.Monster {
	if p == nil {
		return nil
	}
	var dead []*entity.Monster
	for _, m := range s.slaveOf(p) {
		m.Kill()
		dead = append(dead, m)
	}
	return dead
}

// killSlavesWithMaster 主人死亡时把他的宠物一并带走（陪葬）。
//
// ObjBase.pas:3969-4000。GeeM2 的 !setup.txt `MasterDieMutiny=0`
// ⇒ 原版在这套配置下**恒陪葬**、不判变，所以本函数不实现判变分支。
// 宠物的死亡包在锁外广播。
func (s *Server) killSlavesWithMaster(p *Player) {
	s.mu.Lock()
	dead := s.slavesDyingWithMaster(p)
	for _, m := range dead {
		delete(s.world.monsters, m.ID)
		s.world.monsterIdx.Remove(m)
		m.MasterID = 0
	}
	s.mu.Unlock()

	for _, m := range dead {
		s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(o *Player) {
			if o.visible.Remove(m.ID) {
				s.send(o.conn, proto.SM_DEATH, int32(m.ID),
					uint16(m.PosX()), uint16(m.PosY()), uint16(m.Facing()), "")
			}
		})
	}
	if n := len(dead); n > 0 {
		log.Printf("%s 死亡，%d 只宠物陪葬", p.Char.Name, n)
	}
}

// ---------- 宠物 AI ----------

// slaveBackPosition 返回主人**身后**一格（宠物跟随点）。
//
// 对应 ObjBase.pas:22583 `GetBackPosition`：按主人朝向取**反方向**那一格。
// 越界时不偏移（与原版一致：只在该往里挪的方向才 Inc/Dec）。
func slaveBackPosition(master *entity.Object, w, h int) (int, int) {
	x, y := master.PosX(), master.PosY()
	switch master.Facing() {
	case entity.DirUp:
		if y < h-1 {
			y++
		}
	case entity.DirUpRight:
		if x < w-1 && y > 0 {
			x--
			y++
		}
	case entity.DirRight:
		if x > 0 {
			x--
		}
	case entity.DirDownRight:
		if x > 0 && y > 0 {
			x--
			y--
		}
	case entity.DirDown:
		if y > 0 {
			y--
		}
	case entity.DirDownLeft:
		if x > 0 && y < h-1 {
			x++
			y--
		}
	case entity.DirLeft:
		if x < w-1 {
			x++
		}
	case entity.DirUpLeft:
		if x < w-1 && y < h-1 {
			x++
			y++
		}
	}
	return x, y
}

// slaveTeleportRange 是"离这么远就瞬移过去"的阈值（ObjMon.pas:491-492）。
const slaveTeleportRange = 20

// slaveNeedsTeleport 判定宠物是否该瞬移到主人身边。
//
// ObjMon.pas:489-495：地图不同，或 x/y 距离 > 20 ⇒ SpaceMove 到主人那张图。
// ⚠️ 主人休息（m_boSlaveRelax）时**不**瞬移。
func slaveNeedsTeleport(m *entity.Monster, master *Player) bool {
	if m == nil || master == nil || master.Obj == nil || master.slaveRelax {
		return false
	}
	if m.MapRef() != master.Obj.MapRef() {
		return true
	}
	dx, dy := m.PosX()-master.Obj.PosX(), m.PosY()-master.Obj.PosY()
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx > slaveTeleportRange || dy > slaveTeleportRange
}

// slaveShouldAttack 判定宠物该不该打这个目标。
//
// 对应 ObjBase.pas:21330-21370 的宠物分支，要点：
//
//	BaseObject.m_Master = m_Master              → False（不打主人的其它宠物）
//	m_Master.m_boSlaveRelax                     → False（主人休息）
//	BaseObject.m_TargetCret = m_Master          → True （打主人的目标）
//	BaseObject.m_Master = m_Master.m_LastHiter  → True （替主人报仇）
//
// ⚠️ **与野生怪最大的区别：宠物不会"自己找附近的人打"**。
// 这就是原版宠物不误伤路人、也不替主人惹事的根本原因。
// 别在宠物分支里复用"找最近玩家"那套。
func slaveShouldAttack(m *entity.Monster, master *Player, victim *Player) bool {
	if m == nil || master == nil || master.Obj == nil || victim == nil || victim == master {
		return false
	}
	if master.slaveRelax {
		return false
	}
	if master.hasBuff(entity.BuffInvisible) {
		return false // 主人隐身 ⇒ 宠物也停手
	}
	// 主人正在打的人 / 主人刚被打（正当防卫窗口内）
	return master.spellTargetID == victim.Obj.ID || master.pvpFlag
}

// tickSlaves 宠物 AI 的一帧（在 tickMonsters 内部调用，**调用方持 s.mu**）。
//
// 对应 ObjMon.pas:450-520 TRobotMonster.Run 的宠物分支。
// 产出（移动/攻击）由 tickMonsters 统一在锁外发包。
func (s *Server) tickSlaves(now time.Time, moved *[]monsterMove, hits *[]monsterHit,
	petHits *[]petHit) {
	for _, m := range s.world.monsters {
		if m.IsDead() || m.IsNPC || m.MasterID == 0 {
			continue
		}
		master := s.world.players[m.MasterID]
		if master == nil {
			// 主人不在了。正常路径 removePlayer 已经回收过，走到这说明
			// 有别的入口把人踢了——解除归属让它变回普通怪，比留孤儿强。
			s.unbindSlave(m)
			log.Printf("宠物 %s (ActorId=%d) 的主人已不在，解除归属", m.Name, m.ID)
			continue
		}

		// 1. 判变
		if slaveShouldDesert(m, now) {
			s.unbindSlave(m)
			log.Printf("%s 的宠物 %s 忠诚度到期，判变", master.Char.Name, m.Name)
			continue
		}

		// 2. 狂怒到期自动解除（OpenCrazyMode 是有时限的，Magic.pas:850）
		if !m.CrazyUntil.IsZero() && !m.Crazy(now) {
			m.BreakCrazy()
		}

		// 3. 离太远 ⇒ 瞬移到主人身边
		if slaveNeedsTeleport(m, master) {
			mp := master.Obj.MapRef()
			if mp != nil && (m.MapRef() != mp || m.PosX() != master.Obj.PosX() || m.PosY() != master.Obj.PosY()) {
				s.teleportSlave(m, master, mp, now)
				*moved = append(*moved, monsterMove{m.ID, m.PosX(), m.PosY(), m.Facing(), m.MapRef()})
				continue
			}
		}

		if m.MapRef() != master.Obj.MapRef() {
			continue
		}

		// 4. 跟着主人走：走到主人身后一格
		if !master.slaveRelax {
			bx, by := slaveBackPosition(master.Obj, m.MapRef().Width(), m.MapRef().Height())
			if m.Distance(bx, by) > 0 && m.CanAct(now) {
				m.MarkActed(now)
				if m.StepToward(bx, by) {
					s.world.monsterIdx.Update(m)
					*moved = append(*moved, monsterMove{m.ID, m.PosX(), m.PosY(), m.Facing(), m.MapRef()})
					continue
				}
			}
		}

		// 5. 攻击：只打主人正在打的人
		for _, v := range s.world.players {
			if !slaveShouldAttack(m, master, v) {
				continue
			}
			if m.Distance(v.Obj.PosX(), v.Obj.PosY()) <= 1 {
				if m.CanAttack(now) {
					m.MarkAttacked(now)
					*hits = append(*hits, s.monsterStrike(m, v))
				}
				break
			}
			if m.CanAct(now) {
				m.MarkActed(now)
				if m.StepToward(v.Obj.PosX(), v.Obj.PosY()) {
					s.world.monsterIdx.Update(m)
					*moved = append(*moved, monsterMove{m.ID, m.PosX(), m.PosY(), m.Facing(), m.MapRef()})
				}
			}
			break
		}

		// 6. 打怪：原版宠物**不主动找怪**，只打"正在打主人 / 被主人打 / 正在打自己"
		//    的怪（`IsAttackTarget` 的宠物分支，ObjBase.pas:21332-21375；判据与说明
		//    见 petattack.go 文件头）。这也是 `GainSlaveExp` 的唯一触发点。
		if target := s.petAttackTarget(m, master); target != nil {
			if absInt(target.PosX()-m.PosX())+absInt(target.PosY()-m.PosY()) <= 1 {
				if m.CanAttack(now) {
					m.MarkAttacked(now)
					h := s.petAttackMonster(m, target, now)
					if h.dmg > 0 {
						h.master = master
						if h.died {
							// 与火墙一致：锁内移出索引，锁外补经验/掉落/日志
							delete(s.world.monsters, target.ID)
							s.world.monsterIdx.Remove(target)
						}
						*petHits = append(*petHits, h)
					}
				}
				continue
			}
			if m.CanAct(now) {
				m.MarkActed(now)
				if m.StepToward(target.PosX(), target.PosY()) {
					s.world.monsterIdx.Update(m)
					*moved = append(*moved, monsterMove{m.ID, m.PosX(), m.PosY(), m.Facing(), m.MapRef()})
				}
			}
			continue
		}
	}
}

// teleportSlave 把宠物瞬移到主人所在图的主人身后。
//
// 对应 ObjMon.pas:494 `SpaceMove(主人的地图名, m_nTargetX, m_nTargetY, 1)`。
// **调用方持 s.mu**。
func (s *Server) teleportSlave(m *entity.Monster, master *Player, mp *world.Map, now time.Time) {
	bx, by := slaveBackPosition(master.Obj, mp.Width(), mp.Height())
	x, y, ok := nearestWalkable(mp, bx, by, 6)
	if !ok {
		x, y = bx, by
	}
	m.SetPos(mp, x, y)
	s.world.monsterIdx.Update(m)
	m.MarkActed(now) // 瞬移后重置移动冷却，否则它在新位置卡一个间隔
}

// ---------- 诱惑之光 ----------

// tammingGateRoll 是"第一道门"：`(怪不是玩家) and Random(4 - 技能等级) = 0`。
//
// Magic.pas:783。技能 0/1/2/3 级对应 1/4、1/3、1/2、**1/1**（3 级必过）。
// ⚠️ 4 - magicLevel 在技能等级 >3 时是 ≤0，Delphi 的 Random(≤0) = 0 ⇒ 恒过。
// 我们让 delphi.Random 兜住这个（返回 0），与原版一致。
func tammingGateRoll(magicLevel int) bool {
	return delphi.Random(4-magicLevel) == 0
}

// tammingLevelGate 判等级门槛（Magic.pas:801、805）。
//
// 两条硬门槛：
//   - 怪等级 ≤ 主人等级 + 2（Magic.pas:790）
//   - 怪等级 ≤ tammingMaxLevel（Magic.pas:811）
//
// 加一条概率门槛（Magic.pas:805）：
// `RandomRange(主人等级, 主人等级+20) + 技能等级*5 > (怪等级 + tammingTargetLevel)`
func tammingLevelGate(playerLevel, monLevel, magicLevel int) bool {
	if monLevel > playerLevel+2 {
		return false
	}
	if monLevel > tammingMaxLevel {
		return false
	}
	roll := delphi.Random(21) + playerLevel // RandomRange(playerLevel, playerLevel+20)
	return roll+magicLevel*5 > monLevel+tammingTargetLevel
}

// tammingHPRoll 是最后那道成功率判定，返回是否成功。
//
// Magic.pas:816-818：
//
//	n14 := 怪最大HP / tammingHPRate;                 // 分母越小越容易
//	if n14 <= 2 then n14 := 2 else Inc(n14, n14);   // ≤2 保持 2，否则**翻倍**
//	... Random(n14) = 0 才成功
//
// ⚠️ `else Inc(n14, n14)` 是 `n14 + n14` **翻倍**，不是 `Inc(n14)`。
// 照抄成后者会让血量高的怪容易收服一倍。
func tammingHPRoll(maxHP uint32) bool {
	n14 := int(maxHP) / tammingHPRate
	if n14 <= 2 {
		n14 = 2
	} else {
		n14 += n14
	}
	return delphi.Random(n14) == 0
}

// tammingMutinyMinutes 是诱惑成功后宠物的忠诚度分钟数。
//
// Magic.pas:838-841：
//
//	nSpiritMutinyTime := RandomRange(0, 主人等级)
//	                   + 60 * (主人等级 div 10)
//	                   + (技能等级 shl 2) * 5
//
// 原版注释给的锚点：13 级 → 1 小时 38 分；24 级 → 3 小时 24 分；30 级 → 4 小时。
// ⚠️ 3 级技能时 (3<<2)*5 = 60 分钟，也就是技能等级每升一级多 1 小时。
func tammingMutinyMinutes(playerLevel, magicLevel int) int {
	roll := delphi.Random(playerLevel + 1) // RandomRange(0, playerLevel)
	return roll + 60*(playerLevel/10) + (magicLevel<<2)*5
}

// tammingSpeedCap 是成功后对宠物速度/攻击间隔的封顶值。
//
// Magic.pas:846-851：
//
//	if 1500 - 技能等级*200 < m_nWalkSpeed   then m_nWalkSpeed   := 1500 - 技能等级*200
//	if 2000 - 技能等级*200 < m_nNextHitTime then m_nNextHitTime := 2000 - 技能等级*200
//
// 即"太快就压下来"，防止收了一只速度极快的怪。
// ⚠️ **只对诱惑生效**，MakeSlave（召唤骷髅/神兽）不封顶。
func tammingSpeedCap(magicLevel int) (walkCap, hitCap int) {
	return 1500 - magicLevel*200, 2000 - magicLevel*200
}

// castTamming 诱惑之光(20)：把一只野生怪收为自己的宠物。
//
// 对应 ObjBase.pas:488-492 的分派 + Magic.pas:773-895 `MagTamming`。
// 原版那个函数是 **7 层嵌套 Random 的俄罗斯套娃**，这里按同样的判定顺序
// 拆成平铺分支，每条副作用都标了行号。
//
// ⚠️ 原版 `Result := True` 在"过了外层门"之后**无条件**执行（Magic.pas:868），
// 也就是"技能放出去了"与"收服成功"无关。我们保持一致：只广播特效。
func (s *Server) castTamming(c net.Conn, p *Player, info *data.MagicInfo, um *pb.UserMagic,
	targX, targY int, targID uint32) {

	magicLevel := int(um.Level)
	plLevel := int(p.level())

	// 目标定位：坐标优先（与 castDamageSpell 同理，CM_SPELL 的 Series 装不下
	// 怪物 ActorId），targID 仅兜底。
	s.mu.Lock()
	var mon *entity.Monster
	for _, m := range s.world.monsters {
		if !m.IsDead() && m.MapRef() == p.Obj.MapRef() && m.PosX() == targX && m.PosY() == targY {
			mon = m
			break
		}
	}
	if mon == nil && targID != 0 {
		if m := s.world.monsters[targID]; m != nil && !m.IsDead() {
			mon = m
		}
	}
	if mon == nil {
		s.mu.Unlock()
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 有主的/NPC 不接受诱惑（Magic.pas:783 注释"防止抢有主人的宠物"）。
	if mon.IsNPC || mon.MasterID != 0 {
		s.mu.Unlock()
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		return
	}

	now := time.Now()
	outcome := s.tammingAttempt(mon, p, plLevel, magicLevel, now)
	s.mu.Unlock()

	// 特效：不论成败都播（原版失败也有表现，只是没有"收服成功"提示）
	s.broadcastToViewers(p.Obj.MapRef(), targX, targY, func(o *Player) {
		s.send(o.conn, proto.SM_MAGICFIRE, int32(p.Obj.ID),
			uint16(targX), uint16(targY), uint16(info.MagicID), "")
	})

	if outcome == "" {
		return
	}
	// 判变成普通怪 / 被招死 —— 这两种要通知客户端
	switch outcome {
	case tamingOutTamed:
		// 宠物在协议上与野怪同形 ⇒ 名字必须立刻带上 "(主人名)"
		//（原版 MakeSlave 成功后调 RefShowName，见 showname.go 的文件头）
		s.refShowName(mon)
	case tamingOutKilled:
		s.killMonsterBy(p, mon, info.MagicID, mon.PosX(), mon.PosY())
	case tamingOutDeserted:
		s.broadcastToViewers(mon.MapRef(), mon.PosX(), mon.PosY(), func(o *Player) {
			if o.visible.Remove(mon.ID) {
				s.send(o.conn, proto.SM_DISAPPEAR, int32(mon.ID),
					uint16(mon.PosX()), uint16(mon.PosY()), 0, "")
			}
		})
		s.mu.Lock()
		delete(s.world.monsters, mon.ID)
		s.world.monsterIdx.Remove(mon)
		s.mu.Unlock()
	}
	logTamming(p, mon, outcome, magicLevel)
}

// 诱惑之光的判定结果。空串 = 什么也没发生（原版对应"过了外层门但没做成"）。
const (
	tamingOutNone     = ""
	tamingOutTamed    = "tamed"    // 收服成功
	tamingOutSeized   = "seized"   // 反过来把它定住了
	tamingOutKilled   = "killed"   // 收服失败把它招死了
	tamingOutDeserted = "deserted" // 已被驯服 → 不可再诱
)

// tammingAttempt 执行 MagTamming 的全部判定。**调用方持 s.mu**。
//
// 拆出来是为了让 castTamming 能在锁内调它、锁外广播。
func (s *Server) tammingAttempt(mon *entity.Monster, p *Player,
	plLevel, magicLevel int, now time.Time) string {

	// ---- 门 1：Random(4 - 技能等级) = 0（Magic.pas:783）----
	if !tammingGateRoll(magicLevel) {
		// else 分支（Magic.pas:874-875）只是 `if Random(2) = 0 then Result := True`，
		// 什么也没做 ⇒ 对玩家而言就是"没反应"。
		delphi.Random(2) // 照摇一次，保持与原版同一条随机序列
		return tamingOutNone
	}
	// 过了门 1：清空它的当前目标（Magic.pas:785）
	mon.TargetID = 0

	// ---- 门 2：Random(2) = 0（Magic.pas:789）----
	if delphi.Random(2) != 0 {
		// 反过来：不是"尝试收服"，而是**定住它**（名字变褐）
		// `OpenHolySeizeMode(Random(技能等级*5+10) * 1000)`（Magic.pas:865-866）。
		mon.Seize(time.Duration(delphi.Random(magicLevel*5+10)) * time.Second)
		return tamingOutSeized
	}

	// ---- 门 3：Random(1) = 0 恒真（Magic.pas:797）----
	// 原版写的是 `if Random(1) = 0`，Delphi 的 Random(1) 恒为 0 ⇒ 恒成立。
	// 它的 else 分支（Magic.pas:856-860）是**死代码**，不实现。

	// ---- 门 4：等级门槛（Magic.pas:790、805）----
	if !tammingLevelGate(plLevel, int(mon.Info.Level), magicLevel) {
		// 等级差太多 ⇒ 非不死系 1/20 概率狂怒（名字变红），Magic.pas:850-852
		if mon.Info.Undead == 0 && delphi.Random(20) == 0 {
			mon.GoCrazy(time.Duration(delphi.Random(20)+10)*time.Second, time.Now())
		}
		return tamingOutNone
	}

	// ---- 门 5：硬性条件（Magic.pas:810-812）----
	if mon.NoTame {
		// 已被驯服：原版这里**没有任何副作用**，静默失败。
		return tamingOutDeserted
	}
	if mon.Info.Undead != 0 {
		// 不死系不可诱惑，且有 1/20 概率直接招死（Magic.pas:816-822）。
		if delphi.Random(20) == 0 {
			mon.Kill()
			return tamingOutKilled
		}
		return tamingOutNone
	}
	if int(mon.Info.Level) > tammingMaxLevel || slaveCount(p) >= tammingMaxPets {
		return tamingOutNone
	}

	// ---- 门 6：成功率（Magic.pas:816、824-826）----
	if !tammingHPRoll(mon.MaxHP) {
		// 失败副作用：1/14 概率直接把它招死（Magic.pas:828-830）
		if delphi.Random(14) == 0 {
			mon.Kill()
			return tamingOutKilled
		}
		return tamingOutNone
	}

	// ================= 诱惑成功（Magic.pas:832-856）=================
	mon.BreakCrazy()
	// 原版在"从别人手里抢过来"时把血量砍到 1/10（Magic.pas:836-837）；
	// 我们前面已挡掉有主的，所以这里走不到。
	mon.MasterID = p.Obj.ID
	mon.NoTame = true
	mon.SlaveMagicLevel = uint8(magicLevel)
	// 宠物等级：与召唤同源（原版诱惑成功分支也是设这两个字段，ObjBase.pas:13642-13643）
	mon.SlaveMakeLevel = uint8(magicLevel)
	mon.SlaveExpLevel = uint8(magicLevel)
	mon.RoyaltyUntil = now.Add(time.Duration(tammingMutinyMinutes(plLevel, magicLevel)) * time.Minute)
	mon.TargetID = 0
	mon.SeizedUntil = time.Time{} // BreakHolySeizeMode
	mon.HealHalf()                // 原版只补一半血
	p.Slaves = append(p.Slaves, mon.ID)
	applyTammingSpeedCap(mon, magicLevel)
	return tamingOutTamed
}

// applyTammingSpeedCap 按技能等级压低过快的宠物（Magic.pas:846-851）。
//
// ⚠️ 只能压**本实例**：m.Info 是全服共享的怪物模板，改它会让所有同种怪
// （包括野生的）一起变慢。
func applyTammingSpeedCap(m *entity.Monster, magicLevel int) {
	walkCap, hitCap := tammingSpeedCap(magicLevel)
	m.CapSpeed(uint16(max(0, walkCap)), uint16(max(0, hitCap)))
}

// logTamming 记录诱惑之光的每一次判定结果。
//
// 每一次都记（含失败）——否则 e2e 无法区分"没施放"与"施放了但没过门"，
// 而这正是排查概率技能最需要的那条信息。
func logTamming(p *Player, mon *entity.Monster, outcome string, magicLevel int) {
	if p == nil || p.Char == nil || mon == nil {
		return
	}
	switch outcome {
	case tamingOutTamed:
		log.Printf("%s 的诱惑之光收服了 %s (ActorId=%d，技能 %d 级，忠诚度 %d 分钟)",
			p.Char.Name, mon.Name, mon.ID, magicLevel,
			tammingMutinyMinutes(int(p.level()), magicLevel))
	case tamingOutKilled:
		log.Printf("%s 的诱惑之光把 %s 招死了（技能 %d 级）", p.Char.Name, mon.Name, magicLevel)
	case tamingOutDeserted:
		log.Printf("%s 无法诱惑 %s：该兽已被驯服（NoTame）", p.Char.Name, mon.Name)
	case tamingOutSeized:
		log.Printf("%s 的诱惑之光定住了 %s（未收服）", p.Char.Name, mon.Name)
	}
}

// cmdSlaveStatus @slave：切换宠物的攻击/休息状态。
//
// 对应 ObjBase.pas:10988-10996 `CmdChangeSalveStatus`：
//
//	if m_SlaveList.Count > 0 then begin
//	  m_boSlaveRelax := not m_boSlaveRelax;
//	  if m_boSlaveRelax then SysMsg(sPetRest) else SysMsg(sPetAttack)
//	end;
//
// 休息时宠物**既不攻击也不瞬移跟随**（ObjMon.pas:489、506、21366 三处都查这个位）。
// 原版没有宠物时**一个包都不发**，我们照抄。
func (s *Server) cmdSlaveStatus(c net.Conn, p *Player) {
	s.mu.RLock()
	n := len(s.slaveOf(p))
	s.mu.RUnlock()
	if n == 0 {
		return
	}
	p.slaveRelax = !p.slaveRelax
	if p.slaveRelax {
		s.sysMsg(c, "[休息]")
	} else {
		s.sysMsg(c, "[攻击]")
	}
}

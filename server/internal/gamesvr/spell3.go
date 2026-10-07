package gamesvr

import (
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// 第三批技能：抗拒火环(8) / 隐身术(18) / 集体隐身术(19)。
//
// 三个都与"施法者周围的实体"打交道（推人、拉怪、给队友上状态），
// 所以放一起；分派入口仍在 spell.go 的 switch 里。

// ---------- 隐身术(18) / 集体隐身术(19) ----------

// invisibleSeconds 是隐身时长（秒）：`GetPower13(30) + GetRPow(SC) * 3`。
//
// 18/19 用的是同一个公式（Magic.pas:476-483 把 nHTime 传给
// MagMakePrivateTransparent / MagMakeGroupTransparent）。
// ⚠️ GetRPow 会掷骰（区间 [Lo,Hi]），所以每次施法时长都不同。
func invisibleSeconds(info *data.MagicInfo, level uint32, sc uint32) int {
	return magic.GetPower13(30, info, level) + magic.GetRPow(sc)*3
}

// castInvisible 隐身术(18)。
//
// 原版 MagMakePrivateTransparent（Magic.pas:734-758）：
//
//	if m_wStatusTimeArr[STATE_TRANSPARENT] > 0 then exit;   // 已在隐身 ⇒ 直接退出
//	半径 9 内"以我为目标"的怪物：若距离 > 1 或 Random(2)=0 ⇒ 清掉它的目标
//	m_wStatusTimeArr[STATE_TRANSPARENT] := nHTime             // 单位：秒
//	m_boHideMode := True; m_boTransparent := True; 状态广播
//
// ⚠️ 第一条很容易写丢：写成"刷新时长"会让隐身术可以无限续期，
// 而原版在隐身期内再放一次是**完全无效**的（不刷新、也不重新拉怪）。
func (s *Server) castInvisible(p *Player, info *data.MagicInfo, um *pb.UserMagic) {
	if p.hasBuff(entity.BuffInvisible) {
		return // 原版静默 Exit，不给提示
	}
	s.dropAggroAround(p, 9)
	sec := invisibleSeconds(info, um.Level, playerPowerAttr(p, magic.PowerAttrSC))
	s.addBuffFor(p, entity.BuffInvisible, 0, time.Duration(sec)*time.Second)
	log.Printf("%s 隐身 %d 秒（技能等级 %d）", p.Char.Name, sec, um.Level)
	obs.Event("invisible", "player", p.Char.Name, "seconds", sec, "level", int(um.Level))
}

// dropAggroAround 让半径 r 内"以 p 为目标"的怪物丢失目标
// （MagMakePrivateTransparent 的前半段，Magic.pas:748-760）。
//
// 条件：距离 > 1 **或** Random(2)=0 ⇒ 丢目标。也就是说贴身追着的怪
// 只有一半概率会放弃，远处的必放弃。
func (s *Server) dropAggroAround(p *Player, r int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.world.monsterIdx.InRange(p.Obj.PosX(), p.Obj.PosY(), r)
	for _, o := range list {
		m, ok := o.(*entity.Monster)
		if !ok || m.MapRef() != p.Obj.MapRef() || m.TargetID != p.Obj.ID {
			continue
		}
		if absInt(m.PosX()-p.Obj.PosX()) > 1 || absInt(m.PosY()-p.Obj.PosY()) > 1 || delphi.Random(2) == 0 {
			m.TargetID = 0
		}
	}
}

// castGroupInvisible 集体隐身术(19)。
//
// 原版 MagMakeGroupTransparent（Magic.pas:1286-1306）：
//
//	取目标点 (nTargetX,nTargetY) **半径 1** 内的全部对象
//	  若是 IsProperFriend(它) 且它还没隐身 ⇒ 延时 800ms 给它上隐身
//
// 即"以落点为中心给队友（含自己）上隐身"，落点由客户端给（通常是脚下）。
// ⚠️ 护身符在 handleSpell 的 requireAmulet 里已经卡过了（19 属 case 13..19，要 1 个）。
func (s *Server) castGroupInvisible(p *Player, info *data.MagicInfo, um *pb.UserMagic, targX, targY int) {
	sec := invisibleSeconds(info, um.Level, playerPowerAttr(p, magic.PowerAttrSC))
	d := time.Duration(sec) * time.Second

	// 先锁内抓快照，再锁外判关系（isProperFriend 会读行会，自带锁，
	// 不能嵌在 s.mu 里 —— RWMutex 不可重入）。
	s.mu.RLock()
	var cands []*Player
	for _, o := range s.world.index.InRange(targX, targY, 1) {
		q, ok := o.(*Player)
		if !ok || q.Obj.MapRef() != p.Obj.MapRef() {
			continue
		}
		if absInt(q.Obj.PosX()-targX) > 1 || absInt(q.Obj.PosY()-targY) > 1 {
			continue
		}
		cands = append(cands, q)
	}
	s.mu.RUnlock()

	now := time.Now()
	n := 0
	for _, q := range cands {
		if q.hasBuff(entity.BuffInvisible) {
			continue // 原版：已经隐身的不重新上（也不刷新）
		}
		if !s.isProperFriend(p, q, now) {
			continue
		}
		s.addBuffFor(q, entity.BuffInvisible, 0, d)
		n++
	}
	// ⚠️ 一条都没上时也要记：否则"技能放了没反应"分不清是没队友还是逻辑错。
	log.Printf("%s 的集体隐身术让 %d 人隐身（%d 秒，落点 (%d,%d)）",
		p.Char.Name, n, sec, targX, targY)
	obs.Event("group_invisible", "caster", p.Char.Name, "targets", n,
		"seconds", sec, "x", targX, "y", targY)
}

// ---------- 抗拒火环(8) ----------

// castRepulsion 抗拒火环(8)：把身边的敌人推开。
//
// 原版 MagPushArround（Magic.pas:146-171）——对**视野内**每个对象：
//
//	|dx|<=1 且 |dy|<=1（八邻域）
//	未死亡、不是自己
//	我的等级 **高于** 它   且   它不在 m_boStickMode
//	Random(20) < 6 + 技能等级*3 + 等级差        ← 概率
//	IsProperTarget(它)
//	  push := 1 + max(0, 技能等级-1) + Random(2) 格
//	  nDir := 从我指向它的方向
//	  它.CharPushed(nDir, push)
//
// push 是**逐格**走的（CharPushed，ObjBase.pas:2504）：每走通一格就广播一次
// SM_BACKSTEP，走不通（不可通行 / 目标格被占）就停；全部走完后它**朝向反过来**
// （nBackDir），一格都没推开则朝向不变。每推开一格还给它 +800ms 的行动延迟。
//
// ⚠️ 原版的 `m_boStickMode` 是**特定 AI 类**（ObjMon2 里的木桩/固定怪，见
// ObjMon2.pas:165 等 4 处）才置位的标志，我们没有这些 AI 类；这里用
// "NPC 或城堡单位（城门/城墙）不可推"来近似——不然能把沙巴克的墙推走。
func (s *Server) castRepulsion(p *Player, um *pb.UserMagic) {
	const radius = 1

	// ---- ① 锁内抓快照 ----
	s.mu.RLock()
	var cands []pushCandidate
	for _, o := range s.world.monsterIdx.InRange(p.Obj.PosX(), p.Obj.PosY(), radius) {
		m, ok := o.(*entity.Monster)
		if !ok || m.MapRef() != p.Obj.MapRef() || !m.Alive || m.Info == nil {
			continue
		}
		if m.IsNPC || m.CastleKind != "" {
			continue
		}
		if absInt(m.PosX()-p.Obj.PosX()) > radius || absInt(m.PosY()-p.Obj.PosY()) > radius {
			continue
		}
		cands = append(cands, pushCandidate{
			id: m.ID, obj: m.Object, mon: m,
			level: int(m.Info.Level), x: m.PosX(), y: m.PosY(),
		})
	}
	for _, o := range s.world.index.InRange(p.Obj.PosX(), p.Obj.PosY(), radius) {
		q, ok := o.(*Player)
		if !ok || q == p || q.Obj.MapRef() != p.Obj.MapRef() {
			continue
		}
		if q.Char == nil || q.Char.Data == nil || q.Char.Data.Abil == nil ||
			q.hp() == 0 {
			continue
		}
		if absInt(q.Obj.PosX()-p.Obj.PosX()) > radius || absInt(q.Obj.PosY()-p.Obj.PosY()) > radius {
			continue
		}
		cands = append(cands, pushCandidate{
			id: q.Obj.ID, obj: q.Obj, pl: q,
			level: int(q.level()), x: q.Obj.PosX(), y: q.Obj.PosY(),
		})
	}
	s.mu.RUnlock()

	// ---- ② 锁外判定（等级/概率/关系）----
	now := time.Now()
	lv := int(um.Level)
	myLevel := p.level()
	mode := attackModeOf(p)
	type pushJob struct {
		c pushCandidate
		n int
	}
	var jobs []pushJob
	for _, cd := range cands {
		if int(myLevel) <= cd.level {
			continue
		}
		gap := int(myLevel) - cd.level
		if delphi.Random(20) >= 6+lv*3+gap {
			continue
		}
		if cd.pl != nil && !s.canAttackTarget(p, cd.pl, mode, now) {
			continue // IsProperTarget：玩家要过 PvP 关系判定
		}
		jobs = append(jobs, pushJob{c: cd, n: 1 + max(0, lv-1) + delphi.Random(2)})
	}

	// ---- ③ 锁内逐格推 + 收集广播 ----
	//
	// ⚠️ 逐格推走 `pushOneTile`（野蛮冲撞也用同一个）：它顺便同步**空间索引**。
	// 此前这里是就地改坐标，忘了 `monsterIdx.Update`/`index.Update` ——
	// 被推的对象会从按坐标查找里"弄丢"（索引还挂旧格子、坐标已变，比对永不匹配）。
	var bcs []*backstepShot
	steps := 0 // 所有目标被推开的**格数**总和
	s.mu.Lock()
	for _, jb := range jobs {
		if jb.c.obj.MapRef() == nil {
			continue
		}
		dir := uint8(dirFromTo(p.Obj.PosX(), p.Obj.PosY(), jb.c.x, jb.c.y))
		oldDir := jb.c.obj.Facing()
		moved := 0
		for i := 0; i < jb.n; i++ {
			bs := s.pushOneTile(jb.c, dir)
			if bs == nil {
				break // 对应原版 MoveToMovingObject 失败
			}
			moved++
			steps++
			bcs = append(bcs, bs)
		}
		if moved == 0 {
			jb.c.obj.SetFacing(oldDir) // 一格没推动 ⇒ 朝向不变
		}
	}
	s.mu.Unlock()

	// ---- ④ 锁外广播 SM_BACKSTEP ----
	for _, b := range bcs {
		s.broadcastToViewers(b.m, b.x, b.y, func(o *Player) {
			s.send(o.conn, proto.SM_BACKSTEP, int32(b.id),
				uint16(b.x), uint16(b.y), uint16(b.dir), "")
		})
	}

	// 锁内每推一格就 +1，所以 steps 才是"真的推开了几个格子"。
	// ⚠️ 别把候选数（jobs）当结果报——挡住一格都没推动时，候选数照样 > 0，
	// 排查时会误判成"服务端推了但客户端没收到"。
	log.Printf("%s 的抗拒火环：%d 个候选，实际推开 %d 格（等级 %d，范围 (%d,%d)）",
		p.Char.Name, len(jobs), steps, lv, p.Obj.PosX(), p.Obj.PosY())
	obs.Event("repulsion", "player", p.Char.Name, "cands", len(jobs),
		"steps", steps, "level", lv)
}

// pushCandidate 是抗拒火环的一个候选目标（怪物与玩家统一处理）。
type pushCandidate struct {
	id    uint32
	obj   *entity.Object // 统一用它改坐标/朝向
	mon   *entity.Monster
	pl    *Player
	level int
	x, y  int
}

// cellOccupied 判断 (x,y) 是否已被**别人**占用（自己原格不算）。
//
// 为什么不用 monsterAt/playerAt：它们内部会取 s.mu.RLock，
// 而调用方正持着 s.mu（RWMutex **不可重入**，会死锁）。这里改用空间索引。
//
// ⚠️ **必须自己比坐标**：SpatialIndex.InRange 是按 32×32 分块返回候选的
// （见 broadcastToViewers 也要自己判 Distance），拿它当"某格有没有东西"用
// 会把整个分块里的实体都当成占用 ⇒ 一格都推不动。这里踩过一次。
func (s *Server) cellOccupied(m *world.Map, x, y int, self *entity.Object) bool {
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		if e, ok := o.(*entity.Monster); ok && e.MapRef() == m && e.Object != self &&
			e.PosX() == x && e.PosY() == y {
			return true
		}
	}
	for _, o := range s.world.index.InRange(x, y, 0) {
		if e, ok := o.(*Player); ok && e.Obj.MapRef() == m && e.Obj != self &&
			e.Obj.PosX() == x && e.Obj.PosY() == y {
			return true
		}
	}
	return false
}

// dirFromTo 对应 M2Share.pas:3488 的 GetNextDirection(sX,sY,dx,dy)：
// 由"我 → 它"的位移得到八方向编号（顺序与 entity.DirDelta 一致）。
//
// 原版对 |Δ|>2 还有两处"轴对齐吸附"（一轴远、另一轴在 ±1 内就退化成正交方向），
// 一并照抄——抗拒火环只用到八邻域，但野蛮冲撞那类技能会用到长距离版本。
func dirFromTo(x1, y1, x2, y2 int) int {
	flagX := 0
	if x1 < x2 {
		flagX = 1
	} else if x1 > x2 {
		flagX = -1
	}
	if absInt(y1-y2) > 2 && x1 >= x2-1 && x1 <= x2+1 {
		flagX = 0
	}
	flagY := 0
	if y1 < y2 {
		flagY = 1
	} else if y1 > y2 {
		flagY = -1
	}
	if absInt(x1-x2) > 2 && y1 > y2-1 && y1 <= y2+1 {
		flagY = 0
	}
	switch {
	case flagX == 0 && flagY == -1:
		return entity.DirUp
	case flagX == 1 && flagY == -1:
		return entity.DirUpRight
	case flagX == 1 && flagY == 0:
		return entity.DirRight
	case flagX == 1 && flagY == 1:
		return entity.DirDownRight
	case flagX == 0 && flagY == 1:
		return entity.DirDown
	case flagX == -1 && flagY == 1:
		return entity.DirDownLeft
	case flagX == -1 && flagY == 0:
		return entity.DirLeft
	case flagX == -1 && flagY == -1:
		return entity.DirUpLeft
	}
	return entity.DirDown // 原版默认 DR_DOWN
}

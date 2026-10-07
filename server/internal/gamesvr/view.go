package gamesvr

import (
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/world"
)

// broadcastToViewers 对能看到 (x,y) 的玩家执行 fn。
//
// ⚠️ 锁内只做"选出收件人"，`fn` 一律在**锁外**跑 —— 它要发网络包，
// 而"持世界锁写 socket"正是审计 P1-6 记的那条（慢客户端会把全服卡住）。
func (s *Server) broadcastToViewers(m *world.Map, x, y int, fn func(*Player)) {
	s.mu.RLock()
	// 地图/距离过滤也要在锁内：这两个判定读的是**别人**的
	// `Obj.Map`/`Obj.X`/`Obj.Y`，而它们由那个人的 goroutine 改（P1-5）。
	cands := s.world.index.InRange(x, y, s.cfg.viewRange)
	viewers := make([]*Player, 0, len(cands))
	for _, o := range cands {
		p, ok := o.(*Player)
		if !ok || p.Obj.MapRef() != m {
			continue
		}
		if p.Obj.Distance(x, y) > s.cfg.viewRange {
			continue
		}
		viewers = append(viewers, p)
	}
	s.mu.RUnlock()

	for _, p := range viewers {
		fn(p)
	}
}

// ---------- 进入游戏 ----------
// updateVision 增量同步视野内的实体（玩家 + 怪物）进出。
//
// ⚠️ 审计 P1-5/P1-6：这个函数原来分两段、每段都在锁内做 I/O ——
// 第一段锁外发包（还算对），第二段"别人对 p 的可见性"是**持 s.mu 写 socket**。
// 现在统一成"锁内只算差集 + 收集收件人，锁外发全部包"：
//
//   - 锁内读 `other.Obj.Map/Distance`（别人 goroutine 会改，见 P1-5）；
//   - 锁外 `send*`（慢客户端不再按着世界锁，见 P1-6）。
func (s *Server) updateVision(p *Player) {
	var (
		entered []uint32
		left    []uint32
		appears []*Player // 这些人的视野里要"加入 p"（发 SM_TURN）
		gone    []*Player // 这些人的视野里要"移除 p"（发 SM_DISAPPEAR）
		px, py  int
	)

	s.mu.Lock()
	inView := make(map[uint32]struct{})

	for _, o := range s.world.index.InRange(p.Obj.PosX(), p.Obj.PosY(), s.cfg.viewRange) {
		other, ok := o.(*Player)
		if !ok || other == p {
			continue
		}
		if other.Obj.MapRef() == p.Obj.MapRef() && other.Obj.Distance(p.Obj.PosX(), p.Obj.PosY()) <= s.cfg.viewRange {
			inView[other.Obj.ID] = struct{}{}
		}
	}
	for _, o := range s.world.monsterIdx.InRange(p.Obj.PosX(), p.Obj.PosY(), s.cfg.viewRange) {
		m, ok := o.(*entity.Monster)
		if !ok || m.IsDead() {
			continue
		}
		if m.MapRef() == p.Obj.MapRef() && m.Distance(p.Obj.PosX(), p.Obj.PosY()) <= s.cfg.viewRange {
			inView[m.ID] = struct{}{}
		}
	}

	entered, left = p.visible.Update(inView)

	// 双向：别人对 p 的可见性（只收集，不发包）
	for _, other := range s.world.players {
		if other == p {
			continue
		}
		if other.Obj.MapRef() == p.Obj.MapRef() && other.Obj.Distance(p.Obj.PosX(), p.Obj.PosY()) <= s.cfg.viewRange {
			if other.visible.Add(p.Obj.ID) {
				appears = append(appears, other)
			}
		} else if other.visible.Remove(p.Obj.ID) {
			gone = append(gone, other)
		}
	}
	px, py = p.Obj.PosX(), p.Obj.PosY()
	s.mu.Unlock()

	for _, id := range entered {
		s.sendAppear(p, id)
	}
	for _, id := range left {
		s.sendDisappear(p, id, px, py)
	}
	for _, other := range appears {
		s.sendPlayerAppear(other, p)
	}
	for _, other := range gone {
		s.sendDisappear(other, p.Obj.ID, px, py)
	}
}

func (s *Server) sendAppear(to *Player, id uint32) {
	s.mu.RLock()
	if pl := s.world.players[id]; pl != nil {
		s.mu.RUnlock()
		s.sendPlayerAppear(to, pl)
		return
	}
	m := s.world.monsters[id]
	s.mu.RUnlock()
	if m != nil {
		s.sendMonsterAppear(to, m)
	}
}

// sendPlayerAppear 通知 to：玩家 who 出现（SM_TURN + TCharDesc）。
//
// ⚠️ `who` 的坐标/外观可能正被**他自己的 goroutine** 改（移动/换装）⇒
// 用对象自带锁的**一致性快照**（`Place` + `Appearance`，见 entity/object.go）。
// 这里不需要 `s.mu`。
func (s *Server) sendPlayerAppear(to, who *Player) {
	if to == nil || who == nil || who.Obj == nil {
		return
	}
	_, x, y, dir := who.Obj.Place()
	feature, status := who.Obj.Appearance()
	desc := proto.CharDesc{Feature: feature, Status: status}
	s.send(to.conn, proto.SM_TURN, int32(who.Obj.ID), uint16(x), uint16(y),
		uint16(dir), string(desc.Append(nil)))
}

func (s *Server) sendMonsterAppear(to *Player, m *entity.Monster) {
	if to == nil || m == nil {
		return
	}
	// 加入对方视野（否则后续移动广播会被 visible 过滤掉）——
	// `ViewTracker` 自带锁，不必再拿 `s.mu`。
	to.visible.Add(m.ID)
	// 坐标/外观取对象自带锁的快照：怪物由 monsterai 在 ticker 上移动。
	_, mx, my, dir := m.Place()
	desc := proto.CharDesc{Feature: m.FeatureBits(), Status: 0}

	s.send(to.conn, proto.SM_TURN, int32(m.ID), uint16(mx), uint16(my),
		uint16(dir), string(desc.Append(nil)))
	// 名字单独一条 SM_USERNAME(42)：SM_TURN 的 TCharDesc 里**没有名字字段**
	//（原版也是这样，客户端 Actor.m_sUserName 由 SM_USERNAME 填）。
	// 宠物（召唤兽/诱惑）在这里就会带上 "(主人名)"。
	// 怪物名没有颜色（`GetCharColor` 对怪物为 0）⇒ Param/Tag/Series 全 0。
	s.send(to.conn, proto.SM_USERNAME, int32(m.ID), 0, 0, 0, s.showName(m))
}

func (s *Server) sendDisappear(to *Player, id uint32, x, y int) {
	s.send(to.conn, proto.SM_DISAPPEAR, int32(id), uint16(x), uint16(y), 0, "")
}

// ---------- 消息处理 ----------
func (s *Server) broadcastMove(p *Player, ident uint16) {
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		if other != p && other.visible.Contains(p.Obj.ID) {
			s.send(other.conn, ident, int32(p.Obj.ID), uint16(p.Obj.PosX()), uint16(p.Obj.PosY()), uint16(p.Obj.Facing()), "")
		}
	})
}

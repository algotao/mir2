package gamesvr

import (
	"encoding/binary"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/world"
)

// 地面事件（客户端**看得见**的那些：火墙的火、挖矿的碎石、圣言术的光幕…）。
//
// 协议逐字对照两侧：
//
//	原版翻译（ObjBase.pas:6259-6280）
//	  SM_SHOWEVENT: Recog = nParam1、Param = wParam（**事件类型**）、
//	                Tag = nParam2 低字（**x**）、Series = nParam3（**y**）、
//	                包体 = TShortMessage{Ident: HiWord(nParam2)}（**事件参数**）
//	  SM_HIDEEVENT: Recog = nParam1、Param = wParam、Tag = nParam2、Series = nParam3
//	客户端（ClMain.pas:4391-4400）
//	  SM_SHOWEVENT: `TClEvent.Create(msg.Recog, Loword(msg.Tag){x}, msg.Series{y},
//	                 msg.Param{e-type})`，再把包体里的 `smsg.Ident` 读进
//	                 `m_nEventParam`
//	  SM_HIDEEVENT: `EventMan.DelEventById(msg.Recog)` ← **按 Recog 删**
//
// ⇒ 事件必须有**唯一 id**（原版直接拿事件对象指针当 id）：显示时放在 Recog、
// 隐藏时原样回传。我们用自己的自增号。
//
// ⚠️ 我们原来只发 `SM_MAGICFIRE`（起手动画），**没发这两条** ⇒ 真客户端画不出火：
// 玩家会站在"看不见的火墙"里掉血（2026-10-06 补）。
const (
	evDigOutZombi = 1 // 僵尸从地里钻出来
	evMine        = 2 // 矿脉
	evPileStones  = 3 // 挖矿的碎石堆
	evHolyCurtain = 4 // 圣言术的光幕
	evFire        = 5 // 火墙
	evSculpeice   = 6 // 雕像碎块
)

// nextEventID 分配地面事件 id（从 1 起，0 表示"没有事件"）。
func (s *Server) nextEventID() uint32 {
	return uint32(s.eventSeq.Add(1))
}

// showGroundEvent 让视野内的人在 (x,y) 看到一种地面事件，返回事件 id。
//
// `param` 是事件参数（写进包体 `TShortMessage.Ident`，客户端读成 `m_nEventParam`）。
func (s *Server) showGroundEvent(m *world.Map, x, y int, typ, param uint16) uint32 {
	id := s.nextEventID()
	s.announceGroundEvent(m, x, y, id, typ, param)
	return id
}

// announceGroundEvent 把"某格有个地面事件（id 已分配）"发给视野内的人。
//
// ⚠️ **绝不能在持有 `s.mu` 时调用**：`broadcastToViewers` 会拿 `s.mu.RLock`，
// 同一个 goroutine 再 RLock 就是**自死锁**（2026-10-06 在 castWall 里踩过：
// 铺墙持写锁、回调里直接广播 ⇒ 整台服务器的施法全卡住）。
// 正确姿势是"锁内只分配 id，锁外广播"（见 wall.go 的 castWall / wallTick）。
func (s *Server) announceGroundEvent(m *world.Map, x, y int, id uint32, typ, param uint16) {
	if m == nil || id == 0 {
		return
	}
	// 包体就是 `TShortMessage`：Ident（Word）+ wMsg（Word）= 4 字节。
	body := make([]byte, 4)
	binary.LittleEndian.PutUint16(body[0:], param)
	s.broadcastToViewers(m, x, y, func(o *Player) {
		s.send(o.conn, proto.SM_SHOWEVENT, int32(id), typ, uint16(x), uint16(y), string(body))
	})
}

// hideGroundEvent 让视野内的人按 id 抹掉这个事件。
func (s *Server) hideGroundEvent(m *world.Map, x, y int, id uint32) {
	if m == nil || id == 0 {
		return
	}
	s.broadcastToViewers(m, x, y, func(o *Player) {
		s.send(o.conn, proto.SM_HIDEEVENT, int32(id), 0, uint16(x), uint16(y), "")
	})
}

// ---------- 有寿命的地面事件登记表 ----------
//
// 客户端抹事件是**按 id**（`EventMan.DelEventById(Recog)`）⇒ 服务端必须记住
// "哪个 id 在哪一格、什么时候到期"，到期才能发对应的 `SM_HIDEEVENT`。
// 两处用到：挖矿的碎石堆（piles.go）与困魔咒的光幕（下面的 holyCurtainCells）。
type groundEvent struct {
	// eventID 是发给客户端的那个 id。
	eventID uint32
	// until 是到期时刻；零值表示"不自动过期"。
	until time.Time
	// typ 是事件类型（ET_*），便于排查。
	typ uint16
	// layers 只有碎石堆用（原版 `m_nEventParam`，客户端自己也在递增）。
	layers int
}

// groundKey 按"地图 + 坐标 + 类型"定位一个地面事件。
//
// 原版 `GetEvent(Envir, nX, nY)` 按环境+坐标找事件、再比 `m_nEventType`
// （Event.pas:154-175）；键里带上类型，等价于"同格同类型只留一个"。
type groundKey struct {
	m    *world.Map
	x, y int
	typ  uint16
}

// placeGroundEvent 登记并广播一个有寿命的地面事件，返回登记记录（可忽略）。
//
// ⚠️ 调用方**不得**持有 `s.mu`（要发包，理由见 announceGroundEvent 的注释）。
func (s *Server) placeGroundEvent(m *world.Map, x, y int, typ, param uint16, life time.Duration) *groundEvent {
	if m == nil {
		return nil
	}
	k := groundKey{m: m, x: x, y: y, typ: typ}
	id := s.nextEventID()
	ev := &groundEvent{eventID: id, typ: typ}
	if life > 0 {
		ev.until = time.Now().Add(life)
	}

	s.mu.Lock()
	// ⚠️ 懒建：生产路径由 run.go 初始化这张表，但测试里手工构造的 Server 不会
	// ⇒ 不懒建的话这里直接 "assignment to entry in nil map" panic（真踩过）。
	if s.world.groundEvents == nil {
		s.world.groundEvents = make(map[groundKey]*groundEvent)
	}
	s.world.groundEvents[k] = ev
	s.mu.Unlock()

	s.announceGroundEvent(m, x, y, id, typ, param)
	return ev
}

// groundEventTick 把到期的地面事件抹掉（对应原版事件管理器按 `m_dwTime` 回收
// 并发 `SendRefMsg(RM_HIDEEVENT, …)`）。
func (s *Server) groundEventTick(now time.Time) {
	type goneEvent struct {
		m    *world.Map
		x, y int
		id   uint32
	}
	var hides []goneEvent
	s.mu.Lock()
	for k, ev := range s.world.groundEvents { // nil map 上 range 是安全的（零次）
		if ev.until.IsZero() || now.Before(ev.until) {
			continue
		}
		if ev.eventID != 0 {
			hides = append(hides, goneEvent{m: k.m, x: k.x, y: k.y, id: ev.eventID})
		}
		delete(s.world.groundEvents, k)
	}
	s.mu.Unlock()

	// 锁外发（与 wallTick 同一个约定）
	for _, h := range hides {
		s.hideGroundEvent(h.m, h.x, h.y, h.id)
	}
}

// holyCurtainCells 是困魔咒光幕围着目标的那 8 格。
//
// 逐字对照 `TMagicManager.MagMakeHolyCurtain`（Magic.pas:1251-1276）：
//
//	(nX-1, nY-2) (nX+1, nY-2)
//	(nX-2, nY-1)             (nX+2, nY-1)
//	(nX-2, nY+1)             (nX+2, nY+1)
//	(nX-1, nY+2) (nX+1, nY+2)
//
// 原版只在"至少定住一个目标"时才铺这 8 格（`if (Result > 0) and (MagicEvent <> nil)`）。
var holyCurtainCells = [8][2]int{
	{-1, -2}, {+1, -2}, {-2, -1}, {+2, -1},
	{-2, +1}, {+2, +1}, {-1, +2}, {+1, +2},
}

// placeHolyCurtain 在目标周围铺一圈光幕（可见的 `ET_HOLYCURTAIN`），
// 寿命与定身时长一致（原版 `nPower * 1000` 毫秒）。
//
// ⚠️ 调用方**不得**持有 `s.mu`（要发包）。
func (s *Server) placeHolyCurtain(m *world.Map, x, y int, life time.Duration) int {
	placed := 0
	for _, c := range holyCurtainCells {
		cx, cy := x+c[0], y+c[1]
		// 原版不判边界（事件照样建，只是客户端的格子在图外画不出来）；
		// 我们跳过越界格，省掉无意义的包。
		if !m.InBounds(cx, cy) {
			continue
		}
		s.placeGroundEvent(m, cx, cy, evHolyCurtain, 0, life)
		placed++
	}
	return placed
}

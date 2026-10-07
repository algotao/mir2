package gamesvr

import (
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// ⑧ 玩家开门（`CM_OPENDOOR`）+ ⑨ 小地图（`CM_WANTMINIMAP`）。
//
// 两件事都是"客户端问一句、服务端回一句"，原版分别在：
//
//	开门   `TPlayObject.ClientOpenDoor`（ObjBase.pas:17045-17070）
//	       + `TUserEngine.OpenDoor` / `CloseDoor`（UsrEngn.pas:2619-2645）
//	小地图 `TPlayObject.ClientGetMinMap`（:17985-17998）
//
// ⚠️ 门的"开关"其实是**地图格**：地图文件的每格带 `DoorIndex`（高位是"这是门"的标志、
// 低 7 位是门组号），关着时那一格不可走；开了就把整组格子设成可走，5 秒后再关回去。
// 我们的 `world.Map` 早就把这些位解析出来了（`Cell.IsDoor()`/`DoorGroup()`、
// `Map.SetBlock`），只是从来没接线 ⇒ 玩家点门没反应。
// hungerMax 是饥饿度上限（原版 `m_nHungerStatus := _MIN(5000, …)`，
// ObjBase.pas:23375）。
const hungerMax = 5000

const (
	// doorAutoCloseAfter 是门开多久自动关（原版 `UsrEngn.pas:2699`：
	// `(GetTickCount - Door.Status.dwOpenTick) > 5 * 1000`）。
	doorAutoCloseAfter = 5 * time.Second
	// doorReachRange 是我们对"够得着"的判据。原版 `ClientOpenDoor` 没做距离校验
	//（客户端只在 `CanMove` 失败时才发这条包），这里补一个 2 格的界，防隔空开门。
	doorReachRange = 2
)

// openDoor 是被打开的一扇门（一组同号的门格）。
type openDoor struct {
	m        *world.Map
	group    byte
	cells    [][2]int
	openedAt time.Time
}

// doorCellsAt 找出 (x,y) 所在**门组**的全部格子（同一扇门可能横跨好几格）。
func doorCellsAt(m *world.Map, x, y int) (byte, [][2]int, bool) {
	c, ok := m.CellAt(x, y)
	if !ok || !c.IsDoor() {
		return 0, nil, false
	}
	group := c.DoorGroup()
	w, h := m.Width(), m.Height()
	var cells [][2]int
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			cc, ok := m.CellAt(xx, yy)
			if ok && cc.IsDoor() && cc.DoorGroup() == group {
				cells = append(cells, [2]int{xx, yy})
			}
		}
	}
	return group, cells, len(cells) > 0
}

// handleOpenDoor 处理 `CM_OPENDOOR`（原版 `TPlayObject.ClientOpenDoor`）。
//
// 客户端 `ClMain.pas:2376` 发 `SendClientMessage(CM_OPENDOOR, door, dx, dy, 0)`：
// **Recog = 客户端侧的门序号**（我们用不上，按坐标定位）、Param=dx、Tag=dy。
func (s *Server) handleOpenDoor(c net.Conn, p *Player, pkt wire.Packet) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	x, y := int(pkt.Head.Param), int(pkt.Head.Tag)
	if absi(x-p.Obj.PosX()) > doorReachRange || absi(y-p.Obj.PosY()) > doorReachRange {
		return
	}
	group, cells, ok := doorCellsAt(p.Obj.MapRef(), x, y)
	if !ok {
		return
	}
	key := doorKey{m: p.Obj.MapRef(), group: group}
	s.mu.Lock()
	if s.world.doors == nil {
		s.world.doors = make(map[doorKey]*openDoor)
	}
	if d := s.world.doors[key]; d != nil {
		s.mu.Unlock()
		return // 已经开着（原版 `not Door.Status.boOpened` 才开）
	}
	s.world.doors[key] = &openDoor{m: p.Obj.MapRef(), group: group, cells: cells, openedAt: time.Now()}
	s.mu.Unlock()

	s.setDoorOpen(p.Obj.MapRef(), cells, true)
	// 原版 `SendDoorStatus(…, RM_DOOROPEN, …)` ⇒ 客户端 `Map.OpenDoor(param, tag)`
	//（ClMain.pas:4435：`SM_OPENDOOR_OK, msg.param{x}, msg.tag{y}`）。
	s.broadcastToViewers(p.Obj.MapRef(), x, y, func(o *Player) {
		s.send(o.conn, proto.SM_OPENDOOR_OK, 0, uint16(x), uint16(y), 0, "")
	})
	log.Printf("%s 打开 %s 的门 (%d,%d)，%d 格", p.Char.Name, p.Obj.MapRef().Name, x, y, len(cells))
}

// tickDoors 把开够 `doorAutoCloseAfter` 的门关回去（原版引擎每 tick 扫一遍，
// `UsrEngn.pas:2695-2700`；我们挂在 1 秒的 regenLoop 上，粒度差 1 秒，可接受）。
func (s *Server) tickDoors(now time.Time) {
	s.mu.Lock()
	var closing []*openDoor
	for k, d := range s.world.doors {
		if now.Sub(d.openedAt) < doorAutoCloseAfter {
			continue
		}
		closing = append(closing, d)
		delete(s.world.doors, k)
	}
	s.mu.Unlock()
	for _, d := range closing {
		s.setDoorOpen(d.m, d.cells, false)
		for _, cl := range d.cells {
			s.broadcastToViewers(d.m, cl[0], cl[1], func(o *Player) {
				s.send(o.conn, proto.SM_CLOSEDOOR, 0, uint16(cl[0]), uint16(cl[1]), 0, "")
			})
		}
		log.Printf("门自动关闭 %s (%d,%d)，%d 格", d.m.Name, d.cells[0][0], d.cells[0][1], len(d.cells))
	}
}

// setDoorOpen 开门 = 整组格子设成可走；关门 = 设回阻挡。
func (s *Server) setDoorOpen(m *world.Map, cells [][2]int, open bool) {
	if m == nil {
		return
	}
	for _, cl := range cells {
		m.SetBlock(cl[0], cl[1], !open)
	}
}

// doorKey 是"某张图上的某个门组"。
type doorKey struct {
	m     *world.Map
	group byte
}

// handleWantMinimap 处理 `CM_WANTMINIMAP`（原版 `TPlayObject.ClientGetMinMap`）：
//
//	nMinMap := m_PEnvir.nMinMap;   // 地图号 → 小地图编号（data/envir/MiniMap.txt）
//	if nMinMap > 0 then SM_READMINIMAP_OK(0, nMinMap, 0, 0, '')
//	else SM_READMINIMAP_FAIL(0, 0, 0, 0, '');
func (s *Server) handleWantMinimap(c net.Conn, p *Player) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	if n := s.cfg.miniMaps[p.Obj.MapRef().Name]; n > 0 {
		s.send(c, proto.SM_READMINIMAP_OK, 0, n, 0, 0, "")
		return
	}
	s.send(c, proto.SM_READMINIMAP_FAIL, 0, 0, 0, 0, "")
}

package gamesvr

import (
	"fmt"
	"log"
	"net"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/world"
)

// 城门（MainDoor）的开 / 关。
//
// 原版在 ObjMon2.pas:1013-1060，两套机制合起来才等于"门"：
//
//  1. **地图格阻挡** `SetMapXYFlag(nFlag)`：门占据"2 格宽 × 3 格高"的一片格，
//     按 nFlag 三态设置（`TEnvirnoment.SetMapXYFlag` 的 boFlag=True ⇒ chFlag=0 ⇒ **可通行**）：
//         nFlag=0（Open）：8 格可通行，其中 3 格"门框"再改回阻挡；
//         nFlag=1（Close）：全部阻挡；
//         nFlag=2（Die）：全部可通行（门破了随便走）。
//  2. **对象状态**：`m_boOpened` 决定外观与"能否被选为目标"——
//     ⚠️ **开门时 `m_boStoneMode := True`**（看起来反直觉，但石化就是"不可选为目标"，
//     ObjBase.pas:21491；开着的门当然不该能砍），关门时 False。
//
// 存档：`m_boOpened` 落回 SabukW.txt 的 `MainDoorOpen`（Castle.pas:476-506），
// 我们已经解析成 `storage.CastleUnit.Opened`（官方值 1 = 初始开）。

// doorFrameCells 是门开时要单独保持"阻挡"的那 3 格（原版 nFlag=0 分支的最后三行）。
func doorFrameCells(x, y int) [][2]int {
	return [][2]int{{x, y - 2}, {x + 1, y - 1}, {x + 1, y - 2}}
}

// doorBodyCells 是随门状态整体切换的 9 格（原版那 9 次 SetMapXYFlag(bo06)）。
//
// 其中 (x,y)、(x,y-1)、(x,y-2)、(x+1,y-1)、(x+1,y-2) 是门本体（5 格），
// 另外 4 格是门后/门侧的通道（(x-1,y)、(x-2,y)、(x-1,y-1)、(x-1,y+1)）。
func doorBodyCells(x, y int) [][2]int {
	return [][2]int{
		{x, y}, {x, y - 1}, {x, y - 2}, {x + 1, y - 1}, {x + 1, y - 2},
		{x - 1, y}, {x - 2, y}, {x - 1, y - 1}, {x - 1, y + 1},
	}
}

// applyCastleDoorCells 按三态（0=开 / 1=关 / 2=门破）设置城门的阻挡格。
//
// 逐行照抄 ObjMon2.pas:1013-1037，包括"先设可通行、开门时再改回阻挡"的顺序 ——
// 顺序本身不影响结果（同一批格子），照抄是为了以后核对方便。
func (s *Server) applyCastleDoorCells(mp *world.Map, x, y, nFlag int) {
	if mp == nil {
		return
	}
	set := func(cx, cy int, passable bool) {
		if mp.InBounds(cx, cy) {
			mp.SetBlock(cx, cy, !passable)
		}
	}
	for _, c := range doorFrameCells(x, y) {
		set(c[0], c[1], true)
	}
	passable := nFlag != 1 // 关（1）⇒ 阻挡；开（0）/破（2）⇒ 可通行
	for _, c := range doorBodyCells(x, y) {
		set(c[0], c[1], passable)
	}
	if nFlag == 0 {
		for _, c := range doorFrameCells(x, y) {
			set(c[0], c[1], false)
		}
	}
}

// setCastleDoorOpened 开关城门（对应 TCastleDoor.Open/Close，ObjMon2.pas:1039-1060）。
//
// 返回是否真的改动了状态（门不存在/已经是该状态时返回 false）。
func (s *Server) setCastleDoorOpened(cs *castle.Castle, opened bool) bool {
	door := s.castleUnitByKind(storage.CastleMainDoor, 0)
	if door == nil {
		logpvp("城堡 %s 没有城门实体，忽略开关门", cs.Name())
		return false
	}
	if door.DoorOpened == opened {
		return false
	}
	// HP 分档决定关门后的朝向（原版 `m_btDirection := 3 - Round(HP/MaxHP*3)`）
	// 我们只把它记进日志：朝向对玩法无影响。
	ratio := 0.0
	if door.MaxHP > 0 {
		ratio = float64(door.HP) / float64(door.MaxHP)
	}
	door.DoorOpened = opened
	door.StoneMode = opened // ⚠️ 原版就是这么反的：开着的门"石化"= 不可选为目标
	if opened {
		s.applyCastleDoorCells(door.MapRef(), door.PosX(), door.PosY(), 0)
	} else {
		s.applyCastleDoorCells(door.MapRef(), door.PosX(), door.PosY(), 1)
	}
	cs.SetDoorOpened(opened)
	s.broadcastCastleUnitRefresh(door)
	verb := map[bool]string{true: "打开", false: "关闭"}[opened]
	log.Printf("城堡 %s 城门%s（HP %.0f%%，%d,%d）", cs.Name(), verb, ratio*100, door.PosX(), door.PosY())
	return true
}

// applyCastleDoorState 按配置/存档里的状态给城门"落一次"（初始化与热重载时调）。
//
// 与 setCastleDoorOpened 的区别：那个是"玩家操作触发的状态切换"（会比对当前状态），
// 这个是无条件的初始化 —— 进程刚起来时实体上的 DoorOpened 还是零值（false），
// 而存档里可能写着"开着"，所以必须照配置强行设一次。
func (s *Server) applyCastleDoorState(cs *castle.Castle) {
	door := s.castleUnitByKind(storage.CastleMainDoor, 0)
	if door == nil {
		return
	}
	opened := cs.DoorOpened()
	door.DoorOpened = opened
	door.StoneMode = opened
	if opened {
		s.applyCastleDoorCells(door.MapRef(), door.PosX(), door.PosY(), 0)
	} else {
		s.applyCastleDoorCells(door.MapRef(), door.PosX(), door.PosY(), 1)
	}
	log.Printf("城堡 %s 城门初始状态：%s（%d,%d）", cs.Name(),
		map[bool]string{true: "开", false: "关"}[opened], door.PosX(), door.PosY())
}

// castleDoorDestroyed 门被打碎（原版 TCastleDoor.Die → SetMapXYFlag(2)）：
// 全部格改成可通行，谁都能走进沙巴克。
func (s *Server) castleDoorDestroyed(door *entity.Monster) {
	if door == nil || door.CastleKind != storage.CastleMainDoor {
		return
	}
	door.DoorOpened = false
	door.StoneMode = false
	s.applyCastleDoorCells(door.MapRef(), door.PosX(), door.PosY(), 2)
	log.Printf("城门被击碎（%d,%d），城墙缺口打通", door.PosX(), door.PosY())
}

// openMainDoor / closeMainDoor 是 `@@openmaindoor` / `@@closemaindoor` 的实现
// （ObjNpc.pas:557-583 的两个标签 → Castle.MainDoorControl，Castle.pas:1136-1148）。
func (s *Server) mainDoorControl(c net.Conn, p *Player, close bool) bool {
	cs, err := s.castleDefault()
	if err != nil {
		return false
	}
	if !s.setCastleDoorOpened(cs, !close) {
		// 已经是该状态（原版 MainDoorControl 也是"已经开着/关着就什么都不做"）
		state := map[bool]string{true: "已经是打开状态", false: "已经是关闭状态"}[!close]
		s.sysMsg(c, fmt.Sprintf("城门%s", state))
		return true
	}
	s.sysMsg(c, map[bool]string{true: "城门已关闭", false: "城门已打开"}[close])
	return true
}

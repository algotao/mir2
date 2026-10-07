package gamesvr

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// markDoor 把某格标成"门"并设成阻挡（真实地图文件就是这么存门的：
// `Cell.DoorIndex` 高位是标志、低 7 位是门组号）。
func markDoor(m *world.Map, x, y int, group byte) {
	i := m.Index(x, y)
	c := m.Cells[i]
	c.DoorIndex = world.DoorBit | group
	m.Cells[i] = c
	m.SetBlock(x, y, true)
}

// TestDoorOpenAndAutoClose 玩家开门（原版 `ClientOpenDoor` + `UserEngine.OpenDoor`）：
//
//	开门 ⇒ 整组门格变可走 + 广播 SM_OPENDOOR_OK；
//	过 `doorAutoCloseAfter`（5 秒）⇒ 自动关回去 + 广播 SM_CLOSEDOOR。
func TestDoorOpenAndAutoClose(t *testing.T) {
	s, m, p := goldTestServer()
	// 同一扇门横跨两格（门组号一样）
	markDoor(m, 4, 4, 3)
	markDoor(m, 4, 5, 3)
	// 另一扇门（不同组）不该被带着开
	markDoor(m, 6, 6, 9)
	if m.CanWalk(4, 4) || m.CanWalk(4, 5) {
		t.Fatal("门格初始应该是阻挡的")
	}

	p.Obj.SetPos(p.Obj.MapRef(), 4, 3)
	s.handleOpenDoor(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_OPENDOOR, 1, 4, 4, 0)})

	if !m.CanWalk(4, 4) || !m.CanWalk(4, 5) {
		t.Error("开门后同一门组的两格都该可走")
	}
	if m.CanWalk(6, 6) {
		t.Error("别的门组的门不该被带着开")
	}
	if len(s.world.doors) != 1 {
		t.Fatalf("应记下 1 扇开着的门，实际 %d", len(s.world.doors))
	}
	// 已经开着再点一次：不重复记
	s.handleOpenDoor(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_OPENDOOR, 1, 4, 4, 0)})
	if len(s.world.doors) != 1 {
		t.Errorf("重复开门不该多记，实际 %d", len(s.world.doors))
	}

	// 没到时间不关
	s.tickDoors(time.Now())
	if !m.CanWalk(4, 4) {
		t.Error("没到 5 秒不该关门")
	}
	// 到点 ⇒ 关回去
	s.tickDoors(time.Now().Add(doorAutoCloseAfter + time.Second))
	if m.CanWalk(4, 4) || m.CanWalk(4, 5) {
		t.Error("到点后门该重新阻挡")
	}
	if len(s.world.doors) != 0 {
		t.Errorf("关掉后状态该清空，实际 %d", len(s.world.doors))
	}
}

// TestOpenDoorRange 隔空开不了（原版客户端只在走不动时才发，服务端补 2 格的界）。
func TestOpenDoorRange(t *testing.T) {
	s, m, p := goldTestServer()
	markDoor(m, 15, 15, 1)
	p.Obj.SetPos(p.Obj.MapRef(), 4, 4)
	s.handleOpenDoor(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_OPENDOOR, 1, 15, 15, 0)})
	if m.CanWalk(15, 15) {
		t.Error("离门 11 格远不该能开")
	}
	// 不是门的那一格也开不了
	s.handleOpenDoor(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_OPENDOOR, 1, 5, 5, 0)})
	if len(s.world.doors) != 0 {
		t.Error("普通格子不该被当成门")
	}
}

// TestLoadMiniMap 解析官方 `MiniMap.txt`（`D001  1` 这种两列）。
func TestLoadMiniMap(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "MiniMap.txt")
	content := ";注释\nD001  1\nD011  4\n\n坏行\nD999  x\n"
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}
	mm, err := data.LoadMiniMap(f)
	if err != nil {
		t.Fatalf("LoadMiniMap: %v", err)
	}
	if len(mm) != 2 || mm["D001"] != 1 || mm["D011"] != 4 {
		t.Errorf("解析结果 = %v，期望 D001→1 / D011→4", mm)
	}
}

// TestEatFoodAddsHunger `StdMode = 1` 的食物加饥饿度（原版 ObjBase.pas:23373-23380）：
// `Inc(m_nHungerStatus, StdItem.DuraMax div 10)`、上限 5000，并且**食物被吃掉一件**。
func TestEatFoodAddsHunger(t *testing.T) {
	meat := wuItem(1, "干肉", 1, data.MinMax{})
	meat.DuraMax = 1000
	s, p := eatTestServer(t, meat)
	p.Char.Data.BagItems[0] = pbUserItemForTest(1)
	p.Char.Data.HungerStatus = 100

	s.handleEat(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, 0, 0, 0, 0)})
	if p.Char.Data.HungerStatus != 200 {
		t.Errorf("饥饿度 = %d，期望 200（100 + 1000/10）", p.Char.Data.HungerStatus)
	}
	if got := countBagItems(p); got != 0 {
		t.Errorf("食物该被吃掉，背包还剩 %d 件", got)
	}

	// 上限 5000
	p.Char.Data.BagItems[0] = pbUserItemForTest(1)
	p.Char.Data.HungerStatus = 4999
	s.handleEat(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, 0, 0, 0, 0)})
	if p.Char.Data.HungerStatus != hungerMax {
		t.Errorf("饥饿度应被钳到 %d，实际 %d", hungerMax, p.Char.Data.HungerStatus)
	}
}

// pbUserItemForTest 造一件指向物品表第 idx 件的背包物品。
func pbUserItemForTest(idx uint32) *pb.UserItem {
	return &pb.UserItem{Index: idx, MakeIndex: int32(idx), Dura: 1, DuraMax: 1}
}

// countBagItems 数背包里还有几件非空物品。
func countBagItems(p *Player) int {
	n := 0
	for _, it := range p.Char.Data.BagItems {
		if it != nil && it.Index != 0 {
			n++
		}
	}
	return n
}

// TestOpenDoorOnRealMap 用**真地图**跑一遍开门（`data/map/0.map`，比奇省，95 个门格）。
//
// ⚠️ 只用手搓的门位等于自造一个"够不着的替代品"：这条用例证明**门位确实是从地图
// 文件里读出来的**（`world.Parse` 解析 `Cell.DoorIndex`），开门/关门也作用在真格子上。
func TestOpenDoorOnRealMap(t *testing.T) {
	raw, err := os.ReadFile("../../data/map/0.map")
	if err != nil {
		t.Skipf("地图文件不可读（跳过）: %v", err)
	}
	m, err := world.Parse("0", raw)
	if err != nil {
		t.Fatalf("解析真地图失败: %v", err)
	}
	var dx, dy int
	found := false
	for y := 0; y < m.Height() && !found; y++ {
		for x := 0; x < m.Width(); x++ {
			if c, ok := m.CellAt(x, y); ok && c.IsDoor() {
				dx, dy, found = x, y, true
				break
			}
		}
	}
	if !found {
		t.Fatal("真地图 0.map 里应该有门格（脚本扫到 95 个）")
	}
	before := m.CanWalk(dx, dy)

	s, _, p := goldTestServer()
	p.Obj.SetPlace(m, dx+1, dy, p.Obj.Facing())
	s.handleOpenDoor(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_OPENDOOR, 1, uint16(dx), uint16(dy), 0)})
	if !m.CanWalk(dx, dy) {
		t.Fatalf("真地图上那扇门 (%d,%d) 开门后该可走", dx, dy)
	}
	s.tickDoors(time.Now().Add(doorAutoCloseAfter + time.Second))
	if m.CanWalk(dx, dy) != before {
		t.Errorf("关门后该回到原状（原本可走=%v）", before)
	}
}

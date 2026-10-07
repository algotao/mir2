package gamesvr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// flagTestServer 造一个"已知若干张图的标记"的服务器 + 一张 50×50 地图。
//
// 用官方式写法喂 LoadMapInfo（段头 + 同行属性），保证跑的是真解析路径。
func flagTestServer(t *testing.T, mapinfo string) (*Server, *world.Map) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "mapinfo.txt")
	if err := os.WriteFile(p, []byte(mapinfo), 0o600); err != nil {
		t.Fatalf("写临时 mapinfo 失败: %v", err)
	}
	infos, err := data.LoadMapInfo(p)
	if err != nil {
		t.Fatalf("解析 mapinfo 失败: %v", err)
	}
	s := testSlaveServer()
	s.social.guilds = nil
	s.indexMapInfos(infos)
	m := world.Generate("noflag", 50, 50, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	// 让地图号 = 段头里的第一个字段：把这张图登记成 "0"（Map.Name 就是地图号）
	m.Name = "0"
	mm.Put(m)
	mm.SetNames(map[string]string{"0": "noflag"})
	s.world.maps = mm
	if s.world.index == nil {
		s.world.index = world.NewSpatialIndex(32)
	}
	if s.world.ground == nil {
		s.world.ground = make(map[uint32]*GroundItem)
	}
	return s, m
}

// TestMapFlagOf 钉住"按地图号取标记"这条桥：Map.Name 存的是地图号。
func TestMapFlagOf(t *testing.T) {
	s, m := flagTestServer(t, `[0 比奇省 0] SAFE NORECALL
[F001 行会战争地图1 0] FIGHT3 DARK
`)
	if mi := s.mapFlagOf(m); mi == nil || !mi.NoRecall || !mi.Safe {
		t.Fatalf("地图 0 的标记 = %+v，期望 SAFE + NORECALL", mi)
	}
	if s.mapFlagOf(nil) != nil {
		t.Error("nil 地图应返回 nil")
	}
	other := world.Generate("9999", 10, 10, false) // 地图号不在 mapinfo 里 ⇒ 查不到
	if s.mapFlagOf(other) != nil {
		t.Error("地图号对不上时应返回 nil")
	}
}

// TestScrollRandomBlockedOnNoRandomMoveMap 随机传送卷在标了 NORANDOMMOVE 的图上
// **不生效**（原版 ObjBase.pas:23539，Result 保持 False ⇒ 卷不消耗）。
func TestScrollRandomBlockedOnNoRandomMoveMap(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0] NORANDOMMOVE\n")
	oil := &data.StdItem{Index: 1, Name: "随机传送卷", StdMode: 3, Shape: 2}
	set, err := data.NewStdItemSet([]*data.StdItem{oil})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}
	p := newTestPlayer(1, "卷轴测试", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.visible = entity.NewViewTracker()

	if s.eatUseItem(nil, p, oil) {
		t.Error("NORANDOMMOVE 图上随机传送卷应返回 false（不消耗）")
	}
	if p.Obj.PosX() != 10 || p.Obj.PosY() != 10 {
		t.Errorf("不该移动：(%d,%d)", p.Obj.PosX(), p.Obj.PosY())
	}
}

// TestEatBlockedOnNoDrugMap NODRUG 图上一切消耗品都用不了
// （官方 `EatItems` 第一行，ObjBase.pas:23330）⇒ 物品**不消耗**。
func TestEatBlockedOnNoDrugMap(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0] NODRUG\n")
	drug := &data.StdItem{Index: 1, Name: "金创药(小量)", StdMode: 0,
		AC: data.MinMax{Min: 20}}
	set, err := data.NewStdItemSet([]*data.StdItem{drug})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}
	p := newTestPlayer(1, "药品测试", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.Char.Data.BagItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 7, Dura: 5, DuraMax: 5}

	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, 0, 0, 0, 0)}
	s.handleEat(nil, p, pkt)

	if p.Char.Data.BagItems[0].Dura != 5 {
		t.Errorf("NODRUG 图上药品不该被消耗：数量 = %d", p.Char.Data.BagItems[0].Dura)
	}
}

// TestDropItemBlockedOnNoThrowItemMap NOTHROWITEM 图上丢不出去
// （官方 `:16202`/`:16233` 的 `if not m_boCanDrop or …NOTHROWITEM`）。
func TestDropItemBlockedOnNoThrowItemMap(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0] NOTHROWITEM\n")
	it := &data.StdItem{Index: 1, Name: "金创药(小量)", StdMode: 0}
	set, err := data.NewStdItemSet([]*data.StdItem{it})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}
	p := newTestPlayer(1, "丢弃测试", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.Char.Data.BagItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 9, Dura: 1, DuraMax: 1}

	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_DROPITEM, 9, 0, 0, 0), Body: it.Name}
	s.handleDropItem(nil, p, pkt)

	if p.Char.Data.BagItems[0].Index == 0 {
		t.Error("NOTHROWITEM 图上不该把物品丢出去")
	}
	if len(s.world.ground) != 0 {
		t.Errorf("NOTHROWITEM 图上不该产生地面物品：%d 件", len(s.world.ground))
	}
}

// TestDropItemThrowsToGround CM_DROPITEM 的正常路径：背包里那件被移走、
// 地上多出一件，且落点在**范围 1** 内（原版 `DropItemDown(…, 1, False, …)`）。
func TestDropItemThrowsToGround(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0]\n")
	it := &data.StdItem{Index: 1, Name: "金创药(小量)", StdMode: 0}
	set, err := data.NewStdItemSet([]*data.StdItem{it})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}
	p := newTestPlayer(1, "丢弃测试", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.Obj.ID = 5
	p.Char.Data.BagItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 9, Dura: 1, DuraMax: 1}

	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_DROPITEM, 9, 0, 0, 0), Body: it.Name}
	s.handleDropItem(nil, p, pkt)

	// ⚠️ 不能只看 `BagItems[0]`：`takeBagItem` 保持"紧凑前缀 + 空尾巴"，
	// 删掉第 0 格后后面的会左移过来（这里后面本来都是空的 ⇒ 第 0 格变 nil）。
	for _, x := range p.Char.Data.BagItems {
		if x != nil && x.MakeIndex == 9 {
			t.Error("丢出去之后背包里不该还有它")
		}
	}
	if len(s.world.ground) != 1 {
		t.Fatalf("地面物品数 = %d，期望 1", len(s.world.ground))
	}
	for _, gi := range s.world.ground {
		if gi.Map != m {
			t.Error("地面物品该在同一张图")
		}
		// 螺旋第一格是 (-1,-1)，所以范围 1 内
		if abs(gi.X-10) > 1 || abs(gi.Y-10) > 1 {
			t.Errorf("落点 (%d,%d) 超出范围 1", gi.X, gi.Y)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

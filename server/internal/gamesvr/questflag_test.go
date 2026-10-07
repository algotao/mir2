package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// TestQuestFlagReadWrite 旗标读写（官方 `TQuestFlag=array[0..127] of Byte`）：
// 老档案里 `QuestFlag` 可能是空/nil ⇒ 要能按需补齐，越界一律当 0。
func TestQuestFlagReadWrite(t *testing.T) {
	d := &pb.CharacterData{}
	if got := questFlag(d, 5); got != 0 {
		t.Errorf("未分配时读旗标 = %d，期望 0", got)
	}
	setQuestFlag(d, 5, 1)
	if got := questFlag(d, 5); got != 1 {
		t.Errorf("写过之后 = %d，期望 1", got)
	}
	if len(d.QuestFlag) != questFlagMax {
		t.Errorf("按需补齐后长度 = %d，期望 %d", len(d.QuestFlag), questFlagMax)
	}
	// 越界：读当 0、写忽略（原版数组越界会读到别的字段，不值得复刻）
	if got := questFlag(d, questFlagMax); got != 0 {
		t.Errorf("越界读 = %d，期望 0", got)
	}
	setQuestFlag(d, questFlagMax, 9)
	if len(d.QuestFlag) != questFlagMax {
		t.Error("越界写不该改长度")
	}
}

// TestNeedSetGate 进图门（原版 `EnterAnotherMap`，ObjBase.pas:20306-20310）：
// `NEEDSET_ON(n)` 要求旗标 n = 1，`NEEDSET_OFF(n)` 要求 = 0，没标的一律放行。
func TestNeedSetGate(t *testing.T) {
	d := &pb.CharacterData{}
	on := &data.MapInfo{ID: "3", NeedSetOn: true, NeedSetID: 7}
	off := &data.MapInfo{ID: "4", NeedSetOff: true, NeedSetID: 7}
	none := &data.MapInfo{ID: "0", NeedSetID: -1}

	if !needSetAllows(d, nil) || !needSetAllows(d, none) {
		t.Error("没标 NEEDSET 的图该放行")
	}
	if needSetAllows(d, on) {
		t.Error("旗标 7=0 时 NEEDSET_ON(7) 该挡住")
	}
	if !needSetAllows(d, off) {
		t.Error("旗标 7=0 时 NEEDSET_OFF(7) 该放行")
	}
	setQuestFlag(d, 7, 1)
	if !needSetAllows(d, on) {
		t.Error("旗标 7=1 时 NEEDSET_ON(7) 该放行")
	}
	if needSetAllows(d, off) {
		t.Error("旗标 7=1 时 NEEDSET_OFF(7) 该挡住")
	}
}

// TestNeedSetGateBlocksSwitchMap 走一遍真入口：`switchMap` 在门上失败时
// **不能**把玩家挪过去（原版是 `Exit`）。
func TestNeedSetGateBlocksSwitchMap(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	// 目标图标上 NEEDSET_ON(3)，玩家旗标保持 0
	if mi, ok := s.data.mapInfoByID["0"]; ok {
		mi.NeedSetOn = true
		mi.NeedSetID = 3
	} else {
		t.Fatal("测试服里没有地图 0 的 mapinfo")
	}
	fromX, fromY := p.Obj.PosX(), p.Obj.PosY()
	if err := s.switchMap(nil, p, "0", 10, 10); err == nil {
		t.Fatal("旗标不满足时 switchMap 该报错")
	}
	if p.Obj.PosX() != fromX || p.Obj.PosY() != fromY {
		t.Errorf("被挡下后坐标不该变：(%d,%d) → (%d,%d)", fromX, fromY, p.Obj.PosX(), p.Obj.PosY())
	}
	// 设上旗标 ⇒ 放行
	setQuestFlag(p.Char.Data, 3, 1)
	if err := s.switchMap(nil, p, "0", 10, 10); err != nil {
		t.Fatalf("旗标满足后该能进：%v", err)
	}
}

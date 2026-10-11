package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// takeOnPacket / takeOffPacket 按**官方字段**构造穿脱报文（P1-7）：
// Recog = 物品 MakeIndex、Param = 装备槽位、body = 物品名
// （客户端 ClMain.pas:3621-3635 `SendTakeOnItem(where, itmindex, itmname)`）。
func takeOnPacket(makeIdx int32, slot int, name string) wire.Packet {
	return wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_TAKEONITEM, makeIdx, uint16(slot), 0, 0),
		Body: name,
	}
}

func takeOffPacket(makeIdx int32, slot int, name string) wire.Packet {
	return wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_TAKEOFFITEM, makeIdx, uint16(slot), 0, 0),
		Body: name,
	}
}

// TestTakeOnOffByMakeIndex 穿/脱都用 MakeIndex 定位（P1-7）。
//
// 旧实现把 Param 当背包下标、把 Recog 当槽位 —— 官方客户端按 MakeIndex 发过来时
// 会穿到完全不相干的位置（或直接没反应），脱下的 Recog 也被当成槽位。
func TestTakeOnOffByMakeIndex(t *testing.T) {
	s, p := butchTestServer(t, shopItem())
	d := p.Char.Data
	d.BagItems[4] = &pb.UserItem{Index: 1, MakeIndex: 55, Dura: 10, DuraMax: 10}
	rec := &recordingConn{}
	p.conn = rec

	// ① 穿上：MakeIndex=55 那件 → 武器槽（SlotWeapon = 1）
	s.handleTakeOn(rec, p, takeOnPacket(55, proto.SlotWeapon, "测试剑"))
	if got := d.HumItems[proto.SlotWeapon]; got == nil || got.MakeIndex != 55 {
		t.Fatalf("穿上后武器槽 = %+v，期望 MakeIndex=55", got)
	}
	if d.BagItems[4] != nil && d.BagItems[4].Index != 0 {
		t.Errorf("穿上后原背包格没清空: %+v", d.BagItems[4])
	}
	if !hasPacket(rec, t, proto.SM_TAKEON_OK) {
		t.Error("没收到 SM_TAKEON_OK（官方客户端靠它把物品移进 g_UseItems）")
	}

	// ② 脱下：MakeIndex=55 + 武器槽
	rec.reset()
	s.handleTakeOff(rec, p, takeOffPacket(55, proto.SlotWeapon, "测试剑"))
	if got := d.HumItems[proto.SlotWeapon]; got != nil && got.Index != 0 {
		t.Fatalf("脱下后武器槽还有东西: %+v", got)
	}
	if countBagItemNamed(s, p, "测试剑") != 1 {
		t.Error("脱下后物品没回到背包")
	}
	if !hasPacket(rec, t, proto.SM_TAKEOFF_OK) {
		t.Error("没收到 SM_TAKEOFF_OK")
	}
}

// TestTakeOnOffRejectsForgedFields 服务端不能信客户端报的槽位/MakeIndex：
// 槽位与物品类型不符、MakeIndex 编造、脱下时两者不自洽 —— 都必须回 FAIL 且不动装备。
func TestTakeOnOffRejectsForgedFields(t *testing.T) {
	s, p := butchTestServer(t, shopItem())
	d := p.Char.Data
	d.BagItems[4] = &pb.UserItem{Index: 1, MakeIndex: 55, Dura: 10, DuraMax: 10}
	rec := &recordingConn{}
	p.conn = rec

	// ① 把武器往"衣服槽"塞：原版 `CheckUserItems(btWhere, StdItem)` 会拒
	s.handleTakeOn(rec, p, takeOnPacket(55, proto.SlotDress, "测试剑"))
	if d.HumItems[proto.SlotDress] != nil && d.HumItems[proto.SlotDress].Index != 0 {
		t.Error("槽位与物品类型不符时不该穿上")
	}
	if d.BagItems[4] == nil || d.BagItems[4].MakeIndex != 55 {
		t.Error("被拒后背包里那件应该原封不动")
	}
	if !hasPacket(rec, t, proto.SM_TAKEON_FAIL) {
		t.Error("没收到 SM_TAKEON_FAIL")
	}

	// ② MakeIndex 编造（999）：背包里没有这件
	rec.reset()
	s.handleTakeOn(rec, p, takeOnPacket(999, proto.SlotWeapon, "测试剑"))
	if d.HumItems[proto.SlotWeapon] != nil && d.HumItems[proto.SlotWeapon].Index != 0 {
		t.Error("MakeIndex 对不上时不该穿上")
	}
	if !hasPacket(rec, t, proto.SM_TAKEON_FAIL) {
		t.Error("没收到 SM_TAKEON_FAIL")
	}

	// ③ 脱下：先正常穿上，再用"槽位对、MakeIndex 错"的报文脱
	rec.reset()
	s.handleTakeOn(rec, p, takeOnPacket(55, proto.SlotWeapon, "测试剑"))
	rec.reset()
	s.handleTakeOff(rec, p, takeOffPacket(56, proto.SlotWeapon, "测试剑"))
	if got := d.HumItems[proto.SlotWeapon]; got == nil || got.MakeIndex != 55 {
		t.Error("MakeIndex 与槽位不自洽时不该脱下")
	}
	if !hasPacket(rec, t, proto.SM_TAKEOFF_FAIL) {
		t.Error("没收到 SM_TAKEOFF_FAIL")
	}

	// ④ 空槽脱下 ⇒ FAIL 码 -2（原版 `n10 := -2`）
	rec.reset()
	s.handleTakeOff(rec, p, takeOffPacket(0, proto.SlotRingR, "测试剑"))
	if !hasPacket(rec, t, proto.SM_TAKEOFF_FAIL) {
		t.Error("空槽脱下没收到 SM_TAKEOFF_FAIL")
	}
}

// hasPacket 判断记录下来的下行包里有没有某个消息号。
func hasPacket(rec *recordingConn, t *testing.T, ident uint16) bool {
	t.Helper()
	for _, pk := range rec.packets(t) {
		if pk.Head.Ident == ident {
			return true
		}
	}
	return false
}

// TestEquipSlotForActualStdModes 钉住本服物品表中实际可穿戴的 StdMode，尤其是
// 新手初始蜡烛（30）、靴子（62）、腰带（64）。
func TestEquipSlotForActualStdModes(t *testing.T) {
	for _, tc := range []struct {
		mode uint8
		want int
	}{
		{5, proto.SlotWeapon}, {6, proto.SlotWeapon},
		{10, proto.SlotDress}, {11, proto.SlotDress},
		{15, proto.SlotHelmet}, {16, proto.SlotHelmet},
		{19, proto.SlotNecklace}, {22, proto.SlotRingL}, {23, proto.SlotRingR},
		{24, proto.SlotArmRingL}, {26, proto.SlotArmRingR},
		{25, proto.SlotBujuk}, {30, proto.SlotRightHand},
		{62, proto.SlotBoots}, {64, proto.SlotBelt},
	} {
		if got := equipSlotFor(tc.mode); got != tc.want {
			t.Errorf("StdMode %d 装备槽 = %d，期望 %d", tc.mode, got, tc.want)
		}
	}
	for _, mode := range []uint8{0, 1, 4, 40, 43} {
		if got := equipSlotFor(mode); got != -1 {
			t.Errorf("不可穿戴 StdMode %d 不该有槽位，实得 %d", mode, got)
		}
	}
}

// TestProtoEquipUnequip 通过新协议入口穿/脱武器，并确认所有权威刷新都送到客户端。
func TestProtoEquipUnequip(t *testing.T) {
	item := shopItem()
	item.Shape = 1 // 非 0 外观，才能断言 EntityFeatureChanged
	s, p := butchTestServer(t, item)
	p.Char.Data.BagItems[4] = &pb.UserItem{Index: 1, MakeIndex: 55, Dura: 10, DuraMax: 10}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 64)}
	p.protoOut = sink
	ps := &protoSession{srv: s, player: p}

	if !ps.onEquipItem(&protocol.EquipItem{Slot: 4}) {
		t.Fatal("proto 穿戴应被接受")
	}
	if got := p.Char.Data.HumItems[proto.SlotWeapon]; got == nil || got.MakeIndex != 55 {
		t.Fatalf("武器槽 = %+v，期望 MakeIndex=55", got)
	}
	if got := p.Char.Data.BagItems[4]; got != nil && got.Index != 0 {
		t.Errorf("穿上后原背包格应空，实得 %+v", got)
	}
	if proto.FeatureWeapon(p.Obj.FeatureBits()) == 0 {
		t.Error("穿上武器后自己的 weapon feature 应更新")
	}
	var gotEquip, gotBag, gotAbility, gotFeature bool
	for len(sink.ch) > 0 {
		e := <-sink.ch
		switch e.Body.(type) {
		case *protocol.Envelope_EquippedItems:
			gotEquip = true
		case *protocol.Envelope_BagItems:
			gotBag = true
		case *protocol.Envelope_AbilityUpdate:
			gotAbility = true
		case *protocol.Envelope_EntityFeatureChanged:
			gotFeature = true
		}
	}
	if !gotEquip || !gotBag || !gotAbility || !gotFeature {
		t.Errorf("proto 穿戴刷新缺失：EquippedItems=%t BagItems=%t Ability=%t Feature=%t",
			gotEquip, gotBag, gotAbility, gotFeature)
	}

	if !ps.onUnequipItem(&protocol.UnequipItem{EquipSlot: proto.SlotWeapon}) {
		t.Fatal("proto 脱下应被接受")
	}
	if got := p.Char.Data.HumItems[proto.SlotWeapon]; got != nil && got.Index != 0 {
		t.Fatalf("脱下后武器槽应空，实得 %+v", got)
	}
	if n := countBagItemNamed(s, p, "测试剑"); n != 1 {
		t.Errorf("脱下后背包里测试剑 = %d 件，期望 1", n)
	}
}

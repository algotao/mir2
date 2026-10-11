package gamesvr

import (
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// onPickupItem 捡起地面物品（原版 `CM_PICKUP`，按格子认物；这里按 **ground_id**）。
func (ps *protoSession) onPickupItem(m *protocol.PickupItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.pickupGroundByID(nil, p, m.GetGroundId())
	return true
}

// onMoveItem 移动/交换背包两格（原版 `CM_MOVEITEM`；用户 2026-10-10 的包裹
// 操作规范第 2/3 条：放到空格 = 移动，放到有东西的格 = 交换）。
func (ps *protoSession) onMoveItem(m *protocol.MoveItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.doMoveItem(p, int(m.GetFrom()), int(m.GetTo()))
	return true
}

// onUseItem 使用背包物品（原版 `CM_EAT`；用户 2026-10-10 规范第 5 条：
// 包裹中双击物品 = 使用）。服务端吃/用完会发全量背包 + 能力值刷新。
func (ps *protoSession) onUseItem(m *protocol.UseItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.doEat(nil, p, int(m.GetSlot()))
	return true
}

// onEquipItem 穿背包格里的可穿戴物品（原版 `CM_TAKEONITEM`）。新协议的 slot 是
// **背包下标**；目标装备槽由服务端按 StdMode 推导，不能信客户端猜的槽位。
func (ps *protoSession) onEquipItem(m *protocol.EquipItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil || p.Char == nil || p.Char.Data == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	bagSlot := int(m.GetSlot())
	it := p.bagAt(bagSlot)
	if it == nil || it.Index == 0 || it.MakeIndex == 0 {
		ps.srv.notice(p, "包裹里没有这件装备")
		return true
	}
	tmpl := ps.srv.data.tables.Items.Get(int(it.Index) - 1)
	if tmpl == nil {
		ps.srv.notice(p, "这件物品无法穿戴")
		return true
	}
	equipSlot := equipSlotFor(tmpl.StdMode)
	if equipSlot < 0 {
		ps.srv.notice(p, "这件物品不能穿戴")
		return true
	}
	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TAKEONITEM, it.MakeIndex, uint16(equipSlot), 0, 0), Body: tmpl.Name}
	ps.srv.handleTakeOn(nil, p, pkt)
	return true
}

// onUnequipItem 脱下指定装备槽（原版 `CM_TAKEOFFITEM`）；MakeIndex 由服务端从槽位取。
func (ps *protoSession) onUnequipItem(m *protocol.UnequipItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil || p.Char == nil || p.Char.Data == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	slot := int(m.GetEquipSlot())
	if slot < 0 || slot >= len(p.Char.Data.HumItems) {
		ps.srv.notice(p, "装备槽无效")
		return true
	}
	it := p.Char.Data.HumItems[slot]
	if it == nil || it.Index == 0 || it.MakeIndex == 0 {
		ps.srv.notice(p, "这个装备槽是空的")
		return true
	}
	tmpl := ps.srv.data.tables.Items.Get(int(it.Index) - 1)
	name := ""
	if tmpl != nil {
		name = tmpl.Name
	}
	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TAKEOFFITEM, it.MakeIndex, uint16(slot), 0, 0), Body: name}
	ps.srv.handleTakeOff(nil, p, pkt)
	return true
}

// onRepairItem 修 `slot` 那件（原版 `CM_USERREPAIRITEM`）。
//
// 档位由消息里的 `special` 定（特修）；脚本头没声明 `@s_repair` 的商人会被
// `Server.doRepairSlot` 挡掉。修之前必须先走过 `@repair`/`@s_repair` 那个对话
// 入口（服务端会发 `RepairCostNotice` ⇒ 客户端才知道该进入"点物品修理"模式），
// 与 legacy 的 `SM_SENDUSERREPAIR` 打开修理窗是同一条次序。
func (ps *protoSession) onRepairItem(m *protocol.RepairItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	if p.dialog == nil {
		ps.srv.notice(p, "先找修理的商人对话（点 @repair / @s_repair）")
		return true
	}
	npc, _, ok := ps.srv.dialogMerchant(p, p.dialog.npcID)
	if !ok {
		ps.srv.notice(p, "不在修理的商人旁边（或这段对话已经关了）")
		return true
	}
	slot := int(m.GetSlot())
	ui := p.bagAt(slot)
	ps.srv.doRepairSlot(nil, p, npc, slot, ui, true, m.GetSpecial())
	return true
}

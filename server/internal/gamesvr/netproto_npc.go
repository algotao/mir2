package gamesvr

import (
	"log"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/protocol"
)

// onNpcClick 处理"点了个 NPC"（新协议，对应 legacy 的 `CM_CLICKNPC` / `handleClickNPC`）。
//
// ⚠️ 与 legacy 那条路的**唯一区别是出口**：proto 玩家的 legacy 下行会被 `protoDown`
// 丢弃（见 enterWorld 处的说明）⇒ 只发 legacy 时客户端一个字都收不到，表现就是
// 用户报的"点击 NPC 无法弹出对话"。对话正文与选项由 `Server.npcSay` 分流。
func (ps *protoSession) onNpcClick(n *protocol.NpcClick) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	id := uint32(n.GetNpcId())
	ps.srv.mu.RLock()
	npc, ok := ps.srv.world.monsters[id]
	ps.srv.mu.RUnlock()
	if !ok || !npc.IsNPC {
		return true // 点空了/点的不是 NPC：静默忽略
	}
	// 距离校验与 legacy 同一条：够不着就不给隔空对话
	if p.Obj.MapRef() != npc.MapRef() || p.Obj.Distance(npc.PosX(), npc.PosY()) > npcTalkRange {
		return true
	}
	def := ps.srv.npcDefOf(npc)
	if def == nil {
		def = &data.NPC{Name: npc.Name}
	}
	sc := ps.srv.npcScript(def.ID, p.Obj.MapRef().Name)
	if sc == nil {
		// 没有脚本 ⇒ 当**商人**开（legacy 的 `handleClickNPC` 同一条分支：`openShop`）
		ps.srv.openShop(nil, p, id)
		log.Printf("%s 点了没有脚本的 NPC %s（新协议，按商人开）", p.Char.Name, npc.Name)
		return true
	}
	ps.srv.startDialog(nil, p, sc, id)
	// ⚠️ **货架不在这里发**（用户 2026-10-10：「商店图应在"打开 交易市场"时弹出，
	// 而不是开启对话就出」）—— 商品列表等玩家点了脚本里的 @buy/@trading 入口
	//（`dlgSelectIndex` 的 case "buy"）才发。原版虽然点商人就发 `SM_SENDGOODSLIST`，
	// 但商店**窗口**是点入口才弹的 ⇒ 我们把"发数据"也推迟到同一时刻。
	log.Printf("%s 与 %s 开始对话（脚本 %s，新协议）", p.Char.Name, npc.Name, sc.Name)
	return true
}

// onNpcSelect 处理"选了第 index 项"（1-based，与原版的选项序号同义）。
func (ps *protoSession) onNpcSelect(n *protocol.NpcSelect) bool {
	p := ps.player
	if p == nil || p.dialog == nil {
		return true
	}
	ps.srv.dlgSelectIndex(nil, p, int(n.GetIndex())-1)
	return true
}

// onButch 挖肉（原版 `CM_BUTCH`）：目标必须是**死了的动物**、且在自己 2 格内。
//
// ⚠️ 与攻击分两条消息：攻击的 `AttackInput` 走 `handleAttack`，挖肉走这条
// （`doButch`）。判定/节流/给东西全在 `doButch` 里 —— 与 legacy 共用一份。
func (ps *protoSession) onButch(m *protocol.Butch) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	pos := m.GetPosition()
	// 新协议方向 = 原版 + 1（见 directionOf 的反向换算）
	dir := uint8(0)
	if d := int32(m.GetDirection()); d > 0 {
		dir = uint8(d - 1)
	}
	ps.srv.doButch(nil, p, uint32(m.GetTargetId()), int(pos.GetX()), int(pos.GetY()), dir)
	return true
}

// onNpcClose 关掉对话（原版 `@exit`）。
func (ps *protoSession) onNpcClose(n *protocol.NpcClose) bool {
	if ps.player != nil {
		ps.player.dialog = nil
	}
	return true
}

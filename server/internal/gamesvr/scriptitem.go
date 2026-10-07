package gamesvr

// NPC 脚本的"操作位"机制（ITEM 窗口的服务端侧）。
//
// # 这是什么
//
// 官方脚本 QFunction-0.txt 里有一组指令，做"改背包物品某字段再写回"的
// 原子操作，典型用法（QFunction-0.txt:230-238）：
//
//	LinkBagItem <$BagItemMakeIndex>   把背包某物品关联到操作位
//	GetItemFieldValue -1 name S1     读字段到 P 变量
//	GetItemFieldValue -1 idx S2
//	GetItemFieldValue -1 dura S3
//	GetItemFieldValue -1 duramax S4
//	ChangeItemDura -1 = <$Str(S4)> 0  改持久
//	UpdateItem -1                    提交（写回背包 + 下发 SM_BAGITEMS）
//	ClearLinkItem                    解除关联
//
// # 与原版的差异（都是"我们没做 UI"带来的必然结果）
//
// 原版每个玩家有一整个 ITEM 窗口（多个操作位），客户端要画那个窗口，
// 服务端与客户端之间有专门的同步消息。
//
// **本实现只做单槽**（一次关联一个物品），且**不发任何新协议**：
//   - 官方脚本的 4 处用法全部是 -1（单一位），单槽足够覆盖；
//   - 客户端 ITEM 窗口没做，也没有对应协议常量；
//   - UpdateItem 只需重发 SM_BAGITEMS 让客户端刷新背包，
//     效果与原版"写回后同步"一致。
//
// 多槽版本将来要加，只需把 linkedMakeIndex 换成数组 + 补同步协议。

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/script"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// linkSlotNone 是"未关联"的标记（MakeIndex 0 不会被分配）。
const linkSlotNone = 0

// itemFieldNames 是 GetItemFieldValue 支持的字段别名。
//
// 取自 QFunction-0.txt 的真实用法：name / idx / dura / duramax。
var itemFieldNames = map[string]string{
	"name": "name", "idx": "index", "index": "index",
	"dura": "dura", "duramax": "duramax",
}

// findBagItemByMakeIndex 按 MakeIndex 找背包物品，返回槽位（-1 = 没有）。
func (s *Server) findBagItemByMakeIndex(p *Player, makeIndex int32) int {
	if p == nil || p.Char == nil || p.Char.Data == nil || makeIndex == 0 {
		return -1
	}
	for i, ui := range p.Char.Data.BagItems {
		if ui != nil && ui.MakeIndex == makeIndex {
			return i
		}
	}
	return -1
}

// findLinkedSlot 在背包里定位当前关联物品的槽位。
func (p *Player) findLinkedSlot() int {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.linkedMakeIndex == linkSlotNone {
		return -1
	}
	for i, ui := range p.Char.Data.BagItems {
		if ui != nil && ui.MakeIndex == p.linkedMakeIndex {
			return i
		}
	}
	return -1
}

// linkedItem 取当前关联的物品（没有则 nil）。
func (p *Player) linkedItem() *pb.UserItem {
	idx := p.findLinkedSlot()
	if idx < 0 {
		return nil
	}
	return p.Char.Data.BagItems[idx]
}

// actLinkBagItem 实现 LinkBagItem <MakeIndex>。
func (s *Server) actLinkBagItem(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Char.Data == nil {
		return
	}
	mk, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 32)
	if err != nil {
		s.sysMsg(c, "LinkBagItem：物品 ID 不合法: "+args[0])
		return
	}
	idx := s.findBagItemByMakeIndex(p, int32(mk))
	if idx < 0 {
		s.sysMsg(c, fmt.Sprintf("背包里没有 ID=%d 的物品", mk))
		return
	}
	p.linkedMakeIndex = int32(mk)
	obs.Event("script_link_item", "player", p.Char.Name, "make_index", mk, "slot", idx)
}

// actClearLinkItem 实现 ClearLinkItem：解除关联。
func (s *Server) actClearLinkItem(c net.Conn, p *Player) {
	if p.linkedMakeIndex == linkSlotNone {
		return
	}
	p.linkedMakeIndex = linkSlotNone
	obs.Event("script_clear_link_item", "player", p.Char.Name)
}

// actGetItemFieldValue 实现 GetItemFieldValue <位> <字段> <变量>。
//
// 位固定 -1（官方用法），单槽所以不校验。字段读出来写进 P 变量。
func (s *Server) actGetItemFieldValue(c net.Conn, p *Player, args []string) {
	if len(args) < 3 || p.Char.Data == nil {
		return
	}
	field := strings.ToLower(strings.TrimSpace(args[1]))
	varName := strings.TrimSpace(args[2])

	ui := p.linkedItem()
	if ui == nil {
		return // 未关联时静默（原版也读不到）
	}
	kind, known := itemFieldNames[field]
	var v int64
	switch {
	case !known:
		// value0..value13：btValue 附加属性位（升级点数之类）
		if n, err := strconv.Atoi(strings.TrimPrefix(field, "value")); err == nil &&
			field != "value" && n >= 0 && n < len(ui.Value) {
			v = int64(ui.Value[n])
			break
		}
		s.sysMsg(c, "GetItemFieldValue：不支持的字段 "+field)
		return
	case kind == "name":
		// name 是字符串，脚本用 <$BagItemName> 取，不走整数变量。
		s.sysMsg(c, "GetItemFieldValue：name 请用 <$BagItemName> 读取")
		return
	case kind == "dura":
		v = int64(ui.Dura)
	case kind == "duramax":
		v = int64(ui.DuraMax)
	case kind == "index":
		v = int64(ui.Index)
	}
	if idx := varIndex(varName); idx >= 0 {
		newScriptCtx(s, p).SetVar(idx, v)
	}
}

// actChangeItemDura 实现 ChangeItemDura <位> <op> <值> [类型]。
//
// 官方用法（QFunction-0.txt:236 与 :253-254）：
//
//	ChangeItemDura -1 = <$Str(S4)> 0   把持久设成 duramax（修满）
//	ChangeItemDura -1 = <$Str(S4)> 1   改 DuraMax
//	ChangeItemDura -1 + 10 0           相对增减
//
// 第四段"类型"：0 = Dura（当前持久），1 = DuraMax（最大持久）。
// 语义从上面的真实用法反推——:253 用 1 改 DuraMax、:254 紧接着用 0
// 把 Dura 设成刚存进去的 DuraMax，正好是"改上限再修满"。
func (s *Server) actChangeItemDura(c net.Conn, p *Player, args []string) {
	if len(args) < 3 || p.Char.Data == nil {
		return
	}
	op := strings.TrimSpace(args[0])
	val, ok := script.ParseValue(strings.Join(args[1:2], " "), newScriptCtx(s, p))
	if !ok {
		return
	}
	toMax := len(args) >= 4 && strings.TrimSpace(args[3]) == "1"

	ui := p.linkedItem()
	if ui == nil {
		return
	}
	cur := int64(ui.Dura)
	if toMax {
		cur = int64(ui.DuraMax)
	}
	var next int64
	switch op {
	case "=":
		next = val
	case "+":
		next = cur + val
	case "-":
		next = cur - val
	default:
		s.sysMsg(c, "ChangeItemDura：不支持的运算符 "+op)
		return
	}
	if next < 0 {
		next = 0
	}
	if toMax {
		ui.DuraMax = uint32(next)
		if int64(ui.Dura) > next {
			ui.Dura = uint32(next) // Dura 不能超过 DuraMax
		}
	} else {
		ui.Dura = uint32(next)
	}
	obs.Event("script_change_item_dura", "player", p.Char.Name,
		"make_index", ui.MakeIndex, "dura", ui.Dura, "duramax", ui.DuraMax,
		"to_max", toMax)
}

// actUpdateItem 实现 UpdateItem <位>：把操作位的改动写回。
//
// 我们的改动是**就地**改在背包物品上的（不像原版要先在操作位副本上改），
// 所以 UpdateItem 只需重发 SM_BAGITEMS 让客户端刷新；顺带重发 Ability
// 因为持久变化会影响属性上限。
func (s *Server) actUpdateItem(c net.Conn, p *Player) {
	ui := p.linkedItem()
	if ui == nil {
		return
	}
	s.sendBagItems(c, p)
	if p.Char.Data.Abil != nil {
		// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
		s.applyWeights(p)
		abRaw := abilityFromPB(p.Char.Data.Abil).Bytes()
		s.send(c, proto.SM_ABILITY, int32(p.gold()),
			uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
		s.sendSubAbility(c, p)
	}
	obs.Event("script_update_item", "player", p.Char.Name, "make_index", ui.MakeIndex)
	log.Printf("%s UpdateItem 写回 MakeIndex=%d（Dura=%d/%d）",
		p.Char.Name, ui.MakeIndex, ui.Dura, ui.DuraMax)
}

// actAddFunItemDura 实现 AddFunItemDura <值>。
//
// 官方用法（QFunction-0.txt:126/135/144）配合 `<$UseItemName>` 给
// **当前使用中的装备**加持久，用于"给武器加耐久"这类 NPC 服务。
func (s *Server) actAddFunItemDura(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || p.Char.Data == nil {
		return
	}
	v, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil || v == 0 {
		return
	}
	hum := p.Char.Data.HumItems
	if len(hum) <= proto.SlotWeapon || hum[proto.SlotWeapon] == nil || hum[proto.SlotWeapon].Index == 0 {
		s.sysMsg(c, "没有装备武器")
		return
	}
	w := hum[proto.SlotWeapon]
	name := "武器"
	// ⚠️ 可堆叠物品的 Dura 是**数量**不是耐久，当成持久改会把一叠药变成
	// "满耐久的一件"（项目里踩过，见 handleRepairItem 的注释）。
	if t := s.data.tables.Items.Get(int(w.Index) - 1); t != nil {
		if t.StackLimit() > 0 {
			s.sysMsg(c, "当前物品不是装备，无法加持久")
			return
		}
		name = t.Name
	}
	w.DuraMax += uint32(v)
	if int32(w.Dura) > int32(w.DuraMax) {
		w.Dura = w.DuraMax
	}
	s.sendUseItems(c, p)
	s.sysMsg(c, fmt.Sprintf("%s 持久 +%d（当前 %d/%d）", name, v, w.Dura, w.DuraMax))
	obs.Event("script_add_fun_item_dura", "player", p.Char.Name, "delta", v,
		"duramax", w.DuraMax)
}

package gamesvr

import (
	"log"
	"net"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// 仓库（Storage）。
//
// 原版三处拼起来看：
//
//  1. **入口**是商人对话里的 `@storage` / `@getback` 选项（NPC 脚本头行
//     `(@trading @buy @sell @storage @getback …)` 声明，LocalDB.pas:3095-3098）——
//     1.76 **没有** CM_OPENSTORAGE 这种包。选项分发在 ObjNpc.pas:1560-1575，
//     `Storage()`/`GetBack()` 在 :1497-1504。
//  2. **协议**：CM_USERSTORAGEITEM(1031) / CM_USERTAKEBACKSTORAGEITEM(1032) ↑，
//     SM_SENDUSERSTORAGEITEM(700) / SM_SAVEITEMLIST(704) /
//     SM_STORAGE_OK..FAIL(701-703) / SM_TAKEBACKSTORAGEITEM_OK..FULLBAG(705-707) ↓。
//  3. **业务**：ClientStorageItem / ClientTakeBackStorageItem（ObjBase.pas:24684-24842）。
//
// 存档：仓库物品在 `pb.CharacterData.storage_items`（随整个 proto 存进
// `characters.data` BLOB，SQL 不用改）。原版存档数组是 50 格
//（`TStorageItems = array[0..49]`，Grobal2.pas:810），但**只让存 39 件**
//（`m_StorageItemList.Count < 39`，ObjBase.pas:24720）——多出来的格子 1.76 没用上。
//
// ⚠️ 文档里记的 `CheckStorageOpen`/`OpenStoratge` 是 **GeeM2 的多页仓库扩展**：
// Delphi 全树不存在（只有官方 QFunction-0.txt 一个脚本在用）⇒ 不做，见 progress §2.3。
//
// 未建模（都因为缺少对应系统，不是漏抄）：
//   - **仓库锁/密码**：`m_boCanGetBackItem` + `@Lock/@UnLockStorage/@SetPassword` 那套，
//     开关是 `!setup.txt` 的 `PasswordLockSystem`，**官方值是 0**（关）
//     ⇒ 原版默认就是"不锁、直接可取"（`m_boCanGetBackItem` 恒真），我们不实现密码系统。
//   - **负重**：原版取回前有 `IsAddWeightAvailable`，我们还没有负重系统。
//   - **`g_FunctionNPC`**（跨地图功能 NPC）：我们只有普通 NPC，距离判定按原版 15 格。

const (
	// storageMax 是**可存入**的上限（原版 `m_StorageItemList.Count < 39`，ObjBase.pas:24720）。
	storageMax = 39
	// storageNpcRange 是「必须站在 NPC 附近」的判定半径（原版 `abs(dx) < 15`，ObjBase.pas:24716）。
	storageNpcRange = 15
)

// storageItems 返回仓库里的物品（紧凑列表，跳过空槽）。
//
// 原版运行时就是紧凑的 `m_StorageItemList`（TList），存档时按下标密排
// （ObjBase.pas:24974-24979）⇒ 我们也按紧凑处理，读档时顺手把历史空槽滤掉。
func storageItems(d *pb.CharacterData) []*pb.UserItem {
	if d == nil {
		return nil
	}
	out := make([]*pb.UserItem, 0, len(d.StorageItems))
	for _, it := range d.StorageItems {
		if it != nil && it.Index != 0 {
			out = append(out, it)
		}
	}
	return out
}

// setStorageItems 写回仓库（保持紧凑，避免存档里留空槽）。
func setStorageItems(d *pb.CharacterData, items []*pb.UserItem) {
	if d == nil {
		return
	}
	d.StorageItems = items
}

// scriptAllowsTag 判断 NPC 脚本头行（`(@trading @buy @storage …)`）是否声明了某功能。
//
// 对应原版的 `m_boStorage` / `m_boGetback` 标志（LocalDB.pas:3095-3098 把你头行里的
// `@storage`/`@getback` 置位）。头行是 `(@a @b …)` 形式，按空白切再剥括号。
func scriptAllowsTag(cmdLine, tag string) bool {
	tag = strings.ToLower(tag)
	for _, f := range strings.Fields(strings.ToLower(cmdLine)) {
		if strings.Trim(f, "()") == tag {
			return true
		}
	}
	return false
}

// storageDialogNPC 取"当前正在对话的那个 NPC"，并做原版的两项准入检查：
// 同地图 + 15 格内（`Merchant.m_PEnvir = m_PEnvir` 且 |dx|/|dy| < 15，ObjBase.pas:24716）。
//
// ⚠️ 距离检查**不在** NPC 侧而在存/取逻辑里（原版每件物品都重查一次），
// 所以这里返回 false 时调用方要按"没找到匹配"处理（回 FAIL）。
func (s *Server) storageDialogNPC(p *Player) (*entity.Monster, *data.NPC, bool) {
	return s.dialogMerchant(p, 0)
}

// dialogMerchant 是"服务端不可信"那条边界上的共用准入检查：报文里的商人 ActorId
// 必须**真的**指向一个与玩家同地图、在 15 格内、且当前正在对话的 NPC 商人。
//
// 原版三处经济路径各自查一遍同样的东西（ObjBase.pas:16111 卖出 / :16157 买入 /
// :24653 修理）：`UserEngine.FindMerchant(nParam1)` + `m_PEnvir = m_PEnvir` +
// `abs(dx)/abs(dy) < 15`（买入是 `> 15 Exit`，即允许正好 15）。
//
// ⚠️ 原版只查 `m_nCurMerchant`（客户端上次打开的那个商人），**不核对报文里的
// ActorId 是否就是它**；我们额外要求"当前对话上下文就是这个 NPC"（`p.dialog`
// 在点 NPC 时建立），把"没开过商店就凭空发买入包"这条口子一起堵上（P1-10）。
//
// actorID == 0 表示"用当前对话的那个 NPC"（仓库的老路径）。
func (s *Server) dialogMerchant(p *Player, actorID uint32) (*entity.Monster, *data.NPC, bool) {
	if p == nil || p.dialog == nil || p.Char == nil || p.Char.Data == nil {
		return nil, nil, false
	}
	if actorID == 0 {
		actorID = p.dialog.npcID
	} else if actorID != p.dialog.npcID {
		return nil, nil, false // 报文里的商人不是当前在谈的这个
	}
	s.mu.RLock()
	npc, ok := s.world.monsters[actorID]
	s.mu.RUnlock()
	if !ok || npc == nil || !npc.IsNPC {
		return nil, nil, false
	}
	if p.Obj.MapRef() != npc.MapRef() || p.Obj.Distance(npc.PosX(), npc.PosY()) >= storageNpcRange {
		return nil, nil, false
	}
	var def *data.NPC
	for _, n := range s.npc.defs {
		if n.Name == npc.Name {
			def = n
			break
		}
	}
	if def == nil {
		return npc, nil, false // 没脚本定义 = 不是商人
	}
	return npc, def, true
}

// merchantScriptAllows 判断"这个商人的脚本头是否声明了某功能"（原版 `m_boBuy`
// 这类标志就是脚本头里 `@buy` 之类置位的）。
//
// ⚠️ 没有脚本头的 NPC（我们自造的商品列表）按"允许"处理：原版 NPC 都带脚本，
// 而我们的商品表是"按名字关键字近似生成"的（见 shop.go 顶部），若一律要求
// 脚本声明，没有脚本的零售 NPC 会连进货都进不了。有脚本时严格按声明走。
func (s *Server) merchantScriptAllows(p *Player, def *data.NPC, tag string) bool {
	if def == nil || def.ID == "" {
		return true
	}
	sc := s.npcScript(def.ID, p.Obj.MapRef().Name)
	if sc == nil {
		return true
	}
	return scriptAllowsTag(sc.CmdLine, tag)
}

// dialogAllows 判断"对话中的这个 NPC"的脚本头行是否声明了 tag 功能。
func (s *Server) dialogAllows(p *Player, tag string) bool {
	_, def, ok := s.storageDialogNPC(p)
	if !ok {
		return false
	}
	sc := s.npcScript(def.ID, p.Obj.MapRef().Name)
	if sc == nil {
		return false
	}
	return scriptAllowsTag(sc.CmdLine, tag)
}

// openStorage 对应原版 `TNormNpc.Storage`（ObjNpc.pas:1497-1500）：
// 通知客户端"打开这个商人的仓库界面"（SM_SENDUSERSTORAGEITEM，Recog = NPC ActorId）。
func (s *Server) openStorage(c net.Conn, p *Player, npcID uint32) {
	s.send(c, proto.SM_SENDUSERSTORAGEITEM, int32(npcID), 0, 0, 0, "")
	log.Printf("%s 打开仓库（NPC ActorId=%d）", p.Char.Name, npcID)
}

// sendStorageList 下发仓库列表（原版 `SendSaveItemList`，ObjBase.pas:22783-22816）。
//
// 布局：Recog = NPC ActorId、Param/Tag = 0、**Series = 物品件数**、
// body = 每件一个 ClientItem 再用 `/` 分隔（客户端 ClMain.pas:5651-5689 按 '/' 切）。
func (s *Server) sendStorageList(c net.Conn, p *Player, npcID uint32) {
	items := storageItems(p.Char.Data)
	body := make([]byte, 0, len(items)*(proto.ClientItemSize+1))
	n := 0
	for _, it := range items {
		ci, ok := s.buildClientItem(it)
		if !ok {
			continue
		}
		body = ci.Append(body)
		body = append(body, '/')
		n++
	}
	s.send(c, proto.SM_SAVEITEMLIST, int32(npcID), 0, 0, uint16(n), string(body))
	// 这条日志与"存入仓库"那条配对：出现"客户端说仓库 0 件"的偶发时，比对两条
	// 日志（角色名 + 件数）即可判定是**服务端当时确实空**还是**客户端读错**。
	log.Printf("%s 打开仓库列表：%d 件（StorageItems 原始 %d 条）",
		p.Char.Name, n, len(p.Char.Data.StorageItems))
}

// findBagByMakeIndex 按「MakeIndex 匹配 **且** 显示名匹配」在背包里找物品
// （原版 ClientStorageItem/ClientTakeBackStorageItem 的双因子判定，ObjBase.pas:24711/24800）。
func (s *Server) findBagByMakeIndex(p *Player, makeIndex int32, name string) int {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return -1
	}
	for i, it := range p.Char.Data.BagItems {
		if it == nil || it.Index == 0 || it.MakeIndex != makeIndex {
			continue
		}
		if strings.EqualFold(s.bagItemName(it), name) {
			return i
		}
	}
	return -1
}

// handleStorageDialog 处理对话选项 `@storage` / `@getback`
// （原版 ObjNpc.pas:1560-1575：两个选项各自要求 NPC 头行声明了对应功能）。
func (s *Server) handleStorageDialog(c net.Conn, p *Player, label string) bool {
	switch strings.ToLower(label) {
	case "@storage":
		if !s.dialogAllows(p, "@storage") {
			return false
		}
		s.openStorage(c, p, p.dialog.npcID)
		return true
	case "@getback":
		if !s.dialogAllows(p, "@getback") {
			return false
		}
		s.sendStorageList(c, p, p.dialog.npcID)
		return true
	}
	return false
}

// handleStorageDeposit 处理存入（CM_USERSTORAGEITEM）。
//
// 原版 `ClientStorageItem`（ObjBase.pas:24684-24750）的顺序：
//
//	① body 的物品名按第一个空格截断（信件物品名字后带使用次数）
//	② 在背包里找 MakeIndex + 名字都匹配的那件；找不到 ⇒ SM_STORAGE_FAIL
//	③ NPC 必须是"允许存"的商人、同图 15 格内
//	④ 仓库 < 39 ⇒ 存入 + SM_STORAGE_OK；满了 ⇒ SM_STORAGE_FULL
func (s *Server) handleStorageDeposit(c net.Conn, p *Player, m wire.Packet) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	// Param/Tag 是 MakeIndex 的低/高 16 位（客户端 ClMain.pas:3144-3150）
	makeIndex := int32(proto.MakeLong(uint16(m.Head.Param), uint16(m.Head.Tag)))
	name := dealItemName(m.Body)

	bagIdx := s.findBagByMakeIndex(p, makeIndex, name)
	if bagIdx < 0 {
		// ⚠️ 静默失败不可诊断：e2e 里表现为"仓库 0 件"，看不出是**存入被拒**。
		log.Printf("%s 存入仓库被拒：背包里找不到 %q(MakeIndex=%d)",
			p.Char.Name, name, makeIndex)
		s.send(c, proto.SM_STORAGE_FAIL, 0, 0, 0, 0, "")
		return
	}
	allowed := false
	if _, def, ok := s.storageDialogNPC(p); ok {
		if sc := s.npcScript(def.ID, p.Obj.MapRef().Name); sc != nil {
			allowed = scriptAllowsTag(sc.CmdLine, "@storage")
		}
	}
	if !allowed {
		log.Printf("%s 存入仓库被拒：当前对话的 NPC 不是可存商人（或对话已结束）", p.Char.Name)
		s.send(c, proto.SM_STORAGE_FAIL, 0, 0, 0, 0, "")
		return
	}
	items := storageItems(p.Char.Data)
	if len(items) >= storageMax {
		s.send(c, proto.SM_STORAGE_FULL, 0, 0, 0, 0, "")
		return
	}

	// 原版是把对象**从 m_ItemList 挪进 m_StorageItemList**（同一个对象换列表），
	// 所以这里也搬指针、把背包槽位清空，不要拷贝 pb 结构（它带锁，vet 会拦）。
	it := p.bagAt(bagIdx)
	items = append(items, it)
	setStorageItems(p.Char.Data, items)
	s.takeBagItem(p, bagIdx)
	// ⚠️ 多一条 `SM_BAGITEMS`：原版不发它（真客户端在拖动时就把物品从背包面板移走了，
	// 见 ClMain.pas:4604-4617 的 SM_STORAGE_OK 分支），我们与**自家卖出路径**
	//（shop.go:302）保持一致重发一次全量背包 —— 它是幂等的，真客户端也照收。
	s.sendBagItems(c, p)
	s.send(c, proto.SM_STORAGE_OK, 0, 0, 0, 0, "")
	log.Printf("%s 存入仓库 %s（MakeIndex=%d，仓库 %d/%d）",
		p.Char.Name, s.bagItemName(it), it.MakeIndex, len(items), storageMax)
}

// handleStorageTakeBack 处理取回（CM_USERTAKEBACKSTORAGEITEM）。
//
// 原版 `ClientTakeBackStorageItem`（ObjBase.pas:24751-24842）的顺序：
//
//	① NPC 必须存在（否则**静默**返回，不回包）
//	② 仓库锁 `m_boCanGetBackItem`（官方 !setup.txt 的 PasswordLockSystem=0 ⇒ 恒真，我们不做）
//	③ 负重够（我们还没有负重系统，跳过）
//	④ 仓库里找 MakeIndex + 名字匹配的那件
//	⑤ NPC 必须"允许取"、同图 15 格内
//	⑥ 背包放得下 ⇒ SM_ADDITEM + 移出仓库 + SM_TAKEBACKSTORAGEITEM_OK(Recog=MakeIndex)；
//	   放不下 ⇒ SM_TAKEBACKSTORAGEITEM_FULLBAG
//	⑦ 整轮没命中任何一件 ⇒ SM_TAKEBACKSTORAGEITEM_FAIL
func (s *Server) handleStorageTakeBack(c net.Conn, p *Player, m wire.Packet) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	// Param/Tag 是 MakeIndex 的低/高 16 位（客户端 ClMain.pas:3144-3150）
	makeIndex := int32(proto.MakeLong(uint16(m.Head.Param), uint16(m.Head.Tag)))
	name := dealItemName(m.Body)

	if _, _, ok := s.storageDialogNPC(p); !ok {
		return // 原版这里 Merchant = nil 直接 Exit（不回包）
	}

	items := storageItems(p.Char.Data)
	idx := -1
	for i, it := range items {
		if it.MakeIndex == makeIndex && strings.EqualFold(s.bagItemName(it), name) {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.send(c, proto.SM_TAKEBACKSTORAGEITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	if !s.dialogAllows(p, "@getback") {
		s.send(c, proto.SM_TAKEBACKSTORAGEITEM_FAIL, 0, 0, 0, 0, "")
		return
	}

	it := items[idx]
	if s.addToBag(p, it) < 0 {
		s.send(c, proto.SM_TAKEBACKSTORAGEITEM_FULLBAG, 0, 0, 0, 0, "")
		return
	}
	s.sendAddItem(p, it)
	items = append(items[:idx], items[idx+1:]...)
	setStorageItems(p.Char.Data, items)
	s.send(c, proto.SM_TAKEBACKSTORAGEITEM_OK, makeIndex, 0, 0, 0, "")
	log.Printf("%s 从仓库取回 %s（MakeIndex=%d，仓库剩 %d 件）",
		p.Char.Name, s.bagItemName(it), makeIndex, len(items))
}

package gamesvr

import (
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// §2.3 制药（脚本标签 `@makedrug` + `CM_USERMAKEDRUGITEM`）。
//
// ⚠️ 我在上一轮把这条判成"数据阻塞、不做"是**错的**：原版配方表 `g_MakeItemList`
// 看起来由 `LocalDB.pas:425-438` 从"数据库"加载，其实那个 DB 层读的就是
// **文本文件** `Envir/MakeItem.txt` —— 它一直在我们仓库里（59 条配方）。
//
// 整条链路（原版）：
//
//	① 玩家点 NPC 对话里的"制药"链接 ⇒ `TMerchant.MakeDurg`（ObjNpc.pas:1465-1492）
//	   把**该商人的商品**逐个发成 `名字/0/价格/1/` 串（RM_USERMAKEDRUGITEMLIST）。
//	② 客户端选一件 ⇒ `CM_USERMAKEDRUGITEM`（Recog=商人 ActorId，body=成品名，
//	   ClMain.pas:3176-3181）。
//	③ `TPlayObject.ClientMakeDrugItem`（ObjBase.pas:18000-18012）：同图 + 两轴
//	   距离都 < 15 + 这个商人 `m_boMakeDrug`（= 脚本头声明了 `@makedrug`）。
//	④ `TMerchant.ClientMakeDrugItem`（ObjNpc.pas:2276-2392）：
//	   成品必须在该商人**商品表**里 ⇒ 金币够（`MakeDurg=100`，!setup.txt:256）
//	   ⇒ 背包里凑齐配方材料（`sub_4A28FC` 数 `m_ItemList`）⇒ 入包 ⇒ 扣钱 ⇒ 回执。

// makeDrugFailText 把原版的失败码翻成文案（`TMerchant.ClientMakeDrugItem` 的 n14）。
func makeDrugFailText(code int) string {
	switch code {
	case 2:
		return "背包已满" // AddItemToBag 失败
	case 3:
		return "金币不足"
	case 4:
		return "材料不足"
	}
	return "制药失败"
}

// sendMakeDrugList 对应原版 `TMerchant.MakeDurg`：把商品逐个拼成 `名字/0/价格/1/`。
//
// ⚠️ 原版串为空时**不发**（`if sSENDMSG <> ”`，:1489）。
// 字段按 `RM_USERMAKEDRUGITEMLIST → SM_SENDUSERMAKEDRUGITEMLIST` 的映射
// （ObjBase.pas:6039-6047）走：Recog=0、Param=商人 ActorId。
func (s *Server) sendMakeDrugList(c net.Conn, p *Player, npc *entity.Monster, def *data.NPC) {
	goods := s.shopGoods(def)
	var b strings.Builder
	for _, it := range goods {
		fmt.Fprintf(&b, "%s/0/%d/1/", it.Name, s.cfg.makeDrugPrice)
	}
	if b.Len() == 0 {
		return
	}
	s.send(c, proto.SM_SENDUSERMAKEDRUGITEMLIST, 0, uint16(npc.ID), 0, 0, b.String())
	log.Printf("%s 打开 %s 的制药列表（%d 件商品，每件 %d 金币）",
		p.Char.Name, npc.Name, len(goods), s.cfg.makeDrugPrice)
}

// handleMakeDrug 处理 `CM_USERMAKEDRUGITEM`。
func (s *Server) handleMakeDrug(c net.Conn, p *Player, pkt wire.Packet) {
	itemName := strings.TrimSpace(pkt.Body)
	if itemName == "" || p == nil || p.Obj == nil {
		return
	}
	npcID := uint32(pkt.Head.Recog)
	s.mu.RLock()
	npc, ok := s.world.monsters[npcID]
	s.mu.RUnlock()
	if !ok || !npc.IsNPC || p.Obj.MapRef() != npc.MapRef() {
		return
	}
	// `abs(...) < 15`（原版两轴都判，且是**严格小于**）
	if absi(npc.PosX()-p.Obj.PosX()) >= 15 || absi(npc.PosY()-p.Obj.PosY()) >= 15 {
		log.Printf("%s 离 %s 太远，制药被拒（原版 |dx|/|dy| < 15）", p.Char.Name, npc.Name)
		return
	}
	// `m_boMakeDrug`：脚本头声明了 `@makedrug` 才行（原版由 LocalDB 按脚本置位）。
	if p.dialog == nil || !s.dialogAllows(p, "@makedrug") {
		log.Printf("%s 对 %s 制药被拒：该 NPC 的脚本头没声明 @makedrug（原版 m_boMakeDrug）",
			p.Char.Name, npc.Name)
		return
	}
	rec, ok := s.data.makeItems.Get(itemName)
	if !ok {
		s.makeDrugFail(c, p, 4)
		return
	}
	// 成品必须在该商人的**商品表**里（原版遍历 `m_GoodsList` 比对名字）。
	def := s.npcDefOf(npc)
	if def == nil || !s.goodsContains(def, itemName) {
		log.Printf("%s 想制 %s，但它不在 %s 的商品表里（原版遍历 m_GoodsList）",
			p.Char.Name, itemName, npc.Name)
		s.makeDrugFail(c, p, 4)
		return
	}
	d := p.Char.Data
	if d == nil {
		return
	}
	if p.gold() < int64(s.cfg.makeDrugPrice) {
		s.makeDrugFail(c, p, 3)
		return
	}
	// 材料是否齐（原版 `sub_4A28FC`：按 成品名 查配方，逐味数背包）。
	for _, need := range rec {
		if got := countBagByName(s, d, need.Name); got < need.Count {
			log.Printf("%s 制 %s 的材料不够：%s 需要 %d 件、背包里 %d 件",
				p.Char.Name, itemName, need.Name, need.Count, got)
			s.makeDrugFail(c, p, 4)
			return
		}
	}
	// ⚠️ 先把钱**原子扣掉**再做剩下的（原版把扣钱放在最后）。反过来的话，最后那次
	// 扣款可能因为并发收支算错 ⇒ 白送一份药。下面的失败路径统一退款。
	if !p.spendGold(int64(s.cfg.makeDrugPrice)) {
		s.makeDrugFail(c, p, 3)
		return
	}
	// ⚠️ 原版**先扣材料再入包**，入包失败（背包满）时材料已经没了 ⇒ 照抄
	//（`sub_4A28FC` 在 :2288-2327 扣完，`AddItemToBag` 在 :2334 才判）。
	takeBagByName(s, d, rec)
	tmpl := s.data.tables.Items.GetByName(itemName)
	if tmpl == nil {
		p.addGold(int64(s.cfg.makeDrugPrice))
		s.makeDrugFail(c, p, 4)
		return
	}
	ui := &pb.UserItem{
		MakeIndex: int32(s.itemSeq.Add(1)),
		Index:     uint32(tmpl.Index),
		Dura:      initialDura(tmpl),
		DuraMax:   tmpl.DuraMax,
	}
	if s.addToBag(p, ui) < 0 {
		p.addGold(int64(s.cfg.makeDrugPrice))
		s.makeDrugFail(c, p, 2)
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	s.sendBagItems(c, p)
	// 原版把成品当成"新得的物品"推给客户端（`SendAddItem` ⇒ SM_ADDITEM）。
	s.send(c, proto.SM_ADDITEM, int32(ui.MakeIndex), 0, 0, 0, "")
	s.sysMsg(c, fmt.Sprintf("制药成功：%s（花费 %d 金币）", itemName, s.cfg.makeDrugPrice))
	// `RM_MAKEDRUG_SUCCESS`（ObjBase.pas:6048-6056）：Param = **新**金币数。
	// ⚠️ 原版 `SendDefMessage(SM_MAKEDRUG_SUCCESS, nParam1, ...)` 的 Param 是**字**（16 位）
	// ⇒ 金币在这里本来就会被截断（原版也这样，TDefaultMessage 只有 16 位的位段）。
	s.send(c, proto.SM_MAKEDRUG_SUCCESS, 0, uint16(p.gold()), 0, 0, "")
	log.Printf("%s 在 %s 处制成 %s（-%d 金币，余 %d）",
		p.Char.Name, npc.Name, itemName, s.cfg.makeDrugPrice, p.gold())
}

// makeDrugFail 回 `SM_MAKEDRUG_FAIL`（Param = 原版失败码）+ 一条提示。
func (s *Server) makeDrugFail(c net.Conn, p *Player, code int) {
	s.send(c, proto.SM_MAKEDRUG_FAIL, 0, uint16(code), 0, 0, "")
	s.sysMsg(c, "制药失败："+makeDrugFailText(code))
}

// dialogNPC 取当前对话的 NPC（脚本标签都是"对着某个 NPC"说的，
// 而对话上下文只存了 ActorId）。
func (s *Server) dialogNPC(p *Player) *entity.Monster {
	if p == nil || p.dialog == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.world.monsters[p.dialog.npcID]
}

// countBagByName 数背包里某物品**有几件**（原版 `sub_4A28FC` 的计数那半）。
//
// ⚠️ 可堆叠物品在背包里是"一格一堆、数量记在 `Dura` 上"（仓库的老约定，见 eatuse.go）
// ⇒ 必须**按数量**加。照原版那样按 `m_ItemList` 的**格子数**加，一叠 4 片叶子会被算成
// 1 件 ⇒ 制药永远"材料不足"（2026-10-06 在 butch 用例里撞到）。
func countBagByName(s *Server, d *pb.CharacterData, name string) int {
	n := 0
	for _, it := range d.BagItems {
		if it == nil || it.Index == 0 {
			continue
		}
		tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
		if tmpl == nil || tmpl.Name != name {
			continue
		}
		if tmpl.StackLimit() > 0 {
			if it.Dura <= 0 {
				n++
			} else {
				n += int(it.Dura)
			}
			continue
		}
		n++
	}
	return n
}

// takeBagByName 按配方扣材料（从后往前删，避免下标错乱）。
//
// 可堆叠的按数量扣（扣到 0 才移除那一格），不可堆叠的一件一格。
func takeBagByName(s *Server, d *pb.CharacterData, rec []data.MakeItem) {
	for _, need := range rec {
		left := need.Count
		for i := len(d.BagItems) - 1; i >= 0 && left > 0; i-- {
			it := d.BagItems[i]
			if it == nil || it.Index == 0 {
				continue
			}
			tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
			if tmpl == nil || tmpl.Name != need.Name {
				continue
			}
			if tmpl.StackLimit() > 0 {
				if int(it.Dura) > left {
					it.Dura -= uint32(left)
					left = 0
					break // ⚠️ 这里**不能** return：还有别的材料要扣
				}
				left -= int(it.Dura)
				d.BagItems = append(d.BagItems[:i], d.BagItems[i+1:]...)
				continue
			}
			d.BagItems = append(d.BagItems[:i], d.BagItems[i+1:]...)
			left--
		}
	}
}

// goodsContains 判断该商人的商品里有没有这件东西（原版遍历 `m_GoodsList`）。
func (s *Server) goodsContains(def *data.NPC, name string) bool {
	for _, it := range s.shopGoods(def) {
		if it.Name == name {
			return true
		}
	}
	return false
}

// npcDefOf 按 NPC 名字找它的定义（脚本与商品分类都挂在定义上）。
func (s *Server) npcDefOf(npc *entity.Monster) *data.NPC {
	for _, n := range s.npc.defs {
		if n.Name == npc.Name {
			return n
		}
	}
	return nil
}

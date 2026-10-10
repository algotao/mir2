package gamesvr

import (
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// 商店：点击 NPC 开商品列表、买入、卖出、修理。
//
// 协议（消息号取自 Common/Grobal2.pas；字段语义逐个对着**官方客户端**的发送处核过，
// 见下面各处理函数的注释 —— P1-7/P1-10）：
//
//	CM_CLICKNPC     = 1010  Recog = NPC 的 ActorId
//	CM_USERBUYITEM  = 1014  Recog = 商人 ActorId，Param/Tag = MakeLong(货架序号)，
//	                        body = 商品名（原版按**名字**匹配，见 handleBuyItem）
//	CM_USERSELLITEM = 1013  Recog = 商人 ActorId，Param/Tag = MakeLong(MakeIndex)，
//	                        body = 物品名
//	CM_USERREPAIRITEM = 1023 Recog = 商人 ActorId，Param/Tag = MakeLong(MakeIndex)，
//	                        body = 物品名
//	SM_SENDGOODSLIST = 645   Recog = NPC ActorId，Param = 件数，
//	                        body = **文本**：`名称/子菜单/价格/存量/` 重复
//	                        （ClMain.pas:6158-6190 按 '/' 切；我们原来发的是
//	                        二进制 Index+Price，官方客户端解析不了）
//
// ⚠️ 商品来源：1.76 的 market_def/<ID>-<地图>.txt 是**脚本**文件
// （[@main] + #if/#act），商品 [goods] 段在官方配置里基本是注释掉的。
// 因此这里按 NPC 名称关键字从物品表筛选商品——数据到位后改为读 [goods]。

// shopCategoryDef 是一类商店的匹配规则。
type shopCategoryDef struct {
	keywords []string
	stdModes []uint8
}

// shopCategory 是"名称关键字 → 售卖的 StdMode"映射。
var shopCategory = []shopCategoryDef{
	{[]string{"屠夫", "肉店", "肉"}, []uint8{40, 41}},    // 肉、食品
	{[]string{"武器", "卫家", "刀", "剑"}, []uint8{5, 6}}, // 武器
	{[]string{"布衣", "衣服", "衣"}, []uint8{10, 11}},    // 衣服
	{[]string{"首饰", "饰品", "项链", "戒指"}, []uint8{19, 20, 22, 23, 24, 26}},
	// 药品。⚠️ 药粉类（灰色药粉/黄色药粉/超级…）与护身符是 **StdMode 25**，
	// 不加进来的话"制药"的成品一件都不在商品表里 ⇒ `@makedrug` 永远失败
	//（我们的商品表是按名字关键字 + StdMode 粗分类的近似，原版是每 NPC 的商品文件）。
	{[]string{"药"}, []uint8{0, 25}},
	{[]string{"书", "技能"}, []uint8{4}},                     // 技能书
	{[]string{"杂货", "店", "商"}, []uint8{0, 3, 40, 41, 42}}, // 杂货
}

// maxGoods 是单个商店的商品上限（物品表有 686 条，全列太长）。
const maxGoods = 20

// shopGoods 返回某商人的商品列表。
func (s *Server) shopGoods(npc *data.NPC) []*data.StdItem {
	var modes []uint8
	for _, c := range shopCategory {
		for _, kw := range c.keywords {
			if strings.Contains(npc.Name, kw) {
				modes = append(modes, c.stdModes...)
				break
			}
		}
	}
	if len(modes) == 0 {
		// 认不出的商人按杂货铺处理
		modes = []uint8{0, 3, 40, 41}
	}
	want := make(map[uint8]bool, len(modes))
	for _, m := range modes {
		want[m] = true
	}

	out := make([]*data.StdItem, 0, maxGoods)
	for _, it := range s.data.tables.Items.All() {
		if len(out) >= maxGoods {
			break
		}
		if !want[it.StdMode] || it.Price == 0 {
			continue
		}
		// ⚠️ 排除金币：GeeM2 物品表里"金币1"(Idx 0) 的 StdMode 是 41，
		// 与肉/食品同类，不排掉会被当成 1 金币的商品摆上货架。
		if strings.Contains(it.Name, entity.GoldName) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// handleClickNPC 处理点击 NPC：有脚本进对话，没有则开商店。
func (s *Server) handleClickNPC(c net.Conn, p *Player, m wire.Packet) {
	id := uint32(m.Head.Recog)

	s.mu.RLock()
	npc, ok := s.world.monsters[id]
	s.mu.RUnlock()
	if !ok || !npc.IsNPC {
		return
	}

	// 距离校验：必须够得着，否则可以隔空开商店/对话
	if p.Obj.MapRef() != npc.MapRef() || p.Obj.Distance(npc.PosX(), npc.PosY()) > 8 {
		return
	}

	// 找对应的 NPC 定义（用于脚本与商品分类）
	def := s.npcDefOf(npc)
	if def == nil {
		def = &data.NPC{Name: npc.Name}
	}

	// 有脚本 → 对话（脚本里的 @buy/@sell 再打开商店）
	if sc := s.npcScript(def.ID, p.Obj.MapRef().Name); sc != nil {
		s.startDialog(c, p, sc, id)
		log.Printf("%s 与 %s 开始对话（脚本 %s）", p.Char.Name, npc.Name, sc.Name)
		s.sendGoods(c, p, npc, def)
		// 城堡战场地图上的 NPC 再追加一份城堡管理菜单（见 castleOfficialMenu）。
		s.castleOfficialMenu(c, p)
		return
	}

	s.openShop(c, p, id)
	s.castleOfficialMenu(c, p)
}

// openShop 打开某个 NPC 的商店。
func (s *Server) openShop(c net.Conn, p *Player, id uint32) {
	s.mu.RLock()
	npc, ok := s.world.monsters[id]
	s.mu.RUnlock()
	if !ok || !npc.IsNPC {
		return
	}
	// ⚠️ 商店也要建立**对话上下文**：`CM_MERCHANTDLGSELECT` 在 `p.dialog == nil` 时
	// 会被静默丢弃（见 npcdlg.go 顶部注释）。没有脚本的 NPC 原来只发商品列表、
	// 不建上下文 ⇒ 玩家接着点任何"标签型"选项（如城堡菜单里的 @hireguardnow1）都会
	// 石沉大海（2026-10-06 在 castledoor 用例里撞到）。
	if p.dialog == nil {
		p.dialog = &dialog{npcID: id}
	}
	var def *data.NPC
	for _, n := range s.npc.defs {
		if n.Name == npc.Name {
			def = n
			break
		}
	}
	if def == nil {
		def = &data.NPC{Name: npc.Name}
	}
	s.sendGoods(c, p, npc, def)
}

// shopStock 是商品列表里报给客户端的"货架存量"。
//
// 原版这栏是商品栈里的件数（`nStock := List14.Count`，ObjNpc.pas:1455 附近），
// 客户端点了商品之后会把它当 `Param/Tag` 回发（`SendBuyItem(g_nCurMerchant, pg.Stock, pg.Name)`，
// FState.pas:5244）。我们的商品是**按模板即时生成**的（没有货架实例、永不缺货），
// 所以这一栏只是给客户端显示/回发用，服务端不依赖它（买入按名字解析，见 handleBuyItem）。
const shopStock = 100

// sendGoods 下发商品列表。
//
// ⚠️ body 是**文本**（`名称/子菜单/价格/存量/` 重复），不是二进制数组 ——
// 官方客户端 `ClientGetSendGoodsList`（ClMain.pas:6158-6190）就是这么切的：
// 用 '/' 切四次拿 name/submenu/price/stock，`gprice`/`gstock` 缺一就 break。
// `submenu` 是"这件商品要不要弹二级菜单"：可堆叠类（StdMode <= 4 / 31 / 42）为 0，
// 其余为 1（ObjNpc.pas:1449-1452）。
func (s *Server) sendGoods(c net.Conn, p *Player, npc *entity.Monster, def *data.NPC) {
	goods := s.shopGoods(def)
	// **新协议**：结构化商品列表（legacy 那条是 '/' 拼的文本，两种都要发 ⇒ 与
	// `npcSay` 同一条纪律：proto 玩家的 legacy 下行是被丢弃的，只发 legacy 等于没发）。
	if sink := p.protoOut; sink != nil {
		items := make([]*protocol.ShopItem, 0, len(goods))
		for _, it := range goods {
			items = append(items, &protocol.ShopItem{
				Name:    it.Name,
				Price:   uint64(it.Price),
				Stock:   shopStock,
				Submenu: shopSubmenu(it) != 0, // legacy 是 1/0，新协议是 bool
				DuraMax: uint32(it.DuraMax),   // 第三栏"持久"（原版买窗的列）
			})
		}
		sink.enqueue(&protocol.Envelope{Body: &protocol.Envelope_ShopList{
			ShopList: &protocol.ShopList{NpcId: uint64(npc.ID), Items: items}}})
		log.Printf("%s 打开商店 %s（proto，%d 件商品）", p.Char.Name, npc.Name, len(goods))
		return
	}
	var b strings.Builder
	for _, it := range goods {
		fmt.Fprintf(&b, "%s/%d/%d/%d/", it.Name, shopSubmenu(it), it.Price, shopStock)
	}
	s.send(c, proto.SM_SENDGOODSLIST, int32(npc.ID), uint16(len(goods)), 0, 0, b.String())
	log.Printf("%s 打开商店 %s（legacy，%d 件商品）", p.Char.Name, npc.Name, len(goods))
}

// shopSubmenu 是"这件商品要不要弹二级菜单"（原版 `submenu`，`ObjNpc.pas:1449-1452`）：
// 可堆叠类（有 StackLimit / StdMode <= 4 / 31 / 42）为 0，其余为 1。
//
// ⚠️ legacy 是 int（1/0），新协议是 bool —— 两条路共用这一个判据，免得哪天只改一边。
func shopSubmenu(it *data.StdItem) int {
	if it == nil {
		return 0
	}
	if it.StackLimit() > 0 || it.StdMode <= 4 || it.StdMode == 31 || it.StdMode == 42 {
		return 0
	}
	return 1
}

// shopGoodsByName 在商人的商品表里按**名字**找商品（原版就是这么匹配的：
// `if sUserItemName = sItemName then`，ObjNpc.pas:1957）。
func (s *Server) shopGoodsByName(def *data.NPC, name string) *data.StdItem {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, it := range s.shopGoods(def) {
		if it.Name == name {
			return it
		}
	}
	return nil
}

// sendBuyFail 回买入失败（原版 `RM_BUYITEM_FAIL` 的 nParam2 = 失败码，
// 客户端按码给提示：1 = 没这件商品、2 = 带不动了、3 = 出价太低/钱不够）。
func (s *Server) sendBuyFail(c net.Conn, code int) {
	s.send(c, proto.SM_BUYITEM_FAIL, int32(code), 0, 0, 0, "")
}

// handleBuyItem 处理买入（CM_USERBUYITEM = 1014）。
//
// 逐字段对照官方两端：
//
//	客户端 FState.pas:5244  `SendBuyItem(g_nCurMerchant, pg.Stock, pg.Name)`
//	                       ⇒ Recog = 商人 ActorId、Param/Tag = MakeLong(货架存量)、
//	                         body = 商品名
//	服务端 ObjBase.pas:16157 `ClientUserBuyItem(nIdent, nParam1, MakeInt(nParam2, nParam3))`
//	                       ⇒ 商人 = FindMerchant(nParam1)，商品按 **sMsg 名字**匹配
//
// ⚠️ 我们**不用** Param/Tag 当"买几件"：原版这一栏是货架存量，一次点击只买 1 件
// （`ClientBuyItem` 里 `AddItemToBag(UserItem)` 只加一件）。把它当数量的话，
// 官方客户端在存量 100 的商品上点一下会买走 100 件。
func (s *Server) handleBuyItem(c net.Conn, p *Player, m wire.Packet) {
	npc, def, ok := s.dialogMerchant(p, uint32(m.Head.Recog))
	if !ok {
		// 原版这里静默 Exit（ObjBase.pas:16163）。留一条日志便于排查"客户端字段错位"。
		log.Printf("买入被拒：玩家 %s 的报文商人 ActorId=%d 不是当前在谈的商人/不在 15 格内",
			p.Char.Name, m.Head.Recog)
		return
	}
	if !s.merchantScriptAllows(p, def, "@buy") {
		log.Printf("%s 想从 %s 买东西，但该脚本头没声明 @buy", p.Char.Name, npc.Name)
		s.sendBuyFail(c, 1)
		return
	}
	it := s.shopGoodsByName(def, m.Body)
	if it == nil || it.Price == 0 {
		s.sysMsg(c, "没有这件商品")
		s.sendBuyFail(c, 1)
		return
	}
	// 一次买 1 件（见函数头）。价格、扣款、发货的原子性与注释见下。
	price := int64(it.Price)
	if p.gold() < price {
		s.sysMsg(c, fmt.Sprintf("金币不足（需要 %d）", price))
		s.sendBuyFail(c, 3)
		return
	}
	// ⚠️ 先把钱**原子扣掉**再发货。原来（也照抄原版）是先循环 `addToBag` 发货、
	// 最后才 `Gold -= spent` —— 在我们这个多 goroutine 的服务器上，那次扣款可能
	// 因为并发的收支而算错（丢更新）⇒ 等于白送东西。
	if entity.MaxBagSize-countBag(p) <= 0 {
		s.sysMsg(c, "背包已满")
		s.sendBuyFail(c, 2)
		return
	}
	if !p.spendGold(price) {
		s.sysMsg(c, fmt.Sprintf("金币不足（需要 %d）", price))
		s.sendBuyFail(c, 3)
		return
	}

	ui := &pb.UserItem{
		MakeIndex: int32(s.itemSeq.Add(1)),
		Index:     uint32(it.Index),
		Dura:      initialDura(it),
		DuraMax:   it.DuraMax,
	}
	if s.addToBag(p, ui) < 0 {
		// 钱已经付了、东西没发出去 ⇒ 退钱（正常情况下到不了这里：上面刚数过空位）
		p.addGold(price)
		s.sysMsg(c, "背包已满")
		s.sendBuyFail(c, 2)
		return
	}

	// 城堡税从商家的收入里抽（玩家照付原价，ObjNpc.pas:2168）。
	s.castleTax(p, price)
	// 回执：Recog = 金币（客户端 `g_MySelf.m_nGold := msg.Recog`）、
	// Param/Tag = MakeIndex 低/高 16 位（客户端拿它把商品从明细列表里划掉）。
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	s.send(c, proto.SM_BUYITEM_SUCCESS, int32(p.gold()),
		uint16(uint32(ui.MakeIndex)&0xFFFF), uint16(uint32(ui.MakeIndex)>>16), 0, "")
	s.sendBagItems(c, p)
	s.sysMsg(c, fmt.Sprintf("买入 %s x1，花费 %d 金币", it.Name, price))
	log.Printf("%s 从 %s 买入 %s x1（-%d 金币，余 %d）",
		p.Char.Name, npc.Name, it.Name, price, p.gold())
}

// superRepairPriceRate 是特修的价格倍数（官方 `nSuperRepairPriceRate=3`）。
const superRepairPriceRate = 3

// repairDecDuraDivisor 是**普修**磨损耐久上限的分母（官方 `nRepairItemDecDura=30`）。
//
// 原版：`Dec(UserItem.DuraMax, (UserItem.DuraMax - UserItem.Dura) div 30)`
// —— 普修修满是修满，但上限会永久掉一截；特修不动上限。这条以前我们没做。
const repairDecDuraDivisor = 30

// handleRepairItem 处理修理（CM_USERREPAIRITEM=1023）—— 两档，逐句对照
// `TMerchant.ClientRepairItem`（ObjNpc.pas:2422-2493）：
//
//	档位不在消息里，而在**玩家当前所处的脚本标签**（`m_sScriptLable`）：
//	  `@s_repair` ⇒ 特修：需要脚本头声明 `@s_repair`，价格 ×3，修满且**不磨损上限**；
//	  其余标签    ⇒ 普修：需要脚本头声明 `@repair`，价格不变，修满但 `DuraMax -= 缺损/30`。
//	价格 = `Round(nPrice div 3 / DuraMax * (DuraMax - Dura))`（nPrice = 物品表原价）。
//	**矿石不可修**（`StdItem.StdMode <> 43`），且**先扣钱再修**（`DecGold` 失败即回执 FAIL）。
//
// 报文（P1-7）：**Recog = 商人 ActorId**、Param/Tag = MakeLong(MakeIndex)、body = 物品名：
//
//	客户端 `SendRepairItem(g_nCurMerchant, g_SellDlgItem.MakeIndex, name)`（ClMain.pas:3711）
//	服务端 `ClientRepairItem(nParam1, MakeInt(nParam2, nParam3))`（ObjBase.pas:24653）
//	       ⇒ 按 MakeIndex 找物品，再找 `FindMerchant(nParam1)`、判同地图 + 15 格内
//
// ⚠️ 原版只查背包（`m_ItemList`，修理窗口拖的就是背包里的东西）。我们**额外**
// 接受"已穿戴装备"：我们的修理界面此前就是按装备槽做的（修手里的刀是主用例），
// 而 MakeIndex 全局唯一 ⇒ 先背包后装备地找不会歧义。
func (s *Server) handleRepairItem(c net.Conn, p *Player, m wire.Packet) {
	npc, _, ok := s.dialogMerchant(p, uint32(m.Head.Recog))
	if !ok {
		log.Printf("修理被拒：玩家 %s 的报文商人 ActorId=%d 不是当前在谈的商人/不在 15 格内",
			p.Char.Name, m.Head.Recog)
		return
	}
	makeIdx := int32(proto.MakeLong(m.Head.Param, m.Head.Tag))
	slot, ui, inBag := s.itemByMakeIndex(p, makeIdx, m.Body, true)
	if ui == nil || ui.Index == 0 || ui.DuraMax == 0 {
		s.repairFail(c, p, "该物品不在身上（或没有耐久）")
		return
	}
	it := s.data.tables.Items.Get(int(ui.Index) - 1)
	if it == nil {
		return
	}
	// 档位判定（原版比的是玩家当前标签，**不区分大小写**）
	super := p.dialog != nil && p.dialog.label == "@s_repair"
	if super {
		if !s.dialogAllows(p, "@s_repair") {
			s.repairFail(c, p, "这个商人不会特修")
			return
		}
	} else if !s.dialogAllows(p, "@repair") {
		s.repairFail(c, p, "这个商人不会修理")
		return
	}
	// ⚠️ 堆叠物品的 Dura 是数量，不是耐久（仓库的老约定）；矿石（StdMode 43）也不可修。
	if it.StackLimit() > 0 || it.StdMode == 43 {
		s.repairFail(c, p, "这个不能修理")
		return
	}
	if ui.Dura >= ui.DuraMax {
		s.repairFail(c, p, "耐久已满，无需修理")
		return
	}

	// 价格：原版 `nPrice div 3 / DuraMax * 缺损`，特修再 ×3（先乘后除，与整数顺序一致）
	price := int64(it.Price)
	if super {
		price *= superRepairPriceRate
	}
	missing := ui.DuraMax - ui.Dura
	cost := price / 3 * int64(missing) / int64(ui.DuraMax)
	if cost < 1 {
		cost = 1
	}
	// **先扣钱**（原版 `if PlayObject.DecGold(nRepairPrice) then`）。
	// ⚠️ "够不够"的判定与扣款必须是**同一次原子操作**：分开做时并发的收支会在
	// 两者之间插进来 ⇒ 白修一次装备。
	if !p.spendGold(cost) {
		s.repairFail(c, p, fmt.Sprintf("金币不足（修理需要 %d）", cost))
		return
	}
	s.castleTax(p, cost) // 城堡税（ObjNpc.pas:2465 的 IncRateGold）
	if super {
		ui.Dura = ui.DuraMax
	} else {
		// 普修永久磨损上限：`DuraMax -= 缺损/30`，再修满
		ui.DuraMax -= uint32(missing) / repairDecDuraDivisor
		if ui.DuraMax < 1 {
			ui.DuraMax = 1
		}
		ui.Dura = ui.DuraMax
	}

	// 回执：客户端读 `SM_USERREPAIRITEM_OK` 的 Recog=金币、Param=Dura、Tag=DuraMax
	//（ClMain.pas:5185-5194）。
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	s.send(c, proto.SM_USERREPAIRITEM_OK, int32(p.gold()), uint16(ui.Dura), uint16(ui.DuraMax), 0, "")
	// 物品可能在背包（原版路径）或在装备槽（我们多的一档）⇒ 两条列表都刷。
	s.sendUseItems(c, p)
	s.sendBagItems(c, p)
	tier := "普修"
	if super {
		tier = "特修"
	}
	where := fmt.Sprintf("背包%d", slot)
	if !inBag {
		where = fmt.Sprintf("装备槽%d", slot)
	}
	s.sysMsg(c, fmt.Sprintf("%s完成（%s），花费 %d 金币，耐久 %d/%d",
		tier, it.Name, cost, ui.Dura, ui.DuraMax))
	log.Printf("%s 在 %s 处 %s%s %s（-%d 金币，余 %d，耐久 %d/%d）",
		p.Char.Name, npc.Name, tier, where, it.Name, cost, p.gold(), ui.Dura, ui.DuraMax)
}

// repairFail 回执失败（原版 `RM_USERREPAIRITEM_FAIL` ⇒ `SM_USERREPAIRITEM_FAIL`）。
func (s *Server) repairFail(c net.Conn, p *Player, why string) {
	s.send(c, proto.SM_USERREPAIRITEM_FAIL, 0, 0, 0, 0, "")
	s.sysMsg(c, why)
}

// handleSellItem 处理卖出（CM_USERSELLITEM = 1013）。
//
// 逐字段对照官方两端：
//
//	客户端 FState.pas:5452 `SendSellItem(g_nCurMerchant, g_SellDlgItem.MakeIndex, name)`
//	                       ⇒ Recog = 商人 ActorId、Param/Tag = MakeLong(MakeIndex)、
//	                         body = 物品名
//	服务端 ObjBase.pas:16111 `ClientUserSellItem(nParam1, MakeInt(nParam2, nParam3))`
//	                       ⇒ 按 **MakeIndex** 在背包里找（还要求名字相同），
//	                         再查商人：存在、允许收（m_boSell）、同地图、15 格内
//
// ⚠️ 卖价取原价的**一半**（原版商人赚差价），且持久度太低的不收——
// 否则可以靠"买来再卖"刷金币（虽然半价已经不划算）。
func (s *Server) handleSellItem(c net.Conn, p *Player, m wire.Packet) {
	npc, def, ok := s.dialogMerchant(p, uint32(m.Head.Recog))
	if !ok {
		log.Printf("卖出被拒：玩家 %s 的报文商人 ActorId=%d 不是当前在谈的商人/不在 15 格内",
			p.Char.Name, m.Head.Recog)
		// 原版这条路径静默丢弃；我们回 FAIL，否则客户端把物品卡在"待卖"状态。
		s.send(c, proto.SM_USERSELLITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	if !s.merchantScriptAllows(p, def, "@sell") {
		log.Printf("%s 想卖给 %s 东西，但该脚本头没声明 @sell", p.Char.Name, npc.Name)
		s.send(c, proto.SM_USERSELLITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	makeIdx := int32(proto.MakeLong(m.Head.Param, m.Head.Tag))
	slot, ui, _ := s.itemByMakeIndex(p, makeIdx, m.Body, false)
	if ui == nil || ui.Index == 0 {
		s.sysMsg(c, "背包里没有这件东西")
		s.send(c, proto.SM_USERSELLITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	it := s.data.tables.Items.Get(int(ui.Index) - 1)
	if it == nil || it.Price == 0 {
		s.sysMsg(c, "这件东西卖不掉")
		s.send(c, proto.SM_USERSELLITEM_FAIL, 0, 0, 0, 0, "")
		return
	}

	price := int64(it.Price) / 2
	if price <= 0 {
		price = 1
	}
	p.addGold(price)
	s.castleTax(p, price) // 城堡税（ObjNpc.pas:1974）
	s.takeBagItem(p, slot)

	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	// 回执：Recog = 金币（客户端 `g_MySelf.m_nGold := msg.Recog`，ClMain.pas:5162）。
	s.send(c, proto.SM_USERSELLITEM_OK, int32(p.gold()), 0, 0, 0, "")
	s.sendBagItems(c, p)
	s.sysMsg(c, fmt.Sprintf("卖出 %s，获得 %d 金币", it.Name, price))
	log.Printf("%s 卖给 %s %s（+%d 金币，共 %d）",
		p.Char.Name, npc.Name, it.Name, price, p.gold())
}

// itemByMakeIndex 按 MakeIndex（+名字）在身上找一件物品：**先背包，再装备槽**
// （allowEquipped 为 false 时只找背包，用于卖出）。
//
// 返回槽位号、物品，以及它在不在背包里（日志与刷新用）。找不到返回 (-1, nil, false)。
//
// 原版身份判据是"MakeIndex 相同 **且** 名字相同"（`CompareText(sUserItemName, sMsg) = 0`，
// ObjBase.pas:16117 / :24663）—— 名字是客户端给的，只是二次确认，拿不到就跳过。
func (s *Server) itemByMakeIndex(p *Player, makeIdx int32, name string, allowEquipped bool) (int, *pb.UserItem, bool) {
	if p == nil || p.Char == nil || p.Char.Data == nil || makeIdx == 0 {
		return -1, nil, false
	}
	d := p.Char.Data
	match := func(u *pb.UserItem) bool {
		if u == nil || u.Index == 0 || u.MakeIndex != makeIdx {
			return false
		}
		return s.itemNameMatches(u, name)
	}
	// ⚠️ 背包要**持 stateMu 读**（成交/退回会改它）
	bagSlot, bagItem := -1, (*pb.UserItem)(nil)
	p.withBag(func(dd *pb.CharacterData) {
		for i, u := range dd.BagItems {
			if match(u) {
				bagSlot, bagItem = i, u
				return
			}
		}
	})
	if bagSlot >= 0 {
		return bagSlot, bagItem, true
	}
	if allowEquipped {
		for i, u := range d.HumItems {
			if match(u) {
				return i, u, false
			}
		}
	}
	return -1, nil, false
}

// ---------- 新协议的买 / 卖（原版是 `CM_USERBUYITEM` / `CM_USERSELLITEM`）----------
//
// ⚠️ 为什么不能只留 legacy 那两条：proto 玩家的 legacy 下行会被 `protoDown` 丢掉
//（见 netproto.go 那条注释）⇒ `SM_BUYITEM_SUCCESS` / `SM_USERSELLITEM_OK` 那几个号
// **到不了**新协议客户端，点了买就是"没反应"。
// 成交的判定与 legacy **共用同一份**（`shopGoodsByName` / `spendGold` / `addToBag`），
// 分路的只有"回执"这一层 —— 与 `sendGoods` 同一条纪律（D-65）。

// shopResult 回一条买卖结果（新协议）。
//
// legacy 那半边没有对应消息：他们靠 `SM_BUYITEM_SUCCESS` + 随后的金币/背包刷新 +
// `sysMsg`。proto 玩家收不到 `sysMsg`（`s.send` 被丢）⇒ **`ShopResult.message`
// 就是它唯一看得见的回音**，失败原因必须写人能读的话。
func (s *Server) shopResult(p *Player, npcID uint64, ok bool, msg string) {
	if p == nil || p.protoOut == nil {
		return
	}
	p.protoOut.enqueue(&protocol.Envelope{Body: &protocol.Envelope_ShopResult{
		ShopResult: &protocol.ShopResult{NpcId: npcID, Ok: ok, Message: msg}}})
}

// shopPushState 买卖之后把**金币与背包**刷给玩家（两条协议各取所需）。
//
// ⚠️ 少发一边就等于"钱变了、界面没变"：proto 的金币只在 `Ability` 里
// （`protoSink.ability` 带 gold），legacy 走 `SM_GOLDCHANGED`。
func (s *Server) shopPushState(c net.Conn, p *Player) {
	if p == nil {
		return
	}
	s.sendBagItems(c, p)
	if p.protoOut != nil {
		if p.Char != nil && p.Char.Data != nil {
			p.protoOut.ability(p.Char.Data.Abil, p.Char.Data.Gold)
		}
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
}

// whoStr 取个能打进日志的名字（`p.Char` 可能在异常路径上是 nil）。
func whoStr(p *Player) string {
	if p == nil || p.Char == nil {
		return "(未知玩家)"
	}
	return p.Char.Name
}

// onShopBuy 处理新协议的买入。
//
// 与原版的两个不同（都是**我们这边**的取舍，写在 `ShopBuy` 的注释里）：
//   - 一次可以买 `count` 件（原版一次点击只买 1 件：存量那栏被当成数量会买走 100 件）；
//   - 可堆叠的一次最多买**一堆**（`StackLimit`），不可堆叠的受**背包空位**限制。
//
// ⚠️ 扣款与发货的顺序与 legacy 那条一样：**先原子扣款**，发货失败再退钱
// （分开做会被并发的收支插进来 ⇒ 白送东西，见 `handleBuyItem`）。
func (s *Server) onShopBuy(c net.Conn, p *Player, m *protocol.ShopBuy) {
	npc, def, ok := s.dialogMerchant(p, uint32(m.GetNpcId()))
	if !ok {
		log.Printf("买入被拒：玩家 %s 的 npc_id=%d 不是当前在谈的商人/不在 %d 格内",
			whoStr(p), m.GetNpcId(), storageNpcRange)
		s.shopResult(p, m.GetNpcId(), false, "不在商人旁边（或这段对话已经关了）")
		return
	}
	if !s.merchantScriptAllows(p, def, "@buy") {
		log.Printf("%s 想从 %s 买东西，但该脚本头没声明 @buy", whoStr(p), npc.Name)
		s.shopResult(p, m.GetNpcId(), false, "这个商人不做买卖")
		return
	}
	it := s.shopGoodsByName(def, m.GetName())
	if it == nil || it.Price == 0 {
		s.shopResult(p, m.GetNpcId(), false, "没有这件商品")
		return
	}
	// 这一单能买几件
	want := int(m.GetCount())
	if want <= 0 {
		want = 1
	}
	n := want
	if lim := int(it.StackLimit()); lim > 0 {
		if n > lim {
			n = lim // 可堆叠：一次最多一堆
		}
	} else if free := entity.MaxBagSize - countBag(p); n > free {
		n = free // 不可堆叠：一件一个格子
	}
	if n <= 0 {
		s.shopResult(p, m.GetNpcId(), false, "背包已满")
		return
	}
	price := int64(it.Price)
	total := price * int64(n)
	if !p.spendGold(total) {
		s.shopResult(p, m.GetNpcId(), false, fmt.Sprintf("金币不足（需要 %d）", total))
		return
	}
	gave := 0
	if lim := it.StackLimit(); lim > 0 {
		// 可堆叠：一个实例，`Dura` 记数量（`addToBagLocked` 的约定）
		ui := &pb.UserItem{
			MakeIndex: int32(s.itemSeq.Add(1)),
			Index:     uint32(it.Index),
			Dura:      uint32(n),
			DuraMax:   it.DuraMax,
		}
		if s.addToBag(p, ui) >= 0 {
			gave = n
		}
	} else {
		for i := 0; i < n; i++ {
			ui := &pb.UserItem{
				MakeIndex: int32(s.itemSeq.Add(1)),
				Index:     uint32(it.Index),
				Dura:      initialDura(it),
				DuraMax:   it.DuraMax,
			}
			if s.addToBag(p, ui) < 0 {
				break
			}
			gave++
		}
	}
	if gave == 0 {
		p.addGold(total) // 钱已付、货没发 ⇒ 退钱
		s.shopResult(p, m.GetNpcId(), false, "背包已满")
		return
	}
	if gave < n {
		p.addGold(price * int64(n-gave)) // 部分成交 ⇒ 退差价
	}
	s.castleTax(p, price*int64(gave))
	s.shopPushState(c, p)
	s.shopResult(p, m.GetNpcId(), true,
		fmt.Sprintf("买入 %s x%d，花费 %d 金币", it.Name, gave, price*int64(gave)))
	log.Printf("%s 从 %s 买入 %s x%d（-%d 金币，余 %d）[新协议]",
		whoStr(p), npc.Name, it.Name, gave, price*int64(gave), p.gold())
}

// onShopSell 处理新协议的卖出。
//
// 与原版一样按 **MakeIndex** 认物（名字只是二次确认，见 `itemByMakeIndex`），
// 卖价取原价的**一半**（`handleSellItem`：商人赚差价，也顺手堵住"买来再卖"刷金币）。
//
// 比原版多的一档：`count` —— 可堆叠的可以只卖堆里的一部分（原版是整堆一起卖）。
func (s *Server) onShopSell(c net.Conn, p *Player, m *protocol.ShopSell) {
	npc, def, ok := s.dialogMerchant(p, uint32(m.GetNpcId()))
	if !ok {
		log.Printf("卖出被拒：玩家 %s 的 npc_id=%d 不是当前在谈的商人/不在 %d 格内",
			whoStr(p), m.GetNpcId(), storageNpcRange)
		s.shopResult(p, m.GetNpcId(), false, "不在商人旁边（或这段对话已经关了）")
		return
	}
	if !s.merchantScriptAllows(p, def, "@sell") {
		log.Printf("%s 想卖给 %s 东西，但该脚本头没声明 @sell", whoStr(p), npc.Name)
		s.shopResult(p, m.GetNpcId(), false, "这个商人不收货")
		return
	}
	slot, ui, inBag := s.itemByMakeIndex(p, m.GetMakeIndex(), "", false)
	if ui == nil || ui.Index == 0 || !inBag {
		s.shopResult(p, m.GetNpcId(), false, "背包里没有这件东西")
		return
	}
	it := s.data.tables.Items.Get(int(ui.Index) - 1)
	if it == nil || it.Price == 0 {
		s.shopResult(p, m.GetNpcId(), false, "这件东西卖不掉")
		return
	}
	// 卖几件：可堆叠的按堆里的数量（最多 `count`），不可堆叠的就是这一件
	n := 1
	if lim := it.StackLimit(); lim > 0 {
		n = int(ui.Dura)
		if n <= 0 {
			n = 1
		}
		if c := int(m.GetCount()); c > 0 && c < n {
			n = c
		}
	}
	price := int64(it.Price) / 2
	if price <= 0 {
		price = 1
	}
	gain := price * int64(n)
	p.addGold(gain)
	s.castleTax(p, gain)
	// 整堆卖光 ⇒ 清掉这个槽；只卖一部分 ⇒ 堆里减掉（`Dura` 记数量）
	if n >= int(ui.Dura) || it.StackLimit() == 0 {
		s.takeBagItem(p, slot)
	} else {
		ui.Dura -= uint32(n)
	}
	s.shopPushState(c, p)
	s.shopResult(p, m.GetNpcId(), true, fmt.Sprintf("卖出 %s x%d，获得 %d 金币", it.Name, n, gain))
	log.Printf("%s 卖给 %s %s x%d（+%d 金币，共 %d）[新协议]",
		whoStr(p), npc.Name, it.Name, n, gain, p.gold())
}

// itemNameMatches 用玩家发来的名字核对这件物品（名字为空 ⇒ 不核对）。
//
// 原版用 `CompareText`（大小写不敏感）；我们的物品名来自物品表，客户端原样回发。
func (s *Server) itemNameMatches(u *pb.UserItem, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || u == nil || u.Index == 0 {
		return true
	}
	tmpl := s.data.tables.Items.Get(int(u.Index) - 1)
	if tmpl == nil {
		return false
	}
	return strings.EqualFold(tmpl.Name, name)
}

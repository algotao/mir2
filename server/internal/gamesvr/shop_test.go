package gamesvr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// shopTestServer 造"脚本头可指定功能的武器店老板 + 站在它旁边、正与它对话的玩家"。
//
// `cmdLine` 是脚本头声明的功能（`(@trading @buy @sell …)`），原版的
// `m_boBuy`/`m_boSell` 就是按它置位的。
func shopTestServer(t *testing.T, cmdLine string, items ...*data.StdItem) (*Server, *Player, *entity.Monster, *recordingConn) {
	t.Helper()
	s, p := butchTestServer(t, items...)

	dir := t.TempDir()
	body := "(" + cmdLine + ")\r\n\r\n[@main]\r\n你好\\ \\\r\n"
	// ⚠️ 商人代码用 `2Gwe`（2 = 武器）：货架分类看**首位数字**，
	// 用 `9shop` 这种编码会被判成"特殊 NPC ⇒ 不卖东西"（2026-10-17 改）。
	if err := os.WriteFile(filepath.Join(dir, "2Gwe-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir
	s.npc.defs = []*data.NPC{{ID: "2Gwe", Name: "武器店老板", MapID: "0"}}

	npc := newTestMonster(66, "武器店老板", 10)
	npc.IsNPC = true
	npc.ID = 66
	npc.SetPlace(p.Obj.MapRef(), 6, 5, npc.Facing())
	s.mu.Lock()
	s.world.monsters[npc.ID] = npc
	s.mu.Unlock()

	rec := &recordingConn{}
	p.conn = rec
	p.dialog = &dialog{npcID: npc.ID, scriptName: "2Gwe", label: "@main"}
	p.Char.Data.Gold = 10000
	return s, p, npc, rec
}

// shopItem 是一件可买可卖的武器（StdMode 5 ⇒ 归"武器店"的商品分类）。
func shopItem() *data.StdItem {
	it := wuItem(1, "测试剑", 5, data.MinMax{})
	it.Price = 3000
	return it
}

// buyPacket 按**官方字段**构造买入报文：
// Recog = 商人 ActorId、Param/Tag = MakeLong(货架序号)、body = 商品名
// （客户端 FState.pas:5244 → `SendBuyItem(g_nCurMerchant, pg.Stock, pg.Name)`）。
func buyPacket(npcID uint32, stock int, name string) wire.Packet {
	return wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERBUYITEM, int32(npcID), uint16(stock), 0, 0),
		Body: name,
	}
}

// sellPacket / repairPacket 同理：Recog = 商人、Param/Tag = MakeLong(MakeIndex)、body = 名字。
func sellPacket(npcID uint32, makeIdx int32, name string) wire.Packet {
	return wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERSELLITEM, int32(npcID),
			uint16(uint32(makeIdx)&0xFFFF), uint16(uint32(makeIdx)>>16), 0),
		Body: name,
	}
}

// TestGoodsListBodyIsOfficialText 守住 P1-7 的下行方向：商品列表 body 必须是
// `名称/子菜单/价格/存量/` 的**文本**（官方客户端 `ClientGetSendGoodsList`
// 按 '/' 切四次，ClMain.pas:6158-6190），不是二进制 Index+Price。
func TestGoodsListBodyIsOfficialText(t *testing.T) {
	s, p, npc, rec := shopTestServer(t, "@trading @buy @sell", shopItem())

	s.openShop(rec, p, npc.ID)

	var body string
	count := -1
	for _, pk := range rec.packets(t) {
		if pk.Head.Ident == proto.SM_SENDGOODSLIST {
			body, count = pk.Body, int(pk.Head.Param)
			if int32(pk.Head.Recog) != int32(npc.ID) {
				t.Errorf("Recog = %d，期望商人 ActorId %d", pk.Head.Recog, npc.ID)
			}
		}
	}
	if count < 0 {
		t.Fatal("没收到 SM_SENDGOODSLIST")
	}
	if count != 1 {
		t.Fatalf("商品件数 = %d，期望 1", count)
	}
	// 官方格式：名称/子菜单/价格/存量/ —— 收尾也要有 '/'，客户端按它切完最后一段
	if body != "测试剑/1/3000/100/" {
		t.Fatalf("商品列表 body = %q，期望 %q（名称/子菜单/价格/存量/）",
			body, "测试剑/1/3000/100/")
	}
}

// TestBuyKeepsOfficialFieldSemantics 买入：Recog 是**商人 ActorId**、
// Param/Tag 是货架存量（**不是件数**）、body 是商品名。
func TestBuyKeepsOfficialFieldSemantics(t *testing.T) {
	s, p, npc, rec := shopTestServer(t, "@trading @buy @sell", shopItem())

	// ① 存量那一栏给 100（官方客户端就是这么回发的）：必须只买 1 件
	s.handleBuyItem(rec, p, buyPacket(npc.ID, 100, "测试剑"))
	if got := countBagItemNamed(s, p, "测试剑"); got != 1 {
		t.Fatalf("买入件数 = %d，期望 1（Param/Tag 是货架存量，不是数量）", got)
	}
	if p.Char.Data.Gold != 7000 {
		t.Fatalf("金币 = %d，期望 7000（10000 - 3000）", p.Char.Data.Gold)
	}
	// 回执：SM_BUYITEM_SUCCESS 的 Recog = 金币（客户端 `m_nGold := msg.Recog`）
	ok := false
	var makeIdx uint32
	for _, pk := range rec.packets(t) {
		if pk.Head.Ident == proto.SM_BUYITEM_SUCCESS {
			ok = true
			makeIdx = uint32(proto.MakeLong(pk.Head.Param, pk.Head.Tag))
			if pk.Head.Recog != 7000 {
				t.Errorf("SM_BUYITEM_SUCCESS.Recog = %d，期望金币 7000", pk.Head.Recog)
			}
		}
	}
	if !ok {
		t.Error("没收到 SM_BUYITEM_SUCCESS")
	}
	// 而 Param/Tag 带回来的是**新物品的 MakeIndex**（客户端拿它划掉明细）
	bought := -1
	for i, u := range p.Char.Data.BagItems {
		if u != nil && u.MakeIndex == int32(makeIdx) {
			bought = i
		}
	}
	if bought < 0 {
		t.Errorf("SM_BUYITEM_SUCCESS 里的 MakeIndex=%d 与背包里那件对不上", makeIdx)
	}
}

// TestSellByMakeIndex 卖出：Recog 是商人、Param/Tag 是 MakeIndex、body 是名字；
// 回执 SM_USERSELLITEM_OK 的 Recog = 金币。
func TestSellByMakeIndex(t *testing.T) {
	s, p, npc, rec := shopTestServer(t, "@trading @buy @sell", shopItem())
	d := p.Char.Data
	d.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 77, Dura: 10, DuraMax: 10}
	rec.reset()

	s.handleSellItem(rec, p, sellPacket(npc.ID, 77, "测试剑"))

	if p.Char.Data.Gold != 11500 {
		t.Fatalf("金币 = %d，期望 11500（10000 + 3000/2）", p.Char.Data.Gold)
	}
	if countBagItemNamed(s, p, "测试剑") != 0 {
		t.Error("卖掉的物品还在背包里")
	}
	ok := false
	for _, pk := range rec.packets(t) {
		if pk.Head.Ident == proto.SM_USERSELLITEM_OK {
			ok = true
			if pk.Head.Recog != 11500 {
				t.Errorf("SM_USERSELLITEM_OK.Recog = %d，期望金币 11500", pk.Head.Recog)
			}
		}
	}
	if !ok {
		t.Error("没收到 SM_USERSELLITEM_OK")
	}

	// ② MakeIndex 对不上 ⇒ 不卖（伪造的报文不能卖掉别的东西）
	d.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 88, Dura: 10, DuraMax: 10}
	before := p.Char.Data.Gold
	s.handleSellItem(nil, p, sellPacket(npc.ID, 77, "测试剑"))
	if p.Char.Data.Gold != before || countBagItemNamed(s, p, "测试剑") != 1 {
		t.Error("MakeIndex 对不上时不该卖出")
	}
}

// TestShopRejectsUnboundMerchant 守住 P1-10：经济操作必须绑定"当前正在对话的商人"，
// 且同地图、15 格内。任何一条不满足都不能成交（服务端不能信客户端 UI）。
func TestShopRejectsUnboundMerchant(t *testing.T) {
	s, p, npc, rec := shopTestServer(t, "@trading @buy @sell", shopItem())
	d := p.Char.Data

	cases := []struct {
		name   string
		mutate func()
	}{
		{"没开过对话", func() { p.dialog = nil }},
		{"报文里的商人是别人(ActorId 伪造)", func() { p.dialog = &dialog{npcID: npc.ID} }},
		{"商人不在视野/走远了", func() {
			p.Obj.SetPlace(p.Obj.MapRef(), 40, 40, 0) // 与 (6,5) 相距 35 格
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p.dialog = &dialog{npcID: npc.ID}
			p.Obj.SetPlace(p.Obj.MapRef(), 5, 5, 0)
			d.Gold = 10000
			d.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 77, Dura: 10, DuraMax: 10}
			tc.mutate()

			forgedNPC := uint32(npc.ID)
			if strings.Contains(tc.name, "伪造") {
				forgedNPC = npc.ID + 1000
			}
			s.handleBuyItem(rec, p, buyPacket(forgedNPC, 100, "测试剑"))
			s.handleSellItem(rec, p, sellPacket(forgedNPC, 77, "测试剑"))

			if d.Gold != 10000 {
				t.Errorf("金币 = %d，期望不动（10000）", d.Gold)
			}
			if countBagItemNamed(s, p, "测试剑") != 1 {
				t.Error("背包里的东西被动了")
			}
		})
	}

	// ③ 打对商人，但脚本头没声明 @buy/@sell ⇒ 买卖都不成交
	s2, p2, npc2, rec2 := shopTestServer(t, "@trading", shopItem())
	p2.Char.Data.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 77, Dura: 10, DuraMax: 10}
	s2.handleBuyItem(rec2, p2, buyPacket(npc2.ID, 100, "测试剑"))
	s2.handleSellItem(rec2, p2, sellPacket(npc2.ID, 77, "测试剑"))
	if p2.Char.Data.Gold != 10000 {
		t.Errorf("脚本没声明 @buy/@sell 时金币 = %d，期望 10000", p2.Char.Data.Gold)
	}
	if countBagItemNamed(s2, p2, "测试剑") != 1 {
		t.Error("脚本没声明 @sell 时不该卖掉东西")
	}
}

// TestBuyRejectsUnknownGoods 商品名对不上（伪造的商品）⇒ 回 SM_BUYITEM_FAIL，
// 并且不动钱、不给东西。
func TestBuyRejectsUnknownGoods(t *testing.T) {
	s, p, npc, rec := shopTestServer(t, "@trading @buy @sell", shopItem())
	s.handleBuyItem(rec, p, buyPacket(npc.ID, 1, "不存在的神器"))
	if p.Char.Data.Gold != 10000 {
		t.Errorf("金币 = %d，期望不动", p.Char.Data.Gold)
	}
	fail := false
	for _, pk := range rec.packets(t) {
		if pk.Head.Ident == proto.SM_BUYITEM_FAIL {
			fail = true
		}
	}
	if !fail {
		t.Error("没收到 SM_BUYITEM_FAIL")
	}
}

// ---------- 新协议（proto）的买 / 卖 ----------
//
// ⚠️ 这两条为什么必须存在：proto 玩家的 legacy 下行会被 `protoDown` 丢掉 ⇒
// `SM_BUYITEM_SUCCESS` / `SM_USERSELLITEM_OK` 那几个号**到不了**客户端，
// 只测 legacy 那半边，新协议客户端点了买就是"没反应"（这正是用户报的那类问题）。

// protoShopServer 在 `shopTestServer` 之上挂一个新协议的出口（下行全进这个 sink）。
func protoShopServer(t *testing.T, cmdLine string, items ...*data.StdItem) (*Server, *Player, *entity.Monster, *protoSink) {
	t.Helper()
	s, p, npc, _ := shopTestServer(t, cmdLine, items...)
	sink := &protoSink{ch: make(chan *protocol.Envelope, 64)}
	p.protoOut = sink
	return s, p, npc, sink
}

// lastShopResult 从 sink 里捞**最后**一条 `ShopResult`（没有 ⇒ nil）。
func lastShopResult(sink *protoSink) *protocol.ShopResult {
	var out *protocol.ShopResult
	for {
		select {
		case env := <-sink.ch:
			if r := env.GetShopResult(); r != nil {
				out = r
			}
		default:
			return out
		}
	}
}

// TestShopBuyProto 新协议买入：扣款、发货、**回一条看得见的 `ShopResult`**。
func TestShopBuyProto(t *testing.T) {
	s, p, npc, sink := protoShopServer(t, "@trading @buy @sell", shopItem())

	s.onShopBuy(nil, p, &protocol.ShopBuy{
		NpcId: uint64(npc.ID), Name: "测试剑", Count: 1,
	})

	if got := countBagItemNamed(s, p, "测试剑"); got != 1 {
		t.Fatalf("背包里测试剑 = %d 件，期望 1", got)
	}
	if p.Char.Data.Gold != 7000 {
		t.Fatalf("金币 = %d，期望 7000（10000 - 3000）", p.Char.Data.Gold)
	}
	res := lastShopResult(sink)
	if res == nil {
		t.Fatal("没有回 `ShopResult`（新协议客户端唯一的回音）")
	}
	if !res.GetOk() {
		t.Errorf("买入该成功，实得 message=%q", res.GetMessage())
	}
	if res.GetNpcId() != uint64(npc.ID) {
		t.Errorf("ShopResult.npc_id = %d，期望 %d", res.GetNpcId(), npc.ID)
	}

	// 一次买 3 件（不可堆叠 ⇒ 占 3 个格子）：金币 ×3，背包 +3
	p.Char.Data.Gold = 10000
	s.onShopBuy(nil, p, &protocol.ShopBuy{
		NpcId: uint64(npc.ID), Name: "测试剑", Count: 3,
	})
	if got := countBagItemNamed(s, p, "测试剑"); got != 4 {
		t.Errorf("再买 3 件后应有 4 件，实得 %d", got)
	}
	if p.Char.Data.Gold != 1000 {
		t.Errorf("金币 = %d，期望 1000（10000 - 3×3000）", p.Char.Data.Gold)
	}
}

// TestShopBuyProtoRejectsWhenPoor 钱不够 ⇒ **不扣款、不发货**，且回 `ok=false`。
func TestShopBuyProtoRejectsWhenPoor(t *testing.T) {
	s, p, npc, sink := protoShopServer(t, "@trading @buy @sell", shopItem())
	p.Char.Data.Gold = 100 // 一把剑 3000

	s.onShopBuy(nil, p, &protocol.ShopBuy{
		NpcId: uint64(npc.ID), Name: "测试剑", Count: 1,
	})

	if p.Char.Data.Gold != 100 {
		t.Errorf("金币 = %d，期望不动（扣款与判定必须是同一次原子操作）", p.Char.Data.Gold)
	}
	if got := countBagItemNamed(s, p, "测试剑"); got != 0 {
		t.Errorf("没付钱就不该发货，背包里却有 %d 件", got)
	}
	res := lastShopResult(sink)
	if res == nil || res.GetOk() {
		t.Fatalf("该回 ok=false，实得 %+v", res)
	}
	if res.GetMessage() == "" {
		t.Error("失败原因要写人能读的话（proto 玩家收不到 `sysMsg`）")
	}
}

// TestShopSellProto 新协议卖出：整件卖掉，以及**只卖一堆里的一部分**。
func TestShopSellProto(t *testing.T) {
	// StdMode 0 ⇒ 可堆叠（`data.stackableModes`）；`Dura` 记数量
	potion := wuItem(2, "测试药", 0, data.MinMax{})
	potion.Price = 100
	s, p, npc, sink := protoShopServer(t, "@trading @buy @sell", shopItem(), potion)

	// ① 整件卖（武器，不可堆叠）：半价 1500
	p.Char.Data.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 77, Dura: 10, DuraMax: 10}
	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 77})
	if p.Char.Data.Gold != 11500 {
		t.Fatalf("金币 = %d，期望 11500（10000 + 3000/2）", p.Char.Data.Gold)
	}
	if countBagItemNamed(s, p, "测试剑") != 0 {
		t.Error("卖掉的武器还在背包里")
	}
	if res := lastShopResult(sink); res == nil || !res.GetOk() {
		t.Errorf("卖出该回 ok=true，实得 %+v", res)
	}

	// ② 一堆药里只卖 2 个（`Dura = 5` ⇒ 卖完还剩 3），半价 50/个
	p.Char.Data.BagItems[4] = &pb.UserItem{Index: 2, MakeIndex: 78, Dura: 5}
	before := p.Char.Data.Gold
	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 78, Count: 2})
	if want := before + 100; p.Char.Data.Gold != want {
		t.Errorf("金币 = %d，期望 %d（+%d = 2 个 × 半价 50）", p.Char.Data.Gold, want, 100)
	}
	left := -1
	for _, u := range p.Char.Data.BagItems {
		if u != nil && u.MakeIndex == 78 {
			left = int(u.Dura)
		}
	}
	if left != 3 {
		t.Errorf("堆里该剩 3 个，实得 %d（-1 = 整堆都没了，应该是只卖了一部分）", left)
	}
}

// countBagItemNamed 数背包里某名字的物品件数。
func countBagItemNamed(s *Server, p *Player, name string) int {
	n := 0
	for _, u := range p.Char.Data.BagItems {
		if u == nil || u.Index == 0 {
			continue
		}
		if tmpl := s.data.tables.Items.Get(int(u.Index) - 1); tmpl != nil && tmpl.Name == name {
			n++
		}
	}
	return n
}

// TestStackCarriesSellPrice 背包下发的 `ItemStack` 要带**卖价**。
//
// ⚠️ 用户 2026-10-10：卖窗「预估金额显示 0」—— 根因是 `itemStack` 里
// `SellPrice` 那行在上一轮的字符串替换中静默失败（函数加了、字段没填，
// Go 对未使用的私有函数不报错 ⇒ 溜过来了）。这条把它钉死。
func TestStackCarriesSellPrice(t *testing.T) {
	sword := shopItem() // 测试剑 price=3000 ⇒ 卖价 = 1500
	s, _ := flagTestServer(t, "[0 比奇省 0] MINE\n")
	set, err := data.NewStdItemSet([]*data.StdItem{sword})
	if err != nil {
		t.Fatalf("建物品表: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}

	ui := &pb.UserItem{Index: 1, MakeIndex: 7, Dura: 10, DuraMax: 10}
	st, ok := s.itemStack(ui)
	if !ok {
		t.Fatal("itemStack 该成功")
	}
	if st.GetSellPrice() != 1500 {
		t.Errorf("SellPrice = %d，期望 1500（原价 3000 的一半）", st.GetSellPrice())
	}
	if st.GetMakeIndex() != 7 {
		t.Errorf("MakeIndex = %d，期望 7", st.GetMakeIndex())
	}
}

// TestDoMoveItemSwapAndMove 包裹的**移动/交换**（用户 2026-10-10 规范第 2/3 条）。
func TestDoMoveItemSwapAndMove(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试剑", 0, data.MinMax{}))
	a := &pb.UserItem{Index: 1, MakeIndex: 11}
	b := &pb.UserItem{Index: 1, MakeIndex: 22}
	p.Char.Data.BagItems[2] = a
	p.Char.Data.BagItems[5] = b

	// 交换：2 ↔ 5
	s.doMoveItem(p, 2, 5)
	if got := p.Char.Data.BagItems[5]; got == nil || got.MakeIndex != 11 {
		t.Errorf("交换后格 5 该是 11 号物品，实得 %+v", got)
	}
	if got := p.Char.Data.BagItems[2]; got == nil || got.MakeIndex != 22 {
		t.Errorf("交换后格 2 该是 22 号物品，实得 %+v", got)
	}

	// 移动到空格：5（现在是 11 号）→ 0（0 格置空）
	p.Char.Data.BagItems[0] = nil
	s.doMoveItem(p, 5, 0)
	if got := p.Char.Data.BagItems[0]; got == nil || got.MakeIndex != 11 {
		t.Errorf("移动后格 0 该是 11 号物品（原格 5 的那件），实得 %+v", got)
	}
	if got := p.Char.Data.BagItems[5]; got != nil && got.Index != 0 {
		t.Errorf("移动后格 5 该空，实得 %+v", got)
	}

	// 越界/同格：不炸、不变
	before := p.Char.Data.BagItems[0]
	s.doMoveItem(p, 0, 0)
	s.doMoveItem(p, 0, entity.MaxBagSize+3)
	if got := p.Char.Data.BagItems[0]; got == nil || got.MakeIndex != 11 {
		t.Errorf("同格/越界不该改变背包，实得 %+v", got)
	} else {
		_ = before
	}
}

// TestShopSellMeatRemovesIt **卖肉必须真的从背包里消失**（2026-10-11 的 P0）。
//
// 根因：肉(StdMode 40)被当成"可堆叠、`Dura`=数量"，而挖肉往 `Dura` 里写的是
// **肉质量**（`butchTakeItems` 的 `ApplyMeatQuality`，比如 3000）⇒ 卖出走
// "只卖 1 个、质量 -1"那条路：肉不消失、钱照加。
// 修法：`StackLimit()` 加了"`DuraMax > 1` ⇒ 不可堆叠"（肉/矿石的 `Dura` 是
// 质量/纯度，不是数量）。这条钉住它。
func TestShopSellMeatRemovesIt(t *testing.T) {
	meat := wuItem(2, "鸡肉", 40, data.MinMax{})
	meat.DuraMax = 4000 // 质量上限（原版 ApplyMeatQuality）
	meat.Price = 80
	s, p, npc, sink := protoShopServer(t, "@trading @buy @sell", shopItem(), meat)

	// 挖肉挖出来的一块：质量 3000（Dura=3000、DuraMax=4000）
	p.Char.Data.BagItems[2] = &pb.UserItem{Index: 2, MakeIndex: 91, Dura: 3000, DuraMax: 4000}

	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 91, Count: 1})

	if got := countBagItemNamed(s, p, "鸡肉"); got != 0 {
		t.Fatalf("卖掉的肉还在背包里（%d 块）—— P0 复现", got)
	}
	// ⚠️ 卖价**按质量打折**（官方 `GetUserItemPrice`，2026-10-11 补上）：
	// 质量 3000/4000 ⇒ n10 = 80 - 80/2/4000*(4000-3000) = 70 ⇒ 卖价 35
	// （质量满才是 40）
	if p.Char.Data.Gold != 10035 {
		t.Errorf("金币 = %d，期望 10035（初值 10000 + 质量 3000/4000 的卖价 35）", p.Char.Data.Gold)
	}
	if res := lastShopResult(sink); res == nil || !res.GetOk() {
		t.Errorf("卖出该回 ok=true，实得 %+v", res)
	}
	// 矿石同理（Dura=纯度）
	ore := wuItem(2, "铜矿", 43, data.MinMax{})
	ore.DuraMax = 10000
	ore.Price = 60
	if ore.StackLimit() != 0 {
		t.Errorf("矿石(DuraMax=10000)不该可堆叠，StackLimit=%d", ore.StackLimit())
	}
}

// TestShopSellPartialStack **卖堆叠物的一部分**：堆里减掉、钱按件数算。
//
// 原来只有"整堆卖光"和"卖一件"两种被钉住（P0 那条是肉）——"卖 4 瓶剩 6 瓶"
// 走的是 `ui.Dura -= n` 那条分支，没人测过；而它是玩家最常干的事
// （身上 10 瓶药、只卖掉几瓶换钱）。
func TestShopSellPartialStack(t *testing.T) {
	potion := wuItem(2, "金创药", 0, data.MinMax{})
	potion.Price = 100 // 半价 = 50
	s, p, npc, _ := protoShopServer(t, "@trading @buy @sell", shopItem(), potion)
	p.Char.Data.Gold = 0
	p.Char.Data.BagItems[3] = &pb.UserItem{Index: 2, MakeIndex: 55, Dura: 10} // 10 瓶

	// 卖 4 瓶 ⇒ 剩 6 瓶、得 4×50
	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 55, Count: 4})

	got := p.Char.Data.BagItems[3]
	if got == nil || got.Index == 0 {
		t.Fatal("只卖了一部分 ⇒ 剩下的那堆必须还在背包里")
	}
	if got.Dura != 6 {
		t.Errorf("堆里应剩 6 瓶，实得 %d", got.Dura)
	}
	if p.Char.Data.Gold != 200 {
		t.Errorf("金币 = %d，期望 200（半价 50 × 4 瓶）", p.Char.Data.Gold)
	}

	// 卖的比堆里还多 ⇒ 按堆里的全部卖掉（不能卖成负数、也不能多给钱）
	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 55, Count: 99})
	if p.Char.Data.BagItems[3].GetIndex() != 0 {
		t.Errorf("卖光了这一格就该清空，实得 %+v", p.Char.Data.BagItems[3])
	}
	if p.Char.Data.Gold != 200+300 {
		t.Errorf("金币 = %d，期望 500（再卖 6 瓶 × 50）", p.Char.Data.Gold)
	}
}

// TestShopGoodsUsesFirstMatchingCategory 商人的**代码首位数字**决定卖什么
// （1 肉 / 2 武器 / 3 衣服 / 4 药 / 5 书 / 6 仓库 / 7 杂货 / 8 首饰 / 9 特殊）。
//
// 2026-10-17 之前这里走的是"店名关键词"：名字里"店/商"会滑到杂货，7Gst 又因为后缀
// "st" 被判成首饰店 —— 用户报的"店和能卖东西类型的关联不该靠店名关键词"。
func TestShopGoodsUsesFirstMatchingCategory(t *testing.T) {
	sword := wuItem(1, "木剑", 5, data.MinMax{})
	sword.Price = 100
	dress := wuItem(2, "布衣", 10, data.MinMax{})
	dress.Price = 100
	drug := wuItem(3, "金创药", 0, data.MinMax{})
	drug.Price = 100
	meat := wuItem(4, "肉", 40, data.MinMax{})
	meat.Price = 100
	book := wuItem(5, "火球术", 4, data.MinMax{})
	book.Price = 100
	items := []*data.StdItem{sword, dress, drug, meat, book}
	s, _, _, _ := shopTestServer(t, "", items...)
	// ⚠️ 用**真实**的商人代码 + 名字（用户 2026-10-12 纠正：武器店叫"精武馆老板"
	// 2Gwe / "卫家店" 2Bwe，卖衣服的叫"安家布衣" 3Bdr —— 名字里没有"武器/衣"）
	cases := []struct {
		id   string
		name string
		want []uint8
	}{
		{"2Gwe", "精武馆老板", []uint8{5}},
		{"2Bwe", "卫家店", []uint8{5}},
		{"3Bdr", "安家布衣", []uint8{10}},
		{"1Bme", "屠夫", []uint8{40, 41}},
		{"4Bdu", "赖家店老板", []uint8{0, 25}},
		{"5Bbo", "比奇书店", []uint8{4}},
	}
	for _, c := range cases {
		got := s.shopGoods(&data.NPC{ID: c.id, Name: c.name, IsMerchant: true})
		if len(got) == 0 {
			t.Fatalf("%s(%s)：货架是空的", c.name, c.id)
		}
		for _, it := range got {
			ok := false
			for _, w := range c.want {
				if it.StdMode == w {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s(%s) 的货架混进了 %s（StdMode %d）", c.name, c.id, it.Name, it.StdMode)
			}
		}
	}
	// 7Gst（陈家铺老板）是 **7 = 杂货**：旧实现看后缀 "st" 把它判成首饰店，
	// 用户 2026-10-17 报的就是这类串味。名字里也没有任何品类词。
	if got := s.shopGoods(&data.NPC{ID: "7Gst", Name: "陈家铺老板", IsMerchant: true}); len(got) == 0 {
		t.Fatal("7Gst 陈家铺老板：杂货货架不该是空的")
	}
	// 6Bwh 仓库 / 9Bsender 向导：**不卖东西**（不能糊一个杂货铺上去）
	for _, id := range []string{"6Bwh", "9Bsender", "", "垃圾收购员"} {
		if got := s.shopGoods(&data.NPC{ID: id, Name: "仓库保管员"}); len(got) != 0 {
			t.Errorf("%q 不该有货架，实得 %d 件", id, len(got))
		}
	}
}

// TestShopGoodsUsesScriptGoods `[goods]` 段是**权威货架**：
// 哪怕商人代码是 2（武器），脚本里只列了蜡烛，就只卖蜡烛。
func TestShopGoodsUsesScriptGoods(t *testing.T) {
	sword := wuItem(1, "木剑", 5, data.MinMax{})
	sword.Price = 100
	candle := wuItem(2, "蜡烛", 3, data.MinMax{})
	candle.Price = 10
	s, _, _, _ := shopTestServer(t, "", sword, candle)

	dir := t.TempDir()
	body := "(@buy)\r\n\r\n[@main]\r\n卖蜡烛。\\\r\n\r\n[goods]\r\n;名字 数量 小时\r\n蜡烛\t200\t1\r\n"
	if err := os.WriteFile(filepath.Join(dir, "7Gst-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir

	got := s.shopGoods(&data.NPC{ID: "7Gst", Name: "陈家铺老板", MapID: "0", IsMerchant: true})
	if len(got) != 1 || got[0].Name != "蜡烛" {
		t.Fatalf("货架应为脚本 [goods] 里的蜡烛，实得 %+v", got)
	}
	// 脚本里没列的东西一件都不许混进来（哪怕同属 7=杂货）
	for _, it := range got {
		if it.Name == "木剑" {
			t.Error("[goods] 之外的商品混进货架了")
		}
	}
}

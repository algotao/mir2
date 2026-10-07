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
	if err := os.WriteFile(filepath.Join(dir, "9shop-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir
	s.npc.defs = []*data.NPC{{ID: "9shop", Name: "武器店老板"}}

	npc := newTestMonster(66, "武器店老板", 10)
	npc.IsNPC = true
	npc.ID = 66
	npc.SetPlace(p.Obj.MapRef(), 6, 5, npc.Facing())
	s.mu.Lock()
	s.world.monsters[npc.ID] = npc
	s.mu.Unlock()

	rec := &recordingConn{}
	p.conn = rec
	p.dialog = &dialog{npcID: npc.ID, scriptName: "9shop", label: "@main"}
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

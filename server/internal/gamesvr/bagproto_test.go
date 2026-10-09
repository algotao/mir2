package gamesvr

import (
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

// TestSendBagItemsProto 背包下行：**新协议玩家必须收到 `BagItems`**。
//
// 用户 2026-10-09 说"包裹没显示"，根因就是这条链从来没接：`sendBagItems` 只发 legacy，
// 而 proto 玩家的 legacy 下行会被 `protoDown` 丢掉 —— 协议里 `BagItems` 早就定义好了，
// 但全仓**没有一处构造它**（`grep "BagItems{"` 只命中生成文件）。
//
// 这条用例同时钉住两件容易写错的事：
//   - 物品模板查表（`Index` 是 **1-based**，见 `itemStack`）；
//   - **可叠加物品的数量藏在 `Dura` 里**（原版没有数量字段）—— 取错了会显示成 1 个，
//     或者把装备的耐久当数量。
func TestSendBagItemsProto(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables

	// 拿真实表里的第 1、2 条当样本（`Index` 1-based）⇒ 断言的期望值也来自同一张表，
	// 不做硬编码（物品表换版也不会把用例写死）。
	first, second := tables.Items.Get(0), tables.Items.Get(1)
	if first == nil || second == nil {
		t.Fatal("物品表里没有前两条，用例前提不成立")
	}
	p := testMaster(100, "杂货商")
	p.Char.Data = &pb.CharacterData{
		BagItems: []*pb.UserItem{
			{Index: uint32(first.Index), Dura: 3},  // 药水：Dura 就是数量
			{Index: uint32(second.Index), Dura: 7}, // 同上
		},
	}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 8)}
	p.protoOut = sink

	s.sendBagItems(nil, p)

	var env *protocol.Envelope
	select {
	case env = <-sink.ch:
	default:
		t.Fatal("没有下发任何东西（新协议玩家应收到 BagItems）")
	}
	bag := env.GetBagItems()
	if bag == nil {
		t.Fatalf("应为 BagItems，实得 %T", env.Body)
	}
	items := bag.GetItems()
	if len(items) != 2 {
		t.Fatalf("槽数 = %d，应为 2", len(items))
	}
	for i, want := range []struct {
		name  string
		looks uint32
		count uint32
	}{
		{first.Name, uint32(first.Looks), 3},
		{second.Name, uint32(second.Looks), 7},
	} {
		got := items[i]
		if got.GetName() != want.name {
			t.Errorf("第 %d 格名称 = %q，应为 %q", i, got.GetName(), want.name)
		}
		if got.GetLooks() != want.looks {
			t.Errorf("%q 的 looks = %d，应为 %d（客户端靠它取 `Items.wzl[looks]` 的图标）",
				want.name, got.GetLooks(), want.looks)
		}
		if got.GetCount() != want.count {
			t.Errorf("%q 数量 = %d，应为 %d（可叠加物的数量在 Dura 里）",
				want.name, got.GetCount(), want.count)
		}
	}
}

// TestProtoBagOnEnter 进图必须带**背包与已穿戴**（哪怕是空的）—— 这是"客户端看得到背包"的前提。
//
// 走的是真 TCP（`protoEnterWorld` 把 hello/认领/列角/选角/进图跑完）。
func TestProtoBagOnEnter(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)
	cl, ev := protoEnterWorld(t, addr, s, sessionID, charID)

	if env := cl.waitFor(ev, "BagItems", func(e *protocol.Envelope) bool {
		return e.GetBagItems() != nil
	}); env.GetBagItems().GetItems() == nil {
		t.Log("背包是空的（本用例的角色没有物品）—— 空表也要发，不然客户端不知道自己的背包状态")
	}
	if env := cl.waitFor(ev, "EquippedItems", func(e *protocol.Envelope) bool {
		return e.GetEquippedItems() != nil
	}); env.GetEquippedItems().GetItems() == nil {
		t.Log("身上没穿东西（同上）")
	}
}

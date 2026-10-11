package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

// 探针：卖出后服务端到底下发了什么背包。
func TestProbeSellDownlink(t *testing.T) {
	potion := wuItem(2, "测试药", 0, data.MinMax{})
	potion.Price = 100
	s, p, npc, sink := protoShopServer(t, "@trading @buy @sell", shopItem(), potion)
	p.Char.Data.BagItems[3] = &pb.UserItem{Index: 1, MakeIndex: 77, Dura: 10, DuraMax: 10}
	p.Char.Data.BagItems[4] = &pb.UserItem{Index: 2, MakeIndex: 78, Dura: 5}

	s.onShopSell(nil, p, &protocol.ShopSell{NpcId: uint64(npc.ID), MakeIndex: 78, Count: 5})
	t.Logf("卖出后背包（服务端真实）:")
	for i, u := range p.Char.Data.BagItems {
		if u != nil && u.Index != 0 {
			t.Logf("  格%d: MakeIndex=%d Dura=%d", i, u.MakeIndex, u.Dura)
		}
	}
	s.sendBagItems(nil, p)
	for {
		select {
		case env := <-sink.ch:
			if b := env.GetBagItems(); b != nil {
				t.Logf("下发 BagItems 共 %d 项:", len(b.GetItems()))
				for _, it := range b.GetItems() {
					t.Logf("   %s make=%d count=%d dura=%d/%d", it.GetName(), it.GetMakeIndex(), it.GetCount(), it.GetDura(), it.GetDuraMax())
				}
			}
		default:
			return
		}
	}
}

package gamesvr

import "github.com/algotao/mir2/server/protocol"

// onShopBuy 买入（原版 `CM_USERBUYITEM`）。
//
// ⚠️ 连接传 `nil`：新协议玩家的 legacy 下行会被 `protoDown` 丢掉，回执走 `ShopResult`
// （与 `onNpcSelect` 同一条写法）。
func (ps *protoSession) onShopBuy(m *protocol.ShopBuy) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.onShopBuy(nil, p, m)
	return true
}

// onShopSell 卖出（原版 `CM_USERSELLITEM`），同上。
func (ps *protoSession) onShopSell(m *protocol.ShopSell) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.onShopSell(nil, p, m)
	return true
}

// 显示名（原版 `GetShowName` + `RefShowName`，ObjBase.pas:2654-2686 / 4477-4480）。
//
// 活实现只有两条（**公会名那一大段在原版是 `{...}` 注释掉的**，别照抄）：
//
//	GetShowName:
//	  Result := FilterShowName(m_sCharName);
//	  if (m_Master <> nil) and not m_Master.m_boObMode then
//	    Result := Result + '(' + m_Master.m_sCharName + ')';
//
//	RefShowName:
//	  SendRefMsg(RM_USERNAME, 0, 0, 0, 0, GetShowName);   // 广播给视野内的人
//
// 为什么要单独做：宠物（召唤兽 / 诱惑之光收来的兽）在协议上与野怪**完全同形**
// （同一条 SM_TURN + TCharDesc），客户端分辨"这只兽是谁的"靠的**就是这个后缀**。
// 原版在 `MakeSlave` 成功时调 `RefShowName`，我们接在召唤/诱惑成功之后。
//
// ⚠️ 我们不做 `m_boObMode`（GM 观战模式，进客户端后隐身看戏的那种）
// ⇒ 一律按 false 处理，即"主人只要在线就加后缀"。
//
// ⚠️ 名字颜色（`RefNameColor`）没做：宠物名色是客户端本地规则，协议这条
// `SM_USERNAME(42)` 的 Param 才是颜色（客户端 `GetRGB(msg.Param)`）；怪物名给 0。
package gamesvr

import (
	"log"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// showName 返回一个怪物的显示名（原版 GetShowName）。
//
// 有主（召唤兽 / 诱惑之光）⇒ `名字(主人名)`；野生怪 ⇒ 就是名字。
func (s *Server) showName(m *entity.Monster) string {
	if m == nil {
		return ""
	}
	if m.MasterID == 0 {
		return m.Name
	}
	master := s.playerByActorID(m.MasterID)
	if master == nil || master.Char == nil {
		// 原版这里是 `m_Master <> nil` 才加后缀 ⇒ 主人下线/不在线就不加
		//（我们的宠物在主人下线时会被清理，这条是兜底）
		return m.Name
	}
	return m.Name + "(" + master.Char.Name + ")"
}

// refShowName 把显示名广播给**视野内**的人（原版 RefShowName → SendRefMsg）。
//
// 用途：宠物刚认主时（名字要立刻变成"名字(主人名)"）、以及召唤兽出现时。
func (s *Server) refShowName(m *entity.Monster) {
	if m == nil || m.MapRef() == nil {
		return
	}
	name := s.showName(m)
	// 宠物名色（原版 RefNameColor → RM_CHANGENAMECOLOR；等级一变就要刷一次）。
	//
	color := uint16(slaveColorOf(m))
	s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(o *Player) {
		if o.visible.Contains(m.ID) {
			// ⚠️ 颜色在 **Param**（客户端 `GetRGB(msg.Param)`）、名字在包体、不传坐标
			// （客户端按 Recog 找 actor）。见 pvp.go 里同一处的说明。
			s.send(o.conn, proto.SM_USERNAME, int32(m.ID), color, 0, 0, name)
		}
	})
	// 这条日志是 e2e 的断言口（客户端也能收到 SM_USERNAME，但日志更好查）：
	// `check slave-name "$GSLOG" "的显示名：.*(主人名)"`
	log.Printf("ActorId=%d（%s）的显示名：%s（名色 %d）", m.ID, m.Name, name, color)
}

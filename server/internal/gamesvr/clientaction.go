package gamesvr

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
)

// `CM_SITDOWN`（打坐）与 `CM_SOFTCLOSE`（软关服）两个客户端动作。
//
// 两个都是"审计里 0 命中"的消息：前者只有一个节流 + 一条广播，后者只置两个标志，
// 但都不是"随便应答一下"就完事的 —— 语义与坑写在各自的函数上。

// handleSitDown 打坐（原版 `TPlayObject.ClientSitDownHit`，ObjBase.pas:17024-17042）：
//
//	if m_boDeath or (m_wStatusTimeArr[POISON_STONE] <> 0) then Exit;   // 死了/石化中不能打坐
//	if GetTickCount - m_dwTurnTick < dwTurnIntervalTime then Exit;      // 与转身**共用**节流
//	m_dwTurnTick := GetTickCount;
//	SendRefMsg(RM_POWERHIT, 0, 0, 0, 0, '');                            // 广播"打坐"动作
//
// 三点如实复刻、别自作主张：
//
//  1. 节流与**转身/取肉**共用一个时间戳（`m_dwTurnTick`）⇒ 我们复用 `p.turnAt`
//     （`butch.go` 里那个，间隔 `TurnIntervalTime=100ms`）；
//  2. 成功分支原版还回了一句 socket 级状态（`sSTATUS_GOOD+GetTickCount`，失败回
//     `sSTATUS_FAIL`）—— 那是"操作延迟补偿"握手（客户端带时间戳发、服务端回
//     GOOD/FAIL 决定要不要重放这条包）。我们的线协议没有这套（mir2cli 也没有）
//     ⇒ **不发**，记在这里而不是假装做了；
//  3. 广播用的是 `RM_POWERHIT`（8107）—— 而它在服务端 `ProcessMsg` 的**分派被整段
//     注释掉**（ObjBase.pas:5363-5366），客户端也没有这个 id ⇒ **原版的效果是
//     "别人看不到你坐下"**，动作由客户端本地播放。
//     我们改成广播客户端认得的 `SM_SITDOWN`(12)（客户端会把它转给该角色的动作，
//     与取肉动画走的是同一条路）—— 这是**有意的偏差**：让"打坐"在别人屏幕上可见，
//     不然这条消息在我们这儿就是纯空转。原版行为记在此处备查。
func (s *Server) handleSitDown(c net.Conn, p *Player) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil || p.Char.Data.Abil.Hp == 0 {
		return // 死了不能打坐（原版 `if m_boDeath ... then Exit`）
	}
	// ⚠️ 原版这里还有一条 `m_wStatusTimeArr[POISON_STONE] <> 0`（石化/麻痹中不能打坐），
	// 而**玩家侧的石化/麻痹状态我们还没建模**（见 docs 的麻痹戒指待办）⇒ 这条判据
	// 暂时无处可查。等玩家状态落地后补在这里（不写个恒 false 的假检查）。
	now := time.Now()
	if now.Sub(p.turnAt) < tickDur(turnIntervalTime) {
		return
	}
	p.turnAt = now

	x, y := p.Obj.PosX(), p.Obj.PosY()
	s.broadcastToViewers(p.Obj.MapRef(), x, y, func(o *Player) {
		if o == p {
			return // 自己那份由客户端本地播
		}
		s.send(o.conn, proto.SM_SITDOWN, int32(p.Obj.ID), uint16(x), uint16(y),
			uint16(p.Obj.Facing()), "")
	})
}

// gmReconnection 复刻原版 `CmdReconnection`（ObjBase.pas:14086-14105）：
//
//	if m_btPermission < 6 then Exit;                      // 命令表里的权限门槛
//	if (sIPaddr = '') or (sPort = '') then 提示命令格式; Exit;
//	SendMsg(Self, RM_RECONNECTION, 0, 0, 0, 0, sIPaddr + '/' + sPort);
//	  ⇒（RM→SM 映射，ObjBase.pas:6254-6259）
//	    m_boReconnection := True;
//	    SendDefMessage(SM_RECONNECT, 0, 0, 0, 0, sMsg);      // 正文就是 "ip/port"
//
// 客户端 `SM_RECONNECT` 的处理器是 `ClientGetReconnect(body)`（ClMain.pas:3879），
// 拿到地址后**换一个网关**重连 —— 这是原版做网关维护/迁移的手段。
//
// ⚠️ 原版那句 `if not m_boReconnection and m_boSoftClose then SendHumanLogOutMsg`
// （断线时"喊了软关服却没回来"才登出会话，:6589）落在**登录服**；
// 我们三进程架构里那是 accountsvc 的会话管理，目前**没有对应接口**
// ⇒ 这里只能像 `handleSoftClose` 那样把标志记在角色上（`p.reconnection`），
// 供将来接会话保留用。差异记在 docs/progress。
func (s *Server) gmReconnection(c net.Conn, p *Player, args []string) {
	// 原版门槛 ≥ 6；⚠️ `-gm-open`（我们 e2e 的开关）在原版没有对应物，
	// 它在那道门外就放行了（与 @give/@level 一致，见 handleSay）。
	if !s.cfg.gmOpen && p.permission < 6 {
		s.sysMsg(c, "此命令不正确，或没有足够的权限！！！")
		return
	}
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		s.sysMsg(c, "命令格式: @reconnection IP地址 端口")
		return
	}
	// 原版在 RM_RECONNECTION 里顺手置 `m_boReconnection := True`
	p.reconnection = true
	s.send(c, proto.SM_RECONNECT, 0, 0, 0, 0, args[0]+"/"+args[1])
	log.Printf("%s 通知客户端重连到 %s:%s（GM）", p.Char.Name, args[0], args[1])
	s.sysMsg(c, fmt.Sprintf("已通知客户端重连到 %s:%s", args[0], args[1]))
}

// handleSoftClose 软关服（原版 `CM_SOFTCLOSE`，ObjBase.pas:4751-4755）：
//
//	m_boReconnection := True;
//	m_boSoftClose := True;
//
// 语义在断线那段（:6573-6594）：断开时若 `boSoftClose` ⇒ 先补切图坐标、
// `MakeGhost()`（角色离开世界），然后**只有 `not m_boReconnection and m_boSoftClose`
// 才 `SendHumanLogOutMsg`**（通知登录服"这个账号退出了"）—— 也就是说客户端
// "软关服 / 退回选角"时**不要**让登录服释放会话，好让它马上重连回来。
//
// ⚠️ 我们的三进程架构里，"通知登录服"这件事在 **gate → accountsvc** 的断开回调上，
// gamesvr 看不到那个会话 ⇒ 这里能做的就是把两个标志**记在角色上**（`Player.softClose`
// / `Player.reconnection`），供断线清理与后续的"重连保留会话"使用；至于 accountsvc
// 侧要不要据此保留会话，是**另一条待办**（记在 docs/progress.md，不在这里假装做了）。
func (s *Server) handleSoftClose(c net.Conn, p *Player) {
	if p == nil {
		return
	}
	p.softClose = true
	p.reconnection = true
	log.Printf("%s 软关服（退回选角/重连意图，保留会话由网关注销路径决定）", p.Char.Name)
}

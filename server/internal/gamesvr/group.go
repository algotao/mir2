package gamesvr

// 组队系统的服务端接线。
//
// 数据与规则全在 internal/group（那里有逐条的 Delphi 行号注释），
// 本文件只做"协议 ⇄ Manager"的翻译 + 原版那些散在 ObjBase.pas 各处的
// 生命周期挂钩（死亡/下线/心跳/经验）。
//
// 消息一览（Grobal2.pas:141-144 / 319-327）
//
//	CM_GROUPMODE(1019)          Param=1 开/0 关   → SM_GROUPMODECHANGED(659, Param=开关)
//	CM_CREATEGROUP(1020)        body=对方角色名   → SM_CREATEGROUP_OK(660) / _FAIL(661, Recog=-1..-4)
//	CM_ADDGROUPMEMBER(1021)     body=对方角色名   → SM_GROUPADDMEM_OK(662) / _FAIL(664, Recog=-1..-5)
//	CM_DELGROUPMEMBER(1022)     body=对方角色名   → SM_GROUPDELMEM_OK(663, body=被删名) / _FAIL(665, Recog=-1..-3)
//	                             解散/清空名单   → SM_GROUPCANCEL(666)
//	                             刷新成员名单     → SM_GROUPMEMBERS(667, body="A/B/C/")
//
// ⚠️ 原版**没有**邀请/被邀请/同意组队这套协议：CM_CREATEGROUP 直接把人拉进来，
// 唯一条件是对方开了"允许组队"。别自创一个"邀请"流程。

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

// 组消息的显示参数（!setup.txt:389-390）。
//
// ⚠️ 组消息走的是 **SM_SYSMESSAGE(100)** 而不是 SM_GUILDMESSAGE(104)：
// 原版 SendGroupText 发 RM_GROUPMESSAGE(11005)，而它的转换是
// `MakeDefaultMsg(SM_SYSMESSAGE, BaseObject, MakeWord(FColor,BColor), 0, 1)`
// （ObjBase.pas:5671）—— 也就是 **Recog=发送者 ActorId、Series=1**。
// 我们的 SM_SYSMESSAGE 编号就是 100，别和行会消息混。
const (
	groupMsgFColor = 196 // !setup.txt GroupMsgFColor
	groupMsgBColor = 255 // !setup.txt GroupMsgBColor
	// groupMsgPrefix 是 String.ini 的 GroupMsgPreFix。
	groupMsgPrefix = "〖组队〗"
)

// 组消息文案（String.ini + ObjBase.pas:21637/21648 的 resourcestring）。
const (
	groupMsgJoined = "%s 已加入小组"
	groupMsgExited = "%s 已退出小组"
	groupMsgCancel = "你的小组已解散"
)

// groupRecallCooldown 是 @GroupRecall 的冷却（Setup/GroupRecallTime，出厂 180 秒，
// MirServer/!Setup.txt:485）。原版每次**成功使用**后重置（ObjBase.pas:12957）。
const groupRecallCooldown = 180 * time.Second

// handleGroupMsg 分派组队消息。返回 false 表示"不认这个 Ident"。
//
// 对应 ObjBase.pas:4777-4795 的 case 分支。
func (s *Server) handleGroupMsg(c net.Conn, p *Player, pkt wire.Packet) bool {
	// ⚠️ 原版对这三个消息做的是 `Trim(ProcessMsg.sMsg)` —— **直接当角色名**，
	// 没有任何分隔符解析（ObjBase.pas:4784-4795）。
	name := strings.TrimSpace(pkt.Body)

	switch pkt.Head.Ident {
	case proto.CM_GROUPMODE:
		// Param=0 ⇒ 退队/关队；Param≠0 ⇒ 打开"允许组队"开关
		// （ObjBase.pas:4777-4783）。回包永远发，值就是当前开关。
		if pkt.Head.Param == 0 {
			s.cmdGroupClose(c, p)
		} else {
			p.allowGroup = true
			s.send(c, proto.SM_GROUPMODECHANGED, 0, 1, 0, 0, "")
		}
	case proto.CM_CREATEGROUP:
		s.cmdCreateGroup(c, p, name)
	case proto.CM_ADDGROUPMEMBER:
		s.cmdAddGroupMember(c, p, name)
	case proto.CM_DELGROUPMEMBER:
		s.cmdDelGroupMember(c, p, name)
	default:
		return false
	}
	return true
}

// groupPlayer 把 *Player 转成 group.Player（判定用）。
func (s *Server) groupPlayer(p *Player) group.Player {
	gp := group.Player{ID: p.Obj.ID, Name: p.Char.Name, AllowGroup: p.allowGroup}
	// 死亡/幽灵状态不能被拉进队（ObjBase.pas:17552）
	if p.Char.Data == nil || p.Char.Data.Abil == nil || p.Char.Data.Abil.Hp == 0 {
		gp.Dead = true
	}
	return gp
}

// findPlayerByName 按角色名找在线玩家（**调用方持 s.mu**）。
func (s *Server) findPlayerByName(name string) *Player {
	for _, o := range s.world.players {
		if o.Char != nil && o.Char.Name == name {
			return o
		}
	}
	return nil
}

// cmdCreateGroup 处理 CM_CREATEGROUP（ObjBase.pas:17542-17577）。
func (s *Server) cmdCreateGroup(c net.Conn, p *Player, name string) {
	s.mu.Lock()
	leader := s.groupPlayer(p)
	var target group.Player
	if tp := s.findPlayerByName(name); tp != nil {
		target = s.groupPlayer(tp)
	}
	code, ok := s.social.groups.Create(leader, target)
	if ok {
		// ⚠️ 原版 L17573：建组后**队长的 m_boAllowGroup 被强制置 True**。
		p.allowGroup = true
	}
	members := s.groupNamesLocked(s.social.groups.LeaderOf(p.Obj.ID))
	s.mu.Unlock()

	if !ok {
		s.send(c, proto.SM_CREATEGROUP_FAIL, int32(code), 0, 0, 0, "")
		logGroup(p, "建组失败 %q: %s", name, group.CreateFailText(code))
		return
	}
	s.send(c, proto.SM_CREATEGROUP_OK, 0, 0, 0, 0, "")
	s.broadcastGroupMembers(members)
	// JoinGroup → SendGroupText(Format(g_sJoinGroup, [m_sCharName]))
	// （ObjBase.pas:24048-24053）。**双方**各收一条本人入队的提示。
	s.sendGroupText(p.Obj.ID, groupMsgJoined, p.Char.Name)
	s.sendGroupText(p.Obj.ID, groupMsgJoined, name)
	obs.Event("group_create", "leader", p.Char.Name, "member", name, "size", len(members))
	logGroup(p, "建组：%s ← %s", p.Char.Name, name)
	// 原版 NPC 钩子：g_FunctionNPC.GotoLable(Self, '@GroupCreate', False)
	s.gotoFunctionLabel(p, "@GroupCreate")
}

// cmdAddGroupMember 处理 CM_ADDGROUPMEMBER（ObjBase.pas:17579-17618）。
func (s *Server) cmdAddGroupMember(c net.Conn, p *Player, name string) {
	s.mu.Lock()
	leader := s.groupPlayer(p)
	var target group.Player
	if tp := s.findPlayerByName(name); tp != nil {
		target = s.groupPlayer(tp)
	}
	code, ok := s.social.groups.AddMember(leader, target)
	members := s.groupNamesLocked(s.social.groups.LeaderOf(p.Obj.ID))
	s.mu.Unlock()

	if !ok {
		s.send(c, proto.SM_GROUPADDMEM_FAIL, int32(code), 0, 0, 0, "")
		logGroup(p, "加成员失败 %q: %s", name, group.AddFailText(code))
		return
	}
	s.send(c, proto.SM_GROUPADDMEM_OK, 0, 0, 0, 0, "")
	s.broadcastGroupMembers(members)
	s.sendGroupText(p.Obj.ID, groupMsgJoined, name)
	obs.Event("group_add", "leader", p.Char.Name, "member", name, "size", len(members))
	logGroup(p, "%s 把 %s 拉进队（%d 人）", p.Char.Name, name, len(members))
	s.gotoFunctionLabel(p, "@GroupAddMember")
}

// cmdDelGroupMember 处理 CM_DELGROUPMEMBER（ObjBase.pas:17620-17645）。
func (s *Server) cmdDelGroupMember(c net.Conn, p *Player, name string) {
	s.mu.Lock()
	var targetID uint32
	if tp := s.findPlayerByName(name); tp != nil {
		targetID = tp.Obj.ID
	}
	leader := p.Obj.ID
	// ⚠️ **解散前的名单**要提前抓：Remove 在人数掉到 ≤1 时会直接散队
	// （CancelGroup），之后 LeaderOf 返回 0、名单就取不到了。
	// 拿它给 broadcastGroupCancel 定位收件人。
	before := s.groupNamesLocked(leader)
	code, dissolved, ok := s.social.groups.Remove(p.Obj.ID, targetID)
	members := s.groupNamesLocked(s.social.groups.LeaderOf(leader))
	s.mu.Unlock()

	if !ok {
		s.send(c, proto.SM_GROUPDELMEM_FAIL, int32(code), 0, 0, 0, "")
		logGroup(p, "删成员失败 %q: %s", name, group.DelFailText(code))
		return
	}
	s.send(c, proto.SM_GROUPDELMEM_OK, 0, 0, 0, 0, name)
	if dissolved {
		// 顺序照抄：LeaveGroup 先发"XX 已退出小组"（此时队伍还在），
		// 再由 CancelGroup 发"你的小组已解散"，最后才清名单发 CANCEL
		// （ObjBase.pas:21636-21660）。反过来发的话玩家只会看到"已解散"。
		s.sendGroupText(leader, groupMsgExited, name)
		s.sendGroupText(leader, groupMsgCancel)
		// CancelGroup：全队清空客户端列表 + 广播解散（收件人用**解散前**的名单）
		s.broadcastGroupCancel(before)
		logGroup(p, "队伍解散（最后只剩队长，已自动散队）")
	} else {
		s.broadcastGroupMembers(members)
		logGroup(p, "%s 把 %s 移出队（剩 %d 人）", p.Char.Name, name, len(members))
	}
	s.gotoFunctionLabel(p, "@GroupDelMember")
}

// cmdGroupClose 处理 CM_GROUPMODE(Param=0)：普通成员退队 / 关掉组队开关。
//
// 对应 ClientGroupClose（ObjBase.pas:17522-17540）：
//
//	不在队            → m_boAllowGroup := False; Exit;   （**不发任何包**）
//	不是队长          → 退队 + m_boAllowGroup := False
//	是队长            → 只发一句提示，**必须先删成员**（我们照抄）
func (s *Server) cmdGroupClose(c net.Conn, p *Player) {
	s.mu.Lock()
	_, ok := s.social.groups.Quit(p.Obj.ID)
	if !ok {
		// 队长不能直接退队：原版只 SysMsg 一句英文
		p.allowGroup = false
		s.mu.Unlock()
		s.sysMsg(c, "如需退出组队，请先删除队员。")
		return
	}
	// 不在队 ⇒ 也把开关关掉（原版如此）
	p.allowGroup = false
	leader := s.social.groups.LeaderOf(p.Obj.ID)
	members := s.groupNamesLocked(leader)
	s.mu.Unlock()

	s.send(c, proto.SM_GROUPMODECHANGED, 0, 0, 0, 0, "")
	if members == nil {
		// 退队后队伍已散（或本来就没队）：原版发 SM_GROUPCANCEL 清本地列表
		s.send(c, proto.SM_GROUPCANCEL, 0, 0, 0, 0, "")
	} else {
		s.broadcastGroupMembers(members)
		logGroup(p, "%s 退出了组队", p.Char.Name)
	}
	s.gotoFunctionLabel(p, "@GroupClose")
}

// sendGroupText 给全队发一条组消息。
//
// 对应 SendGroupText（ObjBase.pas:20494-20508）：**每个成员都收一条**，
// 且消息前面拼上 GroupMsgPreFix。收件人取自**队长**的名单。
func (s *Server) sendGroupText(leaderID uint32, format string, args ...any) {
	body := groupMsgPrefix + fmt.Sprintf(format, args...)
	param := uint32(groupMsgFColor) | uint32(groupMsgBColor)<<8

	s.mu.RLock()
	ids := s.social.groups.Members(leaderID)
	targets := make([]*Player, 0, len(ids))
	for _, id := range ids {
		if o := s.world.players[id]; o != nil {
			targets = append(targets, o)
		}
	}
	s.mu.RUnlock()

	// Recog=0（发送者未知，原版这里是 BaseObject 指针，组消息没有真实发送者）。
	// 客户端只用 Param 取颜色、body 取文字（ClMain.pas SM_SYSMESSAGE 分支）。
	for _, o := range targets {
		s.send(o.conn, proto.SM_SYSMESSAGE, 0, uint16(param), 0, 1, body)
	}
}

// inSameGroup 报告两个玩家是否同组（IsGroupMember，ObjBase.pas:2151-2166）。
//
// 原版的语义是**挂在目标身上**：`IsAttackTarget` 里写的是
// `if IsGroupMember(BaseObject) then Result := False`
// ——查的是"目标**是不是我的队友**"。所以 A 打 B 时只有 A 单方面被免伤，
// B 想打 A（哪怕 A 是编组模式）照样能打。
//
// ⚠️ 本函数**不要求持 s.mu**：group.Manager 自带锁，而它会被
// canAttackTarget 读到——那条路径明确禁止持 s.mu（见 wall.go 的三段式注释）。
func (s *Server) inSameGroup(a, b *Player) bool {
	if a == nil || b == nil || a.Obj == nil || b.Obj == nil {
		return false
	}
	return s.social.groups.IsMember(a.Obj.ID, b.Obj.ID)
}

// ---------- 名单广播 ----------

// groupNamesLocked 取队长名下所有成员的角色名。**调用方持 s.mu**。
func (s *Server) groupNamesLocked(leaderID uint32) []string {
	ids := s.social.groups.Members(leaderID)
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if o := s.world.players[id]; o != nil && o.Char != nil {
			out = append(out, o.Char.Name)
		} else {
			// 名单里有 offline 的成员（理论上不该有，Sweep 会清）：
			// 保留占位会让客户端列表错位，改为跳过。
			log.Printf("组队名单里的 ActorId=%d 不在线，跳过", id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// broadcastGroupMembers 给全队每个人下发同一份成员名单。
//
// 对应 SendGroupMembers（ObjBase.pas:21662-21679）：**每个成员都收一份**，
// body 是全队名字（不是"从你开始的后半段"）。
func (s *Server) broadcastGroupMembers(names []string) {
	if len(names) == 0 {
		return
	}
	body := group.MembersBody(names)
	s.mu.RLock()
	// 收件人按**名字**匹配而不是按 leader 索引：解散/退队后索引已经变了，
	// 但刚退队的那个人仍需要收到最后一次名单刷新。
	var targets []*Player
	for _, o := range s.world.players {
		if o.Char == nil {
			continue
		}
		for _, n := range names {
			if o.Char.Name == n {
				targets = append(targets, o)
				break
			}
		}
	}
	s.mu.RUnlock()
	for _, o := range targets {
		s.send(o.conn, proto.SM_GROUPMEMBERS, 0, 0, 0, 0, body)
	}
}

// broadcastGroupCancel 给全队发 SM_GROUPCANCEL（清空客户端列表）。
// members 是解散前的名单（用来定位收件人）。
func (s *Server) broadcastGroupCancel(members []string) {
	if len(members) == 0 {
		return
	}
	s.mu.RLock()
	var targets []*Player
	for _, o := range s.world.players {
		if o.Char == nil {
			continue
		}
		for _, n := range members {
			if o.Char.Name == n {
				targets = append(targets, o)
				break
			}
		}
	}
	s.mu.RUnlock()
	for _, o := range targets {
		s.send(o.conn, proto.SM_GROUPCANCEL, 0, 0, 0, 0, "")
	}
}

// ---------- 生命周期挂钩 ----------

// groupDrop 把玩家移出队伍（死亡/下线），并给其余人刷新名单。
//
// 对应两处原版：
//   - ObjBase.pas:21040-21044：**人物死亡立即退组，以防止组队刷经验**；
//   - ObjBase.pas:15470-15478（Disappear）：下线时 `m_GroupOwner.DelMember(Self)`。
func (s *Server) groupDrop(p *Player) {
	if p == nil || p.Obj == nil {
		return
	}
	s.mu.Lock()
	leader := s.social.groups.LeaderOf(p.Obj.ID)
	if leader == 0 {
		s.mu.Unlock()
		return
	}
	// 解散前把收件人名单抓出来（Drop 之后 leader 索引就没了）
	recipients := s.groupNamesLocked(leader)
	s.social.groups.Drop(p.Obj.ID)
	remaining := s.groupNamesLocked(s.social.groups.LeaderOf(p.Obj.ID))
	s.mu.Unlock()

	if remaining == nil {
		// 散队：先按原版顺序发两条提示，再清客户端列表
		// （LeaveGroup 的"已退出" + CancelGroup 的"已解散"，ObjBase.pas:21636-21660）。
		s.sendGroupText(leader, groupMsgExited, p.Char.Name)
		s.sendGroupText(leader, groupMsgCancel)
		recipients = append(recipients, p.Char.Name)
		s.broadcastGroupCancel(recipients)
	} else {
		s.sendGroupText(leader, groupMsgExited, p.Char.Name)
		s.broadcastGroupMembers(remaining)
	}
	logGroup(p, "%s 离开组队", p.Char.Name)
}

// groupSweep 每轮心跳清理"组长已死/成员已死"的队伍。
//
// 对应 ObjBase.pas:4113-4132 TBaseObject.Run。
func (s *Server) groupSweep() {
	s.mu.Lock()
	dead := func(id uint32) bool {
		o := s.world.players[id]
		if o == nil || o.Char == nil || o.Char.Data == nil ||
			o.Char.Data.Abil == nil || o.Char.Data.Abil.Hp == 0 {
			return true // 不在线也算死（原版 m_boGhost）
		}
		return false
	}
	dropped := s.social.groups.Sweep(dead)
	var names []string
	for _, id := range dropped {
		if o := s.world.players[id]; o != nil && o.Char != nil {
			names = append(names, o.Char.Name)
		}
	}
	s.mu.Unlock()
	if len(names) > 0 {
		s.broadcastGroupCancel(names)
	}
}

// ---------- 玩家命令 ----------

// cmdAllowGroupRecall @AllowGroupRecall：切换"是否允许被队长整组传送"。
//
// 对应 ObjBase.pas:11355-11366。**落盘**（HumData.boAllowGroupReCall，
// ObjBase.pas:24937 / UsrEngn.pas:2369）。
func (s *Server) cmdAllowGroupRecall(c net.Conn, p *Player) {
	p.allowGroupRecall = !p.allowGroupRecall
	if p.allowGroupRecall {
		s.sysMsg(c, "你允许被组队传送。")
	} else {
		s.sysMsg(c, "你拒绝被组队传送。")
	}
	s.savePlayer(p)
}

// cmdGroupRecall @GroupRecall：队长把全队（不含自己）拉到身边。
//
// 对应 ObjBase.pas:12942-12996。要点：
//   - **只有队长能用**（`m_GroupOwner = Self`）；
//   - 冷却 `nGroupRecallTime = 180` 秒（!Setup.txt:485）；
//   - 从索引 1 开始 ⇒ **跳过队长自己**；
//   - 逐个判 `m_boAllowGroupReCall`，被拒绝的要单独提示；
//   - 队员所在地图禁止传送（Flag.boNORECALL）时只提示不传送。
func (s *Server) cmdGroupRecall(c net.Conn, p *Player) {
	// 地图标记 `NORECALL`：官方在**队长那张图**上就是整条命令跳过
	//（`ObjBase.pas:12950` `if not m_PEnvir.Flag.boNORECALL`）⇒ 静默不做。
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoRecall {
		return
	}
	now := time.Now()
	if now.Before(p.groupRecallUntil) {
		secs := int(p.groupRecallUntil.Sub(now).Seconds())
		s.sysMsg(c, "组传送冷却中，还需 "+strconv.Itoa(secs)+" 秒。")
		return
	}
	s.mu.RLock()
	leader := s.social.groups.LeaderOf(p.Obj.ID)
	isLeader := s.social.groups.IsLeader(p.Obj.ID)
	ids := s.social.groups.Members(leader)
	s.mu.RUnlock()

	if !isLeader {
		s.sysMsg(c, "只有队长才能使用整组传送。")
		return
	}
	moved, refused := 0, 0
	for _, id := range ids {
		if id == p.Obj.ID { // 索引 0 是队长自己
			continue
		}
		o := s.world.players[id]
		if o == nil {
			continue
		}
		if !o.allowGroupRecall {
			refused++
			s.sysMsg(c, o.Char.Name+" 拒绝被组队传送。")
			continue
		}
		if p.Obj.MapRef() == nil {
			continue
		}
		if err := s.switchMap(o.conn, o, p.Obj.MapRef().Name, p.Obj.PosX(), p.Obj.PosY()); err != nil {
			continue
		}
		moved++
	}
	p.groupRecallUntil = now.Add(groupRecallCooldown)
	if moved == 0 && refused == 0 {
		s.sysMsg(c, "队里没有其它成员。")
	}
	obs.Event("group_recall", "leader", p.Char.Name, "moved", moved, "refused", refused)
	logGroup(p, "整组传送：拉来 %d 人，%d 人拒绝", moved, refused)
}

func logGroup(p *Player, format string, args ...any) {
	if p == nil || p.Char == nil {
		return
	}
	log.Printf(p.Char.Name+" "+format, args...)
}

// actGroupMoveMap 实现脚本 GROUPMOVEMAP：整队一起传送。
//
// 对应 ObjNpc.pas:10962-10992。判定顺序照抄：
// 不在队 → 地图不存在 → 目标格不可走 ⇒ 都是**整条指令失败**（不部分执行）。
func (s *Server) actGroupMoveMap(c net.Conn, p *Player, mapID string, x, y int) {
	s.mu.RLock()
	ids := s.social.groups.Members(s.social.groups.LeaderOf(p.Obj.ID))
	s.mu.RUnlock()

	if len(ids) == 0 {
		s.sysMsg(c, "GROUPMOVEMAP：你还没有组队")
		return
	}
	mp, err := s.world.maps.Get(mapID)
	if err != nil || mp == nil {
		s.sysMsg(c, "GROUPMOVEMAP：地图 "+mapID+" 不存在")
		return
	}
	// ⚠️ 原版判的是**目标坐标**在目标地图上可走（`Envir.CanWalk(x,y,True)`），
	// 一次判定、所有成员共用；不可走就整条失败（不是逐个人试）。
	if !mp.CanWalk(x, y) {
		s.sysMsg(c, "GROUPMOVEMAP：目标坐标不可走")
		return
	}

	moved := 0
	for _, id := range ids {
		o := s.world.players[id]
		if o == nil {
			continue
		}
		if err := s.switchMap(o.conn, o, mapID, x, y); err != nil {
			logGroup(p, "GROUPMOVEMAP：%s 传送失败：%v", o.Char.Name, err)
			continue
		}
		moved++
	}
	obs.Event("group_movemap", "leader", p.Char.Name, "map", mapID, "x", x, "y", y, "moved", moved)
	logGroup(p, "GROUPMOVEMAP → %s(%d,%d)，%d/%d 人到位", mapID, x, y, moved, len(ids))
}

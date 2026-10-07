package gamesvr

// 行会系统（P6）。
//
// 协议对照（消息号来自 MirClient/Grobal2.pas:143-153、359-380，
// 服务端处理在 ObjBase.pas:18014-18302、Guild.pas）：
//
//	CM_OPENGUILDDLG(1035) / CM_GUILDHOME(1036)
//	    → SM_OPENGUILDDLG(753, Series=1) / SM_OPENGUILDDLG_FAIL(754)
//	CM_GUILDMEMBERLIST(1037)     → SM_SENDGUILDMEMBERLIST(756, Series=1)
//	CM_GUILDADDMEMBER(1038)      → SM_GUILDADDMEMBER_OK(757) / _FAIL(758, Recog=原因码 1..5)
//	CM_GUILDDELMEMBER(1039)      → SM_GUILDDELMEMBER_OK(759) / _FAIL(760, Recog=1..4)
//	CM_GUILDUPDATENOTICE(1040)   → 无 OK 包，直接回 SM_OPENGUILDDLG 刷新界面
//	CM_GUILDUPDATERANKINFO(1041) → SM_SENDGUILDMEMBERLIST / SM_GUILDRANKUPDATE_FAIL(761, Recog=-2..-7)
//	CM_GUILDALLY(1044)            → SM_GUILDMAKEALLY_OK(768) / _FAIL(769, Recog=-1..-4)
//	CM_GUILDBREAKALLY(1045, body=对方行会名)
//	                              → SM_GUILDBREAKALLY_OK(770) / _FAIL(回 769 且 Recog=0)
//	建会：NPC 对话框的 @@buildguildnow 标签（走 CM_MERCHANTDLGSELECT）
//	      → SM_BUILDGUILD_OK(762) / SM_BUILDGUILD_FAIL(763, Recog=-1..-4)
//	行会聊天：CM_SAY + '!~' 前缀 → SM_GUILDMESSAGE(104, Param=FColor|BColor<<8, Series=1)
//
// ⚠️ 原版四个怪癖必须保留：
//  1. 解盟失败时发的是 SM_GUILDMAKEALLY_FAIL(769) 而不是 BREAKALLY_FAIL(771)
//     （ObjBase.pas:18298），后者从未被发送过；且 Recog 传的是 **0**（不是原因码）；
//  2. @BanGuildChat 的开关变量实际语义是"**允许**收行会聊天"，默认 True
//     （ObjBase.pas:1329、7646-7652）；
//  3. **重复结盟回 0（成功）**：AllyGuild 内部去重，但 ClientGuildAlly 把它的
//     返回值丢了，无脑 n8 := 0（ObjBase.pas:18240-18249）；
//  4. 宣战与结盟互斥，且是**双向**判定：AddWarGuild 开头 `if not IsAllyGuild`
//     （Guild.pas:1213，盟友之间宣不了战），ClientGuildAlly 判两侧
//     IsNotWarGuild（战争期间一律 -2）。

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/wire"
)

// guildCtx 是行会操作使用的上下文。
//
// 行会读写都是本地 SQLite 的短操作，没有可取消的调用链；原版同样没有超时概念。
func guildCtx() context.Context { return context.Background() }

// guildConfig 是行会配置，默认值取自原版 g_Config。
type guildConfig struct {
	// buildGold 是建会费用（Setup/BuildGuild，M2Share.pas:1760）。
	buildGold int64
	// buildItem 是建会必须持有的号角（Names/WomaHorn，M2Share.pas:1632）。
	buildItem string
	// warGold 是宣战费用（Setup/GuildWarFee，M2Share.pas:1761）。
	//
	// ⚠️ 原版 ReQuestGuildWar **本身不扣费**（ObjBase.pas:26722），
	// 费用由 NPC 脚本层承担。我们保留了字段以便将来接脚本层。
	warGold int64
	// warDuration 是宣战后的战争持续时间。
	warDuration time.Duration
	// chatFColor / chatBColor 是行会聊天前景/背景色（M2Share.pas:1852-1853）。
	chatFColor, chatBColor uint8
}

// defaultGuildConfig 是出厂默认值。
func defaultGuildConfig() guildConfig {
	return guildConfig{
		buildGold:  1_000_000,
		buildItem:  "沃玛号角",
		warGold:    30_000,
		chatFColor: 0xDB,
		chatBColor: 0xFF,
	}
}

// 加成员失败原因码（ObjBase.pas:18082-18125，客户端 ClMain.pas:5453-5464 按此弹文案）。
const (
	guildAddNoPermission = 1 // 不是掌门
	guildAddNotFacing    = 2 // 需面对面（对方朝向你）
	guildAddAlreadyIn    = 3 // 对方已在本行会
	guildAddTargetTaken  = 4 // 对方已加入其它行会 / 职务数超限
	guildAddTargetRefuse = 5 // 对方不允许入会（未开 @LetGuild）
)

// 删成员失败原因码（ObjBase.pas:18127-18178，客户端 ClMain.pas:5468-5475）。
const (
	guildDelNoPermission = 1 // 不是掌门
	guildDelNotMember    = 2 // 对方不是本会成员
	guildDelSelf         = 3 // 掌门不能开除自己（解散失败时也回这个码）
	guildDelFailed       = 4 // 删除失败
)

// 建会失败原因码（ObjNpc.pas:11317-11364，客户端 ClMain.pas:5503-5515）。
const (
	guildBuildAlreadyIn = -1 // 已加入其它行会
	guildBuildNoGold    = -2 // 缺少创建费用
	guildBuildNoItem    = -3 // 缺少号角
	guildBuildFailed    = -4 // 名字非法 / 重名 / 其它
)

// 结盟失败原因码（ObjBase.pas:18225-18259，客户端 ClMain.pas:4887-4894 按此弹文案）。
//
// ⚠️ 别与"加成员失败码"（1..5）混用：两套编号方向相反（结盟是 -1..-4）。
// ⚠️ 客户端对**没有列出**的 Recog 值不弹任何窗（case 里没有 default），
// 所以解盟失败发 Recog=0 时真客户端是**静默**的——那是原版自己的行为。
const (
	allyFailNoPose       = -1 // "您无此权限！"：正对面没人/不是玩家/对方没行会/对方不朝向你
	allyFailAtWar        = -2 // "结盟失败！"：正在行会战（也兜内部落库失败）
	allyFailNotChief     = -3 // "行会结盟必须双方掌门人面对面！"：有一方不是掌门
	allyFailTargetRefuse = -4 // "对方行会掌门人不允许结盟！"：对方没开 @AuthAlly
)

// maxGuildRanks 是职务条目数上限（原版写死 400，ObjBase.pas:18099）。
const maxGuildRanks = 400

// maxGuildNameRunes 是行会名长度上限（原版 nGuildNameLen=16，"字符"在
// 本地码页下按字节算；我们按字符算，避免中文名被过早截断）。
const maxGuildNameRunes = 16

// ---------- 入口 ----------

// handleGuildMsg 处理行会窗口相关消息，返回是否已处理。
func (s *Server) handleGuildMsg(c net.Conn, p *Player, pkt wire.Packet) bool {
	switch pkt.Head.Ident {
	case proto.CM_OPENGUILDDLG, proto.CM_GUILDHOME:
		s.openGuildDlg(c, p)
	case proto.CM_GUILDMEMBERLIST:
		s.sendGuildMemberList(c, p)
	case proto.CM_GUILDADDMEMBER:
		s.handleGuildAddMember(c, p, pkt.Body)
	case proto.CM_GUILDDELMEMBER:
		s.handleGuildDelMember(c, p, pkt.Body)
	case proto.CM_GUILDUPDATENOTICE:
		s.handleGuildUpdateNotice(c, p, pkt.Body)
	case proto.CM_GUILDUPDATERANKINFO:
		s.handleGuildUpdateRank(c, p, pkt.Body)
	case proto.CM_GUILDALLY:
		// 原版入口：客户端在行会窗口点"结盟"（ObjBase.pas:4880 → ClientGuildAlly）。
		// ⚠️ 这条**必须**接——只留 @联盟 的聊天入口等于真客户端点按钮没反应。
		s.cmdAlly(c, p)
	case proto.CM_GUILDBREAKALLY:
		// 原版入口：body 是对方行会名（ObjBase.pas:4884 → ClientGuildBreakAlly）。
		// ⚠️ cutAt 的返回序是 (rest, head)：行会名在 **head**（分隔符之后那半是
		// 下一段），取错就是空串 → Find("") → 静默回 FAIL。
		_, name := cutAt(strings.TrimSpace(pkt.Body), "\r\n")
		s.cmdBreakAlly(c, p, []string{name})
	default:
		return false
	}
	return true
}

// guildOf 实时查询玩家所在行会（成员名反查，等价原版 m_MyGuild）。
//
// 不在 Player 上缓存行会状态：一切以 internal/guild.Manager 为准，
// 避免"被踢了但本地还留着旧行会"这类不一致。
//
// ⚠️ 必须容忍 nil 入参：cmdAlly 会拿"正对面那格"（可能是 nil）去查，
// 原版那边是 `BaseObjectC <> nil and BaseObjectC.m_MyGuild <> nil` 短路，
// 我们不能靠调用方判 nil —— 漏一处就是整服进程崩掉（已踩过一次）。
func (s *Server) guildOf(p *Player) (*guildRef, bool) {
	if p == nil || p.Char == nil {
		return nil, false
	}
	g, rankNo, rankName := s.social.guilds.OfMember(p.Char.Name)
	if g == nil {
		return nil, false
	}
	return &guildRef{guild: g, rankNo: rankNo, rankName: rankName}, true
}

type guildRef struct {
	guild    *storage.Guild
	rankNo   int
	rankName string
}

// isChief 判断是否是行会掌门（rank 1，等价原版 IsGuildMaster）。
func (r *guildRef) isChief() bool { return r.rankNo == guild.ChiefRankNo }

// openGuildDlg 打开行会窗口（CM_OPENGUILDDLG / CM_GUILDHOME 同一路径，
// ObjBase.pas:18014-18059）。
//
// body 结构（全部 #13 结尾）：
//
//	行会名 / " " / "1"|"0"（是否掌门） / <Notice> / 公告行…
//	/ <KillGuilds> / 敌对行会… / <AllyGuilds> / 联盟行会…
func (s *Server) openGuildDlg(c net.Conn, p *Player) {
	ref, ok := s.guildOf(p)
	if !ok {
		s.send(c, proto.SM_OPENGUILDDLG_FAIL, 0, 0, 0, 0, "")
		return
	}
	g := ref.guild

	var b strings.Builder
	b.WriteString(g.Name)
	b.WriteByte(13)
	b.WriteString(" ")
	b.WriteByte(13)
	if ref.isChief() {
		b.WriteString("1")
	} else {
		b.WriteString("0")
	}
	b.WriteByte(13)

	b.WriteString("<Notice>")
	b.WriteByte(13)
	for _, line := range g.Notice {
		// ⚠️ 原版先查长度再加行（ObjBase.pas:18032），保持一致。
		if b.Len() > 5000 {
			break
		}
		b.WriteString(line)
		b.WriteByte(13)
	}
	b.WriteString("<KillGuilds>")
	b.WriteByte(13)
	// 只列**未到期**的敌对行会（过期项顺带在此惰性清理）
	for _, name := range s.social.guilds.ActiveWars(g, time.Now()) {
		b.WriteString(name)
		b.WriteByte(13)
	}
	b.WriteString("<AllyGuilds>")
	b.WriteByte(13)
	for _, a := range g.Allies {
		b.WriteString(a)
		b.WriteByte(13)
	}

	s.send(c, proto.SM_OPENGUILDDLG, 0, 0, 0, 1, b.String())
}

// sendGuildMemberList 下发成员列表（ObjBase.pas:18061-18080）。
//
// body 语法：每个职务为 "#<职务号>/*<职务名>/"，随后逐个 "<成员名>/"；
// 总长超 5000 时只截断成员、不截断职务行（原版行为）。
func (s *Server) sendGuildMemberList(c net.Conn, p *Player) {
	ref, ok := s.guildOf(p)
	if !ok {
		return // 原版：不在行会什么都不发
	}

	var b strings.Builder
	for _, r := range ref.guild.Ranks {
		b.WriteByte('#')
		b.WriteString(strconv.Itoa(r.No))
		b.WriteString("/*")
		b.WriteString(r.Name)
		b.WriteByte('/')
		for _, mem := range r.Members {
			if b.Len() > 5000 {
				break
			}
			b.WriteString(mem)
			b.WriteByte('/')
		}
	}
	s.send(c, proto.SM_SENDGUILDMEMBERLIST, 0, 0, 0, 1, b.String())
}

// ---------- 建会 ----------

// requestBuildGuild 处理 @@buildguildnow（ObjNpc.pas:11317-11364）。
//
// 顺序与原版一致：先校验（未入会/金币/号角），全部通过才扣费建会。
func (s *Server) requestBuildGuild(c net.Conn, p *Player, name string) {
	name = strings.TrimSpace(name)
	ctx := guildCtx()

	if name == "" || !validGuildName(name) {
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildFailed, 0, 0, 0, "")
		return
	}
	if _, ok := s.guildOf(p); ok {
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildAlreadyIn, 0, 0, 0, "")
		return
	}
	d := p.Char.Data
	if d == nil {
		return
	}
	// 预检（给一句友好回执）；**真正**的判据是下面那次原子扣款
	if p.gold() < s.social.guildCfg.buildGold {
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildNoGold, 0, 0, 0, "")
		return
	}
	hornIdx := s.findBagItemByName(p, s.social.guildCfg.buildItem)
	if hornIdx < 0 {
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildNoItem, 0, 0, 0, "")
		return
	}

	// ⚠️ 先把钱**原子扣掉**再建会。原版是"建会成功后再扣"（ObjNpc.pas:11352-11357），
	// 在我们这里那样写不安全：建会已经生效之后那次扣款可能失败（并发收支覆盖）
	// ⇒ 白送一个行会。改成"先扣 → 建会 → 失败退款"，净效果一致。
	if !p.spendGold(s.social.guildCfg.buildGold) {
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildNoGold, 0, 0, 0, "")
		return
	}
	if _, err := s.social.guilds.Create(ctx, name, p.Char.Name); err != nil {
		log.Printf("%s 建会 %q 失败: %v", p.Char.Name, name, err)
		p.addGold(s.social.guildCfg.buildGold) // 建会没成 ⇒ 把钱还回去
		s.send(c, proto.SM_BUILDGUILD_FAIL, guildBuildFailed, 0, 0, 0, "")
		return
	}

	// 扣号角（钱已经在上面扣掉了）
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	s.takeBagItem(p, hornIdx)
	s.sendBagItems(c, p)

	s.send(c, proto.SM_BUILDGUILD_OK, 0, 0, 0, 0, "")
	s.sendChangeGuildName(c, p)
	s.sysMsg(c, fmt.Sprintf("行会 %s 创建成功", name))
	log.Printf("%s 创建行会 %s（花费 %d 金币 + 1 个%s）",
		p.Char.Name, name, s.social.guildCfg.buildGold, s.social.guildCfg.buildItem)
}

// validGuildName 校验行会名（M2Share.pas:3581-3609 CheckGuildName）。
//
// 原版按字节比较：任何 < '0'(0x30) 的字符与 / \ : * 空格 " ' < | ? > 都非法。
func validGuildName(name string) bool {
	if utf8.RuneCountInString(name) > maxGuildNameRunes {
		return false
	}
	for _, c := range name {
		if c < '0' || strings.ContainsRune(`/\:* "'<|?>`, c) {
			return false
		}
	}
	return true
}

// ---------- 成员管理 ----------

// cutAt 在首个 CR/LF 处切分并跳过其后的连续换行
// （等价 HUtil32.GetValidStr3 对 [#$0D] 的处理，空行会被跳过）。
func cutAt(s, seps string) (rest, head string) {
	i := strings.IndexAny(s, seps)
	if i < 0 {
		return "", s
	}
	j := i
	for j < len(s) && strings.IndexByte(seps, s[j]) >= 0 {
		j++
	}
	return s[j:], s[:i]
}

// playerByName 按角色名查在线玩家（等价原版 UserEngine.GetPlayObject）。
func (s *Server) playerByName(name string) *Player {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.world.players {
		if p.Char != nil && p.Char.Name == name {
			return p
		}
	}
	return nil
}

// faceTo 判断 a 是否正朝向 b（原版 GetPoseCreate = Self 的等价物）。
func faceTo(a, b *Player) bool {
	if a.Obj.MapRef() != b.Obj.MapRef() {
		return false
	}
	d := entity.DirDelta[a.Obj.Facing()]
	return b.Obj.PosX() == a.Obj.PosX()+d[0] && b.Obj.PosY() == a.Obj.PosY()+d[1]
}

// handleGuildAddMember 加成员（ObjBase.pas:18082-18125）。
//
// body = 目标角色名。成功条件：自己是掌门、对方在线且正面对着自己、
// 对方开着 @LetGuild、对方未加入任何行会、本会职务数 < 400。
func (s *Server) handleGuildAddMember(c net.Conn, p *Player, body string) {
	ref, ok := s.guildOf(p)
	if !ok {
		return
	}
	name := strings.TrimSpace(body)
	code := guildAddNoPermission
	if ref.isChief() {
		code = guildAddNotFacing
		if target := s.playerByName(name); target != nil {
			switch {
			case !faceTo(target, p):
				code = guildAddNotFacing
			case !target.allowGuild:
				code = guildAddTargetRefuse
			case guild.IsMember(ref.guild, name):
				code = guildAddAlreadyIn
			default:
				if _, inGuild := s.guildOf(target); inGuild || len(ref.guild.Ranks) >= maxGuildRanks {
					code = guildAddTargetTaken
				} else if err := s.social.guilds.AddMember(guildCtx(), ref.guild.Name, name); err != nil {
					log.Printf("加成员 %s 入 %s 失败: %v", name, ref.guild.Name, err)
					code = guildAddTargetTaken
				} else {
					code = 0
				}
			}
		}
	}

	if code == 0 {
		s.send(c, proto.SM_GUILDADDMEMBER_OK, 0, 0, 0, 0, "")
		s.sendChangeGuildName(c, p)
		if target := s.playerByName(name); target != nil {
			s.sendChangeGuildName(target.conn, target)
			s.sysMsg(target.conn, fmt.Sprintf("你加入了行会 %s", ref.guild.Name))
		}
		s.sendGuildMemberList(c, p)
		log.Printf("%s 把 %s 加入行会 %s", p.Char.Name, name, ref.guild.Name)
		return
	}
	s.send(c, proto.SM_GUILDADDMEMBER_FAIL, int32(code), 0, 0, 0, "")
}

// handleGuildDelMember 开除成员 / 解散行会（ObjBase.pas:18127-18178）。
//
// body = 目标角色名。body 与自己同名 = 解散行会（需只剩掌门一人）。
func (s *Server) handleGuildDelMember(c net.Conn, p *Player, body string) {
	ref, ok := s.guildOf(p)
	if !ok {
		return
	}
	name := strings.TrimSpace(body)
	ctx := guildCtx()
	code := guildDelNoPermission
	if ref.isChief() {
		code = guildDelNotMember
		switch {
		case !guild.IsMember(ref.guild, name):
			code = guildDelNotMember
		case name != p.Char.Name:
			removed, err := s.social.guilds.DelMember(ctx, ref.guild.Name, name)
			if err != nil {
				log.Printf("开除 %s 出 %s 失败: %v", name, ref.guild.Name, err)
				code = guildDelFailed
			} else if !removed {
				code = guildDelFailed
			} else {
				code = 0
			}
			if code == 0 {
				if target := s.playerByName(name); target != nil {
					s.sendChangeGuildName(target.conn, target) // 空 body：退出行会
					s.sysMsg(target.conn, fmt.Sprintf("你被移出了行会 %s", ref.guild.Name))
				}
			}
		default:
			// 删自己 = 解散行会（原版 CancelGuld + DELGUILD，Guild.pas:128-147、896-909）。
			code = guildDelSelf
			if guild.CanCancel(ref.guild, p.Char.Name) {
				if err := s.social.guilds.Delete(ctx, ref.guild.Name); err != nil {
					log.Printf("解散行会 %s 失败: %v", ref.guild.Name, err)
				} else {
					code = 0
					s.sendChangeGuildName(c, p)
					log.Printf("%s 解散了行会 %s", p.Char.Name, ref.guild.Name)
				}
			}
		}
	}

	if code == 0 {
		s.send(c, proto.SM_GUILDDELMEMBER_OK, 0, 0, 0, 0, "")
		// ⚠️ 解散后必须清头顶信息：否则客户端（以及服务端自己的
		// sendChangeGuildName）会以为角色还在行会里，重连后拿不到
		// SM_CHANGEGUILDNAME，表现为用例挂在"未收到 SM_CHANGEGUILDNAME"。
		s.sendChangeGuildName(c, p)
		if _, still := s.guildOf(p); still {
			s.sendGuildMemberList(c, p)
		}
		return
	}
	s.send(c, proto.SM_GUILDDELMEMBER_FAIL, int32(code), 0, 0, 0, "")
}

// refreshGuildOnline 给行会内所有在线成员重发 SM_CHANGEGUILDNAME
// （等价原版 UpdateRank 成功后的 RefRankInfo + RefShowName，Guild.pas:1154-1168）。
func (s *Server) refreshGuildOnline(guildName string) {
	for _, name := range s.social.guilds.Roster(guildName) {
		if target := s.playerByName(name); target != nil {
			s.sendChangeGuildName(target.conn, target)
		}
	}
}

// ---------- 公告 / 职务表 ----------

// handleGuildUpdateNotice 改公告（ObjBase.pas:18180-18194）。
//
// 只有掌门有效；无 OK 回包，成功后回 SM_OPENGUILDDLG 刷新界面。
func (s *Server) handleGuildUpdateNotice(c net.Conn, p *Player, body string) {
	ref, ok := s.guildOf(p)
	if !ok || !ref.isChief() {
		return
	}
	var lines []string
	for body != "" {
		var line string
		body, line = cutAt(body, "\r\n")
		lines = append(lines, line)
	}
	if err := s.social.guilds.SetNotice(guildCtx(), ref.guild.Name, lines); err != nil {
		log.Printf("更新 %s 公告失败: %v", ref.guild.Name, err)
		return
	}
	s.openGuildDlg(c, p)
}

// handleGuildUpdateRank 改职务表（ObjBase.pas:18196-18215 + Guild.pas:911-1174）。
//
// 只有掌门有效；成功回成员列表，-1（无变化）不回包，-2 及以下回 RANKUPDATE_FAIL。
func (s *Server) handleGuildUpdateRank(c net.Conn, p *Player, body string) {
	ref, ok := s.guildOf(p)
	if !ok || !ref.isChief() {
		return
	}
	code, err := s.social.guilds.UpdateRanks(guildCtx(), ref.guild.Name, body, func(name string) bool {
		return s.playerByName(name) != nil
	})
	if err != nil {
		log.Printf("更新 %s 职务表失败: %v", ref.guild.Name, err)
		return
	}
	switch {
	case code == guild.RankOK:
		s.refreshGuildOnline(ref.guild.Name)
		s.sendGuildMemberList(c, p)
		log.Printf("%s 更新了行会 %s 的职务表", p.Char.Name, ref.guild.Name)
	case code <= -2:
		s.send(c, proto.SM_GUILDRANKUPDATE_FAIL, int32(code), 0, 0, 0, "")
	}
}

// ---------- 行会聊天 ----------

// handleGuildChat 行会聊天（ObjBase.pas:8678-8697）。
//
// 客户端用普通 CM_SAY 加 '!~' 前缀发送（ClMain.pas:2307-2310）。
// 广播给行会内全部在线成员——包括自己（原版 SendGuildMsg 遍历全部成员）。
func (s *Server) handleGuildChat(p *Player, text string) {
	ref, ok := s.guildOf(p)
	if !ok {
		return
	}
	s.sendGuildChat(ref.guild.Name, p.Char.Name+": "+text)
}

// sendGuildChat 把一条行会频道消息发给该行会全部在线成员
// （等价原版 TGUild.SendGuildMsg：遍历 GuildRankList 里的在线对象）。
func (s *Server) sendGuildChat(guildName, msg string) {
	param := uint16(s.social.guildCfg.chatFColor) | uint16(s.social.guildCfg.chatBColor)<<8
	for _, name := range s.social.guilds.Roster(guildName) {
		target := s.playerByName(name)
		// ⚠️ banGuildChat 的语义是"允许收行会聊天"（默认 true）；
		// 置 false 的人收不到（原版 ObjBase.pas:8690）。
		if target == nil || !target.banGuildChat {
			continue
		}
		s.send(target.conn, proto.SM_GUILDMESSAGE, 0, param, 0, 1, msg)
	}
}

// ---------- 显示同步 ----------

// sendChangeGuildName 下发"行会名/职务名"（ObjBase.pas:22841-22850）。
//
// 未入会时发空 body（客户端据此清掉头顶行会信息）。
//
// 占领方成员会带 "[城堡名]" 前缀（ObjBase.pas:25875-25901 的
// g_sCastleGuildName 格式），见 castleTitleOf。
func (s *Server) sendChangeGuildName(c net.Conn, p *Player) {
	ref, ok := s.guildOf(p)
	if !ok {
		s.send(c, proto.SM_CHANGEGUILDNAME, 0, 0, 0, 0, "")
		return
	}
	s.send(c, proto.SM_CHANGEGUILDNAME, 0, 0, 0, 0,
		s.castleTitleOf(p)+"/"+ref.rankName)
}

// ---------- 命令 ----------

// requestGuildWar 处理 @@guildwar：掌门向目标行会宣战。
//
// 原版语义（ObjBase.pas:26722-26761 ReQuestGuildWar）：
//   - 只有掌门（IsGuildMaster）能发起，且必须本服；
//   - 目标行会必须存在；
//   - **双向**写入：先给己方 AddWarGuild，对方失败时把 dwWarTick 置 0 回滚；
//   - 成功后只广播内部刷新消息 SS_207（跨服用，单机不需要）。
//
// ⚠️ 原版这里**不扣费**（费用在 NPC 脚本层），所以我们也不扣。
func (s *Server) requestGuildWar(c net.Conn, p *Player, target string) {
	target = strings.TrimSpace(target)
	ctx := guildCtx()

	ref, ok := s.guildOf(p)
	if !ok {
		s.sysMsg(c, "你还没有加入行会")
		return
	}
	if !ref.isChief() {
		s.sysMsg(c, "只有掌门才能发起行会战")
		return
	}
	if target == "" {
		s.sysMsg(c, "请指定对方行会名")
		return
	}
	if target == ref.guild.Name {
		s.sysMsg(c, "不能向自己的行会宣战")
		return
	}
	if s.social.guilds.Find(target) == nil {
		s.sysMsg(c, "行会「"+target+"」不存在")
		return
	}

	until := time.Now().Add(s.social.guildCfg.warDuration)
	if err := s.social.guilds.AddWar(ctx, ref.guild.Name, target, until); err != nil {
		s.sysMsg(c, "宣战失败: "+err.Error())
		return
	}
	s.notifyGuildWar(ref.guild.Name, target, until)
	s.sysMsg(c, fmt.Sprintf("已向 %s 宣战，战争持续 %s", target, s.social.guildCfg.warDuration))
}

// notifyGuildWar 把宣战消息告知双方的在线成员。
func (s *Server) notifyGuildWar(a, b string, until time.Time) {
	msg := fmt.Sprintf("行会 %s 与 %s 进入战争状态，持续至 %s", a, b,
		until.Format("15:04:05"))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pl := range s.world.players {
		if ref, ok := s.guildOf(pl); ok &&
			(ref.guild.Name == a || ref.guild.Name == b) {
			s.sysMsg(pl.conn, msg)
		}
	}
}

// cmdLetGuild @LetGuild：允许/禁止被别人加进行会（ObjBase.pas:7655-7658）。
func (s *Server) cmdLetGuild(c net.Conn, p *Player) {
	p.allowGuild = !p.allowGuild
	if p.allowGuild {
		s.sysMsg(c, "你已经允许加入行会")
	} else {
		s.sysMsg(c, "你已经禁止加入行会")
	}
}

// cmdEndGuild @EndGuild：自己退会（ObjBase.pas:12841-12863，要求非掌门）。
func (s *Server) cmdEndGuild(c net.Conn, p *Player) {
	ref, ok := s.guildOf(p)
	if !ok {
		return
	}
	if ref.isChief() {
		s.sysMsg(c, "掌门人不能退会，只能解散行会")
		return
	}
	if _, err := s.social.guilds.DelMember(guildCtx(), ref.guild.Name, p.Char.Name); err != nil {
		log.Printf("%s 退出行会失败: %v", p.Char.Name, err)
		return
	}
	s.sendChangeGuildName(c, p)
	s.sysMsg(c, fmt.Sprintf("你退出了行会 %s", ref.guild.Name))
	s.refreshGuildOnline(ref.guild.Name)
}

// cmdAuthAlly @AuthAlly：切换"允许被结盟"（ObjBase.pas:7667-7672）。
func (s *Server) cmdAuthAlly(c net.Conn, p *Player) {
	ref, ok := s.guildOf(p)
	if !ok || !ref.isChief() {
		return
	}
	v := !ref.guild.EnableAuthAlly
	if err := s.social.guilds.SetEnableAuthAlly(guildCtx(), ref.guild.Name, v); err != nil {
		log.Printf("切换 %s 结盟开关失败: %v", ref.guild.Name, err)
		return
	}
	if v {
		s.sysMsg(c, "你的行会现在允许被结盟")
	} else {
		s.sysMsg(c, "你的行会现在禁止被结盟")
	}
}

// cmdBanGuildChat @BanGuildChat：切换是否接收行会聊天（ObjBase.pas:7646-7652）。
func (s *Server) cmdBanGuildChat(c net.Conn, p *Player) {
	p.banGuildChat = !p.banGuildChat
	if p.banGuildChat {
		s.sysMsg(c, "你现在可以接收行会聊天")
	} else {
		s.sysMsg(c, "你现在无法接收行会聊天")
	}
}

// cmdAlly @联盟：双方掌门面对面时结盟（ObjBase.pas:18217-18268）。
//
// 失败原因码与原版逐条对齐（ObjBase.pas:18225-18259）。客户端按这些数值弹文案
// （ClMain.pas:4887-4894），错一位就是"真客户端提示错话"：
//
//	-1  您无此权限！                正对面没人/不是玩家/对方没行会/对方不朝向你
//	-2  结盟失败！                  正在行会战（IsNotWarGuild 为 false）
//	-3  行会结盟必须双方掌门人面对面！  有一方不是掌门
//	-4  对方行会掌门人不允许结盟！     对方行会没开"允许被结盟"（@AuthAlly）
//
// ⚠️ **重复结盟返回 0（成功）**：原版 `AllyGuild` 内部去重（Guild.pas:1191-1204）
// 但 ClientGuildAlly 丢弃了它的返回值，无脑 `n8 := 0`。别"顺手修正"成 -2。
func (s *Server) cmdAlly(c net.Conn, p *Player) {
	ref, selfOK := s.guildOf(p)

	// 正对面那格里的玩家（等价原版 GetPoseCreate；我们只在玩家里找，
	// 所以原版的 `m_btRaceServer = RC_PLAYOBJECT` 检查天然满足）。
	other := s.faceNeighbor(p)
	otherRef, otherOK := s.guildOf(other)

	code := allyFailNoPose
	if otherOK {
		switch {
		case !otherRef.guild.EnableAuthAlly:
			code = allyFailTargetRefuse
		case !otherRef.isChief() || !selfOK || !ref.isChief():
			code = allyFailNotChief
		case guild.InWar(ref.guild, otherRef.guild, time.Now()):
			code = allyFailAtWar
		default:
			if err := s.social.guilds.AddAlly(guildCtx(), ref.guild.Name, otherRef.guild.Name); err != nil {
				// 原版没有这一支（AllyGuild 的 false 只在"已结盟"时出现，
				// 而那也被原版当成功）。落库真失败时回 -2 —— 客户端文案是
				// 通用的"结盟失败！"，不会误导。
				log.Printf("结盟失败: %v", err)
				code = allyFailAtWar
			} else {
				code = 0
				s.announceAlly(ref.guild.Name, otherRef.guild.Name)
			}
		}
	}

	if code == 0 {
		s.send(c, proto.SM_GUILDMAKEALLY_OK, 0, 0, 0, 0, "")
		return
	}
	s.send(c, proto.SM_GUILDMAKEALLY_FAIL, int32(code), 0, 0, 0, "")
}

// announceAlly 把结盟结果告知双方行会（ObjBase.pas:18243-18248）。
//
// 两句文案的方向别弄反：**A 的成员收到"B 已与您联盟"**，B 的成员收到"A 已与您联盟"。
func (s *Server) announceAlly(a, b string) {
	s.sendGuildChat(a, b+"行会已经和您的行会联盟成功。")
	s.sendGuildChat(b, a+"行会已经和您的行会联盟成功。")
	// RefMemberName：刷新双方在线成员的头顶信息。
	s.refreshGuildOnline(a)
	s.refreshGuildOnline(b)
}

// faceNeighbor 返回 p 正前方一格上的玩家（等价原版 GetPoseCreate）。
func (s *Server) faceNeighbor(p *Player) *Player {
	d := entity.DirDelta[p.Obj.Facing()]
	fx, fy := p.Obj.PosX()+d[0], p.Obj.PosY()+d[1]
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.world.players {
		if o != p && o.Obj.MapRef() == p.Obj.MapRef() && o.Obj.PosX() == fx && o.Obj.PosY() == fy {
			return o
		}
	}
	return nil
}

// cmdBreakAlly @取消联盟 <名>：解除同盟（ObjBase.pas:18270-18302）。
//
// ⚠️ 原版两个怪癖必须保留：
//  1. 非掌门直接 Exit，**一个包都不发**（ObjBase.pas:18276）；
//  2. 失败时发的是 SM_GUILDMAKEALLY_FAIL(769) 且 **Recog=0**（不是原因码，
//     更不是 BREAKALLY_FAIL(771)）——后者从未被发送过（ObjBase.pas:18298）。
func (s *Server) cmdBreakAlly(c net.Conn, p *Player, args []string) {
	ref, ok := s.guildOf(p)
	if !ok || !ref.isChief() {
		return
	}
	if len(args) == 0 {
		s.sysMsg(c, "用法: @取消联盟 <行会名>")
		return
	}
	other := s.social.guilds.Find(args[0])
	switch {
	case other == nil:
		s.send(c, proto.SM_GUILDMAKEALLY_FAIL, 0, 0, 0, 0, "")
	case !guild.HasAlly(ref.guild, other.Name):
		s.send(c, proto.SM_GUILDMAKEALLY_FAIL, 0, 0, 0, 0, "")
	default:
		ok, err := s.social.guilds.BreakAlly(guildCtx(), ref.guild.Name, other.Name)
		if err != nil || !ok {
			log.Printf("解除 %s 与 %s 的同盟失败: %v", ref.guild.Name, other.Name, err)
			s.send(c, proto.SM_GUILDMAKEALLY_FAIL, 0, 0, 0, 0, "")
			return
		}
		s.send(c, proto.SM_GUILDBREAKALLY_OK, 0, 0, 0, 0, "")
		// 原版给双方行会各发一条解除公告（ObjBase.pas:18284-18285），
		// 措辞不同：己方是"与您解除"，对方是"解除了与您"。
		s.sendGuildChat(ref.guild.Name, other.Name+" 行会与您的行会解除联盟成功！！！")
		s.sendGuildChat(other.Name, ref.guild.Name+" 行会解除了与您行会的联盟！！！")
		s.refreshGuildOnline(ref.guild.Name)
		s.refreshGuildOnline(other.Name)
	}
}

// cmdGuild 是 @guild 管理命令（原版是 GM 命令 @AddGuild/@DelGuild 的超集，
// ObjBase.pas:12353-12367；测试与运维用）。
func (s *Server) cmdGuild(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @guild info|create|add|kick|notice|ally <参数>")
		return
	}
	ctx := guildCtx()
	switch strings.ToLower(args[0]) {
	case "info":
		ref, ok := s.guildOf(p)
		if !ok {
			s.sysMsg(c, "你还没有加入行会")
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "行会 %s：%d 人，职务 %d 个，公告 %d 行，敌对 %d，同盟 %d",
			ref.guild.Name, guild.Count(ref.guild), len(ref.guild.Ranks),
			len(ref.guild.Notice), len(ref.guild.Wars), len(ref.guild.Allies))
		s.sysMsg(c, b.String())
	case "create":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @guild create <行会名>")
			return
		}
		if err := s.guildAdminCreate(ctx, p, args[1]); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, fmt.Sprintf("行会 %s 已创建（GM）", args[1]))
	case "add":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @guild add <角色名>")
			return
		}
		ref, ok := s.guildOf(p)
		if !ok {
			s.sysMsg(c, "你还没有加入行会")
			return
		}
		if err := s.social.guilds.AddMember(ctx, ref.guild.Name, args[1]); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.refreshGuildOnline(ref.guild.Name)
		s.sendGuildMemberList(c, p)
		s.sysMsg(c, fmt.Sprintf("%s 已加入 %s（GM）", args[1], ref.guild.Name))
	case "kick":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @guild kick <角色名>")
			return
		}
		ref, ok := s.guildOf(p)
		if !ok {
			s.sysMsg(c, "你还没有加入行会")
			return
		}
		removed, err := s.social.guilds.DelMember(ctx, ref.guild.Name, args[1])
		if err != nil || !removed {
			s.sysMsg(c, "该角色不在本行会")
			return
		}
		if target := s.playerByName(args[1]); target != nil {
			s.sendChangeGuildName(target.conn, target)
		}
		s.sendGuildMemberList(c, p)
		s.sysMsg(c, fmt.Sprintf("%s 已被移出（GM）", args[1]))
	case "notice":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @guild notice <内容>")
			return
		}
		ref, ok := s.guildOf(p)
		if !ok {
			s.sysMsg(c, "你还没有加入行会")
			return
		}
		if err := s.social.guilds.SetNotice(ctx, ref.guild.Name, []string{strings.Join(args[1:], " ")}); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, "公告已更新（GM）")
	case "ally":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @guild ally <行会名>")
			return
		}
		ref, ok := s.guildOf(p)
		if !ok {
			s.sysMsg(c, "你还没有加入行会")
			return
		}
		if s.social.guilds.Find(args[1]) == nil {
			s.sysMsg(c, "目标行会不存在")
			return
		}
		if err := s.social.guilds.AddAlly(ctx, ref.guild.Name, args[1]); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, fmt.Sprintf("已与 %s 结盟（GM）", args[1]))
	default:
		s.sysMsg(c, "用法: @guild info|create|add|kick|notice|ally <参数>")
	}
}

// guildAdminCreate 是 @guild create 的实现：GM 建会不校验金币与号角，
// 但名字合法性/重名/已入会仍然校验（与原版 @AddGuild 一致）。
func (s *Server) guildAdminCreate(ctx context.Context, p *Player, name string) error {
	if !validGuildName(name) {
		return fmt.Errorf("行会名不合法（≤%d 字，不含 / \\ : * 空格 \" ' < | ? >）", maxGuildNameRunes)
	}
	_, err := s.social.guilds.Create(ctx, name, p.Char.Name)
	return err
}

// findBagItemByName 在背包里找物品（返回槽位，-1 = 没有）。
// 与 entity 的堆叠约定一致：可堆叠物品按 Dura 计数，但建会只消耗一个格子。
func (s *Server) findBagItemByName(p *Player, name string) int {
	it := s.data.tables.Items.GetByName(name)
	if it == nil || p.Char.Data == nil {
		return -1
	}
	for i, ui := range p.bagSnapshot() {
		if ui != nil && ui.Index == uint32(it.Index) {
			return i
		}
	}
	return -1
}

package gamesvr

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/world"
)

// 聊天系统（普通 / 喊话 / 私聊 / 组队频道）。
//
// 原版分两层（别混）：
//
//	TPlayObject.ProcessUserLineMsg（ObjBase.pas:7188）—— CM_SAY 入口。
//	  `@` 开头的走 GM 命令表，其余原样交给 ProcessSayMsg。
//	  ⇒ **@ 命令不经过聊天限流**（这点很关键：e2e 里满屏都是 @ 命令，
//	    把限流放到上一层会让整轮回归被"禁言"打挂）。
//	TPlayObject.ProcessSayMsg（ObjBase.pas:8601）—— 频道前缀分派 + 反刷屏限流。
//
// 频道前缀（`sData[1]`）：
//
//	'/'    私聊 `/名字 内容`（另有 `/WHO`、`/TOTAL` 两个查询，我们没做在线统计）
//	'!!'   组队频道 → SendGroupText（走 SM_SYSMESSAGE，见 group.go 的注释）
//	'!~'   行会频道（见 guild.go 的 handleGuildChat）
//	'!'    喊话（等级 > CanShoutMsgLevel、10 秒一次、地图带 QUIZ 则不允许）
//	其余   普通聊天 → **视野内**广播
//
// ⚠️ 三个最容易写错的点：
//
//  1. **喊话落到客户端的是 SM_HEAR，不是 SM_CRY**。原版 RM_CRY→SM_* 的转换表里
//     写的是 `RM_CRY: m_DefMsg := MakeDefaultMsg(SM_HEAR, …)`（ObjBase.pas:5647）,
//     两者只差颜色（HearMsg 0/255、CryMsg 0/151）。发 SM_CRY 客户端不认。
//  2. `Param` 是 `MakeWord(FColor, BColor)`（**前景色在低字节**）。客户端
//     `AddChatBoardString(DecodeString(body), GetRGB(LoByte(Param)), GetRGB(HiByte(Param)))`
//     （ClMain.pas:4339），高低写反会变成另一种配色。
//  3. 私聊**不给发送者回显**（原版只发给目标；客户端自己本地显示）。
//     body 是 `发送者=> 内容`，颜色分普通（252/255）与 GM 发送（255/56）两套。

// 聊天配置：逐条取自 data/envir/!setup.txt（原版读的就是它）。
const (
	// chatSayMaxLen 对应 SayMsgMaxLen=80。
	chatSayMaxLen = 80
	// 反刷屏：SayMsgTime=3000 / SayMsgCount=2 / DisableSayMsgTime=60000。
	//
	// ⚠️ 原版这段的"内容相同"判断是被注释掉的（ObjBase.pas:8614），
	// 实际语义是"3 秒内发到第 3 条就禁言 60 秒"，与内容无关。照抄。
	chatSayRepeatWin = 3 * time.Second
	chatSayRepeatMax = 2
	chatSayBanDur    = 60 * time.Second

	// 喊话：CanShoutMsgLevel=7（要**大于** 7 级）、10 秒冷却、半径 50 格。
	chatShoutMinLevel = 7
	chatShoutInterval = 10 * time.Second
	chatShoutRange    = 50

	// 颜色：MakeWord(F, B)。HearMsg=0/255、CryMsg=0/151、
	// WhisperMsg=252/255、GMWhisperMsg=255/56。
	chatColorHear      uint16 = 0 | 255<<8
	chatColorCry       uint16 = 0 | 151<<8
	chatColorWhisper   uint16 = 252 | 255<<8
	chatColorGMWhisper uint16 = 255 | 56<<8
)

// chatSpamVerdict 是反刷屏的判定结果。
type chatSpamVerdict int

const (
	// chatAllow 放行。
	chatAllow chatSpamVerdict = iota
	// chatDrop：发言被静默丢弃（原版在禁言期内整段跳过，**不发提示**）。
	chatDrop
	// chatBanNow：这一条触发了禁言，要发一次提示。
	chatBanNow
)

// chatSpamStep 是反刷屏状态机（纯函数，便于单测）。
//
// 返回：判定 + 新的消息计数 + 新的"禁言到"时刻。
// last 是上次"窗口内首条"的时刻，repeat 是窗口内已计数。
//
// win / banDur 由调用方传入（而不是直接用常量）：服务端要按倍速缩放它们
// （见 handleChat），单测想用真实值就直接传常量。
func chatSpamStep(last time.Time, repeat int, banUntil, now time.Time,
	win, banDur time.Duration) (chatSpamVerdict, int, time.Time) {
	if banUntil.After(now) {
		return chatDrop, repeat, banUntil
	}
	if now.Sub(last) < win {
		repeat++
		if repeat >= chatSayRepeatMax {
			return chatBanNow, repeat, now.Add(banDur)
		}
		return chatAllow, repeat, banUntil
	}
	// 出了窗口：重新开始计数（原版同时把 tick 刷新到现在）。
	return chatAllow, 0, banUntil
}

// handleChat 处理普通 CM_SAY（既不是 `@` 也不是 `!~`）。
func (s *Server) handleChat(c net.Conn, p *Player, raw string) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	// 长度截断（原版 `Copy(sData, 1, SayMsgMaxLen)`）。
	// 按 **rune** 截而不是 byte：劈开一个 UTF-8 汉字会让客户端显示乱码。
	text := truncateRunes(raw, chatSayMaxLen)
	if text == "" {
		return
	}

	// 反刷屏的窗口/禁言时长随倍速缩放（与服务端其它限流一致：移动、施法冷却、
	// 建角色限流都缩）。否则 TIME_SCALE=20 下"3 秒窗口"仍是 3 秒墙钟，
	// e2e 里连发两条聊天就会被禁言 60 秒——回归必然挂。
	win, banDur := tscale.D(chatSayRepeatWin), tscale.D(chatSayBanDur)
	switch verdict, count, banUntil := chatSpamStep(p.sayLast, p.sayRepeat, p.sayBanUntil, time.Now(), win, banDur); verdict {
	case chatDrop:
		return
	case chatBanNow:
		p.sayRepeat, p.sayBanUntil = count, banUntil
		s.sysMsg(c, fmt.Sprintf("由于你重复发相同的内容，%d分钟内你将被禁止发言...",
			int(chatSayBanDur.Minutes())))
		return
	default:
		if count == 0 {
			p.sayLast = time.Now()
		}
		p.sayRepeat = count
	}

	switch {
	case len(text) >= 1 && text[0] == '/':
		s.handleWhisper(p, text[1:])
	case len(text) >= 2 && text[:2] == "!!":
		// 组队频道（ObjBase.pas:8684-8687 `SendGroupText(m_sCharName + ': ' + SC)`）。
		// 没组队就什么也不做（SendGroupText 遍历成员为空）。
		s.sendGroupText(p.Obj.ID, "%s: %s", p.Char.Name, strings.TrimSpace(text[2:]))
	case len(text) >= 1 && text[0] == '!':
		s.handleShout(c, p, text[1:])
	default:
		s.broadcastHear(p, p.Char.Name+":"+text, chatColorHear)
	}
}

// broadcastHear 把一条聊天发给**视野内**的所有人。
//
// 原版路径：ProcessSayMsg → SendRefMsg(RM_HEAR, 0, FColor, BColor, 0, "名字:文本")
// → 地图上能看见说话者的对象都收到（`SendRefMsg` 发给自己的视野对象）。
//
// ⚠️ **包含发送者自己**：客户端发完 CM_SAY 不会本地回显，
// 它自己的聊天框也靠这条包（漏掉自己会表现为"我说话我自己看不见"）。
func (s *Server) broadcastHear(p *Player, text string, color uint16) {
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(o *Player) {
		s.send(o.conn, proto.SM_HEAR, int32(p.Obj.ID), color, 0, 1, text)
	})
}

// handleShout 喊话（`!内容`）。
//
// 原版 ObjBase.pas:8698-8728 的三道门，逐条照抄：
//
//	not m_PEnvir.Flag.boQUIZ                     地图带 QUIZ ⇒ "本地图不允许喊话！！！"
//	m_Abil.Level > g_Config.nCanShoutMsgLevel    等级要**大于** 7
//	GetTickCount - m_dwShoutMsgTick > 10*1000    10 秒一次
//
// 广播半径是 **50 格**（`CryCry(RM_CRY, …, 50, …)`），不是视野的 12 格；
// 落包仍是 SM_HEAR，只是颜色换 CryMsg（见文件头注释）。
func (s *Server) handleShout(c net.Conn, p *Player, raw string) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return
	}
	if z := s.zoneOf(p.Obj.MapRef()); z != nil && z.Quiz {
		s.sysMsg(c, "本地图不允许喊话！！！")
		return
	}
	if int(p.level()) <= chatShoutMinLevel {
		s.sysMsg(c, fmt.Sprintf("你的等级要在%d级以上才能用此功能！！！", chatShoutMinLevel+1))
		return
	}
	now := time.Now()
	// 冷却按倍速缩放（与服务端其它限流一致），排查疑难时 TIME_SCALE=1 即真实时序。
	if now.Sub(p.shoutLast) < tscale.D(chatShoutInterval) {
		s.sysMsg(c, fmt.Sprintf("你必须在 %d 秒后使用此功能！！！", int(chatShoutInterval.Seconds())))
		return
	}
	p.shoutLast = now

	msg := "(!)" + p.Char.Name + ": " + text
	s.broadcastInRange(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), chatShoutRange, func(o *Player) {
		s.send(o.conn, proto.SM_HEAR, int32(p.Obj.ID), chatColorCry, 0, 1, msg)
	})
}

// handleWhisper 私聊（`/名字 内容`），对应原版 TPlayObject.Whisper（ObjBase.pas:2168）。
//
// 原版顺序：目标必须在线且 `m_boReadyRun`；目标没拒绝私聊
// （m_boHearWhisper 出厂 True，且发送者不在目标的拉黑名单里）；
// 否则回 "名字  无法发送信息." / "名字  拒绝私聊！！！" / "名字  没有在线！！！"。
//
// ⚠️ Recog 是**接收者**（原版 `PlayObject.SendMsg(PlayObject, RM_WHISPER, …)`，
// 第一个参数就是目标自己），与我们其它聊天包（Recog = 说话者）不同。
func (s *Server) handleWhisper(p *Player, rest string) {
	text, who := cutAt(strings.TrimSpace(rest), " ")
	text = strings.TrimSpace(text)
	if who == "" || text == "" {
		return
	}
	target := s.playerByName(who)
	if target == nil {
		s.sysMsg(p.conn, who+"  没有在线！！！")
		return
	}
	// 我们还没做"拒绝私聊"开关（原版 @拒绝私聊 / 拉黑名单），恒为接受。
	color := chatColorWhisper
	if p.permission > 0 {
		color = chatColorGMWhisper
	}
	s.send(target.conn, proto.SM_WHISPER, int32(target.Obj.ID), color, 0, 1,
		p.Char.Name+"=> "+text)
}

// broadcastInRange 对同图内与 (x,y) 距离 ≤ r 的玩家执行 fn。
//
// 与 broadcastToViewers 的区别：那个按**视野半径**（12）筛，
// 喊话要的是 50 格（原版 CryCry 的 nRange）。
func (s *Server) broadcastInRange(m *world.Map, x, y, r int, fn func(*Player)) {
	s.mu.RLock()
	list := s.world.index.InRange(x, y, r)
	s.mu.RUnlock()
	for _, o := range list {
		o2, ok := o.(*Player)
		if !ok || o2.Obj.MapRef() != m {
			continue
		}
		if o2.Obj.Distance(x, y) > r {
			continue
		}
		fn(o2)
	}
}

// truncateRunes 按字符数截断（不劈开多字节字符）。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	cnt := 0
	for i := range s {
		if cnt == n {
			return s[:i]
		}
		cnt++
	}
	return s
}

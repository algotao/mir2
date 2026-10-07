package gamesvr

// NPC 脚本的延时跳转（引擎里最后一块有架构含量的能力）。
//
// 对应原版三条命令：
//   TIMERECALL   <秒> <标签>  sTIMERECALL   延时跳到本页另一段
//   DELAYGOTO    <秒> <标签>  sDELAYGOTO   延时跳转（可带页码）
//   BREAKTIMERECALL            sBREAKTIMERECALL  清掉本角色的全部延时
//
// 另外还有 TIMERECALLGROUP（全组延时，OpenMir2 ConditionCode 里有），
// 我们没有组队系统，跳过。
//
// # 为什么要"队列 + 上下文快照"
//
// 延时到点时，玩家可能已经：换了地图、关了对话、甚至下线了。
// 原版的做法是把延时消息塞进玩家的消息队列（SendDelayMsg），
// 到点后在玩家自己的 Run 里处理，于是天然带着"当前上下文"。
//
// 我们没有 per-player 消息队列，所以**在注册时就快照下必要上下文**
// （脚本名 + 目标标签），到点再用玩家**当前**的连接执行。
// 这样玩家中途换地图/关对话都不会让延时跳转失效或串台。

import (
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/obs"
)

// scriptTimer 是一条待执行的延时跳转。
type scriptTimer struct {
	// charName 是目标角色名（用名字而不是指针：玩家可能已下线）。
	charName string
	// scriptName 是脚本名（快照）。
	scriptName string
	// label 是目标标签（快照）。
	label string
	// at 是执行时刻。
	at time.Time
	// page 是 DELAYGOTO 的可选"页码"（同一脚本内的段选择）。
	// 目前只支持空或与 label 同名——原版的"页"是 QFunction 里的概念，
	// 我们用 label 表达等价语义。
	page string
}

// scriptTimerQueue 是全服共用的延时队列。
//
// 为什么不用 per-player：延时条目少（脚本里也就几条），
// 全服一个队列 + 按角色名索引更简单，且天然支持"玩家下线后丢弃"。
type scriptTimerQueue struct {
	mu     sync.Mutex
	timers []*scriptTimer
}

// add 追加一条延时。
func (q *scriptTimerQueue) add(t *scriptTimer) {
	q.mu.Lock()
	q.timers = append(q.timers, t)
	q.mu.Unlock()
}

// removeByChar 清掉某角色的全部延时（BREAKTIMERECALL）。
func (q *scriptTimerQueue) removeByChar(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.timers[:0]
	n := 0
	for _, t := range q.timers {
		if t.charName == name {
			n++
			continue
		}
		kept = append(kept, t)
	}
	q.timers = kept
	return n
}

// due 返回到点且仍在线的条目，并把它们从队列摘掉。
func (q *scriptTimerQueue) due(now time.Time) []*scriptTimer {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []*scriptTimer
	kept := q.timers[:0]
	for _, t := range q.timers {
		if now.Before(t.at) {
			kept = append(kept, t)
			continue
		}
		out = append(out, t)
	}
	q.timers = kept
	return out
}

// len 返回队列长度（测试与诊断用）。
func (q *scriptTimerQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.timers)
}

// actTimerRecall 实现 TIMERECALL / DELAYGOTO <秒> <标签>。
//
// 两个命令在原版是同一条实现的不同入口，这里合并处理。
// 秒数支持小数（"0.5"），因为官方脚本里有写小数的。
func (s *Server) actTimerRecall(c net.Conn, p *Player, args []string, isDelayGoto bool) {
	if len(args) < 2 {
		return
	}
	// 秒数可能是 "3" 或 "0.5" 或带 s 后缀
	secStr := strings.TrimSuffix(strings.ToLower(args[0]), "s")
	sec, err := strconv.ParseFloat(secStr, 64)
	if err != nil || sec < 0 {
		s.sysMsg(c, "延时秒数不合法: "+args[0])
		return
	}
	label := args[1]
	if p.dialog == nil || p.dialog.scriptName == "" {
		s.sysMsg(c, "当前不在 NPC 对话中，无法延时跳转")
		return
	}
	// 目标标签必须存在，否则到点才报错没意义——原版 GotoLable 也是静默失败，
	// 但我们提前查一次能给出即时反馈。
	sc := s.scriptByName(p.dialog.scriptName)
	if sc == nil || sc.Label(label) == nil {
		s.sysMsg(c, "脚本里没有段 @"+label)
		return
	}
	d := time.Duration(sec * float64(time.Second))
	s.npc.timers.add(&scriptTimer{
		charName:   p.Char.Name,
		scriptName: p.dialog.scriptName,
		label:      label,
		page:       pageOf(args, 2),
		at:         time.Now().Add(d),
	})
	obs.Event("script_timer_set", "player", p.Char.Name, "label", label,
		"seconds", sec, "delay_goto", isDelayGoto)
	log.Printf("%s 延时 %.1fs 跳到 @%s（脚本 %s）", p.Char.Name, sec, label, p.dialog.scriptName)
	s.sysMsg(c, "已安排 "+strconv.FormatFloat(sec, 'f', -1, 64)+" 秒后跳转")
}

// pageOf 取可选的页码参数（DELAYGOTO 的第三段）。
func pageOf(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return ""
}

// actBreakTimerRecall 实现 BREAKTIMERECALL：清掉本角色的全部延时。
func (s *Server) actBreakTimerRecall(c net.Conn, p *Player) {
	n := s.npc.timers.removeByChar(p.Char.Name)
	s.sysMsg(c, "已取消 "+strconv.Itoa(n)+" 个延时跳转")
}

// scriptTimerLoop 每秒检查到点的延时并执行。
func (s *Server) scriptTimerLoop() {
	t := time.NewTicker(tickDur(time.Second))
	defer t.Stop()
	for now := range t.C {
		s.runScriptTimers(now)
	}
}

// runScriptTimers 执行到点的延时条目。
func (s *Server) runScriptTimers(now time.Time) {
	due := s.npc.timers.due(now)
	for _, t := range due {
		p := s.playerByName(t.charName)
		if p == nil {
			// 玩家已下线：丢弃（原版的延时消息也会随玩家消失）。
			log.Printf("延时跳转丢弃：%s 已下线", t.charName)
			continue
		}
		sc := s.scriptByName(t.scriptName)
		if sc == nil {
			continue
		}
		next := sc.Label(t.label)
		if next == nil {
			log.Printf("延时跳转失败：脚本 %s 里没有段 @%s", t.scriptName, t.label)
			continue
		}
		s.showLabel(p.conn, p, sc, next)
		obs.Event("script_timer_fire", "player", t.charName, "label", t.label)
	}
}

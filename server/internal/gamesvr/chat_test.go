package gamesvr

import (
	"testing"
	"time"
)

// TestChatSpamStep 守住反刷屏状态机（ObjBase.pas:8614-8631）。
//
// ⚠️ 原版那段里"内容相同才计数"的判断是**被注释掉的**（`{(sData = m_sOldSayMsg) and}`），
// 实际语义是"3 秒内发到第 3 条就禁言 60 秒"，与内容无关。
// 顺手加一句"内容相同才禁言"会让刷屏检测形同虚设。
func TestChatSpamStep(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	var last time.Time
	var banUntil time.Time
	repeat := 0

	step := func(now time.Time) chatSpamVerdict {
		var v chatSpamVerdict
		v, repeat, banUntil = chatSpamStep(last, repeat, banUntil, now, chatSayRepeatWin, chatSayBanDur)
		if v == chatAllow && repeat == 0 {
			last = now // 与 handleChat 一致：出窗口才刷新窗口起点
		}
		return v
	}

	// 第 1 条：出窗口 ⇒ 放行、计数归零。
	if v := step(t0); v != chatAllow || repeat != 0 {
		t.Fatalf("第 1 条：verdict=%v count=%d，期望 allow/0", v, repeat)
	}
	// 第 2 条（窗口内）：放行，计数 1。
	if v := step(t0.Add(time.Second)); v != chatAllow || repeat != 1 {
		t.Fatalf("第 2 条：verdict=%v count=%d，期望 allow/1", v, repeat)
	}
	// 第 3 条（窗口内）：触发禁言。
	if v := step(t0.Add(2 * time.Second)); v != chatBanNow {
		t.Fatalf("第 3 条：verdict=%v，期望 ban", v)
	}
	if d := banUntil.Sub(t0.Add(2 * time.Second)); d != chatSayBanDur {
		t.Errorf("禁言时长 = %v，期望 %v", d, chatSayBanDur)
	}
	// 禁言期内：**静默丢弃**（原版整段跳过，不发提示——提示只在触发时发一次）。
	if v := step(t0.Add(10 * time.Second)); v != chatDrop {
		t.Fatalf("禁言期内：verdict=%v，期望 drop", v)
	}
	// 禁言结束：恢复放行。
	if v := step(t0.Add(2*time.Second + chatSayBanDur + time.Millisecond)); v != chatAllow {
		t.Fatalf("禁言结束后：verdict=%v，期望 allow", v)
	}
}

// TestChatSpamWindowRestarts 窗口过期后重新计数（否则每分钟只能发两条）。
func TestChatSpamWindowRestarts(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	last := t0
	if v, _, _ := chatSpamStep(last, 1, time.Time{}, t0.Add(chatSayRepeatWin+time.Millisecond), chatSayRepeatWin, chatSayBanDur); v != chatAllow {
		t.Errorf("窗口已过：verdict=%v，期望 allow", v)
	}
}

// TestTruncateRunes 按字符截断（SayMsgMaxLen=80）。
//
// ⚠️ 必须按 **rune** 截：按 byte 截会把一个汉字劈成两半，
// 客户端 DecodeString 出来就是乱码。
func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abc", 10, "abc"},
		{"", 5, ""},
		{"abc", 0, ""},
		// 5 个汉字、截 2 个 ⇒ 必须正好 2 个字符（6 字节），不是 2 字节。
		{"一二三四五", 2, "一二"},
		{"一a二b", 3, "一a二"},
	}
	for _, c := range cases {
		if got := truncateRunes(c.in, c.n); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q，期望 %q", c.in, c.n, got, c.want)
		}
	}
	// 边界：截断结果必须是合法 UTF-8（劈开汉字会不合法）。
	got := truncateRunes("一二三四五", 3)
	if !isValidUTF8(got) {
		t.Errorf("截断结果 %q 不是合法 UTF-8", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

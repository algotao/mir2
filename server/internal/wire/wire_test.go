package wire

import (
	"bytes"
	"errors"
	"testing"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/proto"
)

// TestFrameAsymmetry 固化"上行带序号、下行不带"这一不对称设计。
func TestFrameAsymmetry(t *testing.T) {
	payload := []byte("PAYLOAD")

	up := EncodeUp('3', payload)
	if !bytes.Equal(up, []byte("#3PAYLOAD!")) {
		t.Fatalf("上行帧 = %q, want %q", up, "#3PAYLOAD!")
	}
	f, err := DecodeFrame(up)
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != '3' || !f.IsFromClient() {
		t.Errorf("上行帧 Seq=%q isClient=%v", f.Seq, f.IsFromClient())
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("上行负载 = %q", f.Payload)
	}

	down := EncodeDown(payload)
	if !bytes.Equal(down, []byte("#PAYLOAD!")) {
		t.Fatalf("下行帧 = %q, want %q", down, "#PAYLOAD!")
	}
	f2, err := DecodeFrame(down)
	if err != nil {
		t.Fatal(err)
	}
	if f2.Seq != 0 || f2.IsFromClient() {
		t.Errorf("下行帧 Seq=%q isClient=%v", f2.Seq, f2.IsFromClient())
	}
	if !bytes.Equal(f2.Payload, payload) {
		t.Errorf("下行负载 = %q", f2.Payload)
	}
}

// TestFrameSeqBoundary 校验序号判定不会误伤负载首字符。
//
// 关键前提：6bit 编码输出范围是 [0x3C, 0x7B]（'<','=','>','?','@', A-Z, [\]^_`, a-z, '{'），
// **不含数字 '0'-'9' (0x30-0x39)**，因此下行帧的负载首字符永远不会被误判成序号。
func TestFrameSeqBoundary(t *testing.T) {
	for b := 0; b < 256; b++ {
		enc := codec.Encode6BitBuf([]byte{byte(b)})
		for _, c := range enc {
			if c >= '0' && c <= '9' {
				t.Fatalf("6bit 输出含数字字符 %q，序号判定会失效", c)
			}
		}
	}
	// 带外指令首字符也都不是数字
	for _, cmd := range [][]byte{KeepAlive, DigCmd, SelGateSwitch, RunGateSwitch} {
		if cmd[0] >= '0' && cmd[0] <= '9' {
			t.Errorf("带外指令 %q 首字符是数字", cmd)
		}
	}
}

func TestDecodeFrameErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"缺少起始符", "abc!"},
		{"缺少结束符", "#abc"},
		{"空", ""},
		{"只有定界符", "#"},
	}
	for _, c := range cases {
		if _, err := DecodeFrame([]byte(c.in)); !errors.Is(err, ErrBadFrame) {
			t.Errorf("%s: err = %v, want ErrBadFrame", c.name, err)
		}
	}
}

// TestSplitter 校验 TCP 流式拆包：半包、粘包、前置垃圾。
func TestSplitter(t *testing.T) {
	s := NewSplitter(1024)

	// 半包 + 粘包混合
	s.Feed([]byte("#1AAA"))
	if _, err := s.Next(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("半包应返回 ErrShortFrame, got %v", err)
	}
	s.Feed([]byte("!#2BBB!#3CC"))
	f1, err := s.Next()
	if err != nil || string(f1) != "#1AAA!" {
		t.Fatalf("帧1 = %q, %v", f1, err)
	}
	f2, err := s.Next()
	if err != nil || string(f2) != "#2BBB!" {
		t.Fatalf("帧2 = %q, %v", f2, err)
	}
	if _, err := s.Next(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("帧3 未完整, got %v", err)
	}
	s.Feed([]byte("C!"))
	f3, _ := s.Next()
	if string(f3) != "#3CCC!" {
		t.Fatalf("帧3 = %q", f3)
	}
}

// TestSplitterDropsGarbage 校验前置垃圾数据被丢弃而非卡死。
func TestSplitterDropsGarbage(t *testing.T) {
	s := NewSplitter(1024)
	s.Feed([]byte("garbage-no-delimiter"))
	if _, err := s.Next(); !errors.Is(err, ErrShortFrame) {
		t.Fatalf("应返回 ErrShortFrame, got %v", err)
	}
	if s.Buffered() != 0 {
		t.Errorf("垃圾未清空, buffered=%d", s.Buffered())
	}

	s.Feed([]byte("junk#OK!"))
	got, err := s.Next()
	if err != nil || string(got) != "#OK!" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// TestSplitterOverflow 校验超长无定界符数据被判为损坏而非无限缓冲。
func TestSplitterOverflow(t *testing.T) {
	s := NewSplitter(16)
	s.Feed(bytes.Repeat([]byte("x"), 64))
	if _, err := s.Next(); !errors.Is(err, ErrBadFrame) {
		t.Fatalf("超长应返回 ErrBadFrame, got %v", err)
	}
	if s.Buffered() != 0 {
		t.Errorf("超限未清空, buffered=%d", s.Buffered())
	}
}

// TestPacketRoundTrip 校验消息包编解码往返（含中文正文）。
func TestPacketRoundTrip(t *testing.T) {
	want := Packet{
		Head: proto.MakeDefaultMsg(proto.CM_IDPASSWORD, -7, 300, 200, 1),
		Body: "algotao/secret",
	}
	raw := want.Encode()
	if len(raw) < proto.DEFBLOCKSIZE {
		t.Fatalf("编码过短: %d", len(raw))
	}
	// 头部 16 字符必须是 6bit 可打印
	for _, c := range raw[:proto.DEFBLOCKSIZE] {
		if c < codec.Base6Offset || c > codec.Base6Offset+0x3F {
			t.Fatalf("头部含非法字符 %q", c)
		}
	}
	got, err := DecodePacket(raw)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	if got.Head != want.Head || got.Body != want.Body {
		t.Errorf("往返不一致:\n got=%+v %q\nwant=%+v %q", got.Head, got.Body, want.Head, want.Body)
	}
	if got.Ident() != proto.CM_IDPASSWORD {
		t.Errorf("Ident = %d", got.Ident())
	}
}

// TestPacketNoBody 校验无正文的包（绝大多数动作类消息）。
func TestPacketNoBody(t *testing.T) {
	p := Packet{Head: proto.MakeDefaultMsg(proto.SM_LOGON, 0, 0, 0, 0)}
	raw := p.Encode()
	if len(raw) != proto.DEFBLOCKSIZE {
		t.Fatalf("无正文编码长度 = %d, want %d", len(raw), proto.DEFBLOCKSIZE)
	}
	got, err := DecodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "" {
		t.Errorf("Body 应为空, got %q", got.Body)
	}
}

// TestPacketErrors 校验异常负载不会 panic。
func TestPacketErrors(t *testing.T) {
	if _, err := DecodePacket([]byte("short")); !errors.Is(err, ErrShortPacket) {
		t.Errorf("短负载 = %v, want ErrShortPacket", err)
	}
	// 含低于 0x3C 的字符 → 6bit 解码判整包无效
	bad := make([]byte, proto.DEFBLOCKSIZE)
	for i := range bad {
		bad[i] = codec.Base6Offset
	}
	bad[2] = codec.Base6Offset - 1
	if _, err := DecodePacket(bad); !errors.Is(err, ErrBadEncoding) {
		t.Errorf("非法编码 = %v, want ErrBadEncoding", err)
	}
}

func TestKeepAlive(t *testing.T) {
	if !IsKeepAlive([]byte("*")) {
		t.Error("'*' 应识别为心跳")
	}
	if IsKeepAlive([]byte("**")) || IsKeepAlive([]byte("x")) {
		t.Error("非心跳不应误判")
	}
}

func TestGateSwitch(t *testing.T) {
	got := FormatGateSwitch(SelGateSwitch, "127.0.0.1", 7100)
	if string(got) != "$S127.0.0.1/7100%" {
		t.Fatalf("FormatGateSwitch = %q", got)
	}
	addr, port, ok := ParseGateSwitch(got)
	if !ok || addr != "127.0.0.1" || port != 7100 {
		t.Fatalf("ParseGateSwitch = %q,%d,%v", addr, port, ok)
	}

	// $R 与 $C 同样支持
	for _, p := range [][]byte{RunGateSwitch, RunGateSwitchAlt} {
		raw := FormatGateSwitch(p, "192.168.1.9", 7200)
		if _, _, ok := ParseGateSwitch(raw); !ok {
			t.Errorf("%q 解析失败", p)
		}
	}

	// 非法输入
	bad := []string{"$S", "$S127.0.0.1%", "$S127.0.0.1/", "$X1/2%", "$S1/2", "$S1/abc%"}
	for _, s := range bad {
		if _, _, ok := ParseGateSwitch([]byte(s)); ok {
			t.Errorf("%q 不应解析成功", s)
		}
	}
}

// TestLoginToken 校验进入游戏的认证首包。
//
// 关键细节：** 前缀、6bit 编码、以及 sessionID 必须 >= 2。
func TestLoginToken(t *testing.T) {
	// 正常
	payload := codec.Encode6BitBuf([]byte("**tester/勇士/2/120040918/0"))
	tok, err := ParseLoginToken(payload)
	if err != nil {
		t.Fatalf("ParseLoginToken: %v", err)
	}
	if tok.Account != "tester" || tok.ChrName != "勇士" ||
		tok.SessionID != 2 || tok.ClientVersion != 120040918 || tok.Idx != "0" {
		t.Errorf("解析结果 = %+v", tok)
	}

	bad := []string{
		"tester/勇士/2/1/0",   // 缺 ** 前缀
		"**tester/勇士/1/1/0", // sessionID < 2
		"**tester/勇士/2",     // 字段不足
		"**/勇士/2/1/0",       // 账号为空
		"**tester//2/1/0",   // 角色名为空
		"**tester/勇士/x/1/0", // sessionID 非数字
	}
	for _, s := range bad {
		if _, err := ParseLoginToken(codec.Encode6BitBuf([]byte(s))); !errors.Is(err, ErrBadLoginToken) {
			t.Errorf("%q 应返回 ErrBadLoginToken, got %v", s, err)
		}
	}
}

// TestEndToEndFrame 组合帧+包，模拟一次真实的客户端请求。
func TestEndToEndFrame(t *testing.T) {
	p := Packet{
		Head: proto.MakeDefaultMsg(proto.CM_IDPASSWORD, 0, 0, 0, 0),
		Body: "algotao/secret",
	}
	line := EncodeUp('1', p.Encode())

	s := NewSplitter(4096)
	s.Feed(line)
	raw, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	f, err := DecodeFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != '1' {
		t.Errorf("Seq = %q", f.Seq)
	}
	got, err := DecodePacket(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got.Head.Ident != proto.CM_IDPASSWORD || got.Body != "algotao/secret" {
		t.Errorf("解包不符: %+v %q", got.Head, got.Body)
	}
}

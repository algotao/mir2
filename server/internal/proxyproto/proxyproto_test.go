package proxyproto

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// roundTrip 走一遍"网关写 → 后端读"，返回读出的源地址与该行之后的字节。
func roundTrip(t *testing.T, src, dst net.Addr) (net.TCPAddr, string) {
	t.Helper()
	var sb strings.Builder
	if err := WriteV1(&sb, src, dst); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	// 头之后紧跟一个字节的"协议数据"：读头不能把它吃掉。
	sb.WriteString("X")
	got := sb.String()

	br := bufio.NewReader(strings.NewReader(got))
	addr, err := ReadV1(br)
	if err != nil {
		t.Fatalf("ReadV1(%q): %v", got, err)
	}
	if addr == nil {
		t.Fatalf("ReadV1(%q) 返回 nil 地址（写侧不该对合法 TCP 地址写 UNKNOWN）", got)
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("读头之后的字节: %v", err)
	}
	return *addr, string(rest)
}

func tcp(t *testing.T, s string) *net.TCPAddr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", s)
	if err != nil {
		t.Fatalf("解析 %q: %v", s, err)
	}
	return a
}

// v1 是**线格式**：这一行是多少字节、长什么样，都必须是死的契约 ——
// 两端是两个进程，改一个字节就是"网关与后端对不上"。
func TestWriteV1WireFormat(t *testing.T) {
	var sb strings.Builder
	if err := WriteV1(&sb, tcp(t, "203.0.113.7:51234"), tcp(t, "10.0.0.5:7200")); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	want := "PROXY TCP4 203.0.113.7 10.0.0.5 51234 7200\r\n"
	if sb.String() != want {
		t.Errorf("线格式不符\n  want %q\n  got  %q", want, sb.String())
	}
	if n := len(sb.String()); n > MaxV1Len {
		t.Errorf("头长 %d 超过协议上限 %d", n, MaxV1Len)
	}
}

func TestWriteV1IPv6(t *testing.T) {
	var sb strings.Builder
	if err := WriteV1(&sb, tcp(t, "[2001:db8::1]:51234"), tcp(t, "[2001:db8::5]:7200")); err != nil {
		t.Fatalf("WriteV1: %v", err)
	}
	if !strings.HasPrefix(sb.String(), "PROXY TCP6 2001:db8::1 2001:db8::5 51234 7200") {
		t.Errorf("IPv6 头 = %q", sb.String())
	}
	if !strings.HasSuffix(sb.String(), "\r\n") {
		t.Errorf("IPv6 头未以 CRLF 结束: %q", sb.String())
	}
}

// 四个 v4 地址写出来的头必须能读到同样的地址（往返一致）。
func TestRoundTripIPv4(t *testing.T) {
	addr, rest := roundTrip(t, tcp(t, "198.51.100.9:1234"), tcp(t, "10.0.0.1:7400"))
	if got := addr.String(); got != "198.51.100.9:1234" {
		t.Errorf("源地址 = %q", got)
	}
	if rest != "X" {
		t.Errorf("读头之后的字节 = %q（应为 %q —— 头之后的协议数据一个字节都不能少）", rest, "X")
	}
}

func TestRoundTripIPv6(t *testing.T) {
	addr, rest := roundTrip(t, tcp(t, "[2001:db8::1]:1234"), tcp(t, "[2001:db8::2]:7400"))
	if !addr.IP.Equal(net.ParseIP("2001:db8::1")) || addr.Port != 1234 {
		t.Errorf("源地址 = %v", addr)
	}
	if rest != "X" {
		t.Errorf("读头之后的字节 = %q", rest)
	}
}

// 拿不出 TCP 地址时只能写 UNKNOWN —— 猜一个假地址比说"不知道"危险得多。
func TestWriteV1Unknown(t *testing.T) {
	cases := []struct {
		name     string
		src, dst net.Addr
		want     string
	}{
		{"nil", nil, nil, "PROXY UNKNOWN\r\n"},
		{"非 TCP 地址", fakeAddr("unix"), fakeAddr("unix"), "PROXY UNKNOWN\r\n"},
		{"源是 v4、目的是 v6（族不同）", tcp(t, "1.2.3.4:1"), tcp(t, "[2001:db8::1]:2"), "PROXY UNKNOWN\r\n"},
	}
	for _, c := range cases {
		var sb strings.Builder
		if err := WriteV1(&sb, c.src, c.dst); err != nil {
			t.Errorf("%s: WriteV1: %v", c.name, err)
			continue
		}
		if sb.String() != c.want {
			t.Errorf("%s: got %q, want %q", c.name, sb.String(), c.want)
		}
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return string(a) }
func (a fakeAddr) String() string  { return string(a) }

// UNKNOWN 是合法的：接收方按规格退回 socket 对端地址，**不是错误**。
func TestReadV1UnknownIsNotAnError(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("PROXY UNKNOWN\r\nrest"))
	addr, err := ReadV1(br)
	if err != nil {
		t.Fatalf("ReadV1(UNKNOWN): %v", err)
	}
	if addr != nil {
		t.Errorf("UNKNOWN 应返回 nil 地址，got %v", addr)
	}
	// 规格允许 UNKNOWN 后面跟说明文字，也要能读过去。
	br2 := bufio.NewReader(strings.NewReader("PROXY UNKNOWN ffff:f...f:ffff 65535 65535\r\nrest"))
	if addr, err := ReadV1(br2); err != nil || addr != nil {
		t.Errorf("带附加字段的 UNKNOWN: addr=%v err=%v", addr, err)
	}
	if rest, _ := io.ReadAll(br2); string(rest) != "rest" {
		t.Errorf("UNKNOWN 之后的字节 = %q", rest)
	}
}

// 非法头一律报错 —— 这是接入面的第一个安全边界，不能"尽力而为"地猜。
func TestReadV1Rejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"没写头（HTTP 请求）", "GET / HTTP/1.1\r\n", ErrMissing},
		{"没写头（自研协议首帧）", "\x08\x00\x00\x00junk\r\n", ErrMissing},
		{"PROXY 后缺空格", "PROXYTCP4 1.2.3.4 5.6.7.8 1 2\r\n", ErrMalformed},
		{"未知协议族", "PROXY SCTP4 1.2.3.4 5.6.7.8 1 2\r\n", ErrMalformed},
		{"地址段少一段", "PROXY TCP4 1.2.3.4 5.6.7.8 1\r\n", ErrMalformed},
		{"地址段多一段", "PROXY TCP4 1.2.3.4 5.6.7.8 1 2 3\r\n", ErrMalformed},
		{"源地址不是 IP", "PROXY TCP4 host.example 5.6.7.8 1 2\r\n", ErrMalformed},
		{"TCP4 里塞 IPv6", "PROXY TCP4 2001:db8::1 5.6.7.8 1 2\r\n", ErrMalformed},
		{"TCP6 里塞 IPv4", "PROXY TCP6 1.2.3.4 5.6.7.8 1 2\r\n", ErrMalformed},
		{"目的地址非法", "PROXY TCP4 1.2.3.4 nope 1 2\r\n", ErrMalformed},
		{"端口非数字", "PROXY TCP4 1.2.3.4 5.6.7.8 abc 2\r\n", ErrMalformed},
		{"端口为负", "PROXY TCP4 1.2.3.4 5.6.7.8 -1 2\r\n", ErrMalformed},
		{"端口越界", "PROXY TCP4 1.2.3.4 5.6.7.8 1 65536\r\n", ErrMalformed},
		{"只有 LF、没有 CR", "PROXY TCP4 1.2.3.4 5.6.7.8 1 2\n", ErrMalformed},
		{"一直不发换行", strings.Repeat("A", MaxV1Len+10), ErrTooLong},
		{"v2 二进制头", "\r\n\r\n\x00\r\nQUIT\n" + strings.Repeat("\x00", 20), ErrV2},
	}
	for _, c := range cases {
		_, err := ReadV1(bufio.NewReader(strings.NewReader(c.in)))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

// 头与首帧**同一次写出**（真实网络里极常见）：这是本包存在的理由 ——
// 用裸 net.Conn 读头会把首帧开头吞掉，包一层缓冲才不会。
func TestNewConnKeepsBytesAfterHeader(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = io.WriteString(client, "PROXY TCP4 203.0.113.7 10.0.0.5 51234 7200\r\nFIRSTFRAME")
	}()

	ec, src, err := NewConn(server, time.Second)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	if src == nil || src.String() != "203.0.113.7:51234" {
		t.Fatalf("源地址 = %v", src)
	}
	got := make([]byte, len("FIRSTFRAME"))
	if _, err := io.ReadFull(ec, got); err != nil {
		t.Fatalf("读头之后的字节: %v", err)
	}
	if string(got) != "FIRSTFRAME" {
		t.Errorf("头之后的字节 = %q，应为 %q", got, "FIRSTFRAME")
	}
}

// require=true 时缺头必须**报错**（而不是退回 socket 地址）——
// 否则"必须带头"就是句空话，能直连的人写不写头都行。
func TestServerConnRequire(t *testing.T) {
	// 缺头
	c1, s1 := net.Pipe()
	defer c1.Close()
	defer s1.Close()
	go func() { _, _ = io.WriteString(c1, "GET / HTTP/1.1\r\n") }()
	if _, _, err := ServerConn(s1, true, time.Second); !errors.Is(err, ErrMissing) {
		t.Errorf("缺头时应报错，got %v", err)
	}

	// 不要求时不看头，直接用 socket 对端地址
	c2, s2 := net.Pipe()
	defer c2.Close()
	defer s2.Close()
	conn, addr, err := ServerConn(s2, false, time.Second)
	if err != nil {
		t.Fatalf("require=false: %v", err)
	}
	if conn != s2 {
		t.Error("require=false 应原样返回连接（不该包一层，那会平白多一层缓冲）")
	}
	if addr != s2.RemoteAddr().String() {
		t.Errorf("地址 = %q，应为 socket 对端 %q", addr, s2.RemoteAddr().String())
	}
}

// UNKNOWN 模式下回退到 socket 对端地址（规格行为），并如实标出那就是网关自己。
func TestServerConnUnknownFallsBackToPeer(t *testing.T) {
	c, s := net.Pipe()
	defer c.Close()
	defer s.Close()
	go func() { _, _ = io.WriteString(c, "PROXY UNKNOWN\r\nblah") }()

	ec, addr, err := ServerConn(s, true, time.Second)
	if err != nil {
		t.Fatalf("ServerConn: %v", err)
	}
	if addr != s.RemoteAddr().String() {
		t.Errorf("地址 = %q，应为 socket 对端 %q", addr, s.RemoteAddr().String())
	}
	rest := make([]byte, 4)
	if _, err := io.ReadFull(ec, rest); err != nil || string(rest) != "blah" {
		t.Errorf("UNKNOWN 之后的字节 = %q (err=%v)", rest, err)
	}
}

// 等头的超时**不能**留给正式协议：不然玩家会在进游戏后莫名掉线。
func TestNewConnClearsHeaderDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ready := make(chan struct{})
	go func() {
		_, _ = io.WriteString(client, "PROXY TCP4 1.2.3.4 5.6.7.8 9 10\r\n")
		close(ready)
		// 故意拖过 headerTimeout 之后再发"协议数据"。
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(client, "LATE")
	}()

	ec, _, err := NewConn(server, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	<-ready
	got := make([]byte, 4)
	if _, err := io.ReadFull(ec, got); err != nil {
		t.Fatalf("头的超时不该延续到协议读: %v", err)
	}
	if string(got) != "LATE" {
		t.Errorf("读到的字节 = %q", got)
	}
}

// 首行来自对端，**必须**截断并转义后再进错误信息：否则日志里会塞进任意二进制
// （终端转义序列可以借此操控日志阅读者的终端）。
func TestClipEscapesControlBytes(t *testing.T) {
	in := "\x1b]0;pwned\x07" + strings.Repeat("A", 64) + "\r\n"
	_, err := ReadV1(bufio.NewReader(strings.NewReader(in)))
	if err == nil {
		t.Fatalf("%q 应报错", in)
	}
	msg := err.Error()
	if strings.ContainsRune(msg, 0x1b) || strings.ContainsRune(msg, 0x07) {
		t.Errorf("错误信息里残留了原始控制字节: %q", msg)
	}
	if !strings.Contains(msg, "\\x1b") {
		t.Errorf("控制字节应转义成 \\xNN: %q", msg)
	}
	if n := len(msg); n > 128 {
		t.Errorf("错误信息没有截断（%d 字节）: %q", n, msg)
	}
}

// Package proxyproto 实现 **PROXY protocol v1**（HAProxy 事实标准）的读写两端。
//
// 解决的问题：`gate` 是纯字节转发层（[D-17]），它在客户端与后端之间**另起一条 TCP 连接**，
// 于是后端看到的对端永远是**网关自己**（往往就是 `127.0.0.1`），真实客户端地址在那一跳丢了。
// 旧协议靠"往登录首帧的文本 token 末尾拼 `|<IP>`"夹带，那需要网关**读懂协议**——
// 正是 D-17 要消灭的东西（[D-23] 的抉择）。
//
// PROXY protocol 把这件事挪到**协议之外**：连接建立后、任何协议数据之前，先写一行 ASCII，
//
//	PROXY TCP4 203.0.113.7 10.0.0.5 51234 7200\r\n
//	      └┬─┘ └┬─┘ └───┬────┘ └──┬──┘ └─┬─┘ └┬─┘
//	    固定签名 族    源地址     目的地址 源端口 目的端口
//
// 接收方剥掉这一行，之后的字节流**原样不动**。因此网关不必认识游戏协议、后端也不必多一个
// schema 字段 —— 它刚好落在 D-17 与"可信 IP"两个约束的交点上。
//
// ⚠️ 接入策略只有两种，**没有**第三种：
//
//	必须带（[ServerConn] 的 require=true）或**完全不认**（require=false）。
//
// 「有头就解析、没头就退回 socket 地址」是**可伪造**的 —— 能直连到后端的人自己写一行
// `PROXY TCP4 1.2.3.4 …` 就能冒充任意 IP，那等于回到"客户端自报"。缺头即断开。
//
// 本实现只做 v1（文本）。v2 是二进制、为 UDP 与高性能场景设计，我们不需要；但会**认出**
// 它的魔数并给出明确报错（[ErrV2]），免得"对端配成了 v2"表现成一句笼统的"格式非法"。
//
// [D-17]: ../../../docs/decisions.md
// [D-23]: ../../../docs/decisions.md
package proxyproto

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// MaxV1Len 是 v1 头（含结尾 CRLF）的最大长度。协议规定接收方**必须**支持到 107 字节，
// 超出的部分不必再看 —— 这里直接判为非法，也就顺带堵住"不发换行、无限喂字节"。
const MaxV1Len = 107

// DefaultHeaderTimeout 是等待 PROXY 头到达的默认上限。
//
// 必须有这个上限：头是**接入面的第一份输入**，读不到就断开的连接不该被无限期挂着
// （否则光开连接不说话的扫描流量会一直占着 goroutine 与 fd）。
const DefaultHeaderTimeout = 10 * time.Second

// v2Magic 是 PROXY protocol v2 的 12 字节签名。
var v2Magic = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

var (
	// ErrMissing 表示对端没写 PROXY 头（首行不是 `PROXY `）。
	ErrMissing = errors.New("proxyproto: 缺少 PROXY 头")
	// ErrMalformed 表示头存在但格式或取值非法。
	ErrMalformed = errors.New("proxyproto: PROXY 头格式非法")
	// ErrTooLong 表示还没见到 CRLF 就已超过 [MaxV1Len]。
	ErrTooLong = errors.New("proxyproto: PROXY 头超过 107 字节")
	// ErrV2 表示对端发来的是 v2 二进制头（本实现只支持 v1）。
	ErrV2 = errors.New("proxyproto: 收到 v2 二进制头（本实现只支持 v1）")
)

// ReadV1 从 r 读一行 PROXY v1 头并**消费掉**它，返回客户端源地址。
//
// r 必须是行缓冲的（`*bufio.Reader`，见 [NewConn]）：头与首个协议帧常常在同一次 read 里
// 一起到达，若对它用裸 `net.Conn.Read` 读头，会把首帧开头那几个字节一起吞掉。
//
// 返回 `(nil, nil)` 表示对端写的是 `PROXY UNKNOWN`（它自己也不知道地址）—— 这不算错误，
// 调用方按规格退回 socket 对端地址即可（[ServerConn] 就是这么做的）。
func ReadV1(r *bufio.Reader) (*net.TCPAddr, error) {
	// v2 魔数优先认出：否则它会被当成"某行没有 PROXY 前缀的垃圾"，诊断价值全丢。
	if head, err := r.Peek(len(v2Magic)); err == nil && bytes.Equal(head, v2Magic) {
		return nil, ErrV2
	}
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	rest, ok := strings.CutPrefix(line, "PROXY ")
	if !ok {
		// 分两种报错：真没有前缀 vs 有前缀但少个空格。排查时这两者指向不同的配置错误。
		if strings.HasPrefix(line, "PROXY") {
			return nil, fmt.Errorf("%w（%q 之后缺空格）", ErrMalformed, line)
		}
		return nil, fmt.Errorf("%w（首行 %q）", ErrMissing, clip(line))
	}

	fields := strings.Split(rest, " ")
	switch fields[0] {
	case "UNKNOWN":
		// 规格要求接收方忽略它后面的内容（可以跟任意说明），所以只看这一段落。
		return nil, nil
	case "TCP4", "TCP6":
	default:
		return nil, fmt.Errorf("%w（协议族 %q）", ErrMalformed, clip(fields[0]))
	}
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w（地址段应为 4 段，实际 %d 段）", ErrMalformed, len(fields)-1)
	}

	srcIP, dstIP, srcPort, dstPort := fields[1], fields[2], fields[3], fields[4]
	ip := net.ParseIP(srcIP)
	if ip == nil {
		return nil, fmt.Errorf("%w（源地址 %q）", ErrMalformed, clip(srcIP))
	}
	// 族与地址必须自洽：TCP4 段里塞 IPv6（或反过来）说明对端在乱写，
	// 放过它会让下游拿到一个"地址族对不上"的值。
	if fields[0] == "TCP4" {
		if ip.To4() == nil {
			return nil, fmt.Errorf("%w（TCP4 但源地址 %q 不是 IPv4）", ErrMalformed, clip(srcIP))
		}
		ip = ip.To4()
	} else if ip.To4() != nil {
		return nil, fmt.Errorf("%w（TCP6 但源地址 %q 是 IPv4）", ErrMalformed, clip(srcIP))
	}
	// 目的地址我们不用，但同样要合法 —— 非法值说明对端有问题，不该放过。
	if net.ParseIP(dstIP) == nil {
		return nil, fmt.Errorf("%w（目的地址 %q）", ErrMalformed, clip(dstIP))
	}
	port, err := parsePort(srcPort)
	if err != nil {
		return nil, fmt.Errorf("%w（源端口 %q）", ErrMalformed, clip(srcPort))
	}
	if _, err := parsePort(dstPort); err != nil {
		return nil, fmt.Errorf("%w（目的端口 %q）", ErrMalformed, clip(dstPort))
	}
	return &net.TCPAddr{IP: ip, Port: port}, nil
}

// WriteV1 写出 v1 头：`PROXY TCP4 <src> <dst> <sport> <dport>\r\n`。
//
// src/dst 不是 TCP 地址（或为 nil）时按规格写 `PROXY UNKNOWN\r\n` —— 与其猜一个假地址，
// 不如明说不知道；接收方会退回 socket 对端地址。
//
// ⚠️ 调用时机只有一个：**连上之后、转发任何协议数据之前**。写晚了就变成协议中间的垃圾。
func WriteV1(w io.Writer, src, dst net.Addr) error {
	_, err := io.WriteString(w, formatV1(src, dst))
	return err
}

// formatV1 组装那一行。任何拿不出合法地址/族的情况都退化成 UNKNOWN。
func formatV1(src, dst net.Addr) string {
	const unknown = "PROXY UNKNOWN\r\n"
	s, okS := src.(*net.TCPAddr)
	d, okD := dst.(*net.TCPAddr)
	if !okS || !okD || s == nil || d == nil {
		return unknown
	}
	fam, sIP, dIP, err := familyOf(s.IP, d.IP)
	if err != nil {
		return unknown
	}
	line := fmt.Sprintf("PROXY %s %s %s %d %d\r\n", fam, sIP, dIP, s.Port, d.Port)
	if len(line) > MaxV1Len {
		// 正常地址写不出这么长（IPv6 最长也就 90 来字节）；真出现了宁可说不知道。
		return unknown
	}
	return line
}

// familyOf 判两端的地址族并给出规范写法。
//
// 两端族不同时**不猜**：规格要求源与目的同族，而拼一个跨族的头只会让接收方误判。
func familyOf(srcIP, dstIP net.IP) (fam, src, dst string, err error) {
	s4, d4 := srcIP.To4(), dstIP.To4()
	switch {
	case s4 != nil && d4 != nil:
		return "TCP4", s4.String(), d4.String(), nil
	case s4 == nil && d4 == nil:
		s6, d6 := srcIP.To16(), dstIP.To16()
		if s6 == nil || d6 == nil {
			return "", "", "", errors.New("地址非法")
		}
		return "TCP6", s6.String(), d6.String(), nil
	default:
		return "", "", "", errors.New("源与目的地址族不同")
	}
}

// NewConn 从 c 读掉一行 PROXY v1 头，返回"接着往下读"的连接与客户端源地址。
//
// 返回的连接在**读**方向是包了一层的（先吐缓冲里残留的字节，再接续底层连接），
// 写与关闭仍直达原连接，所以调用方直接把它当 net.Conn 用即可 ——
// 但**后续读协议必须用返回的这个**，用原来的 c 会丢掉缓冲里那一段。
func NewConn(c net.Conn, headerTimeout time.Duration) (net.Conn, *net.TCPAddr, error) {
	if headerTimeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(headerTimeout))
	}
	br := bufio.NewReader(c)
	src, err := ReadV1(br)
	if headerTimeout > 0 {
		// ⚠️ 一定要清掉：这是"等头"的超时，不是协议读的超时。
		// 留着它会让正式协议读到一半莫名超时断开（表现为"玩着玩着掉线"）。
		_ = c.SetReadDeadline(time.Time{})
	}
	if err != nil {
		return nil, nil, err
	}
	return &bufferedConn{Conn: c, br: br}, src, nil
}

// ServerConn 是**接入侧**的便捷包装，按 require 决定要不要读头：
//
//	require=true   必须带 PROXY v1 头；缺失/非法 ⇒ 返回错误，调用方应断开。
//	require=false  完全不认头，原样返回 c 与 socket 对端地址（本地直连调试用）。
//
// 返回的 clientAddr 形如 `203.0.113.7:51234`（与 `net.Conn.RemoteAddr().String()` 同形，
// 可直接喂给 `data.AdminList.LevelFor`，它会自己剥掉端口）。
//
// ⚠️ 刻意**没有**"有就解析、没有就用 socket 地址"这第三种模式 —— 那是可伪造的
// （见包注释）。接入策略要么"必须带"，要么"完全不认"，中间态不存在。
func ServerConn(c net.Conn, require bool, headerTimeout time.Duration) (net.Conn, string, error) {
	peer := c.RemoteAddr().String()
	if !require {
		return c, peer, nil
	}
	ec, src, err := NewConn(c, headerTimeout)
	if err != nil {
		return nil, "", err
	}
	if src == nil {
		// `PROXY UNKNOWN`：发送方自己都不知道地址。按规格退回 socket 对端，
		// 但那意味着 IP 记成了网关自己的 ⇒ 调用方至少能从日志看出来。
		return ec, peer, nil
	}
	return ec, src.String(), nil
}

// bufferedConn 让"已经读进缓冲的字节"能被后续每次 Read 看见。
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// readLine 读到 `\n` 为止（返回内容不含 CRLF），并校验确实以 CRLF 结束。
//
// 逐字节读：头本来就短（≤107），不值得为它引入一次大缓冲；
// 顺带保证"没有换行就不停喂"这种情况在 107 字节处收手。
func readLine(r *bufio.Reader) (string, error) {
	buf := make([]byte, 0, 64)
	for i := 0; i < MaxV1Len; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("proxyproto: 读头失败: %w", err)
		}
		if b == '\n' {
			if len(buf) == 0 || buf[len(buf)-1] != '\r' {
				return "", fmt.Errorf("%w（未以 CRLF 结束）", ErrMalformed)
			}
			return string(buf[:len(buf)-1]), nil
		}
		buf = append(buf, b)
	}
	return "", ErrTooLong
}

// parsePort 解析 v1 里的十进制端口（0–65535，只允许数字）。
//
// 不用 `strconv.Atoi` 直接收：它会接受 `+80` / `-1` 这类写法，而这是**接入面的安全边界**，
// 宁可只认最规矩的那种。
func parsePort(s string) (int, error) {
	if s == "" || len(s) > 5 {
		return 0, errors.New("proxyproto: 端口位数非法")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("proxyproto: 端口含非数字")
		}
		n = n*10 + int(s[i]-'0')
	}
	if n > 65535 {
		return 0, errors.New("proxyproto: 端口越界")
	}
	return n, nil
}

// clip 把可能任意长的首行截成短而可打印的一段，只为出错时能看出对端发的是什么。
//
// ⚠️ 必须截断且转义：这个字符串会进日志，而它完全来自对端 ——
// 直接打印等于把任意字节（含终端转义序列）递给日志阅读者。
func clip(s string) string {
	const maxLen = 32
	var b strings.Builder
	for i, r := range s {
		if i >= maxLen {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7F {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

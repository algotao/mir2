package wire

import (
	"bytes"
	"errors"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/proto"
)

// 带外控制指令。它们不是 DefMsg 包，走同一条连接但语义独立。
var (
	// KeepAlive 是客户端的心跳/窗口确认。收到后应剔除该字符，
	// 网关侧还要据此清空反加速发送窗口（见 docs/legacy-analysis.md §8.3）。
	KeepAlive = []byte{'*'}

	// SelGateSwitch 是"切换到角色网关"：'$S' + ip + '/' + port + '%'
	SelGateSwitch = []byte{'$', 'S'}
	// RunGateSwitch 是"切换到游戏网关"：'$R' + ip + '/' + port + '%'
	RunGateSwitch = []byte{'$', 'R'}
	// RunGateSwitchAlt 是备选写法 '$C'。
	RunGateSwitchAlt = []byte{'$', 'C'}

	// DigCmd 是挖矿指令 "=DIG"。
	DigCmd = []byte{'=', 'D', 'I', 'G'}
)

var (
	// ErrShortPacket 表示负载不足一个消息头。
	ErrShortPacket = errors.New("wire: 负载短于 DEFBLOCKSIZE")
	// ErrBadEncoding 表示 6bit 消息头解码失败（含非法字符）。
	ErrBadEncoding = errors.New("wire: 6bit 消息头解码失败")
	// ErrBadLoginToken 表示认证首包格式非法。
	ErrBadLoginToken = errors.New("wire: 认证首包格式非法")
)

// LoginToken 是进入游戏时的认证首包（客户端连上游戏服务后发的第一条）。
//
// 格式：`**<账号>/<角色名>/<会话号>/<版本>/<序号>`。
//
// ⚠️ 这里**没有**"客户端 IP"字段，而且不要加回去：旧协议让网关把真实 IP 拼在末尾
// （`<序号>|<IP>`），但那需要报文体里有一个**只有网关才该填**的字段 ——
// 既让 schema 语义被污染，又等于相信客户端自报（谁都能自称 1.2.3.4）。
// 真实地址改由**连接级**的 PROXY protocol 交代（docs/decisions.md D-23，
// 实现见 internal/proxyproto）。
type LoginToken struct {
	Account       string
	ChrName       string
	SessionID     int32
	ClientVersion int32
	// Idx 是末段标记："0" 表示正常登录，非 "0" 原版视为异常。
	Idx string
}

// ParseLoginToken 解析认证首包。
//
// 负载**先经 6bit 解码**，得到：
//
//	**<account>/<chrName>/<sessionID>/<clientVersion>/<idx>
//
// 依据 /data/git/MIR2/GameOfMir/M2Server/RunSock.pas:407-429 GetCertification。
//
// ⚠️ 两处容易漏的细节：
//  1. 开头的 "**" 必须存在，之后才按 '/' 切分；
//  2. 原版要求 nSessionID >= 2（SessionID 从 1 递增，2 起才算有效会话）。
func ParseLoginToken(payload []byte) (*LoginToken, error) {
	s := string(codec.Decode6BitBuf(payload))
	if len(s) <= 2 || s[0] != '*' || s[1] != '*' {
		return nil, ErrBadLoginToken
	}
	parts := strings.SplitN(s[2:], "/", 5)
	if len(parts) != 5 {
		return nil, ErrBadLoginToken
	}
	sid, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, ErrBadLoginToken
	}
	ver, _ := strconv.Atoi(parts[3])

	t := &LoginToken{
		Account:       parts[0],
		ChrName:       parts[1],
		SessionID:     int32(sid),
		ClientVersion: int32(ver),
		Idx:           parts[4],
	}
	if t.Account == "" || t.ChrName == "" || t.SessionID < 2 {
		return nil, ErrBadLoginToken
	}
	return t, nil
}

// Packet 是一条应用层消息：16 字符的 6bit 头 + 可选的 6bit 正文。
type Packet struct {
	Head proto.DefaultMessage
	Body string
}

// DecodePacket 从帧负载解析消息。
//
// 负载结构：前 DEFBLOCKSIZE(16) 字符是 6bit 编码的 TDefaultMessage，
// 其余是 6bit 编码的正文（可能为空）。
func DecodePacket(payload []byte) (Packet, error) {
	if len(payload) < proto.DEFBLOCKSIZE {
		return Packet{}, ErrShortPacket
	}
	head, ok := proto.DecodeMessage(string(payload[:proto.DEFBLOCKSIZE]))
	if !ok {
		return Packet{}, ErrBadEncoding
	}
	p := Packet{Head: head}
	if rest := payload[proto.DEFBLOCKSIZE:]; len(rest) > 0 {
		p.Body = string(codec.Decode6BitBuf(rest))
	}
	return p, nil
}

// Encode 序列化为帧负载。正文为空时只输出消息头。
func (p Packet) Encode() []byte {
	out := []byte(p.Head.EncodeMessage())
	if p.Body != "" {
		out = append(out, codec.Encode6BitBuf([]byte(p.Body))...)
	}
	return out
}

// Ident 返回消息号。
func (p Packet) Ident() uint16 { return p.Head.Ident }

// IsKeepAlive 报告负载是否为心跳确认。
func IsKeepAlive(payload []byte) bool { return bytes.Equal(payload, KeepAlive) }

// ParseGateSwitch 解析 "$S<ip>/<port>%" 或 "$R<ip>/<port>%"。
// 返回地址与端口；不是切换指令返回 ok=false。
func ParseGateSwitch(payload []byte) (addr string, port int, ok bool) {
	if len(payload) < 4 {
		return "", 0, false
	}
	switch {
	case bytes.HasPrefix(payload, SelGateSwitch),
		bytes.HasPrefix(payload, RunGateSwitch),
		bytes.HasPrefix(payload, RunGateSwitchAlt):
	default:
		return "", 0, false
	}
	rest := payload[2:]
	if len(rest) == 0 || rest[len(rest)-1] != '%' {
		return "", 0, false
	}
	rest = rest[:len(rest)-1]
	i := bytes.IndexByte(rest, '/')
	if i < 0 {
		return "", 0, false
	}
	addr = string(rest[:i])
	for _, c := range rest[i+1:] {
		if c < '0' || c > '9' {
			return "", 0, false
		}
		port = port*10 + int(c-'0')
	}
	return addr, port, true
}

// FormatGateSwitch 生成切换指令，如 "$S127.0.0.1/7100%"。
func FormatGateSwitch(prefix []byte, addr string, port int) []byte {
	out := make([]byte, 0, len(prefix)+len(addr)+8)
	out = append(out, prefix...)
	out = append(out, addr...)
	out = append(out, '/')
	out = append(out, itoa(port)...)
	out = append(out, '%')
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

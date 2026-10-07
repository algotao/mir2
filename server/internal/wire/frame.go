// Package wire 实现客户端线路层的帧与消息包处理。
//
// 帧格式（客户端 ↔ 网关，必须与原版字节级兼容）：
//
//	上行（客户端 → 服务端）：'#' + 1位序号('1'..'9' 循环) + 负载 + '!'
//	下行（服务端 → 客户端）：'#' + 负载 + '!'
//
// ⚠️ 上下行**不对称**：上行带序号，下行不带。
// 依据：客户端 SendSocket（MirClient/ClMain.pas:3373-3384）拼 IntToStr(code)；
// 服务端下发（RunGate/Main.pas:1230-1263、LoginSrv/LMain.pas:1550）不含序号。
//
// 负载 = 16 字符的 6bit 消息头（DEFBLOCKSIZE）+ 可选的 6bit 正文。
package wire

import "errors"

const (
	frameStart = '#'
	frameEnd   = '!'
)

var (
	// ErrShortFrame 表示帧不完整（半包），调用方应继续累积。
	ErrShortFrame = errors.New("wire: 帧不完整")
	// ErrBadFrame 表示帧格式非法（缺少定界符）。
	ErrBadFrame = errors.New("wire: 帧格式非法")
)

// Frame 是一条线路帧。
type Frame struct {
	// Seq 是上行序号 '1'..'9'；下行帧为 0。
	Seq byte
	// Payload 是定界符之间的原始负载（6bit 编码）。
	Payload []byte
}

// IsFromClient 报告该帧是否来自客户端（带序号）。
func (f Frame) IsFromClient() bool { return f.Seq >= '1' && f.Seq <= '9' }

// DecodeFrame 解析单条完整帧（含首尾定界符）。
// EncodeFrame 构造一个完整帧（含定界符）。
//
// seq 为 0 表示**下行**帧（不带序号）；上行帧传 '1'..'9'。
// 网关需要它来重写首帧（把真实客户端 IP 拼进登录 token 后重新封帧）。
func EncodeFrame(seq byte, payload []byte) []byte {
	out := make([]byte, 0, len(payload)+3)
	out = append(out, frameStart)
	if seq != 0 {
		out = append(out, seq)
	}
	out = append(out, payload...)
	out = append(out, frameEnd)
	return out
}

func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < 2 || b[0] != frameStart || b[len(b)-1] != frameEnd {
		return Frame{}, ErrBadFrame
	}
	body := b[1 : len(b)-1]
	var f Frame
	// 上行帧首字符是 1 位序号
	if len(body) > 0 && body[0] >= '1' && body[0] <= '9' {
		f.Seq = body[0]
		f.Payload = body[1:]
	} else {
		f.Payload = body
	}
	return f, nil
}

// Bytes 序列化为线路字节。Seq 为 0 时生成下行帧。
func (f Frame) Bytes() []byte {
	out := make([]byte, 0, len(f.Payload)+3)
	out = append(out, frameStart)
	if f.Seq != 0 {
		out = append(out, f.Seq)
	}
	out = append(out, f.Payload...)
	out = append(out, frameEnd)
	return out
}

// EncodeUp 组装上行帧（带序号）。
func EncodeUp(seq byte, payload []byte) []byte {
	return Frame{Seq: seq, Payload: payload}.Bytes()
}

// EncodeDown 组装下行帧（无序号）。
func EncodeDown(payload []byte) []byte {
	return Frame{Payload: payload}.Bytes()
}

// Splitter 处理 TCP 流式数据，切出完整帧。
//
// 用法：Feed(收到的数据)，然后循环 Next() 取出完整帧；
// Next 返回 ErrShortFrame 表示半包，应继续 Feed。
type Splitter struct {
	buf    []byte
	maxLen int
}

// NewSplitter 创建拆分器，maxLen 是单帧负载上限（防恶意超长包，0 表示不限制）。
func NewSplitter(maxLen int) *Splitter {
	if maxLen <= 0 {
		maxLen = 1 << 20
	}
	return &Splitter{maxLen: maxLen}
}

// Feed 追加新收到的数据。
func (s *Splitter) Feed(b []byte) { s.buf = append(s.buf, b...) }

// Next 取出一条完整帧（含定界符）。返回 ErrShortFrame 表示还需更多数据。
func (s *Splitter) Next() ([]byte, error) {
	start := -1
	for i, c := range s.buf {
		if c == frameStart {
			start = i
			break
		}
	}
	if start < 0 {
		// 没有帧起始符：整段都是垃圾。若已超限说明对端在灌垃圾（扫描/攻击），
		// 上报 ErrBadFrame 让调用方断链；否则清空后继续等待。
		if len(s.buf) > s.maxLen {
			s.buf = s.buf[:0]
			return nil, ErrBadFrame
		}
		s.buf = s.buf[:0]
		return nil, ErrShortFrame
	}
	if start > 0 {
		s.buf = s.buf[start:]
	}

	end := -1
	for i, c := range s.buf {
		if c == frameEnd {
			end = i
			break
		}
	}
	if end < 0 {
		// 半包；若已远超上限则判定为攻击/损坏，清空缓冲
		if len(s.buf) > s.maxLen {
			s.buf = s.buf[:0]
			return nil, ErrBadFrame
		}
		return nil, ErrShortFrame
	}
	frame := make([]byte, end+1)
	copy(frame, s.buf[:end+1])
	s.buf = s.buf[end+1:]
	return frame, nil
}

// Buffered 返回尚未处理的字节数。
func (s *Splitter) Buffered() int { return len(s.buf) }

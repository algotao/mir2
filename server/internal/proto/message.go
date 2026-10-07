package proto

import (
	"encoding/binary"

	"github.com/algotao/mir2/server/internal/codec"
)

// DefaultMessageSize 是 TDefaultMessage 的二进制长度（字节）。
//
// 对应 Delphi Common/Grobal2.pas:498-504：
//
//	Recog:Integer(4) Ident:Word(2) Param:Word(2) Tag:Word(2) Series:Word(2)
//
// 该 record 非 packed，但字段天然对齐，无填充，故恰好 12 字节。
const DefaultMessageSize = 12

// DEFBLOCKSIZE 是 TDefaultMessage 经 6bit 编码后的**字符数**（非字节数）。
//
// 对应 Delphi Common/Grobal2.pas:58。客户端按 16 字符切出消息头，
// 因此拆包用 16、编解码用 12——两者不可混用。
const DEFBLOCKSIZE = 16

// DefaultMessage 是客户端协议的消息头。
//
// Ident 是消息号（CM_/SM_ 系列），其余字段的语义随 Ident 变化。
// Recog/Param/Tag/Series 在 Delphi 侧被大量用作 MakeLong(x,y) 打包的坐标对。
type DefaultMessage struct {
	Recog  int32
	Ident  uint16
	Param  uint16
	Tag    uint16
	Series uint16
}

// MakeDefaultMsg 对应 Delphi 的 MakeDefaultMsg（Grobal2.pas:2690 / EDcode.pas:77）。
func MakeDefaultMsg(ident uint16, recog int32, param, tag, series uint16) DefaultMessage {
	return DefaultMessage{Recog: recog, Ident: ident, Param: param, Tag: tag, Series: series}
}

// Append 把消息头以小端序追加到 dst，返回新的切片。
//
// 小端序是 x86 Delphi 的原生字节序，不可更改。
func (m DefaultMessage) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Recog))
	dst = binary.LittleEndian.AppendUint16(dst, m.Ident)
	dst = binary.LittleEndian.AppendUint16(dst, m.Param)
	dst = binary.LittleEndian.AppendUint16(dst, m.Tag)
	dst = binary.LittleEndian.AppendUint16(dst, m.Series)
	return dst
}

// Bytes 返回 12 字节的小端序编码。
func (m DefaultMessage) Bytes() [DefaultMessageSize]byte {
	var b [DefaultMessageSize]byte
	binary.LittleEndian.PutUint32(b[0:], uint32(m.Recog))
	binary.LittleEndian.PutUint16(b[4:], m.Ident)
	binary.LittleEndian.PutUint16(b[6:], m.Param)
	binary.LittleEndian.PutUint16(b[8:], m.Tag)
	binary.LittleEndian.PutUint16(b[10:], m.Series)
	return b
}

// DecodeDefaultMessage 从至少 12 字节的缓冲解析消息头。
func DecodeDefaultMessage(b []byte) (m DefaultMessage, ok bool) {
	if len(b) < DefaultMessageSize {
		return m, false
	}
	m.Recog = int32(binary.LittleEndian.Uint32(b[0:]))
	m.Ident = binary.LittleEndian.Uint16(b[4:])
	m.Param = binary.LittleEndian.Uint16(b[6:])
	m.Tag = binary.LittleEndian.Uint16(b[8:])
	m.Series = binary.LittleEndian.Uint16(b[10:])
	return m, true
}

// EncodeMessage 把消息头编码为 16 个 6bit 字符。
// 对应 Delphi Common/EDcode.pas:291 EncodeMessage。
func (m DefaultMessage) EncodeMessage() string {
	raw := m.Bytes()
	return string(codec.Encode6BitBuf(raw[:]))
}

// DecodeMessage 从 6bit 字符串还原消息头。
// 对应 Delphi Common/EDcode.pas DecodeMessage。
func DecodeMessage(s string) (m DefaultMessage, ok bool) {
	raw := codec.Decode6BitBuf([]byte(s))
	return DecodeDefaultMessage(raw)
}

// Payload 描述一条完整的客户端帧：6bit 编码的消息头 + 可选的正文。
//
// 线上形态为 '#' + 1位序号(1..9循环) + head + body + '!'，
// 帧定界由网关负责（见 docs/service-architecture.md §5）。
type Payload struct {
	Head DefaultMessage
	Body string // 已解码的正文；为空表示无正文
}

// MakeLong / LoWord / HiWord 复刻 Delphi 的字操作。
// 原代码大量用 MakeLong(x, y) 把坐标对打包进 Recog/Param/Series。
func MakeLong(lo, hi uint16) int32 {
	return int32(uint32(lo) | uint32(hi)<<16)
}

// LoWord 取低 16 位。
func LoWord(v int32) uint16 { return uint16(uint32(v) & 0xFFFF) }

// HiWord 取高 16 位。
func HiWord(v int32) uint16 { return uint16(uint32(v) >> 16) }

// LoByte / HiByte 取 16 位值的低/高字节。
//
// 原版大量用字节对打包两个小数值，典型如 SM_SUBABILITY
// （ClMain.pas:4801-4806：命中/敏捷、抗毒/抗毒恢复、HP恢复/MP恢复）。
func LoByte(v uint16) uint8 { return uint8(v & 0xFF) }
func HiByte(v uint16) uint8 { return uint8(v >> 8) }

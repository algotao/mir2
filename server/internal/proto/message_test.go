package proto

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestDefaultMessageSize 是 sizeof 断言——Delphi 侧 TDefaultMessage 为 12 字节。
// 任何字段增删都必须同步修改 DefaultMessageSize，否则协议错位。
func TestDefaultMessageSize(t *testing.T) {
	if got := unsafe.Sizeof(DefaultMessage{}); got != DefaultMessageSize {
		t.Fatalf("unsafe.Sizeof(DefaultMessage) = %d, want %d", got, DefaultMessageSize)
	}
	if got := len(DefaultMessage{}.Bytes()); got != DefaultMessageSize {
		t.Fatalf("len(Bytes()) = %d, want %d", got, DefaultMessageSize)
	}
}

// TestMsgHeaderSize 是 sizeof 断言——Delphi 侧 TMsgHeader 为 20 字节（含 2 字节填充）。
func TestMsgHeaderSize(t *testing.T) {
	if got := unsafe.Sizeof(MsgHeader{}); got != MsgHeaderSize {
		t.Fatalf("unsafe.Sizeof(MsgHeader) = %d, want %d", got, MsgHeaderSize)
	}
	var h MsgHeader
	if got := len(h.Append(nil)); got != MsgHeaderSize {
		t.Fatalf("len(Append(nil)) = %d, want %d", got, MsgHeaderSize)
	}
}

// TestDefaultMessageLayout 校验小端序字段偏移。
func TestDefaultMessageLayout(t *testing.T) {
	m := DefaultMessage{Recog: 0x11223344, Ident: 0x5566, Param: 0x7788, Tag: 0x99AA, Series: 0xBBCC}
	b := m.Bytes()

	if binary.LittleEndian.Uint32(b[0:]) != 0x11223344 {
		t.Errorf("Recog 偏移/字节序错误: %x", b[0:4])
	}
	if binary.LittleEndian.Uint16(b[4:]) != 0x5566 {
		t.Errorf("Ident 偏移错误")
	}
	if binary.LittleEndian.Uint16(b[6:]) != 0x7788 {
		t.Errorf("Param 偏移错误")
	}
	if binary.LittleEndian.Uint16(b[8:]) != 0x99AA {
		t.Errorf("Tag 偏移错误")
	}
	if binary.LittleEndian.Uint16(b[10:]) != 0xBBCC {
		t.Errorf("Series 偏移错误")
	}
}

// TestDefaultMessageRoundTrip 二进制与 6bit 两条路径的往返。
func TestDefaultMessageRoundTrip(t *testing.T) {
	m := MakeDefaultMsg(SM_WALK, -12345, 300, 200, 7)

	raw := m.Bytes()
	back, ok := DecodeDefaultMessage(raw[:])
	if !ok || back != m {
		t.Fatalf("二进制往返失败: %+v vs %+v", back, m)
	}

	enc := m.EncodeMessage()
	if len(enc) != DEFBLOCKSIZE {
		t.Fatalf("EncodeMessage 长度 = %d, want DEFBLOCKSIZE=%d", len(enc), DEFBLOCKSIZE)
	}
	back2, ok := DecodeMessage(enc)
	if !ok || back2 != m {
		t.Fatalf("6bit 往返失败: %+v vs %+v", back2, m)
	}
}

// TestEncodeMessageMatchesManual 校验 6bit 编码结果与「手工拼接头+正文」一致。
func TestEncodeMessageMatchesManual(t *testing.T) {
	m := MakeDefaultMsg(CM_SAY, 0, 0, 0, 0)
	raw := m.Bytes()

	// 逐字节推演一次 6bit 编码，确认 codec 调用路径无误
	want := ""
	for i := 0; i+2 < len(raw); i += 3 {
		b0, b1, b2 := raw[i], raw[i+1], raw[i+2]
		want += string([]byte{
			b0>>2 + 0x3C,
			((b0&0x03)<<4 | b1>>4) + 0x3C,
			((b1&0x0F)<<2 | b2>>6) + 0x3C,
			b2&0x3F + 0x3C,
		})
	}
	if got := m.EncodeMessage(); got != want {
		t.Fatalf("EncodeMessage = %q, want %q", got, want)
	}
}

// TestMsgHeaderRoundTrip 校验帧头往返与填充字节。
func TestMsgHeaderRoundTrip(t *testing.T) {
	h := MsgHeader{
		Code:          RunGateCode,
		Socket:        0x0102,
		GSocketIdx:    7,
		Ident:         GM_DATA,
		UserListIndex: 42,
		Length:        128,
	}
	b := h.Append(nil)
	if b[14] != 0 || b[15] != 0 {
		t.Errorf("填充字节必须恒为 0, got %x %x", b[14], b[15])
	}
	back, ok := DecodeMsgHeader(b)
	if !ok || back != h {
		t.Fatalf("往返失败: %+v", back)
	}
	if !back.HasDefaultMessage() || back.PayloadLen() != 128 {
		t.Fatalf("负载长度/类型判定错误: %+v", back)
	}

	// 负长度 = 纯字符串负载
	h.Length = -32
	back, ok = DecodeMsgHeader(h.Append(nil))
	if !ok {
		t.Fatal("负长度解码失败")
	}
	if back.HasDefaultMessage() || back.PayloadLen() != 32 {
		t.Fatalf("负长度语义错误: %+v", back)
	}
}

// TestMsgHeaderBadCode 魔数不匹配必须返回 false，供调用方滑窗重同步。
func TestMsgHeaderBadCode(t *testing.T) {
	b := make([]byte, MsgHeaderSize)
	binary.LittleEndian.PutUint32(b[0:], 0xDEADBEEF)
	if _, ok := DecodeMsgHeader(b); ok {
		t.Fatal("错误魔数不应解析成功")
	}
	if _, ok := DecodeMsgHeader(b[:MsgHeaderSize-1]); ok {
		t.Fatal("短缓冲不应解析成功")
	}
}

// TestWordHelpers 校验 MakeLong/LoWord/HiWord（原代码大量用于打包坐标）。
func TestWordHelpers(t *testing.T) {
	v := MakeLong(0x1234, 0x5678)
	if v != 0x56781234 {
		t.Fatalf("MakeLong = %#x, want 0x56781234", uint32(v))
	}
	if LoWord(v) != 0x1234 || HiWord(v) != 0x5678 {
		t.Fatalf("LoWord/HiWord 错误: %#x %#x", LoWord(v), HiWord(v))
	}
}

package frame

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/algotao/mir2/server/protocol"
)

// helloEnvelope 构造一个**固定**的握手信封，作为黄金报文的输入。
func helloEnvelope() *protocol.Envelope {
	return &protocol.Envelope{
		Seq: 1,
		Body: &protocol.Envelope_ClientHello{
			ClientHello: &protocol.ClientHello{
				ProtocolVersion: protocol.Version,
				ClientBuild:     "test",
				Locale:          "zh-CN",
			},
		},
	}
}

// TestGoldenFrameBytes 是 protocol.md §9.3 的「黄金报文测试」：
// 固定输入 → 断言**输出字节**。任何无意的协议改动（字段号、编码方式、
// 长度域字节序）都会立刻让这条测试变红，而不是等到两端联调时才发现。
//
// 期望值是按 schema 手算的，不是从实现里抄回来的：
//
//	Envelope.seq = 1                      →  08 01
//	Envelope.body 字段号 0x0101（=257）    →  8A 10            （LEN 类型：257<<3|2 = 2058）
//	ClientHello 长度 15                   →  0F
//	  protocol_version = 2                →  08 02
//	  client_build = "test"               →  12 04 74 65 73 74
//	  locale = "zh-CN"                    →  1A 05 7A 68 2D 43 4E
//	帧头 = u32 小端长度 20                  →  14 00 00 00
//
// ⚠️ 版本号的字节是唯一随 `protocol/version.txt` 变的字段（其余都是定长字符串）。
// 这正是它作为"改协议的门"的用法：version 1 → 2（修 `Direction` 枚举顺序）时，
// 必须**看着**这一行确认改的只有它，而不是盲抄一遍新哈希。
func TestGoldenFrameBytes(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, helloEnvelope()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	const wantHex = "1400000008018a100f08021204746573741a057a682d434e"
	got := hex.EncodeToString(buf.Bytes())
	if got != wantHex {
		t.Fatalf("黄金报文不符（协议一改这里必须同步确认）\n  want %s\n  got  %s", wantHex, got)
	}
	// 再加一道哈希：把整帧字节整体钉死（§9.3）。
	// ⚠️ 这里用了 protocol.Version（= version.txt）⇒ **一次 schema/版本变更就应该让
	// 这条测试红**，这正是它存在的意义：改协议不能"顺手改过去"。
	sum := sha256.Sum256(buf.Bytes())
	const wantSum = "994c819606d8c465550516fed310e9950aa580270a4fe0c02716904e1188d02c"
	if hex.EncodeToString(sum[:]) != wantSum {
		t.Logf("帧字节 = %s", got)
		t.Logf("sha256  = %s", hex.EncodeToString(sum[:]))
		t.Fatalf("黄金哈希不符")
	}
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := helloEnvelope()
	if err := Write(&buf, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("往返不一致\n  in  %v\n  out %v", in, out)
	}
	if buf.Len() != 0 {
		t.Fatalf("应读完整个缓冲，剩 %d 字节", buf.Len())
	}
}

func TestMsgName(t *testing.T) {
	if got := MsgName(helloEnvelope()); got != "ClientHello" {
		t.Fatalf("MsgName = %q，期望 ClientHello", got)
	}
	if got := MsgName(&protocol.Envelope{}); got != "" {
		t.Fatalf("无 body 时应返回空串，实得 %q", got)
	}
	if got := MsgName(nil); got != "" {
		t.Fatalf("nil 应返回空串，实得 %q", got)
	}
}

// 版本不匹配必须**明确拒绝**，不做"尽力而为"（protocol.md §5）。
func TestCheckHelloRejectsVersionMismatch(t *testing.T) {
	env := helloEnvelope() // 里面是 protocol.Version
	if _, err := CheckHello(env, protocol.Version); err != nil {
		t.Fatalf("同版本应通过：%v", err)
	}
	_, err := CheckHello(env, protocol.Version+1)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("应报 ErrVersionMismatch，实得 %v", err)
	}
}

func TestCheckHelloRejectsNonHello(t *testing.T) {
	env := &protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 1}}}
	_, err := CheckHello(env, protocol.Version)
	if !errors.Is(err, ErrBadHandshake) {
		t.Fatalf("应报 ErrBadHandshake，实得 %v", err)
	}
	if !strings.Contains(err.Error(), "Ping") {
		t.Fatalf("错误信息应带上实际收到什么：%v", err)
	}
}

// 未知消息不是错误：oneof case 未设置 ⇒ 记数 + 告警 + 忽略，禁止 panic（protocol.md §4.1）。
func TestUnknownMessageIsNotAnError(t *testing.T) {
	// 手工造一帧：字段号 0x0F00 不在我们的 schema 里（模拟未来版本新增的消息）。
	// tag 与长度都用 AppendUvarint 表达，避免手写 varint 出错。
	const unknownField = 0x0F00                             // 我们的分段是 0x01xx–0x0Cxx，这落在外面
	payload := binary.AppendUvarint(nil, unknownField<<3|2) // wire type 2 = LEN
	payload = binary.AppendUvarint(payload, 2)
	payload = append(payload, 0xAA, 0xBB)
	var buf bytes.Buffer
	if err := WriteRaw(&buf, payload); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	env, err := Read(&buf)
	if err != nil {
		t.Fatalf("未知消息不应报错：%v", err)
	}
	if env.Body != nil {
		t.Fatalf("未知消息的 oneof 应为未设置，实得 %T", env.Body)
	}
	if MsgName(env) != "" {
		t.Fatalf("未知消息的 MsgName 应为空串")
	}
}

// 超长帧：**先判长度再分配**，别让伪造的长度域逼对方申请 4 GiB。
func TestRejectOversizeBeforeAllocating(t *testing.T) {
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], 0xFFFF_FFFF) // 声明 4 GiB
	_, err := ReadRaw(bytes.NewReader(hdr[:]))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("应报 ErrTooLarge，实得 %v", err)
	}
}

func TestRejectEmptyAndShort(t *testing.T) {
	var hdr [4]byte // 长度 0
	if _, err := ReadRaw(bytes.NewReader(hdr[:])); !errors.Is(err, ErrEmpty) {
		t.Fatalf("长度 0 应报 ErrEmpty，实得 %v", err)
	}
	if _, err := ReadRaw(bytes.NewReader([]byte{1, 2})); !errors.Is(err, ErrShort) {
		t.Fatalf("不足 4 字节应报 ErrShort，实得 %v", err)
	}
	if err := WriteRaw(&bytes.Buffer{}, nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("写空帧应报 ErrEmpty，实得 %v", err)
	}
	if err := WriteRaw(&bytes.Buffer{}, make([]byte, MaxFrame+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("写超长帧应报 ErrTooLarge，实得 %v", err)
	}
}

// ReadRaw 是给 gate 用的：它只认长度域，不认识 Envelope。
func TestReadRawGivesBytesBackUnchanged(t *testing.T) {
	body := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	var buf bytes.Buffer
	if err := WriteRaw(&buf, body); err != nil {
		t.Fatalf("WriteRaw: %v", err)
	}
	got, err := ReadRaw(&buf)
	if err != nil {
		t.Fatalf("ReadRaw: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("裸帧应原样取回：%x ≠ %x", got, body)
	}
}

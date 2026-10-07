// Package frame —— 新协议的**分帧与信封传输**（[docs/protocol.md §2]）。
//
//	frame := [u32 length][Envelope]      // 小端；length 不含自身，单位字节
//
// ⚠️ 这一层是**手写**的，而且**只允许手写这一层**：长度域不属于 schema，
// 消息体一律由 protoc 生成（protocol.md §3 硬规则 1「禁止手写编解码」指的是消息体）。
//
// [docs/protocol.md §2]: ../../../docs/protocol.md
package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/algotao/mir2/server/protocol"
)

// MaxFrame 是单帧上限。超限**立即断开**，不做"尽力而为"（防 DoS，protocol.md §2）。
const MaxFrame = 64 << 10

var (
	// ErrTooLarge 帧长超过 MaxFrame。
	ErrTooLarge = errors.New("frame: 帧超过 64 KiB 上限")
	// ErrShort 连 4 字节长度域都没读全（对端已断开）。
	ErrShort = errors.New("frame: 长度域不完整")
	// ErrEmpty 长度为 0。空帧无意义，视为协议错误而不是"无事发生"。
	ErrEmpty = errors.New("frame: 空帧")
)

// Write 把一个信封按 [u32 长度][Envelope] 写出。
//
// 用**确定性**序列化：黄金报文测试（protocol.md §9.3）要断言输出字节，
// 字节不稳的编码会让那条门禁变成噪声。
func Write(w io.Writer, env *protocol.Envelope) error {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(env)
	if err != nil {
		return fmt.Errorf("frame: 序列化信封: %w", err)
	}
	if len(body) > MaxFrame {
		return fmt.Errorf("%w（%d 字节）", ErrTooLarge, len(body))
	}
	return WriteRaw(w, body)
}

// Read 读一个信封。
//
// ⚠️ 收到**未知消息**时 oneof 的 case 未设置（`Body == nil`）——
// 这**不是错误**，是联邦版本演进时的正常现象：调用方记数 + 告警 + 忽略，
// **禁止 panic**（protocol.md §4.1）。
func Read(r io.Reader) (*protocol.Envelope, error) {
	body, err := ReadRaw(r)
	if err != nil {
		return nil, err
	}
	env := &protocol.Envelope{}
	if err := proto.Unmarshal(body, env); err != nil {
		return nil, fmt.Errorf("frame: 解析信封: %w", err)
	}
	return env, nil
}

// ReadRaw 只取一帧的**裸字节**。
//
// 给 gate 用：按 D-17，gate 是纯字节转发层（接入 / 分帧转发 / 限流 / 连接元数据），
// **不做协议感知** —— 所以它只需要长度域，不需要也不该认识 Envelope。
func ReadRaw(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShort, err)
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	switch {
	case n == 0:
		return nil, ErrEmpty
	case n > MaxFrame:
		// 先判长度再分配：否则一个伪造的长度域就能让对方申请 4 GiB
		return nil, fmt.Errorf("%w（声明 %d 字节）", ErrTooLarge, n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("frame: 帧体不完整（声明 %d 字节）: %w", n, err)
	}
	return body, nil
}

// WriteRaw 写一帧裸字节（转发应答与直接写信封都走它）。
func WriteRaw(w io.Writer, body []byte) error {
	switch {
	case len(body) == 0:
		return ErrEmpty
	case len(body) > MaxFrame:
		return fmt.Errorf("%w（%d 字节）", ErrTooLarge, len(body))
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// MsgName 取一段可读的消息名，供日志与错误信息用
// （protocol.md §4.1：直接打印变体名而不是数字，是选 oneof 的收益之一）。
//
// 用反射取包装类型名，而不是写 65 个 case 的分支表：
// 那样每加一条消息都要来改这里，正是"两处定义"要避免的东西。
// 未知消息（`Body == nil`，即未来版本的新消息）返回空串。
func MsgName(env *protocol.Envelope) string {
	if env == nil || env.Body == nil {
		return ""
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", env.Body), "*protocol.Envelope_")
}

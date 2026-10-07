package frame

import (
	"errors"
	"fmt"
	"time"

	"github.com/algotao/mir2/server/protocol"
)

var (
	// ErrBadHandshake 连接的第一条消息不是 ClientHello。
	ErrBadHandshake = errors.New("frame: 握手消息不对")
	// ErrVersionMismatch 协议版本不匹配。
	ErrVersionMismatch = errors.New("frame: 协议版本不匹配")
)

// CheckHello 校验客户端握手（protocol.md §5）。
//
// 版本不匹配**明确拒绝**（返回 ErrVersionMismatch，调用方回 ServerError 后断开）。
// protocol.md 写得很清楚：不做"尽力而为"——两端版本不一致时继续跑，
// 只会变成难以定位的静默错乱，而版本协商本来就是防双实现漂移的四道防线之一（§9.4）。
func CheckHello(env *protocol.Envelope, serverVer uint32) (*protocol.ClientHello, error) {
	hello, ok := env.Body.(*protocol.Envelope_ClientHello)
	if !ok {
		return nil, fmt.Errorf("%w：连接的第一条必须是 ClientHello，收到 %q",
			ErrBadHandshake, MsgName(env))
	}
	if v := hello.ClientHello.GetProtocolVersion(); v != serverVer {
		return nil, fmt.Errorf("%w：客户端 %d ≠ 服务端 %d（重新生成两端代码或升/降客户端）",
			ErrVersionMismatch, v, serverVer)
	}
	return hello.ClientHello, nil
}

// NewServerHello 构造握手应答。
//
// `seq` 不由这里填 —— 它是**发送方**的发包计数器（protocol.md §2），
// 属于会话状态，不该由消息构造函数猜。
func NewServerHello(serverVer uint32, sessionKey []byte, caps []string) *protocol.Envelope {
	return &protocol.Envelope{
		Body: &protocol.Envelope_ServerHello{
			ServerHello: &protocol.ServerHello{
				ProtocolVersion: serverVer,
				ServerTimeMs:    time.Now().UnixMilli(),
				SessionKey:      sessionKey,
				Capabilities:    caps,
			},
		},
	}
}

// NewPong 回应心跳。
func NewPong(clientTimeMs uint64) *protocol.Envelope {
	return &protocol.Envelope{
		Body: &protocol.Envelope_Pong{
			Pong: &protocol.Pong{
				ClientTimeMs: clientTimeMs,
				ServerTimeMs: uint64(time.Now().UnixMilli()),
			},
		},
	}
}

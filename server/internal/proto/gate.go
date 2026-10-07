package proto

import "encoding/binary"

// RunGateCode 是网关帧的魔数（TMsgHeader.dwCode）。
// 对应 Delphi Common/Grobal2.pas:1012。
const RunGateCode = 0xAA55AA55

// MsgHeaderSize 是 TMsgHeader 的二进制长度（字节）。
//
// 对应 Delphi Common/Grobal2.pas:1036-1043。该 record 非 packed：
//
//	dwCode:DWord(4) nSocket:Integer(4) wGSocketIdx:Word(2) wIdent:Word(2)
//	wUserListIndex:Word(2) <2 字节填充> nLength:Integer(4)
//
// ⚠️ wUserListIndex 后有 2 字节对齐填充，故是 20 字节而非 18。
const MsgHeaderSize = 20

// GM_* 是 M2Server ↔ RunGate 的帧类型（TMsgHeader.wIdent）。
const (
	GM_OPEN            = 1 // 客户端接入，负载为 RemoteAddress + '\0'
	GM_CLOSE           = 2 // 客户端断开
	GM_CHECKSERVER     = 3 // M2Server → 网关，心跳应答
	GM_CHECKCLIENT     = 4 // 网关 → M2Server，心跳（每 2s）
	GM_DATA            = 5 // 业务数据
	GM_SERVERUSERINDEX = 6
	GM_RECEIVE_OK      = 7  // 发送窗口 ACK（流控）
	GM_TEST            = 20 // 压力测试包
)

// MsgHeader 是网关与 M2Server 之间的二进制帧头。
type MsgHeader struct {
	Code          uint32  // 必须等于 RunGateCode
	Socket        int32   // 客户端 SocketHandle
	GSocketIdx    uint16  // 网关侧会话下标
	Ident         uint16  // GM_*
	UserListIndex uint16  // M2Server UserList 索引（+1 语义）
	_             [2]byte // Delphi 对齐填充，恒为 0
	Length        int32   // 负载长度；< 0 表示纯字符串负载（无 DefaultMessage）
}

// PayloadLen 返回负载的绝对长度。
func (h MsgHeader) PayloadLen() int {
	if h.Length < 0 {
		return int(-h.Length)
	}
	return int(h.Length)
}

// HasDefaultMessage 报告负载是否以 TDefaultMessage 开头。
//
// Delphi 约定：nLength < 0 时负载是纯字符串（ObjBase.pas:2613 / RunSock.pas:1022）。
func (h MsgHeader) HasDefaultMessage() bool { return h.Length >= 0 }

// Append 把帧头以小端序追加到 dst。
func (h MsgHeader) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, h.Code)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(h.Socket))
	dst = binary.LittleEndian.AppendUint16(dst, h.GSocketIdx)
	dst = binary.LittleEndian.AppendUint16(dst, h.Ident)
	dst = binary.LittleEndian.AppendUint16(dst, h.UserListIndex)
	dst = append(dst, 0, 0) // 对齐填充
	dst = binary.LittleEndian.AppendUint32(dst, uint32(h.Length))
	return dst
}

// DecodeMsgHeader 从至少 20 字节的缓冲解析帧头。
// 魔数不匹配返回 ok=false，由调用方执行逐字节滑窗重同步。
func DecodeMsgHeader(b []byte) (h MsgHeader, ok bool) {
	if len(b) < MsgHeaderSize {
		return h, false
	}
	if binary.LittleEndian.Uint32(b[0:]) != RunGateCode {
		return h, false
	}
	h.Code = RunGateCode
	h.Socket = int32(binary.LittleEndian.Uint32(b[4:]))
	h.GSocketIdx = binary.LittleEndian.Uint16(b[8:])
	h.Ident = binary.LittleEndian.Uint16(b[10:])
	h.UserListIndex = binary.LittleEndian.Uint16(b[12:])
	h.Length = int32(binary.LittleEndian.Uint32(b[16:]))
	return h, true
}

package proto

import "encoding/binary"

// AbilitySize 是下发给客户端的 TAbility 长度（字节）。
//
// 客户端按定长结构直接解码（MirClient/ClMain.pas:4798：
// DecodeBuffer(body, @g_MySelf.m_Abil, sizeof(TAbility))），
// 所以这里必须逐字节对齐 Delphi 的 packed record（Common/Grobal2.pas:734-753）。
const AbilitySize = 50

// Ability 是下发给客户端的能力结构（对应 Delphi TAbility）。
//
// ⚠️ AC/MAC/DC/MC/SC 是 DWord：LoWord=下限，HiWord=上限。
// 与存储层 pb.Ability（拆成 Min/Max）不同，此处必须按原版打包。
type Ability struct {
	Level         uint16
	AC            uint32
	MAC           uint32
	DC            uint32
	MC            uint32
	SC            uint32
	HP            uint16
	MP            uint16
	MaxHP         uint16
	MaxMP         uint16
	Exp           uint32
	MaxExp        uint32
	Weight        uint16
	MaxWeight     uint16
	WearWeight    uint16
	MaxWearWeight uint16
	HandWeight    uint16
	MaxHandWeight uint16
}

// PackMinMax 把下限/上限打包进一个 DWord（LoWord=下限，HiWord=上限）。
func PackMinMax(lo, hi uint16) uint32 { return uint32(lo) | uint32(hi)<<16 }

// UnpackLo / UnpackHi 拆分打包后的 DWord。
func UnpackLo(v uint32) uint16 { return uint16(v & 0xFFFF) }
func UnpackHi(v uint32) uint16 { return uint16(v >> 16) }

// Append 以小端序写入 50 字节。
func (a Ability) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, a.Level)
	dst = binary.LittleEndian.AppendUint32(dst, a.AC)
	dst = binary.LittleEndian.AppendUint32(dst, a.MAC)
	dst = binary.LittleEndian.AppendUint32(dst, a.DC)
	dst = binary.LittleEndian.AppendUint32(dst, a.MC)
	dst = binary.LittleEndian.AppendUint32(dst, a.SC)
	dst = binary.LittleEndian.AppendUint16(dst, a.HP)
	dst = binary.LittleEndian.AppendUint16(dst, a.MP)
	dst = binary.LittleEndian.AppendUint16(dst, a.MaxHP)
	dst = binary.LittleEndian.AppendUint16(dst, a.MaxMP)
	dst = binary.LittleEndian.AppendUint32(dst, a.Exp)
	dst = binary.LittleEndian.AppendUint32(dst, a.MaxExp)
	dst = binary.LittleEndian.AppendUint16(dst, a.Weight)
	dst = binary.LittleEndian.AppendUint16(dst, a.MaxWeight)
	dst = binary.LittleEndian.AppendUint16(dst, a.WearWeight)
	dst = binary.LittleEndian.AppendUint16(dst, a.MaxWearWeight)
	dst = binary.LittleEndian.AppendUint16(dst, a.HandWeight)
	dst = binary.LittleEndian.AppendUint16(dst, a.MaxHandWeight)
	return dst
}

// Bytes 返回 50 字节编码。
func (a Ability) Bytes() [AbilitySize]byte {
	var b [AbilitySize]byte
	a.Append(b[:0])
	return b
}

// DecodeAbility 解析客户端结构。
func DecodeAbility(b []byte) (a Ability, ok bool) {
	if len(b) < AbilitySize {
		return a, false
	}
	a.Level = binary.LittleEndian.Uint16(b[0:])
	a.AC = binary.LittleEndian.Uint32(b[2:])
	a.MAC = binary.LittleEndian.Uint32(b[6:])
	a.DC = binary.LittleEndian.Uint32(b[10:])
	a.MC = binary.LittleEndian.Uint32(b[14:])
	a.SC = binary.LittleEndian.Uint32(b[18:])
	a.HP = binary.LittleEndian.Uint16(b[22:])
	a.MP = binary.LittleEndian.Uint16(b[24:])
	a.MaxHP = binary.LittleEndian.Uint16(b[26:])
	a.MaxMP = binary.LittleEndian.Uint16(b[28:])
	a.Exp = binary.LittleEndian.Uint32(b[30:])
	a.MaxExp = binary.LittleEndian.Uint32(b[34:])
	a.Weight = binary.LittleEndian.Uint16(b[38:])
	a.MaxWeight = binary.LittleEndian.Uint16(b[40:])
	a.WearWeight = binary.LittleEndian.Uint16(b[42:])
	a.MaxWearWeight = binary.LittleEndian.Uint16(b[44:])
	a.HandWeight = binary.LittleEndian.Uint16(b[46:])
	a.MaxHandWeight = binary.LittleEndian.Uint16(b[48:])
	return a, true
}

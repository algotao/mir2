package proto

import "encoding/binary"

// MessageBodyWLSize 是 TMessageBodyWL 长度（字节）。
//
// 对应 Delphi Common/Grobal2.pas:712-717（注释标 //16 0x10）。
// SM_LOGON 的正文就是它（MirClient/ClMain.pas:4441 用 sizeof(TMessageBodyWL) 解码）。
const MessageBodyWLSize = 16

// MessageBodyWL 是随消息下发的 4 个整型载荷。
//
// 在 SM_LOGON 中的语义（ClMain.pas:4442-4451）：
//
//	Param1 = 外观 Feature（由 Race/Weapon/Hair/Dress 位域合成）
//	Param2 = CharStatus（状态位）
//	Tag1   = LoByte(LoWord) 为 1 表示允许组队，高位为 FeatureEx
//	Tag2   = 未使用
type MessageBodyWL struct {
	Param1 int32
	Param2 int32
	Tag1   int32
	Tag2   int32
}

// Append 以小端序写入 16 字节。
func (m MessageBodyWL) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Param1))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Param2))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Tag1))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(m.Tag2))
	return dst
}

// Bytes 返回 16 字节编码。
func (m MessageBodyWL) Bytes() [MessageBodyWLSize]byte {
	var b [MessageBodyWLSize]byte
	m.Append(b[:0])
	return b
}

// DecodeMessageBodyWL 解析正文。
func DecodeMessageBodyWL(b []byte) (m MessageBodyWL, ok bool) {
	if len(b) < MessageBodyWLSize {
		return m, false
	}
	m.Param1 = int32(binary.LittleEndian.Uint32(b[0:]))
	m.Param2 = int32(binary.LittleEndian.Uint32(b[4:]))
	m.Tag1 = int32(binary.LittleEndian.Uint32(b[8:]))
	m.Tag2 = int32(binary.LittleEndian.Uint32(b[12:]))
	return m, true
}

// CharDescSize 是 TCharDesc 长度（字节）。
//
// SM_TURN 的正文就是它（MirClient/ClMain.pas:4503 用
// DecodeBuffer(body, @desc, sizeof(TCharDesc)) 取 Feature 与 Status）。
const CharDescSize = 8

// CharDesc 是实体外观描述（对应 Delphi TCharDesc）。
type CharDesc struct {
	Feature int32
	Status  int32
}

// Append 以小端序写入 8 字节。
func (c CharDesc) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(c.Feature))
	dst = binary.LittleEndian.AppendUint32(dst, uint32(c.Status))
	return dst
}

// DecodeCharDesc 解析外观描述。
func DecodeCharDesc(b []byte) (c CharDesc, ok bool) {
	if len(b) < CharDescSize {
		return c, false
	}
	c.Feature = int32(binary.LittleEndian.Uint32(b[0:]))
	c.Status = int32(binary.LittleEndian.Uint32(b[4:]))
	return c, true
}

// MakeFeature 合成角色外观位域。
//
// 32 位布局（Common/Grobal2.pas:2663-2671）：
//
//	低 16 位 = MakeWord(raceImg, weapon) —— 低字节 Race，高字节 Weapon
//	高 16 位 = MakeWord(hair, dress)     —— 低字节 Hair，高字节 Dress
func MakeFeature(raceImg, weapon, hair, dress uint8) int32 {
	return int32(MakeLong(
		uint16(raceImg)|uint16(weapon)<<8,
		uint16(hair)|uint16(dress)<<8,
	))
}

// FeatureRace / FeatureWeapon / FeatureHair / FeatureDress 拆解外观位域。
func FeatureRace(f int32) uint8   { return uint8(LoWord(f) & 0xFF) }
func FeatureWeapon(f int32) uint8 { return uint8(LoWord(f) >> 8) }
func FeatureHair(f int32) uint8   { return uint8(HiWord(f) & 0xFF) }
func FeatureDress(f int32) uint8  { return uint8(HiWord(f) >> 8) }

// MakeFeatureEx 合成扩展外观（高 16 位用于坐骑/时装等，复古版传 0 即可）。
func MakeFeatureEx(allowGroup bool) int32 {
	if allowGroup {
		return 1 // LoByte(LoWord(Tag1)) == 1
	}
	return 0
}

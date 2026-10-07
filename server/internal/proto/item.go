package proto

import "encoding/binary"

// 物品相关的客户端定长结构。
//
// 客户端直接按 sizeof(...) 解码（如 SM_BAGITEMS 的 ClientGetBagItmes
// 用 DecodeClientBuffer<ClientItem>），因此字节布局必须与 Delphi 一致。
//
// ⚠️ 三类结构的对齐规则不同，不可混用：
//
//	TStdItem   packed  → 66 字节，无填充
//	TUserItem  packed  → 24 字节，无填充（落盘用）
//	TClientItem 非 packed → 76 字节，MakeIndex 前有 2 字节对齐填充
const (
	// StdItemSize 是 TStdItem 长度（Common/Grobal2.pas:540-560）。
	StdItemSize = 66
	// UserItemSize 是 TUserItem 长度（:788-795，packed）。
	UserItemSize = 24
	// ClientItemSize 是 TClientItem 长度（:562-568，非 packed）。
	ClientItemSize = 76
	// ItemNameLen 是物品名的短字符串容量（String[20] → 21 字节）。
	ItemNameLen = 20
)

// StdItem 是物品模板（对应 Delphi TStdItem，packed 66 字节）。
//
// AC/MAC/DC/MC/SC 是 DWord：LoWord=下限，HiWord=上限。
// 武器特例：AC 高位=准确、低位=幸运；MAC 高位=速度、低位=诅咒。
type StdItem struct {
	Name         [ItemNameLen + 1]byte // String[20]：1 字节长度 + 内容
	StdMode      uint8
	Shape        uint8
	Weight       uint8
	AniCount     uint8
	Source       int8 // 武器神圣值
	Reserved     uint8
	NeedIdentify uint8
	Looks        uint16 // Items.WIL 图片索引
	DuraMax      uint32
	AC           uint32
	MAC          uint32
	DC           uint32
	MC           uint32
	SC           uint32
	Need         uint32 // 0=等级 1=攻击力 2=魔法力 3=精神力
	NeedLevel    uint32
	Price        uint32
}

// SetName 写入短字符串形式的物品名。
func (s *StdItem) SetName(name string) {
	PutShortString(s.Name[:], ItemNameLen, name)
}

// GetName 读取物品名。
func (s *StdItem) GetName() string { return ShortString(s.Name[:]) }

// Append 以小端序写入 66 字节。
func (s StdItem) Append(dst []byte) []byte {
	dst = append(dst, s.Name[:]...)
	dst = append(dst,
		s.StdMode, s.Shape, s.Weight, s.AniCount,
		byte(s.Source), s.Reserved, s.NeedIdentify)
	dst = binary.LittleEndian.AppendUint16(dst, s.Looks)
	dst = binary.LittleEndian.AppendUint32(dst, s.DuraMax)
	dst = binary.LittleEndian.AppendUint32(dst, s.AC)
	dst = binary.LittleEndian.AppendUint32(dst, s.MAC)
	dst = binary.LittleEndian.AppendUint32(dst, s.DC)
	dst = binary.LittleEndian.AppendUint32(dst, s.MC)
	dst = binary.LittleEndian.AppendUint32(dst, s.SC)
	dst = binary.LittleEndian.AppendUint32(dst, s.Need)
	dst = binary.LittleEndian.AppendUint32(dst, s.NeedLevel)
	dst = binary.LittleEndian.AppendUint32(dst, s.Price)
	return dst
}

// DecodeStdItem 解析物品模板。
func DecodeStdItem(b []byte) (s StdItem, ok bool) {
	if len(b) < StdItemSize {
		return s, false
	}
	copy(s.Name[:], b[0:ItemNameLen+1])
	s.StdMode = b[21]
	s.Shape = b[22]
	s.Weight = b[23]
	s.AniCount = b[24]
	s.Source = int8(b[25])
	s.Reserved = b[26]
	s.NeedIdentify = b[27]
	s.Looks = binary.LittleEndian.Uint16(b[28:])
	s.DuraMax = binary.LittleEndian.Uint32(b[30:])
	s.AC = binary.LittleEndian.Uint32(b[34:])
	s.MAC = binary.LittleEndian.Uint32(b[38:])
	s.DC = binary.LittleEndian.Uint32(b[42:])
	s.MC = binary.LittleEndian.Uint32(b[46:])
	s.SC = binary.LittleEndian.Uint32(b[50:])
	s.Need = binary.LittleEndian.Uint32(b[54:])
	s.NeedLevel = binary.LittleEndian.Uint32(b[58:])
	s.Price = binary.LittleEndian.Uint32(b[62:])
	return s, true
}

// UserItem 是玩家持有的物品实例（对应 TUserItem，packed 24 字节）。
//
// 这是**落盘**用的紧凑结构：只存模板索引与耐久，属性查表得到。
type UserItem struct {
	MakeIndex int32  // 物品唯一 ID（GetItemNumber 分配）
	Index     uint16 // 物品模板索引（1-based）
	Dura      uint16
	DuraMax   uint16
	Value     [14]byte // 附加属性与升级点数
}

// Append 以小端序写入 24 字节。
func (u UserItem) Append(dst []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(u.MakeIndex))
	dst = binary.LittleEndian.AppendUint16(dst, u.Index)
	dst = binary.LittleEndian.AppendUint16(dst, u.Dura)
	dst = binary.LittleEndian.AppendUint16(dst, u.DuraMax)
	dst = append(dst, u.Value[:]...)
	return dst
}

// DecodeUserItem 解析物品实例。
func DecodeUserItem(b []byte) (u UserItem, ok bool) {
	if len(b) < UserItemSize {
		return u, false
	}
	u.MakeIndex = int32(binary.LittleEndian.Uint32(b[0:]))
	u.Index = binary.LittleEndian.Uint16(b[4:])
	u.Dura = binary.LittleEndian.Uint16(b[6:])
	u.DuraMax = binary.LittleEndian.Uint16(b[8:])
	copy(u.Value[:], b[10:24])
	return u, true
}

// ClientItem 是下发给客户端的物品（对应 TClientItem，非 packed 76 字节）。
//
// ⚠️ 非 packed：S 占 66 字节后，MakeIndex(int32) 需对齐到 4 字节边界，
// 故偏移 66..67 是 2 字节填充，MakeIndex 在 68。
// 漏掉填充会导致整个背包解析错位。
type ClientItem struct {
	S         StdItem // 66 字节
	_         [2]byte // 对齐填充
	MakeIndex int32   // @68
	Dura      uint16  // @72
	DuraMax   uint16  // @74
}

// Append 以小端序写入 76 字节。
func (c ClientItem) Append(dst []byte) []byte {
	dst = c.S.Append(dst)
	dst = append(dst, 0, 0) // 对齐填充
	dst = binary.LittleEndian.AppendUint32(dst, uint32(c.MakeIndex))
	dst = binary.LittleEndian.AppendUint16(dst, c.Dura)
	dst = binary.LittleEndian.AppendUint16(dst, c.DuraMax)
	return dst
}

// DecodeClientItem 解析客户端物品结构。
func DecodeClientItem(b []byte) (c ClientItem, ok bool) {
	if len(b) < ClientItemSize {
		return c, false
	}
	if c.S, ok = DecodeStdItem(b[0:66]); !ok {
		return c, false
	}
	c.MakeIndex = int32(binary.LittleEndian.Uint32(b[68:]))
	c.Dura = binary.LittleEndian.Uint16(b[72:])
	c.DuraMax = binary.LittleEndian.Uint16(b[74:])
	return c, true
}

// PutShortString 把 Go 字符串写入 Delphi String[N] 形式的缓冲区。
//
// 供本包内部使用（与 codec.PutShortString 同逻辑，避免循环依赖）。
func PutShortString(dst []byte, n int, s string) {
	if len(dst) < n+1 {
		return
	}
	for i := range dst[:n+1] {
		dst[i] = 0
	}
	b := []byte(s)
	if len(b) > n {
		b = b[:n]
	}
	copy(dst[1:], b)
	dst[0] = byte(len(b))
}

// ShortString 从 String[N] 缓冲区读取内容。
func ShortString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	n := int(b[0])
	if n > len(b)-1 {
		n = len(b) - 1
	}
	return string(b[1 : 1+n])
}

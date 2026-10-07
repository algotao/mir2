// Package codec 实现 MIR2 线上字节流的编解码。
//
// 移植自 Delphi 源码 /data/git/MIR2/GameOfMir/Common/EDcode.pas。
//
// ⚠️ 这不是 base64，是 MIR2 私有编码。客户端侧必须字节级兼容，
// 任何改动都会导致原版客户端无法通信。详见 docs/service-architecture.md §5。
package codec

// Base6Offset 是 6bit 编码输出字符的偏移量。
//
// 每个 6bit 组加上该值后得到一个可打印 ASCII 字符：
// 取值范围 [0x3C, 0x7B]，即 '`' 之前的可打印区（含 '<' '=' '>' '?' '@' A-Z [ \ ] ^ _ ` a-z { ）。
const Base6Offset = 0x3C

// EncodedLen 返回 n 字节编码后需要的字符数（不含结尾 '\0'）。
//
// 等价于 Delphi 侧的 ceil(n*8/6) = ceil(n*4/3)。
// 例如 TDefaultMessage(12 字节) → 16 字符，即 DEFBLOCKSIZE。
func EncodedLen(n int) int {
	if n <= 0 {
		return 0
	}
	return (n*4 + 2) / 3
}

// DecodedLen 返回 n 个编码字符解码后得到的字节数上界。
func DecodedLen(n int) int {
	if n <= 0 {
		return 0
	}
	return n * 3 / 4
}

// Encode6BitBuf 把 src 按 6bit 分组编码，每组加 Base6Offset 输出为可打印 ASCII。
//
// 对应 Delphi Common/EDcode.pas:94 Encode6BitBuf。
//
// 关键：ENDECODEMODE = OLDMODE（EDcode.pas:11），因此 EncodeBitMasks 表与
// n4CEEF4/n4CEEF8/w4CEF00/n4CEEFC 四个魔法常量在编译期被剔除，
// 线上跑的是**纯 6bit + 0x3C** 的变换。本实现只覆盖 OLDMODE。
//
// 输出长度 = EncodedLen(len(src))。
func Encode6BitBuf(src []byte) []byte {
	dest := make([]byte, 0, EncodedLen(len(src)))
	var (
		nRestCount int  // 已借用的"剩余位"计数，步进 2
		btRest     byte // 上一字节遗留、待拼入下一个 6bit 组的位
	)
	for _, btCh := range src {
		// 本次输出 = 上次遗留位 | 当前字节右移后剩下的高位
		btMade := (btRest | (btCh >> (2 + nRestCount))) & 0x3F
		// 遗留位：当前字节的低位左移对齐，再右移 2 位归一。
		// ⚠️ Delphi 中 btRest 是 Byte，左移溢出会截断到 8 位；Go 必须显式截断，
		// 否则会多保留高位导致编码错误。
		btRest = (byte(btCh<<(8-(2+nRestCount))) >> 2) & 0x3F

		nRestCount += 2
		if nRestCount < 6 {
			dest = append(dest, btMade+Base6Offset)
		} else {
			// 凑满 8 位：本次与遗留位各产出一个字符
			dest = append(dest, btMade+Base6Offset)
			dest = append(dest, btRest+Base6Offset)
			nRestCount = 0
			btRest = 0
		}
	}
	if nRestCount > 0 {
		dest = append(dest, btRest+Base6Offset)
	}
	return dest
}

// bit6Masks 对应 Delphi EDcode.pas Decode6BitBuf 中的 Masks: array[2..6]。
//
// 索引即 nBitPos，取值 ($FC, $F8, $F0, $E0, $C0)。
var bit6Masks = [7]byte{0x00, 0x00, 0xFC, 0xF8, 0xF0, 0xE0, 0xC0}

// Decode6BitBuf 把 Encode6BitBuf 的输出还原为原始字节。
//
// 对应 Delphi Common/EDcode.pas:150 Decode6BitBuf。
//
// 遇到 < Base6Offset 的字符时，Delphi 侧执行 `nBufPos := 0; break`，
// 即**整包判为无效**（而非跳过该字符）。本函数返回 nil 表达该语义。
func Decode6BitBuf(src []byte) []byte {
	buf := make([]byte, 0, DecodedLen(len(src))+1)
	var (
		nBitPos  = 2 // 当前遗留位在字节中的起始位，取值 2/4/6
		nMadeBit int // 已凑齐但未输出的位数
		btTmp    byte
	)
	for _, c := range src {
		if c < Base6Offset {
			return nil
		}
		btCh := c - Base6Offset
		if nMadeBit+6 >= 8 {
			btByte := btTmp | ((btCh & 0x3F) >> (6 - nBitPos))
			buf = append(buf, btByte)
			nMadeBit = 0
			if nBitPos < 6 {
				nBitPos += 2
			} else {
				nBitPos = 2
				continue // Delphi: continue —— 跳过下面的 btTmp 赋值
			}
		}
		btTmp = byte(btCh<<nBitPos) & bit6Masks[nBitPos]
		nMadeBit += 8 - nBitPos
	}
	return buf
}

// EncodeString 把字符串（视为字节序列）编码为 6bit 字符串。
// 对应 Delphi EDcode.pas EncodeString。
func EncodeString(s string) string {
	return string(Encode6BitBuf([]byte(s)))
}

// DecodeString 还原 EncodeString 的输出。
// 对应 Delphi EDcode.pas DecodeString。
func DecodeString(s string) string {
	return string(Decode6BitBuf([]byte(s)))
}

// EncodeBuffer 编码任意二进制缓冲，语义同 Encode6BitBuf，保留以对应 Delphi API。
func EncodeBuffer(b []byte) []byte {
	return Encode6BitBuf(b)
}

// DecodeBuffer 解码，语义同 Decode6BitBuf，保留以对应 Delphi API。
func DecodeBuffer(b []byte) []byte {
	return Decode6BitBuf(b)
}

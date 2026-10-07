package codec

// Delphi 的 String[N]（短字符串）在 record 中占 N+1 字节：
// 第 0 字节是长度，其后 N 字节是内容，未用部分填 0。
//
// 这是所有落盘结构（THumDataInfo / TAccountDBRecord / TStdItem ...）与
// 线上结构（TUserEntry / TUserEntryAdd ...）的基础，必须精确复刻。
//
// ⚠️ 原数据为 GBK/ANSI 编码，Go 侧统一 UTF-8。本文件只做**字节搬运**，
// 不做字符集转换——转码由上层在读入/写出时决定，避免中间过程二次损坏。

// ShortStringSize 返回 Delphi String[N] 在 record 中占用的字节数。
func ShortStringSize(n int) int { return n + 1 }

// PutShortString 把 s 的字节写入 dst（需容纳 n+1 字节），返回写入的字节数。
//
// 超出 n 的部分被截断；不足部分补 0。返回值为 n+1。
func PutShortString(dst []byte, n int, s string) int {
	size := n + 1
	if len(dst) < size {
		panic("codec: PutShortString 目标缓冲过小")
	}
	dst[0] = 0
	copy(dst[1:size], []byte(nil))
	b := []byte(s)
	if len(b) > n {
		b = b[:n]
	}
	copy(dst[1:], b)
	dst[0] = byte(len(b))
	return size
}

// GetShortString 读取 Delphi String[N] 的字节形式，返回内容字节（不含长度字节）。
func GetShortString(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	n := int(b[0])
	if n > len(b)-1 {
		n = len(b) - 1
	}
	return b[1 : 1+n]
}

// ShortString 从 b 中读取并返回 Go string（原样字节，未转码）。
func ShortString(b []byte) string {
	return string(GetShortString(b))
}

// ReadShortString 从 b 的 off 处读取一个 Delphi String[N]，
// 返回内容与下一个字段的偏移。越界返回 ok=false。
func ReadShortString(b []byte, off, n int) (s string, next int, ok bool) {
	if off < 0 || off+n+1 > len(b) {
		return "", off, false
	}
	return ShortString(b[off : off+n+1]), off + n + 1, true
}

// CStyle 读取以 0 结尾的 C 风格字符串（Delphi PChar），返回内容与下一个偏移。
//
// 网关帧的负载（如 GM_OPEN 携带的 RemoteAddress）使用这种形式。
func CStyle(b []byte, off int) (s string, next int, ok bool) {
	for i := off; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[off:i]), i + 1, true
		}
	}
	return "", off, false
}

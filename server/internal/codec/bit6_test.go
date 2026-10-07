package codec

import (
	"math/rand"
	"testing"
)

// TestEncodedLen 校验编码长度公式 ceil(n*4/3)。
// 关键样本：TDefaultMessage(12 字节) 必须得到 DEFBLOCKSIZE=16。
func TestEncodedLen(t *testing.T) {
	cases := map[int]int{
		1:  2,
		2:  3,
		3:  4,
		4:  6,
		6:  8,
		12: 16, // DEFBLOCKSIZE
		24: 32,
	}
	for n, want := range cases {
		if got := EncodedLen(n); got != want {
			t.Errorf("EncodedLen(%d) = %d, want %d", n, got, want)
		}
		if got := len(Encode6BitBuf(make([]byte, n))); got != want {
			t.Errorf("len(Encode6BitBuf(%d bytes)) = %d, want %d", n, got, want)
		}
	}
}

// TestBit6KnownVector 用手工推导的向量对拍。
//
// 输入 0x12 0x34 0x56 (00010010 00110100 01010110)
//
//	C0 = 0x12>>2                = 0b000100 =  4 → ' @'
//	C1 = (0x12&3)<<4 | 0x34>>4  = 0b100011 = 35 → '_'
//	C2 = (0x34&0xF)<<2 | 0x56>>6= 0b010001 = 17 → 'M'
//	C3 = 0x56 & 0x3F            = 0b010110 = 22 → 'R'
func TestBit6KnownVector(t *testing.T) {
	in := []byte{0x12, 0x34, 0x56}
	want := "@_MR"

	got := string(Encode6BitBuf(in))
	if got != want {
		t.Fatalf("Encode6BitBuf = %q, want %q", got, want)
	}
	if back := Decode6BitBuf([]byte(want)); string(back) != string(in) {
		t.Fatalf("Decode6BitBuf(%q) = %x, want %x", want, back, in)
	}
}

// TestBit6Exhaustive 遍历全部字节值与全部长度，验证编解码可逆。
func TestBit6Exhaustive(t *testing.T) {
	for n := 1; n <= 64; n++ {
		in := make([]byte, n)
		for i := range in {
			in[i] = byte(i * 7 % 256)
		}
		enc := Encode6BitBuf(in)
		if len(enc) != EncodedLen(n) {
			t.Fatalf("n=%d: 编码长度 %d, want %d", n, len(enc), EncodedLen(n))
		}
		dec := Decode6BitBuf(enc)
		if string(dec) != string(in) {
			t.Fatalf("n=%d: 往返不一致\nin =%x\ndec=%x", n, in, dec)
		}
		// 所有输出字符必须落在可打印区间，否则客户端会解析失败
		for _, c := range enc {
			if c < Base6Offset || c > Base6Offset+0x3F {
				t.Fatalf("n=%d: 输出字符 %q(0x%02X) 越界", n, c, c)
			}
		}
	}
}

// TestBit6ZeroAndFF 边界字节模式。
func TestBit6ZeroAndFF(t *testing.T) {
	for _, fill := range []byte{0x00, 0xFF, 0x80, 0x7F} {
		in := make([]byte, 9)
		for i := range in {
			in[i] = fill
		}
		if dec := Decode6BitBuf(Encode6BitBuf(in)); string(dec) != string(in) {
			t.Errorf("fill=0x%02X 往返失败: %x", fill, dec)
		}
	}
}

// TestBit6Random 随机往返。
func TestBit6Random(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		n := r.Intn(300) + 1
		in := make([]byte, n)
		r.Read(in)
		dec := Decode6BitBuf(Encode6BitBuf(in))
		if string(dec) != string(in) {
			t.Fatalf("n=%d 往返失败", n)
		}
	}
}

// TestBit6InvalidChar 校验「低于 Base6Offset 的字符导致整包无效」这一 Delphi 语义。
//
// 对应 EDcode.pas:166-169：遇到 src[i]-$3C < 0 时 nBufPos := 0; break。
// 注意不是跳过该字符，而是判定整包无效。
func TestBit6InvalidChar(t *testing.T) {
	valid := Encode6BitBuf([]byte{1, 2, 3})
	if Decode6BitBuf(valid) == nil {
		t.Fatal("合法输入不应返回 nil")
	}
	bad := append([]byte(nil), valid...)
	bad[1] = Base6Offset - 1 // 制造非法字符
	if got := Decode6BitBuf(bad); got != nil {
		t.Fatalf("非法字符应返回 nil, got %x", got)
	}
}

// TestStringAPI 校验 EncodeString/DecodeString 与 Buffer 版本一致。
func TestStringAPI(t *testing.T) {
	const s = "test/12345"
	if DecodeString(EncodeString(s)) != s {
		t.Fatal("EncodeString/DecodeString 往返失败")
	}
	if string(DecodeBuffer(EncodeBuffer([]byte(s)))) != s {
		t.Fatal("EncodeBuffer/DecodeBuffer 往返失败")
	}
}

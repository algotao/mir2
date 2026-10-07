package codec

import "testing"

// TestShortString 校验 Delphi String[N] 的读写。
func TestShortString(t *testing.T) {
	buf := make([]byte, ShortStringSize(16)) // String[16] → 17 字节

	n := PutShortString(buf, 16, " algotao")
	if n != 17 {
		t.Fatalf("PutShortString 返回 %d, want 17", n)
	}
	if buf[0] != 8 {
		t.Fatalf("长度字节 = %d, want 8", buf[0])
	}
	if ShortString(buf) != " algotao" {
		t.Fatalf("ShortString = %q", ShortString(buf))
	}
	// 未用部分必须补 0
	for _, c := range buf[9:] {
		if c != 0 {
			t.Fatal("未用部分未补 0")
		}
	}
}

// TestShortStringTruncate 超长内容必须被截断到 N 字节。
func TestShortStringTruncate(t *testing.T) {
	buf := make([]byte, ShortStringSize(4))
	PutShortString(buf, 4, "abcdefgh")
	if buf[0] != 4 {
		t.Fatalf("长度字节 = %d, want 4", buf[0])
	}
	if ShortString(buf) != "abcd" {
		t.Fatalf("截断结果 = %q", ShortString(buf))
	}
}

// TestReadShortString 校验带偏移的顺序读取（用于解析整个 record）。
func TestReadShortString(t *testing.T) {
	// 模拟 String[16] 后紧跟一个 int32
	buf := make([]byte, 17+4)
	PutShortString(buf[0:17], 16, "mir2go")
	buf[17], buf[18], buf[19], buf[20] = 0x01, 0x02, 0x03, 0x04

	s, next, ok := ReadShortString(buf, 0, 16)
	if !ok || s != "mir2go" || next != 17 {
		t.Fatalf("ReadShortString = %q,%d,%v", s, next, ok)
	}
	if buf[next] != 0x01 {
		t.Fatal("下一个字段偏移错误")
	}

	if _, _, ok := ReadShortString(buf, 15, 16); ok {
		t.Fatal("越界应返回 ok=false")
	}
}

// TestCStyle 校验 PChar 形式（以 0 结尾）的读取。
func TestCStyle(t *testing.T) {
	buf := []byte{'1', '2', '7', '.', '0', '.', '0', '.', '1', 0, 0xFF}
	s, next, ok := CStyle(buf, 0)
	if !ok || s != "127.0.0.1" || next != 10 {
		t.Fatalf("CStyle = %q,%d,%v", s, next, ok)
	}
	if _, _, ok := CStyle([]byte("no-term"), 0); ok {
		t.Fatal("无终止符应返回 ok=false")
	}
}

package proto

import (
	"encoding/binary"
	"testing"
)

// TestStdItemLayout 校验 TStdItem 的字段偏移（packed，无填充）。
func TestStdItemLayout(t *testing.T) {
	var s StdItem
	s.SetName("木剑")
	s.StdMode = 5
	s.Shape = 1
	s.Weight = 8
	s.AniCount = 2
	s.Source = -3
	s.Reserved = 4
	s.NeedIdentify = 1
	s.Looks = 1234
	s.DuraMax = 5000
	s.DC = PackMinMax(3, 7)
	s.Price = 999

	raw := s.Append(nil)
	if len(raw) != StdItemSize {
		t.Fatalf("长度 = %d, want %d", len(raw), StdItemSize)
	}
	// Name: 1 字节长度 + String[20] 内容，位于 0..20
	if raw[0] != byte(len([]byte("木剑"))) {
		t.Errorf("Name 长度字节 = %d", raw[0])
	}
	if binary.LittleEndian.Uint16(raw[28:]) != 1234 {
		t.Errorf("Looks @28 = %d", binary.LittleEndian.Uint16(raw[28:]))
	}
	if binary.LittleEndian.Uint32(raw[42:]) != s.DC {
		t.Errorf("DC @42 = %#x", binary.LittleEndian.Uint32(raw[42:]))
	}

	back, ok := DecodeStdItem(raw)
	if !ok {
		t.Fatal("解码失败")
	}
	if back.GetName() != "木剑" {
		t.Errorf("Name = %q", back.GetName())
	}
	if back.StdMode != 5 || back.Source != -3 || back.Looks != 1234 || back.Price != 999 {
		t.Errorf("字段不一致: %+v", back)
	}
	if UnpackLo(back.DC) != 3 || UnpackHi(back.DC) != 7 {
		t.Errorf("DC 拆包 = %d/%d, want 3/7", UnpackLo(back.DC), UnpackHi(back.DC))
	}
}

// TestStdItemNameTruncate 校验超长名被截断到 20 字节。
func TestStdItemNameTruncate(t *testing.T) {
	var s StdItem
	s.SetName("这是一个非常非常长的物品名称超过二十字节限制")
	if raw := s.Append(nil); raw[0] != ItemNameLen {
		t.Errorf("长度字节 = %d, want %d", raw[0], ItemNameLen)
	}
	back, _ := DecodeStdItem(s.Append(nil))
	if len([]byte(back.GetName())) > ItemNameLen {
		t.Errorf("名字未被截断: %d 字节", len([]byte(back.GetName())))
	}
}

// TestUserItemRoundTrip 校验 TUserItem（packed 24 字节）。
func TestUserItemRoundTrip(t *testing.T) {
	u := UserItem{MakeIndex: 987654, Index: 42, Dura: 1000, DuraMax: 2000}
	u.Value[0] = 1
	u.Value[13] = 0xFF

	raw := u.Append(nil)
	if len(raw) != UserItemSize {
		t.Fatalf("长度 = %d, want %d", len(raw), UserItemSize)
	}
	back, ok := DecodeUserItem(raw)
	if !ok || back != u {
		t.Fatalf("往返不一致:\n got=%+v\nwant=%+v", back, u)
	}
}

// TestClientItemPadding 固化 TClientItem 的对齐填充。
//
// ⚠️ TClientItem **不是 packed**：S 占 66 字节后，MakeIndex(int32) 要对齐到
// 4 字节边界，因此 66..67 是填充、MakeIndex 在 68。
// 若按 packed 写成 66，整个背包都会解析错位——且错位后物品名仍可读，
// 极难察觉，只会表现为"耐久/唯一 ID 乱掉"。
func TestClientItemPadding(t *testing.T) {
	var c ClientItem
	c.S.SetName("金创药(小量)")
	c.S.StdMode = 0
	c.S.Looks = 398
	c.MakeIndex = 123456
	c.Dura = 1
	c.DuraMax = 1

	raw := c.Append(nil)
	if len(raw) != ClientItemSize {
		t.Fatalf("长度 = %d, want %d", len(raw), ClientItemSize)
	}
	if raw[66] != 0 || raw[67] != 0 {
		t.Errorf("填充字节应恒为 0: %d %d", raw[66], raw[67])
	}
	if got := binary.LittleEndian.Uint32(raw[68:]); got != 123456 {
		t.Errorf("MakeIndex @68 = %d, want 123456", got)
	}
	if got := binary.LittleEndian.Uint16(raw[72:]); got != 1 {
		t.Errorf("Dura @72 = %d", got)
	}

	back, ok := DecodeClientItem(raw)
	if !ok {
		t.Fatal("解码失败")
	}
	if back.MakeIndex != 123456 || back.Dura != 1 || back.DuraMax != 1 {
		t.Errorf("往返不一致: %+v", back)
	}
	if back.S.GetName() != "金创药(小量)" {
		t.Errorf("Name = %q", back.S.GetName())
	}
}

func TestItemSizes(t *testing.T) {
	// 三个尺寸的断言：改字段时必须同步更新常量
	var s StdItem
	if n := len(s.Append(nil)); n != StdItemSize {
		t.Errorf("StdItem = %d, want %d", n, StdItemSize)
	}
	var u UserItem
	if n := len(u.Append(nil)); n != UserItemSize {
		t.Errorf("UserItem = %d, want %d", n, UserItemSize)
	}
	var c ClientItem
	if n := len(c.Append(nil)); n != ClientItemSize {
		t.Errorf("ClientItem = %d, want %d", n, ClientItemSize)
	}
}

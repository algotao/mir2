package world

import (
	"testing"
)

// TestHeaderLayout 校验 52 字节头的字段偏移。
func TestHeaderLayout(t *testing.T) {
	m := Generate("test", 4, 3, false)
	m.Header.Title = "比奇省"
	raw := m.Serialize()

	if len(raw) != HeaderSize+4*3*CellSize {
		t.Fatalf("序列化长度 = %d, want %d", len(raw), HeaderSize+4*3*CellSize)
	}
	if raw[4] != byte(len([]byte("比奇省"))) {
		t.Errorf("Title 长度字节 = %d", raw[4])
	}
	if got := string(raw[5 : 5+raw[4]]); got != "比奇省" {
		t.Errorf("Title = %q", got)
	}
}

// TestColumnMajorOrder 固化"文件是列主序"这一关键事实。
//
// 原版（Envir.pas:949）索引公式是 n24 := nW * Header.wHeight，即 x 外层。
// 我们加载时转置为行主序，若转置方向搞反，整张地图会沿对角线镜像。
func TestColumnMajorOrder(t *testing.T) {
	const w, h = 4, 3
	// 手工构造：让 (x=2,y=1) 这一格有唯一可识别的值
	m := Generate("t", w, h, false)
	want := Cell{BkImg: 0x1234, FrImg: 0x5678, Area: 0xAB, Light: 0xCD}
	m.Cells[1*w+2] = want // 行主序：y=1, x=2

	raw := m.Serialize()
	// 列主序下该格的偏移 = 52 + (x*Height + y) * 12
	off := HeaderSize + (2*h+1)*CellSize
	if raw[off] != 0x34 || raw[off+1] != 0x12 {
		t.Fatalf("列主序偏移错误: off=%d bytes=%x %x", off, raw[off], raw[off+1])
	}

	// 重新解析，应回到同一位置
	got, err := Parse("t", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cells[1*w+2] != want {
		t.Errorf("往返后 (2,1) = %+v, want %+v", got.Cells[1*w+2], want)
	}
	// 确认没有沿对角线镜像：宽高不同，镜像后尺寸就错了
	if got.Width() != w || got.Height() != h {
		t.Fatalf("尺寸 = %dx%d, want %dx%d", got.Width(), got.Height(), w, h)
	}
}

// TestRoundTrip 生成 → 序列化 → 解析，逐格比对。
func TestRoundTrip(t *testing.T) {
	src := Generate("盟重省", 17, 13, true)
	src.Header.Title = "盟重省"
	// 撒一些门与随机地形
	src.Cells[3*17+5] = Cell{BkImg: 1, DoorIndex: DoorBit | 3}
	src.Cells[7*17+9] = Cell{FrImg: BlockBit | 42}
	src.Cells[0] = Cell{MidImg: 999, AniFrame: 8, AniTick: 2}

	got, err := Parse("盟重省", src.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Title != src.Header.Title {
		t.Errorf("Title = %q, want %q", got.Header.Title, src.Header.Title)
	}
	for i := range src.Cells {
		if got.Cells[i] != src.Cells[i] {
			t.Fatalf("第 %d 格不一致:\n got=%+v\nwant=%+v", i, got.Cells[i], src.Cells[i])
		}
	}
}

// TestBlocking 校验阻挡位判定与预计算位图一致。
func TestBlocking(t *testing.T) {
	m := Generate("t", 10, 10, true)

	// 边界应阻挡，内部应可通行
	if m.CanWalk(0, 0) || m.CanWalk(9, 9) {
		t.Error("边界应不可通行")
	}
	if !m.CanWalk(5, 5) {
		t.Error("内部应可通行")
	}
	if m.CountBlocked() != 10*10-8*8 {
		t.Errorf("阻挡数 = %d, want %d", m.CountBlocked(), 10*10-8*8)
	}

	// 动态设置阻挡
	m.SetBlock(5, 5, true)
	if m.CanWalk(5, 5) {
		t.Error("SetBlock 未生效")
	}
	if m.Cells[5*10+5].BkImg&BlockBit == 0 {
		t.Error("SetBlock 未同步到 Cell")
	}
	m.SetBlock(5, 5, false)
	if !m.CanWalk(5, 5) {
		t.Error("取消阻挡失败")
	}

	// 越界
	if m.CanWalk(-1, 0) || m.CanWalk(0, 10) || m.CanWalk(100, 100) {
		t.Error("越界坐标不应可通行")
	}
}

// TestCellFlags 校验门与阻挡的位判定。
func TestCellFlags(t *testing.T) {
	c := Cell{BkImg: BlockBit, FrImg: 0, DoorIndex: DoorBit | 0x05}
	if c.CanWalk() {
		t.Error("BkImg 置位应不可通行")
	}
	if !c.IsDoor() {
		t.Error("应识别为门")
	}
	if c.DoorGroup() != 5 {
		t.Errorf("DoorGroup = %d, want 5", c.DoorGroup())
	}

	c2 := Cell{BkImg: 0, FrImg: BlockBit | 7}
	if c2.CanWalk() {
		t.Error("FrImg 置位应不可通行")
	}
	if c2.IsDoor() {
		t.Error("不应识别为门")
	}
}

// TestDetectFormat 校验各变体的魔数判别。
func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want int
	}{
		{"经典格式", makeMapBytes(4, 4), 0},
		{"Wemade Mir3", []byte{0, 0, 1, 2, 3, 4, 5, 6}, 5},
		{"Shanda Mir3", []byte("(C) SNDA, MIR3.xxxx"), 6},
		{"AntiHack", []byte("Mir2 AntiHackxxxx"), 4},
		{"Wemade 2010", []byte("Map 2010 Ver 1.0xx"), 1},
	}
	for _, c := range cases {
		got, err := DetectFormat(c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}

	// Shanda 旧版靠文件大小反推：52 + W*H*14
	w, h := 8, 8
	buf := make([]byte, HeaderSize+w*h*14)
	buf[0], buf[1] = 8, 0
	buf[2], buf[3] = 8, 0
	if got, _ := DetectFormat(buf); got != 2 {
		t.Errorf("Shanda 旧版 = %d, want 2", got)
	}
}

// TestParseErrors 校验异常输入不 panic。
func TestParseErrors(t *testing.T) {
	if _, err := Parse("x", make([]byte, 10)); err == nil {
		t.Error("过短数据应报错")
	}
	// 尺寸为 0
	buf := make([]byte, HeaderSize)
	if _, err := Parse("x", buf); err == nil {
		t.Error("尺寸为 0 应报错")
	}
	// 声明的尺寸与实际长度不符
	buf = make([]byte, HeaderSize)
	buf[0], buf[1] = 100, 0
	buf[2], buf[3] = 100, 0
	if _, err := Parse("x", buf); err == nil {
		t.Error("长度不符应报错")
	}
}

func makeMapBytes(w, h int) []byte {
	return Generate("t", w, h, false).Serialize()
}

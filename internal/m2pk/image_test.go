package m2pk

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSource 是内存里的图库（测编码/解码，不碰磁盘）。
type fakeSource struct {
	recs  []ImageRecord
	raws  [][]byte
	blank map[int]bool // 记成"源里就没有"
}

func (f *fakeSource) Count() int { return len(f.recs) }
func (f *fakeSource) Record(i int) ImageRecord {
	if f.blank[i] {
		return ImageRecord{}
	}
	return f.recs[i]
}
func (f *fakeSource) Raw(i int) ([]byte, error) {
	if f.blank[i] {
		return nil, nil
	}
	return f.raws[i], nil
}

func sampleSource() *fakeSource {
	return &fakeSource{
		recs: []ImageRecord{
			{TypeFlag: 3, Width: 2, Height: 2, AnchorX: 7, AnchorY: -44},
			{}, // 空白图占位（源里 packed_size = 0）
			{TypeFlag: 5, Width: 3, Height: 1},
			{TypeFlag: 3, Width: 5, Height: 3, AnchorX: -1, AnchorY: 2},
			{TypeFlag: 3, Width: 1, Height: 1},
		},
		raws: [][]byte{
			{1, 2, 0, 0, 3, 0, 0, 0},
			nil,
			{0x00, 0xF8, 0xE0, 0x07, 0, 0, 0, 0},
			make([]byte, 24),
			{9, 0, 0, 0},
		},
		blank: map[int]bool{1: true},
	}
}

func TestImageLibRoundTrip(t *testing.T) {
	src := sampleSource()
	payload, st, err := EncodeImageLib(src, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 5 || st.Groups != 3 || st.Blank != 1 || st.Broken != 0 {
		t.Fatalf("统计不对：%+v", st)
	}
	lib, err := OpenImageLib(payload)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Count() != 5 || lib.GroupSize() != 2 || lib.Groups() != 3 {
		t.Fatalf("表头不对：count=%d group=%d groups=%d", lib.Count(), lib.GroupSize(), lib.Groups())
	}
	seen := 0
	err = lib.EachImage(func(i int, rec ImageRecord, raw []byte) error {
		seen++
		if rec != src.Record(i) {
			t.Errorf("第 %d 张记录不同：容器 %+v，源 %+v", i, rec, src.Record(i))
		}
		want, _ := src.Raw(i)
		if !bytes.Equal(want, raw) {
			t.Errorf("第 %d 张 raw 不同：容器 %d 字节，源 %d 字节", i, len(raw), len(want))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 5 {
		t.Fatalf("只遍历到 %d 张", seen)
	}
	// 空白图仍然是空白（与直读 .wzl 的行为一致）
	if !lib.Record(1).Blank() {
		t.Error("第 1 张应该是空白")
	}
}

// stride 公式必须与 Rust 侧一致（`((w*bits + 31) >> 5) * 4`）。
func TestImageRecordStride(t *testing.T) {
	cases := []struct {
		rec  ImageRecord
		want int
	}{
		{ImageRecord{TypeFlag: 3, Width: 2, Height: 2}, 4},
		{ImageRecord{TypeFlag: 3, Width: 5, Height: 3}, 8},
		{ImageRecord{TypeFlag: 3, Width: 1, Height: 1}, 4},
		{ImageRecord{TypeFlag: 5, Width: 3, Height: 1}, 8},
		{ImageRecord{TypeFlag: 5, Width: 8, Height: 2}, 16},
	}
	for _, c := range cases {
		if got := c.rec.Stride(); got != c.want {
			t.Errorf("%+v stride = %d，期望 %d", c.rec, got, c.want)
		}
	}
	// 宽高为 0 ⇒ raw 长度为 0（空白）
	if n := (ImageRecord{TypeFlag: 3, Width: 0, Height: 9}).RawLen(); n != 0 {
		t.Errorf("宽 0 的 raw 长度应为 0，实得 %d", n)
	}
}

// 并行度不影响输出字节（与 Pack 同一纪律）。
func TestEncodeImageLibDeterministic(t *testing.T) {
	src := sampleSource()
	a, _, err := EncodeImageLib(src, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := EncodeImageLib(src, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("同一输入在 1 与 8 个 worker 下产出不同字节")
	}
	// 组大小变化 ⇒ 载荷变化（但都能解出同样的图）
	c, _, err := EncodeImageLib(src, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("组大小 2 与 3 不该产出同样字节")
	}
	lib, err := OpenImageLib(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.EachImage(func(i int, rec ImageRecord, raw []byte) error {
		want, _ := src.Raw(i)
		if !bytes.Equal(want, raw) {
			t.Errorf("组大小 3：第 %d 张 raw 不同", i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestImageLibRejectsCorrupt(t *testing.T) {
	src := sampleSource()
	good, _, err := EncodeImageLib(src, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	// 魔数
	bad := append([]byte(nil), good...)
	copy(bad[0:], "XXXX")
	if _, err := OpenImageLib(bad); err == nil {
		t.Error("魔数不对应该报错")
	}
	// 版本
	bad = append([]byte(nil), good...)
	bad[4] = 99
	if _, err := OpenImageLib(bad); err == nil {
		t.Error("版本不对应该报错")
	}
	// 截断（表区都没放全）
	if _, err := OpenImageLib(good[:ImageHeaderSize+2]); err == nil {
		t.Error("截断的载荷应该报错")
	}
	// 组流被截断 ⇒ 解压或长度校验必须拦住
	if _, err := OpenImageLib(good); err != nil {
		t.Fatal(err)
	}
	cut := append([]byte(nil), good[:len(good)-1]...)
	lib2, err := OpenImageLib(cut)
	if err != nil {
		return // 表区就坏了也算拦住
	}
	if _, err := lib2.Group(lib2.Groups() - 1); err == nil {
		t.Error("末组被截断应该报错")
	}
}

// PackGen 与 Pack 同格式：读回来必须逐块等于生成器给的内容，且确定性。
func TestPackGenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "images.m2pk")
	blocks := map[string][]byte{
		"hum":    []byte("IMGP-ish payload A"),
		"prguse": []byte("payload B"),
	}
	items := []GenItem{
		{Name: "hum", Gen: func() ([]byte, error) { return blocks["hum"], nil }},
		{Name: "prguse", Gen: func() ([]byte, error) { return blocks["prguse"], nil }},
	}
	st, err := PackGen(dst, items, Options{Kind: KindImage, Codec: CodecStore})
	if err != nil {
		t.Fatal(err)
	}
	if st.Count != 2 || st.FileSize <= 0 {
		t.Fatalf("统计不对：%+v", st)
	}
	r, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Kind() != KindImage {
		t.Fatalf("kind = %d，期望 %d", r.Kind(), KindImage)
	}
	if r.Codec() != CodecStore {
		t.Fatalf("codec = %d，期望 store", r.Codec())
	}
	for name, want := range blocks {
		got, ok, err := r.ReadName(name)
		if err != nil || !ok {
			t.Fatalf("读 %s 失败：ok=%v err=%v", name, ok, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s 内容不同", name)
		}
	}
	// 范围读（客户端按范围取表与组）
	e, ok := r.Lookup("prguse")
	if !ok {
		t.Fatal("找不到 prguse")
	}
	part, err := r.ReadRange(e, 4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(part) != "oad" { // "payload B" 的第 4..7 字节
		t.Fatalf("范围读得到 %q", part)
	}
	if _, err := r.ReadRange(e, 0, e.CompSize+1); err == nil {
		t.Error("越界范围应该报错")
	}
	// PackGen 不支持压缩块
	if _, err := PackGen(filepath.Join(dir, "x.m2pk"), items, Options{Kind: KindImage, Codec: CodecBrotli}); err == nil {
		t.Error("PackGen 传 CodecBrotli 应该报错")
	}
	// 名字必须升序
	if _, err := PackGen(filepath.Join(dir, "y.m2pk"), []GenItem{
		{Name: "b", Gen: func() ([]byte, error) { return []byte("b"), nil }},
		{Name: "a", Gen: func() ([]byte, error) { return []byte("a"), nil }},
	}, Options{Kind: KindImage, Codec: CodecStore}); err == nil {
		t.Error("名字未升序应该报错")
	}
	// 确定性
	st2, err := PackGen(filepath.Join(dir, "z.m2pk"), items, Options{Kind: KindImage, Codec: CodecStore})
	if err != nil {
		t.Fatal(err)
	}
	if st2.FileSize != st.FileSize {
		t.Error("两次打包文件大小不同")
	}
	a, _ := os.ReadFile(dst)
	b, _ := os.ReadFile(filepath.Join(dir, "z.m2pk"))
	if !bytes.Equal(a, b) {
		t.Error("两次打包字节不同")
	}
}

// 源侧：合成的 .wzl/.wzx 走完全程，raw 与记录必须原样出来。
func TestWzlSourceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "Hum")
	imgs := []struct {
		typ    uint8
		w, h   uint16
		ax, ay int16
		raw    []byte
		blank  bool // packed_size 写 0
	}{
		{typ: 3, w: 2, h: 2, ax: 7, ay: -44, raw: []byte{1, 2, 0, 0, 3, 0, 0, 0}},
		{typ: 3, w: 4, h: 1, raw: []byte{5, 6, 7, 8}, blank: true},
		{typ: 5, w: 3, h: 1, raw: []byte{0x00, 0xF8, 0xE0, 0x07, 0, 0, 0, 0}},
	}
	var wzl []byte
	wzl = append(wzl, make([]byte, 64)...)
	var offs []uint32
	for _, im := range imgs {
		offs = append(offs, uint32(len(wzl)))
		rec := make([]byte, ImageRecordSize)
		rec[0] = im.typ
		binary.LittleEndian.PutUint16(rec[4:], im.w)
		binary.LittleEndian.PutUint16(rec[6:], im.h)
		binary.LittleEndian.PutUint16(rec[8:], uint16(im.ax))
		binary.LittleEndian.PutUint16(rec[10:], uint16(im.ay))
		var payload []byte
		if !im.blank {
			payload = zlibDeflate(t, im.raw)
		}
		binary.LittleEndian.PutUint32(rec[12:], uint32(len(payload)))
		wzl = append(wzl, rec...)
		wzl = append(wzl, payload...)
	}
	wzx := make([]byte, 48)
	binary.LittleEndian.PutUint32(wzx[44:], uint32(len(imgs)))
	for _, o := range offs {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], o)
		wzx = append(wzx, b[:]...)
	}
	if err := os.WriteFile(basePath+".wzl", wzl, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath+".wzx", wzx, 0o644); err != nil {
		t.Fatal(err)
	}

	src, err := OpenWzlSourcePath(basePath + ".wzl")
	if err != nil {
		t.Fatal(err)
	}
	if src.Count() != 3 {
		t.Fatalf("图数 = %d", src.Count())
	}
	payload, st, err := EncodeImageLib(src, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if st.Blank != 1 {
		t.Fatalf("空白图计数 = %d，期望 1（packed_size = 0）", st.Blank)
	}
	lib, err := OpenImageLib(payload)
	if err != nil {
		t.Fatal(err)
	}
	err = lib.EachImage(func(i int, rec ImageRecord, raw []byte) error {
		want := src.Record(i)
		if rec != want {
			t.Errorf("第 %d 张记录不同：%+v vs %+v", i, rec, want)
		}
		wantRaw, err := src.Raw(i)
		if err != nil {
			return err
		}
		if !bytes.Equal(wantRaw, raw) {
			t.Errorf("第 %d 张 raw 不同", i)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 第 1 张（源里 packed_size = 0）在两边都是空白
	if !lib.Record(1).Blank() {
		t.Error("源里 packed_size=0 的图应归一化为空白")
	}
	if _, err := src.Raw(1); err != nil {
		t.Fatal(err)
	}
	if b, _ := src.Raw(1); b != nil {
		t.Error("空白图的 raw 应为 nil")
	}
}

// synImg 是合成源库的一张图。
type synImg struct {
	typ    uint8
	w, h   uint16
	ax, ay int16
	raw    []byte
	blank  bool // true ⇒ 写成 packed_size = 0 的空白图（源里就没有像素）
}

// writeSyntheticWzl 在 dir 下写一对 `{name}.wzl` + `{name}.wzx`，返回 .wzl 路径。
func writeSyntheticWzl(t *testing.T, dir, name string, imgs []synImg) string {
	t.Helper()
	var wzl []byte
	wzl = append(wzl, make([]byte, 64)...)
	var offs []uint32
	for _, im := range imgs {
		offs = append(offs, uint32(len(wzl)))
		rec := make([]byte, ImageRecordSize)
		rec[0] = im.typ
		binary.LittleEndian.PutUint16(rec[4:], im.w)
		binary.LittleEndian.PutUint16(rec[6:], im.h)
		binary.LittleEndian.PutUint16(rec[8:], uint16(im.ax))
		binary.LittleEndian.PutUint16(rec[10:], uint16(im.ay))
		var payload []byte
		if !im.blank {
			payload = zlibDeflate(t, im.raw)
		}
		binary.LittleEndian.PutUint32(rec[12:], uint32(len(payload)))
		wzl = append(wzl, rec...)
		wzl = append(wzl, payload...)
	}
	wzx := make([]byte, 48)
	binary.LittleEndian.PutUint32(wzx[44:], uint32(len(imgs)))
	for _, o := range offs {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], o)
		wzx = append(wzx, b[:]...)
	}
	path := filepath.Join(dir, name+".wzl")
	if err := os.WriteFile(path, wzl, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".wzx"), wzx, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 端到端：源目录 → 打包（PackGen）→ 逐图回验（VerifyImage）。
func TestVerifyImageEndToEnd(t *testing.T) {
	srcDir := t.TempDir()
	writeSyntheticWzl(t, srcDir, "Hum", []synImg{
		{typ: 3, w: 2, h: 2, ax: 7, ay: -44, raw: []byte{1, 2, 0, 0, 3, 0, 0, 0}},
		{typ: 3, w: 4, h: 1, raw: []byte{5, 6, 7, 8}, blank: true},
	})
	writeSyntheticWzl(t, srcDir, "Items", []synImg{
		{typ: 5, w: 3, h: 1, raw: []byte{0x00, 0xF8, 0xE0, 0x07, 0, 0, 0, 0}},
	})

	srcs, err := ScanDir(srcDir, []string{"wzl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 2 {
		t.Fatalf("扫到 %d 个库", len(srcs))
	}
	items := make([]GenItem, len(srcs))
	for i, s := range srcs {
		i, s := i, s
		items[i] = GenItem{Name: s.Name, Gen: func() ([]byte, error) {
			wz, err := OpenWzlSourcePath(s.Path)
			if err != nil {
				return nil, err
			}
			payload, _, err := EncodeImageLib(wz, 2, 2)
			return payload, err
		}}
	}
	out := filepath.Join(t.TempDir(), "images.m2pk")
	if _, err := PackGen(out, items, Options{Kind: KindImage, Codec: CodecStore}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyImage(out, srcDir); err != nil {
		t.Fatalf("回验应通过：%v", err)
	}
	// 改坏一格 raw ⇒ 必须被抓到
	srcDir2 := t.TempDir()
	writeSyntheticWzl(t, srcDir2, "Hum", []synImg{
		{typ: 3, w: 2, h: 2, ax: 7, ay: -44, raw: []byte{1, 2, 0, 0, 3, 0, 0, 9}},
	})
	if err := VerifyImage(out, srcDir2); err == nil {
		t.Error("源与容器不一致时应该报错")
	}
	// 通用 Verify 对美术容器要明确拒绝（它的块不是源文件字节）
	if err := Verify(out, srcDir, []string{"wzl"}); err == nil {
		t.Error("m2pk.Verify 应该拒绝 kind=3")
	}
}

// TestDumpSamplePayload 打印 `sampleSource()` 的 IMGP 载荷（十六进制）。
//
// 用途：Rust 侧单测**内嵌**这份字节，于是"容器读取"在没构建容器的机器上也能测，
// 而且顺带钉住格式（两端漂移会立刻红）。客户端没有 brotli 编码器（见 Cargo.toml
// 的说明），所以这份载荷只能在 Go 侧产出。
//
//	DUMP_PAYLOAD=1 go test ./internal/m2pk/ -run TestDumpSamplePayload -v
func TestDumpSamplePayload(t *testing.T) {
	if os.Getenv("DUMP_PAYLOAD") == "" {
		t.Skip("设 DUMP_PAYLOAD=1 才打印")
	}
	payload, st, err := EncodeImageLib(sampleSource(), 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("载荷 %d 字节，%+v", len(payload), st)
	var sb strings.Builder
	for i, b := range payload {
		if i%16 == 0 {
			sb.WriteString("\n    ")
		}
		fmt.Fprintf(&sb, "0x%02x, ", b)
	}
	t.Logf("Rust 内嵌字面量：%s", sb.String())
}

func zlibDeflate(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

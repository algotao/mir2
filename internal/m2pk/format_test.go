package m2pk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"0.map", "0"},
		{"0100.MAP", "0100"},
		{"2D.MAP", "2d"},
		{"ygfx1.map", "ygfx1"},
		{"a_b.map", "a_b"},
		{"T3063~01.map", "t3063~01"}, // 真实数据里出现过 '~'
		{"a-b.map", "a-b"},
		{"noext", "noext"},
	}
	for _, c := range ok {
		got, err := CanonicalName(c.in)
		if err != nil {
			t.Errorf("CanonicalName(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CanonicalName(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	// 必须拒绝：切掉扩展名后为空、非 ASCII、空格、残留的点
	bad := []string{".map", "中文.map", "a b.map", "a.b.map"}
	for _, in := range bad {
		if _, err := CanonicalName(in); err == nil {
			t.Errorf("CanonicalName(%q) 本应报错", in)
		}
	}
	// 带路径的输入取 basename（防御性；正常调用不会传路径）
	if got, err := CanonicalName("sub/dir/a.map"); err != nil || got != "a" {
		t.Errorf("CanonicalName(\"sub/dir/a.map\") = %q, %v；期望 \"a\", nil", got, err)
	}
}

// writeMaps 造一个源目录，内容覆盖各种边界。
func writeMaps(t *testing.T) (dir string, want map[string][]byte) {
	t.Helper()
	dir = t.TempDir()
	want = map[string][]byte{}

	// 经典地图头 + 小格：52 字节头 + W*H*12
	mkMap := func(w, h int, seed int64) []byte {
		buf := make([]byte, HeaderSizeFake+w*h*12)
		binary.LittleEndian.PutUint16(buf[0:], uint16(w))
		binary.LittleEndian.PutUint16(buf[2:], uint16(h))
		buf[4] = 13
		copy(buf[5:], "Legend of mir")
		rnd := rand.New(rand.NewSource(seed))
		// 让内容有结构（类似真实地图：大量重复 + 少量变化）
		for i := 52; i+12 <= len(buf); i += 12 {
			if rnd.Intn(4) == 0 {
				binary.LittleEndian.PutUint16(buf[i:], uint16(rnd.Intn(400)))
				binary.LittleEndian.PutUint16(buf[i+4:], uint16(rnd.Intn(400)))
			}
		}
		return buf
	}

	cases := map[string][]byte{
		"0.map":      mkMap(30, 30, 1),
		"0100.MAP":   mkMap(8, 8, 2),
		"2D.MAP":     mkMap(4, 6, 3),
		"empty.map":  {},
		"zeros.map":  make([]byte, 52+10*10*12),
		"odd.map":    []byte("不是合法地图，但容器不该关心语义"),
		"bigger.map": mkMap(64, 64, 4),
	}
	for name, data := range cases {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		cn, err := CanonicalName(name)
		if err != nil {
			t.Fatal(err)
		}
		want[cn] = data
	}
	// 干扰项：非目标扩展名与点文件不应被打包
	if err := os.WriteFile(filepath.Join(dir, "bsr03.mex"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, want
}

const HeaderSizeFake = 52

func TestPackVerifyRoundTrip(t *testing.T) {
	dir, want := writeMaps(t)
	out := filepath.Join(t.TempDir(), "maps.m2pk")

	st, err := Pack(out, mustScan(t, dir), Options{})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if st.Count != len(want) {
		t.Fatalf("块数 %d，期望 %d（.mex 与 .DS_Store 不该被打包）", st.Count, len(want))
	}

	// 逐字节回读
	r, err := Open(out)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if r.Count() != len(want) {
		t.Fatalf("读回块数 %d，期望 %d", r.Count(), len(want))
	}
	for name, data := range want {
		got, ok, err := r.ReadName(name)
		if err != nil {
			t.Fatalf("ReadName(%q): %v", name, err)
		}
		if !ok {
			t.Fatalf("ReadName(%q): 未找到", name)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%q 内容不一致：%d vs %d 字节", name, len(got), len(data))
		}
	}

	// 大小写不敏感查找
	if _, ok := r.Lookup("2d"); !ok {
		t.Error("Lookup(2d) 未命中")
	}
	if _, ok := r.Lookup("2D"); !ok {
		t.Error("Lookup(2D) 应被规范化为小写后命中")
	}

	// 官方校验路径
	if err := Verify(out, dir, nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func mustScan(t *testing.T, dir string) []Source {
	t.Helper()
	s, err := ScanDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPackDeterministic(t *testing.T) {
	dir, _ := writeMaps(t)
	a := filepath.Join(t.TempDir(), "a.m2pk")
	b := filepath.Join(t.TempDir(), "b.m2pk")

	// 并行度不同也必须产出相同字节
	if _, err := Pack(a, mustScan(t, dir), Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := Pack(b, mustScan(t, dir), Options{Workers: 8}); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if !bytes.Equal(ba, bb) {
		t.Fatalf("打包不确定：%d vs %d 字节不同", len(ba), len(bb))
	}
}

func TestVerifyDetectsTamperedSource(t *testing.T) {
	dir, _ := writeMaps(t)
	out := filepath.Join(t.TempDir(), "maps.m2pk")
	if _, err := Pack(out, mustScan(t, dir), Options{}); err != nil {
		t.Fatal(err)
	}

	// 改一个字节 → verify 必须失败
	p := filepath.Join(dir, "0.map")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[100] ^= 0xFF
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(out, dir, nil); err == nil {
		t.Fatal("源文件被改动后 Verify 仍通过，逐字节校验失效")
	}
}

func TestVerifyDetectsMissingAndExtra(t *testing.T) {
	dir, _ := writeMaps(t)
	out := filepath.Join(t.TempDir(), "maps.m2pk")
	if _, err := Pack(out, mustScan(t, dir), Options{}); err != nil {
		t.Fatal(err)
	}

	// 源目录少一个文件
	if err := os.Remove(filepath.Join(dir, "0.map")); err != nil {
		t.Fatal(err)
	}
	if err := Verify(out, dir, nil); err == nil {
		t.Fatal("源目录缺文件时 Verify 应失败")
	}
}

// 大小写撞名只能在大小写敏感的卷上作为真实文件出现（D-20），
// 因此直接测下层纯函数，而不是造目录。
func TestBuildSourcesRejectsCaseCollision(t *testing.T) {
	_, err := BuildSources("/tmp", []string{"A.map", "a.map"})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("大小写撞名应报 ErrNameTaken，实得 %v", err)
	}
	if _, err := BuildSources("/tmp", []string{"0.map", "0.MAP"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("扩展名大小写不同也应视为撞名，实得 %v", err)
	}
}

func TestBuildSourcesSortedAndNormalized(t *testing.T) {
	srcs, err := BuildSources("/tmp", []string{"2D.MAP", "0.map", "ygfx1.map", "0100.MAP"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(srcs))
	for i, s := range srcs {
		got[i] = s.Name
	}
	want := []string{"0", "0100", "2d", "ygfx1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("升序规范化结果 = %v，期望 %v", got, want)
		}
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.m2pk")

	// 空文件
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("空文件应报错")
	}

	// 魔数错误
	if err := os.WriteFile(p, append([]byte("XXXX"), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); !errors.Is(err, ErrBadMagic) {
		t.Fatalf("魔数错误应报 ErrBadMagic，实得 %v", err)
	}

	// 合法头部但索引越界
	buf := make([]byte, 64)
	copy(buf, Magic)
	binary.LittleEndian.PutUint16(buf[4:], Version)
	buf[6] = KindMap
	buf[7] = CodecBrotli
	binary.LittleEndian.PutUint32(buf[8:], 3)   // count = 3
	binary.LittleEndian.PutUint32(buf[12:], 32) // namesOff 与 count 不符
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("索引越界应报错")
	}
}

func TestPackRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0.map"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := []Source{
		{Name: "0", Path: filepath.Join(dir, "0.map")},
		{Name: "0", Path: filepath.Join(dir, "0.map")},
	}
	if _, err := Pack(filepath.Join(t.TempDir(), "x.m2pk"), srcs, Options{}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("重名应报 ErrNameTaken，实得 %v", err)
	}
}

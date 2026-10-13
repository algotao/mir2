package main

import (
	"os"
	"testing"

	"github.com/algotao/mir2/internal/m2pk"
)

type manifestTestSource struct {
	recs []m2pk.ImageRecord
	raws [][]byte
}

func (s manifestTestSource) Count() int                    { return len(s.recs) }
func (s manifestTestSource) Record(i int) m2pk.ImageRecord { return s.recs[i] }
func (s manifestTestSource) Raw(i int) ([]byte, error)     { return s.raws[i], nil }

func TestManifestParsesLibraryAndRequiredImage(t *testing.T) {
	m, err := loadImageManifest(t.TempDir() + "/manifest.tsv")
	if err == nil {
		t.Fatal("不存在的清单应报错")
	}
	_ = m

	path := t.TempDir() + "/manifest.tsv"
	if err := os.WriteFile(path, []byte("library\tprguse3\texact\tall\t底栏\nimage\tprguse3\t690\t1024底栏\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadImageManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.rules[0].all || len(got.required) != 1 || got.required[0].index != 690 {
		t.Fatalf("解析结果不对：%+v", got)
	}
}

func TestManifestImageSelectorSupportsCommaRanges(t *testing.T) {
	m, err := loadImageManifestFromString("library\tprguse\texact\t1-2,4,8-9\tUI\nimage\tprguse\t1-2,4,8-9\trequired UI\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.required) != 5 {
		t.Fatalf("逗号区间应展开为 5 张图，实际 %d", len(m.required))
	}
	r, _ := m.rule("Prguse")
	for _, index := range []int{1, 2, 4, 8, 9} {
		if !r.keeps(index) {
			t.Errorf("清单要求图 %d，但 library selection 未包含", index)
		}
	}
	if r.keeps(3) || r.keeps(7) {
		t.Fatal("未列出的图不应进入打包")
	}
}

func TestManifestSparseSelectionPreservesWzlIndex(t *testing.T) {
	m, err := loadImageManifestFromString("library\tprguse3\texact\t690\t底栏\nimage\tprguse3\t690\t底栏\n")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.rule("Prguse3")
	if !ok || !r.keeps(690) || r.keeps(689) {
		t.Fatalf("selection 未按稀疏索引工作：rule=%+v", r)
	}
	records := make([]m2pk.ImageRecord, 700)
	pixels := make([][]byte, 700)
	records[689] = m2pk.ImageRecord{TypeFlag: 3, Width: 2, Height: 2}
	records[690] = m2pk.ImageRecord{TypeFlag: 3, Width: 2, Height: 2}
	pixels[689], pixels[690] = []byte{1}, []byte{2}
	filtered := manifestSource{src: manifestTestSource{records, pixels}, rule: r}
	if filtered.Count() != 700 {
		t.Fatalf("稀疏过滤改变图数：%d", filtered.Count())
	}
	if !filtered.Record(689).Blank() {
		t.Fatalf("清单外的 689 应保留空记录，不得压缩索引")
	}
	if filtered.Record(690).Blank() {
		t.Fatalf("清单内的 690 被丢了")
	}
	raw, _ := filtered.Raw(689)
	if len(raw) != 0 {
		t.Fatalf("清单外的 raw 应为空：%v", raw)
	}
	raw, _ = filtered.Raw(690)
	if len(raw) != 1 || raw[0] != 2 {
		t.Fatalf("清单内 raw 错误：%v", raw)
	}
}

func loadImageManifestFromString(s string) (imageManifest, error) {
	f, err := os.CreateTemp("", "mir2-image-manifest-*.tsv")
	if err != nil {
		return imageManifest{}, err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.WriteString(s); err != nil {
		_ = f.Close()
		return imageManifest{}, err
	}
	if err := f.Close(); err != nil {
		return imageManifest{}, err
	}
	return loadImageManifest(name)
}

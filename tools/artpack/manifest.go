package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/algotao/mir2/internal/m2pk"
)

// imageRule 是共享 manifest.tsv 的一条库规则。
type imageRule struct {
	name   string
	prefix bool
	all    bool
	keep   map[int]bool
	reason string
}

type imageRequirement struct {
	library string
	index   int
	purpose string
}

type imageManifest struct {
	rules    []imageRule
	required []imageRequirement
}

func loadImageManifest(path string) (imageManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return imageManifest{}, fmt.Errorf("读取素材清单 %s: %w", path, err)
	}
	return loadImageManifestText(path, string(data))
}

func loadImageManifestText(path, text string) (imageManifest, error) {
	var out imageManifest
	seen := map[string]bool{}
	for no, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "image\t") {
			f := strings.SplitN(line, "\t", 4)
			if len(f) != 4 {
				return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 期望 image<TAB>library<TAB>index/range<TAB>purpose", path, no+1)
			}
			lib := strings.ToLower(strings.TrimSpace(f[1]))
			indices, ok := parseImageIndices(strings.TrimSpace(f[2]))
			if !ok {
				return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 无效必需图索引 %q", path, no+1, f[2])
			}
			for _, idx := range indices {
				out.required = append(out.required, imageRequirement{library: lib, index: idx, purpose: strings.TrimSpace(f[3])})
			}
			continue
		}
		f := strings.SplitN(line, "\t", 5)
		if len(f) != 5 || f[0] != "library" {
			return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 期望 library<TAB>name<TAB>exact|prefix<TAB>selector<TAB>reason", path, no+1)
		}
		name := strings.ToLower(strings.TrimSpace(f[1]))
		kind := strings.TrimSpace(f[2])
		if kind == "prefix" {
			name = strings.TrimSuffix(name, "*")
		}
		sel := strings.TrimSpace(f[3])
		if name == "" || (kind != "exact" && kind != "prefix") {
			return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 无效的库名/匹配方式", path, no+1)
		}
		key := kind + ":" + name
		if seen[key] {
			return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 重复规则 %s", path, no+1, key)
		}
		seen[key] = true
		r := imageRule{name: name, prefix: kind == "prefix", reason: strings.TrimSpace(f[4])}
		if sel == "all" {
			r.all = true
		} else {
			r.keep = map[int]bool{}
			for _, part := range strings.Split(sel, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				lo, hi, ranged, ok := parseIndexRange(part)
				if !ok {
					return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 无效索引 %q", path, no+1, part)
				}
				if !ranged {
					hi = lo
				}
				for i := lo; i <= hi; i++ {
					r.keep[i] = true
				}
			}
			if len(r.keep) == 0 {
				return imageManifest{}, fmt.Errorf("素材清单 %s:%d: 空索引选择", path, no+1)
			}
		}
		out.rules = append(out.rules, r)
	}
	if len(out.rules) == 0 {
		return imageManifest{}, fmt.Errorf("素材清单 %s 没有图库规则", path)
	}
	for _, req := range out.required {
		found := false
		for i := range out.rules {
			r := &out.rules[i]
			if r.prefix || r.name != req.library {
				continue
			}
			found = true
			if !r.all {
				if r.keep == nil {
					r.keep = map[int]bool{}
				}
				r.keep[req.index] = true
			}
			break
		}
		if !found {
			return imageManifest{}, fmt.Errorf("必需图 %s[%d] 没有关联的 exact library 规则", req.library, req.index)
		}
	}
	return out, nil
}

func parseImageIndices(s string) ([]int, bool) {
	if strings.TrimSpace(s) == "" {
		return nil, false
	}
	seen := make(map[int]bool)
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, false
		}
		lo, hi, ranged, ok := parseIndexRange(part)
		if !ok {
			return nil, false
		}
		if !ranged {
			hi = lo
		}
		if hi-lo > 100_000 {
			return nil, false
		}
		for i := lo; i <= hi; i++ {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	return out, len(out) > 0
}

func parseIndexRange(s string) (lo, hi int, ranged, ok bool) {
	parts := strings.Split(s, "-")
	if len(parts) > 2 {
		return 0, 0, false, false
	}
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || lo < 0 {
		return 0, 0, false, false
	}
	if len(parts) == 1 {
		return lo, lo, false, true
	}
	hi, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || hi < lo {
		return 0, 0, false, false
	}
	return lo, hi, true, true
}

func (m imageManifest) rule(name string) (imageRule, bool) {
	name = strings.ToLower(name)
	for _, r := range m.rules {
		if (!r.prefix && name == r.name) || (r.prefix && strings.HasPrefix(name, r.name)) {
			return r, true
		}
	}
	return imageRule{}, false
}

func (r imageRule) keeps(i int) bool {
	return r.all || r.keep[i]
}

// manifestSource 保留源索引空间，只把未选择的图写成空记录，绝不重排图号。
type manifestSource struct {
	src  m2pk.ImageSource
	rule imageRule
}

func (s manifestSource) Count() int { return s.src.Count() }
func (s manifestSource) Record(i int) m2pk.ImageRecord {
	if !s.rule.keeps(i) {
		return m2pk.ImageRecord{}
	}
	return s.src.Record(i)
}
func (s manifestSource) Raw(i int) ([]byte, error) {
	if !s.rule.keeps(i) {
		return nil, nil
	}
	return s.src.Raw(i)
}

// selectedSources 只返回清单列出的有效图库。exact 库必须存在且非空；prefix 家族中
// 没有像素的空壳库（例如 Objects 的占位 WZL）不打包，但其余源文件仍逐一检查索引。
func (m imageManifest) selectedSources(srcDir string) ([]m2pk.Source, map[string]imageRule, error) {
	srcs, err := m2pk.ScanDir(srcDir, []string{"wzl"})
	if err != nil {
		return nil, nil, err
	}
	matched := map[string]m2pk.Source{}
	rules := map[string]imageRule{}
	matchedRule := make([]bool, len(m.rules))
	var out []m2pk.Source
	for _, s := range srcs {
		r, ok := m.rule(s.Name)
		if !ok {
			continue
		}
		ruleIndex := -1
		for i := range m.rules {
			if m.rules[i].name == r.name && m.rules[i].prefix == r.prefix {
				ruleIndex = i
				break
			}
		}
		if ruleIndex >= 0 {
			matchedRule[ruleIndex] = true
		}
		n, err := m2pk.WzlImageCount(s.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("图库 %s 的索引不可读: %w", s.Name, err)
		}
		if n == 0 && r.prefix {
			continue
		}
		if n == 0 {
			return nil, nil, fmt.Errorf("清单要求图库 %s，但源库图数为 0", s.Name)
		}
		probe, err := m2pk.OpenWzlSourcePath(s.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("打开源库 %s: %w", s.Name, err)
		}
		nonblank := 0
		for i := 0; i < probe.Count(); i++ {
			if !probe.Record(i).Blank() {
				nonblank++
			}
		}
		if nonblank == 0 {
			if r.prefix {
				continue
			}
			return nil, nil, fmt.Errorf("清单要求图库 %s，但所有图号都是空白/无像素", s.Name)
		}
		if !r.all {
			for i := range r.keep {
				if i >= n {
					return nil, nil, fmt.Errorf("图库 %s 缺少清单要求的图号 %d（图数 %d）", s.Name, i, n)
				}
			}
		}
		matched[s.Name] = s
		rules[s.Name] = r
		out = append(out, s)
	}
	for i, r := range m.rules {
		if !matchedRule[i] && !r.prefix {
			return nil, nil, fmt.Errorf("清单要求图库 %s，但源目录不存在", r.name)
		}
		if !matchedRule[i] && r.prefix {
			return nil, nil, fmt.Errorf("清单要求图库家族 %s*，但源目录中一个匹配库都没有", r.name)
		}
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("素材清单没有匹配到有效图库")
	}
	for _, req := range m.required {
		s, ok := matched[req.library]
		if !ok {
			return nil, nil, fmt.Errorf("必需图 %s[%d] 的图库未被选中", req.library, req.index)
		}
		wzl, err := m2pk.OpenWzlSourcePath(s.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("打开源库 %s: %w", s.Name, err)
		}
		rec := wzl.Record(req.index)
		raw, err := wzl.Raw(req.index)
		if err != nil || rec.Blank() || len(raw) == 0 {
			return nil, nil, fmt.Errorf("必需图 %s[%d] (%s) 为空/损坏: %v", req.library, req.index, req.purpose, err)
		}
	}
	return out, rules, nil
}

func verifySelectedImage(containerPath string, srcs []m2pk.Source, rules map[string]imageRule) error {
	r, err := m2pk.Open(containerPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if r.Kind() != m2pk.KindImage {
		return fmt.Errorf("容器 kind=%d，不是美术容器", r.Kind())
	}
	want := make(map[string]bool, len(srcs))
	for _, s := range srcs {
		want[s.Name] = true
	}
	for _, e := range r.Entries() {
		if !want[e.Name] {
			return fmt.Errorf("容器含清单外图库 %s", e.Name)
		}
	}
	for _, s := range srcs {
		if _, ok, err := r.ReadName(s.Name); err != nil || !ok {
			return fmt.Errorf("容器缺清单图库 %s", s.Name)
		}
		src, err := m2pk.OpenWzlSourcePath(s.Path)
		if err != nil {
			return fmt.Errorf("打开源库 %s: %w", s.Name, err)
		}
		filtered := manifestSource{src: src, rule: rules[s.Name]}
		lib, err := m2pk.ReadImagePayload(r, s.Name)
		if err != nil {
			return err
		}
		if lib.Count() != filtered.Count() {
			return fmt.Errorf("%s 图数变化：源 %d / 容器 %d", s.Name, filtered.Count(), lib.Count())
		}
		if err := lib.EachImage(func(i int, rec m2pk.ImageRecord, raw []byte) error {
			wantRec := filtered.Record(i)
			if rec != wantRec {
				return fmt.Errorf("%s[%d] 记录不一致：源 %+v / 容器 %+v", s.Name, i, wantRec, rec)
			}
			wantRaw, err := filtered.Raw(i)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, wantRaw) {
				return fmt.Errorf("%s[%d] raw 像素不一致", s.Name, i)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

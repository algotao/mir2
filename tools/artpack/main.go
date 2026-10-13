// Command artpack 是客户端美术容器的构建与验证工具。
//
// 唯一图库选择源是 `client/assets/image-libraries.tsv`：只打包清单列出的逻辑图库与图号，
// 未列出的源素材一律不进入 images.m2pk。规格见 internal/m2pk/image.go / D-31、D-135。
//
// 用法：
//
//	artpack pack   -src DIR -out FILE -manifest FILE [-group N] [-workers N] [-quality N] [-quiet]
//	artpack verify -in FILE -src DIR -manifest FILE
//	artpack info   -in FILE [-list]
//
// 与 `m2pk verify` 的区别：美术容器的块不是源文件字节（重排过），所以校验是
// **逐图解出来比对记录 + raw 像素**（m2pk.VerifyImage），仍然是全量无损。
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/algotao/mir2/internal/m2pk"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "pack":
		err = cmdPack(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "info":
		err = cmdInfo(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "artpack: 未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nartpack: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `artpack —— 美术容器工具（M2PK kind=3，图库分组拼接）

  pack   -src DIR -out FILE -manifest FILE [-group N] [-workers N] [-quality N] [-quiet]
         按客户端唯一素材清单选择图库/图号，稀疏图号保留原索引，不重排；质量 0..11

  verify -in FILE -src DIR -manifest FILE
         只按同一清单逐图比对记录与 raw 像素；清单外图库会报错

  info   -in FILE [-list]
         打印统计；-list 逐库列出

示例：
  artpack pack   -src $WS/mir2c/data2 -out assets/image/images.m2pk -manifest client/assets/image-libraries.tsv
  artpack verify -in assets/image/images.m2pk -src $WS/mir2c/data2 -manifest client/assets/image-libraries.tsv
`)
}

// libStat 是单库的编码结果（打印 + 体检）。
type libStat struct {
	name   string
	imgs   int
	groups int
	blank  int
	broken int
	raw    int64
	comp   int64
	base   int64 // 现状：源 .wzl + .wzx 的字节数
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	srcDir := fs.String("src", "", "源目录（含 .wzl + .wzx）")
	out := fs.String("out", "", "输出容器路径")
	manifestPath := fs.String("manifest", "", "客户端唯一素材清单（TSV）")
	group := fs.Int("group", m2pk.DefaultImageGroup, "每组图数（1..1024）")
	workers := fs.Int("workers", 0, "并行编码组数（0 = 自动）")
	quality := fs.Int("quality", 8, "brotli 质量（0..11；低质量较快、容器略大）")
	quiet := fs.Bool("quiet", false, "不打印进度")
	fs.Parse(args)

	if *srcDir == "" || *out == "" || *manifestPath == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -src、-out 与 -manifest；禁止不带清单打全库")
	}
	manifest, err := loadImageManifest(*manifestPath)
	if err != nil {
		return err
	}
	srcs, rules, err := manifest.selectedSources(*srcDir)
	if err != nil {
		return err
	}

	// 现状基线：源 .wzl + .wzx 的字节数（我们替换的就是这两份）。
	stats := make([]libStat, len(srcs))
	for i, s := range srcs {
		stats[i] = libStat{name: s.Name, base: fileSize(s.Path) + fileSize(base(s.Path)+".wzx")}
	}

	items := make([]m2pk.GenItem, len(srcs))
	for i, s := range srcs {
		i, s := i, s
		items[i] = m2pk.GenItem{
			Name: s.Name,
			Gen: func() ([]byte, error) {
				wz, err := m2pk.OpenWzlSourcePath(s.Path)
				if err != nil {
					return nil, err
				}
				selected := manifestSource{src: wz, rule: rules[s.Name]}
				payload, st, err := m2pk.EncodeImageLibWithQuality(selected, *group, *workers, *quality)
				if err != nil {
					return nil, err
				}
				if st.Broken != 0 {
					return nil, fmt.Errorf("图库 %s 有 %d 张清单内素材损坏；拒绝生成不完整容器", s.Name, st.Broken)
				}
				stats[i].imgs = st.Count
				stats[i].groups = st.Groups
				stats[i].blank = st.Blank
				stats[i].broken = st.Broken
				stats[i].raw = st.RawTotal
				stats[i].comp = st.CompTotal
				return payload, nil
			},
		}
	}

	fmt.Fprintf(os.Stderr, "打包 %d 个图库（每组 %d 张）→ %s\n", len(srcs), *group, *out)
	var lastPrint time.Time
	prog := func(done, total int, name string, _, _ int) {
		if *quiet {
			return
		}
		if done != total && time.Since(lastPrint) < 300*time.Millisecond {
			return
		}
		lastPrint = time.Now()
		fmt.Fprintf(os.Stderr, "\r  [%d/%d] %-14s ", done, total, name)
	}
	start := time.Now()
	st, err := m2pk.PackGen(*out, items, m2pk.Options{
		Kind:     m2pk.KindImage,
		Codec:    m2pk.CodecStore,
		Progress: prog,
	})
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}

	var base, raw, comp, imgs, broken int64
	for _, s := range stats {
		base += s.base
		raw += s.raw
		comp += s.comp
		imgs += int64(s.imgs)
		broken += int64(s.broken)
	}
	fmt.Printf("打包完成：%d 库 / %d 图（%d 空白，%d 源异常），耗时 %s\n",
		len(srcs), imgs, statsBlank(stats), broken, time.Since(start).Round(time.Millisecond))
	fmt.Printf("  现状（.wzl+.wzx）  %s\n", human(base))
	fmt.Printf("  容器               %s   %.1f%%（省 %s）\n",
		human(st.FileSize), float64(st.FileSize)/float64(base)*100, human(base-st.FileSize))
	fmt.Printf("  像素数据           %s → %s\n", human(raw), human(comp))

	if !*quiet {
		fmt.Printf("\n%-14s %8s %7s %12s %12s %8s\n", "库", "图数", "组数", "像素", "压缩后", "比率")
		sorted := append([]libStat(nil), stats...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].comp > sorted[j].comp })
		for _, s := range sorted {
			ratio := 0.0
			if s.raw > 0 {
				ratio = float64(s.comp) / float64(s.raw)
			}
			note := ""
			if s.broken > 0 {
				note = fmt.Sprintf("  ⚠️ 源异常 %d 张", s.broken)
			}
			fmt.Printf("%-14s %8d %7d %12s %12s %8.4f%s\n",
				s.name, s.imgs, s.groups, human(s.raw), human(s.comp), ratio, note)
		}
	}
	return nil
}

func statsBlank(stats []libStat) int64 {
	var n int64
	for _, s := range stats {
		n += int64(s.blank)
	}
	return n
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("in", "", "容器路径")
	src := fs.String("src", "", "源目录（含 .wzl + .wzx）")
	manifestPath := fs.String("manifest", "", "客户端唯一素材清单（TSV；必须与 pack 一致）")
	fs.Parse(args)
	if *in == "" || *src == "" || *manifestPath == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -in、-src 与 -manifest")
	}
	manifest, err := loadImageManifest(*manifestPath)
	if err != nil {
		return err
	}
	srcs, rules, err := manifest.selectedSources(*src)
	if err != nil {
		return err
	}
	start := time.Now()
	if err := verifySelectedImage(*in, srcs, rules); err != nil {
		return err
	}
	r, err := m2pk.Open(*in)
	if err != nil {
		return err
	}
	defer r.Close()
	st := r.Stats()
	fmt.Printf("校验通过：%d 个图库的记录与 raw 像素逐一一致，容器 %s，耗时 %s\n",
		st.Count, human(st.FileSize), time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	in := fs.String("in", "", "容器路径")
	list := fs.Bool("list", false, "逐库列出")
	fs.Parse(args)
	if *in == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -in")
	}
	r, err := m2pk.Open(*in)
	if err != nil {
		return err
	}
	defer r.Close()
	if r.Kind() != m2pk.KindImage {
		return fmt.Errorf("artpack: 容器 kind=%d，不是美术容器", r.Kind())
	}
	st := r.Stats()
	fmt.Printf("容器   %s\n", *in)
	fmt.Printf("kind   %d（美术）  codec %d（原样）\n", r.Kind(), r.Codec())
	fmt.Printf("图库   %d 个\n", st.Count)
	fmt.Printf("文件   %s\n", human(st.FileSize))
	if !*list {
		return nil
	}
	type row struct {
		name        string
		count       int
		groups      int
		raw, comp   int64
		containerSz int64
	}
	var rows []row
	var rawSum, compSum int64
	for _, e := range r.Entries() {
		lib, err := m2pk.ReadImagePayload(r, e.Name)
		if err != nil {
			return err
		}
		rows = append(rows, row{
			e.Name, lib.Count(), lib.Groups(),
			lib.RawBytes(), lib.CompBytes(), int64(e.CompSize),
		})
		rawSum += lib.RawBytes()
		compSum += lib.CompBytes()
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].containerSz > rows[j].containerSz })
	fmt.Printf("\n%-14s %8s %7s %12s %12s %10s\n", "库", "图数", "组数", "像素", "压缩后", "容器份额")
	for _, x := range rows {
		fmt.Printf("%-14s %8d %7d %12s %12s %10s\n",
			x.name, x.count, x.groups, human(x.raw), human(x.comp), human(x.containerSz))
	}
	fmt.Printf("\n像素合计 %s → %s（%.1f%%）\n",
		human(rawSum), human(compSum), float64(compSum)/float64(rawSum)*100)
	return nil
}

func base(path string) string {
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		return path[:i]
	}
	return path
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

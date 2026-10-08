// Command artpack 是**美术容器**（M2PK kind=3）的命令行工具。
//
// 它把 `mir2c/data` 里的图库（`.wzl` + `.wzx`，每张图各自 zlib）重排成
// "每 N 张一组 + 每组一条 brotli 流"的 IMGP 载荷，一库一块装进容器。
// 规格见 internal/m2pk/image.go，决策见 docs/decisions.md D-31。
//
// 用法：
//
//	artpack pack   -src DIR -out FILE [-group N] [-workers N] [-lib a,b] [-quiet]
//	artpack verify -in FILE -src DIR
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

  pack   -src DIR -out FILE [-group N] [-workers N] [-lib a,b] [-quiet]
         把 .wzl/.wzx 重排成"每 N 张一组 + 每组一条 brotli 流"，一库一块

  verify -in FILE -src DIR
         逐图比对记录与 raw 像素（语义等价的无损校验，D-06 硬要求）

  info   -in FILE [-list]
         打印统计；-list 逐库列出

示例：
  artpack pack   -src $WS/mir2c/data -out assets/image/images.m2pk
  artpack verify -in assets/image/images.m2pk -src $WS/mir2c/data
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

func parseLibs(s string) map[string]bool {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	srcDir := fs.String("src", "", "源目录（含 .wzl + .wzx）")
	out := fs.String("out", "", "输出容器路径")
	group := fs.Int("group", m2pk.DefaultImageGroup, "每组图数（1..1024）")
	workers := fs.Int("workers", 0, "并行编码组数（0 = 自动）")
	libsFlag := fs.String("lib", "", "只打包这些库（逗号分隔，规范化名；空 = 全部）")
	quiet := fs.Bool("quiet", false, "不打印进度")
	fs.Parse(args)

	if *srcDir == "" || *out == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -src 与 -out")
	}

	srcs, err := m2pk.ScanDir(*srcDir, []string{"wzl"})
	if err != nil {
		return err
	}
	only := parseLibs(*libsFlag)
	if only != nil {
		var keep []m2pk.Source
		for _, s := range srcs {
			if only[s.Name] {
				keep = append(keep, s)
			}
		}
		for name := range only {
			found := false
			for _, s := range srcs {
				if s.Name == name {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("artpack: 源目录里没有库 %q", name)
			}
		}
		srcs = keep
	}
	if len(srcs) == 0 {
		return fmt.Errorf("artpack: 源目录里没有 .wzl（%s）", *srcDir)
	}

	// 空库 / 缺索引的库跳过（客户端遇到"容器里没有"会回退裸目录，行为一致）。
	// 不静默：每个都打印原因，最后再汇总一行。
	var skipped []string
	{
		var keep []m2pk.Source
		for _, s := range srcs {
			n, err := m2pk.WzlImageCount(s.Path)
			switch {
			case err != nil:
				skipped = append(skipped, fmt.Sprintf("%s（读不到 .wzx：%v）", s.Name, err))
			case n <= 0:
				skipped = append(skipped, fmt.Sprintf("%s（图数为 0，空库）", s.Name))
			default:
				keep = append(keep, s)
			}
		}
		srcs = keep
	}
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "  跳过 %s\n", s)
	}
	if len(srcs) == 0 {
		return fmt.Errorf("artpack: 没有可打包的图库（全部跳过）")
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
				payload, st, err := m2pk.EncodeImageLib(wz, *group, *workers)
				if err != nil {
					return nil, err
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
	fs.Parse(args)
	if *in == "" || *src == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -in 与 -src")
	}
	start := time.Now()
	if err := m2pk.VerifyImage(*in, *src); err != nil {
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

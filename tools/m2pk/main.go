// Command m2pk 是 M2PK 资产容器的命令行工具（地图打包）。
//
// 用法：
//
//	m2pk pack   -src DIR [-src DIR2 ...] -out FILE [-ext map] [-fast] [-workers N]
//	m2pk verify -in FILE -src DIR [-ext map]
//	m2pk info   -in FILE [-list]
//
// 规格见 docs/assets.md §5，决策见 docs/decisions.md D-11。
// `verify` 是硬要求（D-06）：解压后与源文件**逐字节**比对，不一致则非零退出。
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
		fmt.Fprintf(os.Stderr, "m2pk: 未知子命令 %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nm2pk: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `m2pk —— M2PK 资产容器工具（地图）

  pack   -src DIR [-src DIR2 ...] -out FILE [-ext map] [-quality N] [-workers N]
         把源目录里的文件打包成容器（一文件一块，brotli 压缩，逐字节无损）

  verify -in FILE -src DIR [-ext map]
         逐字节校验容器与源目录一致（D-06 硬要求）

  info   -in FILE [-list]
         打印容器统计；-list 逐条列出

示例：
  m2pk pack   -src $WS/mir2c/map -out assets/map/maps.m2pk
  m2pk verify -in assets/map/maps.m2pk -src $WS/mir2c/map
`)
}

// multiFlag 收集可重复出现的字符串参数。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func parseExts(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	var srcs multiFlag
	fs.Var(&srcs, "src", "源目录（可重复）")
	out := fs.String("out", "", "输出容器路径")
	ext := fs.String("ext", "map", "纳入的文件扩展名（逗号分隔）")
	quality := fs.Int("quality", 0, "brotli 质量档 0–11（0 = 默认 11，体积优先）")
	workers := fs.Int("workers", 0, "并行压缩 goroutine 数（0 = 自动）")
	quiet := fs.Bool("quiet", false, "不打印进度")
	fs.Parse(args)

	if len(srcs) == 0 || *out == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -src 与 -out")
	}

	// 收集并合并多个源目录，规范化重名必须报错（不静默覆盖）。
	seen := make(map[string]string)
	var all []m2pk.Source
	for _, dir := range srcs {
		list, err := m2pk.ScanDir(dir, parseExts(*ext))
		if err != nil {
			return err
		}
		for _, s := range list {
			if prev, dup := seen[s.Name]; dup {
				return fmt.Errorf("m2pk: 名字冲突 %q：%s 与 %s", s.Name, prev, s.Path)
			}
			seen[s.Name] = s.Path
			all = append(all, s)
		}
		fmt.Fprintf(os.Stderr, "扫描 %s：%d 个文件\n", dir, len(list))
	}
	if len(all) == 0 {
		return fmt.Errorf("m2pk: 没有可打包的文件（扩展名 = %s）", *ext)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })

	fmt.Fprintf(os.Stderr, "打包 %d 个文件 → %s\n", len(all), *out)

	var lastPrint time.Time
	start := time.Now()
	prog := func(done, total int, name string, rawSize, compSize int) {
		if *quiet {
			return
		}
		if done%25 != 0 && done != total && time.Since(lastPrint) < time.Second {
			return
		}
		lastPrint = time.Now()
		fmt.Fprintf(os.Stderr, "\r  [%d/%d] %-16s %s → %s   ",
			done, total, name, human64(int64(rawSize)), human64(int64(compSize)))
	}

	st, err := m2pk.Pack(*out, all, m2pk.Options{
		Quality:  *quality,
		Workers:  *workers,
		Progress: prog,
	})
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}
	fmt.Printf("打包完成：%d 块，%s → %s（比率 %.4f），容器 %s，耗时 %s\n",
		st.Count, human64(st.RawTotal), human64(st.CompTotal), st.Ratio(),
		human64(st.FileSize), time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("in", "", "容器路径")
	src := fs.String("src", "", "源目录")
	ext := fs.String("ext", "map", "纳入的文件扩展名（逗号分隔）")
	fs.Parse(args)

	if *in == "" || *src == "" {
		fs.Usage()
		return fmt.Errorf("必须有 -in 与 -src")
	}
	start := time.Now()
	if err := m2pk.Verify(*in, *src, parseExts(*ext)); err != nil {
		return err
	}
	r, err := m2pk.Open(*in)
	if err != nil {
		return err
	}
	defer r.Close()
	st := r.Stats()
	fmt.Printf("校验通过：%d 块逐字节一致，%s → %s（比率 %.4f），耗时 %s\n",
		st.Count, human64(st.RawTotal), human64(st.CompTotal), st.Ratio(),
		time.Since(start).Round(time.Millisecond))
	return nil
}

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	in := fs.String("in", "", "容器路径")
	list := fs.Bool("list", false, "逐条列出")
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

	st := r.Stats()
	fmt.Printf("容器   %s\n", *in)
	fmt.Printf("块数   %d\n", st.Count)
	fmt.Printf("原始   %s\n", human64(st.RawTotal))
	fmt.Printf("压缩后 %s（索引外）\n", human64(st.CompTotal))
	fmt.Printf("比率   %.4f\n", st.Ratio())
	fmt.Printf("文件   %s\n", human64(st.FileSize))
	if !*list {
		return nil
	}
	es := r.Entries()
	sort.Slice(es, func(i, j int) bool { return es[i].RawSize > es[j].RawSize })
	fmt.Printf("\n%-20s %12s %12s %8s\n", "名字", "原始", "压缩后", "比率")
	for _, e := range es {
		ratio := 0.0
		if e.RawSize > 0 {
			ratio = float64(e.CompSize) / float64(e.RawSize)
		}
		fmt.Printf("%-20s %12s %12s %8.4f\n", e.Name, human64(int64(e.RawSize)), human64(int64(e.CompSize)), ratio)
	}
	return nil
}

func human64(n int64) string {
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

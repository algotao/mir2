// Command wavpack 把客户端集的**原始 wav** 转成客户端实际加载的那一套（体积优先），
// 产出 `assets/audio/`（脚本产物，不入库 —— 见 docs/assets.md §6）。
//
// 用法：
//
//	wavpack survey -src DIR                                  # 量一遍（格式/体积/重复/估算）
//	wavpack pack   -src DIR -out DIR [-rate N] [-keep-all]   # 转换（默认自检）
//
// # 为什么要有它
//
// 原始素材是**未压缩 PCM**：实测 777 个 209.0 MB，其中**七个长 BGM 就占 72 MB**
// （`Field2.wav` 一个 33.8 MB / 191.5 s）。想瘦身必须先看清两条：
//
//  1. **无损压缩没用**。对原始 PCM 实测：zlib 87~96%、xz 82~93%（音效是宽带噪声，
//     本来就压不动）。所以"打包 + 压缩"这条路直接排除，省得白干。
//  2. 真正的杠杆是**转换**：原版引擎是"照文件原样建 DirectSound buffer"
//     （`DXSounds.pas:1852-1881`），所以要降就得在这里降。
//
// 按什么降（都有理由，不是拍脑袋）：
//
//   - **音效 → 22.05 kHz 单声道**：原版**没有 pan、没有距离衰减**
//     （`SoundUtil.pas:180-192` 查完 `FileExists` 就按原样播），所以立体声→单声道
//     **不丢任何游戏信息**；22.05 kHz 是那个年代的音效标准采样率（我们这堆里本来
//     就有 4 个 22.05k 的）。
//   - **BGM（登录/选角/自己死亡三首）保持原样**：音乐是立体声的，降采样听得出来。
//   - **原版从不播的长文件不产出**（`-keep-all` 可保留）：
//     `Field2.wav`（`bmg_field` 在 `SoundUtil.pas:33` 定义了，但全代码**未使用**）、
//     `main_theme.wav`（播它的定时器在原版里被注释掉了，`PlayScn.pas:485-486`）、
//     `Game-over2.wav`（与 `log-in-long2.wav` **逐字节相同**，md5 已验）。
//   - **裁掉尾部静音**（原版音效带长尾巴，实测占音效体积约 4.9%）：阈值极低
//     （|样本| < 32 ≈ 0.1%），只切数字静音，听感无差。
package main

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 原版写死的 BGM 文件名（`SoundUtil.pas:31-34`）。这四首**保持原样**（不降采样）。
var bgmNames = map[string]bool{
	"log-in-long2.wav":  true,
	"sellect-loop2.wav": true,
	"field2.wav":        true, // 只在 survey 里用得上；pack 默认跳过它
	"game over2.wav":    true,
}

// 原版**从不播**的长文件：默认不产出（`-keep-all` 覆盖）。
var unusedNames = map[string]string{
	"Field2.wav":     "bmg_field 定义了但全代码未使用（SoundUtil.pas:33）",
	"main_theme.wav": "播它的定时器被注释掉了（PlayScn.pas:485-486）",
	"Game-over2.wav": "与 log-in-long2.wav 逐字节相同（md5 已验）",
}

type wav struct {
	format   int // 1 = PCM
	channels int
	rate     int
	bits     int
	pcm      []int16
}

const trimThreshold = 32 // |样本| < 32 ⇒ 视为静音（int16 满量程 ≈ 0.1%）

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "survey":
		err = cmdSurvey(os.Args[2:])
	case "pack":
		err = cmdPack(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wavpack —— 音频资产转换（源 = 客户端集的 mir2c/wav，产物 = assets/audio）

  wavpack survey -src DIR
  wavpack pack   -src DIR -out DIR [-rate 22050] [-keep-all] [-no-verify]
`)
}

// ---------- 读 / 写 WAV ----------

func readWav(path string) (*wav, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("不是 RIFF/WAVE")
	}
	var w wav
	var dataOff, dataLen = -1, 0
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if body+size > len(b) {
			size = len(b) - body // 有些文件尾部截断，容忍
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("fmt 块太小")
			}
			w.format = int(binary.LittleEndian.Uint16(b[body:]))
			w.channels = int(binary.LittleEndian.Uint16(b[body+2:]))
			w.rate = int(binary.LittleEndian.Uint32(b[body+4:]))
			w.bits = int(binary.LittleEndian.Uint16(b[body+14:]))
		case "data":
			dataOff, dataLen = body, size
		}
		off = body + size + (size & 1)
	}
	if w.format == 0 || w.channels == 0 || w.rate == 0 || dataOff < 0 {
		return nil, errors.New("缺 fmt/data 块")
	}
	if w.format != 1 {
		return nil, fmt.Errorf("非 PCM（format=%d）", w.format)
	}
	raw := b[dataOff : dataOff+dataLen]
	switch w.bits {
	case 16:
		w.pcm = make([]int16, len(raw)/2)
		for i := range w.pcm {
			w.pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
		}
	case 8:
		// 8bit PCM 是**无符号**的（0 = 静音）
		w.pcm = make([]int16, len(raw))
		for i, v := range raw {
			w.pcm[i] = int16((int(v) - 128) << 8)
		}
	default:
		return nil, fmt.Errorf("不支持的位深 %d", w.bits)
	}
	return &w, nil
}

func writeWav16(path string, mono []int16, rate int) error {
	buf := make([]byte, 44+len(mono)*2)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+len(mono)*2))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:], 1) // 单声道
	binary.LittleEndian.PutUint32(buf[24:], uint32(rate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(rate*2)) // byte rate
	binary.LittleEndian.PutUint16(buf[32:], 2)              // block align
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(len(mono)*2))
	for i, v := range mono {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(v))
	}
	return os.WriteFile(path, buf, 0o644)
}

// ---------- 转换 ----------

// toMonoRate 下混成单声道 + 线性重采样到 dstRate。
//
// 用线性插值就够：原版这些是短音效，且我们只是"把 44.1k 降到 22.05k"，
// 不是做母带处理。采样率相同则原样返回（只下混）。
//
// ⚠️ 返回值第二项 = "为了避开**相位相消**改用了较响的那一路"。立体声两声道反相时，
// 平均会**变成静音**（真实存在的一类素材 bug）—— 那时单声道下混等于把这条音效弄丢，
// 所以宁可只留一路（听感上比静音好得多）。
func toMonoRate(src []int16, ch, srcRate, dstRate int) ([]int16, bool) {
	frames := len(src) / ch
	if frames == 0 {
		return nil, false
	}
	mono := make([]int16, frames)
	for i := 0; i < frames; i++ {
		s := 0
		for c := 0; c < ch; c++ {
			s += int(src[i*ch+c])
		}
		mono[i] = int16(s / ch)
	}
	fellBack := false
	if ch > 1 && peak(src) > 0 && peak(mono)*4 < peak(src) {
		loudest := 0
		best := 0
		for c := 0; c < ch; c++ {
			var col []int16
			for i := 0; i < frames; i++ {
				col = append(col, src[i*ch+c])
			}
			if p := peak(col); p > best {
				best, loudest = p, c
			}
		}
		picked := make([]int16, frames)
		for i := 0; i < frames; i++ {
			picked[i] = src[i*ch+loudest]
		}
		mono = picked
		fellBack = true
	}
	if srcRate == dstRate {
		return mono, fellBack
	}
	out := make([]int16, int(float64(frames)*float64(dstRate)/float64(srcRate)))
	for j := range out {
		pos := float64(j) * float64(srcRate) / float64(dstRate)
		i0 := int(pos)
		if i0 > frames-2 {
			i0 = frames - 2
		}
		if i0 < 0 {
			i0 = 0
		}
		frac := pos - float64(i0)
		a := float64(mono[i0])
		b := float64(mono[min(i0+1, frames-1)])
		out[j] = int16(a + (b-a)*frac)
	}
	return out, fellBack
}

// trimTail 裁掉尾部静音（保留 50 ms 尾巴，别把自然衰减切得太硬）。
func trimTail(pcm []int16, rate int) []int16 {
	keep := rate / 20
	i := len(pcm)
	for i > keep && pcm[i-1] > -trimThreshold && pcm[i-1] < trimThreshold {
		i--
	}
	return pcm[:i]
}

func peak(pcm []int16) int {
	m := 0
	for _, v := range pcm {
		if v < 0 {
			v = -v
		}
		if int(v) > m {
			m = int(v)
		}
	}
	return m
}

func md5file(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// ---------- survey ----------

func cmdSurvey(args []string) error {
	fs := flag.NewFlagSet("survey", flag.ExitOnError)
	src := fs.String("src", "", "原始 wav 目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src == "" {
		return errors.New("要 -src DIR")
	}
	names, err := wavNames(*src)
	if err != nil {
		return err
	}

	var totalSize, totalFrames int64
	byRate := map[int]int{}
	byCh := map[int]int{}
	byBits := map[int]int{}
	type big struct {
		name string
		size int64
		w    *wav
	}
	var biggest []big
	sums := map[string][]string{}

	for _, n := range names {
		p := filepath.Join(*src, n)
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		totalSize += fi.Size()
		w, err := readWav(p)
		if err != nil {
			fmt.Printf("  ⚠️ %s: %v\n", n, err)
			continue
		}
		byRate[w.rate]++
		byCh[w.channels]++
		byBits[w.bits]++
		totalFrames += int64(len(w.pcm) / w.channels)
		biggest = append(biggest, big{n, fi.Size(), w})

		sum, err := md5file(p)
		if err == nil {
			sums[sum] = append(sums[sum], n)
		}
	}

	seconds := float64(totalFrames) / float64(44100) // 粗略：多为 44.1k
	fmt.Printf("文件数 %d   合计 %.1f MB\n", len(names), float64(totalSize)/1e6)
	fmt.Printf("采样率 %v\n声道 %v\n位深 %v\n", byRate, byCh, byBits)
	fmt.Printf("（时长按 44.1k 粗算：%.1f 分钟）\n", seconds/60)

	sort.Slice(biggest, func(i, j int) bool { return biggest[i].size > biggest[j].size })
	fmt.Println("\n最大的 10 个：")
	var sumBig int64
	for i, b := range biggest {
		d := float64(len(b.w.pcm)/b.w.channels) / float64(b.w.rate)
		if i < 10 {
			fmt.Printf("  %7.2f MB  %-20s %dHz %dch %dbit %6.1fs\n",
				float64(b.size)/1e6, b.name, b.w.rate, b.w.channels, b.w.bits, d)
		}
	}
	for _, b := range biggest[:min(10, len(biggest))] {
		sumBig += b.size
	}
	fmt.Printf("  ⇒ 这 10 个占 %.1f MB（%.0f%%）\n", float64(sumBig)/1e6, float64(sumBig)/float64(totalSize)*100)

	fmt.Println("\n内容完全相同的重复：")
	dups := 0
	var dupBytes int64
	var keys []string
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := sums[k]
		if len(g) < 2 {
			continue
		}
		dups++
		if fi, err := os.Stat(filepath.Join(*src, g[0])); err == nil {
			dupBytes += fi.Size() * int64(len(g)-1)
		}
		fmt.Printf("  %v\n", g)
	}
	fmt.Printf("  ⇒ %d 组，可省 %.1f MB\n", dups, float64(dupBytes)/1e6)
	return nil
}

// ---------- pack ----------

// soundList 读 `sound.lst`（`<编号>: wav\X.wav`，见 `SoundUtil.pas:151-178`）。
//
// 返回 two 个视图：
//   - `byNum`：编号 → 清单里写的文件名（**保留清单的拼写**）；
//   - `spelled`：小写文件名 → 清单里的拼写（用来对齐大小写，见下）。
//
// ⚠️ **为什么要按清单拼写输出**：清单里写的是 `wav\game-over2.wav`（小写 g），
// 而客户端集里的真文件叫 `Game-over2.wav`（大写 G）。macOS 不区分大小写 ⇒
// 看起来没事；**Linux 上会直接找不到**。产物是我们生成的，就按清单的拼写写，
// 顺手把这个跨平台坑填了。
func soundList(dir string) (map[int]string, map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "sound.lst"))
	if err != nil {
		return nil, nil, err
	}
	byNum := map[int]string{}
	spelled := map[string]string{}
	// 清单是 GBK，但我们只取 ASCII 部分（编号与文件名）
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		head, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		num, err := strconv.Atoi(strings.TrimSpace(head))
		if err != nil {
			continue
		}
		rest = strings.TrimSpace(rest)
		i := strings.LastIndexAny(rest, `\/`)
		name := rest[i+1:]
		if name == "" {
			continue
		}
		byNum[num] = name
		spelled[strings.ToLower(name)] = name
	}
	return byNum, spelled, nil
}

func cmdPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	src := fs.String("src", "", "原始 wav 目录（含 sound.lst）")
	out := fs.String("out", "", "输出目录（assets/audio）")
	rate := fs.Int("rate", 22050, "音效目标采样率（BGM 不动）")
	keepAll := fs.Bool("keep-all", false, "连原版从不播的长文件也一起产出")
	noVerify := fs.Bool("no-verify", false, "跳过自检")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src == "" || *out == "" {
		return errors.New("要 -src DIR 与 -out DIR")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	names, err := wavNames(*src)
	if err != nil {
		return err
	}
	byNum, spelled, err := soundList(*src)
	if err != nil {
		fmt.Printf("  ⚠️ 读不到 sound.lst（%v）：跳过规则与大小写对齐都失效\n", err)
		byNum, spelled = map[int]string{}, map[string]string{}
	}
	// 清单引用了哪些文件（小写比较，用于"别把清单要的文件跳掉"）
	referenced := map[string]bool{}
	for _, name := range byNum {
		referenced[strings.ToLower(name)] = true
	}

	var inBytes, outBytes, skippedBytes int64
	var nConv, nCopy, nSkip, nFall, nRespell int
	for _, n := range names {
		p := filepath.Join(*src, n)
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		inBytes += fi.Size()

		// 产出文件名：清单里引用它 ⇒ 用**清单的拼写**（填 Linux 上的大小写坑）
		outName := n
		if s, ok := spelled[strings.ToLower(n)]; ok && s != n {
			outName = s
			nRespell++
		}
		dst := filepath.Join(*out, outName)

		if why, unused := unusedNames[n]; unused && !*keepAll {
			if referenced[strings.ToLower(n)] {
				// 原版代码不播它，但清单里**有编号指向它** ⇒ 照常转换（少一个音效
				// 比多占几 MB 糟糕得多；而且这正是自检要抓的那类遗漏）。
				fmt.Printf("  注意 %-18s 原版不播（%s），但 sound.lst 引用了它 ⇒ 照常转换\n", n, why)
			} else {
				skippedBytes += fi.Size()
				nSkip++
				fmt.Printf("  跳过 %-18s %6.2f MB  —— %s\n", n, float64(fi.Size())/1e6, why)
				continue
			}
		}
		if bgmNames[strings.ToLower(n)] || *rate <= 0 {
			// BGM：原样搬（音乐不动它）
			if err := copyFile(p, dst); err != nil {
				return err
			}
			outBytes += fi.Size()
			nCopy++
			continue
		}

		w, err := readWav(p)
		if err != nil {
			// 不认识的（非 PCM）原样搬，别把素材弄丢
			fmt.Printf("  ⚠️ %s: %v ⇒ 原样搬\n", n, err)
			if err := copyFile(p, dst); err != nil {
				return err
			}
			outBytes += fi.Size()
			nCopy++
			continue
		}
		flat, fellBack := toMonoRate(w.pcm, w.channels, w.rate, *rate)
		if fellBack {
			nFall++
			fmt.Printf("  ⚠️ %-20s 两声道相消 ⇒ 只留较响的那一路\n", n)
		}
		mono := trimTail(flat, *rate)
		if err := writeWav16(dst, mono, *rate); err != nil {
			return err
		}
		outBytes += 44 + int64(len(mono))*2
		nConv++

		if !*noVerify {
			if err := verifyWav(dst, w, mono, *rate); err != nil {
				return fmt.Errorf("自检失败（%s）：%w", n, err)
			}
		}
	}

	// sound.lst 原样带过去（编号不变、文件名不变 ⇒ 客户端零改动）
	lst := filepath.Join(*src, "sound.lst")
	if fi, err := os.Stat(lst); err == nil {
		if err := copyFile(lst, filepath.Join(*out, "sound.lst")); err != nil {
			return err
		}
		outBytes += fi.Size()
	} else {
		fmt.Println("  ⚠️ 源目录里没有 sound.lst —— 客户端会找不到编号表")
	}

	// ---- 自检②：源里"清单能播的编号"，产物里**一个都不能少** ----
	//
	// 这条是防"瘦身顺手把某个音效弄丢"的：清单里有 13 条编号在源里就没有文件
	//（与我们无关），剩下的每一条，源能播的产物也必须能播。
	if len(byNum) > 0 {
		var lost []int
		srcOK, outOK := 0, 0
		for num, name := range byNum {
			if _, err := os.Stat(filepath.Join(*src, name)); err != nil {
				continue
			}
			srcOK++
			if _, err := os.Stat(filepath.Join(*out, name)); err != nil {
				lost = append(lost, num)
			} else {
				outOK++
			}
		}
		if len(lost) > 0 {
			sort.Ints(lost)
			return fmt.Errorf("产物丢了 %d 个清单编号（源能播、产物没有）：%v", len(lost), lost)
		}
		fmt.Printf("自检②：清单编号 源可播 %d → 产物 %d ✓\n", srcOK, outOK)
	}

	fmt.Printf("\n转换 %d 个（其中 %d 个因两声道相消只留一路），原样搬 %d 个（BGM），跳过 %d 个\n",
		nConv, nFall, nCopy, nSkip)
	if nRespell > 0 {
		fmt.Printf("另有 %d 个文件名按 sound.lst 的拼写输出（大小写对齐，Linux 上才找得到）\n", nRespell)
	}
	fmt.Printf("源 %.1f MB  →  %s %.1f MB  （省 %.1f MB，%.1f×）\n",
		float64(inBytes)/1e6, *out, float64(outBytes)/1e6,
		float64(inBytes-outBytes)/1e6, float64(inBytes)/float64(outBytes))
	if nSkip > 0 {
		fmt.Printf("  （另有 %.1f MB 未产出：原版从不播，-keep-all 可保留）\n", float64(skippedBytes)/1e6)
	}
	return nil
}

// verifyWav 自检：读回产出的文件，核对头与内容**没走样**。
//
// 检查三件（都能抓出"接错声道/算错长度/变成静音"这类真错误）：
//  1. 头：PCM、单声道、16bit、采样率对；
//  2. 长度：与预期帧数一致（允许 1 帧舍入）；
//  3. 内容：峰值与源**同量级**。下界放到 40% 是因为下混本身会降峰（两个声道
//     不相关时平均后自然变小），而上界 120% 抓"越界/放大"。真正的灾难
//     ——"产出整段静音" —— 单独判（下面那条），那才是最该抓的。
func verifyWav(path string, src *wav, want []int16, rate int) error {
	got, err := readWav(path)
	if err != nil {
		return err
	}
	if got.format != 1 || got.channels != 1 || got.bits != 16 || got.rate != rate {
		return fmt.Errorf("头不对：fmt=%d ch=%d bits=%d rate=%d", got.format, got.channels, got.bits, got.rate)
	}
	if len(got.pcm) != len(want) {
		return fmt.Errorf("帧数不符：%d ≠ %d", len(got.pcm), len(want))
	}
	ps, pg := peak(src.pcm), peak(got.pcm)
	if ps > 0 && (pg < ps*40/100 || pg > ps*120/100) {
		return fmt.Errorf("峰值走样：源 %d → 产出 %d", ps, pg)
	}
	if pg == 0 && ps != 0 {
		return errors.New("产出是静音")
	}
	return nil
}

// ---------- 小工具 ----------

// wavNames 列出源目录里的 *.wav（排序稳定）。**只认 .wav**：我们把 mp3 那条路
// 也确认过了 —— 客户端集里没有任何 mp3（详见 README「音频」）。
func wavNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s 里没有 .wav", dir)
	}
	sort.Strings(names)
	return names, nil
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}

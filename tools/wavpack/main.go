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

	"github.com/algotao/mir2/internal/m2pk"
)

// BGM 文件（不降采样，保持立体声原样）。
//
// ⚠️ `main_theme.wav` 是**用户指定**收进来的（2026-10-08）：原版从不播它
// （播它的定时器被注释掉了，`PlayScn.pas:485-486`），但用户要求"服务器选择到登录
// 那一段的背景音是 main_theme"。收它只多 6.3 MB —— 比 `-keep-all` 把
// `Field2.wav`（33.8 MB，确实没人播）也拉进来划算得多。
var bgmNames = map[string]bool{
	"log-in-long2.wav":  true,
	"sellect-loop2.wav": true,
	"field2.wav":        true, // 只在 survey 里用得上；pack 默认跳过它
	"game over2.wav":    true,
	"main_theme.wav":    true, // 用户指定：登录屏用它（见上）
}

// 原版**从不播**的长文件：默认不产出（`-keep-all` 覆盖）。
var unusedNames = map[string]string{
	"Field2.wav":     "bmg_field 定义了但全代码未使用（SoundUtil.pas:33）",
	"Game-over2.wav": "与 log-in-long2.wav 逐字节相同（md5 已验）",
}

type wav struct {
	format   int // 1 = PCM，0x11 = IMA ADPCM
	channels int
	rate     int
	bits     int
	/// `fact` 段里的**真实帧数**（压缩格式才有；见 `readWav` 的说明）。
	fact int
	pcm  []int16
}

// WAVE 的 IMA ADPCM 编码号（`fmt` 的 `wFormatTag`）。
const wavFormatIMA = 0x11

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
	case "decode":
		err = cmdDecode(os.Args[2:])
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
	fmt.Fprint(os.Stderr, `wavpack —— 音频资产转换（源 = 客户端集的 mir2c/wav，产物 = 一个容器文件）

  wavpack survey -src DIR
  wavpack decode -in FILE -out FILE     # 解成 16bit PCM（与外部解码器对拍用）
  wavpack pack   -src DIR -out FILE [-codec adpcm|pcm] [-rate 22050] [-keep-all] [-bgm-pcm]

  -out 是【一个容器文件】（M2PK，kind=audio / codec=store，不压缩 —— 音频压不动）：
  所有 wav + 编号表 sound.lst（键 soundlist）都装在里面，客户端按规范化名字取。
  容器里的键是小写的 ⇒「清单写小写、文件写大写」那类问题【不存在】。

  -codec adpcm（默认）  4bit IMA ADPCM：体积 1/4，波形域编码（无预回声）、样本精确
  -codec pcm           16bit PCM：音效 22.05k 单声道，BGM 原样搬
  -bgm-pcm             BGM 保持 16bit PCM（音质优先），只对音效用 ADPCM
`)
}

// ---------- 读 / 写 WAV ----------

func readWav(path string) (*wav, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return readWavBytes(b)
}

// readWavBytes 解析一份 wav 字节（PCM 或 IMA ADPCM）。
//
// 分两层是因为打包时**不落盘**：转换后的字节直接在内存里被自检读回。
func readWavBytes(b []byte) (*wav, error) {
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
		case "fact":
			// 压缩格式（含 IMA ADPCM）用它记**真实帧数**：块是定长的，
			// 最后一块的补零会解出多余样本，只能靠这个数截断。
			if size >= 4 {
				w.fact = int(binary.LittleEndian.Uint32(b[body:]))
			}
		case "data":
			dataOff, dataLen = body, size
		}
		off = body + size + (size & 1)
	}
	if w.format == 0 || w.channels == 0 || w.rate == 0 || dataOff < 0 {
		return nil, errors.New("缺 fmt/data 块")
	}
	if w.format == wavFormatIMA {
		w.pcm = decodeIMA(b[dataOff:dataOff+dataLen], w.channels)
		if w.fact > 0 && w.fact*w.channels <= len(w.pcm) {
			w.pcm = w.pcm[:w.fact*w.channels]
		}
		w.bits = 4 // 报告用：这是 4bit 编码
		return &w, nil
	}
	if w.format != 1 {
		return nil, fmt.Errorf("不认识的编码（format=0x%X）", w.format)
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

func writeWav16(path string, pcm []int16, channels, rate int) error {
	return os.WriteFile(path, wav16Bytes(pcm, channels, rate), 0o644)
}

// wav16Bytes 生成 16bit PCM WAV 的字节。
func wav16Bytes(pcm []int16, channels, rate int) []byte {
	if channels < 1 {
		channels = 1
	}
	buf := make([]byte, 44+len(pcm)*2)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+len(pcm)*2))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:], uint32(rate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(rate*channels*2)) // byte rate
	binary.LittleEndian.PutUint16(buf[32:], uint16(channels*2))      // block align
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(len(pcm)*2))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(v))
	}
	return buf
}

// writeWavIMA 写一个**标准**的 IMA ADPCM WAV（`wFormatTag = 0x11`）。
//
// 头按微软那套：fmt 20 字节（含 cbSize 与 wSamplesPerBlock）+ **fact**（真实帧数，
// 压缩格式必需）+ data。写成标准格式还有个额外好处：外部工具也能读它来对拍
// （本机就拿 `afconvert` 验过一遍）。
func writeWavIMA(path string, pcm []int16, channels, rate int) error {
	return os.WriteFile(path, imaBytes(pcm, channels, rate), 0o644)
}

// imaBytes 生成 IMA ADPCM WAV（`wFormatTag = 0x11`）的字节。
func imaBytes(pcm []int16, channels, rate int) []byte {
	data := encodeIMA(pcm, channels)
	ba := imaBlockAlign(channels)
	perBlock := imaSamplesPerBlock(channels)
	frames := len(pcm) / channels
	avg := ba * rate / perBlock

	buf := make([]byte, 0, 12+8+20+8+4+8+len(data))
	le16 := func(v int) { buf = append(buf, byte(v), byte(v>>8)) }
	le32 := func(v int) { buf = append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
	ck := func(id string, size int) { buf = append(buf, id...); le32(size) }

	buf = append(buf, "RIFF"...)
	le32(4 + (8 + 20) + (8 + 4) + (8 + len(data)))
	buf = append(buf, "WAVE"...)
	ck("fmt ", 20)
	le16(wavFormatIMA)
	le16(channels)
	le32(rate)
	le32(avg)      // nAvgBytesPerSec（由块长与每块样本数推出）
	le16(ba)       // nBlockAlign
	le16(4)        // wBitsPerSample：ADPCM 写 4
	le16(2)        // cbSize
	le16(perBlock) // wSamplesPerBlock
	ck("fact", 4)
	le32(frames)
	ck("data", len(data))
	buf = append(buf, data...)
	return buf
}

// ---------- 转换 ----------

// convert 把交错 PCM 变成"目标声道数 + 目标采样率"。
//
//	音效：44.1k 立体声 → 22.05k **单声道**（原版没有 pan/距离衰减 ⇒ 不丢游戏信息）
//	BGM ：**两者都不动**（dstCh = 源声道、dstRate = 源采样率 ⇒ 只走编码那一步）
//
// 重采样用线性插值就够：这些是短音效/循环音乐，不是母带处理。
//
// ⚠️ 返回值第二项 = "为了避开**相位相消**改用了较响的那一路"。立体声两声道反相时，
// 平均会**变成静音**（真实存在的一类素材 bug）—— 那时下混等于把这条音效弄丢，
// 所以宁可只留一路（听感上比静音好得多）。
func convert(src []int16, srcCh, srcRate, dstCh, dstRate int) ([]int16, bool) {
	if srcCh < 1 || dstCh < 1 {
		return nil, false
	}
	frames := len(src) / srcCh
	if frames == 0 {
		return nil, false
	}

	var mid []int16
	fellBack := false
	switch {
	case dstCh == srcCh:
		mid = src
	case dstCh == 1:
		mid = make([]int16, frames)
		for i := 0; i < frames; i++ {
			s := 0
			for c := 0; c < srcCh; c++ {
				s += int(src[i*srcCh+c])
			}
			mid[i] = int16(s / srcCh)
		}
		if srcCh > 1 && peak(src) > 0 && peak(mid)*4 < peak(src) {
			loudest, best := 0, 0
			for c := 0; c < srcCh; c++ {
				col := make([]int16, frames)
				for i := 0; i < frames; i++ {
					col[i] = src[i*srcCh+c]
				}
				if p := peak(col); p > best {
					best, loudest = p, c
				}
			}
			for i := 0; i < frames; i++ {
				mid[i] = src[i*srcCh+loudest]
			}
			fellBack = true
		}
	default:
		// 单声道 → 多声道：复制（目前用不到，留着免得静默出错）
		mid = make([]int16, frames*dstCh)
		for i := 0; i < frames; i++ {
			for c := 0; c < dstCh; c++ {
				mid[i*dstCh+c] = src[i]
			}
		}
	}

	if srcRate == dstRate {
		return mid, fellBack
	}
	outFrames := int(float64(frames) * float64(dstRate) / float64(srcRate))
	out := make([]int16, outFrames*dstCh)
	for j := 0; j < outFrames; j++ {
		pos := float64(j) * float64(srcRate) / float64(dstRate)
		i0 := int(pos)
		if i0 > frames-2 {
			i0 = frames - 2
		}
		if i0 < 0 {
			i0 = 0
		}
		frac := pos - float64(i0)
		for c := 0; c < dstCh; c++ {
			a := float64(mid[i0*dstCh+c])
			b := float64(mid[min(i0+1, frames-1)*dstCh+c])
			out[j*dstCh+c] = int16(a + (b-a)*frac)
		}
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

// cmdDecode 把任意支持的 wav（PCM / IMA ADPCM）解成 16bit PCM WAV。
//
// 存在的理由：**与外部解码器对拍**。我们就是这么用 macOS 自带的 `afconvert`
// 验过一遍 —— 它解出来的样本与本解码器逐字节一致，才敢相信 SNR 那些数字。
func cmdDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	in := fs.String("in", "", "输入 wav")
	out := fs.String("out", "", "输出 PCM wav")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("要 -in FILE 与 -out FILE")
	}
	w, err := readWav(*in)
	if err != nil {
		return err
	}
	if err := writeWav16(*out, w.pcm, w.channels, w.rate); err != nil {
		return err
	}
	fmt.Printf("%s: fmt=0x%X %dch %dHz %d 帧 → %s\n",
		filepath.Base(*in), w.format, w.channels, w.rate, len(w.pcm)/w.channels, filepath.Base(*out))
	return nil
}

// assetKey 把文件名变成**容器内的键**（与客户端 `sound::asset_key` 同一套规则）。
//
// 规则：取 basename、砍掉最后一个扩展名、转小写、把 `[a-z0-9_~-]` 之外的字符
// 换成 `_`（`Game over2.wav` → `game_over2`）。
//
// 为什么要有它：容器里的键是唯一的真相，客户端集里那些
// "清单写小写、文件写大写"的坑（`game-over2.wav` vs `Game-over2.wav`）在容器里
// **根本不存在** —— 这也是把这些 wav 打成一个文件的好处之一。
func assetKey(name string) string {
	base := name
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '~':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
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
	out := fs.String("out", "", "输出**容器文件**（如 assets/audio/sounds.m2pk）")
	rate := fs.Int("rate", 22050, "音效目标采样率（BGM 不动）")
	codec := fs.String("codec", "adpcm", "adpcm（4bit IMA，体积 1/4）| pcm（16bit）")
	keepAll := fs.Bool("keep-all", false, "连原版从不播的长文件也一起产出")
	noVerify := fs.Bool("no-verify", false, "跳过自检")
	snrMin := fs.Float64("snr-min", 5, "ADPCM 自检的 SNR 下限（dB）：只抓「彻底写坏」")
	bgmPcm := fs.Bool("bgm-pcm", false, "BGM 保持 16bit PCM（音质优先），只对音效用 ADPCM")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src == "" || *out == "" {
		return errors.New("要 -src DIR 与 -out FILE")
	}
	if *codec != "adpcm" && *codec != "pcm" {
		return fmt.Errorf("-codec 只认 adpcm / pcm，给了 %q", *codec)
	}
	names, err := wavNames(*src)
	if err != nil {
		return err
	}
	byNum, _, err := soundList(*src)
	if err != nil {
		fmt.Printf("  ⚠️ 读不到 sound.lst（%v）：跳过规则与自检都失效\n", err)
		byNum = map[int]string{}
	}
	// 清单引用了哪些文件名（小写比较，用于"别把清单要的文件跳掉"）
	referenced := map[string]bool{}
	for _, name := range byNum {
		referenced[strings.ToLower(name)] = true
	}

	var inBytes, skippedBytes int64
	var nConv, nCopy, nSkip, nFall int
	var snrs []float64
	type snrOf struct {
		name string
		db   float64
	}
	var snrList []snrOf
	var srcs []m2pk.Source         // 容器条目（名字 → 字节）
	keyOf := map[string]string{}   // 原始文件名 → 容器键（自检②用）
	keySeen := map[string]string{} // 容器键 → 原始文件名（查重名）

	for _, n := range names {
		p := filepath.Join(*src, n)
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		inBytes += fi.Size()

		if why, unused := unusedNames[n]; unused && !*keepAll {
			if referenced[strings.ToLower(n)] {
				// 原版代码不播它，但清单里**有编号指向它** ⇒ 照常进容器（少一个音效
				// 比多占几 MB 糟糕得多；这正是自检②要抓的那类遗漏）。
				fmt.Printf("  注意 %-18s 原版不播（%s），但 sound.lst 引用了它 ⇒ 照常进容器\n", n, why)
			} else {
				skippedBytes += fi.Size()
				nSkip++
				fmt.Printf("  跳过 %-18s %6.2f MB  —— %s\n", n, float64(fi.Size())/1e6, why)
				continue
			}
		}

		key := assetKey(n)
		if prev, dup := keySeen[key]; dup {
			return fmt.Errorf("容器键冲突：%q 与 %q 都规范化成 %q", prev, n, key)
		}
		keySeen[key] = n
		keyOf[n] = key

		isBgm := bgmNames[strings.ToLower(n)]
		w, err := readWav(p)
		if err != nil {
			// 不认识的（既非 PCM 也非 ADPCM）原样进容器，别把素材弄丢
			fmt.Printf("  ⚠️ %s: %v ⇒ 原样进容器\n", n, err)
			b, err2 := os.ReadFile(p)
			if err2 != nil {
				return err2
			}
			srcs = append(srcs, m2pk.Source{Name: key, Data: b})
			nCopy++
			continue
		}

		// BGM **不动采样率与声道**（音乐降采样/下混听得出来）；音效降到目标采样率、
		// 单声道（原版没有 pan/距离衰减 ⇒ 单声道不丢游戏信息）。
		dstCh, dstRate := 1, *rate
		if isBgm {
			dstCh, dstRate = w.channels, w.rate
		}
		flat, fellBack := convert(w.pcm, w.channels, w.rate, dstCh, dstRate)
		if fellBack {
			nFall++
			fmt.Printf("  ⚠️ %-20s 两声道相消 ⇒ 只留较响的那一路\n", n)
		}
		flat = trimTail(flat, dstRate)

		// BGM 在 `-bgm-pcm` 下保持 16bit PCM（音乐优先），其余按 `-codec`：
		// 一个开关就能给出"音乐无损 + 音效小"这个折中。
		perCodec := *codec
		if isBgm && *bgmPcm {
			perCodec = "pcm"
		}
		var data []byte
		if perCodec == "pcm" {
			data = wav16Bytes(flat, dstCh, dstRate)
		} else {
			data = imaBytes(flat, dstCh, dstRate)
		}
		if !*noVerify {
			snr, err := verifyWav(data, w, flat, dstCh, dstRate, perCodec, *snrMin)
			if err != nil {
				return fmt.Errorf("自检失败（%s）：%w", n, err)
			}
			if perCodec == "adpcm" {
				snrs = append(snrs, snr)
				snrList = append(snrList, snrOf{name: n, db: snr})
			}
		}
		srcs = append(srcs, m2pk.Source{Name: key, Data: data})
		nConv++
	}

	// 清单也进容器：客户端从容器里读它（键固定 `soundlist`）。
	lstPath := filepath.Join(*src, "sound.lst")
	if lst, err := os.ReadFile(lstPath); err == nil {
		srcs = append(srcs, m2pk.Source{Name: "soundlist", Data: lst})
	} else {
		fmt.Println("  ⚠️ 源目录里没有 sound.lst —— 客户端会找不到编号表")
	}
	// ⚠️ 名字必须升序：读侧靠它二分（`Archive::lookup`）
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Name < srcs[j].Name })

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	st, err := m2pk.Pack(*out, srcs, m2pk.Options{Kind: m2pk.KindAudio, Codec: m2pk.CodecStore})
	if err != nil {
		return err
	}

	// ---- 自检②：**从容器里读回来**，源能播的编号一个都不能少 ----
	r, err := m2pk.Open(*out)
	if err != nil {
		return err
	}
	defer r.Close()
	if len(byNum) > 0 {
		srcOK, outOK := 0, 0
		var lost []int
		for num, name := range byNum {
			if _, err := os.Stat(filepath.Join(*src, name)); err != nil {
				continue // 源里就没有（清单里有 13 条这样），与我们无关
			}
			srcOK++
			if _, ok := r.Lookup(assetKey(name)); ok {
				outOK++
			} else {
				lost = append(lost, num)
			}
		}
		if len(lost) > 0 {
			sort.Ints(lost)
			return fmt.Errorf("容器丢了 %d 个清单编号（源能播、容器里没有）：%v", len(lost), lost)
		}
		fmt.Printf("自检②：清单编号 源可播 %d → 容器 %d ✓\n", srcOK, outOK)
	}
	if _, ok := r.Lookup("soundlist"); !ok {
		return errors.New("容器里没有 soundlist（客户端读不到编号表）")
	}

	// ---- 自检①的汇总：ADPCM 是有损的，得有个数字说话 ----
	if len(snrs) > 0 {
		sorted := append([]float64(nil), snrs...)
		sort.Float64s(sorted)
		sum := 0.0
		for _, v := range sorted {
			sum += v
		}
		// 4bit IMA 的 SNR 大致 15~25 dB（看素材）；很低的多半是"噪声型内容"或
		// "本来就只有 8bit"的短音效 —— 那种 SNR 没有意义。所以下限定得低，
		// 只抓"编码器系统性写错"，并把分布打出来让人自己判断。
		fmt.Printf("自检①：解码回来 vs 原 PCM 的 SNR —— 最小 %.1f dB / 中位 %.1f dB / 均值 %.1f dB\n",
			sorted[0], sorted[len(sorted)/2], sum/float64(len(sorted)))
		// 把最差的几个点名：4bit 对"噪声型"素材最吃亏，这几条值得**用耳朵**复核。
		sort.Slice(snrList, func(i, j int) bool { return snrList[i].db < snrList[j].db })
		worst := min(5, len(snrList))
		names := make([]string, 0, worst)
		for _, x := range snrList[:worst] {
			names = append(names, fmt.Sprintf("%s %.1fdB", x.name, x.db))
		}
		fmt.Printf("      最差 %d 个：%s\n", worst, strings.Join(names, "、"))
	}

	fmt.Printf("\n[%s] 转换 %d 个（%d 个两声道相消只留一路），原样进容器 %d 个，跳过 %d 个\n",
		*codec, nConv, nFall, nCopy, nSkip)
	fmt.Printf("源 %.1f MB  →  %s  %.1f MB（%d 块，含清单）  （省 %.1f MB，%.1f×）\n",
		float64(inBytes)/1e6, *out, float64(st.FileSize)/1e6, st.Count,
		float64(inBytes-int64(st.FileSize))/1e6, float64(inBytes)/float64(st.FileSize))
	if nSkip > 0 {
		fmt.Printf("  （另有 %.1f MB 未产出：原版从不播，-keep-all 可保留）\n", float64(skippedBytes)/1e6)
	}
	return nil
}

// verifyWav 自检：**把产出的文件读回来**（ADPCM 会解码成 PCM），核对三件事：
//
//  1. 头：编码号（PCM 记 1 / IMA 记 0x11）、声道数、采样率、（PCM 时）16bit；
//  2. 长度：帧数与预期一致（允许声道数那么多帧的取整）；
//  3. 内容：**解码结果与预期 PCM 的 SNR**（ADPCM 有量化噪声，低于 15 dB 说明
//     编码器或头写坏了），外加峰值同量级、不能整段静音。
//
// 返回 SNR（dB）供调用方汇总 —— ADPCM 是有损的，得有个数字说话。
// 下界之所以放到 40%：下混本身会降峰（两声道不相关时平均后自然变小）；
// 上界 130% 抓"越界/放大"。真正该抓的灾难是"整段静音"，单独判。
func verifyWav(data []byte, src *wav, want []int16, channels, rate int, codec string, snrMin float64) (float64, error) {
	got, err := readWavBytes(data)
	if err != nil {
		return 0, err
	}
	if got.channels != channels || got.rate != rate {
		return 0, fmt.Errorf("头不对：ch=%d（期望 %d）rate=%d（期望 %d）",
			got.channels, channels, got.rate, rate)
	}
	if codec == "pcm" && (got.format != 1 || got.bits != 16) {
		return 0, fmt.Errorf("PCM 模式下头不对：fmt=%d bits=%d", got.format, got.bits)
	}
	if codec == "adpcm" && got.format != wavFormatIMA {
		return 0, fmt.Errorf("ADPCM 模式下头不对：fmt=0x%X", got.format)
	}
	if d := len(got.pcm) - len(want); d > channels || d < -channels {
		return 0, fmt.Errorf("帧数不符：%d ≠ %d", len(got.pcm), len(want))
	}
	ps, pg := peak(src.pcm), peak(got.pcm)
	if ps > 0 && (pg < ps*40/100 || pg > ps*130/100) {
		return 0, fmt.Errorf("峰值走样：源 %d → 产出 %d", ps, pg)
	}
	if pg == 0 && ps != 0 {
		return 0, errors.New("产出是静音")
	}
	n := min(len(got.pcm), len(want))
	snr := snrDB(want[:n], got.pcm[:n])
	if codec == "adpcm" && snr < snrMin {
		return snr, fmt.Errorf("SNR 太低：%.1f dB（下限 %.1f；编码器或头写坏了？）", snr, snrMin)
	}
	return snr, nil
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

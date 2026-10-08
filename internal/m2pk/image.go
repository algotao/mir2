// IMGP —— 美术图库（`.wzl` + `.wzx`）的**分组拼接**载荷。
//
// 它作为 M2PK 容器 `kind=3`（[KindImage]）的**块内容**存在，容器头 codec = [CodecStore]：
// 块里已经是我们的分组 brotli 流，容器**不再压**（再压一层既无用又费时）。
//
// 为什么要有这一层（实测见 docs/assets.md §5b）：
//
//  1. `.wzl` 里**每张图各自一个 zlib 流**，deflate 的窗口只有 32 KB ⇒ 跨图上下文全丢；
//     同一动作的连续帧本来高度相似，却压不到一起；
//  2. 把每张图的原始像素**解出来**、按 N 张一组拼接、每组一条 brotli 流，
//     实测 type 3（8bit 索引）83.8%、type 5（RGB565）76.5%（相对现状），全量省 ~20%；
//  3. 组是**随机访问的单位**（客户端一组的解压 + 缓存），所以 N 不能太大；
//     8/16/32/64 的拐点由 `tools/artpack -group` 实测决定。
//
// 布局（全部小端）：
//
//	Header（16 B）
//	  0   magic      [4]  "IMGP"
//	  4   version    u8   1
//	  5   reserved   [3]
//	  8   count      u32  图数（与源 `.wzx` 的图数一致，**索引语义按位对齐**）
//	  12  groupSize  u16  每组图数（1..MaxImageGroup）
//	  14  reserved2  u16
//
//	组表（groups × 12 B，紧随 Header）
//	  0   offset   u32  相对数据区起点
//	  4   compLen  u32  brotli 流字节数
//	  8   rawLen   u32  解压后字节数（= 组内各图 raw 顺序拼接的长度）
//
//	记录表（count × 16 B，紧随组表）—— 与 `.wzl` 记录**同构**，只是 `packed_size`
//	字段不再使用（写 0）；宽高为 0 表示**空白图**（源里 `packed_size == 0` 的那些，
//	解码返回 `None`，与直读 `.wzl` 的行为一致）。
//
//	数据区（8 字节对齐）：每组一条独立 brotli 流，组内是各图 raw 像素的顺序拼接；
//	raw 的布局与 `.wzl` 解压后**完全一致**（行 4 字节对齐、自下而上），因此像素
//	解码器一个字都不用改。
//
// **索引语义**：图 `i` 恒等于源 `.wzl` 的第 `i` 张（含空白图占位），且
// `组号 = i / groupSize`、组内偏移 = 该组内前若干张 raw 长度之和 —— 全部可推导，
// 因此记录表里不存组号/偏移（这 6 字节省下来，记录表与 `.wzl` 记录同为 16 字节）。
package m2pk

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
)

// IMGP 常量。
const (
	ImageMagic      = "IMGP"
	ImageVersion    = 1
	ImageHeaderSize = 16
	ImageGroupEntry = 12
	ImageRecordSize = 16
	// DefaultImageGroup 是默认每组图数。定这个值的依据是实测拐点（见 docs/assets.md §5c）。
	DefaultImageGroup = 32
	// MaxImageGroup 是每组图数上限：组内偏移在客户端按 u32 累加，1024 张
	// 单张最大 512×512×2 也放得下。
	MaxImageGroup = 1024
)

// ErrBadImageLib 表示 IMGP 载荷结构损坏。
var ErrBadImageLib = fmt.Errorf("m2pk: IMGP 载荷损坏")

// ImageRecord 是一条图像记录（与 `.wzl` 的 16 字节记录同构）。
type ImageRecord struct {
	TypeFlag uint8
	Width    uint16
	Height   uint16
	AnchorX  int16
	AnchorY  int16
}

// Stride 是每行在 raw 数据里占的字节数（**4 字节对齐**）。
//
// 与 Rust 侧 `wzl::stride_of` 必须一致：`((w*bits + 31) >> 5) * 4`。
func (r ImageRecord) Stride() int {
	bits := 8
	if r.TypeFlag == 5 {
		bits = 16
	}
	return ((int(r.Width)*bits + 31) >> 5) * 4
}

// RawLen 是这张图 raw 像素的字节数（空白图为 0）。
func (r ImageRecord) RawLen() int {
	if r.Width == 0 || r.Height == 0 {
		return 0
	}
	return r.Stride() * int(r.Height)
}

// Blank 表示这张图没有像素数据（解码返回 None）。
func (r ImageRecord) Blank() bool { return r.RawLen() == 0 }

// ImageSource 按索引提供图库内容。
//
// 打包器**逐图**调用 `Raw`（而不是一次要全部）⇒ 内存只与一组有关，
// 1.4 GB 的美术素材也能在几百 MB 内存里打包完。
type ImageSource interface {
	Count() int
	Record(i int) ImageRecord
	// Raw 返回第 i 张图的原始像素（空白图返回 nil）。
	Raw(i int) ([]byte, error)
}

// ImageStats 是一次 IMGP 编码的统计（工具用来打印与体检）。
type ImageStats struct {
	Count     int   // 图数
	Groups    int   // 组数
	Blank     int   // 空白图（源里就没有像素）
	Broken    int   // 源里解不开/长度不对的图（**按空白处理**，但计数暴露出来）
	RawTotal  int64 // 各图 raw 合计（未压缩）
	CompTotal int64 // 各组 brotli 流合计
}

// WzlSource 直读一对 `.wzl` + `.wzx`（打包器的输入侧）。
type WzlSource struct {
	path string
	data []byte
	offs []uint32
	// broken 记录"源里解不开"的图号，供体检打印
	broken map[int]string
}

// OpenWzlSource 打开 `{base}.wzl` + `{base}.wzx`（base 不含扩展名）。
func OpenWzlSource(base string) (*WzlSource, error) {
	data, err := os.ReadFile(base + ".wzl")
	if err != nil {
		return nil, err
	}
	wzx, err := os.ReadFile(base + ".wzx")
	if err != nil {
		return nil, err
	}
	if len(wzx) < 48 {
		return nil, fmt.Errorf("%w: %s.wzx 太短（%d 字节）", ErrBadImageLib, filepath.Base(base), len(wzx))
	}
	count := int(binary.LittleEndian.Uint32(wzx[44:48]))
	if count <= 0 {
		return nil, fmt.Errorf("%w: %s.wzx 图数为 0", ErrBadImageLib, filepath.Base(base))
	}
	if len(wzx) < 48+4*count {
		return nil, fmt.Errorf("%w: %s.wzx 偏移表越界（count=%d）", ErrBadImageLib, filepath.Base(base), count)
	}
	offs := make([]uint32, count)
	for i := 0; i < count; i++ {
		offs[i] = binary.LittleEndian.Uint32(wzx[48+4*i:])
	}
	return &WzlSource{path: base, data: data, offs: offs, broken: map[int]string{}}, nil
}

// Path 返回源库路径（不含扩展名）。
func (s *WzlSource) Path() string { return s.path }

// Len 返回源 `.wzl` 的字节数（体检打印用）。
func (s *WzlSource) Len() int { return len(s.data) }

func (s *WzlSource) Count() int { return len(s.offs) }

// parseRecord 解析第 i 张的记录；第二个返回值是 `packed_size`。
func (s *WzlSource) parseRecord(i int) (ImageRecord, uint32, bool) {
	o := int(s.offs[i])
	if o < 64 || o+ImageRecordSize > len(s.data) {
		return ImageRecord{}, 0, false
	}
	b := s.data[o : o+ImageRecordSize]
	return ImageRecord{
		TypeFlag: b[0],
		Width:    binary.LittleEndian.Uint16(b[4:]),
		Height:   binary.LittleEndian.Uint16(b[6:]),
		AnchorX:  int16(binary.LittleEndian.Uint16(b[8:])),
		AnchorY:  int16(binary.LittleEndian.Uint16(b[10:])),
	}, binary.LittleEndian.Uint32(b[12:]), true
}

// Record 返回第 i 张的记录；不可用（结构坏 / 空白）时返回零值记录。
//
// ⚠️ 这里就把"空白"归一化了：源里 `packed_size == 0` 的图，记录一律写成零值 ——
// 与 Rust 侧 `Wzl::decode` 返回 `None` 的判据（w/h/packed_size 有 0）对齐。
func (s *WzlSource) Record(i int) ImageRecord {
	rec, packed, ok := s.parseRecord(i)
	if !ok || packed == 0 || rec.Width == 0 || rec.Height == 0 {
		return ImageRecord{}
	}
	return rec
}

// Raw 返回第 i 张图的原始像素。
//
// 空白图返回 `(nil, nil)`；源里解不开或长度不对的图**也**返回 `(nil, nil)`
// 但登记进 `Broken()` —— 因为直读路径对这类图就是"解码得到 None"，
// 分组载荷必须保持同样的可见行为（宁可少一张图，也不能让索引错位）。
func (s *WzlSource) Raw(i int) ([]byte, error) {
	rec, packed, ok := s.parseRecord(i)
	if !ok {
		s.broken[i] = "记录越界"
		return nil, nil
	}
	if packed == 0 || rec.Width == 0 || rec.Height == 0 {
		return nil, nil
	}
	start := int(s.offs[i]) + ImageRecordSize
	end := start + int(packed)
	if end > len(s.data) {
		s.broken[i] = "数据越界"
		return nil, nil
	}
	raw, err := zlibInflate(s.data[start:end])
	if err != nil {
		s.broken[i] = "zlib 解压失败"
		return nil, nil
	}
	if len(raw) < rec.RawLen() {
		s.broken[i] = fmt.Sprintf("解压后 %d 字节 < 声明 %d", len(raw), rec.RawLen())
		return nil, nil
	}
	return raw[:rec.RawLen()], nil
}

// Broken 返回"源里就不对"的图号 → 原因。
func (s *WzlSource) Broken() map[int]string { return s.broken }

func zlibInflate(src []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// EncodeImageLib 把 `src` 编码成 IMGP 载荷。
//
// 各组**独立编码**（因此 workers 不影响输出字节，与 `Pack` 同一纪律）。
func EncodeImageLib(src ImageSource, groupSize, workers int) ([]byte, ImageStats, error) {
	var st ImageStats
	count := src.Count()
	if count <= 0 {
		return nil, st, fmt.Errorf("m2pk: 图库为空")
	}
	if groupSize <= 0 {
		groupSize = DefaultImageGroup
	}
	if groupSize > MaxImageGroup {
		return nil, st, fmt.Errorf("m2pk: 每组图数 %d 超过上限 %d", groupSize, MaxImageGroup)
	}

	// 记录表（小：16 B/图）先定下来，组内偏移与"空白"归一化都基于它。
	recs := make([]ImageRecord, count)
	for i := range recs {
		recs[i] = src.Record(i)
		if recs[i].Blank() {
			st.Blank++
		}
	}

	groups := (count + groupSize - 1) / groupSize
	comp := make([][]byte, groups)
	rawLen := make([]uint32, groups)
	broken := 0

	if workers <= 0 {
		// ⚠️ 别写死：本机 14 核却按 8 跑，打包慢 ~40%（实测利用率 761%）。
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > groups {
		workers = groups
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var acc bytes.Buffer
			for g := range jobs {
				mu.Lock()
				failed := firstErr != nil
				mu.Unlock()
				if failed {
					continue
				}
				lo := g * groupSize
				hi := lo + groupSize
				if hi > count {
					hi = count
				}
				acc.Reset()
				for i := lo; i < hi; i++ {
					if recs[i].Blank() {
						continue
					}
					raw, err := src.Raw(i)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("m2pk: 读第 %d 张图失败: %w", i, err)
						}
						mu.Unlock()
						break
					}
					// 源里解不开 ⇒ 归一化成空白（与直读行为一致），但要计数
					if len(raw) == 0 {
						mu.Lock()
						recs[i] = ImageRecord{}
						broken++
						mu.Unlock()
						continue
					}
					acc.Write(raw)
				}
				var out bytes.Buffer
				bw := brotli.NewWriterOptions(&out, brotli.WriterOptions{
					Quality: DefaultQuality,
					LGWin:   DefaultLGWin,
				})
				if _, err := bw.Write(acc.Bytes()); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				if err := bw.Close(); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				comp[g] = out.Bytes()
				rawLen[g] = uint32(acc.Len())
			}
		}()
	}
	for g := 0; g < groups; g++ {
		jobs <- g
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, st, firstErr
	}

	// 组装：Header | 组表 | 记录表 | 8 对齐 | 各组流
	tableEnd := ImageHeaderSize + ImageGroupEntry*groups + ImageRecordSize*count
	dataOff := align8(tableEnd)
	out := make([]byte, dataOff)
	copy(out[0:], ImageMagic)
	out[4] = ImageVersion
	binary.LittleEndian.PutUint32(out[8:], uint32(count))
	binary.LittleEndian.PutUint16(out[12:], uint16(groupSize))

	rel := 0
	for g := 0; g < groups; g++ {
		e := out[ImageHeaderSize+ImageGroupEntry*g:]
		binary.LittleEndian.PutUint32(e[0:], uint32(rel))
		binary.LittleEndian.PutUint32(e[4:], uint32(len(comp[g])))
		binary.LittleEndian.PutUint32(e[8:], rawLen[g])
		out = append(out, comp[g]...)
		rel += len(comp[g])
		st.RawTotal += int64(rawLen[g])
		st.CompTotal += int64(len(comp[g]))
		for rel%8 != 0 { // 组间 8 字节对齐（与容器块同一习惯）
			out = append(out, 0)
			rel++
		}
	}
	for i, rec := range recs {
		b := out[ImageHeaderSize+ImageGroupEntry*groups+ImageRecordSize*i:]
		b[0] = rec.TypeFlag
		binary.LittleEndian.PutUint16(b[4:], rec.Width)
		binary.LittleEndian.PutUint16(b[6:], rec.Height)
		binary.LittleEndian.PutUint16(b[8:], uint16(rec.AnchorX))
		binary.LittleEndian.PutUint16(b[10:], uint16(rec.AnchorY))
	}
	st.Count = count
	st.Groups = groups
	st.Broken = broken
	return out, st, nil
}

// ImageLib 是 IMGP 载荷的只读视图（Go 侧：工具的 `verify` / `info` 用）。
//
// 客户端**不这么用**：它按组懒解压 + 缓存（见 `client/core/src/image_lib.rs`）。
type ImageLib struct {
	payload   []byte
	count     int
	groupSize int
	groups    int
	dataOff   int
	recs      []ImageRecord
}

// OpenImageLib 解析并校验 IMGP 载荷。
func OpenImageLib(payload []byte) (*ImageLib, error) {
	if len(payload) < ImageHeaderSize {
		return nil, fmt.Errorf("%w: 载荷只有 %d 字节", ErrBadImageLib, len(payload))
	}
	if string(payload[0:4]) != ImageMagic {
		return nil, fmt.Errorf("%w: 魔数不是 IMGP", ErrBadImageLib)
	}
	if v := payload[4]; v != ImageVersion {
		return nil, fmt.Errorf("%w: 版本 %d", ErrBadImageLib, v)
	}
	count := int(binary.LittleEndian.Uint32(payload[8:12]))
	groupSize := int(binary.LittleEndian.Uint16(payload[12:14]))
	if count <= 0 || groupSize <= 0 || groupSize > MaxImageGroup {
		return nil, fmt.Errorf("%w: count=%d groupSize=%d", ErrBadImageLib, count, groupSize)
	}
	groups := (count + groupSize - 1) / groupSize
	dataOff := align8(ImageHeaderSize + ImageGroupEntry*groups + ImageRecordSize*count)
	if dataOff > len(payload) {
		return nil, fmt.Errorf("%w: 表区越界（需 %d，实有 %d）", ErrBadImageLib, dataOff, len(payload))
	}
	l := &ImageLib{
		payload:   payload,
		count:     count,
		groupSize: groupSize,
		groups:    groups,
		dataOff:   dataOff,
		recs:      make([]ImageRecord, count),
	}
	for i := 0; i < count; i++ {
		b := payload[ImageHeaderSize+ImageGroupEntry*groups+ImageRecordSize*i:]
		l.recs[i] = ImageRecord{
			TypeFlag: b[0],
			Width:    binary.LittleEndian.Uint16(b[4:]),
			Height:   binary.LittleEndian.Uint16(b[6:]),
			AnchorX:  int16(binary.LittleEndian.Uint16(b[8:])),
			AnchorY:  int16(binary.LittleEndian.Uint16(b[10:])),
		}
	}
	return l, nil
}

func (l *ImageLib) Count() int     { return l.count }
func (l *ImageLib) Groups() int    { return l.groups }
func (l *ImageLib) GroupSize() int { return l.groupSize }
func (l *ImageLib) Record(i int) ImageRecord {
	if i < 0 || i >= l.count {
		return ImageRecord{}
	}
	return l.recs[i]
}

// TableBytes 是表区字节数（Header + 组表 + 记录表，即数据区起点）。
func (l *ImageLib) TableBytes() int { return l.dataOff }

// RawBytes 是全部组解开后的字节数合计。
func (l *ImageLib) RawBytes() int64 {
	var n int64
	for g := 0; g < l.groups; g++ {
		e := l.payload[ImageHeaderSize+ImageGroupEntry*g:]
		n += int64(binary.LittleEndian.Uint32(e[8:]))
	}
	return n
}

// CompBytes 是全部组 brotli 流的字节数合计。
func (l *ImageLib) CompBytes() int64 {
	var n int64
	for g := 0; g < l.groups; g++ {
		e := l.payload[ImageHeaderSize+ImageGroupEntry*g:]
		n += int64(binary.LittleEndian.Uint32(e[4:]))
	}
	return n
}

// Group 解压第 g 组，返回组内拼接的 raw 像素。
func (l *ImageLib) Group(g int) ([]byte, error) {
	if g < 0 || g >= l.groups {
		return nil, fmt.Errorf("%w: 组号 %d 越界", ErrBadImageLib, g)
	}
	e := l.payload[ImageHeaderSize+ImageGroupEntry*g:]
	off := int(binary.LittleEndian.Uint32(e[0:]))
	compLen := int(binary.LittleEndian.Uint32(e[4:]))
	rawLen := int(binary.LittleEndian.Uint32(e[8:]))
	start := l.dataOff + off
	if start+compLen > len(l.payload) {
		return nil, fmt.Errorf("%w: 第 %d 组流越界", ErrBadImageLib, g)
	}
	raw, err := io.ReadAll(brotli.NewReader(bytes.NewReader(l.payload[start : start+compLen])))
	if err != nil {
		return nil, fmt.Errorf("%w: 第 %d 组解压失败: %v", ErrBadImageLib, g, err)
	}
	if len(raw) != rawLen {
		return nil, fmt.Errorf("%w: 第 %d 组解压 %d 字节，声明 %d", ErrBadImageLib, g, len(raw), rawLen)
	}
	return raw, nil
}

// ReadImagePayload 从容器里取出某库的 IMGP 载荷（**整块**读；工具用）。
//
// 客户端不这么用：它按范围读块内的表与组（见 `KindImage` 的说明）。
func ReadImagePayload(r *Reader, name string) (*ImageLib, error) {
	blk, ok, err := r.ReadName(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("m2pk: 容器里没有图库 %q", name)
	}
	return OpenImageLib(blk)
}

// OpenWzlSourcePath 用**带扩展名**的路径打开源库（`Hum.wzl` ⇒ 读 `Hum.wzl` + `Hum.wzx`）。
func OpenWzlSourcePath(wzlPath string) (*WzlSource, error) {
	base := strings.TrimSuffix(wzlPath, filepath.Ext(wzlPath))
	return OpenWzlSource(base)
}

// WzlImageCount 只读 `.wzx` 头拿到图数（不动 `.wzl`，打包前的体检用）。
//
// 用途：客户端集里有**空库**（`anitiles2` / `magic12` / `horse_520` 那类，
// `.wzx` 的 count = 0）。空库没有载荷可编，打包器要跳过它们 ——
// 而"容器里没有这个库"在客户端就是"回退到裸目录"，两边行为一致（都是空）。
func WzlImageCount(wzlPath string) (int, error) {
	base := strings.TrimSuffix(wzlPath, filepath.Ext(wzlPath))
	f, err := os.Open(base + ".wzx")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 48)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, fmt.Errorf("%w: %s.wzx 头部读不出（%v）", ErrBadImageLib, filepath.Base(base), err)
	}
	return int(binary.LittleEndian.Uint32(head[44:48])), nil
}

// VerifyImage 校验美术容器与源目录：**逐图解出**记录与 raw 像素，与直读 `.wzl` 比对。
//
// 为什么不是 `Verify` 那种"逐字节比源文件"：kind=3 的块内容是重排过的（每图各自
// zlib → 分组 brotli），字节层面本来就不等于源文件。但**语义**必须完全一致 ——
// 同样的图数、同样的索引、同样的记录、同样的 raw 像素，因此解码结果逐字节相同
// （客户端的黄金哈希测试会再兜一道底）。
func VerifyImage(containerPath, srcDir string) error {
	srcs, err := ScanDir(srcDir, []string{"wzl"})
	if err != nil {
		return err
	}
	r, err := Open(containerPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if r.kind != KindImage {
		return fmt.Errorf("m2pk: 容器 kind=%d，不是美术容器", r.kind)
	}

	var probs []string
	report := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	// 与打包器同一套跳过规则：空库（`.wzx` count = 0）与读不到索引的库
	// 本来就不进容器，回验时也不该被当成"容器缺条目"。
	var checked []Source
	for _, s := range srcs {
		n, err := WzlImageCount(s.Path)
		if err != nil || n <= 0 {
			continue
		}
		checked = append(checked, s)
	}
	srcs = checked

	inContainer := make(map[string]bool, r.Count())
	for _, e := range r.entries {
		inContainer[e.Name] = true
	}
	inSrc := make(map[string]bool, len(srcs))
	for _, s := range srcs {
		inSrc[s.Name] = true
	}
	for _, s := range srcs {
		if !inContainer[s.Name] {
			report("容器缺少图库: %s", s.Name)
		}
	}
	for _, e := range r.entries {
		if !inSrc[e.Name] {
			report("容器多出图库: %s", e.Name)
		}
	}

	total := 0
	for _, s := range srcs {
		if !inContainer[s.Name] {
			continue
		}
		src, err := OpenWzlSourcePath(s.Path)
		if err != nil {
			report("%s: 打开源库失败: %v", s.Name, err)
			continue
		}
		lib, err := ReadImagePayload(r, s.Name)
		if err != nil {
			report("%s: %v", s.Name, err)
			continue
		}
		if lib.Count() != src.Count() {
			report("%s: 图数不一致（源 %d，容器 %d）", s.Name, src.Count(), lib.Count())
			continue
		}
		bad := 0
		first := ""
		err = lib.EachImage(func(i int, rec ImageRecord, raw []byte) error {
			want := src.Record(i)
			if rec != want {
				if first == "" {
					first = fmt.Sprintf("第 %d 张记录不同（源 %+v，容器 %+v）", i, want, rec)
				}
				bad++
				return nil
			}
			wantRaw, err := src.Raw(i)
			if err != nil {
				return err
			}
			if !bytes.Equal(wantRaw, raw) {
				if first == "" {
					first = fmt.Sprintf("第 %d 张 raw 不同（源 %d 字节，容器 %d 字节）", i, len(wantRaw), len(raw))
				}
				bad++
			}
			return nil
		})
		if err != nil {
			report("%s: %v", s.Name, err)
			continue
		}
		if bad > 0 {
			report("%s: %d 张不一致，例如 %s", s.Name, bad, first)
		}
		total += lib.Count()
	}

	if len(probs) > 0 {
		n := len(probs)
		if n > 20 {
			probs = probs[:20]
		}
		return fmt.Errorf("m2pk: 美术校验失败（%d 处问题）:\n  %s", n, strings.Join(probs, "\n  "))
	}
	_ = total
	return nil
}

// EachImage 按组遍历全部图（每组只解压一次）。`raw` 直接指向组缓冲区。
func (l *ImageLib) EachImage(fn func(i int, rec ImageRecord, raw []byte) error) error {
	for g := 0; g < l.groups; g++ {
		raw, err := l.Group(g)
		if err != nil {
			return err
		}
		lo := g * l.groupSize
		hi := lo + l.groupSize
		if hi > l.count {
			hi = l.count
		}
		off := 0
		for i := lo; i < hi; i++ {
			n := l.recs[i].RawLen()
			if off+n > len(raw) {
				return fmt.Errorf("%w: 第 %d 张越界（组 %d 内偏移 %d，长 %d，组共 %d）",
					ErrBadImageLib, i, g, off, n, len(raw))
			}
			if err := fn(i, l.recs[i], raw[off:off+n]); err != nil {
				return err
			}
			off += n
		}
		if off != len(raw) {
			return fmt.Errorf("%w: 第 %d 组只用了 %d 字节，实有 %d", ErrBadImageLib, g, off, len(raw))
		}
	}
	return nil
}

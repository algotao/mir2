// Package m2pk 实现 M2PK 资产容器（v1）的读写与校验。
//
// 设计目标（规格见 docs/assets.md §5，决策见 docs/decisions.md D-11）：
//
//  1. **逐字节可回验**：容器只做「打包 + 索引 + 压缩」，每块的内容就是源文件的
//     **无损副本**。`verify` 因此是最强的校验——解压后与源文件逐字节比对。
//     容器**不理解**块内的语义（地图是 12/36/24 字节每格都与本包无关）。
//  2. **随机访问**：一块一图，按需解压单块，不必解开整个容器。
//  3. **确定性**：相同输入与参数产出相同字节，便于复现构建与入库比对。
//
// 文件布局（全部小端）：
//
//	Header（32 B）
//	  0   magic      [4]  "M2PK"
//	  4   version    u16  1
//	  6   kind       u8   1 = 地图 / 2 = 音频 / 3 = 美术
//	  7   codec      u8   0 = brotli
//	  8   count      u32  块数
//	  12  namesOff   u32  名字池偏移（相对文件头）
//	  16  namesLen   u32  名字池字节数
//	  20  dataOff    u32  数据区起始偏移
//	  24  reserved   [8]
//
//	Entry（24 B × count，紧随 Header；按名字升序，可二分）
//	  0   nameOff    u32  相对 namesOff
//	  4   nameLen    u8   名字字节数
//	  5   reserved   [3]
//	  8   offset     u64  块偏移（相对文件头）
//	  16  rawSize    u32  解压后字节数（= 源文件字节数）
//	  20  compSize   u32  压缩后字节数
//
//	名字池：各块名字（小写、无扩展名）顺序拼接，无分隔符
//	数据区：每块一个独立的 brotli 流，块起始按 8 字节对齐
package m2pk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
)

// 容器常量。
const (
	Magic      = "M2PK"
	Version    = 1
	HeaderSize = 32
	EntrySize  = 24

	// KindMap 表示地图容器。
	KindMap = 1
	// KindAudio 表示音频容器（音效 + BGM + 清单）。
	//
	// 为什么音频也进容器（而不是一堆散文件）：
	//  1. 一个文件好分发/好校验，也不再受**文件名大小写**影响 —— 容器里的键是
	//     `CanonicalName` 规范化过的（小写、无扩展名），`Game-over2.wav` 与
	//     `game-over2.wav` 在里面**就是同一个键**（这正是客户端集素材的坑，
	//     见 docs/assets.md §6b）；
	//  2. 音频走 `CodecStore`（**不压缩**）：实测 PCM 用 zlib/xz 只能压到 87~96%，
	//     4bit ADPCM 更压不动 ⇒ 压缩纯属浪费 CPU（见 D-29）。
	KindAudio = 2
	// KindImage 表示美术容器（图库 `.wzl`/`.wzx`）。
	//
	// 与地图/音频不同，它的块内容**不是源文件字节**，而是 `image.go` 定义的
	// IMGP 分组载荷（每库一块，块内已按 N 张一组拼好并各自 brotli），因此：
	//
	//  1. codec 恒为 CodecStore —— 再套一层 brotli 毫无意义；
	//  2. `Verify`（逐字节比对源文件）**不适用**，图库有专门的 `VerifyImage`：
	//     逐图解出来与直读 `.wzl` 的记录 + raw 像素比对（语义等价，仍是全量无损）。
	KindImage = 3
	// CodecBrotli 表示块用 brotli 压缩（地图用）。
	CodecBrotli = 0
	// CodecStore 表示块**原样存储**（音频用；见 KindAudio 的说明）。
	CodecStore = 1

	// DefaultQuality 是 brotli 质量档（0–11）。11 = 体积优先。
	DefaultQuality = 11
	// DefaultLGWin 是 brotli 窗口的对数，24 ⇒ 16 MiB 窗口。
	DefaultLGWin = 24
)

// 错误。
var (
	ErrBadMagic     = errors.New("m2pk: 魔数不匹配，不是 M2PK 容器")
	ErrBadVersion   = errors.New("m2pk: 容器版本不支持")
	ErrBadKind      = errors.New("m2pk: 容器类型不支持")
	ErrBadCodec     = errors.New("m2pk: 压缩算法不支持")
	ErrCorrupt      = errors.New("m2pk: 容器结构损坏")
	ErrNotSorted    = errors.New("m2pk: 条目未按名字升序排列")
	ErrNameTaken    = errors.New("m2pk: 规范化名字冲突")
	ErrBadName      = errors.New("m2pk: 名字含非法字符（要求 ASCII 小写，仅 [a-z0-9_~-]）")
	ErrSizeMismatch = errors.New("m2pk: 解压后长度与 rawSize 不符")
)

// Entry 是容器内一块的元信息。
type Entry struct {
	Name     string // 规范化名字（小写、无扩展名）
	Offset   uint64 // 块偏移（相对文件头）
	RawSize  uint32 // 解压后字节数
	CompSize uint32 // 压缩后字节数
}

// Source 是一个待打包的块。
//
// `Data` 非空则**直接用内存里的字节**（不复读磁盘）—— 音频转换器就是这么用的：
// 它一边转换一边攒字节，最后一把打包，中间不落临时文件。
type Source struct {
	Name string // 规范化名字
	Path string // 源文件路径（`Data` 为空时用它）
	Data []byte // 内存数据（优先）
}

// Options 控制打包行为。
//
// 零值是"体积优先"（brotli q11）：打包是离线一次性动作，CPU 不敏感，
// 而体积直接决定安装包大小。运行期的解码开销才是需要顾及的，brotli 解码并不慢。
type Options struct {
	// Kind 是容器类型；0 = KindMap。
	Kind int
	// Codec 是块编码；0 = CodecBrotli（地图），CodecStore = 原样存储（音频）。
	Codec int
	// Quality 是 brotli 质量档（0–11）；0 = DefaultQuality(11)。CodecStore 时忽略。
	Quality int
	// Workers 是并行压缩的 goroutine 数；0 = GOMAXPROCS。
	Workers int
	// Progress 在每完成一块后回调（done 从 1 开始）。可为 nil。
	Progress func(done, total int, name string, rawSize, compSize int)
}

// Stats 是容器或打包结果的统计。
type Stats struct {
	Count     int   // 块数
	RawTotal  int64 // 原始字节合计
	CompTotal int64 // 压缩后字节合计（不含头/索引）
	FileSize  int64 // 容器文件大小
}

// Ratio 返回压缩比（压缩后 / 原始）。
func (s Stats) Ratio() float64 {
	if s.RawTotal == 0 {
		return 0
	}
	return float64(s.CompTotal) / float64(s.RawTotal)
}

// CanonicalName 把源文件名规范化为容器内的键：去掉最后一个扩展名并转小写。
//
// 遵循 D-03（全小写、无中文）与 D-20（大小写敏感门禁）：规范化后必须是
// **ASCII 小写**，只允许 `[a-z0-9_~-]`，不含路径分隔符。
//
// 规则刻意不写成"必须是标识符"：实测真实地图名里出现过 `~`
// （`T3063~01.map`），而真正要防的是大小写歧义、Unicode 与路径穿越，
// 不是字符审美——为了好看而拒绝真实数据，是自伤。
func CanonicalName(fileName string) (string, error) {
	base := filepath.Base(fileName)
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	name := strings.ToLower(base)
	if name == "" {
		return "", fmt.Errorf("%w: %q", ErrBadName, fileName)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '~'
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrBadName, fileName)
		}
	}
	return name, nil
}

// ScanDir 扫描目录，返回按规范化名字升序排列的 Source 列表。
//
// exts 是允许的扩展名（小写、不含点）；为空则默认 {"map"}。
// 跳过子目录与点文件。
func ScanDir(dir string, exts []string) ([]Source, error) {
	allow := extSet(exts)
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, de := range des {
		if de.IsDir() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		if ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(de.Name()), ".")); !allow[ext] {
			continue
		}
		names = append(names, de.Name())
	}
	return BuildSources(dir, names)
}

// BuildSources 把一组文件名规范化并排序，同时检测规范化后的重名。
//
// 单独抽出来是为了可测：macOS 默认大小写不敏感，无法在真实目录里造出
// "A.map 与 a.map 同时存在"的场景（这正是 D-20 要防的那类隐患）。
func BuildSources(dir string, names []string) ([]Source, error) {
	seen := make(map[string]string, len(names)) // 规范化名 -> 原始文件名
	out := make([]Source, 0, len(names))
	for _, n := range names {
		name, err := CanonicalName(n)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("%w: %q 与 %q 规范化后同名", ErrNameTaken, prev, n)
		}
		seen[name] = n
		out = append(out, Source{Name: name, Path: filepath.Join(dir, n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func extSet(exts []string) map[string]bool {
	if len(exts) == 0 {
		exts = []string{"map"}
	}
	allow := make(map[string]bool, len(exts))
	for _, e := range exts {
		allow[strings.ToLower(strings.TrimPrefix(e, "."))] = true
	}
	return allow
}

func align8(n int) int { return (n + 7) &^ 7 }

// Pack 把 srcs 打包成容器写入 dst，并返回统计。
//
// 先把所有块压缩进内存（合计约为压缩后大小），再顺序写出——无需 Seek，
// 也便于将来改成"先算后写"的增量流程。输出对相同输入是确定性的。
func Pack(dst string, srcs []Source, opt Options) (Stats, error) {
	var st Stats
	if len(srcs) == 0 {
		return st, fmt.Errorf("m2pk: 没有可打包的文件")
	}

	// 名字必须唯一且升序（读侧依赖升序做二分）。
	for i, s := range srcs {
		if i > 0 && srcs[i-1].Name == s.Name {
			return st, fmt.Errorf("%w: %q", ErrNameTaken, s.Name)
		}
	}

	kind := opt.Kind
	if kind == 0 {
		kind = KindMap
	}
	codec := opt.Codec
	if codec != CodecStore {
		codec = CodecBrotli
	}
	quality := opt.Quality
	if quality <= 0 {
		quality = DefaultQuality
	}
	if quality > 11 {
		quality = 11
	}
	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(srcs) {
		workers = len(srcs)
	}

	type block struct {
		name     string
		rawSize  uint32
		compSize uint32
		comp     []byte
	}
	blocks := make([]block, len(srcs))

	// 每块独立编码（各自一个 brotli 流），因此并行度不影响输出字节（确定性）。
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var done int
	jobs := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				s := srcs[idx]
				mu.Lock()
				failed := firstErr != nil
				mu.Unlock()
				if failed {
					continue
				}
				raw := s.Data
				var err error
				if raw == nil {
					raw, err = os.ReadFile(s.Path)
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				if len(raw) > int(^uint32(0)) {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("m2pk: %s 过大（%d 字节）", s.Path, len(raw))
					}
					mu.Unlock()
					continue
				}
				var comp []byte
				if codec == CodecStore {
					comp = raw // 不压缩：音频压不动（见 KindAudio 的说明）
				} else {
					var buf bytes.Buffer
					buf.Grow(len(raw)/16 + 64)
					bw := brotli.NewWriterOptions(&buf, brotli.WriterOptions{
						Quality: quality,
						LGWin:   DefaultLGWin,
					})
					if _, err := bw.Write(raw); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("m2pk: 压缩 %s 失败: %w", s.Path, err)
						}
						mu.Unlock()
						continue
					}
					if err := bw.Close(); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("m2pk: 压缩 %s 失败: %w", s.Path, err)
						}
						mu.Unlock()
						continue
					}
					comp = buf.Bytes()
				}
				blocks[idx] = block{
					name:     s.Name,
					rawSize:  uint32(len(raw)),
					compSize: uint32(len(comp)),
					comp:     comp,
				}
				mu.Lock()
				done++
				if opt.Progress != nil {
					opt.Progress(done, len(srcs), s.Name, len(raw), len(comp))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range srcs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return st, firstErr
	}

	// 布局计算
	count := len(blocks)
	namesOff := HeaderSize + EntrySize*count
	namesLen := 0
	for _, b := range blocks {
		namesLen += len(b.name)
	}
	dataOff := align8(namesOff + namesLen)

	out := make([]byte, dataOff)
	copy(out[0:], Magic)
	binary.LittleEndian.PutUint16(out[4:], Version)
	out[6] = byte(kind)
	out[7] = byte(codec)
	binary.LittleEndian.PutUint32(out[8:], uint32(count))
	binary.LittleEndian.PutUint32(out[12:], uint32(namesOff))
	binary.LittleEndian.PutUint32(out[16:], uint32(namesLen))
	binary.LittleEndian.PutUint32(out[20:], uint32(dataOff))

	nameAt := namesOff
	off := dataOff
	for i, b := range blocks {
		e := out[HeaderSize+EntrySize*i:]
		binary.LittleEndian.PutUint32(e[0:], uint32(nameAt-namesOff))
		e[4] = byte(len(b.name))
		binary.LittleEndian.PutUint64(e[8:], uint64(off))
		binary.LittleEndian.PutUint32(e[16:], b.rawSize)
		binary.LittleEndian.PutUint32(e[20:], b.compSize)
		copy(out[nameAt:], b.name)
		nameAt += len(b.name)

		out = append(out, b.comp...)
		st.RawTotal += int64(b.rawSize)
		st.CompTotal += int64(b.compSize)
		off += len(b.comp)
		// 块间 8 字节对齐
		for off%8 != 0 {
			out = append(out, 0)
			off++
		}
	}

	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return st, err
	}
	st.Count = count
	st.FileSize = int64(len(out))
	return st, nil
}

// GenItem 是一块的内容生成器 —— **写到它的时候才生成**。
//
// 与 [`Source`] 的区别就是内存：`Pack` 要求所有块同时在内存里（地图 250 MB、音频 209 MB
// 都还好），而美术载荷全量 1.4 GB，必须边生成边写、写完即丢（见 `image.go`）。
type GenItem struct {
	Name string
	// Gen 返回**已编码**的块字节（怎么编码由调用方定；本函数只做 CodecStore 的搬运）。
	Gen func() ([]byte, error)
}

// PackGen 流式打包：索引区先占位，块逐个生成、写盘、即丢，最后回填索引偏移。
//
// 输出与 `Pack` **同一格式**（读侧无感），且对相同输入同样确定性。
// 只支持 `CodecStore`（内容由 `Gen` 负责编码，再套一层压缩纯属浪费）。
func PackGen(dst string, items []GenItem, opt Options) (Stats, error) {
	var st Stats
	if len(items) == 0 {
		return st, fmt.Errorf("m2pk: 没有可打包的条目")
	}
	for i, it := range items {
		if it.Name == "" || it.Gen == nil {
			return st, fmt.Errorf("m2pk: 第 %d 项缺名字或生成器", i)
		}
		if i > 0 && items[i-1].Name >= it.Name {
			return st, fmt.Errorf("%w: %q 之后是 %q", ErrNotSorted, items[i-1].Name, it.Name)
		}
	}
	kind := opt.Kind
	if kind == 0 {
		kind = KindMap
	}
	if opt.Codec != CodecStore {
		return st, fmt.Errorf("m2pk: PackGen 只支持 CodecStore（块内容由 Gen 自行编码）")
	}

	count := len(items)
	namesOff := HeaderSize + EntrySize*count
	namesLen := 0
	for _, it := range items {
		namesLen += len(it.Name)
	}
	dataOff := align8(namesOff + namesLen)

	// 索引区一次算好（名字与偏移在同一块里），先写出去占位，最后 Seek 回来补偏移。
	idx := make([]byte, dataOff)
	nameAt := namesOff
	for i, it := range items {
		e := idx[HeaderSize+EntrySize*i:]
		binary.LittleEndian.PutUint32(e[0:], uint32(nameAt-namesOff))
		e[4] = byte(len(it.Name))
		copy(idx[nameAt:], it.Name)
		nameAt += len(it.Name)
	}
	copy(idx[0:], Magic)
	binary.LittleEndian.PutUint16(idx[4:], Version)
	idx[6] = byte(kind)
	idx[7] = byte(CodecStore)
	binary.LittleEndian.PutUint32(idx[8:], uint32(count))
	binary.LittleEndian.PutUint32(idx[12:], uint32(namesOff))
	binary.LittleEndian.PutUint32(idx[16:], uint32(namesLen))
	binary.LittleEndian.PutUint32(idx[20:], uint32(dataOff))

	f, err := os.Create(dst)
	if err != nil {
		return st, err
	}
	defer f.Close()
	if _, err := f.Write(idx); err != nil {
		return st, err
	}

	off := int64(dataOff)
	for i, it := range items {
		blk, err := it.Gen()
		if err != nil {
			return st, fmt.Errorf("m2pk: 生成 %q 失败: %w", it.Name, err)
		}
		if uint64(len(blk)) > uint64(^uint32(0)) {
			return st, fmt.Errorf("m2pk: %q 过大（%d 字节）", it.Name, len(blk))
		}
		if _, err := f.Write(blk); err != nil {
			return st, err
		}
		e := idx[HeaderSize+EntrySize*i:]
		binary.LittleEndian.PutUint64(e[8:], uint64(off))
		binary.LittleEndian.PutUint32(e[16:], uint32(len(blk)))
		binary.LittleEndian.PutUint32(e[20:], uint32(len(blk)))
		st.RawTotal += int64(len(blk))
		st.CompTotal += int64(len(blk))
		off += int64(len(blk))
		if pad := (8 - off%8) % 8; pad > 0 {
			if _, err := f.Write(make([]byte, pad)); err != nil {
				return st, err
			}
			off += pad
		}
		if opt.Progress != nil {
			opt.Progress(i+1, count, it.Name, len(blk), len(blk))
		}
	}
	if _, err := f.Seek(int64(HeaderSize), io.SeekStart); err != nil {
		return st, err
	}
	if _, err := f.Write(idx[HeaderSize:]); err != nil {
		return st, err
	}
	st.Count = count
	st.FileSize = off
	return st, nil
}

// Reader 是容器的只读句柄。
type Reader struct {
	f        *os.File
	size     int64
	entries  []Entry
	names    []byte
	namesLen int
	kind     uint8
	codec    uint8
}

// Kind 返回容器类型（`KindMap` / `KindAudio`）。
func (r *Reader) Kind() uint8 { return r.kind }

// Codec 返回块编码（`CodecBrotli` / `CodecStore`）。
func (r *Reader) Codec() uint8 { return r.codec }

// Open 打开容器并校验头部与索引结构。
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	r := &Reader{f: f, size: fi.Size()}

	head := make([]byte, HeaderSize)
	if _, err := f.ReadAt(head, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: 读取头部失败: %v", ErrCorrupt, err)
	}
	if string(head[0:4]) != Magic {
		f.Close()
		return nil, ErrBadMagic
	}
	if v := binary.LittleEndian.Uint16(head[4:]); v != Version {
		f.Close()
		return nil, fmt.Errorf("%w: 版本 %d", ErrBadVersion, v)
	}
	if head[6] != KindMap && head[6] != KindAudio && head[6] != KindImage {
		f.Close()
		return nil, fmt.Errorf("%w: kind=%d", ErrBadKind, head[6])
	}
	if head[7] != CodecBrotli && head[7] != CodecStore {
		f.Close()
		return nil, fmt.Errorf("%w: codec=%d", ErrBadCodec, head[7])
	}
	r.kind = head[6]
	r.codec = head[7]
	count := int(binary.LittleEndian.Uint32(head[8:]))
	namesOff := int(binary.LittleEndian.Uint32(head[12:]))
	namesLen := int(binary.LittleEndian.Uint32(head[16:]))
	dataOff := int64(binary.LittleEndian.Uint32(head[20:]))

	if count <= 0 || namesOff != HeaderSize+EntrySize*count || namesLen < 0 {
		f.Close()
		return nil, fmt.Errorf("%w: count=%d namesOff=%d", ErrCorrupt, count, namesOff)
	}
	if int64(namesOff+namesLen) > r.size || dataOff < int64(namesOff+namesLen) || dataOff > r.size {
		f.Close()
		return nil, fmt.Errorf("%w: 索引区越界", ErrCorrupt)
	}

	idx := make([]byte, EntrySize*count)
	if _, err := f.ReadAt(idx, int64(HeaderSize)); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: 读取索引失败: %v", ErrCorrupt, err)
	}
	r.names = make([]byte, namesLen)
	if _, err := f.ReadAt(r.names, int64(namesOff)); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: 读取名字池失败: %v", ErrCorrupt, err)
	}
	r.namesLen = namesLen

	r.entries = make([]Entry, count)
	for i := 0; i < count; i++ {
		e := idx[EntrySize*i:]
		nameOff := int(binary.LittleEndian.Uint32(e[0:]))
		nameLen := int(e[4])
		if nameOff < 0 || nameLen <= 0 || nameOff+nameLen > namesLen {
			f.Close()
			return nil, fmt.Errorf("%w: 第 %d 项名字越界", ErrCorrupt, i)
		}
		off := binary.LittleEndian.Uint64(e[8:])
		compSize := binary.LittleEndian.Uint32(e[20:])
		if int64(off)+int64(compSize) > r.size {
			f.Close()
			return nil, fmt.Errorf("%w: 第 %d 项数据越界", ErrCorrupt, i)
		}
		r.entries[i] = Entry{
			Name:     string(r.names[nameOff : nameOff+nameLen]),
			Offset:   off,
			RawSize:  binary.LittleEndian.Uint32(e[16:]),
			CompSize: compSize,
		}
		if i > 0 && r.entries[i-1].Name >= r.entries[i].Name {
			f.Close()
			return nil, fmt.Errorf("%w: %q 之后是 %q", ErrNotSorted, r.entries[i-1].Name, r.entries[i].Name)
		}
	}
	return r, nil
}

// Close 关闭容器。
func (r *Reader) Close() error { return r.f.Close() }

// Count 返回块数。
func (r *Reader) Count() int { return len(r.entries) }

// Entries 返回全部条目（按名字升序）。
func (r *Reader) Entries() []Entry { return r.entries }

// Names 返回全部名字（按名字升序）。
func (r *Reader) Names() []string {
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Name
	}
	return out
}

// Stats 汇总容器统计。
func (r *Reader) Stats() Stats {
	s := Stats{Count: len(r.entries), FileSize: r.size}
	for _, e := range r.entries {
		s.RawTotal += int64(e.RawSize)
		s.CompTotal += int64(e.CompSize)
	}
	return s
}

// Lookup 按名字查找条目；名字会被规范化为小写。
func (r *Reader) Lookup(name string) (Entry, bool) {
	key := strings.ToLower(name)
	i := sort.Search(len(r.entries), func(i int) bool { return r.entries[i].Name >= key })
	if i < len(r.entries) && r.entries[i].Name == key {
		return r.entries[i], true
	}
	return Entry{}, false
}

// Read 取出指定条目并校验长度。
//
// 块编码由**容器头**决定：`CodecStore` 直接返回原字节，`CodecBrotli` 才解压。
func (r *Reader) Read(e Entry) ([]byte, error) {
	comp := make([]byte, e.CompSize)
	if _, err := r.f.ReadAt(comp, int64(e.Offset)); err != nil {
		return nil, fmt.Errorf("m2pk: 读取 %q 失败: %w", e.Name, err)
	}
	raw := comp
	if r.codec == CodecBrotli {
		var err error
		raw, err = io.ReadAll(brotli.NewReader(bytes.NewReader(comp)))
		if err != nil {
			return nil, fmt.Errorf("m2pk: 解压 %q 失败: %w", e.Name, err)
		}
	}
	if uint32(len(raw)) != e.RawSize {
		return nil, fmt.Errorf("%w: %q 期望 %d 实得 %d", ErrSizeMismatch, e.Name, e.RawSize, len(raw))
	}
	return raw, nil
}

// ReadName 按名字解压；第二个返回值表示是否存在。
func (r *Reader) ReadName(name string) ([]byte, bool, error) {
	e, ok := r.Lookup(name)
	if !ok {
		return nil, false, nil
	}
	b, err := r.Read(e)
	return b, true, err
}

// ReadRange 取出某块**内部**的一段字节（`off` 相对块起点）。
//
// 只有 `CodecStore` 支持：`CodecBrotli` 的块是一整条流，切一段出来没有意义。
// 用途是美术容器 —— 图库载荷几十 MB，逐图取像素不该把整块读进内存
// （`kind=3` 恒为 store，见 KindImage 的说明）。
func (r *Reader) ReadRange(e Entry, off, length uint32) ([]byte, error) {
	if r.codec != CodecStore {
		return nil, fmt.Errorf("m2pk: %w: 块 %q 是压缩块，不能按范围读", ErrBadCodec, e.Name)
	}
	if uint64(off)+uint64(length) > uint64(e.CompSize) {
		return nil, fmt.Errorf("%w: %q 范围 [%d,%d) 超出块长 %d", ErrCorrupt, e.Name, off, off+length, e.CompSize)
	}
	buf := make([]byte, length)
	if _, err := r.f.ReadAt(buf, int64(e.Offset)+int64(off)); err != nil {
		return nil, fmt.Errorf("m2pk: 读取 %q 的 [%d,%d) 失败: %w", e.Name, off, off+length, err)
	}
	return buf, nil
}

// Verify 逐字节校验容器与源目录是否一致。
//
// 检查项：
//  1. 名字集合完全一致（数量、名字一一对应）
//  2. 每块解压后与源文件**逐字节**相同
//  3. 每块长度与 rawSize 一致（Read 内建）
//
// 返回 nil 表示完全一致；任何不一致都会累积进错误返回。
func Verify(containerPath, srcDir string, exts []string) error {
	srcs, err := ScanDir(srcDir, exts)
	if err != nil {
		return err
	}
	r, err := Open(containerPath)
	if err != nil {
		return err
	}
	defer r.Close()
	if r.kind == KindImage {
		return fmt.Errorf("m2pk: kind=3（美术）的块不是源文件字节，请用 artpack verify")
	}

	var probs []string
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
			probs = append(probs, fmt.Sprintf("容器缺少源文件: %s", s.Name))
		}
	}
	for _, e := range r.entries {
		if !inSrc[e.Name] {
			probs = append(probs, fmt.Sprintf("容器多出条目: %s", e.Name))
		}
	}

	for _, s := range srcs {
		e, ok := r.Lookup(s.Name)
		if !ok {
			continue
		}
		want, err := os.ReadFile(s.Path)
		if err != nil {
			probs = append(probs, fmt.Sprintf("%s: 读取源文件失败: %v", s.Name, err))
			continue
		}
		got, err := r.Read(e)
		if err != nil {
			probs = append(probs, fmt.Sprintf("%s: 解压失败: %v", s.Name, err))
			continue
		}
		if !bytes.Equal(want, got) {
			probs = append(probs, fmt.Sprintf("%s: 内容不一致（源 %d 字节，容器 %d 字节）", s.Name, len(want), len(got)))
		}
	}

	if len(probs) > 0 {
		n := len(probs)
		if n > 20 {
			probs = probs[:20]
		}
		return fmt.Errorf("m2pk: 校验失败（%d 处问题）:\n  %s", n, strings.Join(probs, "\n  "))
	}
	return nil
}

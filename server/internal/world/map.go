// Package world 是地图与场景。
//
// 设计原则：
//   - **磁盘格式照搬**：要能读原版 .map 文件，字节布局必须一致。
//   - **内存结构现代化**：原版是一维数组 + 每格挂一个 ObjList（TList），
//     视野查询要扫 (2*range+1)^2 格。此处改为二维切片 + 按 chunk 的空间索引，
//     并预计算阻挡位图，避免每次判定都做位运算和解引用。
package world

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// 磁盘格式常量（对照 Crystal Server/MirEnvir/Map.cs:112-152 与
// /data/git/MIR2/GameOfMir/M2Server/Envir.pas:17-34）。
const (
	// HeaderSize 是 .map 文件头长度（字节）。
	HeaderSize = 52
	// CellSize 是每格长度（字节）。
	CellSize = 12
	// DoorBit 是 btDoorIndex 中表示"这是门"的位。
	DoorBit = 0x80
	// BlockBit 是 wBkImg / wFrImg 中表示"不可通行"的位。
	BlockBit = 0x8000
)

var (
	ErrShortMap    = errors.New("world: 地图数据过短")
	ErrBadMapSize  = errors.New("world: 地图尺寸非法")
	ErrUnknownType = errors.New("world: 未知的地图格式")
)

// Header 是 .map 文件头。
type Header struct {
	Width      uint16
	Height     uint16
	Title      string // 定长 16 字节短字符串
	UpdateDate time.Time
	Reserved   [23]byte
}

// Cell 是磁盘上的单格定义（12 字节）。
type Cell struct {
	BkImg      uint16 // 地表层；& BlockBit → 不可通行
	MidImg     uint16 // 中间层
	FrImg      uint16 // 前景层；& BlockBit → 不可通行
	DoorIndex  byte   // & DoorBit → 门，低 7 位是门组号
	DoorOffset byte
	AniFrame   byte
	AniTick    byte
	Area       byte
	Light      byte
}

// CanWalk 报告该格是否可通行。
func (c Cell) CanWalk() bool {
	return c.BkImg&BlockBit == 0 && c.FrImg&BlockBit == 0
}

// IsDoor 报告该格是否是门。
func (c Cell) IsDoor() bool { return c.DoorIndex&DoorBit != 0 }

// DoorGroup 返回门组号（低 7 位）。
func (c Cell) DoorGroup() byte { return c.DoorIndex & 0x7F }

// Map 是加载到内存的地图。
type Map struct {
	Name   string
	Header Header
	// Cells 按行主序存放：Index(x, y) = y*Width + x。
	//
	// 原版文件是列主序（先填满第 1 列），加载时转置为行主序，
	// 这样按 x 连续访问时缓存友好。
	Cells []Cell
	// blocked 是预计算的阻挡位图，与 Cells 一一对应。
	blocked []bool
	// chunkSize 是空间索引的分块边长。
	chunkSize int
}

// Width / Height 返回地图尺寸。
func (m *Map) Width() int  { return int(m.Header.Width) }
func (m *Map) Height() int { return int(m.Header.Height) }

// Index 把 (x, y) 转为 Cells 下标；越界返回 -1。
func (m *Map) Index(x, y int) int {
	if x < 0 || y < 0 || x >= m.Width() || y >= m.Height() {
		return -1
	}
	return y*m.Width() + x
}

// InBounds 报告坐标是否在地图内。
func (m *Map) InBounds(x, y int) bool {
	return x >= 0 && y >= 0 && x < m.Width() && y < m.Height()
}

// CellAt 取指定格的副本；越界返回零值。
func (m *Map) CellAt(x, y int) (Cell, bool) {
	i := m.Index(x, y)
	if i < 0 {
		return Cell{}, false
	}
	return m.Cells[i], true
}

// CanWalk 报告坐标是否可通行；越界或阻挡返回 false。
func (m *Map) CanWalk(x, y int) bool {
	i := m.Index(x, y)
	if i < 0 {
		return false
	}
	return !m.blocked[i]
}

// Parse 解析经典格式（v0）的 .map 文件。
//
// 布局：52 字节头 + Width*Height 个 12 字节格，**列主序**（x 外层）。
func Parse(name string, data []byte) (*Map, error) {
	if len(data) < HeaderSize {
		return nil, ErrShortMap
	}
	h := Header{
		Width:  binary.LittleEndian.Uint16(data[0:]),
		Height: binary.LittleEndian.Uint16(data[2:]),
	}
	// String[16]：1 字节长度 + 内容
	n := int(data[4])
	if n > 16 {
		n = 16
	}
	h.Title = string(data[5 : 5+n])
	if bits := binary.LittleEndian.Uint64(data[21:]); bits != 0 {
		h.UpdateDate = delphiTime(math.Float64frombits(bits))
	}
	copy(h.Reserved[:], data[29:52])

	if h.Width == 0 || h.Height == 0 {
		return nil, ErrBadMapSize
	}
	w, ht := int(h.Width), int(h.Height)
	need := HeaderSize + w*ht*CellSize
	if len(data) < need {
		return nil, fmt.Errorf("%w: 需要 %d 字节，实际 %d", ErrShortMap, need, len(data))
	}

	m := &Map{
		Name:      name,
		Header:    h,
		Cells:     make([]Cell, w*ht),
		blocked:   make([]bool, w*ht),
		chunkSize: 32,
	}
	off := HeaderSize
	for x := 0; x < w; x++ { // 列主序：x 在外层
		for y := 0; y < ht; y++ {
			c := Cell{
				BkImg:      binary.LittleEndian.Uint16(data[off:]),
				MidImg:     binary.LittleEndian.Uint16(data[off+2:]),
				FrImg:      binary.LittleEndian.Uint16(data[off+4:]),
				DoorIndex:  data[off+6],
				DoorOffset: data[off+7],
				AniFrame:   data[off+8],
				AniTick:    data[off+9],
				Area:       data[off+10],
				Light:      data[off+11],
			}
			// 转置为行主序存储
			m.Cells[y*w+x] = c
			m.blocked[y*w+x] = !c.CanWalk()
			off += CellSize
		}
	}
	return m, nil
}

// delphiTime 把 Delphi TDateTime（1899-12-30 起的天数）转为 time.Time。
func delphiTime(d float64) time.Time {
	const (
		unixEpoch = 25569.0 // 1970-01-01 对应的 TDateTime 序列值
		day       = 86400.0
	)
	if math.IsNaN(d) || math.IsInf(d, 0) {
		return time.Time{}
	}
	return time.Unix(int64((d-unixEpoch)*day), 0).UTC()
}

// DetectFormat 判别 .map 变体。
//
// 魔数表来自 Crystal Server/MirEnvir/Map.cs:73-110（FindType）。
// 0 = 经典格式（我们要的），其余是各家改版/加密变体。
func DetectFormat(data []byte) (int, error) {
	if len(data) < 4 {
		return 0, ErrShortMap
	}
	switch {
	case data[2] == 0x43 && data[3] == 0x23: // "C#"
		return 100, nil
	case data[0] == 0:
		return 5, nil
	case len(data) >= 15 && string(data[:15]) == "(C) SNDA, MIR3.":
		return 6, nil
	// 注意长度："Mir2 AntiHack" 是 13 字符（不是 14），写错会导致判别静默失效
	case len(data) >= 13 && string(data[:13]) == "Mir2 AntiHack":
		return 4, nil
	case len(data) >= 15 && string(data[:15]) == "Map 2010 Ver 1.":
		return 1, nil
	}
	// Shanda 旧版/2012 靠文件大小反推：52 + W*H*14
	if len(data) >= HeaderSize {
		w := int(binary.LittleEndian.Uint16(data[0:]))
		h := int(binary.LittleEndian.Uint16(data[2:]))
		if w > 0 && h > 0 {
			if len(data) == HeaderSize+w*h*14 {
				return 2, nil
			}
			if len(data) == HeaderSize+4+w*h*14 {
				return 3, nil
			}
		}
	}
	if len(data) > 4 && (string(data[:4]) == "Myth" || string(data[:6]) == "Lifcos") {
		return 7, nil
	}
	return 0, nil // 兜底：经典格式
}

// Generate 生成一张测试地图。
//
// ⚠️ 这句"仓库中没有任何真实 .map 文件"已过时：`data/map/` 下有 605 张真实地图
// （`0.map`、`0100.map`…），`MapManager` 走的是它们。本函数**现在只给单测用**
// （例如 droppos_test.go 要一张"全图可走"的确定性地形）。
//
// border=true 时四周设为阻挡。
func Generate(name string, w, h int, border bool) *Map {
	m := &Map{
		Name:      name,
		Header:    Header{Width: uint16(w), Height: uint16(h), Title: name},
		Cells:     make([]Cell, w*h),
		blocked:   make([]bool, w*h),
		chunkSize: 32,
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c Cell
			if border && (x == 0 || y == 0 || x == w-1 || y == h-1) {
				c.BkImg = BlockBit // 边界不可通行
			}
			m.Cells[y*w+x] = c
			m.blocked[y*w+x] = !c.CanWalk()
		}
	}
	return m
}

// SetBlock 设置/清除指定格的阻挡（供测试与 GM 命令使用）。
func (m *Map) SetBlock(x, y int, block bool) bool {
	i := m.Index(x, y)
	if i < 0 {
		return false
	}
	m.blocked[i] = block
	if block {
		m.Cells[i].BkImg |= BlockBit
	} else {
		m.Cells[i].BkImg &= ^uint16(BlockBit)
	}
	return true
}

// Serialize 写回经典格式的 .map 字节流（列主序）。
//
// 用于往返测试与将来的地图编辑导出。
func (m *Map) Serialize() []byte {
	w, h := m.Width(), m.Height()
	out := make([]byte, HeaderSize+w*h*CellSize)

	binary.LittleEndian.PutUint16(out[0:], m.Header.Width)
	binary.LittleEndian.PutUint16(out[2:], m.Header.Height)
	t := []byte(m.Header.Title)
	if len(t) > 16 {
		t = t[:16]
	}
	out[4] = byte(len(t))
	copy(out[5:], t)
	binary.LittleEndian.PutUint64(out[21:], math.Float64bits(toDelphiTime(m.Header.UpdateDate)))
	copy(out[29:52], m.Header.Reserved[:])

	off := HeaderSize
	for x := 0; x < w; x++ { // 列主序
		for y := 0; y < h; y++ {
			c := m.Cells[y*w+x]
			binary.LittleEndian.PutUint16(out[off:], c.BkImg)
			binary.LittleEndian.PutUint16(out[off+2:], c.MidImg)
			binary.LittleEndian.PutUint16(out[off+4:], c.FrImg)
			out[off+6] = c.DoorIndex
			out[off+7] = c.DoorOffset
			out[off+8] = c.AniFrame
			out[off+9] = c.AniTick
			out[off+10] = c.Area
			out[off+11] = c.Light
			off += CellSize
		}
	}
	return out
}

// toDelphiTime 把 time.Time 转为 Delphi TDateTime 序列值。
func toDelphiTime(t time.Time) float64 {
	const (
		unixEpoch = 25569.0
		day       = 86400.0
	)
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())/day + unixEpoch
}

// CountBlocked 返回阻挡格数量（测试与统计用）。
func (m *Map) CountBlocked() int {
	n := 0
	for _, b := range m.blocked {
		if b {
			n++
		}
	}
	return n
}

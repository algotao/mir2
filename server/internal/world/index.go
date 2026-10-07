package world

// Positioned 是可被空间索引管理的对象的最小契约。
//
// 用它避免 world 包反向依赖 entity 包。
type Positioned interface {
	Pos() (int, int)
}

// SpatialIndex 是按 chunk 分块的空间索引。
// 原版视野查询要遍历 (2*range+1)^2 格并逐格扫 ObjList
// （range=12 时是 625 格/次/实体）。改为按 32×32 分块后，
// 范围查询只需遍历覆盖到的少数几个 chunk。
//
// ⚠️ 索引**自己记着每个对象登记在哪个块**（`at`），而不是让调用方传旧坐标：
// 早期版本签名是 `Update(o, oldX, oldY)`，而"移动后再取坐标"是**必然踩的坑**
// —— 怪物/宠物的 6 处调用点全都写成了 `Update(m, m.PosX(), m.PosY())`
// （移动之后再取坐标 ⇒ 新旧块恒等 ⇒ 提前返回 ⇒ 怪物跨块后索引永远停在旧块）。
// 现在旧块由索引自己查，调用方只报"它动了"，没有可以传错的坐标。
type SpatialIndex struct {
	size   int
	chunks map[chunkKey][]Positioned
	// at 记录每个对象当前登记在哪个块（对象必须可比较：实际使用者都是指针）。
	at map[Positioned]chunkKey
}

type chunkKey struct{ cx, cy int }

// NewSpatialIndex 创建索引，chunkSize 为分块边长（<=0 时用 32）。
func NewSpatialIndex(chunkSize int) *SpatialIndex {
	if chunkSize <= 0 {
		chunkSize = 32
	}
	return &SpatialIndex{
		size:   chunkSize,
		chunks: make(map[chunkKey][]Positioned),
		at:     make(map[Positioned]chunkKey),
	}
}

func (s *SpatialIndex) keyOf(x, y int) chunkKey {
	return chunkKey{x / s.size, y / s.size}
}

// keyOfPos 返回对象当前坐标所在的块。
func (s *SpatialIndex) keyOfPos(o Positioned) chunkKey {
	x, y := o.Pos()
	return s.keyOf(x, y)
}

// Add 登记对象。重复登记同一对象时先摘掉旧块，避免同一对象残留在两个块里。
func (s *SpatialIndex) Add(o Positioned) {
	if old, ok := s.at[o]; ok {
		s.detach(o, old)
	}
	k := s.keyOfPos(o)
	s.chunks[k] = append(s.chunks[k], o)
	s.at[o] = k
}

// Remove 移除对象。
//
// 按**登记时的块**摘除（而不是按当前坐标）：对象在移动后没来得及 Update 时，
// 按当前坐标算块会找不到旧登记 ⇒ 索引里留垃圾。
func (s *SpatialIndex) Remove(o Positioned) {
	k, ok := s.at[o]
	if !ok {
		return
	}
	s.detach(o, k)
	delete(s.at, o)
}

// Update 在对象移动后更新其所属 chunk。未登记过的对象会被直接登记。
func (s *SpatialIndex) Update(o Positioned) {
	k := s.keyOfPos(o)
	old, ok := s.at[o]
	if ok && old == k {
		return // 同块内移动：登记位置不用动
	}
	if ok {
		s.detach(o, old)
	}
	s.chunks[k] = append(s.chunks[k], o)
	s.at[o] = k
}

// detach 把对象从它登记的那个块里摘掉（不碰 at）。
func (s *SpatialIndex) detach(o Positioned, k chunkKey) {
	list := s.chunks[k]
	for i, v := range list {
		if v == o {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(s.chunks, k)
	} else {
		s.chunks[k] = list
	}
}

// InRange 返回切比雪夫距离 <= r 范围内的对象（可能包含边界外的少量对象，
// 调用方需自行精确过滤）。
func (s *SpatialIndex) InRange(x, y, r int) []Positioned {
	if r < 0 {
		return nil
	}
	c0 := s.keyOf(x-r, y-r)
	c1 := s.keyOf(x+r, y+r)

	var out []Positioned
	for cy := c0.cy; cy <= c1.cy; cy++ {
		for cx := c0.cx; cx <= c1.cx; cx++ {
			out = append(out, s.chunks[chunkKey{cx, cy}]...)
		}
	}
	return out
}

// Len 返回已登记对象数（测试与统计用）。
func (s *SpatialIndex) Len() int {
	return len(s.at)
}

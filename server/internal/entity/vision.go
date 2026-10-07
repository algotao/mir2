package entity

import "sync"

// ViewTracker 跟踪一个对象视野内的其他对象，产出"进入/离开"事件。
//
// 原版用 nVisibleFlag 三态标记（0=已离开、1=仍在、2=新进入），
// 每次全量扫描 (2*range+1)^2 格后统一处理。这里只做**差集计算**——
// 候选集合由调用方（空间索引）提供，本类型不关心如何取候选。
//
// 这样拆分是为了可测试：进出判定是纯集合运算，不需要网络与地图。
//
// ⚠️ **本类型自带锁**：它天生跨 goroutine —— 本人 goroutine 在移动/进图时
// 更新自己的视野集合，而**别人**的 goroutine（广播、怪物 tick、其它玩家移动）
// 会读 `Contains` 决定这一包发不发给他。审计 P1-5 记的正是这一点：
// 此前 35 处 `visible.Contains/Remove` 都跑在各自的锁域之外。
//
// 把锁做进类型内部（而不是要求每个调用点记得持 `Server.mu`）是刻意的：
// 调用点太多、而且大多在"锁外发包"的循环里，靠约定维持不住。
//
// 锁序：`Server.mu → ViewTracker.mu`（本类型的方法**不会**回调 Server，
// 所以它是那个方向的叶子锁；不会出现反向持锁）。
type ViewTracker struct {
	mu   sync.RWMutex
	seen map[uint32]struct{}
}

// NewViewTracker 创建视野跟踪器。
func NewViewTracker() *ViewTracker {
	return &ViewTracker{seen: make(map[uint32]struct{})}
}

// Update 用当前可见集合计算差集，返回新进入与已离开的 ID。
//
// current 会被接管（调用方不应再修改它）。
func (v *ViewTracker) Update(current map[uint32]struct{}) (entered, left []uint32) {
	if current == nil {
		current = make(map[uint32]struct{})
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for id := range current {
		if _, had := v.seen[id]; !had {
			entered = append(entered, id)
		}
	}
	for id := range v.seen {
		if _, still := current[id]; !still {
			left = append(left, id)
		}
	}
	v.seen = current
	return entered, left
}

// Seen 返回当前视野集合（只读；持锁取快照，调用方不要再改它）。
func (v *ViewTracker) Seen() map[uint32]struct{} {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.seen == nil {
		return nil
	}
	out := make(map[uint32]struct{}, len(v.seen))
	for id := range v.seen {
		out[id] = struct{}{}
	}
	return out
}

// Contains 报告 id 是否在当前视野内。
func (v *ViewTracker) Contains(id uint32) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.seen[id]
	return ok
}

// Add 加入视野；已存在返回 false。
//
// 用于反向同步：A 移动导致 B 看见 A 时，只需在 B 的集合里增量添加，
// 不必重建整个集合。
func (v *ViewTracker) Add(id uint32) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = make(map[uint32]struct{})
	}
	if _, had := v.seen[id]; had {
		return false
	}
	v.seen[id] = struct{}{}
	return true
}

// Remove 移出视野；原本不存在返回 false。
func (v *ViewTracker) Remove(id uint32) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, had := v.seen[id]; !had {
		return false
	}
	delete(v.seen, id)
	return true
}

// Clear 清空视野，返回原本可见的全部 ID（下线时使用，便于通知对方）。
func (v *ViewTracker) Clear() []uint32 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.seen) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(v.seen))
	for id := range v.seen {
		out = append(out, id)
	}
	v.seen = make(map[uint32]struct{})
	return out
}

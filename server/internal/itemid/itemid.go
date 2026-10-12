// Package itemid 分配与修复物品**实例号**（`UserItem.MakeIndex`）。
//
// # 为什么单独一个包
//
// 发号器有两个（`gamesvr` 与 `accountsvc` 各一个进程），而"认物只认实例号"是
// 玩法规则的**唯一**身份判据 —— 卖/修/用/穿/拖都按它找东西（见
// `protocol/item.proto` 的 `make_index` 说明，与 `itemByMakeIndex`）。
//
// 号一重复，两处都会取到**前一件**：
//
//   - 服务端 `itemByMakeIndex` 用 `for … range` 找第一个匹配 ⇒ 卖掉的永远是靠前那件；
//   - 客户端每帧按号重绑手上的物品（`Drag::rebind` 用 `position()`）⇒ 点第 N 格，
//     跟在鼠标上的却是第 0 格那件。
//
// 用户 2026-10-12 报的正是这两个表现：「售出物品时，取包裹物品总是取第一格，
// 而无论鼠标选哪一格」。
package itemid

import (
	"sync/atomic"

	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// Seq 进程内的发号器（原版 `g_Config.nItemNumber`）。
//
// ⚠️ 原版把它**持久化在配置里**（`M2Share.pas:3610-3628` 的 `nItemNumber`）。
// 我们原来是纯内存计数器、**每次重启都从 1 重新发** ⇒ 与存档里已有的号撞号
// （上面那条用户反馈的根因）。
//
// 修法是**水位只涨不跌**：每次把角色读进内存，都先用 [`Seq.Observe`] 把水位抬到
// 它身上最大的号之上 ⇒ 之后发出的号必然大于"已经发出去过的"。
type Seq struct {
	n atomic.Int64
}

// Next 取下一个实例号（单调递增，第一个是 1）。
func (s *Seq) Next() int32 {
	return int32(s.n.Add(1))
}

// Water 当前水位（已经发到几号）。
func (s *Seq) Water() int64 {
	return s.n.Load()
}

// Observe 把水位抬到 `v` 以上（**只涨不跌**，并发安全）。
func (s *Seq) Observe(v int64) {
	for {
		cur := s.n.Load()
		if v <= cur {
			return
		}
		if s.n.CompareAndSwap(cur, v) {
			return
		}
	}
}

// Heal 修一个角色身上**为 0 或重复**的实例号；返回修了几件。
//
// 老存档里已经撞号的那几件在这里被换成新号 —— 否则它们会一直"两件东西一个号"，
// 客户端重绑与服务端取物都只能命中靠前那件（用户 2026-10-12 那条）。
//
// ⚠️ **只换号，不动别的字段**：已经唯一的号保持不动（换号等价于把这件东西变成
// "另一件"，是这里唯一能做对的修法 —— 快捷栏/交易栏那些运行时引用本来就按号认物，
// 它们会在重连时重新绑定）。
//
// 覆盖**全部**物品栏（已穿戴 / 背包 / 仓库）：按号找物品的三条路
// （`itemByMakeIndex` / `findBagItemByMakeIndex`）搜的正是这三处。
func Heal(d *pb.CharacterData, s *Seq) int {
	if d == nil {
		return 0
	}
	sets := [][]*pb.UserItem{d.GetHumItems(), d.GetBagItems(), d.GetStorageItems()}
	// ① 先把水位抬到现有最大号之上 ⇒ 新发的号不会和"保留下来的那些"再撞
	for _, items := range sets {
		for _, it := range items {
			if it == nil || it.Index == 0 {
				continue
			}
			if v := int64(it.MakeIndex); v > 0 {
				s.Observe(v)
			}
		}
	}
	// ② 0 与重复 ⇒ 发新号
	fixed := 0
	seen := make(map[int32]struct{}, 64)
	for _, items := range sets {
		for _, it := range items {
			if it == nil || it.Index == 0 {
				continue
			}
			if it.MakeIndex == 0 {
				it.MakeIndex = s.Next()
				fixed++
				seen[it.MakeIndex] = struct{}{}
				continue
			}
			if _, dup := seen[it.MakeIndex]; dup {
				it.MakeIndex = s.Next()
				fixed++
			}
			seen[it.MakeIndex] = struct{}{}
		}
	}
	return fixed
}

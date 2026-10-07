package gamesvr

import (
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 交易复制漏洞的回归测试。
//
// 背景（1.76 的经典复制）：物品一放进交易栏就**离开背包**（见 deal.go 文件头怪癖 1）
// ⇒ 交易栏是它的所有权令牌。若"成交搬物品"的过程中有并发路径把同一批指针
// 再塞回原主背包（对方点取消 / 心跳 dealGuard / 对方掉线 dealCancelA），
// 同一件物品就会同时存在于两个人的背包里。
//
// 原版靠"切换服务器"制造跨进程时间差；我们虽然只有一个游戏服，但
// **每个玩家一个 goroutine、心跳是第三个** —— 窗口一模一样。

// dealPair 造一对"面对面 + 正在交易"的玩家。
//
// 面对面是因为心跳守卫（dealGuard）的判据就是"还盯着对方"：不面对面会被判取消。
// 坐标与朝向照 setSlaveServerWithMaps 里那两张测试图的惯例（dir 0 = 上）。
func dealPair(t *testing.T, s *Server) (*Player, *Player) {
	t.Helper()
	a := dealTestPlayer(1, "甲")
	b := dealTestPlayer(2, "乙")
	a.Obj.SetPlace(a.Obj.MapRef(), 10, 10, 0)
	b.Obj.SetPlace(b.Obj.MapRef(), 10, 9, 4) // b 面朝下 ⇒ 面前是 (10,10)=a
	mp, err := s.world.maps.Get("0")
	if err != nil {
		t.Fatalf("取地图 0 失败: %v", err)
	}
	a.Obj.SetMapRef(mp)
	b.Obj.SetMapRef(mp)
	s.world.players[1], s.world.players[2] = a, b
	a.dealing, b.dealing = true, true
	a.dealPartner, b.dealPartner = 2, 1
	// 冷却是 1 秒（dealOKCooldown）：把"最后一次操作"推到过去才允许成交
	a.dealLastTick, b.dealLastTick = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
	return a, b
}

// holdsPtr 报告背包里是否**恰好**一次拿着这个物品指针（多于一次也算异常）。
func holdsPtr(p *Player, it *pb.UserItem) (count int) {
	for _, slot := range p.Char.Data.BagItems {
		if slot == it {
			count++
		}
	}
	return count
}

// TestDealCancelReturnsDealGold 取消交易必须把金币退回钱包。
//
// ⚠️ 这条以前是**确定性丢钱**：`clientChangeDealGold` 放进交易栏那一刻就把钱从钱包
// 扣走了，而 `getBackDealItems` 里是"先 `p.dealGolds = 0` 再 `Gold += p.dealGolds`"
// ⇒ 退的永远是 0。玩家每次取消都白丢一笔。
func TestDealCancelReturnsDealGold(t *testing.T) {
	s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
	a, _ := dealPair(t, s)
	a.Char.Data.Gold = 1000

	s.clientChangeDealGold(nil, a, 300) // 放进交易栏 300
	if a.Char.Data.Gold != 700 || a.dealGolds != 300 {
		t.Fatalf("放进交易栏后：钱包=%d 交易栏=%d，期望 700/300", a.Char.Data.Gold, a.dealGolds)
	}
	s.dealCancel(a)
	if a.Char.Data.Gold != 1000 {
		t.Errorf("取消交易后钱包 = %d，期望退回到 1000（金币被吞了）", a.Char.Data.Gold)
	}
	if a.dealGolds != 0 {
		t.Errorf("取消后交易栏金币 = %d，期望 0", a.dealGolds)
	}
}

// TestDealCompletionKeepsOwnership 成交后：物品**各归一处**、一件不多一件不少。
func TestDealCompletionKeepsOwnership(t *testing.T) {
	s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
	a, b := dealPair(t, s)

	give := &pb.UserItem{Index: 1, MakeIndex: 101} // a 给 b 的
	back := &pb.UserItem{Index: 1, MakeIndex: 202} // b 给 a 的
	a.dealItems = []*pb.UserItem{give}
	b.dealItems = []*pb.UserItem{back}
	a.Char.Data.Gold = 1000
	a.dealGolds = 300
	a.Char.Data.Gold -= 300 // clientChangeDealGold 已经扣过
	b.dealOK = true         // 对方已按成交

	s.clientDealEnd(nil, a)

	if n := holdsPtr(b, give); n != 1 {
		t.Errorf("a 给的物品在 b 背包里出现 %d 次，期望 1", n)
	}
	if n := holdsPtr(a, give); n != 0 {
		t.Errorf("a 给的物品还留在 a 背包里（%d 次）", n)
	}
	if n := holdsPtr(a, back); n != 1 {
		t.Errorf("b 给的物品在 a 背包里出现 %d 次，期望 1", n)
	}
	if n := holdsPtr(b, back); n != 0 {
		t.Errorf("b 给的物品还留在 b 背包里（%d 次）", n)
	}
	if b.Char.Data.Gold != 300 || a.Char.Data.Gold != 700 {
		t.Errorf("金币结算 = a:%d b:%d，期望 a:700 b:300", a.Char.Data.Gold, b.Char.Data.Gold)
	}
	if a.dealing || b.dealing || len(a.dealItems) != 0 || len(b.dealItems) != 0 {
		t.Error("成交后交易态没清干净")
	}
}

// TestDealConcurrentCancelNoDupe **真并发**地对撞"成交"与"取消"，
// 断言那件物品只落在一边、金币总数守恒。
//
// 这条对应原版那类"交易瞬间切图/换服复制物品"：`dealCancel` 来自另一个 goroutine
// （对方点取消；真实场景里还有每秒的心跳 dealGuard 与对方掉线 dealCancelA）。
// 用 `go test -race` 跑，能同时验证交易态没有数据竞争。
func TestDealConcurrentCancelNoDupe(t *testing.T) {
	const rounds = 200
	for i := 0; i < rounds; i++ {
		s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
		a, b := dealPair(t, s)

		item := &pb.UserItem{Index: 1, MakeIndex: 303}
		a.dealItems = []*pb.UserItem{item}
		a.Char.Data.Gold = 1000
		a.dealGolds = 300
		a.Char.Data.Gold -= 300 // 放进交易栏时就扣了
		b.dealOK = true

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.dealCancel(b) // 对方在成交的同一瞬间点了取消
		}()
		s.clientDealEnd(nil, a)
		wg.Wait()

		inA, inB := holdsPtr(a, item), holdsPtr(b, item)
		if inA > 0 && inB > 0 {
			t.Fatalf("第 %d 轮：同一件物品同时在两人的背包里 —— 复制！", i)
		}
		if inA+inB != 1 {
			t.Fatalf("第 %d 轮：物品下落 = a:%d b:%d，期望恰好一边一件", i, inA, inB)
		}
		if gold := a.Char.Data.Gold + b.Char.Data.Gold; gold != 1000 {
			t.Fatalf("第 %d 轮：金币总数 = %d，期望 1000（丢了或多了）", i, gold)
		}
		if a.dealing || b.dealing || a.dealPartner != 0 || b.dealPartner != 0 {
			t.Fatalf("第 %d 轮：交易态没清干净（a.dealing=%v b.dealing=%v）", i, a.dealing, b.dealing)
		}
	}
}

// TestDealCancelDuringCompletionNoDupe 用注入点把"对方在成交搬物品的那一瞬间点取消"
// **确定性地**复现出来（原版那类"交易瞬间切图/换服复制"的同构场景）。
//
// 机制：物品进交易栏时就离开了背包 ⇒ 交易栏是所有权令牌。
//   - 修复后：成交一开始就把两边的交易栏**整体摘下**并把 `dealing` 置 false
//     ⇒ 注入进来的取消在 `if not dealing then return` 直接返回 ⇒ 物品只归一处；
//   - 若谁把那步"摘下"挪到搬运之后（或不摘）⇒ 取消会把**已经搬给对方**的指针
//     再塞回对方背包 ⇒ 同一件物品出现两次 ⇒ 本用例**确定性失败**。
func TestDealCancelDuringCompletionNoDupe(t *testing.T) {
	s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
	a, b := dealPair(t, s)

	item := &pb.UserItem{Index: 1, MakeIndex: 404} // b 给 a 的，成交时会被搬走
	b.dealItems = []*pb.UserItem{item}
	b.dealOK = true

	// 注入：就在"摘下交易栏之后、搬运之前"，对方的取消挤进来
	dealTestHook = func(_, partner *Player) {
		s.dealCancelInner(partner) // 模拟对方点取消（无锁内核，等价于并发插入）
	}
	defer func() { dealTestHook = nil }()

	s.clientDealEnd(nil, a)

	if n := holdsPtr(a, item); n != 1 {
		t.Errorf("成交后该物品在 a 背包里应恰好 1 次，实际 %d 次", n)
	}
	if n := holdsPtr(b, item); n != 0 {
		t.Errorf("同一件物品同时留在 b 背包里（%d 次）—— 复制", n)
	}
}

package gamesvr

import (
	"sync"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// TestBagConcurrentTradeAndOwnOps 把"交易写**对方**背包"与"对方自己动背包"
// 放在两条 goroutine 里对撞。
//
// 修复前这是**真实 data race**：`clientDealEnd` 在 A 的 goroutine 里往 B 的
// `BagItems` 塞物品，而 B 的 goroutine 可能同时在吃药/捡物/换装 ⇒
// 无同步地写同一个切片字段，后果是物品丢失或背包错乱。
// 用 `go test -race` 跑：去掉 baglock.go 那把锁，本用例会直接报 DATA RACE。
func TestBagConcurrentTradeAndOwnOps(t *testing.T) {
	const rounds = 300
	for i := 0; i < rounds; i++ {
		s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
		a, b := dealPair(t, s)

		// ⚠️ 关键：成交会把 **a 的交易栏**塞进 **b 的背包**（① 号搬运），
		// 所以要让 a 手里有东西，才能撞上"b 自己动 b 的背包"。
		a.dealItems = []*pb.UserItem{{Index: 1, MakeIndex: 909}}
		b.dealOK = true

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.clientDealEnd(nil, a) // 写 b 的背包
		}()
		// b 自己的 goroutine：反复摘/放/读自己的背包（制造与成交写入的窗口重叠）
		for k := 0; k < 50; k++ {
			s.takeBagItem(b, 0)
			s.putBackToBag(b, &pb.UserItem{Index: 1, MakeIndex: int32(910 + k)})
			s.sendBagItems(nil, b)
		}
		wg.Wait()

		// 顺带确认没把东西弄丢：b 的背包里应当有刚放回的那件 + 成交送来的那件
		if n := len(b.bagSnapshot()); n == 0 {
			t.Fatalf("第 %d 轮：b 的背包空了（物品丢失）", i)
		}
	}
}

// TestTakeBagItemLockedWrapper 无锁内核与加锁包装的行为一致（补 stub 用例，
// 保证 takeBagItem 的加锁包装本身也被跑到）。
func TestTakeBagItemLockedWrapper(t *testing.T) {
	p := dealTestPlayer(1, "甲")
	p.Char.Data.BagItems = []*pb.UserItem{
		{Index: 1, MakeIndex: 1}, {Index: 2, MakeIndex: 2}, {Index: 3, MakeIndex: 3},
	}
	s := testSlaveServer()
	s.takeBagItem(p, 0)
	got := p.bagSnapshot()
	if len(got) != 3 || got[0].MakeIndex != 2 || got[1].MakeIndex != 3 {
		t.Fatalf("摘除后该前移且尾补空槽，实际 %d 格首件 %d", len(got), got[0].MakeIndex)
	}
	if got[2] == nil || got[2].Index != 0 {
		t.Error("尾格该是空槽（保持无空洞）")
	}
}

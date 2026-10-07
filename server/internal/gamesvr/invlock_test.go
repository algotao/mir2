package gamesvr

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
)

// 钱包（`Char.Data.Gold`）的并发回归测试。
//
// 背景：交易是"两个玩家"的事，成交时会去改**对方**的钱包（`partner.addGold`）；
// 而对方自己的 goroutine 可能同时在买卖/捡钱/交税。两边都不加锁时是
// **读-改-写**被打断 ⇒ 丢更新 —— 少扣的那次就是"买了东西没付钱"，实质等于刷钱。
// 修法与背包一样：统一走 `Player.stateMu`（见 invlock.go）。

// TestSpendGoldIsAtomic 并发扣钱：**成功的次数必须恰好等于钱包里有的份数**。
//
// 不加锁时的典型症状：两个 goroutine 都读到"还剩 1"，于是都判定成功、都写 0
// ⇒ 成功次数 > 实际份数（这就是丢更新/超发的那个方向）。
// 配合 `-race` 跑还能直接暴露对同一字段的并发读写。
func TestSpendGoldIsAtomic(t *testing.T) {
	const (
		rounds  = 100
		workers = 8
		tries   = 10
		wallet  = 40
	)
	for r := 0; r < rounds; r++ {
		p := newTestPlayer(1, "甲", 1)
		p.Char.Data.Gold = wallet

		var ok int64
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < tries; k++ {
					if p.spendGold(1) {
						atomic.AddInt64(&ok, 1)
					}
				}
			}()
		}
		wg.Wait()

		if ok != wallet {
			t.Fatalf("第 %d 轮：成功扣款 %d 次，钱包里只有 %d 份 —— 超发/丢更新", r, ok, wallet)
		}
		if got := p.gold(); got != 0 {
			t.Fatalf("第 %d 轮：扣完余额 = %d，期望 0", r, got)
		}
	}
}

// TestDealGoldConcurrentWithOwnPickup 成交给**对方**入账，与对方自己捡钱并发。
//
// 断言的是**守恒**：一笔都不能丢（成交那 500 与他自己捡的 200 都要在）。
func TestDealGoldConcurrentWithOwnPickup(t *testing.T) {
	const (
		rounds = 200
		picks  = 20
	)
	for r := 0; r < rounds; r++ {
		s, _ := butchTestServer(t, wuItem(1, "测试剑", 5, data.MinMax{}))
		a, b := dealPair(t, s)

		a.Char.Data.Gold = 1000
		b.Char.Data.Gold = 0
		// a 把 500 放进交易栏（那一步已经把钱从钱包扣走了）
		a.Char.Data.Gold -= 500
		a.dealItems = nil
		a.dealGolds = 500
		b.dealOK = true

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.clientDealEnd(nil, a) // 成交：给 b 入账 500
		}()
		// b 自己在同一时刻捡钱（每次 +10）
		for k := 0; k < picks; k++ {
			b.addGold(10)
		}
		wg.Wait()

		want := int64(500 + picks*10)
		if got := b.gold(); got != want {
			t.Fatalf("第 %d 轮：b 的金币 = %d，期望 %d（成交 +500 与自己捡的 +%d）—— 有更新被覆盖",
				r, got, want, picks*10)
		}
		if got := a.gold(); got != 500 {
			t.Fatalf("第 %d 轮：a 的金币 = %d，期望 500（1000 - 放进交易栏的 500）", r, got)
		}
	}
}

// TestGoldAccessorsBasics 三个原语的基本语义（含饱和与"不够就不动"）。
func TestGoldAccessorsBasics(t *testing.T) {
	p := newTestPlayer(1, "甲", 1)
	p.Char.Data.Gold = 100

	if got := p.addGold(50); got != 150 || p.gold() != 150 {
		t.Fatalf("addGold 后 = %d/%d，期望 150", got, p.gold())
	}
	if got := p.setGold(7); got != 7 {
		t.Fatalf("setGold 返回 %d，期望 7", got)
	}
	if p.spendGold(8) {
		t.Error("余额 7 时扣 8 该失败")
	}
	if p.gold() != 7 {
		t.Errorf("扣款失败时余额不该动，实际 %d", p.gold())
	}
	if !p.spendGold(7) || p.gold() != 0 {
		t.Errorf("扣 7 该成功且余额归零，实际 %d", p.gold())
	}
	// 负数扣款不合法（当 0 处理），越界的 set 夹到 0
	if !p.spendGold(0) || !p.spendGold(-5) {
		t.Error("非正的扣款该直接算成功")
	}
	if got := p.setGold(-1); got != 0 {
		t.Errorf("setGold(-1) = %d，期望 0", got)
	}
	// 溢出饱和：加到 MaxInt64 就不再涨
	p.setGold(1 << 62)
	if got := p.addGold(1 << 62); got != 1<<63-1 {
		t.Errorf("溢出该饱和到 MaxInt64，实际 %d", got)
	}
}

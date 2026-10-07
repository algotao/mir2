package gamesvr

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
)

// 血量的并发回归测试（见 statelock.go / entity.Monster 的字段注释）。
//
// 病：**挨打是别人算的** —— 攻击者在自己的 goroutine 上扣受害者的血，
// 而受害者自己的 goroutine（吃药/放技能）和 ticker（恢复/挂毒/火墙）也在动它
// ⇒ `HP -= dmg` 这种读-改-写会互相覆盖（"打了两下只掉一下"）。
//
// ⚠️ 断言要落在**最终血量**上：丢更新时"每次报告扣了多少"仍然是对的，
// 只有最终值会偏高 —— 只加"扣了多少"是抓不到的。

// TestPlayerDamageIsAtomic 多人同时打同一个玩家。
func TestPlayerDamageIsAtomic(t *testing.T) {
	const (
		rounds   = 100
		workers  = 4
		hitsEach = 25
		perHit   = 10
		initial  = 1 << 16
	)
	for r := 0; r < rounds; r++ {
		p := newTestPlayer(1, "挨打测试", entity.JobWarr)
		p.Char.Data.Abil.MaxHp = initial * 2
		p.setHP(initial)

		var reported int64
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < hitsEach; k++ {
					_, actual := p.hurt(perHit)
					atomic.AddInt64(&reported, int64(actual))
				}
			}()
		}
		wg.Wait()

		want := int64(workers * hitsEach * perHit)
		if reported != want {
			t.Fatalf("第 %d 轮：报告扣掉 %d，期望 %d", r, reported, want)
		}
		if got := int64(p.hp()); got != int64(initial)-want {
			t.Fatalf("第 %d 轮：最终血量 %d，期望 %d —— 有伤害被覆盖（丢更新）",
				r, got, int64(initial)-want)
		}
	}
}

// TestMonsterDamageIsAtomic 多人同时打同一只怪（怪物没有自己的 goroutine，
// 打它的可能是任意玩家/宠物 + ticker 的毒/火墙）。
func TestMonsterDamageIsAtomic(t *testing.T) {
	const (
		rounds   = 100
		workers  = 4
		hitsEach = 25
		perHit   = 10
		initial  = 1 << 16
	)
	for r := 0; r < rounds; r++ {
		m := newTestMonster(1, "打怪测试", initial)
		m.MaxHP = initial * 2

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < hitsEach; k++ {
					m.Hurt(perHit)
				}
			}()
		}
		wg.Wait()

		want := uint32(initial - workers*hitsEach*perHit)
		if got := m.HPValue(); got != want {
			t.Fatalf("第 %d 轮：怪的血量 %d，期望 %d —— 有伤害被覆盖（丢更新）", r, got, want)
		}
	}
}

// TestHurtReturnsActual 扣血助手的基本语义（不足时只扣剩下的、死了再打不生效）。
func TestHurtReturnsActual(t *testing.T) {
	p := newTestPlayer(1, "语义", entity.JobWarr)
	p.Char.Data.Abil.MaxHp = 100
	p.setHP(30)

	hp, actual := p.hurt(50)
	if hp != 0 || actual != 30 {
		t.Fatalf("血只剩 30 时挨 50：hp=%d actual=%d，期望 0/30", hp, actual)
	}
	hp, actual = p.hurt(50)
	if hp != 0 || actual != 0 {
		t.Fatalf("已经 0 血再挨打不该再扣：hp=%d actual=%d", hp, actual)
	}
	// 加血夹到上限
	if got := p.addHP(500); got != 100 {
		t.Fatalf("加血该夹到上限 100，实际 %d", got)
	}
	// 蓝：够就扣、不够分文不动
	p.Char.Data.Abil.MaxMp = 50
	p.addMP(50)
	if !p.spendMP(20) || p.mp() != 30 {
		t.Fatalf("蓝够时该扣成功：mp=%d", p.mp())
	}
	if p.spendMP(100) || p.mp() != 30 {
		t.Fatalf("蓝不够时不该动：mp=%d", p.mp())
	}
}

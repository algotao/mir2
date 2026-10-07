package entity

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/world"
)

// TestObjectStateIsRaceFree 守住审计 P1-5 的收口：`Object` 的可变态
// （坐标/朝向/地图/外观/状态位/中毒/石化/血条/城堡仇恨窗口）全部收在**自带的锁**
// 后面，多 goroutine 交叉读写不再有数据竞争。
//
// ⚠️ 判定力来自 `-race`：不带 `-race` 时它只验证"功能没崩"。
// 六条 goroutine 分别扮演真实角色：本人移动、换装/上状态、别人的视野/目标判定、
// 麻痹戒指打人、施毒者、以及 ticker（中毒结算 + 血条/仇恨窗口到期）。
func TestObjectStateIsRaceFree(t *testing.T) {
	mp := world.Generate("锁域图", 64, 64, false)
	o := NewObject(1, "靶子", mp, 10, 10, DirDown, 0x1234)

	var wg sync.WaitGroup
	work := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				fn()
			}
		}()
	}

	// ① 本人 goroutine：走位
	work(func() { o.MoveTo(uint8(rand.IntN(8))) })
	// ② 本人 goroutine：转向 + 换装/状态位变化
	work(func() {
		o.Turn(uint8(rand.IntN(8)))
		o.SetFacing(uint8(rand.IntN(8)))
		o.SetStatusBits(int32(time.Now().UnixNano()))
		o.SetFeatureBits(0x2A)
		o.SetPosX(10)
		o.SetPosY(11)
		o.SetMapRef(mp)
	})
	// ③ 别人的 goroutine：视野差集 / 目标判定要的各种读
	work(func() {
		o.Place()
		o.Pos()
		o.PosX()
		o.PosY()
		o.Facing()
		o.MapRef()
		o.Appearance()
		o.StatusBits()
		o.FeatureBits()
		o.Distance(10, 10)
		o.CanMove(DirDown)
		o.SetPlace(mp, 10, 10, DirDown)
		o.SetPos(mp, 10, 10)
	})
	// ④ 打人的 goroutine：麻痹戒指 ⇒ 石化
	work(func() {
		o.Stone(time.Millisecond)
		o.Stoned(time.Now())
		o.StoneUntil()
		o.ClearStone()
		o.SetStoneUntil(time.Time{})
	})
	// ⑤ 施毒者的 goroutine：施加中毒
	work(func() { o.ApplyPoison(PoisonDecHealth, 1, 3, 7, time.Now()) })
	// ⑥ ticker：中毒结算 + 血条/仇恨窗口到期
	work(func() {
		o.PoisonSnapshot()
		o.PoisonActive(PoisonDecHealth, time.Now())
		o.PoisonUntil(PoisonDecHealth)
		o.PoisonPoint()
		o.PoisonTickAt()
		o.PoisonSrcID()
		o.AnyPoisonActive(time.Now())
		o.PoisonMask(time.Now())
		o.WithPoison(func(ps *PoisonState) { ps.TickAt = time.Now() })
		o.ClearPoison(PoisonDamageArmor)
		o.SetShowHPUntil(time.Now().Add(time.Second))
		o.ShowHPUntil()
		o.SetCastleAggroUntil(time.Now().Add(time.Second))
		o.CastleAggroUntil()
	})
	wg.Wait()
}

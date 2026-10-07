package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// TestMapDecHPIncHP 地图级扣血/加血（`DECHP(点/时间秒)` / `INCHP(点/时间秒)`，
// 原版 `TPlayObject.Run`，ObjBase.pas:6819-6843）。
//
// ⚠️ 三处原版怪癖一起钉住：
//  1. **加血用的也是 `nDECHPPOINT`**（原版没有单独的"加血点"；我们的 `IncHPPoint`
//     只是照抄解析出来的字段，消费时用不到）；
//  2. 扣到 0 **不是死亡**（原版这里没有 Die 调用）；
//  3. 计时器是**每个对象一份**（`m_dwDecHPTick`），且初值 0 ⇒ 进图第一次 Run
//     **两个都会触发**（`GetTickCount - 0 > 间隔` 恒真）。
func TestMapDecHPIncHP(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	ab := p.Char.Data.Abil
	ab.Hp, ab.MaxHp = 20, 100
	now := time.Now()

	// ① 只标 DECHP(5/1)：第一次立刻扣（计时器初值 0）
	s.data.mapInfoByID["0"] = &data.MapInfo{ID: "0", DecHPSet: true, DecHPPoint: 5, DecHPTime: 1}
	s.tickMapHP(p, now)
	if ab.Hp != 15 {
		t.Fatalf("扣血后 HP = %d，期望 15（20 - DECHP 5）", ab.Hp)
	}
	// 没到 nDECHPTIME 秒 ⇒ 不扣
	s.tickMapHP(p, now.Add(500*time.Millisecond))
	if ab.Hp != 15 {
		t.Errorf("没到 nDECHPTIME 不该扣血，实际 HP = %d", ab.Hp)
	}
	// 到点 ⇒ 再扣 5
	s.tickMapHP(p, now.Add(1500*time.Millisecond))
	if ab.Hp != 10 {
		t.Errorf("到点该再扣 5，实际 HP = %d", ab.Hp)
	}

	// ② INCHP：⚠️ 官方加血的量取的是 **nDECHPPOINT**（不是 nINCHPPOINT）——
	// 一张**只写 INCHP** 的图，nDECHPPOINT 还是 0 ⇒ 这条什么都不加（原版怪癖，照抄）。
	ab.Hp = 20
	p.incHPAt = time.Time{}
	s.data.mapInfoByID["0"] = &data.MapInfo{
		ID: "0", IncHPSet: true, IncHPPoint: 7, IncHPTime: 2,
	}
	s.tickMapHP(p, now.Add(10*time.Second))
	if ab.Hp != 20 {
		t.Errorf("只写 INCHP 的图：nDECHPPOINT=0 ⇒ 加血量也是 0（原版如此），实际 HP = %d", ab.Hp)
	}

	// 同时写了 DECHP(5/1) 的图：加血量才等于 5（而不是 INCHP 自己的 7）
	ab.Hp = 20
	p.incHPAt, p.decHPAt = time.Time{}, time.Time{}
	s.data.mapInfoByID["0"] = &data.MapInfo{
		ID: "0", DecHPSet: true, DecHPPoint: 5, DecHPTime: 1,
		IncHPSet: true, IncHPPoint: 7, IncHPTime: 2,
	}
	s.tickMapHP(p, now.Add(15*time.Second))
	if ab.Hp != 20 {
		t.Errorf("扣 5 加 5 后 HP 该仍是 20，实际 %d（加血若用了 IncHPPoint=7 会变成 22）", ab.Hp)
	}
	// 加血不超上限（这里只开加血、并把 nDECHPPOINT 当作已配好的 5）
	ab.Hp = 98
	p.incHPAt = time.Time{}
	s.data.mapInfoByID["0"] = &data.MapInfo{
		ID: "0", DecHPPoint: 5, IncHPSet: true, IncHPPoint: 5, IncHPTime: 2,
	}
	s.tickMapHP(p, now.Add(25*time.Second))
	if ab.Hp != ab.MaxHp {
		t.Errorf("加血应被钳到上限 %d，实际 %d", ab.MaxHp, ab.Hp)
	}

	// ③ 扣到 0 **不是死亡**（原版这里不判死，玩家卡在 0 血等下一下打击）
	ab.Hp = 3
	p.decHPAt = time.Time{}
	s.data.mapInfoByID["0"] = &data.MapInfo{ID: "0", DecHPSet: true, DecHPPoint: 5, DecHPTime: 1}
	s.tickMapHP(p, now.Add(30*time.Second))
	if ab.Hp != 0 {
		t.Errorf("血不足扣时该见底为 0，实际 %d", ab.Hp)
	}
}

// TestMonsterCrossChunkKeepsIndexInSync 守住 P1-8：怪物跨 32×32 chunk 之后，
// 空间索引里必须跟着搬到新块。
//
// 这个用例是**端到端**的（走 `tickMonsters` 的真实追击路径），因为 bug 就出在
// 调用点上：怪物移动的 6 处代码全都写成了 `Update(m, m.PosX(), m.PosY())`
// —— 移动之后再取坐标，新旧块恒等 ⇒ 索引提前返回 ⇒ 跨块后还挂在旧块，
// `monsterAt`/范围查询按坐标比对就再也找不到它（"怪物隐身"）。
func TestMonsterCrossChunkKeepsIndexInSync(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	// 地图是 50×50（flagTestServer）。⚠️ `testSlaveServer` 给 monsterIdx 的分块边长
	// 是 64，那样 x=30→33 还在同一块里、用例会白过 ⇒ 这里显式换成 32（生产值）。
	s.world.monsterIdx = world.NewSpatialIndex(32)
	// 怪物从 x=30 起步、玩家在 x=40 ⇒ 追击必然跨过 x=32 这条 chunk 边界。
	mon := newTestMonster(21, "跨块怪", 500)
	mon.SetPlace(p.Obj.MapRef(), 30, 10, entity.DirRight)
	mon.ViewRange = 20
	p.Obj.SetPlace(p.Obj.MapRef(), 40, 10, 0)
	p.visible = entity.NewViewTracker()
	s.world.players[p.Obj.ID] = p
	s.cfg.aggro = true // 没有目标就不会追（tickMonsters 的选目标门）

	s.mu.Lock()
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)
	s.mu.Unlock()

	now := time.Now()
	// ⚠️ 停在**刚跨块那一格**（x=32；chunk = x/32 ⇒ 31 属块 0、32 属块 1）。
	// 只在边界上停：如果索引更新滞后一拍（旧实现就是"移动前取坐标"），
	// 登记会停在上一格 x=31 ⇒ 正好落在旧块里，用例才咬得住。
	prevX := 30
	for i := 0; i < 12 && mon.PosX() < 32; i++ {
		prevX = mon.PosX()
		now = now.Add(time.Second)
		s.tickMonsters(now)
	}
	if mon.PosX() < 32 {
		t.Fatalf("怪物没跨过 chunk 边界（x=%d），用例前提不成立", mon.PosX())
	}

	x, y := mon.PosX(), mon.PosY()
	want := world.Positioned(mon)
	found := false
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("怪物移到 (%d,%d) 后，索引在新块查不到它（P1-8 回归）", x, y)
	}
	if old := s.world.monsterIdx.InRange(prevX, y, 0); len(old) != 0 {
		t.Errorf("上一格 x=%d 所在的旧块仍有 %d 个残留登记（P1-8 回归）", prevX, len(old))
	}
	// 索引长度必须还是 1：既不能残留旧登记、也不能重复登记
	if n := s.world.monsterIdx.Len(); n != 1 {
		t.Errorf("索引登记数 = %d，期望 1（残留或重复登记）", n)
	}
	// 走**生产路径**再验一次：抗拒火环/刺杀用的 `targetAtLocked` 就是
	// "按坐标从 monsterIdx 取候选再比对坐标"，索引没跟上时这里恒为 nil。
	s.mu.RLock()
	cand := s.targetAtLocked(mon.MapRef(), x, y, p)
	s.mu.RUnlock()
	if cand == nil || cand.mon != mon {
		t.Errorf("按坐标 (%d,%d) 从索引取不到这只怪（P1-8 回归）", x, y)
	}
}

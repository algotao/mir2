package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// goldTestServer 造一个 20×20 全可走地图 + 一个站在 (10,10) 的玩家。
func goldTestServer() (*Server, *world.Map, *Player) {
	s, m := dropTestServer()
	p := newTestPlayer(1, "捡钱测试", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.visible = entity.NewViewTracker()
	s.world.players[1] = p
	return s, m, p
}

// groundGoldAt 返回该格的金币堆（没有则 nil）。
func groundGoldAt(s *Server, m *world.Map, x, y int) *GroundItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, gi := range s.world.ground {
		if gi.Gold > 0 && gi.Map == m && gi.X == x && gi.Y == y {
			return gi
		}
	}
	return nil
}

// groundGoldTotal 返回地面上金币总额（并堆也算一次）。
func groundGoldTotal(s *Server) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sum int64
	for _, gi := range s.world.ground {
		sum += gi.Gold
	}
	return sum
}

// TestGoldShape 钉住 `GetGoldShape`（M2Share.pas:3568-3575）的五档：
// 112 / ≥30→113 / ≥70→114 / ≥300→115 / ≥1000→116。
func TestGoldShape(t *testing.T) {
	cases := []struct {
		n    int64
		want uint16
	}{{1, 112}, {29, 112}, {30, 113}, {69, 113}, {70, 114}, {299, 114}, {300, 115}, {999, 115}, {1000, 116}, {99999, 116}}
	for _, c := range cases {
		if got := goldShape(c.n); got != c.want {
			t.Errorf("goldShape(%d) = %d，期望 %d", c.n, got, c.want)
		}
	}
}

// TestSpawnGroundGoldMerges 同格金币**并堆**（原版 `AddToMap` 返回已有对象即"并了"），
// 且外观按新总额升档。
func TestSpawnGroundGoldMerges(t *testing.T) {
	s, m, _ := goldTestServer()
	s.spawnGroundGold(m, 3, 3, 20, 7)
	s.spawnGroundGold(m, 3, 3, 20, 7)
	gi := groundGoldAt(s, m, 3, 3)
	if gi == nil {
		t.Fatal("金币堆没落下来")
	}
	if gi.Gold != 40 {
		t.Errorf("并堆后金额 = %d，期望 40", gi.Gold)
	}
	if gi.Looks != goldShape(40) {
		t.Errorf("并堆后外观 = %d，期望 %d（≥30 提档）", gi.Looks, goldShape(40))
	}
	if n := len(s.world.ground); n != 1 {
		t.Errorf("同格应只有一堆，实际 %d 件地面物", n)
	}
	if gi.Owner != 7 {
		t.Errorf("归属 = %d，期望 7", gi.Owner)
	}
}

// TestScatterGoldsPileSize 单堆不超过 `MonOneDropGoldCount`（2000），总量不丢。
func TestScatterGoldsPileSize(t *testing.T) {
	s, m, _ := goldTestServer()
	if got := s.scatterGolds(m, 10, 10, 5000, 1); got != 5000 {
		t.Errorf("撒出总量 = %d，期望 5000", got)
	}
	if got := groundGoldTotal(s); got != 5000 {
		t.Errorf("地面金币 = %d，期望 5000", got)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, gi := range s.world.ground {
		if gi.Gold > monOneDropGoldCount {
			t.Errorf("单堆 %d 超过上限 %d", gi.Gold, monOneDropGoldCount)
		}
	}
}

// TestScatterGoldsPileLimit 堆数上限 17（原版 `if i >= 17 then Break`）：
// 60000 最多撒出 17 × 2000 = 34000，其余留在"怪物身上"。
func TestScatterGoldsPileLimit(t *testing.T) {
	s, m, _ := goldTestServer()
	got := s.scatterGolds(m, 10, 10, 60000, 1)
	if got != monGoldPileLimit*monOneDropGoldCount {
		t.Errorf("撒出 = %d，期望 %d", got, monGoldPileLimit*monOneDropGoldCount)
	}
	if n := len(s.world.ground); n != monGoldPileLimit {
		t.Errorf("堆数 = %d，期望 %d", n, monGoldPileLimit)
	}
}

// TestDropGoldDownRange 落点在**3 格**内（原版 `GetDropPosition(…, 3, …)`）。
func TestDropGoldDownRange(t *testing.T) {
	s, m, _ := goldTestServer()
	if !s.dropGoldDown(m, 10, 10, 50, 0) {
		t.Fatal("dropGoldDown 应该成功")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.world.ground) != 1 {
		t.Fatalf("应有 1 堆金币，实际 %d", len(s.world.ground))
	}
	for _, gi := range s.world.ground {
		if d := abs(gi.X-10) + abs(gi.Y-10); d > dropRangeMonsterDie {
			t.Errorf("落点 (%d,%d) 距离 %d 超过范围 %d", gi.X, gi.Y, d, dropRangeMonsterDie)
		}
	}
}

// TestHandleDropGold 玩家扔金币（原版 `ClientDropGold`，ObjBase.pas:16187-16212）。
func TestHandleDropGold(t *testing.T) {
	s, m, p := goldTestServer()
	p.Char.Data.Gold = 10000

	s.handleDropGold(nil, p, 300)
	if p.Char.Data.Gold != 9700 {
		t.Errorf("扔 300 后金币 = %d，期望 9700", p.Char.Data.Gold)
	}
	if got := groundGoldTotal(s); got != 300 {
		t.Errorf("地面金币 = %d，期望 300", got)
	}

	// 原版 `if nGold >= m_nGold then Exit`：不许把钱扔光
	s.handleDropGold(nil, p, 9700)
	if p.Char.Data.Gold != 9700 {
		t.Errorf("扔光应被拒（金币仍 9700），实际 %d", p.Char.Data.Gold)
	}
	// 多扔也一样
	s.handleDropGold(nil, p, 999999)
	if p.Char.Data.Gold != 9700 {
		t.Errorf("超额扔应被拒，实际 %d", p.Char.Data.Gold)
	}
	// 非正数直接忽略
	s.handleDropGold(nil, p, 0)
	s.handleDropGold(nil, p, -5)
	if got := groundGoldTotal(s); got != 300 {
		t.Errorf("地面金币仍应 300，实际 %d", got)
	}
	_ = m
}

// TestHandleDropGoldNoThrowMap 地图标了 `NOTHROWITEM` ⇒ 拒绝（文案见 String.ini）。
func TestHandleDropGoldNoThrowMap(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0] NOTHROWITEM\n")
	p := newTestPlayer(1, "不能扔", entity.JobWarr)
	p.Obj.SetPlace(m, 5, 5, p.Obj.Facing())
	p.Char.Data.Gold = 5000
	s.world.players[1] = p

	s.handleDropGold(nil, p, 100)
	if p.Char.Data.Gold != 5000 {
		t.Errorf("地图禁扔时金币不该变，实际 %d", p.Char.Data.Gold)
	}
	if got := groundGoldTotal(s); got != 0 {
		t.Errorf("地图禁扔时地面不该有金币，实际 %d", got)
	}
}

// TestCanPickUpGround 掉落归属（原版 `ClientPickUpItem`，ObjBase.pas:1699-1707）：
// 无归属/本人/队友/过了 2 分钟 都能捡；别人的新鲜掉落不能捡。
func TestCanPickUpGround(t *testing.T) {
	s, m, p := goldTestServer()
	other := newTestPlayer(2, "别人", entity.JobWarr)
	other.Obj.SetPlace(m, 11, 10, other.Obj.Facing())
	s.world.players[2] = other

	fresh := &GroundItem{Gold: 100, Map: m, X: 10, Y: 10, Owner: 2, CreatedAt: time.Now()}
	if s.canPickUpGround(p, fresh) {
		t.Error("别人的新鲜掉落不该能捡")
	}
	if !s.canPickUpGround(other, fresh) {
		t.Error("归属者本人应该能捡")
	}
	free := &GroundItem{Gold: 100, Owner: 0, CreatedAt: time.Now()}
	if !s.canPickUpGround(p, free) {
		t.Error("无归属的地面金币谁都能捡")
	}
	self := &GroundItem{Gold: 100, Owner: p.Obj.ID, CreatedAt: time.Now()}
	if !s.canPickUpGround(p, self) {
		t.Error("自己的掉落应该能捡")
	}
	// 过了 `FloorItemCanPickUpTime`（2 分钟）⇒ 归属作废
	old := &GroundItem{Gold: 100, Owner: 2, CreatedAt: time.Now().Add(-floorItemCanPickUpTime - time.Second)}
	if !s.canPickUpGround(p, old) {
		t.Error("超过保留时长后谁都能捡")
	}
	// 队友能捡
	s.social.groups.Create(groupPlayerFor(other), groupPlayerFor(p))
	if !s.canPickUpGround(p, fresh) {
		t.Error("队友应该能捡")
	}
}

// TestPickupGold 拾取金币：钱袋加钱、地面堆消失、广播 SM_ITEMHIDE。
func TestPickupGold(t *testing.T) {
	s, m, p := goldTestServer()
	gi := s.spawnGroundGold(m, 10, 10, 250, 1)
	if gi == nil {
		t.Fatal("金币没落地")
	}
	// 客户端 CM_PICKUP 的 Param/Tag 是自己的坐标（原版按"脚下那格"取物品）
	pkt := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_PICKUP, 0, 10, 10, 0)}
	p.Char.Data.Gold = 100
	s.handlePickup(nil, p, pkt)

	if p.Char.Data.Gold != 350 {
		t.Errorf("捡起后金币 = %d，期望 350", p.Char.Data.Gold)
	}
	if len(s.world.ground) != 0 {
		t.Errorf("金币堆该从地面消失，还剩 %d 件", len(s.world.ground))
	}
}

// TestPickupOthersGoldRejected 别人的归属金币捡不走，且**堆仍在地面**（不能凭空吞掉）。
func TestPickupOthersGoldRejected(t *testing.T) {
	s, m, p := goldTestServer()
	other := newTestPlayer(2, "别人", entity.JobWarr)
	other.Obj.SetPlace(m, 10, 10, other.Obj.Facing())
	s.world.players[2] = other
	s.spawnGroundGold(m, 10, 10, 250, other.Obj.ID)

	p.Char.Data.Gold = 100
	s.handlePickup(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_PICKUP, 0, 10, 10, 0)})
	if p.Char.Data.Gold != 100 {
		t.Errorf("不该捡到别人的钱，实际 %d", p.Char.Data.Gold)
	}
	if got := groundGoldTotal(s); got != 250 {
		t.Errorf("金币该留在原地，实际地面 %d", got)
	}
	// 归属者自己来就捡得走
	s.handlePickup(nil, other, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_PICKUP, 0, 10, 10, 0)})
	if other.Char.Data.Gold != 250 {
		t.Errorf("归属者应捡到 250，实际 %d", other.Char.Data.Gold)
	}
}

// TestScatterKillGoldWiring 钉住"怪物死亡 ⇒ 金币**撒到地上**"这条接线
// （原版 `ScatterGolds`，ObjBase.pas:20655）：掉落表里的金币**不再直接进腰包**，
// 而是落成地面金币堆、且归属写的是击杀者。
//
// ⚠️ 这条**不能**放 e2e：蛤蟆那种掉落表是概率出金币（`1/2 金币 280`），
// 跑几次才中一次 ⇒ e2e 必然偶发失败。用单测把接线钉死（掉落表 1/1）。
func TestScatterKillGoldWiring(t *testing.T) {
	s, m, p := goldTestServer()
	s.data.drops = map[string]*entity.DropTable{
		"测试怪": entity.NewDropTable([]entity.DropItem{
			{ItemName: entity.GoldName, SelPoint: 0, MaxPoint: 1, Count: 400},
		}),
	}
	mon := newTestMonster(9, "测试怪", 10)
	mon.SetPlace(m, 8, 8, mon.Facing())

	got := s.scatterKillGold(mon, p.Obj.ID)
	if got <= 0 {
		t.Fatalf("掉落表命中金币却没算出来（gold=%d）", got)
	}
	if on := groundGoldTotal(s); on != int64(got) {
		t.Errorf("地面金币 = %d，期望 %d（金币必须落在地上）", on, got)
	}
	if p.Char.Data.Gold != 0 {
		t.Errorf("金币不该直接进腰包，实际 %d", p.Char.Data.Gold)
	}
	s.mu.RLock()
	var gi *GroundItem
	for _, g := range s.world.ground {
		gi = g
	}
	s.mu.RUnlock()
	if gi == nil || gi.Owner != p.Obj.ID {
		t.Errorf("金币堆归属应为击杀者 %d，实际 %+v", p.Obj.ID, gi)
	}
}

// TestClearMapMonstersNoDeadlock 回归：`clearMapMonsters` 原来在**持 s.mu 时**调
// `dropItems → dropPosition`（内部再取 `s.mu.Lock`）⇒ 自死锁。
// 修好后这个用例正常返回（若回归，测试会卡住直到 go test 超时）。
func TestClearMapMonstersNoDeadlock(t *testing.T) {
	s, m, _ := goldTestServer()
	mon := newTestMonster(5, "测试怪", 10)
	mon.SetPlace(m, 6, 6, mon.Facing())
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	done := make(chan int, 1)
	go func() { n, _ := s.clearMapMonsters(m, ""); done <- n }()
	select {
	case n := <-done:
		if n != 1 {
			t.Errorf("清掉的怪数 = %d，期望 1", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("clearMapMonsters 卡住了（大概率是又回到持锁调 dropPosition）")
	}
	if len(s.world.monsters) != 0 {
		t.Error("怪该被清掉")
	}
}

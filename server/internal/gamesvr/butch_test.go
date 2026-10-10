package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// butchTestServer 造一个"标了 MINE 的地图 + 站在 (5,5) 的玩家"，物品表由调用方给。
//
// 起点用 flagTestServer：它跑的是**真解析路径**（mapinfo 段头 + 同行属性），
// 并且把地图登记成地图号 "0"。地图本身全可走 ⇒ 需要墙的用例自己 `SetBlock`。
func butchTestServer(t *testing.T, items ...*data.StdItem) (*Server, *Player) {
	t.Helper()
	set, err := data.NewStdItemSet(items)
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s, m := flagTestServer(t, "[0 比奇省 0] MINE\n")
	s.data.tables = &data.Tables{Items: set}
	s.social.guilds = guild.NewManager(&fakeGuardGuildStore{m: map[string]*storage.Guild{}})
	p := newTestPlayer(1, "挖肉测试", entity.JobWarr)
	p.Obj.SetPlace(m, 5, 5, 0)
	p.Char.Data.HumItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	p.Char.Data.BagItems = make([]*pb.UserItem, entity.MaxBagSize)
	p.visible = entity.NewViewTracker()
	p.logonDone = true // handleButch/logon 门：没进游戏不收消息
	s.world.players[1] = p
	return s, p
}

// deadAnimal 在玩家正前方放一只刚死的动物（race 51 = 鸡）。
func deadAnimal(s *Server, p *Player, race uint16) *entity.Monster {
	m := newTestMonster(7, "鸡", 5)
	m.Info.Race = race
	m.SetMapRef(p.Obj.MapRef())
	m.SetPos(m.MapRef(), p.Obj.PosX(), p.Obj.PosY()-1) // 玩家面朝 dir=0（上）⇒ 正前方
	m.Alive = false
	s.mu.Lock()
	s.world.monsters[m.ID] = m
	s.mu.Unlock()
	return m
}

// butchOnce 发一次 CM_BUTCH 取肉（先把转身时间戳拨回去，免得被 TurnIntervalTime 挡）。
func butchOnce(s *Server, p *Player, m *entity.Monster) {
	p.turnAt = time.Time{}
	s.handleButch(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(
		proto.CM_BUTCH, int32(m.ID), uint16(m.PosX()), uint16(m.PosY()), 0)})
}

// countBagItem 数背包里某件物品的个数，并返回最后一件的耐久。
func countBagItem(s *Server, p *Player, name string) (int, uint32) {
	n, dura := 0, uint32(0)
	for _, it := range p.Char.Data.BagItems {
		if it == nil || it.Index == 0 {
			continue
		}
		if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil && tmpl.Name == name {
			n++
			dura = it.Dura
		}
	}
	return n, dura
}

// TestAnimalNoDropAtDeath 动物（鸡 51）死亡时**什么都不掉**：
// 原版 `Die` 里整段掉落被 `(not m_boAnimal)` 挡着（ObjBase.pas:20983-20999），
// 东西要取肉时才给。
func TestAnimalNoDropAtDeath(t *testing.T) {
	meat := wuItem(1, "鸡肉", 40, data.MinMax{})
	s, p := butchTestServer(t, meat)
	s.data.drops = map[string]*entity.DropTable{
		"鸡": entity.NewDropTable([]entity.DropItem{
			{ItemName: "鸡肉", SelPoint: 0, MaxPoint: 1, Count: 1},
		}),
		"蛤蟆": entity.NewDropTable([]entity.DropItem{
			{ItemName: "鸡肉", SelPoint: 0, MaxPoint: 1, Count: 1},
		}),
	}
	// 鸡 ⇒ 不掉
	chicken := newTestMonster(11, "鸡", 5)
	chicken.Info.Race = 51
	chicken.SetPlace(p.Obj.MapRef(), 5, 5, chicken.Facing())
	if got := s.scatterKillGold(chicken, p.Obj.ID); got != 0 {
		t.Errorf("动物不该在死亡时掉落，实际 %d", got)
	}
	if n := len(s.world.ground); n != 0 {
		t.Errorf("动物死亡后地面不该有东西，实际 %d 件", n)
	}
	// 蛤蟆（race 83，不是动物）⇒ 照常掉在地上
	frog := newTestMonster(12, "蛤蟆", 20)
	frog.Info.Race = 83
	frog.SetPlace(p.Obj.MapRef(), 6, 6, frog.Facing())
	s.scatterKillGold(frog, p.Obj.ID)
	if n := len(s.world.ground); n == 0 {
		t.Error("非动物应该照常掉落")
	}
}

// TestButchAnimalGivesMeat 取肉：皮革度挖到 ≤0 时变骷髅 + 把肉给取肉的人，
// 且 `StdMode = 40` 的肉**持久度 = 肉质量**（原版 ApplyMeatQuality，:20517）。
func TestButchAnimalGivesMeat(t *testing.T) {
	meat := wuItem(1, "鸡肉", 40, data.MinMax{})
	s, p := butchTestServer(t, meat)
	s.data.drops = map[string]*entity.DropTable{
		"鸡": entity.NewDropTable([]entity.DropItem{
			{ItemName: "鸡肉", SelPoint: 0, MaxPoint: 1, Count: 1},
		}),
	}
	mon := deadAnimal(s, p, 51)
	mon.MeatQuality = 5000
	mon.Leathery = 50
	mon.AnimalSet = true

	for i := 0; i < 40 && !mon.Skeleton; i++ {
		butchOnce(s, p, mon)
	}
	if !mon.Skeleton {
		t.Fatalf("挖了 40 次还没变骷髅（皮革度 %d）", mon.Leathery)
	}
	n, dura := countBagItem(s, p, "鸡肉")
	if n != 1 {
		t.Fatalf("背包里鸡肉 = %d 件，期望 1 件", n)
	}
	if dura != uint32(mon.MeatQuality) {
		t.Errorf("肉的持久度 = %d，期望 = 肉质量 %d", dura, mon.MeatQuality)
	}
	// 变骷髅之后再取也拿不到第二份
	before := n
	butchOnce(s, p, mon)
	if n2, _ := countBagItem(s, p, "鸡肉"); n2 != before {
		t.Errorf("骷髅不该再出肉：%d → %d", before, n2)
	}
}

// TestButchRejectsBadTarget 取肉的四个拒绝条件：
// 太远、活着的、已经不是动物、以及**转身间隔内**连点。
func TestButchRejectsBadTarget(t *testing.T) {
	meat := wuItem(1, "鸡肉", 40, data.MinMax{})
	s, p := butchTestServer(t, meat)
	mon := deadAnimal(s, p, 51)
	mon.MeatQuality = 9000
	mon.Leathery = 40
	mon.AnimalSet = true

	// ① 距离 > 2 格
	mon.SetPos(mon.MapRef(), p.Obj.PosX()+3, p.Obj.PosY())
	butchOnce(s, p, mon)
	if mon.Leathery != 40 {
		t.Errorf("超过 2 格的尸体不该能取肉（皮革度 %d）", mon.Leathery)
	}
	mon.SetPos(mon.MapRef(), p.Obj.PosX(), p.Obj.PosY()-1)

	// ② 活着的
	mon.Alive = true
	butchOnce(s, p, mon)
	if mon.Leathery != 40 {
		t.Errorf("活着的怪不该能取肉（皮革度 %d）", mon.Leathery)
	}
	mon.Alive = false

	// ③ 不是动物（蛤蟆 race 83）
	mon.Info.Race = 83
	butchOnce(s, p, mon)
	if mon.Leathery != 40 {
		t.Errorf("非动物不该能取肉（皮革度 %d）", mon.Leathery)
	}
	mon.Info.Race = 51

	// ④ 转身间隔内连点：第一次生效，紧接着的第二次被挡
	p.turnAt = time.Time{}
	req := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_BUTCH, int32(mon.ID), uint16(mon.PosX()), uint16(mon.PosY()), 0)}
	s.handleButch(nil, p, req)
	first := mon.Leathery
	if first >= 40 {
		t.Fatalf("第一次取肉应该削掉皮革度，实际仍 %d", first)
	}
	s.handleButch(nil, p, req) // 不拨时间戳 ⇒ 应被 TurnIntervalTime 挡
	if mon.Leathery != first {
		t.Errorf("间隔内的第二次取肉该被挡：%d → %d", first, mon.Leathery)
	}
}

// TestHitMonsterMeatDecays 挨打会削肉质量（原版 `TAnimalObject.Struck`：
// `Dec(m_nMeatQuality, Random(300))`，ObjBase.pas:2807-2811）；非动物不受影响。
func TestHitMonsterMeatDecays(t *testing.T) {
	meat := wuItem(1, "鸡肉", 40, data.MinMax{})
	s, p := butchTestServer(t, meat)
	mon := deadAnimal(s, p, 51)
	mon.MeatQuality = 9000
	mon.AnimalSet = true
	restore := withRnd(func(int) int { return 100 })
	defer restore()

	s.struckMonster(mon, 10, time.Now())
	if mon.MeatQuality != 8900 {
		t.Errorf("挨打后肉质量 = %d，期望 8900（9000 - Random(300)=100）", mon.MeatQuality)
	}
	s.hitMonsterMeat(mon, 7, true) // 魔法那条：按伤害量 ×1000
	if mon.MeatQuality != 1900 {
		t.Errorf("魔法削完 = %d，期望 1900（8900 - 7*1000）", mon.MeatQuality)
	}
	mon.MeatQuality = 9000
	mon.Info.Race = 83 // 蛤蟆不是动物
	s.struckMonster(mon, 10, time.Now())
	if mon.MeatQuality != 9000 {
		t.Errorf("非动物不该被削肉质量，实际 %d", mon.MeatQuality)
	}
}

// TestMakeMineOreBands 挖矿的矿石档位（官方 `!setup.txt:764-779`）：
//
//	Random(120) ∈ 1..2 金矿 / 3..3 银矿 / 4..20 铁矿 / 21..99 黑铁矿石 / 其余 铜矿
func TestMakeMineOreBands(t *testing.T) {
	items := []*data.StdItem{
		wuItem(1, goldStoneName, 43, data.MinMax{}),
		wuItem(2, silverStoneName, 43, data.MinMax{}),
		wuItem(3, steelStoneName, 43, data.MinMax{}),
		wuItem(4, blackStoneName, 43, data.MinMax{}),
		wuItem(5, copperStoneName, 43, data.MinMax{}),
	}
	cases := []struct {
		roll int
		want string
	}{
		{1, goldStoneName}, {2, goldStoneName},
		{3, silverStoneName},
		{4, steelStoneName}, {20, steelStoneName},
		{21, blackStoneName}, {99, blackStoneName},
		{0, copperStoneName}, {100, copperStoneName}, {119, copperStoneName},
	}
	for _, c := range cases {
		s, p := butchTestServer(t, items...)
		restore := withRnd(func(int) int { return c.roll })
		s.makeMine(p)
		restore()
		n, _ := countBagItem(s, p, c.want)
		if n != 1 {
			t.Errorf("抽签 %d 应该出 %s，实际背包里 %d 件", c.roll, c.want, n)
		}
	}
}

// TestMineNeedsPickaxeAndWall 挖矿的三个前提（原版 ObjBase.pas:8819-8852）：
// **重击包** + 手里是**鹤嘴锄**（`Shape = 19`）+ **正前方那格不可走**。
func TestMineNeedsPickaxeAndWall(t *testing.T) {
	pickaxe := wuItem(1, "鹤嘴锄", 6, data.MinMax{})
	pickaxe.Shape = 19
	sword := wuItem(2, "木剑", 5, data.MinMax{})
	ore := wuItem(3, copperStoneName, 43, data.MinMax{})
	s, p := butchTestServer(t, pickaxe, sword, ore)
	p.Obj.SetFacing(0) // 面朝上 ⇒ 正前方 (5,4)

	heavy := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HEAVYHIT, 0, 0, 0, 0)}
	hit := wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HIT, 0, 0, 0, 0)}

	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 1, Dura: 100, DuraMax: 100}
	p.Char.Data.BagItems[1] = &pb.UserItem{Index: 2, MakeIndex: 2, Dura: 100, DuraMax: 100}
	pickItem := s.equipWeaponForTest(p, 1)  // 鹤嘴锄实例
	swordItem := s.equipWeaponForTest(p, 2) // 木剑实例
	// ① 普通攻击包 ⇒ 不是挖矿
	p.Char.Data.HumItems[proto.SlotWeapon] = pickItem
	if s.handleMine(p, hit) {
		t.Error("CM_HIT 不该被当成挖矿")
	}
	// ② 拿的不是鹤嘴锄 ⇒ 不是挖矿
	p.Char.Data.HumItems[proto.SlotWeapon] = swordItem
	if s.handleMine(p, heavy) {
		t.Error("不是鹤嘴锄（Shape≠19）不该挖矿")
	}
	// ③ 正前方可走 ⇒ 不是挖矿
	p.Char.Data.HumItems[proto.SlotWeapon] = pickItem
	if s.handleMine(p, heavy) {
		t.Error("正前方可走时不该挖矿")
	}
	// ④ 正前方是墙 + 鹤嘴锄 + 重击 ⇒ 挖矿（抽签全中）
	if !p.Obj.MapRef().SetBlock(p.Obj.PosX(), p.Obj.PosY()-1, true) {
		t.Fatal("SetBlock 失败")
	}
	// ⚠️ 抽签全 0 时矿脉初始储量也是 0（`Random(200)`）⇒ 那等于空矿脉，先给它灌满
	s.mineAt(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY()-1).count = 10
	restore := withRnd(func(int) int { return 0 })
	ok := s.handleMine(p, heavy)
	restore()
	if !ok {
		t.Fatal("正前方是墙、手握鹤嘴锄、发重击包 ⇒ 应该认成挖矿")
	}
	if n, dura := countBagItem(s, p, copperStoneName); n != 1 {
		t.Fatalf("挖矿应该出 1 块矿，实际 %d 件", n)
	} else if dura != stoneMinDura {
		t.Errorf("矿石耐久 = %d，期望 %d（抽签全 0）", dura, stoneMinDura)
	}
	// 武器耐久掉了 5（`DoDamageWeapon(Random(15)+5)`，抽签 0 ⇒ 5）
	if w := s.equipAt(p, proto.SlotWeapon); w == nil || w.Dura != 95 {
		t.Errorf("武器耐久 = %v，期望 95（100-5）", w)
	}
}

// equipWeaponForTest 把背包里 Index == idx 的那件装到武器槽并返回它。
//
// ⚠️ 返回那件本身：用例里要反复换武器（先剑后锄再剑），只按背包下标找的话
// 装过一次之后背包里就没有它了，后面几次会**静默装不上**、断言全错。
func (s *Server) equipWeaponForTest(p *Player, idx uint32) *pb.UserItem {
	for i, it := range p.Char.Data.BagItems {
		if it != nil && it.Index == idx {
			p.Char.Data.HumItems[proto.SlotWeapon] = it
			p.Char.Data.BagItems[i] = &pb.UserItem{}
			return it
		}
	}
	return nil
}

// TestMineRefillAfterEmpty 矿脉挖空后**隔 10 分钟**才补（原版
// `if GetTickCount - m_dwAddStoneMineTick > 10 * 60 * 1000 then AddStoneMine`）。
func TestMineRefillAfterEmpty(t *testing.T) {
	ore := wuItem(1, copperStoneName, 43, data.MinMax{})
	s, p := butchTestServer(t, ore)
	ev := s.mineAt(p.Obj.MapRef(), 9, 9)
	ev.count = 0
	ev.addCount = 77

	// 没到时间：不补
	ev.addTick = time.Now()
	if _, ok := s.pileStones(p, 9, 9); ok {
		t.Error("矿脉空了、还没到补货时间，不该挖出东西")
	}
	if ev.count != 0 {
		t.Errorf("不该补货，实际 count = %d", ev.count)
	}
	// 过了 10 分钟：补成 addCount
	ev.addTick = time.Now().Add(-mineRefillInterval - time.Second)
	s.pileStones(p, 9, 9)
	// 补货与扣减在同一次调用里：这一下只补货（原版 else 分支不再扣 count）
	if ev.count != 77 {
		t.Errorf("补货后 count = %d，期望 77", ev.count)
	}
}

// TestMineViaMessageDispatch 挖矿要能**从消息分发那条路**走通：
// `handleAttack`（CM_HEAVYHIT 进来）→ 认成挖矿 → 出矿进背包 → 且**不再**当普通攻击处理。
//
// ⚠️ 这条不做 e2e：矿脉命中是 `1/2 × 1/8`（`MakeMineHitRate=2` × `MakeMineRate=8`）
// ⇒ 客户端要打几十下才可能出矿，放 e2e 里必然偶发失败。
func TestMineViaMessageDispatch(t *testing.T) {
	pickaxe := wuItem(1, "鹤嘴锄", 6, data.MinMax{})
	pickaxe.Shape = 19
	ore := wuItem(2, blackStoneName, 43, data.MinMax{})
	s, p := butchTestServer(t, pickaxe, ore)
	p.Char.Data.BagItems[0] = &pb.UserItem{Index: 1, MakeIndex: 1, Dura: 100, DuraMax: 100}
	p.Char.Data.HumItems[proto.SlotWeapon] = s.equipWeaponForTest(p, 1)
	p.Obj.SetFacing(0)
	if !p.Obj.MapRef().SetBlock(p.Obj.PosX(), p.Obj.PosY()-1, true) {
		t.Fatal("SetBlock 失败")
	}
	s.mineAt(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY()-1).count = 50

	// 抽签定死：命中(0 < 2)、出矿(0 < 8)、矿石档位落在黑铁矿(21..99 ⇒ 给 50)、耐久基础值
	calls := 0
	restore := withRnd(func(n int) int {
		calls++
		switch {
		case n == stoneTypeRate:
			return 50 // ⇒ 黑铁矿石
		default:
			return 0
		}
	})
	s.handleAttack(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(
		proto.CM_HEAVYHIT, 0, 0, 0, uint16(p.Obj.Facing()))})
	restore()

	if n, _ := countBagItem(s, p, blackStoneName); n != 1 {
		t.Fatalf("重击包挖矿应该出 1 块黑铁矿石，实际 %d 件（抽签调了 %d 次）", n, calls)
	}
	// 武器耐久掉的是 `Random(15)+5`
	if w := s.equipAt(p, proto.SlotWeapon); w == nil || w.Dura != 95 {
		t.Errorf("武器耐久 = %v，期望 95", w)
	}
}

// TestKillLeavesCorpse 怪死了要**留成尸体**（不然"取肉/变骷髅"在游戏里够不着），
// `corpseLifetime` 之后由 `tickMonsters` 收走（原版 ObjBase.pas:3769）。
func TestKillLeavesCorpse(t *testing.T) {
	meat := wuItem(1, "鸡肉", 40, data.MinMax{})
	s, p := butchTestServer(t, meat)
	mon := newTestMonster(21, "测试怪", 10)
	mon.Info.Race = 83
	mon.SetPlace(p.Obj.MapRef(), 6, 6, mon.Facing())
	s.mu.Lock()
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)
	s.mu.Unlock()
	mon.Alive = false

	s.mu.Lock()
	s.keepCorpseOrRemove(mon)
	s.mu.Unlock()
	if s.monsterByID(mon.ID) == nil {
		t.Fatal("普通怪死了应该留成尸体（否则取肉够不着）")
	}
	if mon.DeathAt.IsZero() {
		t.Error("死亡时刻该被记下")
	}
	// 到点 ⇒ 收走。⚠️ 收尸**只在 sweepCorpses**（5 秒那一档）里做：
	// `tickMonsters` 里原来还有一份不发包的清理，会把尸体先删掉，
	// 导致 `sweepCorpses` 发不出 EntityDisappear（客户端尸体永不消失）。
	mon.DeathAt = time.Now().Add(-corpseLifetime - time.Second)
	s.sweepCorpses(time.Now())
	if s.monsterByID(mon.ID) != nil {
		t.Error("超过 corpseLifetime 的尸体该被收走")
	}
}

// TestButchSkeletonToldToProto 变骷髅这件事**必须告诉新协议客户端**。
//
// ⚠️ 用户 2026-10-10 第 2 条：「挖完肉后的动物尸体，印象中应该会变化」。
// `broadcastSkeleton` 原来只发 legacy 的 `SM_SKELETON`，而 proto 玩家的 legacy
// 下行会被丢弃 ⇒ Proto 玩家永远看不到骷髅。这条把"会发 `Skeleton`"钉住。
func TestButchSkeletonToldToProto(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "鸡肉", 40, data.MinMax{}))
	sink := &protoSink{ch: make(chan *protocol.Envelope, 64)}
	p.protoOut = sink

	// ⚠️ 广播按**空间索引 + 视野半径**找观众（view.go:17-26）⇒ 测试服务器得给个
	// 视野半径（默认可能是 0），玩家还得进索引，否则没人收到
	s.cfg.viewRange = 12
	s.world.index.Add(p)
	mon := deadAnimal(s, p, 51)
	mon.MeatQuality = 5000
	mon.Leathery = 1 // 一刀就够
	mon.AnimalSet = true

	butchOnce(s, p, mon)
	if !mon.Skeleton {
		t.Fatal("这一刀该把皮革度挖穿（初值 1）")
	}
	var got *protocol.Skeleton
	for {
		select {
		case e := <-sink.ch:
			if k, ok := e.Body.(*protocol.Envelope_Skeleton); ok {
				got = k.Skeleton
			}
		default:
			if got == nil {
				t.Fatal("没收到 `Skeleton`（proto 玩家看不到尸体变化）")
			}
			return
		}
	}
}

// TestSaveSnapshotCarriesPosition 存档快照要带上**当前所在位置**。
//
// ⚠️ 用户 2026-10-10 第 5 条：下线再上回到了新手村。原因是 `joinWorld` 拿
// `CurMap/CurX/CurY` 定位（join.go:76-86），而全代码从来没写过这三个字段 ⇒
// 存的永远是新角色建号时的坐标。这条把"快照 = 当前地图 + 当前坐标"钉住。
func TestSaveSnapshotCarriesPosition(t *testing.T) {
	_, p := butchTestServer(t, wuItem(1, "鸡肉", 40, data.MinMax{}))
	// 挪到一个不是出生点的位置
	m := p.Obj.MapRef()
	p.Obj.SetPlace(m, 33, 44, 0)
	snap := saveSnapshotOf(p)
	if snap == nil || snap.Data == nil {
		t.Fatal("快照为空")
	}
	if snap.Data.CurMap != m.Name {
		t.Errorf("快照地图 = %q，期望 %q", snap.Data.CurMap, m.Name)
	}
	if snap.Data.CurX != 33 || snap.Data.CurY != 44 {
		t.Errorf("快照坐标 = (%d,%d)，期望 (33,44)", snap.Data.CurX, snap.Data.CurY)
	}
}

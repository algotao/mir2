package gamesvr

import (
	"fmt"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// eatTestServer 造一个带物品表的最小 Server + 玩家（装备槽已分配）。
func eatTestServer(t *testing.T, items ...*data.StdItem) (*Server, *Player) {
	t.Helper()
	set, err := data.NewStdItemSet(items)
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	// ⚠️ 起点必须是 testSlaveServer()（它备好 world.players/monsters/monsterIdx 与 social）：
	// 传送走 switchMap → updateVision，会读这些表；光造 `&Server{data:…}` 会空指针。
	srv := testSlaveServer()
	srv.data.tables = &data.Tables{Items: set}
	// 行会管理器也必须给：`guildNameOf`（→ `guildOf` → `OfMember`）在 nil 管理器上会 panic。
	srv.social.guilds = guild.NewManager(&fakeGuardGuildStore{m: map[string]*storage.Guild{}})
	if srv.world.index == nil {
		srv.world.index = world.NewSpatialIndex(32)
	}
	if srv.world.ground == nil {
		srv.world.ground = make(map[uint32]*GroundItem)
	}
	p := newTestPlayer(1, "消耗品测试", entity.JobWarr)
	p.Char.Data.HumItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	p.visible = entity.NewViewTracker()
	return srv, p
}

// withRnd 注入抽签源；返回值还原。
//
// ⚠️ 每个用例都必须注入：吃祝福油的第一件事就是 `Random(50) = 1` 的"倒大霉"判定，
// 用真随机会 1/50 概率把断言打成"武器被诅咒"，变成偶发失败。
func withRnd(f func(n int) int) func() {
	old := delphi.RandN
	delphi.RandN = f
	return func() { delphi.RandN = old }
}

// equipWeapon 给玩家装上一把武器（幸运位 btValue[3]、诅咒位 btValue[4]）。
func equipWeapon(p *Player, tmpl *data.StdItem, luck, curse byte) *pb.UserItem {
	ui := &pb.UserItem{
		Index:   uint32(tmpl.Index),
		Dura:    100,
		DuraMax: 100,
		Value:   make([]byte, entity.ItemValueLen),
	}
	ui.Value[btValueLuckIdx] = luck
	ui.Value[btValueCurseIdx] = curse
	p.Char.Data.HumItems[proto.SlotWeapon] = ui
	return ui
}

// oilTmpl / weaponTmpl：Shape=4 是祝福油；武器的 DC 跨度决定 nRand。
func oilTmpl() *data.StdItem {
	return &data.StdItem{Index: 1, Name: "祝福油", StdMode: 3, Shape: 4}
}

// weaponTmpl 造武器模板（idx 必须与切片位置一致；dcSpan 决定 nRand = dcSpan/5）。
func weaponTmpl(idx int32, dcSpan uint16) *data.StdItem {
	return &data.StdItem{Index: idx, Name: "测试剑", StdMode: 5, Shape: 1,
		DC: data.MinMax{Min: 0, Max: dcSpan}}
}

// TestOilOfLuckNoWeapon 没装备武器 ⇒ 返回 false（调用方**不消耗**油）。
func TestOilOfLuckNoWeapon(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	restore := withRnd(func(int) int { return 0 })
	defer restore()
	if s.eatUseItem(nil, p, oilTmpl()) {
		t.Error("没武器时吃祝福油应返回 false（原版 WeaptonMakeLuck 首行 Exit(False)）")
	}
}

// TestOilOfLuckFirstPoint 幸运 0 ⇒ 必成 +1（原版 `btValue[3] < nWeaponMakeLuckPoint1`）。
func TestOilOfLuckFirstPoint(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	ui := equipWeapon(p, weaponTmpl(2, 50), 0, 0)
	restore := withRnd(func(int) int { return 0 }) // 0 ≠ 1 ⇒ 不倒大霉
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("有武器时使用祝福油应返回 true（消耗）")
	}
	if got := ui.Value[btValueLuckIdx]; got != 1 {
		t.Errorf("幸运 0 ⇒ 应必成 +1，实际 %d", got)
	}
}

// TestOilOfLuckCurseFirst 诅咒位 > 0 时**先抵掉一次诅咒**，幸运不动
// （原版第一支 `if btValue[4] > 0 then Dec(btValue[4])`，且不再走后面的抽签）。
func TestOilOfLuckCurseFirst(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	ui := equipWeapon(p, weaponTmpl(2, 50), 2, 1)
	restore := withRnd(func(int) int { return 0 })
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("应返回 true")
	}
	if ui.Value[btValueCurseIdx] != 0 || ui.Value[btValueLuckIdx] != 2 {
		t.Errorf("应先抵诅咒：诅咒 1→%d、幸运保持 2→%d",
			ui.Value[btValueCurseIdx], ui.Value[btValueLuckIdx])
	}
}

// TestOilOfLuckBadRollCursesWeapon 1/50 的"倒大霉"：走 MakeWeaponUnlock（幸运 -1）。
func TestOilOfLuckBadRollCursesWeapon(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	ui := equipWeapon(p, weaponTmpl(2, 50), 2, 0)
	// 第一抽（Random(50)）返回 1 ⇒ 命中倒大霉分支
	restore := withRnd(func(n int) int {
		if n == nWeaponMakeUnLuckRate {
			return 1
		}
		return 0
	})
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("倒大霉那支原版也返回 True（油照样消耗）")
	}
	if ui.Value[btValueLuckIdx] != 1 {
		t.Errorf("倒大霉应扣 1 点幸运：2 → %d", ui.Value[btValueLuckIdx])
	}
}

// TestOilOfLuckFailStillConsumes 幸运到顶（7）时抽不中 ⇒ 提示"无效!"，
// 但**油仍然被消耗**（原版末尾 `Result := True`）——这是最容易实现错的一条。
func TestOilOfLuckFailStillConsumes(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	ui := equipWeapon(p, weaponTmpl(2, 50), weaponMakeLuckPoint3, 0)
	restore := withRnd(func(int) int { return 0 })
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("失败也返回 True（无效但消耗）")
	}
	if got := ui.Value[btValueLuckIdx]; got != weaponMakeLuckPoint3 {
		t.Errorf("幸运已到顶不该再涨：%d", got)
	}
}

// TestOilOfLuckNarrowWeaponCapsAtThree DC 跨度 < 5 ⇒ nRand = 0 ⇒
// `Random(0 * 40)` 恒为 0 ⇒ 第三段**永不中签** ⇒ 幸运上限实际是 3（原版行为）。
func TestOilOfLuckNarrowWeaponCapsAtThree(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 4)) // 跨度 4 ⇒ nRand = 0
	ui := equipWeapon(p, weaponTmpl(2, 4), 3, 0)
	// 除了'倒大霉'那一抽，其余一律返回 1（即"只要能中签就中"）
	restore := withRnd(func(n int) int {
		if n == nWeaponMakeUnLuckRate {
			return 0
		}
		return 1
	})
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("应返回 true")
	}
	if got := ui.Value[btValueLuckIdx]; got != 3 {
		t.Errorf("窄跨度武器（nRand=0）幸运不该超过 3，实际 %d", got)
	}
}

// TestOilOfLuckWideWeaponThirdPoint 宽跨度武器能走到第三段（幸运 3 → 4）。
func TestOilOfLuckWideWeaponThirdPoint(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50)) // 跨度 50 ⇒ nRand = 10
	ui := equipWeapon(p, weaponTmpl(2, 50), 3, 0)
	restore := withRnd(func(n int) int {
		switch n {
		case nWeaponMakeUnLuckRate:
			return 0 // 不倒大霉
		case 10 + weaponMakeLuckPoint2Rate: // 第二段：Random(nRand+6) = Random(16)
			return 0 // 不中
		case 10 * weaponMakeLuckPoint3Rate: // 第三段：Random(nRand*40) = Random(400)
			return 1 // 中
		}
		return 0
	})
	defer restore()

	if !s.eatUseItem(nil, p, oilTmpl()) {
		t.Fatal("应返回 true")
	}
	if got := ui.Value[btValueLuckIdx]; got != 4 {
		t.Errorf("第三段中签后幸运应为 4，实际 %d", got)
	}
}

// TestRepairOil 普修油：**永久磨损上限**、耐久补到 gap（上限 5000）、
// 耐久已满则不消耗（返回 false）；特修油只补耐久、不动上限。
func TestRepairOil(t *testing.T) {
	repairOil := &data.StdItem{Index: 1, Name: "修复油", StdMode: 3, Shape: 9}
	superOil := &data.StdItem{Index: 2, Name: "特修油", StdMode: 3, Shape: 10}
	weapon := weaponTmpl(3, 50)
	s, p := eatTestServer(t, repairOil, superOil, weapon)

	ui := equipWeapon(p, weapon, 0, 0)
	ui.Dura, ui.DuraMax = 50, 100

	// 普修：DuraMax -= (100-50)/30 = 1 ⇒ 99；Dura += min(5000, 99-50=49) ⇒ 99
	if !s.eatUseItem(nil, p, repairOil) {
		t.Fatal("普修应返回 true")
	}
	if ui.DuraMax != 99 || ui.Dura != 99 {
		t.Errorf("普修后耐久 = %d/%d，期望 99/99（上限永久磨损 1 点）", ui.Dura, ui.DuraMax)
	}

	// 耐久已满 ⇒ 不消耗
	if s.eatUseItem(nil, p, repairOil) {
		t.Error("耐久已满时用修复油应返回 false（不消耗）")
	}

	// 特修：只把 Dura 拉回 DuraMax，上限不变
	ui.Dura = 10
	if !s.eatUseItem(nil, p, superOil) {
		t.Fatal("特修应返回 true")
	}
	if ui.Dura != ui.DuraMax || ui.DuraMax != 99 {
		t.Errorf("特修后 = %d/%d，期望 %d/%d（上限不变）", ui.Dura, ui.DuraMax, ui.DuraMax, 99)
	}

	// 没武器 ⇒ false
	p.Char.Data.HumItems[proto.SlotWeapon] = &pb.UserItem{}
	if s.eatUseItem(nil, p, superOil) {
		t.Error("没武器时用特修油应返回 false")
	}
}

// TestEatUseItemsUnsupportedShapeNotConsumed 我们**不做**的形状必须返回 false ——
// 否则会"吃掉物品却没效果"。这里用彩票（`Shape = 11`）：原版自己把它注释掉了
// （`//取消彩票功能`，ObjBase.pas:23570-23575）⇒ `case` 里没有分支 ⇒ Result=False。
func TestEatUseItemsUnsupportedShapeNotConsumed(t *testing.T) {
	lottery := &data.StdItem{Index: 1, Name: "彩票", StdMode: 3, Shape: 11}
	s, p := eatTestServer(t, lottery)
	if s.eatUseItem(nil, p, lottery) {
		t.Error("未实现的 Shape 应返回 false（原版 EatUseItems 默认 Result=False）")
	}
}

// TestMapRandomPointInset 钉住原版 `MapRandomMove` 的"避边随机"（ObjBase.pas:9810-9830）：
// 边距按图高取 50 / 20 / 2，落点在 [边距, 宽-边距-2] 区间内。
func TestMapRandomPointInset(t *testing.T) {
	cases := []struct {
		w, h int
		edge int
	}{
		{200, 200, 50}, // 高 ≥ 150
		{200, 100, 20}, // 高 < 150
		{200, 20, 2},   // 高 < 30
	}
	for _, c := range cases {
		m := world.Generate("inset", c.w, c.h, false)
		// ⚠️ 原版只避**左/上**边：`Random(宽-边距-1) + 边距` ⇒ x ∈ [边距, 宽-2]，
		// 右边/下边仍可能贴着边缘（`Random` 的取值数比"边距"只少 1 个）。别照着
		// 直觉写成 [边距, 宽-边距-2]（第一版就这么写错了）。
		xLo, xHi := c.edge, c.w-2
		yLo, yHi := c.edge, c.h-2
		for i := 0; i < 200; i++ {
			x, y := mapRandomPoint(m)
			if x < xLo || x > xHi || y < yLo || y > yHi {
				t.Fatalf("%dx%d（边距 %d）落点 (%d,%d) 越出 [%d,%d]×[%d,%d]",
					c.w, c.h, c.edge, x, y, xLo, xHi, yLo, yHi)
			}
		}
	}
}

// TestScrollCastleWithoutGuildNotConsumed 没行会时用城堡回城卷 ⇒ 返回 false
// （原版 `if m_MyGuild <> nil` 否则整段跳过 ⇒ **不消耗**、连提示都没有）。
func TestScrollCastleWithoutGuildNotConsumed(t *testing.T) {
	oil := &data.StdItem{Index: 1, Name: "行会回城卷", StdMode: 3, Shape: 5}
	s, p := eatTestServer(t, oil)
	if s.eatUseItem(nil, p, oil) {
		t.Error("没行会时用城堡回城卷应返回 false（不消耗）")
	}
}

// TestScrollRandomTeleports 随机传送卷（Shape 2）在当前图落到"避边"区间内的**新**位置。
func TestScrollRandomTeleports(t *testing.T) {
	oil := &data.StdItem{Index: 1, Name: "随机传送卷", StdMode: 3, Shape: 2}
	s, p := eatTestServer(t, oil)
	m := world.Generate("scrolltest", 200, 200, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	mm.SetNames(map[string]string{"0": "scrolltest"})
	s.world.maps = mm
	if s.world.index == nil {
		s.world.index = world.NewSpatialIndex(32)
	}
	p.Obj.SetPlace(m, 100, 100, p.Obj.Facing())
	// ⚠️ 传送会走 switchMap → `p.visible.Clear()` ⇒ 视野跟踪器必须先建好，
	// 否则空指针（生产路径在 session.go:207 建）。
	p.visible = entity.NewViewTracker()

	if !s.eatUseItem(nil, p, oil) {
		t.Fatal("随机传送卷应返回 true")
	}
	if p.Obj.MapRef() != m {
		t.Fatal("随机传送卷不该换图")
	}
	if p.Obj.PosX() == 100 && p.Obj.PosY() == 100 {
		t.Error("随机传送卷应换一个落点") // 1/(149²) 的巧合概率，可忽略
	}
	if p.Obj.PosX() < 50 || p.Obj.PosX() > 198 || p.Obj.PosY() < 50 || p.Obj.PosY() > 198 {
		t.Errorf("落点 (%d,%d) 越出避边区间 [50,198]", p.Obj.PosX(), p.Obj.PosY())
	}
}

// ⚠️ 原版 `SM_BAGITEMS` 的 Series 就是 `m_ItemList.Count`（ObjBase.pas:15926），
// 客户端按收到顺序排列 ⇒ 它发回的下标是**压缩位次**；我们的 BagItems 是定长数组
// ⇒ 中间一旦留空洞，`CM_EAT`/`CM_TAKEONITEM` 的下标就会错位（会"用错物品/穿错装备"）。
func TestTakeBagItemKeepsBagCompact(t *testing.T) {
	s, p := eatTestServer(t, oilTmpl(), weaponTmpl(2, 50))
	d := p.Char.Data
	d.BagItems = make([]*pb.UserItem, 4)
	for i := range d.BagItems {
		d.BagItems[i] = &pb.UserItem{}
	}
	for i := 0; i < 3; i++ {
		d.BagItems[i] = &pb.UserItem{Index: uint32(i + 1)}
	}
	idxOf := func() []uint32 {
		out := make([]uint32, len(d.BagItems))
		for i, it := range d.BagItems {
			if it != nil {
				out[i] = it.Index
			}
		}
		return out
	}

	takeBagItemLocked(d, 1) // 删中间那格（这个用例只测纯行为，不涉及并发）
	if len(d.BagItems) != 4 {
		t.Fatalf("长度应保持 4（末尾补空槽），实际 %d", len(d.BagItems))
	}
	if got := idxOf(); got[0] != 1 || got[1] != 3 || got[2] != 0 || got[3] != 0 {
		t.Errorf("删除后应左移并末尾补空，实际 %v", got)
	}

	takeBagItemLocked(d, 0)
	if got := idxOf(); got[0] != 3 || got[1] != 0 {
		t.Errorf("再删第一格后实际 %v", got)
	}

	// 越界/非法下标不得 panic 也不得改动背包
	before := idxOf()
	takeBagItemLocked(d, 99)
	takeBagItemLocked(d, -1)
	s.takeBagItem(nil, 0)
	if got := idxOf(); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Errorf("非法下标不该改动背包：%v → %v", before, got)
	}
}

// TestScrollShape3RedName 红名回城卷（`Shape = 3`）：`PKLevel >= 2` 时回**红名回城点**
// （官方 `!setup.txt:128 RedHomeMap=3`，原版 `GetStartPoint` ObjBase.pas:9908-9913），
// 白名时与 Shape 1 相同（回存档里的 HomeMap）。
func TestScrollShape3RedName(t *testing.T) {
	s, p := eatTestServer(t, wuItem(1, "回城卷", 3, data.MinMax{}))
	// 三张图都要在表里（3 = 盟重省）
	mm := world.NewMapManager("", 8)
	mm.Put(world.Generate("0101", 40, 40, true))
	mm.Put(world.Generate("3", 60, 60, true))
	mm.Put(world.Generate("0", 40, 40, true))
	s.world.maps = mm
	p.Char.Data.HomeMap = "0101"

	// 白名：回 HomeMap
	p.Char.Data.PkPoint = 100 // PKLevel = 1
	if !s.scrollHomeMap(nil, p, true) {
		t.Fatal("白名的 Shape 3 该照常回城")
	}
	if got := p.Obj.MapRef().Name; got != "0101" {
		t.Errorf("白名该回 HomeMap(0101)，实际 %s", got)
	}

	// 红名（PKPoint=200 ⇒ PKLevel 2）：回红名点所在图
	if m0, err := mm.Get("0101"); err == nil {
		p.Obj.SetMapRef(m0) // 换个起点，免得被"同图随机落点"那条分支挡住
	}
	p.Char.Data.PkPoint = 200
	p.Char.Data.HomeMap = "0101"
	if !s.scrollHomeMap(nil, p, true) {
		t.Fatal("红名的 Shape 3 该回红名点")
	}
	if got := p.Obj.MapRef().Name; got != redHomeMap {
		t.Errorf("红名该回 %s，实际 %s", redHomeMap, got)
	}
}

package gamesvr

import (
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
	"github.com/algotao/mir2/server/protocol"
)

// TestSendBagItemsProto 背包下行：**新协议玩家必须收到 `BagItems`**。
//
// 用户 2026-10-09 说"包裹没显示"，根因就是这条链从来没接：`sendBagItems` 只发 legacy，
// 而 proto 玩家的 legacy 下行会被 `protoDown` 丢掉 —— 协议里 `BagItems` 早就定义好了，
// 但全仓**没有一处构造它**（`grep "BagItems{"` 只命中生成文件）。
//
// 这条用例同时钉住两件容易写错的事：
//   - 物品模板查表（`Index` 是 **1-based**，见 `itemStack`）；
//   - **可叠加物品的数量藏在 `Dura` 里**（原版没有数量字段）—— 取错了会显示成 1 个，
//     或者把装备的耐久当数量。
func TestSendBagItemsProto(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables

	// 拿真实表里的第 1、2 条当样本（`Index` 1-based）⇒ 断言的期望值也来自同一张表，
	// 不做硬编码（物品表换版也不会把用例写死）。
	first, second := tables.Items.Get(0), tables.Items.Get(1)
	if first == nil || second == nil {
		t.Fatal("物品表里没有前两条，用例前提不成立")
	}
	p := testMaster(100, "杂货商")
	p.Char.Data = &pb.CharacterData{
		BagItems: []*pb.UserItem{
			{Index: uint32(first.Index), Dura: 3},  // 药水：Dura 就是数量
			{Index: uint32(second.Index), Dura: 7}, // 同上
		},
	}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 8)}
	p.protoOut = sink

	s.sendBagItems(nil, p)

	var env *protocol.Envelope
	select {
	case env = <-sink.ch:
	default:
		t.Fatal("没有下发任何东西（新协议玩家应收到 BagItems）")
	}
	bag := env.GetBagItems()
	if bag == nil {
		t.Fatalf("应为 BagItems，实得 %T", env.Body)
	}
	items := bag.GetItems()
	// ⚠️ **按槽位发**：空槽也要占位（客户端格子号 == 服务端 `BagItems` 下标）——
	// 只发"有东西的格"会让位次整体前移 ⇒ 玩家把东西放到靠后的空格后又被贴回前面
	//（2026-10-12 用户报的「包裹物品不能换位置」）。
	if len(items) != entity.MaxBagSize {
		t.Fatalf("槽数 = %d，应为 %d（原版 46 格，空槽占位）", len(items), entity.MaxBagSize)
	}
	for i := 2; i < len(items); i++ {
		if items[i].GetIndex() != 0 {
			t.Fatalf("第 %d 格应是空槽，实得 index=%d", i, items[i].GetIndex())
		}
	}
	for i, want := range []struct {
		name  string
		looks uint32
		count uint32
	}{
		{first.Name, uint32(first.Looks), 3},
		{second.Name, uint32(second.Looks), 7},
	} {
		got := items[i]
		if got.GetName() != want.name {
			t.Errorf("第 %d 格名称 = %q，应为 %q", i, got.GetName(), want.name)
		}
		if got.GetLooks() != want.looks {
			t.Errorf("%q 的 looks = %d，应为 %d（客户端靠它取 `Items.wzl[looks]` 的图标）",
				want.name, got.GetLooks(), want.looks)
		}
		if got.GetCount() != want.count {
			t.Errorf("%q 数量 = %d，应为 %d（可叠加物的数量在 Dura 里）",
				want.name, got.GetCount(), want.count)
		}
	}
}

// TestMoveItemToEmptySlot 把东西放到**靠后的空格**要真的落在那一格。
//
// 用户 2026-10-12：「包裹中的物品不能换位置，应该是拾取后放到另一个空位，
// 则允许放置在该空位。现在总是顺序排」。
//
// 两处都得对：服务端的 `BagItems` 真把它挪过去，且**下发按槽位**（空槽占位）——
// 少一个占位就会让客户端的格子号整体前移，看着就是"又排回去了"。
func TestMoveItemToEmptySlot(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables
	p := testMaster(100, "整理包裹的")
	p.Char.Data = &pb.CharacterData{BagItems: []*pb.UserItem{
		{MakeIndex: 1, Index: 5, Dura: 1, DuraMax: 1}, // 第 0 格
		{MakeIndex: 2, Index: 6, Dura: 1, DuraMax: 1}, // 第 1 格
	}}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 8)}
	p.protoOut = sink

	s.doMoveItem(p, 0, 20) // 第 0 格 ⇒ 第 20 格（空槽，还在切片之外）

	got := p.Char.Data.BagItems
	if len(got) <= 20 {
		t.Fatalf("背包长度 = %d，应至少补到 21（目标槽在切片之外要先补空槽）", len(got))
	}
	if got[20].GetMakeIndex() != 1 {
		t.Errorf("第 20 格 = MakeIndex %d，应为 1（刚放过去的那件）", got[20].GetMakeIndex())
	}
	if got[0].GetIndex() != 0 {
		t.Errorf("原格该空出来，实得 index=%d", got[0].GetIndex())
	}
	if got[1].GetMakeIndex() != 2 {
		t.Errorf("第 1 格不该被动，实得 MakeIndex=%d", got[1].GetMakeIndex())
	}
	// 下发**按槽位** ⇒ 客户端那一格就在 20
	env := <-sink.ch
	bag := env.GetBagItems()
	if bag == nil {
		t.Fatalf("应下发 BagItems，实得 %T", env.Body)
	}
	items := bag.GetItems()
	if len(items) != entity.MaxBagSize {
		t.Fatalf("下发槽数 = %d，应为 %d", len(items), entity.MaxBagSize)
	}
	if items[20].GetIndex() != 5 {
		t.Errorf("下发第 20 格 index = %d，应为 5", items[20].GetIndex())
	}
	if items[0].GetIndex() != 0 {
		t.Errorf("下发第 0 格应是空槽，实得 index=%d", items[0].GetIndex())
	}
}

// TestProtoBagOnEnter 进图必须带**背包与已穿戴**（哪怕是空的）—— 这是"客户端看得到背包"的前提。
//
// 走的是真 TCP（`protoEnterWorld` 把 hello/认领/列角/选角/进图跑完）。
func TestProtoBagOnEnter(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)
	cl, ev := protoEnterWorld(t, addr, s, sessionID, charID)

	if env := cl.waitFor(ev, "BagItems", func(e *protocol.Envelope) bool {
		return e.GetBagItems() != nil
	}); env.GetBagItems().GetItems() == nil {
		t.Log("背包是空的（本用例的角色没有物品）—— 空表也要发，不然客户端不知道自己的背包状态")
	}
	if env := cl.waitFor(ev, "EquippedItems", func(e *protocol.Envelope) bool {
		return e.GetEquippedItems() != nil
	}); env.GetEquippedItems().GetItems() == nil {
		t.Log("身上没穿东西（同上）")
	}
}

// TestUpdateFeatureShapeAndEmpty 外观位：衣服/武器要用 **Shape** 算，且**空装备不能 panic**。
//
// 两条都是 2026-10-09 用户报"看不到穿衣服/持武器"时挖出来的病根：
//   - 用 `Looks`（背包图标图号）当外观号 ⇒ 取到 `Hum.wzl` 里不存在的块；
//   - 进图那条路上从没调过 `updateFeature` ⇒ 光身空手（这条由契约用例守着）。
//
// 这里还钉住"空 `HumItems` 不许炸"：那个越界 panic 会把服务端连接直接关掉
// （`Rust 选角剧本` 里的"小法"踩到过，表现为玩家掉线）。
func TestUpdateFeatureShapeAndEmpty(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables

	byName := func(n string) *pb.UserItem {
		it := tables.Items.GetByName(n)
		if it == nil {
			t.Fatalf("物品表里没有 %q", n)
		}
		return &pb.UserItem{MakeIndex: 1, Index: uint32(it.Index), Dura: it.DuraMax, DuraMax: it.DuraMax}
	}

	p := testMaster(100, "穿衣的")
	p.Char.Data = &pb.CharacterData{HumItems: []*pb.UserItem{
		byName("布衣(女)"), // U_DRESS = 0
		byName("铁剑"),    // U_WEAPON = 1
	}}
	s.updateFeature(p)
	f := p.Obj.FeatureBits()
	// 布衣(女)：Shape=1、StdMode=11 ⇒ Dress = 1*2+1 = 3（Hum.wzl 第 3 块 = 布衣女）
	// 铁剑：Shape=2、角色男 ⇒ Weapon = 2*2+0 = 4（`ObjBase.pas:20018` 的 `Shape*2+性别`）
	if got := proto.FeatureDress(f); got != 3 {
		t.Errorf("Dress = %d，应为 3（布衣(女) Shape=1、女装 +1）—— 用 Looks 会得到 80", got)
	}
	if got := proto.FeatureWeapon(f); got != 4 {
		t.Errorf("Weapon = %d，应为 4（铁剑 Shape=2、男 ⇒ 2*2+0）—— 用 Looks 会得到 36", got)
	}

	// 木剑 —— **用户 2026-10-09 截图里那把**：`Shape=1` ⇒ 男 = 2、女 = 3，
	// 正好落在 `Weapon.wzl` 的块 2/3（图号 1200/1800），那两块导出来就是
	// "握在手里的**棕木剑**"（用户给的官方截图与之一致）。
	//
	// 这是本用例最该钉住的一条：`Shape*2+性别` 一旦退回 `Shape`，木剑会画成
	// 块 1 = **下一把武器**（细长银剑，用户原话"更像长剑铁剑"）。
	for _, c := range []struct {
		sex  uint32
		want uint8
	}{{0, 2}, {1, 3}} {
		q := testMaster(200+c.sex, "持木剑的")
		q.Char.Data = &pb.CharacterData{
			Sex:      c.sex,
			HumItems: []*pb.UserItem{nil, byName("木剑")},
		}
		s.updateFeature(q)
		if got := proto.FeatureWeapon(q.Obj.FeatureBits()); got != c.want {
			t.Errorf("木剑 性别%d：Weapon = %d，应为 %d（Shape=1 ⇒ 1*2+性别）", c.sex, got, c.want)
		}
	}
	// 头发/race 不能被外观重算弄丢
	if got := proto.FeatureHair(f); got != 0 {
		t.Logf("Hair = %d（本用例没设过，0 正常）", got)
	}

	// 空装备：不许 panic，且两位都归 0
	empty := testMaster(101, "光身的")
	empty.Char.Data = &pb.CharacterData{}
	s.updateFeature(empty)
	if got := empty.Obj.FeatureBits(); proto.FeatureDress(got) != 0 || proto.FeatureWeapon(got) != 0 {
		t.Errorf("空装备时外观位应为 0，实得 dress=%d weapon=%d",
			proto.FeatureDress(empty.Obj.FeatureBits()), proto.FeatureWeapon(empty.Obj.FeatureBits()))
	}
}

// TestDialogAutoClose 走出对话范围（或 NPC 不在了）要自动关，并下行 `NpcClose`。
//
// 用户 2026-10-09 第 3 条：「离开对话有效区域，应自动关闭对话窗口」。
// 判据必须与"能不能点开"**同一条**（8 格）—— 两边不一致就会出现
// "点不开却关不掉"这种怪状态。
func TestDialogAutoClose(t *testing.T) {
	s := testSlaveServer()
	m := world.Generate("0", 60, 60, false)
	npc := newTestMonster(proto.NpcIDBase+1, "屠夫", 1)
	npc.IsNPC = true
	npc.Object.SetPlace(m, 10, 10, entity.DirDown)
	s.world.monsters[npc.ID] = npc

	p := testMaster(100, "路人")
	p.Obj.SetPlace(m, 12, 10, entity.DirDown) // 距离 2 ⇒ 在范围内
	p.dialog = &dialog{scriptName: "x", npcID: npc.ID}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 4)}
	p.protoOut = sink

	s.tickDialogRange(p)
	if p.dialog == nil {
		t.Fatal("还在范围内（2 格），不该关")
	}

	// 走远到 8 格外
	p.Obj.SetPlace(m, 40, 40, entity.DirDown)
	s.tickDialogRange(p)
	if p.dialog != nil {
		t.Error("走出范围后应清掉对话状态")
	}
	select {
	case env := <-sink.ch:
		if env.GetNpcClose() == nil {
			t.Errorf("应下行 NpcClose，实得 %T", env.Body)
		}
	default:
		t.Error("走出范围必须下行 NpcClose —— 客户端自己不知道，面板会一直挂着")
	}

	// NPC 没了（换图/被清）也要关
	p.dialog = &dialog{scriptName: "x", npcID: 999_999}
	s.tickDialogRange(p)
	if p.dialog != nil {
		t.Error("NPC 不在了也该关")
	}
}

// TestStatusBitsRedName 红名位：PK 值到红档（`PKLevel ≥ 2`）才置。
func TestStatusBitsRedName(t *testing.T) {
	p := testMaster(100, "张三")
	p.Char.Data = &pb.CharacterData{PkPoint: 0}
	if got := p.statusBits(); got&entity.StateRedName != 0 {
		t.Errorf("PK 0 不该是红名（bits=%#x）", got)
	}
	p.Char.Data.PkPoint = 100 // PKLevel = 1
	if got := p.statusBits(); got&entity.StateRedName != 0 {
		t.Errorf("PKLevel=1 还不该是红名（bits=%#x）", got)
	}
	p.Char.Data.PkPoint = 200 // PKLevel = 2 ⇒ 红名
	if got := p.statusBits(); got&entity.StateRedName == 0 {
		t.Errorf("PKLevel=2 应是红名（bits=%#x）", got)
	}
}

// TestProtoDropItem 新协议丢弃：slot → 按 MakeIndex 走 legacy `handleDropItem` 同一条路。
//
// 钉住两件事：
//   - 新协议的 slot 是**背包下标**，不是 MakeIndex（翻译错了会丢错东西）；
//   - 丢成功后背包那格清空、地上多一件（客户端靠这两个刷新看到结果）。
func TestProtoDropItem(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0]\n")
	it := &data.StdItem{Index: 1, Name: "金创药(小量)", StdMode: 0}
	set, err := data.NewStdItemSet([]*data.StdItem{it})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	s.data.tables = &data.Tables{Items: set}

	p := newTestPlayer(1, "丢弃协议", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.Char.Data.BagItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	// 故意放在第 3 格：下标与 MakeIndex 不同，才能证明翻译走的是 slot
	p.Char.Data.BagItems[2] = &pb.UserItem{Index: 1, MakeIndex: 99, Dura: 1, DuraMax: 1}

	ps := &protoSession{srv: s, player: p}
	if !ps.onDropItem(&protocol.DropItem{Slot: 2, Count: 1}) {
		t.Fatal("onDropItem 应返回 true（消息已处理）")
	}
	// ⚠️ 不能按下标断言：`takeBagItem` 取走后会**前移补位**（背包无空洞）。
	for _, it := range p.Char.Data.BagItems {
		if it != nil && it.MakeIndex == 99 {
			t.Error("丢弃后背包里不该还有 MakeIndex=99")
		}
	}
	if len(s.world.ground) != 1 {
		t.Fatalf("地上应多一件，实得 %d 件", len(s.world.ground))
	}
}

// TestProtoDropItemEmptySlot 空格丢弃 = 静默（客户端与服务端背包偶尔差一步）。
func TestProtoDropItemEmptySlot(t *testing.T) {
	s, m := flagTestServer(t, "[0 比奇省 0]\n")
	p := newTestPlayer(1, "丢弃空格", entity.JobWarr)
	p.Obj.SetPlace(m, 10, 10, p.Obj.Facing())
	p.Char.Data.BagItems = make([]*pb.UserItem, proto.MaxEquipSlot)

	ps := &protoSession{srv: s, player: p}
	if !ps.onDropItem(&protocol.DropItem{Slot: 0, Count: 1}) {
		t.Fatal("onDropItem 应返回 true")
	}
	if len(s.world.ground) != 0 {
		t.Errorf("空格不该产生地面物品：%d 件", len(s.world.ground))
	}
}

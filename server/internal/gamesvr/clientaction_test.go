package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// TestChanceOneIn 钉住原版 `Random(N) = 0` 的 1/N 语义与我们的"0 = 关掉"扩展。
func TestChanceOneIn(t *testing.T) {
	for i := 0; i < 32; i++ {
		if !chanceOneIn(1) {
			t.Fatal("1/N=1 应该必中")
		}
		if chanceOneIn(0) {
			t.Fatal("N=0 表示关掉该档，不该中")
		}
		if chanceOneIn(-5) {
			t.Fatal("负数当关掉")
		}
	}
	hit := 0
	for i := 0; i < 4000; i++ {
		if chanceOneIn(2) {
			hit++
		}
	}
	if hit == 0 || hit == 4000 {
		t.Fatalf("1/2 的抽签 4000 次命中 %d 次，不像 1/2", hit)
	}
}

// fillDieItems 把玩家的背包/装备填成 bag 件物品 +（equip>0 时）1 件装备。
//
// ⚠️ 背包是**定长切片**（`entity.MaxBagSize`），不能 append/清空 —— 只置空再填。
func fillDieItems(p *Player, bag, equip int) {
	d := p.Char.Data
	for i := range d.BagItems {
		d.BagItems[i] = nil
	}
	for i := 0; i < bag && i < len(d.BagItems); i++ {
		d.BagItems[i] = pbUserItemForTest(uint32(i + 1))
	}
	for i := range d.HumItems {
		d.HumItems[i] = nil
	}
	if equip > 0 && len(d.HumItems) > 0 {
		d.HumItems[0] = pbUserItemForTest(1)
	}
}

// TestDeathDropRates 死亡掉率对齐 `!setup.txt:814-817`（**1/N** 语义）：
//
//	包裹 `DieScatterBagRate=200`（1/200）、装备 `DieDropUseItemRate=30`（1/30）、
//	红名（PKLevel>2）装备 `DieRedDropUseItemRate=15`、
//	红名（PKLevel>=2）包裹 `DieRedScatterBagAll=1` ⇒ **全掉**。
//
// 抽签类只能钉两端的确定性行为（0=不掉 / 1=必掉），中间概率由 `chanceOneIn` 的分布测兜底。
func TestDeathDropRates(t *testing.T) {
	s, p := butchTestServer(t,
		wuItem(1, "测试物品一", 1, data.MinMax{}),
		wuItem(2, "测试物品二", 1, data.MinMax{}),
		wuItem(3, "测试物品三", 1, data.MinMax{}),
	)
	// ⚠️ 空槽现在是**零值 UserItem 占位**（P1-9：掉装备只置空该格、不压缩数组），
	// 所以判"装备还在不在"要看 `Index != 0`，不能只看非 nil。
	state := func() (bag int, hasEquip bool) {
		return countBagItems(p), len(p.Char.Data.HumItems) > 0 &&
			p.Char.Data.HumItems[0] != nil && p.Char.Data.HumItems[0].Index != 0
	}

	// ① 只掉包裹（装备档关掉）
	fillDieItems(p, 2, 1)
	s.cfg.deathDropBagOneIn, s.cfg.deathDropEquipOneIn = 1, 0
	s.cfg.deathDropRedEquipIn, s.cfg.deathScatterBagAllOn = 0, false
	s.deathDrop(nil, p)
	if bag, hasEquip := state(); bag != 0 || !hasEquip {
		t.Fatalf("包裹 1/1 该全掉、装备 0 该留着；实际剩包裹 %d 件、装备还在=%v", bag, hasEquip)
	}

	// ② 只掉装备（包裹档关掉）
	fillDieItems(p, 2, 1)
	s.cfg.deathDropBagOneIn, s.cfg.deathDropEquipOneIn = 0, 1
	s.deathDrop(nil, p)
	if bag, hasEquip := state(); bag != 2 || hasEquip {
		t.Fatalf("包裹 0 该留着、装备 1/1 该掉；实际剩包裹 %d 件、装备还在=%v", bag, hasEquip)
	}

	// ③ 红名（PKPoint=200 ⇒ PKLevel 2）：DieRedScatterBagAll ⇒ 包裹**全掉**（档位是 0 也掉）
	fillDieItems(p, 2, 1)
	s.cfg.deathDropBagOneIn, s.cfg.deathDropEquipOneIn = 0, 0
	s.cfg.deathDropRedEquipIn, s.cfg.deathScatterBagAllOn = 1, true
	p.Char.Data.PkPoint = 200
	s.deathDrop(nil, p)
	if bag, hasEquip := state(); bag != 0 || !hasEquip {
		t.Fatalf("红名包裹该全掉、红名装备档 1/1 该掉；实际剩包裹 %d 件、装备 %v", bag, hasEquip)
	}

	// ④ "红名全掉"关掉 ⇒ 回到 1/N 档（0 = 不掉）
	fillDieItems(p, 2, 1)
	s.cfg.deathScatterBagAllOn = false
	s.deathDrop(nil, p)
	if bag, hasEquip := state(); bag != 2 || !hasEquip {
		t.Fatalf("红名全掉关掉后该按 0 档不掉；实际剩包裹 %d 件、装备还在=%v", bag, hasEquip)
	}

	// ⑤ 白名（PKLevel 1）走**普通**装备档：普通 0、红名 1 ⇒ 不该掉
	fillDieItems(p, 0, 1)
	s.cfg.deathDropEquipOneIn, s.cfg.deathDropRedEquipIn = 0, 1
	p.Char.Data.PkPoint = 100
	s.deathDrop(nil, p)
	if _, hasEquip := state(); !hasEquip {
		t.Fatal("白名该走普通装备档（0=不掉），不该用红名档")
	}
}

// TestDeathDropKeepsEquipSlots 守住 P1-9：掉装备必须**只置空那一格**。
//
// `HumItems` 是定长穿戴槽位表（原版 `THumItems = array[0..12]`，**下标就是槽位号**），
// 不是可压缩的列表。原实现用 `append(d.HumItems[:i], d.HumItems[i+1:]...)` 删元素
// ⇒ 后面的装备全部前移一格、长度减一：剩下来的装备被当成戴在**错的槽位**
// （属性重算、协议下发、后续换装都按槽号解释），掉两件还会继续错位。
// 原版 `DropUseItems` 是 `m_UseItems[i].wIndex := 0`（长度恒定）。
func TestDeathDropKeepsEquipSlots(t *testing.T) {
	s, p := butchTestServer(t,
		wuItem(1, "测试物品一", 1, data.MinMax{}),
		wuItem(2, "测试物品二", 1, data.MinMax{}),
	)
	d := p.Char.Data
	// 13 个槽位各放一件"身份可辨"的装备（MakeIndex = 槽号+1）
	d.HumItems = make([]*pb.UserItem, proto.MaxEquipSlot)
	for i := range d.HumItems {
		d.HumItems[i] = pbUserItemForTest(uint32(i + 1))
	}
	d.PkPoint = 0 // 白名 ⇒ 走普通装备档
	s.cfg.deathDropBagOneIn, s.cfg.deathDropEquipOneIn = 0, 1
	s.cfg.deathDropRedEquipIn, s.cfg.deathScatterBagAllOn = 0, false

	s.deathDrop(nil, p)

	if got := len(d.HumItems); got != proto.MaxEquipSlot {
		t.Fatalf("掉装备后槽位数 = %d，期望仍是 %d（不能压缩数组）",
			got, proto.MaxEquipSlot)
	}
	dropped := 0
	for i, it := range d.HumItems {
		if it == nil || it.Index == 0 {
			dropped++ // 被掉掉的那件：本槽置空
			continue
		}
		// 没掉的必须**还在原来的槽位**（MakeIndex 由槽号决定）
		if int(it.MakeIndex) != i+1 {
			t.Fatalf("槽 %d 里是 MakeIndex=%d 的装备，期望 %d —— 槽位被挪动了（P1-9 回归）",
				i, it.MakeIndex, i+1)
		}
	}
	if dropped == 0 {
		t.Fatal("装备档设成必掉，应该有槽位被清空")
	}
	// 掉下来的件数 = 置空的槽位数（都落到地上）
	ground := 0
	for _, g := range s.world.ground {
		if g != nil {
			ground++
		}
	}
	if ground != dropped {
		t.Errorf("地面物品 %d 件，期望等于被掉掉的装备件数 %d", ground, dropped)
	}
}

// TestSitDownThrottle 打坐与转身/取肉**共用**节流（原版 `m_dwTurnTick`）：
// 距上次动作不足 `TurnIntervalTime` 时整条包被丢掉（时间戳不动）；
// 死了也不能打坐（原版 `if m_boDeath ... then Exit`）。
func TestSitDownThrottle(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品一", 1, data.MinMax{}))

	p.turnAt = time.Now().Add(-time.Second)
	before := p.turnAt
	s.handleSitDown(nil, p)
	if !p.turnAt.After(before) {
		t.Error("距上次动作超过间隔，打坐应该成功并推进时间戳")
	}
	stamp := p.turnAt
	s.handleSitDown(nil, p)
	if !p.turnAt.Equal(stamp) {
		t.Error("间隔内连点两次打坐，第二次该被丢掉（时间戳不该动）")
	}
	p.Char.Data.Abil.Hp = 0
	p.turnAt = time.Now().Add(-time.Second)
	stamp = p.turnAt
	s.handleSitDown(nil, p)
	if !p.turnAt.Equal(stamp) {
		t.Error("死了不该能打坐")
	}
}

// TestSoftCloseFlags 软关服把原版那两个标志记在角色上
// （`m_boSoftClose`/`m_boReconnection`，ObjBase.pas:4751-4755）。
func TestSoftCloseFlags(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品一", 1, data.MinMax{}))
	if p.softClose || p.reconnection {
		t.Fatal("初始应该是 false")
	}
	s.handleSoftClose(nil, p)
	if !p.softClose || !p.reconnection {
		t.Error("CM_SOFTCLOSE 该置 m_boSoftClose/m_boReconnection 两个标志")
	}
}

// TestClientActionsDispatched 两条消息都要**从消息分发那条路**走通
// （`handleGameMsg` 的 case），不是单测里直接调函数 —— "处理器写了但没接线"
// 正是这轮审计反复抓到的坑。
func TestClientActionsDispatched(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品一", 1, data.MinMax{}))

	// CM_SITDOWN ⇒ handleSitDown（节流时间戳被推进）
	p.turnAt = time.Now().Add(-time.Second)
	stamp := p.turnAt
	s.handleGameMsg(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SITDOWN, 0, 0, 0, 0)})
	if !p.turnAt.After(stamp) {
		t.Error("CM_SITDOWN 没走到 handleSitDown（分发处没接线？）")
	}

	// CM_SOFTCLOSE ⇒ handleSoftClose
	s.handleGameMsg(nil, p, wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SOFTCLOSE, 0, 0, 0, 0)})
	if !p.softClose {
		t.Error("CM_SOFTCLOSE 没走到 handleSoftClose（分发处没接线？）")
	}
}

// TestGMReconnection 复刻 `CmdReconnection`（ObjBase.pas:14086-14105）的三条门：
// 权限 ≥ 6、参数要两个、通过后置 `m_boReconnection` 并把 "ip/port" 发下去。
//
// ⚠️ 我们看不到出站包（send 直接写 conn）⇒ 断言落在**可观测的状态**上：
// `Player.reconnection` 与"缺参数时不该置位"。
func TestGMReconnection(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))

	// ① 权限不足（gmOpen 关、permission=0）⇒ 拒绝
	s.cfg.gmOpen = false
	p.permission = 0
	s.gmReconnection(nil, p, []string{"127.0.0.1", "7401"})
	if p.reconnection {
		t.Error("权限不足时不该置 reconnection")
	}

	// ② 权限够但缺参数 ⇒ 只提示格式
	p.permission = 6
	s.gmReconnection(nil, p, []string{"127.0.0.1"})
	if p.reconnection {
		t.Error("参数不全时不该置 reconnection")
	}

	// ③ 正常 ⇒ 置位（原版 RM_RECONNECTION 里那句 m_boReconnection := True）
	s.gmReconnection(nil, p, []string{"127.0.0.1", "7401"})
	if !p.reconnection {
		t.Error("通过后该置 reconnection")
	}

	// ④ -gm-open（e2e 用）时不过权限门
	s2, p2 := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	s2.cfg.gmOpen = true
	p2.permission = 0
	s2.gmReconnection(nil, p2, []string{"127.0.0.1", "7401"})
	if !p2.reconnection {
		t.Error("-gm-open 下应放行（与 @give/@level 一致）")
	}
}

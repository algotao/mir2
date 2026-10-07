package gamesvr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// repairTestServer 造"会说 @repair/@s_repair 的铁匠 + 手持破损武器的玩家"。
//
// `cmdLine` 是脚本头里声明的命令（原版 `m_boRepair`/`m_boS_repair` 就是按它置位的）。
func repairTestServer(t *testing.T, cmdLine string) (*Server, *Player, *entity.Monster) {
	t.Helper()
	weapon := wuItem(1, "测试剑", 5, data.MinMax{}) // StdMode 5 = 武器
	weapon.Price = 3000
	s, p := butchTestServer(t, weapon)

	dir := t.TempDir()
	body := "(" + cmdLine + ")\r\n\r\n[@main]\r\n你好\\ \\\r\n"
	if err := os.WriteFile(filepath.Join(dir, "9smith-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir
	s.npc.defs = []*data.NPC{{ID: "9smith", Name: "铁匠铺老板"}}

	npc := newTestMonster(88, "铁匠铺老板", 10)
	npc.IsNPC = true
	npc.ID = 88
	npc.SetPlace(p.Obj.MapRef(), 6, 5, npc.Facing())
	s.mu.Lock()
	s.world.monsters[npc.ID] = npc
	s.mu.Unlock()

	p.dialog = &dialog{npcID: npc.ID, scriptName: "9smith", label: "@main"}
	p.Char.Data.Gold = 100000
	// 武器槽（SlotWeapon）上一把跌破一半的武器：Dura 100 / DuraMax 1000
	p.Char.Data.HumItems[proto.SlotWeapon] = &pb.UserItem{Index: 1, MakeIndex: 1, Dura: 100, DuraMax: 1000}
	return s, p, npc
}

// repairWeapon 按**官方字段**发修理报文（P1-7）：
// Recog = 商人 ActorId、Param/Tag = MakeLong(MakeIndex)、body = 物品名
// （客户端 ClMain.pas:3711 → `SendRepairItem(g_nCurMerchant, MakeIndex, name)`）。
func repairWeapon(s *Server, p *Player, npc *entity.Monster) {
	makeIdx := p.Char.Data.HumItems[proto.SlotWeapon].MakeIndex
	s.handleRepairItem(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERREPAIRITEM, int32(npc.ID),
			uint16(uint32(makeIdx)&0xFFFF), uint16(uint32(makeIdx)>>16), 0),
		Body: "测试剑",
	})
}

// TestRepairRequiresBoundMerchant 守住 P1-10：修理也要先过"商人存在 + 同地图 +
// 15 格内 + 正在与它对话"这道门（原版 `ClientRepairItem` 里
// `FindMerchant(nParam1)` + 距离判定的那一段，ObjBase.pas:24653-24680）。
func TestRepairRequiresBoundMerchant(t *testing.T) {
	// ① 报文里的商人 ActorId 是编的（不是当前在谈的那个）
	s, p, npc := repairTestServer(t, "@repair")
	s.handleRepairItem(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERREPAIRITEM, int32(npc.ID)+1000, 1, 0, 0),
		Body: "测试剑",
	})
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 || p.Char.Data.Gold != 100000 {
		t.Errorf("商人 ActorId 对不上时不该修（Dura=%d 金币=%d）", it.Dura, p.Char.Data.Gold)
	}

	// ② 没开对话
	s, p, npc = repairTestServer(t, "@repair")
	p.dialog = nil
	repairWeapon(s, p, npc)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 {
		t.Errorf("没开对话时不该修（Dura=%d）", it.Dura)
	}

	// ③ 走远了（> 15 格）
	s, p, npc = repairTestServer(t, "@repair")
	p.Obj.SetPlace(p.Obj.MapRef(), 40, 40, 0)
	repairWeapon(s, p, npc)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 {
		t.Errorf("离商人 15 格以上时不该修（Dura=%d）", it.Dura)
	}

	// ④ 报文里 MakeIndex 编错 ⇒ 找不到物品，不修（也不能修别人/别的东西）
	s, p, npc = repairTestServer(t, "@repair")
	s.handleRepairItem(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERREPAIRITEM, int32(npc.ID), 999, 0, 0),
		Body: "测试剑",
	})
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 || p.Char.Data.Gold != 100000 {
		t.Errorf("MakeIndex 对不上时不该修（Dura=%d 金币=%d）", it.Dura, p.Char.Data.Gold)
	}
}

// TestRepairBagItemByMakeIndex 原版路径：修理窗口拖的是**背包**里的东西，
// 服务端按 MakeIndex 在背包里找（`ClientRepairItem` 遍历 `m_ItemList`）。
func TestRepairBagItemByMakeIndex(t *testing.T) {
	s, p, npc := repairTestServer(t, "@repair")
	p.Char.Data.HumItems[proto.SlotWeapon] = &pb.UserItem{}
	bagIdx := 2
	p.Char.Data.BagItems[bagIdx] = &pb.UserItem{Index: 1, MakeIndex: 4242, Dura: 100, DuraMax: 1000}

	s.handleRepairItem(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERREPAIRITEM, int32(npc.ID), 4242&0xFFFF, 4242>>16, 0),
		Body: "测试剑",
	})

	it := p.Char.Data.BagItems[bagIdx]
	if it == nil || it.Dura != 970 || it.DuraMax != 970 {
		t.Fatalf("背包里的武器普修后 = %+v，期望 970/970", it)
	}
	if p.Char.Data.Gold != 100000-900 {
		t.Errorf("普修背包物品后金币 = %d，期望 %d", p.Char.Data.Gold, 100000-900)
	}
}

// TestRepairNormalTierWearsDuraMax 普修：价格 `原价 div 3 / DuraMax * 缺损`，
// 修满但**上限永久磨损** `缺损/30`（原版 `nRepairItemDecDura=30`，ObjNpc.pas:2482）。
func TestRepairNormalTierWearsDuraMax(t *testing.T) {
	s, p, npc := repairTestServer(t, "@trading @buy @sell @repair @s_repair")
	repairWeapon(s, p, npc)

	it := p.Char.Data.HumItems[proto.SlotWeapon]
	// 缺损 900 ⇒ 上限 1000 - 900/30 = 970，耐久修满
	if it.DuraMax != 970 || it.Dura != 970 {
		t.Errorf("普修后耐久 = %d/%d，期望 970/970（上限磨损 900/30=30）", it.Dura, it.DuraMax)
	}
	// 价格 = 3000/3 * 900/1000 = 900
	if want := int64(100000 - 900); p.Char.Data.Gold != want {
		t.Errorf("普修后金币 = %d，期望 %d", p.Char.Data.Gold, want)
	}
}

// TestRepairSuperTierKeepsDuraMax 特修（标签 `@s_repair`）：价格 ×3，
// 修满且**不磨损上限**（ObjNpc.pas:2476-2480）。
func TestRepairSuperTierKeepsDuraMax(t *testing.T) {
	s, p, npc := repairTestServer(t, "@trading @buy @sell @repair @s_repair")
	p.dialog.label = "@s_repair"
	repairWeapon(s, p, npc)

	it := p.Char.Data.HumItems[proto.SlotWeapon]
	if it.DuraMax != 1000 || it.Dura != 1000 {
		t.Errorf("特修后耐久 = %d/%d，期望 1000/1000（不动上限）", it.Dura, it.DuraMax)
	}
	// 价格 = 3000*3/3 * 900/1000 = 2700
	if want := int64(100000 - 2700); p.Char.Data.Gold != want {
		t.Errorf("特修后金币 = %d，期望 %d", p.Char.Data.Gold, want)
	}
}

// TestRepairRejects 三条拒绝路径：脚本没声明 `@repair`、点的是特修但只声明了普修、
// **矿石（StdMode 43）不可修**。
func TestRepairRejects(t *testing.T) {
	// ① 脚本头没声明 @repair（只有买卖）
	s, p, npc := repairTestServer(t, "@trading @buy @sell")
	repairWeapon(s, p, npc)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 || p.Char.Data.Gold != 100000 {
		t.Errorf("脚本没声明 @repair 时不该修（Dura=%d 金币=%d）", it.Dura, p.Char.Data.Gold)
	}

	// ② 点的是特修，但脚本只声明了普修
	s, p, npc = repairTestServer(t, "@repair")
	p.dialog.label = "@s_repair"
	repairWeapon(s, p, npc)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 {
		t.Errorf("脚本没声明 @s_repair 时不该特修（Dura=%d）", it.Dura)
	}

	// ③ 矿石（StdMode 43）不可修：把手里那把的模板改成 43
	s, p, npc = repairTestServer(t, "@repair")
	s.data.tables.Items.Get(0).StdMode = 43
	repairWeapon(s, p, npc)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it.Dura != 100 || p.Char.Data.Gold != 100000 {
		t.Errorf("矿石（StdMode 43）不该能修（Dura=%d 金币=%d）", it.Dura, p.Char.Data.Gold)
	}
}

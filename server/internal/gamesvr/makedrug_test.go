package gamesvr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/wire"
)

// 官方 MakeItem.txt 的第一条配方（`[灰色药粉(少量)]`）：这三味材料 + 成品。
const (
	testDrugName  = "灰色药粉(少量)"
	testDrugMat1  = "食人树叶"
	testDrugMat2  = "毒蜘蛛牙齿"
	testDrugMat3  = "食人树的果实"
	testDrugPrice = 100
)

// makeDrugTestServer 造"药店老板（脚本声明了 @makedrug）+ 站在旁边的玩家 + 真配方表"。
//
// ⚠️ 配方表读的是**仓库里那份真的** `data/envir/MakeItem.txt`（数据驱动），
// 物品表里的材料名也用它里面的名字 —— 不手搓一个"只有测试认识"的替代品。
func makeDrugTestServer(t *testing.T) (*Server, *Player, *entity.Monster) {
	t.Helper()
	items := []*data.StdItem{
		wuItem(1, testDrugMat1, 0, data.MinMax{}),
		wuItem(2, testDrugMat2, 0, data.MinMax{}),
		wuItem(3, testDrugMat3, 0, data.MinMax{}),
		// 成品 StdMode=0，好让"药"字商人的商品表里有它（成品必须在商品表里）。
		// ⚠️ 商品过滤里有一条 `Price == 0 就不是商品` ⇒ 必须给它标价（真实物品表里有价）。
		wuItem(4, testDrugName, 0, data.MinMax{}),
	}
	items[3].Price = 200
	s, p := butchTestServer(t, items...)

	mk, err := data.LoadMakeItems("../../data/envir/MakeItem.txt")
	if err != nil {
		t.Fatalf("读官方配方表失败: %v", err)
	}
	if _, ok := mk.Get(testDrugName); !ok {
		t.Fatalf("官方配方表里没有 %q（数据变了？）", testDrugName)
	}
	s.data.makeItems = mk
	s.cfg.makeDrugPrice = testDrugPrice

	// 脚本：头行声明 @makedrug（原版的 m_boMakeDrug 就是按脚本头置位的）
	dir := t.TempDir()
	body := "(@trading @buy @sell @makedrug)\r\n\r\n[@main]\r\n欢迎光临\\ \\\r\n"
	if err := os.WriteFile(filepath.Join(dir, "9drug-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir
	def := &data.NPC{ID: "9drug", Name: "药店老板"}
	s.npc.defs = []*data.NPC{def}

	return finishDrugServer(t, s, p)
}

// finishDrugServer 补上"NPC + 对话上下文"（制药与状态位用例共用）。
func finishDrugServer(t *testing.T, s *Server, p *Player) (*Server, *Player, *entity.Monster) {
	t.Helper()
	npc := newTestMonster(77, "药店老板", 10)
	npc.IsNPC = true
	npc.ID = 77
	npc.SetPlace(p.Obj.MapRef(), 6, 5, npc.Facing())
	s.mu.Lock()
	s.world.monsters[npc.ID] = npc
	s.mu.Unlock()

	p.dialog = &dialog{npcID: npc.ID, scriptName: "9drug"}
	p.Char.Data.Gold = 1000
	return s, p, npc
}

// craft 让玩家发一条 CM_USERMAKEDRUGITEM。
func craft(s *Server, p *Player, npc *entity.Monster, name string) {
	s.handleMakeDrug(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_USERMAKEDRUGITEM, int32(npc.ID), 0, 0, 0),
		Body: name,
	})
}

// TestMakeDrugHappyPath 制一份 `灰色药粉(少量)`：三味材料各扣掉配方里的数量、
// 成品进背包、金币按 `MakeDurg=100` 扣。
func TestMakeDrugHappyPath(t *testing.T) {
	s, p, npc := makeDrugTestServer(t)
	// 材料比配方多放一件：多出来的那件**不该**被扣掉
	d := p.Char.Data
	d.BagItems[0] = pbUserItemForTest(1) // 食人树叶（配方要 4）
	d.BagItems[1] = pbUserItemForTest(1)
	d.BagItems[2] = pbUserItemForTest(1)
	d.BagItems[3] = pbUserItemForTest(1)
	d.BagItems[4] = pbUserItemForTest(1) // 第 5 片叶子：留着
	d.BagItems[5] = pbUserItemForTest(2) // 毒蜘蛛牙齿 ×2
	d.BagItems[6] = pbUserItemForTest(2)
	d.BagItems[7] = pbUserItemForTest(3) // 食人树的果实 ×1
	d.BagItems[8] = pbUserItemForTest(3) // 多的一颗：留着

	craft(s, p, npc, testDrugName)

	if got := countBagByName(s, d, testDrugName); got != 1 {
		t.Fatalf("成品该进背包 1 件，实际 %d", got)
	}
	if got := countBagByName(s, d, testDrugMat1); got != 1 {
		t.Errorf("配方要 4 片叶子、放了 5 片 ⇒ 该剩 1 片，实际 %d", got)
	}
	if got := countBagByName(s, d, testDrugMat2); got != 0 {
		t.Errorf("毒蜘蛛牙齿该全扣（配方要 2、放了 2），实际剩 %d", got)
	}
	if got := countBagByName(s, d, testDrugMat3); got != 1 {
		t.Errorf("果实该剩 1 颗（配方要 1、放了 2），实际 %d", got)
	}
	if d.Gold != 1000-testDrugPrice {
		t.Errorf("金币 = %d，期望 %d", d.Gold, 1000-testDrugPrice)
	}
}

// TestMakeDrugRejects 三条拒绝路径：材料不齐 / 金币不够 / 商品表里没有这个成品。
//
// ⚠️ 材料不齐这条**刻意**不动金币与材料（原版在凑齐之前只数不扣）。
func TestMakeDrugRejects(t *testing.T) {
	s, p, npc := makeDrugTestServer(t)
	d := p.Char.Data

	// ① 材料不齐（只有 1 片叶子）
	d.BagItems[0] = pbUserItemForTest(1)
	before := d.Gold
	craft(s, p, npc, testDrugName)
	if countBagByName(s, d, testDrugMat1) != 1 || d.Gold != before {
		t.Errorf("材料不齐时不该扣东西（叶子 %d、金币 %d）",
			countBagByName(s, d, testDrugMat1), d.Gold)
	}
	if countBagByName(s, d, testDrugName) != 0 {
		t.Error("材料不齐不该出成品")
	}

	// ② 材料齐了但金币不够
	d.BagItems[0] = pbUserItemForTest(1)
	d.BagItems[1] = pbUserItemForTest(1)
	d.BagItems[2] = pbUserItemForTest(1)
	d.BagItems[3] = pbUserItemForTest(1)
	d.BagItems[4] = pbUserItemForTest(2)
	d.BagItems[5] = pbUserItemForTest(2)
	d.BagItems[6] = pbUserItemForTest(3)
	d.Gold = testDrugPrice - 1
	craft(s, p, npc, testDrugName)
	if countBagByName(s, d, testDrugMat1) != 4 || countBagByName(s, d, testDrugName) != 0 {
		t.Error("金币不够时不该扣材料、不该出成品")
	}

	// ③ 商品表里没有这个成品（药店老板只卖 StdMode=0；这里点一个不存在的东西）
	d.Gold = 1000
	craft(s, p, npc, "不存在的药")
	if countBagByName(s, d, testDrugMat1) != 4 {
		t.Error("点了不存在的成品，材料不该动")
	}
}

// TestMakeDrugRequiresScriptTag 脚本头**没**声明 `@makedrug` 的 NPC 不能制药
// （原版查 `m_boMakeDrug`，它由脚本头置位）。
func TestMakeDrugRequiresScriptTag(t *testing.T) {
	s, p, npc := makeDrugTestServer(t)
	d := p.Char.Data
	d.BagItems[0] = pbUserItemForTest(1)
	d.BagItems[1] = pbUserItemForTest(1)
	d.BagItems[2] = pbUserItemForTest(1)
	d.BagItems[3] = pbUserItemForTest(1)
	d.BagItems[4] = pbUserItemForTest(2)
	d.BagItems[5] = pbUserItemForTest(2)
	d.BagItems[6] = pbUserItemForTest(3)

	// 换一份不带 @makedrug 的脚本，并清掉脚本缓存
	dir := t.TempDir()
	body := "(@trading @buy @sell)\r\n\r\n[@main]\r\n欢迎\\ \\\r\n"
	if err := os.WriteFile(filepath.Join(dir, "9drug-0.txt"), []byte(body), 0o600); err != nil {
		t.Fatalf("写脚本失败: %v", err)
	}
	s.npc.scriptDir = dir
	s.npc.mu.Lock()
	s.npc.scripts = map[string]*script.Script{}
	s.npc.mu.Unlock()

	craft(s, p, npc, testDrugName)
	if countBagByName(s, d, testDrugName) != 0 {
		t.Error("脚本头没声明 @makedrug 的 NPC 不该能制药")
	}
	if countBagByName(s, d, testDrugMat1) != 4 {
		t.Error("被拒时材料不该动")
	}
}

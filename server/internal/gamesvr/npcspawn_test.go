package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/world"
)

// TestNpcFacingFixed NPC 的朝向必须是**固定**的（原版默认 4 = 朝下/正面）。
//
// 为什么单列一条：`entity.NewMonster` 给的是 `rand.IntN(8)` 随机朝向，而 NPC 的图块
// 只有 60 帧、站立步长 10 ⇒ **只有 6 个朝向**；给到 6/7 时客户端会算到**下一个 NPC
// 的图块**（症状：这个 NPC 画成了旁边那个的样子，随机朝向时有 1/4 的概率）。
//
// 出处：原版 `ObjBase.pas:1210` 的默认 `m_btDirection := 4`；`merchant.txt` 那个
// "正面"列官方服务端**不读**（`TMerchant` 记录里没有朝向字段，只有卫兵
// `LocalDB.pas:239` 从配置读）。口径见 `docs/authority.md` §4.5。
//
// 怪物**不受影响**（随机没问题：它们会走动、会转向），所以只断言 NPC。
func TestNpcFacingFixed(t *testing.T) {
	s := testSlaveServer()
	m := world.Generate("0", 40, 40, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	s.world.maps = mm
	if s.world.index == nil {
		s.world.index = world.NewSpatialIndex(32)
	}
	s.npc.defs = []*data.NPC{
		{ID: "1Bme", Name: "屠夫", MapID: "0", X: 12, Y: 10, RaceImg: 4, IsMerchant: true},
		{ID: "2Bwe", Name: "布衣店", MapID: "0", X: 14, Y: 10, RaceImg: 1, IsMerchant: true},
	}
	s.spawnNPCs("0")

	n := 0
	for id, mo := range s.world.monsters {
		if !mo.IsNPC {
			continue
		}
		n++
		if _, _, _, dir := mo.Object.Place(); dir != 4 {
			t.Errorf("NPC %s（id %d）朝向 = %d，应为 4（固定朝下；随机朝向会让 6/7 串到下一个 NPC 的图块）",
				mo.Name, id, dir)
		}
	}
	if n != 2 {
		t.Fatalf("生成的 NPC 数 = %d，应为 2", n)
	}
}

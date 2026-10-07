package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
)

// TestSlaveUpKillCount 守住宠物升级阈值（原版 GetUpKillCount，ObjBase.pas:2269-2281）：
//
//	((宠物等级 * 16) - 宠物等级) + 100 + MonUpLvNeedKillCount[宝宝等级]
//	（宝宝等级 ≥ SLAVEMAXLEVEL-2 = 48 时最后一项取 0）
//
// 官方 !setup.txt：MonUpLvNeedKillBase=100、MonUpLvRate=16、
// MonUpLvNeedKillCount = 0,0,50,100,200,300,600,1200。
func TestSlaveUpKillCount(t *testing.T) {
	for _, c := range []struct {
		petLevel, expLevel, want int
	}{
		{1, 0, 115},   // 16-1+100+0
		{1, 1, 115},   // +0
		{1, 2, 165},   // +50
		{1, 3, 215},   // +100
		{3, 0, 145},   // 48-3+100+0（召唤骷髅常见等级）
		{10, 7, 1450}, // 160-10+100+1200
		{10, 47, 250}, // expLevel 47 < 48 ⇒ 仍查表，但表越界 ⇒ 0
		{10, 48, 250}, // expLevel ≥ 48 ⇒ tCount := 0
		{10, 99, 250}, // 同上（越界也取 0）
	} {
		if got := slaveUpKillCount(c.petLevel, c.expLevel); got != c.want {
			t.Errorf("宠物等级 %d / 宝宝等级 %d ⇒ 阈值 %d，期望 %d",
				c.petLevel, c.expLevel, got, c.want)
		}
	}
}

// TestGainSlaveExpLevelUp 守住升级规则：
//
//	累积击杀点数 > 阈值 ⇒ 扣掉阈值、宝宝等级 +1；
//	**上限是 `召唤等级*2+1`**（ObjBase.pas:2292）；到顶后点数照扣、等级不动。
func TestGainSlaveExpLevelUp(t *testing.T) {
	s := &Server{world: worldState{players: map[uint32]*Player{}}}
	mon := &entity.Monster{Object: &entity.Object{ID: 1}, Info: &data.MonsterInfo{Level: 3}}
	mon.SlaveMakeLevel, mon.SlaveExpLevel = 0, 0

	// 阈值 = 3*16-3+100+0 = 145
	if s.gainSlaveExp(mon, 100) {
		t.Error("100 < 145 不该升级")
	}
	if mon.SlaveKills() != 100 {
		t.Errorf("累加点数 = %d，期望 100", mon.SlaveKills())
	}
	// 再来 100 ⇒ 200 > 145 ⇒ 升级并扣掉 145
	if !s.gainSlaveExp(mon, 100) {
		t.Error("200 > 145 应升级")
	}
	if mon.SlaveExpLevel != 1 {
		t.Errorf("宝宝等级 = %d，期望 1", mon.SlaveExpLevel)
	}
	if mon.SlaveKills() != 55 {
		t.Errorf("升级后剩余点数 = %d，期望 55（200-145）", mon.SlaveKills())
	}

	// 上限：召唤等级 0 ⇒ max = 0*2+1 = 1 ⇒ 已到 1，再攒也不涨
	for i := 0; i < 100; i++ {
		s.gainSlaveExp(mon, 1000)
	}
	if mon.SlaveExpLevel != 1 {
		t.Errorf("已到上限（召唤等级 0 ⇒ 1），宝宝等级不该超过 1，实际 %d", mon.SlaveExpLevel)
	}

	// 召唤等级 3 ⇒ 上限 7，能一路升上去
	mon2 := &entity.Monster{Object: &entity.Object{ID: 2}, Info: &data.MonsterInfo{Level: 3}}
	mon2.SlaveMakeLevel, mon2.SlaveExpLevel = 3, 0
	for i := 0; i < 200 && mon2.SlaveExpLevel < 7; i++ {
		s.gainSlaveExp(mon2, 500)
	}
	if mon2.SlaveExpLevel != 7 {
		t.Errorf("召唤等级 3 的宠物应能升到 7，实际 %d", mon2.SlaveExpLevel)
	}
}

// TestSlaveColorOf 守住宠物名色表（官方 !setup.txt:118+，M2Share.pas:1643 同值）。
//
// 原版 `GetNamecolor`：`if m_btSlaveExpLevel < SLAVEMAXLEVEL then
// Result := SlaveColor[m_btSlaveExpLevel]`（ObjBase.pas:19077-19078）。
func TestSlaveColorOf(t *testing.T) {
	want := [9]uint8{0xFF, 0xFE, 0x93, 0x9A, 0xE5, 0xA8, 0xB4, 0xFC, 249}
	for i := 0; i < len(want); i++ {
		m := &entity.Monster{Object: &entity.Object{}}
		m.SlaveExpLevel = uint8(i)
		if got := slaveColorOf(m); got != want[i] {
			t.Errorf("宝宝等级 %d 的名色 = %d，期望 %d", i, got, want[i])
		}
	}
	// 越界（等级 ≥ 表长）⇒ 0（客户端默认色）
	out := &entity.Monster{Object: &entity.Object{}}
	out.SlaveExpLevel = 200
	if got := slaveColorOf(out); got != 0 {
		t.Errorf("越界名色 = %d，期望 0", got)
	}
	if got := slaveColorOf(nil); got != 0 {
		t.Errorf("nil 名色 = %d，期望 0", got)
	}
}

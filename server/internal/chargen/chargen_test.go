package chargen

import (
	"sync/atomic"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
)

// fakeItems 是个**假物品表**：`ItemSource` 定成接口就是为了这个 ——
// 建角那几条规矩不该逼着单测去加载整张 stditems.json。
type fakeItems map[string]*data.StdItem

func (f fakeItems) GetByName(name string) *data.StdItem { return f[name] }

func testItems() fakeItems {
	return fakeItems{
		"木剑":      {Index: 3, Name: "木剑", DuraMax: 20},
		"金创药(小量)": {Index: 5, Name: "金创药(小量)", DuraMax: 10},
	}
}

// 名字规则：至少 3 **字节**（汉字 1 个字就够），且不含空格与 /@?'。
//
// ⚠️ 这条是从 legacy 的 `validChrName` 搬过来的，**语义必须一模一样** ——
// 换了规矩就等于"老客户端能建的名字、新客户端建不了"。
func TestValidName(t *testing.T) {
	ok := []string{"abc", "勇士", "小法甲", "abc123"}
	for _, n := range ok {
		if !ValidName(n) {
			t.Errorf("%q 该算合法", n)
		}
	}
	bad := []string{"", "ab", "a", "a b", "a\tb", "a/b", "a@b", "a'b", "a?b"}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("%q 该算不合法", n)
		}
	}
}

// 组装：13 槽装备位、空槽占位、初始武器、5 瓶药、能力值、出生点。
func TestBuild(t *testing.T) {
	var seq atomic.Int64
	home := Home{Map: "0", X: 289, Y: 618}
	c := Build("tester", "小法", 1, 1, 2, home, testItems(), &seq)

	if c.Account != "tester" || c.Name != "小法" {
		t.Errorf("账号/名字没写对：%q %q", c.Account, c.Name)
	}
	if c.Job != 1 || c.Data.GetJob() != 1 {
		t.Errorf("职业没写对（结构体列与存档都要）：%d %d", c.Job, c.Data.GetJob())
	}
	if c.Data.GetSex() != 1 || c.Data.GetHair() != 2 {
		t.Errorf("性别/发型没写对：%d %d", c.Data.GetSex(), c.Data.GetHair())
	}

	// 装备位：**定长 13 槽**，空槽是 Index=0 的占位（不是缺项）
	hum := c.Data.GetHumItems()
	if len(hum) != EquipSlots {
		t.Fatalf("装备位该 %d 槽，实得 %d", EquipSlots, len(hum))
	}
	if got := hum[1]; got.GetIndex() != 3 || got.GetDura() != 20 || got.GetDuraMax() != 20 {
		t.Errorf("槽 1 该是木剑（Index=3，耐久 20/20），实得 %+v", got)
	}
	if got := hum[0].GetIndex(); got != 0 {
		t.Errorf("空槽该留 Index=0 的占位，实得 %d（槽位信息丢了会让穿装备错位）", got)
	}

	// 背包：5 瓶药，且实例号（MakeIndex）各不相同
	bag := c.Data.GetBagItems()
	if len(bag) != 5 {
		t.Fatalf("初始药水该 5 个，实得 %d", len(bag))
	}
	seen := map[int32]bool{}
	for _, it := range bag {
		if it.GetIndex() != 5 {
			t.Errorf("药水 Index 该是 5，实得 %d", it.GetIndex())
		}
		if seen[it.GetMakeIndex()] {
			t.Errorf("实例号重复：%d（两瓶药共用一个实例号会串）", it.GetMakeIndex())
		}
		seen[it.GetMakeIndex()] = true
	}
	// 武器的实例号也不能和药水撞
	if seen[hum[1].GetMakeIndex()] {
		t.Error("武器的实例号与药水撞了")
	}

	// 能力值：1 级，血蓝是**职业初值**（与 `entity.InitialHPMP` 同源）
	hp0, mp0 := InitialHPMP(1)
	ab := c.Data.GetAbil()
	if ab.GetLevel() != 1 || ab.GetHp() != hp0 || ab.GetMaxHp() != hp0 ||
		ab.GetMp() != mp0 || ab.GetMaxMp() != mp0 {
		t.Errorf("能力值不对：%+v（该是 1 级 hp=%d mp=%d）", ab, hp0, mp0)
	}
	if c.Level != 1 {
		t.Errorf("检索列 Level 该是 1，实得 %d（`ListByAccount` 的摘要要用它）", c.Level)
	}

	// 出生点：当前坐标与回城点都写上（原版两者相同）
	if c.Data.GetCurMap() != "0" || c.Data.GetHomeMap() != "0" {
		t.Errorf("出生地图没写：cur=%q home=%q", c.Data.GetCurMap(), c.Data.GetHomeMap())
	}
	if c.Data.GetCurX() != 289 || c.Data.GetCurY() != 618 ||
		c.Data.GetHomeX() != 289 || c.Data.GetHomeY() != 618 {
		t.Errorf("出生坐标没写对：cur=(%d,%d) home=(%d,%d)",
			c.Data.GetCurX(), c.Data.GetCurY(), c.Data.GetHomeX(), c.Data.GetHomeY())
	}
}

// 没有物品表也不能崩（`accountsvc` 起不来数据目录时的降级）。
func TestBuildWithoutItems(t *testing.T) {
	var seq atomic.Int64
	c := Build("a", "abc", 0, 0, 1, Home{Map: "0"}, nil, &seq)
	if len(c.Data.GetBagItems()) != 0 || c.Data.GetHumItems()[1].GetIndex() != 0 {
		t.Error("没有物品表时不该凭空造出物品")
	}
	if len(c.Data.GetHumItems()) != EquipSlots {
		t.Error("装备位仍该是定长 13 槽（占位不能省）")
	}
}

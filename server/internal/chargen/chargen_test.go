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
		// 原版出生自带的三件（见 `InitialItems` 的说明）
		"布衣(男)": {Index: 4, Name: "布衣(男)", DuraMax: 15},
		"布衣(女)": {Index: 5, Name: "布衣(女)", DuraMax: 15},
		"木剑":    {Index: 3, Name: "木剑", DuraMax: 20},
		"蜡烛":    {Index: 6, Name: "蜡烛", DuraMax: 5},
		// 不再发放，但留在表里：将来要加回初始药水时改一行就行
		"金创药(小量)": {Index: 7, Name: "金创药(小量)", DuraMax: 10},
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
	if got := hum[0]; got.GetIndex() != 5 || got.GetDura() != 15 {
		t.Errorf("槽 0 该是布衣(女)（Index=5 —— sex=1），实得 %+v", got)
	}
	if got := hum[2]; got.GetIndex() != 6 {
		t.Errorf("槽 2 该是蜡烛（Index=6，右手：M2Share.pas:3519），实得 %+v", got)
	}
	if got := hum[3].GetIndex(); got != 0 {
		t.Errorf("空槽该留 Index=0 的占位，实得 %d（槽位信息丢了会让穿装备错位）", got)
	}

	// 背包：原版出生**不带药**（`docs/use.md` 说的三件都在装备位上）
	if bag := c.Data.GetBagItems(); len(bag) != 0 {
		t.Errorf("初始背包该是空的，实得 %d 件", len(bag))
	}
	// 三件装备的实例号（MakeIndex）必须各不相同 —— 共用一个实例号会串
	seen := map[int32]bool{}
	for _, slot := range []int{0, 1, 2} {
		if seen[hum[slot].GetMakeIndex()] {
			t.Errorf("实例号重复：%d", hum[slot].GetMakeIndex())
		}
		seen[hum[slot].GetMakeIndex()] = true
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

// 出生点：**两个新手村随机二选一**（`docs/use.md`：银杏山谷(≈648,624) / 边界村(≈288,615)，
// 不分职业；本仓安全点表里是 `0 650 631` 与 `0 289 618`）。
func TestPickHome(t *testing.T) {
	villages := []Home{
		{Map: "0", X: 650, Y: 631}, // 银杏山谷
		{Map: "0", X: 289, Y: 618}, // 边界村
	}
	// 空候选 ⇒ 零值（调用方退化成"只给地图号"的兜底）
	if h := PickHome(nil); h.Map != "" || h.X != 0 || h.Y != 0 {
		t.Errorf("空候选该给零值，实得 %+v", h)
	}
	// **单个候选 = 确定**（调试与单测要钉死出生点时用它）
	for i := 0; i < 5; i++ {
		if h := PickHome(villages[:1]); h.X != 650 || h.Y != 631 {
			t.Fatalf("只有一个候选时必须确定，实得 %+v", h)
		}
	}
	// 两个候选 ⇒ 只能落在这两个村里，且足够多次里**两个都出现过**（证明真在随机）
	seen := map[uint32]int{}
	for i := 0; i < 200; i++ {
		h := PickHome(villages)
		in650 := h.X == 650 && h.Y == 631
		in289 := h.X == 289 && h.Y == 618
		if !in650 && !in289 {
			t.Fatalf("落到候选之外：%+v", h)
		}
		seen[h.X]++
	}
	if seen[650] == 0 || seen[289] == 0 {
		t.Errorf("200 次里两个村都该出现过（真随机），实得 %v", seen)
	}
}

// `-home-points` 的解析（**两条建角路共用同一份**）：分号分隔、容忍空白/多余分号、
// 坏输入**报错**而不是静默变成空表。
func TestParseHomePoints(t *testing.T) {
	got, err := ParseHomePoints("650,631; 289,618 ;", "0")
	if err != nil {
		t.Fatalf("该解析成功：%v", err)
	}
	if len(got) != 2 || got[0].X != 650 || got[0].Y != 631 || got[0].Map != "0" ||
		got[1].X != 289 || got[1].Y != 618 || got[1].Map != "0" {
		t.Fatalf("解析结果不对：%+v", got)
	}
	for _, bad := range []string{"", "   ", "650", "650;", "a,b", "650,-1"} {
		if _, err := ParseHomePoints(bad, "0"); err == nil {
			t.Errorf("坏输入 %q 该报错（不能静默变成空表）", bad)
		}
	}
}

// 默认的两个新手村：坐标要与 `docs/use.md` 的口径、以及 `StartPoint.txt` 里那两条对得上。
func TestDefaultHomePointsAreTheTwoVillages(t *testing.T) {
	pts := DefaultHomePoints("0")
	if len(pts) != 2 {
		t.Fatalf("该有 2 个新手村，实得 %d", len(pts))
	}
	if pts[0].X != 650 || pts[0].Y != 631 || pts[1].X != 289 || pts[1].Y != 618 {
		t.Errorf("两个新手村坐标不对：%+v（银杏山谷 650,631 / 边界村 289,618）", pts)
	}
}

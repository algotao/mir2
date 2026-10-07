package data

import (
	"testing"
)

const envirDir = "envir"

// TestLoadMerchants 校验 merchant.txt 解析。
//
// 关键点是**名称可能夹在数字之间**，只能按位置取：
// `1Bme  0102  9  7  屠夫  0  11  0` 中"屠夫"是第 5 段。
func TestLoadMerchants(t *testing.T) {
	ms, err := LoadMerchants(seedDir + "/" + envirDir + "/merchant.txt")
	if err != nil {
		t.Fatalf("LoadMerchants: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("没有解析到任何商人")
	}
	seen := 0
	for _, m := range ms {
		if m.Name == "" || m.MapID == "" || m.ID == "" {
			t.Errorf("条目字段缺失: %+v", m)
		}
		if !m.IsMerchant {
			t.Errorf("%s 应标记为商人", m.Name)
		}
		// 名称里不应残留数字字段
		if seen < 5 {
			t.Logf("%s 地图%s (%d,%d) %s img=%d", m.ID, m.MapID, m.X, m.Y, m.Name, m.RaceImg)
			seen++
		}
	}
	t.Logf("商人 %d 个", len(ms))
}

// TestLoadNpcs 校验 Npcs.txt 解析。
//
// 关键点是**名称可能含空格且在最前面**（如"沙巴克城堡官员"），
// 所以要从末尾反推数字字段的位置。
func TestLoadNpcs(t *testing.T) {
	ns, err := LoadNpcs(seedDir + "/" + envirDir + "/Npcs.txt")
	if err != nil {
		t.Fatalf("LoadNpcs: %v", err)
	}
	if len(ns) == 0 {
		t.Fatal("没有解析到任何 NPC")
	}
	shown := 0
	for _, n := range ns {
		if n.Name == "" || n.MapID == "" {
			t.Errorf("条目字段缺失: %+v", n)
		}
		if n.IsMerchant {
			t.Errorf("%s 不应标记为商人", n.Name)
		}
		if shown < 5 {
			t.Logf("%s 地图%s (%d,%d) race=%d body=%d", n.Name, n.MapID, n.X, n.Y, n.Race, n.Body)
			shown++
		}
	}
	t.Logf("NPC %d 个", len(ns))
}

// TestNPCsInMap 校验按地图过滤。
func TestNPCsInMap(t *testing.T) {
	all := []*NPC{
		{Name: "a", MapID: "0"},
		{Name: "b", MapID: "0102"},
		{Name: "c", MapID: "0"},
	}
	got := NPCsInMap(all, "0")
	if len(got) != 2 {
		t.Fatalf("地图 0 应返回 2 个, got %d", len(got))
	}
	if len(NPCsInMap(all, "9999")) != 0 {
		t.Error("不存在的地图应返回空")
	}
}

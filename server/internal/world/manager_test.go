package world

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
)

// readRepoMapInfo 用**生产同款解析器**读仓库内的 mapinfo.txt，返回图号列表。
func readRepoMapInfo(t *testing.T) []string {
	t.Helper()
	infos, err := data.LoadMapInfo(filepath.Join("..", "..", "data", "envir", "mapinfo.txt"))
	if err != nil {
		t.Skipf("跳过：仓库内 mapinfo.txt 不可用 (%v)", err)
	}
	ids := make([]string, 0, len(infos))
	for _, mi := range infos {
		ids = append(ids, mi.ID)
	}
	return ids
}

// TestMapFileCaseInsensitive 守住"mapinfo 的图号大小写与文件名不一致也能加载"。
//
// 官方数据里这条**真的会踩**：mapinfo.txt 写 `D421`，文件却是 `d421.map`，
// 329 条图号里有 35 条如此。原版服务端跑 Windows（文件系统大小写不敏感）
// 从不暴露，我们跑 Linux 精确拼 `<id>.map` 会直接报"地图不存在"。
func TestMapFileCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	// 磁盘上只放小写文件名。
	mp := Generate("d421", 8, 8, true)
	if err := os.WriteFile(filepath.Join(dir, "d421.map"), mp.Serialize(), 0o600); err != nil {
		t.Fatal(err)
	}

	mgr := NewMapManager(dir, 0)

	// 1) mapinfo 里的大小写（大写）必须能取到。
	got, err := mgr.Get("D421")
	if err != nil {
		t.Fatalf("Get(\"D421\") 失败：%v（应回退到 d421.map）", err)
	}
	if got.Width() != 8 || got.Height() != 8 {
		t.Errorf("尺寸 = %dx%d，期望 8x8", got.Width(), got.Height())
	}
	// 2) 精确名也要能取到（缓存里按 id 存，两个 id 各一份，不是错）。
	if _, err := mgr.Get("d421"); err != nil {
		t.Errorf("Get(\"d421\") 失败：%v", err)
	}
	// 3) 真的不存在时仍要报错，不能静默返回空图。
	if _, err := mgr.Get("nosuchmap"); err == nil {
		t.Error("不存在的图号应返回错误（不要把玩家传进假图）")
	}
}

// TestVendoredMapsCoverMapInfo 校验仓库内 vendored 的地图**覆盖 mapinfo 的每一条**。
//
// 这条是"静态数据自包含"的守卫：地图搬进 data/map 之后，任何一条 mapinfo
// 图号在磁盘上找不到对应文件，都说明 vendoring 漏了文件（而不是运行期才发现
// 玩家被传送到一张不存在的图）。
//
// 大小写按 MapManager 的规则宽松匹配（见上一条测试）。
func TestVendoredMapsCoverMapInfo(t *testing.T) {
	dir := filepath.Join("..", "..", "data", "map")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("跳过：仓库内地图数据不可用 (%v)", err)
	}
	byLower := make(map[string]string, len(ents))
	exact := make(map[string]bool, len(ents))
	for _, e := range ents {
		if e.IsDir() || !isMapExt(e.Name()) {
			continue
		}
		n := e.Name()
		exact[n] = true
		byLower[lowerName(n)] = n
	}

	infos := readRepoMapInfo(t)
	if len(infos) == 0 {
		t.Fatal("mapinfo.txt 没解析出任何地图")
	}
	missing := 0
	for _, mi := range infos {
		if exact[mi+".map"] || exact[mi+".MAP"] {
			continue
		}
		if _, ok := byLower[lowerName(mi+".map")]; ok {
			continue
		}
		missing++
		t.Errorf("mapinfo 声明了地图 %s，但 data/map 里没有对应文件", mi)
	}
	t.Logf("mapinfo %d 条，全部在 data/map 中找到对应文件（缺失 %d）", len(infos), missing)
}

// isMapExt 判断扩展名是否为 .map（不区分大小写）。
func isMapExt(name string) bool {
	return lowerName(filepath.Ext(name)) == ".map"
}

// lowerName 是 ASCII 小写化；地图号与文件名都是 ASCII，不用引入 unicode 依赖。
func lowerName(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// TestVendoredFightZoneMapsLoad 守住"e2e 真的会走进去的那几张 FIGHT/FIGHT3 图"。
//
// 死亡掉落豁免（pvp.ZoneMap.SuppressDeathDrop）只在**地图带 FIGHT/FIGHT3 标记**
// 时生效，而 e2e 的 fightdrop 用例靠 `@map F006` 进去。这条同时锚住三件事：
//
//  1. 官方 mapinfo 里 F001-F010 带 FIGHT3、SD000-SD002 带 FIGHT
//     （换了数据包要有人喊，否则豁免分支会静默失效）；
//  2. 这些图在 data/map 里真的有文件（否则 @map 失败、用例白跑）；
//  3. MapManager 能加载它们（等价于游戏里真的进得去）。
func TestVendoredFightZoneMapsLoad(t *testing.T) {
	mapDir := filepath.Join("..", "..", "data", "map")
	if _, err := os.Stat(mapDir); err != nil {
		t.Skipf("跳过：仓库内地图数据不可用 (%v)", err)
	}
	infos, err := data.LoadMapInfo(filepath.Join("..", "..", "data", "envir", "mapinfo.txt"))
	if err != nil {
		t.Skipf("跳过：仓库内 mapinfo.txt 不可用 (%v)", err)
	}
	byID := make(map[string]*data.MapInfo, len(infos))
	for _, mi := range infos {
		byID[mi.ID] = mi
	}
	mgr := NewMapManager(mapDir, 0)

	check := func(id string, wantFight3 bool) {
		t.Helper()
		mi := byID[id]
		if mi == nil {
			t.Errorf("官方 mapinfo 里没有 %s", id)
			return
		}
		if wantFight3 && !mi.Fight3Zone {
			t.Errorf("%s 应带 FIGHT3 标记（死亡不掉落靠它生效）", id)
		}
		if !wantFight3 && !mi.FightZone {
			t.Errorf("%s 应带 FIGHT 标记（死亡不掉落靠它生效）", id)
		}
		if _, err := mgr.Get(id); err != nil {
			t.Errorf("%s 加载失败：%v（e2e 的 @map %s 会失败）", id, err, id)
		}
	}
	// FIGHT3：行会战争地图（e2e 的 fightdrop 用例走 F006）
	for i := 1; i <= 10; i++ {
		check(fmt.Sprintf("F%03d", i), true)
	}
	// FIGHT：PK 区（小黑屋）
	for _, id := range []string{"SD000", "SD001", "SD002"} {
		check(id, false)
	}
}

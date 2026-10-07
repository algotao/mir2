package gamesvr

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

func TestLoadMonGenAllMapsAndReportsMissingTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mongen.txt")
	content := "A 1 2 known 3 4 5\nB 6 7 missing 1 1 2\nmalformed\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	monsters, err := data.NewMonsterSet([]*data.MonsterInfo{{Index: 1, Name: "known"}})
	if err != nil {
		t.Fatal(err)
	}
	spawns, issues, err := loadMonGen(path, &data.Tables{Monsters: monsters}, 0)
	if err != nil {
		t.Fatalf("loadMonGen: %v", err)
	}
	if len(spawns) != 1 || spawns[0].MapName != "A" || spawns[0].MonsterName != "known" {
		t.Fatalf("刷怪点 = %+v，期望保留所有地图上有模板的条目", spawns)
	}
	if len(issues) != 2 || !strings.Contains(issues[0], `缺少怪物模板 "missing"`) ||
		!strings.Contains(issues[1], "解析失败") {
		t.Fatalf("MonGen 诊断 = %+v", issues)
	}
}

func TestPreloadAllMapsCaseInsensitiveAndWarnsMissing(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.MAP", "b.map"} {
		if err := os.WriteFile(filepath.Join(dir, name), tinyMapData(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids, issues, err := preloadAllMaps(world.NewMapManager(dir, 0), dir, []string{"A", "missing"})
	if err != nil {
		t.Fatalf("preloadAllMaps: %v", err)
	}
	if len(ids) != 2 || ids[0] != "A" || ids[1] != "b" {
		t.Fatalf("预加载地图 = %v", ids)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "地图 missing 加载失败") {
		t.Fatalf("地图诊断 = %v", issues)
	}
}

func tinyMapData() []byte {
	b := make([]byte, world.HeaderSize+world.CellSize)
	binary.LittleEndian.PutUint16(b[0:2], 1)
	binary.LittleEndian.PutUint16(b[2:4], 1)
	return b
}

func TestSpawnMapsActivateLazily(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"A", "B"} {
		if err := os.WriteFile(filepath.Join(dir, id+".map"), tinyMapData(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	monsters, err := data.NewMonsterSet([]*data.MonsterInfo{{Index: 1, Name: "mob", HP: 10}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		data: dataState{tables: &data.Tables{Monsters: monsters}},
		world: worldState{
			maps:       world.NewMapManager(dir, 0),
			monsters:   make(map[uint32]*entity.Monster),
			monsterIdx: world.NewSpatialIndex(32),
			index:      world.NewSpatialIndex(32),
			spawns: []entity.SpawnPoint{
				{MapName: "A", X: 0, Y: 0, MonsterName: "mob", Count: 1},
				{MapName: "B", X: 0, Y: 0, MonsterName: "mob", Count: 1},
			},
			activeSpawnMaps: map[string]bool{"A": true},
		},
	}

	s.refillSpawns()
	if got := len(s.world.monsters); got != 1 {
		t.Fatalf("启动地图激活时怪物数=%d，期望仅 A 图 1 只", got)
	}
	s.activateSpawnMap("B")
	if got := len(s.world.monsters); got != 2 {
		t.Fatalf("首次激活 B 图后怪物数=%d，期望 A/B 各 1 只", got)
	}
	for _, m := range s.world.monsters {
		if m.MapRef() == nil || (m.MapRef().Name != "A" && m.MapRef().Name != "B") {
			t.Fatalf("怪物错误地图: %+v", m)
		}
	}
}

func TestCanonicalMapName(t *testing.T) {
	got := canonicalMapName("d421", map[string]string{"d421": "D421"})
	if got != "D421" {
		t.Fatalf("canonicalMapName = %q", got)
	}
}

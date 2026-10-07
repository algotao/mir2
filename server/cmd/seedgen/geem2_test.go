package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGeeM2Monsters(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "data", "seed", "GEEM2.db.sql"))
	if err != nil {
		t.Fatal(err)
	}
	monsters, err := ParseGeeM2Monsters(string(raw))
	if err != nil {
		t.Fatalf("ParseGeeM2Monsters: %v", err)
	}
	if len(monsters) != 378 {
		t.Fatalf("解析到 %d 个怪物模板，期望完整 GeeM2 社区表 378 条", len(monsters))
	}
	chicken := monsters[0]
	if chicken.Name != "鸡" || chicken.Race != 51 || chicken.RaceImg != 11 ||
		chicken.Appr != 160 || chicken.Level != 2 || chicken.HP != 5 {
		t.Fatalf("GeeM2 怪物字段映射错误：%+v", chicken)
	}
	for i, m := range monsters {
		if m.Index != int32(i+1) {
			t.Fatalf("怪物[%d] Index=%d，期望 %d", i, m.Index, i+1)
		}
	}
}

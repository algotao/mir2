package castle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// geeM2CastleDir 是 1.76 官方配置里的城堡目录。
//
// 数据已随仓库分发（data/castle），用包目录相对路径。
var geeM2CastleDir = filepath.Join("..", "..", "data", "castle")

// findUnit 在单位列表里按种类+序号查找。
func findUnit(list []storage.CastleUnit, kind storage.CastleUnitKind, idx int) *storage.CastleUnit {
	for i := range list {
		if list[i].Kind == kind && list[i].Index == idx {
			return &list[i]
		}
	}
	return nil
}

// TestLoadOfficialSabukConfig 解析真实的官方 SabukW.txt。
//
// 该文件是 **GBK** 编码且键名里混着中文（"弓箭卫士_1_X"），
// 是本项目里唯一需要转码的配置，单独用真实文件守住它。
//
// 数据源不在时跳过（外部数据不随仓库走，见 docs/data-sources.md）。
func TestLoadOfficialSabukConfig(t *testing.T) {
	if _, err := os.Stat(filepath.Join(geeM2CastleDir, DefaultConfigDir, "SabukW.txt")); err != nil {
		t.Skipf("官方城堡配置不可用（%v），跳过", err)
	}
	rec, err := LoadSabukConfig(geeM2CastleDir, DefaultConfigDir)
	if err != nil {
		t.Fatalf("LoadSabukConfig: %v", err)
	}

	if rec.Name != "沙巴克" {
		t.Errorf("CastleName = %q, 期望 沙巴克（GBK 解码）", rec.Name)
	}
	if rec.OwnGuild != "" {
		t.Errorf("官方存档 OwnGuild 应为空，实际 %q", rec.OwnGuild)
	}
	// 这些值来自 [defense] 段，不是出厂默认值——能证明键解析对了。
	if rec.MapName != "3" {
		t.Errorf("CastleMap = %q, 期望 3", rec.MapName)
	}
	if rec.PalaceMap != "0150" || rec.SecretMap != "D701" {
		t.Errorf("皇宫/密道 = %q/%q, 期望 0150/D701", rec.PalaceMap, rec.SecretMap)
	}
	if rec.HomeX != 644 || rec.HomeY != 290 {
		t.Errorf("回城点 = (%d,%d), 期望 (644,290)", rec.HomeX, rec.HomeY)
	}

	// 城门：MainDoor* 键。
	door := findUnit(rec.Units, storage.CastleMainDoor, 0)
	if door == nil {
		t.Fatal("未解析出城门")
	}
	if door.Name != "SabukDoor" || door.X != 672 || door.Y != 330 || door.HP != 10000 {
		t.Errorf("城门 = %+v, 期望 SabukDoor(672,330) HP 10000", *door)
	}
	if !door.Opened {
		t.Error("MainDoorOpen 首次出现为 1，城门应为打开状态（重复键取首次）")
	}

	// 三面墙：LeftWall/CenterWall/RightWall 键。
	for i, want := range []struct {
		name string
		x, y int
		hp   int
	}{
		{"SabukW1", 624, 278, 5000},
		{"SabukW2", 627, 278, 5000},
		{"SabukW3", 634, 271, 5000},
	} {
		w := findUnit(rec.Units, storage.CastleWall, i)
		if w == nil {
			t.Fatalf("未解析出第 %d 面墙", i)
		}
		if w.Name != want.name || w.X != want.x || w.Y != want.y || w.HP != want.hp {
			t.Errorf("墙%d = %+v, 期望 %s(%d,%d) HP %d",
				i, *w, want.name, want.x, want.y, want.hp)
		}
	}

	// 弓箭手：坐标取自中文键 "弓箭卫士_N_X/Y"，名字取自 Archer_N_Name。
	a := findUnit(rec.Units, storage.CastleArcher, 0)
	if a == nil {
		t.Fatal("未解析出弓箭手 0")
	}
	if a.X != 662 || a.Y != 333 {
		t.Errorf("弓箭手 0 坐标 = (%d,%d), 期望 (662,333)（中文键 弓箭卫士_1_*）", a.X, a.Y)
	}
	if a.Name != "弓箭手" {
		t.Errorf("弓箭手 0 名字 = %q, 期望 弓箭手", a.Name)
	}
	if a.HP != 0 {
		t.Errorf("未雇佣的弓箭手 HP 应为 0，实际 %d", a.HP)
	}

	// 宣战队列：官方存档是空文件。
	if withWar := LoadAttackSabukWall(geeM2CastleDir, DefaultConfigDir, rec); len(withWar.Attackers) != 0 {
		t.Errorf("官方存档的宣战队列应为空，实际 %+v", withWar.Attackers)
	}
}

// TestParseINIAndTime 守住 INI 解析与宽松时间解析。
func TestParseINIAndTime(t *testing.T) {
	kv := parseINI("[setup]\r\n; 注释\r\n\r\nA=1\r\nB = 2 \r\n无值\r\n[defense]\r\nC=3\r\n")
	if kv["A"] != "1" {
		t.Errorf("A = %q, 期望 1", kv["A"])
	}
	if kv["B"] != "2" {
		t.Errorf("B = %q, 期望 2（应去空格）", kv["B"])
	}
	if kv["C"] != "3" {
		t.Errorf("C = %q, 期望 3（段名应被忽略）", kv["C"])
	}
	if _, ok := kv["无值"]; ok {
		t.Error("没有 '=' 的行不该进表")
	}

	// 官方写法 "2025-1-19 15:38:28"。
	if got := parseLooseTime("2025-1-19 15:38:28", time.Time{}); got.Year() != 2025 || got.Month() != 1 || got.Day() != 19 {
		t.Errorf("解析 2025-1-19 = %v", got)
	}
	// Delphi TDateTime：1899-12-30 起算的浮点天数。
	if got := parseLooseTime("0", time.Time{}); !got.Equal(time.Time{}) {
		t.Errorf("TDateTime 0 = %v, 期望零值", got)
	}
	if got := parseLooseTime("", time.Time{}); !got.Equal(time.Time{}) {
		t.Errorf("空串应回退默认值，实际 %v", got)
	}
}

// TestDecodeGBK 守住转码：GBK 要转、UTF-8 原样透传。
func TestDecodeGBK(t *testing.T) {
	// "沙巴克" 的 GBK 字节，取自官方 SabukW.txt 里 CastleName= 后面那串。
	gbk := []byte{0xC9, 0xB3, 0xB0, 0xCD, 0xBF, 0xCB}
	got, err := decodeGBK(gbk)
	if err != nil {
		t.Fatalf("decodeGBK: %v", err)
	}
	if got != "沙巴克" {
		t.Errorf("GBK 解码 = %q, 期望 沙巴克", got)
	}
	// 已经是 UTF-8 的必须原样返回（Envir 下的脚本就是 UTF-8）。
	if got, err := decodeGBK([]byte("沙巴克")); err != nil || got != "沙巴克" {
		t.Errorf("UTF-8 透传 = %q, %v", got, err)
	}
}

// TestParseINIKeepsFirstOccurrence 守住 TStringList.GetValue 语义。
//
// 官方 SabukW.txt 里 MainDoorOpen / MainDoorHP 各出现两次
// （先 1/10000，后 0/0）。原版 Delphi 用 TIniFile 读，从索引 0 找、
// 命中第一个即返回，所以读到的是 10000；按"后者覆盖"会读成城门已破。
func TestParseINIKeepsFirstOccurrence(t *testing.T) {
	kv := parseINI("MainDoorHP=10000\nMainDoorOpen=1\nMainDoorOpen=0\nMainDoorHP=0\n")
	if kv["MainDoorHP"] != "10000" {
		t.Errorf("MainDoorHP = %q, 期望 10000（首次优先）", kv["MainDoorHP"])
	}
	if kv["MainDoorOpen"] != "1" {
		t.Errorf("MainDoorOpen = %q, 期望 1（首次优先）", kv["MainDoorOpen"])
	}
}

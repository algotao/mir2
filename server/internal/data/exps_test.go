package data

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseLevelKey 守住"Level 之后必须全是数字"。
//
// [Exp] 段里混着 `LevelExpRate999=0`，naive 的 `strings.HasPrefix(key, "Level")`
// 会把 999 当成等级键、把 0 当成"999 级所需经验"。
func TestParseLevelKey(t *testing.T) {
	cases := []struct {
		key   string
		want  int
		valid bool
	}{
		{"Level1", 1, true},
		{"level100", 100, true}, // 段/键大小写不敏感（Delphi TStringList）
		{"LevelExpRate999", 0, false},
		{"Level", 0, false},
		{"Level0", 0, false}, // 等级从 1 起
		{"LevelX", 0, false},
		{"HighLevel", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parseLevelKey(c.key)
		if ok != c.valid || (ok && got != c.want) {
			t.Errorf("parseLevelKey(%q) = (%d, %v)，期望 (%d, %v)", c.key, got, ok, c.want, c.valid)
		}
	}
}

// TestLoadExpsOnlyExpSection 守住"只取 [Exp] 段"。
//
// 官方 Exps.ini 里另有 [HeroExp]/[GamePetExp]/[MedicineExp]/[WineExp]
// 四个段（英雄/游戏宠物/药水/酒），它们的 Level1 各不相同。取错段会让
// 玩家经验直接跑偏（[WineExp] 的 Level1=3333000）。
func TestLoadExpsOnlyExpSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Exps.ini")
	// 结构照抄官方文件：先 [Exp]，紧接着别的段，[Exp] 尾部还有 LevelExpRate 干扰键。
	body := "[Exp]\r\n" +
		"HighLevel=1000\r\n" +
		"HighLevelKillMonFixExp=1\r\n" +
		"Level1=100\r\n" +
		"Level2=200\r\n" +
		"Level3=300\r\n" +
		"LevelExpRate999=0\r\n" +
		"LevelExpRate1000=0\r\n" +
		"\r\n" +
		"[HeroExp]\r\n" +
		"Level1=999999\r\n" +
		"[WineExp]\r\n" +
		"Level1=3333000\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	need, err := LoadExps(path)
	if err != nil {
		t.Fatalf("LoadExps: %v", err)
	}
	if len(need) != 4 { // 下标 0..3
		t.Fatalf("表长 = %d，期望 4（1-based，[0] 未用）", len(need))
	}
	if need[1] != 100 || need[2] != 200 || need[3] != 300 {
		t.Errorf("Level1..3 = %d/%d/%d，期望 100/200/300（不能取到 HeroExp/WineExp）",
			need[1], need[2], need[3])
	}
}

// TestLoadExpsMissingSection 段缺失要报错（调用方据此退回公式）。
func TestLoadExpsMissingSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Exps.ini")
	if err := os.WriteFile(path, []byte("[Setup]\r\nIncAlcoholTime=30\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExps(path); err == nil {
		t.Error("没有 [Exp] 段时应返回错误")
	}
}

// officialExps 是官方 Exps.ini 在仓库内的位置（包目录相对）。
var officialExps = filepath.Join("..", "..", "data", "envir", "Exps.ini")

// TestLoadOfficialExps 用仓库内的官方文件守住解析（数据源不在时跳过）。
func TestLoadOfficialExps(t *testing.T) {
	if _, err := os.Stat(officialExps); err != nil {
		t.Skipf("官方 Exps.ini 不可用（%v），跳过", err)
	}
	need, err := LoadExps(officialExps)
	if err != nil {
		t.Fatalf("LoadExps(%s): %v", officialExps, err)
	}
	if len(need) < 101 {
		t.Fatalf("表长 = %d，官方 [Exp] 段应有 Level1..Level1000", len(need))
	}
	// 官方头几行：Level1=100、Level2=200、Level3=300、Level4=400、Level5=600。
	for lv, want := range map[int]uint64{1: 100, 2: 200, 3: 300, 4: 400, 5: 600} {
		if need[lv] != want {
			t.Errorf("Level%d = %d，期望 %d", lv, need[lv], want)
		}
	}
}

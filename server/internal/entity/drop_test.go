package entity

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDropLine 校验掉落行解析（含 1-based 转换与金币行）。
func TestParseDropLine(t *testing.T) {
	cases := []struct {
		line string
		want DropItem
	}{
		{`1/1 鸡肉`, DropItem{0, 1, "鸡肉", 1}},
		{`1/2 金币 100`, DropItem{0, 2, "金币", 100}},
		{`1/10000 金币 1000`, DropItem{0, 10000, "金币", 1000}},
		{`1/800 攻击神水`, DropItem{0, 800, "攻击神水", 1}},
		{`31/60 "太阳水" 2`, DropItem{30, 60, "太阳水", 2}},
		{`1/10 木剑`, DropItem{0, 10, "木剑", 1}}, // Count 缺省为 1
	}
	for _, c := range cases {
		got, err := ParseDropLine(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if got != c.want {
			t.Errorf("%q → %+v, want %+v", c.line, got, c.want)
		}
	}

	bad := []string{"; 注释", "", "1/30", "0/10 物品", "50/10 物品", "1/0 物品"}
	for _, b := range bad {
		if _, err := ParseDropLine(b); err == nil {
			t.Errorf("%q 应解析失败", b)
		}
	}
}

// TestMatch 校验单条判定：Random(MaxPoint) <= SelPoint。
func TestMatch(t *testing.T) {
	d := DropItem{SelPoint: 0, MaxPoint: 800} // 配置写 "1/800"
	if !d.Match(0) {
		t.Error("roll=0 应命中（1/800 的那一次）")
	}
	if d.Match(1) || d.Match(799) {
		t.Error("roll>0 不应命中")
	}
	if d.ChancePermille() != 1 { // 1/800 ≈ 1.25‰ → 取整 1
		t.Errorf("ChancePermille = %d", d.ChancePermille())
	}

	d2 := DropItem{SelPoint: 0, MaxPoint: 2} // "1/2" = 50%
	if d2.ChancePermille() != 500 {
		t.Errorf("1/2 的概率应为 500‰, got %d", d2.ChancePermille())
	}
}

// TestRollMultipleHits 校验一次可以命中**多条**（原版语义）。
//
// 这是最容易搞错的地方：早期实现当成"区间命中一条"，
// 会显著改变爆率。
func TestRollMultipleHits(t *testing.T) {
	tbl := NewDropTable([]DropItem{
		{0, 1, "鸡肉", 1},    // 必掉
		{0, 1, "金币", 100},  // 必掉
		{0, 1000, "屠龙", 1}, // 千分之一
	})

	// 全部 roll=0 → 三条全中
	got := tbl.Roll([]int{0, 0, 0})
	if len(got) != 3 {
		t.Fatalf("应命中 3 条, got %d", len(got))
	}
	// 前两条 roll=0，第三条 roll=5 → 只中两条
	got = tbl.Roll([]int{0, 0, 5})
	if len(got) != 2 {
		t.Fatalf("应命中 2 条, got %d", len(got))
	}
	// 长度不匹配返回 nil（防御）
	if got := tbl.Roll([]int{0}); got != nil {
		t.Errorf("随机数个数不符应返回 nil, got %v", got)
	}
}

// TestRollDistribution 校验实测频率接近配置概率。
func TestRollDistribution(t *testing.T) {
	tbl := NewDropTable([]DropItem{{0, 10, "太阳水", 1}}) // 1/10
	const n = 20000
	hit := 0
	for i := 0; i < n; i++ {
		if len(tbl.Roll([]int{rand.IntN(10)})) == 1 {
			hit++
		}
	}
	got := hit * 1000 / n
	if got < 70 || got > 130 { // 期望 100‰，留 ±30% 容差
		t.Errorf("实测 %d‰，期望约 100‰", got)
	}
}

func TestDropTableEmpty(t *testing.T) {
	if NewDropTable(nil) != nil {
		t.Error("空表应返回 nil")
	}
	var tbl *DropTable
	if tbl.Len() != 0 || tbl.Items() != nil {
		t.Error("nil 表应安全返回空（不应 panic）")
	}
	if tbl.Roll(nil) != nil {
		t.Error("nil 表 Roll 应返回 nil")
	}
}

// TestGoldAmount 校验金币金额 = Count/2 + Random(Count)。
func TestGoldAmount(t *testing.T) {
	d := DropItem{ItemName: GoldName, Count: 100}
	// randInt 返回 0 → 50；返回 99 → 149
	if got := d.GoldAmount(func(int) int { return 0 }); got != 50 {
		t.Errorf("randInt=0 → %d, want 50", got)
	}
	if got := d.GoldAmount(func(int) int { return 99 }); got != 149 {
		t.Errorf("randInt=99 → %d, want 149", got)
	}
	if !d.IsGold() {
		t.Error("应识别为金币")
	}
}

func TestLoadMonItemsReportsMissingData(t *testing.T) {
	dir := t.TempDir()
	content := "1/10 已知物品\ninvalid row\n1/20 未知物品\n"
	if err := os.WriteFile(filepath.Join(dir, "样例.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := LoadMonItems(dir, func(name string) bool { return name == "已知物品" })
	if err != nil {
		t.Fatalf("LoadMonItems: %v", err)
	}
	stats := items.Stats()
	if stats.Files != 1 || stats.Rules != 2 || stats.BadLines != 1 || stats.UnknownItems != 1 {
		t.Fatalf("统计 = %+v", stats)
	}
	warnings := strings.Join(items.Warnings(), "\n")
	if !strings.Contains(warnings, "样例.txt:2") || !strings.Contains(warnings, `不存在的物品 "未知物品"`) {
		t.Fatalf("WARN 诊断缺项：%s", warnings)
	}
}

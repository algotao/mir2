package gamesvr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/script"
)

// knownBadGoods 是**已知**对不上物品表的货架名字（数据错，不是代码错）。
//
// 为什么留着白名单而不是直接删：删掉就变成"没人知道这里少了几件"；
// 列出来至少让**新**的错名字立刻变红（`makeItem` 那条 lint 的同款思路）。
// 修好一条就从这里删一条 —— 现在的账：
//
//	商品         6 个 8 类/彩票/杂货脚本的占位名（原版脚本自己没填）
//	地牢逃脱卷书 / 随机传送卷书 / 行会回城卷书   全在 7Ast-3，物品表里只有「地牢逃脱卷」「随机传送卷」
var knownBadGoods = map[string]bool{
	"商品":     true,
	"地牢逃脱卷书": true,
	"随机传送卷书": true,
	"行会回城卷书": true,
}

// TestMarketDefGoodsResolve 是**货架数据**的 lint：`market_def/*.txt` 的 `[goods]` 里
// 每个名字都必须在物品表里查得到、且标了价 —— 否则那件商品会被**静默跳过**，
// 玩家只看见"店里少了几样"，日志里什么都没有。
//
// 2026-10-17 审计发现 11 个脚本有这个毛病（其中 `1Bme-0102` 的"干肉产品"、
// 三个脚本的"随即传送卷"已经修掉）；这条把"别再新增"钉住。
func TestMarketDefGoodsResolve(t *testing.T) {
	tb, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据: %v", err)
	}
	dir := filepath.Join("..", "..", "data", "envir", "market_def")
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读脚本目录: %v", err)
	}

	known, parsed, skipped := 0, 0, 0
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".txt") {
			continue
		}
		sc, err := script.ParseFile(filepath.Join(dir, f.Name()))
		if err != nil {
			// 目录里有十几个辅助脚本（会员备案、任务表…）只有注释或 `[goods]`、没有对话段，
			// `Parse` 会以"没有任何段"拒绝 ⇒ 它们本来就没有货架可查，跳过。
			skipped++
			continue
		}
		if len(sc.Goods) == 0 {
			continue
		}
		parsed++
		for _, g := range sc.Goods {
			if it := tb.Items.GetByName(g.Name); it != nil && it.Price > 0 {
				continue
			}
			if knownBadGoods[g.Name] {
				known++
				continue
			}
			t.Errorf("%s：[goods] 里的 %q 在物品表里解析不到（或 Price=0）⇒ 这件商品会被静默跳过",
				f.Name(), g.Name)
		}
	}
	if parsed == 0 {
		t.Fatal("一个带 [goods] 的脚本都没读到 —— 数据路径变了？")
	}
	t.Logf("扫了 %d 个带 [goods] 的脚本（跳过 %d 个没有对话段的辅助脚本）；"+
		"已知坏名字命中 %d 次（修好一条就从 knownBadGoods 删一条）", parsed, skipped, known)
}

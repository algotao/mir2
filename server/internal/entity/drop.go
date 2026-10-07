package entity

import (
	"fmt"
	"strconv"
	"strings"
)

// 掉落表。
//
// 对应原版 Envir\MonItems\<怪物名>.txt 的一行
// （LocalDB.pas:1377-1428 LoadMonitems）：
//
//	<SelPoint>/<MaxPoint> <物品名> [Count]
//
// 分隔符是空格 / '/' / TAB；物品名可用引号包裹（含空格时必需）。
//
// # 判定方式（★ 最容易搞错的地方）
//
// 原版是**逐条独立判定**，不是"区间命中一条"（UsrEngn.pas:1561）：
//
//	for i := 0 to ItemList.Count-1 do
//	    if Random(MonItem.MaxPoint) <= MonItem.SelPoint then 掉落该条
//
// 因此：
//   - 一次击杀可以掉出**多件**物品；
//   - 概率 = (SelPoint+1) / MaxPoint —— 因为读入时 SelPoint 已被减 1
//     （LocalDB.pas:1415 `MonItem.SelPoint := n18 - 1`）。
//     写成 `1/800` 就是 1/800 的概率，符合配置文件里 1/N 的写法。
//
// 真实 1.76 配置里 SelPoint 几乎恒为 1（即减 1 后为 0），
// 此时条件退化为 Random(MaxPoint) == 0。
type DropItem struct {
	// SelPoint 是命中上限（含）。文件里的值已减 1。
	SelPoint int
	// MaxPoint 是随机上界（不含）。
	MaxPoint int
	// ItemName 是物品名，"金币" 有特殊含义（见 DropTable.Roll）。
	ItemName string
	// Count 是数量。金币时为金额基数，实际金额 = Count/2 + Random(Count)。
	Count int
}

// GoldName 是掉落表里的金币关键字（sSTRING_GOLDNAME）。
const GoldName = "金币"

// Match 判定单条是否命中。
//
// ⚠️ roll 由调用方传入（便于测试注入固定值），必须是 [0, MaxPoint) 的随机数。
func (d DropItem) Match(roll int) bool {
	if d.MaxPoint <= 0 {
		return false
	}
	return roll >= 0 && roll <= d.SelPoint
}

// ChancePermille 返回掉落概率的千分数（0~1000），便于配置检查与展示。
func (d DropItem) ChancePermille() int {
	if d.MaxPoint <= 0 {
		return 0
	}
	n := d.SelPoint + 1
	if n <= 0 {
		return 0
	}
	return n * 1000 / d.MaxPoint
}

// IsGold 报告这条是否是金币。
func (d DropItem) IsGold() bool { return d.ItemName == GoldName }

// GoldAmount 计算金币掉落金额。
//
// 原版（UsrEngn.pas:1564）：mon.m_nGold += (Count div 2) + Random(Count)
func (d DropItem) GoldAmount(randInt func(int) int) int {
	if d.Count <= 0 {
		return 0
	}
	return d.Count/2 + randInt(d.Count)
}

// ParseDropLine 解析一行掉落配置。
func ParseDropLine(line string) (DropItem, error) {
	var d DropItem

	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ";") {
		return d, fmt.Errorf("空行或注释")
	}

	// 第一段：SelPoint/MaxPoint（分隔符可为空格或 '/'）
	fields := splitDropFields(line)
	if len(fields) < 3 {
		return d, fmt.Errorf("字段不足: %q", line)
	}

	var err error
	if d.SelPoint, err = strconv.Atoi(fields[0]); err != nil {
		return d, fmt.Errorf("SelPoint 非法: %w", err)
	}
	d.SelPoint-- // ⚠️ 文件是 1-based，读入要 -1（LocalDB.pas:1415）
	if d.MaxPoint, err = strconv.Atoi(fields[1]); err != nil {
		return d, fmt.Errorf("MaxPoint 非法: %w", err)
	}
	d.ItemName = strings.Trim(fields[2], `"`)
	if d.ItemName == "" {
		return d, fmt.Errorf("物品名为空: %q", line)
	}
	d.Count = 1
	if len(fields) >= 4 {
		if n, err := strconv.Atoi(fields[3]); err == nil && n > 0 {
			d.Count = n
		}
	}
	// SelPoint 可以为 0（配置写 1/N 时），MaxPoint 必须为正
	if d.SelPoint < 0 || d.MaxPoint <= 0 || d.SelPoint >= d.MaxPoint {
		return d, fmt.Errorf("概率非法 SelPoint=%d MaxPoint=%d: %q", d.SelPoint, d.MaxPoint, line)
	}
	return d, nil
}

// splitDropFields 切分掉落行，支持引号内的空格。
func splitDropFields(line string) []string {
	var (
		out     []string
		cur     strings.Builder
		inQuote bool
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case (c == ' ' || c == '\t' || c == '/') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// DropTable 是某个怪物的掉落表。
type DropTable struct {
	items []DropItem
}

// NewDropTable 建立掉落表。空表返回 nil。
func NewDropTable(items []DropItem) *DropTable {
	if len(items) == 0 {
		return nil
	}
	return &DropTable{items: items}
}

// Items 返回全部条目。
func (t *DropTable) Items() []DropItem {
	if t == nil {
		return nil
	}
	return t.items
}

// Len 返回条目数。
func (t *DropTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.items)
}

// Roll 遍历所有条目分别判定，返回命中的条目。
//
// ⚠️ 一次可以命中**多条**——原版就是这样，配置里每条都是独立概率。
// 早期实现误以为是"区间命中一条"，会显著改变爆率。
//
// randInts 由调用方提供（长度等于条目数，每个是 [0, MaxPoint) 的随机数），
// 便于测试注入确定值。
func (t *DropTable) Roll(randInts []int) []DropItem {
	if t == nil || len(randInts) != len(t.items) {
		return nil
	}
	var out []DropItem
	for i := range t.items {
		if t.items[i].Match(randInts[i]) {
			out = append(out, t.items[i])
		}
	}
	return out
}

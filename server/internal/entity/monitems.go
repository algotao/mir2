package entity

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 掉落表加载（Envir\MonItems\<怪物名>.txt）。
//
// ⚠️ 放在 entity 包而不是 data 包的原因：entity/monster.go 依赖 data
// （Monster 持有 *data.MonsterInfo），若 data 反过来 import entity 会成环。
// 由调用方（gamesvr）直接加载，不并入 data.Tables。
//
// 数据来源：1.76 官方泄露服务端配置（GeeM2）。

// MonItems 是"怪物名 → 掉落表"的集合。
type MonItems struct {
	tables   map[string]*DropTable
	stats    MonItemsStats
	warnings []string
}

// MonItemsStats 是加载统计。
type MonItemsStats struct {
	Files    int // 成功加载的文件数
	Rules    int // 成功解析的规则条数
	BadLines int // 解析失败的行数
	Skipped  int // 跳过的文件数
	// UnknownItems 是物品表里不存在的物品名数量（多为引擎自定义物品）。
	UnknownItems int
}

// Stats 返回加载统计。
func (m *MonItems) Stats() MonItemsStats { return m.stats }

// Warnings 返回逐文件、逐行的数据缺项诊断。
func (m *MonItems) Warnings() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.warnings...)
}

// Tables 返回全部掉落表（按怪物名）。
func (m *MonItems) Tables() map[string]*DropTable { return m.tables }

// Get 按怪物名查掉落表。
//
// 配置里常见带数字后缀的变体文件（"僵尸1.txt"、"半兽人0.txt"），
// 而怪物表里的名字通常不带后缀。因此：
//  1. 先按**完整名字**精确匹配
//  2. 再退化为**去掉尾部数字**匹配
func (m *MonItems) Get(monName string) *DropTable {
	if m == nil || monName == "" {
		return nil
	}
	if t, ok := m.tables[monName]; ok {
		return t
	}
	if base := strings.TrimRight(monName, "0123456789"); base != monName {
		if t, ok := m.tables[base]; ok {
			return t
		}
	}
	return nil
}

// LoadMonItems 加载一个目录下的全部掉落表。
//
// 目录不存在时返回空集合（不报错）——掉落是"锦上添花"，不应阻止启动。
//
// knownItem 用于统计物品表里不存在的物品名（可为 nil，表示不检查）。
// 这些多为引擎自定义物品，掉落时应跳过，否则会刷出不存在的物品。
func LoadMonItems(dir string, knownItem func(name string) bool) (*MonItems, error) {
	m := &MonItems{tables: make(map[string]*DropTable)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("读取掉落表目录 %s: %w", dir, err)
	}

	unknown := make(map[string]bool)
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".txt") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		path := filepath.Join(dir, e.Name())
		items, badRows, err := parseMonItemsFile(path)
		if err != nil {
			m.stats.Skipped++
			m.warnings = append(m.warnings, fmt.Sprintf("MonItems/%s 读取失败: %v", e.Name(), err))
			continue
		}
		m.stats.Files++
		m.stats.Rules += len(items)
		m.stats.BadLines += len(badRows)
		m.warnings = append(m.warnings, badRows...)
		if knownItem != nil {
			for _, it := range items {
				if !it.IsGold() && !knownItem(it.ItemName) {
					unknown[it.ItemName] = true
				}
			}
		}
		if len(items) > 0 {
			m.tables[name] = NewDropTable(items)
		}
	}
	unknownNames := make([]string, 0, len(unknown))
	for name := range unknown {
		unknownNames = append(unknownNames, name)
	}
	sort.Strings(unknownNames)
	for _, name := range unknownNames {
		m.warnings = append(m.warnings, fmt.Sprintf("掉落规则引用物品表中不存在的物品 %q", name))
	}
	m.stats.UnknownItems = len(unknown)
	return m, nil
}

// parseMonItemsFile 解析单个掉落文件，并保留每条坏行的定位信息。
// 文件是 UTF-8（1.76 泄露配置如此）；原版 Delphi 读的是 ANSI，若拿到 GBK 版需转码。
func parseMonItemsFile(path string) ([]DropItem, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var (
		out     []DropItem
		badRows []string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		it, err := ParseDropLine(line)
		if err != nil {
			badRows = append(badRows, fmt.Sprintf("MonItems/%s:%d 无效规则 %q: %v", filepath.Base(path), lineNo, line, err))
			continue
		}
		out = append(out, it)
	}
	if err := sc.Err(); err != nil {
		return nil, badRows, err
	}
	return out, badRows, nil
}

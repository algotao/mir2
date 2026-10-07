package data

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// MakeItem 是制药配方里的一味材料。
type MakeItem struct {
	// Name 是材料物品名（与物品表里的 Name 比对）。
	Name string
	// Count 是需要几件。
	Count int
}

// MakeItemSet 是制药配方表（官方 `data/envir/MakeItem.txt`）。
//
// 文件形态（与官方一致）：
//
//	[灰色药粉(少量)]
//	食人树叶    4
//	毒蜘蛛牙齿  2
//	食人树的果实  1
//
// 原版由 `LocalDB.pas` 读进 `g_MakeItemList`（**不是**数据库表，是个文本文件），
// 再被 `GetMakeItemInfo`（M2Share.pas:3720-3733）按成品名查。
type MakeItemSet struct {
	// Recipes 是"成品名 → 材料清单"。
	Recipes map[string][]MakeItem
	// Order 是成品在文件里的出现顺序（给列表用，避免 map 遍历顺序乱）。
	Order []string
}

// LoadMakeItems 读官方 `MakeItem.txt`。
func LoadMakeItems(path string) (*MakeItemSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	set := &MakeItemSet{Recipes: make(map[string][]MakeItem)}
	var cur string
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, ";") {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			cur = strings.TrimSpace(s[1 : len(s)-1])
			if cur == "" {
				return nil, fmt.Errorf("%s:%d 配方名为空", path, line)
			}
			if _, dup := set.Recipes[cur]; !dup {
				set.Order = append(set.Order, cur)
			}
			set.Recipes[cur] = nil // 允许同名覆盖（原版也是覆盖）
			continue
		}
		if cur == "" {
			// 段头之前的内容是废行（官方文件里偶尔有）
			continue
		}
		fields := strings.Fields(s)
		if len(fields) == 0 {
			continue
		}
		n := 1
		if len(fields) >= 2 {
			v, err := strconv.Atoi(fields[len(fields)-1])
			if err != nil {
				return nil, fmt.Errorf("%s:%d %q 的数量 %q 不是整数", path, line, cur, fields[len(fields)-1])
			}
			n = v
			fields = fields[:len(fields)-1]
		}
		if n <= 0 {
			return nil, fmt.Errorf("%s:%d %q 的材料数量 %d 非法", path, line, cur, n)
		}
		// 材料名可能含空格（"食人树的果实" 那种不会，但保险起见把剩下的拼回去）
		name := strings.Join(fields, " ")
		set.Recipes[cur] = append(set.Recipes[cur], MakeItem{Name: name, Count: n})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(set.Order) == 0 {
		return nil, fmt.Errorf("%s 里一条配方都没有", path)
	}
	return set, nil
}

// Get 按成品名取配方。
func (s *MakeItemSet) Get(name string) ([]MakeItem, bool) {
	if s == nil {
		return nil, false
	}
	r, ok := s.Recipes[name]
	return r, ok && len(r) > 0
}

// Len 是配方条数。
func (s *MakeItemSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Order)
}

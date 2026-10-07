package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
)

// 解析 GeeM2 的 数据库/GEEM2.db.sql。
//
// 这是**官方 1.76 泄露服务端**自带的数据库，与 Envir/MonItems 掉落表同源。
// 用它替换 OpenMir2 的物品表后，掉落表引用物品的匹配率从 56% 提升到 98%。
//
// 表定义里字段很多（StdItems 有 57 列，含大量 Element/Expand 扩展列），
// 这里只取我们用到的前 24 列，其余忽略。

// ParseGeeM2Items 解析 StdItems 表。
//
// ⚠️ GeeM2 的 Idx 是 **0-based**（0..685），而我们的 data.StdItem.Index 约定
// 为 1-based（Get(idx) 内部会 -1）。因此这里统一 +1。
func ParseGeeM2Monsters(sql string) ([]*data.MonsterInfo, error) {
	cols := geeM2Columns(sql, "Monster")
	if len(cols) < 22 {
		return nil, fmt.Errorf("Monster 字段数不足: %d", len(cols))
	}
	pos := make(map[string]int, len(cols))
	for i, c := range cols {
		pos[c] = i
	}
	required := []string{"Name", "Race", "RaceImg", "Appr", "Lvl", "Undead", "CoolEye", "Exp", "HP", "MP",
		"AC", "MAC", "DC", "DCMAX", "MC", "SC", "SPEED", "HIT", "WALK_SPD", "WalkStep", "WalkWait", "ATTACK_SPD"}
	for _, name := range required {
		if _, ok := pos[name]; !ok {
			return nil, fmt.Errorf("Monster 缺少字段 %s", name)
		}
	}

	rows := geeM2Rows(sql, "Monster")
	out := make([]*data.MonsterInfo, 0, len(rows))
	for _, r := range rows {
		if len(r) < len(cols) {
			continue
		}
		out = append(out, &data.MonsterInfo{
			Index:       int32(len(out) + 1),
			Name:        sqlStr(r[pos["Name"]]),
			Race:        uint16(sqlInt(r[pos["Race"]])),
			RaceImg:     uint16(sqlInt(r[pos["RaceImg"]])),
			Appr:        uint16(sqlInt(r[pos["Appr"]])),
			Level:       uint16(sqlInt(r[pos["Lvl"]])),
			Undead:      uint8(sqlInt(r[pos["Undead"]])),
			CoolEye:     uint8(sqlInt(r[pos["CoolEye"]])),
			Exp:         uint32(sqlInt(r[pos["Exp"]])),
			HP:          uint32(sqlInt(r[pos["HP"]])),
			MP:          uint32(sqlInt(r[pos["MP"]])),
			AC:          uint16(sqlInt(r[pos["AC"]])),
			MAC:         uint16(sqlInt(r[pos["MAC"]])),
			DC:          uint16(sqlInt(r[pos["DC"]])),
			DCMax:       uint16(sqlInt(r[pos["DCMAX"]])),
			MC:          uint16(sqlInt(r[pos["MC"]])),
			SC:          uint16(sqlInt(r[pos["SC"]])),
			Speed:       uint16(sqlInt(r[pos["SPEED"]])),
			Hit:         uint16(sqlInt(r[pos["HIT"]])),
			WalkSpeed:   uint16(sqlInt(r[pos["WALK_SPD"]])),
			WalkStep:    uint16(sqlInt(r[pos["WalkStep"]])),
			WalkWait:    uint16(sqlInt(r[pos["WalkWait"]])),
			AttackSpeed: uint16(sqlInt(r[pos["ATTACK_SPD"]])),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Monster 没有可解析的数据行")
	}
	return out, nil
}

func ParseGeeM2Items(sql string) ([]*data.StdItem, error) {
	cols := geeM2Columns(sql, "StdItems")
	if len(cols) < 24 {
		return nil, fmt.Errorf("StdItems 字段数不足: %d", len(cols))
	}
	pos := make(map[string]int, len(cols))
	for i, c := range cols {
		pos[c] = i
	}
	need := []string{"Idx", "Name", "Stdmode", "Shape", "Weight", "Anicount", "Source",
		"Reserved", "Looks", "DuraMax", "Ac", "Ac2", "Mac", "Mac2", "Dc", "Dc2",
		"Mc", "Mc2", "Sc", "Sc2", "Need", "NeedLevel", "Price", "Stock"}
	for _, n := range need {
		if _, ok := pos[n]; !ok {
			return nil, fmt.Errorf("StdItems 缺少字段 %s", n)
		}
	}

	rows := geeM2Rows(sql, "StdItems")
	out := make([]*data.StdItem, 0, len(rows))
	for _, r := range rows {
		if len(r) < len(cols) {
			continue
		}
		it := &data.StdItem{
			Index:    int32(sqlInt(r[pos["Idx"]])) + 1, // 0-based → 1-based
			Name:     sqlStr(r[pos["Name"]]),
			StdMode:  uint8(sqlInt(r[pos["Stdmode"]])),
			Shape:    uint8(sqlInt(r[pos["Shape"]])),
			Weight:   uint8(sqlInt(r[pos["Weight"]])),
			AniCount: uint8(sqlInt(r[pos["Anicount"]])),
			Source:   int8(sqlInt(r[pos["Source"]])),
			Reserved: uint8(sqlInt(r[pos["Reserved"]])),
			Looks:    uint16(sqlInt(r[pos["Looks"]])),
			DuraMax:  uint32(sqlInt(r[pos["DuraMax"]])),

			AC:  data.MinMax{Min: uint16(sqlInt(r[pos["Ac"]])), Max: uint16(sqlInt(r[pos["Ac2"]]))},
			MAC: data.MinMax{Min: uint16(sqlInt(r[pos["Mac"]])), Max: uint16(sqlInt(r[pos["Mac2"]]))},
			DC:  data.MinMax{Min: uint16(sqlInt(r[pos["Dc"]])), Max: uint16(sqlInt(r[pos["Dc2"]]))},
			MC:  data.MinMax{Min: uint16(sqlInt(r[pos["Mc"]])), Max: uint16(sqlInt(r[pos["Mc2"]]))},
			SC:  data.MinMax{Min: uint16(sqlInt(r[pos["Sc"]])), Max: uint16(sqlInt(r[pos["Sc2"]]))},

			Need:      uint32(sqlInt(r[pos["Need"]])),
			NeedLevel: uint32(sqlInt(r[pos["NeedLevel"]])),
			Price:     uint32(sqlInt(r[pos["Price"]])),
			Stock:     uint32(sqlInt(r[pos["Stock"]])),
		}
		out = append(out, it)
	}
	return out, nil
}

// geeM2Columns 取 CREATE TABLE 的字段顺序。
func geeM2Columns(sql, table string) []string {
	re := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS "` + table + `" \((.*?)\n\);`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		return nil
	}
	var cols []string
	for _, mm := range regexp.MustCompile(`"([A-Za-z0-9_]+)"`).FindAllStringSubmatch(m[1], -1) {
		cols = append(cols, mm[1])
	}
	return cols
}

// geeM2Rows 取某表的全部 INSERT 行，每行是一个值切片。
func geeM2Rows(sql, table string) [][]string {
	re := regexp.MustCompile(`(?s)INSERT INTO "` + table + `" VALUES (.*?);`)
	var out [][]string
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		out = append(out, parseSQLTuples(m[1])...)
	}
	return out
}

// parseSQLTuples 把 "(a,b),(c,'d,e')" 拆成值切片的切片。
//
// ⚠️ 必须跳过字符串字面量内部的逗号与括号，否则物品名里的逗号会切错。
func parseSQLTuples(s string) [][]string {
	var (
		out   [][]string
		cur   []string
		buf   strings.Builder
		inStr bool
		depth int
	)
	flushVal := func() {
		cur = append(cur, strings.TrimSpace(buf.String()))
		buf.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inStr:
			inStr = true
			buf.WriteByte(c)
		case c == '\'' && inStr:
			// SQL 里 '' 表示一个单引号
			if i+1 < len(s) && s[i+1] == '\'' {
				buf.WriteByte(c)
				buf.WriteByte(c)
				i++
				continue
			}
			inStr = false
			buf.WriteByte(c)
		case inStr:
			buf.WriteByte(c)
		case c == '(':
			depth++
			if depth == 1 {
				cur, buf = nil, strings.Builder{}
			} else {
				buf.WriteByte(c)
			}
		case c == ')':
			depth--
			if depth == 0 {
				flushVal()
				if len(cur) > 0 {
					out = append(out, cur)
				}
				cur = nil
			} else {
				buf.WriteByte(c)
			}
		case c == ',' && depth == 1:
			flushVal()
		default:
			buf.WriteByte(c)
		}
	}
	return out
}

// sqlInt 解析整数值（可能是 '123' / 123 / NULL）。
func sqlInt(v string) int64 {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "'")
	if v == "" || strings.EqualFold(v, "NULL") {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// sqlStr 解析字符串值（去引号、还原 ” 转义）。
func sqlStr(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		v = v[1 : len(v)-1]
	}
	return strings.ReplaceAll(v, "''", "'")
}

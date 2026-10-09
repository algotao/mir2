// Command seedgen 把 MySQL dump 转换为 mir2go 的静态数据种子文件（JSON）。
//
// 用法：
//
//	go run ./cmd/seedgen            # 默认读仓库内的 data/seed/*.sql，写 ./data
//
// 生成 stditems.json / monsters.json / magics.json。
//
// 两个输入都是仓库内的种子 SQL（data/seed/），所以**不再依赖外部 checkout**：
//
//	data/seed/mir2_data.sql   OpenMir2 的 705 怪物（跨境大表，兼当"经典怪名"白名单）/ 经典技能 / 物品
//	data/seed/GEEM2.db.sql    GeeM2 的 378 怪物 / 686 物品（默认覆盖前者；⚠️ 不是纯 1.76）
//
// ⚠️ GeeM2 的怪物表混着 176 条后期扩展，默认按经典表裁剪（`-classic-monsters`，
// 见 docs/decisions.md D-56/D-57）；裁剪后 Index 重排为连续值。
//
// 按列名映射而非按位置——SQL 的 magics 表字段顺序与 Delphi `SELECT * FROM Magic`
// 不同，按位置取值会静默错位。
package main

import (
	"flag"
	"fmt"
	_ "github.com/algotao/mir2/server/internal/tz"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
)

// insertRe 定位 `INSERT INTO `xxx` VALUES `，捕获表名。
var insertRe = regexp.MustCompile("INSERT INTO `(stditems|monsters|magics)` VALUES ")

// classicMagicMaxID 是经典技能号上界。
//
// 实测 OpenMir2 的 magics 表：MagicID 1..33 与 Delphi Common/Grobal2.pas:1268-1300
// 的 SKILL_* 常量**逐条对齐**（1 火球术 … 33 冰咆哮），这是复古版需要的部分。
//
// 34 之后开始分歧且出现重复：
//   - 本表 idx=34 是"解毒术"，Delphi 的 SKILL_UNAMYOUNSUL 却是 40（34 是"双龙斩"）
//   - idx=38 "群体施毒术" 与 idx=48 "气功波" 的 MagicID 都是 48
//   - idx 158..206 是 1.8+ 的四级/英雄技能，MagicID 与经典技能冲突
//
// 因此复古版按 MagicID<=33 过滤，既对齐常量又保证唯一。
const classicMagicMaxID = 33

func main() {
	src := flag.String("src", "./data/seed/mir2_data.sql", "OpenMir2 兼容种子 SQL 路径（默认由 GeeM2 Monster/StdItems 覆盖）")
	out := flag.String("out", "./data", "输出目录")
	classicOnly := flag.Bool("classic-only", true,
		"只导出经典技能（index<=108）。关闭则会带上 1.8+ 的四级/英雄技能，它们的 MagicID 与经典技能重复")
	// GeeM2 的官方 1.76 数据库：默认用同源怪物与物品表覆盖 OpenMir2 数据，
	// 使 MonGen/MonItems 与怪物模板配对，缺项由 gamesvr 启动时逐条告警。
	geem2 := flag.String("geem2", "./data/seed/GEEM2.db.sql",
		"GeeM2 数据库 SQL 路径；用同源 Monster/StdItems 覆盖 src 数据（默认开着）")
	// ⚠️ GeeM2 的 Monster 表**不是纯 1.76**：378 条里混着 176 条后期扩展
	// （南蛮/狐月/红洞/封魔/古代/一六男战…），而 Envir/mongen.txt 是 1.76+后期
	// 的混合配置 ⇒ 那些怪会被刷到 1.76 的地图上（幻影寒虎就是这么进新手村的）。
	// 默认只保留「OpenMir2 经典表里也有」的名字（见 docs/decisions.md D-57）。
	classicMonsters := flag.Bool("classic-monsters", true,
		"怪物表只保留 OpenMir2 经典表里也有的（GeeM2 独有 = 非 1.76，见 D-57）")
	flag.Parse()

	raw, err := os.ReadFile(*src)
	if err != nil {
		fatal("读取源文件: %v", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal("创建输出目录: %v", err)
	}

	text := string(raw)

	// ⚠️ 累加器必须在循环外：dump 里每张表是多条 INSERT 语句，
	// 若在循环内重建 slice，最终只会留下最后一条语句的内容。
	var items []*data.StdItem
	var monsters []*data.MonsterInfo
	var magics []*data.MagicInfo
	// classicNames 是 OpenMir2 经典怪物表里的名字集合（`mir2_data.sql` 的 monsters 表）。
	// GeeM2 表按它裁剪，见 -classic-monsters。
	classicNames := map[string]bool{}

	for _, loc := range insertRe.FindAllStringSubmatchIndex(text, -1) {
		table := text[loc[2]:loc[3]]
		rest := text[loc[1]:]
		// 语句以 ';' 结束
		end := strings.IndexByte(rest, ';')
		if end < 0 {
			continue
		}
		rows := parseRows(rest[:end])

		switch table {
		case "stditems":
			for _, r := range rows {
				items = append(items, stdItemFromRow(r))
			}
		case "monsters":
			for _, r := range rows {
				m := monsterFromRow(r)
				classicNames[m.Name] = true
				monsters = append(monsters, m)
			}
		case "magics":
			for _, r := range rows {
				m := magicFromRow(r)
				if *classicOnly && m.MagicID > classicMagicMaxID {
					continue
				}
				magics = append(magics, m)
			}
		}
	}

	// GeeM2 物品表覆盖（可选）
	geeItems := 0
	if *geem2 != "" {
		raw2, err := os.ReadFile(*geem2)
		if err != nil {
			fatal("读取 GeeM2 数据库: %v", err)
		}
		gi, err := ParseGeeM2Items(string(raw2))
		if err != nil {
			fatal("解析 GeeM2 StdItems: %v", err)
		}
		if len(gi) == 0 {
			fatal("GeeM2 StdItems 解析结果为空")
		}
		gm, err := ParseGeeM2Monsters(string(raw2))
		if err != nil {
			fatal("解析 GeeM2 Monster: %v", err)
		}
		if len(gm) == 0 {
			fatal("GeeM2 Monster 解析结果为空")
		}
		items = gi
		monsters = gm
		geeItems = len(gi)
		fmt.Printf("  物品表已切换为 GeeM2 官方 1.76（%d 条）\n", geeItems)
		fmt.Printf("  怪物表已切换为 GeeM2 官方 1.76（%d 条）\n", len(gm))
	}

	// 怪物表按 1.76 裁剪：只留经典表里也有的名字，再重排 Index。
	//
	// ⚠️ Index 必须**连续**（`MonsterSet` 要求 Index == 下标+1），所以裁剪后要重排；
	// 怪物是按名字被引用的（MonGen/MonItems/城堡配置都用名字），重排 Index 安全。
	if *classicMonsters {
		before := len(monsters)
		monsters = pruneToClassic(monsters, classicNames)
		for i, m := range monsters {
			m.Index = int32(i + 1)
		}
		fmt.Printf("  怪物表按 1.76 裁剪：%d → %d 条（裁掉 %d 条 GeeM2 私有的后期怪）\n",
			before, len(monsters), before-len(monsters))
	}

	// 城堡实体模板（城门 + 三段城墙）必须补在末尾。
	//
	// ⚠️ 它们**不在** OpenMir2 的 monsters 表里，取自 GeeM2 的 `GEEM2.db.sql`。
	// 城堡系统按**名字**查这几条模板（Castle/0/SabukW.txt 的 MainDoorName=SabukDoor、
	// LeftWallName=SabukW1…），见 internal/castle。
	//
	// 这几条早先是**手工补进 data/monsters.json** 的，于是重跑 seedgen 会把
	// 它们抹掉（城堡直接少 4 个实体）。放在这里后"重生成 == 提交版"，
	// 仓库内自包含的数据链才算闭环。
	//
	// Index 必须接在已有怪物之后连续（MonsterSet 要求 Index == 下标+1）。
	monsters = append(monsters, castleMonsters(len(monsters))...)

	if err := data.WriteJSON(filepath.Join(*out, data.StdItemsFile), items); err != nil {
		fatal("写 stditems: %v", err)
	}
	if err := data.WriteJSON(filepath.Join(*out, data.MonstersFile), monsters); err != nil {
		fatal("写 monsters: %v", err)
	}
	if err := data.WriteJSON(filepath.Join(*out, data.MagicsFile), magics); err != nil {
		fatal("写 magics: %v", err)
	}
	nItems, nMonsters, nMagics := len(items), len(monsters), len(magics)

	if nItems == 0 || nMonsters == 0 || nMagics == 0 {
		fatal("未解析到数据（items=%d monsters=%d magics=%d），请检查源文件", nItems, nMonsters, nMagics)
	}
	fmt.Printf("生成完成 → %s\n  stditems %d\n  monsters %d\n  magics   %d\n",
		*out, nItems, nMonsters, nMagics)
}

// pruneToClassic 只保留 `classic` 里也有的怪物（见 docs/decisions.md D-56/D-57）。
//
// 判据只有**一个方向**是有力的：GeeM2 表有、OpenMir2 经典表没有 ⇒ 这条是 GeeM2 私有的
// （后期扩展或自造），不属于 1.76 官方 —— 幻影寒虎/南蛮*/狐月*/红洞*/封魔*/古代*/一六男战…
// 反方向不成立：两张表都有的可能是 1.8+ 内容（比如 `魔龙刀兵`），但那些怪只刷在
// 后期地图上，而 `prune176` 已经把那些地图和刷怪点摘了，模板留着不会被刷出来。
//
// 城堡四条（SabukDoor/SabukW1..3）不在 OpenMir2 表里，由 castleMonsters 单独补，不走这里。
func pruneToClassic(ms []*data.MonsterInfo, classic map[string]bool) []*data.MonsterInfo {
	out := make([]*data.MonsterInfo, 0, len(ms))
	for _, m := range ms {
		if classic[m.Name] {
			out = append(out, m)
		}
	}
	return out
}

// castleMonsters 返回 4 条城堡实体模板（Index 接在 base 之后）。
//
// 数值逐字段取自 GeeM2 `GEEM2.db.sql` 的 Monster 表，与
// internal/data/data_test.go 里断言的城堡模板一致：
//
//	SabukDoor  race 110 / RaceImg 99 / Appr 900 / HP 10000 / MAC 20
//	SabukW1..3 race 111 / RaceImg 98 / Appr 901..903 / HP 5000 / MAC 20,20,99
//
// 公共值：Lvl 99、Exp 1（不掉经验）、AC 20、Speed 15、Hit 1、
// WalkSpeed/AttackSpeed 1000、WalkStep 1。
func castleMonsters(base int) []*data.MonsterInfo {
	common := func(i int, name string, race, raceImg, appr uint16, hp uint32, mac uint16) *data.MonsterInfo {
		return &data.MonsterInfo{
			Index: int32(base + i + 1), Name: name,
			Race: race, RaceImg: raceImg, Appr: appr,
			Level: 99, Exp: 1, HP: hp,
			AC: 20, MAC: mac,
			Speed: 15, Hit: 1,
			WalkSpeed: 1000, WalkStep: 1, AttackSpeed: 1000,
		}
	}
	return []*data.MonsterInfo{
		common(0, "SabukDoor", 110, 99, 900, 10000, 20),
		common(1, "SabukW1", 111, 98, 901, 5000, 20),
		common(2, "SabukW2", 111, 98, 902, 5000, 20),
		common(3, "SabukW3", 111, 98, 903, 5000, 99),
	}
}

// ---------- 字段映射：按 SQL 列顺序取值 ----------

// stditems: Id, Name, StdMode, Shape, Weight, AniCount, Source, Reserved,
// ImgIndex, DuraMax, Ac, AcMax, Mac, MacMax, Dc, DcMax, Mc, McMax, Sc, ScMax,
// Need, NeedLevel, Price, Stock, ...
func stdItemFromRow(r row) *data.StdItem {
	return &data.StdItem{
		Index:     int32(r.intAt(0)),
		Name:      r.strAt(1),
		StdMode:   uint8(r.intAt(2)),
		Shape:     uint8(r.intAt(3)),
		Weight:    uint8(r.intAt(4)),
		AniCount:  uint8(r.intAt(5)),
		Source:    int8(r.intAt(6)),
		Reserved:  uint8(r.intAt(7)),
		Looks:     uint16(r.intAt(8)),
		DuraMax:   uint32(r.intAt(9)),
		AC:        data.MinMax{Min: uint16(r.intAt(10)), Max: uint16(r.intAt(11))},
		MAC:       data.MinMax{Min: uint16(r.intAt(12)), Max: uint16(r.intAt(13))},
		DC:        data.MinMax{Min: uint16(r.intAt(14)), Max: uint16(r.intAt(15))},
		MC:        data.MinMax{Min: uint16(r.intAt(16)), Max: uint16(r.intAt(17))},
		SC:        data.MinMax{Min: uint16(r.intAt(18)), Max: uint16(r.intAt(19))},
		Need:      uint32(r.intAt(20)),
		NeedLevel: uint32(r.intAt(21)),
		Price:     uint32(r.intAt(22)),
		Stock:     uint32(r.intAt(23)),
	}
}

// monsters: Idx, Name, Race, RaceImg, Appr, Lvl, Undead, CoolEye, Exp, HP, MP,
// AC, MAC, DC, DCMAX, MC, SC, SPEED, HIT, WALK_SPD, WalkStep, WaLkWait, ATTACK_SPD, ...
func monsterFromRow(r row) *data.MonsterInfo {
	return &data.MonsterInfo{
		Index:       int32(r.intAt(0)),
		Name:        r.strAt(1),
		Race:        uint16(r.intAt(2)),
		RaceImg:     uint16(r.intAt(3)),
		Appr:        uint16(r.intAt(4)),
		Level:       uint16(r.intAt(5)),
		Undead:      uint8(r.intAt(6)),
		CoolEye:     uint8(r.intAt(7)),
		Exp:         uint32(r.intAt(8)),
		HP:          uint32(r.intAt(9)),
		MP:          uint32(r.intAt(10)),
		AC:          uint16(r.intAt(11)),
		MAC:         uint16(r.intAt(12)),
		DC:          uint16(r.intAt(13)),
		DCMax:       uint16(r.intAt(14)),
		MC:          uint16(r.intAt(15)),
		SC:          uint16(r.intAt(16)),
		Speed:       uint16(r.intAt(17)),
		Hit:         uint16(r.intAt(18)),
		WalkSpeed:   uint16(r.intAt(19)),
		WalkStep:    uint16(r.intAt(20)),
		WalkWait:    uint16(r.intAt(21)),
		AttackSpeed: uint16(r.intAt(22)),
	}
}

// magics: Idx, MagID, MagName, EffectType, Effect, Spell, Power, MaxPower,
// DefSpell, DefPower, DefMaxPower, Job, NeedL1, L1Train, NeedL2, L2Train,
// NeedL3, L3Train, Delay, Descr
//
// ⚠️ 该顺序与 Delphi `SELECT * FROM Magic` 不同（Delphi 里 DefSpell/DefPower/
// DefMaxPower 在 Delay 之后）。此处按 SQL 顺序取，字段名一一对应。
func magicFromRow(r row) *data.MagicInfo {
	return &data.MagicInfo{
		Index:       int32(r.intAt(0)),
		MagicID:     uint16(r.intAt(1)),
		Name:        r.strAt(2),
		EffectType:  uint8(r.intAt(3)),
		Effect:      uint8(r.intAt(4)),
		Spell:       uint16(r.intAt(5)),
		Power:       uint16(r.intAt(6)),
		MaxPower:    uint16(r.intAt(7)),
		DefSpell:    uint16(r.intAt(8)),
		DefPower:    uint16(r.intAt(9)),
		DefMaxPower: uint16(r.intAt(10)),
		Job:         uint8(r.intAt(11)),
		NeedL1:      uint16(r.intAt(12)),
		L1Train:     uint32(r.intAt(13)),
		NeedL2:      uint16(r.intAt(14)),
		L2Train:     uint32(r.intAt(15)),
		NeedL3:      uint16(r.intAt(16)),
		L3Train:     uint32(r.intAt(17)),
		Delay:       int32(r.intAt(18)),
		Descr:       r.strAt(19),
	}
}

// ---------- MySQL dump 解析 ----------

// row 是一行已解析的字段值（保留原始文本，含引号）。
type row []string

func (r row) at(i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return strings.TrimSpace(r[i])
}

// intAt 取整数；NULL / 空 → 0。
func (r row) intAt(i int) int64 {
	s := r.at(i)
	if s == "" || strings.EqualFold(s, "NULL") {
		return 0
	}
	s = strings.Trim(s, "'")
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// 容忍 '123' 之外的异常，按 0 处理，避免单条脏数据中断全量导入
		return 0
	}
	return v
}

// strAt 取字符串；NULL → ""。
func (r row) strAt(i int) string {
	s := r.at(i)
	if s == "" || strings.EqualFold(s, "NULL") {
		return ""
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		s = s[1 : len(s)-1]
	}
	return unescapeSQL(s)
}

func unescapeSQL(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseRows 把 `VALUES (..),(..)` 段解析为多行字段。
// 必须正确处理：字符串内的逗号/括号、转义引号、嵌套括号。
func parseRows(s string) []row {
	var rows []row
	i := 0
	for i < len(s) {
		if s[i] != '(' {
			i++
			continue
		}
		// 找到与该 '(' 匹配的 ')'（跳过字符串内部）
		depth := 0
		inStr := false
		j := i
		closed := false
		for ; j < len(s); j++ {
			c := s[j]
			if inStr {
				if c == '\\' && j+1 < len(s) {
					j++
					continue
				}
				if c == '\'' {
					inStr = false
				}
				continue
			}
			switch c {
			case '\'':
				inStr = true
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					closed = true
				}
			}
			if closed {
				break
			}
		}
		if !closed {
			break
		}
		rows = append(rows, splitFields(s[i+1:j]))
		i = j + 1
	}
	return rows
}

// splitFields 按顶层逗号分割字段，保留原始文本（含引号）。
func splitFields(s string) row {
	out := make(row, 0, 24)
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				cur.WriteByte(c)
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == '\'' {
				inStr = false
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '\'':
			inStr = true
			cur.WriteByte(c)
		case ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seedgen: "+format+"\n", args...)
	os.Exit(1)
}

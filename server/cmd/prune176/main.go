// Command prune176 把从 `$WS/Mir2-GeeM2` 拷来的 `Envir` 配置裁成「只留 1.76」。
//
// 背景：`server/data/envir/*`（mapinfo/mongen/NPC 脚本/出生点/小地图…）整份来自
// Mir2-GeeM2，而那份配置是「1.76 官方 + 后期扩展」的混合体 —— 于是后期地图
// （魔龙城/狐月/南蛮/血红洞窟/封魔堡/火龙殿…）在我们这儿是**可达**的，
// 后期怪物也会刷到 1.76 的地图上（幻影寒虎进新手村就是这么来的，见 D-56）。
//
// 本命令做的事（幂等，可重复跑）：
//
//	mapinfo.txt   删掉非 1.76 的地图条目
//	mongen.txt    删掉「非 1.76 地图上」与「怪物模板不存在」的刷怪点
//	StartPoint.txt / MiniMap.txt / Npcs.txt / merchant.txt / GuardList.txt
//	              删掉引用非 1.76 地图的行
//	FireDragonGuard.txt        删除（整份都是火龙，非 1.76）
//	monitems/*.txt            删掉怪物表里已不存在的掉落表
//	market_def/*.txt          删掉「脚本所在图非 1.76 / 已不存在」的脚本；
//	                          再把跳到非 1.76 图的 `[@标签]` 段连同菜单项一起摘掉
//
// 用法：`go run ./cmd/prune176`（在 server/ 下；先跑 `go run ./cmd/seedgen`
// 重新生成 monsters.json，本命令按它判「怪物模板是否存在」）。`-dry` 只报告不改。
//
// ⚠️ 判据（哪些图/怪算非 1.76）与"存疑但保留"的清单写在 docs/decisions.md D-57。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// killMaps 是非 1.76 的地图（GeeM2 后期扩展），值是分组名（写进报告与 D-57）。
//
// 判定依据：① 与 1.76 官方地图集/怪物集对不上的名字（魔龙/狐月/南蛮/血红洞窟/火龙/封魔堡）；
// ② 与"后期批次"同号段（D2051-D2083 紧接 1.76 的封魔谷 D2000-D2013 之后，且与
// 雷炎洞穴/火龙殿同批，怪物也是 1.8x 风格的 恶灵/骷髅长枪兵/牛魔* 变体）；
// ③ 页面级功能（热血足球场）。**存疑而保留**的另有一份清单，见 D-57。
var killMaps = map[string]string{
	// 魔龙（1.8x）：主城 + 郊外 + 林间 + 旧寨/祭坛/沼泽 + 关口 + 宫殿/军营
	"6": "魔龙", "61": "魔龙", "62": "魔龙", "63": "魔龙", "64": "魔龙", "65": "魔龙", "66": "魔龙",
	"611": "魔龙", "612": "魔龙", "613": "魔龙", "621": "魔龙", "622": "魔龙",
	"631": "魔龙", "632": "魔龙", "GA1": "魔龙", "B357": "魔龙",
	// 雷炎洞穴 / 火龙殿
	"D2081": "雷炎火龙", "D2082": "雷炎火龙", "D2083": "雷炎火龙",
	// 尸魔洞 / 骨魔洞 / 牛魔寺庙（D20xx 后期批次）
	"D2051": "尸魔骨魔牛魔", "D2052": "尸魔骨魔牛魔", "D2053": "尸魔骨魔牛魔", "D2054": "尸魔骨魔牛魔",
	"D2055": "尸魔骨魔牛魔", "D2056": "尸魔骨魔牛魔",
	"D2061": "尸魔骨魔牛魔", "D2062": "尸魔骨魔牛魔", "D2063": "尸魔骨魔牛魔", "D2064": "尸魔骨魔牛魔",
	"D2065": "尸魔骨魔牛魔", "D2066": "尸魔骨魔牛魔", "D2067": "尸魔骨魔牛魔",
	"D2070": "尸魔骨魔牛魔", "D2071": "尸魔骨魔牛魔", "D2072": "尸魔骨魔牛魔", "D2073": "尸魔骨魔牛魔",
	"D2074": "尸魔骨魔牛魔", "D2075": "尸魔骨魔牛魔", "D2076": "尸魔骨魔牛魔", "D2077": "尸魔骨魔牛魔",
	"D2078": "尸魔骨魔牛魔", "D2079": "尸魔骨魔牛魔",
	// 狐月 / 南蛮 / 血红洞窟 / 废弃之地
	"fox01": "狐月", "fox02": "狐月", "fox03": "狐月",
	"nanm": "南蛮", "ygfx1": "南蛮", "ygfx2": "南蛮", "ygfx3": "南蛮",
	"hsdk1": "血红洞窟", "hsdk2": "血红洞窟", "hsdk3": "血红洞窟",
	"hsdk777": "血红洞窟", "hsdk778": "血红洞窟", "hsdk779": "血红洞窟", "hsdk780": "血红洞窟",
	"hsdk781": "血红洞窟", "hsdk782": "血红洞窟", "hsdk783": "血红洞窟", "hsdk784": "血红洞窟",
	"hsdk785": "血红洞窟", "hsdk786": "血红洞窟",
	"dygw": "废弃之地",
	// 封魔堡 + 恶魔店铺 + 魔龙军营（B3xx 一整族）
	"B341": "封魔堡", "B342": "封魔堡", "B343": "封魔堡", "B344": "封魔堡", "B345": "封魔堡",
	"B346": "封魔堡", "B347": "封魔堡",
	"B351": "封魔堡", "B352": "封魔堡", "B353": "封魔堡", "B354": "封魔堡", "B355": "封魔堡",
	"B356": "封魔堡",
	// 热血足球场（活动图）
	"G003": "足球场", "G004": "足球场", "G005": "足球场", "G006": "足球场", "G007": "足球场",
	"G008": "足球场", "G009": "足球场", "G010": "足球场", "G011": "足球场", "G013": "足球场",
	"G014": "足球场",
	// 神秘战场
	"F011": "神秘战场", "F012": "神秘战场", "F013": "神秘战场",
	// 奸商 / 国王陵墓
	"DM001": "DM", "DM002": "DM", "DM011": "DM",
}

var (
	entryRe = regexp.MustCompile(`^\[(\S+)\s`)
	labelRe = regexp.MustCompile(`^\s*\[@(\w+)\]`)
	sectRe  = regexp.MustCompile(`^\s*\[`)
	moveRe  = regexp.MustCompile(`(?i)^\s*mapmove\s+(\S+)`)
	menuRe  = regexp.MustCompile(`<[^<>]*/@(\w+)>`)
)

func main() {
	dataDir := flag.String("data", "./data", "静态数据目录（server/data）")
	dry := flag.Bool("dry", false, "只报告，不改文件")
	flag.Parse()

	monsters, err := loadMonsterNames(filepath.Join(*dataDir, "monsters.json"))
	if err != nil {
		log.Fatalf("读取怪物表: %v", err)
	}
	fmt.Printf("怪物模板: %d 条（判「刷怪点引用不存在的怪」用）\n", len(monsters))

	kept, n := pruneMapInfo(filepath.Join(*dataDir, "envir", "mapinfo.txt"), *dry)
	fmt.Printf("mapinfo.txt : 删掉 %d 张非 1.76 地图，剩 %d 张\n", n, len(kept))

	byMap, byMon := pruneMongen(filepath.Join(*dataDir, "envir", "mongen.txt"), kept, monsters, *dry)
	fmt.Printf("mongen.txt  : 删掉 %d 个刷怪点（非 1.76 地图 %d + 怪物模板不存在 %d）\n", byMap+byMon, byMap, byMon)

	for _, f := range []struct {
		name  string
		field int
	}{
		{"StartPoint.txt", 0}, {"MiniMap.txt", 0}, {"Npcs.txt", 2}, {"merchant.txt", 1}, {"GuardList.txt", 1},
	} {
		if k := pruneByField(filepath.Join(*dataDir, "envir", f.name), f.field, kept, *dry); k > 0 {
			fmt.Printf("%-16s: 删掉 %d 行（引用非 1.76 地图）\n", f.name, k)
		}
	}

	if p := filepath.Join(*dataDir, "envir", "FireDragonGuard.txt"); exists(p) {
		fmt.Println("FireDragonGuard.txt: 删除（整份是火龙守护兽，非 1.76）")
		if !*dry {
			if err := os.Remove(p); err != nil {
				log.Fatalf("删除 %s: %v", p, err)
			}
		}
	}

	if dm := pruneMonItems(filepath.Join(*dataDir, "monitems"), monsters, *dry); dm > 0 {
		fmt.Printf("monitems/   : 删掉 %d 个掉落表（怪物模板已不存在）\n", dm)
	}

	sc, dead := pruneMarketDef(filepath.Join(*dataDir, "envir", "market_def"), kept, *dry)
	fmt.Printf("market_def/ : 删掉 %d 个脚本（所在图非 1.76/不存在），摘掉 %d 处跳到非 1.76 图的分支\n", sc, dead)

	if *dry {
		fmt.Println("\n（-dry：没有改任何文件）")
	}
}

// loadMonsterNames 取 monsters.json 里的怪物名集合。
func loadMonsterNames(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(list))
	for _, m := range list {
		out[m.Name] = true
	}
	return out, nil
}

// pruneMapInfo 删掉非 1.76 的 `[图号 图名 ...]` 条目，返回保留的图号集合。
func pruneMapInfo(path string, dry bool) (map[string]bool, int) {
	lines, err := readLines(path)
	if err != nil {
		log.Fatalf("读取 %s: %v", path, err)
	}
	kept := map[string]bool{}
	var out []string
	removed := 0
	for _, ln := range lines {
		if m := entryRe.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			if _, kill := killMaps[m[1]]; kill {
				removed++
				continue
			}
			kept[m[1]] = true
		}
		out = append(out, ln)
	}
	if !dry && removed > 0 {
		writeLines(path, out)
	}
	return kept, removed
}

// pruneMongen 删掉「非 1.76 地图上」与「怪物模板不存在」的刷怪点。
func pruneMongen(path string, kept, monsters map[string]bool, dry bool) (byMap, byMon int) {
	lines, err := readLines(path)
	if err != nil {
		log.Fatalf("读取 %s: %v", path, err)
	}
	var out []string
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" || strings.HasPrefix(s, ";") {
			out = append(out, ln)
			continue
		}
		f := strings.Fields(s)
		if len(f) >= 1 {
			// 第一列是图号：非保留图上的行一律删（含配置里本来就残缺的坏行，
			// 例如只剩一个 `hsdk786` 的那种）。
			if !kept[strings.ToLower(f[0])] && !kept[f[0]] {
				byMap++
				continue
			}
		}
		if len(f) >= 4 && !monsters[f[3]] {
			byMon++
			continue
		}
		out = append(out, ln)
	}
	if !dry && (byMap+byMon) > 0 {
		writeLines(path, out)
	}
	return byMap, byMon
}

// pruneByField 删掉第 field 列（0 起）是「非保留图号」的行。
func pruneByField(path string, field int, kept map[string]bool, dry bool) int {
	lines, err := readLines(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		log.Fatalf("读取 %s: %v", path, err)
	}
	var out []string
	removed := 0
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		f := strings.Fields(s)
		if s != "" && !strings.HasPrefix(s, ";") && len(f) > field {
			if id := f[field]; !kept[id] && !kept[strings.ToLower(id)] {
				removed++
				continue
			}
		}
		out = append(out, ln)
	}
	if !dry && removed > 0 {
		writeLines(path, out)
	}
	return removed
}

// pruneMonItems 删掉文件名（怪物名）已不在怪物表里的掉落表 ——
// 留着只会让启动日志刷「掉落表对应不上怪物模板」的告警。
func pruneMonItems(dir string, monsters map[string]bool, dry bool) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		log.Fatalf("读取 %s: %v", dir, err)
	}
	removed := 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".txt") {
			continue
		}
		if monsters[strings.TrimSuffix(name, ".txt")] {
			continue
		}
		removed++
		if !dry {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				log.Fatalf("删除 %s: %v", name, err)
			}
		}
	}
	return removed
}

// pruneMarketDef 处理 NPC 脚本：
//
//	① 脚本文件名的 `-<图号>` 后缀不是保留图（或压根不是本配置里的图）⇒ 整个脚本删掉 ——
//	   服务器只在 merchant.txt/Npcs.txt 里按图号找脚本，这些脚本永远用不上；
//	② 保留脚本里，`[@标签]` 段内出现 `mapmove <非保留图>` ⇒ 把整段连同指向它的菜单项
//	   （`<文字/@标签>`）一起摘掉，避免玩家点了没反应（幻境 H001 那 100 份就是这么来的）。
func pruneMarketDef(dir string, kept map[string]bool, dry bool) (deleted, deadSec int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0
		}
		log.Fatalf("读取 %s: %v", dir, err)
	}
	var noSuffix []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".txt") {
			continue
		}
		base := strings.TrimSuffix(name, ".txt")
		i := strings.LastIndex(base, "-")
		if i < 0 {
			// 没有 `-图号` 后缀的脚本（会员/媒人这类）：按名字被调用，不属于
			// 「非 1.76 地图」的范畴，一律保留，只统计给报告看。
			noSuffix = append(noSuffix, name)
			continue
		}
		suffix := base[i+1:]
		if !kept[suffix] && !kept[strings.ToLower(suffix)] {
			deleted++
			if !dry {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					log.Fatalf("删除 %s: %v", name, err)
				}
			}
			continue
		}
		path := filepath.Join(dir, name)
		lines, err := readLines(path)
		if err != nil {
			log.Fatalf("读取 %s: %v", name, err)
		}
		keptLines, dropped := stripDeadSections(lines, kept)
		if dropped == 0 {
			continue
		}
		deadSec += dropped
		if !dry {
			writeLines(path, keptLines)
		}
	}
	if len(noSuffix) > 0 {
		sort.Strings(noSuffix)
		fmt.Printf("              保留 %d 个没有 `-图号` 后缀的脚本（会员/媒人这类）: %s\n",
			len(noSuffix), strings.Join(noSuffix, " "))
	}
	return deleted, deadSec
}

// stripDeadSections 摘掉「跳到非保留图」的 `[...]` 段与其菜单项。
func stripDeadSections(lines []string, kept map[string]bool) ([]string, int) {
	// 先把文件切成「段」：`[...]` 行开头算新段。
	type section struct {
		head string
		body []string
		dead bool
	}
	var pre []string
	var secs []section
	for _, ln := range lines {
		if sectRe.MatchString(ln) {
			secs = append(secs, section{head: ln})
			continue
		}
		if len(secs) == 0 {
			pre = append(pre, ln)
			continue
		}
		secs[len(secs)-1].body = append(secs[len(secs)-1].body, ln)
	}

	deadLabels := map[string]bool{}
	dropped := 0
	for i := range secs {
		for _, ln := range secs[i].body {
			m := moveRe.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			tgt := m[1]
			if kept[tgt] || kept[strings.ToLower(tgt)] {
				continue
			}
			secs[i].dead = true
			if lm := labelRe.FindStringSubmatch(secs[i].head); lm != nil {
				deadLabels[lm[1]] = true
			}
		}
		if secs[i].dead {
			dropped++
		}
	}
	if dropped == 0 {
		return lines, 0
	}

	var out []string
	out = append(out, pre...)
	for _, s := range secs {
		if s.dead {
			continue
		}
		body := s.body
		if len(deadLabels) > 0 {
			body = body[:0:0]
			for _, ln := range s.body {
				if mm := menuRe.FindAllStringSubmatch(ln, -1); mm != nil {
					drop := false
					for _, g := range mm {
						if deadLabels[g[1]] {
							drop = true
						}
					}
					if drop {
						continue
					}
				}
				body = append(body, ln)
			}
		}
		out = append(out, s.head)
		out = append(out, body...)
	}
	return out, dropped
}

func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), nil
}

func writeLines(path string, lines []string) {
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		log.Fatalf("写 %s: %v", path, err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

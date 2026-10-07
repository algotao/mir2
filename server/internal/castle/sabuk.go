package castle

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/algotao/mir2/server/internal/storage"
)

// DefaultConfigDir 是默认配置子目录（单机只有一座城堡）。
const DefaultConfigDir = "0"

// MaxArcher / MaxGuard 是可雇佣单位的数量上限
// （MAXCASTLEARCHER=12 / MAXCALSTEGUARD=4，Castle.pas:6-8）。
const (
	MaxArcher = 12
	MaxGuard  = 4
)

// DefaultRecord 返回沙巴克的出厂配置。
//
// 全部取自 Delphi TUserCastle.Create（Castle.pas:161-183）与
// 1.76 官方 Castle\0\SabukW.txt（两者一致，故以官方文件为准）。
func DefaultRecord() storage.Castle {
	return storage.Castle{
		ConfigDir:   DefaultConfigDir,
		Name:        "沙巴克",
		MapName:     "3",
		PalaceMap:   "0150",
		SecretMap:   "D701",
		HomeMap:     "3",
		HomeX:       644,
		HomeY:       290,
		ExtraMaps:   []string{"0151", "0152", "0153", "0154", "0155", "0156"},
		WarRangeX:   100,
		WarRangeY:   100,
		PalaceDoorX: 631,
		PalaceDoorY: 274,
		ChangeDate:  time.Time{},
		WarDate:     time.Time{},
		TotalGold:   0,
		TodayIncome: 0,
		IncomeToday: time.Time{},
		TechLevel:   0,
		Power:       0,
		Units:       defaultUnits(),
	}
}

// defaultUnits 返回官方的城门/城墙/雇佣单位坐标（SabukW.txt 的 [defense] 段）。
//
// HP=0 表示未雇佣（原版据此跳过生成，Castle.pas:220-301）。
func defaultUnits() []storage.CastleUnit {
	units := []storage.CastleUnit{
		{Kind: storage.CastleMainDoor, Index: 0, Name: "SabukDoor", X: 672, Y: 330, HP: 10000, Opened: true},
		{Kind: storage.CastleWall, Index: 0, Name: "SabukW1", X: 624, Y: 278, HP: 5000},
		{Kind: storage.CastleWall, Index: 1, Name: "SabukW2", X: 627, Y: 278, HP: 5000},
		{Kind: storage.CastleWall, Index: 2, Name: "SabukW3", X: 634, Y: 271, HP: 5000},
	}
	// 12 个弓箭手位（官方坐标在 MainDoor 附近围一圈）。
	archerXY := [MaxArcher][2]int{
		{662, 333}, {664, 331}, {666, 329}, {676, 319}, {678, 317}, {681, 314},
		{628, 271}, {632, 267}, {670, 335}, {671, 334}, {675, 330}, {676, 329},
	}
	for i, xy := range archerXY {
		units = append(units, storage.CastleUnit{
			Kind: storage.CastleArcher, Index: i, Name: "弓箭手", X: xy[0], Y: xy[1],
		})
	}
	// 4 个守卫位（官方只填了前两个，另两个留在 0,0 = 未雇佣）。
	guardXY := [MaxGuard][2]int{{671, 334}, {675, 330}, {0, 0}, {0, 0}}
	for i, xy := range guardXY {
		units = append(units, storage.CastleUnit{
			Kind: storage.CastleGuard, Index: i, Name: "守卫", X: xy[0], Y: xy[1],
		})
	}
	return units
}

// LoadSabukConfig 读取 <castleDir>/<configDir>/SabukW.txt。
//
// ⚠️ 官方这个文件是 **GBK** 编码（与 Envir\market_def 下的 UTF-8 脚本不同），
// 这里统一转成 UTF-8 再解析。
//
// 文件是简单的 INI 风格（只有 [setup] / [defense] 两段），没有值转义。
// 解析不了的项**保留 DefaultRecord 的默认值**——官方文件本身带着
// NextWarDate 之类我们不关心的字段，缺字段不等于配置错误。
func LoadSabukConfig(castleDir, configDir string) (storage.Castle, error) {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	rec := DefaultRecord()
	rec.ConfigDir = configDir

	path := filepath.Join(castleDir, configDir, "SabukW.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		return rec, fmt.Errorf("castle: 读取 %s: %w", path, err)
	}
	text, err := decodeGBK(raw)
	if err != nil {
		return rec, fmt.Errorf("castle: 转码 %s: %w", path, err)
	}
	kv := parseINI(text)

	rec.Name = strOr(kv["CastleName"], rec.Name)
	rec.OwnGuild = kv["OwnGuild"] // 允许为空（无主）
	rec.TotalGold = int64Or(kv["TotalGold"], rec.TotalGold)
	rec.TodayIncome = int64Or(kv["TodayIncome"], rec.TodayIncome)
	rec.TechLevel = intOr(kv["TechLevel"], rec.TechLevel)
	rec.Power = intOr(kv["Power"], rec.Power)
	rec.ChangeDate = parseLooseTime(kv["ChangeDate"], rec.ChangeDate)
	rec.IncomeToday = parseLooseTime(kv["IncomeToday"], rec.IncomeToday)
	rec.WarDate = parseLooseTime(kv["WarDate"], rec.WarDate)

	rec.MapName = strOr(kv["CastleMap"], rec.MapName)
	rec.PalaceMap = strOr(kv["CastlePlaceMap"], rec.PalaceMap)
	rec.SecretMap = strOr(kv["CastleSecretMap"], rec.SecretMap)
	rec.HomeMap = strOr(kv["CastleHomeMap"], rec.HomeMap)
	rec.HomeX = intOr(kv["CastleHomeX"], rec.HomeX)
	rec.HomeY = intOr(kv["CastleHomeY"], rec.HomeY)
	rec.WarRangeX = intOr(kv["CastleWarRangeX"], rec.WarRangeX)
	rec.WarRangeY = intOr(kv["CastleWarRangeY"], rec.WarRangeY)
	rec.PalaceDoorX = intOr(kv["CastlePalaceDoorX"], rec.PalaceDoorX)
	rec.PalaceDoorY = intOr(kv["CastlePalaceDoorY"], rec.PalaceDoorY)

	rec.Units = parseUnits(kv, rec.Units)
	return rec, nil
}

// LoadAttackSabukWall 读取 <castleDir>/<configDir>/AttackSabukWall.txt，
// 把宣战队列合进 rec。
//
// 原版格式（Castle.pas:540-592）：每行 `<行会名>  "日期"`。
// 官方 1.76 存档里这个文件是空的（还没人宣战过），所以"文件不存在"
// 与"空文件"都要当作正常情况。
func LoadAttackSabukWall(castleDir, configDir string, rec storage.Castle) storage.Castle {
	if configDir == "" {
		configDir = DefaultConfigDir
	}
	path := filepath.Join(castleDir, configDir, "AttackSabukWall.txt")
	raw, err := os.ReadFile(path)
	if err != nil {
		return rec
	}
	text, err := decodeGBK(raw)
	if err != nil {
		log.Printf("castle: 转码 %s 失败: %v", path, err)
		return rec
	}

	var list []storage.CastleAttacker
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		// 原版用空格/制表符分隔，日期可能带引号。
		name, rest, _ := strings.Cut(line, " ")
		name = strings.TrimSpace(name)
		rest = strings.Trim(strings.TrimSpace(rest), `"`)
		if name == "" {
			continue
		}
		list = append(list, storage.CastleAttacker{
			GuildName:  name,
			AttackDate: parseLooseTime(rest, time.Time{}),
		})
	}
	rec.Attackers = list
	return rec
}

// wallKeyPrefix 是官方文件里三面墙的键前缀（LeftWall/CenterWall/RightWall）。
var wallKeyPrefix = [3]string{"LeftWall", "CenterWall", "RightWall"}

// parseUnits 从配置里取城门/墙/雇佣单位的坐标与血量。
//
// 以默认表为骨架（保证顺序与数量稳定），只覆盖文件里出现的项。
func parseUnits(kv map[string]string, base []storage.CastleUnit) []storage.CastleUnit {
	out := make([]storage.CastleUnit, len(base))
	copy(out, base)
	for i := range out {
		u := &out[i]
		switch u.Kind {
		case storage.CastleMainDoor:
			u.Name = strOr(kv["MainDoorName"], u.Name)
			u.X = intOr(kv["MainDoorX"], u.X)
			u.Y = intOr(kv["MainDoorY"], u.Y)
			u.HP = intOr(kv["MainDoorHP"], u.HP)
			u.Opened = intOr(kv["MainDoorOpen"], boolInt(u.Opened)) != 0
		case storage.CastleWall:
			p := wallKeyPrefix[u.Index]
			u.Name = strOr(kv[p+"Name"], u.Name)
			u.X = intOr(kv[p+"X"], u.X)
			u.Y = intOr(kv[p+"Y"], u.Y)
			u.HP = intOr(kv[p+"HP"], u.HP)
		case storage.CastleArcher:
			// 官方文件同时有 "弓箭卫士_N_*"（坐标）与 "Archer_N_*"（名字/HP）
			// 两套键：坐标取"弓箭卫士"，名字取 Archer。N 从 1 起。
			n := strconv.Itoa(u.Index + 1)
			u.X = intOr(kv["弓箭卫士_"+n+"_X"], u.X)
			u.Y = intOr(kv["弓箭卫士_"+n+"_Y"], u.Y)
			u.HP = intOr(kv["Archer_"+n+"_HP"], u.HP)
			u.Name = strOr(kv["Archer_"+n+"_Name"], u.Name)
		case storage.CastleGuard:
			n := strconv.Itoa(u.Index + 1)
			u.X = intOr(kv["卫士_"+n+"_X"], u.X)
			u.Y = intOr(kv["卫士_"+n+"_Y"], u.Y)
			u.HP = intOr(kv["Guard_"+n+"_HP"], u.HP)
			u.Name = strOr(kv["Guard_"+n+"_Name"], u.Name)
		}
	}
	return out
}

// ---------- 解析小工具 ----------

// parseINI 解析 INI 风格文本。
//
// 段名（[setup] / [defense]）直接忽略——这个文件里两段的键不重名，
// 我们也只用裸键。
//
// ⚠️ **重复键取首次出现的那个**，这不是随便选的：原版用 TIniFile 读
// （Castle.pas:339 `CastleConf.ReadInteger('Defense','MainDoorHP',2000)`），
// 底层是 TStringList.GetValue，它从索引 0 往后找、命中第一个就返回。
// 官方 SabukW.txt 里 MainDoorOpen/MainDoorHP 各出现两次
// （先 1/10000，后 0/0），所以原版读到的是**10000**；若按"后者覆盖"
// 就会读成城门已破的 0。
func parseINI(text string) map[string]string {
	out := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, dup := out[k]; dup {
			continue // 首次优先（TStringList.GetValue 语义）
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

func strOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intOr(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func int64Or(v string, def int64) int64 {
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// timeLayouts 是官方 SabukW.txt 里出现过的日期写法。
var timeLayouts = []string{
	"2006-1-2 15:04:05",
	"2006-1-2 3:04:05 PM",
	"2006-1-2 15:04",
	"2006-1-2",
	time.RFC3339,
}

// parseLooseTime 宽松解析时间：先试几种官方写法，再试 Delphi TDateTime。
//
// Delphi 的 TDateTime 是"1899-12-30 起的浮点天数"。官方文件里
// ChangeDate=1899-12-30 表示"从未换主"，换算后正好落在零值附近。
func parseLooseTime(v string, def time.Time) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	for _, layout := range timeLayouts {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t
		}
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f >= 1e6 {
		return def
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.Local)
	days := int(f)
	frac := f - float64(days)
	return base.AddDate(0, 0, days).Add(time.Duration(frac * 24 * float64(time.Hour)))
}

// decodeGBK 把 GBK 字节转成 UTF-8。
//
// 已经是合法 UTF-8 的输入原样返回——官方数据里两种编码混用
// （Envir\market_def 是 UTF-8，Castle\0\SabukW.txt 是 GBK），
// 直接按 GBK 解后者即可，但为了容错这里先探一下。
func decodeGBK(raw []byte) (string, error) {
	if isUTF8(raw) {
		return string(raw), nil
	}
	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// isUTF8 判断字节流是否是合法 UTF-8（含 ASCII）。
func isUTF8(b []byte) bool {
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			i++
		case c>>5 == 0b110:
			if i+1 >= len(b) || b[i+1]>>6 != 0b10 {
				return false
			}
			i += 2
		case c>>4 == 0b1110:
			if i+2 >= len(b) || b[i+1]>>6 != 0b10 || b[i+2]>>6 != 0b10 {
				return false
			}
			i += 3
		case c>>3 == 0b11110:
			if i+3 >= len(b) || b[i+1]>>6 != 0b10 || b[i+2]>>6 != 0b10 || b[i+3]>>6 != 0b10 {
				return false
			}
			i += 4
		default:
			return false
		}
	}
	return true
}

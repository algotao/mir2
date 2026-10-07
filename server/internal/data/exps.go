package data

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 官方经验表：Mir2-GeeM2/Exps.ini 的 [Exp] 段。
//
// ⚠️ 这张表**不在** !Setup.txt 里。`internal/entity/exp.go` 早先按 `[LevelExp]`
// 去 !Setup.txt 找、找不到就退回公式，并在注释里写"该配置文件缺失"——
// 实际是**找错了文件**（与坑 49「字段名猜错 = 没找到」同类）。
// 原版真正的加载点：M2Share.pas:5145-5155
//
//	LoadString := ExpConf.ReadString('Exp', 'Level' + IntToStr(i), '');
//	LoadInteger := Str_ToInt(LoadString, 0);
//	if LoadInteger = 0 then g_Config.dwNeedExps[i] := g_dwOldNeedExps[i]
//	else g_Config.dwNeedExps[i] := LoadInteger;
//
// 语义（关键，别当成累计值）：`LevelN` 是**从 N 级升到 N+1 级**所需的经验。
// 依据 GetExp（ObjBase.pas:1844-1852）：
//
//	Inc(m_Abil.Exp, dwExp);
//	if m_Abil.Exp >= m_Abil.MaxExp then        // MaxExp = GetLevelExp(m_Abil.Level)
//	  Dec(m_Abil.Exp, m_Abil.MaxExp);          // 升级时**减去本级需求**
//
// Exps.ini 里另有 [HeroExp]/[GamePetExp]/[MedicineExp]/[WineExp] 四个段
// （英雄/游戏宠物/药水/酒），**不是玩家经验**，只取 [Exp]。
// [Exp] 段里还混着 `LevelExpRate999=0` 这种键，必须按"Level 后面全是数字"筛掉。
func LoadExps(path string) ([]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	levels := make(map[int]uint64)
	maxLevel := 0
	inExp := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				inExp = false
				continue
			}
			// 段名比较不区分大小写（Delphi 的 TStringList 默认如此）。
			inExp = strings.EqualFold(strings.TrimSpace(line[1:end]), "Exp")
			continue
		}
		if !inExp {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		lv, ok := parseLevelKey(strings.TrimSpace(line[:eq]))
		if !ok {
			continue
		}
		// 空值（`Level98=`）当作缺省：entity 侧会退回公式。
		n, err := strconv.ParseUint(strings.TrimSpace(line[eq+1:]), 10, 64)
		if err != nil {
			continue
		}
		levels[lv] = n
		if lv > maxLevel {
			maxLevel = lv
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if maxLevel == 0 {
		return nil, fmt.Errorf("data: %s 里没解析出 [Exp] 段的 LevelN 键", path)
	}
	// 1-based：out[L] = 从 L 级升到 L+1 级所需经验（out[0] 未用）。
	out := make([]uint64, maxLevel+1)
	for lv, n := range levels {
		out[lv] = n
	}
	return out, nil
}

// parseLevelKey 解析 `Level<digits>`，返回等级。
//
// 特意要求"Level 之后**全是数字**"：这样 `LevelExpRate999=0` 不会被误当成
// 等级键（它也在 [Exp] 段里）。
func parseLevelKey(key string) (int, bool) {
	const prefix = "level"
	if len(key) <= len(prefix) || !strings.EqualFold(key[:len(prefix)], prefix) {
		return 0, false
	}
	digits := key[len(prefix):]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

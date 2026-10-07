package entity

import (
	"fmt"
	"strconv"
	"strings"
)

// SpawnPoint 是一个刷怪点。
//
// 对应原版 Envir\MonGen.txt 的一行（LocalDB.pas:1152-1286）：
//
//	<地图名> <X> <Y> "<怪物名>" <范围> <数量> <刷新时间(分)> [<集中刷新机率>]
//
// 注意：刷新时间单位是**分钟**（原版 ×60×1000 转毫秒）。
type SpawnPoint struct {
	MapName     string
	X, Y        int
	MonsterName string
	// Range 是以 (X,Y) 为中心的生成半径。
	Range int
	// Count 是该点同时存在的目标数量。
	Count int
	// RefreshMin 是刷新间隔（分钟）。
	RefreshMin int
}

// ParseMonGenLine 解析一行 MonGen 配置。
//
// 怪物名可能带引号且含空格，故不能用简单空格切分。
// 注释以 ';' 开头。
func ParseMonGenLine(line string) (SpawnPoint, error) {
	var sp SpawnPoint

	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ";") {
		return sp, fmt.Errorf("空行或注释")
	}

	fields := splitMonGenFields(line)
	if len(fields) < 7 {
		return sp, fmt.Errorf("字段不足: %q", line)
	}

	sp.MapName = fields[0]
	var err error
	if sp.X, err = strconv.Atoi(fields[1]); err != nil {
		return sp, fmt.Errorf("X 非法: %w", err)
	}
	if sp.Y, err = strconv.Atoi(fields[2]); err != nil {
		return sp, fmt.Errorf("Y 非法: %w", err)
	}
	sp.MonsterName = strings.Trim(fields[3], `"`)
	if sp.Range, err = strconv.Atoi(fields[4]); err != nil {
		return sp, fmt.Errorf("范围非法: %w", err)
	}
	if sp.Count, err = strconv.Atoi(fields[5]); err != nil {
		return sp, fmt.Errorf("数量非法: %w", err)
	}
	if sp.RefreshMin, err = strconv.Atoi(fields[6]); err != nil {
		return sp, fmt.Errorf("刷新时间非法: %w", err)
	}
	if sp.Count <= 0 {
		sp.Count = 1
	}
	if sp.RefreshMin <= 0 {
		sp.RefreshMin = 1
	}
	return sp, nil
}

// splitMonGenFields 切分 MonGen 行，支持引号内的空格。
func splitMonGenFields(line string) []string {
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
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

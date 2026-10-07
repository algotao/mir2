package data

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// 种子数据文件名（由 cmd/seedgen 从 OpenMir2 的 mir2_data.sql 生成）。
const (
	StdItemsFile = "stditems.json"
	MonstersFile = "monsters.json"
	MagicsFile   = "magics.json"
)

// Tables 聚合三张静态表。
type Tables struct {
	Items    *StdItemSet
	Monsters *MonsterSet
	Magics   *MagicSet
}

// LoadDir 从目录加载三张表。
//
// 缺失文件返回错误——静态数据是服务端启动的硬依赖，静默降级会导致后续
// 出现"物品全是 0"这类难以定位的问题。
func LoadDir(dir string) (*Tables, error) {
	items, err := loadStdItems(filepath.Join(dir, StdItemsFile))
	if err != nil {
		return nil, err
	}
	monsters, err := loadMonsters(filepath.Join(dir, MonstersFile))
	if err != nil {
		return nil, err
	}
	magics, err := loadMagics(filepath.Join(dir, MagicsFile))
	if err != nil {
		return nil, err
	}

	itemSet, err := NewStdItemSet(items)
	if err != nil {
		return nil, err
	}
	monSet, err := NewMonsterSet(monsters)
	if err != nil {
		return nil, err
	}
	magSet, err := NewMagicSet(magics)
	if err != nil {
		return nil, err
	}
	return &Tables{Items: itemSet, Monsters: monSet, Magics: magSet}, nil
}

// MustLoadDir 加载失败时 panic，用于 main 启动路径。
func MustLoadDir(dir string) *Tables {
	t, err := LoadDir(dir)
	if err != nil {
		panic("data: 加载静态数据失败: " + err.Error())
	}
	return t
}

func loadStdItems(path string) ([]*StdItem, error) {
	var v []*StdItem
	if err := readJSON(path, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func loadMonsters(path string) ([]*MonsterInfo, error) {
	var v []*MonsterInfo
	if err := readJSON(path, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func loadMagics(path string) ([]*MagicInfo, error) {
	var v []*MagicInfo
	if err := readJSON(path, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", path, err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("解析 %s: %w", path, err)
	}
	return nil
}

// WriteJSON 导出为带缩进的 JSON，供 seedgen 使用。
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

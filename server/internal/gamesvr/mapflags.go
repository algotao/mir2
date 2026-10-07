package gamesvr

import (
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/world"
)

// indexMapInfos 建"地图号 → mapinfo 记录"的索引。
//
// ⚠️ 键是**地图号**：`world.Map.Name` 存的就是地图号（显示名走 `MapManager.Name()`，
// 见 manager.go 的注释与 `initPVP` 里 `zones[mi.ID]` 的用法），所以按 `m.Name` 查得到。
func (s *Server) indexMapInfos(infos []*data.MapInfo) {
	m := make(map[string]*data.MapInfo, len(infos))
	for _, mi := range infos {
		m[mi.ID] = mi
	}
	s.data.mapInfoByID = m
}

// mapFlagOf 取某张地图的 mapinfo 标记；地图或记录缺失时返回 nil（调用方判 nil）。
//
// ⚠️ 2026-10-06 之前**没有任何消费方读这些标记**（解析层还只认 4 个关键字），
// 见 docs/gap-audit.md §3.2。现在按官方 `LocalDB.pas:560-760` 全表解析，
// 逐项消费的落点见各自的调用处。
func (s *Server) mapFlagOf(m *world.Map) *data.MapInfo {
	if m == nil || s.data.mapInfoByID == nil {
		return nil
	}
	return s.data.mapInfoByID[m.Name]
}

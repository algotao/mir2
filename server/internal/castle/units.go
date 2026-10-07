package castle

import (
	"fmt"
	"log"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/storage"
)

// UnitNames 解析单位在怪物库里的真实名字。
//
// ⚠️ 官方 Castle/0/SabukW.txt 写 `Guard_N_Name=守卫`，但 GEEM2 的 Monster 表
// 里实际叫 **"卫士"**。原版 Initialize（Castle.pas:220-301）拿 sName 去怪物库
// 查，查不到就静默生成不出来——所以按官方配置字面实现会得到 0 个守卫。
// 这里做一次名字回退，并保留原名以便日志对照。
var guardNameAliases = map[string]string{
	"守卫": "卫士", // SabukW.txt 的写法 → 怪物库实际名
}

// resolveUnitName 返回该单位在怪物库里应使用的名字。
func resolveUnitName(u storage.CastleUnit) string {
	if fix, ok := guardNameAliases[u.Name]; ok {
		return fix
	}
	return u.Name
}

// SpawnPlan 是生成一个城堡单位所需的全部信息。
//
// 由 internal/castle 产出（纯数据，不依赖实体层），再由 gamesvr 落成
// entity.Monster —— 这样 internal/castle 仍然不 import internal/entity。
type SpawnPlan struct {
	Kind storage.CastleUnitKind
	Idx  int
	// Name 是**怪物库里的真实名字**（已做别名回退）。
	Name string
	// ConfigName 是 SabukW.txt 里写的原名（仅用于日志对照）。
	ConfigName string
	X, Y       int
	HP         int
	Opened     bool
}

// SpawnableUnits 返回该城堡应当生成单位列表。
//
// 对应原版 Initialize（Castle.pas:220-301）：
//   - 城门与三面墙**总是**生成（HP 来自 SabukW.txt 的 *HP 键）
//   - 守卫/弓箭手只在 HP > 0（已雇佣）时生成
//
// names 用于校验怪物库里是否存在该名字（缺失则跳过并告警，
// 与原版"查不到就静默不生成"一致，但我们要让运维看见）。
func (c *Castle) SpawnableUnits(monsters *data.MonsterSet) ([]SpawnPlan, error) {
	var out []SpawnPlan
	var missing []string
	for _, u := range c.rec.Units {
		// 未雇佣的单位（HP=0）不生成：原版用 HP=0 表示"没雇"。
		// 城门/城墙的 HP 恒 > 0（官方给了 10000/5000），所以这条只筛雇佣单位。
		if (u.Kind == storage.CastleGuard || u.Kind == storage.CastleArcher) && u.HP <= 0 {
			continue
		}
		name := resolveUnitName(u)
		if monsters != nil && monsters.GetByName(name) == nil {
			missing = append(missing, fmt.Sprintf("%s(%s)", name, u.Name))
			continue
		}
		out = append(out, SpawnPlan{
			Kind: u.Kind, Idx: u.Index,
			Name: name, ConfigName: u.Name,
			X: u.X, Y: u.Y, HP: u.HP, Opened: u.Opened,
		})
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("怪物库里缺少 %d 个城堡单位模板: %v", len(missing), missing)
	}
	return out, nil
}

// logSpawnErr 打印 SpawnableUnits 的告警（不阻断启动）。
func logSpawnErr(err error) {
	if err != nil {
		log.Printf("castle: %v（这些单位本次不会生成）", err)
	}
}

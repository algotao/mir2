package castle

import (
	"fmt"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// 修门/修墙/雇佣的返回码。
//
// 原版这一层是"成不成功"的布尔（Castle.pas:1150-1219），失败原因由
// NPC 脚本层负责区分（OpenMir2 CastleOfficial.cs:358-406 只区分
// "钱不够"与其它）。这里把原因细分，便于 NPC 直接给玩家文案。
const (
	// RepairOK 成功。
	RepairOK = iota
	// RepairNotUnderWar 攻城战中不能修（原版直接 Exit）。
	RepairNotUnderWar
	// RepairAlreadyFull 已经是满血。
	RepairAlreadyFull
	// RepairTooSoon 被攻击不足免疫时长。
	RepairTooSoon
	// RepairNoGold 金币不足。
	RepairNoGold
	// RepairNoSuchUnit 没有该单位（编号越界或未生成）。
	RepairNoSuchUnit
	// RepairAlreadyHired 该位置已雇佣。
	RepairAlreadyHired
)

// repairCost 返回修缮/雇佣的费用。
func (c *Castle) repairCost(kind storage.CastleUnitKind) int64 {
	switch kind {
	case storage.CastleMainDoor:
		return c.cfg.RepairDoorPrice
	case storage.CastleWall:
		return c.cfg.RepairWallPrice
	case storage.CastleGuard:
		return c.cfg.HireGuardPrice
	case storage.CastleArcher:
		return c.cfg.HireArcherPrice
	}
	return 0
}

// unitOf 按种类+序号取单位配置。
func (c *Castle) unitOf(kind storage.CastleUnitKind, idx int) (storage.CastleUnit, bool) {
	for _, u := range c.rec.Units {
		if u.Kind == kind && u.Index == idx {
			return u, true
		}
	}
	return storage.CastleUnit{}, false
}

// RepairUnit 判定一次修门/修墙/雇佣请求，返回返回码与费用。
//
// 对应 RepairDoor / RepairWall / HireGuard / HireArcher
// （Castle.pas:1150-1219、OpenMir2 CastleOfficial.cs:264-406）。
//
// 原版顺序（以 RepairDoor 为准，2150-1219）：
//
//	if BaseObject=nil or m_boUnderWar or HP >= MaxHP then False
//	if (GetTickCount - m_dwStruckTick) > 60*1000 then HP := MaxHP
//
// ⚠️ 关键：**攻城战中一律不能修**（m_boUnderWar 是 Exit 条件），
// 所以守方在被打的时候没法临阵补墙——这是原版行为，照抄。
//
// unitHP/unitAlive 是该单位当前的血量与存活状态（由实体层传入）。
// hired 标记雇佣单位是否已生成（HP>0 且已生成）。
func (c *Castle) RepairUnit(kind storage.CastleUnitKind, idx int,
	unitHP, unitMaxHP int, alive bool, lastStruckAt time.Time,
	now time.Time, playerGold int64) (int, int64) {

	cost := c.repairCost(kind)
	if _, ok := c.unitOf(kind, idx); !ok {
		return RepairNoSuchUnit, 0
	}
	// 雇佣类：已生成就是已雇佣。
	if kind == storage.CastleGuard || kind == storage.CastleArcher {
		if alive && unitHP > 0 {
			return RepairAlreadyHired, 0
		}
	} else {
		// 修缮类：攻城期禁止 + 满血无需修。
		if c.underWar {
			return RepairNotUnderWar, 0
		}
		if alive && unitHP >= unitMaxHP {
			return RepairAlreadyFull, 0
		}
		// 被攻击后 60 秒免疫。
		//
		// 原版是 `(GetTickCount - m_dwStruckTick) > 60*1000` 才修
		//（Castle.pas:1158），即**恰好 60 秒仍不可修**。用 `<=` 而非 `<`
		// 才是原版语义——GetTickCount 不会恰好等于 60000ms，实践无差别，
		// 但边界要一致。
		if !lastStruckAt.IsZero() && now.Sub(lastStruckAt) <= c.cfg.RepairImmunity {
			return RepairTooSoon, 0
		}
	}
	if playerGold < cost {
		return RepairNoGold, cost
	}
	return RepairOK, cost
}

// RepairErrText 把返回码翻成中文文案。
//
// 参考 OpenMir2 CastleOfficial.cs:358-406 的分支。
func RepairErrText(code int) string {
	switch code {
	case RepairNotUnderWar:
		return "攻城战中无法修理"
	case RepairAlreadyFull:
		return "目前无需修理"
	case RepairTooSoon:
		return "刚刚受到攻击，暂时无法修理"
	case RepairNoGold:
		return "金币不足"
	case RepairAlreadyHired:
		return "该位置已经雇用了"
	case RepairNoSuchUnit:
		return "没有这个位置"
	}
	return ""
}

// String 是修缮请求的可读描述，用于日志。
func (c *Castle) RepairString(kind storage.CastleUnitKind, idx int) string {
	switch kind {
	case storage.CastleMainDoor:
		return "城门"
	case storage.CastleWall:
		return []string{"左墙", "中墙", "右墙"}[idx]
	case storage.CastleGuard:
		return fmt.Sprintf("守卫%d", idx+1)
	case storage.CastleArcher:
		return fmt.Sprintf("弓箭手%d", idx+1)
	}
	return "?"
}

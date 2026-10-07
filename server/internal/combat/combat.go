// Package combat 是**战斗数值判定**：命中/打空、怪物命中与敏捷、红毒受伤放大。
//
// 这些都是"输入数值 → 输出数值"的纯函数（只依赖 internal/entity），
// 被战斗、宠物、技能、怪物 AI 多处共用，所以从 gamesvr 提出来单独成包。
package combat

import (
	"time"

	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
)

// PoisonDamageArmor 红毒受伤倍率分子（!setup.txt:923，12 ⇒ ×1.2）。
const PoisonDamageArmor = 12

// MonsterHitPoint 是怪物的"命中点"（`m_btHitPoint`）。
//
// ⚠️ 原版字段是 Word（0..65535），但用到判定里会被**隐式截成 Byte**
// （`Random(目标.敏捷)` 那个参数是 Byte）⇒ 这里照 `delphi.ByteWord` 截断。
func MonsterHitPoint(m *entity.Monster) int {
	if m == nil || m.Info == nil {
		return 0
	}
	return delphi.ByteWord(m.Info.Hit)
}

// MonsterSpeedPoint 是怪物的"敏捷"（`m_btSpeedPoint`），同样按 Byte 截断。
func MonsterSpeedPoint(m *entity.Monster) int {
	if m == nil || m.Info == nil {
		return 0
	}
	return delphi.ByteWord(m.Info.Speed)
}

// Misses 判断这一击是否**打空**（ObjBase.pas:22241-22246）：
//
//	if 目标.命中点 > 0 then
//	  if (攻击者.命中 < Random(目标.敏捷)) then nPower := 0;
//
// 目标的"命中点"为 0 时不判（原版拿它当"目标是不是有效活物"的代理）。
// 抽签用 `delphi.Random`（半开区间），单测可注入。
func Misses(attackerHit, targetHitPoint, targetSpeed int) bool {
	if targetHitPoint <= 0 {
		return false
	}
	return attackerHit < delphi.Random(delphi.ByteWord(uint16(targetSpeed)))
}

// StruckMul 返回红毒（受伤放大）的伤害倍率；没中毒时恒为 1。
func StruckMul(o *entity.Object, now time.Time) float64 {
	if o == nil || !o.PoisonActive(entity.PoisonDamageArmor, now) {
		return 1
	}
	return float64(PoisonDamageArmor) / 10
}

// AdjustStruck 按红毒倍率放大伤害（原版用 Delphi 的 Round = 四舍五入）。
func AdjustStruck(o *entity.Object, dmg int, now time.Time) int {
	m := StruckMul(o, now)
	if m <= 1 {
		return dmg
	}
	return int(float64(dmg)*m + 0.5)
}

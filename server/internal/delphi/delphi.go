// Package delphi 复刻 Delphi 的**数值语义**。
//
// 原版（M2Server）的伤害/命中/技能公式大量依赖两条与 Go 不同的规则，混用就会得到
// "看起来差不多、实际差几倍"的结果：
//
//  1. `Random(n)` 取 **[0,n)**（半开区间），`Random(0)` 就是 0（不报错）；
//     而业务代码里"某技能 DefPower == DefMaxPower"是常态 ⇒ `Random(0)` 天天走到。
//  2. `Round(x)` 是**银行家舍入**（.5 往偶数靠，FISTTP / x87 默认），
//     不是 Go `math.Round` 的 half-away-from-zero。
//
// 这两条在游戏服里被 10 多个文件用（技能威力、命中/敏捷、中毒、宠物、装备…），
// 所以单独成包，而不是塞进某一个领域包。
package delphi

import (
	"math"
	"math/rand/v2"
)

// RandN 是 Random 的随机源（签名同 math/rand 的 IntN：返回 [0,n)）。
//
// ⚠️ 做成包级变量而不是直接调 rand.IntN，是为了**单测能注入确定性序列** ——
// 那些公式的分支（取整、Random(0)、区间端点）全靠固定输入才验得动。
var RandN = rand.IntN

// Random 复刻 Delphi 的 Random(n)：返回 [0,n) 的整数。
//
// n <= 0 时返回 0：Delphi 的 `Random(0)` 就是 0（不报错）。
func Random(n int) int {
	if n <= 0 {
		return 0
	}
	return RandN(n)
}

// Round 复刻 Delphi 的 `Round()`：银行家舍入（.5 往偶数靠）。
func Round(v float64) int { return int(math.RoundToEven(v)) }

// ByteWord 复刻 Delphi "Word 赋给 Byte" 的截断（原版到处都是这种隐式转换，
// 例如怪物的 hit/speed 是 Word，用到判定里会被截成 Byte）。
func ByteWord(v uint16) int { return int(v & 0xFF) }

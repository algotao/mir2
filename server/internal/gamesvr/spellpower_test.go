package gamesvr

import (
	"github.com/algotao/mir2/server/internal/delphi"
)

// ⚠️ Delphi 语义（Random 半开区间 / Round 银行家舍入）的实现与单测都在
// internal/delphi —— 它们不属于技能威力这一块。

// packMM 把 MinMax 打成原版的 Word（Lo=下限，Hi=上限）。
func packMM(lo, hi uint16) uint32 { return uint32(lo) | uint32(hi)<<16 }

// withFixedRnd 把包级随机源换成确定性实现，返回还原函数。
//
// 这几个公式的分支（银行家舍入、Random(0)、半开/闭区间端点）全靠固定输入
// 才验得动，所以随机源必须是可注入的。
func withFixedRnd(f func(int) int) func() {
	old := delphi.RandN
	delphi.RandN = f
	return func() { delphi.RandN = old }
}

// fixedZero 让所有 Random(n) 返回 0（官方数据里 DefPower==DefMaxPower 时就是这样）。
func fixedZero(int) int { return 0 }

// fixedMax 让 Random(n) 返回 n-1，即"总取到半开区间的上界"。
func fixedMax(n int) int { return n - 1 }

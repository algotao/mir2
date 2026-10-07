// Package tscale 提供全局时间缩放，用于**加速端到端回归**。
//
// 语义：scale=N 表示"游戏内时间流速是现实的 N 倍"，因此
//
//	D(400ms) == 40ms   （scale=10）
//
// 也就是所有"等待/持续/间隔"统一除以 scale：移动限流、怪物 AI tick、
// buff 持续时间、回复周期、施法冷却都跟着变快。
//
// ⚠️ 两端必须用同一个 scale：服务端限流缩了、客户端的按键节奏没缩，
// 就会大量丢步（表现为"客户端一直追不上怪"）。mir2cli 也有同名参数，
// e2e 脚本负责把同一个值传给两边。
//
// ⚠️ 不要用它缩放**网络超时**（读超时、写缓冲等待）：那是 I/O 边界，
// 与游戏内时间无关，缩了会在机器负载高时误判断线。
package tscale

import (
	"sync/atomic"
	"time"
)

// scaleN 以 1e6 倍存储（避免浮点原子操作）。
var scaleN atomic.Int64

func init() { scaleN.Store(1_000_000) }

// Set 设置倍速；<=0 视为 1（正常速度）。
func Set(f float64) {
	if f <= 0 {
		f = 1
	}
	scaleN.Store(int64(f * 1e6))
}

// Get 返回当前倍速。
func Get() float64 { return float64(scaleN.Load()) / 1e6 }

// D 把"游戏内时长"换算成真实等待时长。
//
// 结果不低于 1ms：倍速极高时也不至于变成 0 而导致忙等。
func D(d time.Duration) time.Duration {
	s := Get()
	if s <= 1 {
		return d
	}
	out := time.Duration(float64(d) / s)
	if out < time.Millisecond {
		return time.Millisecond
	}
	return out
}

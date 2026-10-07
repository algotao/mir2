package delphi

import "testing"

// fixedMax 让 Random(n) 返回 n-1，即"总取到半开区间的上界"。
func fixedMax(n int) int { return n - 1 }

// withFixedRnd 把随机源换成确定性实现，返回还原函数。
func withFixedRnd(f func(int) int) func() {
	old := RandN
	RandN = f
	return func() { RandN = old }
}

func TestRandomZeroRange(t *testing.T) {
	// Delphi 的 Random(0) = 0（不报错）。官方数据里一大半技能
	// DefPower == DefMaxPower，天天走到 Random(0)；负数也不该 panic。
	defer withFixedRnd(fixedMax)()
	for _, n := range []int{0, -1, -100} {
		if got := Random(n); got != 0 {
			t.Errorf("Random(%d) = %d, 期望 0", n, got)
		}
	}
	if got := Random(1); got != 0 {
		t.Errorf("Random(1) = %d, 期望 0（[0,1) 只有 0）", got)
	}
	if got := Random(5); got != 4 {
		t.Errorf("Random(5) = %d, 期望 4（[0,5) 的上界）", got)
	}
}

func TestRoundIsBankers(t *testing.T) {
	// Delphi 的 Round 是**银行家舍入**（.5 往偶数靠，FISTTP / x87 默认）。
	// 用 math.Round（half away from zero）会在这些点全部算错。
	cases := []struct{ in, want float64 }{
		{0.5, 0}, {1.5, 2}, {2.5, 2}, {3.5, 4}, {-0.5, 0}, {-1.5, -2}, {2.4, 2}, {2.6, 3},
	}
	for _, c := range cases {
		if got := float64(Round(c.in)); got != c.want {
			t.Errorf("Round(%v) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

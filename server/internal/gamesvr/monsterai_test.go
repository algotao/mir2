package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/entity"
)

// TestAnimalFlees 动物"逃不逃跑"：**29/30 的鹿会反击**（用户 2026-10-09 第 6 条）。
//
// 官方 `UsrEngn.pas:1841-1852`：鹿只有 `Random(30) = 0` 才生成成只会逃跑的
// `TChickenDeer`，其余是普通 `TMonster`（会打人）；鸡那一档一律逃跑。
func TestAnimalFlees(t *testing.T) {
	const deer uint16 = 52
	const chicken uint16 = entity.RaceChicken
	// 鸡：一律逃跑
	for _, id := range []uint32{1, 29, 30, 31, 1000} {
		if !animalFlees(chicken, id) {
			t.Errorf("鸡（id=%d）应该只会逃跑", id)
		}
	}
	// 鹿：只有 id%30==0 那批是逃跑型；其余（29/30）要能反击
	flee, fight := 0, 0
	for id := uint32(1); id <= 300; id++ {
		if animalFlees(deer, id) {
			flee++
		} else {
			fight++
		}
	}
	if flee != 10 || fight != 290 {
		t.Errorf("300 只鹿里逃跑型 %d / 会反击 %d，应为 10 / 290（1/30）", flee, fight)
	}
	if animalFlees(deer, 7) {
		t.Error("id=7 的鹿该是普通怪（会反击）—— 这正是用户看到的")
	}
}

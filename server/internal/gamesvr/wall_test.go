package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// wallTestPlayer 造一个只有魔攻的法师角色（wallDur 只读 Char，Obj 可以为 nil）。
func wallTestPlayer(mcMin, mcMax uint32) *Player {
	return &Player{
		Char: &storage.Character{Data: &pb.CharacterData{
			Job:  1,
			Abil: &pb.Ability{Mc: &pb.MinMax{Min: mcMin, Max: mcMax}},
		}},
	}
}

// wallMagic 是官方火墙(22) 的数据行：def 3/3、power 3/3。
func wallMagic() *data.MagicInfo {
	return &data.MagicInfo{MagicID: 22, DefPower: 3, DefMaxPower: 3, Power: 3, MaxPower: 3}
}

func TestWallDur(t *testing.T) {
	// 原版 Magic.pas:505：GetPower(10) + (Word(GetRPow(MC)) shr 1) 秒
	//
	//	GetPower(10) = ROUND(10/4*(技能等级+1)) + (3 + Random(0))
	//	            = 0/1/2/3 级 → 2+3 / 5+3 / 8+3 / 10+3 = 5 / 8 / 11 / 13 秒
	//
	// ⚠️ 之前这里断言的是"固定 10 秒 + MC 上限/2、与等级无关"，那是我们自己
	// 简化出来的，与原版不符（详见 wallDur 的注释）。
	defer withFixedRnd(fixedZero)()
	for _, c := range []struct {
		level uint32
		want  int
	}{{0, 5}, {1, 8}, {2, 11}, {3, 13}} {
		if got := int(wallDur(wallMagic(), c.level, wallTestPlayer(0, 0)) / time.Second); got != c.want {
			t.Errorf("技能 %d 级 + 无魔攻: 时长 = %d 秒, 期望 %d", c.level, got, c.want)
		}
	}
	if got := wallDur(wallMagic(), 0, nil); got != 5*time.Second {
		t.Errorf("wallDur(nil 角色) = %v, 期望 5s", got)
	}
}

func TestWallDurUsesGetRPowNotHi(t *testing.T) {
	// MC 那半是 **GetRPow(MC) shr 1**：闭区间随机后整除 2。
	// 高低相等时 GetRPow 直接返回该值（不摇随机），所以这组可确定。
	defer withFixedRnd(fixedMax)()
	for _, c := range []struct {
		name       string
		mc         uint16
		wantSecond int
	}{
		{"MC=20 → +10 秒", 20, 23},
		{"MC=9 → +4 秒（整除）", 9, 17},
		{"MC=1 → +0 秒", 1, 13},
	} {
		p := wallTestPlayer(uint32(c.mc), uint32(c.mc))
		if got := int(wallDur(wallMagic(), 3, p) / time.Second); got != c.wantSecond {
			t.Errorf("%s: 时长 = %d 秒, 期望 %d", c.name, got, c.wantSecond)
		}
	}
}

func TestPlaceWallsCross(t *testing.T) {
	m := world.Generate("walltest", 20, 20, false)
	walls := map[wallKey]*Wall{}
	now := time.Now()

	// 5 格十字：上/左/中/右/下（中心 (10,10)）
	got := placeWalls(walls, m, 42, 10, 10, 7, now, 10*time.Second, now, nil)
	if got != 5 {
		t.Fatalf("铺下 %d 格, 期望 5", got)
	}
	want := []wallKey{
		{m, 10, 9}, {m, 9, 10}, {m, 10, 10}, {m, 11, 10}, {m, 10, 11},
	}
	for _, k := range want {
		w, ok := walls[k]
		if !ok {
			t.Fatalf("格子 %v 上没有墙", k)
		}
		if w.OwnerID != 42 || w.Damage != 7 {
			t.Errorf("格子 %v: owner=%d dmg=%d, 期望 42/7", k, w.OwnerID, w.Damage)
		}
	}
	// 对角格不该有墙（原版不是 3×3）
	for _, k := range []wallKey{{m, 9, 9}, {m, 11, 11}, {m, 10, 12}} {
		if walls[k] != nil {
			t.Errorf("格子 %v 不该有墙（十字不是 3×3）", k)
		}
	}
}

func TestPlaceWallsNoOverlapNoOverwrite(t *testing.T) {
	m := world.Generate("walltest", 20, 20, false)
	walls := map[wallKey]*Wall{}
	now := time.Now()

	if got := placeWalls(walls, m, 1, 10, 10, 5, now, time.Second, now, nil); got != 5 {
		t.Fatalf("首次铺墙 %d 格, 期望 5", got)
	}
	// 同一格再来一次：GetEvent 已存在 → 5 格全部跳过（不覆盖、不叠加）
	if got := placeWalls(walls, m, 2, 10, 10, 9, now, time.Second, now, nil); got != 0 {
		t.Errorf("重复铺墙 %d 格, 期望 0（原版不覆盖已有事件）", got)
	}
	if w := walls[wallKey{m, 10, 10}]; w.OwnerID != 1 || w.Damage != 5 {
		t.Errorf("已有事件被覆盖了: owner=%d dmg=%d, 期望仍是 1/5", w.OwnerID, w.Damage)
	}

	// 部分重叠：第二批中心 (11,10) 的十字 = (11,9)(10,10)(11,10)(12,10)(11,11)，
	// 其中 (10,10) 与 (11,10) 已被第一批占了 → 只补 3 格。
	if got := placeWalls(walls, m, 3, 11, 10, 4, now, time.Second, now, nil); got != 3 {
		t.Errorf("部分重叠铺墙 %d 格, 期望 3", got)
	}
}

func TestPlaceWallsSkipsOutOfBounds(t *testing.T) {
	// 中心贴着上边界 (10,0)：十字 = (10,-1)(9,0)(10,0)(11,0)(10,1)，
	// 只有 (10,-1) 越界。原版不判边界（越界事件仍留在列表里、仍占格），
	// 我们显式跳过，否则它会永久占住一个打不到人的格子。
	m := world.Generate("walltest", 20, 20, false)
	walls := map[wallKey]*Wall{}
	now := time.Now()

	got := placeWalls(walls, m, 1, 10, 0, 3, now, time.Second, now, nil)
	if got != 4 {
		t.Fatalf("贴边铺墙 %d 格, 期望 4（仅 (10,-1) 越界）", got)
	}
	if walls[wallKey{m, 10, -1}] != nil {
		t.Error("越界格不应留下墙")
	}
	for _, k := range []wallKey{{m, 9, 0}, {m, 10, 0}, {m, 11, 0}, {m, 10, 1}} {
		if walls[k] == nil {
			t.Errorf("界内格 %v 应该有墙", k)
		}
	}
}

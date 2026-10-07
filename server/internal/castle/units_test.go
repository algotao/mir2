package castle

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/storage"
)

// fakeMonsters 造一个只含指定名字的怪物表。
func fakeMonsters(names ...string) *data.MonsterSet {
	list := make([]*data.MonsterInfo, 0, len(names))
	for i, n := range names {
		// 索引必须从 1 起连续（data.NewMonsterSet 会校验）。
		list = append(list, &data.MonsterInfo{Index: int32(i + 1), Name: n, HP: 100, Race: 111})
	}
	set, err := data.NewMonsterSet(list)
	if err != nil {
		panic(err)
	}
	return set
}

func TestSpawnableUnitsSkipsUnhired(t *testing.T) {
	c := New(DefaultConfig(), DefaultRecord())
	// 官方存档里守卫/弓箭手 HP=0（未雇佣），应被跳过；城门与三面墙照常。
	plans, err := c.SpawnableUnits(fakeMonsters(
		"SabukDoor", "SabukW1", "SabukW2", "SabukW3"))
	if err != nil {
		t.Fatalf("不该有缺失: %v", err)
	}
	if len(plans) != 4 {
		t.Fatalf("应生成 4 个（1 城门 + 3 墙），实际 %d: %+v", len(plans), plans)
	}
	kinds := map[storage.CastleUnitKind]int{}
	for _, p := range plans {
		kinds[p.Kind]++
	}
	if kinds[storage.CastleMainDoor] != 1 {
		t.Errorf("城门数 = %d, 期望 1", kinds[storage.CastleMainDoor])
	}
	if kinds[storage.CastleWall] != 3 {
		t.Errorf("城墙数 = %d, 期望 3", kinds[storage.CastleWall])
	}
	if kinds[storage.CastleGuard] != 0 || kinds[storage.CastleArcher] != 0 {
		t.Errorf("未雇佣的守卫/弓手不该生成，得到 %+v", plans)
	}
}

// TestGuardNameAlias 守住官方配置的错名。
//
// SabukW.txt 写 Guard_N_Name=守卫，怪物库里实际叫"卫士"。
// 不做回退的话，按配置名去查会查不到、原版就生成不出守卫。
func TestGuardNameAlias(t *testing.T) {
	rec := DefaultRecord()
	// 模拟"已雇佣"：把第一个守卫的 HP 与名字设成官方配置里的值。
	for i := range rec.Units {
		if rec.Units[i].Kind == storage.CastleGuard {
			rec.Units[i].Name = "守卫" // SabukW.txt 的写法
			rec.Units[i].HP = 9999
			break
		}
	}
	c := New(DefaultConfig(), rec)
	plans, err := c.SpawnableUnits(fakeMonsters("SabukDoor", "SabukW1", "SabukW2", "SabukW3", "卫士"))
	if err != nil {
		t.Fatalf("别名回退后不该报缺失: %v", err)
	}
	var found bool
	for _, p := range plans {
		if p.Kind == storage.CastleGuard {
			found = true
			if p.Name != "卫士" {
				t.Errorf("守卫的真实名字 = %q, 期望回退成 卫士", p.Name)
			}
			if p.ConfigName != "守卫" {
				t.Errorf("ConfigName 应保留配置原名 守卫，实际 %q", p.ConfigName)
			}
		}
	}
	if !found {
		t.Error("已雇佣的守卫应出现在生成计划里")
	}
}

// TestSpawnableUnitsReportsMissing 怪物库缺模板时要能报出来。
func TestSpawnableUnitsReportsMissing(t *testing.T) {
	c := New(DefaultConfig(), DefaultRecord())
	// 只给城门，墙全缺。
	_, err := c.SpawnableUnits(fakeMonsters("SabukDoor"))
	if err == nil {
		t.Fatal("缺模板时应返回错误")
	}
}

func TestResolveUnitName(t *testing.T) {
	if got := resolveUnitName(storage.CastleUnit{Name: "守卫"}); got != "卫士" {
		t.Errorf("守卫 -> %q, 期望 卫士", got)
	}
	if got := resolveUnitName(storage.CastleUnit{Name: "SabukDoor"}); got != "SabukDoor" {
		t.Errorf("SabukDoor 应原样返回，实际 %q", got)
	}
}

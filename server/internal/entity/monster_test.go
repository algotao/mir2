package entity

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/world"
)

func testMonsterInfo(name string, hp uint32, raceImg, appr uint16) *data.MonsterInfo {
	return &data.MonsterInfo{
		Index: 1, Name: name, HP: hp, RaceImg: raceImg, Appr: appr,
		WalkSpeed: 250, Level: 5, Exp: 10,
	}
}

// TestMonsterFeature 校验怪物外观位域：低 16 位 RaceImg，高 16 位 Appr。
func TestMonsterFeature(t *testing.T) {
	info := testMonsterInfo("鸡", 5, 11, 160)
	m := NewMonster(1, info, nil, 0, 0)

	if got := proto.FeatureRace(m.FeatureBits()); got != 11 {
		t.Errorf("Race = %d, want 11", got)
	}
	if got := proto.FeatureDress(m.FeatureBits()); got != 0 {
		t.Errorf("Dress = %d, want 0", got)
	}
	if m.HP != 5 || m.MaxHP != 5 {
		t.Errorf("HP = %d/%d, want 5/5", m.HP, m.MaxHP)
	}
	if m.IsDead() {
		t.Error("新怪物不应是死亡状态")
	}
}

// TestMonsterDamage 校验伤害与致死判定。
func TestMonsterDamage(t *testing.T) {
	m := NewMonster(1, testMonsterInfo("鸡", 10, 11, 160), nil, 0, 0)

	if m.Damage(3) {
		t.Error("扣 3 不应致死")
	}
	if m.HP != 7 {
		t.Errorf("HP = %d, want 7", m.HP)
	}
	if !m.Damage(7) {
		t.Error("再扣 7 应致死")
	}
	if m.HP != 0 || !m.IsDead() {
		t.Errorf("死亡状态错误: HP=%d dead=%v", m.HP, m.IsDead())
	}
	// 已死不应再受伤
	if m.Damage(1) {
		t.Error("已死怪物不应再次致死")
	}
}

// TestMonsterWonder 校验游荡不会走出地图。
func TestMonsterWonder(t *testing.T) {
	// 10x10，四周阻挡，内部 8x8 可走
	m := world.Generate("t", 10, 10, true)
	mon := NewMonster(1, testMonsterInfo("鸡", 5, 11, 160), m, 5, 5)

	for i := 0; i < 50; i++ {
		mon.Wonder()
		if mon.PosX() < 1 || mon.PosX() > 8 || mon.PosY() < 1 || mon.PosY() > 8 {
			t.Fatalf("游荡走出可走区域: (%d,%d)", mon.PosX(), mon.PosY())
		}
	}
}

// TestMonsterStepToward 校验朝目标贪心靠近。
func TestMonsterStepToward(t *testing.T) {
	m := world.Generate("t", 20, 20, true)
	mon := NewMonster(1, testMonsterInfo("鸡", 5, 11, 160), m, 5, 5)

	before := mon.Distance(15, 5)
	mon.StepToward(15, 5)
	after := mon.Distance(15, 5)
	if after >= before {
		t.Errorf("未靠近目标: 距离 %d → %d", before, after)
	}
}

// TestMonsterActInterval 校验移动间隔限制。
func TestMonsterActInterval(t *testing.T) {
	mon := NewMonster(1, testMonsterInfo("鸡", 5, 11, 160), nil, 0, 0)
	now := time.Now()

	if !mon.CanAct(now.Add(time.Second)) {
		t.Error("经过足够时间应可行动")
	}
	mon.MarkActed(now)
	if mon.CanAct(now.Add(50 * time.Millisecond)) {
		t.Error("间隔不足不应可行动")
	}
	if !mon.CanAct(now.Add(300 * time.Millisecond)) {
		t.Error("超过最小间隔应可行动")
	}
}

// TestMoveIntervalFromSpeed 校验速度下限（原版 WALK_SPD 下限 200ms）。
func TestMoveIntervalFromSpeed(t *testing.T) {
	cases := map[uint16]time.Duration{
		0:    200 * time.Millisecond,
		50:   200 * time.Millisecond, // 低于下限，钳制
		250:  250 * time.Millisecond,
		1000: 1000 * time.Millisecond,
	}
	for speed, want := range cases {
		if got := moveIntervalFromSpeed(speed); got != want {
			t.Errorf("moveIntervalFromSpeed(%d) = %v, want %v", speed, got, want)
		}
	}
}

// TestParseMonGenLine 校验 MonGen 配置解析。
//
// 关键点：怪物名带引号且可能含空格，刷新时间单位是分钟。
func TestParseMonGenLine(t *testing.T) {
	cases := []struct {
		line string
		want SpawnPoint
	}{
		{`0 100 100 "鸡" 5 10 5`, SpawnPoint{"0", 100, 100, "鸡", 5, 10, 5}},
		{`3 289 618 "半兽人" 10 5 3`, SpawnPoint{"3", 289, 618, "半兽人", 10, 5, 3}},
		{`0 1 1 "多 钩 猫" 1 1 1`, SpawnPoint{"0", 1, 1, "多 钩 猫", 1, 1, 1}},
	}
	for _, c := range cases {
		got, err := ParseMonGenLine(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if got != c.want {
			t.Errorf("%q → %+v, want %+v", c.line, got, c.want)
		}
	}

	// 异常输入
	bad := []string{"; 注释", "", "0 100", "0 x 100 \"鸡\" 5 10 5"}
	for _, b := range bad {
		if _, err := ParseMonGenLine(b); err == nil {
			t.Errorf("%q 应解析失败", b)
		}
	}
}

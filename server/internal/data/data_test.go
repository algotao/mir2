package data

import (
	"path/filepath"
	"testing"
)

// seedDir 指向 cmd/seedgen 生成的种子数据目录。
var seedDir = filepath.Join("..", "..", "data")

// TestLoadDir 校验三张表能加载，且怪物模板使用 GeeM2 社区包数据。
func TestLoadDir(t *testing.T) {
	tb, err := LoadDir(seedDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	// ⚠️ 物品数不写死：物品表已切换为 GeeM2 官方 1.76（686 条），
	// 之前注释里的 1000 来自 OpenMir2。这里只保证非空。
	if got := tb.Items.Len(); got == 0 {
		t.Error("物品表为空")
	}
	// GeeM2 社区包含 378 个怪物模板，城堡模板由 seedgen 追加。
	if got := tb.Monsters.Len(); got != 382 {
		t.Errorf("怪物数 = %d, 期望 GeeM2 的 378 个模板 + 4 个城堡模板", got)
	}
	for i, m := range tb.Monsters.All() {
		if m.Index != int32(i)+1 {
			t.Fatalf("怪物[%d] Index=%d，期望 %d", i, m.Index, i+1)
		}
	}
	if got := tb.Magics.Len(); got != 33 {
		t.Errorf("经典技能数 = %d, want 33", got)
	}
}

// TestCastleMonstersPresent 守住城堡实体依赖的怪物模板。
//
// 城墙/城门是可被打的实体（SabukDoor + SabukW1..3），它们不在 OpenMir2 的
// 705 条里，是从 GeeM2 的 GEEM2.db.sql 补进来的——缺了它们城堡 P1 无法启动。
// 血量要与官方 Castle/0/SabukW.txt 对得上（城门 10000、城墙 5000）。
func TestCastleMonstersPresent(t *testing.T) {
	tb := mustLoad(t)
	cases := []struct {
		name string
		race uint16
		hp   uint32
	}{
		{"SabukDoor", 110, 10000},
		{"SabukW1", 111, 5000},
		{"SabukW2", 111, 5000},
		{"SabukW3", 111, 5000},
	}
	for _, c := range cases {
		m := tb.Monsters.GetByName(c.name)
		if m == nil {
			t.Errorf("怪物表缺少 %s（城堡实体依赖它）", c.name)
			continue
		}
		if m.Race != c.race {
			t.Errorf("%s 的 race = %d, 期望 %d", c.name, m.Race, c.race)
		}
		if m.HP != c.hp {
			t.Errorf("%s 的 HP = %d, 期望 %d（要与 SabukW.txt 一致）",
				c.name, m.HP, c.hp)
		}
	}
	// ⚠️ 官方 SabukW.txt 写 Guard_N_Name=守卫，但怪物库里实际叫"卫士"。
	// 守卫是"可雇佣"单位（HP=0 表示未雇佣），血量高得离谱是官方设定。
	if tb.Monsters.GetByName("卫士") == nil {
		t.Error("怪物表缺少 卫士（官方 SabukW.txt 误写成 守卫）")
	}
}

// TestItemIndexContinuity 物品索引必须连续且从 1 开始。
// 对应 Delphi LocalDB.pas:313-322 的约束：IDX 必须等于当前 Count。
func TestItemIndexContinuity(t *testing.T) {
	tb := mustLoad(t)
	for i, it := range tb.Items.All() {
		if it.Index != int32(i)+1 {
			t.Fatalf("第 %d 项 Index=%d, want %d", i, it.Index, i+1)
		}
	}
	// ⚠️ 不要硬编码物品总数：物品表来源可切换（OpenMir2 1000 条 /
	// GeeM2 官方 1.76 的 686 条），写死会让换数据源时测试误报。
	n := tb.Items.Len()
	if n == 0 {
		t.Fatal("物品表为空")
	}
	if tb.Items.Get(0) == nil || tb.Items.Get(n-1) == nil {
		t.Fatalf("下标 0/%d 应可取到物品", n-1)
	}
	if tb.Items.Get(n) != nil {
		t.Fatal("越界下标应返回 nil")
	}
}

// TestClassicMagicIDs 固化"经典技能与 Delphi SKILL_* 常量对齐"这一事实。
//
// 对照 /data/git/MIR2/GameOfMir/Common/Grobal2.pas:1268-1300。
// 这是复古版技能系统的基线，一旦错位所有技能都会放错。
func TestClassicMagicIDs(t *testing.T) {
	want := []string{
		"火球术", "治愈术", "基本剑术", "精神力战法", "大火球", "施毒术",
		"攻杀剑术", "抗拒火环", "地狱火", "疾光电影", "雷电术", "刺杀剑术",
		"灵魂火符", "幽灵盾", "神圣战甲术", "困魔咒", "召唤骷髅", "隐身术",
		"集体隐身术", "诱惑之光", "瞬息移动", "火墙", "爆裂火焰", "地狱雷光",
		"半月弯刀", "烈火剑法", "野蛮冲撞", "心灵启示", "群体治疗术", "召唤神兽",
		"魔法盾", "圣言术", "冰咆哮",
	}
	tb := mustLoad(t)
	for id, name := range want {
		m := tb.Magics.GetByID(uint16(id) + 1)
		if m == nil {
			t.Fatalf("技能号 %d 缺失", id+1)
		}
		if m.Name != name {
			t.Errorf("技能号 %d = %q, want %q", id+1, m.Name, name)
		}
	}
	// 34 之后是 1.8+ 扩展，复古版不应存在
	if tb.Magics.GetByID(34) != nil {
		t.Error("技能号 34（1.8+ 扩展）不应出现在经典表中")
	}
}

// TestMonsterSamples 用已知怪物校验字段映射未错位。
func TestMonsterSamples(t *testing.T) {
	tb := mustLoad(t)
	cases := []struct {
		name  string
		level uint16
	}{
		{"鸡", 2},
		{"鹿", 12},
	}
	for _, c := range cases {
		m := tb.Monsters.GetByName(c.name)
		if m == nil {
			t.Fatalf("怪物 %q 未找到", c.name)
		}
		if m.Level != c.level {
			t.Errorf("%s 等级 = %d, want %d", c.name, m.Level, c.level)
		}
	}
}

// TestMinMaxPack 校验 Min/Max ↔ DWord 打包互逆。
// 原版把下限放 LoWord、上限放 HiWord（Common/Grobal2.pas:734-753）。
func TestMinMaxPack(t *testing.T) {
	m := MinMax{Min: 0x1234, Max: 0x5678}
	if got := m.Pack(); got != 0x56781234 {
		t.Fatalf("Pack = %#x, want 0x56781234", got)
	}
	back := UnpackMinMax(0x56781234)
	if back != m {
		t.Fatalf("UnpackMinMax = %+v, want %+v", back, m)
	}
}

// TestItemTypeOf 校验 StdMode → 物品种类映射（LocalDB.pas:313-322）。
func TestItemTypeOf(t *testing.T) {
	cases := map[uint8]ItemType{
		5:  ItemTypeWeapon,    // 武器
		6:  ItemTypeWeapon,    // 武器
		10: ItemTypeDress,     // 衣服
		11: ItemTypeDress,     // 衣服
		19: ItemTypeAccessory, // 首饰
		26: ItemTypeAccessory,
		52: ItemTypeAccessory,
		63: ItemTypeAccessory,
		0:  ItemTypeOther,
		3:  ItemTypeOther,
	}
	for stdMode, want := range cases {
		if got := ItemTypeOf(stdMode); got != want {
			t.Errorf("ItemTypeOf(%d) = %d, want %d", stdMode, got, want)
		}
	}
}

// TestMagicIDUnique 经典技能号必须唯一（否则 byID 索引会丢数据）。
func TestMagicIDUnique(t *testing.T) {
	tb := mustLoad(t)
	seen := make(map[uint16]int, tb.Magics.Len())
	for _, m := range tb.Magics.All() {
		seen[m.MagicID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("技能号 %d 出现 %d 次", id, n)
		}
	}
	if len(seen) != tb.Magics.Len() {
		t.Errorf("唯一技能号 %d != 记录数 %d", len(seen), tb.Magics.Len())
	}
}

func mustLoad(t *testing.T) *Tables {
	t.Helper()
	tb, err := LoadDir(seedDir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return tb
}

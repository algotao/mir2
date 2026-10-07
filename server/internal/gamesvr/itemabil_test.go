package gamesvr

import (
	"github.com/algotao/mir2/server/internal/netgate"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// item 造一件物品模板（Index 从 1 开始，NewStdItemSet 要求连续）。
func item(idx int32, name string, stdMode uint8, ac, mac, dc data.MinMax) *data.StdItem {
	return &data.StdItem{Index: idx, Name: name, StdMode: stdMode, AC: ac, MAC: mac, DC: dc}
}

// itemUser 造一个物品实例（Value 是 btValue[0..13]）。
func itemUser(index uint32, btValue map[int]byte) *pb.UserItem {
	v := make([]byte, entity.ItemValueLen)
	for i, b := range btValue {
		v[i] = b
	}
	return &pb.UserItem{Index: index, Value: v}
}

// TestItemEffective 守住 `TItem.GetItemAddValue`（ItmUnit.pas:117-169）的**索引**。
//
// ⚠️ 三种物品类型的 btValue 索引完全不同，这是最容易抄错的地方：
//
//	武器：[0..2]→DC/MC/SC 高位、[3]→AC 低位、[4]→MAC 低位、[5]→AC 高位、[6]→MAC 高位
//	衣服/首饰：[0..4]→AC 高位/MAC 高位/DC 高位/MC 高位/SC 高位
func TestItemEffective(t *testing.T) {
	// 武器：模板 AC={1,2} MAC={3,12} DC={5,10}
	w := item(1, "测试剑", 5,
		data.MinMax{Min: 1, Max: 2}, data.MinMax{Min: 3, Max: 12}, data.MinMax{Min: 5, Max: 10})
	v := itemEffective(w, itemUser(1, map[int]byte{0: 7, 3: 4, 4: 1, 5: 6, 7: 9}))
	if v.ac != 1+4 || v.ac2 != 2+6 {
		t.Errorf("武器 AC = (%d,%d)，期望 (5,8)：[3]→低位、[5]→高位", v.ac, v.ac2)
	}
	if v.mac != 3+1 || v.mac2 != 12 {
		t.Errorf("武器 MAC = (%d,%d)，期望 (4,12)：[4]→低位、[6]→高位", v.mac, v.mac2)
	}
	if v.dc != 5 || v.dc2 != 10+7 {
		t.Errorf("武器 DC = (%d,%d)，期望 (5,17)：[0] 加到**高位**", v.dc, v.dc2)
	}
	if v.source != 9 { // [7] 在 1..10 才覆盖 Source
		t.Errorf("武器 Source = %d，期望 9", v.source)
	}
	if got := itemEffective(w, itemUser(1, map[int]byte{7: 11})).source; got != w.Source {
		t.Errorf("武器 Source 被 %d 覆盖了（[7] > 10 不该覆盖）", got)
	}

	// 衣服：模板 AC={5,9} MAC={2,4}，[0]→AC 高位 [1]→MAC 高位
	d := item(2, "测试衣", 10,
		data.MinMax{Min: 5, Max: 9}, data.MinMax{Min: 2, Max: 4}, data.MinMax{})
	vd := itemEffective(d, itemUser(2, map[int]byte{0: 3, 1: 1}))
	if vd.ac != 5 || vd.ac2 != 12 || vd.mac != 2 || vd.mac2 != 5 {
		t.Errorf("衣服有效值 = AC(%d,%d) MAC(%d,%d)，期望 AC(5,12) MAC(2,5)",
			vd.ac, vd.ac2, vd.mac, vd.mac2)
	}

	// 首饰：同样 [0..4]→各高位
	a := item(3, "测试链", 19, data.MinMax{Max: 2}, data.MinMax{Min: 1, Max: 4}, data.MinMax{})
	va := itemEffective(a, itemUser(3, map[int]byte{0: 2}))
	if va.ac2 != 4 {
		t.Errorf("首饰 AC 高位 = %d，期望 4（模板 2 + [0]2）", va.ac2)
	}
}

// TestItemAddAbilWeapon 守住武器分支（ItmUnit.pas:559-571）：
// AC 高位 = 准确、AC 低位 = 幸运、MAC 低位 = 诅咒。
//
// ⚠️ 武器**不提供防御**（原版 RecalcAbilitys 遇到 U_WEAPON 直接 Continue）。
func TestItemAddAbilWeapon(t *testing.T) {
	w := item(1, "测试剑", 5,
		data.MinMax{Min: 1, Max: 2}, data.MinMax{Min: 3, Max: 12}, data.MinMax{Min: 5, Max: 10})
	a := itemEffective(w, nil).apply()
	if a.hit != 2 {
		t.Errorf("准确 = %d，期望 2（AC 高位）", a.hit)
	}
	if a.luck != 1 {
		t.Errorf("幸运 = %d，期望 1（AC 低位）", a.luck)
	}
	if a.unluck != 3 {
		t.Errorf("诅咒 = %d，期望 3（MAC 低位）", a.unluck)
	}
	if a.acLo != 0 || a.acHi != 0 || a.macLo != 0 || a.macHi != 0 {
		t.Errorf("武器不该给防御：AC(%d,%d) MAC(%d,%d)", a.acLo, a.acHi, a.macLo, a.macHi)
	}
	if a.dcLo != 5 || a.dcHi != 10 {
		t.Errorf("武器 DC = (%d,%d)，期望 (5,10)", a.dcLo, a.dcHi)
	}
}

// TestItemAddAbilDress 守住衣服分支（:572-579）：AC/MAC 是防御，Source 低/高字节 = 幸运/诅咒。
func TestItemAddAbilDress(t *testing.T) {
	d := item(1, "测试衣", 10,
		data.MinMax{Min: 5, Max: 9}, data.MinMax{Min: 2, Max: 4}, data.MinMax{})
	for _, c := range []struct {
		name   string
		source int8
		luck   int
		unluck int
	}{
		{"普通衣服（Source=0）", 0, 0, 0},
		{"Source=3 ⇒ 幸运 +3", 3, 3, 0},
		// Delphi 的 LoByte/HiByte 对负数按 Integer 取字节 ⇒ -1 两个字节都是 255
		{"Source=-1 ⇒ 两字节都 255", -1, 255, 255},
	} {
		d.Source = c.source
		a := itemEffective(d, nil).apply()
		if a.acLo != 5 || a.acHi != 9 || a.macLo != 2 || a.macHi != 4 {
			t.Errorf("%s: 防御 = AC(%d,%d) MAC(%d,%d)，期望 AC(5,9) MAC(2,4)",
				c.name, a.acLo, a.acHi, a.macLo, a.macHi)
		}
		if a.luck != c.luck || a.unluck != c.unluck {
			t.Errorf("%s: 幸运/诅咒 = %d/%d，期望 %d/%d",
				c.name, a.luck, a.unluck, c.luck, c.unluck)
		}
	}
}

// TestItemAddAbilAccessory 守住首饰按 StdMode 的分支（:580-693）。
//
// ⚠️ 52/53/54 走哪一支取决于 `AddUserItemNewValue`（官方 !setup.txt:1016 = 1）。
func TestItemAddAbilAccessory(t *testing.T) {
	cases := []struct {
		name    string
		stdMod  uint8
		ac, mac data.MinMax
		check   func(t *testing.T, a addAbil)
	}{
		{"19 项链：抗魔+幸运+诅咒", 19, data.MinMax{Max: 2}, data.MinMax{Min: 1, Max: 4},
			func(t *testing.T, a addAbil) {
				if a.antiMagic != 2 || a.luck != 4 || a.unluck != 1 {
					t.Errorf("抗魔/幸运/诅咒 = %d/%d/%d，期望 2/4/1", a.antiMagic, a.luck, a.unluck)
				}
			}},
		{"20 项链：准确+敏捷", 20, data.MinMax{Max: 3}, data.MinMax{Max: 2},
			func(t *testing.T, a addAbil) {
				if a.hit != 3 || a.speed != 2 {
					t.Errorf("准确/敏捷 = %d/%d，期望 3/2", a.hit, a.speed)
				}
			}},
		{"24 手镯：准确+敏捷", 24, data.MinMax{Max: 1}, data.MinMax{Max: 1},
			func(t *testing.T, a addAbil) {
				if a.hit != 1 || a.speed != 1 {
					t.Errorf("准确/敏捷 = %d/%d，期望 1/1", a.hit, a.speed)
				}
			}},
		{"21 项链：体力·魔法恢复", 21, data.MinMax{Max: 3}, data.MinMax{Max: 2},
			func(t *testing.T, a addAbil) {
				if a.healthRecover != 3 || a.spellRecover != 2 {
					t.Errorf("恢复 = %d/%d，期望 3/2", a.healthRecover, a.spellRecover)
				}
			}},
		{"23 戒指：抗毒+解毒恢复", 23, data.MinMax{Max: 4}, data.MinMax{Max: 1},
			func(t *testing.T, a addAbil) {
				if a.antiPoison != 4 || a.poisonRecover != 1 {
					t.Errorf("抗毒/解毒 = %d/%d，期望 4/1", a.antiPoison, a.poisonRecover)
				}
			}},
		{"63 Charm：HP/MP 上限+幸运+诅咒", 63, data.MinMax{Min: 3, Max: 5}, data.MinMax{Min: 1, Max: 2},
			func(t *testing.T, a addAbil) {
				if a.hp != 3 || a.mp != 5 || a.luck != 2 || a.unluck != 1 {
					t.Errorf("HP/MP/幸运/诅咒 = %d/%d/%d/%d，期望 3/5/2/1",
						a.hp, a.mp, a.luck, a.unluck)
				}
			}},
		{"52 靴子（新属性）：准确+敏捷", 52, data.MinMax{Max: 2}, data.MinMax{Max: 1},
			func(t *testing.T, a addAbil) {
				if a.hit != 2 || a.speed != 1 {
					t.Errorf("准确/敏捷 = %d/%d，期望 2/1（AddUserItemNewValue=1）", a.hit, a.speed)
				}
			}},
		{"22 戒指：无附加属性（原版 case 里没有它）", 22, data.MinMax{Max: 9}, data.MinMax{Max: 9},
			func(t *testing.T, a addAbil) {
				if a.hit != 0 || a.speed != 0 || a.acLo != 0 || a.luck != 0 {
					t.Errorf("22 不该有附加属性，实际 %+v", a)
				}
			}},
	}
	for _, c := range cases {
		it := item(1, c.name, c.stdMod, c.ac, c.mac, data.MinMax{})
		c.check(t, itemEffective(it, nil).apply())
	}
}

// TestPlayerAddAbilAndLuck 守住"装备汇总 → 命中/敏捷/幸运"这条线。
func TestPlayerAddAbilAndLuck(t *testing.T) {
	// 槽位：1=武器、3=项链、4=左手镯（见 equip.go 的 u* 常量）
	items := []*data.StdItem{
		item(1, "幸运剑", 5, data.MinMax{Min: 2, Max: 3}, data.MinMax{}, data.MinMax{Min: 4, Max: 8}),
		item(2, "准确链", 20, data.MinMax{Max: 4}, data.MinMax{Max: 2}, data.MinMax{}),
		item(3, "诅咒衣", 10, data.MinMax{Min: 5, Max: 7}, data.MinMax{Min: 1, Max: 1}, data.MinMax{}),
	}
	set, err := data.NewStdItemSet(items)
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	srv := &Server{data: dataState{tables: &data.Tables{Items: set}}}

	p := &Player{Char: &storage.Character{Job: entity.JobWarr, Data: &pb.CharacterData{
		Job:      entity.JobWarr,
		HumItems: make([]*pb.UserItem, 10),
		BagItems: make([]*pb.UserItem, 0),
	}}}
	p.Char.Data.HumItems[proto.SlotWeapon] = itemUser(1, nil)   // 幸运 2、准确 3、诅咒 0
	p.Char.Data.HumItems[proto.SlotNecklace] = itemUser(2, nil) // 准确 4、敏捷 2
	p.Char.Data.HumItems[proto.SlotDress] = itemUser(3, nil)    // 防御 AC 5..7 / MAC 1

	if got := srv.playerHitPoint(p); got != entity.DefHit+3+4 {
		t.Errorf("命中 = %d，期望 %d（基数 5 + 准确 3 + 4）", got, entity.DefHit+7)
	}
	if got := srv.playerSpeedPoint(p); got != entity.DefSpeed+2 {
		t.Errorf("敏捷 = %d，期望 %d（基数 15 + 敏捷 2）", got, entity.DefSpeed+2)
	}
	if got := srv.playerLuck(p); got != 2 {
		t.Errorf("幸运 = %d，期望 2（武器 AC 低位）", got)
	}
	// 防御只算衣服：武器的 AC 是幸运/准确，项链的 AC 高位是准确
	ac := srv.playerAC(p)
	if lo, hi := uint32(proto.UnpackLo(ac)), uint32(proto.UnpackHi(ac)); lo != 5 || hi != 7 {
		t.Errorf("物防 = (%d,%d)，期望 (5,7)（只有衣服）", lo, hi)
	}
	// 攻击力 = 徒手基数 (1,3)（Abil.Dc 未设 ⇒ 原版 attackPower 的兜底）+ 武器 DC 4..8
	if min, max := srv.attackPower(p); min != 5 || max != 11 {
		t.Errorf("攻击力 = (%d,%d)，期望 (5,11)", min, max)
	}
}

// TestRollAttackLuck 守住 `GetAttackPower` 的幸运/诅咒分支（ObjBase.pas:2421-2431）。
//
//	幸运 > 0：Random(10 - min(9, luck)) = 0 ⇒ **直接取上限**
//	诅咒 < 0：Random(10 - max(0, -luck)) = 0 ⇒ **直接取下限**
//
// 幸运 ≥ 9 / 诅咒 ≤ −9 时内层 Random(1) 恒为 0 ⇒ **每一刀**都是上限/下限。
func TestRollAttackLuck(t *testing.T) {
	old := delphi.RandN
	defer func() { delphi.RandN = old }()

	// 注入的掷骰：固定返回 1（正常掷骰会取到中间值），
	// 但 `Random(1)` 按定义**只能是 0** —— 幸运 ≥ 9 时内层正是 Random(1)。
	delphi.RandN = func(n int) int {
		if n <= 1 {
			return 0
		}
		return 1
	}
	// 幸运 9：无论正常骰子怎么掷，都必须是上限
	if got := rollAttack(10, 20, 9); got != 20 {
		t.Errorf("幸运 9 时伤害 = %d，期望 20（上限）", got)
	}
	// 诅咒 -9：必须是下限
	if got := rollAttack(10, 20, -9); got != 10 {
		t.Errorf("诅咒 -9 时伤害 = %d，期望 10（下限）", got)
	}
	// 幸运 1：Random(9) = 1 ⇒ 不触发，走普通掷骰（此处 RNG 返回 1 ⇒ 10+1）
	if got := rollAttack(10, 20, 1); got != 11 {
		t.Errorf("幸运 1（未触发）伤害 = %d，期望 11", got)
	}
	// 幸运 0 / 无装备：普通掷骰
	if got := rollAttack(10, 20, 0); got != 11 {
		t.Errorf("幸运 0 伤害 = %d，期望 11", got)
	}
	// min == max 时不该掷骰（nPower = 0）
	delphi.RandN = func(n int) int { return 99 }
	if got := rollAttack(7, 7, 0); got != 7 {
		t.Errorf("min=max 时伤害 = %d，期望 7", got)
	}
	// max < min（数据异常）按 min 处理，且 nPower 归 0
	if got := rollAttack(7, 3, 0); got != 7 {
		t.Errorf("max<min 时伤害 = %d，期望 7", got)
	}
}

// TestItemAddAbilHitSpeed 守住攻击速度（`nHitSpeed`）的加减（ItmUnit.pas:562-568、:630-660）。
//
//	武器：MAC 高位 > 10 ⇒ +(MAC2-10)，否则 ⇒ -MAC2
//	首饰 StdMode 21/54/64/23：+AC 低位 -MAC 低位
//
// ⚠️ 这一项**不是**只给客户端做动画：服务端攻击间隔也要减它
// （`ClientHitXY:8774`，见 netgate.go 的 netgate.AttackIntervalFor）。
func TestItemAddAbilHitSpeed(t *testing.T) {
	// 武器：MAC 高位 12 ⇒ +2（"快"武器的 MAC 高位超过 10 才算加速）
	w := item(1, "快剑", 5, data.MinMax{}, data.MinMax{Min: 0, Max: 12}, data.MinMax{})
	if got := itemEffective(w, nil).apply().hitSpeed; got != 2 {
		t.Errorf("武器 MAC2=12 ⇒ 攻速 +2，得到 %d", got)
	}
	// MAC 高位 8（≤10）⇒ 减 8（慢武器是惩罚）
	w.MAC = data.MinMax{Min: 0, Max: 8}
	if got := itemEffective(w, nil).apply().hitSpeed; got != -8 {
		t.Errorf("武器 MAC2=8 ⇒ 攻速 -8，得到 %d", got)
	}
	// 边界 11 ⇒ +1（不是"零"）
	w.MAC = data.MinMax{Min: 0, Max: 11}
	if got := itemEffective(w, nil).apply().hitSpeed; got != 1 {
		t.Errorf("武器 MAC2=11 ⇒ 攻速 +1，得到 %d", got)
	}
	// 木剑这类 MAC2=0 ⇒ 0
	w.MAC = data.MinMax{}
	if got := itemEffective(w, nil).apply().hitSpeed; got != 0 {
		t.Errorf("武器 MAC2=0 ⇒ 攻速 0，得到 %d", got)
	}

	// 首饰：23（戒指）AC 低位 2 ⇒ +2；再把 MAC 低位抬到 3 ⇒ 1-3 = -2
	for _, std := range []uint8{21, 23, 54, 64} {
		acc := item(2, "测试饰", std, data.MinMax{Min: 2, Max: 0}, data.MinMax{}, data.MinMax{})
		if got := itemEffective(acc, nil).apply().hitSpeed; got != 2 {
			t.Errorf("StdMode %d AC=2 ⇒ 攻速 +2，得到 %d", std, got)
		}
		acc.MAC = data.MinMax{Min: 3, Max: 0}
		if got := itemEffective(acc, nil).apply().hitSpeed; got != -1 {
			t.Errorf("StdMode %d AC=2 MAC=3 ⇒ 攻速 -1，得到 %d", std, got)
		}
	}
}

// TestAttackIntervalFor 守住服务端攻击间隔（ObjBase.pas:8774）：
//
//	dwAttackTime := _MAX(0, dwHitIntervalTime - m_nHitSpeed * btItemSpeed)
//
// 基础 520ms（官方 !setup.txt）、btItemSpeed=25（M2Share 出厂值）。
func TestAttackIntervalFor(t *testing.T) {
	for _, c := range []struct {
		hitSpeed int
		want     time.Duration
	}{
		{0, 520 * time.Millisecond},  // 无攻速加成
		{2, 470 * time.Millisecond},  // 狂风戒指那类 +2 ⇒ 少 50ms
		{10, 270 * time.Millisecond}, // +10 ⇒ 几乎减半
		{-8, 720 * time.Millisecond}, // 慢武器 ⇒ 间隔变**长**（原版同样如此）
		{100, 0},                     // 超过基础值 ⇒ `_MAX(0, ...)` 夹到 0
	} {
		if got := netgate.AttackIntervalFor(c.hitSpeed); got != c.want {
			t.Errorf("攻速 %d ⇒ 间隔 %v，期望 %v", c.hitSpeed, got, c.want)
		}
	}
}

// TestPlayerItemSpecials 守住"装备**形状** → 特殊效果"的映射
// （原版 RecalcAbilitys 里按槽位判 StdItem.Shape 的那一大段，ObjBase.pas:3240-3360）。
//
// ⚠️ 两个容易踩的点：
//  1. 只认**已穿戴**的物品（HumItems）：物品表里有些药包 StdMode=31 也带这些形状号，
//     背在包里的它们不该生效（原版也只遍历 m_UseItems）；
//  2. 虹魔套要的是 **ΣAniCount**（吸血百分比）**和件数**（3 件才 +2 准确）两个值。
func TestPlayerItemSpecials(t *testing.T) {
	mk := func(idx int32, name string, std uint8, shape uint8, ani uint8) *data.StdItem {
		it := item(idx, name, std, data.MinMax{}, data.MinMax{}, data.MinMax{Min: 1, Max: 2})
		it.Shape = shape
		it.AniCount = ani
		return it
	}
	items := []*data.StdItem{
		mk(1, "隐身戒指", 22, 111, 0),
		mk(2, "麻痹戒指", 22, 113, 0),
		mk(3, "复活戒指", 22, 114, 0),
		mk(4, "愤怒戒指", 22, 117, 0),
		mk(5, "技巧项链", 20, 120, 0),
		mk(6, "虹魔戒指", 22, 136, 5),
		mk(7, "虹魔手镯", 26, 137, 5),
		mk(8, "虹魔项链", 20, 138, 5),
		mk(9, "太阳水包", 31, 113, 3), // 药包：形状号一样，但不该生效
		mk(10, "普通铁剑", 5, 0, 0),
		mk(11, "传送戒指", 22, 112, 0),
	}
	set, err := data.NewStdItemSet(items)
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	srv := &Server{data: dataState{tables: &data.Tables{Items: set}}}
	p := &Player{Char: &storage.Character{Job: entity.JobWarr, Data: &pb.CharacterData{
		Job:      entity.JobWarr,
		HumItems: make([]*pb.UserItem, 10),
		BagItems: make([]*pb.UserItem, 10),
	}}}

	// ① 空手：一个都不生效
	if sp := srv.playerItemSpecials(p); sp != (itemSpecials{}) {
		t.Errorf("空手不该有特殊效果，得到 %+v", sp)
	}

	// ② 背包里的"药包"不算（形状号 113 与麻痹戒指相同）
	p.Char.Data.BagItems[0] = itemUser(9, nil)
	if sp := srv.playerItemSpecials(p); sp.paralysis {
		t.Error("背包里的药包不该触发麻痹（只认已穿戴）")
	}

	// ③ 逐个戴上，逐条验
	p.Char.Data.HumItems[proto.SlotRingR] = itemUser(1, nil) // 隐身戒指
	p.Char.Data.HumItems[proto.SlotRingL] = itemUser(2, nil) // 麻痹戒指
	p.Char.Data.HumItems[proto.SlotNecklace] = itemUser(3, nil)
	p.Char.Data.HumItems[proto.SlotHelmet] = itemUser(4, nil)
	p.Char.Data.HumItems[proto.SlotArmRingR] = itemUser(5, nil)
	sp := srv.playerItemSpecials(p)
	p.Char.Data.HumItems[proto.SlotRingR] = itemUser(11, nil) // 传送戒指
	sp = srv.playerItemSpecials(p)
	if !sp.teleport {
		t.Error("传送戒指（Shape 112）应置 teleport")
	}
	p.Char.Data.HumItems[proto.SlotRingR] = itemUser(1, nil) // 还原隐身戒指
	sp = srv.playerItemSpecials(p)                           // 上面被传送戒指覆盖过，重新算
	if !sp.hideMode || !sp.paralysis || !sp.revival || !sp.noDrop || !sp.fastTrain {
		t.Errorf("五个单件标志应全部置位，得到 %+v", sp)
	}
	if sp.hongMo != 0 || sp.hongMoPieces != 0 {
		t.Errorf("没戴虹魔不该有虹魔加成，得到 %+v", sp)
	}

	// ④ 虹魔：1 件没有 +2 准确，3 件才有
	p.Char.Data.HumItems[proto.SlotRingR] = itemUser(6, nil)
	// 虹魔戒指模板不带准确（AC.Max=0）⇒ 命中就是基数；1 件**不该**有 +2
	if got := srv.playerHitPoint(p); got != entity.DefHit {
		t.Errorf("虹魔 1 件时命中 = %d，期望 %d（基数，不该 +2）", got, entity.DefHit)
	}
	p.Char.Data.HumItems[proto.SlotArmRingR] = itemUser(7, nil)
	p.Char.Data.HumItems[proto.SlotNecklace] = itemUser(8, nil)
	sp = srv.playerItemSpecials(p)
	if sp.hongMoPieces != 3 || sp.hongMo != 15 {
		t.Errorf("虹魔 3 件应 ΣAniCount=15 件数 3，得到 %+v", sp)
	}
	if got := srv.playerHitPoint(p); got != entity.DefHit+2 {
		t.Errorf("虹魔 3 件命中 = %d，期望 %d（基数 + 2）", got, entity.DefHit+2)
	}
}

// TestApplyEquipHpMp 守住"等级基数 + 装备加成"的上限重算（ObjBase.pas:3390-3392 / :3465-3473）。
//
// 重点验三件事：
//  1. StdMode 63 首饰的 HP/MP 上限加成（原来 `a.hp`/`a.mp` 根本没有消费方）；
//  2. 魔血套是"**把 MaxMP 挪给 MaxHP**"，且三件齐再 +50；
//  3. **幂等**：重复调用不叠加（上限是落盘的，增量写法会在"换装备→存档→重登"后翻倍）。
func TestApplyEquipHpMp(t *testing.T) {
	// ⚠️ StdMode 63 的取法是 **AC 低位 = HP、AC 高位 = MP**（不是 MAC！，
	// 见 itemabil.go 的 `case 63:`），所以两个值都写在 AC 的 Min/Max 上。
	charm := item(1, "生命项链", 63, data.MinMax{Min: 30, Max: 10}, data.MinMax{}, data.MinMax{})
	set, err := data.NewStdItemSet([]*data.StdItem{charm})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	srv := &Server{data: dataState{tables: &data.Tables{Items: set}}}
	const lv = 10
	p := &Player{Char: &storage.Character{Job: entity.JobWarr, Data: &pb.CharacterData{
		Job:      entity.JobWarr,
		HumItems: make([]*pb.UserItem, 10),
		BagItems: make([]*pb.UserItem, 10),
		Abil:     &pb.Ability{Level: lv},
	}}}
	baseHP, baseMP := baseHpMp(lv, entity.JobWarr)
	if baseHP == 0 || baseMP == 0 {
		t.Fatalf("10 级的基数不该是 0（HP %d MP %d）", baseHP, baseMP)
	}

	// 空手：上限 = 基数
	srv.applyEquipHpMp(p)
	if p.Char.Data.Abil.MaxHp != baseHP || p.Char.Data.Abil.MaxMp != baseMP {
		t.Fatalf("空手上限 = %d/%d，期望基数 %d/%d",
			p.Char.Data.Abil.MaxHp, p.Char.Data.Abil.MaxMp, baseHP, baseMP)
	}

	// 戴 63 首饰（AC.Min=30 ⇒ HP+30；MAC.Min=10 ⇒ MP+10）
	p.Char.Data.HumItems[proto.SlotNecklace] = itemUser(1, nil)
	srv.applyEquipHpMp(p)
	if got := p.Char.Data.Abil.MaxHp; got != baseHP+30 {
		t.Errorf("戴 63 首饰后 MaxHp = %d，期望 %d（基数 + 30）", got, baseHP+30)
	}
	if got := p.Char.Data.Abil.MaxMp; got != baseMP+10 {
		t.Errorf("戴 63 首饰后 MaxMp = %d，期望 %d（基数 + 10）", got, baseMP+10)
	}

	// 幂等：再算一次不该叠加
	srv.applyEquipHpMp(p)
	if got := p.Char.Data.Abil.MaxHp; got != baseHP+30 {
		t.Errorf("重复重算后 MaxHp = %d，期望仍是 %d（必须是幂等的）", got, baseHP+30)
	}

	// 脱掉 ⇒ 回到基数
	p.Char.Data.HumItems[proto.SlotNecklace] = &pb.UserItem{}
	srv.applyEquipHpMp(p)
	if got := p.Char.Data.Abil.MaxHp; got != baseHP {
		t.Errorf("脱下后 MaxHp = %d，期望回到基数 %d", got, baseHP)
	}
}

// TestApplyEquipHpMpMoXieSuite 验魔血套：把 MaxMP 挪给 MaxHP。
//
//	每件 `Inc(m_nMoXieSuite, StdItem.AniCount)`（:3250/:3282/:3314）
//	三件齐再 `Inc(m_nMoXieSuite, 50)`（:3341-3342）
//	然后 `Dec(MaxMP, suite)` + `Inc(MaxHP, suite)`（:3465-3473）
func TestApplyEquipHpMpMoXieSuite(t *testing.T) {
	mk := func(idx int32, name string, std uint8, shape uint8, ani uint8) *data.StdItem {
		it := item(idx, name, std, data.MinMax{}, data.MinMax{}, data.MinMax{})
		it.Shape = shape
		it.AniCount = ani
		return it
	}
	set, err := data.NewStdItemSet([]*data.StdItem{
		mk(1, "魔血戒指", 22, 133, 5),
		mk(2, "魔血手镯", 26, 134, 5),
		mk(3, "魔血项链", 20, 135, 5),
	})
	if err != nil {
		t.Fatalf("建物品表失败: %v", err)
	}
	srv := &Server{data: dataState{tables: &data.Tables{Items: set}}}
	const lv = 33
	p := &Player{Char: &storage.Character{Job: entity.JobTaos, Data: &pb.CharacterData{
		Job:      entity.JobTaos,
		HumItems: make([]*pb.UserItem, 10),
		BagItems: make([]*pb.UserItem, 10),
		Abil:     &pb.Ability{Level: lv},
	}}}
	baseHP, baseMP := baseHpMp(lv, entity.JobTaos)

	// 只戴 1 件：ΣAniCount = 5（没到 3 件，没有 +50）
	p.Char.Data.HumItems[proto.SlotRingL] = itemUser(1, nil)
	srv.applyEquipHpMp(p)
	if p.Char.Data.Abil.MaxHp != baseHP+5 || p.Char.Data.Abil.MaxMp != baseMP-5 {
		t.Fatalf("魔血 1 件：HP %d（期望 %d）、MP %d（期望 %d）",
			p.Char.Data.Abil.MaxHp, baseHP+5, p.Char.Data.Abil.MaxMp, baseMP-5)
	}

	// 三件齐：ΣAniCount = 15，再 +50 ⇒ 共挪 65
	p.Char.Data.HumItems[proto.SlotArmRingL] = itemUser(2, nil)
	p.Char.Data.HumItems[proto.SlotNecklace] = itemUser(3, nil)
	srv.applyEquipHpMp(p)
	if p.Char.Data.Abil.MaxHp != baseHP+65 || p.Char.Data.Abil.MaxMp != baseMP-65 {
		t.Errorf("魔血 3 件：HP %d（期望 %d）、MP %d（期望 %d）",
			p.Char.Data.Abil.MaxHp, baseHP+65, p.Char.Data.Abil.MaxMp, baseMP-65)
	}
	// 幂等
	srv.applyEquipHpMp(p)
	if p.Char.Data.Abil.MaxHp != baseHP+65 {
		t.Errorf("魔血重复重算后 MaxHp = %d，期望 %d", p.Char.Data.Abil.MaxHp, baseHP+65)
	}
}

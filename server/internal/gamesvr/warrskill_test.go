package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
)

// TestHitModeOf 守住"攻击模式来自消息号"（ObjBase.pas:8851-8856）。
//
// ⚠️ 别把它写成"从 Tag 高位取模式"——原版是**六条不同的 CM 消息**。
func TestHitModeOf(t *testing.T) {
	cases := map[uint16]int{
		proto.CM_HIT:      hitNormal,
		proto.CM_HEAVYHIT: hitHeavy,
		proto.CM_BIGHIT:   hitBig,
		proto.CM_POWERHIT: hitPower,
		proto.CM_LONGHIT:  hitLong,
		proto.CM_WIDEHIT:  hitWide,
		proto.CM_FIREHIT:  hitFire,
	}
	for ident, want := range cases {
		if got := hitModeOf(ident); got != want {
			t.Errorf("hitModeOf(%d) = %d，期望 %d", ident, got, want)
		}
	}
	// 其它消息号一律当普通攻击（走 normal 分支）。
	if got := hitModeOf(proto.CM_WALK); got != hitNormal {
		t.Errorf("非攻击消息应回落 normal，实际 %d", got)
	}
}

// TestSwingIdent 动画号：没学会技能 / 没充能时保持 SM_HIT（ObjBase.pas:18845-18856）。
func TestSwingIdent(t *testing.T) {
	if got := swingIdent(hitLong, true, false, false, false); got != proto.SM_LONGHIT {
		t.Errorf("学会刺杀 ⇒ SM_LONGHIT(%d)，实际 %d", proto.SM_LONGHIT, got)
	}
	if got := swingIdent(hitLong, false, false, false, false); got != proto.SM_HIT {
		t.Errorf("没学刺杀 ⇒ 仍是 SM_HIT(%d)，实际 %d", proto.SM_HIT, got)
	}
	if got := swingIdent(hitWide, false, true, false, false); got != proto.SM_WIDEHIT {
		t.Errorf("学会半月 ⇒ SM_WIDEHIT(%d)，实际 %d", proto.SM_WIDEHIT, got)
	}
	if got := swingIdent(hitHeavy, false, false, false, false); got != proto.SM_HEAVYHIT {
		t.Errorf("重击 ⇒ SM_HEAVYHIT(%d)，实际 %d", proto.SM_HEAVYHIT, got)
	}
	// 攻杀/烈火：动画由**出刀前的充能快照**决定
	if got := swingIdent(hitPower, false, false, true, false); got != proto.SM_POWERHIT {
		t.Errorf("充能的攻杀 ⇒ SM_POWERHIT(%d)，实际 %d", proto.SM_POWERHIT, got)
	}
	if got := swingIdent(hitPower, false, false, false, false); got != proto.SM_HIT {
		t.Errorf("没充能的攻杀 ⇒ 退回 SM_HIT(%d)，实际 %d", proto.SM_HIT, got)
	}
	if got := swingIdent(hitFire, false, false, false, true); got != proto.SM_FIREHIT {
		t.Errorf("点燃的烈火 ⇒ SM_FIREHIT(%d)，实际 %d", proto.SM_FIREHIT, got)
	}
	if got := swingIdent(hitFire, false, false, false, false); got != proto.SM_HIT {
		t.Errorf("未点燃的烈火 ⇒ 退回 SM_HIT(%d)，实际 %d", proto.SM_HIT, got)
	}
}

// TestWarrChargeBonusValues 守住两个充能技能的加成数值。
//
//	攻杀：m_nHitPlus := DEFHIT + btLevel（M2Share.pas:128 DEFHIT=5）
//	烈火：m_nHitDouble := 4 + btLevel*4，命中时乘 10 倍当百分比
func TestWarrChargeBonusValues(t *testing.T) {
	player := func(magics ...*pb.UserMagic) *Player {
		return &Player{Char: &storage.Character{Data: &pb.CharacterData{Magics: magics}}}
	}
	um := func(id uint32, lv uint32) *pb.UserMagic {
		return &pb.UserMagic{MagicId: id, Level: lv}
	}
	for _, c := range []struct {
		name     string
		magics   []*pb.UserMagic
		wantPlus int
		wantPct  int
	}{
		{"什么都没学", nil, 0, 0},
		{"攻杀 0 级", []*pb.UserMagic{um(7, 0)}, 5, 0},
		{"攻杀 3 级", []*pb.UserMagic{um(7, 3)}, 8, 0},
		{"烈火 0 级", []*pb.UserMagic{um(26, 0)}, 0, 40},
		{"烈火 3 级", []*pb.UserMagic{um(26, 3)}, 0, 160},
		{"两个都学", []*pb.UserMagic{um(7, 1), um(26, 1)}, 6, 80},
	} {
		p := player(c.magics...)
		if got := powerHitPlus(p); got != c.wantPlus {
			t.Errorf("%s: powerHitPlus = %d，期望 %d", c.name, got, c.wantPlus)
		}
		if got := fireHitPercent(p); got != c.wantPct {
			t.Errorf("%s: fireHitPercent = %d，期望 %d", c.name, got, c.wantPct)
		}
	}
}

// TestWarBonusApply 加成作用在**未减防**的威力上：先加攻杀、再乘烈火。
func TestWarBonusApply(t *testing.T) {
	for _, c := range []struct {
		name  string
		bonus warBonus
		power int
		want  int
	}{
		{"无加成", warBonus{}, 100, 100},
		{"攻杀 3 级 +8", warBonus{plus: 8}, 100, 108},
		{"烈火 0 级 +40%", warBonus{firePct: 40}, 100, 140},
		// 原著顺序：Inc(nPower, nHitPlus) 在前，再 ROUND(nPower/100*(nHitDouble*10))
		{"攻杀 + 烈火", warBonus{plus: 8, firePct: 40}, 100, 151}, // (100+8)*1.4 = 151.2 → 151
		{"烈火 3 级 +160%", warBonus{firePct: 160}, 100, 260},
	} {
		if got := c.bonus.apply(c.power); got != c.want {
			t.Errorf("%s: apply(%d) = %d，期望 %d", c.name, c.power, got, c.want)
		}
	}
	if !(warBonus{}).isZero() {
		t.Error("零值 bonus 应 isZero")
	}
	if (warBonus{plus: 1}).isZero() || (warBonus{firePct: 1}).isZero() {
		t.Error("有加成的 bonus 不该 isZero")
	}
	if got := (warBonus{plus: 1}).name(); got != "攻杀剑术" {
		t.Errorf("name = %q，期望 攻杀剑术", got)
	}
	if got := (warBonus{firePct: 1}).name(); got != "烈火剑法" {
		t.Errorf("name = %q，期望 烈火剑法", got)
	}
}

// TestSwordLongPower 刺杀威力：ROUND(nPower/(3+2)*(L+2)) * SwordLongPowerRate/100。
//
// btTrainLv 硬编码 3 ⇒ 分母 5。0 级 40%、3 级 100%（Rate=100）。
func TestSwordLongPower(t *testing.T) {
	for _, c := range []struct {
		power int
		level uint32
		want  int
	}{{100, 0, 40}, {100, 1, 60}, {100, 2, 80}, {100, 3, 100}, {7, 0, 3}} {
		if got := swordLongPower(c.power, c.level); got != c.want {
			t.Errorf("swordLongPower(%d, L%d) = %d，期望 %d", c.power, c.level, got, c.want)
		}
	}
}

// TestSwordWidePower 半月威力：ROUND(nPower/(3+10)*(L+2)) ⇒ 单目标很弱。
func TestSwordWidePower(t *testing.T) {
	// 7/13*2 = 1.077 → 1；100/13*2 = 15.38 → 15；100/13*5 = 38.46 → 38
	for _, c := range []struct {
		power int
		level uint32
		want  int
	}{{100, 0, 15}, {100, 3, 38}, {7, 0, 1}} {
		if got := swordWidePower(c.power, c.level); got != c.want {
			t.Errorf("swordWidePower(%d, L%d) = %d，期望 %d", c.power, c.level, got, c.want)
		}
	}
}

// TestSpellPoint 守住 MP 消耗公式（原版 GetSpellPoint）。
//
//	ROUND(wSpell / 4 * (L+1)) + btDefSpell
//
// ⚠️ 我们此前写的是 `Spell + DefSpell*level`——固定项与等级项搞反了。
// 这条测试专门盯住"固定项是 DefSpell"：
// 魔法盾 spell=20 def=30 ⇒ 0 级 5+30=**35**（旧公式会给 20）。
func TestSpellPoint(t *testing.T) {
	cases := []struct {
		name       string
		spell, def uint16
		level      uint32
		want       uint32
	}{
		{"魔法盾 L0", 20, 30, 0, 35}, // 20/4*1=5 + 30
		{"魔法盾 L3", 20, 30, 3, 50}, // 20/4*4=20 + 30
		{"火球术 L0", 4, 1, 0, 2},    // 1 + 1
		{"治愈术 L0", 7, 0, 0, 2},    // round(1.75) = 2
		{"隐身术 L0", 5, 0, 0, 1},    // round(1.25) = 1
	}
	for _, c := range cases {
		info := &data.MagicInfo{Name: c.name, Spell: c.spell, DefSpell: c.def}
		um := &pb.UserMagic{MagicId: 1, Level: c.level}
		if got := magic.SpellPoint(info, um); got != c.want {
			t.Errorf("%s: SpellPoint = %d，期望 %d", c.name, got, c.want)
		}
	}
	// nil 安全
	if got := magic.SpellPoint(nil, nil); got != 0 {
		t.Errorf("nil 参数应返回 0，实际 %d", got)
	}
}

// TestConsumeWarrCharge 守住"消费充能"的语义（ObjBase.pas:22122-22162）。
//
// 三条容易做错的：① 别的攻击模式不消费；② 消费后标志必须清掉（否则一刀吃两次）；
// ③ 烈火消费时会**刷新计时**（"Jacky 禁止双烈火"），不是清零。
func TestConsumeWarrCharge(t *testing.T) {
	srv := &Server{}
	newPlayer := func() *Player {
		return &Player{Char: &storage.Character{Data: &pb.CharacterData{
			Magics: []*pb.UserMagic{{MagicId: 7, Level: 3}, {MagicId: 26, Level: 0}},
		}}}
	}

	// ① 普通攻击不消费
	p := newPlayer()
	p.powerHit, p.fireHit = true, true
	if got := srv.consumeWarrCharge(p, hitNormal); !got.isZero() {
		t.Errorf("普通攻击不该有加成，得到 %+v", got)
	}
	if !p.powerHit || !p.fireHit {
		t.Error("普通攻击不该清掉充能标志")
	}

	// ② 攻杀：消费 + 加攻 + 清标志
	p = newPlayer()
	p.powerHit = true
	got := srv.consumeWarrCharge(p, hitPower)
	if got.plus != 8 { // DEFHIT(5) + 3 级
		t.Errorf("攻杀加成 = %d，期望 8", got.plus)
	}
	if p.powerHit {
		t.Error("消费后 powerHit 必须清掉（否则一刀吃两次）")
	}
	if again := srv.consumeWarrCharge(p, hitPower); !again.isZero() {
		t.Error("第二次消费不该再有加成")
	}

	// ③ 烈火：消费 + 百分比 + 刷新计时
	p = newPlayer()
	p.fireHit = true
	p.fireHitAt = time.Now().Add(-time.Hour) // 故意放老，验证会被刷新
	got = srv.consumeWarrCharge(p, hitFire)
	if got.firePct != 40 { // (4 + 0*4) * 10
		t.Errorf("烈火加成 = %d%%，期望 40", got.firePct)
	}
	if p.fireHit {
		t.Error("消费后 fireHit 必须清掉")
	}
	if time.Since(p.fireHitAt) > time.Second {
		t.Error("烈火消费时应刷新 fireHitAt（禁止双烈火）")
	}
}

// TestTickWarrChargeExpiry 守住 20 秒有效期（ObjBase.pas:6427）。
func TestTickWarrChargeExpiry(t *testing.T) {
	srv := &Server{}
	p := &Player{Char: &storage.Character{Data: &pb.CharacterData{}}}

	// 未点燃：什么都不做（也不该误报过期）
	srv.tickWarrCharge(p, time.Now())

	p.fireHit = true
	p.fireHitAt = time.Now().Add(-fireHitLife + 5*time.Second)
	srv.tickWarrCharge(p, time.Now())
	if !p.fireHit {
		t.Error("还没到 20 秒不该作废")
	}

	p.fireHitAt = time.Now().Add(-fireHitLife - time.Second)
	srv.tickWarrCharge(p, time.Now())
	if p.fireHit {
		t.Error("超过 20 秒应作废")
	}
}

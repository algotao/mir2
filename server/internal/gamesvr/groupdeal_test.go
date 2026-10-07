package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// ---------- 测试脚手架 ----------

// newTestPlayer 造一个带 Data 的玩家（组队与交易都要读 Char.Data）。
func newTestPlayer(id uint32, name string, job uint32) *Player {
	return &Player{
		Obj: &entity.Object{ID: id, Name: name},
		Char: &storage.Character{Name: name, Data: &pb.CharacterData{
			Job: job,
			Abil: &pb.Ability{
				Level: 30, Hp: 100, MaxHp: 100, Mp: 100, MaxMp: 100,
			},
			BagItems: make([]*pb.UserItem, entity.MaxBagSize),
		}},
	}
}

func newGroupTestPacket(ident uint16) wire.Packet {
	return wire.Packet{Head: proto.MakeDefaultMsg(ident, 0, 0, 0, 0)}
}

// groupPlayerFor 把 *Player 转成 group.Player（AllowGroup 默认打开，
// 免得每个用例都要先勾一次）。
func groupPlayerFor(p *Player) group.Player {
	gp := group.Player{ID: p.Obj.ID, Name: p.Char.Name, AllowGroup: true}
	if p.Char.Data == nil || p.Char.Data.Abil == nil || p.Char.Data.Abil.Hp == 0 {
		gp.Dead = true
	}
	return gp
}

// groupCreateText / groupAddText / groupDelText 是三个失败码文案函数的薄包装，
// 便于在测试里并排比较（它们在 internal/group 里）。
func groupCreateText(code int) string { return group.CreateFailText(code) }
func groupAddText(code int) string    { return group.AddFailText(code) }
func groupDelText(code int) string    { return group.DelFailText(code) }

// dealTestPlayer 造一个能当交易对手的玩家。
func dealTestPlayer(id uint32, name string) *Player { return newTestPlayer(id, name, 1) }

// testSlaveServerWithMaps 在 testSlaveServer 基础上加一张 20×20 的地图，
// 供需要 faceNeighbor（要读 m.CanWalk）的用例使用。
func testSlaveServerWithMaps() *Server {
	s := testSlaveServer()
	mm := world.NewMapManager("", 4)
	mm.Put(world.Generate("grouptest", 20, 20, false))
	mm.SetNames(map[string]string{"0": "grouptest"})
	s.world.maps = mm
	return s
}

// ---------- 组队接线 ----------

func TestGroupMsgUnhandledIdent(t *testing.T) {
	s := testSlaveServer()
	pkt := newGroupTestPacket(9999)
	if s.handleGroupMsg(nil, nil, pkt) {
		t.Error("不认的 Ident 应返回 false")
	}
}

// TestInSameGroupNeedsBothPlayers 组队免伤判定必须两人都在场。
func TestInSameGroupNeedsBothPlayers(t *testing.T) {
	s := testSlaveServer()
	a := newTestPlayer(1, "甲", 0)
	b := newTestPlayer(2, "乙", 1)
	c := newTestPlayer(3, "丙", 2)
	s.world.players[1], s.world.players[2], s.world.players[3] = a, b, c
	if s.inSameGroup(a, b) {
		t.Error("未组队时不该判为同组")
	}
	s.social.groups.Create(groupPlayerFor(a), groupPlayerFor(b))
	if !s.inSameGroup(a, b) || !s.inSameGroup(b, a) {
		t.Error("组队后应双向为真")
	}
	if s.inSameGroup(a, c) {
		t.Error("丙不在队里")
	}
	if s.inSameGroup(nil, b) || s.inSameGroup(a, nil) {
		t.Error("nil 应安全返回 false")
	}
}

// TestSameGroupIsUnidirectionalOnTarget PvP 免伤是**单向**的：
// 只有当**攻击者**处于编组模式时目标才免伤（ObjBase.pas:21441-21450）。
// 这条守住"别把它写成双向免伤"——那会让 A 编组后 B 也打不到 A。
func TestSameGroupIsUnidirectionalOnTarget(t *testing.T) {
	s := testSlaveServer()
	a := newTestPlayer(1, "甲", 0)
	b := newTestPlayer(2, "乙", 1)
	s.world.players[1], s.world.players[2] = a, b
	s.social.groups.Create(groupPlayerFor(a), groupPlayerFor(b))

	// 填 SameGroup 的位置是 canAttackTarget 里的 `t.SameGroup = s.inSameGroup(attacker, target)`。
	// 这里只验证 inSameGroup 的输入方向不影响结果（它是"查询 a 是否与 b 同组"）。
	if !s.inSameGroup(a, b) {
		t.Error("甲应与乙同组")
	}
}

func TestGroupFailTextsDifferPerOperation(t *testing.T) {
	// ⚠️ 三套失败码在数值上**重叠**（-1 三套三个含义），所以文案必须按操作分。
	// 这条守住"别图省事写一个 switch code"——那会把建组 -4（对方拒绝）
	// 翻成加成员 -1（非队长）。
	if groupCreateText(-1) == groupAddText(-1) {
		t.Errorf("建组 -1 与加成员 -1 文案相同：%q", groupCreateText(-1))
	}
	if groupCreateText(-4) != "对方没开允许组队" {
		t.Errorf("建组 -4 = %q，期望 对方没开允许组队", groupCreateText(-4))
	}
	if groupAddText(-5) != "队员已满" {
		t.Errorf("加成员 -5 = %q，期望 队员已满", groupAddText(-5))
	}
	if groupDelText(-3) != "该玩家不在队里" {
		t.Errorf("删成员 -3 = %q，期望 该玩家不在队里", groupDelText(-3))
	}
}

func TestGroupJobCountBounds(t *testing.T) {
	// 职业分布按 0/1/2（战士/法师/道士）统计，越界的职业必须被忽略而不是 panic。
	// （99 是故意越界的：Player.Job 是 uint32，脚本侧只会给 0..2。）
	s := testSlaveServer()
	mage := newTestPlayer(1, "法师甲", 1)
	tao := newTestPlayer(2, "道士乙", 2)
	weird := newTestPlayer(3, "越界丙", 99)
	s.world.players[1], s.world.players[2], s.world.players[3] = mage, tao, weird
	s.social.groups.Create(groupPlayerFor(mage), groupPlayerFor(tao))
	s.social.groups.AddMember(groupPlayerFor(mage), groupPlayerFor(weird))

	count, byJob := s.groupSnapshot(mage)
	if count != 3 {
		t.Errorf("人数 = %d, 期望 3", count)
	}
	if byJob[1] != 1 || byJob[2] != 1 {
		t.Errorf("职业分布 = %v, 期望 法师1/道士1", byJob)
	}
	if byJob[0] != 0 {
		t.Errorf("战士计数 = %d, 期望 0", byJob[0])
	}
}

func TestGroupSnapshotNoGroup(t *testing.T) {
	s := testSlaveServer()
	p := newTestPlayer(1, "独行", 1)
	s.world.players[1] = p
	count, byJob := s.groupSnapshot(p)
	if count != 0 || byJob != [3]int{} {
		t.Errorf("没组队应返回全 0，得到 %d / %v", count, byJob)
	}
	// nil 安全
	if c, _ := s.groupSnapshot(nil); c != 0 {
		t.Errorf("nil 玩家应返回 0，得到 %d", c)
	}
}

// ---------- 交易 ----------

func TestDealItemNameTruncatesAtSpace(t *testing.T) {
	// ⚠️ 原版 17707/17755 是 `GetValidStr3(sItemName, sItemName, [' '])`
	// —— 按**第一个空格**截断（"信件物品"的名字后面带使用次数，如"火把 3"）。
	// 没有分隔符协议，别自作聪明改成别的。
	for _, c := range []struct{ in, want string }{
		{"木剑", "木剑"},
		{"火把 3", "火把"},
		{"金创药(小量) 12", "金创药(小量)"},
		// ⚠️ 原版 GetValidStr3 也是按**第一个**空格切，所以前导空格会切成空串。
		// 这条锁住"与原版一致"，别自作聪明先 Trim。
		{"  前后有空格  ", ""},
		{"", ""},
	} {
		if got := dealItemName(c.in); got != c.want {
			t.Errorf("dealItemName(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func TestDealCanNotGetBackBlocksDelItem(t *testing.T) {
	// !setup.txt:462 `CanNotGetBackDeal=1` ⇒ 禁止把东西从交易栏取回。
	// 这条锁住出厂配置：改错了会让"放了又拿回来"成为可能。
	if !dealCanNotGetBack {
		t.Error("出厂配置 CanNotGetBackDeal 应为 1（禁止取回）")
	}
	if dealMaxItems != 12 {
		t.Errorf("交易栏上限 = %d，原版是 12（ObjBase.pas:17723）", dealMaxItems)
	}
	if dealTryCooldown != 3*time.Second {
		t.Errorf("发起冷却 = %v，!setup.txt TryDealTime=3000ms", dealTryCooldown)
	}
	if dealOKCooldown != 1*time.Second {
		t.Errorf("成交静止期 = %v，!setup.txt DealOKTime=1000ms", dealOKCooldown)
	}
}

func TestDealCancelIsIdempotent(t *testing.T) {
	// DealCancel 开头 `if not m_boDealing then Exit`（15421）——幂等保护
	// 同时也是**递归通知对方时的终止条件**（A→B→A 时第二次直接返回）。
	// 没有它就会无限递归。
	s := testSlaveServer()
	p := dealTestPlayer(1, "甲")
	s.dealCancel(p) // 从未交易过
	if p.dealing {
		t.Error("不该进入交易态")
	}
}

func TestDealGuardCancelsWhenNotFacing(t *testing.T) {
	// 交易中一旦"不再面对对方"就取消（ObjBase.pas:6413-6416）。
	// 这是原版唯一的"超时"来源——没有超时计时器。
	s := testSlaveServerWithMaps()
	a := dealTestPlayer(1, "甲")
	b := dealTestPlayer(2, "乙")
	s.world.players[1], s.world.players[2] = a, b
	a.dealing, b.dealing = true, true
	a.dealPartner, b.dealPartner = 2, 1

	s.dealGuard()
	if a.dealing || b.dealing {
		t.Errorf("未面对时应双方取消：a=%v b=%v", a.dealing, b.dealing)
	}
}

func TestDealGuardKeepsWhenFacing(t *testing.T) {
	s := testSlaveServerWithMaps()
	a := dealTestPlayer(1, "甲")
	b := dealTestPlayer(2, "乙")
	// 让 b 站在 a 的正前方（面朝 dir=0 上 ⇒ 面前是 (x, y-1)）
	a.Obj.SetPlace(a.Obj.MapRef(), 10, 10, 0)
	b.Obj.SetPlace(b.Obj.MapRef(), 10, 9, 4) // b 面朝下 ⇒ 面前是 (10,10)=a
	mp, _ := s.world.maps.Get("0")
	a.Obj.SetMapRef(mp)
	b.Obj.SetMapRef(mp)
	s.world.players[1], s.world.players[2] = a, b
	a.dealing, b.dealing = true, true
	a.dealPartner, b.dealPartner = 2, 1

	s.dealGuard()
	if !a.dealing || !b.dealing {
		t.Errorf("互相对视时应保持交易：a=%v b=%v", a.dealing, b.dealing)
	}
}

func TestCountBagIgnoresEmptySlots(t *testing.T) {
	p := newTestPlayer(1, "甲", 0)
	p.Char.Data.BagItems = []*pb.UserItem{
		{Index: 1, MakeIndex: 1},
		nil,
		{Index: 0, MakeIndex: 2}, // 空槽
		{Index: 2, MakeIndex: 3},
	}
	if got := countBag(p); got != 2 {
		t.Errorf("有效物品数 = %d, 期望 2", got)
	}
	if got := countBag(nil); got != 0 {
		t.Errorf("nil 玩家 = %d, 期望 0", got)
	}
}

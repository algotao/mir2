package group

import "testing"

// 造一批测试用玩家。
func pl(id uint32, allow bool) Player {
	return Player{ID: id, Name: "p" + itoa(id), AllowGroup: allow}
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func names(m *Manager, leader uint32) []string {
	out := []string{}
	for _, id := range m.Members(leader) {
		out = append(out, "p"+itoa(id))
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCreateSuccess(t *testing.T) {
	m := New()
	if code, ok := m.Create(pl(1, false), pl(2, true)); !ok || code != 0 {
		t.Fatalf("建组 code=%d ok=%v，期望 0/true", code, ok)
	}
	// 索引 0 必须是队长自己（与原版 m_GroupMembers 同序）
	if got := names(m, 1); !eq(got, []string{"p1", "p2"}) {
		t.Errorf("成员 = %v，期望 [p1 p2]", got)
	}
	if !m.IsLeader(1) || m.IsLeader(2) {
		t.Error("队长判定错")
	}
	if m.LeaderOf(2) != 1 {
		t.Errorf("p2 的队长 = %d，期望 1", m.LeaderOf(2))
	}
	if !m.IsMember(1, 2) || !m.IsMember(2, 1) {
		t.Error("IsMember 应双向为真（原版是一次查询两个方向）")
	}
	// ⚠️ 队长自己建组后 m_boAllowGroup 被**强制置 True**（原版 L17573）——
	// 不管建组前是什么值。
	if !m.IsMember(1, 1) {
		t.Error("队长应与自己在同组")
	}
}

func TestCreateFailCodes(t *testing.T) {
	// 逐条对齐 ObjBase.pas:17545-17567，**顺序**也要对：
	// 已在队里(-1) 先于 目标非法(-2)。
	m := New()
	m.Create(pl(1, true), pl(2, true))

	if code, _ := m.Create(pl(2, true), pl(3, true)); code != CreateFailAlreadyInGroup {
		t.Errorf("自己已在队里 → %d，期望 %d", code, CreateFailAlreadyInGroup)
	}
	// 目标非法：ID=0 / 目标是自己 / 目标已死
	if code, _ := m.Create(pl(9, true), pl(0, true)); code != CreateFailTargetBad {
		t.Errorf("目标不存在 → %d，期望 %d", code, CreateFailTargetBad)
	}
	if code, _ := m.Create(pl(9, true), pl(9, true)); code != CreateFailTargetBad {
		t.Errorf("目标是自己 → %d，期望 %d", code, CreateFailTargetBad)
	}
	if code, _ := m.Create(pl(9, true), Player{ID: 3, Dead: true}); code != CreateFailTargetBad {
		t.Errorf("目标已死 → %d，期望 %d", code, CreateFailTargetBad)
	}
	// 目标已在队里
	if code, _ := m.Create(pl(9, true), pl(2, true)); code != CreateFailTargetInGroup {
		t.Errorf("目标已在队里 → %d，期望 %d", code, CreateFailTargetInGroup)
	}
	// 目标没开允许
	if code, _ := m.Create(pl(9, true), pl(5, false)); code != CreateFailTargetRefuse {
		t.Errorf("目标没开允许 → %d，期望 %d", code, CreateFailTargetRefuse)
	}
	// -3 的真实含义是"对方已在队里"，**不是**客户端文案写的"队伍名称重复"
	if CreateFailTargetInGroup != -3 || CreateFailTargetRefuse != -4 {
		t.Errorf("建组失败码错位: -3 应为已在队里、-4 应为拒绝，实际 %d/%d",
			CreateFailTargetInGroup, CreateFailTargetRefuse)
	}
}

func TestAddMemberAndHardMaxEleven(t *testing.T) {
	// ⚠️ 实际能到 **11** 人：原版检查是 `Count > MaxMembers`（ObjBase.pas:17591），
	// 即"已有 10 人时才允许加第 11 个"。这条是本组最容易写错的地方。
	m := New()
	m.Create(pl(1, true), pl(2, true)) // 2 人
	for i := uint32(3); i <= 11; i++ {
		if code, ok := m.AddMember(pl(1, true), pl(i, true)); !ok {
			t.Fatalf("加第 %d 人失败 code=%d（上限应为 11）", i, code)
		}
	}
	if n := m.Count(1); n != 11 {
		t.Errorf("人数 = %d，期望 11（HardMax）", n)
	}
	// 第 12 人必须失败
	if code, _ := m.AddMember(pl(1, true), pl(12, true)); code != AddFailGroupFull {
		t.Errorf("第 12 人 → %d，期望 %d", code, AddFailGroupFull)
	}
}

func TestAddMemberFailOrder(t *testing.T) {
	// ⚠️ 人数检查排在**第 2 位**（在确认目标是否合法之前），原版如此。
	m := New()
	m.Create(pl(1, true), pl(2, true))
	if code, _ := m.AddMember(pl(2, true), pl(3, true)); code != AddFailNotLeader {
		t.Errorf("非队长加人 → %d，期望 %d", code, AddFailNotLeader)
	}
	if code, _ := m.AddMember(pl(1, true), pl(0, true)); code != AddFailTargetBad {
		t.Errorf("目标不存在 → %d，期望 %d", code, AddFailTargetBad)
	}
	if code, _ := m.AddMember(pl(1, true), pl(2, true)); code != AddFailTargetInGroup {
		t.Errorf("目标已在队里 → %d，期望 %d", code, AddFailTargetInGroup)
	}
	if code, _ := m.AddMember(pl(1, true), pl(7, false)); code != AddFailTargetRefuse {
		t.Errorf("目标没开允许 → %d，期望 %d", code, AddFailTargetRefuse)
	}
}

func TestRemoveAndAutoDisband(t *testing.T) {
	m := New()
	m.Create(pl(1, true), pl(2, true))
	m.AddMember(pl(1, true), pl(3, true))

	if code, _, _ := m.Remove(2, 3); code != DelFailNotLeader {
		t.Errorf("非队长踢人 → %d，期望 %d", code, DelFailNotLeader)
	}
	if code, _, _ := m.Remove(1, 0); code != DelFailNoSuchUser {
		t.Errorf("踢空 → %d，期望 %d", code, DelFailNoSuchUser)
	}
	if code, _, _ := m.Remove(1, 9); code != DelFailNotMember {
		t.Errorf("踢非队员 → %d，期望 %d", code, DelFailNotMember)
	}

	// 3 人踢到 2 人：**不**散队
	_, dissolved, _ := m.Remove(1, 2)
	if dissolved {
		t.Error("还剩 2 人时不应散队")
	}
	if m.Count(1) != 2 {
		t.Errorf("人数 = %d, 期望 2", m.Count(1))
	}

	// 再踢到只剩 1 人 ⇒ **自动散队**（CancelGroup ObjBase.pas:21647-21660）
	_, dissolved, _ = m.Remove(1, 3)
	if !dissolved {
		t.Error("人数掉到 1 应自动散队")
	}
	if m.InGroup(1) || m.InGroup(3) {
		t.Error("散队后仍被判为在队")
	}
	if m.Get(1) != nil {
		t.Error("散队后 Get 应返回 nil")
	}
}

func TestQuitLeaderMustKickFirst(t *testing.T) {
	// ⚠️ 队长**不能直接退队**，原版只发一句英文提示然后什么都不做
	// （ClientGroupClose ObjBase.pas:17533）。
	m := New()
	m.Create(pl(1, true), pl(2, true))
	if code, ok := m.Quit(1); ok || code != QuitFailLeaderMustKick {
		t.Errorf("队长退队 → (%q,%v)，期望 (%q,false)", code, ok, QuitFailLeaderMustKick)
	}
	if !m.InGroup(1) {
		t.Error("队长退队失败后应仍在队里")
	}

	// 普通成员可以退，且退到 ≤1 人时自动散队
	if code, ok := m.Quit(2); !ok || code != "" {
		t.Errorf("成员退队 → (%q,%v)，期望 (\"\",true)", code, ok)
	}
	if m.InGroup(1) || m.InGroup(2) {
		t.Error("退队后应已散队")
	}
}

func TestQuitNotInGroup(t *testing.T) {
	m := New()
	// 不在队：原版把 m_boAllowGroup 置 False 后 Exit，**不发包也不报错**
	if code, ok := m.Quit(5); !ok || code != "" {
		t.Errorf("不在队时退队 → (%q,%v)，期望 (\"\",true)", code, ok)
	}
}

func TestDropOnDeathAndLogout(t *testing.T) {
	// ObjBase.pas:21040-21044：人物死亡立即退组（防组队刷经验）
	m := New()
	m.Create(pl(1, true), pl(2, true))
	m.AddMember(pl(1, true), pl(3, true))
	if !m.Drop(2) {
		t.Fatal("Drop 应返回 true")
	}
	if m.IsMember(1, 2) {
		t.Error("死亡退组后仍是队友")
	}
	if !m.IsMember(1, 3) {
		t.Error("别人不该被牵连退组")
	}
	// 队长跑 ⇒ 整队解散（原版心跳 ObjBase.pas:4113-4132）
	if !m.Drop(1) {
		t.Fatal("队长 Drop 应返回 true")
	}
	if m.InGroup(3) {
		t.Error("队长消失后成员应被清空")
	}
}

func TestSweepLeaderDead(t *testing.T) {
	// 原版心跳：队长死亡/幽灵 ⇒ 所有成员的 m_GroupOwner 置 nil。
	dead := map[uint32]bool{}
	m := New()
	m.Create(pl(1, true), pl(2, true))
	m.AddMember(pl(1, true), pl(3, true))
	dead[1] = true
	dropped := m.Sweep(func(id uint32) bool { return dead[id] })
	if len(dropped) != 3 {
		t.Errorf("清理 %d 人，期望 3", len(dropped))
	}
	if m.InGroup(2) || m.InGroup(3) {
		t.Error("队长死亡后成员应全部脱离")
	}
}

func TestSweepMemberDead(t *testing.T) {
	dead := map[uint32]bool{2: true}
	m := New()
	m.Create(pl(1, true), pl(2, true))
	m.AddMember(pl(1, true), pl(3, true))
	dropped := m.Sweep(func(id uint32) bool { return dead[id] })
	if len(dropped) != 1 || dropped[0] != 2 {
		t.Errorf("清理 = %v，期望 [2]", dropped)
	}
	if !m.IsMember(1, 3) {
		t.Error("活着的人不该被踢")
	}
}

func TestMembersBodyHasTrailingSlash(t *testing.T) {
	// ⚠️ 尾随斜杠不能省：客户端 GetValidStr3 靠它判结束
	// （MirClient/ClMain.pas:6357-6370）。
	if got := MembersBody([]string{"A", "B", "C"}); got != "A/B/C/" {
		t.Errorf("body = %q，期望 %q", got, "A/B/C/")
	}
	if got := MembersBody(nil); got != "" {
		t.Errorf("空名单 body = %q，期望空串（客户端见到空串才清空列表）", got)
	}
	if got := MembersBody([]string{"独苗"}); got != "独苗/" {
		t.Errorf("body = %q，期望 %q", got, "独苗/")
	}
}

func TestExpBonusTable(t *testing.T) {
	// ObjBase.pas:15564 的 bonus 表
	// ⚠️ 索引 = 人数：2 人 1.3、3 人 1.4、…、10 人 2.1、11 人 2.2。
	for _, c := range []struct {
		n    int
		want float64
	}{{0, 1}, {1, 1.2}, {2, 1.3}, {3, 1.4}, {9, 2.0}, {10, 2.1}, {11, 2.2}} {
		if got := ExpBonus[c.n]; got != c.want {
			t.Errorf("ExpBonus[%d] = %v, 期望 %v", c.n, got, c.want)
		}
	}
	if HardMax != 11 {
		t.Errorf("HardMax = %d，期望 11（GROUPMAX）", HardMax)
	}
}

func TestDistributeExpSingle(t *testing.T) {
	// n <= 1 ⇒ **不共享**，全额给杀怪者（ObjBase.pas:15580 的 `n > 1` 条件）
	r := DistributeExp(1, "m0", 100, 100,
		[]ExpMember{{ID: 1, Level: 30, Map: "m0", X: 100}}, false)
	if r.Shared {
		t.Error("一个人不该共享")
	}
	if r.Award[1] != 100 {
		t.Errorf("Award[1] = %d, 期望 100", r.Award[1])
	}
}

func TestDistributeExpByLevel(t *testing.T) {
	// 3 人 ⇒ bonus[3] = **1.4**（索引就是人数，见 ExpBonus 注释），
	// 100 * 1.4 = 140 → 30/50 = 84，15/50 = 42，5/50 = 14
	members := []ExpMember{
		{ID: 1, Level: 30, Map: "m0", X: 100},
		{ID: 2, Level: 15, Map: "m0", X: 101},
		{ID: 3, Level: 5, Map: "m0", X: 99},
	}
	r := DistributeExp(1, "m0", 100, 100, members, false)
	if !r.Shared {
		t.Fatal("3 人同图应共享")
	}
	if r.Valid != 3 || r.Multiplier != 1.4 || r.LevelSum != 50 {
		t.Errorf("n=%d mult=%v sum=%d，期望 3/1.4/50", r.Valid, r.Multiplier, r.LevelSum)
	}
	if r.Award[1] != 84 {
		t.Errorf("30 级分到 %d，期望 84", r.Award[1])
	}
	if r.Award[2] != 42 {
		t.Errorf("15 级分到 %d，期望 42（140*15/50）", r.Award[2])
	}
	if r.Award[3] != 14 {
		t.Errorf("5 级分到 %d，期望 14", r.Award[3])
	}
}

func TestDistributeExpAverage(t *testing.T) {
	// Setup/HighLevelKillMonFixExp=1 ⇒ 平均分 exp*bonus[n]/n（不做等级比例）
	// 2 人 ⇒ bonus[2]=1.3 ⇒ 100*1.3=130，130/2=65
	members := []ExpMember{
		{ID: 1, Level: 30, Map: "m0", X: 100},
		{ID: 2, Level: 5, Map: "m0", X: 100},
	}
	r := DistributeExp(1, "m0", 100, 100, members, true)
	if r.Award[1] != 65 || r.Award[2] != 65 { // 130/2
		t.Errorf("平均分 = %d/%d，期望 65/65", r.Award[1], r.Award[2])
	}
}

func TestDistributeExpAverageRounds(t *testing.T) {
	// 平均分支原版是 `WinExp(Round(dwExp / n))`：**有小数，必须四舍五入**。
	// 直接整除（floor）在 total 除不尽时会少发 1 点。
	// 3 人 ⇒ bonus[3]=1.4 ⇒ 100*1.4=140；140/3=46.67 ⇒ Round=47（floor 会给 46）。
	members := []ExpMember{
		{ID: 1, Level: 10, Map: "m0", X: 100},
		{ID: 2, Level: 20, Map: "m0", X: 100},
		{ID: 3, Level: 30, Map: "m0", X: 100},
	}
	r := DistributeExp(1, "m0", 100, 100, members, true)
	if r.Multiplier != 1.4 {
		t.Fatalf("mult = %v，期望 1.4", r.Multiplier)
	}
	for _, id := range []uint32{1, 2, 3} {
		if r.Award[id] != 47 {
			t.Errorf("成员 %d 分到 %d，期望 47（Round(140/3)，整除会给 46）", id, r.Award[id])
		}
	}
}

func TestDistributeExpRangeUsesXAxisOnly(t *testing.T) {
	// ⚠️ 原版的 Y 判定写成了 X（ObjBase.pas:15577/15587）：
	// `abs(m_nCurrX - 成员.m_nCurrX) <= 12` 出现两次，**纵向不设限**。
	// 这条守住"别顺手修正成看两个轴"——改了会让 Y 相距很远的队员突然分不到经验。
	members := []ExpMember{
		{ID: 1, Level: 10, Map: "m0", X: 100},
		{ID: 2, Level: 10, Map: "m0", X: 100}, // 同 X，Y 差 999（ExpMember 不带 Y，故意）
	}
	r := DistributeExp(1, "m0", 100, 100, members, false)
	if !r.Shared {
		t.Error("同 X 的两名成员应共享（纵向不设限）")
	}

	// X 差 13 就该出队
	out := []ExpMember{
		{ID: 1, Level: 10, Map: "m0", X: 100},
		{ID: 2, Level: 10, Map: "m0", X: 113},
	}
	r2 := DistributeExp(1, "m0", 100, 100, out, false)
	if r2.Shared {
		t.Error("X 差 13 超出 ExpShareRange=12，不该共享")
	}
}

func TestDistributeExpFiltersDeadAndOtherMap(t *testing.T) {
	members := []ExpMember{
		{ID: 1, Level: 10, Map: "m0", X: 100},
		{ID: 2, Level: 10, Map: "m1", X: 100}, // 不同图
		{ID: 3, Level: 10, Map: "m0", X: 100, Dead: true},
	}
	r := DistributeExp(1, "m0", 100, 100, members, false)
	if r.Shared {
		t.Error("只有 1 个有效成员 ⇒ 不该共享")
	}
	if r.Award[1] != 100 {
		t.Errorf("Award[1] = %d，期望 100", r.Award[1])
	}
}

func TestZeroLevelSumFallsBackToSolo(t *testing.T) {
	// 等级和为 0（理论上不可能，但别除以 0 崩掉）
	r := DistributeExp(1, "m0", 0, 100, []ExpMember{
		{ID: 1, Level: 0, Map: "m0", X: 0},
		{ID: 2, Level: 0, Map: "m0", X: 0},
	}, false)
	if r.Shared {
		t.Error("等级和为 0 时不该共享")
	}
}

func TestMembershipSurvivesRejoin(t *testing.T) {
	// 退队 → 重新被别人拉进另一个队：owner 索引必须跟着改
	m := New()
	m.Create(pl(1, true), pl(2, true))
	m.Quit(2)
	if m.InGroup(2) {
		t.Fatal("退队后不该在队")
	}
	m.Create(pl(3, true), pl(2, true))
	if m.LeaderOf(2) != 3 {
		t.Errorf("p2 的队长 = %d，期望 3", m.LeaderOf(2))
	}
	if m.IsMember(2, 1) {
		t.Error("不该还与旧队有关")
	}
}

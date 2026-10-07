// Package group 实现组队（Group）系统的纯逻辑部分。
//
// # 原版没有 TGroup 类（这是最反直觉的一点）
//
// 1.76 把组队数据挂在**玩家对象**上（ObjBase.pas:150、1326）：
//
//	m_GroupOwner: TBaseObject   // 组长指针。非队长时指向队长，队长时指向自己。
//	m_GroupMembers: TStringList // **只有队长持有**，成员表；Strings[i]=角色名，Objects[i]=对象
//
// 所以"队伍"= 队长的 `m_GroupMembers`，任何查询都要先拿 `m_GroupOwner` 再取
// `Owner.m_GroupMembers`（IsGroupMember，ObjBase.pas:2151-2166）。
// 本包把它规范化成一个 Manager（map[队长ID]*Group + 反向索引），行为照旧。
//
// # 三个必须照抄的原版怪癖
//
//  1. **人数上限实际是 11 不是 10**。检查写的是
//     `if m_GroupMembers.Count > nGroupMembersMax then FAIL`（ObjBase.pas:17591），
//     即"已有 10 人时才能加第 11 个"，加完变 11。`Grobal2.pas:1049` 的
//     `GROUPMAX = 11` 正是经验加成数组的下标上界——11 是有意选的。
//  2. **没有邀请/同意流程**。`CM_CREATEGROUP` 直接把对方拉进来，唯一条件是
//     对方开了"允许组队"（`m_boAllowGroup`，由 `CM_GROUPMODE` 切）。
//     没有"被邀请""同意组队"这些消息。
//  3. **队长不能直接退队**，必须先一个个删成员（ClientGroupClose，ObjBase.pas:17533）。
//     且 `CancelGroup` 在人数 ≤1 时**自动散队**（ObjBase.pas:21647-21660）。
//
// # 明确原版"没做"的（别自创）
//
//   - 组队名称：**没有**这个概念（服务端回的 -3 文案写"队伍名称重复"，
//     实际判的是"对方已经在队里"——文案是历史遗留）。
//   - 组队聊天：原版**没有**玩家发送组消息的协议，只有加入/退出的系统提示。
//   - 组内 HP/MP 同步：没有。`m_GroupMembers` 全仓库 40 处引用里没有一处碰 HP。
//   - `CHECKGROUPMEMBER` 脚本指令：没有。`GROUPMOVE` 只有常量、**无解析无分发**。
package group

import (
	"errors"
	"sync"
)

// MaxMembers 是人数上限的**配置值**（Setup/GroupMembersMax，出厂 10，
// MirServer/!Setup.txt:113）。
//
// ⚠️ 实际能到 **11 人**：原版检查是 `Count > Max` 才拒（ObjBase.pas:17591），
// 即"已有 10 人时才允许加第 11 个"。经验加成数组也印证了这点
// （`GROUPMAX = 11`，Grobal2.pas:1049）。
const MaxMembers = 10

// HardMax 是实际上限（MaxMembers + 1）。经验加成表按它开长度。
const HardMax = MaxMembers + 1

// ExpShareRange 是经验共享的距离上限（ObjBase.pas:15577 的 `<= 12`）。
//
// ⚠️ 原版这里有个 **BUG**：X 与 Y 都写成了 `abs(m_nCurrX - 成员.m_nCurrX) <= 12`
// ——Y 那一行比较的仍然是 X（15577 与 15587 两处都是）。所以真实判定是
// **只看 X 方向、横向 12 格内就算有效**，纵向不设限。
// 照抄（下面的 OnlyXAxis 注释），别"顺手修正"成看两个轴——那会让 Y 相距很远
// 的队员突然分不到经验。
const ExpShareRange = 12

// 分组失败原因码。⚠️ 两组编号**方向相反**且含义不重叠，别混用。
const (
	// 建组（CM_CREATEGROUP → SM_CREATEGROUP_FAIL，ObjBase.pas:17545-17567）
	CreateFailAlreadyInGroup = -1 // 自己已经在队里
	CreateFailTargetBad      = -2 // 对方不存在 / 是自己 / 已死亡 / 幽灵
	CreateFailTargetInGroup  = -3 // ⚠️ 对方已在队里（客户端文案写的是"队伍名称重复"，是历史遗留）
	CreateFailTargetRefuse   = -4 // 对方没开"允许组队"

	// 加成员（CM_ADDGROUPMEMBER → SM_GROUPADDMEM_FAIL，ObjBase.pas:17582-17607）
	AddFailNotLeader     = -1 // 只有队长能加
	AddFailTargetBad     = -2
	AddFailTargetInGroup = -3
	AddFailTargetRefuse  = -4
	AddFailGroupFull     = -5 // ⚠️ 判定是 `Count > Max`（不是 >=）⇒ 上限 11
	// 删成员（CM_DELGROUPMEMBER → SM_GROUPDELMEM_FAIL，ObjBase.pas:17623-17639）
	DelFailNotLeader  = -1
	DelFailNoSuchUser = -2
	DelFailNotMember  = -3
)

// 退队/关队失败的唯一原因（ClientGroupClose，ObjBase.pas:17522-17540）。
// 原版是"发一句英文提示"而不是发包，所以只有文案。
const QuitFailLeaderMustKick = "你必须先删除队员才能退出组队"

// 原版系统消息（SendGroupText 走 RM_GROUPMESSAGE → SM_SYSMESSAGE，
// ObjBase.pas:20494-20508）。文案取 !setup.txt 之外的原版串。
const (
	MsgJoined    = "[系统] %s 加入了组队。"
	MsgLeftGroup = "[系统] %s 离开了组队。"
	MsgCancelled = "[系统] 队伍已解散。"
)

// Group 是一个队伍。**只有队长持有它**，与原版 m_GroupMembers 一一对应。
type Group struct {
	// LeaderID 是队长 ActorId。
	LeaderID uint32
	// Members 是成员 ActorId，**索引 0 一定是队长自己**（与原版同）。
	Members []uint32
}

// Player 是 Manager 需要的最少信息。
type Player struct {
	ID   uint32
	Name string
	// AllowGroup 对应 m_boAllowGroup：是否允许被拉进队（CM_GROUPMODE 切换）。
	AllowGroup bool
	// Dead 对应 m_boDeath || m_boGhost：死亡/幽灵状态不能被拉进队。
	Dead bool
}

// Manager 管理所有队伍。**零值可用，内部自带锁**。
//
// # 为什么自带锁而不是"调用方加 s.mu"
//
// 组队查询（`SameGroup`）会被 `canAttackTarget` 读到，而那条路径**明确不允许持 s.mu**
// ——墙结算的注释写得很死："② 锁外判关系（canAttackTarget 不许在 s.mu 内调）"。
// 若把锁职责推给调用方，就得在"持 s.mu"和"不持 s.mu"两种上下文里各写一遍，
// 迟早有人写错一处、就是一次难查的竞态或死锁。
//
// 所以 Manager 用自己的 `sync.RWMutex`。它保证不会与 s.mu 形成环：
// **Manager 里的方法一个回调都不碰 Server**（纯数据），因此不存在
// "持 groups.mu 再去拿 s.mu"的路径。
type Manager struct {
	mu     sync.RWMutex
	groups map[uint32]*Group // 队长 ID → 队伍
	owner  map[uint32]uint32 // 任意成员 ID → 队长 ID（`m_GroupOwner` 的规范化）
}

// New 返回一个空的 Manager。
func New() *Manager {
	return &Manager{
		groups: make(map[uint32]*Group),
		owner:  make(map[uint32]uint32),
	}
}

// LeaderOf 返回该玩家的队长 ID（0 = 没组队）。对应 `m_GroupOwner`。
func (m *Manager) LeaderOf(playerID uint32) uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.owner[playerID]
}

// InGroup 报告该玩家是否在队里。
func (m *Manager) InGroup(playerID uint32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.owner[playerID] != 0
}

// Get 返回该队长主持的队伍（nil = 没有）。
func (m *Manager) Get(leaderID uint32) *Group {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.groups[leaderID]
}

// Members 返回成员列表的**副本**（nil = 没组队）。
//
// ⚠️ 原版直接返回内部 TStringList 的引用（IsGroupMember 每次都遍历），
// 我们返回副本，避免调用方在遍历时被踢人/解散改坏。
func (m *Manager) Members(leaderID uint32) []uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g := m.groups[leaderID]
	if g == nil {
		return nil
	}
	return append([]uint32(nil), g.Members...)
}

// MembersOf 返回该玩家所在队伍的全部成员（nil = 没组队）。
func (m *Manager) MembersOf(playerID uint32) []uint32 {
	return m.Members(m.LeaderOf(playerID))
}

// Count 返回队伍人数（0 = 没组队）。
func (m *Manager) Count(leaderID uint32) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if g := m.groups[leaderID]; g != nil {
		return len(g.Members)
	}
	return 0
}

// IsMember 报告 a 与 b 是否同组（IsGroupMember，ObjBase.pas:2151-2166）。
//
// ⚠️ 原版是**单向**语义：`a.IsGroupMember(b)` 遍历 `a.m_GroupOwner.m_GroupMembers`。
// 我们取 a 的队长再查表，与原版等价。
func (m *Manager) IsMember(a, b uint32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	leader := m.owner[a]
	if leader == 0 {
		return false
	}
	g := m.groups[leader]
	if g == nil {
		return false
	}
	for _, id := range g.Members {
		if id == b {
			return true
		}
	}
	return false
}

// IsLeader 报告该玩家是否是队长。
func (m *Manager) IsLeader(playerID uint32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l := m.owner[playerID]
	return l != 0 && l == playerID
}

// Create 建组并把 target 拉进来。
//
// 对应 ClientCreateGroup（ObjBase.pas:17542-17577）。判定顺序**逐条照抄**：
//
//  1. 自己已在队里           → -1
//  2. 目标不存在/是自己/已死 → -2
//  3. 目标已在队里           → -3
//  4. 目标没开"允许组队"     → -4
//
// 成功后：清空旧表 → 队长入队（索引 0）→ 目标入队 → **队长的 m_boAllowGroup
// 强制置 True**（原版 L17573，意味着"自己建的队自己一定能被加"）。
func (m *Manager) Create(leader, target Player) (code int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.owner[leader.ID] != 0 {
		return CreateFailAlreadyInGroup, false
	}
	if target.ID == 0 || target.ID == leader.ID || target.Dead {
		return CreateFailTargetBad, false
	}
	if m.owner[target.ID] != 0 {
		return CreateFailTargetInGroup, false
	}
	if !target.AllowGroup {
		return CreateFailTargetRefuse, false
	}
	g := &Group{LeaderID: leader.ID, Members: []uint32{leader.ID, target.ID}}
	m.groups[leader.ID] = g
	m.owner[leader.ID] = leader.ID
	m.owner[target.ID] = leader.ID
	return 0, true
}

// AddMember 队长往队里加人。对应 ClientAddGroupMember（ObjBase.pas:17579-17618）。
//
// ⚠️ 判定顺序里**人数检查排在第 2 位**（在确认目标是否合法之前），
// 且用的是 `Count > MaxMembers` 而非 `>=` ⇒ 实际上限 11 人。
func (m *Manager) AddMember(leader Player, target Player) (code int, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// ⚠️ 直读 m.owner 而不是调 LeaderOf：此刻已持写锁，而 sync.RWMutex
	// **不可重入**，RLock 会自死锁（表现是测试跑到这里直接超时）。
	if m.owner[leader.ID] != leader.ID {
		return AddFailNotLeader, false
	}
	g := m.groups[leader.ID]
	if g == nil {
		return AddFailNotLeader, false
	}
	if len(g.Members) > MaxMembers {
		return AddFailGroupFull, false
	}
	if target.ID == 0 || target.ID == leader.ID || target.Dead {
		return AddFailTargetBad, false
	}
	if m.owner[target.ID] != 0 {
		return AddFailTargetInGroup, false
	}
	if !target.AllowGroup {
		return AddFailTargetRefuse, false
	}
	g.Members = append(g.Members, target.ID)
	m.owner[target.ID] = leader.ID
	return 0, true
}

// Remove 队长踢掉一个成员（或自己退队）。对应 DelMember（ObjBase.pas:18936-18965）。
//
// 判定顺序照抄 ClientDelGroupMember（ObjBase.pas:17620-17645）：
//
//  1. 不是队长        → -1
//  2. 找不到该玩家    → -2
//  3. 该玩家不在队里  → -3
//
// ⚠️ 踢完人之后必须走 cancelGroup：人数掉到 ≤1 时**自动散队**
// （CancelGroup，ObjBase.pas:21647-21660）。原版散队时给全队发
// SM_GROUPCANCEL（清空客户端列表）并广播"队伍已解散"。
func (m *Manager) Remove(leader, targetID uint32) (code int, dissolved bool, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.owner[leader] != leader { // 直读，见 AddMember 的锁说明
		return DelFailNotLeader, false, false
	}
	g := m.groups[leader]
	if g == nil {
		return DelFailNotLeader, false, false
	}
	if targetID == 0 {
		return DelFailNoSuchUser, false, false
	}
	idx := -1
	for i, id := range g.Members {
		if id == targetID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return DelFailNotMember, false, false
	}
	m.owner[targetID] = 0
	g.Members = append(g.Members[:idx], g.Members[idx+1:]...)
	return 0, m.cancelGroup(g), true
}

// Quit 普通成员主动退队。对应 ClientGroupClose（ObjBase.pas:17522-17540）。
//
// ⚠️ **队长不能直接退队**：原版只发一句英文提示然后什么都不做
// （`if m_GroupOwner = Self then SysMsg('If you want to withdraw...')`），
// 必须先删成员。照抄。
func (m *Manager) Quit(playerID uint32) (code string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	leader := m.owner[playerID]
	if leader == 0 {
		return "", true // 不在队：原版把 m_boAllowGroup 置 False 后 Exit，不发包
	}
	if leader == playerID {
		return QuitFailLeaderMustKick, false
	}
	g := m.groups[leader]
	if g == nil {
		delete(m.owner, playerID)
		return "", true
	}
	for i, id := range g.Members {
		if id == playerID {
			g.Members = append(g.Members[:i], g.Members[i+1:]...)
			break
		}
	}
	delete(m.owner, playerID)
	m.cancelGroup(g)
	return "", true
}

// Drop 强制把某玩家移出队伍（不分队长/成员），用于死亡/下线。
//
// 对应 ObjBase.pas:21040-21044（**人物死亡立即退组，以防止组队刷经验**）与
// ObjBase.pas:15470-15478（Disappear 下线时 `m_GroupOwner.DelMember(Self)`）。
//
// 返回是否真的移出了。
func (m *Manager) Drop(playerID uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	leader := m.owner[playerID]
	if leader == 0 {
		return false
	}
	if leader == playerID {
		// 队长跑了：原版心跳（ObjBase.pas:4113-4132）会把所有成员的
		// m_GroupOwner 置 nil，队伍随之名存实亡。直接散队。
		m.disband(leader)
		return true
	}
	g := m.groups[leader]
	if g != nil {
		for i, id := range g.Members {
			if id == playerID {
				g.Members = append(g.Members[:i], g.Members[i+1:]...)
				break
			}
		}
	}
	delete(m.owner, playerID)
	if g != nil {
		m.cancelGroup(g)
	}
	return true
}

// disband 强制解散（不发消息）。**调用方持 m.mu**。
func (m *Manager) disband(leader uint32) {
	g := m.groups[leader]
	if g == nil {
		return
	}
	for _, id := range g.Members {
		delete(m.owner, id)
	}
	delete(m.groups, leader)
}

// cancelGroup 人数 ≤1 时自动散队，返回是否散了。
// 对应 CancelGroup（ObjBase.pas:21647-21660）。**调用方持 m.mu**。
func (m *Manager) cancelGroup(g *Group) bool {
	if g == nil || len(g.Members) > 1 {
		return false
	}
	m.disband(g.LeaderID)
	return true
}

// MembersBody 生成 SM_GROUPMEMBERS 的 body：成员名用 `/` 串联并带**尾随** `/`。
//
// 对应 SendGroupMembers（ObjBase.pas:21662-21679）：
//
//	sSENDMSG := sSENDMSG + PlayObject.m_sCharName + '/';
//
// ⚠️ 尾随斜杠不能省：客户端 `GetValidStr3(bodystr, memb, ['/'])` 靠它判结束
// （MirClient/ClMain.pas:6357-6370）。
func MembersBody(names []string) string {
	out := make([]byte, 0, len(names)*16)
	for _, n := range names {
		out = append(out, n...)
		out = append(out, '/')
	}
	return string(out)
}

// ---------- 经验共享 ----------

// ExpBonus 是人数 → 经验加成倍率（ObjBase.pas:15564）。
//
//	bonus: array[0..GROUPMAX] of Real = (1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 2, 2.1, 2.2);
//	… if n in [0..GROUPMAX] then dwExp := Round(dwExp * bonus[n]);
//
// ⚠️ **索引就是人数 n**，所以 2 人拿的是 bonus[2] = **1.3**（不是 1.2！），
// 11 人拿 bonus[11] = 2.2。bonus[0]/bonus[1] 永远用不到（n > 1 才共享）。
// 查资料时很容易把"下标 1 = 1.2"读成"2 人 = 1.2"，差一档。
var ExpBonus = [HardMax + 1]float64{1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 2, 2.1, 2.2}

// ExpShare 是经验共享的一次结算结果。
type ExpShare struct {
	// Shared 为 false 时 Reward 是给杀怪者一个人的全额。
	Shared bool
	// Valid 是有效成员数 n（距离/死亡过滤后）。
	Valid int
	// LevelSum 是有效成员的等级和。
	LevelSum uint32
	// Multiplier 是 bonus[n]（n>1 时有效）。
	Multiplier float64
	// Award 是给每个成员的经验（按等级比例或平均）。
	Award map[uint32]uint32
}

// ExpMember 是参与分配的一个成员。
type ExpMember struct {
	ID    uint32
	Level uint32
	// Map 标识（指针或地图号）。必须与杀怪者相同才参与。
	Map any
	// X 是成员横坐标。
	X int
	// Dead 对应 m_boDeath。
	Dead bool
}

// DistributeExp 按原版 GainExp（ObjBase.pas:15557-15603）分配经验。
//
// 规则（逐条照抄）：
//   - **n > 1 才共享**，否则返回全额给一人（Shared=false）。
//   - 有效成员 = 未死亡 且 同图 且 **|Δx| ≤ 12**（⚠️ 原版 Y 那行写的是 X，
//     所以纵向不设限，见 ExpShareRange 注释）。
//   - 总经验先乘 `bonus[n]`。
//   - `average=true`（Setup/HighLevelKillMonFixExp=1）时平均分 `exp/n`；
//     否则按等级比例 `exp * Level / ΣLevel`。
//   - ⚠️ 每份都是**独立取整**的（Delphi 的 Round），所以总和可能略大于或小于总数。
//
// killerID 是杀怪者，killerMap/killerX 是它的图与横坐标。
func DistributeExp(killerID uint32, killerMap any, killerX int, exp uint32,
	members []ExpMember, average bool) ExpShare {

	live := make([]ExpMember, 0, len(members))
	var sum uint32
	for _, mb := range members {
		if mb.Dead || mb.Map != killerMap {
			continue
		}
		// ⚠️ 只看 X 轴（照抄原版的 Y 判定 bug）。
		dx := killerX - mb.X
		if dx < 0 {
			dx = -dx
		}
		if dx > ExpShareRange {
			continue
		}
		live = append(live, mb)
		sum += mb.Level
	}
	if len(live) <= 1 || sum == 0 {
		return ExpShare{Shared: false, Award: map[uint32]uint32{killerID: exp}}
	}
	n := len(live)
	mult := 1.0
	if n <= HardMax {
		mult = ExpBonus[n]
	}
	total := roundHalfAway(exp, mult)

	award := make(map[uint32]uint32, n)
	for _, mb := range live {
		var share uint32
		if average {
			// 原版是 `WinExp(Round(dwExp / n))` —— 有小数，必须四舍五入，
			// 直接整除（floor）在 `total % n != 0` 时会少发 1 点。
			share = roundHalfAwayUint(total, 1.0/float64(n))
		} else {
			share = roundHalfAwayUint(total, float64(mb.Level)/float64(sum))
		}
		award[mb.ID] += share
	}
	return ExpShare{
		Shared:     true,
		Valid:      n,
		LevelSum:   sum,
		Multiplier: mult,
		Award:      award,
	}
}

// roundHalfAway 是 Delphi 的 Round（**半远离零**，不是银行家舍入）。
//
// ⚠️ 与 `internal/delphi.Round`（银行家舍入）**不同**：Delphi 的 System.Round 走 FISTTP
// 是银行家舍入，但 ObjBase.pas:15563 那些点数/经验运算用的是同一函数，
// 数值上两者只在 .5 处不同。为了与原版一致这里用 half-away-from-zero，
// 经验值出现 .5 的概率虽低但不为零。
func roundHalfAway(v uint32, mult float64) uint32 {
	f := float64(v) * mult
	// 四舍五入（半远离零）
	if f < 0 {
		return uint32(f - 0.5)
	}
	return uint32(f + 0.5)
}

func roundHalfAwayUint(v uint32, ratio float64) uint32 {
	f := float64(v) * ratio
	if f < 0 {
		return uint32(f - 0.5)
	}
	return uint32(f + 0.5)
}

// ---------- 队伍清理（心跳） ----------

// Sweep 在每轮心跳里调用，清掉"组长已死/已幽灵"造成的名存实亡的队伍。
//
// 对应 ObjBase.pas:4113-4132 TBaseObject.Run：
//
//	if m_GroupOwner <> nil then
//	  if m_GroupOwner.m_boDeath or m_GroupOwner.m_boGhost then m_GroupOwner := nil;
//	if m_GroupOwner = Self then
//	  for i := m_GroupMembers.Count-1 downto 0 do
//	    if 成员.m_boDeath or 成员.m_boGhost then m_GroupMembers.Delete(i);
//
// isDead 回调判断某个 ID 是否死亡/幽灵。返回被清理的成员 ID 列表（需要发包通知）。
func (m *Manager) Sweep(isDead func(uint32) bool) []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()

	var dropped []uint32
	for leader, g := range m.groups {
		// 组长死了：整队解散（成员一起清）
		if leader != g.LeaderID || isDead(leader) {
			for _, id := range g.Members {
				delete(m.owner, id)
			}
			delete(m.groups, leader)
			dropped = append(dropped, g.Members...)
			continue
		}
		// 成员死了：从名单里摘掉（原版只删名单，不动成员的 m_GroupOwner，
		// 因为死亡时已经走 DelMember 了；我们做等价处理）
		kept := g.Members[:0]
		for _, id := range g.Members {
			if id != leader && isDead(id) {
				delete(m.owner, id)
				dropped = append(dropped, id)
				continue
			}
			kept = append(kept, id)
		}
		g.Members = kept
		if len(g.Members) <= 1 {
			// 队长死了之外的情况：人数掉到 ≤1 也自动散队
			for _, id := range g.Members {
				delete(m.owner, id)
			}
			delete(m.groups, leader)
		}
	}
	return dropped
}

// ErrGroupFull 仅用于内部断言式的场景（脚本指令需要文案时用）。
var ErrGroupFull = errors.New("组队人数已满")

// ---------- 失败码文案 ----------
//
// ⚠️ 三套编号**在数值上互相重叠**（-1 在三套里是三个不同含义），
// 所以**不能**写一个 `switch code` 统一翻译——Go 会报 duplicate case，
// 硬用 if 链也会把"建组 -4 对方拒绝"翻成"加成员 -1 非队长"。
// 每个操作一个函数，语义归属就清楚了。

// CreateFailText 把 SM_CREATEGROUP_FAIL 的 Recog 翻成中文。
func CreateFailText(code int) string {
	switch code {
	case CreateFailAlreadyInGroup:
		return "自己已经在队里"
	case CreateFailTargetBad:
		return "对方不存在/是自己/已死亡"
	case CreateFailTargetInGroup:
		// ⚠️ 客户端文案写的是"队伍名称重复"（ClMain.pas:5298），是历史遗留；
		// 服务端的真实判据是"对方已经在队里"（ObjBase.pas:17557）。
		return "对方已经在队里"
	case CreateFailTargetRefuse:
		return "对方没开允许组队"
	default:
		return "未知原因"
	}
}

// AddFailText 把 SM_GROUPADDMEM_FAIL 的 Recog 翻成中文。
func AddFailText(code int) string {
	switch code {
	case AddFailNotLeader:
		return "只有队长能加成员"
	case AddFailTargetBad:
		return "对方不存在/是自己/已死亡"
	case AddFailTargetInGroup:
		return "对方已经在队里"
	case AddFailTargetRefuse:
		return "对方没开允许组队"
	case AddFailGroupFull:
		return "队员已满"
	default:
		return "未知原因"
	}
}

// DelFailText 把 SM_GROUPDELMEM_FAIL 的 Recog 翻成中文。
func DelFailText(code int) string {
	switch code {
	case DelFailNotLeader:
		return "只有队长能删成员"
	case DelFailNoSuchUser:
		return "找不到该玩家"
	case DelFailNotMember:
		return "该玩家不在队里"
	default:
		return "未知原因"
	}
}

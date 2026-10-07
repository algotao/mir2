// Package guild 实现行会状态机。
//
// 对照 Delphi Guild.pas 重写：
//   - Guild/Rank/War 数据结构在 internal/storage（存储层类型），本包只操作它；
//   - 成员管理、职务表解析与校验、联盟等逻辑在此；
//   - Manager（见 manager.go）是内存缓存 + 持久化入口，
//     MemberOfGuild 语义等价原版 TGuildManager.MemberOfGuild（Guild.pas:226）。
//
// 与客户端交互的字节级协议在 cmd/gamesvr/guild.go。
package guild

import (
	"slices"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// 职务约定（默认值取自原版 g_Config：M2Share.pas:2156-2157，
// 以 String.ini [Guild] 段可覆盖：GuildChief=行会掌门人、GuildMemberRank=行会成员）。
const (
	// ChiefRankNo 是掌门人职务号（Guild.pas:698）。
	ChiefRankNo = 1
	// MemberRankNo 是新成员的默认职务号（Guild.pas:862）。
	MemberRankNo = 99
	// ChiefRankName 是掌门人职务的默认名。
	ChiefRankName = "行会掌门人"
	// MemberRankName 是成员职务的默认名。
	MemberRankName = "行会成员"

	// MaxRankNameLen 是职务名长度上限（Guild.pas:956 硬编码 30）。
	MaxRankNameLen = 30
	// maxMembersPerLine 是职务表里一行最多能写的成员数（Guild.pas:977）。
	maxMembersPerLine = 10
	// MaxChiefs 是掌门职务的成员数上限（Guild.pas:1053-1056 的 >2 判 -4）。
	MaxChiefs = 2
	// MaxRankNo 是合法职务号上限（Guild.pas:1140 的 >99 判 -7）。
	MaxRankNo = 99
)

// UpdateRank 返回码（原版 TGUild.UpdateRank 的返回值，Guild.pas:1014-1148）。
//
// 客户端 ClMain.pas:5476-5485 按这些数值弹提示，不可改。
const (
	// RankOK 表示职务表已更新。
	RankOK = 0
	// RankNoChange 表示与服务端现有职务表完全一致（原版 -1，无任何回包）。
	RankNoChange = -1
	// RankBadChief 表示第一个职务不是 rank 1（原版 -2）。
	RankBadChief = -2
	// RankChiefNameEmpty 表示掌门职务名为空（原版 -3）。
	RankChiefNameEmpty = -3
	// RankTooManyChiefs 表示掌门职务成员超过 2 人（原版 -4）。
	RankTooManyChiefs = -4
	// RankChiefOffline 表示掌门职务成员全部离线（原版 -5）。
	RankChiefOffline = -5
	// RankMemberChanged 表示成员名单有增删（只允许改职务名/换职务，原版 -6）。
	RankMemberChanged = -6
	// RankBadRankNo 表示职务号重复或越界（原版 -7）。
	RankBadRankNo = -7
)

// New 创建一个行会，按原版 AddGuild → SetGuildInfo 的语义：
// 只建 rank 1 职务并放入掌门（Guild.pas:691-706）。
// rank 99 的默认成员职务在第一个成员加入时才创建（Guild.pas:859-866）。
func New(name, chief string) *storage.Guild {
	return &storage.Guild{
		Name: name,
		Ranks: []storage.GuildRank{
			{No: ChiefRankNo, Name: ChiefRankName, Members: []string{chief}},
		},
	}
}

// Clone 深拷贝行会。
//
// ⚠️ 变更流程必须是"改副本 → 落库 → 提交内存"（见 Manager.mutate），
// 因此拷贝必须彻底，漏拷一个切片就会让副本与缓存共享底层数组。
// ⚠️ **新增 storage.Guild 字段时必须同步这里**：手写深拷贝漏字段不会编译报错，
// 表现为"改了但读出来还是旧值"（2026-10-05 加争霸赛的 TeamFight 时踩过：
// StartTeamFight 明明调用成功，击杀时读到 TeamFight=false）。
// guild_test.go 的 TestCloneCoversAllFields 用反射兜住这条。
func Clone(g *storage.Guild) *storage.Guild {
	if g == nil {
		return nil
	}
	c := &storage.Guild{
		Name:           g.Name,
		EnableAuthAlly: g.EnableAuthAlly,
		ContestPoint:   g.ContestPoint,
		TeamFight:      g.TeamFight,
	}
	c.Notice = slices.Clone(g.Notice)
	c.Allies = slices.Clone(g.Allies)
	c.Wars = slices.Clone(g.Wars)
	c.Ranks = make([]storage.GuildRank, len(g.Ranks))
	for i, r := range g.Ranks {
		c.Ranks[i] = storage.GuildRank{No: r.No, Name: r.Name, Members: slices.Clone(r.Members)}
	}
	c.TeamFightDead = slices.Clone(g.TeamFightDead)
	return c
}

// RankOf 返回成员所在的职务；不在行会时返回 nil。
func RankOf(g *storage.Guild, member string) *storage.GuildRank {
	if g == nil || member == "" {
		return nil
	}
	for i := range g.Ranks {
		if slices.Contains(g.Ranks[i].Members, member) {
			return &g.Ranks[i]
		}
	}
	return nil
}

// IsMember 判断成员是否在行会中（等价原版 TGUild.IsMember）。
func IsMember(g *storage.Guild, member string) bool { return RankOf(g, member) != nil }

// RankNameOf 返回成员的职务名（不在行会时为空串）。
func RankNameOf(g *storage.Guild, member string) string {
	if r := RankOf(g, member); r != nil {
		return r.Name
	}
	return ""
}

// RankNoOf 返回成员的职务号（不在行会时为 0，与原版 m_nGuildRankNo 一致）。
func RankNoOf(g *storage.Guild, member string) int {
	if r := RankOf(g, member); r != nil {
		return r.No
	}
	return 0
}

// IsChief 判断成员是否是掌门（职务号 1）。等价原版 TBaseObject.IsGuildMaster。
func IsChief(g *storage.Guild, member string) bool {
	return RankNoOf(g, member) == ChiefRankNo
}

// Count 返回行会总人数（等价原版 TGUild.Count）。
func Count(g *storage.Guild) int {
	n := 0
	if g == nil {
		return 0
	}
	for _, r := range g.Ranks {
		n += len(r.Members)
	}
	return n
}

// AddMember 把成员加入 rank 99 的默认职务，没有该职务则创建（Guild.pas:842-870）。
//
// 调用方需保证成员尚未入会（原版在 ObjBase.pas:18082-18125 处校验）。
func AddMember(g *storage.Guild, member string) {
	for i := range g.Ranks {
		if g.Ranks[i].No == MemberRankNo {
			g.Ranks[i].Members = append(g.Ranks[i].Members, member)
			return
		}
	}
	g.Ranks = append(g.Ranks, storage.GuildRank{
		No:      MemberRankNo,
		Name:    MemberRankName,
		Members: []string{member},
	})
}

// DelMember 把成员从行会移除（Guild.pas:872-894）。
//
// 只删成员、不删空职务（原版行为：空职务会保留到下次改职务表）。
// 返回 false 表示该成员不在行会。
func DelMember(g *storage.Guild, member string) bool {
	for i := range g.Ranks {
		for j, m := range g.Ranks[i].Members {
			if m == member {
				g.Ranks[i].Members = append(g.Ranks[i].Members[:j], g.Ranks[i].Members[j+1:]...)
				return true
			}
		}
	}
	return false
}

// CanCancel 判断能否解散行会：只剩 1 个职务且只有 1 人，且就是操作者
// （Guild.pas:896-909 CancelGuld）。
func CanCancel(g *storage.Guild, member string) bool {
	if len(g.Ranks) != 1 {
		return false
	}
	r := g.Ranks[0]
	return len(r.Members) == 1 && r.Members[0] == member
}

// HasAlly 判断两个行会是否已结盟。
func HasAlly(g *storage.Guild, ally string) bool {
	return slices.Contains(g.Allies, ally)
}

// AtWar 判断 a 是否把 b 列为敌对（等价原版 TGUild.IsNotWarGuild，Guild.pas:1176）。
//
// ⚠️ 原版遍历的是**自己的** GuildWarList，所以方向性是真的：
// `AtWar(a, b)` 与 `AtWar(b, a)` 要分别判断（原版 ClientGuildAlly 就是两侧都判）。
// 已到期的记录不算——原版靠 dwWarTick 递减把条目摘掉，我们存绝对时刻，过期即失效。
func AtWar(a, b *storage.Guild, now time.Time) bool {
	if a == nil || b == nil {
		return false
	}
	for _, w := range a.Wars {
		if w.Name == b.Name && w.EndAt.After(now) {
			return true
		}
	}
	return false
}

// InWar 双向判定：双方是否处于行会战中（= 原版两个 IsNotWarGuild 取反的 and）。
func InWar(a, b *storage.Guild, now time.Time) bool {
	return AtWar(a, b, now) || AtWar(b, a, now)
}

// AddAlly 结盟（去重）。等价 Guild.pas:1176 附近的 AllyGuild。
func AddAlly(g *storage.Guild, ally string) {
	if !HasAlly(g, ally) {
		g.Allies = append(g.Allies, ally)
	}
}

// DelAlly 解除同盟。返回是否确实解除。
func DelAlly(g *storage.Guild, ally string) bool {
	i := slices.Index(g.Allies, ally)
	if i < 0 {
		return false
	}
	g.Allies = append(g.Allies[:i], g.Allies[i+1:]...)
	return true
}

// ParseRankData 解析客户端提交的职务表文本。
//
// 语法（原版 Guild.pas:945-979 + 客户端 FState.pas:6514-6565 的拼装）：
//
//	#<职务号> <<职务名>>
//	成员名1 成员名2 ...
//
// 职务行以 '#' 开头，职务名在 <> 中；成员行是按空格/逗号分隔的名字，
// 每行最多 10 个（多出的丢弃，同原版 Guild.pas:977）。
func ParseRankData(text string) []storage.GuildRank {
	var (
		out  []storage.GuildRank
		cur  = -1 // out 下标，-1 表示还没遇到 '#' 职务行
		line string
	)
	for text != "" {
		text, line = cutLine(text)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line[0] == '#' {
			// 职务行：#<职务号> <<职务名>>（职务号与名字之间可有空格）。
			body := line[1:]
			noStr, namePart := body, ""
			if l := strings.IndexAny(body, " <"); l >= 0 {
				noStr, namePart = body[:l], body[l:]
			}
			name, _ := cutAngle(namePart)
			name = strings.TrimSpace(name)
			if r := []rune(name); len(r) > MaxRankNameLen {
				// 原版按字节截断（Guild.pas:956）；我们按字符截，
				// 避免把多字节 UTF-8 汉字切成半个。
				name = string(r[:MaxRankNameLen])
			}
			out = append(out, storage.GuildRank{No: atoiDefault(noStr, MemberRankNo), Name: name})
			cur = len(out) - 1
			continue
		}
		if cur < 0 {
			continue
		}
		n := 0
		for line != "" && n < maxMembersPerLine {
			var name string
			line, name = cutAny(line, " ,")
			if name != "" {
				out[cur].Members = append(out[cur].Members, name)
			}
			n++
		}
	}
	return out
}

// cutLine 按 CR（#13）切出一行，返回剩余部分与当前行。
//
// ⚠️ 原版按 #13 分行（Guild.pas:948 GetValidStr3 的分隔符 [#$0D]），
// 这里同时兼容 \n，避免手工测试时整段被当成一行。
func cutLine(s string) (rest, line string) {
	i := strings.IndexAny(s, "\r\n")
	if i < 0 {
		return "", s
	}
	return s[i+1:], s[:i]
}

// cutAny 在分隔符集合的**第一个**命中处切分，并跳过其后的连续分隔符
// （等价 HUtil32.GetValidStr3 的行为；不跳的话 "a  b" 会切出空段）。
func cutAny(s, seps string) (rest, head string) {
	i := strings.IndexAny(s, seps)
	if i < 0 {
		return "", s
	}
	j := i
	for j < len(s) && strings.IndexByte(seps, s[j]) >= 0 {
		j++
	}
	return s[j:], s[:i]
}

// cutAngle 取出 <...> 中的内容（职务名）。没有尖括号时返回原串。
func cutAngle(s string) (inner, rest string) {
	l := strings.IndexByte(s, '<')
	if l < 0 {
		return s, ""
	}
	r := strings.IndexByte(s[l:], '>')
	if r < 0 {
		return s[l+1:], ""
	}
	return s[l+1 : l+r], s[l+r+1:]
}

func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// ValidateRanks 校验客户端提交的新职务表，返回值语义与原版 UpdateRank 一致
// （Guild.pas:986-1148）。
//
// online 判定成员是否在线（-5 检查用）；nil 表示不检查（全部视为在线）。
//
// ⚠️ 忠实复刻原版的几个怪异行为：
//   - -1（无变化）要求逐个职务、逐个成员按**顺序**完全一致；
//   - -5 的实现是"只要发现第一个离线成员就减一后 break"（Guild.pas:1044-1051），
//     因此只有单人掌门离线时才会命中；
//   - -6 的成员比对是**跨职务**的：允许成员在职务间移动，只禁止增减人数。
func ValidateRanks(g *storage.Guild, ranks []storage.GuildRank, online func(string) bool) int {
	if ranksEqual(g.Ranks, ranks) {
		return RankNoChange
	}

	// 掌门职务必须是第一个，且名字非空。
	if len(ranks) == 0 || ranks[0].No != ChiefRankNo {
		return RankBadChief
	}
	if ranks[0].Name == "" {
		return RankChiefNameEmpty
	}

	// 掌门人数与在线检查。
	chief := ranks[0]
	switch {
	case len(chief.Members) > MaxChiefs:
		return RankTooManyChiefs
	case len(chief.Members) <= MaxChiefs:
		n := len(chief.Members)
		for _, m := range chief.Members {
			if online != nil && !online(m) {
				n--
				break
			}
		}
		if n <= 0 {
			return RankChiefOffline
		}
	}

	// 成员增删检查：双向包含，且总人数一致（Guild.pas:1060-1131）。
	oldTotal, newTotal := 0, 0
	for _, r := range g.Ranks {
		for _, m := range r.Members {
			oldTotal++
			if !memberInAny(ranks, m) {
				return RankMemberChanged
			}
		}
	}
	for _, r := range ranks {
		for _, m := range r.Members {
			newTotal++
			if !memberInAny(g.Ranks, m) {
				return RankMemberChanged
			}
		}
	}
	if oldTotal != newTotal {
		return RankMemberChanged
	}

	// 职务号合法性与唯一性（Guild.pas:1133-1148）。
	seen := make(map[int]bool, len(ranks))
	for _, r := range ranks {
		if r.No <= 0 || r.No > MaxRankNo || seen[r.No] {
			return RankBadRankNo
		}
		seen[r.No] = true
	}
	return RankOK
}

func ranksEqual(a, b []storage.GuildRank) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].No != b[i].No || a[i].Name != b[i].Name {
			return false
		}
		if !slices.Equal(a[i].Members, b[i].Members) {
			return false
		}
	}
	return true
}

func memberInAny(ranks []storage.GuildRank, member string) bool {
	for _, r := range ranks {
		if slices.Contains(r.Members, member) {
			return true
		}
	}
	return false
}

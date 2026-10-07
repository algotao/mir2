package guild

import "github.com/algotao/mir2/server/internal/storage"

// 行会争霸赛（原版 TGUild 的 boTeamFight / nContestPoint / TeamFightDeadList）。
//
// 逐条对应 `Guild.pas`：
//
//	StartTeamFight        :1272-1276   清零积分 + 打开开关 + 清空成员表
//	EndTeamFight          :1278-1282   只关开关（**保留**积分与成员表）
//	AddTeamFightMember    :1284-1286   登记一名成员
//	TeamFightWhoDead      :771-784     记一次阵亡（只记表里的成员）
//	TeamFightWhoWinPoint  :786-800     加个人积分与该行会积分
//
// ⚠️ 两个记账函数都有 `if not boTeamFight then Exit` 的前置 —— 争霸赛没开时
// 阵亡/击杀都不计数（这也是 e2e 里"必须先 @StartContest"的原因）。
//
// ⚠️ 原版把"阵亡次数/个人得分"打包进一个 Integer（低 16 位 / 高 16 位）塞在
// TList.Objects 里，我们用storage.GuildTeamFightMember 的两个字段表达（等价）。

// StartTeamFight 对应 `TGUild.StartTeamFight`（Guild.pas:1272-1276）。
func StartTeamFight(g *storage.Guild) {
	if g == nil {
		return
	}
	g.ContestPoint = 0
	g.TeamFight = true
	g.TeamFightDead = nil
}

// EndTeamFight 对应 `TGUild.EndTeamFight`（Guild.pas:1278-1282）。
//
// 只关开关：积分与成员表要留着，`@EndContest` 得把它们广播出去。
func EndTeamFight(g *storage.Guild) {
	if g == nil {
		return
	}
	g.TeamFight = false
}

// AddTeamFightMember 对应 `TGUild.AddTeamFightMember`（Guild.pas:1284-1286）。
//
// 原版 `TList.Add` 只管加不去重；这里按名字去重，避免同一个人被登记两次
// 导致"两个成员条目分摊同一人的击杀"（原版实际不会重复调用，见 ObjBase.pas:11286-11289）。
func AddTeamFightMember(g *storage.Guild, name string) {
	if g == nil || name == "" {
		return
	}
	if TeamFightMember(g, name) != nil {
		return
	}
	g.TeamFightDead = append(g.TeamFightDead, storage.GuildTeamFightMember{Name: name})
}

// TeamFightMember 取成员在争霸赛表里的条目（没有则 nil）。
func TeamFightMember(g *storage.Guild, name string) *storage.GuildTeamFightMember {
	if g == nil {
		return nil
	}
	for i := range g.TeamFightDead {
		if g.TeamFightDead[i].Name == name {
			return &g.TeamFightDead[i]
		}
	}
	return nil
}

// TeamFightWhoDead 对应 `TGUild.TeamFightWhoDead`（Guild.pas:771-784）。
//
// 返回是否真的记了一笔（调用方据此决定要不要存盘/记日志）。
func TeamFightWhoDead(g *storage.Guild, name string) bool {
	if g == nil || !g.TeamFight {
		return false
	}
	m := TeamFightMember(g, name)
	if m == nil {
		return false
	}
	m.DieCount++
	return true
}

// TeamFightWhoWinPoint 对应 `TGUild.TeamFightWhoWinPoint`（Guild.pas:786-800）：
// 该行会总积分 +points，同时给该成员的个人得分 +points。
func TeamFightWhoWinPoint(g *storage.Guild, name string, points int) bool {
	if g == nil || !g.TeamFight {
		return false
	}
	g.ContestPoint += points
	if m := TeamFightMember(g, name); m != nil {
		m.Point += points
	}
	return true
}

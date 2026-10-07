package gamesvr

import (
	"fmt"
	"log"
	"net"
	"sort"
	"strings"

	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
)

// 行会争霸赛（FIGHT3 区）的 GM 命令与死亡结算。
//
// 原版位置：
//
//	CmdStartContest   ObjBase.pas:11226-11295  起赛（必须站在 FIGHT3 图上）
//	CmdEndContest     :11297-11360             终赛（广播最终比分）
//	CmdContestPoint   :11200-11224             查某行会积分
//	FIGHT3 死亡结算    :21018-21038             阵亡计数 / 己方记死 / 击杀方 +100 / 广播比分
//	FIGHT3 原地复活    UsrEngn.pas:524-534      `m_nFightZoneDieCount < 3` 时满血留在战场
//
// 命令名与权限来自官方 `Command.ini`（`StartContest` / `EndContest` / `ContestPoint`，权限 10）。
//
// ⚠️ 原版的"原地复活"发生在**重新进地图/登录**时（UsrEngn 登录流程里判
// `HP <= 0 and m_nFightZoneDieCount < 3`）。我们是死亡当场复活，所以把同一条判定
// 搬到 `settleFight3Death` 里 —— 语义一致（战场上最多死 3 次），时机不同。
//
// ⚠️ 记分只对**已开赛**（`boTeamFight`）的行会生效（Guild.pas:775/790 的前置），
// 所以 e2e 必须先 `@StartContest`。

const (
	// contestPointsPerKill 是单次击杀的行会得分（原版 `TeamFightWhoWinPoint(..., 100)`）。
	contestPointsPerKill = 100
	// contestMaxFightZoneDie 是 FIGHT3 区原地复活的次数上限（原版 `< 3`）。
	contestMaxFightZoneDie = 3
	// contestRange 是"参战范围"（原版 `GetMapRageHuman(..., 1000, ...)`，1000 格）。
	contestRange = 1000
)

// broadcastCryAt 把一条"喊话式"消息发给范围内所有人（原版 `UserEngine.CryCry(RM_CRY, …)`）。
//
// ⚠️ 落包是 **SM_HEAR**（不是 SM_CRY）：原版 RM_CRY → SM_* 的转换表里写的是
// `MakeDefaultMsg(SM_HEAR, …)`（ObjBase.pas:5647），只是颜色换成 CryMsg。
// 与 chat.go 的喊话同一条路径，这里单独抽出来是因为行会战要用 1000 格的范围。
func (s *Server) broadcastCryAt(p *Player, radius int, msg string) {
	s.broadcastInRange(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), radius, func(o *Player) {
		s.send(o.conn, proto.SM_HEAR, int32(p.Obj.ID), chatColorCry, 0, 1, msg)
	})
}

// playersInRange 收集范围内的玩家（原版 `GetMapRageHuman`）。
func (s *Server) playersInRange(p *Player, radius int) []*Player {
	var out []*Player
	s.broadcastInRange(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), radius, func(o *Player) {
		out = append(out, o)
	})
	return out
}

// cmdStartContest 对应 `TPlayObject.CmdStartContest`（ObjBase.pas:11226-11295）。
//
// 只有在 **FIGHT3 图**上才能用；先把范围内（1000 格）玩家的阵亡计数清零，
// 再对每个"有成员在场"的行会 `StartTeamFight` + 登记成员，最后广播三条消息。
func (s *Server) cmdStartContest(c net.Conn, p *Player, args []string) {
	if !s.contestMapOK(c, p) {
		return
	}
	near := s.playersInRange(p, contestRange)
	seen := map[string]bool{}
	var names []string
	for _, o := range near {
		o.Char.Data.FightZoneDieCount = 0 // 原版 :11260
		ref, ok := s.guildOf(o)
		if !ok || seen[ref.guild.Name] {
			continue
		}
		g := ref.guild.Name
		seen[g] = true
		names = append(names, g)
		if err := s.social.guilds.StartTeamFight(guildCtx(), g); err != nil {
			log.Printf("起赛失败 行会=%s: %v", g, err)
			continue
		}
		for _, m := range near {
			mref, ok := s.guildOf(m)
			if !ok || mref.guild.Name != g {
				continue
			}
			if err := s.social.guilds.AddTeamFightMember(guildCtx(), g, m.Char.Name); err != nil {
				log.Printf("登记参赛成员失败 行会=%s 成员=%s: %v", g, m.Char.Name, err)
			}
		}
	}
	sort.Strings(names)
	s.sysMsg(c, "行会争霸赛已经开始。")
	s.broadcastCryAt(p, contestRange, "- 行会战争已爆发。")
	s.broadcastCryAt(p, contestRange, " -参加的门派:"+strings.Join(names, " "))
	log.Printf("%s 起行会争霸赛（参战行会 %d 个: %s）", p.Char.Name, len(names), strings.Join(names, " "))
	obs.Event("contest_start", "gm", p.Char.Name, "guilds", len(names))
}

// cmdEndContest 对应 `TPlayObject.CmdEndContest`（ObjBase.pas:11297-11360）：
// 关闭记分开关，并把每个参战行会的**最终比分**广播出去（:11410-11422）。
func (s *Server) cmdEndContest(c net.Conn, p *Player, args []string) {
	if !s.contestMapOK(c, p) {
		return
	}
	near := s.playersInRange(p, contestRange)
	seen := map[string]bool{}
	var finals []string
	for _, o := range near {
		ref, ok := s.guildOf(o)
		if !ok || seen[ref.guild.Name] {
			continue
		}
		g := ref.guild.Name
		seen[g] = true
		final := ref.guild.ContestPoint
		if err := s.social.guilds.EndTeamFight(guildCtx(), g); err != nil {
			log.Printf("终赛失败 行会=%s: %v", g, err)
		}
		finals = append(finals, fmt.Sprintf(" - [%s] : %d 分。", g, final))
	}
	sort.Strings(finals)
	s.sysMsg(c, "行会争霸赛已经结束。")
	for _, line := range finals {
		s.broadcastCryAt(p, contestRange, line)
	}
	log.Printf("%s 终行会争霸赛（参战行会 %d 个）", p.Char.Name, len(finals))
	obs.Event("contest_end", "gm", p.Char.Name, "guilds", len(finals))
}

// cmdContestPoint 对应 `TPlayObject.CmdContestPoint`（ObjBase.pas:11200-11224）：
// `@ContestPoint <行会名>` ⇒ "X 的得分为: N"。
func (s *Server) cmdContestPoint(c net.Conn, p *Player, args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "?") {
		s.sysMsg(c, "查看行会战的得分数。")
		s.sysMsg(c, "命令格式: @ContestPoint 行会名称")
		return
	}
	name := args[0]
	g := s.social.guilds.Find(name)
	if g == nil {
		s.sysMsg(c, fmt.Sprintf("行会: %s 不存在！！！", name))
		return
	}
	s.sysMsg(c, fmt.Sprintf("%s 的得分为: %d", name, g.ContestPoint))
}

// contestMapOK 是前两个命令共用的前置：必须站在 FIGHT3 图上（原版 :11247-11251）。
func (s *Server) contestMapOK(c net.Conn, p *Player) bool {
	if z := s.zoneOf(p.Obj.MapRef()); z == nil || !z.Fight3Zone {
		s.sysMsg(c, "此命令不能在当前地图中使用！！！")
		return false
	}
	return true
}

// settleFight3Death 处理"在 FIGHT3 区死亡"，返回 true 表示**已在原地复活**。
//
// 原版 ObjBase.pas:21018-21038 + UsrEngn.pas:524-534：
//
//	Inc(m_nFightZoneDieCount);                        // 阵亡计数（存档字段）
//	if m_MyGuild <> nil then TeamFightWhoDead(自己);   // 己方记一次阵亡
//	if 击杀者 != nil and 双方都有行会 then
//	  TeamFightWhoWinPoint(击杀者, 100);               // 击杀方行会 +100
//	  广播 '击杀方行会:分  死者行会:分'
func (s *Server) settleFight3Death(c net.Conn, p *Player, killer *Player) bool {
	z := s.zoneOf(p.Obj.MapRef())
	if z == nil || !z.Fight3Zone {
		return false
	}
	d := p.Char.Data
	if d == nil {
		return false
	}
	d.FightZoneDieCount++

	var victimGuild string
	if ref, ok := s.guildOf(p); ok {
		victimGuild = ref.guild.Name
		if ref.guild.TeamFight {
			if _, err := s.social.guilds.TeamFightWhoDead(guildCtx(), victimGuild, p.Char.Name); err != nil {
				log.Printf("记阵亡失败 行会=%s 成员=%s: %v", victimGuild, p.Char.Name, err)
			}
		}
	}
	if killer != nil && victimGuild != "" {
		if kref, ok := s.guildOf(killer); ok && kref.guild.TeamFight {
			winner := kref.guild.Name
			if _, err := s.social.guilds.TeamFightWhoWinPoint(guildCtx(), winner,
				killer.Char.Name, contestPointsPerKill); err != nil {
				log.Printf("记击杀得分失败 行会=%s 成员=%s: %v", winner, killer.Char.Name, err)
			}
			// 广播前重取一次：积分刚写库，缓存里的才是最新值
			win, lose := s.social.guilds.Find(winner), s.social.guilds.Find(victimGuild)
			if win != nil && lose != nil {
				msg := fmt.Sprintf("- %s:%d  %s:%d", win.Name, win.ContestPoint, lose.Name, lose.ContestPoint)
				s.broadcastCryAt(p, contestRange, msg)
				log.Printf("行会战比分：%s", msg)
			} else {
				log.Printf("行会战比分读取失败 win=%v lose=%v", win != nil, lose != nil)
			}
		}
	}
	log.Printf("%s 在 FIGHT3 区(%s)阵亡（第 %d 次），行会=%s 击杀者=%s",
		p.Char.Name, z.Name, d.FightZoneDieCount, victimGuild, killerName(killer))
	obs.Event("fight3_death", "player", p.Char.Name, "map", z.Name,
		"die_count", d.FightZoneDieCount, "killer", killerName(killer))

	// 原地复活：`m_nFightZoneDieCount < 3` ⇒ 满血满蓝留在战场（原版在重进地图时做，
	// 我们当场做，见文件头说明）；到 3 次 ⇒ 计数清零并回城。
	if int(d.FightZoneDieCount) < contestMaxFightZoneDie {
		if d.Abil != nil {
			hp, mp, maxHP := p.refillHPMP()
			s.sendHealthChanged(p, p.Obj.ID, hp, mp, maxHP)
		}
		s.send(c, proto.SM_SYSMESSAGE, 0, 0, 0, 0,
			fmt.Sprintf("你还可以在战场上复活 %d 次", contestMaxFightZoneDie-int(d.FightZoneDieCount)))
		return true
	}
	d.FightZoneDieCount = 0
	return false
}

func killerName(k *Player) string {
	if k == nil {
		return ""
	}
	return k.Char.Name
}

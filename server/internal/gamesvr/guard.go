// 城堡守卫 / 弓箭手的"该打谁" —— 原版 `TGuardUnit.IsProperTarget`
// （ObjMon2.pas:828-883）与 `TGuardUnit.Struck`（:817-826）。
//
// 原版伪码（`m_Castle <> nil` 分支；我们的守卫都属于某座城堡）：
//
//	if m_LastHiter = BaseObject then Result := True;              // ① 最后打我的人
//	if BaseObject.bo2B0 then begin                                // ② 打过城堡单位（2 分钟窗口）
//	  if (GetTickCount - BaseObject.m_dw2B4Tick) < 2*60*1000 then Result := True
//	  else BaseObject.bo2B0 := False;
//	  if BaseObject.m_Castle <> nil then begin BaseObject.bo2B0 := False; Result := False; end;
//	end;
//	if TUserCastle(m_Castle).m_boUnderWar then Result := True;    // ③ 攻城期：人人可打
//	if TUserCastle(m_Castle).m_MasterGuild <> nil then begin      // ④ 守方行会与盟友：不主动打
//	  if (守方 = BaseObject.m_MyGuild) or 守方.IsAllyGuild(BaseObject.m_MyGuild) then
//	    if m_LastHiter <> BaseObject then Result := False;        //    —— 除非他先动过手
//	end;
//	if BaseObject.m_boAdminMode or BaseObject.m_boStoneMode or    // ⑤ 排除
//	   (BaseObject.m_btRaceServer in [RC_NPC{10}, RC_ANIMAL{50})) or
//	   (BaseObject = Self) or (BaseObject.m_Castle = Self.m_Castle) then Result := False;
//
// ⚠️ ④ 是**覆盖**（`Result := False`）而不是短路：所以攻城期里（③ 已把 Result 置 True），
// 守方行会的人只要没先动手，守卫**照样不打他** —— 这是原版刻意的行为，别"优化"成短路。
//
// ⚠️ `Struck` 把标记打在**攻击者**身上（`bo2B0`/`m_dw2B4Tick` 是 TBaseObject 的字段）：
// 任何人（哪怕不是攻城方、哪怕就是守方行会的人）只要打过城堡单位，2 分钟内都是守卫的
// 合法目标。我们照抄：`CastleAggroUntil` 在 entity.Object 上、`LastHiterID` 在守卫上。
package gamesvr

import (
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/storage"
)

const (
	// guardAggroWindow 是"打过城堡单位"后的仇恨窗口（ObjMon2.pas:836，2 分钟）。
	guardAggroWindow = 2 * time.Minute
	// guardRaceNPC / guardRaceAnimal 是原版 IsProperTarget ⑤ 里的种族区间
	// `RC_NPC{10} <= 种族 < RC_ANIMAL{50}`（NPC 类目标不打）。
	guardRaceNPC    = 10
	guardRaceAnimal = 50
)

// isCastleGuardKind 判断城堡单位是不是"会还击的"（守卫/弓箭手）。
//
// 城门与城墙是静态实体：原版里它们根本不在 AI 分支里（各自是 TCastleDoor / TWallStructure）。
// ⚠️ 我们的城堡单位都建成了 `entity.Monster` 且 `IsNPC=false` ⇒ 若不显式跳过，
// 它们会走普通怪的 AI **追着玩家跑**（把城门从门的位置挪走）—— 这条分支就是防它的。
func isCastleGuardKind(k storage.CastleUnitKind) bool {
	return k == storage.CastleGuard || k == storage.CastleArcher
}

// markCastleAggro 记录"attacker 打了城堡单位 victim"（对应 TGuardUnit.Struck，:817-826）。
//
// 只对城堡单位生效：打普通怪不进仇恨窗口。
func (s *Server) markCastleAggro(attacker *entity.Object, victim *entity.Monster, now time.Time) {
	if attacker == nil || victim == nil || !victim.IsCastleUnit() {
		return
	}
	victim.LastHiterID = attacker.ID
	attacker.SetCastleAggroUntil(now.Add(guardAggroWindow))
}

// guardCastleOf 取城堡单位所属的城堡。
//
// ⚠️ 不要按"地图名 == cs.Record().MapName"去匹配：地图对象的名字是加载时给的
// （`world.Generate(name, ...)`），未必等于配置里的 "3"，匹配不上就会静默拿到
// nil ⇒ 守卫退化成"打最近的玩家"（正是我们要修的那个 bug，实测踩过）。
// 本仓库对城堡单位的既有约定就是 `s.castleDefault()`（单机一座沙巴克，
// `canHitCastleUnit` 也这么取）—— 保持一致。
func (s *Server) guardCastleOf() *castle.Castle {
	cs, err := s.castleDefault()
	if err != nil {
		return nil
	}
	return cs
}

// guardProperTarget 判断守卫/弓箭手能不能打 cand。逐句对应文件头的伪码。
func (s *Server) guardProperTarget(cs *castle.Castle, guard *entity.Monster, cand *Player, now time.Time) bool {
	if cs == nil || guard == nil || cand == nil || cand.Obj == nil {
		return false
	}
	cid := cand.Obj.ID
	res := false

	// ① 最后打我的人
	if guard.LastHiterID == cid {
		res = true
	}

	// ② 打过城堡单位（2 分钟内）
	if until := cand.Obj.CastleAggroUntil(); !until.IsZero() {
		if now.Before(until) {
			res = true
		} else {
			// 过期就清掉（原版 `BaseObject.bo2B0 := False`），免得长期残留
			cand.Obj.SetCastleAggroUntil(time.Time{})
		}
	}

	// ③ 攻城期：人人可打
	if cs.UnderWar() {
		res = true
	}

	// ④ 守方行会及其盟友：**覆盖**成不打（除非他就是最后打我的那个人）
	if own := cs.OwnGuild(); own != "" {
		if name := s.playerGuildName(cand); name != "" {
			if name == own || s.guildAllied(own, name) {
				if guard.LastHiterID != cid {
					res = false
				}
			}
		}
	}

	// ⑤ ⑤ 排除：管理员模式（GM）、自己、同城堡。
	//
	// 原版这里还查 `m_boStoneMode` 与种族 10..49（NPC 类目标）—— 我们的候选集
	// 只有**玩家**：玩家没有石化、种族恒为 0、也没有"自己的城堡"
	// ⇒ 那三条对玩家恒不成立，故不写（留此注释以免以后误以为漏了）。
	if cand.permission > 0 || cid == guard.ID {
		res = false
	}
	return res
}

// playerGuildName 取玩家当前所在行会名（空 = 无行会）。
func (s *Server) playerGuildName(p *Player) string {
	ref, ok := s.guildOf(p)
	if !ok || ref == nil || ref.guild == nil {
		return ""
	}
	return ref.guild.Name
}

// guildAllied 判断两个行会是否互为盟友。
//
// 等价原版 `IsAllyGuild`（Guild.pas:377-386）：联盟是**双向**写入的，
// 但历史上出现过单边数据，所以两侧都查一遍（宽进严出，宁可不打错人）。
func (s *Server) guildAllied(a, b string) bool {
	if s.social.guilds == nil || a == "" || b == "" {
		return false
	}
	if g := s.social.guilds.Find(a); g != nil && guild.HasAlly(g, b) {
		return true
	}
	if g := s.social.guilds.Find(b); g != nil && guild.HasAlly(g, a) {
		return true
	}
	return false
}

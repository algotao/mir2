package gamesvr

// PK 惩罚的缺口中长期项：武器持久锁、扣幸运、@OnMurder/@Murdered 脚本标签。
//
// 上一轮核实 OpenMir2 与 Delphi 后确认这些是**原版无条件执行**的部分
// （不受 boKillHumanWinLevel 等 4 个开关控制，那些出厂全 False）。
// 本文件只补这部分，4 开关的 PKDie 本体仍保持关闭。
//
// 逐条对应：
//   MakeWeaponUnlock  ObjBase.pas:2393-2414
//   AddBodyLuck       ObjBase.pas:2374-2391
//   脚本标签           ObjBase.pas:21124-21128（@OnMurder / @Murdered）
//   协议 SM_BREAKWEAPON ObjBase.pas:6306-6312

import (
	"math/rand/v2"
	"time"

	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 武器附加属性的字节位（UserItem.Value 是 14 字节的 btValue[0..13]）。
//
// ⚠️ **语义别写反**（2026-10-05 更正）：原版武器道具说明把
// `AC 低位` 显示成"幸运"、`MAC 低位`显示成"诅咒"
// （`Grobal2.pas:547-548` + 客户端 `FState.pas:4065-4067`），而
// `GetItemAddValue`（ItmUnit.pas:125-126）把 `btValue[3]` 加进 AC 低位、
// `btValue[4]` 加进 MAC 低位 ⇒ **btValue[3] 是幸运位、btValue[4] 是诅咒位**。
// 所以我们这里早期把两个常量名写反了（值没变、但读代码会误导）。
//
// `MakeWeaponUnlock`（ObjBase.pas:2393-2414）的表现也就自洽了：
// "幸运 > 0 就减幸运，否则诅咒 +1"，两句都提示"武器被诅咒了"。
const (
	// btValueLuckIdx 是"武器幸运"位（→ AC 低位，幸运 +1）。
	btValueLuckIdx = 3
	// btValueCurseIdx 是"武器诅咒"位（→ MAC 低位，诅咒 +1）。
	btValueCurseIdx = 4
	// weaponCurseMax 是**诅咒**上限（原版 `btValue[4] < 10`，:2402）。
	weaponCurseMax = 10
)

// bodyLuckUnit 是幸运值单位。
//
// ⚠️ 原版 BODYLUCKUNIT = 5000（M2Share.pas:94），不是几十上百。
// m_nBodyLuckLevel = Trunc(m_dBodyLuck / BODYLUCKUNIT)，范围 [-10, +5]，
// 所以 m_dBodyLuck 的实际范围是 [-50000, +25000]。
//
// 这个量级很关键：杀人扣的幸运是 -500（nKillHumanDecLuckPoint），
// 也就是 -0.1 单位——**远不到下限**，正常杀几十次都不会触发边界。
// 若把单位误当成 10，-500 会被立刻 clamp 到 -10 单位（=下限），
// 表现为"杀一次就到顶"。
const bodyLuckUnit = 5000.0

// makeWeaponUnlock 施加武器持久锁 / 加幸运。
//
// 对应 MakeWeaponUnlock（ObjBase.pas:2393-2414）：
//
//	if 武器存在 且 btValue[3] > 0 then
//	    Dec(btValue[3]); 提示"你的武器被诅咒了"
//	else if btValue[4] < 10 then
//	    Inc(btValue[4])
//
// 语义注意：已��持久锁的武器再被打一次就**再减 1**（越打越坏）；
// 没有锁的武器第一次是**加幸运**（往好的方向走）。返回是否真的生效。
func (s *Server) makeWeaponUnlock(killer *Player) bool {
	if killer == nil || killer.Char == nil || killer.Char.Data == nil {
		return false
	}
	hum := killer.Char.Data.HumItems
	if len(hum) <= proto.SlotWeapon || hum[proto.SlotWeapon] == nil || hum[proto.SlotWeapon].Index == 0 {
		return false // 没武器
	}
	ui := hum[proto.SlotWeapon]
	// 定长补零：Value 可能来自旧存档，长度不足 14。
	ensureItemValue(ui)
	// 原版两句话都是"你的武器被诅咒了"（g_sTheWeaponIsCursed，:2400/:2406）——
	// 两个分支一个在**扣幸运**、一个在**加诅咒**，都算"倒霉"，所以文案相同。
	luck := int32(ui.Value[btValueLuckIdx])
	switch {
	case luck > 0:
		ui.Value[btValueLuckIdx] = byte(luck - 1)
		s.sysMsg(killer.conn, "你的武器被诅咒了")
		s.logWeaponCurse(killer, luck-1, int32(ui.Value[btValueCurseIdx]))
	default:
		if int32(ui.Value[btValueCurseIdx]) < weaponCurseMax {
			ui.Value[btValueCurseIdx]++
			s.sysMsg(killer.conn, "你的武器被诅咒了")
			s.logWeaponCurse(killer, int32(ui.Value[btValueLuckIdx]), int32(ui.Value[btValueCurseIdx]))
		} else {
			return false // 诅咒已满，不算生效
		}
	}
	// 原版在这里调 RecalcAbilitys（:2410）。武器的幸运/诅咒**已经**接进战斗
	//（`playerLuck` → `GetAttackPower` 的幸运分支，见 itemabil.go / rollAttack），
	// 所以这里要重发属性：否则客户端显示的命中/敏捷会停在旧值。
	s.sendUseItems(killer.conn, killer)
	return true
}

// logWeaponCurse 记录一次武器幸运/诅咒变化。
//
// 原版只发那两条相同的提示语（`g_sTheWeaponIsCursed`），没有任何日志；
// 我们留一条可用日志/事件，排查"武器怎么越打越坏"时不用猜。
func (s *Server) logWeaponCurse(p *Player, luck, curse int32) {
	logpvp("%s 武器幸运=%d 诅咒=%d", p.Char.Name, luck, curse)
	obs.Event("weapon_curse", "player", p.Char.Name, "luck", luck, "curse", curse)
}

// bodyLuckLevel 是 `m_nBodyLuckLevel`（ObjBase.pas:2387-2390）：
//
//	n := Trunc(m_dBodyLuck / BODYLUCKUNIT)
//	if n > 5 then n := 5;  if n < -10 then n := -10
//
// 它进两处公式：武器升级的三系成功率、以及攻击力的幸运分支（见 rollAttack）。
func bodyLuckLevel(d *pb.CharacterData) int {
	if d == nil {
		return 0
	}
	n := int(d.BodyLuck / bodyLuckUnit)
	if n > 5 {
		n = 5
	}
	if n < -10 {
		n = -10
	}
	return n
}

// addBodyLuck 增减幸运值。
//
// 对应 AddBodyLuck（ObjBase.pas:2374-2391）：正负都有 [-10, +5] 的上下限，
// 内部以 BODYLUCKUNIT 为单位换算成 m_nBodyLuckLevel。
// 返回是否真的变化了。
func addBodyLuck(data *pb.CharacterData, d float64) bool {
	if data == nil {
		return false
	}
	old := data.BodyLuck
	// 原版 ObjBase.pas:2378/2382：正向封顶 +5 单位、反向封底 -10 单位。
	if d > 0 && old < 5*bodyLuckUnit {
		old += d
	} else if d < 0 && old > -10*bodyLuckUnit {
		old += d
	} else {
		return false
	}
	if old > 5*bodyLuckUnit {
		old = 5 * bodyLuckUnit
	}
	if old < -10*bodyLuckUnit {
		old = -10 * bodyLuckUnit
	}
	if old == data.BodyLuck {
		return false
	}
	data.BodyLuck = old
	return true
}

// applyPKPenalties 是击杀后的"无条件惩罚"入口。
//
// 对应 ObjBase.pas:20943-20954（4 开关全关时走的分支）：
//
//	IncPkPoint(100) + 两条系统提示 + AddBodyLuck(-500)
//	+ 20% 概率 MakeWeaponUnlock
//	+ 触发 @OnMurder(被害人) / @Murdered(凶手)
//
// ⚠️ 20% 的判定条件是**死者为白名**（PKLevel < 1）且 Random(5) = 0
// （:20949-20951）——红名被杀不触发。
func (s *Server) applyPKPenalties(killer, victim *Player, now time.Time) {
	if killer == nil || killer.Char == nil || killer.Char.Data == nil {
		return
	}
	if killer.Char == nil || killer.Char.Data == nil {
		return
	}

	// 扣幸运 500（nKillHumanDecLuckPoint = 500，ObjBase.pas:21119）。
	if addBodyLuck(killer.Char.Data, -500) {
		s.sysMsg(killer.conn, "你的运气变差了")
	}

	// 20% 概率武器被锁/加幸运（仅当死者是白名）。
	if victim != nil && victim.Char != nil && victim.Char.Data != nil {
		vpk := int32(victim.Char.Data.PkPoint)
		if pvp.PKLevel(vpk) < 1 && rand.IntN(5) == 0 {
			if s.makeWeaponUnlock(killer) {
				// SM_BREAKWEAPON：Param = 受影响的玩家 ID（ObjBase.pas:6306-6312）。
				s.send(killer.conn, proto.SM_BREAKWEAPON, int32(killer.Obj.ID), 0, 0, 0, "")
			}
		}
	}

	// 脚本标签：@OnMurder 给被害人（他刚被杀），@Murdered 给凶手。
	s.gotoMurderLabels(killer, victim, now)
}

// gotoMurderLabels 触发 @OnMurder / @Murdered 脚本段。
//
// 对应 ObjBase.pas:21124-21128 的 g_FunctionNPC.GotoLable。
// 找不到对应 NPC/标签时静默跳过（原版是 FunctionNPC 为 nil 就跳过）。
func (s *Server) gotoMurderLabels(killer, victim *Player, now time.Time) {
	if victim != nil {
		s.gotoFunctionLabel(victim, "@OnMurder")
	}
	if killer != nil {
		s.gotoFunctionLabel(killer, "@Murdered")
	}
}

// gotoFunctionLabel 跳到 FunctionNPC 的指定标签。
func (s *Server) gotoFunctionLabel(p *Player, label string) {
	if p == nil || p.dialog == nil || p.dialog.scriptName == "" {
		return
	}
	sc := s.scriptByName(p.dialog.scriptName)
	if sc == nil {
		return
	}
	next := sc.Label(label)
	if next == nil {
		return
	}
	s.showLabel(p.conn, p, sc, next)
}

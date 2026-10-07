// PK 死亡的等级/经验奖惩（原版 `TPlayObject.PKDie`，ObjBase.pas:21076-21180）。
//
// 判定（四个开关怎么组合、等级差保护、红名翻倍）在 `internal/pvp.PKDieReward`，
// 这里只负责把结果应用到两个玩家身上。
//
// ⚠️ 两处与原版的**有意差异**（都因为存档模型不同，写在这里免得下次误判为漏做）：
//
//  1. 原版经验存的是**级内值**，所以说"级内经验不够就掉一级，并把掉下去那一级的
//     经验补回来再扣"；我们存**累计值** ⇒ 等价写法是"先减经验，再按累计值重算等级"
//     （`levelForExp`），结果一致：扣完不足当前等级下限就掉级。
//  2. 原版允许把等级扣到 0（`m_Abil.Level := 0`）；我们下限保到 **1**
//     （1 级是建角等级，掉到 0 会让属性/上限/客户端都失去意义）。
package gamesvr

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// applyPKDieReward 结算"玩家杀死玩家"的等级/经验奖惩。
//
// 出厂四个开关**全关**（原版如此）⇒ 默认什么都不做，与官方部署一致。
func (s *Server) applyPKDieReward(killer, victim *Player) {
	if killer == nil || victim == nil || killer.Char == nil || victim.Char == nil ||
		killer.Char.Data == nil || victim.Char.Data == nil ||
		killer.Char.Data.Abil == nil || victim.Char.Data.Abil == nil {
		return
	}
	cfg := s.pvp.cfg.cfg
	// 地图级覆盖（官方 `PKDie` 开头，ObjBase.pas:21085-21110）：地图标了
	// `PKWINLEVEL(n)` 等就把对应开关**强制打开**并换成地图给的值。
	// ⚠️ 取**死者所在图**（原版 `PKDie` 是死者自己跑，`m_PEnvir` 就是他那张图）。
	if mi := s.mapFlagOf(victim.Obj.MapRef()); mi != nil {
		if mi.PKWinLevelSet {
			cfg.PKDieWinLevel, cfg.PKDieWinLevelPoint = true, uint32(mi.PKWinLevel)
		}
		if mi.PKLostLevelSet {
			cfg.PKDieLostLevel, cfg.PKDieLostLevelPoint = true, uint32(mi.PKLostLevel)
		}
		if mi.PKWinExpSet {
			cfg.PKDieWinExp, cfg.PKDieWinExpPoint = true, uint32(mi.PKWinExp)
		}
		if mi.PKLostExpSet {
			cfg.PKDieLostExp, cfg.PKDieLostExpPoint = true, uint32(mi.PKLostExp)
		}
	}
	ka, va := killer.Char.Data.Abil, victim.Char.Data.Abil
	winLevel, lostLevel, winExp, lostExp := pvp.PKDieReward(
		cfg, uint32(ka.Level), uint32(va.Level), pvp.PKLevel(int32(victim.Char.Data.PkPoint)))
	if winLevel == 0 && lostLevel == 0 && winExp == 0 && lostExp == 0 {
		return
	}

	// 赢家：先加等级（原版直接 Inc 等级，不是加经验），再给经验
	if winLevel > 0 {
		target := uint32(ka.Level) + winLevel
		if target > entity.MaxLevel {
			target = entity.MaxLevel
		}
		s.setPlayerLevel(killer, target)
	}
	if winExp > 0 {
		s.grantExpRaw(killer, winExp)
	}

	// 输家：先扣等级，再扣经验（顺序与原版一致）
	if lostLevel > 0 {
		lv := uint32(va.Level)
		if lv > lostLevel {
			lv -= lostLevel
		} else {
			lv = 1 // 原版允许掉到 0；我们保底 1
		}
		s.setPlayerLevel(victim, lv)
	}
	if lostExp > 0 {
		s.loseExpWithLevelDrop(victim, lostExp)
	}
	logpvp("PK 死亡奖惩：%s +%d级/+%d经验，%s -%d级/-%d经验",
		killer.Char.Name, winLevel, winExp, victim.Char.Name, lostLevel, lostExp)
}

// loseExpWithLevelDrop 从玩家身上扣掉 exp 点经验；扣完不足当前等级的下限就掉级。
//
// 这是原版"级内经验不够 ⇒ Dec(Level) 并把那一级经验补回来再扣"（ObjBase.pas:21166-21190）
// 在我们**累计经验**模型下的等价写法（见文件头说明）。
func (s *Server) loseExpWithLevelDrop(p *Player, exp uint32) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil || exp == 0 {
		return
	}
	abil := p.Char.Data.Abil
	cur := abil.Exp
	if cur > uint64(exp) {
		cur -= uint64(exp)
	} else {
		cur = 0
	}
	// 减完后按累计值重算等级（只可能掉、不会涨）
	if lv := levelForExp(cur); lv < uint32(abil.Level) {
		s.setPlayerLevel(p, lv)
	}
	abil.Exp = cur
	s.send(p.conn, proto.SM_WINEXP, -int32(exp), 0, 0, 0, "")
}

// levelForExp 返回累计经验 exp 对应的等级（1..MaxLevel）。
//
// 与 `entity.CheckLevelUp` 同一套 `NeedExp` 表，只是方向相反（它只往上走）。
func levelForExp(exp uint64) uint32 {
	lv := uint32(1)
	for lv < entity.MaxLevel && exp >= entity.NeedExp(lv+1) {
		lv++
	}
	return lv
}

// 保证 pb 被引用（applyPKDieReward 的参数类型来自存档）。
var _ = (*pb.Ability)(nil)

// gmPKDie 是 `@pkdie` 的处理体（调试用，见 maps.go 的用法说明）。
func (s *Server) gmPKDie(c net.Conn, p *Player, args []string) {
	if len(args) < 2 {
		s.sysMsg(c, "用法: @pkdie winlevel|lostlevel|winexp|lostexp on|off [点数]")
		return
	}
	on := strings.EqualFold(args[1], "on") || args[1] == "1"
	cfg := &s.pvp.cfg.cfg
	point := func(def uint32) uint32 {
		if len(args) >= 3 {
			if v, err := strconv.Atoi(args[2]); err == nil && v > 0 {
				return uint32(v)
			}
		}
		return def
	}
	var name string
	switch strings.ToLower(args[0]) {
	case "winlevel":
		cfg.PKDieWinLevel, name = on, "KillHumanWinLevel"
		if len(args) >= 3 {
			cfg.PKDieWinLevelPoint = point(cfg.PKDieWinLevelPoint)
		}
	case "lostlevel":
		cfg.PKDieLostLevel, name = on, "KilledLostLevel"
		if len(args) >= 3 {
			cfg.PKDieLostLevelPoint = point(cfg.PKDieLostLevelPoint)
		}
	case "winexp":
		cfg.PKDieWinExp, name = on, "KillHumanWinExp"
		if len(args) >= 3 {
			cfg.PKDieWinExpPoint = point(cfg.PKDieWinExpPoint)
		}
	case "lostexp":
		cfg.PKDieLostExp, name = on, "KilledLostExp"
		if len(args) >= 3 {
			cfg.PKDieLostExpPoint = point(cfg.PKDieLostExpPoint)
		}
	default:
		s.sysMsg(c, "未知开关 "+args[0])
		return
	}
	s.sysMsg(c, fmt.Sprintf("%s = %v（点数 %d/%d/%d/%d）", name, on,
		cfg.PKDieWinLevelPoint, cfg.PKDieLostLevelPoint,
		cfg.PKDieWinExpPoint, cfg.PKDieLostExpPoint))
	log.Printf("%s 设置 PK 死亡开关 %s=%v", p.Char.Name, name, on)
}

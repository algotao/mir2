// 护身符 / 毒药系统（原版 CheckAmulet / UseAmulet）。
//
// 原版依据：
//
//	CheckAmulet（Magic.pas:84-130）
//	  ① 先看左手镯槽 U_ARMRINGL=5，再看护身符槽 U_BUJUK=9（Grobal2.pas:34/38）
//	  ② 物品必须是 StdMode=25
//	  ③ nType=1「护身符类」要求 Shape = 5；nType=2「毒药类」要求 Shape <= 2
//	  ④ 数量判定 `ROUND(Dura / 100) >= nCount` —— **Dura 存的是"个数×100"**
//
//	UseAmulet（Magic.pas:132-143）
//	  if Dura > nCount * 100 then Dec(Dura, nCount*100) + 发 RM_DURACHANGE
//	  else 清空该装备（SendDelItems + wIndex := 0） ← "刚好用完"走这条
//
// 哪些技能要护身符：外层 case 13..19 与 case 30（Magic.pas）
//
//	13 灵魂火符 / 14 幽灵盾 / 15 神圣战甲 / 16 困魔咒 / 17 召唤骷髅
//	18 隐身术 / 19 集体隐身术  → nCount = 1
//	30 召唤神兽                → nCount = 5（原版就要 5 个）
//
// ⚠️ 我们此前这些技能一律不要求护身符，是已知的玩法偏差。
package gamesvr

import (
	"fmt"
	"math/rand/v2"
	"net"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
)

const (
	// amuletStdMode 护身符/毒药的物品类别（Magic.pas:91 `StdMode = 25`）。
	amuletStdMode = 25
	// amuletShapeCharm 护身符类的 Shape（Magic.pas:93 `Shape = 5`）。
	amuletShapeCharm = 5
	// amuletShapePoisonMax 毒药类的 Shape 上限（Magic.pas:97 `Shape <= 2`）。
	amuletShapePoisonMax = 2
	// amuletUnit 一个护身符/毒药占用的 Dura 单位（原版按 100 为 1 个）。
	amuletUnit = 100

	amuletTypeCharm  = 1 // 护身符类
	amuletTypePoison = 2 // 毒药类
)

// amuletNeed 返回某技能需要的护身符数量与类型；第三返回值 false = 不需要。
func amuletNeed(magicID uint32) (nCount, nType int, need bool) {
	switch magicID {
	case 13, 14, 15, 16, 17, 18, 19:
		return 1, amuletTypeCharm, true // 外层 case 13..19：CheckAmulet(...,1,1,...)
	case 30:
		return 5, amuletTypeCharm, true // 召唤神兽：CheckAmulet(...,5,1,...)
	case 6:
		// 施毒术：`CheckAmulet(PlayObject,1,2,...)`（Magic.pas:361）
		// —— 类型 2 = 毒药类（Shape <= 2：1 绿毒 / 2 红毒）。
		return 1, amuletTypePoison, true
	}
	return 0, 0, false
}

// amuletCountOf 返回物品按其 Dura 折算的"个数"（原版 ROUND(Dura/100)）。
func amuletCountOf(dura uint32) int {
	return int((dura + amuletUnit/2) / amuletUnit)
}

// checkAmulet 校验护身符/毒药是否够用，返回可用槽号（-1 = 不可用）。
// 槽位顺序与原版一致：**先左手镯，再护身符槽**。
func (s *Server) checkAmulet(p *Player, nCount, nType int) int {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return -1
	}
	for _, slot := range []int{proto.SlotArmRingL, proto.SlotBujuk} {
		it := s.equipAt(p, slot)
		if it == nil || it.Index == 0 {
			continue
		}
		tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
		if tmpl == nil || tmpl.StdMode != amuletStdMode {
			continue
		}
		switch nType {
		case amuletTypeCharm:
			if tmpl.Shape != amuletShapeCharm {
				continue
			}
		case amuletTypePoison:
			if tmpl.Shape > amuletShapePoisonMax {
				continue
			}
		default:
			continue
		}
		if amuletCountOf(it.Dura) >= nCount {
			return slot
		}
	}
	return -1
}

// useAmulet 消耗护身符（UseAmulet，Magic.pas:132）。
// useAmulet 见文件头；原版无日志，这里补一条 —— 否则"毒药到底有没有被消耗"
// 从服务端完全看不出来（e2e 的 castledoor 式的日志断言也就无从下手）。
func (s *Server) useAmulet(c net.Conn, p *Player, slot, nCount int) {
	it := s.equipAt(p, slot)
	if it == nil || it.Index == 0 {
		return
	}
	if it.Dura > uint32(nCount)*amuletUnit {
		it.Dura -= uint32(nCount) * amuletUnit // 还剩：只扣持久
		s.send(c, proto.SM_DURACHANGE, int32(slot), uint16(it.Dura), uint16(it.DuraMax), 0, "")
		// 原版这里没有日志；补一条是为了"毒药/护身符到底有没有被消耗"可观测
		//（e2e 的 poison-consume 断言查它）。
		name := "消耗品"
		if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil {
			name = tmpl.Name
		}
		logpvp("%s 消耗 %s：剩 %d/%d（槽 %d）", p.Char.Name, name, it.Dura, it.DuraMax, slot)
		return
	}
	// 刚好用完：原版清空该装备（SendDelItems + wIndex := 0）
	it.Dura = 0
	it.Index = 0
	s.sendUseItems(c, p)
	logpvp("%s 的护身符用尽（槽 %d）", p.Char.Name, slot)
}

// requireAmulet 施法前的护身符校验：够则扣，不够则回 SM_MAGICFIRE_FAIL。
// 返回 false 时调用方应立即 return（不扣 MP、不涨修炼点）。
func (s *Server) requireAmulet(c net.Conn, p *Player, magicID uint32, infoName string) (uint16, bool) {
	nCount, nType, need := amuletNeed(magicID)
	if !need {
		return 0, true
	}
	slot := s.checkAmulet(p, nCount, nType)
	if slot < 0 {
		s.sysMsg(c, fmt.Sprintf("没有%s", amuletLabel(nType)))
		s.send(c, proto.SM_MAGICFIRE_FAIL, 0, 0, 0, 0, "")
		logpvp("%s 放 %s 失败：%s不足（需 %d 个）", p.Char.Name, infoName, amuletLabel(nType), nCount)
		return 0, false
	}
	// ⚠️ **先取模板 Shape，再消耗**：原版就是这个顺序
	//（`StdItem := GetStdItem(...)` 在 `UseAmulet(...)` 之前，Magic.pas:362-365）。
	// 反过来的话，"刚好用掉最后一个"时槽已清空 ⇒ 读不到 Shape ⇒ 施毒术静默不生效
	//（`UseAmulet` 在 `Dura <= nCount*100` 时会直接清空该槽）。
	var shape uint16
	if it := s.equipAt(p, slot); it != nil {
		if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil {
			shape = uint16(tmpl.Shape)
		}
	}
	s.useAmulet(c, p, slot, nCount)
	return shape, true
}

func amuletLabel(nType int) string {
	if nType == amuletTypePoison {
		return "毒药"
	}
	return "护身符"
}

// initialDura 返回物品实例的初始 Dura。
//
// ⚠️ **护身符/毒药（StdMode=25）的 Dura 不是耐久，是"个数×100"**
// （CheckAmulet 按 ROUND(Dura/100) >= nCount 判数量、UseAmulet 每次扣 100）。
// 官方 StdItems 里这三件物品的 DuraMax 恰好是 100/50/200 个
// （护身符 10000、灰色药粉 5000、护身符(大) 20000），据此初始值取 DuraMax。
// 其余物品仍走"随机 20%~99% 耐久"算法。
func initialDura(tmpl *data.StdItem) uint32 {
	if tmpl == nil {
		return 0
	}
	if tmpl.StdMode == amuletStdMode {
		return uint32(tmpl.DuraMax)
	}
	if tmpl.DuraMax == 0 {
		return 0
	}
	return uint32(tmpl.DuraMax) / 100 * uint32(20+rand.IntN(80))
}

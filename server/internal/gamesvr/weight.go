// 负重系统（原版 `m_Abil.MaxWeight/MaxWearWeight/MaxHandWeight` 与
// `m_WAbil.Weight/WearWeight/HandWeight`）。
//
// 三块依据（都在 ObjBase.pas，逐句对照）：
//
//	① RecalcLevel（:1885-1930）按**职业**给三个上限基数：
//	     jTaos  (道士): 50+Round(lv²/4)   、15+Round(lv²/50) 、12+Round(lv²/42)
//	     jWizard(法师): 50+Round(lv²/5)   、15+Round(lv²/100)、12+Round(lv²/90)
//	     jWarr  (战士): 50+Round(lv²/3)   、15+Round(lv²/20) 、12+Round(lv²/13)
//	   ⚠️ 两处容易照抄错：
//	     · `Round((nLevel / 4) * nLevel)` 里的 `/` 是 Delphi 的**实数除**，
//	       不是整除 ⇒ 等价 `lv²/4`（拿 level=7 验：实数除 1.75×7=12.25 → 12；
//	       整除写法 1×7=7 就错了）；
//	     · Delphi 的 `Round` 是**银行家舍入**（.5 向偶数取）⇒ 用 math.RoundToEven。
//	② RecalcAbilitys（:3405-3407）加**装备**加成：
//	     Inc(MaxWeight, m_AddAbil.Weight); Inc(MaxWearWeight, ...); Inc(MaxHandWeight, ...)
//	   而这三个值只由 **StdMode 62** 提供（ItmUnit.pas:626-631）：
//	     HandWeight += AC2;  Weight += MAC;  WearWeight += MAC2
//	③ 超负载戒指（Shape 119 → `m_boMuscleRing`，:3276 置位、:3459-3464 生效）：
//	     三个上限**各自翻倍**（`Inc(x, x)`），且必须在装备加成**之后**。
//
// 当前值（:2950-2960 与 :3088-3096 的装备循环）：
//
//	武器/右手槽 ⇒ 计入 `HandWeight`；衣服与其余所有槽 ⇒ 计入 `WearWeight`。
//	⚠️ 循环里那句 `Inc(m_WAbil.Weight, StdItem.Weight)` 随后被
//	`m_WAbil.Weight := RecalcBagWeight()`（:3360）**覆盖** ⇒ 总负重最终**只算背包**
//	（拾取提示写的也是"背包负重超重"）。这一点容易看漏：循环里确实累加了已穿戴物品，
//	但紧接着就被覆盖。
//
// 判定与文案：
//
//	拾取 `IsAddWeightAvailable`（:2085-2091）：`背包负重 + 新物品重量 <= MaxWeight`，
//	     否则回 `物品太重，无法携带！超重：N`（这条在源码里**硬编码**，:1773）。
//	佩带（ClientTakeOnItems，:22985-22998）：武器/右手槽比**手负重**（单件重量 >
//	     MaxHandWeight ⇒ `腕力不够!`），其余槽比**佩带负重**（新物品重量 + 该槽原有
//	     物品重量 > MaxWearWeight ⇒ `负重力不够!`）。
//	     两条文案来自官方 String.ini（`HandWeightNot` / `WearWeightNot`，第 60/61 行）。
package gamesvr

import (
	"fmt"
	"net"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// 佩带失败的文案（官方 data/envir/String.ini:60-61）。
const (
	msgHandWeightNot = "腕力不够!"
	msgWearWeightNot = "负重力不够!"
)

// baseMaxWeights 返回某职业某等级的 (总负重, 佩带负重, 手负重) 上限基数。
//
// 直接对应 RecalcLevel 里 `case m_btJob of` 的三支（ObjBase.pas:1885-1930）。
// ⚠️ 公式里的 Round 是 Delphi 的**银行家舍入**（.5 向偶数），复用本仓库
// spellpower.go 里已有的 `delphi.Round`（语义一致，别再造一个）。
func baseMaxWeights(level, job uint32) (maxWeight, maxWearWeight, maxHandWeight int) {
	lv := float64(level)
	sq := lv * lv
	switch job {
	case entity.JobWarr: // 战士：lv²/3、lv²/20、lv²/13
		return 50 + delphi.Round(sq/3), 15 + delphi.Round(sq/20), 12 + delphi.Round(sq/13)
	case entity.JobWizard: // 法师：lv²/5、lv²/100、lv²/90
		return 50 + delphi.Round(sq/5), 15 + delphi.Round(sq/100), 12 + delphi.Round(sq/90)
	default: // 道士（含未知职业兜底）：lv²/4、lv²/50、lv²/42
		return 50 + delphi.Round(sq/4), 15 + delphi.Round(sq/50), 12 + delphi.Round(sq/42)
	}
}

// itemWeight 返回物品模板的重量（index 是 1-based 的物品号）。
func (s *Server) itemWeight(index uint32) int {
	if s.data.tables == nil || index == 0 {
		return 0
	}
	if it := s.data.tables.Items.Get(int(index) - 1); it != nil {
		return int(it.Weight)
	}
	return 0
}

// playerWeightInfo 汇总玩家当前的四种负重与三个上限。
//
// 返回值顺序：(背包负重, 佩带负重, 手负重, 总上限, 佩带上限, 手上限)。
func (s *Server) playerWeightInfo(p *Player) (bag, wear, hand, maxWeight, maxWearWeight, maxHandWeight int) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	d := p.Char.Data

	// 背包负重：RecalcBagWeight（:18533）逐件累加 StdItem.Weight
	for _, it := range d.BagItems {
		if it == nil || it.Index == 0 {
			continue
		}
		bag += s.itemWeight(it.Index)
	}

	// 已穿戴：武器槽计手负重，其余（含衣服）计佩带负重（:2950-2960）
	for slot, it := range d.HumItems {
		if it == nil || it.Index == 0 {
			continue
		}
		w := s.itemWeight(it.Index)
		if slot == proto.SlotWeapon {
			hand += w
		} else {
			wear += w
		}
	}

	// 上限基数（等级 × 职业）+ 装备加成（StdMode 62）
	maxWeight, maxWearWeight, maxHandWeight = baseMaxWeights(p.level(), uint32(d.Job))
	a := s.playerAddAbil(p)
	maxWeight += a.weight
	maxWearWeight += a.wearWeight
	maxHandWeight += a.handWeight

	// 超负载戒指（Shape 119）：三个上限各自翻倍（:3459-3464）
	if p.equipSpecials.muscleRing {
		maxWeight += maxWeight
		maxWearWeight += maxWearWeight
		maxHandWeight += maxHandWeight
	}
	return
}

// applyWeights 把负重写回存档属性。
//
// 为什么要写回（而不是下发时现算）：客户端属性包 `SM_ABILITY` 是
// `abilityFromPB(p.Char.Data.Abil)`（main.go），直接读这几个字段；而原版也是
// RecalcAbilitys 一次性重算写进 `m_WAbil`。写回后所有下游（含客户端负重条）都对了。
func (s *Server) applyWeights(p *Player) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	bag, wear, hand, maxW, maxWear, maxHand := s.playerWeightInfo(p)
	abil := p.Char.Data.Abil
	abil.Weight = uint32(bag)
	abil.MaxWeight = uint32(maxW)
	abil.WearWeight = uint32(wear)
	abil.MaxWearWeight = uint32(maxWear)
	abil.HandWeight = uint32(hand)
	abil.MaxHandWeight = uint32(maxHand)
}

// sendAbility 重算负重后下发属性包（SM_ABILITY）。
//
// ⚠️ 为什么把"重算"收口在这里：背包/装备的任何变动都会改 `Weight`，而它只能通过
// 这条包告诉客户端（客户端负重条直接读它）。背包变动路径很多 —— `@give`、拾取、
// 掉落、交易、仓库、吃东西……逐个挂钩子必漏；而**发送点**是有限的。
// 原版同样是在每次背包变动后重算（`m_WAbil.Weight := RecalcBagWeight()`，:21523 等）。
//
// ⚠️ c 为 nil 时静默返回（`send` 已有 nil 保护）。
func (s *Server) sendAbility(c net.Conn, p *Player) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	s.applyWeights(p)
	abRaw := abilityFromPB(p.Char.Data.Abil).Bytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
}

// weightAllowed 判断"再往背包里放 nWeight 会不会超重"（原版 IsAddWeightAvailable，:2085）。
//
// 返回 (是否允许, 超出多少)；超出量用于拼原版提示。
func (s *Server) weightAllowed(p *Player, nWeight int) (bool, int) {
	bag, _, _, maxWeight, _, _ := s.playerWeightInfo(p)
	if bag+nWeight <= maxWeight {
		return true, 0
	}
	return false, bag + nWeight - maxWeight
}

// pickBlockedByWeight 是拾取前的统一检查：超重就不许捡，并回原版提示。
//
// 原版提示（ObjBase.pas:1773，**硬编码**在源码里）：
//
//	SysMsg('物品太重，无法携带！超重：' + IntToStr(m_WAbil.Weight + nStdItemWeight - m_WAbil.MaxWeight))
func (s *Server) pickBlockedByWeight(p *Player, index uint32) bool {
	ok, over := s.weightAllowed(p, s.itemWeight(index))
	if ok {
		return false
	}
	s.sysMsg(p.conn, fmt.Sprintf("物品太重，无法携带！超重：%d", over))
	return true
}

// takeOnBlockedByWeight 是佩带前的负重检查，返回拒绝文案（空串 = 允许）。
//
// 原版（ClientTakeOnItems，:22985-22998）：
//
//	if (nWhere = 1) or (nWhere = 2) then   // U_WEAPON / U_RIGHTHAND
//	  if StdItem.Weight > m_WAbil.MaxHandWeight then SysMsg(sHandWeightNot)
//	else
//	  if (StdItem.Weight + GetUserItemWeitht(nWhere)) > m_WAbil.MaxWearWeight then
//	    SysMsg(sWearWeightNot)
//
// `GetUserItemWeitht(nWhere)` 就是"该槽位原有物品的重量"。
func (s *Server) takeOnBlockedByWeight(p *Player, tmpl *data.StdItem, slot int) string {
	if tmpl == nil || p == nil || p.Char == nil || p.Char.Data == nil {
		return ""
	}
	_, _, _, _, maxWear, maxHand := s.playerWeightInfo(p)
	w := int(tmpl.Weight)
	if slot == proto.SlotWeapon {
		if w > maxHand {
			return msgHandWeightNot
		}
		return ""
	}
	// 该槽原有物品的重量也要算进去（换装时是"替换"，不是"叠加"）
	old := 0
	if slot >= 0 && slot < len(p.Char.Data.HumItems) {
		if it := p.Char.Data.HumItems[slot]; it != nil && it.Index != 0 {
			old = s.itemWeight(it.Index)
		}
	}
	if w+old > maxWear {
		return msgWearWeightNot
	}
	return ""
}

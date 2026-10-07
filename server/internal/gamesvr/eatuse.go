package gamesvr

import (
	"log"
	"net"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// `StdMode = 3` 那一族消耗品（原版 `EatUseItems`，ObjBase.pas:23513）的配置与文案。
//
// 派发链：`TPlayObject.EatItems`（:23324）→ `StdMode = 3` 时
// `Shape = 12` ⇒ 临时属性药水；否则 ⇒ `EatUseItems(Shape)`（:23434）。
//
// 各 Shape 的现状：
//
//	1  回城卷           ✅（回本图出生点附近的随机点：`MapRandomMove`）
//	2  随机传送卷       ✅（本图随机点）
//	3  回城卷（红名）   ✅（PKLevel ≥ 2 走红名回城点 ⚠️ 我们没接该配置 ⇒ 暂同 Shape 1）
//	4  祝福油           ✅
//	5  行会/城堡回城卷   ✅（**只有城主行会**能传，见函数内注释）
//	9  修复油           ✅
//	10 特修油           ✅
//	11 彩票             原版注释掉（`//取消彩票功能`）⇒ 不做
//	12 临时属性药水     由 `EatItems` 直接处理（不在本文件）
//
// 数值来自官方 `!setup.txt:1018-1023`（代码里的默认值有一处不同：
// `nWeaponMakeUnLuckRate` 代码默认 20，官方文件是 **50** ⇒ 以文件为准）。
const (
	// nWeaponMakeUnLuckRate 吃祝福油"倒大霉"（武器被诅咒）的概率分母。
	nWeaponMakeUnLuckRate = 50
	// 幸运分段：<1 必成；<3 抽一次；<7 抽第二次。
	weaponMakeLuckPoint1 = 1
	weaponMakeLuckPoint2 = 3
	weaponMakeLuckPoint3 = 7
	// 第二/三次抽签的分母增量（武器 DC 跨度 /5 后加进来）。
	weaponMakeLuckPoint2Rate = 6
	weaponMakeLuckPoint3Rate = 40
	// repairItemDecDura 修复油会让**耐久上限**永久磨损的分母（官方 = 30）。
	repairItemDecDura = 30
)

// 三条文案取自官方 `data/envir/String.ini:229/230/203`。
const (
	sWeaptonMakeLuck     = "武器被加幸运了..."
	sWeaptonNotMakeLuck  = "无效!"
	sWeaponRepairSuccess = "武器修复成功..."
	// sCanotUseDrugOnThisMap 是 NODRUG 地图上使用物品的提示。
	// 官方 `String.ini` 的 `CanotUseDrugOnThisMap=此地图不允许使用任何药品!`
	//（代码里的默认值用的是句号，以 .ini 为准）。
	sCanotUseDrugOnThisMap = "此地图不允许使用任何药品!"
	// sCastleScrollInvalid 是行会/城堡回城卷"你不是城主"时的提示。
	// 原版是硬编码字面量 `SysMsg('无效', c_Red, t_Hint)`（ObjBase.pas:23562），
	// 与祝福油的 `WeaptonNotMakeLuck='无效!'`（String.ini）**不是同一条**（少了叹号）。
	sCastleScrollInvalid = "无效"
)

// eatUseItem 处理 `StdMode = 3` 的消耗品（原版 `EatUseItems`，ObjBase.pas:23513）。
//
// 返回值是**原版语义的"这次使用有效"**：返回 false 时调用方**不能消耗物品**
// （原版 `EatItems` 的 `Result` 一路传回 `ClientEatItem`，为 False 就不扣）。
func (s *Server) eatUseItem(c net.Conn, p *Player, tmpl *data.StdItem) bool {
	if tmpl == nil {
		return false
	}
	switch tmpl.Shape {
	case 1:
		return s.scrollHome(c, p)
	case 2:
		return s.scrollRandom(c, p)
	case 3:
		// 红名回城卷：原版 3 分支与 1 分支走的是**同一句**
		// `BaseObjectMove(m_sHomeMap, '', '')`，区别只在 `m_sHomeMap` ——
		// `GetStartPoint`（ObjBase.pas:9908-9913）在 `PKLevel >= 2` 时把它覆盖成
		// 红名回城点（官方 `!setup.txt:128-130`：RedHomeMap=3 / RedHomeX=845 / RedHomeY=674）。
		return s.scrollHomeMap(c, p, true)
	case 4:
		return s.oilOfLuck(c, p)
	case 5:
		return s.scrollCastle(c, p)
	case 9:
		return s.repairWeapon(c, p, false)
	case 10:
		return s.repairWeapon(c, p, true)
	}
	return false
}

// mapRandomPoint 复刻原版 `TBaseObject.MapRandomMove`（ObjBase.pas:9810-9830）：
//
//	if 图高 < 150 then (if 图高 < 30 then 边距 := 2 else 边距 := 20) else 边距 := 50;
//	nX := Random(宽 - 边距 - 1) + 边距;  nY := Random(高 - 边距 - 1) + 边距
//
// ⚠️ 原版**不判可走性**（落点落在墙上由 `SpaceMove` 兜底）——我们也一样，
// 兜底交给 `switchMap` 的"就近可走点"。
func mapRandomPoint(m *world.Map) (int, int) {
	h := m.Height()
	edge := 50
	if h < 150 {
		edge = 20
		if h < 30 {
			edge = 2
		}
	}
	wx, wy := m.Width()-edge-1, h-edge-1
	if wx < 1 {
		wx = 1
	}
	if wy < 1 {
		wy = 1
	}
	return delphi.Random(wx) + edge, delphi.Random(wy) + edge
}

// scrollHome 是回城卷（`Shape = 1`）：`BaseObjectMove(m_sHomeMap, ”, ”)`
// ⇒ 回**本图**（`m_sHomeMap`，即存档里的 HomeMap）的随机点。
//
// ⚠️ 注意与 `MoveToHome`（用存档的 HomeX/HomeY）不同：这里坐标传的是空串，
// 走的是 `MapRandomMove` ⇒ **随机点**，不是那个固定点。
func (s *Server) scrollHome(c net.Conn, p *Player) bool {
	return s.scrollHomeMap(c, p, false)
}

// redHomeMap 是红名回城点所在的地图（官方 `!setup.txt:128`
// `RedHomeMap=3`；同段的 `RedHomeX=845` / `RedHomeY=674` 是那个"点"，
// 原版用来填 `m_nHomeX/Y`，而卷本身是在该图上取**随机点**（`MapRandomMove`））。
const redHomeMap = "3"

// scrollHomeMap 是回城卷的公共实现：`redName = true` 表示这是 Shape 3
// （红名回城卷）—— 只有 `PKLevel >= 2` 才真的走红名点，否则与 Shape 1 同。
func (s *Server) scrollHomeMap(c net.Conn, p *Player, redName bool) bool {
	if p == nil || p.Obj == nil || p.Char == nil || p.Char.Data == nil {
		return false
	}
	home := p.Char.Data.HomeMap
	if redName && pvp.PKLevel(int32(p.Char.Data.PkPoint)) >= 2 {
		home = redHomeMap
	}
	if home == "" {
		home = p.Obj.MapRef().Name // 存档里没有就退回当前图（原版 m_sHomeMap 恒有值）
	}
	mp, err := s.world.maps.Get(home)
	if err != nil {
		log.Printf("%s 的回城卷失败：找不到图 %s（%v）", p.Char.Name, home, err)
		return false
	}
	x, y := mapRandomPoint(mp)
	if err := s.switchMap(c, p, home, x, y); err != nil {
		log.Printf("%s 的回城卷失败：%v", p.Char.Name, err)
		return false
	}
	log.Printf("%s 用回城卷回到 %s(%d,%d)", p.Char.Name, home, x, y)
	return true
}

// scrollRandom 是随机传送卷（`Shape = 2`）：`BaseObjectMove(m_sMapName, ”, ”)`
// ⇒ 在**当前图**随机落点。
//
// ⚠️ 原版外面还有一道 `if not m_PEnvir.Flag.boNORANDOMMOVE then`（该图禁止随机移动）：
// 官方形状 2 的分支就是它（ObjBase.pas:23539）⇒ 该图禁用时**不生效**
// （`Result` 保持 False ⇒ 卷不消耗）。
func (s *Server) scrollRandom(c net.Conn, p *Player) bool {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return false
	}
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoRandomMove {
		s.sysMsg(c, "此地图不允许随机移动")
		return false
	}
	mp := p.Obj.MapRef()
	x, y := mapRandomPoint(mp)
	if err := s.switchMap(c, p, mp.Name, x, y); err != nil {
		log.Printf("%s 的随机传送卷失败：%v", p.Char.Name, err)
		return false
	}
	log.Printf("%s 用随机传送卷落到 %s(%d,%d)", p.Char.Name, mp.Name, x, y)
	return true
}

// scrollCastle 是行会/城堡回城卷（`Shape = 5`），逐句对照 ObjBase.pas:23548-23575：
//
//	if 没有行会 then Exit(False)                       // 不消耗、也不提示
//	if 在自由 PK 区 then SysMsg('此处无法使用'); Exit(False)
//	Castle := 我的行会是不是城主行会
//	if 是 then BaseObjectMove(Castle.m_sHomeMap, Castle.GetHomeX, Castle.GetHomeY)
//	else      SysMsg('无效')                            // 注意：这里**消耗**（Result := True）
//
// ⚠️ 三个反直觉之处：
//  1. 只有**城主行会**（`IsMasterGuild`）能用，普通成员用会看到"无效"**并且消耗**；
//  2. 没有行会时**不消耗**、也不提示；
//  3. `GetHomeX/GetHomeY` 自带 ±4 抖动（Castle.pas:921-929，我们已实现）。
//
// `m_boInFreePKArea`（自由 PK 区）我们没建模 ⇒ 按 false 处理（与出厂配置一致）。
func (s *Server) scrollCastle(c net.Conn, p *Player) bool {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return false
	}
	gname := s.guildNameOf(p)
	if gname == "" {
		return false // 没行会：不消耗（原版这条路径连提示都没有）
	}
	// ⚠️ 城堡未启用时 mgr 为 nil（run.go 只在 -castle-enable 时才装）——
	// 没有这道保护，玩家在城堡关闭的服上用一次行会回城卷就是空指针崩溃。
	if s.castle.mgr == nil {
		s.sysMsg(c, sCastleScrollInvalid)
		return true // 与"不是城主"同路：消耗
	}
	cs := s.castle.mgr.IsCastleMember(gname) // 原版 g_CastleManager.IsCastleMember(Self)
	if cs == nil || !cs.IsMasterGuild(gname) {
		s.sysMsg(c, sCastleScrollInvalid)
		return true // ⚠️ 原版这条路径照样消耗
	}
	x, y := cs.HomePos(delphi.Random)
	if err := s.switchMap(c, p, cs.HomeMap(), x, y); err != nil {
		log.Printf("%s 的城堡回城卷失败：%v", p.Char.Name, err)
		return false
	}
	log.Printf("%s 用城堡回城卷回到 %s(%d,%d)", p.Char.Name, cs.HomeMap(), x, y)
	return true
}

// oilOfLuck 是祝福油（`Shape = 4`），逐句对照 `TPlayObject.WeaptonMakeLuck`
// （ObjBase.pas:23621-23670）。
//
//	if 没装备武器 then Exit(False)                    // ⇒ **油不消耗**
//	nRand := abs(武器.DC2 - 武器.DC) div 5            // DC 跨度 /5
//	if Random(nWeaponMakeUnLuckRate) = 1 then
//	  MakeWeaponUnlock()                              // 倒大霉：武器被诅咒
//	else begin
//	  if btValue[4] > 0 then Dec(btValue[4])          // 先抵掉一次**诅咒**
//	  else if btValue[3] < 1 then Inc(btValue[3])     // 必成
//	  else if (btValue[3] < 3) and Random(nRand + 6) = 1 then Inc(btValue[3])
//	  else if (btValue[3] < 7) and Random(nRand * 40) = 1 then Inc(btValue[3])
//	  重算属性 + 发属性/子属性包；一次都没成 ⇒ 提示"无效!"
//	end
//	Result := True                                    // ⇒ 油消耗掉
//
// ⚠️ 三个容易抄错的地方：
//
//  1. **只有"没武器"那一支返回 False（不消耗油）**，连倒大霉那支也是 True；
//  2. 幸运位是 `btValue[3]`、诅咒位是 `btValue[4]`（见 pkpenalty.go 的说明）；
//     第一支是"**用祝福油把已有诅咒抵掉一次**"，原版同样播"武器被加幸运了..."，
//     且它**不再走**后面的抽签（`else if` 链）；
//  3. `Random(nRand * 40)` 在 `nRand = 0` 时（DC 跨度 < 5 的武器）恒为 0
//     ⇒ 那一段**永远不中**，这类武器的幸运上限实际是 3（原版行为，别"修"）。
func (s *Server) oilOfLuck(c net.Conn, p *Player) bool {
	ui := s.equipAt(p, proto.SlotWeapon)
	if ui == nil {
		log.Printf("%s 没装备武器，祝福油不消耗（原版 WeaptonMakeLuck 首行 Exit(False)）", p.Char.Name)
		return false // 没武器 ⇒ 不消耗油
	}
	nRand := 0
	if tmpl := s.data.tables.Items.Get(int(ui.Index) - 1); tmpl != nil {
		d := int(tmpl.DC.Max) - int(tmpl.DC.Min)
		if d < 0 {
			d = -d
		}
		nRand = d / 5
	}
	if delphi.Random(nWeaponMakeUnLuckRate) == 1 {
		s.makeWeaponUnlock(p) // 1/50：武器被诅咒（原版同一条路径）
		return true
	}

	ensureItemValue(ui)
	luck := ui.Value[btValueLuckIdx]
	ok := false
	switch {
	case ui.Value[btValueCurseIdx] > 0:
		ui.Value[btValueCurseIdx]--
		ok = true
	case luck < weaponMakeLuckPoint1:
		ui.Value[btValueLuckIdx] = luck + 1
		ok = true
	case luck < weaponMakeLuckPoint2 && delphi.Random(nRand+weaponMakeLuckPoint2Rate) == 1:
		ui.Value[btValueLuckIdx] = luck + 1
		ok = true
	case luck < weaponMakeLuckPoint3 && delphi.Random(nRand*weaponMakeLuckPoint3Rate) == 1:
		ui.Value[btValueLuckIdx] = luck + 1
		ok = true
	}

	if ok {
		s.sysMsg(c, sWeaptonMakeLuck)
	} else {
		s.sysMsg(c, sWeaptonNotMakeLuck)
	}
	// 原版在这里 `RecalcAbilitys()` + 发 `RM_ABILITY`/`RM_SUBABILITY`
	//（幸运进 `GetAttackPower` 的幸运/诅咒分支，见 itemabil.go 与 rollAttack）。
	s.sendAbility(c, p)
	s.sendSubAbility(c, p)
	// ⚠️ 我们额外发一次物品列表：原版全靠客户端自己刷新道具说明，
	// 而"武器幸运"是显示在**道具说明**里的（`btValue[3]`）⇒ 不发的话客户端
	// 上的幸运值会停在旧值。`makeWeaponUnlock` 也是这么做的。
	s.sendUseItems(c, p)
	log.Printf("%s 使用祝福油：幸运 %d 诅咒 %d 生效=%v",
		p.Char.Name, ui.Value[btValueLuckIdx], ui.Value[btValueCurseIdx], ok)
	return true
}

// repairWeapon 是修复油（`Shape = 9`）与特修油（`Shape = 10`），
// 逐句对照 `RepairWeapon` / `SuperRepairWeapon`（ObjBase.pas:23673-23700）。
//
//	普修：没武器或**耐久已满** ⇒ Exit(False)（不消耗）
//	      Dec(DuraMax, (DuraMax-Dura) div 30)   // ⚠️ 上限**永久**磨损
//	      nDura := _MIN(5000, DuraMax-Dura); Inc(Dura, nDura)
//	      发 RM_DURACHANGE + 提示"武器修复成功..."
//	特修：没武器 ⇒ Exit(False)；Dura := DuraMax（**不动**上限）
//
// ⚠️ 普修的"永久磨损上限"是原版设定（修一次少一点上限），不是 bug。
func (s *Server) repairWeapon(c net.Conn, p *Player, super bool) bool {
	ui := s.equipAt(p, proto.SlotWeapon)
	if ui == nil {
		return false
	}
	if super {
		ui.Dura = ui.DuraMax
	} else {
		if ui.DuraMax <= ui.Dura {
			return false // 耐久已满 ⇒ 不消耗
		}
		ui.DuraMax -= (ui.DuraMax - ui.Dura) / repairItemDecDura
		nDura := ui.DuraMax - ui.Dura
		if nDura > 5000 {
			nDura = 5000
		}
		if nDura <= 0 {
			return false
		}
		ui.Dura += nDura
	}
	s.send(c, proto.SM_DURACHANGE, int32(proto.SlotWeapon), uint16(ui.Dura), uint16(ui.DuraMax), 0, "")
	s.sysMsg(c, sWeaponRepairSuccess)
	log.Printf("%s 使用%s油修复武器：耐久 %d/%d", p.Char.Name, map[bool]string{true: "特修", false: ""}[super],
		ui.Dura, ui.DuraMax)
	return true
}

// ensureItemValue 保证物品的附加属性数组是 14 字节（旧存档可能短）。
//
// 从 pkpenalty.go 的 makeWeaponUnlock 里抽出来，祝福油也要用。
func ensureItemValue(ui *pb.UserItem) {
	if ui == nil || len(ui.Value) >= entity.ItemValueLen {
		return
	}
	pad := make([]byte, entity.ItemValueLen)
	copy(pad, ui.Value)
	ui.Value = pad
}

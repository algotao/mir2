package gamesvr

import (
	"log"
	"net"
	"sort"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/delphi"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 武器升级（原版 `TMerchant.UpgradeWapon` / `GetBackupgWeapon`，ObjNpc.pas:1034-1384）。
//
// 这条链路是**三段式**的，时间线拉得很开，别当成一次原子操作：
//
//	① `@upgradenow`（ObjNpc.pas:1034-1196）
//	   扣金币 → **把武器从身上摘走** → 把背包里的黑铁矿石与首饰一起收走，
//	   折算成四项点数（btDc/btSc/btMc/btDura）→ 记进该 NPC 的"修炼中"列表。
//	② `@getbackupgnow`（:1197-1384）
//	   过了 `UPgradeWeaponGetBackTime` 才能取；取回时按 btDura 调耐久、
//	   按三点抽一次属性，结果**先写进武器的 `btValue[10]`**，武器放回背包。
//	③ 下一刀**砍到东西**时（原版 `AttackDir` 开头，ObjBase.pas:18819-18824）
//	   才真正结算：`btValue[10]` 换算成 `btValue[0/1/2]`（武器 DC/MC/SC 上限 +1..+4），
//	   或者**武器破碎**（`btValue[10] = 1`）。结算后 `btValue[10] := 0`。
//
// ⚠️ 第 ③ 步为什么放在攻击里：原版就是这么写的（`CheckWeaponUpgrade` 定义在
// `AttackDir` 内部），所以"取回来但没试刀"的武器在客户端只显示一个待鉴定标记
//（`ItmUnit.pas:131` 把 `btValue[10] <> 0` 映射到 `StdItem.Reserved` 的 bit0）。

const (
	// 数值全部取自官方 `!setup.txt:242-256`。⚠️ 代码里的默认值有一处不同：
	// `nUpgradeWeaponPrice` 代码默认 10000，官方文件是 **20000** ⇒ 以文件为准。
	upgradeWeaponPrice       = 20000
	upgradeWeaponMaxPoint    = 20
	upgradeWeaponDCRate      = 100
	upgradeWeaponDCTwoRate   = 30
	upgradeWeaponDCThreeRate = 200
	upgradeWeaponMCRate      = 100
	upgradeWeaponMCTwoRate   = 30
	upgradeWeaponMCThreeRate = 200
	upgradeWeaponSCRate      = 100
	upgradeWeaponSCTwoRate   = 30
	upgradeWeaponSCThreeRate = 200

	// upgradeGetBackTime 是"修炼多久才能取回"（官方 `UPgradeWeaponGetBackTime=1000`）。
	// ⚠️ 它与 `GetTickCount` 相减比较 ⇒ 单位是**毫秒**，1000 就是 1 秒。
	// 脚本对白写的是"请一个小时后再回来"（原版老文案），与这个值对不上 ——
	// 以**配置值**为准（那才是运营可调的），文案照抄原版。
	upgradeGetBackTime = 1000 * time.Millisecond

	// blackStoneName 是黑铁矿石的物品名（官方 `!setup.txt:3472` 的 `[Names] BlackStone`）。
	blackStoneName = "黑铁矿石"

	// btValueUpgrade 是"待鉴定/待结算"标记位（原版 `btValue[10]`）。
	btValueUpgrade = 10
)

// sTheWeaponBroke 是武器破碎的提示（官方 `String.ini` 的 `TheWeaponBroke=武器破碎!`；
// 代码里的默认值写作 `武器升级破碎.`，以 .ini 为准）。
const sTheWeaponBroke = "武器破碎!"

// weaponUpgrade 是"修炼中"的一条记录（原版 `TUpgradeInfo`）。
type weaponUpgrade struct {
	userName string
	// item 是被收走的那把武器（整件实例，含 btValue）。
	item *pb.UserItem
	// dc/sc/mc 是首饰折算出的三系点数，dura 是黑铁折算出的耐久点数（都是 Byte）。
	dc, sc, mc byte
	dura       byte

	dtTime time.Time
	// getBackTick 是收下武器的时刻，用于 `@getbackupgnow` 的时间判定。
	getBackTick time.Time
}

// upgradeSlot 取某个 NPC 的"修炼中"列表（没有就建）。
//
// ⚠️ 原版是 `TMerchant.m_UpgradeWeaponList`，存在 NPC 对象上并落盘
// （`SaveUpgradingList`/`LoadUpgradeWeaponRecord`）。我们按 **NPC ActorId** 存在
// `s.npc.upgrades`，由 `Server.mu` 保护（与 `npc.spawned` 同一把锁）。
// **不落盘**：进程重启会丢掉修炼中的武器 —— 已在 docs/progress.md 记为偏差
// （我们还没有"按 NPC 存文件"的存储层）。
func (s *Server) upgradeSlot(npcID uint32) []*weaponUpgrade {
	s.mu.RLock()
	list := s.npc.upgrades[npcID]
	s.mu.RUnlock()
	return list
}

// upgradeSet 覆盖某个 NPC 的修炼列表（调用方需自行保证串行）。
func (s *Server) upgradeSet(npcID uint32, list []*weaponUpgrade) {
	s.mu.Lock()
	if s.npc.upgrades == nil {
		s.npc.upgrades = make(map[uint32][]*weaponUpgrade)
	}
	if len(list) == 0 {
		delete(s.npc.upgrades, npcID)
	} else {
		s.npc.upgrades[npcID] = list
	}
	s.mu.Unlock()
}

// findUpgrade 在列表里按角色名找（原版按 `sUserName` 线性查）。
func findUpgrade(list []*weaponUpgrade, name string) int {
	for i, u := range list {
		if u.userName == name {
			return i
		}
	}
	return -1
}

// calcUpgradePoints 复刻 `sub_4A0218`（ObjNpc.pas:1035-1164）：
// 扫一遍**背包**，把黑铁矿石与首饰收走（`takeBagItem`），并算出四项点数。
//
//	黑铁矿石：只取耐久，Dura 是千分制 ⇒ Round(Dura/1000)，**最多取前 5 块**
//	         （按耐久从大到小排序后求和）
//	首饰    ：按 StdMode 取 DC/SC/MC 的**有效值**（含这件首饰自己的 btValue），
//	         只记"最大"与"次大"两个数（原版把这两个变量叫 nDcMin/nDcMax，名字是反的）
//
// 折算公式（原版逐字）：
//
//	btDura := Round(min(5,n) + min(5,n) * ((nDura / n) / 5.0))
//	btDc   := nDcMin div 5 + nDcMax div 3        // btSc / btMc 同理
//
// ⚠️ `n` 是黑铁块数，公式里拿它当除数 ⇒ **一块都没有会除零**。原版的调用点外面
// 有 `CheckItems(sBlackStone) <> nil` 兜着，所以不会走到；我们这里也加了防御。
func (s *Server) calcUpgradePoints(p *Player) (dc, sc, mc, dura byte) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	d := p.Char.Data
	// ⚠️ 整个"遍历 + 摘出黑铁/首饰"必须在同一把背包锁里：循环里边走边删，
	// 分开加锁会与并发写者（成交/退回）错位。见 baglock.go。
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	var duras []int
	nDcMin, nDcMax := 0, 0
	nScMin, nScMax := 0, 0
	nMcMin, nMcMax := 0, 0

	// ⚠️ 从后往前遍历（原版 `for i := Count-1 downto 0`），删元素不影响未访问的下标。
	for i := len(d.BagItems) - 1; i >= 0; i-- {
		it := d.BagItems[i]
		if it == nil || it.Index == 0 {
			continue
		}
		tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
		if tmpl == nil {
			continue
		}
		if tmpl.Name == blackStoneName {
			duras = append(duras, delphi.Round(float64(it.Dura)/1000.0))
			takeBagItemLocked(d, i)
			continue
		}
		if data.ItemTypeOf(tmpl.StdMode) != data.ItemTypeAccessory {
			continue
		}
		// 首饰：有效值 = 模板值 + 这件实例的 btValue（`GetItemAddValue`）。
		eff := itemEffective(tmpl, it)
		var nDc, nSc, nMc int
		switch tmpl.StdMode {
		case 19, 20, 21, 22, 23:
			nDc = int(eff.dc) + int(eff.dc2)
			nSc = int(eff.sc) + int(eff.sc2)
			nMc = int(eff.mc) + int(eff.mc2)
		case 24, 26:
			nDc = int(eff.dc) + int(eff.dc2) + 1
			nSc = int(eff.sc) + int(eff.sc2) + 1
			nMc = int(eff.mc) + int(eff.mc2) + 1
		default:
			// `ItemType` 是首饰、StdMode 却不在这 7 个里 ⇒ **照样收走但不计点**
			//（原版那个 `case` 没有 else 分支，行为一致，见 :1077-1095）。
		}
		if nDcMin < nDc {
			nDcMax, nDcMin = nDcMin, nDc
		} else if nDcMax < nDc {
			nDcMax = nDc
		}
		if nScMin < nSc {
			nScMax, nScMin = nScMin, nSc
		} else if nScMax < nSc {
			nScMax = nSc
		}
		if nMcMin < nMc {
			nMcMax, nMcMin = nMcMin, nMc
		} else if nMcMax < nMc {
			nMcMax = nMc
		}
		takeBagItemLocked(d, i)
	}

	// 黑铁耐久从大到小排序，取前 5 块求和（原版是冒泡排 DuraList）。
	sort.Sort(sort.Reverse(sort.IntSlice(duras)))
	nDura, n := 0, 0
	for _, v := range duras {
		nDura += v
		n++
		if n >= 5 {
			break
		}
	}
	if n > 0 {
		dura = byte(delphi.Round(float64(n) + float64(n)*(float64(nDura)/float64(n)/5.0)))
	}
	dc = byte(nDcMin/5 + nDcMax/3)
	sc = byte(nScMin/5 + nScMax/3)
	mc = byte(nMcMin/5 + nMcMax/3)
	return
}

// ---------- ① `@upgradenow` ----------

// weaponUpgradeStart 复刻 `TMerchant.UpgradeWapon`（ObjNpc.pas:1034-1196）。
//
//	已有修炼中的记录        ⇒ ~@upgradenow_ing
//	武器 + 钱 + 黑铁齐全    ⇒ 扣钱、摘武器、收料、记账 ⇒ ~@upgradenow_ok
//	否则                    ⇒ ~@upgradenow_fail
func (s *Server) weaponUpgradeStart(c net.Conn, p *Player, npcID uint32) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	d := p.Char.Data
	if findUpgrade(s.upgradeSlot(npcID), p.Char.Name) >= 0 {
		s.gotoScriptLabel(c, p, "~@upgradenow_ing")
		return
	}
	// 三个前置条件（原版一行 `if`）：身上有武器、金币够、背包里有黑铁矿石。
	weapon := s.equipAt(p, proto.SlotWeapon)
	if weapon == nil || p.gold() < upgradeWeaponPrice || !s.hasBlackStone(d) {
		s.gotoScriptLabel(c, p, "~@upgradenow_fail")
		return
	}
	// ⚠️ 上面那句只是预检；原子扣款失败（并发收支）同样按"条件不满足"处理
	if !p.spendGold(upgradeWeaponPrice) {
		s.gotoScriptLabel(c, p, "~@upgradenow_fail")
		return
	}
	// 城堡抽税（原版 :1186 的 IncRateGold，正是 `castleTax` 注释里记的那四个调用点之一）。
	s.castleTax(p, int64(upgradeWeaponPrice))
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")

	up := &weaponUpgrade{
		userName:    p.Char.Name,
		item:        weapon,
		dtTime:      time.Now(),
		getBackTick: time.Now(),
	}
	// 摘武器（原版 `User.m_UseItems[U_WEAPON].wIndex := 0` + RecalcAbilitys +
	// FeatureChanged + RM_ABILITY）。
	d.HumItems[proto.SlotWeapon] = &pb.UserItem{}
	// 收料 + 算点数（会给背包里那几件物品直接做掉）。
	up.dc, up.sc, up.mc, up.dura = s.calcUpgradePoints(p)

	list := append(s.upgradeSlot(npcID), up)
	s.upgradeSet(npcID, list)

	s.refreshEquip(c, p) // 重算属性 + 刷装备/背包 + 广播外观
	log.Printf("%s 在 NPC(%d) 修炼武器 %s：点数 DC=%d SC=%d MC=%d 耐久=%d",
		p.Char.Name, npcID, s.itemName(up.item), up.dc, up.sc, up.mc, up.dura)
	s.gotoScriptLabel(c, p, "~@upgradenow_ok")
}

// hasBlackStone 判断背包里有没有黑铁矿石（原版 `CheckItems(sBlackStone)`，
// ObjBase.pas:24843-24858 —— **只扫背包**，不含身上的装备）。
func (s *Server) hasBlackStone(d *pb.CharacterData) bool {
	for _, it := range d.BagItems {
		if it == nil || it.Index == 0 {
			continue
		}
		if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil && tmpl.Name == blackStoneName {
			return true
		}
	}
	return false
}

// itemName 取物品名（拼日志用）。
func (s *Server) itemName(it *pb.UserItem) string {
	if it == nil || it.Index == 0 {
		return "(空)"
	}
	if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil {
		return tmpl.Name
	}
	return "(无模板)"
}

// ---------- ② `@getbackupgnow` ----------

// weaponUpgradeTake 复刻 `TMerchant.GetBackupgWeapon`（ObjNpc.pas:1197-1384）。
func (s *Server) weaponUpgradeTake(c net.Conn, p *Player, npcID uint32) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	// 背包满 ⇒ ~@getbackupgnow_bagfull（原版先查这个，连"有没有记录"都不看）。
	if !s.bagHasRoom(p.Char.Data) {
		s.gotoScriptLabel(c, p, "~@getbackupgnow_bagfull")
		return
	}
	list := s.upgradeSlot(npcID)
	idx := findUpgrade(list, p.Char.Name)
	if idx < 0 {
		s.gotoScriptLabel(c, p, "~@getbackupgnow_fail")
		return
	}
	up := list[idx]
	// 时间没到（且不是 GM）⇒ ~@getbackupgnow_ing，记录**留在原地**。
	if time.Since(up.getBackTick) <= upgradeGetBackTime && p.permission < 4 {
		s.gotoScriptLabel(c, p, "~@getbackupgnow_ing")
		return
	}
	rest := append(append([]*weaponUpgrade{}, list[:idx]...), list[idx+1:]...)
	s.upgradeSet(npcID, rest)

	w := up.item
	s.rollUpgradedWeapon(p, w, up)
	if s.addToBag(p, w) < 0 {
		// 理论上前面查过背包，这里兜一层：放不回去就把它留在记录里，下次再来。
		s.upgradeSet(npcID, append(s.upgradeSlot(npcID), up))
		s.gotoScriptLabel(c, p, "~@getbackupgnow_bagfull")
		return
	}
	s.refreshEquip(c, p)
	log.Printf("%s 取回修炼的武器 %s：点数 DC=%d SC=%d MC=%d 耐久=%d ⇒ btValue[10]=%d",
		p.Char.Name, s.itemName(w), up.dc, up.sc, up.mc, up.dura, btValueAt(w, btValueUpgrade))
	s.gotoScriptLabel(c, p, "~@getbackupgnow_ok")
}

// bagHasRoom 报告背包还有没有空位（原版 `User.IsEnoughBag`）。
func (s *Server) bagHasRoom(d *pb.CharacterData) bool {
	for _, it := range d.BagItems {
		if it == nil || it.Index == 0 {
			return true
		}
	}
	// 全满时还允许"长一格"（我们背包是可增长的切片，见 equip.go 的脱装备逻辑）。
	return len(d.BagItems) < int(entity.MaxBagSize)
}

// rollUpgradedWeapon 是取回时的**结算（写标记位）**，复刻 ObjNpc.pas:1270-1345。
//
// 先按 `btDura` 调耐久，再按三点抽一次属性，结果写进 `btValue[10]`：
//
//	1     ⇒ 失败（下一刀**武器破碎**）
//	10-12 ⇒ DC +1/+2/+3      20-22 ⇒ MC +1/+2/+3      30-32 ⇒ SC +1/+2/+3
//
// ⚠️ 三处原版怪癖，我按原样复刻（不要"顺手修正"，否则与原版不一致）：
//
//  1. 三个属性分支是**三个独立的 `if`**（不是 else-if）。当三系点数全相等时
//     （典型的：**一块首饰都没放** ⇒ 全 0），三个条件**同时成立** ⇒ DC、MC、SC
//     依次执行，**最后 SC 的那次结果留下**。`n1C := Random(3)` 在这种情形下等于没用。
//  2. SC 分支里 `n90 := _MIN(11, btMc)` 用的是 **btMc**（原版 :1332），不是 btSc
//     —— 看着像原版复制粘贴漏改，但对外表现就是"SC 走 MC 的档位"。
//  3. 耐久那段的 `8..255: Inc(DuraMax, 4000)` 会**吞掉 `Random(btDura-18) = 0`**
//     与 8 以上所有值；16..17 不落在任何 case 里 ⇒ 耐久**不变**。
func (s *Server) rollUpgradedWeapon(p *Player, w *pb.UserItem, up *weaponUpgrade) {
	if w == nil {
		return
	}
	var luckData *pb.CharacterData
	if p != nil && p.Char != nil {
		luckData = p.Char.Data
	}
	// ---- 耐久 ----
	switch {
	case up.dura <= 8:
		if w.DuraMax > 3000 {
			w.DuraMax -= 3000
		} else {
			w.DuraMax /= 2
		}
		if w.Dura > w.DuraMax {
			w.Dura = w.DuraMax
		}
	case up.dura <= 15:
		if delphi.Random(int(up.dura)) < 6 {
			if w.DuraMax > 1000 {
				w.DuraMax -= 1000
			}
			if w.Dura > w.DuraMax {
				w.Dura = w.DuraMax
			}
		}
	case up.dura >= 18:
		switch n := delphi.Random(int(up.dura) - 18); {
		case n >= 1 && n <= 4:
			w.DuraMax += 1000
		case n >= 5 && n <= 7:
			w.DuraMax += 2000
		default: // 0 与 8 及以上
			w.DuraMax += 4000
		}
	}

	// ---- 属性 ----
	n1C := -1
	if up.dc == up.mc && up.mc == up.sc {
		n1C = delphi.Random(3)
	}
	// 三系共用的 n10（原版把这段算三遍，`n90 shl 3 - n90 + 10` 就是 `7*n90 + 10`，
	// 注释掉的那行 `n90 * 8 - n90 + 10` 是同一件事）。
	roll := func(n90 int) int {
		n10 := 7*n90 + 10 + int(btValueAt(w, 3)) - int(btValueAt(w, 4)) + bodyLuckLevel(luckData)
		if n10 > 85 {
			n10 = 85
		}
		return n10
	}
	set := func(v byte) { setBtValue(w, btValueUpgrade, v) }

	if (up.dc >= up.mc && up.dc >= up.sc) || n1C == 0 {
		n10 := roll(min(11, int(up.dc)))
		if delphi.Random(upgradeWeaponDCRate) < n10 {
			set(10)
			if n10 > 63 && delphi.Random(upgradeWeaponDCTwoRate) == 0 {
				set(11)
			}
			if n10 > 79 && delphi.Random(upgradeWeaponDCThreeRate) == 0 {
				set(12)
			}
		} else {
			set(1)
		}
	}
	if (up.mc >= up.dc && up.mc >= up.sc) || n1C == 1 {
		n10 := roll(min(11, int(up.mc)))
		if delphi.Random(upgradeWeaponMCRate) < n10 {
			set(20)
			if n10 > 63 && delphi.Random(upgradeWeaponMCTwoRate) == 0 {
				set(21)
			}
			if n10 > 79 && delphi.Random(upgradeWeaponMCThreeRate) == 0 {
				set(22)
			}
		} else {
			set(1)
		}
	}
	if (up.sc >= up.mc && up.sc >= up.dc) || n1C == 2 {
		n10 := roll(min(11, int(up.mc))) // ⚠️ 原版这里用的就是 btMc，见上
		if delphi.Random(upgradeWeaponSCRate) < n10 {
			set(30)
			if n10 > 63 && delphi.Random(upgradeWeaponSCTwoRate) == 0 {
				set(31)
			}
			if n10 > 79 && delphi.Random(upgradeWeaponSCThreeRate) == 0 {
				set(32)
			}
		} else {
			set(1)
		}
	}
}

// ---------- ③ 试刀：攻击时结算 ----------

// settleWeaponUpgrade 是"试刀"：武器上的 `btValue[10]` 真正生效的地方。
//
// 复刻 `CheckWeaponUpgrade` + `CheckWeaponUpgradeStatus`（ObjBase.pas:18704-18725），
// 由 `AttackDir` 开头调用（:18819-18824）：**有目标且有武器**时，先结算再算伤害。
//
//	btValue[10] == 0                     ⇒ 没事
//	sum(btValue[0..2]) >= MaxPoint       ⇒ 武器**破碎**（原版直接 wIndex := 0）
//	btValue[10] == 1                     ⇒ 破碎
//	10..13 ⇒ btValue[0] += btValue[10]-9 （DC +1..+4）
//	20..23 ⇒ btValue[1] += btValue[10]-19
//	30..33 ⇒ btValue[2] += btValue[10]-29
//
// 结算后 `btValue[10] := 0`（不论成功失败）。
func (s *Server) settleWeaponUpgrade(p *Player) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Obj == nil {
		return
	}
	w := s.equipAt(p, proto.SlotWeapon)
	if w == nil {
		return
	}
	mark := int(btValueAt(w, btValueUpgrade))
	if mark == 0 {
		return
	}
	name := s.itemName(w)
	sum := int(btValueAt(w, 0)) + int(btValueAt(w, 1)) + int(btValueAt(w, 2))
	broken := false
	if sum < upgradeWeaponMaxPoint {
		switch {
		case mark == 1:
			broken = true
		case mark >= 10 && mark <= 13:
			setBtValue(w, 0, byte(int(btValueAt(w, 0))+mark-9))
		case mark >= 20 && mark <= 23:
			setBtValue(w, 1, byte(int(btValueAt(w, 1))+mark-19))
		case mark >= 30 && mark <= 33:
			setBtValue(w, 2, byte(int(btValueAt(w, 2))+mark-29))
		}
	} else {
		broken = true // 三系点数之和到上限 ⇒ 再升就碎
	}
	setBtValue(w, btValueUpgrade, 0)

	if !broken {
		log.Printf("%s 试刀成功：%s 点数 +%d（btValue[0..2]=%d/%d/%d）",
			p.Char.Name, name, mark-9, btValueAt(w, 0), btValueAt(w, 1), btValueAt(w, 2))
		// 属性变了要重算（武器 DC 上限进了战斗力）。
		s.refreshEquip(p.conn, p)
		return
	}
	// 破碎：摘掉武器 + 提示 + SM_BREAKWEAPON（原版还发 SendDelItems）。
	p.Char.Data.HumItems[proto.SlotWeapon] = &pb.UserItem{}
	s.refreshEquip(p.conn, p)
	s.sysMsg(p.conn, sTheWeaponBroke)
	s.send(p.conn, proto.SM_BREAKWEAPON, int32(p.Obj.ID), 0, 0, 0, "")
	log.Printf("%s 的 %s 在试刀时破碎（btValue[10]=%d，三系和=%d/%d）",
		p.Char.Name, name, mark, sum, upgradeWeaponMaxPoint)
}

// facingHasTarget 报告玩家正前方那一格有没有可打的对象。
//
// 对应原版的 `AttackTarget := GetPoseCreate()`（ObjBase.pas:18819）——它取的是
// "面前那一格的对象"，玩家与怪物都算（不区分），也正是 `CheckWeaponUpgrade`
// 的触发条件之一。
func (s *Server) facingHasTarget(p *Player, dir uint8) bool {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return false
	}
	tx := p.Obj.PosX() + int(entity.DirDelta[dir][0])
	ty := p.Obj.PosY() + int(entity.DirDelta[dir][1])
	pl, mon := s.playerAt(p.Obj.MapRef(), tx, ty), s.monsterAt(p.Obj.MapRef(), tx, ty)
	// 只在"武器上还压着待结算标记"时才多打一行：这正是试刀窗口，
	// 真出问题时（例如怪不在正面那格）日志里能直接看出来，平时一条不刷。
	if w := s.equipAt(p, proto.SlotWeapon); w != nil && btValueAt(w, btValueUpgrade) != 0 {
		log.Printf("试刀判定：%s 在(%d,%d) 方向%d 正面格(%d,%d) 玩家=%v 怪物=%v（标记=%d）",
			p.Char.Name, p.Obj.PosX(), p.Obj.PosY(), dir, tx, ty, pl != nil, mon != nil, btValueAt(w, btValueUpgrade))
	}
	return pl != nil || mon != nil
}

// ---------- btValue / 标签 / 幸运值 小工具 ----------

// btValueAt 读物品的 btValue[i]（越界返回 0）。
func btValueAt(it *pb.UserItem, i int) byte {
	if it == nil || i < 0 || i >= len(it.Value) {
		return 0
	}
	return it.Value[i]
}

// setBtValue 写物品的 btValue[i]（不足 14 字节时补齐，与 ensureItemValue 同规矩）。
func setBtValue(it *pb.UserItem, i int, v byte) {
	if it == nil || i < 0 || i >= entity.ItemValueLen {
		return
	}
	ensureItemValue(it)
	it.Value[i] = v
}

// gotoScriptLabel 跳到当前 NPC 脚本里的某个段（原版 `GotoLable(User, sXXX, False)`）。
//
// ⚠️ 标签名要带 `~@` 前缀：脚本里写的是 `[~@upgradenow_ok]`，而解析器只去掉方括号
// 与**开头**的 `@`（script.go:98-110）⇒ 键就是 `~@upgradenow_ok`。
func (s *Server) gotoScriptLabel(c net.Conn, p *Player, name string) {
	if p == nil || p.dialog == nil {
		return
	}
	sc := s.scriptByName(p.dialog.scriptName)
	if sc == nil {
		return
	}
	if l := sc.Label(name); l != nil {
		s.showLabel(c, p, sc, l)
		return
	}
	log.Printf("%s 的脚本 %s 里没有 %q 段（旧脚本可能没有这一节）", p.Char.Name, sc.Name, name)
}

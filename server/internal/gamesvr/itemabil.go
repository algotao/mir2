package gamesvr

import (
	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// 装备的"附加属性"（原版 TAddAbility）——人物副属性的**装备来源**。
//
// 过去我们只把装备的 DC/AC/MAC 直接用掉（`magic.AttackPower`/`playerAC`/`playerMAC`），
// 而原版是先把每件装备折算成一个 `TAddAbility`，再统一加到人物身上
//（ObjBase.pas:3395-3416 的 `Inc(m_btHitPoint, m_AddAbil.wHitPoint)` 那一段）。
// 少了这一层，"装备带准确/幸运"在 1.76 里是**有的**（客户端道具说明里也显示），
// 我们却一律按 0 处理 —— 命中偏低的根因之一。
//
// 两段照搬（都在 `ItmUnit.pas`）：
//
//	GetItemAddValue(UserItem, StdItem)  :117-169  模板值 + 实例 btValue[] 升级值 → 该件"有效值"
//	ApplyItemParameters(var AddAbility) :556-699  有效值 → 按**物品类型/StdMode** 折算成加成
//
// 字段语义（`Grobal2.pas:547-548` 的原文 + 客户端道具说明 `FState.pas:4057-4067`）：
//
//	武器：AC 高位 = 准确、低位 = 幸运；MAC 高位 = 攻击速度、低位 = 诅咒
//	衣服：AC/MAC 是防御；Source 低字节 = 幸运、高字节 = 诅咒
//	首饰：按 StdMode 分（19 项链=抗魔/幸运/诅咒；20/24 手镯戒指=准确/敏捷；…）
//
// ⚠️ 与 `pkpenalty.go` 的 `btValue` 索引是同一套，别写反：
// **btValue[3] 是"幸运"位**（+AC 低位）、**btValue[4] 是"诅咒"位**（+MAC 低位）。
// 原版 `MakeWeaponUnlock`（ObjBase.pas:2393-2414）用"幸运位 > 0 就 -1，否则诅咒位 +1"
// 来表现"武器被诅咒"，两处语义自洽；我们早期把常量名写反了（已改）。
//
// 未建模的部分（都有明确的消费方缺失，不是漏抄）：
//
//   - `nHitSpeed`（攻击速度）：原版只把它 `RM_CHARSTATUSCHANGED` 发给客户端
//     做挥砍动画，服务端战斗节奏不用它 ⇒ 我们不算。
//   - `Weight/WearWeight/HandWeight`（负重）：我们还没有负重系统。
//   - 套装加成（如"红魔 3 件 +2 准确"，ObjBase.pas:3345）：没有套装系统。
//   - `wHP/wMP`（63 类首饰直接加血/魔上限）：原版接在
//     `m_WAbil.MaxHP := _MIN(High(Word), m_Abil.MaxHP + m_AddAbil.wHP)`（:3409）；
//     这类 StdMode 也不在我们可穿戴槽位表里（`equipSlotFor`），暂无消费方。
//   - `wAntiMagic`（抗魔）：原版只在 `MagPassThroughMagic`（穿透类魔法，
//     `Random(10) >= 抗魔` 才命中，ObjBase.pas:2549）里用；我们没有那类技能
//     ⇒ 目前**只下发显示**。物理/魔法减伤走的是 AC / MAC（:22414 / GetMagStruckDamage）。
//   - `NameColor`/`boFixColor` 等外观类：与属性无关，另有路径。

// addUserItemNewValue 对应 g_Config.boAddUserItemNewValue。
//
// 出厂默认 True（M2Share.pas:2142），官方 `!setup.txt:1016` 也是
// `AddUserItemNewValue=1` ⇒ 按实测值取 true。它决定 StdMode 52/53/54 走
// "新属性"分支（准确/敏捷/恢复/抗魔）还是当普通 AC/MAC 用。
const addUserItemNewValue = true

// itemVals 是一件装备的**有效值**（对应原版 TItem 的 AC/AC2/MAC/MAC2/DC/... 字段）：
// 模板值 + 该实例的 `btValue[]` 升级值。名字里的 2 是原版的"高位"。
type itemVals struct {
	item   *data.StdItem
	source int8

	ac, ac2   uint16
	mac, mac2 uint16
	dc, dc2   uint16
	mc, mc2   uint16
	sc, sc2   uint16
}

// itemEffective 复刻 `TItem.GetItemAddValue`（ItmUnit.pas:117-169）。
//
// ⚠️ 三种物品类型的 `btValue[]` 索引**完全不同**（武器的 [3]/[5] 是 AC 低/高、
// [4]/[6] 是 MAC 低/高；衣服首饰则是 [0..4] 依次给 AC2/MAC2/DC2/MC2/SC2）。
// 照抄索引，别"类比"。
func itemEffective(it *data.StdItem, ui *pb.UserItem) itemVals {
	v := itemVals{
		item:   it,
		source: it.Source,
		ac:     it.AC.Min, ac2: it.AC.Max,
		mac: it.MAC.Min, mac2: it.MAC.Max,
		dc: it.DC.Min, dc2: it.DC.Max,
		mc: it.MC.Min, mc2: it.MC.Max,
		sc: it.SC.Min, sc2: it.SC.Max,
	}
	if ui == nil {
		return v
	}
	bv := func(i int) uint16 {
		if i < len(ui.Value) {
			return uint16(ui.Value[i])
		}
		return 0
	}
	switch data.ItemTypeOf(it.StdMode) {
	case data.ItemTypeWeapon:
		v.dc2 += bv(0)
		v.mc2 += bv(1)
		v.sc2 += bv(2)
		v.ac += bv(3)
		v.ac2 += bv(5)
		v.mac += bv(4)
		v.mac2 += bv(6)
		// Source（神圣值）：只有 1..10 才覆盖，其余保持模板值（:127-130）。
		if s := bv(7); s >= 1 && s <= 10 {
			v.source = int8(s)
		}
	case data.ItemTypeDress:
		v.ac2 += bv(0)
		v.mac2 += bv(1)
		v.dc2 += bv(2)
		v.mc2 += bv(3)
		v.sc2 += bv(4)
	case data.ItemTypeAccessory:
		v.ac2 += bv(0)
		v.mac2 += bv(1)
		v.dc2 += bv(2)
		v.mc2 += bv(3)
		v.sc2 += bv(4)
	}
	return v
}

// addAbil 是原版 `TAddAbility` 里**我们有用到**的那部分。
//
// 原版结构见 `Common/Grobal2.pas:1434-1445`（wDC/wMC/wSC/wAC/wMAC/wHP/wMP/
// wHitPoint/wSpeedPoint/wAntiPoison/wPoisonRecover/.../btLuck/btUnLuck/nHitSpeed）。
// 没列进来的（nHitSpeed/Weight/WearWeight/HandWeight）见文件头"未建模的部分"。
type addAbil struct {
	hit   int // wHitPoint   准确 → m_btHitPoint
	speed int // wSpeedPoint 敏捷 → m_btSpeedPoint

	// hitSpeed 是攻击速度（`nHitSpeed` → `m_nHitSpeed`，ObjBase.pas:3403）。
	//
	// 来源（ItmUnit.pas:562-568 / :630-660）：
	//	武器：MAC 高位 > 10 ⇒ +（MAC2-10）；否则 ⇒ -MAC2
	//	首饰 StdMode 21/54/64（项链/腰带）、23（戒指）：+AC 低位 -MAC 低位
	// 消费点有两处：① `RM_CHARSTATUSCHANGED` 下发给客户端做挥砍动画；
	// ② **服务端攻击间隔**（ClientHitXY:8774，见 netgate.go 的 AttackIntervalFor）。
	hitSpeed int

	luck   int // btLuck   → m_nLuck 加
	unluck int // btUnLuck → m_nLuck 减

	antiPoison    int // wAntiPoison
	poisonRecover int // wPoisonRecover
	healthRecover int // wHealthRecover
	spellRecover  int // wSpellRecover
	antiMagic     int // wAntiMagic

	hp, mp int // wHP/wMP（63 类首饰直接加血上限/魔上限）

	// 三个"上限加成"（原版 `m_AddAbil.Weight/WearWeight/HandWeight`）。
	//
	// 唯一来源是 **StdMode 62**（ItmUnit.pas:626-631）：
	//
	//	Inc(AddAbility.HandWeight, AC2);
	//	Inc(AddAbility.Weight,     MAC);
	//	Inc(AddAbility.WearWeight, MAC2);
	//
	// 消费点见 weight.go（上限 = 等级/职业基数 + 这三个值，超负载戒指再翻倍）。
	weight, wearWeight, handWeight int
	acLo, acHi                     int // wAC（打包的低/高）
	macLo, macHi                   int // wMAC
	dcLo, dcHi                     int // wDC
	mcLo, mcHi                     int // wMC
	scLo, scHi                     int // wSC
}

// apply 复刻 `TItem.ApplyItemParameters`（ItmUnit.pas:556-699）。
func (v itemVals) apply() addAbil {
	var a addAbil
	switch data.ItemTypeOf(v.item.StdMode) {
	case data.ItemTypeWeapon:
		a.hit += int(v.ac2) // 准确 = AC 高位
		// 攻击速度（ItmUnit.pas:562-568）：MAC 高位 > 10 才算**加速**，
		// 加 (MAC2-10)；否则是减速，减 MAC2。中档（11..20）里 11 只加 1。
		if int(v.mac2) > 10 {
			a.hitSpeed += int(v.mac2) - 10
		} else {
			a.hitSpeed -= int(v.mac2)
		}
		a.luck += int(v.ac)    // 幸运 = AC 低位
		a.unluck += int(v.mac) // 诅咒 = MAC 低位
	case data.ItemTypeDress:
		// 衣服：AC/MAC 是防御（低/高各自累加）
		a.acLo += int(v.ac)
		a.acHi += int(v.ac2)
		a.macLo += int(v.mac)
		a.macHi += int(v.mac2)
		// Source 低字节 = 幸运、高字节 = 诅咒
		a.luck += loByteOf(int(v.source))
		a.unluck += hiByteOf(int(v.source))
	case data.ItemTypeAccessory:
		switch v.item.StdMode {
		case 19: // 项链
			a.antiMagic += int(v.ac2)
			a.unluck += int(v.mac)
			a.luck += int(v.mac2)
		case 53: // 新项链
			if addUserItemNewValue {
				a.antiMagic += int(v.ac2)
				a.unluck += int(v.mac)
				a.luck += int(v.mac2)
			} else {
				a.acLo += int(v.ac)
				a.acHi += int(v.ac2)
				a.macLo += int(v.mac)
				a.macHi += int(v.mac2)
			}
		case 63: // Charm：HP/MP 上限 + 幸运/诅咒
			a.hp += int(v.ac)
			a.mp += int(v.ac2)
			a.unluck += int(v.mac)
			a.luck += int(v.mac2)
		case 20, 24: // 项链/手镯：准确 + 敏捷
			a.hit += int(v.ac2)
			a.speed += int(v.mac2)
		case 52: // 靴子（新属性）
			if addUserItemNewValue {
				a.hit += int(v.ac2)
				a.speed += int(v.mac2)
			} else {
				a.acLo += int(v.ac)
				a.acHi += int(v.ac2)
				a.macLo += int(v.mac)
				a.macHi += int(v.mac2)
			}
		case 62: // 负重类首饰：加三个负重上限（ItmUnit.pas:626-631）
			a.handWeight += int(v.ac2)
			a.weight += int(v.mac)
			a.wearWeight += int(v.mac2)
		case 21, 54, 64: // 项链/腰带：体力·魔法恢复 + **攻速**
			a.healthRecover += int(v.ac2)
			a.spellRecover += int(v.mac2)
			// ⚠️ StdMode 54 在原版还看 `boAddUserItemNewValue`：开了走这套，
			// 关掉则退化成"当防御用"。官方 !setup.txt 没有这一项 ⇒ 用出厂默认（开），
			// 与上面 53/52 的处理保持一致。
			a.hitSpeed += int(v.ac) - int(v.mac)
		case 23: // 戒指：抗毒 + 解毒恢复 + **攻速**
			a.antiPoison += int(v.ac2)
			a.poisonRecover += int(v.mac2)
			a.hitSpeed += int(v.ac) - int(v.mac)
		}
	}
	// 最后统一累加 DC/MC/SC（:696-698，三种类型都走这里）
	a.dcLo += int(v.dc)
	a.dcHi += int(v.dc2)
	a.mcLo += int(v.mc)
	a.mcHi += int(v.mc2)
	a.scLo += int(v.sc)
	a.scHi += int(v.sc2)
	return a
}

// add 累加另一件装备的加成（原版就是一路 `Inc`）。
func (a *addAbil) add(b addAbil) {
	a.hit += b.hit
	a.speed += b.speed
	a.hitSpeed += b.hitSpeed
	a.weight += b.weight
	a.wearWeight += b.wearWeight
	a.handWeight += b.handWeight
	a.luck += b.luck
	a.unluck += b.unluck
	a.antiPoison += b.antiPoison
	a.poisonRecover += b.poisonRecover
	a.healthRecover += b.healthRecover
	a.spellRecover += b.spellRecover
	a.antiMagic += b.antiMagic
	a.hp += b.hp
	a.mp += b.mp
	a.acLo += b.acLo
	a.acHi += b.acHi
	a.macLo += b.macLo
	a.macHi += b.macHi
	a.dcLo += b.dcLo
	a.dcHi += b.dcHi
	a.mcLo += b.mcLo
	a.mcHi += b.mcHi
	a.scLo += b.scLo
	a.scHi += b.scHi
}

// itemSpecials 是"装备**形状**"带来的特殊效果。
//
// 原版在 `RecalcAbilitys` 里按装备槽位逐件判 `StdItem.Shape`
// （ObjBase.pas:3240-3360 那一大段 `if (i = U_NECKLACE) then ...` /
// `if (i = U_RINGR) or (i = U_RINGL) then ...`），把结果存进一批布尔标志。
//
// ⚠️ 只做**我们物品表里真实存在**的那些（形状号→物品见下表）。原版表里还有
// 祈祷(127-130)/猛击(200-202)/罗汉(203-205)/清心(206-208)/记忆全套/五行(216) 等，
// 这些物品不在官方 1.76 物品表里（我们数据里查不到）⇒ 不实现，别照抄形状号。
//
//	111 隐身戒指(idx89)   113 麻痹戒指(137)  114 复活戒指(138)
//	115 火焰戒指(139)     116 治愈戒指(140)  117 愤怒戒指(141)
//	118 护身戒指(142)     119 超负载戒指(143) 120 技巧项链(144)
//	121 探测项链(149)     133/134/135 魔血套(295/296/297)
//	136/137/138 虹魔套(298/299/300)
type itemSpecials struct {
	// paralysis：命中时按"目标抗毒 + 5"抽签把目标**石化 5 秒**（_Attack:22265）
	paralysis bool
	// revival：死亡前拦一次，60 秒冷却，每次扣 1000 耐久（:3755 + ItemDamageRevivalRing）
	revival bool
	// hideMode：装备期间始终处于"透明"状态（STATE_TRANSPARENT，:3367/:8947）
	hideMode bool
	// noDrop：死亡不掉物品（:15498 / :20566 / :26660）
	noDrop bool
	// fastTrain：修炼点 ×3（TBaseObject.TrainSkill，:21820）
	fastTrain bool
	// teleport：开放 `@move X Y` 命令（原版 `m_boTeleport`：
	// `TPlayObject.CmdUserMoveXY` 开头就是 `if m_boTeleport then`，:15195）
	teleport bool
	// flameRing / healRing：装备期间**临时授予**火球术(1) / 治愈术(2)（等级 1）。
	// 原版 `m_boFlameRing` / `m_boRecoveryRing` → `AddItemSkill(1/2)`（:3453-3457）。
	flameRing bool
	healRing  bool
	// muscleRing：**超负载戒指**（Shape 119），把三个负重上限各自**翻倍**。
	//
	// 原版 `m_boMuscleRing`：`RecalcAbilitys` 里按 `StdItem.Shape = 119` 置位（:3276），
	// 随后 `Inc(m_WAbil.MaxWeight, m_WAbil.MaxWeight)` 等三连（:3459-3464）。
	// 消费点见 weight.go 的 playerWeightInfo。
	muscleRing bool
	// probeNecklace：开放 `@searchhuman <角色名>`（原版 `m_boProbeNecklace`，
	// `TPlayObject.CmdSearchHuman`，:14356；10 秒冷却）
	probeNecklace bool
	// hongMo 是虹魔套的 ΣAniCount（每件 5，见物品表）：
	//   吸血 `伤害 × ΣAniCount / 100`，原版要求 ≥2 点才回血（_Attack:22273）
	hongMo int
	// hongMoPieces 是虹魔套的件数：3 件齐 ⇒ 额外 +2 准确（:3343）
	hongMoPieces int
	// moXie 是魔血套（形状 133/134/135）的 ΣAniCount，moXiePieces 是件数。
	// 效果是"把 MaxMP 挪给 MaxHP"（`:3465-3473`）：挪的量 = ΣAniCount，三件齐再 +50。
	moXie       int
	moXiePieces int
}

// playerItemSpecials 汇总当前装备触发的特殊效果。
//
// ⚠️ 只扫 `HumItems`（已穿戴）：物品表里有些**药包**（StdMode=31）也带 111/113/114
// 这些形状号，背包里的它们不该生效 —— 原版也是只遍历 `m_UseItems`。
func (s *Server) playerItemSpecials(p *Player) itemSpecials {
	var out itemSpecials
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return out
	}
	for _, u := range p.Char.Data.HumItems {
		if u == nil || u.Index == 0 {
			continue
		}
		it := s.data.tables.Items.Get(int(u.Index) - 1)
		if it == nil {
			continue
		}
		switch it.Shape {
		case 111:
			out.hideMode = true
		case 113:
			out.paralysis = true
		case 114:
			out.revival = true
		case 112:
			out.teleport = true
		case 115:
			out.flameRing = true
		case 116:
			out.healRing = true
		case 119:
			out.muscleRing = true
		case 121:
			out.probeNecklace = true
		case 117:
			out.noDrop = true
		case 120:
			out.fastTrain = true
		case 136, 137, 138: // 虹魔套
			out.hongMoPieces++
			out.hongMo += int(it.AniCount)
		case 133, 134, 135: // 魔血套
			out.moXiePieces++
			out.moXie += int(it.AniCount)
		}
	}
	return out
}

// playerHitSpeed 返回玩家的攻击速度（`m_nHitSpeed := m_AddAbil.nHitSpeed`，
// ObjBase.pas:3403）。
//
// ⚠️ 原版在装备之后还有两项**不建模**（都依赖我们没实现的系统，见 §2.1）：
//
//	`m_wStatusArrValue[3]` 状态值加攻速（:3437，脚本/状态系统给的 +N）
//	套装 `m_bopirit` +2 / `m_boSmashSet` +1（:3476/:3481，我们没有套装表）
//
// 这两项在原版里对普通玩法几乎不可达（套装表缺数据、状态值要靠 GM），保持 0。
func (s *Server) playerHitSpeed(p *Player) int {
	return s.playerAddAbil(p).hitSpeed
}

// playerAddAbil 把身上所有装备的加成汇总（原版 RecalcAbilitys 的装备循环，
// ObjBase.pas:3000-3350 那一大段）。
//
// ⚠️ **武器也算在内**：它走自己的分支（准确/幸运/速度/诅咒），不贡献防御 ——
// 所以防御要用 `acLo/acHi`/`macLo/macHi`，不能像早期那样把每件的 AC/MAC 直接相加。
func (s *Server) playerAddAbil(p *Player) addAbil {
	var out addAbil
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return out
	}
	for _, u := range p.Char.Data.HumItems {
		if u == nil || u.Index == 0 {
			continue
		}
		it := s.data.tables.Items.Get(int(u.Index) - 1)
		if it == nil {
			continue
		}
		out.add(itemEffective(it, u).apply())
	}
	return out
}

// loByteOf / hiByteOf 复刻 Delphi 的 LoByte/HiByte（参数按整数取低/高 8 位）。
//
// ⚠️ 负数也一样：Delphi 里 `LoByte(-1)` 是 255（按 Integer 取低字节），
// 不是 0。衣服的 Source 我们数据里都是 0，但语义要写对。
func loByteOf(v int) int { return v & 0xFF }

func hiByteOf(v int) int { return (v >> 8) & 0xFF }

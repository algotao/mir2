package gamesvr

import (
	"github.com/algotao/mir2/server/internal/pvp"
	"log"
	"math/rand/v2"
	"net"

	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
)

// deathDrop 死亡掉落：按概率把随身装备与包裹物品丢在**死亡处附近**。
//
// 逐句对照官方 1.76（`ObjBase.pas`）：
//
//   - **死亡不掉经验**：`Die`（:20980-21035）只做掉落（`DropUseItems`/`ScatterBagItems`）
//     与扣幸运（`AddBodyLuck`）。掉经验只存在于 `PKDie` 的 `boKilledLostExp`
//     （:21089，出厂关，我们已按四个开关实现，见 pkdie.go）；
//   - 装备与包裹**用不同的掉率**（原版 `nDieDropUseItemRate {30}` /
//     `nDieRedDropUseItemRate {15}` / 包裹 `Random(3)`）；
//     **出厂数值**（`!setup.txt:814-817`，原版就是 `Random(N) = 0` 的 1/N 语义）：
//     包裹 1/200、装备 1/30（红名 PKLevel>2 ⇒ 装备 1/15）、红名（PKLevel≥2）包裹**全掉**
//     （`DieRedScatterBagAll=1`）。这两条原版判据我们以前整段没做，现在按原版补齐；
//   - 散落范围 **2**（`dropwide := 2`，:20570）；逐件算落点，见 dropPosition；
//   - **FIGHT / FIGHT3 区内整段跳过**（含扣幸运），见函数内注释。
//
// chanceOneIn 是原版的 `Random(N) = 0`（1/N）。N <= 0 表示**关掉这一档**。
//
// ⚠️ 原版没有"关掉"的表达（N=0 会 `Random(0)` 直接除零），这是我们为测试加的开关
// （e2e 里 `-death-drop-equip-rate 0` 的用法）。
func chanceOneIn(n int) bool {
	return n > 0 && rand.IntN(n) == 0
}

func (s *Server) deathDrop(c net.Conn, p *Player) {
	// 愤怒戒指（Shape 117 ⇒ `m_boAngryRing`）：原版在三条掉落路径的开头都是
	// `if m_boAngryRing or m_boNoDropUseItem then Exit;`（:15498/:20566/:26660）。
	// 另外两个开关（m_boNoDropItem / 地图 boNODROPITEM）我们没有对应数据，只做戒指。
	if p != nil && p.equipSpecials.noDrop {
		logpvp("%s 戴着愤怒戒指，死亡不掉落物品", p.Char.Name)
		return
	}
	d := p.Char.Data
	if d == nil {
		return
	}

	// ⚠️ **FIGHT / FIGHT3 区死亡不掉任何东西**。
	//
	// 原版（ObjBase.pas:20983）把整段掉落——`DropUseItems`/`ScatterBagItems`/
	// `ScatterGolds` 以及**扣幸运的 `AddBodyLuck`**——都包在
	//
	//	if (not m_PEnvir.Flag.boFIGHTZone) and (not m_PEnvir.Flag.boFIGHT3Zone)
	//	   and (not m_boAnimal) then
	//
	// 里面。官方图里带这些标记的：FIGHT3 = F001-F010（行会战争地图）+
	// G003/G005（热血足球场）；FIGHT = SD000-SD002（小黑屋）。
	//
	// 门放在这里而不是各调用点，是因为它必须同时盖住两条死亡路径
	//（被怪杀：main.go 的 hit 结算 → revive；被玩家杀：killPlayerByPlayer → revivePvP → revive）。
	// 这里也是唯一收口，顺手修掉了 PvP 路径重复调用导致的"掉两遍"。
	// 地图标记 `NODROPITEM`（官方 `ObjBase.pas:20566`/`:20995`）：该图死亡不掉东西。
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoDropItem {
		log.Printf("%s 在禁止掉落的地图(%s NODROPITEM)死亡，按原版不掉落", p.Char.Name, mi.ID)
		return
	}
	if z := s.zoneOf(p.Obj.MapRef()); z.SuppressDeathDrop() {
		log.Printf("%s 在 FIGHT 区(%s FIGHT=%v FIGHT3=%v)死亡，按原版不掉落",
			p.Char.Name, z.Name, z.FightZone, z.Fight3Zone)
		return
	}

	x, y := p.Obj.PosX(), p.Obj.PosY()
	dropped := 0

	// ⚠️ 落点是**逐件**算的（原版 `DropItemDown` 逐件调 `GetDropPosition`）：
	// 每件都能看到前面几件已经占了格子，于是同一次死亡掉的东西会自己散开。
	// 散落范围用**玩家死亡**的 2（`dropwide := 2`，ObjBase.pas:20570/15533）；
	// 忽略自己，否则"刚死的那个人"会把自己脚底那格挡掉（原版看 m_boDeath）。
	dropAt := func(it *pb.UserItem) {
		dx, dy := s.dropPosition(p.Obj.MapRef(), x, y, dropRangePlayerDie, p.Obj.ID)
		s.spawnGroundItem(p, it, dx, dy)
	}

	// 红名档（原版 `PKLevel`：`m_nPkPoint div 100`，见 pvp.go）。
	pkLevel := pvp.PKLevel(int32(d.PkPoint))
	// 包裹物品（从后往前删，避免下标错乱）。
	//
	// 原版 `ScatterBagItems`（:26662-26673）：`boDropall := boDieRedScatterBagAll and (PKLevel >= 2)`
	// ⇒ 红名全掉，否则 `Random(nDieScatterBagRate) = 0`（出厂 200 ⇒ 1/200）。
	dropAll := s.cfg.deathScatterBagAllOn && pkLevel >= 2
	// ⚠️ 遍历 + 摘除必须在同一把锁里（`takeBagItem` 会前移元素 ⇒ 分开做必然错位）
	p.stateMu.Lock()
	for i := len(d.BagItems) - 1; i >= 0; i-- {
		it := d.BagItems[i]
		if it == nil || (!dropAll && !chanceOneIn(s.cfg.deathDropBagOneIn)) {
			continue
		}
		takeBagItemLocked(d, i)
		dropAt(it)
		dropped++
	}
	p.stateMu.Unlock()
	// 装备。原版 `DropUseItems`（:15526-15531）：红名（PKLevel > 2）用 15、其余用 30，
	// 判据是 `Random(nRate) <> 0 then Continue`（⇒ 命中即 1/nRate）。
	equipOneIn := s.cfg.deathDropEquipOneIn
	if pkLevel > 2 {
		equipOneIn = s.cfg.deathDropRedEquipIn
	}
	equipDropped := false
	for i := len(d.HumItems) - 1; i >= 0; i-- {
		it := d.HumItems[i]
		if it == nil || it.Index == 0 || !chanceOneIn(equipOneIn) {
			continue
		}
		// ⚠️ **置空槽位，不要压缩数组**（P1-9）。`HumItems` 是**定长穿戴槽位表**
		//（原版 `THumItems = array[0..12]`，下标就是槽位号），不是可删元素的列表：
		// 原来这里 `append(d.HumItems[:i], d.HumItems[i+1:]...)` 会把后面所有装备
		// 前移一格、长度减一 ⇒ 剩下的装备被当成戴在**错的槽位**（属性重算、
		// 协议下发、后续换装全按槽号解释）；掉两件还会继续错位。
		// 原版 `DropUseItems` 走的是 `m_UseItems[n] := ...空的 TUserItem...`，
		// 长度恒定。空槽用零值 UserItem 占位（与原版/我们的存档约定一致）。
		d.HumItems[i] = &pb.UserItem{}
		dropAt(it)
		dropped++
		equipDropped = true
	}

	if dropped == 0 {
		return
	}
	// 掉了装备 ⇒ 外观（武器/衣服位）与由装备推导的一切（上限、负重、特殊效果、
	// AC/MAC/DC）都要重算，否则玩家"光着身子顶着旧属性"。
	if equipDropped {
		s.updateFeature(p)
		s.refreshSpecials(p)
		s.applyEquipHpMp(p)
		s.applyWeights(p)
	}
	s.sendBagItems(c, p)
	s.sendUseItems(c, p)
	if equipDropped {
		if d.Abil != nil {
			abRaw := abilityFromPB(d.Abil).Bytes()
			s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(d.Job), 0, 0, string(abRaw[:]))
			s.sendSubAbility(c, p)
		}
		s.broadcastStatus(p)
	}
	log.Printf("%s 死亡掉落 %d 件物品于 (%d,%d)", p.Char.Name, dropped, x, y)
}

// revive 死亡后回城复活（满血满蓝）。
//
// 此前是原地满血，属于占位实现。回城点取存钱档的 HomeMap/HomeX/HomeY，
// 缺失时退回该地图的 StartPoint。
//
// ⚠️ 这里**曾经**挂着 `TODO(P4)：死亡惩罚（掉经验 / 掉物品）尚未实现`——已过时：
// 物品掉落一直在（`deathDrop`，掉率/范围见它的注释），而"掉经验"原版死亡路径
// 根本没有（只有 PKDie 的 `boKilledLostExp`，也已实现）。
//
// killer 是"致死的那一方"（怪物或玩家，nil = 非战斗死亡：@die / GM / 自伤）。
// 只有 FIGHT3 区的行会战积分需要它（原版用 m_LastHiter）。
func (s *Server) revive(c net.Conn, p *Player, killer *Player) {
	if p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	p.refillHPMP() // 复活回满（持锁，见 statelock.go）

	// ⚠️ **人物死亡立即退组**（ObjBase.pas:21040-21044，注释原文
	// "以防止组队刷经验"）。挂在 revive 上是因为被怪杀与被玩家杀两条
	// 死亡路径都汇到这里。
	s.groupDrop(p)

	// 宠物陪葬：主人死亡 ⇒ 宠物 HP=0（ObjBase.pas:3969-4000）。
	//
	// ⚠️ 挂在 revive 上是因为"被怪杀"与"被玩家杀"两条死亡路径都汇到这里
	//（killPlayerByPlayer → revivePvP → revive），挂别处会漏掉一条。
	// 原版还有 MasterDieMutiny 判变分支，但 GeeM2 的 !setup.txt 里
	// `MasterDieMutiny=0` ⇒ 恒陪葬，不实现判变。
	s.killSlavesWithMaster(p)

	// 死亡惩罚：**不掉经验**，按概率掉随身物品与装备（原版行为）。
	//
	// ⚠️ 之前这里实现的是"损失部分经验"，与原版相反：传奇里死亡只掉东西，
	// 经验是打死怪才结算的，死亡不会倒退。
	s.deathDrop(c, p)

	// ⚠️ FIGHT3 区（行会战争地图）另有一套：记阵亡/记分/广播比分，并且**前 3 次原地
	// 满血复活**（原版 ObjBase.pas:21018-21038 + UsrEngn.pas:524-534）。
	//
	// ⚠️ 位置必须在 `deathDrop` **之后**：原版也是先走掉落那段（FIGHT3 在图标记上
	// 就被免掉，`ObjBase.pas:20983`），再进 FIGHT3 的结算块。放前面会让
	// "FIGHT3 不掉落"这条日志根本不打（e2e 的 fightdrop-deny 就查它）。
	if s.settleFight3Death(c, p, killer) {
		return
	}

	home := p.Char.Data.HomeMap
	x, y := int(p.Char.Data.HomeX), int(p.Char.Data.HomeY)
	if home == "" {
		home = s.world.defaultMap.Name
	}
	if (x == 0 && y == 0) || home != p.Char.Data.HomeMap {
		if sp := s.startPointOf(home); sp != nil {
			x, y = sp.X, sp.Y
		}
	}

	// 回城点失效（地图没了/坐标非法）时退回默认地图
	if _, err := s.world.maps.Get(home); err != nil {
		home = s.world.defaultMap.Name
		if sp := s.startPointOf(home); sp != nil {
			x, y = sp.X, sp.Y
		}
	}

	if err := s.switchMap(c, p, home, x, y); err != nil {
		log.Printf("%s 回城失败（%v），改为原地复活", p.Char.Name, err)
	}
	s.sendHealthChanged(p, p.Obj.ID, p.hp(), p.mp(), p.maxHP())
	// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
	s.applyWeights(p)
	abRaw := p.abilBytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw))
	s.sendSubAbility(c, p)
	log.Printf("%s 死亡，回城至 %s(%s) (%d,%d)", p.Char.Name, s.world.maps.Name(home), home, x, y)
}

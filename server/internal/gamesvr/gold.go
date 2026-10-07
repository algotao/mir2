package gamesvr

import (
	"log"
	"math"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/world"
)

// 金币的"掉落—拾取—丢弃"链路。
//
// 原版分散在四处，这里按原样搬：
//
//	`TBaseObject.DropGoldDown`    （ObjBase.pas:2301-2344）把 nGold 撒在 3 格内；
//	                              同格已有金币就**并进去**（`AddToMap` 返回的不是新对象即"并了"）
//	`TBaseObject.ScatterGolds`    （:20655-20692）怪物身上的金币按 `MonOneDropGoldCount`
//	                              一堆堆撒出，**最多 17 堆**
//	`TPlayObject.ClientDropGold`  （:16187-16212）玩家主动扔金币
//	`TPlayObject.ClientPickUpItem`（:1664-1730）捡起：金币 `IncGold`，物品进背包；
//	                              两者共用同一套**归属**判定
//
// ⚠️ `dwCanPickUpTick` 那行**不是**"物品过期"，是**掉落归属**（`:1699-1707`）：
//
//	if (GetTickCount - MapItem.dwCanPickUpTick) > dwFloorItemCanPickUpTime then
//	  MapItem.OfBaseObject := nil;        // 过 2 分钟 ⇒ 归属作废，谁都能捡
//	if not IsSelf(OfBaseObject) and not IsOfGroup(OfBaseObject) then
//	  SysMsg(g_sCanotPickUpItem); Exit;   // 否则只有击杀者本人与队友能捡
//
// 归属由掉落方写入：怪物掉落 = 击杀者（`ScatterGolds(GoldOfCreat)` / `DropItemDown(…, ItemOfCreat, …)`），
// **玩家主动扔的** = nil（谁都能捡，`:16265`）。
const (
	// goldName 是地上金币的名字（原版 `sSTRING_GOLDNAME`）。
	goldName = "金币"
	// monOneDropGoldCount 是**单堆**金币的上限（官方 `!setup.txt:753`）。
	monOneDropGoldCount = 2000
	// monGoldPileLimit 是 `ScatterGolds` 的堆数上限（原版 `if i >= 17 then Break`）。
	monGoldPileLimit = 17
	// controlDropItem / canDropGold / inSafeDisableDrop 见官方 `!setup.txt:803-805`
	// ⇒ 出厂全是"不限制"（`ControlDropItem=0`、`InSafeDisableDrop=0`）。
	controlDropItem   = false
	canDropGold       = 1000
	inSafeDisableDrop = false
	// floorItemCanPickUpTime 是**掉落归属**的保留时长（官方 `FloorItemCanPickUpTime=120000`，
	// 毫秒 ⇒ 2 分钟）。过时后归属作废。
	floorItemCanPickUpTime = 120 * time.Second
)

// String.ini 的文案（我们家的 `data/envir/String.ini` 里就有这几条）。
const (
	sCanotPickUpItem     = "在一定时间以内无法捡起此物品!"
	sCanotDropGold       = "金币太少不允许扔!"
	sCanotDropInSafeZone = "安全区禁止扔物品!"
	sCanotDropItem       = "当前无法进行此操作!"
)

// goldShape 复刻 `GetGoldShape`（M2Share.pas:3568-3575）：按金额选地上金币的外观。
func goldShape(nGold int64) uint16 {
	switch {
	case nGold >= 1000:
		return 116
	case nGold >= 300:
		return 115
	case nGold >= 70:
		return 114
	case nGold >= 30:
		return 113
	default:
		return 112
	}
}

// sendGroundItem 把一件地面物品发给某个玩家（原版 `SendRefMsg(RM_ITEMSHOW, …)`）。
//
// ⚠️ 金币**没有** `StdItem`（原版它就是"名字 + Count + Looks"的 TMapItem）⇒ 现场合成
// 一个只带名字与外观的 ClientItem，不能走 `buildClientItem`（那里 `Index == 0` 直接返回 false）。
func (s *Server) sendGroundItem(pl *Player, gi *GroundItem) {
	if pl == nil || gi == nil {
		return
	}
	var ci *proto.ClientItem
	if gi.Gold > 0 {
		ci = &proto.ClientItem{}
		ci.S.SetName(goldName)
		ci.S.Looks = gi.Looks
	} else {
		built, ok := s.buildClientItem(gi.Item)
		if !ok {
			return
		}
		ci = built
	}
	s.send(pl.conn, proto.SM_ADDITEM, int32(gi.ID), uint16(gi.X), uint16(gi.Y), gi.Looks,
		string(ci.Append(nil)))
}

// broadcastGroundItem 把一件地面物品发给视野内所有人。
func (s *Server) broadcastGroundItem(gi *GroundItem) {
	if gi == nil {
		return
	}
	s.broadcastToViewers(gi.Map, gi.X, gi.Y, func(pl *Player) { s.sendGroundItem(pl, gi) })
}

// spawnGroundGold 在 (x,y) 放一堆金币，**同格已有金币就并进去**（原版 `AddToMap` 的语义）。
//
// owner 是归属者（击杀者）的 ActorId，0 表示无归属（玩家自己扔的、脚本刷的）。
// ⚠️ "并进去"时**不改** `CreatedAt`/`Owner`：原版那条路上新建的 MapItem 直接被
// `Dispose` 掉，已有那件的 `dwCanPickUpTick` 与 `OfBaseObject` 都不更新。
func (s *Server) spawnGroundGold(m *world.Map, x, y int, nGold int64, owner uint32) *GroundItem {
	if m == nil || nGold <= 0 {
		return nil
	}
	// 金币堆的落点也受"每格一件"的约束吗？不受 —— 原版允许同格并堆（见上）。
	s.mu.Lock()
	var target *GroundItem
	for _, gi := range s.world.ground {
		if gi.Gold > 0 && gi.Map == m && gi.X == x && gi.Y == y {
			gi.Gold += nGold
			gi.Looks = goldShape(gi.Gold)
			target = gi
			break
		}
	}
	if target == nil {
		target = &GroundItem{
			ID:        s.world.groundSeq.Add(1),
			Name:      goldName,
			Map:       m,
			X:         x,
			Y:         y,
			Looks:     goldShape(nGold),
			Gold:      nGold,
			Owner:     owner,
			CreatedAt: time.Now(),
		}
		s.world.ground[target.ID] = target
	}
	s.mu.Unlock()

	// 原版新建与"并进去"两条路都会发一次 RM_ITEMSHOW（并堆后图标档位会变）。
	s.broadcastGroundItem(target)
	return target
}

// dropGoldDown 复刻 `TBaseObject.DropGoldDown`（ObjBase.pas:2301-2344）。
//
// 落点用 `GetDropPosition(m_nCurrX, m_nCurrY, 3, …)` ⇒ 范围 **3**（与怪物死亡掉落同档）。
// 返回 false 表示没落地 ⇒ 调用方必须把钱退回去
// （原版 `if not DropGoldDown(…) then Inc(m_nGold, nGold)`）。
func (s *Server) dropGoldDown(m *world.Map, x, y int, nGold int64, owner uint32) bool {
	if m == nil || nGold <= 0 {
		return false
	}
	nx, ny := s.dropPosition(m, x, y, dropRangeMonsterDie, owner)
	return s.spawnGroundGold(m, nx, ny, nGold, owner) != nil
}

// scatterGolds 复刻 `TBaseObject.ScatterGolds`（ObjBase.pas:20655-20692）：
// 金币按 `MonOneDropGoldCount` 一堆堆撒出去，**最多 17 堆**（原版剩下的留在怪身上，
// 而我们这里怪已经没了 ⇒ 多出来的直接不打）。
//
// owner 是击杀者的 ActorId：原版把它写进 `MapItem.OfBaseObject`，于是 2 分钟内
// **只有击杀者与队友**能捡（`ClientPickUpItem` 的归属判定）。
func (s *Server) scatterGolds(m *world.Map, x, y int, nGold int64, owner uint32) int {
	if m == nil || nGold <= 0 {
		return 0
	}
	left, piles := nGold, 0
	for i := 0; i < monGoldPileLimit && left > 0; i++ {
		pile := left
		if pile > monOneDropGoldCount {
			pile = monOneDropGoldCount
		}
		left -= pile
		if s.dropGoldDown(m, x, y, pile, owner) {
			piles++
			continue
		}
		left += pile // 没落地 ⇒ 退回剩余，别再试
		break
	}
	dropped := int(nGold - left)
	if dropped > 0 {
		log.Printf("金币落地 %d（%d 堆）于 (%d,%d)", dropped, piles, x, y)
	}
	return dropped
}

// scatterKillGold 是"怪物死了 ⇒ 掉物品 + 金币撒地"的统一入口（四个击杀点共用）。
//
// 原版结构就是分开的两段：`DropUseItems`（物品）与 `ScatterGolds`（金币），
// 金币由怪物对象带着。我们这边掉落表直接给出金币额（`dropItems` 的返回值）⇒ 喂给
// `scatterGolds`。owner 传击杀者，用作掉落归属。
func (s *Server) scatterKillGold(m *entity.Monster, owner uint32) int {
	if m == nil || m.MapRef() == nil {
		return 0
	}
	// ⚠️ **动物（鸡/鹿/狼）死亡时什么都不掉**：原版 `Die` 里那一整段掉落
	//（`DropUseItems` + `ScatterBagItems` + `ScatterGolds`）被 `(not m_boAnimal)`
	// 挡在门外（ObjBase.pas:20983-20999）⇒ 它的肉与身上的东西要**取肉**时才给
	//（`TakeBagItems`，见 butch.go）。
	if s.isAnimal(m) {
		return 0
	}
	gold := s.dropItems(m, owner)
	if gold > 0 {
		s.scatterGolds(m.MapRef(), m.PosX(), m.PosY(), int64(gold), owner)
	}
	return gold
}

// handleDropGold 复刻 `TPlayObject.ClientDropGold`（ObjBase.pas:16187-16212）。
//
// 客户端 `SendDropGold`（ClMain.pas:3184-3190）发
// `MakeDefaultMsg(CM_DROPGOLD, dropgold, 0, 0, 0)` ⇒ 金额在 **Recog**。
//
// ⚠️ 原版还有 `m_boCanDrop`（脚本开关）那半，我们没建模 ⇒ 只实现了地图标记那半
// （与 `handleDropItem` 同一处注释）。
func (s *Server) handleDropGold(c net.Conn, p *Player, nGold int64) {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Obj == nil || nGold <= 0 {
		return
	}
	if inSafeDisableDrop {
		if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.Safe {
			s.sysMsg(c, sCanotDropInSafeZone)
			return
		}
	}
	if controlDropItem && nGold < canDropGold {
		s.sysMsg(c, sCanotDropGold)
		return
	}
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoThrowItem {
		s.sysMsg(c, sCanotDropItem)
		return
	}
	// 原版是 `if nGold >= m_nGold then Exit`：**不许把钱扔光**（留至少 1）。
	// ⚠️ "够且留底"的判定与扣款必须在**同一把锁**里做：分开的话，别的 goroutine
	// 的收支会在两者之间插进来（丢更新），甚至能把钱扣成负数。
	left, ok := int64(0), false
	p.withGold(func(cur int64) int64 {
		if nGold >= cur {
			return cur
		}
		left, ok = cur-nGold, true
		return left
	})
	if !ok {
		return
	}
	if !s.dropGoldDown(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), nGold, 0) {
		p.addGold(nGold) // 没落地就退回去
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(left), 0, 0, 0, "")
	log.Printf("%s 扔下金币 %d 于 (%d,%d)（余 %d）", p.Char.Name, nGold, p.Obj.PosX(), p.Obj.PosY(), left)
}

// canPickUpGround 复刻 `ClientPickUpItem` 的**归属**判定（ObjBase.pas:1699-1707）。
func (s *Server) canPickUpGround(p *Player, gi *GroundItem) bool {
	if gi == nil || gi.Owner == 0 || p == nil || p.Obj == nil || gi.Owner == p.Obj.ID {
		return true
	}
	// 过 `FloorItemCanPickUpTime` ⇒ 原版在这里把 OfBaseObject 清空 ⇒ 谁都能捡。
	if time.Since(gi.CreatedAt) > floorItemCanPickUpTime {
		return true
	}
	owner := s.world.players[gi.Owner]
	if owner == nil || owner == p {
		return true // 归属者已下线 ⇒ 不卡人
	}
	return s.inSameGroup(p, owner)
}

// pickupGold 是拾取金币：原版 `if IncGold(MapItem.Count) then … else 放回地上`。
func (s *Server) pickupGold(c net.Conn, p *Player, gi *GroundItem) {
	if p.Char.Data == nil || gi.Gold <= 0 {
		return
	}
	// ⚠️ 溢出判定与加钱同锁（原版 `if IncGold(MapItem.Count) then … else 放回地上`）：
	// 分开做时，并发的收支会让"刚才判定没溢出"这件事失效。
	ok := false
	nowGold := int64(0)
	p.withGold(func(cur int64) int64 {
		if cur > math.MaxInt64-gi.Gold {
			return cur // 溢出（原版 IncGold 返回 False）⇒ 放回地上，不能凭空吞掉
		}
		ok, nowGold = true, cur+gi.Gold
		return nowGold
	})
	if !ok {
		s.mu.Lock()
		s.world.ground[gi.ID] = gi
		s.mu.Unlock()
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(nowGold), 0, 0, 0, "")
	s.broadcastToViewers(gi.Map, gi.X, gi.Y, func(other *Player) {
		s.send(other.conn, proto.SM_ITEMHIDE, int32(gi.ID), uint16(gi.X), uint16(gi.Y), 0, "")
	})
	log.Printf("%s 捡起金币 %d 于 (%d,%d)（共 %d）", p.Char.Name, gi.Gold, gi.X, gi.Y, nowGold)
}

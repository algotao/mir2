package gamesvr

import (
	"log"
	"net"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// 装备穿脱（CM_TAKEONITEM=1003 / CM_TAKEOFFITEM=1004）。
//
// ⚠️ 字段语义对照官方两端逐个核过（P1-7）：
//
//	客户端 ClMain.pas:3621-3635
//	  `SendTakeOnItem(where, itmindex, itmname)`  ⇒ MakeDefaultMsg(CM_TAKEONITEM, itmindex, where, 0, 0)
//	  `SendTakeOffItem(where, itmindex, itmname)` ⇒ MakeDefaultMsg(CM_TAKEOFFITEM, itmindex, where, 0, 0)
//	  ⇒ **Recog = 物品的 MakeIndex（唯一 ID）**、**Param = 装备槽位**、body = 物品名
//	    （FState.pas:3458 传 `g_MovingItem.Item.MakeIndex` 与目标槽 `where`；
//	      :4768 脱下时传 `-(Index+1)`，即槽位）
//	服务端 ObjBase.pas:4694-4700
//	  `ClientTakeOnItems(nParam2, nParam1, sMsg)` ⇒ (btWhere = Param, nItemIdx = Recog)
//	  按 MakeIndex 在**背包**里找那件物品，再 `CheckUserItems(btWhere, StdItem)` 核对槽位类型
//
// 我们早先的实现把 Param 当背包下标、把 Recog 当槽位（自己编的契约）⇒ 官方客户端
// 一穿就穿错/没反应；同理脱下的 Recog 也被当成槽位。现在按原版改回 MakeIndex 定位。

// 装备槽位常量见 internal/proto 的 `Slot*`（客户端按同一套下标发消息 ⇒ 属协议契约）。
// 原版 `CheckAmulet` 会**同时查护身符槽与左手镯槽**（U_BUJUK=9 / U_ARMRINGL=5），
// 我们这里统一放护身符槽。

// stdModeToSlot 是物品 StdMode → 装备槽的映射。
//
// 取自 stditems 的实际分布：
// 5/6 武器、10/11 衣服(男/女)、15/16 头盔/斗笠、19/20/21 项链、
// 22/23 戒指、24/26 手镯、52 靴子、54 腰带。
var stdModeToSlot = map[uint8]int{
	5: proto.SlotWeapon, 6: proto.SlotWeapon,
	10: proto.SlotDress, 11: proto.SlotDress,
	15: proto.SlotHelmet, 16: proto.SlotHelmet,
	19: proto.SlotNecklace, 20: proto.SlotNecklace, 21: proto.SlotNecklace,
	22: proto.SlotRingL, 23: proto.SlotRingR,
	24: proto.SlotArmRingL, 26: proto.SlotArmRingR,
	52: proto.SlotBoots,
	54: proto.SlotBelt,
	// 护身符/毒药：原版可装在左手镯(5)或护身符槽(9)，我们统一放护身符槽，
	// 左手镯留给真正的首饰（StdMode 24/26）。
	25: proto.SlotBujuk,
}

// equipSlotFor 返回物品应穿戴的槽位，不可穿戴返回 -1。
func equipSlotFor(stdMode uint8) int {
	if s, ok := stdModeToSlot[stdMode]; ok {
		return s
	}
	return -1
}

// handleTakeOn 处理穿戴（CM_TAKEONITEM = 1003）。
//
// Recog = 物品 MakeIndex、Param = 目标装备槽位、body = 物品名（见文件头）。
func (s *Server) handleTakeOn(c net.Conn, p *Player, m wire.Packet) {
	data := p.Char.Data
	if data == nil || data.Abil == nil {
		return
	}
	slot := int(m.Head.Param)
	makeIdx := int32(m.Head.Recog)
	if makeIdx == 0 || slot < 0 || slot >= len(data.HumItems) {
		s.sendTakeOnFail(c, -1)
		return
	}
	bagIdx, it, _ := s.itemByMakeIndex(p, makeIdx, m.Body, false)
	if it == nil || it.Index == 0 || bagIdx < 0 || bagIdx >= p.bagLen() {
		s.sendTakeOnFail(c, -1)
		return
	}
	tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
	if tmpl == nil {
		s.sendTakeOnFail(c, -1)
		return
	}
	// 槽位类型核对（原版 `CheckUserItems(btWhere, StdItem)`，ObjBase.pas:17103）：
	// 客户端说"穿到槽 1"时必须真是一件武器 —— 否则就是伪造的报文。
	if want := equipSlotFor(tmpl.StdMode); want < 0 || want != slot {
		log.Printf("%s 穿 %s 被拒：物品该进槽 %d，报文说槽 %d",
			p.Char.Name, tmpl.Name, want, slot)
		s.sendTakeOnFail(c, -1)
		return
	}
	// 等级需求（Need 字段是需求类型 0=等级 1=攻击 2=魔法 3=精神，
	// 这里只校验等级，其余需求类型待补）
	if tmpl.NeedLevel > 0 && p.level() < tmpl.NeedLevel {
		log.Printf("%s 等级不足，无法穿 %s（需 %d 级）", p.Char.Name, tmpl.Name, tmpl.NeedLevel)
		s.sendTakeOnFail(c, -1)
		return
	}
	// 职业需求（部分物品 Need=职业限定），数据里 Need 语义是需求类型，暂不校验

	// ⚠️ 负重检查（原版 ClientTakeOnItems，:22985-22998）：武器槽比**手负重**
	//（单件重量），其余槽比**佩带负重**（新物品 + 该槽原有物品的重量）。
	if msg := s.takeOnBlockedByWeight(p, tmpl, slot); msg != "" {
		log.Printf("%s 穿 %s 失败：%s", p.Char.Name, tmpl.Name, msg)
		s.sysMsg(c, msg)
		s.sendTakeOnFail(c, -1)
		return
	}

	// 交换：原装备回到背包同一格
	old := data.HumItems[slot]
	data.HumItems[slot] = it
	if old == nil {
		old = &pb.UserItem{}
	}
	p.withBag(func(d *pb.CharacterData) { d.BagItems[bagIdx] = old })
	// ⚠️ 原来这里没有这一步：当该槽**原本没装备**（old 为空）时，上面那行会在背包
	// 中间留下**空洞** ⇒ 客户端按原版语义发回的"压缩位次"与服务端数组下标错位
	//（表现：用错物品/穿错装备，且只在背包中间有空洞时复现）。
	// `compactBag` 早就有了（deal.go），只是这里漏调。
	s.compactBag(p)

	s.refreshEquip(c, p)
	// 回执：Recog = 外观（`GetFeatureToLong`，客户端 `g_MySelf.m_nFeature := msg.Recog`）、
	// Param/Tag = GetFeatureEx（我们只有一套 Feature，见 SM_FEATURECHANGED）。
	s.send(c, proto.SM_TAKEON_OK, int32(p.Obj.FeatureBits()),
		uint16(p.Obj.FeatureBits()&0xFFFF), uint16(p.Obj.FeatureBits()>>16), 0, "")
	log.Printf("%s 穿上 %s（槽%d）", p.Char.Name, tmpl.Name, slot)
}

// sendTakeOnFail 回穿戴失败（原版 `SM_TAKEON_FAIL` 的 Recog = 错误码：
// -1 = 物品/槽位不对，-2 = 该槽没东西，-3 = 背包已满，-4 = 该物品不允许取下）。
func (s *Server) sendTakeOnFail(c net.Conn, code int) {
	s.send(c, proto.SM_TAKEON_FAIL, int32(code), 0, 0, 0, "")
}

// handleTakeOff 处理脱下（CM_TAKEOFFITEM = 1004）。
//
// Recog = 物品 MakeIndex、Param = 装备槽位、body = 物品名（见文件头）。
// 原版 `ClientTakeOffItems`（ObjBase.pas:17221）：要求 `m_UseItems[btWhere].MakeIndex = nItemIdx`，
// 名字也要一致 —— 也就是说"槽位 + MakeIndex"两个字段必须自洽，否则不动。
func (s *Server) handleTakeOff(c net.Conn, p *Player, m wire.Packet) {
	slot := int(m.Head.Param)
	makeIdx := int32(m.Head.Recog)
	data := p.Char.Data
	if data == nil || slot < 0 || slot >= len(data.HumItems) || makeIdx == 0 {
		s.sendTakeOffFail(c, -1)
		return
	}
	it := data.HumItems[slot]
	if it == nil || it.Index == 0 {
		s.sendTakeOffFail(c, -2)
		return
	}
	if it.MakeIndex != makeIdx || !s.itemNameMatches(it, m.Body) {
		log.Printf("%s 脱下被拒：槽%d 里是 MakeIndex=%d，报文说 %d",
			p.Char.Name, slot, it.MakeIndex, makeIdx)
		s.sendTakeOffFail(c, -1)
		return
	}

	// 找背包空位；满了就不允许脱下（否则物品会丢失）。
	// ⚠️ "找空位 + 放进去"必须在同一把锁里：否则并发的成交/退回可能挤掉这件装备。
	free := -1
	full := false
	p.withBag(func(d *pb.CharacterData) {
		for i, b := range d.BagItems {
			if b == nil || b.Index == 0 {
				free = i
				break
			}
		}
		switch {
		case free >= 0:
			d.BagItems[free] = it
		case len(d.BagItems) < entity.MaxBagSize:
			d.BagItems = append(d.BagItems, it)
			free = len(d.BagItems) - 1
		default:
			full = true
		}
	})
	if full {
		log.Printf("%s 背包已满，无法脱下装备", p.Char.Name)
		s.sendTakeOffFail(c, -3)
		return
	}
	data.HumItems[slot] = &pb.UserItem{}

	s.refreshEquip(c, p)
	s.send(c, proto.SM_TAKEOFF_OK, int32(p.Obj.FeatureBits()),
		uint16(p.Obj.FeatureBits()&0xFFFF), uint16(p.Obj.FeatureBits()>>16), 0, "")
	if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil {
		log.Printf("%s 脱下 %s（槽%d → 背包%d）", p.Char.Name, tmpl.Name, slot, free)
	}
}

// sendTakeOffFail 回脱下失败（原版 `SM_TAKEOFF_FAIL` 的错误码同 sendTakeOnFail）。
func (s *Server) sendTakeOffFail(c net.Conn, code int) {
	s.send(c, proto.SM_TAKEOFF_FAIL, int32(code), 0, 0, 0, "")
}

// refreshEquip 装备变更后的一次性刷新：外观重算 + 三条下发 + 广播外观。
func (s *Server) refreshEquip(c net.Conn, p *Player) {
	s.updateFeature(p)
	s.sendUseItems(c, p)
	s.sendBagItems(c, p)
	// ⚠️ **先重算、再发属性包**：`applyEquipHpMp`/`applyWeights` 会改
	// MaxHp/MaxMP/负重，而它们原来排在下面 ⇒ 这个属性包里带的是**旧值**
	//（客户端负重条/血上限要等下一次重发才对）。
	// 装备**形状**可能带来隐身/麻痹/复活等特殊效果 ⇒ 缓存也要跟着换。
	s.refreshSpecials(p)
	s.applyEquipHpMp(p)
	s.applyWeights(p)
	// 属性变了也要重发（AC/MAC/DC、上限、负重等都来自装备）
	if p.Char.Data != nil && p.Char.Data.Abil != nil {
		// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
		s.applyWeights(p)
		abRaw := abilityFromPB(p.Char.Data.Abil).Bytes()
		s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
		// 装备带"准确/幸运/敏捷"（武器 AC/MAC 高低位、首饰按 StdMode）⇒ 副属性也变了
		s.sendSubAbility(c, p)
	}
	// 装备还带**攻击速度**（武器 MAC 高位、首饰 21/54/64/23 的 AC-MAC）⇒
	// 它走的是另一条包（`RM_CHARSTATUSCHANGED` 的 Param，原版 RecalcAbilitys:3516），
	// 不下发的话客户端挥砍动画速度就停在旧值。
	s.broadcastStatus(p)
	// 让周围玩家看到外观变化（武器/衣服）
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		if other == p {
			return
		}
		s.sendFeatureChanged(other, p)
	})
	s.sendFeatureChanged(p, p)
}

// sendFeatureChanged 下发外观变更（SM_FEATURECHANGED）。
//
// Recog=ActorId, Param=LoWord(feature), Tag=HiWord(feature)。
func (s *Server) sendFeatureChanged(to *Player, who *Player) {
	f := uint32(who.Obj.FeatureBits())
	s.send(to.conn, proto.SM_FEATURECHANGED, int32(who.Obj.ID),
		uint16(f&0xFFFF), uint16(f>>16), 0, "")
}

// updateFeature 按当前装备重算外观的武器/衣服位。
func (s *Server) updateFeature(p *Player) {
	if p.Char.Data == nil {
		return
	}
	weapon, dress := uint8(0), uint8(0)
	if it := p.Char.Data.HumItems[proto.SlotWeapon]; it != nil && it.Index != 0 {
		if t := s.data.tables.Items.Get(int(it.Index) - 1); t != nil {
			weapon = uint8(t.Looks & 0xFF)
		}
	}
	if it := p.Char.Data.HumItems[proto.SlotDress]; it != nil && it.Index != 0 {
		if t := s.data.tables.Items.Get(int(it.Index) - 1); t != nil {
			dress = uint8(t.Looks & 0xFF)
		}
	}
	p.Obj.SetFeatureBits(proto.MakeFeature(
		proto.FeatureRace(p.Obj.FeatureBits()), weapon,
		proto.FeatureHair(p.Obj.FeatureBits()), dress))
}

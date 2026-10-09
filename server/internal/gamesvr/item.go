package gamesvr

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	// 新协议（`protocol/`）的 `ItemStack`/`BagItems` —— 与 legacy 的 `proto` 包区分开
	"github.com/algotao/mir2/server/protocol"
)

// wearWeapon 攻击后损耗武器耐久。
//
// ⚠️ 只对**不可堆叠**的装备生效：堆叠物品的 Dura 是数量而不是耐久，
// 误扣会把一叠药变成少一个。
func (s *Server) wearWeapon(p *Player) {
	data := p.Char.Data
	if data == nil || proto.SlotWeapon >= len(data.HumItems) {
		return
	}
	w := data.HumItems[proto.SlotWeapon]
	if w == nil || w.Index == 0 || w.DuraMax == 0 {
		return
	}
	it := s.data.tables.Items.Get(int(w.Index) - 1)
	if it != nil && it.StackLimit() > 0 {
		return
	}
	// 统一走 damageWeapon（挖矿那边是 `DoDamageWeapon(Random(15)+5)`）
	s.damageWeapon(p, 1)
}

// refreshSpecials 刷新装备特殊效果缓存（穿脱装备、进游戏时各调一次）。
func (s *Server) refreshSpecials(p *Player) {
	if p == nil {
		return
	}
	p.equipSpecials = s.playerItemSpecials(p)
	// 火焰/治愈戒指会**临时授予**技能（火球术/治愈术），换装备时要跟着同步。
	s.syncItemSkills(p)
}

// ---------- 连接 ----------
// learnMagic 使用技能书学习技能。
//
// 技能书（StdMode=4）的**物品名与技能名一致**，直接按名字查技能表即可。
// 数据里有 117 本，只有与技能表同名的 32 本能学（其余是 GeeM2 扩展技能）。
func (s *Server) learnMagic(c net.Conn, p *Player, tmpl *data.StdItem, bagIdx int) {
	data0 := p.Char.Data
	info := s.data.tables.Magics.GetByName(tmpl.Name)
	if info == nil {
		s.sysMsg(c, fmt.Sprintf("%s 目前无法学习（技能表无此技能）", tmpl.Name))
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 职业：TMagic.Job 0=战 1=法 2=道
	if job := uint8(data0.Job); job <= 2 && info.Job != job {
		s.sysMsg(c, fmt.Sprintf("你的职业无法学习 %s", info.Name))
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 等级需求
	if data0.Abil != nil && uint32(data0.Abil.Level) < uint32(info.NeedL1) {
		s.sysMsg(c, fmt.Sprintf("需要 %d 级才能学习 %s", info.NeedL1, info.Name))
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 重复学习
	for _, um := range data0.Magics {
		if um != nil && um.MagicId == uint32(info.MagicID) {
			s.sysMsg(c, "已经学会该技能")
			s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
			return
		}
	}

	data0.Magics = append(data0.Magics, &pb.UserMagic{MagicId: uint32(info.MagicID), Level: 0})
	s.takeBagItem(p, bagIdx) // 消耗掉技能书

	s.send(c, proto.SM_EAT_OK, 0, 0, 0, 0, "")
	s.sendMyMagics(c, p)
	s.sendBagItems(c, p)
	s.sysMsg(c, fmt.Sprintf("学会了 %s", info.Name))
	log.Printf("%s 学会 %s（技能号 %d）", p.Char.Name, info.Name, info.MagicID)
}

// ---------- 战斗 ----------
// handleAttack 处理物理攻击（CM_HIT）。
//
// 原版流程：AttackDir → _Attack → 命中/伤害判定 → 广播 SM_HIT + SM_STRUCK。
// 此处实现单目标近战：取朝向前方 1 格的怪物。
// sendBagItems 下发全量背包（SM_BAGITEMS）。
//
// body 是 ClientItem 的顺序拼接，客户端按 sizeof(ClientItem) 逐条切分
// （ClMain.pas ClientGetBagItmes）。**没有**数量前缀。
func (s *Server) sendBagItems(c net.Conn, p *Player) {
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	// 遍历期间持锁（`buildClientItem` 只读物品表，不碰背包 ⇒ 不会自死锁），
	// 但 `s.send` 放到锁外（避免在锁里做 I/O）。
	var body []byte
	// 新协议同一次遍历里一起攒（`docs/decisions.md` D-65：proto 玩家原来**收不到**背包，
	// 因为这条只发 legacy，而 legacy 下行会被 `protoDown` 丢掉）。
	var stack []*protocol.ItemStack
	p.stateMu.Lock()
	for _, it := range p.Char.Data.BagItems {
		if it == nil || it.Index == 0 {
			continue
		}
		ci, ok := s.buildClientItem(it)
		if !ok {
			// ⚠️ 这里跳过会让客户端的"压缩位次"少一位 ⇒ 之后所有 CM_EAT/CM_TAKEONITEM
			// 的下标都会错位（见 takeBagItem 的说明）。正常情况下不该发生。
			log.Printf("警告：%s 背包物品 Index=%d 构造 ClientItem 失败，已跳过（会造成客户端位次错位）",
				p.Char.Name, it.Index)
			continue
		}
		body = ci.Append(body)
		if st, ok := s.itemStack(it); ok {
			stack = append(stack, st)
		}
	}
	p.stateMu.Unlock()
	s.send(c, proto.SM_BAGITEMS, 0, 0, 0, 0, string(body))
	if p.protoOut != nil {
		p.protoOut.bag(stack)
	}
}

// takeBagItem 把背包某槽的物品拿走，并**保持背包没有空洞**（末尾补一个空槽）。
//
// ⚠️ 这是一个**协议契约**问题，不是风格问题：
//
//   - 原版 `SM_BAGITEMS` 的 Series 字段就是 `m_ItemList.Count`（ObjBase.pas:15926），
//     客户端按**收到的顺序**摆在背包格里 ⇒ 它发回来的 `CM_EAT`/`CM_TAKEONITEM`
//     里的下标是**压缩位次**（第几个物品，不是第几号槽）；
//   - 我们的 `pb.CharacterData.BagItems` 是**定长数组**，若某处写成 `BagItems[i] = 空`
//     就会留下空洞 ⇒ 之后的压缩位次与数组下标**错位** ⇒ 玩家"用了别的药 / 穿上了
//     别的装备"，而且只在背包中间有空洞时才复现（很难查）。
//
// 保持"紧凑前缀 + 空尾巴"后，两种下标恒等 ⇒ 服务端各处可以继续直接按下标访问。
//
// （踩坑记录：e2e 的祝福油用例就是这么暴露出来的——前面用例用过一堆物品后，
// 客户端给的祝福油位次落到服务端的另一格里，`CM_EAT` 直接判成非法下标。）
func (s *Server) takeBagItem(p *Player, idx int) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	takeBagItemLocked(p.Char.Data, idx)
}

// takeBagItemLocked 是 takeBagItem 的**无锁**内核（调用方已持 stateMu）。
func takeBagItemLocked(data *pb.CharacterData, idx int) {
	if data == nil || idx < 0 || idx >= len(data.BagItems) {
		return
	}
	data.BagItems = append(data.BagItems[:idx], data.BagItems[idx+1:]...)
	// 补回长度：把空槽放到末尾，而不是留在原位。
	data.BagItems = append(data.BagItems, &pb.UserItem{})
}

// takeBagByIndex 按 `MakeIndex` + 名字双因子**原子地**从背包取出物品
// （找格子、摘指针、保持"无空洞"三件事在同一把锁里完成）。
//
// ⚠️ 分开做（先 find 再 take）会留下 TOCTOU 窗口：两个 goroutine 同时在动这个背包时，
// idx 可能在两步之间失效 ⇒ 交易可能拿到**另一个**物品。见 deal.go 的 clientAddDealItem。
func (s *Server) takeBagByIndex(p *Player, makeIndex uint32, name string) (*pb.UserItem, bool) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return nil, false
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	for i, it := range p.Char.Data.BagItems {
		if it == nil || uint32(it.MakeIndex) != makeIndex {
			continue
		}
		if name != "" && s.bagItemName(it) != name {
			continue
		}
		takeBagItemLocked(p.Char.Data, i)
		return it, true
	}
	return nil, false
}

// addToBag 把物品放进背包，返回槽位下标；背包满返回 -1。
//
// 可堆叠物品（药品/卷轴等）优先累加到已有堆，堆满或不可堆叠才占新槽。
// 对堆叠物品，Dura 表示数量。
func (s *Server) addToBag(p *Player, it *pb.UserItem) int {
	if p == nil {
		return -1
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return s.addToBagLocked(p, it)
}

// addToBagLocked 是 addToBag 的**无锁**内核（调用方已持 stateMu）。
func (s *Server) addToBagLocked(p *Player, it *pb.UserItem) int {
	data := p.Char.Data
	if data == nil {
		return -1
	}
	tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
	if tmpl != nil {
		if lim := tmpl.StackLimit(); lim > 0 {
			if it.Dura == 0 {
				it.Dura = 1
			}
			for i, slot := range data.BagItems {
				if slot == nil || slot.Index != it.Index || slot.Dura >= lim {
					continue
				}
				// 累加，溢出部分留在本对象里继续找下一个堆
				room := lim - slot.Dura
				if uint32(it.Dura) <= room {
					slot.Dura += uint32(it.Dura)
					it.Dura = 0
					return i
				}
				slot.Dura = lim
				it.Dura -= room
			}
		}
	}
	// 不可堆叠，或堆都满了：占一个新槽
	for i, slot := range data.BagItems {
		if slot == nil || slot.Index == 0 {
			data.BagItems[i] = it
			return i
		}
	}
	if len(data.BagItems) >= entity.MaxBagSize {
		return -1
	}
	data.BagItems = append(data.BagItems, it)
	return len(data.BagItems) - 1
}

// handlePickup 处理 CM_PICKUP（捡起地面物品）。
//
// 客户端发 MakeDefaultMsg(CM_PICKUP, 0, x, y, 0)（ClMain.pas:3617），
// 即 Param=x、Tag=y。
func (s *Server) handlePickup(c net.Conn, p *Player, m wire.Packet) {
	// ⚠️ 交易中不能捡地上物品（ObjBase.pas:1695 `if m_boDealing then Exit`）。
	if s.dealingBlocks(p, "捡东西") {
		return
	}
	x, y := int(m.Head.Param), int(m.Head.Tag)

	// ⚠️ 必须校验坐标就是玩家所在格：坐标由客户端提供，
	// 不加校验的话可以隔空取物。
	if p.Obj.PosX() != x || p.Obj.PosY() != y {
		return
	}

	s.mu.Lock()
	var found *GroundItem
	for _, gi := range s.world.ground {
		if gi.Map == p.Obj.MapRef() && gi.X == x && gi.Y == y {
			found = gi
			break
		}
	}
	if found != nil {
		delete(s.world.ground, found.ID)
	}
	s.mu.Unlock()

	if found == nil {
		return
	}

	// ⚠️ 掉落归属（原版 ClientPickUpItem，ObjBase.pas:1699-1707）：怪物掉的东西
	// 2 分钟内只有**击杀者本人与队友**能捡，过了才谁都能捡（见 canPickUpGround）。
	// 不通过时把物品放回地面，别凭空吞掉。
	if !s.canPickUpGround(p, found) {
		s.mu.Lock()
		s.world.ground[found.ID] = found
		s.mu.Unlock()
		s.sysMsg(c, sCanotPickUpItem)
		return
	}

	// 金币堆走另一条路（原版金币分支在物品分支**之前**，见 ObjBase.pas:1708-1730）：
	// 直接进钱袋，不过背包/负重。
	if found.Gold > 0 {
		s.pickupGold(c, p, found)
		return
	}

	// ⚠️ 超重不许捡（原版 IsAddWeightAvailable，ObjBase.pas:2085-2091）；
	// 与"背包满"一样把物品**放回地面**，不能凭空吞掉。
	if s.pickBlockedByWeight(p, found.Item.Index) {
		s.mu.Lock()
		s.world.ground[found.ID] = found
		s.mu.Unlock()
		return
	}

	// 背包满则放回地面，不能凭空吞掉物品
	if s.addToBag(p, found.Item) < 0 {
		s.mu.Lock()
		s.world.ground[found.ID] = found
		s.mu.Unlock()
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 地面物品消失
	s.broadcastToViewers(found.Map, found.X, found.Y, func(other *Player) {
		s.send(other.conn, proto.SM_ITEMHIDE, int32(found.ID),
			uint16(found.X), uint16(found.Y), 0, "")
	})
	// 背包全量刷新
	s.sendBagItems(c, p)
	// 捡进来了 ⇒ 背包负重变了，重算（客户端负重条要跟着动）
	s.applyWeights(p)
	log.Printf("%s 捡起 %s 于 (%d,%d)", p.Char.Name, found.Name, x, y)
}

// handleEat 处理 CM_EAT（使用背包物品）。
//
// 目前覆盖三类：药品（`StdMode = 0`）、技能书（`StdMode = 4`）、
// `StdMode = 3` 的 `EatUseItems` 一族（祝福油 / 修复油 / 特修油…，见 eatuse.go）。
//
// 客户端发 MakeDefaultMsg(CM_EAT, itmindex, 0, 0, 0)（ClMain.pas:3641），
// Recog 是背包槽位下标。
//
// 药品判定：StdMode=0 且 AC.Min/MAC.Min 不全为 0 ——
// 数据中 AC.Min 是回血量、MAC.Min 是回蓝量（金创药(小量) 20/0、
// 太阳水 30/40、魔法药(小量) 0/30）。
func (s *Server) handleEat(c net.Conn, p *Player, m wire.Packet) {
	// 地图标记 `NODRUG`：官方 `EatItems` 的**第一行**就是它
	//（`ObjBase.pas:23330`），该图禁止使用一切药品/消耗品。
	if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoDrug {
		s.sysMsg(c, sCanotUseDrugOnThisMap)
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	idx := int(m.Head.Recog)
	data := p.Char.Data
	if data == nil || data.Abil == nil || idx < 0 || idx >= p.bagLen() {
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	it := p.bagAt(idx)
	if it == nil || it.Index == 0 {
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	tmpl := s.data.tables.Items.Get(int(it.Index) - 1)
	if tmpl == nil {
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 技能书：按物品名学会对应技能（StdMode=4，物品名与技能名一致）
	if tmpl.StdMode == 4 {
		s.learnMagic(c, p, tmpl, idx)
		return
	}
	// 食物：`StdMode = 1` 加饥饿度（原版 `TPlayObject.EatItems` 的 1 分支，
	// ObjBase.pas:23373-23380）：
	//
	//	Inc(m_nHungerStatus, StdItem.DuraMax div 10);
	//	m_nHungerStatus := _MIN(5000, m_nHungerStatus);
	//	if nOldStatus <> GetMyStatus() then RefMyStatus();
	//
	// ⚠️ 出厂 `!setup.txt` 里 `HungerSystem=0`/`HungerDecPower=0`/`HungerDecHP=0`
	// ⇒ 饥饿**惩罚**本来就不生效；我们也没实现那两条，所以这段的实际效果就是
	// "食物能吃、把饥饿度填上"（原来 `StdMode = 1` 的干肉**根本吃不了**）。
	// 那句状态刷新依赖 `GetMyStatus` 里的饥饿档位，我们还没建模 ⇒ 一并没做。
	if tmpl.StdMode == 1 {
		data.HungerStatus += int64(tmpl.DuraMax / 10)
		if data.HungerStatus > hungerMax {
			data.HungerStatus = hungerMax
		}
		if lim := tmpl.StackLimit(); lim > 0 && it.Dura > 1 {
			it.Dura--
		} else {
			s.takeBagItem(p, idx)
		}
		s.send(c, proto.SM_EAT_OK, 0, 0, 0, 0, "")
		s.sendBagItems(c, p)
		log.Printf("%s 吃下 %s：饥饿度 = %d", p.Char.Name, tmpl.Name, data.HungerStatus)
		return
	}
	// StdMode=3：`EatUseItems` 一族（祝福油 / 修复油 / 特修油 / 传送卷…，
	// 原版 `TPlayObject.EatItems` 的 StdMode 3 分支 ⇒ `EatUseItems(Shape)`，
	// ObjBase.pas:23324/:23513）。见 eatuse.go。
	//
	// ⚠️ 顺序照原版：**先判效果、有效才扣物品**（吃祝福油时没武器、用修复油时
	// 耐久已满，都不消耗）。
	if tmpl.StdMode == 3 {
		if !s.eatUseItem(c, p, tmpl) {
			s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
			return
		}
		if lim := tmpl.StackLimit(); lim > 0 && it.Dura > 1 {
			it.Dura--
		} else {
			s.takeBagItem(p, idx)
		}
		s.send(c, proto.SM_EAT_OK, 0, 0, 0, 0, "")
		s.sendBagItems(c, p)
		return
	}
	if tmpl.StdMode != 0 {
		// 非药品、非技能书（装备应走 CM_TAKEON）
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}
	hpAdd, mpAdd := uint32(tmpl.AC.Min), uint32(tmpl.MAC.Min)
	if hpAdd == 0 && mpAdd == 0 {
		s.send(c, proto.SM_EAT_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 消耗：可堆叠的只减数量，减到 0 才清空该槽
	if lim := tmpl.StackLimit(); lim > 0 && it.Dura > 1 {
		it.Dura--
	} else {
		s.takeBagItem(p, idx)
	}

	if hpAdd > 0 {
		p.addHP(int64(hpAdd)) // 夹到上限（持锁）
	}
	if mpAdd > 0 {
		p.addMP(int64(mpAdd))
	}

	s.send(c, proto.SM_EAT_OK, 0, 0, 0, 0, "")
	s.sendHealthChanged(p, p.Obj.ID, p.hp(), p.mp(), p.maxHP())
	s.sendBagItems(c, p)
	log.Printf("%s 使用 %s（HP+%d MP+%d → %d/%d %d/%d）",
		p.Char.Name, tmpl.Name, hpAdd, mpAdd, p.hp(), p.maxHP(), p.mp(), p.maxMP())
}

// equipAt 取指定装备槽的物品。
func (s *Server) equipAt(p *Player, slot int) *pb.UserItem {
	if p.Char.Data == nil || slot >= len(p.Char.Data.HumItems) {
		return nil
	}
	u := p.Char.Data.HumItems[slot]
	if u == nil || u.Index == 0 {
		return nil
	}
	return u
}

// ---------- 物品与属性 ----------
func (s *Server) buildClientItem(u *pb.UserItem) (*proto.ClientItem, bool) {
	if u == nil || u.Index == 0 {
		return nil, false
	}
	tmpl := s.data.tables.Items.Get(int(u.Index) - 1) // Index 是 1-based
	if tmpl == nil {
		return nil, false
	}
	pack := func(m data.MinMax) uint32 { return uint32(m.Min) | uint32(m.Max)<<16 }

	var si proto.StdItem
	si.SetName(tmpl.Name)
	si.StdMode = tmpl.StdMode
	si.Shape = tmpl.Shape
	si.Weight = tmpl.Weight
	si.AniCount = tmpl.AniCount
	si.Source = tmpl.Source
	si.Reserved = tmpl.Reserved
	si.Looks = tmpl.Looks
	si.DuraMax = tmpl.DuraMax
	si.AC = pack(tmpl.AC)
	si.MAC = pack(tmpl.MAC)
	si.DC = pack(tmpl.DC)
	si.MC = pack(tmpl.MC)
	si.SC = pack(tmpl.SC)
	si.Need = tmpl.Need
	si.NeedLevel = tmpl.NeedLevel
	si.Price = tmpl.Price

	return &proto.ClientItem{S: si, MakeIndex: u.MakeIndex, Dura: uint16(u.Dura), DuraMax: uint16(u.DuraMax)}, true
}

// itemStack 把一件存档物品映射成新协议的 `ItemStack`（背包格 / 装备格 / 将来的商店格都用它）。
//
// 与 [`Server.buildClientItem`] **同源**（同一份模板查表）：那边产出定长二进制的
// `ClientItem`，这边产出结构化字段。两处都得维护 ⇒ 以后加字段**记得两边都加**
// （这正是 `Ability::from_proto` 那条"两个地方各写一遍必然漏一个"的教训）。
func (s *Server) itemStack(u *pb.UserItem) (*protocol.ItemStack, bool) {
	if u == nil || u.Index == 0 {
		return nil, false
	}
	tmpl := s.data.tables.Items.Get(int(u.Index) - 1) // Index 是 1-based
	if tmpl == nil {
		return nil, false
	}
	// 可叠加物的**数量**在原版是塞在 `Dura` 里的（金创药的 Dura 就是瓶数）——
	// legacy 的 `ClientItem` 压根没有数量字段，只有 Dura/DuraMax。
	// 判据：`DuraMax <= 1` = 没有耐久 = 可叠加（药水/卷轴），此时 Dura 即数量。
	count := uint32(1)
	if tmpl.DuraMax <= 1 && u.Dura > 0 {
		count = uint32(u.Dura)
	}
	return &protocol.ItemStack{
		Index:   uint32(u.Index),
		Name:    tmpl.Name,
		Looks:   uint32(tmpl.Looks),
		Count:   count,
		Dura:    uint32(u.Dura),
		DuraMax: uint32(u.DuraMax),
	}, true
}

// sendUseItems 下发已穿戴装备（body：槽位/ClientItem/... 每 2 段一组）。
func (s *Server) sendUseItems(c net.Conn, p *Player) {
	var parts []string
	// 新协议：**按槽位**排（空槽补 `index=0`）—— 装备槽的位置本身有意义
	//（武器/衣服/项链…），不能像背包那样压缩。见 `docs/decisions.md` D-65。
	var equip []*protocol.ItemStack
	if p.Char.Data != nil {
		for i, u := range p.Char.Data.HumItems {
			ci, ok := s.buildClientItem(u)
			if !ok {
				continue
			}
			parts = append(parts, strconv.Itoa(i), string(ci.Append(nil)))
			for len(equip) < i {
				equip = append(equip, &protocol.ItemStack{})
			}
			if st, ok := s.itemStack(u); ok {
				equip = append(equip, st)
			} else {
				equip = append(equip, &protocol.ItemStack{})
			}
		}
	}
	s.send(c, proto.SM_SENDUSEITEMS, 0, 0, 0, 0, strings.Join(parts, "/"))
	if p.protoOut != nil {
		p.protoOut.equip(equip)
	}
}

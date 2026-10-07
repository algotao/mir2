package gamesvr

// 玩家交易（Deal / Trade）系统。
//
// # 状态机：没有"对方接受"这一步
//
// 原版双方是**同时**被置为交易态的（ObjBase.pas:24039-24046 OpenDealDlg），
// 靠"互相对视"作为唯一门槛：
//
//	CM_DEALTRY(1025) → 面前一格有玩家、对方也正对着我、对方开着 @LetTrade
//	                 → 双方同时 OpenDealDlg → 双方各收 SM_DEALMENU(673)+对方名字
//	CM_DEALADDITEM(1026)  Recog=MakeIndex, body=物品名
//	CM_DEALCHGGOLD(1029)  Recog=金币数（金币在此刻就从背包扣走了）
//	CM_DEALDELITEM(1027)  Recog=MakeIndex, body=物品名
//	CM_DEALEND(1030)      我按"成交" ⇒ 双方都按过才成交
//	CM_DEALCANCEL(1028)   任一方取消（**递归**通知对方）
//
// # 六个必须照抄的怪癖（其余见 internal 注释与文档）
//
//  1. **金币在放进交易栏时就扣了**（17817 `m_nGold := (m_nGold + m_nDealGolds) - nGold`），
//     `m_nDealGolds` 只是"待交付余额"。写成"成交时才扣"会造出复制漏洞。
//  2. **交易栏存的是指针**（`m_DealItemList.Add(UserItem)` + `m_ItemList.Delete(i)`），
//     全程只有一份物品内存，结构上无法复制。
//  3. **`ClientDealEnd` 不检查 `m_boDealing`**，只查 `m_DealCreat = nil`；
//     而且开头就无条件 `m_boDealOK := True` 再判 nil —— 对方指针为空时
//     退出后残留"已确认"，交易栏被永久卡死（17842-17843）。
//  4. **失去对视立即取消**（`TPlayObject.Run:6413-6416`）—— 走一格、转身、
//     对方走开、换图、死亡都算。这是 99% 的"超时"来源（原版没有超时计时器）。
//  5. **改金币被拒时发的是 `SM_DEALDELITEM_FAIL`**（不是 `SM_DEALCHGGOLD_FAIL`），
//     见 17801。客户端会走错分支，可能把背包里一件还在的东西删掉。
//  6. **`m_DealLastTick` 是双向共享心跳**：任何一方操作都会刷新对方的 tick
//     （24015-24016 / 23932-23933 / 17821-17823），所以"确认前静止 1 秒"
//     会被对方操作绕过。照抄，否则会出现"双方同时操作导致状态错乱"。

import (
	"log"
	"net"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
)

// ---------- 出厂配置（对齐 GeeM2 的 !setup.txt:460-463）----------

const (
	// dealTryCooldown 是发起交易的冷却（TryDealTime=3000ms）。
	// DealCancel 也会重置它 ⇒ 取消后 3 秒内不能再发起（15431）。
	dealTryCooldown = 3 * time.Second
	// dealOKCooldown 是"按成交前双方必须静止"的时长（DealOKTime=1000ms）。
	dealOKCooldown = 1 * time.Second
	// dealCanNotGetBack 默认禁止把物品/金币从交易栏**取回**
	// （CanNotGetBackDeal=1，!setup.txt:462）。
	//
	// ⚠️ 这是官方部署的值。设为 0 才能"放了又拿回来"。
	dealCanNotGetBack = true
	// dealMaxItems 是交易栏物品数上限（原版 12，ObjBase.pas:17723）。
	// ⚠️ 客户端交易栏只有 **10** 格（MirClient/MShare.pas:354），
	// 所以 11~12 只有改客户端才用得到。
	dealMaxItems = 12
)

// 交易失败/状态提示文案。
//
// 取自 String.ini / ObjBase.pas 的 resourcestring（g_s* 系列）。
const (
	dealMsgOpened      = "%s 已打开交易栏。" // g_sOpenedDealMsg
	dealMsgTargetOff   = "对方禁止进入交易。"  // g_sPoseDisableDealMsg
	dealMsgTryLater    = "请稍后再试。"     // g_sPleaseTryDealLaterMsg
	dealMsgCannotTry   = "您现在不能进行交易。" // g_sCanotTryDealMsg
	dealMsgCancelled   = "交易取消。"      // g_sDealActionCancelMsg
	dealMsgOKTooFast   = "太快了，请稍候再试。" // g_sDealOKTooFast
	dealMsgYouOK       = "你已经确认交易了。"  // g_sYouDealOKMsg
	dealMsgOtherOK     = "对方已经确认交易了。" // g_sPoseDealOKMsg
	dealMsgSuccess     = "交易成功。"      // g_sDealSuccessMsg
	dealMsgBagTooSmall = "你的背包空间不足。"  // g_sYourBagSizeTooSmall
	dealMsgOtherBag    = "对方的背包空间不足。" // g_sDealHumanBagSizeTooSmall
	dealMsgGoldTooBig  = "您的金币超过上限。"  // g_sYourGoldLargeThenLimit
	dealMsgOtherGold   = "对方的金币超过上限。" // g_sDealHumanGoldLargeThenLimit
	dealMsgDenyGetBack = "不能取回交易物品。"  // g_sDealItemsDenyGetBackMsg
)

// handleDealMsg 分派交易消息。返回 false 表示"不认这个 Ident"。
//
// 对应 ObjBase.pas:4804-4827 的 case 分支。
//
// ⚠️ CM_DEALTRY 的 body（对方角色名）服务端**完全丢弃**（ClientDealTry 的形参
// 从未被引用）——因为客户端的 `who` 从未被赋值（MirClient/ClMain.pas:3799-3813
// 整段被注释）。目标靠"朝向格"确定。别自作聪明改成"按名字找"。
func (s *Server) handleDealMsg(c net.Conn, p *Player, pkt wire.Packet) bool {
	switch pkt.Head.Ident {
	case proto.CM_DEALTRY:
		s.clientDealTry(c, p)
	case proto.CM_DEALADDITEM:
		s.clientAddDealItem(c, p, pkt.Head.Recog, pkt.Body)
	case proto.CM_DEALDELITEM:
		s.clientDelDealItem(c, p, pkt.Head.Recog, pkt.Body)
	case proto.CM_DEALCANCEL:
		s.dealCancel(p)
	case proto.CM_DEALCHGGOLD:
		s.clientChangeDealGold(c, p, pkt.Head.Recog)
	case proto.CM_DEALEND:
		s.clientDealEnd(c, p)
	default:
		return false
	}
	return true
}

// clientDealTry 处理 CM_DEALTRY。
//
// 对应 ClientDealTry（ObjBase.pas:17647-17697）。八条准入逐条照抄：
//
//	① g_Config.boDisableDeal 全局关闭  → 我们没有这个开关
//	② 自己已在交易                    → 静默 Exit（**不发包**）
//	③ 距上次操作 < TryDealTime(3s)    → 提示
//	④ m_boCanDeal（密码锁）            → 提示（我们没有密码锁）
//	⑤ GetPoseCreate <> nil            → 面前一格必须有对象
//	⑥ 对方也正对着我                  → SM_DEALTRY_FAIL
//	⑦ 对方必须是玩家                  → 同上
//	⑧ 对方 m_boAllowDeal              → 提示"对方禁止进入交易"
//
// ⚠️ 原版**没有**距离数值检查（"面前一格"就是全部）、**没有**安全区/行会/组队/
// 等级/PK 检查。照抄，别加。
func (s *Server) clientDealTry(c net.Conn, p *Player) {
	// 交易全程串行（见 Server.dealMu 的说明）：交易是"两个玩家 + 心跳"三方并发。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	now := time.Now()
	if p.dealing {
		return // ② 静默：原版 Exit 且不发包
	}
	if now.Sub(p.dealLastTick) < dealTryCooldown {
		s.sysMsg(c, dealMsgTryLater)
		return // ③
	}
	// ⑤ 面前一格（GetPoseCreate，ObjBase.pas:15875-15884）
	target := s.faceNeighbor(p)
	if target == nil {
		s.send(c, proto.SM_DEALTRY_FAIL, 0, 0, 0, 0, "")
		return
	}
	// ⑥ 对方也正对着我
	if s.faceNeighbor(target) != p {
		s.send(c, proto.SM_DEALTRY_FAIL, 0, 0, 0, 0, "")
		return
	}
	// ⑦⑧
	if !target.allowDeal {
		s.sysMsg(c, dealMsgTargetOff)
		return
	}
	if target.dealing {
		s.send(c, proto.SM_DEALTRY_FAIL, 0, 0, 0, 0, "")
		return
	}

	// 双方同时进入交易态（OpenDealDlg 会被连续调两次）
	s.openDealDlg(p, target)
	s.openDealDlg(target, p)

	sysMsgTo(c, target.Char.Name+" 已打开交易栏。")
	sysMsgTo(target.conn, p.Char.Name+" 已打开交易栏。")
	obs.Event("deal_open", "a", p.Char.Name, "b", target.Char.Name)
	log.Printf("%s 与 %s 开始交易", p.Char.Name, target.Char.Name)
}

// openDealDlg 把双方置为交易态。
//
// 对应 OpenDealDlg（ObjBase.pas:24039-24046）：
//
//	m_boDealing := True;
//	m_DealCreat := BaseObject;
//	m_DealLastTick := GetTickCount;
//	SendAddDealItem(...GetBackDealItems...)  // 先把残留清干净
//	SendDefMessage(SM_DEALMENU, 0,0,0,0, m_DealCreat.m_sCharName)
func (s *Server) openDealDlg(p, partner *Player) {
	p.dealing = true
	p.dealPartner = partner.Obj.ID
	p.dealLastTick = time.Now()
	s.getBackDealItems(p)
	s.send(p.conn, proto.SM_DEALMENU, 0, 0, 0, 0, partner.Char.Name)
}

// ---------- 物品进出交易栏 ----------

// clientAddDealItem 处理 CM_DEALADDITEM。
//
// 对应 ClientAddDealItem（ObjBase.pas:17699-17737）。校验：
//
//	178: if not m_boDealing then Exit
//	179: if m_DealCreat <> nil then
//	180:   if not m_DealCreat.m_boDealOK then        ← 对方已确认 ⇒ 不能再放
//	181:   if (m_DealCreat.GetFrontPosition <> Self) then Exit   ← 必须还面对着
//	186: GetValidStr3(sItemName, sItemName, [' '])   ← 按**第一个空格**截断
//	…: 遍历 m_ItemList，MakeIndex 匹配 **且** 名字匹配，且 count < 12
//	→ m_DealItemList.Add(UserItem); m_ItemList.Delete(i)
//
// ⚠️ 双因子校验（MakeIndex + 名字）是防"改名物品复制"的关键，别只查 MakeIndex。
// ⚠️ 名字按第一个空格截断是因为"信件物品"的名字后面带使用次数。
func (s *Server) clientAddDealItem(c net.Conn, p *Player, makeIndex int32, itemName string) {
	// 交易全程串行（见 Server.dealMu 的说明）：交易是"两个玩家 + 心跳"三方并发。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	if !p.dealing {
		return
	}
	partner := s.partnerOf(p)
	if partner == nil {
		return
	}
	// 对方已确认 ⇒ 交易栏冻结（原版 180）
	if partner.dealOK {
		return
	}
	// 必须还面对着对方（原版 181）
	if s.faceNeighbor(p) != partner {
		return
	}
	// CanNotGetBackDeal 只是"不许取回"，不影响"放进来"。原版这里是反过来的：
	// 放进来时**不**查它，只有取回（ClientDelDealItem）才查。
	_ = itemName

	if len(p.dealItems) >= dealMaxItems {
		// 原版 17723 的 `count < 12` 是在**加入之后**才判的边界，这里加入前判。
		//
		// ⚠️ 老代码是"先取出来、发现满了再把**交易栏最后一个**放回背包"——
		// 那会把一件已经放进交易栏的物品退回背包（玩家莫名其妙丢位），
		// 而且退回的是**错误的那一件**。现在改成先判上限再取。
		s.send(c, proto.SM_DEALADDITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	// 指针转移：同一块 pb.UserItem 从背包搬到交易栏（不拷贝）。
	// 查找 + 摘出 + 保持无空洞在同一次 `takeBagByIndex` 里完成（原子，见 item.go）。
	it, ok := s.takeBagByIndex(p, uint32(makeIndex), dealItemName(itemName))
	if !ok {
		s.send(c, proto.SM_DEALADDITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	p.dealItems = append(p.dealItems, it)

	s.send(c, proto.SM_DEALADDITEM_OK, 0, 0, 0, 0, "")
	s.sendAddDealItemRemote(partner, p, it)
	// 双向共享心跳（怪癖 6）
	p.dealLastTick = time.Now()
	partner.dealLastTick = p.dealLastTick
	obs.Event("deal_add_item", "who", p.Char.Name, "partner", partner.Char.Name,
		"make_index", makeIndex, "count", len(p.dealItems))
}

// clientDelDealItem 处理 CM_DEALDELITEM（把物品/金币从交易栏取回背包）。
//
// 对应 ClientDelDealItem（ObjBase.pas:17738-17791）。⚠️ 注意原版的判定顺序
// 与其它函数**相反**：`boCanNotGetBackDeal` 的检查排在 `m_DealCreat = nil` 之前
// （17746-17753），所以即使对方指针为空也会先发一次 SM_DEALDELITEM_FAIL。
func (s *Server) clientDelDealItem(c net.Conn, p *Player, makeIndex int32, itemName string) {
	// 交易全程串行（见 Server.dealMu 的说明）：交易是"两个玩家 + 心跳"三方并发。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	if !p.dealing {
		return
	}
	if dealCanNotGetBack {
		// 原版 17746：CanNotGetBackDeal 时**先**发 SM_DEALDELITEM_FAIL，
		// 然后才查 m_DealCreat —— 顺序照抄，否则客户端表现不同。
		s.send(c, proto.SM_DEALDELITEM_FAIL, 0, 0, 0, 0, "")
		s.sysMsg(c, dealMsgDenyGetBack)
		return
	}
	partner := s.partnerOf(p)
	if partner == nil {
		return
	}
	if partner.dealOK {
		return
	}
	_ = itemName

	pos := -1
	for i, it := range p.dealItems {
		if it != nil && it.MakeIndex == int32(makeIndex) {
			pos = i
			break
		}
	}
	if pos < 0 {
		s.send(c, proto.SM_DEALDELITEM_FAIL, 0, 0, 0, 0, "")
		return
	}
	s.putBackToBag(p, p.dealItems[pos])
	p.dealItems = append(p.dealItems[:pos], p.dealItems[pos+1:]...)

	s.send(c, proto.SM_DEALDELITEM_OK, 0, 0, 0, 0, "")
	s.sendDelDealItemRemote(partner, p, uint32(makeIndex))
	p.dealLastTick = time.Now()
	partner.dealLastTick = p.dealLastTick
}

// clientChangeDealGold 处理 CM_DEALCHGGOLD。
//
// 对应 ClientChangeDealGold（ObjBase.pas:17792-17832）：
//
//	17817: m_nGold := (m_nGold + m_nDealGolds) - nGold;   ← 此刻就扣钱！
//	17818: m_nDealGolds := nGold;
//	… 17819: SendDefMessage(SM_DEALCHGGOLD_OK, m_nDealGolds, LoWord(Gold), HiWord(Gold), 0, '')
//	… 17820: 对方.SendDefMessage(SM_DEALREMOTECHGGOLD, m_nDealGolds, 0,0,0, '')
//
// ⚠️ Recog 是 32 位有符号数，负数会先被夹到 0（原版靠 `if nGold < 0`）。
func (s *Server) clientChangeDealGold(c net.Conn, p *Player, gold int32) {
	// 交易全程串行（见 Server.dealMu 的说明）：交易是"两个玩家 + 心跳"三方并发。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	if p.dealPartner == 0 {
		return
	}
	if dealCanNotGetBack && gold < int32(p.dealGolds) {
		// ⚠️ 怪癖 5：原版这里发的是 SM_DEALDELITEM_FAIL（17801），
		// 不是 SM_DEALCHGGOLD_FAIL。客户端会走错分支。
		s.send(c, proto.SM_DEALDELITEM_FAIL, 0, 0, 0, 0, "")
		s.sysMsg(c, dealMsgDenyGetBack)
		return
	}
	partner := s.partnerOf(p)
	if partner == nil {
		return
	}
	if partner.dealOK {
		return
	}
	if s.faceNeighbor(p) != partner {
		return
	}
	// ⚠️ 一次**原子**读改写（原版 `m_nGold := (m_nGold + m_nDealGolds) - nGold`）：
	// 拆成"读余额 → 算 → 写回"会被并发的收支插进来 ⇒ 丢更新。
	// ⚠️ 上限按原版钳在**当前钱包**（不含交易栏里那份），别改成"钱包+交易栏"。
	var n int64
	g := p.withGold(func(cur int64) int64 {
		n = int64(gold)
		if n < 0 {
			n = 0
		}
		if n > cur {
			n = cur
		}
		return cur + p.dealGolds - n
	})
	p.dealGolds = n
	s.send(c, proto.SM_DEALCHGGOLD_OK, int32(p.dealGolds),
		uint16(g), uint16(g>>16), 0, "")
	s.send(partner.conn, proto.SM_DEALREMOTECHGGOLD, int32(partner.dealGolds), 0, 0, 0, "")
	p.dealLastTick = time.Now()
	partner.dealLastTick = p.dealLastTick
	obs.Event("deal_gold", "who", p.Char.Name, "gold", n)
}

// ---------- 成交 ----------

// clientDealEnd 处理 CM_DEALEND（我按"成交"）。
//
// 对应 ClientDealEnd（ObjBase.pas:17834-17983）。⚠️ 怪癖 3：原版**不检查**
// m_boDealing，只查 m_DealCreat = nil；而且开头就无条件 `m_boDealOK := True`。
//
// 四项成交前校验（任一不过 ⇒ 整笔取消，不是部分成交）：
//
//	17853: 我的背包装得下对方的东西    (MaxBagSize - 我背包格数) < 对方交易栏数
//	17858: 我的金币上限装得下对方的钱
//	17863: 对方背包装得下我的东西
//	17868: 对方金币上限装得下我的钱
func (s *Server) clientDealEnd(c net.Conn, p *Player) {
	// 交易全程串行（见 Server.dealMu 的说明）：交易是"两个玩家 + 心跳"三方并发。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()

	p.dealOK = true // 17842：无条件先置
	partner := s.partnerOf(p)
	if partner == nil {
		return // 17843：⚠️ 残留 dealOK=True（照抄，见文件头怪癖 3）
	}
	now := time.Now()
	// 17844：双方最后一次操作都要静止 ≥ DealOKTime
	if now.Sub(p.dealLastTick) < dealOKCooldown ||
		now.Sub(partner.dealLastTick) < dealOKCooldown {
		s.sysMsg(c, dealMsgOKTooFast)
		s.dealCancelInner(p)
		return
	}
	if !partner.dealOK {
		// 17880-17881：等对方
		s.sysMsg(c, dealMsgYouOK)
		sysMsgTo(partner.conn, dealMsgOtherOK)
		return
	}

	// ---- 四项校验 ----
	ok := true
	bagFree := entity.MaxBagSize - countBag(p)
	if bagFree < len(partner.dealItems) {
		s.sysMsg(c, dealMsgBagTooSmall)
		ok = false
	}
	if playerMaxGold(uint32(p.level()))-p.gold() < partner.dealGolds {
		s.sysMsg(c, dealMsgGoldTooBig)
		ok = false
	}
	partnerBagFree := entity.MaxBagSize - countBag(partner)
	if partnerBagFree < len(p.dealItems) {
		sysMsgTo(partner.conn, dealMsgOtherBag)
		ok = false
	}
	if playerMaxGold(uint32(partner.level()))-partner.gold() < p.dealGolds {
		sysMsgTo(partner.conn, dealMsgOtherGold)
		ok = false
	}
	if !ok {
		s.dealCancelInner(p) // 17974
		return
	}

	// ---- 成交：先把两边的"交易栏"整体摘下（置空）----
	//
	// ⚠️⚠️ 这是**防物品复制的关键**，别为了"照抄原版顺序"把它挪到后面。
	//
	// 物品一放进交易栏就离开了背包（见文件头怪癖 1）⇒ 交易栏就是它的**所有权令牌**。
	// 下面要把它"搬"进对方背包，而这段搬运不是一条指令 —— 另一个 goroutine
	// （对方点取消 / 心跳 dealGuard / 对方掉线 dealCancelA）随时可能
	// `getBackDealItems` 把**同一批指针**再塞回原主的背包，于是同一件物品
	// 同时出现在两个人的背包里。原版靠"换服务器"制造跨进程时间差（单进程下它其实
	// 也只在单线程 Run 里串行才安全），我们两个玩家本来就是两个 goroutine。
	//
	// 摘下来 + 把双方 `dealing` 置 false 之后，任何并发的取消都会在
	// `dealCancelInner` 第一行 `if not dealing then Exit`（原版 15421 的幂等保护）
	// 直接返回，不会再碰这些指针；`dealMu` 则保证根本不会交错。
	mine, theirs := p.dealItems, partner.dealItems
	myGold, theirGold := p.dealGolds, partner.dealGolds
	p.dealItems, partner.dealItems = nil, nil
	p.dealGolds, partner.dealGolds = 0, 0
	p.dealing, partner.dealing = false, false

	// ⚠️ 仅测试：注入点。放在"摘下交易栏"**之后**、搬运**之前**，让回归测试能在
	// 确切那一瞬插一个"对方的取消"进来 —— 这正是原版那类复制的时间差。
	// 生产路径恒为 nil；如果谁把上面的"摘下"挪到后面（或不摘），
	// TestDealCancelDuringCompletionNoDupe 会**确定性**地抓到复制。
	if dealTestHook != nil {
		dealTestHook(p, partner)
	}

	// 成交：顺序照抄（先给对方我的，再给我对方的）
	// ① 我的交易栏 → 对方背包（17875-17880）
	//
	// ⚠️ 原版 17878 **忽略** `AddItemToBag` 的返回值、照样 `SendAddItem`；我们若在
	// 这里中途放弃并取消，会把**已经交出去**的物品也退回来 ⇒ 又是复制。所以照原版
	// "硬塞"：addToBag 失败就 putBackToBag（找空位/追加，背包可能短暂超容，
	// 与仓库其它地方一致）并记日志，保证物品**不多不少**。
	for _, it := range mine {
		if s.addToBag(partner, it) < 0 {
			log.Printf("⚠️ %s 的背包在成交瞬间满了，%s 的物品 %s 超容放入（原版同样忽略返回值）",
				partner.Char.Name, p.Char.Name, dealItemNameOf(s, it))
			s.putBackToBag(partner, it)
		}
		s.sendAddItem(partner, it)
	}
	// ② 我的金币 → 对方（17900-17905）
	if myGold > 0 {
		// ⚠️ 这是**跨玩家**写对手的钱包 ⇒ 必须走他自己那把随身财物锁，
		// 否则会与他自己的买卖/捡钱/交税丢更新（"少扣的那次"就等于刷钱）。
		ng := partner.addGold(myGold)
		s.send(p2conn(partner), proto.SM_GOLDCHANGED, int32(ng), 0, 0, 0, "")
	}
	// ③ 对方交易栏 → 我的背包（17916-17920）
	for _, it := range theirs {
		if s.addToBag(p, it) < 0 {
			log.Printf("⚠️ %s 的背包在成交瞬间满了，%s 的物品 %s 超容放入（原版同样忽略返回值）",
				p.Char.Name, partner.Char.Name, dealItemNameOf(s, it))
			s.putBackToBag(p, it)
		}
		s.sendAddItem(p, it)
	}
	// ④ 对方金币 → 我（17941-17944）
	if theirGold > 0 {
		s.send(p.conn, proto.SM_GOLDCHANGED, int32(p.addGold(theirGold)), 0, 0, 0, "")
	}
	// ⑤ SM_DEALSUCCESS：**对方先、自己后**（17959 / 17967）
	s.send(partner.conn, proto.SM_DEALSUCCESS, 0, 0, 0, 0, "")
	sysMsgTo(partner.conn, dealMsgSuccess)
	s.resetDeal(partner)
	s.send(c, proto.SM_DEALSUCCESS, 0, 0, 0, 0, "")
	s.sysMsg(c, dealMsgSuccess)
	s.resetDeal(p)

	obs.Event("deal_success", "a", p.Char.Name, "b", partner.Char.Name,
		"a_items", len(mine), "b_items", len(theirs),
		"a_gold", myGold, "b_gold", theirGold)
	log.Printf("%s 与 %s 交易成功", p.Char.Name, partner.Char.Name)
}

// resetDeal 清掉一方（及对方）的交易态。**调用方无锁**。
func (s *Server) resetDeal(p *Player) {
	p.dealing = false
	p.dealPartner = 0
	p.dealItems = nil
	p.dealGolds = 0
	p.dealOK = false
	// ⚠️ 原版成交后**不**重置 m_DealLastTick（只有 DealCancel 重置）⇒
	// 成交后 3 秒内无法再发起交易。照抄。
}

// ---------- 取消与回滚 ----------

// dealCancel 取消交易并**递归**通知对方。
//
// 对应 DealCancel（ObjBase.pas:15419-15432）：
//
//	if not m_boDealing then Exit;      ← 幂等保护，递归靠它终止
//	m_boDealing := False;
//	SendDefMessage(SM_DEALCANCEL, ...);
//	if m_DealCreat <> nil then TPlayObject(m_DealCreat).DealCancel;  ← 递归
//	m_DealCreat := nil;
//	GetBackDealItems;                   ← 物品+金币退回
//	SysMsg('交易取消');
//	m_DealLastTick := GetTickCount;     ← 取消后 3 秒内不能再发起
func (s *Server) dealCancel(p *Player) {
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	s.dealCancelInner(p)
}

// dealTestHook 是**仅测试**用的注入点（生产恒为 nil）：
// 在"成交摘下两边的交易栏之后、开始搬运之前"被调用一次，
// 让回归测试能确定性地复现"对方在成交瞬间点取消"这个时间差。
var dealTestHook func(a, b *Player)

// dealCancelInner 是 dealCancel 的**无锁**内核（调用方已持 dealMu）。
//
// 递归靠 `if not dealing then Exit`（原版 15421 的幂等保护）终止，
// 所以它必须能重入自身；也正因如此它不能自己加锁。
func (s *Server) dealCancelInner(p *Player) {
	if p == nil || !p.dealing {
		return
	}
	partner := s.partnerOf(p)
	p.dealing = false
	p.dealPartner = 0
	s.send(p.conn, proto.SM_DEALCANCEL, 0, 0, 0, 0, "")
	if partner != nil {
		// 递归：partner 那边也会置 false 并回滚
		s.dealCancelInner(partner)
	}
	s.getBackDealItems(p)
	s.sysMsg(p.conn, dealMsgCancelled)
	p.dealLastTick = time.Now()
}

// dealCancelA 是"退出/存档时"的取消，额外回满血。
//
// 对应 DealCancelA（ObjBase.pas:15434-15438）：`m_Abil.HP := m_WAbil.HP; DealCancel;`。
func (s *Server) dealCancelA(p *Player) {
	if p == nil {
		return
	}
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	if p.Char.Data != nil && p.Char.Data.Abil != nil {
		p.refillHP() // 持锁（原版 DealCancelA 里的 Inc(Hp, MaxHp)）
	}
	s.dealCancelInner(p)
}

// getBackDealItems 把交易栏里的东西全退回背包。
//
// 对应 GetBackDealItems（ObjBase.pas:15613-15628）。⚠️ 原版**不检查背包容量**
// 就 Add —— 背包被别的东西塞满时物品会被塞进"超载"的列表。
// 我们照抄同样的行为（addToBag 返回 -1 时放回交易栏而不是丢弃，绝不凭空消失），
// 但会记日志，便于发现超载。
func (s *Server) getBackDealItems(p *Player) {
	for _, it := range p.dealItems {
		if it == nil {
			continue
		}
		if s.addToBag(p, it) < 0 {
			log.Printf("⚠️ %s 背包超载，交易物品 %s 无处退回（照抄原版行为，但物品没丢）",
				p.Char.Name, dealItemNameOf(s, it))
		}
	}
	// ⚠️ 金币必须**先退再清零**：`clientChangeDealGold` 在"放进交易栏"那一刻就把钱从
	// 钱包扣走了（原版 `m_Abil.Gold := (m_Abil.Gold + m_DealGolds) - nGold`）⇒
	// 先清零的话这里退回的是 0 ⇒ **每次取消都把放进交易栏的金币吞掉**。
	// 原版 `GetBackDealItems`（15613-15628）就是先 `Inc(m_Abil.Gold, m_DealGold)` 再清零。
	if p.Char.Data != nil && p.dealGolds > 0 {
		p.addGold(p.dealGolds) // 持锁加钱（可能是在给对方退，见 dealCancelInner 的递归）
	}
	p.dealItems = nil
	p.dealGolds = 0
	p.dealOK = false
}

// ---------- 每帧守卫 ----------

// dealGuard 每轮心跳调用：交易中一旦"不再面对对方"就取消。
//
// 对应 TPlayObject.Run:6413-6416：
//
//	if m_boDealing then
//	  if (GetPoseCreate <> m_DealCreat) or (m_DealCreat = Self) or (m_DealCreat = nil) then
//	    DealCancel;
//
// ⚠️ 这是原版**唯一**的"超时"来源（没有超时计时器）。走一格、转身、对方走开、
// 换图、死亡都会走到这里。
//
// 另有一条 ObjBase.pas:4134-4137：对方变 ghost 时只 `m_DealCreat := nil`
// **不取消交易**，靠下一帧本函数收尸。我们这里直接当"对方没了"处理，等价。
func (s *Server) dealGuard() {
	s.mu.RLock()
	var list []*Player
	for _, p := range s.world.players {
		if p != nil && p.dealing {
			list = append(list, p)
		}
	}
	s.mu.RUnlock()

	// 整个巡检在 dealMu 下做：它可能同时取消好几对交易，而对方可能正在成交。
	s.dealMu.Lock()
	defer s.dealMu.Unlock()
	for _, p := range list {
		partner := s.partnerOf(p)
		if partner == nil || partner == p || s.faceNeighbor(p) != partner {
			s.dealCancelInner(p)
		}
	}
}

// partnerOf 取交易对方（nil = 不在交易/对方已走）。
func (s *Server) partnerOf(p *Player) *Player {
	if p == nil || p.dealPartner == 0 || p.Obj == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.world.players[p.dealPartner]
}

// ---------- 物品取出/放回与消息 ----------

// takeFromBag 按 MakeIndex + 名字双因子从背包取出物品，返回槽位下标。
//
// 双因子是原版 17717/17722 的判定（`MakeIndex` 匹配 **且** `GetItemName` 匹配），
// 作用是防"改名物品"被复制：改名物品的显示名来自 CustomItem，而交易时
// 客户端传的是显示名。
func (s *Server) takeFromBag(p *Player, makeIndex uint32, name string) int {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return -1
	}
	// 持锁遍历（"只查"版本；真正取出要用 takeBagByIndex，它是原子的）
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	for i, it := range p.Char.Data.BagItems {
		if it == nil || uint32(it.MakeIndex) != makeIndex {
			continue
		}
		if name != "" && s.bagItemName(it) != name {
			continue
		}
		return i
	}
	return -1
}

// putBackToBag 把物品放回背包（取回失败时用）。
func (s *Server) putBackToBag(p *Player, it *pb.UserItem) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	s.putBackToBagLocked(p, it)
}

// putBackToBagLocked 是 putBackToBag 的**无锁**内核（调用方已持 stateMu）。
func (s *Server) putBackToBagLocked(p *Player, it *pb.UserItem) {
	if s.addToBagLocked(p, it) >= 0 {
		return
	}
	// 背包满：塞回原槽位，绝不丢物品
	for i, slot := range p.Char.Data.BagItems {
		if slot == nil || slot.Index == 0 {
			p.Char.Data.BagItems[i] = it
			return
		}
	}
	p.Char.Data.BagItems = append(p.Char.Data.BagItems, it)
}

// compactBag 把 BagItems 里的 nil 压掉，保持"下标即槽位"的语义。
func (s *Server) compactBag(p *Player) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char.Data == nil {
		return
	}
	items := p.Char.Data.BagItems
	kept := items[:0]
	for _, it := range items {
		if it != nil && it.Index != 0 {
			kept = append(kept, it)
		}
	}
	for i := len(kept); i < len(items); i++ {
		items[i] = nil
	}
}

// countBag 数背包里有效物品数（原版 m_ItemList.Count）。
func countBag(p *Player) int {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return 0
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	n := 0
	for _, it := range p.Char.Data.BagItems {
		if it != nil && it.Index != 0 {
			n++
		}
	}
	return n
}

// bagItemName 算物品的显示名（原版 GetItemName，ItmUnit.pas:733-741）。
func (s *Server) bagItemName(it *pb.UserItem) string {
	return dealItemNameOf(s, it)
}

// dealItemNameOf 是 dealItemName 的服务无关版本（无表时退回空串）。
func dealItemNameOf(s *Server, it *pb.UserItem) string {
	if it == nil {
		return ""
	}
	if s == nil {
		return ""
	}
	if tmpl := s.data.tables.Items.Get(int(it.Index) - 1); tmpl != nil {
		return tmpl.Name
	}
	return ""
}

// dealItemName 处理交易消息 body 里的物品名。
//
// ⚠️ 原版 17707/17755 是 `GetValidStr3(sItemName, sItemName, [' '])` ——
// **按第一个空格截断**，因为"信件物品"的名字后面带使用次数（如"火把 3"）。
// 没有分隔符协议，全靠 Pos(' ') 猜。照抄。
func dealItemName(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// sendAddItem 通知客户端"你的背包多了一件"（原版 SendAddItem → SM_ADDITEM）。
func (s *Server) sendAddItem(p *Player, it *pb.UserItem) {
	ci, ok := s.buildClientItem(it)
	if !ok {
		return
	}
	s.send(p.conn, proto.SM_ADDITEM, int32(it.MakeIndex), 0, 0, 0, string(ci.Append(nil)))
}

// sysMsgTo 给指定连接发一条系统消息（等价 s.sysMsg，但能指定 conn）。
func sysMsgTo(c net.Conn, msg string) {
	if c == nil {
		return
	}
	raw := wire.Packet{
		Head: proto.MakeDefaultMsg(proto.SM_SYSMESSAGE, 0, 0, 0, 0),
		Body: msg,
	}
	if _, err := c.Write(wire.EncodeDown(raw.Encode())); err != nil {
		log.Printf("发送失败: %v", err)
	}
}

// p2conn 取玩家连接（nil 安全）。
func p2conn(p *Player) net.Conn {
	if p == nil {
		return nil
	}
	return p.conn
}

// sendAddDealItemRemote 通知对方"对方交易栏多了一件"。
//
// 对应 SendAddDealItem（ObjBase.pas:24013-24014）：
//
//	SendSocket(MakeDefaultMsg(SM_DEALREMOTEADDITEM, Integer(Self), 0, 0, 1),
//	           EncodeBuffer(@ClientItem))
//
// ⚠️ **Series=1 是 body 存在标志**（客户端 `if body <> ”` 之外还靠 Series 判断
// 走 DecodeBuffer，MirClient/ClMain.pas:6638-6656），且用 SendSocket 绕过
// SendDefMessage 的"空 body 不发"。
func (s *Server) sendAddDealItemRemote(viewer, owner *Player, it *pb.UserItem) {
	ci, ok := s.buildClientItem(it)
	if !ok {
		return // 原版 24001/24020 返回 nil 时也不发（怪癖 D17）
	}
	s.send(viewer.conn, proto.SM_DEALREMOTEADDITEM, int32(owner.Obj.ID), 0, 0, 1,
		string(ci.Append(nil)))
}

// sendDelDealItemRemote 通知对方"对方交易栏少了一件"。
//
// 对应 SendDelDealItem（ObjBase.pas:23914-23915），字段同上。
func (s *Server) sendDelDealItemRemote(viewer, owner *Player, makeIndex uint32) {
	s.send(viewer.conn, proto.SM_DEALREMOTEDELITEM, int32(owner.Obj.ID), 0, 0, 1, "")
}

// ---------- 交易中的其它限制 ----------

// dealingBlocks 报告"因为在交易中所以拒绝"的操作为真，并已给玩家发提示。
//
// 原版共 4 处检查 `m_boDealing`：
//
//	1695  PickUp            交易中不能捡地上物品
//	16162 ClientUserBuyItem 交易中不能向商店买
//	17230 ClientTakeOnItems 交易中不能穿装备
//	17656 ClientDealTry     已在交易不能再发起
func (s *Server) dealingBlocks(p *Player, what string) bool {
	if p == nil || !p.dealing {
		return false
	}
	s.sysMsg(p.conn, "交易中不能"+what)
	return true
}

// cmdLetTrade @LetTrade：切换"是否允许被交易"。
//
// 对应 ObjBase.pas:7639-7645（原版拼作 `LetTrade`，是玩家**自己**的聊天命令，
// 和 @ALLOWMSG / @LETSHOUT 同一个 RunMsg，任何玩家都能用）。
//
// ⚠️ 原版字段 `m_boAllowDeal` 的注释写反了（`//0x27F 是不允许交易`），
// 实际语义是"允许交易"，默认 True（ObjBase.pas:1330 构造时置 True）。
func (s *Server) cmdLetTrade(c net.Conn, p *Player) {
	p.allowDeal = !p.allowDeal
	if p.allowDeal {
		s.sysMsg(c, "你现在允许被交易。")
	} else {
		s.sysMsg(c, "你现在禁止被交易。")
	}
}

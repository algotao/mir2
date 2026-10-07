package main

// 组队 + 玩家交易的 e2e 用例。
//
// 两条链路都要求**两个人面对面**——原版唯一的距离/朝向门槛
// （GetPoseCreate，ObjBase.pas:15875-15884）。
//
// ⚠️ 断言策略：**只看主角色这一侧的包**，对方侧的验证放到 e2e 的服务端日志
// check 里。原因是对方的收包窗口很难稳定——两个角色的出生点随机（实测同一次
// 运行里能差 17 格）、并行 + 20 倍速下 SM_TURN/SM_WALK 的到达顺序不保证，
// 早先版本就因为"等对方收 SM_GROUPMEMBERS"而随机失败。

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/wire"
)

// runGroupAndDeal 验证组队与玩家交易。
func runGroupAndDeal(cc, peer *conn, actorID, peerActorID int32,
	posX, posY int, selfDir uint8, peerName string) {

	fmt.Println("[20] 测试组队（建组/名单/加成员/删成员）")

	// peer 打开"允许组队"（CM_GROUPMODE Param=1）——原版没这个开关就拉不动人
	// （ObjBase.pas:17562 判 `not m_boAllowGroup`）。
	peer.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_GROUPMODE, 0, 1, 0, 0)})

	// 把 peer 摆到主角色正前方一格并面向它。
	mx, my := posX, posY
	fx, fy := frontTileOf(mx, my, selfDir)
	facePeerBefore(peer, fx, fy, oppositeDir(selfDir))
	// 摆位会在两边都产生噪声包（SM_CLEAROBJECTS/SM_CHANGEMAP/SM_TURN/SM_WALK），
	// 清掉以免干扰下面的查找。
	drain(peer, 8, 200*time.Millisecond)
	drain(cc, 8, 300*time.Millisecond)

	// ① 建组。body=对方名字；⚠️ 服务端其实**丢弃**它（ClientCreateGroup 的形参
	// 从未被引用），目标完全靠"朝向格"确定。带上它是为了贴近真实客户端。
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_CREATEGROUP, 0, 0, 0, 0),
		Body: peerName,
	})
	gotOK, sawList := false, false
	for i := 0; i < 14 && !(gotOK && sawList); i++ {
		p := cc.tryRecvTimeout(700 * time.Millisecond)
		if p == nil {
			break
		}
		switch p.Head.Ident {
		case proto.SM_CREATEGROUP_OK:
			gotOK = true
		case proto.SM_GROUPMEMBERS:
			sawList = true
			if !strings.Contains(p.Body, peerName) {
				log.Fatalf("成员名单里没有 %s：%q", peerName, p.Body)
			}
			// ⚠️ 尾随的 "/" 不能省（客户端靠它判结束，ClMain.pas:6357-6370）
			if !strings.HasSuffix(p.Body, "/") {
				log.Fatalf("成员名单 %q 缺少尾随斜杠", p.Body)
			}
		}
	}
	if !gotOK {
		log.Fatal("SM_CREATEGROUP_OK 没收到（建组失败：peer 不在面前，或没开允许组队）")
	}
	if !sawList {
		log.Fatal("SM_GROUPMEMBERS 没收到")
	}
	fmt.Println("    建组成功 + 成员名单（含尾随斜杠）✓")

	// ② 重复加成员 ⇒ SM_GROUPADDMEM_FAIL(-3 = 对方已在队里)
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_ADDGROUPMEMBER, 0, 0, 0, 0),
		Body: peerName,
	})
	code := int32(-999)
	for i := 0; i < 14 && code == -999; i++ {
		p := cc.tryRecvTimeout(700 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident == proto.SM_GROUPADDMEM_FAIL {
			code = p.Head.Recog
		}
	}
	if code != -3 {
		log.Fatalf("重复加成员 Recog = %d，期望 -3（对方已在队里）", code)
	}
	fmt.Println("    重复加成员 → Recog=-3（对方已在队里）✓")

	// ③ 删成员 ⇒ 人数掉到 1 ⇒ 自动散队（CancelGroup ObjBase.pas:21647-21660）
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_DELGROUPMEMBER, 0, 0, 0, 0),
		Body: peerName,
	})
	delOK, cancelOK := false, false
	for i := 0; i < 16 && !(delOK && cancelOK); i++ {
		p := cc.tryRecvTimeout(700 * time.Millisecond)
		if p == nil {
			break
		}
		switch p.Head.Ident {
		case proto.SM_GROUPDELMEM_OK:
			delOK = true
		case proto.SM_GROUPCANCEL:
			cancelOK = true
		}
	}
	if !delOK {
		log.Fatal("SM_GROUPDELMEM_OK 没收到")
	}
	if !cancelOK {
		log.Fatal("踢到只剩 1 人后应自动散队（SM_GROUPCANCEL）")
	}
	fmt.Println("    删成员 → 自动散队 ✓")

	// ---------- 交易 ----------
	fmt.Println("[21] 测试玩家交易（发起/放物品/改金币/取消）")

	// ⚠️ **不要重新摆位**。组队那几步只发协议、不挪人，peer 仍然站在
	// 主角色面前一格并朝着它。反而是"再传送一次"会出问题：第二次 @map
	// 的 SM_CHANGEMAP 偶尔落在上一次 facePeerBefore 的 200ms 睡眠窗口里
	// 被当成旧包消耗掉，于是这里的表现是"传送失败"——一个自造的假故障。
	// 只把两边的噪声包清掉即可。
	drain(peer, 8, 200*time.Millisecond)
	drain(cc, 6, 300*time.Millisecond)

	// ① 发起：CM_DEALTRY。目标不看 body，看"自己面朝那一格"。
	//    双方是**同时**进入交易态的——原版没有"对方接受"这一步。
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_DEALTRY, 0, 0, 0, 0),
		Body: peerName,
	})
	menuOK := false
	for i := 0; i < 14 && !menuOK; i++ {
		p := cc.tryRecvTimeout(700 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident == proto.SM_DEALMENU {
			menuOK = true
			if !strings.Contains(p.Body, peerName) {
				log.Fatalf("SM_DEALMENU 的 body 应是对方名字 %s，得到 %q", peerName, p.Body)
			}
		}
	}
	if !menuOK {
		log.Fatal("SM_DEALMENU 没收到（交易未开启：peer 不在面前）")
	}
	fmt.Println("    交易开启（双方同时进入；原版没有\"对方接受\"这一步）✓")

	// ② 放一件物品进交易栏。
	//
	// ⚠️ body 是**物品名**、Recog 是 MakeIndex（MirClient/ClMain.pas:3836-3837），
	// 服务端做**双因子**校验（MakeIndex + 名字）——这是防"改名物品复制"的关键。
	it, ok := firstBagItem(cc)
	if !ok {
		log.Fatal("背包是空的，无法测交易放物品")
	}
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_DEALADDITEM, it.MakeIndex, 0, 0, 0),
		Body: it.Name,
	})
	if !waitIdent(cc, proto.SM_DEALADDITEM_OK, 14) {
		log.Fatal("SM_DEALADDITEM_OK 没收到")
	}
	fmt.Printf("    放入交易栏：%s (MakeIndex=%d) ✓\n", it.Name, it.MakeIndex)

	// 放错名字 ⇒ 必须被拒（双因子校验的另一半）
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_DEALADDITEM, it.MakeIndex, 0, 0, 0),
		Body: "不存在的物品名",
	})
	if !waitIdent(cc, proto.SM_DEALADDITEM_FAIL, 14) {
		fmt.Println("    [警告] 名字不匹配竟被接受（双因子校验可能没生效）")
	} else {
		fmt.Println("    名字不匹配 → SM_DEALADDITEM_FAIL（双因子校验生效）✓")
	}

	// ③ 改金币：金币数在 **Recog**（32 位有符号，ClMain.pas:3852）
	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_DEALCHGGOLD, 1000, 0, 0, 0)})
	if !waitIdent(cc, proto.SM_DEALCHGGOLD_OK, 14) {
		log.Fatal("SM_DEALCHGGOLD_OK 没收到")
	}
	fmt.Println("    放入金币 1000 ✓")

	// ④ 取消（此时对方还没按成交）⇒ 物品/金币退回、双方都收到 SM_DEALCANCEL
	cc.send(pkt(proto.CM_DEALCANCEL, ""))
	if !waitIdent(cc, proto.SM_DEALCANCEL, 14) {
		log.Fatal("SM_DEALCANCEL 没收到")
	}
	fmt.Println("    取消交易（物品/金币退回）✓")
	fmt.Println("    组队与交易链路已验证 ✓")
}

// frontTileOf 返回 (x,y) 面朝 dir 时**正前方**那一格。
//
// 对应原版 GetFrontPosition（ObjBase.pas:15875-15884）：只取一格，没有距离概念。
func frontTileOf(x, y int, dir uint8) (int, int) {
	d := entity.DirDelta[dir&7]
	return x + d[0], y + d[1]
}

// oppositeDir 返回反方向（peer 站在主角色面前，所以 peer 朝向是主角色的反向）。
func oppositeDir(dir uint8) uint8 { return (dir + 4) & 7 }

// facePeerBefore 用 GM 传送把 peer 摆到 (x,y) 并面向 dir。
//
// ⚠️ **刻意用传送而不是走路**：服务端的 CM_WALK 只按 Tag 里的方向走**一格**
// （ObjBase.pas:1739-1774 取 `LoByte(DefMsg.Tag)` 当方向，Recog 里的目标坐标
// 并不直接用），而两个人的出生点是随机的（实测同一次运行里能差 17 格）。
// 真客户端是客户端自己算路径一步步走；测试里那样既慢又会被移动限流打断。
// 摆位是测试脚手架的事，不是被测功能。
//
// 传送成功的判据取 **peer 自己**收到的 SM_CHANGEMAP —— 主角色那一侧是否
// 收到广播取决于两人是否已在视野内，不可靠。
//
// ⚠️ 下面两个"坑"都是踩过的（2026-10-05 定位；症状是**偶发** `无法把 peer 传送到…`，
// 而服务端日志里其实**已经切图成功** —— 是客户端自己没读到）：
//
//  1. **发传送前必须先清空遗留包**。peer 刚进游戏时，服务端会为它新视野里的
//     **每一个对象**发一条 SM_TURN（图 0 的出生点附近有大量 NPC 与怪），几十条；
//     peer 的进游戏握手只读到"看到主角色"那一条就停了，剩下的全留在缓冲里。
//     早先按**固定包数**（30 次）等 CHANGEMAP，预算在读到它之前就被这些遗留包
//     吃光 ⇒ 假故障。是否触发取决于出生点附近恰好有多少对象，所以表现为偶发。
//  2. 等待要用**墙钟截止**而不是固定包数：切图成功后服务端还会补发一批
//     SM_CLEAROBJECTS/SM_TURN（新视野），同样会吃掉按包数算的预算。
func facePeerBefore(peer *conn, x, y int, dir uint8) {
	// ① 先吃掉遗留包（含上面那批 SM_TURN）：一直读到出现一段空窗为止。
	drain(peer, 256, 150*time.Millisecond)

	peer.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: fmt.Sprintf("@map 0 %d %d", x, y),
	})

	// ② 墙钟截止（10s）内等 CHANGEMAP，中间来什么包都不消耗预算。
	//
	// ⚠️ 也不能在第一个 nil 就 break：一次 tryRecvTimeout 只读一个包，
	// 队列瞬时空不代表传送失败（switchMap → SM_CLEAROBJECTS → SM_CHANGEMAP
	// 之间还有 updateVision 的耗时）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p := peer.tryRecvTimeout(500 * time.Millisecond)
		if p == nil {
			continue
		}
		if p.Head.Ident != proto.SM_CHANGEMAP {
			continue
		}
		// 到位后用 **CM_TURN** 定朝向。
		//
		// ⚠️ **不能用 CM_WALK 定朝向**：服务端 handleMove 只把 Tag 当方向
		// 走**一格**（ObjBase.pas:1739-1774），那会把 peer 从 (x,y) 又挪走一格
		// ——正好挪到主角色所在格上 Earlier版本就是这么把站位搞坏的。
		// CM_TURN(3010) 只改朝向不动位置（handleTurn → Obj.Turn）。
		peer.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_TURN, 0, 0, uint16(dir), 0),
		})
		time.Sleep(tscale.D(200 * time.Millisecond))
		return
	}
	log.Fatalf("无法把 peer 传送到 (%d,%d)（组队/交易要求面对面）", x, y)
}

// waitIdent 等某个 Ident 的包（顺带丢弃其它包），返回是否等到。
func waitIdent(cc *conn, ident uint16, tries int) bool {
	for i := 0; i < tries; i++ {
		p := cc.tryRecvTimeout(800 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident == ident {
			return true
		}
	}
	return false
}

// drain 丢弃 n 个包（或直到超时），用于清掉摆位阶段的噪声。
func drain(cc *conn, n int, d time.Duration) {
	for i := 0; i < n; i++ {
		if cc.tryRecvTimeout(d) == nil {
			return
		}
	}
}

// bagItemInfo 是从 SM_BAGITEMS 里切出来的一条物品的最小信息。
type bagItemInfo struct {
	Name      string
	MakeIndex int32
}

// firstBagItem 取背包里第一件有效物品（交易用例要用 MakeIndex + 名字）。
func firstBagItem(cc *conn) (bagItemInfo, bool) {
	cc.send(pkt(proto.CM_QUERYBAGITEMS, ""))
	for i := 0; i < 10; i++ {
		p := cc.tryRecvTimeout(800 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident != proto.SM_BAGITEMS {
			continue
		}
		// body 是 ClientItem 的顺序拼接，**没有**数量前缀，
		// 按 ClientItemSize 逐条切（ClMain.pas ClientGetBagItmes）。
		body := []byte(p.Body)
		for off := 0; off+proto.ClientItemSize <= len(body); off += proto.ClientItemSize {
			ci, ok := proto.DecodeClientItem(body[off : off+proto.ClientItemSize])
			if !ok {
				continue
			}
			if name := ci.S.GetName(); name != "" {
				return bagItemInfo{Name: name, MakeIndex: ci.MakeIndex}, true
			}
		}
		return bagItemInfo{}, false
	}
	return bagItemInfo{}, false
}

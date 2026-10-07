package main

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

// 城门开 / 关 + 城墙石化 的端到端用例。
//
// 两件事都落在**服务端的地图格与目标判定**上，客户端只能这么验：
//
//	A. 城门开/关 = 地图格阻挡（原版 `TCastleDoor.SetMapXYFlag`，ObjMon2.pas:1013-1060）：
//	   沙巴克城门在 (672,330)（SabukW.txt 的 MainDoorX/Y），门是"2 格宽 3 格高 + 门框"。
//	     门开着（官方 MainDoorOpen=1）⇒ 从 (672,331) 往上走**能进** (672,330)；
//	     `@castle door close` ⇒ 同一格走不动（SM_MOVEFAIL）；
//	     `@castle door open`  ⇒ 又能走。
//
//	B. 石化 = **不可被选为目标**（`ObjBase.pas:21491-21492`）：
//	   非攻城期城墙石化 ⇒ 砍它**不产生任何 SM_STRUCK**；
//	   强制开战（`@castle war on`，需要先设占领方）后石化解除 ⇒ 同一刀命中。
//	   两条互为对照，避免"因为别的原因没打到"当成通过。
func runCastleDoor(cc *conn, actorID int32, startX, startY int) (int, int) {
	fmt.Println("\n[城门] 测试开/关与城墙石化")

	posX, posY := startX, startY

	// 到城门外侧（门在 (672,330)，站在它正下方）
	if !mapTo(cc, "3", 672, 331, &posX, &posY) {
		log.Fatal("未切换到沙巴克战场地图（map 3）")
	}
	fmt.Println("    已到城门外侧 (672,331)")

	step := func(dir uint8) (int, int, bool) {
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_WALK,
				proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(dir), 0),
		})
		deadline := time.Now().Add(rcv(1200 * time.Millisecond))
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			switch rp.Head.Ident {
			case proto.SM_WALK:
				if int32(rp.Head.Recog) == actorID {
					return int(rp.Head.Param), int(rp.Head.Tag), true
				}
			case proto.SM_MOVEFAIL:
				if int32(rp.Head.Recog) == actorID {
					return posX, posY, false
				}
			}
		}
		return posX, posY, false
	}
	// 走路有 600ms（游戏时间）限流，两次移动之间要等一下
	waitMove := func() { time.Sleep(tscale.D(700 * time.Millisecond)) }
	say := func(cmd string) {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
	}

	// A1：初始门是开的（SabukW.txt:18 `MainDoorOpen=1`）⇒ 能走进门那一格
	if nx, ny, ok := step(entity.DirUp); !ok || nx != 672 || ny != 330 {
		log.Fatalf("门开着却进不去 (672,330)：ok=%v 走到 (%d,%d)", ok, nx, ny)
	}
	posX, posY = 672, 330
	fmt.Println("    门开着：走进 (672,330) ✓")
	waitMove()

	// A2：关门 ⇒ 同一格走不动
	say("@castle door close")
	if waitSysMsg(cc, "城门") == "" {
		log.Fatal("@castle door close 没有回执")
	}
	waitMove()
	if _, _, ok := step(entity.DirUp); ok {
		log.Fatal("门关了却还能往里走（阻挡格没生效）")
	}
	fmt.Println("    门关着：走不进去（SM_MOVEFAIL）✓")
	waitMove()

	// A3：再开 ⇒ 又能走
	say("@castle door open")
	if waitSysMsg(cc, "城门") == "" {
		log.Fatal("@castle door open 没有回执")
	}
	waitMove()
	nx, ny, ok := step(entity.DirUp)
	if !ok || ny != posY-1 {
		log.Fatalf("门重新打开后仍然走不动：ok=%v 走到 (%d,%d)", ok, nx, ny)
	}
	posX, posY = nx, ny
	fmt.Printf("    门重新打开：走到 (%d,%d) ✓\n", posX, posY)

	// B：石化不可打 —— 拿左城墙（SabukW1，SabukW.txt: 624,278）做对照
	if !mapTo(cc, "3", 624, 279, &posX, &posY) {
		log.Fatal("未切换到城墙下方 (624,279)")
	}
	hitUp := func() {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HIT, 0, 0, uint16(entity.DirUp), 0)})
	}
	struck := func(win time.Duration) bool {
		deadline := time.Now().Add(win)
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_STRUCK {
				return true
			}
		}
		return false
	}
	// ⚠️ 新角色命中只有 5、城墙敏捷 15 ⇒ `Random(15)` 中 6..14 都会打空，打空率 60%；
	// "石化"那一侧是**确定性**的（石化=不可选为目标，挥多少刀都不该有 SM_STRUCK），
	// "可打"那一侧则靠多挥几刀把随机性压掉。
	swing := func(times int) bool {
		for i := 0; i < times; i++ {
			drainUntilQuiet(cc, rcv(200*time.Millisecond))
			hitUp()
			if struck(rcv(600 * time.Millisecond)) {
				return true
			}
			time.Sleep(tscale.D(700 * time.Millisecond)) // 攻击也有最小间隔
		}
		return false
	}
	if swing(6) {
		log.Fatal("非攻城期城墙处于石化，攻击不该命中（收到 SM_STRUCK）")
	}
	fmt.Println("    非攻城期：砍石化城墙 6 刀 → 一刀没中 ✓")

	// 强制开战（GM）：需要先有占领方；开战后城墙石化解除
	// ⚠️ 每条命令前先排空：`@castle owner` 会连发两条系统消息
	//（"已被 … 占领" + "城堡占领方已设为 …"），不排空就会把上一条的尾巴
	// 当成这一条的回执（踩过：@castle war on 的"回执"其实是 owner 的第二条）。
	gm := func(cmd, want string) {
		drainUntilQuiet(cc, rcv(400*time.Millisecond))
		say(cmd)
		got := waitSysMsg(cc, want)
		fmt.Printf("    %s → %q\n", cmd, got)
		if got == "" {
			log.Fatalf("%s 没有回执（期望含 %q）", cmd, want)
		}
	}
	gm("@guild create 城门行会", "已创建")
	gm("@castle owner 城门行会", "占领")
	gm("@castle war on", "攻城")
	// 砍城墙的命中也必须**确定性**：裸命中 5 对城墙敏捷 15 有 60% 打空，
	// 8 刀全空的概率约 1.7%（这轮在满载回归中撞到）。通过练满两个被动，
	// 命中 = 5 + 基本剑术 9 + 精神力战法 8 = 22 > Random(15) 上界 ⇒ 必中。
	for _, skill := range []int{3, 4} {
		cmd := fmt.Sprintf("@magic %d 3", skill)
		gm(cmd, fmt.Sprintf("技能 %d 等级设为 3", skill))
	}
	// 服务端在开战钩子里会立刻同步一次石化（castleLoop 每 10 秒游戏时间也会同步），
	// 这里等**真实** 600ms 让广播/状态都落地（tscale.D 会缩成几十毫秒，不够）。
	time.Sleep(600 * time.Millisecond)
	if !swing(8) {
		log.Fatal("开战后城墙石化应解除，攻击却仍然打不到")
	}
	fmt.Println("    攻城期：砍同一面墙 → 命中 ✓")
	say("@castle war off")
	if m := waitSysMsg(cc, "攻城"); m == "" {
		fmt.Println("    （@castle war off 没回执，忽略）")
	}
	fmt.Println("    城门开/关 + 石化目标判定已验证 ✓")
	// ⚠️ 这一段（D）必须在 [守卫] 那一段**之前**：C2 打完守卫/门之后守卫会进入
	// 2 分钟仇恨窗口，而 1 级角色只有 20 血、守卫一刀 200 伤害 ⇒ 必死回城，
	// 后面的断言全部作废（实测踩到："@castle repair 没有回执"，服务端日志里
	// 角色是被守卫击倒后回城的）。
	// D：门被打碎后的**重建**（原版 RepairDoor 的"已摧毁"分支，Castle.pas:1170-1179）。
	//
	//	门关着 ⇒ (672,330) 走不进去（A2 已经验过一次）
	//	打碎（@castle damage door 1）⇒ 门格全开（SetMapXYFlag(2)）⇒ 能走进去
	//	修好（@castle repair door 1）⇒ 原版补 `m_boDeath := False` **和
	//	   `m_boOpened := False`**（关门）⇒ TCastleDoor.Close 会 SetMapXYFlag(1)
	//	   ⇒ 同一格**又走不进去**
	//
	// ⚠️ 少了"关门 + 重设门格"这一步，表现就是"修完了照样能穿门而过"
	//（地图格还停在破门时的 nFlag=2）—— 这正是本次修的问题。
	fmt.Println("\n[城门] 测试打碎后的重建")
	if !mapTo(cc, "3", 672, 331, &posX, &posY) {
		log.Fatal("未切换回门下 (672,331)")
	}
	drainUntilQuiet(cc, rcv(400*time.Millisecond))
	gm("@castle door close", "城门")
	if _, _, ok := step(entity.DirUp); ok {
		log.Fatal("门关着却能往里走（与 A2 的结论矛盾，状态不对）")
	}
	waitMove()
	gm("@castle damage door 1", "已被打坏")
	waitMove()
	if nx, ny, ok := step(entity.DirUp); !ok || ny != 330 {
		log.Fatalf("门被打碎后应能走进去，实际 ok=%v 走到 (%d,%d)", ok, nx, ny)
	}
	fmt.Println("    打碎后能走进去 ✓")
	waitMove()
	// 回到门外再修（修完要验"又走不进去"）
	if !mapTo(cc, "3", 672, 331, &posX, &posY) {
		log.Fatal("破门后回到门外失败")
	}
	drainUntilQuiet(cc, rcv(400*time.Millisecond))
	gm("@gold 5000000", "金币设为") // 修门要 RepairDoorPrice=2000000
	gm("@castle repair door 1", "修理完成")
	waitMove()
	if _, _, ok := step(entity.DirUp); ok {
		log.Fatal("门修好后应重新挡住（RepairDoor 会关门并重设门格）")
	}
	fmt.Println("    修好后又被挡住 ✓（服务端 rebuild 日志见 e2e 断言）")

	// C：守卫的"该打谁"（原版 TGuardUnit.IsProperTarget，ObjMon2.pas:828-883）。
	//
	//  C1 角色此时是**守方行会**（"城门行会"）掌门、非攻城期 ⇒ 站在守卫旁**不该**被打。
	//     ⚠️ 这条卡住老 bug：城堡单位不加分流时，守卫/城门按"最近的玩家"开打
	//     （城门还会追着人跑）。
	//  C2 打过城堡单位（砍门）⇒ 2 分钟仇恨窗口（TGuardUnit.Struck）⇒ 守卫锁定他。
	//     ⚠️ 必须先**退会**：守方行会成员即使有过窗口，④ 也会把结果覆盖回"不打"
	//     （原版如此，见 guard.go 文件头）——不退会就会误判成功能没生效。
	fmt.Println("\n[守卫] 测试城堡守卫的目标判定")
	// 守卫 1 在 (671,334)（internal/castle/sabuk.go 的 guardXY），站它正下方
	if !mapTo(cc, "3", 671, 335, &posX, &posY) {
		log.Fatal("未切换到守卫处 (671,335)")
	}
	// 雇佣要钱（HireGuardPrice=300000）；gm() 会打印原始回执，便于定位
	gm("@gold 1000000", "金币设为")
	gm("@castle hire guard 1", "雇佣")
	// 等实体生成 + 广播落地（真实 600ms：tscale.D 会缩得不够）
	time.Sleep(600 * time.Millisecond)

	// C1.5：**玩家侧标签**（审计 §2.6）。城堡战场地图上的 NPC 会追加一份城堡菜单
	//（原版那套属于 `TCastleOfficial` 这个特殊 NPC，出厂数据里没有 ⇒ 我们的替身规则），
	// 点它会发回原版标签原文 `@hireguardnow<N>` ⇒ 与 GM 的 `@castle hire guard N`
	// 走**同一套**底层逻辑（handleDlgSelectText → handleCastleMsg → handleCastleRepair）。
	if !mapTo(cc, "3", 663, 305, &posX, &posY) {
		log.Fatal("未切换到城内 NPC 旁 (663,305)")
	}
	// ⚠️ 这里**不能** drain：NPC 的出现包就夹在切图那批里，drain 掉就再也等不到了
	//（NPC 只在"进视野"那一刻发一次 SM_TURN）。
	npcID := uint32(0)
	npcDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(npcDeadline) && npcID == 0 {
		rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_TURN && rp.Head.Recog >= proto.NpcIDBase &&
			int(rp.Head.Param) == 663 && int(rp.Head.Tag) == 304 {
			npcID = uint32(rp.Head.Recog)
		}
	}
	if npcID == 0 {
		log.Fatal("城内没看到 NPC（663,304）")
	}
	menu := ""
	for attempt := 0; attempt < 3 && menu == ""; attempt++ {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0)})
		end := time.Now().Add(rcv(2 * time.Second))
		for time.Now().Before(end) && menu == "" {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(rp.Body, "修城门") {
				menu = rp.Body
			}
		}
	}
	if menu == "" {
		log.Fatal("打开城内 NPC 后没收到城堡菜单（castleOfficialMenu 没生效？）")
	}
	fmt.Printf("    城内 NPC 对话里出现城堡菜单 ✓（%d 字节）\n", len(menu))
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_MERCHANTDLGSELECT, int32(npcID), 0, 0, 0),
		Body: "@hireguardnow2",
	})
	hired := false
	hireEnd := time.Now().Add(rcv(3 * time.Second))
	for time.Now().Before(hireEnd) && !hired {
		rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_SYSMESSAGE {
			if strings.Contains(rp.Body, "雇佣成功") {
				hired = true
			}
		}
	}
	if !hired {
		log.Fatal("点 @hireguardnow2 没有雇佣成功（玩家侧标签没接上？）")
	}
	fmt.Println("    @hireguardnow2 → 雇佣成功 ✓（玩家侧标签与 GM 走同一套逻辑）")

	// C1：守方行会成员 + 非攻城期 ⇒ 不该被打
	deadline := time.Now().Add(3 * time.Second)
	hitBy := 0
	for time.Now().Before(deadline) {
		rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_STRUCK && int32(rp.Head.Recog) == actorID {
			hitBy++
		}
	}
	if hitBy > 0 {
		log.Fatalf("非攻城期的守方成员站在守卫旁，守卫却打了他 %d 次（IsProperTarget ④ 没生效）", hitBy)
	}
	fmt.Println("    守方成员站在守卫旁 3 秒：一次没被打 ✓")

	// C2：退会（掌门 `@guild kick 自己` = 解散）⇒ 不再是守方成员
	gm("@guild kick 回归城门", "已被移出")
	// ⚠️ 必须**先开战**才砍得动门：`canHitCastleUnit` 只在"攻城期 / 守方 / 盟友"
	// 三种情况下放行（非攻城期退会后连门都打不到 —— 踩过，表现为"砍 12 刀一刀没中"）。
	gm("@castle war on", "攻城")
	// 关门（A3 结束时门是开的；开着的门处于石化 = 不可打）
	gm("@castle door close", "城门")
	// 到门下砍门：砍中一下即进 2 分钟仇恨窗口（命中率不高，多砍几刀）
	if !mapTo(cc, "3", 672, 331, &posX, &posY) {
		log.Fatal("未切换到门下 (672,331)")
	}
	drainUntilQuiet(cc, rcv(400*time.Millisecond))
	landed := 0
	for i := 0; i < 12; i++ {
		hitUp()
		if struck(rcv(600 * time.Millisecond)) {
			landed++
			break
		}
		time.Sleep(tscale.D(700 * time.Millisecond)) // 攻击有最小间隔
	}
	if landed == 0 {
		log.Fatal("砍城门 12 刀一刀没中（门没关好？），guard-lock 断言无从谈起")
	}
	fmt.Println("    已砍中城门（进 2 分钟仇恨窗口）✓")
	// ⚠️ **关战**：这样守卫锁定的依据只剩 ②（仇恨窗口）—— 否则 ③「攻城期人人可打」
	// 会让这条断言失去证明力（不开战也照样锁定）。
	gm("@castle war off", "攻城")

	// 回到守卫旁：它此时应把"打过城堡单位"的玩家当合法目标 ⇒ 服务端记锁定日志
	if !mapTo(cc, "3", 671, 335, &posX, &posY) {
		log.Fatal("回守卫处失败")
	}
	// 等守卫的 AI 跑到"换目标"那一步（真实 ~1.2s，怪物 tick 500ms）
	time.Sleep(1200 * time.Millisecond)
	fmt.Println("    守卫应已锁定（服务端 guard-lock 日志）")

	// ⚠️ 收尾站到**已知能走的空地**再交还控制权：用例末尾那条"走路"断言会从
	// 当前位置往下一格走，而城墙脚下 (624,279) 的下方是堵死的（踩过：
	// 整个用例都过了，最后一步"未收到 SM_WALK"）。用城堡回城点（3,644,290）。
	if !mapTo(cc, "3", 644, 290, &posX, &posY) {
		log.Fatal("收尾传送失败")
	}
	fmt.Printf("    收尾站位 (%d,%d)（供末尾走路断言用）\n", posX, posY)
	return posX, posY
}

// mapTo 用 GM 传送切到 (mapID, x, y)，成功时把 *px/*py 更新成落点。
//
// ⚠️ 用例最后还有"走路"断言按 main 里的 posX/posY 比对，所以必须把落点带回去
// （同 -pvp/-contest/-storage 的处理）。
func mapTo(cc *conn, mapID string, x, y int, px, py *int) bool {
	drainUntilQuiet(cc, rcv(500*time.Millisecond))
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: fmt.Sprintf("@map %s %d %d", mapID, x, y),
	})
	// ⚠️ 改成队列**谓词等待**（只摘走匹配的那条，其余留在队列）：
	// 原来"固定 12 次 + 每条无关包都消耗预算"，切图的 SM_TURN 洪流一来就吃光。
	rp := cc.waitFor(func(p *wire.Packet) bool {
		return p.Head.Ident == proto.SM_CHANGEMAP && strings.TrimSpace(p.Body) != ""
	}, rcv(6*time.Second))
	if rp == nil {
		return false
	}
	*px, *py = int(rp.Head.Param), int(rp.Head.Tag)
	return true
}

package main

// PvP 端到端用例：验证"能打玩家 + PK 罪 + 红名 + 安全区 + 正当防卫"。
//
// ⚠️ 这些断言依赖服务端**不缩放收包超时**，所以本用例不适合时间倍速下
// 反复跑（见 docs/HANDOFF.md 的"时间倍速"一节：收包预算是 I/O 等待，
// 缩了会误判"服务端没响应"）。scripts/e2e.sh 里用默认倍速或较低倍速。

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

// runPvP 执行 PvP 用例。actorID/peer/peerName 来自主流程已建好的两个连接。
func runPvP(cc *conn, peer *conn, peerID int32) {
	fmt.Println("\n[PvP] 测试玩家对战")

	if peer == nil {
		log.Fatal("PvP 用例需要 -peer 才有第二个玩家")
	}

	// ---- 0. 走出安全区 ----
	//
	// 双方都出生在比奇省出生点 (289,618)，而出生点半径 10 内是安全区
	//（ObjBase.pas:21545-21554），IsProtectTarget 会正确地不让打。
	// 所以要先把两人挪出安全区，否则整段 PvP 断言都在验"打不到"。
	//
	// 用 GM 传送而不是走路：走路要走 10+ 步且会被移动限流拖慢。
	// ⚠️ 两人必须**相邻**：服务端按"自己坐标 + 方向"取目标格的前一格
	//（main.go handleAttack），隔 2 格打不到。
	const (
		fieldX = 320
		fieldY = 618
	)
	sayCmd(cc, fmt.Sprintf("@map 0 %d %d", fieldX, fieldY))
	sayCmd(peer, fmt.Sprintf("@map 0 %d %d", fieldX+1, fieldY))
	// ⚠️ 这里必须等**真实** 3.2 秒，不能用 sc() 缩放：
	// 传送后 3 秒内互相打不到人（m_dwMapMoveTick，ObjBase.pas:21322-21323，
	// 由 internal/pvp.MoveProtectTime 实现）。20 倍速下 sc(900ms) 只有 45ms，
	// 会被这条保护拦掉，表现为"全体攻击也打不中"。
	time.Sleep(3200 * time.Millisecond)
	pvpFieldPosX, pvpFieldPosY = fieldX, fieldY
	fmt.Println("    双方已挪出安全区（比奇省出生点半径 10），并等过传送 3 秒保护")

	// ⚠️ 先把攻击方的**命中**练满：原版 `_Attack` 有打空判定
	//（`命中 < Random(目标敏捷)`，见 hitpoint.go），两个新号的命中都是 DEFHIT=5、
	// 敏捷都是 15 ⇒ 会打空 2/3 —— 那样"全体攻击应命中"这条断言就得靠运气。
	// 基本剑术(+9) + 精神力战法(+8) ⇒ 命中 22 > Random(15) 上界，一刀不空。
	sayCmd(cc, "@magic 3 3")
	sayCmd(cc, "@magic 4 3")
	fmt.Println("    攻击方练满基本剑术/精神力战法（命中 22，不会打空）")

	// ---- 1. 切和平模式：打不到玩家 ----
	//
	// ⚠️ 必须**显式**切：AttackMode 的零值是 HamAll（全体攻击），
	// 新建角色的存档里 AttackMode=0，所以默认就能打人。
	sayCmd(cc, "@atkmode 1")
	if m := waitSysMsg(cc, "和平"); m == "" {
		log.Fatal("切换和平模式失败")
	}
	// 先排空 peer 队列里的残留包，否则上一次攻击的 SM_STRUCK
	// 会被当成本次结果（表现为"和平模式也打中了"）。
	drainStruck(peer, 300*time.Millisecond, peerID)
	hitRight(cc)
	// 和平模式下不该收到 SM_STRUCK（对方不会掉血）。
	//
	// ⚠️ 从 **peer 连接**收包：主角色是攻击者，它收到的是 SM_HIT 与
	// 旁人视角的广播；只有被打的 peer 自己必然收到那一条 SM_STRUCK
	// （服务端对 o == victim 有特判，见 pvp.go attackPlayer）。
	if hit := drainStruck(peer, 700*time.Millisecond, peerID); hit != nil {
		log.Fatalf("和平模式不该打中玩家，却收到 SM_STRUCK（HP %d/%d）",
			hit.hp, hit.maxHP)
	}
	fmt.Println("    和平模式：打不到玩家 ✓")

	// ---- 2. 切全体攻击 ----
	sayCmd(cc, "@atkmode 0")
	waitSysMsg(cc, "攻击模式")

	drainStruck(peer, 300*time.Millisecond, peerID)
	hitRight(cc)
	hit := drainStruck(peer, 3*time.Second, peerID)
	if hit == nil {
		log.Fatal("全体攻击模式下应能打中玩家，未收到 SM_STRUCK")
	}
	// ⚠️ 伤害值不能读 Head.Series：那是 16 位字段，装不下大数（会截断成
	// 86907195345134 这种离谱值）。真值在 SM_STRUCK 的 body 里
	//（TMessageBodyWL 的 Tag2，见 main.go sendStruck）。
	fmt.Printf("    全体攻击：命中，HP %d/%d（伤害 %d）✓\n",
		hit.hp, hit.maxHP, hit.dmg)

	// ---- 3. 查 PK 点 ----
	sayCmd(cc, "@pk")
	if m := waitSysMsg(cc, "PK 点"); m != "" {
		fmt.Printf("    %s\n", m)
	}

	// ---- 4. PK 死亡的等级奖惩（原版 `TPlayObject.PKDie`，ObjBase.pas:21076-21180）----
	//
	// 出厂四个开关**全关**（M2Share.pas:1779-1782，官方 !setup.txt:937-944 也都是 0）
	// ⇒ 默认杀人不改等级/经验。这里用调试命令 `@pkdie` 打开"赢家升级 + 输家掉级"，
	// 再把 peer 打到死，验**赢家的等级真的 +1**（输家是 1 级，掉级有 1 级保底 ⇒ 看不出来）。
	//
	// ⚠️ 只开这两个**等级**开关，不开经验那两个：`KillHumanWinExpPoint=100000`
	// 会让赢家连升好几级，断言反而不好写（经验那条由单测覆盖）。
	fmt.Println("\n[PKDeath] 测试 PK 死亡的等级奖惩")
	sayCmd(cc, "@pkdie winlevel on")
	if m := waitSysMsg(cc, "KillHumanWinLevel"); m == "" {
		log.Fatal("@pkdie winlevel on 没有回执")
	}
	sayCmd(cc, "@pkdie lostlevel on")
	if m := waitSysMsg(cc, "KilledLostLevel"); m == "" {
		log.Fatal("@pkdie lostlevel on 没有回执")
	}

	// 读自己的等级（SM_ABILITY 里带 Level）。
	//
	// ⚠️ 它是**排空式**的：窗口内最后一个属性包胜出（队列里早先到的也会被看到），
	// 所以"基线"这种一次性读取不会因为包先到而错过。
	readLevel := func(wait time.Duration) (uint16, bool) {
		var lv uint16
		got := false
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(50 * time.Millisecond)
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_ABILITY {
				if ab, dec := proto.DecodeAbility([]byte(rp.Body)); dec {
					lv, got = ab.Level, true
				}
			}
		}
		return lv, got
	}

	// ⚠️ 先把自己升到 5 级再打 —— "伤害不够"是这条断言最大的坑：
	//
	//  ① 1 级一刀只有 5~7，而对方 20 血、每击往返各回一口血（实测 +1）
	//     ⇒ 打了 8 刀 HP 还在 13↔12 之间来回，40 刀都打不死（用例白耗 15 秒还判失败）。
	//     5 级 DC 上台阶 ⇒ 一刀约 14，2~3 刀必死。
	//  ② 等级差必须 **≤ HumanLevelDiffer(10)**，否则 PKDie 的等级差保护会让四项全 0
	//     ⇒ 5 级打 1 级（差 4）刚好安全，**别顺手往高调**。
	//  ③ `@level` 会带出属性包 ⇒ 顺手把**基线等级**读出来（以前写死字面量 1，脆）。
	sayCmd(cc, "@level 5")
	baseLevel, gotBase := readLevel(2 * time.Second)
	if !gotBase {
		log.Fatal("① @level 5 后没读到属性包（拿不到基线等级）")
	}
	if baseLevel != 5 {
		log.Fatalf("@level 5 之后等级是 %d，期望 5（基线读错，后面全白测）", baseLevel)
	}

	// 打到死：按**攻击门禁的最快节奏**挥（520ms 游戏时间 ÷ 时间倍速 ≈ 26ms 现实），
	// 每 4 刀查一次等级（查一次要排空一小段收包窗口，太勤反而拖慢总节奏）。
	killed, lvAfter := false, baseLevel
	for i := 0; i < 60 && !killed; i++ {
		drainStruck(peer, 15*time.Millisecond, peerID)
		hitRight(cc)
		time.Sleep(30 * time.Millisecond)
		if i%4 == 3 {
			if lv, ok := readLevel(60 * time.Millisecond); ok && lv > baseLevel {
				killed, lvAfter = true, lv
			}
		}
	}
	if !killed {
		// 收尾再给一次机会：升级包可能刚好落在最后一刀的收包窗口之后
		if lv, ok := readLevel(time.Second); ok && lv > baseLevel {
			killed, lvAfter = true, lv
		}
	}
	if !killed {
		log.Fatalf("打死 peer 后自己的等级没变（仍为 %d）：PK 死亡的升级奖惩没生效", baseLevel)
	}
	if lvAfter != baseLevel+1 {
		log.Fatalf("赢家等级 %d → %d，期望只涨 1 级（KillHumanWinLevelPoint=1）", baseLevel, lvAfter)
	}
	fmt.Printf("    PK 死亡奖惩：赢家等级 %d → %d（输家掉级有 1 级保底）✓\n", baseLevel, lvAfter)

	// ⚠️ 收尾必须把 peer 挪回主角色身边：它刚被打死 ⇒ 已在老家（比奇省 289,618）复活，
	// 而本用例最后的**通用"走路"断言**要求"观察者收到同一次移动的广播"
	//（只发给视野内的玩家）⇒ 不搬回来那条断言必然超时
	//（踩过：报"等待响应: 超时"，前面所有断言都过了）。
	sayCmd(peer, fmt.Sprintf("@map 0 %d %d", fieldX+1, fieldY))
	// 等它切图 + 双方重新互相"看到"（出现包是广播的，排空即可）
	time.Sleep(400 * time.Millisecond)
	drainUntilQuiet(cc, 250*time.Millisecond)
	drainUntilQuiet(peer, 250*time.Millisecond)
	fmt.Println("    观察者已搬回原地（否则后面的走路广播断言收不到）")
}

// sayCmd 发一条聊天/GM 命令（客户端走 CM_SAY，body 是文本）。
func sayCmd(cc *conn, text string) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: text,
	})
}

// waitSysMsg 等一条含 key 的 SM_SYSMESSAGE，返回去掉前缀后的正文。
func waitSysMsg(cc *conn, key string) string {
	// 队列**谓词等待**：无关包留在队列里，不消耗等待预算（原来是逐条吃）
	p := cc.waitFor(func(p *wire.Packet) bool {
		return p.Head.Ident == proto.SM_SYSMESSAGE && (key == "" || strings.Contains(p.Body, key))
	}, 3*time.Second)
	if p == nil {
		return ""
	}
	return p.Body
}

// pvpFieldPos 记录 PvP 用例把主角色传送到哪，后续走路断言要用。
var pvpFieldPosX, pvpFieldPosY int

// hitRight 攻击主角色**正右方**那一格。
//
// CM_HIT 的 Recog 是目标坐标 MakeLong(x,y)、Tag 是方向（与打怪用例同写法）；
// 服务端按"自己坐标 + 方向"取目标格的前一格（main.go handleAttack）。
// 本用例把 peer 传送到主角色正右方一格，所以方向是确定的——
// 不要扫 4 个方向：20 倍速下靠 sleep 间隔连发会被限流吃掉。
func hitRight(cc *conn) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_HIT, 0, 0, uint16(entity.DirRight), 0),
	})
}

// struckInfo 是从 SM_STRUCK 解出的受击信息。
type struckInfo struct {
	recog     int32 // 受击者 ActorId
	hp, maxHP uint32
	dmg       uint32
}

// drainStruck 在窗口内收 SM_STRUCK；只认 recv 的那个受击者，没有则返回 nil。
//
// ⚠️ **必须按 Recog 过滤**：比奇省出生点附近有怪，攻击落点相邻格时可能
// 打到怪而不是 peer，会收到"别人的"SM_STRUCK 而误判成打中了玩家。
//
// SM_STRUCK 的字段语义（ClMain.pas:4964）：
// Recog=受击者 ActorId、Param=HP、Tag=MaxHP、Series=伤害（16 位，会截断）。
// 伤害真值在 body 的 TMessageBodyWL.Tag2，所以这里从 body 取。
func drainStruck(cc *conn, win time.Duration, recv int32) *struckInfo {
	deadline := time.Now().Add(win)
	for time.Now().Before(deadline) {
		p := cc.tryRecvTimeout(time.Until(deadline))
		if p == nil {
			return nil
		}
		if p.Head.Ident != proto.SM_STRUCK {
			continue
		}
		if int32(p.Head.Recog) != recv {
			continue
		}
		info := &struckInfo{
			recog: int32(p.Head.Recog),
			hp:    uint32(p.Head.Param),
			maxHP: uint32(p.Head.Tag),
			dmg:   uint32(p.Head.Series),
		}
		if wl, ok := proto.DecodeMessageBodyWL([]byte(p.Body)); ok {
			info.dmg = uint32(wl.Tag2)
		}
		return info
	}
	return nil
}

package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
)

// 行会争霸赛（FIGHT3 区）端到端用例。
//
// 验的是"击杀 → 击杀方行会 +100 → 广播比分"这条链路（原版 ObjBase.pas:21018-21038）：
//
//	双方各建一个行会 → 都进 F006（FIGHT3 图）并相邻站位 → 攻击方 @StartContest
//	（原版要求发出者**站在 FIGHT3 图上**，ObjBase.pas:11247-11251）
//	→ 把对方砍死 → 攻击方应收到 SM_HEAR 广播 "- 争霸A:100  争霸B:0"
//	→ @ContestPoint 争霸A → "争霸A 的得分为: 100"
//
// ⚠️ 建行会走 GM 命令 `@guild create`（测试捷径）：原版要 200W 金币 + 沃玛号角 +
// 行会管理员对话，那条正规路径由 e2e 的 `guild` 用例单独覆盖。
//
// ⚠️ `@StartContest` 不能省：记分函数都有 `if not boTeamFight then Exit` 的前置
// （Guild.pas:775/790）——"没开赛死了也不加分"正是这条用例要抓的。
func runContest(cc, peer *conn, peerName string, peerID int32) {
	fmt.Println("\n[行会争霸赛] 测试 FIGHT3 区的击杀记分")
	if peer == nil {
		log.Fatal("争霸赛用例需要 -peer（要两个不同行会的角色）")
	}

	const (
		guildA         = "争霸A"
		guildB         = "争霸B"
		fieldX, fieldY = 100, 100
	)
	// 记下站位：用例结束后调用方要把 posX/posY 换成这里（同 -pvp 的处理），
	// 否则最后那条走路断言会拿"进游戏时的出生点"去比对。见 main.go 的调用点。
	contestFieldPosX, contestFieldPosY = fieldX, fieldY

	// 1) 两边各建一个行会（一个角色只能在一个行会里，所以必须两个行会）
	for _, side := range []struct {
		c *conn
		g string
	}{{cc, guildA}, {peer, guildB}} {
		sayCmd(side.c, "@guild create "+side.g)
		if m := waitSysMsg(side.c, "已创建"); m == "" {
			log.Fatalf("建行会 %s 失败（GM 命令 @guild create）", side.g)
		}
	}
	fmt.Printf("    已有两个行会：%s / %s\n", guildA, guildB)

	// 2) 都进 F006（mapinfo: `[F006 行会战争地图6 0] FIGHT3 DARK`），相邻站位
	sayCmd(cc, fmt.Sprintf("@map F006 %d %d", fieldX, fieldY))
	sayCmd(peer, fmt.Sprintf("@map F006 %d %d", fieldX+1, fieldY))
	drainUntilQuiet(cc, rcv(800*time.Millisecond))
	drainUntilQuiet(peer, rcv(800*time.Millisecond))
	// ⚠️ 传送后 3 秒内互相打不到（m_dwMapMoveTick，pvp.MoveProtectTime）：
	// 必须等**真实** 3.2 秒，sc() 缩放后只有几十毫秒会被这条保护拦掉。
	time.Sleep(3200 * time.Millisecond)

	// 3) 攻击方：练满命中（否则打空 2/3）+ 全体攻击模式 + 起赛
	sayCmd(cc, "@magic 3 3")
	sayCmd(cc, "@magic 4 3")
	sayCmd(cc, "@atkmode 0")
	sayCmd(cc, "@StartContest")
	if m := waitSysMsg(cc, "行会争霸赛已经开始"); m == "" {
		log.Fatal("@StartContest 没有回执（该命令必须在 FIGHT3 图上用）")
	}
	fmt.Println("    已起赛（@StartContest 在 FIGHT3 图上）")

	// 4) 砍死对方，等那条**比分广播**（它就是"击杀记分真的发生了"的证据）
	// ⚠️ 用**真实** 20 秒而不是 rcv()：击杀本身很快，但广播可能被
	// SM_HIT/SM_STRUCK 的洪流压在后面，预算缩成 1 秒就看不到它了。
	deadline := time.Now().Add(20 * time.Second)
	score := ""
	for time.Now().Before(deadline) && score == "" {
		hitRight(cc)
		for k := 0; k < 8; k++ {
			p := cc.tryRecvTimeout(rcv(200 * time.Millisecond))
			if p == nil {
				break
			}
			if p.Head.Ident == proto.SM_HEAR && strings.Contains(p.Body, guildA+":") {
				score = p.Body
			}
		}
	}
	if score == "" {
		log.Fatal("砍死对方后没收到行会战比分广播（SM_HEAR）")
	}
	// 第一段广播必然是 100 : 0（原版每次击杀 +100，ObjBase.pas:21030）
	if !strings.Contains(score, guildA+":100") || !strings.Contains(score, guildB+":0") {
		log.Fatalf("比分广播内容不对：%q（期望含 %s:100 与 %s:0）", score, guildA, guildB)
	}
	fmt.Printf("    比分广播：%s ✓\n", score)

	// 5) @ContestPoint 查积分
	sayCmd(cc, "@ContestPoint "+guildA)
	m := waitSysMsg(cc, "的得分为")
	if m == "" {
		log.Fatal("@ContestPoint 没有回积分")
	}
	fmt.Printf("    @ContestPoint：%s\n", m)
	// ⚠️ 只要求 ≥100：上面那段循环在收到广播后可能又多挥了两刀，
	// 让对方在战场里第二次阵亡 ⇒ 积分会继续涨（那本身也是正确行为）。
	if n := contestPointOf(m); n < 100 {
		log.Fatalf("积分应 ≥100，实际 %d（%q）", n, m)
	}
	fmt.Println("    行会争霸赛积分链路已验证 ✓")
}

// contestPointOf 从 "X 的得分为: N" 里取 N（取不到返回 -1）。
func contestPointOf(msg string) int {
	i := strings.LastIndex(msg, ": ")
	if i < 0 {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(msg[i+2:]))
	if err != nil {
		return -1
	}
	return n
}

// contestFieldPos 记录争霸赛用例把角色传送到哪，供后续走路断言复用。
var contestFieldPosX, contestFieldPosY int

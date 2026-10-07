package main

import (
	"fmt"
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

// 施毒术(6) 与 心灵启示(28) 的端到端用例（两个都是 1.76 道士技能）。
//
// 断言分三层，都要求**确定性**：
//
//	① 心灵启示：客户端直接收到 `SM_OPENHEALTH`(1100)。
//	   原版 `SendRefMsg(RM_OPENHEALTH, 0, HP, MaxHP, 0, '')`（ObjBase.pas:3611），
//	   客户端 `ClMain.pas:4283` 按 `Recog=ActorId, Param=HP, Tag=MaxHP` 开血条。
//	   （项目文档长期写着"心灵启示依赖客户端血条协议、做了没法验证" —— 那是错的：
//	    协议早就在，客户端也早有处理，这里就是它的断言口。）
//
//	② 施毒术的绿毒 DoT：怪物会**掉血**（`SM_STRUCK`），服务端另有
//	   "中了绿毒"/"受到毒伤"/"被毒死" 三条日志（e2e 的 check 查它们）。
//
//	③ 毒药被消耗：`灰色药粉`(Shape=1 绿毒) / `黄色药粉`(Shape=2 红毒)，
//	   服务端日志 "消耗 灰色药粉…" 可查（原版没有这条日志，是我们补的可观测点）。
//
// 红毒的"受伤 ×1.2"不好在客户端观测（要控制伤害数值），由单测
// `TestAdjustStruckRedPoison` 钉住 `struckMul/adjustStruck`。
func runPoisonSpells(cc *conn, actorID int32, startX, startY int) (int, int) {
	fmt.Println("\n[毒/启示] 测试施毒术(6) 与 心灵启示(28)")
	posX, posY := startX, startY

	say := func(cmd string) {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
	}
	gm := func(cmd string, wait time.Duration) {
		say(cmd)
		drainUntilQuiet(cc, wait)
	}

	// 两招都要求道士，等级 14/26 起；e2e 用 -grant-all-magics 给技能、@magic 提级。
	// ⚠️ 0 级也能放（原版只看 btLevel，0 级照样生效），提级只是为了公式里的等级项。
	gm("@magic 6 3", rcv(400*time.Millisecond))
	gm("@magic 28 3", rcv(400*time.Millisecond))

	// 刷几只鸡当靶子（一次 @spawn 往返，见 spawnManyMonsters 的说明）
	targets := spawnManyMonsters(cc, "鸡", 4)
	if len(targets) < 3 {
		log.Fatalf("@spawn 只出现 %d 只鸡，施毒术/心灵启示验不了（要 ≥3）", len(targets))
	}

	// ---- ① 心灵启示：给第一只开血条 ----
	cc.send(wire.Packet{Head: spellMsg(28, targets[0].x, targets[0].y)})
	opened := false
	deadline := time.Now().Add(rcv(4 * time.Second))
	for !opened && time.Now().Before(deadline) {
		rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_OPENHEALTH {
			fmt.Printf("    心灵启示 → SM_OPENHEALTH ActorId=%d HP=%d/%d ✓\n",
				rp.Head.Recog, rp.Head.Param, rp.Head.Tag)
			if uint32(rp.Head.Recog) != targets[0].id {
				log.Fatalf("血条的 ActorId=%d，期望那只鸡 %d", rp.Head.Recog, targets[0].id)
			}
			if rp.Head.Param == 0 || rp.Head.Tag == 0 {
				log.Fatalf("血条包的 HP/MaxHP 不该是 0（%d/%d）", rp.Head.Param, rp.Head.Tag)
			}
			opened = true
		}
	}
	if !opened {
		log.Fatal("心灵启示没有收到 SM_OPENHEALTH（血条没开）")
	}

	// ---- ② 施毒术·绿毒（灰色药粉 Shape=1）----
	if !grantEquip(cc, "灰色药粉(少量)") {
		log.Fatal("拿不到绿毒药（灰色药粉(少量)）")
	}
	cc.send(wire.Packet{Head: spellMsg(6, targets[1].x, targets[1].y)})
	poisoned := false
	deadline = time.Now().Add(rcv(5 * time.Second))
	for !poisoned && time.Now().Before(deadline) {
		rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_STRUCK && uint32(rp.Head.Recog) == targets[1].id {
			fmt.Printf("    绿毒 DoT → 鸡(%d) 掉血到 %d/%d ✓\n",
				targets[1].id, rp.Head.Param, rp.Head.Tag)
			poisoned = true
		}
	}
	if !poisoned {
		log.Fatal("施毒术后没看到目标掉血（绿毒的周期伤害没生效）")
	}

	// ---- ③ 施毒术·红毒（黄色药粉 Shape=2）----
	// 换一件毒药：`@give` 会进背包，穿上去会把原来那件换回背包。
	if !grantEquip(cc, "黄色药粉(少量)") {
		log.Fatal("拿不到红毒药（黄色药粉(少量)）")
	}
	gm("@magic 6 3", rcv(400*time.Millisecond)) // 补蓝也顺带清冷却
	cc.send(wire.Packet{Head: spellMsg(6, targets[2].x, targets[2].y)})
	// 红毒只验"能命中"（服务端日志 `中了红毒`），伤害放大由单测钉住。
	drainUntilQuiet(cc, rcv(1200*time.Millisecond))
	fmt.Println("    红毒已施放（×1.2 受伤由单测 TestAdjustStruckRedPoison 钉住）✓")

	fmt.Println("    施毒术/心灵启示链路已验证 ✓")
	return posX, posY
}

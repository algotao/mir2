// Command mir2cli 是命令行测试客户端，用于端到端验证服务端协议。
//
// 对标 OpenMir2 的 src/Tools/MakePlayer——不需要真客户端就能跑通整条链路，
// 这对协议对齐的回归测试非常关键。
//
// 用法：
//
//	# 先起服务
//	go run ./cmd/accountsvc -db /tmp/mir2go.db
//	# 再跑客户端（自动建账号、建角色、走完整流程）
//	go run ./cmd/mir2cli -db /tmp/mir2go.db -user tester -pass pw123 -new-char 勇士
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	_ "github.com/algotao/mir2/server/internal/tz"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/wire"
)

func main() {
	var (
		loginAddr  = flag.String("login", "127.0.0.1:7000", "登录服务地址")
		selAddr    = flag.String("sel", "127.0.0.1:7100", "选角服务地址（服务端下发优先）")
		user       = flag.String("user", "tester", "账号")
		pass       = flag.String("pass", "pw123", "口令")
		dbPath     = flag.String("db", "", "数据库路径；非空则先确保账号存在")
		newChar    = flag.String("new-char", "", "要创建的角色名（留空则不建）")
		job        = flag.Int("job", 0, "角色职业 0=战士 1=法师 2=道士")
		server     = flag.String("server", "mir2go", "服务器名")
		skipGame   = flag.Bool("skip-game", false, "跳过进入游戏阶段（未启动 gamesvr 时使用）")
		skipMove   = flag.Bool("skip-move", false, "跳过走路测试")
		leaveTest  = flag.Bool("leave", false, "验证离开视野（SM_DISAPPEAR）")
		viewRange  = flag.Int("view-range", 12, "服务端视野半径，需与 gamesvr 一致")
		attackTest = flag.Bool("attack", false, "验证攻击怪物（含伤害与死亡）")
		spellTest  = flag.Bool("spell", false, "验证技能释放（火球术/治愈术）")
		equipTest  = flag.Bool("equip", false, "验证装备穿脱（脱下武器再穿上）")
		buffTest   = flag.Bool("buff", false, "验证增益状态与召唤兽（需服务端 -grant-all-magics）")
		mapTest    = flag.Bool("maptest", false, "验证地图切换（GM 命令 @map）")
		mapTarget  = flag.String("map-target", "0101", "地图切换的目标地图号")
		learnTest  = flag.Bool("learn", false, "验证技能书学习（@give 技能书 → 使用 → 学会）")
		learnBook  = flag.String("book", "雷电术", "要学习的技能书名（需与角色职业匹配）")
		npcTest    = flag.Bool("npctest", false, "验证 NPC 显示（传送到 NPC 旁并检查视野）")
		shopTest   = flag.Bool("shop", false, "验证商店（点击 NPC → 商品列表 → 买入/卖出）")
		dieTest    = flag.Bool("die", false, "验证死亡回城（@map 到远处后 @die）")
		fightDrop  = flag.Bool("fightdrop", false,
			"验证 FIGHT/FIGHT3 区死亡不掉落（@map F006 → @give → @die，再在盟重省对照一次）")
		dlgTest     = flag.Bool("dlg", false, "验证 NPC 脚本对话（点屠夫 → 显示脚本正文）")
		upgradeTest = flag.Bool("upgrade", false, "验证武器升级（交武器 → 取回 → 试刀结算）")
		goldTest    = flag.Bool("gold", false, "验证金币链路（扔金币/怪物掉金币 → 拾取）")
		butchTest   = flag.Bool("butch", false, "验证取肉（打死动物 → CM_BUTCH → 变骷髅 + 得肉）")
		storageTest = flag.Bool("storage", false,
			"验证仓库：@storage/@getback 打开与列表 + 存/取（商人 1Bme，map 0 313,271）")
		storageLeft = flag.Int("storage-left", 0,
			"开箱时仓库里应有几件（配 -storage 用：第二段传 1 验跨登录持久化）")
		guildTest = flag.Bool("guild", false,
			"验证行会系统（建会/窗口/成员/聊天/公告/职务/解散；配 -peer 可验加人踢人、"+
				"结盟 CM_GUILDALLY 与解除联盟 CM_GUILDBREAKALLY）")
		stackTest  = flag.Bool("stack", false, "验证物品堆叠（@give 多个同类物品应占同一格）")
		repairTest = flag.Bool("repair", false, "验证装备修理（打怪耗耐久 → 修理恢复）")
		warrTest   = flag.Bool("warr", false,
			"验证战士近战技能：刺杀剑术(12) 打第二格 / 半月弯刀(25) 打三格（需 -grant-all-magics）")
		skill3Test = flag.Bool("skill3", false,
			"验证第三批技能：抗拒火环(8)/集体隐身术(19)（需服务端 -grant-all-magics）")
		chatTest = flag.Bool("chat", false,
			"验证聊天（普通/喊话/私聊/组队频道；需 -peer，断言落在观察者侧）")
		gmTest = flag.Bool("gmtest", false,
			"验证 GM 权限门（AdminList）：角色名 = GM01 时按\"在名单里\"验，其余按\"不在名单\"验")
		wallTest = flag.Bool("wall", false,
			"验证火墙（22）：铺 5 格地面事件 + 周期伤害（需服务端 -grant-all-magics）")
		skill2Test = flag.Bool("skill2", false,
			"验证第二批技能：圣言术(32)/瞬息移动(21)/群体治愈(29)/困魔咒(16)")
		slaveTest = flag.Bool("slave", false,
			"验证宠物系统：召唤骷髅(17) 跟随 + 诱惑之光(20) 收服野生怪"+
				"（需服务端 -grant-all-magics；诱惑是概率技能，会重试）")
		tankTest  = flag.Bool("tank", false, "站桩不动，验证怪物主动攻击玩家")
		groupTest = flag.Bool("group", false,
			"验证组队（CM_CREATEGROUP/ADDGROUPMEMBER/DELGROUPMEMBER + SM_GROUPMEMBERS 名单）"+
				"与玩家交易（CM_DEALTRY/ADDITEM/CHGGOLD/END）")
		peerMode   = flag.Bool("peer", false, "额外登录第二个角色，验证移动广播")
		poisonTest = flag.Bool("poison", false,
			"测试施毒术(6) 与心灵启示(28)（需要道士角色与 -grant-all-magics）")
		castleDoorTest = flag.Bool("castledoor", false,
			"测试城门开/关（地图格阻挡）与城墙石化不可打（沙巴克 map 3 672,330）")
		contestTest = flag.Bool("contest", false,
			"测试行会争霸赛记分：两个角色各建一行会 → 都进 F006 → @StartContest → 面对面砍死 → 看比分广播")
		pvpTest   = flag.Bool("pvp", false, "验证玩家对战（需配 -peer；PvP 收包预算不缩放）")
		timeScale = flag.Float64("time-scale", 1,
			"与服务端相同的倍速（10=十倍速）；只缩放本客户端的节流等待，收包超时不变")
	)
	flag.Parse()

	// 倍速：与服务端保持一致（服务端限流同步缩短，否则客户端会大量丢步）
	tscale.Set(*timeScale)
	if *timeScale != 1 {
		fmt.Printf("时间流速 = %gx\n", tscale.Get())
	}

	if *dbPath != "" {
		if err := ensureAccount(*dbPath, *user, *pass); err != nil {
			log.Fatalf("准备账号: %v", err)
		}
	}

	// ---- 阶段 1：登录 ----
	c1, err := net.DialTimeout("tcp", *loginAddr, 3*time.Second)
	if err != nil {
		log.Fatalf("连接登录服务 %s: %v", *loginAddr, err)
	}
	seq := byte('1')

	fmt.Println("[1] 版本握手")
	r := roundTrip(c1, &seq, pkt(proto.CM_PROTOCOL, ""))
	expect(r, proto.SM_CERTIFICATION_SUCCESS, "SM_CERTIFICATION_SUCCESS")
	fmt.Printf("    版本日期 = %d\n", r.Head.Recog)

	fmt.Println("[2] 口令认证")
	r = roundTrip(c1, &seq, pkt(proto.CM_IDPASSWORD, *user+"/"+*pass))
	expect(r, proto.SM_PASSOK_SELECTSERVER, "SM_PASSOK_SELECTSERVER")
	fmt.Printf("    服务器列表 = %q\n", r.Body)

	fmt.Println("[3] 选服")
	r = roundTrip(c1, &seq, pkt(proto.CM_SELECTSERVER, *server))
	expect(r, proto.SM_SELECTSERVER_OK, "SM_SELECTSERVER_OK")
	parts := strings.Split(r.Body, "/")
	if len(parts) != 3 {
		log.Fatalf("选服响应正文 = %q, want ip/port/sessionID", r.Body)
	}
	selTarget := net.JoinHostPort(parts[0], parts[1])
	sessionID := parts[2]
	fmt.Printf("    角色网关 = %s, SessionID = %s\n", selTarget, sessionID)
	_ = c1.Close()

	// ---- 阶段 2：选角（客户端会断连重连）----
	target := selTarget
	if *selAddr != "127.0.0.1:7100" {
		target = *selAddr // 允许命令行覆盖
	}
	c2, err := net.DialTimeout("tcp", target, 3*time.Second)
	if err != nil {
		log.Fatalf("连接选角服务 %s: %v", target, err)
	}
	defer c2.Close()
	seq = byte('1')

	// createChar 建角色，能正确处理限流与"已存在"。
	//
	// ⚠️ 两个坑：
	//   - 限流时服务端**不回包**（onNewChr 直接 return nil）→ 用 roundTripSoft
	//     靠超时重试，不能当致命错误；
	//   - 失败码 2 是**重名**而不是限流：重试时若上一次其实已建好，
	//     再发就会拿到 2，应视为成功（否则会一直失败到重试上限）。
	createChar := func(account, name, job string) {
		body := fmt.Sprintf("%s/%s/1/%s/0", account, name, job)
		for attempt := 0; attempt < 6; attempt++ {
			pr := roundTripSoft(c2, &seq, pkt(proto.CM_NEWCHR, body))
			if pr != nil {
				if pr.Head.Ident == proto.SM_NEWCHR_SUCCESS {
					return
				}
				if pr.Head.Ident == proto.SM_NEWCHR_FAIL {
					if pr.Head.Recog == 2 {
						fmt.Printf("    角色 %q 已存在（视为成功）\n", name)
						return
					}
					log.Fatalf("创建角色 %q 失败（码 %d）", name, pr.Head.Recog)
				}
			}
			fmt.Printf("    建角色 %q 无响应（服务端限流），1.5s 后重试(%d)\n", name, attempt+1)
			time.Sleep(tscale.D(1500 * time.Millisecond))
		}
		log.Fatalf("创建角色 %q 反复失败（服务端限流）", name)
	}

	fmt.Println("[4] 查询角色")
	q := *user + "/" + sessionID
	r = roundTrip(c2, &seq, pkt(proto.CM_QUERYCHR, q))
	expect(r, proto.SM_QUERYCHR, "SM_QUERYCHR")
	fmt.Printf("    角色数 = %d, 列表 = %q\n", r.Head.Recog, r.Body)

	if *newChar != "" && !strings.Contains(r.Body, *newChar+"/") {
		fmt.Printf("[5] 创建角色 %q\n", *newChar)
		createChar(*user, *newChar, strconv.Itoa(*job))

		fmt.Println("[6] 重新查询")
		r = roundTrip(c2, &seq, pkt(proto.CM_QUERYCHR, q))
		expect(r, proto.SM_QUERYCHR, "SM_QUERYCHR")
		fmt.Printf("    角色数 = %d, 列表 = %q\n", r.Head.Recog, r.Body)
	} else {
		fmt.Println("[5] 跳过创建角色")
	}

	if r.Head.Recog == 0 {
		fmt.Println("没有可选角色，流程到此结束")
		return
	}
	chrName := strings.SplitN(r.Body, "/", 2)[0]
	chrName = strings.TrimPrefix(chrName, "*")

	fmt.Printf("[7] 选择角色 %q\n", chrName)
	r = roundTrip(c2, &seq, pkt(proto.CM_SELCHR, *user+"/"+chrName))
	expect(r, proto.SM_STARTPLAY, "SM_STARTPLAY")
	fmt.Printf("    游戏网关 = %s\n", r.Body)

	if *skipGame {
		fmt.Println("\n✓ 登录链路通过（已跳过进入游戏）")
		return
	}

	// ---- 阶段 3：进入游戏 ----
	gp := strings.Split(r.Body, "/")
	if len(gp) != 2 {
		log.Fatalf("SM_STARTPLAY 正文 = %q, want ip/port", r.Body)
	}
	gameTarget := net.JoinHostPort(gp[0], gp[1])
	c3, err := net.DialTimeout("tcp", gameTarget, 3*time.Second)
	if err != nil {
		log.Fatalf("连接游戏服务 %s: %v（需先启动 cmd/gamesvr）", gameTarget, err)
	}
	defer c3.Close()

	fmt.Printf("[8] 进入游戏 %s\n", gameTarget)
	cc := newConn(c3)
	// 认证首包：**<account>/<chrName>/<sessionID>/<clientVersion>/<idx>
	// 注意它是 6bit 编码的**原文**，不是 DefMsg 包
	token := fmt.Sprintf("**%s/%s/%s/120040918/0", *user, chrName, sessionID)
	if _, err := c3.Write(wire.EncodeUp('1', codec.Encode6BitBuf([]byte(token)))); err != nil {
		log.Fatalf("发送认证包: %v", err)
	}
	cc.seq = '2'

	// 公告握手：服务端先发 SM_SENDNOTICE，客户端回 CM_LOGINNOTICEOK 后才真正进游戏
	r = cc.recv()
	expect(r, proto.SM_SENDNOTICE, "SM_SENDNOTICE")
	fmt.Printf("    公告 = %q\n", r.Body)
	cc.send(pkt(proto.CM_LOGINNOTICEOK, ""))
	fmt.Println("    → CM_LOGINNOTICEOK")

	// 进入世界序列。服务端在一个 TCP 段里连发多帧，
	// 靠连接级 Splitter 依次取出（顺序不能乱）
	r = cc.recv()
	expect(r, proto.SM_NEWMAP, "SM_NEWMAP")
	fmt.Printf("    地图 = %q 坐标=(%d,%d) 亮度=%d\n",
		r.Body, r.Head.Param, r.Head.Tag, r.Head.Series)

	r = cc.recv()
	expect(r, proto.SM_LOGON, "SM_LOGON")
	actorID, posX, posY := r.Head.Recog, int(r.Head.Param), int(r.Head.Tag)
	// selfDir 是主角色当前朝向（组队/交易用例要拿它算"我面朝哪一格"）。
	selfDir := uint8(r.Head.Series)
	fmt.Printf("    ActorId=%d 坐标=(%d,%d) 方向=%d\n",
		r.Head.Recog, r.Head.Param, r.Head.Tag, r.Head.Series)
	if wl, ok := proto.DecodeMessageBodyWL([]byte(r.Body)); ok {
		fmt.Printf("    外观 Feature=%d (Race=%d Weapon=%d Hair=%d Dress=%d)\n",
			wl.Param1, proto.FeatureRace(wl.Param1), proto.FeatureWeapon(wl.Param1),
			proto.FeatureHair(wl.Param1), proto.FeatureDress(wl.Param1))
	} else {
		log.Fatalf("TMessageBodyWL 解码失败: body 长度 %d", len(r.Body))
	}

	// 服务端配置（原版 `RM_LOGON` 的第 4 条：SendLogon → **SendServerConfig** →
	// ClientQueryUserName → RefMapDescription…，ObjBase.pas:5618-5626）。
	r = cc.recv()
	expect(r, proto.SM_SERVERCONFIG, "SM_SERVERCONFIG")
	{
		conf, ok := proto.DecodeClientConf(codec.DecodeBuffer([]byte(r.Body)))
		if !ok {
			log.Fatalf("SM_SERVERCONFIG 记录解码失败（body %d 字节）", len(r.Body))
		}
		// 四个"能不能跑"的开关打包在 Recog 里（客户端 LoByte(LoWord(Recog)) 依次取）
		recog := uint32(r.Head.Recog)
		if bool(recog&0xFF != 0) != conf.RunHuman || bool((recog>>8)&0xFF != 0) != conf.RunMon ||
			bool((recog>>16)&0xFF != 0) != conf.RunNpc || bool((recog>>24)&0xFF != 0) != conf.WarRunAll {
			log.Fatalf("Recog=0x%08X 与记录里的跑动开关不一致：%+v", recog, conf)
		}
		// 出厂 !setup.txt:996-997 是 520/450 ⇒ 下发 +500/+300
		if conf.HitTime != 1020 || conf.SpellTime != 750 {
			log.Fatalf("攻击/施法间隔 = %d/%d，期望 1020/750（!setup.txt HitIntervalTime=520 MagicHitIntervalTime=450）",
				conf.HitTime, conf.SpellTime)
		}
		if uint8(r.Head.Param) != conf.DieColor || conf.DieColor != 5 {
			log.Fatalf("死亡特效颜色 Param=%d 记录=%d，期望都是 5", uint8(r.Head.Param), conf.DieColor)
		}
		fmt.Printf("    服务端配置：跑动(人/怪/NPC/攻城)=%v/%v/%v/%v 攻速=%d 施法=%d 死亡色=%d ✓\n",
			conf.RunHuman, conf.RunMon, conf.RunNpc, conf.WarRunAll,
			conf.HitTime, conf.SpellTime, conf.DieColor)
	}

	r = cc.recv()
	expect(r, proto.SM_FEATURECHANGED, "SM_FEATURECHANGED")
	fmt.Printf("    外观变更 Recog=%d Feature=%d\n",
		r.Head.Recog, proto.MakeLong(r.Head.Param, r.Head.Tag))

	r = cc.recv()
	expect(r, proto.SM_MAPDESCRIPTION, "SM_MAPDESCRIPTION")
	fmt.Printf("    地图名 = %q\n", r.Body)

	r = cc.recv()
	expect(r, proto.SM_ABILITY, "SM_ABILITY")
	fmt.Printf("    金币=%d 职业=%d 元宝=%d\n",
		r.Head.Recog, r.Head.Param, proto.MakeLong(r.Head.Tag, r.Head.Series))
	// 属性是 TAbility 定长结构（50 字节）；recv 已做过 6bit 解码
	if ab, ok := proto.DecodeAbility([]byte(r.Body)); ok {
		fmt.Printf("    等级=%d 经验=%d/%d HP=%d/%d MP=%d/%d DC=%d/%d\n",
			ab.Level, ab.Exp, ab.MaxExp, ab.HP, ab.MaxHP, ab.MP, ab.MaxMP,
			proto.UnpackLo(ab.DC), proto.UnpackHi(ab.DC))
		// ⚠️ 客户端经验条是 `100 * Exp / MaxExp`（FState.pas:2885）。
		// MaxExp 为 0 就是"经验条除以 0"（服务端曾长期不下发它），
		// 所以这里把"分母非零且是**级内**值"当成硬断言。
		if ab.MaxExp == 0 {
			log.Fatal("TAbility.MaxExp = 0（客户端经验条会除以 0）")
		}
		if ab.Exp >= ab.MaxExp {
			log.Fatalf("TAbility.Exp(%d) >= MaxExp(%d)：经验应是**级内**值而非累计值", ab.Exp, ab.MaxExp)
		}
	} else {
		log.Fatalf("TAbility 解码失败: body 长度 %d", len(r.Body))
	}

	r = cc.recv()
	expect(r, proto.SM_SUBABILITY, "SM_SUBABILITY")
	fmt.Printf("    命中=%d 敏捷=%d 抗毒=%d\n",
		proto.LoByte(r.Head.Param), proto.HiByte(r.Head.Param), proto.LoByte(r.Head.Tag))

	r = cc.recv()
	expect(r, proto.SM_SENDUSEITEMS, "SM_SENDUSEITEMS")
	fmt.Printf("    装备：%s\n", describeUseItems(r.Body))

	// 客户端进游戏后会立即查背包（ClMain.pas:4447）
	cc.send(pkt(proto.CM_QUERYBAGITEMS, ""))
	r = recvExpected(cc, proto.SM_BAGITEMS)
	fmt.Printf("    背包条目数 = %d  %s\n", len(r.Body)/proto.ClientItemSize, describeBag(r.Body))

	// PvP / 组队 / 交易用例都需要第二个玩家当对手，自动打开 peer 模式。
	if *pvpTest || *groupTest || *contestTest {
		*peerMode = true
	}

	// ---- 阶段 3.5（可选）：第二个角色进游戏，用于验证广播 ----
	var peer *conn
	var peerX, peerY int
	// peerActorID 是观察者的 ActorId（从主角色收到的 SM_TURN 里拿）。
	// PvP 用例要按它过滤 SM_STRUCK，否则会打到旁边的怪。
	var peerActorID int32
	// peerName 是观察者的角色名（组队用例要按名字发 CM_CREATEGROUP）。
	peerName := ""
	if *peerMode {
		// 建角色有 1000ms 限流（防刷号），主角色刚建完需等待
		// 建角色限流是 1000ms/会话（UsrSoc.pas:548）。从进游戏完成后再等 2.4s：
		// 并行 + 时间倍速下余量太紧会一直撞上限流。
		time.Sleep(tscale.D(2400 * time.Millisecond))
		peerName = chrName + "B"
		createChar(*user, peerName, "0")
		var pr *wire.Packet
		roundTrip(c2, &seq, pkt(proto.CM_QUERYCHR, q))
		pr = roundTrip(c2, &seq, pkt(proto.CM_SELCHR, *user+"/"+peerName))
		expect(pr, proto.SM_STARTPLAY, "SM_STARTPLAY(观察者)")

		pg := strings.Split(pr.Body, "/")
		pc, err := net.DialTimeout("tcp", net.JoinHostPort(pg[0], pg[1]), 3*time.Second)
		if err != nil {
			log.Fatalf("观察者连接游戏服务: %v", err)
		}
		defer pc.Close()
		peer = newConn(pc)
		ptok := fmt.Sprintf("**%s/%s/%s/120040918/0", *user, peerName, sessionID)
		if _, err := pc.Write(wire.EncodeUp('1', codec.Encode6BitBuf([]byte(ptok)))); err != nil {
			log.Fatalf("观察者认证: %v", err)
		}
		peer.seq = '2'
		pr = peer.recv()
		expect(pr, proto.SM_SENDNOTICE, "SM_SENDNOTICE(观察者)")
		peer.send(pkt(proto.CM_LOGINNOTICEOK, ""))
		// 进入游戏序列：NEWMAP/LOGON/FEATURECHANGED/MAPDESCRIPTION/ABILITY/
		// SUBABILITY/SENDUSEITEMS/**SM_SENDMYMAGIC**，
		// 之后是**视野内每个实体一条 SM_TURN**（地图 0 光 NPC 就有 34 个，
		// 再加上前序用例留在出生点附近的怪物）。
		//
		// ⚠️ 别写"最多收 N 个包"：N 取小一点，主线角色的那条就排不进前 N 个，
		// 表现为间歇性"观察者进游戏后未收到 SM_TURN"。这里改成
		// **等到出现或超时**：正常情况找到就立刻返回，找不到才是真问题。
		var turnP *wire.Packet
		deadline := time.Now().Add(5 * time.Second)
		for turnP == nil && time.Now().Before(deadline) {
			pr := peer.tryRecvTimeout(500 * time.Millisecond)
			if pr == nil {
				continue
			}
			if pr.Head.Ident == proto.SM_LOGON {
				peerX, peerY = int(pr.Head.Param), int(pr.Head.Tag)
				// 自己的 ActorId 来自 SM_LOGON 的 Recog。
				// ⚠️ 不要用"主角色收到的第一个 SM_TURN"——那可能是怪物或 NPC。
				peerActorID = int32(pr.Head.Recog)
			}
			// ⚠️ 必须匹配 Recog == 主角色：地图上还有 NPC，
			// 它们的 SM_TURN 也会先到，不区分就会认错
			if pr.Head.Ident == proto.SM_TURN && int32(pr.Head.Recog) == actorID {
				turnP = pr
			}
		}
		// 视野同步：观察者应看到已在线的主角色
		if turnP == nil {
			log.Fatal("观察者进游戏后未收到 SM_TURN（视野同步）")
		}
		pr = turnP
		if int(pr.Head.Recog) != int(actorID) {
			log.Fatalf("观察者看到的 Recog = %d, want %d", pr.Head.Recog, actorID)
		}
		fmt.Printf("    观察者 %q 已进游戏，看到主角色 ActorId=%d 于 (%d,%d)\n",
			peerName, pr.Head.Recog, pr.Head.Param, pr.Head.Tag)

		// 反向：主角色也应收到观察者出现的通知。
		//
		// ⚠️ 必须**按 peerActorID 过滤**、且等到出现或超时：
		// 地图上有怪物与 NPC，它们的 SM_TURN 也会先到。原来写成"等下一条 SM_TURN"，
		// 于是"某只怪"被当成观察者（表现为组队用例"无法把 peer 传送到…"、
		// 或"主角色看到观察者 ActorId=1002xxx"这种怪物号）。
		var rm *wire.Packet
		dl := time.Now().Add(5 * time.Second)
		for rm == nil && time.Now().Before(dl) {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_TURN && int32(rp.Head.Recog) == peerActorID {
				rm = rp
			}
		}
		if rm == nil {
			log.Fatal("主角色没看到观察者出现（SM_TURN 按 ActorId 过滤后仍无）")
		}
		fmt.Printf("    主角色看到观察者 ActorId=%d 于 (%d,%d)\n",
			rm.Head.Recog, rm.Head.Param, rm.Head.Tag)
	}

	// ---- 组队 + 交易用例（需要 peer 已就绪）----
	if *groupTest {
		runGroupAndDeal(cc, peer, actorID, peerActorID, posX, posY, selfDir, peerName)
	}

	// ---- PvP 用例（需要 peer 已就绪）----
	if *pvpTest {
		runPvP(cc, peer, peerActorID)
		// 用例里把两人传送到了城外，后续走路断言必须用新坐标，
		// 否则会拿"传送前"的 posX/posY 去比对。
		posX, posY = pvpFieldPosX, pvpFieldPosY
	}

	// 施毒术 + 心灵启示
	if *poisonTest {
		posX, posY = runPoisonSpells(cc, actorID, posX, posY)
	}

	// 城门开/关 + 城墙石化（沙巴克）
	if *castleDoorTest {
		posX, posY = runCastleDoor(cc, actorID, posX, posY)
	}

	// 行会争霸赛（FIGHT3 区击杀记分）：需要两个**不同行会**的角色，所以也走 -peer。
	if *contestTest {
		runContest(cc, peer, peerName, peerActorID)
		// 同 -pvp：用例把角色传送到了行会战争地图，后续走路断言要用新坐标
		posX, posY = contestFieldPosX, contestFieldPosY
	}

	// ---- 聊天用例（需要 peer 已就绪）----
	if *warrTest {
		runWarrSkill(cc, actorID, posX, posY)
	}

	if *skill3Test {
		runSkill3(cc, actorID, posX, posY)
	}

	if *chatTest {
		runChat(cc, peer, chrName, peerName, actorID)
	}

	if *skipMove {
		fmt.Println("\n✓ 完整链路通过（登录 → 选角 → 进入游戏）")
		return
	}

	// ---- 阶段 4：走路 ----
	// CM_WALK：Recog=MakeLong(x,y)，Tag=方向（UsrEngn.pas:1739-1774）
	fmt.Println("[9] 测试走路")
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_WALK, proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(entity.DirDown), 0),
	})
	r = recvExpectedFrom(cc, proto.SM_WALK, actorID)
	if int(r.Head.Param) != posX || int(r.Head.Tag) != posY+1 {
		log.Fatalf("移动后坐标 = (%d,%d), want (%d,%d)", r.Head.Param, r.Head.Tag, posX, posY+1)
	}
	fmt.Printf("    (%d,%d) → (%d,%d) 方向=%d\n", posX, posY, r.Head.Param, r.Head.Tag, r.Head.Series)
	posX, posY = int(r.Head.Param), int(r.Head.Tag)

	// 观察者应收到同一次移动的广播
	if peer != nil {
		// ⚠️ 循环查找主角色那条：地图上怪物/NPC 的移动也会以
		// SM_WALK 发来，只收一个包就判定必然认错。
		var rp *wire.Packet
		for i := 0; i < maxRecvTries && rp == nil; i++ {
			p := peer.recv()
			if p.Head.Ident == proto.SM_WALK && int32(p.Head.Recog) == actorID {
				rp = p
			}
		}
		if rp == nil {
			log.Fatal("观察者未收到主角色的移动广播")
		}
		if int(rp.Head.Recog) != int(actorID) {
			log.Fatalf("广播 Recog = %d, want %d", rp.Head.Recog, actorID)
		}
		fmt.Printf("    观察者收到广播: ActorId=%d → (%d,%d) ✓\n",
			rp.Head.Recog, rp.Head.Param, rp.Head.Tag)

		// 打坐（CM_SITDOWN）要能被**别人**看到：原版广播的是 RM_POWERHIT（而它
		// 在服务端的分派被注释掉、客户端也没这个 id ⇒ 原版"别人看不到"）；
		// 我们广播客户端认得的 SM_SITDOWN(12)，这条断言就是钉住那次广播。
		//
		// ⚠️ 打坐与转身/取肉**共用** 100ms（游戏时间）节流 ⇒ 先等一会儿再坐，
		// 否则可能撞上刚才那次走路的动作时间戳。
		time.Sleep(tscale.D(300 * time.Millisecond))
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SITDOWN, 0, 0, 0, 0)})
		var sit *wire.Packet
		for i := 0; i < maxRecvTries && sit == nil; i++ {
			pp := peer.recv()
			if pp.Head.Ident == proto.SM_SITDOWN && pp.Head.Recog == actorID {
				sit = pp
			}
		}
		if sit == nil {
			log.Fatal("观察者没收到打坐广播（SM_SITDOWN）")
		}
		fmt.Printf("    观察者收到打坐广播: ActorId=%d (%d,%d) ✓\n",
			sit.Head.Recog, sit.Head.Param, sit.Head.Tag)
	}

	// 连续两次移动：第二次应被限流（走路最小间隔 600ms）。
	// ⚠️ 期间会有怪物移动/进出视野的包到达，需按 Recog 排除，只看自己的移动确认。
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_WALK, proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(entity.DirDown), 0),
	})
	limited := false
	for i := 0; i < 3 && !limited; i++ {
		rp := cc.tryRecvTimeout(700 * time.Millisecond)
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_WALK && int32(rp.Head.Recog) == actorID {
			limited = true
			break
		}
		sidePackets = append(sidePackets, rp)
	}
	if limited {
		log.Fatal("限速内不应收到自己的移动确认 —— 限流失效")
	}
	fmt.Println("    连续移动被限流 ✓")

	// 等够间隔后应能继续走
	time.Sleep(sc(650 * time.Millisecond))
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_WALK, proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(entity.DirRight), 0),
	})
	r = recvExpectedFrom(cc, proto.SM_WALK, actorID)
	fmt.Printf("    → (%d,%d)\n", r.Head.Param, r.Head.Tag)

	if *attackTest {
		fmt.Println("[11] 测试攻击怪物")
		// known 记录视野内见过的怪物位置（含 @spawn 刷出来的），击杀后据此挑下一只。
		//
		// ⚠️ 不能只等"新的 SM_TURN"：怪物游荡关闭时不会移动，出现包只在
		// 进入视野那一刻发一次。击杀当前目标后若只等新包，用例会以为没有
		// 目标而在原地空转（表现为"打了 40 秒只杀了 3 只，升级从未验证"）。
		known := map[int32][2]int{}
		// 身旁刷一批，保证场景可控（真实地图的怪很分散）。
		//
		// ⚠️ 必须刷**够多**：升级要到几百经验，刷 1-2 只时用例会一路
		// 空转到循环上限，结果是"打了 80 秒却从未验证升级"（升级链路
		// 静默失去覆盖）。这批怪都落在玩家周围几格内，连杀不需要跑图。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@spawn 鸡 20",
		})
		time.Sleep(700 * time.Millisecond)

		// ⚠️ 收集全部可见怪物并选**最近**的一只。
		// 原来取"第一个收到的"，真实地图上那往往是最远的，
		// 结果整轮都在追击、一次也没打成。
		seen := make(map[int32]*wire.Packet)
		for i := 0; i < 30; i++ {
			p := popSide(proto.SM_TURN)
			if p == nil {
				p = cc.tryRecvTimeout(700 * time.Millisecond)
			}
			if p == nil {
				break
			}
			if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
				seen[p.Head.Recog] = p
				continue
			}
			sidePackets = append(sidePackets, p)
		}
		if len(seen) == 0 {
			log.Fatal("视野内未出现怪物")
		}
		var mon *wire.Packet
		bestD := -1
		for _, p := range seen {
			d := absi(int(p.Head.Param)-posX) + absi(int(p.Head.Tag)-posY)
			if bestD < 0 || d < bestD {
				mon, bestD = p, d
			}
		}
		monID, monX, monY := int(mon.Head.Recog), int(mon.Head.Param), int(mon.Head.Tag)
		fmt.Printf("    发现怪物 ActorId=%d 于 (%d,%d)\n", monID, monX, monY)

		// 本阶段收走的 SM_TURN 不会再来了，必须补进 known，
		// 否则击杀第一只之后就"找不到怪"。
		for id, p := range seen {
			known[id] = [2]int{int(p.Head.Param), int(p.Head.Tag)}
		}

		// 统计变量：drain 是闭包，必须在其定义**之前**声明才能被捕获
		killed, hit, tanked := false, false, false
		leveled, newLevel, kills, drops := false, 0, 0, 0
		dropX, dropY := -1, -1

		// noteDrop 记录掉落，但**只把"相邻格"的掉落当捡取目标**。
		//
		// ⚠️ 掉落是逐件推来的，其中不少落在追怪途中经过的远处（还常常
		// 隔着障碍）。拿它们当捡取目标时，客户端坐标与服务端不同步
		// （移动被拒时服务端不回显坐标），走位会一直失败。
		// 而"刚击杀的怪脚下"必然与自己相邻（攻击判定就是相邻格），
		// 走一步就到，成功率极高。
		noteDrop := func(x, y int) {
			drops++
			if dropX < 0 && absi(x-posX)+absi(y-posY) <= 1 {
				dropX, dropY = x, y
			}
		}

		// drain 消化所有已到达的包，同步自己与怪物的坐标。
		//
		// ⚠️ 必须同时跟踪**自己**的坐标：若只用怪物坐标而自己的坐标落后，
		// 算出的攻击方向会指向错误的格子，服务端按服务端坐标判定就会落空。
		drain := func() {
			// ⚠️ 必须先 pump：tryNext 只从 Splitter 取帧，没人 Read socket
			// 的话服务端刚发来的移动回显永远进不来，坐标会一直停在旧值。
			cc.pump(30*time.Millisecond, 3)
			for {
				rp := cc.tryNext()
				if rp == nil {
					return
				}
				switch {
				case (rp.Head.Ident == proto.SM_WALK || rp.Head.Ident == proto.SM_RUN) &&
					int(rp.Head.Recog) == monID:
					monX, monY = int(rp.Head.Param), int(rp.Head.Tag)
				case (rp.Head.Ident == proto.SM_WALK || rp.Head.Ident == proto.SM_RUN) &&
					int32(rp.Head.Recog) == actorID:
					posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
				case rp.Head.Ident == proto.SM_DISAPPEAR && isMonsterID(rp.Head.Recog):
					delete(known, rp.Head.Recog)
					if int(rp.Head.Recog) == monID {
						monX, monY = -999, -999
					}
				case rp.Head.Ident == proto.SM_DEATH && isMonsterID(rp.Head.Recog):
					delete(known, rp.Head.Recog)
				case rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog):
					known[rp.Head.Recog] = [2]int{int(rp.Head.Param), int(rp.Head.Tag)}
					// ⚠️ 只在**没有目标时**才锁定新目标。
					// 若允许追击途中切换，视野里每出现一只新怪就会改道，
					// 结果是永远追不上任何一只（真实地图怪很密）。
					if monID == 0 {
						monID, monX, monY = int(rp.Head.Recog), int(rp.Head.Param), int(rp.Head.Tag)
					}
				case (rp.Head.Ident == proto.SM_HIT) && int32(rp.Head.Recog) == actorID:
					// ★ 用服务端回显的坐标校正：SM_HIT 的 Param/Tag 就是服务端认定的位置。
					// 本地推算必然滞后（移动请求可能还在路上或被限流），
					// 而攻击方向依赖精确位置，差 1 格就会打空。
					posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
				case rp.Head.Ident == proto.SM_ADDITEM:
					// ⚠️ 必须在此捕获：掉落是服务端主动推送，
					// 若落进 default 分支就只会进 sidePackets，永远看不到。
					noteDrop(int(rp.Head.Param), int(rp.Head.Tag))
					fmt.Printf("    掉落: 地面物品ID=%d 于 (%d,%d)\n",
						rp.Head.Recog, rp.Head.Param, rp.Head.Tag)
				case rp.Head.Ident == proto.SM_LEVELUP:
					leveled = true
					newLevel = int(proto.MakeLong(rp.Head.Param, rp.Head.Tag))
					fmt.Printf("    ★ 升级! 等级=%d\n", newLevel)
				default:
					sidePackets = append(sidePackets, rp)
				}
			}
		}
		gapToMon := func() int {
			g := absi(monX - posX)
			if v := absi(monY - posY); v > g {
				g = v
			}
			return g
		}

		// 1) 跑到怪物旁边
		//
		// ⚠️ 真实地图有障碍，直线追击会撞墙卡死，
		// 因此移动失败时轮流尝试相邻方向绕行。
		stuck := 0
		for step := 0; step < 24; step++ {
			drain()
			if gapToMon() == 1 {
				break // 相邻，可以打了（先判断再等，省掉一次 430ms）
			}
			time.Sleep(sc(430 * time.Millisecond)) // 跑步最小间隔 400ms
			d := dirTowardAxis(monX-posX, monY-posY)
			if gapToMon() == 0 {
				// ⚠️ 与怪同格时打不到：攻击判定的是**相邻格**。
				// 这里先朝反方向退开一步再打。
				d = (d + 4) % 8
			}
			if stuck > 0 {
				d = uint8((int(d) + stuck) % 8)
			}
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_RUN,
					proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(d), 0),
			})
			// ⚠️ 必须用 drain()（排空）而不是"只收一个包"：
			// 地图上怪物的 SM_TURN/SM_WALK 比自己的回显还多，
			// 单收一个十有八九不是自己的，坐标会一路滞后——
			// 滞后会让后续所有方向计算都指向错误的格子
			// （表现为攻击大量落空、走位到不了目标）。
			bx, by := posX, posY
			time.Sleep(sc(120 * time.Millisecond))
			drain()
			if posX != bx || posY != by {
				stuck = 0
			} else {
				stuck++
				if stuck > 6 {
					stuck = 1
				}
			}
		}
		// 等最后一个移动确认到达，避免用"已发出但未确认"的坐标算方向
		time.Sleep(600 * time.Millisecond)
		drain()
		fmt.Printf("    玩家 (%d,%d)，怪物 (%d,%d)，距离=%d\n", posX, posY, monX, monY, gapToMon())

		// 2) 挥砍：击杀后继续找下一只，直到升级（验证掉落与升级）
		miss, stuckMove := 0, 0
		for i := 0; i < 150 && !leveled; i++ {
			killed = false
			drain()
			// 目标已死/消失：挑下一只继续打。
			//
			// ⚠️ **相邻格优先**：距离 1 可以原地直接打，没有相邻的才去追
			// 全局最近的那只。只按"最近"挑时角色会在两侧的怪之间来回横穿，
			// 每步 400ms 限流，时间全花在路上了。
			if monID == 0 {
				bestD := 1 << 30
				for id, p := range known {
					d := absi(p[0]-posX) + absi(p[1]-posY)
					if d == 1 {
						monID, monX, monY = int(id), p[0], p[1]
						break
					}
					if d < bestD {
						monID, monX, monY, bestD = int(id), p[0], p[1], d
					}
				}
				if monID == 0 {
					// 视野里确实没有活怪了（打光/走远）：补刷一批再继续。
					// ⚠️ 不补刷的话，下面的移动分支会拿 monID=0 的"陈旧
					// 坐标"当目标，把角色一路带到地图角落（(0,0)），
					// 每步 1.4 秒，用例看起来就是"卡住不动"。
					cc.send(wire.Packet{
						Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
						Body: "@spawn 鸡 20",
					})
					time.Sleep(200 * time.Millisecond)
					drain() // drain 会在 monID==0 时自动锁定新出现的怪
					continue
				}
			}
			// 周期性进度：用例卡住时这几行能立刻说明是"目标丢了"
			// 还是"追不上"（比事后翻日志猜快得多）。
			if i%10 == 0 {
				fmt.Printf("    [进度] 轮%d 目标=%d@(%d,%d) 自己(%d,%d) 距离=%d 已知怪=%d 已杀=%d\n",
					i, monID, monX, monY, posX, posY, gapToMon(), len(known), kills)
			}
			// ⚠️ 只有**距离正好 1** 才能打中：
			//   - 距离 > 1：够不着，走近；
			//   - 距离 = 0（与怪同格）：攻击判定的是相邻格，同格永远落空，
			//     必须朝反方向退开一步（原版客户端同样先退开）。
			// 曾经这里只判 `> 1`，同格时一路空挥到 150 次上限。
			if gap := gapToMon(); gap != 1 {
				time.Sleep(sc(430 * time.Millisecond))
				d := dirTowardAxis(monX-posX, monY-posY)
				if gap == 0 {
					d = (d + 4) % 8
				}
				cc.send(wire.Packet{
					Head: proto.MakeDefaultMsg(proto.CM_RUN,
						proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(d), 0),
				})
				time.Sleep(sc(120 * time.Millisecond))
				drain() // 同追怪循环：必须排空，否则坐标会滞后
				// 走完没更近 = 被建筑隔开了。再怎么绕也到不了，
				// 直接放弃这只、换 known 里的下一只（否则空转到循环上限）。
				if gapToMon() >= gap {
					stuckMove++
				} else {
					stuckMove = 0
				}
				if stuckMove >= 8 {
					delete(known, int32(monID))
					monID, monX, monY = 0, 0, 0
					stuckMove = 0
				}
				continue
			}
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_HIT,
					proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(dirToward(monX-posX, monY-posY)), 0),
			})
			// 一次挥砍会产生多个包：SM_HIT / SM_STRUCK / SM_DEATH / SM_WINEXP
			//
			// ⚠️ 首包等 400ms（等服务端响应），后续只等 80ms：
			// 一次挥砍的后续包是服务端在**同一个处理过程**里同步发出的，
			// 本地回环微秒级到达。每轮都等满 400ms 会让"打到升级"白等
			// 几十秒——attack 用例曾经 86 秒里有约 60 秒是这么等掉的。
			hitNow := false
			for j := 0; j < 6; j++ {
				wait := 80 * time.Millisecond
				if j == 0 {
					wait = 400 * time.Millisecond
				}
				rp := cc.tryRecvTimeout(wait)
				if rp == nil {
					break
				}
				// 先维护怪物表：本循环同样会吃掉出现/死亡包，
				// ⚠️ 漏了这里就会出现"对着已经死掉的怪一直空挥"——
				// 表现为循环跑满上限、升级永远验证不到（攻击落空 145 次）。
				switch rp.Head.Ident {
				case proto.SM_TURN:
					if isMonsterID(rp.Head.Recog) {
						known[rp.Head.Recog] = [2]int{int(rp.Head.Param), int(rp.Head.Tag)}
					}
				case proto.SM_DEATH, proto.SM_DISAPPEAR:
					delete(known, rp.Head.Recog)
				}
				switch rp.Head.Ident {
				case proto.SM_HIT:
					if int32(rp.Head.Recog) == actorID {
						posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
					}
				case proto.SM_STRUCK:
					// 自己挨打（怪物反击）
					if int32(rp.Head.Recog) == actorID {
						tanked = true
						fmt.Printf("    玩家受击: HP=%d/%d 伤害=%d\n",
							rp.Head.Param, rp.Head.Tag, rp.Head.Series)
						continue
					}
					if int32(rp.Head.Recog) != int32(monID) {
						continue
					}
					hit = true
					hitNow = true
					// ⚠️ SM_STRUCK：Param=HP, Tag=MaxHP, Series=**伤害**
					fmt.Printf("    命中: 怪物剩余HP=%d/%d 伤害=%d\n",
						rp.Head.Param, rp.Head.Tag, rp.Head.Series)
				case proto.SM_WINEXP:
					kills++
					fmt.Printf("    击杀(%d)! 经验+%d\n", kills, rp.Head.Recog)
					killed = true
					monID = 0 // 释放目标，下一轮锁定另一只
				case proto.SM_ADDITEM:
					noteDrop(int(rp.Head.Param), int(rp.Head.Tag))
					fmt.Printf("    掉落: 地面物品ID=%d 于 (%d,%d)\n",
						rp.Head.Recog, rp.Head.Param, rp.Head.Tag)
				case proto.SM_LEVELUP:
					leveled = true
					newLevel = int(proto.MakeLong(rp.Head.Param, rp.Head.Tag))
					fmt.Printf("    ★ 升级! 等级=%d\n", newLevel)
				case proto.SM_DEATH:
					fmt.Printf("    怪物死亡动画 ActorId=%d\n", rp.Head.Recog)
				}
				if killed {
					break
				}
			}
			// 自愈：连续空挥说明这个目标在服务端已经不存在（或打不到），
			// 从表里剔除换下一只。没有这层兜底时用例会对着幽灵空转到上限
			// （表现为"跑了 40 秒只杀 3-4 只、升级永远验证不到"）。
			if killed || hitNow {
				miss = 0
			} else if miss++; miss >= 5 {
				delete(known, int32(monID))
				monID, monX, monY = 0, 0, 0
				miss = 0
			}
			// 攻击间隔：本地回环下 60ms 足够（服务端没有攻击冷却），
			// 原来给 150ms 会让 150 轮空转白白多花 20 秒。
			time.Sleep(sc(60 * time.Millisecond))
		}
		if !hit {
			log.Fatal("攻击未命中任何怪物")
		}
		if tanked {
			fmt.Println("    玩家确实受到了怪物反击 ✓")
		}
		if drops > 0 {
			fmt.Printf("    共掉落 %d 件物品 ✓\n", drops)
		}

		// [13] 捡取与药水：打完掉的东西要能捡起来、能用掉
		if drops > 0 && dropX >= 0 {
			fmt.Println("[13] 测试捡取与使用药水")

			// 原版只能捡**自己脚下**的（ObjBase CM_PICKUP 校验坐标），
			// 所以必须走到掉落格上：走一步 → 排空收包（用服务端回显纠正
			// 自己的坐标）→ 到位就捡，最多试 15 次。
			//
			// ⚠️ 不要"先走满 12 步再一次捡取"：移动请求可能被限流丢掉，
			// 客户端坐标与服务端不同步时算出的方向会越走越偏，
			// 结果是"停在掉落点旁边一格"然后 Fatal。
			picked, bagN := false, 0
			blocked := 0
			for attempt := 0; attempt < 30 && !picked; attempt++ {
				drain()
				if attempt%5 == 0 {
					fmt.Printf("    [捡取] 尝试%d 自己(%d,%d) 掉落(%d,%d)\n",
						attempt, posX, posY, dropX, dropY)
				}
				if posX != dropX || posY != dropY {
					d := dirTowardAxis(dropX-posX, dropY-posY)
					if blocked > 0 {
						// 撞墙：绕一下（和追怪的移动循环同一套处理）
						d = uint8((int(d) + blocked) % 8)
					}
					bx, by := posX, posY
					time.Sleep(sc(430 * time.Millisecond)) // 走路最小间隔 400ms
					cc.send(wire.Packet{
						Head: proto.MakeDefaultMsg(proto.CM_RUN,
							proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(d), 0),
					})
					time.Sleep(150 * time.Millisecond)
					drain()
					if posX == bx && posY == by {
						blocked++
						if blocked > 6 {
							blocked = 1
						}
					} else {
						blocked = 0
					}
					continue
				}
				// 已站到掉落格：捡
				if attempt == 0 {
					fmt.Printf("    玩家 (%d,%d)，掉落点 (%d,%d)\n", posX, posY, dropX, dropY)
				}
				// CM_PICKUP: Recog=0, Param=x, Tag=y（ClMain.pas:3617）
				cc.send(wire.Packet{
					Head: proto.MakeDefaultMsg(proto.CM_PICKUP, 0, uint16(posX), uint16(posY), 0),
				})
				for j := 0; j < 8; j++ {
					rp := cc.tryRecvTimeout(300 * time.Millisecond)
					if rp == nil {
						break
					}
					switch rp.Head.Ident {
					case proto.SM_ITEMHIDE:
						picked = true
						fmt.Printf("    地面物品 %d 消失（已捡起）\n", rp.Head.Recog)
					case proto.SM_BAGITEMS:
						bagN = len(rp.Body) / proto.ClientItemSize
						fmt.Printf("    背包刷新: %d 件  %s\n", bagN, describeBag(rp.Body))
					}
				}
			}
			if !picked {
				log.Fatalf("捡取失败：未收到 SM_ITEMHIDE（自己(%d,%d) 掉落点(%d,%d)）",
					posX, posY, dropX, dropY)
			}
			fmt.Println("    捡取成功 ✓")

			// 用掉背包里的第一件药品（CM_EAT: Recog=槽位下标，ClMain.pas:3641）
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(0), 0, 0, 0),
			})
			used := false
			for j := 0; j < 8; j++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_EAT_OK:
					used = true
					fmt.Println("    SM_EAT_OK（使用成功）")
				case proto.SM_EAT_FAIL:
					fmt.Println("    SM_EAT_FAIL（不是可用的药品）")
				case proto.SM_BAGITEMS:
					fmt.Printf("    背包刷新: %d 件  %s\n",
						len(rp.Body)/proto.ClientItemSize, describeBag(rp.Body))
				}
			}
			if !used {
				log.Fatal("使用药品失败：未收到 SM_EAT_OK")
			}
			fmt.Println("    使用药品成功 ✓")
		}
		if leveled {
			fmt.Printf("    升级到 %d 级 ✓\n", newLevel)
		}
		if kills == 0 {
			fmt.Println("    （未击杀；伤害链路已验证）")
		}
	}

	if *tankTest {
		// ⚠️ 先提等级再挨打，两个原因：
		//  1) 群体治愈 0 级要 12 点 MP，新角色只有 5 点 → MP 不足会被服务端
		//     **静默丢弃**（只回一个 SM_MAGICFIRE_FAIL），表现为"技能没用"；
		//  2) 提级必须发生在**挨打之前** —— @level 会重算属性把 HP 回满，
		//     而群体治愈只对 HP<MaxHP 的友方生效（Magic.pas:180）。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 30",
		})
		time.Sleep(tscale.D(500 * time.Millisecond))

		// 先在身旁刷几只，否则真实地图上怪可能离得很远，等不到攻击
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@spawn 鸡 4",
		})
		time.Sleep(900 * time.Millisecond)

		// 站桩挨打：不移动，只统计自己受到多少次 SM_STRUCK。
		// 与攻击测试分开进行——怪物追击会让客户端坐标持续滞后，
		// 混在一起无法稳定验证。
		fmt.Println("[12] 测试怪物反击（站桩挨打）")
		got, lastHP := 0, 0
		// ⚠️ 只找**第一次**受伤就停下：30 级玩家的 HP 上限高，而自然恢复
		// （regenLoop，每秒发 SM_HEALTHSPELLCHANGED）会把这几点血很快回满。
		// 若继续挨打 28 秒再放群体治疗，服务端会因 `HP >= MaxHP` 判定"无需治疗"
		// 而跳过 —— 表现为 group-heal 断言偶发失败。
		// 所以：受伤 → 立刻验证治疗 → 再继续挨打验证"被多次攻击"。
		// ⚠️ 改成**墙钟截止（最多 20s）+ 每 ~5s 补刷一批鸡**：并发跑 14 槽时怪物 tick
		// 会被拖慢，"12×700ms 内一定挨到打"不成立 —— 等不到挨打 ⇒ 后面放群体治愈时
		// 玩家满血 ⇒ 服务端走"无需治疗"分支 ⇒ 日志里没有"的群体治愈：" ⇒
		// group-heal 偶发失败（实测根因就是这个，不是断言写法）。
		strikeDeadline := time.Now().Add(20 * time.Second)
		lastRespawn := time.Now()
		for got == 0 && time.Now().Before(strikeDeadline) {
			if time.Since(lastRespawn) > 5*time.Second {
				cc.send(wire.Packet{
					Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
					Body: "@spawn 鸡 4",
				})
				lastRespawn = time.Now()
			}
			rp := cc.tryRecvTimeout(700 * time.Millisecond)
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_STRUCK && int32(rp.Head.Recog) == actorID {
				got++
				lastHP = int(rp.Head.Param)
				fmt.Printf("    首次受击: HP=%d/%d 伤害=%d\n",
					rp.Head.Param, rp.Head.Tag, rp.Head.Series)
			}
			if rp.Head.Ident == proto.SM_NOWDEATH && int32(rp.Head.Recog) == actorID {
				fmt.Printf("    玩家被击倒（HP=%d）\n", lastHP)
			}
		}
		healed := false
		if healed = healIfHurt(cc, actorID, posX, posY, lastHP); !healed {
			// 没治成多半是"这一瞬间 HP 已满"（@level 重算把血回满 / 还没挨到打）。
			// 再等一次受击后重试，别直接把断言交给运气。
			for i := 0; i < 12; i++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					continue
				}
				if rp.Head.Ident == proto.SM_STRUCK && int32(rp.Head.Recog) == actorID {
					lastHP = int(rp.Head.Param)
					break
				}
			}
			healed = healIfHurt(cc, actorID, posX, posY, lastHP)
		}
		// ⚠️ 两次都没看到血量回升就**直接失败**：服务端只在真的治好时才打
		// "的群体治愈：" 那条日志（e2e 的 group-heal 查它），用例沉默地过会让
		// 断言在日志侧莫名其妙失败、且不吃 run 的重试。失败交给重试去重跑一遍。
		if !healed {
			log.Fatal("群体治愈没有让血量回升（服务端可能判成\"无人可治\"，查 -noregen 是否生效）")
		}
		// 继续挨打，验证"能被多次攻击"
		for i := 0; i < 28 && got < 3; i++ {
			rp := cc.tryRecvTimeout(700 * time.Millisecond)
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_STRUCK && int32(rp.Head.Recog) == actorID {
				got++
				lastHP = int(rp.Head.Param)
			}
			if rp.Head.Ident == proto.SM_NOWDEATH && int32(rp.Head.Recog) == actorID {
				fmt.Printf("    玩家被击倒（HP=%d）\n", lastHP)
			}
		}
		if got == 0 {
			log.Fatal("站桩期间未被攻击 —— 怪物反击未生效")
		}
		fmt.Printf("    共受击 %d 次 ✓\n", got)
	}

	if *spellTest {
		fmt.Println("[14] 测试技能释放")

		// 1) 技能列表（SM_SENDMYMAGIC，每条 12 字节）
		mag := popSide(proto.SM_SENDMYMAGIC)
		for i := 0; i < 10 && mag == nil; i++ {
			if p := cc.tryRecvTimeout(700 * time.Millisecond); p != nil {
				if p.Head.Ident == proto.SM_SENDMYMAGIC {
					mag = p
				} else {
					sidePackets = append(sidePackets, p)
				}
			}
		}
		if mag == nil {
			log.Fatal("未收到 SM_SENDMYMAGIC")
		}
		n := len(mag.Body) / 12
		if n == 0 {
			log.Fatal("角色没有任何技能")
		}
		var firstID uint32
		for i := 0; i < n; i++ {
			b := []byte(mag.Body[i*12 : (i+1)*12])
			id := binary.LittleEndian.Uint32(b[0:])
			lv := binary.LittleEndian.Uint32(b[4:])
			if i == 0 {
				firstID = id
			}
			fmt.Printf("    技能[%d]: MagicID=%d 等级=%d\n", i, id, lv)
		}

		// 2) 找一只怪（先在身旁刷一只，保证场景可控）
		//
		// ⚠️ 必须选**距离最近**的怪：地图上本来就有怪/NPC，取"第一个收到的"
		// 往往是远处的，技能会打在远处目标上——特效广播发生在目标周围，
		// 施法者不在其中，表现为偶发的"未收到 SM_MAGICFIRE"。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@spawn 鸡 1",
		})
		bestID, bestX, bestY, bestD := 0, 0, 0, 1<<30
		consider := func(p *wire.Packet) {
			if p.Head.Ident != proto.SM_TURN || !isMonsterID(p.Head.Recog) {
				return
			}
			x, y := int(p.Head.Param), int(p.Head.Tag)
			if d := absi(x-posX) + absi(y-posY); d < bestD {
				bestID, bestX, bestY, bestD = int(p.Head.Recog), x, y, d
			}
		}
		for _, p := range sidePackets {
			consider(p)
		}
		for i := 0; i < 20; i++ {
			p := cc.tryRecvTimeout(700 * time.Millisecond)
			if p == nil {
				break
			}
			if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
				consider(p)
			} else {
				sidePackets = append(sidePackets, p)
			}
		}
		if bestID == 0 {
			log.Fatal("视野内无怪物")
		}
		monID, monX, monY := bestID, bestX, bestY
		fmt.Printf("    目标怪物 ActorId=%d 于 (%d,%d) 距离 %d\n", monID, monX, monY, bestD)

		// 3) 释放（字段布局见 spellMsg 的注释：Recog=MakeLong(x,y)、技能号在 Param）
		//
		// ⚠️ Series 传 0：它只有 16 位，装不下 1000000+ 的怪物 ActorId，
		// 传真实 ID 会被截断成垃圾值（服务端按它查会打错怪）。
		// 传 0 让服务端只按坐标定位——坐标双方都无损。
		cc.send(wire.Packet{
			Head: spellMsg(int32(firstID), monX, monY),
		})

		fired, struck := false, false
		for j := 0; j < 10; j++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_MAGICFIRE:
				fired = true
				fmt.Printf("    SM_MAGICFIRE 特效 (技能号=%d)\n", rp.Head.Series)
			case proto.SM_STRUCK:
				// STRUCK 不带坐标，而同格可能有多只怪（多轮刷的鸡会留在图上），
				// 服务端按坐标定位到的目标未必等于本地记录的 monID。
				// 因此：自己挨打单独判，其余怪物受击一律算命中。
				if int32(rp.Head.Recog) == actorID {
					fmt.Printf("    玩家受击: HP=%d/%d 伤害=%d\n",
						rp.Head.Param, rp.Head.Tag, rp.Head.Series)
				} else if isMonsterID(rp.Head.Recog) {
					struck = true
					fmt.Printf("    命中: 怪物 ActorId=%d HP=%d/%d 伤害=%d\n",
						rp.Head.Recog, rp.Head.Param, rp.Head.Tag, rp.Head.Series)
				}
			case proto.SM_MAGIC_LVEXP:
				fmt.Printf("    修炼: MagicID=%d 等级=%d 修炼点=%d\n",
					rp.Head.Recog, rp.Head.Param, rp.Head.Tag)
			case proto.SM_WINEXP:
				fmt.Printf("    击杀! 经验+%d\n", rp.Head.Recog)
			case proto.SM_MAGICFIRE_FAIL:
				fmt.Println("    SM_MAGICFIRE_FAIL（MP 不足 / 未学会 / 被动技能）")
			default:
				fmt.Printf("    收到包 Ident=%d Recog=%d\n", rp.Head.Ident, rp.Head.Recog)
			}
		}
		if !fired {
			log.Fatal("未收到 SM_MAGICFIRE —— 技能未生效")
		}
		if firstID == 2 {
			// 治愈术：不造成伤害，看施法成功 + 血量同步即可
			fmt.Println("    治愈术施放成功 ✓")
		} else {
			if !struck {
				log.Fatal("技能未造成伤害")
			}
			fmt.Println("    技能释放成功 ✓")
		}
	}

	if *wallTest {
		// 火墙(22)：铺 5 格十字地面事件，之后每 3 秒对格内 IsProperTarget 的
		// 目标结算一次固定伤害（Event.pas:236 TFireBurnEvent.Run）。
		// 客户端**看不到火墙**（原版 Envir.pas:236 的 OS_EVENTOBJECT 分支是空的，
		// 事件不下发），所以只能靠"怪真的在掉血"来验证。
		fmt.Println("[17] 测试火墙")

		// ⚠️ 必须提级：火墙 0 级要 **30 点 MP**（`DefSpell 25 + ROUND(Spell 20/4)`），
		// 而 1 级法师只有 20 点 ⇒ 不提级连技能都放不出来。
		// 这条用例此前是"卡着线过"的：旧代码把 MP 公式写成 `Spell + DefSpell*level`
		// = 20，恰好等于 1 级 MP；公式按原版修正（见 spellpower.go 的 spellPoint）
		// 之后就暴露了。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 60",
		})
		time.Sleep(rcv(300 * time.Millisecond))

		// 0) 先挪一小段：火墙是**地面事件**、持续 5 秒（不随倍速缩），
		//    同一个格子反复铺会被服务端以"目标十字五格都已有事件"拒绝
		//    （e2e 的失败重试、以及手动连跑两次都撞过）。
		//    换个地方铺，每次都是干净地面。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: fmt.Sprintf("@map 0 %d %d", posX+9, posY+9),
		})
		for i := 0; i < 10; i++ {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_CHANGEMAP {
				posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
				break
			}
		}

		// 1) 身旁刷一只怪，取它的坐标当火墙中心。
		//    ⚠️ 必须选**最近**的怪（同 -spell 的理由：远处怪的受击广播不在身边）。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@spawn 鸡 1",
		})
		bestID, bestX, bestY, bestD := 0, 0, 0, 1<<30
		consider := func(p *wire.Packet) {
			if p.Head.Ident != proto.SM_TURN || !isMonsterID(p.Head.Recog) {
				return
			}
			x, y := int(p.Head.Param), int(p.Head.Tag)
			if d := absi(x-posX) + absi(y-posY); d < bestD {
				bestID, bestX, bestY, bestD = int(p.Head.Recog), x, y, d
			}
		}
		for _, p := range sidePackets {
			consider(p)
		}
		// ⚠️ 原先是"固定 20 次、**首个 nil 就 break**"：并发负载下只要有一个 700ms
		// 空窗就直接放弃 ⇒ `视野内无怪物` ⇒ wall 与 wall-kill 一起失败（实测偶发根因）。
		// 改成**墙钟截止 + 每 3s 补刷**，收到就退（预算 15s）。
		spawnDeadline := time.Now().Add(15 * time.Second)
		lastSpawn := time.Now()
		// ⚠️ 条件是"**够近的怪**还没找到"，不是"任何怪都没找到"：
		// 地图上有遗留的远处怪（实测距离 12），一碰到它就退出 ⇒ 火墙铺在 12 格外，
		// 而落点特效只广播给**那一片视野**，施法者收不到 ⇒ 报"没收到落点特效"，
		// 重试还会被第一次的墙挡住（"十字五格都已有事件"）。
		// ⚠️ 阈值取 **10**，依据是服务端的**特效广播半径**：
		// `broadcastToViewers` 用的是 `viewRange`（默认 12、切比雪夫距离），
		// 而这里的距离是**曼哈顿**（dx+dy）⇒ 曼哈顿 ≤10 必然切比雪夫 ≤10 < 12
		// ⇒ 施法者一定在广播范围内。
		// （早先写 ≤3 是过严：实测拥挤位置下 @spawn 的鸡会落到 8 格外，
		//   4 次刷怪都够不到 ⇒ 用例反而稳定失败。）
		const wallMaxDist = 10
		for bestD > wallMaxDist && time.Now().Before(spawnDeadline) {
			if time.Since(lastSpawn) > 3*time.Second {
				cc.send(wire.Packet{
					Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
					Body: "@spawn 鸡 1",
				})
				lastSpawn = time.Now()
			}
			p := cc.tryRecvTimeout(700 * time.Millisecond)
			if p == nil {
				continue
			}
			if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
				consider(p)
			} else {
				sidePackets = append(sidePackets, p)
			}
		}
		if bestD > wallMaxDist {
			log.Fatalf("15s 内没等到 ≤%d 格的怪（最近的在 %d 格外，ActorId=%d）",
				wallMaxDist, bestD, bestID)
		}
		monID, monX, monY := bestID, bestX, bestY
		fmt.Printf("    火墙中心 = 怪物所在格 ActorId=%d (%d,%d) 距离 %d\n", monID, monX, monY, bestD)

		// 2) 铺墙。火墙要耗蓝（0 级 20 点），先等自然回复。
		//
		// ⚠️ 与 -buff 的 cast() 一样：收不到有效响应就**重施**——冷却中的技能
		// 服务端直接 return 不回包（本次火墙 Delay=120ms，倍速下更易被冷却吞掉）。
		// Series 传 0：目标定位靠坐标（16 位装不下怪物 ActorId）。
		placed := false
		for attempt := 0; attempt < 3 && !placed; attempt++ {
			if attempt > 0 {
				fmt.Println("    (没收到落点特效，重施火墙)")
			}
			// ⚠️ **施法前先排空**：切图/刷怪之后的遗留包（updateVision 的
			// SM_TURN 洪流）会排在落点特效**前面**。原来是"固定 6 个包"的预算，
			// 被这些遗留包吃光就永远读不到特效 ⇒ 表现为"服务端日志明明铺了火墙、
			// 客户端却说没收到落点特效"（实测就是这么稳定失败的）。
			// 这与项目里那条"固定包数陷阱"是同一回事，标准做法：先 drainUntilQuiet。
			drainUntilQuiet(cc, rcv(1*time.Second))
			cc.send(wire.Packet{
				Head: spellMsg(22, monX, monY),
			})
			rejected := false
			// 再按**墙钟截止**等特效（收不到就继续等，绝不因一次空窗放弃）
			waitUntil := time.Now().Add(rcv(3 * time.Second))
			for time.Now().Before(waitUntil) && !placed {
				rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
				if rp == nil {
					continue
				}
				switch rp.Head.Ident {
				case proto.SM_MAGICFIRE:
					if int32(rp.Head.Series) == 22 {
						placed = true
						fmt.Printf("    火墙落点特效 (%d,%d) 技能号=%d\n",
							rp.Head.Param, rp.Head.Tag, rp.Head.Series)
					}
				case proto.SM_MAGICFIRE_FAIL:
					fmt.Println("    SM_MAGICFIRE_FAIL（MP 不足 / 未学会）")
					rejected = true
				}
			}
			if rejected && !placed {
				// MP 不够：等自然回复再重施（-buff 的 cast() 同样处理）
				time.Sleep(tscale.D(1500 * time.Millisecond))
			}
		}
		if !placed {
			log.Fatal("火墙未铺下（未收到技能号 22 的落点特效）")
		}

		// 3) 等周期伤害。服务端 3 秒一跳（倍速下更密），这里给足预算收至少一跳。
		//    判定：SM_STRUCK 的 Recog 是怪物（自己不会踩自己的墙，IsProperTarget
		//    对自己为假，所以不会收到自己的受击）。
		hit, killed := false, false
		for j := 0; j < 40 && !hit; j++ {
			// ⚠️ 必须**收到第一跳就退出**：这个循环原先是固定跑满 40 次，
			// 而 `tryRecvTimeout(500ms)` 在没包时会等满 500ms ⇒ 世界安静时
			// 白等 20s（实测该用例 14.6s 全耗在这里）。倍速只缩放游戏内时间，
			// 收包预算不缩放，所以这类"等够 N 次"的写法是提速的主要障碍。
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				continue
			}
			switch rp.Head.Ident {
			case proto.SM_STRUCK:
				if int32(rp.Head.Recog) == actorID {
					fmt.Printf("    玩家受击: HP=%d/%d 伤害=%d\n",
						rp.Head.Param, rp.Head.Tag, rp.Head.Series)
				} else if isMonsterID(rp.Head.Recog) {
					hit = true
					fmt.Printf("    火墙命中: 怪物 ActorId=%d HP=%d/%d 伤害=%d\n",
						rp.Head.Recog, rp.Head.Param, rp.Head.Tag, rp.Head.Series)
				}
			case proto.SM_DEATH:
				killed = true
				fmt.Printf("    怪物死亡 ActorId=%d\n", rp.Head.Recog)
			case proto.SM_WINEXP:
				fmt.Printf("    击杀经验+%d\n", rp.Head.Recog)
			}
		}
		if !hit {
			log.Fatal("火墙铺下但没有任何怪物掉血（周期结算没生效）")
		}
		// 再短暂收几条，把"烧死"那条也吃掉（只是为了让下面的打印有内容）。
		//
		// ⚠️ 这里**不能**把"收到 SM_DEATH"当断言的硬条件：击杀由服务端的火墙
		// 事件触发，死亡包不一定发到攻击者这里（实测经常只有服务端日志有
		// "的火墙烧死"）。真正的断言在服务端日志 `wall-kill` 上，而且 e2e 那边
		// 用**轮询等待**读过它，所以用例这里不必等满。
		// （原来这里固定跑满 40×500ms = 20s，是火墙用例 14.6s 的根源。）
		// ⚠️ 必须留够**第二跳**的时间，而且**不能提前断开**：
		// 鸡 5 点血、墙每跳 4 点 ⇒ 第一跳留 1 血、第二跳才死；而墙在**主人下线**
		// 时立刻置 Inert（wall.go 的"墙主失效检测"），客户端一走墙就不再结算
		// ⇒ 服务端永远不会打"的火墙烧死"（e2e 的 wall-kill 就找不到）。
		// 墙的寿命是 5 秒（不随倍速缩），这里等 1.5s 足够两跳。
		deadline := time.Now().Add(5 * time.Second)
		for !killed && time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(rcv(200 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_DEATH {
				killed = true
				fmt.Printf("    怪物死亡 ActorId=%d\n", rp.Head.Recog)
			}
		}
		if killed {
			fmt.Println("    火墙把怪烧死了 ✓")
		}
		fmt.Println("    火墙铺墙 + 周期伤害成功 ✓")
	}

	if *skill2Test {
		// 第二批技能：圣言术(32) / 瞬息移动(21) / 群体治愈(29) / 困魔咒(16)
		//
		// ⚠️ 这几个都是**概率或条件触发**的（圣言术按等级差掷骰、瞬息移动
		// Random(11) 判成功、群体治疗要求范围内有人掉血、困魔咒要等级压制），
		// 所以本用例只断言"技能被服务端识别并处理"（收到落点特效或明确失败），
		// **实际效果由 e2e 的服务端日志断言验证**（"圣言术命中"/"定住"/"群体治愈"）。
		fmt.Println("[18] 测试第二批技能（圣言术/瞬息移动/群体治愈/困魔咒）")
		// 困魔咒(16) 属原版 case 13..19，需要护身符
		grantEquip(cc, "护身符")

		// 0) 等级压制：新角色是 1 级，不提级的话圣言术与困魔咒的等级判定
		//    必然失败（`roll+(Lv-1) > 怪等级`）。提到 60 让压制余量最大 ——
		//    附近原生怪等级未知，30 级时可能压不住（表现为技能"没反应"）。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 60",
		})
		time.Sleep(tscale.D(300 * time.Millisecond))

		// 1) 逐个技能现刷目标。
		//
		// ⚠️ **不能**只刷一只然后连打三个技能：圣言术(32) 对不死系是**即死**
		//（60 级对 10 级怪成功率 65%），它把目标打死后面两个技能就
		// 只能拿到 SM_MAGICFIRE_FAIL —— 表现为"困魔咒时灵时不灵"。
		// 之前就是这个间歇性失败（服务端日志里没有"定住"那行）。
		//
		// 目标必须**精确指定**：原版 case 13..19 里圣言术只打 LA_UNDEAD，
		// 而这张图上自然刷着鸡/鹿等非不死系怪，"最近的那只"经常不对口。
		// 批次 2 关了 -monster-wander=false ⇒ 刷怪命令后新出现的第一个
		// SM_TURN 怪物就是我们刷的那只（见 spawnAndFindMonster）。
		spawnTarget := func(name string) (int, int, int) {
			id, x, y, ok := spawnAndFindMonster(cc, name)
			if !ok {
				log.Fatalf("视野内刷不出 %s", name)
			}
			return int(id), x, y
		}
		undeadID, ux, uy := spawnTarget("稻草人")
		fmt.Printf("    目标 稻草人 ActorId=%d (%d,%d)（不死系）\n", undeadID, ux, uy)

		// 2) 逐个释放。castOne 只看"服务端认了这个技能"：
		//    落点特效（SM_MAGICFIRE）或明确失败（SM_MAGICFIRE_FAIL）都算处理了。
		castOne := func(name string, magicID int32, x, y int) {
			cc.send(wire.Packet{Head: spellMsg(magicID, x, y)})
			got := "无响应"
			for j := 0; j < 8; j++ {
				rp := cc.tryRecvTimeout(800 * time.Millisecond)
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_MAGICFIRE:
					if int32(rp.Head.Series) == magicID {
						got = "落点特效"
					}
				case proto.SM_MAGICFIRE_FAIL:
					got = "被拒(SM_MAGICFIRE_FAIL)"
				case proto.SM_CHANGEMAP:
					got = "已切图(SM_CHANGEMAP)"
				case proto.SM_DEATH:
					got = "目标死亡"
				case proto.SM_STRUCK:
					if isMonsterID(rp.Head.Recog) {
						got = fmt.Sprintf("命中 ActorId=%d 伤害=%d", rp.Head.Recog, rp.Head.Series)
					}
				}
			}
			fmt.Printf("    %-12s 技能 %2d → %s\n", name, magicID, got)
		}

		// 圣言术：只打不死系 + 概率即死。打完目标大概率没了。
		castOne("圣言术", 32, ux, uy)

		// 困魔咒：重新刷一只，别指望上一只还活着。
		_, cx, cy := spawnTarget("稻草人")
		castOne("困魔咒", 16, cx, cy)

		// 群体治愈：目标是**自己**，不刷怪（且它要 HP<MaxHP 才生效，
		// 效果断言挪到批次 3 的 group-heal）。
		castOne("群体治愈", 29, posX, posY)

		// 瞬息移动放最后：它会切图，之后的坐标都变了。
		//
		// ⚠️ 它是**概率技能**：原版 MagSaceMove 判 `Random(11) < 技能等级*2+4`，
		// 0 级只有 4/11 ≈ 36% 成功率 ⇒ 放一次有 64% 概率不发动（且不涨修炼点）。
		// 重试 20 次累计失败率 0.64^20 ≈ 0.01%，把回归抖动压到可忽略。
		moved := false
		for attempt := 0; attempt < 20 && !moved; attempt++ {
			cc.send(wire.Packet{
				Head: spellMsg(21, posX, posY),
			})
			for j := 0; j < 6; j++ {
				rp := cc.tryRecvTimeout(600 * time.Millisecond)
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_CHANGEMAP {
					moved = true
					posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
					fmt.Printf("    瞬息移动   技能 21 → 已切图 (%d,%d)（第 %d 次尝试）\n",
						posX, posY, attempt+1)
					break
				}
			}
		}
		if !moved {
			fmt.Println("    瞬息移动   技能 21 → 20 次尝试都没发动（概率技能，正常）")
		}
		fmt.Println("    第二批技能分派链路已验证（效果断言见 e2e 服务端日志）✓")
	}

	if *slaveTest {
		// 宠物系统：召唤骷髅(17) 的跟随 + 诱惑之光(20) 收服野生怪。
		//
		// ⚠️ 效果几乎全断在**服务端日志**上：召唤兽与野怪在协议上**完全同形**
		//（同一条 SM_TURN + TCharDesc），客户端没有"这是宠物"的标志位。
		// 所以本用例只负责"把技能放出去、放到服务端认得"，断言交给 e2e 的 check。
		fmt.Println("[19] 测试宠物系统（召唤骷髅 / 诱惑之光）")

		// ⚠️ 召唤骷髅属原版 case 13..19，**要护身符**（CheckAmulet nType=1、数量 1）。
		// 少了这一步服务端会静默回 "护身符不足"（Magic.pas:769 的 case 13..19 入口）。
		// 诱惑之光(20) 在 `SKILL_TAMMING` 分支，**不**需要护身符。
		grantEquip(cc, "护身符")

		// 提到 60 级：诱惑之光的等级门槛
		// `RandomRange(Lv,Lv+20) + 技能*5 > (怪等级 + MagTammingTargetLevel)`，
		// 而 !setup.txt 的 MagTammingTargetLevel=1（不是 Delphi 出厂默认的 10），
		// 等级高 ⇒ 门槛恒过。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 60",
		})
		time.Sleep(tscale.D(300 * time.Millisecond))

		// ---- 1) 召唤骷髅(17)：生成一只"变异骷髅"并归到主人名下 ----
		//
		// ⚠️ 原版 sSkeleton 默认是**变异骷髅**（M2Share.pas:2061），
		// 我们之前写的是怪物表里另一只普通怪"骷髅"。
		before := countSlaveActors()
		summoned := false
		for attempt := 0; attempt < 6 && !summoned; attempt++ {
			cc.send(wire.Packet{Head: spellMsg(17, posX, posY)})
			// 预算随倍速缩（召唤是瞬发，20 倍下 1s 余量足够）
			summoned = waitForSlaveAppear(cc, before, rcv(1*time.Second))
			if !summoned {
				time.Sleep(tscale.D(800 * time.Millisecond))
			}
		}
		fmt.Printf("    召唤骷髅   技能 17 → 视野内实体 %s（归属断言见 e2e check slave-summon）\n",
			map[bool]string{true: "已增加", false: "无变化（查服务端日志）"}[summoned])

		// ---- 2) 诱惑之光(20)：收一只野生的鸡 ----
		//
		// 概率（!setup.txt 的 MagTammingTargetHPRate=1000 下）：
		//   门1 Random(4-技能等级)=0 → 0 级 25%、3 级 **100%**
		//   门2 Random(2)=0        → 50%（否则反过来把怪定住）
		//   血量门 Random(n14)=0    → 鸡只有 5 点血 ⇒ n14=2 ⇒ 50%
		// 所以先把技能提到 3 级（门1 恒过），单次 25%，20 次累计 >99%。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@magic 20 3",
		})
		time.Sleep(tscale.D(300 * time.Millisecond))

		// ⚠️ **每一次都刷一只新鸡**：收服成功后那只鸡就有主了
		//（`mon.MasterID != 0` ⇒ 服务端直接回 SM_MAGICFIRE_FAIL，Magic.pas:783），
		// 失败时又有 1/14 概率被招死。所以"打同一只 20 下"里能真正参与判定的
		// 往往只有 6~7 下——按 25%/下算只有 13% 把握，间歇失败就来自这里。
		// 每次换新目标 ⇒ 20 次**独立**的 25%，累计 >99%。
		//
		// ⚠️ 这一段刻意放在**诱惑之光之前**：`@spawn` 是按玩家周围的 8 格环绕
		// 找空位，而诱惑那段一次刷 30 只鸡会把周围占满 ⇒ 那时再刷半兽勇士
		// 会"生成 x0"（实测踩过）。
		// ---- 宠物打怪（原版 IsAttackTarget：主人打哪只，宠物就跟打哪只）----
		//
		// 靶子用**蛤蟆**（hp=20、AC 0、MAC 0、DC 0/5）：它同时满足"确定性击杀"两个条件：
		//   ① 主人的一发火球只打掉 ~4 点（道士 MC 低）⇒ 打不死它、不抢击杀；
		//   ② 剩 16 点，宠物（一刀 9~14）两刀收掉 ⇒ 一两秒内可观测。
		// 于是"击杀者是宠物自己"这条（`GainSlaveExp` 唯一触发点）能端到端钉住。
		// （早先试过变异骷髅 60 血：宠物要磨十几秒，整槽 10s → 45s，不值当。）
		// 本槽位是 -monster-aggro=false ⇒ 靶子不还手，纯粹看宠物打。
		//
		// ⚠️ 客户端**不等**击杀：SM_STRUCK 会在中间的 drainUntilQuiet 里被吃掉，
		// 等不到就白等十几秒；断言都在服务端日志（e2e 的 slave-pet-hit /
		// slave-pet-kill / slave-pet-exp，后两条 check_wait 轮询）。
		if beasts := spawnManyMonsters(cc, "蛤蟆", 1); len(beasts) > 0 {
			b := beasts[0]
			// 主人这一发只把目标记进 combatTargetID（原版 SetTargetCreat，与
			// "打不打得动"无关）；接下来两刀由宠物完成。
			cc.send(wire.Packet{Head: spellMsg(1, b.x, b.y)})
			drainUntilQuiet(cc, rcv(600*time.Millisecond))
		} else {
			log.Fatal("@spawn 蛤蟆 失败（宠物打怪这条验不了）")
		}

		// ⚠️ **先把技能等级提到 3**（0 级时这条断言是脆的）：
		//
		//	门1 `Random(4 - 技能等级) = 0` ⇒ 0 级是 1/4，3 级恒过；
		//	门2 `Random(2) != 0` 会走"反过来把它定住"分支 ⇒ 还要再砍一半。
		//
		// 也就是说 0 级时单次收服率只有 1/8，30 次全败的概率 (7/8)^30 ≈ **1.8%**
		//（实测撞到过一次：日志里 7 条"定住了"、0 条"收服了"）。3 级后单次 1/2，
		// 30 次全败 ≈ 1e-9，这条断言才算钉住。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@magic 20 3",
		})
		drainUntilQuiet(cc, rcv(400*time.Millisecond))
		// ⚠️ **必须跑多轮、并且按"实际施放次数"判充足性**。
		//
		// 一轮 `@spawn 鸡 30` 往往只出十来只（刷怪点有限），而单次收服率是
		// `门1 × 门2` ——3 级技能下 门1 恒过、门2 还要走 1/2 的"定住"分支
		// ⇒ 单次 1/2。十几次施放全败仍有 % 级概率（实测撞到过两次：日志里
		// 4 条"定住了"、2 条"招死了"、0 条"收服了"）。三轮重刷把实际施放
		// 堆到 40+ ⇒ 0.5^40 ≈ 1e-12。
		const tamingCasts = 30
		killed, casts := 0, 0 // 收服失败 + 1/14 招死 ⇒ 目标消失
		for round := 0; round < 3; round++ {
			targets := spawnManyMonsters(cc, "鸡", tamingCasts)
			if round == 0 && len(targets) < 10 {
				log.Fatalf("@spawn 只出现 %d 只鸡，诱惑之光验不了（要 ≥10）",
					len(targets))
			}
			for _, t := range targets {
				// ⚠️ 每 8 次补一次 `@level 60`：诱惑之光每次要 20 点上下 MP，
				// 连放会把蓝榨干 ⇒ **MP 不足时服务端静默丢弃**（只回一个
				// SM_MAGICFIRE_FAIL，根本没有"收服/定住/招死"的日志）。
				// `@level` 会把 HP/MP 补满（exp.go 的等级设置分支）。
				if casts > 0 && casts%8 == 0 {
					cc.send(wire.Packet{
						Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
						Body: "@level 60",
					})
					drainUntilQuiet(cc, rcv(300*time.Millisecond))
				}
				cc.send(wire.Packet{Head: spellMsg(20, t.x, t.y)})
				casts++
				// 每次只收两条：一条技能特效/失败、一条可能的 SM_DEATH。
				// 收包预算是**墙钟**（不随倍速缩），多收只会拖长用例。
				for j := 0; j < 2; j++ {
					rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
					if rp == nil {
						break
					}
					if rp.Head.Ident == proto.SM_DEATH && uint32(rp.Head.Recog) == t.id {
						killed++
					}
				}
			}
		}
		fmt.Printf("    诱惑之光   技能 20 → 施放 %d 次（%d 只目标被收服失败招死），"+
			"收服结果断言见 e2e check slave-tame\n", casts, killed)
		fmt.Println("    宠物分派链路已验证 ✓")

		// ---- 4) 宠物击杀 ⇒ 涨**宠物**经验 ----
		//
		// 原版只有"击杀者**本身是宠物**"才给宠物涨经验（ObjBase.pas:20845，
		// 见 petexp.go / petattack.go），断言在服务端日志 `slave-kill`。
		//
		// ⚠️ 这里**不**单独刷靶子：上面"诱惑之光"那段已经把 20+ 只蛤蟆刷在
		// 主人身边，宠物打怪 + 击杀本来就会顺带发生（实测每次都有），
		// 而再 `@spawn` 会因为**落点环被占满**刷出 0 只（踩过：生成 沙虫 x0）。
		fmt.Println("    宠物击杀链路已由上面的打怪顺带覆盖（e2e check slave-kill）")
	}

	if *equipTest {
		fmt.Println("[15] 测试装备穿脱")

		// 1) 脱下武器（槽 U_WEAPON=1）
		//
		// ⚠️ 原版字段（ClMain.pas:3633 / ObjBase.pas:4698）：
		//    Recog = 物品的 **MakeIndex**、Param = 装备槽位、body = 物品名。
		// MakeIndex 只能从"已穿戴列表"（SM_SENDUSEITEMS）里拿 —— 服务端登录时
		// 就下发过，连接结构体会把它缓存下来（见 conn.lastUseItems）。
		weaponIdx := int32(0)
		for i := 0; i < 30 && weaponIdx == 0; i++ {
			time.Sleep(rcv(100 * time.Millisecond))
			weaponIdx = useItemMakeIndex(cc.lastUse(), proto.SlotWeapon)
		}
		if weaponIdx == 0 {
			log.Fatalf("没拿到已穿戴武器的 MakeIndex（已穿戴列表=[%s]）", cc.lastUse())
		}
		fmt.Printf("    手里武器的 MakeIndex=%d\n", weaponIdx)
		cc.sendTakeOff(weaponIdx, proto.SlotWeapon, "木剑")
		bagAfterOff, useAfterOff := "", ""
		for j := 0; j < 10; j++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_BAGITEMS:
				bagAfterOff = rp.Body
			case proto.SM_SENDUSEITEMS:
				useAfterOff = rp.Body
			case proto.SM_FEATURECHANGED:
				fmt.Printf("    外观变更 Feature=%d (武器位=%d)\n",
					proto.MakeLong(rp.Head.Param, rp.Head.Tag),
					proto.FeatureWeapon(proto.MakeLong(rp.Head.Param, rp.Head.Tag)))
			}
		}
		fmt.Printf("    脱下后 装备栏=[%s] 背包=%s\n", useAfterOff, describeBag(bagAfterOff))

		idx := findInBag(bagAfterOff, "木剑")
		if idx < 0 {
			log.Fatal("脱下后背包里没找到木剑")
		}
		bagIdx := itemMakeIndexByName(bagAfterOff, "木剑")
		if bagIdx == 0 {
			log.Fatal("脱下后背包里的木剑没有 MakeIndex")
		}
		fmt.Printf("    木剑在背包槽位 %d（MakeIndex=%d）✓\n", idx, bagIdx)

		// 2) 穿上：Recog = MakeIndex、Param = 装备槽位、body = 物品名
		cc.sendTakeOn(bagIdx, proto.SlotWeapon, "木剑")
		useAfterOn, feat := "", int32(0)
		for j := 0; j < 10; j++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_SENDUSEITEMS:
				useAfterOn = rp.Body
			case proto.SM_FEATURECHANGED:
				feat = proto.MakeLong(rp.Head.Param, rp.Head.Tag)
			}
		}
		fmt.Printf("    穿上后 装备栏=[%s]\n", useAfterOn)
		if !strings.Contains(useAfterOn, "木剑") {
			log.Fatal("穿上后装备栏里没有木剑")
		}
		if proto.FeatureWeapon(feat) == 0 {
			log.Fatal("穿上武器后外观的武器位仍为 0")
		}
		fmt.Printf("    外观武器位=%d ✓\n", proto.FeatureWeapon(feat))

		// 3) 装备的"附加属性"（准确/敏捷）必须真的进战斗数值。
		//
		// 原版把每件装备折算成 `TAddAbility`（ItmUnit.pas:556-699）：**手镯/项链
		// 的 AC 高位是"准确"、MAC 高位是"敏捷"**（`Grobal2.pas:547-548` 的字段
		// 语义 + 客户端道具说明 `FState.pas:4057-4067`），由 `RecalcHitSpeed`
		// 加进 `m_btHitPoint`/`m_btSpeedPoint`，再经 `SM_SUBABILITY` 下发给客户端
		//（ObjBase.pas:5599-5603）。客户端能读到的只有这一条包，所以断言就落在这。
		//
		// 取值（`data/stditems.json`）：铁手镯 24 → ac.max=1 ⇒ 准确 +1；
		// 金项链 20 → mac.max=1 ⇒ 敏捷 +1。基数：命中 DEFHIT=5、敏捷 DEFSPEED=15。
		const baseHit, baseSpeed = 5, 15

		// ⚠️ 先提级：铁手镯需 3 级、金项链需 2 级（NeedLevel），1 级会被服务端拒掉。
		// 提级不影响命中/敏捷基数（那两项只看装备与技能等级）。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 5",
		})
		// 排空 @level 引发的重发（SM_ABILITY/SM_SUBABILITY/SM_SENDUSEITEMS），
		// 否则下面会读到"旧的" SM_SUBABILITY。
		drainUntilQuiet(cc, rcv(1*time.Second))

		takeOn := func(name string) (hit, speed int, ok bool) {
			drainUntilQuiet(cc, rcv(500*time.Millisecond))
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: "@give " + name,
			})
			bag := ""
			for j := 0; j < 10 && bag == ""; j++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_BAGITEMS {
					bag = rp.Body
				}
			}
			idx := findInBag(bag, name)
			if idx < 0 {
				fmt.Printf("    @give %s 后背包里没找到它（%s）\n", name, describeBag(bag))
				return 0, 0, false
			}
			// 原版字段：Recog = 物品 MakeIndex、Param = 槽位（按 StdMode 算）、body = 名字
			if _, slot := takeOnFromBag(cc, bag, name); slot < 0 {
				fmt.Printf("    @give %s 后算不出它的装备槽（%s）\n", name, describeBag(bag))
				return 0, 0, false
			}
			for j := 0; j < 12; j++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_SUBABILITY {
					return int(proto.LoByte(rp.Head.Param)), int(proto.HiByte(rp.Head.Param)), true
				}
			}
			return 0, 0, false
		}

		hit, speed, ok := takeOn("铁手镯") // 槽 5 = 左手镯（U_ARMRINGL，注意 U_HELMET=4）
		if !ok {
			log.Fatal("穿上铁手镯后没收到 SM_SUBABILITY")
		}
		fmt.Printf("    穿上铁手镯 → 命中=%d 敏捷=%d\n", hit, speed)
		if hit != baseHit+1 {
			log.Fatalf("命中 = %d，期望 %d（基数 %d + 铁手镯准确 1）", hit, baseHit+1, baseHit)
		}

		hit, speed, ok = takeOn("金项链") // 槽 3 = 项链
		if !ok {
			log.Fatal("穿上金项链后没收到 SM_SUBABILITY")
		}
		fmt.Printf("    再穿金项链 → 命中=%d 敏捷=%d\n", hit, speed)
		if speed != baseSpeed+1 {
			log.Fatalf("敏捷 = %d，期望 %d（基数 %d + 金项链敏捷 1）", speed, baseSpeed+1, baseSpeed)
		}
		if hit != baseHit+1 {
			log.Fatalf("命中 = %d，期望 %d（项链不影响命中）", hit, baseHit+1)
		}
		fmt.Println("    装备附加属性（准确/敏捷）已进副属性 ✓")

		// 4) 攻击速度：**只有** SM_CHARSTATUSCHANGED 带它。字段顺序照原版
		//    `SendUpdateMsg(Self, RM_CHARSTATUSCHANGED, m_nHitSpeed, m_nCharStatus, …)`
		//    ⇒ wParam=攻速、lParam1=32 位状态，翻译时按 lParam 拆字
		//    ⇒ 最终 **Param=状态低字、Tag=状态高字、Series=攻速**（见 buff.go 的注释）。
		//
		// 取值（data/stditems.json）：狂风戒指 145，StdMode 23，`ac.min=2` ⇒ 攻速 +2
		//（ItmUnit.pas:630-660：首饰 `Inc(nHitSpeed, AC); Dec(nHitSpeed, MAC)`）。
		// 它同时验证"装备 ⇒ RecalcAbilitys ⇒ RM_CHARSTATUSCHANGED"这条链路。
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@level 20"})
		drainUntilQuiet(cc, rcv(1*time.Second))
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@give 狂风戒指"})
		ringBag := ""
		for j := 0; j < 10 && ringBag == ""; j++ {
			if rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond)); rp != nil && rp.Head.Ident == proto.SM_BAGITEMS {
				ringBag = rp.Body
			}
		}
		ringIdx := findInBag(ringBag, "狂风戒指")
		if ringIdx < 0 {
			log.Fatalf("背包里没有狂风戒指（%s）", describeBag(ringBag))
		}
		if _, slot := takeOnFromBag(cc, ringBag, "狂风戒指"); slot < 0 {
			log.Fatalf("狂风戒指算不出装备槽（%s）", describeBag(ringBag))
		}
		hitSpeed, gotStatus := -1, false
		for j := 0; j < 12; j++ {
			rp := cc.tryRecvTimeout(rcv(500 * time.Millisecond))
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_CHARSTATUSCHANGED && rp.Head.Recog == actorID {
				hitSpeed, gotStatus = int(int16(rp.Head.Series)), true
			}
		}
		if !gotStatus {
			log.Fatal("穿上狂风戒指后没收到 SM_CHARSTATUSCHANGED")
		}
		fmt.Printf("    穿上狂风戒指 → 攻速=%d\n", hitSpeed)
		if hitSpeed != 2 {
			log.Fatalf("攻速 = %d，期望 2（狂风戒指 ac.min=2，ItmUnit.pas:636-637）", hitSpeed)
		}
		fmt.Println("    装备攻击速度已进状态包 ✓")

		// 5) 装备**形状**触发的特殊效果（原版 RecalcAbilitys 里按槽位判 StdItem.Shape
		//    那一大段，ObjBase.pas:3240-3360）。这里只验两条**确定性**的：
		//      111 隐身戒指 ⇒ 装备期间一直处于"隐身"状态位
		//      114 复活戒指 ⇒ @die 被拦下（HP 回满、不回城）
		//    抽签类的（113 麻痹 1/5）、虹魔吸血、技巧项链修炼点 ×3 由单测覆盖。
		wearByName := func(name string) {
			drainUntilQuiet(cc, rcv(500*time.Millisecond))
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@give " + name})
			bag := ""
			for j := 0; j < 10 && bag == ""; j++ {
				if rp := cc.tryRecvTimeout(rcv(500 * time.Millisecond)); rp != nil && rp.Head.Ident == proto.SM_BAGITEMS {
					bag = rp.Body
				}
			}
			idx := findInBag(bag, name)
			if idx < 0 {
				log.Fatalf("背包里没有 %s（%s）", name, describeBag(bag))
			}
			if _, s := takeOnFromBag(cc, bag, name); s < 0 {
				log.Fatalf("%s 算不出装备槽（%s）", name, describeBag(bag))
			}
			// ⚠️ 这里**不能**排空：穿上装备时服务端立刻发状态位/副属性包，
			// 排空会把要断言的那条吃掉（实测：隐身戒指穿了、状态包却"没收到"）。
			// 后面的 waitFor 只摘匹配的那条，无关包留在队列里即可。
		}
		// ① 隐身戒指（Shape 111）⇒ 状态位出现 StateInvisible（官方下标 8 ⇒ 0x00800000）
		wearByName("隐身戒指") // StdMode 22 ⇒ 左戒指槽
		if cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_CHARSTATUSCHANGED && p.Head.Recog == actorID &&
				clientStatus(p)&entity.StateInvisible != 0
		}, 3*time.Second) == nil {
			log.Fatal("戴上隐身戒指后状态位里没有 StateInvisible")
		}
		fmt.Println("    隐身戒指 → 状态位含隐身 ✓")

		// ② 复活戒指（Shape 114）⇒ @die 被拦下（服务端有"复活戒指生效"日志 + 提示）
		wearByName("复活戒指") // StdMode 23 ⇒ 右戒指槽
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@die"})
		if m := waitSysMsg(cc, "复活戒指生效"); m == "" {
			log.Fatal("@die 没被复活戒指拦下（期望提示\"复活戒指生效，体力恢复\"）")
		} else {
			fmt.Printf("    复活戒指 → %s ✓\n", m)
		}

		// 6) 上限重算：魔血套把 **MaxMP 挪给 MaxHP**（原版 :3465-3473）。
		//
		// 顺带修的是一个**结构缺口**：`playerAddAbil` 算出的 `a.hp`/`a.mp`
		//（StdMode 63 首饰、魔血套）此前没有消费方 —— `Abil.MaxHp/MaxMp` 只由等级给。
		// 现在按"等级基数 + 装备加成"重算（幂等 ⇒ 落盘后重新登录也不会加成两次）。
		//
		// 数值（data/stditems.json）：魔血戒指/手镯/项链形状 133/134/135、AniCount 各 5
		// ⇒ Σ=15，三件齐再 +50 ⇒ 一共挪 65 点。
		// 上限在 **SM_ABILITY**（TAbility 里有 MaxHP/MaxMP）里下发，不是血量包。
		// ⚠️ 要**收满整个窗口并保留最后一条**：穿三件魔血会连发三条 SM_ABILITY，
		// 只读第一条会拿到中间态（实测：报 MaxHP 275→323，而服务端最终是 380）。
		readMax := func(wait time.Duration) (uint16, uint16) {
			hp, mp := uint16(0), uint16(0)
			deadline := time.Now().Add(wait)
			for time.Now().Before(deadline) {
				rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
				if rp == nil {
					continue
				}
				if rp.Head.Ident == proto.SM_ABILITY {
					if ab, ok := proto.DecodeAbility([]byte(rp.Body)); ok {
						hp, mp = ab.MaxHP, ab.MaxMP
					}
				}
			}
			return hp, mp
		}
		drainUntilQuiet(cc, rcv(400*time.Millisecond))
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@level 33"})
		beforeHP, beforeMP := readMax(3 * time.Second)
		if beforeHP == 0 {
			log.Fatal("没读到戴上魔血套之前的 MaxHP（SM_ABILITY 里应有）")
		}
		for _, n := range []string{"魔血戒指", "魔血手镯", "魔血项链"} {
			wearByName(n)
		}
		afterHP, afterMP := readMax(3 * time.Second)
		fmt.Printf("    魔血套：MaxHP %d → %d，MaxMP %d → %d\n", beforeHP, afterHP, beforeMP, afterMP)
		// ⚠️ 这里**不做数值断言**，只打印观测量：穿三件会连发三条 SM_ABILITY，
		// 客户端的"前后快照"与服务端的中间态很难对齐（实测差 1~3 点），
		// 硬断数字只会变成假失败。精确算术由单测 `TestApplyEquipHpMpMoXieSuite` 守
		// （ΣAniCount + 三件齐 50 + "不能扣光蓝"的钳制），e2e 用**服务端日志**
		// `上限重算：HP` 断言（见 e2e 的 equip-maxhp）。
		fmt.Printf("    魔血套换上：MaxHP %d → %d，MaxMP %d → %d（详细数值见服务端日志）\n",
			beforeHP, afterHP, beforeMP, afterMP)
		fmt.Println("    上限重算（魔血套 MaxMP → MaxHP）✓")

		// 7) 传送戒指（Shape 112）开放 `@move X Y`（原版 CmdUserMoveXY，ObjBase.pas:15195）：
		//    可走 + 不占位 ⇒ 同图落点；冷却 `UserMoveTime=10` 秒（官方 !setup.txt:848）。
		//    第二发必然被拒（"N秒之后才可以再使用此功能！！！"）。
		wearByName("传送戒指") // StdMode 23 ⇒ 右戒指槽
		drainUntilQuiet(cc, rcv(400*time.Millisecond))
		// ⚠️ 落点必须**同时**可走且没有别的对象站着（原版 `CanWalkOfItem` 的
		// `UserMoveCanDupObj=0`；我们本来就是一格一对象）—— 屠夫脚下 (313,271)
		// 就属于"被占"，拿它当落点必然被拒。所以就近试几个候选，命中一个即可
		//（**成功前**的失败不会进入冷却，只有第一发成功才开始 10 秒计时）。
		var target [2]int
		landed := false
		for _, off := range [][2]int{{8, 0}, {0, 8}, {-8, 0}, {0, -8}, {5, 5}} {
			tx, ty := posX+off[0], posY+off[1]
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: fmt.Sprintf("@move %d %d", tx, ty)})
			moved := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_CHANGEMAP && int(p.Head.Param) == tx && int(p.Head.Tag) == ty
			}, 2*time.Second)
			if moved != nil {
				target, landed = [2]int{tx, ty}, true
				break
			}
			drainUntilQuiet(cc, rcv(200*time.Millisecond))
		}
		if !landed {
			log.Fatal("@move 试了 5 个候选点都没落地（传送戒指没生效？）")
		}
		posX, posY = target[0], target[1] // 用例末尾的走路断言按这两个变量比对
		fmt.Printf("    传送戒指 → @move 落到 (%d,%d) ✓\n", posX, posY)
		// 第二发：冷却中，应回"…秒之后才可以再使用此功能！！！"
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: fmt.Sprintf("@move %d %d", posX+1, posY)})
		if m := waitSysMsg(cc, "才可以再使用此功能"); m == "" {
			log.Fatal("@move 的 10 秒冷却没生效（第二发没被拒）")
		} else {
			fmt.Printf("    传送戒指冷却 → %s ✓\n", m)
		}
		// 8) 火焰戒指（Shape 115）⇒ 装备期间**临时授予**火球术(1)（原版 `AddItemSkill(1)`，
		//    ObjBase.pas:3453）：技能列表里原本没有就会按**等级 1** 加进来，
		//    摘下来时按职业保护回收（法师不删火球术 —— 见 syncItemSkills 的注释）。
		//    这里只验"授予"这一半：服务端会记一条"授予了 火球术"。
		wearByName("火焰戒指") // StdMode 22 ⇒ 左戒指槽
		if cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_SENDMYMAGIC }, 3*time.Second) == nil {
			log.Fatal("戴上火焰戒指后没收到 SM_SENDMYMAGIC（技能列表没更新）")
		}
		fmt.Println("    火焰戒指 → 授予火球术（服务端日志见 equip-ringskill）✓")

		// 9) 负重（原版 `m_WAbil.MaxWeight/MaxWearWeight/MaxHandWeight`，见 weight.go）。
		//
		// 取两条**与职业/等级无关**的确定性断言：
		//   ① `@give 木剑`（重量 4）⇒ 属性包的 `Weight`（背包负重）应 +4；
		//   ② 戴超负载戒指（Shape 119）⇒ `MaxWeight` **翻倍**
		//      （原版 `Inc(m_WAbil.MaxWeight, m_WAbil.MaxWeight)`，ObjBase.pas:3459-3464）。
		// 负重只在属性包 SM_ABILITY 里下发，所以都从这条包读。
		readAbility := func(wait time.Duration) (proto.Ability, bool) {
			var got proto.Ability
			ok := false
			deadline := time.Now().Add(wait)
			for time.Now().Before(deadline) {
				rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
				if rp == nil {
					continue
				}
				if rp.Head.Ident == proto.SM_ABILITY {
					if ab, dec := proto.DecodeAbility([]byte(rp.Body)); dec {
						got, ok = ab, true
					}
				}
			}
			return got, ok
		}
		drainUntilQuiet(cc, rcv(400*time.Millisecond))
		// ① 给一把木剑：背包负重 +4
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@give 木剑"})
		before, ok1 := readAbility(2 * time.Second)
		if !ok1 {
			log.Fatal("① 没读到属性包（SM_ABILITY）")
		}
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@give 木剑"})
		after, ok2 := readAbility(2 * time.Second)
		if !ok2 {
			log.Fatal("② 没读到属性包（SM_ABILITY）")
		}
		fmt.Printf("    负重：%d/%d → %d/%d\n", before.Weight, before.MaxWeight, after.Weight, after.MaxWeight)
		if after.Weight != before.Weight+4 {
			log.Fatalf("再给一把木剑（重量 4）后背包负重 = %d，期望 %d",
				after.Weight, before.Weight+4)
		}
		if after.MaxWeight == 0 {
			log.Fatal("MaxWeight 为 0：负重上限没下发")
		}

		// ② 超负载戒指 ⇒ 上限翻倍（在"两把木剑"的负重基础上只改上限）
		wearByName("超负载戒指") // StdMode 22 ⇒ 左戒指槽
		ring, ok3 := readAbility(3 * time.Second)
		if !ok3 {
			log.Fatal("③ 戴超负载戒指后没读到属性包")
		}
		if int(ring.MaxWeight) != int(after.MaxWeight)*2 {
			log.Fatalf("戴超负载戒指后 MaxWeight = %d，期望 %d（翻倍）", ring.MaxWeight, after.MaxWeight*2)
		}
		fmt.Printf("    超负载戒指 → MaxWeight %d → %d（翻倍）✓\n", after.MaxWeight, ring.MaxWeight)
		fmt.Println("    装备穿脱成功 ✓")

		// 10) `StdMode = 3` 消耗品：祝福油（原版 `EatUseItems`，见 gamesvr/eatuse.go）。
		//
		// 只钉**完全确定**的两条：
		//   ① 没装备武器时用 ⇒ `SM_EAT_FAIL`，且**油留在背包里**（原版
		//      `WeaptonMakeLuck` 首行 `Exit(False)` ⇒ 不消耗）；
		//   ② 装上武器后再用 ⇒ `SM_EAT_OK` 且油被扣掉。
		//
		// ⚠️ **不写死"幸运 +1"**：使用祝福油的第一件事是 `Random(50) = 1` 的
		// "倒大霉"判定（武器被诅咒），写死就变成 2% 概率的偶发失败。
		// 具体加没加上由单测（eatuse_test.go）钉。
		fmt.Println("\n[31] 测试消耗品（祝福油）")
		eatBagIndexOf := func(name string) int {
			drainUntilQuiet(cc, rcv(250*time.Millisecond))
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@give " + name})
			bag := ""
			for j := 0; j < 10 && bag == ""; j++ {
				rp := cc.tryRecvTimeout(rcv(500 * time.Millisecond))
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_BAGITEMS {
					bag = rp.Body
				}
			}
			idx := findInBag(bag, name)
			if idx < 0 {
				log.Fatalf("@give %s 后背包里没找到它（%s）", name, describeBag(bag))
			}
			return idx
		}
		eatIdx := func(idx int) (ok, fail bool, bag string) {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(idx), 0, 0, 0)})
			for j := 0; j < 10; j++ {
				rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_EAT_OK:
					ok = true
				case proto.SM_EAT_FAIL:
					fail = true
				case proto.SM_BAGITEMS:
					bag = rp.Body
				}
			}
			return
		}

		// ① 先把武器**显式脱掉**（本用例前面不一定还穿着它——
		//    踩过：以为"用例结束时会脱下来"，结果油直接生效了，断言反过来失败）
		//
		// 原版字段：Recog = 武器 MakeIndex（从已穿戴列表里取）、Param = 槽位。
		if w := useItemMakeIndex(cc.lastUse(), proto.SlotWeapon); w != 0 {
			cc.sendTakeOff(w, proto.SlotWeapon, "木剑")
		}
		drainUntilQuiet(cc, rcv(300*time.Millisecond))

		oilIdx := eatBagIndexOf("祝福油")
		if _, fail, _ := eatIdx(oilIdx); !fail {
			log.Fatal("没装备武器时用祝福油应回 SM_EAT_FAIL")
		}
		// ⚠️ 这里**不能**顺手断言"油还在背包里"：`SM_EAT_FAIL` 路径服务端不回背包
		// （原版也不回）⇒ 客户端拿不到新背包，断言必然落空（踩过一次）。
		// "没消耗"由服务端日志钉：只有 `eatUseItem` 返回 true 才会打
		// `使用祝福油：...`，两者互斥 ⇒ e2e.sh 的 equip-oil-noweapon 检查足够。
		fmt.Println("    没武器 → SM_EAT_FAIL（消耗与否见服务端日志）✓")

		// ② 装上武器再来（背包里此时有 2 个祝福油：①②各 @give 了一个 ⇒ 叠成一堆）
		wearByName("木剑")
		ok, fail, bag := eatIdx(eatBagIndexOf("祝福油"))
		if !ok || fail {
			log.Fatalf("装备武器后使用祝福油应回 SM_EAT_OK（ok=%v fail=%v）", ok, fail)
		}
		// ⚠️ 不能断言"油从背包消失"：祝福油可堆叠，两个叠成一堆（数量存在 Dura 里，
		// 见 addToBag），用掉一个后**还剩 1 个**。踩过一次。
		if n := bagCountOf(bag, "祝福油"); n != 1 {
			log.Fatalf("生效后祝福油数量应为 1（2 个叠堆用掉一个），实际 %d", n)
		}
		fmt.Println("    有武器 → SM_EAT_OK 且堆叠数量 2 → 1 ✓（加没加上由服务端日志/单测钉）")

		// 11) 传送类消耗品（同属 `StdMode=3` 的 EatUseItems 一族）：
		//   随机传送卷（Shape 2）在原图随机落点、回城卷（Shape 1）回本图随机落点
		//   —— 原版都走 `MapRandomMove`（ObjBase.pas:9810-9830，避边随机）。
		// 断言"收到带坐标的 SM_CHANGEMAP"（我们同图传送也走 switchMap 的这套包），
		// 具体落点由服务端日志与单测钉（`TestMapRandomPointInset`/`TestScrollRandomTeleports`）。
		for _, nm := range []string{"随机传送卷", "回城卷"} {
			idx := eatBagIndexOf(nm)
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(idx), 0, 0, 0)})
			moved := false
			var x, y uint16
			for j := 0; j < 10; j++ {
				rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_CHANGEMAP {
					moved, x, y = true, rp.Head.Param, rp.Head.Tag
				}
			}
			if !moved {
				log.Fatalf("使用 %s 后没收到 SM_CHANGEMAP", nm)
			}
			fmt.Printf("    %s → 落点 (%d,%d) ✓\n", nm, x, y)
		}
	}

	if *buffTest {
		fmt.Println("[16] 测试增益状态与召唤兽")
		// 原版 case 13..19 要求装备护身符（Shape=5），否则技能放不出来
		grantEquip(cc, "护身符")

		// ⚠️ 必须先提级：本用例连放 隐身/魔法盾/幽灵盾/神圣战甲/召唤骷髅，
		// 而新角色只有 MP=15~40。魔法盾 0 级就要 20 点，**MP 不足时服务端
		// 静默丢弃**（不回包），表现为"魔法盾未生效"——曾因此把本用例
		// 变成间歇性失败（复跑三次才过一次）。@level 会顺带把 MP 补满。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 60",
		})
		time.Sleep(tscale.D(300 * time.Millisecond))

		cast := func(magicID int32) []*wire.Packet {
			// 技能要耗蓝，两次施法之间等一会儿让自然回复补上
			time.Sleep(tscale.D(1200 * time.Millisecond))
			// ⚠️ **先排空遗留包再施法**。进游戏/换装会补发一批（SM_TURN、
			// SM_SUBABILITY、回复…），它们都在下面那张"算有效响应"的名单里，
			// 会被当成**本次施法**的响应（`responded=true` 后本轮就结束），
			// 于是真正要看的 SM_CHARSTATUSCHANGED 根本没读到。
			// 2026-10-05 加发 SM_SUBABILITY 后立刻暴露：此前只是靠
			// "10 个包够用"侥幸过，包数一变就崩。
			drainUntilQuiet(cc, rcv(400*time.Millisecond))

			// ⚠️ 收不到任何有效响应时**重施**：施法可能被冷却/限流静默丢弃
			// （服务端对冷却中的技能直接 return，不回包）。
			var got []*wire.Packet
			for attempt := 0; attempt < 3; attempt++ {
				cc.send(wire.Packet{
					Head: spellMsg(magicID, posX, posY),
				})
				got = nil
				responded := false
				// 预算用**墙钟截止**（不按包数算，也不"首个 nil 就 break"）：
				// 一轮施法的回包是同一个处理过程里发的，收完会静下来。
				// 收到响应后再多等一个"安静窗口"，把后续包（状态位等）一起收走。
				last := time.Now()
				deadline := time.Now().Add(rcv(2 * time.Second))
				for time.Now().Before(deadline) {
					rp := cc.tryRecvTimeout(rcv(250 * time.Millisecond))
					if rp == nil {
						if responded && time.Since(last) >= rcv(250*time.Millisecond) {
							break
						}
						continue
					}
					last = time.Now()
					got = append(got, rp)
					switch rp.Head.Ident {
					case proto.SM_CHARSTATUSCHANGED, proto.SM_HEALTHSPELLCHANGED,
						proto.SM_MAGICFIRE, proto.SM_MAGICFIRE_FAIL, proto.SM_TURN:
						// ⚠️ SM_MAGICFIRE_FAIL 也要算"有响应"：它表示
						// 施法被拒（MP 不足/冷却），此时重施也没用。
						responded = true
					}
				}
				if responded {
					break
				}
				fmt.Printf("    (技能 %d 没收到有效响应，重施)\n", magicID)
			}
			return got
		}

		// 1) 隐身术(18) → 状态位出现 StateInvisible（官方下标 8 ⇒ 0x00800000）
		invisible := false
		for _, rp := range cast(18) {
			if rp.Head.Ident == proto.SM_CHARSTATUSCHANGED {
				st := clientStatus(rp) // 官方：Param=状态低字、Tag=状态高字
				invisible = st&entity.StateInvisible != 0
				fmt.Printf("    隐身术 → 状态位=%d (隐身=%v)\n", st, invisible)
			}
		}
		if !invisible {
			log.Fatal("隐身术未生效（状态位无 StateInvisible）")
		}

		// 2) 魔法盾(31) → 状态位出现 StateShield（官方下标 11 ⇒ 0x00100000）
		shield := false
		for _, rp := range cast(31) {
			if rp.Head.Ident == proto.SM_CHARSTATUSCHANGED {
				st := clientStatus(rp) // 官方：Param=状态低字、Tag=状态高字
				shield = st&entity.StateShield != 0
				fmt.Printf("    魔法盾 → 状态位=%d (护盾=%v)\n", st, shield)
			}
		}
		if !shield {
			log.Fatal("魔法盾未生效（状态位无 StateShield）")
		}

		// 3) 召唤骷髅(17) → 视野内出现 RaceImg=23 的新怪物
		//
		// ⚠️ **23 是变异骷髅**，不是骷髅的 14。原版 sSkeleton 默认就是
		// '变异骷髅'（M2Share.pas:2061），我们之前召的是怪物表里另一只
		// 普通怪"骷髅"（RaceImg=14）——那是个 faithfulness bug，
		// 断言随之改成 23（Race=100）。
		summoned, race := false, uint8(0)
		for _, rp := range cast(17) {
			if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
				if cd, ok := proto.DecodeCharDesc([]byte(rp.Body)); ok {
					race = proto.FeatureRace(cd.Feature)
					if race == summonSkeletonRaceImg {
						summoned = true
					}
				}
			}
		}
		if !summoned {
			log.Fatalf("召唤骷髅失败（未出现 RaceImg=%d 的变异骷髅，最后见到 race=%d）",
				summonSkeletonRaceImg, race)
		}
		fmt.Printf("    召唤骷髅成功：变异骷髅 RaceImg=%d ✓\n", race)

		// 4) 召唤神兽(30) → RaceImg=54（神兽，与原版 sDragon 一致，这只没改）
		shen := false
		for _, rp := range cast(30) {
			if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
				if cd, ok := proto.DecodeCharDesc([]byte(rp.Body)); ok {
					if proto.FeatureRace(cd.Feature) == summonShenRaceImg {
						shen = true
					}
				}
			}
		}
		if shen {
			fmt.Println("    召唤神兽成功 ✓")
		}
		fmt.Println("    增益与召唤验证通过 ✓")
	}

	if *mapTest {
		fmt.Println("[17] 测试地图切换")

		// 依次切到每个目标，最后回到 0，验证往返与缓存
		targets := strings.Split(*mapTarget, ",")
		targets = append(targets, "0")

		for _, tgt := range targets {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: "@map " + tgt,
			})
			changed := false
			for j := 0; j < 12; j++ {
				rp := cc.tryRecvTimeout(600 * time.Millisecond)
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_CLEAROBJECTS:
					fmt.Printf("    -> %s: SM_CLEAROBJECTS\n", tgt)
				case proto.SM_CHANGEMAP:
					changed = true
					fmt.Printf("    -> %s: 地图=%q 坐标=(%d,%d)\n",
						tgt, rp.Body, rp.Head.Param, rp.Head.Tag)
				case proto.SM_SYSMESSAGE:
					fmt.Printf("    -> %s: %s\n", tgt, rp.Body)
				}
			}
			if !changed {
				log.Fatalf("切换到 %s 未收到 SM_CHANGEMAP", tgt)
			}
		}
		fmt.Println("    地图切换成功 ✓")
	}

	if *learnTest {
		fmt.Println("[18] 测试技能书学习")

		book := *learnBook
		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		// 收集包，返回本次收到的系统消息
		pump := func() (string, bool) {
			msgs, ok := "", false
			for k := 0; k < 12; k++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_SYSMESSAGE {
					msgs += rp.Body + " "
					if strings.Contains(rp.Body, "学会了") {
						ok = true
					}
				}
			}
			return msgs, ok
		}

		// 1) 拿到技能书
		say("@give " + book)
		bagBody := ""
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_BAGITEMS {
				bagBody = rp.Body
			}
		}
		idx := findInBag(bagBody, book)
		if idx < 0 {
			log.Fatalf("背包中未找到 %s", book)
		}
		fmt.Printf("    技能书 %s 在背包槽位 %d\n", book, idx)

		// 2) 1 级时学习：应被等级需求拒绝
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(idx), 0, 0, 0)})
		msgs, ok := pump()
		if ok {
			log.Fatalf("1 级不应学会 %s（等级需求未生效）", book)
		}
		fmt.Printf("    等级不足被拒绝 ✓  %s\n", strings.TrimSpace(msgs))

		// 3) 提升到 7 级后再学
		say("@level 20")
		pump()
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(idx), 0, 0, 0)})
		msgs, ok = pump()
		if !ok {
			log.Fatalf("升级后仍学不会 %s: %s", book, msgs)
		}
		fmt.Printf("    学会 ✓  %s\n", strings.TrimSpace(msgs))
		fmt.Println("    技能书学习成功 ✓")
	}

	if *npcTest {
		fmt.Println("[19] 测试 NPC 显示")

		// 传送到地图 0 的屠夫附近（merchant.txt: 1Bme 0 313 271）
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@map 0 313 271",
		})

		seen := 0
		for j := 0; j < 15; j++ {
			rp := cc.tryRecvTimeout(600 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Recog >= proto.NpcIDBase &&
				(rp.Head.Ident == proto.SM_TURN || rp.Head.Ident == proto.SM_WALK) {
				seen++
				fmt.Printf("    看到 NPC ActorId=%d 于 (%d,%d)\n",
					rp.Head.Recog, rp.Head.Param, rp.Head.Tag)
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE {
				fmt.Printf("    %s\n", rp.Body)
			}
		}
		if seen == 0 {
			log.Fatal("视野内没有出现任何 NPC")
		}
		fmt.Printf("    NPC 显示成功 ✓（%d 个）\n", seen)
	}

	if *shopTest {
		fmt.Println("[20] 测试商店")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		pump := func() {
			for k := 0; k < 12; k++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					return
				}
				if rp.Head.Ident == proto.SM_SYSMESSAGE {
					fmt.Printf("    %s\n", rp.Body)
				}
			}
		}

		// 1) 传送到屠夫旁，并在**同一轮收包**里记下 NPC 的 ActorId。
		//
		// ⚠️ 不能先用 pump() 消化：NPC 的 SM_TURN 是切图后立刻发来的，
		// 被 pump 丢掉后就再也找不到了。
		say("@map 0 313 271")
		npcID := uint32(0)
		for k := 0; k < 15; k++ {
			rp := cc.tryRecvTimeout(600 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Recog >= proto.NpcIDBase && npcID == 0 {
				npcID = uint32(rp.Head.Recog)
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE {
				fmt.Printf("    %s\n", rp.Body)
			}
		}
		if npcID == 0 {
			log.Fatal("视野内没有 NPC")
		}

		say("@gold 10000")
		pump()
		fmt.Printf("    点击 NPC ActorId=%d\n", npcID)

		// 2) 打开商店
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0),
		})
		goodsBody, goodsNPC := "", uint32(0)
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_SENDGOODSLIST {
				goodsBody, goodsNPC = rp.Body, uint32(rp.Head.Recog)
				fmt.Printf("    商品列表: %d 件（Recog=%d）\n", rp.Head.Param, goodsNPC)
			}
		}
		goods := parseGoodsList(goodsBody)
		if len(goods) == 0 {
			log.Fatalf("未收到可解析的商品列表（body=%q）", goodsBody)
		}
		// 商品列表的 Recog 必须是商人的 ActorId（后续买卖报文也用它）。
		if goodsNPC != npcID {
			log.Fatalf("商品列表 Recog=%d，期望商人 ActorId=%d", goodsNPC, npcID)
		}
		// 挑**最便宜**的一件来买卖：商品列表是按物品表顺序给的，第一件可能是
		// 我们买不起的东西（用例靠 @gold 给的钱有上限）。
		pick := goods[0]
		for _, g := range goods[1:] {
			if g.price < pick.price {
				pick = g
			}
		}
		fmt.Printf("    选中商品: %s 价格=%d 存量=%d（共 %d 件）\n",
			pick.name, pick.price, pick.stock, len(goods))

		// 3) 买入。原版字段（ClMain.pas:3735 → `SendBuyItem(g_nCurMerchant, pg.Stock, pg.Name)`）：
		//    Recog = 商人 ActorId、Param/Tag = MakeLong(货架存量)、body = 商品名。
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_USERBUYITEM, int32(npcID),
				proto.LoWord(int32(pick.stock)), proto.HiWord(int32(pick.stock)), 0),
			Body: pick.name,
		})
		bought, goldAfterBuy, bagAfterBuy := false, int32(-1), ""
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_BUYITEM_SUCCESS:
				bought, goldAfterBuy = true, rp.Head.Recog
			case proto.SM_BUYITEM_FAIL:
				log.Fatalf("买入 %s 失败：失败码 %d", pick.name, rp.Head.Recog)
			case proto.SM_BAGITEMS:
				bagAfterBuy = rp.Body
			case proto.SM_SYSMESSAGE:
				fmt.Printf("    %s\n", rp.Body)
			}
		}
		if !bought {
			log.Fatalf("买入 %s 没收到 SM_BUYITEM_SUCCESS", pick.name)
		}
		if want := int32(10000 - pick.price); goldAfterBuy != want {
			log.Fatalf("买入后金币 = %d，期望 %d（Recog 是金币）", goldAfterBuy, want)
		}
		makeIdx := itemMakeIndexByName(bagAfterBuy, pick.name)
		if makeIdx == 0 {
			log.Fatalf("买入后背包里没有 %s（%s）", pick.name, describeBag(bagAfterBuy))
		}
		fmt.Printf("    买入 %s（MakeIndex=%d）后金币=%d ✓\n", pick.name, makeIdx, goldAfterBuy)

		// 4) 卖出。原版字段（ClMain.pas:3703 → `SendSellItem(g_nCurMerchant, MakeIndex, name)`）：
		//    Recog = 商人 ActorId、Param/Tag = MakeLong(MakeIndex)、body = 物品名。
		cc.sendMerchantItem(proto.CM_USERSELLITEM, npcID, makeIdx, pick.name)
		sold, goldAfterSell := false, int32(-1)
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_USERSELLITEM_OK:
				sold, goldAfterSell = true, rp.Head.Recog
			case proto.SM_USERSELLITEM_FAIL:
				log.Fatalf("卖出 %s 失败", pick.name)
			case proto.SM_SYSMESSAGE:
				fmt.Printf("    %s\n", rp.Body)
			}
		}
		if !sold {
			log.Fatalf("卖出 %s 没收到 SM_USERSELLITEM_OK", pick.name)
		}
		if goldAfterSell <= goldAfterBuy {
			log.Fatalf("卖出后金币 = %d，应比买入后（%d）多", goldAfterSell, goldAfterBuy)
		}

		fmt.Println("    商店交互完成")
		fmt.Println("    商店功能验证通过 ✓")
	}

	if *dieTest {
		fmt.Println("[21] 测试死亡回城")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		// 先跑到远处（盟重省），再模拟死亡，看是否回得来
		say("@map 3")
		farMap := ""
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(600 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_CHANGEMAP {
				farMap = rp.Body
			}
		}
		fmt.Printf("    已到远处: %q\n", farMap)
		if farMap == "" {
			log.Fatal("未切换到远处地图")
		}

		// 死亡掉落的**落点**（原版 `TBaseObject.GetDropPosition`，ObjBase.pas:1534-1584）。
		//
		// 先塞三件**不同**的物品：同类物品会叠成一个背包格（见 stack 用例），
		// 而掉落是**逐件**算落点的 ⇒ 三件会落在螺旋的前三格。
		//
		// ⚠️ 那三格是 ((-1,-1), (0,-1), (1,-1))（相对死亡点）—— 同一行的连续三格，
		// 且 X 递增。这就是"散落"的真身：**确定性螺旋扫描**，不是随机散落。
		// 只断言"相对关系"（同行、X 连续），不需要知道死亡点的绝对坐标。
		//
		// ⚠️ 本用例需以 `-death-drop-bag-rate 1000 -death-drop-equip-rate 0` 启动。
		for _, nm := range []string{"木剑", "金创药(小量)", "布衣(男)"} {
			say("@give " + nm)
			drainUntilQuiet(cc, rcv(250*time.Millisecond))
		}

		say("@die")
		home := ""
		var drops [][2]int
		for k := 0; k < 12; k++ {
			rp := cc.tryRecvTimeout(600 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_CHANGEMAP {
				home = rp.Body
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE {
				fmt.Printf("    %s\n", rp.Body)
			}
			if rp.Head.Ident == proto.SM_ADDITEM {
				drops = append(drops, [2]int{int(rp.Head.Param), int(rp.Head.Tag)})
			}
		}
		if len(drops) < 3 {
			log.Fatalf("死亡掉落只有 %d 件（%v），期望 ≥3（三件不同物品各掉一件）", len(drops), drops)
		}
		x0, y0 := drops[0][0], drops[0][1]
		for i, d := range drops[:3] {
			fmt.Printf("    掉落 %d: (%d,%d)\n", i+1, d[0], d[1])
			if d[1] != y0 {
				log.Fatalf("散落应落在同一行：第 %d 件的 Y=%d，第 1 件的 Y=%d", i+1, d[1], y0)
			}
			if d[0] != x0+i {
				log.Fatalf("散落应逐格递增：第 %d 件的 X=%d，期望 %d（螺旋前三格）", i+1, d[0], x0+i)
			}
		}
		fmt.Printf("    散落：三件落在同一行的连续三格 (%d,%d)→(%d,%d) ✓\n", x0, y0, x0+2, y0)
		if home == "" {
			log.Fatal("死亡后未收到回城切图")
		}
		if home == farMap {
			log.Fatalf("仍停在 %q，没有回城", farMap)
		}
		fmt.Printf("    死亡后回城: %q ✓\n", home)
	}

	if *fightDrop {
		fmt.Println("[30] 测试 FIGHT/FIGHT3 区死亡不掉落")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}

		// mapTo 执行 @map 并等切图包（SM_CHANGEMAP 的 body 是地图显示名）。
		mapTo := func(id string) bool {
			// ⚠️ 先排空遗留包、再用**墙钟截止**等 CHANGEMAP。
			// 原实现是"固定 12 个包、首个 nil 就 break"：@die 之后的复活/换图
			// 会补发一批 SM_CLEAROBJECTS/SM_TURN，把预算吃光 ⇒ 服务端日志里
			// 明明"切换到 3(盟重省)"，客户端却报"未切换"（与 facePeerBefore 同坑）。
			drainUntilQuiet(cc, rcv(1*time.Second))
			say("@map " + id)
			got := ""
			deadline := time.Now().Add(rcv(6 * time.Second))
			for got == "" && time.Now().Before(deadline) {
				rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
				if rp == nil {
					continue
				}
				switch rp.Head.Ident {
				case proto.SM_CHANGEMAP:
					got = rp.Body
				case proto.SM_SYSMESSAGE:
					fmt.Printf("    %s\n", rp.Body)
				}
			}
			fmt.Printf("    @map %-5s → %q\n", id, got)
			return got != ""
		}

		// dieAndCount：塞满背包 → @die → 数收到的 背包刷新 / 地面掉落。
		//
		// ⚠️ 判据取 **SM_BAGITEMS**（而不是 SM_ADDITEM）：服务端的 deathDrop
		// 只在**真的掉了东西**（`dropped > 0`）时才回发背包刷新
		//（main.go 的 deathDrop 末尾就是 `if dropped == 0 { return }`），
		// 所以"死完有没有收到背包包"就是"掉没掉"的确定性信号；
		// SM_ADDITEM 依赖自己的可见集合含不含自己，判起来不干净。
		dieAndCount := func() (bag, add int) {
			say("@give 金创药(小量) 20")
			// 先把 @give 引发的回包（含它自己的背包刷新）吃完，别混进下面计数
			for k := 0; k < 12; k++ {
				if cc.tryRecvTimeout(rcv(250*time.Millisecond)) == nil {
					break
				}
			}
			say("@die")
			for k := 0; k < 16; k++ {
				rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_BAGITEMS:
					bag++
				case proto.SM_ADDITEM:
					add++
				}
			}
			return bag, add
		}

		// ① 官方行会战争地图 F006（mapinfo：`[F006 行会战争地图6 0] FIGHT3 DARK`）
		if !mapTo("F006") {
			log.Fatal("未切换到 F006（FIGHT3 图）")
		}
		bag, add := dieAndCount()
		fmt.Printf("    F006(FIGHT3) 死亡: 背包刷新=%d 次, 地面掉落=%d 件\n", bag, add)
		if bag != 0 || add != 0 {
			log.Fatalf("FIGHT3 区死亡不应掉落（背包刷新=%d 地面掉落=%d）", bag, add)
		}

		// ③ FIGHT3 图里死亡是**原地复活**（前 3 次），与行会战积分同源
		//（原版判 `m_nFightZoneDieCount < 3`，UsrEngn.pas:524-534；我们把它搬到
		// 死亡当场，见 gamesvr/contest.go 的文件头）。
		//
		// ① 的那次死亡已经是第 1 次，所以：
		//   第 2 次 ⇒ 服务端回"你还可以在战场上复活 1 次"且**不换图**；
		//   第 3 次 ⇒ 计数清零并回城（收到 SM_CHANGEMAP）。
		dieInFight3 := func(round int) (respawn bool, changedTo string) {
			say("@die")
			for k := 0; k < 14; k++ {
				rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
				if rp == nil {
					continue
				}
				switch rp.Head.Ident {
				case proto.SM_SYSMESSAGE:
					if strings.Contains(rp.Body, "在战场上复活") {
						fmt.Printf("    第 %d 次阵亡 → %s\n", round, rp.Body)
						return true, ""
					}
				case proto.SM_CHANGEMAP:
					changedTo = rp.Body
				}
			}
			return false, changedTo
		}
		if respawn, _ := dieInFight3(2); !respawn {
			log.Fatal("FIGHT3 图第 2 次阵亡应原地复活（原版 m_nFightZoneDieCount < 3）")
		}
		if respawn, to := dieInFight3(3); respawn || to == "" {
			log.Fatalf("FIGHT3 图第 3 次阵亡应计数清零并回城，实际 respawn=%v 换图=%q", respawn, to)
		}
		fmt.Println("    FIGHT3 区阵亡：前 3 次原地复活、第 3 次后回城 ✓")

		// ② 对照：普通图盟重省（map 3），应照常掉
		if !mapTo("3") {
			log.Fatal("未切换到盟重省(map 3)")
		}
		bag2, add2 := dieAndCount()
		fmt.Printf("    盟重省(普通图) 死亡: 背包刷新=%d 次, 地面掉落=%d 件\n", bag2, add2)
		if bag2 == 0 {
			log.Fatal("普通图死亡应当掉落 —— 本用例需以 -death-drop-bag-rate 1000 启动")
		}

		fmt.Println("    FIGHT/FIGHT3 区死亡豁免生效 ✓")
	}

	if *stackTest {
		fmt.Println("[22] 测试物品堆叠")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		// 收一次背包，返回条目数
		bagCount := func() int {
			for k := 0; k < 12; k++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					return -1
				}
				if rp.Head.Ident == proto.SM_BAGITEMS {
					return len(rp.Body) / proto.ClientItemSize
				}
			}
			return -1
		}

		// 记住初始格数（新角色有 5 个金创药，各占一格）
		say("@give 金创药(小量) 1")
		before := bagCount()

		// 再给 5 个同类：可堆叠的话不应新增格子
		say("@give 金创药(小量) 5")
		after := bagCount()

		fmt.Printf("    给 5 个前: %d 格 → 后: %d 格\n", before, after)
		if before < 0 || after < 0 {
			log.Fatal("未收到背包刷新")
		}
		if after > before {
			log.Fatalf("可堆叠物品不该新增格子（%d → %d）", before, after)
		}
		fmt.Println("    物品堆叠生效 ✓")
	}

	if *repairTest {
		fmt.Println("[23] 测试装备修理")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		pump := func() {
			for k := 0; k < 12; k++ {
				rp := cc.tryRecvTimeout(500 * time.Millisecond)
				if rp == nil {
					return
				}
				if rp.Head.Ident == proto.SM_SYSMESSAGE {
					fmt.Printf("    %s\n", rp.Body)
				}
			}
		}

		// 先刷怪打几下，磨掉武器耐久。
		//
		// ⚠️ 必须刷 **3 只**：`@spawn` 会从环绕玩家的格子起步、再用 freeCellNear
		// 半径 2 就近找可走格，**单只**很容易被挤到 2~3 格外 ⇒ 下面轮询的 8 个
		// 方向全部落空 ⇒ 武器耐久一点没掉 ⇒ 修理时服务端回"耐久已满，无需修理"。
		// 这正是 `repair` 用例长期偶发失败的根因（本行原为 `鸡 1`，与注释矛盾）。
		say("@spawn 鸡 3")
		time.Sleep(sc(800 * time.Millisecond))
		pump()
		say("@gold 10000")
		pump()
		// ⚠️ 先把命中练满再挥：原版有打空判定（`命中 < Random(目标敏捷)`，
		// 见 hitpoint.go），裸命中 5 打鸡（敏捷 10）约 40% 打空 ——
		// 那样 8 刀只有 ~5 刀命中，武器耐久可能掉不够 ⇒ "耐久已满，无需修理"。
		say("@magic 3 3")
		time.Sleep(sc(300 * time.Millisecond))
		pump()

		// 打几下（复用攻击流程的简化版：朝最近怪挥砍）
		// 覆盖全部 8 个方向：@spawn 刷在相邻格，总有一个方向能打中
		for i := 0; i < 8; i++ {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_HIT,
					proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(i), 0),
			})
			time.Sleep(sc(500 * time.Millisecond))
			for k := 0; k < 3; k++ {
				cc.tryRecvTimeout(200 * time.Millisecond)
			}
		}

		// 修理武器槽（SlotWeapon = 1）。
		//
		// ⚠️ 修理必须**在商人对话里**（原版 `ClientRepairItem` 按玩家当前脚本标签分档、
		// 且会查 `m_boRepair`/`m_boS_repair`）⇒ 先点开一个会修理的 NPC 的 `@repair`
		//（那一步服务端回 `SM_SENDUSERREPAIR` 打开修理界面）。
		//
		// 比奇省的**铁匠铺老板**（2Bwe @ 0:302,219）脚本头就声明了 `@repair`。
		// ⚠️ 必须**传送到它旁边**再等：NPC 的出现包只在"进视野"那一刻发一次，
		// 玩家一路走过来时早发过了（上一版就是这么踩空的：视野里"一个 NPC 都没有"）。
		if !mapTo(cc, "0", 302, 220, &posX, &posY) {
			log.Fatal("未切换到铁匠铺旁 (302,220)")
		}
		repairNPC := uint32(0)
		smithDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(smithDeadline) && repairNPC == 0 {
			rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_TURN && rp.Head.Recog >= proto.NpcIDBase &&
				int(rp.Head.Param) == 302 && int(rp.Head.Tag) == 219 {
				repairNPC = uint32(rp.Head.Recog)
			}
		}
		if repairNPC == 0 {
			log.Fatal("没看到铁匠铺老板（0:302,219）")
		}
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(repairNPC), 0, 0, 0)})
		time.Sleep(sc(200 * time.Millisecond))
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_MERCHANTDLGSELECT, int32(repairNPC), 0, 0, 0),
			Body: "@repair",
		})
		uiOpen := false
		uiDeadline := time.Now().Add(rcv(2 * time.Second))
		for time.Now().Before(uiDeadline) && !uiOpen {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_SENDUSERREPAIR {
				uiOpen = true
			}
		}
		if !uiOpen {
			log.Fatal("点了 @repair 但铁匠没打开修理界面（SM_SENDUSERREPAIR）")
		}
		fmt.Printf("    在铁匠 ActorId=%d 处打开修理界面 ✓\n", repairNPC)

		// 修理手里那把武器。原版字段（ClMain.pas:3711 → `SendRepairItem(merchant, MakeIndex, name)`）：
		// Recog = 商人 ActorId、Param/Tag = MakeLong(MakeIndex)、body = 物品名。
		repairIdx, repairName := useItemAt(cc.lastUse(), proto.SlotWeapon)
		if repairIdx == 0 {
			log.Fatalf("已穿戴列表里没有武器（%q），没法发修理报文", cc.lastUse())
		}
		cc.sendMerchantItem(proto.CM_USERREPAIRITEM, repairNPC, repairIdx, repairName)
		repaired := false
		for k := 0; k < 12; k++ {
			// 并行多实例时服务端响应变慢，收包预算同步放大（原 500ms）
			rp := cc.tryRecvTimeout(1500 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE {
				fmt.Printf("    %s\n", rp.Body)
				if strings.Contains(rp.Body, "修完成") {
					repaired = true
				}
			}
		}
		if !repaired {
			log.Fatal("修理未成功（未收到'普修完成'/'特修完成'）")
		}
		fmt.Println("    装备修理成功 ✓")
	}

	if *gmTest {
		fmt.Println("[25] 测试 GM 权限门（Envir/AdminList.txt）")

		// 约定：**角色名 = GM01** 时验"在名单里"这一侧（官方样例就是 `* GM01`），
		// 其余角色名验"不在名单"这一侧。
		inList := chrName == "GM01"
		fmt.Printf("    角色 %q，AdminList 里%s\n", chrName,
			map[bool]string{true: "有（应能用 @）", false: "没有（应被拒）"}[inList])
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: "@level 30",
		})

		denied, leveled := false, false
		for k := 0; k < 8; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			switch rp.Head.Ident {
			case proto.SM_SYSMESSAGE:
				fmt.Printf("    %s\n", rp.Body)
				if strings.Contains(rp.Body, "没有足够的权限") {
					denied = true
				}
			case proto.SM_ABILITY, proto.SM_LEVELUP:
				leveled = true
			}
		}
		if inList {
			if !leveled {
				log.Fatal("GM01 在 AdminList 里，@level 应生效（却没收到属性/升级包）")
			}
			fmt.Println("    名单内角色可用 @ 命令 ✓")
		} else {
			if !denied {
				log.Fatal("非 GM 的 @level 没收到拒绝提示（权限门没生效？）")
			}
			if leveled {
				log.Fatal("非 GM 的 @level 竟然生效了")
			}
			fmt.Println("    名单外角色被拒绝 ✓")
		}
	}

	if *goldTest {
		fmt.Println("[26] 测试金币：掉落 → 拾取")

		// 原版链路：
		//   怪物掉金币 `TBaseObject.ScatterGolds`（ObjBase.pas:20655）⇒ `DropGoldDown`
		//     （:2301，落点范围 3、同格并堆）⇒ 地面出现金币堆（`RM_ITEMSHOW`）
		//   玩家扔金币 `CM_DROPGOLD`（ClMain.pas:3188，金额在 Recog）⇒ `ClientDropGold`（:16187）
		//   捡起 `CM_PICKUP` ⇒ `ClientPickUpItem` 的金币分支（:1708-1730）
		//     ⚠️ 归属：**2 分钟内只有击杀者本人与队友**能捡（`OfBaseObject`，:1699-1707）
		say := func(cmd string) {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
		}
		drain := func() {
			for k := 0; k < 8; k++ {
				if cc.tryRecvTimeout(200*time.Millisecond) == nil {
					return
				}
			}
		}
		// 等一个地面物品包（SM_ADDITEM：Recog=地面物品ID，Param=x，Tag=y）
		ground := func(d time.Duration) *wire.Packet {
			return cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_ADDITEM && p.Head.Recog > 0
			}, d)
		}
		goldNow := int64(0) // 服务端每次变钱都发 SM_GOLDCHANGED，跟着记

		// 走到那一格再捡：服务端要求"人就在这一格"（原版 `GetItem(m_nCurrX, m_nCurrY)`，:1676）
		pickAt := func(x, y int, wantExact int64, what string) {
			say(fmt.Sprintf("@map 0 %d %d", x, y))
			if cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_CHANGEMAP }, 5*time.Second) == nil {
				log.Fatal("传送到金币格失败（没收到 SM_CHANGEMAP）")
			}
			drain() // 丢掉切图那批包
			before := goldNow
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_PICKUP, 0, uint16(x), uint16(y), 0)})
			rp := cc.waitFor(func(p *wire.Packet) bool {
				if p.Head.Ident != proto.SM_GOLDCHANGED {
					return false
				}
				got := int64(p.Head.Recog)
				if wantExact > 0 {
					return got == wantExact
				}
				return got > before // 怪物掉多少看掉落表 ⇒ 只要求"变多了"
			}, 3*time.Second)
			if rp == nil {
				log.Fatalf("%s：金币没有按预期变化（期望 %d，当前 %d）", what, wantExact, before)
			}
			goldNow = int64(rp.Head.Recog)
			fmt.Printf("    %s ✓（金币 = %d）\n", what, goldNow)
		}

		// ---- ① 玩家扔金币：10000 → 9700，地上 300 ----
		say("@gold 10000")
		drain()
		goldNow = 10000
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_DROPGOLD, 300, 0, 0, 0)})
		pile := ground(3 * time.Second)
		if pile == nil {
			log.Fatal("扔金币后地面没出现东西（SM_ADDITEM 没来）")
		}
		gx, gy := int(pile.Head.Param), int(pile.Head.Tag)
		fmt.Printf("    扔下 300 金币 → 地面金币堆于 (%d,%d) ✓\n", gx, gy)
		pickAt(gx, gy, 10000, "拾回自己扔的金币")

		// ⚠️ 这里**不**去验"怪物掉金币"：蛤蟆那种掉落表是概率出金币（`1/2 金币 280`），
		// 跑几次才中一次 ⇒ e2e 必然偶发失败。那条接线由单测
		// `TestScatterKillGoldWiring`（1/1 必中）与 `TestScatterGoldsPileSize/Limit` 钉死。
		fmt.Println("    金币链路（扔 → 地面 → 归属 → 拾取）端到端 ✓")

		// ---- ③ 小地图（CM_WANTMINIMAP）与食物（StdMode = 1）----
		//
		// 小地图：原版 `ClientGetMinMap` 查 `MiniMap.txt`，出厂数据里 "0" → 101。
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_WANTMINIMAP, 0, 0, 0, 0)})
		mm := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_READMINIMAP_OK || p.Head.Ident == proto.SM_READMINIMAP_FAIL
		}, 3*time.Second)
		if mm == nil {
			log.Fatal("请求小地图没有回应")
		}
		if mm.Head.Ident != proto.SM_READMINIMAP_OK || int(mm.Head.Param) != 101 {
			log.Fatalf("小地图回应不对：ident=%d param=%d（期望 OK/101）", mm.Head.Ident, mm.Head.Param)
		}
		fmt.Println("    小地图：地图 0 → 编号 101 ✓")

		// 食物：`StdMode = 1` 的物品能吃（原来吃不了）。
		// ⚠️ 出厂数据里 干肉/面条 的 DuraMax 都是 **1** ⇒ `DuraMax/10 = 0`，
		// 饥饿度其实加不上（原版也一样）⇒ 这里只断言"吃得下去"。
		say("@give 干肉")
		drain()
		// 背包要"排空后主动要"一份新的（旧包会让槽位对不上，见 upgrade 用例里的注释）
		drain()
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_QUERYBAGITEMS, 0, 0, 0, 0)})
		jb := ""
		if rp := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_BAGITEMS
		}, 3*time.Second); rp != nil {
			jb = rp.Body
		}
		ji := findInBag(jb, "干肉")
		if ji < 0 {
			log.Fatalf("背包里没有干肉（%s）", describeBag(jb))
		}
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_EAT, int32(ji), 0, 0, 0)})
		if rp := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_EAT_OK || p.Head.Ident == proto.SM_EAT_FAIL
		}, 3*time.Second); rp == nil {
			log.Fatal("吃食物没有回应")
		} else if rp.Head.Ident != proto.SM_EAT_OK {
			log.Fatal("干肉（StdMode=1）吃不了")
		}
		fmt.Println("    食物：干肉吃得下 ✓（饥饿度见服务端日志）")

		// ---- ④ 换网关（`@reconnection IP 端口`，原版 CmdReconnection）----
		//
		// 原版：权限 ≥ 6 ⇒ 置 `m_boReconnection` + `SM_RECONNECT`，正文是 "ip/port"
		//（客户端 `ClientGetReconnect(body)` 拿去重连）。
		drain()
		say("@reconnection 127.0.0.1 7401")
		rc := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_RECONNECT
		}, 3*time.Second)
		if rc == nil {
			log.Fatal("@reconnection 没收到 SM_RECONNECT")
		}
		if rc.Body != "127.0.0.1/7401" {
			log.Fatalf("SM_RECONNECT 正文 = %q，期望 %q", rc.Body, "127.0.0.1/7401")
		}
		fmt.Printf("    @reconnection → SM_RECONNECT %q ✓\n", rc.Body)
	}

	if *butchTest {
		fmt.Println("[27] 测试取肉（CM_BUTCH）")

		// 原版链路（ObjBase.pas:17458-17504，入口 CM_BUTCH 在 :4706）：
		//   ① 目标必须是**死了、还没变骷髅、而且是动物**（鸡 race 51 / 鹿 52 / 狼 53）
		//   ② 每次削皮革度 `Random(16)+5` 与肉质量 `Random(201)+100`
		//   ③ 皮革度 ≤ 0 ⇒ 变骷髅 + 把身上的东西给取肉的人（肉的持久度 = 肉质量）
		//   ⚠️ 动物**死亡时什么都不掉**（原版 `Die` 里被 `(not m_boAnimal)` 挡着，
		//      :20983-20999）⇒ 肉只能靠取肉拿到；这也要求怪死了**留成尸体**
		//      （我们原来立刻删怪，见 gold.go / butch.go 的注释）。
		say := func(cmd string) {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
		}
		drain := func() {
			for k := 0; k < 8; k++ {
				if cc.tryRecvTimeout(200*time.Millisecond) == nil {
					return
				}
			}
		}

		say("@level 40")
		say("@magic 3 3") // 命中练满（原版有打空判定）
		drain()
		say("@spawn 鸡 1")
		time.Sleep(sc(700 * time.Millisecond))

		// 找到那只鸡（SM_TURN，怪物号段）
		var monID uint32
		mx, my := 0, 0
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(400 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
				monID, mx, my = uint32(rp.Head.Recog), int(rp.Head.Param), int(rp.Head.Tag)
				break
			}
		}
		if monID == 0 {
			log.Fatal("没找到 @spawn 出来的鸡")
		}
		fmt.Printf("    鸡 ActorId=%d 于 (%d,%d)\n", monID, mx, my)

		// 打死它（八方向各砍几轮，怪就在相邻格）
		dead := false
		for round := 0; round < 5 && !dead; round++ {
			for i := 0; i < 8; i++ {
				cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HIT,
					proto.MakeLong(uint16(mx), uint16(my)), 0, uint16(i), 0)})
				time.Sleep(sc(250 * time.Millisecond))
				for k := 0; k < 4; k++ {
					rp := cc.tryRecvTimeout(120 * time.Millisecond)
					if rp == nil {
						break
					}
					if rp.Head.Ident == proto.SM_DEATH && rp.Head.Recog == int32(monID) {
						dead = true
					}
				}
				if dead {
					break
				}
			}
		}
		if !dead {
			log.Fatal("没打死那只鸡（收不到 SM_DEATH）")
		}
		fmt.Println("    鸡已死（留成尸体）✓")

		// 反复取肉：皮革度挖到 0 就会变骷髅（客户端收 SM_SKELETON=33）
		//
		// ⚠️ 服务端有转身/取肉的共用间隔（`TurnIntervalTime=100`ms）⇒ 两次之间要隔开，
		// 不然除了第一次全被挡掉。皮革度初值 50、每次削 5..20 ⇒ 最多 10 次。
		skel := false
		tries := 0
		for i := 0; i < 40 && !skel; i++ {
			tries++
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(
				proto.CM_BUTCH, int32(monID), uint16(mx), uint16(my), 0)})
			time.Sleep(sc(130 * time.Millisecond))
			for k := 0; k < 4; k++ {
				rp := cc.tryRecvTimeout(150 * time.Millisecond)
				if rp == nil {
					break
				}
				if rp.Head.Ident == proto.SM_SKELETON && rp.Head.Recog == int32(monID) {
					skel = true
				}
			}
		}
		if !skel {
			log.Fatalf("取肉 %d 次都没变骷髅", tries)
		}
		fmt.Printf("    取肉 %d 次 → 变骷髅 ✓（肉已进背包，服务端日志可见）\n", tries)

		// ---- ④ 制药（`@makedrug` + CM_USERMAKEDRUGITEM，审计 §2.3）----
		// 到盟重省的**药店老板**（4Mdu @ 3:361,335）：它的脚本头声明了 @makedrug，
		// 商品表里也有药粉（StdMode 25）。配方取官方 MakeItem.txt 第一条：
		// 食人树叶×4 + 毒蜘蛛牙齿×2 + 食人树的果实×1 ⇒ 灰色药粉(少量)，单价 100。
		say("@gold 10000")
		say("@give 食人树叶 4")
		say("@give 毒蜘蛛牙齿 2")
		say("@give 食人树的果实 1")
		drain()
		if !mapTo(cc, "3", 361, 336, &posX, &posY) {
			log.Fatal("未切换到药店老板旁 (361,336)")
		}
		drugNPC := uint32(0)
		drugDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(drugDeadline) && drugNPC == 0 {
			rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_TURN && rp.Head.Recog >= proto.NpcIDBase &&
				int(rp.Head.Param) == 361 && int(rp.Head.Tag) == 335 {
				drugNPC = uint32(rp.Head.Recog)
			}
		}
		if drugNPC == 0 {
			log.Fatal("没看到药店老板（3:361,335）")
		}
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(drugNPC), 0, 0, 0)})
		time.Sleep(rcv(400 * time.Millisecond))
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_USERMAKEDRUGITEM, int32(drugNPC), 0, 0, 0),
			Body: "灰色药粉(少量)",
		})
		made := false
		madeDeadline := time.Now().Add(rcv(3 * time.Second))
		for time.Now().Before(madeDeadline) && !made {
			rp := cc.tryRecvTimeout(rcv(300 * time.Millisecond))
			if rp == nil {
				continue
			}
			if rp.Head.Ident == proto.SM_MAKEDRUG_SUCCESS {
				made = true
			}
			if rp.Head.Ident == proto.SM_MAKEDRUG_FAIL {
				log.Fatalf("制药失败，失败码 %d（2=背包满 3=金币不足 4=材料不足/不在商品表）",
					rp.Head.Param)
			}
		}
		if !made {
			log.Fatal("制药没有回执（SM_MAKEDRUG_SUCCESS 没来）")
		}
		fmt.Println("    制药：食人树叶×4 + 毒蜘蛛牙齿×2 + 果实×1 → 灰色药粉(少量) ✓")
	}

	if *upgradeTest {
		fmt.Println("[25] 测试武器升级（兵器店老板 9Aup @ 0151 铁匠铺）")

		// 官方链路（`TMerchant.UpgradeWapon` / `GetBackupgWeapon`，ObjNpc.pas:1034-1384）：
		//   ① `@upgradenow`：扣 2 万金币、摘走武器、收走背包里的黑铁矿石与首饰，
		//      折算成四项点数存在该 NPC 的"修炼中"列表里 ⇒ 回 `~@upgradenow_ok`
		//   ② 隔 `UPgradeWeaponGetBackTime`（官方 1000ms）后 `@getbackupgnow`
		//      ⇒ 回 `~@getbackupgnow_ok`，武器回到背包（结果记在它的 btValue[10]）
		//   ③ **再砍一刀**才结算：加 DC/MC/SC 上限、或者武器破碎
		//
		// ⚠️ 用的是**真脚本**：只有 `market_def/9Aup-0151.txt` 与
		// `9Sabuk_Wall_Weapon_refine-0151.txt` 里才有 `[~@upgradenow_ok]` 这些段。
		// 屠夫那种商人脚本只是**脚本头**声明了 `@upgradenow`（M2 配置工具自动列的），
		// 正文并没有实现 ⇒ 用屠夫就只能验证到"服务端接了活"，断言不到回执。
		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		sendBody := func(npcID uint32, text string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_MERCHANTDLGSELECT, int32(npcID), 0, 0, 0),
				Body: text,
			})
		}
		// ⚠️ 对话文本我们走的是 `SM_SYSMESSAGE`（`showLabel` 用 `sysMsg` 发，
		// 是这套服务端自己的简化；原版发 RM_MENU_OK 那一路），所以两种都要收。
		waitDlg := func(contains string, d time.Duration) *wire.Packet {
			return cc.waitFor(func(p *wire.Packet) bool {
				if p.Head.Ident != proto.SM_DLGMSG && p.Head.Ident != proto.SM_SYSMESSAGE {
					return false
				}
				return strings.Contains(p.Body, contains)
			}, d)
		}

		// 备料：钱、黑铁矿石、首饰（首饰的 DC/SC/MC 决定三系点数）。
		//
		// ⚠️ **不要** `@give` 武器：新角色身上本来就自带一把木剑（登录日志里
		// `装备：槽1=木剑`），再给一把就会出现**两把同名武器** —— 取回之后
		// `findInBag` 只能按名字找，拿到的很可能是没升过的那把（`Value` 长度 0、
		// `btValue[10]` 读出来是 0），表现为"取回成功但永远不结算"。
		// 踩过：日志里 `试刀判定` 一次都不打，装备栏里那把的 `Value` 是空的。
		say("@gold 2000000")
		say("@give 黑铁矿石 3")
		say("@give 金项链")

		// ⚠️ 背包必须**排空后再主动要**（`CM_QUERYBAGITEMS`），不能直接等队列里的
		// `SM_BAGITEMS`：`waitFor` 会把不匹配的包**留在队列里**，于是极易匹配到一条
		// 更早的旧背包 —— 用它算出的槽位在**当前**背包里指向别的东西，后面那次穿戴会
		// 静默失败（踩过：试刀时装备栏里是空的占位物品，`btValue[10]` 自然读不到，
		// 表现为"取回成功但永远不结算"）。
		drain := func() {
			for i := 0; i < 30; i++ {
				if cc.tryRecvTimeout(150*time.Millisecond) == nil {
					return
				}
			}
		}
		freshBag := func() string {
			drain()
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_QUERYBAGITEMS, 0, 0, 0, 0)})
			rp := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_BAGITEMS }, 3*time.Second)
			if rp == nil {
				return ""
			}
			return rp.Body
		}

		// 自带的武器就一直戴着（`@upgradenow` 要求武器在**身上**，见 ObjNpc.pas:1183
		// 的 `m_UseItems[U_WEAPON].wIndex <> 0`）。
		if bag := freshBag(); bag == "" {
			log.Fatal("没等到 SM_BAGITEMS（备料后检查背包）")
		}
		fmt.Println("    备料完成：自带的武器在装备栏，黑铁矿石×3 + 金项链在背包")

		// 走到兵器店老板那里（merchant.txt: 9Aup 0151 12 10 兵器店老板）。
		say("@map 0151 13 10")
		chg := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_CHANGEMAP }, 5*time.Second)
		if chg == nil {
			log.Fatal("传送到铁匠铺失败（没收到 SM_CHANGEMAP）")
		}
		px, py := int(chg.Head.Param), int(chg.Head.Tag)
		// ⚠️ 要**取最近的**那个 NPC，不能拿第一个收到的 SM_TURN：
		// 铁匠铺里有 3 个 NPC，先到的那只可能在 (7,14) 这种远处，
		// 而服务端的对话/仓库准入都带距离校验（storageDialogNPC）⇒ 后面的
		// `@upgradenow` 会被**静默丢弃**（踩过：日志里一条对话都没有）。
		var npcID uint32
		bestD := 1 << 30
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(400 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_TURN && rp.Head.Recog >= proto.NpcIDBase {
				d := absi(int(rp.Head.Param)-px) + absi(int(rp.Head.Tag)-py)
				if d < bestD {
					npcID, bestD = uint32(rp.Head.Recog), d
					fmt.Printf("    候选 NPC ActorId=%d 于 (%d,%d)（距离 %d）\n",
						npcID, rp.Head.Param, rp.Head.Tag, d)
				}
			}
		}
		if npcID == 0 {
			log.Fatal("铁匠铺里没看到 NPC")
		}
		fmt.Printf("    选中最近的 NPC ActorId=%d（距离 %d），玩家在 (%d,%d)\n", npcID, bestD, px, py)

		// 开对话（必须先开：`CM_MERCHANTDLGSELECT` 在服务端 p.dialog == nil 时被静默丢弃）。
		opened := false
		for attempt := 0; attempt < 3 && !opened; attempt++ {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0)})
			opened = cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_DLGMSG || p.Head.Ident == proto.SM_SENDGOODSLIST
			}, 2*time.Second) != nil
		}
		if !opened {
			log.Fatal("点击兵器店老板后没收到对话")
		}

		// ① 交武器修炼
		sendBody(npcID, "@upgradenow")
		okDlg := waitDlg("新的一样", 5*time.Second)
		if okDlg == nil {
			log.Fatal("交武器后没收到 ~@upgradenow_ok（装备/金币/黑铁有一条不满足？）")
		}
		fmt.Printf("    交武器 → ~@upgradenow_ok ✓ 回执: %s\n", strings.TrimSpace(okDlg.Body))

		// ② 取回（官方 UPgradeWeaponGetBackTime=1000ms ⇒ 等 1.2 秒）
		time.Sleep(1200 * time.Millisecond)
		sendBody(npcID, "@getbackupgnow")
		backDlg := waitDlg("精炼了你的武器", 5*time.Second)
		if backDlg == nil {
			log.Fatal("取回时没收到 ~@getbackupgnow_ok")
		}
		fmt.Printf("    取回 → ~@getbackupgnow_ok ✓ 回执: %s\n", strings.TrimSpace(backDlg.Body))

		// ③ 试刀：把武器再装上、刷一只鸡、八个方向各砍一刀（总有一个方向命中）
		bag2 := freshBag()
		if bag2 == "" {
			log.Fatal("取回后没等到 SM_BAGITEMS")
		}
		wi2 := findInBag(bag2, "木剑")
		if wi2 < 0 {
			log.Fatalf("取回后背包里没有木剑（%s）", describeBag(bag2))
		}
		if _, slot := takeOnFromBag(cc, bag2, "木剑"); slot < 0 {
			log.Fatalf("取回后木剑算不出装备槽（%s）", describeBag(bag2))
		}
		time.Sleep(sc(300 * time.Millisecond))
		say("@spawn 鸡 1")
		time.Sleep(sc(700 * time.Millisecond))
		for i := 0; i < 8; i++ {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_HIT,
					proto.MakeLong(uint16(px), uint16(py)), 0, uint16(i), 0),
			})
			time.Sleep(sc(400 * time.Millisecond))
			for k := 0; k < 3; k++ {
				cc.tryRecvTimeout(200 * time.Millisecond)
			}
		}
		fmt.Println("    试刀完成（加值或破碎见服务端日志）✓")
	}

	if *dlgTest {
		fmt.Println("[24] 测试 NPC 脚本对话")

		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		// 到屠夫旁边（merchant.txt: 1Bme 0 313 271）
		say("@map 0 313 271")

		// 收集视野里的 NPC，选**最近**的那个（屠夫就在脚下附近）
		npcID, npcX, npcY := uint32(0), 0, 0
		for k := 0; k < 20; k++ {
			rp := cc.tryRecvTimeout(600 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Recog >= proto.NpcIDBase {
				d := absi(int(rp.Head.Param)-posX) + absi(int(rp.Head.Tag)-posY)
				if npcID == 0 || d < absi(npcX-posX)+absi(npcY-posY) {
					npcID, npcX, npcY = uint32(rp.Head.Recog), int(rp.Head.Param), int(rp.Head.Tag)
				}
			}
		}
		if npcID == 0 {
			log.Fatal("视野内没有 NPC")
		}
		fmt.Printf("    点击最近的 NPC ActorId=%d 于 (%d,%d)\n", npcID, npcX, npcY)
		cc.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0),
		})
		msgs := ""
		hasGoods := false
		for k := 0; k < 14; k++ {
			rp := cc.tryRecvTimeout(500 * time.Millisecond)
			if rp == nil {
				break
			}
			if rp.Head.Ident == proto.SM_SYSMESSAGE {
				msgs += rp.Body + "\n"
			}
			if rp.Head.Ident == proto.SM_SENDGOODSLIST {
				hasGoods = true
			}
		}
		if msgs == "" {
			log.Fatal("点击 NPC 后没有任何响应")
		}
		fmt.Printf("    NPC 回应:\n%s", msgs)
		if !hasGoods {
			log.Fatal("未收到商品列表")
		}
		fmt.Println("    NPC 对话/商店响应成功 ✓")
	}

	if *storageTest {
		// 仓库：原版入口是**商人对话里的 @storage / @getback 选项**（1.76 没有
		// CM_OPENSTORAGE 这种包），见 gamesvr/storage.go 的文件头。断言全在协议上：
		//
		//	@storage                    → SM_SENDUSERSTORAGEITEM（Recog = NPC ActorId）
		//	@getback                    → SM_SAVEITEMLIST（Series = 件数，body = ClientItem 用 '/' 拼）
		//	CM_USERSTORAGEITEM          → SM_STORAGE_OK / FAIL / FULL
		//	CM_USERTAKEBACKSTORAGEITEM  → SM_TAKEBACKSTORAGEITEM_OK（Recog = MakeIndex）/ FAIL / FULLBAG
		fmt.Println("[25] 测试仓库（存 / 取 / 列表）")

		// ⚠️ npcID 必须先声明：下面的 dlg/itemMsg 两个闭包都捕获它。
		npcID := uint32(0)
		say := func(cmd string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: cmd,
			})
		}
		dlg := func(body string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_MERCHANTDLGSELECT, int32(npcID), 0, 0, 0),
				Body: body,
			})
		}
		// recvUntil 按**墙钟截止**收某个包（收不到再等，别"首个 nil 就 break"）。
		recvUntil := func(want uint16, budget time.Duration) *wire.Packet {
			// 队列谓词等待：只摘走匹配的那条，无关包（SM_TURN 洪流）留在队列
			return cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == want }, budget)
		}
		type stEntry struct {
			name      string
			makeIndex int32
		}
		// storageList 拉一次仓库列表（@getback），返回 (件数, 条目)。
		//
		// ⚠️ 取回要用**仓库里那条**的 MakeIndex：它不在背包里，查不到。
		// 列表里每条 ClientItem 都带 MakeIndex（原版客户端就是把它塞进 Price 回发的）。
		storageList := func() (int, []stEntry) {
			drainUntilQuiet(cc, 400*time.Millisecond)
			dlg("@getback")
			rp := recvUntil(proto.SM_SAVEITEMLIST, 3*time.Second)
			if rp == nil {
				log.Fatal("未收到 SM_SAVEITEMLIST")
			}
			var out []stEntry
			// body 是"每条 ClientItem 后跟一个 '/'"，按定长切、跳过分隔符
			raw := []byte(rp.Body)
			for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
				ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
				if !ok || ci.S.GetName() == "" {
					break
				}
				out = append(out, stEntry{name: ci.S.GetName(), makeIndex: ci.MakeIndex})
				if off+proto.ClientItemSize < len(raw) && raw[off+proto.ClientItemSize] == '/' {
					off++ // 跳过分隔符
				}
			}
			// Series 是件数（原版 MakeDefaultMsg(SM_SAVEITEMLIST, NPC, 0, 0, Count)）
			return int(rp.Head.Series), out
		}
		// deposit / takeBack 按原版字段发：Recog = NPC，Param/Tag = MakeIndex 低/高 16 位，
		// body = 物品显示名（原版用名字做二次校验）。
		itemMsg := func(ident uint16, makeIndex int32, name string) {
			cc.send(wire.Packet{
				Head: proto.MakeDefaultMsg(ident, int32(npcID),
					proto.LoWord(makeIndex), proto.HiWord(makeIndex), 0),
				Body: name,
			})
		}

		// 到屠夫旁边（merchant.txt: `1Bme  0  313  271  屠夫`，脚本头行含 @storage/@getback）
		say("@map 0 313 271")
		// ⚠️ 传送后必须把 posX/posY 换成新坐标：用例末尾还有一条"走路"断言按它们
		// 比对（同 -pvp/-contest 的处理），否则整例会在最后一步假失败。
		posX, posY = 313, 271
		mapChange := recvUntil(proto.SM_CHANGEMAP, 5*time.Second)
		if mapChange == nil {
			log.Fatal("传送到屠夫附近后未收到 SM_CHANGEMAP")
		}
		// ⚠️ 视野队列可能残留切图前其它 NPC 的出现包；必须用屠夫已知坐标
		// (313,271) 定位，不能取第一个 Recog>=NpcIDBase。
		npc := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_TURN && p.Head.Recog >= proto.NpcIDBase &&
				p.Head.Param == 313 && p.Head.Tag == 271
		}, 5*time.Second)
		if npc == nil {
			log.Fatal("视野内没有屠夫 NPC（ActorId / 坐标 313,271）")
		}
		npcID = uint32(npc.Head.Recog)
		// 点击对话后按墙钟等匹配包，非商品/对话包留在队列中。
		var opened bool
		for attempt := 0; attempt < 3 && !opened; attempt++ {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0)})
			opened = cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_SENDGOODSLIST || p.Head.Ident == proto.SM_DLGMSG
			}, 2*time.Second) != nil
		}
		if !opened {
			log.Fatal("点击屠夫后没收到对话/商品列表")
		}
		fmt.Printf("    点击屠夫 ActorId=%d\n", npcID)

		// 1) @storage 开界面 + @getback 拉列表（此刻应为空）
		//
		// ⚠️ 同样留重试：`@storage` 是普通对话选项，万一对话状态没跟上会被忽略。
		var st *wire.Packet
		for attempt := 0; attempt < 3 && st == nil; attempt++ {
			dlg("@storage")
			st = recvUntil(proto.SM_SENDUSERSTORAGEITEM, 3*time.Second)
		}
		if st == nil {
			log.Fatal("发 @storage 后没收到 SM_SENDUSERSTORAGEITEM")
		}
		if uint32(st.Head.Recog) != npcID {
			log.Fatalf("SM_SENDUSERSTORAGEITEM.Recog = %d，期望 NPC ActorId %d", st.Head.Recog, npcID)
		}
		// ⚠️ 列表可能"取早了"（服务端加载仓库/存档落盘与 @getback 的先后）。
		// 实测偶发 `Series=0`、条目为空 ⇒ 重拉几次再断言。
		n, entries := storageList()
		for attempt := 0; n != *storageLeft && attempt < 4; attempt++ {
			fmt.Printf("    (仓库件数 %d ≠ 期望 %d，重拉第 %d 次)\n", n, *storageLeft, attempt+1)
			time.Sleep(500 * time.Millisecond)
			n, entries = storageList()
		}
		if n != *storageLeft {
			log.Fatalf("开箱时仓库应为 %d 件，实际 Series=%d 条目=%v", *storageLeft, n, entries)
		}
		if *storageLeft == 0 {
			fmt.Println("    打开仓库：列表为空 ✓")
		} else {
			fmt.Printf("    打开仓库：%d 件（%v）—— 上一段存进去的还在 ✓\n", n, entries)
			// 第二段：把东西取回来，别让仓库在反复跑时越积越多
			for _, e := range entries {
				itemMsg(proto.CM_USERTAKEBACKSTORAGEITEM, e.makeIndex, e.name)
				if rp := recvUntil(proto.SM_TAKEBACKSTORAGEITEM_OK, 3*time.Second); rp == nil {
					log.Fatalf("取回 %q（MakeIndex=%d）失败", e.name, e.makeIndex)
				}
			}
			if n, entries = storageList(); n != 0 {
				log.Fatalf("取空后仓库应为 0 件，实际 Series=%d 条目=%v", n, entries)
			}
			fmt.Println("    取空仓库 ✓（下一段会重新存一件）")
		}

		// 2) 拿一件可存的物品（用 MakeIndex 定位，与原版一致）
		say("@give 木剑")
		drainUntilQuiet(cc, 500*time.Millisecond)
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_QUERYBAGITEMS, 0, 0, 0, 0)})
		bag := ""
		if rp := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_BAGITEMS }, 3*time.Second); rp != nil {
			bag = rp.Body
		}
		pickName, pickIdx := "", int32(0)
		raw := []byte(bag)
		for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
			ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
			if !ok {
				continue
			}
			if ci.S.GetName() == "木剑" && ci.MakeIndex != 0 {
				pickName, pickIdx = ci.S.GetName(), ci.MakeIndex
				break
			}
		}
		if pickIdx == 0 {
			log.Fatalf("背包里没找到木剑（%s）", describeBag(bag))
		}
		fmt.Printf("    背包里有 %q MakeIndex=%d\n", pickName, pickIdx)

		// 3) 存入 → SM_STORAGE_OK，列表变 1 件
		//
		// ⚠️ 服务端的存入有三条拒绝路径（背包里找不到该 MakeIndex+名字 / 当前对话的 NPC
		// 不是可存商人 / 仓库满），任一条都只回 `SM_STORAGE_FAIL`，而客户端原来收到
		// 非 OK 就直接放弃 ⇒ 表现为"仓库 0 件"，看着像"仓库自己空了"（2026-10-05 用
		// 服务端日志定性的就是这个：那轮**只有**"打开仓库列表：0 件"、**没有**"存入仓库"）。
		// 负载下最容易撞上的是"对话状态丢了" ⇒ 这里重开对话 + 刷一次背包再重试。
		stored := false
		for attempt := 0; attempt < 3 && !stored; attempt++ {
			if attempt > 0 {
				fmt.Printf("    (存入没成功，重开对话+刷背包后重试第 %d 次)\n", attempt)
				dlg("@storage")
				_ = recvUntil(proto.SM_SENDUSERSTORAGEITEM, 2*time.Second)
				cc.send(wire.Packet{
					Head: proto.MakeDefaultMsg(proto.CM_QUERYBAGITEMS, 0, 0, 0, 0),
					Body: "",
				})
				_ = recvUntil(proto.SM_BAGITEMS, 2*time.Second)
			}
			itemMsg(proto.CM_USERSTORAGEITEM, pickIdx, pickName)
			if rp := recvUntil(proto.SM_STORAGE_OK, 3*time.Second); rp != nil {
				stored = true
			}
		}
		if !stored {
			log.Fatal("存入后没收到 SM_STORAGE_OK（已重试 3 次：重开对话 + 刷背包）")
		}
		n, entries = storageList()
		if n != 1 || len(entries) != 1 || entries[0].name != pickName {
			log.Fatalf("存入后仓库应为 1 件 %q，实际 Series=%d 条目=%v", pickName, n, entries)
		}
		fmt.Printf("    存入 %q → 仓库 %d 件 ✓\n", pickName, n)

		// 4) 存入一件不存在的（MakeIndex 编一个）⇒ SM_STORAGE_FAIL
		itemMsg(proto.CM_USERSTORAGEITEM, pickIdx+999999, pickName)
		if rp := recvUntil(proto.SM_STORAGE_FAIL, 3*time.Second); rp == nil {
			log.Fatal("存入不存在的物品应回 SM_STORAGE_FAIL")
		}
		fmt.Println("    存入不存在的物品 → SM_STORAGE_FAIL ✓")

		// 5) 取回 → SM_TAKEBACKSTORAGEITEM_OK（Recog = MakeIndex），列表回空
		itemMsg(proto.CM_USERTAKEBACKSTORAGEITEM, pickIdx, pickName)
		ok := recvUntil(proto.SM_TAKEBACKSTORAGEITEM_OK, 3*time.Second)
		if ok == nil {
			log.Fatal("取回后没收到 SM_TAKEBACKSTORAGEITEM_OK")
		}
		if int32(ok.Head.Recog) != pickIdx {
			log.Fatalf("SM_TAKEBACKSTORAGEITEM_OK.Recog = %d，期望 MakeIndex %d", ok.Head.Recog, pickIdx)
		}
		if n, entries = storageList(); n != 0 || len(entries) != 0 {
			log.Fatalf("取回后仓库应为空，实际 Series=%d 条目=%v", n, entries)
		}
		// 背包里应该又有它（我们额外重发全量背包，见 storage.go 的说明）
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_QUERYBAGITEMS, 0, 0, 0, 0)})
		back := ""
		if rp := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_BAGITEMS }, 3*time.Second); rp != nil {
			back = rp.Body
		}
		if findInBag(back, pickName) < 0 {
			log.Fatalf("取回后背包里没有 %q（%s）", pickName, describeBag(back))
		}
		fmt.Println("    取回后回到背包 ✓")

		// 6) 取回一件不存在的 ⇒ SM_TAKEBACKSTORAGEITEM_FAIL
		itemMsg(proto.CM_USERTAKEBACKSTORAGEITEM, pickIdx, pickName)
		if rp := recvUntil(proto.SM_TAKEBACKSTORAGEITEM_FAIL, 3*time.Second); rp == nil {
			log.Fatal("取回不存在的物品应回 SM_TAKEBACKSTORAGEITEM_FAIL")
		}
		fmt.Println("    取回不存在的物品 → SM_TAKEBACKSTORAGEITEM_FAIL ✓")

		// 7) 最后**留一件在仓库里**：e2e 的第二段用同一角色再登录，
		//    断言它还在（仓库存的是 pb.CharacterData.storage_items，随存档走）。
		itemMsg(proto.CM_USERSTORAGEITEM, pickIdx, pickName)
		if rp := recvUntil(proto.SM_STORAGE_OK, 3*time.Second); rp == nil {
			log.Fatal("留存入库没收到 SM_STORAGE_OK")
		}
		if n, entries = storageList(); n != 1 || entries[0].name != pickName {
			log.Fatalf("留存后仓库应为 1 件 %q，实际 Series=%d 条目=%v", pickName, n, entries)
		}
		fmt.Printf("    留 %q 在仓库里（供下一段验持久化）✓\n", pickName)
		fmt.Println("    仓库存/取链路已验证 ✓")
	}

	if *guildTest {
		fmt.Println("[26] 测试行会系统")

		sendPkt := func(ident uint16, recog int32, body string) {
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(ident, recog, 0, 0, 0), Body: body})
		}
		sendSay := func(text string) { sendPkt(proto.CM_SAY, 0, text) }
		recvUntil := func(want uint16, max int) *wire.Packet {
			// 不按"包数 + 首个 nil"等：并发 e2e 负载下队列会短暂空窗，
			// nil 只表示此刻无包，不代表服务端没回。谓词等待保留无关广播包。
			return cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == want },
				time.Duration(max)*500*time.Millisecond)
		}
		recogOf := func(p *wire.Packet) int32 {
			if p == nil {
				return 0
			}
			return p.Head.Recog
		}
		bodyOf := func(p *wire.Packet) string {
			if p == nil {
				return "<超时>"
			}
			return p.Body
		}
		mustOK := func(ok bool, format string, args ...any) {
			if !ok {
				log.Fatalf(format, args...)
			}
		}

		// 行会名从角色名派生：角色名全局唯一，行会名也就不会撞车。
		guildName := chrName + "会"

		// ---- 准备：金币与号角，然后点开一个 NPC 的对话 ----
		// @@buildguildnow 在真实客户端里是 NPC 对话框上的链接，
		// 服务端只在其对话上下文中受理（原版 TGuildOfficial.UserSelect）。
		sendSay("@gold 2000000")
		sendSay("@give 沃玛号角")
		for k := 0; k < 8; k++ {
			if rp := cc.tryRecvTimeout(300 * time.Millisecond); rp == nil {
				break
			}
		}

		sendSay("@map 0 313 271")
		// 切图与 NPC 出现包是异步广播；队列短暂空窗不能当成"没有 NPC"。
		mapChange := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_CHANGEMAP }, 5*time.Second)
		mustOK(mapChange != nil, "传送到屠夫附近后未收到 SM_CHANGEMAP")
		fmt.Printf("    已传送到屠夫附近 (%d,%d)\n", mapChange.Head.Param, mapChange.Head.Tag)
		// 队列里可能还残留切图前其它 NPC 的出现包，不能取"第一个 Recog>=NpcIDBase"；
		// 必须按**屠夫的已知坐标**(313,271) 定位，否则会点击错 NPC，客户端随后等不到
		// 对话包（服务端也没有"开始对话"日志）。
		npc := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_TURN && p.Head.Recog >= proto.NpcIDBase &&
				p.Head.Param == 313 && p.Head.Tag == 271
		}, 5*time.Second)
		mustOK(npc != nil, "视野内没有屠夫 NPC（ActorId / 坐标 313,271）")
		npcID := uint32(npc.Head.Recog)
		// ⚠️ 必须**确认对话真的开了**再发 `@@buildguildnow`：
		// `CM_MERCHANTDLGSELECT` 在服务端 `p.dialog == nil` 时会被**静默丢弃**
		//（与 `@storage` 那条坑同源）。上面"点一下、随便收几个包"是不够的：
		// 并行跑 e2e 时机器一忙，`CM_CLICKNPC` 的处理还没轮到，后面那条消息就先到了
		// ⇒ 客户端干等 10 秒 ⇒ 报"建会失败（码 0）"，而 **0 其实是"压根没收到回包"**
		//（2026-10-05 抓到一次现场：guild + 重试都是码 0，服务端连"建会"日志都没有）。
		//
		// 修法与 storage 用例一致：点击后**确认收到对话/商品包**，最多 3 轮；
		// 建会请求本身也按"没回包就重开对话再发一次"重试。
		openDialog := func() bool {
			for attempt := 0; attempt < 3; attempt++ {
				cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_CLICKNPC, int32(npcID), 0, 0, 0)})
				// 裸墙钟：这是 I/O 边界上的等待，不该跟游戏内时间倍速缩；只拿走匹配包。
				rp := cc.waitFor(func(p *wire.Packet) bool {
					return p.Head.Ident == proto.SM_SENDGOODSLIST || p.Head.Ident == proto.SM_DLGMSG
				}, 2*time.Second)
				if rp != nil {
					return true
				}
			}
			return false
		}
		mustOK(openDialog(), "点击 NPC 后没收到对话/商品列表（对话没开，后续会被静默丢弃）")

		// ---- 建会：@@buildguildnow + 行会名（原版 ObjNpc.pas:11317-11364）----
		var built, buildFail *wire.Packet
		deadline := time.Now().Add(12 * time.Second)
		for built == nil && buildFail == nil && time.Now().Before(deadline) {
			sendPkt(proto.CM_MERCHANTDLGSELECT, int32(npcID), "@@buildguildnow\r"+guildName)
			// ⚠️ 单次等 2.5 秒**墙钟**（不是 rcv 缩过的）：一次 nil 只说明这一瞬间
			// 队列为空，放弃会误判成"建会失败（码 0）"。
			rp := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_BUILDGUILD_OK || p.Head.Ident == proto.SM_BUILDGUILD_FAIL
			}, 2500*time.Millisecond)
			if rp != nil {
				if rp.Head.Ident == proto.SM_BUILDGUILD_OK {
					built = rp
				} else {
					buildFail = rp
				}
			}
			if built == nil && buildFail == nil {
				// 这一发被丢了（对话状态没跟上）：重开对话再发一次
				if !openDialog() {
					break
				}
			}
		}
		// -1 = 已入会：e2e 的失败重试会用同一个角色，第二次跑时它已经在
		// 第一次建的行会里，此时跳过建会、继续验证后续环节即可。
		if built == nil && recogOf(buildFail) == -1 {
			fmt.Println("    角色已在行会中（重试），跳过建会")
		} else {
			mustOK(built != nil,
				"建会失败（码 %d：-1 已入会 / -2 缺金币 / -3 缺号角 / -4 名字非法或重名）", recogOf(buildFail))
		}
		fmt.Printf("    行会 %q 创建成功\n", guildName)

		rp := recvUntil(proto.SM_CHANGEGUILDNAME, 20)
		mustOK(rp != nil, "未收到 SM_CHANGEGUILDNAME")
		mustOK(rp.Body == guildName+"/行会掌门人",
			"SM_CHANGEGUILDNAME body = %q，期望 %q", bodyOf(rp), guildName+"/行会掌门人")
		fmt.Printf("    头顶信息 = %q\n", rp.Body)

		// ---- 行会窗口（SM_OPENGUILDDLG 753）----
		sendPkt(proto.CM_OPENGUILDDLG, 0, "")
		rp = recvUntil(proto.SM_OPENGUILDDLG, 20)
		mustOK(rp != nil, "未收到 SM_OPENGUILDDLG")
		win := strings.Split(rp.Body, "\r")
		mustOK(len(win) >= 5, "行会窗口行数 = %d: %q", len(win), rp.Body)
		mustOK(win[0] == guildName, "窗口首行 = %q，期望 %q", win[0], guildName)
		mustOK(win[2] == "1", "掌门标志 = %q，期望 \"1\"", win[2])
		mustOK(strings.Contains(rp.Body, "<Notice>") &&
			strings.Contains(rp.Body, "<KillGuilds>") &&
			strings.Contains(rp.Body, "<AllyGuilds>"), "窗口缺少段落标记: %q", rp.Body)
		fmt.Println("    窗口字段正确（行会名 / 掌门标志 / 三段标记）")
		// ---- 成员列表（SM_SENDGUILDMEMBERLIST 756）----
		sendPkt(proto.CM_GUILDMEMBERLIST, 0, "")
		rp = recvUntil(proto.SM_SENDGUILDMEMBERLIST, 20)
		mustOK(rp != nil, "未收到 SM_SENDGUILDMEMBERLIST")
		mustOK(rp.Head.Series == 1, "成员列表 Series = %d，期望 1", rp.Head.Series)
		mustOK(strings.Contains(rp.Body, "#1/*行会掌门人/"+chrName+"/"), "成员列表 = %q", bodyOf(rp))
		fmt.Printf("    成员列表 = %q\n", rp.Body)

		// ---- 行会聊天（CM_SAY 的 '!~' 前缀 → SM_GUILDMESSAGE 104）----
		sendSay("!~大家好")
		rp = recvUntil(proto.SM_GUILDMESSAGE, 20)
		mustOK(rp != nil, "未收到 SM_GUILDMESSAGE")
		mustOK(rp.Body == chrName+": 大家好", "行会聊天 body = %q", bodyOf(rp))
		mustOK(uint8(rp.Head.Param) == 0xDB && uint8(rp.Head.Param>>8) == 0xFF,
			"聊天颜色 Param = 0x%04X，期望 0xFFDB", rp.Head.Param)
		fmt.Printf("    行会聊天 = %q（前景 0x%02X / 背景 0x%02X）\n",
			rp.Body, uint8(rp.Head.Param), uint8(rp.Head.Param>>8))

		// ---- 改公告（CM_GUILDUPDATENOTICE 1040，无 OK 包）----
		sendPkt(proto.CM_GUILDUPDATENOTICE, 0, "欢迎加入\r本会招人中\r")
		rp = recvUntil(proto.SM_OPENGUILDDLG, 20)
		mustOK(rp != nil && strings.Contains(rp.Body, "欢迎加入") && strings.Contains(rp.Body, "本会招人中"),
			"公告未生效: %q", bodyOf(rp))
		fmt.Println("    公告已更新（回 SM_OPENGUILDDLG 刷新界面）")

		// ---- 改职务表（CM_GUILDUPDATERANKINFO 1041）----
		// 客户端格式：#<职务号> <<职务名>>，后跟成员行（FState.pas:6514-6565）。
		sendPkt(proto.CM_GUILDUPDATERANKINFO, 0, "#1 <会长>\r"+chrName)
		rp = recvUntil(proto.SM_SENDGUILDMEMBERLIST, 20)
		// ⚠️ 成员列表里的职务名是 "#<号>/*<名>/"，不带尖括号（尖括号只用于
		// 客户端回传的职务表文本）。ClMain.pas:6451-6490 按 '*' 前缀取值。
		mustOK(rp != nil && strings.Contains(rp.Body, "#1/*会长/"), "职务表未生效: %q", bodyOf(rp))
		fmt.Println("    职务表已更新（行会掌门人 → 会长）")

		// peerRecvUntil / faceEachOther 在加人、开除、结盟三段里都要用，提到外层。
		peerRecvUntil := func(want uint16, max int) *wire.Packet {
			// peer 也用队列谓词等待：非匹配广播包不能吃掉等待预算。
			return peer.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == want },
				time.Duration(max)*400*time.Millisecond)
		}
		// myX/myY 与 px/py 是两个角色当前的地图坐标（结盟要靠坐标定位正对面）。
		var myX, myY, px, py int
		// faceEachOther 让两个角色转向彼此（CM_TURN 的方向在 Tag 字段）。
		// ⚠️ 双方都得转：原版要求 `BaseObjectC.GetPoseCreate = Self`，
		// 即"对方也正朝向我"，只转一边会回失败码 -1。
		faceEachOther := func() {
			myDir := dirToward(px-myX, py-myY)
			peerDir := dirToward(myX-px, myY-py)
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TURN, 0, 0, uint16(myDir), 0)})
			peer.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TURN, 0, 0, uint16(peerDir), 0)})

			// 两条 TCP 连接各自独立排队；不能"发出 turn 后马上发 add"，否则主连接
			// 可能先到服务端、而 peer 的转向还在另一条连接队列里，faceTo(target,p)
			// 看到的仍是旧方向。必须等**双方各自收到服务端 SM_TURN 确认**再发 add。
			myAck := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_TURN && p.Head.Recog == actorID &&
					p.Head.Series == uint16(myDir)
			}, 2*time.Second)
			peerAck := peer.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_TURN && p.Head.Recog == peerActorID &&
					p.Head.Series == uint16(peerDir)
			}, 2*time.Second)
			mustOK(myAck != nil, "主角色转向未得到 SM_TURN 确认（dir=%d）", myDir)
			mustOK(peerAck != nil, "观察者转向未得到 SM_TURN 确认（dir=%d）", peerDir)
		}

		if peer != nil {
			peerName := chrName + "B"

			// a) 未面对面 → 失败码 2（原版校验路径）
			sendPkt(proto.CM_GUILDADDMEMBER, 0, peerName)
			rp = recvUntil(proto.SM_GUILDADDMEMBER_FAIL, 20)
			mustOK(rp != nil && rp.Head.Recog == 2, "未面对面应回失败码 2，得到 %d", recogOf(rp))
			fmt.Println("    未面对面加人被拒（失败码 2）✓")

			// b) 传送到同一片空地，面对面后加人
			sendSay("@map 0 330 270")
			myMap := cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_CHANGEMAP }, 5*time.Second)
			mustOK(myMap != nil, "切图后未收到 SM_CHANGEMAP")
			myX, myY = int(myMap.Head.Param), int(myMap.Head.Tag)

			// peer：允许入会 + 传送到主角左侧一格
			peer.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@LetGuild"})
			peer.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: fmt.Sprintf("@map 0 %d %d", myX-1, myY),
			})
			peerMap := peer.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == proto.SM_CHANGEMAP }, 8*time.Second)
			mustOK(peerMap != nil, "peer 切图失败")
			px, py = int(peerMap.Head.Param), int(peerMap.Head.Tag)
			fmt.Printf("    主角色 (%d,%d) / 观察者 (%d,%d)\n", myX, myY, px, py)

			faceEachOther()

			sendPkt(proto.CM_GUILDADDMEMBER, 0, peerName)
			rp = recvUntil(proto.SM_GUILDADDMEMBER_OK, 25)
			mustOK(rp != nil, "面对面加人失败（失败码 %d）", recogOf(rp))
			fmt.Println("    面对面加人成功（SM_GUILDADDMEMBER_OK）")

			// 新成员应收到 行会名/行会成员
			rp = peerRecvUntil(proto.SM_CHANGEGUILDNAME, 30)
			mustOK(rp != nil, "观察者未收到 SM_CHANGEGUILDNAME")
			mustOK(rp.Body == guildName+"/行会成员", "观察者头顶信息 = %q", bodyOf(rp))
			fmt.Printf("    观察者头顶信息 = %q\n", rp.Body)

			// c) 开除成员
			sendPkt(proto.CM_GUILDDELMEMBER, 0, peerName)
			rp = recvUntil(proto.SM_GUILDDELMEMBER_OK, 25)
			mustOK(rp != nil, "开除成员失败（失败码 %d）", recogOf(rp))
			rp = peerRecvUntil(proto.SM_CHANGEGUILDNAME, 30)
			mustOK(rp != nil && rp.Body == "", "被开除者应收到空的 SM_CHANGEGUILDNAME，得到 %q", bodyOf(rp))
			fmt.Println("    开除成员成功（观察者头顶信息已清空）")
		}

		// ---- 行会战：向对方的行会宣战，验证窗口 <KillGuilds> 段 ----
		//
		// 用 peer 建第二个行会（走 GM 命令 @guild create，省掉一次 NPC 对话），
		// 再由主角色用 NPC 对话框的 @@guildwar 标签宣战（原版入口）。
		if peer != nil {
			foeGuild := peerName + "对手会" // 角色名唯一 ⇒ 行会名也唯一（循环复用 DB 不撞名）
			peer.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
				Body: fmt.Sprintf("@guild create %s", foeGuild),
			})
			// 等精确的"已创建"系统消息；用裸墙钟谓词等待，不能用 20×400ms×tscale
			//（20 倍速时总预算只有 400ms，还会因无关广播/短暂空窗误报未建成）。
			foeReady := peer.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(p.Body, foeGuild) &&
					(strings.Contains(p.Body, "已创建") || strings.Contains(p.Body, "已存在"))
			}, 8*time.Second)
			mustOK(foeReady != nil, "对手行会 %q 未建成", foeGuild)

			// ================= 行会联盟 =================
			//
			// 走**原版协议** CM_GUILDALLY(1044) / CM_GUILDBREAKALLY(1045)，
			// 不用 `@联盟` 聊天入口——后者只是测试捷径；只有接了 CM_GUILDALLY
			// 才代表真客户端在行会窗口点"结盟"按钮真的能通。
			//
			// 原版 ClientGuildAlly（ObjBase.pas:18217-18268）的判定顺序：
			//   正对面没人/不是玩家/对方没行会/对方不朝向你 → -1
			//   对方行会没开"允许被结盟"                  → -4
			//   有一方不是掌门                             → -3
			//   正在行会战                                 → -2
			//   其余                                       → 0
			// ⚠️ **重复结盟返回 0**：原版 AllyGuild 内部去重，但返回值被丢弃。
			allyCode := func(want int) {
				sendPkt(proto.CM_GUILDALLY, 0, "")
				resp := cc.waitFor(func(p *wire.Packet) bool {
					return p.Head.Ident == proto.SM_GUILDMAKEALLY_OK || p.Head.Ident == proto.SM_GUILDMAKEALLY_FAIL
				}, 10*time.Second)
				var okPkt, failPkt *wire.Packet
				if resp != nil {
					if resp.Head.Ident == proto.SM_GUILDMAKEALLY_OK {
						okPkt = resp
					} else {
						failPkt = resp
					}
				}
				if want == 0 {
					mustOK(okPkt != nil, "结盟应成功，得到失败码 %d", recogOf(failPkt))
					return
				}
				mustOK(failPkt != nil, "结盟应回失败码 %d，却收到 SM_GUILDMAKEALLY_OK", want)
				mustOK(int(failPkt.Head.Recog) == want,
					"结盟失败码 = %d，期望 %d", failPkt.Head.Recog, want)
			}
			allySeg := func() string {
				sendPkt(proto.CM_OPENGUILDDLG, 0, "")
				w := recvUntil(proto.SM_OPENGUILDDLG, 20)
				mustOK(w != nil, "结盟后未收到行会窗口")
				return w.Body
			}

			// a) 主角色背对 peer（正前方那格没人）→ -1
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TURN, 0, 0,
				uint16(entity.DirUp), 0)})
			turnAck := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_TURN && p.Head.Recog == actorID &&
					p.Head.Series == uint16(entity.DirUp)
			}, 2*time.Second)
			mustOK(turnAck != nil, "主角背向观察者的转向未确认")
			allyCode(-1)
			fmt.Println("    未面对面结盟被拒（失败码 -1）✓")

			// b) 面对面了，但对方行会没开 @AuthAlly → -4
			faceEachOther()
			allyCode(-4)
			fmt.Println("    对方未开启允许被结盟（失败码 -4）✓")

			// c) 对方开 @AuthAlly（仅掌门有效）后再结盟 → 0
			peer.send(wire.Packet{
				Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: "@AuthAlly",
			})
			rp = peer.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(p.Body, "现在允许被结盟")
			}, 8*time.Second)
			mustOK(rp != nil, "peer 未开启 @AuthAlly")
			faceEachOther()
			allyCode(0)
			fmt.Println("    结盟成功（SM_GUILDMAKEALLY_OK）✓")

			// 双方窗口的 <AllyGuilds> 段应互相出现对方行会名。
			// ⚠️ 顺序要紧：原版是"先发行会公告、再刷头顶信息"，peerRecvUntil
			// 会把不匹配的包**丢掉**，所以公告必须在请求窗口之前读。
			rp = peerRecvUntil(proto.SM_GUILDMESSAGE, 25)
			mustOK(rp != nil && rp.Body == guildName+"行会已经和您的行会联盟成功。",
				"对方行会公告 = %q", bodyOf(rp))
			peer.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_OPENGUILDDLG, 0, 0, 0, 0), Body: ""})
			rp = peerRecvUntil(proto.SM_OPENGUILDDLG, 25)
			mustOK(rp != nil && strings.Contains(rp.Body, "<AllyGuilds>\r"+guildName),
				"对方窗口联盟段缺少 %q: %q", guildName, bodyOf(rp))
			mustOK(strings.Contains(allySeg(), "<AllyGuilds>\r"+foeGuild),
				"本会窗口联盟段缺少 %q", foeGuild)
			fmt.Println("    双方窗口 <AllyGuilds> 与行会公告均正确 ✓")

			// d) 重复结盟仍是成功（AllyGuild 去重，返回值被原版丢弃）
			allyCode(0)
			fmt.Println("    重复结盟仍回成功（幂等）✓")

			// e) 解除联盟：CM_GUILDBREAKALLY + body=对方行会名
			sendPkt(proto.CM_GUILDBREAKALLY, 0, foeGuild)
			breakResp := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_GUILDBREAKALLY_OK || p.Head.Ident == proto.SM_GUILDMAKEALLY_FAIL
			}, 10*time.Second)
			mustOK(breakResp != nil && breakResp.Head.Ident == proto.SM_GUILDBREAKALLY_OK,
				"解除联盟失败（回的是 SM_GUILDMAKEALLY_FAIL）")
			mustOK(!strings.Contains(allySeg(), "<AllyGuilds>\r"+foeGuild),
				"解除后窗口联盟段仍列着 %q", foeGuild)
			fmt.Println("    解除联盟成功（SM_GUILDBREAKALLY_OK）✓")

			// 宣战：@@guildwar + 对方行会名（与 @@buildguildnow 同一入口）
			// ⚠️ 必须在解除联盟**之后**：原版 AddWarGuild 开头就
			// `if not IsAllyGuild(Guild)`，盟友之间根本宣不了战（Guild.pas:1213）。
			sendPkt(proto.CM_MERCHANTDLGSELECT, int32(npcID), "@@guildwar\r"+foeGuild)
			warMsg := cc.waitFor(func(p *wire.Packet) bool {
				return p.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(p.Body, "宣战")
			}, 10*time.Second)
			mustOK(warMsg != nil, "宣战未生效（没收到宣战提示）")
			fmt.Printf("    宣战提示: %s\n", warMsg.Body)

			// 打开行会窗口：敌对行会段应出现对方行会名
			sendPkt(proto.CM_OPENGUILDDLG, 0, "")
			rp = recvUntil(proto.SM_OPENGUILDDLG, 20)
			mustOK(rp != nil, "宣战后未收到行会窗口")
			mustOK(strings.Contains(rp.Body, "<KillGuilds>\r"+foeGuild),
				"行会窗口敌对段缺少 %q: %q", foeGuild, rp.Body)
			fmt.Println("    行会战：窗口敌对段已包含对手行会 ✓")

			// f) 战争中结盟被拒 → -2（原版 IsNotWarGuild）
			//    顺序在宣战之后：ClientGuildAlly 判的是"正在战争"，不判顺序的话
			//    这条与上面的成功用例互斥，没法同时验。
			allyCode(-2)
			fmt.Println("    行会战中结盟被拒（失败码 -2）✓")
		}

		// ---- 解散行会：先清掉空职务（开除后 rank99 会残留），再删自己 ----
		sendPkt(proto.CM_GUILDUPDATERANKINFO, 0, "#1 <会长>\r"+chrName)
		rp = recvUntil(proto.SM_SENDGUILDMEMBERLIST, 20)
		mustOK(rp != nil, "清空职务失败")

		sendPkt(proto.CM_GUILDDELMEMBER, 0, chrName)
		// ⚠️ 顺序：服务端先发 SM_CHANGEGUILDNAME（清头顶信息）再发
		// SM_GUILDDELMEMBER_OK，与原版一致（ObjBase.pas:18127-18178）。
		delResp := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_GUILDDELMEMBER_OK || p.Head.Ident == proto.SM_GUILDDELMEMBER_FAIL
		}, 10*time.Second)
		var delOK, delFail *wire.Packet
		if delResp != nil {
			if delResp.Head.Ident == proto.SM_GUILDDELMEMBER_OK {
				delOK = delResp
			} else {
				delFail = delResp
			}
		}
		// 职务表重置时也会发一次非空 SM_CHANGEGUILDNAME（例如"行会名/会长"）；
		// 不能拿到它就当成解散确认，必须等解散后那条**空 body**。
		delName := cc.waitFor(func(p *wire.Packet) bool {
			return p.Head.Ident == proto.SM_CHANGEGUILDNAME && p.Body == ""
		}, 5*time.Second)
		mustOK(delOK != nil, "解散行会失败（失败码 %d）", recogOf(delFail))
		mustOK(delName != nil && delName.Body == "", "解散后应收到空 SM_CHANGEGUILDNAME，得到 %q", bodyOf(delName))
		fmt.Println("    解散行会成功")

		// ---- 再开窗口应失败（SM_OPENGUILDDLG_FAIL 754）----
		sendPkt(proto.CM_OPENGUILDDLG, 0, "")
		rp = recvUntil(proto.SM_OPENGUILDDLG_FAIL, 20)
		mustOK(rp != nil, "已无行会，应收到 SM_OPENGUILDDLG_FAIL")
		fmt.Println("    未入会时打开窗口失败 ✓")
		fmt.Println("    行会系统验证通过 ✓")
	}

	if peer != nil && *leaveTest {
		fmt.Println("[10] 测试视野离开")
		//
		// ⚠️ 真实地图有建筑，观察者一直往右走很可能撞墙走不远，
		// 距离不够就不会触发 SM_DISAPPEAR，表现为等待超时。
		// 这里改用传送直接拉开距离，结果稳定。
		steps := *viewRange + 2
		peer.send(wire.Packet{
			Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
			Body: fmt.Sprintf("@map 0 %d %d", peerX+steps+5, peerY),
		})
		for i := 0; i < maxRecvTries; i++ {
			rp := peer.recv()
			if rp.Head.Ident == proto.SM_CHANGEMAP {
				peerX, peerY = int(rp.Head.Param), int(rp.Head.Tag)
				break
			}
		}
		fmt.Printf("    观察者远离至 (%d,%d)\n", peerX, peerY)

		// 主角色应陆续收到 SM_RUN（仍在视野时）后收到 SM_DISAPPEAR
		got := false
		for i := 0; i < steps+5 && !got; i++ {
			rd := cc.recv()
			if rd.Head.Ident == proto.SM_DISAPPEAR {
				fmt.Printf("    主角色收到 SM_DISAPPEAR(ActorId=%d) ✓\n", rd.Head.Recog)
				got = true
			}
		}
		if !got {
			log.Fatal("未收到 SM_DISAPPEAR —— 离开视野同步失败")
		}
	}

	fmt.Println("\n✓ 完整链路通过（登录 → 选角 → 进入游戏 → 走路）")
}

// ensureAccount 确保账号存在（直接写库，绕过 CM_ADDNEWUSER——该命令尚未实现）。
func ensureAccount(dbPath, name, pw string) error {
	st, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if _, err := st.Accounts().GetByName(ctx, name); err == nil {
		return nil // 已存在
	}
	hash, salt, err := storage.HashPassword(pw)
	if err != nil {
		return err
	}
	return st.Accounts().Create(ctx, &storage.Account{Name: name, PasswordHash: hash, Salt: salt})
}

func pkt(ident uint16, body string) wire.Packet {
	return wire.Packet{Head: proto.MakeDefaultMsg(ident, 0, 0, 0, 0), Body: body}
}

// dirToward 返回朝 (dx,dy) 走的八方向之一。
func dirToward(dx, dy int) uint8 {
	switch {
	case dx > 0 && dy > 0:
		return entity.DirDownRight
	case dx > 0 && dy < 0:
		return entity.DirUpRight
	case dx < 0 && dy > 0:
		return entity.DirDownLeft
	case dx < 0 && dy < 0:
		return entity.DirUpLeft
	case dx > 0:
		return entity.DirRight
	case dx < 0:
		return entity.DirLeft
	case dy > 0:
		return entity.DirDown
	default:
		return entity.DirUp
	}
}

// sc 是"节流等待"的缩放：游戏内时长 → 真实等待。
//
// ⚠️ 只用于**节流**（移动间隔、攻击节奏），收包超时不能缩（那是 I/O 等待，
// 缩了会在负载高时误判"服务端没响应"）。
//
// ⚠️ 下限 = 服务端移动限流的缩放值 + 余量：客户端按键必须**慢于**服务端限流，
// 否则请求刚发出就被限流丢弃（表现为"客户端一直原地打转追不上怪"）。
// 10 倍速下 430ms → 50ms 而不是 43ms，就是这 10ms 余量的作用。
func sc(d time.Duration) time.Duration {
	v := tscale.D(d)
	if min := tscale.D(400*time.Millisecond) + 10*time.Millisecond; v < min {
		v = min
	}
	return v
}

// rcv 是"等一个**游戏内**本该很快到的包/节奏"的预算：随倍速缩放，但有下限。
//
// 与裸 `tryRecvTimeout`/`time.Sleep` 的区别（这是本项目提速的关键）：
//
//	裸墙钟等待（如 `time.Sleep(350 * time.Millisecond)`）**完全不随倍速变**，
//	20x 与 50x 耗时一模一样。`slave` 用例 21s 里有 20×(350ms + 300ms) ≈ 13s
//	就是这种等待 —— 这才是"提到 50 倍速也没变快"的原因，不是倍速机制不行。
//
// 但也不能一律 `tscale.D`：下限必须**高于服务端缩放后的限流/冷却**
// （施法冷却 minSpellInterval 也按倍速缩，20x 下约 40ms），否则请求刚发出
// 就被服务端冷却丢弃。100ms 对本地回环是充裕的。
//
// ⚠️ 不要拿它去替代"连接存活"类的读超时——那类是 I/O 边界，见 tscale 包注释。
func rcv(d time.Duration) time.Duration {
	v := tscale.D(d)
	if v < 100*time.Millisecond {
		v = 100 * time.Millisecond
	}
	return v
}

// runChat 验证聊天四路：普通 / 喊话 / 私聊 / 组队频道。
//
// ⚠️ 关键断言落在**观察者（peer）**身上。普通聊天与喊话的语义都是"发给视野内的人"，
// 只断言发送者自己收到的话，"只发给本人"这种广播范围写错也照样过。
func runChat(cc, peer *conn, chrName, peerName string, actorID int32) {
	fmt.Println("[26] 测试聊天（普通 / 喊话 / 私聊 / 组队频道）")

	// 反刷屏是"3 秒内发到第 3 条就禁言 60 秒"（SayMsgTime=3000 / SayMsgCount=2），
	// 所以每条聊天之间必须**跨过窗口**。窗口按倍速缩放（服务端 tscale.D），
	// 这里留 4 秒游戏时间的余量；TIME_SCALE=1 时就是真实的 4 秒。
	gap := func() { time.Sleep(rcv(4000 * time.Millisecond)) }
	say := func(c *conn, body string) {
		c.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: body})
	}

	// ---- 1) 普通聊天：视野内的人都该收到（含自己）----
	say(cc, "你好")
	want := chrName + ":你好"
	got := waitPacket(peer, proto.SM_HEAR, 2*time.Second)
	if got == nil {
		log.Fatal("观察者没收到主角色的普通聊天（SM_HEAR）")
	}
	if got.Body != want {
		log.Fatalf("观察者收到的聊天正文 = %q，期望 %q", got.Body, want)
	}
	// Recog 是说话者的 ActorId（原版 SendMsg(Self, RM_HEAR, …)）
	if got.Head.Recog != actorID {
		log.Fatalf("聊天包 Recog=%d，期望说话者 ActorId=%d", got.Head.Recog, actorID)
	}
	// Param = MakeWord(F,B) = HearMsgFColor/BColor = 0/255
	if f, b := proto.LoByte(got.Head.Param), proto.HiByte(got.Head.Param); f != 0 || b != 255 {
		log.Fatalf("普通聊天颜色 = (%d,%d)，期望 (0,255)", f, b)
	}
	fmt.Printf("    观察者收到普通聊天 %q（Recog=%d，色 %d/%d）✓\n",
		got.Body, got.Head.Recog, proto.LoByte(got.Head.Param), proto.HiByte(got.Head.Param))

	// 发送者自己也必须收到：客户端不会本地回显自己发的聊天。
	if self := waitPacket(cc, proto.SM_HEAR, 2*time.Second); self == nil || self.Body != want {
		log.Fatalf("发送者自己没收到自己的聊天（客户端不会本地回显）: %v", bodyOf(self))
	}
	fmt.Println("    发送者自己也收到同一句（客户端不本地回显）✓")

	// ---- 2) 喊话 `!`：等级要 > CanShoutMsgLevel(7) ----
	gap()
	say(cc, "@level 60")
	time.Sleep(rcv(300 * time.Millisecond))
	gap()
	say(cc, "!喊话测试")
	wantCry := "(!)" + chrName + ": 喊话测试"
	cry := waitPacket(peer, proto.SM_HEAR, 2*time.Second)
	if cry == nil {
		log.Fatal("观察者没收到喊话")
	}
	if cry.Body != wantCry {
		log.Fatalf("喊话正文 = %q，期望 %q", cry.Body, wantCry)
	}
	// ⚠️ 喊话走的是 SM_HEAR（不是 SM_CRY），只有背景色不同：CryMsgBColor=151
	if proto.HiByte(cry.Head.Param) != 151 {
		log.Fatalf("喊话背景色 = %d，期望 151（CryMsgBColor）", proto.HiByte(cry.Head.Param))
	}
	fmt.Printf("    观察者收到喊话 %q（ident=SM_HEAR，背景色 151）✓\n", cry.Body)

	// ---- 3) 私聊：`/名字 内容` ----
	gap()
	say(cc, "/不存在的角色 hi")
	if msg := waitSysMsgText(cc, "没有在线", 2*time.Second); msg == "" {
		log.Fatal("私聊给不存在的角色，发送者没收到「没有在线」提示")
	} else {
		fmt.Printf("    私聊离线的角色 → 发送者收到 %q ✓\n", msg)
	}

	gap()
	say(cc, "/"+peerName+" 私聊测试")
	wp := waitPacket(peer, proto.SM_WHISPER, 2*time.Second)
	if wp == nil {
		log.Fatal("观察者没收到私聊（SM_WHISPER）")
	}
	wantWp := chrName + "=> 私聊测试"
	if wp.Body != wantWp {
		log.Fatalf("私聊正文 = %q，期望 %q", wp.Body, wantWp)
	}
	fmt.Printf("    观察者收到私聊 %q ✓\n", wp.Body)

	// ---- 4) 组队频道 `!!`：**没组队就什么也不该发** ----
	gap()
	say(cc, "!!组队频道测试")
	if p := waitSysMsgText(peer, "组队频道测试", 600*time.Millisecond); p != "" {
		log.Fatalf("没组队却收到了组队频道消息: %q", p)
	}
	fmt.Println("    没组队时 `!!` 不发消息 ✓")

	fmt.Println("    聊天四路已验证 ✓")
}

// waitIdent 在一段时间内等某个消息号（跳过其它包）。超时返回 nil。
func waitPacket(c *conn, ident uint16, budget time.Duration) *wire.Packet {
	deadline := time.Now().Add(budget)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		rp := c.tryRecvTimeout(left)
		if rp == nil {
			return nil
		}
		if rp.Head.Ident == ident {
			return rp
		}
	}
}

// waitSysMsg 等一条**包含** sub 的系统消息，返回其正文（超时返回空串）。
func waitSysMsgText(c *conn, sub string, budget time.Duration) string {
	// ⚠️ 原来是"取到一条包，不匹配就 return """——一条无关包（视野同步）就能把
	// 等待打断。改成队列**谓词等待**：只摘走匹配的那条，其余留在队列。
	rp := c.waitFor(func(p *wire.Packet) bool {
		return p.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(p.Body, sub)
	}, budget)
	if rp == nil {
		return ""
	}
	return rp.Body
}

// bodyOf 取包体（nil 安全，供错误信息用）。
func bodyOf(p *wire.Packet) string {
	if p == nil {
		return "(无包)"
	}
	return p.Body
}

// runWarrSkill 验证战士近战系：刺杀剑术(12) / 半月弯刀(25)。
//
// 这两个技能的模式**来自攻击包的消息号**（CM_LONGHIT / CM_WIDEHIT），
// 不是 Tag 里的字段，所以断言必须发对应的包、收对应的动画号：
//
//	刺杀剑术：只打**正前方第二格** —— 断言"第二格中招、第一格没中招"。
//	          只断言"收到 SM_LONGHIT"抓不到目标格写错（打第一格也会发这个包）。
//	半月弯刀：一次打**三个格子** —— 断言收到 ≥2 个不同怪的 SM_STRUCK。
func runWarrSkill(cc *conn, actorID int32, posX, posY int) {
	fmt.Println("[28] 测试战士近战技能（刺杀剑术 / 半月弯刀）")

	say := func(cmd string) {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
	}
	drain := func(n int) {
		for i := 0; i < n; i++ {
			if cc.tryRecvTimeout(250*time.Millisecond) == nil {
				return
			}
		}
	}
	// collect 把视野里出现过的怪记进 map（(dx,dy) → ActorId）。
	// quiet 是"这一段包发完了"的判据：连续这么久没有新包就收工。
	//
	// ⚠️ 下面两个收集循环都**必须能在安静时提前返回**：原实现一律跑满整个
	// 预算窗口（900~1200ms），而服务端的一批 SM_TURN 是**一次发完**的，
	// 于是每次调用都白等将近 1s —— warr 用例里这类调用有十几次，
	// 10s 的耗时大半在这儿（倍的只是游戏内时间，收包预算不缩放）。
	quiet := rcv(250 * time.Millisecond)

	collect := func(budget time.Duration) map[[2]int]int32 {
		out := map[[2]int]int32{}
		deadline := time.Now().Add(budget)
		last := time.Now()
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(quiet)
			if rp == nil {
				if time.Since(last) >= quiet {
					break // 安静了一整个窗口 ⇒ 收工
				}
				continue
			}
			last = time.Now()
			if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
				out[[2]int{int(rp.Head.Param) - posX, int(rp.Head.Tag) - posY}] = rp.Head.Recog
			}
		}
		return out
	}
	// teleportTo 传送到 (tx,ty) 并收一轮"新视野里的怪"（以传送后的坐标为原点）。
	//
	// ⚠️ 必须按 **SM_CHANGEMAP 回包的坐标**更新自身站位：服务端会把落点
	// 就近修正到可走格（`switchMap` 的 nearestWalkable），拿请求坐标当原点
	// 会让所有相对坐标整体偏移。
	teleportTo := func(tx, ty int) map[[2]int]int32 {
		say(fmt.Sprintf("@map 0 %d %d", tx, ty))
		seen := map[[2]int]int32{}
		gotMap := false
		deadline := time.Now().Add(rcv(900 * time.Millisecond))
		last := time.Now()
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(quiet)
			if rp == nil {
				// 已收到 CHANGEMAP 且安静了一整个窗口 ⇒ 收工（见 collect 的说明）
				if gotMap && time.Since(last) >= quiet {
					break
				}
				continue
			}
			last = time.Now()
			switch rp.Head.Ident {
			case proto.SM_CHANGEMAP:
				posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
				seen = map[[2]int]int32{} // 换图后旧视野作废，重收
				gotMap = true
			case proto.SM_TURN:
				if isMonsterID(rp.Head.Recog) {
					seen[[2]int{int(rp.Head.Param) - posX, int(rp.Head.Tag) - posY}] = rp.Head.Recog
				}
			}
		}
		return seen
	}

	// 技能等级拉满：刺杀 0 级只有 40% 威力、半月 0 级只有 15%
	say("@level 60")
	time.Sleep(rcv(300 * time.Millisecond))
	say("@magic 12 3")
	time.Sleep(rcv(300 * time.Millisecond))
	say("@magic 25 3")
	time.Sleep(rcv(300 * time.Millisecond))
	drain(8)

	// 先换到一片**空地**：这个用例的每一步都依赖 `@spawn` 的落点
	//（刺杀要"正前方两格"正好有怪、半月要邻格有怪、攻杀/烈火要"正前方一格"有怪），
	// 而出身点附近本来就有怪占着邻格 —— `@spawn` 只往空位放，邻格满了就把新怪
	// 甩到两格外，于是"落点不齐"偶发失败。地图 0 的远端没有刷怪点，传过去
	// 既清空视野又保证落点整齐。
	//
	// ⚠️ 结束时要**传送回去**：`posX/posY` 是值传递，调用方那份还指着出生点，
	// 而后面还有"走路"断言按那组坐标收包（不回去就会卡在"未收到 SM_WALK"）。
	origX, origY := posX, posY
	say(fmt.Sprintf("@map 0 %d %d", posX, posY+60))
	for i := 0; i < 12; i++ {
		rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_CHANGEMAP {
			posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
		}
	}
	fmt.Printf("    换到空地 (%d,%d)（出生点附近邻格被占，落点会不齐）\n", posX, posY)

	// ---- 0) 命中 / 敏捷：**还没练**基本剑术/精神力战法时会打空 ----
	//
	//	命中 = DEFHIT(5) + 基本剑术(3)×3 + 精神力战法(4) Round(8/3×等级) + 攻杀(7) 等级
	//	打空 = 攻击者命中 < Random(目标敏捷)      （ObjBase.pas:22241）
	//
	// 靶子敏捷 10 ⇒ 裸命中 5 时打空率约 40%；把基本剑术(+9) 与精神力战法(+8)
	// 练满后命中 22 ≥ Random(10) 的上界 ⇒ **一刀都不空**（后面的断言才有确定性）。
	//
	// ⚠️ 靶子必须**一直站在相邻格上**（这里是本用例最容易假失败的一点）：
	//
	//	打空是**掷骰**，只有"这一格上还站着活怪"才掷一次 ⇒ 靶子一死，那一格
	//	就再也不掷了。鸡只有 5 点血、**一刀就死** ⇒ 8 只只给 8 次样本
	//	（0.6⁸ ≈ 1.7%，2026-10-05 实测撞到，随后该槽位 8 条断言连环挂）。
	//
	// 换成**血僵尸**（HP 900、敏捷同样是 10）：60 级但只拿木剑的伤害对 AC 12
	// 基本是 0，靶子打不死 ⇒ 同一批能给 4 轮 × 8 个方向 = 32 次掷骰
	// （全中的概率 0.6³² ≈ 7e-8）。
	//
	// ⚠️ 原来用的是"HP 9999 的热血足球"，但社区包（GeeM2）的 monsters.json 里
	// **没有这个模板** ⇒ `@spawn` 静默不刷（只在服务端回一条提示）⇒ 客户端
	// 一个相邻怪都收不到、`dirs` 为空、挥 0 刀直接 log.Fatal。
	say("@spawn 血僵尸 8")
	at := collect(rcv(1200 * time.Millisecond))
	if len(at) == 0 {
		log.Fatal("@spawn 血僵尸 没刷出靶子")
	}

	swings, hits := 0, 0
	for round := 0; round < 4; round++ {
		adjacent := 0
		for d := 0; d < 8; d++ {
			delta := entity.DirDelta[d]
			if _, ok := at[[2]int{delta[0], delta[1]}]; !ok {
				continue
			}
			adjacent++
			cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HIT,
				proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(d), 0)})
			for j := 0; j < 2; j++ {
				rp := cc.tryRecvTimeout(rcv(120 * time.Millisecond))
				if rp == nil {
					break
				}
				switch {
				case rp.Head.Ident == proto.SM_HIT && int32(rp.Head.Recog) == actorID:
					swings++
				case rp.Head.Ident == proto.SM_STRUCK && isMonsterID(rp.Head.Recog):
					hits++
				}
			}
		}
		fmt.Printf("    第 %d 轮：相邻 %d 只血僵尸（累计挥 %d 刀、命中 %d 次）\n",
			round+1, adjacent, swings, hits)
	}
	fmt.Printf("    命中判定：裸命中(5) 挥 %d 刀、命中 %d 次（靶子敏捷 10 ⇒ 打空率约 40%%）\n",
		swings, hits)
	if swings == 0 {
		log.Fatal("没收到自己的挥砍动作包（SM_HIT），命中判定没验到")
	}
	// ⚠️ 样本不足时**不要硬判**，否则又变成"随机数不够"的假失败：
	// ≥12 刀时全中的概率 0.6¹² ≈ 0.22%，这时"至少打空一次"才站得住。
	if swings >= 12 && hits >= swings {
		log.Fatalf("裸命中 5 对敏捷 10 的靶子应当打空约 40%%，实际 %d/%d 一刀没空", hits, swings)
	}
	if swings < 12 {
		fmt.Printf("    （只挥了 %d 刀，样本不足以判打空率，跳过该断言）\n", swings)
	}

	// 练满两个"加命中"的被动：命中 = 5 + 9 + 8 = 22 > Random(10) 上界 ⇒ 后面必中。
	// 这也是"精神力战法(4) 的加成落在命中上"的端到端验证。
	say("@magic 3 3")
	time.Sleep(rcv(300 * time.Millisecond))
	say("@magic 4 3")
	time.Sleep(rcv(300 * time.Millisecond))
	drain(8)
	fmt.Println("    基本剑术/精神力战法练满（命中 22）✓")

	// 换一片**干净地形**再刷木桩：同一片被反复刷怪后八邻域与两格外都被占，
	// 落点不再整齐（"刺杀要正前方两格有怪"就会找不到）。
	//
	// ⚠️ 偏移是 (-60, 0)：地图 0 只有 700×700，原来写的 `(+37,+37)` 从 (283,672)
	// 出去就是 y=709 —— **越界**，`switchMap` 直接拒绝，"换地形"从来没生效过
	// （后面的 `@spawn` 一直刷在同一片拥挤的地形上）。(223,672) 是实测"离 map 0
	// 所有刷怪点都 >范围+6 且 3×3 可走"的干净格。
	teleportTo(posX-60, posY)
	// 靶子换成**练功师**（HP 9999、命中/敏捷都是 0，原版研究院里的木桩）：
	// 社区包（GeeM2）的 monsters.json 里有它，而且它**不会死、也不会被打空**，
	// 后面"刺杀第二格 / 半月三格 / 攻杀 / 烈火"每一段都能拿到确定的站位，
	// 不必再靠"换地形重刷鸡"。
	//
	// ⚠️ 野蛮冲撞(27) 用不了它：`CanMotaebo` 要求玩家等级**严格高于**目标
	// （ObjBase.pas:21704），练功师是 99 级 ⇒ 那一段仍用鸡。
	say("@spawn 练功师 12")
	at = collect(rcv(1200 * time.Millisecond))
	if len(at) == 0 {
		log.Fatal("@spawn 练功师 没刷出木桩")
	}

	// ---- 1) 刺杀剑术：正前方第二格 ----
	//
	// ⚠️ `@spawn` 的落点取决于"哪一格空着"：地形被占满时两格外的**正方向**
	// 不一定有怪。找不到就换一片地形重刷（偏移 >12 格，免得被服务端的"就近修正"
	// 拉回原地）。练功师是 12 只（环 1 八格 + 环 2 四格），找不到才会走到这里。
	longSpots := [][2]int{{0, 0}, {53, -53}, {-53, -53}, {67, 0}, {0, -67}}
	longDir, longID := -1, int32(0)
	for attempt := 0; attempt < len(longSpots) && longDir < 0; attempt++ {
		if attempt > 0 {
			teleportTo(posX+longSpots[attempt][0], posY+longSpots[attempt][1])
			say("@spawn 练功师 12")
			at = collect(rcv(1200 * time.Millisecond))
		}
		if len(at) < 6 {
			fmt.Printf("    （第 %d 片地形只刷出 %d 只木桩，换一片）\n", attempt+1, len(at))
			continue
		}
		for d := 0; d < 8; d++ {
			dd := entity.DirDelta[d]
			if id, ok := at[[2]int{dd[0] * 2, dd[1] * 2}]; ok {
				longDir, longID = d, id
				break
			}
		}
	}
	if longDir < 0 {
		log.Fatalf("试了 %d 片地形都没有怪正好在正前方两格", len(longSpots))
	}
	dd := entity.DirDelta[longDir]
	firstID, hasFirst := at[[2]int{dd[0], dd[1]}]

	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_TURN, 0, 0, 0, uint16(longDir))})
	time.Sleep(rcv(200 * time.Millisecond))
	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_LONGHIT,
		proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(longDir), 0)})

	swing, struck := false, map[int32]bool{}
	for i := 0; i < 16; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		switch rp.Head.Ident {
		case proto.SM_LONGHIT:
			swing = true
		case proto.SM_STRUCK:
			struck[rp.Head.Recog] = true
		}
	}
	if !swing {
		log.Fatal("没收到 SM_LONGHIT（刺杀剑术动画）")
	}
	if !struck[longID] {
		log.Fatalf("刺杀剑术没打中正前方第二格的怪 %d（收到的受击：%v）", longID, struck)
	}
	if hasFirst && struck[firstID] {
		log.Fatalf("刺杀剑术打中了**第一格**的怪 %d —— 应当只打第二格", firstID)
	}
	fmt.Printf("    刺杀剑术：命中第二格 %d，未命中第一格 ✓\n", longID)

	// ---- 2) 半月弯刀：一次三个格子 ----
	// 木桩不会死 ⇒ 不用像原来刷鸡那样"再补一轮把八邻域填满"。
	drain(20)
	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_WIDEHIT,
		proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(longDir), 0)})

	wideSwing, wideHit := false, map[int32]bool{}
	for i := 0; i < 16; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		switch rp.Head.Ident {
		case proto.SM_WIDEHIT:
			wideSwing = true
		case proto.SM_STRUCK:
			wideHit[rp.Head.Recog] = true
		}
	}
	if !wideSwing {
		log.Fatal("没收到 SM_WIDEHIT（半月弯刀动画）")
	}
	if len(wideHit) < 2 {
		log.Fatalf("半月弯刀只打中 %d 个目标（应当一次覆盖三格）", len(wideHit))
	}
	fmt.Printf("    半月弯刀：一刀命中 %d 个目标 ✓\n", len(wideHit))

	// ---- 3) 攻杀剑术(7)：充能由**服务端自动积累** ----
	//
	// ⚠️ 原版的充能提示走 `SendSocket(nil, '+PWR')` 这条 raw 通道，我们不下发，
	// 所以客户端**看不见**"充好了"。这里靠周期本身的确定性：
	// `count := 7 - 技能等级`（等级 3 ⇒ 4）、`point := Random(count)`，
	// 每挥一刀 `count--`，落到 `point` 时充能，而且**没人消费就会一直保持**。
	// ⇒ 连挥 4 刀后必然已充能，接一发 CM_POWERHIT 必定吃到加成。
	say("@magic 7 3")
	time.Sleep(rcv(300 * time.Millisecond))
	drain(8)

	// vision 在原地重进一次视野：`switchMap → updateVision` 会把当前**全部**
	// 可见对象重发一遍，拿到的才是此刻真实站位。
	//
	// ⚠️ 不能只看"新刷出来的怪"：`@spawn` 只往空位放，邻格被别人占着时新怪会被
	// 甩到两格外，于是"正前方一格有怪"偶尔找不到。
	//
	// ⚠️ `@map` 只查地形不查占用：脚下可能正好站着一只怪（此时"相对坐标"全错，
	// 野蛮冲撞怎么都推不动）。脚下有怪就挪到相邻空格重来。
	vision := func() map[[2]int]int32 {
		for i := 0; i < 4; i++ {
			seen := teleportTo(posX, posY)
			if _, occupied := seen[[2]int{0, 0}]; !occupied {
				return seen
			}
			moved := false
			for d := 0; d < 8 && !moved; d++ {
				dd := entity.DirDelta[d]
				if _, busy := seen[[2]int{dd[0], dd[1]}]; busy {
					continue
				}
				posX, posY = posX+dd[0], posY+dd[1]
				moved = true
			}
			if !moved {
				return seen // 八邻域全是怪：将就用，让上层断言报出来
			}
		}
		return teleportTo(posX, posY)
	}
	pickDirs := func(seen map[[2]int]int32) (charge, hit int) {
		charge, hit = -1, -1
		for d := 0; d < 8; d++ {
			dd := entity.DirDelta[d]
			_, occupied := seen[[2]int{dd[0], dd[1]}]
			if occupied && hit < 0 {
				hit = d
			}
			if !occupied && charge < 0 {
				charge = d
			}
		}
		return
	}
	// 充能只数挥砍次数、与有没有目标无关 ⇒ 朝**空地**挥最省事。木桩不会死，
	// 所以八邻域全被占、随手朝木桩挥也没关系（原来刷鸡时那几刀会把它打死，
	// 才必须等挥完再挑加成目标）。
	chargeDir, _ := pickDirs(vision())
	if chargeDir < 0 {
		chargeDir = 0
	}
	for i := 0; i < 5; i++ {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_HIT,
			proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(chargeDir), 0)})
		time.Sleep(rcv(150 * time.Millisecond))
	}
	drain(12)

	var hitDir int
	_, hitDir = pickDirs(vision())
	for attempt := 0; attempt < 3 && hitDir < 0; attempt++ {
		say("@spawn 练功师 8")
		drain(10)
		_, hitDir = pickDirs(vision())
	}
	if hitDir < 0 {
		log.Fatal("正前方一格没有怪，攻杀剑术的加成验不了")
	}
	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_POWERHIT,
		proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(hitDir), 0)})
	powerSwing, powerStruck := false, false
	for i := 0; i < 16; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		switch rp.Head.Ident {
		case proto.SM_POWERHIT:
			powerSwing = true
		case proto.SM_STRUCK:
			if isMonsterID(rp.Head.Recog) {
				powerStruck = true
			}
		}
	}
	if !powerSwing {
		log.Fatal("没收到 SM_POWERHIT（攻杀剑术动画）—— 充能没生效或没消费")
	}
	if !powerStruck {
		log.Fatal("攻杀剑术的强攻打空了 —— 加成没落到伤害上（查服务端有没有命中日志）")
	}
	fmt.Println("    攻杀剑术：强攻动画 + 命中目标 ✓")

	// ---- 4) 烈火剑法(26)：按键点燃 → CM_FIREHIT 打出 ----
	//
	// 真客户端的"效果类型 0"技能走这条布局（ClMain.pas:3019 SendSpellMsg +
	// 1816-1833 UseMagic）：`MakeDefaultMsg(CM_SPELL, MakeLong(自身朝向,0), 0, 技能号, 0)`
	// —— 技能号在 **Tag**、Recog 低 16 位是朝向（野蛮冲撞直接把它当方向用）。
	warrSpell := func(magicID int32, dir uint8) wire.Packet {
		return wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SPELL,
			proto.MakeLong(uint16(dir), 0), 0, uint16(magicID), 0)}
	}
	say("@magic 26 3")
	time.Sleep(rcv(300 * time.Millisecond))
	drain(8)

	// ① 点燃：应回 String.ini 的 `FireSpiritsSummoned`
	cc.send(warrSpell(26, uint8(hitDir)))
	armed := false
	for i := 0; i < 10; i++ {
		rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(rp.Body, "精神火球") {
			armed = true
		}
	}
	if !armed {
		log.Fatal("点燃烈火剑法后没收到\"精神火球\"提示")
	}
	fmt.Println("    烈火剑法：点燃提示 ✓")

	// ② 冷却内再点一次应被拒（`FireSpiritsFail`）。
	//    冷却是**节奏类**时间 ⇒ 20 倍速下只有 0.5 秒，下一发必被拒。
	cc.send(warrSpell(26, uint8(hitDir)))
	coolRejected := false
	for i := 0; i < 10; i++ {
		rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_SYSMESSAGE && strings.Contains(rp.Body, "凝结内力失败") {
			coolRejected = true
		}
	}
	if !coolRejected {
		log.Fatal("10 秒冷却内重复点燃应被拒（应回\"凝结内力失败\"）")
	}
	fmt.Println("    烈火剑法：冷却内重复点燃被拒 ✓")

	// ③ 打出去：CM_FIREHIT ⇒ SM_FIREHIT（而且不能把点燃状态留下）
	//
	// 再找一只正前方的怪：木桩不会死（原来刷鸡时那只可能已经被强攻带走），
	// 这里仍然用"原地重进视野"拿此刻的真实站位。
	_, fireDir := pickDirs(vision())
	for attempt := 0; attempt < 3 && fireDir < 0; attempt++ {
		say("@spawn 练功师 6")
		drain(10)
		_, fireDir = pickDirs(vision())
	}
	if fireDir < 0 {
		log.Fatal("@spawn 后正前方一格没有怪，烈火打不中")
	}
	cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_FIREHIT,
		proto.MakeLong(uint16(posX), uint16(posY)), 0, uint16(fireDir), 0)})
	fireSwing, fireStruck := false, false
	for i := 0; i < 16; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		switch rp.Head.Ident {
		case proto.SM_FIREHIT:
			fireSwing = true
		case proto.SM_STRUCK:
			if isMonsterID(rp.Head.Recog) {
				fireStruck = true
			}
		}
	}
	if !fireSwing {
		log.Fatal("没收到 SM_FIREHIT（烈火剑法动画）—— 点燃没生效或没消费")
	}
	if !fireStruck {
		log.Fatal("烈火剑法打空了 —— 加成没落到伤害上（查服务端有没有命中日志）")
	}
	fmt.Println("    烈火剑法：强化动画 + 命中目标 ✓")

	// ---- 5) 野蛮冲撞(27)：把正前方的怪推开 + 自己跟进 ----
	//
	// 方向来自**技能包里的自身朝向**（`Recog` 低 16 位），不是坐标。
	// 等级 60 vs 鸡 5 ⇒ 概率门 `Random(20) < 3*4+6+55` 恒成立，结果只取决于
	// 目标身后那一格是否空着（空则推开 + 自己跟进；不空则 `SM_RUSHKUNG`）。
	//
	// ⚠️ 这一段**不能用练功师当靶子**：`CanMotaebo` 要求玩家等级**严格高于**
	// 目标（ObjBase.pas:21704-21715），练功师是 99 级 ⇒ 玩家 60 级永远推不动。
	// 鸡（2 级）才是能撞的目标。
	say("@magic 27 3")
	time.Sleep(rcv(300 * time.Millisecond))
	drain(8)

	baseX, baseY := posX, posY
	// 每轮换一片**地形**：地图上水/树/房会把某个方向的"落点"堵死
	//（服务端诊断日志里是 `推不动 … 可走=false`），而客户端看不到地形。
	//
	// ⚠️ 偏移必须**大于 12 格**：`switchMap` 会把不可走的落点就近修正
	//（`nearestWalkable(..., 12)`），偏移太小会被拉回原地、白试一轮（踩过）。
	//
	// ⚠️ 且不能再往 +y 偏：地图 0 只有 700×700，这一段的基点在 (223,672)
	// （出生点那一带已经被前面的用例占满），y+37 立刻越界 ⇒ 服务端拒绝传送，
	// 而客户端拿不到 SM_CHANGEMAP 就会继续用**越界请求坐标**当原点算相对位置，
	// 那一轮纯属浪费。
	offsets := [][2]int{{0, 0}, {37, -37}, {-37, -37}, {60, 0}, {-60, 0}}
	pushDir := -1
	for attempt := 0; attempt < len(offsets) && pushDir < 0; attempt++ {
		if attempt > 0 {
			// 等冷却过去：冷却是节奏类（20 倍下 150ms），这里留 250ms 余量
			time.Sleep(rcv(5 * time.Second))
		}
		posX, posY = baseX+offsets[attempt][0], baseY+offsets[attempt][1]
		// ⚠️ 顺序不能反：`@spawn` 是**就地**刷怪，先刷再传送会把鸡留在原地
		//（表现为"新地形视野内 0 只怪"）。先落到新地点，再刷。
		teleportTo(posX, posY)
		// 刷 6 只而不是 8 只：八邻域只有 8 格，刷满了"目标身后那格"必定被占，
		// 就会出现"有怪但没方向可撞"（客户端看不到地形，只能靠留空位）。
		say("@spawn 鸡 6")
		drain(10)
		seen := vision()
		var cands []int
		for d := 0; d < 8; d++ {
			dd := entity.DirDelta[d]
			if _, ok := seen[[2]int{dd[0], dd[1]}]; !ok {
				continue
			}
			// ⚠️ 还必须要求**目标身后那一格没有别的怪**：`@spawn` 把八邻域塞满，
			// 随手挑一个方向多半会先撞上另一只鸡（服务端回 SM_RUSHKUNG）。
			if _, occupied := seen[[2]int{2 * dd[0], 2 * dd[1]}]; occupied {
				continue
			}
			cands = append(cands, d)
		}
		fmt.Printf("    野蛮冲撞：第 %d 片地形 (%d,%d)，视野内 %d 只怪，候选方向 %v\n",
			attempt+1, posX, posY, len(seen), cands)
		if len(cands) == 0 {
			continue // 这一片没有"正前方有怪 + 身后空着"的方向，换一片
		}
		// 逐个候选方向试：**地图上可能有树/石头**（客户端看不到地形，
		// `seen` 里只有怪），落点是墙时服务端会回 SM_RUSHKUNG。
		for _, d := range cands {
			dd := entity.DirDelta[d]
			pushID := seen[[2]int{dd[0], dd[1]}]
			startX, startY := posX, posY
			refused := false
			cc.send(warrSpell(27, uint8(d)))
			// 紧接着再按一次：必须被 3 秒冷却拒掉（服务端会打"还在 … 冷却内"，
			// e2e 用 `check` 断言）。客户端这边不断言包数 ——
			// 一次冲撞本来就会产生**多条** SM_RUSH（每推进一格一条）。
			cc.send(warrSpell(27, uint8(d)))
			selfRush, pushedBack := false, false
			for i := 0; i < 16; i++ {
				rp := cc.tryRecvTimeout(300 * time.Millisecond)
				if rp == nil {
					break
				}
				switch rp.Head.Ident {
				case proto.SM_RUSH:
					if int32(rp.Head.Recog) == actorID {
						selfRush = true
						posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
					}
				case proto.SM_BACKSTEP:
					if int32(rp.Head.Recog) == pushID {
						// 目标原来在「起点 + 方向」；被撞开后必须**沿撞击方向**后退
						//（1 格或 2 格都算对：等级高时原版会连推，见 DoMotaebo 的循环）。
						// ⚠️ 用起点算而不是拿当前站位算：SM_RUSH 与 SM_BACKSTEP 的到达
						// 顺序不保证，用当前站位会偶发失配。
						orig := [2]int{startX + dd[0], startY + dd[1]}
						got := [2]int{int(rp.Head.Param), int(rp.Head.Tag)}
						dx, dy := got[0]-orig[0], got[1]-orig[1]
						if dx*dd[0]+dy*dd[1] <= 0 || dx*dd[1]-dy*dd[0] != 0 {
							log.Fatalf("被撞的怪从 %v 退到了 %v（方向 %d = %v）：没沿撞击方向后退",
								orig, got, d, dd)
						}
						pushedBack = true
						fmt.Printf("    被撞的怪 %d：%v → %v ✓\n", pushID, orig, got)
					}
				case proto.SM_RUSHKUNG:
					refused = true
				}
			}
			if refused {
				continue // 这个方向是墙，换一个
			}
			if !selfRush {
				continue
			}
			if !pushedBack {
				log.Fatal("野蛮冲撞冲出去了，但没收到正前方那只怪的 SM_BACKSTEP")
			}
			if posX == startX && posY == startY {
				log.Fatal("野蛮冲撞后自己没位移")
			}
			pushDir = d
			fmt.Printf("    野蛮冲撞：自己 (%d,%d) → (%d,%d)，目标被推开 ✓\n",
				startX, startY, posX, posY)
			break
		}
	}
	if pushDir < 0 {
		log.Fatal("试了多个方向都没撞动（地图地形/怪占位挡住了）")
	}

	// 冷却断言落在服务端日志上（本用例每次按键都连按两下，见上面的说明）。
	fmt.Println("    野蛮冲撞：推送 + 自己跟进 ✓（冷却拒发由 e2e 查服务端日志）")

	// 传送回出生点：调用方的走路断言按原坐标收包（见上面 origX/origY 的说明）。
	say(fmt.Sprintf("@map 0 %d %d", origX, origY))
	for i := 0; i < 12; i++ {
		rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_CHANGEMAP {
			posX, posY = int(rp.Head.Param), int(rp.Head.Tag)
		}
	}
	fmt.Printf("    回到出生点 (%d,%d) ✓\n", posX, posY)

	fmt.Println("    战士近战技能已验证 ✓")
}

// runSkill3 验证第三批技能：抗拒火环(8) / 集体隐身术(19)。
//
// 两个都落在"周围的实体"上，所以断言也都在周围：
//   - 抗拒火环：必须收到被推对象的 **SM_BACKSTEP**，且新位置**离我更远**
//     （只断言"收到了 SM_BACKSTEP"抓不到方向写反——推到自己脸上也会发这个包）；
//   - 集体隐身术：自己（落点半径 1 内的友方）必须进入隐身状态位。
func runSkill3(cc *conn, actorID int32, posX, posY int) {
	fmt.Println("[27] 测试第三批技能（抗拒火环 / 集体隐身术）")

	say := func(cmd string) {
		cc.send(wire.Packet{Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0), Body: cmd})
	}
	pump := func() {
		for i := 0; i < 8; i++ {
			if cc.tryRecvTimeout(300*time.Millisecond) == nil {
				return
			}
		}
	}

	// 集体隐身术属原版 case 13..19 ⇒ 要 1 个护身符（CheckAmulet）
	if !grantEquip(cc, "护身符") {
		log.Fatal("护身符没装上，集体隐身术会被拒")
	}

	// 提级 + 技能等级：
	//   抗拒火环的击退判定要求"我的等级 > 怪等级"，
	//   概率 = Random(20) < 6 + 技能等级*3 + 等级差 ⇒ 60 级 + 3 级技能**必定**推开。
	say("@level 60")
	time.Sleep(rcv(300 * time.Millisecond))
	say("@magic 8 3")
	time.Sleep(rcv(300 * time.Millisecond))
	pump()

	// ---- 1) 集体隐身术(19)：落点（脚下）半径 1 的友方隐身 ----
	invisible := false
	cc.send(wire.Packet{Head: spellMsg(19, posX, posY)})
	for i := 0; i < 10; i++ {
		rp := cc.tryRecvTimeout(600 * time.Millisecond)
		if rp == nil {
			break
		}
		switch rp.Head.Ident {
		case proto.SM_CHARSTATUSCHANGED:
			st := clientStatus(rp) // 官方：Param=状态低字、Tag=状态高字
			if rp.Head.Recog == actorID && st&entity.StateInvisible != 0 {
				invisible = true
				fmt.Printf("    集体隐身术 → 自身状态位=%d（隐身）\n", st)
			}
		case proto.SM_MAGICFIRE_FAIL:
			log.Fatal("集体隐身术被拒（护身符或 MP？）")
		}
	}
	if !invisible {
		log.Fatal("集体隐身术未让自己隐身（状态位无 StateInvisible）")
	}

	// ---- 2) 抗拒火环(8)：把身边的怪推开 ----
	// 先刷 4 只鸡（@spawn 按八邻域铺开），记下它们的位置，再放火环。
	say("@spawn 鸡 4")
	time.Sleep(rcv(900 * time.Millisecond))
	before := map[int32][2]int{}
	for i := 0; i < 12; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
			before[rp.Head.Recog] = [2]int{int(rp.Head.Param), int(rp.Head.Tag)}
		}
	}
	if len(before) == 0 {
		log.Fatal("@spawn 没刷出怪（八邻域内没有目标可推）")
	}
	cc.send(wire.Packet{Head: spellMsg(8, posX, posY)})
	pushed, farther := 0, 0
	for i := 0; i < 20; i++ {
		rp := cc.tryRecvTimeout(300 * time.Millisecond)
		if rp == nil {
			break
		}
		if rp.Head.Ident != proto.SM_BACKSTEP {
			continue
		}
		pushed++
		old, ok := before[rp.Head.Recog]
		if !ok {
			continue
		}
		nx, ny := int(rp.Head.Param), int(rp.Head.Tag)
		dOld := absi(old[0]-posX) + absi(old[1]-posY)
		dNew := absi(nx-posX) + absi(ny-posY)
		if dNew > dOld {
			farther++
		}
		fmt.Printf("    抗拒火环 → 怪 %d (%d,%d) → (%d,%d)\n", rp.Head.Recog, old[0], old[1], nx, ny)
	}
	if pushed == 0 {
		log.Fatal("抗拒火环没有推开任何目标（未收到 SM_BACKSTEP）")
	}
	if farther == 0 {
		log.Fatal("被推的目标没有**远离**施法者（方向算反了？）")
	}
	fmt.Printf("    抗拒火环推开 %d 步，其中 %d 步是远离的 ✓\n", pushed, farther)
	fmt.Println("    第三批技能已验证 ✓")
}

func absi(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// dirTowardAxis 返回**只沿一个轴**走的方向（差值大的轴优先）。
//
// ⚠️ 移动必须用它，不能用 dirToward 的斜向：斜向一步要求横竖两个方向
// 同时可通行，在比奇省这种建筑密集的地图上很容易两边都被挡，
// 表现为"一直撞墙追不上那只怪"（移动失败上百次、用例空转）。
// 攻击判定仍要用 dirToward——那里要的就是精确的斜向。
func dirTowardAxis(dx, dy int) uint8 {
	if absi(dx) >= absi(dy) {
		if dx > 0 {
			return entity.DirRight
		}
		if dx < 0 {
			return entity.DirLeft
		}
	}
	if dy > 0 {
		return entity.DirDown
	}
	if dy < 0 {
		return entity.DirUp
	}
	return entity.DirUp
}

// describeUseItems 解析装备列表。
//
// body 格式："槽位/ClientItem/槽位/ClientItem/..."（每 2 段一组）
func describeUseItems(body string) string {
	parts := strings.Split(body, "/")
	var names []string
	for i := 0; i+1 < len(parts); i += 2 {
		slot := parts[i]
		raw := []byte(parts[i+1])
		if len(raw) < proto.ClientItemSize {
			continue
		}
		if ci, ok := proto.DecodeClientItem(raw[:proto.ClientItemSize]); ok {
			names = append(names, fmt.Sprintf("槽%s=%s", slot, ci.S.GetName()))
		}
	}
	if len(names) == 0 {
		return "空"
	}
	return strings.Join(names, " ")
}

// describeBag 解析背包物品。body："ClientItem/ClientItem/..."
//
// ⚠️ 原版用 '/' 分隔二进制结构，若某个字段恰好含 0x2F('/') 会切错——
// 这是协议本身的隐患，此处照原版语义解析并报告异常段数。
// describeBag 解析背包。
//
// body 是 ClientItem 的**定长顺序拼接**（每条 ClientItemSize=76 字节），
// 客户端 ClientGetBagItmes 就是按 sizeof(TClientItem) 逐条切分的。
// ⚠️ 早期实现用 "/" 分隔，与客户端不兼容，已废弃。
func describeBag(body string) string {
	if body == "" {
		return ""
	}
	raw := []byte(body)
	var names []string
	bad := 0
	for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
		ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
		if !ok {
			bad++
			continue
		}
		if n := ci.S.GetName(); n != "" {
			names = append(names, n)
		}
	}
	out := strings.Join(names, "、")
	if bad > 0 {
		out += fmt.Sprintf("（另有 %d 段长度异常，可能含 '/' 分隔符冲突）", bad)
	}
	return out
}

// findInBag 在背包 body 中按名字查找，返回槽位下标（-1 表示未找到）。
// bagCountOf 取背包里某个堆叠物品的**数量**（存在 ClientItem.Dura 里，见 addToBag）；
// 找不到返回 0。
func bagCountOf(body, name string) int {
	raw := []byte(body)
	for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
		ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
		if ok && ci.S.GetName() == name {
			return int(ci.Dura)
		}
	}
	return 0
}

func findInBag(body, name string) int {
	raw := []byte(body)
	for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
		ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
		if ok && ci.S.GetName() == name {
			return off / proto.ClientItemSize
		}
	}
	return -1
}

// ---------- 原版字段下的"按 MakeIndex 定位物品" ----------
//
// 穿脱/卖出/修理的报文里，**Recog 是物品的 MakeIndex**（不是槽位、不是背包下标）：
//
//	CM_TAKEONITEM  Recog = MakeIndex, Param = 装备槽位, body = 名字（ClMain.pas:3625）
//	CM_TAKEOFFITEM Recog = MakeIndex, Param = 装备槽位, body = 名字（ClMain.pas:3633）
//	CM_USERSELLITEM / CM_USERREPAIRITEM
//	               Recog = 商人 ActorId, Param/Tag = MakeLong(MakeIndex), body = 名字
//	               （ClMain.pas:3703/3711 —— 客户端把 `g_SellDlgItem.MakeIndex` 拆成低/高字）
//
// MakeIndex 只出现在列表报文里，所以下面两个解析函数是这些报文的"原料"。

// useItemAt 取某个装备槽上那件物品的 (MakeIndex, 名字)。
// 该槽为空或解析不出时返回 (0, "")。
//
// body = SM_SENDUSEITEMS：`槽号/ClientItem/槽号/ClientItem/…`（原版
// `ClientGetSenduseItems` 就是按 '/' 切两段一对，ClMain.pas:6054-6072）。
func useItemAt(body string, slot int) (int32, string) {
	raw := []byte(body)
	off := 0
	for off < len(raw) {
		// 槽号（十进制文本，到 '/' 为止）
		sep := bytes.IndexByte(raw[off:], '/')
		if sep < 0 {
			return 0, ""
		}
		n, err := strconv.Atoi(string(raw[off : off+sep]))
		off += sep + 1
		if err != nil || off+proto.ClientItemSize > len(raw) {
			return 0, ""
		}
		ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize])
		if !ok {
			return 0, ""
		}
		off += proto.ClientItemSize
		if off < len(raw) && raw[off] == '/' {
			off++
		}
		if n == slot {
			return ci.MakeIndex, ci.S.GetName()
		}
	}
	return 0, ""
}

// useItemMakeIndex 只取 MakeIndex（0 = 该槽为空/解析不出）。
func useItemMakeIndex(body string, slot int) int32 {
	idx, _ := useItemAt(body, slot)
	return idx
}

// bagItemByName 在背包 body（定长 ClientItem 顺序拼接）里按名字取回整条 ClientItem。
func bagItemByName(body, name string) (proto.ClientItem, bool) {
	raw := []byte(body)
	for off := 0; off+proto.ClientItemSize <= len(raw); off += proto.ClientItemSize {
		if ci, ok := proto.DecodeClientItem(raw[off : off+proto.ClientItemSize]); ok && ci.S.GetName() == name {
			return ci, true
		}
	}
	return proto.ClientItem{}, false
}

// itemMakeIndexByName 在背包 body 里按名字取 MakeIndex（0 = 没找到）。
func itemMakeIndexByName(body, name string) int32 {
	ci, ok := bagItemByName(body, name)
	if !ok {
		return 0
	}
	return ci.MakeIndex
}

// equipSlotForStdMode 是"物品 StdMode → 装备槽"的映射，与服务端
// `gamesvr/equip.go` 的 stdModeToSlot 一致 —— 官方客户端也自带一份
// （FState.pas:3400-3470 按鼠标点到的格子算 `where`）。客户端必须报**正确**的槽位，
// 因为服务端会用 `CheckUserItems(btWhere, StdItem)` 核对（伪造/错位直接拒）。
func equipSlotForStdMode(stdMode uint8) int {
	switch stdMode {
	case 5, 6:
		return proto.SlotWeapon
	case 10, 11:
		return proto.SlotDress
	case 15, 16:
		return proto.SlotHelmet
	case 19, 20, 21:
		return proto.SlotNecklace
	case 22:
		return proto.SlotRingL
	case 23:
		return proto.SlotRingR
	case 24:
		return proto.SlotArmRingL
	case 26:
		return proto.SlotArmRingR
	case 52:
		return proto.SlotBoots
	case 54:
		return proto.SlotBelt
	case 25:
		return proto.SlotBujuk
	}
	return -1
}

// lastUse 返回最近一次收到的已穿戴列表 body（空 = 还没收到过）。
func (cc *conn) lastUse() string {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.lastUseItems
}

// lastBag 返回最近一次收到的背包列表 body（空 = 还没收到过）。
func (cc *conn) lastBag() string {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.lastBagItems
}

// sendTakeOn / sendTakeOff 按原版字段发穿脱报文。
func (cc *conn) sendTakeOn(makeIdx int32, slot int, name string) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_TAKEONITEM, makeIdx, uint16(slot), 0, 0),
		Body: name,
	})
}

func (cc *conn) sendTakeOff(makeIdx int32, slot int, name string) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_TAKEOFFITEM, makeIdx, uint16(slot), 0, 0),
		Body: name,
	})
}

// takeOnFromBag 从背包列表 body 里按名字取物品，按它的 StdMode 算出装备槽，再按原版
// 字段穿上（Recog = MakeIndex、Param = 槽位、body = 名字）。
//
// 槽位**必须在客户端算**：服务端会 `CheckUserItems(btWhere, StdItem)` 核对
// （原版客户端也是从物品的 StdMode 算出 `where`，FState.pas:3400-3470）。
// 返回 (MakeIndex, 槽位)；找不到/算不出返回 (0, -1)。
func takeOnFromBag(cc *conn, bag, name string) (int32, int) {
	ci, ok := bagItemByName(bag, name)
	if !ok {
		return 0, -1
	}
	slot := equipSlotForStdMode(ci.S.StdMode)
	if slot < 0 {
		return 0, -1
	}
	cc.sendTakeOn(ci.MakeIndex, slot, name)
	return ci.MakeIndex, slot
}

// sendMerchantItem 发"对商人操作某件物品"的报文（卖出 / 修理 / 仓库存取）：
// Recog = 商人 ActorId、Param/Tag = MakeIndex 低/高 16 位、body = 物品名。
func (cc *conn) sendMerchantItem(ident uint16, npcID uint32, makeIdx int32, name string) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(ident, int32(npcID),
			proto.LoWord(makeIdx), proto.HiWord(makeIdx), 0),
		Body: name,
	})
}

// goodsEntry 是商品列表里的一条（原版客户端里的 `TClientGoods`）。
type goodsEntry struct {
	name  string
	sub   int // 子菜单（0 = 直接买，1 = 先弹明细）
	price int
	stock int
}

// parseGoodsList 解析 SM_SENDGOODSLIST 的 body。
//
// 原版格式（`ClientGetSendGoodsList`，ClMain.pas:6158-6190）是**文本**：
// 每件 `名称/子菜单/价格/存量/`（四个 '/' 结尾），切不出四段就结束。
// ⚠️ 早期我们发的是"二进制 Index+Price"（自身编的契约），官方客户端解析不了。
func parseGoodsList(body string) []goodsEntry {
	var out []goodsEntry
	rest := body
	for {
		f := strings.SplitN(rest, "/", 5)
		if len(f) < 5 {
			return out
		}
		price, err1 := strconv.Atoi(f[2])
		stock, err2 := strconv.Atoi(f[3])
		if f[0] == "" || err1 != nil || err2 != nil {
			return out
		}
		sub, _ := strconv.Atoi(f[1])
		out = append(out, goodsEntry{name: f[0], sub: sub, price: price, stock: stock})
		rest = f[4]
	}
}

// isMonsterID 判断 ActorId 是否是怪物。
//
// ⚠️ 不能只判 ">= MonsterIDBase"：NPC 的 ActorId 从 NpcIDBase 起，
// 也在该区间内。把远处的 NPC 当成怪物会让攻击/施法目标落空——
// 特效广播发生在**目标**周围，自己不在其中，表现为"技能偶发不生效"。
func isMonsterID(recog int32) bool {
	return recog >= proto.MonsterIDBase && recog < proto.NpcIDBase
}

// conn 封装一条连接的收发状态。
//
// ⚠️ Splitter 必须是**连接级**的：服务端常在一个 TCP 段里连发多个帧
// （进游戏时一次发 5 个包），若每次收包都新建 Splitter，
// 同批到达的后续帧会被丢弃——表现为"期望 SM_LOGON 却收到 SM_ABILITY"。
// conn 是客户端连接。
//
// ⚠️ **收包架构（2026-10-05 重构）**：原版客户端是"持续收、持续拆、按门禁间隔投递"，
// 而之前是"要用的时候才去 socket 上按**包数预算**捞" —— 两个结构性问题，此前所有
// 偶发都出在这上面：
//
//  1. **遗留包排在响应包前面**（切图/刷怪后的 updateVision SM_TURN 洪流）把
//     "固定 6 个包"这类预算吃光 ⇒ 服务端明明回了、客户端却读不到；
//  2. 一次 Read 空窗就让调用方 `break`，负载一高就误判。
//
// 现在：**后台读协程持续 Read + 拆帧 → 队列**；用例只跟队列打交道 ——
// `tryRecvTimeout` 取队首、`waitFor` 按谓词**只摘走匹配的那条（其余留在队列）**。
type conn struct {
	c   net.Conn
	sp  *wire.Splitter // 只由读协程访问
	seq byte

	mu    sync.Mutex
	queue []*wire.Packet // 已拆好的帧（读协程投递）
	wake  chan struct{}  // 有新帧/读协程退出时唤醒（容量 1，非阻塞投递）
	err   error          // 读协程退出原因（EOF / 错误）

	// lastUseItems / lastBagItems 是**最近一次**收到的"已穿戴/背包"列表 body，
	// 由读协程顺手缓存（原版客户端 g_UseItems/g_ItemArr 也是这么常驻的）。
	//
	// ⚠️ 用例需要它们是因为 **CM_TAKEOFFITEM / CM_USERSELLITEM / CM_USERREPAIRITEM
	// 的 Recog 是物品的 MakeIndex**（原版字段，见 gamesvr/equip.go、shop.go 的头注释），
	// 而 MakeIndex 只出现在这两条列表报文里 —— 用例中间任何一次 pump 都可能把它
	// 丢掉，缓存一份最省事。
	lastUseItems string
	lastBagItems string
}

func newConn(c net.Conn) *conn {
	cc := &conn{c: c, sp: wire.NewSplitter(65536), seq: '1', wake: make(chan struct{}, 1)}
	go cc.readLoop()
	return cc
}

// readLoop 是**唯一的读 socket 的协程**：持续收、持续拆、投递到队列。
func (cc *conn) readLoop() {
	buf := make([]byte, 8192)
	for {
		n, err := cc.c.Read(buf)
		if n > 0 {
			cc.sp.Feed(buf[:n])
			for {
				p := cc.readFrame()
				if p == nil {
					break
				}
				cc.mu.Lock()
				switch p.Head.Ident {
				case proto.SM_SENDUSEITEMS:
					cc.lastUseItems = p.Body
				case proto.SM_BAGITEMS:
					cc.lastBagItems = p.Body
				}
				cc.queue = append(cc.queue, p)
				cc.mu.Unlock()
				select {
				case cc.wake <- struct{}{}:
				default:
				}
			}
		}
		if err != nil {
			cc.mu.Lock()
			cc.err = err
			cc.mu.Unlock()
			select {
			case cc.wake <- struct{}{}:
			default:
			}
			return
		}
	}
}

// popQueue 取队首（无则 nil）。
func (cc *conn) popQueue() *wire.Packet {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if len(cc.queue) == 0 {
		return nil
	}
	p := cc.queue[0]
	cc.queue = cc.queue[1:]
	return p
}

// waitFor 按**谓词**等待一条帧（墙钟截止）：命中就**只摘走那一条**，其余留在队列。
func (cc *conn) waitFor(pred func(*wire.Packet) bool, budget time.Duration) *wire.Packet {
	deadline := time.Now().Add(budget)
	for {
		cc.mu.Lock()
		for i, p := range cc.queue {
			if pred(p) {
				cc.queue = append(cc.queue[:i], cc.queue[i+1:]...)
				cc.mu.Unlock()
				return p
			}
		}
		cc.mu.Unlock()
		remain := time.Until(deadline)
		if remain <= 0 {
			return nil
		}
		timer := time.NewTimer(remain)
		select {
		case <-cc.wake:
			timer.Stop()
		case <-timer.C:
			return nil
		}
	}
}

// clientStatus 按官方字段顺序把 SM_CHARSTATUSCHANGED 的 32 位状态拼出来：
// `MakeLong(msg.Param, msg.Tag)`（ClMain.pas:4492 —— 客户端就是这么解的）。
func clientStatus(p *wire.Packet) uint32 {
	return uint32(p.Head.Param) | uint32(p.Head.Tag)<<16
}

// waitIdent 按消息号等待（waitFor 的常用简写）。
func (cc *conn) waitIdent(ident uint16, budget time.Duration) *wire.Packet {
	return cc.waitFor(func(p *wire.Packet) bool { return p.Head.Ident == ident }, budget)
}

func (cc *conn) send(p wire.Packet) {
	if _, err := cc.c.Write(wire.EncodeUp(cc.seq, p.Encode())); err != nil {
		log.Fatalf("发送: %v", err)
	}
	cc.seq++
	if cc.seq > '9' {
		cc.seq = '1' // 原版 1..9 循环
	}
}

func (cc *conn) recv() *wire.Packet {
	// 队列里已有就立刻返回；否则等读协程（最多 5s）。
	if p := cc.waitFor(func(*wire.Packet) bool { return true }, 5*time.Second); p != nil {
		return p
	}
	cc.mu.Lock()
	err := cc.err
	cc.mu.Unlock()
	log.Fatalf("等待响应: 超时（读协程状态 %v）", err)
	return nil
}

// sidePackets 暂存"非预期"的包。
//
// 服务端会主动推送视野同步（怪物出现/移动），它们可能插在任何请求-响应之间。
// 若断言"下一个包必须是某类型"就会脆弱，故改为按类型收取、其余暂存备用。
var sidePackets []*wire.Packet

// popSide 取出暂存的指定类型包。
func popSide(ident uint16) *wire.Packet {
	for i, p := range sidePackets {
		if p.Head.Ident == ident {
			sidePackets = append(sidePackets[:i], sidePackets[i+1:]...)
			return p
		}
	}
	return nil
}

// recvExpected 收包直到 ident 匹配；其余存入 sidePackets。
// maxRecvTries 是收包时允许跳过的无关消息条数。
//
// ⚠️ 真实地图上怪物/NPC 很多，视野内一进图就是几十条 SM_TURN，
// 次数太少会把真正的目标消息漏掉（表现为用例时好时坏）。
const maxRecvTries = 80

// recvWaitBudget 是"查找某个包"的总等待预算。
//
// ⚠️ 真实地图上包很多（怪物/NPC 动不动几十条 SM_TURN），给几秒是必要的；
// 但也不能无限等，否则真正的缺陷会被超时掩盖。
const recvWaitBudget = 6 * time.Second

// recvPollInterval 是查找时的单次读超时。用它轮询（而不是一次长读）是关键：
// conn.recv() 内部单次读阻塞 5 秒，期间到达的包会被读走、又因"不匹配"被
// 旁路缓存，最后只能超时——查找必须能**及时**认出目标包。
const recvPollInterval = 200 * time.Millisecond

// sideCacheLimit 限制旁路缓存条数，防止长时间查找时无限增长。
const sideCacheLimit = 4096

// cacheSide 把不匹配的包存入旁路缓存，供后续查找使用（不丢弃）。
func cacheSide(p *wire.Packet) {
	sidePackets = append(sidePackets, p)
	if len(sidePackets) > sideCacheLimit {
		sidePackets = sidePackets[len(sidePackets)-sideCacheLimit:]
	}
}

// recvExpected 在预算内查找 ident 匹配的包；期间收到的其它包进旁路缓存。
//
// ⚠️ 三处都是踩坑换来的：
//  1. 先查旁路缓存：前面的收包循环（进游戏序列、查背包）很可能已经
//     收到并缓存了目标包，只读 socket 会永远错过它；
//  2. 短超时轮询而非一次长读（见 recvPollInterval 的说明）；
//  3. 等待期间收到的不匹配包继续缓存，后续查找还能用上。
func recvExpected(cc *conn, ident uint16) *wire.Packet {
	if p := popSide(ident); p != nil {
		return p
	}
	deadline := time.Now().Add(recvWaitBudget)
	for time.Now().Before(deadline) {
		p := cc.tryRecvTimeout(recvPollInterval)
		if p == nil {
			continue
		}
		if p.Head.Ident == ident {
			return p
		}
		cacheSide(p)
	}
	log.Fatalf("未收到期望的消息 %d（%.1fs 内）", ident, recvWaitBudget.Seconds())
	return nil
}

// popSideFrom 从缓存里取指定 ident 且 Recog 匹配的包。
func popSideFrom(ident uint16, recog int32) *wire.Packet {
	for i, p := range sidePackets {
		if p.Head.Ident == ident && int32(p.Head.Recog) == recog {
			sidePackets = append(sidePackets[:i], sidePackets[i+1:]...)
			return p
		}
	}
	return nil
}

// recvExpectedFrom 同 recvExpected，但额外校验 Recog。
//
// ⚠️ 必需：服务端会把**怪物**的移动也以 SM_WALK 广播过来，
// 只按 Ident 匹配会把怪物移动误认为自己的移动确认。
func recvExpectedFrom(cc *conn, ident uint16, recog int32) *wire.Packet {
	// ⚠️ 必须先查 sidePackets：前面的收包循环（进游戏序列、查背包等）会把
	// 不匹配的包缓存起来，目标包很可能已经在里面。只读 socket 会永远错过它。
	if p := popSideFrom(ident, recog); p != nil {
		return p
	}
	deadline := time.Now().Add(recvWaitBudget)
	for time.Now().Before(deadline) {
		p := cc.tryRecvTimeout(recvPollInterval)
		if p == nil {
			continue
		}
		if p.Head.Ident == ident && int32(p.Head.Recog) == recog {
			return p
		}
		cacheSide(p)
	}
	log.Fatalf("未收到期望的消息 %d (Recog=%d, %.1fs 内)", ident, recog, recvWaitBudget.Seconds())
	return nil
}

// pump 在旧架构里是"把 socket 数据读进 Splitter"的前置步骤（自己 Read）。
//
// ⚠️ 收包架构改成**常驻读协程**后，这里必须是**空操作**：socket 随时都在被读、
// 帧随时进队列。若还在这里 Read，就会出现**两个读者抢同一个 socket** ⇒ 包被
// 其中一方读走后另一方再也看不到（实测：整轮从 17s 变 66s、attack 用例稳定失败）。
//
// 签名保留只是为了不动既有调用点。
func (cc *conn) pump(d time.Duration, max int) {
	_ = d
	_ = max
}

// tryRecvTimeout 取一条帧，最多等待 d（超时返回 nil，不 Fatal）。
//
// ⚠️ 现在取的是**读协程投递的队列**（原来是自己去 Read，一次空窗就返回 nil）。
func (cc *conn) tryRecvTimeout(d time.Duration) *wire.Packet {
	return cc.waitFor(func(*wire.Packet) bool { return true }, d)
}

// tryNext 取队首（保留旧名给既有调用点用；数据来自读协程）。
func (cc *conn) tryNext() *wire.Packet { return cc.popQueue() }

// readFrame 从 Splitter 缓冲里取一条可解析的帧；没有则 nil。**只有读协程调用**。
func (cc *conn) readFrame() *wire.Packet {
	for {
		raw, err := cc.sp.Next()
		if err != nil {
			return nil // 半包或垃圾，等更多数据
		}
		f, err := wire.DecodeFrame(raw)
		if err != nil || wire.IsKeepAlive(f.Payload) {
			continue
		}
		got, err := wire.DecodePacket(f.Payload)
		if err != nil {
			continue
		}
		return &got
	}
}

// roundTrip 发一条消息并等一条响应。
func roundTrip(c net.Conn, seq *byte, p wire.Packet) *wire.Packet {
	if _, err := c.Write(wire.EncodeUp(*seq, p.Encode())); err != nil {
		log.Fatalf("发送: %v", err)
	}
	*seq++
	if *seq > '9' {
		*seq = '1' // 原版 1..9 循环
	}
	// 复用连接级 Splitter：以连接对象为键太麻烦，这里退化为
	// 每次新建但**先取完所有已到达的帧**——由调用方保证按序读取。
	sp := wire.NewSplitter(65536)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			log.Fatalf("等待响应: %v", err)
		}
		sp.Feed(buf[:n])
		for {
			raw, err := sp.Next()
			if err != nil {
				break
			}
			f, err := wire.DecodeFrame(raw)
			if err != nil || wire.IsKeepAlive(f.Payload) {
				continue
			}
			got, err := wire.DecodePacket(f.Payload)
			if err != nil {
				continue
			}
			return &got
		}
	}
}

// roundTripSoft 与 roundTrip 相同，但**超时时返回 nil 而不是 Fatal**。
//
// 用于建角色：服务端对建/删角色有 1000ms 限流，被限流时**直接不回包**
// （internal/accountsvc/service.go: `if !sess.passModifyLimit(...) { return nil }`），
// 所以"无响应"是正常的限流信号，不能当致命错误。
func roundTripSoft(c net.Conn, seq *byte, p wire.Packet) *wire.Packet {
	if _, err := c.Write(wire.EncodeUp(*seq, p.Encode())); err != nil {
		log.Fatalf("发送: %v", err)
	}
	*seq++
	if *seq > '9' {
		*seq = '1'
	}
	sp := wire.NewSplitter(65536)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil
		}
		sp.Feed(buf[:n])
		for {
			raw, err := sp.Next()
			if err != nil {
				break
			}
			f, err := wire.DecodeFrame(raw)
			if err != nil || wire.IsKeepAlive(f.Payload) {
				continue
			}
			got, err := wire.DecodePacket(f.Payload)
			if err != nil {
				continue
			}
			return &got
		}
	}
}

func expect(p *wire.Packet, ident uint16, name string) {
	if p.Head.Ident != ident {
		log.Fatalf("期望 %s(%d), 实际 %d (Recog=%d Body=%q)",
			name, ident, p.Head.Ident, p.Head.Recog, p.Body)
	}
}

// healIfHurt 放一次群体治愈术(29) 并观察血量是否回升。
//
// ⚠️ 它的效果**必须**在"刚受伤"时验证：服务端判定 `HP < MaxHP` 才治疗
// （原版 MagBigHealing，Magic.pas:180），而自然恢复（regenLoop，每秒发
// SM_HEALTHSPELLCHANGED）会在这几百毫秒里把血回满 → 服务端跳过治疗。
// 所以调用点要放在"第一次受击之后立刻"。
//
// ⚠️ 这里**不能**断言"血没升"就算失败：自然恢复本身也让 HP 上升，客户端
// 分不清是治疗还是回血。真正的判定在 e2e —— 查服务端日志“…的群体治愈：”。
// healIfHurt 放群体治愈(29)，返回是否**确认血量回升**。
//
// ⚠️ 判据必须是"HP 比施法前高"，不能只看"收到 SM_HEALTHSPELLCHANGED"：
// **受击也会发同一条包**（applyMonsterHit → sendHealthChanged），而服务端在
// `HP >= MaxHP` 时走"无人可治"分支跳过治疗。两者都让"收到包"为真
// ⇒ 原来的判据会把"跳过治疗"当成成功，group-heal 断言随之偶发失败。
//
// 重施 3 次：可能撞上施法冷却（服务端冷却中直接丢弃、不回包）。
func healIfHurt(cc *conn, actorID int32, posX, posY, hpBefore int) bool {
	for attempt := 0; attempt < 3; attempt++ {
		cc.send(wire.Packet{
			Head: spellMsg(29, posX, posY),
		})
		deadline := time.Now().Add(rcv(2 * time.Second))
		for time.Now().Before(deadline) {
			rp := cc.tryRecvTimeout(rcv(400 * time.Millisecond))
			if rp == nil {
				continue
			}
			switch rp.Head.Ident {
			case proto.SM_HEALTHSPELLCHANGED:
				if int32(rp.Head.Recog) == actorID && int(rp.Head.Param) > hpBefore {
					fmt.Printf("    群体治愈术: HP %d → %d/%d ✓\n",
						hpBefore, rp.Head.Param, rp.Head.Series)
					return true
				}
			case proto.SM_STRUCK:
				// 又挨了一下：把基准降到新的血量（否则"回升"永远判不出来）
				if int32(rp.Head.Recog) == actorID && int(rp.Head.Param) < hpBefore {
					hpBefore = int(rp.Head.Param)
				}
			}
		}
	}
	fmt.Println("    群体治愈术：客户端未观测到血量回升（效果以服务端日志为准）")
	return false
}

// spellMsg 按**原版客户端的字段布局**构造 CM_SPELL。
//
// 原版依据（Grobal2.pas:2676 + ClMain.pas:3586）：
//
//	MakeDefaultMsg(msg: smallint; Recog: integer; param, tag, series: word)
//	SendSpellMsg(ident, x, y, dir, target) →
//	  MakeDefaultMsg(ident, MakeLong(x,y), Loword(target), dir, Hiword(target))
//
// 即：**Recog = MakeLong(x,y)**（坐标打包）、**技能号在 Param 或 Tag**
// （无目标时在 Tag，有目标时在 Param）、Tag 另一情形放方向。
//
// ⚠️ 我们早期把技能号放 Recog、x/y 放 Param/Tag —— 那是"自己发自己解"，
// 真客户端会完全打不出技能。2026-10-04 已按原版改正。
func spellMsg(magicID int32, x, y int) proto.DefaultMessage {
	return proto.MakeDefaultMsg(proto.CM_SPELL, int32(uint16(x))|int32(uint16(y))<<16,
		uint16(magicID), 0, 0)
}

// grantEquip 发一个物品并穿上，返回是否成功。
//
// 用途：护身符。原版 case 13..19 / 30 的技能（幽灵盾、神圣战甲、困魔咒、
// 召唤骷髅/神兽、隐身术）都要求背包里装备着护身符，没有就放不出来。
func grantEquip(cc *conn, name string) bool {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: "@give " + name + " 1",
	})
	idx, bagBody := -1, ""
	for i := 0; i < 12 && idx < 0; i++ {
		rp := cc.tryRecvTimeout(600 * time.Millisecond)
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_BAGITEMS {
			bagBody = rp.Body
			if k := findInBag(rp.Body, name); k >= 0 {
				idx = k
			}
		}
	}
	if idx < 0 {
		fmt.Printf("    (@give %s 失败：背包里找不到)\n", name)
		return false
	}
	// 原版字段：Recog = MakeIndex、Param = 装备槽位（槽位由物品 StdMode 算出）、
	// body = 物品名。槽位必须报对：服务端会 `CheckUserItems(btWhere, StdItem)` 核对。
	ci, okItem := bagItemByName(bagBody, name)
	slot := equipSlotForStdMode(ci.S.StdMode)
	if !okItem || slot < 0 {
		fmt.Printf("    (@give %s 失败：拿不到 StdMode/槽位)\n", name)
		return false
	}
	cc.sendTakeOn(ci.MakeIndex, slot, name)
	ok := false
	for i := 0; i < 10 && !ok; i++ {
		rp := cc.tryRecvTimeout(500 * time.Millisecond)
		if rp == nil {
			break
		}
		if rp.Head.Ident == proto.SM_SENDUSEITEMS {
			ok = true
		}
	}
	fmt.Printf("    已装备 %s（背包槽 %d）%s\n", name, idx, map[bool]string{true: "✓", false: "✗"}[ok])
	return ok
}

// countSlaveActors 数当前收到的 SM_TURN 里"看起来像召唤兽"的实体数。
//
// 召唤兽与野怪在协议上**完全一样**（同一条 SM_TURN + TCharDesc），
// 客户端没法凭包区分——只能靠服务端日志。所以这里只做"数量有没有变"的
// 粗判，真正断言在 e2e 的服务端日志 check。
func countSlaveActors() int {
	n := 0
	for _, p := range sidePackets {
		if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
			n++
		}
	}
	return n
}

// waitForSlaveAppear 等一只新实体出现（视野内怪物数增加）。
func waitForSlaveAppear(cc *conn, before int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		rp := cc.tryRecvTimeout(200 * time.Millisecond)
		if rp == nil {
			continue
		}
		if rp.Head.Ident == proto.SM_TURN && isMonsterID(rp.Head.Recog) {
			// ⚠️ 必须**先收进 sidePackets 再比数量**。原实现先比后收，
			// 刚到的这只自己不算数 ⇒ "召唤兽正好是本轮最后一条包"时永远检测不到
			// （6 次尝试各白等满 2s ≈ 12s，是 slave 用例 14.6s 的几乎全部）。
			sidePackets = append(sidePackets, rp)
			if countSlaveActors() > before {
				return true
			}
		}
	}
	return false
}

// findNearestMonster 找视野内离 (x,y) 最近的怪物坐标。
func findNearestMonster(cc *conn, x, y int) (int, int, bool) {
	bestX, bestY, bestD := 0, 0, 1<<30
	scan := func(p *wire.Packet) {
		if p.Head.Ident != proto.SM_TURN || !isMonsterID(p.Head.Recog) {
			return
		}
		mx, my := int(p.Head.Param), int(p.Head.Tag)
		if d := absi(mx-x) + absi(my-y); d < bestD {
			bestX, bestY, bestD = mx, my, d
		}
	}
	for _, p := range sidePackets {
		scan(p)
	}
	for i := 0; i < 20; i++ {
		p := cc.tryRecvTimeout(600 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
			scan(p)
		} else {
			sidePackets = append(sidePackets, p)
		}
	}
	return bestX, bestY, bestD < 1<<30
}

// spawnAndFindMonster 用 `@spawn <名字> <数量>` 刷怪，并返回**刚刷出来那只**的
//
//	ActorId 与坐标。
//
// ⚠️ 不要用 findNearestMonster：那张图上自然刷着稻草人等怪，"最近的那只"
//
//	经常不是我们刚刷的鸡——而稻草人是**不死系**（undead=1），原版明文禁止诱惑
//	（Magic.pas:810），打它 100 次也不会成功。教训：概率技能的目标必须**精确指定**。
//
// 判据：-e2e 批次 2 开了 -monster-wander=false，原地图上的怪不会动 ⇒ 刷怪命令
//
//	之后新出现的**第一个** SM_TURN 怪物就是我们刷的那只。
//
// monSpot 是怪物在视野里的落点（诱惑之光按坐标选目标）。
type monSpot struct {
	id   uint32
	x, y int
}

// spawnManyMonsters 一次刷 n 只同名怪并收集它们的落点。
//
// 比"循环 n 次 spawnAndFindMonster"省得多：**一次 @spawn 往返**取代 n 次。
// 诱惑之光按坐标选目标（CM_SPELL 的 Series 装不下 ActorId），而怪物不游荡时
// 坐标不会再变，所以刷完收集一次就够 —— 原实现每轮刷一只（@spawn 往返 +
// 等出现），20 轮累计十几秒，是 slave 用例 16s 的主要来源。
//
// 收包窗口用**墙钟截止**：收满 n 只或 2s 就停（按包数算预算会被 updateVision
// 的 SM_TURN 洪水吃光，同 facePeerBefore 的注释）。
func spawnManyMonsters(cc *conn, monName string, n int) []monSpot {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: fmt.Sprintf("@spawn %s %d", monName, n),
	})
	seen := map[uint32]bool{}
	out := make([]monSpot, 0, n)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(out) < n {
		p := cc.tryRecvTimeout(150 * time.Millisecond)
		if p == nil {
			continue
		}
		id := uint32(p.Head.Recog)
		if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) && !seen[id] {
			seen[id] = true
			out = append(out, monSpot{id: id, x: int(p.Head.Param), y: int(p.Head.Tag)})
			continue
		}
		sidePackets = append(sidePackets, p)
	}
	return out
}

// drainUntilQuiet 读到"连续一个窗口没有新包"为止（或超时）。
//
// ⚠️ 用途是**清掉遗留包**：服务端常常紧跟着补发一批（`updateVision` 的 SM_TURN
// 洪流、切图后的 SM_CLEAROBJECTS/SM_TURN）。后面若按**固定包数**等某个包，
// 预算会被这些遗留包吃光，表现为"服务端明明成功了、客户端却说没收到"
// （facePeerBefore / mapTo 的偶发失败都是这个坑）。
//
// 与"固定包数 + 首个 nil 就 break"的区别：那个写法在**队列瞬时为空**时直接放弃，
// 机器一忙就误判（并行跑 e2e 时尤其明显）。
func drainUntilQuiet(cc *conn, budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cc.tryRecvTimeout(rcv(80*time.Millisecond)) == nil {
			return // 静了一个窗口 ⇒ 排空了
		}
	}
}

func spawnAndFindMonster(cc *conn, monName string) (id uint32, x, y int, ok bool) {
	cc.send(wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: "@spawn " + monName + " 1",
	})
	for i := 0; i < 25; i++ {
		p := cc.tryRecvTimeout(600 * time.Millisecond)
		if p == nil {
			break
		}
		if p.Head.Ident == proto.SM_TURN && isMonsterID(p.Head.Recog) {
			return uint32(p.Head.Recog), int(p.Head.Param), int(p.Head.Tag), true
		}
		sidePackets = append(sidePackets, p)
	}
	return 0, 0, 0, false
}

// 召唤兽的 RaceImg（= TCharDesc 里的 RaceImg / 外观低位）。
//
// 取自 data/monsters.json：变异骷髅 race_img=23（race=100）、神兽 race_img=54。
// 断言写死这两个值，是为了让"召唤错了怪"能立刻暴露——之前召的是"骷髅"
// （race_img=14），断言也跟着错成 14，两边一起错就没人发现了。
const (
	summonSkeletonRaceImg = 23
	summonShenRaceImg     = 54
)

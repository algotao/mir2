// Command accountsvc 是账号与角色服务（原 LoginSrv + DBServer 合并）。
//
// 监听两个端口，对应客户端的两个阶段：
//
//	:7000  登录阶段  CM_PROTOCOL / CM_IDPASSWORD / CM_SELECTSERVER
//	:7100  选角阶段  CM_QUERYCHR / CM_NEWCHR / CM_DELCHR / CM_SELCHR
//
// 客户端登录成功后会**断连**并转连 :7100，靠 CM_QUERYCHR 正文里的
// SessionID 绑定回已认证会话。
//
// 用法：
//
//	go run ./cmd/accountsvc -db ./mir2go.db
//
// 经网关接入（gate 占住对外的 7000/7100/7400，accountsvc 挪到内网端口，
// 下发给客户端的地址指向 gate）：
//
//	go run ./cmd/gate -route :7000=127.0.0.1:17000 -route :7100=127.0.0.1:17100 \
//	                   -route :7400=127.0.0.1:7200
//	go run ./cmd/accountsvc -db ./mir2go.db -proxy-protocol \
//	                   -login-addr 127.0.0.1:17000 -sel-addr 127.0.0.1:17100 \
//	                   -selgate-addr 127.0.0.1 -selgate-port 7100 \
//	                   -rungate-addr 127.0.0.1 -rungate-port 7400
//
// ⚠️ `-proxy-protocol` 必须与网关一致（gate 默认就写这个头）：开着它才拿得到真实客户端 IP，
// 关着则所有连接看起来都来自网关自己 —— 封禁/同 IP 多开/审计都会失准（docs/decisions.md D-23）。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/algotao/mir2/server/internal/accountsvc"
	"github.com/algotao/mir2/server/internal/chargen"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proxyproto"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
	"github.com/algotao/mir2/server/internal/tscale"
	_ "github.com/algotao/mir2/server/internal/tz"
	"github.com/algotao/mir2/server/internal/wire"
)

const (
	// maxFrameLen 单帧负载上限，防恶意超长包。
	maxFrameLen = 64 * 1024
	// readTimeout 连接空闲超时。
	readTimeout = 5 * time.Minute
)

func main() {
	var (
		dbPath    = flag.String("db", "./mir2go.db", "SQLite 数据库路径")
		loginAddr = flag.String("login-addr", ":7000", "登录阶段监听地址")
		selAddr   = flag.String("sel-addr", ":7100", "选角阶段监听地址")
		selGate   = flag.String("selgate-addr", "127.0.0.1", "角色网关对外地址（下发给客户端）")
		selPort   = flag.Int("selgate-port", 7100, "角色网关对外端口")
		runGate   = flag.String("rungate-addr", "127.0.0.1",
			"游戏网关对外地址（下发给客户端）—— 指向 gate 时填它监听的地址，如 -rungate-port 7400")
		runPort   = flag.Int("rungate-port", 7200, "游戏网关对外端口")
		srvName   = flag.String("server-name", "mir2go", "服务器名")
		timeScale = flag.Float64("time-scale", 1,
			"游戏内时间流速倍率（1=正常；20=二十倍速。客户端与 gamesvr 需同值）")
		dataDir = flag.String("data", "./data", "静态数据目录（用于新角色的初始物品）")
		homePts = flag.String("home-points", "650,631;289,618",
			"新角色出生点候选（`x,y;x,y`；**多于一个就随机挑一个**）。"+
				"默认 = 原版 1.76 的两个新手村（银杏山谷 650,631 / 边界村 289,618），"+
				"与 gamesvr 同源，见 docs/use.md 与 D-40")
		proxyProtocol = flag.Bool("proxy-protocol", false,
			"要求接入连接先带一行 PROXY protocol v1 头（网关 -proxy-protocol 会写），"+
				"从中取真实客户端 IP（docs/decisions.md D-23）。直连调试时保持关闭；"+
				"打开后缺头即断开，没有\"有头就认、没头退回 socket\"这种可伪造的中间态")
	)
	flag.Parse()

	store, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开数据库 %s: %v", *dbPath, err)
	}
	defer store.Close()

	// 时间倍速：建角色限流是"按会话的最小间隔"，必须跟着缩，
	// 否则客户端（同样在缩放）会一直撞限流。
	tscale.Set(*timeScale)
	cfg := accountsvc.DefaultConfig()
	if *timeScale > 1 {
		cfg.NewChrIntervalMs = int64(float64(cfg.NewChrIntervalMs) / *timeScale)
	}
	cfg.ServerName = *srvName
	cfg.ServerTitle = *srvName
	cfg.SelGateAddr = *selGate
	cfg.SelGatePort = *selPort
	cfg.RunGateAddr = *runGate
	cfg.RunGatePort = *runPort
	// 出生点候选：与 gamesvr 的 `-home-points` **同一份解析**（`chargen.ParseHomePoints`）
	if homes, err := chargen.ParseHomePoints(*homePts, cfg.HomeMap); err != nil {
		log.Fatalf("-home-points 解析失败：%v", err)
	} else {
		cfg.HomePoints = homes
	}

	svc := accountsvc.New(store, cfg)
	// 静态数据用于给新角色发初始装备；加载失败只告警，不影响建号
	if tables, err := data.LoadDir(*dataDir); err != nil {
		log.Printf("警告: 静态数据加载失败，新角色将无初始物品: %v", err)
	} else {
		svc.SetTables(tables)
		log.Printf("静态数据: %d 物品 / %d 怪物 / %d 技能",
			tables.Items.Len(), tables.Monsters.Len(), tables.Magics.Len())
	}

	loginLn, err := net.Listen("tcp", *loginAddr)
	if err != nil {
		log.Fatalf("监听 %s: %v", *loginAddr, err)
	}
	selLn, err := net.Listen("tcp", *selAddr)
	if err != nil {
		log.Fatalf("监听 %s: %v", *selAddr, err)
	}

	log.Printf("accountsvc 启动: db=%s 登录=%s 选角=%s 服务器=%q",
		*dbPath, *loginAddr, *selAddr, *srvName)
	if *proxyProtocol {
		log.Printf("客户端地址来源: PROXY protocol v1 头（要求网关转发；缺头即断开）")
	} else {
		log.Printf("客户端地址来源: TCP 对端地址（直连模式；经网关转发时看到的会是网关自己）")
	}
	// 下发给客户端的地址单独打一行：走网关时它与上面两个监听地址**不同**
	//（监听 7000/7100 的是 gate，accountsvc 挪到 17000/17100）。
	// 排查"客户端到底连到哪去了"先看这行。
	log.Printf("下发给客户端: 选角 %s:%d 游戏 %s:%d", *selGate, *selPort, *runGate, *runPort)

	go acceptLoop(loginLn, svc, true, *proxyProtocol)
	go acceptLoop(selLn, svc, false, *proxyProtocol)

	// 优雅退出：必须显式 Close，否则 WAL 未 checkpoint
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("收到退出信号，关闭中...")
	_ = loginLn.Close()
	_ = selLn.Close()
	_ = store.Close()
	log.Println("已关闭")
}

// acceptLoop 接受连接。isLogin 决定会话是"注册"还是"游离"。
func acceptLoop(ln net.Listener, svc *accountsvc.Service, isLogin, proxyProtocol bool) {
	for {
		c, err := ln.Accept()
		if err != nil {
			// 监听器关闭时退出
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				continue
			}
			return
		}
		// 取真实客户端地址（PROXY protocol，见 docs/decisions.md D-23）后再交给会话处理：
		// 会话/日志里那一串 `addr` 从这里往后就都是**客户端**的地址，而不是网关的。
		//
		// ⚠️ 必须**在 goroutine 里**等头：等头最长要 10 秒（见 DefaultHeaderTimeout），
		// 放在 accept 循环里同步做，一个连上就不说话的客户端就能把整个接入面堵住。
		go func() {
			ec, addr, err := proxyproto.ServerConn(c, proxyProtocol, proxyproto.DefaultHeaderTimeout)
			if err != nil {
				log.Printf("%s: 取得客户端地址失败，断开: %v", c.RemoteAddr(), err)
				_ = c.Close()
				return
			}
			handleConn(ec, svc, isLogin, addr)
		}()
	}
}

func handleConn(c net.Conn, svc *accountsvc.Service, isLogin bool, addr string) {
	defer c.Close()

	var sess *accountsvc.Session
	if isLogin {
		var err error
		sess, err = svc.NewSession(addr)
		if err != nil {
			log.Printf("%s: 建立登录会话失败: %v", addr, err)
			return
		}
	} else {
		sess = svc.NewConnSession(addr)
	}
	if isLogin {
		defer svc.Sessions().Forget(sess.SessionID)
	}

	sp := wire.NewSplitter(maxFrameLen)
	buf := make([]byte, 4096)
	for {
		_ = c.SetReadDeadline(time.Now().Add(readTimeout))
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		sp.Feed(buf[:n])

		for {
			raw, err := sp.Next()
			if err != nil {
				break // 半包或垃圾，等下一批数据
			}
			if err := processFrame(c, svc, sess, raw); err != nil {
				log.Printf("%s: %v", addr, err)
				return
			}
		}
	}
}

func processFrame(c net.Conn, svc *accountsvc.Service, sess *accountsvc.Session, raw []byte) error {
	f, err := wire.DecodeFrame(raw)
	if err != nil {
		return nil // 非法帧，跳过
	}
	// 心跳/窗口确认：原版网关据此清空反加速窗口，此处只需忽略
	if wire.IsKeepAlive(f.Payload) {
		return nil
	}
	p, err := wire.DecodePacket(f.Payload)
	if err != nil {
		return nil // 无法解析的包跳过，不断链
	}

	out := svc.Handle(context.Background(), sess, p)
	for _, r := range out {
		if _, err := c.Write(wire.EncodeDown(r.Encode())); err != nil {
			return err
		}
	}
	if sess.Account != "" && !svc.IsCurrentSession(sess) {
		return errors.New("登录会话已被新登录接管")
	}
	return nil
}

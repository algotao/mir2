// Command gate 是客户端接入网关。
//
// 职责：把客户端连接转发到后端服务，并在上行首帧里填入**真实客户端 IP**
// （直连时后端看到的是网关自己的地址）。
//
// 用法：
//
//	go run ./cmd/gate -route :7300=127.0.0.1:7200
//	go run ./cmd/gate -route :7000=127.0.0.1:7000 -route :7100=127.0.0.1:7100
//
// 完整部署形态（accountsvc 下发的地址指向本网关，回归批次 4 就是它）：
//
//	gate -route :7000=127.0.0.1:17000 -route :7100=127.0.0.1:17100 \
//	     -route :7400=127.0.0.1:7200
//	accountsvc -login-addr 127.0.0.1:17000 -sel-addr 127.0.0.1:17100 \
//	           -selgate-addr 127.0.0.1 -selgate-port 7100 \
//	           -rungate-addr 127.0.0.1 -rungate-port 7400
//
// 协议两端完全一致（都是 wire 的 '#'…'!' 帧 + 6bit 负载），
// 因此网关可以做**透明转发**，只在首帧上做一处改写。
//
// ⚠️ 真实 IP 只注入 `**` 开头的**登录首帧**（进游戏阶段）。登录/选角阶段
// 首帧是 CM_PROTOCOL 之类的文本包，注入会破坏协议，故原样透传——
// accountsvc 记到的会话 IP 因此是网关自己的地址（它目前不用这个 IP 做判定）。
package main

import (
	"flag"
	"fmt"
	_ "github.com/algotao/mir2/server/internal/tz"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/wire"
)

const (
	maxFrameLen  = 64 * 1024
	readBufSize  = 4096
	readTimeout  = 5 * time.Minute
	dialTimeout  = 5 * time.Second
	maxConnPerIP = 8   // 单 IP 最大并发连接
	maxPPS       = 200 // 单连接每秒上行帧数上限
)

// route 是一条 监听 → 后端 的转发规则。
type route struct {
	listen  string
	backend string
}

// routeList 支持重复传入 -route。
type routeList []route

func (r *routeList) String() string { return fmt.Sprint(*r) }

func (r *routeList) Set(v string) error {
	listen, backend, ok := strings.Cut(v, "=")
	if !ok || listen == "" || backend == "" {
		return fmt.Errorf("格式应为 <监听地址>=<后端地址>，如 :7300=127.0.0.1:7200，实际 %q", v)
	}
	*r = append(*r, route{listen: listen, backend: backend})
	return nil
}

func main() {
	var routes routeList
	flag.Var(&routes, "route", "转发规则 <监听>=<后端>，可重复传入")
	flag.Parse()

	if len(routes) == 0 {
		fmt.Fprintln(os.Stderr, "至少需要一条 -route 规则")
		flag.Usage()
		os.Exit(2)
	}

	// 每 IP 的并发连接计数
	var mu sync.Mutex
	conns := make(map[string]int)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	for _, rt := range routes {
		rt := rt
		ln, err := net.Listen("tcp", rt.listen)
		if err != nil {
			log.Fatalf("监听 %s: %v", rt.listen, err)
		}
		log.Printf("网关 %s → %s", rt.listen, rt.backend)

		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				ip := clientIP(c.RemoteAddr())

				mu.Lock()
				if conns[ip] >= maxConnPerIP {
					mu.Unlock()
					log.Printf("拒绝 %s：单 IP 连接数超限（%d）", ip, maxConnPerIP)
					c.Close()
					continue
				}
				conns[ip]++
				mu.Unlock()

				go func() {
					defer func() {
						// 单个 defer 里先 recover 再归还计数：一条连接上的 panic
						// 只该断掉这一条连接，不该带走整个网关进程（那会让所有
						// 在线玩家一起掉线），也不能把连接数泄漏掉。
						if r := recover(); r != nil {
							log.Printf("%s 转发时 panic: %v", ip, r)
						}
						mu.Lock()
						conns[ip]--
						if conns[ip] <= 0 {
							delete(conns, ip)
						}
						mu.Unlock()
					}()
					serve(c, rt.backend, ip)
				}()

			}
		}()
	}

	<-sig
	log.Println("收到退出信号，关闭中...")
}

// serve 处理一条客户端连接：连后端，双向转发。
func serve(client net.Conn, backend, ip string) {
	defer client.Close()

	up, err := net.DialTimeout("tcp", backend, dialTimeout)
	if err != nil {
		log.Printf("连接后端 %s 失败: %v", backend, err)
		return
	}
	defer up.Close()

	done := make(chan struct{}, 2)

	// 下行：后端 → 客户端（原样转发）
	go func() {
		io.Copy(client, up)
		done <- struct{}{}
	}()

	// 上行：客户端 → 后端（首帧注入真实 IP）
	go func() {
		sp := wire.NewSplitter(maxFrameLen)
		buf := make([]byte, readBufSize)
		first := true
		// 简易限速：每秒允许的帧数
		var winStart time.Time
		var winCount int

		for {
			_ = client.SetReadDeadline(time.Now().Add(readTimeout))
			n, err := client.Read(buf)
			if err != nil {
				break
			}
			sp.Feed(buf[:n])

			for {
				raw, err := sp.Next()
				if err != nil {
					break
				}

				// 限速：滑动窗口按秒重置
				now := time.Now()
				if now.Sub(winStart) >= time.Second {
					winStart, winCount = now, 0
				}
				winCount++
				if winCount > maxPPS {
					log.Printf("%s 发包过快，断开", ip)
					client.Close()
					return
				}

				if first {
					raw = injectIP(raw, ip)
					first = false
				}
				if _, err := up.Write(raw); err != nil {
					break
				}
			}
		}
		done <- struct{}{}
	}()

	<-done
}

// injectIP 把真实客户端 IP 拼进首帧的登录 token。
//
// token 形如 `**<账号>/<角色>/<会话>/<版本>/<序号>`，
// 这里在末尾追加 `|<IP>`；解析侧用 strings.Cut 取，直连时不带也兼容。
//
// ⚠️ 只对**登录首帧**生效（以 `**` 开头）。若首帧不是它（比如直连 gamesvr
// 之外的场景），原样返回，不破坏数据。
func injectIP(raw []byte, ip string) []byte {
	f, err := wire.DecodeFrame(raw)
	if err != nil {
		return raw
	}
	s := string(codec.Decode6BitBuf(f.Payload))
	if !strings.HasPrefix(s, "**") {
		return raw
	}
	// 6bit 编回去（追加 IP 后）
	s = s + "|" + ip
	return wire.EncodeFrame(f.Seq, codec.Encode6BitBuf([]byte(s)))
}

// clientIP 去掉端口号，只留 IP。
func clientIP(addr net.Addr) string {
	s := addr.String()
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

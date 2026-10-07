// Command gate 是客户端接入网关（D-17：**纯字节转发层**）。
//
// 职责只有四件：**TCP 接入 / 按帧转发 / 限流 / 连接元数据**。
// 它**不做协议感知** —— 只认 `[u32 小端长度][bytes]` 这一层分帧
// （[docs/protocol.md §2]），不认识 Envelope、不解析消息体、不随 schema 演进。
//
// 用法：
//
//	go run ./cmd/gate -route :7300=127.0.0.1:7200
//	go run ./cmd/gate -route :7000=127.0.0.1:7000 -route :7100=127.0.0.1:7100
//
// ⚠️ **真实客户端 IP 目前只落在网关自己的日志里，没有传给后端。**
// 旧协议是在登录首帧的文本 token 里拼 `|<IP>`，新协议里没有这种可以"顺手拼一段"
// 的字段（这正是自研协议的好处：不再有格式与字符集耦合的定长串）。要把它交给后端，
// 得在下面几条里选一条，属**待决**（见 docs/decisions.md）：
//
//  1. PROXY protocol（HAProxy 事实标准）：网关在首帧前写一行
//     `PROXY TCP4 <src> <dst> <sport> <dport>\r\n`，后端剥掉。**协议无关**，推荐；
//  2. 后端向网关发起内部查询（网关维护"连接元数据"表，按 conn id 查）；
//  3. 把 client_ip 放进 ClientHello —— 但这等于**相信客户端自报**，只在网关
//     不可信时才勉强可接受，且会让 schema 多一个"只有网关填"的字段。
//
// [docs/protocol.md §2]: ../../../docs/protocol.md
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/algotao/mir2/server/internal/tz"

	"github.com/algotao/mir2/server/internal/frame"
)

const (
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

	// 下行：后端 → 客户端。**纯 `io.Copy`** —— 连长度域都不需要看，
	// 后端写什么就原样给客户端什么。
	go func() {
		_, _ = io.Copy(client, up)
		done <- struct{}{}
	}()

	// 上行：客户端 → 后端，逐帧转发 + 限速
	go func() {
		forwardUp(client, up, ip)
		done <- struct{}{}
	}()

	<-done
}

// forwardUp 逐帧把上行转发给后端，顺带做单连接速率限制。
//
// 只用到 [`frame.ReadRaw`] / [`frame.WriteRaw`] —— 即"长度域"这一层。
// 帧长超限（> 64 KiB）与空帧都属于协议错误：**断开**，不静默跳过，
// 否则对端可以一直喂垃圾把网关当免费的中继使。
func forwardUp(client, up net.Conn, ip string) {
	br := bufio.NewReaderSize(client, readBufSize)
	var winStart time.Time
	var winCount int

	for {
		_ = client.SetReadDeadline(time.Now().Add(readTimeout))
		raw, err := frame.ReadRaw(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClosed(err) {
				log.Printf("%s 上行帧错误: %v", ip, err)
			}
			return
		}

		// 限速：滑动窗口按秒重置
		now := time.Now()
		if now.Sub(winStart) >= time.Second {
			winStart, winCount = now, 0
		}
		winCount++
		if winCount > maxPPS {
			log.Printf("%s 发包过快（>%d 帧/秒），断开", ip, maxPPS)
			return
		}

		if err := frame.WriteRaw(up, raw); err != nil {
			return
		}
	}
}

// isClosed 判断"对端已关闭"这类正常收尾错误（不值得打日志）。
func isClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded)
}

// clientIP 去掉端口号，只留 IP。
func clientIP(addr net.Addr) string {
	s := addr.String()
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

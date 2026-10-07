package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/frame"
	"github.com/algotao/mir2/server/internal/proxyproto"
)

// 网关与后端**两端对得上**：这是 D-23 的核心契约 ——
// 网关写的那一行 PROXY 头，正是后端剥掉之后能拿到真实客户端地址的那一行。
//
// ⚠️ 用真 TCP（而不是 net.Pipe）：写头要用 client.RemoteAddr()/LocalAddr()，
// 它们必须是 *net.TCPAddr；net.Pipe 给的是个假地址，只会写成 `PROXY UNKNOWN`，
// 那样这条测试就什么都没验到。
func TestServeWritesProxyHeader(t *testing.T) {
	beLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("假后端监听: %v", err)
	}
	defer beLn.Close()

	type received struct {
		addr string
		raw  []byte
		err  error
	}
	got := make(chan received, 1)
	go func() {
		c, err := beLn.Accept()
		if err != nil {
			got <- received{err: err}
			return
		}
		defer c.Close()
		// 后端以"必须带头"的模式接入 —— 与 gamesvr/accountsvc 的 -proxy-protocol 一致。
		ec, addr, err := proxyproto.ServerConn(c, true, 2*time.Second)
		if err != nil {
			got <- received{err: err}
			return
		}
		raw, err := frame.ReadRaw(ec)
		got <- received{addr: addr, raw: raw, err: err}
	}()

	// 真 TCP 客户端：Accept 出来的连接才有真的 RemoteAddr。
	clLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("客户端监听: %v", err)
	}
	defer clLn.Close()
	peer, err := net.Dial("tcp", clLn.Addr().String())
	if err != nil {
		t.Fatalf("客户端连接: %v", err)
	}
	defer peer.Close()
	client, err := clLn.Accept()
	if err != nil {
		t.Fatalf("客户端 Accept: %v", err)
	}

	go serve(client, beLn.Addr().String(), "127.0.0.1", true)

	// 头之后的**协议数据**必须一字节不差地穿过去（写头不能顺手改协议）。
	want := []byte{0x08, 0x01, 0xDE, 0xAD}
	if err := frame.WriteRaw(peer, want); err != nil {
		t.Fatalf("客户端发帧: %v", err)
	}

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("后端剥离 PROXY 头/收帧: %v", r.err)
		}
		// 地址应是客户端**这条 TCP 连接**的真实地址，而不是网关到后端那条。
		wantAddr := net.JoinHostPort("127.0.0.1",
			strconv.Itoa(peer.LocalAddr().(*net.TCPAddr).Port))
		if r.addr != wantAddr {
			t.Errorf("后端看到的客户端地址 = %q，应为 %q", r.addr, wantAddr)
		}
		if !bytes.Equal(r.raw, want) {
			t.Errorf("头之后的帧 = %x，应为 %x", r.raw, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("后端没等到连接与 PROXY 头")
	}
}

// forwardUp 只依赖 frame 那一层，于是用 net.Pipe 就能测，不必起真监听。
// 这条测试守的是 D-17 的核心契约：**网关逐帧原样转发，不改一个字节**。
func TestForwardUpRelaysFramesVerbatim(t *testing.T) {
	client, clientPeer := net.Pipe()
	up, upPeer := net.Pipe()
	defer client.Close()
	defer clientPeer.Close()
	defer up.Close()
	defer upPeer.Close()

	go forwardUp(client, up, "1.2.3.4")

	sent := [][]byte{
		{0x08, 0x01},             // 像个真信封的开头
		{0xDE, 0xAD, 0xBE, 0xEF}, // 任意字节也必须原样过
		bytes.Repeat([]byte{7}, 1000),
	}
	go func() {
		for _, b := range sent {
			_ = frame.WriteRaw(clientPeer, b)
		}
	}()

	br := bufio.NewReader(upPeer)
	for i, want := range sent {
		_ = upPeer.SetReadDeadline(time.Now().Add(2 * time.Second))
		got, err := frame.ReadRaw(br)
		if err != nil {
			t.Fatalf("第 %d 帧读取失败: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("第 %d 帧被改动了\n  want %x\n  got  %x", i, want, got)
		}
	}
}

// 协议错误（这里：声明长度 0）必须让转发**收手**，而不是继续当中继。
func TestForwardUpStopsOnEmptyFrame(t *testing.T) {
	client, clientPeer := net.Pipe()
	up, upPeer := net.Pipe()
	defer client.Close()
	defer clientPeer.Close()
	defer up.Close()
	defer upPeer.Close()

	stopped := make(chan struct{})
	go func() {
		forwardUp(client, up, "1.2.3.4")
		close(stopped)
	}()

	if _, err := clientPeer.Write([]byte{0, 0, 0, 0}); err != nil {
		t.Fatalf("写长度 0 的帧头: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("空帧之后 forwardUp 应当返回")
	}

	// 一帧都不该被转发出去
	_ = upPeer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := upPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("空帧不应产生任何下行流量")
	}
}

// 长度域超限同样要收手：不能拿伪造的长度去申请 4 GiB，也不能继续转发。
func TestForwardUpStopsOnOversizeFrame(t *testing.T) {
	client, clientPeer := net.Pipe()
	up, upPeer := net.Pipe()
	defer client.Close()
	defer clientPeer.Close()
	defer up.Close()
	defer upPeer.Close()

	stopped := make(chan struct{})
	go func() {
		forwardUp(client, up, "1.2.3.4")
		close(stopped)
	}()

	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], 0xFFFF_FFFF)
	if _, err := clientPeer.Write(hdr[:]); err != nil && err != io.ErrClosedPipe {
		t.Fatalf("写超限帧头: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("超限帧之后 forwardUp 应当返回")
	}
}

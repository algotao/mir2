package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/frame"
)

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

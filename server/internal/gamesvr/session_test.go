package gamesvr

import (
	"io"
	"net"
	"testing"
	"time"
)

// D-23 的核心约束：打开 `-proxy-protocol` 之后，**缺头必须断开**。
//
// 不能"没头就用 socket 地址兜底" —— 那等于给"能直连到 gamesvr 的人"留了个口子：
// 他写不写头都行，而且写了就能自称任意 IP（`PROXY TCP4 1.2.3.4 …`），
// 于是"必须带头"这句话就是空的，封禁 / 同 IP 多开限制全部失准。
func TestAcceptConnRejectsMissingProxyHeader(t *testing.T) {
	srv := &Server{cfg: configState{proxyProtocol: true}}
	client, server := net.Pipe()
	defer client.Close()

	go srv.acceptConn(server) // 出错时它会关掉自己那一端

	// 一条**没有 PROXY 头**的连接（字节随便，反正不是 `PROXY ` 开头）
	if _, err := io.WriteString(client, "GET / HTTP/1.1\r\n"); err != nil {
		t.Fatalf("写客户端数据: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("缺头时服务端必须断开连接（探针到这里应该已经 EOF）")
	}
}

// 头非法（格式错 / 超长 / v2）同样必须断开，而不是"看不懂就当没看见"。
func TestAcceptConnRejectsMalformedProxyHeader(t *testing.T) {
	for _, in := range []string{
		"PROXY SCTP4 1.2.3.4 5.6.7.8 1 2\r\n",    // 未知协议族
		"PROXY TCP4 1.2.3.4 5.6.7.8\r\n",         // 少两段
		"PROXY TCP4 1.2.3.4 5.6.7.8 1 99999\r\n", // 端口越界
		"\r\n\r\n\x00\r\nQUIT\n\x21\x11\x00\x0c", // v2 二进制头
	} {
		srv := &Server{cfg: configState{proxyProtocol: true}}
		client, server := net.Pipe()
		go srv.acceptConn(server)
		if _, err := io.WriteString(client, in); err != nil {
			client.Close()
			t.Fatalf("写 %q: %v", in, err)
		}
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err := client.Read(make([]byte, 1))
		client.Close()
		if err == nil {
			t.Errorf("%q：非法头必须断开连接", in)
		}
	}
}

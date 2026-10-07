package gamesvr

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

// TestGameTimePhase 钉住相位分档 —— 逐字对照 `FrnEngn.pas:87-90 GetGameTime`：
//
//	4,15                ⇒ 0 SUNRAISE
//	5..10, 16..22       ⇒ 1 DAY
//	11,23               ⇒ 2 SUNSET
//	0..3, 12,13,14      ⇒ 3 NIGHT
func TestGameTimePhase(t *testing.T) {
	want := map[int]int{
		4: gameTimeSunraise, 15: gameTimeSunraise,
		11: gameTimeSunset, 23: gameTimeSunset,
		5: gameTimeDay, 6: gameTimeDay, 7: gameTimeDay, 8: gameTimeDay,
		9: gameTimeDay, 10: gameTimeDay, 16: gameTimeDay, 17: gameTimeDay,
		18: gameTimeDay, 19: gameTimeDay, 20: gameTimeDay, 21: gameTimeDay,
		22: gameTimeDay,
		0:  gameTimeNight, 1: gameTimeNight, 2: gameTimeNight, 3: gameTimeNight,
		12: gameTimeNight, 13: gameTimeNight, 14: gameTimeNight,
	}
	if len(want) != 24 {
		t.Fatalf("24 小时都要有归属，表里只有 %d 个", len(want))
	}
	for h := 0; h < 24; h++ {
		at := time.Date(2026, 10, 6, h, 30, 0, 0, time.Local)
		if got := gameTimePhase(at); got != want[h] {
			t.Errorf("%d 点 ⇒ 相位 %d，期望 %d", h, got, want[h])
		}
	}
	// 相位名（脚本 `DAYTIME` 的参数）
	names := map[int]string{
		gameTimeSunraise: "SUNRAISE", gameTimeDay: "DAY",
		gameTimeSunset: "SUNSET", gameTimeNight: "NIGHT",
	}
	for phase, name := range names {
		if got := gameTimeName(phase); got != name {
			t.Errorf("相位 %d 的名字 = %q，期望 %q", phase, got, name)
		}
	}
}

// TestDayBrightMapping 钉住 `TPlayObject.DayBright`（ObjBase.pas:4283-4294）：
// DARK ⇒ 1；DAYLIGHT ⇒ 0 且**压过** DARK 与相位；其余按相位。
func TestDayBrightMapping(t *testing.T) {
	phases := []int{gameTimeSunraise, gameTimeDay, gameTimeSunset, gameTimeNight}
	cases := []struct {
		name  string
		mi    *data.MapInfo
		phase int
		want  uint16
	}{
		{"普通图·白天", &data.MapInfo{}, gameTimeDay, 0},
		{"普通图·夜晚", &data.MapInfo{}, gameTimeNight, 1},
		{"普通图·日出", &data.MapInfo{}, gameTimeSunraise, 2},
		{"普通图·日落", &data.MapInfo{}, gameTimeSunset, 2},
		{"DARK 图·白天", &data.MapInfo{Darkness: true}, gameTimeDay, 1},
		{"DARK 图·日出", &data.MapInfo{Darkness: true}, gameTimeSunraise, 1},
		{"DAY 图·夜晚", &data.MapInfo{DayLight: true}, gameTimeNight, 0},
		// ⚠️ 原版最后那句 `if boDAYLIGHT then Result := 0` **压过** DARK
		{"DARK+DAY 图·夜晚", &data.MapInfo{Darkness: true, DayLight: true}, gameTimeNight, 0},
		{"没配 mapinfo", nil, gameTimeNight, 1},
	}
	for _, c := range cases {
		if got := dayBright(c.mi, c.phase); got != c.want {
			t.Errorf("%s ⇒ %d，期望 %d", c.name, got, c.want)
		}
	}
	// 四个相位都要覆盖到（避免表里漏一个相位还"全绿"）
	seen := map[int]bool{}
	for _, c := range cases {
		seen[c.phase] = true
	}
	for _, p := range phases {
		if !seen[p] {
			t.Errorf("相位 %d 没被覆盖", p)
		}
	}
}

// TestDayChangingPacket 相位变了要发一条 SM_DAYCHANGING（Param=相位、Tag=亮度），
// 没变则**一条都不发**（原版 `Run` 里那句 `if m_btBright <> g_nGameTime then …`）。
//
// 这条抓的是**真报文**（写出什么字节就解什么）—— 光断言内部字段证明不了发出去什么。
func TestDayChangingPacket(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	s.data.mapInfoByID["0"] = &data.MapInfo{ID: "0"} // 普通图：亮度只由相位决定
	conn := &recordingConn{}
	p.conn = conn

	now := time.Now()
	// 登录包序列未发完前，周期 tick 不得抢发 SM_DAYCHANGING。
	p.brightInit, p.brightPhase = true, gamePhaseOther(now)
	s.tickDayChanging(p, now)
	if got := len(conn.packets(t)); got != 0 {
		t.Fatalf("进入世界完成前不应发相位包，got %d", got)
	}

	// 登录包序列完成后，相位变化才允许发 SM_DAYCHANGING。
	p.dayChangingReady.Store(true)
	s.tickDayChanging(p, now)

	pkts := conn.packets(t)
	if len(pkts) != 1 {
		t.Fatalf("相位变了该正好发 1 条，实际 %d 条", len(pkts))
	}
	if pkts[0].Head.Ident != proto.SM_DAYCHANGING {
		t.Fatalf("消息号 = %d，期望 SM_DAYCHANGING(%d)", pkts[0].Head.Ident, proto.SM_DAYCHANGING)
	}
	wantPhase := uint16(gameTimePhase(now))
	if pkts[0].Head.Param != wantPhase {
		t.Errorf("Param(相位) = %d，期望 %d", pkts[0].Head.Param, wantPhase)
	}
	if want := dayBright(s.data.mapInfoByID["0"], gameTimePhase(now)); pkts[0].Head.Tag != want {
		t.Errorf("Tag(亮度) = %d，期望 %d", pkts[0].Head.Tag, want)
	}

	// 相位没变 ⇒ 不该再发
	s.tickDayChanging(p, now)
	if got := len(conn.packets(t)); got != 1 {
		t.Errorf("相位没变却又发了：共 %d 条", got)
	}
}

// gamePhaseOther 返回一个与当前相位不同的相位（造"变了"的情形）。
func gamePhaseOther(now time.Time) int {
	if gameTimePhase(now) == gameTimeSunraise {
		return gameTimeNight
	}
	return gameTimeSunraise
}

// recordingConn 是"只把写出的字节记下来"的假连接。
//
// ⚠️ **别用 `net.Pipe()` 干这个**：它是无缓冲 + 同步的，`Write` 会一直阻塞到有人
// `Read` 为止。而服务端的 `send` 是在当前 goroutine 里写、测试又要等 tick 返回后
// 才去读 ⇒ 直接**死锁**（`go test` 默认要 10 分钟才超时，2026-10-06 就是这么把
// 一次回归跑挂住的）。这里没有任何同步等待，天生不会挂。
type recordingConn struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *recordingConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(b)
}

func (c *recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Close() error                     { return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return recordingAddr{} }
func (c *recordingConn) RemoteAddr() net.Addr             { return recordingAddr{} }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

// reset 清空已记录的字节（用于"只看接下来发的包"）。
func (c *recordingConn) reset() {
	c.mu.Lock()
	c.buf.Reset()
	c.mu.Unlock()
}

// packets 把记录下来的字节解成下行包（走与 `handleConn` 同一条拆帧/解码路径）。
func (c *recordingConn) packets(t *testing.T) []wire.Packet {
	t.Helper()
	c.mu.Lock()
	raw := append([]byte(nil), c.buf.Bytes()...)
	c.mu.Unlock()

	sp := wire.NewSplitter(maxFrameLen)
	sp.Feed(raw)
	var out []wire.Packet
	for {
		fr, err := sp.Next()
		if err != nil {
			break // 没有完整帧了（可能还有半帧）
		}
		f, err := wire.DecodeFrame(fr)
		if err != nil {
			t.Fatalf("解帧失败: %v", err)
		}
		pkt, err := wire.DecodePacket(f.Payload)
		if err != nil {
			t.Fatalf("解包失败: %v", err)
		}
		out = append(out, pkt)
	}
	return out
}

type recordingAddr struct{}

func (recordingAddr) Network() string { return "test" }
func (recordingAddr) String() string  { return "test" }

package gamesvr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/frame"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
	"github.com/algotao/mir2/server/internal/world"
	"github.com/algotao/mir2/server/protocol"
)

// 契约测试骨架（docs/protocol.md §9.2）。
//
// 起一个**真 TCP** 上的新协议入口，用一个最小客户端把 §5 的序列走一遍，
// 并**断言收到的消息序列**（类型 + 顺序 + 关键字段），以及各种错误路径的行为。
//
// ⚠️ 驱动端现在是 Go，不是 `client/e2e` —— 那是 §11 待办里的下一项（要先有
// `client/protocol` + `client/net`）。本文件的价值不在于"谁来驱动"，而在于**先把契约钉死**：
// 消息类型、顺序、握手/版本/顺序错误时的行为。Rust 剧本接上来之后，
// 这里应当变成同一份序列的第二个实现（两边都必须过）—— 那才叫"防双实现漂移"。

// ---------- 装配 ----------

// protoContractServer 起一个新协议入口（内存地图 + 临时 SQLite）。
func protoContractServer(t *testing.T) (*Server, storage.Store, string) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "proto.db"))
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := world.Generate("0", 60, 60, false)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	mm.SetNames(map[string]string{"0": "0"})

	s := testSlaveServer() // players / monsters / monsterIdx / social.groups
	s.store = store
	s.world.maps = mm
	s.world.defaultMap = m
	s.world.index = world.NewSpatialIndex(32)
	s.world.ground = map[uint32]*GroundItem{}
	s.world.groundEvents = map[groundKey]*groundEvent{}
	s.world.walls = map[wallKey]*Wall{}
	s.cfg.viewRange = 12

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveProtoConn(c, "127.0.0.1:1")
		}
	}()
	return s, store, ln.Addr().String()
}

// seedAccount 造一个账号 + 一个角色 + 一条**未选角**的会话，返回会话号与角色 id。
func seedAccount(t *testing.T, store storage.Store) (sessionID int32, charID uint64) {
	t.Helper()
	ctx := context.Background()
	hash, salt, err := storage.HashPassword("pw")
	if err != nil {
		t.Fatalf("哈希口令: %v", err)
	}
	if err := store.Accounts().Create(ctx, &storage.Account{
		Name: "tester", PasswordHash: hash, Salt: salt, Data: &pb.AccountData{},
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	chr := &storage.Character{
		Account: "tester", Name: "勇士",
		Data: &pb.CharacterData{
			Account: "tester", ChrName: "勇士",
			CurMap: "0", CurX: 10, CurY: 10, Dir: uint32(entity.DirRight), Hair: 3,
			Abil: &pb.Ability{Level: 7, Hp: 30, MaxHp: 40, Mp: 5, MaxMp: 9},
		},
	}
	if err := store.Characters().Create(ctx, chr); err != nil {
		t.Fatalf("建角色: %v", err)
	}
	// Stage 2 = 已登录、还没选角（accountsvc 的分档）⇒ 认领会话后应回落到选角。
	rec := &storage.SessionRecord{
		SessionID: 7, Account: "tester", Stage: 2, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.Sessions().Create(ctx, rec); err != nil {
		t.Fatalf("建会话: %v", err)
	}
	return rec.SessionID, uint64(chr.ID)
}

// ---------- 最小客户端 ----------

type protoClient struct {
	t   *testing.T
	c   net.Conn
	rd  *bufio.Reader
	seq uint32
}

func dialProto(t *testing.T, addr string) *protoClient {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("连服务端: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &protoClient{t: t, c: c, rd: bufio.NewReader(c)}
}

func (cl *protoClient) send(env *protocol.Envelope) {
	cl.t.Helper()
	cl.seq++
	env.Seq = cl.seq
	_ = cl.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := frame.Write(cl.c, env); err != nil {
		cl.t.Fatalf("发帧: %v", err)
	}
}

// recv 收一条信封；收不到就 Fatal（契约测试里"该来的没来"就是失败）。
func (cl *protoClient) recv() *protocol.Envelope {
	cl.t.Helper()
	_ = cl.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	env, err := frame.Read(cl.rd)
	if err != nil {
		cl.t.Fatalf("收帧: %v", err)
	}
	return env
}

// expectClosed 断言服务端**已经断开**（读到 EOF），而不是继续等。
func (cl *protoClient) expectClosed() {
	cl.t.Helper()
	_ = cl.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	for {
		_, err := cl.rd.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			// 连接被重置也是"断开"，但超时不是。
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				cl.t.Fatal("服务端没有断开连接（超时）")
			}
			return
		}
	}
}

func (cl *protoClient) hello() {
	cl.t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
		ProtocolVersion: protocol.Version, ClientBuild: "contract-test", Locale: "zh-CN"}}})
	sh, ok := cl.recv().Body.(*protocol.Envelope_ServerHello)
	if !ok {
		cl.t.Fatalf("握手应答类型不对: %T", cl.recv().Body)
	}
	if sh.ServerHello.GetProtocolVersion() != protocol.Version {
		cl.t.Errorf("握手版本 = %d，应为 %d", sh.ServerHello.GetProtocolVersion(), protocol.Version)
	}
	if len(sh.ServerHello.GetSessionKey()) == 0 {
		cl.t.Error("ServerHello 应带 session_key（口令挑战应答的位置）")
	}
	if !hasCap(sh.ServerHello.GetCapabilities(), "enter-world") {
		cl.t.Errorf("capabilities = %v，应含 enter-world", sh.ServerHello.GetCapabilities())
	}
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// sessionTokenV0 是 v0 的临时编码：4 字节小端会话号（见 netproto.go 的说明）。
func sessionTokenV0(id int32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(id))
	return b
}

// ---------- 主流程 ----------

// TestProtoContractEnterWorld 是 §9.2 的主契约：**完整走一遍 §5 的序列**。
func TestProtoContractEnterWorld(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	// 视野里放一只怪：进图快照里"看得见的实体"必须包含它。
	mon := newTestMonster(1_000_001, "鸡", 15)
	mon.Object.SetPlace(s.world.defaultMap, 12, 11, entity.DirDown)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	cl := dialProto(t, addr)
	cl.hello()

	// ① 认领会话（v0 的入口）。会话还没选角 ⇒ 应回落到选角阶段。
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Reconnect{
		Reconnect: &protocol.Reconnect{SessionToken: sessionTokenV0(sessionID)}}})
	rr, ok := cl.recv().Body.(*protocol.Envelope_ReconnectResult)
	if !ok {
		t.Fatalf("② 应为 ReconnectResult，实得 %T", rr)
	}
	if rr.ReconnectResult.GetStatus() != protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT {
		t.Fatalf("未选角的会话应回落到选角，实得 %v", rr.ReconnectResult.GetStatus())
	}

	// ② 列角色
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ListCharacters{ListCharacters: &protocol.ListCharacters{}}})
	list, ok := cl.recv().Body.(*protocol.Envelope_CharacterList)
	if !ok {
		t.Fatalf("③ 应为 CharacterList，实得 %T", list)
	}
	if n := len(list.CharacterList.GetCharacters()); n != 1 {
		t.Fatalf("角色数 = %d，应为 1", n)
	}
	sum := list.CharacterList.GetCharacters()[0]
	if sum.GetName() != "勇士" || sum.GetLevel() != 7 ||
		sum.GetClass() != protocol.CharClass_CHAR_CLASS_WARRIOR {
		t.Errorf("角色摘要 = %+v", sum)
	}
	if sum.GetCharacterId() != charID {
		t.Errorf("角色 id = %d，应为 %d（客户端下一步就靠它选角）", sum.GetCharacterId(), charID)
	}

	// ③ 选角 ⇒ 服务端在**这一步**申请角色租约（§5）
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacter{
		SelectCharacter: &protocol.SelectCharacter{CharacterId: charID}}})
	sel, ok := cl.recv().Body.(*protocol.Envelope_SelectCharacterResult)
	if !ok {
		t.Fatalf("④ 应为 SelectCharacterResult，实得 %T", sel)
	}
	if sel.SelectCharacterResult.GetCode() != protocol.SelectCharCode_SELECT_CHAR_OK {
		t.Fatalf("选角失败: %v（%s）", sel.SelectCharacterResult.GetCode(), sel.SelectCharacterResult.GetMessage())
	}

	// ④ 进图快照。**顺序是契约**：EnterWorld 必须在 AbilityUpdate 之前
	//（客户端在 EnterWorld 里才创建"自己"，而能力值要挂到那个自己身上）。
	ewEnv := cl.recv()
	ew, ok := ewEnv.Body.(*protocol.Envelope_EnterWorld)
	if !ok {
		t.Fatalf("⑤ 应为 EnterWorld，实得 %T", ewEnv.Body)
	}
	enter := ew.EnterWorld
	if enter.GetSelfEntityId() == 0 {
		t.Error("EnterWorld 必须带 self_entity_id")
	}
	if enter.GetMapName() != "0" {
		t.Errorf("地图 = %q，应为 %q", enter.GetMapName(), "0")
	}
	if p := enter.GetPosition(); p.GetX() != 10 || p.GetY() != 10 {
		t.Errorf("坐标 = (%d,%d)，应为存档里的 (10,10)", p.GetX(), p.GetY())
	}
	// 存档里 Dir=右（原版 2）⇒ 新协议的 DIR_RIGHT(=3)。这是「新枚举 = 原版 + 1」的活证据。
	if d := enter.GetDirection(); d != protocol.Direction_DIR_RIGHT {
		t.Errorf("朝向 = %v，应为 DIR_RIGHT（存档 Dir=2 = 原版的「右」）", d)
	}
	// 视野内那只怪必须在初始快照里（否则客户端进图看不见旁边的怪）
	var found *protocol.EntityState
	for _, e := range enter.GetEntities() {
		if e.GetEntityId() == uint64(mon.ID) {
			found = e
		}
		if e.GetEntityId() == enter.GetSelfEntityId() {
			t.Error("初始快照不该包含自己（self_entity_id 已经单列）")
		}
	}
	if found == nil {
		t.Fatalf("初始快照里没有视野内的怪（entities=%d 条）", len(enter.GetEntities()))
	}
	if found.GetKind() != 1 || found.GetName() != "鸡" || found.GetHp() != 15 || found.GetMaxHp() != 15 {
		t.Errorf("怪的快照 = %+v", found)
	}

	abEnv := cl.recv()
	ab, ok := abEnv.Body.(*protocol.Envelope_AbilityUpdate)
	if !ok {
		t.Fatalf("⑥ 应为 AbilityUpdate，实得 %T", abEnv.Body)
	}
	if got := ab.AbilityUpdate.GetAbility(); got.GetLevel() != 7 || got.GetHp() != 30 || got.GetMaxHp() != 40 {
		t.Errorf("能力值 = %+v", got)
	}

	// ⑤ 心跳
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 12345}}})
	pong, ok := cl.recv().Body.(*protocol.Envelope_Pong)
	if !ok {
		t.Fatalf("⑦ 应为 Pong，实得 %T", pong)
	}
	if pong.Pong.GetClientTimeMs() != 12345 {
		t.Errorf("Pong 应回显 client_time_ms，实得 %d", pong.Pong.GetClientTimeMs())
	}

	// ⑥ 未知/未实现的消息：记数 + 忽略，**禁止 panic、禁止断开**（§4.1 硬规则 4）。
	cl.send(&protocol.Envelope{}) // 连 oneof case 都没有
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Raw{Raw: &protocol.Raw{MsgId: 0x0F01}}})
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 999}}})
	pong2, ok := cl.recv().Body.(*protocol.Envelope_Pong)
	if !ok {
		t.Fatalf("未知消息之后连接应当还活着，实得 %T", pong2)
	}
	if pong2.Pong.GetClientTimeMs() != 999 {
		t.Errorf("Pong 回显 = %d", pong2.Pong.GetClientTimeMs())
	}
}

// ---------- 错误路径 ----------

// 版本不匹配 ⇒ ServerError + **立即断开**（§5：禁止"尽力而为"）。
func TestProtoHandshakeRejectsVersionMismatch(t *testing.T) {
	_, _, addr := protoContractServer(t)
	cl := dialProto(t, addr)
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
		ProtocolVersion: protocol.Version + 1, ClientBuild: "too-new"}}})

	se, ok := cl.recv().Body.(*protocol.Envelope_ServerError)
	if !ok {
		t.Fatalf("应回 ServerError，实得 %T", se)
	}
	if se.ServerError.GetCode() != protoErrHandshake {
		t.Errorf("错误码 = %d", se.ServerError.GetCode())
	}
	if se.ServerError.GetMessage() == "" {
		t.Error("ServerError 应说明原因（否则客户端只能猜）")
	}
	cl.expectClosed()
}

// 首包不是 ClientHello ⇒ 同样拒绝（否则后续所有解析都建立在错误假设上）。
func TestProtoHandshakeRejectsNonHello(t *testing.T) {
	_, _, addr := protoContractServer(t)
	cl := dialProto(t, addr)
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 1}}})
	if _, ok := cl.recv().Body.(*protocol.Envelope_ServerError); !ok {
		t.Fatal("首包不是 ClientHello 时应回 ServerError")
	}
	cl.expectClosed()
}

// 消息顺序不对（还没认领会话就要选角）⇒ ServerError，但**连接保留**
// （客户端还能补上正确的顺序 —— 这与"协议错误"不是一回事）。
func TestProtoOutOfOrderKeepsConnection(t *testing.T) {
	_, _, addr := protoContractServer(t)
	cl := dialProto(t, addr)
	cl.hello()

	cl.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacter{
		SelectCharacter: &protocol.SelectCharacter{CharacterId: 1}}})
	se, ok := cl.recv().Body.(*protocol.Envelope_ServerError)
	if !ok {
		t.Fatalf("应回 ServerError，实得 %T", se)
	}
	if se.ServerError.GetCode() != protoErrOutOfOrder {
		t.Errorf("错误码 = %d，应为 %d（顺序不对）", se.ServerError.GetCode(), protoErrOutOfOrder)
	}

	// 连接还在：补上心跳照样有回音。
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 7}}})
	if _, ok := cl.recv().Body.(*protocol.Envelope_Pong); !ok {
		t.Fatal("顺序错误之后连接应当保留")
	}
}

// 认领会话失败（token 无效）⇒ FAILED + Disconnect + 断开。
func TestProtoReconnectRejectsBadToken(t *testing.T) {
	_, _, addr := protoContractServer(t)
	for _, tok := range [][]byte{
		{1, 2, 3},            // 长度不对
		sessionTokenV0(4242), // 会话不存在
	} {
		cl := dialProto(t, addr)
		cl.hello()
		cl.send(&protocol.Envelope{Body: &protocol.Envelope_Reconnect{
			Reconnect: &protocol.Reconnect{SessionToken: tok}}})
		rr, ok := cl.recv().Body.(*protocol.Envelope_ReconnectResult)
		if !ok {
			t.Fatalf("token=%v 应回 ReconnectResult，实得 %T", tok, rr)
		}
		if rr.ReconnectResult.GetStatus() != protocol.ReconnectStatus_RECONNECT_FAILED {
			t.Errorf("token=%v 应 FAILED，实得 %v", tok, rr.ReconnectResult.GetStatus())
		}
		if _, ok := cl.recv().Body.(*protocol.Envelope_Disconnect); !ok {
			t.Errorf("token=%v 失败后应发 Disconnect 说明原因", tok)
		}
		cl.expectClosed()
	}
}

// 同账号第二次登录（新会话）⇒ 顶掉旧连接并把角色租约转过去（§5 的"租约"语义）。
func TestProtoTakeoverReleasesLease(t *testing.T) {
	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	first := dialProto(t, addr)
	first.hello()
	enterProto(t, first, sessionID, charID)

	// 第二次登录：**新会话**（同一账号，模拟换了个客户端/重登）。
	ctx := context.Background()
	second := &storage.SessionRecord{
		SessionID: 8, Account: "tester", Stage: 2, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.Sessions().Create(ctx, second); err != nil {
		t.Fatalf("建第二个会话: %v", err)
	}

	cl2 := dialProto(t, addr)
	cl2.hello()
	cl2.send(&protocol.Envelope{Body: &protocol.Envelope_Reconnect{
		Reconnect: &protocol.Reconnect{SessionToken: sessionTokenV0(second.SessionID)}}})
	if rr, ok := cl2.recv().Body.(*protocol.Envelope_ReconnectResult); !ok ||
		rr.ReconnectResult.GetStatus() != protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT {
		t.Fatalf("第二个会话应回选角")
	}
	cl2.send(&protocol.Envelope{Body: &protocol.Envelope_ListCharacters{ListCharacters: &protocol.ListCharacters{}}})
	cl2.recv()
	cl2.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacter{
		SelectCharacter: &protocol.SelectCharacter{CharacterId: charID}}})
	if sel, ok := cl2.recv().Body.(*protocol.Envelope_SelectCharacterResult); !ok ||
		sel.SelectCharacterResult.GetCode() != protocol.SelectCharCode_SELECT_CHAR_OK {
		t.Fatalf("第二个会话选角应成功（旧连接必须被顶掉并释放租约）")
	}
	cl2.recv() // EnterWorld
	cl2.recv() // AbilityUpdate

	// 旧连接必须已经被服务端关掉。
	first.expectClosed()

	// 世界上只应剩一个「勇士」（顶号不能留下两个同名实体）。
	s.mu.RLock()
	n := 0
	for _, p := range s.world.players {
		if p.Char != nil && p.Char.Name == "勇士" {
			n++
		}
	}
	s.mu.RUnlock()
	if n != 1 {
		t.Errorf("世界里的「勇士」= %d 个，应为 1", n)
	}
}

// enterProto 走完"认领会话 → 选角 → 进图"，断言每一步都成功。
func enterProto(t *testing.T, cl *protoClient, sessionID int32, charID uint64) {
	t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Reconnect{
		Reconnect: &protocol.Reconnect{SessionToken: sessionTokenV0(sessionID)}}})
	cl.recv() // ReconnectResult
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ListCharacters{ListCharacters: &protocol.ListCharacters{}}})
	cl.recv() // CharacterList
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacter{
		SelectCharacter: &protocol.SelectCharacter{CharacterId: charID}}})
	sel, ok := cl.recv().Body.(*protocol.Envelope_SelectCharacterResult)
	if !ok {
		t.Fatalf("选角应答类型不对: %T", sel)
	}
	if sel.SelectCharacterResult.GetCode() != protocol.SelectCharCode_SELECT_CHAR_OK {
		t.Fatalf("选角失败: %v（%s）",
			sel.SelectCharacterResult.GetCode(), sel.SelectCharacterResult.GetMessage())
	}
	if _, ok := cl.recv().Body.(*protocol.Envelope_EnterWorld); !ok {
		t.Fatal("选角成功后应收到 EnterWorld")
	}
	if _, ok := cl.recv().Body.(*protocol.Envelope_AbilityUpdate); !ok {
		t.Fatal("EnterWorld 之后应收到 AbilityUpdate")
	}
}

// TestProtoContractRustClient 是 §9.2 的**完整形态**：
// 起 Go 服务端 → 跑 **`client/e2e` 的剧本**（`mir2-e2e contract`）→ 断言退出码与关键输出。
//
// 为什么值得单列一条：它检验的是**两个独立实现**是否真的对得上 ——
// protoc 各自生成的两端代码、两端各自写的分帧/会话层。这是 R-1（双实现漂移）唯一的
// 自动防线：同一封信，Go 编出来和 Rust 编出来必须逐字节相同（§9.3 的黄金报文已在
// 两端各自单测里钉住），而 §5 的整个序列必须一方发、另一方认。
//
// 约定与其它"依赖外部产物"的用例一致（如 core 的 `real_container_if_present`）：
// **产物缺失就跳过**，并打印怎么补。跑法：
//
//	cargo build -p mir2-e2e && go test ./server/internal/gamesvr/ -run Rust
func TestProtoContractRustClient(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}

	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	// 与 Go 侧主契约同构：视野里放一只怪，好让两边断言的实体数一致。
	mon := newTestMonster(1_000_001, "鸡", 15)
	mon.Object.SetPlace(s.world.defaultMap, 12, 11, entity.DirDown)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	// `-expect-*` 是"服务端那侧已知的真值"：只有播种数据的一方能下这些断言，
	// Rust 客户端本身是通用的（不该知道我们的播种）。存档 Dir=2（原版「右」）
	// ⇒ 线上应当是 3（新枚举 = 原版 + 1）。
	cmd := exec.Command(bin, "contract",
		"-addr", addr,
		"-session", strconv.Itoa(int(sessionID)),
		"-char", strconv.FormatUint(charID, 10),
		"-expect-map", "0",
		"-expect-pos", "10,10",
		"-expect-dir", "3",
		"-expect-entities", "1",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Rust 契约剧本失败：%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "契约通过") {
		t.Errorf("剧本没打通过标记，输出：\n%s", out.String())
	}
	t.Logf("Rust 客户端剧本输出：\n%s", out.String())
}

// findE2EBin 找 `mir2-e2e` 可执行文件；找不到就返回原因（调用方跳过）。
//
// 刻意**不**在测试里自动 `cargo build`：那会把一次网络+编译（分钟级）塞进 `go test`，
// 而这条用例的价值是"偶尔真跑一次"，不是每次单测都跑。要跑就先手动构建。
func findE2EBin() (string, string) {
	if p := os.Getenv("MIR2_E2E_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, ""
		}
		return "", fmt.Sprintf("$MIR2_E2E_BIN=%s 不存在", p)
	}
	// 仓库根：本文件在 server/internal/gamesvr/
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return "", "定位仓库根失败"
	}
	cands := []string{
		// CARGO_TARGET_DIR 可能被指到仓库外（本机就是 ../.mir2-cargo-target）
		filepath.Join(root, "..", ".mir2-cargo-target", "debug", "mir2-e2e"),
		filepath.Join(root, "client", "target", "debug", "mir2-e2e"),
	}
	if td := os.Getenv("CARGO_TARGET_DIR"); td != "" {
		cands = append([]string{filepath.Join(td, "debug", "mir2-e2e")}, cands...)
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, ""
		}
	}
	return "", "没找到 mir2-e2e 可执行文件（先 `cd client && cargo build -p mir2-e2e`，" +
		"或设 $MIR2_E2E_BIN 指向它）"
}

// ---------- 编号顺序的钉子 ----------

// 朝向的编号顺序是**契约**：新枚举 = 原版 + 1（common.proto 的注释）。
//
// 这条测试把两侧的常量钉在一起。任何一边被"顺手重排"都会立刻红 ——
// 否则症状是"人物朝上却在往下走"，而两端都会认为自己对（§9 要防的正是这个）。
func TestDirectionEnumMatchesLegacyOrder(t *testing.T) {
	cases := []struct {
		legacy uint8
		want   protocol.Direction
		name   string
	}{
		{entity.DirUp, protocol.Direction_DIR_UP, "上"},
		{entity.DirUpRight, protocol.Direction_DIR_UP_RIGHT, "右上"},
		{entity.DirRight, protocol.Direction_DIR_RIGHT, "右"},
		{entity.DirDownRight, protocol.Direction_DIR_DOWN_RIGHT, "右下"},
		{entity.DirDown, protocol.Direction_DIR_DOWN, "下"},
		{entity.DirDownLeft, protocol.Direction_DIR_DOWN_LEFT, "左下"},
		{entity.DirLeft, protocol.Direction_DIR_LEFT, "左"},
		{entity.DirUpLeft, protocol.Direction_DIR_UP_LEFT, "左上"},
	}
	for _, c := range cases {
		if got := directionOf(c.legacy); got != c.want {
			t.Errorf("原版 %d（%s）→ %v，应为 %v", c.legacy, c.name, got, c.want)
		}
		if int32(c.legacy)+1 != int32(c.want) {
			t.Errorf("%s：新枚举应当就是原版 + 1（%d vs %v）", c.name, c.legacy, c.want)
		}
	}
	if got := directionOf(99); got != protocol.Direction_DIRECTION_UNSPECIFIED {
		t.Errorf("越界朝向应回 0（未指定），实得 %v", got)
	}
	// 原版只有 8 个方向；多出来的一定是枚举被改动了。
	if entity.DirUpLeft != 7 {
		t.Errorf("原版朝向常量变成 %d 个了？", entity.DirUpLeft+1)
	}
}

package gamesvr

import (
	"bufio"
	"bytes"
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

	"github.com/algotao/mir2/server/internal/authn"

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

	// border=true：四周一圈阻挡 ⇒ "撞墙"那条用例有确定的地形可用
	//（玩家播在 (1,1)，向上是 (1,0) 即边界）。
	m := world.Generate("0", 60, 60, true)
	mm := world.NewMapManager("", 4)
	mm.Put(m)
	mm.SetNames(map[string]string{"0": "0"})

	s := testSlaveServer() // players / monsters / monsterIdx / social.groups
	s.store = store
	s.world.maps = mm
	s.world.defaultMap = m
	// 建角要用"默认地图号"当出生点（`newCharHome`）—— 测试服务器的默认地图就是 "0"。
	s.data.defaultMapID = "0"
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
			// 播在 (1,1)：紧贴边界 ⇒ 向右可走、向上被挡（见 protoContractServer 的地图）
			CurMap: "0", CurX: 1, CurY: 1, Dir: uint32(entity.DirRight), Hair: 3,
			// ⚠️ DC 必须给：打怪的伤害来自它，缺了就只有 0-0（战斗用例会验不到东西）
			Abil: &pb.Ability{Level: 7, Hp: 30, MaxHp: 40, Mp: 5, MaxMp: 9,
				Dc: &pb.MinMax{Min: 20, Max: 25}},
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

// protoEvents 记录服务端**主动推**来的实体事件。
//
// ⚠️ 进世界之后这些消息是**随时**来的（怪物 AI、别人的移动、周期性视野同步）——
// 真客户端按类型分派，契约测试也得照这个来，否则会因为一条完全正常的
// EntityMove 判成"顺序不符"而红（本轮真的这么红过一次）。
type protoEvents struct {
	appears    []*protocol.EntityState
	moves      []*protocol.EntityMove
	disappears []uint64
}

// note 若这条是实体事件就记下并返回 true（调用方据此跳过它）。
func (e *protoEvents) note(env *protocol.Envelope) bool {
	switch b := env.Body.(type) {
	case *protocol.Envelope_EntityAppear:
		e.appears = append(e.appears, b.EntityAppear.GetEntity())
		return true
	case *protocol.Envelope_EntityMove:
		e.moves = append(e.moves, b.EntityMove)
		return true
	case *protocol.Envelope_EntityDisappear:
		e.disappears = append(e.disappears, b.EntityDisappear.GetEntityId())
		return true
	}
	return false
}

// waitFor 一直读到 pred 命中为止；途中遇到的实体事件记进 ev。
func (cl *protoClient) waitFor(ev *protoEvents, what string, pred func(*protocol.Envelope) bool) *protocol.Envelope {
	cl.t.Helper()
	for i := 0; i < 128; i++ {
		env := cl.recv()
		if pred(env) {
			return env
		}
		if !ev.note(env) {
			cl.t.Fatalf("等 %s 时收到无关消息 %s", what, frame.MsgName(env))
		}
	}
	cl.t.Fatalf("等 %s 时读了 128 条都没等到", what)
	return nil
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

	// 视野里放一只怪（玩家在 (1,1)，怪在 (3,2)：距离 2 ⇒ 在视野内）。
	// 进图快照里"看得见的实体"必须包含它。
	mon := newTestMonster(1_000_001, "鸡", 15)
	mon.Object.SetPlace(s.world.defaultMap, 3, 2, entity.DirDown)
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
	ev := &protoEvents{}
	ewEnv := cl.waitFor(ev, "EnterWorld", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_EnterWorld)
		return ok
	})
	ew := ewEnv.Body.(*protocol.Envelope_EnterWorld)
	enter := ew.EnterWorld
	if enter.GetSelfEntityId() == 0 {
		t.Error("EnterWorld 必须带 self_entity_id")
	}
	if enter.GetMapName() != "0" {
		t.Errorf("地图 = %q，应为 %q", enter.GetMapName(), "0")
	}
	if p := enter.GetPosition(); p.GetX() != 1 || p.GetY() != 1 {
		t.Errorf("坐标 = (%d,%d)，应为存档里的 (1,1)", p.GetX(), p.GetY())
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
	if p := found.GetPosition(); p.GetX() != 3 || p.GetY() != 2 {
		t.Errorf("怪的快照坐标 = (%d,%d)，应为 (3,2)", p.GetX(), p.GetY())
	}

	abEnv := cl.waitFor(ev, "AbilityUpdate", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_AbilityUpdate)
		return ok
	})
	ab := abEnv.Body.(*protocol.Envelope_AbilityUpdate)
	if got := ab.AbilityUpdate.GetAbility(); got.GetLevel() != 7 || got.GetHp() != 30 || got.GetMaxHp() != 40 {
		t.Errorf("能力值 = %+v", got)
	}

	// ⑤ 心跳
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 12345}}})
	pong := cl.waitFor(ev, "Pong", isPong).Body.(*protocol.Envelope_Pong)
	if pong.Pong.GetClientTimeMs() != 12345 {
		t.Errorf("Pong 应回显 client_time_ms，实得 %d", pong.Pong.GetClientTimeMs())
	}

	// ⑥ 未知/未实现的消息：记数 + 忽略，**禁止 panic、禁止断开**（§4.1 硬规则 4）。
	cl.send(&protocol.Envelope{}) // 连 oneof case 都没有
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Raw{Raw: &protocol.Raw{MsgId: 0x0F01}}})
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Ping{Ping: &protocol.Ping{ClientTimeMs: 999}}})
	pong2 := cl.waitFor(ev, "Pong（未知消息之后）", isPong).Body.(*protocol.Envelope_Pong)
	if pong2.Pong.GetClientTimeMs() != 999 {
		t.Errorf("Pong 回显 = %d", pong2.Pong.GetClientTimeMs())
	}

	// ---------- ⑦ 实时性：服务端**主动推**的实体事件 ----------
	//
	// 这一段是本轮的验收点：进图不再是一张静止的快照。
	player := onlyProtoPlayer(t, s)

	// （a）别的实体走动 ⇒ EntityMove，且必须带 from（客户端靠它插值）。
	s.broadcastMonsterMove(monsterMove{id: mon.ID, x: 4, y: 2, dir: entity.DirRight,
		mapRef: s.world.defaultMap, fromX: 3, fromY: 2})
	mv := waitMoveOf(t, cl, ev, uint64(mon.ID))
	if f := mv.GetFrom(); f.GetX() != 3 || f.GetY() != 2 {
		t.Errorf("EntityMove.from = (%d,%d)，应为移动前的 (3,2)", f.GetX(), f.GetY())
	}
	if to := mv.GetTo(); to.GetX() != 4 || to.GetY() != 2 {
		t.Errorf("EntityMove.to = (%d,%d)，应为 (4,2)", to.GetX(), to.GetY())
	}
	if d := mv.GetDirection(); d != protocol.Direction_DIR_RIGHT {
		t.Errorf("EntityMove.direction = %v，应为 DIR_RIGHT", d)
	}

	// （b）走出视野 ⇒ EntityDisappear（由周期性视野同步发现 ——
	// 这正是 legacy 那半边缺的那一块：站着不动也得看得见"走近/走远"）。
	mon.Object.SetPlace(s.world.defaultMap, 40, 40, entity.DirRight)
	s.world.monsterIdx.Update(mon)
	s.tickProtoVision(player)
	waitDisappearOf(t, cl, ev, uint64(mon.ID))

	// （c）再走回视野 ⇒ EntityAppear
	mon.Object.SetPlace(s.world.defaultMap, 3, 2, entity.DirLeft)
	s.world.monsterIdx.Update(mon)
	s.tickProtoVision(player)
	ap := waitAppearOf(t, cl, ev, uint64(mon.ID))
	if p := ap.GetPosition(); p.GetX() != 3 || p.GetY() != 2 {
		t.Errorf("重新出现的怪坐标 = (%d,%d)，应为 (3,2)", p.GetX(), p.GetY())
	}

	// （d）自己走一步：MoveInput ⇒ 收到**自己的权威回显**（客户端预测的纠偏依据）。
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_MoveInput{
		MoveInput: &protocol.MoveInput{Direction: protocol.Direction_DIR_RIGHT, ClientTick: 1}}})
	self := waitMoveOf(t, cl, ev, uint64(player.Obj.ID))
	if f := self.GetFrom(); f.GetX() != 1 || f.GetY() != 1 {
		t.Errorf("自己移动的 from = (%d,%d)，应为 (1,1)", f.GetX(), f.GetY())
	}
	if to := self.GetTo(); to.GetX() != 2 || to.GetY() != 1 {
		t.Errorf("自己移动的 to = (%d,%d)，应为 (2,1)", to.GetX(), to.GetY())
	}

	// （e）**超速**：紧接着再走一次 ⇒ 限流拒绝，**必须回一条** MoveRejected(1)。
	// 这一条是新协议特有的：客户端已经**预测**着走过去了，服务端不吭声
	// 它的位置就永久分叉（legacy 不预测，所以那边静默忽略是对的）。
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_MoveInput{
		MoveInput: &protocol.MoveInput{Direction: protocol.Direction_DIR_RIGHT, ClientTick: 2}}})
	fast := waitReject(t, cl, ev)
	if fast.GetReason() != 1 {
		t.Errorf("紧接着的第二次移动应被限流（reason 1），实得 %d", fast.GetReason())
	}
	if p := fast.GetAuthoritativePosition(); p.GetX() != 2 || p.GetY() != 1 {
		t.Errorf("限流的权威位置 = (%d,%d)，应为原地 (2,1)", p.GetX(), p.GetY())
	}

	// （f）撞墙：向上是边界 ⇒ MoveRejected(3)，把预测掰回权威位置。
	// ⚠️ 先等过限流窗口（走路 600ms，见 entity.NewMoveLimiter）——
	// 否则这一条会先被（e）的限流拦下，验不到"阻挡"。
	time.Sleep(700 * time.Millisecond)
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_MoveInput{
		MoveInput: &protocol.MoveInput{Direction: protocol.Direction_DIR_UP, ClientTick: 3}}})
	rej := waitReject(t, cl, ev)
	if p := rej.GetAuthoritativePosition(); p.GetX() != 2 || p.GetY() != 1 {
		t.Errorf("MoveRejected 的权威位置 = (%d,%d)，应为 (2,1)", p.GetX(), p.GetY())
	}
	if rej.GetReason() != 3 {
		t.Errorf("MoveRejected.reason = %d，应为 3（阻挡）", rej.GetReason())
	}
	// 全程**不该有任何"多出来"的实体事件**：上面每一步都用带 id 的谓词精确取走了
	// 它要的那条，剩下的计数只会被"意料之外的重发"顶起来。
	//
	// ⚠️ 这条断言不是摆设：本轮它真的抓到过一次 —— 进图时"快照已经给了实体、
	// `updateVision` 又把它当新进入视野推一遍"，于是这里会多出 1 条 EntityAppear。
	if n := len(ev.appears); n != 0 {
		t.Errorf("多出来 %d 条 EntityAppear（快照里的实体不该再当\"新出现\"推一遍）：%+v", n, ev.appears)
	}
	if n := len(ev.disappears); n != 0 {
		t.Errorf("多出来 %d 条 EntityDisappear：%v", n, ev.disappears)
	}
	if n := len(ev.moves); n != 0 {
		t.Errorf("多出来 %d 条 EntityMove：%+v", n, ev.moves)
	}
}

func isPong(e *protocol.Envelope) bool {
	_, ok := e.Body.(*protocol.Envelope_Pong)
	return ok
}

// onlyProtoPlayer 取世界里那名新协议玩家（本用例里只有他一个）。
func onlyProtoPlayer(t *testing.T, s *Server) *Player {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.world.players {
		if p.protoOut != nil {
			return p
		}
	}
	t.Fatal("世界里没有新协议玩家")
	return nil
}

func waitMoveOf(t *testing.T, cl *protoClient, ev *protoEvents, id uint64) *protocol.EntityMove {
	t.Helper()
	env := cl.waitFor(ev, fmt.Sprintf("ActorId=%d 的 EntityMove", id), func(e *protocol.Envelope) bool {
		b, ok := e.Body.(*protocol.Envelope_EntityMove)
		return ok && b.EntityMove.GetEntityId() == id
	})
	return env.Body.(*protocol.Envelope_EntityMove).EntityMove
}

func waitAppearOf(t *testing.T, cl *protoClient, ev *protoEvents, id uint64) *protocol.EntityState {
	t.Helper()
	env := cl.waitFor(ev, fmt.Sprintf("ActorId=%d 的 EntityAppear", id), func(e *protocol.Envelope) bool {
		b, ok := e.Body.(*protocol.Envelope_EntityAppear)
		return ok && b.EntityAppear.GetEntity().GetEntityId() == id
	})
	return env.Body.(*protocol.Envelope_EntityAppear).EntityAppear.GetEntity()
}

func waitDisappearOf(t *testing.T, cl *protoClient, ev *protoEvents, id uint64) {
	t.Helper()
	cl.waitFor(ev, fmt.Sprintf("ActorId=%d 的 EntityDisappear", id), func(e *protocol.Envelope) bool {
		b, ok := e.Body.(*protocol.Envelope_EntityDisappear)
		return ok && b.EntityDisappear.GetEntityId() == id
	})
}

func waitReject(t *testing.T, cl *protoClient, ev *protoEvents) *protocol.MoveRejected {
	t.Helper()
	env := cl.waitFor(ev, "MoveRejected", func(e *protocol.Envelope) bool {
		_, ok := e.Body.(*protocol.Envelope_MoveRejected)
		return ok
	})
	return env.Body.(*protocol.Envelope_MoveRejected).MoveRejected
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
	mon.Object.SetPlace(s.world.defaultMap, 3, 2, entity.DirDown)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	// `-expect-*` 是"服务端那侧已知的真值"：只有播种数据的一方能下这些断言，
	// Rust 客户端本身是通用的（不该知道我们的播种）。存档 Dir=2（原版「右」）
	// ⇒ 线上应当是 3（新枚举 = 原版 + 1）。玩家在 (1,1)，向右走一步到 (2,1)。
	cmd := exec.Command(bin, "contract",
		"-addr", addr,
		"-session", strconv.Itoa(int(sessionID)),
		"-char", strconv.FormatUint(charID, 10),
		"-expect-map", "0",
		"-expect-pos", "1,1",
		"-expect-dir", "3",
		"-expect-entities", "1",
		"-expect-walk-to", "2,1",
		"-expect-pushed", "1",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 Rust 剧本: %v", err)
	}
	// 剧本跑起来之后，由**服务端这侧**推一条实体事件 —— 这就是"实时性"那一步的对手方
	//（客户端在 [9] 等着它）。
	go func() {
		// 等到"玩家已进图 **且视野账本里已经有那只怪**"再推。
		// ⚠️ 这个条件不是"等一会儿"那种赌时间的写法：它正好是
		// `broadcastMonsterMove` 的投递前提（`p.visible.Contains`），
		// 抢在它之前推的话那一条会被过滤掉，用例就会偶发地挂。
		for i := 0; i < 300; i++ {
			s.mu.RLock()
			var ready bool
			for _, p := range s.world.players {
				if p.protoOut != nil && p.visible.Contains(mon.ID) {
					ready = true
					break
				}
			}
			s.mu.RUnlock()
			if ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.broadcastMonsterMove(monsterMove{id: mon.ID, x: 4, y: 2, dir: entity.DirRight,
			mapRef: s.world.defaultMap, fromX: 3, fromY: 2})
	}()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("Rust 契约剧本失败：%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "契约通过") {
		t.Errorf("剧本没打通过标记，输出：\n%s", out.String())
	}
	t.Logf("Rust 客户端剧本输出：\n%s", out.String())
}

// TestProtoRustWorldModel 走**会话层**那条路（`mir2-net` 的消息泵 + `core` 的握手与世界
// 状态机）—— 这正是 `client/app` 用的那一份（D-18），所以它能替 app 守住"连上服务端"。
//
// 与 `TestProtoContractRustClient` 的分工：
//
//	contract —— 逐条消息的类型/顺序/字段（"线路对不对"）
//	world    —— 状态机串起来能不能用（"连上之后世界是什么样"，且世界真的在动）
func TestProtoRustWorldModel(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}

	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	mon := newTestMonster(1_000_001, "鸡", 15)
	mon.Object.SetPlace(s.world.defaultMap, 3, 2, entity.DirDown)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	// `-expect-entity-at 4,2` 是关键的一条：驱动方会把这只鸡推一步到 (4,2)，
	// 断言它必须**落到世界模型里**（不只是"线上收到过一条消息"）。
	cmd := exec.Command(bin, "world",
		"-addr", addr,
		"-session", strconv.Itoa(int(sessionID)),
		"-char", strconv.FormatUint(charID, 10),
		"-expect-map", "0",
		"-expect-pos", "1,1",
		"-expect-entities", "1",
		"-expect-entity-at", "4,2",
		"-move-steps", "1",
		"-timeout-ms", "8000",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 Rust 世界脚本: %v", err)
	}
	// 玩家进图后推一条实体事件（与 contract 那条同样的同步手法：等的是
	// broadcastMonsterMove 的投递前提，而不是"等一会儿"）。
	go func() {
		for i := 0; i < 300; i++ {
			s.mu.RLock()
			var ready bool
			for _, p := range s.world.players {
				if p.protoOut != nil && p.visible.Contains(mon.ID) {
					ready = true
					break
				}
			}
			s.mu.RUnlock()
			if ready {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.broadcastMonsterMove(monsterMove{id: mon.ID, x: 4, y: 2, dir: entity.DirRight,
			mapRef: s.world.defaultMap, fromX: 3, fromY: 2})
	}()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("Rust 世界脚本失败：%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "世界状态通过") {
		t.Errorf("脚本没打通过标记，输出：\n%s", out.String())
	}
	t.Logf("Rust 世界模型输出：\n%s", out.String())
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

// TestProtoRustCombat 是 A′（攻击 → 伤害 → 血量 → 死亡）的跨实现验收：
// Rust 客户端发 `AttackInput`，Go 服务端走 legacy 那条结算路径，客户端应当依次看到
// **自己的挥砍动作**、**伤害**、**血量**、**死亡**。
//
// ⚠️ 为什么这条值得单列：战斗是本项目**规则最密**的一块（威力/打空/减防/麻痹/掉落/经验）。
// 它同时也是"新协议客户端能不能真的玩"的第一道门槛。
func TestProtoRustCombat(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}

	s, store, addr := protoContractServer(t)
	sessionID, charID := seedAccount(t, store)

	// 一只紧贴玩家的怪（对角相邻 = 八格里的一格），15 血、玩家 DC 20-25 ⇒ 一刀毙命。
	// 位置 (2,2) 与玩家 (1,1) 是"右下"邻格 —— 正好验 service 端算出的朝向。
	mon := newTestMonster(1_000_001, "鸡", 15)
	mon.Object.SetPlace(s.world.defaultMap, 2, 2, entity.DirDown)
	s.world.monsters[mon.ID] = mon
	s.world.monsterIdx.Add(mon)

	cmd := exec.Command(bin, "world",
		"-addr", addr,
		"-session", strconv.Itoa(int(sessionID)),
		"-char", strconv.FormatUint(charID, 10),
		"-expect-map", "0",
		"-expect-pos", "1,1",
		"-expect-entities", "1",
		"-attack", strconv.FormatUint(uint64(mon.ID), 10),
		"-expect-damage", "1",
		"-expect-kill",
		"-timeout-ms", "8000",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Rust 战斗剧本失败：%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "世界状态通过") {
		t.Errorf("剧本没打通过标记，输出：\n%s", out.String())
	}
	// 伤害值必须是**真的算出来的**（不是 0）：DC 20-25 减怪物 AC 之后仍应 > 0。
	if !strings.Contains(out.String(), "伤害：") {
		t.Errorf("输出里没有伤害事件：\n%s", out.String())
	}
	t.Logf("Rust 战斗剧本输出：\n%s", out.String())
}

// TestProtoLoginChallenge 是 D-24① 挑战应答的**完整线上往返**：
//
//	ClientHello → ServerHello(nonce) → LoginSaltRequest → LoginSalt
//	→（客户端算 K 与证明）→ Login → LoginResult
//
// 并验三件事：拿到的 token 真的能用（Reconnect 回去）、错口令被拒、
// 错够次数会锁（策略与 accountsvc 共用 `authn` 那一份）。
//
// 建号（D-32）的线上契约：开关、先取盐、重名、节流、非法名，五条都要对，
// 而且**建完立刻能用它登录**（这条一通，说明盐/K/落库整条链是对齐的）。
//
// ⚠️ 节流是**按 IP** 的，而契约测试全走 127.0.0.1 ⇒ 用例之间必须显式清掉节流表，
// 否则第二条会被第一条的 5 秒窗口挡住（"测试互相干扰"是最难查的那类）。
func TestProtoCreateAccount(t *testing.T) {
	srv, store, addr := protoContractServer(t)

	// 直连发一条建号；`takeSalt=false` 模拟"没先取盐"。
	create := func(t *testing.T, account, password string, takeSalt bool) *protocol.CreateAccountResult {
		t.Helper()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("连接: %v", err)
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		write := func(env *protocol.Envelope) {
			if err := frame.Write(c, env); err != nil {
				t.Fatalf("发帧: %v", err)
			}
		}
		read := func() *protocol.Envelope {
			env, err := frame.Read(rd)
			if err != nil {
				t.Fatalf("读帧: %v", err)
			}
			return env
		}
		write(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
			ProtocolVersion: protocol.Version, ClientBuild: "test", Locale: "zh-CN",
		}}})
		read() // ServerHello

		verifier := hex.EncodeToString(make([]byte, sha256.Size))
		if takeSalt {
			write(&protocol.Envelope{Body: &protocol.Envelope_LoginSaltRequest{
				LoginSaltRequest: &protocol.LoginSaltRequest{Account: account}}})
			s := read().GetLoginSalt()
			if len(s.GetSalt()) == 0 || s.GetIterations() == 0 || s.GetKeyLen() == 0 {
				t.Fatalf("LoginSalt 不完整: %v", s)
			}
			k, err := pbkdf2.Key(sha256.New, password, s.GetSalt(),
				int(s.GetIterations()), int(s.GetKeyLen()))
			if err != nil {
				t.Fatalf("PBKDF2: %v", err)
			}
			verifier = hex.EncodeToString(k)
		}
		write(&protocol.Envelope{Body: &protocol.Envelope_CreateAccount{CreateAccount: &protocol.CreateAccount{
			Account: account, Verifier: verifier}}})
		return read().GetCreateAccountResult()
	}
	clearThrottle := func() {
		srv.createMu.Lock()
		srv.createLast = nil
		srv.createMu.Unlock()
	}
	code := func(r *protocol.CreateAccountResult) uint32 { return r.GetResult().GetCode() }

	// ① 开关默认关 ⇒ 拒（而且这一判**先于**取盐，所以不取盐也回同一码）
	if got := create(t, "newbie", "pw123", false); code(got) != createAccountDisabled {
		t.Fatalf("开关关着该拒，实得 code=%d %s", code(got), got.GetResult().GetMessage())
	}

	srv.cfg.allowNewAccount = true

	// ② 没先取盐 ⇒ 拒（否则会存下一个"永远登不上"的号）
	clearThrottle()
	if got := create(t, "newbie", "pw123", false); code(got) != createAccountNoSalt {
		t.Fatalf("没取盐该拒，实得 code=%d %s", code(got), got.GetResult().GetMessage())
	}

	// ③ 正常建号：账号名**统一转小写**（原版客户端也 LowerCase，见 IntroScn.pas:1035）
	clearThrottle()
	if got := create(t, "NewBie", "pw123", true); code(got) != createAccountOK {
		t.Fatalf("建号该成功，实得 code=%d %s", code(got), got.GetResult().GetMessage())
	}
	ctx := context.Background()
	if _, err := store.Accounts().GetByName(ctx, "newbie"); err != nil {
		t.Fatalf("建完该以**小写**入库: %v", err)
	}

	// ④ 重名 ⇒ 拒（原版 AccountDB.Index >= 0 走的就是这条）
	clearThrottle()
	if got := create(t, "newbie", "other", true); code(got) != createAccountExists {
		t.Fatalf("重名该拒，实得 code=%d %s", code(got), got.GetResult().GetMessage())
	}

	// ⑤ 节流：不清表连发两条，第二条必须被挡（每 IP 5 秒）
	//
	// ⚠️ 节流记的是**尝试**（失败也占额度）—— 那正是防刷的意义。所以这里要先清表，
	// 否则上一用例（重名那次尝试）已经把额度占了。
	clearThrottle()
	if got := create(t, "another1", "pw123", true); code(got) != createAccountOK {
		t.Fatalf("节流窗口内第一条该放行，实得 code=%d", code(got))
	}
	if got := create(t, "another2", "pw123", true); code(got) != createAccountTooFast {
		t.Fatalf("5 秒内第二条该被挡，实得 code=%d %s", code(got), got.GetResult().GetMessage())
	}

	// ⑥ 非法账号名
	for _, bad := range []string{"ab", "0123456789abcde", "有中文", "a b", "a-b"} {
		clearThrottle()
		if got := create(t, bad, "pw123", true); code(got) != createAccountBadName {
			t.Fatalf("非法名 %q 该拒，实得 code=%d", bad, code(got))
		}
	}

	// ⑦ 建完的号**立刻能登录**（盐、K、库三者对齐的终点断言）
	clearThrottle()
	{
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("连接: %v", err)
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		write := func(env *protocol.Envelope) {
			if err := frame.Write(c, env); err != nil {
				t.Fatalf("发帧: %v", err)
			}
		}
		read := func() *protocol.Envelope {
			env, err := frame.Read(rd)
			if err != nil {
				t.Fatalf("读帧: %v", err)
			}
			return env
		}
		write(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
			ProtocolVersion: protocol.Version, ClientBuild: "test"}}})
		nonce := read().GetServerHello().GetSessionKey()
		write(&protocol.Envelope{Body: &protocol.Envelope_LoginSaltRequest{
			LoginSaltRequest: &protocol.LoginSaltRequest{Account: "newbie"}}})
		s := read().GetLoginSalt()
		k, err := pbkdf2.Key(sha256.New, "pw123", s.GetSalt(), int(s.GetIterations()), int(s.GetKeyLen()))
		if err != nil {
			t.Fatalf("PBKDF2: %v", err)
		}
		write(&protocol.Envelope{Body: &protocol.Envelope_Login{Login: &protocol.Login{
			Account:      "newbie",
			PasswordHash: hex.EncodeToString(authn.ExpectedProof(k, nonce, "newbie")),
		}}})
		if got := read().GetLoginResult(); got.GetCode() != protocol.LoginCode_LOGIN_OK {
			t.Fatalf("新建的号该能登录，实得 %v %s", got.GetCode(), got.GetMessage())
		}
	}
}

// ⚠️ 这里手搓每一步，**故意不复用** `core::entrance`：服务端测试要对的是线上字节，
// 客户端状态机错的时候这条不该跟着错。
func TestProtoLoginChallenge(t *testing.T) {
	_, store, addr := protoContractServer(t)
	seedAccount(t, store) // 账号 tester / 口令 pw

	// 走一遍登录，返回 (结果, nonce)
	login := func(t *testing.T, account, password string) *protocol.LoginResult {
		t.Helper()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("连接: %v", err)
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		write := func(env *protocol.Envelope) {
			if err := frame.Write(c, env); err != nil {
				t.Fatalf("发帧: %v", err)
			}
		}
		read := func() *protocol.Envelope {
			env, err := frame.Read(rd)
			if err != nil {
				t.Fatalf("读帧: %v", err)
			}
			return env
		}

		write(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
			ProtocolVersion: protocol.Version, ClientBuild: "test", Locale: "zh-CN",
		}}})
		nonce := read().GetServerHello().GetSessionKey()
		if len(nonce) == 0 {
			t.Fatal("握手里没给 nonce（挑战应答要用它）")
		}

		// ① 取盐
		write(&protocol.Envelope{Body: &protocol.Envelope_LoginSaltRequest{
			LoginSaltRequest: &protocol.LoginSaltRequest{Account: account}}})
		salt := read().GetLoginSalt()
		if len(salt.GetSalt()) == 0 || salt.GetIterations() == 0 || salt.GetKeyLen() == 0 {
			t.Fatalf("LoginSalt 不完整: %v", salt)
		}

		// ② 算证明：K = PBKDF2(口令, 盐, 迭代, 派生长)，证明 = HMAC(K, nonce‖account)
		k, err := pbkdf2.Key(sha256.New, password, salt.GetSalt(),
			int(salt.GetIterations()), int(salt.GetKeyLen()))
		if err != nil {
			t.Fatalf("PBKDF2: %v", err)
		}
		write(&protocol.Envelope{Body: &protocol.Envelope_Login{Login: &protocol.Login{
			Account:      account,
			PasswordHash: hex.EncodeToString(authn.ExpectedProof(k, nonce, account)),
		}}})
		return read().GetLoginResult()
	}

	// 口令对 ⇒ OK + 4 字节 token
	res := login(t, "tester", "pw")
	if res.GetCode() != protocol.LoginCode_LOGIN_OK {
		t.Fatalf("登录该成功，实得 %v %s", res.GetCode(), res.GetMessage())
	}
	tok := res.GetSessionToken()
	if len(tok) != 4 {
		t.Fatalf("session_token 该是 4 字节（会话号），实得 %d 字节", len(tok))
	}
	if sid := int32(binary.LittleEndian.Uint32(tok)); sid < 2 {
		t.Fatalf("会话号不合法: %d", sid)
	}

	// token 真的能用：Reconnect 回去应回"回选角"（这条连接还没选角）
	{
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("连接: %v", err)
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		if err := frame.Write(c, &protocol.Envelope{Body: &protocol.Envelope_ClientHello{
			ClientHello: &protocol.ClientHello{ProtocolVersion: protocol.Version, ClientBuild: "test"}}}); err != nil {
			t.Fatalf("发 ClientHello: %v", err)
		}
		if _, err := frame.Read(rd); err != nil {
			t.Fatalf("读 ServerHello: %v", err)
		}
		if err := frame.Write(c, &protocol.Envelope{Body: &protocol.Envelope_Reconnect{
			Reconnect: &protocol.Reconnect{SessionToken: tok}}}); err != nil {
			t.Fatalf("发 Reconnect: %v", err)
		}
		env, err := frame.Read(rd)
		if err != nil {
			t.Fatalf("读 ReconnectResult: %v", err)
		}
		if got := env.GetReconnectResult().GetStatus(); got != protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT {
			t.Fatalf("拿登录签发的 token 重连该回「回选角」，实得 %v", got)
		}
	}

	// 口令错 ⇒ 被拒（且**不区分**账号是否存在）
	if got := login(t, "tester", "wrong"); got.GetCode() != protocol.LoginCode_LOGIN_BAD_CREDENTIALS {
		t.Fatalf("错口令该被拒，实得 %v %s", got.GetCode(), got.GetMessage())
	}
	// 不存在的账号：盐照给（随机），登录照样只是"口令不正确"——不暴露账号是否存在
	if got := login(t, "nobody", "pw"); got.GetCode() != protocol.LoginCode_LOGIN_BAD_CREDENTIALS {
		t.Fatalf("不存在的账号该按口令错误回，实得 %v", got.GetCode())
	}

	// 错够次数（默认 5）之后连**对的**口令也要被锁 —— 策略与 accountsvc 共用一份
	for i := 0; i < 4; i++ {
		login(t, "tester", "wrong")
	}
	if got := login(t, "tester", "pw"); got.GetCode() != protocol.LoginCode_LOGIN_LOCKED {
		t.Fatalf("错够次数后该锁，实得 %v %s", got.GetCode(), got.GetMessage())
	}
}

// TestProtoRustLogin 让**真 Rust 客户端**用口令登一次（D-24① 的跨语言端到端）：
// 先取盐、算证明、发 Login，然后接着走选角 → 进世界 —— 与产线上同一条路。
//
// ⚠️ 与 `TestProtoLoginChallenge`（Go 手搓每一步）互补：那条验**服务端**的线上字节，
// 这条验**客户端实现**（PBKDF2/HMAC 算得对不对、顺序对不对）。两端各写一套密码学，
// 算得不一样的话只有这条会发现。
func TestProtoRustLogin(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}
	_, store, addr := protoContractServer(t)
	seedAccount(t, store) // 账号 tester / 口令 pw

	out, err := exec.Command(bin, "world",
		"-addr", addr,
		"-account", "tester",
		"-password", "pw",
		"-expect-map", "0",
		"-expect-pos", "1,1",
		// 这条验的是**登录**那条路（视野里没有别人 ⇒ 0 个实体）
		"-expect-entities", "0",
		"-timeout-ms", "8000",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("Rust 口令登录失败：%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "世界状态通过") {
		t.Errorf("没走通：\n%s", out)
	}

	// 错口令必须进不去 —— 否则"验过了"就是假的（这条防的是"客户端根本不发证明也能过"）
	out2, err2 := exec.Command(bin, "world",
		"-addr", addr, "-account", "tester", "-password", "WRONG", "-timeout-ms", "3000",
	).CombinedOutput()
	if err2 == nil {
		t.Fatalf("错口令竟然也进了世界：\n%s", out2)
	}
	t.Logf("Rust 口令登录输出：\n%s", out)
}

// TestCharacterSummaryGender：选角界面要靠 `Gender` 挑小人图。
//
// 原版选角界面按 (Job, Sex) 各有一套坐标与图号
// （`IntroScn.pas:1390-1429`；站位/图号 `stand_index = 40+Job*40+Sex*120`）。
// 这个字段**在协议里、在存档里，就是没接上** —— 于是六个职业/性别组合
// 全会画成同一个。这里把"照实下发 + 脏数据不猜"钉住。
func TestCharacterSummaryGender(t *testing.T) {
	for _, c := range []struct {
		sex  uint32
		want protocol.Gender
		why  string
	}{
		{0, protocol.Gender_GENDER_MALE, "存档 0 = 男"},
		{1, protocol.Gender_GENDER_FEMALE, "存档 1 = 女"},
		{7, protocol.Gender_GENDER_UNSPECIFIED, "脏数据 ⇒ 未指定（不猜）"},
	} {
		sum := characterSummary(&storage.Character{
			ID: 1, Name: "勇士", Level: 7, Job: 0,
			Data: &pb.CharacterData{Job: 0, Hair: 3, Sex: c.sex},
		})
		if sum.Gender != c.want {
			t.Errorf("Sex=%d：Gender=%v，期望 %v（%s）", c.sex, sum.Gender, c.want, c.why)
		}
		if sum.Class != protocol.CharClass_CHAR_CLASS_WARRIOR {
			t.Errorf("Sex=%d：Class=%v，期望战士", c.sex, sum.Class)
		}
		if sum.GenderHair != 3 {
			t.Errorf("Sex=%d：GenderHair=%d，期望 3（发型照旧要带上）", c.sex, sum.GenderHair)
		}
	}
	// 没有存档详情也不能崩（损坏/半初始化的数据）
	if sum := characterSummary(&storage.Character{ID: 2, Name: "空档"}); sum.Gender != protocol.Gender_GENDER_UNSPECIFIED {
		t.Errorf("无 Data 时 Gender=%v，期望未指定", sum.Gender)
	}
}

// TestProtoRustSignup 是**建号**的跨实现验收（D-32）：Rust 客户端建号 + 就地用同一个
// 口令登录，服务端只看线上字节 —— 与 `TestProtoRustLogin` 同一套路。
//
// ⚠️ 这里必须把服务端开成 `-allow-new-account`：**默认关**是产品默认（公网上开着等于
// 把注册入口挂出去），测试里显式打开它；"关着就拒"那条在 `TestProtoCreateAccount` 里。
func TestProtoRustSignup(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}
	srv, _, addr := protoContractServer(t)
	srv.cfg.allowNewAccount = true

	out, err := exec.Command(bin, "signup",
		"-addr", addr,
		"-account", "rustsignup",
		"-password", "pw123",
		"-expect-msg", "账号已建立，请登录",
		"-timeout-ms", "8000",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("Rust 建号链路失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "跨语言链路 OK") {
		t.Fatalf("没看到成功标志：\n%s", out)
	}
}

// TestProtoRustPickCharacter 是**选角**的跨实现验收：
// Rust 客户端**自己挑**第二个角色（不是让状态机自动选第一个），
// 并断言服务端把职业/性别/等级都带过来了 —— 那是"选角界面能画对小人"的前提
// （原版按 (Job,Sex) 各有一套坐标与图号，`IntroScn.pas:1390-1429`）。
//
// 为什么这条值得单列：**手动选角是 app 的默认路径**（`set_manual_pick`），
// 而其它用例走的都是"自动选第一个" —— 那条路测不到界面真正要走的那条。
func TestProtoRustPickCharacter(t *testing.T) {
	bin, why := findE2EBin()
	if bin == "" {
		t.Skipf("跳过：%s", why)
	}

	_, store, addr := protoContractServer(t)
	seedAccount(t, store) // 造出房主"勇士"（男战士，站在 (1,1)）

	// 第二个角色：**女法师**，而且**站在别的格子** —— 于是"进世界的到底是哪一个"
	// 可以靠位置断言（第一个在 (1,1)，这个在 (3,3)）。
	second := &storage.Character{
		Account: "tester", Name: "小法", Job: 1,
		Data: &pb.CharacterData{
			Account: "tester", ChrName: "小法",
			CurMap: "0", CurX: 3, CurY: 3, Dir: uint32(entity.DirDown),
			Hair: 5, Job: 1, Sex: 1,
			Abil: &pb.Ability{Level: 9, Hp: 20, MaxHp: 20},
		},
	}
	if err := store.Characters().Create(context.Background(), second); err != nil {
		t.Fatalf("建第二个角色: %v", err)
	}

	cmd := exec.Command(bin, "world",
		"-addr", addr,
		"-account", "tester",
		"-password", "pw",
		"-pick", strconv.FormatInt(second.ID, 10),
		"-expect-map", "0",
		"-expect-pos", "3,3", // ← 只有真的进了"被选中的那个"才会是 3,3
		"-timeout-ms", "8000",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Rust 选角剧本失败：%v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"名=勇士",     // 列表里有第一个
		"名=小法",     // 也有第二个
		"职业=2",     // 法师（协议里 1 战 / 2 法 / 3 道）
		"性别=2",     // 女 ⇒ 服务端把 `Data.Sex` 带过来了（否则永远是 0/未指定）
		"选角：选 id=", // 是客户端主动选的，不是状态机自动选的
		"世界状态通过",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("输出里没有 %q：\n%s", want, text)
		}
	}
	t.Logf("Rust 选角剧本输出：\n%s", text)
}

// protoLogin 在一条连接上走完口令登录，返回**登录用的那条证明**（hex）。
//
// 抽出来是因为建/删角色的用例还要拿它当删角的"二次确认"——
// 那正是 `DeleteCharacter.password_hash` 的语义（见 `onDeleteCharacter`）。
//
// ⚠️ 不直接用 `cl.hello()`：它把 `ServerHello` 读掉了、没留 nonce，
// 而证明要绑在 nonce 上（D-24①）。
func protoLogin(t *testing.T, cl *protoClient, account, password string) string {
	t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ClientHello{ClientHello: &protocol.ClientHello{
		ProtocolVersion: protocol.Version, ClientBuild: "contract-test", Locale: "zh-CN"}}})
	nonce := cl.recv().GetServerHello().GetSessionKey()
	if len(nonce) == 0 {
		t.Fatal("握手里没给 nonce（证明要绑它）")
	}

	cl.send(&protocol.Envelope{Body: &protocol.Envelope_LoginSaltRequest{
		LoginSaltRequest: &protocol.LoginSaltRequest{Account: account}}})
	salt := cl.recv().GetLoginSalt()
	k, err := pbkdf2.Key(sha256.New, password, salt.GetSalt(),
		int(salt.GetIterations()), int(salt.GetKeyLen()))
	if err != nil {
		t.Fatalf("PBKDF2: %v", err)
	}
	proof := hex.EncodeToString(authn.ExpectedProof(k, nonce, account))
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_Login{Login: &protocol.Login{
		Account: account, PasswordHash: proof}}})
	res := cl.recv().GetLoginResult()
	if res.GetCode() != protocol.LoginCode_LOGIN_OK {
		t.Fatalf("登录该成功，实得 %v %s", res.GetCode(), res.GetMessage())
	}
	return proof
}

// 等某条回执（建/删角用的都是这个形状）。
func waitCreate(t *testing.T, cl *protoClient) *protocol.CreateCharacterResult {
	t.Helper()
	env := cl.waitFor(&protoEvents{}, "CreateCharacterResult", func(e *protocol.Envelope) bool {
		return e.GetCreateCharacterResult() != nil
	})
	return env.GetCreateCharacterResult()
}

func waitDelete(t *testing.T, cl *protoClient) *protocol.DeleteCharacterResult {
	t.Helper()
	env := cl.waitFor(&protoEvents{}, "DeleteCharacterResult", func(e *protocol.Envelope) bool {
		return e.GetDeleteCharacterResult() != nil
	})
	return env.GetDeleteCharacterResult()
}

// listChars 请求一次角色列表（服务端**不会**主动推，得自己问）。
func listChars(t *testing.T, cl *protoClient) []*protocol.CharacterSummary {
	t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_ListCharacters{
		ListCharacters: &protocol.ListCharacters{}}})
	env := cl.waitFor(&protoEvents{}, "CharacterList", func(e *protocol.Envelope) bool {
		return e.GetCharacterList() != nil
	})
	return env.GetCharacterList().GetCharacters()
}

// sendCreate 建角并回执（`name` 用参数，其余固定）。
func sendCreate(t *testing.T, cl *protoClient, name string, class protocol.CharClass, gender protocol.Gender) *protocol.CreateCharacterResult {
	t.Helper()
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_CreateCharacter{
		CreateCharacter: &protocol.CreateCharacter{
			Name: name, Class: class, Gender: gender, Hair: 2,
		}}})
	return waitCreate(t, cl)
}

// TestProtoCreateDeleteCharacter 是**建角 / 删角**的契约验收。
//
// 为什么值得单列：这两条是"新账号能不能自己玩起来"的入口 ——
// 在它之前，新号只能靠 `mir2cli` 建（客户端点了只会弹"还没接"）。
//
// 除了协议回执，这里还断言**存储层的落地细节**（初始物品、13 槽装备位、
// 出生点）：那些规矩在 `chargen` 里、legacy 与这里共用一份（R-7），
// 一旦有人把它们改歪，legacy 建的角色和新协议建的角色就不一样了。
func TestProtoCreateDeleteCharacter(t *testing.T) {
	_, store, addr := protoContractServer(t)
	seedAccount(t, store) // 账号 tester / 口令 pw，已有一个角色"勇士"

	cl := dialProto(t, addr)
	proof := protoLogin(t, cl, "tester", "pw")

	// ---- 建角：女法师 ----
	res := sendCreate(t, cl, "小法", protocol.CharClass_CHAR_CLASS_WIZARD, protocol.Gender_GENDER_FEMALE)
	if !res.GetResult().GetOk() {
		t.Fatalf("建角该成功，实得 %+v", res.GetResult())
	}
	newID := res.GetCharacterId()
	if newID == 0 {
		t.Fatal("建角成功却没给 character_id（界面上没法选它）")
	}

	// 存储层：真的落库了，而且规矩照旧
	ctx := context.Background()
	chr, err := store.Characters().GetByName(ctx, "小法")
	if err != nil {
		t.Fatalf("库里查不到新角色: %v", err)
	}
	if chr.Job != 1 || chr.Data.GetSex() != 1 {
		t.Errorf("职业/性别写错了：job=%d sex=%d", chr.Job, chr.Data.GetSex())
	}
	if len(chr.Data.GetHumItems()) != 13 {
		t.Errorf("装备位该是定长 13 槽，实得 %d", len(chr.Data.GetHumItems()))
	}
	if chr.Data.GetCurMap() == "" || chr.Data.GetHomeMap() == "" {
		t.Error("出生点没写（进游戏会没地方站）")
	}
	// ⚠️ **初始物品**（木剑、药水）这里不查：契约测试的服务器没加载物品表，
	// 那条规矩属于 `chargen`，在 `chargen_test.go` 里用假物品表单独验（分工更清楚）。

	// ---- 列表里多了一个，且摘要能挑图（选角界面靠 class/gender） ----
	chars := listChars(t, cl)
	if len(chars) != 2 {
		t.Fatalf("列表该有 2 个角色，实得 %d", len(chars))
	}
	var found *protocol.CharacterSummary
	for _, c := range chars {
		if c.GetName() == "小法" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("列表里没有新角色")
	}
	if found.GetClass() != protocol.CharClass_CHAR_CLASS_WIZARD ||
		found.GetGender() != protocol.Gender_GENDER_FEMALE {
		t.Errorf("摘要的职业/性别不对：%v %v", found.GetClass(), found.GetGender())
	}

	// ---- 重名 ----
	if got := sendCreate(t, cl, "小法", protocol.CharClass_CHAR_CLASS_WARRIOR, protocol.Gender_GENDER_MALE); got.GetResult().GetOk() {
		t.Error("重名该被拒")
	} else if got.GetResult().GetCode() != 3 {
		t.Errorf("重名该回 code=3，实得 %d", got.GetResult().GetCode())
	}

	// ---- 数量上限（每账号 2 个） ----
	if got := sendCreate(t, cl, "第三个", protocol.CharClass_CHAR_CLASS_TAOIST, protocol.Gender_GENDER_MALE); got.GetResult().GetOk() {
		t.Error("超过上限该被拒")
	} else if got.GetResult().GetCode() != 4 {
		t.Errorf("超限该回 code=4，实得 %d", got.GetResult().GetCode())
	}

	// ---- 名字不合规 ----
	if got := sendCreate(t, cl, "a b", protocol.CharClass_CHAR_CLASS_WARRIOR, protocol.Gender_GENDER_MALE); got.GetResult().GetOk() {
		t.Error("名字里有空格该被拒")
	} else if got.GetResult().GetCode() != 2 {
		t.Errorf("名字不合规该回 code=2，实得 %d", got.GetResult().GetCode())
	}

	// ---- 删角：二次确认（错的口令证明） ----
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_DeleteCharacter{
		DeleteCharacter: &protocol.DeleteCharacter{
			CharacterId: newID, PasswordHash: strings.Repeat("00", 32)}}})
	if got := waitDelete(t, cl); got.GetResult().GetOk() {
		t.Error("证明不对该被拒（删除不可恢复，这道门不能形同虚设）")
	} else if got.GetResult().GetCode() != 1 {
		t.Errorf("证明不对该回 code=1，实得 %d", got.GetResult().GetCode())
	}

	// ---- 删角：带**登录时那条证明**（绑在连接 nonce 上，重放不了） ----
	cl.send(&protocol.Envelope{Body: &protocol.Envelope_DeleteCharacter{
		DeleteCharacter: &protocol.DeleteCharacter{
			CharacterId: newID, PasswordHash: proof}}})
	if got := waitDelete(t, cl); !got.GetResult().GetOk() {
		t.Fatalf("证明正确该删成功，实得 %+v", got.GetResult())
	}

	// ---- 列表回到 1 个；存储层是**软删**（数据还在，只是 Deleted） ----
	if chars := listChars(t, cl); len(chars) != 1 {
		t.Errorf("删完该只剩 1 个，实得 %d", len(chars))
	}
	gone, err := store.Characters().GetByName(ctx, "小法")
	if err != nil {
		t.Fatalf("软删不该把行删掉（要能救回来）: %v", err)
	}
	if !gone.Deleted {
		t.Error("该是软删（Deleted=true），而不是真删")
	}
}

// `EntityMove.run` 必须如实带上 —— 客户端靠它决定播 `ActWalk` 还是 `ActRun`
// （两段图**差 64 个图号**，选错不报错、只是放另一套动作；`scene.proto` 的字段说明）。
//
// 这条不碰网络也不碰会话：`protoSink.move` 就是"造那条信封"本身，造出来看一眼字段，
// 比跑一遍完整握手再猜要稳（也能单独跑：`-run TestEntityMoveCarriesRun`）。
func TestEntityMoveCarriesRun(t *testing.T) {
	sink := &protoSink{ch: make(chan *protocol.Envelope, 4)}
	// 跑一步：5,5 → 7,5（一步 2 格）
	sink.move(42, 5, 5, 7, 5, 2, true)
	select {
	case env := <-sink.ch:
		m := env.GetEntityMove()
		if m == nil {
			t.Fatalf("该是 EntityMove，实得 %T", env.Body)
		}
		if !m.GetRun() {
			t.Error("跑一步没带 run ⇒ 客户端只能播 ActWalk（这就是用户报的\"跑起来不像跑\"）")
		}
		if m.GetEntityId() != 42 || m.GetFrom().GetX() != 5 || m.GetTo().GetX() != 7 {
			t.Errorf("字段串了：id=%d from=%v to=%v", m.GetEntityId(), m.GetFrom(), m.GetTo())
		}
	default:
		t.Fatal("没投出信封")
	}

	// 走一步：run 必须是 false（不是"没设"—— 客户端读到的就是 false）
	sink.move(42, 7, 5, 8, 5, 2, false)
	select {
	case env := <-sink.ch:
		if env.GetEntityMove().GetRun() {
			t.Error("走一步不该带 run")
		}
	default:
		t.Fatal("没投出信封")
	}
}

// 新协议入口：`[u32 长度][Envelope]`（docs/protocol.md §2）+ §5 的握手与进图序列。
//
// 与 legacy 入口的关系是**并存**（不是替换）：
//
//	legacy：`#<序号>…!` + 6bit 包  —— `session.go` 的 handleConn，服务 mir2cli 与现有回归
//	新协议：[u32 长度][Envelope]    —— 本文件，默认**不监听**（`-proto-addr` 为空即关）
//
// 落点见 protocol.md §11 待办与 plan.md §6「A 服务端 协议改造」。
//
// # v0 的边界（都在 protocol.md §11 记了待办）
//
//   - **不实现 `Login`**：`Login.password_hash` 到底是什么（客户端预哈希？挑战应答？）
//     还没有定论，而它依赖 accountsvc 的接入（D-13 的内部 RPC）。所以 v0 的入口是
//     `Reconnect`：拿**已有会话号**认领会话 —— 这条路径不会白写，§5 的重连语义就是它，
//     将来 `LoginResult` 签发的 `session_token` 只会替换掉 v0 这层"会话号即 token"的编码。
//   - 只做到"进图 + 看见自己"：`EnterWorld` 初始快照 + `AbilityUpdate`。
//     移动/攻击/物品等一律先走 legacy 入口。
//   - 进图的身份仍由 **accountsvc 建立的会话**提供（真实口令校验在那里）；
//     本入口不重复实现登录生命周期，只接管"选角 → 进世界"这一段。
package gamesvr

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/frame"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

const (
	// protoIdleTimeout 是新协议连接的空闲上限（与 legacy 的 readTimeout 同值）。
	protoIdleTimeout = 30 * time.Minute
	// protoHandshakeTimeout 只用于**等 ClientHello**：握手是接入面的第一份输入，
	// 不能让"连上就不说话"的连接无限期挂着（与 PROXY 头那个超时同理）。
	protoHandshakeTimeout = 10 * time.Second
	// protoStoreTimeout 是单次查库的上限（会话/角色都在这里读）。
	protoStoreTimeout = 5 * time.Second
	// protoTicketTTL 是选角时开出的"进游戏凭证"有效期。
	// 之所以要有期限：它消费一次即作废，但**没被消费**的那些必须自己过期，
	// 否则一条"选完角就不来了"的连接会给这个角色留下永久可用的凭证。
	protoTicketTTL = 2 * time.Minute

	// ServerError.code（**非致命**错误，连接保留）——与 Disconnect.code（1001…）是两个命名空间。
	protoErrHandshake  = 1 // 握手：首包不是 ClientHello，或协议版本不符
	protoErrOutOfOrder = 2 // 消息顺序不对：还没认领会话 / 还没进世界
	protoErrInternal   = 3 // 服务端内部错误（查库失败等）
)

// protoDown 是新协议连接的**下行守卫**。
//
// 为什么需要：这名玩家会作为世界公民注册进 `world.players`（这样他才有 ActorId、
// 才在空间索引里、后来的移动/战斗/视野逻辑才能原样复用）。代价是 legacy 的广播路径
// （`s.send` → `wire.EncodeDown`）也可能往这条连接写 —— 那些 6bit 帧会让新协议客户端
// 解析错乱。所以：**legacy 下行全部丢弃**，新协议的下行自己写（不走 Write）。
//
// 读、关闭、Deadline 一律直达底层连接 —— 其余部分把它当普通 net.Conn 用即可。
type protoDown struct{ net.Conn }

func (protoDown) Write(b []byte) (int, error) { return len(b), nil }

// protoSession 是一条新协议连接的状态机。
type protoSession struct {
	srv      *Server
	raw      net.Conn
	rd       *bufio.Reader
	clientIP string

	// rec 是 Reconnect 认领到的会话；player 是进世界之后的在线对象。
	rec    *storage.SessionRecord
	player *Player

	// snapReq 是自动存档的投递通道（容量 1，与 legacy 同一套机制）。
	// 在**注册进 world.players 之前**挂到 player 上，见 enterWorld。
	snapReq chan chan *storage.Character

	// unknown 统计"不认识/未实现"的消息条数（§4.1 硬规则 4：记数 + 告警 + 忽略）。
	unknown int
}

// serveProtoConn 处理一条新协议连接（由 run.go 的 accept 循环调用）。
func (s *Server) serveProtoConn(c net.Conn, clientIP string) {
	ps := &protoSession{
		srv:      s,
		raw:      c,
		rd:       bufio.NewReader(c),
		clientIP: clientIP,
		snapReq:  make(chan chan *storage.Character, 1),
	}
	defer c.Close()
	// 进过世界的玩家必须走**同一套**下线收尾（最终存档 + 释放角色租约）。
	defer func() {
		if ps.player != nil {
			s.removePlayer(ps.player)
		}
	}()

	if !ps.handshake() {
		return
	}

	// 收帧放在独立 goroutine 上（与 legacy handleConn 同一形状），主 goroutine 只做派发。
	//
	// 为什么必须拆：自动存档线程会经 `snapReq` 要求"在**本人 goroutine** 上做一份快照"，
	// 而快照只有在唯一写者（这条派发循环）上做才天然一致。派发循环若阻塞在 Read 上，
	// 这个请求就只能超时 ⇒ 新协议玩家永远不会被自动存档。
	inbox := make(chan *protocol.Envelope, 256)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_ = ps.raw.SetReadDeadline(time.Now().Add(protoIdleTimeout))
			env, err := frame.Read(ps.rd)
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
					log.Printf("%s: 新协议读帧失败: %v", ps.clientIP, err)
				}
				close(inbox)
				return
			}
			select {
			case inbox <- env:
			case <-done:
				return
			}
		}
	}()

	for {
		select {
		case env, ok := <-inbox:
			if !ok {
				return
			}
			if !ps.dispatch(env) {
				return
			}
		case reply := <-ps.snapReq:
			// player 尚未进世界时为 nil ⇒ 存档线程跳过（与 legacy 的同一处理）。
			reply <- saveSnapshotOf(ps.player)
		}
	}
}

// handshake 完成 §5 的握手：首包必须是 `ClientHello`，版本不符**立即断开**。
func (ps *protoSession) handshake() bool {
	_ = ps.raw.SetReadDeadline(time.Now().Add(protoHandshakeTimeout))
	env, err := frame.Read(ps.rd)
	if err != nil {
		log.Printf("%s: 新协议握手读失败: %v", ps.clientIP, err)
		return false
	}
	if _, err := frame.CheckHello(env, protocol.Version); err != nil {
		// §5：不做"尽力而为"。两端版本不一致还继续跑，只会变成难以定位的静默错乱；
		// 所以回一条 ServerError 说明原因，然后断开。
		_ = ps.send(&protocol.Envelope{Body: &protocol.Envelope_ServerError{
			ServerError: &protocol.ServerError{Code: protoErrHandshake, Message: err.Error()}}})
		log.Printf("%s: 新协议握手被拒: %v", ps.clientIP, err)
		return false
	}

	// `session_key` 先给一个随机 nonce：v0 不用它做校验，但握手里带上它，
	// 是为将来"口令挑战应答"（`Login.password_hash` 的定论）留的位置。
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("%s: 生成握手 nonce 失败: %v", ps.clientIP, err)
		return false
	}
	// capabilities 目前只报一条：v0 的能力面就是"进图快照"。
	if err := ps.send(frame.NewServerHello(protocol.Version, nonce, []string{"enter-world"})); err != nil {
		return false
	}
	log.Printf("%s: 新协议握手完成（版本 %d）", ps.clientIP, protocol.Version)
	return true
}

// dispatch 派发一条信封。返回 false 表示断开。
//
// ⚠️ 每个消息单独 recover：一条畸形消息（或一个空指针 bug）不该把整个游戏服带走
// —— 那会让**所有**在线玩家瞬间掉线。与 legacy 的 dispatch 同一条纪律。
func (ps *protoSession) dispatch(env *protocol.Envelope) (keep bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("%s: 处理新协议消息 %s 时 panic: %v（断开）",
				ps.clientIP, frame.MsgName(env), r)
			keep = false
		}
	}()

	switch body := env.Body.(type) {
	case *protocol.Envelope_Ping:
		return ps.send(frame.NewPong(body.Ping.GetClientTimeMs())) == nil
	case *protocol.Envelope_Reconnect:
		return ps.onReconnect(body.Reconnect)
	case *protocol.Envelope_ListCharacters:
		return ps.onListCharacters()
	case *protocol.Envelope_SelectCharacter:
		return ps.onSelectCharacter(body.SelectCharacter)
	default:
		// ClientHello（重复发）也走这里 —— 握手之后它不再有意义，按"不认识"处理。
		ps.noteUnknown(env)
		return true
	}
}

// noteUnknown 处理"不认识 / 未实现"的消息（§4.1 硬规则 4）。
//
// ⚠️ 禁止 panic，也**不断开**：收到未知消息是版本演进时的正常现象（新客户端有新消息）。
// 但必须记数 + 告警 —— 否则"客户端一直在发一条被我们忽略的消息"会完全无声。
func (ps *protoSession) noteUnknown(env *protocol.Envelope) {
	ps.unknown++
	if ps.unknown > 3 && ps.unknown%100 != 0 {
		return // 只记头几条与每百条，免得一条 1Hz 的消息把日志刷满
	}
	name := frame.MsgName(env)
	if name == "" {
		name = "未知（oneof case 未设置）"
	}
	log.Printf("%s: 忽略未实现的新协议消息 %s（本连接累计 %d 条）", ps.clientIP, name, ps.unknown)
}

// onReconnect 用会话号认领会话（v0 的入口；语义与 §5 的重连一致）。
//
// ⚠️ v0 的 `session_token` 就是 4 字节小端会话号 —— 这是**临时编码**：
// 正式 token 应由 accountsvc 在登录后签发（不可猜测、可换发、可失效），见 protocol.md §11。
func (ps *protoSession) onReconnect(m *protocol.Reconnect) bool {
	if ps.rec != nil {
		// 已经认领过：当幂等回执，不重复认领（也绝不再申请一次租约）。
		return ps.sendReconnectResult(protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT) == nil
	}
	tok := m.GetSessionToken()
	if len(tok) != 4 {
		return ps.failReconnect(fmt.Sprintf("session_token 长度非法（%d 字节）", len(tok)))
	}
	sid := int32(binary.LittleEndian.Uint32(tok))

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	rec, err := ps.srv.store.Sessions().Get(ctx, sid)
	if err != nil {
		return ps.failReconnect(fmt.Sprintf("会话 %d 不存在", sid))
	}
	now := time.Now()
	if !rec.ExpiresAt.After(now) {
		return ps.failReconnect("会话已过期")
	}
	ps.rec = rec

	// §5 的重连语义：租约仍在 ⇒ 直接回世界；否则回落到选角阶段。
	// 「租约仍在」的判据 = 这条会话还是该账号的当前登录者，且已经选好角色。
	status := protocol.ReconnectStatus_RECONNECT_BACK_TO_SELECT
	if rec.Stage == sessionStageSelected && rec.CharacterName != "" {
		if ok, err := ps.srv.store.Sessions().IsCurrent(ctx, rec.Account, sid, now); err == nil && ok {
			status = protocol.ReconnectStatus_RECONNECT_OK_IN_WORLD
		}
	}
	if err := ps.sendReconnectResult(status); err != nil {
		return false
	}
	if status != protocol.ReconnectStatus_RECONNECT_OK_IN_WORLD {
		log.Printf("%s: 新协议认领会话 %d（账号 %s）→ 回选角", ps.clientIP, sid, rec.Account)
		return true
	}
	// 回世界：与"选完角"是同一条尾巴。
	chr, err := ps.srv.store.Characters().GetByName(ctx, rec.CharacterName)
	if err != nil || chr.Deleted {
		return ps.failReconnect(fmt.Sprintf("角色 %s 不可用", rec.CharacterName))
	}
	return ps.enterWorld(chr)
}

// failReconnect 回一条 FAILED 并断开：token 无效时**没有**可继续的状态。
func (ps *protoSession) failReconnect(reason string) bool {
	log.Printf("%s: 新协议重连失败: %s", ps.clientIP, reason)
	_ = ps.sendReconnectResult(protocol.ReconnectStatus_RECONNECT_FAILED)
	_ = ps.send(&protocol.Envelope{Body: &protocol.Envelope_Disconnect{
		Disconnect: &protocol.Disconnect{Code: 1004, Reason: reason}}})
	return false
}

func (ps *protoSession) sendReconnectResult(s protocol.ReconnectStatus) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_ReconnectResult{
		ReconnectResult: &protocol.ReconnectResult{Status: s}}})
}

// onListCharacters 列出该账号未删除的角色（选角列表）。
func (ps *protoSession) onListCharacters() bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	chars, err := ps.srv.store.Characters().ListByAccount(ctx, ps.rec.Account)
	if err != nil {
		log.Printf("%s: 列角色失败: %v", ps.clientIP, err)
		return ps.sendServerError(protoErrInternal, "读取角色列表失败") == nil
	}
	out := &protocol.CharacterList{}
	for _, ch := range chars {
		if ch.Deleted {
			continue
		}
		out.Characters = append(out.Characters, characterSummary(ch))
	}
	log.Printf("%s: 新协议列角色 → %d 个", ps.clientIP, len(out.Characters))
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_CharacterList{
		CharacterList: out}}) == nil
}

// onSelectCharacter 选角：**这里申请角色租约**（§5）。
//
// 租约被占时**明确拒绝**（`SELECT_CHAR_LEASE_HELD`），不做"顶号"式静默踢人（D-13）。
func (ps *protoSession) onSelectCharacter(m *protocol.SelectCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	if ps.player != nil {
		// 已经在世界里：幂等回执（重复发不重复进图）。
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_OK, "", m.GetCharacterId()) == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gameLeaseWait+5*time.Second)
	defer cancel()

	chr, err := ps.findCharacter(ctx, m.GetCharacterId())
	if err != nil {
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_NOT_FOUND, err.Error(), 0) == nil
	}

	// 会话推进到"已选好角色"：`IssueGameTicket`/`ClaimGameLease` 在存储层都要求
	// `stage=4 且 character_name=本人`（见 storage/sqlite）。这一步在**新协议里
	// 本来就归 gamesvr**（D-13：它是客户端的唯一对端），只是 accountsvc 里也有一份
	// 等价的推进逻辑，将来随内部 RPC 一起收敛。
	rec := *ps.rec
	rec.Stage = sessionStageSelected
	rec.CharacterName = chr.Name
	if err := ps.srv.store.Sessions().Activate(ctx, &rec); err != nil {
		log.Printf("%s: 选角推进会话失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_SERVER_BUSY,
			"会话已失效，请重新登录", 0) == nil
	}
	ps.rec = &rec

	if err := ps.srv.store.Sessions().IssueGameTicket(ctx, rec.Account, rec.SessionID,
		chr.Name, time.Now(), time.Now().Add(protoTicketTTL)); err != nil {
		log.Printf("%s: 选角开票失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_SERVER_BUSY, "开票失败", 0) == nil
	}

	if err := ps.srv.claimGameLease(ctx, rec.Account, rec.SessionID, chr.Name); err != nil {
		log.Printf("%s: 选角申请角色租约失败: %v", ps.clientIP, err)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_LEASE_HELD, err.Error(), 0) == nil
	}

	// 取得租约后再读一次角色：旧连接释放租约前会做最终存档，早先那份可能已经过时。
	chr, err = ps.srv.store.Characters().GetByName(ctx, chr.Name)
	if err != nil || chr.Deleted {
		_ = ps.srv.store.Sessions().ReleaseGameLease(ctx, rec.Account, rec.SessionID, chr.Name)
		return ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_NOT_FOUND, "角色不可用", 0) == nil
	}
	if err := ps.sendSelectResult(protocol.SelectCharCode_SELECT_CHAR_OK, "", uint64(chr.ID)); err != nil {
		return false
	}
	return ps.enterWorld(chr)
}

// enterWorld 把角色放进世界，并发 §5 的最后一段：`← EnterWorld{初始快照}`。
func (ps *protoSession) enterWorld(chr *storage.Character) bool {
	s := ps.srv
	p := s.joinWorld(chr, ps.rec.SessionID, ps.clientIP)
	// legacy 下行必须被丢弃（见 protoDown）；读/关闭照常。
	p.conn = protoDown{ps.raw}
	// ⚠️ 在**注册进 world.players 之前**挂上快照通道：自动存档线程之后会读它
	// （在 s.mu 下取出玩家列表后再用），这次赋值与那次读之间由 s.mu 建立 happens-before。
	p.snapReq = ps.snapReq

	s.mu.Lock()
	s.world.players[p.Obj.ID] = p
	s.world.index.Add(p)
	s.mu.Unlock()
	ps.player = p

	// 快照：先在锁内/lock-free 读出一份，再发（发包不带世界锁）。
	env := &protocol.Envelope{Body: &protocol.Envelope_EnterWorld{EnterWorld: &protocol.EnterWorld{
		SelfEntityId: uint64(p.Obj.ID),
		// ⚠️ `map_id` 暂置 0：地图在本项目是**按名字**索引的（D-22，容器里就是 `<名字>.map`），
		// 新协议的 `map_id` 语义还没定（见 protocol.md §11 待办）。客户端请用 `map_name`。
		MapId:      0,
		MapName:    p.Obj.MapRef().Name,
		Position:   &protocol.Vec2{X: int32(p.Obj.PosX()), Y: int32(p.Obj.PosY())},
		Direction:  directionOf(p.Obj.Facing()),
		Entities:   s.entitySnapshot(p),
		ServerTick: uint32(time.Now().UnixMilli()),
	}}}
	if err := ps.send(env); err != nil {
		return false
	}
	// 自身能力值（客户端要画血条/负重条/经验条）。
	if err := ps.send(&protocol.Envelope{Body: &protocol.Envelope_AbilityUpdate{
		AbilityUpdate: &protocol.AbilityUpdate{Ability: protocolAbility(chr.Data.Abil, chr.Data.Gold)}}}); err != nil {
		return false
	}

	// 让 legacy 那半边也知道他来了：世界的可见性账本（`p.visible`）也要填上，
	// 否则他"看得见的实体"与"后续广播的判据"会是两个集合。
	// 其中发给**他自己**的那些 legacy 包会被 protoDown 丢掉（正是它存在的理由）。
	s.updateVision(p)
	log.Printf("%s: %s 进图（新协议 ActorId=%d 地图=%s 坐标=(%d,%d)）",
		ps.clientIP, p.Char.Name, p.Obj.ID, env.GetEnterWorld().MapName,
		p.Obj.PosX(), p.Obj.PosY())
	return true
}

// findCharacter 按角色 id 找角色（`CharacterStore` 只有按名字/按账号两个入口）。
func (ps *protoSession) findCharacter(ctx context.Context, id uint64) (*storage.Character, error) {
	chars, err := ps.srv.store.Characters().ListByAccount(ctx, ps.rec.Account)
	if err != nil {
		return nil, fmt.Errorf("读取角色列表失败")
	}
	for _, ch := range chars {
		if uint64(ch.ID) == id && !ch.Deleted {
			return ch, nil
		}
	}
	return nil, fmt.Errorf("角色 %d 不属于该账号", id)
}

// entitySnapshot 收集 p 视野内的实体快照（玩家 + 怪物/NPC），用于进图的初始快照。
//
// ⚠️ 过滤条件必须与 `updateVision` **逐条一致**（同图 + 切比雪夫距离 ≤ viewRange，
// 怪物还要排除尸体）—— 否则"进图看得见的"与"走一步看得见的"会是两个集合。
// ⚠️ 字段一律取**对象自带锁**的快照（`Place`/`Appearance`）：它们可能正被对方的
// goroutine 改（审计 P1-5）。
func (s *Server) entitySnapshot(p *Player) []*protocol.EntityState {
	px, py := p.Obj.PosX(), p.Obj.PosY()
	r := s.cfg.viewRange

	s.mu.RLock()
	var players []*Player
	var monsters []*entity.Monster
	for _, o := range s.world.index.InRange(px, py, r) {
		other, ok := o.(*Player)
		if !ok || other == p {
			continue
		}
		if other.Obj.MapRef() == p.Obj.MapRef() && other.Obj.Distance(px, py) <= r {
			players = append(players, other)
		}
	}
	for _, o := range s.world.monsterIdx.InRange(px, py, r) {
		m, ok := o.(*entity.Monster)
		if !ok || m.IsDead() {
			continue
		}
		if m.MapRef() == p.Obj.MapRef() && m.Distance(px, py) <= r {
			monsters = append(monsters, m)
		}
	}
	s.mu.RUnlock()

	out := make([]*protocol.EntityState, 0, len(players)+len(monsters))
	for _, other := range players {
		out = append(out, playerState(other))
	}
	for _, m := range monsters {
		out = append(out, monsterState(m))
	}
	return out
}

// playerState 取一名玩家的实体快照。
//
// ⚠️ 刻意**不读** `p.Char`（血量/能力值都躺在那儿）：那份存档由**该玩家自己的
// goroutine** 拥有，"别人的 goroutine 读它"就是 data race。原版 `SM_TURN` 也不带血量
// （血量靠受击/血条事件单独下发），这里照做。
func playerState(p *Player) *protocol.EntityState {
	_, x, y, dir := p.Obj.Place()
	feature, status := p.Obj.Appearance()
	st := &protocol.EntityState{
		EntityId:   uint64(p.Obj.ID),
		Kind:       0, // 0=玩家
		Name:       p.Obj.Name,
		Position:   &protocol.Vec2{X: int32(x), Y: int32(y)},
		Direction:  directionOf(dir),
		Feature:    featureOf(feature),
		StatusBits: uint64(uint32(status)),
	}
	return st
}

// monsterState 取一只怪物/NPC 的实体快照。
func monsterState(m *entity.Monster) *protocol.EntityState {
	_, x, y, dir := m.Place()
	kind := uint32(1) // 1=怪物
	if m.IsNPC {
		kind = 2 // 2=NPC（复用 Monster 承载，见 entity/monster.go）
	}
	maxHP := uint32(0)
	if m.Info != nil {
		maxHP = m.Info.HP
	}
	return &protocol.EntityState{
		EntityId:  uint64(m.ID),
		Kind:      kind,
		Name:      m.Name, // ⚠️ 宠物名的"(主人名)"装饰在 s.showName，属显示策略，留给后续的实体更新路径
		Position:  &protocol.Vec2{X: int32(x), Y: int32(y)},
		Direction: directionOf(dir),
		Feature:   featureOf(m.FeatureBits()),
		Hp:        m.HPValue(),
		MaxHp:     maxHP,
	}
}

// directionOf 把原版朝向（0..7，上起顺时针）转成新协议的 `Direction`。
//
// 新枚举就是"原版 + 1"（见 common.proto 的注释与 netproto_test 里那条钉子测试）——
// 之所以不是恒等映射：0 在新协议里保留给"未指定"，而原版的 0 是"上"。
func directionOf(dir uint8) protocol.Direction {
	if dir > entity.DirUpLeft {
		return protocol.Direction_DIRECTION_UNSPECIFIED
	}
	return protocol.Direction(dir + 1)
}

// featureOf 把原版**打包**的外观位域拆成新协议的显式字段。
//
// ⚠️ 这是我们自己过去的坑留下的桥：`entity.Object` 存的是 `proto.MakeFeature` 那套
// 16 位打包（raceImg/weapon/hair/dress 各一字节）。新协议刻意**不**用位掩码
// （common.proto：位掩码是原版 bug 的来源之一），所以这里拆一次。
// 等实体层不再打包（把四个字段直接存）时，这个函数就该删掉。
func featureOf(f int32) *protocol.EntityFeature {
	return &protocol.EntityFeature{
		RaceImg: uint32(proto.FeatureRace(f)),
		Weapon:  uint32(proto.FeatureWeapon(f)),
		Hair:    uint32(proto.FeatureHair(f)),
		Dress:   uint32(proto.FeatureDress(f)),
	}
}

// characterSummary 把存档转成选角列表要的摘要（只带选角需要的几项，不带整份存档）。
func characterSummary(ch *storage.Character) *protocol.CharacterSummary {
	sum := &protocol.CharacterSummary{
		CharacterId: uint64(ch.ID),
		Name:        ch.Name,
		Level:       ch.Level,
	}
	if ch.Data != nil {
		// 职业：存档里是 0/1/2（战/法/道），新协议是 1/2/3。
		sum.Class = protocol.CharClass(ch.Data.Job + 1)
		// ⚠️ 1.76 里"性别"由发型编码（`GenderHair` 就是它）⇒ 不单独猜一个 gender 值。
		sum.GenderHair = ch.Data.Hair
	}
	return sum
}

// protocolAbility 把存档里的能力值转成新协议的 `Ability`。
//
// ⚠️ 与 legacy 的 `abilityFromPB`（send.go）是**同一份语义的两条出口**：
// 那边要压进 16 位与位域，这边是 typed 字段。改一边必须改另一边。
func protocolAbility(a *pb.Ability, gold int64) *protocol.Ability {
	if a == nil {
		return &protocol.Ability{Level: 1, Gold: uint64(gold)}
	}
	pair := func(m *pb.MinMax) (uint32, uint32) {
		if m == nil {
			return 0, 0
		}
		return uint32(m.Min), uint32(m.Max)
	}
	dcMin, dcMax := pair(a.Dc)
	mcMin, mcMax := pair(a.Mc)
	scMin, scMax := pair(a.Sc)
	// ⚠️ 1.76 的 AC/MAC 也是 (min,max) 对偶，而新协议的 `ac`/`mac` 是**单值** ——
	// 这里先取 min（旧协议把它们打包成一个 uint32 发出去，客户端再拆）。
	// 该不该在新协议里拆成两个字段，等 combat 落地时一并定（protocol.md §11 待办）。
	acMin, _ := pair(a.Ac)
	macMin, _ := pair(a.Mac)
	return &protocol.Ability{
		DcMin: dcMin, DcMax: dcMax,
		McMin: mcMin, McMax: mcMax,
		ScMin: scMin, ScMax: scMax,
		Ac: acMin, Mac: macMin,
		Hp: uint32(a.Hp), Mp: uint32(a.Mp),
		MaxHp: uint32(a.MaxHp), MaxMp: uint32(a.MaxMp),
		Weight:    uint32(a.Weight),
		MaxWeight: uint32(a.MaxWeight),
		// ⚠️ `MaxWearWeight` 要夹到 255 才发（原版 ObjBase.pas:25086，见 send.go 的 minU32）。
		WearWeight:    uint32(a.WearWeight),
		MaxWearWeight: minU32(a.MaxWearWeight, 255),
		Level:         uint32(a.Level),
		Gold:          uint64(gold),
	}
}

// ---------- 发送与拒绝 ----------

// send 写一条信封（带写超时；写不动就断开，与 legacy 的慢消费者策略一致）。
func (ps *protoSession) send(env *protocol.Envelope) error {
	_ = ps.raw.SetWriteDeadline(time.Now().Add(sendTimeout))
	if err := frame.Write(ps.raw, env); err != nil {
		log.Printf("%s: 新协议发帧失败: %v", ps.clientIP, err)
		return err
	}
	return nil
}

func (ps *protoSession) sendServerError(code uint32, msg string) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_ServerError{
		ServerError: &protocol.ServerError{Code: code, Message: msg}}})
}

// rejectOutOfOrder 回一条"顺序不对"的 ServerError（连接保留：客户端还能补上）。
func (ps *protoSession) rejectOutOfOrder(reason string) bool {
	log.Printf("%s: 新协议消息顺序不对: %s", ps.clientIP, reason)
	return ps.sendServerError(protoErrOutOfOrder, reason) == nil
}

func (ps *protoSession) sendSelectResult(code protocol.SelectCharCode, msg string, id uint64) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_SelectCharacterResult{
		SelectCharacterResult: &protocol.SelectCharacterResult{
			Code: code, Message: msg, CharacterId: id}}})
}

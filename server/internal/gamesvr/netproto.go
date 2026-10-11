// 新协议 transport/dispatch 入口：`[u32 长度][Envelope]`（docs/protocol.md §2）。
// 业务 handler 按域拆在 `netproto_{auth,character,scene,world,npc,item,shop}.go`。
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
//   - **`Login` 已实现**（2026-10-08，D-24① 挑战应答）：两步往返，服务端始终不知道口令。
//     与 `Reconnect` 的分工：`Login` 是"第一次来"，`Reconnect` 是"带着已经签发的会话号回来"。
//     两条路之后走的是**同一条尾巴**（选角 → 进世界）。见 onLoginSaltRequest / onLogin。
//   - `accountsvc` 那条 legacy 登录路径仍然在（服务旧客户端与 mir2cli）；
//     口令策略与校验原语两边共用 `internal/authn` 那一份。
//   - 已做到：进图快照 + 能力值 + **实时实体事件**（出现/消失/移动，出站）与
//     `MoveInput`（入站：走一步 / 限流 / 被挡回权威位置）。
//   - 未做到：攻击 / 物品 / 聊天 / 技能仍走 legacy 入口 —— 那些 legacy 下行会被
//     `protoDown` 丢掉（新协议客户端暂时看不见背包、聊天与伤害数字）。
package gamesvr

import (
	"bufio"
	"crypto/rand"
	"errors"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

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
	// loginSessionTTL 是登录后签发的会话寿命。
	//
	// ⚠️ 与 `accountsvc.loginSessionTTL` 同值（10 分钟）—— 两边各存一份迟早漂，
	// 该搬到一个共享常量（记在 protocol.md §11）。
	loginSessionTTL = 10 * time.Minute

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

// protoSink 是"世界 → 新协议玩家"的**唯一出口**（实体事件：出现 / 消失 / 移动）。
//
// ⚠️ 为什么走 channel 而不是直接写 socket：
//
//  1. 这些调用来自**别人的 goroutine**（怪物 AI 的 ticker、其他玩家的移动路径）。
//     直接写会与"会话自己的写"交错 ⇒ 帧字节互相插进对方中间，整条流就废了；
//  2. 慢客户端会把**别人的 goroutine** 按住（正是审计 P1-6 记的那条：
//     "持世界锁写 socket" 的同类 —— 只不过这里持的是怪物 ticker 的时间）。
//
// ⚠️ 容量满时**丢最旧的**（不是丢新的）：按 protocol.md §6 的语义分类，
// 出现/消失是幂等状态同步、移动是可丢弃的高频位置 —— 两者都是"新值覆盖旧值"，
// 丢旧保新才能让客户端尽快追上权威状态。丢弃计数进日志，不静默。
type protoSink struct {
	ch      chan *protocol.Envelope
	ps      *protoSession
	dropped atomic.Uint64
}

// sinkBuf 是每名新协议玩家的下行缓冲。256 条足够覆盖"一屏实体同时动"，
// 真填满了说明客户端已经读不动了（那时丢最旧的正是想要的反应）。
const sinkBuf = 256

func newProtoSink(ps *protoSession) *protoSink {
	return &protoSink{ch: make(chan *protocol.Envelope, sinkBuf), ps: ps}
}

// enqueue 投递一条；缓冲满时挤掉最旧的一条。
func (k *protoSink) enqueue(env *protocol.Envelope) {
	select {
	case k.ch <- env:
		return
	default:
	}
	select { // 腾一格：丢掉最旧的
	case <-k.ch:
		if n := k.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Printf("%s: 新协议下行积压，已丢弃 %d 条旧消息（客户端读得比世界变得慢）",
				k.ps.clientIP, n)
		}
	default:
	}
	select {
	case k.ch <- env:
	default:
	}
}

// appear 发一条实体出现（玩家/怪物/NPC 一律如此）。
func (k *protoSink) appear(e *protocol.EntityState) {
	if e == nil {
		return
	}
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EntityAppear{
		EntityAppear: &protocol.EntityAppear{Entity: e}}})
}

// disappear 发一条实体消失。
//
// ⚠️ 原版的"消失"有几种原因（离开视野/隐身/死亡/下线），但 legacy 的 `sendDisappear`
// 只带 (id, x, y) ⇒ 这一层**分辨不出来**，统一报 LEFT_VIEW。
// 要精确原因得把理由从调用点一路传进来（见 protocol.md §11 待办）。
func (k *protoSink) disappear(id uint32) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EntityDisappear{
		EntityDisappear: &protocol.EntityDisappear{
			EntityId: uint64(id),
			Reason:   protocol.DisappearReason_DISAPPEAR_LEFT_VIEW,
		}}})
}

// ---------- 动作 id（`EntityAction.action` 的值域）----------
//
// ⚠️ 这个值域是**我们定的**：原版把它拆成 70 个手写动画类 + 十几个消息号
// （SM_HIT/SM_HEAVYHIT/…），没法照搬。取值规则两条：
//
//  1. **1..8 与 `AttackAction` 同值**：客户端收到 `EntityAction{action: 1}` 与
//     自己发 `AttackInput{action: ATTACK_HIT}` 播的是同一套动作，不必再翻译一次；
//  2. 其余从 **51** 起（避开 0 与 kind 那种 0..2 的小值域，一眼能看出不是攻击）。
//
// 表见 docs/protocol.md §9.5。
const (
	// actionHurt 是"受击"（被打了一下）。
	actionHurt = 51
	// actionDeath 是"死亡"（尸骨留在原地；移出视野另有一条 EntityDisappear）。
	actionDeath = 52
)

// attackActionOf 把 legacy 的挥砍消息号翻成动作 id（见上面的值域说明）。
func attackActionOf(ident uint16) uint32 {
	switch ident {
	case proto.SM_HEAVYHIT:
		return 2
	case proto.SM_BIGHIT:
		return 3
	case proto.SM_POWERHIT:
		return 4
	case proto.SM_LONGHIT:
		return 5
	case proto.SM_WIDEHIT:
		return 6
	case proto.SM_FIREHIT:
		return 7
	case proto.SM_TWINHIT:
		return 8
	}
	return 1 // SM_HIT
}

// action 发一条动作（挥砍/受击/死亡…）。
func (k *protoSink) action(id uint32, what uint32) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EntityAction{
		EntityAction: &protocol.EntityAction{
			EntityId:   uint64(id),
			Action:     what,
			ServerTick: uint32(time.Now().UnixMilli()),
		}}})
}

// damage 发一条伤害（客户端据此弹伤害数字）。
//
// `attackerID` 允许为 0（火墙/毒/脚本这类"没有具体出手者"的伤害）——
// 与 `Death.killer_id` 的约定一致。
func (k *protoSink) damage(attackerID, targetID uint32, value int32, flags uint32) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_Damage{
		Damage: &protocol.Damage{
			AttackerId: uint64(attackerID),
			TargetId:   uint64(targetID),
			Value:      value,
			Flags:      flags,
		}}})
}

// health 同步一条血量（自己或视野内的实体）。
func (k *protoSink) health(id uint32, hp, maxHP uint32) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EntityHealth{
		EntityHealth: &protocol.EntityHealth{
			EntityId: uint64(id),
			Hp:       hp,
			MaxHp:    maxHP,
		}}})
}

// death 发一条死亡。
func (k *protoSink) death(id, killerID uint32) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_Death{
		Death: &protocol.Death{EntityId: uint64(id), KillerId: uint64(killerID)}}})
}

// ability 发一条完整能力值（自己的 hp/mp/等级/金币）。
//
// 用途：`EntityHealth` 只覆盖 hp/max_hp（见 `sendHealthChanged` 的说明），
// 所以自己的 mp 变化靠这条捎带。
func (k *protoSink) ability(ab *pb.Ability, gold int64) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_AbilityUpdate{
		AbilityUpdate: &protocol.AbilityUpdate{Ability: protocolAbility(ab, gold)}}})
}

// bag 发一条**全量背包**（新协议 `BagItems`）。
//
// ⚠️ 用户 2026-10-09 报"包裹负重/经验比例没显示"时，背后的一件事就是这条链从来没接：
// `sendBagItems` 只发 legacy，而 proto 玩家的 legacy 下行会被 `protoDown` 丢掉
// ⇒ 新协议客户端**永远看不到背包**（协议里 `BagItems` 早就定义好了，只是没人构造它）。
// 现在 `sendBagItems` 在同一次遍历里攒出这份结构化列表一起发（见 `docs/decisions.md` D-65）。
//
// 槽位约定：**按槽位顺序、尾部空槽省略**（服务端的背包本来就是"紧凑前缀 + 空尾巴"，
// 见 `takeBagItemLocked`）⇒ 客户端把第 i 项放进第 i 格就是对的。
func (k *protoSink) bag(items []*protocol.ItemStack) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_BagItems{
		BagItems: &protocol.BagItems{Items: items}}})
}

// equip 发一条**已穿戴**（`EquippedItems`）。
//
// 与背包不同：装备槽的**位置本身有意义**（武器/衣服/项链…），所以空槽要占位
// （发 `index = 0` 的 `ItemStack`），客户端按下标认槽。
func (k *protoSink) equip(items []*protocol.ItemStack) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EquippedItems{
		EquippedItems: &protocol.EquippedItems{Items: items}}})
}

// exp 发一条经验获得（只有自己会收到）。
func (k *protoSink) exp(amount, total uint64) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_ExperienceGain{
		ExperienceGain: &protocol.ExperienceGain{Amount: amount, Total: total}}})
}

// levelUp 发一条升级（带升级后的完整能力值 —— 客户端据此重算血条/负重）。
func (k *protoSink) levelUp(level uint32, ab *pb.Ability, gold int64) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_LevelUp{
		LevelUp: &protocol.LevelUp{Level: level, Ability: protocolAbility(ab, gold)}}})
}

// move 发一条权威移动（含"原地转身"：from == to，只是朝向变了）。
//
// ⚠️ 新协议里**没有**独立的"转身"消息，所以转身也用 EntityMove 表达
// （客户端看到 from == to 就只更新朝向）。补一条专用的动作/转身消息
// 记在 protocol.md §11。
func (k *protoSink) move(id uint32, fromX, fromY, toX, toY int, dir uint8, run bool) {
	k.enqueue(&protocol.Envelope{Body: &protocol.Envelope_EntityMove{
		EntityMove: &protocol.EntityMove{
			EntityId:   uint64(id),
			From:       &protocol.Vec2{X: int32(fromX), Y: int32(fromY)},
			To:         &protocol.Vec2{X: int32(toX), Y: int32(toY)},
			Direction:  directionOf(dir),
			ServerTick: uint32(time.Now().UnixMilli()),
			Run:        run,
		}}})
}

// protoSession 是一条新协议连接的状态机。
type protoSession struct {
	srv      *Server
	raw      net.Conn
	rd       *bufio.Reader
	clientIP string

	// rec 是 Reconnect/Login 认领到的会话；player 是进世界之后的在线对象。
	rec    *storage.SessionRecord
	player *Player

	// nonce 是这条连接**一次性**的握手随机值（`ServerHello.session_key`）。
	//
	// ⚠️ 它是 D-24① 挑战应答的另一半：客户端把口令证明绑在它上面，
	// 所以证明**重放不了**（换个连接 nonce 就变）。见 onLogin 的说明。
	nonce []byte

	// snapReq 是自动存档的投递通道（容量 1，与 legacy 同一套机制）。
	// 在**注册进 world.players 之前**挂到 player 上，见 enterWorld。
	snapReq chan chan *storage.Character

	// sink 是实体事件的下行出口（进世界后挂到 player.protoOut 上）。
	sink *protoSink

	// saltFor/salt 是**上一条 `LoginSaltRequest` 的账号与盐**（D-32 建号要用）。
	//
	// ⚠️ 为什么记在会话上、而不是让客户端建号时把盐一起送来：盐的意义正是"服务端
	// 随机"（`storage.HashPassword` 就是这么生成的）——客户端自带盐等于把这条保证
	// 让出去。记在会话上 ⇒ 建号只能紧接着取盐之后做（否则回"没先取盐"）。
	saltFor string
	salt    []byte

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
	ps.sink = newProtoSink(ps)
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
		case env := <-ps.sink.ch:
			// 别人（怪物 AI / 其他玩家的 goroutine）投来的实体事件：
			// **只有本 goroutine 写 socket**，顺序与自己的回应答混在一起也是有序的。
			if ps.send(env) != nil {
				return
			}
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

	// `session_key` 是这条连接**一次性**的 nonce：登录时客户端把口令证明绑在它上面
	//（D-24① 挑战应答），所以这里必须**留下来**，不能只发出去就算完。
	ps.nonce = make([]byte, 16)
	if _, err := rand.Read(ps.nonce); err != nil {
		log.Printf("%s: 生成握手 nonce 失败: %v", ps.clientIP, err)
		return false
	}
	// capabilities 目前只报一条：v0 的能力面就是"进图快照"。
	if err := ps.send(frame.NewServerHello(protocol.Version, ps.nonce, []string{"enter-world"})); err != nil {
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
	case *protocol.Envelope_LoginSaltRequest:
		return ps.onLoginSaltRequest(body.LoginSaltRequest)
	case *protocol.Envelope_Login:
		return ps.onLogin(body.Login)
	case *protocol.Envelope_CreateAccount:
		return ps.onCreateAccount(body.CreateAccount)
	case *protocol.Envelope_ListCharacters:
		return ps.onListCharacters()
	case *protocol.Envelope_SelectCharacter:
		return ps.onSelectCharacter(body.SelectCharacter)
	case *protocol.Envelope_MoveInput:
		return ps.onMoveInput(body.MoveInput)
	case *protocol.Envelope_CreateCharacter:
		return ps.onCreateCharacter(body.CreateCharacter)
	case *protocol.Envelope_DeleteCharacter:
		return ps.onDeleteCharacter(body.DeleteCharacter)
	case *protocol.Envelope_NpcClick:
		return ps.onNpcClick(body.NpcClick)
	case *protocol.Envelope_NpcSelect:
		return ps.onNpcSelect(body.NpcSelect)
	case *protocol.Envelope_NpcClose:
		return ps.onNpcClose(body.NpcClose)
	case *protocol.Envelope_AttackInput:
		return ps.onAttackInput(body.AttackInput)
	case *protocol.Envelope_ShopBuy:
		return ps.onShopBuy(body.ShopBuy)
	case *protocol.Envelope_ShopSell:
		return ps.onShopSell(body.ShopSell)
	case *protocol.Envelope_PickupItem:
		return ps.onPickupItem(body.PickupItem)
	case *protocol.Envelope_MoveItem:
		return ps.onMoveItem(body.MoveItem)
	case *protocol.Envelope_UseItem:
		return ps.onUseItem(body.UseItem)
	case *protocol.Envelope_RepairItem:
		return ps.onRepairItem(body.RepairItem)
	case *protocol.Envelope_EquipItem:
		return ps.onEquipItem(body.EquipItem)
	case *protocol.Envelope_UnequipItem:
		return ps.onUnequipItem(body.UnequipItem)
	case *protocol.Envelope_Butch:
		return ps.onButch(body.Butch)
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

// ---------- 登录：D-24① 挑战应答 ----------
//
// 两步：
//
//	① 客户端要 **KDF 参数**（`LoginSaltRequest` → `LoginSalt`）：盐在服务端且是随机的，
//	   客户端拿不到就算不出与服务端存储一致的 `K` —— 这正是 D-24 当初卡住的地方。
//	② 客户端发**证明** `HMAC-SHA256(K, nonce ‖ account)`（放进 `Login.password_hash`），
//	   nonce 是握手里那条连接一次的 `session_key`。
//
// 服务端**始终不知道口令**：它存着 `K`（PBKDF2 派生值），重算一遍 HMAC 就能比对
//（`authn.CheckProof`，常量时间）。于是：明文不落网络、`K` 不落网络、证明本身也绑在
// 这条连接上（嗅到也重放不了）。
//
// ⚠️ 策略（锁定/失败计数）与 accountsvc 共用 `authn` 那一份 —— 安全策略只允许有一份（R-7）。

// onLoginSaltRequest 回一条 KDF 参数。
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

// ---------- NPC 对话（新协议出口，见 docs/todo.md 第 1 条）----------

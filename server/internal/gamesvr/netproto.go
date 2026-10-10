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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/algotao/mir2/server/internal/authn"
	"github.com/algotao/mir2/server/internal/chargen"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/frame"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
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
func (ps *protoSession) onLoginSaltRequest(m *protocol.LoginSaltRequest) bool {
	// ⚠️ 查库与留盐都用**规范化**后的名字：库里一律小写（建号时归一化），
	// 建号那边同样按规范化比对 `saltFor`（见 authn.CanonicalAccount）。
	account := authn.CanonicalAccount(m.GetAccount())
	params := storage.KDFParams()

	out := &protocol.LoginSalt{
		Iterations: uint32(params.Iterations),
		KeyLen:     uint32(params.KeyLen),
	}
	// 先用**随机盐**打底：账号不存在/已停用也回它 —— 不在这里露出"账号存不存在"
	//（与 accountsvc 那套"不存在也按口令错误回"是同一条纪律）。
	out.Salt = make([]byte, params.SaltLen)
	if _, err := rand.Read(out.Salt); err != nil {
		log.Printf("%s: 生成随机盐失败: %v", ps.clientIP, err)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	if acc, err := ps.srv.store.Accounts().GetByName(ctx, account); err == nil && !acc.Deleted {
		out.Salt = acc.Salt
	}
	// 记下来：建号（D-32）要用**同一个盐** —— 客户端就是拿它算 K 的，换一个盐
	// 存库 ⇒ 以后登录算出的 K 对不上（这条链最容易踩的坑，所以存在会话上）。
	ps.saltFor, ps.salt = account, out.Salt
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_LoginSalt{LoginSalt: out}}) == nil
}

// onLogin 校验口令证明，成功则开一条会话（与 accountsvc 登录成功后做的是同一件事）。
func (ps *protoSession) onLogin(m *protocol.Login) bool {
	if ps.rec != nil {
		// 这条连接已经认领过会话：当幂等回执（与 onReconnect 同一纪律），
		// 但**不重发 token**（那等于把已经给过的东西再喊一遍）。
		return ps.sendLoginResult(protocol.LoginCode_LOGIN_OK, "已登录", nil) == nil
	}
	if len(ps.nonce) == 0 {
		// 握手里的 nonce 没留下来 ⇒ 证明没法验。这属服务器内部错误，不是客户端的错。
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器缺少握手 nonce")
	}
	proof, err := hex.DecodeString(strings.TrimSpace(m.GetPasswordHash()))
	if err != nil || len(proof) != sha256.Size {
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "口令证明格式非法")
	}
	// ⚠️ 证明里的 account 用**客户端原样**发来的那个（两端要对齐，D-24）；
	// 查库用规范化后的（库里一律小写）。两者**刻意分开**，别合成一个变量。
	account := m.GetAccount()

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	acc, err := ps.srv.store.Accounts().GetByName(ctx, authn.CanonicalAccount(account))
	if err != nil || acc.Deleted {
		// ⚠️ 不存在与已停用**都按口令错误回**：不告诉对方账号是否存在
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "账号或口令不正确")
	}
	now := time.Now()
	lock := authn.DefaultLockPolicy()
	if lock.Locked(acc, now) {
		return ps.failLogin(protocol.LoginCode_LOGIN_LOCKED, "口令错误次数过多，请稍后再试")
	}
	if !authn.CheckProof(acc.PasswordHash, ps.nonce, account, proof) {
		lock.NoteFailure(acc, now)
		if err := ps.srv.store.Accounts().Update(ctx, acc); err != nil {
			log.Printf("%s: 写回失败计数出错（不影响本次拒绝）: %v", ps.clientIP, err)
		}
		return ps.failLogin(protocol.LoginCode_LOGIN_BAD_CREDENTIALS, "账号或口令不正确")
	}
	if authn.NoteSuccess(acc) {
		_ = ps.srv.store.Accounts().Update(ctx, acc)
	}

	// 成功：开一条**已认证**的会话，并把会话号当 token 回给客户端。
	// ⚠️ 会话号是 31 位随机值（不可猜测）；唯一性由存储主键兜底（Create 失败就换个号）。
	rec := &storage.SessionRecord{
		// 用库里那份名字（规范化过的），别用客户端原样发来的 —— 会话后面要按它
		// 查角色，两种写法混着用就会出现"登录成功但角色列表是空的"。
		Account:   acc.Name,
		IP:        ps.clientIP,
		Stage:     sessionStageAuthed,
		ExpiresAt: now.Add(loginSessionTTL),
	}
	if _, err := ps.newSession(ctx, rec); err != nil {
		log.Printf("%s: 账号 %s 登录后开会话失败: %v", ps.clientIP, account, err)
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器暂时无法分配会话")
	}
	if err := ps.srv.store.Sessions().Activate(ctx, rec); err != nil {
		log.Printf("%s: 账号 %s 会话接管失败: %v", ps.clientIP, account, err)
		return ps.failLogin(protocol.LoginCode_LOGIN_SERVER_FULL, "服务器暂时无法分配会话")
	}
	ps.rec = rec

	var tok [4]byte
	binary.LittleEndian.PutUint32(tok[:], uint32(rec.SessionID))
	log.Printf("%s: 账号 %s 登录成功（会话 %d）", ps.clientIP, account, rec.SessionID)
	return ps.sendLoginResult(protocol.LoginCode_LOGIN_OK, "", tok[:]) == nil
}

// ---------- 建号：D-32 ----------

// 建号结果码 —— 与 `protocol/account.proto` 里 CreateAccountResult 的注释一一对应。
const (
	createAccountOK       = 0
	createAccountExists   = 1
	createAccountBadName  = 2
	createAccountDisabled = 3
	createAccountTooFast  = 4
	createAccountStoreErr = 5
	createAccountNoSalt   = 6
)

// newAccountCooldown 是建号节流窗口。
//
// 原版是**每连接** 5 秒（`LoginSrv/LMain.pas:977-986` 的 `GetTickCount -
// dwClientTick > 5000`），我们按 **IP** 限 —— 每连接限速在公网上毫无意义。
const newAccountCooldown = 5 * time.Second

// onCreateAccount 建号（D-32）。三条纪律照原版：**开关**、**节流**、**重名拒绝**。
//
// 与原版的差别（有意为之，写在协议注释里）：
//
//   - 原版收两个 16 字节定长结构（`TUserEntry` + `TUserEntryAdd`：账号/口令/姓名/
//     身份证/密保…全落库），我们只收 `account + verifier` —— 存储没有那些列；
//   - **口令长度只有客户端把关**：服务端只收到 `verifier`，从来见不到口令
//     （原版服务端同样不查口令，见 `LMain.pas:1019-1079`）。
//
// 失败**不断连接**（与登录不同）：建号没成只是这次没成，用户还要接着重填或去登录。
func (ps *protoSession) onCreateAccount(m *protocol.CreateAccount) bool {
	if !ps.srv.cfg.allowNewAccount {
		return ps.sendCreateAccountResult(createAccountDisabled, "本服未开放注册")
	}
	account, err := authn.NormalizeAccount(m.GetAccount())
	if err != nil {
		return ps.sendCreateAccountResult(createAccountBadName, err.Error())
	}
	verifier, err := hex.DecodeString(strings.TrimSpace(m.GetVerifier()))
	if err != nil || len(verifier) != sha256.Size {
		return ps.sendCreateAccountResult(createAccountBadName, "口令校验值格式非法")
	}
	if len(ps.salt) == 0 || ps.saltFor != account {
		// 没先取盐（或取的是别的账号的盐）⇒ 不知道客户端拿哪个盐算的 K，
		// 落库以后登录必然对不上。宁可拒绝，也不要存一个"永远登不上"的号。
		return ps.sendCreateAccountResult(createAccountNoSalt, "请先取盐再建号")
	}
	now := time.Now()
	if !ps.srv.allowAccountCreate(ps.clientIP, now) {
		log.Printf("%s: 建号过快（%s）", ps.clientIP, account)
		return ps.sendCreateAccountResult(createAccountTooFast, "操作过快，请稍后再试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	// 先查一次只为给出"已存在"这个明确回执；真正的唯一性由存储主键兜底
	//（并发建同名时 Create 会回 ErrExists，见下面那一支）。
	if acc, err := ps.srv.store.Accounts().GetByName(ctx, account); err == nil && !acc.Deleted {
		return ps.sendCreateAccountResult(createAccountExists, "这个账号已经被使用了")
	}
	salt := append([]byte(nil), ps.salt...)
	rec := &storage.Account{Name: account, PasswordHash: verifier, Salt: salt}
	if err := ps.srv.store.Accounts().Create(ctx, rec); err != nil {
		if errors.Is(err, storage.ErrExists) {
			return ps.sendCreateAccountResult(createAccountExists, "这个账号已经被使用了")
		}
		log.Printf("%s: 建号 %s 落库失败: %v", ps.clientIP, account, err)
		return ps.sendCreateAccountResult(createAccountStoreErr, "服务器暂时无法创建账号")
	}
	log.Printf("%s: 建号成功 %s", ps.clientIP, account)
	// ⚠️ 建完**不清** salt：同一条连接再建一个号时（先取新盐）会被覆盖，这里不清
	// 只是省一次判断；真正的把关是上面的 `saltFor == account`。
	return ps.sendCreateAccountResult(createAccountOK, "账号已建立，请登录")
}

// sendCreateAccountResult 回一条建号结果。
//
// ⚠️ **拒绝也要留一行日志**：成功那条本来就有 `建号成功`，而"已存在/未开放/过快"
// 这些被拒的分支原先一行都不打 —— 用户那次"点了建号什么都没发生"就无处可查
// （服务端日志里只有 `握手完成`，排查时只能靠客户端那行 `提交建号` 去猜）。
// 拒绝是**正常结果**，但必须看得见。
func (ps *protoSession) sendCreateAccountResult(code uint32, msg string) bool {
	if code != createAccountOK {
		// 账号名用会话上留的那个（取盐时记的）—— 走到这里一定有过一次取盐。
		log.Printf("%s: 建号被拒 %s：%s", ps.clientIP, ps.saltFor, msg)
	}
	out := &protocol.CreateAccountResult{Result: &protocol.ActionResult{
		Ok:      code == createAccountOK,
		Code:    code,
		Message: msg,
	}}
	return ps.send(&protocol.Envelope{
		Body: &protocol.Envelope_CreateAccountResult{CreateAccountResult: out},
	}) == nil
}

// allowAccountCreate 是建号节流：同一 IP 在 `newAccountCooldown` 内只放行一次。
//
// ⚠️ 表会随不同 IP 增长 ⇒ 超过一定规模就顺手清掉过期项（不改语义，只防内存）。
// 用一把粗锁：建号是低频动作，热点在别处。
func (s *Server) allowAccountCreate(ip string, now time.Time) bool {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if s.createLast == nil {
		s.createLast = make(map[string]time.Time)
	}
	if len(s.createLast) > 4096 {
		for k, t := range s.createLast {
			if now.Sub(t) >= newAccountCooldown {
				delete(s.createLast, k)
			}
		}
	}
	if last, ok := s.createLast[ip]; ok && now.Sub(last) < newAccountCooldown {
		return false
	}
	s.createLast[ip] = now
	return true
}

// newSession 分配一个不可猜测的会话号并落库（形状与 accountsvc.SessionStore.Create 一致）。
func (ps *protoSession) newSession(ctx context.Context, rec *storage.SessionRecord) (int32, error) {
	for i := 0; i < 8; i++ {
		var raw [4]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, err
		}
		rec.SessionID = int32(binary.BigEndian.Uint32(raw[:]) & 0x7fffffff)
		if rec.SessionID < 2 {
			continue
		}
		if err := ps.srv.store.Sessions().Create(ctx, rec); err == nil {
			return rec.SessionID, nil
		}
	}
	return 0, errors.New("无法分配唯一会话号")
}

// sendLoginResult 回一条登录结果。
func (ps *protoSession) sendLoginResult(code protocol.LoginCode, msg string, token []byte) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_LoginResult{
		LoginResult: &protocol.LoginResult{Code: code, Message: msg, SessionToken: token}}})
}

// failLogin 回一条失败并断开：登录失败时**没有**可继续的状态（与 failReconnect 同一纪律）。
func (ps *protoSession) failLogin(code protocol.LoginCode, reason string) bool {
	log.Printf("%s: 新协议登录失败（%s）: %s", ps.clientIP, code, reason)
	_ = ps.sendLoginResult(code, reason, nil)
	_ = ps.send(&protocol.Envelope{Body: &protocol.Envelope_Disconnect{
		Disconnect: &protocol.Disconnect{Reason: reason}}})
	return false
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

// 建角/删角失败的 `ActionResult.code`（0 = 成功）。
//
// ⚠️ 这几个码是**我们定的**（协议里 `ActionResult.code` 只写了"非 0 见各消息的注释"），
// 所以这里就是它的注释，`protocol/account.proto` 里同步写着同一份。
const (
	createBadClass  = 1 // 职业不是 1/2/3
	createBadName   = 2 // 名字不合规（太短/含禁用字符）
	createNameTaken = 3 // 重名
	createTooMany   = 4 // 该账号角色已满
	createInternal  = 5 // 存储层出错
	deleteBadProof  = 1 // 二次确认（口令证明）没通过
	deleteNotFound  = 2 // 这个角色不属于本账号 / 已经删了
	deleteInternal  = 3 // 存储层出错
)

// onCreateCharacter 建角色（新协议）。
//
// ⚠️ 规矩**一律走 `chargen`** —— 与 legacy 的 `CM_NEWCHR` 是**同一份实现**（R-7）：
// 名字规则、初始物品、13 槽装备位、初始 HP/MP 都在那边。
// 这里只管几道门（职业/重名/数量）与回执，免得两边各长出一套。
func (ps *protoSession) onCreateCharacter(m *protocol.CreateCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()
	account := ps.rec.Account

	// 职业：新协议 1/2/3 → 存档 0/1/2（与 `characterSummary` 反着来）
	job := int(m.GetClass()) - 1
	if job < 0 || job > 2 {
		return ps.sendCreateResult(false, createBadClass, "职业不对", 0) == nil
	}
	sex := uint32(0)
	if m.GetGender() == protocol.Gender_GENDER_FEMALE {
		sex = 1
	}
	name := strings.TrimSpace(m.GetName())
	if !chargen.ValidName(name) {
		return ps.sendCreateResult(
			false, createBadName, "角色名不合适（至少 3 个字节，且不能含空格与 /@?'）", 0,
		) == nil
	}
	// 重名（**全局**唯一：原版也是全局查重）
	if _, err := ps.srv.store.Characters().GetByName(ctx, name); err == nil {
		return ps.sendCreateResult(false, createNameTaken, "这个名字已经有人用了", 0) == nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return ps.sendCreateResult(false, createInternal, "查重失败", 0) == nil
	}
	// 数量（软删的不算）
	chars, err := ps.srv.store.Characters().ListByAccount(ctx, account)
	if err != nil {
		return ps.sendCreateResult(false, createInternal, "读取角色列表失败", 0) == nil
	}
	alive := 0
	for _, c := range chars {
		if !c.Deleted {
			alive++
		}
	}
	if alive >= chargen.MaxPerAccount {
		return ps.sendCreateResult(
			false, createTooMany,
			fmt.Sprintf("每个账号最多 %d 个角色", chargen.MaxPerAccount), 0,
		) == nil
	}

	var items chargen.ItemSource
	if ps.srv.data.tables != nil {
		items = ps.srv.data.tables.Items
	}
	c := chargen.Build(
		account, name, uint32(job), sex, m.GetHair(), ps.srv.newCharHome(), items, &ps.srv.itemSeq,
	)
	if err := ps.srv.store.Characters().Create(ctx, c); err != nil {
		log.Printf("%s: 建角色 %q 失败: %v", ps.clientIP, name, err)
		return ps.sendCreateResult(false, createInternal, "存档写不进去", 0) == nil
	}
	log.Printf("%s: 建角色 %q（账号 %s 职业 %d 性别 %d）→ id=%d",
		ps.clientIP, name, account, job, sex, c.ID)
	return ps.sendCreateResult(true, 0, "", uint64(c.ID)) == nil
}

// onDeleteCharacter 删角色（**软删**：数据留着，只是不再出现在列表里、也不能进）。
//
// `password_hash` 是**二次确认**，复用登录时那条口令证明：
// 它绑在**本条连接的 nonce** 上（D-24①），所以换个连接就失效、重放不了；
// 服务端拿存储里的 `K` 再算一遍（`authn.CheckProof`，常量时间比较）。
// 原版删角色只弹一个确认框、不带口令 —— 这一道门是我们加的（存储层虽是软删、
// 能救回来，但"删了不可恢复"值得多问一次）。
func (ps *protoSession) onDeleteCharacter(m *protocol.DeleteCharacter) bool {
	if ps.rec == nil {
		return ps.rejectOutOfOrder("尚未认领会话")
	}
	ctx, cancel := context.WithTimeout(context.Background(), protoStoreTimeout)
	defer cancel()

	acc, err := ps.srv.store.Accounts().GetByName(ctx, ps.rec.Account)
	if err != nil {
		log.Printf("%s: 删角取账号失败: %v", ps.clientIP, err)
		return ps.sendDeleteResult(false, deleteInternal, "读不到账号") == nil
	}
	proof, err := hex.DecodeString(strings.TrimSpace(m.GetPasswordHash()))
	if err != nil || len(proof) != sha256.Size {
		return ps.sendDeleteResult(false, deleteBadProof, "二次确认的格式不对") == nil
	}
	if !authn.CheckProof(acc.PasswordHash, ps.nonce, ps.rec.Account, proof) {
		return ps.sendDeleteResult(false, deleteBadProof, "口令确认没通过") == nil
	}
	chr, err := ps.findCharacter(ctx, m.GetCharacterId())
	if err != nil {
		return ps.sendDeleteResult(false, deleteNotFound, err.Error()) == nil
	}
	if err := ps.srv.store.Characters().MarkDeleted(ctx, chr.ID); err != nil {
		log.Printf("%s: 删角色 %q 失败: %v", ps.clientIP, chr.Name, err)
		return ps.sendDeleteResult(false, deleteInternal, "删不掉") == nil
	}
	log.Printf("%s: 删角色 %q（账号 %s id=%d）", ps.clientIP, chr.Name, ps.rec.Account, chr.ID)
	return ps.sendDeleteResult(true, 0, "") == nil
}

// sendCreateResult / sendDeleteResult 两条回执（都带 `ActionResult`）。
func (ps *protoSession) sendCreateResult(ok bool, code uint32, msg string, id uint64) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_CreateCharacterResult{
		CreateCharacterResult: &protocol.CreateCharacterResult{
			Result:      &protocol.ActionResult{Ok: ok, Code: code, Message: msg},
			CharacterId: id,
		}}})
}

func (ps *protoSession) sendDeleteResult(ok bool, code uint32, msg string) error {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_DeleteCharacterResult{
		DeleteCharacterResult: &protocol.DeleteCharacterResult{
			Result: &protocol.ActionResult{Ok: ok, Code: code, Message: msg},
		}}})
}

// newCharHome 新角色的出生点：**默认地图的 StartPoint**（表里没有就 (0,0)）。
//
// ⚠️ 与 legacy 的 `HomeMap/HomeX/HomeY` 是同一语义（那边是配置里的 289,618）——
// 两边的"新号出生在哪"必须是同一个地方。
func (s *Server) newCharHome() chargen.Home {
	// **原版 1.76：两个新手村随机二选一**（银杏山谷 / 边界村，不分职业）——
	// 口径见 `docs/use.md`，候选表是 `-home-points`（默认就是那两个村）。
	//
	// ⚠️ 走过的弯路记一笔（D-38 → D-40）：先是"取安全点表第一条"（⇒ 永远边界村），
	// 再是"按配置挑最近的一条"（⇒ 永远银杏谷，确定性）；原版其实是**随机**。
	// 空候选 ⇒ 只给地图号，坐标交给 `chargen` 那边的兜底。
	if len(s.data.homePoints) > 0 {
		return chargen.PickHome(s.data.homePoints)
	}
	return chargen.Home{Map: s.data.defaultMapID}
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
	// ⚠️ 补生成该图的 NPC（与 legacy 的 sendEnterWorld 同一件事：
	// 存档在图上的玩家**不走切图**，只靠 switchMap 那一处的话，他会登进一张
	// "没有商人/仓库/铁匠"的地图）。放在注册之前 ⇒ NPC 直接进下面的快照。
	s.spawnNPCs(p.Obj.MapRef().Name)
	// ⚠️ 在**注册进 world.players 之前**挂上快照通道与实体事件出口：
	// 之后怪物 AI ticker / 别人的 goroutine 会读它们（在 s.mu 下取出玩家列表后再用），
	// 这次赋值与那次读之间由 s.mu 建立 happens-before。
	p.snapReq = ps.snapReq
	p.protoOut = ps.sink
	// ⚠️ `logonDone` 是 legacy 那套"进游戏收尾完成、可以行动"的标志位。
	// 新协议玩家**没有**legacy 的进游戏包序列，但对服务端来说"进了世界"是同一件事：
	// 共享的行动路径（`handleAttack` 的第一道门、`handleMove`/`handleTurn`）都要它。
	// 设它的副作用是"legacy 那些 handle* 会对这名玩家生效"——但那些只在收到
	// **legacy 包**时才被调用，而新协议会话永远不会喂 legacy 包进 handleGameMsg。
	p.logonDone = true

	s.mu.Lock()
	s.world.players[p.Obj.ID] = p
	s.world.index.Add(p)
	s.mu.Unlock()
	ps.player = p

	// 快照：先在锁内/lock-free 读出一份，再发（发包不带世界锁）。
	states, inView := s.entitySnapshot(p)
	env := &protocol.Envelope{Body: &protocol.Envelope_EnterWorld{EnterWorld: &protocol.EnterWorld{
		SelfEntityId: uint64(p.Obj.ID),
		// ⚠️ `map_id` 暂置 0：地图在本项目是**按名字**索引的（D-22，容器里就是 `<名字>.map`），
		// 新协议的 `map_id` 语义还没定（见 protocol.md §11 待办）。客户端请用 `map_name`。
		MapId:   0,
		MapName: p.Obj.MapRef().Name,
		// 左下角那行的抬头 = 地图显示名（官方是服务端下发的描述，见 mapTitleOf）
		MapTitle:    s.mapTitleOf(p.Obj.MapRef().Name),
		Position:    &protocol.Vec2{X: int32(p.Obj.PosX()), Y: int32(p.Obj.PosY())},
		Direction:   directionOf(p.Obj.Facing()),
		Entities:    states,
		ServerTick:  uint32(time.Now().UnixMilli()),
		SelfFeature: featureOf(p.Obj.FeatureBits()),
		// 自己的名字：客户端要画在自己头顶（快照不含自己 ⇒ 名字得单独给）
		SelfName: p.Char.Name,
		// 小地图图号：客户端按 `mmap[图号-1]` 取整张缩略图（0 = 没有）
		MinimapIndex: s.minimapIndexOf(p.Obj.MapRef().Name),
	}}}
	if err := ps.send(env); err != nil {
		return false
	}
	// 自身能力值（客户端要画血条/负重条/经验条）。
	if err := ps.send(&protocol.Envelope{Body: &protocol.Envelope_AbilityUpdate{
		AbilityUpdate: &protocol.AbilityUpdate{Ability: protocolAbility(chr.Data.Abil, chr.Data.Gold)}}}); err != nil {
		return false
	}

	// **背包 + 已穿戴**（新协议 `BagItems` / `EquippedItems`）。
	//
	// ⚠️ 用户 2026-10-09「包裹负重没显示」：负重数值在 `AbilityUpdate` 里已经带了，
	// 但**背包内容**这条链从来没接 —— `sendBagItems` / `sendUseItems` 只发 legacy，
	// 而 proto 玩家的 legacy 下行会被 `protoDown` 丢掉 ⇒ 进图后背包永远是空的。
	// 这里复用它们的**同一次遍历**（`c = nil` 有意为之：legacy 那份由 legacy 入口负责，
	// 这里只要新协议这一份；`s.send(nil, …)` 自带 nil 守卫）。
	ps.srv.sendBagItems(nil, p)
	ps.srv.sendUseItems(nil, p)

	// ⚠️ **快照就是"出现"**：先把这批实体记进视野账本，再让 updateVision 做差集。
	// 不这么做的话，updateVision 会把同一批实体再当"新进入视野"推一遍 ——
	// 客户端会收到"快照里已经有它 + 又出现一次"，白流量，而且契约测试会因为
	// 多出一条消息而红（本轮就是这么发现的）。
	p.visible.Update(inView)

	// 让 legacy 那半边也知道他来了（别人看他那条腿要发 SM_TURN）：
	// 发给**他自己**的那些 legacy 包会被 protoDown 丢掉（正是它存在的理由）。
	s.updateVision(p)
	// 视野内的**地面物品**（掉在地上的东西进图就该看见，不然杀完怪还得走两步才"突然出现"）
	s.sendGroundInView(p)
	log.Printf("%s: %s 进图（新协议 ActorId=%d 地图=%s 坐标=(%d,%d) 视野实体=%d）",
		ps.clientIP, p.Char.Name, p.Obj.ID, env.GetEnterWorld().MapName,
		p.Obj.PosX(), p.Obj.PosY(), len(states))
	return true
}

// onMoveInput 处理一次移动输入（§8：服务端权威 + 客户端预测）。
//
// 与 legacy 的 `handleMove` 同一条路径（`movePlayer` / `broadcastMove` / `updateVision`），
// 只是出入两端换成 typed 消息。三条规则照搬，因为它们与协议无关、只与玩法有关：
//
//  1. **石化/麻痹期间禁止移动**（服务端补的门：原版不拦，靠客户端自觉 ⇒ 改包就能硬走）；
//  2. **限速**（`p.Limiter`，防加速外挂）；
//  3. 被挡要回权威位置（新协议是 `MoveRejected`，legacy 是 `SM_MOVEFAIL`）。
//
// ⚠️ 走/跑：新协议用 `MoveInput.run` 区分（原版是 CM_WALK / CM_RUN 两条消息）。
// **跑一步 = 2 格**、节流更短（`MinRun` vs `MinWalk`）—— 照原版 `GetNextRunXY`。
func (ps *protoSession) onMoveInput(m *protocol.MoveInput) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	// 新枚举 = 原版 + 1（见 directionOf）；0（未指定）与越界一律当"非法输入"忽略。
	if m.GetDirection() <= protocol.Direction_DIRECTION_UNSPECIFIED ||
		int32(m.GetDirection()) > int32(entity.DirUpLeft)+1 {
		return true
	}
	dir := uint8(m.GetDirection()) - 1

	if p.Obj.Stoned(time.Now()) {
		return true
	}
	// 跑：一步 2 格、节流更短（`MinRun = 400ms` vs `MinWalk = 600ms`，见 entity.Limiter）
	running := m.GetRun()
	steps := 1
	if running {
		steps = 2
	}
	if !p.Limiter.Allow(running, time.Now()) {
		obs.Event("move_rate_limited", "player", p.Char.Name, "dir", dir, "running", running)
		// ⚠️ 与 legacy **不同**：这里必须回一条，不能静默忽略。
		// 新协议的客户端是**预测**移动的（protocol.md §8）：它按了键就已经在本地走了，
		// 服务端不吭声 ⇒ 它的位置与权威位置就此分叉且永远掰不回来。
		// legacy 那条路不预测（等 SM_WALK 才动），所以它静默忽略没问题。
		return ps.rejectMove(1, p.Obj.PosX(), p.Obj.PosY())
	}
	obs.Event("move_try", "player", p.Char.Name, "dir", dir, "running", running,
		"x", p.Obj.PosX(), "y", p.Obj.PosY())

	_, fromX, fromY, _ := p.Obj.Place()
	// 跑 = 一步 2 格（`steps` 在上面按 `run` 算好）
	newX, newY, newDir, moved, reason := ps.srv.movePlayerSteps(p, dir, steps)
	if !moved {
		// 没走成：回权威位置 + 原因（原版 `SM_MOVEFAIL` 的对应物）。
		// reason 现在有真值了：2=越界 / 3=阻挡（原来恒 3，越界那条是死枚举）。
		return ps.rejectMove(reason, newX, newY)
	}

	// 自己的权威回显（客户端已经在本地预测过，这条用来对齐/纠偏）。
	if err := ps.send(&protocol.Envelope{Body: &protocol.Envelope_EntityMove{
		EntityMove: &protocol.EntityMove{
			EntityId:   uint64(p.Obj.ID),
			From:       &protocol.Vec2{X: int32(fromX), Y: int32(fromY)},
			To:         &protocol.Vec2{X: int32(newX), Y: int32(newY)},
			Direction:  directionOf(newDir),
			ServerTick: uint32(time.Now().UnixMilli()),
			// 客户端要它才能播对动画（走 ActWalk / 跑 ActRun，见 scene.proto 的说明）
			Run: running,
		}}}); err != nil {
		return false
	}
	// 看得见他的人（两条协议各取所需，见 view.go 的分支）。
	ps.srv.broadcastMove(p, proto.SM_WALK, fromX, fromY, running)
	obs.Event("move", "player", p.Char.Name, "x", newX, "y", newY, "dir", newDir, "running", running)

	// 换格之后视野差集要重算：新进来的（EntityAppear）/ 走出去的（EntityDisappear）。
	ps.srv.updateVision(p)
	// 走进新格 ⇒ 视野里可能有还没发过的地面物品（发过的有账本，不会重发）
	ps.srv.sendGroundInView(p)
	// 火墙的第二条伤害路径：踩上去立刻结算一次（ObjBase.pas:20190 Walk）。
	ps.srv.wallBurnAtCell(p.Obj.MapRef(), newX, newY)
	return true
}

// rejectMove 告诉客户端"这一步没成"，并给出权威位置供其纠偏。
// reason 取值见 scene.proto 的 `MoveRejected.reason`：1=超速 2=越界 3=阻挡。
func (ps *protoSession) rejectMove(reason uint32, x, y int) bool {
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_MoveRejected{
		MoveRejected: &protocol.MoveRejected{
			AuthoritativePosition: &protocol.Vec2{X: int32(x), Y: int32(y)},
			Reason:                reason,
		}}}) == nil
}

// ---------- 战斗 ----------

// attackIdentOf 把新协议的 `AttackAction` 翻成 legacy 的攻击包消息号。
//
// ⚠️ 这张表与新协议的 `AttackAction` 值域**必须一一对应**（combat.proto）：
// 反过来 `attackActionOf` 又把消息号翻回动作 id 发给客户端 —— 两者是同一套语义的
// 两个方向，改一个必须改另一个。
func attackIdentOf(action protocol.AttackAction) uint16 {
	switch action {
	case protocol.AttackAction_ATTACK_HEAVY:
		return proto.CM_HEAVYHIT
	case protocol.AttackAction_ATTACK_BIG:
		return proto.CM_BIGHIT
	case protocol.AttackAction_ATTACK_POWER:
		return proto.CM_POWERHIT
	case protocol.AttackAction_ATTACK_LONG:
		return proto.CM_LONGHIT
	case protocol.AttackAction_ATTACK_WIDE:
		return proto.CM_WIDEHIT
	case protocol.AttackAction_ATTACK_FIRE:
		return proto.CM_FIREHIT
	}
	return proto.CM_HIT
}

// onAttackInput 处理一次攻击输入。
//
// # 为什么这里"合成一个 legacy 攻击包"
//
// legacy 的攻击路径（`handleAttack`）是**按朝向格**定位目标的，而新协议是
// "我要打**这个**实体"（`target_entity_id`）。两者的差别只在"怎么找到目标那格"：
// 找到之后，威力判定/打空/减防/受击/死亡/经验/掉落**完全同一条路**。
//
// 所以这里做的是**翻译**而不是重写：
//
//	按 id 找到目标 → 算出朝向它的方向 → 合成一个 CM_* 包 → 交给 handleAttack
//
// 好处（这是选它而不是抽取 `resolveMonsterMelee` 的理由）：
// 技能模式（攻杀/刺杀/半月/烈火）、挖矿、武器升级试刀、宠物跟打、掉落与经验
// 全都只有**一份**实现（R-7），新协议不会长出一个"少了几条规则"的影子版本。
// 代价是这一层翻译与 legacy 同生共死；legacy 退役时把 `handleAttack` 的入参
// 从 `wire.Packet` 换成 `(dir, mode)` 即可，那一步很小。
//
// ⚠️ 因此 `p.logonDone` 必须为 true（`handleAttack` 的第一道门），见 `enterWorld`。
func (ps *protoSession) onAttackInput(m *protocol.AttackInput) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	// 石化/麻痹期间不能出手（服务端补的门，与 legacy 那处同一句）。
	if p.Obj.Stoned(time.Now()) {
		return true
	}
	id := uint32(m.GetTargetEntityId())
	dir, ok := ps.srv.attackTargetDir(p, id)
	if !ok {
		// 目标不在相邻八格（或已经死了/不在视野）⇒ **静默忽略**：客户端可能只是
		// 本地预测着挥了一刀。IDL 里没有"攻击被拒"的消息，所以这里只留一条日志。
		log.Printf("%s: 新协议攻击目标 ActorId=%d 不可及（忽略）", p.Char.Name, id)
		return true
	}
	ident := attackIdentOf(m.GetAction())
	pkt := wire.Packet{Head: proto.MakeDefaultMsg(ident, 0, 0, uint16(dir), 0)}
	ps.srv.handleAttack(p.conn, p, pkt)

	// ⚠️ 攻击的第一件事是**转身**（`handleAttack` 里的 `turnPlayer`），而新协议的客户端
	// 不知道这件事 —— 它的朝向由服务端权威决定。不回显的话，它那一刀会朝着**旧方向**播
	//（标记/动画都偏 90°）。所以这里补一条"原地改朝向"（`from == to`，见 protoSink.move）。
	_, x, y, facing := p.Obj.Place()
	return ps.send(&protocol.Envelope{Body: &protocol.Envelope_EntityMove{
		EntityMove: &protocol.EntityMove{
			EntityId:   uint64(p.Obj.ID),
			From:       &protocol.Vec2{X: int32(x), Y: int32(y)},
			To:         &protocol.Vec2{X: int32(x), Y: int32(y)},
			Direction:  directionOf(facing),
			ServerTick: uint32(time.Now().UnixMilli()),
		}}}) == nil
}

// attackTargetDir 按 ActorId 找目标，并返回**从 p 指向它的方向**；不可及时 ok=false。
//
// 只认相邻八格：单格近战就是"朝向那一格"的语义（新协议的 `target_entity_id`
// 只是把"哪一格"说得更明确，并没有改变攻击的射程）。
func (s *Server) attackTargetDir(p *Player, targetID uint32) (uint8, bool) {
	if targetID == 0 {
		return 0, false
	}
	px, py := p.Obj.PosX(), p.Obj.PosY()
	mapRef := p.Obj.MapRef()

	s.mu.RLock()
	var tx, ty int
	found := false
	// 玩家与怪物都可能被打（PvP 与打怪共用一条路径）。
	if other := s.world.players[targetID]; other != nil && other != p {
		if other.Obj.MapRef() == mapRef {
			tx, ty, found = other.Obj.PosX(), other.Obj.PosY(), true
		}
	} else if mon := s.world.monsters[targetID]; mon != nil {
		if mon.MapRef() == mapRef && !mon.IsDead() {
			tx, ty, found = mon.PosX(), mon.PosY(), true
		}
	}
	s.mu.RUnlock()
	if !found {
		return 0, false
	}

	for d := uint8(0); d <= entity.DirUpLeft; d++ {
		if px+entity.DirDelta[d][0] == tx && py+entity.DirDelta[d][1] == ty {
			return d, true
		}
	}
	return 0, false
}

// sendMapSnapshotTo 给新协议玩家发一份"当前地图快照"（`ChangeMap`）。
//
// 进图（`EnterWorld`）与换图（回城/传送/脚本传送）是同一件事的两种时机，
// 所以两者共用 `entitySnapshot` 与同一套"快照即出现"的账本规则。
func (s *Server) sendMapSnapshotTo(p *Player, mapID string) {
	if p.protoOut == nil {
		return
	}
	states, inView := s.entitySnapshot(p)
	// ⚠️ 快照就是"出现"：先填视野账本，否则随后的 updateVision 会把同一批实体
	// 再当"新进入视野"推一遍（与 enterWorld 同一个坑，见那里的注释）。
	p.visible.Update(inView)
	// 换图 = 另一批地面物品："发过"的账本要清掉重来
	p.groundSent = nil
	p.protoOut.enqueue(&protocol.Envelope{Body: &protocol.Envelope_ChangeMap{
		ChangeMap: &protocol.ChangeMap{
			MapId:       0, // ⚠️ 语义未定（v0 恒 0，以 map_name 为准），见 protocol.md §11
			MapName:     mapID,
			Position:    &protocol.Vec2{X: int32(p.Obj.PosX()), Y: int32(p.Obj.PosY())},
			Entities:    states,
			ServerTick:  uint32(time.Now().UnixMilli()),
			SelfFeature: featureOf(p.Obj.FeatureBits()),
			// 换图了就是另一张缩略图
			MinimapIndex: s.minimapIndexOf(mapID),
			MapTitle:     s.mapTitleOf(mapID),
		}}})
	if p.Char != nil && p.Char.Data != nil {
		p.protoOut.ability(p.Char.Data.Abil, p.Char.Data.Gold)
	}
	// 换图后视野内的地面物品（同 enterWorld）
	s.sendGroundInView(p)
}

// tickProtoVision 是**只发给新协议玩家**的周期性视野同步。
//
// 为什么需要它：`updateVision` 只在"本人移动 / 换图 / 进游戏 / 脚本刷怪"时触发，
// 所以**站着不动的人看不见"走近"的实体**（这是 legacy 那半边也有的同一个缺口，
// 见 spawn.go 里那条"必须主动广播"的注释）。新协议这边要"实时"，就得定期对一次差集。
//
// ⚠️ 为什么**只对新协议玩家**做：补 legacy 那半边会给 legacy 客户端插进额外的
// SM_TURN/SM_DISAPPEAR，而 mir2cli 的 e2e 是按**包序**断言的 ⇒ 会红。
// legacy 要补的话，得连着把 e2e 的"允许穿插视野包"一起改（另开一条）。
func (s *Server) tickProtoVision(p *Player) {
	if p == nil || p.protoOut == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	s.updateVision(p)
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
//
// 第二个返回值是这批实体的 id 集合（= 本次进图后的视野集合）：调用方要拿它
// **先填 `p.visible`**，否则随后的 `updateVision` 会把同一批再当"新出现"推一遍。
func (s *Server) entitySnapshot(p *Player) ([]*protocol.EntityState, map[uint32]struct{}) {
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
	inView := make(map[uint32]struct{}, len(players)+len(monsters))
	for _, other := range players {
		out = append(out, playerState(other))
		inView[other.Obj.ID] = struct{}{}
	}
	for _, m := range monsters {
		out = append(out, monsterState(m))
		inView[m.ID] = struct{}{}
	}
	return out, inView
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
		Feature:   monsterFeatureOf(m.FeatureBits()),
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

// monsterFeatureOf 同 featureOf，但把怪物的**外观号**也填上。
//
// ⚠️ 这里是原版那套打包位域的**最后一次拆解**：`Object` 存的是
// `MakeLong(RaceImg, Appr)`（`entity/monster.go`），所以 Appr 的 16 位躺在了
// `hair`（低字节）与 `dress`（高字节）里 —— 就在这里把它拼回来，别让客户端去猜。
// 玩家那条路径不调它（玩家的 hair/dress 是真字段，appr 恒 0）。
//
// 等实体层不再打包（把四个字段直接存）时，这个函数与该拆解一起消失。
func monsterFeatureOf(f int32) *protocol.EntityFeature {
	feat := featureOf(f)
	feat.Appr = uint32(proto.FeatureHair(f)) | uint32(proto.FeatureDress(f))<<8
	return feat
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
		// 发型（选角界面不用它，进世界时才用得上）。
		sum.GenderHair = ch.Data.Hair
		// 性别：存档里有**独立字段** `Sex`（0 男 / 1 女；accountsvc 建角就写它，
		// `service.go:542`）—— 不是从发型推的，所以照实下发，不是"猜"。
		// ⚠️ 客户端**要靠它挑小人图**：原版选角界面按 (Job, Sex) 各有一套
		// 坐标与图号（`IntroScn.pas:1390-1429`、`stand_index = 40+Job*40+Sex*120`），
		// 缺了它六个职业/性别组合只能画成同一个。
		// 超出 0/1 的脏数据按"未指定"下发（宁可让客户端用默认性别，也不下发错的）。
		if ch.Data.Sex <= 1 {
			sum.Gender = protocol.Gender(ch.Data.Sex + 1)
		}
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
		// 经验/升级所需（HUD 的经验条要用；与 send.go 的 legacy 出口同一套算法）：
		// 存的是**级内经验**，`MaxExp` 由等级现算（原版 `GetExp` 升级时会减掉它）。
		Exp:    uint32(a.Exp),
		MaxExp: uint32(entity.LevelNeed(a.Level)),
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

// ---------- NPC 对话（新协议出口，见 docs/todo.md 第 1 条）----------

// onNpcClick 处理"点了个 NPC"（新协议，对应 legacy 的 `CM_CLICKNPC` / `handleClickNPC`）。
//
// ⚠️ 与 legacy 那条路的**唯一区别是出口**：proto 玩家的 legacy 下行会被 `protoDown`
// 丢弃（见 enterWorld 处的说明）⇒ 只发 legacy 时客户端一个字都收不到，表现就是
// 用户报的"点击 NPC 无法弹出对话"。对话正文与选项由 `Server.npcSay` 分流。
func (ps *protoSession) onNpcClick(n *protocol.NpcClick) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	id := uint32(n.GetNpcId())
	ps.srv.mu.RLock()
	npc, ok := ps.srv.world.monsters[id]
	ps.srv.mu.RUnlock()
	if !ok || !npc.IsNPC {
		return true // 点空了/点的不是 NPC：静默忽略
	}
	// 距离校验与 legacy 同一条：够不着就不给隔空对话
	if p.Obj.MapRef() != npc.MapRef() || p.Obj.Distance(npc.PosX(), npc.PosY()) > npcTalkRange {
		return true
	}
	def := ps.srv.npcDefOf(npc)
	if def == nil {
		def = &data.NPC{Name: npc.Name}
	}
	sc := ps.srv.npcScript(def.ID, p.Obj.MapRef().Name)
	if sc == nil {
		// 没有脚本 ⇒ 当**商人**开（legacy 的 `handleClickNPC` 同一条分支：`openShop`）
		ps.srv.openShop(nil, p, id)
		log.Printf("%s 点了没有脚本的 NPC %s（新协议，按商人开）", p.Char.Name, npc.Name)
		return true
	}
	ps.srv.startDialog(nil, p, sc, id)
	// ⚠️ **货架不在这里发**（用户 2026-10-10：「商店图应在"打开 交易市场"时弹出，
	// 而不是开启对话就出」）—— 商品列表等玩家点了脚本里的 @buy/@trading 入口
	//（`dlgSelectIndex` 的 case "buy"）才发。原版虽然点商人就发 `SM_SENDGOODSLIST`，
	// 但商店**窗口**是点入口才弹的 ⇒ 我们把"发数据"也推迟到同一时刻。
	log.Printf("%s 与 %s 开始对话（脚本 %s，新协议）", p.Char.Name, npc.Name, sc.Name)
	return true
}

// onNpcSelect 处理"选了第 index 项"（1-based，与原版的选项序号同义）。
func (ps *protoSession) onNpcSelect(n *protocol.NpcSelect) bool {
	p := ps.player
	if p == nil || p.dialog == nil {
		return true
	}
	ps.srv.dlgSelectIndex(nil, p, int(n.GetIndex())-1)
	return true
}

// onShopBuy 买入（原版 `CM_USERBUYITEM`）。
//
// ⚠️ 连接传 `nil`：新协议玩家的 legacy 下行会被 `protoDown` 丢掉，回执走 `ShopResult`
// （与 `onNpcSelect` 同一条写法）。
func (ps *protoSession) onShopBuy(m *protocol.ShopBuy) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.onShopBuy(nil, p, m)
	return true
}

// onShopSell 卖出（原版 `CM_USERSELLITEM`），同上。
func (ps *protoSession) onShopSell(m *protocol.ShopSell) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.onShopSell(nil, p, m)
	return true
}

// onPickupItem 捡起地面物品（原版 `CM_PICKUP`，按格子认物；这里按 **ground_id**）。
func (ps *protoSession) onPickupItem(m *protocol.PickupItem) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	ps.srv.pickupGroundByID(nil, p, m.GetGroundId())
	return true
}

// onNpcClose 关掉对话（原版 `@exit`）。
func (ps *protoSession) onNpcClose(n *protocol.NpcClose) bool {
	if ps.player != nil {
		ps.player.dialog = nil
	}
	return true
}

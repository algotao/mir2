package gamesvr

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

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

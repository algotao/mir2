package gamesvr

import (
	"log"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

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

// onChatInput 处理聊天输入（social.proto 的 `ChatInput`）。
//
// ⚠️ 现在只接 **'@' 命令**：Ctrl+H 切攻击模式发的是 `@atkmode`（用户 2026-10-12
// 第 5 条要底栏显示攻击模式）。复用 legacy 的 `handleSay` —— 命令分发、权限门、
// 玩家命令白名单都在那一份里，别长出第二套。
// 普通聊天（说话/喊话）还没有频道 UI ⇒ 先不接，明说一句而不是静默吞掉。
func (ps *protoSession) onChatInput(m *protocol.ChatInput) bool {
	p := ps.player
	if p == nil || p.Obj == nil {
		return ps.rejectOutOfOrder("还没进世界")
	}
	text := strings.TrimSpace(m.GetText())
	if text == "" {
		return true
	}
	if !strings.HasPrefix(text, "@") {
		ps.srv.notice(p, "聊天频道还没开放（命令要以 @ 开头）")
		return true
	}
	ps.srv.handleSay(nil, p, wire.Packet{
		Head: proto.MakeDefaultMsg(proto.CM_SAY, 0, 0, 0, 0),
		Body: text,
	})
	return true
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

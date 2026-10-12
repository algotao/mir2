package gamesvr

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

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
		// `MaxExp` 由等级现算（原版 `GetExp` 升级时会减掉它），而 **`Exp` 必须换算成
		// 级内值** —— 存档里的 `a.Exp` 是**累计**值（entity/exp.go 的说明），
		// 客户端经验条是 `100 * Exp / MaxExp`（FState.pas:2885）⇒ 直接发累计值会让
		// 经验条**从第一级起就满格**、看起来"经验满了却不升级"
		//（用户 2026-10-12 P0）。
		Exp:    uint32(entity.LevelProgress(a.Exp, a.Level)),
		MaxExp: uint32(entity.LevelNeed(a.Level)),
	}
}

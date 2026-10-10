package gamesvr

import (
	"log"
	"math/rand/v2"
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/combat"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

// monsterHit 是一次怪物攻击的结果，收集后在锁外发包。
type monsterHit struct {
	monID, playerID uint32
	dmg             uint32
	hp, maxHP, mp   uint32
	died            bool
	// miss 为真表示这一下**打空**了（命中不足）：不扣血、也不发受击包，
	// 玩家只会看到怪物挥了一下（原版 `_Attack` 里 `nPower := 0` 的效果）。
	miss bool
}

// monsterMove 是一次怪物移动。
type monsterMove struct {
	id     uint32
	x, y   int
	dir    uint8
	mapRef *world.Map
	// fromX/fromY 是移动**前**的坐标：新协议的 `EntityMove` 要带 from
	//（客户端靠它插值），legacy 的 SM_WALK 只带新位置。
	fromX, fromY int
}

// broadcastMonsterMove 把一次怪物移动广播给看得见它的人。
//
// 从 tickMonsters 里抽出来有两个理由：一是给新协议腾一个分支点（与 view.go 的
// `broadcastMove` 同构），二是它因此**可测** —— 契约测试直接调它来验证
// "客户端能实时收到 EntityMove"，而不必去等 AI 的 500ms tick（那会让用例变脆）。
func (s *Server) broadcastMonsterMove(mv monsterMove) {
	s.broadcastToViewers(mv.mapRef, mv.x, mv.y, func(p *Player) {
		if !p.visible.Contains(mv.id) {
			return
		}
		if sink := p.protoOut; sink != nil {
			// 怪物不跑（`run = false`）：原版的怪物 AI 也只有走（`Monster.pas` 无 Run 分支），
			// 骑兵类要跑是另一件事，真要做时再从这里往后传。
			sink.move(mv.id, mv.fromX, mv.fromY, mv.x, mv.y, mv.dir, false)
			return
		}
		s.send(p.conn, proto.SM_WALK, int32(mv.id), uint16(mv.x), uint16(mv.y), uint16(mv.dir), "")
	})
}

// tickMonsters 怪物 AI：选目标 → 追击 → 攻击；无目标则游荡。
//
// 原版 TMonster.Run 用大量派生类区分行为，此处先用统一的状态机：
// 目标选取 → 距离判定（相邻则攻击，否则靠近）→ 冷却控制。
func (s *Server) tickMonsters(now time.Time) {
	s.mu.Lock()
	// 尸体收尾**只在 `sweepCorpses`（5 秒那一档）里做**。
	//
	// ⚠️ 这里原来还有一份"到点就 delete"的清理 —— 它不发 `EntityDisappear`，
	// 而且跑在 500ms 这一档 ⇒ 尸体总是**先被它删掉**，`sweepCorpses` 再也找不到，
	// 于是客户端那边尸体**永远不消失**（用户 2026-10-09 问的第 4 条）。
	var moved []monsterMove
	var hits []monsterHit
	var petHits []petHit   // 宠物打怪（petattack.go）
	var guardHits []petHit // 守卫打怪（wildguard.go）：同一种结算形状，但没有主人

	// 0. 宠物走**另一套** AI（跟随主人 / 只打主人的目标 / 判变）。
	//    必须在野生怪分支之前跑，并且 continue 掉——否则宠物会被下面的
	//    "找最近玩家"逻辑当成普通怪，见 ObjMon.pas:450-520。
	s.tickSlaves(now, &moved, &hits, &petHits)

	for _, m := range s.world.monsters {
		if m.IsDead() || m.IsNPC || m.MasterID != 0 {
			continue // NPC 不参与 AI；有主的走上面的宠物分支
		}

		// 0.5 城堡单位：城门/城墙是**静态**实体，守卫/弓箭手走专门的"该打谁"。
		//
		// ⚠️ 城堡单位是 `IsNPC=false` 的 Monster ⇒ 不显式分流的话会走下面这套
		// 普通怪 AI，**城门会追着玩家跑**（把自己从门的位置挪走）。
		// 判定依据：`TGuardUnit.IsProperTarget`（ObjMon2.pas:828-883），见 guard.go。
		var guardCastle *castle.Castle
		if m.IsCastleUnit() {
			if !isCastleGuardKind(m.CastleKind) {
				continue
			}
			guardCastle = s.guardCastleOf()
		}

		// 1. 选目标：已锁定的先校验（可能已下线/跑远），否则在视野内找最近的。
		//
		// ⚠️ 关闭 aggro 时必须**整个跳过**选目标。只清空 m.TargetID 是不够的——
		// 后面的"找最近玩家"会立刻重新锁定，导致怪物仍然追击。
		//
		// ⚠️ **城堡守卫是例外**：`-monster-aggro=false` 是为野生怪准备的用例开关
		//（不主动追击 ⇒ 用例可复现），而守卫/弓箭手是城堡的防守单位——它们本来
		// 就该一直盯着"该打的人"，关掉这个开关等于把守卫功能从回归里摘出去了
		//（门/墙已在上面按 IsCastleUnit 排除，不会因为走这个分支而乱跑）。
		// ⚠️ **种族决定"打不打人"**（原版 `Grobal2.pas:1100-1106` 的 RC_*）。
		// 这两条以前都没有 ⇒ 新手村的鸡/鹿会追着新号打，野生弓箭守卫还会追白名玩家
		//（2026-10-08 用户报的"怎么那么多怪物来攻击我，连守卫都来 K 我"）。
		//
		//   · 动物（`RC_ANIMAL(50)..RC_MONSTER(80)`：鸡/鹿…）：**不主动攻击**，
		//     原版 `TChickenDeer.Run` 只会挑最近的威胁逃跑（`ObjMon.pas:542-560`）。
		//   · 守卫（`RC_GUARD(11)`/`RC_ARCHERGUARD(112)`）：只打**红名**
		//     （原版 `ObjGuard.pas:87-126`：`PKLevel >= 2` 或目标是怪）。
		//     城堡单位不走这条（它们有 `guardProperTarget` 那套行会/攻城关系）。
		// `passive` = "动物：**不主动打人**"（官方 `UsrEngn.pas` 里两支都置
		// `m_boAnimal := True`）。它管的是"要不要主动找目标"（下面那个 `case`）。
		passive := m.Info != nil && entity.IsAnimalRace(m.Info.Race)
		// ⚠️ "动物 = 只会逃跑"**不对**。原版是在**生成时**掷骰子定性格
		//（`UsrEngn.pas:1841-1852`）：
		//
		//	ANIMAL_DEER（鹿）：`if Random(30) = 0 then TChickenDeer（只会逃跑）
		//	                   else TMonster（**普通怪，会反击**）`
		//
		// ⇒ **29/30 的鹿是会打人的**。用户 2026-10-09 第 6 条：「鹿会反击，
		// 现在的实现只会逃跑」说的就是这个（我们原来把 race 50..79 一律当逃跑型）。
		// 羊同理（同一档 race）；鸡那一档（race 51）官方**一律**逃跑。
		//
		// 实现上用"按怪物 ID 定死的伪随机"代替"生成时掷骰子"：同一只怪每 tick 结果
		// 都一样（不会一会儿打一会儿跑），分布仍是 1/30。以后有 spawn 钩子再改回真随机。
		// `flee` = "**只会逃跑**"那一档（鸡一律、鹿 1/30）。**没进这一档的动物
		// （29/30 的鹿）虽然不主动找人，但被打之后会还手** —— 用户 2026-10-09 第 6 条。
		flee := passive && animalFlees(m.Info.Race, m.ID)
		stick := m.Info != nil && entity.IsStickRace(m.Info.Race)
		wildGuard := guardCastle == nil && m.Info != nil && entity.IsGuardRace(m.Info.Race)

		var target *Player
		var targetMon *entity.Monster
		switch {
		case wildGuard:
			// 野生守卫（大刀/弓箭守卫）：人 + 怪一起看（原版 `m_VisibleActors` 就是两者都看），
			// 按"红名 > 怪物 > 攻击过我的"分档挑 —— 口径见 wildguard.go 的文件头与 docs/g.md。
			if t := s.guardPick(m); t != nil {
				target, targetMon = t.player, t.monster
			}
		case !passive && (s.cfg.aggro || guardCastle != nil):
			if m.TargetID != 0 {
				// ⚠️ 保留判据从"出视野(10 格)就丢"改成原版的两条：
				// **30 秒没打到** 或 **距离 > 15 格**（`ObjBase.pas:3886-3891`）。
				// 原来的写法让怪追两步就放弃（用户报的"怪物的行为"）。
				if p := s.world.players[m.TargetID]; p != nil && p.Obj.MapRef() == m.MapRef() &&
					!m.TargetExpired(now, p.Obj.PosX(), p.Obj.PosY()) {
					target = p
				} else {
					m.TargetID = 0
				}
				// 守卫：**已锁定的目标也要重算**（原版每轮都过 IsProperTarget，
				// 行会关系/攻城状态变了要立刻放下武器）。
				if target != nil && guardCastle != nil &&
					!s.guardProperTarget(guardCastle, m, target, now) {
					m.TargetID, target = 0, nil
				}
				// 野生守卫：目标"洗白"了（PK 值降下来）也立刻放下武器
				if target != nil && wildGuard && !target.isRedName() {
					m.TargetID, target = 0, nil
				}
			}
			// ① 先看"最后打我的人"（原版 `RM_STRUCK → SetTargetCreat(hiter)`，
			// `ObjBase.pas:2761-2806`：怪被打就转过去咬打它的人，哪怕那人不是最近的）。
			// ⚠️ 原来没有这条 ⇒ 两个玩家一起打，怪只咬"视野内最近的"；被隐身玩家打永不还手。
			if target == nil && guardCastle == nil {
				if id := m.LastHiter(now); id != 0 {
					if p := s.world.players[id]; p != nil && p.Obj.MapRef() == m.MapRef() &&
						!m.TargetExpired(now, p.Obj.PosX(), p.Obj.PosY()) {
						target, m.TargetID = p, p.Obj.ID
						m.MarkTargetFocus(now)
					}
				}
			}
			if target == nil {
				best := -1
				for _, p := range s.world.players {
					if p.Obj.MapRef() != m.MapRef() {
						continue
					}
					// 隐身：怪物看不见（隐身术的效果就体现在这里）
					if p.hasBuff(entity.BuffInvisible) {
						continue
					}
					d := m.Distance(p.Obj.PosX(), p.Obj.PosY())
					if d > m.ViewRange {
						continue
					}
					// 守卫/弓箭手：只打"该打的人"（守方行会与盟友不打、非攻城期不打路人）
					if guardCastle != nil && !s.guardProperTarget(guardCastle, m, p, now) {
						continue
					}
					// 野生守卫/弓箭手：只打**红名**（原版 `ObjGuard.pas:100`）。
					// 白名玩家站在新手村/城门口，守卫不该追着他打。
					if wildGuard && !p.isRedName() {
						continue
					}
					if best < 0 || d < best {
						target, best = p, d
					}
				}
				if target != nil {
					// 守卫换目标时记一条：这是"该打谁"判定的**可观测点**
					//（e2e 的 guard-lock 断言查它；原版没有这条日志）。
					if guardCastle != nil && m.TargetID != target.Obj.ID {
						logpvp("城堡守卫 %s 锁定目标 %s", m.Name, target.Char.Name)
					}
					m.TargetID = target.Obj.ID
					m.MarkTargetFocus(now)
				}
			}
		}

		// 1.5 食人花（`StickMonster`）：**不挪步**（`StickMode`）+ 埋着（`FixedHideMode`）。
		//
		// ⚠️ 它以前走的是下面那套普通怪 AI ⇒ **会追着人跑**（用户 2026-10-09 报的
		// "食人花在走动，请参考官方实现"）。出处与字段含义见 `entity.RcStick` 的注释。
		//
		// ⚠️ **咬人只在相邻八格**：原版 `StickMonster.AttackTarget` 用的是基类
		// `GetAttackDir`（`ObjBase.pas:18449-18502`，只认八邻域），`AttackRange = 4`
		// 不是攻击距离 —— 它的作用是 `Run` 里那句 "目标出了 4 格就 `ComeDown` 缩回地下"
		//（`ObjMon2.pas:205-222/276-281`）。原来我们按 `<4 格` 判定咬人，
		// 等于给它装了门 4 格远程炮。
		if stick {
			if target != nil && m.Distance(target.Obj.PosX(), target.Obj.PosY()) <= 1 && m.CanAttack(now) {
				m.MarkAttacked(now)
				m.MarkTargetFocus(now)
				hits = append(hits, s.monsterStrike(m, target))
			}
			continue
		}

		// 1.6 动物（鸡/鹿…，race 50..79）：**不还手，只逃跑**。
		//
		// 原版 `TChickenDeer.Run`（`ObjMon.pas:542-598`）：按 tick 扫 `m_VisibleActors`
		// 取**最近的威胁**（曼哈顿距离），置 `m_boRunAwayMode := True` 并朝背离方向跑；
		// 没有威胁就 `RunAwayMode := False` 落回游荡。**从不攻击**（没有 AttackTarget）。
		// ⇒ 用户 2026-10-09 第 2 条问的"鹿、鸡在受攻击时有反击吗"：**没有，是跑**。
		// ⚠️ 原来这里整段跳过 ⇒ 鸡/鹿被打也只会原地乱晃。
		if flee {
			threat := s.nearestThreat(m)
			if threat == nil {
				// 没有威胁 ⇒ 落到下面"无目标：游荡"
			} else if m.CanAct(now) {
				m.MarkActed(now)
				fromX, fromY := m.PosX(), m.PosY()
				if m.StepAway(threat.Obj.PosX(), threat.Obj.PosY()) {
					s.world.monsterIdx.Update(m)
					moved = append(moved, monsterMove{id: m.ID, x: m.PosX(), y: m.PosY(),
						dir: m.Facing(), mapRef: m.MapRef(), fromX: fromX, fromY: fromY})
				}
				continue
			} else {
				continue
			}
		}

		// 2. 有目标：攻击 / 靠近
		if wildGuard {
			// ⚠️ 守卫**原地站桩**、**视野内就能打**（远程）：目标跑出视野就丢，绝不追。
			// 口径 `docs/g.md`（两者都"固定站位、不移动"）；原版大刀 `TSuperGuard.AttackTarget`
			// 连自己的坐标都是临时挪到目标身上再挪回来 —— 打人不挪步。
			// ⚠️ 守卫的出手间隔**不是**通用的 `attackInterval`：弓箭守卫按
			// `WalkSpeed`（500ms，原版 `m_nWalkSpeed`）连发 —— 见
			// `Monster::GuardAttackInterval`。用通用值会让它只有原版 40% 的频率。
			if m.CanAttackEvery(now, m.GuardAttackInterval()) {
				switch {
				case target != nil && m.Distance(target.Obj.PosX(), target.Obj.PosY()) <= m.ViewRange:
					m.MarkAttacked(now)
					hits = append(hits, s.monsterStrike(m, target))
				case targetMon != nil && m.Distance(targetMon.PosX(), targetMon.PosY()) <= m.ViewRange:
					m.MarkAttacked(now)
					if h := s.guardAttackMonster(m, targetMon, now); h.dmg > 0 {
						guardHits = append(guardHits, h)
					}
				}
			}
			continue // 不挪步、也不游荡（守卫没有"闲逛"这回事）
		}
		if target != nil {
			if m.Distance(target.Obj.PosX(), target.Obj.PosY()) <= 1 {
				if m.CanAttack(now) {
					m.MarkAttacked(now)
					// 打到人就刷新"盯着目标"的计时（原版 `m_dwTargetFocusTick`）
					m.MarkTargetFocus(now)
					hits = append(hits, s.monsterStrike(m, target))
				}
				continue
			}
			if !m.CanAct(now) {
				continue
			}
			m.MarkActed(now)
			fromX, fromY := m.PosX(), m.PosY()
			if m.StepTowardChecked(target.Obj.PosX(), target.Obj.PosY(), s.monsterFreeLocked(m)) {
				s.world.monsterIdx.Update(m)
				moved = append(moved, monsterMove{id: m.ID, x: m.PosX(), y: m.PosY(),
					dir: m.Facing(), mapRef: m.MapRef(), fromX: fromX, fromY: fromY})
			}
			continue
		}

		// 3. 无目标：游荡（关闭 wander 后静止，便于验证战斗链路）
		if !s.cfg.wander || !m.CanAct(now) {
			continue
		}
		m.MarkActed(now)
		fromX, fromY := m.PosX(), m.PosY()
		if m.WonderChecked(s.monsterFreeLocked(m)) {
			s.world.monsterIdx.Update(m)
			moved = append(moved, monsterMove{id: m.ID, x: m.PosX(), y: m.PosY(),
				dir: m.Facing(), mapRef: m.MapRef(), fromX: fromX, fromY: fromY})
		}
	}
	s.mu.Unlock()

	// 锁外发包，避免持锁做网络 IO
	for _, mv := range moved {
		s.broadcastMonsterMove(mv)
		// 火墙的第二条伤害路径：怪物踩上去也立刻结算（ObjBase.pas:20190）。
		s.wallBurnAtCell(mv.mapRef, mv.x, mv.y)
	}
	for _, h := range hits {
		s.applyMonsterHit(h)
	}
	s.applyPetHits(petHits, now)
	s.applyGuardHits(guardHits)
}

// monsterStrike 怪物攻击玩家：算伤害并扣血。**调用方持锁**。
// stickComeOut 是食人花的"露头半径"（原版 `StickMonster.ComeOutValue = 4`，单位：格）。
const stickComeOut = 4

// stickShows 报告这只**食人花**该不该露头（原版 `StickMonster.FixedHideMode` +
// `CheckComeOut`）：有"该打的人"在两轴各自 < [`stickComeOut`] 格内就钻出来（`RM_DIGUP`），
// 否则一直埋着 —— 埋着的时候**不进任何人的视野**（见 `view.go`）。
//
// **调用方持 `s.mu`**（读 `world.players`）。隐身的玩家它看不见，与 AI 选目标同一条口径
// （`tickMonsters` 里那句"隐身：怪物看不见"）。
func (s *Server) stickShows(m *entity.Monster) bool {
	for _, p := range s.world.players {
		if p.Obj.MapRef() != m.MapRef() || p.hasBuff(entity.BuffInvisible) {
			continue
		}
		if absi(m.PosX()-p.Obj.PosX()) < stickComeOut &&
			absi(m.PosY()-p.Obj.PosY()) < stickComeOut {
			return true
		}
	}
	return false
}

// nearestThreat 取动物"视野内最近的威胁"（原版 `TChickenDeer.Run` 的选法：
// 曼哈顿距离最近、`IsProperTarget` 的对象；`ObjMon.pas:556-575`）。
//
// **调用方持 `s.mu`**。只挑玩家：宠物/召唤物对动物的威胁不建模（原版会一起算，
// 但那依赖 PvP 状态判定，这里取最小口径）。
func (s *Server) nearestThreat(m *entity.Monster) *Player {
	if m == nil {
		return nil
	}
	var best *Player
	bestD := 1 << 30
	for _, p := range s.world.players {
		if p.Obj.MapRef() != m.MapRef() || p.hasBuff(entity.BuffInvisible) {
			continue
		}
		if m.Distance(p.Obj.PosX(), p.Obj.PosY()) > m.ViewRange {
			continue
		}
		d := absi(m.PosX()-p.Obj.PosX()) + absi(m.PosY()-p.Obj.PosY())
		if d < bestD {
			best, bestD = p, d
		}
	}
	return best
}

// monsterFreeLocked 给出"怪朝 dir 走一格，目标格没被别人占"的判据，
// 交给 `WonderChecked` / `StepTowardChecked`（原版 `Envir.CanWalkEx` 的对象阻挡）。
//
// **调用方持 `s.mu`**。
func (s *Server) monsterFreeLocked(m *entity.Monster) func(dir uint8) bool {
	return func(dir uint8) bool {
		if dir > entity.DirUpLeft {
			return false
		}
		d := entity.DirDelta[dir]
		return !s.cellOccupiedLocked(m.MapRef(), m.PosX()+d[0], m.PosY()+d[1], m.ID)
	}
}

func (s *Server) monsterStrike(m *entity.Monster, p *Player) monsterHit {
	// 命中判定（原版怪物打玩家同样走 `_Attack` 的打空分支）。
	// 例：鸡(hit=3) 打玩家(敏捷 15) 的命中率只有 4/15 ≈ 27%。
	if combat.Misses(combat.MonsterHitPoint(m), s.playerHitPoint(p), s.playerSpeedPoint(p)) {
		return monsterHit{monID: m.ID, playerID: p.Obj.ID, miss: true}
	}

	minAtk, maxAtk := uint32(m.Info.DC), uint32(m.Info.DCMax)
	if maxAtk < minAtk {
		maxAtk = minAtk
	}
	// 大刀卫士（race 11）：**固定伤害、无视防御**（`docs/g.md`：一刀 200）。
	// 数据里 `卫士` 的 DC..DCMax 就是 200/200，所以"固定"天然成立；这里只跳过**减防御**
	// 那一步（`rollDamage` 会按玩家 AC 上下浮动）。魔法盾照常在后面的受击处理里吸收。
	dmg := maxAtk
	if m.Info.Race != entity.RcGuard {
		dmg = rollDamage(minAtk, maxAtk, s.playerAC(p))
	}

	if dmg == 0 {
		// 没破防（`rollDamage` 下界 0）：原版 `_Attack` 的 `if nPower > 0` 不成立
		// ⇒ 不发受击包、不扣血。借用 `miss` 那条路（调用方只按它决定发不发包）。
		return monsterHit{monID: m.ID, playerID: p.Obj.ID, miss: true}
	}

	// 红毒：被打的人受伤放大（原版 StruckDamage 在受击方算；放在魔法盾之前，
	// 因为原版的两者是各自独立的乘算，顺序只影响取整的零点几）
	dmg = s.struckPlayer(p, dmg, time.Now())

	// 魔法盾减伤
	if r := p.damageReduction(); r > 0 {
		dmg = dmg * (100 - r) / 100
	}
	// 挨打会打破隐身（原版：受击即现身）
	if p.hasBuff(entity.BuffInvisible) {
		s.removeBuff(p, entity.BuffInvisible)
	}

	if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return monsterHit{monID: m.ID, playerID: p.Obj.ID, dmg: dmg}
	}
	// ⚠️ 怪咬人走的是 **ticker** goroutine（怪物没有自己的 goroutine），而被咬者
	// 自己的 goroutine 同时在吃药/放技能 ⇒ 判定 + 扣减同锁（见 statelock.go）。
	// 显示的伤害数字仍按原版用**未削减**的 dmg（原版如此，别顺手改）。
	hp, _ := p.hurt(dmg)
	return monsterHit{
		monID: m.ID, playerID: p.Obj.ID, dmg: dmg,
		hp: hp, maxHP: p.maxHP(), mp: p.mp(), died: hp == 0,
	}
}

// applyMonsterHit 广播一次怪物攻击的结果（锁外）。
func (s *Server) applyMonsterHit(h monsterHit) {
	s.mu.RLock()
	p := s.world.players[h.playerID]
	s.mu.RUnlock()
	if p == nil {
		return
	}
	if h.miss {
		// 打空：不发受击包、不同步血量（原版伤害为 0 时不进 StruckDamage）。
		// 留一条日志，否则"怪物挥了但没掉血"从服务端完全看不出原因。
		log.Printf("ActorId=%d 的攻击被 %s 闪开了（命中不足）", h.monID, p.Char.Name)
		return
	}

	// 受击动作：给能看到该玩家的客户端
	s.broadcastToViewers(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY(), func(other *Player) {
		if other.visible.Contains(h.playerID) {
			s.sendStruck(other, h.monID, h.playerID, h.hp, h.maxHP, h.dmg)
		}
	})
	// ⚠️ 受击者自己也要收到一份：自己的 visible 集合不含自己，
	// 只靠上面那段广播的话，玩家永远看不到自己挨打（闪红/受击动作）。
	s.sendStruck(p, h.monID, h.playerID, h.hp, h.maxHP, h.dmg)
	// 血量同步（SM_HEALTHSPELLCHANGED）
	s.sendHealthChanged(p, h.playerID, h.hp, h.mp, h.maxHP)

	// 最后打本玩家的怪（原版 m_LastHiter）：宠物据此替主人报仇
	p.lastHitBy = h.monID
	if !h.died {
		return
	}
	// 复活戒指（原版 ObjBase.pas:3755）：HP 归零时先看戒指，不满足才真死。
	// 原版是在 Run 里判、**不发死亡包**；我们在死亡收尾前拦，行为一致。
	if s.tryRevivalRing(p) {
		return
	}
	log.Printf("%s 被 ActorId=%d 击倒（伤害 %d）", p.Char.Name, h.monID, h.dmg)
	if sink := p.protoOut; sink != nil {
		// 新协议：死亡动作 + Death（击杀者是那只怪）；回城走 switchMap → ChangeMap 快照。
		sink.action(h.playerID, actionDeath)
		sink.death(h.playerID, h.monID)
	} else {
		s.send(p.conn, proto.SM_NOWDEATH, int32(h.playerID),
			uint16(p.Obj.PosX()), uint16(p.Obj.PosY()), uint16(p.Obj.Facing()), "")
	}
	s.revive(p.conn, p, nil) // 怪物致死：没有"杀人者行会"可记分
}

// dropItems 在怪物死亡处生成掉落物并广播，返回掉落的金币数（给击杀者）。
//
// 判定：**逐条独立**，一次可掉多件（UsrEngn.pas:1561）。
// 早期实现误当作"区间命中一条"，会显著改变爆率。
// dropItems 执行一只怪的掉落表，返回其中**金币的总量**（物品已就地散落到地上）。
//
// owner 是击杀者 ActorId ⇒ 写进掉落物的归属（原版 `DropItemDown(…, ItemOfCreat, …)`）。
func (s *Server) dropItems(m *entity.Monster, owner uint32) int {
	tbl := s.dropTableFor(m)
	if tbl == nil {
		return 0
	}
	return s.dropFromTable(m, tbl, owner)
}

// dropTableFor 取一只怪用的掉落表（真实表优先、内置兜底）。
func (s *Server) dropTableFor(m *entity.Monster) *entity.DropTable {
	if m == nil {
		return nil
	}
	if tbl := s.data.dropTables.Get(m.Name); tbl != nil && tbl.Len() > 0 {
		return tbl
	}
	if tbl := s.data.drops[m.Name]; tbl != nil && tbl.Len() > 0 {
		return tbl
	}
	return nil
}

// rollTable 按一张掉落表掷一次（随机数在内部取，`DropTable.Roll` 才是可注入的）。
func rollTable(tbl *entity.DropTable) []entity.DropItem {
	if tbl == nil || tbl.Len() == 0 {
		return nil
	}
	items := tbl.Items()
	rolls := make([]int, len(items))
	for i, it := range items {
		rolls[i] = rand.IntN(it.MaxPoint)
	}
	return tbl.Roll(rolls)
}

// rollLoot 掷出一只怪的掉落（取肉用，见 butch.go 的 butchTakeItems）。
func (s *Server) rollLoot(m *entity.Monster) []entity.DropItem {
	return rollTable(s.dropTableFor(m))
}

// dropFromTable 按一张掉落表执行判定并生成掉落。
func (s *Server) dropFromTable(m *entity.Monster, tbl *entity.DropTable, owner uint32) int {
	hits := rollTable(tbl)
	if len(hits) == 0 {
		return 0
	}

	gold := 0
	for _, it := range hits {
		if it.IsGold() {
			gold += it.GoldAmount(func(n int) int { return rand.IntN(n) })
			continue
		}
		tmpl := s.data.tables.Items.GetByName(it.ItemName)
		if tmpl == nil {
			// 引擎自定义物品，物品表里没有——跳过，不能刷出不存在的东西
			continue
		}
		// 持久度：原版 Dura = DuraMax/100 * (20 + Random(80))，即 20%~99%
		dura := initialDura(tmpl) // 护身符取满数量，其余随机 20%~99%
		if dura == 0 && tmpl.DuraMax > 0 {
			dura = 1
		}
		// ⚠️ 落点是**逐件**算的（原版 `DropItemDown` 逐件调 `GetDropPosition`），
		// 散落范围用**怪物死亡**的 3（`dropwide := 3`，ObjBase.pas:20641）；
		// 忽略这只正在死的怪（原版看 m_boDeath）。
		dx, dy := s.dropPosition(m.MapRef(), m.PosX(), m.PosY(), dropRangeMonsterDie, m.ID)
		gi := &GroundItem{
			ID:   s.world.groundSeq.Add(1),
			Name: tmpl.Name,
			Item: &pb.UserItem{
				MakeIndex: int32(s.itemSeq.Add(1)),
				Index:     uint32(tmpl.Index),
				Dura:      dura,
				DuraMax:   tmpl.DuraMax,
			},
			Map:       m.MapRef(),
			X:         dx,
			Y:         dy,
			Looks:     tmpl.Looks,
			Owner:     owner, // 原版 DropItemDown(…, ItemOfCreat, …)：击杀者有 2 分钟优先
			CreatedAt: time.Now(),
		}
		s.mu.Lock()
		s.world.ground[gi.ID] = gi
		s.mu.Unlock()

		// SM_ADDITEM：Recog=地面物品ID, Param=x, Tag=y, Series=外观，body=ClientItem
		// （金币走 sendGroundItem 的另一条分支，见 gold.go）
		s.broadcastGroundItem(gi)
		log.Printf("%s 掉落 %s 于 (%d,%d)", m.Name, gi.Name, gi.X, gi.Y)
	}
	return gold
}

// monsterAt 返回指定坐标上的存活怪物（同格多只取第一只）。
//
// ⚠️ 本函数**自己加读锁**，调用方必须**不持有** s.mu：
// sync.RWMutex 不可重入，嵌套加锁会死锁。
// 若调用方已持锁，请内联遍历 s.world.monsters（见 spell.go castDamageSpell）。
func (s *Server) monsterAt(m *world.Map, x, y int) *entity.Monster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, mon := range s.world.monsters {
		if !mon.IsDead() && mon.MapRef() == m && mon.PosX() == x && mon.PosY() == y {
			return mon
		}
	}
	return nil
}

// animalFlees 判断这头"动物"是**只会逃跑**的那种，还是**普通怪（会反击）**。
//
// 官方在生成时掷骰子定性格（`UsrEngn.pas:1841-1852`）：
//
//	ANIMAL_DEER（鹿）：`if Random(30) = 0 then TChickenDeer`（只会逃跑）
//	                   `else TMonster`（普通怪，**会反击**）
//	鸡那一档：一律 `TChickenDeer` ⇒ 一直逃跑
//
// 我们用"按 ID 定死的伪随机"代替掷骰子：同一只怪结果恒定（不会一会儿打一会儿跑），
// 分布仍是 1/30。用户 2026-10-09 第 6 条「鹿会反击，现在的实现只会逃跑」。
func animalFlees(race uint16, id uint32) bool {
	return race == entity.RaceChicken || id%30 == 0
}

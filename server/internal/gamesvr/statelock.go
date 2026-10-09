package gamesvr

import (
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	goproto "google.golang.org/protobuf/proto"
	"math"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/world"
)

// `Player.stateMu`：一个玩家的**私有状态**（随身财物 + 属性）的并发访问规则
// —— 这个文件是那条规则的**唯一出口**。
//
// 管的是什么：
//
//	随身财物：背包 `Char.Data.BagItems`、钱包 `Char.Data.Gold`
//	属性    ：`Char.Data.Abil`（Hp/Mp/AC/MAC/经验/等级…）
//
// 为什么属性也要管：**挨打是别人算的**。攻击者在**自己的 goroutine** 上
// `victim.Char.Data.Abil.Hp -= dmg`（`damagePlayer`，pvp.go），而被攻击者自己的
// goroutine 同时在放技能/吃药/ticker 里恢复 ⇒ 两边都是读-改-写 ⇒ **丢更新**：
// 两个人同时打同一个目标时，一次伤害会被另一次覆盖（"打了两下只掉一下"）。
// 和背包/钱包是同一个病，只是字段不同。
//
// 为什么需要它：
//
//	交易是"两个玩家"的事，我们一个连接一个 goroutine ⇒ A 的 goroutine 会去改
//	**B 的随身财物**（成交时把交易栏的物品/金币塞给对方、取消时退回来）。
//	而 B 的 goroutine 可能同时在吃药/捡物/换装/买东西（`takeBagItem`/`compactBag`/
//	格位赋值/`Gold -=`）⇒ 两条 goroutine 无同步地读写同一处 —— 真实 data race：
//
//	  - 背包：物品丢失或背包错乱（**不是**复制：复制要同一指针落在两处，这里只写一次）；
//	  - 钱包：**丢更新**。读-改-写被打断时，一边的加减会被另一边覆盖 ⇒
//	    少扣的那次就是"买了东西没付钱"，实质等于刷钱（2026-10-06 补）。
//
// 规则：
//
//	1. **任何**对 `BagItems` / `Gold` / `Abil` 的读写都必须经由本文件或
//	   item.go/deal.go 里那几个**加锁**的助手，不许裸写：
//	     背包：`addToBag`/`takeBagItem`/`takeBagByIndex`/`putBackToBag`/`compactBag`/
//	           `countBag`/`takeFromBag`/`sendBagItems`/`bagAt`/`bagLen`/`bagSnapshot`/`withBag`
//	     钱包：`withGold`/`gold`/`addGold`/`setGold`/`spendGold`
//	     属性：`withAbil`（通用读改写）/`abilCopy`（整块快照）/
//	           `hp`/`setHP`/`addHP`/`hurt`/`mp`/`addMP`/`spendMP`/`abilBytes`
//	2. `*Locked` 变体只在**已持 stateMu** 时调用（Go 的 Mutex 不可重入，嵌套就是自死锁）。
//	3. 锁序：`dealMu → stateMu`（交易先拿自己的锁再碰财物）。**永不**同时持有
//	   两个玩家的 stateMu —— 对两边的操作一律**依次**做（见 clientDealEnd；
//	   连"打人"这种天然跨玩家的动作也只碰对方那一把）。所以既不需要排序也不会死锁。
//	   `stateMu` 是**叶子锁**：它里面不会再拿任何锁（所以 `withGold`/`withBag`/
//	   `withAbil` 的回调里不许调用会加锁的东西，也不许发网络包）。
//
// ✅ 相邻的一条已闭合（2026-10-06 第六轮，见 docs/progress §3.34）：自动存档
// `saveAll` 原先在**存档 goroutine** 里直接序列化整个 `p.Char`（"快照 vs 变异"）。
// 现在改成把"做快照"投给**玩家自己的 goroutine**（`Player.snapReq`）去做，
// 由它深拷贝一份 —— 取 `BagItems`/`Gold` 时持本锁，所以与交易的跨玩家写互斥，
// 其余字段只有本人 goroutine 写、无需加锁。
//
// 至此本文件覆盖的是"**另一个 goroutine 会碰我随身财物**"的所有已知路径。
//
// ⚠️ 还**不在**本锁覆盖范围内的（都是**只读**竞态：读到瞬时旧值，不会丢更新，
// 但 `-race` 一样会报；单独记在 docs/progress §1.6）：
//
//	1. `Abil` 里除 Hp/Mp 之外的字段的**读**（`Level`/`Exp`/`Ac`/`Mac`/`Dc`/`Mc`/`Sc`）——
//	   写侧只有本人 goroutine（升级/换装/重算），读侧散落在伤害计算、脚本条件、
//	   名字颜色等处，约 40 处。整块读请用 `abilCopy()`（新代码照这个来）。
//	   玩法路径的裸读已于 §3.40 清零；**ticker 侧**（`tickMapHP`/`regenOnce`）
//	   在 §3.43 也改成了持锁快照，剩下的都是别处的散点。
//	2. ~~`entity.Object` 的坐标/朝向/状态位~~ —— **已收口（§3.43）**：
//	   这些字段不再导出，读写只能经 `entity/object.go` 的访问器，锁在对象内部
//	   （`o.mu`）。所以本节只管**世界结构与空间索引**那一半。
//
// 装备槽（`HumItems`）目前只有本人 goroutine 会动（交易不碰装备）⇒ 没有加锁；
// 将来若有跨玩家改装备的路径（例如 GM 给别人换装），要照这套规则补上。

// withBag 在持有该玩家背包锁的情况下执行 fn（fn 里可自由读写 d.BagItems）。
//
// ⚠️ fn 里**不能**再调上面那些加锁助手（只能用 `*Locked` 变体或直接操作）。
func (p *Player) withBag(fn func(d *pb.CharacterData)) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	fn(p.Char.Data)
}

// bagAt 取背包某一格的物品指针（持锁读）。越界/空槽返回 nil。
func (p *Player) bagAt(idx int) *pb.UserItem {
	if p == nil || idx < 0 {
		return nil
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil || idx >= len(p.Char.Data.BagItems) {
		return nil
	}
	return p.Char.Data.BagItems[idx]
}

// bagLen 返回背包切片长度（持锁读）。
func (p *Player) bagLen() int {
	if p == nil {
		return 0
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil {
		return 0
	}
	return len(p.Char.Data.BagItems)
}

// bagSnapshot 返回背包切片的**快照**（持锁复制切片头）。
//
// 之后遍历这份快照是安全的：即使别的 goroutine 在同时改背包（原地移动或
// realloc 出新的底层数组），旧数组依然有效，只是快照可能**略旧**。
// 需要"精确视图"时请用 withBag 持锁读完。
func (p *Player) bagSnapshot() []*pb.UserItem {
	if p == nil {
		return nil
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil {
		return nil
	}
	return p.Char.Data.BagItems
}

// ---------- 钱包（Char.Data.Gold）----------

// withGold 在持有随身财物锁的情况下读写钱包：fn 拿到**当前余额**、返回**新余额**，
// 返回值即落库后的余额。
//
// 这是钱包唯一的读改写原语 —— `gold`/`addGold`/`setGold`/`spendGold` 都建在它上面，
// 各调用点也应当优先用它（而不是"先 gold() 再 setGold()"：那是两步，
// 中间会被别的 goroutine 插进来，照样丢更新）。
func (p *Player) withGold(fn func(cur int64) int64) int64 {
	if p == nil {
		return 0
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil {
		return 0
	}
	next := fn(p.Char.Data.Gold)
	p.Char.Data.Gold = next
	return next
}

// gold 读余额（持锁快照）。⚠️ 只用于显示/日志；**花钱**请用 spendGold，
// 否则"读到的够"与"真扣的时候还够"是两回事。
func (p *Player) gold() int64 {
	return p.withGold(func(cur int64) int64 { return cur })
}

// addGold 加钱（负数即扣，但不允许扣成负数），返回新余额。
//
// 溢出按饱和处理（钳到 MaxInt64）—— 捡金币那条路上原版也没有回绕语义。
func (p *Player) addGold(n int64) int64 {
	return p.withGold(func(cur int64) int64 {
		if n > 0 && cur > math.MaxInt64-n {
			return math.MaxInt64
		}
		next := cur + n
		if next < 0 {
			next = 0
		}
		return next
	})
}

// setGold 直接设余额（GM 命令 / 交易栏调整），返回新余额。
func (p *Player) setGold(v int64) int64 {
	if v < 0 {
		v = 0
	}
	return p.withGold(func(int64) int64 { return v })
}

// spendGold **原子地**"够就扣"：余额 >= n 时扣掉并返回 true，否则分文不动返回 false。
//
// ⚠️ 别在它之前用 `gold()` 判"够不够"就发货：那个判断与这里之间会被别的 goroutine
// 插进来（这就是丢更新的入口）。要"先看够不够"就用它的返回值当唯一判据。
func (p *Player) spendGold(n int64) bool {
	if n <= 0 {
		return true
	}
	ok := false
	p.withGold(func(cur int64) int64 {
		if cur < n {
			return cur
		}
		ok = true
		return cur - n
	})
	return ok
}

// ---------- 属性（Char.Data.Abil）----------

// withAbil 在持有该玩家状态锁的情况下读写属性块。
//
// ⚠️ 回调里只能碰 `ab` 本身（和闭包外那些**不加锁**的局部变量）：
// 不许调用其它加锁助手、不许发网络包（见文件头的"叶子锁"规则）。
func (p *Player) withAbil(fn func(ab *pb.Ability)) {
	if p == nil {
		return
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	if p.Char.Data.Abil == nil {
		p.Char.Data.Abil = &pb.Ability{}
	}
	fn(p.Char.Data.Abil)
}

// abilCopy 取一份属性**快照**（持锁深拷贝）。用于整块编码下发或"要看好几个字段"
// 的判定 —— 拿快照就不必一直持锁，也不会读到别的 goroutine 写了一半的中间态。
func (p *Player) abilCopy() *pb.Ability {
	if p == nil {
		return nil
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return nil
	}
	return goproto.Clone(p.Char.Data.Abil).(*pb.Ability)
}

// hp / mp 读当前值（持锁快照）。
func (p *Player) hp() uint32 {
	if p == nil {
		return 0
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return 0
	}
	return p.Char.Data.Abil.Hp
}

func (p *Player) mp() uint32 {
	if p == nil {
		return 0
	}
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	if p.Char == nil || p.Char.Data == nil || p.Char.Data.Abil == nil {
		return 0
	}
	return p.Char.Data.Abil.Mp
}

// setHP 直接设血量，返回设置后的值（夹到 [0, MaxHP]）。
func (p *Player) setHP(v uint32) uint32 {
	out := uint32(0)
	p.withAbil(func(ab *pb.Ability) {
		if v > ab.MaxHp {
			v = ab.MaxHp
		}
		ab.Hp = v
		out = ab.Hp
	})
	return out
}

// addHP 加减血（负数为掉血），夹到 [0, MaxHp]，返回新值。
//
// ⚠️ 要"最多扣这么多"的语义（原版 `if dmg > m_Abil.HP then dmg := m_Abil.HP`）
// 请用 `hurt` —— 它把"判定 + 扣减"放在同一次持锁里。
func (p *Player) addHP(d int64) uint32 {
	out := uint32(0)
	p.withAbil(func(ab *pb.Ability) {
		next := int64(ab.Hp) + d
		if next < 0 {
			next = 0
		}
		if next > int64(ab.MaxHp) {
			next = int64(ab.MaxHp)
		}
		ab.Hp = uint32(next)
		out = ab.Hp
	})
	return out
}

// hurt 扣血并返回**实际扣掉的值**（原版 `StruckDamage` 之后那段：
// `if dmg > Hp then dmg := Hp; Dec(Hp, dmg)`）。
//
// 这是挨打唯一该用的入口：判定与扣减同锁 ⇒ 两个人同时打不会互相覆盖。
func (p *Player) hurt(dmg uint32) (newHP, actual uint32) {
	p.withAbil(func(ab *pb.Ability) {
		if dmg > ab.Hp {
			dmg = ab.Hp
		}
		ab.Hp -= dmg
		newHP, actual = ab.Hp, dmg
	})
	return newHP, actual
}

// addMP 加减蓝，夹到 [0, MaxMp]，返回新值。
func (p *Player) addMP(d int64) uint32 {
	out := uint32(0)
	p.withAbil(func(ab *pb.Ability) {
		next := int64(ab.Mp) + d
		if next < 0 {
			next = 0
		}
		if next > int64(ab.MaxMp) {
			next = int64(ab.MaxMp)
		}
		ab.Mp = uint32(next)
		out = ab.Mp
	})
	return out
}

// spendMP **原子地**"够就扣"：够则扣掉返回 true，不够则分文不动返回 false。
func (p *Player) spendMP(n uint32) bool {
	ok := false
	p.withAbil(func(ab *pb.Ability) {
		if ab.Mp < n {
			return
		}
		ab.Mp -= n
		ok = true
	})
	return ok
}

// abilBytes 编码 `SM_ABILITY` 用的那 50 字节属性块（持锁快照后编码）。
func (p *Player) abilBytes() []byte {
	ab := p.abilCopy()
	if ab == nil {
		return nil
	}
	b := abilityFromPB(ab).Bytes()
	return b[:]
}

// maxHP / maxMP 读上限（持锁）。
//
// ⚠️ 上限由**本人** goroutine 在属性重算（换装/升级/状态）时改写，
// 但别人（攻击者）也要读去算伤害/显示 ⇒ 一样要持锁读。
func (p *Player) maxHP() uint32 {
	ab := p.abilCopy()
	if ab == nil {
		return 0
	}
	return ab.MaxHp
}

func (p *Player) maxMP() uint32 {
	ab := p.abilCopy()
	if ab == nil {
		return 0
	}
	return ab.MaxMp
}

// refillHPMP 回满血蓝，返回（新血量, 新蓝量, 血上限）。升级 / 复活 / 进图用。
func (p *Player) refillHPMP() (hp, mp, maxHP uint32) {
	p.withAbil(func(ab *pb.Ability) {
		ab.Hp, ab.Mp = ab.MaxHp, ab.MaxMp
		hp, mp, maxHP = ab.Hp, ab.Mp, ab.MaxHp
	})
	return
}

// refillHP 只回满血（不回蓝）。
func (p *Player) refillHP() uint32 {
	out := uint32(0)
	p.withAbil(func(ab *pb.Ability) {
		ab.Hp = ab.MaxHp
		out = ab.Hp
	})
	return out
}

// clampHPMP 把血蓝夹进当前上限（属性重算后调）。
func (p *Player) clampHPMP() (hp, mp uint32) {
	p.withAbil(func(ab *pb.Ability) {
		if ab.Hp > ab.MaxHp {
			ab.Hp = ab.MaxHp
		}
		if ab.Mp > ab.MaxMp {
			ab.Mp = ab.MaxMp
		}
		hp, mp = ab.Hp, ab.Mp
	})
	return
}

// level 读角色等级（持锁；用 withAbil 直接取，不整份深拷贝）。
//
// ⚠️ 等级由**本人** goroutine 写（升级 / 转生 / 属性重算），但别人的命中与伤害判定、
// 脚本条件（CHECKLEVEL 一族）、组队与交易门槛都要读它 ⇒ 一样得持锁读。
// 读侧清扫见 docs/progress §3.40。
func (p *Player) level() uint32 {
	out := uint32(0)
	p.withAbil(func(ab *pb.Ability) { out = ab.Level })
	return out
}

// exp 读当前经验（持锁）。脚本的 EXP 动态变量用。
// ⚠️ `Abil.Exp` 是 **uint64**（不是 uint32），别照 hp/mp 的直觉写。
func (p *Player) exp() uint64 {
	out := uint64(0)
	p.withAbil(func(ab *pb.Ability) { out = ab.Exp })
	return out
}

// ---------- 第二节：世界态（坐标/朝向/状态位）与烈火充能 ----------
//
// 审计 P1-5 有三类"跨 goroutine 无同步读写"，收口方式各不相同：
//
//	1. **实体自身的可变态**（`Map/X/Y/Dir/Feature/Status/Poison/StoneUntil/
//	   ShowHPUntil/CastleAggroUntil`）——**已经不用这里的锁了**：它们收在
//	   `entity.Object` 自带的 `o.mu` 后面（可变态一律不导出，只能经访问器读写，
//	   见 entity/object.go 的文件头）。这样"谁在哪个 goroutine 读"不再需要逐个
//	   论证，`-race` 也不会再报这一类；`o.mu` 是叶子锁，任何位置取它都不会死锁。
//	2. **世界结构与空间索引**（`world.players/monsters/index/monsterIdx/npc…`）
//	   仍然是 `Server.mu` 域。所以"移动"这件事有两半：坐标是 ①，索引是 ② ⇒
//	   玩家一侧统一走 `movePlayer` / `turnPlayer`（内部同时做两件事，见下）。
//	3. **`Abil`**（Hp/Mp/AC/…）：见文件头与 `stateMu` 那套。ticker 里最容易忘，
//	   `tickMapHP`/`regenOnce` 已改为 `abilCopy()` 持锁快照。
//	4. **烈火充能**（`fireHit/fireHitAt`）：走下面那组访问器（`fireMu` 叶子锁）。
//
// 锁序：`Server.mu → entity.Object.mu`、`Server.mu → ViewTracker.mu`、
// `stateMu`/`fireMu` 各自独立（回调里不取别的锁）。因为 `o.mu`/`ViewTracker.mu`
// 都是叶子锁，"先 s.mu 再对象锁"是唯一的嵌套方向，不会出现反向持锁。

// movePlayer 在 `s.mu` 下把玩家朝 dir 移动一格，并同步空间索引。
//
// 返回移动后的 (x, y, dir) 快照（供锁外发包用）与是否真的移动了。
//
// ⚠️ 为什么还要 `s.mu`：坐标本身有自己的锁（见上），但**空间索引必须同步**，
// 而索引是 `s.mu` 域。"坐标已变、索引仍挂旧格"的窗口里 `playerAt`/`monsterAt`
// （按坐标比对）会找不到这名玩家 ⇒ 两件事要放在同一段临界区里。
// ⚠️ 调用方**不得**已持 `s.mu`（Go 的 Mutex 不可重入）。
func (s *Server) movePlayer(p *Player, dir uint8) (x, y int, d uint8, moved bool) {
	x, y, d, moved, _ = s.movePlayerSteps(p, dir, 1)
	return x, y, d, moved
}

// movePlayerSteps 朝 dir 连续走最多 `steps` 格（**跑 = 2 格**，照原版 `GetNextRunXY`）。
//
// ⚠️ 一格一格走、**撞墙就停**，而不是"算一个 +2 的落点"：中间有障碍时两者结果不同
// （原版 `ClientRunXY` 也是逐步走、逐步判，`ObjBase.pas:9506-9535`）。
// cellOccupiedLocked 报告 (x,y) 上是不是已经站着**别的**对象。
//
// 原版 `Envir.CanWalkEx`（`Envir.pas:488-540`）判两件事：地图 `chFlag` 与"该格上有没有
// 其它对象"（`MoveToMovingObject:287-340`）。我们原来只判前者 ⇒ 两人/一人一怪能站同一格。
//
// `selfID` = 发起者的 ActorId（自己那格不算被占）。**尸体不算障碍**
//（原版尸骨在图上，但不挡下一个对象生成 —— 这也是我们 `cellFreeLocked` 原来漏掉的）。
//
// **调用方持 `s.mu`**（空间索引本身无锁）。
func (s *Server) cellOccupiedLocked(m *world.Map, x, y int, selfID uint32) bool {
	if m == nil {
		return false
	}
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		e, ok := o.(*entity.Monster)
		if !ok || e.IsDead() || e.MapRef() != m || e.PosX() != x || e.PosY() != y || e.ID == selfID {
			continue
		}
		return true
	}
	for _, o := range s.world.index.InRange(x, y, 0) {
		e, ok := o.(*Player)
		if !ok || e.Obj.MapRef() != m || e.Obj.PosX() != x || e.Obj.PosY() != y || e.Obj.ID == selfID {
			continue
		}
		return true
	}
	return false
}

// movePlayerSteps 朝 dir 连续走最多 `steps` 格（**跑 = 2 格**，照原版 `GetNextRunXY`）。
//
// `reason`：0 = 成功；**2 = 越界**；**3 = 阻挡**（地形不可走，或目标格已被别人占）——
// 与 `MoveRejected.reason` 一一对应（`netproto.go` 的 `rejectMove`）。
//
// ⚠️ **跑一格不让**：原版 `RunTo`（`ObjBase.pas:9255-9362`）先把两格都校验完、位移在
// case 末尾**统一提交** ⇒ 第二格过不去时**一格都不动**。原来这里 `break` 之后照样提交
// 第一格，于是"半跑"：客户端按 2 格补间动画，位置与服务端就此分叉。
//
// ⚠️ **不许叠格**：见 [`Server.cellOccupiedLocked`]。
func (s *Server) movePlayerSteps(p *Player, dir uint8, steps int) (x, y int, d uint8, moved bool, reason uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, y, d = p.Obj.PosX(), p.Obj.PosY(), p.Obj.Facing()
	if dir > entity.DirUpLeft || steps < 1 {
		return x, y, d, false, 3
	}
	m := p.Obj.MapRef()
	if m == nil {
		return x, y, d, false, 3
	}
	delta := entity.DirDelta[dir]
	for i := 1; i <= steps; i++ {
		cx, cy := x+delta[0]*i, y+delta[1]*i
		if !m.InBounds(cx, cy) {
			return x, y, d, false, 2
		}
		if !m.CanWalk(cx, cy) || s.cellOccupiedLocked(m, cx, cy, p.Obj.ID) {
			return x, y, d, false, 3
		}
	}
	for i := 0; i < steps; i++ {
		if !p.Obj.MoveTo(dir) {
			break // 上面已经校验过；真落不下来就停在这里，别越试越远
		}
	}
	s.world.index.Update(p)
	return p.Obj.PosX(), p.Obj.PosY(), p.Obj.Facing(), true, 0
}

// turnPlayer 在 `s.mu` 下改玩家朝向，返回移动后的 (x, y, dir) 快照与是否成功。
// ⚠️ 调用方**不得**已持 `s.mu`。
func (s *Server) turnPlayer(p *Player, dir uint8) (x, y int, d uint8, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !p.Obj.Turn(dir) {
		return p.Obj.PosX(), p.Obj.PosY(), p.Obj.Facing(), false
	}
	return p.Obj.PosX(), p.Obj.PosY(), p.Obj.Facing(), true
}

// setStatusBit 更新"上次发出去的状态位"快照。
//
// 为什么要锁：这份快照由**本人 goroutine**（换装/吃药/增益）与 **ticker**
// （`tickPlayerStatus` 到期补播）共同读写，而它又是每条 `TCharDesc` 的字段。
// 锁在 `entity.Object` 内部（`o.mu`），所以这里不必再拿 `s.mu`。
func (s *Server) setStatusBit(p *Player, st uint32) {
	if p == nil || p.Obj == nil {
		return
	}
	// ⚠️ 不再需要 `s.mu`：`Object` 自带锁（见 entity/object.go）。
	p.Obj.SetStatusBits(int32(st))
}

// statusBit 读回那份快照（给 ticker 比对用）。
func (s *Server) statusBit(p *Player) uint32 {
	if p == nil || p.Obj == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uint32(p.Obj.StatusBits())
}

// ---------- 烈火充能（fireHit / fireHitAt）----------
//
// 审计 P1-5：这两个字段由**玩家 goroutine**（`armFireHit` 点燃、
// `consumeWarrCharge` 消费并刷新计时）与 **regenLoop 的 ticker**
// （`tickWarrCharge` 到期作废）同时读写，此前既没有锁也没有原子。
//
// ⚠️ 四个访问器是唯一出口；`fireMu` 是叶子锁 —— 回调里不加别的锁、不发包
//（`expireFireCharge` 只回一个 bool，发包由调用方在锁外做）。

// fireChargeSnapshot 读"当前是否已点燃 + 点燃时刻"的持锁快照。
func (p *Player) fireChargeSnapshot() (bool, time.Time) {
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	return p.fireHit, p.fireHitAt
}

// armFireCharge 原子地"判冷却 + 点燃"（原版 `AllowFireHitSkill`，ObjBase.pas:9784-9791）。
// 冷却内返回 false（调用方回 `FireSpiritsFail`）。
func (p *Player) armFireCharge(now time.Time, gap time.Duration) bool {
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	if now.Sub(p.fireHitAt) <= gap {
		return false
	}
	p.fireHitAt = now
	p.fireHit = true
	return true
}

// consumeFireCharge 消费点燃标志（打空也消费），并把计时刷成"此刻"
// （原版 `m_dwLatestFireHitTick`，注释原文"Jacky 防止砍空刀刀烈火"）。
// 返回是否真的消费到了充能。
func (p *Player) consumeFireCharge(now time.Time) bool {
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	if !p.fireHit {
		return false
	}
	p.fireHit = false
	p.fireHitAt = now
	return true
}

// expireFireCharge 到期作废（原版 ObjBase.pas:6425-6433）。返回是否**刚刚**作废
// （true 表示调用方要在锁外补一条 `SpiritsGone` 提示）。
func (p *Player) expireFireCharge(now time.Time, life time.Duration) bool {
	p.fireMu.Lock()
	defer p.fireMu.Unlock()
	if !p.fireHit || now.Sub(p.fireHitAt) <= life {
		return false
	}
	p.fireHit = false
	return true
}

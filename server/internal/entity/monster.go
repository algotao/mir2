package entity

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/world"
)

// SlaveKills / AddSlaveKills 是宠物经验累加器（原版那个私有 n294）。
func (m *Monster) SlaveKills() int { return m.slaveKills }

// AddSlaveKills 累加/扣减宠物击杀点数。
func (m *Monster) AddSlaveKills(d int) { m.slaveKills += d }

// Monster 是怪物实体（原版 TMonster 家族）。
//
// 原版按 Race 分出 60+ 个派生类（ObjMon/ObjMon2/ObjMon3/ObjAxeMon/ObjGuard），
// 各自重写 Run/AttackTarget。这里先用**行为参数**驱动，后续按需要再拆策略：
// 早期就把 60 个类铺开，会让每处通用改动都要改 60 次。
type Monster struct {
	*Object
	Info *data.MonsterInfo

	HP    uint32
	MaxHP uint32
	Alive bool

	// mu 保护 HP / Alive（**围绕这两个字段的读改写**）。
	//
	// 为什么需要：怪物没有自己的 goroutine —— 打它的可能是**任意**玩家/宠物的
	// goroutine，ticker 还会同时给它挂毒、烧火墙 ⇒ 原来的 `HP -= dmg` 这种
	// 读-改-写会互相覆盖（"两个人同时打，血条偶尔不对"）。和玩家侧的
	// `Player.stateMu` 是同一个病，只是对象不同。
	//
	// ⚠️ `MaxHP` **不**在这把锁里：它只在出生时写一次（之后只读）⇒ 当不可变值用。
	//
	// ⚠️ 这把锁与 `Player.stateMu` 都是**叶子锁**：不得在持一方的回调里调用
	// 另一方的加锁方法（需要两个对象时，按"先做完一个再碰下一个"的顺序写）。
	mu sync.Mutex

	// TargetID 是当前追击的目标（0 表示无目标）。
	TargetID uint32

	// wonderDir 是游荡方向偏好：完全随机每帧会让怪物原地抖动，
	// 原版用 WalkStep/WalkWait 走一段停一段，这里简化为方向惯性。
	wonderDir uint8
	// lastMove 上次移动时刻，用于控制怪物移动频率。
	lastMove time.Time
	// moveInterval 移动间隔（由 MoveSpeed 推导）。
	moveInterval time.Duration

	// lastAttack 上次攻击时刻；attackInterval 攻击间隔（由 AttackSpeed 推导）。
	// 攻击与移动分开计时，否则"追到目标后"会因为移动间隔还没到而卡住。
	lastAttack     time.Time
	attackInterval time.Duration
	// viewRange 是怪物的主动视野（原版按种族 5~16，默认 10）。
	ViewRange int

	// MeatQuality 是"肉质量"（原版 `m_nMeatQuality`）：取肉时直接写进肉的持久度。
	// 动物的初值按 race 掷（见 gamesvr.animalInit），被击/中毒会衰减。
	MeatQuality int
	// Leathery 是"皮革度"（原版 `m_nBodyLeathery`）：挖到 ≤ 0 才把肉取出来。
	Leathery int
	// Skeleton 标记这具尸体已经变成骷髅（取过一次肉之后不能再取）。
	Skeleton bool
	// AnimalSet 标记肉质初值已经掷过（原版在**出生时**掷，我们是懒掷）。
	AnimalSet bool
	// LootTaken 标记身上的东西已经被取走（原版 `m_ItemList` 被掏空）。
	LootTaken bool
	// DeathAt 是死亡时刻（原版 `m_dwDeathTick`）：尸体 `corpseLifetime` 之后收走。
	DeathAt time.Time

	// IsNPC 标记这是非战斗 NPC（商人/功能 NPC）。
	//
	// 复用 Monster 是为了直接借用实体/视野/广播机制，
	// 但 NPC 不游荡、不攻击、不可被攻击、不掉落。
	IsNPC bool

	// MasterID 是召唤者的 ActorId（0 表示野生怪物）。
	// 召唤兽不会攻击主人，也不计入野生刷怪的存活统计。
	MasterID uint32

	// ---------- 宠物（召唤兽 / 诱惑之光）----------
	//
	// 原版把这套状态放在 TBaseObject 上（ObjBase.pas:155-170），
	// 判定逻辑集中在 ObjMon.pas 的 TRobotMonster.Run（宠物走**机器人**分支，
	// 不是野生怪的分支）—— 这就是"宠物不乱跑、只打主人的目标"的由来。

	// ---------- 宠物经验/等级（原版 TBaseObject 的两个 Byte 字段）----------
	//
	// 原版 ObjBase.pas:162-163（`m_btSlaveExpLevel: Byte; //宝宝等级 1-7`、
	// `m_btSlaveMakeLevel: Byte; //召唤等级`），升级逻辑在 GainSlaveExp
	//（:2268-2299），等级决定宠物名色（GetNamecolor，:19077）。
	//
	//	SlaveMakeLevel = 召唤时的技能等级（决定等级上限 make*2+1）
	//	SlaveExpLevel  = 当前宝宝等级（0 起，击杀累积到阈值 +1）
	//	slaveKills     = 原版那个私有累加器（`Inc(n294, nLevel)`）
	SlaveMakeLevel uint8
	SlaveExpLevel  uint8
	slaveKills     int

	// RoyaltyUntil 是忠诚度到期（判变）的时刻（m_dwMasterRoyaltyTick）。
	// 零值 = 永不判变。
	//
	// 召唤骷髅/神兽给 10 天（MakeSlave 的 dwRoyaltySec = 10*24*60*60，
	// Magic.pas:786/816）；诱惑之光给按公式算出的分钟数（Magic.pas:839）。
	RoyaltyUntil time.Time
	// NoTame 标记"已被驯服"，别人不能再诱惑它（m_boNoTame）。
	// 诱惑成功时置位（Magic.pas:852）。
	NoTame bool
	// SlaveMagicLevel 是诱惑它时用的技能等级（m_btSlaveMakeLevel）。
	// 召唤兽存的是技能等级（MakeSlave 的 nMakeLevel）。
	SlaveMagicLevel uint8
	// CrazyUntil 是"狂怒"（m_boCrazyMode / OpenCrazyMode）的到期时刻。
	// 狂怒期间名字变红（$7B 之类）、AI 更激进。
	//
	// 诱惑失败时对非不死系怪物会用（Magic.pas:850/856：
	// `OpenCrazyMode(Random(20) + 10)`，单位秒）。零值 = 不狂怒。
	CrazyUntil time.Time

	// CastleKind 标记这是城堡上的单位（城门/城墙/守卫/弓箭手），
	// 非空时走城堡的判定与生命周期。对应 Delphi 的
	// TCastleDoor / TWallStructure / TGuardUnit（ObjMon2.pas:78-133）。
	//
	// 复用 Monster 而不是新造实体类型，是为了直接借用实体/视野/广播机制。
	CastleKind storage.CastleUnitKind
	// CastleIdx 是同类内的序号：墙 0=左 1=中 2=右，门 0。
	CastleIdx int
	// StoneMode 是石化：非攻城期城墙/城门处于石化状态，不可被攻击
	//（对应 m_boStoneMode；原版在 Castle.pas:696-721 每轮切换）。
	StoneMode bool
	// CastleNPC 表示这是个**属于城堡的商人**（原版 `TMerchant.m_boCastle`，
	// ObjNpc.pas:285）。它决定玩家在这个 NPC 处交易时要不要抽城堡税
	//（`if m_boCastle or g_Config.boGetAllNpcTax`，ObjNpc.pas:1974 等四处）。
	// 取值来自 NPC 定义（merchant.txt 最后一列"属沙城"）。
	CastleNPC bool

	// LastHiterID 是"最后打我的对象 ID"（原版 TGuardUnit.m_LastHiter，
	// ObjMon2.pas:833/854/861/875）。守卫用它实现"谁先动手先还击谁"，
	// 以及"守方行会的人只要动过手，也照打"。
	LastHiterID uint32

	// DoorOpened 是**城门**的开/关状态（原版 TCastleDoor.m_boOpened，ObjMon2.pas:1029-1060）。
	//
	// ⚠️ 与 StoneMode 联动但语义不同：**开门**时 `m_boStoneMode := True`
	//（开着的门不能被选为目标，ObjBase.pas:21491），关门时 False。
	// 门是否**挡路**由地图格标志决定（原版 SetMapXYFlag，我们见
	// gamesvr/castle.go 的 applyCastleDoorCells）。
	DoorOpened bool
	// NoCorpse 为 true 时死亡后尸体不消失、不回收
	//（原版靠 Run 里不停刷新 m_dwDeathTick 实现，ObjMon2.pas:1069-1086）。
	NoCorpse bool
	// lastStruckAt 是最近一次被攻击的时刻（m_dwStruckTick）。
	//
	// 修门/修墙要求"被攻击 60 秒之后才能修"（Castle.pas:1150-1219）。
	lastStruckAt time.Time

	// SeizedUntil 是被"困魔咒"定住的到期时刻（m_boHolySeize +
	// m_dwHolySeizeInterval，ObjBase.pas:21604 OpenHolySeizeMode）。
	// 零值 = 未被定住。
	//
	// 原版定身有两个副作用：
	//   - 不能移动（ObjBase.pas:2004 WalkTo 首行 `if m_boHolySeize then Exit`）
	//   - 不再被当作有效目标（ObjBase.pas:21365 目标判定里 Result := False）
	//   - 名字变褐色 $7D（RefNameColor，我们未做）
	// 我们把它接在 CanAct 上（移动/游荡都经过它）。
	SeizedUntil time.Time
}

// DelayMove 把"下次可移动时刻"往后推 d。
//
// 对应原版被推开时给行走计时器加罚：
//
//	CharPushed（ObjBase.pas:2504）：每被推开一格 `m_dwWalkTick := m_dwWalkTick + 800`
//	（只对 m_btRaceServer >= RC_ANIMAL 的怪物）。
//
// 我们这里的等价物是 lastMove（下次可动 = lastMove + moveInterval）。
func (m *Monster) DelayMove(d time.Duration) {
	m.lastMove = m.lastMove.Add(d)
}

// Seize 定住 n 秒（困魔咒）。
func (m *Monster) Seize(d time.Duration) {
	m.SeizedUntil = time.Now().Add(d)
}

// Seized 报告此刻是否处于定身状态。
func (m *Monster) Seized(now time.Time) bool { return now.Before(m.SeizedUntil) }

// IsSlave 报告这是不是有主的宠物（召唤兽或诱惑来的）。
//
// 原版判 m_Master <> nil。我们额外要求 ID 能在 s.players 里找到——
// 主人下线后那一只会在同一 tick 内被回收，不该有一帧"有主但找不到主人"。
func (m *Monster) IsSlave() bool { return m.MasterID != 0 }

// Deserted 报告忠诚度是否已到期（该判变了）。
// 零值 RoyaltyUntil = 永不判变（召唤骷髅/神兽之外的场景）。
func (m *Monster) Deserted(now time.Time) bool {
	return !m.RoyaltyUntil.IsZero() && !now.Before(m.RoyaltyUntil)
}

// Crazy 报告是否处于狂怒状态。
func (m *Monster) Crazy(now time.Time) bool { return now.Before(m.CrazyUntil) }

// GoCrazy 进入狂怒状态 d 秒（OpenCrazyMode）。
func (m *Monster) GoCrazy(d time.Duration, now time.Time) {
	m.CrazyUntil = now.Add(d)
}

// BreakCrazy 解除狂怒（BreakCrazyMode；诱惑成功时调用，Magic.pas:834）。
func (m *Monster) BreakCrazy() { m.CrazyUntil = time.Time{} }

// Repair 恢复城堡单位的血量并清除死亡状态。
//
// ⚠️ 持 m.mu：修门/修墙可能是**别人**（玩家/守卫）触发的，与正在拆门的人并发。
// 对应 RepairDoor / RepairWall（Castle.pas:1150-1219）：HP 拉满、
// 死亡时额外把 m_boDeath 置回 false，然后 RefStatus。
// 返回是否真的做了修复（已满血则返回 false）。
func (m *Monster) Repair() bool {
	if !m.IsCastleUnit() {
		return false
	}
	m.mu.Lock()
	if m.Alive && m.HP >= m.MaxHP {
		m.mu.Unlock()
		return false
	}
	m.HP, m.Alive = m.MaxHP, true
	m.mu.Unlock()
	return true
}

// StruckMark 记录被攻击时刻（修门/修墙的 60 秒免疫用）。
func (m *Monster) StruckMark(now time.Time) { m.lastStruckAt = now }

// StruckRecently 报告最近 minDuration 内是否被攻击过。
func (m *Monster) StruckRecently(now time.Time, minDuration time.Duration) bool {
	if m.lastStruckAt.IsZero() {
		return false
	}
	return now.Sub(m.lastStruckAt) < minDuration
}

// IsCastleUnit 报告这是否是城堡上的单位。
func (m *Monster) IsCastleUnit() bool { return m.CastleKind != "" }

// CanBeAttackedBy 判定玩家能否攻击这个城堡单位（TGuardUnit.IsProperTarget，
// ObjMon2.pas:828-883 的城堡分支）。
//
// 原版六层判定（按顺序）：
//  1. 打过它的人（m_LastHiter）总能继续打——仇恨
//  2. 两分钟内攻击过守城成员的人（bo2B0）
//  3. 攻城期（m_boUnderWar）所有人可打
//  4. 守方行会/联盟行会的人**不可打**
//  5. GM 隐身 / 石化 / NPC 种族 / 另一个城堡单位：不可打
//
// underWar 由攻城战状态机算好后传进来（城堡包只管实体，不查战期）。
func (m *Monster) CanBeAttackedBy(underWar, isOwnGuild, isAllyGuild, stoneMode bool) bool {
	if !m.IsCastleUnit() {
		return true // 非城堡单位走普通判定
	}
	if m.StoneMode || stoneMode {
		return false
	}
	if m.Info != nil && m.Info.Race >= RcNpc && m.Info.Race < RcAnimal {
		return false // RC_NPC(10) <= race < RC_ANIMAL(50)：非战斗单位
	}
	if underWar {
		return true // 攻城期：所有人可打（但守方已被 PVP 判定挡在外面）
	}
	// 非攻城期：只有守方才可打自己的城墙（修门/修墙也走这里）。
	return isOwnGuild || isAllyGuild
}

// 怪物的**种族**（`Race`）常量 —— 数值照抄原版 `Grobal2.pas:1100-1106`，**必须一致**：
// 它们既决定 AI 行为（见 [`IsAnimalRace`] / [`IsGuardRace`]），也决定取肉/变骷髅之类。
const (
	RcPlayer      = 0   // RC_PLAYOBJECT
	RcNpc         = 10  // RC_NPC
	RcGuard       = 11  // RC_GUARD
	RcPeaceNpc    = 15  // RC_PEACENPC
	RcAnimal      = 50  // RC_ANIMAL
	RcMonster     = 80  // RC_MONSTER
	RcArcherGuard = 112 // RC_ARCHERGUARD
)

// IsAnimalRace 是**动物**（鸡/鹿…）。
// 原版里它们**不主动攻击玩家**：`TChickenDeer.Run` 只会挑最近的威胁**逃跑**
// （`ObjMon.pas:542-560`）。取值范围照原版 `RC_ANIMAL(50) <= race < RC_MONSTER(80)`。
func IsAnimalRace(race uint16) bool { return race >= RcAnimal && race < RcMonster }

// IsGuardRace 是**守卫 / 弓箭守卫**。
// 原版只在目标是**红名**（`PKLevel >= 2`）或怪物时才动手（`ObjGuard.pas:87-126`）。
func IsGuardRace(race uint16) bool { return race == RcGuard || race == RcArcherGuard }

// IsNonCombatant 是**非战斗单位**：`RC_NPC(10) <= race < RC_ANIMAL(50)`
// （NPC、大刀卫士、弓箭警察…）。
//
// 两个用处（别混）：
//   - **守卫的目标排除项**：原版 `IsProperTarget` 把这一段排除在外（`ObjMon2.pas:866-868`）
//     ⇒ 守卫不打 NPC / 不打同类；
//   - **玩家的"打不动"**：`docs/g.md` 的「大刀卫士：无敌，玩家无法击杀」。
func IsNonCombatant(race uint16) bool { return race >= RcNpc && race < RcAnimal }

// NewMonster 创建怪物。
func NewMonster(id uint32, info *data.MonsterInfo, m *world.Map, x, y int) *Monster {
	hp := uint32(info.HP)
	return &Monster{
		Object: &Object{
			ID:     id,
			Name:   info.Name,
			mapRef: m,
			posX:   x,
			posY:   y,
			facing: uint8(rand.IntN(8)),
			// 怪物外观：低 16 位 = MakeWord(RaceImg, Weapon)，高 16 位 = Appr
			feature: proto.MakeLong(uint16(info.RaceImg), info.Appr),
		},
		Info:         info,
		HP:           hp,
		MaxHP:        hp,
		Alive:        true,
		wonderDir:    uint8(rand.IntN(8)),
		lastMove:     time.Now(),
		moveInterval: moveIntervalFromSpeed(info.WalkSpeed),
		// 原版攻击速度单位与移动类似，下限同样取 200ms；
		// 未配置时退化为"与移动同速"。
		attackInterval: attackIntervalFromSpeed(info.AttackSpeed),
		ViewRange:      MonsterViewRange,
	}
}

// MonsterViewRange 是怪物主动视野的默认半径（切比雪夫距离）。
//
// 原版按种族区分（ObjBase.pas 中 m_nViewRange 5~16，默认 10）。
const MonsterViewRange = 10

// attackIntervalFromSpeed 由 AttackSpeed 推导攻击间隔，下限 200ms。
func attackIntervalFromSpeed(speed uint16) time.Duration {
	const minInterval = 200 * time.Millisecond
	if speed == 0 {
		return minInterval
	}
	d := time.Duration(speed) * time.Millisecond
	if d < minInterval {
		return minInterval
	}
	return d
}

// moveIntervalFromSpeed 由 WalkSpeed 推导移动间隔。
//
// 原版 WALK_SPD 单位是毫秒且下限 200（LocalDB.pas:1355 附近）。
func moveIntervalFromSpeed(speed uint16) time.Duration {
	const minInterval = 200 * time.Millisecond
	if speed == 0 {
		return minInterval
	}
	d := time.Duration(speed) * time.Millisecond
	if d < minInterval {
		return minInterval
	}
	return d
}

// CapSpeed 把移动/攻击间隔**压到不超过** cap 毫秒（只压慢、不加快）。
//
// 对应诱惑之光成功后那段（Magic.pas:846-851）：
//
//	if LongWord(1500 - 技能等级*200) < LongWord(m_nWalkSpeed) then m_nWalkSpeed := 1500 - 技能等级*200
//	if LongWord(2000 - 技能等级*200) < LongWord(m_nNextHitTime) then m_nNextHitTime := 2000 - 技能等级*200
//
// 即"收了个速度太快的怪就把它压下来"，防止某些怪（WalkSpeed 远小于 900ms）
// 收服后跟主人跟得离谱地快。
//
// ⚠️ 这里改的是**本实例**的间隔，**不能**去改 m.Info.WalkSpeed——
// 那是全服共享的怪物模板，改一处所有同种怪都被改。
func (m *Monster) CapSpeed(walkCap, hitCap uint16) {
	const minInterval = 200 * time.Millisecond
	clamp := func(d time.Duration, cap uint16) time.Duration {
		if cap == 0 {
			return d
		}
		c := time.Duration(cap) * time.Millisecond
		if d <= c {
			return d
		}
		if c < minInterval {
			return minInterval
		}
		return c
	}
	m.moveInterval = clamp(m.moveInterval, walkCap)
	m.attackInterval = clamp(m.attackInterval, hitCap)
}

// MoveInterval 返回当前移动间隔（受 CapSpeed 影响）。
func (m *Monster) MoveInterval() time.Duration { return m.moveInterval }

// AttackInterval 返回当前攻击间隔（受 CapSpeed 影响）。
func (m *Monster) AttackInterval() time.Duration { return m.attackInterval }

// Pos 实现 world.Positioned。
func (m *Monster) Pos() (int, int) { return m.Object.Pos() }

// ---------- 血量（唯一入口，都持 m.mu）----------

// HPValue 读当前血量。
func (m *Monster) HPValue() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.HP
}

// IsDead 报告是否已死亡。
func (m *Monster) IsDead() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.Alive || m.HP == 0
}

// SetHP 直接设血量（夹到 MaxHP），返回设置后的值。
func (m *Monster) SetHP(v uint32) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v > m.MaxHP {
		v = m.MaxHP
	}
	m.HP = v
	return m.HP
}

// Heal 加血（夹到 MaxHP），返回新血量。
func (m *Monster) Heal(n uint32) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > m.MaxHP-m.HP {
		m.HP = m.MaxHP
	} else {
		m.HP += n
	}
	return m.HP
}

// HealHalf 补到一半（原版召唤/诱惑成功时的"补一半血"，见 slave.go）。
func (m *Monster) HealHalf() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.HP < m.MaxHP {
		m.HP += (m.MaxHP - m.HP) / 2
	}
	return m.HP
}

// Kill 直接击杀（HP=0 + Alive=false）。
func (m *Monster) Kill() {
	m.mu.Lock()
	m.HP, m.Alive = 0, false
	m.mu.Unlock()
}

// Damage 扣血，返回是否**这一下**致死（已死的怪返回 false，不再重复结算）。
func (m *Monster) Damage(n uint32) (died bool) {
	_, _, died = m.Hurt(n)
	return died
}

// Hurt 扣血，返回（实际扣掉的量, 新血量, 是否致死）。
//
// ⚠️ 判定与扣减在同一次持锁里 —— 这是"打怪"唯一该用的入口：
// 原来各调用点自己 `if dmg >= mon.HP { mon.HP = 0 } else { mon.HP -= dmg }`，
// 两个 goroutine 同时打就会被覆盖（伤害凭空少掉一次）。
func (m *Monster) Hurt(n uint32) (actual, newHP uint32, died bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.Alive || m.HP == 0 {
		return 0, m.HP, false
	}
	// **无敌**：非战斗单位（NPC / 大刀卫士 / 弓箭警察…）不吃伤害 ——
	// `docs/g.md`「大刀卫士：无敌，玩家无法击杀」（原版把它们当非战斗单位，
	// `ObjMon2.pas:866-868`）。放在这里而不是各个攻击点：
	// ① 一处生效，近战/技能/火墙/毒/别的怪一视同仁；
	// ② 它本来就是这只怪的属性，不是某个调用方的规矩。
	// ⚠️ 城堡单位（城门/城墙/守卫）不受这条限制 —— 攻城与修门都要能打。
	if !m.CanBeHurt() {
		return 0, m.HP, false
	}
	if n >= m.HP {
		m.HP, m.Alive = 0, false
		return m.HP + n, 0, true // 实际扣掉的就是原来那些血
	}
	m.HP -= n
	return n, m.HP, false
}

// CanBeHurt 报告这只怪**现在还吃不吃伤害**（`docs/g.md` 的"大刀无敌"）。
//
// 非战斗单位免疫；城堡单位例外（城门/城墙/守为攻城目标，见 `CanBeAttackedBy`）。
func (m *Monster) CanBeHurt() bool {
	if m.Info == nil {
		return false
	}
	if m.IsCastleUnit() {
		return true
	}
	return !IsNonCombatant(m.Info.Race)
}

// CanAct 报告此刻是否可执行移动（受移动间隔限制 + 定身）。
//
// ⚠️ 定身判定放在这里，是因为**所有**移动都经过 CanAct：
// 追击（tickMonsters 的 StepToward 前）与游荡（Wonder 前）都查它。
func (m *Monster) CanAct(now time.Time) bool {
	// 困魔咒（SeizedUntil）与石化（StoneUntil）都禁止行动：原版怪物 AI 里
	// 两者的判据并列（`m_boHolySeize` 与 `m_wStatusTimeArr[POISON_STONE] = 0`，
	// 见 ObjMon.pas:423 / ObjAxeMon.pas:133）。
	if m.Seized(now) || m.Stoned(now) {
		return false
	}
	return now.Sub(m.lastMove) >= m.moveInterval
}

// MarkActed 记录一次动作时刻。
func (m *Monster) MarkActed(now time.Time) { m.lastMove = now }

// CanAttack 报告此刻是否可发起攻击（受攻击间隔限制）。
func (m *Monster) CanAttack(now time.Time) bool {
	return now.Sub(m.lastAttack) >= m.attackInterval
}

// MarkAttacked 记录一次攻击时刻。
func (m *Monster) MarkAttacked(now time.Time) { m.lastAttack = now }

// InView 报告坐标是否在怪物视野内。
func (m *Monster) InView(x, y int) bool { return m.Distance(x, y) <= m.ViewRange }

// Wonder 无目标时随机游荡：优先沿当前方向，撞墙则换向。
//
// 返回是否发生了移动。
func (m *Monster) Wonder() bool {
	// 先试当前方向
	if m.Object.MoveTo(m.wonderDir) {
		return true
	}
	// 撞墙：随机换一个能走的方向
	for i := 0; i < 8; i++ {
		d := uint8(rand.IntN(8))
		if m.Object.MoveTo(d) {
			m.wonderDir = d
			return true
		}
	}
	// 偶尔原地转向，避免完全静止显得呆板
	m.wonderDir = uint8(rand.IntN(8))
	m.Object.Turn(m.wonderDir)
	return false
}

// StepToward 朝目标坐标走一步（贪心，非寻路）。
//
// 原版也没有 A*/FindPath（全库零命中），怪物靠 GetNextPosition 贪心靠近，
// 卡住时 MapRandomMove。这里保持一致。
func (m *Monster) StepToward(tx, ty int) bool {
	mx, my := m.Pos()
	dx, dy := tx-mx, ty-my
	var dir uint8
	switch {
	case dx > 0 && dy > 0:
		dir = DirDownRight
	case dx > 0 && dy < 0:
		dir = DirUpRight
	case dx < 0 && dy > 0:
		dir = DirDownLeft
	case dx < 0 && dy < 0:
		dir = DirUpLeft
	case dx > 0:
		dir = DirRight
	case dx < 0:
		dir = DirLeft
	case dy > 0:
		dir = DirDown
	default:
		dir = DirUp
	}
	if m.Object.MoveTo(dir) {
		return true
	}
	// 直线走不通，退化为随机游荡一次
	return m.Wonder()
}

// LastStruckAt 返回最近一次被攻击的时刻（零值表示从未被打）。
func (m *Monster) LastStruckAt() time.Time { return m.lastStruckAt }

// Package pvp 实现 PvP 判定与 PK 惩罚的纯逻辑部分。
//
// 对应 Delphi 的 IsAttackTarget / IsProtectTarget / IsProperTarget /
// IncPkPoint / SetPKFlag（ObjBase.pas:21220-21519、18868-18904、2234-2372）。
//
// 这里只做"能不能打"的判定与 PK 罪计算，不碰网络/数据库，方便单测驱动。
//
// # 与 OpenMir2 的两处差异（OpenMir2 有 bug，必须以 Delphi 为准）
//
//  1. **IsProtectTarget 是赋值不是提前 return**（ObjBase.pas:21502）。
//     OpenMir2（PlayObject.cs:1936）写成了 `return`，后面的判断就没机会了。
//  2. **行会关系实时计算**，不写"上次刷新留下的标记"。Delphi 的
//     m_boGuildWarArea 只在 GetGuildRelation 里赋值，而它只被 GetCharColor
//     与 Die 调用，导致 IsAttackTarget 读到陈旧状态（ObjBase.pas:2349/2353
//     vs 21460）。
package pvp

import (
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// AttackMode 是攻击模式（对应 m_btAttatckMode，ObjBase.pas:21385 的 case）。
// 客户端可主动切换，默认 HamAll（全体攻击）。
type AttackMode uint8

const (
	// HamAll 全体攻击：玩家、NPC、怪都能打（ObjBase.pas:21388-21393）。
	HamAll AttackMode = iota
	// HamPeace 和平：只打怪，**完全打不到玩家**（ObjBase.pas:21395-21398）。
	HamPeace
	// HamGroup 编组：非 NPC 区间都能打，但队友除外（ObjBase.pas:21441-21449）。
	HamGroup
	// HamGuild 行会：非同会成员、非同盟行会才能打（ObjBase.pas:21451-21467）。
	HamGuild
	// HamPKAttack 只打红名：目标 PKLevel 必须 >= 2（ObjBase.pas:21469-21487）。
	HamPKAttack
)

// RaceServer 是对象种族（对应 m_btRaceServer，Grobal2.pas 的 RC_* 常量）。
type RaceServer uint8

const (
	// RacePlay 玩家（RC_PLAYOBJECT=9）。
	RacePlay RaceServer = 9
	// RaceNPC NPC（RC_NPC=10）。
	RaceNPC RaceServer = 10
	// RacePeaceNPC 和平 NPC（RC_PEACENPC=15）。
	RacePeaceNPC RaceServer = 15
	// RaceAnimal 动物/怪（RC_ANIMAL=50）。
	RaceAnimal RaceServer = 50
)

// MoveProtectTime 是传送后的保护时长（ObjBase.pas:21322 的硬编码 3000ms）。
//
// 刚传送/刚换图的 3 秒内既打不到人也打不了人，避免"切图瞬间被守尸"。
const MoveProtectTime = 3 * time.Second

// RedNameLevel 是红名阈值：PKLevel >= 2 即红名。
//
// 出现于 ObjBase.pas:21474（HAM_PKATTACK）、21270/21279（等级保护）、
// 20949（武器锁）、21149（红名掉双倍等级）等处。
const RedNameLevel = 2

// Config 是 PK 相关配置，默认值取自 g_Config（M2Share.pas）。
type Config struct {
	// SafeZoneSize 是出生点/红名区的安全区半径（nSafeZoneSize=10）。
	SafeZoneSize int
	// RedHomeMap 是红名监狱地图（sRedHomeMap="3"，M2Share.pas:1693）。
	RedHomeMap string
	// RedHomeX/RedHomeY 是红名监狱坐标（839/668）。
	//
	// ⚠️ 原版 InSafeZone 也拿这个点当安全区（ObjBase.pas:21535-21542），
	//   也就是"监狱门口也安全"。看着奇怪但是原版行为，照抄。
	RedHomeX, RedHomeY int
	// RedPKProtectLevel 是红名保护等级阈值（nRedPKProtectLevel=10）。
	//
	// ⚠️ 这段在 IsProtectTarget 里**无条件生效**（ObjBase.pas:21308），
	//   不受 LevelProtect 影响，所以 1.5 复古默认就有"10 级以下打不到人"。
	RedPKProtectLevel uint32
	// LevelProtect 是总开关（boPKLevelProtect，出厂 False）。
	// 打开后额外启用 PKProtectLevel 那段（ObjBase.pas:21266-21280）。
	LevelProtect bool
	// PKProtectLevel 是等级保护阈值（nPKProtectLevel=10）。
	PKProtectLevel uint32
	// KillAddPKPoint 是杀白名加的 PK 点（nKillHumanAddPKPoint=100）。
	KillAddPKPoint int32
	// HumanLevelDiffer 是"正当防卫"的等级差阈值（nHumanLevelDiffer=10）。
	// 超过它就不算正当防卫（ObjBase.pas:21112）。
	HumanLevelDiffer uint32
	// PKFlagTime 是被打后的正当防卫窗口（dwPKFlagTime=60s）。
	PKFlagTime time.Duration
	// DecPointInterval/DecPointCount 是 PK 点衰减（120s 减 1 点）。
	DecPointInterval time.Duration
	DecPointCount    int32
	// MaxPKPoint 是 PK 点上限（脚本 INCPPKPOINT 的 clamp，ObjNpc.pas:8170）。
	MaxPKPoint int32

	// ---- PK 死亡的等级/经验奖惩（TPlayObject.PKDie，ObjBase.pas:21076-21180）----
	//
	// 四个**开关**（原版 `boKillHumanWinLevel` / `boKilledLostLevel` /
	// `boKillHumanWinExp` / `boKilledLostExp`）出厂**全 False**
	//（M2Share.pas:1779-1782；官方 !setup.txt:937-944 也全是 0）。
	PKDieWinLevel  bool
	PKDieLostLevel bool
	PKDieWinExp    bool
	PKDieLostExp   bool
	// 四个**数值**（M2Share.pas:1783-1786，官方 !setup.txt:939-944 同值）。
	PKDieWinLevelPoint  uint32
	PKDieLostLevelPoint uint32
	PKDieWinExpPoint    uint32
	PKDieLostExpPoint   uint32
}

// DefaultConfig 返回原版出厂默认值。
func DefaultConfig() Config {
	return Config{
		SafeZoneSize:      10,
		RedHomeMap:        "3",
		RedHomeX:          839,
		RedHomeY:          668,
		RedPKProtectLevel: 10,
		LevelProtect:      false, // 原版出厂 False
		PKProtectLevel:    10,
		KillAddPKPoint:    100,
		HumanLevelDiffer:  10,
		PKFlagTime:        60 * time.Second,
		DecPointInterval:  120 * time.Second,
		DecPointCount:     1,
		MaxPKPoint:        10000,
		// PK 死亡奖惩：出厂**全关**（原版如此），数值取原版出厂值。
		PKDieWinLevel:       false,
		PKDieLostLevel:      false,
		PKDieWinExp:         false,
		PKDieLostExp:        false,
		PKDieWinLevelPoint:  1,
		PKDieLostLevelPoint: 1,
		PKDieWinExpPoint:    100000,
		PKDieLostExpPoint:   100000,
	}
}

// Actor 是判定所需的最少信息。
//
// 抽成结构体是为了让 internal/pvp 不依赖 entity/Player，
// 便于用纯数据单测覆盖所有判定组合。
type Actor struct {
	// ID 是 ActorId。
	ID uint32
	// Race 是种族。
	Race RaceServer
	// Level 是等级。
	Level uint32
	// GuildName 是所在行会名（无行会为空）。
	GuildName string
	// PkPoint 是 PK 罪点数（m_nPkPoint）。
	PkPoint int32
	// InSafeZone 为 true 表示位于安全区（由调用方按 InSafeZone 规则算好）。
	InSafeZone bool
	// InFreePKArea 对应 m_boInFreePKArea：自由 PK 区/攻城战区，
	// 由攻城战的 ChangePKStatus 开关（ObjBase.pas:1525）。
	InFreePKArea bool
	// PvpFlag 对应 m_boPKFlag：被打后的正当防卫标记。
	PvpFlag bool
	// PvpFlagUntil 是 PvpFlag 的到期时刻。
	PvpFlagUntil time.Time
	// LastMoveAt 是最近一次传送/换图的时刻（m_dwMapMoveTick）。
	//
	// 原版据此给"传送后 3 秒保护"：ObjBase.pas:21322-21323。
	LastMoveAt time.Time
	// AdminMode/StoneMode 对应 m_boAdminMode / m_boStoneMode：GM 隐身/石化免疫。
	AdminMode bool
	StoneMode bool
	// SameGroup 报告是否与攻击者同组（编组攻击用）。
	SameGroup bool
	// Master 指召唤兽的主人；玩家为 nil。
	Master *Actor
}

// PKLevel 是红名等级：每 100 点一级（ObjBase.pas:2234-2237）。
func PKLevel(pkPoint int32) int { return int(pkPoint / 100) }

// IsRedName 是否红名（PKLevel >= 2）。
func IsRedName(pkPoint int32) bool { return PKLevel(pkPoint) >= RedNameLevel }

// IncPKPoint 加 PK 点。
//
// 返回是否需要刷新名字颜色：原版只在跨 1/2 级边界时刷
// （ObjBase.pas:2366-2371 的 `if PKLevel <= 2`）。
func IncPKPoint(pkPoint *int32, n int32, max int32) bool {
	old := PKLevel(*pkPoint)
	*pkPoint += n
	if *pkPoint > max {
		*pkPoint = max
	}
	if *pkPoint < 0 {
		*pkPoint = 0
	}
	cur := PKLevel(*pkPoint)
	return cur != old && cur <= RedNameLevel
}

// DecPKPoint 减 PK 点（时间衰减，ObjBase.pas:18893-18904）。
func DecPKPoint(pkPoint *int32, n int32) bool {
	old := PKLevel(*pkPoint)
	*pkPoint -= n
	if *pkPoint < 0 {
		*pkPoint = 0
	}
	cur := PKLevel(*pkPoint)
	return cur != old && old > 0 && old <= RedNameLevel
}

// SetPKFlag 给被打者打上正当防卫标记（ObjBase.pas:21220-21236）。
//
// 原版条件：双方都不到红名、不在 FIGHT 区、且此前没有标记时才置位。
// 返回是否真的置位。
func SetPKFlag(attacker, victim *Actor, inFightZone, pvpFlagOn bool) bool {
	if PKLevel(victim.PkPoint) >= RedNameLevel || PKLevel(attacker.PkPoint) >= RedNameLevel {
		return false
	}
	if inFightZone || pvpFlagOn {
		return false
	}
	victim.PvpFlag = true
	return true
}

// CheckPKStatus 到期清除正当防卫标记（ObjBase.pas:18868-18875）。
// 返回是否清除了。
func CheckPKStatus(victim *Actor, now time.Time) bool {
	if victim.PvpFlag && !now.Before(victim.PvpFlagUntil) {
		victim.PvpFlag = false
		return true
	}
	return false
}

// IsGoodKilling 判定这次击杀是否属于"正当防卫"（ObjBase.pas:21251-21255）：
// 被打者当时处于 PK 标记期就不算罪。
func IsGoodKilling(victim *Actor) bool { return victim.PvpFlag }

// ---------- 三层判定链（ObjBase.pas:21495-21519）----------
//
//	攻击 → IsProperTarget
//	         ├─ IsAttackTarget   攻击模式 / 攻击对象种族
//	         └─ IsProtectTarget  安全区 / 等级保护（仅玩家 vs 玩家）
//
// ⚠️ IsProtectTarget 在 IsProperTarget 里是**赋值**（ObjBase.pas:21502），
// 之后 IsAttackTarget 的结论仍可被它覆盖；Master 分支是独立 if。

// IsAttackTarget 判定能否攻击目标（ObjBase.pas:21332-21493）。
//
// attackerPKAttackMode 是攻击者的攻击模式；sameGuild/sameAlly 由调用方
// 用实时的行会关系算出（**不要**读任何缓存标记，见包注释）。
func IsAttackTarget(cfg Config, attacker, target *Actor, mode AttackMode, sameGuild, sameAlly bool) bool {
	if target == nil || target.ID == attacker.ID {
		return false
	}
	// 召唤兽与主人同属一方时不互打（ObjBase.pas:21364）。
	if target.Master != nil && target.Master.ID == attacker.ID {
		return false
	}
	// 目标在安全区时，怪也不能打（ObjBase.pas:21367-21371）。
	if target.Race == RacePlay && target.InSafeZone {
		return false
	}

	switch target.Race {
	case RaceAnimal:
		return true
	case RacePlay:
		// 走攻击模式分支（ObjBase.pas:21385）。
		return isAttackTargetPlayer(mode, target, sameGuild, sameAlly)
	default:
		// NPC / 和平 NPC：只有全体攻击会打（ObjBase.pas:21388-21393）。
		return mode == HamAll
	}
}

// isAttackTargetPlayer 是玩家目标的攻击模式判定（ObjBase.pas:21385-21487）。
func isAttackTargetPlayer(mode AttackMode, target *Actor, sameGuild, sameAlly bool) bool {
	switch mode {
	case HamPeace:
		// 和平模式：玩家完全打不到玩家。
		return false
	case HamGroup:
		// 编组：非同组即可打。
		return !target.SameGroup
	case HamGuild:
		// 行会：同会成员与同盟行会免打。
		if sameGuild {
			return false
		}
		return !sameAlly
	case HamPKAttack:
		// 只打红名。
		return IsRedName(target.PkPoint)
	default: // HamAll
		return true
	}
}

// IsProtectTarget 判定"是否可以打这个玩家"（ObjBase.pas:21258-21331）。
//
// 注意参数方向：**self 是攻击者**，BaseObject 是目标。
// 原版 21263 只看**被攻击者**的 m_boInFreePKArea，不看攻击者。
func IsProtectTarget(cfg Config, attacker, target *Actor, now time.Time) bool {
	// ★ 安全区一律不能打（ObjBase.pas:21262）。
	if attacker.InSafeZone || target.InSafeZone {
		return false
	}
	// 目标在自由 PK 区（攻城战）时跳过全部等级保护（ObjBase.pas:21263）。
	if target.InFreePKArea {
		return true
	}
	// 通用等级保护（ObjBase.pas:21266-21280），出厂关闭。
	if cfg.LevelProtect {
		if attacker.Level > cfg.PKProtectLevel &&
			!target.PvpFlag && target.Level <= cfg.PKProtectLevel && !IsRedName(target.PkPoint) {
			return false
		}
		if attacker.Level <= cfg.PKProtectLevel &&
			!target.PvpFlag && target.Level > cfg.PKProtectLevel && !IsRedName(attacker.PkPoint) {
			return false
		}
	}
	// 红名保护（ObjBase.pas:21308-21330）：**无条件生效**，不受上面的开关影响。
	// 这是 1.5 复古版"10 级以下打不到人"的由来。
	if IsRedName(attacker.PkPoint) && attacker.Level > cfg.RedPKProtectLevel &&
		target.Level <= cfg.RedPKProtectLevel && !IsRedName(target.PkPoint) {
		return false
	}
	if attacker.Level <= cfg.RedPKProtectLevel && !IsRedName(attacker.PkPoint) &&
		IsRedName(target.PkPoint) && target.Level > cfg.RedPKProtectLevel {
		return false
	}
	// 传送保护：任一方刚传送 3 秒内互相不能打
	//（ObjBase.pas:21322-21323，m_dwMapMoveTick）。
	if now.Sub(attacker.LastMoveAt) < MoveProtectTime ||
		now.Sub(target.LastMoveAt) < MoveProtectTime {
		return false
	}
	return true
}

// IsProperTarget 汇总判定（ObjBase.pas:21495-21519）。
func IsProperTarget(cfg Config, attacker, target *Actor, mode AttackMode, sameGuild, sameAlly bool, now time.Time) bool {
	result := IsAttackTarget(cfg, attacker, target, mode, sameGuild, sameAlly)
	// 玩家 vs 玩家：IsProtectTarget 的结论**覆盖**前者。
	//
	// ⚠️ 原版这里有 `if Result then` 守卫（ObjBase.pas:21498）——**只有
	// IsAttackTarget 为真才走 IsProtectTarget**。漏掉它会让 IsProtectTarget
	// 把"和平模式拒绝"的 false 覆盖回 true，等于和平模式完全失效
	// （OpenMir2 的 `return IsProtectTarget(...)` 同样有这个错）。
	if result && attacker.Race == RacePlay && target.Race == RacePlay {
		result = IsProtectTarget(cfg, attacker, target, now)
	}
	// 目标不是玩家但有主人（召唤兽）时，改判它的主人（ObjBase.pas:21505-21516）。
	if target.Master != nil && target.Race != RacePlay {
		result = IsAttackTarget(cfg, attacker, target.Master, mode, sameGuild, sameAlly)
		if IsProtectTarget(cfg, attacker, target, now) {
			result = false
		}
	}
	// 任一方在安全区则不能打召唤兽（ObjBase.pas:21516）。
	if target.Master != nil && (attacker.InSafeZone || target.InSafeZone) {
		result = false
	}
	// GM 隐身 / 石头模式免疫（ObjBase.pas:21491）。
	if target.AdminMode || target.StoneMode {
		result = false
	}
	return result
}

// ---------- 行会关系（ObjBase.pas:2346-2360 的 GetGuildRelation）----------

// GuildRelation 是两个行会之间的关系。
type GuildRelation int

const (
	// RelationNone 无关系（0）。
	RelationNone GuildRelation = iota
	// RelationSameGuild 同会（1）。
	RelationSameGuild
	// RelationWarGuild 敌对行会（2）——**互相击杀免罪**。
	RelationWarGuild
	// RelationAllyGuild 同盟（3）。
	RelationAllyGuild
)

// Relations 一次性算出攻击者与目标行会的关系。
//
// ⚠️ 与原版的差异：原版 GetGuildRelation 有两个副作用
//
//	—— 重置 m_boGuildWarArea，且在安全区直接 Exit（ObjBase.pas:2351）。
//	我们不引入这个全局标记，改为把结果返回给调用方，
//	免得 IsAttackTarget 读到"上次刷颜色留下的陈旧值"。
//
// 敌对判定用行会战记录（storage.Guild.Wars，到期即失效）。
func Relations(a, b *storage.Guild, aInSafeZone bool) (same, ally, war bool) {
	if a == nil || b == nil {
		return false, false, false
	}
	// 安全区不进入行会战关系（ObjBase.pas:2351）。
	if aInSafeZone {
		return a.Name == b.Name, false, false
	}
	if a.Name == b.Name {
		return true, false, false
	}
	for _, al := range a.Allies {
		if al == b.Name {
			ally = true
			break
		}
	}
	for _, al := range b.Allies {
		if al == a.Name {
			ally = true
			break
		}
	}
	// IsWarGuild 双向判定（Guild.pas:414-427）：双方记录里都有对方。
	war = hasWar(a, b.Name) && hasWar(b, a.Name)
	return same, ally, war
}

func hasWar(g *storage.Guild, enemy string) bool {
	for _, w := range g.Wars {
		if w.Name == enemy {
			return true
		}
	}
	return false
}

// ---------- 安全区（ObjBase.pas:21527-21562 的 InSafeZone）----------

// ZoneMap 是判断安全区所需的地图信息。
type ZoneMap struct {
	// Name 是地图号。
	Name string
	// Safe 来自 mapinfo.txt 的 SAFE 关键字。
	Safe bool
	// FightZone 来自 FIGHT 关键字。
	FightZone bool
	// Fight3Zone 来自 FIGHT3 关键字（工会战区）。
	Fight3Zone bool
	// Quiz 来自 QUIZ 关键字。
	//
	// 目前唯一用途：**带 QUIZ 的图不允许喊话**
	//（ObjBase.pas:8699 `if not m_PEnvir.Flag.boQUIZ then`，否则回
	// "本地图不允许喊话！！！"）。官方 mapinfo 里有 7 张图带它
	//（质询屋 G001/G002、热血足球场 G004/G006/G008/G009…）。
	Quiz bool
}

// SuppressDeathDrop 报告这张图上死亡是否**不掉任何东西**。
//
// 原版 `ObjBase.pas:20983` 把整段死亡掉落包在
//
//	if (not m_PEnvir.Flag.boFIGHTZone) and
//	   (not m_PEnvir.Flag.boFIGHT3Zone) and
//	   (not m_boAnimal) then
//
// 里——`AddBodyLuck`（扣幸运）也在同一段。所以 **PK 区（FIGHT）与
// 工会战区（FIGHT3，官方是 F001-F010 行会战争地图 + G003/G005 热血足球场）
// 内死亡不掉落、不掉幸运**；掉装备的 `DropUseItems`/`ScatterBagItems`
// 全在这个门里。
//
// `m_boAnimal` 是召唤兽/动物标记，只影响"死亡的是怪物"那一支（我们走
// 的是玩家路径，不涉及）；这里不表示它。
//
// nil（地图没有 mapinfo 记录）按"没有标记"处理，即照常掉落。
func (z *ZoneMap) SuppressDeathDrop() bool {
	return z != nil && (z.FightZone || z.Fight3Zone)
}

// InSafeZone 判定某点是否在安全区（ObjBase.pas:21527-21562），三段取或：
//
//  1. 地图属性 SAFE（MapFlag.boSAFE）；
//  2. 红名监狱点半径内（sRedHomeMap + nRedHomeX/Y，半径 SafeZoneSize）；
//  3. 该地图任一出生点半径内（半径 SafeZoneSize）。
//
// 这就是为什么主城不用 SAFE 关键字也安全——出生点周围自动是安全区。
func InSafeZone(cfg Config, zm *ZoneMap, x, y int, spawns []SpawnPoint) bool {
	if zm == nil {
		return false
	}
	// ① 地图属性
	if zm.Safe {
		return true
	}
	// ② 红名监狱点（原版就是拿红名监狱当安全区，照抄）
	if zm.Name == cfg.RedHomeMap &&
		abs(x-cfg.RedHomeX) <= cfg.SafeZoneSize &&
		abs(y-cfg.RedHomeY) <= cfg.SafeZoneSize {
		return true
	}
	// ③ 出生点半径
	for _, sp := range spawns {
		if sp.MapID != zm.Name {
			continue
		}
		if abs(x-sp.X) <= cfg.SafeZoneSize && abs(y-sp.Y) <= cfg.SafeZoneSize {
			return true
		}
	}
	return false
}

// SpawnPoint 是出生点（对应 TStartPoint，StartPoint.txt 的一项）。
type SpawnPoint struct {
	MapID string
	X, Y  int
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// PKDieReward 计算一次 PK 死亡的等级/经验奖惩（`TPlayObject.PKDie`，
// ObjBase.pas:21076-21180 —— 连同两段分支的**嵌套**关系）。
//
// 返回要给**赢家加**与从**输家扣**的 (等级, 经验)。
//
// ⚠️ 三处最容易抄错的地方，这里按原版原样写死：
//
//  1. **等级差保护在最前**（:21112-21135）：`killer.Level - victim.Level >
//     HumanLevelDiffer` 时原版直接 `Exit` ⇒ **四项全为 0**（只留 PK 点/谋杀罪那套）。
//     注意这里用的是"赢家高出多少"，不是绝对差。
//  2. 两对开关是**嵌套**的（等级一段、经验一段）：
//     if boWinLevel then begin 加级;  if boLostLevel then 扣级 end;
//     if boWinExp   then begin 加经验; if boLostExp  then 扣经验 end;
//     ⇒ 只开"输家掉级"而没开"赢家升级"时，**掉级根本不会发生**。
//     这不是笔误，是原版的控制流；别"顺手修"成两个并列 if。
//  3. 输家掉级量看**红名**（:21150-21157）：`victimPKLevel >= 2` ⇒ 扣 **2 倍**。
func PKDieReward(cfg Config, killerLevel, victimLevel uint32, victimPKLevel int) (winLevel, lostLevel, winExp, lostExp uint32) {
	// ① 等级差保护
	if killerLevel > victimLevel && killerLevel-victimLevel > cfg.HumanLevelDiffer {
		return 0, 0, 0, 0
	}
	// ② 等级一段（嵌套）
	if cfg.PKDieWinLevel {
		winLevel = cfg.PKDieWinLevelPoint
		if cfg.PKDieLostLevel {
			lostLevel = cfg.PKDieLostLevelPoint
			if victimPKLevel >= RedNameLevel {
				lostLevel *= 2
			}
		}
	}
	// ② 经验一段（同样是嵌套的）
	if cfg.PKDieWinExp {
		winExp = cfg.PKDieWinExpPoint
		if cfg.PKDieLostExp {
			lostExp = cfg.PKDieLostExpPoint
		}
	}
	return
}

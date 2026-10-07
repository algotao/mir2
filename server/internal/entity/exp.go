package entity

// 经验与升级。
//
// 原版经验表在 `Exps.ini` 的 [Exp] 段（`LevelN` = 从 N 级升到 N+1 级所需），
// 由 M2Share.pas:5145-5155 加载到 `g_Config.dwNeedExps[]`。装载入口是
// `SetLevelNeed`（gamesvr 启动时用 `data.LoadExps` 读进来）。
//
// ⚠️ **存储用累计、显示用每级**：
// `pb.Ability.Exp` 一直是**累计**值（历史实现如此，改了要迁档），而原版与客户端
// 用的是**每级内**的 Exp/MaxExp（客户端经验条 `FState.pas:2885` 就是
// `100 * Exp / MaxExp`）。两者在本文件里换算：`NeedExp` 累计、
// `LevelNeed`/`LevelProgress` 给客户端。
//
// ⚠️ 表缺失时（单测/无外部数据）`NeedExp` 退回旧公式 100·(L-1)²，
// 行为与改造前**逐位一致**（见 fallbackNeedAt 的推导）。

// MaxLevel 是等级上限。
const MaxLevel = 100

var (
	// levelNeed 是官方每级需求（下标=等级，[0] 未用）。
	levelNeed []uint64
	// levelCum 是 levelNeed 的前缀和，让 NeedExp 变成 O(1)。
	levelCum []uint64
)

// SetLevelNeed 装入官方每级经验表（`data.LoadExps` 的产物）。
//
// need[L] 必须表示"从 L 级升到 L+1 级所需经验"；need[0] 未用。
// 传 nil 表示退回公式。
func SetLevelNeed(need []uint64) {
	levelNeed = need
	n := len(need)
	if n == 0 {
		levelCum = nil
		return
	}
	// levelCum[L] = 升到 L 级所需的**累计**经验 = Σ_{k=1..L-1} needAt(k)。
	levelCum = make([]uint64, n)
	for i := 2; i < n; i++ {
		levelCum[i] = levelCum[i-1] + needAt(uint32(i-1))
	}
}

// LevelNeed 返回从 level 级升到 level+1 级所需的经验。
//
// 这就是发给客户端的 `TAbility.MaxExp`（原版 `GetLevelExp(Level)`）。
func LevelNeed(level uint32) uint64 { return needAt(level) }

// LevelProgress 把**累计**经验换算成客户端要的"当前级内经验"。
//
// 原版 `m_Abil.Exp` 本来就是级内值（升级时 `Dec(Exp, MaxExp)`）；
// 我们存的是累计值，所以要减去本级起点。返回值恒 < LevelNeed(level)。
func LevelProgress(cumExp uint64, level uint32) uint64 {
	base := NeedExp(level)
	if cumExp <= base {
		return 0
	}
	return cumExp - base
}

// needAt 返回 L→L+1 的需求：官方表里有就用，否则退回公式差分。
func needAt(level uint32) uint64 {
	if level == 0 {
		return 0
	}
	if int(level) < len(levelNeed) && levelNeed[level] > 0 {
		return levelNeed[level]
	}
	return fallbackNeedAt(level)
}

// fallbackNeedAt 是旧公式的**逐级差分**：100·(2L-1)。
//
// 旧 `NeedExp(L) = 100·(L-1)²` ⇒ `NeedExp(L)-NeedExp(L-1) = 100·(2L-1)`，
// 所以由它累加出来的累计值与旧公式完全一致（L1→100、L2→300、L3→500…）。
func fallbackNeedAt(level uint32) uint64 {
	return 100 * (2*uint64(level) - 1)
}

// NeedExp 返回升到 level 级所需的**累计**经验。
func NeedExp(level uint32) uint64 {
	if level <= 1 {
		return 0
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	if int(level) < len(levelCum) {
		return levelCum[level]
	}
	l := uint64(level - 1)
	return l * l * 100
}

// LevelUpResult 是一次升级结算的结果。
type LevelUpResult struct {
	// NewLevel 是升级后的等级。
	NewLevel uint32
	// Gained 是本次升了多少级。
	Gained uint32
	// HPUp / MPUp 是本次升级带来的血量/蓝量增量。
	HPUp uint32
	MPUp uint32
}

// CheckLevelUp 按累计经验计算应达到的等级。
//
// exp 是**累计**经验（不是本级的）。循环升级，直到经验不足以再升。
func CheckLevelUp(curLevel uint32, exp uint64) LevelUpResult {
	res := LevelUpResult{NewLevel: curLevel}
	if curLevel >= MaxLevel {
		return res
	}
	totalHP, totalMP := uint32(0), uint32(0)
	// ⚠️ 只在循环体内自增一次：若同时依赖 for 的 post 语句会每次跳 2 级，
	// 表现为"经验够了却升不满"。
	lvl := curLevel
	for lvl < MaxLevel && exp >= NeedExp(lvl+1) {
		lvl++
		res.NewLevel = lvl
		res.Gained++
		hp, mp := GrowthFor(lvl)
		totalHP += hp
		totalMP += mp
	}
	res.HPUp, res.MPUp = totalHP, totalMP
	return res
}

// GrowthFor 返回升到 level 级时增加的最大 HP / MP。
//
// 按等级线性递增（真实传奇是分段曲线，这里取简化）。
// InitialHPMP 返回某职业 **1 级的初始 HP/MP**。
//
// ⚠️ 它是"等级上限"公式的**起点**，不是可有可无的常数：
// `GrowthFor` 给的是"升到下一级的增量"，所以 N 级的上限 =
//
//	InitialHPMP(job) + Σ_{lv=1}^{N-1} GrowthFor(lv)
//
// 漏掉起点会让 1 级角色的上限变成 0（实测：重算后 HP 20 → 1）。
// 建角（accountsvc）与上限重算（gamesvr）都走这里，保证只有一份来源。
func InitialHPMP(job uint32) (hp, mp uint32) {
	switch job {
	case 0: // 战士
		return 20, 5
	case 1: // 法师
		return 15, 20
	default: // 道士
		// ⚠️ MP 不能太低：道士的核心技能（魔法盾 20、召唤骷髅 16、
		// 幽灵盾/神圣战甲 15）都要耗蓝，给 15 会导致 1 级道士放不出魔法盾。
		return 17, 40
	}
}

func GrowthFor(level uint32) (hp, mp uint32) {
	hp, mp = 5+level/5, 2+level/10
	return hp, mp
}

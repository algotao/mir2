// Package castle 实现沙巴克城堡（P6）。
//
// 对应 Delphi TUserCastle（Castle.pas）与 TCastleManager（Castle.pas:128-155）。
//
// # 与原版的三处关键差异（都是原版的坑，不是我们的选择）
//
//  1. **没有状态枚举**。原版的攻守期由两个 Boolean + 一个 tick 组成：
//     m_boStartWar（当日到点）、m_boUnderWar（正在攻城）、
//     m_dwStartCastleWarTick（开战时刻）。此处照抄这个三元组，
//     但把 tick 换成**绝对时刻** WarStartAt——原版的 GetTickCount
//     差值在重启后立刻失真。
//
//  2. **战期状态不落库**。原版也存在内存里，且攻城期只有 3 小时，
//     跨重启必然作废；恢复时按"未开战"处理。
//
//  3. **StopWar 不踢人**。原版 StopWallconquestWar（Castle.pas:864-889）
//     声明了 ListC 却没传给 GetMapOfRangeHumanCount，循环体是死代码，
//     所以"攻城结束踢人 + 关 PK"从未真正执行过（OpenMir2 原样继承了它）。
//     我们不复刻这个 bug：PK 状态由玩家 tick 按 UnderWar **现算**，
//     攻城一结束自动解除，不存在需要清理的历史状态。
//
// # 依赖方向
//
// Castle → Guild（与原版一致，Guild.pas 里零个 Castle 引用）。
// 这里只依赖一个 Find 接口，由 internal/guild.Manager 适配，
// 避免 internal/castle 反向依赖 internal/guild。
package castle

import (
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// Config 是城堡玩法参数。
//
// 默认值全部取自 g_Config（Common/M2Share.pas:1754-1759、2090-2102），
// 由 DefaultConfig 给出，可整体覆盖。
type Config struct {
	// StartWarHour 是开城时刻（小时，0..23）。原版只比"小时"，
	// 一旦某个 tick 落在这个小时就开战，当天不再重复（Castle.pas:652）。
	StartWarHour int
	// WarDuration 是攻守期总时长（dwCastleWarTime=3h，Castle.pas:700）。
	WarDuration time.Duration
	// EndMsgBefore 是提前广播"还剩 N 分钟"的时间（dwShowCastleWarEndMsgTime=10min）。
	EndMsgBefore time.Duration
	// GetCastleDelay 是开战多久后允许攻陷（dwGetCastleTime=10min，Castle.pas:805）。
	//
	// 作用是给守方反应时间；期间攻方进皇宫也占不下城堡。
	GetCastleDelay time.Duration
	// DeclareDays 是宣战到开战的提前天数（nStartCastleWarDays=4，Castle.pas:1228）。
	DeclareDays int
	// TaxRate 是税收比例，**百分数**（nCastleTaxRate=5，Castle.pas:1030 除以 100）。
	TaxRate int
	// OneDayGoldMax 是单日收税上限（nCastleOneDayGold=2000000）。
	OneDayGoldMax int64
	// GoldMax 是金库上限（nCastleGoldMax=10000000）。
	GoldMax int64
	// RepairDoorPrice 是修门费用（nRepairDoorPrice=2000000）。
	RepairDoorPrice int64
	// RepairWallPrice 是修墙费用（nRepairWallPrice=500000）。
	RepairWallPrice int64
	// HireGuardPrice 是雇佣一名守卫的费用（nHireGuardPrice=300000）。
	HireGuardPrice int64
	// HireArcherPrice 是雇佣一名弓箭手的费用（nHireArcherPrice=300000）。
	HireArcherPrice int64
	// RepairImmunity 是修门/修墙的被攻击免疫时长（Castle.pas:1158 硬编码 60s）。
	RepairImmunity time.Duration
}

// DefaultConfig 返回原版出厂默认值。
func DefaultConfig() Config {
	return Config{
		StartWarHour:    20,
		WarDuration:     3 * time.Hour,
		EndMsgBefore:    10 * time.Minute,
		GetCastleDelay:  10 * time.Minute,
		DeclareDays:     4,
		TaxRate:         5,
		OneDayGoldMax:   2_000_000,
		GoldMax:         10_000_000,
		RepairDoorPrice: 2_000_000,
		RepairWallPrice: 500_000,
		HireGuardPrice:  300_000,
		HireArcherPrice: 300_000,
		RepairImmunity:  60 * time.Second,
	}
}

// Attacker 是一条宣战记录（对应 TAttackerInfo，Castle.pas:33-38）。
//
// 是 storage.CastleAttacker 的别名——宣战队列要落库，不另立一份类型，
// 免得每次转换都要记得同步字段。
type Attacker = storage.CastleAttacker

// Castle 是一座城堡的完整状态（内存态，对应 TUserCastle，Castle.pas:39-126）。
//
// 持久化字段存 storage.Castle；本类型在其上叠加运行期状态。
type Castle struct {
	cfg Config

	// rec 是落库用的数据副本。
	rec storage.Castle

	// --- 运行期战期状态（不落库）---

	// startWar 对应 m_boStartWar：当日开城时刻已过。
	startWar bool
	// underWar 对应 m_boUnderWar：正在攻城。
	underWar bool
	// showOverMsg 对应 m_boShowOverMsg：已广播过"还剩 N 分钟"。
	showOverMsg bool
	// warStart 对应 m_dwStartCastleWarTick（换算成绝对时刻）。
	warStart time.Time
	// participants 是本场攻城方行会名（对应 m_AttackGuildList）。
	//
	// ⚠️ **含守方**：原版开战时把 m_MasterGuild 也塞进这个列表
	// （Castle.pas:670），InPalaceGuildCount 直接返回它的长度。
	participants []string

	// dirty 表示自上次落库后有变更。
	dirty bool
}

// New 由持久化记录构造运行期状态。
func New(cfg Config, rec storage.Castle) *Castle {
	return &Castle{cfg: cfg, rec: rec}
}

// Config 返回该城堡使用的玩法参数。
func (c *Castle) Config() Config { return c.cfg }

// Record 导出落库用的记录副本。
func (c *Castle) Record() storage.Castle { return c.rec }

// Dirty 报告自上次落库后是否有变更。
func (c *Castle) Dirty() bool { return c.dirty }

// MarkClean 清掉变更标记（落库成功后调用）。
func (c *Castle) MarkClean() { c.dirty = false }

// ---------- 只读访问 ----------

// Name 是城堡名（沙巴克）。
func (c *Castle) Name() string { return c.rec.Name }

// ConfigDir 是配置子目录（单机固定 "0"）。
func (c *Castle) ConfigDir() string { return c.rec.ConfigDir }

// OwnGuild 是占领行会名，空表示无主。
func (c *Castle) OwnGuild() string { return c.rec.OwnGuild }

// UnderWar 报告是否正在攻城。
func (c *Castle) UnderWar() bool { return c.underWar }

// StartWar 报告当日是否已过开城时刻。
func (c *Castle) StartWar() bool { return c.startWar }

// WarStart 是本场开战的时刻；未开战时为零值。
func (c *Castle) WarStart() time.Time { return c.warStart }

// WarLeft 返回攻守期剩余时长；未开战时返回战期全长。
func (c *Castle) WarLeft(now time.Time) time.Duration {
	if !c.underWar {
		return c.cfg.WarDuration
	}
	if left := c.cfg.WarDuration - now.Sub(c.warStart); left > 0 {
		return left
	}
	return 0
}

// TotalGold 是金库余额。
func (c *Castle) TotalGold() int64 { return c.rec.TotalGold }

// TodayIncome 是当日已收税额。
func (c *Castle) TodayIncome() int64 { return c.rec.TodayIncome }

// Attackers 是宣战队列（返回副本）。
func (c *Castle) Attackers() []Attacker {
	out := make([]Attacker, len(c.rec.Attackers))
	copy(out, c.rec.Attackers)
	return out
}

// Participants 返回本场攻城方行会名（**含守方**，与原版 m_AttackGuildList 一致）。
func (c *Castle) Participants() []string {
	out := make([]string, len(c.participants))
	copy(out, c.participants)
	return out
}

// ChangeDate 是换主时刻。
func (c *Castle) ChangeDate() time.Time { return c.rec.ChangeDate }

// PalaceCount 是 InPalaceGuildCount 的等价物： participants 的长度
// （原版 Castle.pas:891-894，含守方，所以恒 >= 1）。
func (c *Castle) PalaceCount() int { return len(c.participants) }

// ---------- 战区判定 ----------

// InWarArea 判定某点是否属于城堡战区（Castle.pas:734-760）。
//
// ⚠️ 原版三条规则**都不看是否在攻城期**，照抄的后果是：和平时期进皇宫地图
// （0150）也会被判成战区，从而影响 PK 状态（见 gamesvr 的 castlePlayerTick）。
// 这是原版行为，不是 bug 修复项。
func (c *Castle) InWarArea(mapName string, x, y int) bool {
	// 1. 战场地图上的攻守区域（以回城点为中心，半径 WarRangeX/Y）。
	if mapName == c.rec.MapName &&
		abs(c.rec.HomeX-x) < c.rec.WarRangeX &&
		abs(c.rec.HomeY-y) < c.rec.WarRangeY {
		return true
	}
	// 2. 皇宫与密道：无条件算战区。
	if mapName == c.rec.PalaceMap || mapName == c.rec.SecretMap {
		return true
	}
	// 3. 归属地图列表（0151..0156）。
	for _, m := range c.rec.ExtraMaps {
		if mapName == m {
			return true
		}
	}
	return false
}

// HomeMap 是城堡的行会回城地图（原版 `Castle.m_sHomeMap`）。
func (c *Castle) HomeMap() string { return c.rec.HomeMap }

// GetHomeX/GetHomeY 是行会回城点，原版带 ±4 随机抖动（Castle.pas:921-929）。
// rnd 必须非 nil。
func (c *Castle) HomePos(rnd func(int) int) (int, int) {
	return c.rec.HomeX - 4 + rnd(9), c.rec.HomeY - 4 + rnd(9)
}

// ---------- 阵营判定 ----------

// GuildResolver 解析行会（由 internal/guild.Manager 适配）。
type GuildResolver interface {
	// Find 按行会名取行会副本；不存在返回 nil。
	Find(name string) *storage.Guild
}

// IsMasterGuild 是否占领方（Castle.pas:912-917）。
//
// ⚠️ **攻城期外也返回 true**：原版只比指针，不看 m_boUnderWar。
func (c *Castle) IsMasterGuild(name string) bool {
	return c.rec.OwnGuild != "" && c.rec.OwnGuild == name
}

// IsMember 是否占领方成员（Castle.pas:762-765）。
func (c *Castle) IsMember(guildName string) bool { return c.IsMasterGuild(guildName) }

// IsAttackGuild 是否本场攻城方（Castle.pas:785-800）。
//
// ⚠️ 同 IsMasterGuild，原版不检查 underWar。
func (c *Castle) IsAttackGuild(name string) bool {
	if name == "" || c.IsMasterGuild(name) {
		return false
	}
	for _, p := range c.participants {
		if p == name {
			return true
		}
	}
	return false
}

// IsAttackAllyGuild 是否某个攻城方行会的盟友（Castle.pas:768-783）。
func (c *Castle) IsAttackAllyGuild(g GuildResolver, name string) bool {
	if name == "" {
		return false
	}
	for _, p := range c.participants {
		if p == c.rec.OwnGuild {
			continue
		}
		if allyOf(g, p, name) {
			return true
		}
	}
	return false
}

// IsDefenseGuild 是否守方（Castle.pas:905-910）。**攻城期外一律 false。**
func (c *Castle) IsDefenseGuild(name string) bool {
	return c.underWar && c.IsMasterGuild(name)
}

// IsDefenseAllyGuild 是否守方盟友（Castle.pas:896-902）。**攻城期外一律 false。**
func (c *Castle) IsDefenseAllyGuild(g GuildResolver, name string) bool {
	if !c.underWar || c.rec.OwnGuild == "" || name == "" {
		return false
	}
	return allyOf(g, c.rec.OwnGuild, name)
}

// allyOf 判断 guildName 是否与 other 互为盟友。
//
// 原版 IsAllyGuild（Guild.pas:377-386）遍历 GuildAllList，
// 联盟是**双向**写入的（见 guild.Manager.AddAlly），所以单边查即可。
func allyOf(g GuildResolver, guildName, other string) bool {
	if g == nil || guildName == "" || other == "" {
		return false
	}
	gu := g.Find(guildName)
	if gu == nil {
		return false
	}
	for _, a := range gu.Allies {
		if a == other {
			return true
		}
	}
	return false
}

// ---------- 攻陷 ----------

// CanGetCastle 攻陷判定（Castle.pas:802-826）。
//
// 四重与：
//  1. 开战已超过 GetCastleDelay（给守方反应时间）；
//  2. 该行会是本场攻城方；
//  3. 该行会不是占领方；
//  4. 皇宫里所有**活着的**人（含无行会者）都属于该行会。
//
// ⚠️ 与等级、PK 点、装备**完全无关**——原版只看行会归属。
// aliveInPalace 由调用方给出（皇宫地图内所有不死玩家的行会名，无行会为空串）。
func (c *Castle) CanGetCastle(g GuildResolver, guildName string, now time.Time, aliveInPalace []string) bool {
	if !c.underWar {
		return false
	}
	if now.Sub(c.warStart) <= c.cfg.GetCastleDelay {
		return false
	}
	if !c.IsAttackGuild(guildName) || c.IsMember(guildName) {
		return false
	}
	for _, g := range aliveInPalace {
		if g != guildName {
			return false
		}
	}
	return true
}

// GetCastle 换主（Castle.pas:828-846），返回**旧占领行会名**（空表示原来无主）。
//
// 原版换主后**不**重置 underWar，由调用方按 InPalaceGuildCount <= 1 决定
// 是否 StopWar（ObjBase.pas:6540-6541）——所以守方也能反攻，本函数不代劳。
func (c *Castle) GetCastle(guildName string, now time.Time) string {
	old := c.rec.OwnGuild
	c.rec.OwnGuild = guildName
	c.rec.ChangeDate = now
	c.dirty = true
	return old
}

// ---------- 宣战 ----------

// InAttackerList 该行会是否已在宣战队列（Castle.pas:1236-1249）。
func (c *Castle) InAttackerList(guildName string) bool {
	for _, a := range c.rec.Attackers {
		if a.GuildName == guildName {
			return true
		}
	}
	return false
}

// AddAttacker 登记宣战（Castle.pas:1220-1234）。
//
// 预定攻城日 = now + DeclareDays。已宣战过则返回 false（原版直接 Exit）。
//
// ⚠️ 原版宣战**不扣金币**，只扣 NPC 层检查的"祖玛碎片"道具；
// 宣战后到开战日之间不能取消也不能退款。
func (c *Castle) AddAttacker(guildName string, now time.Time) bool {
	if guildName == "" || c.InAttackerList(guildName) {
		return false
	}
	c.rec.Attackers = append(c.rec.Attackers, Attacker{
		GuildName:  guildName,
		AttackDate: now.AddDate(0, 0, c.cfg.DeclareDays),
	})
	c.dirty = true
	return true
}

// CancelAttacker 撤销宣战。
//
// ⚠️ 原版**没有**这个接口（宣战不可撤）。保留给 GM 纠错用。
func (c *Castle) CancelAttacker(guildName string) bool {
	for i, a := range c.rec.Attackers {
		if a.GuildName == guildName {
			c.rec.Attackers = append(c.rec.Attackers[:i:i], c.rec.Attackers[i+1:]...)
			c.dirty = true
			return true
		}
	}
	return false
}

// ---------- 税收 ----------

// 存取金返回码（Castle.pas:1069-1134）。
// NPC 侧据此弹中文文案（OpenMir2 CastleOfficial.cs:159-194）。
const (
	// GoldOK 成功。
	GoldOK = 1
	// GoldNoRight 不是占领方掌门。
	GoldNoRight = -1
	// GoldNoFund 金库或钱包里的钱不够。
	GoldNoFund = -2
	// GoldTooMuch 超过携带/存放上限。
	GoldTooMuch = -3
	// GoldBadArgs 金额非法（<= 0）。
	GoldBadArgs = -4
)

// IncRateGold 从一笔交易里抽税入库，返回实际抽取的金额。
//
// （Castle.pas:1022-1066）
//
// 两级上限：先受当日累计（OneDayGoldMax）约束，再受金库总额（GoldMax）约束。
// ⚠️ 原版只写 m_nTotalGold，不动玩家金币——税是"少给 NPC"而不是"收走玩家的钱"，
// 调用方负责把 nInGold 从给玩家的金额里扣掉。
func (c *Castle) IncRateGold(gold int64) int64 {
	if gold <= 0 {
		return 0
	}
	// Round(gold * rate / 100)：先乘后除，与原版整数运算顺序一致。
	n := gold * int64(c.cfg.TaxRate) / 100
	if n <= 0 {
		return 0
	}
	// 单日上限：超出部分截断（Castle.pas:1032-1041）。
	if room := c.cfg.OneDayGoldMax - c.rec.TodayIncome; room <= 0 {
		return 0
	} else if n > room {
		n = room
	}
	c.rec.TodayIncome += n
	// 金库上限（Castle.pas:1043-1046）。
	if c.rec.TotalGold+n > c.cfg.GoldMax {
		c.rec.TotalGold = c.cfg.GoldMax
	} else {
		c.rec.TotalGold += n
	}
	c.dirty = true
	return n
}

// Wallet 是取存金时用来判断"拿不拿得动"的钱包信息。
type Wallet struct {
	// Gold 是玩家当前金币。
	Gold int64
	// MaxGold 是玩家金币上限（原版 m_nGoldMax）。
	MaxGold int64
}

// WithDrawalGolds 掌门从金库取款（Castle.pas:1069-1101）。
//
// 权限：占领方 + rank 1 掌门（isChief 由调用方判定，等价原版 m_nGuildRankNo = 1）。
func (c *Castle) WithDrawalGolds(guildName string, isChief bool, gold int64, w Wallet) int {
	switch {
	case !c.IsMasterGuild(guildName) || !isChief:
		return GoldNoRight
	case gold <= 0:
		return GoldBadArgs
	case gold > c.rec.TotalGold:
		return GoldNoFund
	case w.Gold+gold > w.MaxGold:
		return GoldTooMuch
	}
	c.rec.TotalGold -= gold
	c.dirty = true
	return GoldOK
}

// ReceiptGolds 掌门向金库交税（Castle.pas:1103-1134）。
func (c *Castle) ReceiptGolds(guildName string, isChief bool, gold int64, w Wallet) int {
	switch {
	case !c.IsMasterGuild(guildName) || !isChief:
		return GoldNoRight
	case gold <= 0:
		return GoldBadArgs
	case gold > w.Gold:
		return GoldNoFund
	case c.rec.TotalGold+gold > c.cfg.GoldMax:
		return GoldTooMuch
	}
	c.rec.TotalGold += gold
	c.dirty = true
	return GoldOK
}

// ---------- 状态机 ----------

// Hooks 是 Run 的副作用回调。
//
// 状态机本身不碰网络、不碰数据库，所有对外动作都经这里抛出去——
// 这样 internal/castle 可以被单测直接驱动。
type Hooks struct {
	// OnDayChange 跨日：当日税收清零、重新允许开战。
	OnDayChange func(c *Castle)
	// OnWarStart 开战（广播 + 关门 + 刷新头顶行会名）。
	OnWarStart func(c *Castle)
	// OnEndingSoon 提前广播"还有 N 分钟"，left 是剩余时长。
	OnEndingSoon func(c *Castle, left time.Duration)
	// OnWarEnd 攻城结束。
	OnWarEnd func(c *Castle)
	// OnOccupantChanged 占领方变更（oldGuild 为旧占领行会名，空表示原来无主）。
	OnOccupantChanged func(c *Castle, oldGuild string)
}

// Run 推进一次状态机。
//
// 对应 TUserCastle.Run（Castle.pas:619-726）。原版由 svMain 每 10 秒调一次
// （svMain.pas:1701-1706），我们的调用方也应保持同量级。
func (c *Castle) Run(now time.Time, h Hooks) {
	// Step 0 跨日重置（Castle.pas:636-643）。
	if !sameDay(now, c.rec.IncomeToday) {
		c.rec.TodayIncome = 0
		c.rec.IncomeToday = now
		c.startWar = false
		c.dirty = true
		if h.OnDayChange != nil {
			h.OnDayChange(c)
		}
	}

	// Step 1 开门判定（Castle.pas:644-679）。
	//
	// 原版只比"小时"，所以 20:00~20:59 之间任意一个 tick 命中即开战；
	// startWar 一旦置位当天不再重复，跨日才复位。
	if !c.startWar && !c.underWar && now.Hour() == c.cfg.StartWarHour {
		c.startWar = true
		if promoted := c.promoteAttackers(now); len(promoted) > 0 {
			c.underWar = true
			c.showOverMsg = false
			c.rec.WarDate = now
			c.warStart = now
			// ★守方也要进名单（原版 Castle.pas:670）——
			// PalaceCount 正是靠它保证 >= 1。
			c.participants = promoted
			if c.rec.OwnGuild != "" {
				c.participants = append(c.participants, c.rec.OwnGuild)
			}
			c.dirty = true
			if h.OnWarStart != nil {
				h.OnWarStart(c)
			}
		}
	}

	// Step 3 攻城期推进（Castle.pas:694-715）。
	if c.underWar {
		elapsed := now.Sub(c.warStart)
		if !c.showOverMsg && elapsed > c.cfg.WarDuration-c.cfg.EndMsgBefore {
			c.showOverMsg = true
			if h.OnEndingSoon != nil {
				h.OnEndingSoon(c, c.cfg.WarDuration-elapsed)
			}
		}
		if elapsed > c.cfg.WarDuration {
			c.StopWar(h)
		}
	}
}

// promoteAttackers 把宣战队列里"预定日就是今天"的行会提进本场名单。
//
// 返回提上来的行会名（空表示今晚没有到期的宣战，不开战）。
//
// 两条与原版的差异：
//  1. 原版倒序遍历 + Dispose + Delete；这里用新切片重建，等价且更安全。
//  2. 原版只删"当天命中"的条目，**过期未打的宣战会永久滞留在文件里**。
//     这里顺手丢弃 AttackDate 已过的条目，避免队列无限增长。
func (c *Castle) promoteAttackers(now time.Time) []string {
	if len(c.rec.Attackers) == 0 {
		return nil
	}
	kept := make([]Attacker, 0, len(c.rec.Attackers))
	promoted := make([]string, 0, len(c.rec.Attackers))
	for _, a := range c.rec.Attackers {
		switch {
		case sameDay(a.AttackDate, now):
			promoted = append(promoted, a.GuildName)
		case a.AttackDate.After(now):
			kept = append(kept, a)
		default:
			// 过期未打：丢弃（原版会一直留着）。
		}
	}
	c.rec.Attackers = kept
	return promoted
}

// StopWar 结束攻城（Castle.pas:864-889）。
//
// ⚠️ 原版在此把非守方踢出城、清 AttackGuildList，并广播"攻城战已经结束"。
// 它**确实**清了列表（participants 随之清空），但没有踢人也没有关 PK
// （ListC 是死代码）。我们清列表 + 广播，踢人交给调用方的 OnWarEnd。
func (c *Castle) StopWar(h Hooks) {
	if !c.underWar {
		return
	}
	c.underWar = false
	c.participants = nil
	if h.OnWarEnd != nil {
		h.OnWarEnd(c)
	}
}

// ForceWarStart 是 **GM 强制开战**（原版 CmdForcedWallConQuestWar，ObjBase.pas:12877-12911）：
//
//	Castle.m_boUnderWar := not Castle.m_boUnderWar;
//	if Castle.m_boUnderWar then
//	  Castle.m_dwStartCastleWarTick := GetTickCount();
//	  Castle.StartWallconquestWar();        // 起战（含关门）
//
// 与 `Run` 的到点开战**不是一回事**：那条还要求"当日到点 + 有已宣战行会"，
// 而 GM 命令是直接翻状态、立刻开打（我们早先只调 Run 并回一句"已强制进入攻城期"，
// 于是白天执行时战争根本没开、消息却在骗人 —— 已修）。
//
// 返回是否真的开了战（已经在攻城中或没有参战行会时返回 false）。
func (c *Castle) ForceWarStart(h Hooks, now time.Time) bool {
	if c.underWar || c.rec.OwnGuild == "" {
		return false
	}
	c.underWar = true
	c.startWar = true
	c.showOverMsg = false
	c.rec.WarDate = now
	c.warStart = now
	c.participants = nil
	if c.rec.OwnGuild != "" {
		c.participants = append(c.participants, c.rec.OwnGuild)
	}
	for _, a := range c.rec.Attackers {
		if a.GuildName != "" && a.GuildName != c.rec.OwnGuild {
			c.participants = append(c.participants, a.GuildName)
		}
	}
	c.dirty = true
	if h.OnWarStart != nil {
		h.OnWarStart(c)
	}
	return true
}

// SetOccupant 直接指定占领方（GM 用，等价 GetCastle 但不要求在攻城期）。
// 返回旧占领行会名。副作用通知由 Manager 负责。
func (c *Castle) SetOccupant(guildName string, now time.Time) string {
	return c.GetCastle(guildName, now)
}

// SetTotalGold 直接设金库余额（GM 用，对应 CmdShowSbkGold，ObjBase.pas:14383-14434）。
func (c *Castle) SetTotalGold(v int64) {
	c.rec.TotalGold = v
	c.dirty = true
}

// ---------- 工具 ----------

// sameDay 判断两个时刻是否同年同月同日（对应 DecodeDate 的三项比较）。
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// SetUnits 替换单位配置（雇佣后写回 HP 与坐标）。
func (c *Castle) SetUnits(units []storage.CastleUnit) {
	c.rec.Units = units
	c.dirty = true
}

// UnitConfig 按种类+序号取单位配置副本。
func (c *Castle) UnitConfig(kind storage.CastleUnitKind, idx int) (storage.CastleUnit, bool) {
	return c.unitOf(kind, idx)
}

// HireUnit 标记某个雇佣单位为"已雇佣"（HP>0 表示已雇，Castle.pas:220-301）。
//
// 官方 SabukW.txt 里未雇佣的守卫坐标是 0,0；x/y 都为 0 时给一个城门附近的
// 默认位（与另两个已填的守卫位 671,334 / 675,330 一致）。
// SetDoorOpened 写回城门的开/关状态（原版 TCastleDoor.Open/Close 会把
// `m_boOpened` 落到 SabukW.txt 的 MainDoorOpen，Castle.pas:476-506 的 SaveConfigFile）。
func (c *Castle) SetDoorOpened(opened bool) bool {
	for i := range c.rec.Units {
		u := &c.rec.Units[i]
		if u.Kind != storage.CastleMainDoor || u.Index != 0 {
			continue
		}
		u.Opened = opened
		c.dirty = true
		return true
	}
	return false
}

// DoorOpened 返回配置里的城门初始状态（官方 SabukW.txt:18 的 MainDoorOpen=1）。
func (c *Castle) DoorOpened() bool {
	for _, u := range c.rec.Units {
		if u.Kind == storage.CastleMainDoor && u.Index == 0 {
			return u.Opened
		}
	}
	return false
}

func (c *Castle) HireUnit(kind storage.CastleUnitKind, idx int, hp int) bool {
	for i := range c.rec.Units {
		u := &c.rec.Units[i]
		if u.Kind != kind || u.Index != idx {
			continue
		}
		u.HP = hp
		if u.X == 0 && u.Y == 0 {
			u.X, u.Y = 671+idx, 334
		}
		c.dirty = true
		return true
	}
	return false
}

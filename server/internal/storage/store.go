// Package storage 定义账号与角色存档的持久化抽象。
//
// 设计要点：
//  1. 接口与实现分离——默认 SQLite（零依赖），未来要换 MySQL 只需新增一个实现，
//     业务代码不动。见 docs/service-architecture.md。
//  2. 采用"索引列 + 详情 BLOB"的混合表结构：高频查询/排序字段（账号、角色名、
//     职业、等级、金币）独立成列；背包/装备/技能/任务标志等整块读写的内容
//     用 protobuf 序列化后存入 data 列。
//     理由：传奇的角色存档本来就是整块读（LoadHumanRcd）/整块写（SaveHumanRcd），
//     拆成十张关系表只会让每次存档变成多表事务，无收益。
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/algotao/mir2/server/internal/storage/pb"
)

var (
	// ErrNotFound 表示记录不存在。
	ErrNotFound = errors.New("storage: 记录不存在")
	// ErrExists 表示唯一键冲突（重名账号/角色）。
	ErrExists = errors.New("storage: 记录已存在")
	// ErrLeaseLost 表示会话已失效，或持有者租约不再有效。
	ErrLeaseLost = errors.New("storage: 会话租约已失效")
)

// Account 是账号。
//
// 对应 Delphi TAccountDBRecord（Common/Grobal2.pas:987-993）。
// 原版密码明文存储+明文比较+明文写日志（LMain.pas:1211），此处改为 PBKDF2 哈希。
type Account struct {
	ID           int64
	Name         string
	PasswordHash []byte
	Salt         []byte
	ErrorCount   int   // 连续密码错误次数（原版 >=5 锁定）
	ActionTick   int64 // 上次错误的时间戳（毫秒）
	Deleted      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// Data 是详情部分（TUserEntry + TUserEntryAdd）。
	Data *pb.AccountData
}

// Character 是角色。
//
// 对应 Delphi THumDataInfo（Grobal2.pas:955-958）。
// / CharacterSlot 是"选角界面的一个槽位"：`Slot` 是位置，`Char` 为 nil = 空位
// /（被删掉的那个位置留着，后面的人不左移 —— 见 `CharacterStore::ListByAccountWithSlots`）。
type CharacterSlot struct {
	Slot int
	Char *Character
}

// / MaxChrSlots 选角界面的槽位数（原版 1.76 一个账号 **2** 个角色，
// / `UsrSoc.pas:652`；客户端 `select_ui::Art::SLOTS` 同值）。
// /
// / ⚠️ 槽位与 id 无关：某号位上的角色被删掉 ⇒ 那个位置**空出来**（留给下一个新角色），
// / 但它**后面**的角色不左移（用户 2026-10-10 第 1 条）。
const MaxChrSlots = 2

type Character struct {
	ID      int64
	Account string
	Name    string
	/// 选角界面的**槽位**（0..MaxChrSlots-1；-1 = 还没分配）。
	///
	/// ⚠️ 必须落库：删掉 1 号位的角色后，下一个新角色要**补到这个空位**（原版语义），
	/// 而"按行号现算"做不到 —— 已删除的行会一直占位，把活着的角色顶到槽位之外
	///（用户 2026-10-10：改完之后选角界面不出现人物了）。
	Slot      int
	Job       uint32 // 0=战 1=法 2=道
	Level     uint32
	Gold      int64
	Deleted   bool // 软删：客户端删角色只是禁用，数据保留
	LastLogin time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	// Data 是整块存档详情。
	Data *pb.CharacterData
}

// SyncFromData 把详情里的高频字段投影到索引列。
//
// 存盘前必须调用，否则按等级/金币的查询会读到旧值。
func (c *Character) SyncFromData() {
	if c.Data == nil {
		return
	}
	c.Name = c.Data.ChrName
	if c.Data.Account != "" {
		c.Account = c.Data.Account
	}
	c.Job = c.Data.Job
	c.Gold = c.Data.Gold
	if c.Data.Abil != nil {
		c.Level = c.Data.Abil.Level
	}
}

// SyncToData 把索引列回填到详情，保证两者一致。
func (c *Character) SyncToData() {
	if c.Data == nil {
		return
	}
	c.Data.ChrName = c.Name
	c.Data.Account = c.Account
	c.Data.Job = c.Job
	c.Data.Gold = c.Gold
	if c.Data.Abil == nil {
		c.Data.Abil = &pb.Ability{}
	}
	c.Data.Abil.Level = c.Level
}

// SessionRecord 是持久化的会话记录。
//
// 落盘而非仅存内存的原因：gamesvr 需要校验客户端带来的 SessionID，
// 若会话只在 accountsvc 的内存里，两个进程就必须再做一套 RPC。
// 存进共享库后服务变成无状态，accountsvc 与 gamesvr 各自直连即可。
//
// 注意：连接级状态（是否查过角色、限流时间戳）不落盘——它们只属于单条连接。
type SessionRecord struct {
	SessionID     int32
	Account       string
	IP            string
	Stage         int // 见 accountsvc.Stage
	ServerName    string
	CharacterName string
	HandoffUsed   bool
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Guild 是行会。
//
// 对应 Delphi TGUild（Guild.pas:19-92）。原版把每个行会存成
// GuildBase/Guilds/<行会名>.txt 文本文件（Guild.pas:597-643），
// 此处统一进 SQLite：与角色同库、多进程共享，免去手工解析文本格式。
//
// 成员只记名字（原版成员是 TPlayObject 指针，落盘时也只写名字）：
// 在线对象由 gamesvr 按名字反查，见 internal/guild.Manager。
type Guild struct {
	Name string
	// Notice 是公告，按行存（原版 NoticeList）。
	Notice []string
	// Allies 是联盟行会名（原版 GuildAllList）。
	Allies []string
	// Wars 是敌对行会（原版 GuildWarList，值是到期时刻）。
	Wars []GuildWar
	// Ranks 是职务表。rank 1 = 掌门，rank 99 = 默认成员职务（Guild.pas:691-706）。
	Ranks []GuildRank
	// EnableAuthAlly 对应 m_boEnableAuthAlly（@AuthAlly 开关：允许被结盟）。
	EnableAuthAlly bool

	// ---- 行会争霸赛（原版 TGUild 的三个字段）----

	// ContestPoint 是行会战积分（原版 nContestPoint，Guild.pas:25）：
	// 每次击杀 +100（ObjBase.pas:21030），`StartTeamFight` 时清零。
	ContestPoint int
	// TeamFight 对应 boTeamFight（Guild.pas:26）：**只有争霸赛期间**才记分，
	// 由 `@StartContest` 打开、`@EndContest` 关闭（Guild.pas:1272-1282）。
	TeamFight bool
	// TeamFightDead 是争霸赛成员表（原版 `TeamFightDeadList`）：
	// 只有表里的成员阵亡/得分才计数（Guild.pas:775/790）。
	TeamFightDead []GuildTeamFightMember
}

// GuildTeamFightMember 是争霸赛成员的两个计数。
//
// ⚠️ 原版把两个数**打包进一个整数**塞在 TList.Objects 里：低 16 位 = 阵亡次数、
// 高 16 位 = 个人得分（Guild.pas:775-798 的 MakeLong/LoWord/HiWord）。
// 我们拆成两个字段（存档是 JSON，没有打包的必要时也更好读）。
type GuildTeamFightMember struct {
	// Name 是角色名。
	Name string
	// DieCount 是该成员在争霸赛里阵亡的次数（原版 LoWord）。
	DieCount int
	// Point 是该成员的个人得分（原版 HiWord）。
	Point int
}

// GuildRank 是行会职务（对应 Delphi TGuildRank，Guild.pas:7-11）。
type GuildRank struct {
	// No 是职务号，1..99（Guild.pas:1140 的合法区间）。
	No int
	// Name 是职务名（如"掌门人"/"行会成员"）。
	Name string
	// Members 是成员角色名。
	Members []string
}

// GuildWar 是一条敌对行会记录（对应 Delphi TWarGuild，Guild.pas:13-17）。
type GuildWar struct {
	// Name 是敌对行会名。
	Name string
	// EndAt 是行会战到期时刻。
	// 原版存"剩余毫秒"（Guild.pas:614），此处换绝对时刻，避免跨重启漂移。
	EndAt time.Time
}

// Castle 是城堡（沙巴克）。
//
// 对应 Delphi TUserCastle（Castle.pas:39-126）。原版每个城堡一个配置子目录，
// 存两份文件：<dir>/SabukW.txt（归属/税收/坐标）与 <dir>/AttackSabukWall.txt
// （宣战队列）。这里合成一条记录，宣战队列作为数组存在同一行里——
// 单机只有一座城堡，读写都是整块，没必要拆两张表。
//
// ⚠️ 战期状态（是否开城/是否在攻城/剩余时间）**不落库**：
// 原版存在内存（m_boStartWar / m_boUnderWar / m_dwStartCastleWarTick），
// 攻城期只有 3 小时且跨重启必然作废，恢复时按"未开战"处理即可。
type Castle struct {
	// ConfigDir 是配置子目录（原版 sConfigDir，多城堡时区分；单机固定 "0"）。
	ConfigDir string
	// Name 是城堡名（沙巴克，g_Config.sCastleName）。
	Name string

	// --- 地图与坐标（SabukW.txt 的 [defense] 段与后半部分）---

	// MapName 是战场地图（CastleMap=3）。
	MapName string
	// PalaceMap 是皇宫地图（CastlePlaceMap=0150）。攻城判定就发生在这里。
	PalaceMap string
	// SecretMap 是密道地图（CastleSecretMap=D701）。
	SecretMap string
	// HomeMap/HomeX/HomeY 是行会回城点（CastleHomeMap/X/Y=3,644,290）。
	HomeMap string
	HomeX   int
	HomeY   int
	// WarRangeX/WarRangeY 是攻守期区域半径（CastleWarRangeX/Y=100）。
	WarRangeX int
	WarRangeY int
	// PalaceDoorX/PalaceDoorY 是皇宫门坐标（CastlePalaceDoorX/Y=631,274）。
	PalaceDoorX int
	PalaceDoorY int
	// ExtraMaps 是归属地图列表（原版 m_EnvirList，硬编码 0151..0156，
	// Castle.pas:1355-1360）。落在这几张图上也算城堡战区。
	ExtraMaps []string

	// --- 归属与战史 ---

	// OwnGuild 是占领行会名（OwnGuild=）。空表示无主。
	OwnGuild string
	// ChangeDate 是换主时间（ChangeDate=）。
	ChangeDate time.Time
	// WarDate 是最近一次攻城开始时间（WarDate=）。
	WarDate time.Time
	// Attackers 是宣战队列（AttackSabukWall.txt）：行会名 + 预定攻城日。
	Attackers []CastleAttacker

	// --- 税收（IncRateGold 从 NPC 交易里抽 5%，Castle.pas:1022-1066）---

	// TotalGold 是金库余额（TotalGold=）。
	TotalGold int64
	// TodayIncome 是当日已收（TodayIncome=），跨日清零。
	TodayIncome int64
	// IncomeToday 是当日收税的起算时刻（IncomeToday=）。
	IncomeToday time.Time

	// --- 经济与科技（m_nTechLevel / m_nPower，原版无逻辑消费，纯存档）---

	TechLevel int
	Power     int

	// Units 是城门/城墙/雇佣守卫的读档坐标（MainDoorX/Y、LeftWallX/Y…、
	// 弓箭卫士_N_X/Y、Archer_N_*、Guard_N_*）。
	//
	// HP=0 表示该单位未雇佣（原版据此跳过生成，Castle.pas:220-301）。
	Units []CastleUnit
}

// CastleAttacker 是一条宣战记录（对应 Delphi TAttackerInfo，Castle.pas:33-38）。
type CastleAttacker struct {
	// GuildName 是宣战行会名。
	GuildName string
	// AttackDate 是预定攻城日（原版 Now + nStartCastleWarDays，默认 4 天后）。
	// 到了这一天且时钟走到开城时刻，该行会自动进入本场攻城方名单。
	AttackDate time.Time
}

// CastleUnitKind 区分城堡上的单位种类。
type CastleUnitKind string

const (
	// CastleMainDoor 是城门（TObjUnit，SabukW.txt 的 MainDoor*）。
	CastleMainDoor CastleUnitKind = "door"
	// CastleWall 是城墙（SabukW.txt 的 LeftWall/CenterWall/RightWall）。
	CastleWall CastleUnitKind = "wall"
	// CastleGuard 是可雇佣守卫（MAXCALSTEGUARD=4）。
	CastleGuard CastleUnitKind = "guard"
	// CastleArcher 是可雇佣弓箭手（MAXCASTLEARCHER=12）。
	CastleArcher CastleUnitKind = "archer"
)

// CastleUnit 是一个城堡单位（对应 Delphi TObjUnit，Castle.pas:23-32）。
type CastleUnit struct {
	Kind CastleUnitKind
	// Index 是同类内的序号：墙 0=左 1=中 2=右，守卫与弓箭手从 0 起。
	Index int
	// Name 是怪物库中的名字（MainDoorName=SabukDoor，LeftWallName=SabukW1 …）。
	Name string
	X, Y int
	// HP 是读档血量（MainDoorHP=10000，LeftWallHP=5000，雇佣单位为 0=未雇佣）。
	HP int
	// Opened 只对城门有意义（MainDoorOpen=1），对应 TCastleDoor.m_boOpened。
	Opened bool
}

// CastleStore 管理城堡。
//
// 与行会不同，城堡**没有删除路径**（原版也没有）：只有"更新"，
// 首次落库用 Save（靠 upsert 语义）或显式 Create。
type CastleStore interface {
	Get(ctx context.Context, configDir string) (*Castle, error)
	// List 全量列出城堡（原版启动时 LoadCastleList，Castle.pas:1292-1350）。
	List(ctx context.Context) ([]*Castle, error)
	// Save 写入或更新（upsert）。
	Save(ctx context.Context, c *Castle) error
}

// Store 是持久化入口。
type Store interface {
	Accounts() AccountStore
	Characters() CharacterStore
	Sessions() SessionStore
	Guilds() GuildStore
	Castles() CastleStore
	// Ping 检查底层连接可用。
	Ping(ctx context.Context) error
	Close() error
}

// GuildStore 管理行会。
type GuildStore interface {
	Create(ctx context.Context, g *Guild) error
	GetByName(ctx context.Context, name string) (*Guild, error)
	// List 全量列出行会（原版启动时全量 LoadGuildInfo，Guild.pas:186-224）。
	List(ctx context.Context) ([]*Guild, error)
	Save(ctx context.Context, g *Guild) error
	Delete(ctx context.Context, name string) error
}

// AccountStore 管理账号。
type AccountStore interface {
	Create(ctx context.Context, a *Account) error
	GetByName(ctx context.Context, name string) (*Account, error)
	Update(ctx context.Context, a *Account) error
	// MarkDeleted 软删（保留记录，禁止登录）。
	MarkDeleted(ctx context.Context, id int64) error
}

// SessionStore 管理会话记录。
type SessionStore interface {
	Create(ctx context.Context, s *SessionRecord) error
	Get(ctx context.Context, sessionID int32) (*SessionRecord, error)
	Update(ctx context.Context, s *SessionRecord) error
	// Activate atomically makes this authenticated session the account's current login owner.
	Activate(ctx context.Context, s *SessionRecord) error
	// IsCurrent checks that an unexpired account lease still belongs to this session.
	IsCurrent(ctx context.Context, account string, sessionID int32, now time.Time) (bool, error)
	// BindHandoff atomically consumes the one-time login-to-character-server handoff.
	BindHandoff(ctx context.Context, account string, sessionID int32, now time.Time) (*SessionRecord, error)
	// IssueGameTicket records a one-use selection for this character under the current account session.
	IssueGameTicket(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) error
	// ClaimGameLease atomically consumes that ticket and reserves the character. A false result
	// means the ticket is invalid/replayed or another unexpired connection owns this character.
	ClaimGameLease(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) (bool, error)
	RenewGameLease(ctx context.Context, account string, sessionID int32, character string, now, expires time.Time) (bool, error)
	ReleaseGameLease(ctx context.Context, account string, sessionID int32, character string) error
	ReleaseCurrent(ctx context.Context, account string, sessionID int32) error
	CleanupExpired(ctx context.Context, now time.Time) error
	// Delete removes one session record. It must not release a different current session.
	Delete(ctx context.Context, sessionID int32) error
	// DeleteBefore 清理指定时间之前的会话，返回删除条数。
	DeleteBefore(ctx context.Context, t time.Time) (int64, error)
}

// CharacterStore 管理角色。
type CharacterStore interface {
	Create(ctx context.Context, c *Character) error
	GetByName(ctx context.Context, name string) (*Character, error)
	// ListByAccount 列出某账号下的全部未删除角色（原版上限 2 个）。
	ListByAccount(ctx context.Context, account string) ([]*Character, error)
	// ListByAccountWithSlots 同 [`ListByAccount`]，但**连同已删除的一起编号**：
	// 返回的第 i 项对应选角界面的第 i 个槽位。
	//
	// ⚠️ 为什么要带已删除的：选角界面里每个角色占**固定的槽位** —— 删掉 1 号之后，
	// 2 号必须还待在 2 号位（用户 2026-10-10 第 1 条：删了角色1，角色2 左移了）。
	// 只按"未删除"编号就会把后面的人整体左移一格。
	ListByAccountWithSlots(ctx context.Context, account string) ([]CharacterSlot, error)
	Update(ctx context.Context, c *Character) error
	// UpdateForSession persists only while the game's account lease still belongs to sessionID.
	UpdateForSession(ctx context.Context, c *Character, sessionID int32) error
	MarkDeleted(ctx context.Context, id int64) error
}

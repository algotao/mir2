package gamesvr

import (
	"context"
	goproto "google.golang.org/protobuf/proto"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/chargen"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/proxyproto"
	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/storage"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/world"
)

const (
	maxFrameLen = 64 * 1024
	readTimeout = 30 * time.Minute
	// proxyHeaderTimeout 是"等 PROXY 头"的上限（只在该模式打开时用）。
	// 与 readTimeout 差了三个数量级是有意的：协议读 30 分钟是"玩家挂机"，
	// 头读 30 分钟就是"有人在白占连接"。
	proxyHeaderTimeout = proxyproto.DefaultHeaderTimeout
	// loginNoticeTimeout 等待客户端回应公告的时限（原版 10 秒）。
	loginNoticeTimeout = 10 * time.Second

	// monsterTickInterval 是怪物 AI 周期。
	monsterTickInterval = 500 * time.Millisecond
	// spawnCheckInterval 是检查刷怪的周期。
	spawnCheckInterval = 5 * time.Second
	// sessionLeaseInterval 轮询共享存储，发现被接管的游戏连接后立即断开旧玩家。
	sessionLeaseInterval = time.Second
	// gameLeaseDuration 超过该时间未续租时允许另一个 gamesvr 接管。
	gameLeaseDuration = 8 * time.Second
	// gameLeaseWait 等待旧 gamesvr 完成下线存档并释放租约的最长时间。
	gameLeaseWait = 10 * time.Second
)

// Player 是在线玩家。
type Player struct {
	Obj  *entity.Object
	Char *storage.Character
	// Limiter 控制移动频率。
	Limiter *entity.MoveLimiter

	conn            net.Conn
	IP              string
	sessionID       int32
	revoked         atomic.Bool
	cleanupOnce     atomic.Bool
	cleanupDoneOnce sync.Once
	cleanupDone     chan struct{}

	// brightPhase / brightInit 记"上次发出去的昼夜相位"（原版 `m_btBright`）：
	// 相位变了要补一条 SM_DAYCHANGING，见 daynight.go 的 tickDayChanging。
	brightPhase      int
	brightInit       bool
	dayChangingReady atomic.Bool

	// snapReq 是"请在这名玩家**自己的 goroutine** 上做一份存档快照"的投递通道
	//（自动存档用，见 saveAll / saveSnapshotOf）。请求方塞进一个回复通道，
	// 对方做完把快照塞回去。
	// ⚠️ 容量 1：请求方超时放弃后，玩家 goroutine 也不会因为没人接而卡住。
	// ⚠️ 只在**注册进 world.players 之前**赋值一次，之后只读（见 handleConn）。
	snapReq chan chan *storage.Character

	// protoOut 非 nil ⇒ 这名玩家走**新协议**（见 netproto.go）：
	// 实体事件（出现/消失/移动）从它出去，而不再走 legacy 的 `s.send`。
	//
	// 判定点刻意放在**实体事件的出口**（view.go 的四个函数）里，而不是散在调用点：
	// "谁该看见谁"的判定只有 vision 那套一处，两条协议共享它。
	// ⚠️ 同样的赋值纪律：注册进 world.players 之前赋值一次，之后多 goroutine 只读
	// （怪物 AI ticker 会读它，见 protoSink）。
	protoOut *protoSink

	noticeSent bool
	logonDone  bool
	// permission 是 GM 权限等级（0 = 不是 GM），登录时按 Envir/AdminList.txt
	// 的角色名（+可选 IP）匹配；见 data.AdminList 与 handleSay 的门。
	permission uint8

	// 聊天状态（对应原版 m_dwSayMsgTick / m_nSayMsgCount /
	// m_dwDisableSayMsgTick / m_dwShoutMsgTick）。
	//
	// ⚠️ 只作用于**聊天**：@ 命令走另一条路（原版 ProcessUserLineMsg 先分派 @），
	// 不参与反刷屏——否则 e2e 满屏的 @ 命令会把玩家"禁言"掉。
	sayLast     time.Time
	sayRepeat   int
	sayBanUntil time.Time
	shoutLast   time.Time
	// spellLast 按技能号记录上次施法时刻。
	//
	// ⚠️ 必须**按技能分别计时**：若共用一个时间戳，放完隐身术后
	// 魔法盾会被前者的冷却挡住（表现为"第二个技能毫无反应"）。
	spellLast map[uint32]time.Time
	// spellTargetID 是技能锁定的目标（SetTargetCreat）。
	// 圣言术命中后会把施法者的目标指向那只怪（Magic.pas:911）。
	spellTargetID uint32
	// combatTargetID 是**近战/技能正在打的那只怪**（原版 m_TargetCret / m_ExpHitter）。
	// 宠物的目标判据要用它（IsAttackTarget：主人正在打的怪要一起打）。
	combatTargetID uint32
	// equipSpecials 是当前**装备形状**触发的特殊效果缓存
	//（隐身戒指/麻痹戒指/复活戒指…，见 itemabil.go 的 playerItemSpecials）。
	//
	// 为什么要缓存：`statusBits()` 是 *Player 方法（拿不到物品表），而状态位在多处要用；
	// 穿脱装备与进游戏时用 refreshSpecials 刷一次即可。
	equipSpecials itemSpecials
	// revivalAt 是复活戒指上次生效的时刻（60 秒冷却，原版 m_dwRevivalTick）。
	revivalAt time.Time
	// lastTeleportAt 是传送戒指上次 `@move` 的时刻（原版 m_dwTeleportTick）。
	lastTeleportAt time.Time
	// lastProbeAt 是探测项链上次 `@searchhuman` 的时刻（原版 m_dwProbeTick）。
	lastProbeAt time.Time

	// lastHitBy 是**最后打本玩家的怪**（原版 m_LastHiter）。宠物据此"替主人报仇"。
	lastHitBy uint32

	// ---- 战士近战系的"充能"状态（攻杀剑术 7 / 烈火剑法 26）----
	//
	// 详见 warrskill.go 的说明与原版位置。两个标志都是"攒一次强化攻击"。
	//
	// powerHit 对应 m_boPowerHit：攻杀剑术的充能，**由服务端按挥砍次数自动积累**。
	powerHit bool
	// powerHitCount/powerHitPoint 对应 m_btAttackSkillCount/m_btAttackSkillPointCount
	// （每刀 -1；计数落到 point 时充能）。powerHitInit 记录是否已按技能等级初始化过。
	powerHitCount int
	powerHitPoint int
	powerHitInit  bool
	// fireHit 对应 m_boFireHitSkill + m_dwLatestFireHitTick：烈火剑法的点燃状态，
	// fireHitAt 既是"点燃时刻"（20 秒有效期）也是"上次点燃时刻"（10 秒冷却）。
	//
	// ⚠️ 这两个字段**跨 goroutine**：玩家收包路径（armFireHit / consumeWarrCharge）
	// 在自己的 goroutine 上读写，而 `regenLoop` 的 ticker 通过 tickWarrCharge
	// 读+写（到期作废）。所以**必须**经 statelock.go 里的那组访问器，
	// 不能裸读裸写（审计 P1-5）。`fireMu` 是它们唯一的锁。
	fireHit   bool
	fireHitAt time.Time
	// fireMu 保护 fireHit / fireHitAt（叶子锁：回调里不加别的锁、不发包）。
	fireMu sync.Mutex
	// motaeboAt 对应 m_dwDoMotaeboTick：野蛮冲撞(27) 的 3 秒冷却。
	motaeboAt time.Time
	// dialog 是当前所处的 NPC 对话（nil 表示不在对话中）。
	dialog *dialog
	// buffs 是身上的增益（隐身/魔法盾/幽灵盾/神圣战甲），值为到期时刻。
	buffs map[entity.BuffType]buffState

	// allowGuild 对应原版 m_boAllowGuild（@LetGuild 开关，默认 False，
	// ObjBase.pas:1271）：为 True 时才允许被掌门加进行会。
	allowGuild bool
	// banGuildChat 对应原版 m_boBanGuildChat，其真实语义是"**允许**接收
	// 行会聊天"，默认 True，@BanGuildChat 取反（ObjBase.pas:1329、7646-7652）。
	// 保留原版命名以免与原客户端文案对不上。
	banGuildChat bool

	// visible 记录其视野内的所有实体（玩家与怪物共用 ID 空间）。
	visible *entity.ViewTracker

	// lastMoveAt 是最近一次传送/换图的时刻（m_dwMapMoveTick）。
	// 传送后 3 秒内互相不能打（ObjBase.pas:21322-21323）。
	lastMoveAt time.Time

	// turnAt 是最近一次转身/取肉/打坐的时刻（原版 `m_dwTurnTick`，ClientGetButchItem
	// 首行与 CM_TURN/CM_SITDOWN 共用同一个时间戳，间隔 `TurnIntervalTime=100`ms）。
	turnAt time.Time
	// softClose / reconnection 对应原版 `m_boSoftClose` / `m_boReconnection`
	// （CM_SOFTCLOSE 置位，见 clientaction.go）。
	softClose    bool
	reconnection bool
	// stateMu 保护 `Char.Data.BagItems`（背包）的并发访问。
	//
	// ⚠️ 为什么需要：交易是"两个玩家"的事，A 的 goroutine 会改 **B 的背包**
	//（成交塞物品 / 取消退回），而 B 的 goroutine 可能同时在吃药/捡物/换装
	// ⇒ 无同步地读写同一个切片 = 真实 data race（后果是物品丢失/背包错乱）。
	//
	// 规则与全部出口见 baglock.go 的文件头（含锁序：dealMu → stateMu，永不嵌套两把）。
	stateMu sync.Mutex
	// decHPAt / incHPAt 是地图级扣血/加血的计时器（原版 `m_dwDecHPTick` /
	// `m_dwIncHPTick`，见 tickMapHP）。
	decHPAt time.Time
	incHPAt time.Time

	// inFreePKArea 对应 m_boInFreePKArea：自由 PK 区/攻城战区。
	// 由 syncFreePKArea 每秒按"是否在攻城战区且正在攻城"重算。
	inFreePKArea bool
	// pvpFlag 对应 m_boPKFlag：被打后的正当防卫标记（60 秒）。
	pvpFlag bool
	// pvpFlagUntil 是该标记的到期时刻。
	pvpFlagUntil time.Time
	// linkedMakeIndex 是 NPC 脚本"操作位"当前关联的物品 MakeIndex
	//（LINKBAGITEM 指令，0 = 未关联）。不落盘：原版的操作位也是会话内状态。
	linkedMakeIndex int32
	// nameColorSent 缓存"我上次给自己视角下发的名字颜色"，用于跳过无变化的重发。
	nameColorSent uint8
	// areaStateBits 缓存上次下发的 SM_AREASTATE 位掩码。
	//
	// ⚠️ 必须做变化检测再发：pvpLoop 跑在 tickDur(1s) 上，20 倍速回归时
	// 是 50ms 一轮，无条件发包会把协议流淹掉、干扰测试客户端的包序断言。
	areaStateBits uint32

	// ---------- 宠物（m_SlaveList）----------
	//
	// Slaves 存宠物的 ActorId。**不落盘**：原版的宠物是会话内状态，
	// 主人下线兽就没了（见 removePlayer → releaseSlaves）。
	//
	// ⚠️ 以前这里什么都没有，"谁是我的宠物"靠全表扫 s.world.monsters 过滤
	// MasterID，主人下线后那一只就成了永久孤儿（还在世界里乱跑、
	// 还会打别人）。诱惑之光要判 `m_SlaveList.Count < 5`（Magic.pas:812），
	// 更是必须有一份真列表。
	Slaves []uint32
	// slaveRelax 对应 m_boSlaveRelax：true = 休息（宠物不攻击、也不瞬移跟随）。
	// 由 @slave 切换（ObjBase.pas:10989 CmdChangeSalveStatus）。
	slaveRelax bool

	// ---------- 组队（m_GroupOwner / m_boAllowGroup 等）----------
	//
	// ⚠️ 原版把组数据挂在玩家上，**没有独立的队伍对象**（见 internal/group 的包注释）。
	// 这里只留"这个人的组状态"，队伍本体在 s.social.groups 里。

	// allowGroup 对应 m_boAllowGroup：是否允许被拉进队。
	// 由 CM_GROUPMODE(Param=1) 打开；**落盘**（HumData.btAllowGroup，
	// ObjBase.pas:24920-24921 / UsrEngn.pas:2351-2352）。
	allowGroup bool
	// allowGroupRecall 对应 m_boAllowGroupReCall：是否允许被队长整组传送。
	// 由 @AllowGroupRecall 切换；**落盘**（ObjBase.pas:24937 / UsrEngn.pas:2369）。
	allowGroupRecall bool
	// groupRecallUntil 是 @GroupRecall 的冷却到期时刻（m_wGroupRcallTime）。
	groupRecallUntil time.Time

	// ---------- 玩家交易（m_boDealing 等，字段都在 TBaseObject 上）----------

	// dealing 对应 m_boDealing：是否处于交易态。
	dealing bool
	// dealPartner 是交易对方的 ActorId（0 = 不在交易）。对应 m_DealCreat。
	//
	// ⚠️ 原版声明成 TBaseObject 并到处硬 cast TPlayObject(...)，Go 里用 ID 更安全
	// ——对方下线/消失时 partnerOf 直接返回 nil，不会像原版那样 AV。
	dealPartner uint32
	// dealItems 是**我**放进交易栏的物品（原版 m_DealItemList）。
	//
	// ⚠️ 存的是**指针**（同一块 pb.UserItem 从背包搬到交易栏，不是拷贝），
	// 与原版一致：结构上不可能复制出第二份物品。
	dealItems []*pb.UserItem
	// dealGolds 对应 m_nDealGolds：**已经从我背包扣走**、待交付给对方的金币。
	//
	// ⚠️ 不是"成交时才扣"，放进来时就扣了（见 deal.go 文件头怪癖 1）。
	dealGolds int64
	// dealOK 对应 m_boDealOK：**我**已按"成交"。
	//
	// ⚠️ 原版语义拧巴：它被当成"对方已确认"来读（ClientAddDealItem 判
	// `not m_DealCreat.m_boDealOK`）。照抄。
	dealOK bool
	// dealLastTick 对应 m_DealLastTick：最后一次交易操作时刻。
	//
	// ⚠️ **双向共享**：任一方操作都会刷新对方的 tick（180/24015-24016 等），
	// 所以"确认前静止 1 秒"能被对方操作绕过。这是原版行为。
	dealLastTick time.Time
	// allowDeal 对应 m_boAllowDeal：是否允许被交易（@LetTrade 开关）。
	allowDeal bool
}

func (p *Player) Pos() (int, int) { return p.Obj.Pos() }

// Server 是游戏服务。
// configState 是**运行时配置与【仅测试】开关**（阶段 4 从散字段收进子结构）。
//
// 与 dataState 的分工：这里回答"怎么跑"（视野半径、存档间隔、各项开关、掉落率、GM 名单），
// 那里回答"跑什么数据"（静态表）。两者都在启动时由 flag 决定，之后基本只读。
type configState struct {
	// allowNewAccount 决定**是否开放注册**（原版 `Config.boEnableMakingID`，D-32）。
	//
	// ⚠️ 默认**关**（原版默认是开，`LSShare.pas:96`）：开着的服务端在公网上就是
	// 一个自助注册入口。要用 `-allow-new-account` 显式打开。
	allowNewAccount bool
	// viewRange 是玩家视野半径（切比雪夫距离）。
	viewRange int
	// proxyProtocol 决定**怎么取得可信的客户端 IP**（D-23）：
	//
	//	true   要求接入连接先写一行 PROXY protocol v1 头（gate 会写），缺头即断开；
	//	false  直连模式，用 socket 对端地址（本地开发与回归用）。
	//
	// ⚠️ 没有"有头就解析、没头退回 socket 地址"这第三种模式 —— 那是可伪造的
	// （能直连到 gamesvr 的人可以自称 `PROXY TCP4 1.2.3.4 …`）。见 internal/proxyproto。
	proxyProtocol bool
	// saveInterval 自动存档间隔。
	saveInterval time.Duration
	// makeDrugPrice 是制药单价（官方 `!setup.txt:256 MakeDurg=100`）。
	makeDrugPrice int
	// miniMaps 是"地图号 → 小地图编号"（官方 `data/envir/MiniMap.txt`，
	// 原版 `ClientGetMinMap` 回的就是它）。没配的地图回 SM_READMINIMAP_FAIL。
	miniMaps map[string]uint16
	// grantAll 仅测试用：进游戏授予全部技能，便于验证尚未开放学习的技能。
	grantAll bool
	// noRegen 仅测试用：关闭自然恢复（见 flag 说明）。
	noRegen bool
	// wander 控制怪物是否游荡；aggro 控制是否主动攻击玩家。
	// 两者都可关，便于分别验证"玩家打怪"与"怪打玩家"。
	wander bool
	aggro  bool
	// wallNoSafeZone 对应原版 boDisableInSafeZoneFireCross
	// （Setup 的 DisableInSafeZoneFireCross，出厂 False = 允许在安全区铺）。
	wallNoSafeZone bool
	// adminList 是 GM 名单（Envir/AdminList.txt）。空名单 = 谁都不是 GM。
	adminList *data.AdminList
	// gmOpen 是【仅测试】开关：忽略 GM 名单，人人可用 @ 命令。
	//
	// ⚠️ 存在的原因是**回归里的每个用例都靠 @ 命令驱动**
	//（@level/@spawn/@give/@map/@die…），而用例账号不在官方名单里。
	// 这是唯一能让 e2e 跑起来又不删掉权限门的方式。
	gmOpen bool
	// deathDrop… 是死亡掉落的三档"1/N"（**不是**千分比）—— 原版就是
	// `Random(nRate) = 0` / `Random(nRate) <> 0`，N 取自 `!setup.txt:814-817`：
	//
	//	DieScatterBagRate=200    ⇒ 包裹 1/200（非红名）
	//	DieRedScatterBagAll=1    ⇒ 红名（PKLevel ≥ 2）**全掉**
	//	DieDropUseItemRate=30    ⇒ 装备 1/30
	//	DieRedDropUseItemRate=15 ⇒ 红名（PKLevel > 2）装备 1/15
	//
	// ⚠️ 原来是千分比（包裹 200‰=20%、装备 100‰=10%），**两头都不对**：
	// 出厂配置里包裹其实是 0.5%、装备是 3.3%。0 = 关掉该档。
	// 分开配置的原因见 deathDrop：OpenMir2 的 DieDropUseItemRate 与散落率
	// 是两个独立配置，装备通常比包裹更容易掉。
	deathDropBagOneIn    int
	deathDropEquipOneIn  int
	deathDropRedEquipIn  int
	deathScatterBagAllOn bool
}

// dataState 是**加载好的静态数据表**（阶段 4 从散字段收进子结构）。
type dataState struct {
	// mapInfoByID 是"地图号 → mapinfo 记录"的索引（含 DARK/NORECALL/NODRUG… 全部标记）。
	// 由 `indexMapInfos` 在启动时建好；`mapFlagOf` 用它按图查标记。
	mapInfoByID map[string]*data.MapInfo
	// tables 是 data 目录加载出来的全部静态表。
	tables *data.Tables

	// defaultMapID 是 `-map` 指定的默认地图号。
	//
	// 建角色要用它当出生地图（新角色的 `CurMap`/`HomeMap`），
	// 而 `defaultMap` 是**已加载的地图对象**、拿不到"号"。
	defaultMapID string
	// drops 是内置兜底掉落表（数据目录缺失时使用）。
	drops map[string]*entity.DropTable
	// dropTables 是从 Envir\MonItems 加载的真实掉落表，优先于 drops。
	dropTables *entity.MonItems
	// makeItems 是制药配方表（官方 `data/envir/MakeItem.txt`，59 条）。
	makeItems *data.MakeItemSet
	// mapInfos 是 mapinfo.txt 的地图元信息。
	mapInfos []*data.MapInfo
	// startPoints 是 StartPoint.txt 的出生/复活点。
	startPoints []*data.StartPoint
	// homePoints 是 `-home-points`：新角色出生点候选。
	//
	// **多于一个就随机挑一个**（`chargen.PickHome`）—— 这是原版 1.76 的行为：
	// 两个新手村随机二选一（银杏山谷 / 边界村），不分职业，见 `docs/use.md` 与 D-40。
	homePoints []chargen.Home
}

// worldState 是**世界本体**：在线玩家、怪物、地面物品、火墙与刷怪点。
//
// ⚠️ 锁：这些字段全部由外部 `Server.mu` 保护（**没有**跟着搬进来）。
// 原因是否则 `npc.spawned` 之类同一个锁保护、但不在这个结构里的字段会失去明确归属；
// 锁留在 `Server` 上，纪律不变："世界状态一律在 s.mu 下访问"。
type worldState struct {
	// players 是全部在线玩家（键为角色 ActorId）。
	players map[uint32]*Player
	// index 是玩家的空间索引（视野扫描用）。
	index *world.SpatialIndex
	// monsters 是全部怪物（键为 ActorId）。
	monsters map[uint32]*entity.Monster
	// monsterIdx 是怪物的空间索引。
	monsterIdx *world.SpatialIndex
	// actorSeq 分配玩家的 ActorId。
	actorSeq atomic.Uint32
	// monsterSeq 分配怪物的 ActorId（从 proto.MonsterIDBase 起）。
	monsterSeq atomic.Uint32
	// spawns 是全部地图的刷怪点（mongen.txt + 内置兜底）；只有已激活地图会生成活体怪物。
	spawns []entity.SpawnPoint
	// activeSpawnMaps 记录已经进入运行态的地图，避免服务启动就实例化全服数万只怪物。
	// 首次进入地图时激活；每图怪物仍按该图所有 MonGen 点自然刷新。
	activeSpawnMaps map[string]bool
	// maps 是地图管理器（按需加载 + LRU）。
	maps *world.MapManager
	// defaultMap 是默认地图（出生/回城用）。
	defaultMap *world.Map
	// mines 是各地图的矿脉（键 = `*world.Map`，值 = 每格一个 mineEvent，见 butch.go）。
	// 原版 `TFrmMain.MakeStoneMines` 在**启动时**给 MINE/MINE2 图的每一格都建一个，
	// 我们推迟到"第一次挖到这一格"（矿脉对客户端不可见，观测差别只是掷点时机）。
	mines map[*world.Map]map[[2]int]*mineEvent
	// doors 是"当前被打开的门"（键 = 地图 + 门组号，见 mapdoor.go）。
	doors map[doorKey]*openDoor
	// ground 是地图上的地面物品，键为地面物品 ID。
	ground map[uint32]*GroundItem
	// groundSeq 分配地面物品 ID。
	groundSeq atomic.Uint32
	// groundEvents 是"有寿命的地面事件"登记表（键为 地图+坐标+事件类型）：
	// 挖矿的碎石堆（piles.go）与困魔咒的光幕（groundevent.go）都在这里，
	// 到期按登记的 id 发 SM_HIDEEVENT 抹掉。
	groundEvents map[groundKey]*groundEvent
	// walls 是地图上的火墙事件（TFireBurnEvent / ET_FIRE），键为 地图+坐标。
	//
	// ⚠️ 刻意**不**挂在 *world.Map 上：MapManager 是 LRU（manager.go 的设计
	// 约束明写"将来要在 Map 上挂动态状态必须先迁出去，否则淘汰即丢失"）。
	// 地面物品 ground 也是同样的处理。
	walls map[wallKey]*Wall
}

// socialState 是"玩家之间的组织"：组队、行会及其配置（阶段 4）。
type socialState struct {
	// groups 管理所有组队（internal/group）。
	//
	// ⚠️ 队伍本体归它管，但**所有访问必须在 s.mu 下**（它自身不加锁）。
	// 玩家侧的 `allowGroup` / `groupID` 语义见 Player 的注释。
	groups *group.Manager
	// guilds 是行会管理器（P6）。
	guilds *guild.Manager
	// guildCfg 是行会配置（建会费用/号角/聊天颜色）。
	guildCfg guildConfig
}

// castleState 是城堡（沙巴克）管理器与配置（P6，单机一座）。
type castleState struct {
	// mgr 是城堡管理器；未启用时为 nil。
	mgr *castle.Manager
	// config 是城堡配置（税率/宣战道具/战区税收开关）。
	//
	// ⚠️ 名字用 config 而不是 cfg：`castleConfig` 自己有个 `cfg castle.Config` 字段，
	// 若这里也叫 cfg 就会出现 `s.castle.cfg.cfg`（语义没错但没法读）。
	config castleConfig
}

// pvpState 是 PvP 的配置与它那把小锁。
type pvpState struct {
	// cfg 是 PvP 配置（判定参数/区域属性/名字颜色）。
	cfg pvpConfig
	// mu 只保护 cfg.decTick（按角色名记衰减时刻）。
	mu sync.Mutex
}

// npcState 是 NPC 定义、脚本缓存与脚本定时器的集合。
//
// 拆出来的理由见 docs/service-architecture.md §8.3 阶段 4：这几个字段原本散在
// `Server` 的 90 个字段里，读写点跨 8 个文件，靠命名前缀（`script*`/`npc*`）勉强分组。
//
// ⚠️ 锁的纪律与拆之前**完全一致**，只是路径变短：
//   - `mu` 保护 `scripts` 缓存（原 `scriptMu`）；
//   - `spawned` 由外部 `Server.mu` 保护（原 `npcSpawned` 也是这样）；
//   - `timers` 自带锁（`scriptTimerQueue` 内部）。
type npcState struct {
	// defs 是 merchant.txt + Npcs.txt 的全部 NPC 定义。
	defs []*data.NPC
	// scriptDir 是 market_def 脚本目录。
	scriptDir string
	// scripts 是已加载的脚本缓存（键为 <ID>-<地图>）。
	scripts map[string]*script.Script
	// mu 保护 scripts。
	mu sync.RWMutex
	// spawned 记录哪些地图的 NPC 已生成，避免重复生成（由 Server.mu 保护）。
	spawned map[string]bool
	// seq 分配 NPC 的 ActorId 序列号。
	seq atomic.Uint32
	// timers 是 NPC 脚本的延时跳转队列（TIMERECALL / DELAYGOTO）。
	timers scriptTimerQueue
	// upgrades 是各 NPC 的"武器修炼中"列表（键 = NPC ActorId，见 weaponupgrade.go）。
	//
	// ⚠️ 由 `Server.mu` 保护（与 spawned 同一把锁）——原版把它挂在 NPC 对象上、
	// 靠 NPC 自己的消息线程串行化，我们没有那个线程模型。
	upgrades map[uint32][]*weaponUpgrade
}

type Server struct {
	store storage.Store

	// cfg 是运行时配置与【仅测试】开关；data 是加载好的静态数据表。
	// 两者的区别见各自类型的文档（阶段 4：从散字段收进子结构）。
	cfg  configState
	data dataState

	mu sync.RWMutex

	// createMu/createLast 是**建号节流**（D-32）：同一 IP 在 `newAccountCooldown`
	// 内只允许建一个号。见 allowAccountCreate。
	//
	// ⚠️ 挂在 Server 上而不是挂连接：节流必须是全局的，否则"多开几条连接"就绕过去了。
	// 原版是每连接 5 秒（`LoginSrv/LMain.pas:977-986`）—— 公网上那等于没限。
	createMu   sync.Mutex
	createLast map[string]time.Time

	// dealMu 串行化**交易**（deal.go）。为什么要单独一把锁：
	//
	//   - 交易是**两个玩家**的事，而每个玩家一个 goroutine，心跳（pvpTick→dealGuard）
	//     还是第三个 ⇒ "成交"与"取消"可以真并发；
	//   - 成交要把交易栏的物品搬进对方背包，而物品进交易栏时就已离开背包 ⇒
	//     交易栏就是它的所有权令牌。搬迁过程若被并发取消插进来
	//     （对方点取消 / 心跳守卫 / 对方掉线 dealCancelA），同一批指针会被
	//     再塞回原主背包 ⇒ **同一件物品出现在两个人的背包里**。
	//     这正是 1.76 那类"交易瞬间切图/换服复制物品"的同构漏洞。
	//   - 不用 s.mu：交易函数内部要调 partnerOf（自己拿 s.mu.RLock），
	//     sync.RWMutex 不可重入 ⇒ 会自死锁。两把锁的获取顺序**恒为**
	//     dealMu → s.mu（removePlayer 也是先 dealCancelA 再 s.mu），无反向路径。
	dealMu sync.Mutex

	// world 是世界本体（玩家/怪物/地面物品/火墙/刷怪点）。
	world worldState
	// npc 是 NPC 与脚本那一摊状态（阶段 4：把散落字段按域收进子结构，见 §8.3）。
	npc npcState

	// itemSeq 分配物品唯一 ID（MakeIndex）。
	itemSeq atomic.Int64

	// eventSeq 分配**地面事件** id（客户端 `EventMan.DelEventById(Recog)` 按它抹事件，
	// 见 groundevent.go）。原版直接拿事件对象指针当 id。
	eventSeq atomic.Uint32

	// snapTimeout 是自动存档等"某个玩家交出快照"的上限（0 ⇒ defaultSnapshotTimeout）。
	// 只为测试调小用。
	snapTimeout time.Duration

	// guilds 是行会管理器（P6）。
	social socialState
	// castles 是城堡管理器（P6，单机一座）。
	castle castleState
	pvp    pvpState
}

// GroundItem 是掉在地上的物品。
type GroundItem struct {
	ID   uint32
	Name string
	// Item 是普通物品；**金币堆这里是 nil**（原版金币是"名字 + Count + Looks"的
	// TMapItem，不对应任何 StdItem）⇒ 读它之前必须先判 `Gold > 0`。见 gold.go。
	Item *pb.UserItem
	// Gold 是金币堆的金额（> 0 即金币堆）。原版 `TMapItem.Count`。
	Gold  int64
	Map   *world.Map
	X, Y  int
	Looks uint16
	// Owner 是**掉落归属**（击杀者/丢弃者的 ActorId，0 = 无归属）。
	// 原版 `TMapItem.OfBaseObject`：2 分钟内只有本人与队友能捡，见 canPickUpGround。
	Owner     uint32
	CreatedAt time.Time
}

// deathDropRateDefault 是死亡掉物品的默认概率（千分比）。
//
// 原版行为：**死亡不掉经验**，但随身物品/装备按概率掉在死亡处。
// 之前的实现反了（掉经验不掉东西），已按原版纠正。
const (
	// 出厂 `!setup.txt:814-817` 的四个值（见 configState 里的说明）。
	deathDropBagDefault       = 200  // DieScatterBagRate：包裹 1/200
	deathDropEquipDefault     = 30   // DieDropUseItemRate：装备 1/30
	deathDropRedEquipDefault  = 15   // DieRedDropUseItemRate：红名装备 1/15
	deathRedScatterBagDefault = true // DieRedScatterBagAll：红名包裹全掉
)

// savePlayer 把角色写回数据库。
//
// 原版只在**正常退出**时落盘（且索引只在退出时保存），强杀即丢档——
// 这是原版已知的坑，这里改为下线即存 + 定时存。
// saveSnapshotOf 做一份**存档用的**角色快照（深拷贝，脱离在线状态）。
//
// ⚠️ 必须在**该玩家自己的 goroutine** 上调用 —— 它才是这份状态的写者，
// 所以除了下面那一小撮，全程不需要锁：
//
//   - 背包与钱包：**别的 goroutine 也会写**（交易的成交/取消，见 invlock.go）
//     ⇒ 取深拷贝时持 `stateMu`；
//   - 其余字段（属性/装备/魔法/任务旗标…）：只有本人 goroutine 写 ⇒ 无需加锁。
//
// 自动存档跑在 ticker goroutine 上（不是本人的），所以它不直接调这个函数，
// 而是通过 `Player.snapReq` 把活投过来（见 saveAll / requestSaveSnapshot）。
func saveSnapshotOf(p *Player) *storage.Character {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return nil
	}
	// 会话态的开关同步进存档字段。
	//
	// ⚠️ 逐字段同步而不是依赖调用点记得赋值：自动存档、下线、若干脚本动作都会存盘，
	// 漏一处就丢一次开关。对应 ObjBase.pas:24920-24937（SaveHumData 那段）。
	p.Char.Data.AllowGroup = p.allowGroup
	p.Char.Data.AllowGroupRecall = p.allowGroupRecall

	// `storage.Character` 除 Data 外都是值类型 ⇒ 浅拷贝之后再换掉 Data 即可
	//（`store.Update` 会自己 `SyncFromData` 把索引列从 Data 同步过去）。
	snap := *p.Char
	p.stateMu.Lock()
	snap.Data = goproto.Clone(p.Char.Data).(*pb.CharacterData)
	p.stateMu.Unlock()
	return &snap
}

// persistCharacter 把一份**已经脱离在线状态**的快照落盘。
//
// 只碰快照、不碰在线对象 ⇒ 可以在任何 goroutine 上跑（DB IO 不持锁）。
func (s *Server) persistCharacter(chr *storage.Character) error {
	if chr == nil || chr.Data == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := s.store.Characters().Update(ctx, chr); err != nil {
		log.Printf("保存角色 %s 失败: %v", chr.Name, err)
		return err
	}
	ab := chr.Data.Abil
	if ab == nil {
		ab = &pb.Ability{}
	}
	log.Printf("已保存 %s: Lv%d 经验=%d 金币=%d HP=%d/%d 背包=%d",
		chr.Name, ab.Level, ab.Exp, chr.Data.Gold,
		ab.Hp, ab.MaxHp, nonEmptyBag(chr.Data.BagItems))
	return nil
}

// persistCharacterForSession fences saves from a replaced session; lease verification and the
// character UPDATE happen in one SQL statement inside CharacterStore.
func (s *Server) persistCharacterForSession(chr *storage.Character, sessionID int32) error {
	if sessionID == 0 {
		return s.persistCharacter(chr)
	}
	if chr == nil || chr.Data == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := s.store.Characters().UpdateForSession(ctx, chr, sessionID); err != nil {
		log.Printf("保存角色 %s 失败（会话 %d）: %v", chr.Name, sessionID, err)
		return err
	}
	ab := chr.Data.Abil
	if ab == nil {
		ab = &pb.Ability{}
	}
	log.Printf("已保存 %s: Lv%d 经验=%d 金币=%d HP=%d/%d 背包=%d",
		chr.Name, ab.Level, ab.Exp, chr.Data.Gold, ab.Hp, ab.MaxHp, nonEmptyBag(chr.Data.BagItems))
	return nil
}

// savePlayer 存盘一名角色.
//
// ⚠️ **只允许在该玩家自己的 goroutine 上调用**（下线 `removePlayer`、GM/脚本动作
// 那几个点都是）；自动存档不能这么干 —— 它跑在 ticker goroutine 上，要先请对方的
// goroutine 做快照（见 saveAll）。
func (s *Server) savePlayer(p *Player) {
	if p == nil {
		return
	}
	_ = s.persistCharacterForSession(saveSnapshotOf(p), p.sessionID)
}

// nonEmptyBag 统计背包中非空条目数。
func nonEmptyBag(bag []*pb.UserItem) int {
	n := 0
	for _, it := range bag {
		if it != nil && it.Index != 0 {
			n++
		}
	}
	return n
}

// saveAll 保存所有在线角色。
//
// ⚠️ 它跑在**自动存档 ticker 的 goroutine** 上，**不是**玩家自己的 ⇒ 不许直接
// 序列化 `p.Char`：那正是"快照 vs 变异"—— 玩家此刻可能正在吃药/捡钱/换装/交易，
// 读到的是半截状态，而且每一格都是一次真实的 data race。
//
// 做法：把"做快照"这件事**投给玩家自己的 goroutine**（`snapReq`），拿到一份
// 脱离在线状态的深拷贝，再在本 goroutine 落盘。这样：
//
//   - 玩家状态的一致性由"只有一个写者"保证（不需要给所有字段加锁）；
//   - DB IO 仍然在存档线程上，不占玩家 goroutine。
func (s *Server) saveAll() {
	s.mu.RLock()
	ps := make([]*Player, 0, len(s.world.players))
	for _, p := range s.world.players {
		ps = append(ps, p)
	}
	s.mu.RUnlock()

	saved, skipped := 0, 0
	for _, p := range ps {
		snap := s.requestSaveSnapshot(p)
		if snap == nil {
			skipped++
			continue
		}
		if err := s.persistCharacterForSession(snap, p.sessionID); err == nil {
			saved++
		}
	}
	switch {
	case len(ps) == 0:
	case skipped > 0:
		log.Printf("自动存档完成：%d 个角色（%d 个没能及时交出快照，跳过，下轮再存）", saved, skipped)
	default:
		log.Printf("自动存档完成：%d 个角色", saved)
	}
}

// defaultSnapshotTimeout 是等某个玩家交出存档快照的默认上限。
const defaultSnapshotTimeout = 2 * time.Second

// snapshotTimeout 取生效的上限（测试里会调小）。
func (s *Server) snapshotTimeout() time.Duration {
	if s.snapTimeout > 0 {
		return s.snapTimeout
	}
	return defaultSnapshotTimeout
}

// requestSaveSnapshot 请某个玩家的 goroutine 交一份存档快照，超时返回 nil。
//
// 超时**不是错误**：玩家 goroutine 可能正卡在**门禁延时**里（攻击 520ms /
// 施法 450ms，官方 !setup.txt），那段时间它根本不处理新消息。这一轮跳过该玩家
// 就行 —— 他下线时还会存一次，下一轮（默认 5 分钟）也会再存。
//
// ⚠️ 日志里读 `p.Char.Name`：它在登录之后再不改动，所以是安全的（其余字段一律不碰）。
func (s *Server) requestSaveSnapshot(p *Player) *storage.Character {
	if p == nil || p.snapReq == nil {
		return nil
	}
	reply := make(chan *storage.Character, 1)
	select {
	case p.snapReq <- reply:
	case <-time.After(s.snapshotTimeout()):
		log.Printf("请求 %s 的存档快照超时（它的 goroutine 正忙），跳过", p.Char.Name)
		return nil
	}
	select {
	case snap := <-reply:
		return snap
	case <-time.After(s.snapshotTimeout()):
		log.Printf("等 %s 的存档快照超时，跳过本轮", p.Char.Name)
		return nil
	}
}

// ---------- 自然恢复 ----------

// ---------- 存档 ----------
// saveTimeout 是单次存盘的超时上限。
const saveTimeout = 5 * time.Second

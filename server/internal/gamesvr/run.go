package gamesvr

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/algotao/mir2/server/internal/chargen"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/group"
	"github.com/algotao/mir2/server/internal/guild"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proxyproto"
	"github.com/algotao/mir2/server/internal/script"
	"github.com/algotao/mir2/server/internal/storage/sqlite"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/world"
	"github.com/algotao/mir2/server/protocol"
)

// Main 是游戏服进程的入口：解析 flag → 装配 → 起各条循环 → 收到信号后优雅退出。
//
// ⚠️ 它是**库函数**，真正的 `func main()` 在 `cmd/gamesvr/main.go`（薄入口）。
// 这样 `cmd/` 只剩装配、业务代码归 `internal/`，符合 Go 的目录约定；
// 背景见 docs/service-architecture.md §8.1/§8.3。
//
// ⚠️ 函数体仍然很长（Phase 2 会把它拆成 装配 / 循环 / 会话 三块），
// 本次只做"搬家 + 改可见性"，不动内部结构。
func Main() {
	var (
		dbPath    = flag.String("db", "./mir2go.db", "SQLite 路径（与 accountsvc 共享）")
		dataDir   = flag.String("data", "./data", "静态数据目录")
		gameAddr  = flag.String("addr", ":7200", "监听地址")
		protoAddr = flag.String("proto-addr", "",
			"新协议监听地址（[u32 长度][Envelope]，docs/protocol.md §2/§5）；留空则不监听。\n"+
				"与 legacy 的 -addr **并存**：现有回归与 mir2cli 走 legacy，新协议客户端走这里。\n"+
				"v0 只到\"进图 + 看见自己\"（见 internal/gamesvr/netproto.go 的文件头）")
		proxyProtocol = flag.Bool("proxy-protocol", false,
			"要求接入连接先带一行 PROXY protocol v1 头（网关 -proxy-protocol 会写），"+
				"从中取真实客户端 IP（docs/decisions.md D-23）。直连调试时保持关闭；"+
				"打开后缺头即断开，没有\"有头就认、没头退回 socket\"这种可伪造的中间态")
		viewRangeFlag = flag.Int("view-range", 12, "玩家视野半径（切比雪夫距离）")
		noMonster     = flag.Bool("no-monster", false, "不生成怪物")
		monsterWander = flag.Bool("monster-wander", true, "怪物是否游荡（关闭便于端到端验证战斗）")
		monsterAggro  = flag.Bool("monster-aggro", true, "怪物是否主动攻击玩家（关闭便于验证玩家攻击）")
		saveInterval  = flag.Duration("save-interval", 5*time.Minute, "自动存档间隔")
		grantAll      = flag.Bool("grant-all-magics", false, "【仅测试】进游戏时授予全部技能")
		noRegen       = flag.Bool("noregen", false,
			"【仅测试】关闭自然恢复。用来让\"受伤后立刻验证治疗类技能\"变得确定 —— "+
				"高等级玩家回血速度远快于怪物掉血，不关掉的话治疗会被 regenLoop 的回满抢先一步")
		mapDir       = flag.String("map-dir", "./data/map", "地图目录（含 <地图号>.map）")
		defaultMapID = flag.String("map", "0", "默认地图号（对应 <map-dir>/<地图号>.map）")
		homePoints   = flag.String("home-points", "650,631;289,618",
			"新角色出生点候选（`x,y;x,y`；**多于一个就随机挑一个**）。"+
				"默认 = 原版 1.76 的两个新手村：银杏山谷(650,631) 与 边界村(289,618)"+
				"（口径见 docs/use.md 与 D-40）；想钉死某个点就只留一个")
		maxSpawns = flag.Int("max-spawns", 0, "最多加载多少个刷怪点（0=不限）")
		// 开发期换刷怪表：默认是 1.76 的全量配置；`envir/mongen.newbie.txt` 只在新手村刷鹿
		//（用户 2026-10-09 要的"简化体验环境"）。相对路径按 -data 目录解析。
		monGenFile = flag.String("mongen", "envir/mongen.txt",
			"刷怪表（相对 -data 目录）；envir/mongen.newbie.txt = 只在两个新手村刷鹿")
		mapCacheLimit = flag.Int("map-cache", 0,
			"已弃用：完整初始化要求全部地图常驻；非 0 值会被忽略")
		scriptDir = flag.String("script-dir", "./data/envir/market_def",
			"NPC 脚本目录（market_def）")
		adminFile = flag.String("admin-file", "./data/envir/AdminList.txt",
			"GM 名单（AdminList.txt）：不在名单里的人不能用 @ 命令")
		gmOpen = flag.Bool("gm-open", false,
			"【仅测试】忽略 GM 名单，人人可用 @ 命令（回归用例全靠 @ 驱动）")
		allowNewAccount = flag.Bool("allow-new-account", false,
			"开放注册（原版 boEnableMakingID；默认**关**——开着等于把注册入口挂公网）")
		mapPreload = flag.Bool("map-preload", true,
			"兼容旧参数；完整初始化始终在启动时预加载所有地图")
		buildGuildGold = flag.Int64("build-guild-gold", 1_000_000,
			"建会费用（原版 Setup/BuildGuild）")
		buildGuildItem = flag.String("build-guild-item", "沃玛号角",
			"建会所需号角物品（原版 Names/WomaHorn）")
		timeScaleFlag = flag.Float64("time-scale", 1,
			"游戏内时间流速倍率（1=正常；10=十倍速，用于加速端到端回归。客户端需传同一个值）")
		eventLogPath = flag.String("event-log", "",
			"事件日志（JSON Lines）输出路径；留空则关闭")
		eventLogAppend = flag.Bool("event-log-append", true,
			"事件日志追加到既有文件（保留上一次运行的时间线）")
		deathDropBagRate = flag.Int("death-drop-bag-rate", deathDropBagDefault,
			"死亡掉包裹物品的概率（原版语义 **1/N**，出厂 200；1=必掉、0=不掉）")
		deathDropEquipRate = flag.Int("death-drop-equip-rate", deathDropEquipDefault,
			"死亡掉装备的概率（1/N，出厂 30；1=必掉、0=不掉）")
		deathDropRedEquipRate = flag.Int("death-drop-red-equip-rate", deathDropRedEquipDefault,
			"红名（PKLevel>2）死亡掉装备的概率（1/N，出厂 15）")
		deathRedScatterAll = flag.Bool("death-red-scatter-all", deathRedScatterBagDefault,
			"红名（PKLevel>=2）死亡**包裹全掉**（原版 DieRedScatterBagAll=1）")
		guildWarDur = flag.Duration("guild-war-duration", 30*time.Minute,
			"行会战持续时间（到期自动结束）")
		guildWarGold = flag.Int64("guild-war-fee", 30_000,
			"行会战宣战费用（原版 Setup/GuildWarFee）")
		castleDir = flag.String("castle-dir", "./data/castle",
			"城堡配置目录（含 <子目录>/SabukW.txt）；留空则用出厂默认值")
		makeItemFile = flag.String("makeitem-file", "./data/envir/MakeItem.txt",
			"官方制药配方表（MakeItem.txt：[成品名] + 材料名 数量）；读不到则制药一律失败")
		makeDrugPrice = flag.Int("makedrug-price", 100,
			"制药单价（官方 !setup.txt:256 MakeDurg=100）")
		miniMapFile = flag.String("minimap-file", "./data/envir/MiniMap.txt",
			"官方小地图对照表（MiniMap.txt：地图号 小地图编号）；读不到则 CM_WANTMINIMAP 一律回 FAIL")
		expFile = flag.String("exp-file", "./data/envir/Exps.ini",
			"官方经验表（Exps.ini 的 [Exp] 段，LevelN = N→N+1 级所需经验）；读不到则退回内置公式")
		castleConfigDir = flag.String("castle-config-dir", "0",
			"城堡配置子目录（单机固定 0）")
		castleTaxAllNPC = flag.Bool("castle-tax-all-npc", false,
			"所有 NPC 交易都抽城堡税（原版 boGetAllNpcTax，默认关）")
		wallNoSafeZone = flag.Bool("wall-no-safezone", false,
			"安全区不允许铺火墙（原版 boDisableInSafeZoneFireCross，默认关=允许）")
		castleWarDur = flag.Duration("castle-war-duration", 3*time.Hour,
			"攻城战攻守期时长（原版 CastleWarTime）")
		castleWarHour = flag.Int("castle-war-hour", 20,
			"开城时刻（小时，0..23；原版 StartCastlewarTime）")
		castleDeclareDays = flag.Int("castle-declare-days", 4,
			"宣战到开战的提前天数（原版 StartCastleWarDays）")
		castleTaxRate = flag.Int("castle-tax-rate", 5,
			"城堡税率（百分数，原版 CastleTaxRate）")
	)
	flag.Parse()

	// 时间倍速与事件日志要在任何定时器/限流建立**之前**就位：
	// 限流器在 NewPlayer 里构造，读的就是 tscale 的当前值。
	tscale.Set(*timeScaleFlag)
	if *timeScaleFlag != 1 {
		log.Printf("时间流速 = %gx（客户端需传同样的 -time-scale）", tscale.Get())
	}
	if err := obs.Init(*eventLogPath, obs.LevelInfo, *eventLogAppend); err != nil {
		log.Fatalf("事件日志: %v", err)
	}
	defer obs.Close()

	store, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatalf("打开数据库: %v", err)
	}
	defer store.Close()

	tables, err := data.LoadDir(*dataDir)
	if err != nil {
		log.Fatalf("加载静态数据: %v", err)
	}

	// 地图与刷怪点都已接真实数据（`data/map/*.map` 605 张、`data/envir/mongen.txt`）。
	// ⚠️ 这里**曾经**写着 `TODO(P3)：真实地图与 MonGen.txt 数据缺失，先用程序化地图
	// 与刷怪点驱动`——已过时（数据到位了，回退分支只是防御）。`world.Generate` 现在
	// 只给单测用。
	cacheLimit := *mapCacheLimit
	if cacheLimit > 0 {
		log.Printf("WARN: -map-cache=%d 已忽略：全量地图初始化期间地图必须常驻", cacheLimit)
		cacheLimit = 0
	}
	if !*mapPreload {
		log.Printf("WARN: -map-preload=false 已忽略：启动始终执行全量初始化")
	}
	maps := world.NewMapManager(*mapDir, cacheLimit)
	dm := mustDefaultMap(maps, *defaultMapID)
	srv := &Server{
		store: store,
		cfg: configState{
			viewRange:      *viewRangeFlag,
			saveInterval:   *saveInterval,
			grantAll:       *grantAll,
			noRegen:        *noRegen,
			wander:         *monsterWander,
			aggro:          *monsterAggro,
			wallNoSafeZone: *wallNoSafeZone,
			proxyProtocol:  *proxyProtocol,
		},
		data: dataState{
			tables:       tables,
			defaultMapID: *defaultMapID, // 建角要用它当出生地图（见 newCharHome）
			homePoints:   mustHomePoints(*homePoints, *defaultMapID),
			drops:        defaultDrops(tables),
		},
		world: worldState{
			maps:         maps,
			defaultMap:   dm,
			players:      make(map[uint32]*Player),
			index:        world.NewSpatialIndex(32),
			monsters:     make(map[uint32]*entity.Monster),
			monsterIdx:   world.NewSpatialIndex(32),
			ground:       make(map[uint32]*GroundItem),
			groundEvents: make(map[groundKey]*groundEvent),
			walls:        make(map[wallKey]*Wall),
		},
		npc: npcState{
			spawned:   make(map[string]bool),
			scripts:   make(map[string]*script.Script),
			scriptDir: *scriptDir,
		},
		social: socialState{
			groups:   group.New(),
			guildCfg: defaultGuildConfig(),
		},
		castle: castleState{
			config: defaultCastleConfig(),
		},
		pvp: pvpState{
			cfg: defaultPVPConfig(),
		},
	}

	srv.social.guildCfg.buildGold = *buildGuildGold
	srv.social.guildCfg.buildItem = *buildGuildItem
	srv.cfg.deathDropBagOneIn = *deathDropBagRate
	srv.cfg.deathDropEquipOneIn = *deathDropEquipRate
	srv.cfg.deathDropRedEquipIn = *deathDropRedEquipRate
	srv.cfg.deathScatterBagAllOn = *deathRedScatterAll
	srv.social.guildCfg.warGold = *guildWarGold
	srv.social.guildCfg.warDuration = *guildWarDur
	srv.social.guilds = guild.NewManager(store.Guilds())
	if err := srv.social.guilds.Load(context.Background()); err != nil {
		log.Fatalf("加载行会: %v", err)
	}
	if n := srv.social.guilds.Count(); n > 0 {
		log.Printf("行会: %d 个", n)
	}

	// 城堡依赖行会（联盟/宣战判定），必须在行会载入之后。
	srv.castle.config.cfg.WarDuration = *castleWarDur
	srv.castle.config.cfg.StartWarHour = *castleWarHour
	srv.castle.config.cfg.DeclareDays = *castleDeclareDays
	srv.castle.config.cfg.TaxRate = *castleTaxRate
	srv.castle.config.taxAllNpc = *castleTaxAllNPC
	srv.initCastle(*castleDir, *castleConfigDir)

	// 官方经验表（Exps.ini 的 [Exp] 段）。读不到就退回内置公式、不阻断启动，
	// 但升级节奏会与原版不一致，所以把失败打出来。
	if need, err := data.LoadExps(*expFile); err != nil {
		log.Printf("经验表读取失败（退回内置公式 100·(L-1)²）: %v", err)
	} else {
		entity.SetLevelNeed(need)
		log.Printf("经验表已装载：%d 级（1→2 需 %d 经验，2→3 需 %d）",
			len(need)-1, entity.LevelNeed(1), entity.LevelNeed(2))
	}

	// 官方制药配方表（MakeItem.txt）。读不到不阻断启动 —— 制药会一律回"材料不足"。
	if mk, err := data.LoadMakeItems(*makeItemFile); err != nil {
		log.Printf("制药配方表读取失败（制药将不可用）: %v", err)
	} else {
		srv.data.makeItems = mk
		srv.cfg.makeDrugPrice = *makeDrugPrice
		log.Printf("制药配方表已装载：%d 条（%s，单价 %d 金币）", mk.Len(), *makeItemFile, *makeDrugPrice)
	}

	// 官方小地图对照表（MiniMap.txt）。读不到不阻断启动 —— 客户端只是看不到小地图
	//（`CM_WANTMINIMAP` 一律回 SM_READMINIMAP_FAIL）。
	if mm, err := data.LoadMiniMap(*miniMapFile); err != nil {
		log.Printf("小地图对照表读取失败（客户端将看不到小地图）: %v", err)
	} else {
		srv.cfg.miniMaps = mm
		log.Printf("小地图对照表已装载：%d 张图（%s）", len(mm), *miniMapFile)
	}

	// GM 名单（Envir/AdminList.txt）。空名单 = 谁都不是 GM（安全默认）。
	srv.cfg.adminList, err = data.LoadAdminList(*adminFile)
	if err != nil {
		log.Printf("GM 名单读取失败（%s）：%v —— 谁都不是 GM", *adminFile, err)
		srv.cfg.adminList = &data.AdminList{}
	}
	srv.cfg.gmOpen = *gmOpen
	srv.cfg.allowNewAccount = *allowNewAccount
	if srv.cfg.allowNewAccount {
		log.Printf("⚠️ -allow-new-account 已开启：**任何人都能建号**（每 IP 5 秒一个）")
	}
	if srv.cfg.gmOpen {
		log.Printf("⚠️ -gm-open 已开启：**所有**玩家都能用 @ 命令（仅测试用）")
	} else {
		log.Printf("GM 名单已装载：%d 条（%s）", srv.cfg.adminList.Len(), *adminFile)
	}
	canonicalMapIDs := make(map[string]string)
	mapInfoPath := filepath.Join(*dataDir, "envir", "mapinfo.txt")
	infos, mapInfoErr := data.LoadMapInfo(mapInfoPath)
	if mapInfoErr != nil {
		log.Printf("WARN: 地图信息读取失败（%s）: %v", mapInfoPath, mapInfoErr)
	} else {
		names := make(map[string]string, len(infos))
		for _, mi := range infos {
			canonicalMapIDs[strings.ToLower(mi.ID)] = mi.ID
			names[mi.ID] = mi.Name
		}
		maps.SetNames(names)
		srv.data.mapInfos = infos
		srv.indexMapInfos(infos) // 地图号 → 标记（见 mapflags.go）
		log.Printf("地图信息: %d 张", len(infos))
	}
	merchantPath := filepath.Join(*dataDir, "envir", "merchant.txt")
	if ms, err := data.LoadMerchants(merchantPath); err != nil {
		log.Printf("WARN: 商人配置读取失败（%s）: %v", merchantPath, err)
	} else {
		for _, np := range ms {
			np.MapID = canonicalMapName(np.MapID, canonicalMapIDs)
		}
		srv.npc.defs = append(srv.npc.defs, ms...)
	}
	npcsPath := filepath.Join(*dataDir, "envir", "Npcs.txt")
	if ns, err := data.LoadNpcs(npcsPath); err != nil {
		log.Printf("WARN: 普通 NPC 配置读取失败（%s）: %v", npcsPath, err)
	} else {
		for _, np := range ns {
			np.MapID = canonicalMapName(np.MapID, canonicalMapIDs)
		}
		srv.npc.defs = append(srv.npc.defs, ns...)
	}
	if len(srv.npc.defs) > 0 {
		log.Printf("NPC 定义: %d 个", len(srv.npc.defs))
	}

	startPointPath := filepath.Join(*dataDir, "envir", "StartPoint.txt")
	if sps, err := data.LoadStartPoints(startPointPath); err != nil {
		log.Printf("WARN: 出生点配置读取失败（%s）: %v", startPointPath, err)
	} else {
		for _, sp := range sps {
			sp.MapID = canonicalMapName(sp.MapID, canonicalMapIDs)
		}
		srv.data.startPoints = sps
		log.Printf("出生点: %d 个", len(sps))
	}
	// PvP 判定依赖区域属性与出生点（安全区），要在任何玩家进来之前就绪。
	srv.initPVP(srv.data.mapInfos, srv.data.startPoints)

	// 社区包的 MonGen 与地图、怪物模板一并初始化；只禁用怪物实体时仍保留定义诊断。
	// 默认 `envir/mongen.txt`（1.76 全量）；`-mongen` 可换成开发期的小表（见旗标说明）。
	monGenPath := *monGenFile
	if !filepath.IsAbs(monGenPath) {
		monGenPath = filepath.Join(*dataDir, monGenPath)
	}
	log.Printf("刷怪表: %s", monGenPath)
	spawns, spawnIssues, spawnErr := loadMonGen(monGenPath, tables, *maxSpawns)
	if spawnErr != nil {
		log.Printf("WARN: 刷怪配置读取失败（%s）: %v", monGenPath, spawnErr)
		if !*noMonster {
			spawns = defaultSpawns(tables)
		}
	}
	for _, issue := range spawnIssues {
		log.Printf("WARN: %s", issue)
	}
	if *maxSpawns > 0 {
		log.Printf("WARN: -max-spawns=%d 已截断全地图刷怪配置，仅用于测试/诊断", *maxSpawns)
	}
	for i := range spawns {
		spawns[i].MapName = canonicalMapName(spawns[i].MapName, canonicalMapIDs)
	}
	if !*noMonster {
		srv.world.spawns = spawns
	}

	drops, err := entity.LoadMonItems(filepath.Join(*dataDir, "monitems"), func(name string) bool {
		return tables.Items.GetByName(name) != nil
	})
	if err != nil {
		log.Fatalf("加载掉落表失败: %v", err)
	}
	srv.data.dropTables = drops
	if st := drops.Stats(); st.Files == 0 {
		log.Printf("WARN: 没有加载到任何 MonItems 掉落表（%s）", filepath.Join(*dataDir, "monitems"))
	} else {
		log.Printf("掉落表: %d 个文件 %d 条规则（坏行 %d，物品表外 %d 种）",
			st.Files, st.Rules, st.BadLines, st.UnknownItems)
	}
	for _, warning := range drops.Warnings() {
		log.Printf("WARN: %s", warning)
	}
	warnUnmatchedDropTables(drops, tables)
	warnMissingSpawnDrops(spawns, drops)

	preferredMapIDs := make([]string, 0, len(srv.data.mapInfos)+len(srv.npc.defs)+len(srv.data.startPoints)+len(spawns)+1)
	for _, mi := range srv.data.mapInfos {
		preferredMapIDs = append(preferredMapIDs, mi.ID)
	}
	for _, np := range srv.npc.defs {
		preferredMapIDs = append(preferredMapIDs, np.MapID)
	}
	for _, point := range srv.data.startPoints {
		preferredMapIDs = append(preferredMapIDs, point.MapID)
	}
	for _, spawn := range spawns {
		preferredMapIDs = append(preferredMapIDs, spawn.MapName)
	}
	preferredMapIDs = append(preferredMapIDs, *defaultMapID)
	mapIDs, mapIssues, preloadErr := preloadAllMaps(maps, *mapDir, preferredMapIDs)
	if preloadErr != nil {
		log.Printf("WARN: 地图目录预加载失败（%s）: %v", *mapDir, preloadErr)
	}
	for _, issue := range mapIssues {
		log.Printf("WARN: %s", issue)
	}
	loadedMapIDs := make(map[string]bool, len(mapIDs))
	for _, id := range mapIDs {
		loadedMapIDs[strings.ToLower(id)] = true
	}
	if !loadedMapIDs[strings.ToLower(*defaultMapID)] {
		if _, err := maps.Get(*defaultMapID); err == nil {
			mapIDs = append(mapIDs, *defaultMapID)
			loadedMapIDs[strings.ToLower(*defaultMapID)] = true
		}
	}
	for _, point := range srv.data.startPoints {
		if !loadedMapIDs[strings.ToLower(point.MapID)] {
			log.Printf("WARN: 出生点地图 %s 未加载，出生点 (%d,%d) 不可用", point.MapID, point.X, point.Y)
			continue
		}
		mp, err := maps.Get(point.MapID)
		if err != nil || !mp.InBounds(point.X, point.Y) {
			log.Printf("WARN: 出生点 %s (%d,%d) 无有效地图坐标", point.MapID, point.X, point.Y)
		}
	}
	for _, np := range srv.npc.defs {
		if !loadedMapIDs[strings.ToLower(np.MapID)] {
			log.Printf("WARN: NPC %s（%s）地图 %s 未加载，NPC 不会生成", np.Name, np.ID, np.MapID)
		}
	}

	validSpawns := spawns[:0]
	for _, spawn := range srv.world.spawns {
		if !loadedMapIDs[strings.ToLower(spawn.MapName)] {
			log.Printf("WARN: MonGen 怪物 %q 所在地图 %s 未能初始化，刷怪点 (%d,%d) 被跳过",
				spawn.MonsterName, spawn.MapName, spawn.X, spawn.Y)
			continue
		}
		mp, err := maps.Get(spawn.MapName)
		if err != nil || !mp.InBounds(spawn.X, spawn.Y) {
			log.Printf("WARN: MonGen 刷怪点 %s (%d,%d) 无有效地图坐标，已跳过",
				spawn.MapName, spawn.X, spawn.Y)
			continue
		}
		validSpawns = append(validSpawns, spawn)
	}
	if !*noMonster {
		srv.world.spawns = validSpawns
	}
	log.Printf("全量地图初始化完成: %d 张地图，NPC 定义 %d 个，刷怪点 %d 个",
		len(mapIDs), len(srv.npc.defs), len(srv.world.spawns))

	for _, mapID := range mapIDs {
		srv.spawnNPCs(mapID)
	}
	for _, np := range srv.npc.defs {
		if !np.IsMerchant {
			continue
		}
		if srv.npcScript(np.ID, np.MapID) == nil {
			log.Printf("WARN: 商人 %s（%s，地图 %s）缺少可解析脚本", np.Name, np.ID, np.MapID)
		}
	}

	// 启动只初始化默认地图；全量地图配置保留在 spawns 中，首次进其它地图时再激活，
	// 避免启动时在一个 gamesvr 实例化社区包全部 60k+ 的配置怪物。
	srv.activateSpawnMap(dm.Name)

	// 单独打一行：'客户端到底是谁' 是排查封禁/多开/审计时的第一个问题，
	// 而这两种模式的表现**完全不同**（直连模式下所有人看起来都来自网关）。
	if *proxyProtocol {
		log.Printf("客户端地址来源: PROXY protocol v1 头（要求网关转发；缺头即断开）")
	} else {
		log.Printf("客户端地址来源: TCP 对端地址（直连模式；经网关转发时看到的会是网关自己）")
	}

	ln, err := net.Listen("tcp", *gameAddr)
	if err != nil {
		log.Fatalf("监听 %s: %v", *gameAddr, err)
	}
	log.Printf("gamesvr 启动: addr=%s 物品=%d 怪物模板=%d 技能=%d 地图=%dx%d 全图=%d NPC=%d 怪物=%d 视野=%d 刷怪点=%d",
		*gameAddr, tables.Items.Len(), tables.Monsters.Len(), tables.Magics.Len(),
		dm.Width(), dm.Height(), len(mapIDs), len(srv.npc.defs), len(srv.world.monsters), srv.cfg.viewRange, len(srv.world.spawns))
	go srv.spawnLoop()
	go srv.monsterLoop()
	go srv.autosaveLoop()
	go srv.sessionLeaseLoop()
	go srv.regenLoop()
	go srv.castleLoop()
	// 中毒结算与血条到期（原版挂在每个对象的 Run 里，我们单独一条 0.5 秒的循环）
	go srv.poisonTickLoop()
	go srv.pvpLoop()
	go srv.scriptTimerLoop()
	go srv.wallLoop()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.acceptConn(c)
		}
	}()

	// 新协议入口（并存，见 netproto.go 的文件头）。默认关闭：它不是回归路径，
	// 而是"客户端唯一对端"（D-13）那条线的落点，还没做到能替代 legacy。
	if *protoAddr != "" {
		pln, err := net.Listen("tcp", *protoAddr)
		if err != nil {
			log.Fatalf("监听新协议 %s: %v", *protoAddr, err)
		}
		log.Printf("新协议入口 %s（[u32 长度][Envelope]，版本 %d）", *protoAddr, protocol.Version)
		go func() {
			for {
				c, err := pln.Accept()
				if err != nil {
					return
				}
				// 真实客户端 IP 的取得与 legacy 入口**同一套**（PROXY 头或 socket 对端）。
				ec, ip, err := proxyproto.ServerConn(c, srv.cfg.proxyProtocol, proxyHeaderTimeout)
				if err != nil {
					log.Printf("%s: 新协议取得客户端地址失败，断开: %v", c.RemoteAddr(), err)
					_ = c.Close()
					continue
				}
				go srv.serveProtoConn(ec, ip)
			}
		}()
		defer pln.Close()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("收到退出信号，保存所有在线角色...")
	srv.saveAll()
	if srv.castle.mgr != nil {
		srv.castle.mgr.SaveNow(castleCtx())
	}
	log.Println("关闭中...")
	_ = ln.Close()
	_ = store.Close()
}

// loadMonGen 读取社区包的全部地图刷怪点，并报告坏行与缺失的怪物模板。
// 解析复用 entity.ParseMonGenLine（怪物名可能带引号含空格）。
func loadMonGen(path string, tables *data.Tables, limit int) ([]entity.SpawnPoint, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var (
		out    []entity.SpawnPoint
		issues []string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		sp, err := entity.ParseMonGenLine(line)
		if err != nil {
			issues = append(issues, fmt.Sprintf("MonGen %s:%d 解析失败: %v", path, lineNo, err))
			continue
		}
		if sp.Range < 0 {
			issues = append(issues, fmt.Sprintf("MonGen %s:%d 范围为负数 %d", path, lineNo, sp.Range))
			continue
		}
		if tables.Monsters.GetByName(sp.MonsterName) == nil {
			issues = append(issues, fmt.Sprintf("MonGen %s:%d 缺少怪物模板 %q（地图 %s）", path, lineNo, sp.MonsterName, sp.MapName))
			continue
		}
		if limit <= 0 || len(out) < limit {
			out = append(out, sp)
		}
	}
	if err := sc.Err(); err != nil {
		return out, issues, fmt.Errorf("扫描 MonGen: %w", err)
	}
	return out, issues, nil
}

// mustDefaultMap 取默认地图；自定义目录里的地图缺失或损坏时，回退到程序生成的空地图。
// 社区地图默认随仓库位于 data/map，运行时也允许用 -map-dir 覆盖。
func mustDefaultMap(mm *world.MapManager, id string) *world.Map {
	const fallbackW, fallbackH = 200, 200
	m, err := mm.Get(id)
	if err == nil {
		log.Printf("已加载地图 %s: %dx%d", id, m.Width(), m.Height())
		return m
	}
	log.Printf("WARN: 地图 %s 加载失败（%v），回退到程序生成的 %dx%d 空地图",
		id, err, fallbackW, fallbackH)
	fb := world.Generate(id, fallbackW, fallbackH, true)
	mm.Put(fb)
	return fb
}

// mustHomePoints 解析 `-home-points`（`x,y;x,y`）⇒ 新角色出生点候选。
//
// 解析本身在 `chargen.ParseHomePoints`（**legacy 那条路共用同一份**）；
// 这里只负责"坏输入立刻启动失败"，而不是带着空表跑起来。
func mustHomePoints(spec, mapID string) []chargen.Home {
	homes, err := chargen.ParseHomePoints(spec, mapID)
	if err != nil {
		log.Fatalf("-home-points 解析失败：%v", err)
	}
	return homes
}

// preloadAllMaps 按配置中的规范地图号优先初始化，再加载目录中剩余的所有地图文件。
// failures 对每个缺失或损坏项保留独立诊断，调用方按 WARN 输出。
func preloadAllMaps(mm *world.MapManager, dir string, preferredIDs []string) ([]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var (
		loaded   []string
		failures []string
	)
	attempted := make(map[string]bool)
	load := func(id string) {
		key := strings.ToLower(id)
		if id == "" || attempted[key] {
			return
		}
		attempted[key] = true
		if _, err := mm.Get(id); err != nil {
			failures = append(failures, fmt.Sprintf("地图 %s 加载失败: %v", id, err))
			return
		}
		loaded = append(loaded, id)
	}
	for _, id := range preferredIDs {
		load(id)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".map") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		load(id)
	}
	return loaded, failures, nil
}

func canonicalMapName(id string, canonical map[string]string) string {
	if name, ok := canonical[strings.ToLower(id)]; ok {
		return name
	}
	return id
}

func warnUnmatchedDropTables(drops *entity.MonItems, tables *data.Tables) {
	if drops == nil || tables == nil || tables.Monsters == nil {
		return
	}
	names := make([]string, 0, len(drops.Tables()))
	for name := range drops.Tables() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		matched := false
		for _, mon := range tables.Monsters.All() {
			if mon.Name == name || strings.TrimRight(mon.Name, "0123456789") == name {
				matched = true
				break
			}
		}
		if !matched {
			log.Printf("WARN: MonItems/%s.txt 缺少对应的怪物模板", name)
		}
	}
}

func warnMissingSpawnDrops(spawns []entity.SpawnPoint, drops *entity.MonItems) {
	seen := make(map[string]bool)
	for _, spawn := range spawns {
		if seen[spawn.MonsterName] {
			continue
		}
		seen[spawn.MonsterName] = true
		if drops.Get(spawn.MonsterName) == nil {
			log.Printf("WARN: MonGen 怪物 %q 缺少 MonItems 掉落表", spawn.MonsterName)
		}
	}
}

// defaultSpawns 是 MonGen 文件缺失时的最小测试/开发回退配置；正常启动加载社区包全量刷怪点。
func defaultSpawns(tables *data.Tables) []entity.SpawnPoint {
	// 优先低血量怪物：便于端到端验证"击杀 → 经验"链路
	names := []string{"鸡", "鸡", "鹿", "稻草人", "多钩猫"}
	var out []entity.SpawnPoint
	for i, n := range names {
		if tables.Monsters.GetByName(n) == nil {
			continue
		}
		// 靠近出生点（100,100）布置，Range 小一些，便于端到端验证战斗链路
		out = append(out, entity.SpawnPoint{
			MapName:     "0",
			X:           99 + (i % 3),
			Y:           102 + (i / 3),
			MonsterName: n,
			Range:       1,
			Count:       3,
			RefreshMin:  1,
		})
	}
	return out
}

// defaultDrops 是社区包掉落表缺失时的少量基础回退表；正常情况下优先使用 MonItems。
func defaultDrops(tables *data.Tables) map[string]*entity.DropTable {
	names := []string{"鸡", "鹿", "稻草人", "多钩猫", "钉耙猫"}
	// 只保留物品表里真实存在的掉落物，避免刷出不存在的物品
	candidates := []entity.DropItem{
		{SelPoint: 0, MaxPoint: 25, ItemName: "金创药(小量)", Count: 1},
		{SelPoint: 25, MaxPoint: 40, ItemName: "太阳水", Count: 1},
		{SelPoint: 40, MaxPoint: 50, ItemName: "木剑", Count: 1},
	}
	out := make(map[string]*entity.DropTable, len(names))
	for _, n := range names {
		if tables.Monsters.GetByName(n) == nil {
			continue
		}
		var items []entity.DropItem
		for _, c := range candidates {
			if tables.Items.GetByName(c.ItemName) != nil {
				items = append(items, c)
			}
		}
		out[n] = entity.NewDropTable(items)
	}
	return out
}

// ---------- 怪物 ----------

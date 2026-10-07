package gamesvr

// 沙巴克城堡（P6）——状态机接线。
//
// 行为对照（服务端逻辑在 Delphi Castle.pas）：
//
//	开战/结束：全服广播 SendBroadCastMsgExt（Castle.pas:673、886）
//	攻陷：ObjBase.pas:6536-6542，皇宫清场后自动换主 + 广播
//	头顶头衔：SM_CHANGEGUILDNAME，占领方成员显示 "[沙巴克]行会名"
//	      （ObjBase.pas:25875-25901，格式串 g_sCastleGuildName）
//	税收：IncRateGold 5%（Castle.pas:1022-1066），触发点在 ObjNpc
//
// 宣战 / 存取金 / GM 命令见 castlecmd.go。

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
)

// castleTickInterval 是城堡状态机的推进周期。
//
// 原版 svMain 每 10 秒调一次 g_CastleManager.Run（svMain.pas:1701-1706）；
// 判定只比"小时"，精度完全够。
const castleTickInterval = 10 * time.Second

// castleConfig 是城堡玩法配置。默认值取自 g_Config（M2Share.pas）。
type castleConfig struct {
	// cfg 是 internal/castle 的玩法参数（税率/时长/各项上限）。
	cfg castle.Config
	// declareItem 是宣战道具名（原版 g_Config.sZumaPiece）。
	declareItem string
	// taxAllNpc 对应 boGetAllNpcTax（默认 False，M2Share.pas:2093）。
	//
	// ⚠️ 原版只对**城堡 NPC**（m_boCastle）或开了这个全局开关才抽税。
	// 我们没有 NPC 的 m_boCastle 标记（那依赖沙巴克专用 NPC 脚本），
	// 所以开关关着时等于"完全不抽税"，与出厂值一致。
	// 要开就加 -castle-tax-all-npc。
	taxAllNpc bool
	// enabled 为 false 时完全不启用城堡。
	enabled bool
}

// defaultCastleConfig 返回出厂配置。
func defaultCastleConfig() castleConfig {
	return castleConfig{cfg: castle.DefaultConfig(), declareItem: "祖玛碎片"}
}

// castleCtx 是城堡落库用的上下文（本地 SQLite 短操作，无可取消链路）。
func castleCtx() context.Context { return context.Background() }

// ---------- 初始化 ----------

// initCastle 载入城堡配置并注册状态机回调。
//
// 官方配置在 <castleDir>/<configDir>/SabukW.txt（**GBK 编码**）。
// 读不到就用 internal/castle.DefaultRecord() 的出厂值，不阻断启动——
// 城堡是可选玩法，不该因为缺一个配置文件就开不了服。
func (s *Server) initCastle(castleDir, configDir string) {
	if configDir == "" {
		configDir = castle.DefaultConfigDir
	}
	rec := castle.DefaultRecord()
	rec.ConfigDir = configDir
	if castleDir != "" {
		got, err := castle.LoadSabukConfig(castleDir, configDir)
		if err != nil {
			log.Printf("城堡配置读取失败（用出厂默认值）: %v", err)
		} else {
			rec = castle.LoadAttackSabukWall(castleDir, configDir, got)
		}
	}

	mgr := castle.NewManager(s.store.Castles(), s.social.guilds, s.castle.config.cfg)
	if err := mgr.Load(castleCtx(), []storage.Castle{rec}); err != nil {
		log.Printf("城堡载入失败（本次不启用）: %v", err)
		return
	}
	mgr.SetHooks(s.castleHooks())
	s.castle.mgr = mgr
	s.castle.config.enabled = true

	cs, err := mgr.Default()
	if err != nil {
		log.Printf("城堡启用失败: %v", err)
		return
	}
	r := cs.Record()
	log.Printf("城堡已启用：%s（战场 %s / 皇宫 %s / 密道 %s，回城点 %s(%d,%d)，占领方 %q）",
		cs.Name(), r.MapName, r.PalaceMap, r.SecretMap, r.HomeMap, r.HomeX, r.HomeY, cs.OwnGuild())
	// 生成城门/城墙（守卫与弓箭手要等雇佣，配置里 HP=0 表示未雇佣）。
	s.spawnCastleUnits(cs)
	// 城门按存档/配置里的状态落一次：官方 SabukW.txt 的 `MainDoorOpen=1`（初始开）。
	//
	// ⚠️ 必须先 spawn 再落状态：门的实体是 `applyCastleDoorCells` 的坐标来源。
	s.applyCastleDoorState(cs)
	s.syncCastleStone(cs)
}

// castleHooks 把状态机事件接到游戏层（广播 + 头衔刷新）。
func (s *Server) castleHooks() castle.Hooks {
	return castle.Hooks{
		OnWarStart: func(cs *castle.Castle) {
			s.broadcastSysMsg(fmt.Sprintf("[%s 攻城战已经开始]", cs.Name()))
			// 原版开战即关门（`MainDoorControl(True)`，Castle.pas:676 与
			// ObjBase.pas:12907 的 GM 强制开关战两处），再刷皇宫内玩家的头顶行会名
			//（StartWallconquestWar，Castle.pas:848-862）。
			s.setCastleDoorOpened(cs, false)
			// 石化状态本来是 castleLoop 每 10 秒（游戏时间）同步一次，开战/终战
			// 这里立刻同步一遍：否则"刚开战砍不动城墙"要等下一拍，且 e2e 得靠 sleep 赌。
			s.syncCastleStone(cs)
			s.refreshCastleTitles(cs)
			log.Printf("城堡攻城战开始：%s，攻守方 %v", cs.Name(), cs.Participants())
		},
		OnEndingSoon: func(cs *castle.Castle, left time.Duration) {
			mins := int(left.Minutes())
			if mins < 1 {
				mins = 1
			}
			s.broadcastSysMsg(fmt.Sprintf("[%s 攻城战还有 %d 分钟结束]", cs.Name(), mins))
		},
		OnWarEnd: func(cs *castle.Castle) {
			s.broadcastSysMsg(fmt.Sprintf("[%s 攻城战已经结束]", cs.Name()))
			// 同上：终战立刻把城墙恢复石化（`m_boUnderWar := False` ⇒ 墙石化）
			s.syncCastleStone(cs)
			s.refreshCastleTitles(cs)
			log.Printf("城堡攻城战结束：%s", cs.Name())
		},
		OnOccupantChanged: func(cs *castle.Castle, oldGuild string) {
			s.broadcastSysMsg(fmt.Sprintf("[%s 已被 %s 占领]", cs.Name(), cs.OwnGuild()))
			log.Printf("城堡换主：%q -> %q", oldGuild, cs.OwnGuild())
			// 换主要刷新新旧双方的抬头信息（GetCastle 里 RefMemberName ×2，
			// Castle.pas:840-841）。
			s.refreshCastleTitles(cs)
			if oldGuild != "" {
				s.refreshGuildOnline(oldGuild)
			}
			if cs.OwnGuild() != "" {
				s.refreshGuildOnline(cs.OwnGuild())
			}
		},
		OnDayChange: func(cs *castle.Castle) {
			log.Printf("城堡 %s 跨日：当日税收清零，重新允许开战", cs.Name())
		},
	}
}

// ---------- 循环 ----------

// castleLoop 推进城堡状态机（每 10 秒）。
func (s *Server) castleLoop() {
	if s.castle.mgr == nil {
		return
	}
	t := time.NewTicker(tickDur(castleTickInterval))
	defer t.Stop()
	for now := range t.C {
		s.castle.mgr.Run(castleCtx(), now)
		// 攻陷判定按原版放在玩家的 tick 里（ObjBase.pas:6536-6542）。
		s.castleCaptureTick(now)
		// 城墙/城门的石化状态随战期翻转（Castle.pas:694-721）。
		if cs, err := s.castleDefault(); err == nil {
			s.syncCastleStone(cs)
		}
	}
}

// castleCaptureTick 皇宫清场后自动攻陷（ObjBase.pas:6536-6542）。
//
// 原版条件：玩家在皇宫地图、是该行会成员、该行会是宣战方、
// CanGetCastle 通过（开战超 10 分钟 + 皇宫活人清一色），就换主；
// 换主后若 PalaceCount <= 1 则结束攻城。
func (s *Server) castleCaptureTick(now time.Time) {
	cs, err := s.castleDefault()
	if err != nil || !cs.UnderWar() {
		return
	}
	inPalace := s.castlePlayersInPalace(cs)
	if len(inPalace) == 0 {
		return
	}
	// 皇宫里所有活人的行会名（无行会为空串；原版按指针不等，nil 也算"不同"）。
	names := make([]string, 0, len(inPalace))
	for _, p := range inPalace {
		names = append(names, s.guildNameOf(p))
	}
	for _, p := range inPalace {
		name := s.guildNameOf(p)
		if name == "" {
			continue // 无行会：永远占不下城堡
		}
		if !cs.CanGetCastle(s.social.guilds, name, now, names) {
			continue
		}
		if _, err := s.castle.mgr.GetCastle(castleCtx(), cs.ConfigDir(), name, now); err != nil {
			log.Printf("攻陷换主失败: %v", err)
			return
		}
		log.Printf("%s 在皇宫清场后攻下了城堡", p.Char.Name)
		// 守方也在名单里，所以要等只剩一方才结束（ObjBase.pas:6541）。
		if cs.PalaceCount() <= 1 {
			cs.StopWar(s.castleHooks())
		}
		return
	}
}

// castlePlayersInPalace 返回皇宫地图内的在线玩家。
func (s *Server) castlePlayersInPalace(cs *castle.Castle) []*Player {
	palace := cs.Record().PalaceMap
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Player
	for _, p := range s.world.players {
		if p.Obj != nil && p.Obj.MapRef() != nil && p.Obj.MapRef().Name == palace {
			out = append(out, p)
		}
	}
	return out
}

// castleDefault 取默认城堡；未启用时返回错误。
func (s *Server) castleDefault() (*castle.Castle, error) {
	if s.castle.mgr == nil {
		return nil, castle.ErrNoCastle
	}
	return s.castle.mgr.Default()
}

// castleAt 返回该位置所属的城堡战区（无则 nil）。
func (s *Server) castleAt(p *Player) *castle.Castle {
	if s.castle.mgr == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return nil
	}
	return s.castle.mgr.InWarAreaOf(p.Obj.MapRef().Name, p.Obj.PosX(), p.Obj.PosY())
}

// guildNameOf 取玩家所在行会名（无行会返回空串）。
func (s *Server) guildNameOf(p *Player) string {
	if ref, ok := s.guildOf(p); ok {
		return ref.guild.Name
	}
	return ""
}

// ---------- 头衔 ----------

// castleTitleOf 返回玩家应显示的抬头行会串。
//
// 对应 ObjBase.pas:25875-25901：
//   - 占领方成员 → "[城堡名]行会名"（g_sCastleGuildName）
//   - 其余人     → "行会名"，或空（未入会）
func (s *Server) castleTitleOf(p *Player) string {
	ref, ok := s.guildOf(p)
	if !ok {
		return ""
	}
	if s.castle.mgr != nil {
		if cs := s.castle.mgr.IsCastleMember(ref.guild.Name); cs != nil {
			return "[" + cs.Name() + "]" + ref.guild.Name
		}
	}
	return ref.guild.Name
}

// refreshCastleTitles 给相关在线玩家重发 SM_CHANGEGUILDNAME。
//
// 对应原版 StartWallconquestWar 的 GetMapRageHuman + RefShowName
// （Castle.pas:848-862）与 GetCastle 的 RefMemberName ×2。
func (s *Server) refreshCastleTitles(cs *castle.Castle) {
	for _, name := range cs.Participants() {
		s.refreshGuildOnline(name)
	}
	if cs.OwnGuild() != "" {
		s.refreshGuildOnline(cs.OwnGuild())
	}
}

// ---------- 税收 ----------

// castleTax 在 NPC 交易里抽城堡税，返回实际抽取额（调用方从给玩家的
// 金额里扣掉）。
//
// 对应 IncRateGold 的四个调用点（ObjNpc.pas:1186 升级武器 / 1974 卖给 NPC
// / 2168 从 NPC 买 / 2465 修理）。
//
// ⚠️ 原版判定是 `if m_boCastle or g_Config.boGetAllNpcTax then`（四处都一样）：
// **这个商人属于城堡**（`m_boCastle`），或者开了全局开关（出厂 False）。
// `m_boCastle` 来自 NPC 定义里的"属沙城"列（merchant.txt 最后一列，见
// data/npc.go 的 Castle 字段），不是按地图推断 —— 所以我们直接读那个标记。
func (s *Server) castleTax(p *Player, amount int64) int64 {
	if s.castle.mgr == nil || amount <= 0 {
		return 0
	}
	// 当前对话的商人是不是城堡 NPC
	castleNPC := false
	if p != nil && p.dialog != nil {
		s.mu.RLock()
		if m := s.world.monsters[p.dialog.npcID]; m != nil {
			castleNPC = m.CastleNPC
		}
		s.mu.RUnlock()
	}
	if !castleNPC && !s.castle.config.taxAllNpc {
		return 0
	}
	got := s.castle.mgr.IncRateGold(castleCtx(), castle.DefaultConfigDir, amount)
	if got > 0 {
		name := ""
		if p != nil && p.Char != nil {
			name = p.Char.Name
		}
		// 原版没有这条日志；补它是为了 e2e 能断言"城堡 NPC 交易真的抽到税"。
		logpvp("城堡税收：%s 交易 %d 金币 → 抽 %d（城堡 NPC=%v，全局开关=%v）",
			name, amount, got, castleNPC, s.castle.config.taxAllNpc)
	}
	return got
}

// ---------- 城堡实体（城门/城墙/守卫/弓箭手）----------

// spawnCastleUnits 生成城堡上的所有单位。
//
// 对应原版 TCastle.Initialize（Castle.pas:220-301）：用 SabukW.txt 的
// 坐标与名字去怪物库 RegenMonsterByName，并把 HP 设成配置值。
// 已在 s.world.monsters 里的不重复生成（支持热重载时只补缺的）。
func (s *Server) spawnCastleUnits(cs *castle.Castle) {
	plans, err := cs.SpawnableUnits(s.data.tables.Monsters)
	if err != nil {
		logpvp("城堡单位模板缺失: %v", err)
	}
	if len(plans) == 0 {
		return
	}
	mp, err := s.world.maps.Get(cs.Record().MapName)
	if err != nil {
		logpvp("城堡战场地图 %s 加载失败: %v", cs.Record().MapName, err)
		return
	}

	// 已存在的（同castle单位）索引，避免重复。
	existing := make(map[[2]any]bool)
	s.mu.RLock()
	for _, m := range s.world.monsters {
		if m.IsCastleUnit() {
			existing[[2]any{m.CastleKind, m.CastleIdx}] = true
		}
	}
	s.mu.RUnlock()

	created := 0
	for _, plan := range plans {
		key := [2]any{plan.Kind, plan.Idx}
		if existing[key] {
			continue
		}
		info := s.data.tables.Monsters.GetByName(plan.Name)
		if info == nil {
			continue
		}
		x, y := plan.X, plan.Y
		// 坐标非法时就近修正，避免卡在墙里。
		if !mp.CanWalk(x, y) {
			nx, ny, ok := nearestWalkable(mp, x, y, 6)
			if !ok {
				logpvp("城堡单位 %s(%d,%d) 附近无可走格，跳过", plan.Name, x, y)
				continue
			}
			x, y = nx, ny
		}
		// ⚠️ ActorId 必须是 MonsterIDBase + 序列号，不能把偏移量加进序列号
		// （写成 s.world.monsterSeq.Add(1_000_000) 会让序列号直接跳 100 万，
		//  后续真实怪物 ID 撞进 NPC 区间 [2_000_000,)，客户端
		//  isMonsterID 判 false → 表现为"视野内没有怪物"）。
		m := entity.NewMonster(proto.MonsterIDBase+s.world.monsterSeq.Add(1), info, mp, x, y)
		m.CastleKind, m.CastleIdx = plan.Kind, plan.Idx
		// 城门/城墙的 HP 用配置值覆盖怪物库默认值（官方 10000/5000）。
		if plan.HP > 0 {
			m.HP, m.MaxHP = uint32(plan.HP), uint32(plan.HP)
		}
		// 城堡单位不游荡、不攻击、不掉落：靠 NPC 种族或显式标记区分。
		m.IsNPC = false
		m.NoCorpse = true // 尸体不回收（ObjMon2.pas:1069-1086）
		s.mu.Lock()
		s.world.monsters[m.ID] = m
		s.world.monsterIdx.Add(m)
		s.mu.Unlock()
		created++
	}
	if created > 0 {
		logpvp("城堡 %s 生成单位 %d 个（战场 %s）", cs.Name(), created, mp.Name)
		// 生成后要主动广播：站着的玩家视野里原本没有这些实体
		//（updateVision 只在玩家移动时触发）。
		s.broadcastCastleUnits(cs)
	}
}

// broadcastCastleUnits 把新生成的城堡单位推给视野内的玩家。
func (s *Server) broadcastCastleUnits(cs *castle.Castle) {
	s.mu.RLock()
	mp, mons, ps := s.world.defaultMap, s.world.monsters, s.world.players
	_ = mp
	s.mu.RUnlock()
	for _, p := range ps {
		if p.Obj == nil || p.Obj.MapRef() == nil {
			continue
		}
		for _, m := range mons {
			if !m.IsCastleUnit() || m.MapRef() != p.Obj.MapRef() || m.IsDead() {
				continue
			}
			if p.Obj.Distance(m.PosX(), m.PosY()) > s.cfg.viewRange {
				continue
			}
			p.visible.Add(m.ID)
			s.sendMonsterAppear(p, m)
		}
	}
}

// syncCastleStone 每轮切换城墙/城门的石化状态。
//
// 对应 Castle.pas:694-721：攻城期 m_boStoneMode := False（可被打），
// 非攻城期 := True（不可被打）。
func (s *Server) syncCastleStone(cs *castle.Castle) {
	want := !cs.UnderWar()
	s.mu.Lock()
	changed := false
	for _, m := range s.world.monsters {
		if !m.IsCastleUnit() {
			continue
		}
		// 只有**城墙**跟着攻城期石化（Castle.pas:694-721 的列表里只有三面墙）。
		//
		// ⚠️ **城门不能放这里**：门的石化由"开/关"决定（开门 ⇒ 石化 ⇒ 不可砍，
		// ObjMon2.pas:1039-1052），跟攻城期无关；早先一起按 `!UnderWar` 设置，
		// 结果是"非攻城期城门石化（打不动）" + "开门后照样能被砍"两个都反了。
		if m.CastleKind != storage.CastleWall {
			continue
		}
		if m.StoneMode != want {
			m.StoneMode = want
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		logpvp("城堡 %s 城墙石化 = %v（%s）", cs.Name(), want,
			map[bool]string{true: "非攻城期，不可打", false: "攻城期，可打"}[want])
	}
}

// canHitCastleUnit 判定玩家能否攻击城堡单位（ObjMon2.pas:828-883 的城堡分支）。
//
// 非攻城期：石化中不可打；守方/盟方也不该打自己的城墙（修门另走 NPC）。
// 攻城期：所有人可打。
func (s *Server) canHitCastleUnit(p *Player, m *entity.Monster) bool {
	cs, err := s.castleDefault()
	if err != nil {
		return false
	}
	underWar := cs.UnderWar()
	// ⚠️ 石化 = **不可被选为目标**（ObjBase.pas:21491-21492 `IsProperTarget` 的第一条）。
	// 早期这里返回 true（"放行，让流程走完打不动"），结果是搬起砖头砸自己的墙 ——
	// 石化期间攻击照样结算伤害。现在直接判"打不到"，并在日志里留痕（e2e 查它）。
	if m.StoneMode {
		logpvp("%s 攻击 %s 失败：目标石化中（城门=%v 攻城期=%v）",
			p.Char.Name, m.Name, m.CastleKind == storage.CastleMainDoor, underWar)
		return false
	}
	own, ally := false, false
	name := s.guildNameOf(p)
	if name != "" && s.castle.mgr != nil {
		own = cs.IsMasterGuild(name)
		if !own {
			ally = cs.IsDefenseAllyGuild(s.social.guilds, name)
		}
	}
	// 攻城期一律可打；非攻城期只有守方能打（用于修门/修墙的判定入口）。
	return underWar || own || ally
}

// ---------- 修门/修墙/雇佣 ----------

// castleUnitByKind 在实体表里找某个城堡单位。
func (s *Server) castleUnitByKind(kind storage.CastleUnitKind, idx int) *entity.Monster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.world.monsters {
		if m.IsCastleUnit() && m.CastleKind == kind && m.CastleIdx == idx {
			return m
		}
	}
	return nil
}

// handleCastleRepair 处理 NPC 的修门/修墙/雇佣标签。
//
// kind 之外的编号放在 arg 里：墙 1..3 对应左/中/右（原版 RepairWall
// 的 1=Left 2=Center 3=Right，Castle.pas:1180），守卫/弓手 1..N。
func (s *Server) handleCastleRepair(c net.Conn, p *Player, kind storage.CastleUnitKind, arg string) {
	cs, err := s.castleDefault()
	if err != nil {
		s.sysMsg(c, "本服未开放城堡")
		return
	}
	idx, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		s.sysMsg(c, "格式不对："+cs.RepairString(kind, 0)+"要指定编号")
		return
	}
	// 原版编号从 1 起（RepairWall(1)=左墙），内部存储从 0 起。
	idx--
	if idx < 0 {
		s.sysMsg(c, "编号从 1 开始")
		return
	}
	if kind == storage.CastleGuard && idx >= castle.MaxGuard {
		s.sysMsg(c, fmt.Sprintf("守卫最多 %d 名", castle.MaxGuard))
		return
	}
	if kind == storage.CastleArcher && idx >= castle.MaxArcher {
		s.sysMsg(c, fmt.Sprintf("弓箭手最多 %d 名", castle.MaxArcher))
		return
	}

	now := time.Now()
	var (
		unitAlive  bool
		unitHP     int
		unitMaxHP  int
		lastStruck time.Time
	)
	// 修缮类必须有实体；雇佣类实体不存在即"未雇佣"。
	if m := s.castleUnitByKind(kind, idx); m != nil {
		unitAlive, unitHP, unitMaxHP = !m.IsDead(), int(m.HP), int(m.MaxHP)
		lastStruck = m.LastStruckAt()
	}
	d := p.Char.Data
	if d == nil {
		return
	}
	gold := p.gold() // 持锁快照：下面的价格判定用
	code, cost := cs.RepairUnit(kind, idx, unitHP, unitMaxHP, unitAlive, lastStruck, now, gold)
	if code != castle.RepairOK {
		s.sysMsg(c, castle.RepairErrText(code))
		return
	}
	// 扣钱（原价由 NPC 侧校验，成功才扣，OpenMir2 CastleOfficial.cs:358-406）。
	// ⚠️ 原子扣款：`RepairUnit` 用的是余额**快照**，真扣时可能已经不够了。
	if !p.spendGold(cost) {
		s.sysMsg(c, "金币不足")
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")

	if kind == storage.CastleGuard || kind == storage.CastleArcher {
		s.hireCastleUnit(c, cs, kind, idx)
	} else {
		s.repairCastleUnit(c, p, kind, idx, now)
	}
	logpvp("%s %s %s，花费 %d 金币", p.Char.Name,
		map[bool]string{true: "修理", false: "雇佣"}[kind == storage.CastleMainDoor || kind == storage.CastleWall],
		cs.RepairString(kind, idx), cost)
}

// repairCastleUnit 执行修门/修墙的实体操作。
func (s *Server) repairCastleUnit(c net.Conn, p *Player, kind storage.CastleUnitKind, idx int, now time.Time) {
	m := s.castleUnitByKind(kind, idx)
	if m == nil {
		s.sysMsg(c, "该位置没有可修理的部分")
		return
	}
	s.mu.Lock()
	fixed := m.Repair()
	fixedDir := fixed && m.StoneMode
	s.mu.Unlock()
	if !fixed {
		s.sysMsg(c, "目前无需修理")
		return
	}
	// 修好后如果处于石化期就解除，好让玩家立刻能看到门开了。
	if fixedDir {
		s.mu.Lock()
		m.StoneMode = false
		s.mu.Unlock()
	}
	// ⚠️ **城门**修好还必须"重新立起来"：原版 RepairDoor（Castle.pas:1170-1179）在
	// "已摧毁"分支里显式做三件事 —— 补满 HP、`m_boDeath := False`、**`m_boOpened := False`**
	//（关门），然后 `RefStatus`。而 `TCastleDoor.Close` 会 `SetMapXYFlag(1)` 把整片门格
	// 重新设成阻挡（ObjMon2.pas:1050-1060）。
	// 少了关门/重设格这步，门虽然"修好"了、地图格却还停在 nFlag=2（门破时设的可通行）
	// ⇒ 表现是"修完了照样能穿门而过"。
	if m.CastleKind == storage.CastleMainDoor {
		s.mu.Lock()
		m.DoorOpened = false
		m.StoneMode = false
		s.mu.Unlock()
		s.applyCastleDoorCells(m.MapRef(), m.PosX(), m.PosY(), 1) // 1 = 关：整片门格阻挡
		if cs, err := s.castleDefault(); err == nil {
			cs.SetDoorOpened(false) // 存档里的开门状态也跟着回退
		}
		logpvp("城门修缮完成：已重新关闭并封住门格（%d,%d）", m.PosX(), m.PosY())
	}
	s.broadcastCastleUnitRefresh(m)
	s.sysMsg(c, "修理完成")
}

// hireCastleUnit 雇佣守卫/弓箭手：标记已雇佣并生成实体。
func (s *Server) hireCastleUnit(c net.Conn, cs *castle.Castle, kind storage.CastleUnitKind, idx int) {
	// 先写回配置：SpawnableUnits 只放行 HP>0 的雇佣单位。
	if !cs.HireUnit(kind, idx, 9999) {
		s.sysMsg(c, "没有这个位置")
		return
	}
	s.respawnCastleUnits(cs)
	s.sysMsg(c, "雇佣成功")
}

// respawnCastleUnits 补生成缺失的城堡单位（雇佣后调用）。
func (s *Server) respawnCastleUnits(cs *castle.Castle) { s.spawnCastleUnits(cs) }

// broadcastCastleUnitRefresh 让视野内的人看到修好后的样子。
//
// 对应原版 RefStatus：城门/墙的外观方向按剩余血量百分比变化
// （TCastleDoor.RefStatus，ObjMon2.pas:1088-1096：满血=0，两/三=1，一/三=2）。
func (s *Server) broadcastCastleUnitRefresh(m *entity.Monster) {
	if m == nil {
		return
	}
	ratio := 0.0
	if m.MaxHP > 0 {
		ratio = float64(m.HP) / float64(m.MaxHP)
	}
	// 0=满 1=2/3 2=1/3 3/4=破。钳到 0..4，超出当 0（与原版一致）。
	// ⚠️ 朝向经 `SetFacing`（Object 自带锁，见 entity/object.go 的并发说明）。
	switch {
	case ratio > 0.66:
		m.SetFacing(0)
	case ratio > 0.33:
		m.SetFacing(1)
	case ratio > 0.0:
		m.SetFacing(2)
	default:
		m.SetFacing(3)
	}
	s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(o *Player) {
		if o.visible.Contains(m.ID) {
			s.send(o.conn, proto.SM_TURN, int32(m.ID), uint16(m.PosX()), uint16(m.PosY()),
				uint16(m.Facing()), "")
		}
	})
}

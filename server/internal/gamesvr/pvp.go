package gamesvr

// PvP 与 PK 惩罚（P6）。
//
// 行为对照（Delphi ObjBase.pas）：
//
//	攻击判定  IsProperTarget(21495) = IsAttackTarget(21332) + IsProtectTarget(21258)
//	          玩家 vs 玩家时 IsProtectTarget 的结论**覆盖**前者（21502）
//	PK 罪     PKLevel = m_nPkPoint div 100（2234）；杀人 +100（20945）
//	          每 120 秒衰减 1 点（4042-4047）
//	正当防卫  SetPKFlag(21220) 被打后 60 秒；IsGoodKilling(21251)
//	免罪      guildwarkill(20905-20914)：行会战 / 攻城战 / 自由 PK 区
//	客户端    SM_AREASTATE(766) 区状态位；SM_CHANGENAMECOLOR(656) 刷名字颜色
//
// # 与 OpenMir2 的差异（OpenMir2 有 bug，以 Delphi 为准）
//
//  1. IsProtectTarget 是**赋值**不是提前 return（21502）。OpenMir2
//     PlayObject.cs:1936 写成了 return。
//  2. guildwarkill 的两段判断是**两个独立 if**（20906 与 20912），不是 if/else。
//     OpenMir2 写成 else，会导致"双方都有行会 + 攻城战中"误加 PK 罪。
//  3. 行会关系**实时计算**，不读 m_boGuildWarArea（那是个陈旧标记，详见
//     internal/pvp 的包注释）。
//
// # 原版默认关闭的（这里也保持关闭）
//
//   - PKDie 的等级/经验奖惩（boKillHumanWinLevel 等 4 个开关，出厂全 False）
//   - boPKLevelProtect（新人保护，出厂 False）

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/magic"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/pvp"
	"github.com/algotao/mir2/server/internal/world"
)

// SM_AREASTATE 的位掩码（ObjBase.pas:4471-4474）。
const (
	// areaFightZone 是 FIGHT 区标记。
	areaFightZone = 1 << 0
	// areaSafe 是地图安全区标记。
	areaSafe = 1 << 1
	// areaFreePK 是自由 PK 区/攻城战区标记（m_boInFreePKArea）。
	areaFreePK = 1 << 2
)

// 名字颜色（g_Config，M2Share.pas:2039-2044）。
const (
	colorPKFlag    = 0x2F // 被打后的正当防卫标记
	colorPKLevel1  = 0xFB // 浅红（1 级）
	colorPKLevel2  = 0xF9 // 红名（2 级以上）
	colorAllyGuild = 0xB4 // 同会/同盟
	colorWarGuild  = 0x45 // 敌对行会
	colorFreePK    = 0xDD // 自由 PK 区
)

// pvpConfig 是 PvP 玩法配置。
type pvpConfig struct {
	// cfg 是 internal/pvp 的判定与 PK 点参数。
	cfg pvp.Config
	// zones 是地图号 → 区域属性（来自 mapinfo.txt）。
	zones map[string]*pvp.ZoneMap
	// spawns 是出生点（安全区判定用，ObjBase.pas:21545-21554）。
	spawns []pvp.SpawnPoint
	// nonPKServer 对应 boNonPKServer（默认 False）：无视种族全都能打。
	nonPKServer bool
	// decTick 是每个在线角色上次做 PK 点衰减的时刻。
	decTick map[string]time.Time
}

// defaultPVPConfig 返回出厂配置。
func defaultPVPConfig() pvpConfig {
	return pvpConfig{
		cfg:     pvp.DefaultConfig(),
		zones:   make(map[string]*pvp.ZoneMap),
		decTick: make(map[string]time.Time),
	}
}

// initPVP 从 mapinfo.txt 建区域属性索引。
func (s *Server) initPVP(infos []*data.MapInfo, spawns []*data.StartPoint) {
	zones := make(map[string]*pvp.ZoneMap, len(infos))
	for _, mi := range infos {
		zones[mi.ID] = &pvp.ZoneMap{
			Name:       mi.ID,
			Safe:       mi.Safe,
			FightZone:  mi.FightZone,
			Fight3Zone: mi.Fight3Zone,
			Quiz:       mi.Quiz,
		}
	}
	sp := make([]pvp.SpawnPoint, 0, len(spawns))
	for _, p := range spawns {
		sp = append(sp, pvp.SpawnPoint{MapID: p.MapID, X: p.X, Y: p.Y})
	}
	s.pvp.cfg.zones = zones
	s.pvp.cfg.spawns = sp
	safe := 0
	for _, z := range zones {
		if z.Safe {
			safe++
		}
	}
	logpvp("区域属性: %d 张图，其中 %d 张 SAFE；出生点 %d 个", len(zones), safe, len(sp))
}

// zoneOf 返回地图的区域属性（没有记录时返回 nil，InSafeZone 会当成不安全）。
func (s *Server) zoneOf(m *world.Map) *pvp.ZoneMap {
	if m == nil {
		return nil
	}
	return s.pvp.cfg.zones[m.Name]
}

// safeZoneAt 判定某点是否在安全区（ObjBase.pas:21527-21562）。
func (s *Server) safeZoneAt(m *world.Map, x, y int) bool {
	return pvp.InSafeZone(s.pvp.cfg.cfg, s.zoneOf(m), x, y, s.pvp.cfg.spawns)
}

// actorOf 把在线玩家转成判定用的 Actor。
func (s *Server) actorOf(p *Player, now time.Time) *pvp.Actor {
	if p == nil || p.Char == nil || p.Char.Data == nil || p.Obj == nil {
		return nil
	}
	a := &pvp.Actor{
		ID:           p.Obj.ID,
		Race:         pvp.RacePlay,
		Level:        p.level(),
		GuildName:    s.guildNameOf(p),
		PkPoint:      int32(p.Char.Data.PkPoint),
		InFreePKArea: p.inFreePKArea,
		PvpFlag:      p.pvpFlag,
		PvpFlagUntil: p.pvpFlagUntil,
		LastMoveAt:   p.lastMoveAt,
	}
	if p.Obj.MapRef() != nil {
		a.InSafeZone = s.safeZoneAt(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY())
	}
	return a
}

// attackModeOf 读玩家的攻击模式（存档字段 AttackMode，原版 btAttatckMode）。
func attackModeOf(p *Player) pvp.AttackMode {
	if p.Char == nil || p.Char.Data == nil {
		return pvp.HamAll
	}
	return pvp.AttackMode(p.Char.Data.AttackMode)
}

// ---------- 判定入口 ----------

// canAttackTarget 是攻击玩家前的统一判定（IsProperTarget 的接线）。
//
// attacker/attackerMode 是攻击者，target 是 prospective 目标（玩家）。
// 返回 false 时**不能**发 SM_STRUCK、也不能扣血。
func (s *Server) canAttackTarget(attacker, target *Player, mode pvp.AttackMode, now time.Time) bool {
	a := s.actorOf(attacker, now)
	t := s.actorOf(target, now)
	if a == nil || t == nil {
		return false
	}
	// SameGroup 必须填在**目标**身上：`isAttackTargetPlayer` 的 HamGroup 分支
	// 读的是 `target.SameGroup`，而原版写的是 `IsGroupMember(BaseObject)`
	// ——问的是"目标**是不是我的队友**"（ObjBase.pas:21445-21446）。
	// ⚠️ 判定是**单向**的：A 打 B 时 B 被免伤，但 B 想打 A 照样能打
	//（只要 B 不是编组模式）。
	t.SameGroup = s.inSameGroup(attacker, target)

	// 行会关系实时算（不读任何缓存标记，见 internal/pvp 包注释）。
	ag := s.social.guilds.Find(a.GuildName)
	tg := s.social.guilds.Find(t.GuildName)
	same, ally, _ := pvp.Relations(ag, tg, a.InSafeZone)
	return pvp.IsProperTarget(s.pvp.cfg.cfg, a, t, mode, same, ally, now)
}

// ---------- 名字颜色 ----------

// nameColorOf 计算 target 在 viewer 视角下的头顶名字颜色
// （GetCharColor，ObjBase.pas:19091-19190）。
//
// 优先级：GetNamecolor（红名/浅红）→ PK 标记 → 行会关系 → 自由 PK 区。
// ⚠️ 只有**非红名**才会被后面的规则覆盖（19102）。
func (s *Server) nameColorOf(target, viewer *Player, now time.Time) uint8 {
	a := s.actorOf(target, now)
	if a == nil {
		return 0
	}
	// 起点 GetNamecolor（ObjBase.pas:19197-19202）：红名/浅红。
	color := uint8(0)
	switch {
	case pvp.PKLevel(a.PkPoint) >= pvp.RedNameLevel:
		color = colorPKLevel2
	case pvp.PKLevel(a.PkPoint) == 1:
		color = colorPKLevel1
	default:
		// 白名，继续往下走关系色。
		if a.PvpFlag {
			return colorPKFlag
		}
		if v := s.actorOf(viewer, now); v != nil {
			ag := s.social.guilds.Find(v.GuildName)
			bg := s.social.guilds.Find(a.GuildName)
			same, ally, war := pvp.Relations(ag, bg, v.InSafeZone)
			switch {
			case war:
				return colorWarGuild
			case same || ally:
				return colorAllyGuild
			}
			// 自由 PK 区 / 攻城战：双方都在区内才用区颜色
			//（ObjBase.pas:19121-19124）。
			if a.InFreePKArea && v.InFreePKArea {
				return colorFreePK
			}
		}
		return color
	}
	// 红名不被后续规则覆盖（ObjBase.pas:19102）
	return color
}

// sendNameColor 刷新 target 在**自己视野内各人视角下**的名字颜色。
//
// 对应 RefNameColor（ObjBase.pas:2263-2266）发 SM_CHANGENAMECOLOR(656)，
// 客户端随后重新拉 SM_USERNAME(42)——颜色走在 **Param** 字段里。
func (s *Server) sendNameColor(target *Player, now time.Time) {
	s.sendNameColorIf(target, now, true)
}

// sendNameColorIf 在 needSend 为真、或颜色确实变化时才发。
//
// 幂等很重要：它由 pvpLoop 每轮调用，20 倍速回归时是 50ms 一轮，
// 无条件重发会淹掉协议流。
func (s *Server) sendNameColorIf(target *Player, now time.Time, force bool) {
	if target == nil || target.Obj == nil || target.Char == nil || target.Char.Data == nil {
		return
	}
	// 以"自己看自己"为基准缓存颜色；无行会关系时这就是最终颜色。
	self := s.nameColorOf(target, target, now)
	if !force && self == target.nameColorSent {
		return
	}
	target.nameColorSent = self
	// 自己也收到一次，用于刷新自己的界面配色。
	s.send(target.conn, proto.SM_CHANGENAMECOLOR, 0, 0, 0, 0, "")

	s.mu.RLock()
	others := make([]*Player, 0, len(s.world.players))
	for _, o := range s.world.players {
		others = append(others, o)
	}
	s.mu.RUnlock()

	for _, o := range others {
		if o == target || o.Obj == nil || o.Obj.MapRef() != target.Obj.MapRef() {
			continue
		}
		if !o.visible.Contains(target.Obj.ID) {
			continue
		}
		color := s.nameColorOf(target, o, now)
		// ⚠️ 字段位置照客户端：颜色在 **Param**（`GetRGB(msg.Param)`，ClMain.pas:4352），
		// Tag/Series 留 0，名字走包体（原版 `MakeDefaultMsg(SM_USERNAME, obj,
		// GetCharColor(obj), 0, 0)`，ObjBase.pas:5571-5574）。
		// 我们原来是 X/Y 塞 Param/Tag、颜色塞 Series ⇒ 真客户端拿**坐标当颜色**
		//（名字颜色错乱，还会随移动变化）。2026-10-06 修。
		s.send(o.conn, proto.SM_USERNAME, int32(target.Obj.ID),
			uint16(color), 0, 0, target.Char.Name)
	}
}

// sendAreaState 下发区域状态位（RefUserState，ObjBase.pas:4467-4476）。
//
// **只在位掩码变化时真发**（幂等）：它由 pvpLoop 每轮调用，而 pvpLoop 跑在
// tickDur(1s) 上——20 倍速回归时是 50ms 一轮，无条件发包会把协议流淹掉。
func (s *Server) sendAreaState(to *Player) {
	if to == nil || to.Obj == nil || to.Obj.MapRef() == nil {
		return
	}
	bits := s.areaStateOf(to)
	if bits == to.areaStateBits {
		return
	}
	to.areaStateBits = bits
	s.send(to.conn, proto.SM_AREASTATE, int32(bits), 0, 0, 0, "")
}

// areaStateOf 计算区域状态位（不发送）。
func (s *Server) areaStateOf(p *Player) uint32 {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return 0
	}
	var bits uint32
	if z := s.zoneOf(p.Obj.MapRef()); z != nil {
		if z.FightZone {
			bits |= areaFightZone
		}
		// ⚠️ 这里只翻 mapinfo 的 SAFE 位。出生点/红名区也是安全区，但客户端
		// 只认这个位；"打不到人"由 IsProtectTarget 单独保证，不靠图标。
		if z.Safe {
			bits |= areaSafe
		}
	}
	if p.inFreePKArea {
		bits |= areaFreePK
	}
	return bits
}

// ---------- 循环 ----------

// pvpLoop 每秒推进 PK 状态。
//
// 对应原版的三处周期任务：
//   - PK 点衰减（ObjBase.pas:4042-4047）：每 dwDecPkPointTime 减 nDecPkPointCount
//   - 正当防卫标记到期（CheckPKStatus，18868-18875）：dwPKFlagTime 后自动清
//   - 攻城战区的 m_boInFreePKArea 开关（ChangePKStatus，6470-6475 / 6528-6549）
func (s *Server) pvpLoop() {
	t := time.NewTicker(tickDur(time.Second))
	defer t.Stop()
	for now := range t.C {
		s.pvpTick(now)
	}
}

func (s *Server) pvpTick(now time.Time) {
	// ① 组队名册清理（ObjBase.pas:4113-4132 的心跳段）：
	//    组长死亡/幽灵 ⇒ 整队解散；成员死亡 ⇒ 从名单里摘掉。
	//    放在这里是因为它与"下线/死亡退组"互为兜底 —— 那两处挂在
	//    removePlayer/revive 上，但原版还有一条心跳兜底路径。
	s.groupSweep()
	// 交易守卫：不再面对对方就取消（ObjBase.pas:6413-6416）。
	// 这是原版**唯一**的"超时"来源——没有超时计时器，走一格/转身/换图即取消。
	s.dealGuard()

	s.mu.RLock()
	ps := make([]*Player, 0, len(s.world.players))
	for _, p := range s.world.players {
		ps = append(ps, p)
	}
	s.mu.RUnlock()

	for _, p := range ps {
		if p.Char == nil || p.Char.Data == nil {
			continue
		}
		// ③ PK 点衰减
		if s.pvp.cfg.cfg.DecPointInterval > 0 {
			last := s.pvpDecTickOf(p.Char.Name, now)
			if now.Sub(last) >= s.pvp.cfg.cfg.DecPointInterval {
				s.pvpSetDecTick(p.Char.Name, now)
				pt := int32(p.Char.Data.PkPoint)
				if pt > 0 && pvp.DecPKPoint(&pt, s.pvp.cfg.cfg.DecPointCount) {
					p.Char.Data.PkPoint = int64(pt)
					s.sendNameColor(p, now)
				}
			}
		}
		// ② 正当防卫标记到期
		if p.pvpFlag && !now.Before(p.pvpFlagUntil) {
			p.pvpFlag = false
			s.sendNameColor(p, now)
		}
		// ③ 攻城战自由 PK 区（变化时才发）
		s.syncFreePKArea(p)
		// ④ 区状态与名字颜色做幂等补发：只有真的变了才插包
		s.sendAreaState(p)
		s.sendNameColorIf(p, now, false)
	}
}

// syncFreePKArea 按"是否在攻城战区且正在攻城"同步 m_boInFreePKArea。
//
// 对应 ObjBase.pas:6470-6475（开）与 6528-6549（关）。
// ⚠️ 原版这两处在同一段代码里、每帧无条件跑，等于**每帧重算**；
// 我们每秒算一次，语义相同而开销小得多。
func (s *Server) syncFreePKArea(p *Player) {
	want := false
	if cs := s.castleAt(p); cs != nil && cs.UnderWar() {
		want = true
	}
	if p.inFreePKArea == want {
		return
	}
	p.inFreePKArea = want
	// 变化会影响名字颜色与免罪，两个都要刷。
	s.sendNameColor(p, time.Now())
	s.sendAreaState(p)
}

func (s *Server) pvpDecTickOf(name string, now time.Time) time.Time {
	s.pvp.mu.Lock()
	defer s.pvp.mu.Unlock()
	t, ok := s.pvp.cfg.decTick[name]
	if !ok {
		// 首次见到：把起点定在现在，等一个完整周期后再减。
		s.pvp.cfg.decTick[name] = now
		return now
	}
	return t
}

func (s *Server) pvpSetDecTick(name string, now time.Time) {
	s.pvp.mu.Lock()
	s.pvp.cfg.decTick[name] = now
	s.pvp.mu.Unlock()
}

// ---------- 死亡结算 ----------

// pvpKillResult 记录一次击杀的判定结果，供日志与提示使用。
type pvpKillResult struct {
	// GuildWarKill 为真表示本次击杀免罪（行会战 / 攻城战 / 自由 PK 区）。
	GuildWarKill bool
	// GoodKilling 为真表示是正当防卫（对方刚打过自己）。
	GoodKilling bool
	// AddPKPoint 是给凶手加的 PK 点（0 = 不加）。
	AddPKPoint int32
}

// judgeKill 判定一次玩家击杀的 PK 罪（ObjBase.pas:20903-20954）。
//
// ⚠️ 两个独立 if，不是 if/else（20906 与 20912）——OpenMir2 在这里
// 写成了 else，会让"双方都有行会 + 攻城战中"误加 PK 罪。
func (s *Server) judgeKill(victim, killer *Player, now time.Time) pvpKillResult {
	res := pvpKillResult{}
	v := s.actorOf(victim, now)
	k := s.actorOf(killer, now)
	if v == nil || k == nil {
		return res
	}
	// boPK 判定（ObjBase.pas:20887）：红名被杀不判 PK 罪。
	//   (Race==Play) and (LastHiter<>nil) and (PKLevel < 2)
	if pvp.PKLevel(v.PkPoint) >= pvp.RedNameLevel {
		return res
	}
	// ① 行会战：双方都有行会且互相宣战
	if v.GuildName != "" && k.GuildName != "" {
		vg := s.social.guilds.Find(v.GuildName)
		kg := s.social.guilds.Find(k.GuildName)
		_, _, war := pvp.Relations(vg, kg, v.InSafeZone)
		if war {
			res.GuildWarKill = true
		}
	}
	// ② 攻城战 / 自由 PK 区（独立判断）
	if cs := s.castleAt(victim); cs != nil && cs.UnderWar() {
		res.GuildWarKill = true
	}
	if v.InFreePKArea {
		res.GuildWarKill = true
	}
	if res.GuildWarKill {
		return res
	}
	// ③ 正当防卫（ObjBase.pas:20943-20953）
	if pvp.IsGoodKilling(v) {
		res.GoodKilling = true
		return res
	}
	res.AddPKPoint = s.pvp.cfg.cfg.KillAddPKPoint
	return res
}

// addPKPoint 给玩家加 PK 点并按需刷颜色（IncPkPoint，ObjBase.pas:2362-2372）。
func (s *Server) addPKPoint(p *Player, n int32) {
	if p == nil || p.Char == nil || p.Char.Data == nil || n == 0 {
		return
	}
	pt := int32(p.Char.Data.PkPoint)
	if !pvp.IncPKPoint(&pt, n, s.pvp.cfg.cfg.MaxPKPoint) {
		return
	}
	p.Char.Data.PkPoint = int64(pt)
	now := time.Now()
	s.sendNameColor(p, now)
	s.sysMsg(p.conn, "你犯了谋杀罪！！！")
}

// setPKFlag 给被打者打上正当防卫标记（SetPKFlag，ObjBase.pas:21220-21236）。
func (s *Server) setPKFlag(attacker, victim *Player, now time.Time) {
	if victim == nil || attacker == nil {
		return
	}
	a := s.actorOf(attacker, now)
	v := s.actorOf(victim, now)
	if a == nil || v == nil {
		return
	}
	z := (*pvp.ZoneMap)(nil)
	if victim.Obj != nil && victim.Obj.MapRef() != nil {
		z = s.zoneOf(victim.Obj.MapRef())
	}
	inFight := z != nil && z.FightZone
	if !pvp.SetPKFlag(a, v, inFight, v.PvpFlag) {
		return
	}
	victim.pvpFlag = true
	victim.pvpFlagUntil = now.Add(s.pvp.cfg.cfg.PKFlagTime)
	s.sendNameColor(victim, now)
}

// ---------- 命令 ----------

// cmdAttackMode 切换攻击模式（CmdChangeAttackMode，ObjBase.pas:10679-10707）。
//
// 客户端是靠点按钮切换的，我们没有那条 C→S 消息，用聊天框命令代替。
// 切换后立刻刷新名字颜色（同会成员/同盟色会随之变化）。
func (s *Server) cmdAttackMode(c net.Conn, p *Player, args []string) {
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	next := pvp.HamAll
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 || v > int(pvp.HamPKAttack) {
			s.sysMsg(c, "用法: @atkmode [0-4]（0 全体 / 1 和平 / 2 编组 / 3 行会 / 4 只打红名）")
			return
		}
		next = pvp.AttackMode(v)
	} else {
		next = (attackModeOf(p) + 1) % 5
	}
	p.Char.Data.AttackMode = uint32(next)
	names := [...]string{"全体攻击", "和平模式", "编组攻击", "行会攻击", "只打红名"}
	s.sysMsg(c, "攻击模式："+names[next])
	// 模式影响"看到的人的颜色"（同会免打标记），刷一次视野。
	now := time.Now()
	s.mu.RLock()
	others := make([]*Player, 0, len(s.world.players))
	for _, o := range s.world.players {
		others = append(others, o)
	}
	s.mu.RUnlock()
	for _, o := range others {
		if o != p && o.Obj != nil && o.Obj.MapRef() == p.Obj.MapRef() && o.visible.Contains(p.Obj.ID) {
			s.send(o.conn, proto.SM_USERNAME, int32(p.Obj.ID),
				uint16(s.nameColorOf(p, o, now)), 0, 0, p.Char.Name)
		}
	}
}

// cmdPKPoint 是 GM 命令：查看/设置 PK 点。
func (s *Server) cmdPKPoint(c net.Conn, p *Player, args []string) {
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	if len(args) == 0 {
		pt := int32(p.Char.Data.PkPoint)
		color := "白名"
		if pvp.PKLevel(pt) >= pvp.RedNameLevel {
			color = "红名"
		} else if pvp.PKLevel(pt) == 1 {
			color = "浅红"
		}
		s.sysMsg(c, fmt.Sprintf("PK 点 %d（%d 级，%s）%s", pt, pvp.PKLevel(pt), color,
			map[bool]string{true: "，正当防卫标记中", false: ""}[p.pvpFlag]))
		return
	}
	v, err := strconv.Atoi(args[0])
	if err != nil || v < 0 {
		s.sysMsg(c, "用法: @pk [点数]")
		return
	}
	if int32(v) > s.pvp.cfg.cfg.MaxPKPoint {
		v = int(s.pvp.cfg.cfg.MaxPKPoint)
	}
	p.Char.Data.PkPoint = int64(v)
	now := time.Now()
	s.sendNameColor(p, now)
	s.sysMsg(c, fmt.Sprintf("PK 点设为 %d（%d 级）", v, pvp.PKLevel(int32(v))))
}

// logpvp 打一条带统一前缀的日志。
func logpvp(format string, args ...any) {
	log.Printf("PvP: "+format, args...)
}

// ---------- 攻击接线 ----------

// playerAt 按坐标找在线玩家（与 monsterAt 同构，排除自己）。
func (s *Server) playerAt(m *world.Map, x, y int) *Player {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.world.players {
		if p.Obj != nil && p.Obj.MapRef() == m && p.Obj.PosX() == x && p.Obj.PosY() == y {
			return p
		}
	}
	return nil
}

// attackPlayer 处理攻击玩家（IsProperTarget 通过后的伤害与表现）。
//
// 对应 Delphi DirectAttack（ObjBase.pas:21969-21991）：
// 玩家 vs 玩家时**跳过**"双方都在安全区就不打"的判断（21974），
// 因为安全区已经在 IsProtectTarget 里整体拦掉了。
func (s *Server) attackPlayer(c net.Conn, attacker, victim *Player, dir uint8, bonus warBonus) {
	now := time.Now()
	mode := attackModeOf(attacker)
	if !s.canAttackTarget(attacker, victim, mode, now) {
		logpvp("%s 攻击 %s 被拒（模式=%d，安全区=%v/%v，PK点 %d/%d，等级 %d/%d）",
			attacker.Char.Name, victim.Char.Name, mode,
			s.actorInSafe(attacker), s.actorInSafe(victim),
			attacker.Char.Data.PkPoint, victim.Char.Data.PkPoint,
			attacker.level(), victim.level())
		return
	}

	// 伤害：与打怪同一条路径（先掷未减防的威力 → 叠加充能加成 → 再减防），
	// AC 是 MinMax 打包（Grobal2.pas:736）。
	//
	// ⚠️ 充能加成（攻杀/烈火）在 `_Attack` 里对**任何**目标都生效，所以
	// 这里必须接 bonus —— 由 handleAttack 消费后传下来。
	minAtk, maxAtk := s.attackPower(attacker)
	// 打空判定也在 `_Attack` 里 ⇒ 玩家互砍一样会 miss（命中 vs 对方敏捷）。
	power, missed := s.rollMelee(s.playerHitPoint(attacker), s.playerHitPoint(victim),
		s.playerSpeedPoint(victim), s.playerLuck(attacker), minAtk, maxAtk, bonus)
	if missed {
		logpvp("%s 攻击 %s 打空（命中 %d < Random(敏捷 %d)）",
			attacker.Char.Name, victim.Char.Name,
			s.playerHitPoint(attacker), s.playerSpeedPoint(victim))
		return
	}
	ac := abilityFromPB(victim.Char.Data.Abil).AC
	dmg := applyArmor(power, ac)
	if !bonus.isZero() {
		logpvp("%s 的%s命中 %s：威力 %d → 伤害 %d（HP %d/%d）",
			attacker.Char.Name, bonus.name(), victim.Char.Name, power, dmg,
			victim.hp(), victim.maxHP())
	}

	// 武器耐久损耗（与打怪一致）
	s.wearWeapon(attacker)

	// 被打者获得正当防卫标记（SetPKFlag）
	s.setPKFlag(attacker, victim, now)

	s.damagePlayer(c, attacker, victim, dmg, now)
	// 麻痹戒指对**玩家**同样生效：原版 `_Attack` 里紧跟伤害之后判
	//（ObjBase.pas:22265），且裹在 `if nPower > 0 then` 里 ⇒ 只有打中才判。
	// ⚠️ 只挂在**近战**路径：原版的判定写在 `_Attack`，法术走的是另一条路
	//（`RM_MAGSTRUCK` 那段没有这个判定）⇒ 法术打人不麻痹。
	if dmg > 0 {
		s.paralysisPlayerOnHit(attacker, victim)
	}
}

// damagePlayer 把**已经算好**的近战伤害打到一个玩家身上（受击广播 + 死亡分流）。
//
// 从 attackPlayer 里抽出来，给"伤害公式不同、落点却相同"的技能复用
// （野蛮冲撞(27) 的 `DoMotaebo` 末尾就是自己算伤害再 `StruckDamage`）。
func (s *Server) damagePlayer(c net.Conn, attacker, victim *Player, dmg uint32, now time.Time) {
	if victim.Char == nil || victim.Char.Data == nil || victim.Char.Data.Abil == nil {
		return
	}
	// 红毒：被打的人受伤放大（原版 StruckDamage 在受击方）。PvP 与野蛮冲撞共用这里。
	dmg = s.struckPlayer(victim, dmg, now)

	// ⚠️ "够不够扣 + 扣多少"必须在**同一次持锁**里做（`Player.stateMu`，见 statelock.go）：
	// 挨打是**别人算的** —— 受害者自己的 goroutine 同时在吃药/放技能，别的攻击者
	// 也可能同时打他。原来这里 `if dmg > abil.Hp {…}; abil.Hp -= dmg` 分开写，
	// 两次伤害会互相覆盖（"打了两下只掉一下"）。
	hp, dmg := victim.hurt(dmg)
	maxHP := victim.maxHP()

	// 受击者自己也要收到 SM_STRUCK（他的 visible 集合不含自己）。
	s.broadcastToViewers(victim.Obj.MapRef(), victim.Obj.PosX(), victim.Obj.PosY(), func(o *Player) {
		if o.visible.Contains(victim.Obj.ID) || o == victim {
			s.sendStruck(o, attacker.Obj.ID, victim.Obj.ID, hp, maxHP, dmg)
		}
	})
	obs.Event("pvp_hit", "attacker", attacker.Char.Name, "victim", victim.Char.Name,
		"dmg", dmg, "hp", hp, "max_hp", maxHP)
	logpvp("%s 打了 %s 一记（伤害 %d，HP %d/%d）",
		attacker.Char.Name, victim.Char.Name, dmg, hp, maxHP)

	if hp > 0 {
		return
	}
	// 复活戒指（与"被怪打死"同一条拦截，见 itemspecial.go）
	if s.tryRevivalRing(victim) {
		return
	}
	s.killPlayerByPlayer(c, attacker, victim, now)
}

func (s *Server) actorInSafe(p *Player) bool {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return false
	}
	return s.safeZoneAt(p.Obj.MapRef(), p.Obj.PosX(), p.Obj.PosY())
}

// killPlayerByPlayer 处理玩家击杀玩家（Die 的 PK 罪分支，ObjBase.pas:20860-21075）。
//
// 顺序照原版：
//  1. SM_NOWDEATH 广播（受击者视角）
//  2. judgeKill 判免罪 / 正当防卫 / 加 PK 罪
//  3. guildwarkill 为假才扣 PK 罪；两种情况都要掉装备（DropUseItems）
//  4. revive 回城
func (s *Server) killPlayerByPlayer(c net.Conn, killer, victim *Player, now time.Time) {
	res := s.judgeKill(victim, killer, now)

	// SM_NOWDEATH：原版发 SM_DEATH 广播 + 自己收到 NOWDEATH（ObjBase.pas:21071）。
	s.broadcastToViewers(victim.Obj.MapRef(), victim.Obj.PosX(), victim.Obj.PosY(), func(o *Player) {
		if o.visible.Remove(victim.Obj.ID) {
			s.sendDeathTo(o, victim.Obj.ID, victim.Obj.PosX(), victim.Obj.PosY(), victim.Obj.Facing(), killer.Obj.ID)
		}
	})
	s.send(victim.conn, proto.SM_NOWDEATH, int32(victim.Obj.ID),
		uint16(victim.Obj.PosX()), uint16(victim.Obj.PosY()), uint16(victim.Obj.Facing()), "")

	// PK 罪结算（免罪时跳过，ObjBase.pas:20936 `if not guildwarkill`）
	if res.AddPKPoint > 0 {
		s.addPKPoint(killer, res.AddPKPoint)
		s.sysMsg(killer.conn, fmt.Sprintf("你被 %s 杀害了！！！", victim.Char.Name))
		// 无条件惩罚：扣幸运 500 + 20% 武器锁 + @OnMurder/@Murdered
		//（ObjBase.pas:20943-20954，见 pkpenalty.go）
		s.applyPKPenalties(killer, victim, now)
	} else if res.GoodKilling {
		s.sysMsg(killer.conn, "[你受到正当规则保护。]")
	}

	// 掉装备：**不在这里调** deathDrop。
	//
	// 死亡掉落挂在 revive 里（main.go 的 `revive` → `deathDrop`），而下面的
	// revivePvP 就会走到它；这里再调一次等于 **PvP 死亡掉两遍**
	//（每个物品被独立掷两次骰：原本 20% 会变成 36%）。
	//
	// ⚠️ 原版这段在 `if (not boFIGHTZone) and (not boFIGHT3Zone)` 内
	//（ObjBase.pas:20983）：FIGHT(PK 区) 与 FIGHT3(行会战争地图) 内死亡不掉落。
	// 该门现在实现在 deathDrop 里（`pvp.ZoneMap.SuppressDeathDrop`），
	// 两条死亡路径都经过它。

	// 等级/经验奖惩（`TPlayObject.PKDie`，ObjBase.pas:21076-21180）。
	// ⚠️ 出厂四个开关全关 ⇒ 默认不生效（见 pkdie.go）。
	s.applyPKDieReward(killer, victim)

	// 复活：红名去监狱地图且只回 14 点血（UsrEngn.pas:579-600）。
	s.revivePvP(victim, killer, now)

	obs.Event("pvp_kill", "killer", killer.Char.Name, "victim", victim.Char.Name,
		"guild_war_kill", res.GuildWarKill, "good_killing", res.GoodKilling,
		"add_pk", res.AddPKPoint)
	logpvp("%s 击杀 %s（免罪=%v 正当防卫=%v 加PK点=%d）",
		killer.Char.Name, victim.Char.Name, res.GuildWarKill, res.GoodKilling, res.AddPKPoint)
}

// revivePvP 是被玩家杀死后的复活处理。
//
// 原版玩家死亡是"掉线式"的：复活点在**下次 Logon** 时决定
// （UsrEngn.pas:576-601），红名去 sRedHomeMap 且 HP 固定 14。
// 我们是原地不踢下线，所以在这里直接按同样的规则处理。
func (s *Server) revivePvP(victim *Player, killer *Player, now time.Time) {
	d := victim.Char.Data
	if d == nil || d.Abil == nil {
		return
	}
	// 掉装备/金币后强制回城，坐标用存档 HomeX/HomeY。
	// killer 只给 FIGHT3 区的行会战积分用（原版 m_LastHiter）。
	s.revive(victim.conn, victim, killer)
	// 红名监狱分流（UsrEngn.pas:594-600）
	if pvp.IsRedName(int32(d.PkPoint)) {
		if err := s.switchMap(victim.conn, victim, s.pvp.cfg.cfg.RedHomeMap,
			s.pvp.cfg.cfg.RedHomeX+int(now.Unix()%13),
			s.pvp.cfg.cfg.RedHomeY+int(now.Unix()%13)); err == nil {
			hp := victim.setHP(14) // 原版固定 14 点
			s.sendHealthChanged(victim, victim.Obj.ID,
				hp, victim.mp(), victim.maxHP())
			logpvp("%s 红名，复活到监狱地图 %s(%d,%d) HP=14",
				victim.Char.Name, s.pvp.cfg.cfg.RedHomeMap,
				victim.Obj.PosX(), victim.Obj.PosY())
		}
	}
	// 复活后清掉正当防卫标记（新的一次人生）
	victim.pvpFlag = false
	s.sendNameColor(victim, now)
}

// ---------- 技能打玩家 ----------

// spellHitsPlayers 判断技能是否可能打到玩家。
//
// 原版没有这个分流：所有攻击性技能都走 DirectAttack → IsProperTarget，
// 治疗/增益类则走各自的分支（不经过 IsProperTarget）。
// 这里按 EffectType 粗分，避免把治疗术打人。
//
// EffectType 取值来自 OpenMir2 mir2_data.sql 的 magics 表；
// 0=攻击(伤害) 1=治疗 2=增益(buff)。
func spellHitsPlayers(info *data.MagicInfo) bool {
	if info == nil {
		return false
	}
	return info.EffectType == 0
}

// spellHitPlayer 技能命中玩家：走与物理攻击同一套 PvP 判定。
//
// 对应 ObjBase.pas:21976（DirectAttack 里 IsProperTarget 通过后 StruckDamage）。
// 治疗/增益类技能不会走到这里（spellHitsPlayers 已挡掉）。
//
// ⚠️ 减伤走 **MAC**（GetMagStruckDamage）而不是 AC，也不保底 1 点——
// 魔法与物理是两条不同的减伤路径，别拿 rollDamage 顶替。
func (s *Server) spellHitPlayer(c net.Conn, caster, victim *Player, nPower int, info *data.MagicInfo) {
	now := time.Now()
	mode := attackModeOf(caster)
	if !s.canAttackTarget(caster, victim, mode, now) {
		logpvp("%s 用 %s 攻击 %s 被拒（模式=%d）",
			caster.Char.Name, info.Name, victim.Char.Name, mode)
		return
	}

	// 飞行特效：与打怪同一条 SM_MAGICFIRE（施法者 + 目标坐标）。
	s.broadcastToViewers(victim.Obj.MapRef(), victim.Obj.PosX(), victim.Obj.PosY(), func(o *Player) {
		s.send(o.conn, proto.SM_MAGICFIRE, int32(caster.Obj.ID),
			uint16(victim.Obj.PosX()), uint16(victim.Obj.PosY()), uint16(info.MagicID), "")
	})

	// 伤害：MAC 是 MinMax 打包（Grobal2.pas:736）。
	// ⚠️ 读对方的属性要拿**快照**：对方自己的 goroutine 正在改它（见 statelock.go）。
	mac := uint32(0)
	if ab := victim.abilCopy(); ab != nil {
		mac = abilityFromPB(ab).MAC
	}
	dmg := uint32(magic.MagStruckDamage(mac, nPower))
	// 红毒：法术打玩家同样放大（落点与近战不同，见 struckPlayer 的说明）
	dmg = s.struckPlayer(victim, dmg, now)
	// ⚠️ 同 damagePlayer：判定 + 扣减同锁
	hp, dmg := victim.hurt(dmg)
	maxHP := victim.maxHP()

	s.broadcastToViewers(victim.Obj.MapRef(), victim.Obj.PosX(), victim.Obj.PosY(), func(o *Player) {
		if o.visible.Contains(victim.Obj.ID) || o == victim {
			s.sendStruck(o, caster.Obj.ID, victim.Obj.ID, hp, maxHP, dmg)
		}
	})
	obs.Event("pvp_spell_hit", "caster", caster.Char.Name, "victim", victim.Char.Name,
		"spell", info.Name, "dmg", dmg, "hp", hp)
	logpvp("%s 用 %s 命中 %s（伤害 %d，HP %d/%d）",
		caster.Char.Name, info.Name, victim.Char.Name, dmg, hp, maxHP)

	// 被打者获得正当防卫标记
	s.setPKFlag(caster, victim, now)

	if hp > 0 {
		return
	}
	s.killPlayerByPlayer(c, caster, victim, now)
}

// setPKPoint 直接设定 PK 点并刷新名字颜色。
//
// 脚本 CHANGEPKPOINT（ActionOfChangePkPoint，ObjNpc.pas:3101）与
// GM 命令 @pk 都走这里——它们是"设定值"而不是"加增量"，
// 所以不走 addPKPoint（那个会附带发"你犯了谋杀罪"的提示）。
func (s *Server) setPKPoint(p *Player, want int32) {
	if p == nil || p.Char == nil || p.Char.Data == nil {
		return
	}
	if want < 0 {
		want = 0
	}
	if want > s.pvp.cfg.cfg.MaxPKPoint {
		want = s.pvp.cfg.cfg.MaxPKPoint
	}
	if p.Char.Data.PkPoint == int64(want) {
		return
	}
	p.Char.Data.PkPoint = int64(want)
	now := time.Now()
	s.sendNameColor(p, now)
	s.sendAreaState(p)
}

// isRedName 是**红名**吗 —— 原版 `ObjGuard.pas:100` 判的就是这个（`PKLevel >= 2`）。
//
// 谁在用：**野生守卫/弓箭手**的选目标（见 `monsterai.go`）—— 白名玩家在城里不该被守卫追着打。
func (p *Player) isRedName() bool {
	return pvp.PKLevel(int32(p.Char.Data.PkPoint)) >= pvp.RedNameLevel
}

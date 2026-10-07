package gamesvr

// 沙巴克城堡（P6）——宣战、存取金、GM 命令。
//
// 协议/脚本对照：
//
//	宣战：NPC 对话框的 @@castlewar 标签 → TGuildOfficial.ReQuestCastleWar
//	      （ObjNpc.pas:11388-11430）：只扣「祖玛碎片」，**不扣金币**，
//	      宣战后不可撤销
//	存取金：@@withdrawal / @@receipts（OpenMir2 CastleOfficial.cs:155-198
//	      用 sWITHDRAWAL / sRECEIPTS 两个脚本常量触发）→
//	      TUserCastle.WithDrawalGolds / ReceiptGolds（Castle.pas:1069-1134）
//
// GM 命令（对应 ObjBase.pas 的 CmdChangeSabukLord /
// CmdForcedWallconquestWar / CmdShowSbkGold，测试与运维用）：
//
//	@castle info                当前状态（占领方/战期/宣战队列/金库）
//	@castle declare <行会名>      宣战（GM 代宣，不扣道具）
//	@castle undeclare <行会名>    撤销宣战（原版无此能力）
//	@castle owner <行会名>        强制换主（留空 = 无主）
//	@castle war on|off           强制开/关攻城期
//	@castle gold <金额>          直接改金库余额

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/castle"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
)

// ---------- 宣战 ----------

// requestCastleWar 处理 @@castlewar 宣战（ObjNpc.pas:11388-11430）。
//
// 顺序与原版一致：先校验（掌门、**不是**占领方成员、持有祖玛碎片、
// 未重复宣战），全过了才扣道具并登记。
func (s *Server) requestCastleWar(c net.Conn, p *Player) {
	cs, err := s.castleDefault()
	if err != nil {
		s.sysMsg(c, "本服未开放城堡")
		return
	}
	ref, ok := s.guildOf(p)
	if !ok {
		s.sysMsg(c, "你还没有加入行会")
		return
	}
	if !ref.isChief() {
		s.sysMsg(c, "只有掌门才能宣战")
		return
	}
	if cs.IsMember(ref.guild.Name) {
		s.sysMsg(c, "你已经是城堡之主，无需宣战")
		return
	}
	idx := s.findBagItemByName(p, s.castle.config.declareItem)
	if idx < 0 {
		s.sysMsg(c, fmt.Sprintf("宣战需要 1 个%s", s.castle.config.declareItem))
		return
	}
	now := time.Now()
	if err := s.castle.mgr.AddAttacker(castleCtx(), cs.ConfigDir(), ref.guild.Name, now); err != nil {
		s.sysMsg(c, "宣战失败: "+err.Error())
		return
	}
	s.takeBagItem(p, idx)
	s.sendBagItems(c, p)

	date := now.AddDate(0, 0, s.castle.config.cfg.DeclareDays)
	s.sysMsg(c, fmt.Sprintf("宣战成功，%s 将发起攻城", date.Format("2006-01-02")))
	log.Printf("%s 宣战城堡（行会 %s，预定 %s，消耗 1 个%s）",
		p.Char.Name, ref.guild.Name, date.Format("2006-01-02"), s.castle.config.declareItem)
}

// ---------- 存取金 ----------

// castleGoldAction 是存取金的两个动作（原版是两个脚本常量）。
type castleGoldAction int

const (
	castleWithdraw castleGoldAction = iota
	castleDeposit
)

// castleGoldErrText 把 internal/castle 的返回码翻成中文。
//
// 文案取自 OpenMir2 CastleOfficial.cs:159-194 的错误分支。
func castleGoldErrText(code int, owner string) string {
	switch code {
	case castle.GoldNoRight:
		return "只有行会 " + owner + " 的掌门人才能使用!!!"
	case castle.GoldNoFund:
		return "该城内没有这么多金币."
	case castle.GoldTooMuch:
		return "你已经达到在城内存放货物的限制了。"
	default:
		return "输入的金币数不正确!!!"
	}
}

// castleGold 存取金（@@withdrawal / @@receipts）。
func (s *Server) castleGold(c net.Conn, p *Player, act castleGoldAction, arg string) {
	cs, err := s.castleDefault()
	if err != nil {
		s.sysMsg(c, "本服未开放城堡")
		return
	}
	ref, ok := s.guildOf(p)
	if !ok {
		s.sysMsg(c, "你还没有加入行会")
		return
	}
	gold, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
	if err != nil {
		s.sysMsg(c, "输入的金币数不正确!!!")
		return
	}
	d := p.Char.Data
	if d == nil || d.Abil == nil {
		return
	}
	w := castle.Wallet{Gold: p.gold(), MaxGold: playerMaxGold(p.level())}

	var code int
	switch act {
	case castleWithdraw:
		code = s.castle.mgr.WithDrawalGolds(castleCtx(), cs.ConfigDir(), ref.guild.Name, ref.isChief(), gold, w)
		if code == castle.GoldOK {
			p.addGold(gold)
		}
	case castleDeposit:
		// ⚠️ 先把钱从钱包**原子扣出**再去记账。反过来（记账成功、扣款失败）会让金库
		// 凭空多一笔 —— 那正是丢更新的那个方向。记账失败就把钱还回去。
		if !p.spendGold(gold) {
			s.sysMsg(c, "金币不足")
			return
		}
		code = s.castle.mgr.ReceiptGolds(castleCtx(), cs.ConfigDir(), ref.guild.Name, ref.isChief(), gold, w)
		if code != castle.GoldOK {
			p.addGold(gold) // 记账没成功 ⇒ 退钱
		}
	}
	if code != castle.GoldOK {
		s.sysMsg(c, castleGoldErrText(code, cs.OwnGuild()))
		return
	}
	s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
	s.sysMsg(c, fmt.Sprintf("操作成功，金库余额 %d", cs.TotalGold()))
}

// playerMaxGold 是玩家金币上限（原版 m_nGoldMax）。
//
// 我们没实现 MaxGold 字段，这里按传奇量级给：等级 × 1000000。
func playerMaxGold(level uint32) int64 { return int64(level) * 1_000_000 }

// ---------- GM 命令 ----------

// cmdCastle 是 @castle 管理命令。
func (s *Server) cmdCastle(c net.Conn, p *Player, args []string) {
	cs, err := s.castleDefault()
	if err != nil {
		s.sysMsg(c, "本服未开放城堡")
		return
	}
	if len(args) == 0 {
		s.castleInfo(c, cs)
		return
	}
	ctx := castleCtx()
	switch strings.ToLower(args[0]) {
	case "info":
		s.castleInfo(c, cs)
	case "door":
		// 开关城门（GM 用；玩家侧走沙巴克城堡官员的 @@openmaindoor/@@closemaindoor，
		// 原版 ObjNpc.pas:557-583 → Castle.MainDoorControl）。
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle door open|close")
			return
		}
		switch strings.ToLower(args[1]) {
		case "open":
			s.mainDoorControl(c, p, false)
		case "close":
			s.mainDoorControl(c, p, true)
		default:
			s.sysMsg(c, "用法: @castle door open|close")
		}
	case "declare":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle declare <行会名>")
			return
		}
		if s.social.guilds.Find(args[1]) == nil {
			s.sysMsg(c, "行会「"+args[1]+"」不存在")
			return
		}
		if err := s.castle.mgr.AddAttacker(ctx, cs.ConfigDir(), args[1], time.Now()); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, fmt.Sprintf("行会 %s 已宣战（预定 %s，GM 代宣）", args[1],
			time.Now().AddDate(0, 0, s.castle.config.cfg.DeclareDays).Format("2006-01-02")))
	case "undeclare":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle undeclare <行会名>")
			return
		}
		if err := s.castle.mgr.CancelAttacker(ctx, cs.ConfigDir(), args[1]); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, "已撤销宣战: "+args[1])
	case "owner":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle owner <行会名>（留空 = 无主）")
			return
		}
		name := strings.Join(args[1:], " ")
		if name != "" && s.social.guilds.Find(name) == nil {
			s.sysMsg(c, "行会「"+name+"」不存在")
			return
		}
		if _, err := s.castle.mgr.SetOccupant(ctx, cs.ConfigDir(), name, time.Now()); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, "城堡占领方已设为 "+name)
	case "war":
		s.gmCastleWar(c, cs, args[1:])
	case "repair", "hire":
		// @castle repair door|wall <n> / @castle hire guard|archer <n>
		//
		// ⚠️ args[0] 是**动词**（repair/hire），类型在 args[1]。这里原来写成
		// `switch args[0]` ⇒ 永远落到 default 回一句"未知类型 repair"
		// ⇒ `@castle repair/hire` 从来没能用过（e2e 此前只用 door/war/owner，
		// 2026-10-05 加守卫用例时才暴露）。
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle repair door|wall <编号>，@castle hire guard|archer <编号>")
			return
		}
		kind := storage.CastleMainDoor
		switch strings.ToLower(args[1]) {
		case "door":
			kind = storage.CastleMainDoor
		case "wall":
			kind = storage.CastleWall
		case "guard":
			kind = storage.CastleGuard
		case "archer":
			kind = storage.CastleArcher
		default:
			s.sysMsg(c, "未知类型 "+args[1])
			return
		}
		args = args[2:] // [动词, 类型, 编号...] ⇒ 只剩 <编号>
		// 统一按 1 起编号，与原版 RepairWall(1)=左墙 一致。
		num := "1"
		if len(args) > 0 {
			num = args[0]
		}
		s.handleCastleRepair(c, p, kind, num)
	case "damage":
		// 调试用：把某个城堡单位打掉，验证修缮流程。
		//
		// ⚠️ 与上面的 repair/hire 是**同一个坑的第三处**：args[0] 是**动词**（damage），
		// 类型在 args[1]。原来写成 `strings.EqualFold(args[0], "wall")` +
		// `Atoi(args[1])` ⇒ "@castle damage door 1" 会被解成"类型=门、编号=door(=0)"
		// ⇒ `castleUnitByKind(kind, -1)` 拿不到单位、回一句"没有该单位"。
		if len(args) < 3 {
			s.sysMsg(c, "用法: @castle damage door|wall <编号>")
			return
		}
		kind := storage.CastleMainDoor
		switch strings.ToLower(args[1]) {
		case "door":
			kind = storage.CastleMainDoor
		case "wall":
			kind = storage.CastleWall
		default:
			s.sysMsg(c, "未知类型 "+args[1])
			return
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 1 {
			s.sysMsg(c, "编号从 1 开始")
			return
		}
		m := s.castleUnitByKind(kind, n-1)
		if m == nil {
			s.sysMsg(c, "没有该单位")
			return
		}
		s.mu.Lock()
		died := m.Damage(m.HP)
		s.mu.Unlock()
		// ⚠️ 必须补**死亡收尾**：玩家挥砍那条路径（main.go 的 hitMonster）在
		// `died && IsCastleUnit()` 时会调 `castleDoorDestroyed` 重设门格，
		// 这里只把 HP 打到 0 的话，门"坏了"但格还挡着（实测：打碎后走不进去）。
		if died && m.IsCastleUnit() {
			s.castleDoorDestroyed(m)
		}
		s.broadcastCastleUnitRefresh(m)
		s.sysMsg(c, fmt.Sprintf("%s 已被打坏（HP 0）", cs.RepairString(kind, n-1)))
	case "gold":
		if len(args) < 2 {
			s.sysMsg(c, "用法: @castle gold <金额>")
			return
		}
		v, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || v < 0 {
			s.sysMsg(c, "金额非法")
			return
		}
		if err := s.castle.mgr.SetTotalGold(ctx, cs.ConfigDir(), v); err != nil {
			s.sysMsg(c, err.Error())
			return
		}
		s.sysMsg(c, fmt.Sprintf("金库余额设为 %d（GM）", v))
	default:
		s.sysMsg(c, "用法: @castle info|declare|undeclare|owner|war|gold|repair|hire|damage <参数>")
	}
}

// castleInfo 打印城堡状态。
func (s *Server) castleInfo(c net.Conn, cs *castle.Castle) {
	r := cs.Record()
	owner := cs.OwnGuild()
	if owner == "" {
		owner = "（无主）"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s：占领方 %s｜战期 %s｜攻守方 %v", cs.Name(), owner,
		s.castleWarState(cs), cs.Participants())
	fmt.Fprintf(&b, "｜金库 %d（当日 %d）", cs.TotalGold(), cs.TodayIncome())
	if at := cs.Attackers(); len(at) > 0 {
		parts := make([]string, 0, len(at))
		for _, a := range at {
			parts = append(parts, fmt.Sprintf("%s@%s", a.GuildName, a.AttackDate.Format("01-02")))
		}
		fmt.Fprintf(&b, "｜宣战 %s", strings.Join(parts, " "))
	}
	fmt.Fprintf(&b, "｜战区 %s±%d / 皇宫 %s", r.MapName, r.WarRangeX, r.PalaceMap)
	s.sysMsg(c, b.String())
}

// castleWarState 描述当前战期状态。
func (s *Server) castleWarState(cs *castle.Castle) string {
	now := time.Now()
	switch {
	case cs.UnderWar():
		return fmt.Sprintf("攻城中（剩 %s）", cs.WarLeft(now).Round(time.Minute))
	case cs.StartWar():
		return "今日已过开城时刻"
	default:
		return fmt.Sprintf("未开战（每日 %d 点开城，攻守期 %s）",
			cs.Config().StartWarHour, cs.Config().WarDuration)
	}
}

// gmCastleWar 强制开/关攻城期。
//
// 对应原版 CmdForcedWallconquestWar（ObjBase.pas:12877-12916）：开 = 置
// underWar + 重置开战时刻 + 广播 + 关门；关 = StopWallconquestWar。
//
// 我们没有导出"直接置位"的入口，所以开走的是等效路径：把守方行会的宣战日
// 设成今天，再跑一次状态机（它会正常走完 promote → 开战的全流程）。
func (s *Server) gmCastleWar(c net.Conn, cs *castle.Castle, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @castle war on|off")
		return
	}
	switch strings.ToLower(args[0]) {
	case "on", "1":
		if cs.UnderWar() {
			s.sysMsg(c, "已经在攻城中")
			return
		}
		if cs.OwnGuild() == "" {
			s.sysMsg(c, "请先用 @castle owner <行会名> 设一个占领方，再强制开战")
			return
		}
		// 原版 GM 命令是**直接翻 m_boUnderWar 并 StartWallconquestWar**
		//（ObjBase.pas:12877-12911），不走"当日到点 + 有已宣战行会"那条时间条件。
		// 早先这里只调 Run 就回"已强制进入攻城期" —— 白天执行时战争根本没开，
		// 消息却在骗人（城墙照样石化、打不动），已改成 ForceWarStart。
		if !cs.ForceWarStart(s.castleHooks(), time.Now()) {
			s.sysMsg(c, "强制开战失败（没有参战行会）")
			return
		}
		s.sysMsg(c, "已强制进入攻城期")
	case "off", "0":
		if !cs.UnderWar() {
			s.sysMsg(c, "当前不在攻城中")
			return
		}
		cs.StopWar(s.castleHooks())
		s.sysMsg(c, "已强制结束攻城")
	default:
		s.sysMsg(c, "用法: @castle war on|off")
	}
}

// ---------- 广播与协议入口 ----------

// castleOfficialMenu 给"站在城堡战场地图上的玩家"追加一份城堡管理菜单。
//
// ⚠️ 原版这套菜单属于 `TCastleOfficial` —— 一个**特殊的 NPC 类**，菜单由代码 +
// 动态变量 `<$REQUESTCASTLELIST>` 拼出来（ObjNpc.pas:530-620、11260-11296）。
// 而**出厂数据里没有这个 NPC**：我们这份 GeeM2 数据的沙巴克城里只有屠夫/小贩
// （`data/castle/0/SabukW.txt` 只给了门/墙/守卫坐标，`merchant.txt` 里没有管理员）。
//
// 于是我们定了一条**替身规则**（写在注释里，不是抄来的）：
// 玩家在**城堡战场地图**上打开任意 NPC 对话时，追加一份菜单。
// 链接文本用的就是原版标签原文（`<修城门/@repairdoornow>` …）⇒ 客户端点回来
// `handleCastleMsg` 正好认；服务端不依赖菜单，只依赖标签。
func (s *Server) castleOfficialMenu(c net.Conn, p *Player) {
	if p == nil || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	cs, err := s.castleDefault()
	if err != nil || p.Obj.MapRef().Name != cs.Record().MapName {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【%s】<修城门/@repairdoornow> "+
		"<修左墙/@repairwallnow1> <修中墙/@repairwallnow2> <修右墙/@repairwallnow3>", cs.Name())
	for i := 1; i <= castle.MaxGuard; i++ {
		fmt.Fprintf(&b, "<雇佣守卫%d/@hireguardnow%d>", i, i)
	}
	for i := 1; i <= castle.MaxArcher; i++ {
		fmt.Fprintf(&b, "<雇佣弓箭手%d/@hirearchernow%d>", i, i)
	}
	fmt.Fprintf(&b, "<开城门/@openmaindoor> <关城门/@closemaindoor>")
	s.sysMsg(c, b.String())
}

// broadcastSysMsg 给全服在线玩家发一条系统消息。
//
// 对应原版 SendBroadCastMsgExt(sMsg, t_System)（Castle.pas:673、845、886）。
func (s *Server) broadcastSysMsg(msg string) {
	s.mu.RLock()
	conns := make([]net.Conn, 0, len(s.world.players))
	for _, pl := range s.world.players {
		conns = append(conns, pl.conn)
	}
	s.mu.RUnlock()
	for _, c := range conns {
		s.sysMsg(c, msg)
	}
}

// castlePlayerAllowed 是原版 `TCastleOfficial` 最外层那道门（ObjNpc.pas:612-616）：
// 只有**占领行会的掌门人**能用整套城堡菜单，其他人只回一句"你没有权利使用..."。
//
// ⚠️ `@@` 那批（GM 形态）**不走**这道门 —— 它们是原版没有的家用入口，
// 交给 GM 命令自己的权限体系。
func (s *Server) castlePlayerAllowed(c net.Conn, p *Player, cs *castle.Castle) bool {
	ref, ok := s.guildOf(p)
	if !ok || !ref.isChief() || !cs.IsMasterGuild(ref.guild.Name) {
		s.sysMsg(c, "你没有权利使用...")
		return false
	}
	return true
}

// castleLabelIndex 从 `@hireguardnow<N>` 这类"标签带编号"的形态里取 <N>（原样返回字符串，
// 交给 `handleCastleRepair` 去解析 —— 它按原版约定把编号当 **1 起**）。
func castleLabelIndex(label, prefix string) (string, bool) {
	if len(label) <= len(prefix) || !strings.HasPrefix(label, prefix) {
		return "", false
	}
	return label[len(prefix):], true
}

// handleCastleMsg 处理城堡相关的 NPC 标签，返回是否已处理。
//
// 两套形态：
//
//	`@@xxx` —— 我们自己加的家用形态（编号放在**输入框**里，见 docs）；
//	`@xxx`  —— **原版客户端点链接**发上来的原文（`TCastleOfficial.UserSelect`，
//	           ObjNpc.pas:530-620）：修墙是三个固定标签、守卫/弓箭手把编号**跟在标签后面**、
//	           攻城是 `@requestcastlewarnow`（行会管理员菜单里那串 `<$REQUESTCASTLELIST>`）。
func (s *Server) handleCastleMsg(c net.Conn, p *Player, tag, param string) bool {
	lc := strings.ToLower(tag)
	// —— 玩家侧（原版）形态：先过"掌门人"那道门 ——
	switch {
	case lc == "@repairdoornow", lc == "@repairwallnow1", lc == "@repairwallnow2",
		lc == "@repairwallnow3", lc == "@requestcastlewarnow", lc == "@openmaindoor",
		lc == "@closemaindoor", strings.HasPrefix(lc, "@hireguardnow"),
		strings.HasPrefix(lc, "@hirearchernow"):
		cs, err := s.castleDefault()
		if err != nil {
			s.sysMsg(c, "本服未开放城堡")
			return true
		}
		if !s.castlePlayerAllowed(c, p, cs) {
			return true
		}
		switch {
		case lc == "@repairdoornow":
			s.handleCastleRepair(c, p, storage.CastleMainDoor, "0")
		case lc == "@repairwallnow1":
			s.handleCastleRepair(c, p, storage.CastleWall, "1")
		case lc == "@repairwallnow2":
			s.handleCastleRepair(c, p, storage.CastleWall, "2")
		case lc == "@repairwallnow3":
			s.handleCastleRepair(c, p, storage.CastleWall, "3")
		case lc == "@requestcastlewarnow":
			// 原版是 `<$REQUESTCASTLELIST>` 里逐个城堡的编号；我们单城 ⇒ 忽略编号。
			s.requestCastleWar(c, p)
		case lc == "@openmaindoor":
			s.mainDoorControl(c, p, false)
		case lc == "@closemaindoor":
			s.mainDoorControl(c, p, true)
		case strings.HasPrefix(lc, "@hireguardnow"):
			idx, _ := castleLabelIndex(lc, "@hireguardnow")
			s.handleCastleRepair(c, p, storage.CastleGuard, idx)
		default:
			idx, _ := castleLabelIndex(lc, "@hirearchernow")
			s.handleCastleRepair(c, p, storage.CastleArcher, idx)
		}
		return true
	}

	switch lc {
	case "@@castlewar", "@@requestcastlewar":
		// 原版客户端在攻城对话框里点宣战，不需要输入行会名
		//（自己就是宣战方），所以没有内嵌输入框。
		s.requestCastleWar(c, p)
	case "@@withdrawal":
		s.castleGold(c, p, castleWithdraw, param)
	case "@@receipts", "@@receipt":
		s.castleGold(c, p, castleDeposit, param)
	// 修门/修墙/雇佣。编号放在输入框里（1 起）。
	case "@@repairdoor":
		s.handleCastleRepair(c, p, storage.CastleMainDoor, "0")
	case "@@repairwall":
		s.handleCastleRepair(c, p, storage.CastleWall, param)
	case "@@hireguard":
		s.handleCastleRepair(c, p, storage.CastleGuard, param)
	case "@@hirearcher":
		s.handleCastleRepair(c, p, storage.CastleArcher, param)
	// 开/关城门（原版标签 sOPENMAINDOOR/sCLOSEMAINDOOR，ObjNpc.pas:557-583 →
	// Castle.MainDoorControl，Castle.pas:1136-1148）。
	case "@@openmaindoor":
		s.mainDoorControl(c, p, false)
	case "@@closemaindoor":
		s.mainDoorControl(c, p, true)
	default:
		return false
	}
	return true
}

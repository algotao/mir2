package gamesvr

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/script"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/protocol"
)

// NPC 对话与脚本执行。
//
// 脚本来自 Envir\market_def\<NPC ID>-<地图>.txt（1.76 官方配置）。
// 点击 NPC 时：有脚本 → 进入对话；没有 → 打开商店。

// dialog 记录玩家当前所处的对话。
// npcTalkRange 是**能跟 NPC 对话的距离**（格，切比雪夫）。
//
// 两个地方共用同一条口径：`onNpcClick`（点得开吗）与 `tickDialogRange`
// （走远了自动关吗）—— 两边不一样就会出现"点不开但关不掉"这种怪状态。
const npcTalkRange = 8

type dialog struct {
	scriptName string
	// npcID 用于对白里的 @buy/@sell/@trading 打开对应商店。
	npcID uint32
	links []script.Link
	// label 是**当前所处的脚本标签**（原版 `m_sScriptLable`，小写）。
	// 修理的两档就是按它分的（`@s_repair` 特修 / 其余普修，ObjNpc.pas:2431-2446）。
	label string
}

// npcScript 取某个 NPC 的脚本，按需加载并缓存。
//
// 文件名是 <NPC ID>-<地图>.txt（如 1Bme-0102.txt）。
// 同一 NPC 在不同地图可能挂不同脚本。
func (s *Server) npcScript(id, mapID string) *script.Script {
	if id == "" {
		return nil
	}
	key := id + "-" + mapID

	s.npc.mu.RLock()
	if sc, ok := s.npc.scripts[key]; ok {
		s.npc.mu.RUnlock()
		return sc
	}
	s.npc.mu.RUnlock()

	path := filepath.Join(s.npc.scriptDir, key+".txt")
	if _, err := os.Stat(path); err != nil {
		// 没有专属脚本时退回 <ID>.txt（部分 NPC 不分地图）
		path = filepath.Join(s.npc.scriptDir, id+".txt")
		if _, err := os.Stat(path); err != nil {
			return nil
		}
	}
	log.Printf("加载 NPC 脚本: %s", path)
	sc, err := script.ParseFile(path)
	if err != nil {
		log.Printf("脚本 %s 解析失败: %v", path, err)
		return nil
	}

	s.npc.mu.Lock()
	// `scripts` 在最小测试服里是 nil（生产路径由启动流程建好）—— 与 `spawned` 同款防御。
	if s.npc.scripts == nil {
		s.npc.scripts = make(map[string]*script.Script)
	}
	s.npc.scripts[key] = sc
	s.npc.mu.Unlock()
	return sc
}

// startDialog 打开一个脚本的入口段。
func (s *Server) startDialog(c net.Conn, p *Player, sc *script.Script, npcID uint32) {
	entry := sc.Entry()
	if entry == nil {
		return
	}
	p.dialog = &dialog{scriptName: sc.Name, npcID: npcID}
	s.showLabel(c, p, sc, entry)
}

// showLabel 显示一个段：按 #if 结果选择 #act 或 #elseact，再输出文本与选项。
// 没有 #elseact 且条件不满足时，该段仍静默停在这里。
func (s *Server) showLabel(c net.Conn, p *Player, sc *script.Script, l *script.Label) {
	if l == nil {
		return
	}
	if p.dialog == nil {
		p.dialog = &dialog{scriptName: sc.Name}
	}

	passed := s.evalLabelConds(p, l)
	acts := l.Acts
	if !passed {
		if len(l.ElseActs) == 0 {
			p.dialog.links = nil
			return
		}
		acts = l.ElseActs
	}

	p.dialog.links = l.Links

	// 选中的动作在显示之前执行（原版语义）。
	s.runActs(c, p, acts)

	// 正文用**原样行**（带行内标记 `<打开/@1>`）：用户在 2026-10-09 报
	// "交易窗口渲染不对 —— 应该为『打开 交易市场』在一行，其中『打开』可点击"，
	// 根因就是旧版发的是 `l.Say`（行内选项已被抽走 ⇒ 只能单列成底部列表）。
	// `Links` 仍然照发：客户端拿它兜底（正文没有标记时按"底部选项列表"画），
	// 也方便老客户端。
	text := l.Say
	if len(l.Lines) > 0 {
		text = strings.Join(l.Lines, "\n")
	}
	s.npcSay(c, p, p.dialog.npcID, text, l.Links)
}

// npcClose 告诉客户端"这段对话结束了"（新协议 `NpcClose`；legacy 没有对应下行，
// 原版客户端是收到脚本结束标记后自己清的）。
//
// ⚠️ 必须**在清 `p.dialog` 之前**调（要用里面的 npc id）。
func (s *Server) npcClose(c net.Conn, p *Player) {
	if p == nil || p.protoOut == nil || p.dialog == nil {
		return
	}
	p.protoOut.enqueue(&protocol.Envelope{Body: &protocol.Envelope_NpcClose{
		NpcClose: &protocol.NpcClose{NpcId: uint64(p.dialog.npcID)}}})
}

// npcSay 把一段对白发给玩家：**新协议**走结构化 `NpcSay`（正文 + 选项），legacy 走 `sysMsg`。
//
// ⚠️ 为什么必须分流：proto 玩家的 legacy 下行是**被丢弃**的（`protoDown`，见 netproto.go
// 里 enterWorld 的说明）⇒ 只发 legacy 的话新协议客户端一个字都收不到 ——
// 用户报的"点击 NPC 无法弹出对话"一半就是这个原因（另一半是客户端没发 NpcClick）。
func (s *Server) npcSay(c net.Conn, p *Player, npcID uint32, text string, links []script.Link) {
	// ⚠️ 行尾的 `\` 是脚本的**行继续符**（`[@main]` 一行写不下时换行接着写），
	// 不是正文 ⇒ 下发前抹掉。不抹的话客户端（对话窗/聊天里）就会多出一个反斜杠
	//（用户 2026-10-10 第 3 条，反复出现过）。
	text = stripLineContinuations(text)
	if p != nil && p.protoOut != nil {
		opts := make([]*protocol.NpcOption, 0, len(links))
		for i, lk := range links {
			opts = append(opts, &protocol.NpcOption{Index: uint32(i + 1), Text: lk.Text})
		}
		if text == "" && len(opts) == 0 {
			text = "……"
		}
		p.protoOut.enqueue(&protocol.Envelope{Body: &protocol.Envelope_NpcSay{
			NpcSay: &protocol.NpcSay{NpcId: uint64(npcID), Text: text, Options: opts}}})
		return
	}
	// legacy：原版就是把选项编号拼进正文一起发（`[1] 选项`）
	msg := text
	for i, lk := range links {
		if msg != "" {
			msg += "\n"
		}
		msg += fmt.Sprintf("[%d] %s", i+1, lk.Text)
	}
	if msg == "" {
		msg = "……"
	}
	s.sysMsg(c, msg)
}

// stripLineContinuations 去掉每行末尾的 `\`（脚本的行继续符）。
//
// 例：`要不要来点肉？\` + `<我要买/@1>\` ⇒ 展示成两行时，第一行末尾那个 `\`
// 只是"这里还没写完"，不该给玩家看。
func stripLineContinuations(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\\")
	}
	return strings.Join(lines, "\n")
}

// handleDlgSelect 处理玩家在对话里选择某项（CM_MERCHANTDLGSELECT=1011）。
//
// 两种形态：
//   - 原版：Recog = NPC 的 ActorId，body = 选项文本。以 '@@' 开头的标签
//     表示需要内嵌输入框，客户端会把玩家输入拼在 #13 之后一起发来
//     （ClMain.pas:3661-3681）；
//   - 简化：Recog = 选项序号（1-based），body 为空（mir2cli 的用法）。
func (s *Server) handleDlgSelect(c net.Conn, p *Player, m wire.Packet) {
	if p.dialog == nil {
		return
	}
	if body := strings.TrimSpace(m.Body); strings.HasPrefix(body, "@") {
		s.handleDlgSelectText(c, p, body)
		return
	}
	s.dlgSelectIndex(c, p, int(m.Head.Recog)-1)
}

// dlgSelectIndex 按**序号**（0-based）选一项（原版 `CM_MERCHANTDLGSELECT` 的简化形态：
// Recog = 选项序号）。新协议的 `NpcSelect.index - 1` 也落在同一个函数上。
func (s *Server) dlgSelectIndex(c net.Conn, p *Player, idx int) {
	if p.dialog == nil || idx < 0 || idx >= len(p.dialog.links) {
		return
	}
	lk := p.dialog.links[idx]
	// 记下"玩家点进哪个标签了"（原版 m_sScriptLable，由 GotoLable 推进）
	p.dialog.label = strings.ToLower(lk.Label)

	switch strings.ToLower(lk.Label) {
	case "exit":
		// ⚠️ 用户 2026-10-09 报的 bug：点脚本里的「退出」**窗口不关**（按 ESC 能关 ——
		// 那是客户端本地清的）。根因就在这里：只把服务端状态清了、**没有下行**，
		// 客户端不知道 ⇒ 面板一直挂着。凡是"对话结束"都要走 `npcClose`。
		s.npcClose(c, p)
		p.dialog = nil
		return
	case "buy", "trading":
		// 脚本里的商店入口 —— **这一下才把货架发过去**（用户 2026-10-10：
		// 「商店图应在"打开 交易市场"时弹出，而不是开启对话就出」）。
		// 点 NPC 只开对话；`openShop` 内部会建对话上下文 + 发商品列表（两条协议都发）。
		s.openShop(c, p, p.dialog.npcID)
		// 原版点"买"之后对话**换成**这句（用户给的截图：正文"你想买什么?" + 「返回」
		// 链接），货架列表是另一个窗。给 proto 玩家补上这段（legacy 客户端自己画）。
		//
		// ⚠️ 行内标记必须是 **`<文字/@序号>`**（序号 1 起，与 `links` 次序一致）：
		// 客户端只认数字序号（`input::parse_marked_line`）。早先这里直接写
		// `<返回/@main>`（脚本标签）⇒ 解析不出来 ⇒ "返回"变成一行死文字、
		// 点它没反应（用户 2026-10-10 第 3 条）。所以正文给序号、**同时**把
		// `links` 设成 [返回 → @main] —— 客户端回序号，服务端按 links 找标签跳转。
		if c == nil && p.protoOut != nil {
			p.dialog.links = []script.Link{{Text: "返回", Label: "main"}}
			s.npcSay(c, p, p.dialog.npcID, "你想买什么？\\\n<返回/@1>", nil)
		}
		return
	case "sell":
		// 卖：原版这里开"卖出"窗。我们的卖法是**商店窗开着时点背包里的东西**
		// ⇒ 给 proto 玩家指个路；legacy 客户端有自己的卖出窗，照旧开商店。
		if c == nil && p.protoOut != nil {
			s.npcSay(c, p, p.dialog.npcID,
				"要卖东西：保持这个商店窗开着，按 F9 打开背包，点背包里的物品就卖了。", nil)
			return
		}
		s.openShop(c, p, p.dialog.npcID)
		return
	case "storage", "getback":
		// 同上（按序号选择时镜像一份；原版是文本形态）
		if s.handleStorageDialog(c, p, "@"+strings.ToLower(lk.Label)) {
			return
		}
	case "upgradenow", "getbackupgnow":
		// 武器修炼（镜像文本形态那条路，见 @upgradenow 分支）
		if !s.dialogAllows(p, "@"+strings.ToLower(lk.Label)) {
			return
		}
		if strings.EqualFold(lk.Label, "upgradenow") {
			s.weaponUpgradeStart(c, p, p.dialog.npcID)
		} else {
			s.weaponUpgradeTake(c, p, p.dialog.npcID)
		}
	case "main":
		// 回到入口
	}

	sc := s.scriptByName(p.dialog.scriptName)
	if sc == nil {
		return
	}
	if next := sc.Label(lk.Label); next != nil {
		s.showLabel(c, p, sc, next)
		return
	}
	// 标签不存在（很多脚本里的 @buy/@sell 指向商店功能）
	s.sysMsg(c, fmt.Sprintf("（选项 %q 尚未实现）", lk.Label))
}

// handleDlgSelectText 处理原版形态的选项文本。
//
// 目前支持 NPC 脚本里的 @@ 内嵌输入框标签（客户端会弹输入框，
// 并把输入拼在标签后的 #13 之后）：
//
//	@@buildguildnow\r<行会名>   —— 建会（ObjNpc.pas:11317-11364）
//	@@guildwar\r<行会名>       —— 行会战（ObjNpc.pas:26722）
//
// 以及城堡的无输入框标签（见 handleCastleMsg）。
//
// 其余 @@ 标签暂未实现，回一条系统提示。
func (s *Server) handleDlgSelectText(c net.Conn, p *Player, body string) {
	param, tag := cutAt(body, "\r\n")
	// 记下当前标签（小写；原版 m_sScriptLable）—— 修理分档要用
	p.dialog.label = strings.ToLower(strings.TrimSpace(tag))
	// 仓库入口：`@storage`（开界面）/ `@getback`（拉列表）。
	//
	// ⚠️ 这是**单 @** 的脚本标签（不是 @@ 输入框标签），1.76 的仓库就靠它打开
	//（ObjNpc.pas:1560-1575），没有 CM_OPENSTORAGE 这种包。见 storage.go。
	if s.handleStorageDialog(c, p, tag) {
		return
	}
	switch strings.ToLower(tag) {
	case "@repair", "@s_repair":
		// 打开修理界面（原版 `TMerchant.RepairItem`/`S_RepairItem`，
		// ObjNpc.pas:1536-1560 ⇒ `User.SendMsg(Self, RM_SENDUSERREPAIR, …)`）：
		// `@s_repair` 是**特修**档（价格 ×3、不磨损上限），`@repair` 是普修。
		// 两者都要脚本头声明了对应标签才开（原版查 `m_boS_repair`/`m_boRepair`）。
		if s.dialogAllows(p, strings.ToLower(tag)) {
			if npc := s.dialogNPC(p); npc != nil {
				s.send(c, proto.SM_SENDUSERREPAIR, 0, uint16(npc.ID), 0, 0, "")
			}
		}
	case "@makedrug":
		// 打开制药列表（原版 `TMerchant.MakeDurg`，ObjNpc.pas:1465-1492）：
		// 脚本头没声明 `@makedrug` 的 NPC 不接这活（原版查 `m_boMakeDrug`）。
		if s.dialogAllows(p, "@makedrug") {
			if npc := s.dialogNPC(p); npc != nil {
				s.sendMakeDrugList(c, p, npc, s.npcDefOf(npc))
			}
		}
	case "@upgradenow":
		// 武器修炼（原版 `TMerchant.UpgradeWapon`，见 weaponupgrade.go）。
		// 脚本头没声明 @upgradenow 的 NPC 不允许接这活（原版查 m_boUpgradenow）。
		if s.dialogAllows(p, "@upgradenow") {
			s.weaponUpgradeStart(c, p, p.dialog.npcID)
		}
	case "@getbackupgnow":
		if s.dialogAllows(p, "@getbackupgnow") {
			s.weaponUpgradeTake(c, p, p.dialog.npcID)
		}
	case "@@buildguildnow":
		s.requestBuildGuild(c, p, param)
	case "@@guildwar":
		// 原版客户端在行会管理员对话框里点"发起行会战"后弹输入框，
		// 玩家填对方行会名（ObjNpc.pas:11365-11383 → ReQuestGuildWar）。
		s.requestGuildWar(c, p, param)
	default:
		if s.handleCastleMsg(c, p, tag, param) {
			return
		}
		s.sysMsg(c, fmt.Sprintf("（选项 %q 尚未实现）", tag))
	}
}

// scriptByName 从缓存里按脚本名找（用于对话跳转）。
func (s *Server) scriptByName(name string) *script.Script {
	s.npc.mu.RLock()
	defer s.npc.mu.RUnlock()
	for _, sc := range s.npc.scripts {
		if sc.Name == name {
			return sc
		}
	}
	return nil
}

// ---------- #act 指令 ----------

// runActs 执行一组 #act 指令。
//
// 目前支持：mapmove / give / take（扣物品）/ takegold / givegold / gold。
// 其余指令忽略并记日志——1.76 脚本里有十几条常用指令，逐步补齐。
func (s *Server) runActs(c net.Conn, p *Player, acts []string) {
	for _, raw := range acts {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		switch strings.ToLower(f[0]) {
		case "groupmovemap":
			// GROUPMOVEMAP <地图名> <X> <Y>：把**全队**送到同一坐标。
			//
			// 对应 ObjNpc.pas:10962-10992 ActionOfGroupMoveMap。
			//
			// ⚠️ 原版 `GROUPMOVE`（不带 MAP）**只有常量、没有解析也没有分发**，
			// 是死代码（M2Share.pas:828-829 有常量，LocalDB/ObjNpc 都没有分支）；
			// 可用的是 GROUPMOVEMAP。别顺手实现 GROUPMOVE。
			if len(f) >= 4 {
				x, e1 := strconv.Atoi(f[2])
				y, e2 := strconv.Atoi(f[3])
				if e1 == nil && e2 == nil {
					s.actGroupMoveMap(c, p, f[1], x, y)
				}
			}
		case "mapmove":
			if len(f) >= 4 {
				x, e1 := strconv.Atoi(f[2])
				y, e2 := strconv.Atoi(f[3])
				if e1 == nil && e2 == nil {
					if err := s.switchMap(c, p, f[1], x, y); err != nil {
						log.Printf("脚本 mapmove 失败: %v", err)
					}
				}
			}
		case "give":
			if len(f) >= 2 {
				n := 1
				if len(f) >= 3 {
					n, _ = strconv.Atoi(f[2])
				}
				s.actGive(c, p, f[1], n)
			}
		case "take":
			if len(f) >= 2 {
				n := 1
				if len(f) >= 3 {
					n, _ = strconv.Atoi(f[2])
				}
				s.actTake(c, p, f[1], n)
			}
		case "givegold":
			if len(f) >= 2 && p.Char.Data != nil {
				if v, err := strconv.Atoi(f[1]); err == nil {
					p.addGold(int64(v))
					s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
				}
			}
		case "takegold":
			if len(f) >= 2 && p.Char.Data != nil {
				if v, err := strconv.Atoi(f[1]); err == nil {
					// 原子"够就扣"：判定与扣款不能分家（并发收支会插进来）
					if !p.spendGold(int64(v)) {
						s.sysMsg(c, "金币不足")
						return
					}
					s.send(c, proto.SM_GOLDCHANGED, int32(p.gold()), 0, 0, 0, "")
				}
			}
		case "map":
			// MAP <地图> [x] [y]：MAPMOVE 的简写，不带发包模拟。
			// 官方脚本高频（61 个文件用），语义与 MAPMOVE 相同。
			s.actMapMove(c, p, f[1:])
		case "break":
			// BREAK：中断本段后续指令（BREAKTIMERECALL 之外的普通中断）。
			// 原版语义是停止执行当前标签的剩余 #act。
			return
		case "mov", "set":
			// MOV <变量> <值> / SET <变量> <值>
			// 官方写法：mov n1 <$STR(n2)>、set n1 0。变量名形如 n1..n99。
			if len(f) >= 3 {
				s.actSetVar(p, f[1], strings.Join(f[2:], " "))
			}
		case "inc", "dec":
			// INC/DEC <变量> [<增量>]：默认 ±1。
			if len(f) >= 2 {
				delta := int64(1)
				if strings.EqualFold(f[0], "dec") {
					delta = -1
				}
				if len(f) >= 3 {
					if v, err := strconv.ParseInt(f[2], 10, 64); err == nil {
						delta = v
						if strings.EqualFold(f[0], "dec") {
							delta = -v
						}
					}
				}
				s.actIncrVar(p, f[1], delta)
			}
		case "sendmsg":
			// SENDMSG <文本>：给玩家发一句系统消息。
			// 官方 QFunction-0.txt 一个文件里用了 11 次。
			if len(f) >= 2 {
				s.sysMsg(c, strings.Join(f[1:], " "))
			}
		// ---- 第二批：技能 / 经验 / 等级 / PK 点 / 刷怪 / 清怪 / 元宝 ----
		case "addskill":
			s.actAddSkill(c, p, f[1:])
		case "delskill", "delnojobskill", "clearskill":
			s.actDelSkill(c, p, f[1:])
		case "skilllevel":
			s.actSkillLevel(c, p, f[1:])
		case "changeexp":
			s.actChangeExp(c, p, f[1:])
		case "changelevel":
			if len(f) >= 2 {
				if lv, err := strconv.Atoi(f[1]); err == nil && lv > 0 {
					s.setPlayerLevel(p, uint32(lv))
				}
			}
		case "changepkpoint", "setpkpoint":
			s.actChangePKPoint(c, p, f[1:])
		case "recallmob", "mongene", "mongenex":
			s.actRecallMob(c, p, f[1:])
		case "clearmapmon", "monclear":
			s.actClearMapMon(c, p, f[1:])
		case "gamegold", "autogamegold":
			s.actGameGold(c, p, f[1:])
		case "gamepoint", "autogetexp":
			s.actGamePoint(c, p, f[1:])
		// ---- 操作位（ITEM 窗口服务端侧，见 scriptitem.go）----
		case "linkbagitem":
			s.actLinkBagItem(c, p, f[1:])
		case "clearlinkitem":
			s.actClearLinkItem(c, p)
		case "getitemfieldvalue":
			s.actGetItemFieldValue(c, p, f[1:])
		case "changeitemdura":
			s.actChangeItemDura(c, p, f[1:])
		case "updateitem":
			s.actUpdateItem(c, p)
		case "addfunitemdura":
			s.actAddFunItemDura(c, p, f[1:])

		case "timerecall":
			s.actTimerRecall(c, p, f[1:], false)
		case "delaygoto":
			s.actTimerRecall(c, p, f[1:], true)
		case "breaktimerecall":
			s.actBreakTimerRecall(c, p)
		case "humanhp", "hum:hp":
			s.actSetHP(c, p, f[1:], true)
		case "humanmp", "hum:mp":
			s.actSetHP(c, p, f[1:], false)
		default:
			log.Printf("脚本指令未实现: %s", line)
		}
	}
}

// actGive 给玩家物品。
func (s *Server) actGive(c net.Conn, p *Player, name string, n int) {
	it := s.data.tables.Items.GetByName(name)
	if it == nil {
		log.Printf("脚本 give：物品表没有 %q", name)
		return
	}
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		ui := &pb.UserItem{
			MakeIndex: int32(s.itemSeq.Add(1)),
			Index:     uint32(it.Index),
			Dura:      initialDura(it),
			DuraMax:   it.DuraMax,
		}
		if s.addToBag(p, ui) < 0 {
			s.sysMsg(c, "背包已满")
			return
		}
	}
	s.sendBagItems(c, p)
	s.sysMsg(c, fmt.Sprintf("获得 %s x%d", it.Name, n))
}

// actTake 从背包扣物品。
func (s *Server) actTake(c net.Conn, p *Player, name string, n int) {
	it := s.data.tables.Items.GetByName(name)
	if it == nil || p.Char.Data == nil {
		return
	}
	if n <= 0 {
		n = 1
	}
	// ⚠️ 先收集槽位、再**从后往前**删：takeBagItem 会把后面的元素整体左移
	//（保持背包无空洞），正序"边遍历边删"会漏掉元素。
	var hits []int
	for i, ui := range p.Char.Data.BagItems {
		if len(hits) >= n {
			break
		}
		if ui == nil || ui.Index != uint32(it.Index) {
			continue
		}
		hits = append(hits, i)
	}
	for k := len(hits) - 1; k >= 0; k-- {
		s.takeBagItem(p, hits[k])
	}
	if len(hits) > 0 {
		s.sendBagItems(c, p)
	}
}

package gamesvr

import (
	"fmt"
	"github.com/algotao/mir2/server/internal/obs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/internal/wire"
	"github.com/algotao/mir2/server/internal/world"
)

// 地图切换与 GM 命令。

// switchMap 把玩家切换到另一张地图的指定坐标。
//
// 包序列：SM_CLEAROBJECTS（丢弃旧实体）→ SM_CHANGEMAP（切图）→ 重建视野。
//
// ⚠️ 顺序不能反：客户端先收到 CHANGEMAP 的话，会拿旧地图的实体去渲染新地图。
func (s *Server) switchMap(c net.Conn, p *Player, mapID string, x, y int) error {
	mp, err := s.world.maps.Get(mapID)
	if err != nil {
		return err
	}
	// 进图门：该图可能要求某个**任务旗标**的值（`NEEDSET_ON(n)`/`NEEDSET_OFF(n)`）。
	// 官方在 `TBaseObject.EnterAnotherMap` 的开头就判这两句（ObjBase.pas:20306-20310），
	// 不满足就 `Exit`（**静默失败**，没有提示）。见 questflag.go。
	if mi := s.mapFlagOf(mp); !needSetAllows(p.Char.Data, mi) {
		want := "OFF"
		if mi.NeedSetOn {
			want = "ON"
		}
		log.Printf("%s 进 %s 被 NEEDSET 门挡下：旗标 [%d] 要求 %s",
			p.Char.Name, mapID, mi.NeedSetID, want)
		return errNeedSetNotMet
	}

	// 该地图的 NPC 要在这里补生成。
	//
	// ⚠️ 原来只在**启动时**对默认地图调了一次 `spawnNPCs`，于是任何"用到才加载"的
	// 地图（`MapManager` 是按需加载的）里**一个 NPC 都没有** —— 玩家传送到铁匠铺
	// 会发现屋里空无一人、所有非默认地图的商人/仓库/铁匠全是隐形的。
	// `spawned` 表保证每张图只生成一次。放在 updateVision 之前，让 NPC 进第一屏。
	s.spawnNPCs(mapID)
	if !mp.InBounds(x, y) {
		return fmt.Errorf("坐标 (%d,%d) 越界（地图 %dx%d）", x, y, mp.Width(), mp.Height())
	}
	// 目标不可走时螺旋搜索最近的可通行格：
	// 传送点配置偶尔落在墙上（尤其用地图中心做默认值时），
	// 直接失败会让 @map 几乎不可用。
	if !mp.CanWalk(x, y) {
		nx, ny, ok := nearestWalkable(mp, x, y, 12)
		if !ok {
			return fmt.Errorf("坐标 (%d,%d) 附近 12 格内没有可通行点", x, y)
		}
		log.Printf("目标 (%d,%d) 不可通行，改用最近可走点 (%d,%d)", x, y, nx, ny)
		x, y = nx, ny
	}

	s.mu.Lock()
	s.world.index.Remove(p)
	p.Obj.SetPos(mp, x, y)
	p.visible.Clear() // 旧视野整体作废，交给 updateVision 重建
	s.world.index.Add(p)
	s.mu.Unlock()

	// 新协议：换图 = **一份新快照**（`ChangeMap`）。
	//
	// ⚠️ legacy 那套三步（先 SM_CLEAROBJECTS 清掉旧对象、再 SM_CHANGEMAP、最后靠
	// updateVision 增量补）在新协议里是多余的：客户端拿到"新地图名 + 整份实体表"
	// 就自然重建了。而且少了"先清空"这一步，也就没有"清空与补发之间闪一下空地图"
	// 那个窗口。
	//
	// ⚠️ `p.visible` 已被上面的 `Clear()` 清空，`sendMapSnapshotTo` 会把它填成
	// 这次快照的集合（"快照即出现"，见那里的注释）。
	if p.protoOut != nil {
		s.activateSpawnMap(mp.Name)
		s.sendMapSnapshotTo(p, mp.Name)
		// "别人看我"那半边仍要走（我这边已由快照填好 ⇒ 不会再发一遍出现）。
		s.updateVision(p)
		p.lastMoveAt = time.Now()
		log.Printf("%s 切换到 %s(%s) (%d,%d)（新协议）",
			p.Char.Name, mapID, s.world.maps.Name(mapID), x, y)
		obs.Event("map_change", "player", p.Char.Name, "map", mapID, "x", x, "y", y)
		return nil
	}

	s.send(c, proto.SM_CLEAROBJECTS, 0, 0, 0, 0, "")
	// ⚠️ 客户端把这条的 Series 也读作 darkness（ClMain.pas:2493）⇒ 原来写死 0 的话
	// 夜里切图会突然变亮。按新地图算（原版 ObjBase.pas:5796 也是 `DayBright()`）。
	s.send(c, proto.SM_CHANGEMAP, int32(p.Obj.ID), uint16(x), uint16(y),
		s.dayBrightOf(mp, time.Now()), s.world.maps.Name(mapID))

	// 新地图的自然怪物按图首次激活；放在 SM_CHANGEMAP 后，避免客户端在切图前收到新图对象。
	s.activateSpawnMap(mp.Name)

	// updateVision 是双向的：新地图的其它玩家会看到我，
	// 旧地图的会收到消失通知，无需在这里额外处理。
	s.updateVision(p)

	// 传送后 3 秒内互相不能打（m_dwMapMoveTick，ObjBase.pas:21322-21323）。
	p.lastMoveAt = time.Now()
	// 换图后区状态与名字颜色都可能变，各刷一次。
	s.sendAreaState(p)
	s.sendNameColor(p, time.Now())
	log.Printf("%s 切换到 %s(%s) (%d,%d)", p.Char.Name, mapID, s.world.maps.Name(mapID), x, y)
	obs.Event("map_change", "player", p.Char.Name, "map", mapID, "x", x, "y", y)
	return nil
}

// nearestWalkable 从 (x,y) 螺旋搜索最近的可通行格。
//
// 只检查每层方环的边框（内部格在更小的半径时已查过），避免 O(r³)。
func nearestWalkable(mp *world.Map, x, y, maxR int) (int, int, bool) {
	if mp.CanWalk(x, y) {
		return x, y, true
	}
	for r := 1; r <= maxR; r++ {
		for dx := -r; dx <= r; dx++ {
			for dy := -r; dy <= r; dy++ {
				if absInt(dx) != r && absInt(dy) != r {
					continue // 只查环的边框
				}
				nx, ny := x+dx, y+dy
				if mp.InBounds(nx, ny) && mp.CanWalk(nx, ny) {
					return nx, ny, true
				}
			}
		}
	}
	return x, y, false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// startPointOf 返回某地图的出生点，没有则 nil。
func (s *Server) startPointOf(mapID string) *data.StartPoint {
	for _, sp := range s.data.startPoints {
		if sp.MapID == mapID {
			return sp
		}
	}
	return nil
}

// 出生点怎么选现在在 `chargen.PickHome`（**两个新手村随机二选一**，见 `newCharHome`）。
// 这里原来有个 `homePointOf`（"从安全点表里挑离配置 Home 最近的一条"）——
// 那是 D-38 的临时做法，D-40 按原版改回随机后已删。

// handleSay 处理 CM_SAY（聊天 / GM 命令）。
func (s *Server) handleSay(c net.Conn, p *Player, pkt wire.Packet) {
	// 地图标记 `NOCHAT`：官方在聊天日志那条链上判它（`ObjBase.pas:8639`
	// `if not (boDisableSayMsg or m_PEnvir.Flag.boNOCHAT)`）⇒ 该图禁止普通聊天。
	// ⚠️ 只拦**聊天**，不拦 `@` 命令（否则该图上的 GM/脚本命令会一起失效）。
	if body := strings.TrimSpace(pkt.Body); body != "" && !strings.HasPrefix(body, "@") {
		if mi := s.mapFlagOf(p.Obj.MapRef()); mi != nil && mi.NoChat {
			return
		}
	}
	msg := strings.TrimSpace(pkt.Body)
	if msg == "" {
		return
	}
	// 行会聊天：客户端"行会聊天"按钮发送 '!~' + 文本（ClMain.pas:2307-2310）
	if strings.HasPrefix(msg, "!~") {
		s.handleGuildChat(p, strings.TrimSpace(msg[len("!~"):]))
		return
	}
	// GM 命令以 '@' 开头
	if strings.HasPrefix(msg, "@") {
		// ⚠️ **权限门**：原版每条命令都带 `nPermissionMin`（Command.ini，
		// 如 Map=3 / Level=10 / GameMaster=10），`m_btPermission` 不够就走
		// 同一个拒绝分支，回一句"此命令不正确，或没有足够的权限！！！"
		// （ObjBase.pas:8591；权限本身来自 Envir/AdminList.txt）。
		//
		// 我们此前**完全不校验** ⇒ 任何玩家都能 @give 屠龙 / @level 60。
		fields := strings.Fields(msg[1:])
		if len(fields) == 0 {
			return
		}
		if !s.cfg.gmOpen && p.permission == 0 {
			s.sysMsg(c, "@"+fields[0]+" 此命令不正确，或没有足够的权限！！！")
			log.Printf("%s 使用 GM 命令被拒（不在 AdminList）: %s", p.Char.Name, msg)
			return
		}
		s.handleGMCommand(c, p, msg[1:])
		return
	}
	// 其余就是聊天（普通 / `/` 私聊 / `!!` 组队 / `!` 喊话）。
	// 原版这里是 ProcessSayMsg（带频道前缀分派与反刷屏限流），见 chat.go。
	s.handleChat(c, p, msg)
}

// handleGMCommand 处理一条 GM 命令。
//
// 权限门在 handleSay 里（原版每条命令的 nPermissionMin 见 Command.ini；
// 权限等级来自 Envir/AdminList.txt）。
func (s *Server) handleGMCommand(c net.Conn, p *Player, line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	switch strings.ToLower(fields[0]) {
	case "map":
		s.gmMap(c, p, fields[1:])
	case "spawn":
		s.gmSpawn(c, p, fields[1:])
	case "die":
		// 模拟死亡，用于验证回城流程（调试用）
		if p.Char.Data != nil && p.Char.Data.Abil != nil {
			// ⚠️ 这是**漏网**的一处裸写：上一轮自检的 grep 模式太窄（要求
			// `Abil.Hp =` / `abil.Hp =` 这类前缀），`p.Char.Data.Abil.Hp = 0` 没被扫到。
			p.setHP(0)
			// 复活戒指要拦在所有"致死"之前（原版在 Run 里对所有致死途径都判）；
			// 这条 GM 路径也是致死途径之一，漏了它 e2e 就没法确定性地验戒指。
			if s.tryRevivalRing(p) {
				return
			}
			s.revive(c, p, nil)
		}
	case "setflag":
		// 任务旗标（原版 SETFLAG 命令，ObjBase.pas:8290-8305）
		s.gmSetFlag(c, p, fields[1:])
	case "getflag":
		// 查询旗标（原版那条 show-human-flag 命令，:11762-11780）
		s.gmGetFlag(c, p, fields[1:])
	case "reconnection":
		// 换网关（原版 `CmdReconnection`，权限 ≥ 6）
		s.gmReconnection(c, p, fields[1:])
	case "gold":
		if len(fields) < 2 {
			s.sysMsg(c, "用法: @gold <数量>")
			return
		}
		if v, err := strconv.Atoi(fields[1]); err == nil && v >= 0 && p.Char.Data != nil {
			n := p.setGold(int64(v))
			s.send(c, proto.SM_GOLDCHANGED, int32(n), 0, 0, 0, "")
			s.sysMsg(c, fmt.Sprintf("金币设为 %d", v))
			log.Printf("%s 金币设为 %d（GM）", p.Char.Name, v)
		}
	case "level":
		s.gmLevel(c, p, fields[1:])
	case "magic", "skill":
		s.gmMagicLevel(c, p, fields[1:])
	case "give":
		s.gmGive(c, p, fields[1:])
	case "maps":
		// 列出已知地图（前 20 个）
		var b strings.Builder
		n := 0
		for _, mi := range s.data.mapInfos {
			if n >= 20 {
				break
			}
			fmt.Fprintf(&b, "%s=%s ", mi.ID, mi.Name)
			n++
		}
		s.sysMsg(c, "地图: "+b.String())

	// ---- 行会命令（原版命令名与默认值见 M2Share.pas:2730-2742）----
	case "letguild":
		s.cmdLetGuild(c, p)
	case "endguild":
		s.cmdEndGuild(c, p)
	case "pkdie":
		// 调试用：打开/关闭 PK 死亡的四个奖惩开关（原版从 !setup.txt 读，
		// 出厂全关）。e2e 用它把开关打开来验奖惩真的落地。
		//	@pkdie winlevel on|off [点数]   赢家升级（含"输家掉级"的嵌套开关）
		//	@pkdie lostlevel on|off [点数]
		//	@pkdie winexp on|off [点数]
		//	@pkdie lostexp on|off [点数]
		s.gmPKDie(c, p, fields[1:])
	case "searchhuman":
		// 探测项链（Shape 121）开放的命令，原版 TPlayObject.CmdSearchHuman（:14356）。
		// 没戴项链时什么都不做（原版那半边分支根本不可达）。
		s.cmdSearchHuman(p, fields[1:])
	case "move":
		// 传送戒指（Shape 112）开放的命令，原版 TPlayObject.CmdUserMoveXY（:15195）。
		// 没戴戒指时**什么都不做**（原版那个分支就是空的）。
		s.cmdUserMove(p, fields[1:])
	case "authally":
		s.cmdAuthAlly(c, p)
	case "banguildchat":
		s.cmdBanGuildChat(c, p)
	case "联盟":
		// ⚠️ 这是**测试捷径**：原版客户端点"结盟"按钮发的是 CM_GUILDALLY(1044)，
		// 见 handleGuildMsg。两者走同一个 cmdAlly。
		s.cmdAlly(c, p)
	case "取消联盟":
		s.cmdBreakAlly(c, p, fields[1:])
	case "guild":
		s.cmdGuild(c, p, fields[1:])

	// ---- 城堡命令（原版对应 ObjBase.pas 的 CmdChangeSabukLord 等）----
	case "castle":
		s.cmdCastle(c, p, fields[1:])

	// ---- 行会争霸赛（原版 CmdStartContest / CmdEndContest / CmdContestPoint）----
	// 命令名取自官方 Command.ini（三个都是权限 10）。
	case "startcontest":
		s.cmdStartContest(c, p, fields[1:])
	case "endcontest":
		s.cmdEndContest(c, p, fields[1:])
	case "contestpoint":
		s.cmdContestPoint(c, p, fields[1:])

	// ---- PvP 命令（对应 ObjBase.pas 的 CmdChangeAttackMode / INCPKPOINT）----
	case "atkmode":
		s.cmdAttackMode(c, p, fields[1:])
	case "pk":
		s.cmdPKPoint(c, p, fields[1:])

	// ---- 宠物命令（对应 ObjBase.pas:10988 CmdChangeSalveStatus）----
	case "slave", "宠物":
		s.cmdSlaveStatus(c, p)

	// ---- 交易命令（对应 ObjBase.pas:7639-7645 的 @LetTrade）----
	case "lettrade":
		s.cmdLetTrade(c, p)

	// ---- 组队命令（对应 ObjBase.pas:7677-7686 的命令分发）----
	case "allowgrouprecall":
		s.cmdAllowGroupRecall(c, p)
	case "grouprecall":
		s.cmdGroupRecall(c, p)

	default:
		s.sysMsg(c, "未知命令 @"+fields[0]+
			"（可用: @map <地图号> [x] [y], @maps, @guild ..., @castle info）")
	}
}

// gmMap 处理 `@map <地图号> [x] [y]`。
func (s *Server) gmMap(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @map <地图号> [x] [y]")
		return
	}
	id := args[0]

	// 未给坐标时用该地图的出生点
	x, y := -1, -1
	if len(args) >= 3 {
		x, _ = strconv.Atoi(args[1])
		y, _ = strconv.Atoi(args[2])
	}
	if x < 0 || y < 0 {
		if sp := s.startPointOf(id); sp != nil {
			x, y = sp.X, sp.Y
		} else {
			// 没有出生点就用地图中心（switchMap 会就近修正到可走格）
			if mp, err := s.world.maps.Get(id); err == nil {
				x, y = mp.Width()/2, mp.Height()/2
			} else {
				s.sysMsg(c, "传送失败: "+err.Error())
				return
			}
		}
	}

	if err := s.switchMap(c, p, id, x, y); err != nil {
		s.sysMsg(c, "传送失败: "+err.Error())
		return
	}
	s.sysMsg(c, fmt.Sprintf("已传送到 %s(%s) (%d,%d)", s.world.maps.Name(id), id, x, y))
}

// gmSpawn 处理 `@spawn <怪物名> [数量]`：在玩家身旁刷怪。
//
// 主要用于让端到端测试场景可控——真实地图的怪很分散，
// 测试若依赖"视野里恰好有只近处的怪"就会时好时坏。
func (s *Server) gmSpawn(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @spawn <怪物名> [数量]")
		return
	}
	info := s.data.tables.Monsters.GetByName(args[0])
	if info == nil {
		s.sysMsg(c, "怪物表没有: "+args[0])
		return
	}
	n := 1
	if len(args) >= 2 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 20 {
		n = 20
	}

	// ring 是环绕玩家的 8 个方向，按圈数向外铺开。
	ring := [8][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}

	done := 0
	for i := 0; i < n; i++ {
		// ⚠️ 两点：
		// 1. 不能刷在玩家脚下——攻击判定的是相邻格，同格永远打不到
		// 2. 必须刷在**玩家手边**：原来起点是 (X+1, Y+i)，第 20 只离玩家
		//    19 格远，用例得跑一趟图（每步 400ms 限流）才打得到——
		//    attack 用例有一半时间耗在这段路上。现在按 8 方向一圈圈往外铺，
		//    n 只怪都落在 3 格内。
		r := i/8 + 1
		dx, dy := ring[i%8][0]*r, ring[i%8][1]*r
		// 找落脚点在锁内做：要读空间索引判占用（见 freeCellNearLocked）。
		s.mu.Lock()
		x, y, ok := s.freeCellNearLocked(p.Obj.MapRef(), p.Obj.PosX()+dx, p.Obj.PosY()+dy, 2, p.Obj.PosX(), p.Obj.PosY())
		if !ok {
			s.mu.Unlock()
			continue
		}
		mon := entity.NewMonster(proto.MonsterIDBase+s.world.monsterSeq.Add(1), info, p.Obj.MapRef(), x, y)
		s.world.monsters[mon.ID] = mon
		s.world.monsterIdx.Add(mon)
		s.mu.Unlock()

		s.broadcastToViewers(mon.MapRef(), mon.PosX(), mon.PosY(), func(other *Player) {
			s.sendMonsterAppear(other, mon)
		})
		done++
	}
	s.sysMsg(c, fmt.Sprintf("生成 %s x%d", args[0], done))
	log.Printf("%s 生成 %s x%d（GM）", p.Char.Name, args[0], done)
}

// freeCellNearLocked 从 (x,y) 螺旋找附近**能放新对象**的格：可通行 + 没被占用。
// 跳过 avoidX/avoidY 那一格（通常是玩家自己脚下）。**调用方持 s.mu**。
//
// ⚠️ 光查 CanWalk 是不够的：原版地图每格只容一个对象
// （AddToMap / MoveToMovingObject 会失败），所以两只怪不可能同格。
// 我们若允许同格，`findMonsterAt` 从同格里返回哪一只就变成**看 Go map 遍历顺序**
// —— 表现为"圣言术偶尔打中了同格的鸡（非不死系）⇒ SM_MAGICFIRE_FAIL"
// 这种间歇性怪事（e2e `skill2-undead` 偶发的根因）。
func (s *Server) freeCellNearLocked(mp *world.Map, x, y, maxR, avoidX, avoidY int) (int, int, bool) {
	for r := 0; r <= maxR; r++ {
		for dx := -r; dx <= r; dx++ {
			for dy := -r; dy <= r; dy++ {
				if absInt(dx) != r && absInt(dy) != r {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx == avoidX && ny == avoidY {
					continue
				}
				if mp.InBounds(nx, ny) && mp.CanWalk(nx, ny) && s.cellFreeLocked(mp, nx, ny) {
					return nx, ny, true
				}
			}
		}
	}
	return x, y, false
}

// cellFreeLocked 判断 (x,y) 能否放一个新对象：可通行 + 没有别的怪/玩家占着。
//
// **调用方持 s.mu**（空间索引本身无锁，读它必须持锁）。
// 与 warrskill.go 的 cellOccupied 是同一件事的两个视角：
// 那个用于"推人时看目标格有没有东西"，这个用于"刷怪时找落脚点"。
func (s *Server) cellFreeLocked(m *world.Map, x, y int) bool {
	if m == nil || !m.CanWalk(x, y) {
		return false
	}
	for _, o := range s.world.monsterIdx.InRange(x, y, 0) {
		// 尸体不算障碍（原版尸骨在图上但不挡下一个对象生成）
		if e, ok := o.(*entity.Monster); ok && !e.IsDead() &&
			e.MapRef() == m && e.PosX() == x && e.PosY() == y {
			return false
		}
	}
	for _, o := range s.world.index.InRange(x, y, 0) {
		if e, ok := o.(*Player); ok && e.Obj.MapRef() == m && e.Obj.PosX() == x && e.Obj.PosY() == y {
			return false
		}
	}
	return true
}

// gmLevel 处理 `@level <等级>`：直接设定人物等级（测试用）。
// gmMagicLevel 处理 `@magic <技能名|技能号> <等级>`：直接改技能等级。
//
// 与 @level/@give 同类的测试便利命令。存在的理由很实际：
// 概率技能（诱惑之光 20）在 0 级时单次成功率只有 1/4×1/2×1/2 = 6%，
// e2e 要跑几十次才碰得到一次；调到 3 级后第一道门 `Random(4-3)=Random(1)`
// 恒过（Magic.pas:783），单次成功率升到 25%，20 次足够稳定。
//
// 不落盘语义之外的东西：只改内存里的 UserMagic.Level，与训练点无关。
func (s *Server) gmMagicLevel(c net.Conn, p *Player, args []string) {
	if len(args) < 2 {
		s.sysMsg(c, "用法: @magic <技能名|技能号> <等级 0-3>")
		return
	}
	if p.Char == nil || p.Char.Data == nil {
		return
	}
	// 技能号或名字都能定位
	var id uint32
	if n, err := strconv.Atoi(args[0]); err == nil {
		id = uint32(n)
	} else if info := s.data.tables.Magics.GetByName(args[0]); info != nil {
		id = uint32(info.MagicID)
	} else {
		s.sysMsg(c, "技能表里没有 "+args[0])
		return
	}
	lv, err := strconv.Atoi(args[1])
	if err != nil || lv < 0 || lv > entity.MagicMaxLevel {
		s.sysMsg(c, fmt.Sprintf("技能等级范围 0~%d", entity.MagicMaxLevel))
		return
	}
	for _, um := range p.Char.Data.Magics {
		if um != nil && um.MagicId == id {
			um.Level = uint32(lv)
			s.sendMyMagics(c, p)
			s.sysMsg(c, fmt.Sprintf("技能 %d 等级设为 %d", id, lv))
			log.Printf("%s 的技能 %d 等级设为 %d（GM）", p.Char.Name, id, lv)
			return
		}
	}
	s.sysMsg(c, "该角色没有这个技能")
}

func (s *Server) gmLevel(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @level <等级>")
		return
	}
	lv, err := strconv.Atoi(args[0])
	if err != nil || lv < 1 || lv > int(entity.MaxLevel) {
		s.sysMsg(c, fmt.Sprintf("等级范围 1~%d", entity.MaxLevel))
		return
	}
	if p.Char.Data == nil || p.Char.Data.Abil == nil {
		return
	}
	ab := p.Char.Data.Abil
	// 补上升级带来的属性成长，否则高等级仍是新手血量
	for l := uint32(ab.Level) + 1; l <= uint32(lv); l++ {
		hp, mp := entity.GrowthFor(l)
		ab.MaxHp += hp
		ab.MaxMp += mp
	}
	ab.Level = uint32(lv)
	ab.Exp = entity.NeedExp(uint32(lv))
	ab.Hp, ab.Mp = ab.MaxHp, ab.MaxMp

	s.sendHealthChanged(p, p.Obj.ID, ab.Hp, ab.Mp, ab.MaxHp)
	// 下发前重算负重：背包/装备的任何变动都会改 Weight，而它只能靠这条包告诉客户端
	s.applyWeights(p)
	abRaw := abilityFromPB(ab).Bytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(p.Char.Data.Job), 0, 0, string(abRaw[:]))
	s.sendSubAbility(c, p)
	s.sysMsg(c, fmt.Sprintf("等级设为 %d", lv))
	log.Printf("%s 等级设为 %d（GM）", p.Char.Name, lv)
}

// gmGive 处理 `@give <物品名> [数量]`。
func (s *Server) gmGive(c net.Conn, p *Player, args []string) {
	if len(args) == 0 {
		s.sysMsg(c, "用法: @give <物品名> [数量]")
		return
	}
	name := args[0]
	n := 1
	if len(args) >= 2 {
		if v, err := strconv.Atoi(args[1]); err == nil && v > 0 {
			n = v
		}
	}
	tmpl := s.data.tables.Items.GetByName(name)
	if tmpl == nil {
		s.sysMsg(c, "物品表没有: "+name)
		return
	}
	got := 0
	for i := 0; i < n; i++ {
		it := &pb.UserItem{
			MakeIndex: int32(s.itemSeq.Add(1)),
			Index:     uint32(tmpl.Index),
			Dura:      initialDura(tmpl),
			DuraMax:   tmpl.DuraMax,
		}
		if s.addToBag(p, it) < 0 {
			s.sysMsg(c, "背包已满")
			break
		}
		got++
	}
	s.sendBagItems(c, p)
	// 背包负重变了 ⇒ 属性包也要重发（客户端负重条读它）
	s.sendAbility(c, p)
	s.sysMsg(c, fmt.Sprintf("获得 %s x%d", name, got))
	log.Printf("%s 通过 GM 命令获得 %s x%d", p.Char.Name, name, got)
}

// sysMsg 发一条系统消息（SM_SYSMESSAGE=100）。
func (s *Server) sysMsg(c net.Conn, msg string) {
	s.send(c, proto.SM_SYSMESSAGE, 0, 0, 0, 0, msg)
}

// resolveMapDir 在给定地图目录**不存在**时，按"客户端资产那套"的老规矩再找一遍。
//
// 候选顺序（先环境变量、再兄弟 checkout；相对路径按**进程工作目录**算，
// 所以 `../` 与 `../../` 两种深度都列上 —— 有人从仓库根跑、有人从 server/ 跑）：
//
//	$MIR2_MAP_DIR                         显式指定，优先
//	$(dirname $MIR2C_DATA)/map             客户端资产旁边那套地图（D-22 的权威集）
//	../mir2c/map, ../../mir2c/map          客户端自带
//	../mir2go/data/map, ../../mir2go/...   参照服务端自带
//
// 都不存在就**原样返回**（照旧由调用方打告警），不做静默兜底 —— 悄悄换目录会让人
// 以为在看 A 图，实际是 B 图。
func resolveMapDir(dir string) string {
	if isDir(dir) {
		return dir
	}
	var cands []string
	if v := os.Getenv("MIR2_MAP_DIR"); v != "" {
		cands = append(cands, v)
	}
	if v := os.Getenv("MIR2C_DATA"); v != "" {
		cands = append(cands, filepath.Join(filepath.Dir(v), "map"))
	}
	for _, up := range []string{"..", filepath.Join("..", "..")} {
		cands = append(cands,
			filepath.Join(up, "mir2c", "map"),
			filepath.Join(up, "mir2go", "data", "map"))
	}
	for _, c := range cands {
		if isDir(c) {
			return c
		}
	}
	return dir
}

// isDir 判断路径是不是存在的目录。
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

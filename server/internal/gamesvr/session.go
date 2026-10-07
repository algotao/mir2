package gamesvr

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/netgate"
	"github.com/algotao/mir2/server/internal/obs"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/proxyproto"
	"github.com/algotao/mir2/server/internal/storage"
	"github.com/algotao/mir2/server/internal/tscale"
	"github.com/algotao/mir2/server/internal/wire"
)

// acceptConn 是接入侧的**第一段**：先按 -proxy-protocol 的约定取到可信的客户端地址，
// 再把（已剥掉 PROXY 头的）连接交给 handleConn。
//
// 为什么不并进 handleConn：handleConn 的契约是"一条已经在按协议说话的连接"，
// 而 PROXY 头属于**协议之前**的事。混在一起之后，"这条连接读到哪儿了"就没人讲得清。
func (s *Server) acceptConn(c net.Conn) {
	ec, clientIP, err := proxyproto.ServerConn(c, s.cfg.proxyProtocol, proxyHeaderTimeout)
	if err != nil {
		// 缺头/头非法 ⇒ 直接断开，**不退回 socket 地址**：
		// 那会让"必须带头"变成一句空话（能直连到 gamesvr 的人，写不写头都行）。
		log.Printf("%s: 取得客户端地址失败，断开: %v", c.RemoteAddr(), err)
		_ = c.Close()
		return
	}
	s.handleConn(ec, clientIP)
}

// handleConn 是接入侧的**第二段**：连接已就位（PROXY 头已剥离），开始按协议收发。
//
// clientIP 是**可信的**客户端地址（形如 `1.2.3.4:51234`）：直连模式下取 socket 对端，
// 经网关转发时来自 PROXY 头（见 internal/proxyproto 与 docs/decisions.md 的 D-23）。
func (s *Server) handleConn(c net.Conn, clientIP string) {
	defer c.Close()

	sp := wire.NewSplitter(maxFrameLen)
	var player *Player

	// ---- 收包架构：读协程（持续收、持续拆）→ 队列 → 派发循环 ----
	//
	// 原版见 netgate.go 的文件头。为什么必须拆成两半：
	//  1. 派发可能被**门禁延时**挡住（攻击 520ms / 施法 450ms，官方 !setup.txt）
	//     —— 若在读循环里直接 sleep，这段时间 socket 没人读，对方的包全堆在内核
	//     缓冲区里（表现为"客户端感觉卡住"）；
	//  2. 处理一条慢消息（比如跨图）也不该挡住后面几条已经到达的包。
	//
	// 队列里放的是**帧载荷**（不是解析好的包）：认证首包不是 TDefaultMessage，
	// 它也要按 FIFO 走 —— 否则"读协程已经读到游戏消息、派发循环还没认证"会乱序。
	type inMsg struct {
		payload []byte
		late    bool // 补投（已等过门禁间隔，不再重复判定）
	}
	const inboxSize = 256
	inbox := make(chan inMsg, inboxSize)
	done := make(chan struct{})
	defer close(done)

	go func() {
		buf := make([]byte, 4096)
		for {
			_ = c.SetReadDeadline(time.Now().Add(readTimeout))
			n, err := c.Read(buf)
			if err != nil {
				close(inbox)
				return
			}
			sp.Feed(buf[:n])
			for {
				raw, err := sp.Next()
				if err != nil {
					break
				}
				f, err := wire.DecodeFrame(raw)
				if err != nil || wire.IsKeepAlive(f.Payload) {
					continue
				}
				payload := make([]byte, len(f.Payload))
				copy(payload, f.Payload)
				select {
				case inbox <- inMsg{payload: payload}:
				case <-done:
					return
				}
			}
		}
	}()

	gate := netgate.NewGate()

	// ---- 存档快照的投递通道（自动存档用）----
	//
	// 角色状态的写者只有本 goroutine ⇒ 与其让存档线程去读（那就是 data race），
	// 不如把"做快照"这件事投到这里来做：快照天然一致，不需要给所有字段加锁。
	// 容量 1 + 回复通道容量 1 ⇒ 请求方超时放弃时两边都不会永久阻塞。
	snapReq := make(chan chan *storage.Character, 1)

	for {
		var msg inMsg
		select {
		case m, ok := <-inbox:
			if !ok {
				// 读协程收尾（断开/超时）—— 与原 `for range` 走完等价
				return
			}
			msg = m
		case reply := <-snapReq:
			reply <- saveSnapshotOf(player) // player 尚未认证时为 nil ⇒ 存档线程跳过
			continue
		}

		// 认证首包
		if player == nil {
			tok, err := wire.ParseLoginToken(msg.payload)
			if err != nil {
				log.Printf("%s: 认证首包非法: %v", clientIP, err)
				s.send(c, proto.SM_OUTOFCONNECTION, 0, 0, 0, 0, "")
				return
			}
			// ⚠️ 客户端 IP **只能**来自连接本身（PROXY 头，或直连时的 socket 对端）。
			// 旧协议把它拼在登录 token 末尾（`idx|<IP>`），那是**客户端自报** ——
			// 谁都能自称 1.2.3.4。那条路已随 D-23 删除，见 internal/proxyproto。
			p, err := s.authenticate(tok, clientIP)
			if err != nil {
				log.Printf("%s: 认证失败: %v", clientIP, err)
				s.send(c, proto.SM_OUTOFCONNECTION, 0, 0, 0, 0, "")
				return
			}
			player = p
			player.conn = c
			// ⚠️ 在**注册进 world.players 之前**挂上快照通道：之后自动存档线程
			// 会读它（在 s.mu 下取出玩家列表后再用），这次赋值与那次读之间由
			// s.mu 建立 happens-before。
			player.snapReq = snapReq
			player.permission = s.cfg.adminList.LevelFor(p.Char.Name, clientIP)
			if player.permission > 0 {
				log.Printf("%s 的 GM 权限等级 = %d", p.Char.Name, player.permission)
			}
			log.Printf("%s: %s 进游戏 (ActorId=%d)", clientIP, p.Char.Name, p.Obj.ID)

			s.mu.Lock()
			s.world.players[p.Obj.ID] = p
			s.world.index.Add(p)
			s.mu.Unlock()
			defer s.removePlayer(p)

			s.sendNotice(c, p)
			continue
		}

		if player != nil && player.revoked.Load() {
			return
		}
		pkt, err := wire.DecodePacket(msg.payload)
		if err != nil {
			continue
		}

		// ---- 门禁：间隔不足就延时投递，连续超速就告警 ----
		//
		// 对应原版 `ClientHitXY`（ObjBase.pas:8760-8800）的间隔判定与调用处的
		// `SendDelayMsg`（:4710）。补投的消息（late）不再判定，与原版
		// `if not boLateDelivery` 一致。
		if !msg.late {
			interval := tscale.D(netgate.IntervalOf(pkt.Head.Ident))
			// 攻击族：间隔还要减去"攻速 × ItemSpeedTime"（原版 ClientHitXY:8774）
			if netgate.IsAttackIdent(pkt.Head.Ident) {
				interval = netgate.AttackIntervalFor(s.playerHitSpeed(player))
			}
			if gap, overspeed := gate.Decide(pkt.Head.Ident, time.Now(), interval); gap > 0 {
				if overspeed {
					// 告警后清零，与原版一致（`m_dwAttackCount := 0`）
					gate.ResetCount(pkt.Head.Ident)
					netgate.LogOverSpeed(player.Char.Name, pkt.Head.Ident, gap, netgate.OverSpeedKickCount)
				}
				// 延时到点再投（期间读协程照常把后面的包收进队列）
				select {
				case <-time.After(gap):
				case <-done:
					return
				}
			}
		}

		if player.revoked.Load() {
			return
		}
		// Fail closed if this account lease was superseded or expired between ticks.
		leaseCtx, leaseCancel := context.WithTimeout(context.Background(), time.Second)
		current, leaseErr := s.store.Sessions().IsCurrent(leaseCtx, player.Char.Account, player.sessionID, time.Now())
		leaseCancel()
		if leaseErr != nil || !current {
			player.revoked.Store(true)
			return
		}
		// ⚠️ 每个包单独 recover：一条畸形包（或一个空指针 bug）不该把整个
		// 游戏服带走——那会让**所有**在线玩家瞬间掉线，且现场被冲掉。
		if err := s.dispatch(c, player, pkt); err != nil {
			log.Printf("%s: 处理消息 %d 出错: %v", player.Char.Name, pkt.Head.Ident, err)
		}
	}
}

// dispatch 是 handleGameMsg 的 panic 包装。
func (s *Server) takeoverLocalPlayer(next *Player) bool {
	if next == nil || next.Char == nil {
		return true
	}
	s.mu.RLock()
	var old *Player
	for _, candidate := range s.world.players {
		if candidate != next && candidate.sessionID != next.sessionID &&
			candidate.Char != nil && candidate.Char.Name == next.Char.Name {
			old = candidate
			break
		}
	}
	s.mu.RUnlock()
	if old == nil {
		return true
	}
	old.revoked.Store(true)
	if old.conn != nil {
		_ = old.conn.Close()
	}
	if old.cleanupDone != nil {
		select {
		case <-old.cleanupDone:
			return true
		case <-time.After(gameLeaseWait):
			log.Printf("等待旧角色 %s 清理超时，拒绝重复接入", old.Char.Name)
			return false
		}
	}
	return true
}

func (s *Server) dispatch(c net.Conn, p *Player, pkt wire.Packet) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	s.handleGameMsg(c, p, pkt)
	return nil
}
func (s *Server) authenticate(tok *wire.LoginToken, ip string) (*Player, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gameLeaseWait+5*time.Second)
	defer cancel()

	now := time.Now()
	rec, err := s.store.Sessions().Get(ctx, tok.SessionID)
	if err != nil {
		return nil, fmt.Errorf("会话 %d 不存在", tok.SessionID)
	}
	if rec.Account == "" || rec.Account != tok.Account || rec.Stage != 4 ||
		rec.CharacterName != tok.ChrName || !rec.ExpiresAt.After(now) {
		return nil, fmt.Errorf("会话未选定角色或已过期")
	}
	current, err := s.store.Sessions().IsCurrent(ctx, tok.Account, tok.SessionID, now)
	if err != nil || !current {
		return nil, fmt.Errorf("会话已被新登录接管")
	}
	chr, err := s.store.Characters().GetByName(ctx, tok.ChrName)
	if err != nil {
		return nil, fmt.Errorf("角色 %s 不存在", tok.ChrName)
	}
	if chr.Account != tok.Account || chr.Deleted {
		return nil, fmt.Errorf("角色不属于该账号")
	}

	// 同进程内先主动关闭同角色旧连接；跨进程连接通过 account_sessions
	// generation 失效，在 sessionLeaseLoop 中关闭。两种情况都等旧连接清理完成，
	// 再让 ClaimGameLease 获取角色写租约。
	if !s.takeoverLocalPlayer(&Player{Char: chr, sessionID: tok.SessionID}) {
		return nil, fmt.Errorf("等待本地旧角色连接清理超时")
	}
	// 新登录先让旧 gamesvr 发现 account_sessions 的新 SID；旧连接会在租约
	// 轮询中关闭、完成最终存档并释放 game_leases。只在旧租约释放/过期后才接管。
	deadline := time.Now().Add(gameLeaseWait)
	for {
		claimed, err := s.store.Sessions().ClaimGameLease(
			ctx, tok.Account, tok.SessionID, tok.ChrName, time.Now(), time.Now().Add(gameLeaseDuration))
		if err != nil {
			return nil, fmt.Errorf("取得游戏租约失败: %w", err)
		}
		if claimed {
			break
		}
		current, err = s.store.Sessions().IsCurrent(ctx, tok.Account, tok.SessionID, time.Now())
		if err != nil || !current {
			return nil, fmt.Errorf("进入游戏凭证已失效")
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("账号仍在其他游戏连接中")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 旧连接释放租约前完成最终存档；取得租约后重新读取角色，避免使用早期快照。
	chr, err = s.store.Characters().GetByName(ctx, tok.ChrName)
	if err != nil || chr.Account != tok.Account || chr.Deleted {
		_ = s.store.Sessions().ReleaseGameLease(ctx, tok.Account, tok.SessionID, tok.ChrName)
		return nil, fmt.Errorf("角色 %s 不存在或不可用", tok.ChrName)
	}
	m := s.world.defaultMap
	if mm, err := s.world.maps.Get(chr.Data.CurMap); err == nil {
		m = mm
	} else {
		log.Printf("角色 %s 的地图 %s 加载失败（%v），回退到默认地图",
			chr.Name, chr.Data.CurMap, err)
	}
	s.activateSpawnMap(m.Name)
	x, y := int(chr.Data.CurX), int(chr.Data.CurY)
	if !m.CanWalk(x, y) {
		x, y = m.Width()/2, m.Height()/2
	}

	// ⚠️ `entity.Object` 的可变态不导出（自带锁，见 entity/object.go），
	// 跨包构造统一走 `entity.NewObject`。
	obj := entity.NewObject(s.world.actorSeq.Add(1), chr.Name, m, x, y,
		uint8(chr.Data.Dir), proto.MakeFeature(0, 0, uint8(chr.Data.Hair), 0))
	return &Player{
		Obj:         obj,
		Char:        chr,
		Limiter:     entity.NewMoveLimiter(),
		IP:          ip,
		sessionID:   tok.SessionID,
		cleanupDone: make(chan struct{}),
		visible:     entity.NewViewTracker(),
		// 原版默认值：不允许被加进行会（ObjBase.pas:1271），
		// 但允许接收行会聊天（ObjBase.pas:1329）。
		allowGuild:   false,
		banGuildChat: true,
	}, nil
}
func (s *Server) removePlayer(p *Player) {
	if p == nil || !p.cleanupOnce.CompareAndSwap(false, true) {
		return
	}
	if p.cleanupDone != nil {
		defer p.cleanupDoneOnce.Do(func() { close(p.cleanupDone) })
	}
	// ⚠️⚠️ 取消交易与退组必须在**取 s.mu 之前**做。
	//
	// 这两个都会自己拿锁（dealCancel→partnerOf、groupDrop→groups.LeaderOf），
	// 而 sync.RWMutex **不可重入** —— 放进临界区就是自死锁。
	// 症状特别隐蔽：第一个玩家正常，之后每个新连接都卡在"进入游戏"处
	// i/o timeout（gamesvr 整体卡在 s.mu 上，监听还在但认证拿不到锁），
	// 看起来像"服务端挂了"而不是"这里死锁了"。
	//
	// 顺序上没问题：两者只靠 p.Obj.ID / p.dealPartner 找关系，不依赖 p
	// 还在 s.world.players 里；而下面 delete 之前的 releaseSlaves 仍能看到它。
	// 对应原版：ObjBase.pas:15470-15478（Disappear 里 DelMember）与
	// UsrEngn.pas:987（掉线时 DealCancelA）。
	s.dealCancelA(p)
	s.groupDrop(p)

	// 消失包用的坐标先取（本人 goroutine，无需锁）
	px, py := p.Obj.PosX(), p.Obj.PosY()
	var viewers []*Player

	s.mu.Lock()
	// ⚠️ 必须在 delete(s.world.players, ...) **之前**：releaseSlaves 还要用
	// p.Obj.ID 去 s.world.monsters 里认领这些兽。不回收的话它们会变成孤儿——
	// 继续被 AI 驱动、继续主动攻击路人、还永久占着主人的宠物数量上限。
	gone := s.releaseSlaves(p)
	delete(s.world.players, p.Obj.ID)
	s.world.index.Remove(p)
	for _, other := range s.world.players {
		if other.visible.Remove(p.Obj.ID) {
			viewers = append(viewers, other)
		}
	}
	s.mu.Unlock()

	// ⚠️ 审计 P1-6：这两条 SM_DISAPPEAR 以前是在 `s.mu` 里发的 ——
	// 一个假死客户端就能把世界锁按住。改成锁内收集、锁外发。
	for _, other := range viewers {
		s.sendDisappear(other, p.Obj.ID, px, py)
	}

	// 宠物的下线包（锁外发）
	for _, m := range gone {
		s.broadcastToViewers(m.MapRef(), m.PosX(), m.PosY(), func(o *Player) {
			if o.visible.Remove(m.ID) {
				s.send(o.conn, proto.SM_DISAPPEAR, int32(m.ID),
					uint16(m.PosX()), uint16(m.PosY()), 0, "")
			}
		})
	}
	if n := len(gone); n > 0 {
		log.Printf("%s 下线，回收 %d 只宠物", p.Char.Name, n)
	}

	// 旧玩家在释放租约前完成最终存档；新 gamesvr 会等待租约释放后再加载角色。
	s.savePlayer(p)
	if p.sessionID != 0 && p.Char != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.store.Sessions().ReleaseGameLease(ctx, p.Char.Account, p.sessionID, p.Char.Name); err != nil {
			log.Printf("%s 释放游戏会话失败: %v", p.Char.Name, err)
		}
		cancel()
	}
}

// sendEnterWorld 发送进入世界所需的包序列。
//
// ⚠️ 顺序不能乱：客户端在 SM_LOGON 里才创建"自己"的 Actor，
// 而 SM_ABILITY 的处理直接引用 g_MySelf（ClMain.pas:4794）。
func (s *Server) sendEnterWorld(c net.Conn, p *Player) {
	o := p.Obj
	d := p.Char.Data

	// 补生成该地图的 NPC（同 switchMap 里的理由）：存档在图上的玩家**不走切图**，
	// 只靠 switchMap 那一处的话，他会登进一张"没有商人/仓库/铁匠"的地图。
	s.spawnNPCs(o.MapRef().Name)

	// 亮度按 `DayBright()` 算（地图 DARK/DAYLIGHT + 昼夜相位），见 daynight.go。
	//
	// ⚠️ 这里同时把"已发出的相位"记下来：否则每秒的 `tickDayChanging` 会在
	// **登录这一瞬间**补发一条 `SM_DAYCHANGING`，而它与这条 `SM_NEWMAP` 是
	// **两个 goroutine** 发的、顺序不定 ⇒ 严格按序收包的客户端会看到
	// "期望 SM_NEWMAP(51) 实际 46"（e2e 真的偶发挂在这上面）。
	now := time.Now()
	s.send(c, proto.SM_NEWMAP, int32(o.ID), uint16(o.PosX()), uint16(o.PosY()),
		s.dayBrightOf(o.MapRef(), now), o.MapRef().Name)
	p.brightInit, p.brightPhase = true, gameTimePhase(now)

	wl := proto.MessageBodyWL{Param1: o.FeatureBits(), Param2: o.StatusBits(), Tag1: proto.MakeFeatureEx(true)}
	wlRaw := wl.Bytes()
	s.send(c, proto.SM_LOGON, int32(o.ID), uint16(o.PosX()), uint16(o.PosY()), uint16(o.Facing()), string(wlRaw[:]))

	// 服务端配置（原版 `RM_LOGON` 的顺序：SendLogon → **SendServerConfig** →
	// ClientQueryUserName → RefUserState → SendMapDescription，ObjBase.pas:5618-5626）。
	// ⚠️ 原版**只在进游戏这一步发**（另一处是 GM 的 `@TESTSERVERCONFIG` 测试命令）
	// ⇒ 我们不随切图重发（该图能不能跑由客户端首次进入时的标记决定，与原版一致）。
	s.sendServerConfig(c, p)

	feature := uint32(wl.Param1)
	s.send(c, proto.SM_FEATURECHANGED, int32(o.ID), uint16(feature&0xFFFF), uint16(feature>>16), 0, "")
	s.send(c, proto.SM_MAPDESCRIPTION, -1, 0, 0, 0, o.MapRef().Name)

	abil := abilityFromPB(d.Abil)
	abRaw := abil.Bytes()
	s.send(c, proto.SM_ABILITY, int32(p.gold()), uint16(d.Job),
		proto.LoWord(int32(d.GameGold)), proto.HiWord(int32(d.GameGold)), string(abRaw[:]))
	s.sendSubAbility(c, p)
	// 装备**形状**触发的特殊效果（隐身戒指/麻痹/复活…）缓存一次：
	// statusBits() 是 *Player 方法，拿不到物品表，只能走缓存。
	s.refreshSpecials(p)
	// 上限也要按"等级基数 + 装备加成"重算一次：存档里的 MaxHp 可能带着上次的
	// 加成（换了装备却没上线过），重算才不会再叠加一次。
	s.applyEquipHpMp(p)
	// 负重同理（上限是等级/职业的纯函数，重算是幂等的）。
	s.applyWeights(p)

	s.sendUseItems(c, p)
	// 从存档恢复两个"行为开关"（原版 HumData 字段，ObjBase.pas:24920-24937）。
	//
	// ⚠️ allowGroup 必须恢复：它是"允许被别人拉进队"的开关（CM_GROUPMODE），
	// 不恢复的话玩家每次上线都要重新勾一次，队友就拉不动他了。
	// 对应 ObjBase.pas:2351-2352（UsrEngn 读 HumData.btAllowGroup）。
	if d := p.Char.Data; d != nil {
		p.allowGroup = d.AllowGroup
		p.allowGroupRecall = d.AllowGroupRecall
	}
	// 原版 TBaseObject 构造时 `m_boAllowDeal := True`（ObjBase.pas:1330），
	// 登录时不从存档恢复（新角色默认允许被交易）。
	p.allowDeal = true
	p.logonDone = true
	p.dayChangingReady.Store(true)
	s.ensureInitialMagics(p)
	s.sendMyMagics(c, p)

	// 行会信息恢复：原版在角色上线时按成员名反查行会
	// （ObjBase.pas:16575），并下发 SM_CHANGEGUILDNAME 让客户端显示行会名。
	s.sendChangeGuildName(c, p)
	// 传送保护计时从此刻起算（m_dwMapMoveTick，ObjBase.pas:21322）。
	//
	// ⚠️ 这里**不主动下发** SM_AREASTATE / SM_CHANGENAMECOLOR：
	// 进游戏的包序列有严格顺序（见本函数顶部注释），插入额外包会让
	// 真客户端与 mir2cli 的"下一个包"断言错位。两者由 pvpLoop 在
	// 登录包消费完之后按需补发。
	p.lastMoveAt = time.Now()
}
func (s *Server) sendNotice(c net.Conn, p *Player) {
	p.noticeSent = true
	s.send(c, proto.SM_SENDNOTICE, 2000, 0, 0, 0, "欢迎来到 mir2go")
	_ = c.SetReadDeadline(time.Now().Add(loginNoticeTimeout))
}

// ---------- 视野 ----------
func (s *Server) handleGameMsg(c net.Conn, p *Player, pkt wire.Packet) {
	switch pkt.Head.Ident {
	case proto.CM_LOGINNOTICEOK:
		if p.noticeSent && !p.logonDone {
			_ = c.SetReadDeadline(time.Now().Add(readTimeout))
			s.sendEnterWorld(c, p)
			s.updateVision(p)
		}

	case proto.CM_QUERYBAGITEMS:
		if p.logonDone {
			s.sendBagItems(c, p)
		}

	case proto.CM_WALK, proto.CM_RUN:
		// 石化/麻痹期间禁止移动。⚠️ 原版**不拦**（它把状态位下发后由客户端
		// 自己不打移动包）⇒ 这一道是我们在服务端补的，防止改包客户端硬走。
		if p.Obj != nil && p.Obj.Stoned(time.Now()) {
			return
		}
		s.handleMove(c, p, pkt, pkt.Head.Ident == proto.CM_RUN)

	case proto.CM_TURN:
		s.handleTurn(c, p, pkt)

	case proto.CM_PICKUP:
		s.handlePickup(c, p, pkt)
	case proto.CM_DROPITEM:
		s.handleDropItem(c, p, pkt)
	case proto.CM_DROPGOLD:
		// 丢金币：金额在 **Recog**（客户端 ClMain.pas:3188）。
		s.handleDropGold(c, p, int64(pkt.Head.Recog))
	case proto.CM_USERMAKEDRUGITEM:
		// 制药（原版 `ClientMakeDrugItem`，ObjBase.pas:18000）。见 makedrug.go。
		s.handleMakeDrug(c, p, pkt)
	case proto.CM_SOFTCLOSE:
		// 软关服/退到选角（原版 `m_boReconnection := True; m_boSoftClose := True`，
		// ObjBase.pas:4751-4755）。见 handleSoftClose。
		s.handleSoftClose(c, p)
	case proto.CM_SITDOWN:
		// 打坐（原版 `TPlayObject.ClientSitDownHit`，ObjBase.pas:17024-17042）。
		// 见 handleSitDown。
		s.handleSitDown(c, p)
	case proto.CM_OPENDOOR:
		// 开门：Recog=客户端侧门序号（忽略）、Param=x、Tag=y（ClMain.pas:2376）。
		s.handleOpenDoor(c, p, pkt)
	case proto.CM_WANTMINIMAP:
		s.handleWantMinimap(c, p)
	case proto.CM_BUTCH:
		// 取肉：Recog=目标、Param=x、Tag=y、Series=dir（ClMain.pas:3082）。
		s.handleButch(c, p, pkt)
	case proto.CM_EAT:
		s.handleEat(c, p, pkt)
	case proto.CM_TAKEONITEM:
		s.handleTakeOn(c, p, pkt)
	case proto.CM_TAKEOFFITEM:
		s.handleTakeOff(c, p, pkt)
	case proto.CM_CLICKNPC:
		s.handleClickNPC(c, p, pkt)
	case proto.CM_USERBUYITEM:
		s.handleBuyItem(c, p, pkt)
	case proto.CM_USERSELLITEM:
		s.handleSellItem(c, p, pkt)
	case proto.CM_USERREPAIRITEM:
		s.handleRepairItem(c, p, pkt)
	case proto.CM_MERCHANTDLGSELECT:
		s.handleDlgSelect(c, p, pkt)
	// 仓库存入 / 取回（1031/1032）。Recog = 商人 ActorId，Param/Tag = MakeIndex
	// 低/高 16 位，body = 物品显示名（可能带" 次数"后缀）。见 storage.go 的文件头。
	case proto.CM_USERSTORAGEITEM:
		s.handleStorageDeposit(c, p, pkt)
	case proto.CM_USERTAKEBACKSTORAGEITEM:
		s.handleStorageTakeBack(c, p, pkt)
	case proto.CM_SPELL:
		s.handleSpell(c, p, pkt)
	// 七种攻击包 → 七种攻击模式（wHitMode），见 warrskill.go 的文件头。
	// ⚠️ 模式来自**消息号**，不是 Tag 里的字段。
	case proto.CM_HIT, proto.CM_HEAVYHIT, proto.CM_BIGHIT, proto.CM_POWERHIT,
		proto.CM_LONGHIT, proto.CM_WIDEHIT, proto.CM_FIREHIT:
		s.handleAttack(c, p, pkt)

	case proto.CM_SAY:
		s.handleSay(c, p, pkt)

	case proto.CM_OPENGUILDDLG, proto.CM_GUILDHOME, proto.CM_GUILDMEMBERLIST,
		proto.CM_GUILDADDMEMBER, proto.CM_GUILDDELMEMBER,
		proto.CM_GUILDUPDATENOTICE, proto.CM_GUILDUPDATERANKINFO,
		proto.CM_GUILDALLY, proto.CM_GUILDBREAKALLY:
		s.handleGuildMsg(c, p, pkt)

	// 组队：CM_GROUPMODE 的 Param 决定"开/关"，另三个 body 是角色名。
	// 原版 ObjBase.pas:4777-4795 的 case 分支。
	case proto.CM_GROUPMODE, proto.CM_CREATEGROUP, proto.CM_ADDGROUPMEMBER,
		proto.CM_DELGROUPMEMBER:
		if !s.handleGroupMsg(c, p, pkt) {
			obs.Event("group_msg_unhandled", "player", p.Char.Name, "ident", pkt.Head.Ident)
		}

	// 玩家交易：CM_DEALTRY 的目标靠"朝向格"确定（body 被服务端丢弃），
	// CM_DEALADDITEM/DELITEM 的 body 是物品名，CM_DEALCHGGOLD 的金币在 Recog。
	case proto.CM_DEALTRY, proto.CM_DEALADDITEM, proto.CM_DEALDELITEM,
		proto.CM_DEALCANCEL, proto.CM_DEALCHGGOLD, proto.CM_DEALEND:
		if !s.handleDealMsg(c, p, pkt) {
			obs.Event("deal_msg_unhandled", "player", p.Char.Name, "ident", pkt.Head.Ident)
		}
	default:
	}
}

// handleMove 处理走/跑。方向取自 Tag（UsrEngn.pas:1739-1774）。
func (s *Server) handleMove(c net.Conn, p *Player, pkt wire.Packet, running bool) {
	if !p.logonDone {
		return
	}
	dir := proto.LoByte(pkt.Head.Tag)
	if !p.Limiter.Allow(running, time.Now()) {
		obs.Event("move_rate_limited", "player", p.Char.Name, "dir", dir, "running", running)
		return
	}

	obs.Event("move_try", "player", p.Char.Name, "dir", dir, "running", running,
		"x", p.Obj.PosX(), "y", p.Obj.PosY())
	// 改坐标与同步空间索引放在**同一段 s.mu 临界区**里（见 statelock.go 第二节：
	// 审计 P1-5 记的正是"先 MoveTo 改坐标、再只锁索引"这个跨锁域写法）。
	newX, newY, newDir, moved := s.movePlayer(p, dir)
	if !moved {
		s.send(c, proto.SM_MOVEFAIL, int32(p.Obj.ID), uint16(newX), uint16(newY), uint16(newDir), "")
		return
	}

	ident := uint16(proto.SM_WALK)
	if running {
		ident = proto.SM_RUN
	}
	s.send(c, ident, int32(p.Obj.ID), uint16(newX), uint16(newY), uint16(newDir), "")
	s.broadcastMove(p, ident)
	obs.Event("move", "player", p.Char.Name, "x", newX, "y", newY,
		"dir", newDir, "running", running)
	s.updateVision(p)
	// 火墙的第二条伤害路径：踩上去立刻结算一次（ObjBase.pas:20190 Walk）。
	// ⚠️ 必须在 s.mu 之外调：wallBurnAtCell 自己加锁。
	s.wallBurnAtCell(p.Obj.MapRef(), newX, newY)
}
func (s *Server) handleTurn(c net.Conn, p *Player, pkt wire.Packet) {
	if !p.logonDone {
		return
	}
	// 朝向同样属于 s.mu 域（statelock.go 第二节）。
	x, y, dir, ok := s.turnPlayer(p, proto.LoByte(pkt.Head.Tag))
	if !ok {
		return
	}
	// 转身与取肉共用一个时间戳（原版 `m_dwTurnTick`，见 ClientGetButchItem 首行）。
	p.turnAt = time.Now()
	s.send(c, proto.SM_TURN, int32(p.Obj.ID), uint16(x), uint16(y), uint16(dir), "")
	s.broadcastMove(p, proto.SM_TURN)
}

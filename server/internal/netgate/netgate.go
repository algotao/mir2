// Package netgate 实现**按门禁间隔投递**：一条命令来得太快时，决定"立即放行 /
// 延时到点再投 / 连续超速告警"。服务端的收包架构是"持续收 → 持续拆 → 队列 → 按门禁投递"。
//
// 服务端收包架构：**持续收 → 持续拆 → 队列 → 按门禁间隔投递**。
//
// 原版就是这套（用户 2026-10-05 指出我们缺这一半）：
//
//	RunSock.pas:866  收到一帧 → UserEngine.ProcessUserMessage(...)
//	TBaseObject.Run  while GetMessage(@ProcessMsg) do Operate(@ProcessMsg)   // 每个对象自己的队列
//	ObjBase.pas:8760-8800  ClientHitXY 里的"间隔判定"：
//
//	  if not boLateDelivery then begin
//	    if not CheckActionStatus(wIdent, dwDelayTime) then Exit;
//	    dwAttackTime := _MAX(0, Integer(g_Config.dwHitIntervalTime) - m_nHitSpeed * btItemSpeed);
//	    dwCheckTime  := GetTickCount - m_dwAttackTick;
//	    if dwCheckTime < dwAttackTime then begin
//	      Inc(m_dwAttackCount);
//	      dwDelayTime := dwAttackTime - dwCheckTime;
//	      if dwDelayTime > g_Config.dwDropOverSpeed then begin
//	        if m_dwAttackCount >= 4 then ...            // 连续超速
//	      end else
//	        dwDelayTime := 0;                            // 差得不多 ⇒ 立刻放行
//	    end else begin
//	      m_dwAttackCount := 0; m_dwAttackTick := GetTickCount;   // 正常间隔 ⇒ 清零
//	    end;
//	  end;
//
//	调用处（ObjBase.pas:4710-4740）：`dwDelayTime <> 0` ⇒ **SendDelayMsg(..., dwDelayTime)**
//	（ObjBase.pas:19367：造一个 `dwDeliveryTime := GetTickCount + dwDelay` 的消息**重新投递**，
//	`boLateDelivery := True` 表示"这次是补投，不再走间隔判定"）。
//
// 数值全部取官方 `!setup.txt`（不要自己发明）：
//
//	OverSpeedKickCount=4     DropOverSpeed=10
//	HitIntervalTime=520      MagicHitIntervalTime=450
//	RunLongHitIntervalTime=200  RunHitIntervalTime=200  WalkHitIntervalTime=200
//
// 我们这里做的是**同样的事**：每条连接一个读协程（持续收、持续拆），
// 一个派发循环按门禁决定「立即派发 / 延时到点再派发 / 超速丢弃」。
//
// ⚠️ 延时是**队头阻塞**式的（延时期间不派发后面的消息）——这与原版一致：
// 每个对象的消息队列本来就是 FIFO，`SendDelayMsg` 补投的那条也在队里排队。
package netgate

import (
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/tscale"
)

// 官方 !setup.txt:992-997 的三个门禁参数。
const (
	// dropOverSpeed 是"小于它就不算超速、直接放行"的阈值（DropOverSpeed=10ms）。
	dropOverSpeed = 10 * time.Millisecond
	// OverSpeedKickCount 是连续超速多少次算异常（OverSpeedKickCount=4）。
	// 官方 `KickOverSpeed=0`（默认**不踢**）⇒ 我们只计数告警，不主动断线。
	OverSpeedKickCount = 4
	// hitIntervalTime 是攻击命令的最小间隔（HitIntervalTime=520ms）。
	// 原版还会减去 `m_nHitSpeed * btItemSpeed`；我们的攻速在实体层
	// （`entity.Monster.CanAttack` / 玩家的攻击冷却）另有一层，这里用官方基数。
	hitIntervalTime = 520 * time.Millisecond
	// magicHitInterval 是施法命令的基础间隔（MagicHitIntervalTime=450ms）。
	// 原版是 `UserMagic.MagicInfo.dwDelayTime + 450`；我们每个技能自己的
	// `spellLast` 冷却在 spell.go 里已经算了，这里只加基础间隔。
	magicHitInterval = 450 * time.Millisecond
)

// IntervalOf 返回一条命令的**最小间隔**（0 = 不受门禁）。
//
// 覆盖范围照原版：攻击族走 ClientHitXY（8760）、施法走 ClientSpellXY（8985）。
// 走路/跑/转身/说话的门禁在各处理器内部（`CheckActionStatus` + 各自的
// `m_dwWalkTick`/`m_dwSayMsgTick` 等），我们那些已经在实体层/chat.go 里有了，
// 不在这里重复，免得两层门互相压制。
func IntervalOf(ident uint16) time.Duration {
	switch ident {
	case proto.CM_HIT, proto.CM_HEAVYHIT, proto.CM_BIGHIT, proto.CM_POWERHIT,
		proto.CM_LONGHIT, proto.CM_WIDEHIT, proto.CM_FIREHIT:
		return hitIntervalTime
	case proto.CM_SPELL:
		return magicHitInterval
	}
	return 0
}

// itemSpeedTime 是 `g_Config.ClientConf.btItemSpeed`（!setup.txt 的 `ItemSpeedTime`）。
//
// ⚠️ 官方 `!setup.txt` **没有**这一项 ⇒ 用 M2Share 的出厂值 **25**（M2Share.pas:2170
// `btItemSpeed: 25; {60}`）。它把"攻速点数"换算成毫秒。
const itemSpeedTime = 25

// IsAttackIdent 判断是不是攻击族的攻击命令（攻速只作用于这一族）。
func IsAttackIdent(ident uint16) bool {
	switch ident {
	case proto.CM_HIT, proto.CM_HEAVYHIT, proto.CM_BIGHIT, proto.CM_POWERHIT,
		proto.CM_LONGHIT, proto.CM_WIDEHIT, proto.CM_FIREHIT:
		return true
	}
	return false
}

// AttackIntervalFor 返回该玩家攻击命令的最小间隔（原版 ClientHitXY:8774）：
//
//	dwAttackTime := _MAX(0, Integer(g_Config.dwHitIntervalTime)
//	                       - m_nHitSpeed * g_Config.ClientConf.btItemSpeed);
//
// 即"基础 520ms 减去 攻速×25ms"，**下限 0**（原版 `_MAX(0, ...)` 就是防负数）。
// 攻速为负（戴了减速武器）时间隔会变长 —— 原版同样如此，别当 bug 修。
//
// ⚠️ 两项都过 tscale.D：本工程用时间倍速跑回归，游戏内节奏要一起缩。
func AttackIntervalFor(hitSpeed int) time.Duration {
	d := tscale.D(hitIntervalTime) - tscale.D(time.Duration(hitSpeed*itemSpeedTime)*time.Millisecond)
	if d < 0 {
		return 0
	}
	return d
}

// Gate 是一条连接的门禁状态（对应原版 TBaseObject 上的
// `m_dwAttackTick` / `m_dwAttackCount`，我们按消息号分别记）。
type Gate struct {
	tick  map[uint16]time.Time // 上次**放行**的时刻
	count map[uint16]int       // 连续超速次数（正常间隔后清零）
}

func NewGate() *Gate {
	return &Gate{tick: map[uint16]time.Time{}, count: map[uint16]int{}}
}

// decide 判定一条命令该怎么走。返回 (延时, 是否超速告警)。
//
//	延时 == 0        ⇒ 立即派发（含"差得不多"的放行，原版 dwDelayTime := 0）
//	延时 > 0         ⇒ 延时这么久再派发（原版 SendDelayMsg）
//	overspeed == true ⇒ 本次属"连续超速"（原版 m_dwAttackCount >= 4 那条告警）
//
// ⚠️ interval / gap 都按**服务端时间倍速缩放后的值**传入（调用方用 tscale.D）：
// 原版是实时服务器、间隔就是墙钟毫秒；我们跑 `-time-scale 20` 做回归，
// 若这里用裸墙钟，20 倍速下客户端"看起来"就在超速 ⇒ 每条攻击都被真延时投递，
// 用例时序全乱（实测：13 个用例挂）。项目里既有的冷却（minSpellInterval 等）
// 一律走 tscale.D，这里保持一致。
func (g *Gate) Decide(ident uint16, now time.Time, interval time.Duration) (time.Duration, bool) {
	if interval <= 0 {
		return 0, false
	}
	last, seen := g.tick[ident]
	if !seen {
		g.tick[ident] = now
		g.count[ident] = 0
		return 0, false
	}
	elapsed := now.Sub(last)
	if elapsed >= interval {
		// 间隔正常 ⇒ 放行并清零（原版 `m_dwAttackCount := 0; m_dwAttackTick := GetTickCount`）
		g.tick[ident] = now
		g.count[ident] = 0
		return 0, false
	}
	// 太快了：算出还差多少
	gap := interval - elapsed
	if gap <= tscale.D(dropOverSpeed) {
		// 差得不多（原版 `else dwDelayTime := 0`）⇒ 立刻放行，且**不算超速**
		return 0, false
	}
	g.count[ident]++
	if g.count[ident] >= OverSpeedKickCount {
		// 原版这里走告警（KickOverSpeed=0 时不踢线）⇒ 我们交回调用方决定怎么记
		return gap, true
	}
	return gap, false
}

// ResetCount 把某条命令的连续超速计数清零。
//
// 调用方在**打完告警日志后**调用它（原版 `m_dwAttackCount := 0` 的那一处：
// 告警后重新计数，否则会连着每一条都告警）。
func (g *Gate) ResetCount(ident uint16) { g.count[ident] = 0 }

// LateDelivery 表示"这条消息是补投的，不再走门禁"（原版 boLateDelivery）。
// 派发循环用它区分首次到达与延时补投。
type LateDelivery bool

// LogOverSpeed 打一条超速日志（原版的 g_sHitOverSpeed / g_sSpellOverSpeed）。
func LogOverSpeed(name string, ident uint16, gap time.Duration, count int) {
	kind := "攻击"
	if ident == proto.CM_SPELL {
		kind = "魔法"
	}
	log.Printf("[%s超速] %s 间隔不足（还差 %v，连续第 %d 次）", kind, name, gap, count)
}

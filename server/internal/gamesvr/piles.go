package gamesvr

import (
	"time"

	"github.com/algotao/mir2/server/internal/world"
)

// 挖矿的碎石堆（原版 `TPileStones`，事件类型 `ET_PILESTONES`）。
//
// 原版 `TPlayObject.PileStones`（ObjBase.pas:21881-21925）在**挖矿者自己脚下**铺一个事件：
//
//	PileEvent := TEvent(m_PEnvir.GetEvent(m_nCurrX, m_nCurrY));
//	if PileEvent = nil then
//	  TPileStones.Create(m_PEnvir, m_nCurrX, m_nCurrY, ET_PILESTONES, 5 * 60 * 1000)  // 5 分钟
//	else if PileEvent.m_nEventType = ET_PILESTONES then
//	  TPileStones(PileEvent).AddEventParam;      // 已有 ⇒ 层数 +1（封顶 5）
//
// ⚠️ 三个容易做错的细节：
//
//	① 位置是**挖矿者脚下**（`m_nCurrX/Y`），不是矿脉那一格；
//	② 寿命**不刷新**：`AddEventParam` 只 `Inc(m_nEventParam)`（Event.pas:347-350），
//	   5 分钟是从**创建**那刻算的；
//	③ 重复挖**不需要发包**：客户端在挥镐那一帧自己也会把层数 +1
//	  （`Actor.pas:3501-3506`），服务端只在"从无到有"时发一条 `SM_SHOWEVENT`。
const (
	pileLifetime  = 5 * time.Minute // 原版 `5 * 60 * 1000`
	pileMaxLayers = 5               // 客户端 `clEvent.pas:108-109` 把 >5 夹成 5
)

// ensurePileAt 在 (x,y) 铺/叠一个碎石堆：
// 没有就建（并发一条 `SM_SHOWEVENT`，事件参数 1），已有就只把层数 +1（**不发包**）。
//
// ⚠️ 调用方**不得**持有 `s.mu`：这里要在锁外发包 —— `announceGroundEvent`
// 会再拿 `s.mu.RLock`，同一个 goroutine 里"持写锁再读锁"就是自死锁
// （2026-10-06 在 `castWall` 上踩过一次，见 groundevent.go 的注释）。
func (s *Server) ensurePileAt(m *world.Map, x, y int) {
	if m == nil {
		return
	}
	k := groundKey{m: m, x: x, y: y, typ: evPileStones}
	now := time.Now()

	s.mu.Lock()
	if ev := s.world.groundEvents[k]; ev != nil {
		if ev.layers < pileMaxLayers {
			ev.layers++ // 只记数：客户端的层数是它自己 +1 的
		}
		s.mu.Unlock()
		return
	}
	id := s.nextEventID()            // 分配 id 不需要锁
	if s.world.groundEvents == nil { // 懒建，理由见 placeGroundEvent
		s.world.groundEvents = make(map[groundKey]*groundEvent)
	}
	s.world.groundEvents[k] = &groundEvent{
		eventID: id,
		until:   now.Add(pileLifetime),
		typ:     evPileStones,
		layers:  1,
	}
	s.mu.Unlock()

	// 原版 `TPileStones.Create` 里 `m_nEventParam := 1`（Event.pas:334-339）
	// ⇒ 首包的事件参数就是 1，客户端拿它画第一层。
	s.announceGroundEvent(m, x, y, id, evPileStones, 1)
}

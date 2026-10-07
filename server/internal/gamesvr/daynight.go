package gamesvr

import (
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/world"
)

// 昼夜（原版 `g_nGameTime` + `TPlayObject.DayBright`）。
//
// 原版把"游戏时间"做成一个**全局相位**（`M2Share.pas:1404 g_nGameTime: Integer`），
// 由引擎按**宿主机时钟的小时**推进（`FrnEngn.pas:83-91 GetGameTime`）：
//
//	4,15                    ⇒ 0  SUNRAISE 日出
//	5..10, 16..22           ⇒ 1  DAY      白天
//	11,23                   ⇒ 2  SUNSET   日落
//	0..3, 12,13,14          ⇒ 3  NIGHT    夜晚
//
// 消费方有两个：
//
//  1. **客户端亮度**：`DayBright()`（ObjBase.pas:4283-4294）算出来挂在
//     `SM_NEWMAP` / `SM_CHANGEMAP` 的 Series 上（客户端读作 darkness，
//     见 ClMain.pas:2493 / :3842）；相位变了还要补一条 `SM_DAYCHANGING`
//     （`RM_DAYCHANGING` → `MakeDefaultMsg(SM_DAYCHANGING, 0, m_btBright,
//     DayBright(), 0)`，ObjBase.pas:5726）——原版在 `Run` 里轮询
//     `if m_btBright <> g_nGameTime then …`（:15607-15610）。
//  2. **脚本条件** `DAYTIME SUNRAISE|DAY|SUNSET|NIGHT`（ObjNpc.pas:7013-7030）
//     比的就是这个相位；官方脚本里有 1 处在用
//     （`market_def/4Reagent_store-0119.txt`）。
//
// ⚠️ 我们原来在 `SM_NEWMAP` 上写死 `brightDay = 0`（且注释把语义写反了），
// 切图那条更是直接写 0 ⇒ **夜里也是大白天**、脚本也无从判断时辰。
const (
	gameTimeSunraise = 0
	gameTimeDay      = 1
	gameTimeSunset   = 2
	gameTimeNight    = 3
)

// gameTimePhase 按小时算当前相位（逐字照抄 `FrnEngn.pas:87-90` 的分档）。
func gameTimePhase(now time.Time) int {
	switch h := now.Hour(); {
	case h == 4 || h == 15:
		return gameTimeSunraise
	case h == 11 || h == 23:
		return gameTimeSunset
	case (h >= 5 && h <= 10) || (h >= 16 && h <= 22):
		return gameTimeDay
	default: // 0..3, 12,13,14
		return gameTimeNight
	}
}

// gameTimeName 是相位的脚本名（`DAYTIME` 条件的参数，M2Share 的 `sSUNRAISE` 等）。
func gameTimeName(phase int) string {
	switch phase {
	case gameTimeSunraise:
		return "SUNRAISE"
	case gameTimeDay:
		return "DAY"
	case gameTimeSunset:
		return "SUNSET"
	default:
		return "NIGHT"
	}
}

// dayBright 复刻 `TPlayObject.DayBright`（ObjBase.pas:4283-4294）：
//
//	地图标了 DARK     ⇒ 1（永远暗）
//	地图标了 DAYLIGHT ⇒ 0（永远亮）
//	否则按相位：白天(1) ⇒ 0、夜晚(3) ⇒ 1、日出/日落(0/2) ⇒ 2
//
// ⚠️ 两句 `if` 的**先后**是原版的：`DAYLIGHT` 写在后头 ⇒ 它压过 DARK 与相位
// （原版就是"先按 DARK 置 1、再按相位覆盖、最后 DAYLIGHT 一票否决"）。
func dayBright(mi *data.MapInfo, phase int) uint16 {
	out := uint16(2)
	switch {
	case mi != nil && mi.Darkness:
		out = 1
	case phase == gameTimeDay:
		out = 0
	case phase == gameTimeNight:
		out = 1
	}
	if mi != nil && mi.DayLight {
		out = 0
	}
	return out
}

// dayBrightOf 取某张地图此刻的亮度。
func (s *Server) dayBrightOf(m *world.Map, now time.Time) uint16 {
	return dayBright(s.mapFlagOf(m), gameTimePhase(now))
}

// tickDayChanging 复刻原版 `Run` 里那段轮询：**相位变了**就补一条
// `SM_DAYCHANGING`（Param = 相位、Tag = `DayBright()`）。
//
// 原版是每个对象自己比 `m_btBright <> g_nGameTime`（ObjBase.pas:15607-15610），
// 我们挂在每秒的 `tickPlayers` 上（粒度差 1 秒，注释里写明）。
// 亮度本身随 `SM_NEWMAP`/`SM_CHANGEMAP` 走，所以这里只认相位。
func (s *Server) tickDayChanging(p *Player, now time.Time) {
	if p == nil || !p.dayChangingReady.Load() || p.Obj == nil || p.Obj.MapRef() == nil {
		return
	}
	phase := gameTimePhase(now)
	if p.brightInit && p.brightPhase == phase {
		return
	}
	p.brightInit, p.brightPhase = true, phase
	s.send(p.conn, proto.SM_DAYCHANGING, 0, uint16(phase),
		dayBright(s.mapFlagOf(p.Obj.MapRef()), phase), 0, "")
}

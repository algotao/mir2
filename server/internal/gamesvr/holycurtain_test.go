package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// TestHolyCurtainGroundEvent 困魔咒的光幕要让客户端看得见：
//
//	围着目标铺 **8 格** `ET_HOLYCURTAIN`（位置逐字对照 Magic.pas:1251-1276）
//	寿命与定身时长一致，到期按各自的 id 发 `SM_HIDEEVENT`
//
// ⚠️ 补的是一个真缺口：我们原来只定身、不发事件 ⇒ 真客户端**看不见光幕**
// （怪物凭空定住，旁观者一头雾水）。
func TestHolyCurtainGroundEvent(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	m := p.Obj.MapRef()

	viewer := newTestPlayer(2, "旁观者", entity.JobWarr)
	viewer.Obj.SetPlace(m, p.Obj.PosX(), p.Obj.PosY(), viewer.Obj.Facing())
	viewer.visible = entity.NewViewTracker()
	rec := &recordingConn{}
	viewer.conn = rec
	s.world.players[viewer.Obj.ID] = viewer
	s.world.index.Add(viewer)
	s.world.groundEvents = make(map[groundKey]*groundEvent)
	// 手工构造的 Server 里 cfg 是零值 ⇒ 视野半径 0 会让广播什么都不发（同格距离 0 才侥幸过）。
	s.cfg.viewRange = 12

	x, y := p.Obj.PosX()+2, p.Obj.PosY()+2
	if got := s.placeHolyCurtain(m, x, y, time.Minute); got != len(holyCurtainCells) {
		t.Fatalf("该铺 %d 格光幕，实际 %d 格", len(holyCurtainCells), got)
	}

	shows := packetsOfIdent(rec.packets(t), proto.SM_SHOWEVENT)
	if len(shows) != len(holyCurtainCells) {
		t.Fatalf("该发 %d 条 SM_SHOWEVENT，实际 %d 条", len(holyCurtainCells), len(shows))
	}
	got := map[[2]int]bool{}
	for _, pk := range shows {
		if pk.Head.Param != evHolyCurtain {
			t.Errorf("Param 该是 ET_HOLYCURTAIN(%d)：%d", evHolyCurtain, pk.Head.Param)
		}
		got[[2]int{int(pk.Head.Tag), int(pk.Head.Series)}] = true
	}
	for _, c := range holyCurtainCells {
		cell := [2]int{x + c[0], y + c[1]}
		if !got[cell] {
			t.Errorf("缺了 (%d,%d) 这一格", cell[0], cell[1])
		}
	}

	// 到期 ⇒ 每格一条 SM_HIDEEVENT，且 id 都是登记过的
	rec.reset()
	ids := map[int32]bool{}
	for _, ev := range s.world.groundEvents {
		ids[int32(ev.eventID)] = true
		ev.until = time.Now().Add(-time.Second)
	}
	s.groundEventTick(time.Now())
	hides := packetsOfIdent(rec.packets(t), proto.SM_HIDEEVENT)
	if len(hides) != len(holyCurtainCells) {
		t.Fatalf("到期该发 %d 条 SM_HIDEEVENT，实际 %d 条", len(holyCurtainCells), len(hides))
	}
	for _, h := range hides {
		if !ids[h.Head.Recog] {
			t.Errorf("SM_HIDEEVENT 的 Recog=%d 不是铺光幕时给的 id", h.Head.Recog)
		}
	}
	if len(s.world.groundEvents) != 0 {
		t.Errorf("到期后登记表该空了，还剩 %d 条", len(s.world.groundEvents))
	}
}

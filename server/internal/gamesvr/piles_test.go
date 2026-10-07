package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// TestPileStonesGroundEvent 挖矿的碎石堆要让客户端看得见（`ET_PILESTONES`）：
//
//	从无到有 ⇒ 一条 SM_SHOWEVENT（Param=ET_PILESTONES、Tag/Series=脚下坐标、
//	            包体事件参数=1（原版 `TPileStones.Create` 里 `m_nEventParam := 1`））
//	再挖一次 ⇒ **不发包**（客户端在挥镐那一帧自己 +1，见 Actor.pas:3501-3506），
//	            但服务端要记层数、封顶 5
//	5 分钟到期 ⇒ 按同一个事件 id 发 SM_HIDEEVENT
func TestPileStonesGroundEvent(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	m := p.Obj.MapRef()

	viewer := newTestPlayer(2, "旁观者", entity.JobWarr)
	viewer.Obj.SetPlace(m, p.Obj.PosX(), p.Obj.PosY(), viewer.Obj.Facing())
	viewer.visible = entity.NewViewTracker()
	rec := &recordingConn{}
	viewer.conn = rec
	s.world.players[viewer.Obj.ID] = viewer
	s.world.index.Add(viewer)
	s.world.groundEvents = make(map[groundKey]*groundEvent) // 手工构造的 Server 得自己备好这张表

	// ① 从无到有：一条 SM_SHOWEVENT，落在**挖矿者脚下**
	s.ensurePileAt(m, p.Obj.PosX(), p.Obj.PosY())
	shows := packetsOfIdent(rec.packets(t), proto.SM_SHOWEVENT)
	if len(shows) != 1 {
		t.Fatalf("第一次挖中该发 1 条 SM_SHOWEVENT，实际 %d 条", len(shows))
	}
	got := shows[0]
	if got.Head.Param != evPileStones {
		t.Errorf("Param 该是事件类型 ET_PILESTONES(%d)：%d", evPileStones, got.Head.Param)
	}
	if got.Head.Tag != uint16(p.Obj.PosX()) || got.Head.Series != uint16(p.Obj.PosY()) {
		t.Errorf("位置该是挖矿者脚下：Tag=%d Series=%d，期望 %d/%d",
			got.Head.Tag, got.Head.Series, p.Obj.PosX(), p.Obj.PosY())
	}
	if len(got.Body) < 2 || got.Body[0] != 1 {
		t.Errorf("首包的事件参数该是 1（原版 Create 里 m_nEventParam := 1）：%v", got.Body)
	}
	id := uint32(got.Head.Recog)

	// ② 再挖两次：不发新包，但层数要涨
	rec.reset()
	s.ensurePileAt(m, p.Obj.PosX(), p.Obj.PosY())
	s.ensurePileAt(m, p.Obj.PosX(), p.Obj.PosY())
	if got := len(packetsOfIdent(rec.packets(t), proto.SM_SHOWEVENT)); got != 0 {
		t.Errorf("重复挖中不该再发包（客户端自己 +1），实际 %d 条", got)
	}
	pl := s.world.groundEvents[groundKey{m: m, x: p.Obj.PosX(), y: p.Obj.PosY(), typ: evPileStones}]
	if pl == nil {
		t.Fatal("碎石堆没记进世界状态")
	}
	if pl.layers != 3 {
		t.Errorf("层数该是 3（1 建 + 2 叠），实际 %d", pl.layers)
	}
	for i := 0; i < pileMaxLayers+3; i++ { // 继续叠，验证封顶
		s.ensurePileAt(m, p.Obj.PosX(), p.Obj.PosY())
	}
	if pl.layers != pileMaxLayers {
		t.Errorf("层数该封顶在 %d，实际 %d", pileMaxLayers, pl.layers)
	}

	// ③ 到期：按同一个 id 发 SM_HIDEEVENT
	rec.reset()
	pl.until = time.Now().Add(-time.Second)
	s.groundEventTick(time.Now())
	hides := packetsOfIdent(rec.packets(t), proto.SM_HIDEEVENT)
	if len(hides) != 1 {
		t.Fatalf("到期该发 1 条 SM_HIDEEVENT，实际 %d 条", len(hides))
	}
	if hides[0].Head.Recog != int32(id) {
		t.Errorf("SM_HIDEEVENT 的 Recog 该是建堆时那个 id：%d，期望 %d", hides[0].Head.Recog, id)
	}
	if s.world.groundEvents[groundKey{m: m, x: p.Obj.PosX(), y: p.Obj.PosY(), typ: evPileStones}] != nil {
		t.Error("到期后世界状态里不该还留着碎石堆")
	}
}

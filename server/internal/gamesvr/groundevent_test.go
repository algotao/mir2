package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/wire"
)

// TestFireWallGroundEvent 火墙必须让客户端**看得见**：
//
//	铺墙时每格一条 SM_SHOWEVENT（Param=ET_FIRE、Tag=x、Series=y、包体=TShortMessage）
//	到期时按**同一个事件 id** 发 SM_HIDEEVENT（客户端 `EventMan.DelEventById(Recog)`）
//
// ⚠️ 补的是一个真缺口：我们原来只发 `SM_MAGICFIRE`（起手动画），没发地面事件
// ⇒ 真客户端画不出火，玩家会站在"看不见的火墙"里掉血。
func TestFireWallGroundEvent(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	m := p.Obj.MapRef()

	// 一个同格附近的旁观者（`broadcastToViewers` 按空间索引找人）
	viewer := newTestPlayer(2, "旁观者", entity.JobWarr)
	viewer.Obj.SetPlace(m, p.Obj.PosX(), p.Obj.PosY(), viewer.Obj.Facing())
	viewer.visible = entity.NewViewTracker()
	rec := &recordingConn{}
	viewer.conn = rec
	s.world.players[viewer.Obj.ID] = viewer
	s.world.index.Add(viewer)

	// ① 一个事件 ⇒ 一条 SM_SHOWEVENT，字段位置照客户端
	id := s.showGroundEvent(m, p.Obj.PosX(), p.Obj.PosY(), evFire, 0)
	if id == 0 {
		t.Fatal("没分到事件 id（SM_HIDEEVENT 要靠它删）")
	}
	shows := packetsOfIdent(rec.packets(t), proto.SM_SHOWEVENT)
	if len(shows) != 1 {
		t.Fatalf("该发 1 条 SM_SHOWEVENT，实际 %d 条", len(shows))
	}
	got := shows[0]
	if got.Head.Recog != int32(id) {
		t.Errorf("Recog 该是事件 id：%d，期望 %d", got.Head.Recog, id)
	}
	if got.Head.Param != evFire {
		t.Errorf("Param 该是事件类型 ET_FIRE(%d)：%d", evFire, got.Head.Param)
	}
	if got.Head.Tag != uint16(p.Obj.PosX()) || got.Head.Series != uint16(p.Obj.PosY()) {
		t.Errorf("位置该在 Tag/Series：Tag=%d Series=%d，期望 %d/%d",
			got.Head.Tag, got.Head.Series, p.Obj.PosX(), p.Obj.PosY())
	}
	if len(got.Body) < 4 {
		t.Errorf("包体该是 TShortMessage（4 字节），实际 %d 字节", len(got.Body))
	}

	// ② 铺满一次十字火墙，然后让它到期 ⇒ 每格一条 SM_HIDEEVENT，id 与铺时的相同
	now := time.Now()
	walls := map[wallKey]*Wall{}
	placed := placeWalls(walls, m, p.Obj.ID, p.Obj.PosX(), p.Obj.PosY(), 7, now, time.Second, now,
		func(cx, cy int) uint32 { return s.showGroundEvent(m, cx, cy, evFire, 0) })
	if placed == 0 {
		t.Fatal("没铺下任何格子")
	}
	ids := map[int32]bool{}
	for _, w := range walls {
		if w.EventID == 0 {
			t.Fatal("铺下的墙没记事件 id（到期就删不掉客户端的火）")
		}
		ids[int32(w.EventID)] = true
	}
	s.world.walls = walls
	rec.reset()
	s.wallTick(now.Add(2 * time.Second))

	hides := packetsOfIdent(rec.packets(t), proto.SM_HIDEEVENT)
	if len(hides) == 0 {
		t.Fatal("墙到期了却没发 SM_HIDEEVENT（客户端会一直燃着）")
	}
	for _, h := range hides {
		if !ids[h.Head.Recog] {
			t.Errorf("SM_HIDEEVENT 的 Recog=%d 不是铺墙时给的 id", h.Head.Recog)
		}
	}
}

// packetsOfIdent 从记录下来的包里挑出指定消息号的那些。
func packetsOfIdent(all []wire.Packet, ident uint16) []wire.Packet {
	out := make([]wire.Packet, 0, len(all))
	for _, p := range all {
		if p.Head.Ident == ident {
			out = append(out, p)
		}
	}
	return out
}

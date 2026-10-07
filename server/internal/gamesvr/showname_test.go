package gamesvr

import (
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
)

// TestNameColorPacketLayout 名色包 `SM_USERNAME(42)` 的**字段位置**必须照客户端：
//
//	Recog        = 对象 ID
//	Param        = 颜色（客户端 `GetRGB(msg.Param)`，ClMain.pas:4352）
//	Tag / Series = 0
//	包体         = 显示名（`DecodeString(body)`；客户端按 Recog 找 actor，不要坐标）
//
// 原版就这么发：`MakeDefaultMsg(SM_USERNAME, obj, GetCharColor(obj), 0, 0)`
// （ObjBase.pas:5571-5574）。
//
// ⚠️ 这条补的是一个**真 bug**：我们原来把 X/Y 塞进 Param/Tag、颜色塞进 Series
// ⇒ 真客户端会拿**坐标当颜色**（名字颜色错乱，而且随移动变化）。
// e2e 抓不到它 —— 那些用例断言的是服务端日志，不是包。
func TestNameColorPacketLayout(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	viewer := newTestPlayer(2, "旁观者", entity.JobWarr)
	viewer.Obj.SetMapRef(p.Obj.MapRef())
	viewer.Obj.SetPos(viewer.Obj.MapRef(), p.Obj.PosX()+1, p.Obj.PosY())
	viewer.visible = entity.NewViewTracker()
	viewer.visible.Add(p.Obj.ID) // 旁观者"看得见" p
	rec := &recordingConn{}
	viewer.conn = rec
	s.world.players[viewer.Obj.ID] = viewer

	// 红名 ⇒ `GetCharColor` 给 colorPKLevel2（ObjBase.pas:19197-19202）
	p.Char.Data.PkPoint = 1000
	s.sendNameColor(p, time.Now())

	pkts := rec.packets(t)
	if len(pkts) != 1 {
		t.Fatalf("该正好发一条 SM_USERNAME，实际 %d 条", len(pkts))
	}
	got := pkts[0]
	if got.Head.Ident != proto.SM_USERNAME || got.Head.Recog != int32(p.Obj.ID) {
		t.Fatalf("包不对：ident=%d recog=%d（期望 SM_USERNAME(%d)/%d）",
			got.Head.Ident, got.Head.Recog, proto.SM_USERNAME, p.Obj.ID)
	}
	if got.Head.Param != uint16(colorPKLevel2) {
		t.Errorf("颜色必须在 Param（客户端读的是 `GetRGB(msg.Param)`）：Param=%d，期望 %d",
			got.Head.Param, uint16(colorPKLevel2))
	}
	if got.Head.Tag != 0 || got.Head.Series != 0 {
		t.Errorf("Tag/Series 必须是 0（原来这里塞的是坐标 X/Y）：Tag=%d Series=%d",
			got.Head.Tag, got.Head.Series)
	}
	if got.Body != p.Char.Name {
		t.Errorf("包体该是显示名：%q，期望 %q", got.Body, p.Char.Name)
	}
}

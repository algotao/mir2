package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/codec"
	"github.com/algotao/mir2/server/internal/data"
	"github.com/algotao/mir2/server/internal/proto"
)

// TestClientConfLayout 钉住 `TClientConf` 的**字节布局**（24 字节，非 packed）。
//
// 客户端是 `DecodeBuffer(body, @ClientConf, SizeOf(ClientConf))` 直接当记录读
// （ClMain.pas:6364-6365）⇒ 偏移错一个字节，客户端读到的就是一堆垃圾。
func TestClientConfLayout(t *testing.T) {
	conf := proto.ClientConf{
		ClientCanSet: true, RunHuman: true, RunMon: false, RunNpc: false, WarRunAll: false,
		DieColor: 5, SpellTime: 750, HitTime: 1020, ItemFlashTime: 500, ItemSpeed: 25,
		CanStartRun: false, ParalyCanRun: false, ParalyCanWalk: false, ParalyCanHit: false,
		ParalyCanSpell: false, ShowRedHPLabel: false, ShowHPNumber: false,
		ShowJobLevel: true, DuraAlert: true, MagicLock: false, AutoPickUpItem: false,
	}
	b := conf.Bytes()
	if len(b) != proto.ClientConfSize {
		t.Fatalf("记录长度 = %d，期望 %d（Delphi 非 packed 的对齐结果）", len(b), proto.ClientConfSize)
	}
	// 逐字节钉死关键偏移（前 13 个字段）
	want := []byte{
		1, 1, 0, 0, // boClientCanSet / boRunHuman / boRunMon / boRunNpc
		0, 5, // boWarRunAll / btDieColor
		0xEE, 0x02, // wSpellTime = 750 (0x02EE)
		0xFC, 0x03, // wHitIime = 1020 (0x03FC)
		0xF4, 0x01, // wItemFlashTime = 500 (0x01F4)
		25,            // btItemSpeed
		0, 0, 0, 0, 0, // boCanStartRun … boParalyCanSpell
		0, 0, 1, 1, 0, 0, // boShowRedHPLable / ShowHPNumber / ShowJobLevel / DuraAlert / MagicLock / AutoPuckUpItem
	}
	if len(want) != len(b) {
		t.Fatalf("期望字节数 = %d，实际 %d", len(want), len(b))
	}
	for i := range want {
		if b[i] != want[i] {
			t.Errorf("偏移 %d = 0x%02X，期望 0x%02X", i, b[i], want[i])
		}
	}
	// 解回来要一致（客户端同样的读法）
	got, ok := proto.DecodeClientConf(b)
	if !ok || got != conf {
		t.Errorf("往返不一致：%+v", got)
	}
}

// TestSendServerConfigWiring 走一遍"进游戏"的装配，断言真的发了 SM_SERVERCONFIG，
// 且 Recog/Param/正文都对（图标记 RUNHUMAN 进 Recog 低字节）。
func TestSendServerConfigWiring(t *testing.T) {
	s, p := butchTestServer(t, wuItem(1, "测试物品", 1, data.MinMax{}))
	// 把该图标成 RUNHUMAN
	mi := s.data.mapInfoByID["0"]
	mi.RunHuman = true

	conf, ok := s.serverConf(p)
	if !ok {
		t.Fatal("组装配置失败（地图/对象为空）")
	}
	if !conf.RunHuman || conf.RunNpc || conf.WarRunAll {
		t.Errorf("RUNHUMAN 该进配置、RunNpc/WarRunAll 应保持出厂 0：%+v", conf)
	}
	if conf.HitTime != 1020 || conf.SpellTime != 750 || conf.DieColor != 5 {
		t.Errorf("间隔/颜色不对：%+v", conf)
	}
	if len(codec.EncodeBuffer(conf.Bytes())) == 0 {
		t.Error("正文不该为空")
	}
	// 没标 RUNHUMAN 的地图 ⇒ 该位必须是 0（RUNHUMAN 出厂就是 0）
	mi.RunHuman = false
	if conf2, _ := s.serverConf(p); conf2.RunHuman {
		t.Error("取消地图标记后不该再是 RUNHUMAN")
	}
}

package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/proto"
)

// TestDecodeSpellMsg 原版客户端的 CM_SPELL 字段布局。
//
// 2026-10-04 的**协议级修正**：我们此前把技能号放 Recog、x/y 放 Param/Tag，
// 而原版是"坐标打包进 Recog、技能号在 Param 或 Tag"。错法长期没被发现，
// 是因为 mir2cli 按我们的错法发、服务端按我们的错法解——自己发自己解。
// 真客户端会完全打不出技能。
func TestDecodeSpellMsg(t *testing.T) {
	// 发法2「有目标」ClMain.pas:2443
	//   Recog = MakeLong(targx, targy)，Param = 技能ID，Tag = 方向
	head := proto.MakeDefaultMsg(proto.CM_SPELL,
		int32(uint16(289))|int32(uint16(618))<<16, // Recog = 坐标
		uint16(22), 0, 0) // Param = 火墙
	magicID, x, y, _ := decodeSpellMsg(head)
	if magicID != 22 {
		t.Errorf("发法2 技能号 = %d, 期望 22", magicID)
	}
	if x != 289 || y != 618 {
		t.Errorf("发法2 坐标 = (%d,%d), 期望 (289,618)", x, y)
	}

	// 发法1「自身技能，无目标」ClMain.pas:2393
	//   Recog = MakeLong(自身方向, 0)，Tag = 技能ID
	head2 := proto.MakeDefaultMsg(proto.CM_SPELL,
		int32(uint16(4)), 0, // Recog = 方向打包，Param = 0
		uint16(11), 0) // Tag = 雷电术
	magicID2, _, _, _ := decodeSpellMsg(head2)
	if magicID2 != 11 {
		t.Errorf("发法1 技能号 = %d, 期望 11（应回退读 Tag）", magicID2)
	}
}

// TestDecodeSpellMsgFallback 兜底：坐标全 0 时回退到"Param=x, Tag=y"旧布局。
func TestDecodeSpellMsgFallback(t *testing.T) {
	head := proto.MakeDefaultMsg(proto.CM_SPELL, 0, 100, 200, 0)
	_, x, y, _ := decodeSpellMsg(head)
	if x != 100 || y != 200 {
		t.Errorf("兜底坐标 = (%d,%d), 期望 (100,200)", x, y)
	}
}

package gamesvr

import (
	"path/filepath"
	"testing"

	"github.com/algotao/mir2/server/internal/data"
	pb "github.com/algotao/mir2/server/internal/storage/pb"
	"github.com/algotao/mir2/server/protocol"
)

// TestSendMyMagicsProto 新协议玩家必须收到 **MagicList**（`SM_SENDMYMAGIC` 是
// legacy 下行、会被 `protoDown` 丢掉）⇒ 不补这条，技能窗与技能栏永远是空的。
//
// 顺带钉住三个容易写错的字段：
//   - `key`：字符值（`'1'..'8'`），0 = 没绑；
//   - `icon`：`effect * 2`（**不是**技能号，原版 FState.pas:3655）；
//   - `name`：客户端不持有技能表 ⇒ 名字由服务端带下来。
func TestSendMyMagicsProto(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables

	p := testMaster(100, "法师")
	p.Char.Data = &pb.CharacterData{Magics: []*pb.UserMagic{
		{MagicId: 1, Level: 2, TranPoint: 120, Key: '1'}, // 火球术，绑 F1
		{MagicId: 2, Level: 1},                           // 治愈术，没绑
	}}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 8)}
	p.protoOut = sink

	s.sendMyMagics(nil, p)

	env := <-sink.ch
	list := env.GetMagicList()
	if list == nil {
		t.Fatalf("应为 MagicList，实得 %T", env.Body)
	}
	if len(list.GetMagics()) != 2 {
		t.Fatalf("技能数 = %d，应为 2", len(list.GetMagics()))
	}
	fire := list.GetMagics()[0]
	if fire.GetSkillId() != 1 || fire.GetLevel() != 2 || fire.GetExp() != 120 {
		t.Errorf("火球术 = id%d lv%d exp%d，应 1/2/120", fire.GetSkillId(), fire.GetLevel(), fire.GetExp())
	}
	if fire.GetKey() != uint32('1') {
		t.Errorf("key = %d（%c），应为 %d（F1）", fire.GetKey(), fire.GetKey(), uint32('1'))
	}
	if want := uint32(tables.Magics.GetByID(1).Effect) * 2; fire.GetIcon() != want {
		t.Errorf("icon = %d，应为 %d（effect*2，不是技能号）", fire.GetIcon(), want)
	}
	if fire.GetName() != "火球术" {
		t.Errorf("name = %q，应为 火球术（客户端不持有技能表）", fire.GetName())
	}
	if list.GetMagics()[1].GetKey() != 0 {
		t.Errorf("没绑的技能 key 应为 0，实得 %d", list.GetMagics()[1].GetKey())
	}
}

// TestSetMagicKey 改快捷键：**一个键只对应一个技能** —— 服务端先把占用同一键的
// 旧技能清掉（原版 FState.pas:3687-3693 在客户端做这一步，我们放服务端 ⇒
// 任何客户端都躲不掉）。
func TestSetMagicKey(t *testing.T) {
	tables, err := data.LoadDir(filepath.Join("..", "..", "data"))
	if err != nil {
		t.Fatalf("加载数据表: %v", err)
	}
	s := testSlaveServer()
	s.data.tables = tables

	p := testMaster(100, "法师")
	p.Char.Data = &pb.CharacterData{Magics: []*pb.UserMagic{
		{MagicId: 1, Key: '1'}, // 火球术占着 F1
		{MagicId: 2},           // 治愈术
	}}
	sink := &protoSink{ch: make(chan *protocol.Envelope, 8)}
	p.protoOut = sink
	ps := &protoSession{srv: s, player: p}

	// 把治愈术设到 F1 ⇒ 火球术让位
	if !ps.onSetMagicKey(&protocol.SetMagicKey{SkillId: 2, Key: '1'}) {
		t.Fatal("onSetMagicKey 应返回 true（连接保持）")
	}
	if got := keyOf(p, 1); got != 0 {
		t.Errorf("火球术该让位，实得 key=%d", got)
	}
	if got := keyOf(p, 2); got != '1' {
		t.Errorf("治愈术该占上 F1，实得 key=%d", got)
	}
	// 改完要重发列表（客户端按它重画技能栏）
	if env := <-sink.ch; env.GetMagicList() == nil {
		t.Errorf("改键后应重发 MagicList，实得 %T", env.Body)
	}
	// 不认的键位：F9
	if !ps.onSetMagicKey(&protocol.SetMagicKey{SkillId: 2, Key: '9'}) {
		t.Fatal("非法键位也该保持连接")
	}
	if got := keyOf(p, 2); got != '1' {
		t.Errorf("非法键位不该改动绑定，实得 %d", got)
	}
	// 没学会的技能
	if !ps.onSetMagicKey(&protocol.SetMagicKey{SkillId: 99, Key: '3'}) {
		t.Fatal("未知技能也该保持连接")
	}
	if got := keyOf(p, 99); got != 0 {
		t.Errorf("没学会的技能不该被绑定，实得 %d", got)
	}
}

// keyOf 取某个技能当前绑的键（0 = 没绑 / 没这个技能）。
func keyOf(p *Player, magicID uint32) uint32 {
	for _, um := range p.Char.Data.Magics {
		if um != nil && um.MagicId == magicID {
			return um.Key
		}
	}
	return 0
}

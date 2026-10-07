package gamesvr

import (
	"context"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/guild"
)

// TestGuildWarReQuest 掌门宣战（原版 `ReQuestGuildWar`，ObjBase.pas:26722-26761）：
// 只有掌门能发、不能向自己宣战、目标行会必须存在、成功后**双向**写战争表。
//
// ⚠️ 原版这里**不扣费**（费用在 NPC 脚本层，且我们也不扣）—— 这条也钉一下。
func TestGuildWarReQuest(t *testing.T) {
	s, gm := guardTestServer()
	makeGuild(t, gm, "甲会", "掌门甲")
	makeGuild(t, gm, "乙会", "掌门乙")

	// ⚠️ 战争时长来自配置（生产路径由命令行开关给），测试服里是零值 ⇒
	// `until = now` 会让战争"立刻过期"，`AtWar` 永远为假。必须自己设上。
	s.social.guildCfg.warDuration = time.Hour

	chief := newTestPlayer(1, "掌门甲", entity.JobWarr)
	s.world.players[1] = chief
	member := newTestPlayer(2, "小弟甲", entity.JobWarr)
	s.world.players[2] = member
	if err := gm.AddMember(context.Background(), "甲会", "小弟甲"); err != nil {
		t.Fatalf("加成员失败: %v", err)
	}
	atWar := func() bool {
		return guild.AtWar(gm.Find("甲会"), gm.Find("乙会"), time.Now())
	}

	// ① 非掌门 ⇒ 拒绝
	s.requestGuildWar(nil, member, "乙会")
	if atWar() {
		t.Error("非掌门宣战不该生效")
	}
	// ② 向自己宣战 ⇒ 拒绝
	s.requestGuildWar(nil, chief, "甲会")
	if atWar() {
		t.Error("向自己宣战不该生效")
	}
	// ③ 目标不存在 ⇒ 拒绝
	s.requestGuildWar(nil, chief, "不存在的会")
	if atWar() {
		t.Error("目标不存在时不该生效")
	}
	// ④ 正常 ⇒ 双向写入
	s.requestGuildWar(nil, chief, "乙会")
	a, b := gm.Find("甲会"), gm.Find("乙会")
	if !guild.AtWar(a, b, time.Now()) {
		t.Fatal("宣战后（甲→乙）该在战争表里")
	}
	if !guild.AtWar(b, a, time.Now()) {
		t.Fatal("宣战后（乙→甲）也该在战争表里 —— 原版是双向写入")
	}
}

// TestGuildWarAllyExclusive 盟友之间不能宣战（原版 `AddWarGuild` 开头
// `if not IsAllyGuild` ⇒ 结盟与宣战互斥）。
func TestGuildWarAllyExclusive(t *testing.T) {
	s, gm := guardTestServer()
	makeGuild(t, gm, "甲会", "掌门甲")
	makeGuild(t, gm, "乙会", "掌门乙")
	if err := gm.AddAlly(context.Background(), "甲会", "乙会"); err != nil {
		t.Fatalf("结盟失败: %v", err)
	}
	chief := newTestPlayer(1, "掌门甲", entity.JobWarr)
	s.world.players[1] = chief

	s.requestGuildWar(nil, chief, "乙会")
	if guild.AtWar(gm.Find("甲会"), gm.Find("乙会"), time.Now()) {
		t.Error("盟友之间不该能宣战")
	}
}

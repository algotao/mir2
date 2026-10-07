package guild

import (
	"reflect"
	"testing"
	"time"

	"github.com/algotao/mir2/server/internal/storage"
)

// TestCloneCoversAllFields 用反射兜住"加了字段忘了加进 Clone"。
//
// ⚠️ Clone 是**手写深拷贝**：漏字段不会编译报错，表现是"改了但读出来还是旧值"。
// 2026-10-05 加争霸赛字段时就踩过：`@StartContest` 调用成功（写库正常），
// 但击杀时从缓存读到的 `TeamFight` 还是 false ⇒ 永远不加分。
func TestCloneCoversAllFields(t *testing.T) {
	src := &storage.Guild{
		Name:           "甲",
		Notice:         []string{"一", "二"},
		Allies:         []string{"乙"},
		Wars:           []storage.GuildWar{{Name: "丙", EndAt: time.Unix(123, 0)}},
		Ranks:          []storage.GuildRank{{No: 1, Name: "掌门", Members: []string{"甲"}}},
		EnableAuthAlly: true,
		ContestPoint:   700,
		TeamFight:      true,
		TeamFightDead:  []storage.GuildTeamFightMember{{Name: "甲", DieCount: 2, Point: 300}},
	}
	c := Clone(src)
	if !reflect.DeepEqual(src, c) {
		t.Errorf("Clone 没有覆盖全部字段：\n源  = %+v\n副本= %+v", src, c)
	}
	// 深拷贝：改副本不该动到源
	c.TeamFightDead[0].Point = 0
	c.Notice[0] = "改"
	c.Ranks[0].Members[0] = "改"
	if src.TeamFightDead[0].Point != 300 || src.Notice[0] != "一" || src.Ranks[0].Members[0] != "甲" {
		t.Error("Clone 是浅拷贝：改副本影响到了源")
	}
}

// TestTeamFightScoring 守住争霸赛的三个行为（Guild.pas:771-800、1272-1286）。
func TestTeamFightScoring(t *testing.T) {
	g := &storage.Guild{Name: "甲"}
	AddTeamFightMember(g, "甲")

	// ① 没开赛时，阵亡/击杀都不记（`if not boTeamFight then Exit`）
	if TeamFightWhoDead(g, "甲") {
		t.Error("没开赛时不该记阵亡")
	}
	if TeamFightWhoWinPoint(g, "甲", 100) {
		t.Error("没开赛时不该记分")
	}
	if g.ContestPoint != 0 {
		t.Errorf("积分 = %d，期望 0", g.ContestPoint)
	}

	// ② 开赛后：清零积分、登记成员、关闭时保留积分与成员表
	g.ContestPoint = 999
	g.TeamFightDead = []storage.GuildTeamFightMember{{Name: "旧", Point: 1}}
	StartTeamFight(g)
	if !g.TeamFight || g.ContestPoint != 0 || len(g.TeamFightDead) != 0 {
		t.Fatalf("StartTeamFight 后 = %+v，期望开关开、积分清零、成员表清空", g)
	}
	AddTeamFightMember(g, "甲")
	AddTeamFightMember(g, "甲") // 去重
	if len(g.TeamFightDead) != 1 {
		t.Fatalf("成员表 = %+v，期望 1 条（同名去重）", g.TeamFightDead)
	}

	// ③ 记一次阵亡 + 一次击杀 +100
	if !TeamFightWhoDead(g, "甲") || g.TeamFightDead[0].DieCount != 1 {
		t.Errorf("阵亡计数 = %+v，期望 DieCount=1", g.TeamFightDead[0])
	}
	if !TeamFightWhoWinPoint(g, "甲", 100) {
		t.Error("开赛后应记分")
	}
	if g.ContestPoint != 100 || g.TeamFightDead[0].Point != 100 {
		t.Errorf("行会积分/个人得分 = %d/%d，期望 100/100",
			g.ContestPoint, g.TeamFightDead[0].Point)
	}
	// 不在表里的成员：行会积分照样加（原版先 Inc 再找名单），个人得分不记
	if !TeamFightWhoWinPoint(g, "乙", 100) {
		t.Error("开赛后应记分（哪怕人不在成员表里）")
	}
	if g.ContestPoint != 200 || TeamFightMember(g, "乙") != nil {
		t.Errorf("积分 = %d、乙是否入表 = %v，期望 200 / false",
			g.ContestPoint, TeamFightMember(g, "乙") != nil)
	}

	EndTeamFight(g)
	if g.TeamFight {
		t.Error("EndTeamFight 后开关应为关")
	}
	if g.ContestPoint != 200 || len(g.TeamFightDead) != 1 {
		t.Errorf("EndTeamFight 不该清积分/成员表：积分 %d、成员 %d", g.ContestPoint, len(g.TeamFightDead))
	}
}

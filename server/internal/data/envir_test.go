package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadMapInfoFlags 守住 mapinfo.txt 的两种属性写法。
//
// 官方格式是**属性与段头在同一行**（`[G001 质询屋 0] SAFE DAY QUIZ`），
// 早期实现遇到 `[` 就跳过整行，导致一张 SAFE 都读不到、安全区完全失效。
// 独立成行的属性（`[0162 监狱 0]` 后另起一行 `SAFE`）也要认。
func TestLoadMapInfoFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapinfo.txt")
	body := ";注释\n" +
		"\n" +
		"[0 比奇省 0]\n" +
		"[G001 质询屋 0] SAFE DAY QUIZ\n" + // 属性同行
		"[0162  监狱 0] SAFE\n" + // 属性同行
		"[G004 热血足球场 0]\n" +
		"DAY FIGHT\n" + // 属性独立成行
		"[0150 皇宫 0] FIGHT3 SAFE\n" +
		"[NOPE]\n" // 只有一个字段，应被跳过
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := LoadMapInfo(path)
	if err != nil {
		t.Fatalf("LoadMapInfo: %v", err)
	}
	byID := make(map[string]*MapInfo, len(infos))
	for _, mi := range infos {
		byID[mi.ID] = mi
	}
	if len(byID) != 5 {
		t.Errorf("应解析出 5 张图，实际 %d（%v）", len(byID), byID)
	}
	cases := []struct {
		id                        string
		safe, fight, fight3, quiz bool
	}{
		{"0", false, false, false, false},
		{"G001", true, false, false, true},  // 同行属性（SAFE DAY QUIZ）
		{"0162", true, false, false, false}, // 同行属性
		{"G004", false, true, false, false}, // 独立行属性（DAY FIGHT）
		{"0150", true, false, true, false},  // 同行两个属性
	}
	for _, c := range cases {
		mi := byID[c.id]
		if mi == nil {
			t.Errorf("缺少地图 %s", c.id)
			continue
		}
		if mi.Safe != c.safe || mi.FightZone != c.fight || mi.Fight3Zone != c.fight3 ||
			mi.Quiz != c.quiz {
			t.Errorf("%s: Safe/Fight/Fight3/Quiz = %v/%v/%v/%v, 期望 %v/%v/%v/%v",
				c.id, mi.Safe, mi.FightZone, mi.Fight3Zone, mi.Quiz,
				c.safe, c.fight, c.fight3, c.quiz)
		}
	}
	if _, ok := byID["NOPE"]; ok {
		t.Error("字段不足的段头应被跳过")
	}
}

// TestLoadMapInfoFullFlagTable 钉住官方标记表（`LocalDB.pas:560-760`）的全量解析。
//
// ⚠️ 这条是**补的**：2026-10-06 做"地图标记全表解析"时，提交信息里写了
// "`internal/data` 全表解析测试"，但当时只有下面那条老的 `TestLoadMapInfoFlags`
// （只覆盖 SAFE/DAY/FIGHT/FIGHT3/QUIZ 五个）——这里把它补齐，并覆盖 `KEY(值)` 形式。
func TestLoadMapInfoFullFlagTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mapinfo.txt")
	body := strings.Join([]string{
		// 官方写法：段头 + 同行属性串；带参数的关键字是 `KEY(值)`。
		"[F001 行会战争地图1 0] FIGHT3 DARK",
		"[0 比奇省 0] SAFE NORECALL NOGUILDRECALL NODEARRECALL NOMASTERRECALL",
		"[0162 监狱 0] NODRUG NODROPITEM NOTHROWITEM NOPOSITIONMOVE NORANDOMMOVE",
		"[M1 矿区 0] MINE MINE2 NEEDHOLE DAY RUNHUMAN RUNMON",
		"[Q1 活动区 0] QUIZ NOCHAT(2)",
		"[E1 双倍区 0] EXPRATE(200) MUSIC(42) NEEDSET_ON(3)",
		"[E2 惩罚区 0] PKWINLEVEL(2) PKWINEXP(500) PKLOSTLEVEL(1) PKLOSTEXP(300)",
		"[E3 掉血区 0] DECHP(10/5) INCHP(3/7) NORECONNECT(0) NEEDSET_OFF(4)",
		"[E4 元宝区 0] DECGAMEGOLD(60) INCGAMEPOINT(60) NOHORSE", // 识别但不消费
		"[E5 未知 0] UNKNOWN_KEY SOMETHING(1)",                  // 未知 token 应被忽略
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := LoadMapInfo(path)
	if err != nil {
		t.Fatalf("LoadMapInfo: %v", err)
	}
	byID := make(map[string]*MapInfo, len(infos))
	for _, mi := range infos {
		byID[mi.ID] = mi
	}
	if len(byID) != 10 {
		t.Fatalf("应解析出 10 张图，实际 %d", len(byID))
	}

	// 逐条断言：一张图只带它该有的标记（顺带证明不会串到别的图上）。
	check := func(id string, fn func(t *testing.T, mi *MapInfo)) {
		t.Helper()
		mi := byID[id]
		if mi == nil {
			t.Fatalf("没解析出地图 %s", id)
		}
		fn(t, mi)
	}

	check("F001", func(t *testing.T, mi *MapInfo) {
		if !mi.Fight3Zone || !mi.Darkness {
			t.Errorf("F001 = %+v，期望 FIGHT3+DARK", mi)
		}
	})
	check("0", func(t *testing.T, mi *MapInfo) {
		if !mi.Safe || !mi.NoRecall || !mi.NoGuildRecall || !mi.NoDearRecall || !mi.NoMasterRecall {
			t.Errorf("地图 0 的召集类标记没全解析：%+v", mi)
		}
	})
	check("0162", func(t *testing.T, mi *MapInfo) {
		if !mi.NoDrug || !mi.NoDropItem || !mi.NoThrowItem || !mi.NoPositionMove || !mi.NoRandomMove {
			t.Errorf("地图 0162 的禁制类标记没全解析：%+v", mi)
		}
	})
	check("M1", func(t *testing.T, mi *MapInfo) {
		if !mi.Mine || !mi.Mine2 || !mi.NeedHole || !mi.DayLight || !mi.RunHuman || !mi.RunMon {
			t.Errorf("矿区 M1 的标记没全解析：%+v", mi)
		}
	})
	check("Q1", func(t *testing.T, mi *MapInfo) {
		if !mi.Quiz || !mi.NoChat || mi.ChatLevel != 2 {
			t.Errorf("Q1 应 QUIZ + NOCHAT(2)，实际 %+v", mi)
		}
	})
	check("E1", func(t *testing.T, mi *MapInfo) {
		if mi.ExpRate != 200 || !mi.Music || mi.MusicID != 42 || !mi.NeedSetOn || mi.NeedSetID != 3 {
			t.Errorf("E1 的带参关键字没解析对：%+v", mi)
		}
	})
	check("E2", func(t *testing.T, mi *MapInfo) {
		if !mi.PKWinLevelSet || mi.PKWinLevel != 2 || !mi.PKWinExpSet || mi.PKWinExp != 500 ||
			!mi.PKLostLevelSet || mi.PKLostLevel != 1 || !mi.PKLostExpSet || mi.PKLostExp != 300 {
			t.Errorf("E2 的 PK 地图覆盖没解析对：%+v", mi)
		}
	})
	check("E3", func(t *testing.T, mi *MapInfo) {
		if !mi.DecHPSet || mi.DecHPPoint != 10 || mi.DecHPTime != 5 ||
			!mi.IncHPSet || mi.IncHPPoint != 3 || mi.IncHPTime != 7 ||
			!mi.NoReconnect || mi.ReconnectMap != "0" || !mi.NeedSetOff || mi.NeedSetID != 4 {
			t.Errorf("E3 的 DECHP/INCHP/NORECONNECT 没解析对：%+v", mi)
		}
	})
	check("E4", func(t *testing.T, mi *MapInfo) {
		// GeeM2 那三个识别但不消费：**不能**因此把别的标记带进来。
		if mi.ExpRate != 0 || mi.NoChat || mi.NoPositionMove {
			t.Errorf("E4 只该识别不消费，实际 %+v", mi)
		}
	})
	check("E5", func(t *testing.T, mi *MapInfo) {
		if mi.Safe || mi.FightZone || mi.ExpRate != 0 || mi.NoChat {
			t.Errorf("未知 token 应被忽略，实际 %+v", mi)
		}
	})
}

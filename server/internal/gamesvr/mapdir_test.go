package gamesvr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveMapDir 守住"地图目录找不到就换兄弟目录"这条兜底。
//
// 起因：用户照 README 抄 `-map-dir $WS/mir2c/map`（`$WS` 是**文档写法**、不是环境变量），
// 实际传进去的是 `/mir2c/map` ⇒ 地图全加载失败、回退成空图 ⇒ NPC/出生点全部越界不生成，
// 表现成"地图上怎么没有 NPC"。这条兜底让照抄命令也能跑起来。
func TestResolveMapDir(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "根本没这个目录")

	// ① 目录存在 ⇒ 原样返回（不折腾）
	if got := resolveMapDir(tmp); got != tmp {
		t.Fatalf("存在的目录该原样返回：%q != %q", got, tmp)
	}

	// ② 显式环境变量优先
	alt := t.TempDir()
	t.Setenv("MIR2_MAP_DIR", alt)
	t.Setenv("MIR2C_DATA", "")
	if got := resolveMapDir(missing); got != alt {
		t.Fatalf("该退回 $MIR2_MAP_DIR：%q != %q", got, alt)
	}

	// ③ 其次认客户端资产旁边那套 `map/`（`$MIR2C_DATA` = …/mir2c/data ⇒ 兄弟 map）
	cdata := filepath.Join(t.TempDir(), "mir2c", "data")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(cdata), "map"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIR2_MAP_DIR", "")
	t.Setenv("MIR2C_DATA", cdata)
	want := filepath.Join(filepath.Dir(cdata), "map")
	if got := resolveMapDir(missing); got != want {
		t.Fatalf("该退回 $MIR2C_DATA 旁边的 map/：%q != %q", got, want)
	}

	// ④ 候选全落空 ⇒ **原样返回**（由调用方告警），不许静默换目录
	t.Setenv("MIR2_MAP_DIR", "")
	t.Setenv("MIR2C_DATA", "")
	t.Chdir(t.TempDir()) // 让"兄弟目录"候选也落空
	if got := resolveMapDir(missing); got != missing {
		t.Fatalf("找不到候选该原样返回（宁可见到告警，也不要悄悄换图）：%q != %q", got, missing)
	}
}

package gamesvr

import (
	"testing"

	"github.com/algotao/mir2/server/internal/data"
)

// TestNpcRaceAppr 守住"NPC 的 (种族, 外观) 怎么给客户端"这条（用户 2026-10-09 第 1 条：
// NPC 显示不对）。
//
// 客户端拿这两个值去 `Npc.wzl` 取图：块起点 = `GetNpcOffset(appr)`、
// 动作表 = `GetRaceByPM(race, appr)`（`Actor.pas:2866-2896/3028-3046`）。
// 两条数据来源不一样，别写反：
//
//	· 商人（merchant.txt）那一列存的是**外观**（"主要部分"），种族一律 50（商人）；
//	· `Npcs.txt` 的 `Race` 是真种族、`Body` 是外观。
func TestNpcRaceAppr(t *testing.T) {
	// 屠夫：merchant.txt 里 `1Bme 0102 9 7 屠夫 0 11 0` ⇒ 外观 11
	if race, appr := npcRaceAppr(&data.NPC{IsMerchant: true, RaceImg: 11}); race != 50 || appr != 11 {
		t.Fatalf("商人 (race, appr) = (%d, %d)，want (50, 11)", race, appr)
	}
	// Npcs.txt 的：各自两列
	if race, appr := npcRaceAppr(&data.NPC{Race: 10, Body: 23}); race != 10 || appr != 23 {
		t.Fatalf("普通 NPC (race, appr) = (%d, %d)，want (10, 23)", race, appr)
	}
}

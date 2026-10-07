package gamesvr

import (
	"github.com/algotao/mir2/server/internal/entity"
	"testing"
	"time"
)

// TestSearchHumanPermissionBackdoor 守住探测项链的两个 GM 权限后门
// （原版 ObjBase.pas:14358 `m_btPermission >= 6`、:14363 `>= 3`）。
//
//	权限 ≥ 6：**不戴项链**也能用（否则回 "您现在还无法使用此功能！！！"）
//	权限 ≥ 3：**跳过** 10 秒冷却
//
// 判定结果用 `p.lastProbeAt` 观察：只有真正走到"执行探测"那一步才会被刷新
// （被门挡住或还在冷却里都不会动它）。
func TestSearchHumanPermissionBackdoor(t *testing.T) {
	// 目标玩家（探测要能查到他才算走通）
	srv := &Server{}
	victim := newTestPlayer(2, "被探测者", entity.JobWarr)
	victim.Obj.SetMapRef(nil) // mapDescOf 对 nil 地图安全（见其实现）
	srv.world.players = map[uint32]*Player{victim.Obj.ID: victim}

	probe := func(perm uint8, necklace bool) *Player {
		p := newTestPlayer(1, "探测者", entity.JobWarr)
		p.permission = perm
		p.equipSpecials.probeNecklace = necklace
		return p
	}
	// ① 普通人（权限 0）+ 没戴项链 ⇒ 被挡（lastProbeAt 不动）
	p := probe(0, false)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	if !p.lastProbeAt.IsZero() {
		t.Error("权限 0 且没戴项链时不该执行探测（原版回\"您现在还无法使用此功能！！！\"）")
	}
	// ② 权限 6 ⇒ 不戴项链也能用
	p = probe(permissionProbe, false)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	if p.lastProbeAt.IsZero() {
		t.Error("权限 ≥ 6 的 GM 不戴项链也应能用（m_btPermission >= 6 后门）")
	}
	// ③ 戴了项链的普通人 ⇒ 能用
	p = probe(0, true)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	if p.lastProbeAt.IsZero() {
		t.Error("戴了探测项链的普通人应能用")
	}
	// ④ 冷却：普通人连着用两次，第二次不动（10 秒内）
	first := p.lastProbeAt
	time.Sleep(5 * time.Millisecond)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	if !p.lastProbeAt.Equal(first) {
		t.Error("普通人在 10 秒冷却内不该能再用")
	}
	// ⑤ 权限 3 ⇒ 跳过冷却（连用两次都刷新）
	p = probe(permissionProbeCooldown, true)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	first = p.lastProbeAt
	time.Sleep(5 * time.Millisecond)
	srv.cmdSearchHuman(p, []string{"被探测者"})
	if p.lastProbeAt.Equal(first) {
		t.Error("权限 ≥ 3 应跳过 10 秒冷却（m_btPermission >= 3 后门）")
	}
}

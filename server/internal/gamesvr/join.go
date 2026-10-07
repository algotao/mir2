// 进世界（进图）的共用部分：**角色租约申请** + 地图定位 + 实体构造 + 装配 Player。
//
// 为什么单独成文件：**两条入口共用它** ——
//
//	legacy：`#<序号>…!` 帧 + 6bit 包（`session.go` 的 authenticate）
//	新协议：`[u32 长度][Envelope]`（`netproto.go`）
//
// 两处各写一份的话，"同一名角色进游戏"的规则（地图回退、坐标不可走时回中心、
// ActorId 分配、租约接管顺序）迟早会漂移，而这类漂移只会在线上表现为"进游戏位置不对"
// 或"顶号没顶掉"。本文件的内容是从 `authenticate` 的尾段**逐行搬出**（R-7：搬迁不改语义）。
package gamesvr

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/algotao/mir2/server/internal/entity"
	"github.com/algotao/mir2/server/internal/proto"
	"github.com/algotao/mir2/server/internal/storage"
)

// sessionStageSelected 是"已选好角色、可以进游戏"的会话阶段。
//
// 取值 4 来自 accountsvc 的 `Stage` 分档；SQLite 层的 `IssueGameTicket` / `ClaimGameLease`
// 也硬编码了 `s.stage=4`（`storage/sqlite/store.go`）—— 所以说它是**存储层的约定**，
// 不只是某个服务的内部常量。新协议入口在自己选角后会把它推进到这一档。
const sessionStageSelected = 4

// claimGameLease 申请角色写租约（进游戏前必须拿到，否则同一角色会被两个连接同时跑）。
//
// 两步是**有意的顺序**，原注释一并搬来：
//  1. 同进程内先主动关闭同角色的旧连接；
//  2. 再让旧 gamesvr 通过 `account_sessions` 的 generation 失效并释放租约，
//     之后才 `ClaimGameLease`（跨进程的接管靠 `sessionLeaseLoop` 轮询）。
func (s *Server) claimGameLease(ctx context.Context, account string, sessionID int32, chrName string) error {
	if !s.takeoverLocalPlayer(chrName, sessionID) {
		return fmt.Errorf("等待本地旧角色连接清理超时")
	}
	// 新登录先让旧 gamesvr 发现 account_sessions 的新 SID；旧连接会在租约
	// 轮询中关闭、完成最终存档并释放 game_leases。只在旧租约释放/过期后才接管。
	deadline := time.Now().Add(gameLeaseWait)
	for {
		claimed, err := s.store.Sessions().ClaimGameLease(
			ctx, account, sessionID, chrName, time.Now(), time.Now().Add(gameLeaseDuration))
		if err != nil {
			return fmt.Errorf("取得游戏租约失败: %w", err)
		}
		if claimed {
			return nil
		}
		current, err := s.store.Sessions().IsCurrent(ctx, account, sessionID, time.Now())
		if err != nil || !current {
			return fmt.Errorf("进入游戏凭证已失效")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("账号仍在其他游戏连接中")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// joinWorld 把一份角色存档放进世界：定位地图与坐标、建实体、装配 Player。
//
// ⚠️ 只做"构造"，**不**注册进 `world.players` —— 那一步由调用方在合适的时机做，
// 因为它要和各自的收尾逻辑配对（两条入口都是"注册后 defer 下线清理"）。
func (s *Server) joinWorld(chr *storage.Character, sessionID int32, ip string) *Player {
	m := s.world.defaultMap
	if mm, err := s.world.maps.Get(chr.Data.CurMap); err == nil {
		m = mm
	} else {
		log.Printf("角色 %s 的地图 %s 加载失败（%v），回退到默认地图",
			chr.Name, chr.Data.CurMap, err)
	}
	s.activateSpawnMap(m.Name)
	x, y := int(chr.Data.CurX), int(chr.Data.CurY)
	if !m.CanWalk(x, y) {
		x, y = m.Width()/2, m.Height()/2
	}

	// ⚠️ `entity.Object` 的可变态不导出（自带锁，见 entity/object.go），
	// 跨包构造统一走 `entity.NewObject`。
	obj := entity.NewObject(s.world.actorSeq.Add(1), chr.Name, m, x, y,
		uint8(chr.Data.Dir), proto.MakeFeature(0, 0, uint8(chr.Data.Hair), 0))
	return &Player{
		Obj:         obj,
		Char:        chr,
		Limiter:     entity.NewMoveLimiter(),
		IP:          ip,
		sessionID:   sessionID,
		cleanupDone: make(chan struct{}),
		visible:     entity.NewViewTracker(),
		// 原版默认值：不允许被加进行会（ObjBase.pas:1271），
		// 但允许接收行会聊天（ObjBase.pas:1329）。
		allowGuild:   false,
		banGuildChat: true,
	}
}

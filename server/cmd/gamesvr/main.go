// Command gamesvr 是游戏服务（原 M2Server）。
//
// 已实现：认证 → 公告握手 → 进游戏 → 走路/转向 → 视野进出 → 装备背包
//
//	→ 怪物生成与游荡 → 玩家攻击怪物（伤害/死亡/经验）
//
// 关键协议均对照原版客户端解析代码核准，见各函数注释。
//
// 用法：
//
//	go run ./cmd/gamesvr -db /tmp/mir2go.db -data ./data

package main

import (
	// 内嵌 IANA 时区库（见 internal/tz 的说明）：scratch 镜像里没有
	// /usr/share/zoneinfo，不编进去的话 `TZ=Asia/Shanghai` 会被静默忽略（退回 UTC）。
	_ "github.com/algotao/mir2/server/internal/tz"

	"github.com/algotao/mir2/server/internal/gamesvr"
)

func main() { gamesvr.Main() }

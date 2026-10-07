// Package gamesvr 是游戏服（原 M2Server）的实现。
//
// 分层（见 docs/service-architecture.md §8）：
//
//	server.go      Server / Player 类型、存档、核心包发送辅助
//	run.go         进程入口 Main()：flag、数据加载、装配、起循环
//	session.go     连接与会话：认证、消息分发、进出游戏、移动/转向
//	loops.go       各条周期任务（刷怪、怪物 AI 心跳、恢复、自动存档）
//	spawn.go       刷怪与地面物品
//	monsterai.go   怪物 AI：选目标、攻击、受击结算、掉落
//	view.go        视野进出与广播（出现/消失/移动）
//	combat.go      近战攻击与伤害结算
//	item.go        背包、拾取、使用、装备、物品形态特效
//	death.go       死亡与复活
//	send.go        包发送与属性编码
//
// 领域逻辑（行会/城堡/PvP/组队/脚本/宠物…）分别在同名文件里；
// 更细的划分边界见上述文档 §8.3。
package gamesvr

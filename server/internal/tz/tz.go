// Package tz 把 IANA 时区数据库**编进二进制**。
//
// 为什么需要它：镜像是 `scratch`（见 `Dockerfile`），里面**没有 `/usr/share/zoneinfo`**。
// 而 `TZ=Asia/Shanghai` 这种"名字"必须靠 zoneinfo 才能解析 —— 解析不了时 Go 会
// 静默退回 **UTC**，于是表现成"明明设了 TZ 却不生效"：
//
//	2026-10-06 线上就是这个症状：宿主 `date` 是 20:34 CST，容器日志却是 11:34；
//	而且昼夜相位（`gamesvr/daynight.go` 的 `gameTimePhase`）按**小时**分档
//	⇒ 日出/日落时间也跟着偏了 8 小时。
//
// `time/tzdata` 是 Go 1.15+ 自带的**内嵌时区库**（约 450KB）：只要有人在程序里
// 空导入它，`TZ` / `time.LoadLocation` / `time.Local` 在无 zoneinfo 的镜像里
// 也能正常工作（查找顺序：`$ZONEINFO` → 系统目录 → 内嵌的那份）。
//
// 五个二进制的 `main` 都空导入本包（`_ "github.com/algotao/mir2/server/internal/tz"`），
// 这样"为什么需要"只写在这一处。
package tz

import _ "time/tzdata"

# mir2

传奇（Legend of Mir 2）的**现代重制**：服务端 + 客户端同仓库。

> **🚩 接手 / 恢复工作先读 [docs/plan.md](./docs/plan.md)**，再看 [docs/decisions.md](./docs/decisions.md)。

**当前状态：仅文档，未开工。**

---

## 一句话原则

**在体感上与原版一致，在实现上完全自由。**

| 面 | 是否自由 | 说明 |
|---|---|---|
| 网络协议 | ✅ **完全自由** | 自研协议。原版报文很丑陋，**不复刻** |
| 系统架构 | ✅ **完全自由** | 进程划分、模块边界、存储，自己定 |
| 美术资产 | ❌ 必须是同一套 | 这是**内容**不是实现。**载体可自选**（见 [assets.md](./docs/assets.md)） |
| 游戏数值 | ❌ 必须与原版一致 | 物品/怪物/技能/经验表是内容，改了就变味 |
| 体感规格 | ❌ 来源仍是原版 | 遮挡顺序、动画时序、攻击间隔的**判定依据**不变 |

## 三个不自由的东西

1. **美术是内容**。你可以自由选载体（直读原 WIL / 转自研容器），但不能重新画。
2. **数值是内容**。物品属性、怪物属性、技能效果、经验表必须与原版一致。
3. **体感规格的来源是原版**。协议自由 ≠ 玩法自由。"怪物攻击间隔多少"这类问题，
   答案仍在 `$WS/mir2standard` 的源码或 `mir2go` 的行为里。

## 仓库布局

```
mir2/
├── docs/          方案与决策（唯一真源）
├── protocol/      ★ 协议单一真源（IDL/schema，两端代码生成的输入）
├── server/        Go
├── client/        Rust + SDL3
└── tools/         资产转换、协议代码生成、数据导入
```

## 开发环境与路径约定

**主力开发机：macOS（Apple Silicon / M4），原生开发，不用容器。**

| 项 | 说明 |
|---|---|
| 路径约定 | 本文档所有路径写作 **`$WS/...`**，`$WS` = 与 `mir2` 平级的兄弟目录所在的那一级 |
| 历史路径 | 这些路径原先写作容器内的 `/data/git`（已全部改写）。把整个目录拷到 Mac，**相对结构一致** |
| Rust | stable + `aarch64-apple-darwin`；SDL3 走 `sdl3` 的 `build-from-source-static`（[D-04](./docs/decisions.md)） |
| Go | **1.27.x**（`go.mod` 已定）+ **`CGO_ENABLED=0`**（[D-21](./docs/decisions.md)） |
| CMake + Xcode CLT | 构建 SDL3 用。这是**构建期**依赖，不影响"运行期零依赖" |
| 容器 / CI | **可选**：只用于出 Linux 部署产物与 CI 门禁，**不再用于日常开发** |

**⚠️ 大小写敏感必须处理**：macOS 文件系统默认**大小写不敏感**，会**掩盖**大小写 bug
（import 路径、引用的资源名），直到部署到 Linux 才炸。对策见
[D-20](./docs/decisions.md)：**建议在 Mac 上用大小写敏感的 APFS 卷放工作区**。

**迁移安全性（已实测）**：当前工作区有 **0 个**"仅差大小写"的文件/目录
⇒ 拷到大小写不敏感的文件系统**不会静默覆盖**。自查命令：

```bash
find $WS -not -path '*/.git/*' -printf '%p\n' | tr 'A-Z' 'a-z' | sort | uniq -d
```

## 参照系统（不删，用于查规格与对拍）

| 路径 | 角色 |
|---|---|
| `$WS/mir2go` | 服务端 Go 重写，行为已被 **46/46 e2e** 验证。**保留作参照系统**：抽取来源 + 对拍基准。不再加功能 |
| `$WS/mir2standard` | 官方 Delphi 服务端 + 客户端，**只读**。体感规格的唯一权威。⚠️ GBK 编码 |
| `$WS/Mir2-GeeM2` | 1.76 官方配置全集（Map 605 张、Envir 配置、脚本） |
| `$WS/OpenMir2` | C# 参考（`sql/mir2_data.sql` 是数据金矿）；⚠️ 6bit 带 XOR，不可抄 |

## 文档

| 文档 | 内容 |
|---|---|
| **[docs/plan.md](./docs/plan.md)** | **主方案**：原则、架构、仓库布局、里程碑、排期、风险 |
| **[docs/decisions.md](./docs/decisions.md)** | **决策记录**：状态（已定/待定）、依据、影响面 |
| **[docs/protocol.md](./docs/protocol.md)** | **协议设计**：分帧、IDL、消息分段、握手、防漂移 |
| **[docs/assets.md](./docs/assets.md)** | **资产策略与格式规格**：WIL/WIX/map 规格、自研容器、验证方法 |
| **[docs/server-extraction.md](./docs/server-extraction.md)** | **从 mir2go 抽取的地图**：哪些搬、哪些重写（量化） |
| [docs/legacy-analysis.md](./docs/legacy-analysis.md) | 遗留客户端实测：87k 行拆解、216 消息、28 窗口、动画面 |
| **[docs/messages.md](./docs/messages.md)** | **M0 消息清单**：216 个 `SM_*` 分档、与 mir2go 标杆对拍、28 窗口交叉核对 |

## 三条最容易踩的

1. **协议两端不可手写**。必须在 `protocol/` 有单一真源 + 代码生成，否则 Go 与 Rust
   迟早漂移，而这次**没有"原版客户端"当裁判**。
2. **体感 ≠ 分辨率**。可以提高分辨率，但**视野格数必须锁死**（原版视距是玩法平衡的一部分，
   看得更远 = 更容易）。
3. **GBK 源码不是文本**。`$WS/mir2standard` 下所有 `.pas` 都是 GBK，
   直接 grep 中文是静默 0 命中，先 `iconv -f GBK -t UTF-8`。

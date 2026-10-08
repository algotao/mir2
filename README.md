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

## 本地跑起来（登录 → 进图）

四个东西，**共用同一个 `-db`**（账号、会话、角色都在这个 SQLite 里）。

```bash
# 0) 先编三个二进制 —— 别用 `go run`：kill 它杀不掉它拉起的子进程
cd server
go build -o /tmp/mir2dev/bin/accountsvc ./cmd/accountsvc
go build -o /tmp/mir2dev/bin/mir2cli  ./cmd/mir2cli
go build -o /tmp/mir2dev/bin/gamesvr  ./cmd/gamesvr

# 1) 账号服务（⚠️ 端口见下面那条坑）
/tmp/mir2dev/bin/accountsvc -db /tmp/mir2dev/mir2go.db -data ./data \
    -login-addr :17000 -sel-addr :17100

# 2) 建账号 + 角色（`-new-char`；mir2cli 是**直接写库**建账号，
#    绕过还没实现的 CM_ADDNEWUSER —— 所以它能给你一个能用的账号）
/tmp/mir2dev/bin/mir2cli -db /tmp/mir2dev/mir2go.db \
    -login 127.0.0.1:17000 -sel 127.0.0.1:17100 \
    -user test -pass pw123 -new-char 勇士 -skip-game

# 3) 游戏服（⚠️ `-proto-addr` **默认为空 = 新协议入口是关的**）
/tmp/mir2dev/bin/gamesvr -db /tmp/mir2dev/mir2go.db -data ./data -addr :7200 \
    -proto-addr 127.0.0.1:7500 -map-dir $WS/mir2c/map -map 0

# 4) 客户端（默认开在登录界面，输账号口令回车）
cd client && MIR2_SERVER=127.0.0.1:7500 MIR2_ASSET_DIR=$WS/mir2c/data cargo run -p mir2-app
```

不开窗口、走**同一份客户端代码**验一遍：

```bash
cd client && cargo run -p mir2-e2e -- world -addr 127.0.0.1:7500 \
    -account test -password pw123 -expect-map 0
```

| 踩到的样子 | 真正的原因 / 怎么办 |
|---|---|
| 登录界面弹 `Connection refused (os error 61)` | **不是鉴权失败**（那会弹"账号或口令不正确"），是 TCP 层没人监听：`gamesvr` 没起，或者起的时候**没带 `-proto-addr`** |
| `accountsvc` 报 `bind: address already in use`（:7000） | macOS 的 **AirPlay 接收器（ControlCenter）占着 7000**。把 `-login-addr` 换成 `:17000` 之类，mir2cli 跟着改 `-login` |
| 服务端要 `<地图号>.map` 目录，可仓库里只有 `assets/map/maps.m2pk` | 用**客户端集** `$WS/mir2c/map`（[D-22](./docs/decisions.md)：两套地图以客户端集为准；它里面 `0.map` 就是边界村） |
| 起 gamesvr 刷一堆 `缺少怪物模板 "红野猪3"` | 刷怪表引用了我们数据里没有的怪，**无害**（那些刷怪点空着） |

## 音频（音乐 / 音效）

窗口里按 **`M`** 切音乐、**`N`** 切音效（原版只有这两个开关，**没有音量滑条** ——
`MShare.pas:213-214`）。终端里会打一行 `[audio] BGM = log-in-long2.wav`，
听不见的时候靠它判断到底有没有在放。

素材：原始是 `$WS/mir2c/wav`（777 个 `.wav` + **`sound.lst`** 索引表，**209 MB**）。
路径解析：`$MIR2_AUDIO_DIR` / `$MIR2C_WAV` → **`assets/audio`（产物，优先）** →
`mir2c/wav`（原件），规则在 `client/core/src/paths.rs`（与美术目录同一套约定）。

**瘦身**（`tools/wavpack/build.sh`，**209 MB → 73.5 MB，2.8×**，1.5 秒）：

```bash
tools/wavpack/build.sh              # 源自动探测 = 客户端集
tools/wavpack survey -src DIR       # 只量不改：格式分布 / 最大的文件 / 重复 / 估算
```

* 音效 → **22.05 kHz 单声道**（原版没有 pan/距离衰减 ⇒ 单声道不丢游戏信息），
  BGM 三首**保持原样**（音乐降采样听得出来）；
* 原版**从不播**的长文件不产出（省 40 MB）：`Field2.wav`、`main_theme.wav`
  —— 但**清单引用了的绝不跳**（`Game-over2.wav` 就属于这种）；
* ⚠️ **无损压缩白费**（实测 zlib 87~96%、xz 82~93%）—— 音效是宽带噪声，压不动，
  所以这条路根本没做；
* 自检两条：每个文件读回核对（头/帧数/峰值）+ **清单里源能播的编号产物一个不少**
  （实测 741 → 741）；顺手把 7 个文件的大小写按清单拼写对齐（Linux 上才找得到）。

* **规格**在 `client/core/src/sound.rs`：编号表、地形→脚步、被击中/技能/怪物编号、
  `sound.lst` 的"编号 → 文件"。全部照抄原版（`SoundUtil.pas:36-142`、`Actor.pas:2144-2396`），
  文件头有出处表；测试里逐条对齐了原版的数字列表。
* **发声**在 `client/app/src/audio.rs`：SDL3 流回调 + **软件混音**（多路叠加、BGM 循环、
  两组开关互不牵连）。用 SDL3 自带的 WAV 加载，**没有引入任何新依赖**。
* 已接上的事件：登录/选角/死亡三首场景 BGM、按钮声(103)、开门(100)、选角解冻(101)、
  走路脚步（按地形 1..32，帧 1/4 各一声）、攻击挥刀（按武器形状 50..57）、
  自己挨打惨叫（138/139）、自己死亡（144/145 + `game over2.wav`）。

| 还没接的 | 为什么 |
|---|---|
| 进图音乐 `Music/<地图音乐号>.mp3` | **素材不存在**：客户端集里**没有任何 mp3**（`.mp3/.ogg/.wma/.mid` 全 `find` 过，只有 `wav/`）；协议里 `MapDescription` 也**没有音乐号字段**（原版是服务端 `SM_MAPDESCRIPTION.Recog` 下发，`ClMain.pas:5222`）。管道位置已留好：`sound::map_music` |
| 物品/技能/金币音（106/107/108/111..118、`10000+技能号*10`） | 那几套系统还没接，没有触发点（编号与算法都已写在 `sound.rs` 里） |

## 三条最容易踩的

1. **协议两端不可手写**。必须在 `protocol/` 有单一真源 + 代码生成，否则 Go 与 Rust
   迟早漂移，而这次**没有"原版客户端"当裁判**。
2. **体感 ≠ 分辨率**。可以提高分辨率，但**视野格数必须锁死**（原版视距是玩法平衡的一部分，
   看得更远 = 更容易）。
3. **GBK 源码不是文本**。`$WS/mir2standard` 下所有 `.pas` 都是 GBK，
   直接 grep 中文是静默 0 命中，先 `iconv -f GBK -t UTF-8`。

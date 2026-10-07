# 从 mir2go 抽取的地图

> 来源：`$WS/mir2go`（63,867 行，e2e 46/46）。目标：`mir2/server/`。
> **mir2go 保留为参照系统**（D-14），不做双向同步。

---

## 1. 为什么值得抽取

`mir2go` 是**唯一**"跑得起来 + 行为被 46/46 e2e 验证过"的实现。
里面沉淀的不是代码，是**行为规格**——33 个技能的实现语义、命中/敏捷体系、
掉落算法、经验表、行会/城堡/沙巴克、脚本引擎、怪物 AI，以及大量踩坑结论。
这些**必须保留**，否则新服务端会重新踩一遍。

---

## 2. 协议耦合面（实测）

判据：文件是否 `import .../internal/proto`。

| 包 | 文件数 | 碰 `proto` | 处置 |
|---|---|---|---|
| `internal/gamesvr` | **121** | **71** ⚠️ | **换协议边界**（重点） |
| `internal/entity` | 16 | 2 | 基本直接搬 |
| `internal/data` | 14 | 1 | 基本直接搬 |
| `internal/castle` | 9 | 0 | **直接搬** |
| `internal/world` | 8 | 0 | **直接搬**（`map.go` 是地图格式的权威规格） |
| `internal/script` | 7 | 0 | **直接搬** |
| `internal/storage`（+`sqlite`/`pb`） | 6 | 0 | **直接搬** |
| `internal/guild` | 5 | 0 | **直接搬** |
| `internal/combat` / `pvp` / `group` / `tz` / `magic` | 各 2 | 0–1 | **直接搬** |
| `internal/delphi` | 2 | 0 | 搬到 `tools/`（读 Delphi 格式，**仅导入用**） |
| `internal/tscale` / `obs` | 各 1 | 0 | **直接搬** |
| `internal/proto` | 10 | — | **重写** |
| `internal/codec` | 4 | 0 | **6bit 删除**，短字符串工具保留给导入用 |
| `internal/wire` | 3 | 2 | **重写** |
| `internal/netgate` | 2 | 2 | **重写**（收包架构思想可借鉴） |
| `internal/accountsvc` | 3 | 2 | **重写协议部分** |
| `cmd/gate` | 1 | | **重写** |
| `cmd/mir2cli` | 6 | | **重写**（或改造为契约测试的驱动） |
| `cmd/seedgen` | 3 | | **保留**（数据导出，逐字节复现约束不变） |

**关键结论：领域逻辑是干净的，脏的只在协议边界。** `gamesvr` 那 71 个文件里
大多数只是"把 `proto.X` 换成新包类型"，是**机械替换而非重写逻辑**。

### 2.1 实测复核（2026-10-07，抽取 ① 之后在 `server/` 上复量）

**受影响面 = 10 个包 / 89 个文件**（按文件计，判据 = import 了旧协议层四包之一）：

| 包 | 文件数 | 说明 |
|---|---:|---|
| `internal/gamesvr` | **71** | 大头；与上表一致 ⇒ 抽取未改变耦合面 |
| `cmd/mir2cli` | 6 | 按 §2 处置：**重写**（或改造为契约测试驱动） |
| `internal/wire` | 2 | 自身待删 |
| `internal/netgate` | 2 | 自身待删 |
| `internal/entity` | 2 | 只碰 `proto` 的类型 |
| `internal/accountsvc` | 2 | 重写协议部分 |
| `internal/proto` | 1 | 自身待删 |
| `internal/magic` | 1 | |
| `cmd/gate` | 1 | 按 D-17 **重写**为纯字节转发 |
| `cmd/accountsvc` | 1 | |

**新协议层要提供的 API 面**（用到的符号数，实测）：

| 旧包 | 不同符号 | 高频用例 | 处置 |
|---|---:|---|---|
| `internal/proto` | **239** | `SlotWeapon`(69) `MakeDefaultMsg`(39) `SM_MAGICFIRE_FAIL`(19) `SM_STARTFAIL`(17) `CM_IDPASSWORD`(17) | 消息名 → `oneof` case；**`MakeDefaultMsg` 是要删的遗留**（非移植项） |
| `internal/wire` | 8 | `Packet`(92) `NewSplitter` `EncodeDown` `DecodePacket` `DecodeFrame` `ParseLoginToken` `LoginToken` `IsKeepAlive` | **重写**（分帧 + 握手 + 限流） |
| `internal/netgate` | 6 | `AttackIntervalFor`(3) `OverSpeedKickCount` `NewGate` `LogOverSpeed` `IsAttackIdent` `IntervalOf` | 重写；**语义要保留**（限流阈值是行为规格） |
| `internal/codec` | 1 | `EncodeBuffer`(2) | **6bit 删除**，短字符串工具只留给导入用 |

⚠️ **量法说明**：不要用"删掉再 `go build`"来数 —— Go 在缺包时**只报前几个就停**，
得到的清单是**不完整**的（实测只报出 4 个包）。可靠的量法是查**依赖图**
（`go list -e -f '{{.ImportPath}}|{{join .Imports " "}}'`）或直接文本统计 import。

### 2.2 ④ 的真实工作量：**schema 缺口**（2026-10-07 实测）

| | 数量 |
|---|---|
| `server/internal/gamesvr` 用到的**不同**消息 | `SM_*` **126** + `CM_*` **52** = **178** |
| 新 schema 已定义（`protocol/envelope.proto` 的 oneof） | **65** |

⇒ **有约 113 条消息需要设计**（每条的字段都得从遗留代码反推语义）。这是 ④ 之所以是
"8–12 天"的原因，也是 §3 第①步"编译通过"之后**真正的**工作量所在。

**但顺序上不该一次设计 178 条** —— 那会在没有任何客户端验证的情况下冻结大量字段。
好消息：**M1 纵切片要的那批 schema 已经覆盖**（登录 / 选角 / 进图 / 移动 / 实体 /
攻击 / 聊天 / 物品 / 技能）。所以 ④ 按域推进：

> **每个功能域三步**：
> ① 把该域缺失的消息补进 `envelope.proto` → 跑 `protocol/gen.sh` 重新生成
> （顺带 bump `version.txt`）→ ② 迁移该域收发：
> `switch pkt.Head.Ident` ⇒ `switch env.Body.(type)`
> （**让编译器做穷尽性检查**，这正是选 oneof 的最大收益）；
> ③ 该域测试跟上。

⚠️ **黄金报文测试会在每次 schema 变更时变红，这是预期的** ——
它的作用就是逼你"改协议必须显式确认"，而不是顺手改过去。
更新期望值时必须**人工核对** diff，不能无脑覆盖。

**已完成的域**：`cmd/gate`（按 D-17 重写为纯字节转发，见下）。
**下一步**：`internal/accountsvc` + `cmd/accountsvc` → 然后进 `gamesvr`。

---

## 3. 抽取方法（顺序很重要）

```
① 整体复制 mir2go → mir2/server/，只改 module path
   → 目标：编译通过。这一步是纯机械动作，编译通过 = 抽取无遗漏。

② 删除协议层（proto / codec 的 6bit / wire / netgate）
   → 目标：让它编译不过。**失败清单 = 需要改的地方清单。**

③ 按失败清单逐个换协议，用 protocol/ 生成的代码替换
   → 每改一批就编译 + 提交，保持可回退。

④ 补契约测试（见 protocol.md §9），建立 CI 门禁。
```

**②是核心技巧**：不要手工统计耦合点，**让编译器枚举**。任何遗漏都会在编译期暴露，
不会留到运行时。

### 3.1 ① 实测踩到的两个坑（2026-10-07）

**坑 1：生成的 `.pb.go` 里内嵌 protobuf 描述符字节数组，其中含 `go_package` 字符串。**
纯 sed 改 module 前缀会把字符串**改长**（47 → 52 字节），但描述符里的**长度前缀没变** ⇒
描述符损坏 ⇒ 运行期 `panic: slice bounds out of range [-4:]`（`internal/storage/pb` 实测）。
⇒ 正确做法：改 `.proto` 的 `go_package`，再**重新生成**：

```bash
# go_package 写成 github.com/algotao/mir2/server/internal/storage/pb
protoc -I server/proto --go_out=. --go_opt=module=github.com/algotao/mir2 \
    server/proto/storage/v1/character.proto
```

（`module=` 的作用是按 `go_package` 剥掉 module 前缀，输出落到 `server/internal/storage/pb/`。）

**坑 2：`data/` 必须随代码一起下沉一层。**
测试用的是相对路径 `../../data/...`；代码从 `mir2go/cmd` 搬到 `mir2/server/cmd` 后，
`../../data` 解析成 `server/data` 而不是仓库根的 `data`。
⇒ 数据目录落在 **`server/data/`**（`-data ./data` 的运行期默认值也据此）。
⚠️ **`server/data/map/` 不搬**（242 MB）：按 D-22 改用客户端地图集，容器经
[assets.md §5](./assets.md) 的 M2PK 读取 —— 因此 6 个依赖真实地图的测试当前会 skip。

**明令禁止**：不要"边搬边改"。搬迁阶段只允许改 module path 与 import，
任何逻辑改动单独提交。否则回归无法定位。

---

## 4. 必须保留的行为规格（不可丢失清单）

| 领域 | 内容 |
|---|---|
| 技能 | 33 个技能的语义；MP 公式 = `ROUND(Spell/4*(L+1)) + DefSpell`（**固定项是 DefSpell**） |
| 战斗 | 命中/敏捷体系、打空判定、幸运影响攻击力、攻速 |
| 掉落 | **逐条独立** `Random(MaxPoint) <= SelPoint`（不是区间）；物品表必须与掉落表同源 |
| 成长 | 经验表 `Exps.ini` 的 `LevelN` = 每级需求；升级管线 |
| 行会/城堡 | 职务表 `-1..-7` 码、沙巴克、城门/城墙、税收 |
| 脚本 | `#IF/#ACT/#SAY` 引擎、435 个 `market_def` 脚本的语义 |
| 怪物 | AI、刷新（占用检查）、`MonItems` 掉落表查找 |
| 地图 | `.map` 格式、`SpatialIndex` 分块（⚠️ `InRange` 返回候选**不过滤坐标**） |

**这些规格的载体是 `mir2go` 的代码与注释**，搬迁时逐文件确认不要丢注释。

---

## 5. 顺带要修的（协议自由后变得更简单）

| 项 | 现状 | 处置 |
|---|---|---|
| 运行期 GBK 转码 | `internal/castle/sabuk.go:336 decodeGBK` 是唯一转码点 | 下沉到 `tools/`，运行期删除 |
| `codec.PutShortString` 的 `b[:n]` | 会切断多字节序列 | **协议自由后此函数不再是运行期路径**，只留给导入工具 |
| `data/` 文件名规范化 | 855 大写 + 324 非 ASCII | `tools/assetnorm`（D-03） |
| `data/monitems/*.txt` | 300 个中文名，**文件名即怪物名** | 改索引文件，摆脱"文件名当主键" |
| 大写/非 ASCII 路径门禁 | 缺失 | 加上，防规范回退 |

---

## 6. 风险

| # | 风险 | 对策 |
|---|---|---|
| 1 | **`gamesvr` 是 121 文件的巨型包**，71 个要改 | 借搬迁机会拆分（原版 `M2Server` 是上帝对象，`mir2go` 已拆过一部分）。但**不要在搬迁阶段拆**，先搬完再单独重构 |
| 2 | 边搬边改导致回归无法定位 | §3 的"明令禁止" |
| 3 | 把 `mir2go` 当活体同步源（两边都改） | D-14：**禁止双向同步**，新仓库独立演进 |
| 4 | 过度保留 legacy 产物（6bit、`TDefaultMessage`） | 删干净；`codec` 只留导入工具需要的部分 |

---

## 7. 工作量

| 阶段 | 天 |
|---|---|
| ① 复制 + 改 module path + 编译通过 | 1–2 |
| ② 删协议层，收集失败清单 | 0.5 |
| ③ 新协议层（IDL + 生成 + 分帧 + 握手） | 5–8 |
| ④ `gamesvr` 71 文件替换 | 8–12 |
| ⑤ 契约测试 + CI 门禁 | 2–3 |
| **合计** | **17–27** |

④ 是全项目最"体力活"的单项，但也是 AI 最擅长的（机械替换）。

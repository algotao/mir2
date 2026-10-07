# 协议设计纲要

> 状态：**纲要（未定稿）**。决策见 [decisions.md D-05/D-12/D-13](./decisions.md)。
> 真源是 `protocol/` 下的 IDL，**本文件是设计意图，不替代 schema**。

---

## 1. 目标与非目标

**目标**

- 两端由**同一份 schema 生成**，消除双实现漂移（§6）。
- 分帧简单到可以用 `nc` 肉眼验；调试成本低。
- 明确每类消息的可靠性语义，不留"靠 TCP 顺序隐式同步"的暗坑。
- 支持协议演进（客户端与服务端可以不同版本灰度）。

**非目标**

- 不为兼容原版客户端做任何妥协（D-05）。
- 不在第一阶段做 UDP / 可靠 UDP / 状态压缩（原版就是 TCP，够用）。
- 不追求"最小字节数"。**网络延迟主导，不是带宽主导。**

---

## 2. 分帧

```
frame := [u32 length][Envelope]        // length 不含自身，单位字节
```

```proto
message Envelope {
  uint32 seq        = 1;   // 发送方单调递增
  uint32 ack_seq    = 2;   // 已处理的对端 seq（用于输入确认）
  uint64 request_id = 3;   // 可选：一次性事务的幂等键，缺省 0（见 §6）

  oneof body {             // ★ 字段号 = 消息号，见 §4
    ClientHello client_hello = 0x0101;
    ServerHello server_hello = 0x0102;
    EntityMove  entity_move  = 0x0503;
    // ... 全部消息都在这里，且只在这里
  }
}
```

规则：

- 字节序统一 **小端**。
- `length` 上限 **64 KiB**，超限立即断开（防 DoS）。
- **不用 protobuf 的 delimited（varint 长度）编码**：固定 4 字节小端长度更易校验，
  也便于用 `nc` / 十六进制工具肉眼验。长度域不属于 schema。
- `body` 不压缩；大的二进制块（地图块、图集）走**独立的分块传输消息**，单独分帧。
- **不使用字符帧**（原版的 `#<1-9>…!`），也不需要 6bit 码表。
- `request_id` 放在**信封**里而不是各消息内，是为了让通用分派层能做幂等判重，
  不必认识具体消息类型（§6）。

---

## 3. IDL 与代码生成（单一真源）

```
protocol/
├── envelope.proto        ★ 信封 + oneof 消息总目录（**唯一集中点**，65 条）
├── common.proto          共享类型（Vec2 / Direction / Ability / EntityFeature…）
├── control.proto         0x01xx 控制 / 握手 / 心跳
├── account.proto         0x02xx 账号、0x03xx 角色
├── scene.proto           0x04xx 场景、0x05xx 实体
├── combat.proto          0x06xx 战斗、0x07xx 技能
├── item.proto            0x08xx 物品
├── social.proto          0x09xx 聊天（0x0Axx 社交待定）
├── version.txt           协议版本号（握手用）
└── gen.sh                两端代码生成脚本
```

> **现状（2026-10-07）**：以上文件均已落地，`protoc` 校验通过，
> Go 产物已生成（`server/protocol/*.pb.go`）并**编译通过**。
> 尚未开工：`admin.proto`（0x0Cxx GM）、`0x0Bxx` NPC/脚本、`0x0Axx` 社交。
> `common.proto` 是新增文件（原清单未列）——共享类型集中一处，避免各模块重复定义。

消息**体**按模块拆文件，但 **oneof 字段列表必须集中在 `envelope.proto`**
（`oneof` 无法跨文件扩展），理由见 §4.1。

| 端 | 生成器 | 产物 |
|---|---|---|
| Go | `protoc-gen-go` | `server/protocol/*.pb.go`（**非** `internal/`——`tools/` 与契约测试驱动要引用） |
| Rust | `prost-build`（build.rs） | `client/protocol/src/*.rs` |

**硬规则**

1. **禁止手写编解码**。任何"我这边先手写一个临时的"都会立刻变成漂移源。
2. schema 变更 = 两端同时重新生成 + `version.txt` bump。
3. 生成的产物**入库**（便于 review 协议变更的实际影响）。

---

## 4. 消息号与分段（字段号即消息号）

**不设独立的 `msg_id` 字段**：用 oneof 的字段号同时承担「类型标识」与「线上编号」
两个角色。理由与代价见 §4.1。

```
0x01xx  控制/握手/心跳/错误
0x02xx  账号（登录、注册、改密）
0x03xx  角色（选角、建角、删角）
0x04xx  场景（进图、地图块、时间/天气）
0x05xx  实体（出现、消失、移动、动画、状态、属性）
0x06xx  战斗（攻击、伤害、死亡、复活、PK）
0x07xx  技能（施法、特效、充能）
0x08xx  物品（背包、装备、掉落、拾取、修理、堆叠）
0x09xx  聊天（各频道、喊话、私聊）
0x0Axx  社交（组队、行会、好友、黑名单、邮件、备忘录）
0x0Bxx  NPC / 脚本（对话、商店、仓库、合成）
0x0Cxx  管理 / GM
```

**覆盖范围以实际可达的消息为准。** 原版定义了 216 个 `SM_*`，但很多在 1.76 玩法里不可达
（见 [legacy-analysis.md §3](./legacy-analysis.md)）。第一批只实现核心子集，
其余按需增量补——**schema 的字段号留出余量即可**。

### 4.1 为什么用 oneof，而不是独立的 `msg_id` + `bytes body`

**收益**

1. **类型与成员结构必然配对**。字段号既是类型标识又是线上编号，只有一处定义——
   重命名/重构消息类型时不会漏改编号（"两处定义"最典型的失效方式）。
2. **编译期穷尽性检查（最大收益）**。Rust 的 `match` 必须覆盖 oneof 的所有变体；
   Go 侧新增变体未处理也会被 lint / 静态检查抓到。
   ⇒ **加一条消息，两端所有分发点都会编译报错**，逼你处理。
   `msg_id` + `bytes body` 给不了这个：加了消息却忘了处理，会静默走 default 分支。
3. **日志可读**：直接打印变体名（`EntityMove`）而不是数字 `0x0503`。
4. 省掉每包 3–5 字节的独立 `msg_id` 字段。

**代价与对策**

| 代价 | 对策 |
|---|---|
| `oneof` **无法跨文件扩展** ⇒ 所有消息必须在同一个 message 内 | 消息体定义按模块拆文件，**只有 oneof 字段列表**集中在 `envelope.proto`（约 200 行）。这份集中列表本身就是"消息总目录"，review 性反而更好 |
| 字段号需全局唯一且**永不复用** | 写进 schema 注释当硬规则；避开 protobuf 保留区 **19000–19999**（我们的 `0x01xx`–`0x0Cxx` 完全避开） |
| 收到**未知消息**时 oneof 的 case 未设置 | 显式处理 `None` 分支：记数 + 告警 + 忽略，**不要 panic**。这比 `msg_id` 方案更安全（后者只能忽略未知编号） |
| 若 gate 要"不改 body 就转发"，需完整 parse | **让 gate 不做协议感知**（D-17）：只做 TCP 接入、字节转发、限流、连接元数据。这样 gate 根本不需要知道 oneof |

**什么时候该反过来选 `msg_id` + `bytes`**（据实判断，不教条）：

- 需要 gate 做**协议感知**的转发 / 改写 / 按类型限流；
- 消息数膨胀到几百上千，单文件 `envelope.proto` 变得难维护；
- 需要**协议层可扩展**（第三方或用 GM 工具注入自定义消息）——
  `oneof` 无法从外部扩展，这是唯一一个真实的硬限制。

当前规模（~200 条消息、单一团队、两端同仓库）**oneof 更合适**。
若日后确实需要外部扩展，加一个逃生舱变体即可，不必推翻整体设计：

```proto
    Raw raw = 0x0FFF;   // { uint32 msg_id; bytes body; } 仅供 GM 工具 / 调试 / 插件
```

---

## 5. 连接与握手（客户端单连接）

```
客户端 ──TCP/单地址──> [gate 无状态转发] ──> [gamesvr（区组）] ──内部 RPC──> accountsvc
```

```
  → ClientHello { protocol_version, client_build, locale }
  ← ServerHello { protocol_version, server_time, session_key, capabilities }
     版本不匹配 → 立即断开（禁止"尽力而为"，那只会变成静默错乱）
  → Login { account, ... }        ← LoginResult { session_token, ... }
  → ListCharacters                ← CharacterList
  → SelectCharacter { char_id }   ← SelectCharacterResult
       ★ 服务端在此**申请角色租约**（gamesvr → accountsvc 内部 RPC）
         租约被占（同角色在线）→ **明确拒绝**，不要"顶号"式静默踢人
  ← EnterWorld { spawn_state, 初始快照 } → 数据面
```

**客户端只有一条连接、一个地址、一个状态机**（D-13）。`accountsvc` **不对客户端 Listen**，
账号能力由 `gamesvr` 通过内部 RPC 访问。

技术要求（单连接方案能否成立的关键）：

- **会话 token 独立于 TCP 连接对象**：`session_token` 由 `LoginResult` 下发，
  重连时**用它**恢复会话，而不是靠"连接还在"来认定。
- **租约宽限**：连接断开**不立即**释放角色租约，超时才释放（否则断线重连必失败）。
- **重连语义**：重新握手 → 用 `session_token` 恢复 → 租约仍在则回到世界，
  否则回落到选角阶段。**不要**回落到"重连三个 gate"。
- **换区组 = 换地址重连**（选服界面选中另一区组时）。

---

## 6. 可靠性与语义

TCP 保证送达与顺序，但不保证"语义上的只有一次"。所以按**语义**分类：

| 类别 | 例 | 语义 | 要求 |
|---|---|---|---|
| 状态同步 | 实体出现/消失/属性变化 | 幂等，可丢失旧包 | 带 `server_tick`，旧的丢弃 |
| 高频位置 | 移动广播 | 可丢弃过期 | 按 `seq` 丢弃过期位置 |
| 客户端输入 | 移动、攻击 | **有且仅有一次生效** | 带 `request_id`，服务端去重 |
| 一次性事务 | 拾取、使用物品、交易 | **幂等** | `request_id` + 服务端判重 |
| 请求-响应 | 打开商店、查询 | 需要配对 | 请求带 `request_id`，响带回显 |

**为什么要 `request_id`**：原版的交易复制漏洞、重复拾取一类问题，根因就是
"重发/重放没有被判重"。这在自研协议里是**设计期就能消灭**的一类 bug，别等到出事再补。

---

## 7. 实体标识

- `uint64 entity_id` **全局唯一**，服务端分配。
- 不再有原版 16 位 `Series` 装不下 `ActorId` 的坑，也**不再需要"坐标优先定位目标"的妥协**。
- 沿用 `mir2go` 的分段约定便于与参照系统对日志：
  **玩家 < 1_000_000**、**怪物 ≥ 1_000_000**、**NPC ≥ 2_000_000**。

---

## 8. 移动与预测

- 客户端**预测**移动并在本地立即表现；服务端**权威**校正。
- 每个移动输入带 `client_tick`（毫秒）与 `request_id`。
- 服务端按**速度上限**校验（防加速外挂）；越界则拒绝并回校正包。
- 广播 `EntityMove { entity_id, from, to, direction, server_tick }`。
- ⚠️ **视野格数锁死**（D-07）：提高分辨率可以，看到更多格不行——那是玩法平衡。

---

## 9. 防止双实现漂移

这是协议自研的**最大风险**（见 [plan.md §7](./plan.md) R-1）。四道防线：

1. **单一真源 + 代码生成**（§3）——根本手段。
2. **契约测试**（CI 门禁）：起 Go 服务端 → 跑 **`client/e2e` 产物**
   （headless CLI，无显示环境，读剧本：登录 → 进图 → 移动 → 攻击 → 拾取 → 掉落 → 交易），
   **断言收到的消息序列**。它必须走与 `client/app` **完全相同**的协议编解码与会话状态机
   （[D-18](./decisions.md)）——否则它检验的就不是真客户端了。
3. **黄金报文测试**：一组固定输入 → 断言输出字节的哈希。
   任何无意的协议改动立刻红。
4. **版本协商**：`version.txt` 不匹配即拒绝连接，避免"两端都以为自己对"。

**这套东西是 `mir2go` 46/46 e2e 的等价物**，必须在 M1 就建立，不能等出问题再补。

---

## 9.5 动作 id（`EntityAction.action`）

⚠️ 这个值域是**我们定的**：原版把"一个动作"拆成 70 个手写动画类，再加上十几个攻击消息号
（SM_HIT / SM_HEAVYHIT / …，见 `combat.proto` 表头），既没法照搬也不该照搬。

| 值 | 含义 | 说明 |
|---|---|---|
| 0 | 未指定 | |
| 1..8 | **攻击** | 与 `AttackAction` **同值**（1 砍 / 2 重砍 / 3 大力 / 4 攻杀 / 5 刺杀 / 6 半月 / 7 烈火 / 8 双龙）。于是"我发 `ATTACK_HIT`"与"我看到别人 `action=1`"播的是同一套动作，客户端不必再翻译一层 |
| 51 | 受击 | 被打了一下（原版 SM_STRUCK 的动画部分） |
| 52 | 死亡 | 尸骨**留在原地**；"移出视野"是另一条 `EntityDisappear`（`reason=DISAPPEAR_DEAD`）—— 两者是两件事 |
| 53.. | 保留 | 骑马 / 挖矿 / 施法…按需往上加 |

两条纪律：

1. **1..8 不能改**：它们是"动作"（服务端 → 客户端）与"输入"（客户端 → 服务端）**共用**的值域，
   动一处等于同时改两处的语义；
2. **新增不要插在中间**：客户端可能按区间判断（`core::world::action::is_attack` 就是 `1..=8`）。

代码里的两个方向：`gamesvr.attackActionOf`（legacy 消息号 → 动作 id）与
`gamesvr.attackIdentOf`（`AttackAction` → legacy 消息号）。

---

## 10. 禁止事项（从原版学到的教训）

| ❌ | 原版的问题 | 本协议的做法 |
|---|---|---|
| 16 位实体 ID | `TDefaultMessage.Series` 装不下 `ActorId`，被迫"坐标优先定位目标" | `uint64 entity_id` |
| 定长字节字符串 | Delphi `String[N]` 让长度与字符集耦合，UTF-8 化时全部返工 | UTF-8 + 长度前缀 |
| 靠接收顺序承载状态 | 不可重放、不可断言、难写测试 | 显式 `seq` / `ack_seq` / `server_tick` |
| 同一语义两个字段 | `SetBuff`/`AddBuff` 之类的混乱 | 一个语义一个字段 |
| 客户端权威 | 移动/伤害由客户端说了算 → 外挂 | 服务端权威 + 客户端预测 |
| 一次性事件无幂等 | 交易复制、重复拾取 | 信封里的 `request_id` 判重 |
| 类型与编号两处定义 | 消息号常量与处理分支分开维护，加消息易漏 | **oneof 字段号即消息号**（D-16） |

---

## 11. 待办

- [x] **消息清单** ⇒ [messages.md](./messages.md)：`SM_` 216 + `CM_` 87 双向分档
      （T1 可达 172 / T2 待定 26 / T3 引擎扩展 18），与 28 窗口交叉核对。
- [x] **`protocol/` 骨架 + 生成脚本 + 核心子集 schema v1**（握手 / 登录 / 选角 / 进图 / 移动 /
      攻击 / 聊天 / 物品，oneof 共 65 条）；`protoc` 校验通过，Go 产物生成并编译通过。
- [x] **分帧 + 握手层落地**（2026-10-07）⇒ [`server/internal/frame`](../server/internal/frame)：
      `Write`/`Read`（信封）+ `ReadRaw`/`WriteRaw`（**给 gate 用**：按 D-17 gate 不做协议感知，
      只需要长度域）+ `CheckHello`（版本不匹配**明确拒绝**）+ `MsgName`（日志可读）。
      - **手写的只有长度域**：消息体一律 protoc 生成（§3 硬规则 1）
      - `protocol.Version` 也由 `gen.sh` 从 `version.txt` 生成 —— Go 侧不再有"手抄一份版本号"
      - **黄金报文测试已就位**（§9.3 的第一道门禁）：`TestGoldenFrameBytes` 断言
        固定握手信封的**逐字节输出**与 sha256；改 schema/版本就会红
- [x] **Rust 侧生成落地**（2026-10-07）⇒ [`client/protocol`](../client/protocol)（`build.rs` + prost-build）
      + [`client/net`](../client/net)（连接、握手、发一收一；**不含 SDL**，D-18）。
      版本号也由 `build.rs` 从 `version.txt` 生成（Rust 侧不手抄）。
      两条判据：
      - **黄金报文两端逐字节相同**：Rust 的 `golden_frame_bytes_match_go` 与 Go 的
        `TestGoldenFrameBytes` 断言**同一串** `1400000008018a100f08021204746573741a057a682d434e`
        （§9.3 那道门第一次跨了两个实现）；
      - `mir2-e2e` 的依赖树里**没有 sdl3**（D-18 门禁仍成立）。
- [x] **契约测试**（2026-10-07，§9.2 的完整形态）：Go 起服务端 → 跑 **`client/e2e` 的剧本**
      ⇒ `mir2-e2e contract`（握手 → 认领会话 → 列角色 → 选角 → 进图 → 能力值 → 心跳 → 未知消息）
      + Go 侧 [`TestProtoContractRustClient`](../server/internal/gamesvr/netproto_test.go)
      （起服务端、播种数据、把**已知真值**用 `-expect-*` 传给 Rust 端断言）。
      Go 侧另有一份同构的纯 Go 契约用例（`TestProtoContractEnterWorld`）覆盖错误路径。
      ⚠️ 产物缺失时**跳过**（与 core 的 `real_container_if_present` 同一约定）：
      先 `cd client && cargo build -p mir2-e2e`，或设 `$MIR2_E2E_BIN`。
- [x] **修正 `Direction` 枚举顺序**（2026-10-07）：原枚举按"下→逆时针"排（`DIR_DOWN=1` …），
      注释却声称"沿用原版顺序"（原版是 **上→顺时针**，`Grobal2.pas:19-26`）。已改为
      **原版 + 1**，并 `version.txt 1 → 2`（改枚举值 = 改线上格式 ⇒ 必须 bump）。
      钉子测试：`TestDirectionEnumMatchesLegacyOrder`（服务端）把两侧常量锁在一起。
      ⚠️ 这类错**不会报错**，只会让每个实体差 4 个方向（上看起来是下）—— 正是 §9 要防的。
- [ ] D-13（连接模型）定稿。D-12 / D-16 已定（protobuf 3 + oneof 信封），D-17 已定（gate 不做协议感知）。
- [ ] **`Login` 还没实现**（新协议入口目前用 `Reconnect` 认领既有会话）：
      卡在 `Login.password_hash` 的语义 —— 即**口令怎么过网络**（要定成 [D-24](./decisions.md)），
      它同时依赖 accountsvc 的接入（D-13 的内部 RPC）。
- [ ] `EnterWorld.map_id` 的取值还没定义（v0 恒 0，客户端请用 `map_name`）：
      本项目地图**按名字**索引（D-22，容器里就是 `<名字>.map`），需要定"id 到底是什么"
      （编号？CRC？还是干脆从 schema 里去掉）。
- [ ] `Ability.ac` / `ability.mac` 是否要拆成 `(min,max)` 对偶：1.76 的 AC/MAC 本来是
      (min,max)（原版打包成一个 uint32 发出去、客户端再拆），而新协议给了**单值** ⇒
      v0 只取了 `min`，**丢了信息**。等 combat 落地时一并定。
- [ ] `session_token` 的正式格式与签发者：v0 是"4 字节小端会话号"（可猜测、不可换发），
      正式应由 `LoginResult` 签发一个不可猜的随机值（§5 要求它独立于 TCP 连接）。
- [x] **实时实体事件**（2026-10-07）：进图不再是一张静止快照。
      出站 `EntityAppear / EntityDisappear / EntityMove`（+ 自己的 `MoveRejected`），
      入站 `MoveInput`（走一步、限流、被挡）。
      做法：在**实体事件的出口**（`view.go` 的 `sendPlayerAppear` / `sendMonsterAppear` /
      `sendDisappear` / `broadcastMove` + 怪物移动）里判 `p.protoOut` ——
      "谁该看见谁"的判定只有 vision 那一处，两条协议共享它，不必改 239 个调用点。
      另加一条**只对新协议玩家**的周期性视野同步（legacy 那半边有同一个缺口：
      `updateVision` 只在移动/进图时触发 ⇒ 站着不动看不见"走近"的实体）；
      不对 legacy 做是因为那会给 mir2cli 的**包序**断言插进额外包（另开一条）。
- [x] **战斗**（2026-10-07）：`AttackInput`（入站）+ `Damage` / `EntityHealth` / `EntityAction`
      / `Death` / `LevelUp`（出站）。
      做法：**在 legacy 的出站漏斗内部分支** —— `sendStruck`（受击，13 处调用点）/
      `sendHealthChanged`（血量+自己 mp，23 处）/ `sendSwing`（挥砍）/ `sendDeathTo`
      （死亡，8 个 SM_DEATH 点统一收口）/ `applyLevelUp`。所以"谁看得见什么"的判定
      仍然只有一处，两条协议共享它。
      ⚠️ 入站那段是**翻译而不是重写**：`AttackInput{target_entity_id}` → 按 id 找到目标、
      算出朝向它的方向 → 合成一个 legacy 攻击包交给 `handleAttack`。
      这样威力/打空/减防/技能模式/挖矿/试刀/宠物跟打/掉落/经验**只有一份实现**（R-7），
      新协议不会长出一个"少了几条规则"的影子版本。代价是这一层翻译与 legacy 同生共死；
      legacy 退役时把 `handleAttack` 的入参从 `wire.Packet` 换成 `(dir, mode)` 即可。
- [x] 换图（回城/传送）也接上了：`switchMap` 对新协议玩家发**一份 `ChangeMap` 快照**，
      不再走"先 SM_CLEAROBJECTS 再增量补"那三步（客户端拿到新地图名 + 整份实体表就重建了）。
- [ ] `EntityHealth` **没有 mp 字段**：自己的 mp 目前靠 `sendHealthChanged` 里**捎带一条
      完整 `AbilityUpdate`**（代价：每次血量变化多发一条）。mp 的同步一旦变频繁就要给它
      一条专门的消息（或给 `EntityHealth` 加 mp）。
- [ ] `Death.killer_id` 只在"出手者就在作用域里"的地方填得上（近战/技能/怪致死）；
      **毒 / 火墙 / 脚本**那几条收尾路径传 0（= 非玩家击杀）。要精确归因，得把出手者
      一路传进那些路径（它们今天只带 (id, 坐标)）。
- [ ] `ExperienceGain` **还没发**：legacy 把经验塞在 `SM_ABILITY` 的位域里，而新协议的
      `Ability` 没有 exp 字段 ⇒ 新协议客户端目前看不到经验条。要么用这条消息，
      要么给 `Ability` 加 exp（两选一要定）。
- [ ] `Revive` **还没发**：死亡回城走的是 `ChangeMap` 快照 + `EntityHealth`，
      所以 `Revive` 暂时没有发送点（要么删掉它，要么把它用在"原地复活"那条路径上）。
- [ ] 攻击目标不可及（不相邻 / 已死 / 不在视野）时服务端**静默忽略**（只留日志）：
      IDL 里没有 `AttackRejected` 这类消息。客户端只能靠"没看到动作/伤害"自己判断，
      要不要补一条拒绝消息等预测逻辑成型再定。
- [x] `EntityFeature.appr` 与 `EnterWorld/ChangeMap.self_feature`（**协议版本 3**）：
      前者是怪物外观号 —— 原版把它打包进了 `hair`/`dress` 两个字节（`Grobal2.pas:2663-2699`），
      新协议给它自己的字段；后者是**自己的外观** —— 快照刻意不含自己（`self_entity_id` 才代表自己），
      少了它客户端连"自己长什么样"都不知道。
      ⚠️ 两者都是"给渲染用的显式字段"，与 §10 的"一个语义一个字段"同一条纪律。
- [ ] **自己的换装不会重发 `self_feature`**：装备/发型变化目前只在进图与换图时下发一次。
      要跟上得在 `EntityFeatureChanged`（scene.proto 里已有）上给"自己"也发一份 ——
      现在不影响观感（换装要走背包，那条链还没接）。
- [ ] `MapChunk` **还没有发送点**：客户端读本地地图容器（`maps.m2pk`，D-22），
      服务端下发地图块那条路只在 IDL 里存在。
- [x] **登录**（2026-10-08，D-24① 挑战应答）：`LoginSaltRequest` / `LoginSalt` / `Login`
      / `LoginResult`（协议版本 3 → 4）。服务端两步：先给 KDF 参数，再用**存着的口令凭证**
      重算 `HMAC(K, nonce ‖ account)` 比对（`nonce` = 握手的 `session_key`）——
      明文、等价口令、证明本身都不落网络且不可重放。
      ⚠️ 配套：`ChangePassword` 还是空壳（要旧口令证明 + 新口令的新盐，还没接）；
      会话阶段号（`sessionStageAuthed = 1`）与 `loginSessionTTL` 在 gamesvr 与 accountsvc
      **各存了一份**，该搬到 `storage` 去。
- [ ] 新协议入口的**剩余边界**（见 [`netproto.go`](../server/internal/gamesvr/netproto.go) 文件头）：
      物品/聊天/技能输入/组队/交易仍走 legacy；`protoDown` 会把那些 legacy 下行丢掉。
- [ ] `MoveInput` **没有走/跑标志**（legacy 靠 CM_WALK / CM_RUN 两条消息区分）⇒
      新协议客户端目前只能走。补它要改 schema + bump 版本。
- [ ] **没有独立的"转身"与"动作"消息**：转身目前也用 `EntityMove`（`from == to`）表达，
      客户端据此只更新朝向。要不要一条 `EntityAction` 语义的专用消息，等动画状态机落地时定。
- [ ] `EntityDisappear.reason` **分辨不出来**：legacy 的 `sendDisappear` 只带 (id, x, y)，
      所以离开视野/隐身/死亡/下线一律报 `LEFT_VIEW`。要精确原因得把理由从调用点传进来。
- [ ] `MapChunk` 目前**用不上**：本项目客户端与服务端读**同一份**地图容器（D-11/D-22），
      地图数据两端各自本地就有 ⇒ 服务端不必推。它是留给"服务端权威地图"那种形态的。
      需要先完成服务端抽取 ③（换协议）/ ④（`gamesvr` 71 文件替换）。

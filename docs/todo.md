# 待办（明确的欠账，别丢）

> 用户点过名、或实现时明确记过账的**未完成项**。每条都写清：为什么、在哪、怎么验。
> 做完一条就把这条删掉，并在 `docs/decisions.md` 里留一条 D-xx。

## 1. NPC 对话 / 交易（2026-10-09 开工：**对话已通**，商店还差）

**已做（D-61）**：
- 协议 `protocol/npc.proto`：`NpcClick` / `NpcSay`（正文 + `NpcOption[]`）/ `NpcSelect` / `NpcClose`
- 服务端：`Server.npcSay` 分流（新协议发 `NpcSay`、legacy 发 `sysMsg`）；
  `protoSession.onNpcClick/onNpcSelect/onNpcClose`；对话框内核 `dlgSelectIndex` 与 legacy 共用
- 客户端：点 NPC **发 `NpcClick` 而不是走路**（就是"点 NPC 变成反复往那格走"的修法）；
  对话面板（正文折行 + 可点选项，`Esc` 关闭）

**还差**：
1. **商店（买/卖）**：`openShop`/`sendGoods` 仍只发 legacy ⇒ proto 玩家收不到。
   现在点"买/卖"会在对话里明说一句"商店买卖还没接新协议"。要加
   `ShopList`/`ShopBuy`/`ShopSell` 三条消息 + 商品窗口（买东西还要背包窗口才能看结果）。
2. **对话版式**：现在是一块**自己的面板**（功能优先）；原版是把"正文 + `[1] 选项`"
   当系统消息发进**聊天区**、文本里的 `<文字/@标签>` 可点（`SM_*` 那套）。
   要对齐原版就把面板改成聊天区渲染 + 文本内链接命中。
3. 选项里的 `@@` 内嵌输入（建会/行会战那类）还没接新协议。

**根因**：新协议里**没有** NPC 点击 / 对话消息。服务端的脚本引擎是齐的
（`internal/gamesvr/npcdlg.go`：`startDialog` / `showLabel` / `handleDlgSelect` /
`runActs` 全套，`shop.go` 管买卖），但它现在只往 **legacy** `SM_*` 出口发
（`SM_SENDUSERREPAIR` / `SM_GOLDCHANGED` …），新协议客户端收不到。

**要做**：

1. **协议**（`protocol/`，加完跑 `protocol/gen.sh`）：

   | 消息 | 方向 | 字段 |
   |---|---|---|
   | `NpcClick` | C → S | `npc_id`（`EntityState.entity_id`，NPC 的 id 在 `proto.NpcIDBase` 之上） |
   | `NpcSay` | S → C | `npc_id`、`text`、`repeated NpcOption{label, action}`（对应脚本里的 `<文字/@标签>`） |
   | `NpcSelect` | C → S | `npc_id`、`label`（脚本 `[@标签]` 名） |
   | `NpcClose` | C → S | `npc_id`（原版 `@exit`） |
   | 商店：`ShopList` / `ShopBuy` / `ShopSell` | 双向 | 商品名/价格/数量（服务端 `shop.go` 已有数据） |

2. **服务端**：把 `npcdlg.go` 的出口从 legacy 换成"新协议有连接就发新协议"
   （照 `sendSwing` 那种双出口写法：`if sink := to.protoOut; sink != nil { … }`）。
   脚本里 `<同意/@h2>` 这类菜单要解析成 `NpcSay.options`（`script.Label` 里已经有 acts ✓）。

3. **客户端**：
   - `input::mouse_intent` / `click_step`：**先判 NPC**（`kind == 2`）—— 点在 NPC 上
     发 `NpcClick`、**不移动**（用户第 2 条："点 NPC 总是变成人物行走"）。
   - 对话窗口：面板 + 文本（真字体能画中文 ✓）+ 选项按钮；`Esc`/`@exit` 关闭。
   - 商店窗口：商品列表 + 买/卖（先做列表与买，卖要等背包窗口）。

4. **验收**：在比奇省点"屠夫"（`merchant.txt` 的 `1Bme`，坐标 (313,271) 一带），
   出对话文字 + 选项；点"买肉"能出商品列表并扣钱加物品。

## 2. NPC 精灵：等肉眼验收（2026-10-09 已实现，未看过画面）

**做了什么**：`npc.wzl` + `GetNpcOffset(appr)` + `GetRaceByPM(race, appr)` 的站立段
（`core::actor::{npc_offset, npc_actions, npc_index}`）；服务端按"商人 race 恒 50、
`Npcs.txt` 各用 `Race`/`Body`"给 `(race, appr)`（`npcRaceAppr`）。

**要验的**：比奇省的 34 个 NPC 是否长得**像原版**（不是标记、也不是错图）。
若不对，先查服务端给的值（`npcRaceAppr`）与 `Npc.wzl` 的块起点，再查 race 50 的
外观分派表（`Actor.pas:881-912`，已逐条搬进 `npc_actions`）。

## 3. 地图"区域名"（银杏山谷）

**已查清**（2026-10-09）：

- 左下角那行的抬头是**服务端下发的地图描述**（`ClientGetMapDescription`，
  `ClMain.pas:5215-5224` → `DrawScrn.pas:513`）⇒ 已实现（`EnterWorld/ChangeMap.map_title`，
  服务端取 `mapinfo.txt` 的描述，地图 0 = **比奇省**）。
- "银杏山谷"在官方数据里是**区域标注**，不是地图名：
  - `$WS/mir2c/Config/Script/MapDesc1.dat`（GBK）里有一条
    `比奇省,620,626,银杏山谷,$33FFFF,0` —— 与 `StartPoint.txt` 的 (650,631)/(289,618)
    那片完全对得上（同一文件里还有 `比奇省,294,630,边界村`）；
  - GeeM2 的 `mapinfo.txt` 也把 0 号图那段门表标成 `;银杏山谷`。

**可做**（用户定）：把 `MapDesc1.dat` 那套标注画到**小地图**上（原版小地图上就带区域名），
或单纯把地图 0 的描述改成"银杏山谷"（一行数据，但那会让整张比奇省都叫银杏山谷）。

## 4. 其它已记账的

- **背包窗口 F9 / 角色状态窗口 F10**（D-58 ⑤）：协议与服务端都齐了，缺客户端
  （消费 `BagItems`/`AddItem`/`WeightChanged`… + 窗口）。
- **物品表仍是 GeeM2 的 686 条**（含后期物品；掉落规则里 `天衣无缝（男/女）` 引用不到物品）。
- **进图音乐**：`MapDescription` 还有个音乐号没接（`ClMain.pas:5218` 的 `g_nMapMusic`），
  我们连 mp3 都还没有（`docs/use.md`）。
- **客户端地图容器**仍按 `$WS/mir2c/map` 全量打包（770 张），删掉的图进不了游戏但占体积。

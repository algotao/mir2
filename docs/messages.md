# M0 · 消息清单（216 个 `SM_*`）

> M0 任务池第 1 项（[plan.md §8](./plan.md)）。用途：**界定功能范围**——216 条里哪些要在新实现里做、哪些不做。
> 对象：`$WS/mir2standard/GameOfMir`（**只读**；⚠️ 源码是 GBK）
> 复现命令见 §1（**已改为 macOS / BSD 兼容写法**）。
> 生成日期：2026-10-07。状态：**第一版（机械分档）**，待人工复核项见 §6。

---

## 1. 复现命令（BSD 兼容）

⚠️ [legacy-analysis.md §8](./legacy-analysis.md) 里的命令用的是 GNU grep 的 `-P`；
**macOS 自带的是 BSD grep，不支持 `-P`**（会报 `invalid option -- P`）。本文统一用 `grep -E`。

```bash
WS=~/Develop/git
OUT=/tmp/mir2cli-utf8; mkdir -p $OUT
cd $WS/mir2standard/GameOfMir/Client

# GBK → UTF-8（要读中文注释必须走这步）
iconv -f GBK -t UTF-8 Grobal2.pas > $OUT/Grobal2.pas

# 216 条定义（名 = 值; // 注释）
grep -hE '^[[:space:]]*SM_[A-Z0-9_]+[[:space:]]*=' Grobal2.pas | sed 's/^[[:space:]]*//' > $OUT/sm_defs.txt

# 客户端引用（★必须 --exclude 定义文件 Grobal2.pas，否则每条都 ≥1，差集恒为空）
grep -rohE 'SM_[A-Z0-9_]+' --include='*.pas' --exclude='Grobal2.pas' Client | sort -u > $OUT/used_client.txt

# 服务端引用（★必须含全部服务端组件；漏掉 LoginSrv/DBServer 会把登录/选角流程误判成"仅客户端"）
grep -rohE 'SM_[A-Z0-9_]+' --include='*.pas' --exclude='Grobal2.pas' \
  LoginSrv DBServer M2Server SelGate LoginGate RunGate MirServer GameCenter LogDataServer Common \
  | sort -u > $OUT/used_server.txt

# 客户端分派标签（ClMain.pas 的 case 标签）
grep -ohE 'SM_[A-Z0-9_]+[[:space:]]*:' ClMain.pas | sed 's/[[:space:]]*:.*//' | sort -u > $OUT/clmain_labels.txt

# ★ 服务端「实际发送」（比"引用"强得多）：构造/发送函数的首参为 SM_
cd $WS/mir2standard/GameOfMir
find . -name '*.pas' -not -path './Client/*' -not -path './MirClient/*' -print0 \
  | xargs -0 grep -ohE '(SendDefMessage|MakeDefaultMsg|SendMsg|SendSocketMsg)\([[:space:]]*SM_[A-Z0-9_]+' \
  | grep -ohE 'SM_[A-Z0-9_]+' | sort -u > $OUT/sm_sent_all.txt   # → 194

# ⚠️ 服务端的 RM_→SM_ 映射（形如 ObjBase.pas:5729 `RM_ITEMSHOW:` → `SendDefMessage(SM_ITEMSHOW,…)`）
#    也是 SendDefMessage(SM_...)，已包含在上面 —— 别再单独找一遍

# 值域分区、分档见 §3/§4
```

---

## 2. 总览

协议是**双向**的，两张表分开统计。

**`SM_`（服务端 → 客户端）**

| 项 | 数 |
|---|---|
| `Grobal2.pas` 定义 | **216** |
| 客户端引用（去重，已排除定义文件） | 207 |
| `ClMain.pas` 分派标签（去重） | 189 |
| 服务端"引用"（去重，已排除客户端与定义文件） | 207 |
| **服务端"实际发送"**（`SendDefMessage`/`MakeDefaultMsg`/`SendMsg`/`SendSocketMsg` 首参为 `SM_`） | **194** |
| **客户端分派 ∩ 服务端发送** | **178** |
| 仅客户端分派 / 仅服务端发送 / **两者都没** | 11 / 16 / **11** |
| **引用但未定义** | 1（`SM_READYFIREHIT`） |

**`CM_`（客户端 → 服务端）** —— 详见 §8

| 项 | 数 |
|---|---|
| `Grobal2.pas` 定义 | **87** |
| 客户端引用（去重） | 93 |
| 服务端引用（去重） | 104 |
| 定义中被服务端引用 | **82 / 87** |
| **服务端零引用（死请求候选）** | **5** |

⚠️ **方法论陷阱（本次踩过，务必记住）**：统计「定义是否被引用」时，若**不排除定义文件 `Grobal2.pas`**，
会得到「216 条全部有引用」的**假象**——每条定义在自己的文件里至少出现一次。
排除后，服务端真实覆盖是 **194/216**。**凡做「定义 vs 引用」差集，必须先排除定义文件。**

**两端都零引用的 4 条（最强死代码候选）**：
`SM_SPELL2`(117)、`SM_ID_NOTFOUND`(502)、`SM_ITEMUPDATE`(1500)、`SM_MONSTERSAY`(1501)。

**结论**：「是否被引用」**不能单独**决定可达性。

---

## 2.5 ★ 与 mir2go 标杆对拍

[`$WS/mir2go`](../../mir2go) 是**反复调研各版本后推进、行为被 46/46 e2e 验证过**的实现（[plan.md §9](./plan.md)），
是**行为 / 数值**的标杆。

⚠️ **但它不是"消息选集"的标杆**——这是本次实测发现的，务必记住：

- `internal/gamesvr/gold.go:96` 把**地面物品**用 **`SM_ADDITEM`(200)** 发送；
  而原版客户端 `ClMain.pas:4403/4433` 里 `SM_ADDITEM` → **进背包**、`SM_ITEMSHOW`(610) → **地面物品**
  ⇒ **mir2go 为同一功能选了不同的消息号**。
- 根因：mir2go 的 e2e 是**自己的服务端 ⇄ 自己的客户端**（`mir2cli`），
  "字节级兼容"是**自我一致**，并不构成对原版客户端的证明。

**因此**：可达性判据改用 **「客户端分派 ∩ 服务端实际发送」**（见 §3）；
mir2go 只作**行为 / 数值**参考，以及"这功能值不值得做"的旁证。

`mir2go/internal/proto/msgid.go` 完整移植了 `Common/Grobal2.pas` 的常量表，镜像了同样的 216 个 `SM_`。
真正有意义的是**除 `msgid.go` 之外**的引用（= 真实现）：

| 集合 | 定义 | mir2go 实现 | 覆盖率 |
|---|---:|---:|---:|
| `SM_`（服务端 → 客户端） | 216 | **142** | 66% |
| `CM_`（客户端 → 服务端） | 87 | **62** | 71% |

⚠️ **顺带修正一个范围错误**：`Client/Grobal2.pas` 其实是**一整张协议常量表**，不只是 `SM_`：
`CM_` **87**、`SM_` 216、`RM_` 126、`SS_` 24、`SG_` 7、`GS_` 3、`DB_` 6。
**IDL 必须双向**（`CM_` 是客户端发出的请求），所以 M0 的清单**不能只做 `SM_`**——`CM_` 87 个需补齐（下一步）。

```bash
# 复现（关键：排除 msgid.go 自身）
cd $WS/mir2go
find . -name '*.go' -not -name 'msgid.go' -print0 | xargs -0 grep -ohE 'SM_[A-Z0-9_]+' | sort -u   # → 142
find . -name '*.go' -not -name 'msgid.go' -print0 | xargs -0 grep -ohE 'CM_[A-Z0-9_]+' | sort -u   # → 62
```

**mir2go 未实现的 74 个 `SM_`**（`msgid.go` 有定义、其余代码零引用）——这才是「不做 / 待确认」的候选：

```
SM_41 SM_43 SM_716 SM_ACTION2_MAX SM_ACTION2_MIN SM_ACTION_MAX SM_ACTION_MIN
SM_ADDMAGIC SM_ADJUST_BONUS SM_ALIVE SM_CHANGEFACE SM_CHANGELIGHT
SM_CHGPASSWD_FAIL SM_CHGPASSWD_SUCCESS SM_CRSHIT SM_DELITEM SM_DELITEMS SM_DELMAGIC
SM_DIGDOWN SM_DIGUP SM_DONATE_FAIL SM_DONATE_OK SM_DROPITEM_FAIL
SM_EXCHGTAKEON_FAIL SM_EXCHGTAKEON_OK SM_FLYAXE SM_GAMEGOLDNAME
SM_GETBACKPASSWD_FAIL SM_GETBACKPASSWD_SUCCESS SM_GETREGINFO SM_GHOST SM_GROUPMESSAGE
SM_GUILDBREAKALLY_FAIL SM_HIDE SM_HORSERUN SM_ID_NOTFOUND SM_INSTANCEHEALGUAGE
SM_ITEMSHOW SM_ITEMUPDATE SM_LAMPCHANGEDURA SM_LIGHTING SM_MENU_OK
SM_MERCHANTDLGCLOSE SM_MERCHANTSAY SM_MONSTERSAY SM_MYSTATUS SM_NEEDPASSWORD
SM_NEEDUPDATE_ACCOUNT SM_NEWID_FAIL SM_NEWID_SUCCESS SM_OPENDOOR_LOCK SM_PASSWORD
SM_PASSWORDSTATUS SM_PLAYDICE SM_SENDBUYPRICE SM_SENDDETAILGOODSLIST SM_SENDGAMELIST
SM_SENDREPAIRCOST SM_SENDUSERSELL SM_SENDUSERSTATE SM_SPACEMOVE_HIDE SM_SPACEMOVE_HIDE2
SM_SPACEMOVE_SHOW SM_SPACEMOVE_SHOW2 SM_SPELL2 SM_TEST SM_THROW SM_TIMECHECK_MSG
SM_TWINHIT SM_UPDATEID_FAIL SM_UPDATEID_SUCCESS SM_UPDATEITEM SM_VERSION_FAIL SM_WEIGHTCHANGED
```

⚠️ **别把它直接当"死代码"**。mir2go 未用有几种成因，必须分别处置：

1. **确认不做**（依据见 [mir2go/docs/gap-audit.md §4](../../mir2go/docs/gap-audit.md)）：
   - `SM_HORSERUN` —— **物品表里无坐骑数据** ⇒ 客户端无处可骑
   - `SM_ADJUST_BONUS` —— 属性点获取路径被 `{$IFDEF FOR_ABIL_POINT}` **编译期剔除**
   - `SM_PASSWORD*` / `SM_NEEDPASSWORD` —— 仓库锁出厂 `PasswordLockSystem=0`
   - `SM_GETREGINFO` / `SM_GETBACKPASSWD_*` / `SM_SENDGAMELIST` —— GeeM2 网关扩展
2. **我们可能要比 mir2go 更全**：`SM_ADDMAGIC`/`SM_DELMAGIC`/`SM_DELITEM`/`SM_ITEMSHOW`/
   `SM_MERCHANTSAY`/`SM_SENDUSERSELL`/`SM_MYSTATUS`/`SM_WEIGHTCHANGED`/`SM_ALIVE` 等**属核心玩法**，
   mir2go 或许经 `RM_*` 内部流或其它路径间接覆盖 ⇒ **必须逐条人工确认，不能盲从本次差集**。
3. **值域 ≥ 5000 的引擎扩展**（17 个）⇒ 归 T3。

> **下一步**：本清单要逐条标注「mir2go 是否实现 + 若否，是'确认不做'还是'待确认'」，
> 并把 `CM_` 87 个补进来——两者合起来才是完整协议面。

---

## 3. 分档

**判据优先级**：**「客户端分派 ∩ 服务端实际发送」** → **值域** → mir2go（**仅参考**，理由见 §2.5）。

| 档 | 规则 | 数 |
|---|---|---:|
| **T1 1.76 可达** | 值 < 5000 **且** 客户端有分派 **且** 服务端实际发送 | **172** |
| **T2 待定 / 死代码** | 值 < 5000 且非上述 | **26** |
| **T3 引擎扩展** | 值 ≥ 5000 | **18** |

172 + 26 + 18 = **216** ✓

⚠️ `T2` 的 26 条**必须再拆两类**（见 §6）：**11 条**是「两端都没」（真死代码），
**15 条**是「仅服务端发送、客户端在别处处理」（聊天/仓库类，**其实可达**）。

**T2 死代码（7）**

| 值 | 名称 | 说明 |
|---:|---|---|
| 43 | `SM_43` | 占位，无处理 |
| 117 | `SM_SPELL2` | 仅占位（另有 `SM_SPELL`=17） |
| 500 | `SM_CERTIFICATION_SUCCESS` | 被 `SM_PASSOK_SELECTSERVER`(529) 取代 |
| 501 | `SM_CERTIFICATION_FAIL` | 同上 |
| 502 | `SM_ID_NOTFOUND` | 被 `SM_PASSWD_FAIL`(503) 统一 |
| 1500 | `SM_ITEMUPDATE` | 无处理 |
| 1501 | `SM_MONSTERSAY` | 无处理 |

**T3 引擎扩展（18）**：`SM_SENDGAMELIST`(5002)、`SM_GETBACKPASSWD_SUCCESS/FAIL`(5005/5006)、`SM_SERVERCONFIG`(5007)、`SM_GAMEGOLDNAME`(5008)、`SM_PASSWORD`(5009)、`SM_HORSERUN`(5010)、`SM_PLAYDICE`(8001)、`SM_PASSWORDSTATUS`(8002)、`SM_NEEDPASSWORD`(8003)、`SM_GETREGINFO`(8004)、`SM_EXCHGTAKEON_OK/FAIL`(65023/65024)、`SM_TEST`(65037)、`SM_ACTION_MIN/MAX`(65070/65071)、`SM_ACTION2_MIN/MAX`(65072/65073)

---

## 4. 值域分区

| 值域 | 张 | 内容 |
|---|---:|---|
| 4–54 | 43 | 动作 / 状态（走、跑、攻击、受击、死亡、升级…） |
| 100–117 | 6 | 聊天（系统 / 组队 / 喊 / 私聊 / 行会） |
| 200–212 | 7 | 物品 / 魔法 |
| 500–533 | 22 | 账号 / 登录 / 选区 / 选角 / 建角 / 改密 |
| 600–772 | 102 | 世界 / 交易 / 商店 / 仓库 / 组队 / 行会 / NPC |
| 800–811 | 10 | 空间移动 / 事件 / 时间 |
| 1100–1106 | 6 | 生命值 / 武器 / 版本 |
| 1500–1501 | 2 | 物品更新 / 怪物说话 |
| 5002–5010 | 7 | 区服列表 / 取回密码 / 服务器配置 / 游戏币名 / 骑马 / 密码 |
| 8001–8004 | 4 | 骰子 / 密码状态 / 注册信息 |
| 65023–65073 | 7 | 交易穿戴 / 测试 / 动作区间 |

**判读**：**5000+** 与 **65000+** 两段是**引擎私有扩展**（1.76 原始协议止于 ~1500 一带）。
`SM_HORSERUN`(5010) 客户端引用 **25 次**，是「扩展且客户端活跃」的典型；
但 **mir2go 未实现**，依据是**物品表里无坐骑数据**（[gap-audit §4](../../mir2go/docs/gap-audit.md)）⇒ 不做（见 §6.4）。

---

## 5. ⚠️ 窗口 × 协议 交叉核对（关键发现）

`FState.pas` 里**存在**下列窗口，但 `Grobal2.pas` 的 216 条定义里**没有**对应协议消息：

| 窗口 | 协议消息 | 触发方式 | 结论 |
|---|---|---|---|
| `DMailListDlg` 邮件列表 | **无** | `ClMain.pas` **零引用** | UI 残留，无协议支撑 |
| `DFriendDlg` 好友 | **无** | `FState.pas:6797` 本地按钮 toggle 可见性 | UI 残留 |
| `DBlockListDlg` 黑名单 | **无** | `ClMain.pas` **零引用** | UI 残留 |
| `DMemo` 备忘录 | **无** | 本地 | UI 残留 |

在 216 条定义里搜索 `MAIL|FRIEND|BLOCK|MEMO|QUEST|AUCTION|STALL|MARR|WEATHER` → **零命中**。

**意义**：这**回答了** [legacy-analysis.md §4.3](./legacy-analysis.md) 遗留的疑问（"待重新评估：邮件、好友、黑名单、备忘录"）——
本版协议里它们**没有消息**，客户端也不会因服务端消息打开它们。
⇒ 按 [plan.md §1](./plan.md)「不做遗留客户端里没有的东西」，**这四项不在范围内**（可选保留 UI 外观）。
⚠️ 待确认：是否存在**不在** `Grobal2.pas` 216 条内的引擎扩展消息驱动它们。

---

## 6. 待决策清单

1. **T2 的 26 条必须拆两类**（**不要**用单一判据看）：
   - **11 条「两端都没」= 真死代码候选**：
     `SM_ACTION_MIN` `SM_ACTION_MAX` `SM_ACTION2_MIN` `SM_ACTION2_MAX` `SM_SPELL2` `SM_ID_NOTFOUND`
     `SM_ITEMUPDATE` `SM_MONSTERSAY` `SM_THROW` `SM_CRY` `SM_GROUPMESSAGE`
   - **15 条「仅服务端发送」= 客户端在别处处理，其实可达**：
     `SM_41` `SM_43` `SM_CERTIFICATION_SUCCESS` `SM_CERTIFICATION_FAIL` `SM_GETBACKPASSWD_SUCCESS`
     `SM_GETBACKPASSWD_FAIL` `SM_GHOST` `SM_GUILDMAKEALLY_OK` `SM_GUILDBREAKALLY_OK` `SM_GUILDBREAKALLY_FAIL`
     `SM_GUILDMESSAGE` `SM_SENDGAMELIST` `SM_STORAGE_OK` `SM_STORAGE_FULL` `SM_WHISPER`
     `SM_TAKEBACKSTORAGEITEM_OK` `SM_TAKEBACKSTORAGEITEM_FAIL`
2. **`SM_READYFIREHIT`**：被 `Actor.pas` / `ClMain.pas` 引用，但**未在 `Grobal2.pas` 定义** → 疑似拼写错误或另有取值，需查证。
3. **T3（18 条，值 ≥ 5000）：默认延后**。已可判「不做」的：
   `SM_SENDGAMELIST` / `SM_GETBACKPASSWD_*` / `SM_GETREGINFO`（GeeM2 网关扩展）、
   `SM_PASSWORD*` / `SM_NEEDPASSWORD`（仓库锁出厂关闭）、
   `SM_ADJUST_BONUS`（属性点获取被 `{$IFDEF}` 剔除）、
   `SM_HORSERUN`（**物品表中无坐骑数据**）——依据 [mir2go gap-audit §4](../../mir2go/docs/gap-audit.md)。
4. **T1（172 条）里 mir2go 未实现的 44 条**：原版服务端**确实发送**它们，
   ⇒ **不能**以「mir2go 未实现」为由判"不做"（地面物品已证实 mir2go 只是换了消息号）。
   需要时逐条对照原版服务端行为。
5. **`SM_CRY` / `SM_GROUPMESSAGE` 的疑点**：两端都没——聊天在原版很可能统一走 `SM_HEAR`/`SM_SYSMESSAGE`
   ⇒ 需确认这两个号是否已被弃用（若是，`SM_CRY`/`SM_GROUPMESSAGE` 归死代码）。

---

## 7. 完整清单（216 行）

列：`值 | 名称 | 客引 | 服引 | 档 | M | 原注释`。

- 「客引 / 服引」= 源码引用次数（**已排除定义文件本身**）
- **`M`** = mir2go 是否实现（`Y` = `msgid.go` 之外有引用；`-` = 未实现）
- 「档」：`T1` 可达 / `T2` 客户端零引用 / `T3` 值 ≥ 5000
- 注释里的 `?????` 是**原作者自己写的问号**，不是转码丢失（`iconv` 零错误）

| 值 | 名称 | 客引 | 服引 | 档 | M | 原注释 |
|---:|---|---:|---:|:--:|:--:|---|
| 4 | SM_41 | 1 | 1 | T2 | - |  |
| 5 | SM_THROW | 6 | 0 | T2 | - |  |
| 6 | SM_RUSH | 16 | 1 | T1 | Y |  |
| 7 | SM_RUSHKUNG | 15 | 1 | T1 | Y |  |
| 8 | SM_FIREHIT | 8 | 1 | T1 | Y | 烈火 |
| 9 | SM_BACKSTEP | 34 | 1 | T1 | Y | 走路不成功???? |
| 10 | SM_TURN | 24 | 1 | T1 | Y | 转动方向 |
| 11 | SM_WALK | 35 | 1 | T1 | Y | 走 |
| 12 | SM_SITDOWN | 6 | 0 | T2 | Y | 挖 |
| 13 | SM_RUN | 27 | 1 | T1 | Y | 跑 |
| 14 | SM_HIT | 41 | 1 | T1 | Y | 砍 |
| 15 | SM_HEAVYHIT | 6 | 1 | T1 | Y |  |
| 16 | SM_BIGHIT | 4 | 1 | T1 | Y |  |
| 17 | SM_SPELL | 9 | 1 | T1 | Y | 使用魔法 |
| 18 | SM_POWERHIT | 7 | 1 | T1 | Y | 攻杀 |
| 19 | SM_LONGHIT | 7 | 1 | T1 | Y | 刺杀 |
| 20 | SM_DIGUP | 19 | 1 | T1 | - | 挖取 |
| 21 | SM_DIGDOWN | 9 | 1 | T1 | - | 挖下????????? |
| 22 | SM_FLYAXE | 13 | 1 | T1 | - | ??????????????? |
| 23 | SM_LIGHTING | 18 | 1 | T1 | - | 天亮????????????? |
| 24 | SM_WIDEHIT | 7 | 1 | T1 | Y | 半月 |
| 25 | SM_CRSHIT | 8 | 1 | T1 | - |  |
| 26 | SM_TWINHIT | 8 | 1 | T1 | - |  |
| 27 | SM_ALIVE | 10 | 1 | T1 | - |  |
| 28 | SM_MOVEFAIL | 3 | 1 | T1 | Y |  |
| 29 | SM_HIDE | 5 | 0 | T2 | - |  |
| 30 | SM_DISAPPEAR | 3 | 1 | T1 | Y | 物品消失?????? |
| 31 | SM_STRUCK | 25 | 1 | T1 | Y | 弯腰 |
| 32 | SM_DEATH | 17 | 1 | T1 | Y |  |
| 33 | SM_SKELETON | 11 | 1 | T1 | Y | SM_DEATH 尸骨??尸体 |
| 34 | SM_NOWDEATH | 25 | 1 | T1 | Y |  |
| 40 | SM_HEAR | 3 | 4 | T1 | Y | 听到说话 |
| 41 | SM_FEATURECHANGED | 4 | 3 | T1 | Y | 容貌??特征??改变??????????? |
| 42 | SM_USERNAME | 3 | 2 | T1 | Y | 用户名??玩家名??????? |
| 43 | SM_43 | 0 | 1 | T2 | - |  |
| 44 | SM_WINEXP | 3 | 1 | T1 | Y | 胜利指数???杀怪获得的经验值??????????????? |
| 45 | SM_LEVELUP | 3 | 1 | T1 | Y | 等级提升 |
| 46 | SM_DAYCHANGING | 3 | 1 | T1 | Y | 日期正在改变???? |
| 50 | SM_LOGON | 4 | 1 | T1 | Y | 登录注册 |
| 51 | SM_NEWMAP | 5 | 1 | T1 | Y | 新地图 |
| 52 | SM_ABILITY | 1 | 3 | T1 | Y | 能力 |
| 53 | SM_HEALTHSPELLCHANGED | 3 | 1 | T1 | Y | 红血兰血 改变 |
| 54 | SM_MAPDESCRIPTION | 1 | 1 | T1 | Y | 地图形容,地图描述 |
| 117 | SM_SPELL2 | 0 | 0 | T2 | - |  |
| 100 | SM_SYSMESSAGE | 1 | 6 | T1 | Y | 系统消息 |
| 101 | SM_GROUPMESSAGE | 1 | 0 | T2 | - | 组队消息 |
| 102 | SM_CRY | 1 | 0 | T2 | Y | 喊 |
| 103 | SM_WHISPER | 1 | 3 | T2 | Y | 私聊 |
| 104 | SM_GUILDMESSAGE | 2 | 4 | T2 | Y | 行会信息 |
| 200 | SM_ADDITEM | 1 | 2 | T1 | Y | 添加物品 |
| 201 | SM_BAGITEMS | 1 | 2 | T1 | Y | 包裹物品 |
| 202 | SM_DELITEM | 1 | 2 | T1 | - | 删除物品???? |
| 203 | SM_UPDATEITEM | 1 | 2 | T1 | - |  |
| 210 | SM_ADDMAGIC | 1 | 1 | T1 | - | 添加魔法 |
| 211 | SM_SENDMYMAGIC | 1 | 1 | T1 | Y | 我所会的魔法 |
| 212 | SM_DELMAGIC | 1 | 1 | T1 | - |  |
| 500 | SM_CERTIFICATION_SUCCESS | 0 | 2 | T2 | Y |  |
| 501 | SM_CERTIFICATION_FAIL | 0 | 1 | T2 | Y |  |
| 502 | SM_ID_NOTFOUND | 0 | 0 | T2 | - | ID未发现,用户名错误 |
| 503 | SM_PASSWD_FAIL | 1 | 1 | T1 | Y | 密码错误 |
| 504 | SM_NEWID_SUCCESS | 1 | 1 | T1 | - | 创建新ID成功 |
| 505 | SM_NEWID_FAIL | 1 | 1 | T1 | - | 新ID失败 |
| 506 | SM_CHGPASSWD_SUCCESS | 1 | 1 | T1 | - | 更改密码成功 |
| 507 | SM_CHGPASSWD_FAIL | 1 | 1 | T1 | - | 更改密码失败 |
| 520 | SM_QUERYCHR | 1 | 1 | T1 | Y | 查询人物(2人窗口) |
| 521 | SM_NEWCHR_SUCCESS | 1 | 1 | T1 | Y | 创建人物成功 |
| 522 | SM_NEWCHR_FAIL | 1 | 1 | T1 | Y | 创建人物失败 |
| 523 | SM_DELCHR_SUCCESS | 1 | 1 | T1 | Y | 删除人物成功 |
| 524 | SM_DELCHR_FAIL | 1 | 1 | T1 | Y | 删除人物失败 |
| 525 | SM_STARTPLAY | 1 | 1 | T1 | Y | 开始游戏 |
| 526 | SM_STARTFAIL | 1 | 2 | T1 | Y | 进入游戏失败 |
| 527 | SM_QUERYCHR_FAIL | 1 | 1 | T1 | Y | 查询人物失败 |
| 528 | SM_OUTOFCONNECTION | 2 | 4 | T1 | Y | 连接已断开 |
| 529 | SM_PASSOK_SELECTSERVER | 1 | 2 | T1 | Y | 用户名/密码 验证通过 |
| 530 | SM_SELECTSERVER_OK | 1 | 1 | T1 | Y | 服务器选择成功 |
| 531 | SM_NEEDUPDATE_ACCOUNT | 1 | 1 | T1 | - | 需要更新_说明???? |
| 532 | SM_UPDATEID_SUCCESS | 1 | 1 | T1 | - | 更新ID成功????? |
| 533 | SM_UPDATEID_FAIL | 1 | 1 | T1 | - | 更新ID失败??????? |
| 600 | SM_DROPITEM_SUCCESS | 1 | 1 | T1 | Y | 丢弃物品成功 |
| 601 | SM_DROPITEM_FAIL | 1 | 1 | T1 | - | 丢弃物品失败 |
| 610 | SM_ITEMSHOW | 3 | 1 | T1 | - | 显示物品 |
| 611 | SM_ITEMHIDE | 3 | 1 | T1 | Y | 地上的物品消失 |
| 612 | SM_OPENDOOR_OK | 1 | 1 | T1 | Y | 开门成功 |
| 613 | SM_OPENDOOR_LOCK | 1 | 0 | T2 | - |  |
| 614 | SM_CLOSEDOOR | 1 | 1 | T1 | Y |  |
| 615 | SM_TAKEON_OK | 1 | 2 | T1 | Y | 穿上戴上成功 |
| 616 | SM_TAKEON_FAIL | 1 | 1 | T1 | Y | 穿失败 |
| 619 | SM_TAKEOFF_OK | 1 | 1 | T1 | Y | 脱下成功 |
| 620 | SM_TAKEOFF_FAIL | 1 | 1 | T1 | Y | 脱下失败 |
| 621 | SM_SENDUSEITEMS | 1 | 2 | T1 | Y | 身上穿戴物品 |
| 622 | SM_WEIGHTCHANGED | 1 | 1 | T1 | - | 背包重量改变 |
| 633 | SM_CLEAROBJECTS | 1 | 1 | T1 | Y | 清除对象?????????? |
| 634 | SM_CHANGEMAP | 4 | 1 | T1 | Y | 地图改变 |
| 635 | SM_EAT_OK | 1 | 1 | T1 | Y | 吃物品成功 |
| 636 | SM_EAT_FAIL | 1 | 1 | T1 | Y | 吃物品失败 |
| 637 | SM_BUTCH | 1 | 1 | T1 | Y |  |
| 638 | SM_MAGICFIRE | 6 | 1 | T1 | Y | 魔法火????????????? |
| 639 | SM_MAGICFIRE_FAIL | 3 | 1 | T1 | Y | 魔法火失败????????????? |
| 640 | SM_MAGIC_LVEXP | 1 | 1 | T1 | Y | 魔法等级 |
| 642 | SM_DURACHANGE | 1 | 1 | T1 | Y |  |
| 643 | SM_MERCHANTSAY | 1 | 2 | T1 | - | 商人说话 |
| 644 | SM_MERCHANTDLGCLOSE | 1 | 1 | T1 | - | 商人窗口关闭 |
| 645 | SM_SENDGOODSLIST | 1 | 1 | T1 | Y | 货物列表 |
| 646 | SM_SENDUSERSELL | 1 | 1 | T1 | - | 用户出售 |
| 647 | SM_SENDBUYPRICE | 1 | 1 | T1 | - | 购买价格 |
| 648 | SM_USERSELLITEM_OK | 1 | 1 | T1 | Y | 用户出售物品成功 |
| 649 | SM_USERSELLITEM_FAIL | 1 | 1 | T1 | Y | 用户出售物品失败 |
| 650 | SM_BUYITEM_SUCCESS | 1 | 1 | T1 | Y | 用户购买物品成功 |
| 651 | SM_BUYITEM_FAIL | 1 | 1 | T1 | Y | 用户购买失败 |
| 652 | SM_SENDDETAILGOODSLIST | 1 | 1 | T1 | - | 详细货物列表 |
| 653 | SM_GOLDCHANGED | 1 | 1 | T1 | Y | 金币改变 |
| 654 | SM_CHANGELIGHT | 1 | 1 | T1 | - | 改变亮度???? |
| 655 | SM_LAMPCHANGEDURA | 1 | 1 | T1 | - |  |
| 656 | SM_CHANGENAMECOLOR | 3 | 1 | T1 | Y | 改变宝宝颜色????? |
| 657 | SM_CHARSTATUSCHANGED | 4 | 1 | T1 | Y |  |
| 658 | SM_SENDNOTICE | 2 | 2 | T1 | Y | 进入游戏弹出窗口 |
| 659 | SM_GROUPMODECHANGED | 1 | 2 | T1 | Y |  |
| 660 | SM_CREATEGROUP_OK | 1 | 1 | T1 | Y | 创建编组成功 |
| 661 | SM_CREATEGROUP_FAIL | 1 | 4 | T1 | Y | 创建编组失败 |
| 662 | SM_GROUPADDMEM_OK | 1 | 1 | T1 | Y |  |
| 663 | SM_GROUPDELMEM_OK | 1 | 1 | T1 | Y |  |
| 664 | SM_GROUPADDMEM_FAIL | 1 | 5 | T1 | Y |  |
| 665 | SM_GROUPDELMEM_FAIL | 1 | 3 | T1 | Y |  |
| 666 | SM_GROUPCANCEL | 1 | 2 | T1 | Y | 编组取消?????????? |
| 667 | SM_GROUPMEMBERS | 1 | 1 | T1 | Y | 编组成员 |
| 668 | SM_SENDUSERREPAIR | 1 | 1 | T1 | Y |  |
| 669 | SM_USERREPAIRITEM_OK | 1 | 1 | T1 | Y |  |
| 670 | SM_USERREPAIRITEM_FAIL | 1 | 1 | T1 | Y |  |
| 671 | SM_SENDREPAIRCOST | 1 | 1 | T1 | - |  |
| 673 | SM_DEALMENU | 1 | 1 | T1 | Y |  |
| 674 | SM_DEALTRY_FAIL | 1 | 2 | T1 | Y |  |
| 675 | SM_DEALADDITEM_OK | 1 | 3 | T1 | Y |  |
| 676 | SM_DEALADDITEM_FAIL | 1 | 1 | T1 | Y |  |
| 677 | SM_DEALDELITEM_OK | 1 | 1 | T1 | Y |  |
| 678 | SM_DEALDELITEM_FAIL | 1 | 3 | T1 | Y |  |
| 681 | SM_DEALCANCEL | 1 | 1 | T1 | Y |  |
| 682 | SM_DEALREMOTEADDITEM | 1 | 4 | T1 | Y |  |
| 683 | SM_DEALREMOTEDELITEM | 1 | 2 | T1 | Y |  |
| 684 | SM_DEALCHGGOLD_OK | 1 | 1 | T1 | Y |  |
| 685 | SM_DEALCHGGOLD_FAIL | 1 | 2 | T1 | Y |  |
| 686 | SM_DEALREMOTECHGGOLD | 1 | 1 | T1 | Y |  |
| 687 | SM_DEALSUCCESS | 1 | 2 | T1 | Y |  |
| 700 | SM_SENDUSERSTORAGEITEM | 1 | 1 | T1 | Y |  |
| 701 | SM_STORAGE_OK | 2 | 1 | T2 | Y |  |
| 702 | SM_STORAGE_FULL | 2 | 1 | T2 | Y |  |
| 703 | SM_STORAGE_FAIL | 1 | 1 | T1 | Y |  |
| 704 | SM_SAVEITEMLIST | 1 | 3 | T1 | Y |  |
| 705 | SM_TAKEBACKSTORAGEITEM_OK | 2 | 1 | T2 | Y |  |
| 706 | SM_TAKEBACKSTORAGEITEM_FAIL | 1 | 1 | T2 | Y |  |
| 707 | SM_TAKEBACKSTORAGEITEM_FULLBAG | 2 | 1 | T1 | Y |  |
| 766 | SM_AREASTATE | 1 | 1 | T1 | Y | 地区状态 |
| 708 | SM_MYSTATUS | 1 | 1 | T1 | - |  |
| 709 | SM_DELITEMS | 1 | 2 | T1 | - | 删除物品?????? |
| 710 | SM_READMINIMAP_OK | 1 | 1 | T1 | Y |  |
| 711 | SM_READMINIMAP_FAIL | 1 | 1 | T1 | Y |  |
| 712 | SM_SENDUSERMAKEDRUGITEMLIST | 1 | 1 | T1 | Y |  |
| 713 | SM_MAKEDRUG_SUCCESS | 1 | 1 | T1 | Y |  |
| 714 | SM_MAKEDRUG_FAIL | 1 | 1 | T1 | Y |  |
| 716 | SM_716 | 1 | 1 | T1 | - |  |
| 750 | SM_CHANGEGUILDNAME | 1 | 3 | T1 | Y | 改变行会名称 |
| 751 | SM_SENDUSERSTATE | 1 | 2 | T1 | - |  |
| 752 | SM_SUBABILITY | 1 | 2 | T1 | Y |  |
| 753 | SM_OPENGUILDDLG | 1 | 1 | T1 | Y | 打开行会窗口 |
| 754 | SM_OPENGUILDDLG_FAIL | 1 | 1 | T1 | Y | 打开行会窗口失败 |
| 756 | SM_SENDGUILDMEMBERLIST | 1 | 1 | T1 | Y | 行会成员列表 |
| 757 | SM_GUILDADDMEMBER_OK | 1 | 1 | T1 | Y | 行会添加成员成功 |
| 758 | SM_GUILDADDMEMBER_FAIL | 1 | 1 | T1 | Y | 行会添加成员失败 |
| 759 | SM_GUILDDELMEMBER_OK | 1 | 1 | T1 | Y | 行会删除成员成功 |
| 760 | SM_GUILDDELMEMBER_FAIL | 1 | 1 | T1 | Y | 行会删除成员失败 |
| 761 | SM_GUILDRANKUPDATE_FAIL | 1 | 1 | T1 | Y | 行会等级/排列更新失败 |
| 762 | SM_BUILDGUILD_OK | 1 | 1 | T1 | Y | 创建行会成功 |
| 763 | SM_BUILDGUILD_FAIL | 1 | 1 | T1 | Y | 创建行会失败 |
| 764 | SM_DONATE_OK | 1 | 1 | T1 | - |  |
| 765 | SM_DONATE_FAIL | 1 | 1 | T1 | - |  |
| 767 | SM_MENU_OK | 1 | 1 | T1 | - | ? |
| 768 | SM_GUILDMAKEALLY_OK | 1 | 1 | T2 | Y | 创建行会同盟成功 |
| 769 | SM_GUILDMAKEALLY_FAIL | 1 | 2 | T1 | Y | 创建行会同盟失败 |
| 770 | SM_GUILDBREAKALLY_OK | 1 | 1 | T2 | Y | 删除行会同盟成功 |
| 771 | SM_GUILDBREAKALLY_FAIL | 1 | 0 | T2 | - | 删除行会同盟失败 |
| 772 | SM_DLGMSG | 1 | 0 | T2 | Y | 窗口消息????弹出窗口??????? |
| 800 | SM_SPACEMOVE_HIDE | 4 | 1 | T1 | - |  |
| 801 | SM_SPACEMOVE_SHOW | 4 | 1 | T1 | - |  |
| 802 | SM_RECONNECT | 2 | 2 | T1 | Y |  |
| 803 | SM_GHOST | 1 | 1 | T2 | - |  |
| 804 | SM_SHOWEVENT | 3 | 1 | T1 | Y | 显示事件???????? |
| 805 | SM_HIDEEVENT | 3 | 1 | T1 | Y | 隐藏事件????????? |
| 806 | SM_SPACEMOVE_HIDE2 | 2 | 1 | T1 | - |  |
| 807 | SM_SPACEMOVE_SHOW2 | 2 | 1 | T1 | - |  |
| 810 | SM_TIMECHECK_MSG | 1 | 0 | T2 | - |  |
| 811 | SM_ADJUST_BONUS | 1 | 1 | T1 | - | ? |
| 1100 | SM_OPENHEALTH | 1 | 1 | T1 | Y | 打开健康???????? |
| 1101 | SM_CLOSEHEALTH | 1 | 1 | T1 | Y | 关闭健康??????? |
| 1104 | SM_CHANGEFACE | 1 | 1 | T1 | - |  |
| 1102 | SM_BREAKWEAPON | 1 | 1 | T1 | Y |  |
| 1103 | SM_INSTANCEHEALGUAGE | 1 | 1 | T1 | - | ?? |
| 1106 | SM_VERSION_FAIL | 2 | 1 | T1 | - |  |
| 1500 | SM_ITEMUPDATE | 0 | 0 | T2 | - |  |
| 1501 | SM_MONSTERSAY | 0 | 0 | T2 | - | 怪物说话 |
| 65023 | SM_EXCHGTAKEON_OK | 1 | 0 | T3 | - |  |
| 65024 | SM_EXCHGTAKEON_FAIL | 1 | 0 | T3 | - |  |
| 65037 | SM_TEST | 2 | 0 | T3 | - |  |
| 65070 | SM_ACTION_MIN | 1 | 0 | T3 | - |  |
| 65071 | SM_ACTION_MAX | 1 | 0 | T3 | - |  |
| 65072 | SM_ACTION2_MIN | 1 | 0 | T3 | - |  |
| 65073 | SM_ACTION2_MAX | 1 | 0 | T3 | - |  |
| 5002 | SM_SENDGAMELIST | 0 | 3 | T3 | - |  |
| 5005 | SM_GETBACKPASSWD_SUCCESS | 0 | 1 | T3 | - |  |
| 5006 | SM_GETBACKPASSWD_FAIL | 0 | 1 | T3 | - |  |
| 5007 | SM_SERVERCONFIG | 1 | 1 | T3 | Y |  |
| 5008 | SM_GAMEGOLDNAME | 1 | 3 | T3 | - |  |
| 5009 | SM_PASSWORD | 1 | 1 | T3 | - |  |
| 5010 | SM_HORSERUN | 25 | 1 | T3 | - |  |
| 8001 | SM_PLAYDICE | 1 | 1 | T3 | - |  |
| 8002 | SM_PASSWORDSTATUS | 1 | 1 | T3 | - |  |
| 8003 | SM_NEEDPASSWORD | 1 | 0 | T3 | - |  |
| 8004 | SM_GETREGINFO | 1 | 0 | T3 | - |  |

---

## 8. `CM_` 完整清单（87 行，客户端 → 服务端）

判据与 §7 **相反**：`CM_` 是客户端发出的**请求**，所以「**服务端是否处理**」才有意义——
`服引 = 0` 才是死请求候选（而不是"客户端没引用"）。

列同上：`值 | 名称 | 客引 | 服引 | 档 | M | 原注释`。

- **档**：`T1` 服务端处理 / `T2` 服务端零引用（死请求候选）/ `T3` 值 ≥ 5000
- **`M`**：mir2go 是否实现（**62 / 87**）

| 值 | 名称 | 客引 | 服引 | 档 | M | 原注释 |
|---:|---|---:|---:|:--:|:--:|---|
| 0 | CM_POWERBLOCK | 1 | 0 | T2 | - | Damian |
| 82 | CM_QUERYUSERSTATE | 1 | 1 | T1 | - |  |
| 80 | CM_QUERYUSERNAME | 1 | 2 | T1 | - | 查询用户姓名 |
| 81 | CM_QUERYBAGITEMS | 1 | 1 | T1 | Y | 查询包裹内容 |
| 100 | CM_QUERYCHR | 1 | 1 | T1 | Y | 查询人物 |
| 101 | CM_NEWCHR | 1 | 1 | T1 | Y | 新人物 |
| 102 | CM_DELCHR | 1 | 1 | T1 | Y | 删除人物 |
| 103 | CM_SELCHR | 1 | 1 | T1 | Y | 选择人物 |
| 104 | CM_SELECTSERVER | 1 | 1 | T1 | Y | 选择服务器 |
| 1002 | CM_OPENDOOR | 1 | 1 | T1 | Y | 开门 |
| 1009 | CM_SOFTCLOSE | 1 | 1 | T1 | Y |  |
| 1000 | CM_DROPITEM | 1 | 3 | T1 | Y | 丢掉物品 |
| 1001 | CM_PICKUP | 1 | 2 | T1 | Y | 拣东西 |
| 1003 | CM_TAKEONITEM | 1 | 2 | T1 | Y | 穿上/戴上/拿上 物品 |
| 1004 | CM_TAKEOFFITEM | 1 | 2 | T1 | Y | 脱下物品 |
| 1005 | CM_1005 | 0 | 1 | T1 | - |  |
| 1006 | CM_EAT | 1 | 1 | T1 | Y | 吃物品 |
| 1007 | CM_BUTCH | 1 | 2 | T1 | Y |  |
| 1008 | CM_MAGICKEYCHANGE | 1 | 1 | T1 | - | 改变魔法按键 |
| 1010 | CM_CLICKNPC | 1 | 1 | T1 | Y | 点击NPC??? |
| 1011 | CM_MERCHANTDLGSELECT | 1 | 2 | T1 | Y | NPC Tag Click 选择商人功能窗口 |
| 1012 | CM_MERCHANTQUERYSELLPRICE | 1 | 2 | T1 | - | 查询出卖给商人的价格 |
| 1013 | CM_USERSELLITEM | 1 | 2 | T1 | Y | 选择物品 |
| 1014 | CM_USERBUYITEM | 1 | 3 | T1 | Y | 购买物品 |
| 1015 | CM_USERGETDETAILITEM | 1 | 3 | T1 | - | ???????????????????????? |
| 1016 | CM_DROPGOLD | 1 | 1 | T1 | Y | 丢掉金币 |
| 1017 | CM_1017 | 0 | 1 | T1 | - |  |
| 1018 | CM_LOGINNOTICEOK | 1 | 1 | T1 | Y | 进入游戏窗口确定按钮 |
| 1019 | CM_GROUPMODE | 2 | 1 | T1 | Y | 编组模式 |
| 1020 | CM_CREATEGROUP | 1 | 2 | T1 | Y | 创建编组 |
| 1021 | CM_ADDGROUPMEMBER | 1 | 2 | T1 | Y | 添加编组成员 |
| 1022 | CM_DELGROUPMEMBER | 1 | 2 | T1 | Y | 删除编组成员 |
| 1023 | CM_USERREPAIRITEM | 1 | 2 | T1 | Y | 修理物品 |
| 1024 | CM_MERCHANTQUERYREPAIRCOST | 1 | 2 | T1 | - | 查询修理价格 |
| 1025 | CM_DEALTRY | 1 | 2 | T1 | Y | 交易开始 |
| 1026 | CM_DEALADDITEM | 1 | 2 | T1 | Y | 交易添加物品 |
| 1027 | CM_DEALDELITEM | 1 | 2 | T1 | Y | 交易删除物品 |
| 1028 | CM_DEALCANCEL | 1 | 1 | T1 | Y | 交易取消 |
| 1029 | CM_DEALCHGGOLD | 1 | 1 | T1 | Y | 交易改变金币 |
| 1030 | CM_DEALEND | 1 | 1 | T1 | Y | 交易完毕 |
| 1031 | CM_USERSTORAGEITEM | 1 | 2 | T1 | Y | 用户存储物品 |
| 1032 | CM_USERTAKEBACKSTORAGEITEM | 1 | 2 | T1 | Y | 从仓库取回物品 |
| 1033 | CM_WANTMINIMAP | 1 | 2 | T1 | Y |  |
| 1034 | CM_USERMAKEDRUGITEM | 1 | 2 | T1 | Y | 制作毒药物品 |
| 1035 | CM_OPENGUILDDLG | 1 | 1 | T1 | Y | 打开行会窗口 |
| 1036 | CM_GUILDHOME | 1 | 2 | T1 | Y | 行会主页 |
| 1037 | CM_GUILDMEMBERLIST | 1 | 1 | T1 | Y | 行会成员列表 |
| 1038 | CM_GUILDADDMEMBER | 1 | 2 | T1 | Y | 添加行会成员 |
| 1039 | CM_GUILDDELMEMBER | 1 | 2 | T1 | Y | 删除行会成员 |
| 1040 | CM_GUILDUPDATENOTICE | 1 | 2 | T1 | Y | 更新行会信息 |
| 1041 | CM_GUILDUPDATERANKINFO | 1 | 2 | T1 | Y | 更新行会等级/排列信息???? |
| 1042 | CM_1042 | 0 | 1 | T1 | - |  |
| 1043 | CM_ADJUST_BONUS | 1 | 2 | T1 | - |  |
| 1044 | CM_GUILDALLY | 0 | 1 | T1 | Y | 行会结盟 |
| 1045 | CM_GUILDBREAKALLY | 0 | 1 | T1 | Y | 行会解盟 |
| 10430 | CM_SPEEDHACKUSER | 1 | 0 | T3 | - | ?? |
| 2000 | CM_PROTOCOL | 0 | 1 | T1 | Y |  |
| 2001 | CM_IDPASSWORD | 1 | 1 | T1 | Y | 发送用户名/密码 |
| 2002 | CM_ADDNEWUSER | 1 | 1 | T1 | Y |  |
| 2003 | CM_CHANGEPASSWORD | 1 | 1 | T1 | - | 更改密码 |
| 2004 | CM_UPDATEUSER | 1 | 1 | T1 | - |  |
| 3005 | CM_THROW | 2 | 0 | T2 | - | 投掷 |
| 3010 | CM_TURN | 4 | 6 | T1 | Y | 转 |
| 3011 | CM_WALK | 6 | 8 | T1 | Y | 走路 |
| 3012 | CM_SITDOWN | 3 | 5 | T1 | Y | 挖 |
| 3013 | CM_RUN | 5 | 11 | T1 | Y | 跑 |
| 3014 | CM_HIT | 5 | 13 | T1 | Y | 砍 |
| 3015 | CM_HEAVYHIT | 2 | 9 | T1 | Y |  |
| 3016 | CM_BIGHIT | 1 | 8 | T1 | Y |  |
| 3017 | CM_SPELL | 4 | 6 | T1 | Y | 魔法 |
| 3018 | CM_POWERHIT | 2 | 8 | T1 | Y | 攻杀 |
| 3019 | CM_LONGHIT | 3 | 11 | T1 | Y | 刺杀 |
| 3024 | CM_WIDEHIT | 3 | 8 | T1 | Y | 半月 |
| 3025 | CM_FIREHIT | 2 | 8 | T1 | Y |  |
| 3030 | CM_SAY | 1 | 4 | T1 | Y | 说话 |
| 65074 | CM_SERVERREGINFO | 0 | 0 | T3 | - |  |
| 5001 | CM_GETGAMELIST | 0 | 2 | T3 | - |  |
| 5003 | CM_GETBACKPASSWORD | 0 | 1 | T3 | - |  |
| 42 | CM_42HIT | 0 | 2 | T1 | - |  |
| 2001 | CM_PASSWORD | 1 | 2 | T1 | - |  |
| 2002 | CM_CHGPASSWORD | 0 | 1 | T1 | - |  |
| 2004 | CM_SETPASSWORD | 0 | 1 | T1 | - |  |
| 3035 | CM_HORSERUN | 4 | 3 | T1 | - | ------------未知消息码 |
| 3036 | CM_CRSHIT | 3 | 4 | T1 | - | ------------未知消息码 |
| 3037 | CM_3037 | 1 | 0 | T2 | - |  |
| 3038 | CM_TWINHIT | 2 | 4 | T1 | - |  |
| 3040 | CM_QUERYUSERSET | 0 | 1 | T1 | - |  |

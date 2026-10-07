# 遗留客户端实测分析

> 对象：`$WS/mir2standard/GameOfMir/Client`（Delphi 6，**只读**）
> 方法：全部数字由本文 §8 的命令实测得出，可复现。
> ⚠️ **源码是 GBK**，直接 grep 中文是静默 0 命中，必须先 `iconv -f GBK -t UTF-8`。
>
> 资源格式规格（WIL/WIX/map/lig）已移到 [assets.md §3](./assets.md)，本文不再重复。

---

## 1. 规模

| 项 | 实测 |
|---|---|
| `.pas` | 38 个，**87,036 行** |
| `.inc` | 2 个，44 行 |
| **合计** | **87,080 行** |
| 类声明 | 145 个 |
| 单文件最大 | `DirectX.pas` 15,995 行（DelphiX） |

### 行数分布（前 20）

| 文件 | 行数 | 性质 |
|---|---|---|
| `DirectX.pas` | 15,995 | DelphiX，**删** |
| `DXRender.pas` | 8,081 | 软件混合 + JIT 代码生成，**删**（换成 ~200 行 shader） |
| `DXDraws.pas` | 7,075 | DelphiX，**删** |
| `FState.pas` | 6,831 | **全部 UI**（28 窗口 + 262 控件） |
| `ClMain.pas` | 6,500 | 主逻辑 + 消息分派 |
| `DShow.pas` | 5,906 | DirectShow，**删**（开场视频不做） |
| `Actor.pas` | 3,944 | 角色渲染/动作 |
| `DIB.pas` | 3,239 | 8 位 DIB，**留一半**（WIL 解码要用） |
| `AxeMon.pas` | 2,846 | 怪物动画（34 个类） |
| `Grobal2.pas` | 2,703 | 协议常量与结构 |
| `DXSounds.pas` | 2,599 | DirectSound，**删** |
| `PlayScn.pas` | 2,366 | 主场景/地图渲染/光照 |
| `JSocket.pas` | 2,162 | 异步 socket，**删**（换标准库） |
| `HUtil32.pas` | 2,102 | 工具（编码/字符串） |
| `IntroScn.pas` | 1,565 | 开场/登录/选角场景 |
| `magiceff.pas` | 1,509 | 魔法特效（15 个类） |
| `DXTexImg.pas` | 1,304 | 纹理，**删** |
| `cliUtil.pas` | 1,214 | 聊天/文本/输入 |
| `DWinCtl.pas` | 1,124 | **自绘控件框架**（要重写） |
| `MShare.pas` | 1,117 | 共享状态 |

---

## 2. 可删部分：46,245 行（53%）

现代库直接替代，**一段都不用移植**：

| 文件 | 行数 | 替代物 |
|---|---|---|
| `DirectX.pas` | 15,995 | SDL3 |
| `DXRender.pas` | 8,081 | 调色板查表 shader |
| `DXDraws.pas` | 7,075 | SDL3 纹理 |
| `DShow.pas` | 5,906 | **砍掉**（开场 MPEG 视频） |
| `DXSounds.pas` | 2,599 | SDL3 audio |
| `JSocket.pas` | 2,162 | 标准库 `net` |
| `DXTexImg.pas` | 1,304 | SDL3 纹理 |
| `DXClass.pas` | 697 | — |
| `Mpeg.pas` | 114 | — |
| `DXConsts.pas` | 103 | — |
| `DelphiXcfg.inc` + `DXRender.inc` | 44 | — |
| `DIB.pas` 的一半 | ~1,639 | 保留 ~1,600 行供 WIL 解码 |
| `Wave.pas` 的多数 | ~526 | 自写 WAV 解析约 200 行 |
| | **≈ 46,245** | |

**净需重写 ≈ 40,835 行**。但这不是新客户端的行数——数据驱动会让它更少。
参考拆解见 [plan.md §4.2](./plan.md)。

---

## 3. 协议面

| 指标 | 实测 |
|---|---|
| `Grobal2.pas` 定义的 `SM_*` 常量 | **216** |
| 客户端其他文件引用到的（去重） | 206 |
| 定义了但客户端未使用 | 10 |
| `ClMain.pas` 中 `SM_xxx:` 形式的分派标签 | **220** |

**原版报文不复刻**（D-05）。本清单的用途是**界定功能范围**：
216 个里哪些在 1.76 玩法中可达。这正是 M0 任务池第一项「消息清单」要产出的东西。

---

## 4. UI 面（客户端工作量大头）

### 4.1 控件实例（实测："`变量名: 类型`" 形式的字段声明）

| 类型 | 实例数 |
|---|---|
| `TDButton` | 208 |
| `TDWindow` | **28** |
| `TDControl`（基类实例） | 23 |
| `TDGrid` | 3 |
| **合计** | **262** |

**全部集中在 `FState.pas`**（6,831 行 / 364 个方法，其中 318 个是 UI 方法）。

`DWinCtl.pas` 是自绘控件框架：`TDControl`(TCustomControl) → `TDButton` → `TDWindow`，
另有 `TDGrid` 与 `TDWinManager`。

### 4.2 28 个窗口（这是可靠的功能清单）

比关键词搜索可靠得多——窗口名是英文标识符：

| 变量名 | 功能 | 变量名 | 功能 |
|---|---|---|---|
| `DLogIn` | 登录 | `DGuildDlg` | 行会 |
| `DNewAccount` | 新账号 | `DGuildEditNotice` | 行会公告编辑 |
| `DSelServerDlg` | 选服 | `DGroupDlg` | 组队 |
| `DSelectChr` | 选角 | `DDealDlg` | 交易 |
| `DCreateChr` | 建角 | `DDealRemoteDlg` | 远程交易 |
| `DChgPw` / `DChgGamePwd` | 改密码 / 改游戏密码 | `DMerchantDlg` | NPC 商店/对话 |
| `DMsgDlg` | 通用消息框 | `DSellDlg` | 出售 |
| `DBackground` / `DBottom` | 背景层 / 底部主控条 | `DStateWin` / `DUserState1` | 自身状态 / 他人状态 |
| `DItemBag` | 背包 | `DAdjustAbility` | 属性调整 |
| `DMenuDlg` | 菜单 | `DFriendDlg` | 好友 |
| `DKeySelDlg` | 按键设置 | `DMailListDlg` | **邮件列表** |
| `DConfigDlg` | 配置 | `DBlockListDlg` | **黑名单** |
| `DMemo` | **备忘录** | | |

### 4.3 ⚠️ 方法论更正：关键词命中法不可靠

早期用「中文关键词命中数」判断功能面（背包 11 / 行会 72 / 交易 14 / 装备 9，
而"邮件、任务、拍卖、摆摊、结婚、师徒、天气、输入法"全为 0），据此得出"不做邮件"。

**这个结论是错的**：窗口名是英文标识符（`DMailListDlg`），中文关键词自然命中 0。
**功能面必须以窗口清单（§4.2）+ 消息清单为准。**

待重新评估：邮件、好友、黑名单、备忘录。
任务 / 拍卖 / 摆摊 / 结婚 / 师徒 / 天气 仍需用窗口清单与消息清单双重确认后再下结论。

---

## 5. 动画面

| 文件 | 类数 |
|---|---|
| `AxeMon.pas` | 34 |
| `magiceff.pas` | 15 |
| `HerbActor.pas` | 10 |
| `Actor.pas` | 9 |
| **合计** | **~70 个手写动画类** |

**不要照搬这个结构。** 新版 = 「一个状态机引擎 + 动作数据表」，
把 70 个类压成数据 + 5k 行代码。

---

## 6. 资源目录关键常量

路径常量在 `Share.pas:42-74`：

```
Data/Prguse.wil, Prguse2.wil, Prguse3.wil      // UI 图
Data/ChrSel.wil, mmap.wil                       // 选角 / 小地图
Data/Tiles.wil, SmTiles.wil                     // 地形
Data/Hum.wil, Hair.wil, Weapon.wil, Npc.wil     // 角色
Data/Magic.wil, Magic2.wil, MagIcon.wil, HumEffect.wil
Data/Items.wil, StateItem.wil, DnItems.wil
Data/Objects.wil, Objects%d.wil
Data/Mon%d.wil, Dragon.wil, Effect.wil
Graphics/FrmMain/Main.wil, Main2.wil, Main3.wil
Graphics/Monster/%d.wil
```

其他场景常量：`PlayScn.pas:14-27`（`MAPSURFACEWIDTH=800`、`SOFFX`、`AAX=16`、
`FLASHBASE=410`、`MAXLIGHT=5`）、光照掩膜 `PlayScn.pas:37-120`。

⚠️ **注意 `Share.pas:42-45` 有两套编译分支**（`MAINIMAGEFILE` 指向 `Graphics\FrmMain\`
或 `Data\Prguse*`），起步时要确认以哪套为准。

### ⚠️ 6.1 分辨率疑点（待实测）

`PlayScn.pas:14-15` 是 `MAPSURFACEWIDTH=800 / MAPSURFACEHEIGHT=445`，
但**移动与攻击是按格判定的**，而 `LOGICALMAPUNIT=40`（`Grobal2.pas:43`）无法整除
标准 MIR2 图块尺寸（48×32）。这意味着 800×445 很可能是**地图表面的像素尺寸**，
而**逻辑/视口分辨率是 320×240**（标准 1.76 客户端）。

**后果**：若按 800×600 布 UI，可能是全项目最贵的一次返工。
**M1 必须先实测原版截图确认**；工程上要保证逻辑分辨率是**可配置常量**，
不要硬编码（见 [plan.md M1](./plan.md)）。

---

## 7. `data/` 文件名规范化面（mir2go 侧）

| 目录 | 含大写 | 含非 ASCII |
|---|---|---|
| `data/map` | 456 | 0 |
| `data/envir/market_def` | 332 | 24 |
| `data/envir` | 63 | 0 |
| `data/monitems` | 0 | **300** |
| `data/castle` | 3 | 0 |
| `data/seed` | 1 | 0 |
| **合计** | **855** | **324** |

三类性质不同，处理方式见 [decisions.md D-03](./decisions.md)。
其中 `data/monitems/*.txt` **文件名即怪物名**
（`mir2go/internal/entity/monitems.go:61` 是 `tables[monName]`），不要重命名。

---

## 8. 复现命令

```bash
# 行数
cd $WS/mir2standard/GameOfMir/Client && wc -l *.pas *.inc | sort -rn

# GBK → UTF-8（后续 grep 必须走这一步）
mkdir -p /tmp/mir2cli-utf8
for f in *.pas; do iconv -f GBK -t UTF-8 "$f" > /tmp/mir2cli-utf8/"$f"; done

# 协议面
grep -cP '^\s*SM_[A-Z0-9_]+\s*=' Grobal2.pas                   # 216 定义
grep -cP '\bSM_[A-Z0-9_]+\s*:' ClMain.pas                      # 220 分派标签

# UI 面
grep -hP ':\s*TDWindow\b' /tmp/mir2cli-utf8/*.pas | wc -l      # 28 窗口
grep -hP ':\s*TDButton\b' /tmp/mir2cli-utf8/*.pas | wc -l      # 208 按钮
grep -oP '^\s*\K[A-Za-z0-9_]+(?=\s*:\s*TDWindow)' /tmp/mir2cli-utf8/FState.pas

# 动画面
grep -cP '^\s*T[A-Za-z0-9_]+\s*=\s*class' AxeMon.pas           # 34

# 资源文件名规范化面（mir2go 侧）
cd $WS/mir2go && find data -type f | grep -Pc '[A-Z]'     # 855
find data -type f | grep -Pc '[^\x00-\x7F]'                     # 324

# 资产体积与压缩收益
du -sh $WS/mir2go/data/map                                # 242M
gzip -9 -c $WS/mir2go/data/map/3.map | wc -c              # 0.059x
```

---

## 9. 版本差异：`Client/` vs `MirClient/`

两者**不是简单副本**，18 个 `.pas` 不同：

| 文件 | `Client/` | `MirClient/` |
|---|---|---|
| `ClMain.pas` | 6,500 | 7,126 |
| `FState.pas` | 6,831 | 7,091 |
| `PlayScn.pas` | 2,366 | 2,476 |
| `IntroScn.pas` | 1,565 | 1,667 |
| `Actor.pas` | 3,944 | 3,964 |
| `AxeMon.pas` | 2,846 | 2,913 |
| 其余 12 个 | 小数行差 | |

另有 `MirClient/GShare-oldbk.pas` 是 `Client/` 没有的。

**基准版本已定：`MirClient/`（D-10，2026-10-07）** —— 它是**未精简的原始快照**
（中文注释完整、有效代码更多、含 `GShare-oldbk.pas`）。
两版**协议常量表几乎一致**（`CM_` 87/87；`SM_` 仅差 1 条 T3 `SM_PLAYDICE`）
⇒ 本文与 [messages.md](./messages.md) 的结论**无需重做**。

---

## 10. ⚠️ 素材缺失

全盘实测 **零个 `.wil` / `.wix`**，`mir2standard` 只有源码（375 `.pas` + 186 `.dfm` + 10 exe）。
详见 [decisions.md §0](./decisions.md)——这是目前唯一的 P0 阻塞项。

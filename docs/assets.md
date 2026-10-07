# 资产策略与格式规格

> 状态：规格部分**已实测**，容器设计**待定稿**（D-11）。
> 决策见 [decisions.md D-06/D-11](./decisions.md)。

---

## 1. 原则

1. **美术是内容，不是实现**。必须是同一套美术，但**载体可自选**。
2. **转换必须逐像素可回验**。工具要提供 `verify`：解码产物与原始文件逐字节比对。
   没有这条，"体感一致"就没有证据（D-06 的硬要求）。
3. **原始文件是唯一真源**。转换产物建议**脚本生成、不入库**（沿用 `seedgen` 模式）。

## ✅ 2. 素材状态（2026-10-07 更新）

**已到位**：`$WS/mir2c`（2.2 GB，详见 [decisions.md §0.2](./decisions.md)），
容器为 **`.wzl` / `.wzx`**（规格见 §3.2b），另有 777 个 `.wav` 与 770 张 `.map`。
**D-15 的技术阻塞已解除。**

⚠️ 两个待确认项（**不阻塞开发**，但影响验收范围）：

1. **完整度**：252 个 `.wzl` 中 **146 个是 64 字节空壳**（登录器按需下载）；
   `Hair` / `Npc` / `StateItem` / `Dragon` **缺失**。
2. **美术一致性**（C-1 硬约束）：该客户端是**现代版**（有 `Objects25`/`Tiles8`/`Magic8-16`/宠物·龙等），
   需**抽查核心库**（`Prguse`/`Hum`/`Weapon`/`Magic`/`Items`/`Tiles`）是否与原版 1.76 一致。

> 历史：此前全盘搜索零个 `.wil`/`.wix`（`mir2standard` 只有源码没有美术），M1 视觉验收一度被阻塞。
> 地图部分的体积收益已实测（§4），美术部分待转换器落地后实测。

---

## 3. 原始格式规格

### 3.1 `.wil`（图像库）

字段语义来自 `mir2standard/GameOfMir/Client/wmUtil.pas:11-17`：

```
Title        String[40]   // 'WEMADE Entertainment inc.'
ImageCount   integer
ColorCount   integer
PaletteSize  integer
VerFlag      integer      // 老格式无此字段
```

- 头之后紧跟 **256 × 4 字节调色板**（BGRA，`WIL.pas:280-289`）。
- 每张图：`w:i16, h:i16, px:i16, py:i16`（老格式多 4 字节指针占位），
  随后 `w * h` 字节的 **8 位调色板索引**。
- **索引 0 = 透明色**（`WIL.pas:274`）。
- 版本判别：`VerFlag = 0` ⇒ 老格式，头部少 4 字节（`WIL.pas:175-178`）。

**⚠️ 两个必须在第一步核对掉的坑：**

1. **精确偏移不确定**。`String[40]` 占 41 字节，Delphi 记录对齐可能补 3 字节
   ⇒ 新格式头 **57 或 60** 字节、老格式 **53 或 56** 字节，两种都合理。
   选错读出乱码，表现为"图全花了"。
   **可靠核对法**：`.wix` 第一项 = 第 0 张图的 `.wil` 偏移，调色板必然紧邻其前 1024 字节
   ⇒ `palette_start = wix[0] - 1024`。反推头部长度并写成单测，一次定死。
2. **`.wix` 每项 4 字节，不是 8 字节**。`wmUtil.pas:54-57` 定义了
   `TWMIndexInfo = record Position: integer; Size: integer end;`，
   但 `LoadIndex` 只读 `4 * IndexCount` 字节——`Size` 是遗留未用字段。按 8 字节读会全盘错位。

### 3.2 `.wix`（图像索引）

```
Title       String[40]
IndexCount  integer
VerFlag     integer        // 老格式无此字段
IndexCount × int32         // 每项 = 对应图在 .wil 中的偏移
```

### 3.2b `.wzl` / `.wzx`（盛大新格式，**2026-10-07 已实测破解**）

`$WS/mir2c`（1.76 怀旧客户端）用这套容器，**不是 `.wil`**。
权威参考实现：`$WS/Crystal/LibraryEditor/Graphics/WeMadeLibrary.cs`（C#，`_nType==1` 分支），
我方解析与它**逐字段吻合**并已用 Python 实证。

**`.wzx`（索引）** —— 与 `.wix` 同构，头 **48 字节**：

```
Title        String[40]   // 41 字节，补齐到 44
ImageCount   u32          // 位于偏移 44
u32[ImageCount]           // 每项 = 对应图在 .wzl 中的字节偏移（从偏移 48 开始）
```

**`.wzl`（数据）** —— 头 **64 字节**（可忽略，偏移以 `.wzx` 为准），图记录从偏移 64 开始：

```
每条记录（16 字节）：
  u8   typeFlag      // 5 = 16 位 RGB565；其它值 = 8 位调色板索引
  u8[3] 跳过
  i16  width, height
  i16  anchorX, anchorY          // ★ 渲染对齐必须保留
  i32  compSize                  // zlib 压缩后字节数
  u8[compSize]                   // zlib 流
```

解压后为**原始像素**：

| 位深 | 每行字节 | 说明 |
|---|---|---|
| 8 位 | `((w*8+31)>>5)*4` | 调色板索引；**索引 0 = 透明** |
| 16 位 | `((w*16+31)>>5)*4` | RGB565 小端，无需调色板 |

- ⚠️ **行按 4 字节对齐**；数据**自下而上**存储（第一行是图像底行）。
- ⚠️ **图库尾部无追加调色板**（实测所有库 `文件末尾 == 最后一张图末尾`，余 0）。

**调色板 = 经典 MIR2 256 色，硬编码，不在文件里**：
取自 `WeMadeLibrary.cs` 的 `_palette` 数组（已人工目视验证：用它渲染 `Prguse`/`Hum` 正常）。
`data/npal.idx`（48 头 + 320×1024）**与本客户端美术无关**——320 个候选逐一比对，与经典调色板**零匹配**；
用它渲染，颜色全错。

**位深实测**：

| 8 位（需调色板） | 16 位（免调色板） |
|---|---|
| `Prguse` `Hum` `Tiles` `SmTiles` `ChrSel` `mmap` `Effect` `Magic` `Mon1` … | **`Items`** |

### 3.3 `.map`（地图）

**权威实现已存在**：`$WS/mir2go/internal/world/map.go`（有测试）。新项目照抄语义。

| 项 | 值 |
|---|---|
| 文件头 | **52 字节**：`Width:u16, Height:u16, Title:String[16], UpdateDate:double, Reserved[23]` |
| 每格 | **12 字节**：`BkImg:u16, MidImg:u16, FrImg:u16, DoorIndex:u8, DoorOffset:u8, AniFrame:u8, AniTick:u8, Area:u8, Light:u8` |
| 排列 | **列主序**（`x` 在外层循环） |
| 阻挡位 | `BkImg` / `FrImg` 的 `0x8000` |
| 门位 | `DoorIndex` 的 `0x80`，低 7 位是门组号 |
| 变体判别 | 8 种（经典 v0 + 各家改版/加密），见 `map.go:197-231` `DetectFormat` |

#### 3.3a 字段语义（2026-10-07 由官方客户端源码定案）

**`.map` 不含像素**，只是一张**格子索引表**：每格说"三层各贴哪张图 + 能否通行 + 门/动画/光照"。
像素在 `.wzl` 里。权威出处：`MirClient/PlayScn.pas:560/586/1143` + `MShare.pas:786 GetObjs`。

| 字段 | 图层 | 图库 | 依据 |
|---|---|---|---|
| `wBkImg` | 地表 | `Tiles.wzl` | `PlayScn.pas:564` `g_WTilesImages` |
| `wMidImg` | 中间 | `SmTiles.wzl` | `PlayScn.pas:589` `g_WSmTilesImages` |
| `wFrImg` | 前景 | `Objects<N>.wzl`，**N = `btArea`** | `PlayScn.pas:1172` `GetObjs(wunit, fridx)` |

三条必须照做的小语义：

1. **图号是 1 基**：取图时 `图号 - 1`；`0` 表示该层不画。
2. **地表层只在 `(i mod 2 = 0) and (j mod 2 = 0)` 的格上绘制**（`PlayScn.pas:561`）——
   地块是 48×32，横向覆盖 2 个逻辑格。
3. **通行** = `(BkImg and $8000) + (FrImg and $8000) = 0`（`MapUnit.pas:320`）；
   另有只看前景层的判定（`MapUnit.pas:337`）。
4. 前景动画：`btAniFrame` 的 `$80` 位 = **混合标志**（语义见 §3.3a-3，是**滤色不是 alpha**），
   低 7 位 = 帧数；`btAniTick` 是节拍；`btDoorOffset` 的 `$80` 位 = **有位移**（开门），低 7 位 = 偏移量。

⚠️ **已推翻的旧疑问**：曾疑"旧客户端只用两层、`mir2go` 却归三层"。
实测**确实是三层**——`wMidImg` 存在且走 `SmTiles`。

⚠️ **`UpdateDate` 不可信**：实测样本（`0.map`）该 8 字节是垃圾位模式（形似写图工具误写 f32），
**不要依赖它**。`Reserved[23]` 同理，未见使用。

⚠️ **`TMapInfo` 是 `packed record`**（`MapUnit.pas:39`）⇒ 每格**恰好 12 字节**，无对齐填充。

#### 3.3a-2 图块落点：三层**不是**同一种对齐（2026-10-07 由两份实现交叉验证）

逻辑格 = **48×32 px**（`Grobal2.pas:45` `UNITX=48`/`UNITY=32`，Crystal `GameScene.cs:10292` 同样是 48/32）。
格 `(x, y)` 的左上角在屏幕上的位置 = `(x*48, y*32)`——**三层共用同一套格子坐标**，
差别只在"图块落在格子的哪个角"：

| 层 | 图块尺寸（实测） | 落点 |
|---|---|---|
| 地表 `BkImg` → `Tiles` | **96×64** | 左上角对齐**格的顶边**（只在 `x`、`y` 皆偶数时画） |
| 中间 `MidImg` → `SmTiles` | **48×32** | 左上角对齐**格的底边**（即**低一格**）⚠️ |
| 前景 `FrImg` → `Objects<N>` | 48×32 与**更高的**（48×86/87/93…） | **平图块**（48×32 / 96×64 且无动画）⇒ 格顶；**其余 ⇒ 底边对齐格的底边** |

**推导方法**：以官方 actor 的落点公式为参照系。`PlayScn.pas:1887`：

```pascal
dy := (a.m_nRy - Top - 1) * UNITY + m_nDefYY + a.m_nPy;   // m_nDefYY = defy = -UNITY*2
```

取 `py = 0` 得"格原点" `Y0 = 32*ry - 96`（代 `Top=0`）；又因 actor 的脚底正是 `Y0 + py + H`
且锚点满足 `H + py = 32`，故 **格底边 = `Y0 + UNITY`**。据此换算三层
（地表/中间先画进 `m_MapSurface`，再以源偏移 `(UNITX*3, UNITY*2)` 贴到 `m_ObjSurface`）：

- 地表 `nY := -UNITY*2`（`:555`）⇒ 落点 `= Y0` ⇒ **格顶**
- 中间 `nY := -UNITY`（`:581`，比地表少一个 `-UNITY`）⇒ 落点 `= Y0 + UNITY` ⇒ **格底**
- 前景 `mmm := m + UNITY - Height`（`:1166`）⇒ 底边 `= Y0 + UNITY` ⇒ **格底**

⇒ **官方里地表与另两层差整整一格**。`MapUnit.pas.LoadMapArr` 装载时对 `wMidImg`
**没有任何预移位补偿**（同文件 `UpdateMapSeg` 是空函数），故这是渲染器的真实行为，
不是数据约定。对连续水面这种一格位移不可见；只有在水/覆盖物的**边界**上才看得出来。

⚠️ **两份实现的差异**：Crystal（`GameScene.cs:10707/10719/10755`）把中间层改成了**格顶**，
并把"平的"前景塞进 floor 趟——属它的"修正"。本项目按 **C-1「体感与原版一致」**
**跟随官方**（中间层格底、前景"平的"也走格底判定）。

#### 3.3a-3 **Alpha 物件**（`btAniFrame & $80`）走**另一条**定位规则

官方第二个物件趟（`PlayScn.pas:1196` 起）里两个分支是分开的：

```pascal
if not blend then begin                       // 普通物件
   DSurface := GetObjs (wunit, fridx);
   mmm := m + UNITY - DSurface.Height;        // 底边对齐格底
end else begin                                // ★ Alpha 物件
   DSurface := GetObjsEx (wunit, fridx, ax, ay);   // 顺带取该图的锚点
   mmm := m + ay - 68;                        //  用锚点定位（68 是原版魔数）
   DrawBlend (m_ObjSurface, n+ax-2, mmm, DSurface, 1);  // 半透明混色
end;
```

- 判定：`btAniFrame and $80 <> 0`（低 7 位仍是动画帧数）。
- 定位：Y = **格原点 + 锚点 y − 68**；X = **格原点 + 锚点 x − 2**。
- 混合：`DrawBlend(..., 1)` —— ⚠️ **不是 alpha 混合，是滤色（SCREEN）**，见下。
- 实测占比很小但**可见**：`0.map` 里 28,281 个前景格中仅 **45 格**（0.16%），
  且**全部指向同一个图号 2723**（一块 100×100 的光效 —— 不做混合时会渲染成
  **不透明黑方块**，位置也错，这就是它看起来"错位"的原因）。

##### 混合模式的**真身**：两张查表，都不是 alpha（2026-10-07 定案）

`cliUtil.pas:957` 决定用哪张表：`blendmode = 0` 取 `Color256Mix`，**否则取 `Color256Anti`**；
而物件路径写死 `DrawBlend(..., 1)` ⇒ 走 **`Color256Anti`**。两张表的生成代码
（`BuildMix` / `BuildAnti`）化简后是：

```text
BuildMix  （blendmode 0）: out = (src + dst) / 2               ← 50% 平均
BuildAnti （blendmode 1）: out = src + (255-src)/255 * dst     ← SCREEN（滤色）
```

**踩过的坑**：把 `BuildAnti` 当 50% 透明来画。光源贴图 `#2723` 的亮度分布是

| 亮度 | 像素数 | 占比 |
|---|---:|---:|
| 0–31（近黑外圈） | 5039 | **56%** |
| 32–63 | 1790 | 20% |
| 64–127 | 1675 | 19% |
| 128–255（中心） | 424 | 5% |

SCREEN 下，占 56% 的近黑外圈满足 `out = src + dst·(1−src/255) ≈ dst` —— **等于透明**，
只有中心把地面**提亮**，这才是"一盏灯"。50% alpha 下同一片像素把地面**压暗一半**，
100×100 铺过去就是**一坨半透明黑斑**（用户实测截图即此现象）。

**SDL3 侧的等价实现**：SDL 没有 SCREEN 模式，但
`SDL_BLENDMODE_BLEND_PREMULTIPLIED` 的公式是 `dstRGBA = srcRGBA + dstRGBA·(1−srcA)`
（`SDL_blendmode.h`），与官方同形 —— 只要把源像素的 **alpha 换成它的亮度**，
就得到 `out = src + dst·(1−brightness/255)`。
⇒ [`client/core/src/blend.rs`](../client/core/src/blend.rs) 的 `screen_source()` +
`BlendMode` 走底层常量 `SDL_BLENDMODE_BLEND_PREMULTIPLIED`
（`sdl3::render::BlendMode` 没暴露这一个，见 `client/app/Cargo.toml` 的 `sdl3-sys`）。

⚠️ **已知偏差**：官方对 R/G/B **各自**算 `1−src_c/255`，SDL 只有**一个** alpha 因子，
故取三通道均值。纯灰像素误差为 0；实测该贴图最大的 `(66,49,16)` 差 **16/255（6%）**，
均值远小于此（有单测守着，见 `blend.rs` 的 `approximates_official_screen_*`）。
定点混合管线无法表达逐通道 SCREEN，此为可接受的近似。

⚠️ Crystal 另用 `BackImage & 0x1FFFFFFF`（29 位，含 `BackIndex` 选库），是**新格式**；
1.76 用 16 位 + `0x7FFF` 掩码 + `btArea` 选 `Objects<N>`。**以 1.76 为准**。

##### 前景动画的帧推进（2026-10-07 定案）

官方 `PlayScn.pas:1150-1153`（前景两趟各有一份，逻辑相同）：

```text
fridx := fridx + (aniCount mod (ani + ani*anitick)) div (1 + anitick)
```

- `ani` = `btAniFrame and $7F`（帧数）、`anitick` = `btAniTick`；
  **每帧持续 `(1+anitick)` 个 tick**，一整轮 `ani*(1+anitick)`。
- `aniCount` 是 `PlayScn.pas:963` 的全局计数器：**每 50 ms 加一**
  （`GetTickCount` 比较，与渲染帧率**无关**）⇒ 实现必须按"毫秒 / 50"驱动，
  **不能**每渲染帧 +1，否则动画速度随机器性能漂移。
- 开门偏移（同一段代码）：`if (btDoorOffset and $80) > 0 and (btDoorIndex and $7F) > 0 then
  fridx := fridx + (btDoorOffset and $7F)` —— 加在 `-1` 转 0 基**之前**。
- ⚠️ **动画只作用于前景层**：官方两趟都只读 `wFrImg`，地表/中间层不参与。

实测那盏灯（格 `(344,335)`：`fr=2724`、`btAniFrame=0x8A`（10 帧 + blend）、`btAniTick=0`）
⇒ 每 50 ms 换一帧，`#2723..#2732` 共 10 个 100×100 帧，**0.5 秒一轮**。

#### 3.3b 实测：地图**不止一种布局**（2026-10-07，全量 605 + 771 张扫描）

上面那张表只描述**经典布局**。全量扫描后实际存在三种：

| 布局 | 判据 | 客户端集 | 服务端集 | 说明 |
|---|---|---|---|---|
| **经典** | `52 + W·H·12` | **641** | **592** | 本文档描述的那一种 |
| **扩展（36 B/格）** | `52 + W·H·36` | **39**（26.7 MB） | **7** | 前 12 字节语义与经典**相同**（实测 `ygfx1` 的 `BkImg/MidImg/FrImg/DoorIndex/…` 位置一致），后 24 字节未知（样本里多为零 + 一个低位计数） |
| **`EM*` / `T2*` 族** | 定长 **20,692 B**，头声明 30×35 | **90** | 4 | 体长 20,640 与 `W·H·12`（12,600）不符；**周期检测得 24 B**（匹配率 95%），但 24 不能整除 30×35 的格数。**布局未定** |

- 该族 `EM001.map`/`EM300.map`/`T118.map` 等**存在内容完全相同的重复文件**（`sha256` 一致）。
- **对容器无影响**：M2PK 只存原始字节、不理解语义（§5）。但**地图解析器**（M1）必须处理：
  先支持经典 12 B，36 B 变体可只取前 12 字节，`EM*` 族需进一步分析。

#### 3.3c 两套地图**不是同一套**（→ **D-22：以客户端集为准**）

原始地图有两份，来源不同：

| 来源 | 张数 | 体积 | 用途 |
|---|---|---|---|
| `$WS/mir2c/map` | **770** `.map` + 1 `.mex` | **253.7 MB** | 客户端自带（§2 记的就是这一份） |
| `$WS/mir2go/data/map` | 605 | 242 MB | 参照服务端自带 |

- 规范化名字后可配 **557** 对：**552 对逐字节相同，5 对内容不同**：
  `4.map`（**8.6 MB vs 2.9 MB，客户端是 36 B/格版、服务端是 12 B/格版**）、
  `1.map` / `5.map` / `D2071.map` / `0115.map`（同尺寸但内容不同，差异 0.006%–0.18%，落在保留区与少量格上）。
- 客户端独有 **208** 个、服务端独有 **42** 个。
- `bsr03.mex` 是唯一非 `.map` 文件（436 KB，魔数不认识），**尚未识别**。

**已裁决（D-22，2026-10-07）**：**唯一真源 = `$WS/mir2c/map`**。
客户端集来自 **SDO 官方分发、属经典版**，与 C-1 同源；
`mir2go` 那套**不采用**，对拍时遇到上述 5 张差异**以本项目为准**。

### 3.4 界面素材（`Prguse` / `ChrSel`）—— 以登录界面为例

界面素材的图号是**原版写死的常量**（不像 actor 图号要算），所以只能照抄那张表：

| 用途 | 素材 | 位置 | 出处 |
|---|---|---|---|
| 登录对话框 | `Prguse[60]`（296×254） | **屏幕居中** | `FState.pas:771-775` |
| [提交] | `Prguse[62]`（76×33） | 对话框 +(169,163) | `FState.pas:790-792` |
| [新用户] | `Prguse[61]`（100×32） | 对话框 +(25,207) | `FState.pas:786-789` |
| [修改密码] | `Prguse[53]`（128×33） | 对话框 +(130,207) | `FState.pas:793-795` |
| [X] | `Prguse[64]`（16×23） | 对话框 +(252,28) | `FState.pas:796-798` |
| 用户名/密码框 | **自绘**（原版是原生 `TEdit`，素材里没这张图） | 对话框 +(98,85) / +(98,117)，112×16 | `IntroScn.pas:269,283,552-565` |
| 全屏背景 | `ChrSel[22]`（800×600，门口石头） | 屏幕居中 | `IntroScn.pas:886-889` |
| 开门动画 | `ChrSel[23..32]`，10 帧 × 300ms | 屏幕居中 | `IntroScn.pas:894-914` |
| 报错弹窗 | `Prguse[360]`（452×179）+ `[363]`（80×34） | 屏幕居中 | `FState.pas:750-760` |

⚠️ 输入框坐标原版写的是**相对屏幕中心**（`Left := cx-50; Top := cy-42 / cy-10`）。
换算成"对话框内偏移"是 `(98,85)` / `(98,117)`，**800×600 与 1024×768 算出来一样**；
而且对话框图**自带的凹槽正好在这两处**（无头合成出来一看就对上）—— 素材本身印证了这组偏移。
版式实现在 `client/core/src/login_ui.rs`（app 与 e2e 共用一份）。

⚠️ `ChrSel[23]` 在本套素材里是**空壳**（`.wzx` 索引里有、`.wzl` 里没有数据）⇒
开门动画实际从 24 起播（`login.rs` 里取不到就跳过该帧）。

### 3.4b 其他

| 格式 | 规格 |
|---|---|
| `lig*.dat` | 6 档光照掩膜（`PlayScn.pas:28-35`），索引色小图；掩膜矩阵在 `PlayScn.pas:37-120`（`LightMask0..5`） |
| `.wav` | 音效，标准格式，自解析 |
| `Music/%d.mp3` | 按地图编号取音乐（`SoundUtil.pas:219`） |

### 3.5 actor 图号（人物 / 怪物）—— **2026-10-07 提取并落地**

`Object` 的图不是一个"图号"字段，而是由**方向 + 动作 + 帧**算出来的。原版公式在这里定案
（实现见 `client/core/src/actor.rs`，出处行号都注在那边）：

```text
人物（Hum.wzl / Weapon.wzl，0 基）
  图号 = 600 * 部位 + HA.<动作>.start + dir * (frame + skip) + 帧序
怪物（Mon<Appr/10 + 1>.wzl，0 基）
  图号 = GetOffset(Appr) + MA.<动作>.start + dir * (frame + skip) + 帧序
```

| 事 | 出处 |
|---|---|
| 人物每块 600 张（= 24 个着装块，`Hum.wzl` 共 14400 张） | `Actor.pas:14` `HUMANFRAME`；`ClMain.pas:6280-6293` |
| 人物 14 个动作段的 (start/frame/skip/ftime) | `Actor.pas:75-91` 的 `HA` 表 |
| 怪物品种（`RaceImg`）→ 动作表 | `Actor.pas:848-954` `GetRaceByPM`（**参数名却叫 `Race`**，喂的是 `RACEfeature` 低字节） |
| 怪物图片块起点（`Appr` → 块偏移） | `Actor.pas:1003+` `GetOffset`（块大小按 `Appr/10` 分档：280/230/360/430/440…） |
| 怪物容器（`Appr/10` → `Mon<N>`） | `Actor.pas:958-1000` `aGetMonImg` + `MShare.pas:832-857` |
| 落点：**精灵左上角 = 格子左上角 + 图自带锚点** | `PlayScn.pas:1236` + `Actor.pas` 的 `dx + m_nPx, dy + m_nPy` |

⚠️ 三处最容易做错的：`dir` 是原版 0..7（我们的协议枚举是**原版+1**）；`frame + skip` 才是
每个方向的步长（`skip` 是"没有有效图"的保留格）；怪物表的"品种"其实是 **`RaceImg`**，
不是服务端那个只用于 AI 的 `Race`。

表是**生成**的、不是手抄的：`client/core/tools/gen_actor_tables.py` 直接解析
`Actor.pas`（26 张怪物表 + 映射 + `GetOffset`），输出贴进 `actor.rs` 的生成段。
重跑方式见脚本头部；源在 `mir2standard/GameOfMir/Client/`（仓库外）。

**本套素材的边界**（§2 已记）：`Hum.wzl`/`Weapon.wzl`/`Mon1..34.wzl` 齐全；
`Hair` **没有这个文件**（`hair_ck.wzl` 只有 64 字节的头、索引却是 5328 条）、
`Npc` / `Dragon` 缺失 ⇒ 头发层、NPC、龙**画不出来**，实现里**降级成标记**而不是猜。

---

## 4. 资产体量与压缩收益（实测）

### 4.1 地图（2026-10-07 用真实容器实测，取代早先的 gzip 估算）

源目录 `$WS/mir2c/map`（客户端集，见 §3.3c）：**770 张 / 253.73 MB**。

| 编码器 / 参数 | 总量 | 比率 | 打包 CPU 时间 |
|---|---:|---:|---:|
| gzip -9（早先估算） | ~17 MB | ~0.07 | — |
| zstd -19 | 9.62 MB | 0.0376 | — |
| xz -9 | 9.07 MB | 0.0357 | 30 s |
| **brotli q11（已采用）** | **8.81 MB** | **0.0347** | 260 s |
| ~~Go 版 zstd（klauspost）~~ | ~~14.46 MB~~ | ~~0.0570~~ | 见 §5.4 |

**结论**：地图不压缩是纯粹浪费，**28.8 倍体积差**；`brotli q11` 最优。
实测容器：**253.73 MB → 8.83 MB（0.0347，−96.5%）**，打包 29 s、逐字节回验 0.4 s。

### 4.2 其他

| 资产 | 原始 | 现状 | 处置 |
|---|---|---|---|
| 美术 `.wzl`（106 库 / 252 文件） | 1682.9 MB | 已 zlib（0.22–0.26） | **直读**（D-11） |
| 服务端配置（`mir2go/data` 其余） | ~7 MB | — | 直读 |
| 音频 `.wav`（779 个） | 199.3 MB | 未压缩 | 先直读（D-11） |

旁证：`mir2go` 把整个 `data/`（含 242 MB 地图）入库后，`git size-pack` 只有 **20.84 MiB**。

---

## 5. M2PK v1（地图容器，**已实现**）

> **范围只管地图**：美术 `.wzl` **直读**、音频直读，都不走 M2PK（D-11）。
> 实现 = [`internal/m2pk`](../internal/m2pk)（库）+ [`tools/m2pk`](../tools/m2pk)（CLI + `build.sh`）。

**目标**：逐字节可回验、随机访问、确定性、体积最小。

### 5.1 格式

```
Header（32 B，小端）
  0   magic      [4]  "M2PK"
  4   version    u16  1
  6   kind       u8   1 = 地图
  7   codec      u8   0 = brotli
  8   count      u32  块数
  12  namesOff   u32  名字池偏移（相对文件头）
  16  namesLen   u32  名字池字节数
  20  dataOff    u32  数据区起始偏移
  24  reserved   [8]

Entry（24 B × count，紧随 Header，按名字升序 ⇒ 读侧可二分）
  0   nameOff    u32  相对 namesOff
  4   nameLen    u8
  5   reserved   [3]
  8   offset     u64  块偏移（相对文件头，8 字节对齐）
  16  rawSize    u32  解压后字节数（= 源文件字节数）
  20  compSize   u32  压缩后字节数

名字池   各块名字（去扩展名 + 小写）顺序拼接，无分隔符
数据区   每块一个独立的 brotli 流，块起始 8 字节对齐
```

### 5.2 设计要点

1. **块内容 = 源文件字节的无损副本**。容器**不理解**块内语义 ⇒ §3.3b 的三种地图布局
   与容器无关；`verify` 因此能做**最强的校验：逐字节**。
2. **一图一块**（不是 solid 大块）。代价实测只有 **6.5%**（7.7 → 7.2 MB @zstd），
   换来**随机访问**——加载一张图不必解开整个容器。
3. **确定性**：块彼此独立 + 顺序按名字升序 ⇒ **并行度不影响输出字节**（有单测守着）。
4. **名字规范化**：去扩展名 + 转小写（D-03 / D-20）；只允许 `[a-z0-9_~-]`；
   重名（**大小写不敏感**）**报错而不是静默覆盖**。
5. **运行期**：按地图为单位解压 + LRU 缓存，不要一次全解。

### 5.3 用法

```bash
tools/m2pk/build.sh                          # 打包 + 逐字节回验（自动探测源目录）
m2pk pack   -src DIR -out FILE [-quality N] [-workers N]
m2pk verify -in FILE -src DIR                # 不一致 ⇒ 非零退出码（D-06）
m2pk info   -in FILE [-list]
```

### 5.4 为什么是 brotli 而不是 zstd（实测，2026-10-07）

源 `$WS/mir2c/map`，770 张 / 253.73 MB：

| 方案 | 总量 | 比率 | 打包 CPU |
|---|---:|---:|---:|
| zstd -19（C libzstd） | 9.62 MB | 0.0376 | — |
| xz -9 | 9.07 MB | 0.0357 | 30 s |
| **brotli q11（采用）** | **8.81 MB** | **0.0347** | 260 s |
| Go 版 zstd（klauspost） | 14.46 MB | 0.0570 | 1 s |

- **纯 Go 的 zstd 拿不到 libzstd 高档位的收益**：同一张 `3.map`，
  CLI `zstd -19` = 385 KB，klauspost `SpeedBestCompression` = **602 KB（差 56%）**，
  且窗口从 8 MiB 提到 128 MiB **毫无变化**。第一版容器（zstd）就是 14.46 MB。
- 换 brotli 后：**253.73 MB → 8.83 MB（−96.5%）**，打包 29 s（8 路）、回验 0.4 s。
- 两端都是**成熟纯实现**，无 cgo / 无 C 依赖：Go `andybalholm/brotli`、Rust `brotli`。
  且实测 **Go 版输出与 C 版逐字节相同**（`3.map` 均为 351,163 B）。
- 打包慢（260 s CPU）是**离线一次性**代价；解码并不慢。

---

## 6. 资产树布局

```
assets/                    # 由 tools/ 生成，**不入库**（.gitignore 已含 /assets/）
├── image/                 # ★ 不放 m2pk —— 直接放原始 .wzl/.wzx（D-11 美术直读）
├── map/maps.m2pk          # ★ 单文件容器，8.83 MB（tools/m2pk/build.sh 产出）
├── audio/                 # 原始 .wav（先直读）
└── font/                  # 点阵字库（bitmap）
```

- **产物不入库**：原始 `.map`（253.7 MB）本身不能入库；容器是它的**确定性**派生物
  （同输入 ⇒ 同字节，有单测守着）⇒ 一条 `build.sh` 重建即可。
- 命名规则遵循 [D-03](./decisions.md)：**全小写、无中文、`/` 分隔**。

---

## 7. 待办

- [x] **素材获取**（D-15，2026-10-07）⇒ `$WS/mir2c`：`.wzl`/`.wzx`，格式已破解（§3.2b）。
- [x] **实测美术库压缩率** —— 结论：WZL **已压缩**（0.22–0.26），故**美术直读**（[D-11](./decisions.md) 重评）。
- [x] **地图容器**（2026-10-07）⇒ [`internal/m2pk`](../internal/m2pk) + [`tools/m2pk`](../tools/m2pk)：
      brotli q11、一图一块；`$WS/mir2c/map` **770 张 253.73 MB → 8.83 MB（0.0347，−96.5%）**；
      `verify` **逐字节**回验通过（D-06 硬要求），`pack` 29 s / `verify` 0.4 s。详见 §5.4。
- [x] **客户端 WZL/WZX 读取器** ⇒ [`client/core`](../client/core)（`wzl.rs` / `wzx.rs` / `palette.rs`）：
      纯函数、无 SDL、有黄金哈希回归；`client/app` 已渲染真实精灵（登录界面右侧面板，**人工确认无偏色**）。
- [x] ~~核对地图二层 vs 三层~~ ⇒ **已定案：确实是三层**（§3.3a），`MidImg` 走 `SmTiles`；
      并顺带得到 `btArea` 的真实语义（前景层选哪本 `Objects<N>.wzl`）。
- [x] **地图源目录二选一**（**D-22 已定**，2026-10-07）：**取 `mir2c/map`（770 张，SDO 经典版）**；
      `mir2go` 的地图**不采用**（557 对里 5 对内容不同，见 §3.3c）。
- [ ] **地图解析器补变体**：36 B/格（39+7 张）与 `EM*`/`T2*` 族（90+4 张，**布局未定**）—— §3.3b。
- [ ] **`bsr03.mex` 识别**（436 KB，魔数未知；客户端集里唯一的非 `.map` 文件）。
- [ ] 点阵字库方案（自带位图字库，零字体依赖）。
- [ ] **美术一致性抽查**（C-1）：解 `Prguse`/`Hum` 与原版 1.76 对照（[decisions.md §0.2](./decisions.md)）。
- [ ] 核对 WIL 头部偏移（§3.1 的 `wix[0] - 1024` 法）—— 当前素材是 WZL，**暂不需要**。

# 资产策略与格式规格

> 状态：规格部分**已实测**，容器设计**待定稿**（D-11）。
> 决策见 [decisions.md D-06/D-11](./decisions.md)。

---

## 1. 原则

1. **美术是内容，不是实现**。必须是同一套美术，但**载体可自选**。
2. **转换必须逐像素可回验**。工具要提供 `verify`：解码产物与原始文件逐字节比对。
   没有这条，"体感一致"就没有证据（D-06 的硬要求）。
3. **原始文件是唯一真源**。转换产物建议**脚本生成、不入库**（沿用 `seedgen` 模式）。

## ⚠️ 2. 前置阻塞：素材当前缺失

见 [decisions.md §0](./decisions.md)——**全盘搜索零个 `.wil`/`.wix`**，
`mir2standard` 只有源码没有美术。**在素材到位前，视觉验收无法进行。**
本文件剩余部分的体积收益对**地图部分已实测**，对美术部分只能给预期区间。

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

⚠️ **待核对**：旧客户端 `MapUnit.pas` 只用两层（`wBkImg` / `wFrImg`），
而 `mir2go` 归为三层（`Bk` / `Mid` / `Fr`）。需确认旧客户端第二层对应哪个字段。

### 3.4 其他

| 格式 | 规格 |
|---|---|
| `lig*.dat` | 6 档光照掩膜（`PlayScn.pas:28-35`），索引色小图；掩膜矩阵在 `PlayScn.pas:37-120`（`LightMask0..5`） |
| `.wav` | 音效，标准格式，自解析 |
| `Music/%d.mp3` | 按地图编号取音乐（`SoundUtil.pas:219`） |

---

## 4. 资产体量与压缩收益（实测）

| 资产 | 原始 | gzip -9 后 | 倍率 |
|---|---|---|---|
| **地图 605 张** | **242 MB** | **~17 MB** | **0.06–0.10x** ⭐ |
| 服务端配置（`mir2go/data` 其余） | ~7 MB | — | — |
| 美术库 `.wil` | **缺失** | 预期 0.4–0.6x | 待实测 |

单张实测：

```
data/map/0.map      5.61 MB → 0.39 MB   (0.070x)
data/map/3.map      9.16 MB → 0.54 MB   (0.059x)
data/map/0100.map   0.003 MB → 0.0003 MB (0.102x)
data/map/ygfx1.map  23.0 MB（最大单张）
```

旁证：`mir2go` 把整个 `data/`（含这 242 MB 地图）入库后，`git size-pack` 只有 **20.84 MiB**。

**结论**：地图不压缩是纯粹的浪费，**10–17 倍的体积差**——这单独就足够支撑 D-11
选「自研容器」而不是直读原始文件。

---

## 5. 自研容器设计（M2PK v1，草案）

**目标**：单一真源可回验、随机访问、可并行解码、压缩收益最大化。

```
Header（32 B，小端）
  0   magic      [4]  "M2PK"
  4   version    u16  1
  6   kind       u8   0=图像库 1=地图 2=音频 3=字库
  7   flags      u8   bit0=每项独立zlib  bit1=含调色板  bit2=已展开RGBA
  8   count      u32  项数
  12  paletteOff u32  0 = 无
  16  paletteLen u32  1024 = 256 × BGRA
  20  dataOff    u32
  24  reserved   [8]

Entry（24 B × count，紧随 Header）
  offset    u32   相对文件头
  compSize  u32   压缩后字节数
  rawSize   u32   原始字节数
  w         i16
  h         i16
  px        i16   锚点偏移（原版 px/py，渲染对齐要用）
  py        i16
  fmt       u8    0=8bit索引 1=RGBA8888
  flags     u8    每项独立（例如该图不可压缩）
  reserved  u16
```

设计要点：

1. **保留 8 位索引色 + 每库调色板**。展开成 RGBA 会让数据量 ×4，
   压缩后仍不划算。客户端继续用调色板查表 shader。
2. **每项独立压缩**（不是整文件流式）。换来：随机访问、并行解码、单张损坏不影响全局。
   代价：压缩率略降（可接受）。
3. **按"库"分包**：一个 `.wil` → 一个 `.m2pk`；地图可合成少量分卷
   （单卷 ~17 MB 的量级完全可接受）。
4. **`px`/`py` 必须保留**。原版的图有锚点偏移，丢了会导致所有精灵错位。
5. **`verify` 是硬要求**：`m2pk verify --against <原始目录>` 必须能做
   逐像素（图像）/ 逐字节（地图、音频）比对并返回非零退出码。

**地图的解压策略**：按地图为单位解压（单张解压后最大 23 MB）+ LRU 缓存，
不要一次全解（242 MB）。

---

## 6. 资产树布局

```
assets/                    # 由 tools/ 生成，脚本产物建议不入库
├── image/                 # 每库一个：hum.m2pk, weapon.m2pk, mon1.m2pk ...
├── map/                   # 分卷：map-0.m2pk ...（单卷 ~17 MB）
├── audio/                 # sfx.m2pk（wav）+ music.m2pk（mp3）
└── font/                  # 点阵字库（bitmap）
```

命名规则遵循 [D-03](./decisions.md)：**全小写、无中文、`/` 分隔**。

---

## 7. 待办

- [ ] **素材获取**（D-15，阻塞）。
- [ ] 素材到位后：核对 WIL 头部偏移（§3.1 的 `wix[0] - 1024` 法）并写单测。
- [ ] 实测美术库的压缩率，与地图的 0.06–0.10x 对比，确认容器收益。
- [ ] 核对地图二层 vs 三层的字段对应（§3.3）。
- [ ] `tools/m2pk` 转换器 + `verify`。
- [ ] 点阵字库方案（自带位图字库，零字体依赖）。

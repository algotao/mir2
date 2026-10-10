# 权威口径账本：素材 / 数据 / 字段 → 外观与特效

> **这份文档要解决一件事**：把「官方代码里的字段」↔「我们磁盘上的素材」之间的对应关系
> 一次性钉死，免得以后每换一把武器、每加一层特效就重新猜一遍。
>
> 起因（2026-10-09，用户要求）：上两轮连着踩了两个同源的坑 —— 把 `.wzx` 当 16 字节/项解析，
> 得出"素材坏了"，于是**换错图库**（D-67）并写在文档里；后来又发现**文档说的"素材缺失"是错的**
> （`Npc.wzl` 一直在，还带真图）。这类错误的共同点是：**没有一份"谁权威、字段怎么映射"的账本**。
>
> 纪律（三条，违反任何一条都会重演上面的坑）：
> 1. **口径只认 `mir2standard/GameOfMir`**（官方 Delphi 服务端 + 客户端源码）。它给不出的，才看
>    OpenMir2（C# 服务端）/ Crystal（C# 客户端）对读，并标注"推断"。
> 2. **素材只认 `mir2c/data`**。别用任何副本、别用 Wiki、别用印象。每个结论都要能落到
>    `wzldump` 解出来的像素上（§9 有命令）。
> 3. **"素材缺失"是会过期的**：这套素材由登录器**按需下载**（146/252 个 `.wzl` 仍是空壳，
>    但会变）。任何"缺不缺"的结论都必须**当场核验**（§9 第一条命令），不许照抄本文或旧文档。

---

## 1. 唯一真源（谁权威）

| 类别 | 唯一真源 | 谁规定的 / 证据 | 产物 → 运行期入口 |
|---|---|---|---|
| **美术图库**（.wzl/.wzx） | `mir2c/data/*.wzl`（252 对）★ 本源/怀旧 客户端 | `tools/artpack/build.sh:23` 写死 `CANON="$ROOT/../mir2c/data"`，**且刻意不自动回退**（换素材＝换整个游戏美术，属最难查的一类 bug） | `assets/image/images.m2pk`（1.25 GB，IMGP 载荷）→ `client/core/src/image_lib.rs::art_archive()` |
| **地图** | `mir2c/map/*.map`（770 张） | `tools/m2pk/build.sh:23` 同款 CANON | `assets/map/maps.m2pk` |
| **音频** | `mir2c/wav/*.wav`（777 个） | `tools/wavpack/build.sh` | `assets/audio/sounds.m2pk` |
| **服务端配置**（Envir） | `Mir2-GeeM2/Envir` → 抄进 `server/data/envir/*.txt` | `server/internal/data/envir.go:11`（"来自 1.76 官方服务端 Mir2-GeeM2/Envir"） | 运行期 `data.LoadDir`（`internal/data/load.go:28`） |
| **物品 / 怪物 / 魔法表** | `server/data/seed/GEEM2.db.sql`（**GeeM2 官方 1.76 数据库**，3 张表：`Magic`/`Monster`/`StdItems`） | `cmd/seedgen/geem2.go:24/79`；GeeM2 **覆盖** OpenMir2（`cmd/seedgen/main.go:139-143`） | 生成期 → `stditems.json`/`monsters.json`/`magics.json` → 运行期 `data.LoadDir` |
| **公式 / 行为口径** | `mir2standard/GameOfMir/**`（Delphi 服务端 + 客户端源码） | 唯一能回答"**为什么这么算**"的源；本文每条都注了 `文件:行号` | 抄进我们代码的注释里 |
| 对读参考（非口径） | `OpenMir2`（C# 服务端）、`Crystal`（C# 客户端） | 交叉验证用；**不单独作为依据** | — |

### 1.1 哪些**不是**权威（别用）

| 东西 | 为什么不能用 |
|---|---|
| `mir2c/data/*.dat`（78 个） | 是**客户端资源**（技能/物品**说明文本**、地图标注、`Gameconfig.ini` 分线配置），**不是数据表**。`mir2c/data` 里**没有** `.db/.sql/.wil/.pak` —— 物品/怪物表压根不在客户端集里 |
| `mir2c/data/Weapon2.wzl`、`Weapon9.wzl` | **不是**主武器库（主库是 `Weapon.wzl`，见 §4.4）。它们装的是**另一套武器造型**，索引口径**尚未确认** |
| `weapon3/4/10.wzl`、`hair_ck.wzl`、`hum2..9.wzl`、`mon23..42.wzl` 等 | 当前是 **64 字节空壳**（登录器未下载），`decode` 取不到图；**不要据此断定"素材坏了"**（见 §7 坑 1、3） |
| 旧文档里的一切"素材缺失"结论 | 会过期。以 §9 的当场核验为准 |

---

## 2. 特征（Feature）：外观是怎么送到客户端的

### 2.1 人类 —— `MakeHumanFeature`

`mir2standard/GameOfMir/Common/Grobal2.pas:2729`（客户端同构：`MirClient/Grobal2.pas:2700`）：

```pascal
function MakeHumanFeature(btRaceImg, btDress, btWeapon, btHair: Byte): Integer;
begin
  Result := MakeLong(MakeWord(btRaceImg, btWeapon), MakeWord(btHair, btDress));
end;
```

`MakeWord(a,b) = a | (b<<8)`、`MakeLong(lo,hi) = lo | (hi<<16)` ⇒ 32 位 `cfeature` 按字节：

| 字节 | 含义 | 客户端提取函数（`MirClient/Grobal2.pas`） |
|---|---|---|
| bits 0..7 | **RaceImg**（0=人物；50=商人；其它=怪物） | `RACEfeature` `:2686` |
| bits 8..15 | **Weapon** = `武器Shape*2 + 性别` | `WEAPONfeature = HiByte` `:2670` |
| bits 16..23 | **Hair** = `发型*2 + 性别` | `HAIRfeature` `:2682` |
| bits 24..31 | **Dress** = `衣服Shape*2 + 性别` | `DRESSfeature` `:2674` |

### 2.2 怪物 / NPC —— `MakeMonsterFeature`

`Grobal2.pas:2733`：`MakeLong(MakeWord(btRaceImg, btWeapon), wAppr)` ⇒ 高 16 位是 **Appr**（外观号），客户端用 `APPRfeature` 取出后 `race = appr div 10`、`pos = appr mod 10` 选块（`MirClient/Actor.pas:1015-1031`）。

### 2.3 特效字 —— `featureEx`（独立的另一个 Word）

`M2Server/ObjBase.pas:19982`：

```pascal
if m_boOnHorse then Result := MakeWord(m_btHorseType, m_btDressEffType)
else                Result := MakeWord(0, m_btDressEffType);
```

⇒ 低字节 = **坐骑号**（`Horsefeature`）、高字节 = **衣服特效号/翅膀**（`Effectfeature`），
常量在 `Grobal2.pas:2691/2695`。随 `SM_LOGON` / `SM_FEATURECHANGED` 一起下发。

### 2.4 我们的对应

| 官方 | 我们 | 位置 |
|---|---|---|
| `MakeHumanFeature` / 字节布局 | `proto.MakeFeature(raceImg, weapon, hair, dress)` | `server/internal/proto/body.go:89` |
| 拆包 | `unpack_human_feature` | `client/core/src/actor.rs:214` |
| 定值 | `Server.updateFeature` | `server/internal/gamesvr/equip.go:279` |

---

## 3. 字段 → 表现：`StdItems` 每个字段到底驱动什么

结构：线协议 `Common/Grobal2.pas:541`（`TStdItem`，60B）；服务端运行期 `M2Server/ItmUnit.pas:22`。
我们的表：`server/data/seed/GEEM2.db.sql:78-141`（62 列，**前 24 列**逐列对应 `data.StdItem`，
`server/internal/data/item.go:80-107`）。

### 3.1 按用途分三类（**这是最容易搞错的地方**）

| 字段 | 类别 | 驱动什么 | 证据 |
|---|---|---|---|
| `Shape` | **外观** | 衣服/武器的 feature 字节（`Shape*2+性别`）；**也是特效号**（139/140/141/142/154/182）、坐骑号（51..100）、衣服特效号（1..50） | `ObjBase.pas:20007/20018/3129/3134-3149` |
| `Looks` | **图标** | `Items.wil[Looks]`（背包/地面/买卖列表）；**绝不是外观号** | `MirClient/FState.pas:4020/4841/5431` |
| `AniCount` | **特效开关** | 111..183 一大票特殊装备效果（见 §3.3） | `ObjBase.pas:2960-3057` |
| `Stdmode` | **分类** | 决定槽位与使用行为（见 §3.2） | `M2Share.pas:3516`、`LocalDB.pas:306` |
| `Source` | 数值+特效 | 武器强度/神圣（`btWeaponStrong`/`btUndead`） | `ObjBase.pas:3098-3108` |
| `Reserved` | 逻辑 | 绑定 / 不可取下标记 | `ObjBase.pas:17132/17138` |
| `Light` | 表现 | 是否发光（衣服分支置 `m_nLight := 3`；随后又被右手火把/蜡烛覆盖） | `ObjBase.pas:3129` / `3387-3390` |
| `Weight` / `DuraMax` / `AC..SC` / `Need*` / `Price` / `Stock` | **纯数值** | 负重、持久、攻防、需求、价格库存 | `ItmUnit.pas:696-698` 等 |
| `btValue[]`（**运行时**，不在 DB） | 特效 | `[3]`幸运 `[4]`诅咒 `[5]`命中·准确 `[6]`速度 `[7]`神圣 `[10]`升级标记 | `ItmUnit.pas:117-132`、`ObjBase.pas:2396/23641-23658`（**语义为交叉推断**，无集中注释表） |

**一句话记牢**：`Shape` = 画出来的样子，`Looks` = 背包里的图标，`Appr` = 怪/NPC 外表号，
`RaceImg` = 种族（我们 Go 里把 merchant.txt 的"主要部分"也叫 `RaceImg`，**那是 Appr**，
见 §6.3 术语对照 —— 这个词最容易读错）。

### 3.2 `Stdmode` 取值表（分类）

`M2Share.pas:3517` + `LocalDB.pas:307` + `ObjBase.pas:1597/1610/2114/17360`：

| StdMode | 分类 | 备注 |
|---|---|---|
| 0 | 药 | `Shape=1` 回血、`Shape=2` 解毒（`ObjBase.pas:23338`） |
| 1 | 食物 | 饱食度 |
| 3 | 药水 | `Shape`：1 回城 / 2 随机传送 / 3 地牢逃脱 / **4 祝福油** / 5 行会回城 / 9 修复 / 10 超级修复 |
| 4 | 技能书 | `ReadBook` |
| **5 / 6** | **武器** | 6 = 重击武器（客户端 `CM_HEAVYHIT`） |
| **10 / 11** | **衣服** | 10 = 男装、11 = 女装（我们 `updateFeature` 就靠它选性别位） |
| 15 | 头盔 | |
| 19 / 20 / 21 | 项链 | |
| 22 | 戒指 | 强推断（配置项名 `nRing22`） |
| 23 / 26 | 手镯 | 配置项 `nRing23` / `nArmRing26`（强推断） |
| 25 | 护身符 / 符 | 占 `U_ARMRINGL` / `U_BUJUK` |
| 28 / 29 / 30 | 右手（火把 / 蜡烛类） | 决定 `m_nLight` |
| 31 | 可解包物品 | `AniCount=0` 表示解绑 |
| 40 | 肉类 | 按耐久 |
| 43 | 耐久下限 1 万 | |
| 45 | 随机外观 | |
| 50 | 名字后显 `#耐久` | |
| 51 | 宝石 | |
| 52 / 53 / 54、62 / 63 / 64 | 靴 / 腰带 / 宝石（1.70 新装备） | |

### 3.3 `AniCount` 特效编号（**"装备效果/特性"的真实出处**）

`ObjBase.pas:2960-3057`（全部由 `RecalcAbilitys` 在**穿脱装备时**重算）：

| AniCount | 效果 | | 编号 | 效果 |
|---|---|---|---|---|
| 111 | 隐身 | | 139 | 防麻痹 |
| 112 | 传送 | | 140 | 超人 |
| **113** | **麻痹**（`m_boParalysis`） | | 141 / 142 / 182 / 183 | 经验 / 爆率 |
| 114 | 复活 | | 143 | 破盾 |
| 115 | 火焰 | | 144 | 破复活 |
| 116 | 回血 | | 145 | 行会回城 |
| 117 | 愤怒 | | 150–158 | 组合（麻痹护身 / 麻痹火球 / 传送麻痹…） |
| 118 | 护盾 | | 170 | 愤怒 |
| 119 | 负重 | | 171 / 172 | 免掉落 |
| 120 | 加速训练 | | 135 / 138 | 套装计数 |
| 121 | 探测 | | | |

另按 `Shape` 特值：139 防麻、140 超人、141 经验、142 爆率、154 护身火球（`ObjBase.pas:3134-3149/3033`）。

### 3.4 什么时机重算并下发

| 时机 | 位置 | 动作 |
|---|---|---|
| 穿上 / 脱下 | `ObjBase.pas:17164-17168` / `17276-17280` | `RecalcAbilitys`（算特效）+ `SM_TAKEON_OK/TAKEOFF_OK` + `FeatureChanged` |
| 衣服/武器持久归零（爆装） | `ObjBase.pas:22502/22543` | `wIndex := 0` + `FeatureChanged` |
| 上下马 | `ObjBase.pas:15057-15077` | `FeatureChanged` |
| 改性别 / 改发型 | `ObjBase.pas:10779` / `13110` | 只 `FeatureChanged`（**不重算特效字**） |
| 登录 | `ObjBase.pas:16784-16799` | `SM_LOGON`（带 feature）+ `SM_FEATURECHANGED` |
| 移动/转身广播 | `ObjBase.pas:5297/5307/5317/5370/5449/5513` | `CharDesc.feature := GetFeature(...)` |

**唯一计算处**是 `ObjBase.pas:19992-20023`（`GetFeature`），公式：

```pascal
nDress  := StdItem.Shape * 2;  Inc(nDress,  m_btGender);   // 男 0 / 女 1
nWeapon := StdItem.Shape * 2;  Inc(nWeapon, m_btGender);
nHair   := m_btHair * 2 + m_btGender;
```

`m_btGender` 来源：登录读档 `UsrEngn.pas:2308`（`HumData.btSex`）、性别 NPC `ObjNpc.pas:8760`、GM 命令 `ObjBase.pas:10779`。

---

## 4. 素材层与取图公式

### 4.1 库名表（官方常量 → 磁盘 → 我们）

| 官方常量（`MirClient/Share.pas`） | 文件 | mir2c 现状 | 我们 |
|---|---|---|---|
| `HUMIMGIMAGESFILE` `:59` | `Hum.wil` | ✓ 13.9 MB | ✅ 已用（`HUM_LIB`） |
| `WEAPONIMAGESFILE` `:61` | `Weapon.wil` | ✓ 8.2 MB，**76 块** | ✅ 已用（`WEAPON_LIB`） |
| `HAIRIMGIMAGESFILE` `:60` | `Hair.wil` | ✗ **不存在**；头发库是 **`hair2.wzl`**（21600 张 = 36 块 = 18 种发型 × 2 性别） | ✅ 已用（`HAIR_LIB = "hair2"`，2026-10-10） |
| `HUMWINGIMAGESFILE` `:57` | `HumEffect.wil` | ✓ **22 MB** | ❌ 未做（翅膀/时装层） |
| —（本版源码未引） | `WeaponEffect.wzl` | ✓ **25 MB** | ❌ 未做（用途待查，见 §8-4） |
| `BAGITEMIMAGESFILE` `:66` | `Items.wil` | ✓ 3.7 MB（6846 张图标） | ✅ 已用（背包/图标） |
| `STATEITEMIMAGESFILE` `:67` | `StateItem.wil` | ✓ 14 MB | ❌ 未做（大图/状态窗） |
| `DNITEMIMAGESFILE` `:68` | `DnItems.wil` | ✓ 1.5 MB | ❌ 未做（地面掉落图标） |
| `NPCIMAGESFILE` `:62` | `Npc.wil` | ✓ **7.6 MB（5010 张，有真图）** | ✅ 已用（`NPC_LIB`，**曾经被误判为缺失**，见 §7 坑 3） |
| `MONIMAGEFILE` `:71` | `Mon%d.wil` | Mon1..22、Mon34 ✓；**Mon23..42 是空壳** | ✅ 已用（`Mon%d` + `mon_offset`） |
| `MAGICIMAGESFILE`/`MAGIC2IMAGESFILE` `:63/64` | `Magic.wil`/`Magic2.wil` | ✓（`magic10-12` 空壳） | ✅ 已用（技能特效） |
| `TITLESIMAGEFILE`/`SMLTITLESIMAGEFILE` `:55/56` | `Tiles`/`SmTiles` | ✓（`tiles9/10/15/16/17`、`smtiles10/14-16/22-24` 空壳） | ✅ 已用 |
| `MINMAPIMAGEFILE` `:54` | `mmap.wil` | ✓ 19 MB | ✅ 已用（小地图） |
| `MAINIMAGEFILE`/`2`/`3` `:47-49` | `Prguse*.wil` | ✓ | ✅ 已用（HUD/窗口） |
| `CHRSELIMAGEFILE` `:53` | `ChrSel.wil` | ✓ 21.6 MB | ✅ 已用（选人界面） |
| `EFFECTIMAGEFILE` `:73` | `Effect.wil` | ✓ 5.1 MB | ✅ 已用 |
| `EVENTEFFECTIMAGESFILE` `:65` | `Event.wil` | ✗ **不存在** | — |
| `OBJECTIMAGEFILE%d` `:69/70` | `Objects*.wil` | ✓（`objects11/12/19/35/36/42/44` 空壳） | ❌ 未做（门/静态物） |
| `DRAGONIMAGEFILE` `:72` | `Dragon.wil` | ✗ 不存在 | — |

**空壳 ≠ 缺失**：252 个 `.wzl` 里**146 个当前是 ≤128 字节的空壳**（登录器按需下载，见 §1.1）。
其中和我们有关的：`weapon3/4/10`、`hair_ck`/`hair4_ck`、`hum2..9`、`hum_ck*`、
`mon23..42/48/54`、`objects11/12/19/35/36/42/44`、`magic10-12`、`tiles9/10/15-17`、
`smtiles10/14-16/22-24`、`weaponfashion*`、`*_ck*`。

### 4.2 人物分层的取图公式（**核心**）

`MirClient/Actor.pas:15` `HUMANFRAME = 600`（每种外观/性别 600 幅），`Actor.pas:1903-1910`：

```pascal
m_nBodyOffset   := HUMANFRAME * m_btDress;                       // 600 * 衣服字节
m_nWeaponOffset := HUMANFRAME * m_btWeapon;                      // 600 * 武器字节
m_nHairOffset   := HUMANFRAME * (m_btHair * 2 + m_btSex);         // ⚠️ 见 §8-1
```

即 **身体、武器两层客户端直接乘 600** —— 服务端给的字节**已经是 `Shape*2+性别`**。
反推也在源码里：`ClMain.pas:6877/6913` `FileIdx := (Dress - m_btSex) div 2;`。

动作表 `HA`（`Actor.pas:76-92`，每项 `(start, frame, skip)`），**图号 = `start + dir*(frame+skip) + 帧`**：

| 动作 | start | frame | skip | 步长 |
|---|---|---|---|---|
| Stand | 0 | 4 | 4 | 8 |
| Walk | 64 | 6 | 2 | 8 |
| Run | 128 | 6 | 2 | 8 |
| Hit | 200 | 6 | 2 | 8 |
| Spell | 392 | 6 | 2 | 8 |
| Struck | 472 | 3 | 5 | 8 |
| Die | 536 | 4 | 4 | 8 |

**对齐（锚点）**：不是算出来的，是**每帧图片自带的 `nPx/nPy`**（`Wil.pas:628-629`），
三层各取各的（`Actor.pas:3727/3733/3763`），绘制时各自 `+px/+py` 叠加。
**武器画在身体前还是身体后** 由 `WORDER` 表决定（`Actor.pas:465/3788/3835-3848`）。
⚠️ 我们客户端用的是"锚点"字段（`client/app/src/actor.rs:453`），与原版同一套语义 ——
**换库/换块时锚点会跟着变，别按格心硬对齐**。

### 4.3 每把武器占两块 —— `块 = 2*Shape + 性别`

- 官方：`nWeapon := StdItem.Shape * 2; Inc(nWeapon, m_btGender)`（§3.4）。
- 素材印证（**已逐块扫过**）：`Weapon.wzl` = 45600 张 ÷ 600 = **76 块**，**块 0/1 恒为空**
  （`Shape` 从 1 起 ⇒ 字节从 2 起），**块 2..75 共 74 块 = 37 把 × 2 性别**，与我们的物品表
  （武器 `Shape` 1..37 全用到）**一格不差**。
- 木剑 `Shape=1`、男 ⇒ **块 2 / 图号 1200**（D-71 实证）；铁剑/青铜剑 `Shape=2` ⇒ 块 4/5。

### 4.4 边界（**shape ≥ 38 时别想当然**）

| 库 | 张数 | 块数 | 说明 |
|---|---|---|---|
| `Weapon.wzl` | 45600 | **76** | `Shape` 1..37，已实证 |
| `Weapon2.wzl` | 25200 | **42** | = 21 把 × 2；**块 0 就有图**（≠ 上面的"块 0/1 空"规律）⇒ 索引口径**不是**同一条 |
| `Weapon9.wzl` | 18000 | **30** | = 15 把 × 2 |
| `weapon3/4/10.wzl` | — | — | 64 字节空壳 |

⇒ 76 + 42 + 30 = 148 块 = **74 把**。**推测**（未证实）：`Weapon2` 接 `Shape` 38..58、
`Weapon9` 接 59..73，块号 = `2*Shape+性别 - 76` / `-118`。**没有证据链，别照抄**。

我们物品表里**超出覆盖范围**的武器（这些武器现在画不出手上外观，客户端会安静地不画）：

| Shape | 块 = 2*Shape | 武器 |
|---|---|---|
| 101 | 202 | 魔血剑 |
| 102 | 204 | 龙血剑 |
| 103 | 206 | 神血枪 |
| 105 | 210 | 黑虎斧 |
| 106 | 212 | 火莲杖 |
| 107 | 214 | 鹤羽扇 |
| 108 | 216 | 炎狱血剑 |

（7 种；`Shape` ≤ 37 的 37 种全部有素材。这几个来自 GeeM2 较新版本的物品表，
**不是 1.76 的武器** —— 要么按新库接，要么在物品表里标成"无外观"。）

### 4.5 怪物 / NPC

- 怪物容器 `Mon%d.wil`，块起点 `GetOffset(appr)`（`Actor.pas:1015-1031`：`race = appr div 10`、
  `pos = appr mod 10`，每 race 的块大小不同：280/230/360/430/440/350…）；
  `Appr >= 1000` 时原版去读**外部文件** `Graphics\Monster\<Appr>.wil`（本套素材没有 ⇒ 降级成标记）。
- NPC 容器 `Npc.wil`，块起点 `GetNpcOffset(appr)`（`Actor.pas:1156-1200`，注意**上面那套 case 被
  注释掉了、生效的是下面那套**），站立帧 = `ActStand.start + dir*(frame+skip)`（`Npc` 站着不动 ⇒ 帧恒 0）。
  **已核验**：我们 `merchant.txt` + `Npcs.txt` 里地图 0 的 **23 + 11 个 NPC，外观块全部有图**
  （例：屠夫 `appr=4` → 块起点 240，42 帧有图）。
- ⚠️ **NPC 只有 6 个朝向**：块长 60 帧（`GetNpcOffset` 的步长 `appr*60`）、商人那张动作表
  （`MA31`/`MA32`）站立**步长 10** ⇒ 方向 0/10/20/30/40/50，**给到 6/7 就串到下一个 NPC 的
  图块**（画成旁边那个的样子）。⇒ 客户端 `npc_index` 按 `(块长-1)/步长` **夹住方向**
  （`client/core/src/actor.rs`）。
- ⚠️ **NPC 朝向要固定**：官方默认 `m_btDirection := 4`（朝下/正面，`ObjBase.pas:1210`）；
  `merchant.txt` 那个"正面"列官方服务端**不读**（`TMerchant` 记录里没有朝向字段，只有卫兵
  `LocalDB.pas:239` 从配置读）。**不能像怪物那样随机**（怪物随机没问题：它们会走动转向）。

### 4.6 图标 / 大图 / 掉落

| 用途 | 图号 | 库 | 证据 |
|---|---|---|---|
| 背包 / 地面 / 买卖列表 | `Looks` | `Items.wil` | `FState.pas:4020/4841/5431` |
| 状态窗大图 | `Idx`（`<10000` 直接取；否则 `Graphics\Items\St<Idx div 10000>.wil`） | `StateItem.wil` | `ClMain.pas:6812-6824` |
| 地面掉落小图标 | — | `DnItems.wil` | `Share.pas:68` |

**没有** `Items2`/`Items_ck` 之类的备用图标库 —— 图标只有一条路。

---

## 5. 特效（"效果 / 特性"到底怎么表现）

### 5.1 魔法与打击特效

库 `Magic.wil` / `Magic2.wil`，基址表写死在客户端 `magiceff.pas:34-77`：
`EffectBase[31] = (0,200,400,600,0,900,…,3840)`、`HitEffectBase[5] = (800,1410,1700,3480,3390)`；
**图号 = `base + dir*10 + 帧`**（每特效每方向 10 幅，`Actor.pas:3925/3941`）。

### 5.2 人物翅膀 / 时装（`HumEffect.wil`）

由 `featureEx` 的高字节（`Effectfeature`）来，`Actor.pas:1917-1918`：

```pascal
if m_btEffect = 50 then m_nHumWinOffset := 352
else                    m_nHumWinOffset := (m_btEffect - 1) * HUMANFRAME;   // 600/块
```

绘制：帧 `< 64` 时用 `offset + dir*8 + 帧`，否则 `offset + 当前帧`（`Actor.pas:3749-3756`）。
**什么让 `m_btDressEffType` 有值**（`ObjBase.pas:3113-3127`，穿脱时重算）：

```pascal
if i = U_RIGHTHAND then begin
  if StdItem.Shape in [1..50]   then m_btDressEffType := StdItem.Shape;      // 衣服特效/翅膀
  if StdItem.Shape in [51..100] then m_btHorseType    := StdItem.Shape - 50; // 坐骑
end;
if m_UseItems[i].btValue[5] > 0 then m_btDressEffType := m_UseItems[i].btValue[5];
if StdItem.AniCount > 0        then m_btDressEffType := StdItem.AniCount;
```

即：**翅膀/坐骑看"右手那件东西的 `Shape`"**（1..50 翅膀、51..100 坐骑），衣服的 `btValue[5]`
与任何装备的 `AniCount` 都能覆盖它。

### 5.3 武器断裂特效

`WPEFFECTBASE = 3750`、`MAXWPEFFECTFRAME = 5`（`Actor.pas:30-31`），图号
`3750 + dir*10 + 帧`，**仍在 `Magic.wil` 里**；触发是收到服务端 `SM_BREAKWEAPON`
（`ClMain.pas:4912-4917` → `Actor.pas:3552/3580-3585/3952`）。

### 5.4 ⚠️ 这个版本的客户端**没有**"+N 武器发光"

`DrawWeaponGlimmer`（`Actor.pas:2014-2034`）**整段被 `(* *)` 注释掉**，虽然 `3837/3850`
还在调用它。⇒ 「武器 +N 发光」不是我们漏做，是这份客户端源码里就没有；
`WeaponEffect.wzl`（25 MB）**在源码里也找不到引用**（用途待查，§8-4）。
别拿别的版本（或记忆里的"祝福油发光"）当需求。

### 5.5 光源

`RM_CHANGELIGHT` → `SM_CHANGELIGHT`（`ObjBase.pas:5948-5955`），`m_nLight` 由右手的
火把/蜡烛类（`StdMode 28/29/30`）重算（`ObjBase.pas:3387-3390`）。

---

## 6. 我方现状与术语对照

### 6.1 已实现

| 层 | 状态 | 位置 |
|---|---|---|
| 特征字节（weapon/dress 两位） | ✅ | `server/internal/gamesvr/equip.go:279`、`netproto.go:1519-1521` |
| 身体层 `Hum.wzl` | ✅ | `client/app/src/actor.rs:375` |
| 武器层 `Weapon.wzl` | ✅ | `client/app/src/actor.rs:472` |
| 头发层 `hair2.wzl` | ✅ 2026-10-10 | `client/app/src/actor.rs` 的 `hair_sprite`、`core::actor::hair_index` |
| **F10 状态窗**（小人 + 13 装备槽 + 属性） | ✅ 2026-10-10 | `client/app/src/status.rs`（背板 `Prguse3[4]`） |
| 怪物 `Mon%d` / NPC `Npc.wzl` | ✅ | `client/core/src/actor.rs:280/352` |
| 图标 `Items.wzl[Looks]` | ✅ | 背包窗口 |
| 技能/地图/HUD/小地图 | ✅ | 各自模块 |

### 6.2 缺口（**有素材、只是还没做**）

| 层 | 素材 | 备注 |
|---|---|---|
| 翅膀 / 时装 | `HumEffect.wzl` ✓ 22 MB | 公式见 §5.2，但 `m_btDressEffType` 我们还没算 |
| 武器特效 | `WeaponEffect.wzl` ✓ 25 MB | 用途未确认（§8-4） |
| 坐骑 | 同 HumEffect | 需要服务端算 `m_btHorseType` |
| 地面掉落图标 | `DnItems.wil` ✓ | |
| 状态窗大图 | `StateItem.wil` ✓ | |
| 门 / 静态物 | `Objects*.wil` ✓ | |
| 装备外观层（头盔/项链/戒指/手镯） | **不存在** | 1.76 原版**就没有**：人物外观只有 **衣服 + 武器 + 头发** 三层，首饰不显示。别去找"项链的图" |

### 6.3 术语对照（这四个词最容易读错）

| 官方 Pascal | 我们的 Go | 我们的 Rust | 含义 |
|---|---|---|---|
| `StdItem.Shape` | `StdItem.Shape` | — | **外观号**（feature 字节的一半） |
| `StdItem.Looks` | `StdItem.Looks` | — | **图标号**（`Items.wil`） |
| `wAppr` / `Appr` | （怪物）`Appr` | `MonsterFeature.appr` | 怪/NPC **外表号** |
| `m_btRaceImg` / `btRaceImg` | `RaceImg` | `HumanFeature.race_img` | **种族**（0 人 / 50 商人 / 其它怪） |
| ⚠️ `data.NPC.RaceImg`（我们的 Go） | — | — | **其实是 Appr**（`merchant.txt` 的"主要部分"列）；`Npcs.txt` 那边叫 `Body`。命名沿用旧文档，读的时候按"Appr"理解 |
| `m_btGender` | — | — | 性别（0 男 / 1 女），**三处外观字节都要加它** |

---

## 7. 已经踩过的坑（每条附"怎么避免"）

| # | 坑 | 症状 | 避免 |
|---|---|---|---|
| 1 | **把 `.wzx` 当 16 字节/项解析** | 算出"45600 张 vs 11403 条记录"⇒ 断定素材坏了 | `.wzx` = **48 字节头 + `count`×4 字节偏移**（`client/core/src/wzx.rs`）。**别用别的语言手搓解析**，直接用 Rust 读取器（`wzldump`） |
| 2 | **在空块上取样** | `Weapon.wzl` 块 0/1 取样 ⇒ "手上什么都没有" ⇒ 换去 `Weapon2` | `Shape` 从 1 起 ⇒ 武器在**块 2 起**。**先扫全库占用图**（`--avg`），再决定看哪块 |
| 3 | **照抄"素材缺失"的旧结论** | 文档说 `Npc`/`Hair` 缺失 ⇒ 真信了就不做 NPC 层 | 素材是**按需下载**的。**当场核验**（§9 命令） |
| 4 | **拿 `Looks` 当外观号** | 用 `Items.wil` 的图标号去取 `Weapon.wil` ⇒ 空白 | `Shape` = 外观、`Looks` = 图标（§3.1）；`Weapon.wil` 取到空块时**先怀疑自己取错了块，再怀疑素材** |
| 5 | 只看"有没有图"就下结论 | 块号差一格也能有图（相邻武器） | 加**像素级**断言（木剑必须是棕色、铁剑必须是银灰），见 `client/core/src/actor.rs` 的素材测试 |
| 6 | 忘了锚点 | 换库后武器"浮"在人物旁边 | 对齐一律用**每帧自带锚点**（§4.2），别按格心 |

---

## 8. 待查（有明确问题、还没答案）

1. ~~头发字节的双重加倍~~ **已定口径（2026-10-10）**：官方 `M2Server` 发 `Hair*2+性别`、
   而 `MirClient` 收到**又乘一次** ⇒ 两边配不上（疑版本不配对）。我们**以客户端公式为准**：
   服务端发**原始**发型值（`join.go:92` 现在就是这样），客户端 `hair_index` 做 `*2+性别`。
   ⚠️ 别"顺手把服务端也乘一次"—— 那会让头发跳到 4 倍偏移上。
2. ~~`hair2.wzl` 能不能当头发层~~ **已验证（2026-10-10）**：`hair2` = 21600 张 = 36 块 =
   18 种发型 × 2 性别，与 `Actor.pas:1904` 的 `ImageCount div HUMANFRAME div 2` 完全吻合；
   按锚点把"身体 + 头发 + 武器"合成，8 个方向头发都长在头上（发型 3 = 棕发，全方向可辨）。
   块 0（发型 0、男）是空的 ⇒ 那是"默认光头"，不是素材坏了。
3. **`Weapon2.wzl` / `Weapon9.wzl` 的索引口径**（§4.4 的推测未证实）。验法：找一个确定属于
   新武器库的武器（形状 ≥ 38）在**现代客户端**里的实际块号，再倒推公式；或找本源客户端的物品表对照。
4. **`WeaponEffect.wzl`（25 MB）到底给谁用**：本版源码没有引用。可能是更新版本客户端的
   "+N 发光/武器光效"。别在没确认前实现它。
5. **`SM_CHARACTERINFO` 不存在**：给邻近玩家送名字+外观走的是 `SM_LOGON`/`SM_TURN`
   （`ObjBase.pas:1750/5297`）—— 如果在别处看到这个名字，那是别的版本或我们自己的命名。
6. `Stdmode` 的 `2 / 30 / 40 / 41 / 43` 中文语义、`btValue[8]/[9]` 语义：**源码里没有注释**，
   现表里的是**按上下文推断**。

---

## 9. 核验命令（可复制粘贴；结论变了就说明素材变了）

```bash
MIR2C=/Users/taohuifeng/Develop/git/mir2c/data      # 素材唯一真源
CLIENT=/Users/taohuifeng/Develop/git/mir2/client

# ① 素材"缺不缺/是不是空壳"—— 任何"缺失"结论都要先跑这条
for f in $MIR2C/*.wzl; do sz=$(stat -f%z "$f"); [ "$sz" -le 128 ] && echo "空壳: $(basename $f)"; done | wc -l
ls $MIR2C/Npc.wzl $MIR2C/Hair.wzl $MIR2C/hair2.wzl 2>&1        # 缺失会报 No such file

# ② 某个图的真身（找素材的基本操作：先列尺寸、再拼出来看）
cd $CLIENT
cargo run -q -p mir2-core --example wzldump -- $MIR2C Weapon 1200 1264 /tmp/ui --list      # 列记录
cargo run -q -p mir2-core --example wzldump -- $MIR2C Weapon 0 0 /tmp/ui --only 1208,1216 # 只看指定图号
cargo run -q -p mir2-core --example wzldump -- $MIR2C Weapon 0 25200 /tmp/ui --avg         # 逐张平均色（按颜色筛几万张）

# ③ 放大/拼版看细节（像素画必须最近邻放大，插值会糊）
python3 ../tools/imgtool.py crop  <in.bmp> <out.bmp> x y w h 4
python3 ../tools/imgtool.py sheet <out.bmp> 3 6 40,40,40 a.bmp b.bmp ...

# ④ 服务端字段 ↔ 素材的自动核对（带真素材跑，红了就是口径变了）
cd $CLIENT && MIR2C_DATA=$MIR2C cargo test --workspace
```

**改口径时必须同步改的地方**（漏一处就会自相矛盾）：
`server/internal/gamesvr/equip.go`（算字节）→ `client/core/src/actor.rs`（库常量 + 公式 + 素材测试）
→ `client/app/src/assets.rs`（预加载清单）→ 本文 → `docs/todo.md`。

//! 音效与音乐的**规格**：哪个事件放哪一条、`sound.lst` 怎么解析。
//!
//! 这一层**不含 SDL**（发声在 `app/src/audio.rs`）。分开的理由和 `login_ui.rs`
//! 一样：原版的音效是**数据驱动**的 "编号 → 文件"（`SoundUtil.pas:180-192`），
//! 而"什么事件放哪条编号"是**游戏知识**，与声卡无关 —— 放 core 能与 e2e 共用、
//! 并且可以单测（本模块的测试不需要声卡）。
//!
//! 出处（照抄，不是猜）：
//!
//! | 内容 | 原版 |
//! |---|---|
//! | 编号常量 `1..145` | `SoundUtil.pas:36-142` |
//! | `sound.lst` 装载 | `SoundUtil.pas:151-178`（调用点 `ClMain.pas:515-519`）|
//! | 播放（编号 → 文件 → 波形）| `SoundUtil.pas:180-192` |
//! | 脚步：按地形选 `1..32` | `Actor.pas:2144-2238`（消费点 `Actor.pas:2650-2662`）|
//! | 攻击：按武器分类 `50..57` | `Actor.pas:2250-2263`（消费点 `2396-2439`）|
//! | 被击中：防具/身体 `70..83` + 攻击武器 `60..65` | `Actor.pas:2264-2294`、`2336-2353` |
//! | 受伤/死亡：按性别 `138/139`、`144/145` | `Actor.pas:2242-2248` |
//! | 技能三段音 `10000 + 技能号*10 (+0/1/2)` | `Actor.pas:2297-2302` |
//! | 怪物音 `200 + 外观*10 (+0..6)` | `Actor.pas:2323-2332` |
//! | BGM：登录 / 选角 / 自己死亡 | `SoundUtil.pas:31-34`；`IntroScn.pas:518`、`:1152`；`Actor.pas:2374` |
//! | 进图音乐 `Music/<编号>.mp3` | `SoundUtil.pas:219`（编号由服务端下发，`ClMain.pas:5222`）|
//!
//! ⚠️ 原版**没有**音量滑条（只有"音效/音乐"两个开关，`MShare.pas:213-214`）、
//! **没有**距离衰减、**没有**左右声道 pan、**没有**毫秒级节流 —— 别自己加戏。
//! 唯一的去重是**动作级**的（`m_boRunSound`，`Actor.pas:2357-2359`）：一招只响一次。

use std::collections::HashMap;
use std::path::{Path, PathBuf};

use crate::m2pk::Archive;

/// 原版写死的音效编号（`SoundUtil.pas:36-142`）。
///
/// 只列**有语义、且我们接得上**的那些；脚步与攻击的分类值见下面的函数。
pub mod idx {
    /// 挖矿碎石（`Actor.pas:3498`）。
    pub const STRIKE_STONE: u16 = 91;
    /// 开石门（登录那扇门，`IntroScn.pas:801`）。
    pub const ROCK_DOOR_OPEN: u16 = 100;
    /// 选角石化解冻（`IntroScn.pas:1170/1187`）。
    pub const MELTSTONE: u16 = 101;
    /// ⚠️ 原版 `s_intro_theme` 与 `s_main_theme` **同值**（都是 102）——
    /// 照实抄，别"修"成两个数。
    pub const THEME: u16 = 102;
    /// 普通按钮点击（原版 `DLoginNewClickSound` 的 `csNorm`，`FState.pas:2380`）。
    pub const NORM_BUTTON_CLICK: u16 = 103;
    /// 石门按钮点击（`csStone`）。
    pub const ROCK_BUTTON_CLICK: u16 = 104;
    /// 玻璃按钮点击（`csGlass`）。
    pub const GLASS_BUTTON_CLICK: u16 = 105;
    /// 金币（`FState.pas:4668` 等三处）。
    pub const MONEY: u16 = 106;
    /// 吃东西/喝药（`ItemUseSound` 的 StdMode 1/2）。
    pub const EAT_DRUG: u16 = 107;
    /// 点药水（`ItemUseSound` 的 StdMode 0）。
    pub const CLICK_DRUG: u16 = 108;
    /// 地图传送：离开 / 出现（`Actor.pas:1638/1650`）。
    pub const SPACEMOVE_OUT: u16 = 109;
    pub const SPACEMOVE_IN: u16 = 110;
    /// 点装备（按部位，`ItemClickSound`，`SoundUtil.pas:293-310`）。
    pub const CLICK_WEAPON: u16 = 111;
    pub const CLICK_ARMOR: u16 = 112;
    pub const CLICK_RING: u16 = 113;
    pub const CLICK_ARMRING: u16 = 114;
    pub const CLICK_NECKLACE: u16 = 115;
    pub const CLICK_HELMET: u16 = 116;
    pub const CLICK_GROBES: u16 = 117;
    /// 点道具的兜底声。
    pub const ITMCLICK: u16 = 118;
    /// 男/女：被击中 / 死亡（`Actor.pas:2243-2247`）。
    pub const MAN_STRUCK: u16 = 138;
    pub const WOM_STRUCK: u16 = 139;
    pub const MAN_DIE: u16 = 144;
    pub const WOM_DIE: u16 = 145;
}

// ---------- 脚步：地形 ----------

/// 脚步音的**地形分组**（原版按地表图号判出来的那几组，`Actor.pas:2148-2238`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Terrain {
    Ground,
    Lawn,
    Rough,
    Stone,
    Cave,
    Wood,
    Room,
    Water,
}

impl Terrain {
    /// 走路的 `_l`（左脚）编号 —— 原版常量表里这八组是 1/9/13/5/21/17/25/29。
    pub fn walk_left(self) -> u16 {
        match self {
            Terrain::Ground => 1,
            Terrain::Stone => 5,
            Terrain::Lawn => 9,
            Terrain::Rough => 13,
            Terrain::Wood => 17,
            Terrain::Cave => 21,
            Terrain::Room => 25,
            Terrain::Water => 29,
        }
    }
}

/// 一次脚步该播哪条编号。
///
/// 原版：先按地形取"走"的基号，**跑**再加 2（`_l` → `_r`），最后脚 A/脚 B 差 1
/// （`Actor.pas:2237-2238` 加 2，`2659-2660` 播 `+0`/`+1`）。
///
/// `second_foot` = 该动画的**第二只脚**（原版在帧 4 播 `+1`）。
pub fn footstep(terrain: Terrain, running: bool, second_foot: bool) -> u16 {
    terrain.walk_left() + if running { 2 } else { 0 } + if second_foot { 1 } else { 0 }
}

/// 从地图三层的图号与区域号判出地形 —— `Actor.pas:2144-2238` 的**逐条**转写。
///
/// ⚠️ 三个注意点（都是原版的行为，不是我们的发挥）：
///
/// 1. 原版取的是 `正下方偏左` 的那一格：`cx div 2 * 2, cy div 2 * 2`（`Actor.pas:2146-2147`）
///    —— 调用方先把坐标对齐到偶数格再调这里；
/// 2. `bidx = 区域号*10000 + (地表图号 & 0x7FFF) - 1`（`Actor.pas:2149-2150`）；
/// 3. 判定**分三层依次覆盖**：先地表（带 `else` 兜底），再中间层、再前景层
///    —— 后两层的区间**只覆盖命中的那几个**，没命中就保留前一层的结论。
pub fn terrain(bk_img: u16, area: u8, mid_img: u16, fr_img: u16) -> Terrain {
    let bidx = area as i32 * 10000 + (bk_img & 0x7FFF) as i32 - 1;

    // ---- 地表 ----
    let mut t = match bidx {
        // 草地上行走
        330..=349
        | 450..=454
        | 550..=554
        | 750..=754
        | 950..=954
        | 1250..=1254
        | 1400..=1424
        | 1455..=1474
        | 1500..=1524
        | 1550..=1574 => Terrain::Lawn,
        // 粗糙的地面
        250..=254 | 1005..=1009 | 1050..=1054 | 1060..=1064 | 1450..=1454 | 1650..=1654 => {
            Terrain::Rough
        }
        // 石头地面
        605..=609
        | 650..=654
        | 660..=664
        | 2000..=2049
        | 3025..=3049
        | 2400..=2424
        | 4625..=4649
        | 4675..=4678 => Terrain::Stone,
        // 洞穴
        1825..=1924 | 2150..=2174 | 3075..=3099 | 3325..=3349 | 3375..=3399 => Terrain::Cave,
        // 木头地面（两组，第二组是"带栏杆"）
        3230 | 3231 | 3246 | 3277 | 3780..=3799 => Terrain::Wood,
        // ⚠️ 这一大段里**只有每 25 个一组**是木头，其余是土地 —— 原版就是这个意思
        3825..=4434 => {
            if (bidx - 3825) % 25 == 0 {
                Terrain::Wood
            } else {
                Terrain::Ground
            }
        }
        // 房间里 / 水中
        2075..=2099 | 2125..=2149 => Terrain::Room,
        1800..=1824 => Terrain::Water,
        _ => Terrain::Ground,
    };

    // 石路 / 洞穴通道：按 25 一组交替（原版的两个补充判定）
    if (825..=1349).contains(&bidx) && ((bidx - 825) / 25) % 2 == 0 {
        t = Terrain::Stone;
    }
    if (1375..=1799).contains(&bidx) && ((bidx - 1375) / 25) % 2 == 0 {
        t = Terrain::Cave;
    }
    if matches!(bidx, 1385 | 1386 | 1391 | 1392) {
        t = Terrain::Wood;
    }

    // ---- 中间层 ----
    let m = (mid_img & 0x7FFF) as i32 - 1;
    if (0..=115).contains(&m) {
        t = Terrain::Ground;
    } else if (120..=124).contains(&m) {
        t = Terrain::Lawn;
    }

    // ---- 前景层 ----
    let f = (fr_img & 0x7FFF) as i32 - 1;
    if matches!(f, 221..=289 | 583..=658 | 1183..=1206 | 7163..=7295 | 7404..=7414) {
        t = Terrain::Stone;
    } else if matches!(f, 3125..=3267 | 3757..=3948 | 6030..=6999) {
        t = Terrain::Wood;
    } else if (3316..=3589).contains(&f) {
        t = Terrain::Room;
    }

    t
}

// ---------- 攻击 / 被击中：按武器分类 ----------

/// 武器分类（原版用 `m_btWeapon div 2` 得到的那个"形状号"来判，`Actor.pas:2254-2261`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Weapon {
    Short,
    Wooden,
    Sword,
    /// `s_hit_do` —— 刀类。
    Blade,
    Axe,
    Club,
    Long,
    Fist,
}

/// 形状号 → 武器分类。**原版两组表（挥刀 50..57 与被击中 60..65）用的是同一套分类**
/// —— 只有"长兵器被击中"那一条例外，见 [`struck_weapon`]。
pub fn weapon(shape: u16) -> Weapon {
    match shape {
        6 | 20 => Weapon::Short,
        1 => Weapon::Wooden,
        2 | 13 | 9 | 5 | 14 | 22 => Weapon::Sword,
        4 | 17 | 10 | 15 | 16 | 23 => Weapon::Blade,
        3 | 7 | 11 => Weapon::Axe,
        24 => Weapon::Club,
        8 | 12 | 18 | 21 => Weapon::Long,
        _ => Weapon::Fist,
    }
}

/// 挥刀声（`Actor.pas:2254-2261` → 50..57）。
pub fn swing(shape: u16) -> u16 {
    match weapon(shape) {
        Weapon::Short => 50,
        Weapon::Wooden => 51,
        Weapon::Sword => 52,
        Weapon::Blade => 53,
        Weapon::Axe => 54,
        Weapon::Club => 55,
        Weapon::Long => 56,
        Weapon::Fist => 57,
    }
}

/// "被击中的兵器碰撞声"（`Actor.pas:2343-2349` → 60..65）。
///
/// ⚠️ 长兵器（`8,12,18,21`）这一条在原版里落到的是 **`s_struck_wooden`(61)**，
/// 而不是别的 xxx_long —— 照抄，别按[`weapon`]的统一分类"顺手修一下"。
pub fn struck_weapon(shape: u16) -> u16 {
    match weapon(shape) {
        Weapon::Short => 60,
        Weapon::Wooden => 61,
        Weapon::Sword => 62,
        Weapon::Blade => 63,
        Weapon::Axe => 64,
        Weapon::Club => 65,
        Weapon::Long => 61,
        Weapon::Fist => 61,
    }
}

/// "打在身上 / 打在甲上"的钝声（`Actor.pas:2277-2289` → 70..73 / 80..83）。
///
/// `armored` = 挨打的人穿的是甲（原版 `m_btDress div 2 == 3`）。
pub fn struck_body(attacker_shape: u16, armored: bool) -> u16 {
    let base = if armored { 80 } else { 70 };
    base + match weapon(attacker_shape) {
        // 短兵器与"剑类"是一组（原版这两组的数字列表不同，但结果同值）
        Weapon::Short | Weapon::Wooden | Weapon::Sword | Weapon::Blade => 0,
        Weapon::Axe => 1,
        Weapon::Long => 2,
        Weapon::Club | Weapon::Fist => 3,
    }
}

// ---------- 受伤 / 死亡 / 技能 / 怪物 ----------

/// 被击中的惨叫声（`Actor.pas:2243-2247`）。`sex`：`0` 男、`1` 女。
pub fn scream(sex: u8) -> u16 {
    if sex == 0 {
        idx::MAN_STRUCK
    } else {
        idx::WOM_STRUCK
    }
}

/// 死亡声（`Actor.pas:2243-2247`）。
pub fn die(sex: u8) -> u16 {
    if sex == 0 {
        idx::MAN_DIE
    } else {
        idx::WOM_DIE
    }
}

/// 技能音的**三段**（`Actor.pas:2297-2302`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MagicStage {
    /// 起手（`SM_SPELL`，`Actor.pas:2385-2388`）。
    Start = 0,
    /// 飞行/命中。
    Fire = 1,
    /// 爆炸（`magiceff.pas:797/1344`）。
    Explosion = 2,
}

/// 技能音编号：`10000 + 技能号*10 + 段`（原版线性推出，没有独立的技能音名表）。
pub fn magic(serial: u16, stage: MagicStage) -> u16 {
    10000 + serial * 10 + stage as u16
}

/// 怪物音的**七段**（`Actor.pas:2325-2331`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MonsterSound {
    Appear = 0,
    /// 走动/转身（原版 1/8 概率，`Actor.pas:2446`）。
    Normal = 1,
    Attack = 2,
    /// 怪物的"兵器"声。
    Weapon = 3,
    Scream = 4,
    Die = 5,
    /// 死亡第二声（只有 `appr=80` 那类用，`Actor.pas:2457-2466`）。
    Die2 = 6,
}

/// 怪物音编号：`200 + 外观号*10 + 段`（`Actor.pas:2325-2331`）。
pub fn monster(appr: u16, kind: MonsterSound) -> u16 {
    200 + appr * 10 + kind as u16
}

// ---------- BGM ----------

/// 登录界面的 BGM（`SoundUtil.pas:31`，`IntroScn.pas:518` 播、循环）。
/// 登录界面的循环 BGM（**用户指定**，2026-10-08）。
///
/// 口径：「吹箫 / 服务器选择到登录，背景音 `main_theme`」。
///
/// ⚠️ 与原版的差别记在这里，免得以后有人翻代码时以为抄错了：原版登录场景播的是
/// `PlayBGM(bmg_intro)` = `log-in-long2.wav`（`IntroScn.pas:518` + `SoundUtil.pas:31`），
/// 而 `main_theme.wav` 原版**从不播**（播它的定时器被注释掉了，`PlayScn.pas:485-486`）。
/// 用户要这首 ⇒ 它在打包器的 BGM 白名单里（`tools/wavpack/main.go` 的 `bgmNames`），
/// 容器里有一份 PCM 原样（改回原版只需把这里换成 `log-in-long2.wav`）。
pub const BGM_LOGIN: &str = "main_theme.wav";
/// 选角界面的 BGM（`SoundUtil.pas:32`，`IntroScn.pas:1152`）。
pub const BGM_SELECT: &str = "sellect-loop2.wav";
/// 自己死亡时的 BGM（`SoundUtil.pas:34`，`Actor.pas:2374`）。
pub const BGM_GAMEOVER: &str = "game over2.wav";

/// 进图音乐的文件名：`Music/<地图音乐编号>.mp3`（`SoundUtil.pas:219`）。
///
/// ⚠️ 编号是**服务端下发**的（原版 `SM_MAPDESCRIPTION.Recog` → `ClMain.pas:5222`）。
/// 我们协议里 `MapDescription` 目前**没有**这个字段、手上也**没有任何 mp3**
/// ⇒ 这条路暂时没有声音（见 `Library::missing_map_music` 的实测）。
pub fn map_music(number: i32) -> Option<String> {
    (number > 0).then(|| format!("Music/{number}.mp3"))
}

// ---------- 资产：一个容器（或退化为目录） ----------

/// 把文件名变成**容器内的键**。
///
/// 规则与写侧（Go `tools/wavpack` 的 `assetKey`）**逐条一致**：取 basename、
/// 砍掉最后一个扩展名、转小写、把 `[a-z0-9_~-]` 之外的字符换成 `_`
/// （`Game over2.wav` → `game_over2`）。
///
/// 这条规则是"把音频打成一个文件"的**主要收益之一**：客户端集里那类
/// "清单写小写、文件写大写"的坑（`game-over2.wav` vs `Game-over2.wav`）
/// 在容器里根本不存在 —— 键只有一个。
pub fn asset_key(name: &str) -> String {
    let base = name.rsplit(['/', '\\']).next().unwrap_or(name);
    let stem = match base.rfind('.') {
        Some(i) if i > 0 => &base[..i],
        _ => base,
    };
    stem.chars()
        .map(|c| {
            let c = c.to_ascii_lowercase();
            match c {
                'a'..='z' | '0'..='9' | '_' | '-' | '~' => c,
                _ => '_',
            }
        })
        .collect()
}

/// 编号表：`sound.lst` 里的"编号 → 文件名"（原版 `SoundUtil.pas:151-178`）。
///
/// 只保存**清单里写的文件名**（不解析成路径）：真正取字节由 [`SoundBank`] 负责 ——
/// 容器里按 [`asset_key`] 查、目录里按文件名找。这样同一张表两地都能用。
#[derive(Debug, Default, Clone)]
pub struct Library {
    names: HashMap<u16, String>,
    /// 清单里有多少行没解析出来（坏行/注释之外的东西）—— 起服务时打一行。
    skipped: usize,
}

impl Library {
    /// 解析 `sound.lst` 的字节：`;` 开头是注释，行首是编号，其余是路径
    ///（分隔符 `:`/空格/制表符，与原版 `GetValidStr3` 一致）。
    ///
    /// ⚠️ 刻意**不查磁盘**：容器里没有"文件是否存在"这回事（条目就是存在）。
    pub fn parse(list: &[u8]) -> Library {
        let text = String::from_utf8_lossy(list);
        let mut lib = Library::default();
        for line in text.lines() {
            let line = line.trim();
            if line.is_empty() || line.starts_with(';') {
                continue;
            }
            let Some(i) = line.find([':', ' ', '\t']) else {
                lib.skipped += 1;
                continue;
            };
            let Ok(number) = line[..i].trim().parse::<u16>() else {
                lib.skipped += 1;
                continue;
            };
            let rest = line[i + 1..].trim();
            let name = rest.rsplit(['\\', '/']).next().unwrap_or(rest).trim();
            if name.is_empty() || name.contains("..") {
                lib.skipped += 1;
                continue;
            }
            lib.names.insert(number, name.to_string());
        }
        lib
    }

    /// 清单里这一编号对应的**文件名**。
    pub fn name_of(&self, number: u16) -> Option<&str> {
        self.names.get(&number).map(|s| s.as_str())
    }

    /// 清单里能查到名字的编号数。
    pub fn len(&self) -> usize {
        self.names.len()
    }

    pub fn is_empty(&self) -> bool {
        self.names.is_empty()
    }

    /// 解析不出来的行数。
    pub fn skipped(&self) -> usize {
        self.skipped
    }
}

/// 音频资产的取字节入口：**容器优先**（一个文件），目录退化（原始素材/调试）。
///
/// 容器由 `tools/wavpack/build.sh` 产出（M2PK，`kind=audio` / `codec=store`），
/// 里面装着所有 wav 与编号表（键 `soundlist`）。
pub enum SoundBank {
    /// 一个容器文件里的全部音频。
    Container { archive: Archive },
    /// 一个目录里的散装 wav（原始客户端集，或 `-codec pcm` 时代的旧产物）。
    Dir {
        /// 小写文件名 → 路径（大小写不敏感的查找表，避免 Linux 上被文件名大小写坑到）。
        files: HashMap<String, PathBuf>,
    },
}

impl SoundBank {
    /// 按 `paths` 的约定打开：容器优先，其次目录。
    pub fn open() -> Option<SoundBank> {
        if let Some(p) = crate::paths::audio_container() {
            if let Ok(b) = SoundBank::open_container(&p) {
                return Some(b);
            }
        }
        crate::paths::audio_dir().and_then(|d| SoundBank::open_dir(&d))
    }

    /// 打开一个音频容器。
    pub fn open_container(path: &Path) -> std::io::Result<SoundBank> {
        Ok(SoundBank::Container {
            archive: Archive::open(path)?,
        })
    }

    /// 扫一个目录里的 `*.wav`（大小写不敏感的文件名表）。
    pub fn open_dir(dir: &Path) -> Option<SoundBank> {
        let entries = std::fs::read_dir(dir).ok()?;
        let mut files = HashMap::new();
        for e in entries.flatten() {
            let name = e.file_name().to_string_lossy().into_owned();
            if name.to_ascii_lowercase().ends_with(".wav") {
                files.insert(name.to_ascii_lowercase(), e.path());
            }
        }
        (!files.is_empty()).then_some(SoundBank::Dir { files })
    }

    /// 编号表：容器里的 `soundlist`，或目录里的 `sound.lst`。
    pub fn library(&self) -> Option<Library> {
        match self {
            SoundBank::Container { archive } => {
                let bytes = archive.read_name("soundlist").ok().flatten()?;
                Some(Library::parse(&bytes))
            }
            SoundBank::Dir { .. } => {
                let dir = crate::paths::audio_dir()?;
                let bytes = std::fs::read(dir.join("sound.lst")).ok()?;
                Some(Library::parse(&bytes))
            }
        }
    }

    /// 按**清单里的文件名**取字节。
    pub fn data_name(&self, name: &str) -> Option<Vec<u8>> {
        match self {
            SoundBank::Container { archive } => archive.read_name(&asset_key(name)).ok().flatten(),
            SoundBank::Dir { files } => {
                // 容器有规范化键，目录没有 ⇒ 这里**大小写不敏感**地找一遍
                // （客户端集里 `Game-over2.wav` 与清单的 `game-over2.wav` 就是这么错开的）
                let key = name
                    .rsplit(['\\', '/'])
                    .next()
                    .unwrap_or(name)
                    .to_ascii_lowercase();
                std::fs::read(files.get(&key)?).ok()
            }
        }
    }

    /// 按编号取字节。
    pub fn data(&self, library: &Library, number: u16) -> Option<Vec<u8>> {
        self.data_name(library.name_of(number)?)
    }

    /// 给终端打的一行"我在用哪一套"。
    pub fn describe(&self) -> String {
        match self {
            SoundBank::Container { archive } => format!(
                "容器 {} 块（kind={} codec={}）",
                archive.len(),
                archive.kind(),
                archive.codec()
            ),
            SoundBank::Dir { files } => format!("目录 {} 个 wav", files.len()),
        }
    }
}

/// 音频资产包：**取字节的两件套**（容器/目录 + 编号表）。
///
/// 抽出来是为了让调用方（app / e2e）少写两次 "Option 展开 + 查表 + 规范化" ——
/// 那三件事每次都要做对，尤其是 [`asset_key`] 那一步（容器里靠它查）。
pub struct SoundAssets {
    pub bank: SoundBank,
    pub library: Library,
}

impl SoundAssets {
    /// 按 `paths` 的约定打开（容器优先，其次目录）。取不到编号表 ⇒ `None`。
    pub fn open() -> Option<SoundAssets> {
        let bank = SoundBank::open()?;
        let library = bank.library()?;
        Some(SoundAssets { bank, library })
    }

    /// 按原版编号取字节，同时给出**容器里的键**（调用方拿它做缓存/去重的键）。
    pub fn bytes(&self, number: u16) -> Option<(String, Vec<u8>)> {
        let name = self.library.name_of(number)?;
        self.bytes_name(name)
    }

    /// 按文件名取字节（BGM 用的是写死的文件名，不是编号）。
    pub fn bytes_name(&self, name: &str) -> Option<(String, Vec<u8>)> {
        let data = self.bank.data_name(name)?;
        Some((asset_key(name), data))
    }

    /// 能取到字节的编号数（起着服务时打一行，方便"以为有声音其实没装全"）。
    pub fn available(&self) -> usize {
        (0..=u16::MAX)
            .filter(|n| self.library.name_of(*n).is_some())
            .filter(|n| self.bank.data(&self.library, *n).is_some())
            .count()
    }

    /// 给终端打的一行。
    pub fn describe(&self) -> String {
        format!(
            "{}，编号表 {} 条（能取到 {} 条）",
            self.bank.describe(),
            self.library.len(),
            self.available()
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 脚步：地形 + 走/跑 + 两只脚 —— 原版是"基号、跑 +2、第二只脚 +1"
    /// （`Actor.pas:2237-2238`、`2659-2660`）。
    #[test]
    fn 脚步编号() {
        // 土地：走 1/2，跑 3/4
        assert_eq!(footstep(Terrain::Ground, false, false), 1);
        assert_eq!(footstep(Terrain::Ground, false, true), 2);
        assert_eq!(footstep(Terrain::Ground, true, false), 3);
        assert_eq!(footstep(Terrain::Ground, true, true), 4);
        // 石头 5..8、水面 29..32（原版常量表）
        assert_eq!(footstep(Terrain::Stone, false, false), 5);
        assert_eq!(footstep(Terrain::Water, true, true), 32);
        // 八组基号互不重叠，且全部落在 1..32 内
        let mut bases: Vec<u16> = [
            Terrain::Ground,
            Terrain::Stone,
            Terrain::Lawn,
            Terrain::Rough,
            Terrain::Wood,
            Terrain::Cave,
            Terrain::Room,
            Terrain::Water,
        ]
        .iter()
        .map(|t| t.walk_left())
        .collect();
        bases.sort_unstable();
        assert_eq!(bases, vec![1, 5, 9, 13, 17, 21, 25, 29]);
        assert_eq!(footstep(Terrain::Water, true, true), 32);
    }

    /// 地形判定：几个**原版区间**的代表值，以及"三层依次覆盖"的语义。
    #[test]
    fn 地形判定() {
        // 地表：330..349 是草地（`Actor.pas:2152-2156`）→ 图号 = 330 + 1（原版减 1）
        assert_eq!(terrain(331, 0, 0, 0), Terrain::Lawn);
        // 250..254 粗糙
        assert_eq!(terrain(251, 0, 0, 0), Terrain::Rough);
        // 605..609 石头 = 图号 606
        assert_eq!(terrain(606, 0, 0, 0), Terrain::Stone);
        // 1800..1824 水 = 图号 1801
        assert_eq!(terrain(1801, 0, 0, 0), Terrain::Water);
        // 默认（不在任何区间）是土地
        assert_eq!(terrain(5000, 0, 0, 0), Terrain::Ground);
        // 区域号参与：`区域*10000 + 图号 - 1`（`Actor.pas:2150`）
        assert_eq!(
            terrain(331, 1, 0, 0),
            Terrain::Ground,
            "区域 1 落到 10000+330"
        );
        // 石路交替：825..1349 里每 25 一组（`Actor.pas:2199-2203`）
        assert_eq!(terrain(826, 0, 0, 0), Terrain::Stone, "825 这一组是石头");
        assert_eq!(terrain(851, 0, 0, 0), Terrain::Ground, "下一组回到土地");
        // 洞穴通道交替：1375..1799（`Actor.pas:2205-2209`）
        assert_eq!(terrain(1376, 0, 0, 0), Terrain::Cave);
        // 木头那四个特例（`Actor.pas:2211-2214`）
        assert_eq!(terrain(1386, 0, 0, 0), Terrain::Wood);
        // 中间层覆盖：0..115 → 土地（`Actor.pas:2220-2225`）
        assert_eq!(terrain(331, 0, 3, 0), Terrain::Ground, "中间层把它拉回土地");
        // 中间层 120..124 → 草地
        assert_eq!(terrain(5000, 0, 121, 0), Terrain::Lawn);
        // 前景层覆盖：221..289 → 石头（`Actor.pas:2229-2236`）。
        // ⚠️ 判的是**减 1 之后**的值（原版 `bidx := wFrImg and $7FFF; bidx := bidx - 1`），
        // 所以区间 221..289 对应的图号是 222..290。
        assert_eq!(terrain(5000, 0, 0, 250), Terrain::Stone);
        // 前景层 3125..3267 → 木头
        assert_eq!(terrain(5000, 0, 0, 3200), Terrain::Wood);
        // 前景层 3316..3589 → 房间
        assert_eq!(terrain(5000, 0, 0, 3400), Terrain::Room);
    }

    /// 武器的两组表：号码与**原版列表逐条对齐**（`Actor.pas:2254-2261`、`2343-2349`）。
    #[test]
    fn 武器音() {
        // 挥刀：短/木/剑/刀/斧/棒/长/拳
        for (shape, want) in [
            (6u16, 50u16),
            (20, 50),
            (1, 51),
            (2, 52),
            (13, 52),
            (9, 52),
            (5, 52),
            (14, 52),
            (22, 52),
            (4, 53),
            (17, 53),
            (10, 53),
            (15, 53),
            (16, 53),
            (23, 53),
            (3, 54),
            (7, 54),
            (11, 54),
            (24, 55),
            (8, 56),
            (12, 56),
            (18, 56),
            (21, 56),
            (0, 57),
            (99, 57),
        ] {
            assert_eq!(swing(shape), want, "挥刀：形状 {shape}");
        }
        // 被击中的兵器声：长兵器落回 61（原版的"s_struck_wooden"）
        assert_eq!(struck_weapon(6), 60);
        assert_eq!(struck_weapon(1), 61);
        assert_eq!(struck_weapon(2), 62);
        assert_eq!(struck_weapon(4), 63);
        assert_eq!(struck_weapon(3), 64);
        assert_eq!(struck_weapon(24), 65);
        assert_eq!(struck_weapon(8), 61, "长兵器在原版里就是木头的碰撞声");
        assert_eq!(struck_weapon(0), 61, "拳脚也是");
    }

    /// 打在甲上 / 打在布衣上：两组 70..73 与 80..83（`Actor.pas:2277-2289`）。
    #[test]
    fn 打在身上与甲上() {
        assert_eq!(struck_body(2, false), 70);
        assert_eq!(struck_body(3, false), 71);
        assert_eq!(struck_body(8, false), 72);
        assert_eq!(struck_body(24, false), 73);
        assert_eq!(struck_body(2, true), 80);
        assert_eq!(struck_body(3, true), 81);
        assert_eq!(struck_body(8, true), 82);
        assert_eq!(struck_body(99, true), 83, "拳脚");
    }

    /// 性别相关的受伤/死亡、技能、怪物编号。
    #[test]
    fn 受伤死亡技能怪物() {
        assert_eq!(scream(0), 138);
        assert_eq!(scream(1), 139);
        assert_eq!(die(0), 144);
        assert_eq!(die(1), 145);
        // 技能：10000 + 号*10 + 段
        assert_eq!(magic(1, MagicStage::Start), 10010);
        assert_eq!(magic(1, MagicStage::Fire), 10011);
        assert_eq!(magic(1, MagicStage::Explosion), 10012);
        assert_eq!(magic(33, MagicStage::Explosion), 10332);
        // 怪物：200 + 外观*10 + 段
        assert_eq!(monster(0, MonsterSound::Appear), 200);
        assert_eq!(monster(1, MonsterSound::Die), 215);
        assert_eq!(monster(80, MonsterSound::Die2), 1006);
    }

    /// `sound.lst` 的解析：注释、CRLF、`wav\` 前缀、坏行。
    ///
    /// ⚠️ 现在**不查磁盘**了（容器里没有"文件是否存在"这回事，条目就是存在）；
    /// 取字节由 [`SoundBank`] 负责。
    #[test]
    fn 清单解析() {
        // ⚠️ 原版清单是 GBK + CRLF；解析只取 ASCII 部分，所以这里用字符串再取字节。
        let list = "; 这是注释\r\n103:\twav\\103.wav\r\n1: wav\\1.wav\r\nbad line\r\n104: wav\\104.wav\r\n";
        let lib = Library::parse(list.as_bytes());
        assert_eq!(lib.len(), 3, "三条有效行");
        assert_eq!(lib.skipped(), 1, "bad line 解析不出来，要计数而不是静默");
        assert_eq!(lib.name_of(103), Some("103.wav"));
        assert_eq!(lib.name_of(1), Some("1.wav"), "路径前缀要剥掉");
        assert_eq!(lib.name_of(999), None);
    }

    /// 容器内的键：与写侧（Go `assetKey`）同规则 —— 大小写与非法字符都归一。
    #[test]
    fn 容器键() {
        assert_eq!(asset_key("103.wav"), "103");
        assert_eq!(asset_key("Game-over2.wav"), "game-over2");
        assert_eq!(
            asset_key("game-over2.wav"),
            "game-over2",
            "大小写归一 ⇒ 同一个键"
        );
        assert_eq!(asset_key("Game over2.wav"), "game_over2", "空格换成下划线");
        assert_eq!(asset_key("M55-2.WAV"), "m55-2");
        assert_eq!(asset_key("wav\\1370-2-1.wav"), "1370-2-1");
    }

    /// BGM 与进图音乐的文件名（`SoundUtil.pas:31-34`、`:219`）。
    #[test]
    fn 音乐文件名() {
        // 用户指定（见 `BGM_LOGIN` 的说明：原版是 log-in-long2）。
        assert_eq!(BGM_LOGIN, "main_theme.wav");
        assert_eq!(BGM_SELECT, "sellect-loop2.wav");
        assert_eq!(BGM_GAMEOVER, "game over2.wav");
        assert_eq!(map_music(7).as_deref(), Some("Music/7.mp3"));
        assert_eq!(map_music(0), None, "0 = 没有音乐");
        assert_eq!(map_music(-1), None, "-1 = 停");
    }

    /// **真素材验收（端到端）**：从音频资产里取字节 → 用 [`crate::wave`] 解码。
    ///
    /// 这条同时**跨语言验证**了两件事：容器是 Go 写的（`tools/wavpack`）、
    /// ADPCM 是 Go 编的，而这里是 Rust 读 + Rust 解 —— 任何一边写歪了，
    /// 这里的帧数/峰值/时长都会立刻不对。
    ///
    /// 门控同别处：没装资产（`assets/audio` 与 `mir2c/wav` 都没有）就跳过。
    #[test]
    fn 真素材_音效清单与关键编号() {
        let Some(bank) = SoundBank::open() else {
            eprintln!("跳过：没找到音频资产（先跑 tools/wavpack/build.sh）");
            return;
        };
        let Some(lib) = bank.library() else {
            panic!("有资产却读不到编号表（容器里的 soundlist / 目录里的 sound.lst）");
        };
        eprintln!("音频资产：{}，编号表 {} 条", bank.describe(), lib.len());
        assert!(
            lib.len() > 700,
            "原版清单有一千多条，手上该有 700+ 个能播的编号"
        );

        // 我们接上的那些编号，一条条都要**真的解得出声音**
        for (n, why) in [
            (idx::NORM_BUTTON_CLICK, "UI 按钮"),
            (idx::ROCK_DOOR_OPEN, "开门"),
            (idx::MELTSTONE, "选角解冻"),
            (idx::MONEY, "金币"),
            (idx::MAN_STRUCK, "男受伤"),
            (idx::WOM_STRUCK, "女受伤"),
            (idx::MAN_DIE, "男死"),
            (idx::WOM_DIE, "女死"),
            (swing(2), "挥剑"),
            (swing(0), "挥拳"),
            (struck_body(2, true), "打甲上"),
            (footstep(Terrain::Water, true, true), "水里跑（第二只脚）"),
            (magic(1, MagicStage::Start), "技能起手"),
        ] {
            let bytes = bank
                .data(&lib, n)
                .unwrap_or_else(|| panic!("编号 {n}（{why}）取不到字节"));
            let pcm = crate::wave::decode(&bytes)
                .unwrap_or_else(|e| panic!("编号 {n}（{why}）解码失败：{e}"));
            assert!(pcm.frames() > 0, "编号 {n}（{why}）解出来是空的");
            assert!(pcm.peak() > 655, "编号 {n}（{why}）峰值只有 {}", pcm.peak());
            assert!(
                pcm.rate >= 8000 && pcm.rate <= 48000,
                "编号 {n} 采样率 {} 离谱",
                pcm.rate
            );
        }

        // 三首场景 BGM（原版写死的文件名）：能从资产里取到、解得出、且是"一首歌"的长度
        for bgm in [BGM_LOGIN, BGM_SELECT, BGM_GAMEOVER] {
            let bytes = bank
                .data_name(bgm)
                .unwrap_or_else(|| panic!("取不到 BGM：{bgm}"));
            let pcm = crate::wave::decode(&bytes).unwrap_or_else(|e| panic!("{bgm} 解码失败：{e}"));
            let secs = pcm.frames() as f32 / pcm.rate as f32;
            assert!(secs > 5.0, "{bgm} 只有 {secs:.1}s，不像一首 BGM");
        }

        // `fact` 截断：ADPCM 最后一块会补零，解码器必须按真实帧数截断。
        // 这里自己解析同一个字节流里 `fact` 的值，与解码结果的帧数对上。
        let bytes = bank.data(&lib, idx::NORM_BUTTON_CLICK).unwrap();
        let fact = fact_frames(&bytes);
        if let Some(f) = fact {
            let pcm = crate::wave::decode(&bytes).unwrap();
            assert_eq!(
                pcm.frames(),
                f,
                "解码帧数必须等于 fact（否则最后一块的补零混进来了）"
            );
            eprintln!("fact 截断检查 ✓（{f} 帧）");
        }

        // 进图音乐：**素材里根本没有 mp3**（客户端集只有 wav/），这条路没有素材。
        // 不断言"必须没有"（真补了资产这条就该变），只把现状打出来。
        eprintln!(
            "进图音乐 {}：素材不存在（客户端集里没有 mp3，协议里也还没有地图音乐号）",
            map_music(1).unwrap()
        );
    }

    /// 从 wav 字节里取 `fact` 段声明的帧数（压缩格式的真实长度）。
    fn fact_frames(bytes: &[u8]) -> Option<usize> {
        let mut off = 12usize;
        while off + 8 <= bytes.len() {
            let id = &bytes[off..off + 4];
            let size = u32::from_le_bytes([
                bytes[off + 4],
                bytes[off + 5],
                bytes[off + 6],
                bytes[off + 7],
            ]) as usize;
            if id == b"fact" && size >= 4 {
                let b = off + 8;
                return Some(
                    u32::from_le_bytes([bytes[b], bytes[b + 1], bytes[b + 2], bytes[b + 3]])
                        as usize,
                );
            }
            off = off + 8 + size + (size & 1);
        }
        None
    }
}

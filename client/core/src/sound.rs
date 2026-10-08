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
pub const BGM_LOGIN: &str = "log-in-long2.wav";
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

// ---------- `sound.lst`：编号 → 文件 ----------

/// 音效库：`sound.lst`（`<编号>: wav\X.wav`）解析出来的"编号 → 文件"。
///
/// 原版是**外部文本**驱动的（`SoundUtil.pas:151-178`）：`;` 开头是注释，行首是编号，
/// 其余是路径（分隔符 `:`/空格/制表符）。路径是**相对游戏根目录**的（`wav\X.wav`），
/// 而我们 `sound.lst` 与那 778 个 wav **同在一个目录**里 ⇒ 这里只取文件名，
/// 拼到该目录上。
#[derive(Debug, Default)]
pub struct Library {
    files: HashMap<u16, PathBuf>,
    /// `sound.lst` 里有、但磁盘上没有的编号（统计用；原版每次播放都要 `FileExists`，
    /// 我们装载时查一次就够）。
    missing: usize,
}

impl Library {
    /// 从 `sound.lst` 的**字节**解析（不碰磁盘，方便单测）。
    ///
    /// `wav_dir` = `sound.lst` 与那堆 wav 所在目录。
    pub fn parse(list: &[u8], wav_dir: &Path) -> Library {
        let text = String::from_utf8_lossy(list);
        let mut lib = Library::default();
        for line in text.lines() {
            let line = line.trim(); // 原版清单是 CRLF，`lines()` 已剥 `\r`
            if line.is_empty() || line.starts_with(';') {
                continue;
            }
            // 原版用 `GetValidStr3(str, data, [':', ' ', #9])`：第一个 token 是编号，
            // 剩下的是路径。
            let (head, rest) = match line.find([':', ' ', '\t']) {
                Some(i) => (&line[..i], line[i + 1..].trim()),
                None => continue,
            };
            let Ok(number) = head.trim().parse::<u16>() else {
                continue;
            };
            let Some(name) = file_name_of(rest) else {
                continue;
            };
            let path = wav_dir.join(name);
            if path.is_file() {
                lib.files.insert(number, path);
            } else {
                lib.missing += 1;
            }
        }
        lib
    }

    /// 从目录里的 `sound.lst` 装载。没有清单文件就 `None`（调用方降级）。
    pub fn load(wav_dir: &Path) -> Option<Library> {
        let list = std::fs::read(wav_dir.join("sound.lst")).ok()?;
        Some(Library::parse(&list, wav_dir))
    }

    /// 该编号对应的文件（**已确认存在**）。原版 `PlaySound` 就先查这个
    /// （`SoundUtil.pas:183-186`）。
    pub fn playable(&self, number: u16) -> Option<&Path> {
        self.files.get(&number).map(|p| p.as_path())
    }

    /// 能播的编号数。
    pub fn len(&self) -> usize {
        self.files.len()
    }

    pub fn is_empty(&self) -> bool {
        self.files.is_empty()
    }

    /// 清单里有、磁盘上没有的条数（起服务时打一行，方便"以为有声音其实没装全"）。
    pub fn missing(&self) -> usize {
        self.missing
    }
}

/// 从 `wav\103.wav` / `wav/103.wav` 这种清单里取**文件名**。
///
/// 原版路径是相对**游戏根目录**的（`.\wav\...`），而我们的 wav 与该清单同目录，
/// 所以只留文件名；带目录分隔的（`..\`）一律不认 —— 不让清单指到别处去。
fn file_name_of(entry: &str) -> Option<&str> {
    // `..` 要拦在**整条**路径上：`..\..\evil.wav` 的最后一段是干净的 `evil.wav`
    if entry.contains("..") {
        return None;
    }
    let last = entry.rsplit(['\\', '/']).next()?.trim();
    if last.is_empty() {
        return None;
    }
    Some(last)
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
    #[test]
    fn 清单解析() {
        let dir = std::env::temp_dir().join("mir2-sound-清单解析");
        let _ = std::fs::create_dir_all(&dir);
        std::fs::write(dir.join("103.wav"), b"RIFF").unwrap();

        // ⚠️ 原版清单是 GBK + CRLF；解析只认 ASCII 部分，所以这里用字符串再取字节。
        let list = "; 这是注释\r\n103:\twav\\103.wav\r\n1: wav\\1.wav\r\nbad line\r\n104: wav\\104.wav\r\n";
        let lib = Library::parse(list.as_bytes(), &dir);

        // 103 存在 ⇒ 收录；1/104 不存在 ⇒ 只计数
        assert_eq!(lib.len(), 1);
        assert_eq!(lib.missing(), 2);
        assert_eq!(lib.playable(103).unwrap().file_name().unwrap(), "103.wav");
        assert!(
            lib.playable(1).is_none(),
            "磁盘上没有的不给播（原版也是先 FileExists）"
        );
        assert!(lib.playable(999).is_none());
        // 越界的目录（`..\`）不认，不让清单指到别处
        assert!(file_name_of("..\\..\\evil.wav").is_none());
        assert_eq!(file_name_of("wav\\M100-0.wav"), Some("M100-0.wav"));
    }

    /// BGM 与进图音乐的文件名（`SoundUtil.pas:31-34`、`:219`）。
    #[test]
    fn 音乐文件名() {
        assert_eq!(BGM_LOGIN, "log-in-long2.wav");
        assert_eq!(BGM_SELECT, "sellect-loop2.wav");
        assert_eq!(BGM_GAMEOVER, "game over2.wav");
        assert_eq!(map_music(7).as_deref(), Some("Music/7.mp3"));
        assert_eq!(map_music(0), None, "0 = 没有音乐");
        assert_eq!(map_music(-1), None, "-1 = 停");
    }

    /// **真素材验收**：手上的 `mir2c/wav` 真能对上原版这套编号。
    ///
    /// 门控是 `MIR2C_DATA`（同 `login_ui.rs` / `select_ui.rs`）：没装素材的机器跳过。
    #[test]
    fn 真素材_音效清单与关键编号() {
        let Some(dir) = crate::paths::audio_dir() else {
            eprintln!("跳过：没找到音频目录（设 MIR2C_DATA 或 MIR2_ASSET_DIR）");
            return;
        };
        let lib = Library::load(&dir).expect("音频目录里该有 sound.lst");
        eprintln!(
            "音效库：{}（可播 {} 条，清单里有 {} 条缺文件）",
            dir.display(),
            lib.len(),
            lib.missing()
        );
        assert!(lib.len() > 700, "原版清单有一千多条，手上该有 778 个 wav");
        // 我们接上的那些编号，一条条都要真的能播
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
            assert!(
                lib.playable(n).is_some(),
                "编号 {n}（{why}）在清单里没有可播的文件"
            );
        }
        // 三个场景 BGM（原版写死的文件名，同目录）
        for bgm in [BGM_LOGIN, BGM_SELECT, BGM_GAMEOVER] {
            assert!(dir.join(bgm).is_file(), "缺少 BGM：{bgm}");
        }
        // 进图音乐：**实测过没有** —— 手上一个 mp3 都没有。
        // 不断言"必须没有"（以后补了资产这条就会变），只把现状打出来。
        let mp3 = map_music(1).unwrap();
        eprintln!(
            "进图音乐 {}：{}（协议里也还没有地图音乐号）",
            mp3,
            if dir.join(&mp3).is_file() {
                "在"
            } else {
                "没有 —— 这条路暂时没声音"
            }
        );
    }
}

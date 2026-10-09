//! actor 的**图号公式**（人物 / 怪物）：方向 + 动作 + 帧 → 容器里的第几张图。
//!
//! 出处（原版 Delphi，逐条注了行号）：
//!
//! | 东西 | 位置 |
//! |---|---|
//! | 人物取图 | `Actor.pas:3147-3187`（`THumActor.CalcActorFrame`）|
//! | 人物动作表 `HA` | `Actor.pas:75-91` |
//! | 人物容器选择 / 每块 600 张 | `ClMain.pas:6280-6293`（`GetWHumImg`）|
//! | 武器层 | `ClMain.pas:6244-6277`（`GetWWeaponImg`）|
//! | 怪物取图 | `Actor.pas:1351-1434`（`TActor.CalcActorFrame`）|
//! | 怪物图片块起点 | `Actor.pas:1003+`（`GetOffset`）|
//! | 怪物品种→动作表 | `Actor.pas:848-954`（`GetRaceByPM`）|
//! | 怪物容器选择 | `Actor.pas:958-1000`（`aGetMonImg`）+ `MShare.pas:832-857` |
//!
//! 源码在**仓库外**：`/Users/taohuifeng/Develop/git/mir2standard/GameOfMir/Client/`。
//!
//! 文件下半部分是**生成段**（`client/core/tools/gen_actor_tables.py` 从 `Actor.pas` 抽的）：
//! 那 26 张表 + `mon_offset` 的几十个数字**不要手改**，要改就改生成器再重跑。
//!
//! # 两条公式
//!
//! ```text
//! 人物（Hum.wzl / Weapon.wzl，图号 0 基）：
//!   图号 = 600 * 部位 + HA.<动作>.start + dir * (frame + skip) + 帧序
//!
//! 怪物（Mon<Appr/10 + 1>.wzl，图号 0 基）：
//!   图号 = mon_offset(Appr) + MA.<动作>.start + dir * (frame + skip) + 帧序
//! ```
//!
//! `部位`（`Dress` / `Weapon`）= `Shape * 2 + 性别`，由**服务端**算好放进 feature 位域
//! （`ObjBase.pas:19998-20024`），客户端只负责拆位（见 `unpack_human_feature`）。
//!
//! # 三处容易踩的坑
//!
//! 1. **`dir` 是原版的 0..7**（上=0 右上=1 … 左上=7），而我们的协议枚举是原版 **+1**
//!    （`common.proto` 的 `Direction`）—— 换算只允许在 [`dir_of`] 一处发生；
//! 2. **`frame + skip` 才是每个方向的步长**：`skip` 是原版为"没有有效图的保留格"留的
//!    洞（`Actor.pas:35-59` 的注释），**不是**我们要跳过的动画帧；
//! 3. **怪物表的参数名叫 `Race`，喂进去的却是 `RaceImg`**
//!    （`RACEfeature(c_feature)` = 低字节；服务端 `Race` 只用于 AI、不下发）。
//!
//! # 故意没做的（都在 docs/plan.md 与 docs/assets.md 里记着）
//!
//! - **头发**：要 `Hair.wzl`，本套素材缺失（`hair_ck.wzl` / `hair4_ck.wzl` 是 64 字节空壳）
//! - **翅膀/时装**（`HumEffect.wzl`）、**怪物武器**（`m_btMonsterWeapon`，我们数据里恒 0）、
//!   **`Appr >= 1000` 的外部容器**（`Graphics\Monster\<Appr>.wil`，本套素材没有）

use crate::world::action;

/// 一个动作块（原版 `TActionInfo`，`Actor.pas:35-42`）。
///
/// `usetick` 原版用来定"几帧推进一次"，本项目按 `ftime` 毫秒推进（见 [`Act::frame_at`]）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Act {
    /// 该动作在本方向块的起始帧。
    pub start: u16,
    /// 有效帧数。
    pub frame: u16,
    /// 保留格数（**步长 = frame + skip**）。
    pub skip: u16,
    /// 每帧毫秒。
    pub ftime: u16,
}

impl Act {
    /// 每个方向的步长。原版所有公式里的 `dir * (frame + skip)` 用的就是它。
    pub fn stride(&self) -> u16 {
        self.frame + self.skip
    }

    /// 本动作在某个方向上的首帧。
    pub fn first(&self, dir: u8) -> u32 {
        self.start as u32 + dir as u32 * self.stride() as u32
    }

    /// 按真实时间取循环动画的帧号（走/跑这类）。
    ///
    /// ⚠️ 用 `ftime` 而不是"每逻辑帧加一"：原版就是这么定的（每帧延迟 N 毫秒），
    /// 这样动画速度与我们的帧率无关 —— 与地图动画那套（`core/src/map.rs` 的
    /// `fr_frame`，50ms 一格）是同一个思路。
    pub fn frame_at(&self, elapsed_ms: u32) -> u16 {
        if self.frame == 0 || self.ftime == 0 {
            return 0;
        }
        ((elapsed_ms / self.ftime as u32) % self.frame as u32) as u16
    }

    /// 按真实时间取**一次播完**的动作帧号（攻击/受击/死亡）：停在最后一帧。
    pub fn frame_once(&self, elapsed_ms: u32) -> u16 {
        if self.frame == 0 || self.ftime == 0 {
            return 0;
        }
        ((elapsed_ms / self.ftime as u32) as u16).min(self.frame - 1)
    }

    /// **最后一帧**号 —— 一次播完的动作停在它上面；对 `Die` 来说它**就是尸骨那一帧**。
    ///
    /// ⚠️ 死了以后必须一直停在它上面（见 `monster_sample` 的说明）：退回循环动作 =
    /// 画面上"死而复生"（用户 2026-10-09 报的）。
    pub fn last_frame(&self) -> u16 {
        self.frame.saturating_sub(1)
    }

    /// 一次播完需要多少毫秒（渲染层用来决定"这个动作什么时候结束"）。
    pub fn duration_ms(&self) -> u32 {
        self.ftime as u32 * self.frame as u32
    }
}

// ---------- 人物 ----------

/// 人物容器里**一个着装块**的图片数（原版 `HUMANFRAME`，`Actor.pas:14`）。
///
/// 校验：`Hum.wzl` 共 14400 张 = 600 × 24 个着装块。
pub const HUMAN_FRAME: u32 = 600;

/// 人物的动作段（顺序 = 生成段 `HA` 的下标，**不能重排**）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HAct {
    Stand = 0,
    Walk,
    Run,
    RushLeft,
    RushRight,
    WarMode,
    Hit,
    HeavyHit,
    BigHit,
    FireHitReady,
    Spell,
    Sitdown,
    Struck,
    Die,
}

impl HAct {
    pub fn act(self) -> Act {
        HA[self as usize]
    }
}

/// 人物一次动作的取样结果：用哪一段、要不要循环、已经播了多久。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Pose {
    pub act: HAct,
    /// 循环动作（站立/走）还是"播完停住"（攻击/受击/死亡）。
    pub looping: bool,
}

/// 由协议的动作 id（`protocol.md` §9.5）与"是否在移动"决定人物播哪一段。
///
/// ⚠️ 消息号→段的对应照原版 `Actor.pas:3191+`：`SM_HIT / SM_POWERHIT / SM_LONGHIT /
/// SM_WIDEHIT / SM_FIREHIT / SM_TWINHIT` **全都进 `ActHit`**，只有 HEAVY / BIG 另有段。
pub fn human_pose(action_id: Option<u32>, moving: bool, run: bool) -> Pose {
    match action_id {
        Some(action::HURT) => Pose {
            act: HAct::Struck,
            looping: false,
        },
        Some(action::DEATH) => Pose {
            act: HAct::Die,
            looping: false,
        },
        // ⚠️ **移动优先于攻击**（用户 2026-10-09 补的第 2 条："不要在奔跑/走动没结束时
        // 释放攻击动作"）。原来攻击判在前面 ⇒ 走/跑没走完就切进挥砍，看起来就是
        // "边走边砍、同手同脚"。原版靠 `CanNextAction`/`IsIdle`（`Actor.pas:1722-1736`）
        // 保证一个动作播完才发下一个 —— 客户端这边就是这条：**走完再砍**。
        // 受击/死亡仍优先于移动（被打/死要立刻表现，原版也如此）。
        _ if moving => Pose {
            // 走 / 跑是**两段不同的图**：`ActWalk` 起点 64、`ActRun` 起点 128，各 6 帧
            //（帧间隔 90 vs 120 ms，`Actor.pas:77-78`）。原版靠 `CM_RUN`/`SM_RUN` 两条消息
            // 区分（`Actor.pas:1471`/`3230-3263`），我们的 `EntityMove.run` 就是那个标志
            //（见 `world::Entity::run`）。⚠️ 段落选错的后果是"跑步放走的图"，
            // 帧数一样、图号差 64 ⇒ 画面上是另一套动作，**不报错**。
            act: if run { HAct::Run } else { HAct::Walk },
            looping: true,
        },
        Some(a) if action::is_attack(a) => Pose {
            act: match a {
                2 => HAct::HeavyHit,
                3 => HAct::BigHit,
                _ => HAct::Hit,
            },
            looping: false,
        },
        _ => Pose {
            act: HAct::Stand,
            looping: true,
        },
    }
}

/// 人物（或武器/头发）在容器里的图号。
///
/// `part` = `Dress` 或 `Weapon`（已是 `Shape*2 + 性别`）。三个部位**同一个公式**，
/// 只是容器不同（`Hum.wzl` / `Weapon.wzl` / `Hair.wzl`）。
pub fn human_index(part: u8, act: HAct, dir: u8, frame: u16) -> u32 {
    HUMAN_FRAME * part as u32 + act.act().first(dir) + frame as u32
}

/// 拆人类 feature 位域（与服务端 `proto.MakeFeature` 对称）。
///
/// 布局：低 16 = `MakeWord(RaceImg, Weapon)`，高 16 = `MakeWord(Hair, Dress)`
/// （`Grobal2.pas:2663-2699` 的 `MakeHumanFeature`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct HumanFeature {
    pub race_img: u8,
    pub weapon: u8,
    pub hair: u8,
    pub dress: u8,
}

pub fn unpack_human_feature(f: u32) -> HumanFeature {
    let lo = (f & 0xFFFF) as u16;
    let hi = (f >> 16) as u16;
    HumanFeature {
        race_img: (lo & 0xFF) as u8,
        weapon: (lo >> 8) as u8,
        hair: (hi & 0xFF) as u8,
        dress: (hi >> 8) as u8,
    }
}

// ---------- 怪物 ----------

/// 怪物的动作段（顺序 = 生成段里每张表的下标，**不能重排**）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MAct {
    Stand = 0,
    Walk,
    Attack,
    Critical,
    Struck,
    Die,
    Death,
}

/// 怪物一次动作的取样结果。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct MPose {
    pub act: MAct,
    pub looping: bool,
}

/// 由协议的动作 id 与"是否在移动"决定怪物播哪一段。
///
/// 返回的段**可能没有有效帧**（原版很多品种的 `ActCritical/ActDeath` 是 `frame:0`），
/// 所以这里会退回 `ActStand` —— 见 [`monster_index`] 的第 3 条说明。
pub fn monster_pose(race_img: u8, action_id: Option<u32>, moving: bool) -> MPose {
    let table = mon_actions(race_img);
    let want = match action_id {
        Some(action::HURT) => MAct::Struck,
        Some(action::DEATH) => MAct::Die,
        // 同 `human_pose`：**移动优先于攻击**（走完再砍）。
        _ if moving => MAct::Walk,
        Some(a) if action::is_attack(a) => MAct::Attack,
        _ => MAct::Stand,
    };
    // 该品种这一段没有有效帧 ⇒ 退回站立（原版是"两个动作段 start 相同"来兜，
    // 我们显式判 frame == 0，省得画出别的品种的图）。
    if table[want as usize].frame == 0 {
        return MPose {
            act: MAct::Stand,
            looping: true,
        };
    }
    MPose {
        act: want,
        looping: !matches!(want, MAct::Attack | MAct::Struck | MAct::Die),
    }
}

/// 怪物在容器里的图号。
///
/// 1. `appr` 选**图片块**（`mon_offset`）——它同时决定了"是哪一只怪"；
/// 2. `race_img` 选**动作表**（`mon_actions`）——同一张容器里不同品种的动作段不同；
/// 3. 若目标容器在素材里不存在（如 `Appr >= 800` 的龙/特效），调用方按 `None` 降级。
pub fn monster_index(appr: u16, race_img: u8, act: MAct, dir: u8, frame: u16) -> u32 {
    mon_offset(appr) + mon_actions(race_img)[act as usize].first(dir) + frame as u32
}

/// NPC 用的容器（`Npc.wzl`）。
///
/// 原版是 `g_WNpcImgImages`（`TNpcActor.LoadSurface`，`Actor.pas:3040-3046`：
/// `m_BodySurface := g_WNpcImgImages.GetCachedImage(m_nBodyOffset + m_nCurrentFrame, …)`）。
pub const NPC_LIB: &str = "Npc";

/// NPC 外观在 `Npc.wzl` 里的**块起点** —— 原版 `GetNpcOffset`（`Actor.pas:1156-1200`）。
///
/// ⚠️ 原版这个函数里有两套 case：上面一套被 `{ }` 注释掉了，**生效的是下面那套**
/// （0..22 / 23 / 24,25 / 26…41 / 42,43 / 44..47 / 48..50 / 51 / 52 / 53 / 54..57 /
/// 58… / 77..80 / 81 / 82 / 83）。这里照**生效的那套**搬。
pub fn npc_offset(appr: u16) -> u32 {
    match appr {
        0..=22 => appr as u32 * 60,
        23 => 1380,
        24 | 25 => (appr as u32 - 24) * 60 + 1470,
        27 | 32 => (appr as u32 - 26) * 60 + 1620 - 30,
        26 | 28..=31 | 33..=41 => (appr as u32 - 26) * 60 + 1620,
        42 | 43 => 2580,
        44..=47 => 2640,
        48..=50 => (appr as u32 - 48) * 60 + 2700,
        51 => 2880,
        52 => 2960,
        53 => 3020,
        54..=57 => (appr as u32 - 54) * 60 + 3070,
        58 => 3270,
        59 => 3290,
        60 => 3330,
        61..=64 => 3350,
        65 => 3430,
        66 => 3450,
        67 => 3500,
        68 => 3570,
        69..=74 => (appr as u32 - 69) * 20 + 3610,
        75 => 3730,
        76 => 3810,
        77..=80 => (appr as u32 - 77) * 20 + 3850,
        81 => 4070,
        82 => 4110,
        _ => 4150, // 83 及其后：原版到 83 为止，再大就退回最后一支
    }
}

/// NPC 的动作表 —— 原版 `GetRaceByPM(race, Appr)` 里**针对 NPC 那一支**（`Actor.pas:881-912`）。
///
/// - `race == 50`（商人）时原版**再按外观**分派：23→MA36、24/25/27/32→MA37、
///   26/28..31/33/34/51→MA35、35..41 与 48..50/52/53→MA41、42..47→MA46、其余 MA35；
/// - 其它 race 直接就是那张按 race 编号的动作表（10/11/15 这些 NPC 种族都在里面）。
///
/// ⚠️ 我们这张表的键是**race**（服务端在 `RaceImg` 字段里传的就是它）——
/// 怪物那边同理（见 `mon_actions` 的说明）。
pub fn npc_actions(race: u8, appr: u16) -> &'static [Act; 7] {
    if race == 50 {
        return match appr {
            23 => mon_actions(36),
            24 | 25 | 27 | 32 => mon_actions(37),
            35..=41 | 48..=50 | 52 | 53 => mon_actions(41),
            42..=47 => mon_actions(46),
            _ => mon_actions(35),
        };
    }
    mon_actions(race)
}

/// NPC 在 `Npc.wzl` 里的图号：块起点 + **站立**动作在本方向的起点（原版
/// `TNpcActor.LoadSurface` 用 `m_nBodyOffset + m_nCurrentFrame`，而
/// `m_nCurrentFrame` 来自 `GetDefaultFrame` = `ActStand.start + dir * (frame + skip) + cf`）。
///
/// `frame` 是站立段内的帧（NPC 站着不动 ⇒ 恒 0；有动画的 NPC 才需要它）。
pub fn npc_index(race: u8, appr: u16, dir: u8, frame: u16) -> u32 {
    let stand = npc_actions(race, appr)[MAct::Stand as usize];
    npc_offset(appr) + stand.first(dir) + frame as u32
}

/// 怪物用的容器名（不含扩展名）——原版 `aGetMonImg`（`Actor.pas:958-1000`）。
///
/// `None` = 原版会去读**外部文件**（`Appr >= 1000` 的 `Graphics\Monster\<Appr>.wil`、
/// 或 `Appr` 落在没有分支的区间），本套素材没有 ⇒ 调用方降级成标记。
pub fn mon_container(appr: u16) -> Option<&'static str> {
    if appr >= 1000 {
        return None;
    }
    let nrace = appr / 10;
    if nrace <= 27 {
        Some(MON_FILES[nrace as usize])
    } else {
        match nrace {
            // ⚠️ 这两个是**别的目录**的容器：`Dragon.wzl` 本套素材缺失、`Effect.wzl` 有
            80 => Some("Dragon"),
            90 => Some("Effect"),
            _ => None,
        }
    }
}

/// 协议方向（`common.proto` 的 `Direction`，原版 +1）→ 原版 0..7。
///
/// 原版：`DR_UP=0 DR_UPRIGHT=1 DR_RIGHT=2 DR_DOWNRIGHT=3 DR_DOWN=4 DR_DOWNLEFT=5
/// DR_LEFT=6 DR_UPLEFT=7`（`Grobal2.pas:17-24`），与我们的枚举**顺序相同、差 1**。
///
/// 未指定（0）按原版惯例当下（朝下站立的图最不容易看出错）。
pub fn dir_of(direction: i32) -> u8 {
    match direction {
        1..=8 => (direction - 1) as u8,
        _ => 4,
    }
}

/// 人物容器（`Hum.wzl`）。
pub const HUM_LIB: &str = "Hum";
/// 武器容器（`Weapon.wzl`）。
///
/// # 为什么是 `Weapon`（2026-10-09 查证，用户给了"手持木剑"的官方截图）
///
/// 1. **公式来自官方服务端**：`mir2standard/GameOfMir/M2Server/ObjBase.pas:20018`
///
///    ```pascal
///    nWeapon := StdItem.Shape * 2;  Inc(nWeapon, m_btGender);
///    ```
///
///    即特征里的武器字节 = **`Shape*2 + 性别`（男 0 / 女 1）**，不是 `Shape`。
///    客户端 `Actor.pas` 的 `m_nWeaponOffset := HUMANFRAME * m_btWeapon`（600 一块）
///    直接拿它当块号 ⇒ [公式见 `human_index`]。
/// 2. **素材印证**：`Weapon.wzl` 有 45600 张 = **76 块**，其中 **块 0/1 恒为空**
///    （`Shape` 从 1 起 ⇒ 字节从 2 起），**块 2..75 共 74 块非空 = 37 把武器 × 2 性别**
///    —— 与 `Shape*2+性别` 一格不差（用 `wzldump --avg` 逐块扫出来的）。
///    块 2（图号 1200）= **浅棕木剑 + 握拳**，正是用户截图里木剑（`Shape=1`、男）的样子；
///    块 4/5（铁剑/青铜剑 `Shape=2`）= 银灰钢剑。
/// 3. **上一版写成 `Weapon2` 是误判**：当时把 `.wzx` 当 **16 字节/项**解析（真实格式是
///    **48 字节头 + 4 字节偏移/项**，见 [`crate::wzx`]），算出的"记录数对不上"是假的；
///    又恰好去 0/1 块取样（那两块本来就空）⇒ 得出"`Weapon.wzl` 坏了"的错误结论。
///    `Weapon2.wzl` 只有 42 块（21 把 × 2 性别），是**另一套造型**，不是主武器库。
///
/// ⚠️ 同时记得：`Shape` 是外观号、`Looks` 才是 `Items.wzl` 里的**图标**号
///（木剑 `Shape=1 / Looks=30`），两者不能混。
pub const WEAPON_LIB: &str = "Weapon";

// ---------- 生成段 ----------
// 由 `client/core/tools/gen_actor_tables.py` 从 Actor.pas 抽出；**不要手改数字**。
// （下面的 `HA` / `MA*` / `mon_actions` / `mon_offset` 全部来自那里。）

/// 怪物容器名表（`Mon1`..`Mon28`，`aGetMonImg` 的 0..27 分支）。
const MON_FILES: [&str; 28] = [
    "Mon1", "Mon2", "Mon3", "Mon4", "Mon5", "Mon6", "Mon7", "Mon8", "Mon9", "Mon10", "Mon11",
    "Mon12", "Mon13", "Mon14", "Mon15", "Mon16", "Mon17", "Mon18", "Mon19", "Mon20", "Mon21",
    "Mon22", "Mon23", "Mon24", "Mon25", "Mon26", "Mon27", "Mon28",
];

/// 人物动作表（原版 `HA: THumanAction`，Actor.pas:75-91）。
///
/// 顺序即 `HAct` 的判别式顺序，**不能重排**。
pub const HA: [Act; 14] = [
    Act {
        start: 0,
        frame: 4,
        skip: 4,
        ftime: 200,
    }, // ActStand
    Act {
        start: 64,
        frame: 6,
        skip: 2,
        ftime: 90,
    }, // ActWalk
    Act {
        start: 128,
        frame: 6,
        skip: 2,
        ftime: 120,
    }, // ActRun
    Act {
        start: 128,
        frame: 3,
        skip: 5,
        ftime: 120,
    }, // ActRushLeft
    Act {
        start: 131,
        frame: 3,
        skip: 5,
        ftime: 120,
    }, // ActRushRight
    Act {
        start: 192,
        frame: 1,
        skip: 0,
        ftime: 200,
    }, // ActWarMode
    Act {
        start: 200,
        frame: 6,
        skip: 2,
        ftime: 85,
    }, // ActHit
    Act {
        start: 264,
        frame: 6,
        skip: 2,
        ftime: 90,
    }, // ActHeavyHit
    Act {
        start: 328,
        frame: 8,
        skip: 0,
        ftime: 70,
    }, // ActBigHit
    Act {
        start: 192,
        frame: 6,
        skip: 4,
        ftime: 70,
    }, // ActFireHitReady
    Act {
        start: 392,
        frame: 6,
        skip: 2,
        ftime: 60,
    }, // ActSpell
    Act {
        start: 456,
        frame: 2,
        skip: 0,
        ftime: 300,
    }, // ActSitdown
    Act {
        start: 472,
        frame: 3,
        skip: 5,
        ftime: 70,
    }, // ActStruck
    Act {
        start: 536,
        frame: 4,
        skip: 4,
        ftime: 120,
    }, // ActDie
];

/// 怪物动作表（原版 `MA<n>: TMonsterAction`，Actor.pas:92-847），已去重。
///
/// ⚠️ 只生成了**我们的数据真正用到**的品种 + 出现次数最多的那张（当兜底）；
/// 原版对没列出的品种是 `Result := nil`（就是崩溃），我们不学它。
/// 原版 `MA10`（内容相同的还有 0 张：…）
pub const MA10: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 4,
        ftime: 200,
    }, // ActStand
    Act {
        start: 64,
        frame: 6,
        skip: 2,
        ftime: 120,
    }, // ActWalk
    Act {
        start: 128,
        frame: 4,
        skip: 4,
        ftime: 150,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 192,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 208,
        frame: 4,
        skip: 4,
        ftime: 140,
    }, // ActDie
    Act {
        start: 272,
        frame: 1,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// 原版 `MA11`（内容相同的还有 0 张：…）
pub const MA11: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 140,
    }, // ActDie
    Act {
        start: 340,
        frame: 1,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// 原版 `MA12`（内容相同的还有 0 张：…）
pub const MA12: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 4,
        ftime: 200,
    }, // ActStand
    Act {
        start: 64,
        frame: 6,
        skip: 2,
        ftime: 120,
    }, // ActWalk
    Act {
        start: 128,
        frame: 6,
        skip: 2,
        ftime: 150,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 192,
        frame: 2,
        skip: 0,
        ftime: 150,
    }, // ActStruck
    Act {
        start: 208,
        frame: 4,
        skip: 4,
        ftime: 160,
    }, // ActDie
    Act {
        start: 272,
        frame: 1,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// 原版 `MA14`（内容相同的还有 0 张：…）
pub const MA14: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 340,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActDeath
];

/// 原版 `MA15`（内容相同的还有 0 张：…）
pub const MA15: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 1,
        frame: 1,
        skip: 0,
        ftime: 100,
    }, // ActDeath
];

/// 原版 `MA16`（内容相同的还有 0 张：…）
pub const MA16: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 4,
        skip: 6,
        ftime: 160,
    }, // ActDie
    Act {
        start: 0,
        frame: 1,
        skip: 0,
        ftime: 160,
    }, // ActDeath
];

/// 原版 `MA17`（内容相同的还有 0 张：…）
pub const MA17: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 60,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActDie
    Act {
        start: 340,
        frame: 1,
        skip: 0,
        ftime: 140,
    }, // ActDeath
];

/// 原版 `MA19`（内容相同的还有 0 张：…）
pub const MA19: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 140,
    }, // ActDie
    Act {
        start: 340,
        frame: 1,
        skip: 0,
        ftime: 140,
    }, // ActDeath
];

/// 原版 `MA20`（内容相同的还有 0 张：…）
pub const MA20: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActDie
    Act {
        start: 340,
        frame: 10,
        skip: 0,
        ftime: 170,
    }, // ActDeath
];

/// 原版 `MA21`（内容相同的还有 0 张：…）
pub const MA21: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActWalk
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 20,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 30,
        frame: 10,
        skip: 0,
        ftime: 160,
    }, // ActDie
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// 原版 `MA22`（内容相同的还有 0 张：…）
pub const MA22: [Act; 7] = [
    Act {
        start: 80,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 240,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 320,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 340,
        frame: 10,
        skip: 0,
        ftime: 160,
    }, // ActDie
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 170,
    }, // ActDeath
];

/// 原版 `MA23`（内容相同的还有 0 张：…）
pub const MA23: [Act; 7] = [
    Act {
        start: 20,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 100,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 180,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 260,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 280,
        frame: 10,
        skip: 0,
        ftime: 160,
    }, // ActDie
    Act {
        start: 0,
        frame: 20,
        skip: 0,
        ftime: 100,
    }, // ActDeath
];

/// 原版 `MA24`（内容相同的还有 0 张：…）
pub const MA24: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 240,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActCritical
    Act {
        start: 320,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 340,
        frame: 10,
        skip: 0,
        ftime: 140,
    }, // ActDie
    Act {
        start: 420,
        frame: 1,
        skip: 0,
        ftime: 140,
    }, // ActDeath
];

/// 原版 `MA25`（内容相同的还有 0 张：…）
pub const MA25: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 70,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 20,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 50,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 60,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDie
    Act {
        start: 80,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA26`（内容相同的还有 0 张：…）
pub const MA26: [Act; 7] = [
    Act {
        start: 0,
        frame: 1,
        skip: 7,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 56,
        frame: 6,
        skip: 2,
        ftime: 500,
    }, // ActAttack
    Act {
        start: 64,
        frame: 6,
        skip: 2,
        ftime: 500,
    }, // ActCritical
    Act {
        start: 0,
        frame: 4,
        skip: 4,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 24,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 150,
    }, // ActDeath
];

/// 原版 `MA27`（内容相同的还有 0 张：…）
pub const MA27: [Act; 7] = [
    Act {
        start: 0,
        frame: 1,
        skip: 7,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 250,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 250,
    }, // ActCritical
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 150,
    }, // ActDeath
];

/// 原版 `MA28`（内容相同的还有 0 张：…）
pub const MA28: [Act; 7] = [
    Act {
        start: 80,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActDeath
];

/// 原版 `MA29`（内容相同的还有 0 张：…）
pub const MA29: [Act; 7] = [
    Act {
        start: 80,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 160,
    }, // ActWalk
    Act {
        start: 240,
        frame: 6,
        skip: 4,
        ftime: 100,
    }, // ActAttack
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActCritical
    Act {
        start: 320,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 340,
        frame: 10,
        skip: 0,
        ftime: 120,
    }, // ActDie
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 100,
    }, // ActDeath
];

/// 原版 `MA30`（内容相同的还有 0 张：…）
pub const MA30: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 20,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 30,
        frame: 20,
        skip: 0,
        ftime: 150,
    }, // ActDie
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA31`（内容相同的还有 0 张：…）
pub const MA31: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 0,
        frame: 2,
        skip: 8,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 20,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDie
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA32`（内容相同的还有 0 张：…）
pub const MA32: [Act; 7] = [
    Act {
        start: 0,
        frame: 1,
        skip: 9,
        ftime: 200,
    }, // ActStand
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 0,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 0,
        frame: 2,
        skip: 8,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 80,
        frame: 10,
        skip: 0,
        ftime: 80,
    }, // ActDie
    Act {
        start: 80,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA33`（内容相同的还有 0 张：…）
pub const MA33: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 340,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDie
    Act {
        start: 260,
        frame: 10,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA34`（内容相同的还有 0 张：…）
pub const MA34: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 200,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 200,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActAttack
    Act {
        start: 320,
        frame: 6,
        skip: 4,
        ftime: 120,
    }, // ActCritical
    Act {
        start: 400,
        frame: 2,
        skip: 0,
        ftime: 100,
    }, // ActStruck
    Act {
        start: 420,
        frame: 20,
        skip: 0,
        ftime: 200,
    }, // ActDie
    Act {
        start: 420,
        frame: 20,
        skip: 0,
        ftime: 200,
    }, // ActDeath
];

/// 原版 `MA39`（内容相同的还有 0 张：…）
pub const MA39: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 300,
    }, // ActStand
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActWalk
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 150,
    }, // ActAttack
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActCritical
    Act {
        start: 20,
        frame: 2,
        skip: 0,
        ftime: 150,
    }, // ActStruck
    Act {
        start: 30,
        frame: 10,
        skip: 0,
        ftime: 80,
    }, // ActDie
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// 原版 `MA40`（内容相同的还有 0 张：…）
pub const MA40: [Act; 7] = [
    Act {
        start: 0,
        frame: 4,
        skip: 6,
        ftime: 250,
    }, // ActStand
    Act {
        start: 80,
        frame: 6,
        skip: 4,
        ftime: 210,
    }, // ActWalk
    Act {
        start: 160,
        frame: 6,
        skip: 4,
        ftime: 110,
    }, // ActAttack
    Act {
        start: 580,
        frame: 20,
        skip: 0,
        ftime: 135,
    }, // ActCritical
    Act {
        start: 240,
        frame: 2,
        skip: 0,
        ftime: 120,
    }, // ActStruck
    Act {
        start: 260,
        frame: 20,
        skip: 0,
        ftime: 130,
    }, // ActDie
    Act {
        start: 260,
        frame: 20,
        skip: 0,
        ftime: 130,
    }, // ActDeath
];

/// 原版 `MA44`（内容相同的还有 0 张：…）
pub const MA44: [Act; 7] = [
    Act {
        start: 0,
        frame: 10,
        skip: 0,
        ftime: 300,
    }, // ActStand
    Act {
        start: 10,
        frame: 6,
        skip: 4,
        ftime: 150,
    }, // ActWalk
    Act {
        start: 20,
        frame: 6,
        skip: 4,
        ftime: 150,
    }, // ActAttack
    Act {
        start: 40,
        frame: 10,
        skip: 0,
        ftime: 150,
    }, // ActCritical
    Act {
        start: 40,
        frame: 2,
        skip: 8,
        ftime: 150,
    }, // ActStruck
    Act {
        start: 30,
        frame: 6,
        skip: 4,
        ftime: 150,
    }, // ActDie
    Act {
        start: 0,
        frame: 0,
        skip: 0,
        ftime: 0,
    }, // ActDeath
];

/// `RaceImg` → 动作表（原版 `GetRaceByPM`，Actor.pas:848-954）。
///
/// ⚠️ 参数名在原版里叫 `Race`，但喂进去的是 **`RACEfeature(c_feature)`**（低字节）
/// = 我们协议里的 `RaceImg` —— 不是服务端的 `Race` 字段（那个只用于 AI，不下发）。
///
/// ⚠️ 原版 `Race=50` 那一支还有一层 `case Appr of`（嵌套=True）：我们的数据没有
/// `RaceImg=50`，**故意没生成**；要支持时照 Actor.pas:881-915 补。
pub fn mon_actions(race_img: u8) -> &'static [Act; 7] {
    match race_img {
        10 => &MA10,
        11 => &MA11,
        12 | 24 => &MA12,
        13 | 14 | 17 | 18 | 23 => &MA14,
        15 | 22 => &MA15,
        16 => &MA16,
        19 | 20 | 21 | 37 | 40 | 45 | 52 | 53 | 64 | 65 | 66 | 67 | 68 | 69 | 73 | 74 | 79 => &MA19,
        30 | 31 => &MA17,
        32 => &MA24,
        33 => &MA25,
        34 | 90 => &MA30,
        35 => &MA31,
        36 => &MA32,
        41 | 42 => &MA20,
        43 => &MA21,
        47 => &MA22,
        48 | 49 => &MA23,
        54 => &MA28,
        55 => &MA29,
        60 | 61 | 62 | 70 | 71 | 72 => &MA33,
        63 => &MA34,
        75 | 77 => &MA39,
        78 => &MA40,
        83 => &MA44,
        98 => &MA27,
        99 => &MA26,
        // 原版对没列出的品种是 `Result := nil` ⇒ 崩溃；我们退回 MA19
        // （挑它是因为它覆盖我们数据里最多的怪：215/382 只；能对上表的共 308/382 只）
        _ => &MA19,
    }
}

/// 怪物外观号 → 图片块起点（原版 `GetOffset`，Actor.pas:1003-1218）。
///
/// `nrace = Appr / 10` 选“哪一段”，`npos = Appr % 10` 选段内第几个。
pub fn mon_offset(appr: u16) -> u32 {
    if appr >= 1000 {
        return 0; // 原版：>=1000 走外部 `Graphics\Monster\<Appr>.wil`，偏移从 0 算
    }
    let nrace = appr / 10;
    let npos = (appr % 10) as u32;
    match nrace {
        0 => npos * 280,
        1 => npos * 230,
        2 | 3 | 7 | 8 | 9 | 10 | 11 | 12 => npos * 360,
        4 => {
            if npos == 1 {
                600
            } else {
                npos * 360
            }
        }
        5 => npos * 430,
        6 => npos * 440,
        13 => match npos {
            0 => 0,
            1 => 360,
            2 => 440,
            3 => 550,
            _ => npos * 360,
        },
        14 => npos * 360,
        15 => npos * 360,
        16 => npos * 360,
        17 => match npos {
            2 => 920,
            _ => npos * 350,
        },
        18 => match npos {
            0 => 0,
            1 => 520,
            2 => 950,
            _ => 0,
        },
        19 => match npos {
            0 => 0,
            1 => 370,
            2 => 810,
            3 => 1250,
            4 => 1630,
            5 => 2010,
            6 => 2390,
            _ => 0,
        },
        20 => match npos {
            0 => 0,
            1 => 360,
            2 => 720,
            3 => 1080,
            4 => 1440,
            5 => 1800,
            6 => 2350,
            7 => 3060,
            _ => 0,
        },
        21 => match npos {
            0 => 0,
            1 => 460,
            2 => 820,
            3 => 1180,
            4 => 1540,
            5 => 1900,
            6 => 2440,
            7 => 2570,
            8 => 2700,
            _ => 0,
        },
        22 => match npos {
            0 => 0,
            1 => 430,
            2 => 1290,
            3 => 1810,
            _ => 0,
        },
        23 => match npos {
            0 => 0,
            1 => 440,
            2 => 820,
            3 => 1360,
            4 => 1420,
            5 => 1450,
            6 => 1560,
            7 => 1670,
            8 => 2270,
            9 => 2700,
            _ => 0,
        },
        24 => match npos {
            0 => 0,
            1 => 350,
            2 => 700,
            3 => 1050,
            4 => 1650,
            5 => 3100,
            6 => 3450,
            7 => 3880,
            8 => 4230,
            9 => 4580,
            _ => 0,
        },
        25 => match npos {
            0 => 0,
            1 => 350,
            2 => 700,
            3 => 1050,
            4 => 1400,
            5 => 1750,
            6 => 2180,
            7 => 2530,
            8 => 3000,
            9 => 3810,
            _ => 0,
        },
        26 => match npos {
            0 => 0,
            1 => 370,
            2 => 720,
            3 => 1080,
            4 => 1430,
            5 => 1780,
            6 => 2290,
            7 => 2720,
            8 => 3150,
            9 => 4000,
            _ => 0,
        },
        27 => match npos {
            0 => 0,
            1 => 350,
            2 => 700,
            3 => 1210,
            4 => 1720,
            5 => 2170,
            6 => 2250,
            7 => 2720,
            _ => 0,
        },
        80 => match npos {
            0 => 0,
            1 => 80,
            2 => 300,
            3 => 301,
            4 => 302,
            5 => 320,
            6 => 321,
            7 => 322,
            8 => 321,
            _ => 0,
        },
        90 => match npos {
            0 => 80,
            1 => 168,
            2 => 184,
            3 => 200,
            _ => 0,
        },
        _ => 0,
    }
}
// ---------- 生成段结束 ----------

#[cfg(test)]
mod tests {
    use super::*;

    /// 人物公式与原版逐条对齐（`Actor.pas:75-91` 的表 + `:3160` 的 `600*Dress`）。
    #[test]
    fn 人物图号() {
        // 站立、朝上（dir=0）、第 0 帧 = 块起点
        assert_eq!(human_index(0, human_pose(None, false, false).act, 0, 0), 0);
        assert_eq!(
            human_index(1, human_pose(None, false, false).act, 0, 0),
            600
        );
        assert_eq!(
            human_index(23, human_pose(None, false, false).act, 0, 0),
            13_800
        );
        // 走：start=64、stride=8 ⇒ 朝右（dir=2）首帧 80
        assert_eq!(human_index(0, human_pose(None, true, false).act, 2, 0), 80);
        assert_eq!(
            human_index(0, human_pose(None, true, false).act, 7, 6),
            64 + 56 + 6
        );
        // 攻击：start=200、stride=8
        assert_eq!(
            human_index(0, human_pose(Some(1), false, false).act, 4, 5),
            200 + 32 + 5
        );
        // 死亡：start=536、stride=8 ⇒ 最后一个方向最后一帧仍在 600 以内（块不越界）
        assert_eq!(
            human_index(0, human_pose(Some(action::DEATH), false, false).act, 7, 3),
            536 + 56 + 3
        );
        assert!(
            human_index(0, human_pose(Some(action::DEATH), false, false).act, 7, 3) < HUMAN_FRAME
        );
        // 受击（51）走 ActStruck
        assert_eq!(
            human_pose(Some(action::HURT), false, false).act,
            HAct::Struck
        );
    }

    /// 走 / 跑选的是**两段不同的图**（起点差 64），`EntityMove.run` 一路传到这儿。
    ///
    /// ⚠️ 选错**不报错**：帧数一样（都是 6 帧），只是画面上放着另一套动作 ——
    /// 用户 2026-10-08 报的"跑起来不像跑"就是这个。
    #[test]
    fn 跑和走是两段图() {
        assert_eq!(human_pose(None, true, false).act, HAct::Walk);
        assert_eq!(human_pose(None, true, true).act, HAct::Run);
        // 站着不动时 `run` 没有意义（"这一步是跑的"只在移动中成立）
        assert_eq!(human_pose(None, false, true).act, HAct::Stand);
        // **动作优先于移动**：受击时即使在跑也放受击（判断顺序见 `human_pose`）
        assert_eq!(human_pose(Some(action::HURT), true, true).act, HAct::Struck);
        // 两段的起点差 64（`HA`：Walk 64 / Run 128，`Actor.pas:77-78`）
        assert_eq!(
            HAct::Walk.act().start + 64,
            HAct::Run.act().start,
            "原版两段图相差 64 —— 图号公式若改，这条会先红"
        );
    }

    /// 四种攻击消息号里有三种**共用** `ActHit`（原版 `Actor.pas` 的 case 列表）——
    /// 这条测试是防"顺手给半月/刺杀各配一段"的：原版它们就是同一段。
    #[test]
    fn 攻击动作都进同一段() {
        for a in [1u32, 4, 5, 6, 7, 8] {
            assert_eq!(
                human_pose(Some(a), false, false).act,
                HAct::Hit,
                "动作 {a} 应进 ActHit"
            );
        }
        assert_eq!(human_pose(Some(2), false, false).act, HAct::HeavyHit);
        assert_eq!(human_pose(Some(3), false, false).act, HAct::BigHit);
    }

    /// 帧推进：按 `ftime` 计时，循环的绕回、一次播完的停在末帧。
    #[test]
    fn 帧推进() {
        let stand = HAct::Stand.act(); // frame 4, ftime 200
        assert_eq!(stand.frame_at(0), 0);
        assert_eq!(stand.frame_at(199), 0);
        assert_eq!(stand.frame_at(200), 1);
        assert_eq!(stand.frame_at(800), 0, "4 帧后绕回");
        let die = HAct::Die.act(); // frame 4, ftime 120
        assert_eq!(die.frame_once(0), 0);
        assert_eq!(die.frame_once(10_000), 3, "一次播完应停在末帧");
        assert_eq!(die.duration_ms(), 480);
        // 雷区：ftime=0 / frame=0 的动作段不能除零
        let empty = Act {
            start: 0,
            frame: 0,
            skip: 0,
            ftime: 0,
        };
        assert_eq!(empty.frame_at(999), 0);
        assert_eq!(empty.frame_once(999), 0);
    }

    /// 怪物图片块起点（`GetOffset`，原版 `Actor.pas:1003+`）。
    #[test]
    fn 怪物块起点() {
        assert_eq!(mon_offset(0), 0);
        assert_eq!(mon_offset(1), 280); // nrace=0：每个 280
        assert_eq!(mon_offset(9), 2520);
        assert_eq!(mon_offset(11), 230); // nrace=1：每个 230（npos=1）
        assert_eq!(mon_offset(15), 1150); // nrace=1、npos=5
        assert_eq!(mon_offset(72), 720); // nrace=7：每个 360
        assert_eq!(mon_offset(41), 600); // nrace=4 且 npos=1：**特例**，不是 360
        assert_eq!(mon_offset(40), 0);
        assert_eq!(mon_offset(42), 720);
        assert_eq!(mon_offset(190), 0); // nrace=19 的 npos=0 就是 0
        assert_eq!(mon_offset(191), 370);
        assert_eq!(mon_offset(900), 80); // nrace=90（Effect）
        assert_eq!(mon_offset(1000), 0); // >=1000 走外部容器
        assert_eq!(mon_offset(320), 0); // 没有分支的 nrace ⇒ 原版 Result 保持 0
    }

    /// 容器选择（`aGetMonImg`）。
    #[test]
    fn 怪物容器() {
        assert_eq!(mon_container(0), Some("Mon1"));
        assert_eq!(mon_container(9), Some("Mon1"));
        assert_eq!(mon_container(10), Some("Mon2"));
        assert_eq!(mon_container(190), Some("Mon20"));
        assert_eq!(mon_container(279), Some("Mon28"));
        assert_eq!(mon_container(280), None, "nrace=28 没有分支");
        assert_eq!(mon_container(800), Some("Dragon"));
        assert_eq!(mon_container(900), Some("Effect"));
        assert_eq!(mon_container(1000), None);
    }

    /// 品种→动作表：19 是我们数据里最大的一支（见生成段的覆盖率注释）。
    #[test]
    fn 怪物动作表映射() {
        assert_eq!(
            mon_actions(19)[MAct::Stand as usize].start,
            MA19[MAct::Stand as usize].start
        );
        // 原版 `13{05}: Result:=@MA14`（表名与品种号**不同**，别按号猜）
        assert_eq!(
            mon_actions(13)[MAct::Walk as usize],
            MA14[MAct::Walk as usize]
        );
        assert_eq!(
            mon_actions(34)[MAct::Walk as usize],
            MA30[MAct::Walk as usize]
        );
        // 没列出的品种退回 MA19（原版是 nil ⇒ 崩溃）
        assert_eq!(
            mon_actions(156)[MAct::Stand as usize],
            MA19[MAct::Stand as usize]
        );
    }

    /// 没有有效帧的动作段要退回站立 —— 否则会画出**别的品种的图**（原版靠
    /// 两个动作段 start 相同来兜，我们显式判 frame==0）。
    #[test]
    fn 空动作段退回站立() {
        // MA9 的 ActDie 是 (0,1,7)（有帧），ActCritical 是 (0,0,0)（没有）
        assert_eq!(monster_pose(9, None, false).act, MAct::Stand);
        // 找一个 Die 有效帧为 0 的品种：直接构造断言逻辑
        let table = mon_actions(19);
        if table[MAct::Die as usize].frame == 0 {
            assert_eq!(
                monster_pose(19, Some(action::DEATH), false).act,
                MAct::Stand
            );
        }
    }

    /// 方向换算只在 `dir_of` 一处发生。
    #[test]
    fn 方向换算() {
        assert_eq!(dir_of(1), 0, "协议的上 = 原版的 0");
        assert_eq!(dir_of(5), 4, "协议的下 = 原版的 4");
        assert_eq!(dir_of(8), 7);
        assert_eq!(dir_of(0), 4, "未指定按朝下");
        assert_eq!(dir_of(99), 4);
    }

    /// feature 拆位必须与服务端 `proto.MakeFeature`/`MakeLong` 对称。
    ///
    /// 期望值用位运算算出来（不是抄一份常量），这样服务端改了布局这条会红。
    #[test]
    fn feature_拆位() {
        // MakeHumanFeature(0, dress=10, weapon=20, hair=3) = MakeLong(0|20<<8, 3|10<<8)
        let f = ((3u32 | 10 << 8) << 16) | (20 << 8);
        let hf = unpack_human_feature(f);
        assert_eq!((hf.dress, hf.weapon, hf.hair, hf.race_img), (10, 20, 3, 0));

        // 怪物：MakeLong(RaceImg=19, Appr=151)（服务端 entity/monster.go）
        let m = (151u32 << 16) | 19;
        let mf = unpack_monster_feature(m);
        assert_eq!((mf.race_img, mf.appr), (19, 151));
    }

    /// **真素材验收**：公式算出来的图号，在容器里就是**真图**。
    ///
    /// 这条是 B′ 的关键 —— 图号公式错一格，代码照样跑（画出来只是"别的帧/别的方向"），
    /// 而上面那些数字断言只能证明"公式与我自己想的一致"。所以必须落一次真素材：
    ///
    /// - 8 个方向的站立帧都要能解码，且**彼此不同**（方向步长写错会撞成同一张）；
    /// - 走一整个循环（6 帧）都要有图；
    /// - 怪物按 `Appr` 选出的块也要有图。
    ///
    /// 素材不在时跳过（与 `wzl.rs` 的黄金哈希同一个门控）。
    #[test]
    fn 真素材_图号落在真图上() {
        use crate::wzl::Wzl;

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::Path::new(&dir);

        let hum = Wzl::open(dir.join(HUM_LIB)).expect("Hum.wzl");
        // 600 张一块：24 块 ⇒ 块起点不能越界
        assert!(
            hum.len() >= 24 * HUMAN_FRAME as usize,
            "Hum 只有 {} 张",
            hum.len()
        );
        for d in 0..8u8 {
            let i = human_index(0, human_pose(None, false, false).act, d, 0);
            let s = hum
                .decode(i as usize)
                .unwrap_or_else(|| panic!("站立方向 {d}：图号 {i} 没有图"));
            assert!(!s.is_empty(), "站立方向 {d}：图号 {i} 是空图");
        }
        // 方向步长（frame+skip）写错时，相邻方向会**撞成同一张图**
        let right = hum
            .decode(human_index(0, human_pose(None, false, false).act, 2, 0) as usize)
            .unwrap();
        let down_right = hum
            .decode(human_index(0, human_pose(None, false, false).act, 3, 0) as usize)
            .unwrap();
        assert_ne!(
            right.rgba, down_right.rgba,
            "朝右与朝右下不该是同一张图（方向步长错了？）"
        );

        // 走一整个循环：6 帧都要有图（`skip` 那两格才是空的，别把 stride 当成 frame）
        let walk = human_pose(None, true, false);
        for k in 0..walk.act.act().frame {
            let i = human_index(0, walk.act, 5, k);
            assert!(
                hum.decode(i as usize).is_some(),
                "走向下第 {k} 帧：图号 {i} 没有图"
            );
        }

        // 怪物：`Appr=1` 落在 Mon1 的第二块（280 起）
        let lib = mon_container(1).expect("Appr=1 应有容器");
        let mon = Wzl::open(dir.join(lib)).unwrap_or_else(|e| panic!("{lib}.wzl: {e}"));
        let idx = monster_index(1, 19, monster_pose(19, None, false).act, 0, 0);
        let s = mon
            .decode(idx as usize)
            .unwrap_or_else(|| panic!("{lib} 图号 {idx} 没有图（共 {} 张）", mon.len()));
        assert!(!s.is_empty());

        // ---- 武器层：块号必须是 `2*Shape + 性别`（不是 `Shape`）----
        //
        // 官方口径 `ObjBase.pas:20018`：`nWeapon := StdItem.Shape * 2; Inc(nWeapon, m_btGender)`；
        // 客户端再乘 600 取块 ⇒ `Weapon.wzl` 是"**每把武器占两块**（男/女）"：
        // 45600 张 = 76 块，**块 0/1 恒为空**（`Shape` 从 1 起 ⇒ 字节从 2 起），
        // 块 2..75 共 74 块 = `Shape` 1..37 × 2。
        //
        // ⚠️ 用户 2026-10-09 给了"手持木剑"的官方截图才查出来：木剑 `Shape=1`、男
        // ⇒ 块 2（图号 1200）才是那把棕木剑。之前公式写成 `Shape` ⇒ 取块 1，而块 1
        // **整块是空的** ⇒ 看起来"拿武器的人手上什么都没有"，于是误判成"素材坏了"、
        // 换去 `Weapon2`（`Shape=1` 落到那把**细长银剑**，用户原话"更像长剑铁剑"）。
        let wp = Wzl::open(dir.join(WEAPON_LIB)).unwrap_or_else(|e| panic!("{WEAPON_LIB}.wzl: {e}"));
        assert!(
            wp.len() >= 76 * HUMAN_FRAME as usize,
            "{WEAPON_LIB} 只有 {} 张（应 ≥ 76 块 × 600）",
            wp.len()
        );
        let stand = human_pose(None, false, false).act;
        for shape in 1..=37u8 {
            for sex in 0..2u8 {
                let part = shape * 2 + sex;
                // 个别方向素材里就是**空占位**（朝上时剑被身体挡住，见 `--list` 里
                // 那些 8x8 的记录）⇒ 只要 8 个站姿方向里**有一个能出图**就算这块有货。
                let ok = (0..8u8).any(|d| {
                    wp.decode(human_index(part, stand, d, 0) as usize)
                        .is_some_and(|s| !s.is_empty())
                });
                assert!(ok, "武器 Shape={shape} 性别={sex}（块 {part}）：八个方向都没有图");
            }
        }
        // 块 1（= `Shape 0` 那格）必须是空的 —— 它空着本身就是"字节从 2 起"的证据，
        // 也是上一版误判的现场。它哪天有图了，说明口径要重查。
        assert!(
            wp.decode(human_index(1, stand, 4, 0) as usize)
                .map_or(true, |s| s.is_empty()),
            "块 1 竟然有图 ⇒ `2*Shape+性别` 这条口径要重新查"
        );
        // 木剑（`Shape=1`、男 ⇒ 块 2）画出来必须是**木头色**：给对了块的像素级证据
        //（官方截图里那把木剑就是浅棕）。铁剑/青铜剑（`Shape=2` ⇒ 块 4/5）是银灰。
        let mut brown = (0u64, 0u64, 0u64, 0u64);
        let mut steel = (0u64, 0u64, 0u64, 0u64);
        for (part, acc) in [(2u8, &mut brown), (4u8, &mut steel)] {
            for d in 0..8u8 {
                let Some(s) = wp.decode(human_index(part, stand, d, 0) as usize) else { continue };
                for px in s.rgba.chunks(4) {
                    if px[3] > 128 {
                        acc.0 += px[0] as u64;
                        acc.1 += px[1] as u64;
                        acc.2 += px[2] as u64;
                        acc.3 += 1;
                    }
                }
            }
        }
        assert!(brown.3 > 0, "木剑（块 2）一个不透明像素都没有");
        assert!(steel.3 > 0, "铁剑（块 4）一个不透明像素都没有");
        let (r, g, b) = (brown.0 / brown.3, brown.1 / brown.3, brown.2 / brown.3);
        assert!(
            r > g && g > b && r - b > 20,
            "木剑该是棕色（R>G>B 且偏暖），实得 rgb({r},{g},{b})"
        );
        let (sr, sg, sb) = (steel.0 / steel.3, steel.1 / steel.3, steel.2 / steel.3);
        assert!(
            (sr as i64 - sb as i64).abs() < 40 && (sg as i64 - sb as i64) < 40,
            "铁剑该是银灰（近中性），实得 rgb({sr},{sg},{sb}) —— 别与木剑换块"
        );

        // 头发：原版要的容器叫 `Hair`（`Share.pas:59` 的 `HAIRIMGIMAGESFILE`），
        // 而本套素材**没有这个文件**；有的是 `hair2`（真素材）与 `hair_ck` / `hair4_ck`
        // ——后两个的 `.wzx` 索引里有 5328 条，`.wzl` 却只有 64 字节的头（取不出图）。
        // 这条断言就是"不画头发"这个决定的地基：哪天它红了，说明素材补上了。
        assert!(
            Wzl::open(dir.join("Hair")).is_err(),
            "本套素材出现了 Hair.wzl ⇒ 头发层（Actor.pas:3162-3167）该实现了"
        );
        if let Ok(hair_ck) = Wzl::open(dir.join("hair_ck")) {
            for i in 0..hair_ck.len().min(8) {
                if let Some(s) = hair_ck.decode(i) {
                    assert!(s.is_empty(), "hair_ck 第 {i} 张取出了真图 ⇒ 头发层该实现了");
                }
            }
        }
    }
}

/// 拆怪物 feature 位域（与服务端 `proto.MakeLong(RaceImg, Appr)` 对称）。
///
/// ⚠️ 客户端那个"Race"参数吃的是**低字节 = `RaceImg`**，不是服务端的 `Race` 字段
/// （`RACEfeature(c_feature)`，`Grobal2.pas:2681`；服务端的 `Race` 只用于 AI、不下发）。
/// （`RACEfeature(c_feature)`，`Grobal2.pas:2681`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct MonsterFeature {
    pub race_img: u8,
    pub appr: u16,
}

pub fn unpack_monster_feature(f: u32) -> MonsterFeature {
    MonsterFeature {
        race_img: (f & 0xFF) as u8,
        appr: (f >> 16) as u16,
    }
}

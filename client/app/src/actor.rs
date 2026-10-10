//! actor 精灵与实体渲染：缓存、动画状态、走跑补间、名字条/血条、降级标记。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::HashMap;
use std::path::Path;
use std::time::Instant;

use mir2_core::map::{UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
use sdl3::render::{
    BlendMode, FPoint, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use crate::assets::open_lib;
use crate::colors::{C_HOVER_NAME, HOVER_TINT};
use crate::geom::{cell_to_screen, cell_to_screen_f, dir_delta};
use crate::gfx::trunc;
use crate::input::ACTION_TAIL_MS;
use crate::layout::BAR_TOP;
use crate::net::{RUN_MS, WALK_MS};
use crate::window::{WIN_H, WIN_W};

use crate::font;

/// 精灵纹理缓存上限。与图块缓存同理：越界就整个清掉，不做 LRU ——
/// 地图比视口大得多，走到哪解到哪，记账成本换不来什么。
pub(crate) const SPRITE_CACHE_CAP: usize = 512;

/// 走路**一格**的补间时长（毫秒）—— 与**服务端的移动节流**对齐。
///
/// ⚠️ 出处是 `server/internal/entity/object.go:483` 的 `MoveLimiter`：`MinWalk = 600ms`、
/// `MinRun = 400ms`（**一步 2 格**）。改服务端那儿就得改这里：
/// 补间比它短 = "每格提前到位再干等"（用户 2026-10-08 报的卡顿，原先写死 320 ms 就是这毛病）；
/// 比它长 = 精灵被下一格"拽着走"。
///
/// ⚠️ 这两个常数只给**别人**（怪/其他玩家）—— 它们的节奏由服务端驱动；
/// **自己**用 [`self_move_ms`]（我们自己的发送步频 `WALK_MS`/`RUN_MS`）。
pub(crate) const WALK_STEP_MS: u32 = 600;

/// 跑**一步**（`RUN_STEPS` 格）的补间时长。
///
/// ⚠️ 它**不是**一格的时长：跑一步 2 格 ⇒ 每格 300 ms，是走（600 ms）的一半。
///
/// 数值对齐原版：`Actor.pas` 的 `ActRun` 是 6 帧 × 120ms = 720ms 走 2 格，
/// 而服务端节流 600ms 拖着 ⇒ 实际 **600ms / 2 格 = 300ms 一格**
///（动画永远播不到第 6 帧，收到下一条 `SM_RUN` 就重置 —— 这就是原版的跑动手感）。
/// 原来写 400（每格 200ms）是跟着**错的** `MinRun = 400` 推的 ⇒ 跑快 50% 且滑步
///（用户 2026-10-09 第 4 条）。
pub(crate) const RUN_STEP_MS: u32 = 600;

/// 跑一步的格数（原版 `GetNextRunXY` 一次 +2；斜着跑也是两格）。
pub(crate) const RUN_STEPS: i32 = 2;

/// 一次移动该补间多久：**按实际格数与走/跑算**（别写死一格）。
///
/// 格数用 `max(|dx|,|dy|)`（切比雪夫距离）：斜着走一格 = 一格，斜着跑 = 两格。
pub(crate) fn move_ms(dx: i32, dy: i32, run: bool) -> u32 {
    let cells = dx.abs().max(dy.abs()).max(1) as u32;
    let per_cell = if run {
        RUN_STEP_MS / RUN_STEPS as u32 // 跑：600/2 = 300 ms 一格
    } else {
        WALK_STEP_MS
    };
    per_cell * cells
}

/// **自己**这一步该补间多久 —— 用**本客户端自己的步频**（[`WALK_MS`]/[`RUN_MS`]）。
///
/// # 为什么要跟别人分开（2026-10-08，用户报"走路手感"时发现）
///
/// 我们**不是预测式移动**：每一步都是"发 `MoveInput` → 等服务端回显 → 才开始补间"，
/// 而下一步的 `MoveInput` 是在 `WALK_MS`（650）之后才发的 ⇒ 自己这一步的**实际**节奏是
/// **650ms/格**。若按服务端的 `MinWalk`（600）补间，就会在**每格末尾空出 50ms**：
/// 那 50ms 里 `ActorAnim::moving()` 变 false ⇒ ①走路动画闪回站立帧、②相机停一下 ——
/// 每步都来一次，正是"没做好"的来源。
///
/// ⚠️ 别的实体（怪/别人）**不能**用这个：它们的节奏由服务端驱动（`MinWalk = 600`），
/// 按 650 补间会"被下一格拽着走"（补间还没完，下一条 `EntityMove` 就到了）。
pub(crate) fn self_move_ms(dx: i32, dy: i32, run: bool) -> u32 {
    let cells = dx.abs().max(dy.abs()).max(1) as u32;
    let per_cell = if run {
        RUN_MS as u32 / RUN_STEPS as u32 // 跑：650/2 = 325 ms 一格
    } else {
        WALK_MS as u32
    };
    per_cell * cells
}

/// 新来一格时，走路动画的相位起点要不要重置。
///
/// **接着推**（`prev`）还是**从头**（`now`）只看一件事：上一格还没走到位吗
/// （`was_moving`）。连贯地走/跑时不重置 —— 原版就是这么推的
///（`Actor.pas:3230-3263`：`m_dwFrameTime := HA.ActWalk.ftime`，不按"到位"重置）。
///
/// ⚠️ 每格都重置的后果：`ActWalk` 一轮 540 ms，而每格 600 ms ⇒ 永远播不到第 5、6 帧，
/// 看起来像"一瘸一拐"（用户报的）。
pub(crate) fn next_walk_since(was_moving: bool, prev: Instant, now: Instant) -> Instant {
    if was_moving {
        prev
    } else {
        now
    }
}

// ⚠️ 这里原来有个写死的 `MOVE_MS = 320`（注释说"客户端定的观感参数"）。
// 它同时是两件事的根源：① 每格**提前到位再干等**（320 < 服务端的 600）；
// ② 走路动画每格重置。2026-10-08 换成按动作算的 `move_ms()`（见上面）——
// 时长现在**跟着服务端节流走**，动画则改成连续相位（`next_walk_since`）。

/// 精灵纹理缓存键：容器名 + 图号（容器名都是 `&'static str`，见 `core::actor`）。
pub(crate) type SpriteKey = (&'static str, u32);

pub(crate) struct SpriteTex<'a> {
    pub(crate) tex: Texture<'a>,
    /// 图自带锚点（原版 `m_nPx/m_nPy`）。
    pub(crate) anchor_x: i16,
    pub(crate) anchor_y: i16,
    /// **不透明内容框** `(x, y, w, h)`（图内坐标）—— 悬停命中用（见 `actor_rect`）。
    pub(crate) bbox: Option<mir2_core::wzl::BBox>,
}

/// 玩家/怪物精灵的纹理缓存（与图块缓存分开：键是容器名、混合一律普通 alpha）。
pub(crate) struct SpriteCache<'a> {
    pub(crate) libs: HashMap<&'static str, Option<Wzl>>,
    pub(crate) texs: HashMap<SpriteKey, SpriteTex<'a>>,
}

impl<'a> SpriteCache<'a> {
    pub(crate) fn new() -> Self {
        Self {
            libs: HashMap::new(),
            texs: HashMap::new(),
        }
    }

    /// 保证缓存里有该精灵；容器缺失 / 图号取不出图时返回 `None`（调用方降级成标记）。
    pub(crate) fn ensure<T>(
        &mut self,
        tc: &'a TextureCreator<T>,
        dir: &Path,
        lib: &'static str,
        idx: u32,
    ) -> Option<()> {
        if self.texs.contains_key(&(lib, idx)) {
            return Some(());
        }
        if self.texs.len() >= SPRITE_CACHE_CAP {
            self.texs.clear();
        }
        let w = self
            .libs
            .entry(lib)
            .or_insert_with(|| open_lib(dir, lib))
            .as_ref()?;
        let s = w.decode(idx as usize)?;
        if s.is_empty() {
            return None;
        }
        let mut t = tc
            .create_texture(
                PixelFormat::RGBA32,
                TextureAccess::Streaming,
                s.width as u32,
                s.height as u32,
            )
            .ok()?;
        t.set_blend_mode(BlendMode::Blend);
        t.set_scale_mode(ScaleMode::Nearest);
        t.update(None::<Rect>, &s.rgba, s.width as usize * 4).ok()?;
        // 不透明包围盒（解码时顺手算一次）：**悬停命中**要用 —— 人物/怪图的透明边
        // 各不一样，按整图判"指着没有"会把一大圈空气也算进去（用户 2026-10-10
        // 第 4 条：怪有透明像素 ⇒ 鼠标很难选中）。原版按**画面上的像素**判
        //（`GetAttackFocusCharacter`）⇒ 我们用同一份解码数据算出内容框。
        let bbox = s.alpha_bbox();
        self.texs.insert(
            (lib, idx),
            SpriteTex {
                tex: t,
                anchor_x: s.anchor_x,
                anchor_y: s.anchor_y,
                bbox,
            },
        );
        Some(())
    }
}

/// 一帧"看到的"实体状态：`(id, 格子, 动作, 这一步是不是跑)` —— 喂给 `ActorAnim` 的那四个数。
///
/// 抽个别名是因为它要四处传（收集 / 遍历 / 调试），写成裸元组 clippy 会报 `type_complexity`，
/// 更要紧的是读代码时看不出第四个 `bool` 是"跑"。
/// 一帧看到的实体：`(id, 格子, 动作值, 是不是跑, 动作事件计数)`。
///
/// ⚠️ 最后那个 `action_seq` 是**动作事件计数**（`core::world` 每收到一条 `EntityAction`
/// 就 +1）：判"要不要重播挥砍动画"必须用它，不能只看动作值 —— 普通攻击的值恒为 1，
/// 按值判会让**第二次以后的每一刀都没有挥砍动作**（用户 2026-10-09 反复报的那条）。
pub(crate) type SeenEntity = (u64, (i32, i32), Option<u32>, bool, u64);

/// 一个实体的动画状态（**渲染层**持有 —— 世界模型是不带时钟的纯状态）。
pub(crate) struct ActorAnim {
    /// 上次看到的格子（用来判"又动了"）。
    pub(crate) cell: (i32, i32),
    /// 上一次移动的来处（补间用）。
    pub(crate) from: Option<(i32, i32)>,
    /// 最近一次动作（协议动作 id，见 `protocol.md` §9.5）。
    pub(crate) action: Option<u32>,
    /// 上一次据以**重播**动画的动作事件计数（`core::world` 的 `action_seq`／
    /// `self_action_seq`）。
    ///
    /// 每来一条新的 `EntityAction` 计数就变 ⇒ 重播一次挥砍；计数没变就绝不动
    /// `action_at`（否则移动、状态刷新之类的每帧调用会把动画一次次摁回第一帧）。
    pub(crate) action_seq: u64,
    /// **还没播的**动作（`(值, 事件计数)`）：走/跑没结束时收到的挥砍先存这儿，
    /// 等这一步走完再补播（用户 2026-10-09 补的第 2 条："即使是在'追打'时，
    /// 也应在移动结束后再补攻击动作"）。
    ///
    /// 为什么连 `action_at` 也一起推迟：动作的播放进度是从 `action_at` 起的 ⇒
    /// 走路那 600ms 里如果先起钟，走到位时挥砍已经播完了，表现就是"没砍"。
    pub(crate) pending_action: Option<(Option<u32>, u64)>,
    /// **移动**的起始时刻（补间与 `moving()` 用它）。
    pub(crate) changed_at: Instant,
    /// **动作**的起始时刻（挥砍/受击的播放进度用它）。
    ///
    /// # ⚠️ 为什么必须与 `changed_at` 分开（用户 2026-10-09 报的"杀死怪之后再跑还有砍的动作"）
    ///
    /// 原来只有一个钟：动作与移动共用 `changed_at` ⇒ **每走一格都把动作进度清零**
    /// ⇒ 那个"早就该过期的挥砍"（`world.self_action` 收到过就一直留着）在每次移动时
    /// **重播**一遍：表现就是"杀了怪，一跑起来又在砍"（别的实体同理：怪走路时也会闪攻击动作）。
    /// 分开之后移动只动移动钟；动作到点就过期，**不会被下一次移动救活**。
    pub(crate) action_at: Instant,
    /// 这次移动的补间时长（ms）—— 由 `from → cell` 的格数与走/跑算出（见 `move_ms`）。
    ///
    /// **一步一算**（走一格 600 ms、跑一步 400 ms），所以不能再有全局常量。
    pub(crate) move_ms: u32,
    /// 走路/跑步动画的**连续相位**起点（见 `next_walk_since`）。
    ///
    /// ⚠️ 与 `changed_at` 分工明确：那个是"这一格从哪来"（每步都要重置），
    /// 这个是"动画播到第几帧了"（只在停下/换动作时重置）。混用一个时钟 =
    /// 每走一格动画从头开始 = `ActWalk` 的 6 帧只看得见前 4 帧（用户报的"一瘸一拐"）。
    pub(crate) walk_since: Instant,
}

impl ActorAnim {
    pub(crate) fn elapsed_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.changed_at).as_millis() as u32
    }

    /// 手上的动作已经播了多久（`body_sprite`/`weapon_sprite` 取帧用它）。
    pub(crate) fn action_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.action_at).as_millis() as u32
    }

    /// 是不是正走在半路上（决定播走路的动画）。
    pub(crate) fn moving(&self, now: Instant) -> bool {
        self.from.is_some() && self.elapsed_ms(now) < self.move_ms
    }

    /// 走路/跑步动画已经播了多久（**连续相位**，不随每格重置）。
    pub(crate) fn walk_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.walk_since).as_millis() as u32
    }

    /// 手上的**攻击**动画还没播完（`false` = 可以发下一个动作/走下一步）。
    ///
    /// 原版这条是 `CanNextAction`（`g_MySelf.IsIdle`，`Actor.pas:1722-1736`：
    /// `m_nCurrentAction <> 0` 就"不空"、发不出下一个动作）。追打时靠它保证
    /// **砍完一刀再迈步**（用户 2026-10-09 补的第 2 条后半句）。
    pub(crate) fn attack_busy(&self, now: Instant) -> bool {
        let Some(a) = self.action else {
            return false;
        };
        if !mir2_core::world::action::is_attack(a) {
            return false;
        }
        let pose = mir2_core::actor::human_pose(Some(a), false, false);
        if pose.looping {
            return false;
        }
        self.action_ms(now) < pose.act.act().duration_ms() + ACTION_TAIL_MS
    }

    /// 补间后的绘制坐标（格子坐标，浮点）。
    pub(crate) fn draw_pos(&self, to: (i32, i32), now: Instant) -> (f32, f32) {
        match self.from {
            Some(f) if self.moving(now) => {
                let t = self.elapsed_ms(now) as f32 / self.move_ms.max(1) as f32;
                (
                    f.0 as f32 + (to.0 - f.0) as f32 * t,
                    f.1 as f32 + (to.1 - f.1) as f32 * t,
                )
            }
            _ => (to.0 as f32, to.1 as f32),
        }
    }
}

/// 人物动作采样：动作**播完就回到站立/走路**。
///
/// ⚠️ 不这么做的话实体会永远停在那一刀的末帧 —— 协议只在"动作变化"时发
/// `EntityAction`，没有"动作结束"这条消息。时长取自动作表（`ftime × frame`）。
pub(crate) fn human_sample(
    held: Option<u32>,
    held_ms: u32,
    moving: bool,
    run: bool,
    move_ms: u32,
    dead: bool,
) -> (mir2_core::actor::HAct, u16) {
    use mir2_core::actor as A;
    // 死了 ⇒ 停在 `Die` 的最后一帧（尸骨）；理由见 `monster_sample`。
    if dead {
        return (A::HAct::Die, A::HAct::Die.act().last_frame());
    }
    let mut pose = A::human_pose(held, moving, run);
    let mut act = pose.act.act();
    let mut elapsed = if !pose.looping && held_ms >= act.duration_ms() + ACTION_TAIL_MS {
        // 播完**且过了尾巴** ⇒ 回到站立/走路（`ACTION_TAIL_MS` 的说明见常量处）
        pose = A::human_pose(None, moving, run);
        act = pose.act.act();
        0
    } else {
        held_ms
    };
    // 走/跑这两段用**移动相位**（连续推进），不是"这一格走了多久"：
    // 后者每格重置 ⇒ `ActWalk` 的 6 帧只看得见前 4 帧（见 `ActorAnim::walk_since`）。
    if matches!(pose.act, A::HAct::Walk | A::HAct::Run) {
        elapsed = move_ms;
    }
    let frame = if pose.looping {
        act.frame_at(elapsed)
    } else {
        act.frame_once(elapsed)
    };
    (pose.act, frame)
}

/// 怪物动作采样（同人物：空动作段与"播完"都退回站立/走路）。
pub(crate) fn monster_sample(
    race_img: u8,
    held: Option<u32>,
    held_ms: u32,
    moving: bool,
    dead: bool,
) -> (mir2_core::actor::MAct, u16) {
    use mir2_core::actor as A;
    // 死了 ⇒ **尸骨**：永远停在 `Die` 的最后一帧。
    //
    // ⚠️ 少了这一条：`Die` 播完（`held_ms >= act.duration_ms()`，`Die` 是"一次播完"的）
    // 就退回**站立** —— 画面上就是"死而复生"（用户 2026-10-09 报的"死后没有显示尸体状态"）。
    // 原版靠 `m_boDeath` + `m_nCurrentAction` 收尾后一直画尸骨那帧，同一件事。
    if dead {
        let die = A::mon_actions(race_img)[A::MAct::Die as usize];
        if die.frame > 0 {
            return (A::MAct::Die, die.last_frame());
        }
        // 该品种没有死亡段（`Die.frame == 0`）⇒ 别硬画（那是别的品种的图），退回常规
    }
    let mut pose = A::monster_pose(race_img, held, moving);
    let mut act = A::mon_actions(race_img)[pose.act as usize];
    let elapsed = if !pose.looping && held_ms >= act.duration_ms() + ACTION_TAIL_MS {
        // 播完**且过了尾巴** ⇒ 回到站立（见 `ACTION_TAIL_MS`）
        pose = A::monster_pose(race_img, None, moving);
        act = A::mon_actions(race_img)[pose.act as usize];
        0
    } else {
        held_ms
    };
    let frame = if pose.looping {
        act.frame_at(elapsed)
    } else {
        act.frame_once(elapsed)
    };
    (pose.act, frame)
}

/// 取"本体"精灵（容器名 + 图号）。取不到返回 `None` ⇒ 调用方退回标记。
///
/// NPC（kind=2）恒为 `None`：原版走 `Npc.wzl`，本套素材没有（docs/assets.md §2）。
pub(crate) fn body_sprite(
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
) -> Option<(&'static str, u32)> {
    use mir2_core::actor as A;
    let f = e.feature.as_ref()?;
    let dir = A::dir_of(e.dir);
    let (held, held_ms) = anim.map_or((None, 0), |a| (a.action, a.action_ms(now)));
    let moving = anim.is_some_and(|a| a.moving(now));
    // 走/跑动画用**连续相位**（不随每格重置）；`e.run` 决定播 Walk 还是 Run
    let walk_ms = anim.map_or(0, |a| a.walk_ms(now));
    match e.kind {
        // 玩家：本体在 Hum.wzl，部位号 = Dress（服务端已经算成 `Shape*2+性别`）
        0 => {
            let (act, frame) = human_sample(held, held_ms, moving, e.run, walk_ms, e.dead);
            Some((A::HUM_LIB, A::human_index(f.dress as u8, act, dir, frame)))
        }
        // 怪物：容器与块起点都由 Appr 定（`Mon<Appr/10+1>`）
        1 => {
            let appr = f.appr as u16;
            let lib = A::mon_container(appr)?;
            let (act, frame) = monster_sample(f.race_img as u8, held, held_ms, moving, e.dead);
            Some((
                lib,
                A::monster_index(appr, f.race_img as u8, act, dir, frame),
            ))
        }
        // NPC：容器是 `Npc.wzl`，图号 = 块起点(Appr) + 站立段在本方向的起点。
        //
        // 原版 `TNpcActor`（`Actor.pas:2866-2896/3028-3046`）：块起点 `GetNpcOffset(appr)`、
        // 帧号 `GetRaceByPM(race, appr)` 的 `ActStand` —— NPC 站着不动 ⇒ 段内帧恒 0。
        // race 与 appr 都由服务端给（商人 race 恒 50、appr 是"主要部分"那一列；
        // `Npcs.txt` 的 NPC 则是各自的两列，见 `data/npc.go`）。
        2 => Some((
            A::NPC_LIB,
            A::npc_index(f.race_img as u8, f.appr as u16, dir, 0),
        )),
        _ => None,
    }
}

/// 一个实体**画出来**的那个框（身体 + 武器的并集）—— **悬停命中**用它。
///
/// # 为什么不能按"它在哪一格"判（用户 2026-10-09 报的"没有高亮"）
///
/// 图的锚点、透明边、朝向都会让"画出来的身体"和"它所在的格"错开：高身材的怪
/// （鹿/牛）身体能高出它那格一格多，低矮的（蛇/虫）又贴着格底 ⇒ 按格子判就是
/// "明明指着身体却没反应"。原版/Crystal 都是按**画面上的矩形/像素**判的
///（`GetAttackFocusCharacter`、`MapObject.MouseOver`）。
///
/// 死了的不参与（原版 `g_FocusCret` 画之前要 `IsValidActor`，Crystal 显式排除 `Dead`）。
/// 取不到精灵时退回"占格框"（`draw_entity_marker` 画的就是它）。
pub(crate) fn actor_rect<'a, T>(
    tc: &'a TextureCreator<T>,
    sprites: &mut SpriteCache<'a>,
    dir_assets: &Path,
    cam: (f32, f32),
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
) -> Option<FRect> {
    if e.dead {
        return None;
    }
    let (fx, fy) = anim.map_or((e.x as f32, e.y as f32), |a| a.draw_pos((e.x, e.y), now));
    let (px, py) = cell_to_screen_f(cam, fx, fy);
    let mut hit: Option<FRect> = None;
    for layer in [
        body_sprite(e, anim, now),
        hair_sprite(e, anim, now),
        weapon_sprite(e, anim, now),
    ] {
        let Some((lib, idx)) = layer else { continue };
        if sprites.ensure(tc, dir_assets, lib, idx).is_none() {
            continue;
        }
        let Some(t) = sprites.texs.get(&(lib, idx)) else {
            continue;
        };
        // ⚠️ 命中框用**不透明内容框**（`bbox`），不是整图：人物/怪图四周的透明边
        // 少则几像素、多则半张图，按整图判就是"明明指着身体却没反应"
        //（用户 2026-10-10 第 4 条）。取不到 bbox（全透明？）⇒ 退回整图。
        let r = match t.bbox {
            Some((bx, by, bw, bh)) => FRect::new(
                px + t.anchor_x as f32 + bx as f32,
                py + t.anchor_y as f32 + by as f32,
                bw as f32,
                bh as f32,
            ),
            None => {
                let q = t.tex.query();
                FRect::new(
                    px + t.anchor_x as f32,
                    py + t.anchor_y as f32,
                    q.width as f32,
                    q.height as f32,
                )
            }
        };
        hit = Some(match hit {
            Some(h) => FRect::new(
                h.x.min(r.x),
                h.y.min(r.y),
                (h.x + h.w).max(r.x + r.w) - h.x.min(r.x),
                (h.y + h.h).max(r.y + r.h) - h.y.min(r.y),
            ),
            None => r,
        });
    }
    hit.or(Some(FRect::new(px, py, UNIT_X as f32, UNIT_Y as f32)))
}

/// 取"武器层"（只有玩家有；怪物的 `m_btMonsterWeapon` 在我们数据里恒 0）。
pub(crate) fn weapon_sprite(
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
) -> Option<(&'static str, u32)> {
    use mir2_core::actor as A;
    if e.kind != 0 {
        return None;
    }
    let f = e.feature.as_ref()?;
    if f.weapon == 0 {
        return None; // 空手
    }
    let (held, held_ms) = anim.map_or((None, 0), |a| (a.action, a.action_ms(now)));
    let moving = anim.is_some_and(|a| a.moving(now));
    // ⚠️ 与 `body_sprite` **同一份采样**（同样的 run/相位）：各算各的会让武器与身体错帧
    let walk_ms = anim.map_or(0, |a| a.walk_ms(now));
    // 死了不画武器（尸骨手上没有刀）
    if e.dead {
        return None;
    }
    let (act, frame) = human_sample(held, held_ms, moving, e.run, walk_ms, false);
    Some((
        A::WEAPON_LIB,
        A::human_index(f.weapon as u8, act, A::dir_of(e.dir), frame),
    ))
}

/// 取"头发层"（只有玩家有；服务端发的 `hair` 是**原始发型号**）。
///
/// 层序：本体 → **头发** → 武器（官方 `Actor.pas:3835-3848` 是"武器(在身后时) → 身体 → 头发"，
/// 我们武器统一画在最上，于是头发夹在中间 —— 免得剑压住头发）。
///
/// 发型 `0` 不画：`hair2.wzl` 的块 0（发型 0、男）**本来就是空的**（光头）。
pub(crate) fn hair_sprite(
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
) -> Option<(&'static str, u32)> {
    use mir2_core::actor as A;
    if e.kind != 0 {
        return None;
    }
    let f = e.feature.as_ref()?;
    if f.hair == 0 || e.dead {
        return None;
    }
    // ⚠️ 与 `body_sprite`/`weapon_sprite` **同一份采样**（同样的 run/相位）：各算各的会错帧
    let (held, held_ms) = anim.map_or((None, 0), |a| (a.action, a.action_ms(now)));
    let moving = anim.is_some_and(|a| a.moving(now));
    let walk_ms = anim.map_or(0, |a| a.walk_ms(now));
    let (act, frame) = human_sample(held, held_ms, moving, e.run, walk_ms, false);
    // 性别位从 `dress` 低位取（服务端发的 dress = 衣服Shape*2 + 性别）
    let sex = (f.dress & 1) as u8;
    Some((
        A::HAIR_LIB,
        A::hair_index(f.hair as u8, sex, act, A::dir_of(e.dir), frame),
    ))
}

/// 画一个实体：**先精灵、取不到再退标记**，最后统一画名字与血条。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_actor<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    names: &mut font::TextCache<'a>,
    sprites: &mut SpriteCache<'a>,
    dir_assets: &Path,
    cam: (f32, f32),
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
    name: &str,
    hp: u32,
    max_hp: u32,
    color: Color,
    // 鼠标悬停在它身上（身体**再画一遍**做高亮；见 `HOVER_TINT`）
    highlight: bool,
    // 画 `当前/总量`（只有自己那份传 true）
    show_numbers: bool,
) -> Result<(), sdl3::Error> {
    // 补间后的位置（不做插值的话，精灵是一格一格跳的）
    let (fx, fy) = anim.map_or((e.x as f32, e.y as f32), |a| a.draw_pos((e.x, e.y), now));
    let (px, py) = cell_to_screen_f(cam, fx, fy);

    let body = body_sprite(e, anim, now);
    if body.is_none() {
        // 没有精灵（NPC / 素材缺失 / 图号取不到）：退回标记，**不静默什么都不画**
        return draw_entity_marker(
            canvas,
            tc,
            names,
            cam,
            e.x,
            e.y,
            color,
            name,
            hp,
            max_hp,
            e.dir,
            highlight,
            show_numbers,
        );
    }
    // 本体 → 头发 → 武器（原版层序：武器压在身体上面；头发夹在中间，免得被剑压住）
    let layers = [body, hair_sprite(e, anim, now), weapon_sprite(e, anim, now)];
    for &(lib, idx) in layers.iter().flatten() {
        sprites.ensure(tc, dir_assets, lib, idx);
    }
    for &(lib, idx) in layers.iter().flatten() {
        draw_actor_layer(canvas, sprites, lib, idx, px, py, None)?;
    }
    // 悬停高亮：**再画一遍** —— 原版就是 `g_FocusCret.DrawChr(..., blend=TRUE)`
    //（`PlayScn.pas:1369-1376` ⇒ `DrawEffSurface` → `DrawBlend`）；
    // Crystal 的 `MouseObject.DrawBlend()`（`GameScene.cs:10973-10976`，0.3 半透明）也是同一手。
    if highlight {
        for &(lib, idx) in layers.iter().flatten() {
            draw_actor_layer(canvas, sprites, lib, idx, px, py, Some(HOVER_TINT))?;
        }
    }
    // 名字/血条挂在**精灵的头顶**：图自带锚点（常是负的），`py` 是格子左上角 ——
    // 直接挂 `py` 会压在身体上（实测那一版就是）。
    let head = layers
        .iter()
        .flatten()
        .filter_map(|&(lib, idx)| {
            sprites
                .texs
                .get(&(lib, idx))
                .map(|t| py + t.anchor_y as f32)
        })
        .fold(py, f32::min);
    draw_name_bar(
        canvas,
        tc,
        names,
        px + UNIT_X as f32 / 2.0,
        head,
        name,
        hp,
        max_hp,
        color,
        highlight,
        show_numbers,
    )
}

/// 画一层 actor 精灵（本体 / 武器）。`tint = None` ⇒ 原色不透明。
///
/// ⚠️ 两种情况都**显式设一遍**颜色与透明度：`set_color_mod`/`set_alpha_mod` 是粘在
/// **贴图**上的，而贴图是缓存共用的 —— 高亮完不复位，下一帧所有精灵都会带暖色。
/// （`core::select_ui` 那边踩过同样的坑，见 `UiCache::draw_tint` 的说明。）
pub(crate) fn draw_actor_layer(
    canvas: &mut WindowCanvas,
    sprites: &mut SpriteCache<'_>,
    lib: &'static str,
    idx: u32,
    px: f32,
    py: f32,
    tint: Option<(u8, u8, u8, u8)>,
) -> Result<(), sdl3::Error> {
    let Some(t) = sprites.texs.get_mut(&(lib, idx)) else {
        return Ok(());
    };
    let q = t.tex.query();
    let (r, g, b, a) = tint.unwrap_or((255, 255, 255, 255));
    t.tex.set_color_mod(r, g, b);
    t.tex.set_alpha_mod(a);
    canvas.copy(
        &t.tex,
        None::<FRect>,
        FRect::new(
            px + t.anchor_x as f32,
            py + t.anchor_y as f32,
            q.width as f32,
            q.height as f32,
        ),
    )
}

/// 画一个实体标记（**降级路径**：拿不到精灵时用，也让人一眼看出"这里本该有东西"）。
///
/// ⚠️ **为什么先画标记而不是精灵**：actor 的图号公式（`raceImg/weapon/hair/dress` →
/// `Hum.wzl` / `Objects<N>.wzl` 里的第几张，还要按朝向/动作分块）尚未提取，
/// 那属于 M2 的"角色/怪物动画状态机"；且本套素材里 `Hair.wzl` 是空壳。
/// 标记先把"位置/朝向/名字/血量"这条链验通 —— 换精灵时只改这一个函数。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_entity_marker<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    names: &mut font::TextCache<'a>,
    cam: (f32, f32),
    cx: i32,
    cy: i32,
    color: Color,
    name: &str,
    hp: u32,
    max_hp: u32,
    dir: i32,
    // 鼠标悬停在它身上 ⇒ 名字画亮（标记这条路没有"再画一遍"可用）
    highlight: bool,
    // 画 `当前/总量`（只有自己那份传 true）
    show_numbers: bool,
) -> Result<(), sdl3::Error> {
    let (sx, sy) = cell_to_screen(cam, cx, cy);
    // 视口外直接跳过（地图比视口大得多）
    if (sx + UNIT_X as f32) < 0.0
        || sx > WIN_W as f32
        || (sy + UNIT_Y as f32) < BAR_TOP
        || sy > WIN_H as f32
    {
        return Ok(());
    }
    // 占格框（内缩一点，免得与调试格网糊在一起）
    canvas.set_draw_color(color);
    canvas.draw_rect(FRect::new(
        sx + 8.0,
        sy + 2.0,
        UNIT_X as f32 - 16.0,
        UNIT_Y as f32 - 4.0,
    ))?;
    // 朝向：从格中心往外一小段
    let (dx, dy) = dir_delta(dir);
    if dx != 0.0 || dy != 0.0 {
        let (mx, my) = (sx + UNIT_X as f32 / 2.0, sy + UNIT_Y as f32 / 2.0);
        canvas.draw_line(
            FPoint::new(mx, my),
            FPoint::new(mx + dx * 12.0, my + dy * 8.0),
        )?;
    }
    draw_name_bar(
        canvas,
        tc,
        names,
        sx + UNIT_X as f32 / 2.0,
        sy,
        name,
        hp,
        max_hp,
        color,
        highlight,
        show_numbers,
    )
}

/// 名字 + 血条（精灵与标记两条路共用；名字居中在**格子中心**上方）。
///
/// 血条只在"受了伤"时画，否则一屏全是条。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_name_bar<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    names: &mut font::TextCache<'a>,
    center_x: f32,
    cell_top: f32,
    name: &str,
    hp: u32,
    max_hp: u32,
    color: Color,
    // 悬停 ⇒ 名字换亮色（原版悬停只换光标/加一遍混合；名字亮一点是最省事的等价反馈）
    highlight: bool,
    // 画 `当前/总量`（**只有自己**：参考图里玩家头顶带数值，怪只给一条血条）
    show_numbers: bool,
) -> Result<(), sdl3::Error> {
    // ⚠️ 名字必须走**真字体**（`font::TextCache`）：SDL3 那个 8×8 调试字体
    // **只认 ASCII** ⇒ 中文名字一个字都画不出来（用户 2026-10-09 报的"没有显示名字，
    // 更像字体问题"）；选角/登录早就换成真字体了（D-25），世界里这条是漏的。
    //
    // 版式（照参考图）：**血条在最上、名字在血条下面、再往下才是人**。
    let label = trunc(name, 12);
    let col = if highlight { C_HOVER_NAME } else { color };
    names.draw(
        canvas,
        tc,
        &label,
        center_x - names.width(&label) / 2.0,
        cell_top - names.line_height() - 2.0,
        (col.r, col.g, col.b),
        // 白字黑边（原版 `BoldTextOut` 就是描一遍黑边，见 font.rs 的说明）
        Some((0, 0, 0)),
    )?;
    // 血条：自己**一直画**（参考图里自己的条常驻），别人只在掉血时画（否则一屏全是条）
    if max_hp > 0 && (hp < max_hp || show_numbers) {
        let w = UNIT_X as f32 - 16.0;
        let frac = (hp as f32 / max_hp as f32).clamp(0.0, 1.0);
        let y = cell_top - names.line_height() - 10.0;
        canvas.set_draw_color(Color::RGB(40, 40, 40));
        canvas.fill_rect(FRect::new(center_x - w / 2.0, y, w, 5.0))?;
        canvas.set_draw_color(Color::RGB(220, 60, 60));
        canvas.fill_rect(FRect::new(center_x - w / 2.0, y, w * frac, 5.0))?;
        if show_numbers {
            // `当前/总量`（参考图：压在这条上）
            let t = format!("{hp}/{max_hp}");
            names.draw(
                canvas,
                tc,
                &t,
                center_x - names.width(&t) / 2.0,
                y + 5.0 - names.line_height(),
                (255, 255, 255),
                Some((0, 0, 0)),
            )?;
        }
    }
    Ok(())
}

//! MIR2 1.76 客户端 —— 开发期查看器（两个模式）
//!
//! * **登录界面**：窗口 / 文本输入 / 2D 渲染（M0 的 SDL3 落地验证）
//! * **地图视图**：从 M2PK 容器加载真实 `.map`，绘制**三层**（地表 `Tiles` /
//!   中间 `SmTiles` / 前景 `Objects<N>`）—— M1「地图加载」的验收
//!
//! 渲染几何来自官方客户端 `Grobal2.pas:45`：`UNITX=48`、`UNITY=32`（逻辑格 48×32）。
//! 实测图块尺寸：`Tiles` = 96×64（**2×2 格** ⇒ 只在 x、y 皆为偶数的格上画）、
//! `SmTiles` = 48×32、`Objects` = 48×宽×不定高（后两者每格都画）。
//!
//! 资产目录解析：`$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data`；
//! 容器路径：`$MIR2_MAP_CONTAINER` → `assets/map/maps.m2pk`。找不到就降级显示，不崩。
//!
//! 屏幕文字用 SDL3 内置 8x8 调试字体，**只认 ASCII**。
//!
//! **音频**（`M` 切音乐 / `N` 切音效）：
//!
//! * **规格**在 `mir2_core::sound`（原版 `SoundUtil.pas` 的编号表、`Actor.pas` 的地形→脚步、
//!   `sound.lst` 的"编号 → 文件"）；**发声**在 `audio.rs`（SDL3 软件混音：多路叠加 + 循环）；
//! * 音频目录：`$MIR2_AUDIO_DIR` / `$MIR2C_WAV` → 美术目录旁 → 仓库旁的 `mir2c/wav`；
//! * 场景 BGM：登录 `log-in-long2.wav`、选角 `sellect-loop2.wav`、自己死亡 `game over2.wav`
//!   （`SoundUtil.pas:31-34`）；
//! * ⚠️ **进图音乐没接**：原版走"服务端下发地图音乐号 + `Music/<号>.mp3`"
//!   （`SoundUtil.pas:219`、`ClMain.pas:5222`），我们协议里还没有那个字段、
//!   手上也没有 mp3（`sound::map_music` 里有实测）。
//!
//! **连服务端**（B 阶段，`C` 键或环境变量）：
//!
//! ```text
//! MIR2_SERVER=127.0.0.1:7500 MIR2_SESSION=7 cargo run -p mir2-app
//! ```
//!
//! * `MIR2_SESSION` 是**已认证的会话号** —— 新协议的 `Login` 还没实现
//!   （口令怎么过网络未定，见 D-24），所以只能认领一个既有会话；
//! * 连上之后：相机跟着自己、方向键 = 走一步（离线时仍是平移镜头）、
//!   视野内的实体画成**标记**（位置/朝向/名字/血量）；
//! * 实体画的是**真精灵**（角色/怪物）：图号公式在 `mir2_core::actor`（原版逐条翻译，
//!   出处都注在那边）。取不到精灵时**退回标记**（`draw_entity_marker`）——
//!   NPC 要 `Npc.wzl`、头发要 `Hair.wzl`，本套素材缺失（docs/assets.md §2）。

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use mir2_core::m2pk::Archive;
use mir2_core::map::{Layer, Lib, Map, TileDraw, LAYERS_ALL, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::event::{Event, WindowEvent};
use sdl3::keyboard::{Keycode, Mod};
use sdl3::mouse::{Cursor, MouseButton, SystemCursor};

mod audio;
mod font;
mod login;
mod select;
mod ui;
use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
// 注：`WindowContext` 在 sdl3 里是私有类型、不可具名，
// 故凡是需要纹理创建器的地方一律对类型参数 `T` 泛化。
use sdl3::render::{
    BlendMode, FPoint, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};
use sdl3::EventPump;

/// 打开一个图库：**美术容器优先**（`assets/image/images.m2pk`，`kind=3`），
/// 容器里没有这个库（或压根没容器）就回退裸目录的 `.wzl` + `.wzx`。
///
/// 两条载体解出的像素**逐字节相同**（像素解码只有一份，见 `mir2_core::wzl::to_rgba`），
/// 所以 `app` / `ui` / 各缓存统一走这一个入口，不必各自判断。
///
/// 容器句柄是进程级懒打开的（`mir2_core::image_lib::art_archive`）—— 只把索引区
/// 读进内存（全量 1.4 GB，不能整文件读），图库内容按组取。
pub(crate) fn open_lib(dir: &Path, name: &str) -> Option<Wzl> {
    let art = mir2_core::image_lib::art_archive();
    Wzl::open_preferring(art.as_ref(), dir, name).ok()
}

/// 界面**设计尺寸**（原版素材与版式都是按 800×600 做的：登录背景、选角面板、地图视口…）。
///
/// # ⚠️ 现在它**就是**窗口大小（1024×768），画面 1:1，不再缩放（2026-10-09 改）
///
/// 之前是 800×600 的设计空间 + SDL 逻辑呈现放大到 1024×768（×1.28）—— 用户一眼看出
/// 来了：「感觉被拉伸了？画质比原版更粗糙，且图形更大」。**放大 1.28 倍**正是那个观感：
/// 像素被抽成 1.28 倍、又不是整数倍 ⇒ 又糊又"大」（原版 1024×768 是真按 1024 画的：
/// 视口 ±12 格、界面素材居中不缩放 —— `PlayScn.pas` 的 `SWH1024` 分支、以及登录界面
/// `(SCREENWIDTH-800) div 2` 那句）。
///
/// 于是：**世界按 1024×768 原生渲染**（视口更大更清楚），登录/选角那套 800×600 素材
/// 由各自的 `Layout::build(win, ...)` **居中**摆（它们本来就是这么写的，见 D-27/D-42）。
///
/// 窗口仍可拉大拉小（`.resizable()`）；那时才轮到 SDL 的逻辑呈现去等比缩放 + 留边。
/// 鼠标事件用 `Event::get_converted_coords` 换算回这个空间。
const WIN_W: u32 = 1024;
const WIN_H: u32 = 768;

/// 登录/选角那两屏的**设计空间**（素材原生 800×600）。
///
/// `login_ui::Layout` / `select_ui::Layout` 都在这个空间里算（它们只认"窗口"尺寸），
/// 画到 [`WIN_W`]×[`WIN_H`] 的画布上时由 [`ui::UI_SCALE`] 整体乘 1.28 —— 也就是
/// **整屏拉伸铺满**，和官方客户端一样（见 `ui::UI_SCALE` 的说明与 D-52）。
///
/// ⚠️ 所以那两屏的**鼠标坐标**也得先除回这个空间（`ui::ui_inv_pt`），
/// 而世界那套（`screen_to_cell` 等）用的是画布坐标，**不要**混。
const UI_WIN: (u32, u32) = (800, 600);

/// 启动时的窗口大小（用户随后可以随意拉大拉小；改这里**不影响版式**，见 [`WIN_W`]）。
///
/// ⚠️ 别设得比设计尺寸还小：逻辑呈现只保证"完整可见 + 不变形"，不保证 1:1 以上
/// —— 比 800×600 小就会把界面**缩小**（糊）。这条**编译期**就钉住。
///
/// ⚠️ 窗口与设计尺寸**不需要**同比例：`LETTERBOX` 会等比缩放并在多出来的那一边留黑边
///（比例不等只意味着有黑边，不会变形）。
const WINDOW_W: u32 = 1024;
const WINDOW_H: u32 = 768;

const _: () = assert!(
    WINDOW_W >= WIN_W && WINDOW_H >= WIN_H,
    "默认窗口比设计尺寸小 ⇒ 界面会被缩小"
);

/// 启动窗口大小：`MIR2_WINDOW=<宽>x<高>`（例 `1280x960`；`docs/use.md` 的三档是
/// 800×600 / 1024×768 / 1280×960）。填错或比设计尺寸小就忽略并退回默认值。
///
/// ⚠️ 这三档**不是三个不同的版式** —— 版式永远活在 800×600 里，改的只是"铺到多大"。
/// 窗口本身也可拉（`.resizable()`）。
fn window_size() -> (u32, u32) {
    let Ok(s) = std::env::var("MIR2_WINDOW") else {
        return (WINDOW_W, WINDOW_H);
    };
    match parse_window(&s) {
        Some((w, h)) => {
            println!("[mir2-app] 窗口 = {w}×{h}（MIR2_WINDOW）");
            (w, h)
        }
        None => {
            println!(
                "[mir2-app] MIR2_WINDOW=\"{s}\" 不认（要 <宽>x<高> 且不小于 800×600），用默认"
            );
            (WINDOW_W, WINDOW_H)
        }
    }
}

/// `MIR2_WINDOW` 的解析（纯函数，便于单测）：`"1280x960" → Some((1280,960))`；
/// 格式不对、或比设计尺寸小 ⇒ `None`（宁可退回默认，也不把界面缩小）。
fn parse_window(s: &str) -> Option<(u32, u32)> {
    let (w, h) = s.split_once(['x', 'X'])?;
    let (w, h): (u32, u32) = (w.trim().parse().ok()?, h.trim().parse().ok()?);
    (w >= WIN_W && h >= WIN_H).then_some((w, h))
}

/// 主显示器上**真正能放窗口**的那块（去掉菜单栏 / 任务栏 / Dock）。拿不到 ⇒ `None`。
///
/// 存在理由：`1024×768` 的屏上，标题栏一占，客户区就放不下 1024×768 了 ——
/// 那时要么缩画面（糊）、要么裁边（见 [`apply_presentation`]）。
fn usable_bounds(video: &sdl3::VideoSubsystem) -> Option<(u32, u32)> {
    let d = video.get_primary_display().ok()?;
    let r = d.get_usable_bounds().ok()?;
    Some((r.width(), r.height()))
}

/// 把想要的窗口尺寸夹进可用区域（纯函数，便于单测）。
fn fit_window(want: (u32, u32), usable: (u32, u32)) -> (u32, u32) {
    (want.0.min(usable.0), want.1.min(usable.1))
}

/// 挑呈现模式（纯函数，便于单测）：窗口装得下 [`WIN_W`]×[`WIN_H`] 才允许等比缩放。
fn present_mode(win: (u32, u32)) -> sdl3_sys::render::SDL_RendererLogicalPresentation {
    if win.0 >= WIN_W && win.1 >= WIN_H {
        sdl3_sys::render::SDL_LOGICAL_PRESENTATION_LETTERBOX
    } else {
        sdl3_sys::render::SDL_LOGICAL_PRESENTATION_DISABLED
    }
}

/// 呈现策略（用户 2026-10-09 第 4 条）：**装得下就等比放大，装不下就 1:1 裁切，
/// 绝不缩小、绝不拉伸**。
///
/// - 窗口 ≥ 1024×768 ⇒ `LETTERBOX`：等比放大 + 留边（窗口更大时画面跟着变大，不变形）；
/// - 窗口 < 1024×768（例：1024×768 的屏 + 标题栏/菜单栏 ⇒ 客户区只有 1024×743）
///   ⇒ `DISABLED`：**1:1 画**，右边/下边多出去的那点直接裁掉。
///   注意 `LETTERBOX` 在这种情形下**会连画面一起缩**（1024×743 ⇒ 0.967 倍）——
///   整屏糊一档，而在它和"裁掉最外圈的石头边框"之间，用户明确要后者。
///
/// ⚠️ 两种模式下**鼠标换算都不用自己写**：SDL 的 `Event::get_converted_coords`
/// 认的正是这里的口径（缩放 / 留边 / 裁切它都管）。
fn apply_presentation(canvas: &mut WindowCanvas) -> Result<(), String> {
    let (w, h) = canvas.output_size().map_err(|e| e.to_string())?;
    canvas
        .set_logical_size(WIN_W, WIN_H, present_mode((w, h)))
        .map_err(|e| format!("设置逻辑呈现失败: {e}"))
}

/// 地图视图的顶部信息条高度。
///
/// ⚠️ **0**：调试信息从"一条不透明的信息条"改成**左上角叠加**（用户 2026-10-09 要求
/// 「移动至左上角排列，叠加在游戏内容上」）—— 世界因此铺满整屏（原版也是铺满的，
/// 底下那块是 HUD 面板**盖**上来的，不是把视口切掉）。
const BAR_TOP: f32 = 0.0;
/// 底部条高度：同样归零（那儿现在是 HUD 操作面板）。
const BAR_BOTTOM: f32 = 0.0;
/// 地图可视区高度。
const VIEW_H: f32 = WIN_H as f32 - BAR_TOP - BAR_BOTTOM;

// ---------- 底部操作面板（HUD）----------
//
// 版式与图号**全部照抄原版**（`FState.pas` 的 `TFrmDlg.DBottomDirectPaint`）：
//
// ```text
// BOTTOMBOARD800 = 1;                          // Prguse[1]，800×251，画在 SCREENHEIGHT - h
// 上半部 120px 透明（色键）/ 下半部不透明
// Images[4] 画在 (40, btop+91)                 // 球：左半红=HP、右半蓝=MP，按比例裁
//   HP 那一半：rc.Right := Right div 2 - 1; rc.Top := Round(rc.Bottom / MaxHP * (MaxHP - HP))
// PomiTextOut(660, SCREENHEIGHT-104)           // 等级（Prguse[30..39] = '0'..'9'、[40] = '-'）
// Images[7] 画在 (666, SCREENHEIGHT-73)        // 经验条
// 聊天文字 (209, SCREENHEIGHT-128)，行距 13
// ```
//
// ⚠️ 我们**没有** `Images[7]` 要用的经验值（协议 `Ability` 里没有 exp/max_exp）⇒
// 经验条先留空并记在 D-49；`[8]/[9]` 那类装饰件同理。
/// 底部操作面板（`Prguse[1]`，800×251）。
const HUD_BOARD: u32 = 1;
/// 血/魔法球（`Prguse[4]`，92×90；左半红=HP、右半蓝=MP）。
const HUD_ORB: u32 = 4;
/// 经验条（`Prguse[7]`，76×13）。
///
/// ⚠️ **暂时画不出来**：协议 `Ability` 没有 `exp/max_exp`（原版 `SM_ABILITY` 有），
/// 服务端也没下发 ⇒ 先备着，等协议补上再画（D-49）。
#[allow(dead_code)]
const HUD_EXP: u32 = 7;
/// 等级数字的第一张（`Prguse[30..39]` = '0'..'9'，8px 一位）。
const HUD_DIGIT0: u32 = 30;
/// 球在**面板坐标**里的落点（原版 `(40, btop+91)`）。
const ORB_AT: (f32, f32) = (40.0, 91.0);
/// 面板**左右两块**的原始宽度。
///
/// ⚠️ 这两块必须 **1:1**（左边是球、右边是按钮/状态行，一拉就变形）；中间那块
/// （聊天框，本来就是矩形框）按窗口宽度**拉伸**。
/// 为什么需要这个：我们这套素材只有 **800 宽**的面板 —— 原版 1024 走
/// `BOTTOMBOARD1024 = Prguse[2]`，而本套的 `Prguse[2]` 是**空图号**（实测）⇒
/// 只能"左右保持原样、把中间那段拉宽"，而不是把整个面板放大 1.28 倍（那就又糊又变形了，
/// 正是用户 2026-10-09 说的那个观感）。
const HUD_SIDE_W: f32 = 200.0;

/// 右侧那块面板的左边缘（原版 800 版是 600 —— 我们这块宽 200，贴着右边）。
fn hud_right_x() -> f32 {
    WIN_W as f32 - HUD_SIDE_W
}

/// 等级在**屏幕坐标**里的落点（原版 800 版 `(660, SCREENHEIGHT-104)`；660 = 右块 + 60）。
fn level_at() -> (f32, f32) {
    (hud_right_x() + 60.0, WIN_H as f32 - 104.0)
}

/// 经验条在屏幕坐标里的落点（原版 800 版 `(666, …)`；666 = 右块 + 66）。
#[allow(dead_code)] // 与 `HUD_EXP` 同一条：协议还没有 exp，先备着
fn exp_at() -> (f32, f32) {
    (hud_right_x() + 66.0, WIN_H as f32 - 73.0)
}
/// 聊天文字在屏幕坐标里的落点（原版 `(209, SCREENHEIGHT-128)`）与行距。
const CHAT_AT: (f32, f32) = (209.0, WIN_H as f32 - 128.0);
const CHAT_LINE_H: f32 = 13.0;

/// **液面裁切**：按百分比取球的"下半部分"。
///
/// 原版就是这么一个式子（`FState.pas:3784-3795`）：
/// `rc.Top := Round(rc.Bottom / Max * (Max - Cur))`，落点也跟着下移同样的量
/// ⇒ 看得见的永远是**下面 `pct` 那一截**（像球里的液面）。
///
/// 返回 `(液面在球内的 y, 可见高度)`。
fn gauge_band(pct: f32, h: i32) -> (i32, i32) {
    let pct = pct.clamp(0.0, 1.0);
    let top = (h as f32 * (1.0 - pct)).round() as i32;
    (top, (h - top).max(0))
}

/// 内置字体等宽 8px ⇒ 一行能放多少列（两侧各留 1 列边距）。
///
/// 用窗口宽度算，而不是写死列数：窗口加宽后信息条/调试读数/提示条应当铺满，
/// 否则宽出来的部分白放着，长内容（比如探针读数）还会被无谓截断。
const TEXT_COLS: usize = WIN_W as usize / 8 - 2;

/// 右侧信息区每行最大列数（内置字体等宽 8px）。
const INFO_LINE_H: f32 = 16.0;

/// 图块纹理缓存上限；超出就整批丢掉重建（开发期查看器，够用且简单）。
const TILE_CACHE_CAP: usize = 4000;

/// 登录模式可浏览的图库。
const LIBS: &[&str] = &[
    "Prguse", "Hum", "Items", "Mon1", "Tiles", "Magic", "ChrSel", "Effect", "Weapon",
];

// ---------- 配色 ----------
const C_BG: Color = Color::RGB(10, 14, 28);
const C_PANEL: Color = Color::RGB(22, 30, 56);
const C_PANEL_BORDER: Color = Color::RGB(90, 120, 170);
const C_TITLE: Color = Color::RGB(232, 200, 96);
const C_TEXT: Color = Color::RGB(206, 212, 226);
const C_DIM: Color = Color::RGB(120, 132, 156);
const C_FIELD: Color = Color::RGB(8, 10, 20);
const C_CHECKER_A: Color = Color::RGB(34, 38, 52);
const C_CHECKER_B: Color = Color::RGB(26, 30, 42);
const C_OK: Color = Color::RGB(120, 220, 150);
const C_ERR: Color = Color::RGB(232, 120, 120);
// 调试叠加层
const C_GRID: Color = Color::RGB(40, 48, 70);
const C_GRID_GROUND: Color = Color::RGB(70, 110, 200);
const C_GRID_MID: Color = Color::RGB(60, 170, 170);
const C_GRID_FRONT: Color = Color::RGB(210, 140, 60);
const C_CELLBASE: Color = Color::RGB(255, 220, 80);
const C_CROSS: Color = Color::RGB(255, 255, 255);
/// 鼠标下那张图**自己那一格**的高亮色（与鼠标格区分开）
const C_TOPMOST: Color = Color::RGB(255, 90, 220);
/// 联网实体标记的配色（按 `EntityState.kind`：0=玩家 1=怪物 2=NPC）。
const C_ENT_PLAYER: Color = Color::RGB(120, 200, 255);
const C_ENT_MONSTER: Color = Color::RGB(255, 110, 110);
const C_ENT_NPC: Color = Color::RGB(255, 220, 120);
/// 自己（相机跟着它）。
const C_ENT_SELF: Color = Color::RGB(120, 255, 140);
/// 尸体（`Death` 之后、`EntityDisappear` 之前 —— 原版里尸骨会留一会儿）。
const C_ENT_DEAD: Color = Color::RGB(120, 120, 120);
/// **锁定的攻击目标**（左键点怪锁住的那个）—— 名字换成这个颜色，一眼看得出在打谁。
///
/// 原版是用光标/血条高亮标的（`ClMain.pas` 的 `g_TargetCret` + 光标）；我们先用名字色，
/// 省一套贴图（本套素材里也没有"目标框"那种图）。
const C_ENT_TARGET: Color = Color::RGB(255, 236, 140);
/// 鼠标**悬停**那个实体的名字色（比锁定目标再亮一档 —— 两个状态同时出现时要分得出）。
///
/// 照原版：悬停是 `g_FocusCret` + 身体**再画一遍**（`PlayScn.pas:1369-1376`）；
/// Crystal 是 `MouseObject.DrawName()` + `DrawBlend()`（`GameScene.cs:10605 / 10973`）。
const C_HOVER_NAME: Color = Color::RGB(255, 255, 210);
/// HUD 聊天区的配色：系统消息 / 坏消息（原版 `ChatStrs` 每行自带一色，我们只用三档）。
const C_CHAT_SYS: Color = Color::RGB(230, 230, 210);
const C_CHAT_BAD: Color = Color::RGB(255, 120, 120);
/// HUD 里"地图名 + 坐标"那行（左下角）。
const C_HUD_COORD: Color = Color::RGB(255, 236, 180);
/// 精灵纹理缓存上限。与图块缓存同理：越界就整个清掉，不做 LRU ——
/// 地图比视口大得多，走到哪解到哪，记账成本换不来什么。
const SPRITE_CACHE_CAP: usize = 512;

/// 走路**一格**的补间时长（毫秒）—— 与**服务端的移动节流**对齐。
///
/// ⚠️ 出处是 `server/internal/entity/object.go:483` 的 `MoveLimiter`：`MinWalk = 600ms`、
/// `MinRun = 400ms`（**一步 2 格**）。改服务端那儿就得改这里：
/// 补间比它短 = "每格提前到位再干等"（用户 2026-10-08 报的卡顿，原先写死 320 ms 就是这毛病）；
/// 比它长 = 精灵被下一格"拽着走"。
///
/// ⚠️ 这两个常数只给**别人**（怪/其他玩家）—— 它们的节奏由服务端驱动；
/// **自己**用 [`self_move_ms`]（我们自己的发送步频 `WALK_MS`/`RUN_MS`）。
const WALK_STEP_MS: u32 = 600;

/// 跑**一步**（`RUN_STEPS` 格）的补间时长。
///
/// 注意它**不是**一格的时长：跑一步 2 格 ⇒ 每格 200 ms、比走的 600 ms 快三倍
/// —— 这就是原版"跑明显更快"的来源（`GetNextRunXY`，`ClFunc.pas:370-382`）。
const RUN_STEP_MS: u32 = 400;

/// 跑一步的格数（原版 `GetNextRunXY` 一次 +2；斜着跑也是两格）。
const RUN_STEPS: i32 = 2;

/// 一次移动该补间多久：**按实际格数与走/跑算**（别写死一格）。
///
/// 格数用 `max(|dx|,|dy|)`（切比雪夫距离）：斜着走一格 = 一格，斜着跑 = 两格。
fn move_ms(dx: i32, dy: i32, run: bool) -> u32 {
    let cells = dx.abs().max(dy.abs()).max(1) as u32;
    let per_cell = if run {
        RUN_STEP_MS / RUN_STEPS as u32 // 跑：400/2 = 200 ms 一格
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
fn self_move_ms(dx: i32, dy: i32, run: bool) -> u32 {
    let cells = dx.abs().max(dy.abs()).max(1) as u32;
    let per_cell = if run {
        RUN_MS as u32 / RUN_STEPS as u32 // 跑：450/2 = 225 ms 一格
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
fn next_walk_since(was_moving: bool, prev: Instant, now: Instant) -> Instant {
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

/// 伤害飘字的三档亮度（8x8 调试字体只有一档颜色 ⇒ 用亮度代替透明度淡出）。
const C_DMG_HOT: Color = Color::RGB(255, 240, 120);
const C_DMG_MID: Color = Color::RGB(255, 170, 60);
const C_DMG_DIM: Color = Color::RGB(190, 90, 40);

// 图层可见性掩码定义在 core（`map::LAYERS_ALL` / `Layer::bit`）——
// app 与 e2e 都要用它过滤绘制指令，各写一份迟早不一致（plan §4.2 / R-10）。
// 语义：bit0 = 地表、bit1 = 中间、bit2 = 前景。

// ---------- 调试功能的开关（**关掉，不是删掉**）----------
//
// 这两项是当初为**排查贴图/错位问题**做的：一个按层拆开看、一个把格网与
// 各层落点框（辅助线 + 格子坐标）叠在画面上。日常游玩时它们只会碍事
//（遮挡画面、还容易手滑把某一层关掉而以为是渲染坏了）。
//
// ⇒ **默认关闭**，代码一行不动地留着。要调试时把下面改成 `true` 重建即可
//（改这一处就够了：按键、绘制、提示条三处都跟着它走 —— 别去各处注释代码）。
/// `CTRL+1/2/3`（以及 `L` 循环）逐层显隐。
const DEBUG_LAYERS: bool = false;
/// `D` 辅助线与格子坐标叠加层。
const DEBUG_OVERLAY: bool = false;

/// 掩码 → 三字母缩写（G=地表 M=中间 F=前景），隐藏的层显示为 `-`。
fn layers_desc(m: u8) -> String {
    let ch = |bit: u8, on: char| if m & bit != 0 { on } else { '-' };
    format!("{}{}{}", ch(1, 'G'), ch(2, 'M'), ch(4, 'F'))
}

// ---------- 绘制辅助 ----------
fn fill(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    col: Color,
) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.fill_rect(FRect::new(x, y, w, h))
}

fn frame(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    col: Color,
) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_rect(FRect::new(x, y, w, h))
}

fn text(c: &mut WindowCanvas, s: &str, x: f32, y: f32, col: Color) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_debug_text(s, FPoint::new(x, y))
}

fn center_x(s: &str, area_x: f32, area_w: f32) -> f32 {
    area_x + (area_w - s.chars().count() as f32 * 8.0) / 2.0
}

fn trunc(s: &str, cols: usize) -> String {
    s.chars().take(cols).collect()
}

/// 在预览面板里画棋盘格底（证明透明区真的透明）。
fn checkerboard(c: &mut WindowCanvas, x: f32, y: f32, w: f32, h: f32) -> Result<(), sdl3::Error> {
    const T: f32 = 8.0;
    let mut yy = 0.0;
    while yy < h {
        let mut xx = 0.0;
        while xx < w {
            let col = if ((xx / T) as i32 + (yy / T) as i32) % 2 == 0 {
                C_CHECKER_A
            } else {
                C_CHECKER_B
            };
            fill(c, x + xx, y + yy, T.min(w - xx), T.min(h - yy), col)?;
            xx += T;
        }
        yy += T;
    }
    Ok(())
}

// ---------- 路径解析 ----------
// 放在 core（`mir2_core::paths`）：`client/e2e` 也要用同一套规则，
// 两个产物各写一份迟早会在某台机器上不一致（plan §4.2 / R-10）。

// ---------- 图块纹理缓存 ----------
/// 图块缓存键：图库 + `Objects` 的编号 + 图号 + 是否混合。
///
/// 末位是必需的：同一张图在"混合"与"不混合"两种画法下**上传的像素数据不同**
/// （混合件要换成 [`mir2_core::blend::screen_source`] 的 SCREEN 源），
/// 少了它就会把两种画法互相串味。
type TileKey = (Lib, u8, u16, bool);

/// 缓存的一张图块纹理 + 它的锚点（**Alpha 物件**要用锚点定位，见 core 的 `TileDraw`）。
struct TileTex<'a> {
    tex: Texture<'a>,
    anchor_x: i16,
    anchor_y: i16,
}

/// 设置纹理的混合模式。
///
/// `screen = true` 时用 **SCREEN（滤色）** —— 官方"Alpha 物件"的真实语义
/// （`DrawBlend(...,1)` → `Color256Anti`，推导见 [`mir2_core::blend`]）。
/// SDL 的等价物是 `SDL_BLENDMODE_BLEND_PREMULTIPLIED`
/// （`dstRGBA = srcRGBA + dstRGBA*(1-srcA)`），但 `sdl3::render::BlendMode`
/// 只映射了 4 种模式、**没有**这一个，所以走底层常量。
///
/// ⚠️ 这里一旦退回 `BlendMode::Blend`（普通 alpha），光源贴图近黑的外圈
/// 会把背景压暗一半 —— 灯就变成一坨黑斑（实测踩过）。
fn set_texture_blend(tex: &mut Texture<'_>, screen: bool) {
    if screen {
        // SAFETY: `tex.raw()` 是 SDL 持有的有效纹理指针；该函数只写纹理的
        // 混合模式字段，失败时返回 false（此处不需要回滚，也没有别名风险）。
        unsafe {
            sdl3_sys::render::SDL_SetTextureBlendMode(
                tex.raw(),
                sdl3_sys::blendmode::SDL_BLENDMODE_BLEND_PREMULTIPLIED,
            );
        }
    } else {
        tex.set_blend_mode(BlendMode::Blend);
    }
}

/// 保证 `cache` 里有该图块的纹理；解不出来就返回 `None`（原版也有大量空壳图）。
#[allow(clippy::too_many_arguments)] // 都是渲染所需的最小上下文，不宜再打包成结构体
fn ensure_tile<'a, T>(
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    cache: &mut HashMap<TileKey, TileTex<'a>>,
    dir: &Path,
    lib_kind: Lib,
    area: u8,
    idx: u16,
    blend: bool,
) -> Option<()> {
    let key = (lib_kind, area, idx, blend);
    if cache.contains_key(&key) {
        return Some(());
    }
    if cache.len() >= TILE_CACHE_CAP {
        cache.clear();
    }
    // 文件名规则是游戏知识，放在 core（Lib::file_name，对应 GetObjs）
    let name = lib_kind.file_name(area);
    let lib = libs
        .entry(name.clone())
        .or_insert_with(|| open_lib(dir, &name))
        .as_ref()?;
    let sprite = lib.decode(idx as usize)?;
    if sprite.is_empty() {
        return None;
    }
    let mut t = tc
        .create_texture(
            PixelFormat::RGBA32,
            TextureAccess::Streaming,
            sprite.width as u32,
            sprite.height as u32,
        )
        .ok()?;
    // 混合件（官方 DrawBlend(...,1) = SCREEN，见 core::blend）：
    // 像素换成"SCREEN 源"（alpha = 亮度）并走**预乘**混合 —— 它的公式
    // `dst = src + dst*(1-srcA)` 与官方的 `src + dst*(1-src/255)` 同形。
    // 绝不能退化成 50% alpha：那会把光源贴图近黑的外圈压暗成黑斑。
    let pixels = if blend {
        mir2_core::blend::screen_source(&sprite.rgba)
    } else {
        sprite.rgba
    };
    set_texture_blend(&mut t, blend);
    t.set_scale_mode(ScaleMode::Nearest);
    t.update(None::<Rect>, &pixels, sprite.width as usize * 4)
        .ok()?;
    cache.insert(
        key,
        TileTex {
            tex: t,
            anchor_x: sprite.anchor_x,
            anchor_y: sprite.anchor_y,
        },
    );
    Some(())
}

/// 按一条 [`TileDraw`] 把图块画出来。
///
/// `'a` 把纹理创建器与缓存绑在一起——`Texture<'a>` 借的是创建器，
/// 少了这层关联编译器就没法确认缓存不会比创建器活得久。
#[allow(clippy::too_many_arguments)]
fn draw_tile<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    cache: &mut HashMap<TileKey, TileTex<'a>>,
    dir: &Path,
    d: &TileDraw,
    origin_y: f32,
    sub: (f32, f32),
) -> Result<(), sdl3::Error> {
    let _ = ensure_tile(tc, libs, cache, dir, d.lib, d.area, d.index, d.blend);
    if let Some(t) = cache.get_mut(&(d.lib, d.area, d.index, d.blend)) {
        let q = t.tex.query();
        // 落点由 core 决定（三层规则 + Alpha 物件用锚点）；
        // 混合在贴图创建时就定好了（见 ensure_tile），这里不再动 alpha_mod。
        let top = d.top_y(q.width as i32, q.height as i32, t.anchor_y as i32);
        let left = d.left_x(t.anchor_x as i32);
        canvas.copy(
            &t.tex,
            None::<FRect>,
            FRect::new(
                // 亚格那半格在这里减掉 ⇒ 地图**逐帧平滑卷动**（见 `cam_parts`）
                left as f32 - sub.0,
                origin_y + top as f32 - sub.1,
                q.width as f32,
                q.height as f32,
            ),
        )?;
    }
    Ok(())
}

/// 调试叠加层：格网 + 各层落点框 + 鼠标十字线与"点哪读哪"的读数。
///
/// 关键辅助：前景图块额外画一条**格的底边黄线** —— 官方规则是"底边对齐格底"，
/// 有这条线就能一眼看出对齐对不对（而不是靠猜）。
fn draw_debug_overlay(
    canvas: &mut WindowCanvas,
    draws: &[TileDraw],
    tiles: &HashMap<TileKey, TileTex<'_>>,
    cam: (f32, f32),
    mouse: (f32, f32),
    layers: u8,
) -> Result<(), sdl3::Error> {
    // 叠加层要和图块**同步**：图块被减掉了亚格偏移（见 `cam_parts`），这里也得减，
    // 否则调试框会比图块偏半格（最多 47px），"点哪读哪"就不可信了。
    let sub = cam_parts(cam).sub;
    // 1) 格网（48×32）
    canvas.set_draw_color(C_GRID);
    let mut gx = -sub.0;
    while gx < WIN_W as f32 {
        canvas.draw_line(FPoint::new(gx, BAR_TOP), FPoint::new(gx, BAR_TOP + VIEW_H))?;
        gx += UNIT_X as f32;
    }
    let mut gy = BAR_TOP - sub.1;
    while gy < BAR_TOP + VIEW_H {
        canvas.draw_line(FPoint::new(0.0, gy), FPoint::new(WIN_W as f32, gy))?;
        gy += UNIT_Y as f32;
    }

    // 2) 各层落点框（与图块同步显隐：关掉的层不留框，免得误判还剩东西）
    for d in draws {
        if layers & d.layer.bit() == 0 {
            continue;
        }
        let Some(r) = rect_of(d, tiles, sub) else {
            continue;
        };
        canvas.set_draw_color(match d.layer {
            Layer::Ground => C_GRID_GROUND,
            Layer::Mid => C_GRID_MID,
            Layer::Front => C_GRID_FRONT,
        });
        canvas.draw_rect(r)?;
        if d.layer == Layer::Front {
            let by = BAR_TOP + d.y as f32 + UNIT_Y as f32 - sub.1;
            canvas.set_draw_color(C_CELLBASE);
            canvas.draw_line(
                FPoint::new(d.x as f32 - sub.0, by),
                FPoint::new(d.x as f32 - sub.0 + UNIT_X as f32, by),
            )?;
        }
    }

    // 3) 鼠标十字线 + 所在格 + 读数
    let (mx, my) = mouse;
    if (BAR_TOP..BAR_TOP + VIEW_H).contains(&my) {
        let (cx, cy) = screen_to_cell(cam, mx, my);
        let (hx, hy) = cell_to_screen(cam, cx, cy);
        canvas.set_draw_color(C_CROSS);
        canvas.draw_rect(FRect::new(hx, hy, UNIT_X as f32, UNIT_Y as f32))?;
        canvas.draw_line(FPoint::new(mx, BAR_TOP), FPoint::new(mx, BAR_TOP + VIEW_H))?;
        canvas.draw_line(FPoint::new(0.0, my), FPoint::new(WIN_W as f32, my))?;

        // 该像素最上层的那一条（绘制顺序里最后命中的；隐藏层不参与）
        let topmost = draws.iter().rev().find(|d| {
            layers & d.layer.bit() != 0
                && rect_of(d, tiles, sub)
                    .is_some_and(|r| mx >= r.x && mx < r.x + r.w && my >= r.y && my < r.y + r.h)
        });
        let line = match topmost {
            Some(d) => {
                let (w, h, ax, ay) = match tiles.get(&(d.lib, d.area, d.index, d.blend)) {
                    Some(t) => {
                        let q = t.tex.query();
                        (
                            q.width as i32,
                            q.height as i32,
                            t.anchor_x as i32,
                            t.anchor_y as i32,
                        )
                    }
                    None => (0, 0, 0, 0),
                };
                let top = d.top_y(w, h, ay);
                let left = d.left_x(ax);
                // ★ 这张图**自己那一格**——大写标注，避免与"鼠标所在格"混淆：
                //   高精灵（实测最高 582px ≈ 18 格）会向上盖住很多格，
                //   不标出它的归属格，就会误以为"图被画错了位置"。
                let cell_x = cam_parts(cam).cell.0 + d.x / UNIT_X;
                let cell_y = cam_parts(cam).cell.1 + d.y / UNIT_Y;
                let (bx, by) = cell_to_screen(cam, cell_x, cell_y);
                // 高亮：它自己的格（亮白）+ 整张图外框（亮白）+ 它的格底线（亮黄）
                canvas.set_draw_color(C_TOPMOST);
                canvas.draw_rect(FRect::new(bx, by, UNIT_X as f32, UNIT_Y as f32))?;
                canvas.draw_rect(FRect::new(
                    left as f32 - sub.0,
                    BAR_TOP + top as f32 - sub.1,
                    w as f32,
                    h as f32,
                ))?;
                canvas.set_draw_color(C_CELLBASE);
                canvas.draw_line(
                    FPoint::new(bx, by + UNIT_Y as f32),
                    FPoint::new(bx + UNIT_X as f32, by + UNIT_Y as f32),
                )?;
                format!(
                    "MOUSE({},{}) TOP={:?}#{} {}x{} OWNS({},{}) BOT={} COVERS {}rows{}",
                    cx,
                    cy,
                    d.layer,
                    d.index,
                    w,
                    h,
                    cell_x,
                    cell_y,
                    by as i32 + UNIT_Y,
                    (h + UNIT_Y - 1) / UNIT_Y,
                    if d.blend { " ALPHA" } else { "" }
                )
            }
            None => format!("MOUSE({cx},{cy})  NO TILE HERE"),
        };
        let ry = BAR_TOP + VIEW_H - 11.0;
        fill(
            canvas,
            0.0,
            ry - 1.0,
            WIN_W as f32,
            12.0,
            Color::RGB(0, 0, 0),
        )?;
        text(canvas, &trunc(&line, TEXT_COLS), 2.0, ry, C_CROSS)?;
    }
    Ok(())
}

// ---------- 视口剔除 ----------

/// 地图视口矩形（与 `draw_map_view` 里 `set_clip_rect` 用的是同一块）。
fn viewport_rect() -> FRect {
    FRect::new(0.0, BAR_TOP, WIN_W as f32, VIEW_H)
}

/// 两个矩形是否相交（半个像素也不相交就返回 false ⇒ 可安全跳过）。
fn intersects(a: &FRect, b: &FRect) -> bool {
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

/// [`rect_of`] 的"冷"版本：**只读 WZL 记录、不解码像素**。
///
/// 用途：前景层按官方要向下多扫 35 行（`core::map::FRONT_ROW_MARGIN`），
/// 那批候选里绝大多数是矮图块、落点远在视口下方。先按记录把框算出来判掉，
/// 就不必为它们做 zlib 解压 + RGBA 转换 + 贴图上传。
///
/// 与 [`rect_of`] 不会打架：两者都用 core 的 `top_y` / `left_x` 定位，
/// 只是尺寸一个取自贴图、一个取自记录（两者必然相同，解码器就按记录建图）。
fn draw_rect_cold(
    libs: &mut HashMap<String, Option<Wzl>>,
    dir: &Path,
    d: &TileDraw,
    sub: (f32, f32),
) -> Option<FRect> {
    let name = d.lib.file_name(d.area);
    let lib = libs
        .entry(name.clone())
        .or_insert_with(|| open_lib(dir, &name))
        .as_ref()?;
    let rec = lib.record(d.index as usize)?;
    if rec.width == 0 || rec.height == 0 {
        return None;
    }
    let top = d.top_y(rec.width as i32, rec.height as i32, rec.anchor_y as i32);
    // 与 `rect_of` 同口径：减掉亚格偏移（真实落点）
    Some(FRect::new(
        d.left_x(rec.anchor_x as i32) as f32 - sub.0,
        BAR_TOP + top as f32 - sub.1,
        rec.width as f32,
        rec.height as f32,
    ))
}

// ---------- 调试工具（D 叠加层 / P 打印清单 / 左键点哪读哪）----------

/// 一条绘制指令**真正画上去**的那个屏幕矩形（**已计入 `top_y` 与亚格偏移 `sub`**）。
///
/// ⚠️ `sub` 必须减掉（`draw_tile` 就是这么画的）：凡是"拿框去和屏幕坐标比"的地方
/// —— 视口剔除、调试框、"点哪读哪" —— 都得用**真实落点**，否则会差最多一格
/// （走动时画面右/下边缘那条**黑带**就是这么来的：图块明明还盖着屏幕，却被判成
/// "在视口外"剔掉了，用户 2026-10-08 报的）。
fn rect_of(d: &TileDraw, tiles: &HashMap<TileKey, TileTex<'_>>, sub: (f32, f32)) -> Option<FRect> {
    let t = tiles.get(&(d.lib, d.area, d.index, d.blend))?;
    let q = t.tex.query();
    let top = d.top_y(q.width as i32, q.height as i32, t.anchor_y as i32);
    let left = d.left_x(t.anchor_x as i32);
    Some(FRect::new(
        left as f32 - sub.0,
        BAR_TOP + top as f32 - sub.1,
        q.width as f32,
        q.height as f32,
    ))
}

/// 这条绘制指令要不要画：**视口剔除**。
///
/// ⚠️ 参数特意收**整个 [`CamParts`]**、而不是"相机 + 亚格偏移两个参数"：
/// 少传/漏减一次亚格偏移，判据就会和分析出来的落点差最多一格 ——
/// 表现是走动时画面边缘**漏画一条黑底**（用户 2026-10-08 报的）。
/// 收成一个值，就没法"只传一半"了。
fn tile_in_view(
    libs: &mut HashMap<String, Option<Wzl>>,
    dir: &Path,
    d: &TileDraw,
    cp: &CamParts,
    layers: u8,
    view: &FRect,
) -> bool {
    layers & d.layer.bit() != 0
        && draw_rect_cold(libs, dir, d, cp.sub).is_some_and(|r| intersects(&r, view))
}

/// 把视口内的绘制清单打到终端（顺序即绘制顺序）——可复制的 debug log。
fn dump_draws(
    draws: &[TileDraw],
    cam: (i32, i32),
    tiles: &HashMap<TileKey, TileTex<'_>>,
    layers: u8,
) {
    println!(
        "\n[draws] 视口内 {} 条（顺序即绘制顺序；top_y 是图块真实落点）  可见层 {}，标 HIDDEN 的当前不画",
        draws.len(),
        layers_desc(layers)
    );
    for (i, d) in draws.iter().enumerate() {
        let (w, h, ax, ay) = match tiles.get(&(d.lib, d.area, d.index, d.blend)) {
            Some(t) => {
                let q = t.tex.query();
                (
                    q.width as i32,
                    q.height as i32,
                    t.anchor_x as i32,
                    t.anchor_y as i32,
                )
            }
            None => (0, 0, 0, 0),
        };
        println!(
            "  [{i:3}] {:<6?} 格({:4},{:4}) 图号={:5} {:3}x{:<4} 锚({:3},{:4}) 格顶y={:5} top_y={:5} x={:4} 库={}{}",
            d.layer,
            cam.0 + d.x / UNIT_X,
            cam.1 + d.y / UNIT_Y,
            d.index,
            w,
            h,
            ax,
            ay,
            d.y,
            d.top_y(w, h, ay),
            d.left_x(ax),
            d.lib.file_name(d.area),
            if layers & d.layer.bit() == 0 {
                " HIDDEN"
            } else if d.ani_frames > 0 {
                " ANI"
            } else {
                ""
            }
        );
    }
}

/// 报告某个视口像素被哪些图块覆盖（按绘制顺序，最后一个在最上层）。
///
/// `layers` 是当前可见性掩码：**被隐藏的层不参与命中**，
/// 否则"关掉前景再点一下"会报出一堆看不见的物件，读数就没法信了。
fn probe_at(
    px: f32,
    py: f32,
    cam: (f32, f32),
    draws: &[TileDraw],
    tiles: &HashMap<TileKey, TileTex<'_>>,
    layers: u8,
) {
    let (cx, cy) = screen_to_cell(cam, px, py);
    println!(
        "\n[probe] 视口像素=({px:.0},{py:.0}) → 格=({cx},{cy})   可见层 {}",
        layers_desc(layers)
    );
    let mut hits = 0;
    for (i, d) in draws.iter().enumerate() {
        if layers & d.layer.bit() == 0 {
            continue;
        }
        let Some(r) = rect_of(d, tiles, cam_parts(cam).sub) else {
            continue;
        };
        if (r.x..r.x + r.w).contains(&px) && (r.y..r.y + r.h).contains(&py) {
            hits += 1;
            println!(
                "  [{i:3}] {:<6?} 格({:4},{:4}) 图号={:5} 尺寸={:.0}x{:.0} 落点=({:.0},{:.0})..({:.0},{:.0}) 库={}",
                d.layer,
                cam_parts(cam).cell.0 + d.x / UNIT_X,
                cam_parts(cam).cell.1 + d.y / UNIT_Y,
                d.index,
                r.w,
                r.h,
                r.x,
                r.y,
                r.x + r.w,
                r.y + r.h,
                d.lib.file_name(d.area)
            );
        }
    }
    if hits == 0 {
        println!("  （该像素没有任何图块覆盖）");
    } else {
        println!("  共 {hits} 条；**最后一条在最上层**");
    }
}

// ---------- 联网（B：app 连上 server）----------

/// app 侧的联网状态。**薄薄一层**：
///
/// - 连接与消息泵在 `mir2-net`（独立线程 → channel）
/// - 握手状态机与世界状态在 `mir2-core`（纯函数，`client/e2e` 用的是**同一份**，见 D-18）
///
/// 这里只负责"把它们按帧推一下、把状态交给渲染"，**不放任何游戏规则**。
struct Net {
    sess: mir2_net::Session,
    entrance: mir2_core::entrance::Entrance,
    world: mir2_core::world::World,
    /// 会话号（v0 的 `session_token` 就是它；`Entrance` 内部也持一份，这里留一份
    /// 是为了把 `Reconnect` 翻成 `Cmd::Reconnect` 时不必从 token 字节里解回来）。
    session: i32,
    /// 给人看的连接状态（连不上/已连接/进图/出错）。
    status: String,
    /// 累计世界变更次数（"世界在动"最直接的观测量）。
    changes: u32,
    /// 伤害飘字：文本 + 格子坐标 + 出生时刻。
    ///
    /// ⚠️ 世界模型（`core::world`）是**没有时钟**的纯状态，只负责把 `Damage` 记进
    /// 一个队列；计时与淡出是渲染层的事（这里才有帧时钟）。
    floaters: Vec<(String, i32, i32, Instant)>,
    /// 连接层给出的结束原因（连不上 / 被断开）。**登录界面靠它弹窗** ——
    /// 少了它，连不上时界面会一直卡在 `CONNECTING ...`（踩过）。
    fail: Option<String>,
    /// 是否已经处理过"刚进世界"那一帧。
    ///
    /// ⚠️ 必须有这个标志：`entrance.in_world()` **每帧都为真**，而下面那个
    /// "状态行变了没"的判据在 `map_name` 为空时（重连直接回世界那条路不带
    /// `ChangeMap`）**也**恒真 ⇒ 直接 `println!` 会变成每帧一行（实测刷了几百行）。
    entered_once: bool,
    /// 建 `Net` 的时刻：只为算"连接 → 进世界"用了多久。
    ///
    /// ⚠️ 这个数字是有用的：曾经有个 bug 让这一段整整多花 20 秒（`flush_entrance`
    /// 的说明），当时是**靠翻服务端日志的时间戳**才发现的。现在它直接打在终端上。
    started: Instant,
    /// 每个实体的**动画状态**（移动的补间进度、动作播放到哪了）。
    ///
    /// ⚠️ 同样只在渲染层：世界模型只存事实（在哪、什么动作），"什么时候发生的"归这里。
    anims: HashMap<u64, ActorAnim>,
    /// 世界侧排出来的音效编号（挨打 / 死亡），由主循环取走播放。
    ///
    /// ⚠️ 为什么要绕这一道：`pump()` 在 `Net` 里，**拿不到音频设备**（那在 `main`
    /// 的作用域）—— 与 `take_damage`（伤害飘字）同一套路：世界只记账，
    /// 谁有时钟/设备谁去表现。
    pending_sfx: Vec<u16>,
    /// HUD 聊天区的行（原版 `ChatStrs`）—— 见 [`Chat`]。
    chat: Chat,
    /// 已经为自己死放过一次声（`self_dead` 会一直为真，不能每帧放）。
    died_once: bool,
    /// 刚死 ⇒ 主循环切 game over 音乐（原版 `Actor.pas:2373-2374`）。
    gameover: bool,
}

impl Net {
    /// 读环境变量连一个服务端。返回 `Net`（**连接是异步的**：结果从事件里回来）。
    ///
    /// ```
    /// MIR2_SERVER=127.0.0.1:7500 MIR2_SESSION=7 cargo run -p mir2-app
    /// ```
    ///
    /// ⚠️ 为什么还要 `MIR2_SESSION`：新协议的 `Login` 还没实现（口令怎么过网络未定，
    /// 见 D-24），所以客户端只能认领一个**既有会话** —— 它由账户服务（或 e2e 测试）建立。
    /// 用**口令**登录（D-24① 挑战应答；口令不上网络，只上证明）。
    ///
    /// ⚠️ 与 `connect()`（认领既有会话）的区别只有"入口不同"：两条路之后
    /// 走的是**同一条尾巴**（列角色 → 选角 → 进世界），见 `core::entrance` 的文件头。
    fn connect_with_password(addr: &str, account: &str, password: &str) -> Result<Net, String> {
        if account.is_empty() {
            return Err("账号不能为空".into());
        }
        if password.is_empty() {
            return Err("口令不能为空".into());
        }
        let char_id: Option<u64> = match std::env::var("MIR2_CHAR") {
            Ok(s) => Some(s.parse().map_err(|_| "MIR2_CHAR 必须是整数".to_string())?),
            Err(_) => None,
        };
        println!("[net] 连接 {addr}（账号 {account}，口令登录）…");
        let sess = mir2_net::Session::spawn(addr.to_string(), "mir2-app".into(), "zh-CN".into());
        let mut entrance = mir2_core::entrance::Entrance::new_with_password(
            account.to_string(),
            password.to_string(),
            char_id,
        );
        // ⚠️ 把"选哪个角色"交给 app 的选角界面：不设这个，状态机会**自己把列表第一个
        // 选掉**（那是 e2e / 无头驱动的默认行为，见 `Entrance::set_manual_pick`）。
        entrance.set_manual_pick(true);
        Ok(Net {
            sess,
            entrance,
            world: mir2_core::world::World::default(),
            session: 0,
            status: format!("登录 {account} …"),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    /// **建号**（D-32）：与口令登录同一条连接流程，只是状态机的第一步变成
    /// "取盐 → 发口令校验值"。建完不自动登录，所以拿到回执后这条连接就没用了。
    fn connect_for_signup(addr: &str, account: &str, password: &str) -> Result<Net, String> {
        if account.is_empty() {
            return Err("账号不能为空".into());
        }
        if password.is_empty() {
            return Err("口令不能为空".into());
        }
        println!("[net] 连接 {addr}（建号 {account}）…");
        let sess = mir2_net::Session::spawn(addr.to_string(), "mir2-app".into(), "zh-CN".into());
        let mut entrance = mir2_core::entrance::Entrance::new_for_signup(
            account.to_string(),
            password.to_string(),
        );
        // 建号不涉及选角，但保持与另两条入口一致（免得将来复用这条连接时行为不同）。
        entrance.set_manual_pick(true);
        Ok(Net {
            sess,
            entrance,
            world: mir2_core::world::World::default(),
            session: 0,
            status: format!("建号 {account} …"),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    fn connect() -> Result<Net, String> {
        let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
        let session: i32 = std::env::var("MIR2_SESSION")
            .map_err(|_| {
                "缺 MIR2_SESSION（这条是\"认领既有会话\"的入口；用登录界面输入账号口令则不需要它）"
                    .to_string()
            })?
            .parse()
            .map_err(|_| "MIR2_SESSION 必须是十进制整数".to_string())?;
        let char_id: Option<u64> = match std::env::var("MIR2_CHAR") {
            Ok(s) => Some(s.parse().map_err(|_| "MIR2_CHAR 必须是整数".to_string())?),
            Err(_) => None,
        };

        println!("[net] 连接 {addr}（会话 {session}）…");
        let sess = mir2_net::Session::spawn(addr, "mir2-app".into(), "zh-CN".into());
        Ok(Net {
            sess,
            entrance: {
                let mut e = mir2_core::entrance::Entrance::new(session, char_id);
                // 与口令那条路一致：由选角界面来选（见 `connect_with_password` 的说明）
                e.set_manual_pick(true);
                e
            },
            world: mir2_core::world::World::default(),
            session,
            status: "连接中…".into(),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    /// 把"网络线程收到的东西"推进两个状态机（握手 + 世界）。**每帧调一次**。
    ///
    /// 这是 plan §4.1 的"收包线程 → channel → 主循环按帧消费"：
    /// 主循环永远不会被网络阻塞。
    fn pump(&mut self) {
        while let Ok(ev) = self.sess.evs.try_recv() {
            match ev {
                mir2_net::Ev::Connected {
                    version,
                    capabilities,
                    nonce,
                } => {
                    self.chat
                        .push(format!("已连接（协议 {version}）"), C_CHAT_SYS);
                    // ⚠️ nonce 是**这条连接一次**的握手随机值，登录时要把口令证明绑在它上面
                    //（D-24①）⇒ 必须立刻交给握手状态机，晚了就发不出证明。
                    self.entrance.on_nonce(&nonce);
                    self.status =
                        format!("已连接（协议 {version}，能力 {}）", capabilities.join(","));
                    println!("[net] {}", self.status);
                }
                mir2_net::Ev::Closed(why) => {
                    self.chat.push(format!("断开：{why}"), C_CHAT_BAD);
                    self.status = format!("断开：{why}");
                    self.fail = Some(why);
                    println!("[net] {}", self.status);
                }
                mir2_net::Ev::Envelope(env) => {
                    // 两条线各吃同一条信封：握手状态机管那几步，世界状态机管实体。
                    // ⚠️ 待发命令**不在这里**拉 —— 见 `pump` 末尾的 `flush_entrance`
                    //（拉在信封里会漏掉"非信封推动的转折"，那是一个实测过的真 bug）。
                    if let Some(b) = self.entrance.on(&env) {
                        self.send(&b);
                    }
                    if self.world.apply(&env) == mir2_core::world::Change::World {
                        self.changes += 1;
                    }
                    if self.entrance.in_world() && !self.entered_once {
                        self.entered_once = true;
                        self.chat.push(
                            format!(
                                "进入 {} ({},{})",
                                self.world.map_name, self.world.self_pos.0, self.world.self_pos.1
                            ),
                            C_CHAT_SYS,
                        );
                        println!("[net] 进世界：连接到现在 {:.2?}", self.started.elapsed());
                        // 进图后把状态行换成"世界摘要"（比"已连接"有用得多）。
                        self.status = format!(
                            "{} @{} ({},{})",
                            self.world.map_name,
                            self.world.self_id,
                            self.world.self_pos.0,
                            self.world.self_pos.1
                        );
                    }
                }
            }
        }
        // 状态机的待发命令：**每帧**排空，与有没有入站包无关。
        //
        // ⚠️ 这里曾经是错的：`next_cmd()` 被塞在上面那个 `Ev::Envelope` arm 里拉。
        // 于是"收到握手 nonce（`Ev::Connected`）⇒ 要发 `LoginSaltRequest`"这一步
        // 得**等下一个入站包**才出去 —— 而写线程的心跳是 `PING_EVERY = 20s`，
        // 服务端回 Pong 才构成那个包：表现是**输完账号要等 20 秒才开始开门**
        // （服务端日志实测：`握手完成` 与 `登录成功` 之间正好 21 秒）。
        //
        // e2e 抓不到这个：`worldcmd.rs:150` 是**开局就先拉一次**（不依赖入站包），
        // 天然不会漏 —— 这也正是它 1 秒、而 app 21 秒的原因。
        //（先收进一个小 Vec 再发：至多一两条，免得闭包借 `self` 与 `&mut self.entrance` 打架。）
        let mut pending: Vec<mir2_protocol::envelope::Body> = Vec::new();
        flush_entrance(&mut self.entrance, &mut |b| pending.push(b.clone()));
        for b in &pending {
            self.send(b);
        }

        // 伤害飘字：世界只记账，这里取走并计时。
        for d in self.world.take_damage() {
            let (x, y) = self.pos_of(d.target_id);
            self.floaters
                .push((format!("{}", d.value), x, y, Instant::now()));
            // 挨打的是自己 ⇒ 惨叫（按性别，`Actor.pas:2243-2247`）
            if d.target_id == self.world.self_id {
                self.pending_sfx
                    .push(mir2_core::sound::scream(self.self_sex()));
            }
        }
        // 自己死了 ⇒ 死亡声 + game over 音乐（`Actor.pas:2368-2376`）。
        // **只放一次**：`self_dead` 会一直为真（尸体还在），每帧放就成了噪音。
        if self.world.self_dead && !self.died_once {
            self.died_once = true;
            self.pending_sfx
                .push(mir2_core::sound::die(self.self_sex()));
            self.gameover = true;
        }
        self.floaters
            .retain(|f| f.3.elapsed() < Duration::from_millis(900));

        self.sync_anims();

        if let Some(why) = self.entrance.failed() {
            if !self.status.starts_with("失败") {
                self.status = format!("失败：{why}");
                println!("[net] {}", self.status);
            }
        }
    }

    /// 把握手状态机吐出来的信封翻译成会话命令发出去。
    fn send(&self, body: &mir2_protocol::envelope::Body) {
        match to_cmd(body, self.session) {
            Some(c) => {
                let _ = self.sess.cmds.send(c);
            }
            // ⚠️ **绝不静默吞**：这条路上漏一项就是"点了按钮没反应、服务端连请求都没收到"
            //（`LoginSaltRequest` 与 `CreateAccount` 都这么丢过，见 `to_cmd` 的说明）。
            None => eprintln!("[net] ⚠️ 这条命令没有翻译，已丢弃：{body:?}"),
        }
    }

    /// 取走"这一帧该响的音效"（世界只记账，设备在主循环里）。
    fn take_sfx(&mut self) -> Vec<u16> {
        std::mem::take(&mut self.pending_sfx)
    }

    /// 是不是**刚**死了（取走后清空）：主循环据此切 game over 音乐。
    fn take_gameover(&mut self) -> bool {
        std::mem::take(&mut self.gameover)
    }

    /// 取走"这次失败的原因"，**只给一次**（连接层断开 / 握手失败）。
    ///
    /// ⚠️ 这两样都是**粘性**的：`entrance.stage == Failed(..)` 会一直挂着、`fail` 也一直不空
    /// ⇒ 界面必须**取走**，不能每帧读 —— 否则弹窗刚点掉，下一帧又弹回来
    ///（用户 2026-10-08 报的"登录失败弹窗关不掉"）。
    fn take_fail(&mut self) -> Option<String> {
        if let Some(why) = self.fail.take() {
            return Some(connect_hint(&why));
        }
        self.entrance.take_failed()
    }

    /// 自己的性别（`0` 男 `1` 女）——协议里 `dress = 形状*2 + 性别`
    /// （`core/src/actor.rs:31`）；拿不到特征时按男（原版 `m_btSex = 0` 是男）。
    fn self_sex(&self) -> u8 {
        self.world
            .self_feature
            .as_ref()
            .map_or(0, |f| (f.dress & 1) as u8)
    }

    /// 自己这一帧的**走路动画帧号**（原版脚步按帧 1 / 帧 4 播，`Actor.pas:2659-2660`）。
    ///
    /// `None` = 没在走（站着 / 攻击 / 受击…）⇒ 调用方清掉"上一帧"的记录，
    /// 免得停下再走时被当成"帧号没变"。
    fn self_walk_frame(&self, now: Instant) -> Option<(u16, bool)> {
        let a = self.anims.get(&self.world.self_id)?;
        if !a.moving(now) {
            return None;
        }
        let run = self.world.self_run;
        let pose = mir2_core::actor::human_pose(a.action, true, run);
        if !matches!(
            pose.act,
            mir2_core::actor::HAct::Walk | mir2_core::actor::HAct::Run
        ) {
            return None;
        }
        // 跑在原版是**另一段动作、脚步基号 +2**（`Actor.pas:2237`）⇒ 两个都返回。
        // ⚠️ 帧号取**移动相位**（与画精灵同一份）：拿"这一格走了多久"会让脚步声
        // 与动画错开（动画是连续推的，见 `ActorAnim::walk_since`）。
        Some((pose.act.act().frame_at(a.walk_ms(now)), run))
    }

    /// 发一次移动输入（走）。方向用**线上编号**（`core::world` 里也不做 ±1 转换）。
    fn walk(&self, dir: mir2_protocol::Direction) {
        let _ = self.sess.cmds.send(mir2_net::Cmd::Move(dir as i32));
    }

    /// **跑**一步（原版 `CM_RUN`；服务端一步 2 格）。
    fn run(&self, dir: mir2_protocol::Direction) {
        let _ = self.sess.cmds.send(mir2_net::Cmd::Run(dir as i32));
    }

    /// 把"这一帧看到的"折进各实体的动画状态：移动了就给补间的起止，动作变了就重置计时。
    ///
    /// ⚠️ 只在**变化时**刷新 `changed_at`：`EntityMove`/`EntityAction` 不是每帧都来，
    /// 每帧重置的话走路会永远停在第一帧、动作永远播不完。
    fn sync_anims(&mut self) {
        let now = Instant::now();
        // `run` 也要带上：它决定走/跑播哪段图（见 `human_pose` 的 run 分支）
        let mut live: Vec<SeenEntity> = self
            .world
            .entities
            .values()
            .map(|e| (e.id, (e.x, e.y), e.action, e.run))
            .collect();
        if self.world.in_world() {
            live.push((
                self.world.self_id,
                self.world.self_pos,
                self.world.self_action,
                self.world.self_run,
            ));
        }
        let ids: std::collections::HashSet<u64> = live.iter().map(|(id, ..)| *id).collect();
        for (id, cell, action, run) in live {
            let a = self.anims.entry(id).or_insert(ActorAnim {
                cell,
                from: None,
                action,
                changed_at: now,
                action_at: now,
                // 刚出现/刚进视野：先按"走一格"算，下一步会据实重算
                move_ms: WALK_STEP_MS,
                walk_since: now,
            });
            if a.cell != cell {
                // ⚠️ 顺序要紧：`was_moving` 必须在改 `cell`/`from` **之前**问 ——
                // 它决定走路动画"接着推"还是"从头开始"（见 `next_walk_since`）。
                let was_moving = a.moving(now);
                let from = a.cell;
                // 服务端用 `from == to` 表达**原地转身**（没有独立的转身消息）——
                // 那不是移动：只更新朝向，别动补间/相位。
                if from != cell {
                    a.from = Some(from);
                    a.changed_at = now;
                    // 自己按**本客户端的步频**补间（否则每格末尾空 50ms：动画闪回站立 +
                    // 镜头停一下），别人按服务端的节流 —— 见 `self_move_ms`。
                    a.move_ms = if id == self.world.self_id {
                        self_move_ms(cell.0 - from.0, cell.1 - from.1, run)
                    } else {
                        move_ms(cell.0 - from.0, cell.1 - from.1, run)
                    };
                    a.walk_since = next_walk_since(was_moving, a.walk_since, now);
                }
                a.cell = cell;
            }
            if a.action != action {
                a.action = action;
                // ⚠️ 只动**动作钟**（见 `action_at` 的说明）：动 `changed_at` 会让补间从头开始
                a.action_at = now;
                // 换动作（砍/受击…）就从"走路的相位"里出来了 ⇒ 相位重开
                a.walk_since = now;
            }
        }
        // 视野外的实体不再留着（否则跑一圈地图会攒下几百条死账）
        self.anims.retain(|id, _| ids.contains(id));
    }

    /// 某个实体当前所在的格子（用来把飘字摆在它头上）。
    ///
    /// 找不到（已经消失）就退回自己的位置 —— 总比不画好。
    fn pos_of(&self, id: u64) -> (i32, i32) {
        if id == self.world.self_id {
            return self.world.self_pos;
        }
        self.world
            .entities
            .get(&id)
            .map(|e| (e.x, e.y))
            .unwrap_or(self.world.self_pos)
    }

    /// 打一下**紧邻**（八格）的那个实体。
    ///
    /// ⚠️ 只认相邻：服务端的 `AttackInput` 也只在相邻八格里才认（见那边的说明），
    /// 目标太远服务端会静默忽略。返回 false = 身边没有可打的目标。
    fn attack_adjacent(&self) -> bool {
        let (sx, sy) = self.world.self_pos;
        let target = self.world.entities.values().find(|e| {
            !e.dead && (e.x - sx).abs() <= 1 && (e.y - sy).abs() <= 1 && (e.x != sx || e.y != sy)
        });
        match target {
            Some(e) => {
                println!("[net] 攻击 {} (ActorId={})", e.name, e.id);
                self.attack_target(e.id)
            }
            None => false,
        }
    }

    /// 打**指定的**目标（左键点怪锁住之后每帧来一次）。
    ///
    /// 消息号固定 `ATTACK_HIT`（普通挥砍）：原版会按武器/技能挑 `CM_HEAVYHIT/CM_POWERHIT/…`
    ///（`ClMain.pas:2695-2722`），那些（重击/攻杀/刺杀）都还没接 —— 这里只发基础那一种。
    fn attack_target(&self, id: u64) -> bool {
        self.sess
            .cmds
            .send(mir2_net::Cmd::Attack {
                target_id: id,
                action: mir2_protocol::AttackAction::AttackHit as i32,
            })
            .is_ok()
    }

    /// 自己的**渲染位置**（补间后的浮点格）；没进世界时给服务端那一格。
    ///
    /// 相机、小地图、大地图都用它 —— 它们**必须**和画精灵用同一份（`ActorAnim::draw_pos`），
    /// 否则"人在这、图心在那"。
    fn self_render(&self, now: Instant) -> Option<(f32, f32)> {
        if !self.world.in_world() {
            return None;
        }
        Some(self_render_pos(
            self.anims.get(&self.world.self_id),
            self.world.self_pos,
            now,
        ))
    }
}

/// 方向键：联网且在世界里 ⇒ 走一步并返回 `true`（调用方就别动镜头了）。
///
/// 离线时返回 `false` ⇒ 保持原来的"方向键平移镜头"（开发查看器最常用的动作）。
fn walk_if_online(net: &Option<Net>, dir: mir2_protocol::Direction) -> bool {
    move_if_online(net, dir, false)
}

/// 同上，但可以**跑**（原版：左键走、右键跑/按住 Ctrl 走改为跑）。
fn move_if_online(net: &Option<Net>, dir: mir2_protocol::Direction, run: bool) -> bool {
    match net {
        Some(n) if n.world.in_world() => {
            if run {
                n.run(dir);
            } else {
                n.walk(dir);
            }
            true
        }
        _ => false,
    }
}

/// 鼠标连续走路的**步频**（毫秒）：比服务端的节流稍慢一点，免得每步都被拒。
/// 服务端 `entity.Limiter` 是 `MinWalk = 600ms` / `MinRun = 400ms`。
const WALK_MS: u64 = 650;
const RUN_MS: u64 = 450;

/// **按住鼠标时**重取目标的间隔（毫秒）—— 原版 `ClMain.pas:2678-2679`：
/// `if (ssLeft in Shift) or (ssRight in Shift)) and (GetTickCount - mousedowntime > 300)`.
const MOUSE_REPEAT_MS: u64 = 300;

/// 移动**被服务端拒了**之后，多久不许再发（毫秒）—— 原版 `ActionFailed` 的
/// `ActionFailLock`：`GetTickCount - ActionFailLockTime > 1000` 才解锁（`ClMain.pas:4005-4020`）。
///
/// 不锁的表现：朝一个撞墙的方向**每 `WALK_MS` 发一次**、每次都被拒 ⇒ 人物"卡在那儿不动"
/// 而且服务端日志刷满（用户 2026-10-08 报的"跑不到怪身边"多半就有它）。
const MOVE_FAIL_LOCK_MS: u64 = 1000;

/// 一次"鼠标点下去"算什么：`(要锁的目标, 要走的格子)`。
///
/// 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：**先清掉旧目标**，再看光标那格有什么 ——
/// 有**活怪** ⇒ 锁它（之后每帧自动靠近/出手，见 `World::combat_step`）；
/// 空地（或玩家/NPC：那要 Shift 才是 PK 那条线，还没做）⇒ 走/跑到那一格。
///
/// 抽成纯函数是为了能单测：这条规则在原版里散在几百行事件代码里，改一次踩一次。
fn mouse_intent(
    world: &mir2_core::world::World,
    cell: (i32, i32),
    run: bool,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    match world.attack_target_at(cell) {
        Some(id) => (Some(id), None),
        None => (None, Some((cell.0, cell.1, run))),
    }
}

/// **按住不放**时每 [`MOUSE_REPEAT_MS`] 重取一次目标 —— 与新鲜按下（[`mouse_intent`]）
/// 只差一条：光标底下什么都没有时，**不取消**已经锁住的那个怪物。
///
/// # 为什么要差这一条（用户 2026-10-08 报的"点怪之后站不住、掉头去走路"）
///
/// 原版重跑按下逻辑会先把 `g_TargetCret := nil`（`ClMain.pas:2813`），光标不在怪身上就不再锁
/// —— 而**镜头跟着人走**（[`follow_cam`]）⇒ 跑向怪的路上光标自己就从怪身上滑开了，
/// 于是"按住左键点怪"会半路把目标弄丢、改成走去光标那格。
/// 单击（按下就抬）不走这条路，所以原版"单击点怪 ⇒ 一路打到它死"是好的 ——
/// 我们这里把**按住**也保住：已经锁着、而且那个怪还活着（`combat_step` 还有得出）就继续打。
/// 想取消？松手再点一下地（新鲜按下还是照原版**清目标**）。
fn mouse_repeat(
    world: &mir2_core::world::World,
    cell: (i32, i32),
    run: bool,
    locked: Option<u64>,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    match world.attack_target_at(cell) {
        Some(id) => (Some(id), None),
        // 锁着的那个还在（活着且还在视野里）⇒ 继续打它
        None if locked.is_some_and(|id| world.combat_step(id).is_some()) => (locked, None),
        None => (None, Some((cell.0, cell.1, run))),
    }
}

/// 连续攻击的**最小节拍**：服务端的 `hitIntervalTime = 520ms` **再加一点余量**。
///
/// # ⚠️ 为什么不能取"攻击动画的时长"（510ms）—— 这是"砍几下就停"的病根
///
/// 服务端按 `AttackIntervalFor` 限流（`netgate.go:63/112`：`520ms − 攻速×25ms`），
/// **比它快的攻击被直接丢掉**。丢掉的那次服务端**不发 `EntityAction`** ⇒ 我们自己的
/// 挥砍动画（完全来自服务端回包，见 `world::self_action`）就断一拍 ——
/// 表现正是用户 2026-10-09 报的"砍几下停下、但怪在掉血"（掉血是过掉的那几刀给的）。
///
/// 取 510ms 时是**每一刀丢一刀**（510 < 520）。这里取基数 + 余量：
/// ⚠️ 服务端真实间隔随**攻速**下降（`− hitSpeed×25ms`），但我们手里没有攻速
///（`Ability` 没下发这个字段）⇒ 宁可慢一点：至少不乱丢、动画不断。
const HIT_BASE_MS: u64 = 520;
fn attack_gap() -> Duration {
    Duration::from_millis(HIT_BASE_MS + 40)
}

/// 一次播完的动作（挥砍/受击）**多留最后一帧**多久（毫秒）。
///
/// # 为什么需要（用户 2026-10-09 报的"砍几下停下"的第二半）
///
/// 原版**看不出缝**：它的动画时长就是服务端下发的 `ClientConfig.HitTime`（`ClMain.pas:2700`），
/// 与攻击间隔同源（`ActHit` 6×85 = 510ms vs 间隔 520ms ⇒ 缝 10ms，眼睛看不见）。
/// 我们的动作表是写死的 510ms，而节拍取的是**服务端的基数 + 余量**（`attack_gap()` = 560ms，
/// 不能更小 —— 更小会被服务端丢掉整刀）⇒ 缝 50ms ≈ 三帧"站立"，看着就是一顿。
///
/// 所以把最后一帧多留这么久：下一刀在 560ms 到，正好接上（也不再需要把节拍压到危险区）。
const ACTION_TAIL_MS: u32 = 80;

/// 这一刻能不能**出手**：①手上这一步走完了 ②距上次出手够久了。
///
/// # ① 为什么必须有（用户 2026-10-08 报的"还没靠近怪就开始攻击"）
///
/// 原版的门是 `CanNextAction` = `g_MySelf.IsIdle`（`ClMain.pas:3975-3984` +
/// `Actor.pas:1722-1736`）：`m_nCurrentAction <> 0`（**动作没完**）就 `IsIdle = FALSE`
/// ⇒ 走路的动作还没播完，**攻击发不出去**。
///
/// 我们不做移动预测（位置是服务端权威的），所以特别需要这道门：服务端确认"到位"时，
/// 画面上的补间才刚起步 —— `combat_step` 已经说"相邻、该出手"，挥砍就播在半路上了。
fn can_attack(stepping: bool, since_last_attack: Duration) -> bool {
    !stepping && since_last_attack >= attack_gap()
}

/// 挥刀声：按**武器形状**分类（`Actor.pas:2254-2261`）。
///
/// ⚠️ 原版在攻击动画的**帧 2** 播（`:2396-2401`），我们在发起这一帧就播 —— 差几十毫秒，
/// 接线简单得多（这条差异记在这儿，别当"照原版"）。
fn swing_sfx(n: &Net, sound: &audio::Audio, sounds: &Option<mir2_core::sound::SoundAssets>) {
    let shape = n.world.self_feature.as_ref().map_or(0, |f| f.weapon / 2) as u16;
    sfx(sound, sounds, mir2_core::sound::swing(shape));
}

/// 按住了 Ctrl 吗（`keymod` 那套；开发键都收在 Ctrl+ 里）。
fn ctrl(m: sdl3::keyboard::Mod) -> bool {
    m.intersects(sdl3::keyboard::Mod::LCTRLMOD) || m.intersects(sdl3::keyboard::Mod::RCTRLMOD)
}

// ---------- 小地图 / 大地图（原版 `PlayScn.pas:791` 的 `DrawMiniMap`）----------

/// 小地图在屏幕右上角的边长（原版就是 120×120，`PlayScn.pas:815-817`）。
const MINIMAP_PX: f32 = 120.0;

/// 大地图的**放大倍数**。
///
/// 取 2 的理由：缩略图（mmap）一张约 540×360（原版把整张塞进 540×360 的
/// `g_MiniMapSurface`，`ClMain.pas:1030`），1 倍时整张图还没窗口大 ⇒ 没得滚动，
/// "跟着人物滑"就看不出来。2 倍 ⇒ 1080×720 > 窗口 ⇒ 站在图中间看得见四周，
/// 跑起来图会滑（这正是用户要的手感）。
const BIGMAP_ZOOM: f32 = 2.0;

/// 自己在缩略图上的标记色（原版是把那个像素直接写 255，`PlayScn.pas:790+32`）。
const C_SELF_DOT: Color = Color::RGB(255, 255, 255);

/// 格坐标 → 缩略图像素（原版 `PlayScn.pas:808-813`：
/// `mx := (x*48) div 32`、`my := (y*32) div 32` ⇒ **X 是 1.5 倍、Y 是 1 倍**）。
///
/// 这个不对称的宽高比是**原版就有的**（地图格在缩略图上不是方的），不是笔误。
/// 收 `f32` 是为了能吃**补间中的小数格**（否则图会一格一格跳，见 `self_render_pos`）。
fn minimap_point(px: f32, py: f32) -> (f32, f32) {
    (px * 48.0 / 32.0, py)
}

/// 以小地图上的 `(cx, cy)` 为中心取 `size` 见方的**源裁剪框**，夹在图内。
///
/// 返回 `(x, y, w, h)`：贴边时四个数都会被夹（不许露出图外 —— 原版用的是
/// `_MAX(0, mx-60)` + `_MIN(图宽, left+120)`，同一件事）。
fn minimap_crop(cx: f32, cy: f32, img: (u32, u32), size: f32) -> (f32, f32, f32, f32) {
    let (iw, ih) = (img.0 as f32, img.1 as f32);
    let w = size.clamp(1.0, iw.max(1.0));
    let h = size.clamp(1.0, ih.max(1.0));
    let x = (cx - w / 2.0).clamp(0.0, (iw - w).max(0.0));
    let y = (cy - h / 2.0).clamp(0.0, (ih - h).max(0.0));
    (x, y, w, h)
}

/// 大地图的贴图矩形：**人物永远落在窗口正中**，图比窗口大 ⇒ 跑动时图会滑。
///
/// 与 `minimap_crop` 的取舍相反：那儿要把图夹在图内（小地图不许露白），
/// 这儿**故意不夹** —— 用户要的是"人物总是处于地图中间"（靠近地图边缘时，
/// 边缘之外就是空的，也不许把人物从正中挪走）。
fn bigmap_dst(
    px: f32,
    py: f32,
    img: (u32, u32),
    win: (u32, u32),
    zoom: f32,
) -> (f32, f32, f32, f32) {
    (
        win.0 as f32 / 2.0 - px * zoom,
        win.1 as f32 / 2.0 - py * zoom,
        img.0 as f32 * zoom,
        img.1 as f32 * zoom,
    )
}

/// 自己的**渲染位置**（浮点格）：有动画状态就走补间，否则就是服务端给的那一格。
///
/// ⚠️ 小地图/大地图取它、**不能取 `world.self_pos`**：后者只在**服务端确认到位**时才变
///（走一格要等一个来回）⇒ 图上是"到位才跳一格"（用户 2026-10-08 报的）。
/// 这里与画精灵用的是同一份补间（`ActorAnim::draw_pos`）⇒ 图上的移动和人的动作**同步**。
///
/// 抽成自由函数就是为了能单测（`Net` 不好在单测里造）。
fn self_render_pos(anim: Option<&ActorAnim>, to: (i32, i32), now: Instant) -> (f32, f32) {
    anim.map_or((to.0 as f32, to.1 as f32), |a| a.draw_pos(to, now))
}

/// 相机该在哪个**浮点格**：把自己的**渲染位置**摆到视口正中。
///
/// # 为什么必须是"渲染位置"而不是"服务端那一格"（用户 2026-10-08 报的走路/跑动手感）
///
/// 原版就是这么干的（`PlayScn.pas:1084-1097`）：
///
/// ```text
/// with Map.m_ClientRect do begin
///    Left := g_MySelf.m_nRx - 9;   // m_nRx = **渲染**坐标（补间后，不是 m_nCurrX）
///    Top  := g_MySelf.m_nRy - 9;
///    Right := g_MySelf.m_nRx + 9;  Bottom := g_MySelf.m_nRy + 8;
/// end;
/// Map.UpdateMapPos (g_MySelf.m_nRx, g_MySelf.m_nRy);
/// ```
///
/// ⇒ 表现是"**人物钉在屏幕中间不动，地图往前卷**"。相机若取整格，就变成
/// "人在视口里一格格往前蹭、蹭满一格镜头再猛跳一下" —— 完全不是那个手感。
///
/// ⚠️ x 用**浮点**除（`WIN_W/UNIT_X/2` = 8.33 格 ⇒ 人正落在 400px 中央）；用整数除
/// （8 格 = 384px）会让人偏左 16px。
fn follow_cam(render: (f32, f32)) -> (f32, f32) {
    (
        render.0 - (WIN_W as f32 / UNIT_X as f32) / 2.0,
        render.1 - (VIEW_H / UNIT_Y as f32) / 2.0,
    )
}

/// 画小地图（Tab）/ 大地图（M）。
///
/// 数据是图库 `mmap` 里"整张地图的预渲染缩略图"，下标 = **图号 − 1**
///（原版 `ClMain.pas:6045-6051` 的 `g_nMiniMapIndex := mapindex - 1`）；
/// 图号由服务端随 `EnterWorld`/`ChangeMap` 下发（新协议不再单开一问一答）。
///
/// - 小地图：以自己为中心裁 `MINIMAP_PX` 见方，贴**屏幕右上角**（原版 `(W-120, 0)`），
///   自己画个小方点；
/// - 大地图：同一张图**等比缩放**到窗口内居中，同一个换算再乘缩放（原版的大地图窗口）。
#[allow(clippy::too_many_arguments)]
fn draw_minimaps<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    asset_dir: &Option<std::path::PathBuf>,
    // `(小地图图号, **补间后**的自己位置)` —— 位置见 `self_render_pos`
    world: Option<(u32, (f32, f32))>,
    minimap_on: bool,
    bigmap_on: bool,
    win: (u32, u32),
) -> Result<(), sdl3::Error> {
    if !minimap_on && !bigmap_on {
        return Ok(());
    }
    let (Some(dir), Some((idx, pos))) = (asset_dir.as_ref(), world) else {
        return Ok(()); // 没素材 / 还没进世界
    };
    if idx == 0 {
        return Ok(()); // 该图没有小地图（服务端查表查不到）
    }
    let lib_idx = idx - 1; // 图号 − 1 = 图库下标
    let (mx, my) = minimap_point(pos.0, pos.1);
    let Some(img) = ui.size(dir, "mmap", lib_idx) else {
        return Ok(()); // 图库里没有这一张
    };

    if minimap_on {
        let (sx, sy, sw, sh) = minimap_crop(mx, my, img, MINIMAP_PX);
        let (dx, dy) = (win.0 as f32 - MINIMAP_PX, 0.0);
        let _ = ui.draw_src(
            canvas,
            tc,
            dir,
            "mmap",
            lib_idx,
            FRect::new(sx, sy, sw, sh),
            FRect::new(dx, dy, sw, sh),
            255,
        );
        // 自己那个点（原版：`surface.Pixels[mx, my] := 255`）
        let cx = dx + (mx - sx);
        let cy = dy + (my - sy);
        fill(canvas, cx - 1.0, cy - 1.0, 3.0, 3.0, C_SELF_DOT)?;
    }

    if bigmap_on {
        let (iw, ih) = (img.0 as f32, img.1 as f32);
        if iw > 0.0 && ih > 0.0 {
            let (dx, dy, dw, dh) = bigmap_dst(mx, my, img, win, BIGMAP_ZOOM);
            // 半透明：大地图铺满整屏，全不透明就看不见自己脚下的路了
            let _ = ui.draw_src(
                canvas,
                tc,
                dir,
                "mmap",
                lib_idx,
                FRect::new(0.0, 0.0, iw, ih),
                FRect::new(dx, dy, dw, dh),
                140,
            );
            // 人物在正中（`bigmap_dst` 保证这一点），标记就画在窗口中心
            let (cx, cy) = (win.0 as f32 / 2.0, win.1 as f32 / 2.0);
            fill(canvas, cx - 2.0, cy - 2.0, 5.0, 5.0, C_SELF_DOT)?;
        }
    }
    Ok(())
}

/// **屏幕坐标 → 地图格**（鼠标点哪走到哪要用它；与 [`cell_to_screen`] 互为逆）。
/// ⚠️ 必须是 `(cam + px/UNIT).floor()`，**不是** `cam.floor() + (px/UNIT).floor()`：
/// 相机是浮点格（走路时一直在动），拆开取整会在小数处差**一格** ——
/// "点哪走哪"就会偏（`屏幕与格子互为逆` 这条单测就是抓它的）。
fn screen_to_cell(cam: (f32, f32), px: f32, py: f32) -> (i32, i32) {
    (
        (cam.0 + px / UNIT_X as f32).floor() as i32,
        (cam.1 + (py - BAR_TOP) / UNIT_Y as f32).floor() as i32,
    )
}

/// 从 `from` 格朝 `to` 格的方向（协议值 = 原版 + 1，顺时针：1 上、2 右上 … 8 左上）。
///
/// 照原版 `GetNextDirection`（`ClFunc.pas:398-422`）：先把 Δ 取**三态符号**，再映到 8 方向；
/// **同一格**给 `None`（没有方向可言 —— 调用方据此判"到了"）。
fn dir_to(from: (i32, i32), to: (i32, i32)) -> Option<mir2_protocol::Direction> {
    use mir2_protocol::Direction as D;
    let (dx, dy) = ((to.0 - from.0).signum(), (to.1 - from.1).signum());
    Some(match (dx, dy) {
        (0, -1) => D::DirUp,
        (1, -1) => D::DirUpRight,
        (1, 0) => D::DirRight,
        (1, 1) => D::DirDownRight,
        (0, 1) => D::DirDown,
        (-1, 1) => D::DirDownLeft,
        (-1, 0) => D::DirLeft,
        (-1, -1) => D::DirUpLeft,
        _ => return None,
    })
}

/// 鼠标连续走路：**这一步该走还是该跑**（`None` = 已经站在目标格上、收工）。
///
/// 照原版 `ClMain.pas:1863-1978`（`ProcessActionMessages`）：
///
/// ```text
/// if (g_nTargetX <> 自己.x) or (g_nTargetY <> 自己.y) then   // 还没到
///    caRun: if (GetDistance(自己, 目标) >= 2) and ... then 发 CM_RUN
///           else 只转身 + 清目标, goto LB_WALK（**走一步**）
/// ```
///
/// 也就是说**跑只在"距离 ≥ 2"时才发**（`GetDistance` = 切比雪夫距离，
/// `ClFunc.pas:352-355`：`_MAX(abs(dx), abs(dy))`）。
/// ⚠️ 少了这条，目标格是**奇数距离**时跑步（一次跨 2 格）会**跨过去再跨回来**，
/// 表现就是"奔跑位置左右乱换"（用户 2026-10-08 报的）；距离 < 2 时改成走 1 格正好落到。
fn next_move_step(
    from: (i32, i32),
    to: (i32, i32),
    want_run: bool,
) -> Option<(mir2_protocol::Direction, bool)> {
    let dir = dir_to(from, to)?; // 同一格 ⇒ None ⇒ 调用方收工
    let far = (to.0 - from.0).abs().max((to.1 - from.1).abs()) >= 2; // GetDistance（切比雪夫）
    Some((dir, want_run && far))
}

/// 协议朝向（1..8）→ 屏幕增量。**1 = 上**（新枚举 = 原版 + 1，见 common.proto）。
///
/// ⚠️ 这张表与服务端 `entity.DirDelta` 是同一份顺序（原版 0..7 各 +1）——
/// 两处一旦不一致，人物会朝反方向走，而且不会报错。
/// 格子坐标 → 视口坐标（地图绘制用的同一套换算：`UNIT_X/UNIT_Y` + 顶部信息条）。
fn cell_to_screen(cam: (f32, f32), cx: i32, cy: i32) -> (f32, f32) {
    cell_to_screen_f(cam, cx as f32, cy as f32)
}

/// 同上，但两边都允许**小数**：相机是浮点格（见 [`follow_cam`]），格子也可能是
/// 补间到一半的（见 [`ActorAnim::draw_pos`]）。
fn cell_to_screen_f(cam: (f32, f32), cx: f32, cy: f32) -> (f32, f32) {
    (
        (cx - cam.0) * UNIT_X as f32,
        BAR_TOP + (cy - cam.1) * UNIT_Y as f32,
    )
}

/// 相机拆成"**整格 + 亚格像素**"。
///
/// 地图那条路（`Map::visible_tiles`）只认**整格**相机，它算出来的图块落点是"格 × UNIT"
/// 的整数像素 ⇒ 亚格那半格由绘制时减去 [`CamParts::sub`] 补上（见 `draw_tile`）。
/// 这样地图就能**逐帧平滑卷动**，而不是"攒够一格再猛跳一下"。
///
/// 别改成"把小数扔掉"：那就退回"人在视口里一格格蹭"的老样子了（用户 2026-10-08 报的）。
fn cam_parts(cam: (f32, f32)) -> CamParts {
    let cell = (cam.0.floor() as i32, cam.1.floor() as i32);
    CamParts {
        cell,
        sub: (
            (cam.0 - cell.0 as f32) * UNIT_X as f32,
            (cam.1 - cell.1 as f32) * UNIT_Y as f32,
        ),
    }
}

/// [`cam_parts`] 的结果：整格相机 + 要减掉的亚格像素。
struct CamParts {
    /// 整格相机（`visible_tiles` 用）。
    cell: (i32, i32),
    /// 亚格像素偏移：所有"以整格相机算出来"的落点都要减掉它。
    sub: (f32, f32),
}

fn dir_delta(dir: i32) -> (f32, f32) {
    match dir {
        1 => (0.0, -1.0),  // 上
        2 => (1.0, -1.0),  // 右上
        3 => (1.0, 0.0),   // 右
        4 => (1.0, 1.0),   // 右下
        5 => (0.0, 1.0),   // 下
        6 => (-1.0, 1.0),  // 左下
        7 => (-1.0, 0.0),  // 左
        8 => (-1.0, -1.0), // 左上
        _ => (0.0, 0.0),   // 未指定
    }
}

// ---------- actor 精灵 ----------
//
// 图号公式在 `mir2_core::actor`（原版 Delphi 的逐条翻译）。这里只做三件事：
// 取纹理、**按锚点定位**、按时间推进帧。
//
// ⚠️ 落点公式（原版 `PlayScn.pas:1236` 把**格子左上角**交给 actor，再由 `Actor.pas`
// 里的 `dx + m_nPx, dy + m_nPy` 落笔）：
//
//     精灵左上角 = 格子左上角 + 图自带的锚点
//
// 锚点**常是负的**（`Hum#0` 是 (8,-48)）：71 像素高的人站在 32 像素的格子上，
// 脑袋当然得画到格子上面去。别改成"底边对齐格底" —— 那样每一帧都对不上。

/// 精灵纹理缓存键：容器名 + 图号（容器名都是 `&'static str`，见 `core::actor`）。
type SpriteKey = (&'static str, u32);

struct SpriteTex<'a> {
    tex: Texture<'a>,
    /// 图自带锚点（原版 `m_nPx/m_nPy`）。
    anchor_x: i16,
    anchor_y: i16,
}

/// 玩家/怪物精灵的纹理缓存（与图块缓存分开：键是容器名、混合一律普通 alpha）。
struct SpriteCache<'a> {
    libs: HashMap<&'static str, Option<Wzl>>,
    texs: HashMap<SpriteKey, SpriteTex<'a>>,
}

impl<'a> SpriteCache<'a> {
    fn new() -> Self {
        Self {
            libs: HashMap::new(),
            texs: HashMap::new(),
        }
    }

    /// 保证缓存里有该精灵；容器缺失 / 图号取不出图时返回 `None`（调用方降级成标记）。
    fn ensure<T>(
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
        self.texs.insert(
            (lib, idx),
            SpriteTex {
                tex: t,
                anchor_x: s.anchor_x,
                anchor_y: s.anchor_y,
            },
        );
        Some(())
    }
}

/// HUD 聊天区里的那几行（原版 `ChatStrs`：每行自带前景/背景色，`FState.pas:3868-3886`）。
///
/// ⚠️ 与终端里的 `println!` **不重复也不替代**：终端是给开发看的流水账，
/// 这里是给玩家看的"最近发生了什么"（有上限、旧的滚掉）。
#[derive(Default)]
struct Chat {
    lines: std::collections::VecDeque<(String, Color)>,
}

impl Chat {
    /// 推一行。超过 [`CHAT_CAP`] 就丢最旧的。
    fn push(&mut self, s: impl Into<String>, col: Color) {
        self.lines.push_back((s.into(), col));
        while self.lines.len() > CHAT_CAP {
            self.lines.pop_front();
        }
    }
}

/// 聊天区最多留几行（画出来的只有最后 [`CHAT_VIEW_LINES`] 行，多留些好回看）。
const CHAT_CAP: usize = 64;
/// 聊天区一屏几行（原版那块框高 105px ÷ 行距 13 ≈ 8）。
const CHAT_VIEW_LINES: usize = 8;

/// 一帧"看到的"实体状态：`(id, 格子, 动作, 这一步是不是跑)` —— 喂给 `ActorAnim` 的那四个数。
///
/// 抽个别名是因为它要四处传（收集 / 遍历 / 调试），写成裸元组 clippy 会报 `type_complexity`，
/// 更要紧的是读代码时看不出第四个 `bool` 是"跑"。
type SeenEntity = (u64, (i32, i32), Option<u32>, bool);

/// 一个实体的动画状态（**渲染层**持有 —— 世界模型是不带时钟的纯状态）。
struct ActorAnim {
    /// 上次看到的格子（用来判"又动了"）。
    cell: (i32, i32),
    /// 上一次移动的来处（补间用）。
    from: Option<(i32, i32)>,
    /// 最近一次动作（协议动作 id，见 `protocol.md` §9.5）。
    action: Option<u32>,
    /// **移动**的起始时刻（补间与 `moving()` 用它）。
    changed_at: Instant,
    /// **动作**的起始时刻（挥砍/受击的播放进度用它）。
    ///
    /// # ⚠️ 为什么必须与 `changed_at` 分开（用户 2026-10-09 报的"杀死怪之后再跑还有砍的动作"）
    ///
    /// 原来只有一个钟：动作与移动共用 `changed_at` ⇒ **每走一格都把动作进度清零**
    /// ⇒ 那个"早就该过期的挥砍"（`world.self_action` 收到过就一直留着）在每次移动时
    /// **重播**一遍：表现就是"杀了怪，一跑起来又在砍"（别的实体同理：怪走路时也会闪攻击动作）。
    /// 分开之后移动只动移动钟；动作到点就过期，**不会被下一次移动救活**。
    action_at: Instant,
    /// 这次移动的补间时长（ms）—— 由 `from → cell` 的格数与走/跑算出（见 `move_ms`）。
    ///
    /// **一步一算**（走一格 600 ms、跑一步 400 ms），所以不能再有全局常量。
    move_ms: u32,
    /// 走路/跑步动画的**连续相位**起点（见 `next_walk_since`）。
    ///
    /// ⚠️ 与 `changed_at` 分工明确：那个是"这一格从哪来"（每步都要重置），
    /// 这个是"动画播到第几帧了"（只在停下/换动作时重置）。混用一个时钟 =
    /// 每走一格动画从头开始 = `ActWalk` 的 6 帧只看得见前 4 帧（用户报的"一瘸一拐"）。
    walk_since: Instant,
}

impl ActorAnim {
    fn elapsed_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.changed_at).as_millis() as u32
    }

    /// 手上的动作已经播了多久（`body_sprite`/`weapon_sprite` 取帧用它）。
    fn action_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.action_at).as_millis() as u32
    }

    /// 是不是正走在半路上（决定播走路的动画）。
    fn moving(&self, now: Instant) -> bool {
        self.from.is_some() && self.elapsed_ms(now) < self.move_ms
    }

    /// 走路/跑步动画已经播了多久（**连续相位**，不随每格重置）。
    fn walk_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.walk_since).as_millis() as u32
    }

    /// 补间后的绘制坐标（格子坐标，浮点）。
    fn draw_pos(&self, to: (i32, i32), now: Instant) -> (f32, f32) {
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
fn human_sample(
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
fn monster_sample(
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
fn body_sprite(
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
fn actor_rect<'a, T>(
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
    for layer in [body_sprite(e, anim, now), weapon_sprite(e, anim, now)] {
        let Some((lib, idx)) = layer else { continue };
        if sprites.ensure(tc, dir_assets, lib, idx).is_none() {
            continue;
        }
        let Some(t) = sprites.texs.get(&(lib, idx)) else {
            continue;
        };
        let q = t.tex.query();
        let r = FRect::new(
            px + t.anchor_x as f32,
            py + t.anchor_y as f32,
            q.width as f32,
            q.height as f32,
        );
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
fn weapon_sprite(
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

/// 画一个实体：**先精灵、取不到再退标记**，最后统一画名字与血条。
#[allow(clippy::too_many_arguments)]
fn draw_actor<'a, T>(
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
    // 本体 → 武器（原版层序：武器压在身体上面）
    let layers = [body, weapon_sprite(e, anim, now)];
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

/// 悬停高亮那一遍的**颜色 + 透明度**。
///
/// # 为什么不照抄原版"原样再画一遍半透明"
///
/// 原版（`PlayScn.pas:1369-1376`）与 Crystal（`GameScene.cs:10973`）都是**把同一张图
/// 再画一遍**（原版 `DrawBlend` 走 `Color256Mix` 表、Crystal `SetBlend(true, 0.3F)`）。
/// 但那个做法对我们**不产生任何视觉变化**：`dst = src*a + dst*(1-a)`，而 dst 本来就是
/// 第一遍画上去的 src ⇒ `dst` 不变（只有贴图的半透明边缘会变实一点）。
///
/// 所以这里用**暖色 + 半透明**：机制仍是"再画一遍"（能与别处对齐），但反馈看得见。
/// ⚠️ 颜色是**粘在贴图上**的（贴图是缓存共用的）⇒ 每次 `copy` 前都要显式设一遍。
const HOVER_TINT: (u8, u8, u8, u8) = (255, 232, 150, 140);

/// 画一层 actor 精灵（本体 / 武器）。`tint = None` ⇒ 原色不透明。
///
/// ⚠️ 两种情况都**显式设一遍**颜色与透明度：`set_color_mod`/`set_alpha_mod` 是粘在
/// **贴图**上的，而贴图是缓存共用的 —— 高亮完不复位，下一帧所有精灵都会带暖色。
/// （`core::select_ui` 那边踩过同样的坑，见 `UiCache::draw_tint` 的说明。）
fn draw_actor_layer(
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
fn draw_entity_marker<'a, T>(
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
fn draw_name_bar<'a, T>(
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

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let sdl = sdl3::init()?;
    let video = sdl.video()?;

    let (win_w, win_h) = window_size();
    let mut window = video
        .window("MIR2 1.76 CLIENT - DEV VIEWER", win_w, win_h)
        .position_centered()
        // 可拉大拉小：逻辑呈现会把 800×600 的界面**等比**铺到新尺寸（见 [`WIN_W`]）
        .resizable()
        .build()
        .map_err(|e| format!("创建窗口失败: {e}"))?;

    video.text_input().start(&window);
    // 屏幕放不下"1024×768 的**客户区**"时（1024×768 的屏加上标题栏/菜单栏就放不下），
    // 把窗口夹进**可用区域**：宁可窗口小一点、画面按 1:1 画、多出去的边裁掉，
    // 也不把整屏缩小或拉变形（用户 2026-10-09 第 4 条）。
    if let Some(u) = usable_bounds(&video) {
        let fit = fit_window((win_w, win_h), u);
        if fit != (win_w, win_h) {
            println!(
                "[mir2-app] 屏幕可用区域 {}×{} 装不下 {}×{} ⇒ 窗口取 {}×{}（画面 1:1、多出的边裁掉）",
                u.0, u.1, win_w, win_h, fit.0, fit.1
            );
            window
                .set_size(fit.0, fit.1)
                .map_err(|e| format!("调整窗口大小失败: {e}"))?;
        }
    }
    let mut canvas = window.into_canvas();
    apply_presentation(&mut canvas)?;
    let tex_creator = canvas.texture_creator();

    // ---------- 音频 ----------
    // 规格（编号表 / 地形 → 脚步 / `sound.lst`）在 `mir2_core::sound`，这里只开设备。
    // ⚠️ `_stream` 就是声卡：**必须活在这个作用域里** —— drop 掉即静音。
    let sdl_audio = sdl.audio()?;
    // `AudioSubsystem` 是 sdl3 的私有类型 ⇒ 开流这一步只能在这里写（见 `audio::spec`）
    let (sound, _stream) = audio::Audio::open_with(true, true, |m| {
        sdl_audio.open_playback_stream(&audio::spec(), m)
    })?;

    // ---------- 资产 ----------
    let asset_dir = mir2_core::paths::asset_dir();
    let container_path = mir2_core::paths::map_container();
    let archive = match &container_path {
        Some(p) => match Archive::open(p) {
            Ok(a) => {
                println!("[mir2-app] 地图容器 = {}（{} 张）", p.display(), a.len());
                Some(a)
            }
            Err(e) => {
                println!("[mir2-app] 地图容器打不开：{e}");
                None
            }
        },
        None => {
            println!("[mir2-app] 未找到地图容器（先跑 tools/m2pk/build.sh）");
            None
        }
    };
    match &asset_dir {
        Some(d) => println!("[mir2-app] 资产目录 = {}", d.display()),
        None => println!("[mir2-app] 未找到资产目录：设 MIR2_ASSET_DIR=<mir2c/data>"),
    }
    // 音频资产：**一个容器**（`assets/audio/sounds.m2pk`，`tools/wavpack` 产出），
    // 取不到就退化到目录（原始素材 / 旧产物）。都没有 ⇒ 静音降级，界面照旧能用。
    let sounds: Option<mir2_core::sound::SoundAssets> = match mir2_core::sound::SoundAssets::open()
    {
        Some(a) => {
            println!("[mir2-app] 音频资产 = {}", a.describe());
            Some(a)
        }
        None => {
            println!("[mir2-app] 未找到音频资产：跑 tools/wavpack/build.sh（或设 MIR2_AUDIO_DIR）");
            None
        }
    };
    println!("[mir2-app] 音频驱动 = {}", sdl_audio.current_audio_driver());
    println!(
        "[mir2-app] 操作：F1 登录界面 / F2 地图视图 / F3 素材浏览器 / M 音乐 / N 音效 / ESC 退出"
    );

    let mut events: EventPump = sdl.event_pump()?;

    // 登录界面的状态（照原版的那套版式与交互，见 `login.rs`）
    let mut login = login::Login::new();
    // 界面素材缓存（`Prguse` / `ChrSel`）—— 与地图图块、actor 精灵的缓存分开
    let mut ui = ui::UiCache::new();
    // 选角场景（登录成功、状态机停在"等你选"时才建）与真字体绘制器
    let mut select_scene: Option<select::Select> = None;
    // 登录/选角专用：同一份字体、按**缩放后**的字号重新光栅化（`UI_PX * 1.28 ≈ 17.9px`）。
    // 两屏的美术是整屏拉伸 1.28 的（见 `ui::UI_SCALE`），字**不能跟着位图一起拉** ——
    // 那是"把 14px 的字拉成 17.9px"（糊）。这里直接把字号给足，落点由 `draw_ui` 换算。
    let mut ui_texts = font::TextCache::new(mir2_core::text::UI_PX * ui::UI_SCALE);
    // 世界里的字（怪物名字/伤害飘字）单独一份、小一档 —— 见 `text::NAME_PX`。
    let mut names = font::TextCache::new(mir2_core::text::NAME_PX);

    // 素材浏览器的状态（F3）
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // 地图模式的状态
    let mut mode: u8 = 1; // 1 = 登录界面（从头开始就是它），2 = 地图，3 = 素材浏览器
                          // 鼠标走路：目标格 + 是否跑（左键走、右键跑；松开清空）。`move_at` 是步频节流。
    let mut move_target: Option<(i32, i32, bool)> = None;
    let mut move_at = Instant::now();
    // **按住的是哪个键**（松开清掉）+ 上次"重取目标"的时刻 —— 按住时每
    // [`MOUSE_REPEAT_MS`] 重跑一遍按下逻辑（照原版 `DXDrawMouseMove`）。
    let mut held_move: Option<MouseButton> = None;
    let mut retarget_at = Instant::now();
    // 移动被拒的"记账"（`World::move_fail` 的上一帧读数）与**锁到什么时候**
    //（照原版 `ActionFailLock`，见 [`MOVE_FAIL_LOCK_MS`]）。
    let mut last_move_fail = 0u64;
    let mut move_block_until = Instant::now();
    // 悬停的那个实体（由 `draw_map_view` 每帧算出来 —— 要精灵落点，见 `actor_rect`）
    let mut hover: Option<u64>;
    // 进世界的按键提示只推一次（见下面那段）
    let mut hint_pushed = false;
    // 悬停可攻击目标时把光标换成"准星"（Crystal 是 `MouseCursor.Attack`，
    // `GameScene.cs:432-433`）；只在**状态变了**才设，别每帧调。
    let cursor_arrow = Cursor::from_system(SystemCursor::Arrow)?;
    let cursor_cross = Cursor::from_system(SystemCursor::Crosshair)?;
    let mut cursor_is_cross = false;
    // **锁定的攻击目标**（左键点怪锁住）：之后每帧自动"靠近 / 出手"，直到它死掉或消失
    //（照原版 `g_TargetCret` + `MouseTimerTimer`，`ClMain.pas:2863-2878 / 2962-2997`）。
    // `attack_at` 是出手节流 —— 原版是 `CanNextHit`。
    let mut combat_target: Option<u64> = None;
    let mut attack_at = Instant::now();
    let mut map_i: usize = 0;
    let mut map: Option<Map> = None;
    let mut map_err = String::new();
    // 相机：**浮点格**（进了世界由 [`follow_cam`] 每帧算；离线时是手动镜头）。
    let mut cam = (0f32, 0f32);
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, TileTex<'_>> = HashMap::new();
    let mut sprites = SpriteCache::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    // 调试叠加层（D 开关）：画格网 + 每层落点框 + 鼠标十字线，并"点哪读哪"
    let mut debug = false;
    let mut layers: u8 = LAYERS_ALL; // 三层显隐掩码（CTRL+1/2/3 独立开关，L 循环单选）
    let mut mouse = (0.0f32, 0.0f32);

    let mut music_on = true;
    // 小地图（Tab）/ 大地图（M）—— 原版 1.76 的键位，默认都关（原版也要按才出来）
    let mut minimap_on = false;
    let mut bigmap_on = false;
    // 音效开关：原版是**两个独立开关**（音效 / 音乐，`MShare.pas:213-214`），
    // 所以这里也是两个（`N` 切音效）。
    let mut sfx_on = true;
    // 上一步的走路动画帧号 —— 脚步只在**帧号变到 1 / 4** 时响一次
    //（原版就是这么对齐的，`Actor.pas:2659-2660`）。
    let mut last_foot_frame: Option<u16> = None;
    // ⚠️ "现在"必须在**每帧开头**取（见循环里的重取）。原来只在循环外取一次，
    // 于是它是个常量：选角场景按 `now - last` 算 dt ⇒ dt 恒为 0 ⇒ **动画永不推进**
    //（实测踩过：选中角色后小人一动不动）。
    // 这里不写初值：唯一的作用域就是循环体内，初值只会是"读了但没人用"的警告。
    let mut started;

    // 联网状态（`C` 键连接/断开）。地址与会话号走环境变量，见 `Net::connect`。
    let mut net: Option<Net> = None;
    // 环境变量给了会话号就**开机自动连**（省得每次手按 `C`；开发时最常用）。
    if std::env::var("MIR2_SESSION").is_ok() {
        match Net::connect() {
            Ok(n) => {
                println!("[net] {}", n.status);
                net = Some(n);
            }
            Err(e) => println!("[net] 连不上：{e}"),
        }
    }

    // 给了会话号就说明"我已经有会话了" ⇒ 直接进地图（登录界面留给真登录用）
    if net.is_some() {
        mode = 2;
    }

    // 载入初始地图
    if let Some(a) = &archive {
        load_map(a, map_i, &mut map, &mut map_err, &mut cam);
    }

    'main: loop {
        // 每帧重新取"现在"：所有按时间推进的东西（选角动画、开门动画、移动补间）
        // 都拿它当基准。⚠️ 漏了这行 = 动画全部静止（踩过）。
        started = Instant::now();

        for ev in events.poll_iter() {
            // ⚠️ 事件里的 `x/y` 是**窗口坐标**（高 DPI 下还是物理像素），而界面画在
            // 800×600 的逻辑空间里 ⇒ 一律让 SDL 换算（它同时管逻辑呈现的缩放与留边）。
            // 漏了它的表现是"按钮点不中 / 点哪走哪偏一截"（踩过）。
            let ev = ev.get_converted_coords(&canvas).unwrap_or(ev);
            match ev {
                Event::Quit { .. } => break 'main,
                // 窗口尺寸/像素尺寸变了 ⇒ 重挑呈现模式（见 `apply_presentation`）。
                // ⚠️ 必须挂上：不然后面那次 resize 还把画布按老尺寸缩放（画面拉变形）。
                Event::Window { win_event, .. } => {
                    let changed = matches!(
                        win_event,
                        WindowEvent::Resized(..) | WindowEvent::PixelSizeChanged(..)
                    );
                    if changed {
                        if let Err(e) = apply_presentation(&mut canvas) {
                            eprintln!("[mir2-app] {e}");
                        }
                    }
                }
                Event::KeyDown {
                    keycode, keymod, ..
                } => match keycode {
                    Some(Keycode::Escape) => break 'main,
                    // ⚠️ **开发键让开原版键位**（口径见 `docs/use.md`）：F1~F8 是技能、
                    // F9~F12 是包裹/属性/技能/内挂、M 是大地图、Tab 是小地图、数字是快捷物品
                    // ⇒ 这些"开发查看器"入口统统收进 **Ctrl+**，原版键位留给真功能。
                    Some(Keycode::F1) if ctrl(keymod) => mode = 1,
                    Some(Keycode::F2) if ctrl(keymod) => mode = 2,
                    Some(Keycode::F3) if ctrl(keymod) => mode = 3,
                    Some(Keycode::M) if ctrl(keymod) => {
                        music_on = !music_on;
                        sound.set_music_on(music_on);
                        // 带上"正在响几路 / 有没有 BGM"：一眼看出混音器是不是活的
                        let (voices, bgm, ..) = sound.stats();
                        status = format!(
                            "MUSIC {}  [sfx {} voices, bgm {}]",
                            if music_on { "ON" } else { "OFF" },
                            voices,
                            if bgm { "ON" } else { "OFF" }
                        );
                    }
                    Some(Keycode::N) if ctrl(keymod) => {
                        sfx_on = !sfx_on;
                        sound.set_sfx_on(sfx_on);
                        let (voices, bgm, ..) = sound.stats();
                        status = format!(
                            "SOUND {}  [sfx {} voices, bgm {}]",
                            if sfx_on { "ON" } else { "OFF" },
                            voices,
                            if bgm { "ON" } else { "OFF" }
                        );
                    }
                    // 选角：键盘是**我们的扩展**（原版选角场景只认鼠标）
                    _ if mode == 4 => {
                        if let Some(k) = keycode {
                            let act = match select_scene.as_mut() {
                                Some(s) => s.on_key(k),
                                None => select::Action::None,
                            };
                            if do_select_action(act, &mut net, &mut select_scene, &sound, &sounds)?
                            {
                                break 'main;
                            }
                        }
                    }
                    // 登录界面：全部交互在 `login` 里（Tab/退格/回车/ESC），这里只把
                    // 它给出的动作翻译成"接下来干什么"。
                    _ if mode == 1 => {
                        if let Some(k) = keycode {
                            let act = login.on_key(k);
                            // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                            if act != login::Action::None {
                                sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                            }
                            match act {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::SubmitSignup => {
                                    submit_signup(&mut login, &mut net, &mut status)
                                }
                                login::Action::NewAccount => {
                                    // 面板切换在 `login` 里已经做完了（`enter_signup`），
                                    // 这里只给状态栏一句人话（原版是开 `DNewAccount`）。
                                    status = "NEW ACCOUNT".into();
                                }
                                login::Action::CancelSignup => status = String::new(),
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::Quit => break 'main,
                                login::Action::None | login::Action::Dismiss => {}
                            }
                        }
                    }
                    // 素材浏览器（开发用）
                    _ if mode == 3 => match keycode {
                        Some(Keycode::LeftBracket) => {
                            lib_idx = (lib_idx + LIBS.len() - 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::RightBracket) => {
                            lib_idx = (lib_idx + 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::Comma) | Some(Keycode::Left) => {
                            img_idx = img_idx.saturating_sub(1);
                        }
                        Some(Keycode::Period) | Some(Keycode::Right) => img_idx += 1,
                        _ => {}
                    },
                    _ if mode == 2 => match keycode {
                        // 方向键：**联网且在世界里 ⇒ 走一步**（相机跟着自己）；否则平移镜头。
                        Some(Keycode::Left) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirLeft) {
                                cam.0 -= 2.0
                            }
                        }
                        Some(Keycode::Right) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirRight) {
                                cam.0 += 2.0
                            }
                        }
                        Some(Keycode::Up) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirUp) {
                                cam.1 -= 2.0
                            }
                        }
                        Some(Keycode::Down) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirDown) {
                                cam.1 += 2.0
                            }
                        }
                        // Tab = **小地图**开关、M = **大地图**开关（原版 1.76 的键位，
                        // 见 `docs/use.md`；音乐已经挪到 Ctrl+M，不再抢 M）。
                        Some(Keycode::Tab) => minimap_on = !minimap_on,
                        Some(Keycode::M) => bigmap_on = !bigmap_on,
                        // 空格：打一下身边的目标（A′：走 + 砍 = 能玩）。**左键点怪**才是
                        // 主路（会锁住目标、自动靠近）—— 见下面 `MouseButtonDown` 那段。
                        Some(Keycode::Space) => {
                            if let Some(n) = net.as_ref().filter(|n| n.world.in_world()) {
                                if n.attack_adjacent() {
                                    swing_sfx(n, &sound, &sounds);
                                } else {
                                    println!("[net] 身边没有可打的目标（八格内）");
                                }
                            }
                        }
                        // C：连接/断开新协议服务端（地址与会话号走环境变量，见 `Net::connect`）。
                        Some(Keycode::C) => {
                            if net.is_some() {
                                println!("[net] 主动断开");
                                net = None;
                            } else {
                                match Net::connect() {
                                    Ok(n) => {
                                        println!("[net] {}", n.status);
                                        net = Some(n);
                                    }
                                    Err(e) => println!("[net] 连不上：{e}"),
                                }
                            }
                        }
                        Some(Keycode::Home) => cam = (0.0, 0.0),
                        // ---- 调试叠加层（只在地图模式，避免污染登录输入框）----
                        // 辅助线/坐标叠加层：默认关闭（见 `DEBUG_OVERLAY`）
                        Some(Keycode::D) if DEBUG_OVERLAY => {
                            debug = !debug;
                            println!(
                                "[debug] 叠加层 {}（L 或 CTRL+1/2/3 控制图层显隐 / P 打印绘制清单 / 左键点哪读哪）",
                                if debug { "ON" } else { "OFF" }
                            );
                        }
                        // 逐层独立显隐：排查错位时最常用的是"关掉一层看底下那层"
                        Some(Keycode::_1) | Some(Keycode::_2) | Some(Keycode::_3)
                            if DEBUG_LAYERS
                                && (keymod.intersects(Mod::LCTRLMOD)
                                    || keymod.intersects(Mod::RCTRLMOD)) =>
                        {
                            let (bit, name) = match keycode {
                                Some(Keycode::_1) => (1u8, "地表 Tiles"),
                                Some(Keycode::_2) => (2u8, "中间 SmTiles"),
                                _ => (4u8, "前景 Objects"),
                            };
                            layers ^= bit;
                            println!(
                                "[layer] {} {}   →   当前可见 {}（G=地表 M=中间 F=前景）",
                                name,
                                if layers & bit != 0 {
                                    "显示"
                                } else {
                                    "隐藏"
                                },
                                layers_desc(layers)
                            );
                        }
                        // `L` 是同一个功能的"循环"绑定 —— 只关 CTRL 那三个键等于
                        // 留了后门，所以一起跟着 `DEBUG_LAYERS` 走。
                        Some(Keycode::L) if DEBUG_LAYERS => {
                            // 循环：全部 → 仅地表 → 仅中间 → 仅前景 → 全部
                            layers = match layers {
                                LAYERS_ALL => 1,
                                1 => 2,
                                2 => 4,
                                _ => LAYERS_ALL,
                            };
                            println!("[layer] 过滤循环   →   当前可见 {}", layers_desc(layers));
                        }
                        Some(Keycode::P) => {
                            dump_draws(&draws, cam_parts(cam).cell, &tiles, layers);
                        }
                        Some(Keycode::LeftBracket) | Some(Keycode::RightBracket) => {
                            if let Some(a) = &archive {
                                let step: i64 = if keycode == Some(Keycode::RightBracket) {
                                    1
                                } else {
                                    -1
                                };
                                let n = a.len() as i64;
                                map_i = (((map_i as i64 + step) % n + n) % n) as usize;
                                load_map(a, map_i, &mut map, &mut map_err, &mut cam);
                            }
                        }
                        _ => {}
                    },
                    _ => {}
                },
                // 鼠标位置（已在循环头换算成界面的 800×600 空间）
                Event::MouseMotion { x, y, .. } => mouse = (x, y),
                // 鼠标移动：**在世界里 ⇒ 左键走 / 右键跑**（照原版 `ClMain.pas:2246-2352`：
                // 左键 = 走，右键 = 跑；方向由鼠标相对角色的方位定，见 `dir_to`）。
                // 离线看地图（没进世界）时左键仍是那个诊断探针 —— 它是开发用的，
                // 别和"走路"抢同一个键。
                Event::MouseButtonDown {
                    mouse_btn, x, y, ..
                } if mode == 2 => {
                    let in_world = net.as_ref().is_some_and(|n| n.world.in_world());
                    match mouse_btn {
                        MouseButton::Left | MouseButton::Right if in_world => {
                            let cell = screen_to_cell(cam, x, y);
                            let run = mouse_btn == MouseButton::Right;
                            // 按住时每 300ms 要拿"当前鼠标位置"重取目标（见下面 `MOUSE_REPEAT_MS`
                            // 那段）⇒ 按下这一刻就得把位置记上（光等 `MouseMotion` 会漏掉
                            // "按下后一动不动"的那种按住）。
                            mouse = (x, y);
                            // 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：
                            // **先清掉旧目标**，点到**活怪**就锁住它（之后每帧自动靠近/出手，
                            // 直到它死掉或消失）；点空地 ⇒ 走/跑到那一格。
                            let (ct, mt) = net
                                .as_ref()
                                .map(|n| mouse_intent(&n.world, cell, run))
                                .unwrap_or((None, None));
                            combat_target = ct;
                            move_target = mt;
                            attack_at = Instant::now(); // 立刻判"该出手还是该靠近"
                            move_at = Instant::now(); // 立刻踏出第一步
                                                      // ⚠️ **按住不放要能一直走**：原版靠 `DXDrawMouseMove`
                                                      //（`ClMain.pas:2678-2679`）—— 按住时只要距上次 >300ms 就**重跑一遍
                                                      // 按下逻辑**，于是"鼠标那一格"被反复重新当成目标。少了它，
                                                      // 目标格是按下那一刻定死的 ⇒ 走到那儿就停（用户 2026-10-08 报的
                                                      // "按住只能走数次"）。
                            held_move = Some(mouse_btn);
                            retarget_at = Instant::now();
                            match ct {
                                Some(id) => {
                                    println!("[net] 锁定目标 ActorId={id}（靠近后自动出手）");
                                }
                                None => println!(
                                    "[move] 目标格 ({},{})：{}",
                                    cell.0,
                                    cell.1,
                                    if run { "跑" } else { "走" }
                                ),
                            }
                        }
                        MouseButton::Left => probe_at(x, y, cam, &draws, &tiles, layers),
                        _ => {}
                    }
                }
                // 松开就停（原版也是松手清目标：`ClMain.pas:2384-2389`）
                // ⚠️ **不清** `combat_target`：原版锁住的目标是"打到它死/消失"为止
                //（`MouseTimerTimer`，`ClMain.pas:2962-2997`），不是"松手就取消"。
                Event::MouseButtonUp { mouse_btn, .. }
                    if mode == 2 && matches!(mouse_btn, MouseButton::Left | MouseButton::Right) =>
                {
                    move_target = None;
                    held_move = None;
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    // 这两屏的命中测试活在**设计空间**（800×600）里 ⇒ 鼠标先除回去
                    let (x, y) = ui::ui_inv_pt((x, y));
                    if let (Some(dir), Some(scene)) = (asset_dir.as_ref(), select_scene.as_mut()) {
                        // 弹窗的几何要给进去：它那颗 [确定] 也走"按下与抬起同一颗"
                        let msg = select::msgbox_geom(&mut ui, dir, UI_WIN);
                        if let Some(l) =
                            mir2_core::select_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i))
                        {
                            scene.on_down((x, y), &l, msg);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    let (x, y) = ui::ui_inv_pt((x, y)); // 设计空间（见上面那条）
                    let act = match (asset_dir.as_ref(), select_scene.as_mut()) {
                        (Some(dir), Some(scene)) => {
                            // 弹窗的几何也要给进去：不然它那颗 [确定] 点不中（见 `Select::on_up`）
                            let msg = select::msgbox_geom(&mut ui, dir, UI_WIN);
                            match mir2_core::select_ui::Layout::build(UI_WIN, |c, i| {
                                ui.size(dir, c, i)
                            }) {
                                Some(l) => scene.on_up((x, y), &l, msg),
                                None => select::Action::None,
                            }
                        }
                        _ => select::Action::None,
                    };
                    if do_select_action(act, &mut net, &mut select_scene, &sound, &sounds)? {
                        break 'main;
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 1 => {
                    // 版式每帧现算（尺寸来自容器头，不解压 ⇒ 很便宜），用于命中判定。
                    // ⚠️ 版式在**设计空间**（800×600）里 ⇒ 鼠标先除回去（见 `UI_WIN`）
                    let (x, y) = ui::ui_inv_pt((x, y));
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            login.on_down((x, y), &l);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 1 => {
                    let (x, y) = ui::ui_inv_pt((x, y)); // 设计空间（见上面那条）
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            let act = login.on_up((x, y), &l);
                            // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                            if act != login::Action::None {
                                sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                            }
                            match act {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::SubmitSignup => {
                                    submit_signup(&mut login, &mut net, &mut status)
                                }
                                login::Action::Quit => break 'main,
                                login::Action::Dismiss => {}
                                login::Action::NewAccount => {
                                    status = "NEW ACCOUNT".into();
                                }
                                login::Action::CancelSignup => status = String::new(),
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::None => {}
                            }
                        }
                    }
                }
                Event::TextInput { text: t, .. } if mode == 1 => login.on_text(&t),
                // 选角界面的**建角对话框**也吃文字（`Select::on_text` 自己判断对话框开没开、
                // 焦点在不在姓名上）—— 少了这条，姓名框打字没反应（原版是 `TEdit` 收键）。
                Event::TextInput { text: t, .. } if mode == 4 => {
                    if let Some(s) = select_scene.as_mut() {
                        s.on_text(&t);
                    }
                }
                _ => {}
            }
        }

        // 联网：把网络线程收到的东西推进状态机（**每帧一次**，永不阻塞）。
        if let Some(n) = &mut net {
            n.pump();
            // 登录界面与网络状态互相照应。三件事：
            //   ① 失败 ⇒ 弹窗（原版也是 `DMessageDlg`），并把"登录中"解掉；
            //   ② 进世界 ⇒ 开始播开门动画（原版 `IntroScn.pas:907-914`），播完切地图；
            //   ③ 成功拿到的会话号存下来 —— 之后重连走 `Reconnect`，不必再输口令。
            // ⚠️ **取走**失败原因，而不是每帧读 `entrance.failed()` / `n.fail`：
            // 两者都是粘性状态（`stage == Failed` 会一直挂着）⇒ 每帧读的话，用户点了[确定]
            // 之后下一帧它又弹回来，看着就是"弹窗关不掉"（用户 2026-10-08 报的）。
            // 弹窗正开着就不取（那条失败会等关掉之后再弹）—— 免得把两条原因挤掉一条。
            if login.error.is_none() {
                if let Some(why) = n.take_fail() {
                    login.error = Some(why);
                    login.busy = false;
                }
            }
            // 登录**通过**（清单在手）⇒ 原版 `OpenLoginDoor`：藏小窗 + 开门 + 开门声。
            //
            // ⚠️ 触发点是"登录通过"，**不是"进世界"**：原版由登录成功那条路调
            // `OpenLoginDoor`（`IntroScn.pas:795-801`），门放完才 `ChangeScene(stSelectChr)`
            //（`IntroScn.pas:838-847`）。早先挂在 `in_world()` 上 ⇒ 门排在选角与进图之后，
            // 而且门画在登录框背后 ⇒ 看起来"压根没有开门动画"（用户报的就是这个）。
            // `login.busy` 的含义正是"这次是口令登录"（`submit_login` 设的）。
            if login.busy && n.entrance.stage() == &mir2_core::entrance::Stage::AwaitList {
                login.opened_at = Some(Instant::now());
                login.busy = false;
                select_scene = None;
                mode = 1; // 开门动画在登录屏上播（见 `login::draw` 的说明）
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 兜底：**认领会话**那条路（没有口令登录这一步）落到世界里时才补一次开门。
            if n.entrance.in_world() && login.opened_at.is_none() {
                login.opened_at = Some(Instant::now());
                login.busy = false;
                select_scene = None;
                mode = 1;
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 登录成功后会停在 `AwaitPick`（`set_manual_pick`）⇒ 切到选角场景。
            //
            // ⚠️ "停在等你选"是**状态机说的**，不是我们猜的时机：角色列表就在它手上
            //（`entrance.characters()`），界面只负责显示与选择。
            // 每帧"网络 → 画面"的决策：**纯函数**（见 `plan` 的说明 —— 决策必须放在
            // 能单测的地方，主循环只负责执行）。判据全是状态机说的事实，没有一处是"猜时机"。
            let chars: Vec<select::CharEntry> = n
                .entrance
                .characters()
                .iter()
                .map(select::CharEntry::from_summary)
                .collect();
            let p = plan(
                mode,
                n.entrance.stage() == &mir2_core::entrance::Stage::AwaitPick,
                n.entrance.in_world(),
                list_changed(select_scene.as_ref(), &chars),
                // "门挡着" = 开过门（`opened_at` 有值）但还没放完
                login.opened_at.is_some() && !login.door_done(),
            );
            // ⚠️ **只在列表真的变了**的时候才重建场景。这段在"门还在放"的那三秒里每帧都会
            // 走到 —— 早先每帧重建 + 每帧 println，表现是**日志刷屏**（用户报过），
            // 而且把用户已经移好的选中位置**每帧抹回第 0 个**。
            // 列表真的会变的场合只有两种：建/删角回执后的重拉、重连回落。
            if p.rebuild_select {
                println!("[net] 角色列表：{} 个", chars.len());
                let mut scene = select::Select::new(chars);
                // `MIR2_CHAR=<id>` 指定初选（手动模式下状态机不看它了，落到界面上）
                if let Ok(v) = std::env::var("MIR2_CHAR") {
                    match v.parse::<u64>() {
                        Ok(id) => scene.pick_id(id),
                        Err(_) => println!("[net] MIR2_CHAR={v} 不是整数，忽略"),
                    }
                }
                select_scene = Some(scene);
            }
            // 门还在放就先不切：原版顺序是"门放完 → `ChangeScene(stSelectChr)`"
            //（`IntroScn.pas:838-847`），一上来就切会把门当场掐掉（用户看到的就是"没有开门动画"）。
            // 门放完那一刻由主循环里 `mode == 1` 那条分支接手。
            if p.enter_select {
                mode = 4;
            }
            // 选角通过 ⇒ 换到游戏主场景（原版在这里是 `ChangeScene(stPlay)`）
            if p.enter_play {
                mode = 2;
                // 选角场景用不上了（它的槽动画也不必再跑）
                select_scene = None;
            }
            // 选角被拒（例如租约被占）⇒ 弹给用户换一个（状态机会退回 `AwaitPick`）。
            if let Some(why) = n.entrance.take_pick_error() {
                if let Some(scene) = select_scene.as_mut() {
                    scene.say(why);
                }
            }
            // 在选角场景里出了**致命**错（服务端断开…）⇒ 回登录界面，
            // 用那套已有的弹窗说清楚（否则用户会在选角界面上干等）。
            if mode == 4 && n.entrance.failed().is_some() {
                mode = 1;
            }
            if let Some(tok) = n.entrance.session_token() {
                n.session = tok;
            }
        }
        // 鼠标走路：按住时**一步步**朝目标格走。
        //
        // 原版是"按住每 ≥300ms 重新触发一次 `_DXDrawMouseDown`、松开清目标"
        //（`ClMain.pas:2115-2116 / 2384-2389`），步频本身受服务端节流限制 ⇒
        // 我们按 `WALK_MS`/`RUN_MS` 发，到了目标格就停。
        //
        // ⚠️ "这一步走还是跑"由 [`next_move_step`] 定：**距离 < 2 就不许跑** ——
        // 跑步一次跨 2 格，奇数距离时不许跑才不会"跨过去再跨回来"（用户报的"左右乱换"）。

        // **按住鼠标不放 ⇒ 一直走/跑**：每 [`MOUSE_REPEAT_MS`] 拿**当前鼠标那一格**重新定目标。
        //
        // 照原版 `ClMain.pas:2678-2679`（`DXDrawMouseMove` 里"按住且距上次 >300ms ⇒
        // 重跑一遍 `_DXDrawMouseDown`"）。少了这一步，目标格就是按下那一刻定死的 ⇒
        // 走到那儿就停（用户 2026-10-08 报的"按住只能走数次"）。
        //
        // ⚠️ 它必须与"人物不动、地图卷动"（[`follow_cam`]）配套：镜头跟着人走，鼠标**屏幕**
        // 位置不变时它对应的**格子**会往前跑 ⇒ 按住不放就是**一直走下去**（原版的"按住跑直线"）。
        // 镜头不卷或者目标不重取，两样单独都做不出这个手感。
        if mode == 2
            && held_move.is_some()
            && retarget_at.elapsed() >= Duration::from_millis(MOUSE_REPEAT_MS)
        {
            retarget_at = Instant::now();
            if let Some(n) = net.as_ref().filter(|n| n.world.in_world()) {
                let cell = screen_to_cell(cam, mouse.0, mouse.1);
                let run = held_move == Some(MouseButton::Right);
                // ⚠️ 用 `mouse_repeat` 而不是 `mouse_intent`：按住时**不能**因为
                // "光标不在怪身上了"就把锁住的怪丢掉（镜头跟着人走，光标一定会滑开，
                // 否则按住点怪会半路变成走路）。见 `mouse_repeat` 的说明。
                let (ct, mt) = mouse_repeat(&n.world, cell, run, combat_target);
                combat_target = ct;
                move_target = mt;
            }
        }

        // **锁定的攻击目标**：够近就出手、不够近就靠近。
        //
        // 照原版：`_DXDrawMouseDown` 点到怪就 `g_TargetCret := target`（`ClMain.pas:2866`），
        // 之后 `MouseTimerTimer`（`ClMain.pas:2962-2997`）**每帧**对那个目标调 `AttackTarget`
        // —— `AttackTarget`（`:2691-2743`）自己判"相邻就砍、不够近就朝它旁边那格走/跑"。
        // 也就是原版**没有"按住才打"这一说**：锁住了就一直打，直到它死掉或消失。
        if mode == 2 && !net.as_ref().is_some_and(|n| n.world.self_dead) {
            if let Some(id) = combat_target {
                let step = net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .and_then(|n| n.world.combat_step(id));
                match step {
                    // 死了 / 消失了（尸体被清）/ 自己掉线了 ⇒ 解除锁定
                    None => {
                        println!("[net] 目标不在了，解除锁定");
                        combat_target = None;
                        move_target = None;
                    }
                    Some(mir2_core::world::CombatStep::Attack) => {
                        // ⚠️ **这一步走完再出手** —— 原版 `CanNextAction` = `g_MySelf.IsIdle`
                        //（`Actor.pas:1722-1736`：`m_nCurrentAction <> 0` 就"不空"，不许发下一个动作），
                        // 而 `ActionFinished` 看的是**动画有没有播到 `m_nEndFrame`**
                        // ⇒ 走路那段动作没播完，原版**发不出攻击**。
                        //
                        // 为什么我们特别需要它：我们不做移动预测，位置是**服务端权威**的 ——
                        // 服务端确认"到了"时，画面上的补间才刚起步 ⇒ `combat_step` 已经说"相邻、
                        // 该出手"，于是挥砍动画在人还没走到时就播了（用户 2026-10-08 报的）。
                        let stepping = net
                            .as_ref()
                            .and_then(|n| n.anims.get(&n.world.self_id))
                            .is_some_and(|a| a.moving(started));
                        if can_attack(stepping, attack_at.elapsed()) {
                            if let Some(n) = net.as_ref() {
                                let _ = n.attack_target(id);
                                swing_sfx(n, &sound, &sounds);
                            }
                            attack_at = Instant::now();
                        }
                    }
                    // 够不着：复用鼠标走路那条路（步频/节流/"到了就停"都在下面那段里）
                    Some(mir2_core::world::CombatStep::Approach { x, y, run }) => {
                        move_target = Some((x, y, run));
                    }
                }
            }
        }

        // 服务端**拒了这一步**（`MoveRejected`）⇒ 照原版 `ActionFailed`
        //（`ClMain.pas:4005-4012`：清走法目标 + 锁 1 秒不许再发）。
        //
        // ⚠️ 少了这个反应，撞墙时客户端会朝同一个方向每 `WALK_MS` 发一次、每次都被拒，
        // 人物就"卡在那儿不动"，而且服务端日志刷满 —— "跑不到怪身边"多半有它一份。
        // 计数**变没变**就是"刚刚被拒了一次"的信号（见 `World::move_fail`）。
        let fails = net.as_ref().map_or(0, |n| n.world.move_fail);
        if fails != last_move_fail {
            let reason = net.as_ref().map_or(0, |n| n.world.move_fail_reason);
            last_move_fail = fails;
            move_target = None;
            move_at = Instant::now();
            move_block_until = Instant::now() + Duration::from_millis(MOVE_FAIL_LOCK_MS);
            println!(
                "[move] 服务端拒绝了这一步（reason={reason}，1=超速 2=越界 3=阻挡）\
                 —— 清走法目标 + 锁 {MOVE_FAIL_LOCK_MS}ms"
            );
        }

        if mode == 2 {
            if let Some((tx, ty, run)) = move_target {
                let here = net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .map(|n| (n.world.self_pos.0, n.world.self_pos.1));
                match here {
                    // 没进世界（掉线/还没到）⇒ 目标作废，别攒着一堆移动
                    None => move_target = None,
                    Some(pos) => match next_move_step(pos, (tx, ty), run) {
                        // 已经站在目标格上 ⇒ 收工（原版到点也停）
                        None => move_target = None,
                        Some((dir, step_run)) => {
                            let gap = if step_run { RUN_MS } else { WALK_MS };
                            // 被拒后的锁还没到期 ⇒ 先别发（原版 `IsUnLockAction`，
                            // `ClMain.pas:4014-4021`：锁着就一律不许动）
                            if move_block_until <= Instant::now()
                                && move_at.elapsed() >= Duration::from_millis(gap)
                            {
                                move_if_online(&net, dir, step_run);
                                move_at = Instant::now();
                            }
                        }
                    },
                }
            }
        }

        // 建号回执（D-32）：**不自动登录**（与原版一致 —— 它也只是弹个提示，
        // `ClMain.pas:3684-3691`）。提示用登录界面那套模态框说，面板切回登录；
        // 那条"只为建号用过的连接"顺手收尾：登录会另开一条（nonce 要重新握手）。
        let signup = net.as_mut().and_then(|n| n.entrance.take_signup_msg());
        if let Some((ok, msg)) = signup {
            login.leave_signup();
            login.busy = false;
            net = None;
            status = if ok {
                "ACCOUNT CREATED - PLEASE SIGN IN".into()
            } else {
                "SIGN UP FAILED".into()
            };
            login.error = Some(msg.clone());
            println!("[login] 建号{}：{msg}", if ok { "成功" } else { "失败" });
        }
        // 网络侧排出来的音效（挨打 / 死亡）：`pump` 在 `Net` 里，拿不到音频设备。
        if let Some(n) = net.as_mut() {
            for s in n.take_sfx() {
                sfx(&sound, &sounds, s);
            }
            if n.take_gameover() {
                // 自己死亡 ⇒ game over 音乐（原版 `Actor.pas:2373-2374`）
                bgm(&sound, &sounds, mir2_core::sound::BGM_GAMEOVER);
            }
        }
        // 选角场景排出来的音效（选中一个槽 ⇒ 解冻声 `101`，`IntroScn.pas:1170`）
        if let Some(scene) = select_scene.as_mut() {
            for s in scene.take_sfx() {
                sfx(&sound, &sounds, s);
            }
        }
        // 场景 BGM：登录（`IntroScn.pas:518`）与选角（`:1152`）各一首**循环**。
        // 进图音乐 `Music/<地图音乐号>.mp3` **暂时没有声音**（协议没有那个字段、
        // 手上也没有 mp3）⇒ 进地图就停掉上一首，而不是放错一首。
        // 自己死了就别停：那时该响的是 game over 那首（`Actor.pas:2373-2374`）。
        if mode == 4 {
            bgm(&sound, &sounds, mir2_core::sound::BGM_SELECT);
        } else if mode == 1 {
            // ⚠️ 开门动画期间**继续放**登录曲（原版 `PlayBGM(bmg_intro)` 一放放到换场景，
            // `IntroScn.pas:518`；门放完 `ChangeScene(stSelectChr)` 才换成选角曲）。
            // 早先这里多了个 `opened_at.is_none()` ⇒ 门一开就静音三秒（踩过）。
            bgm(&sound, &sounds, mir2_core::sound::BGM_LOGIN);
        } else if mode == 2 && !net.as_ref().is_some_and(|n| n.world.self_dead) && sound.stop_bgm()
        {
            println!("[audio] BGM 停（进图音乐这条路还没接：手上没有 mp3、协议里也没有音乐号）");
        }
        // 脚步：原版在走路动画的**帧 1 / 帧 4** 各响一次（`Actor.pas:2659-2660`），
        // 音色按**自己脚下那一格**定（原版把坐标对齐到偶数格，`Actor.pas:2146-2147`）。
        match (net.as_ref(), map.as_ref()) {
            (Some(n), Some(m)) if n.world.in_world() => match n.self_walk_frame(started) {
                Some((frame, running)) => {
                    if let Some(second_foot) = footstep_of(frame, last_foot_frame) {
                        let (mx, my) = (n.world.self_pos.0, n.world.self_pos.1);
                        let cell = m.at((mx.max(0) / 2 * 2) as usize, (my.max(0) / 2 * 2) as usize);
                        if let Some(c) = cell {
                            let t =
                                mir2_core::sound::terrain(c.bk_img, c.area, c.mid_img, c.fr_img);
                            sfx(
                                &sound,
                                &sounds,
                                mir2_core::sound::footstep(t, running, second_foot),
                            );
                        }
                    }
                    last_foot_frame = Some(frame);
                }
                None => last_foot_frame = None,
            },
            _ => last_foot_frame = None,
        }

        // 进了世界就让相机跟着自己（离线时保持手动镜头）。
        //
        // ⚠️ 跟的是**渲染位置**（补间后的浮点格），不是服务端那一格 —— 见 [`follow_cam`]。
        // 于是"人在屏幕中间不动、地图往前卷"，而不是"人在视口里蹭、镜头一格一跳"。
        if let Some(render) = net.as_ref().and_then(|n| n.self_render(started)) {
            cam = follow_cam(render);
        }

        // 地图镜头夹在合理范围内（允许露出边缘一格）
        if let Some(m) = &map {
            let max_x = (m.width as i32 - (WIN_W as i32 / UNIT_X) + 2).max(0) as f32;
            let max_y = (m.height as i32 - (VIEW_H as i32 / UNIT_Y) + 2).max(0) as f32;
            cam.0 = cam.0.clamp(-2.0, max_x);
            cam.1 = cam.1.clamp(-2.0, max_y);
        }

        // 前景动画的节拍：官方 `m_nAniCount` **每 50 ms 加一**（`PlayScn.pas:963`，
        // 固定定时器、与帧率无关）。所以这里按**真实时间**算，而不是每帧 +1 ——
        // 否则灯会随机器性能忽快忽慢。
        let ani_count = (started.elapsed().as_millis() / 50) as u32;

        canvas.set_draw_color(C_BG);
        canvas.clear();

        // 悬停的那个实体（由 `draw_map_view` 按精灵落点算出来）—— 光标/高亮都用它
        if mode == 2 {
            hover = draw_map_view(
                &mut canvas,
                &tex_creator,
                &mut names,
                &mut ui,
                &mut libs,
                &mut tiles,
                &mut draws,
                &mut sprites,
                &asset_dir,
                &map,
                &map_err,
                cam,
                map_i,
                archive.as_ref().map(|a| a.len()).unwrap_or(0),
                debug,
                layers,
                mouse,
                ani_count,
                net.as_ref(),
                combat_target,
            )?;
            // 光标跟着悬停状态走：悬停的是**怪**才换准星（原版悬停谁都不换光标，
            // 这是 Crystal 那套；换成"能打的东西"上才有意义）
            let want_cross = hover.is_some_and(|id| {
                net.as_ref().is_some_and(|n| {
                    n.world
                        .entities
                        .get(&id)
                        .is_some_and(|e| e.kind == mir2_core::world::KIND_MONSTER)
                })
            });
            if want_cross != cursor_is_cross {
                cursor_is_cross = want_cross;
                if want_cross {
                    cursor_cross.set();
                } else {
                    cursor_arrow.set();
                }
            }

            // 小地图 / 大地图（原版 `PlayScn.pas:791` 的 `DrawMiniMap`）
            draw_minimaps(
                &mut canvas,
                &tex_creator,
                &mut ui,
                &asset_dir,
                net.as_ref().filter(|n| n.world.in_world()).map(|n| {
                    (
                        n.world.minimap_index,
                        // ⚠️ 取**补间后**的位置（与画精灵同一份）：服务端位置只在
                        // "到位"时变 ⇒ 拿它当图心就是"跑完一格图才跳一格"（用户报的）。
                        self_render_pos(n.anims.get(&n.world.self_id), n.world.self_pos, started),
                    )
                }),
                minimap_on,
                bigmap_on,
                (WIN_W, WIN_H),
            )?;
        } else if mode == 4 {
            if let Some(scene) = select_scene.as_mut() {
                scene.draw(
                    &mut canvas,
                    &tex_creator,
                    &mut ui,
                    &mut ui_texts,
                    &asset_dir,
                    // ⚠️ 给的是**设计尺寸**（800×600）：版式算在设计空间里，画的时候整体乘 1.28
                    UI_WIN,
                    started,
                )?;
            }
        } else if mode == 1 {
            // 开门动画播完 ⇒ **换场景**。原版换到的是**选角**
            //（`ChangeScene(stSelectChr)`，`IntroScn.pas:838-847`），不是地图 ——
            // 早先这里直接 `mode = 2` 是错的（等于把选角整段跳过）。
            if login.door_done() {
                if select_scene.is_some() {
                    mode = 4;
                } else if net.as_ref().is_some_and(|n| n.world.in_world()) {
                    // 认领会话那条路没有选角：门放完直接进地图
                    mode = 2;
                }
                // 两个都没有 ⇒ 留在这一屏等（角色列表马上到）
            }
            login.draw(
                &mut canvas,
                &mut ui,
                &mut ui_texts,
                &tex_creator,
                &asset_dir,
                // ⚠️ 同上：设计尺寸
                UI_WIN,
                started,
            )?;
        } else {
            draw_asset_view(
                &mut canvas,
                &tex_creator,
                &asset_dir,
                &mut loaded,
                &mut sprite_tex,
                &status,
                music_on,
                lib_idx,
                &mut img_idx,
                started,
            )?;
        }

        // 底部那条**开发用**提示条撤了：现在那儿是 HUD 操作面板（字会压在按钮上）。
        // 按键提示改在**进世界时推一行到聊天区**（玩家看得到的那处）。
        if !hint_pushed && mode == 2 {
            if let Some(n) = net.as_mut() {
                if n.entered_once {
                    hint_pushed = true;
                    n.chat.push(trunc(hint_text(2), 30), C_CHAT_SYS);
                    println!("[hud] 按键提示已进聊天区：{}", trunc(hint_text(2), 30));
                }
            }
        }

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

/// 切换到第 `i` 张地图（按容器内名字升序）。
fn load_map(a: &Archive, i: usize, map: &mut Option<Map>, err: &mut String, cam: &mut (f32, f32)) {
    let Some(e) = a.entries().get(i) else {
        return;
    };
    let name = e.name.clone();
    match Map::load(a, &name) {
        Ok(m) => {
            println!(
                "[map] {} {}x{} {} B/格{}",
                name,
                m.width,
                m.height,
                m.cell_len,
                if m.is_extended() {
                    "（扩展布局）"
                } else {
                    ""
                }
            );
            // 镜头对准离中心最近的前景物件：中心常常是空地，
            // 一进去看到空白会让人以为渲染坏了。
            let (w, h) = (m.width as i32, m.height as i32);
            let (tx, ty) = m.nearest_front_tile(w / 2, h / 2).unwrap_or((w / 2, h / 2));
            let cols = WIN_W as i32 / UNIT_X;
            let rows = VIEW_H as i32 / UNIT_Y;
            *cam = (tx as f32 - cols as f32 / 2.0, ty as f32 - rows as f32 / 2.0);
            *err = String::new();
            *map = Some(m);
        }
        Err(e) => {
            // 已知缺口：EM*/T2* 族布局未定（assets.md §3.3b）——这里如实显示，不猜。
            println!("[map] {name} 解析失败：{e}");
            *err = e.to_string();
            *map = None;
        }
    }
}

/// 底部操作面板（HUD）：面板 + 血/魔法球 + 等级 + 经验条 + 聊天行 + 地图名/坐标。
///
/// 版式与图号**照抄原版**（`FState.pas` 的 `TFrmDlg.DBottomDirectPaint`，引文见上面那组常量）。
/// 它画在**世界之上**（原版也是最后贴上去的），所以在 `draw_map_view` 末尾调。
#[allow(clippy::too_many_arguments)]
fn draw_hud<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    names: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    // 地图名（容器里的标题；空则退回服务端那个代号）
    map_title: &str,
) -> Result<(), sdl3::Error> {
    let (bw, bh) = ui.size(dir, "Prguse", HUD_BOARD).unwrap_or((800, 251));
    let (bw, bh) = (bw as f32, bh as f32);
    // 原版：`btop := SCREENHEIGHT - d.height`
    let board_y = WIN_H as f32 - bh;
    // ⚠️ 一张 alpha 贴图就等价于原版那两刀（上半 120px 走色键、下半不透明）：
    // 我们的解码把调色板索引 0 当透明，上半正好是空的。
    //
    // 面板分三段贴（见 `HUD_SIDE_W`）：左右 1:1，中间那段拉宽到窗口宽 ——
    // 于是 1024 窗口下球和按钮都不变形，只有聊天框变宽。
    let mid_src_w = (bw - 2.0 * HUD_SIDE_W).max(1.0);
    let mid_dst_w = (WIN_W as f32 - 2.0 * HUD_SIDE_W).max(mid_src_w);
    for (sx, sw, dx, dw) in [
        (0.0, HUD_SIDE_W, 0.0, HUD_SIDE_W),             // 左：球那一块
        (HUD_SIDE_W, mid_src_w, HUD_SIDE_W, mid_dst_w), // 中：聊天框（拉宽）
        (bw - HUD_SIDE_W, HUD_SIDE_W, hud_right_x(), HUD_SIDE_W), // 右：按钮/状态
    ] {
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_BOARD,
            FRect::new(sx, 0.0, sw, bh),
            FRect::new(dx, board_y, dw, bh),
            255,
        );
    }

    // 血 / 魔法球：左半红 = HP、右半蓝 = MP，各自按比例从**下面**留一截（`gauge_band`）
    let (hp, max_hp) = net.map_or((0, 0), |n| n.world.self_hp.unwrap_or((0, 0)));
    let (mp, max_mp) = net.map_or((0, 0), |n| {
        n.world
            .ability
            .as_ref()
            .map_or((0, 0), |a| (a.mp, a.max_mp))
    });
    if max_hp > 0 && max_mp > 0 {
        let (ow, oh) = ui.size(dir, "Prguse", HUD_ORB).unwrap_or((92, 90));
        let (ow, oh) = (ow as i32, oh as i32);
        let half = ow / 2 - 1; // 原版：`rc.Right := d.ClientRect.Right div 2 - 1`
        let ball_y = board_y + ORB_AT.1;
        // HP：源矩形右边界砍到中线
        let (top, h) = gauge_band(hp as f32 / max_hp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB,
            FRect::new(0.0, top as f32, half as f32, h as f32),
            FRect::new(ORB_AT.0, ball_y + top as f32, half as f32, h as f32),
            255,
        );
        // MP：源矩形左边界从中线 +1 起，落点 x 也加它（原版 `40 + rc.Left`）
        let left = ow / 2 + 1;
        let (top, h) = gauge_band(mp as f32 / max_mp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB,
            FRect::new(left as f32, top as f32, (ow - 1 - left) as f32, h as f32),
            FRect::new(
                ORB_AT.0 + left as f32,
                ball_y + top as f32,
                (ow - 1 - left) as f32,
                h as f32,
            ),
            255,
        );
    }

    // 等级：原版 `PomiTextOut` —— 数字图（`Prguse[30..39]`，12×10）**每 8px 一位**，
    // 且**第一位画在 `x + 8`**（`for i := 1 to Length(str)`），这里照做。
    let level = net
        .and_then(|n| n.world.ability.as_ref().map(|a| a.level))
        .unwrap_or(1);
    let (dw, dh) = ui.size(dir, "Prguse", HUD_DIGIT0).unwrap_or((12, 10));
    let level_at = level_at();
    for (i, ch) in level.to_string().chars().enumerate() {
        let Some(d) = ch.to_digit(10) else { continue };
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_DIGIT0 + d,
            FRect::new(0.0, 0.0, dw as f32, dh as f32),
            FRect::new(
                level_at.0 + (i as f32 + 1.0) * 8.0,
                level_at.1,
                dw as f32,
                dh as f32,
            ),
            255,
        );
    }

    // 经验条：⚠️ **画不出来** —— 协议 `Ability` 里没有 exp/max_exp（原版 `SM_ABILITY` 有），
    // 服务端也没下发 ⇒ 这里如实留空（`HUD_EXP` / `EXP_AT` 先备着，见 D-49）。

    // 聊天行：原版是"每行自带背景色、OPAQUE 画"（`FState.pas:3868-3886`）——
    // 所以面板上那块浅色框只是**垫底**，真正的深底是文字自己铺出来的。
    // 我们铺一次深底 + 逐行前景色，效果一致。
    if let Some(n) = net {
        let chat = &n.chat;
        let shown = chat.lines.len().min(CHAT_VIEW_LINES);
        if shown > 0 {
            let skip = chat.lines.len() - shown;
            canvas.set_draw_color(Color::RGB(24, 24, 30));
            canvas.fill_rect(FRect::new(
                CHAT_AT.0 - 4.0,
                CHAT_AT.1 - 2.0,
                // 聊天框在中间那段里（面板那一段是被拉宽的 ⇒ 底色也跟着宽）
                (WIN_W as f32 - 2.0 * HUD_SIDE_W) - 12.0,
                shown as f32 * CHAT_LINE_H + 4.0,
            ))?;
            for (i, (line, col)) in chat.lines.iter().skip(skip).enumerate() {
                names.draw(
                    canvas,
                    tc,
                    line,
                    CHAT_AT.0,
                    CHAT_AT.1 + i as f32 * CHAT_LINE_H,
                    (col.r, col.g, col.b),
                    Some((0, 0, 0)),
                )?;
            }
        }
    }

    // 地图名 + 坐标（用户参考图里在左下角；原版 1.76 也在这块地方）
    if let Some(n) = net {
        if n.world.in_world() {
            let title = if map_title.is_empty() {
                n.world.map_name.as_str()
            } else {
                map_title
            };
            names.draw(
                canvas,
                tc,
                &format!(
                    "{}  {}:{}",
                    trunc(title, 12),
                    n.world.self_pos.0,
                    n.world.self_pos.1
                ),
                8.0,
                WIN_H as f32 - 20.0,
                (C_HUD_COORD.r, C_HUD_COORD.g, C_HUD_COORD.b),
                Some((0, 0, 0)),
            )?;
        }
    }
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn draw_map_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    names: &mut font::TextCache<'a>,
    ui: &mut ui::UiCache<'a>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<TileKey, TileTex<'a>>,
    draws: &mut Vec<TileDraw>,
    sprites: &mut SpriteCache<'a>,
    asset_dir: &Option<PathBuf>,
    map: &Option<Map>,
    map_err: &str,
    cam: (f32, f32),
    map_i: usize,
    map_count: usize,
    debug: bool,
    layers: u8,
    mouse: (f32, f32),
    ani_count: u32,
    net: Option<&Net>,
    // 锁定中的攻击目标（`None` = 没锁）—— 只用来给它的名字换色，让人看得出在打谁。
    combat_target: Option<u64>,
) -> Result<Option<u64>, sdl3::Error> {
    fill(canvas, 0.0, 0.0, WIN_W as f32, BAR_TOP, C_PANEL)?;

    let Some(dir) = asset_dir else {
        text(
            canvas,
            "ASSETS NOT FOUND - SET MIR2_ASSET_DIR",
            4.0,
            8.0,
            C_ERR,
        )?;
        return Ok(None);
    };

    if map.is_none() {
        let msg = if map_err.is_empty() {
            "NO MAP CONTAINER - RUN tools/m2pk/build.sh".to_string()
        } else {
            format!("PARSE FAILED: {}", trunc(map_err, 60))
        };
        text(canvas, &msg, 4.0, 8.0, C_ERR)?;
        return Ok(None);
    }
    let m = map.as_ref().unwrap();

    // 「画什么、按什么顺序画」是游戏知识，放在 core（map::visible_tiles，
    // 有单测守着三层顺序与隔格规则）；这里只负责取纹理 + 上屏。
    let cols = WIN_W as i32 / UNIT_X + 3;
    let rows = VIEW_H as i32 / UNIT_Y + 3;
    // 相机拆成"整格 + 亚格"：`visible_tiles` 只认整格（它给的落点是整数像素），
    // 亚格那半格由 `draw_tile` 减掉 ⇒ 地图逐帧平滑卷动（见 `cam_parts`）。
    let cp = cam_parts(cam);
    m.visible_tiles(cp.cell.0, cp.cell.1, cols, rows, ani_count, draws);
    // 裁剪到地图视口：`visible_tiles` 左上会多给一格（坐标可能为负），
    // 且高图块（树/墙）本身上端会超出视口——不裁剪就会画到上下信息条上。
    canvas.set_clip_rect(Some(Rect::new(0, BAR_TOP as i32, WIN_W, VIEW_H as u32)));
    let view = viewport_rect();
    for d in draws.iter() {
        // 逐层显隐（CTRL+1/2/3 / L）：关掉的层**既不画图块也不画调试框**。
        // 视口剔除：前景向下多扫了 35 行，那批候选多半够不着视口。
        // 先按 WZL 记录（不解码像素）判掉，省下解码与贴图上传。
        // ⚠️ 判据与落点必须是**同一份**（都含亚格偏移）——见 `tile_in_view`。
        if !tile_in_view(libs, dir, d, &cp, layers, &view) {
            continue;
        }
        draw_tile(canvas, tc, libs, tiles, dir, d, BAR_TOP, cp.sub)?;
    }
    canvas.set_clip_rect(None::<Rect>);

    // **悬停命中**（照原版 `g_FocusCret` / Crystal `MouseObject`）：按**精灵落点**
    // 而不是按格子（见 `actor_rect` 的说明）。重叠时取**脚最靠下**的那个 ——
    // Mir2 的 Y 序里它画在最前面，"指着谁就亮谁"。
    let mut hover: Option<u64> = None;
    if let Some(n) = net {
        if n.world.in_world() {
            let now = Instant::now();
            let mut best = f32::MIN;
            for e in n.world.entities.values() {
                let Some(r) = actor_rect(tc, sprites, dir, cam, e, n.anims.get(&e.id), now) else {
                    continue;
                };
                if mouse.0 >= r.x
                    && mouse.0 < r.x + r.w
                    && mouse.1 >= r.y
                    && mouse.1 < r.y + r.h
                    && e.y as f32 > best
                {
                    best = e.y as f32;
                    hover = Some(e.id);
                }
            }
        }
    }

    // 实体标记（B：联网之后"看得见世界"）。画在世界之上、调试叠加层之下 ——
    // 这样按 D 打开叠加层时，格网仍然压在最上面（否则标记会盖住格线，很难读）。
    if let Some(n) = net {
        if n.world.in_world() {
            let now = Instant::now();
            for e in n.world.entities.values() {
                // 颜色只用于**降级标记**（精灵走的是图本身）；尸体另给一色，
                // 这样"素材缺失 + 已死"也能一眼看出来。
                let color = if e.dead {
                    C_ENT_DEAD
                } else if combat_target == Some(e.id) {
                    // 锁定的目标：名字换成高亮色（"在打谁"要有反馈）
                    C_ENT_TARGET
                } else {
                    match e.kind {
                        0 => C_ENT_PLAYER,
                        2 => C_ENT_NPC,
                        _ => C_ENT_MONSTER,
                    }
                };
                draw_actor(
                    canvas,
                    tc,
                    names,
                    sprites,
                    dir,
                    cam,
                    e,
                    n.anims.get(&e.id),
                    now,
                    &e.name,
                    e.hp,
                    e.max_hp,
                    color,
                    // 悬停高亮（照原版 `g_FocusCret.DrawChr(..., blend=TRUE)`：
                    // **再画一遍**、半透明 —— 见 `HOVER_TINT`）
                    hover == Some(e.id),
                    // 别人不画数值（参考图里只有玩家头顶带）
                    false,
                )?;
            }

            // 自己：`entities` 里**没有自己**（快照刻意不含，见 core::world 的 self_feature）
            // ⇒ 在这里造一个临时实体走**同一条**绘制路径，免得"自己的画法"与别人漂成两套。
            let (hp, max_hp) = n.world.self_hp.unwrap_or((0, 0));
            let me = mir2_core::world::Entity {
                id: n.world.self_id,
                kind: 0,
                // 真名（协议 `EnterWorld.self_name`）；没给（老服务端）才退回占位符
                name: if n.world.self_name.is_empty() {
                    "[自己]".to_string()
                } else {
                    n.world.self_name.clone()
                },
                x: n.world.self_pos.0,
                y: n.world.self_pos.1,
                dir: n.world.self_dir,
                feature: n.world.self_feature,
                hp,
                // 自己那份"跑"标记（决定播 ActWalk 还是 ActRun）
                run: n.world.self_run,
                max_hp,
                status_bits: 0,
                dead: n.world.self_dead,
                action: n.world.self_action,
            };
            draw_actor(
                canvas,
                tc,
                names,
                sprites,
                dir,
                cam,
                &me,
                n.anims.get(&me.id),
                now,
                &me.name,
                hp,
                max_hp,
                C_ENT_SELF,
                false, // 自己永不算是"悬停高亮"（原版 `g_FocusCret <> g_MySelf`）
                true,  // 自己头顶画 `当前/总量`（用户参考图）
            )?;

            // 伤害飘字（A′：打怪要看得见数字）。往上飘，三档亮度代替淡出 ——
            // 8x8 调试字体只有一档颜色，靠 alpha 淡化在 `draw_debug_text` 上不一定生效。
            for (txt, fx, fy, born) in &n.floaters {
                let (px, py) = cell_to_screen(cam, *fx, *fy);
                let age = born.elapsed().as_millis();
                let col = if age < 300 {
                    C_DMG_HOT
                } else if age < 600 {
                    C_DMG_MID
                } else {
                    C_DMG_DIM
                };
                names.draw(
                    canvas,
                    tc,
                    txt,
                    px + 18.0,
                    py - 10.0 - age as f32 / 60.0,
                    (col.r, col.g, col.b),
                    Some((0, 0, 0)),
                )?;
            }
        }
    }

    // 辅助线/坐标叠加层（默认关闭，见 `DEBUG_OVERLAY`）
    if debug && DEBUG_OVERLAY {
        draw_debug_overlay(canvas, draws, tiles, cam, mouse, layers)?;
    }

    // 信息条
    let info = format!(
        "MAP {} [{}]  {}x{}  {}B/cell  CAM {},{}{}",
        m.title,
        map_i + 1,
        m.width,
        m.height,
        m.cell_len,
        cam.0,
        cam.1,
        if map_count == 0 {
            String::new()
        } else {
            format!(" /{map_count}")
        }
    );
    // **左上角叠加**（用户 2026-10-09：调试信息移到左上角、叠在游戏内容上）——
    // 不带底条，靠黑边压住底下的地图；两行：地图信息 / 世界摘要。
    let lh = names.line_height();
    names.draw(
        canvas,
        tc,
        &trunc(&info, 64),
        4.0,
        4.0,
        (C_TITLE.r, C_TITLE.g, C_TITLE.b),
        Some((0, 0, 0)),
    )?;
    // 右上角：层可见性（三层全开时不显示，免得占地方）+ 纹理缓存数
    let base = if layers == LAYERS_ALL {
        format!("TILES {}", tiles.len())
    } else {
        format!("LAYER {}   TILES {}", layers_desc(layers), tiles.len())
    };
    // 联网时把世界摘要接在后面：状态 / 视野实体数 / 世界变更次数 / 未识别消息数。
    // ⚠️ `CHG` 是"世界在动"最直接的观测量（联调时盯它涨没涨，比盯着画面猜靠谱）；
    // `UNK` 不该大于 0 —— 涨了就说明两边对不上（见 core::world）。
    let right = match net {
        Some(n) => format!(
            "NET {}  ENT {}  CHG {}  UNK {}   {}",
            trunc(&n.status, 40),
            n.world.entities.len(),
            n.changes,
            n.world.unknown,
            base
        ),
        None => base,
    };
    names.draw(
        canvas,
        tc,
        &trunc(&right, 64),
        4.0,
        4.0 + lh,
        (C_DIM.r, C_DIM.g, C_DIM.b),
        Some((0, 0, 0)),
    )?;

    // **底部操作面板**：最后贴（原版也是最后贴的），盖住世界下沿
    draw_hud(canvas, tc, ui, names, dir, net, &m.title)?;

    // 悬停结果还给主循环：光标要跟着它换（Crystal 的 Attack 光标）
    Ok(hover)
}

/// 素材浏览器（**开发用**，`F3` 进出）：任取一个容器里的第 N 张图，看它解码成什么样。
///
/// 它原先挤在登录页右侧 —— 那让"照原版的登录界面"没法做（一屏两件事）。
/// 现在独立成一屏：`[` `]` 换容器、`,` `.` 换图号。
#[allow(clippy::too_many_arguments)] // 都是渲染所需的最小上下文，与 draw_login_view 同理
fn draw_asset_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    asset_dir: &Option<PathBuf>,
    loaded: &mut Option<(usize, Wzl)>,
    sprite_tex: &mut Option<Texture<'a>>,
    status: &str,
    music_on: bool,
    lib_idx: usize,
    img_idx: &mut usize,
    started: Instant,
) -> Result<(), sdl3::Error> {
    let t1 = "ASSET  BROWSER   (F1 = LOGIN,  F2 = MAP)";
    text(canvas, t1, center_x(t1, 0.0, WIN_W as f32), 14.0, C_TITLE)?;
    let t2 = format!(
        "UPTIME {:.0}s    MUSIC {}    STATUS: {}",
        started.elapsed().as_secs_f32(),
        if music_on { "ON" } else { "OFF" },
        status
    );
    text(canvas, &trunc(&t2, 118), 12.0, 34.0, C_DIM)?;

    // 预览区（棋盘底 + 最近邻放大）与右侧信息
    const PX: f32 = 24.0;
    const PY: f32 = 60.0;
    const PW: f32 = 496.0;
    const PH: f32 = 420.0;
    const IX: f32 = PX + PW + 16.0;
    checkerboard(canvas, PX, PY, PW, PH)?;
    frame(canvas, PX, PY, PW, PH, C_PANEL_BORDER)?;

    let mut info_lines: Vec<(String, Color)> = vec![("NO ASSETS".to_string(), C_ERR)];
    let mut sprite_dims = (0u32, 0u32);
    let mut ready = false;

    if let Some(dir) = asset_dir {
        let name = LIBS[lib_idx];
        if loaded.as_ref().map(|(i, _)| *i) != Some(lib_idx) {
            *loaded = open_lib(dir, name).map(|l| (lib_idx, l));
            *sprite_tex = None;
        }
        if let Some((_, lib)) = loaded.as_ref() {
            let total = lib.len();
            if *img_idx >= total {
                *img_idx = 0;
            }
            // 空壳图很常见（本套素材里 146 个容器是 64 字节的空壳）⇒ 往后找 64 张
            let mut found = None;
            for k in 0..64 {
                let i = (*img_idx + k) % total.max(1);
                if let Some(sp) = lib.decode(i) {
                    found = Some((i, sp));
                    break;
                }
            }
            if let Some((i, sp)) = found {
                *img_idx = i;
                let is16 = lib.record(i).map(|r| r.is_16bit()).unwrap_or(false);
                sprite_dims = (sp.width as u32, sp.height as u32);
                info_lines = vec![
                    (format!("{name}  #{i} / {total}"), C_TEXT),
                    (
                        format!(
                            "{}x{}   ANCHOR({},{})",
                            sp.width, sp.height, sp.anchor_x, sp.anchor_y
                        ),
                        C_DIM,
                    ),
                    (
                        if is16 { "DIRECT 16BIT" } else { "PALETTE 8BIT" }.to_string(),
                        C_OK,
                    ),
                ];
                ready = true;

                let need_new = match sprite_tex.as_ref() {
                    Some(t) => {
                        let q = t.query();
                        q.width != sp.width as u32 || q.height != sp.height as u32
                    }
                    None => true,
                };
                if need_new {
                    *sprite_tex = None;
                }
                if sprite_tex.is_none() {
                    if let Ok(mut t) = tc.create_texture(
                        PixelFormat::RGBA32,
                        TextureAccess::Streaming,
                        sp.width as u32,
                        sp.height as u32,
                    ) {
                        t.set_blend_mode(BlendMode::Blend);
                        t.set_scale_mode(ScaleMode::Nearest);
                        *sprite_tex = Some(t);
                    }
                }
                if let Some(t) = sprite_tex.as_mut() {
                    if t.update(None::<Rect>, &sp.rgba, sp.width as usize * 4)
                        .is_err()
                    {
                        ready = false;
                    }
                }
            } else {
                info_lines = vec![(format!("{name}  #{}  (EMPTY)", img_idx), C_DIM)];
            }
        } else {
            info_lines = vec![(format!("{name}.wzl NOT FOUND"), C_ERR)];
        }
    }

    let px = PX + 24.0;
    let mut py = PY + 24.0;
    for (line, col) in &info_lines {
        text(canvas, &trunc(line, 56), px, py, *col)?;
        py += INFO_LINE_H;
    }
    py += 8.0;
    for line in [
        format!("LIB [{}/{}]  =  {}", lib_idx + 1, LIBS.len(), LIBS[lib_idx]),
        format!("IMG {}", *img_idx),
        "[ ] CHANGE LIB      , . CHANGE IMG".to_string(),
        "F1 LOGIN   F2 MAP   M MUSIC   ESC QUIT".to_string(),
    ] {
        text(canvas, &trunc(&line, 56), px, py, C_DIM)?;
        py += INFO_LINE_H;
    }

    // 精灵本体：等比放大画进预览区（最近邻，像素不糊）
    if ready {
        if let Some(t) = sprite_tex.as_ref() {
            let (sw, sh) = (sprite_dims.0 as f32, sprite_dims.1 as f32);
            let scale = (PW / sw).min(PH / sh).floor().clamp(1.0, 8.0);
            let (dw, dh) = (sw * scale, sh * scale);
            canvas.copy(
                t,
                None::<FRect>,
                FRect::new(PX + (PW - dw) / 2.0, PY + (PH - dh) / 2.0, dw, dh),
            )?;
        }
    } else {
        let msg = if asset_dir.is_none() {
            "ASSETS NOT FOUND - SET MIR2_ASSET_DIR"
        } else {
            "NO SPRITE"
        };
        text(canvas, msg, center_x(msg, PX, PW), PY + PH / 2.0, C_ERR)?;
    }
    let _ = IX;
    Ok(())
}

/// 把握手状态机的**待发命令排空**（`Entrance::next_cmd`）。
///
/// ⚠️ 语义就是"**每帧**调一次"：有的阶段转折不是被信封推动的 —— 最典型的是
/// `Ev::Connected`（握手 nonce 到了）之后要发 `LoginSaltRequest`。
/// 只在"收到信封"时拉，这一步就得等下一个入站包（心跳是 20 秒一次）。
///
/// 抽成独立函数是为了能单测这条契约（不需要真的网络）。
fn flush_entrance(
    entrance: &mut mir2_core::entrance::Entrance,
    send: &mut impl FnMut(&mir2_protocol::envelope::Body),
) {
    if entrance.failed().is_some() || entrance.in_world() {
        return;
    }
    // `next_cmd` 只在 `Stage::Start` 有货（之后就 `None`）⇒ 不会在这里打转。
    while let Some(b) = entrance.next_cmd() {
        send(&b);
    }
}

/// 状态机吐出来的信封 → 会话命令（**这张表就是"接线"本身**）。
///
/// ⚠️ 漏一项的后果不是报错，而是**静默吞掉**：界面照常转圈，而服务端一条请求都收不到。
/// 这个坑已经栽过两次：
///   - `LoginSaltRequest`/`Login` ⇒「点了登录一直转圈」（e2e 的 `TestProtoRustLogin` 抓到的）；
///   - `CreateAccount` ⇒「点了建号没反应」（2026-10-08 用户报的：服务端日志里只有
///     `握手完成`，`LoginSaltRequest`/`CreateAccount` 一条都没到）。
///
/// ⇒ 两道防线：① `Net::send` 对 `None` **大声打日志**（不再 `_ => None` 悄悄丢）；
/// ② 单测 `建号那条路的每条命令都有翻译` 把这条链钉住。
///
/// 抽成自由函数就是为了能单测：`Reconnect` 要用会话号，所以 `session` 从外面传进来。
fn to_cmd(body: &mir2_protocol::envelope::Body, session: i32) -> Option<mir2_net::Cmd> {
    use mir2_protocol::envelope::Body;
    Some(match body {
        Body::Reconnect(_) => mir2_net::Cmd::Reconnect(session),
        Body::LoginSaltRequest(r) => mir2_net::Cmd::LoginSaltRequest(r.account.clone()),
        Body::Login(l) => mir2_net::Cmd::Login {
            account: l.account.clone(),
            proof_hex: l.password_hash.clone(),
        },
        // 建号（D-32）：发的是口令的**校验值** `hex(K)`，不是证明 —— 建号时服务端
        // 手里什么都没有，得拿这个值落库才能验以后的登录（见 `account.proto` 的说明）。
        Body::CreateAccount(c) => mir2_net::Cmd::CreateAccount {
            account: c.account.clone(),
            verifier_hex: c.verifier.clone(),
        },
        Body::ListCharacters(_) => mir2_net::Cmd::ListCharacters,
        Body::SelectCharacter(s) => mir2_net::Cmd::SelectCharacter(s.character_id),
        // 建/删角（选角界面）：状态机吐的是**协议 body**，会话层再翻成 `Cmd`。
        Body::CreateCharacter(c) => mir2_net::Cmd::CreateCharacter {
            name: c.name.clone(),
            class: c.class,
            gender: c.gender,
            hair: c.hair,
        },
        Body::DeleteCharacter(d) => mir2_net::Cmd::DeleteCharacter {
            character_id: d.character_id,
            proof_hex: d.password_hash.clone(),
        },
        // 世界输入不走这里（`Cmd::Move`/`Attack` 由输入那条路直发）；服务端单向消息
        // （实体事件、心跳…）本来就不是命令 ⇒ 到这里是 `None`，由调用方打日志。
        _ => return None,
    })
}

/// **每帧"网络 → 画面"的决策**（纯函数；主循环只负责执行它）。
///
/// # 为什么一定要有这一层
///
/// 2026-10-08 用户问："这么显著的问题，你的测试用例是怎么通过的？" —— 答案就在这个函数
/// **以前不存在**：那两条判定原本写死在**主循环里**（`pump()` 里那两段），而主循环要 SDL
/// 窗口与素材才能跑 ⇒ **测试碰不到它**，于是"测试全绿 + 功能是坏的"能并存。两条判定
/// **各漏过一次**，症状都是"画面没跟着状态机走"：
///
///   ① 建/删角之后不重建：守卫写成 `mode != 4 && awaiting_pick` —— 而建角**恰恰发生在
///      已经在选角屏**的时候（`mode == 4`）⇒ 整条分支被跳过（“建完角色要重登才看得见”）。
///   ② 选角通过后不换屏：没人把 `mode` 切到 2 ⇒ 世界在跑（能听见受击/死亡声）、
///      画面却还停在选角界面。当时留下的 `Select::start_clicked` 是个**只写不读**的标记。
///
/// 搬到这个纯函数之后，**同样这两个错误会让 `换屏判定` 变红**（那条测试里写了怎么复现，
/// 而且实际改回去验过一遍：红 → 改回来 → 绿）。**判据全部来自状态机**，没有一处是"猜的时机"。
///
/// 参数就是主循环手里那几件事实：
/// - `awaiting_pick`：状态机停在"等你选角"（`Stage::AwaitPick`）；
/// - `in_world`：状态机说已经进世界了（`Entrance::in_world`）；
/// - `changed`：状态机手里的角色列表与界面上的场景**不一样**（`list_changed`）；
/// - `door_blocking`：开门动画还在放（原版顺序：门放完 → `ChangeScene(stSelectChr)`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct Plan {
    /// 按新的角色列表**重建**选角场景。
    rebuild_select: bool,
    /// 把画面切到选角。门还在放时**不切** —— 一上来就切会把门当场掐掉。
    enter_select: bool,
    /// 把画面切到**游戏主场景**（选角通过）。
    enter_play: bool,
}

fn plan(mode: u8, awaiting_pick: bool, in_world: bool, changed: bool, door_blocking: bool) -> Plan {
    Plan {
        // ⚠️ 这里**绝不能**再加"当前不在选角屏"这类条件（bug ① 就是加了个 `mode != 4`）：
        //    建/删角发生在选角屏**里面**，加上它 = 列表更新了却不重建。
        rebuild_select: awaiting_pick && changed,
        // 门放完之前不切（`opened_at.is_none()` = 压根没有门要走 ⇒ 立刻可切）。
        enter_select: awaiting_pick && !door_blocking,
        // ⚠️ 只从**选角屏**进游戏（门那屏由主循环里 `mode == 1` 那条分支自己接手）；
        //    缺了这条 = 只闻其声、不见其画面（bug ②）。
        enter_play: mode == 4 && in_world,
    }
}

/// 角色列表变了没（决定要不要重建选角场景，见调用处）。
///
/// 抽出来是为了能单测：这条判断错了，一边是**日志刷屏 + 选中位置被每帧抹掉**
///（门还在放的那三秒里 `mode` 还是 1，分支每帧命中），另一边是列表更新了却不重建
///（建完角色回到选角看不到新角色）。
fn list_changed(scene: Option<&select::Select>, chars: &[select::CharEntry]) -> bool {
    match scene {
        Some(s) => s.chars.as_slice() != chars,
        None => true,
    }
}

/// 选角场景做出的动作 → 真的去做。
///
/// ⚠️ 抽出来是因为**键盘与鼠标两条路**都会产生它：两处各写一遍迟早漂移
///（一边发了 `SelectCharacter`、另一边忘了）。返回 `true` = 该退出程序。
fn do_select_action(
    act: select::Action,
    net: &mut Option<Net>,
    select_scene: &mut Option<select::Select>,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
) -> Result<bool, sdl3::Error> {
    // 点下去的按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` = 103）。
    // 槽上那一下的**解冻声**（101）由场景自己排出来（见 `Select::take_sfx`）。
    if act != select::Action::None {
        sfx(sound, sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
    }
    match act {
        select::Action::None => Ok(false),
        select::Action::Exit => Ok(true),
        // 建角：状态机只在选角阶段（`AwaitPick`）才发得出去。发不出去就**明说一句**，
        // 不静默（这条纪律踩过：静默吞命令的表现就是"点了没反应"）。
        select::Action::Create {
            name,
            class,
            gender,
            hair,
        } => {
            let mut sent = false;
            if let Some(n) = net.as_mut() {
                if let Some(b) = n.entrance.create_character(&name, class, gender, hair) {
                    n.send(&b);
                    sent = true;
                    println!("[net] 建角：{name}（职业={class} 性别={gender} 发型={hair}）");
                }
            }
            if !sent {
                if let Some(s) = select_scene.as_mut() {
                    s.say("现在不能建角（连接/阶段不对）—— 重新登录后再试。");
                }
            }
            // 回执是**异步**的：成败都回来一次（失败弹窗、成功重拉列表，见
            // `Entrance::on` 的 `CreateCharacterResult` 那条）⇒ 这里不动界面。
            Ok(false)
        }
        // 删角：要**登录时那条口令证明**（`Entrance::delete_character` 自己带）。
        // 认领会话进来的没有证明 ⇒ 发不出去，明说一句怎么办。
        select::Action::Delete(id) => {
            let mut sent = false;
            if let Some(n) = net.as_mut() {
                if let Some(b) = n.entrance.delete_character(id) {
                    n.send(&b);
                    sent = true;
                    println!("[net] 删角：id={id}");
                }
            }
            if !sent {
                if let Some(s) = select_scene.as_mut() {
                    s.say(
                        "没发出去：删角要用登录时那条口令证明（认领会话进来的没有）—— \
                         用账号口令重新登一次再删。",
                    );
                }
            }
            Ok(false)
        }
        select::Action::Enter(id) => {
            if let Some(n) = net.as_mut() {
                match n.entrance.pick(id) {
                    Some(b) => {
                        n.send(&b);
                        println!("[net] 选角：进入角色 ActorId={id}");
                    }
                    // 状态机不在等选角（比如已经发过一次）⇒ 忽略，别静默发怪消息
                    None => println!("[net] 选角：状态机不在等选角，忽略这次选择"),
                }
            }
            // ⚠️ 这里**不**记"点过了"：换屏的判据是状态机的 `in_world()`（见 `show()`），
            // 不是"哪颗按钮被按过" —— 早先那个只写不读的 `start_clicked` 就是这么留下的。
            Ok(false)
        }
    }
}

/// 底部的按键提示。
///
/// ⚠️ 抽成函数是为了**能测**：提示条必须跟着 [`DEBUG_LAYERS`] / [`DEBUG_OVERLAY`] 走
/// —— 关掉的功能还写在提示里，用户就会去按、然后按了没反应（那是另一种 bug 报告）。
fn hint_text(mode: u8) -> &'static str {
    match mode {
        2 => {
            // ⚠️ 提示条按**原版键位**写（`docs/use.md`）：Tab 小地图、M 大地图；
            // 开发查看器入口一律 `CTRL+`（F1~F8 是技能、F9~F12 是窗口，别抢）。
            const PLAY: &str = "LMB WALK  RMB RUN  TAB MINIMAP  M BIGMAP  SPACE HIT  \
                                C CONNECT  [ ] MAP  CTRL+M MUSIC  CTRL+F1 LOGIN  ESC";
            const DEBUG_KEYS: &str = "LMB WALK  RMB RUN  TAB MINIMAP  M BIGMAP  SPACE HIT  \
                                      C CONNECT  [ ] MAP  D DEBUG  CTRL+1/2/3 LAYER  \
                                      CTRL+M MUSIC  CTRL+F1 LOGIN  ESC";
            if DEBUG_LAYERS || DEBUG_OVERLAY {
                DEBUG_KEYS
            } else {
                PLAY
            }
        }
        1 => "TAB NEXT FIELD   ENTER LOGIN   F2 MAP   F3 ASSETS   M MUSIC   ESC QUIT",
        4 => "LEFT/RIGHT PICK   ENTER START   F1 LOGIN   F2 MAP   ESC QUIT",
        _ => "F3 ASSETS   [ ] LIB   , . IMG   F1 LOGIN   F2 MAP   M MUSIC   ESC QUIT",
    }
}

/// 走路动画帧号 → 这一步该不该响、响的是不是**第二只脚**。
///
/// 原版：走路动画的**帧 1** 响一声、**帧 4** 再响一声（`Actor.pas:2659-2660`），
/// 两声差 1 号（`_l` / `_r`）。`last` 是上一帧的帧号 —— 同一个帧号只响一次
/// （帧率比 `ftime` 快时，帧号会连续几帧不变）。
///
/// 抽成函数是为了能单测这条"边沿判定"（循环里测不到）。
fn footstep_of(frame: u16, last: Option<u16>) -> Option<bool> {
    if last == Some(frame) {
        return None;
    }
    match frame {
        1 => Some(false),
        4 => Some(true),
        _ => None,
    }
}

/// 按原版编号播一条音效：**没有资产 / 没有这一条 ⇒ 静默跳过**（不报错、不崩）。
///
/// ⚠️ 静默是**有意的**：原版 `PlaySound` 也是先 `FileExists` 再放
/// （`SoundUtil.pas:183-186`），缺素材是常态（清单里就有 13 条编号在源里没文件）。
fn sfx(sound: &audio::Audio, assets: &Option<mir2_core::sound::SoundAssets>, number: u16) {
    if let Some(a) = assets {
        sound.play_idx(a, number);
    }
}

/// 切场景 BGM（循环）。资产里没有 ⇒ 静默（同上）。
///
/// 真的换上了一首就打一行 —— 听不见的时候，"有没有音乐"总得有个可观测的东西。
fn bgm(sound: &audio::Audio, assets: &Option<mir2_core::sound::SoundAssets>, name: &str) {
    if let Some(a) = assets {
        if sound.bgm_name(a, name) {
            println!("[audio] BGM = {name}");
        }
    }
}

/// 把连接层的原因翻成"人话 + 下一步该查什么"。
///
/// ⚠️ `Connection refused` 与"口令错"是**两回事**：前者是 TCP 层没人监听
/// （服务端没起、或者起的时候没带 `-proto-addr`），根本还没走到鉴权。
/// 这一条就是为这个区分写的 —— 别让人对着"连不上"去怀疑密码。
fn connect_hint(why: &str) -> String {
    let w = why.to_ascii_lowercase();
    if w.contains("connection refused") || w.contains("os error 61") {
        return format!(
            "{why}\n\n(服务端没在监听：gamesvr 要带 -proto-addr 127.0.0.1:7500 才开新协议入口)"
        );
    }
    if w.contains("timed out") || w.contains("timeout") {
        return format!("{why}\n\n(超时：地址/防火墙？服务端卡住了？)");
    }
    why.to_string()
}

/// 提交登录。
///
/// ⚠️ 这里只做**客户端侧**的准备（必填校验、置忙、记日志）：真正的认证要走新协议的
/// `Login`，而它的口令形态是 [D-24](../../../docs/decisions.md) 在管的事 ——
/// 在定下来之前不假装成功（`docs/decisions.md` 原文：**也不把 `password_hash` 当成
/// "收到了就用"**）。
fn submit_login(login: &mut login::Login, net: &mut Option<Net>, status: &mut String) {
    if login.account.is_empty() {
        login.error = Some("Please enter your account name.".into());
        return;
    }
    if login.password.is_empty() {
        login.error = Some("Please enter your password.".into());
        return;
    }
    let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
    match Net::connect_with_password(&addr, &login.account, &login.password) {
        Ok(n) => {
            login.busy = true;
            login.error = None;
            *status = format!("LOGIN {}", login.account);
            println!(
                "[login] 提交：账号={:?} → 口令挑战应答（D-24①）",
                login.account
            );
            *net = Some(n);
        }
        Err(e) => login.error = Some(e),
    }
}

/// 提交建号（D-32）。
///
/// 与 `submit_login` 的差别只有两点：走 `connect_for_signup`，以及提示语措辞 ——
/// 建完**不自动登录**（原版也只是弹个提示，要用户自己再登一次）。
fn submit_signup(login: &mut login::Login, net: &mut Option<Net>, status: &mut String) {
    if login.account.trim().is_empty() {
        login.error = Some("Please enter your account name.".into());
        return;
    }
    if login.password.is_empty() {
        login.error = Some("Please enter your password.".into());
        return;
    }
    let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
    match Net::connect_for_signup(&addr, login.account.trim(), &login.password) {
        Ok(n) => {
            login.busy = true;
            login.error = None;
            *status = format!("SIGN UP {}", login.account.trim());
            println!("[login] 提交建号：账号={:?}", login.account.trim());
            *net = Some(n);
        }
        Err(e) => login.error = Some(e),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mir2_core::actor as A;
    use mir2_core::world::Entity;

    fn ent(kind: u32, f: mir2_protocol::EntityFeature) -> Entity {
        Entity {
            id: 7,
            kind,
            name: "甲".into(),
            x: 3,
            y: 4,
            dir: 5, // 协议方向 5 = 下 ⇒ 原版 4
            feature: Some(f),
            hp: 10,
            max_hp: 20,
            run: false,
            status_bits: 0,
            dead: false,
            action: None,
        }
    }

    /// 「待发命令要**每帧**排空，不能只在收到信封时排」——
    /// 这曾经是个真 bug：`Ev::Connected` 之后要发的 `LoginSaltRequest` 被塞在
    /// 信封 arm 里拉，于是得等 20 秒后的心跳应答才出去（输完账号等 20 秒才开门）。
    #[test]
    fn 待发命令不依赖入站包() {
        use mir2_protocol::envelope::Body;
        let mut e =
            mir2_core::entrance::Entrance::new_with_password("test".into(), "pw".into(), None);

        // 第一帧（还没收到任何信封）就该把"要盐"的请求发出去
        let mut sent = Vec::new();
        flush_entrance(&mut e, &mut |b| sent.push(b.clone()));
        assert_eq!(sent.len(), 1, "第一帧就该发 LoginSaltRequest");
        match &sent[0] {
            Body::LoginSaltRequest(r) => assert_eq!(r.account, "test"),
            other => panic!("第一条应当是 LoginSaltRequest，实得 {other:?}"),
        }

        // 阶段已前进到 AwaitSalt ⇒ 再排也不能重复发
        let mut again = Vec::new();
        flush_entrance(&mut e, &mut |b| again.push(b.clone()));
        assert!(again.is_empty(), "同一阶段不该重复发命令");
    }

    /// 调试功能关掉之后，提示条**不能**还写着那些键。
    ///
    /// 这条盯的是"关掉了但界面还在教人按"这种半拉子状态：翻开关时容易忘了
    /// 同步提示条，而症状是"按了没反应"（用户会当成 bug 来报）。
    #[test]
    fn 提示条跟着调试开关走() {
        let h = hint_text(2);
        assert!(
            h.contains("WALK") && h.contains("CONNECT"),
            "正常玩法提示要还在：{h}"
        );
        if DEBUG_LAYERS || DEBUG_OVERLAY {
            // 开着的时候要**写着**（否则等于藏了一个没人知道的调试入口）
            if DEBUG_OVERLAY {
                assert!(h.contains("D DEBUG"), "{h}");
            }
        } else {
            assert!(!h.contains("1/2/3"), "图层键已关，提示里不该还有：{h}");
            assert!(!h.contains("D DEBUG"), "叠加层已关，提示里不该还有：{h}");
        }
    }

    /// 脚步的边沿判定：**帧 1 / 帧 4 各一次，同一帧号只响一次**。
    ///
    /// 原版就是这么对齐的（`Actor.pas:2659-2660`）；帧 4 那一声是第二只脚。
    #[test]
    fn 脚步只在帧1帧4响() {
        assert_eq!(footstep_of(1, Some(0)), Some(false), "帧 1 ⇒ 第一只脚");
        assert_eq!(footstep_of(1, Some(1)), None, "同一个帧号只响一次");
        assert_eq!(footstep_of(2, Some(1)), None);
        assert_eq!(footstep_of(3, Some(2)), None);
        assert_eq!(footstep_of(4, Some(3)), Some(true), "帧 4 ⇒ 第二只脚");
        assert_eq!(footstep_of(4, Some(4)), None);
        assert_eq!(footstep_of(0, None), None, "站立/起手不响");
    }

    /// 连不上时要给出"下一步查什么"，而且**不能**把人往"密码错"上引。
    #[test]
    fn 连接失败的提示() {
        let h = connect_hint("连接 127.0.0.1:7500 失败：IO: Connection refused (os error 61)");
        assert!(h.contains("-proto-addr"), "该提示去查新协议入口：{h}");
        assert!(h.contains("Connection refused"), "原始原因要留着");
        let t = connect_hint("read tcp: i/o timeout");
        assert!(t.contains("超时"));
        assert_eq!(connect_hint("被服务端断开 105"), "被服务端断开 105");
    }

    /// 玩家的本体：容器是 `Hum`，图号 = `600*Dress + 站立段 + 方向步长`。
    #[test]
    fn 玩家本体走_hum() {
        let f = mir2_protocol::EntityFeature {
            dress: 10,
            ..Default::default()
        };
        let (lib, idx) = body_sprite(&ent(0, f), None, Instant::now()).expect("玩家该有精灵");
        assert_eq!(lib, A::HUM_LIB);
        assert_eq!(idx, A::human_index(10, A::HAct::Stand, 4, 0));
        assert_eq!(idx, 600 * 10 + 4 * 8);
    }

    /// 武器层只有**手上有东西**时才画（`weapon == 0` 是空手）。
    #[test]
    fn 武器层_空手不画() {
        let bare = mir2_protocol::EntityFeature {
            dress: 1,
            ..Default::default()
        };
        assert!(weapon_sprite(&ent(0, bare), None, Instant::now()).is_none());
        let armed = mir2_protocol::EntityFeature {
            dress: 1,
            weapon: 21,
            ..Default::default()
        };
        let (lib, idx) = weapon_sprite(&ent(0, armed), None, Instant::now()).unwrap();
        assert_eq!(
            (lib, idx),
            (A::WEAPON_LIB, A::human_index(21, A::HAct::Stand, 4, 0))
        );
    }

    /// 怪物：容器由图里的 `Appr` 定、动作表由 `RaceImg` 定。
    #[test]
    fn 怪物走_appr() {
        let f = mir2_protocol::EntityFeature {
            race_img: 19,
            appr: 151,
            ..Default::default()
        };
        let (lib, idx) = body_sprite(&ent(1, f), None, Instant::now()).unwrap();
        assert_eq!(lib, A::mon_container(151).unwrap());
        assert_eq!(idx, A::monster_index(151, 19, A::MAct::Stand, 4, 0));
        assert!(weapon_sprite(&ent(1, f), None, Instant::now()).is_none());
    }

    /// NPC 没有精灵（`Npc.wzl` 缺失）⇒ 退回标记，而不是画个错的东西。
    #[test]
    fn npc_退回标记() {
        assert!(body_sprite(&ent(2, Default::default()), None, Instant::now()).is_none());
    }

    /// 没有外观信息（旧服务端 / 快照还没到）⇒ 退回标记。
    #[test]
    fn 缺外观信息退回标记() {
        let mut e = ent(0, Default::default());
        e.feature = None;
        assert!(body_sprite(&e, None, Instant::now()).is_none());
    }

    /// 补间：刚移动时画在旧格与新格之间，过了时长就到位（且不再播走路）。
    #[test]
    fn 移动补间() {
        let now = Instant::now();
        let a = ActorAnim {
            cell: (5, 5),
            from: Some((4, 5)),
            action: None,
            changed_at: now,
            action_at: now,
            move_ms: move_ms(1, 0, false), // 走一格 = 600 ms（见 `move_ms`）
            walk_since: now,
        };
        let (x, y) = a.draw_pos((5, 5), now);
        assert!(
            (x - 4.0).abs() < 0.01 && (y - 5.0).abs() < 0.01,
            "刚开始还该在来处"
        );
        // 半路在半格附近（600ms 的一半 ⇒ 第 4.5 格）
        let (hx, _) = a.draw_pos((5, 5), now + Duration::from_millis(300));
        assert!((hx - 4.5).abs() < 0.01, "300ms 该走到第 4.5 格，实得 {hx}");
        let later = now + Duration::from_millis(a.move_ms as u64 + 10);
        assert_eq!(
            a.draw_pos((5, 5), later),
            (5.0, 5.0),
            "过了补间时长就该到位"
        );
        assert!(!a.moving(later));
    }

    /// 补间时长**跟着服务端的移动节流走**（`entity.MoveLimiter`：走 600 / 跑 400 一步）。
    ///
    /// ⚠️ 这条是用户 2026-10-08 报的"一瘸一拐"的根源：原先写死 320 ms，
    /// 每格都提前到位再干等 280 ms。
    #[test]
    fn 补间时长跟服务端节流() {
        assert_eq!(move_ms(1, 0, false), WALK_STEP_MS, "走一格 = 600 ms");
        assert_eq!(move_ms(0, -1, false), WALK_STEP_MS, "四个方向一样");
        assert_eq!(
            move_ms(1, 1, false),
            WALK_STEP_MS,
            "斜着走也是一格（切比雪夫）"
        );
        assert_eq!(move_ms(2, 0, true), RUN_STEP_MS, "跑一步 2 格 = 400 ms");
        assert_eq!(move_ms(0, 2, true), RUN_STEP_MS);
        assert_eq!(move_ms(2, 2, true), RUN_STEP_MS, "斜着跑也是两格");
        // 每格：走 600 ms、跑 200 ms ⇒ 跑确实快三倍（原版 GetNextRunXY 一步 2 格）
        assert_eq!(RUN_STEP_MS / RUN_STEPS as u32, 200);
        assert!(RUN_STEP_MS / RUN_STEPS as u32 * 2 < WALK_STEP_MS * 2);
        // 距离算不出来（原地转身）也不能是 0 ⇒ 会除零
        assert!(move_ms(0, 0, false) > 0);
    }

    /// 走路动画的相位**不随每格重置**（原版就是这么推的：`Actor.pas:3230-3263`）。
    #[test]
    fn 走路相位不随每格重置() {
        let t0 = Instant::now();
        let t1 = t0 + Duration::from_millis(600);
        // 上一格还没走到位（连贯地走）⇒ 接着推：相位起点不动
        assert_eq!(next_walk_since(true, t0, t1), t0, "连着走时相位要接着推");
        // 上一格已经走完（停过一下/刚开始走）⇒ 从头开始
        assert_eq!(next_walk_since(false, t0, t1), t1, "停下来再走要从头");
    }

    /// 走/跑动画按**移动相位**推帧：6 帧要能播满（而不是每格从第 0 帧重来）。
    ///
    /// `ActWalk` 6 帧 × 90 ms、`ActRun` 6 帧 × 120 ms（`Actor.pas:77-78`）。
    #[test]
    fn 走跑动画按相位推帧() {
        // 走：相位 0 → 0 帧、90 → 1 帧、540 → 又回到 0（一轮 = 6×90）
        assert_eq!(
            human_sample(None, 0, true, false, 0, false),
            (A::HAct::Walk, 0)
        );
        assert_eq!(
            human_sample(None, 0, true, false, 90, false),
            (A::HAct::Walk, 1)
        );
        assert_eq!(
            human_sample(None, 0, true, false, 450, false),
            (A::HAct::Walk, 5)
        );
        assert_eq!(
            human_sample(None, 0, true, false, 540, false),
            (A::HAct::Walk, 0)
        );
        // 跑：同一套相位走在**另一段图**上（120 ms 一帧）
        assert_eq!(
            human_sample(None, 0, true, true, 0, false),
            (A::HAct::Run, 0)
        );
        assert_eq!(
            human_sample(None, 0, true, true, 480, false),
            (A::HAct::Run, 4)
        );
        // 站着：相位无关（stand 的帧按自己的 200 ms 走）
        assert_eq!(
            human_sample(None, 0, false, true, 450, false).0,
            A::HAct::Stand
        );
    }

    /// 动作播完回站立 —— 否则实体会永远停在那一刀的末帧。
    #[test]
    fn 动作播完回站立() {
        assert_eq!(
            human_sample(Some(1), 0, false, false, 0, false).0,
            A::HAct::Hit
        );
        // **尾巴**内（510+80=590ms 之前）：仍停在挥砍的最后一帧 —— 两刀之间不留缝
        // （节拍 560ms，动画只有 510ms；不留尾巴就会闪 3 帧站立，看着像"砍一下停一下"）
        assert_eq!(
            human_sample(Some(1), 520, false, false, 0, false),
            (A::HAct::Hit, A::HAct::Hit.act().last_frame()),
            "尾巴内该停在最后一帧"
        );

        // ActHit 是 6 帧 × 85ms = 510ms ⇒ 过了尾巴（600ms）应回到站立
        assert_eq!(
            human_sample(Some(1), 600, false, false, 0, false),
            (A::HAct::Stand, 0)
        );
    }

    /// 手上的动作播完后，**走路**优先于站立（在走就别站着）。
    #[test]
    fn 动作播完且在走就播走路() {
        assert_eq!(
            human_sample(Some(1), 600, true, false, 0, false).0,
            A::HAct::Walk
        );
        // 跑也一样优先于站立，只是换成 ActRun
        assert_eq!(
            human_sample(Some(1), 600, true, true, 0, false).0,
            A::HAct::Run
        );
    }

    /// **建号那条路的每一步都必须有翻译** —— 用户报的"点了建号没反应"根因就是
    /// 状态机吐了 `CreateAccount`，而 `send()` 用 `_ => None` 把它静默吞了
    ///（服务端日志里只有 `握手完成`，`CreateAccount` 一条都没到）。
    #[test]
    fn 建号那条路的每条命令都有翻译() {
        use mir2_core::entrance::{Entrance, Stage};
        use mir2_protocol::envelope::Body;
        let mut e = Entrance::new_for_signup("algo".into(), "pw123".into());

        // ① 取盐
        let cmd = e.next_cmd().expect("第一步是取盐");
        assert!(matches!(cmd, Body::LoginSaltRequest(_)));
        assert_eq!(*e.stage(), Stage::AwaitSignupSalt);
        assert!(to_cmd(&cmd, 0).is_some(), "取盐没有翻译 ⇒ 会被静默吞掉");

        // ② 回盐 ⇒ 该吐 `CreateAccount`（**就是漏掉的那一条**）
        let cmd = e
            .on(&mir2_protocol::Envelope {
                body: Some(Body::LoginSalt(mir2_protocol::LoginSalt {
                    salt: vec![9; 16],
                    iterations: 1000,
                    key_len: 32,
                })),
                ..Default::default()
            })
            .expect("回盐之后该吐 CreateAccount");
        assert!(matches!(cmd, Body::CreateAccount(_)));
        assert_eq!(*e.stage(), Stage::AwaitSignup);
        assert!(to_cmd(&cmd, 0).is_some(), "建号没有翻译 ⇒ 点了没反应");

        // ③ 回执 ⇒ 留一条提示给界面（弹窗 + 切回登录面板）
        let _ = e.on(&mir2_protocol::Envelope {
            body: Some(Body::CreateAccountResult(
                mir2_protocol::CreateAccountResult {
                    result: Some(mir2_protocol::ActionResult {
                        ok: true,
                        code: 0,
                        message: "账号已建立，请登录".into(),
                    }),
                },
            )),
            ..Default::default()
        });
        assert_eq!(
            e.take_signup_msg(),
            Some((true, "账号已建立，请登录".into()))
        );
    }

    /// 翻译表**没有缺项**：状态机能吐出来的每一条命令都得在里面
    ///（上面那条钉"建号这条路"，这条钉"表本身"）。
    #[test]
    fn 状态机命令的翻译表没有缺项() {
        use mir2_protocol::envelope::Body;
        let cases: Vec<Body> = vec![
            Body::Reconnect(mir2_protocol::Reconnect {
                session_token: 7i32.to_le_bytes().to_vec(),
                last_ack_seq: 0,
            }),
            Body::LoginSaltRequest(mir2_protocol::LoginSaltRequest {
                account: "algo".into(),
            }),
            Body::Login(mir2_protocol::Login {
                account: "algo".into(),
                password_hash: "ab".repeat(32),
                client_build: String::new(),
            }),
            Body::CreateAccount(mir2_protocol::CreateAccount {
                account: "algo".into(),
                verifier: "cd".repeat(32),
            }),
            Body::ListCharacters(mir2_protocol::ListCharacters {}),
            Body::SelectCharacter(mir2_protocol::SelectCharacter { character_id: 42 }),
            Body::CreateCharacter(mir2_protocol::CreateCharacter {
                name: "新角色".into(),
                class: 1,
                gender: 1,
                hair: 3,
            }),
            Body::DeleteCharacter(mir2_protocol::DeleteCharacter {
                character_id: 42,
                password_hash: "ef".repeat(32),
            }),
        ];
        for b in &cases {
            assert!(to_cmd(b, 7).is_some(), "没有翻译：{b:?}");
        }
        // 反向：不是命令的单向消息**不该**被翻译（否则会把服务端的话原样发回去）
        assert!(to_cmd(
            &Body::ServerError(mir2_protocol::ServerError {
                code: 1,
                message: "x".into(),
            }),
            7
        )
        .is_none());
    }

    /// 角色列表**没变**就不重建场景 —— 用户报的日志刷屏（门还在放的那三秒里每帧重建 +
    /// 每帧 `println`）与"选中位置被每帧抹回第 0 个"根因都是这条判断。
    #[test]
    fn 列表没变就不重建选角场景() {
        let a = vec![select::CharEntry {
            id: 1,
            name: "甲".into(),
            level: 1,
            class: 1,
            sex: 0,
        }];
        let scene = select::Select::new(a.clone());
        assert!(!list_changed(Some(&scene), &a), "同一个列表不该重建");
        assert!(list_changed(None, &a), "还没有场景时必须建");
        let mut b = a.clone();
        b.push(select::CharEntry {
            id: 2,
            name: "乙".into(),
            level: 2,
            class: 2,
            sex: 1,
        });
        assert!(list_changed(Some(&scene), &b), "列表变了要重建");
    }

    /// 换屏判定 —— **这两条各漏过一次**（见 `plan` 的说明），所以逐条钉住。
    ///
    /// 这条测试是**真会红**的那种（已实际验过：把 bug 改回去 ⇒ 红；改回来 ⇒ 绿）：
    ///   · `rebuild_select` 改回 `awaiting_pick && changed && mode != 4` ⇒ 第①行红（bug ①）；
    ///   · `enter_play` 写死 `false`（或删掉）⇒ 第②行红（bug ②）。
    #[test]
    fn 换屏判定() {
        // ① **已经在选角屏**（mode 4）+ 列表变了 ⇒ 必须重建（建/删角到手的新列表）
        let p = plan(4, true, false, true, false);
        assert!(p.rebuild_select, "在选角屏里也必须按新列表重建");
        assert!(p.enter_select, "画面还得停在选角上");
        // ② 选角通过（在选角屏 + 状态机说进世界）⇒ 切游戏主场景
        let p = plan(4, false, true, false, false);
        assert!(p.enter_play, "选角通过必须换到游戏主场景");
        assert!(!p.rebuild_select && !p.enter_select);
        // ③ 门还在放 ⇒ 不切走（切了会把开门动画掐掉），但列表可以先备好
        let p = plan(1, true, false, true, true);
        assert!(!p.enter_select, "门没放完不该切走");
        assert!(p.rebuild_select, "但列表可以先建好");
        // ④ 已经在游戏里 ⇒ 什么都不动（别把玩家踢回选角）
        let p = plan(2, false, true, false, false);
        assert!(!p.rebuild_select && !p.enter_select && !p.enter_play);
        // ⑤ 列表没变 ⇒ 不重建（否则每帧重建：选中位置被抹掉、日志还会刷屏）
        assert!(!plan(4, true, false, false, false).rebuild_select);
        // ⑥ 门那屏（mode 1）收到 EnterWorld 也不该切游戏：门放完由 `mode == 1` 那条分支接手
        assert!(!plan(1, false, true, false, false).enter_play);
    }

    /// 鼠标走路的方向：8 个方位 + "同一格没有方向"（照原版 `GetNextDirection`，
    /// `ClFunc.pas:398-422`）。协议值 = 原版 + 1（1 上、2 右上 … 8 左上，顺时针）。
    #[test]
    fn 鼠标方位给方向() {
        use mir2_protocol::Direction as D;
        let at = (10, 10);
        assert_eq!(dir_to(at, (10, 5)), Some(D::DirUp), "正上方");
        assert_eq!(dir_to(at, (15, 5)), Some(D::DirUpRight));
        assert_eq!(dir_to(at, (15, 10)), Some(D::DirRight));
        assert_eq!(dir_to(at, (15, 15)), Some(D::DirDownRight));
        assert_eq!(dir_to(at, (10, 15)), Some(D::DirDown));
        assert_eq!(dir_to(at, (5, 15)), Some(D::DirDownLeft));
        assert_eq!(dir_to(at, (5, 10)), Some(D::DirLeft));
        assert_eq!(dir_to(at, (5, 5)), Some(D::DirUpLeft));
        assert_eq!(
            dir_to(at, at),
            None,
            "同一格没有方向 ⇒ 调用方据此判\"到了\""
        );
        // 远距离也只看方位（不是只看相邻格）
        assert_eq!(dir_to(at, (99, 10)), Some(D::DirRight));
    }

    /// 鼠标连续走路：**距离 < 2 时不许跑**（照原版 `ClMain.pas:1950-1978`）。
    ///
    /// 少了这条，跑步（一次跨 2 格）遇到**奇数距离**的目标就会跨过去再跨回来 ——
    /// 用户 2026-10-08 报的"奔跑位置左右乱换"。距离 < 2 时改成走一步，正好落到目标格。
    #[test]
    fn 鼠标走路距离近了不许跑() {
        use mir2_protocol::Direction as D;
        let at = (10, 10);
        // 已经站在目标格 ⇒ 没有下一步（调用方收工）
        assert_eq!(next_move_step(at, at, true), None);
        assert_eq!(next_move_step(at, at, false), None);

        // 相邻（距离 1，含斜向）⇒ **走**，哪怕想要跑（跑会跨过去）
        assert_eq!(
            next_move_step(at, (11, 10), true),
            Some((D::DirRight, false))
        );
        assert_eq!(
            next_move_step(at, (11, 11), true),
            Some((D::DirDownRight, false))
        );
        assert_eq!(next_move_step(at, (10, 10), false), None);

        // 距离 ≥ 2（切比雪夫：`ClFunc.pas:352` 的 `MAX(abs(dx),abs(dy))`）⇒ 想跑就真跑
        assert_eq!(
            next_move_step(at, (12, 10), true),
            Some((D::DirRight, true))
        );
        assert_eq!(next_move_step(at, (10, 13), true), Some((D::DirDown, true)));
        // 斜向 (2,2)：切比雪夫距离是 2 ⇒ 也算"够远"（跑一次正好 2 格斜向）
        assert_eq!(
            next_move_step(at, (12, 12), true),
            Some((D::DirDownRight, true))
        );
        // 不想跑就永远走（左键）
        assert_eq!(
            next_move_step(at, (99, 10), false),
            Some((D::DirRight, false))
        );
    }

    /// 小地图的换算：**X 是 1.5 倍、Y 是 1 倍**（原版 `PlayScn.pas:808-813`）——
    /// 这个不对称是原版就有的，写成 `*1.5/*1.5` 会让点位系统性偏左。
    #[test]
    fn 小地图换算x是一倍半y是一倍() {
        assert_eq!(minimap_point(0.0, 0.0), (0.0, 0.0));
        assert_eq!(minimap_point(2.0, 2.0), (3.0, 2.0)); // 2*48/32 = 3
        assert_eq!(minimap_point(289.0, 618.0), (433.5, 618.0)); // 边界村
        assert_eq!(minimap_point(650.0, 631.0), (975.0, 631.0)); // 银杏山谷
                                                                 // **小数格**也要能用：走路补间落在两格之间（图上才能滑，而不是一格一格跳）
        assert_eq!(minimap_point(10.5, 20.25), (15.75, 20.25));
    }

    /// 小地图的裁剪框：以自己为中心 `120` 见方，**贴边时夹回图内**（原版也是这么夹的）。
    #[test]
    fn 小地图裁剪贴边要夹住() {
        let img = (1000u32, 800u32);
        assert_eq!(
            minimap_crop(500.0, 400.0, img, 120.0),
            (440.0, 340.0, 120.0, 120.0),
            "居中时是 120 见方"
        );
        assert_eq!(
            minimap_crop(10.0, 400.0, img, 120.0),
            (0.0, 340.0, 120.0, 120.0),
            "贴左边界"
        );
        assert_eq!(
            minimap_crop(500.0, 5.0, img, 120.0),
            (440.0, 0.0, 120.0, 120.0),
            "贴上边界"
        );
        assert_eq!(
            minimap_crop(999.0, 799.0, img, 120.0),
            (880.0, 680.0, 120.0, 120.0),
            "贴右下角"
        );
        // 图比窗口还小 ⇒ 给整张图（w/h 跟着缩），不会出现负数或越界
        assert_eq!(
            minimap_crop(5.0, 5.0, (50, 40), 120.0),
            (0.0, 0.0, 50.0, 40.0)
        );
        // 补间中的小数位置 ⇒ 裁剪框**连续**（"图滑得平滑"就是这儿来的）
        assert_eq!(
            minimap_crop(500.5, 400.0, img, 120.0),
            (440.5, 340.0, 120.0, 120.0)
        );
    }

    /// 大地图：**人物永远在窗口正中**，图比窗口大 ⇒ 跑动时图会滑（用户 2026-10-08 要的手感）。
    #[test]
    fn 大地图人物永远在正中() {
        let img = (540u32, 360u32);
        let win = (800u32, 600u32);
        let (px, py) = minimap_point(100.0, 200.0);
        let (dx, dy, dw, dh) = bigmap_dst(px, py, img, win, BIGMAP_ZOOM);
        // 人物在贴图上的位置 == 窗口中心（`bigmap_dst` 的全部意义）
        assert_eq!(
            (dx + px * BIGMAP_ZOOM, dy + py * BIGMAP_ZOOM),
            (400.0, 300.0)
        );
        // 放大 2 倍 ⇒ 图（1080×720）比窗口（800×600）大 ⇒ 有得滑
        assert_eq!((dw, dh), (1080.0, 720.0));
        assert!(
            dw > win.0 as f32 && dh > win.1 as f32,
            "图必须比窗口大，否则滑不起来"
        );
        // 走一格 ⇒ 图整体反向平移，而且是**连续**的（不是跳一格）
        let (dx2, _, _, _) = bigmap_dst(px + 1.5, py, img, win, BIGMAP_ZOOM);
        assert_eq!(
            dx2,
            dx - 3.0,
            "X 走一格（1.5 缩略图像素）⇒ 图反向滑 3 像素（×2）"
        );
        // ⚠️ 故意**不夹**：图外就是空的，但人物必须还在正中（用户的原话）
        let (dx3, _, _, _) = bigmap_dst(0.0, 0.0, img, win, BIGMAP_ZOOM);
        assert_eq!(dx3, 400.0, "到了地图左上角，贴图也照样把人物摆在正中");
    }

    /// 走路时地图取**补间位置**：没动画状态 ⇒ 服务端那一格；走起来 ⇒ 落在两格之间。
    ///
    /// 这条钉的就是用户 2026-10-08 报的手感问题：图跟着**动作**走，不是"到位才跳一格"。
    #[test]
    fn 走动时地图取的是补间位置() {
        let now = Instant::now();
        // 还没有动画状态（刚进图）⇒ 原样的格子
        assert_eq!(self_render_pos(None, (10, 20), now), (10.0, 20.0));

        // 刚迈出一步：(10,20) → (11,20)
        let anim = ActorAnim {
            cell: (11, 20),
            from: Some((10, 20)),
            action: None,
            changed_at: now,
            action_at: now,
            move_ms: move_ms(1, 0, false),
            walk_since: now,
        };
        // 起点那一刻
        assert_eq!(self_render_pos(Some(&anim), (11, 20), now), (10.0, 20.0));
        // 半路（补间中）—— 这一条就是"平滑"的证据
        let mid = now + Duration::from_millis(anim.move_ms as u64 / 2);
        let (mx, _) = self_render_pos(Some(&anim), (11, 20), mid);
        assert!(
            (10.4..=10.6).contains(&mx),
            "半路该在第 10.5 格附近，实得 {mx}"
        );
        // 过了补间时长 ⇒ 到位
        let done = now + Duration::from_millis(anim.move_ms as u64 + 10);
        assert_eq!(self_render_pos(Some(&anim), (11, 20), done), (11.0, 20.0));
    }

    /// `MIR2_WINDOW` 的解析：比设计尺寸小 / 格式不对 ⇒ `None`（退回默认）。
    ///
    /// ⚠️ 设计尺寸 2026-10-09 起是 **1024×768**（之前 800×600 是被放大到 1024 的那档）⇒
    /// `800x600` 现在**不认**了：那会把 1024 的版式缩小（HUD 面板都放不下）。
    #[test]
    fn 窗口尺寸参数解析() {
        for (s, want) in [
            ("1024x768", Some((1024, 768))),
            ("1280x960", Some((1280, 960))),
            ("1600x1200", Some((1600, 1200))),
            // 大小写、空格都容错
            (" 1280 X 960 ", Some((1280, 960))),
            // 比设计尺寸（1024×768）小 ⇒ 不认
            ("800x600", None),
            ("1024x700", None),
            ("640x480", None),
            ("1024x500", None),
            // 格式不对
            ("1280*960", None),
            ("1280", None),
            ("abcxdef", None),
            ("", None),
        ] {
            assert_eq!(parse_window(s), want, "「{s}」解析不对");
        }
    }

    /// **登录/选角背景是 800×600**（真素材）—— 它们在 1024×768 的窗口里**居中**摆。
    ///
    /// 2026-10-09 之前设计空间就是 800×600，这条断言写作"素材 == 设计尺寸"；
    /// 现在设计空间改成 1024×768（原生渲染、不放大），这条只钉素材尺寸。
    #[test]
    fn 登录与选角背景正好是设计尺寸() {
        let Ok(dir) = std::env::var("MIR2_ASSET_DIR").or_else(|_| std::env::var("MIR2C_DATA"))
        else {
            eprintln!("跳过：未设置 MIR2_ASSET_DIR / MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(dir);
        let mut ui = ui::UiCache::new();
        for (what, lib, idx) in [
            (
                "登录背景",
                mir2_core::login_ui::Art::BG.0,
                mir2_core::login_ui::Art::BG.1,
            ),
            (
                "选角背景",
                mir2_core::select_ui::Art::BG.0,
                mir2_core::select_ui::Art::BG.1,
            ),
        ] {
            // ⚠️ 登录/选角那套素材是 **800×600**，而窗口/设计空间是 1024×768 ⇒
            // 它们是**居中摆**、不缩放（原版也是这么干的：`(SCREENWIDTH-800) div 2`）。
            // 这条钉住"素材尺寸没变" —— 变了各自的 `Layout::build` 会自动跟着居中。
            assert_eq!(
                ui.size(&dir, lib, idx),
                Some((800, 600)),
                "{what}（{lib}[{idx}]）该是 800×600（居中摆在 {WIN_W}×{WIN_H} 里）"
            );
        }
    }

    // 鼠标坐标换算**交给 SDL**（事件循环头的 `Event::get_converted_coords`，它同时管
    // 逻辑呈现的缩放与留边）⇒ 这边没有可单测的纯函数了。
    //
    // ⚠️ 别再手写 `x / 某常数`：那个写法假设窗口是 4:3，窗口一改（现在可拉大拉小）
    // 就会"点哪走哪偏一截"。

    /// **走动/跑动时画面边缘不能露黑底**（用户 2026-10-08 报的"边缘一整片半格黑底，
    /// 像是走动时叠瓦没盖全"）。
    ///
    /// 病根：视口剔除拿的是**没减亚格偏移**的框，而图块实际画在 `-sub` 处 ⇒
    /// 视口右/下边缘那一溜图块被判成"在视口外"剔掉 ⇒ 露黑底（相机是整格时 `sub = 0`，
    /// 所以只在**走动中**出现 —— 必须拿小数相机测）。
    ///
    /// 判据是"盖满"：真地图 + 真图库，扫视口里的像素，要求每一处都有**保留下来的
    /// 地表图块**压着。用的还是剔除那条路（`draw_rect_cold`），所以它一退化就红。
    #[test]
    fn 走动时画面边缘不露黑底() {
        let (Some(dir), Some(pack)) = (
            mir2_core::paths::asset_dir(),
            mir2_core::paths::map_container(),
        ) else {
            eprintln!("跳过：没有资产目录 / 地图容器");
            return;
        };
        let Ok(a) = Archive::open(&pack) else {
            eprintln!("跳过：地图容器打不开");
            return;
        };
        // 优先用 "0"（新手村，地表是满的）；没有就用第一张
        let name = if a.lookup("0").is_some() {
            "0".to_string()
        } else {
            match a.entries().first() {
                Some(e) => e.name.clone(),
                None => return,
            }
        };
        let Ok(m) = Map::load(&a, &name) else {
            eprintln!("跳过：地图 {name} 打不开");
            return;
        };

        let cols = WIN_W as i32 / UNIT_X + 3;
        let rows = VIEW_H as i32 / UNIT_Y + 3;
        let view = viewport_rect();
        // 相机放在地图中央（保证视口整块都在图内 —— 图外的黑是应该的，不算漏画）
        let cx = m.width as i32 / 2 - WIN_W as i32 / UNIT_X / 2;
        let cy = m.height as i32 / 2 - VIEW_H as i32 / UNIT_Y / 2;
        let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
        let mut draws: Vec<TileDraw> = Vec::new();

        for frac in [0.0f32, 0.25, 0.5, 0.75] {
            let cam = (cx as f32 + frac, cy as f32 + frac);
            let cp = cam_parts(cam);
            m.visible_tiles(cp.cell.0, cp.cell.1, cols, rows, 0, &mut draws);
            // "保留下来的地表图块" —— 走的就是绘制时那条剔除（同一个 `sub`）
            // ⚠️ 用的是**绘制时那条判据本身**（`tile_in_view`），不是自己重算一遍 ——
            // 自己重算就等于"只测了 `intersects`"，调用点漏减一次 `sub` 照样绿。
            let mut kept: Vec<FRect> = Vec::new();
            for d in draws.iter().filter(|d| d.layer == Layer::Ground) {
                if !tile_in_view(&mut libs, &dir, d, &cp, LAYERS_ALL, &view) {
                    continue;
                }
                if let Some(r) = draw_rect_cold(&mut libs, &dir, d, cp.sub) {
                    kept.push(r);
                }
            }
            assert!(!kept.is_empty(), "相机 {cam:?} 一块地表都没留下？");
            // 4px 网格扫视口：够密（黑带至少几十像素宽），又不至于慢
            let mut px = 0.5;
            while px < WIN_W as f32 {
                let mut py = BAR_TOP + 0.5;
                while py < BAR_TOP + VIEW_H {
                    assert!(
                        kept.iter()
                            .any(|r| r.x <= px && px < r.x + r.w && r.y <= py && py < r.y + r.h),
                        "相机 {cam:?}（亚格偏移 {:.0}px）：视口像素 ({px},{py}) 没有地表图块盖着 \
                         —— 走动时这里就是一条黑底",
                        cp.sub.0
                    );
                    py += 4.0;
                }
                px += 4.0;
            }
        }
    }

    /// 屏幕坐标 ↔ 格子互为逆（"点哪走到哪"靠这一对；鼠标那条路用的是反算）。
    #[test]
    fn 屏幕与格子互为逆() {
        let cam = (100.0, 200.0);
        for (cx, cy) in [(100, 200), (103, 205), (99, 199), (140, 260)] {
            let (px, py) = cell_to_screen(cam, cx, cy);
            // 取格内一点（+1px）再反算，避开格边界
            assert_eq!(
                screen_to_cell(cam, px + 1.0, py + 1.0),
                (cx, cy),
                "({cx},{cy}) 往返失败"
            );
        }
        // 相机带小数时也一样（走路时相机就是小数格）
        let camf = (100.25, 200.75);
        let (px, py) = cell_to_screen(camf, 103, 205);
        assert_eq!(screen_to_cell(camf, px + 1.0, py + 1.0), (103, 205));
    }

    /// **走路/跑动时人物钉在屏幕中间不动、地图往前卷**（原版 `PlayScn.pas:1084-1089`：
    /// `m_ClientRect.Left := g_MySelf.m_nRx - 9`，`m_nRx` 是**渲染**坐标）。
    ///
    /// 这条是用户 2026-10-08 报的那个手感的判据：相机若取"服务端那一格"，
    /// 人就变成"在视口里一格格蹭、蹭满一格镜头再跳一下"。
    #[test]
    fn 走路时人物钉在屏幕中间地图往前卷() {
        let now = Instant::now();
        let step = |anim: &ActorAnim, ms: u64| {
            let render = anim.draw_pos((11, 10), now + Duration::from_millis(ms));
            (render, follow_cam(render))
        };
        // 一跳：格 (10,10) → (11,10)，整跳 600ms
        let a = ActorAnim {
            cell: (11, 10),
            from: Some((10, 10)),
            action: None,
            changed_at: now,
            action_at: now,
            move_ms: 600,
            walk_since: now,
        };
        let mut cams = Vec::new();
        for ms in [0u64, 150, 300, 450, 599] {
            let (render, cam) = step(&a, ms);
            let (px, py) = cell_to_screen_f(cam, render.0, render.1);
            assert!(
                (px - WIN_W as f32 / 2.0).abs() < 0.01
                    && (py - VIEW_H / 2.0 - BAR_TOP).abs() < 0.01,
                "{ms}ms 时人物不在视口正中：({px},{py})"
            );
            cams.push(cam.0);
        }
        // 相机确实在往前走（而且**每帧走一点**，不是攒够一整格才跳）
        assert!(
            cams[0] < cams[1] && cams[1] < cams[2],
            "相机该平滑前进：{cams:?}"
        );
        let per_step = cams[1] - cams[0];
        assert!(
            per_step > 0.0 && per_step < 0.5,
            "150ms 只该走 0.25 格（一格 600ms），实得 {per_step}"
        );
    }

    /// 相机拆成"整格 + 亚格像素"：地图那条路只认整格，亚格靠绘制时减回来。
    #[test]
    fn 相机拆成整格加亚格() {
        let cp = cam_parts((10.0, 20.0));
        assert_eq!(cp.cell, (10, 20));
        assert_eq!(cp.sub, (0.0, 0.0));
        // 0.25 格 ⇒ 12px（UNIT_X = 48）；0.5 格 ⇒ 16px（UNIT_Y = 32）
        let cp = cam_parts((10.25, 20.5));
        assert_eq!(cp.cell, (10, 20));
        assert_eq!(cp.sub, (12.0, 16.0));
        // 负数相机（地图边缘会露出来一格）也要往下取整 —— 否则整格与亚格对不上
        let cp = cam_parts((-0.25, -0.5));
        assert_eq!(cp.cell, (-1, -1));
        assert_eq!(cp.sub, (36.0, 16.0));
    }

    /// **出手不能比服务端允许的更快**（用户 2026-10-09 报的"砍几下就停"）。
    ///
    /// 服务端按 `520ms − 攻速×25ms` 限流（`netgate.go:63/112`），比它快的攻击**被丢掉**、
    /// 而且**不发 `EntityAction`** ⇒ 我们自己的挥砍动画断一拍（动画完全来自服务端回包）。
    /// 旧值取的是动作表时长 510ms ⇒ 每一刀丢一刀。
    #[test]
    fn 出手不比服务端快() {
        let gap = attack_gap().as_millis() as u64;
        assert!(
            gap >= HIT_BASE_MS,
            "出手间隔 {gap}ms 比服务端基数 {HIT_BASE_MS}ms 还快 ⇒ 会被丢掉、动画断拍"
        );
        // 也要够长到让挥砍动画播完（不然下一刀会把动画从头拽）
        assert!(
            gap >= u64::from(mir2_core::actor::HAct::Hit.act().duration_ms()),
            "出手间隔比挥砍动画还短"
        );
    }

    /// **死了要一直画尸骨**（用户 2026-10-09 报的"死后没有显示尸体状态"）。
    ///
    /// 病根：`Die` 是"一次播完"的动作，播完（`held_ms >= duration`）原来的代码会退回
    /// **站立** ⇒ 画面上"死而复生"。现在 `dead = true` 一律钉在 `Die` 的最后一帧。
    #[test]
    fn 死了停在尸骨那帧() {
        use mir2_core::actor as A;
        // 任选一个有死亡段的品种（10 = 鸡/鹿那一族）
        let race = 10u8;
        let die = A::mon_actions(race)[A::MAct::Die as usize];
        assert!(die.frame > 0, "这条用例要求该品种有死亡段");
        let (act, frame) = monster_sample(
            race,
            Some(mir2_core::world::action::DEATH),
            10_000_000,
            false,
            true,
        );
        assert_eq!(act, A::MAct::Die, "死了该是 Die");
        assert_eq!(frame, die.last_frame(), "死了要停在**最后一帧**（尸骨）");
        // 不管过了多久都不许动（原来就是这里退回站立的）
        for ms in [0u32, die.duration_ms(), 10_000_000] {
            let (act, frame) =
                monster_sample(race, Some(mir2_core::world::action::DEATH), ms, false, true);
            assert_eq!(
                (act, frame),
                (A::MAct::Die, die.last_frame()),
                "{ms}ms 时不是尸骨"
            );
        }
        // 玩家那条路同理
        let (act, frame) = human_sample(None, 0, false, false, 0, true);
        assert_eq!(act, A::HAct::Die);
        assert_eq!(frame, A::HAct::Die.act().last_frame());

        // 还在播的过程中（`dead = false`：刚收到动作、`Death` 包还没到）照旧按死亡动作播；
        // ⚠️ 别把"播完回站立"这条也一起钉死 —— 那是攻击/受击该有的行为（只有 `dead` 才钉住）
        let (act, frame) =
            monster_sample(race, Some(mir2_core::world::action::DEATH), 0, false, false);
        assert_eq!(act, A::MAct::Die, "收到死亡动作就按它播");
        assert_eq!(frame, 0, "刚开始播是第一帧");
    }

    /// **移动不能让挥砍动作重播**（用户 2026-10-09："杀了怪，一跑起来又在砍"）。
    ///
    /// 病根是一个时钟干两件事：动作进度与移动补间共用 `changed_at` ⇒ 每走一格就把
    /// 动作进度清零 ⇒ 那个早该过期的 `Some(1)`（`world.self_action` 一直留着）又播一遍。
    #[test]
    fn 移动不重播挥砍() {
        let now = Instant::now();
        // 只看"手上的动作"取到什么姿势：`Some(1)` = 挥砍
        let hit = |anim: &ActorAnim, at: Instant| {
            let (held, ms) = (anim.action, anim.action_ms(at));
            human_sample(held, ms, anim.moving(at), false, anim.walk_ms(at), false).0
        };
        // t0 收到挥砍（动作钟 t0），t0+700ms 走了一步（**移动钟**被重置）
        let mut a = ActorAnim {
            cell: (3, 4),
            from: None,
            action: Some(1),
            changed_at: now,
            action_at: now,
            move_ms: 600,
            walk_since: now,
        };
        assert_eq!(hit(&a, now), A::HAct::Hit, "刚砍：是挥砍");
        a.from = Some((2, 4));
        a.changed_at = now + Duration::from_millis(700); // 移动**只**动移动钟
        a.move_ms = 600;
        assert_eq!(
            hit(&a, now + Duration::from_millis(750)),
            A::HAct::Walk,
            "挥砍早过期了 ⇒ 走动时该是走路，不能又砍一刀"
        );
        // 而**新来**一个动作（服务端再发一次挥砍）当然要正常播
        a.action_at = now + Duration::from_millis(750);
        assert_eq!(hit(&a, now + Duration::from_millis(760)), A::HAct::Hit);
    }

    /// 球的"液面"裁切：**看得见的永远是下面那一截**（原版 `FState.pas:3784-3795`）。
    ///
    /// 判据是三条边界：满血 = 整张图不动；空 = 什么都不画；一半 = 下面一半。
    #[test]
    fn 球的液面裁切() {
        assert_eq!(gauge_band(1.0, 90), (0, 90), "满：整张图");
        assert_eq!(gauge_band(0.0, 90), (90, 0), "空：什么都不画");
        assert_eq!(gauge_band(0.5, 90), (45, 45), "一半：下面一半");
        assert_eq!(gauge_band(0.25, 90), (68, 22), "四分之一：下面四分之一");
        // 越界要夹住（服务端给的 HP 可能一时大于 MaxHP）
        assert_eq!(gauge_band(1.5, 90), (0, 90));
        assert_eq!(gauge_band(-1.0, 90), (90, 0));
        // 可视高度 + 液面 = 球高（画的两个矩形必须正好拼上）
        for pct in [0.0, 0.1, 0.33, 0.5, 0.97, 1.0] {
            let (top, h) = gauge_band(pct, 90);
            assert_eq!(top + h, 90, "pct={pct} 时拼不上");
        }
    }

    /// 出手的两重门：**手上这一步没走完不能打**（原版 `IsIdle`），
    /// 以及出手冷却（原版 `CanNextHit`）。
    #[test]
    fn 走完这一步才出手() {
        let gap = attack_gap();
        assert!(can_attack(false, gap), "站着 + 冷却到点 ⇒ 能打");
        assert!(
            !can_attack(true, gap * 2),
            "还在走这一步 ⇒ 不许出手（否则挥砍会在半路上播）"
        );
        assert!(!can_attack(false, gap / 2), "冷却没到 ⇒ 不许出手");
    }

    /// 自己这一步的补间**盖满**自己的发送步频 —— 否则每格末尾会空出几十毫秒
    /// （走路动画闪回站立帧、镜头停一下），用户报的"走路没做好"里就有它。
    ///
    /// 别人用服务端的节流（`move_ms`，比自己的短）—— 它们的下一条由服务端驱动。
    #[test]
    fn 自己的补间盖满发送步频() {
        assert_eq!(
            self_move_ms(1, 0, false),
            WALK_MS as u32,
            "走一格 = 一个步频"
        );
        assert_eq!(self_move_ms(0, -1, false), WALK_MS as u32);
        assert_eq!(
            self_move_ms(2, 0, true),
            RUN_MS as u32,
            "跑一步 2 格 = 一个步频"
        );
        assert_eq!(self_move_ms(2, 2, true), RUN_MS as u32, "斜着跑也是 2 格");
        assert_eq!(
            self_move_ms(1, 0, true),
            (RUN_MS / 2) as u32,
            "被挡成 1 格的跑：按格数摊"
        );
        assert!(
            move_ms(1, 0, false) < self_move_ms(1, 0, false),
            "别人的补间该比自己短（600 < 650）"
        );
    }

    /// 鼠标点一格算什么：**活怪 ⇒ 锁它**；空地/死怪/玩家/NPC ⇒ 走/跑到那格。
    ///
    /// 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）与 `AttackTarget`（`:2691`）。
    #[test]
    fn 点鼠标算什么() {
        use mir2_core::world::{World, KIND_MONSTER};
        let mut w = World::default();
        w.self_id = 1;
        w.self_pos = (10, 10);
        let mut put = |id: u64, kind: u32, x: i32, y: i32, dead: bool| {
            w.entities.insert(
                id,
                Entity {
                    id,
                    kind,
                    name: "甲".into(),
                    x,
                    y,
                    dir: 1,
                    feature: None,
                    hp: 10,
                    max_hp: 10,
                    run: false,
                    status_bits: 0,
                    dead,
                    action: None,
                },
            );
        };
        put(100, KIND_MONSTER, 12, 10, false);
        put(200, 0, 13, 10, false); // 玩家（要 Shift，那条线还没做）
        put(300, KIND_MONSTER, 14, 10, true); // 死怪（尸骨）

        // 点活怪 ⇒ 锁它，不去走
        assert_eq!(mouse_intent(&w, (12, 10), false), (Some(100), None));
        // 点空地 ⇒ 走那一格（右键 = 跑）
        assert_eq!(
            mouse_intent(&w, (11, 10), false),
            (None, Some((11, 10, false)))
        );
        assert_eq!(
            mouse_intent(&w, (11, 10), true),
            (None, Some((11, 10, true)))
        );
        // 玩家 / 死怪 / 自己那格 ⇒ 走
        assert_eq!(mouse_intent(&w, (13, 10), false).0, None);
        assert_eq!(mouse_intent(&w, (14, 10), false).0, None);
        assert_eq!(
            mouse_intent(&w, (10, 10), false),
            (None, Some((10, 10, false)))
        );
    }

    /// **按住左键点怪**：光标滑开了也**不许把锁住的怪弄丢**（用户 2026-10-08 报的
    /// "点怪之后站不住、掉头去走路"）。
    ///
    /// 病根是镜头跟着人走（`follow_cam`）—— 跑向怪的路上，光标（屏幕位置不动）
    /// 对应的格子会从怪身上滑开，而按住时每 300ms 重取目标、照原版会先 `g_TargetCret := nil`
    /// ⇒ 目标半路被丢。
    #[test]
    fn 按住不丢已锁的怪() {
        use mir2_core::world::{World, KIND_MONSTER};
        let mut w = World::default();
        w.self_id = 1;
        w.self_pos = (10, 10);
        let put = |w: &mut World, id: u64, kind: u32, x: i32, y: i32, dead: bool| {
            w.entities.insert(
                id,
                Entity {
                    id,
                    kind,
                    name: "甲".into(),
                    x,
                    y,
                    dir: 1,
                    feature: None,
                    hp: 10,
                    max_hp: 10,
                    run: false,
                    status_bits: 0,
                    dead,
                    action: None,
                },
            );
        };
        put(&mut w, 100, KIND_MONSTER, 12, 10, false);

        // 新鲜按下：光标在怪身上 ⇒ 锁它
        assert_eq!(mouse_intent(&w, (12, 10), false), (Some(100), None));
        // 按住重取：光标已经滑到**空地** ⇒ **仍然锁着它**（不是掉头去走）
        assert_eq!(
            mouse_repeat(&w, (15, 10), false, Some(100)),
            (Some(100), None),
            "按住时不该因为光标离开怪而丢目标"
        );
        // 光标滑到**另一只**怪身上 ⇒ 换目标
        put(&mut w, 200, KIND_MONSTER, 11, 10, false);
        assert_eq!(
            mouse_repeat(&w, (11, 10), false, Some(100)),
            (Some(200), None)
        );
        // 锁的那个死了 ⇒ 回到"走去光标那格"（光标在空地）
        w.entities.get_mut(&100).unwrap().dead = true;
        assert_eq!(
            mouse_repeat(&w, (15, 10), false, Some(100)),
            (None, Some((15, 10, false)))
        );
        // 没锁东西时与新鲜按下同一条路
        assert_eq!(
            mouse_repeat(&w, (15, 10), false, None),
            (None, Some((15, 10, false)))
        );
    }

    /// 登录/选角那两屏：设计空间 800×600 铺到 1024×768 画布上应当是**整数关系**的
    /// 正好铺满（800×1.28 = 1024、600×1.28 = 768）—— 这是"拉伸"那条政策的地基。
    #[test]
    fn 设计空间拉伸正好铺满画布() {
        assert_eq!(ui::UI_SCALE, 1024.0 / 800.0);
        assert_eq!(800.0 * ui::UI_SCALE, WIN_W as f32);
        assert_eq!(600.0 * ui::UI_SCALE, WIN_H as f32);
        // 换算互为逆（鼠标那条路：画布 → 设计）
        for p in [(0.0, 0.0), (252.0, 173.0), (800.0, 600.0)] {
            let back = ui::ui_inv_pt(ui::ui_pt(p));
            assert!((back.0 - p.0).abs() < 0.01 && (back.1 - p.1).abs() < 0.01);
        }
        // 版式里那两个字面量：登录框在设计空间居中 ⇒ 画布上也居中
        let (cx, cy) = ui::ui_pt((252.0, 173.0));
        assert!(((WIN_W as f32 - 296.0 * ui::UI_SCALE) / 2.0 - cx).abs() < 0.01);
        assert!(((WIN_H as f32 - 254.0 * ui::UI_SCALE) / 2.0 - cy).abs() < 0.01);
    }

    /// 窗口尺寸夹进屏幕可用区域（1024×768 的屏 + 标题栏 ⇒ 客户区只有 1024×743 那种）。
    #[test]
    fn 窗口夹进可用区域() {
        assert_eq!(fit_window((1024, 768), (1920, 1080)), (1024, 768));
        assert_eq!(fit_window((1024, 768), (1024, 743)), (1024, 743));
        assert_eq!(fit_window((1280, 960), (1280, 800)), (1280, 800));
    }

    /// 呈现模式：**装得下就等比缩放，装不下就 1:1 裁切**（绝不缩小 —— 用户第 4 条）。
    #[test]
    fn 呈现模式不缩小() {
        let lb = sdl3_sys::render::SDL_LOGICAL_PRESENTATION_LETTERBOX;
        let dis = sdl3_sys::render::SDL_LOGICAL_PRESENTATION_DISABLED;
        assert_eq!(present_mode((1024, 768)), lb, "正好装得下 ⇒ 等比");
        assert_eq!(present_mode((1920, 1200)), lb, "更大 ⇒ 等比放大");
        assert_eq!(present_mode((1024, 743)), dis, "矮一点 ⇒ 1:1 裁切（不缩）");
        assert_eq!(present_mode((900, 768)), dis, "窄一点 ⇒ 1:1 裁切（不缩）");
    }
}

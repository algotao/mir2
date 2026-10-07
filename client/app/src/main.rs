//! MIR2 1.76 客户端 —— 开发期查看器（两个模式）
//!
//! * **登录界面**：窗口 / 文本输入 / 2D 渲染 / 程序化音乐（M0 的 SDL3 落地验证）
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
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use mir2_core::m2pk::Archive;
use mir2_core::map::{Layer, Lib, Map, TileDraw, LAYERS_ALL, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream};
use sdl3::event::Event;
use sdl3::keyboard::{Keycode, Mod};
use sdl3::mouse::MouseButton;

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

const WIN_W: u32 = 1024;
const WIN_H: u32 = 768;
const SAMPLE_RATE: i32 = 44_100;

/// 地图视图的顶部信息条高度。
const BAR_TOP: f32 = 24.0;
/// 底部提示条高度。
const BAR_BOTTOM: f32 = 22.0;
/// 地图可视区高度。
const VIEW_H: f32 = WIN_H as f32 - BAR_TOP - BAR_BOTTOM;

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
const C_ACTIVE: Color = Color::RGB(255, 236, 140);
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
/// 精灵纹理缓存上限。与图块缓存同理：越界就整个清掉，不做 LRU ——
/// 地图比视口大得多，走到哪解到哪，记账成本换不来什么。
const SPRITE_CACHE_CAP: usize = 512;

/// 走一格的补间时长。
///
/// ⚠️ 这是**客户端定的观感参数**：新协议的 `EntityMove` 只给 `from`/`to`，没有时长
/// （原版靠移动速度算节拍，那个还没下发）。320ms 与"人走两步"大致同量级。
const MOVE_MS: u32 = 320;

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

// ---------- 程序化音乐 ----------
const TEMPO_SEC: f32 = 0.34;
/// (MIDI 音高, 拍数)；音高 0 表示休止
const MELODY: &[(u8, f32)] = &[
    (72, 1.0),
    (74, 1.0),
    (76, 1.0),
    (79, 1.0),
    (76, 1.0),
    (74, 1.0),
    (72, 2.0),
    (69, 1.0),
    (72, 1.0),
    (76, 1.0),
    (74, 2.0),
    (72, 1.0),
    (69, 1.0),
    (67, 2.0),
    (0, 1.0),
];

struct Music {
    sr: f32,
    phase: f32,
    note: usize,
    elapsed: f32,
    muted: Arc<AtomicBool>,
}

impl Music {
    fn new(sr: f32, muted: Arc<AtomicBool>) -> Self {
        Self {
            sr,
            phase: 0.0,
            note: 0,
            elapsed: 0.0,
            muted,
        }
    }

    fn freq(midi: u8) -> f32 {
        if midi == 0 {
            0.0
        } else {
            440.0 * 2f32.powf((midi as f32 - 69.0) / 12.0)
        }
    }
}

impl AudioCallback<f32> for Music {
    fn callback(&mut self, stream: &mut AudioStream, requested: i32) {
        let n = requested.max(0) as usize;
        let mut out = Vec::with_capacity(n);
        let silent = self.muted.load(Ordering::Relaxed);

        for _ in 0..n {
            let (midi, beats) = MELODY[self.note];
            let dur = (beats * TEMPO_SEC).max(0.05);
            let t = self.elapsed / dur;
            let env = if t < 0.03 {
                t / 0.03
            } else {
                (1.0 - (t - 0.03) / 0.97).clamp(0.0, 1.0)
            };

            let f = Self::freq(midi);
            let s = if f > 0.0 && !silent {
                let sq = if (self.phase % 1.0) < 0.5 { 1.0 } else { -1.0 };
                sq * env * 0.10
            } else {
                0.0
            };
            out.push(s);

            if f > 0.0 {
                self.phase += f / self.sr;
                if self.phase >= 1.0 {
                    self.phase -= 1.0;
                }
            }
            self.elapsed += 1.0 / self.sr;
            if self.elapsed >= dur {
                self.elapsed = 0.0;
                self.note = (self.note + 1) % MELODY.len();
            }
        }

        let _ = stream.put_data_f32(&out);
    }
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
        .or_insert_with(|| Wzl::open(dir.join(&name)).ok())
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
fn draw_tile<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    cache: &mut HashMap<TileKey, TileTex<'a>>,
    dir: &Path,
    d: &TileDraw,
    origin_y: f32,
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
                left as f32,
                origin_y + top as f32,
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
    cam: (i32, i32),
    mouse: (f32, f32),
    layers: u8,
) -> Result<(), sdl3::Error> {
    // 1) 格网（48×32）
    canvas.set_draw_color(C_GRID);
    let mut gx = 0.0;
    while gx < WIN_W as f32 {
        canvas.draw_line(FPoint::new(gx, BAR_TOP), FPoint::new(gx, BAR_TOP + VIEW_H))?;
        gx += UNIT_X as f32;
    }
    let mut gy = BAR_TOP;
    while gy < BAR_TOP + VIEW_H {
        canvas.draw_line(FPoint::new(0.0, gy), FPoint::new(WIN_W as f32, gy))?;
        gy += UNIT_Y as f32;
    }

    // 2) 各层落点框（与图块同步显隐：关掉的层不留框，免得误判还剩东西）
    for d in draws {
        if layers & d.layer.bit() == 0 {
            continue;
        }
        let Some(r) = rect_of(d, tiles) else { continue };
        canvas.set_draw_color(match d.layer {
            Layer::Ground => C_GRID_GROUND,
            Layer::Mid => C_GRID_MID,
            Layer::Front => C_GRID_FRONT,
        });
        canvas.draw_rect(r)?;
        if d.layer == Layer::Front {
            let by = BAR_TOP + d.y as f32 + UNIT_Y as f32;
            canvas.set_draw_color(C_CELLBASE);
            canvas.draw_line(
                FPoint::new(d.x as f32, by),
                FPoint::new(d.x as f32 + UNIT_X as f32, by),
            )?;
        }
    }

    // 3) 鼠标十字线 + 所在格 + 读数
    let (mx, my) = mouse;
    if (BAR_TOP..BAR_TOP + VIEW_H).contains(&my) {
        let cx = cam.0 + (mx / UNIT_X as f32).floor() as i32;
        let cy = cam.1 + ((my - BAR_TOP) / UNIT_Y as f32).floor() as i32;
        let hx = (cx - cam.0) as f32 * UNIT_X as f32;
        let hy = BAR_TOP + (cy - cam.1) as f32 * UNIT_Y as f32;
        canvas.set_draw_color(C_CROSS);
        canvas.draw_rect(FRect::new(hx, hy, UNIT_X as f32, UNIT_Y as f32))?;
        canvas.draw_line(FPoint::new(mx, BAR_TOP), FPoint::new(mx, BAR_TOP + VIEW_H))?;
        canvas.draw_line(FPoint::new(0.0, my), FPoint::new(WIN_W as f32, my))?;

        // 该像素最上层的那一条（绘制顺序里最后命中的；隐藏层不参与）
        let topmost = draws.iter().rev().find(|d| {
            layers & d.layer.bit() != 0
                && rect_of(d, tiles)
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
                let cell_x = cam.0 + d.x / UNIT_X;
                let cell_y = cam.1 + d.y / UNIT_Y;
                let bx = (cell_x - cam.0) as f32 * UNIT_X as f32;
                let by = BAR_TOP + (cell_y - cam.1) as f32 * UNIT_Y as f32;
                // 高亮：它自己的格（亮白）+ 整张图外框（亮白）+ 它的格底线（亮黄）
                canvas.set_draw_color(C_TOPMOST);
                canvas.draw_rect(FRect::new(bx, by, UNIT_X as f32, UNIT_Y as f32))?;
                canvas.draw_rect(FRect::new(
                    left as f32,
                    BAR_TOP + top as f32,
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
) -> Option<FRect> {
    let name = d.lib.file_name(d.area);
    let lib = libs
        .entry(name.clone())
        .or_insert_with(|| Wzl::open(dir.join(&name)).ok())
        .as_ref()?;
    let rec = lib.record(d.index as usize)?;
    if rec.width == 0 || rec.height == 0 {
        return None;
    }
    let top = d.top_y(rec.width as i32, rec.height as i32, rec.anchor_y as i32);
    Some(FRect::new(
        d.left_x(rec.anchor_x as i32) as f32,
        BAR_TOP + top as f32,
        rec.width as f32,
        rec.height as f32,
    ))
}

// ---------- 调试工具（D 叠加层 / P 打印清单 / 左键点哪读哪）----------

/// 一条绘制指令的屏幕矩形（**已计入 `top_y`**，即图块真正落下的位置）。
fn rect_of(d: &TileDraw, tiles: &HashMap<TileKey, TileTex<'_>>) -> Option<FRect> {
    let t = tiles.get(&(d.lib, d.area, d.index, d.blend))?;
    let q = t.tex.query();
    let top = d.top_y(q.width as i32, q.height as i32, t.anchor_y as i32);
    let left = d.left_x(t.anchor_x as i32);
    Some(FRect::new(
        left as f32,
        BAR_TOP + top as f32,
        q.width as f32,
        q.height as f32,
    ))
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
    cam: (i32, i32),
    draws: &[TileDraw],
    tiles: &HashMap<TileKey, TileTex<'_>>,
    layers: u8,
) {
    let cx = cam.0 + (px / UNIT_X as f32).floor() as i32;
    let cy = cam.1 + ((py - BAR_TOP) / UNIT_Y as f32).floor() as i32;
    println!(
        "\n[probe] 视口像素=({px:.0},{py:.0}) → 格=({cx},{cy})   可见层 {}",
        layers_desc(layers)
    );
    let mut hits = 0;
    for (i, d) in draws.iter().enumerate() {
        if layers & d.layer.bit() == 0 {
            continue;
        }
        let Some(r) = rect_of(d, tiles) else { continue };
        if (r.x..r.x + r.w).contains(&px) && (r.y..r.y + r.h).contains(&py) {
            hits += 1;
            println!(
                "  [{i:3}] {:<6?} 格({:4},{:4}) 图号={:5} 尺寸={:.0}x{:.0} 落点=({:.0},{:.0})..({:.0},{:.0}) 库={}",
                d.layer,
                cam.0 + d.x / UNIT_X,
                cam.1 + d.y / UNIT_Y,
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
            anims: HashMap::new(),
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
            anims: HashMap::new(),
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
                    // ⚠️ nonce 是**这条连接一次**的握手随机值，登录时要把口令证明绑在它上面
                    //（D-24①）⇒ 必须立刻交给握手状态机，晚了就发不出证明。
                    self.entrance.on_nonce(&nonce);
                    self.status =
                        format!("已连接（协议 {version}，能力 {}）", capabilities.join(","));
                    println!("[net] {}", self.status);
                }
                mir2_net::Ev::Closed(why) => {
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
        use mir2_protocol::envelope::Body;
        let cmd = match body {
            Body::Reconnect(_) => Some(mir2_net::Cmd::Reconnect(self.session)),
            // ⚠️ 口令登录那两步也必须在这里翻译 —— 少了它状态机吐出来的
            // `LoginSaltRequest`/`Login` 会被 `_ => None` **静默吞掉**，
            // 表现是"点了登录一直转圈"（e2e 的 `TestProtoRustLogin` 抓的就是这个）。
            Body::LoginSaltRequest(r) => Some(mir2_net::Cmd::LoginSaltRequest(r.account.clone())),
            Body::Login(l) => Some(mir2_net::Cmd::Login {
                account: l.account.clone(),
                proof_hex: l.password_hash.clone(),
            }),
            Body::ListCharacters(_) => Some(mir2_net::Cmd::ListCharacters),
            Body::SelectCharacter(s) => Some(mir2_net::Cmd::SelectCharacter(s.character_id)),
            _ => None,
        };
        if let Some(c) = cmd {
            let _ = self.sess.cmds.send(c);
        }
    }

    /// 发一次移动输入（走）。方向用**线上编号**（`core::world` 里也不做 ±1 转换）。
    fn walk(&self, dir: mir2_protocol::Direction) {
        let _ = self.sess.cmds.send(mir2_net::Cmd::Move(dir as i32));
    }

    /// 把"这一帧看到的"折进各实体的动画状态：移动了就给补间的起止，动作变了就重置计时。
    ///
    /// ⚠️ 只在**变化时**刷新 `changed_at`：`EntityMove`/`EntityAction` 不是每帧都来，
    /// 每帧重置的话走路会永远停在第一帧、动作永远播不完。
    fn sync_anims(&mut self) {
        let now = Instant::now();
        let mut live: Vec<(u64, (i32, i32), Option<u32>)> = self
            .world
            .entities
            .values()
            .map(|e| (e.id, (e.x, e.y), e.action))
            .collect();
        if self.world.in_world() {
            live.push((
                self.world.self_id,
                self.world.self_pos,
                self.world.self_action,
            ));
        }
        let ids: std::collections::HashSet<u64> = live.iter().map(|(id, _, _)| *id).collect();
        for (id, cell, action) in live {
            let a = self.anims.entry(id).or_insert(ActorAnim {
                cell,
                from: None,
                action,
                changed_at: now,
            });
            if a.cell != cell {
                a.from = Some(a.cell); // 刚动了：记下从哪来（补间要用）
                a.cell = cell;
                a.changed_at = now;
            }
            if a.action != action {
                a.action = action;
                a.changed_at = now;
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
                let _ = self.sess.cmds.send(mir2_net::Cmd::Attack {
                    target_id: e.id,
                    action: mir2_protocol::AttackAction::AttackHit as i32,
                });
                println!("[net] 攻击 {} (ActorId={})", e.name, e.id);
                true
            }
            None => false,
        }
    }

    /// 相机该对着哪一格（居中自身）。没进世界时返回 `None`（保持手动镜头）。
    fn follow_cam(&self) -> Option<(i32, i32)> {
        if !self.world.in_world() {
            return None;
        }
        let (sx, sy) = self.world.self_pos;
        Some((
            sx - (WIN_W as i32 / UNIT_X) / 2,
            sy - (VIEW_H as i32 / UNIT_Y) / 2,
        ))
    }
}

/// 方向键：联网且在世界里 ⇒ 走一步并返回 `true`（调用方就别动镜头了）。
///
/// 离线时返回 `false` ⇒ 保持原来的"方向键平移镜头"（开发查看器最常用的动作）。
fn walk_if_online(net: &Option<Net>, dir: mir2_protocol::Direction) -> bool {
    match net {
        Some(n) if n.world.in_world() => {
            n.walk(dir);
            true
        }
        _ => false,
    }
}

/// 协议朝向（1..8）→ 屏幕增量。**1 = 上**（新枚举 = 原版 + 1，见 common.proto）。
///
/// ⚠️ 这张表与服务端 `entity.DirDelta` 是同一份顺序（原版 0..7 各 +1）——
/// 两处一旦不一致，人物会朝反方向走，而且不会报错。
/// 格子坐标 → 视口坐标（地图绘制用的同一套换算：`UNIT_X/UNIT_Y` + 顶部信息条）。
fn cell_to_screen(cam: (i32, i32), cx: i32, cy: i32) -> (f32, f32) {
    cell_to_screen_f(cam, cx as f32, cy as f32)
}

/// 同上，但允许**小数格** —— 走路的补间落在两格之间（见 `ActorAnim::draw_pos`）。
fn cell_to_screen_f(cam: (i32, i32), cx: f32, cy: f32) -> (f32, f32) {
    (
        (cx - cam.0 as f32) * UNIT_X as f32,
        BAR_TOP + (cy - cam.1 as f32) * UNIT_Y as f32,
    )
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
            .or_insert_with(|| Wzl::open(dir.join(lib)).ok())
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

    fn get(&self, lib: &'static str, idx: u32) -> Option<&SpriteTex<'a>> {
        self.texs.get(&(lib, idx))
    }
}

/// 一个实体的动画状态（**渲染层**持有 —— 世界模型是不带时钟的纯状态）。
struct ActorAnim {
    /// 上次看到的格子（用来判"又动了"）。
    cell: (i32, i32),
    /// 上一次移动的来处（补间用）。
    from: Option<(i32, i32)>,
    /// 最近一次动作（协议动作 id，见 `protocol.md` §9.5）。
    action: Option<u32>,
    /// 上面两者的发生时刻（一个时钟够用：动作与移动不会同时开始）。
    changed_at: Instant,
}

impl ActorAnim {
    fn elapsed_ms(&self, now: Instant) -> u32 {
        now.duration_since(self.changed_at).as_millis() as u32
    }

    /// 是不是正走在半路上（决定播走路的动画）。
    fn moving(&self, now: Instant) -> bool {
        self.from.is_some() && self.elapsed_ms(now) < MOVE_MS
    }

    /// 补间后的绘制坐标（格子坐标，浮点）。
    fn draw_pos(&self, to: (i32, i32), now: Instant) -> (f32, f32) {
        match self.from {
            Some(f) if self.moving(now) => {
                let t = self.elapsed_ms(now) as f32 / MOVE_MS as f32;
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
fn human_sample(held: Option<u32>, held_ms: u32, moving: bool) -> (mir2_core::actor::HAct, u16) {
    use mir2_core::actor as A;
    let mut pose = A::human_pose(held, moving);
    let mut act = pose.act.act();
    let elapsed = if !pose.looping && held_ms >= act.duration_ms() {
        pose = A::human_pose(None, moving);
        act = pose.act.act();
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

/// 怪物动作采样（同人物：空动作段与"播完"都退回站立/走路）。
fn monster_sample(
    race_img: u8,
    held: Option<u32>,
    held_ms: u32,
    moving: bool,
) -> (mir2_core::actor::MAct, u16) {
    use mir2_core::actor as A;
    let mut pose = A::monster_pose(race_img, held, moving);
    let mut act = A::mon_actions(race_img)[pose.act as usize];
    let elapsed = if !pose.looping && held_ms >= act.duration_ms() {
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
    let (held, held_ms) = anim.map_or((None, 0), |a| (a.action, a.elapsed_ms(now)));
    let moving = anim.is_some_and(|a| a.moving(now));
    match e.kind {
        // 玩家：本体在 Hum.wzl，部位号 = Dress（服务端已经算成 `Shape*2+性别`）
        0 => {
            let (act, frame) = human_sample(held, held_ms, moving);
            Some((A::HUM_LIB, A::human_index(f.dress as u8, act, dir, frame)))
        }
        // 怪物：容器与块起点都由 Appr 定（`Mon<Appr/10+1>`）
        1 => {
            let appr = f.appr as u16;
            let lib = A::mon_container(appr)?;
            let (act, frame) = monster_sample(f.race_img as u8, held, held_ms, moving);
            Some((
                lib,
                A::monster_index(appr, f.race_img as u8, act, dir, frame),
            ))
        }
        _ => None,
    }
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
    let (held, held_ms) = anim.map_or((None, 0), |a| (a.action, a.elapsed_ms(now)));
    let moving = anim.is_some_and(|a| a.moving(now));
    let (act, frame) = human_sample(held, held_ms, moving);
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
    sprites: &mut SpriteCache<'a>,
    dir_assets: &Path,
    cam: (i32, i32),
    e: &mir2_core::world::Entity,
    anim: Option<&ActorAnim>,
    now: Instant,
    name: &str,
    hp: u32,
    max_hp: u32,
    color: Color,
) -> Result<(), sdl3::Error> {
    // 补间后的位置（不做插值的话，精灵是一格一格跳的）
    let (fx, fy) = anim.map_or((e.x as f32, e.y as f32), |a| a.draw_pos((e.x, e.y), now));
    let (px, py) = cell_to_screen_f(cam, fx, fy);

    let body = body_sprite(e, anim, now);
    if body.is_none() {
        // 没有精灵（NPC / 素材缺失 / 图号取不到）：退回标记，**不静默什么都不画**
        return draw_entity_marker(canvas, cam, e.x, e.y, color, name, hp, max_hp, e.dir);
    }
    // 本体 → 武器（原版层序：武器压在身体上面）
    for layer in [body, weapon_sprite(e, anim, now)] {
        let Some((lib, idx)) = layer else { continue };
        if sprites.ensure(tc, dir_assets, lib, idx).is_none() {
            continue;
        }
        if let Some(t) = sprites.get(lib, idx) {
            let q = t.tex.query();
            canvas.copy(
                &t.tex,
                None::<FRect>,
                FRect::new(
                    px + t.anchor_x as f32,
                    py + t.anchor_y as f32,
                    q.width as f32,
                    q.height as f32,
                ),
            )?;
        }
    }
    draw_name_bar(
        canvas,
        px + UNIT_X as f32 / 2.0,
        py,
        name,
        hp,
        max_hp,
        color,
    )
}

/// 画一个实体标记（**降级路径**：拿不到精灵时用，也让人一眼看出"这里本该有东西"）。
///
/// ⚠️ **为什么先画标记而不是精灵**：actor 的图号公式（`raceImg/weapon/hair/dress` →
/// `Hum.wzl` / `Objects<N>.wzl` 里的第几张，还要按朝向/动作分块）尚未提取，
/// 那属于 M2 的"角色/怪物动画状态机"；且本套素材里 `Hair.wzl` 是空壳。
/// 标记先把"位置/朝向/名字/血量"这条链验通 —— 换精灵时只改这一个函数。
#[allow(clippy::too_many_arguments)]
fn draw_entity_marker(
    canvas: &mut WindowCanvas,
    cam: (i32, i32),
    cx: i32,
    cy: i32,
    color: Color,
    name: &str,
    hp: u32,
    max_hp: u32,
    dir: i32,
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
        sx + UNIT_X as f32 / 2.0,
        sy,
        name,
        hp,
        max_hp,
        color,
    )
}

/// 名字 + 血条（精灵与标记两条路共用；名字居中在**格子中心**上方）。
///
/// 血条只在"受了伤"时画，否则一屏全是条。
fn draw_name_bar(
    canvas: &mut WindowCanvas,
    center_x: f32,
    cell_top: f32,
    name: &str,
    hp: u32,
    max_hp: u32,
    color: Color,
) -> Result<(), sdl3::Error> {
    let label = trunc(name, 12);
    text(
        canvas,
        &label,
        center_x - label.chars().count() as f32 * 4.0,
        cell_top - 9.0,
        color,
    )?;
    if max_hp > 0 && hp < max_hp {
        let w = UNIT_X as f32 - 16.0;
        let frac = (hp as f32 / max_hp as f32).clamp(0.0, 1.0);
        let y = cell_top + UNIT_Y as f32 - 6.0;
        canvas.set_draw_color(Color::RGB(40, 40, 40));
        canvas.fill_rect(FRect::new(center_x - w / 2.0, y, w, 3.0))?;
        canvas.set_draw_color(Color::RGB(220, 60, 60));
        canvas.fill_rect(FRect::new(center_x - w / 2.0, y, w * frac, 3.0))?;
    }
    Ok(())
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let sdl = sdl3::init()?;
    let video = sdl.video()?;

    let window = video
        .window("MIR2 1.76 CLIENT - DEV VIEWER", WIN_W, WIN_H)
        .position_centered()
        .build()
        .map_err(|e| format!("创建窗口失败: {e}"))?;

    video.text_input().start(&window);
    let mut canvas = window.into_canvas();
    let tex_creator = canvas.texture_creator();

    // ---------- 音频 ----------
    let audio = sdl.audio()?;
    let muted = Arc::new(AtomicBool::new(false));
    let spec = AudioSpec {
        freq: Some(SAMPLE_RATE),
        channels: Some(1),
        format: Some(AudioFormat::F32LE),
    };
    let device =
        audio.open_playback_stream(&spec, Music::new(SAMPLE_RATE as f32, muted.clone()))?;
    device.resume()?;

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
    println!("[mir2-app] 音频驱动 = {}", audio.current_audio_driver());
    println!("[mir2-app] 操作：F1 登录界面 / F2 地图视图 / F3 素材浏览器 / M 音乐 / ESC 退出");

    let mut events: EventPump = sdl.event_pump()?;

    // 登录界面的状态（照原版的那套版式与交互，见 `login.rs`）
    let mut login = login::Login::new();
    // 界面素材缓存（`Prguse` / `ChrSel`）—— 与地图图块、actor 精灵的缓存分开
    let mut ui = ui::UiCache::new();
    // 选角场景（登录成功、状态机停在"等你选"时才建）与真字体绘制器
    let mut select_scene: Option<select::Select> = None;
    let mut texts = font::TextCache::new(mir2_core::text::UI_PX);

    // 素材浏览器的状态（F3）
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // 地图模式的状态
    let mut mode: u8 = 1; // 1 = 登录界面（从头开始就是它），2 = 地图，3 = 素材浏览器
    let mut map_i: usize = 0;
    let mut map: Option<Map> = None;
    let mut map_err = String::new();
    let mut cam = (0i32, 0i32);
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, TileTex<'_>> = HashMap::new();
    let mut sprites = SpriteCache::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    // 调试叠加层（D 开关）：画格网 + 每层落点框 + 鼠标十字线，并"点哪读哪"
    let mut debug = false;
    let mut layers: u8 = LAYERS_ALL; // 三层显隐掩码（CTRL+1/2/3 独立开关，L 循环单选）
    let mut mouse = (0.0f32, 0.0f32);

    let mut music_on = true;
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
            match ev {
                Event::Quit { .. } => break 'main,
                Event::KeyDown {
                    keycode, keymod, ..
                } => match keycode {
                    Some(Keycode::Escape) => break 'main,
                    Some(Keycode::F1) => mode = 1,
                    Some(Keycode::F2) => mode = 2,
                    Some(Keycode::F3) => mode = 3,
                    Some(Keycode::M) => {
                        music_on = !music_on;
                        muted.store(!music_on, Ordering::Relaxed);
                        status = format!("MUSIC {}", if music_on { "ON" } else { "OFF" });
                    }
                    // 选角：键盘是**我们的扩展**（原版选角场景只认鼠标）
                    _ if mode == 4 => {
                        if let Some(k) = keycode {
                            let act = match select_scene.as_mut() {
                                Some(s) => s.on_key(k),
                                None => select::Action::None,
                            };
                            if do_select_action(act, &mut net, &mut select_scene)? {
                                break 'main;
                            }
                        }
                    }
                    // 登录界面：全部交互在 `login` 里（Tab/退格/回车/ESC），这里只把
                    // 它给出的动作翻译成"接下来干什么"。
                    _ if mode == 1 => {
                        if let Some(k) = keycode {
                            match login.on_key(k) {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::NewAccount => {
                                    // 原版会开 `DLoginNew` 对话框（`FState.pas:886`）。
                                    // 那条链要服务端配合建号，还没接 ⇒ 明确说一声，别装作成功。
                                    status = "NEW ACCOUNT: NOT WIRED YET".into();
                                    println!("[login] 新建账号尚未接线（原版开 DLoginNew 对话框）");
                                }
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
                                cam.0 -= 2
                            }
                        }
                        Some(Keycode::Right) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirRight) {
                                cam.0 += 2
                            }
                        }
                        Some(Keycode::Up) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirUp) {
                                cam.1 -= 2
                            }
                        }
                        Some(Keycode::Down) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirDown) {
                                cam.1 += 2
                            }
                        }
                        // 空格：打一下身边的目标（A′：走 + 砍 = 能玩）。
                        Some(Keycode::Space) => {
                            if let Some(n) = net.as_ref().filter(|n| n.world.in_world()) {
                                if !n.attack_adjacent() {
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
                        Some(Keycode::Home) => cam = (0, 0),
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
                            dump_draws(&draws, cam, &tiles, layers);
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
                Event::MouseMotion { x, y, .. } => mouse = (x, y),
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 2 => probe_at(x, y, cam, &draws, &tiles, layers),
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    if let (Some(dir), Some(scene)) = (asset_dir.as_ref(), select_scene.as_mut()) {
                        if let Some(l) =
                            mir2_core::select_ui::Layout::build((WIN_W, WIN_H), |c, i| {
                                ui.size(dir, c, i)
                            })
                        {
                            scene.on_down((x, y), &l);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    let act = match (asset_dir.as_ref(), select_scene.as_mut()) {
                        (Some(dir), Some(scene)) => {
                            match mir2_core::select_ui::Layout::build((WIN_W, WIN_H), |c, i| {
                                ui.size(dir, c, i)
                            }) {
                                Some(l) => scene.on_up((x, y), &l),
                                None => select::Action::None,
                            }
                        }
                        _ => select::Action::None,
                    };
                    if do_select_action(act, &mut net, &mut select_scene)? {
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
                    if let Some(dir) = asset_dir.as_ref() {
                        let l = mir2_core::login_ui::Layout::build((WIN_W, WIN_H), |c, i| {
                            ui.size(dir, c, i)
                        });
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
                    if let Some(dir) = asset_dir.as_ref() {
                        let l = mir2_core::login_ui::Layout::build((WIN_W, WIN_H), |c, i| {
                            ui.size(dir, c, i)
                        });
                        if let Some(l) = l {
                            match login.on_up((x, y), &l) {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::Quit => break 'main,
                                login::Action::Dismiss => {}
                                login::Action::NewAccount => {
                                    status = "NEW ACCOUNT: NOT WIRED YET".into();
                                }
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::None => {}
                            }
                        }
                    }
                }
                Event::TextInput { text: t, .. } if mode == 1 => login.on_text(&t),
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
            if let Some(why) = n.entrance.failed() {
                if login.error.is_none() {
                    login.error = Some(why.to_string());
                    login.busy = false;
                }
            }
            // 连接层失败（连不上/被断开）：同样要弹出来并解掉"登录中"，
            // 否则界面会一直转圈 —— 而原因只在终端里（踩过）。
            if let Some(why) = n.fail.clone() {
                if login.error.is_none() {
                    login.error = Some(connect_hint(&why));
                    login.busy = false;
                }
            }
            if n.entrance.in_world() && login.opened_at.is_none() {
                login.opened_at = Some(Instant::now());
                login.busy = false;
                select_scene = None;
                mode = 1; // 开门动画在登录屏上播（否则会在地图里"看不见地"播完）
            }
            // 登录成功后会停在 `AwaitPick`（`set_manual_pick`）⇒ 切到选角场景。
            //
            // ⚠️ "停在等你选"是**状态机说的**，不是我们猜的时机：角色列表就在它手上
            //（`entrance.characters()`），界面只负责显示与选择。
            // ⚠️ 判据**不能**带上 `mode == 1`：带会话号启动时 mode 会被设成 2（地图，
            // 见上面的 `if net.is_some()`），而重连回落到选角一样要切过来（实测踩过：
            // 服务端已经回了"列角色 → 1 个"，界面却还停在地图视图上）。
            if mode != 4 && n.entrance.stage() == &mir2_core::entrance::Stage::AwaitPick {
                let chars: Vec<select::CharEntry> = n
                    .entrance
                    .characters()
                    .iter()
                    .map(select::CharEntry::from_summary)
                    .collect();
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
                mode = 4;
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
        // 进了世界就让相机跟着自己（离线时保持手动镜头）。
        if let Some(c) = net.as_ref().and_then(|n| n.follow_cam()) {
            cam = c;
        }

        // 地图镜头夹在合理范围内（允许露出边缘一格）
        if let Some(m) = &map {
            let max_x = (m.width as i32 - (WIN_W as i32 / UNIT_X) + 2).max(0);
            let max_y = (m.height as i32 - (VIEW_H as i32 / UNIT_Y) + 2).max(0);
            cam.0 = cam.0.clamp(-2, max_x);
            cam.1 = cam.1.clamp(-2, max_y);
        }

        // 前景动画的节拍：官方 `m_nAniCount` **每 50 ms 加一**（`PlayScn.pas:963`，
        // 固定定时器、与帧率无关）。所以这里按**真实时间**算，而不是每帧 +1 ——
        // 否则灯会随机器性能忽快忽慢。
        let ani_count = (started.elapsed().as_millis() / 50) as u32;

        canvas.set_draw_color(C_BG);
        canvas.clear();

        if mode == 2 {
            draw_map_view(
                &mut canvas,
                &tex_creator,
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
            )?;
        } else if mode == 4 {
            if let Some(scene) = select_scene.as_mut() {
                scene.draw(
                    &mut canvas,
                    &tex_creator,
                    &mut ui,
                    &mut texts,
                    &asset_dir,
                    (WIN_W, WIN_H),
                    started,
                )?;
            }
        } else if mode == 1 {
            // 开门动画播完 ⇒ 进地图（原版也是"开门 → 换场景"，`IntroScn.pas:907-914`）
            if login.door_done() {
                mode = 2;
            }
            login.draw(
                &mut canvas,
                &mut ui,
                &tex_creator,
                &asset_dir,
                (WIN_W, WIN_H),
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

        // 顶部/底部公共条
        let hint = hint_text(mode);
        fill(
            &mut canvas,
            0.0,
            WIN_H as f32 - BAR_BOTTOM,
            WIN_W as f32,
            BAR_BOTTOM,
            C_PANEL,
        )?;
        text(
            &mut canvas,
            &trunc(hint, TEXT_COLS),
            4.0,
            WIN_H as f32 - BAR_BOTTOM + 6.0,
            C_DIM,
        )?;

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

/// 切换到第 `i` 张地图（按容器内名字升序）。
fn load_map(a: &Archive, i: usize, map: &mut Option<Map>, err: &mut String, cam: &mut (i32, i32)) {
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
            *cam = (tx - cols / 2, ty - rows / 2);
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

#[allow(clippy::too_many_arguments)]
fn draw_map_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<TileKey, TileTex<'a>>,
    draws: &mut Vec<TileDraw>,
    sprites: &mut SpriteCache<'a>,
    asset_dir: &Option<PathBuf>,
    map: &Option<Map>,
    map_err: &str,
    cam: (i32, i32),
    map_i: usize,
    map_count: usize,
    debug: bool,
    layers: u8,
    mouse: (f32, f32),
    ani_count: u32,
    net: Option<&Net>,
) -> Result<(), sdl3::Error> {
    fill(canvas, 0.0, 0.0, WIN_W as f32, BAR_TOP, C_PANEL)?;

    let Some(dir) = asset_dir else {
        text(
            canvas,
            "ASSETS NOT FOUND - SET MIR2_ASSET_DIR",
            4.0,
            8.0,
            C_ERR,
        )?;
        return Ok(());
    };

    if map.is_none() {
        let msg = if map_err.is_empty() {
            "NO MAP CONTAINER - RUN tools/m2pk/build.sh".to_string()
        } else {
            format!("PARSE FAILED: {}", trunc(map_err, 60))
        };
        text(canvas, &msg, 4.0, 8.0, C_ERR)?;
        return Ok(());
    }
    let m = map.as_ref().unwrap();

    // 「画什么、按什么顺序画」是游戏知识，放在 core（map::visible_tiles，
    // 有单测守着三层顺序与隔格规则）；这里只负责取纹理 + 上屏。
    let cols = WIN_W as i32 / UNIT_X + 3;
    let rows = VIEW_H as i32 / UNIT_Y + 3;
    m.visible_tiles(cam.0, cam.1, cols, rows, ani_count, draws);
    // 裁剪到地图视口：`visible_tiles` 左上会多给一格（坐标可能为负），
    // 且高图块（树/墙）本身上端会超出视口——不裁剪就会画到上下信息条上。
    canvas.set_clip_rect(Some(Rect::new(0, BAR_TOP as i32, WIN_W, VIEW_H as u32)));
    let view = viewport_rect();
    for d in draws.iter() {
        // 逐层显隐（CTRL+1/2/3 / L）：关掉的层**既不画图块也不画调试框**
        if layers & d.layer.bit() == 0 {
            continue;
        }
        // 视口剔除：前景向下多扫了 35 行，那批候选多半够不着视口。
        // 先按 WZL 记录（不解码像素）判掉，省下解码与贴图上传。
        if !draw_rect_cold(libs, dir, d).is_some_and(|r| intersects(&r, &view)) {
            continue;
        }
        draw_tile(canvas, tc, libs, tiles, dir, d, BAR_TOP)?;
    }
    canvas.set_clip_rect(None::<Rect>);

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
                )?;
            }

            // 自己：`entities` 里**没有自己**（快照刻意不含，见 core::world 的 self_feature）
            // ⇒ 在这里造一个临时实体走**同一条**绘制路径，免得"自己的画法"与别人漂成两套。
            let (hp, max_hp) = n.world.self_hp.unwrap_or((0, 0));
            let me = mir2_core::world::Entity {
                id: n.world.self_id,
                kind: 0,
                name: "[自己]".to_string(),
                x: n.world.self_pos.0,
                y: n.world.self_pos.1,
                dir: n.world.self_dir,
                feature: n.world.self_feature,
                hp,
                max_hp,
                status_bits: 0,
                dead: n.world.self_dead,
                action: n.world.self_action,
            };
            draw_actor(
                canvas,
                tc,
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
                text(canvas, txt, px + 18.0, py - 10.0 - age as f32 / 60.0, col)?;
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
    text(canvas, &trunc(&info, TEXT_COLS - 20), 4.0, 8.0, C_TITLE)?;
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
    let rx = WIN_W as f32 - 6.0 - right.chars().count() as f32 * 8.0;
    text(canvas, &right, rx, 8.0, C_DIM)?;
    Ok(())
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
            *loaded = Wzl::open(dir.join(name)).ok().map(|l| (lib_idx, l));
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

/// 选角场景做出的动作 → 真的去做。
///
/// ⚠️ 抽出来是因为**键盘与鼠标两条路**都会产生它：两处各写一遍迟早漂移
///（一边发了 `SelectCharacter`、另一边忘了）。返回 `true` = 该退出程序。
fn do_select_action(
    act: select::Action,
    net: &mut Option<Net>,
    select_scene: &mut Option<select::Select>,
) -> Result<bool, sdl3::Error> {
    match act {
        select::Action::None => Ok(false),
        select::Action::Exit => Ok(true),
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
            if let Some(scene) = select_scene.as_mut() {
                scene.start_clicked = true;
            }
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
            const PLAY: &str = "ARROWS WALK  SPACE HIT  C CONNECT  [ ] MAP  P DUMP  F1 LOGIN  ESC";
            const DEBUG_KEYS: &str =
                "ARROWS WALK  SPACE HIT  C CONNECT  [ ] MAP  D DEBUG  1/2/3 LAYER  P DUMP  F1 LOGIN  ESC";
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
        };
        let (x, y) = a.draw_pos((5, 5), now);
        assert!(
            (x - 4.0).abs() < 0.01 && (y - 5.0).abs() < 0.01,
            "刚开始还该在来处"
        );
        let later = now + Duration::from_millis(MOVE_MS as u64 + 10);
        assert_eq!(
            a.draw_pos((5, 5), later),
            (5.0, 5.0),
            "过了补间时长就该到位"
        );
        assert!(!a.moving(later));
    }

    /// 动作播完回站立 —— 否则实体会永远停在那一刀的末帧。
    #[test]
    fn 动作播完回站立() {
        assert_eq!(human_sample(Some(1), 0, false).0, A::HAct::Hit);
        // ActHit 是 6 帧 × 85ms = 510ms ⇒ 600ms 后应回到站立
        assert_eq!(human_sample(Some(1), 600, false), (A::HAct::Stand, 0));
    }

    /// 手上的动作播完后，**走路**优先于站立（在走就别站着）。
    #[test]
    fn 动作播完且在走就播走路() {
        assert_eq!(human_sample(Some(1), 600, true).0, A::HAct::Walk);
    }
}

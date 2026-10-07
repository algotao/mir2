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

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use mir2_core::m2pk::Archive;
use mir2_core::map::{Layer, Lib, Map, TileDraw, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream};
use sdl3::event::Event;
use sdl3::keyboard::{Keycode, Mod};
use sdl3::mouse::MouseButton;
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
const INFO_COLS: usize = 28;
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
const C_BTN: Color = Color::RGB(46, 70, 116);
const C_BTN_BORDER: Color = Color::RGB(150, 186, 236);
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

/// 图层可见性掩码：bit0 = 地表 bit1 = 中间 bit2 = 前景；默认三层全开。
///
/// 三层**各自独立**开关（`CTRL+1/2/3`），而不是"单选一层"——
/// 排查错位时最常用的动作是"只关掉一层看底下那层在哪"，
/// 单选模式反而要来回切两次才能对比。
const LAYERS_ALL: u8 = 0b111;

/// `Layer` → 可见性掩码位。
fn layer_bit(l: Layer) -> u8 {
    match l {
        Layer::Ground => 1,
        Layer::Mid => 2,
        Layer::Front => 4,
    }
}

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
/// `$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data`。
fn resolve_asset_dir() -> Option<PathBuf> {
    for key in ["MIR2_ASSET_DIR", "MIR2C_DATA"] {
        if let Ok(v) = std::env::var(key) {
            let p = PathBuf::from(v);
            if p.is_dir() {
                return Some(p);
            }
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../mir2c/data");
    guess.canonicalize().ok().filter(|p| p.is_dir())
}

/// `$MIR2_MAP_CONTAINER` → 仓库的 `assets/map/maps.m2pk`。
fn resolve_container() -> Option<PathBuf> {
    if let Ok(v) = std::env::var("MIR2_MAP_CONTAINER") {
        let p = PathBuf::from(v);
        if p.is_file() {
            return Some(p);
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/map/maps.m2pk");
    guess.canonicalize().ok().filter(|p| p.is_file())
}

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
        if layers & layer_bit(d.layer) == 0 {
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
            layers & layer_bit(d.layer) != 0
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
            if layers & layer_bit(d.layer) == 0 {
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
        if layers & layer_bit(d.layer) == 0 {
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
    let asset_dir = resolve_asset_dir();
    let container_path = resolve_container();
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
    println!("[mir2-app] 操作：F1 登录界面 / F2 地图视图 / M 音乐 / ESC 退出");

    let mut events: EventPump = sdl.event_pump()?;

    // 登录模式的状态
    let mut id = String::new();
    let mut pw = String::new();
    let mut active: usize = 0;
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // 地图模式的状态
    let mut mode: u8 = 2; // 1 = 登录，2 = 地图（直接开在地图视图上）
    let mut map_i: usize = 0;
    let mut map: Option<Map> = None;
    let mut map_err = String::new();
    let mut cam = (0i32, 0i32);
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, TileTex<'_>> = HashMap::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    // 调试叠加层（D 开关）：画格网 + 每层落点框 + 鼠标十字线，并"点哪读哪"
    let mut debug = false;
    let mut layers: u8 = LAYERS_ALL; // 三层显隐掩码（CTRL+1/2/3 独立开关，L 循环单选）
    let mut mouse = (0.0f32, 0.0f32);

    let mut music_on = true;
    let started = Instant::now();

    // 载入初始地图
    if let Some(a) = &archive {
        load_map(a, map_i, &mut map, &mut map_err, &mut cam);
    }

    'main: loop {
        for ev in events.poll_iter() {
            match ev {
                Event::Quit { .. } => break 'main,
                Event::KeyDown {
                    keycode, keymod, ..
                } => match keycode {
                    Some(Keycode::Escape) => break 'main,
                    Some(Keycode::F1) => mode = 1,
                    Some(Keycode::F2) => mode = 2,
                    Some(Keycode::M) => {
                        music_on = !music_on;
                        muted.store(!music_on, Ordering::Relaxed);
                        status = format!("MUSIC {}", if music_on { "ON" } else { "OFF" });
                    }
                    _ if mode == 1 => match keycode {
                        Some(Keycode::Tab) => active = 1 - active,
                        Some(Keycode::Backspace) => {
                            if active == 0 {
                                id.pop();
                            } else {
                                pw.pop();
                            }
                        }
                        Some(Keycode::Return) => {
                            let who = if id.is_empty() { "GUEST" } else { id.as_str() };
                            println!(
                                "[login] 用户名={:?} 密码长度={} → 桩实现（尚未连接服务端）",
                                who,
                                pw.chars().count()
                            );
                            status = format!("LOGIN AS {} ... STUB OK", who);
                        }
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
                        Some(Keycode::Left) => cam.0 -= 2,
                        Some(Keycode::Right) => cam.0 += 2,
                        Some(Keycode::Up) => cam.1 -= 2,
                        Some(Keycode::Down) => cam.1 += 2,
                        Some(Keycode::Home) => cam = (0, 0),
                        // ---- 调试叠加层（只在地图模式，避免污染登录输入框）----
                        Some(Keycode::D) => {
                            debug = !debug;
                            println!(
                                "[debug] 叠加层 {}（L 或 CTRL+1/2/3 控制图层显隐 / P 打印绘制清单 / 左键点哪读哪）",
                                if debug { "ON" } else { "OFF" }
                            );
                        }
                        // 逐层独立显隐：排查错位时最常用的是"关掉一层看底下那层"
                        Some(Keycode::_1) | Some(Keycode::_2) | Some(Keycode::_3)
                            if keymod.intersects(Mod::LCTRLMOD)
                                || keymod.intersects(Mod::RCTRLMOD) =>
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
                        Some(Keycode::L) => {
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
                Event::TextInput { text: t, .. } if mode == 1 => {
                    for ch in t.chars() {
                        if ch.is_ascii_graphic() || ch == ' ' {
                            if active == 0 && id.chars().count() < 12 {
                                id.push(ch);
                            } else if active == 1 && pw.chars().count() < 12 {
                                pw.push(ch);
                            }
                        }
                    }
                }
                _ => {}
            }
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
            )?;
        } else {
            draw_login_view(
                &mut canvas,
                &tex_creator,
                &asset_dir,
                &mut loaded,
                &mut sprite_tex,
                &id,
                &pw,
                active,
                &status,
                music_on,
                lib_idx,
                &mut img_idx,
                started,
            )?;
        }

        // 顶部/底部公共条
        let hint = if mode == 2 {
            "ARROWS  [ ] MAP  HOME  D DEBUG  CTRL 1/2/3 LAYER  P DUMP  PROBE  F1 LOGIN  ESC"
        } else {
            "TAB FIELD   ENTER LOGIN   [ ] LIB   , . IMG   F2 MAP   M MUSIC   ESC QUIT"
        };
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
    for d in draws.iter() {
        // 逐层显隐（CTRL+1/2/3 / L）：关掉的层**既不画图块也不画调试框**
        if layers & layer_bit(d.layer) == 0 {
            continue;
        }
        draw_tile(canvas, tc, libs, tiles, dir, d, BAR_TOP)?;
    }
    canvas.set_clip_rect(None::<Rect>);

    if debug {
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
    let right = if layers == LAYERS_ALL {
        format!("TILES {}", tiles.len())
    } else {
        format!("LAYER {}   TILES {}", layers_desc(layers), tiles.len())
    };
    let rx = WIN_W as f32 - 6.0 - right.chars().count() as f32 * 8.0;
    text(canvas, &right, rx, 8.0, C_DIM)?;
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn draw_login_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    asset_dir: &Option<PathBuf>,
    loaded: &mut Option<(usize, Wzl)>,
    sprite_tex: &mut Option<Texture<'a>>,
    id: &str,
    pw: &str,
    active: usize,
    status: &str,
    music_on: bool,
    lib_idx: usize,
    img_idx: &mut usize,
    started: Instant,
) -> Result<(), sdl3::Error> {
    // 右侧：真实精灵
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
            let mut found = None;
            for k in 0..64 {
                let i = (*img_idx + k) % total.max(1);
                if let Some(s) = lib.decode(i) {
                    found = Some((i, s));
                    break;
                }
            }
            if let Some((i, s)) = found {
                *img_idx = i;
                let is16 = lib.record(i).map(|r| r.is_16bit()).unwrap_or(false);
                sprite_dims = (s.width as u32, s.height as u32);
                info_lines = vec![
                    (format!("{}  #{} / {}", name, i, total), C_TEXT),
                    (
                        format!(
                            "{}x{}   ANCHOR({},{})",
                            s.width, s.height, s.anchor_x, s.anchor_y
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
                        q.width != s.width as u32 || q.height != s.height as u32
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
                        s.width as u32,
                        s.height as u32,
                    ) {
                        t.set_blend_mode(BlendMode::Blend);
                        t.set_scale_mode(ScaleMode::Nearest);
                        *sprite_tex = Some(t);
                    }
                }
                if let Some(t) = sprite_tex.as_mut() {
                    if t.update(None::<Rect>, &s.rgba, s.width as usize * 4)
                        .is_err()
                    {
                        ready = false;
                    }
                }
            } else {
                info_lines = vec![(format!("{}  #{}  (空壳图)", name, img_idx), C_DIM)];
            }
        } else {
            info_lines = vec![(format!("{name}.wzl 打不开"), C_ERR)];
        }
    }

    // 整块**垂直居中**：本布局是按 480 高的窗口排的，窗口变高后若不偏移，
    // 所有内容都会挤在顶部、下方空掉一大片（768 高时尤其难看）。
    // 用**渲染视口平移**实现 —— 坐标字面量一处都不用改；
    // 底部提示条由调用方在原视口下画，不受影响。
    let dy = ((WIN_H as f32 - 480.0) * 0.5).max(0.0) as i32;
    canvas.set_viewport(Some(Rect::new(0, dy, WIN_W, WIN_H - dy as u32)));

    let t1 = "MIR2  1.76  CLIENT";
    text(canvas, t1, center_x(t1, 0.0, WIN_W as f32), 16.0, C_TITLE)?;
    let t2 = "SDL3  DEV  VIEWER  (F2 = MAP)";
    text(canvas, t2, center_x(t2, 0.0, WIN_W as f32), 34.0, C_DIM)?;

    const LX: f32 = 20.0;
    const LW: f32 = 336.0;
    fill(canvas, LX, 60.0, LW, 250.0, C_PANEL)?;
    frame(canvas, LX, 60.0, LW, 250.0, C_PANEL_BORDER)?;

    text(canvas, "ACCOUNT", LX + 14.0, 96.0, C_TEXT)?;
    fill(canvas, LX + 14.0, 112.0, LW - 28.0, 22.0, C_FIELD)?;
    frame(
        canvas,
        LX + 14.0,
        112.0,
        LW - 28.0,
        22.0,
        if active == 0 {
            C_ACTIVE
        } else {
            C_PANEL_BORDER
        },
    )?;
    text(canvas, id, LX + 20.0, 119.0, C_TEXT)?;

    text(canvas, "PASSWORD", LX + 14.0, 152.0, C_TEXT)?;
    fill(canvas, LX + 14.0, 168.0, LW - 28.0, 22.0, C_FIELD)?;
    frame(
        canvas,
        LX + 14.0,
        168.0,
        LW - 28.0,
        22.0,
        if active == 1 {
            C_ACTIVE
        } else {
            C_PANEL_BORDER
        },
    )?;
    let masked = "*".repeat(pw.chars().count());
    text(canvas, &masked, LX + 20.0, 175.0, C_TEXT)?;

    fill(canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN)?;
    frame(canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
    text(
        canvas,
        "LOGIN",
        LX + 14.0 + (140.0 - 40.0) / 2.0,
        220.0,
        C_ACTIVE,
    )?;
    fill(canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN)?;
    frame(canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
    text(
        canvas,
        "EXIT",
        LX + 182.0 + (140.0 - 32.0) / 2.0,
        220.0,
        C_TEXT,
    )?;

    let mus = format!("MUSIC: {}", if music_on { "ON" } else { "OFF" });
    text(
        canvas,
        &mus,
        LX + 14.0,
        256.0,
        if music_on { C_ACTIVE } else { C_DIM },
    )?;
    text(
        canvas,
        &format!("UPTIME {:.0}s", started.elapsed().as_secs_f32()),
        LX + 200.0,
        256.0,
        C_DIM,
    )?;
    text(
        canvas,
        &format!("STATUS: {status}"),
        LX + 14.0,
        280.0,
        C_TEXT,
    )?;

    // 右侧精灵面板（**右对齐**：窗口加宽后不会挤在中间）
    const RW: f32 = 248.0;
    let rx = WIN_W as f32 - RW - 20.0;
    fill(canvas, rx, 60.0, RW, 250.0, C_PANEL)?;
    frame(canvas, rx, 60.0, RW, 250.0, C_PANEL_BORDER)?;
    text(canvas, "SPRITE (REAL .WZL)", rx + 12.0, 72.0, C_TITLE)?;

    let px = rx + 12.0;
    let py = 92.0;
    const PW: f32 = RW - 24.0;
    const PH: f32 = 132.0;
    checkerboard(canvas, px, py, PW, PH)?;
    frame(canvas, px, py, PW, PH, C_PANEL_BORDER)?;

    if ready {
        if let Some(t) = sprite_tex.as_ref() {
            let (sw, sh) = (sprite_dims.0 as f32, sprite_dims.1 as f32);
            let scale = (PW / sw).min(PH / sh).floor().clamp(1.0, 6.0);
            let (dw, dh) = (sw * scale, sh * scale);
            canvas.copy(
                t,
                None::<FRect>,
                FRect::new(px + (PW - dw) / 2.0, py + (PH - dh) / 2.0, dw, dh),
            )?;
        }
    } else {
        let msg = if asset_dir.is_none() {
            "ASSETS NOT FOUND"
        } else {
            "NO SPRITE"
        };
        text(canvas, msg, center_x(msg, px, PW), py + 60.0, C_ERR)?;
    }

    for (i, (line, col)) in info_lines.iter().enumerate().take(3) {
        text(
            canvas,
            &trunc(line, INFO_COLS),
            px,
            py + PH + 8.0 + i as f32 * INFO_LINE_H,
            *col,
        )?;
    }
    let libl = format!("LIB [{}/{}]   IMG {}", lib_idx + 1, LIBS.len(), *img_idx);
    text(canvas, &libl, px, py + PH + 8.0 + 3.0 * INFO_LINE_H, C_TEXT)?;

    canvas.set_viewport(None::<Rect>);
    Ok(())
}

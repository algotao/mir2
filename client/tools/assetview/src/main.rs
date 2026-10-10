//! **素材观察器**：把 `mir2c/data` 下的 `.wzl` 图库摊成一格一格的缩略图，
//! 鼠标悬停高亮、点击即显示该图的 **ID**（窗口最下方，自动复制到剪贴板）。
//!
//! 找素材的痛点：图号是散落在代码里的魔法数字（`Prguse[384]`、`Items[398]`…），
//! 人眼认不出"第 384 张长什么样"。这个工具把图库摊开，点一下就知道 ID，
//! 还能直接复制（默认复制纯数字 `123`，按 `Shift+C` 复制 `库名[123]`）。
//!
//! # 用法
//! ```text
//! cargo run -p mir2-assetview -- --dir /path/to/mir2c/data --lib Prguse
//! # 不传参就按环境变量（MIR2_ASSET_DIR / MIR2C_DATA）找素材目录，库默认 Prguse
//! ```
//!
//! # 键位
//! - 滚轮 / `PageUp`/`PageDown` / `Home`/`End`：翻页
//! - `[` / `]`（或 `,` / `.`）：切换上一个 / 下一个图库
//! - 点图 ⇒ 选中并复制 ID；`C` 复制纯数字、`Shift+C` 复制 `库名[ID]`
//! - `Esc` / `Q`：退出

use std::collections::HashMap;
use std::error::Error;
use std::path::{Path, PathBuf};

use sdl3::clipboard::ClipboardUtil;
use sdl3::event::Event;
use sdl3::keyboard::{Keycode, Mod};
use sdl3::pixels::{Color, PixelFormat};
use sdl3::render::{
    BlendMode, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use mir2_core::text::{Face, Glyph};
use mir2_core::wzl::Wzl;

// ---------- 布局 ----------
const WIN_W: u32 = 1280;
const WIN_H: u32 = 880;
const BAR_H: f32 = 86.0; // 底部信息栏高
const PAD: f32 = 8.0; // 外边距
const CELL: f32 = 128.0; // 每格边长
const GAP: f32 = 8.0; // 格间距
const THUMB: f32 = CELL - 16.0; // 缩略图可用区（留边框）

fn cols() -> usize {
    (((WIN_W as f32 - PAD * 2.0) / (CELL + GAP)).floor() as usize).max(1)
}
fn rows() -> usize {
    (((WIN_H as f32 - BAR_H - PAD * 2.0) / (CELL + GAP)).floor() as usize).max(1)
}

// ---------- 字形（照 app/src/font.rs 的精简版：白色字形纹理 + 颜色在绘制时打） ----------
struct GlyphTex<'a> {
    tex: Texture<'a>,
    w: f32,
    h: f32,
}

struct Text<'a> {
    face: Option<Face>,
    texs: HashMap<(char, u32), GlyphTex<'a>>,
}

impl<'a> Text<'a> {
    fn new() -> Self {
        Text {
            face: Face::load(),
            texs: HashMap::new(),
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn draw<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        x: f32,
        y: f32,
        color: (u8, u8, u8),
        px: f32,
    ) -> Result<(), sdl3::Error> {
        let Some(face) = self.face.as_ref() else {
            canvas.set_draw_color(Color::RGB(color.0, color.1, color.2));
            let _ = canvas.draw_debug_text(s, sdl3::render::FPoint::new(x, y));
            return Ok(());
        };
        let ascent = face.metrics(px).ascent;
        let mut pen = x;
        for ch in s.chars() {
            let g = face.glyph(ch, px);
            ensure_glyph(&mut self.texs, tc, ch, px, &g);
            let top = ascent - (g.ymin as f32 + g.h as f32);
            if let Some(t) = self.texs.get_mut(&(ch, px.to_bits())) {
                if t.w > 0.0 {
                    t.tex.set_color_mod(color.0, color.1, color.2);
                    canvas.copy(
                        &t.tex,
                        None::<FRect>,
                        FRect::new((pen + g.xmin as f32).round(), y + top.round(), t.w, t.h),
                    )?;
                }
            }
            pen += g.advance;
        }
        Ok(())
    }
}

fn ensure_glyph<'a, T>(
    texs: &mut HashMap<(char, u32), GlyphTex<'a>>,
    tc: &'a TextureCreator<T>,
    ch: char,
    px: f32,
    g: &Glyph,
) {
    let key = (ch, px.to_bits());
    if texs.contains_key(&key) {
        return;
    }
    if texs.len() >= 512 {
        texs.clear();
    }
    if g.w == 0 || g.h == 0 {
        if let Ok(tex) = tc.create_texture(PixelFormat::RGBA32, TextureAccess::Static, 1, 1) {
            texs.insert(
                key,
                GlyphTex {
                    tex,
                    w: 0.0,
                    h: 0.0,
                },
            );
        }
        return;
    }
    let mut rgba = Vec::with_capacity(g.cov.len() * 4);
    for c in &g.cov {
        rgba.extend_from_slice(&[255, 255, 255, *c]);
    }
    let Ok(mut tex) = tc.create_texture(PixelFormat::RGBA32, TextureAccess::Static, g.w, g.h)
    else {
        return;
    };
    tex.set_blend_mode(BlendMode::Blend);
    tex.set_scale_mode(ScaleMode::Nearest);
    if tex
        .update(None::<sdl3::rect::Rect>, &rgba, g.w as usize * 4)
        .is_err()
    {
        return;
    }
    texs.insert(
        key,
        GlyphTex {
            tex,
            w: g.w as f32,
            h: g.h as f32,
        },
    );
}

// ---------- 网格里的一个格 ----------
fn cell_rect(i: usize) -> FRect {
    let c = cols();
    let ix = (i % c) as f32;
    let iy = (i / c) as f32;
    FRect::new(PAD + ix * (CELL + GAP), PAD + iy * (CELL + GAP), CELL, CELL)
}

/// 画布上一个点落在哪一格（没有 ⇒ None）。
fn hit_cell(x: f32, y: f32) -> Option<usize> {
    let c = cols();
    let r = rows();
    let cx = ((x - PAD) / (CELL + GAP)).floor() as i32;
    let cy = ((y - PAD) / (CELL + GAP)).floor() as i32;
    if cx < 0 || cy < 0 || cx >= c as i32 || cy >= r as i32 {
        return None;
    }
    let lx = x - (PAD + cx as f32 * (CELL + GAP));
    let ly = y - (PAD + cy as f32 * (CELL + GAP));
    if lx < 0.0 || ly < 0.0 || lx > CELL || ly > CELL {
        return None;
    }
    Some(cy as usize * c + cx as usize)
}

struct Args {
    dir: Option<PathBuf>,
    lib: Option<String>,
}

fn parse_args() -> Args {
    let mut a = Args {
        dir: None,
        lib: None,
    };
    let v: Vec<String> = std::env::args().collect();
    let mut i = 1;
    while i < v.len() {
        match v[i].as_str() {
            "--dir" | "-d" => {
                a.dir = v.get(i + 1).map(PathBuf::from);
                i += 2;
            }
            "--lib" | "-l" => {
                a.lib = v.get(i + 1).cloned();
                i += 2;
            }
            _ => i += 1,
        }
    }
    a
}

fn list_libs(dir: &Path) -> Vec<String> {
    let mut out = Vec::new();
    let Ok(rd) = std::fs::read_dir(dir) else {
        return out;
    };
    for e in rd.flatten() {
        let p = e.path();
        if p.extension().and_then(|s| s.to_str()) == Some("wzl") {
            if let Some(stem) = p.file_stem().and_then(|s| s.to_str()) {
                out.push(stem.to_string());
            }
        }
    }
    out.sort();
    out
}

fn main() {
    let args = parse_args();
    let dir = args
        .dir
        .clone()
        .or_else(mir2_core::paths::asset_dir)
        .unwrap_or_else(|| PathBuf::from("."));
    if !dir.is_dir() {
        eprintln!("素材目录不存在：{dir:?}（用 --dir 指定，或设 MIR2_ASSET_DIR）");
        std::process::exit(2);
    }
    let libs = list_libs(&dir);
    if libs.is_empty() {
        eprintln!("{dir:?} 下没有 .wzl 图库");
        std::process::exit(2);
    }
    let start_lib = args.lib.unwrap_or_else(|| "Prguse".to_string());
    let lib_idx = libs.iter().position(|l| *l == start_lib).unwrap_or(0);

    if let Err(e) = run(&dir, &libs, lib_idx) {
        eprintln!("运行失败: {e}");
        std::process::exit(1);
    }
}

fn run(dir: &Path, libs: &[String], start_idx: usize) -> Result<(), Box<dyn Error>> {
    let sdl = sdl3::init()?;
    let video = sdl.video()?;
    let window = video
        .window("素材观察器", WIN_W, WIN_H)
        .position_centered()
        .resizable()
        .build()
        .map_err(|e| format!("建窗口失败: {e}"))?;
    let mut canvas = window.into_canvas();
    let tc = canvas.texture_creator();
    let mut text = Text::new();
    let clipboard = video.clipboard();

    let mut lib_idx = start_idx;
    let mut wzl = open_wzl(dir, &libs[lib_idx])?;
    let mut scroll = 0usize;
    let mut selected: Option<usize> = None;
    let mut copied: String = String::new();
    let mut tex_cache = HashMap::new();

    let mut mouse = (0.0f32, 0.0f32);
    let mut running = true;
    'main: loop {
        // ---- 事件 ----
        let mut lib_changed = false;
        let mut scroll_changed = false;
        for e in sdl.event_pump()?.poll_iter() {
            match e {
                Event::Quit { .. } => {
                    running = false;
                    break;
                }
                Event::MouseMotion { x, y, .. } => mouse = (x, y),
                Event::MouseWheel { y, .. } => {
                    let step = if y >= 1.0 { y as usize } else { 1 };
                    scroll = scroll.saturating_sub(step * cols());
                    scroll_changed = true;
                }
                Event::MouseButtonDown {
                    mouse_btn: sdl3::mouse::MouseButton::Left,
                    x,
                    y,
                    ..
                } => {
                    if y >= WIN_H as f32 - BAR_H {
                        if let Some(sel) = selected {
                            copied = copy(&clipboard, sel, &libs[lib_idx], false);
                        }
                    } else if let Some(cell) = hit_cell(x, y) {
                        let idx = scroll + cell;
                        if idx < wzl.len() {
                            selected = Some(idx);
                            copied = copy(&clipboard, idx, &libs[lib_idx], false);
                        }
                    }
                }
                Event::KeyDown {
                    keycode, keymod, ..
                } => {
                    let shift = keymod.contains(Mod::LSHIFTMOD) || keymod.contains(Mod::RSHIFTMOD);
                    match keycode {
                        Some(Keycode::Escape) | Some(Keycode::Q) => {
                            running = false;
                            break;
                        }
                        Some(Keycode::C) => {
                            if let Some(sel) = selected {
                                copied = copy(&clipboard, sel, &libs[lib_idx], shift);
                            }
                        }
                        Some(Keycode::LeftBracket) | Some(Keycode::Comma) => {
                            lib_idx = (lib_idx + libs.len() - 1) % libs.len();
                            lib_changed = true;
                        }
                        Some(Keycode::RightBracket) | Some(Keycode::Period) => {
                            lib_idx = (lib_idx + 1) % libs.len();
                            lib_changed = true;
                        }
                        Some(Keycode::PageDown) => {
                            scroll += cols() * rows();
                            scroll_changed = true;
                        }
                        Some(Keycode::PageUp) => {
                            scroll = scroll.saturating_sub(cols() * rows());
                            scroll_changed = true;
                        }
                        Some(Keycode::Home) => {
                            scroll = 0;
                            scroll_changed = true;
                        }
                        Some(Keycode::End) => {
                            scroll = wzl.len().saturating_sub(cols() * rows());
                            scroll_changed = true;
                        }
                        _ => {}
                    }
                }
                _ => {}
            }
            if !running {
                break 'main;
            }
        }
        if !running {
            break;
        }

        if lib_changed {
            wzl = open_wzl(dir, &libs[lib_idx])?;
            tex_cache.clear();
            scroll = 0;
            selected = None;
            copied.clear();
        }
        if scroll_changed && scroll + cols() * rows() > wzl.len() {
            scroll = wzl.len().saturating_sub(cols() * rows());
        }

        let hover = hit_cell(mouse.0, mouse.1)
            .map(|c| scroll + c)
            .filter(|i| *i < wzl.len());

        canvas.window_mut().set_title(&format!(
            "素材观察器 — {}（{} 张）",
            libs[lib_idx],
            wzl.len()
        ))?;

        // ---- 绘制 ----
        canvas.set_draw_color(Color::RGB(28, 28, 34));
        canvas.clear();
        draw_grid(
            &mut canvas,
            &tc,
            &wzl,
            scroll,
            hover,
            selected,
            &mut tex_cache,
        )?;

        // 底部信息栏
        canvas.set_draw_color(Color::RGB(18, 18, 22));
        let _ = canvas.fill_rect(FRect::new(0.0, WIN_H as f32 - BAR_H, WIN_W as f32, BAR_H));
        canvas.set_draw_color(Color::RGB(90, 90, 100));
        let _ = canvas.fill_rect(FRect::new(0.0, WIN_H as f32 - BAR_H, WIN_W as f32, 1.0));

        let line1 = hover.map_or_else(
            || "悬停到某张图上查看它的信息".to_string(),
            |i| desc(&wzl, i, "悬停"),
        );
        text.draw(
            &mut canvas,
            &tc,
            &line1,
            14.0,
            WIN_H as f32 - BAR_H + 10.0,
            (210, 210, 210),
            16.0,
        )?;

        let line2 = match selected {
            Some(i) => {
                let d = desc(&wzl, i, "选中");
                let tip = if copied.is_empty() {
                    String::new()
                } else {
                    format!("   （已复制：{copied}）")
                };
                format!("{d}  → 点底部或按 C 复制{tip}")
            }
            None => "点一张图 ⇒ 显示 ID 并复制（C 复制数字，Shift+C 复制 库名[ID]）".to_string(),
        };
        text.draw(
            &mut canvas,
            &tc,
            &line2,
            14.0,
            WIN_H as f32 - BAR_H + 38.0,
            (90, 200, 255),
            16.0,
        )?;

        canvas.present();
        std::thread::sleep(std::time::Duration::from_millis(16));
    }
    Ok(())
}

fn open_wzl(dir: &Path, lib: &str) -> Result<Wzl, Box<dyn Error>> {
    Wzl::open(dir.join(lib)).map_err(|e| format!("打开 {lib}: {e}").into())
}

/// 一张图的描述行（悬停/选中通用）。
fn desc(wzl: &Wzl, i: usize, tag: &str) -> String {
    match wzl.decode(i) {
        Some(sp) => format!(
            "{tag} ID {i}   {w}×{h}  锚点(ax {ax}, ay {ay})",
            w = sp.width,
            h = sp.height,
            ax = sp.anchor_x,
            ay = sp.anchor_y
        ),
        None => format!("{tag} ID {i}  （取不到）"),
    }
}

/// 复制到剪贴板；`full` ⇒ `库名[ID]`，否则纯数字。返回复制的内容（提示用）。
fn copy(clip: &ClipboardUtil, id: usize, lib: &str, full: bool) -> String {
    let s = if full {
        format!("{lib}[{id}]")
    } else {
        id.to_string()
    };
    let _ = clip.set_clipboard_text(&s);
    s
}

/// 画网格：只为**当前页**解码并建纹理（图库可能上千张，全解码又慢又占内存）。
#[allow(clippy::too_many_arguments)]
fn draw_grid<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    wzl: &Wzl,
    scroll: usize,
    hover: Option<usize>,
    selected: Option<usize>,
    cache: &mut HashMap<usize, Texture<'a>>,
) -> Result<(), sdl3::Error> {
    let total_cells = cols() * rows();
    for cell in 0..total_cells {
        let idx = scroll + cell;
        let r = cell_rect(cell);
        canvas.set_draw_color(if cell % 2 == 0 {
            Color::RGB(44, 44, 50)
        } else {
            Color::RGB(50, 50, 58)
        });
        let _ = canvas.fill_rect(r);
        if idx >= wzl.len() {
            continue;
        }

        #[allow(clippy::map_entry)]
        if !cache.contains_key(&idx) {
            if let Some(sp) = wzl.decode(idx) {
                if !sp.is_empty() {
                    if let Ok(mut tex) = tc.create_texture(
                        PixelFormat::RGBA32,
                        TextureAccess::Static,
                        sp.width as u32,
                        sp.height as u32,
                    ) {
                        tex.set_blend_mode(BlendMode::Blend);
                        tex.set_scale_mode(ScaleMode::Nearest);
                        if tex
                            .update(None::<sdl3::rect::Rect>, &sp.rgba, sp.width as usize * 4)
                            .is_ok()
                        {
                            cache.insert(idx, tex);
                        }
                    }
                }
            }
        }
        if let Some(tex) = cache.get(&idx) {
            let q = tex.query();
            let scale = (THUMB / q.width as f32)
                .min(THUMB / q.height as f32)
                .min(1.0);
            let w = q.width as f32 * scale;
            let h = q.height as f32 * scale;
            let dst = FRect::new(r.x + (CELL - w) / 2.0, r.y + (CELL - h) / 2.0, w, h);
            canvas.copy(tex, None::<FRect>, dst)?;
        }

        // 图号角标
        canvas.set_draw_color(Color::RGB(120, 120, 130));
        let _ = canvas.draw_debug_text(
            &idx.to_string(),
            sdl3::render::FPoint::new(r.x + 3.0, r.y + 2.0),
        );

        if Some(idx) == selected {
            canvas.set_draw_color(Color::RGB(70, 160, 255));
            let _ = canvas.draw_rect(r);
            let _ = canvas.draw_rect(FRect::new(r.x + 1.0, r.y + 1.0, r.w - 2.0, r.h - 2.0));
        } else if Some(idx) == hover {
            canvas.set_draw_color(Color::RGB(255, 210, 90));
            let _ = canvas.draw_rect(r);
        }
    }
    Ok(())
}

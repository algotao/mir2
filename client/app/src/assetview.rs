//! **素材观察器**（内嵌在客户端里：`mir2-app --assetview [库名]`）。
//!
//! 把 `mir2c/data` 下的 `.wzl` 图库摊成缩略图：鼠标指向哪张、点一下 ⇒
//! 窗口最下方显示它的 **ID / 尺寸 / 锚点**，并**复制到剪贴板**。
//!
//! # 键位（**没有滚轮**：翻页用键盘，用户 2026-10-11「滚轮容易误操作」）
//! - `PageDown`/`PageUp`（或 `↓`/`↑`）翻页 / 翻一行；`Home`/`End` 到头/到尾
//! - `[` `]`（或 `,` `.`）切换图库
//! - 点图 ⇒ 选中并复制 ID；`C` 复制数字、`Shift+C` 复制 `库名[ID]`
//! - `Esc` / `Q` 退出

use std::collections::HashMap;
use std::error::Error;
use std::path::Path;

use sdl3::clipboard::ClipboardUtil;
use sdl3::event::Event;
use sdl3::keyboard::{Keycode, Mod};
use sdl3::pixels::Color;
use sdl3::pixels::PixelFormat;
use sdl3::render::{
    BlendMode, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use mir2_core::wzl::Wzl;

use crate::font::TextCache;

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

pub(crate) fn list_libs(dir: &Path) -> Vec<String> {
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

pub(crate) fn run(dir: &Path, libs: &[String], start_idx: usize) -> Result<(), Box<dyn Error>> {
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
    let mut text = TextCache::new(mir2_core::text::UI_PX);
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
                // ⚠️ **故意不接滚轮**：翻页一律用键盘（用户 2026-10-11：
                // 「素材预览不要支持鼠标滚动，这很容易误操作」）。
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
                            // 再点同一张 = 取消选中（关掉下面那张原始大小预览）
                            if selected == Some(idx) {
                                selected = None;
                            } else {
                                selected = Some(idx);
                                copied = copy(&clipboard, idx, &libs[lib_idx], false);
                            }
                        }
                    }
                }
                Event::KeyDown {
                    keycode, keymod, ..
                } => {
                    let shift = keymod.contains(Mod::LSHIFTMOD) || keymod.contains(Mod::RSHIFTMOD);
                    match keycode {
                        Some(Keycode::Escape) | Some(Keycode::Q) => {
                            if selected.is_some() {
                                selected = None; // 先收预览，再谈退出
                            } else {
                                running = false;
                                break;
                            }
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
                        Some(Keycode::Down) => {
                            scroll += cols();
                            scroll_changed = true;
                        }
                        Some(Keycode::Up) => {
                            scroll = scroll.saturating_sub(cols());
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

        // **原始大小预览**（用户 2026-10-11 第 5 条）：缩略图看不清大图 ⇒
        // 点中的那张按**原始尺寸**浮在一块深底板上（放不下才等比缩小并标注缩放）。
        draw_preview(&mut canvas, &tc, &wzl, selected, &mut tex_cache)?;

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
            Some((0, 0, 0)),
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
            Some((0, 0, 0)),
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

/// **原始大小预览**：选中那张图按原生尺寸浮在深底板上（网格之上、信息栏之下）。
///
/// 比窗口还大的图（比如 640×473 的地图块）等比缩小到放得下，并在角上标注缩放比
/// —— 缩略图看不清细节，这才是"点开看"的意义（用户 2026-10-11 第 5 条）。
fn draw_preview<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    wzl: &Wzl,
    selected: Option<usize>,
    cache: &mut HashMap<usize, Texture<'a>>,
) -> Result<(), sdl3::Error> {
    let Some(idx) = selected else {
        return Ok(());
    };
    if idx >= wzl.len() {
        return Ok(());
    }
    // 复用网格的纹理缓存（里面存的就是**原生尺寸**的纹理）；
    // 不在当前页（被清掉了）就重建一次。
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
    let Some(tex) = cache.get(&idx) else {
        return Ok(());
    };
    let q = tex.query();
    let (iw, ih) = (q.width as f32, q.height as f32);
    if iw <= 0.0 || ih <= 0.0 {
        return Ok(());
    }

    // 可用区：信息栏之上留 12px 边距
    let avail_w = WIN_W as f32 - 24.0;
    let avail_h = WIN_H as f32 - BAR_H - 24.0;
    let scale = (avail_w / iw).min(avail_h / ih).min(1.0);
    let (w, h) = (iw * scale, ih * scale);
    let pad = 10.0;
    let pw = w + pad * 2.0;
    let ph = h + pad * 2.0;
    // 水平居中、贴着信息栏上方
    let px = (WIN_W as f32 - pw) / 2.0;
    let py = WIN_H as f32 - BAR_H - ph - 8.0;

    canvas.set_blend_mode(BlendMode::Blend);
    canvas.set_draw_color(Color::RGBA(12, 12, 16, 242));
    canvas.fill_rect(FRect::new(px, py, pw, ph))?;
    canvas.set_draw_color(Color::RGB(70, 160, 255));
    canvas.draw_rect(FRect::new(px, py, pw, ph))?;
    canvas.set_blend_mode(BlendMode::None);
    canvas.copy(tex, None::<FRect>, FRect::new(px + pad, py + pad, w, h))?;

    // 缩放比（只有真被缩小了才标）
    if scale < 1.0 {
        canvas.set_draw_color(Color::RGB(255, 210, 90));
        let _ = canvas.draw_debug_text(
            &format!("缩放 {:.0}%", scale * 100.0),
            sdl3::render::FPoint::new(px + 6.0, py + 4.0),
        );
    }
    Ok(())
}

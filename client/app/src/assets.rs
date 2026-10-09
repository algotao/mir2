//! 素材容器入口（`open_lib`）与 F3 素材浏览器。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::path::{Path, PathBuf};
use std::time::Instant;

use mir2_core::wzl::Wzl;

use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
use sdl3::render::{
    BlendMode, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use crate::colors::{C_DIM, C_ERR, C_OK, C_PANEL_BORDER, C_TEXT, C_TITLE};
use crate::gfx::{center_x, checkerboard, frame, text, trunc};
use crate::layout::INFO_LINE_H;
use crate::window::WIN_W;

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

/// 登录模式可浏览的图库。
pub(crate) const LIBS: &[&str] = &[
    "Prguse", "Hum", "Items", "Mon1", "Tiles", "Magic", "ChrSel", "Effect", "Weapon",
];

// ---------- 配色 ----------

/// 素材浏览器（**开发用**，`F3` 进出）：任取一个容器里的第 N 张图，看它解码成什么样。
///
/// 它原先挤在登录页右侧 —— 那让"照原版的登录界面"没法做（一屏两件事）。
/// 现在独立成一屏：`[` `]` 换容器、`,` `.` 换图号。
#[allow(clippy::too_many_arguments)] // 都是渲染所需的最小上下文，与 draw_login_view 同理
pub(crate) fn draw_asset_view<'a, T>(
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

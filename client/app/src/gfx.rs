//! 基础绘制工具（实心框、边框、8×8 调试字、棋盘底、矩形相交）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use sdl3::pixels::Color;
use sdl3::render::{FPoint, FRect, WindowCanvas};

use crate::colors::{C_CHECKER_A, C_CHECKER_B};
use crate::layout::{BAR_TOP, VIEW_H};
use crate::window::WIN_W;

pub(crate) fn fill(
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

pub(crate) fn frame(
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

pub(crate) fn text(
    c: &mut WindowCanvas,
    s: &str,
    x: f32,
    y: f32,
    col: Color,
) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_debug_text(s, FPoint::new(x, y))
}

pub(crate) fn center_x(s: &str, area_x: f32, area_w: f32) -> f32 {
    area_x + (area_w - s.chars().count() as f32 * 8.0) / 2.0
}

pub(crate) fn trunc(s: &str, cols: usize) -> String {
    s.chars().take(cols).collect()
}

/// 在预览面板里画棋盘格底（证明透明区真的透明）。
pub(crate) fn checkerboard(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
) -> Result<(), sdl3::Error> {
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

/// 地图视口矩形（与 `draw_map_view` 里 `set_clip_rect` 用的是同一块）。
pub(crate) fn viewport_rect() -> FRect {
    FRect::new(0.0, BAR_TOP, WIN_W as f32, VIEW_H)
}

/// 两个矩形是否相交（半个像素也不相交就返回 false ⇒ 可安全跳过）。
pub(crate) fn intersects(a: &FRect, b: &FRect) -> bool {
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
}

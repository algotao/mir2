//! 调试可视化：图层开关、格网叠加层、绘制清单 dump、"点哪读哪"探针。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::HashMap;

use mir2_core::map::{Layer, TileDraw, UNIT_X, UNIT_Y};

use sdl3::pixels::Color;
use sdl3::render::{FPoint, FRect, WindowCanvas};

use crate::colors::{
    C_CELLBASE, C_CROSS, C_GRID, C_GRID_FRONT, C_GRID_GROUND, C_GRID_MID, C_TOPMOST,
};
use crate::geom::{cam_parts, cell_to_screen, screen_to_cell};
use crate::gfx::{fill, text, trunc};
use crate::layout::{BAR_TOP, TEXT_COLS, VIEW_H};
use crate::tiles::{rect_of, TileKey, TileTex};
use crate::window::WIN_W;

/// `CTRL+1/2/3`（以及 `L` 循环）逐层显隐。
pub(crate) const DEBUG_LAYERS: bool = false;

/// `D` 辅助线与格子坐标叠加层。
pub(crate) const DEBUG_OVERLAY: bool = false;

/// 掩码 → 三字母缩写（G=地表 M=中间 F=前景），隐藏的层显示为 `-`。
pub(crate) fn layers_desc(m: u8) -> String {
    let ch = |bit: u8, on: char| if m & bit != 0 { on } else { '-' };
    format!("{}{}{}", ch(1, 'G'), ch(2, 'M'), ch(4, 'F'))
}

// ---------- 绘制辅助 ----------

/// 调试叠加层：格网 + 各层落点框 + 鼠标十字线与"点哪读哪"的读数。
///
/// 关键辅助：前景图块额外画一条**格的底边黄线** —— 官方规则是"底边对齐格底"，
/// 有这条线就能一眼看出对齐对不对（而不是靠猜）。
pub(crate) fn draw_debug_overlay(
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

/// 把视口内的绘制清单打到终端（顺序即绘制顺序）——可复制的 debug log。
pub(crate) fn dump_draws(
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
pub(crate) fn probe_at(
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

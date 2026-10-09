//! 小地图 / 大地图。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use sdl3::render::{FRect, TextureCreator, WindowCanvas};

use crate::colors::C_SELF_DOT;
use crate::gfx::fill;

use crate::ui;

/// 小地图在屏幕右上角的边长（原版就是 120×120，`PlayScn.pas:815-817`）。
pub(crate) const MINIMAP_PX: f32 = 120.0;

/// 大地图的**放大倍数**。
///
/// 取 2 的理由：缩略图（mmap）一张约 540×360（原版把整张塞进 540×360 的
/// `g_MiniMapSurface`，`ClMain.pas:1030`），1 倍时整张图还没窗口大 ⇒ 没得滚动，
/// "跟着人物滑"就看不出来。2 倍 ⇒ 1080×720 > 窗口 ⇒ 站在图中间看得见四周，
/// 跑起来图会滑（这正是用户要的手感）。
pub(crate) const BIGMAP_ZOOM: f32 = 2.0;

/// 格坐标 → 缩略图像素（原版 `PlayScn.pas:808-813`：
/// `mx := (x*48) div 32`、`my := (y*32) div 32` ⇒ **X 是 1.5 倍、Y 是 1 倍**）。
///
/// 这个不对称的宽高比是**原版就有的**（地图格在缩略图上不是方的），不是笔误。
/// 收 `f32` 是为了能吃**补间中的小数格**（否则图会一格一格跳，见 `self_render_pos`）。
pub(crate) fn minimap_point(px: f32, py: f32) -> (f32, f32) {
    (px * 48.0 / 32.0, py)
}

/// 以小地图上的 `(cx, cy)` 为中心取 `size` 见方的**源裁剪框**，夹在图内。
///
/// 返回 `(x, y, w, h)`：贴边时四个数都会被夹（不许露出图外 —— 原版用的是
/// `_MAX(0, mx-60)` + `_MIN(图宽, left+120)`，同一件事）。
pub(crate) fn minimap_crop(cx: f32, cy: f32, img: (u32, u32), size: f32) -> (f32, f32, f32, f32) {
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
pub(crate) fn bigmap_dst(
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
pub(crate) fn draw_minimaps<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    asset_dir: &Option<std::path::PathBuf>,
    // 画区域标注用的字体（原版小地图上就带"银杏山谷/边界村/店铺"这些字）
    names: &mut crate::font::TextCache<'a>,
    // `(小地图图号, **补间后**的自己位置, 地图显示名)` —— 位置见 `self_render_pos`；
    // 显示名用来查 `core::map_labels`（那张表的键就是它，见生成器的说明）
    world: Option<(u32, (f32, f32), &str)>,
    minimap_on: bool,
    bigmap_on: bool,
    win: (u32, u32),
) -> Result<(), sdl3::Error> {
    if !minimap_on && !bigmap_on {
        return Ok(());
    }
    let (Some(dir), Some((idx, pos, map_title))) = (asset_dir.as_ref(), world) else {
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
        // 区域标注（用户 2026-10-09 选的 (a)）：原版小地图上就带这些字 ——
        // `data/MapDesc1.dat` 的 182 条，位置是**格坐标** ⇒ 与图心同一套换算
        // （`minimap_point`），落在 120×120 裁剪框外的直接不画。
        //
        // ⚠️ 裁剪：SDL 的 `copy` 不认"超出小地图那一块"，所以画字之前先设 clip ——
        // 不设的话标签会糊到右边的游戏画面上。
        canvas.set_clip_rect(Some(sdl3::rect::Rect::new(
            dx as i32,
            dy as i32,
            MINIMAP_PX as u32,
            MINIMAP_PX as u32,
        )));
        for l in mir2_core::map_labels::labels_for(map_title) {
            let (_, lx, ly, text, rgb) = *l;
            let (px, py) = minimap_point(lx as f32, ly as f32);
            if px < sx || px > sx + sw || py < sy || py > sy + sh {
                continue;
            }
            names.draw(
                canvas,
                tc,
                text,
                dx + (px - sx),
                dy + (py - sy),
                (((rgb >> 16) & 0xFF) as u8, ((rgb >> 8) & 0xFF) as u8, (rgb & 0xFF) as u8),
                Some((0, 0, 0)),
            )?;
        }
        canvas.set_clip_rect(None);

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

//! 地图图块：纹理缓存、落点、视口剔除（含亚格偏移，D-46 那条黑带就出在这）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::HashMap;
use std::path::Path;

use mir2_core::map::{Lib, TileDraw};
use mir2_core::wzl::Wzl;

use sdl3::pixels::PixelFormat;
use sdl3::rect::Rect;
use sdl3::render::{
    BlendMode, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use crate::assets::open_lib;
use crate::geom::CamParts;
use crate::gfx::intersects;
use crate::layout::BAR_TOP;

/// 图块纹理缓存上限；超出就整批丢掉重建（开发期查看器，够用且简单）。
pub(crate) const TILE_CACHE_CAP: usize = 4000;

/// 图块缓存键：图库 + `Objects` 的编号 + 图号 + 是否混合。
///
/// 末位是必需的：同一张图在"混合"与"不混合"两种画法下**上传的像素数据不同**
/// （混合件要换成 [`mir2_core::blend::screen_source`] 的 SCREEN 源），
/// 少了它就会把两种画法互相串味。
pub(crate) type TileKey = (Lib, u8, u16, bool);

/// 缓存的一张图块纹理 + 它的锚点（**Alpha 物件**要用锚点定位，见 core 的 `TileDraw`）。
pub(crate) struct TileTex<'a> {
    pub(crate) tex: Texture<'a>,
    pub(crate) anchor_x: i16,
    pub(crate) anchor_y: i16,
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
pub(crate) fn set_texture_blend(tex: &mut Texture<'_>, screen: bool) {
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
pub(crate) fn ensure_tile<'a, T>(
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
pub(crate) fn draw_tile<'a, T>(
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

/// [`rect_of`] 的"冷"版本：**只读 WZL 记录、不解码像素**。
///
/// 用途：前景层按官方要向下多扫 35 行（`core::map::FRONT_ROW_MARGIN`），
/// 那批候选里绝大多数是矮图块、落点远在视口下方。先按记录把框算出来判掉，
/// 就不必为它们做 zlib 解压 + RGBA 转换 + 贴图上传。
///
/// 与 [`rect_of`] 不会打架：两者都用 core 的 `top_y` / `left_x` 定位，
/// 只是尺寸一个取自贴图、一个取自记录（两者必然相同，解码器就按记录建图）。
pub(crate) fn draw_rect_cold(
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
pub(crate) fn rect_of(
    d: &TileDraw,
    tiles: &HashMap<TileKey, TileTex<'_>>,
    sub: (f32, f32),
) -> Option<FRect> {
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
pub(crate) fn tile_in_view(
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

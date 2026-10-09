//! 界面素材（`Prguse` / `ChrSel` 这类**界面容器**）的纹理缓存。
//!
//! 与地图图块缓存、actor 精灵缓存分开，是因为键与用法都不同：
//!
//! - 图块：`(Lib 枚举, 区域, 编号)`，几千张、按视口取；
//! - 精灵：`(容器名, 图号)`，图号**算出来**（`core::actor`）；
//! - 界面：`(容器名, 图号)`，图号是**原版写死的常量**（`Prguse[60]` 就是登录框），
//!   一屏几张，而且**必须能在画之前拿到尺寸**（版式要按它居中/摆按钮）。
//!
//! ⚠️ 尺寸走 `Wzl::record`（只读头部，**不解压像素**）—— 版式计算每帧都要用，
//! 走解压就白解码一整张图。

use std::collections::HashMap;
use std::path::Path;

use sdl3::pixels::PixelFormat;
use sdl3::rect::Rect;
use sdl3::render::{
    BlendMode, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use mir2_core::wzl::{BBox, Wzl};

/// 缓存上限（界面素材用量小，越界直接清空）。
const UI_CACHE_CAP: usize = 256;

/// **设计空间 → 画布**的缩放：登录/选角那套界面素材原生是 **800×600**
///（实测 `ChrSel[22]` = 800×600、`Prguse[65]` = 800×600；本套素材里**没有** 1024 版，
/// `Prguse[2]` 在 D-50 里量过是空壳），而画布是 [`crate::WIN_W`]×[`crate::WIN_H`]（1024×768）。
///
/// # 为什么是"拉伸"而不是"居中 1:1 + 补边"（2026-10-09 改，D-52）
///
/// 官方客户端（用户给的 `SQ 0.1.183.1963` 选角截图）**就是把 800×600 整屏拉伸 1.28 倍铺满**
/// 1024×768 的 —— 实测：截图里两块凹槽落在**拉伸**预测的位置（117..483 / 539..928），
/// 而不是"居中 1:1"的位置（204..489 / 533..837）。
///
/// 原来的做法（D-51）是 1:1 居中、四周拿素材自己的石纹平铺补边；那是**照原版 1.76**
///（`FState.pas:934-953` / `IntroScn.pas:886-889` 全是 `(SCREENWIDTH-800) div 2 + x`，
/// **不缩放**）。现代官方改了，用户要求跟现代官方（并明确"拉伸是最优解"）。
///
/// ⚠️ **只拉伸美术，不拉伸字**：字走 `font::TextCache`（TTF 光栅化），
/// 按 `UI_PX * UI_SCALE` **重新光栅化** ⇒ 清晰（用户第 2 条：字体要"缩放"不要"拉伸"）。
/// 位图字体在 1.28 倍下只能被拉糊，那正是 D-50 用户明确不喜欢的东西。
pub const UI_SCALE: f32 = crate::WIN_W as f32 / 800.0;

/// 设计空间（800×600）里的一个坐标/长度 → 画布坐标。
///
/// 登录/选角的**所有**绘制分成两半，这一半给"直接调 canvas 的"（自绘输入框、
/// 8×8 调试字体、`font::TextCache::draw_ui` 内部也用它）；另一半是 `UiCache::draw*`
/// —— 它内部自己乘 [`UI_SCALE`]，调用方照样传设计坐标。
#[inline]
pub fn ui_px(v: f32) -> f32 {
    v * UI_SCALE
}

/// 同上，点。
#[inline]
pub fn ui_pt(p: (f32, f32)) -> (f32, f32) {
    (p.0 * UI_SCALE, p.1 * UI_SCALE)
}

/// 画布坐标 → 设计空间（鼠标事件用的那个方向；见 [`UI_SCALE`]）。
#[inline]
pub fn ui_inv_pt(p: (f32, f32)) -> (f32, f32) {
    (p.0 / UI_SCALE, p.1 / UI_SCALE)
}

/// 画布长度 → 设计空间长度（**字宽/行高是画布像素**，要挪进设计坐标的算式里时用它）。
#[inline]
pub fn ui_inv_px(v: f32) -> f32 {
    v / UI_SCALE
}

/// **设计空间**的实心矩形（登录/选角里那些自绘的输入框、建角框）。
///
/// ⚠️ 它和 [`crate::fill`] 的分工得记牢：`fill` 是**画布坐标**（世界 HUD 也在用），
/// `uifill` 收的是 800×600 的设计坐标。两者长得一样、错了不报错，只是画面偏 1.28 倍。
pub fn uifill(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    col: sdl3::pixels::Color,
) -> Result<(), sdl3::Error> {
    crate::fill(c, ui_px(x), ui_px(y), ui_px(w), ui_px(h), col)
}

struct UiTex<'a> {
    tex: Texture<'a>,
}

/// 界面素材缓存。`lib + 图号 → 纹理`。
pub struct UiCache<'a> {
    libs: HashMap<&'static str, Option<Wzl>>,
    texs: HashMap<(&'static str, u32), UiTex<'a>>,
    /// 不透明包围盒缓存（键与纹理同）。选角的小人要按它"按内容对齐"。
    bboxes: HashMap<(&'static str, u32), Option<BBox>>,
}

impl<'a> UiCache<'a> {
    pub fn new() -> Self {
        Self {
            libs: HashMap::new(),
            texs: HashMap::new(),
            bboxes: HashMap::new(),
        }
    }

    fn lib(&mut self, dir: &Path, name: &'static str) -> Option<&Wzl> {
        self.libs
            .entry(name)
            .or_insert_with(|| crate::open_lib(dir, name))
            .as_ref()
    }

    /// 某张界面图的尺寸（**只读容器记录，不解压**）。取不到返回 `None`。
    ///
    /// 版式计算靠它：对话框要居中、按钮要摆进框内、都会在画之前先问尺寸。
    pub fn size(&mut self, dir: &Path, lib: &'static str, idx: u32) -> Option<(u32, u32)> {
        let w = self.lib(dir, lib)?;
        let r = w.record(idx as usize)?;
        Some((r.width as u32, r.height as u32))
    }

    /// 一张界面图的**不透明包围盒**（图内坐标）。取不到/全透明 ⇒ `None`。
    ///
    /// 选角界面靠它把小人的**不透明部分**对齐到凹槽上（见 `Sprite::alpha_bbox`）。
    pub fn bbox(
        &mut self,
        dir: &Path,
        lib: &'static str,
        idx: u32,
    ) -> Option<(i32, i32, u32, u32)> {
        if let Some(b) = self.bboxes.get(&(lib, idx)) {
            return *b;
        }
        let b = self.lib(dir, lib)?.decode(idx as usize)?.alpha_bbox();
        self.bboxes.insert((lib, idx), b);
        b
    }

    /// 画一张界面图（不调色）。返回它的尺寸（画不出来 = 素材缺/空壳，返回 `None`）。
    #[allow(clippy::too_many_arguments)]
    pub fn draw<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        dir: &Path,
        lib: &'static str,
        idx: u32,
        x: f32,
        y: f32,
    ) -> Option<(u32, u32)> {
        self.draw_tint(canvas, tc, dir, lib, idx, x, y, (255, 255, 255))
    }

    /// 画一张界面图 + **调色**（选角界面靠它把未选中的小人压暗；
    /// 原版是 `MakeDark`，见 `core::select_ui` 的说明）。
    ///
    /// ⚠️ 每次 `copy` 前都**重设**颜色：`set_color_mod` 是**粘在纹理上**的，
    /// 被调过色的图下一次不重设就会沿用旧颜色 —— 同一张图在两处颜色不同时就露馅。
    #[allow(clippy::too_many_arguments)]
    pub fn draw_tint<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        dir: &Path,
        lib: &'static str,
        idx: u32,
        x: f32,
        y: f32,
        tint: (u8, u8, u8),
    ) -> Option<(u32, u32)> {
        self.ensure(dir, lib, idx, tc)?;
        let t = self.texs.get_mut(&(lib, idx))?;
        let q = t.tex.query();
        // ⚠️ 贴之前一定要重设颜色（见上面那条：不设就会沿用上一次的）；
        // 透明度同理 —— `draw_src` 会给大地图设 140，没复位就会让后来贴的同一张图变半透明。
        t.tex.set_color_mod(tint.0, tint.1, tint.2);
        t.tex.set_alpha_mod(255);
        // 界面这条路是**放大**（1.28 倍，见 [`UI_SCALE`]）⇒ 用线性采样：
        // 最近邻在非整数倍下会"有的像素宽、有的像素窄"（D-50 用户报的"又糊又大"就是它）；
        // 线性在 1:1（UI_SCALE == 1）时与最近邻逐像素等价。
        t.tex.set_scale_mode(ScaleMode::Linear);
        canvas
            .copy(
                &t.tex,
                None::<FRect>,
                // 设计空间 → 画布：**位置和尺寸都乘**（800×600 的图铺到 1024×768 上）。
                // 缩放本身交给 GPU（见 `ensure` 里设的 `ScaleMode::Linear`），
                // 字不在这里 —— 字走 `TextCache::draw_ui`，按目标字号**重新光栅化**。
                FRect::new(
                    ui_px(x),
                    ui_px(y),
                    ui_px(q.width as f32),
                    ui_px(q.height as f32),
                ),
            )
            .ok()?;
        Some((q.width, q.height))
    }

    /// 界面用的**裁剪贴**：`src` 与 `dst` 都收**设计空间**坐标（`dst` 内部乘 [`UI_SCALE`]，
    /// `src` 是源图自己的像素 —— 那本来就是设计像素）。
    ///
    /// 用途：把"只存在别处一小块"的美术搬过来 —— 现在只有一处：「开始」那颗石台
    ///（在 `Prguse2[480]` 里烘着，1.76 底图没有；见 `core::select_ui::START_PLATE`）。
    ///
    /// ⚠️ 与世界的 [`UiCache::draw_src`] 分开：那个收**画布**坐标、贴的是地图缩略图，
    /// 而且不设采样模式（缩略图那套要最近邻）。这里必须**线性**——放大 1.28 倍，
    /// 最近邻会抽出"有的像素宽有的窄"（见 [`draw_tint`] 里那条）。
    #[allow(clippy::too_many_arguments)] // 画布+图号+源矩形+目标矩形，与 `draw_src` 同一情况
    pub fn draw_ui_src<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        dir: &Path,
        lib: &'static str,
        idx: u32,
        src: (f32, f32, f32, f32),
        dst: (f32, f32, f32, f32),
    ) -> Option<()> {
        self.ensure(dir, lib, idx, tc)?;
        let t = self.texs.get_mut(&(lib, idx))?;
        // 与 `draw_tint` 同一条纪律：颜色/透明度**每次都要重设**（它们粘在纹理上）
        t.tex.set_color_mod(255, 255, 255);
        t.tex.set_alpha_mod(255);
        t.tex.set_scale_mode(ScaleMode::Linear);
        canvas
            .copy(
                &t.tex,
                Some(FRect::new(src.0, src.1, src.2, src.3)),
                Some(FRect::new(
                    ui_px(dst.0),
                    ui_px(dst.1),
                    ui_px(dst.2),
                    ui_px(dst.3),
                )),
            )
            .ok()?;
        Some(())
    }

    /// 保证 `(lib, idx)` 那张图已经进了缓存（`draw_tint` / `draw_src` 共用）。
    ///
    /// 抽出来的理由：两处各写一遍必然漂移 —— 缓存上限、混合/缩放模式、颜色复位
    /// 这几条必须完全一致，而它们**都不会报错**，只会让画面在某张图上悄悄不对。
    fn ensure<T>(
        &mut self,
        dir: &Path,
        lib: &'static str,
        idx: u32,
        tc: &'a TextureCreator<T>,
    ) -> Option<()> {
        if self.texs.contains_key(&(lib, idx)) {
            return Some(());
        }
        if self.texs.len() >= UI_CACHE_CAP {
            self.texs.clear();
        }
        let s = self.lib(dir, lib)?.decode(idx as usize)?;
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
        self.texs.insert((lib, idx), UiTex { tex: t });
        Some(())
    }

    /// 取一张图，按**源矩形裁剪 + 缩放**贴到目标矩形上（小地图 / 大地图要用）。
    ///
    /// 与 `draw_tint` 的分工：那个是"整图 1:1 + 调色"（界面素材那套素材），
    /// 这个是"裁剪 / 缩放"（地图缩略图那套）。**别拿它替代界面绘制** ——
    /// 调色那条纪律（见 `draw_tint` 上面）还得走 `draw_tint`。
    ///
    /// 返回**原图**尺寸：调用方要先按它算裁剪框（见 app 的 `minimap_crop`）。
    ///
    /// `alpha` 是整张图的透明度（255 = 不透明）—— 大地图铺满整屏，要半透明才看得见
    /// 脚下的世界。⚠️ 与颜色一样，**每次都得重设**（上一处调过就会沿用）。
    #[allow(clippy::too_many_arguments)]
    pub fn draw_src<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        dir: &Path,
        lib: &'static str,
        idx: u32,
        src: FRect,
        dst: FRect,
        alpha: u8,
    ) -> Option<(u32, u32)> {
        self.ensure(dir, lib, idx, tc)?;
        let t = self.texs.get_mut(&(lib, idx))?;
        // 贴之前把颜色/透明度**都复位**：上一处可能给它调过（见 `draw_tint` 的说明）
        t.tex.set_color_mod(255, 255, 255);
        t.tex.set_alpha_mod(alpha);
        let q = t.tex.query();
        canvas.copy(&t.tex, Some(src), Some(dst)).ok()?;
        Some((q.width, q.height))
    }
}

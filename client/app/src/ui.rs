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
        // ⚠️ 贴之前一定要重设颜色（见上面那条：不设就会沿用上一次的）
        t.tex.set_color_mod(tint.0, tint.1, tint.2);
        canvas
            .copy(
                &t.tex,
                None::<FRect>,
                FRect::new(x, y, q.width as f32, q.height as f32),
            )
            .ok()?;
        Some((q.width, q.height))
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
    ) -> Option<(u32, u32)> {
        self.ensure(dir, lib, idx, tc)?;
        let t = self.texs.get_mut(&(lib, idx))?;
        // 贴之前把颜色**复位**：上一处可能给它调过色（见 `draw_tint` 的说明）
        t.tex.set_color_mod(255, 255, 255);
        let q = t.tex.query();
        canvas.copy(&t.tex, Some(src), Some(dst)).ok()?;
        Some((q.width, q.height))
    }
}

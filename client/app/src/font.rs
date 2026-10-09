//! 屏幕文字（**真字体**）的纹理缓存与绘制。
//!
//! 光栅化与量宽在 `mir2_core::text`（纯计算，core 里没有 SDL）；
//! 这里只管"字形覆盖度 → SDL 纹理 → 画上去"，以及**找不到字体时的降级**。
//!
//! # 三个值得说明的实现细节
//!
//! 1. **字形纹理存白色，颜色靠 `set_color_mod` 打**。字形只有"覆盖度"（8 位 alpha），
//!    颜色是绘制时的事 ⇒ 一个字（一个字号）只要一张纹理，白字、黑描边、
//!    变暗全都用同一张（比按颜色缓存省得多）。
//!    ⚠️ `set_color_mod` 是**粘在纹理上**的，所以每次 `copy` 前都要设一遍 ——
//!    漏设会让后画的字沿用上一次的颜色，看着像"随机变色"，很难查。
//! 2. **描边 = 错开 1px 再画一遍黑的**，顺序与配色照原版：
//!    `BoldTextOut(surface, x, y, clWhite, clBlack, s)`（`ClFunc.pas:605`）。
//! 3. **降级**：找不到字体就退回 SDL3 的 8×8 调试字体（画不出中文，但不会崩、
//!    也不会一片空白 —— 那种症状最难查）。

use std::collections::HashMap;

use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
use sdl3::render::{
    BlendMode, FPoint, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};

use mir2_core::text::{Face, Glyph};

/// 字形纹理缓存上限（一屏中文也就几十个字；越界直接清空）。
const GLYPH_CAP: usize = 512;

/// 颜色三元组。
pub type Rgb = (u8, u8, u8);

struct GlyphTex<'a> {
    tex: Texture<'a>,
    /// 位图尺寸（0 = 空白字形，不画）。
    w: f32,
    h: f32,
}

/// 文字绘制器：持有字体 + 字形纹理缓存。
pub struct TextCache<'a> {
    face: Option<Face>,
    px: f32,
    texs: HashMap<(char, u32), GlyphTex<'a>>,
    /// 累计画了多少串（调试/冒烟时看它就知道有没有在画字）。
    pub lines: u64,
}

impl<'a> TextCache<'a> {
    /// 建一个（去找字体；找不到就降级，并在终端**说清楚**用了哪个）。
    pub fn new(px: f32) -> Self {
        let face = Face::load();
        match &face {
            Some(f) => println!("[font] 用字体：{}（{px}px）", f.source()),
            None => println!(
                "[font] 没找到能画汉字的字体 ⇒ 退回 8×8 调试字体（中文画不出来）；\
                 可用 MIR2_FONT=/path/to/宋体.ttc 指定"
            ),
        }
        Self {
            face,
            px,
            texs: HashMap::new(),
            lines: 0,
        }
    }

    #[allow(dead_code)] // 门面：日志/冒烟要看它俩（现在由 `new()` 打印）
    /// 有没有真字体（没有 = 正在用 8×8 降级）。
    pub fn available(&self) -> bool {
        self.face.is_some()
    }

    #[allow(dead_code)] // 门面：日志/冒烟要看它俩（现在由 `new()` 打印）
    /// 用的哪份字体（降级时 `None`）。
    pub fn source(&self) -> Option<&str> {
        self.face.as_ref().map(|f| f.source())
    }

    /// 字符串会有多宽（降级时按 8px/字符估）。
    pub fn width(&self, s: &str) -> f32 {
        match &self.face {
            Some(f) => f.width(s, self.px),
            None => s.chars().count() as f32 * 8.0,
        }
    }

    /// 行高（画多行时步进用）。
    pub fn line_height(&self) -> f32 {
        match &self.face {
            Some(f) => f.metrics(self.px).line,
            None => 8.0,
        }
    }

    /// 画一串字。`(x, y)` 是**左上角** —— 与原版 `BoldTextOut` 的 y 同义
    /// （GDI `TextOut` 的 y 也是"文字的顶"）。
    ///
    /// `shadow` 给了就先在 `(x+1, y+1)` 用那个颜色画一遍（原版白字黑边就这么来）。
    #[allow(clippy::too_many_arguments)] // 与 `ui::UiCache::draw` 同一情况：坐标+配色就是这么多
    pub fn draw<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        x: f32,
        y: f32,
        color: Rgb,
        shadow: Option<Rgb>,
    ) -> Result<(), sdl3::Error> {
        if s.is_empty() {
            return Ok(());
        }
        self.lines += 1;
        if let Some(sc) = shadow {
            self.paint(canvas, tc, s, x + 1.0, y + 1.0, sc)?;
        }
        self.paint(canvas, tc, s, x, y, color)
    }

    /// 居中画（`cx` 是中心，`y` 仍是顶）。
    #[allow(dead_code)] // 门面：居中文字（原版画服务器名就是居中）
    #[allow(clippy::too_many_arguments)]
    pub fn draw_centered<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        cx: f32,
        y: f32,
        color: Rgb,
        shadow: Option<Rgb>,
    ) -> Result<(), sdl3::Error> {
        let x = cx - self.width(s) / 2.0;
        self.draw(canvas, tc, s, x, y, color, shadow)
    }

    /// 画一串字，坐标是**设计空间（800×600）**的 —— 内部乘 [`crate::ui::UI_SCALE`]。
    ///
    /// # 登录/选角为什么单开一个入口（2026-10-09，D-52）
    ///
    /// 那两屏的美术是 800×600、画布是 1024×768 ⇒ 整屏拉伸 1.28（跟官方客户端一样）。
    /// 但**字不能跟着位图一起拉**（用户明确要求"字体应该是缩放侧不是拉伸"）：它的位图
    /// 是 14px 的话，被拉成 17.9px 就是糊的。所以字走**另一条路** ——
    ///
    /// - 建这个 cache 时就按 `UI_PX * UI_SCALE`（≈ 17.9px）**重新光栅化**（清晰）；
    /// - 落点由这里乘 `UI_SCALE` 换算到画布。
    ///
    /// 调用方（`login.rs` / `select.rs`）拿到的是设计坐标（`Layout` / `SlotText` 给的
    /// 都是 800×600 空间的），照原样传进来即可。世界那套（头顶名字、伤害飘字）**不要**
    /// 用这个入口 —— 那是画布坐标，用 [`TextCache::draw`]。
    #[allow(clippy::too_many_arguments)]
    pub fn draw_ui<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        x: f32,
        y: f32,
        color: Rgb,
        shadow: Option<Rgb>,
    ) -> Result<(), sdl3::Error> {
        let (x, y) = crate::ui::ui_pt((x, y));
        self.draw(canvas, tc, s, x, y, color, shadow)
    }

    /// [`TextCache::draw_ui`] 的居中版（`cx` 也是设计坐标；`self.width` 已经按
    /// 光栅化字号算，所以只换算中心点）。
    #[allow(dead_code)] // 门面：与 `draw_centered` 对称（登录/选角暂时没有居中的字）
    #[allow(clippy::too_many_arguments)]
    pub fn draw_ui_centered<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        cx: f32,
        y: f32,
        color: Rgb,
        shadow: Option<Rgb>,
    ) -> Result<(), sdl3::Error> {
        let x = crate::ui::ui_px(cx) - self.width(s) / 2.0;
        let y = crate::ui::ui_px(y);
        self.draw(canvas, tc, s, x, y, color, shadow)
    }

    /// 真正落笔（逐字推进笔位）。
    fn paint<T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        s: &str,
        x: f32,
        y: f32,
        color: Rgb,
    ) -> Result<(), sdl3::Error> {
        let Some(face) = self.face.as_ref() else {
            // 降级：SDL3 的 8×8 调试字体
            canvas.set_draw_color(Color::RGB(color.0, color.1, color.2));
            return canvas.draw_debug_text(s, FPoint::new(x, y));
        };
        // ⚠️ `face` 借的是 `self.face`，`self.texs` 是另一个字段 ——
        // 直接按字段借，借用检查器认它们是两回事（不用绕、更不用 unsafe）。
        let px = self.px;
        let ascent = face.metrics(px).ascent;
        let mut pen = x;
        for ch in s.chars() {
            let g = face.glyph(ch, px);
            ensure_glyph(&mut self.texs, tc, ch, px, &g);
            let top = ascent - (g.ymin as f32 + g.h as f32);
            if let Some(t) = self.texs.get_mut(&(ch, px.to_bits())) {
                if t.w > 0.0 {
                    t.tex.set_color_mod(color.0, color.1, color.2);
                    // ⚠️ 落笔**取整到整像素**：`pen` 是小数累加（`g.advance` 带小数），
                    // 亚像素落点会让 SDL 做线性采样 ⇒ 字发糊。用户 2026-10-09：
                    // 「字体有点糊…请用字号调节，不要使用拉伸渲染」——字号本来就是原生的
                    // （`UI_PX=14`，不再乘 1.28，见 D-64），糊的就是这个小数笔位。
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

/// 确保某个字的纹理在缓存里（没有就现建一张：白色 + 覆盖度当 alpha）。
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
    if texs.len() >= GLYPH_CAP {
        texs.clear();
    }
    if g.w == 0 || g.h == 0 {
        // 空白字形（空格、或字体里没有这个字）：记个空壳，省得每帧再问一次字体
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
    if tex.update(None::<Rect>, &rgba, g.w as usize * 4).is_err() {
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

#[cfg(test)]
mod tests {
    use super::*;

    /// 量宽要成比例（这条不需要 SDL：`TextCache::new` 只读字体文件）。
    ///
    /// ⚠️ 中文在这里是**必须**的：8×8 降级模式下 `width("勇士")` 只会是 16
    /// （按 8px/字符估），而真字体下它约等于两倍字号的宽度。
    #[test]
    fn 量宽成比例() {
        let t = TextCache::new(14.0);
        if !t.available() {
            eprintln!("跳过：本机没找到能画汉字的字体");
            return;
        }
        eprintln!("字体：{:?}", t.source());
        let (w1, w2) = (t.width("勇"), t.width("勇士"));
        assert!(w1 > 8.0, "一个汉字该有 14px 上下宽，实得 {w1}");
        assert!(w2 > w1 * 1.5, "{w2} 与 {w1}：两个字该明显更宽");
        assert!(t.line_height() >= 14.0, "行高 {} 小于字号", t.line_height());
        // 空串不画、也不该有宽度（免得居中算法把它算成 1 个字符宽）
        assert_eq!(t.width(""), 0.0);
    }
}

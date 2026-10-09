//! 屏幕文字：**真字体**（中文要能画）。
//!
//! # 为什么要专门做一层
//!
//! app 原来所有文字都走 SDL3 内置的 8×8 调试字体，**只认 ASCII** ——
//! 角色名（`勇士`）、职业（`战士`）、怪物名、聊天……一个都画不出来。
//! 而原版这些字是 GDI 用**系统字体**画的：
//!
//! ```text
//! BoldTextOut(surface, x, y, clWhite, clBlack, UserChr.Name);   // IntroScn.pas:1519
//! ```
//!
//! 也就是说"用一份字体文件/系统字体画字"**本身就是原版的做法**（`ClFunc.pas:605`
//! 内部就是 `CreateFontIndirect` + `TextOut`），不是我们取巧。
//!
//! # 取哪份字体
//!
//! 与其它素材同一条纪律：**不进仓库**，按顺序找（[`Face::load`]）：
//!
//!   1. `$MIR2_FONT` —— 写死路径（调试 / 部署时指定）
//!   2. 资产目录（`paths::asset_dir()`）里的 `font.ttf` / `font.otf` / `font.ttc`
//!   3. 系统字体 —— macOS 优先**宋体**（`Songti.ttc`，原版就是它），
//!      Linux 找 Noto CJK / 文泉驿，Windows 找 宋体 / 微软雅黑
//!
//! ⚠️ 候选**必须真的能画出汉字**才算数（见 `Face::probe`）：字体集合（`.ttc`）
//! 里第一个 face 未必有中文字形，光"文件能打开"是不够的。
//! 一份都用不了 ⇒ `None` ⇒ 调用方退回 8×8 调试字体（画不了中文，但不会崩）。
//!
//! # 与 `client/app` 的分工
//!
//! 这里只做**光栅化与量宽**（纯计算 —— `core` 里没有 SDL，也就没法建纹理）。
//! 纹理、配色、描边与缓存都在 app（`app::font`）。

/// 界面文字字号（px）。
///
/// ⚠️ 原版是 GDI 的 `宋体 12pt`（`ClFunc.pas:605` 附近），点→像素的换算依赖
/// 屏幕 DPI，**没有逐像素对拍过**。取 14px 是因为在 800×600 的界面素材上
/// 与原版的观感相当（名字/等级/职业三行都放得下）。
pub const UI_PX: f32 = 14.0;

/// **世界里**的字号（怪物/玩家头顶的名字、伤害飘字）。
///
/// 比界面小一档：一个是界面上"三行值"要读得清，一个是 48×32 的格子上顶个名字，
/// 14px 会盖住隔壁格（原版名字也明显小于界面字）。
pub const NAME_PX: f32 = 12.0;

/// 一个已加载的字体（含来源，便于把"用的哪份字体"写进日志/断言里）。
pub struct Face {
    font: fontdue::Font,
    source: String,
}

/// 一个字形光栅化后的结果。
///
/// 坐标系照 fontdue：`xmin`/`ymin` 是相对**笔位基线**的偏移（`ymin` 向上为正），
/// 位图左上角 = `(笔位 + xmin, 基线 - (ymin + h))`。
#[derive(Debug, Clone)]
pub struct Glyph {
    pub w: u32,
    pub h: u32,
    pub xmin: i32,
    pub ymin: i32,
    /// 笔位前进量（下一个字的起点）。
    pub advance: f32,
    /// 8 位覆盖度（`w * h`），当作 alpha 用。
    pub cov: Vec<u8>,
}

impl Glyph {
    /// 空字形（空白字符、或字体里没有这个字）。
    pub fn blank(advance: f32) -> Self {
        Glyph {
            w: 0,
            h: 0,
            xmin: 0,
            ymin: 0,
            advance,
            cov: Vec::new(),
        }
    }
}

/// 行度量（定位用）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct LineMetrics {
    /// 基线到顶（正）。
    pub ascent: f32,
    /// 基线到底（负）。
    pub descent: f32,
    /// 行高（原版无行距概念；单行界面够用）。
    pub line: f32,
}

/// 候选字体：`(路径, .ttc 里的第几个 face)`。
///
/// 顺序就是"照原版"的顺序：原版用宋体 ⇒ macOS 的 `Songti.ttc` 排第一，
/// Windows 的 `simsun.ttc`（宋体）同理。
const CANDIDATES: &[(&str, u32)] = &[
    // macOS
    ("/System/Library/Fonts/Supplemental/Songti.ttc", 0),
    ("/System/Library/Fonts/Supplemental/Arial Unicode.ttf", 0),
    ("/System/Library/Fonts/STHeiti Medium.ttc", 0),
    ("/Library/Fonts/Arial Unicode.ttf", 0),
    // Linux
    ("/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", 0),
    ("/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc", 0),
    ("/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc", 0),
    ("/usr/share/fonts/truetype/arphic/uming.ttc", 0),
    // Windows
    ("C:/Windows/Fonts/simsun.ttc", 0),
    ("C:/Windows/Fonts/msyh.ttc", 0),
    ("C:/Windows/Fonts/msyh.ttf", 0),
];

/// 资产目录里放字体时的文件名（按顺序试）。
const ASSET_NAMES: &[&str] = &["font.ttf", "font.otf", "font.ttc"];

/// 自检用的汉字：**能画出它**才算这份字体能用。
///
/// 挑"勇"是因为它笔画多（能过这一关的字体基本什么汉字都能画），
/// 而且它就在我们的用例里（角色名"勇士"）。
const PROBE_CHAR: char = '勇';

impl Face {
    /// 按 [`CANDIDATES`] 的顺序找一份**能画汉字**的字体。
    pub fn load() -> Option<Face> {
        // 1) 显式指定
        if let Ok(p) = std::env::var("MIR2_FONT") {
            for col in [0u32, 1, 2] {
                if let Some(f) = Face::from_file(std::path::Path::new(&p), col) {
                    return Some(f);
                }
            }
            eprintln!("MIR2_FONT={p} 载不进（或画不出汉字），继续找系统字体");
        }
        // 2) 资产目录（与 wzl 同一处：素材不进仓库）
        if let Some(dir) = crate::paths::asset_dir() {
            for name in ASSET_NAMES {
                let p = dir.join(name);
                if let Some(f) = Face::from_file(&p, 0) {
                    return Some(f);
                }
            }
        }
        // 3) 系统字体
        for (path, col) in CANDIDATES {
            if let Some(f) = Face::from_file(std::path::Path::new(path), *col) {
                return Some(f);
            }
        }
        None
    }

    /// 载入指定文件（并**自检**：画得出汉字才认）。
    pub fn from_file(path: &std::path::Path, collection: u32) -> Option<Face> {
        let bytes = std::fs::read(path).ok()?;
        let font = fontdue::Font::from_bytes(
            bytes,
            fontdue::FontSettings {
                collection_index: collection,
                // 界面字号就在 14px 附近，按它优化光栅化（fontdue 的 scale 只是提示）
                scale: UI_PX,
                ..Default::default()
            },
        )
        .ok()?;
        let f = Face {
            font,
            source: format!(
                "{}{}",
                path.display(),
                if collection > 0 {
                    format!("#{collection}")
                } else {
                    String::new()
                }
            ),
        };
        f.probe().then_some(f)
    }

    /// 自检：真的画得出汉字吗（空位图 = 这份字体没有中文字形）。
    ///
    /// ⚠️ 这一步是必须的：`.ttc` 里第 0 个 face 常常是拉丁字体
    /// （`Songti.ttc` 的第 0 个恰好是宋体，但别的机器上未必），
    /// 光"文件读进来了"会让界面变成一片空白 —— 那是最难查的一种症状。
    fn probe(&self) -> bool {
        self.has(PROBE_CHAR) && !self.glyph(PROBE_CHAR, UI_PX).cov.is_empty()
    }

    /// 这份字体用的哪个文件（日志与断言用）。
    pub fn source(&self) -> &str {
        &self.source
    }

    /// 字体里有没有这个字的字形。
    pub fn has(&self, ch: char) -> bool {
        self.font.lookup_glyph_index(ch) != 0
    }

    /// 光栅化一个字。
    ///
    /// 字体里没有这个字（或空白字符）⇒ 返回 `Glyph::blank`，但**笔位照常前进**
    /// （用一个"豆腐块"宽度的估计值），免得整行挤在一起。
    pub fn glyph(&self, ch: char, px: f32) -> Glyph {
        if ch.is_control() {
            return Glyph::blank(0.0);
        }
        if !self.has(ch) {
            // 没有字形：留半个字宽，**不画**（画成方块在中文界面里更容易误认）
            return Glyph::blank(px * 0.5);
        }
        let (m, cov) = self.font.rasterize(ch, px);
        Glyph {
            w: m.width as u32,
            h: m.height as u32,
            xmin: m.xmin,
            ymin: m.ymin,
            advance: m.advance_width,
            cov,
        }
    }

    /// 一串文字的宽度（= 各字前进量之和）。
    pub fn width(&self, s: &str, px: f32) -> f32 {
        s.chars().map(|c| self.glyph(c, px).advance).sum()
    }

    /// 行度量（定位基线用）。
    pub fn metrics(&self, px: f32) -> LineMetrics {
        match self.font.horizontal_line_metrics(px) {
            Some(m) => LineMetrics {
                ascent: m.ascent,
                descent: m.descent,
                line: m.new_line_size,
            },
            // 字体没给度量：按字号估（ascent ≈ 0.8em，这是常见比例）
            None => LineMetrics {
                ascent: px * 0.8,
                descent: -px * 0.2,
                line: px * 1.2,
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 本机有没有可用字体；没有就跳过（与 `wzl.rs`/`actor.rs` 的真素材验收同一个门控）。
    fn face_or_skip() -> Option<Face> {
        match Face::load() {
            Some(f) => Some(f),
            None => {
                eprintln!("跳过：本机找不到能画汉字的字体（可用 MIR2_FONT=... 指定）");
                None
            }
        }
    }

    /// **真字体验收**：汉字真的画得出来，而且量宽是"成比例"的。
    ///
    /// 这条是这一层的核心 —— 8×8 调试字体在这里会全红（它画不出汉字）。
    #[test]
    fn 真字体_汉字画得出来() {
        let Some(f) = face_or_skip() else { return };
        eprintln!("用字体：{}", f.source());

        assert!(f.has('勇'), "这份字体画不出'勇'");
        let g = f.glyph('勇', UI_PX);
        assert!(g.w > 0 && g.h > 0, "字形位图是空的：{g:?}");
        assert!(!g.cov.is_empty());
        // 14px 的字，位图不该超过 20px（超了说明 px 被当成了 pt 或翻了倍）
        assert!(g.w <= 20 && g.h <= 20, "字形尺寸离谱：{}x{}", g.w, g.h);
        // 笔画是真的有墨（全 0 = 空图）
        let ink: u32 = g.cov.iter().map(|&v| v as u32).sum();
        assert!(ink > 0, "字形有尺寸但一个像素都没画");

        // 两个汉字比一个字宽，且大致是两倍（说明 advance 在累加，而不是常数）
        let (w1, w2) = (f.width("勇", UI_PX), f.width("勇士", UI_PX));
        assert!(w2 > w1 * 1.5, "两个字 {w2} 不比一个字 {w1} 宽多少");

        // ASCII 也要能画（等级数字、按钮提示都要用）
        assert!(f.has('7'), "连 ASCII 都画不出");
        assert!(f.glyph('7', UI_PX).h > 0);
    }

    /// 行度量得是"正数 ascent + 负数 descent"，否则基线定位会翻过来。
    #[test]
    fn 行度量方向正确() {
        let Some(f) = face_or_skip() else { return };
        let m = f.metrics(UI_PX);
        assert!(m.ascent > 0.0, "{m:?}");
        assert!(m.descent <= 0.0, "{m:?}");
        assert!(m.line >= m.ascent - m.descent - 0.01, "{m:?}");
    }

    /// 字体里没有的字：**不崩、不画方块**，但笔位仍要前进（否则整行会挤在一起）。
    #[test]
    fn 缺字形时留位不画() {
        let Some(f) = face_or_skip() else { return };
        // 私用区码位：几乎可以肯定任何字体都没有
        let g = f.glyph('\u{E000}', UI_PX);
        assert!(g.cov.is_empty());
        assert!(g.advance > 0.0, "缺字形也要前进，否则后面的字会叠上来");
        // 空白与换行不该前进（它们是排版指令，不是字形）
        assert_eq!(f.glyph('\n', UI_PX).advance, 0.0);
    }

    /// 载不进的文件 ⇒ `None`（而不是 panic）：字体是**外部素材**，缺了得能降级。
    #[test]
    fn 坏文件不崩() {
        assert!(Face::from_file(std::path::Path::new("/nonexistent/x.ttf"), 0).is_none());
    }
}

//! 界面控件层 —— 照官方 `DWinCtl.pas` 的 `TDControl` 体系来，但用 Rust 的方式落地。
//!
//! 官方那一套（我们照它的语义，不照它的继承）：
//!
//! ```text
//! TDControl   rect / Visible / WLib+FaceIndex / SurfaceX(父偏移) / InRange(hit)
//!             OnDirectPaint / OnMouseMove / OnMouseDown / OnMouseUp / OnClick / OnDblClick
//!   ├─ TDButton   Downed（按下态）
//!   ├─ TDWindow   Floating（可拖）
//!   └─ TDGrid     ColCount/RowCount/…（格子）
//! ```
//!
//! ⚠️ Rust 里**不照搬回调**：事件用**返回值**（[`Response`]）而不是闭包 —— 闭包要
//! 抓 `&mut` 一堆状态，在这套"几何在 `layout.rs`、动作在主循环里 match"的结构下
//! 会非常别扭。控件**只是数据**（属性），交互由 [`Ui`] 这一个引擎统一算。
//!
//! 坐标：**一律画布坐标**（1024×768）。登录/选角那两屏是设计坐标 800×600，
//! 换算只在它们自己那一处做 —— `UI_SCALE` 不许出现在控件层（踩过：D-64）。

use crate::ui::UiCache;
use sdl3::render::{FRect, TextureCreator, WindowCanvas};
use std::path::Path;

/// 矩形 `(x, y, w, h)`。
pub(crate) type Rect = (f32, f32, f32, f32);

/// 一张界面图（容器 + 图号）—— 官方的 `WLib` + `FaceIndex`。
pub(crate) type Img = (&'static str, u32);

/// `p` 落在 `r` 里吗（**半开区间**：右/下边界不算 —— 与官方 `InRange` 同口径）。
pub(crate) fn hit(r: Rect, p: (f32, f32)) -> bool {
    p.0 >= r.0 && p.0 < r.0 + r.2 && p.1 >= r.1 && p.1 < r.1 + r.3
}

/// 把 `r` 整体挪 `(dx, dy)` —— 官方的 `SurfaceX/SurfaceY`（父窗口偏移）。
pub(crate) fn offset(r: Rect, dx: f32, dy: f32) -> Rect {
    (r.0 + dx, r.1 + dy, r.2, r.3)
}

/// 一次交互的结果（**立即模式**：不用回调，主循环里 match 它）。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct Response {
    /// 鼠标在控件上（**官方没有 hover 这个概念**，见 [`ButtonArt::hover`]）。
    pub hovered: bool,
    /// 按住且还在控件上 —— 官方 `TDButton.Downed`。
    pub pressed: bool,
    /// **OnClick**：左键按下 + 松开都落在同一控件（`DWinCtl.pas:678-691`）。
    pub clicked: bool,
}

/// 界面上**可按的控件**（`Ui` 的按下/点击标识）。
///
/// ⚠️ 只列"按钮 / 开关"。**格子与列表不在这里**（背包格、商品行、装备槽）——
/// 官方那是 `TDGrid` 一族，语义是"选中 / 拖拽起点"，不是 OnClick 按钮。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Press {
    /// 底栏右上 4 颗（人物/包裹/技能/选项）。
    Hud(crate::layout::HudBtn),
    /// 底栏左下 7 颗（小地图/交易/行会/组队/属性/注销/退出）。
    Quick(crate::layout::QuickBtn),
    /// 快捷（腰带）栏第 `i` 格。
    Belt(usize),
    /// 背包窗的关闭 X。
    BagClose,
    /// 状态窗的关闭 X / 翻页箭头。
    Status(crate::status::Hit),
    /// 交易窗的 [确定]/[左]/[右]/[关闭]。
    Shop(crate::shop::Hit),
    /// 卖货窗的槽 / [OK] / [关闭]。
    Sell(crate::shop::SellHit),
    /// 退出确认框的 [确定]。
    ConfirmOk,
    /// 退出确认框的 [取消]。
    ConfirmCancel,
}

/// **界面引擎**：所有控件共用这一份，统一算 hover / 按下 / OnClick / 双击。
///
/// OnClick 的规范（用户 2026-10-11，并与官方 `TDButton` 对上）：
///
/// | 情况 | 触发 |
/// |---|---|
/// | 左键在同一控件**按下 + 松开** | ✅ |
/// | 按下后**拖出**控件再松开 | ❌ |
/// | 右键 / 中键 | ❌（只有 MouseDown/MouseUp） |
/// | 只有 MouseDown 没有 MouseUp | ❌ |
pub(crate) struct Ui {
    /// 鼠标位置（画布坐标）。
    mouse: (f32, f32),
    /// 按下时记下的（控件 + **它的矩形**）。
    ///
    /// ⚠️ 记矩形是为了松开时不必再算一遍"点中了谁"：只有落在**同一块**才算数，
    /// 拖出去再松开自然就不命中了。
    down: Option<(Press, Rect)>,
}

impl Default for Ui {
    fn default() -> Self {
        Self {
            mouse: (0.0, 0.0),
            down: None,
        }
    }
}

impl Ui {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    /// 每帧（或每个鼠标事件后）更新鼠标位置。
    pub(crate) fn set_mouse(&mut self, p: (f32, f32)) {
        self.mouse = p;
    }

    /// **MouseDown**（只认左键）⇒ 记下控件，**不执行**。
    pub(crate) fn press(&mut self, key: Press, rect: Rect) {
        self.down = Some((key, rect));
    }

    /// **MouseUp**（只认左键）⇒ 落在同一个控件上才返回它（**这就是 OnClick**）。
    pub(crate) fn release(&mut self, p: (f32, f32)) -> Option<Press> {
        let (k, r) = self.down.take()?;
        if !hit(r, p) {
            return None; // 拖出去再松开 ⇒ 不触发
        }
        Some(k)
    }

    /// 当前**按住且还在控件上**的是谁 ⇒ 给**按下态高亮**用
    ///（拖出即返回 `None`，与官方 `TDButton.MouseMove` 的 `Downed` 同口径）。
    pub(crate) fn held(&self) -> Option<Press> {
        let (k, r) = self.down.as_ref()?;
        if hit(*r, self.mouse) {
            Some(*k)
        } else {
            None
        }
    }

    /// 鼠标现在压在 `r` 上吗（hover —— 官方没有，但我们允许控件可选地用它）。
    pub(crate) fn hovered(&self, r: Rect) -> bool {
        hit(r, self.mouse)
    }

    /// 丢掉按下记录（换界面时用，免得它漂到别处）。
    #[allow(dead_code)]
    pub(crate) fn clear(&mut self) {
        self.down = None;
    }
}

// ---------- Button ----------

/// 一个按钮的几张图。
///
/// ⚠️ **官方只有"常态 + 按下态"两张**（`FaceIndex` 与 `FaceIndex+1`），**没有 hover**
/// —— 用户 2026-10-11 敲定：**hover 允许配，但默认不配**（`None`）⇒ 行为与官方一致。
///
/// `normal == None` 是合法的：很多按钮的**常态就画在窗口底图里**，只有高亮/按下
/// 那张才是单独的图（底栏 8-11、交易窗 386-388、卖窗 393 都是这种）。
#[derive(Debug, Clone, Copy, Default)]
pub(crate) struct ButtonArt {
    /// 常态（`None` = 常态在底图里 ⇒ 什么都不画）。
    pub normal: Option<Img>,
    /// 按下态（高亮图）。
    pub down: Option<Img>,
    /// 悬停态（**默认 `None`**，官方没有；要就配）。
    pub hover: Option<Img>,
    /// 禁用态。
    pub disabled: Option<Img>,
}

impl ButtonArt {
    /// 只有一张"按下/高亮"图（常态在底图里）—— 官方最常见的那种。
    pub(crate) fn highlight_only(down: Img) -> Self {
        Self {
            normal: None,
            down: Some(down),
            hover: None,
            disabled: None,
        }
    }

    /// 常态图 + 按下图（左下那排快捷钮那种）。
    pub(crate) fn two_state(normal: Img, down: Img) -> Self {
        Self {
            normal: Some(normal),
            down: Some(down),
            hover: None,
            disabled: None,
        }
    }

    /// 按当前状态挑该画哪张。
    pub(crate) fn pick(self, r: &Response, enabled: bool) -> Option<Img> {
        if !enabled {
            return self.disabled.or(self.normal);
        }
        if r.pressed {
            self.down.or(self.normal)
        } else if r.hovered {
            self.hover.or(self.normal)
        } else {
            self.normal
        }
    }
}

/// 一个按钮（**只是数据**：谁来画、点中算什么，都由调用方决定）。
#[derive(Debug, Clone, Copy)]
pub(crate) struct Button {
    /// 按下/点击的标识。
    pub key: Press,
    pub rect: Rect,
    pub art: ButtonArt,
    /// 禁用（画 `art.disabled`，且不响应点击）。
    pub enabled: bool,
}

impl Button {
    /// 算这一帧的交互结果（不画）。
    pub(crate) fn response(&self, ui: &Ui) -> Response {
        Response {
            hovered: ui.hovered(self.rect),
            pressed: ui.held() == Some(self.key),
            clicked: false, // OnClick 是"松开"那一刻的事，由 `Ui::release` 给
        }
    }
}

/// 画一个按钮（**画布坐标**，走 `draw_src` 不做换算 —— 见 `hud::hud_img` 那条说明）。
pub(crate) fn draw_button<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui_img: &mut UiCache<'a>,
    dir: &Path,
    b: &Button,
    r: &Response,
) {
    let Some((lib, idx)) = b.art.pick(r, b.enabled) else {
        return; // 常态在底图里、又没在按下 ⇒ 什么都不画
    };
    let Some((iw, ih)) = ui_img.size(dir, lib, idx) else {
        return;
    };
    let _ = ui_img.draw_src(
        canvas,
        tc,
        dir,
        lib,
        idx,
        FRect::new(0.0, 0.0, iw as f32, ih as f32),
        FRect::new(b.rect.0, b.rect.1, iw as f32, ih as f32),
        255,
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    const R: Rect = (10.0, 10.0, 20.0, 20.0);
    const A: Press = Press::BagClose;

    #[test]
    fn 按下与松开在同一控件才算点击() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse((15.0, 15.0));
        assert_eq!(ui.release((15.0, 15.0)), Some(A));
        assert_eq!(ui.release((15.0, 15.0)), None, "松开过就该清掉");
    }

    #[test]
    fn 拖出控件再松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse((99.0, 99.0));
        assert_eq!(ui.release((99.0, 99.0)), None);
        assert_eq!(ui.held(), None);
    }

    #[test]
    fn 只按下不松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse((15.0, 15.0));
        assert_eq!(ui.held(), Some(A), "按着没松 ⇒ 高亮还在");
        assert_eq!(ui.release((99.0, 99.0)), None);
    }

    #[test]
    fn 高亮随拖出而灭() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse((15.0, 15.0));
        assert_eq!(ui.held(), Some(A));
        ui.set_mouse((50.0, 50.0));
        assert_eq!(ui.held(), None, "拖出去 ⇒ 灭");
        ui.set_mouse((15.0, 15.0));
        assert_eq!(ui.held(), Some(A), "拖回来 ⇒ 又亮");
    }

    #[test]
    fn 按钮图的挑选顺序() {
        // 只有高亮图：常态/悬停都不画，按下才画
        let art = ButtonArt::highlight_only(("Prguse", 393));
        assert_eq!(
            art.pick(&Response::default(), true),
            None,
            "常态 ⇒ 不画（在底图里）"
        );
        assert_eq!(
            art.pick(
                &Response {
                    hovered: true,
                    ..Default::default()
                },
                true
            ),
            None,
            "没配 hover ⇒ 悬停也不画"
        );
        assert_eq!(
            art.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 393))
        );

        // 两态：常态常画，按下换图
        let two = ButtonArt::two_state(("Prguse", 130), ("Prguse", 131));
        assert_eq!(two.pick(&Response::default(), true), Some(("Prguse", 130)));
        assert_eq!(
            two.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 131))
        );

        // 配了 hover 才有悬停图
        let mut h = ButtonArt::two_state(("Prguse", 1), ("Prguse", 2));
        h.hover = Some(("Prguse", 3));
        assert_eq!(
            h.pick(
                &Response {
                    hovered: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 3))
        );
    }

    #[test]
    fn 禁用时画禁用图且不响应() {
        let mut art = ButtonArt::two_state(("Prguse", 1), ("Prguse", 2));
        art.disabled = Some(("Prguse", 9));
        assert_eq!(art.pick(&Response::default(), true), Some(("Prguse", 1)));
        assert_eq!(
            art.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                false
            ),
            Some(("Prguse", 9))
        );
    }

    #[test]
    fn 矩形是半开区间() {
        assert!(hit(R, (10.0, 10.0)));
        assert!(!hit(R, (30.0, 30.0)));
        assert_eq!(offset(R, 5.0, -1.0), (15.0, 9.0, 20.0, 20.0));
    }
}

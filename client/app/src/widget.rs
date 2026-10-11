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
//! 坐标：**控件引擎一律 `CPt/CRect` 画布坐标**（1024×768）。登录/选角的布局仍在
//! 设计空间 800×600 中计算，但进入 `Ui::press` **之前**显式转成画布 `CRect`，
//! 鼠标始终以画布 `CPt` 进入引擎；设计/画布/窗口局部三种坐标不能混用（D-64）。

use crate::ui::UiCache;
use sdl3::render::{FRect, TextureCreator, WindowCanvas};
use std::path::Path;

pub(crate) use crate::widget_state::{
    canvas_pt, canvas_rect, design_rect, offset_local_to_canvas, Press, Rect, Response, Ui,
};

/// 一张界面图（容器 + 图号）—— 官方的 `WLib` + `FaceIndex`。
pub(crate) type Img = (&'static str, u32);

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

    /// 只有一张常态图（没有高亮图 —— 比如状态窗的翻页箭头）。
    pub(crate) fn plain(normal: Img) -> Self {
        Self {
            normal: Some(normal),
            down: None,
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
        FRect::new(b.rect.0 .0, b.rect.1 .0, iw as f32, ih as f32),
        255,
    );
}

// ---------- Grid（官方 TDGrid）----------

/// 网格（`TDGrid`）：列数/行数 + 格子尺寸 ⇒ `cell_rect(i)` / `hit_cell(p)`。
///
/// ⚠️ 官方的 `TDGrid` 语义是**选中 / 拖拽起点**，不是 `OnClick` 按钮 ⇒
/// 它不进 [`Press`]；而且背包网格的原点是**窗口局部坐标**，所以使用 `LRect/LPt`，
/// 不能把它伪装成按钮使用的全局画布 `Rect`。
#[derive(Debug, Clone, Copy)]
pub(crate) struct Grid {
    origin: crate::space::LPt,
    cols: usize,
    rows: usize,
    cell_w: crate::space::Lpx,
    cell_h: crate::space::Lpx,
}

impl Grid {
    pub(crate) fn new(
        origin: crate::space::LPt,
        cols: usize,
        rows: usize,
        cell_w: crate::space::Lpx,
        cell_h: crate::space::Lpx,
    ) -> Self {
        Self {
            origin,
            cols,
            rows,
            cell_w,
            cell_h,
        }
    }

    /// 格子总数。
    pub(crate) fn len(&self) -> usize {
        self.cols * self.rows
    }

    /// 第 `i` 格的**窗口局部**矩形。
    pub(crate) fn cell_rect(&self, i: usize) -> crate::space::LRect {
        let (c, r) = (i % self.cols, i / self.cols);
        (
            crate::space::Lpx(self.origin.0 .0 + c as f32 * self.cell_w.0),
            crate::space::Lpx(self.origin.1 .0 + r as f32 * self.cell_h.0),
            self.cell_w,
            self.cell_h,
        )
    }

    /// `p` 落在第几格（`None` = 不在网格里）。与 [`Grid::cell_rect`] **同源**。
    pub(crate) fn hit_cell(&self, p: crate::space::LPt) -> Option<usize> {
        (0..self.len()).find(|i| crate::space::hit_local(self.cell_rect(*i), p))
    }
}

// ---------- 一个物品格（拖拽源 + 放置目标）----------

/// 物品从哪个槽位来（官方 `g_MovingItem.Index` 的正规化）。
///
/// ⚠️ **下一步**才接线：现在"手上拿着东西"还是 `sell_held: Option<usize>` +
/// `sell_placed: Option<usize>`（只认背包格）⇒ 换成 `Option<Drag>` 之后，
/// 背包 ↔ 卖窗槽 ↔ 腰带 ↔ 装备槽 就能共用一套拖拽，不用每种组合各写一遍。
///
/// ⚠️ 现在只有 `Bag` / `Sell` / `Belt` / `Equip` 四种；以后加仓库/交易窗只要加变体，
/// 拖拽逻辑不用再改 —— 这正是把"手上拿着东西"从一个 `Option<usize>` 升级成
/// 带**来源标记**的结构的意义。
#[allow(dead_code)] // 见上面"下一步"（迁移完 `sell_held` 就去掉）
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SlotRef {
    /// 背包第 `idx` 格（**服务端那一格的下标**，不是页内下标）。
    Bag(usize),
    /// 卖货窗那个圆槽。
    Sell,
    /// 快捷（腰带）栏第 `i` 格。
    Belt(usize),
    /// 装备槽（服务端槽位下标）。
    Equip(usize),
}

/// **拖拽会话**（官方 `g_boItemMoving` + `g_MovingItem` 的正规化）。
///
/// 一个"手上拿着东西"的状态 = 拿着的**是什么**（`make` 实例号）+ **从哪来**（`from`）。
///
/// ⚠️ `from` 里的**下标会过期**：服务端随时可能重排背包（用了东西、买了东西、
/// 捡了地上的）⇒ 那一格可能已经换了东西。这时若还按老下标去"放下/卖出"，
/// 动的会是**别的物品**（卖错东西不可恢复）⇒ 每帧用 [`Drag::rebind`] 按
/// 实例号重新定位（用户 2026-10-11 盲查发现，见 D-75）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Drag {
    pub from: SlotRef,
    /// 物品实例号（服务端 `MakeIndex`）—— 认物**只认它**，不认下标。
    pub make: i32,
}

impl Drag {
    pub(crate) fn new(from: SlotRef, make: i32) -> Self {
        Self { from, make }
    }

    /// **按实例号重新定位**（背包被服务端重排之后）。
    ///
    /// `find(make) -> 新的下标`；找不到了 ⇒ `None`（这件东西没了：用光/卖掉/被拿走）
    /// ⇒ 调用方把"手上拿着"清掉，免得继续拿着一个幽灵。
    pub(crate) fn rebind(self, find: impl Fn(i32) -> Option<usize>) -> Option<Self> {
        let i = find(self.make)?;
        Some(Self {
            from: SlotRef::Bag(i),
            make: self.make,
        })
    }

    /// **卖窗圆槽的点按**（原版 `DSellItemSlotDirectPaint` 的语义）：
    /// 空手点槽 = 把槽里那件**拿回手上**；拿着东西点槽 = **放进去**。
    /// 返回 `(手上, 槽里)`。
    ///
    /// ⚠️ 抽出来是因为它原来埋在事件循环里（测不到）；"同时拿着又放着"
    /// 是不该出现的状态，这里保证不会出现。
    pub(crate) fn toggle_slot(
        held: Option<Drag>,
        placed: Option<Drag>,
    ) -> (Option<Drag>, Option<Drag>) {
        match (held, placed) {
            (Some(d), None) => (None, Some(d)),
            (None, Some(d)) => (Some(d), None),
            (h, p) => (h, p),
        }
    }

    /// 它是从背包第几格拿起来的（不是背包来的 ⇒ `None`）。
    pub(crate) fn bag_index(self) -> Option<usize> {
        match self.from {
            SlotRef::Bag(i) => Some(i),
            _ => None,
        }
    }
}

#[cfg(test)]
mod grid_tests {
    use super::*;

    #[test]
    fn 网格的格子排布与命中() {
        let g = Grid::new(
            crate::space::LPt(crate::space::Lpx(10.0), crate::space::Lpx(20.0)),
            6,
            4,
            crate::space::Lpx(35.5),
            crate::space::Lpx(32.5),
        );
        assert_eq!(g.len(), 24);
        // 第 0 格（窗口局部空间）
        assert_eq!(
            g.cell_rect(0),
            (
                crate::space::Lpx(10.0),
                crate::space::Lpx(20.0),
                crate::space::Lpx(35.5),
                crate::space::Lpx(32.5)
            )
        );
        // 第 6 格 = 第二行第一列
        assert_eq!(g.cell_rect(6).0, crate::space::Lpx(10.0));
        assert_eq!(g.cell_rect(6).1, crate::space::Lpx(52.5));
        // 第 7 格 = 第二行第二列
        assert_eq!(g.cell_rect(7).0, crate::space::Lpx(45.5));
        assert_eq!(g.cell_rect(7).1, crate::space::Lpx(52.5));
        // 命中与矩形同源
        for i in 0..g.len() {
            let (x, y, w, h) = g.cell_rect(i);
            assert_eq!(
                g.hit_cell(crate::space::LPt(
                    crate::space::Lpx(x.0 + w.0 / 2.0),
                    crate::space::Lpx(y.0 + h.0 / 2.0),
                )),
                Some(i),
                "第 {i} 格"
            );
        }
        assert_eq!(
            g.hit_cell(crate::space::LPt(
                crate::space::Lpx(0.0),
                crate::space::Lpx(0.0)
            )),
            None,
            "网格外 ⇒ None"
        );
    }

    #[test]
    fn 卖窗圆槽的点按在手上与槽之间搬() {
        let d = Drag::new(SlotRef::Bag(3), 91);
        // 拿着 ⇒ 点槽 = 放进去
        assert_eq!(Drag::toggle_slot(Some(d), None), (None, Some(d)));
        // 空手 ⇒ 点槽 = 拿回手上
        assert_eq!(Drag::toggle_slot(None, Some(d)), (Some(d), None));
        // 两边都空 / 两边都有 ⇒ 原样（不该出现，也不能变出东西来）
        assert_eq!(Drag::toggle_slot(None, None), (None, None));
        assert_eq!(Drag::toggle_slot(Some(d), Some(d)), (Some(d), Some(d)));
    }

    #[test]
    fn 拖拽会话带来源() {
        let d = Drag::new(SlotRef::Bag(7), 91);
        assert_eq!(d.bag_index(), Some(7));
        assert_eq!(Drag::new(SlotRef::Sell, 1).bag_index(), None);
        assert_eq!(Drag::new(SlotRef::Belt(2), 1).bag_index(), None);
    }

    #[test]
    fn 背包重排后按实例号重新定位() {
        // 实例号 91 原来在第 7 格，服务端重排后跑到了第 2 格
        let d = Drag::new(SlotRef::Bag(7), 91);
        let bag: [(i32, usize); 2] = [(90, 5), (91, 2)];
        let got = d
            .rebind(|mk| bag.iter().find(|(m, _)| *m == mk).map(|(_, i)| *i))
            .expect("还在背包里");
        assert_eq!(got.bag_index(), Some(2), "下标要跟着重排走");
        assert_eq!(got.make, 91, "实例号不变");

        // 找不到了（用光/卖掉/被捡走）⇒ None，免得拿着一个幽灵继续操作
        assert_eq!(d.rebind(|_| None), None);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const R: Rect = (
        crate::space::Cpx(10.0),
        crate::space::Cpx(10.0),
        crate::space::Cpx(20.0),
        crate::space::Cpx(20.0),
    );
    const IN: crate::space::CPt =
        crate::space::CPt(crate::space::Cpx(15.0), crate::space::Cpx(15.0));
    const OUT: crate::space::CPt =
        crate::space::CPt(crate::space::Cpx(99.0), crate::space::Cpx(99.0));
    const A: Press = Press::BagClose;

    #[test]
    fn 按下与松开在同一控件才算点击() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.release(IN), Some(A));
        assert_eq!(ui.release(IN), None, "松开过就该清掉");
    }

    #[test]
    fn 拖出控件再松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(OUT);
        assert_eq!(ui.release(OUT), None);
        assert_eq!(ui.held(), None);
    }

    #[test]
    fn 只按下不松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.held(), Some(A), "按着没松 ⇒ 高亮还在");
        assert_eq!(ui.release(OUT), None);
    }

    #[test]
    fn 高亮随拖出而灭() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.held(), Some(A));
        ui.set_mouse(crate::space::CPt(
            crate::space::Cpx(50.0),
            crate::space::Cpx(50.0),
        ));
        assert_eq!(ui.held(), None, "拖出去 ⇒ 灭");
        ui.set_mouse(IN);
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
        assert!(crate::widget_state::hit(
            R,
            crate::space::CPt(crate::space::Cpx(10.0), crate::space::Cpx(10.0))
        ));
        assert!(!crate::widget_state::hit(
            R,
            crate::space::CPt(crate::space::Cpx(30.0), crate::space::Cpx(30.0))
        ));
    }

    #[test]
    fn 设计空间按钮矩形与画布鼠标同空间判定() {
        let r = design_rect((10.0, 10.0, 20.0, 20.0));
        let p = crate::ui::ui_pt((15.0, 15.0));
        let mut ui = Ui::new();
        ui.set_mouse(p);
        ui.press(A, r);
        assert_eq!(
            ui.held(),
            Some(A),
            "设计矩形须显式换到画布，才能和鼠标同空间命中"
        );
        assert_eq!(ui.release(p), Some(A));
    }
}

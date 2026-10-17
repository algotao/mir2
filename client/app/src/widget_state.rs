//! **纯控件交互状态**：几何命中、按下/抬起 OnClick 和控件标识。
//!
//! 不依赖 SDL 或纹理。`widget.rs` 只负责把这里的响应绘制成按钮外观；
//! 事件适配层将 SDL 坐标转为 `CPt` 后交给 `Ui`。

/// 按钮的全局画布矩形（设计空间矩形或窗口局部矩形不能直接传进来）。
pub(crate) type Rect = crate::space::CRect;

/// `p` 落在 `r` 里吗（**半开区间**：右/下边界不算 —— 与官方 `InRange` 同口径）。
pub(crate) fn hit(r: Rect, p: crate::space::CPt) -> bool {
    crate::space::hit(r, p)
}

/// 由已确认处于逻辑画布空间的外部坐标构造按钮矩形。
///
/// ⚠️ `f32` 输入只允许留在 SDL/layout 边界；设计坐标应使用 [`design_rect`]，
/// 面板局部几何应使用 [`local_rect`] + [`offset_local_to_canvas`]。
pub(crate) fn canvas_rect(r: (f32, f32, f32, f32)) -> Rect {
    crate::space::canvas_rect(r.0, r.1, r.2, r.3)
}

/// SDL 逻辑画布坐标边界转成强类型点。
pub(crate) fn canvas_pt(p: (f32, f32)) -> crate::space::CPt {
    crate::space::CPt::from_xy(p.0, p.1)
}

/// 由设计空间几何建立画布矩形（显式做一次 800×600 → 画布转换）。
pub(crate) fn design_rect(r: crate::space::DRect) -> Rect {
    crate::space::Dpx::rect_to_canvas(r)
}

/// 局部面板矩形 + 全局画布原点 ⇒ 全局画布矩形。
pub(crate) fn offset_local_to_canvas(r: (f32, f32, f32, f32), origin: crate::space::CPt) -> Rect {
    crate::space::local_rect_to_canvas(crate::space::local_rect(r.0, r.1, r.2, r.3), origin)
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
    /// **技能栏**第 `i` 格（F1..F8）：点它 = 放绑在那格上的技能。
    SkillBar(usize),
    /// 背包窗的关闭 X。
    BagClose,
    /// 状态窗的关闭 X / 翻页箭头。
    Status(crate::status::Hit),
    /// 交易窗的 [确定]/[左]/[右]/[关闭]。
    Shop(crate::shop::Hit),
    /// 卖货窗的槽 / [OK] / [关闭]。
    Sell(crate::shop::SellHit),
    /// 商店数量面板的 [−] / [+] / [确定] / [取消]（D-148）。
    Qty(crate::qty::Hit),
    /// 选角场景的按钮（[`crate::select::Hit`]）。
    Select(crate::select::Hit),
    /// 建角对话框里的控件（[`mir2_core::select_ui::DialogHit`]）。
    NewChar(mir2_core::select_ui::DialogHit),
    /// 登录界面的按钮（[`crate::login::Btn`]）。
    ///
    /// ⚠️ 登录是 `mode == 1`，与游戏内（`mode == 2`）的 `release` 不会撞车
    /// ⇒ 它单独用自己的一次 `press` / `release`。
    Login(crate::login::Btn),
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
    /// 鼠标位置（全局**画布**坐标；设计空间必须先转换）。
    mouse: crate::space::CPt,
    /// 按下时记下的（控件 + **它的矩形**）。
    ///
    /// ⚠️ 记矩形是为了松开时不必再算一遍"点中了谁"：只有落在**同一块**才算数，
    /// 拖出去再松开自然就不命中了。
    down: Option<(Press, Rect)>,
}

impl Default for Ui {
    fn default() -> Self {
        Self {
            mouse: crate::space::CPt(crate::space::Cpx(0.0), crate::space::Cpx(0.0)),
            down: None,
        }
    }
}

impl Ui {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    /// 每帧（或每个鼠标事件后）更新鼠标位置。
    pub(crate) fn set_mouse(&mut self, p: crate::space::CPt) {
        self.mouse = p;
    }

    /// **MouseDown**（只认左键）⇒ 记下控件，**不执行**。
    pub(crate) fn press(&mut self, key: Press, rect: Rect) {
        self.down = Some((key, rect));
    }

    /// **MouseUp**（只认左键）⇒ 落在同一个控件上才返回它（**这就是 OnClick**）。
    pub(crate) fn release(&mut self, p: crate::space::CPt) -> Option<Press> {
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

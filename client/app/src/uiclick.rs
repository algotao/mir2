//! 界面点击的**统一状态机**（OnClick 规范）—— 所有窗共用这一份，别各写一遍。
//!
//! 规范（用户 2026-10-11 给的，并与官方 `TDButton` 对上）：
//!
//! | 情况 | 是否触发 OnClick |
//! |---|---|
//! | 左键在同一控件上 **按下 + 松开** | ✅ 触发 |
//! | 按下后**拖出控件**再松开 | ❌ 不触发 |
//! | **右键 / 中键**单击 | ❌ 不触发（只有 OnMouseDown/OnMouseUp） |
//! | 只有 MouseDown、没有 MouseUp | ❌ 不触发 |
//!
//! 官方出处：`DWinCtl.pas:678-691` 的 `TDButton.MouseUp` —— `Downed` 由 `MouseDown`
//! 置位，只有松开时 `InRange(X,Y)` 才调 `FOnClick`；`MouseMove` 里还会
//! `if InRange then Downed := TRUE else Downed := FALSE` ⇒ **拖出控件时高亮也要灭**。
//!
//! ⚠️ 还有一层：高亮图（8-11 / 130-141 / 64 / 371 / 386-388 / 393）画的是
//! **按下态**（`Downed`），不是 hover —— 见 [`Click::held`]。

use crate::layout;
use crate::shop;
use crate::status;

/// 一个矩形 `(x, y, w, h)`（**画布坐标**）。
pub(crate) type Rect = (f32, f32, f32, f32);

/// `p` 落在 `r` 里吗（**半开区间**：右/下边界不算在内，与 `Rect::hit` 同口径）。
pub(crate) fn hit(r: Rect, p: (f32, f32)) -> bool {
    p.0 >= r.0 && p.0 < r.0 + r.2 && p.1 >= r.1 && p.1 < r.1 + r.3
}

/// **界面点击状态机**，`K` = 控件标识。
///
/// 用法（每个窗都一样）：
///
/// ```text
/// MouseButtonDown(左键) 命中控件 ⇒ click.press(kind, rect)      // 只记，不执行
/// MouseButtonUp(左键)            ⇒ if let Some(k) = click.release(p) { 执行 k }
/// 画高亮                          ⇒ click.held(p) == Some(k) 时才画那张图
/// ```
pub(crate) struct Click<K> {
    /// 按下时记下的（控件 + **它的矩形**）。
    ///
    /// ⚠️ 记矩形是为了松开时不必再算一遍"点中了谁"：只有落在**同一块**才算数，
    /// 拖出去再松开自然就不命中了。
    down: Option<(K, Rect)>,
}

impl<K: Copy + PartialEq> Default for Click<K> {
    fn default() -> Self {
        Self { down: None }
    }
}

impl<K: Copy + PartialEq> Click<K> {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    /// 左键按在 `key` 上 ⇒ **记下**（不执行任何动作）。
    ///
    /// 调用方负责判"是不是左键"：右键/中键不该走到这里（它们不触发 OnClick）。
    pub(crate) fn press(&mut self, key: K, rect: Rect) {
        self.down = Some((key, rect));
    }

    /// 左键**松开** ⇒ 落在同一个控件上才返回它（**这就是 OnClick**）。
    ///
    /// 拖出控件再松开 / 压根没按下 ⇒ `None`。无论结果如何都会清掉按下记录。
    pub(crate) fn release(&mut self, p: (f32, f32)) -> Option<K> {
        let (k, r) = self.down.take()?;
        if hit(r, p) {
            Some(k)
        } else {
            None
        }
    }

    /// 当前**按住且还在控件上**的是谁（给**按下态高亮**用）。
    ///
    /// ⚠️ 拖出控件时返回 `None` ⇒ 高亮跟着灭（官方 `TDButton.MouseMove` 的口径）。
    pub(crate) fn held(&self, p: (f32, f32)) -> Option<K> {
        let (k, r) = self.down.as_ref()?;
        if hit(*r, p) {
            Some(*k)
        } else {
            None
        }
    }

    /// 丢掉按下记录（换窗 / 换模式时用，免得"按下记录"漂到别的界面上）。
    #[allow(dead_code)] // 目前只在测试里用；留着给"换模式"那条路
    pub(crate) fn clear(&mut self) {
        self.down = None;
    }
}

/// 界面上**可按的控件**（`Click<K>` 里的 `K`）。
///
/// ⚠️ 只列"按钮/开关"这一类。**格子与列表不在这里**（背包格、商品行、装备槽）
/// —— 官方那是 `TDGrid` 一族，语义是"选中/拖拽起点"，不是 OnClick 按钮，
/// 它们照旧在 MouseDown 上响应。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum UiPress {
    /// 底栏右上 4 颗（人物/包裹/技能/选项）。
    Hud(layout::HudBtn),
    /// 底栏左下 7 颗（小地图/交易/行会/组队/属性/注销/退出）。
    Quick(layout::QuickBtn),
    /// 快捷（腰带）栏第 `i` 格。
    Belt(usize),
    /// 背包窗的关闭 X。
    BagClose,
    /// 状态窗的关闭 X / 翻页箭头（`Slot` 不在里，那是装备槽）。
    Status(status::Hit),
    /// 交易窗的 [确定]/[左]/[右]/[关闭]（`Row` 不在里，那是商品行）。
    Shop(shop::Hit),
    /// 卖货窗的槽 / [OK] / [关闭]。
    Sell(shop::SellHit),
    /// 退出确认框的 [确定]。
    ConfirmOk,
    /// 退出确认框的 [取消]。
    ConfirmCancel,
}

#[cfg(test)]
mod tests {
    use super::*;

    const R: Rect = (10.0, 10.0, 20.0, 20.0);

    /// 标签：同一个控件，用来验证"按下 + 松开在同一控件"。
    #[derive(Debug, Clone, Copy, PartialEq, Eq)]
    enum K {
        A,
        B,
    }

    #[test]
    fn 按下与松开在同一控件才算点击() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        assert_eq!(c.release((15.0, 15.0)), Some(K::A));
        assert_eq!(
            c.release((15.0, 15.0)),
            None,
            "松开过就该清掉（不能点两次）"
        );
    }

    #[test]
    fn 拖出控件再松开不触发() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        assert_eq!(c.release((99.0, 99.0)), None, "拖出去松手 ⇒ 不触发");
        assert_eq!(c.held((15.0, 15.0)), None, "已经不算按住了");
    }

    #[test]
    fn 只按下不松开不触发() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        assert_eq!(
            c.held((15.0, 15.0)),
            Some(K::A),
            "按着没松 ⇒ 高亮还在（Downed）"
        );
        // 没有 release ⇒ 什么都不会执行（这里只是保证 held 不等于\"执行过\"）
        assert_eq!(c.release((99.0, 99.0)), None);
    }

    #[test]
    fn 高亮跟随拖出而灭() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        assert_eq!(c.held((15.0, 15.0)), Some(K::A), "在控件内 ⇒ 高亮");
        assert_eq!(c.held((50.0, 50.0)), None, "拖出去 ⇒ 高亮灭（官方口径）");
        assert_eq!(c.held((15.0, 15.0)), Some(K::A), "又拖回来 ⇒ 高亮回来");
    }

    #[test]
    fn 换控件按下会顶掉原来那个() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        c.press(K::B, (50.0, 50.0, 10.0, 10.0));
        assert_eq!(c.release((55.0, 55.0)), Some(K::B));
        assert_eq!(c.release((15.0, 15.0)), None, "A 那次已经作废");
    }

    #[test]
    fn clear_丢掉按下记录() {
        let mut c = Click::<K>::new();
        c.press(K::A, R);
        c.clear();
        assert_eq!(c.held((15.0, 15.0)), None);
        assert_eq!(c.release((15.0, 15.0)), None);
    }

    #[test]
    fn 边界是半开区间() {
        assert!(hit(R, (10.0, 10.0)), "左上角在内");
        assert!(!hit(R, (30.0, 30.0)), "右下角在外（半开）");
        assert!(hit(R, (29.9, 29.9)));
    }
}

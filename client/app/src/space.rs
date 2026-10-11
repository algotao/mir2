//! **两个坐标空间**（架构方向 ②）—— 设计空间（800×600）与画布像素（×`UI_SCALE`）。
//!
//! # 为什么要类型
//!
//! 传奇的界面素材是 **800×600** 画的，而窗口可能是 1024×768 ⇒ 一切都要乘
//! `UI_SCALE`。原来换算函数是 `fn ui_px(f32) -> f32`：**传进去的是哪个空间的
//! 数、返回的是哪个空间的数，全靠记** ——
//!
//! - **D-64**：HUD 按钮被 `UI_SCALE` **乘了两次**，推出画布外；
//! - 同类还踩过好几次（画在这、点在那）。
//!
//! 现在 [`Dpx`]（设计空间）和 [`Cpx`]（画布像素）是**两个类型**，混用编译不过。
//! 想绕过？只能显式写 `Cpx(v)` —— 那就是在明说"我知道这里是什么空间"，
//! 是**有意为之**而不是手滑。
//!
//! # 怎么迁
//!
//! `ui_px(v)` ⇒ `ui_px(Dpx(v))`，返回值从 `f32` 变成 `Cpx` ⇒ 用 `.0` 取回。
//! 编译器会把每一处都点出来，逐个确认即可（这正是这次迁移的价值：
//! **让编译器当检查员**）。

/// 画布宽 / 设计宽 —— 界面整体放大倍率。
pub const UI_SCALE: f32 = crate::WIN_W as f32 / 800.0;

/// **设计空间**的长度（素材那套 800×600 坐标系里的像素）。
#[derive(Debug, Clone, Copy, Default, PartialEq, PartialOrd)]
pub struct Dpx(pub f32);

/// **画布像素**的长度（屏幕上真正的像素）。
#[derive(Debug, Clone, Copy, Default, PartialEq, PartialOrd)]
pub struct Cpx(pub f32);

impl Dpx {
    /// 设计空间 → 画布像素（画东西之前必须过这一道）。
    #[inline]
    pub fn to_canvas(self) -> Cpx {
        Cpx(self.0 * UI_SCALE)
    }
}

impl Cpx {
    /// 画布像素 → 设计空间（鼠标事件给它用的那个方向）。
    #[inline]
    pub fn to_design(self) -> Dpx {
        Dpx(self.0 / UI_SCALE)
    }
}

/// 设计空间的点。
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct DPt(pub Dpx, pub Dpx);

/// 画布像素的点。
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct CPt(pub Cpx, pub Cpx);

impl DPt {
    #[inline]
    pub fn to_canvas(self) -> CPt {
        CPt(self.0.to_canvas(), self.1.to_canvas())
    }
}

impl CPt {
    #[inline]
    pub fn to_design(self) -> DPt {
        DPt(self.0.to_design(), self.1.to_design())
    }
}

/// 设计空间的矩形（`(x, y, w, h)`）。
///
/// ⚠️ 下一步才用（把 `widget::Rect` 也类型化）；先留着，别到时再写一遍。
#[allow(dead_code)]
pub type DRect = (Dpx, Dpx, Dpx, Dpx);

/// 画布像素的矩形（`(x, y, w, h)`）—— 画与命中都用它，两者才不会跑偏。
pub type CRect = (Cpx, Cpx, Cpx, Cpx);

/// 面板/窗口**局部**长度（尚未加画布原点，不可直接绘制/命中全局鼠标）。
#[derive(Debug, Clone, Copy, Default, PartialEq, PartialOrd)]
pub struct Lpx(pub f32);

/// 面板/窗口局部点。
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub struct LPt(pub Lpx, pub Lpx);

/// 面板/窗口局部矩形。
pub type LRect = (Lpx, Lpx, Lpx, Lpx);

/// 从未标注的 SDL/布局数值建立**画布**矩形。调用点必须确认数值已在逻辑画布空间。
#[inline]
pub fn canvas_rect(x: f32, y: f32, w: f32, h: f32) -> CRect {
    (Cpx(x), Cpx(y), Cpx(w), Cpx(h))
}

/// 从未标注的面板布局数值建立局部矩形；返回类型明确禁止直接拿去画全局画布。
#[inline]
pub fn local_rect(x: f32, y: f32, w: f32, h: f32) -> LRect {
    (Lpx(x), Lpx(y), Lpx(w), Lpx(h))
}

/// 将已确认的面板局部矩形平移到画布（`origin` 是全局画布点）。
#[inline]
pub fn local_rect_to_canvas(r: LRect, origin: CPt) -> CRect {
    (
        Cpx(origin.0 .0 + r.0 .0),
        Cpx(origin.1 .0 + r.1 .0),
        Cpx(r.2 .0),
        Cpx(r.3 .0),
    )
}

/// 局部点命中局部矩形（仍用半开区间；不允许跟全局 `CPt` 混用）。
#[inline]
pub fn hit_local(r: LRect, p: LPt) -> bool {
    p.0 .0 >= r.0 .0 && p.0 .0 < r.0 .0 + r.2 .0 && p.1 .0 >= r.1 .0 && p.1 .0 < r.1 .0 + r.3 .0
}

impl Dpx {
    /// 矩形整体换算（画之前过这一道）。
    #[allow(dead_code)] // 同上
    pub fn rect_to_canvas(r: DRect) -> CRect {
        (
            r.0.to_canvas(),
            r.1.to_canvas(),
            r.2.to_canvas(),
            r.3.to_canvas(),
        )
    }
}

/// `p` 落在 `r` 里吗（**半开区间**：左边算、右边不算 ⇒ 相邻两块不会重叠）。
///
/// ⚠️ 两个参数**必须是同一个空间** —— 由类型保证，混用编译不过。
#[allow(dead_code)] // 同上
#[inline]
pub fn hit(r: CRect, p: CPt) -> bool {
    p.0 .0 >= r.0 .0 && p.0 .0 < r.0 .0 + r.2 .0 && p.1 .0 >= r.1 .0 && p.1 .0 < r.1 .0 + r.3 .0
}

/// 同上，设计空间。
#[allow(dead_code)] // 同上
#[inline]
pub fn hit_design(r: DRect, p: DPt) -> bool {
    p.0 .0 >= r.0 .0 && p.0 .0 < r.0 .0 + r.2 .0 && p.1 .0 >= r.1 .0 && p.1 .0 < r.1 .0 + r.3 .0
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 两个方向的换算互为逆() {
        let d = Dpx(100.0);
        let c = d.to_canvas();
        assert!((c.0 - 100.0 * UI_SCALE).abs() < 0.001);
        // 画布 → 设计 → 画布 ⇒ 回到原处
        let back = c.to_design().to_canvas();
        assert!((back.0 - c.0).abs() < 0.001, "来回换算不能漂");
    }

    #[test]
    fn 点的换算与分量换算一致() {
        let p = DPt(Dpx(10.0), Dpx(20.0));
        let c = p.to_canvas();
        assert!((c.0 .0 - 10.0 * UI_SCALE).abs() < 0.001);
        assert!((c.1 .0 - 20.0 * UI_SCALE).abs() < 0.001);
        let back = c.to_design();
        assert!((back.0 .0 - 10.0).abs() < 0.001);
        assert!((back.1 .0 - 20.0).abs() < 0.001);
    }

    #[test]
    fn 命中是半开区间() {
        let r = (Cpx(0.0), Cpx(0.0), Cpx(10.0), Cpx(10.0));
        assert!(hit(r, CPt(Cpx(0.0), Cpx(0.0))), "左边算");
        assert!(
            !hit(r, CPt(Cpx(10.0), Cpx(0.0))),
            "右边不算 ⇒ 相邻两块不重叠"
        );
        assert!(hit(r, CPt(Cpx(9.99), Cpx(9.99))));
        assert!(!hit(r, CPt(Cpx(-0.01), Cpx(5.0))));
    }

    #[test]
    fn 矩形换算四个分量都过一遍() {
        let d: DRect = (Dpx(1.0), Dpx(2.0), Dpx(3.0), Dpx(4.0));
        let c = Dpx::rect_to_canvas(d);
        assert!((c.0 .0 - 1.0 * UI_SCALE).abs() < 0.001);
        assert!((c.1 .0 - 2.0 * UI_SCALE).abs() < 0.001);
        assert!((c.2 .0 - 3.0 * UI_SCALE).abs() < 0.001);
        assert!((c.3 .0 - 4.0 * UI_SCALE).abs() < 0.001);
    }

    /// ⚠️ **D-64 那条**：HUD 按钮被乘了两次 `UI_SCALE` 推出画布。
    /// 这条钉住"换算只能过一道" —— 类型让它过不了第二道（第二次得显式写
    /// `Cpx(..)`，那就是在明说，手滑做不到）。
    #[test]
    fn 局部矩形加画布原点后才是全局画布矩形() {
        let local = local_rect(4.0, 6.0, 20.0, 12.0);
        let screen = local_rect_to_canvas(local, CPt(Cpx(100.0), Cpx(200.0)));
        assert_eq!(screen, (Cpx(104.0), Cpx(206.0), Cpx(20.0), Cpx(12.0)));
        assert!(hit(screen, CPt(Cpx(105.0), Cpx(207.0))));
        assert!(!hit(screen, CPt(Cpx(99.0), Cpx(207.0))));
    }

    #[test]
    fn 换算不能过第二道() {
        let once = Dpx(50.0).to_canvas();
        // 再乘一次只能**显式**绕过类型 ⇒ 不会是手滑
        let twice = Cpx(once.0 * UI_SCALE);
        assert!(
            (twice.0 - once.0).abs() > 0.5,
            "过第二道会明显跑偏（D-64 就是这个）"
        );
    }
}

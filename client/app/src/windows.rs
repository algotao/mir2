//! **窗口的开关与联动**（架构方向 ①）—— 包裹 / 状态 / 商店（买+卖）。
//!
//! ⚠️ 不碰 SDL：只有几个 `bool` 和"谁关了要连累谁"的规则 ⇒ 这些规则能进测试。
//!
//! 抽之前它们散在 `main.rs` 的事件循环里（Esc 一处、点 X 一处、货架没了
//! 一处、注销一处…），**同样的"关商店要不要连累包裹"各写了一遍**，最易漏改。
//!
//! 规则（用户 2026-10-10 第 1/7 条、2026-10-11 第 3 条）：
//!
//! - 货架**来了** ⇒ 开商店窗，并**顺带开包裹**（原版买东西时包裹就在旁边开着）；
//! - 货架**没了**（对话关了 / 换图）⇒ 商店 + 包裹**一起收**；
//! - 但只收"**因为买东西才自动开起来**"的那一份 —— 玩家自己按 F9 开的包裹
//!   不该被顺手关掉；
//! - 关**卖**窗 ⇒ 商店 + 包裹一起关，**无条件**（卖窗就是包裹的入口，D-55）；
//! - 注销 / 退出 ⇒ 全关。

/// 三个浮窗的开关状态。
#[derive(Debug, Clone, Copy, Default)]
pub(crate) struct Windows {
    bag: bool,
    status: bool,
    shop: bool,
    /// 包裹是"**因为买东西**才自动开起来的"吗 ⇒ 收摊时要不要一起收走。
    bag_by_shop: bool,
}

impl Windows {
    pub(crate) fn new() -> Self {
        Self::default()
    }

    pub(crate) fn bag(self) -> bool {
        self.bag
    }

    pub(crate) fn status(self) -> bool {
        self.status
    }

    pub(crate) fn shop(self) -> bool {
        self.shop
    }

    // ---------- 包裹 / 状态 ----------

    /// F9 或底栏那颗 ⇒ **切换**包裹（打开时页码归零见调用方）。
    pub(crate) fn toggle_bag(&mut self) {
        self.bag = !self.bag;
        if !self.bag {
            self.bag_by_shop = false; // 手动关掉了 ⇒ 不再是"因为买东西开着"
        }
    }

    /// 切状态窗（人物）。
    pub(crate) fn toggle_status(&mut self) {
        self.status = !self.status;
    }

    /// 关包裹（点 X / Esc）。
    pub(crate) fn close_bag(&mut self) {
        self.bag = false;
        self.bag_by_shop = false;
    }

    /// 关状态窗（点 X / Esc）。
    pub(crate) fn close_status(&mut self) {
        self.status = false;
    }

    // ---------- 商店 ----------

    /// **货架来了**（服务端推来新货架）⇒ 开商店窗。
    ///
    /// ⚠️ 顺带把**包裹打开**并记一笔"它是因为买东西才开的"（原版买东西时
    /// 包裹就在旁边开着，用户 2026-10-10 第 7 条）。
    pub(crate) fn open_shop(&mut self) {
        self.shop = true;
        if !self.bag {
            self.bag = true;
            self.bag_by_shop = true;
        }
    }

    /// **货架被收走**（对话关了 / 换图 / 服务端没了）⇒ 商店与包裹一起收。
    pub(crate) fn shop_gone(&mut self) {
        self.close_shop();
    }

    /// **主动关购买窗**（点 X / Esc）⇒ 商店关掉；
    /// 包裹**只有是"因为买东西才开的"才跟着关**。
    pub(crate) fn close_shop(&mut self) {
        self.shop = false;
        if self.bag_by_shop {
            self.bag = false;
            self.bag_by_shop = false;
        }
    }

    /// 关**卖**窗 ⇒ 商店 + 包裹**一起关，无条件**（D-55：卖窗就是"卖出"这件事
    /// 的入口，它关了包裹留着没意义）。
    pub(crate) fn close_sell(&mut self) {
        self.shop = false;
        self.bag = false;
        self.bag_by_shop = false;
    }

    /// 注销 / 退出 ⇒ 全关。
    pub(crate) fn close_all(&mut self) {
        self.bag = false;
        self.status = false;
        self.shop = false;
        self.bag_by_shop = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 开商店会顺带开包裹并记住() {
        let mut w = Windows::new();
        assert!(!w.bag());
        w.open_shop();
        assert!(w.shop());
        assert!(w.bag(), "买东西时包裹就该在旁边开着（用户第 7 条）");
        assert!(w.bag_by_shop);
    }

    #[test]
    fn 因为买东西开的包裹收摊时一起关() {
        let mut w = Windows::new();
        w.open_shop();
        w.close_shop();
        assert!(!w.shop());
        assert!(!w.bag(), "收摊 ⇒ 包裹一起收（用户 2026-10-10 第 1 条）");
        assert!(!w.bag_by_shop);
    }

    #[test]
    fn 自己开的包裹不该被收摊关掉() {
        let mut w = Windows::new();
        w.toggle_bag(); // 玩家自己按 F9 开的
        assert!(w.bag() && !w.bag_by_shop);
        w.open_shop(); // 货架来了（包裹已经开着 ⇒ 不重复开、也不改它的出身）
        assert!(w.bag());
        assert!(!w.bag_by_shop);
        w.close_shop();
        assert!(!w.shop());
        assert!(w.bag(), "自己开的包裹不该被顺手关掉");
    }

    #[test]
    fn 关卖窗无条件连包裹一起关() {
        let mut w = Windows::new();
        w.toggle_bag(); // 自己开的
        w.open_shop();
        w.close_sell();
        assert!(!w.shop());
        assert!(!w.bag(), "卖窗是包裹的入口 ⇒ 关了就连包裹一起收（D-55）");
    }

    #[test]
    fn 手动关包裹后不再算因商店而开() {
        let mut w = Windows::new();
        w.open_shop();
        assert!(w.bag_by_shop);
        w.close_bag();
        assert!(!w.bag());
        assert!(!w.bag_by_shop, "关掉之后出身就清了");
    }

    #[test]
    fn 关包裹不动商店() {
        let mut w = Windows::new();
        w.open_shop();
        w.close_bag();
        assert!(w.shop(), "关包裹不该把商店也关了");
    }

    #[test]
    fn 注销全关() {
        let mut w = Windows::new();
        w.open_shop();
        w.toggle_status();
        w.close_all();
        assert!(!w.bag() && !w.status() && !w.shop() && !w.bag_by_shop);
    }
}

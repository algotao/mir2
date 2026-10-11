//! **商店窗（买）的交互状态**（架构方向 ①）—— 翻页 + 选中哪一行。
//!
//! ⚠️ 不碰 SDL：只有"第几页"和"选中第几件"，于是"换了商人要不要清选中"
//! "翻页后选中要不要跟着动"这类规则都能进单元测试。

use crate::pager::Pager;
use crate::shop::ROWS;

/// 商店窗（买）的状态。
#[derive(Debug, Clone, Copy, Default)]
pub(crate) struct ShopUi {
    page: Pager,
    /// 选中的是**第几件商品**（全列表下标，不是页内行号）。
    sel: Option<usize>,
}

impl ShopUi {
    pub(crate) fn new() -> Self {
        Self {
            page: Pager::new(),
            sel: None,
        }
    }

    pub(crate) fn page(self) -> usize {
        self.page.page()
    }

    /// 选中的是第几件（`None` = 没选）。
    pub(crate) fn sel(self) -> Option<usize> {
        self.sel
    }

    /// **点了第 `row` 行**（页内行号）⇒ 选中那一件。
    pub(crate) fn click_row(&mut self, row: usize) {
        self.sel = Some(self.page.page() * ROWS + row);
    }

    /// 翻页（`dir > 0` 下一页 / `< 0` 上一页）。
    pub(crate) fn page_step(&mut self, dir: i8, items: usize) {
        self.page.step(dir, crate::shop::pages(items));
    }

    /// 滚轮翻页。
    pub(crate) fn wheel(&mut self, delta: i32, items: usize) {
        self.page.wheel(delta, crate::shop::pages(items));
    }

    /// 商品列表变了（换了商人 / 服务端刷新）⇒ **页数与选中都要夹回合法范围**。
    ///
    /// ⚠️ 不清选中：玩家在 A 商人那儿选了第 3 件，翻了一页，选中该还在。
    /// 但**列表变短**导致那件不存在了 ⇒ 必须清掉，否则点 OK 会"买了个空"。
    pub(crate) fn revalidate(&mut self, items: usize) {
        self.page.step(0, crate::shop::pages(items));
        if self.sel.is_some_and(|s| s >= items) {
            self.sel = None;
        }
    }

    /// 关窗 ⇒ 清掉选中（下次打开是干净的）。
    pub(crate) fn close(&mut self) {
        self.sel = None;
        self.page.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 点行选中的是全列表下标() {
        let mut s = ShopUi::new();
        s.click_row(2);
        assert_eq!(s.sel(), Some(2), "第 0 页第 2 行 ⇒ 第 2 件");
        s.page_step(1, 100); // 翻到第 1 页
        s.click_row(0);
        assert_eq!(s.sel(), Some(ROWS), "第 1 页第 0 行 ⇒ 第 ROWS 件");
    }

    #[test]
    fn 翻页不清选中() {
        let mut s = ShopUi::new();
        s.click_row(1);
        s.page_step(1, 100);
        assert_eq!(s.sel(), Some(1), "翻页只是换看的那一页，选中还在");
    }

    #[test]
    fn 列表变短后越界的选中要清掉() {
        let mut s = ShopUi::new();
        s.click_row(9); // 选中第 9 件
        assert_eq!(s.sel(), Some(9));
        // 换了个只有 3 件商品的商人
        s.revalidate(3);
        assert_eq!(
            s.sel(),
            None,
            "第 9 件不存在了 ⇒ 不能还留着（点 OK 会买空）"
        );
    }

    #[test]
    fn 列表变短后页码也要夹回来() {
        let mut s = ShopUi::new();
        s.page_step(1, 100);
        s.page_step(1, 100);
        assert_eq!(s.page(), 2);
        s.revalidate(ROWS); // 只剩一页
        assert_eq!(s.page(), 0);
    }

    #[test]
    fn 关窗后是干净的() {
        let mut s = ShopUi::new();
        s.click_row(3);
        s.page_step(1, 100);
        s.close();
        assert_eq!(s.sel(), None);
        assert_eq!(s.page(), 0);
    }
}

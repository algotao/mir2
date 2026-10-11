//! **翻页状态**（架构方向 ①）—— 商店 / 状态窗 / 背包共用。
//!
//! ⚠️ 不碰 SDL：只有"第几页"和"一共几页"，于是翻页的**边界**（循环、夹取、
//! 列表变短后回到合法页）都能进单元测试，不必跑起来点箭头。

/// 翻页器。
///
/// 翻页是**循环**的（官方也是：最后一页再按"下一页"回到第一页）。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct Pager {
    page: usize,
}

impl Pager {
    pub(crate) fn new() -> Self {
        Self { page: 0 }
    }

    /// 现在第几页。
    pub(crate) fn page(self) -> usize {
        self.page
    }

    /// 第几页（**直接赋值**，夹到合法范围）。
    #[allow(dead_code)] // 目前只有测试用；留着是因为"跳页"迟早要用
    pub(crate) fn set(&mut self, p: usize, pages: usize) {
        self.page = p.min(pages.saturating_sub(1));
    }

    /// 回到第一页。
    pub(crate) fn reset(&mut self) {
        self.page = 0;
    }

    /// 下一页（`dir > 0`）/ 上一页（`dir < 0`），**循环**。
    /// `dir == 0` = 只夹取（列表变短了用它把页码拉回合法范围）。
    pub(crate) fn step(&mut self, dir: i8, pages: usize) {
        let n = pages.max(1);
        self.page = if dir > 0 {
            (self.page + 1) % n
        } else if dir < 0 {
            (self.page + n - 1) % n
        } else {
            self.page.min(n - 1)
        };
    }

    /// 滚轮：`delta` 是滚动量（正 = 往后翻）。
    pub(crate) fn wheel(&mut self, delta: i32, pages: usize) {
        if delta != 0 {
            self.step(if delta > 0 { 1 } else { -1 }, pages);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 翻页是循环的() {
        let mut p = Pager::new();
        p.step(1, 3);
        assert_eq!(p.page(), 1);
        p.step(1, 3);
        assert_eq!(p.page(), 2);
        p.step(1, 3);
        assert_eq!(p.page(), 0, "最后一页再往后 ⇒ 回到第一页（官方也是循环）");
        p.step(-1, 3);
        assert_eq!(p.page(), 2, "第一页往前 ⇒ 到最后一页");
    }

    #[test]
    fn 只有一页时怎么翻都是第零页() {
        let mut p = Pager::new();
        for _ in 0..3 {
            p.step(1, 1);
        }
        assert_eq!(p.page(), 0);
        p.step(-1, 1);
        assert_eq!(p.page(), 0);
    }

    #[test]
    fn 列表变短后把页码拉回合法范围() {
        let mut p = Pager::new();
        p.set(4, 5);
        assert_eq!(p.page(), 4);
        p.step(0, 2); // 商品只剩 2 页了
        assert_eq!(p.page(), 1, "不能停在已经不存在的第 4 页");
    }

    #[test]
    fn 滚轮按方向翻一页() {
        let mut p = Pager::new();
        p.wheel(1, 3);
        assert_eq!(p.page(), 1);
        p.wheel(-1, 3);
        assert_eq!(p.page(), 0);
        p.wheel(0, 3);
        assert_eq!(p.page(), 0, "没有滚动量 ⇒ 不动");
    }
}

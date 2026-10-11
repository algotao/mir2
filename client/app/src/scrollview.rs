//! **对话窗的滚动位置**（架构方向 ①）。
//!
//! ⚠️ 不碰 SDL：只有"滚到第几行"，于是"正文变短了怎么办""换了一段新对话要不要
//! 归零"这类规则能进测试。
//!
//! 滚动的**几何**（滑块多大、轨道在哪）已经在 `dialog::dialog_*` 里；这里管的
//! 只是那个行号本身。

/// 一段正文的滚动位置。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct ScrollView {
    scroll: usize,
}

impl ScrollView {
    pub(crate) fn new() -> Self {
        Self { scroll: 0 }
    }

    /// 现在滚到第几行。
    pub(crate) fn scroll(self) -> usize {
        self.scroll
    }

    /// **换了一段新对话** ⇒ 归零。
    ///
    /// ⚠️ 不归零的话：在商人 A 那儿滚到第 20 行，再去点商人 B，B 的正文会
    /// 直接从第 20 行开始显示 —— 看着像"这段对话是空的"（用户踩过）。
    pub(crate) fn reset(&mut self) {
        self.scroll = 0;
    }

    /// 正文行数变了 ⇒ 把位置夹回合法范围（不能停在已经不存在的行）。
    pub(crate) fn clamp(&mut self, lines: usize) {
        self.scroll = crate::dialog::dialog_scroll_clamp(self.scroll, lines);
    }

    /// 滚轮：`y` 是 SDL 的滚轮量（**> 0 = 往上滚**）。
    pub(crate) fn wheel(&mut self, y: f32, lines: usize) {
        self.scroll = crate::dialog::dialog_scroll_step(self.scroll, lines, y);
    }

    /// **新的一段对话来了吗**（`seq` 是服务端那段对话的序号）。
    /// 换了 ⇒ 归零，并返回 `true`。
    pub(crate) fn on_new_text(&mut self, seq: u64, seen: u64) -> bool {
        if seq != seen {
            self.reset();
            return true;
        }
        false
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 换了新对话要归零() {
        let mut v = ScrollView::new();
        v.wheel(-1.0, 30); // 往下滚一行
        v.wheel(-1.0, 30);
        assert_eq!(v.scroll(), 2);
        assert!(v.on_new_text(7, 6), "序号变了 ⇒ 换了新对话");
        assert_eq!(v.scroll(), 0, "不归零的话新对话会从中段开始显示 ⇒ 像空的");
    }

    #[test]
    fn 同一段对话不算换() {
        let mut v = ScrollView::new();
        v.wheel(-1.0, 30);
        assert!(!v.on_new_text(7, 7));
        assert_eq!(v.scroll(), 1, "同一段 ⇒ 滚动位置要留着");
    }

    #[test]
    fn 正文变短后位置夹回范围() {
        let mut v = ScrollView::new();
        for _ in 0..20 {
            v.wheel(-1.0, 30);
        }
        assert!(v.scroll() > 0);
        v.clamp(3); // 这段对话只剩 3 行
        assert!(
            v.scroll() <= 3,
            "不能停在已经不存在的行（实得 {}）",
            v.scroll()
        );
    }

    #[test]
    fn 滚轮方向对() {
        let mut v = ScrollView::new();
        v.wheel(-1.0, 10); // 往下
        v.wheel(-1.0, 10);
        assert_eq!(v.scroll(), 2);
        v.wheel(1.0, 10); // 往上
        assert_eq!(v.scroll(), 1);
    }
}

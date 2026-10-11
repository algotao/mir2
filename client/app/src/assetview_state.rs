//! 素材观察器的纯选择状态（开发工具，无 SDL 资源）。

/// 当前查看的图库与图号。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub(crate) struct AssetViewState {
    lib_idx: usize,
    img_idx: usize,
}

impl AssetViewState {
    pub(crate) fn library(self) -> usize {
        self.lib_idx
    }

    pub(crate) fn image(self) -> usize {
        self.img_idx
    }

    pub(crate) fn previous_library(&mut self, count: usize) {
        let n = count.max(1);
        self.lib_idx = (self.lib_idx + n - 1) % n;
        self.img_idx = 0;
    }

    pub(crate) fn next_library(&mut self, count: usize) {
        let n = count.max(1);
        self.lib_idx = (self.lib_idx + 1) % n;
        self.img_idx = 0;
    }

    pub(crate) fn previous_image(&mut self) {
        self.img_idx = self.img_idx.saturating_sub(1);
    }

    pub(crate) fn next_image(&mut self) {
        self.img_idx = self.img_idx.saturating_add(1);
    }

    /// 图库视图解析到有效图号后回报选择；索引由视图适配器传回 reducer 更新。
    pub(crate) fn select_image(&mut self, index: usize) {
        self.img_idx = index;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 切换图库循环并把图号归零() {
        let mut s = AssetViewState::default();
        s.previous_image();
        assert_eq!(s.image(), 0);
        s.next_library(3);
        assert_eq!((s.library(), s.image()), (1, 0));
        s.previous_library(3);
        assert_eq!((s.library(), s.image()), (0, 0));
        s.previous_library(3);
        assert_eq!(s.library(), 2);
    }

    #[test]
    fn 图号按步进并可由视图归一() {
        let mut s = AssetViewState::default();
        s.next_image();
        s.next_image();
        s.previous_image();
        assert_eq!(s.image(), 1);
        s.select_image(17);
        assert_eq!(s.image(), 17);
    }

    #[test]
    fn 空图库数不会除零() {
        let mut s = AssetViewState::default();
        s.previous_library(0);
        assert_eq!(s.library(), 0);
        s.next_library(0);
        assert_eq!(s.library(), 0);
    }
}

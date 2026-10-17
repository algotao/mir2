//! 商店「买几个 / 卖几个」面板的**纯状态**（架构方向 ①）。
//!
//! 只算"买谁、几件、多少钱"，不碰 SDL、不发包 —— 所以开面板/加减/夹取/成交数量
//! 都能脱离窗口系统直接测（与 `shopui` / `bagdrag` 同一条纪律）。
//!
//! ⚠️ 这**不是原版**的行为：原版没有"买几个"的输入框，`submenu` 是商品明细列表
//!（`FState.pas:5036`，见 D-148 的语义订正）。面板属**私服便利**，服务端
//! `ShopBuy.count` / `ShopSell.count` 本来就有，缺的一直只是客户端入口。
//! 触发条件仍挂在 `submenu` 上（用户选 A 保留面板，触发语义未改）。

/// 这一笔交易认什么。
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Target {
    /// 买：认**商品名**（`ShopBuy` 收名字；同名的货架项只有一条）。
    Buy { npc: u64, name: String, unit: u64 },
    /// 卖：认**实例号**（原版卖货按 `MakeIndex`，见 `item.proto`）。
    Sell {
        npc: u64,
        make_index: i32,
        name: String,
        unit: u64,
    },
}

impl Target {
    /// 单价（买 = 原价；卖 = 半价）。
    pub(crate) fn unit(&self) -> u64 {
        match self {
            Self::Buy { unit, .. } | Self::Sell { unit, .. } => *unit,
        }
    }
}

/// 数量面板里点中了什么。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Hit {
    Minus,
    Plus,
    Ok,
    Cancel,
}

/// 一次最多买/卖多少 —— 防手滑；服务端还会按背包空位/真实数量再夹一次。
pub(crate) const MAX: u32 = 999;

/// 数量面板的开着的状态。
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Qty {
    target: Target,
    count: u32,
    max: u32,
    /// 键盘直接输入的那串数字（面板是模态，数字键归它）。
    ///
    /// 与 `count` 分开是为了保留前导零：`0` 先按 1 件算，继续打 `5` 就成了 05 → 5 件；
    /// 按钮/↑↓ 一碰就清空（改回"从当前件数加减"）。
    digits: String,
}

impl Qty {
    /// 点 OK 时决定要不要问数量：**只有 `submenu` 的商品才问**，
    /// 可叠加的一律按"买 1 个"直接成交（与原版一致）。
    pub(crate) fn for_buy(
        npc: u64,
        name: &str,
        price: u64,
        stock: u32,
        submenu: bool,
    ) -> Option<Self> {
        submenu.then(|| {
            Self::new(
                Target::Buy {
                    npc,
                    name: name.to_owned(),
                    unit: price,
                },
                1,
                stock,
            )
        })
    }

    /// 卖：整叠才问数量（`have == 1` ⇒ 直接卖 1 件，不用弹面板）。
    pub(crate) fn for_sell(
        npc: u64,
        make_index: i32,
        name: &str,
        unit: u64,
        have: u32,
    ) -> Option<Self> {
        (have > 1).then(|| {
            Self::new(
                Target::Sell {
                    npc,
                    make_index,
                    name: name.to_owned(),
                    unit,
                },
                have,
                have,
            )
        })
    }

    fn new(target: Target, count: u32, max: u32) -> Self {
        let max = max.clamp(1, MAX);
        Self {
            target,
            count: count.clamp(1, max),
            max,
            digits: String::new(),
        }
    }

    pub(crate) fn target(&self) -> &Target {
        &self.target
    }

    pub(crate) fn count(&self) -> u32 {
        self.count
    }

    /// 上限（面板暂不显示，留给"输入框"那条路和测试用）。
    #[allow(dead_code)]
    pub(crate) fn max(&self) -> u32 {
        self.max
    }

    /// 面板上"共 N"那个数（单价 × 件数；饱和乘法，别为一个显示数字 panic）。
    pub(crate) fn total(&self) -> u64 {
        self.target.unit().saturating_mul(u64::from(self.count))
    }

    /// 标题："买几个？ / 卖几个？"
    pub(crate) fn title(&self) -> &'static str {
        match self.target {
            Target::Buy { .. } => "买几个？",
            Target::Sell { .. } => "卖几个？",
        }
    }

    pub(crate) fn dec(&mut self) {
        self.digits.clear();
        self.count = self.count.saturating_sub(1).max(1);
    }

    pub(crate) fn inc(&mut self) {
        self.digits.clear();
        self.count = self.count.saturating_add(1).min(self.max);
    }

    /// 键盘一次调多少：`coarse`（Shift）走 10 件。
    pub(crate) fn bump(&mut self, up: bool, coarse: bool) {
        self.digits.clear();
        let step = if coarse { 10 } else { 1 };
        if up {
            self.count = self.count.saturating_add(step).min(self.max);
        } else {
            self.count = self.count.saturating_sub(step).max(1);
        }
    }

    /// 直接设件数（测试用；等价于"用按钮改成 n"）。
    #[allow(dead_code)]
    pub(crate) fn set(&mut self, n: u32) {
        self.digits.clear();
        self.count = n.clamp(1, self.max);
    }

    /// 键盘敲一个十进制位：从 0 开始重新输入，首位为 0 也先按 1 件算。
    pub(crate) fn type_digit(&mut self, d: u32) {
        // 上限 999（[`MAX`]）⇒ 三位就够；多按的位直接忽略。
        if d > 9 || self.digits.len() >= 3 {
            return;
        }
        self.digits.push(char::from(b'0' + d as u8));
        self.sync_count();
    }

    /// 退格：删最后一位；删空 ⇒ 回到 1 件。
    pub(crate) fn backspace(&mut self) {
        if self.digits.pop().is_none() {
            return;
        }
        if self.digits.is_empty() {
            self.count = 1;
        } else {
            self.sync_count();
        }
    }

    /// 正在输入的数字串（面板画光标/测试用）。
    pub(crate) fn typed(&self) -> &str {
        &self.digits
    }

    fn sync_count(&mut self) {
        let n: u32 = self.digits.parse().unwrap_or(0);
        // 0 件不成立 ⇒ 先显示 1；继续打会自然变大（"05" → 5）
        self.count = n.clamp(1, self.max);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 只有二级菜单商品才问数量() {
        assert!(
            Qty::for_buy(7, "炼狱", 3000, 5, true).is_some(),
            "submenu=1 ⇒ 弹面板"
        );
        assert!(
            Qty::for_buy(7, "金创药", 20, 200, false).is_none(),
            "可叠加物 ⇒ 直接买 1 个"
        );
    }

    #[test]
    fn 单件不弹卖数量面板() {
        assert!(Qty::for_sell(7, 42, "木剑", 50, 1).is_none());
        assert_eq!(
            Qty::for_sell(7, 42, "金创药", 10, 7).map(|q| q.count()),
            Some(7),
            "整叠卖：默认就是全部"
        );
    }

    #[test]
    fn 加减夹在1和上限之间() {
        let mut q = Qty::for_buy(7, "炼狱", 3000, 3, true).unwrap();
        assert_eq!(q.count(), 1);
        q.dec();
        assert_eq!(q.count(), 1, "不能到 0");
        q.inc();
        q.inc();
        q.inc();
        q.inc();
        assert_eq!(q.count(), 3, "不能超上限");
        q.set(99);
        assert_eq!(q.count(), 3);
        q.set(0);
        assert_eq!(q.count(), 1);
    }

    #[test]
    fn 库存为0时上限兜底成1() {
        let q = Qty::for_buy(7, "炼狱", 3000, 0, true).unwrap();
        assert_eq!((q.count(), q.max()), (1, 1));
    }

    #[test]
    fn 总价按件数与单价算() {
        let mut q = Qty::for_sell(7, 42, "金创药", 10, 7).unwrap();
        assert_eq!(q.total(), 70);
        q.set(3);
        assert_eq!(q.total(), 30);
    }

    #[test]
    fn 键盘粗调一次10件() {
        let mut q = Qty::for_buy(7, "炼狱", 3000, 25, true).unwrap();
        q.bump(true, true);
        assert_eq!(q.count(), 11);
        q.bump(false, true);
        assert_eq!(q.count(), 1, "往下也不穿过 1");
    }

    #[test]
    fn 键盘直接打件数且退格删一位() {
        let mut q = Qty::for_buy(7, "炼狱", 3000, 99, true).unwrap();
        q.type_digit(1);
        q.type_digit(2);
        assert_eq!((q.count(), q.typed()), (12, "12"));
        q.backspace();
        assert_eq!((q.count(), q.typed()), (1, "1"));
        q.backspace();
        assert_eq!((q.count(), q.typed()), (1, ""), "删空回到 1 件");
    }

    #[test]
    fn 打字超上限时夹到上限() {
        let mut q = Qty::for_buy(7, "炼狱", 3000, 5, true).unwrap();
        q.type_digit(9);
        q.type_digit(9);
        assert_eq!(q.count(), 5, "库存 5 ⇒ 显示 5");
        assert_eq!(q.typed(), "99");
        q.inc();
        assert_eq!(q.typed(), "", "一碰按钮就回到加减语义");
        assert_eq!(q.count(), 5);
    }

    #[test]
    fn 打0开头先按1件算() {
        let mut q = Qty::for_buy(7, "炼狱", 3000, 99, true).unwrap();
        q.type_digit(0);
        assert_eq!(q.count(), 1, "0 件不成立");
        q.type_digit(5);
        assert_eq!((q.count(), q.typed()), (5, "05"));
    }

    #[test]
    fn 总价溢出时饱和而不是panic() {
        let mut q = Qty::for_buy(7, "炼狱", u64::MAX / 2, MAX, true).unwrap();
        q.set(MAX);
        assert_eq!(q.total(), u64::MAX, "饱和乘法：显示最大值，不 panic");
    }
}

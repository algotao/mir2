//! **背包/卖窗的拖拽状态机**（架构方向 ① 的第一个产物 —— D-76）。
//!
//! ⚠️ 这里**不碰 SDL**：没有画布、没有纹理、没有鼠标事件，只有"点了一下"和
//! "背包里第几格是什么"。于是这些规则**可以进单元测试**，不必跑起来看
//! —— 这是把它从 `main.rs`（2600+ 行、与 SDL 缠死）里抽出来的全部理由。
//!
//! 抽之前：`main.rs` 里有**两份近乎一样**的背包点击逻辑（普通 vs 卖出模式），
//! 一边改了另一边容易忘 —— 现在只有这一份。
//!
//! 认物**只认实例号**（`make`），不认下标：背包一被服务端重排，下标就指到
//! 别的物品上了（见 D-75 的 `Drag::rebind`）。

use std::time::{Duration, Instant};

use crate::widget::{Drag, SlotRef};

/// 双击判定的窗口（原版没有双击用物，这是我们的扩展）。
const DOUBLE_CLICK_MS: u64 = 450;

/// 背包某一格"有什么"（**只留状态机需要的那几个字段**）。
///
/// ⚠️ 不从 `BagItem` 整个搬过来：那样状态机就依赖 `core::world`，测的时候得
/// 造一整个 `BagItem`。这里只留"判得出动作"的最小集。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Cell {
    /// 实例号（`MakeIndex`）。**0 = 空格**（服务端用 0 表示没有）。
    pub make: i32,
    /// 物品类别（`StdMode`）—— 用来判"双击是穿戴还是使用"。
    pub std_mode: u32,
}

/// 一次点击**要做的动作**（状态机只决定"做什么"，发包/改世界由调用方去做）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Act {
    /// 什么也不做。
    None,
    /// 修它（`@repair` / `@s_repair` 那个对话入口开着）。
    Repair { slot: usize },
    /// 双击非装备 ⇒ **使用**。
    Use { slot: usize },
    /// 双击装备 ⇒ **穿戴**。
    Equip { slot: usize },
    /// 从 `from` 挪到 `to`（原格有东西 ⇒ 交换；原格空 ⇒ 移动）。
    Move { from: usize, to: usize },
    /// 空手点一个格子 ⇒ 把那件**拿在手上**。
    PickUp { slot: usize },
    /// 拿着东西点回原格 ⇒ 放回（取消）。
    PutBack,
}

/// **拖拽状态**：手上拿着什么、卖窗槽里放着什么、上次点的是哪格（双击用）。
#[derive(Debug, Clone, Copy)]
pub(crate) struct BagDrag {
    /// 手上拿着的（跟着鼠标走）。
    held: Option<Drag>,
    /// 已经**放进卖窗圆槽**的那件。
    placed: Option<Drag>,
    /// 上次点的格子 + 时间（450ms 内同格再点 = 双击）。
    last_click: Option<(usize, Instant)>,
}

impl Default for BagDrag {
    fn default() -> Self {
        Self::new()
    }
}

impl BagDrag {
    pub(crate) fn new() -> Self {
        Self {
            held: None,
            placed: None,
            last_click: None,
        }
    }

    /// 手上拿着的那件（**背包下标**；不是从背包拿的 ⇒ `None`）。
    pub(crate) fn held_slot(&self) -> Option<usize> {
        self.held.and_then(Drag::bag_index)
    }

    /// 卖窗圆槽里那件的背包下标。
    pub(crate) fn placed_slot(&self) -> Option<usize> {
        self.placed.and_then(Drag::bag_index)
    }

    /// 手上那件的**实例号**（测试用；主循环目前只问 `held_slot()`）。
    #[allow(dead_code)]
    pub(crate) fn held_make(&self) -> Option<i32> {
        self.held.map(|d| d.make)
    }

    /// 卖窗槽里那件的**实例号**（先**只看不拿**，确认卖得出去才 `take_placed`）。
    pub(crate) fn placed_make(&self) -> Option<i32> {
        self.placed.map(|d| d.make)
    }

    /// **点了一格背包**。
    ///
    /// - `slot`：背包第几格（服务端那一格的下标，不是页内下标）；
    /// - `now`：现在（双击判定用，**由调用方给** ⇒ 测试里可以造时间）；
    /// - `repair`：`Some(特修?)` = 修理模式开着 ⇒ 点什么都是修它；
    /// - `cell`：`cell(i)` 返回第 i 格有什么（`None` = 空）。
    pub(crate) fn click_bag(
        &mut self,
        slot: usize,
        now: Instant,
        repair: Option<bool>,
        cell: impl Fn(usize) -> Option<Cell>,
    ) -> Act {
        let double = self.last_click.is_some_and(|(prev, at)| {
            prev == slot
                && now.saturating_duration_since(at) <= Duration::from_millis(DOUBLE_CLICK_MS)
        });
        self.last_click = Some((slot, now));

        // ① 修理模式最大（原版是拖进修理窗）
        if repair.is_some() {
            if cell(slot).is_some_and(|c| c.make != 0) {
                return Act::Repair { slot };
            }
            return Act::None;
        }

        // ② 双击 = 使用 / 穿戴。⚠️ 手上**空着**或**拿着的就是这一格**才认
        //（第一击已经把东西拿到手上了，第二击才算双击）
        let held_here = self.held_slot() == Some(slot);
        if double && (self.held.is_none() || held_here) {
            let Some(c) = cell(slot) else {
                return Act::None;
            };
            if c.make == 0 {
                return Act::None;
            }
            self.held = None;
            self.placed = None;
            return if crate::input::equip_slot_for_std_mode(c.std_mode).is_some() {
                Act::Equip { slot }
            } else {
                Act::Use { slot }
            };
        }

        // ③ 手上拿着 ⇒ 放到这一格（点回原格 = 放回）
        if let Some(from) = self.held_slot() {
            if from == slot {
                self.held = None;
                return Act::PutBack;
            }
            // 原格有东西 ⇒ **交换**：那一件变成"拿在手上"
            let target = cell(slot);
            self.held = target.map(|c| Drag::new(SlotRef::Bag(slot), c.make));
            self.placed = None;
            return Act::Move { from, to: slot };
        }

        // ④ 空手 ⇒ 拿起这一格
        let Some(c) = cell(slot) else {
            return Act::None;
        };
        if c.make == 0 {
            return Act::None;
        }
        self.held = Some(Drag::new(SlotRef::Bag(slot), c.make));
        self.placed = None;
        Act::PickUp { slot }
    }

    /// **点了卖窗那个圆槽**：手上拿着 ⇒ 放进去；空手 ⇒ 拿回手上。
    pub(crate) fn click_slot(&mut self) {
        let (h, p) = Drag::toggle_slot(self.held, self.placed);
        self.held = h;
        self.placed = p;
    }

    /// **点了卖窗的 [OK]** ⇒ 要卖掉槽里那件（返回它的**实例号**）。
    pub(crate) fn take_placed(&mut self) -> Option<i32> {
        let d = self.placed.take()?;
        Some(d.make)
    }

    /// 背包被服务端重排后：**按实例号重新定位**手上/槽里那件。
    /// 找不到了（用光/卖掉/被捡走）⇒ 清掉，免得动到别的物品（D-75）。
    pub(crate) fn rebind(&mut self, find: impl Fn(i32) -> Option<usize> + Copy) {
        for slot in [&mut self.held, &mut self.placed] {
            let Some(cur) = *slot else { continue };
            if cur.bag_index().is_none() {
                continue; // 不是从背包拿的（还没接线）⇒ 不动
            }
            *slot = cur.rebind(find);
        }
    }

    /// 关窗 / 收摊：手上和槽里都清掉。
    pub(crate) fn clear(&mut self) {
        self.held = None;
        self.placed = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 造一个"背包"：`[(实例号, StdMode); 8]`，0 = 空格。
    fn bag(items: &[(i32, u32)]) -> impl Fn(usize) -> Option<Cell> + '_ {
        move |i| {
            items
                .get(i)
                .and_then(|&(make, std_mode)| (make != 0).then_some(Cell { make, std_mode }))
        }
    }

    fn t0() -> Instant {
        Instant::now()
    }

    #[test]
    fn 空手点有东西的格就拿起() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        assert_eq!(d.click_bag(1, t0(), None, &b), Act::PickUp { slot: 1 });
        assert_eq!(d.held_slot(), Some(1));
        assert_eq!(d.held_make(), Some(91));
    }

    #[test]
    fn 空手点空格什么也不做() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        assert_eq!(d.click_bag(0, t0(), None, &b), Act::None);
        assert_eq!(d.held_slot(), None);
    }

    #[test]
    fn 拿着点回原格是放回() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        let t = t0();
        d.click_bag(1, t, None, &b);
        // ⚠️ 必须**过了双击窗口**才叫"放回"：450ms 内同格再点是双击（用物），
        // 那是另一条路（见 `双击穿戴与使用按类别分`）。
        assert_eq!(
            d.click_bag(1, t + Duration::from_millis(1000), None, &b),
            Act::PutBack
        );
        assert_eq!(d.held_slot(), None, "放回后手上是空的");
    }

    #[test]
    fn 拿着的放到空格是移动() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0), (0, 0)]);
        d.click_bag(1, t0(), None, &b);
        assert_eq!(d.click_bag(2, t0(), None, &b), Act::Move { from: 1, to: 2 });
        assert_eq!(d.held_slot(), None, "目标是空格 ⇒ 手上空了");
    }

    #[test]
    fn 拿着的放到有东西的格是交换且手上换成那一件() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0), (92, 0)]);
        d.click_bag(1, t0(), None, &b); // 拿起 91
        assert_eq!(
            d.click_bag(2, t0(), None, &b),
            Act::Move { from: 1, to: 2 },
            "发的是 1→2"
        );
        // 交换后 92 到了第 1 格、91 到了第 2 格 ⇒ 手上变成 92（原第 2 格那件）
        assert_eq!(d.held_slot(), Some(2), "手上换成目标格那件（下标）");
        assert_eq!(d.held_make(), Some(92), "实例号要对得上");
    }

    #[test]
    fn 双击穿戴与使用按类别分() {
        let mut d = BagDrag::new();
        // StdMode 5 = 武器 ⇒ 穿戴；0 = 药 ⇒ 使用
        let b = bag(&[(0, 0), (91, 5), (92, 0)]);
        let t = t0();
        d.click_bag(1, t, None, &b); // 第一击：拿起
        let act = d.click_bag(1, t + Duration::from_millis(100), None, &b);
        assert_eq!(act, Act::Equip { slot: 1 }, "武器 ⇒ 穿戴");
        assert_eq!(d.held_slot(), None, "穿完手上不该还有东西");

        let mut d = BagDrag::new();
        let b2 = bag(&[(0, 0), (93, 0)]);
        let t2 = t0();
        d.click_bag(1, t2, None, &b2);
        assert_eq!(
            d.click_bag(1, t2 + Duration::from_millis(100), None, &b2),
            Act::Use { slot: 1 },
            "药 ⇒ 使用"
        );
    }

    #[test]
    fn 超过窗口就不算双击() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        let t = t0();
        d.click_bag(1, t, None, &b); // 拿起
                                     // 隔了 1 秒再点同一格 ⇒ 不算双击 ⇒ 那是"放回"
        assert_eq!(
            d.click_bag(1, t + Duration::from_millis(1000), None, &b),
            Act::PutBack
        );
    }

    #[test]
    fn 修理模式下点什么都是修它() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 5)]);
        assert_eq!(
            d.click_bag(1, t0(), Some(false), &b),
            Act::Repair { slot: 1 },
            "修理模式优先于双击/移动"
        );
        assert_eq!(d.held_slot(), None);
    }

    #[test]
    fn 卖窗圆槽在手上与槽之间搬() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        d.click_bag(1, t0(), None, &b);
        d.click_slot(); // 拿着 ⇒ 放进去
        assert_eq!(d.held_slot(), None);
        assert_eq!(d.placed_slot(), Some(1));
        d.click_slot(); // 空手 ⇒ 拿回手上
        assert_eq!(d.held_slot(), Some(1));
        assert_eq!(d.placed_slot(), None);
    }

    #[test]
    #[allow(non_snake_case)]
    fn 点OK卖掉槽里那件且槽清空() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        d.click_bag(1, t0(), None, &b);
        d.click_slot();
        assert_eq!(d.take_placed(), Some(91), "卖的是实例号 91");
        assert_eq!(d.placed_slot(), None, "卖完槽要空");
        assert_eq!(d.take_placed(), None, "再点 OK 不该还能卖");
    }

    #[test]
    fn 背包重排后按实例号重定位() {
        let mut d = BagDrag::new();
        let b = bag(&[(0, 0), (91, 0)]);
        d.click_bag(1, t0(), None, &b); // 第 1 格是 91
                                        // 服务端重排：91 跑到了第 5 格
        let after = [(0i32, 0u32), (0, 0), (0, 0), (0, 0), (0, 0), (91, 0)];
        d.rebind(|mk| after.iter().position(|(m, _)| *m == mk && *m != 0));
        assert_eq!(d.held_slot(), Some(5), "下标要跟着走");
        assert_eq!(d.held_make(), Some(91));
        // 东西没了 ⇒ 清掉（否则会动到别的物品）
        d.rebind(|_| None);
        assert_eq!(d.held_slot(), None);
    }
}

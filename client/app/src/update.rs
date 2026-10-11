//! **每帧的"网络 → 状态"更新**（架构方向 ⑥：从 `main.rs` 拆出来的第一刀）。
//!
//! ⚠️ 为什么先切这块：它**不碰画布**（只改状态、发几个包），是 `main()` 里
//! 边界最清楚的一段。参数名与 `main` 里的局部变量**同名** ⇒ 函数体是原样搬过来的，
//! 没有顺手改逻辑 —— 拆 move 类的重构必须这样，混着改就看不出是不是搬错了。
//!
//! 剩下的两块（事件循环、渲染）要搬得先有 `App` 结构体把它们的状态归拢，
//! 那是一次更大的手术，留到下一轮。

use crate::net::Net;
use crate::{bagdrag, shopui, windows};

/// 一帧里"该发生什么"（不含画）。
#[allow(clippy::too_many_arguments)]
pub(crate) fn tick(
    net: &mut Option<Net>,
    shop_seen_seq: &mut u64,
    shop_ui: &mut shopui::ShopUi,
    win: &mut windows::Windows,
    drag: &mut bagdrag::BagDrag,
    belt: &mut [Option<crate::layout::BeltSlot>],
) {
    // 货架**新到了一份**（seq 变了）⇒ 开窗、翻回第一页、清选中；
    // 顺手把**包裹也打开**（原版买东西时包裹就在旁边开着，用户第 7 条）。
    let shop_seq_now = net.as_ref().map_or(0, |n| n.world.shop_seq);
    if shop_seq_now != *shop_seen_seq {
        *shop_seen_seq = shop_seq_now;
        shop_ui.close();
        if shop_seq_now > 0 {
            win.open_shop(); // 顺带把包裹打开（原版买东西时包裹就在旁边）
        }
    }
    // 商品列表可能变短（换商人 / 服务端刷新）⇒ 页码与选中夹回合法范围，
    // 否则会停在"已经不存在的第 4 页"或选中一件没有的商品（`shopui` 有测试）。
    if let Some(sh) = net.as_ref().and_then(|n| n.world.shop.as_ref()) {
        shop_ui.revalidate(sh.items.len());
    }
    // 货架被收走（对话关了 / 换图）⇒ **商品列表与包裹一起关**
    //（用户 2026-10-10 第 1 条：购买对话框关闭时，物品列表及包裹窗口也关闭）
    if net.as_ref().is_none_or(|n| n.world.shop.is_none()) && win.shop() {
        win.shop_gone();
        shop_ui.close();
        drag.clear();
    }

    // **快捷栏的失效格清理**：背包被服务端重排/物品用光后，实例号找不到了
    // ⇒ 那一格留着就是"点了没反应"，清掉。
    if let Some(n) = net.as_ref() {
        for slot in belt.iter_mut() {
            let gone = slot.is_some_and(|b| {
                !n.world
                    .bag
                    .iter()
                    .flatten()
                    .any(|it| it.make_index == b.make_index)
            });
            if gone {
                *slot = None;
            }
        }
    }

    // **手上/卖窗槽里那件的"下标重绑"**（D-75）：背包被服务端重排后，`Drag`
    // 里记的**下标**可能已经指向另一件东西 ⇒ 按实例号重新定位；找不到了就是
    // 这件没了（用光/卖掉/被捡走）⇒ 清掉，免得"放下/卖出"动到别的物品。
    // ⚠️ 卖错东西**不可恢复**，这条和下面删角确认框那条是同一类防线。
    if let Some(n) = net.as_ref() {
        drag.rebind(|mk| {
            n.world
                .bag
                .iter()
                .position(|o| o.as_ref().is_some_and(|it| it.make_index == mk))
        });
    }
}

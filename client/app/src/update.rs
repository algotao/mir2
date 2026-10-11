//! **每帧的"网络 → 状态"更新**（架构方向 ⑥：从 `main.rs` 拆出来的第一刀）。
//!
//! ⚠️ 为什么先切这块：它**不碰画布**（只改状态、发几个包），是 `main()` 里
//! 边界最清楚的一段。参数名与 `main` 里的局部变量**同名** ⇒ 函数体是原样搬过来的，
//! 没有顺手改逻辑 —— 拆 move 类的重构必须这样，混着改就看不出是不是搬错了。
//!
//! 剩下的两块（事件循环、渲染）要搬得先有 `App` 结构体把它们的状态归拢，
//! 那是一次更大的手术，留到下一轮。

use std::time::{Duration, Instant};

use crate::app_state::AppState;
use crate::hud;

/// 一帧里"该发生什么"（不含画）。
pub(crate) fn tick(app: &mut AppState) {
    // 货架**新到了一份**（seq 变了）⇒ 开窗、翻回第一页、清选中；
    // 顺手把**包裹也打开**（原版买东西时包裹就在旁边开着，用户第 7 条）。
    let shop_seq_now = app.net.as_ref().map_or(0, |n| n.world.shop_seq);
    if shop_seq_now != app.shop_seen_seq {
        app.shop_seen_seq = shop_seq_now;
        app.shop_ui.close();
        if shop_seq_now > 0 {
            app.win.open_shop(); // 顺带把包裹打开（原版买东西时包裹就在旁边）
        }
    }
    // 商品列表可能变短（换商人 / 服务端刷新）⇒ 页码与选中夹回合法范围，
    // 否则会停在"已经不存在的第 4 页"或选中一件没有的商品（`shopui` 有测试）。
    if let Some(sh) = app.net.as_ref().and_then(|n| n.world.shop.as_ref()) {
        app.shop_ui.revalidate(sh.items.len());
    }
    // 货架被收走（对话关了 / 换图）⇒ **商品列表与包裹一起关**
    //（用户 2026-10-10 第 1 条：购买对话框关闭时，物品列表及包裹窗口也关闭）
    if app.net.as_ref().is_none_or(|n| n.world.shop.is_none()) && app.win.shop() {
        app.win.shop_gone();
        app.shop_ui.close();
        app.drag.clear();
    }

    // **快捷栏的失效格清理**：背包被服务端重排/物品用光后，实例号找不到了
    // ⇒ 那一格留着就是"点了没反应"，清掉。
    if let Some(n) = app.net.as_ref() {
        for slot in app.belt.iter_mut() {
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
    if let Some(n) = app.net.as_ref() {
        app.drag.rebind(|mk| {
            n.world
                .bag
                .iter()
                .position(|o| o.as_ref().is_some_and(|it| it.make_index == mk))
        });
    }
}

/// 每帧的**世界侧状态推进**（不画）：持续挖、到位捡取、对话滚动归零。
///
/// 返回 `false` 表示原循环里那条 `continue`（挖肉目标还在、但网络会话已断）；
/// 调用方据此跳过本帧后续阶段，不把循环控制流藏进 helper。
pub(crate) fn advance_world(app: &mut AppState) -> bool {
    if let Some(tid) = app.butch_target {
        let Some(n) = app.net.as_ref() else {
            app.butch_target = None;
            return false;
        };
        if let Some(e) = n.world.entities.get(&tid) {
            if e.dead && n.world.in_world() {
                let (px, py) = n.world.self_pos;
                if (e.x - px).abs() <= 2
                    && (e.y - py).abs() <= 2
                    && app.butch_next <= Instant::now()
                {
                    let dir = crate::geom::dir_to((px, py), (e.x, e.y)).map_or(0, |d| d as i32);
                    n.butch(tid, e.x, e.y, dir);
                    app.butch_next = Instant::now() + Duration::from_millis(450);
                }
            } else {
                app.butch_target = None;
            }
        } else {
            app.butch_target = None;
        }
    }

    if let Some(n) = app.net.as_ref() {
        match app.pending_pickup {
            Some((gid, cell)) if n.world.self_pos == cell => {
                n.pickup(gid);
                app.pending_pickup = None;
            }
            Some((gid, _)) if !n.world.ground.contains_key(&gid) => app.pending_pickup = None,
            _ => {}
        }

        if app
            .dlg_scroll
            .on_new_text(n.world.dialog_seq, app.dialog_seq_seen)
        {
            app.dialog_seq_seen = n.world.dialog_seq;
        }
        if let Some((_, lines, _)) = hud::dialog_geom(app.net.as_ref(), app.dlg_scroll.scroll()) {
            app.dlg_scroll.clamp(lines.len());
        }
    }
    true
}

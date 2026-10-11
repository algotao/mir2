//! **浮窗/快捷栏按下后的纯状态转换**（架构方向 ①）。
//!
//! `world_input` 负责 SDL 坐标命中；这里接收已经命中的 `Press`，只改运行状态并返回
//! 一个待执行效果。它不发包、不打印、不画 —— 所以窗口联动、翻页、确认等分支能直接测。

use crate::app_state::AppState;
use crate::confirm::QuitAct;
use crate::widget::Press;

/// UI 状态转换产生的外部效果（由 `world_input` 执行）。
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Effect {
    None,
    Say(String),
    Notice(String),
    Buy {
        npc: u64,
        name: String,
    },
    Sell {
        npc: u64,
        make_index: i32,
        count: u32,
        name: String,
    },
    UseBelt {
        slot: usize,
        make_index: i32,
    },
    BoundBelt {
        slot: usize,
        name: String,
    },
    BagToggled {
        open: bool,
    },
    StatusToggled {
        open: bool,
    },
    MinimapToggled {
        open: bool,
    },
    Logout,
    Exit,
}

/// 对一个已经命中的控件执行**纯状态部分**，返回需要适配层执行的副作用。
pub(crate) fn reduce(app: &mut AppState, press: Press) -> Effect {
    match press {
        Press::Hud(button) => match button {
            crate::layout::HudBtn::State => {
                app.win.toggle_status();
                return Effect::StatusToggled {
                    open: app.win.status(),
                };
            }
            crate::layout::HudBtn::Bag => {
                app.win.toggle_bag();
                app.bag_page.reset();
                return Effect::BagToggled {
                    open: app.win.bag(),
                };
            }
            crate::layout::HudBtn::Magic => {
                return Effect::Say("技能窗还没做（原版 F11）。".into())
            }
            crate::layout::HudBtn::Option => return Effect::Say("选项窗还没做。".into()),
        },
        Press::Quick(button) => match button {
            crate::layout::QuickBtn::MiniMap => {
                app.minimap_on = !app.minimap_on;
                return Effect::MinimapToggled {
                    open: app.minimap_on,
                };
            }
            crate::layout::QuickBtn::Trade => return Effect::Say("玩家交易还没做。".into()),
            crate::layout::QuickBtn::Guild => return Effect::Say("行会还没做。".into()),
            crate::layout::QuickBtn::Group => return Effect::Say("组队还没做。".into()),
            crate::layout::QuickBtn::PlusAbil => return Effect::Say("属性加点还没做。".into()),
            crate::layout::QuickBtn::Logout => app.confirm.ask(QuitAct::Logout),
            crate::layout::QuickBtn::Exit => app.confirm.ask(QuitAct::Exit),
        },
        Press::Belt(slot) => {
            if let Some(idx) = app.drag.held_slot() {
                let bound = app
                    .net
                    .as_ref()
                    .and_then(|n| n.world.bag.get(idx))
                    .and_then(|it| it.as_ref())
                    .map(|it| (it.name.clone(), it.make_index, it.looks));
                app.drag.clear();
                if let Some((name, make_index, looks)) = bound {
                    if let Some(cell) = app.belt.get_mut(slot) {
                        *cell = Some(crate::layout::BeltSlot { make_index, looks });
                        return Effect::BoundBelt { slot, name };
                    }
                }
            } else if let Some(b) = app.belt.get(slot).copied().flatten() {
                return Effect::UseBelt {
                    slot,
                    make_index: b.make_index,
                };
            }
        }
        Press::BagClose => {
            app.win.close_bag();
            return Effect::Notice("背包窗关闭（点 X）".into());
        }
        Press::Status(crate::status::Hit::Close) => {
            app.win.close_status();
            return Effect::Notice("状态窗关闭（点 X）".into());
        }
        Press::Status(crate::status::Hit::Arrow(dir)) => {
            app.status_page.step(dir, crate::status::PAGES);
            return Effect::Notice(format!("状态窗翻到第 {} 页", app.status_page.page()));
        }
        Press::Status(_) => {}
        Press::Shop(crate::shop::Hit::Prev) => {
            let items = app
                .net
                .as_ref()
                .and_then(|n| n.world.shop.as_ref())
                .map_or(0, |shop| shop.items.len());
            app.shop_ui.page_step(-1, items);
        }
        Press::Shop(crate::shop::Hit::Next) => {
            let items = app
                .net
                .as_ref()
                .and_then(|n| n.world.shop.as_ref())
                .map_or(0, |shop| shop.items.len());
            app.shop_ui.page_step(1, items);
        }
        Press::Shop(crate::shop::Hit::Ok) => {
            if let Some((npc, name)) = app
                .net
                .as_ref()
                .and_then(|n| n.world.shop.as_ref())
                .and_then(|shop| {
                    app.shop_ui
                        .sel()
                        .and_then(|selected| shop.items.get(selected))
                        .map(|item| (shop.npc_id, item.name.clone()))
                })
            {
                return Effect::Buy { npc, name };
            }
            if app.net.as_ref().is_some_and(|n| n.world.shop.is_some()) {
                return Effect::Say("先点一件商品再点 OK".into());
            }
        }
        Press::Shop(crate::shop::Hit::Close) => {
            app.win.close_shop();
            app.shop_ui.close();
            return Effect::Notice("商店窗关闭（素材关闭钮）".into());
        }
        Press::Shop(_) => {}
        Press::Sell(crate::shop::SellHit::Close) => {
            app.win.close_sell();
            app.drag.clear();
            return Effect::Notice("卖货窗关闭（同步收包裹）".into());
        }
        Press::Sell(crate::shop::SellHit::Slot) => app.drag.click_slot(),
        Press::Sell(crate::shop::SellHit::Ok) => {
            let Some(make_index) = app.drag.placed_make() else {
                return Effect::None;
            };
            let Some((npc, count, name)) = app.net.as_ref().and_then(|n| {
                let shop = n.world.shop.as_ref()?;
                let item = n
                    .world
                    .bag
                    .iter()
                    .flatten()
                    .find(|item| item.make_index == make_index)?;
                Some((shop.npc_id, item.count.max(1), item.name.clone()))
            }) else {
                return Effect::None;
            };
            return Effect::Sell {
                npc,
                make_index,
                count,
                name,
            };
        }
        Press::Sell(_) => {}
        Press::ConfirmOk => match app.confirm.confirm() {
            Some(QuitAct::Logout) => return Effect::Logout,
            Some(QuitAct::Exit) => return Effect::Exit,
            None => {}
        },
        Press::ConfirmCancel => app.confirm.cancel(),
        Press::Login(_) | Press::Select(_) | Press::NewChar(_) => {}
    }
    Effect::None
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app_state::Scene;

    #[test]
    #[allow(non_snake_case)]
    fn 控件状态转换不需要网络和SDL() {
        let mut app = AppState::new();
        assert!(!app.win.bag());
        assert_eq!(
            reduce(&mut app, Press::Hud(crate::layout::HudBtn::Bag)),
            Effect::BagToggled { open: true }
        );
        assert!(app.win.bag(), "HUD 包裹按钮应只改变窗口状态");

        assert_eq!(
            reduce(&mut app, Press::Quick(crate::layout::QuickBtn::Logout)),
            Effect::None
        );
        assert_eq!(app.confirm.pending(), Some(QuitAct::Logout));
        assert_eq!(reduce(&mut app, Press::ConfirmCancel), Effect::None);
        assert!(!app.confirm.is_open(), "取消确认框只改状态、不发副作用");
    }

    #[test]
    fn 状态页箭头由reducer推进() {
        let mut app = AppState::new();
        reduce(&mut app, Press::Status(crate::status::Hit::Arrow(1)));
        assert_eq!(app.status_page.page(), 1);
        reduce(&mut app, Press::Status(crate::status::Hit::Arrow(1)));
        assert_eq!(app.status_page.page(), 0, "状态页翻页循环");
    }

    #[test]
    fn 确认退出返回效果而不执行退出() {
        let mut app = AppState::new();
        app.confirm.ask(QuitAct::Exit);
        assert_eq!(reduce(&mut app, Press::ConfirmOk), Effect::Exit);
        assert_eq!(app.scene, Scene::Login, "纯reducer不碰场景/循环控制");
    }

    #[test]
    fn 卖窗圆槽press通过reducer在手上与槽之间搬运() {
        let mut app = AppState::new();
        let act = app.drag.click_bag(0, std::time::Instant::now(), None, |i| {
            (i == 0).then_some(crate::bagdrag::Cell {
                make: 42,
                std_mode: 0,
            })
        });
        assert_eq!(act, crate::bagdrag::Act::PickUp { slot: 0 });
        assert_eq!(
            reduce(&mut app, Press::Sell(crate::shop::SellHit::Slot)),
            Effect::None
        );
        assert_eq!(app.drag.held_slot(), None);
        assert_eq!(app.drag.placed_make(), Some(42));
        reduce(&mut app, Press::Sell(crate::shop::SellHit::Slot));
        assert_eq!(app.drag.held_slot(), Some(0));
        assert_eq!(app.drag.placed_make(), None);
    }

    #[test]
    #[allow(non_snake_case)]
    fn 无网络时商店OK不产生网络效果() {
        let mut app = AppState::new();
        assert_eq!(
            reduce(&mut app, Press::Shop(crate::shop::Hit::Ok)),
            Effect::None
        );
    }
}

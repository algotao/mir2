//! **画一帧**（架构方向 ⑥）：把 UI/世界/选角/登录/素材浏览器的绘制从 `main()` 拆开。
//!
//! `AppState` 是纯运行态，SDL/纹理缓存作为显式参数传入；函数只负责画，除光标模式与
//! `app.hover/app.mode/app.cursor_is_cross` 这些本来就是 draw 结果的状态外不改游戏数据。

use std::collections::HashMap;
use std::path::PathBuf;

use mir2_core::m2pk::Archive;
use mir2_core::map::TileDraw;
use mir2_core::wzl::Wzl;
use sdl3::mouse::Cursor;
use sdl3::render::{Texture, TextureCreator, WindowCanvas};

use crate::actor::SpriteCache;
use crate::app_state::AppState;
use crate::font::TextCache;
use crate::tiles::TileTex;
use crate::ui::UiCache;
use crate::*;

#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_frame<'a, T>(
    app: &mut AppState,
    canvas: &mut WindowCanvas,
    tex_creator: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    ui_texts: &mut TextCache<'a>,
    names: &mut TextCache<'a>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<crate::tiles::TileKey, TileTex<'a>>,
    draws: &mut Vec<TileDraw>,
    sprites: &mut SpriteCache<'a>,
    asset_dir: &Option<PathBuf>,
    archive: Option<&Archive>,
    cursor_arrow: &Cursor,
    cursor_cross: &Cursor,
    status: &str,
    loaded: &mut Option<(usize, Wzl)>,
    sprite_tex: &mut Option<Texture<'a>>,
    lib_idx: usize,
    img_idx: &mut usize,
) -> Result<(), sdl3::Error> {
    let ani_count = (app.started.elapsed().as_millis() / 50) as u32;

    canvas.set_draw_color(C_BG);
    canvas.clear();

    // 悬停的那个实体（由 `draw_map_view` 按精灵落点算出来）—— 光标/高亮都用它
    if app.scene == crate::app_state::Scene::World {
        app.hover = draw_map_view(
            canvas,
            tex_creator,
            names,
            ui_texts,
            ui,
            libs,
            tiles,
            draws,
            sprites,
            asset_dir,
            &app.map,
            &app.map_err,
            app.cam,
            app.map_i,
            archive.as_ref().map(|a| a.len()).unwrap_or(0),
            app.debug,
            app.layers,
            app.mouse,
            ani_count,
            app.net.as_ref(),
            app.combat_target,
            app.dlg_scroll.scroll(),
            &app.ui_click, // 控件引擎（底栏按钮的 hover/按下）
        )?;
        // **快捷（腰带）栏**：画在世界与 HUD 之上（框在底栏里、内容在这里）。
        if app.scene == crate::app_state::Scene::World {
            if let Some(dir) = asset_dir.as_deref() {
                hud::draw_belt(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    &app.belt,
                    match app.ui_click.held() {
                        Some(Press::Belt(i)) => Some(i),
                        _ => None,
                    },
                )?;
            }
        }
        // **退出 / 注销的确认框**（模态）⇒ 画在所有窗之上
        if let (crate::app_state::Scene::World, Some(act)) = (app.scene, app.confirm.pending()) {
            if let Some(dir) = asset_dir.as_deref() {
                hud::draw_confirm(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    (WIN_W, WIN_H),
                    act.prompt(),
                )?;
            }
        }
        // 背包窗画在**世界与 HUD 之上**（它是浮窗；原版也是最后贴）。
        // 素材目录缺失时和 `draw_map_view` 一样什么都不画（那屏已经打了横幅提示）。
        if app.win.bag() {
            if let Some(dir) = asset_dir.as_deref() {
                // 抓在手上 / 放进卖窗圆槽的那件 ⇒ 原格子隐藏（用户 2026-10-10）
                let mut hide = Vec::new();
                if let Some(h) = app.drag.held_slot() {
                    hide.push(h);
                }
                if let Some(p) = app.drag.placed_slot() {
                    hide.push(p);
                }
                // 关闭钮的 hover（高亮素材 64 叠上去）
                hud::draw_bag(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    app.net.as_ref(),
                    app.bag_page.page(),
                    app.win.shop(),
                    &hide,
                    &app.ui_click,
                )?;
            }
        }
        // ⚠️ 买/卖是**两个不同的窗**（共用同一次货架数据，`ShopList.mode` 区分）：
        // 买 ⇒ 商品列表窗；卖 ⇒ "卖:"条 + 悬挂的放物品槽 + OK。
        // 三块绘制**并列**（上一版把卖窗嵌在购买窗的绘制块里，加了"卖模式不画
        // 购买窗"的条件后把整个块短路了 ⇒ 卖窗永远不出现 —— 用户 2026-10-10 截图）。
        let shop_is_sell = app
            .net
            .as_ref()
            .and_then(|n| n.world.shop.as_ref())
            .is_some_and(|s| s.is_sell());
        if let Some(dir) = asset_dir.as_deref() {
            // ① 商店窗（买）：商品列表
            if app.win.shop() && !shop_is_sell {
                // 商店窗的悬停（OK 按钮的 hover 态要用）
                // 高亮只在**按住**时展现（原版 `Downed`；不是 hover）
                shop::draw(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    app.net.as_ref(),
                    app.shop_ui.page(),
                    app.shop_ui.sel(),
                    &app.ui_click,
                )?;
            }
            // ② 卖货窗（卖）：放物品的槽 + OK（hover 时叠高亮素材 393）
            if app.win.shop() && app.win.bag() && shop_is_sell {
                shop::draw_sell(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    app.net.as_ref(),
                    app.drag.placed_slot(),
                    &app.ui_click,
                )?;
            }
            // ③ 物品悬停提示：鼠标压在包裹格上 ⇒ 显示名字/类别/耐久/卖价
            if app.win.bag() {
                if let Some(n) = app.net.as_ref() {
                    let (bx, by) = crate::layout::bag_pos(app.win.shop());
                    let lx = app.mouse.0 - bx;
                    let ly = app.mouse.1 - by;
                    if let Some(slot) = crate::layout::bag_slot_at((lx, ly)) {
                        let idx = app.bag_page.page() * crate::layout::BAG_PAGE_SLOTS + slot;
                        if let Some(Some(it)) = n.world.bag.get(idx) {
                            if it.make_index != 0 && app.drag.held_slot().is_none() {
                                hud::draw_item_tip(canvas, tex_creator, ui_texts, app.mouse, it)?;
                            }
                        }
                    }
                }
            }
            // ④ 抓在手上的物品：跟着鼠标走（原版就是把东西"拿在手里"）。
            //
            // ⚠️ 必须画在卖窗**之后**（最上层）：原来夹在买窗与卖窗之间 ⇒
            // 物品移到卖窗上时被卖窗遮住（用户 2026-10-10）。
            if let Some(held) = app.drag.held_slot() {
                if let Some(n) = app.net.as_ref() {
                    if let Some(Some(it)) = n.world.bag.get(held) {
                        if let Some((iw, ih)) = ui.size(dir, "Items", it.looks) {
                            let _ = ui.draw_src(
                                canvas,
                                tex_creator,
                                dir,
                                "Items",
                                it.looks,
                                FRect::new(0.0, 0.0, iw as f32, ih as f32),
                                FRect::new(
                                    app.mouse.0 - 4.0,
                                    app.mouse.1 - 4.0,
                                    iw as f32,
                                    ih as f32,
                                ),
                                255,
                            );
                        }
                    }
                }
            }
        }
        // 光标跟着悬停状态走：悬停的是**怪**才换准星（原版悬停谁都不换光标，
        // 这是 Crystal 那套；换成"能打的东西"上才有意义）
        let want_cross = app.hover.is_some_and(|id| {
            app.net.as_ref().is_some_and(|n| {
                n.world
                    .entities
                    .get(&id)
                    .is_some_and(|e| e.kind == mir2_core::world::KIND_MONSTER)
            })
        });
        if want_cross != app.cursor_is_cross {
            app.cursor_is_cross = want_cross;
            if want_cross {
                cursor_cross.set();
            } else {
                cursor_arrow.set();
            }
        }

        // 小地图 / 大地图（原版 `PlayScn.pas:791` 的 `DrawMiniMap`）
        draw_minimaps(
            canvas,
            tex_creator,
            ui,
            asset_dir,
            names,
            app.net.as_ref().filter(|n| n.world.in_world()).map(|n| {
                (
                    n.world.minimap_index,
                    // ⚠️ 取**补间后**的位置（与画精灵同一份）：服务端位置只在
                    // "到位"时变 ⇒ 拿它当图心就是"跑完一格图才跳一格"（用户报的）。
                    self_render_pos(n.anims.get(&n.world.self_id), n.world.self_pos, app.started),
                    // 区域标注按**地图显示名**查（服务端下发的 `map_title` 就是它）
                    n.world.map_title.as_str(),
                )
            }),
            app.minimap_on,
            app.bigmap_on,
            (WIN_W, WIN_H),
        )?;
        // 状态窗（F10）画在**小地图之后**：两者都在右上（官方也是贴右缘），
        // 窗口该压在小地图上面，否则标题栏与关闭 X 会被盖住（命中与视觉就不一致了）。
        if app.win.status() {
            if let Some(dir) = asset_dir.as_deref() {
                // 关闭钮的高亮 371 只在**按住**时叠（原版 `DCloseState` 走 `Downed`）
                status::draw(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    app.net.as_ref(),
                    app.status_page.page(),
                    &app.ui_click,
                )?;
            }
        }
    } else if app.scene == crate::app_state::Scene::Select {
        if let Some(scene) = app.select_scene.as_ref() {
            // 鼠标位置（**设计空间**）⇒ 只作为视图参数传入，不写回运行态
            let mouse = {
                let p = ui::ui_inv_pt(app.mouse);
                (p.0 .0, p.1 .0)
            };
            scene.draw(
                &mut app.select_view,
                canvas,
                tex_creator,
                ui,
                ui_texts,
                asset_dir,
                // ⚠️ 给的是**设计尺寸**（800×600）：版式算在设计空间里，画的时候整体乘 1.28
                UI_WIN,
                mouse,
                &app.ui_click,
            )?;
        }
    } else if app.scene == crate::app_state::Scene::Login {
        app.login.draw(
            canvas,
            ui,
            ui_texts,
            tex_creator,
            asset_dir,
            // ⚠️ 同上：设计尺寸
            UI_WIN,
            app.started,
            &app.ui_click,
        )?;
    } else {
        draw_asset_view(
            canvas,
            tex_creator,
            asset_dir,
            loaded,
            sprite_tex,
            status,
            app.music_on,
            lib_idx,
            img_idx,
            app.started,
        )?;
    }

    Ok(())
}

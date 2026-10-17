//! **画一帧**（架构方向 ⑥）：把 UI/世界/选角/登录/素材浏览器的绘制从 `main()` 拆开。
//!
//! `AppState` 是纯运行态，SDL/纹理缓存作为显式参数传入；函数只负责画，除光标模式与
//! 场景状态只读；hover 与系统光标只作为帧输出/本地 view cache，不写回游戏 AppState。

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

/// 纯视图缓存：鼠标命中、光标选择与懒计算素材落点。生命周期属于 renderer，不属于游戏状态。
#[derive(Default)]
pub(crate) struct FrameState {
    pub hover: Option<u64>,
    cursor_is_cross: bool,
    select_view: select::view::ViewState,
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_frame<'a, T>(
    app: &AppState,
    frame: &mut FrameState,
    canvas: &mut WindowCanvas,
    tex_creator: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    ui_texts: &mut TextCache<'a>,
    names: &mut TextCache<'a>,
    shop_texts: &mut TextCache<'a>,
    // 小地图区域名的专用小字号（`MAP_LABEL_PX`）
    map_texts: &mut TextCache<'a>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<crate::tiles::TileKey, TileTex<'a>>,
    draws: &mut Vec<TileDraw>,
    sprites: &mut SpriteCache<'a>,
    asset_dir: &Option<PathBuf>,
    archive: Option<&Archive>,
    cursor_arrow: &Cursor,
    cursor_cross: &Cursor,
    loaded: &mut Option<(usize, crate::assets::ViewerLib)>,
    sprite_tex: &mut Option<Texture<'a>>,
) -> Result<Option<usize>, sdl3::Error> {
    let ani_count = (app.started.elapsed().as_millis() / 50) as u32;

    canvas.set_draw_color(C_BG);
    canvas.clear();
    // 命中结果是本帧的渲染/输入适配数据，不写进游戏 AppState。
    let mut hover = None;
    let mut asset_image_index = None;

    // 悬停的那个实体（由 `draw_map_view` 按精灵落点算出来）—— 光标/高亮都用它
    if app.scene == crate::app_state::Scene::World {
        hover = draw_map_view(
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
            app.world_control.combat_target,
            app.dlg_scroll.scroll(),
            &app.ui_click, // 控件引擎（底栏按钮的 hover/按下）
        )?;
        // **快捷（腰带）栏**：画在世界与 HUD 之上（框在底栏里、内容在这里）。
        if app.scene == crate::app_state::Scene::World {
            if let Some(dir) = asset_dir.as_deref() {
                hud::draw_skill_bar(
                    canvas,
                    tex_creator,
                    ui,
                    ui_texts,
                    dir,
                    app.net.as_ref(),
                    match app.ui_click.held() {
                        Some(Press::SkillBar(i)) => Some(i),
                        _ => None,
                    },
                )?;
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
        // **聊天输入行**（D-152）：贴在聊天区最底一行。画在 HUD 之上、窗口之下
        //（窗口盖住聊天区时，输入行也该被盖住，与原版层序一致）。
        if let Some(ci) = app.chat_input.as_ref() {
            hud::draw_chat_input(canvas, tex_creator, names, ci.text())?;
        }
        let shop_is_sell = app
            .net
            .as_ref()
            .and_then(|net| net.world.shop.as_ref())
            .is_some_and(|shop| shop.is_sell());
        let minimap_world = app
            .net
            .as_ref()
            .filter(|net| net.world.in_world())
            .map(|net| {
                (
                    net.world.minimap_index,
                    self_render_pos(
                        net.anims.get(&net.world.self_id),
                        net.world.self_pos,
                        app.started,
                    ),
                    net.world.map_title.as_str(),
                )
            });

        // 绘制与 world_input 命中共用同一层表。Dialog 已由 draw_map_view 画在底层；
        // 其余窗口严格按 WorldUiLayer 从低到高绘制。
        for layer in crate::layout::WORLD_UI_STACK {
            match layer {
                crate::layout::WorldUiLayer::Dialog => {
                    // 已在 `draw_map_view` 的基础世界/HUD 阶段绘制。
                }
                crate::layout::WorldUiLayer::Bag => {
                    if app.win.bag() {
                        if let Some(dir) = asset_dir.as_deref() {
                            let mut hide = Vec::new();
                            if let Some(held) = app.drag.held_slot() {
                                hide.push(held);
                            }
                            if let Some(placed) = app.drag.placed_slot() {
                                hide.push(placed);
                            }
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
                }
                crate::layout::WorldUiLayer::Shop => {
                    if app.win.shop() && !shop_is_sell {
                        if let Some(dir) = asset_dir.as_deref() {
                            shop::draw(
                                canvas,
                                tex_creator,
                                ui,
                                shop_texts,
                                dir,
                                app.net.as_ref(),
                                app.shop_ui.page(),
                                app.shop_ui.sel(),
                                &app.ui_click,
                            )?;
                        }
                        // 「买几个」面板跟着买窗这一层（它在对话窗右侧，见 D-148）
                        if let Some(q) = app.qty.as_ref() {
                            shop::draw_qty(canvas, tex_creator, shop_texts, q, &app.ui_click)?;
                        }
                    }
                }
                crate::layout::WorldUiLayer::Sell => {
                    if app.win.shop() && app.win.bag() && shop_is_sell {
                        if let Some(dir) = asset_dir.as_deref() {
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
                        // 「卖几个」面板跟着卖窗这一层
                        if let Some(q) = app.qty.as_ref() {
                            shop::draw_qty(canvas, tex_creator, ui_texts, q, &app.ui_click)?;
                        }
                    }
                }
                crate::layout::WorldUiLayer::BagOverlay => {
                    if let Some(dir) = asset_dir.as_deref() {
                        // 物品悬停提示统一在 `hover_tip`（所有窗口画完之后）——
                        // 这里只画"手上拿着的那件"跟着鼠标走的图标。
                        if let Some(held) = app.drag.held_slot() {
                            if let Some(net) = app.net.as_ref() {
                                if let Some(Some(item)) = net.world.bag.get(held) {
                                    if let Some((width, height)) = ui.size(dir, "Items", item.looks)
                                    {
                                        let _ = ui.draw_src(
                                            canvas,
                                            tex_creator,
                                            dir,
                                            "Items",
                                            item.looks,
                                            FRect::new(0.0, 0.0, width as f32, height as f32),
                                            FRect::new(
                                                app.mouse.0 - 4.0,
                                                app.mouse.1 - 4.0,
                                                width as f32,
                                                height as f32,
                                            ),
                                            255,
                                        );
                                    }
                                }
                            }
                        }
                    }
                }
                crate::layout::WorldUiLayer::Minimap | crate::layout::WorldUiLayer::Bigmap => {
                    let small = layer == crate::layout::WorldUiLayer::Minimap;
                    draw_minimaps(
                        canvas,
                        tex_creator,
                        ui,
                        asset_dir,
                        map_texts,
                        minimap_world,
                        small && app.minimap_on,
                        !small && app.bigmap_on,
                        (WIN_W, WIN_H),
                    )?;
                }
                crate::layout::WorldUiLayer::Status => {
                    if app.win.status() {
                        if let Some(dir) = asset_dir.as_deref() {
                            status::draw(
                                canvas,
                                tex_creator,
                                ui,
                                ui_texts,
                                dir,
                                app.net.as_ref(),
                                app.status_page.page(),
                                app.magic_page.page(),
                                app.magic_selected,
                                &app.ui_click,
                            )?;
                        }
                    }
                }
                crate::layout::WorldUiLayer::Confirm => {
                    if let (crate::app_state::Scene::World, Some(act)) =
                        (app.scene, app.confirm.pending())
                    {
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
                }
            }
        }
        // **物品悬停提示**（最上层；原版 `ShowHint` 也画在所有窗之上）。
        // 悬停判定全在 `hover_tip` 里（与各窗自己的命中函数同源）。
        if let Some(lines) = hover_tip(app) {
            hint::draw(
                canvas,
                tex_creator,
                ui_texts,
                &lines,
                app.mouse,
                (WIN_W, WIN_H),
            )?;
        }
        // 光标跟着悬停状态走：悬停的是**怪**才换准星（原版悬停谁都不换光标，
        // 这是 Crystal 那套；换成"能打的东西"上才有意义）
        let want_cross = hover.is_some_and(|id| {
            app.net.as_ref().is_some_and(|net| {
                net.world
                    .entities
                    .get(&id)
                    .is_some_and(|entity| entity.kind == mir2_core::world::KIND_MONSTER)
            })
        });
        if want_cross != frame.cursor_is_cross {
            frame.cursor_is_cross = want_cross;
            if want_cross {
                cursor_cross.set();
            } else {
                cursor_arrow.set();
            }
        }
    } else if app.scene == crate::app_state::Scene::Select {
        if let Some(scene) = app.select_scene.as_ref() {
            // 鼠标位置（**设计空间**）⇒ 只作为视图参数传入，不写回运行态
            let mouse = {
                let p = ui::ui_inv_pt(crate::widget::canvas_pt(app.mouse));
                (p.0 .0, p.1 .0)
            };
            scene.draw(
                &mut frame.select_view,
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
        asset_image_index = draw_asset_view(
            canvas,
            tex_creator,
            asset_dir,
            loaded,
            sprite_tex,
            &app.status,
            app.music_on,
            app.asset_view.library(),
            app.asset_view.image(),
            app.started,
        )?;
    }

    frame.hover = hover;
    Ok(asset_image_index)
}

/// 鼠标下面那件东西的提示行（**悬停判定的唯一入口**）。
///
/// 层序照 [`crate::layout::WORLD_UI_STACK`]：状态窗 → 买窗 → 背包 → 腰带（HUD 最底层）。
/// 每处都用**它自己的命中函数**（`status::hit` / `shop::hit` / `layout::bag_slot_at`），
/// 于是"能点到的位置 ⇔ 会出提示的位置"，提示不会跑到空白处。
///
/// 窗口内的空白处直接 `return None`：状态窗/买窗/背包是不透的，鼠标压在上面时
/// 不该透出下一层的提示。
fn hover_tip(app: &AppState) -> Option<Vec<String>> {
    if app.layout_edit.on {
        return None;
    }
    let n = app.net.as_ref()?;
    if !n.world.in_world() {
        return None;
    }
    let m = app.mouse;
    let inside =
        |r: (f32, f32, f32, f32)| m.0 >= r.0 && m.0 < r.0 + r.2 && m.1 >= r.1 && m.1 < r.1 + r.3;

    // ① 状态窗（z 最高）。只有**装备页**的槽有图标；属性页同坐标是文字。
    if app.win.status() {
        let (x, y, w, h) = status::panel_rect();
        if inside((x, y, w, h)) {
            if app.status_page.page() == 0 {
                if let status::Hit::Slot(slot) = status::hit((m.0 - x, m.1 - y), 0) {
                    if let Some(Some(it)) = n.world.equip.get(slot) {
                        return Some(bag_lines(it));
                    }
                }
            }
            return None;
        }
    }

    // ② 买窗的货架行（卖窗没有列表；它的槽里是手上拖着的物品）
    if app.win.shop() {
        if let Some(s) = n.world.shop.as_ref().filter(|s| !s.is_sell()) {
            let (x, y, w, h) = shop::panel();
            if inside((x, y, w, h)) {
                // 与 `shop::draw` 同一条 clamp：页码越界时按最后一页算
                let page = app.shop_ui.page().min(shop::pages(s.items.len()) - 1);
                let base = page * shop::ROWS;
                if let shop::Hit::Row(i) = shop::hit((m.0 - x, m.1 - y), s.items.len()) {
                    if let Some(it) = s.items.get(base + i) {
                        return Some(hint::shop_lines(&it.name, it.price, it.dura_max, it.stock));
                    }
                }
                return None;
            }
        }
    }

    // ③ 背包。手上拿着/槽里放着的那格被 `draw_bag` 隐藏了，提示也不出。
    if app.win.bag() {
        let (rect, page) = hud::bag_geom(n.world.bag.len(), app.bag_page.page(), app.win.shop());
        if inside(rect) {
            if let Some(slot) = crate::layout::bag_slot_at((m.0 - rect.0, m.1 - rect.1)) {
                let idx = page * crate::layout::BAG_PAGE_SLOTS + slot;
                if app.drag.held_slot() != Some(idx) && app.drag.placed_slot() != Some(idx) {
                    if let Some(Some(it)) = n.world.bag.get(idx) {
                        if it.make_index != 0 {
                            return Some(bag_lines(it));
                        }
                    }
                }
            }
            return None;
        }
    }

    // ④ 腰带（HUD 最底层，窗口都没压住时才轮到它）。
    if let Some(i) = crate::layout::belt_slot_at(crate::widget::canvas_pt(m)) {
        if let Some(b) = app.belt.get(i).copied().flatten() {
            // 腰带只记实例号 ⇒ 回背包取名字/持久/卖价（背包会被重排，不能存格位号）
            if let Some(it) = n
                .world
                .bag
                .iter()
                .flatten()
                .find(|it| it.make_index == b.make_index)
            {
                return Some(bag_lines(it));
            }
        }
    }
    None
}

/// `BagItem` → 提示行（背包/装备/腰带三处同一口径）。
fn bag_lines(it: &mir2_core::world::BagItem) -> Vec<String> {
    hint::item_lines(&it.name, it.count, it.dura, it.dura_max, it.sell_price)
}

//! 世界/UI 鼠标交互与其共享动作（架构方向 ⑥）。
//!
//! 把键盘共用的快捷栏/窗口小动作，以及世界模式 MouseDown/MouseUp、滚轮的 UI 命中
//! 从 `runtime` 的事件 match 拿出来。原分支的覆盖顺序和 early-return 保持原样：
//! 确认框/浮窗消费的点击不会穿透成走路、挖肉或点 NPC。

use std::collections::HashMap;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use sdl3::mouse::MouseButton;

use crate::app_state::AppState;
use crate::*;

/// 往聊天区说一句（点了个"还没做"的按钮时用 —— **绝不静默**）。
fn ui_say(net: Option<&mut Net>, msg: &str) {
    println!("[ui] {msg}");
    if let Some(n) = net {
        n.chat.push(msg.to_string(), C_CHAT_SYS);
    }
}

/// 开关状态窗（F10 与底栏那颗 [人物] 都走这里 —— 鼠标与键盘必须同一个动作）。
pub(crate) fn toggle_status(win: &mut windows::Windows) {
    win.toggle_status();
    println!("[ui] 状态窗 {}", if win.status() { "打开" } else { "关闭" });
}

/// 用快捷栏第 `i` 格：按实例号回**背包**找当前格位再用（背包会被重排 ⇒
/// 不能存格位号）。找不到就清掉那一格并明说一句。
fn belt_use(
    net: &mut Option<Net>,
    belt: &mut [Option<crate::layout::BeltSlot>; crate::layout::BELT_SLOTS],
    i: usize,
    make_index: i32,
) {
    let Some(n) = net.as_mut() else {
        return;
    };
    match n
        .world
        .bag
        .iter()
        .position(|it| it.as_ref().is_some_and(|b| b.make_index == make_index))
    {
        Some(slot) => {
            let name = n.world.bag[slot]
                .as_ref()
                .map(|b| b.name.clone())
                .unwrap_or_default();
            n.bag_use(slot as u32);
            println!("[ui] 快捷栏 {} ⇒ 使用 {}", i + 1, name);
        }
        None => {
            belt[i] = None;
            n.chat.push(
                format!("快捷栏 {} 的东西已经不在背包里了。", i + 1),
                C_CHAT_SYS,
            );
        }
    }
}

/// 数字键 1..6 ⇒ 用快捷栏那一格（**与点它同一个动作**）。空就明说一句。
pub(crate) fn belt_key(
    net: &mut Option<Net>,
    belt: &mut [Option<crate::layout::BeltSlot>; crate::layout::BELT_SLOTS],
    i: usize,
) {
    match belt[i] {
        Some(b) => belt_use(net, belt, i, b.make_index),
        None => ui_say(
            net.as_mut(),
            &format!("快捷栏 {i} 是空的（把物品拖进去）。"),
        ),
    }
}

/// 开关小地图（**Tab 与底栏那颗 [小地图] 都走这里** —— 鼠标与键盘同一个动作）。
pub(crate) fn toggle_minimap(minimap_on: &mut bool) {
    *minimap_on = !*minimap_on;
    println!("[ui] 小地图 {}", if *minimap_on { "打开" } else { "关闭" });
}

/// 开关背包（**F9 与底栏那颗 [包裹] 都走这里**）。
///
/// ⚠️ 原来 F9 那支还顺手把 `bag_page` 归零 —— 抽出来就是为了"点按钮"和"按 F9"
/// **一模一样**（用户 2026-10-11：鼠标行为与键盘行为要一致），别一处归零一处不归。
pub(crate) fn toggle_bag(win: &mut windows::Windows, bag_page: &mut pager::Pager) {
    win.toggle_bag();
    bag_page.reset();
    println!("[ui] 背包窗 {}", if win.bag() { "打开" } else { "关闭" });
}

/// 执行 `bagdrag` 给的动作（**两个窗共用**）：发包 + 乐观更新。
///
/// ⚠️ **规则本身不在这里** —— 它在 `bagdrag::BagDrag`（不碰 SDL ⇒ 有单元测试）。
/// 这里只管"决定好了之后怎么落地"。
fn apply_bag_act(act: bagdrag::Act, net: &mut Option<Net>) {
    let Some(n) = net.as_mut() else {
        return;
    };
    match act {
        bagdrag::Act::None => {}
        bagdrag::Act::PutBack => println!("[ui] 放回原格"),
        bagdrag::Act::Repair { slot } => {
            let special = n.world.repair.unwrap_or(false);
            if let Some(it) = n.world.bag.get(slot).and_then(|o| o.as_ref()) {
                let name = it.name.clone();
                n.bag_repair(slot as u32, special);
                println!(
                    "[net] 修理 {}（{}）",
                    name,
                    if special { "特修" } else { "普修" }
                );
            }
        }
        bagdrag::Act::Equip { slot } => {
            if let Some(it) = n.world.bag.get(slot).and_then(|o| o.as_ref()) {
                let name = it.name.clone();
                n.bag_equip(slot as u32);
                println!("[net] 穿戴 {}", name);
            }
        }
        bagdrag::Act::Use { slot } => {
            if let Some(it) = n.world.bag.get(slot).and_then(|o| o.as_ref()) {
                let name = it.name.clone();
                n.bag_use(slot as u32);
                println!("[net] 使用 {}", name);
            }
        }
        bagdrag::Act::PickUp { slot } => {
            if let Some(it) = n.world.bag.get(slot).and_then(|o| o.as_ref()) {
                println!("[ui] 拾起 {}", it.name);
            }
        }
        bagdrag::Act::Move { from, to } => {
            n.bag_move(from as u32, to as u32);
            // 乐观交换/移动；服务端随后用全量 BagItems 覆盖。
            if from < n.world.bag.len() && to < n.world.bag.len() {
                let moved = n.world.bag[from].clone();
                let target = n.world.bag[to].clone();
                n.world.bag[to] = moved;
                n.world.bag[from] = target;
            }
            println!("[net] 包裹 {} → {}", from, to);
        }
    }
}

/// 放一个技能（**技能栏点击与 F1..F8 共用** —— 鼠标与键盘同一个动作）。
///
/// 目标顺序照原版 `UseMagic`（ClMain.pas:2370-2449）：**有锁定目标就打它**，
/// 否则打光标那一格。
pub(crate) fn cast_skill(app: &mut AppState, skill_id: u32, name: &str) {
    let Some(n) = app.net.as_ref() else {
        return;
    };
    if !n.world.in_world() {
        return;
    }
    let (tx, ty) = screen_to_cell(app.cam, app.mouse.0, app.mouse.1);
    let target = app.world_control.combat_target.unwrap_or(0);
    n.cast_skill(skill_id, target, tx, ty);
    println!("[net] 施放 {name}（目标 {target} / 格 {tx},{ty}）");
}

/// 打开**技能窗**（原版 F11 与底栏「技能」按钮都是 `StatePage := 3`，
/// 也就是状态窗的技能页 —— `ClMain.pas:2056-2059`）。
pub(crate) fn open_skill_window(app: &mut AppState) {
    app.status_page
        .set(crate::status::SKILL_PAGE, crate::status::PAGES);
    app.magic_selected = None;
    if !app.win.status() {
        app.win.toggle_status();
    }
    println!("[ui] 技能窗");
}

/// F1..F8：技能窗里**选中了**某条 ⇒ 给它设快捷键；否则 ⇒ 放绑在那个键上的技能。
pub(crate) fn skill_hotkey(app: &mut AppState, slot: usize) {
    let key = b'1' as u32 + slot as u32;
    if let Some(id) = app.magic_selected.take() {
        if let Some(n) = app.net.as_ref() {
            n.set_magic_key(id, key);
            println!("[net] 技能 {id} 绑定到 F{}", slot + 1);
        }
        return;
    }
    let picked = app.net.as_ref().and_then(|n| {
        n.world
            .magics
            .iter()
            .find(|m| m.key == key)
            .map(|m| (m.skill_id, m.name.clone()))
    });
    match picked {
        Some((skill_id, name)) => cast_skill(app, skill_id, &name),
        None => println!("[ui] F{} 还没绑技能", slot + 1),
    }
}

/// 执行浮窗状态机给出的副作用（SDL 命中与纯状态转换已经完成）。
fn handle_ui_press(app: &mut AppState, press: Press) -> bool {
    use crate::overlay_state::Effect;

    match crate::overlay_state::reduce(app, press) {
        Effect::None => {}
        Effect::Say(msg) => ui_say(app.net.as_mut(), &msg),
        Effect::Notice(msg) => println!("[ui] {msg}"),
        Effect::BagToggled { open } => {
            println!("[ui] 背包窗 {}", if open { "打开" } else { "关闭" })
        }
        Effect::StatusToggled { open } => {
            println!("[ui] 状态窗 {}", if open { "打开" } else { "关闭" })
        }
        Effect::MinimapToggled { open } => {
            println!("[ui] 小地图 {}", if open { "打开" } else { "关闭" })
        }
        Effect::BoundBelt { slot, name } => println!("[ui] 快捷栏 {} = {}", slot + 1, name),
        Effect::UseBelt { slot, make_index } => {
            belt_use(&mut app.net, &mut app.belt, slot, make_index)
        }
        Effect::CastSkill {
            slot: _,
            skill_id,
            name,
        } => cast_skill(app, skill_id, &name),
        Effect::Buy { npc, name } => {
            if let Some(n) = app.net.as_ref() {
                n.shop_buy(npc, &name, 1);
                println!("[net] 买入 {name}");
            }
        }
        Effect::Sell {
            npc,
            make_index,
            count,
            name,
        } => {
            if let Some(n) = app.net.as_ref() {
                n.shop_sell(npc, make_index, count);
                println!("[net] 卖出 {name} x{count}");
                // 请求已排队后才清槽；reducer 在没有可卖物时不会生成 Sell effect。
                app.drag.take_placed();
            }
        }
        Effect::Logout => {
            ui_say(app.net.as_mut(), "已注销。");
            app.net = None;
            app.scene = crate::app_state::Scene::Login;
            app.belt = [None; crate::layout::BELT_SLOTS];
            app.win.close_all();
        }
        Effect::Exit => return true,
    }
    false
}

/// 世界模式 MouseDown：浮窗优先消费，其余再落到移动/挖肉/NPC/诊断探针。
///
/// 这里保留原分支的检查顺序与 early-return：特别是 modal confirm 和命中窗口后，
/// 不能再穿透成走路或点 NPC。
#[allow(clippy::too_many_arguments)]
/// 光标是不是压在**开着的浮窗**上（对话 / 包裹 / 状态 / 商品列表 / 卖窗 / 确认框）。
///
/// ⚠️ **P0**（用户 2026-10-12）：压着窗就不许把点击透给下一层的地图 —— 原来只在各窗
/// 分支里"事后清掉走路意图"，`[move]` 日志已经打出去了，而 **MouseUp 那条路根本没
/// 判** ⇒ 单击对话窗真的会把人走过去。
///
/// 抽成一个函数是因为按下 / 松开**两条路都要判**（原版也是两个入口各判一次）。
pub(crate) fn over_ui_window(
    app: &AppState,
    ui: &mut ui::UiCache<'_>,
    dir: Option<&std::path::Path>,
    x: f32,
    y: f32,
) -> bool {
    use crate::widget::{Align, EditPolicy, InputPolicy, LayoutNode, VisualSource};
    let viewport = crate::layout::LRect::new(0.0, 0.0, WIN_W as f32, WIN_H as f32);
    let mut tree = crate::layout::layout_document().widget_tree();
    let mut add_window = |id, rect: (f32, f32, f32, f32), z| {
        tree.push(LayoutNode {
            id,
            parent: None,
            rect: crate::layout::LRect::new(rect.0, rect.1, rect.2, rect.3),
            align: Align::TOP_LEFT,
            z,
            clip_children: true,
            input: InputPolicy::Block,
            edit: EditPolicy::LOCKED,
            visual: VisualSource::Independent,
        });
    };

    // z 顺序与模态优先级集中在这棵树；所有打开的窗口区域都拦截地图输入。
    if app.win.bag() {
        let rect = hud::bag_geom(
            app.net.as_ref().map_or(0, |n| n.world.bag.len()),
            app.bag_page.page(),
            app.win.shop(),
        )
        .0;
        add_window("window_bag", rect, 10);
    }
    if app.win.status() {
        add_window("window_status", crate::status::panel_rect(), 20);
    }
    if let Some(shop) = app.net.as_ref().and_then(|n| n.world.shop.as_ref()) {
        add_window("window_shop", crate::shop::panel(), 30);
        if shop.is_sell() {
            add_window("window_sell", crate::shop::sell_panel(), 31);
        }
    }
    if app.net.as_ref().is_some_and(|n| n.world.dialog.is_some()) {
        add_window("window_dialog", crate::dialog::dialog_panel(), 40);
    }
    if app.confirm.is_open() {
        let dir = dir.expect("确认窗打开时必须有已预检的素材上下文");
        let c = hud::confirm_geom(ui, dir, (WIN_W, WIN_H));
        tree.push(LayoutNode {
            id: "modal_confirm",
            parent: None,
            rect: viewport,
            align: Align::TOP_LEFT,
            z: 100,
            clip_children: false,
            input: InputPolicy::Block,
            edit: EditPolicy::LOCKED,
            visual: VisualSource::Independent,
        });
        tree.push(LayoutNode {
            id: "confirm_frame",
            parent: Some("modal_confirm"),
            rect: crate::layout::LRect::new(c.frame.x, c.frame.y, c.frame.w, c.frame.h),
            align: Align::TOP_LEFT,
            z: 1,
            clip_children: true,
            input: InputPolicy::Block,
            edit: EditPolicy::LOCKED,
            visual: VisualSource::Independent,
        });
        for (id, r) in [("confirm_ok", c.ok), ("confirm_cancel", c.cancel)] {
            tree.push(LayoutNode {
                id,
                parent: Some("confirm_frame"),
                rect: crate::layout::LRect::new(r.x - c.frame.x, r.y - c.frame.y, r.w, r.h),
                align: Align::TOP_LEFT,
                z: 1,
                clip_children: false,
                input: InputPolicy::Target,
                edit: EditPolicy::LOCKED,
                visual: VisualSource::Independent,
            });
        }
    }
    tree.hit_test(viewport, (x, y))
        .expect("visible-window widget tree must be valid")
        .is_some()
}

// ⚠️ 参数多是有意的：这条是 SDL 事件的**唯一**入口，把应用状态拆成参数传进来
// 才好单测（架构方向 ①：SDL 只在适配层）。
#[allow(clippy::too_many_arguments)]
pub(crate) fn handle_world_mouse_down<'a>(
    app: &mut AppState,
    mouse_btn: MouseButton,
    x: f32,
    y: f32,
    ui: &mut ui::UiCache<'a>,
    asset_dir: &Option<PathBuf>,
    ui_texts: &mut font::TextCache<'a>,
    draws: &[TileDraw],
    tiles: &HashMap<TileKey, TileTex<'a>>,
    hover: Option<u64>,
) {
    let in_world = app.net.as_ref().is_some_and(|n| n.world.in_world());
    // ⓪ **界面开发器**（D-134）：编辑模式下鼠标归它 —— 选控件 / 拖动 / 改大小，
    // 不穿透到世界（否则拖控件会顺带把人走过去）。
    if app.layout_edit.on {
        if mouse_btn == MouseButton::Left {
            app.mouse = (x, y);
            // ⚠️ 屏幕坐标 → **逻辑**坐标（画面被放大时鼠标要反算回去，D-134）
            let layout_xy = app.layout_edit.pointer_to_layout((x, y));
            app.layout_edit.mouse_down_screen((x, y), layout_xy);
        }
        return;
    }
    match mouse_btn {
        MouseButton::Left | MouseButton::Right if in_world => {
            let cell = screen_to_cell(app.cam, x, y);
            let run = mouse_btn == MouseButton::Right;
            // 按住时每 300ms 要拿"当前鼠标位置"重取目标（见下面 `MOUSE_REPEAT_MS`
            // 那段）⇒ 按下这一刻就得把位置记上（光等 `MouseMotion` 会漏掉
            // "按下后一动不动"的那种按住）。
            app.mouse = (x, y);
            app.ui_click.set_mouse(crate::widget::canvas_pt(app.mouse));
            // ⚠️ **本次点击是否被某个浮窗消费**（todo 第 7 条）：
            // 浮窗是画在世界**之上**的，点在窗里就不该再穿透到世界
            //（原来各窗只清一次移动目标，后面那步"点 NPC"照跑 ⇒
            //  隔着窗点一下照样跟 NPC 说话）。窗命中时置 true，
            // 下面的"世界"分支（移动/挖肉/点 NPC）统一跳过。
            let mut ui_consumed = false;
            // ①″ **确认框开着 ⇒ 它是模态**：只认框里的两颗按钮，
            // 点框外/别处一律当作"取消"（不走路、不穿透到世界）。
            if app.confirm.is_open() {
                if mouse_btn == MouseButton::Left {
                    let dir = asset_dir.as_deref().expect("确认窗必须有素材上下文");
                    let c = hud::confirm_geom(ui, dir, (WIN_W, WIN_H));
                    // OnClick 规范 ⇒ 按下只记，松开才执行（见 `widget`）
                    if c.ok.hit(app.mouse) {
                        app.ui_click.press(
                            Press::ConfirmOk,
                            crate::widget::canvas_rect((c.ok.x, c.ok.y, c.ok.w, c.ok.h)),
                        );
                    } else if c.cancel.hit(app.mouse) {
                        app.ui_click.press(
                            Press::ConfirmCancel,
                            crate::widget::canvas_rect((
                                c.cancel.x, c.cancel.y, c.cancel.w, c.cancel.h,
                            )),
                        );
                    } else if !c.frame.hit(app.mouse) {
                        app.confirm.cancel();
                    }
                }
                app.world_control.consume_world_click();
                return;
            }
            // ①‴ **底栏右上那 4 个功能按钮**（人物/包裹/技能/选项，
            // 图号 8-11，用户 2026-10-11）：点它们 = 按对应的功能键，
            // 走**同一个** `toggle_*` ⇒ 鼠标与键盘行为一致。
            // ⚠️ 面板盖在世界之上 ⇒ 点中就要 `ui_consumed`（不然人还会走）。
            if mouse_btn == MouseButton::Left {
                if let Some(b) = crate::layout::hud_btn_at(crate::widget::canvas_pt(app.mouse)) {
                    // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                    app.ui_click.press(Press::Hud(b), b.rect());
                    // 目标清掉，**本次点击不再穿透到世界**（与其它浮窗
                    // 同一条纪律：命中就 `continue`，不然人还会走）。
                    app.world_control.consume_world_click();
                    return;
                }
                // **快捷（腰带）栏**：手上拿着东西 ⇒ 放进这格；
                // 空手 ⇒ 使用这格里的东西（原版单击是"拿起"，
                // 但快捷栏的意义就是快速使用 ⇒ 单击即用）。
                if let Some(i) = crate::layout::belt_slot_at(crate::widget::canvas_pt(app.mouse)) {
                    app.ui_click
                        .press(Press::Belt(i), crate::layout::belt_slot_rect(i));
                    app.world_control.consume_world_click();
                    return;
                }
                // **技能栏**（F1..F8）：点一格 = 放绑在那格的技能（与腰带同一套：
                // OnClick 规范 ⇒ 按下只记，松开才执行）。
                if let Some(i) = crate::layout::skill_slot_at(crate::widget::canvas_pt(app.mouse)) {
                    app.ui_click
                        .press(Press::SkillBar(i), crate::layout::skill_slot_rect(i));
                    app.world_control.consume_world_click();
                    return;
                }
                // 底栏**左下**那排快捷按钮（小地图/交易/行会/组队/属性）。
                // 小地图那颗 = **Tab** 同一个动作（`toggle_minimap`）。
                if let Some(b) = crate::layout::hud_quick_at(crate::widget::canvas_pt(app.mouse)) {
                    // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                    app.ui_click.press(Press::Quick(b), b.rect());
                    app.world_control.consume_world_click();
                    return;
                }
            }
            // 左键才判"单击"（用户第 3 条说的是左键）；右键仍是按住=跑
            if mouse_btn == MouseButton::Left {
                app.world_control.press_at = Some(Instant::now());
                app.world_control.press_cell = cell;
            } else {
                app.world_control.press_at = None;
            }
            // **挖肉锁定**（原版 `CM_BUTCH`，用户第 1 条）：**按住 Alt 点一下**
            // 动物尸体 ⇒ 锁定为"正在挖它"，**按住不放就持续挖**（不是反复点）。
            // 皮革度要挖好几刀才归零（鸡 50，一刀 -5..20）⇒ 一次点击出不了肉。
            let mut did_butch = false;
            if mouse_btn == MouseButton::Left && app.world_control.alt_down {
                if let Some(n) = app.net.as_ref() {
                    let corpse = n
                        .world
                        .entities
                        .values()
                        .find(|e| e.dead && (e.x, e.y) == cell);
                    if let Some(e) = corpse {
                        app.world_control.butch_target = Some(e.id);
                        app.world_control.butch_next = Instant::now(); // 立刻挖第一刀
                        println!("[net] 挖肉锁定 {}", e.name);
                        did_butch = true;
                        // 挖肉不是"选目标/走路"⇒ 目标清掉（下面 `(ct, mt)`
                        // 走 None 那支），也别把这次当"单击走一格"。
                        app.world_control.press_at = None;
                        app.world_control.pending_pickup = None;
                    }
                }
            }
            // 包裹操作（普通/卖出共用）：单击拾取，双击可穿戴物品=穿、
            // 其他物品=使用；拿起后点空格=移动、点已有物品=交换。
            let mut bag_handled = false;
            if mouse_btn == MouseButton::Left && app.win.bag() {
                if let Some(n) = app.net.as_ref() {
                    let (bx, by) = crate::layout::bag_pos(app.win.shop());
                    let lx = x - bx;
                    let ly = y - by;
                    if let Some(slot) = crate::layout::bag_slot_at((lx, ly)) {
                        let idx = app.bag_page.page() * crate::layout::BAG_PAGE_SLOTS + slot;
                        // —— 包裹操作规范（用户 2026-10-10，普通/卖出一致）——
                        // 2/3. 手上拿着：放到**空格** = 移动；放到**有东西的格**
                        //     = **交换**（那格的物品变为拾取）。服务端是权威
                        //     （发 `MoveItem`），本地乐观更新，等全量刷新覆盖。
                        // 5. 双击物品格 = **使用**（原版 `CM_EAT`）。
                        // —— 包裹操作规范（用户 2026-10-10，普通/卖出一致）——
                        // 规则全在 `bagdrag`（架构方向 ①）：那份不碰 SDL ⇒
                        // 有单元测试；这里只负责**执行**它给的动作。
                        let act = {
                            let cell = |i: usize| {
                                n.world.bag.get(i).and_then(|o| o.as_ref()).map(|it| {
                                    bagdrag::Cell {
                                        make: it.make_index,
                                        std_mode: it.std_mode,
                                    }
                                })
                            };
                            app.drag
                                .click_bag(idx, Instant::now(), n.world.repair, cell)
                        };
                        apply_bag_act(act, &mut app.net);
                        // 抓在手上了 ⇒ 别再走过去 / 捡地上的东西
                        if matches!(act, bagdrag::Act::PickUp { .. }) {
                            app.world_control.press_at = None;
                            app.world_control.pending_pickup = None;
                        }
                        bag_handled = true;
                    }
                }
                if bag_handled {
                    app.world_control.combat_target = None;
                    app.world_control.move_target = None;
                    app.world_control.press_at = None;
                    app.world_control.held_move = None;
                    return;
                }
            }
            // ⚠️ **P0**（用户 2026-10-12）：光标压在开着的浮窗上 ⇒ **不走路、不锁怪**，
            // 也别打 `[move]` 那行日志（原来打完才被各窗分支清掉，日志误导排查）。
            let over_ui = over_ui_window(app, ui, asset_dir.as_deref(), x, y);
            let (ct, mt) = if over_ui {
                (None, None)
            } else if did_butch || app.world_control.butch_target.is_some() {
                // 挖肉中（含"这一下就是挖肉"）⇒ **不走路、不选目标**
                //（用户 2026-10-10 第 1 条：挖肉时人物不该动）
                (None, None)
            } else {
                // 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：
                // **先清掉旧目标**，点到**活怪**就锁住它（之后每帧自动靠近/出手，
                // 直到它死掉或消失）；点空地 ⇒ 走/跑到那一格。
                app.net
                    .as_ref()
                    .map(|n| mouse_intent(&n.world, cell, run, hover))
                    .unwrap_or((None, None))
            };
            app.world_control.combat_target = ct;
            app.world_control.move_target = mt;
            // 点在**地上有东西**的格子 ⇒ 记下"待捡"：走到那格自动捡
            //（原版点物品也是先走过去；到了就发，见帧循环里那段）。
            if mouse_btn == MouseButton::Left {
                if let Some(n) = app.net.as_ref() {
                    match n.world.ground.values().find(|g| (g.x, g.y) == cell) {
                        Some(gi) if n.world.self_pos == cell => {
                            n.pickup(gi.ground_id);
                            app.world_control.pending_pickup = None;
                        }
                        Some(gi) => app.world_control.pending_pickup = Some((gi.ground_id, cell)),
                        None => app.world_control.pending_pickup = None,
                    }
                }
            }
            app.world_control.attack_at = Instant::now(); // 立刻判"该出手还是该靠近"
                                                          // ⚠️ `move_at` **不清零**：原版的客户端是"停等"的
                                                          //（发一条动作后 `ActionLock := TRUE`，等服务端
                                                          // `sSTATUS_GOOD/FAIL` 才解锁并计下一拍，`ClMain.pas:3575-3584/4214-4222`）。
                                                          // 我们每次按下都清零 ⇒ 连点会把间隔压到 0 ⇒ 服务端按到达时间限流
                                                          // 直接回 `reason=1`，客户端再锁 1 秒 ⇒ "点了没反应"。
                                                          // ⚠️ **按住不放要能一直走**：原版靠 `DXDrawMouseMove`
                                                          //（`ClMain.pas:2678-2679`）—— 按住时只要距上次 >300ms 就**重跑一遍
                                                          // 按下逻辑**，于是"鼠标那一格"被反复重新当成目标。少了它，
                                                          // 目标格是按下那一刻定死的 ⇒ 走到那儿就停（用户 2026-10-08 报的
                                                          // "按住只能走数次"）。
            if !over_ui {
                app.world_control.held_move = Some(if mouse_btn == MouseButton::Right {
                    crate::world_control::MoveButton::Run
                } else {
                    crate::world_control::MoveButton::Walk
                });
                app.world_control.retarget_at = Instant::now();
            } else {
                // ⚠️ 连 `press_at` 一起清：MouseUp 那条路（"单击走一格"）靠它判断，
                // 不清的话点上对话窗松手仍会把人走过去（用户 2026-10-12 的 P0）。
                app.world_control.press_at = None;
                app.world_control.pending_pickup = None;
            }
            match ct {
                Some(id) => {
                    println!("[net] 锁定目标 ActorId={id}（靠近后自动出手）");
                }
                None => println!(
                    "[move] 目标格 ({},{})：{}",
                    cell.0,
                    cell.1,
                    if run { "跑" } else { "走" }
                ),
            }
            // ② 对话开着：点选项 = 选它；点面板别处 = 什么也别做（**别走路**）
            //
            // ⚠️ 这两条排在"走路/锁怪"那段**之后**，靠"清掉已写好的意图"
            // 实现（把上面那一大段包进 if 会多一层缩进、更容易漏改）。
            if let Some(n) = app.net.as_ref() {
                if let Some((panel, lines, scroll)) =
                    hud::dialog_geom(app.net.as_ref(), app.dlg_scroll.scroll())
                {
                    // **行内可点文字**：命中矩形由 `dialog::dialog_line_pieces`
                    // 算（与 `draw_dialog` 是同一份 ⇒ 画哪点哪），
                    // 片段自带**选项序号**（服务端把 `<打开/@trading>` 改写成了
                    // `<打开/@1>`），直接拿它回包。
                    let hit = {
                        let mut measure = |t: &str| ui_texts.width(t);
                        // ⚠️ 命中要带上**滚动位置**：画的是卷过之后的那几行，
                        // 拿未卷的行号去判就会点空/点到隔壁（画与命中同源）。
                        dialog::dialog_link_at(panel, &lines, (x, y), scroll, &mut measure)
                    };
                    if let Some(idx) = hit {
                        if let Some(d) = n.world.dialog.as_ref() {
                            n.npc_select(d.npc_id, idx);
                            println!("[net] 对话选项 {idx}");
                        }
                    } else if dialog::dialog_close_hit(panel, (x, y)) {
                        // 右上角那个红 X（用户 2026-10-09 第 1 条：
                        // 「对话窗口右上角有个X按钮，现在你没有接上"关闭/退出"」）
                        // ⚠️ 与点「退出」/按 ESC **完全同一条路**
                        //（用户 2026-10-10 第 2 条：点 X 也要关掉
                        // 物品列表与包裹）—— 所以复用同一个出口：
                        // 发 `NpcClose` + 本地 `close_dialog()`（它连带收货架）。
                        if let Some(d) = n.world.dialog.as_ref() {
                            n.npc_close(d.npc_id);
                        }
                        if let Some(n) = app.net.as_mut() {
                            n.world.close_dialog();
                        }
                        println!("[net] 对话关闭（点 X，与退出同一条路）");
                    }
                    // ⚠️ **只有点在面板里**才吞掉这次点击。
                    // 用户 2026-10-09：「对话期间应仍能操作（走、跑、打架），
                    // 现在不能跑不能走，人被定在那里」—— 原来是无条件清空意图，
                    // 等于把整个屏幕都变成"对话区"。原版的对话窗是**非模态**的
                    //（`NPCDialog` 就是个窗口，`DrawScrn.pas` 里它跟世界各画各的），
                    // 外面照常点：此时才拦。
                    if dialog::dialog_hit(panel, (x, y)) {
                        app.world_control.combat_target = None;
                        app.world_control.move_target = None;
                        app.world_control.press_at = None;
                        app.world_control.held_move = None;
                        ui_consumed = true;
                    }
                }
            }
            // ②″ F10 状态窗：点 X = 关窗、点箭头 = 翻页、点槽 = 报一下那件；
            // 点在窗里别处 = 吞掉（与背包窗同一套）
            if app.win.status() {
                let dir = asset_dir
                    .as_deref()
                    .expect("assets preflight guarantees a source context");
                let bg = ui
                    .size(dir, crate::status::BG_LIB, crate::status::BG)
                    .expect("素材清单要求 Prguse[370] 人物信息面板背景");
                let (wx, wy, ww, wh) = crate::status::panel(bg);
                if x >= wx && x < wx + ww && y >= wy && y < wy + wh {
                    ui_consumed = true;
                    match crate::status::hit((x - wx, y - wy), app.status_page.page()) {
                        // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                        crate::status::Hit::Close => {
                            let c = crate::status::CLOSE;
                            app.ui_click.press(
                                Press::Status(crate::status::Hit::Close),
                                crate::widget::canvas_rect((wx + c.0, wy + c.1, c.2, c.3)),
                            );
                        }
                        crate::status::Hit::Arrow(d) => {
                            let a = if d < 0 {
                                crate::status::ARROW_UP_RECT
                            } else {
                                crate::status::ARROW_DOWN_RECT
                            };
                            app.ui_click.press(
                                Press::Status(crate::status::Hit::Arrow(d)),
                                crate::widget::canvas_rect((wx + a.0, wy + a.1, a.2, a.3)),
                            );
                        }
                        crate::status::Hit::Slot(slot) => {
                            if let Some(n) = app.net.as_ref() {
                                let name = crate::status::slot_name(slot);
                                if app.status_page.page() == 0 {
                                    if let Some(Some(it)) = n.world.equip.get(slot) {
                                        n.equip_unequip(slot as u32);
                                        println!("[net] 脱下 {}（{}）", it.name, name);
                                    } else {
                                        println!("[ui] 装备槽 {}（{}）是空的", slot + 1, name);
                                    }
                                } else {
                                    let msg = match n.world.equip.get(slot) {
                                        Some(Some(it)) => {
                                            format!("装备槽 {}（{}）：{}", slot + 1, name, it.name)
                                        }
                                        _ => {
                                            format!("装备槽 {}（{}）是空的", slot + 1, name)
                                        }
                                    };
                                    println!("[ui] {msg}");
                                    if let Some(n) = app.net.as_mut() {
                                        n.chat.push(trunc(&msg, 30), C_CHAT_SYS);
                                    }
                                }
                            }
                        }
                        // 技能页：**点一行 = 选中它**（选中后按 F1..F8 设快捷键；
                        // 原版是弹一个选键对话框，我们还没有那块面板）。
                        crate::status::Hit::Skill(row) => {
                            let base = app.magic_page.page() * crate::status::SKILL_ROWS;
                            let picked = app
                                .net
                                .as_ref()
                                .and_then(|n| n.world.magics.get(base + row))
                                .map(|m| (m.skill_id, m.name.clone()));
                            if let Some((id, name)) = picked {
                                app.magic_selected = Some(id);
                                println!("[ui] 选中技能 {name}（按 F1..F8 设快捷键）");
                                if let Some(n) = app.net.as_mut() {
                                    n.chat.push(
                                        trunc(&format!("选中 {name}，按 F1-F8 设快捷键"), 30),
                                        C_CHAT_SYS,
                                    );
                                }
                            }
                        }
                        crate::status::Hit::None => {}
                    }
                    app.world_control.combat_target = None;
                    app.world_control.move_target = None;
                    app.world_control.press_at = None;
                    app.world_control.held_move = None;
                }
            }
            // ②′ 背包窗：左键抓起；双击可穿戴物品 = 穿、其他物品 = 使用；
            // 拿着物品点空格 = 移动、点已有物品 = 交换。卖出模式复用同一只手。
            if app.win.bag() {
                let (bx, by) = crate::layout::bag_pos(app.win.shop());
                let inside = x >= bx
                    && x < bx + crate::layout::BAG_W
                    && y >= by
                    && y < by + crate::layout::BAG_H;
                if inside {
                    ui_consumed = true;
                    let (lx, ly) = (x - bx, y - by);
                    let on_close = (crate::layout::BAG_CLOSE_X
                        ..crate::layout::BAG_CLOSE_X + crate::layout::BAG_CLOSE_W)
                        .contains(&lx)
                        && (crate::layout::BAG_CLOSE_Y
                            ..crate::layout::BAG_CLOSE_Y + crate::layout::BAG_CLOSE_H)
                            .contains(&ly);
                    if on_close {
                        app.ui_click.press(
                            Press::BagClose,
                            crate::widget::canvas_rect((
                                bx + crate::layout::BAG_CLOSE_X,
                                by + crate::layout::BAG_CLOSE_Y,
                                crate::layout::BAG_CLOSE_W,
                                crate::layout::BAG_CLOSE_H,
                            )),
                        );
                    } else if let Some(slot) = crate::layout::bag_slot_at((lx, ly)) {
                        let idx = app.bag_page.page() * crate::layout::BAG_PAGE_SLOTS + slot;
                        // 与上面那处**同一份规则**（`bagdrag`），不再各写一遍
                        if let Some(n) = app.net.as_ref() {
                            let act = {
                                let cell = |i: usize| {
                                    n.world.bag.get(i).and_then(|o| o.as_ref()).map(|it| {
                                        bagdrag::Cell {
                                            make: it.make_index,
                                            std_mode: it.std_mode,
                                        }
                                    })
                                };
                                app.drag
                                    .click_bag(idx, Instant::now(), n.world.repair, cell)
                            };
                            apply_bag_act(act, &mut app.net);
                        }
                    }
                    app.world_control.combat_target = None;
                    app.world_control.move_target = None;
                    app.world_control.press_at = None;
                    app.world_control.held_move = None;
                }
            }
            // ②″″ 商店窗：点行=**选中**、点箭头=翻页、点 OK=买选中的
            //（原版口径，用户 2026-10-10 第 7 条）；点在窗里别走路
            // ⚠️ 买/卖是**两个窗**：只有"买"模式才走商品列表窗
            //（卖模式下面那个窗出的是放物品的槽）
            if app.win.shop() {
                if let Some(n) = app.net.as_ref() {
                    if let Some(sh) = n.world.shop.as_ref().filter(|s| !s.is_sell()) {
                        let (wx, wy, ww, wh) = crate::shop::panel();
                        if x >= wx && x < wx + ww && y >= wy && y < wy + wh {
                            ui_consumed = true;
                            let base = app.shop_ui.page() * crate::shop::ROWS;
                            let rows = sh.items.len().saturating_sub(base);
                            match crate::shop::hit((x - wx, y - wy), rows) {
                                crate::shop::Hit::Row(r) => {
                                    app.shop_ui.click_row(r);
                                }
                                // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                                crate::shop::Hit::Prev => {
                                    let p = crate::shop::PREV_R;
                                    app.ui_click.press(
                                        Press::Shop(crate::shop::Hit::Prev),
                                        crate::widget::canvas_rect((wx + p.0, wy + p.1, p.2, p.3)),
                                    );
                                }
                                crate::shop::Hit::Next => {
                                    let p = crate::shop::NEXT_R;
                                    app.ui_click.press(
                                        Press::Shop(crate::shop::Hit::Next),
                                        crate::widget::canvas_rect((wx + p.0, wy + p.1, p.2, p.3)),
                                    );
                                }
                                crate::shop::Hit::Ok => {
                                    let p = crate::shop::OK;
                                    app.ui_click.press(
                                        Press::Shop(crate::shop::Hit::Ok),
                                        crate::widget::canvas_rect((wx + p.0, wy + p.1, p.2, p.3)),
                                    );
                                }
                                crate::shop::Hit::Close => {
                                    let p = crate::shop::CLOSE;
                                    app.ui_click.press(
                                        Press::Shop(crate::shop::Hit::Close),
                                        crate::widget::canvas_rect((wx + p.0, wy + p.1, p.2, p.3)),
                                    );
                                }
                                crate::shop::Hit::None => {}
                            }
                            app.world_control.combat_target = None;
                            app.world_control.move_target = None;
                            app.world_control.press_at = None;
                            app.world_control.held_move = None;
                        }
                    }
                }
            }
            // ②‴ 卖货窗：点圈 = 放下手上的东西；点 OK = 卖掉圈里那件
            //（原版拖放式，用户 2026-10-10 第 3 条）；点在窗里别走路。
            if app.win.shop()
                && app.win.bag()
                && app
                    .net
                    .as_ref()
                    .and_then(|n| n.world.shop.as_ref())
                    .is_some_and(|s| s.is_sell())
            {
                let (sx, sy, sw, sh) = crate::shop::sell_panel();
                if x >= sx && x < sx + sw && y >= sy && y < sy + sh {
                    ui_consumed = true;
                    match crate::shop::sell_hit((x - sx, y - sy)) {
                        // 右上角的**红色关闭叉**（素材自带）⇒ 关卖窗。
                        // ⚠️ **同时关包裹**（用户 2026-10-11 第 3 条）：
                        // 卖窗就是"卖出"这件事的入口，它关了包裹留着没意义，
                        // 原版关卖窗时包裹也是一起收的。
                        // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                        crate::shop::SellHit::Close => {
                            let p = crate::shop::SELL_CLOSE;
                            app.ui_click.press(
                                Press::Sell(crate::shop::SellHit::Close),
                                crate::widget::canvas_rect((sx + p.0, sy + p.1, p.2, p.3)),
                            );
                        }
                        // 圆槽：**手上拿着 ⇒ 放下；已经放着 ⇒ 再拿起来**
                        //（用户 2026-10-10：物品放下后还可以再次拾起、放回包裹）
                        crate::shop::SellHit::Slot => {
                            let p = crate::shop::SELL_SLOT;
                            app.ui_click.press(
                                Press::Sell(crate::shop::SellHit::Slot),
                                crate::widget::canvas_rect((sx + p.0, sy + p.1, p.2, p.3)),
                            );
                        }
                        crate::shop::SellHit::Ok => {
                            let p = crate::shop::SELL_OK;
                            app.ui_click.press(
                                Press::Sell(crate::shop::SellHit::Ok),
                                crate::widget::canvas_rect((sx + p.0, sy + p.1, p.2, p.3)),
                            );
                        }
                        crate::shop::SellHit::None => {}
                    }
                    app.world_control.combat_target = None;
                    app.world_control.move_target = None;
                    app.world_control.press_at = None;
                    app.world_control.held_move = None;
                }
            }
            // ③ 点 NPC ⇒ **说话**，不是往它那格走。
            //
            // ⚠️ 判据分两层（用户 2026-10-09 报"点 NPC 没反应"）：
            //   1. `hover`（上一帧用 `actor_rect` = **画出来的框**算的）——
            //      NPC 的精灵比格子高，点它的头/肩时格子是**上面那一格**，
            //      只看格子就是"点了没反应、人还往那边走"；
            //   2. 兜底再用格子（光标正好压在它脚下那格时，`hover` 可能为空）。
            if !ui_consumed {
                if let Some(n) = app.net.as_ref() {
                    let hit = hover
                        .filter(|id| n.world.npc_kind(*id))
                        .or_else(|| n.world.npc_at(cell.0, cell.1));
                    if let Some(id) = hit {
                        n.npc_click(id);
                        app.world_control.combat_target = None;
                        app.world_control.move_target = None;
                        app.world_control.press_at = None; // 松开时别再改判成"走一格"
                        app.world_control.held_move = None;
                        println!("[net] 点 NPC ActorId={id}（等 NpcSay）");
                    }
                }
            }
        }
        MouseButton::Left => probe_at(x, y, app.cam, draws, tiles, app.layers),
        _ => {}
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum MouseUpOutcome {
    /// 继续下游事件（可能退出主循环）。
    Continue,
    /// 点击了 UI 控件，不能再落成世界走路。
    Consumed,
    /// 已确认退出。
    Quit,
}

/// 世界模式 MouseUp：通用控件点击优先；未被 UI 消费时再处理单击走一格。
pub(crate) fn handle_world_mouse_up(
    app: &mut AppState,
    mouse_btn: MouseButton,
    x: f32,
    y: f32,
    hover: Option<u64>,
) -> MouseUpOutcome {
    // ⓪ **界面开发器**（D-134）：松开 = 结束拖动，不走世界的单击逻辑。
    if app.layout_edit.on {
        app.layout_edit.mouse_up();
        return MouseUpOutcome::Consumed;
    }
    // ① **OnClick**：左键按下 + 松开在**同一控件**才执行
    //（规范见 `widget` 模块头的表：拖出控件再松开 / 右键 / 只有
    // MouseDown ⇒ 都不触发）。右键不会走到这里（`release` 只对左键调）。
    if mouse_btn == MouseButton::Left {
        if let Some(k) = app.ui_click.release(crate::widget::canvas_pt((x, y))) {
            if handle_ui_press(app, k) {
                return MouseUpOutcome::Quit;
            }
            return MouseUpOutcome::Consumed;
        }
    }
    let press_at = app.world_control.release_mouse();
    // ⚠️ 单击 = 走**一格**（用户 2026-10-09 第 3 条）：按下那一刻已经把
    // "走到光标那格"当成目标了（`mouse_intent`），够快松手就把它换成
    // **紧邻那一格** ⇒ 一步之后"到达"、目标自清。按住（> `CLICK_MS`）
    // 仍是一路走过去。
    if mouse_btn == MouseButton::Left {
        if let Some(t) = press_at {
            if t.elapsed() < Duration::from_millis(input::CLICK_MS) {
                if let Some(n) = app.net.as_mut() {
                    if n.world.in_world() {
                        let (ct, mt) = if app.world_control.butch_target.is_some() {
                            (None, None) // 挖肉中 ⇒ 松手也不走一格
                        } else {
                            input::click_step(
                                &n.world,
                                n.world.self_pos,
                                app.world_control.press_cell,
                                false,
                                hover,
                            )
                        };
                        if ct.is_some() || mt.is_some() {
                            app.world_control.combat_target = ct;
                            app.world_control.move_target = mt;
                            app.world_control.attack_at = Instant::now();
                        }
                    }
                }
            }
        }
    }
    MouseUpOutcome::Continue
}

/// 处理背包 / 对话 / 商店三个窗的滚轮，按覆盖顺序只让最上层命中的一个消费。
pub(crate) fn handle_mouse_wheel(app: &mut AppState, y: f32) {
    let mut done = false;
    // ① 背包窗内滚轮翻页（服务端背包 46 格 = 2 页，窗口一次只画 24 格）
    if app.win.bag() {
        let (bx, by) = crate::layout::bag_pos(app.win.shop());
        let inside = app.mouse.0 >= bx
            && app.mouse.0 < bx + crate::layout::BAG_W
            && app.mouse.1 >= by
            && app.mouse.1 < by + crate::layout::BAG_H;
        if inside {
            let pages = app
                .net
                .as_ref()
                .map_or(1, |n| crate::layout::bag_pages(n.world.bag.len()));
            if y > 0.0 {
                app.bag_page.step(1, pages);
            } else if y < 0.0 {
                app.bag_page.step(-1, pages);
            }
            done = true;
        }
    }
    // ①′ 状态窗**技能页**内滚轮翻技能列表（一页 5 条）。
    //
    // ⚠️ 为什么不用那两个箭头：原版在技能页拿箭头翻技能列表（FState.pas:3704），
    // 但我们没有那排"状态/技能"标签页 ⇒ 箭头被抢走就再也翻不回别的页。
    if !done && app.win.status() && app.status_page.page() == crate::status::SKILL_PAGE {
        let (wx, wy, ww, wh) = crate::status::panel_rect();
        let over = app.mouse.0 >= wx
            && app.mouse.0 < wx + ww
            && app.mouse.1 >= wy
            && app.mouse.1 < wy + wh;
        if over {
            let dir = if y > 0.0 { 1 } else { -1 };
            let pages = app
                .net
                .as_ref()
                .map_or(1, |n| crate::status::skill_pages(n.world.magics.len()));
            app.magic_page.step(dir, pages);
            done = true;
        }
    }
    // ② 对话窗内滚轮卷行（正文折行后常常超出背板：298 个脚本里 114 个超一屏）
    if !done && app.net.is_some() {
        if let Some((panel, lines, _)) = hud::dialog_geom(app.net.as_ref(), app.dlg_scroll.scroll())
        {
            if dialog::dialog_hit(panel, app.mouse) {
                app.dlg_scroll.wheel(y, lines.len());
                done = true;
            }
        }
    }
    // ③ 商店窗内滚轮翻页（一个商人最多 20 件商品 ⇒ 2 页）
    if !done && app.win.shop() {
        if let Some(n) = app.net.as_ref() {
            if let Some(sh) = n.world.shop.as_ref() {
                let (wx, wy, ww, wh) = crate::shop::panel();
                if app.mouse.0 >= wx
                    && app.mouse.0 < wx + ww
                    && app.mouse.1 >= wy
                    && app.mouse.1 < wy + wh
                {
                    app.shop_ui
                        .wheel(if y > 0.0 { 1 } else { -1 }, sh.items.len());
                }
            }
        }
    }
}

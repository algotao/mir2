//! 键盘输入分发。SDL 按键事件在这里映射为运行态动作；不画 UI。

use sdl3::keyboard::{Keycode, Mod};

use crate::app_state::AppState;
use crate::*;

/// SDL 按键 → 登录/选角状态机的无平台键值。
fn scene_key(k: Keycode) -> crate::app_state::SceneKey {
    use crate::app_state::SceneKey as K;
    match k {
        Keycode::Tab => K::Tab,
        Keycode::Backspace => K::Backspace,
        Keycode::Return | Keycode::KpEnter => K::Enter,
        Keycode::Escape => K::Escape,
        Keycode::Left => K::Left,
        Keycode::Right => K::Right,
        Keycode::Up => K::Up,
        Keycode::Down => K::Down,
        Keycode::Space => K::Space,
        _ => K::Other,
    }
}

/// 数字键 → `0..9`（主键盘 + 小键盘）。
fn digit_key(k: Keycode) -> Option<u32> {
    use Keycode::*;
    Some(match k {
        _0 | Kp0 => 0,
        _1 | Kp1 => 1,
        _2 | Kp2 => 2,
        _3 | Kp3 => 3,
        _4 | Kp4 => 4,
        _5 | Kp5 => 5,
        _6 | Kp6 => 6,
        _7 | Kp7 => 7,
        _8 | Kp8 => 8,
        _9 | Kp9 => 9,
        _ => return None,
    })
}

/// 处理键盘事件。`true` 表示请求退出主循环（原 `break 'run`）。
#[allow(clippy::too_many_arguments)]
pub(crate) fn handle_key_down(
    app: &mut AppState,
    keycode: Option<Keycode>,
    keymod: Mod,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
    draws: &[mir2_core::map::TileDraw],
    tiles: &std::collections::HashMap<crate::tiles::TileKey, crate::tiles::TileTex<'_>>,
    archive: Option<&mir2_core::m2pk::Archive>,
    asset_dir: &Option<std::path::PathBuf>,
) -> Result<bool, Box<dyn std::error::Error>> {
    // Alt 是挖肉的修饰键（`Alt` + 左键点动物尸体）
    if matches!(keycode, Some(Keycode::LAlt) | Some(Keycode::RAlt)) {
        app.world_control.alt_down = true;
    }
    match keycode {
        // **数量面板**（D-148）开着 ⇒ 它是世界里的**最上层模态**：
        // ESC 只关它、Enter = 确定、↑/↓（Shift 粗调）改件数。
        Some(Keycode::Escape) if app.qty.is_some() => app.qty = None,
        Some(Keycode::Return) | Some(Keycode::KpEnter) if app.qty.is_some() => {
            if crate::world_input::qty_confirm(app) {
                return Ok(true);
            }
        }
        Some(Keycode::Up) if app.qty.is_some() => {
            crate::world_input::qty_bump(app, true, shift(keymod))
        }
        Some(Keycode::Down) if app.qty.is_some() => {
            crate::world_input::qty_bump(app, false, shift(keymod))
        }
        // **数量面板开着时它是模态**：数字键直接打件数（主键盘/小键盘），退格删一位，
        // 其余键一律吞掉（别顺手把快捷栏的药嗑了）。Esc/Enter/↑↓ 在上面已经处理。
        Some(k) if app.qty.is_some() => {
            if let Some(d) = digit_key(k) {
                crate::world_input::qty_digit(app, d);
            } else if k == Keycode::Backspace {
                crate::world_input::qty_backspace(app);
            }
        }
        // **聊天输入行**开着 ⇒ 同样是模态：Enter 发送、Esc 取消、退格删字，
        // 其余键一律吞掉（否则打字时 W 会走路、F9 会开背包）。
        Some(Keycode::Escape) if app.chat_input.is_some() => app.chat_input = None,
        Some(Keycode::Return) | Some(Keycode::KpEnter) if app.chat_input.is_some() => {
            crate::world_input::chat_send(app)
        }
        Some(Keycode::Backspace) if app.chat_input.is_some() => {
            crate::world_input::chat_backspace(app)
        }
        Some(_) if app.chat_input.is_some() => {}
        // ⚠️ **登录/选角里 ESC 先交给场景**（2026-10-09 修）。
        //
        // 这条 arm 排在所有 mode 分支之前，原来无条件 `break 'run` ⇒
        // 那两个界面里按 ESC **直接退客户端**，于是
        // `select.rs` 的"ESC 关建角框 / 关弹窗 / 取消待确认"与 `login.rs`
        // 的"ESC 退建号面板 / 关报错弹窗"全是**不可达死代码**。
        // 现在放它们落到下面的 mode 分支去；场景要退（`Action::Exit` /
        // `Action::Quit`）才真退。其余模式（世界/素材浏览器）保持原样。
        // 确认框开着 ⇒ ESC **先关它**（别顺手把窗/客户端关了）
        Some(Keycode::Escape)
            if app.scene == crate::app_state::Scene::World && app.confirm.is_open() =>
        {
            app.confirm.cancel()
        }
        // 背包窗开着 ⇒ ESC **先关背包**
        Some(Keycode::Escape) if app.scene == crate::app_state::Scene::World && app.win.bag() => {
            app.win.close_bag()
        }
        // 状态窗同理 ⇒ ESC **先关状态窗**（别顺手把客户端退了）
        Some(Keycode::Escape)
            if app.scene == crate::app_state::Scene::World && app.win.status() =>
        {
            app.win.close_status()
        }
        // NPC 对话开着 ⇒ ESC **先关对话**（原版 `@exit`），别顺手退了客户端
        Some(Keycode::Escape)
            if app.scene == crate::app_state::Scene::World
                && app.net.as_ref().is_some_and(|n| n.world.dialog.is_some()) =>
        {
            if let Some(n) = app.net.as_ref() {
                if let Some(d) = n.world.dialog.as_ref() {
                    n.npc_close(d.npc_id);
                }
            }
            if let Some(n) = app.net.as_mut() {
                n.world.close_dialog();
            }
        }
        // 商店窗同理 ⇒ ESC **先关商店窗**，并把"因买东西才开的"包裹一起收走
        //（用户 2026-10-10 第 1 条：购买窗关闭 ⇒ 物品列表与包裹一起关）
        Some(Keycode::Escape) if app.scene == crate::app_state::Scene::World && app.win.shop() => {
            app.win.close_shop();
            app.shop_ui.close();
        }
        Some(Keycode::Escape)
            if !app.layout_edit.on
                && app.scene != crate::app_state::Scene::Login
                && app.scene != crate::app_state::Scene::Select =>
        {
            return Ok(true)
        }
        // 世界场景 **Enter = 打开聊天输入行**（原版回车打字；`chat_can_open`
        // 会挡掉"有模态/窗口开着"的情况，见 D-152）。
        Some(Keycode::Return) | Some(Keycode::KpEnter)
            if crate::world_input::chat_can_open(app) =>
        {
            crate::world_input::chat_open(app)
        }
        // 聊天区翻页（D-154）：PageUp 看更旧的，PageDown 往回贴最新。
        Some(Keycode::PageUp) if app.scene == crate::app_state::Scene::World => {
            if let Some(n) = app.net.as_mut() {
                n.chat.scroll_by(hud::CHAT_VIEW_LINES as i32);
            }
        }
        Some(Keycode::PageDown) if app.scene == crate::app_state::Scene::World => {
            if let Some(n) = app.net.as_mut() {
                n.chat.scroll_by(-(hud::CHAT_VIEW_LINES as i32));
            }
        }
        // 数字键 1..6 = **用快捷栏**（与点那一格同一个动作）
        Some(Keycode::_1) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 0)
        }
        Some(Keycode::_2) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 1)
        }
        Some(Keycode::_3) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 2)
        }
        Some(Keycode::_4) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 3)
        }
        Some(Keycode::_5) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 4)
        }
        Some(Keycode::_6) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::belt_key(&mut app.net, &mut app.belt, 5)
        }
        // F1..F8 = **技能**（原版键位，ClMain.pas:2038-2048）：
        // 技能窗里选中了某条 ⇒ 给它设快捷键；否则 ⇒ 放绑在那个键上的技能。
        Some(Keycode::F1) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 0)
        }
        Some(Keycode::F2) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 1)
        }
        Some(Keycode::F3) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 2)
        }
        Some(Keycode::F4) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 3)
        }
        Some(Keycode::F5) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 4)
        }
        Some(Keycode::F6) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 5)
        }
        Some(Keycode::F7) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 6)
        }
        Some(Keycode::F8) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::skill_hotkey(app, 7)
        }
        // F11 = **技能窗**（原版 `StatePage := 3` + 打开状态窗，ClMain.pas:2056）
        Some(Keycode::F11) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::open_skill_window(app)
        }
        // ⚠️ **开发键让开原版键位**（口径见 `docs/use.md`）：F1~F8 是技能、
        // F9~F12 是包裹/属性/技能/内挂、M 是大地图、Tab 是小地图、数字是快捷物品
        // ⇒ 这些"开发查看器"入口统统收进 **Ctrl+**，原版键位留给真功能。
        // F9 = 背包窗（原版键位，见上面那条注释的口径）
        Some(Keycode::F9) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::toggle_bag(&mut app.win, &mut app.bag_page)
        }
        // F10 = 状态窗（原版键位：F9 包裹 / F10 属性 / F11 技能 / F12 内挂）。
        // ⚠️ macOS 上要 **Fn+F10**（F10 默认是 App Exposé）—— 2026-10-10
        // 用户确认 Fn+F10 能用；曾加过的备用键 Ctrl+C 已撤（不再占 C）。
        Some(Keycode::F10) if app.scene == crate::app_state::Scene::World => {
            crate::world_input::toggle_status(&mut app.win)
        }
        Some(Keycode::F1) if ctrl(keymod) => app.scene = crate::app_state::Scene::Login,
        Some(Keycode::F2) if ctrl(keymod) => app.scene = crate::app_state::Scene::World,
        Some(Keycode::F3) if ctrl(keymod) => app.scene = crate::app_state::Scene::AssetView,
        // **界面开发器**（D-134/D-136）：Ctrl+L 在世界、登录、选角三场景切换通用布局编辑。
        Some(Keycode::L)
            if ctrl(keymod)
                && matches!(
                    app.scene,
                    crate::app_state::Scene::World
                        | crate::app_state::Scene::Login
                        | crate::app_state::Scene::Select
                ) =>
        {
            app.layout_edit.toggle()
        }
        Some(Keycode::G) if app.layout_edit.on => app.layout_edit.cycle_grid(),
        Some(Keycode::A) if app.layout_edit.on => app.layout_edit.cycle_align(shift(keymod)),
        Some(Keycode::Z) if app.layout_edit.on && ctrl(keymod) => {
            if shift(keymod) {
                app.layout_edit.redo();
            } else {
                app.layout_edit.undo();
            }
        }
        Some(Keycode::Y) if app.layout_edit.on && ctrl(keymod) => app.layout_edit.redo(),
        // 放大镜：以**当前鼠标位置**为中心（用户 2026-10-12："用 +- 号以鼠标位置为中心
        // 放大画面倍数，最高 8 倍、最小 1 倍"）
        Some(Keycode::Equals | Keycode::Plus)
            if app.layout_edit.on
                && app.layout_edit.scope() == crate::layout::EditorScope::World =>
        {
            app.layout_edit.zoom_by(1.0, app.mouse)
        }
        Some(Keycode::Minus)
            if app.layout_edit.on
                && app.layout_edit.scope() == crate::layout::EditorScope::World =>
        {
            app.layout_edit.zoom_by(-1.0, app.mouse)
        }
        Some(Keycode::S) if app.layout_edit.on => app.layout_edit.save(),
        Some(Keycode::R) if app.layout_edit.on => app.layout_edit.reset_selected(),
        Some(Keycode::Escape) if app.layout_edit.on => app.layout_edit.toggle(),
        // 方向键微调（Shift 加速到 10px）
        Some(Keycode::Left) if app.layout_edit.on => {
            let d = if shift(keymod) { -10.0 } else { -1.0 };
            app.layout_edit.nudge(d, 0.0)
        }
        Some(Keycode::Right) if app.layout_edit.on => {
            let d = if shift(keymod) { 10.0 } else { 1.0 };
            app.layout_edit.nudge(d, 0.0)
        }
        Some(Keycode::Up) if app.layout_edit.on => {
            let d = if shift(keymod) { -10.0 } else { -1.0 };
            app.layout_edit.nudge(0.0, d)
        }
        Some(Keycode::Down) if app.layout_edit.on => {
            let d = if shift(keymod) { 10.0 } else { 1.0 };
            app.layout_edit.nudge(0.0, d)
        }
        Some(Keycode::M) if ctrl(keymod) => {
            app.music_on = !app.music_on;
            sound.set_music_on(app.music_on);
            // 带上"正在响几路 / 有没有 BGM"：一眼看出混音器是不是活的
            let (voices, bgm, ..) = sound.stats();
            app.status = format!(
                "MUSIC {}  [sfx {} voices, bgm {}]",
                if app.music_on { "ON" } else { "OFF" },
                voices,
                if bgm { "ON" } else { "OFF" }
            );
        }
        Some(Keycode::N) if ctrl(keymod) => {
            app.sfx_on = !app.sfx_on;
            sound.set_sfx_on(app.sfx_on);
            let (voices, bgm, ..) = sound.stats();
            app.status = format!(
                "SOUND {}  [sfx {} voices, bgm {}]",
                if app.sfx_on { "ON" } else { "OFF" },
                voices,
                if bgm { "ON" } else { "OFF" }
            );
        }
        // 登录/选角布局编辑时屏蔽场景业务键（提交/建角/确认），防止误触发操作。
        _ if app.layout_edit.on
            && matches!(
                app.scene,
                crate::app_state::Scene::Login | crate::app_state::Scene::Select
            ) => {}
        // 选角：键盘是**我们的扩展**（原版选角场景只认鼠标）
        _ if app.scene == crate::app_state::Scene::Select => {
            if let Some(k) = keycode {
                let act = match app.select_scene.as_mut() {
                    Some(s) => s.on_key(scene_key(k)),
                    None => select::Action::None,
                };
                if do_select_action(act, &mut app.net, &mut app.select_scene, sound, sounds)? {
                    return Ok(true);
                }
            }
        }
        // 登录界面：全部交互在 `login` 里（Tab/退格/回车/ESC），这里只把
        // 它给出的动作翻译成"接下来干什么"。
        _ if app.scene == crate::app_state::Scene::Login => {
            if let Some(k) = keycode {
                let action = app.login.on_key(scene_key(k));
                if crate::flow::do_login_action(
                    action,
                    &mut app.login,
                    &mut app.net,
                    &mut app.status,
                    sound,
                    sounds,
                ) {
                    return Ok(true);
                }
            }
        }
        // 素材浏览器（开发用）
        _ if app.scene == crate::app_state::Scene::AssetView => match keycode {
            Some(Keycode::LeftBracket) => {
                let count = asset_dir
                    .as_deref()
                    .map(crate::assets::viewer_libraries)
                    .map_or(0, |libs| libs.len());
                app.asset_view.previous_library(count);
            }
            Some(Keycode::RightBracket) => {
                let count = asset_dir
                    .as_deref()
                    .map(crate::assets::viewer_libraries)
                    .map_or(0, |libs| libs.len());
                app.asset_view.next_library(count);
            }
            Some(Keycode::Comma) | Some(Keycode::Left) => app.asset_view.previous_image(),
            Some(Keycode::Period) | Some(Keycode::Right) => app.asset_view.next_image(),
            _ => {}
        },
        _ if app.scene == crate::app_state::Scene::World => match keycode {
            // 方向键：**联网且在世界里 ⇒ 走一步**（相机跟着自己）；否则平移镜头。
            Some(Keycode::Left) => {
                if !walk_if_online(&app.net, mir2_protocol::Direction::DirLeft) {
                    app.cam.0 -= 2.0
                }
            }
            Some(Keycode::Right) => {
                if !walk_if_online(&app.net, mir2_protocol::Direction::DirRight) {
                    app.cam.0 += 2.0
                }
            }
            Some(Keycode::Up) => {
                if !walk_if_online(&app.net, mir2_protocol::Direction::DirUp) {
                    app.cam.1 -= 2.0
                }
            }
            Some(Keycode::Down) => {
                if !walk_if_online(&app.net, mir2_protocol::Direction::DirDown) {
                    app.cam.1 += 2.0
                }
            }
            // Tab = **小地图**开关、M = **大地图**开关（原版 1.76 的键位，
            // 见 `docs/use.md`；音乐已经挪到 Ctrl+M，不再抢 M）。
            Some(Keycode::Tab) => crate::world_input::toggle_minimap(&mut app.minimap_on),
            Some(Keycode::M) => app.bigmap_on = !app.bigmap_on,
            // Ctrl+H = **切换攻击模式**（原版 `ClMain.pas:2078-2081`：word('H') +
            // `ssCtrl` ⇒ `SendSay('@AttackMode')`）。命令文本用服务端认的那个
            // （`@atkmode`）；模式变化由服务端的系统提示同步回底栏显示
            //（见 `net::Net::attack_mode` 的说明）。
            Some(Keycode::H) if ctrl(keymod) && app.scene == crate::app_state::Scene::World => {
                if let Some(n) = app.net.as_ref() {
                    n.say("@atkmode");
                    println!("[net] 发送 @atkmode（切换攻击模式）");
                }
            }
            // 空格：打一下身边的目标（A′：走 + 砍 = 能玩）。**左键点怪**才是
            // 主路（会锁住目标、自动靠近）—— 见下面 `MouseButtonDown` 那段。
            Some(Keycode::Space) => {
                if let Some(n) = app.net.as_ref().filter(|n| n.world.in_world()) {
                    if n.attack_adjacent() {
                        swing_sfx(n, sound, sounds);
                    } else {
                        println!("[net] 身边没有可打的目标（八格内）");
                    }
                }
            }
            // C：连接/断开新协议服务端（地址与会话号走环境变量，见 `Net::connect`）。
            Some(Keycode::C) => {
                if app.net.is_some() {
                    println!("[net] 主动断开");
                    app.net = None;
                } else {
                    match Net::connect() {
                        Ok(n) => {
                            println!("[net] {}", n.status);
                            app.net = Some(n);
                        }
                        Err(e) => println!("[net] 连不上：{e}"),
                    }
                }
            }
            Some(Keycode::Home) => app.cam = (0.0, 0.0),
            // ---- 调试叠加层（只在地图模式，避免污染登录输入框）----
            // 辅助线/坐标叠加层：默认关闭（见 `DEBUG_OVERLAY`）
            Some(Keycode::D) if DEBUG_OVERLAY => {
                app.debug = !app.debug;
                println!(
                                "[debug] 叠加层 {}（L 或 CTRL+1/2/3 控制图层显隐 / P 打印绘制清单 / 左键点哪读哪）",
                                if app.debug { "ON" } else { "OFF" }
                            );
            }
            // 逐层独立显隐：排查错位时最常用的是"关掉一层看底下那层"
            Some(Keycode::_1) | Some(Keycode::_2) | Some(Keycode::_3)
                if DEBUG_LAYERS
                    && (keymod.intersects(Mod::LCTRLMOD) || keymod.intersects(Mod::RCTRLMOD)) =>
            {
                let (bit, name) = match keycode {
                    Some(Keycode::_1) => (1u8, "地表 Tiles"),
                    Some(Keycode::_2) => (2u8, "中间 SmTiles"),
                    _ => (4u8, "前景 Objects"),
                };
                app.layers ^= bit;
                println!(
                    "[layer] {} {}   →   当前可见 {}（G=地表 M=中间 F=前景）",
                    name,
                    if app.layers & bit != 0 {
                        "显示"
                    } else {
                        "隐藏"
                    },
                    layers_desc(app.layers)
                );
            }
            // `L` 是同一个功能的"循环"绑定 —— 只关 CTRL 那三个键等于
            // 留了后门，所以一起跟着 `DEBUG_LAYERS` 走。
            Some(Keycode::L) if DEBUG_LAYERS => {
                // 循环：全部 → 仅地表 → 仅中间 → 仅前景 → 全部
                app.layers = match app.layers {
                    LAYERS_ALL => 1,
                    1 => 2,
                    2 => 4,
                    _ => LAYERS_ALL,
                };
                println!(
                    "[layer] 过滤循环   →   当前可见 {}",
                    layers_desc(app.layers)
                );
            }
            Some(Keycode::P) => {
                dump_draws(draws, cam_parts(app.cam).cell, tiles, app.layers);
            }
            Some(Keycode::LeftBracket) | Some(Keycode::RightBracket) => {
                if let Some(a) = &archive {
                    let step: i64 = if keycode == Some(Keycode::RightBracket) {
                        1
                    } else {
                        -1
                    };
                    let n = a.len() as i64;
                    app.map_i = (((app.map_i as i64 + step) % n + n) % n) as usize;
                    load_map(a, app.map_i, &mut app.map, &mut app.map_err, &mut app.cam);
                }
            }
            _ => {}
        },
        _ => {}
    }
    Ok(false)
}

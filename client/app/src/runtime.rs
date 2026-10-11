//! 主循环执行器：SDL 事件分派、运行态更新与 `render::draw_frame` 编排。
//!
//! `main.rs` 负责创建平台资源；资源建好后交给这里运行。长期可继续把事件处理
//! 按输入域拆开，但所有可变会话状态已经归到 `AppState`。

use std::collections::HashMap;
use std::time::{Duration, Instant};

use crate::*;

/// 往聊天区说一句（点了个"还没做"的按钮时用 —— **绝不静默**）。
fn ui_say(net: Option<&mut Net>, msg: &str) {
    println!("[ui] {msg}");
    if let Some(n) = net {
        n.chat.push(msg.to_string(), C_CHAT_SYS);
    }
}

/// 开关状态窗（F10 与底栏那颗 [人物] 都走这里 —— 鼠标与键盘必须同一个动作）。
fn toggle_status(win: &mut windows::Windows) {
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
fn belt_key(
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
fn toggle_minimap(minimap_on: &mut bool) {
    *minimap_on = !*minimap_on;
    println!("[ui] 小地图 {}", if *minimap_on { "打开" } else { "关闭" });
}

/// 开关背包（**F9 与底栏那颗 [包裹] 都走这里**）。
///
/// ⚠️ 原来 F9 那支还顺手把 `bag_page` 归零 —— 抽出来就是为了"点按钮"和"按 F9"
/// **一模一样**（用户 2026-10-11：鼠标行为与键盘行为要一致），别一处归零一处不归。
fn toggle_bag(win: &mut windows::Windows, bag_page: &mut pager::Pager) {
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

#[allow(clippy::too_many_arguments)]
pub(crate) fn run_app<T>(
    mut app: app_state::AppState,
    mut events: EventPump,
    mut canvas: sdl3::render::WindowCanvas,
    tex_creator: sdl3::render::TextureCreator<T>,
    sound: audio::Audio,
    sounds: Option<mir2_core::sound::SoundAssets>,
    asset_dir: Option<std::path::PathBuf>,
    archive: Option<Archive>,
) -> Result<(), Box<dyn std::error::Error>> {
    // **运行态统一归属**（架构方向 ⑥）：场景 / UI / 世界交互 / 地图状态从几十个
    // `let mut` 收进 `AppState`；SDL、画布、纹理与缓存仍留在这一层（资源生命周期）。
    // 界面 / 渲染资源（带 SDL 纹理生命周期，暂留在 `main`）
    let mut ui = ui::UiCache::new();
    let mut ui_texts = font::TextCache::new(mir2_core::text::UI_PX);
    let mut names = font::TextCache::new(mir2_core::text::NAME_PX);

    // 素材浏览器的独立数据与 GPU 纹理（F3）
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // SDL / GPU 资源（有平台句柄或缓存生命周期，不属于纯运行态）
    let cursor_arrow = Cursor::from_system(SystemCursor::Arrow)?;
    let cursor_cross = Cursor::from_system(SystemCursor::Crosshair)?;
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, TileTex<'_>> = HashMap::new();
    let mut sprites = SpriteCache::new();
    let mut draws: Vec<TileDraw> = Vec::new();
    // 环境变量给了会话号就**开机自动连**（省得每次手按 `C`；开发时最常用）。
    if std::env::var("MIR2_SESSION").is_ok() {
        match Net::connect() {
            Ok(n) => {
                println!("[net] {}", n.status);
                app.net = Some(n);
            }
            Err(e) => println!("[net] 连不上：{e}"),
        }
    }

    // 给了会话号就说明"我已经有会话了" ⇒ 直接进地图（登录界面留给真登录用）
    if app.net.is_some() {
        app.mode = 2;
    }

    // 载入初始地图
    if let Some(a) = &archive {
        load_map(a, app.map_i, &mut app.map, &mut app.map_err, &mut app.cam);
    }

    'run: loop {
        // 每帧重新取"现在"：所有按时间推进的东西（选角动画、开门动画、移动补间）
        // 都拿它当基准。⚠️ 漏了这行 = 动画全部静止（踩过）。
        app.begin_frame();

        for ev in events.poll_iter() {
            // ⚠️ 事件里的 `x/y` 是**窗口坐标**（高 DPI 下还是物理像素），而界面画在
            // 800×600 的逻辑空间里 ⇒ 一律让 SDL 换算（它同时管逻辑呈现的缩放与留边）。
            // 漏了它的表现是"按钮点不中 / 点哪走哪偏一截"（踩过）。
            let ev = ev.get_converted_coords(&canvas).unwrap_or(ev);
            match ev {
                Event::Quit { .. } => break 'run,
                // 窗口尺寸/像素尺寸变了 ⇒ 重挑呈现模式（见 `apply_presentation`）。
                // ⚠️ 必须挂上：不然后面那次 resize 还把画布按老尺寸缩放（画面拉变形）。
                Event::Window { win_event, .. } => {
                    let changed = matches!(
                        win_event,
                        WindowEvent::Resized(..) | WindowEvent::PixelSizeChanged(..)
                    );
                    if changed {
                        if let Err(e) = apply_presentation(&mut canvas) {
                            eprintln!("[mir2-app] {e}");
                        }
                    }
                }
                Event::KeyDown {
                    keycode, keymod, ..
                } => {
                    // Alt 是挖肉的修饰键（`Alt` + 左键点动物尸体）
                    if matches!(keycode, Some(Keycode::LAlt) | Some(Keycode::RAlt)) {
                        app.alt_down = true;
                    }
                    match keycode {
                        // ⚠️ **登录/选角里 ESC 先交给场景**（2026-10-09 修）。
                        //
                        // 这条 arm 排在所有 mode 分支之前，原来无条件 `break 'run` ⇒
                        // 那两个界面里按 ESC **直接退客户端**，于是
                        // `select.rs` 的"ESC 关建角框 / 关弹窗 / 取消待确认"与 `login.rs`
                        // 的"ESC 退建号面板 / 关报错弹窗"全是**不可达死代码**。
                        // 现在放它们落到下面的 mode 分支去；场景要退（`Action::Exit` /
                        // `Action::Quit`）才真退。其余模式（世界/素材浏览器）保持原样。
                        // 确认框开着 ⇒ ESC **先关它**（别顺手把窗/客户端关了）
                        Some(Keycode::Escape) if app.mode == 2 && app.confirm.is_open() => {
                            app.confirm.cancel()
                        }
                        // 背包窗开着 ⇒ ESC **先关背包**
                        Some(Keycode::Escape) if app.mode == 2 && app.win.bag() => {
                            app.win.close_bag()
                        }
                        // 状态窗同理 ⇒ ESC **先关状态窗**（别顺手把客户端退了）
                        Some(Keycode::Escape) if app.mode == 2 && app.win.status() => {
                            app.win.close_status()
                        }
                        // NPC 对话开着 ⇒ ESC **先关对话**（原版 `@exit`），别顺手退了客户端
                        Some(Keycode::Escape)
                            if app.mode == 2
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
                        Some(Keycode::Escape) if app.mode == 2 && app.win.shop() => {
                            app.win.close_shop();
                            app.shop_ui.close();
                        }
                        Some(Keycode::Escape) if app.mode != 1 && app.mode != 4 => break 'run,
                        // 数字键 1..6 = **用快捷栏**（与点那一格同一个动作）
                        Some(Keycode::_1) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 0)
                        }
                        Some(Keycode::_2) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 1)
                        }
                        Some(Keycode::_3) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 2)
                        }
                        Some(Keycode::_4) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 3)
                        }
                        Some(Keycode::_5) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 4)
                        }
                        Some(Keycode::_6) if app.mode == 2 => {
                            belt_key(&mut app.net, &mut app.belt, 5)
                        }
                        // ⚠️ **开发键让开原版键位**（口径见 `docs/use.md`）：F1~F8 是技能、
                        // F9~F12 是包裹/属性/技能/内挂、M 是大地图、Tab 是小地图、数字是快捷物品
                        // ⇒ 这些"开发查看器"入口统统收进 **Ctrl+**，原版键位留给真功能。
                        // F9 = 背包窗（原版键位，见上面那条注释的口径）
                        Some(Keycode::F9) if app.mode == 2 => {
                            toggle_bag(&mut app.win, &mut app.bag_page)
                        }
                        // F10 = 状态窗（原版键位：F9 包裹 / F10 属性 / F11 技能 / F12 内挂）。
                        // ⚠️ macOS 上要 **Fn+F10**（F10 默认是 App Exposé）—— 2026-10-10
                        // 用户确认 Fn+F10 能用；曾加过的备用键 Ctrl+C 已撤（不再占 C）。
                        Some(Keycode::F10) if app.mode == 2 => toggle_status(&mut app.win),
                        Some(Keycode::F1) if ctrl(keymod) => app.mode = 1,
                        Some(Keycode::F2) if ctrl(keymod) => app.mode = 2,
                        Some(Keycode::F3) if ctrl(keymod) => app.mode = 3,
                        Some(Keycode::M) if ctrl(keymod) => {
                            app.music_on = !app.music_on;
                            sound.set_music_on(app.music_on);
                            // 带上"正在响几路 / 有没有 BGM"：一眼看出混音器是不是活的
                            let (voices, bgm, ..) = sound.stats();
                            status = format!(
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
                            status = format!(
                                "SOUND {}  [sfx {} voices, bgm {}]",
                                if app.sfx_on { "ON" } else { "OFF" },
                                voices,
                                if bgm { "ON" } else { "OFF" }
                            );
                        }
                        // 选角：键盘是**我们的扩展**（原版选角场景只认鼠标）
                        _ if app.mode == 4 => {
                            if let Some(k) = keycode {
                                let act = match app.select_scene.as_mut() {
                                    Some(s) => s.on_key(k),
                                    None => select::Action::None,
                                };
                                if do_select_action(
                                    act,
                                    &mut app.net,
                                    &mut app.select_scene,
                                    &sound,
                                    &sounds,
                                )? {
                                    break 'run;
                                }
                            }
                        }
                        // 登录界面：全部交互在 `login` 里（Tab/退格/回车/ESC），这里只把
                        // 它给出的动作翻译成"接下来干什么"。
                        _ if app.mode == 1 => {
                            if let Some(k) = keycode {
                                let act = app.login.on_key(k);
                                // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                                if act != login::Action::None {
                                    sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                                }
                                match act {
                                    login::Action::Submit => {
                                        submit_login(&mut app.login, &mut app.net, &mut status)
                                    }
                                    login::Action::SubmitSignup => {
                                        submit_signup(&mut app.login, &mut app.net, &mut status)
                                    }
                                    login::Action::NewAccount => {
                                        // 面板切换在 `login` 里已经做完了（`enter_signup`），
                                        // 这里只给状态栏一句人话（原版是开 `DNewAccount`）。
                                        status = "NEW ACCOUNT".into();
                                    }
                                    login::Action::CancelSignup => status = String::new(),
                                    login::Action::ChangePassword => {
                                        status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                    }
                                    login::Action::Quit => break 'run,
                                    login::Action::None | login::Action::Dismiss => {}
                                }
                            }
                        }
                        // 素材浏览器（开发用）
                        _ if app.mode == 3 => match keycode {
                            Some(Keycode::LeftBracket) => {
                                lib_idx = (lib_idx + LIBS.len() - 1) % LIBS.len();
                                img_idx = 0;
                            }
                            Some(Keycode::RightBracket) => {
                                lib_idx = (lib_idx + 1) % LIBS.len();
                                img_idx = 0;
                            }
                            Some(Keycode::Comma) | Some(Keycode::Left) => {
                                img_idx = img_idx.saturating_sub(1);
                            }
                            Some(Keycode::Period) | Some(Keycode::Right) => img_idx += 1,
                            _ => {}
                        },
                        _ if app.mode == 2 => match keycode {
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
                            Some(Keycode::Tab) => toggle_minimap(&mut app.minimap_on),
                            Some(Keycode::M) => app.bigmap_on = !app.bigmap_on,
                            // 空格：打一下身边的目标（A′：走 + 砍 = 能玩）。**左键点怪**才是
                            // 主路（会锁住目标、自动靠近）—— 见下面 `MouseButtonDown` 那段。
                            Some(Keycode::Space) => {
                                if let Some(n) = app.net.as_ref().filter(|n| n.world.in_world()) {
                                    if n.attack_adjacent() {
                                        swing_sfx(n, &sound, &sounds);
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
                                    && (keymod.intersects(Mod::LCTRLMOD)
                                        || keymod.intersects(Mod::RCTRLMOD)) =>
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
                                dump_draws(&draws, cam_parts(app.cam).cell, &tiles, app.layers);
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
                                    load_map(
                                        a,
                                        app.map_i,
                                        &mut app.map,
                                        &mut app.map_err,
                                        &mut app.cam,
                                    );
                                }
                            }
                            _ => {}
                        },
                        _ => {}
                    }
                }
                // 松开 Alt ⇒ 挖肉的修饰键失效（SDL 的鼠标事件不带 keymod ⇒ 自己记）
                Event::KeyUp { keycode, .. } => {
                    if matches!(keycode, Some(Keycode::LAlt) | Some(Keycode::RAlt)) {
                        app.alt_down = false;
                        app.butch_target = None; // 按住才持续挖；松手就停
                    }
                }
                // 鼠标位置（已在循环头换算成界面的 800×600 空间）
                Event::MouseMotion { x, y, .. } => {
                    app.mouse = (x, y);
                    app.ui_click.set_mouse(crate::widget::canvas_pt(app.mouse));
                }
                // 滚轮：两个**固定高**的窗都靠它翻内容 —— 背包窗翻页、对话窗卷行。
                //
                // ⚠️ 合成一条分支（原来那条只认背包）：两个窗可能同时开着，
                // 分开写的话先命中的那条会把另一个的滚轮吃掉（match 不穿透）。
                Event::MouseWheel { y, .. } if app.mode == 2 => {
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
                                .map_or(1, |n| {
                                    n.world.bag.len().div_ceil(crate::layout::BAG_PAGE_SLOTS)
                                })
                                .max(1);
                            if y > 0.0 {
                                app.bag_page.step(1, pages);
                            } else if y < 0.0 {
                                app.bag_page.step(-1, pages);
                            }
                            done = true;
                        }
                    }
                    // ② 对话窗内滚轮卷行（正文折行后常常超出背板：298 个脚本里 114 个超一屏）
                    if !done && app.net.is_some() {
                        if let Some((panel, lines, _)) =
                            hud::dialog_geom(app.net.as_ref(), app.dlg_scroll.scroll())
                        {
                            if input::dialog_hit(panel, app.mouse) {
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
                // 鼠标移动：**在世界里 ⇒ 左键走 / 右键跑**（照原版 `ClMain.pas:2246-2352`：
                // 左键 = 走，右键 = 跑；方向由鼠标相对角色的方位定，见 `dir_to`）。
                // 离线看地图（没进世界）时左键仍是那个诊断探针 —— 它是开发用的，
                // 别和"走路"抢同一个键。
                Event::MouseButtonDown {
                    mouse_btn, x, y, ..
                } if app.mode == 2 => {
                    let in_world = app.net.as_ref().is_some_and(|n| n.world.in_world());
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
                                    if let Some(dir) = asset_dir.as_deref() {
                                        let c = hud::confirm_geom(&mut ui, dir, (WIN_W, WIN_H));
                                        if let Some(c) = c {
                                            // OnClick 规范 ⇒ 按下只记，松开才执行（见 `widget`）
                                            if c.ok.hit(app.mouse) {
                                                app.ui_click.press(
                                                    Press::ConfirmOk,
                                                    crate::widget::canvas_rect((
                                                        c.ok.x, c.ok.y, c.ok.w, c.ok.h,
                                                    )),
                                                );
                                            } else if c.cancel.hit(app.mouse) {
                                                app.ui_click.press(
                                                    Press::ConfirmCancel,
                                                    crate::widget::canvas_rect((
                                                        c.cancel.x, c.cancel.y, c.cancel.w,
                                                        c.cancel.h,
                                                    )),
                                                );
                                            } else if !c.frame.hit(app.mouse) {
                                                // 点框外 = 收起模态（不是按钮）⇒ 即时生效
                                                app.confirm.cancel();
                                            }
                                        } else {
                                            app.confirm.cancel(); // 量不到素材 ⇒ 别把人卡住
                                        }
                                    }
                                }
                                app.combat_target = None;
                                app.move_target = None;
                                app.press_at = None;
                                app.pending_pickup = None;
                                app.held_move = None;
                                continue;
                            }
                            // ①‴ **底栏右上那 4 个功能按钮**（人物/包裹/技能/选项，
                            // 图号 8-11，用户 2026-10-11）：点它们 = 按对应的功能键，
                            // 走**同一个** `toggle_*` ⇒ 鼠标与键盘行为一致。
                            // ⚠️ 面板盖在世界之上 ⇒ 点中就要 `ui_consumed`（不然人还会走）。
                            if mouse_btn == MouseButton::Left {
                                if let Some(b) = crate::layout::hud_btn_at(app.mouse) {
                                    // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                                    app.ui_click
                                        .press(Press::Hud(b), crate::widget::canvas_rect(b.rect()));
                                    // 目标清掉，**本次点击不再穿透到世界**（与其它浮窗
                                    // 同一条纪律：命中就 `continue`，不然人还会走）。
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.pending_pickup = None;
                                    app.held_move = None;
                                    continue;
                                }
                                // **快捷（腰带）栏**：手上拿着东西 ⇒ 放进这格；
                                // 空手 ⇒ 使用这格里的东西（原版单击是"拿起"，
                                // 但快捷栏的意义就是快速使用 ⇒ 单击即用）。
                                if let Some(i) = crate::layout::belt_slot_at(app.mouse) {
                                    app.ui_click.press(
                                        Press::Belt(i),
                                        crate::widget::canvas_rect(crate::layout::belt_slot_rect(
                                            i,
                                        )),
                                    );
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.pending_pickup = None;
                                    app.held_move = None;
                                    continue;
                                }
                                // 底栏**左下**那排快捷按钮（小地图/交易/行会/组队/属性）。
                                // 小地图那颗 = **Tab** 同一个动作（`toggle_minimap`）。
                                if let Some(b) = crate::layout::hud_quick_at(app.mouse) {
                                    // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                                    app.ui_click.press(
                                        Press::Quick(b),
                                        crate::widget::canvas_rect(b.rect()),
                                    );
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.pending_pickup = None;
                                    app.held_move = None;
                                    continue;
                                }
                            }
                            // 左键才判"单击"（用户第 3 条说的是左键）；右键仍是按住=跑
                            if mouse_btn == MouseButton::Left {
                                app.press_at = Some(Instant::now());
                                app.press_cell = cell;
                            } else {
                                app.press_at = None;
                            }
                            // **挖肉锁定**（原版 `CM_BUTCH`，用户第 1 条）：**按住 Alt 点一下**
                            // 动物尸体 ⇒ 锁定为"正在挖它"，**按住不放就持续挖**（不是反复点）。
                            // 皮革度要挖好几刀才归零（鸡 50，一刀 -5..20）⇒ 一次点击出不了肉。
                            let mut did_butch = false;
                            if mouse_btn == MouseButton::Left && app.alt_down {
                                if let Some(n) = app.net.as_ref() {
                                    let corpse = n
                                        .world
                                        .entities
                                        .values()
                                        .find(|e| e.dead && (e.x, e.y) == cell);
                                    if let Some(e) = corpse {
                                        app.butch_target = Some(e.id);
                                        app.butch_next = Instant::now(); // 立刻挖第一刀
                                        println!("[net] 挖肉锁定 {}", e.name);
                                        did_butch = true;
                                        // 挖肉不是"选目标/走路"⇒ 目标清掉（下面 `(ct, mt)`
                                        // 走 None 那支），也别把这次当"单击走一格"。
                                        app.press_at = None;
                                        app.pending_pickup = None;
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
                                        let idx = app.bag_page.page()
                                            * crate::layout::BAG_PAGE_SLOTS
                                            + slot;
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
                                                n.world.bag.get(i).and_then(|o| o.as_ref()).map(
                                                    |it| bagdrag::Cell {
                                                        make: it.make_index,
                                                        std_mode: it.std_mode,
                                                    },
                                                )
                                            };
                                            app.drag.click_bag(
                                                idx,
                                                Instant::now(),
                                                n.world.repair,
                                                cell,
                                            )
                                        };
                                        apply_bag_act(act, &mut app.net);
                                        // 抓在手上了 ⇒ 别再走过去 / 捡地上的东西
                                        if matches!(act, bagdrag::Act::PickUp { .. }) {
                                            app.press_at = None;
                                            app.pending_pickup = None;
                                        }
                                        bag_handled = true;
                                    }
                                }
                                if bag_handled {
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.held_move = None;
                                    continue;
                                }
                            }
                            let (ct, mt) = if did_butch || app.butch_target.is_some() {
                                // 挖肉中（含"这一下就是挖肉"）⇒ **不走路、不选目标**
                                //（用户 2026-10-10 第 1 条：挖肉时人物不该动）
                                (None, None)
                            } else {
                                // 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：
                                // **先清掉旧目标**，点到**活怪**就锁住它（之后每帧自动靠近/出手，
                                // 直到它死掉或消失）；点空地 ⇒ 走/跑到那一格。
                                app.net
                                    .as_ref()
                                    .map(|n| mouse_intent(&n.world, cell, run, app.hover))
                                    .unwrap_or((None, None))
                            };
                            app.combat_target = ct;
                            app.move_target = mt;
                            // 点在**地上有东西**的格子 ⇒ 记下"待捡"：走到那格自动捡
                            //（原版点物品也是先走过去；到了就发，见帧循环里那段）。
                            if mouse_btn == MouseButton::Left {
                                if let Some(n) = app.net.as_ref() {
                                    match n.world.ground.values().find(|g| (g.x, g.y) == cell) {
                                        Some(gi) if n.world.self_pos == cell => {
                                            n.pickup(gi.ground_id);
                                            app.pending_pickup = None;
                                        }
                                        Some(gi) => app.pending_pickup = Some((gi.ground_id, cell)),
                                        None => app.pending_pickup = None,
                                    }
                                }
                            }
                            app.attack_at = Instant::now(); // 立刻判"该出手还是该靠近"
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
                            app.held_move = Some(mouse_btn);
                            app.retarget_at = Instant::now();
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
                                    // **行内可点文字**：命中矩形由 `input::dialog_line_pieces`
                                    // 算（与 `draw_dialog` 是同一份 ⇒ 画哪点哪），
                                    // 片段自带**选项序号**（服务端把 `<打开/@trading>` 改写成了
                                    // `<打开/@1>`），直接拿它回包。
                                    let hit = {
                                        let mut measure = |t: &str| ui_texts.width(t);
                                        // ⚠️ 命中要带上**滚动位置**：画的是卷过之后的那几行，
                                        // 拿未卷的行号去判就会点空/点到隔壁（画与命中同源）。
                                        input::dialog_link_at(
                                            panel,
                                            &lines,
                                            (x, y),
                                            scroll,
                                            &mut measure,
                                        )
                                    };
                                    if let Some(idx) = hit {
                                        if let Some(d) = n.world.dialog.as_ref() {
                                            n.npc_select(d.npc_id, idx);
                                            println!("[net] 对话选项 {idx}");
                                        }
                                    } else if input::dialog_close_hit(panel, (x, y)) {
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
                                    if input::dialog_hit(panel, (x, y)) {
                                        app.combat_target = None;
                                        app.move_target = None;
                                        app.press_at = None;
                                        app.held_move = None;
                                        ui_consumed = true;
                                    }
                                }
                            }
                            // ②″ F10 状态窗：点 X = 关窗、点箭头 = 翻页、点槽 = 报一下那件；
                            // 点在窗里别处 = 吞掉（与背包窗同一套）
                            if app.win.status() {
                                if let Some(dir) = asset_dir.as_deref() {
                                    if let Some(bg) =
                                        ui.size(dir, crate::status::BG_LIB, crate::status::BG)
                                    {
                                        let (wx, wy, ww, wh) = crate::status::panel(bg);
                                        if x >= wx && x < wx + ww && y >= wy && y < wy + wh {
                                            ui_consumed = true;
                                            match crate::status::hit((x - wx, y - wy)) {
                                                // OnClick 规范 ⇒ 按下只记（动作在 MouseUp 里做）
                                                crate::status::Hit::Close => {
                                                    let c = crate::status::CLOSE;
                                                    app.ui_click.press(
                                                        Press::Status(crate::status::Hit::Close),
                                                        crate::widget::canvas_rect((
                                                            wx + c.0,
                                                            wy + c.1,
                                                            c.2,
                                                            c.3,
                                                        )),
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
                                                        crate::widget::canvas_rect((
                                                            wx + a.0,
                                                            wy + a.1,
                                                            a.2,
                                                            a.3,
                                                        )),
                                                    );
                                                }
                                                crate::status::Hit::Slot(slot) => {
                                                    if let Some(n) = app.net.as_ref() {
                                                        let name = crate::status::slot_name(slot);
                                                        if app.status_page.page() == 0 {
                                                            if let Some(Some(it)) =
                                                                n.world.equip.get(slot)
                                                            {
                                                                n.equip_unequip(slot as u32);
                                                                println!(
                                                                    "[net] 脱下 {}（{}）",
                                                                    it.name, name
                                                                );
                                                            } else {
                                                                println!(
                                                                    "[ui] 装备槽 {}（{}）是空的",
                                                                    slot + 1,
                                                                    name
                                                                );
                                                            }
                                                        } else {
                                                            let msg = match n.world.equip.get(slot)
                                                            {
                                                                Some(Some(it)) => format!(
                                                                    "装备槽 {}（{}）：{}",
                                                                    slot + 1,
                                                                    name,
                                                                    it.name
                                                                ),
                                                                _ => format!(
                                                                    "装备槽 {}（{}）是空的",
                                                                    slot + 1,
                                                                    name
                                                                ),
                                                            };
                                                            println!("[ui] {msg}");
                                                            if let Some(n) = app.net.as_mut() {
                                                                n.chat.push(
                                                                    trunc(&msg, 30),
                                                                    C_CHAT_SYS,
                                                                );
                                                            }
                                                        }
                                                    }
                                                }
                                                crate::status::Hit::None => {}
                                            }
                                            app.combat_target = None;
                                            app.move_target = None;
                                            app.press_at = None;
                                            app.held_move = None;
                                        }
                                    }
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
                                            ..crate::layout::BAG_CLOSE_Y
                                                + crate::layout::BAG_CLOSE_H)
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
                                    } else if let Some(slot) = crate::layout::bag_slot_at((lx, ly))
                                    {
                                        let idx = app.bag_page.page()
                                            * crate::layout::BAG_PAGE_SLOTS
                                            + slot;
                                        // 与上面那处**同一份规则**（`bagdrag`），不再各写一遍
                                        if let Some(n) = app.net.as_ref() {
                                            let act = {
                                                let cell = |i: usize| {
                                                    n.world.bag.get(i).and_then(|o| o.as_ref()).map(
                                                        |it| bagdrag::Cell {
                                                            make: it.make_index,
                                                            std_mode: it.std_mode,
                                                        },
                                                    )
                                                };
                                                app.drag.click_bag(
                                                    idx,
                                                    Instant::now(),
                                                    n.world.repair,
                                                    cell,
                                                )
                                            };
                                            apply_bag_act(act, &mut app.net);
                                        }
                                    }
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.held_move = None;
                                }
                            }
                            // ②″″ 商店窗：点行=**选中**、点箭头=翻页、点 OK=买选中的
                            //（原版口径，用户 2026-10-10 第 7 条）；点在窗里别走路
                            // ⚠️ 买/卖是**两个窗**：只有"买"模式才走商品列表窗
                            //（卖模式下面那个窗出的是放物品的槽）
                            if app.win.shop() {
                                if let Some(n) = app.net.as_ref() {
                                    if let Some(sh) = n.world.shop.as_ref().filter(|s| !s.is_sell())
                                    {
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
                                                        crate::widget::canvas_rect((
                                                            wx + p.0,
                                                            wy + p.1,
                                                            p.2,
                                                            p.3,
                                                        )),
                                                    );
                                                }
                                                crate::shop::Hit::Next => {
                                                    let p = crate::shop::NEXT_R;
                                                    app.ui_click.press(
                                                        Press::Shop(crate::shop::Hit::Next),
                                                        crate::widget::canvas_rect((
                                                            wx + p.0,
                                                            wy + p.1,
                                                            p.2,
                                                            p.3,
                                                        )),
                                                    );
                                                }
                                                crate::shop::Hit::Ok => {
                                                    let p = crate::shop::OK;
                                                    app.ui_click.press(
                                                        Press::Shop(crate::shop::Hit::Ok),
                                                        crate::widget::canvas_rect((
                                                            wx + p.0,
                                                            wy + p.1,
                                                            p.2,
                                                            p.3,
                                                        )),
                                                    );
                                                }
                                                crate::shop::Hit::Close => {
                                                    let p = crate::shop::CLOSE;
                                                    app.ui_click.press(
                                                        Press::Shop(crate::shop::Hit::Close),
                                                        crate::widget::canvas_rect((
                                                            wx + p.0,
                                                            wy + p.1,
                                                            p.2,
                                                            p.3,
                                                        )),
                                                    );
                                                }
                                                crate::shop::Hit::None => {}
                                            }
                                            app.combat_target = None;
                                            app.move_target = None;
                                            app.press_at = None;
                                            app.held_move = None;
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
                                                crate::widget::canvas_rect((
                                                    sx + p.0,
                                                    sy + p.1,
                                                    p.2,
                                                    p.3,
                                                )),
                                            );
                                        }
                                        // 圆槽：**手上拿着 ⇒ 放下；已经放着 ⇒ 再拿起来**
                                        //（用户 2026-10-10：物品放下后还可以再次拾起、放回包裹）
                                        crate::shop::SellHit::Slot => {
                                            let p = crate::shop::SELL_SLOT;
                                            app.ui_click.press(
                                                Press::Sell(crate::shop::SellHit::Slot),
                                                crate::widget::canvas_rect((
                                                    sx + p.0,
                                                    sy + p.1,
                                                    p.2,
                                                    p.3,
                                                )),
                                            );
                                        }
                                        crate::shop::SellHit::Ok => {
                                            let p = crate::shop::SELL_OK;
                                            app.ui_click.press(
                                                Press::Sell(crate::shop::SellHit::Ok),
                                                crate::widget::canvas_rect((
                                                    sx + p.0,
                                                    sy + p.1,
                                                    p.2,
                                                    p.3,
                                                )),
                                            );
                                        }
                                        crate::shop::SellHit::None => {}
                                    }
                                    app.combat_target = None;
                                    app.move_target = None;
                                    app.press_at = None;
                                    app.held_move = None;
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
                                    let hit = app
                                        .hover
                                        .filter(|id| n.world.npc_kind(*id))
                                        .or_else(|| n.world.npc_at(cell.0, cell.1));
                                    if let Some(id) = hit {
                                        n.npc_click(id);
                                        app.combat_target = None;
                                        app.move_target = None;
                                        app.press_at = None; // 松开时别再改判成"走一格"
                                        app.held_move = None;
                                        println!("[net] 点 NPC ActorId={id}（等 NpcSay）");
                                    }
                                }
                            }
                        }
                        MouseButton::Left => probe_at(x, y, app.cam, &draws, &tiles, app.layers),
                        _ => {}
                    }
                }
                // 松开：**停止"按住"那条重取目标的链**，但**不清走法目标**。
                //
                // ⚠️ 2026-10-09 改（用户第 7 条）：原来这里 `move_target = None` ⇒
                // **单击只走一步**（目标只在按住时才被一次次重取）。用户要的是
                // 「单击地图位置 ⇒ 跑动+走动到该位置；那格有障碍 ⇒ 就近停下」——
                // 于是目标留着，由下面的走法逻辑一直推到**到达**或**被拒**为止：
                //
                //   · 到达  ⇒ `next_move_step` 给不出下一步，自己就清了（见它的说明）；
                //   · 被挡  ⇒ 服务端回 `MoveRejected` ⇒ 那条路清目标 + 锁 1 秒
                //           （见下面 `world.move_fail` 那段，口径照原版 `ActionFailed`）。
                //
                // ⚠️ **不清** `combat_target`：原版锁住的目标是"打到它死/消失"为止
                //（`MouseTimerTimer`，`ClMain.pas:2962-2997`），不是"松手就取消"。
                Event::MouseButtonUp {
                    mouse_btn, x, y, ..
                } if app.mode == 2
                    && matches!(mouse_btn, MouseButton::Left | MouseButton::Right) =>
                {
                    // ① **OnClick**：左键按下 + 松开在**同一控件**才执行
                    //（规范见 `widget` 模块头的表：拖出控件再松开 / 右键 / 只有
                    // MouseDown ⇒ 都不触发）。右键不会走到这里（`release` 只对左键调）。
                    if mouse_btn == MouseButton::Left {
                        if let Some(k) = app.ui_click.release(crate::widget::canvas_pt((x, y))) {
                            match k {
                                Press::Hud(b) => match b {
                                    crate::layout::HudBtn::State => toggle_status(&mut app.win),
                                    crate::layout::HudBtn::Bag => {
                                        toggle_bag(&mut app.win, &mut app.bag_page)
                                    }
                                    crate::layout::HudBtn::Magic => {
                                        ui_say(app.net.as_mut(), "技能窗还没做（原版 F11）。")
                                    }
                                    crate::layout::HudBtn::Option => {
                                        ui_say(app.net.as_mut(), "选项窗还没做。")
                                    }
                                },
                                Press::Quick(b) => match b {
                                    crate::layout::QuickBtn::MiniMap => {
                                        toggle_minimap(&mut app.minimap_on)
                                    }
                                    crate::layout::QuickBtn::Trade => {
                                        ui_say(app.net.as_mut(), "玩家交易还没做。")
                                    }
                                    crate::layout::QuickBtn::Guild => {
                                        ui_say(app.net.as_mut(), "行会还没做。")
                                    }
                                    crate::layout::QuickBtn::Group => {
                                        ui_say(app.net.as_mut(), "组队还没做。")
                                    }
                                    crate::layout::QuickBtn::PlusAbil => {
                                        ui_say(app.net.as_mut(), "属性加点还没做。")
                                    }
                                    // 这两颗不直接生效 ⇒ 弹确认框
                                    crate::layout::QuickBtn::Logout => {
                                        app.confirm.ask(confirm::QuitAct::Logout)
                                    }
                                    crate::layout::QuickBtn::Exit => {
                                        app.confirm.ask(confirm::QuitAct::Exit)
                                    }
                                },
                                // 快捷栏：手上拿着 ⇒ 放进这格；空手 ⇒ 使用
                                Press::Belt(i) => {
                                    // 手上拿的是**背包里的东西** ⇒ 放进这一格。
                                    // （从腰带/装备槽拿来的还没接线 ⇒ `bag_index()` 为 None，
                                    //  那就落到下面"使用这一格"的老逻辑上。）
                                    if let Some(idx) = app.drag.held_slot() {
                                        if let Some(Some(it)) =
                                            app.net.as_ref().and_then(|n| n.world.bag.get(idx))
                                        {
                                            app.belt[i] = Some(crate::layout::BeltSlot {
                                                make_index: it.make_index,
                                                looks: it.looks,
                                            });
                                            println!("[ui] 快捷栏 {} = {}", i + 1, it.name);
                                        }
                                        // 放进快捷栏了 ⇒ 手上与卖窗槽都清掉
                                        app.drag.clear();
                                    } else if let Some(b) = app.belt[i] {
                                        belt_use(&mut app.net, &mut app.belt, i, b.make_index);
                                    }
                                }
                                Press::BagClose => {
                                    app.win.close_bag();
                                    println!("[ui] 背包窗关闭（点 X）");
                                }
                                Press::Status(crate::status::Hit::Close) => {
                                    app.win.close_status();
                                    println!("[ui] 状态窗关闭（点 X）");
                                }
                                Press::Status(crate::status::Hit::Arrow(d)) => {
                                    app.status_page.step(d, crate::status::PAGES);
                                    println!("[ui] 状态窗翻到第 {} 页", app.status_page.page());
                                }
                                Press::Status(_) => {}
                                Press::Shop(crate::shop::Hit::Prev) => {
                                    let n = app
                                        .net
                                        .as_ref()
                                        .and_then(|n| n.world.shop.as_ref())
                                        .map_or(0, |sh| sh.items.len());
                                    app.shop_ui.page_step(-1, n);
                                }
                                Press::Shop(crate::shop::Hit::Next) => {
                                    let n = app
                                        .net
                                        .as_ref()
                                        .and_then(|n| n.world.shop.as_ref())
                                        .map_or(0, |sh| sh.items.len());
                                    app.shop_ui.page_step(1, n);
                                }
                                Press::Shop(crate::shop::Hit::Ok) => {
                                    if let Some(sh) =
                                        app.net.as_ref().and_then(|n| n.world.shop.as_ref())
                                    {
                                        match app.shop_ui.sel().and_then(|s| sh.items.get(s)) {
                                            Some(it) => {
                                                let name = it.name.clone();
                                                let npc = sh.npc_id;
                                                if let Some(n) = app.net.as_ref() {
                                                    n.shop_buy(npc, &name, 1);
                                                }
                                                println!("[net] 买入 {}", name);
                                            }
                                            None => ui_say(app.net.as_mut(), "先点一件商品再点 OK"),
                                        }
                                    }
                                }
                                Press::Shop(crate::shop::Hit::Close) => {
                                    app.win.close_shop();
                                    app.shop_ui.close();
                                    println!("[ui] 商店窗关闭（素材关闭钮）");
                                }
                                Press::Shop(_) => {}
                                Press::Sell(crate::shop::SellHit::Close) => {
                                    app.win.close_sell();
                                    app.drag.clear();
                                    println!("[ui] 卖货窗关闭（同步收包裹）");
                                }
                                Press::Sell(crate::shop::SellHit::Slot) => {
                                    // 拿着东西点槽 = **放进去**；空手点槽 = **拿回手上**。
                                    // 两边都 `take()` 再按情况放回去 ⇒ 不存在"同时拿着又放着"。
                                    app.drag.click_slot();
                                }
                                Press::Sell(crate::shop::SellHit::Ok) => {
                                    // 认物**只认实例号**（D-75）：背包随时可能被服务端重排，
                                    // 槽里那件记的**下标**会漂 ⇒ 按 `make_index` 找它。
                                    let Some(mk) = app.drag.placed_make() else {
                                        continue;
                                    };
                                    let Some(n) = app.net.as_ref() else {
                                        continue;
                                    };
                                    let Some(sh) = n.world.shop.as_ref() else {
                                        continue;
                                    };
                                    let npc = sh.npc_id;
                                    let Some(it) =
                                        n.world.bag.iter().flatten().find(|it| it.make_index == mk)
                                    else {
                                        continue;
                                    };
                                    if it.make_index != 0 {
                                        let count = it.count.max(1);
                                        let mk2 = it.make_index;
                                        let name = it.name.clone();
                                        n.shop_sell(npc, mk2, count);
                                        println!("[net] 卖出 {} x{count}", name);
                                        // 真卖出去了才把它从槽里拿掉
                                        app.drag.take_placed();
                                    }
                                }
                                Press::Sell(_) => {}
                                Press::ConfirmOk => {
                                    if let Some(act) = app.confirm.confirm() {
                                        match act {
                                            confirm::QuitAct::Logout => {
                                                ui_say(app.net.as_mut(), "已注销。");
                                                app.net = None;
                                                app.mode = 1;
                                                app.belt = [None; crate::layout::BELT_SLOTS];
                                                app.win.close_all();
                                            }
                                            confirm::QuitAct::Exit => break 'run,
                                        }
                                    }
                                }
                                Press::ConfirmCancel => app.confirm.cancel(),
                                // 登录界面的按钮：不会走到这里（那是 `mode == 1` 的事，
                                // 见下面登录那条 `release`）。列出来只为穷尽匹配。
                                Press::Login(_) => {}
                                // 选角场景（那是 `mode == 4` 的事）⇒ 同上，只为穷尽匹配
                                Press::Select(_) | Press::NewChar(_) => {}
                            }
                            continue; // 按在界面上 ⇒ 不走世界的"松手"逻辑
                        }
                    }
                    app.held_move = None;
                    // ⚠️ 单击 = 走**一格**（用户 2026-10-09 第 3 条）：按下那一刻已经把
                    // "走到光标那格"当成目标了（`mouse_intent`），够快松手就把它换成
                    // **紧邻那一格** ⇒ 一步之后"到达"、目标自清。按住（> `CLICK_MS`）
                    // 仍是一路走过去。
                    if mouse_btn == MouseButton::Left {
                        if let Some(t) = app.press_at.take() {
                            if t.elapsed() < Duration::from_millis(input::CLICK_MS) {
                                if let Some(n) = app.net.as_mut() {
                                    if n.world.in_world() {
                                        let (ct, mt) = if app.butch_target.is_some() {
                                            (None, None) // 挖肉中 ⇒ 松手也不走一格
                                        } else {
                                            input::click_step(
                                                &n.world,
                                                n.world.self_pos,
                                                app.press_cell,
                                                false,
                                                app.hover,
                                            )
                                        };
                                        if ct.is_some() || mt.is_some() {
                                            app.combat_target = ct;
                                            app.move_target = mt;
                                            app.attack_at = Instant::now();
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 4 => {
                    // 这两屏的命中测试活在**设计空间**（800×600）里 ⇒ 鼠标先除回去
                    let (x, y) = {
                        let p = ui::ui_inv_pt((x, y));
                        (p.0 .0, p.1 .0)
                    };
                    if let (Some(dir), Some(scene)) =
                        (asset_dir.as_ref(), app.select_scene.as_mut())
                    {
                        // 弹窗的几何要给进去：它那颗 [确定] 也走"按下与抬起同一颗"
                        let msg = select::msgbox_geom(&mut ui, dir, UI_WIN);
                        if let Some(l) =
                            mir2_core::select_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i))
                        {
                            scene.on_down((x, y), &l, msg, &mut app.ui_click);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 4 => {
                    let canvas_p = crate::widget::canvas_pt((x, y));
                    let act = match (asset_dir.as_ref(), app.select_scene.as_mut()) {
                        (Some(dir), Some(scene)) => {
                            // OnClick 交给引擎判（拖出去松开就不算）⇒ 这里只执行
                            let key = app.ui_click.release(canvas_p);
                            match mir2_core::select_ui::Layout::build(UI_WIN, |c, i| {
                                ui.size(dir, c, i)
                            }) {
                                Some(l) => match key {
                                    Some(Press::Select(h)) => scene.click_select(h, &l),
                                    Some(Press::NewChar(dh)) => scene.click_newchar(dh),
                                    // 没按在任何控件上 ⇒ 弹窗（模态）就此关掉
                                    _ => scene.click_outside(),
                                },
                                None => select::Action::None,
                            }
                        }
                        _ => select::Action::None,
                    };
                    if do_select_action(act, &mut app.net, &mut app.select_scene, &sound, &sounds)?
                    {
                        break 'run;
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 1 => {
                    // 版式每帧现算（尺寸来自容器头，不解压 ⇒ 很便宜），用于命中判定。
                    // ⚠️ 版式在**设计空间**（800×600）里 ⇒ 鼠标先除回去（见 `UI_WIN`）
                    let (x, y) = {
                        let p = ui::ui_inv_pt((x, y));
                        (p.0 .0, p.1 .0)
                    };
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            app.login.on_down((x, y), &l, &mut app.ui_click);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 1 => {
                    let canvas_p = crate::widget::canvas_pt((x, y));
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            // OnClick 交给引擎判（拖出去松开就不算）⇒ 这里只映射动作
                            let act = match app.ui_click.release(canvas_p) {
                                Some(Press::Login(b)) => app.login.click(b, &l),
                                _ => login::Action::None,
                            };
                            // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                            if act != login::Action::None {
                                sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                            }
                            match act {
                                login::Action::Submit => {
                                    submit_login(&mut app.login, &mut app.net, &mut status)
                                }
                                login::Action::SubmitSignup => {
                                    submit_signup(&mut app.login, &mut app.net, &mut status)
                                }
                                login::Action::Quit => break 'run,
                                login::Action::Dismiss => {}
                                login::Action::NewAccount => {
                                    status = "NEW ACCOUNT".into();
                                }
                                login::Action::CancelSignup => status = String::new(),
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::None => {}
                            }
                        }
                    }
                }
                Event::TextInput { text: t, .. } if app.mode == 1 => app.login.on_text(&t),
                // 选角界面的**建角对话框**也吃文字（`Select::on_text` 自己判断对话框开没开、
                // 焦点在不在姓名上）—— 少了这条，姓名框打字没反应（原版是 `TEdit` 收键）。
                Event::TextInput { text: t, .. } if app.mode == 4 => {
                    if let Some(s) = app.select_scene.as_mut() {
                        s.on_text(&t);
                    }
                }
                _ => {}
            }
        }

        // 联网：把网络线程收到的东西推进状态机（**每帧一次**，永不阻塞）。
        if let Some(n) = &mut app.net {
            n.pump();
            // 登录界面与网络状态互相照应。三件事：
            //   ① 失败 ⇒ 弹窗（原版也是 `DMessageDlg`），并把"登录中"解掉；
            //   ② 进世界 ⇒ 开始播开门动画（原版 `IntroScn.pas:907-914`），播完切地图；
            //   ③ 成功拿到的会话号存下来 —— 之后重连走 `Reconnect`，不必再输口令。
            // ⚠️ **取走**失败原因，而不是每帧读 `entrance.failed()` / `n.fail`：
            // 两者都是粘性状态（`stage == Failed` 会一直挂着）⇒ 每帧读的话，用户点了[确定]
            // 之后下一帧它又弹回来，看着就是"弹窗关不掉"（用户 2026-10-08 报的）。
            // 弹窗正开着就不取（那条失败会等关掉之后再弹）—— 免得把两条原因挤掉一条。
            if app.login.error.is_none() {
                if let Some(why) = n.take_fail() {
                    // 失败：弹提示 + 焦点回账号框（原版 `PassWdFail`，`IntroScn.pas:613-619`）
                    app.login.fail(why);
                }
            }
            // 登录**通过**（清单在手）⇒ 原版 `OpenLoginDoor`：藏小窗 + 开门 + 开门声。
            //
            // ⚠️ 触发点是"登录通过"，**不是"进世界"**：原版由登录成功那条路调
            // `OpenLoginDoor`（`IntroScn.pas:795-801`），门放完才 `ChangeScene(stSelectChr)`
            //（`IntroScn.pas:838-847`）。早先挂在 `in_world()` 上 ⇒ 门排在选角与进图之后，
            // 而且门画在登录框背后 ⇒ 看起来"压根没有开门动画"（用户报的就是这个）。
            // `login.busy` 的含义正是"这次是口令登录"（`submit_login` 设的）。
            if app.login.busy && n.entrance.stage() == &mir2_core::entrance::Stage::AwaitList {
                app.login.opened_at = Some(Instant::now());
                app.login.busy = false;
                app.select_scene = None;
                app.mode = 1; // 开门动画在登录屏上播（见 `login::draw` 的说明）
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 兜底：**认领会话**那条路（没有口令登录这一步）落到世界里时才补一次开门。
            if n.entrance.in_world() && app.login.opened_at.is_none() {
                app.login.opened_at = Some(Instant::now());
                app.login.busy = false;
                app.select_scene = None;
                app.mode = 1;
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 登录成功后会停在 `AwaitPick`（`set_manual_pick`）⇒ 切到选角场景。
            //
            // ⚠️ "停在等你选"是**状态机说的**，不是我们猜的时机：角色列表就在它手上
            //（`entrance.characters()`），界面只负责显示与选择。
            // 每帧"网络 → 画面"的决策：**纯函数**（见 `plan` 的说明 —— 决策必须放在
            // 能单测的地方，主循环只负责执行）。判据全是状态机说的事实，没有一处是"猜时机"。
            let chars: Vec<select::CharEntry> = n
                .entrance
                .characters()
                .iter()
                .map(select::CharEntry::from_summary)
                .collect();
            let p = plan(
                app.mode,
                n.entrance.stage() == &mir2_core::entrance::Stage::AwaitPick,
                n.entrance.in_world(),
                list_changed(app.select_scene.as_ref(), &chars),
                // "门挡着" = 开过门（`opened_at` 有值）但还没放完
                app.login.opened_at.is_some() && !app.login.door_done(),
            );
            // ⚠️ **只在列表真的变了**的时候才重建场景。这段在"门还在放"的那三秒里每帧都会
            // 走到 —— 早先每帧重建 + 每帧 println，表现是**日志刷屏**（用户报过），
            // 而且把用户已经移好的选中位置**每帧抹回第 0 个**。
            // 列表真的会变的场合只有两种：建/删角回执后的重拉、重连回落。
            if p.rebuild_select {
                println!("[net] 角色列表：{} 个", chars.len());
                let mut scene = select::Select::new(chars);
                // 名字规则**跟着角色列表一起来**（服务端下发 ⇒ 单一来源在那边，D-84）
                if let Some(r) = n.entrance.name_rule.clone() {
                    scene.set_name_rule(r);
                }
                // `MIR2_CHAR=<id>` 指定初选（手动模式下状态机不看它了，落到界面上）
                if let Ok(v) = std::env::var("MIR2_CHAR") {
                    match v.parse::<u64>() {
                        Ok(id) => scene.pick_id(id),
                        Err(_) => println!("[net] MIR2_CHAR={v} 不是整数，忽略"),
                    }
                }
                app.select_scene = Some(scene);
            }
            // 门还在放就先不切：原版顺序是"门放完 → `ChangeScene(stSelectChr)`"
            //（`IntroScn.pas:838-847`），一上来就切会把门当场掐掉（用户看到的就是"没有开门动画"）。
            // 门放完那一刻由主循环里 `mode == 1` 那条分支接手。
            if p.enter_select {
                app.mode = 4;
            }
            // 选角通过 ⇒ 换到游戏主场景（原版在这里是 `ChangeScene(stPlay)`）
            if p.enter_play {
                app.mode = 2;
                // 选角场景用不上了（它的槽动画也不必再跑）
                app.select_scene = None;
            }
            // 选角被拒（例如租约被占）⇒ 弹给用户换一个（状态机会退回 `AwaitPick`）。
            if let Some(why) = n.entrance.take_pick_error() {
                if let Some(scene) = app.select_scene.as_mut() {
                    scene.say(why);
                }
            }
            // 在选角场景里出了**致命**错（服务端断开…）⇒ 回登录界面，
            // 用那套已有的弹窗说清楚（否则用户会在选角界面上干等）。
            if app.mode == 4 && n.entrance.failed().is_some() {
                app.mode = 1;
            }
            if let Some(tok) = n.entrance.session_token() {
                n.session = tok;
            }
        }
        // 鼠标走路：按住时**一步步**朝目标格走。
        //
        // 原版是"按住每 ≥300ms 重新触发一次 `_DXDrawMouseDown`、松开清目标"
        //（`ClMain.pas:2115-2116 / 2384-2389`），步频本身受服务端节流限制 ⇒
        // 我们按 `WALK_MS`/`RUN_MS` 发，到了目标格就停。
        //
        // ⚠️ "这一步走还是跑"由 [`next_move_step`] 定：**距离 < 2 就不许跑** ——
        // 跑步一次跨 2 格，奇数距离时不许跑才不会"跨过去再跨回来"（用户报的"左右乱换"）。

        // **按住鼠标不放 ⇒ 一直走/跑**：每 [`MOUSE_REPEAT_MS`] 拿**当前鼠标那一格**重新定目标。
        //
        // 照原版 `ClMain.pas:2678-2679`（`DXDrawMouseMove` 里"按住且距上次 >300ms ⇒
        // 重跑一遍 `_DXDrawMouseDown`"）。少了这一步，目标格就是按下那一刻定死的 ⇒
        // 走到那儿就停（用户 2026-10-08 报的"按住只能走数次"）。
        //
        // ⚠️ 它必须与"人物不动、地图卷动"（[`follow_cam`]）配套：镜头跟着人走，鼠标**屏幕**
        // 位置不变时它对应的**格子**会往前跑 ⇒ 按住不放就是**一直走下去**（原版的"按住跑直线"）。
        // 镜头不卷或者目标不重取，两样单独都做不出这个手感。
        // ⚠️ 挖肉中 ⇒ 按住/移动鼠标都**不许**变成走路（用户第 1 条）
        if app.mode == 2
            && app.held_move.is_some()
            && app.butch_target.is_none()
            && app.retarget_at.elapsed() >= Duration::from_millis(MOUSE_REPEAT_MS)
        {
            app.retarget_at = Instant::now();
            if let Some(n) = app.net.as_ref().filter(|n| n.world.in_world()) {
                let cell = screen_to_cell(app.cam, app.mouse.0, app.mouse.1);
                let run = app.held_move == Some(MouseButton::Right);
                // ⚠️ 用 `mouse_repeat` 而不是 `mouse_intent`：按住时**不能**因为
                // "光标不在怪身上了"就把锁住的怪丢掉（镜头跟着人走，光标一定会滑开，
                // 否则按住点怪会半路变成走路）。见 `mouse_repeat` 的说明。
                let (ct, mt) = mouse_repeat(&n.world, cell, run, app.combat_target);
                app.combat_target = ct;
                app.move_target = mt;
            }
        }

        // **锁定的攻击目标**：够近就出手、不够近就靠近。
        //
        // 照原版：`_DXDrawMouseDown` 点到怪就 `g_TargetCret := target`（`ClMain.pas:2866`），
        // 之后 `MouseTimerTimer`（`ClMain.pas:2962-2997`）**每帧**对那个目标调 `AttackTarget`
        // —— `AttackTarget`（`:2691-2743`）自己判"相邻就砍、不够近就朝它旁边那格走/跑"。
        // 也就是原版**没有"按住才打"这一说**：锁住了就一直打，直到它死掉或消失。
        if app.mode == 2 && !app.net.as_ref().is_some_and(|n| n.world.self_dead) {
            if let Some(id) = app.combat_target {
                let step = app
                    .net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .and_then(|n| n.world.combat_step(id));
                // 手上的挥砍还没播完？原版 `CanNextAction`（`IsIdle`）——"砍完再迈步"，
                // 也是用户 2026-10-09 补充的第 2 条后半句（"攻击完后再次判断是不是要走/跑"）。
                let swing_busy = app
                    .net
                    .as_ref()
                    .and_then(|n| n.anims.get(&n.world.self_id))
                    .is_some_and(|a| a.attack_busy(app.started));
                match step {
                    // 死了 / 消失了（尸体被清）/ 自己掉线了 ⇒ 解除锁定
                    None => {
                        println!("[net] 目标不在了，解除锁定");
                        app.combat_target = None;
                        app.move_target = None;
                    }
                    Some(mir2_core::world::CombatStep::Attack) => {
                        // ⚠️ **这一步走完再出手** —— 原版 `CanNextAction` = `g_MySelf.IsIdle`
                        //（`Actor.pas:1722-1736`：`m_nCurrentAction <> 0` 就"不空"，不许发下一个动作），
                        // 而 `ActionFinished` 看的是**动画有没有播到 `m_nEndFrame`**
                        // ⇒ 走路那段动作没播完，原版**发不出攻击**。
                        //
                        // 为什么我们特别需要它：我们不做移动预测，位置是**服务端权威**的 ——
                        // 服务端确认"到了"时，画面上的补间才刚起步 ⇒ `combat_step` 已经说"相邻、
                        // 该出手"，于是挥砍动画在人还没走到时就播了（用户 2026-10-08 报的）。
                        let stepping = app
                            .net
                            .as_ref()
                            .and_then(|n| n.anims.get(&n.world.self_id))
                            .is_some_and(|a| a.moving(app.started));
                        // 节拍按**自己的等级**算（原版 `CanNextHit`：`1020 − min(370, level*14)`）
                        let level = app
                            .net
                            .as_ref()
                            .and_then(|n| n.world.ability.as_ref().map(|a| a.level))
                            .unwrap_or(1)
                            .max(1);
                        if can_attack(stepping || swing_busy, app.attack_at.elapsed(), level) {
                            if let Some(n) = app.net.as_ref() {
                                let _ = n.attack_target(id);
                                swing_sfx(n, &sound, &sounds);
                            }
                            app.attack_at = Instant::now();
                        }
                    }
                    // 够不着：复用鼠标走路那条路（步频/节流/"到了就停"都在下面那段里）
                    //
                    // ⚠️ 手上的挥砍没播完就先站着（用户第 2 条："即使是在'追打'时，
                    // 也应在移动结束后再补攻击动作，攻击完后再次判断是不是要走/跑"）。
                    // 不挡的话就是"边走边砍/同手同脚"。
                    Some(mir2_core::world::CombatStep::Approach { x, y, run }) => {
                        // ⚠️ 绕障排队期间**别覆写**：`detour` 排好的那步侧移还没走，
                        // 这儿每帧重算会把它冲掉 ⇒ 表现就是"顶着怪一遍遍推"
                        //（用户 2026-10-10 第 6 条的另一半）。
                        if !swing_busy && app.detour.is_none() {
                            app.move_target = Some((x, y, run));
                        }
                    }
                }
            }
        }

        // 服务端**拒了这一步**（`MoveRejected`）⇒ 照原版 `ActionFailed`
        //（`ClMain.pas:4005-4012`：清走法目标 + 锁 1 秒不许再发）。
        //
        // ⚠️ 少了这个反应，撞墙时客户端会朝同一个方向每 `WALK_MS` 发一次、每次都被拒，
        // 人物就"卡在那儿不动"，而且服务端日志刷满 —— "跑不到怪身边"多半有它一份。
        // 计数**变没变**就是"刚刚被拒了一次"的信号（见 `World::move_fail`）。
        let fails = app.net.as_ref().map_or(0, |n| n.world.move_fail);
        if fails != app.last_move_fail {
            let reason = app.net.as_ref().map_or(0, |n| n.world.move_fail_reason);
            app.last_move_fail = fails;
            app.move_at = Instant::now();
            if reason == 3 && app.move_target.is_some() && app.detour != Some(1) {
                // **撞墙先绕一步**（原版 `ClMain.pas:1886-1923`：直走那格过不去时
                // 先试 `PrivDir`（逆时针 45°），再试 `NextDir`（顺时针 45°），
                // 能走就走），而不是像原来那样"清目标 + 锁 1 秒"——
                // 那正是用户 2026-10-09 第 4 条说的"点哪走哪"手感问题。
                // 两次都撞不上才认输（下面那条），并且**不加 1 秒锁**（只等一个正常步频）。
                app.detour = Some(app.detour.map_or(0, |k| k + 1));
                println!(
                    "[move] 前方被挡（reason=3）⇒ 侧移 45° 试第 {} 次",
                    app.detour.unwrap_or(1) + 1
                );
            } else {
                // 越界(2)/超速(1)，或绕障两次都不行 ⇒ 照原版 `ActionFailed`：
                // 清走法目标 + 锁 [`MOVE_FAIL_LOCK_MS`]（"就近停下"）。
                // ⚠️ **追打目标也要一起放**（用户 2026-10-10 第 6 条）：追打那块
                // 每帧都会把 move_target 重算回去 —— 只清 move_target 的话，
                // 下一帧又凑近、又被拒、锁完接着顶，永远停不下来。
                // 目标格被怪占着凑不近 ⇒ 就近停下，等玩家重新点。
                app.move_target = None;
                app.combat_target = None;
                app.detour = None;
                app.move_block_until = Instant::now() + Duration::from_millis(MOVE_FAIL_LOCK_MS);
                println!(
                    "[move] 服务端拒绝了这一步（reason={reason}，1=超速 2=越界 3=阻挡）\
                     —— 清走法目标 + 锁 {MOVE_FAIL_LOCK_MS}ms"
                );
            }
        }

        // 位置一变 ⇒ 上一步走成了 ⇒ 绕障状态清掉，回到"朝目标直走"
        if let Some(n) = app.net.as_ref() {
            let p = n.world.self_pos;
            if p != app.last_self_pos {
                app.last_self_pos = p;
                app.detour = None;
            }
        }

        if app.mode == 2 {
            if let Some((tx, ty, run)) = app.move_target {
                let here = app
                    .net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .map(|n| (n.world.self_pos.0, n.world.self_pos.1));
                match here {
                    // 没进世界（掉线/还没到）⇒ 目标作废，别攒着一堆移动
                    None => app.move_target = None,
                    Some(pos) => match input::route_step(app.map.as_ref(), pos, (tx, ty), run) {
                        // 已经站在目标格上 ⇒ 收工（原版到点也停）
                        None => app.move_target = None,
                        Some((dir, step_run)) => {
                            // 绕障：把这一步转 45°（先逆后顺），并且**用走的**
                            //（侧移一小步就够，跑一次 2 格反而更容易顶死在障碍上）。
                            let (dir, step_run) = match app.detour {
                                Some(0) => (turn45(dir, -1), false),
                                Some(1) => (turn45(dir, 1), false),
                                _ => (dir, step_run),
                            };
                            let gap = if step_run { RUN_MS } else { WALK_MS };
                            // 被拒后的锁还没到期 ⇒ 先别发（原版 `IsUnLockAction`，
                            // `ClMain.pas:4014-4021`：锁着就一律不许动）
                            if app.move_block_until <= Instant::now()
                                && app.move_at.elapsed() >= Duration::from_millis(gap)
                            {
                                move_if_online(&app.net, dir, step_run);
                                app.move_at = Instant::now();
                            }
                        }
                    },
                }
            }
        }

        // 建号回执（D-32）：**不自动登录**（与原版一致 —— 它也只是弹个提示，
        // `ClMain.pas:3684-3691`）。提示用登录界面那套模态框说，面板切回登录；
        // 那条"只为建号用过的连接"顺手收尾：登录会另开一条（nonce 要重新握手）。
        let signup = app.net.as_mut().and_then(|n| n.entrance.take_signup_msg());
        if let Some((ok, msg)) = signup {
            app.login.leave_signup();
            app.login.busy = false;
            app.net = None;
            status = if ok {
                "ACCOUNT CREATED - PLEASE SIGN IN".into()
            } else {
                "SIGN UP FAILED".into()
            };
            app.login.fail(msg.clone());
            println!("[login] 建号{}：{msg}", if ok { "成功" } else { "失败" });
        }
        // 网络侧排出来的音效（挨打 / 死亡）：`pump` 在 `Net` 里，拿不到音频设备。
        if let Some(n) = app.net.as_mut() {
            for s in n.take_sfx() {
                sfx(&sound, &sounds, s);
            }
            if n.take_gameover() {
                // 自己死亡 ⇒ game over 音乐（原版 `Actor.pas:2373-2374`）
                bgm(&sound, &sounds, mir2_core::sound::BGM_GAMEOVER);
            }
        }
        // 选角场景排出来的音效（选中一个槽 ⇒ 解冻声 `101`，`IntroScn.pas:1170`）
        if let Some(scene) = app.select_scene.as_mut() {
            for s in scene.take_sfx() {
                sfx(&sound, &sounds, s);
            }
        }
        // 场景 BGM：登录（`IntroScn.pas:518`）与选角（`:1152`）各一首**循环**。
        // 进图音乐 `Music/<地图音乐号>.mp3` **暂时没有声音**（协议没有那个字段、
        // 手上也没有 mp3）⇒ 进地图就停掉上一首，而不是放错一首。
        // 自己死了就别停：那时该响的是 game over 那首（`Actor.pas:2373-2374`）。
        if app.mode == 4 {
            bgm(&sound, &sounds, mir2_core::sound::BGM_SELECT);
        } else if app.mode == 1 {
            // ⚠️ 开门动画期间**继续放**登录曲（原版 `PlayBGM(bmg_intro)` 一放放到换场景，
            // `IntroScn.pas:518`；门放完 `ChangeScene(stSelectChr)` 才换成选角曲）。
            // 早先这里多了个 `opened_at.is_none()` ⇒ 门一开就静音三秒（踩过）。
            bgm(&sound, &sounds, mir2_core::sound::BGM_LOGIN);
        } else if app.mode == 2
            && !app.net.as_ref().is_some_and(|n| n.world.self_dead)
            && sound.stop_bgm()
        {
            println!("[audio] BGM 停（进图音乐这条路还没接：手上没有 mp3、协议里也没有音乐号）");
        }
        // 怪物"正常声"：鸡叫/鹿鸣…（用户 2026-10-09 第 4 条）。
        // 官方规则与我们的近似写法见 `sfx::monster_ambient` 的说明。
        if app.mode == 2 {
            if let Some(n) = app.net.as_ref() {
                monster_ambient(
                    n,
                    &sound,
                    &sounds,
                    Instant::now(),
                    &mut app.sfx_rng,
                    &mut app.last_monster_sfx,
                );
            }
        }
        // 脚步：原版在走路动画的**帧 1 / 帧 4** 各响一次（`Actor.pas:2659-2660`），
        // 音色按**自己脚下那一格**定（原版把坐标对齐到偶数格，`Actor.pas:2146-2147`）。
        match (app.net.as_ref(), app.map.as_ref()) {
            (Some(n), Some(m)) if n.world.in_world() => match n.self_walk_frame(app.started) {
                Some((frame, running)) => {
                    if let Some(second_foot) = footstep_of(frame, app.last_foot_frame) {
                        let (mx, my) = (n.world.self_pos.0, n.world.self_pos.1);
                        let cell = m.at((mx.max(0) / 2 * 2) as usize, (my.max(0) / 2 * 2) as usize);
                        if let Some(c) = cell {
                            let t =
                                mir2_core::sound::terrain(c.bk_img, c.area, c.mid_img, c.fr_img);
                            sfx(
                                &sound,
                                &sounds,
                                mir2_core::sound::footstep(t, running, second_foot),
                            );
                        }
                    }
                    app.last_foot_frame = Some(frame);
                }
                None => app.last_foot_frame = None,
            },
            _ => app.last_foot_frame = None,
        }

        // 进了世界就让相机跟着自己（离线时保持手动镜头）。
        //
        // ⚠️ 跟的是**渲染位置**（补间后的浮点格），不是服务端那一格 —— 见 [`follow_cam`]。
        // 于是"人在屏幕中间不动、地图往前卷"，而不是"人在视口里蹭、镜头一格一跳"。
        if let Some(render) = app.net.as_ref().and_then(|n| n.self_render(app.started)) {
            app.cam = follow_cam(render);
        }

        // 地图镜头夹在合理范围内（允许露出边缘一格）
        if let Some(m) = &app.map {
            let max_x = (m.width as i32 - (WIN_W as i32 / UNIT_X) + 2).max(0) as f32;
            let max_y = (m.height as i32 - (VIEW_H as i32 / UNIT_Y) + 2).max(0) as f32;
            app.cam.0 = app.cam.0.clamp(-2.0, max_x);
            app.cam.1 = app.cam.1.clamp(-2.0, max_y);
        }

        update::tick(&mut app);

        // 世界侧持续状态推进（按住挖、到位捡取、对话滚动归零）—— 不画、可独立验证。
        if !update::advance_world(&mut app) {
            continue;
        }

        // 前景动画的节拍：官方 `m_nAniCount` **每 50 ms 加一**（`PlayScn.pas:963`，
        // 固定定时器、与帧率无关）。所以这里按**真实时间**算，而不是每帧 +1 ——
        // 否则灯会随机器性能忽快忽慢。
        render::draw_frame(
            &mut app,
            &mut canvas,
            &tex_creator,
            &mut ui,
            &mut ui_texts,
            &mut names,
            &mut libs,
            &mut tiles,
            &mut draws,
            &mut sprites,
            &asset_dir,
            archive.as_ref(),
            &cursor_arrow,
            &cursor_cross,
            &status,
            &mut loaded,
            &mut sprite_tex,
            lib_idx,
            &mut img_idx,
        )?;

        // 底部那条**开发用**提示条撤了：现在那儿是 HUD 操作面板（字会压在按钮上）。
        // 按键提示改在**进世界时推一行到聊天区**（玩家看得到的那处）。
        if !app.hint_pushed && app.mode == 2 {
            if let Some(n) = app.net.as_mut() {
                if n.entered_once {
                    app.hint_pushed = true;
                    // 外观诊断（用户 2026-10-09 第 2 条"木剑图不对"）：把自己的外观字节打出来。
                    // `weapon` = 服务端算的 **`Shape*2+性别`**（`ObjBase.pas:20018`）
                    // ⇒ 取图 = `Weapon.wzl[600 * weapon]`（每把武器占男/女两块）。
                    // 手上是木剑（`Shape=1`、男）→ `weapon=2`；若看到 `1`，那是**空块**
                    //（`Shape` 从 1 起），若看到 `4`，那是铁剑。
                    if let Some(f) = n.world.self_feature.as_ref() {
                        println!(
                            "[look] 自己的外观：dress={} weapon={} hair={} race_img={}",
                            f.dress, f.weapon, f.hair, f.race_img
                        );
                    }
                    n.chat.push(trunc(hint_text(2), 30), C_CHAT_SYS);
                    println!("[hud] 按键提示已进聊天区：{}", trunc(hint_text(2), 30));
                }
            }
        }

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

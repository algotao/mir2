//! 主循环执行器：SDL 事件分派、运行态更新与 `render::draw_frame` 编排。
//!
//! `main.rs` 负责创建平台资源；资源建好后交给这里运行。长期可继续把事件处理
//! 按输入域拆开，但所有可变会话状态已经归到 `AppState`。

use std::collections::HashMap;

use sdl3::keyboard::Keycode;

use crate::*;

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
    // 商店/卖货窗专用（比界面字小 2 号，用户 2026-10-12：购买窗字过大）
    let mut shop_texts = font::TextCache::new(mir2_core::text::SHOP_PX);

    // 素材浏览器的独立数据与 GPU 纹理（F3）
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;
    // hover / 系统光标 / 懒素材几何是 renderer 的 view cache，不属于游戏 AppState。
    let mut frame_state = render::FrameState::default();
    // 界面开发器的放大镜：放大时先把整帧画到这张纹理（按需建一次）。
    let mut zoom_tex: Option<Texture<'_>> = None;

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
        app.scene = crate::app_state::Scene::World;
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
                    if keyboard::handle_key_down(
                        &mut app,
                        keycode,
                        keymod,
                        &sound,
                        &sounds,
                        &draws,
                        &tiles,
                        archive.as_ref(),
                    )? {
                        break 'run;
                    }
                }
                // 松开 Alt ⇒ 挖肉的修饰键失效（SDL 的鼠标事件不带 keymod ⇒ 自己记）
                Event::KeyUp { keycode, .. } => {
                    if matches!(keycode, Some(Keycode::LAlt) | Some(Keycode::RAlt)) {
                        app.world_control.alt_down = false;
                        app.world_control.butch_target = None; // 按住才持续挖；松手就停
                    }
                }
                // 鼠标位置（已在循环头换算成界面的 800×600 空间）
                Event::MouseMotion { x, y, .. } => {
                    app.mouse = (x, y);
                    app.ui_click.set_mouse(crate::widget::canvas_pt(app.mouse));
                    // 界面开发器：拖动中（D-134）。屏幕坐标 → 逻辑坐标（放大时反算）
                    let logic = app.layout_edit.to_logic((x, y));
                    app.layout_edit.mouse_move(logic);
                }
                Event::MouseWheel { y, .. } if app.scene == crate::app_state::Scene::World => {
                    world_input::handle_mouse_wheel(&mut app, y)
                }
                // 鼠标移动：**在世界里 ⇒ 左键走 / 右键跑**（照原版 `ClMain.pas:2246-2352`：
                // 左键 = 走，右键 = 跑；方向由鼠标相对角色的方位定，见 `dir_to`）。
                // 离线看地图（没进世界）时左键仍是那个诊断探针 —— 它是开发用的，
                // 别和"走路"抢同一个键。
                Event::MouseButtonDown {
                    mouse_btn, x, y, ..
                } if app.scene == crate::app_state::Scene::World => {
                    world_input::handle_world_mouse_down(
                        &mut app,
                        mouse_btn,
                        x,
                        y,
                        &mut ui,
                        &asset_dir,
                        &mut ui_texts,
                        &draws,
                        &tiles,
                        frame_state.hover,
                    );
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
                } if app.scene == crate::app_state::Scene::World
                    && matches!(mouse_btn, MouseButton::Left | MouseButton::Right) =>
                {
                    match world_input::handle_world_mouse_up(
                        &mut app,
                        mouse_btn,
                        x,
                        y,
                        frame_state.hover,
                    ) {
                        world_input::MouseUpOutcome::Quit => break 'run,
                        world_input::MouseUpOutcome::Consumed => continue,
                        world_input::MouseUpOutcome::Continue => {}
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.scene == crate::app_state::Scene::Select => {
                    scene_input::select_down(&mut app, &mut ui, &asset_dir, (x, y));
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.scene == crate::app_state::Scene::Select => {
                    let act = scene_input::select_up(&mut app, &mut ui, &asset_dir, (x, y));
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
                } if app.scene == crate::app_state::Scene::Login => {
                    scene_input::login_down(&mut app, &mut ui, &asset_dir, (x, y));
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.scene == crate::app_state::Scene::Login => {
                    let act = scene_input::login_up(&mut app, &mut ui, &asset_dir, (x, y));
                    // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                    if act != login::Action::None {
                        sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                    }
                    match act {
                        login::Action::Submit => {
                            submit_login(&mut app.login, &mut app.net, &mut app.status)
                        }
                        login::Action::SubmitSignup => {
                            submit_signup(&mut app.login, &mut app.net, &mut app.status)
                        }
                        login::Action::Quit => break 'run,
                        login::Action::Dismiss => {}
                        login::Action::NewAccount => app.status = "NEW ACCOUNT".into(),
                        login::Action::CancelSignup => app.status = String::new(),
                        login::Action::ChangePassword => {
                            app.status = "CHANGE PASSWORD: NOT WIRED YET".into();
                        }
                        login::Action::None => {}
                    }
                }
                Event::TextInput { text: t, .. } if app.scene == crate::app_state::Scene::Login => {
                    app.login.on_text(&t)
                }
                // 选角界面的**建角对话框**也吃文字（`Select::on_text` 自己判断对话框开没开、
                // 焦点在不在姓名上）—— 少了这条，姓名框打字没反应（原版是 `TEdit` 收键）。
                Event::TextInput { text: t, .. }
                    if app.scene == crate::app_state::Scene::Select =>
                {
                    if let Some(s) = app.select_scene.as_mut() {
                        s.on_text(&t);
                    }
                }
                _ => {}
            }
        }

        network_tick::pump(&mut app, &sound, &sounds);

        if !simulation::tick(&mut app, &sound, &sounds) {
            continue;
        }

        // 前景动画的节拍：官方 `m_nAniCount` **每 50 ms 加一**（`PlayScn.pas:963`，
        // 固定定时器、与帧率无关）。所以这里按**真实时间**算，而不是每帧 +1 ——
        // 否则灯会随机器性能忽快忽慢。
        // **界面开发器的放大镜**（D-134）：放大时先把整帧 1:1 画进一张纹理，
        // 再把这张纹理按倍数贴回屏幕（以"缩放那一刻的鼠标位置"为中心）。
        //
        // 为什么不直接 `canvas.set_scale`：那是以**原点**为中心放大，拖不到鼠标那里；
        // 而 SDL 的视口不支持负偏移 ⇒ 只能走"渲染到纹理 + copy 到算好的矩形"。
        let zoom = app.layout_edit.zoom();
        let image = if app.layout_edit.on && zoom > 1.0 && app.scene == app_state::Scene::World {
            let tex = zoom_tex.get_or_insert_with(|| {
                tex_creator
                    .create_texture_target(
                        tex_creator.default_pixel_format(),
                        window::WIN_W,
                        window::WIN_H,
                    )
                    .expect("放大镜的目标纹理")
            });
            let mut drawn = None;
            canvas
                .with_texture_canvas(tex, |c| {
                    drawn = render::draw_frame(
                        &app,
                        &mut frame_state,
                        c,
                        &tex_creator,
                        &mut ui,
                        &mut ui_texts,
                        &mut names,
                        &mut shop_texts,
                        &mut libs,
                        &mut tiles,
                        &mut draws,
                        &mut sprites,
                        &asset_dir,
                        archive.as_ref(),
                        &cursor_arrow,
                        &cursor_cross,
                        &mut loaded,
                        &mut sprite_tex,
                    )
                    .ok();
                })
                .map_err(|e| match e {
                    sdl3::render::TargetRenderError::SdlError(s) => s,
                })?;
            // 贴回：逻辑 (0,0) 落在屏幕 `center - center*z`
            let (cx, cy) = app.layout_edit.zoom_center();
            canvas.set_draw_color(colors::C_BG);
            canvas.clear();
            canvas.copy(
                tex,
                None,
                FRect::new(
                    cx - cx * zoom,
                    cy - cy * zoom,
                    window::WIN_W as f32 * zoom,
                    window::WIN_H as f32 * zoom,
                ),
            )?;
            drawn.flatten()
        } else {
            render::draw_frame(
                &app,
                &mut frame_state,
                &mut canvas,
                &tex_creator,
                &mut ui,
                &mut ui_texts,
                &mut names,
                &mut shop_texts,
                &mut libs,
                &mut tiles,
                &mut draws,
                &mut sprites,
                &asset_dir,
                archive.as_ref(),
                &cursor_arrow,
                &cursor_cross,
                &mut loaded,
                &mut sprite_tex,
            )?
        };
        // 编辑器的控件框**画在放大之后**（跟着放大换算），字才不会被拉糊。
        if app.layout_edit.on {
            app.layout_edit
                .draw(&mut canvas, &mut names, &tex_creator)?;
        }
        if let Some(image) = image {
            app.asset_view.select_image(image);
        }

        // 底部那条**开发用**提示条撤了：现在那儿是 HUD 操作面板（字会压在按钮上）。
        // 按键提示改在**进世界时推一行到聊天区**（玩家看得到的那处）。
        if !app.hint_pushed && app.scene == crate::app_state::Scene::World {
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
                    n.chat.push(trunc(hint_text(app.scene), 30), C_CHAT_SYS);
                    println!(
                        "[hud] 按键提示已进聊天区：{}",
                        trunc(hint_text(app.scene), 30)
                    );
                }
            }
        }

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

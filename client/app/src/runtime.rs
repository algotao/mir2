//! 主循环执行器：SDL 事件分派、运行态更新与 `render::draw_frame` 编排。
//!
//! `main.rs` 负责创建平台资源；资源建好后交给这里运行。长期可继续把事件处理
//! 按输入域拆开，但所有可变会话状态已经归到 `AppState`。

use std::collections::HashMap;
use std::time::{Duration, Instant};

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
                    if keyboard::handle_key_down(
                        &mut app,
                        keycode,
                        keymod,
                        &sound,
                        &sounds,
                        &draws,
                        &tiles,
                        archive.as_ref(),
                        &mut status,
                        &mut lib_idx,
                        &mut img_idx,
                    )? {
                        break 'run;
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
                Event::MouseWheel { y, .. } if app.mode == 2 => {
                    world_input::handle_mouse_wheel(&mut app, y)
                }
                // 鼠标移动：**在世界里 ⇒ 左键走 / 右键跑**（照原版 `ClMain.pas:2246-2352`：
                // 左键 = 走，右键 = 跑；方向由鼠标相对角色的方位定，见 `dir_to`）。
                // 离线看地图（没进世界）时左键仍是那个诊断探针 —— 它是开发用的，
                // 别和"走路"抢同一个键。
                Event::MouseButtonDown {
                    mouse_btn, x, y, ..
                } if app.mode == 2 => {
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
                } if app.mode == 2
                    && matches!(mouse_btn, MouseButton::Left | MouseButton::Right) =>
                {
                    match world_input::handle_world_mouse_up(&mut app, mouse_btn, x, y) {
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
                } if app.mode == 4 => {
                    scene_input::select_down(&mut app, &mut ui, &asset_dir, (x, y));
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 4 => {
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
                } if app.mode == 1 => {
                    scene_input::login_down(&mut app, &mut ui, &asset_dir, (x, y));
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if app.mode == 1 => {
                    let act = scene_input::login_up(&mut app, &mut ui, &asset_dir, (x, y));
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
                        login::Action::NewAccount => status = "NEW ACCOUNT".into(),
                        login::Action::CancelSignup => status = String::new(),
                        login::Action::ChangePassword => {
                            status = "CHANGE PASSWORD: NOT WIRED YET".into();
                        }
                        login::Action::None => {}
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

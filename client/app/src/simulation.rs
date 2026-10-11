//! 每帧世界模拟：走路/追打/绕障/音效/镜头推进。
//!
//! 不画 SDL 控件；在 `runtime` 的 network pump 后、render 前调用。搬动期间保持原顺序，
//! 这让帧阶段的调用边界可读，而具体规则仍由 `input` / `AppState` 拥有。

use std::time::{Duration, Instant};

use crate::app_state::AppState;
use crate::audio;
use crate::*;

pub(crate) fn tick(
    app: &mut AppState,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
) -> bool {
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
    if app.scene == crate::app_state::Scene::World
        && app.world_control.held_move.is_some()
        && app.world_control.butch_target.is_none()
        && app.world_control.retarget_at.elapsed() >= Duration::from_millis(MOUSE_REPEAT_MS)
    {
        app.world_control.retarget_at = Instant::now();
        if let Some(n) = app.net.as_ref().filter(|n| n.world.in_world()) {
            let cell = screen_to_cell(app.cam, app.mouse.0, app.mouse.1);
            let run = app.world_control.held_move == Some(crate::world_control::MoveButton::Run);
            // ⚠️ 用 `mouse_repeat` 而不是 `mouse_intent`：按住时**不能**因为
            // "光标不在怪身上了"就把锁住的怪丢掉（镜头跟着人走，光标一定会滑开，
            // 否则按住点怪会半路变成走路）。见 `mouse_repeat` 的说明。
            let (ct, mt) = mouse_repeat(&n.world, cell, run, app.world_control.combat_target);
            app.world_control.combat_target = ct;
            app.world_control.move_target = mt;
        }
    }

    // **锁定的攻击目标**：够近就出手、不够近就靠近。
    //
    // 照原版：`_DXDrawMouseDown` 点到怪就 `g_TargetCret := target`（`ClMain.pas:2866`），
    // 之后 `MouseTimerTimer`（`ClMain.pas:2962-2997`）**每帧**对那个目标调 `AttackTarget`
    // —— `AttackTarget`（`:2691-2743`）自己判"相邻就砍、不够近就朝它旁边那格走/跑"。
    // 也就是原版**没有"按住才打"这一说**：锁住了就一直打，直到它死掉或消失。
    if app.scene == crate::app_state::Scene::World
        && !app.net.as_ref().is_some_and(|n| n.world.self_dead)
    {
        if let Some(id) = app.world_control.combat_target {
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
                    app.world_control.clear_targets();
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
                    if can_attack(
                        stepping || swing_busy,
                        app.world_control.attack_at.elapsed(),
                        level,
                    ) {
                        if let Some(n) = app.net.as_ref() {
                            let _ = n.attack_target(id);
                            swing_sfx(n, sound, sounds);
                        }
                        app.world_control.attack_at = Instant::now();
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
                    if !swing_busy && app.world_control.detour.is_none() {
                        app.world_control.move_target = Some((x, y, run));
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
    if fails != app.world_control.last_move_fail {
        let reason = app.net.as_ref().map_or(0, |n| n.world.move_fail_reason);
        app.world_control.last_move_fail = fails;
        app.world_control.move_at = Instant::now();
        if reason == 3
            && app.world_control.move_target.is_some()
            && app.world_control.detour != Some(1)
        {
            // **撞墙先绕一步**（原版 `ClMain.pas:1886-1923`：直走那格过不去时
            // 先试 `PrivDir`（逆时针 45°），再试 `NextDir`（顺时针 45°），
            // 能走就走），而不是像原来那样"清目标 + 锁 1 秒"——
            // 那正是用户 2026-10-09 第 4 条说的"点哪走哪"手感问题。
            // 两次都撞不上才认输（下面那条），并且**不加 1 秒锁**（只等一个正常步频）。
            app.world_control.detour = Some(app.world_control.detour.map_or(0, |k| k + 1));
            println!(
                "[move] 前方被挡（reason=3）⇒ 侧移 45° 试第 {} 次",
                app.world_control.detour.unwrap_or(1) + 1
            );
        } else {
            // 越界(2)/超速(1)，或绕障两次都不行 ⇒ 照原版 `ActionFailed`：
            // 清走法目标 + 锁 [`MOVE_FAIL_LOCK_MS`]（"就近停下"）。
            // ⚠️ **追打目标也要一起放**（用户 2026-10-10 第 6 条）：追打那块
            // 每帧都会把 move_target 重算回去 —— 只清 move_target 的话，
            // 下一帧又凑近、又被拒、锁完接着顶，永远停不下来。
            // 目标格被怪占着凑不近 ⇒ 就近停下，等玩家重新点。
            app.world_control.cancel_target();
            app.world_control.move_block_until =
                Instant::now() + Duration::from_millis(MOVE_FAIL_LOCK_MS);
            println!(
                "[move] 服务端拒绝了这一步（reason={reason}，1=超速 2=越界 3=阻挡）\
             —— 清走法目标 + 锁 {MOVE_FAIL_LOCK_MS}ms"
            );
        }
    }

    // 位置一变 ⇒ 上一步走成了 ⇒ 绕障状态清掉，回到"朝目标直走"
    if let Some(n) = app.net.as_ref() {
        let p = n.world.self_pos;
        if p != app.world_control.last_self_pos {
            app.world_control.last_self_pos = p;
            app.world_control.detour = None;
        }
    }

    if app.scene == crate::app_state::Scene::World {
        if let Some((tx, ty, run)) = app.world_control.move_target {
            let here = app
                .net
                .as_ref()
                .filter(|n| n.world.in_world())
                .map(|n| (n.world.self_pos.0, n.world.self_pos.1));
            match here {
                // 没进世界（掉线/还没到）⇒ 目标作废，别攒着一堆移动
                None => app.world_control.move_target = None,
                Some(pos) => match input::route_step(app.map.as_ref(), pos, (tx, ty), run) {
                    // 已经站在目标格上 ⇒ 收工（原版到点也停）
                    None => app.world_control.move_target = None,
                    Some((dir, step_run)) => {
                        // 绕障：把这一步转 45°（先逆后顺），并且**用走的**
                        //（侧移一小步就够，跑一次 2 格反而更容易顶死在障碍上）。
                        let (dir, step_run) = match app.world_control.detour {
                            Some(0) => (turn45(dir, -1), false),
                            Some(1) => (turn45(dir, 1), false),
                            _ => (dir, step_run),
                        };
                        let gap = if step_run { RUN_MS } else { WALK_MS };
                        // 被拒后的锁还没到期 ⇒ 先别发（原版 `IsUnLockAction`，
                        // `ClMain.pas:4014-4021`：锁着就一律不许动）
                        if app.world_control.move_block_until <= Instant::now()
                            && app.world_control.move_at.elapsed() >= Duration::from_millis(gap)
                        {
                            move_if_online(&app.net, dir, step_run);
                            app.world_control.move_at = Instant::now();
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
        app.status = if ok {
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
            sfx(sound, sounds, s);
        }
        if n.take_gameover() {
            // 自己死亡 ⇒ game over 音乐（原版 `Actor.pas:2373-2374`）
            bgm(sound, sounds, mir2_core::sound::BGM_GAMEOVER);
        }
    }
    // 选角场景排出来的音效（选中一个槽 ⇒ 解冻声 `101`，`IntroScn.pas:1170`）
    if let Some(scene) = app.select_scene.as_mut() {
        for s in scene.take_sfx() {
            sfx(sound, sounds, s);
        }
    }
    // 场景 BGM：登录（`IntroScn.pas:518`）与选角（`:1152`）各一首**循环**。
    // 进图音乐 `Music/<地图音乐号>.mp3` **暂时没有声音**（协议没有那个字段、
    // 手上也没有 mp3）⇒ 进地图就停掉上一首，而不是放错一首。
    // 自己死了就别停：那时该响的是 game over 那首（`Actor.pas:2373-2374`）。
    if app.scene == crate::app_state::Scene::Select {
        bgm(sound, sounds, mir2_core::sound::BGM_SELECT);
    } else if app.scene == crate::app_state::Scene::Login {
        // ⚠️ 开门动画期间**继续放**登录曲（原版 `PlayBGM(bmg_intro)` 一放放到换场景，
        // `IntroScn.pas:518`；门放完 `ChangeScene(stSelectChr)` 才换成选角曲）。
        // 早先这里多了个 `opened_at.is_none()` ⇒ 门一开就静音三秒（踩过）。
        bgm(sound, sounds, mir2_core::sound::BGM_LOGIN);
    } else if app.scene == crate::app_state::Scene::World
        && !app.net.as_ref().is_some_and(|n| n.world.self_dead)
        && sound.stop_bgm()
    {
        println!("[audio] BGM 停（进图音乐这条路还没接：手上没有 mp3、协议里也没有音乐号）");
    }
    // 怪物"正常声"：鸡叫/鹿鸣…（用户 2026-10-09 第 4 条）。
    // 官方规则与我们的近似写法见 `sfx::monster_ambient` 的说明。
    if app.scene == crate::app_state::Scene::World {
        if let Some(n) = app.net.as_ref() {
            monster_ambient(
                n,
                sound,
                sounds,
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
                if let Some(second_foot) = footstep_of(frame, app.world_control.last_foot_frame) {
                    let (mx, my) = (n.world.self_pos.0, n.world.self_pos.1);
                    let cell = m.at((mx.max(0) / 2 * 2) as usize, (my.max(0) / 2 * 2) as usize);
                    if let Some(c) = cell {
                        let t = mir2_core::sound::terrain(c.bk_img, c.area, c.mid_img, c.fr_img);
                        sfx(
                            sound,
                            sounds,
                            mir2_core::sound::footstep(t, running, second_foot),
                        );
                    }
                }
                app.world_control.last_foot_frame = Some(frame);
            }
            None => app.world_control.last_foot_frame = None,
        },
        _ => app.world_control.last_foot_frame = None,
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

    update::tick(app);

    // 世界侧持续状态推进（按住挖、到位捡取、对话滚动归零）—— 不画、可独立验证。
    update::advance_world(app)
}

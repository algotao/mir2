//! 网络事件泵与 Entrance → AppState 同步（runtime 的 network/update 阶段）。

use std::time::Instant;

use crate::app_state::AppState;
use crate::audio;
use crate::*;

/// 每帧泵网络，并把认证/选角状态投影到客户端场景状态。
pub(crate) fn pump(
    app: &mut AppState,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
) {
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
            app.scene = crate::app_state::Scene::Login; // 开门动画在登录屏上播（见 `login::draw` 的说明）
            sfx(sound, sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
        }
        // 兜底：**认领会话**那条路（没有口令登录这一步）落到世界里时才补一次开门。
        if n.entrance.in_world() && app.login.opened_at.is_none() {
            app.login.opened_at = Some(Instant::now());
            app.login.busy = false;
            app.select_scene = None;
            app.scene = crate::app_state::Scene::Login;
            sfx(sound, sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
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
            app.scene,
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
            app.scene = crate::app_state::Scene::Select;
        }
        // 选角通过 ⇒ 换到游戏主场景（原版在这里是 `ChangeScene(stPlay)`）
        if p.enter_play {
            app.scene = crate::app_state::Scene::World;
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
        if app.scene == crate::app_state::Scene::Select && n.entrance.failed().is_some() {
            app.scene = crate::app_state::Scene::Login;
        }
        if let Some(tok) = n.entrance.session_token() {
            n.session = tok;
        }
    }
}

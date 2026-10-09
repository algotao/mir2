//! 每帧的"网络 → 画面"决策（换屏、选角动作、登录/建号提交）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use crate::debug::{DEBUG_LAYERS, DEBUG_OVERLAY};
use crate::net::Net;
use crate::sfx::sfx;

use crate::{audio, login, select};

/// **每帧"网络 → 画面"的决策**（纯函数；主循环只负责执行它）。
///
/// # 为什么一定要有这一层
///
/// 2026-10-08 用户问："这么显著的问题，你的测试用例是怎么通过的？" —— 答案就在这个函数
/// **以前不存在**：那两条判定原本写死在**主循环里**（`pump()` 里那两段），而主循环要 SDL
/// 窗口与素材才能跑 ⇒ **测试碰不到它**，于是"测试全绿 + 功能是坏的"能并存。两条判定
/// **各漏过一次**，症状都是"画面没跟着状态机走"：
///
///   ① 建/删角之后不重建：守卫写成 `mode != 4 && awaiting_pick` —— 而建角**恰恰发生在
///      已经在选角屏**的时候（`mode == 4`）⇒ 整条分支被跳过（“建完角色要重登才看得见”）。
///   ② 选角通过后不换屏：没人把 `mode` 切到 2 ⇒ 世界在跑（能听见受击/死亡声）、
///      画面却还停在选角界面。当时留下的 `Select::start_clicked` 是个**只写不读**的标记。
///
/// 搬到这个纯函数之后，**同样这两个错误会让 `换屏判定` 变红**（那条测试里写了怎么复现，
/// 而且实际改回去验过一遍：红 → 改回来 → 绿）。**判据全部来自状态机**，没有一处是"猜的时机"。
///
/// 参数就是主循环手里那几件事实：
/// - `awaiting_pick`：状态机停在"等你选角"（`Stage::AwaitPick`）；
/// - `in_world`：状态机说已经进世界了（`Entrance::in_world`）；
/// - `changed`：状态机手里的角色列表与界面上的场景**不一样**（`list_changed`）；
/// - `door_blocking`：开门动画还在放（原版顺序：门放完 → `ChangeScene(stSelectChr)`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Plan {
    /// 按新的角色列表**重建**选角场景。
    pub(crate) rebuild_select: bool,
    /// 把画面切到选角。门还在放时**不切** —— 一上来就切会把门当场掐掉。
    pub(crate) enter_select: bool,
    /// 把画面切到**游戏主场景**（选角通过）。
    pub(crate) enter_play: bool,
}

pub(crate) fn plan(
    mode: u8,
    awaiting_pick: bool,
    in_world: bool,
    changed: bool,
    door_blocking: bool,
) -> Plan {
    Plan {
        // ⚠️ 这里**绝不能**再加"当前不在选角屏"这类条件（bug ① 就是加了个 `mode != 4`）：
        //    建/删角发生在选角屏**里面**，加上它 = 列表更新了却不重建。
        rebuild_select: awaiting_pick && changed,
        // 门放完之前不切（`opened_at.is_none()` = 压根没有门要走 ⇒ 立刻可切）。
        enter_select: awaiting_pick && !door_blocking,
        // ⚠️ 只从**选角屏**进游戏（门那屏由主循环里 `mode == 1` 那条分支自己接手）；
        //    缺了这条 = 只闻其声、不见其画面（bug ②）。
        enter_play: mode == 4 && in_world,
    }
}

/// 角色列表变了没（决定要不要重建选角场景，见调用处）。
///
/// 抽出来是为了能单测：这条判断错了，一边是**日志刷屏 + 选中位置被每帧抹掉**
///（门还在放的那三秒里 `mode` 还是 1，分支每帧命中），另一边是列表更新了却不重建
///（建完角色回到选角看不到新角色）。
pub(crate) fn list_changed(scene: Option<&select::Select>, chars: &[select::CharEntry]) -> bool {
    match scene {
        Some(s) => s.chars.as_slice() != chars,
        None => true,
    }
}

/// 选角场景做出的动作 → 真的去做。
///
/// ⚠️ 抽出来是因为**键盘与鼠标两条路**都会产生它：两处各写一遍迟早漂移
///（一边发了 `SelectCharacter`、另一边忘了）。返回 `true` = 该退出程序。
pub(crate) fn do_select_action(
    act: select::Action,
    net: &mut Option<Net>,
    select_scene: &mut Option<select::Select>,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
) -> Result<bool, sdl3::Error> {
    // 点下去的按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` = 103）。
    // 槽上那一下的**解冻声**（101）由场景自己排出来（见 `Select::take_sfx`）。
    if act != select::Action::None {
        sfx(sound, sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
    }
    match act {
        select::Action::None => Ok(false),
        select::Action::Exit => Ok(true),
        // 建角：状态机只在选角阶段（`AwaitPick`）才发得出去。发不出去就**明说一句**，
        // 不静默（这条纪律踩过：静默吞命令的表现就是"点了没反应"）。
        select::Action::Create {
            name,
            class,
            gender,
            hair,
        } => {
            let mut sent = false;
            if let Some(n) = net.as_mut() {
                if let Some(b) = n.entrance.create_character(&name, class, gender, hair) {
                    n.send(&b);
                    sent = true;
                    println!("[net] 建角：{name}（职业={class} 性别={gender} 发型={hair}）");
                }
            }
            if !sent {
                if let Some(s) = select_scene.as_mut() {
                    s.say("现在不能建角（连接/阶段不对）—— 重新登录后再试。");
                }
            }
            // 回执是**异步**的：成败都回来一次（失败弹窗、成功重拉列表，见
            // `Entrance::on` 的 `CreateCharacterResult` 那条）⇒ 这里不动界面。
            Ok(false)
        }
        // 删角：要**登录时那条口令证明**（`Entrance::delete_character` 自己带）。
        // 认领会话进来的没有证明 ⇒ 发不出去，明说一句怎么办。
        select::Action::Delete(id) => {
            let mut sent = false;
            if let Some(n) = net.as_mut() {
                if let Some(b) = n.entrance.delete_character(id) {
                    n.send(&b);
                    sent = true;
                    println!("[net] 删角：id={id}");
                }
            }
            if !sent {
                if let Some(s) = select_scene.as_mut() {
                    s.say(
                        "没发出去：删角要用登录时那条口令证明（认领会话进来的没有）—— \
                         用账号口令重新登一次再删。",
                    );
                }
            }
            Ok(false)
        }
        select::Action::Enter(id) => {
            if let Some(n) = net.as_mut() {
                match n.entrance.pick(id) {
                    Some(b) => {
                        // 角色名在**选角列表**里就有 ⇒ 先记进世界：`EnterWorld.self_name`
                        // 万一为空（老服务端/异常路径），头顶也画得出真名而不是占位词
                        //（用户 2026-10-09 的要求）。
                        let name = n
                            .entrance
                            .characters()
                            .iter()
                            .find(|c| c.character_id == id)
                            .map(|c| c.name.clone());
                        if let Some(name) = name {
                            n.world.remember_self_name(&name);
                        }
                        n.send(&b);
                        println!("[net] 选角：进入角色 ActorId={id}");
                    }
                    // 状态机不在等选角（比如已经发过一次）⇒ 忽略，别静默发怪消息
                    None => println!("[net] 选角：状态机不在等选角，忽略这次选择"),
                }
            }
            // ⚠️ 这里**不**记"点过了"：换屏的判据是状态机的 `in_world()`（见 `show()`），
            // 不是"哪颗按钮被按过" —— 早先那个只写不读的 `start_clicked` 就是这么留下的。
            Ok(false)
        }
    }
}

/// 底部的按键提示。
///
/// ⚠️ 抽成函数是为了**能测**：提示条必须跟着 [`DEBUG_LAYERS`] / [`DEBUG_OVERLAY`] 走
/// —— 关掉的功能还写在提示里，用户就会去按、然后按了没反应（那是另一种 bug 报告）。
pub(crate) fn hint_text(mode: u8) -> &'static str {
    match mode {
        2 => {
            // ⚠️ 提示条按**原版键位**写（`docs/use.md`）：Tab 小地图、M 大地图；
            // 开发查看器入口一律 `CTRL+`（F1~F8 是技能、F9~F12 是窗口，别抢）。
            const PLAY: &str = "LMB WALK  RMB RUN  TAB MINIMAP  M BIGMAP  SPACE HIT  \
                                C CONNECT  [ ] MAP  CTRL+M MUSIC  CTRL+F1 LOGIN  ESC";
            const DEBUG_KEYS: &str = "LMB WALK  RMB RUN  TAB MINIMAP  M BIGMAP  SPACE HIT  \
                                      C CONNECT  [ ] MAP  D DEBUG  CTRL+1/2/3 LAYER  \
                                      CTRL+M MUSIC  CTRL+F1 LOGIN  ESC";
            if DEBUG_LAYERS || DEBUG_OVERLAY {
                DEBUG_KEYS
            } else {
                PLAY
            }
        }
        1 => "TAB NEXT FIELD   ENTER LOGIN   F2 MAP   F3 ASSETS   M MUSIC   ESC QUIT",
        4 => "LEFT/RIGHT PICK   ENTER START   F1 LOGIN   F2 MAP   ESC QUIT",
        _ => "F3 ASSETS   [ ] LIB   , . IMG   F1 LOGIN   F2 MAP   M MUSIC   ESC QUIT",
    }
}

/// 提交登录。
///
/// ⚠️ 这里只做**客户端侧**的准备（必填校验、置忙、记日志）：真正的认证要走新协议的
/// `Login`，而它的口令形态是 [D-24](../../../docs/decisions.md) 在管的事 ——
/// 在定下来之前不假装成功（`docs/decisions.md` 原文：**也不把 `password_hash` 当成
/// "收到了就用"**）。
pub(crate) fn submit_login(login: &mut login::Login, net: &mut Option<Net>, status: &mut String) {
    if login.account.is_empty() {
        login.error = Some("Please enter your account name.".into());
        return;
    }
    if login.password.is_empty() {
        login.error = Some("Please enter your password.".into());
        return;
    }
    let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
    match Net::connect_with_password(&addr, &login.account, &login.password) {
        Ok(n) => {
            login.busy = true;
            login.error = None;
            *status = format!("LOGIN {}", login.account);
            println!(
                "[login] 提交：账号={:?} → 口令挑战应答（D-24①）",
                login.account
            );
            *net = Some(n);
        }
        Err(e) => login.error = Some(e),
    }
}

/// 提交建号（D-32）。
///
/// 与 `submit_login` 的差别只有两点：走 `connect_for_signup`，以及提示语措辞 ——
/// 建完**不自动登录**（原版也只是弹个提示，要用户自己再登一次）。
pub(crate) fn submit_signup(login: &mut login::Login, net: &mut Option<Net>, status: &mut String) {
    if login.account.trim().is_empty() {
        login.error = Some("Please enter your account name.".into());
        return;
    }
    if login.password.is_empty() {
        login.error = Some("Please enter your password.".into());
        return;
    }
    let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
    match Net::connect_for_signup(&addr, login.account.trim(), &login.password) {
        Ok(n) => {
            login.busy = true;
            login.error = None;
            *status = format!("SIGN UP {}", login.account.trim());
            println!("[login] 提交建号：账号={:?}", login.account.trim());
            *net = Some(n);
        }
        Err(e) => login.error = Some(e),
    }
}

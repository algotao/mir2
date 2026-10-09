//! 登录界面 —— **照原版**（`mir2standard/GameOfMir/MirClient/`）。
//!
//! 版式（"登录框长什么样"）在 `mir2_core::login_ui`：它是游戏知识，且必须能被
//! `client/e2e` 用同一份（那边无头合成一张图给人看）。这里只管两件事：
//!
//! 1. **交互**：焦点、输入、按钮按下/抬起、ESC；
//! 2. **绘制**：把版式算出来的矩形与素材编号交给 `UiCache` 去画。
//!
//! ⚠️ 输入框是**自绘**的：原版用的是原生 `TEdit`（黑底白字 + 闪烁光标），
//! 素材里没有这张图 ⇒ 画黑底 + 白字 + 光标。
//!
//! ⚠️ 按钮**没有 hover/按下图**：原版 `TDControl.DirectPaint` 只画一张 `FaceIndex`
//! （`DWinCtl.pas:624-640`），按下反馈是**坐标 +1**（`FState.pas:785` 的注释说的就是它）。
//!
//! ⚠️ 报错是**弹窗**（`FState.DMessageDlg`），弹窗不关掉碰不到输入框
//! （`IntroScn.pas:613-619` 的 `PassWdFail`）。
//!
//! # 与我们的差异（有意为之）
//!
//! - **没有"选择服务器"那一步**：服务器清单按 D-13 走客户端本地配置，不进 schema。
//! - **没上音效**：原版登录放 `bmg_intro`（`wav\log-in-long2.wav`）与按钮声
//!   （`SoundUtil.pas:32,109-115`）；我们的音频层目前只有一个测试音。
//! - **中文打不进来**：SDL3 的 8x8 调试字体只认 ASCII（`main.rs` 文件头写明）。
//!   这是登录界面上**唯一**明显不像原版的地方，要改得先换字体。

use std::time::Instant;

use sdl3::keyboard::Keycode;
use sdl3::render::{TextureCreator, WindowCanvas};

use mir2_core::login_ui::{Art, Layout, Rect};

use crate::font::{Rgb, TextCache};
use crate::ui::UiCache;
use crate::{center_x, fill, text, C_ACTIVE, C_BG, C_DIM, C_ERR, C_FIELD, C_OK, C_TEXT};

/// 光标闪烁周期（原版是系统 `TEdit` 的光标；这里 500ms 闪一下）。
const CARET_MS: u128 = 500;

/// 弹窗文字那套配色（**真字体**那条路用 `Rgb`；本文件其余地方用的 `C_*` 是 SDL 的 `Color`）。
const RGB_TEXT: Rgb = (255, 255, 255);
const RGB_SHADOW: Rgb = (0, 0, 0);

/// 界面上的动作（按键或点按产生；由 `main` 决定"接下来干什么"）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    None,
    /// 提交登录（`[提交]` 或回车）。
    Submit,
    /// 切到了**建号面板**（原版 `DLoginNew` → `DNewAccount`；面板切换由本模块自己完成）。
    NewAccount,
    /// 提交建号（建号面板的 `[确定]` 或回车；本地校验不过不会产生这条）。
    SubmitSignup,
    /// 关掉建号面板回登录（`[取消]` / `[X]` / ESC）。
    CancelSignup,
    /// 修改密码（原版开 `DLoginChgPw` 对话框；同样还没接）。
    ChangePassword,
    /// 关掉报错弹窗。
    Dismiss,
    /// 退出（`[X]` 或 ESC）。
    Quit,
}

/// 面板模式（D-32）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    /// 登录面板（原版 `DLogin`）。
    Login,
    /// 建号面板（原版 `DNewAccount`；我们只留 3 个字段，见 `login_ui::Layout::confirm`）。
    Signup,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Btn {
    Ok,
    New,
    ChgPw,
    Close,
    MsgOk,
    /// 建号面板的 `[确定]` / `[取消]`（原版 `DNewAccountOk/Cancel`）。
    SignupOk,
    SignupCancel,
}

/// 登录界面的状态。
pub struct Login {
    pub account: String,
    pub password: String,
    /// 建号面板的"确认口令"（只在 `Mode::Signup` 用）。
    pub confirm: String,
    /// 当前是哪块面板（登录 / 建号）。
    pub mode: Mode,
    /// 0 = 用户名框，1 = 密码框，2 = 确认口令框（**只在建号模式**）。
    pub focus: usize,
    /// 报错弹窗里那句话（`None` = 没有弹窗）。
    pub error: Option<String>,
    /// 已发出登录请求（按钮按着不放的样子 + 输入锁定）。
    pub busy: bool,
    /// 登录成功那一刻 ⇒ 开始播开门动画。
    pub opened_at: Option<Instant>,
    /// 正被按住的按钮（原版靠它做"+1 位移"的按下反馈）。
    pressed: Option<Btn>,
}

impl Default for Login {
    fn default() -> Self {
        Self::new()
    }
}

impl Login {
    pub fn new() -> Self {
        Self {
            account: String::new(),
            password: String::new(),
            confirm: String::new(),
            mode: Mode::Login,
            focus: 0,
            error: None,
            busy: false,
            opened_at: None,
            pressed: None,
        }
    }

    /// 现在是不是建号面板。
    pub fn signup(&self) -> bool {
        self.mode == Mode::Signup
    }

    /// 切到建号面板（原版 `IntroScn.pas:929-934` 的 `NewClick`）。
    ///
    /// 账号**留着**（用户多半是想把这个名字注册掉），口令清掉重输。
    pub fn enter_signup(&mut self) {
        self.mode = Mode::Signup;
        self.password.clear();
        self.confirm.clear();
        self.focus = if self.account.is_empty() { 0 } else { 1 };
        self.error = None;
        self.pressed = None;
    }

    /// 关掉建号面板回登录（原版 `NewAccountClose` → `ChangeLoginState(lsLogin)`）。
    pub fn leave_signup(&mut self) {
        self.mode = Mode::Login;
        self.password.clear();
        self.confirm.clear();
        self.focus = 0;
        self.pressed = None;
    }

    /// 建号面板的**本地校验**（原版 `CheckUserEntrys`，`IntroScn.pas:976-1029`）：
    /// 账号/口令至少 3 位、两次口令一致。不过关就弹模态框（原版也是弹框）。
    ///
    /// ⚠️ 口令长度**只有这里把关**：建号时服务端只收到校验值，从来见不到口令
    ///（原版服务端同样不查口令，见 D-32 与 `account.proto` 的说明）。
    fn try_signup(&mut self) -> Action {
        let why = if self.account.trim().chars().count() < 3 {
            Some("Please enter at least 3 characters for the account name.")
        } else if self.password.chars().count() < 3 {
            Some("Please enter at least 3 characters for the password.")
        } else if self.password != self.confirm {
            Some("The two passwords do not match.")
        } else {
            None
        };
        match why {
            Some(msg) => {
                self.error = Some(msg.into());
                Action::None
            }
            None => Action::SubmitSignup,
        }
    }

    /// 开门动画播完了吗（播完就该切到地图）。
    pub fn door_done(&self) -> bool {
        self.opened_at.is_some_and(|t| {
            t.elapsed().as_millis() >= (Art::DOOR_FRAMES as u128) * Art::DOOR_MS as u128
        })
    }

    /// 输入文本。⚠️ 只收 ASCII —— 8x8 调试字体认不了别的（见文件头）。
    pub fn on_text(&mut self, t: &str) {
        if self.busy || self.error.is_some() {
            return;
        }
        let (field, max) = match (self.mode, self.focus) {
            (_, 0) => (&mut self.account, Art::MAX_ACCOUNT),
            (_, 1) => (&mut self.password, Art::MAX_PASSWORD),
            (Mode::Signup, _) => (&mut self.confirm, Art::MAX_PASSWORD),
            // 登录面板只有两个框，焦点 2 不该出现
            (Mode::Login, _) => return,
        };
        for ch in t.chars() {
            if !(ch.is_ascii_graphic() || ch == ' ') || field.chars().count() >= max {
                continue;
            }
            field.push(ch);
        }
    }

    pub fn on_key(&mut self, k: Keycode) -> Action {
        // 报错弹窗是**模态**的
        if self.error.is_some() {
            return match k {
                Keycode::Return | Keycode::KpEnter | Keycode::Escape => {
                    self.error = None;
                    Action::Dismiss
                }
                _ => Action::None,
            };
        }
        match k {
            Keycode::Tab => {
                // 建号面板三个框（账号/口令/确认），登录面板两个
                let n = if self.signup() { 3 } else { 2 };
                self.focus = (self.focus + 1) % n;
                Action::None
            }
            Keycode::Backspace => {
                match (self.mode, self.focus) {
                    (_, 0) => {
                        self.account.pop();
                    }
                    (_, 1) => {
                        self.password.pop();
                    }
                    (Mode::Signup, _) => {
                        self.confirm.pop();
                    }
                    (Mode::Login, _) => {}
                }
                Action::None
            }
            Keycode::Return | Keycode::KpEnter => {
                if self.signup() {
                    self.try_signup()
                } else {
                    Action::Submit
                }
            }
            Keycode::Escape => {
                if self.signup() {
                    self.leave_signup();
                    Action::CancelSignup
                } else {
                    Action::Quit
                }
            }
            _ => Action::None,
        }
    }

    /// 鼠标按下：记下按住的是哪颗（画的时候 +1 位移）。
    ///
    /// ⚠️ 输入框没有"按下"状态 —— 原版也只是把焦点交给那个 `TEdit`。
    pub fn on_down(&mut self, p: (f32, f32), l: &Layout) {
        if self.error.is_some() {
            self.pressed = l.msg_ok.hit(p).then_some(Btn::MsgOk);
            return;
        }
        // 建号面板只有 [确定]/[取消]/[X] 可点；登录面板是 [提交]/[新用户]/[改密码]/[X]
        self.pressed = if self.signup() {
            if l.signup_ok.hit(p) {
                Some(Btn::SignupOk)
            } else if l.signup_cancel.hit(p) {
                Some(Btn::SignupCancel)
            } else if l.close.hit(p) {
                Some(Btn::Close)
            } else {
                None
            }
        } else if l.ok.hit(p) {
            Some(Btn::Ok)
        } else if l.new.hit(p) {
            Some(Btn::New)
        } else if l.chgpw.hit(p) {
            Some(Btn::ChgPw)
        } else if l.close.hit(p) {
            Some(Btn::Close)
        } else {
            None
        };
        // 输入框点一下就换焦点（原版也只是把焦点交给那个 TEdit）
        if self.pressed.is_none() {
            if l.account.hit(p) {
                self.focus = 0;
            } else if l.password.hit(p) {
                self.focus = 1;
            } else if self.signup() && l.confirm.hit(p) {
                self.focus = 2;
            }
        }
    }

    /// 鼠标抬起：只有"按下与抬起落在同一颗"才算点中。
    pub fn on_up(&mut self, p: (f32, f32), l: &Layout) -> Action {
        let Some(btn) = self.pressed.take() else {
            return Action::None;
        };
        // ⚠️ 弹窗的 [Ok] 要**真的把窗关掉**（清 `error`）：原来自绘的那颗只是画着好看 ——
        // `on_up` 不清 `error`、`main` 收到 `Dismiss` 也不做什么 ⇒ 点了没反应。
        // 回车那条路（`on_key`）一直是清的，所以**两条路必须都清**（用户 2026-10-08 报的）。
        if btn == Btn::MsgOk {
            if l.msg_ok.hit(p) {
                self.error = None;
                return Action::Dismiss;
            }
            return Action::None;
        }
        let (r, act) = match btn {
            Btn::Ok => (l.ok, Action::Submit),
            Btn::New => (l.new, Action::NewAccount),
            Btn::ChgPw => (l.chgpw, Action::ChangePassword),
            Btn::Close => (l.close, Action::Quit),
            Btn::MsgOk => (l.msg_ok, Action::Dismiss),
            Btn::SignupOk => (l.signup_ok, Action::SubmitSignup),
            Btn::SignupCancel => (l.signup_cancel, Action::CancelSignup),
        };
        if !r.hit(p) {
            return Action::None;
        }
        // ⚠️ 面板的切换在**这里**完成（不是 `main` 的事）：点 [新用户] 就切进建号面板，
        // 点 [取消]/[X] 就切回登录面板。动作只是"通知"，`main` 不必知道面板状态。
        match act {
            Action::NewAccount => {
                self.enter_signup();
                Action::NewAccount
            }
            Action::SubmitSignup => self.try_signup(),
            Action::CancelSignup => {
                self.leave_signup();
                Action::CancelSignup
            }
            // 建号面板上的 [X] = 关掉面板回登录（不是退出程序）
            Action::Quit if self.signup() => {
                self.leave_signup();
                Action::CancelSignup
            }
            other => other,
        }
    }

    /// 画一帧。素材缺失时画一行提示（**不静默黑屏**）。
    /// ⚠️ 参数名是 `texts` 而不是 `text`：本文件其余地方还要调那个**自由函数** `text()`
    ///（8×8 调试字体，画 ASCII 用），参数一旦也叫 `text` 就会把函数遮住（编译期报
    /// "expected function, found &mut TextCache" —— 踩过）。
    #[allow(clippy::too_many_arguments)] // 与 `font::TextCache::draw` / `ui::UiCache::draw` 同一情况：坐标+配色+画布就是这么多
    pub fn draw<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        ui: &mut UiCache<'a>,
        texts: &mut TextCache<'a>,
        tc: &'a TextureCreator<T>,
        asset_dir: &Option<std::path::PathBuf>,
        win: (u32, u32),
        started: Instant,
    ) -> Result<(), sdl3::Error> {
        let winf = (win.0 as f32, win.1 as f32);
        fill(canvas, 0.0, 0.0, winf.0, winf.1, C_BG)?;
        let Some(dir) = asset_dir else {
            let msg = "ASSETS NOT FOUND - SET MIR2_ASSET_DIR";
            return text(canvas, msg, center_x(msg, 0.0, winf.0), winf.1 / 2.0, C_ERR);
        };

        // ① 背景：800×600 的图**居中 1:1** 铺在 1024×768 的窗口里，四周拿素材自己的石纹补
        //（不是纯黑 —— 用户 2026-10-09 报的"周围显示为黑底"；也不放大，那会糊）
        let bg = (Art::BG.0, Art::BG.1);
        // 补边块 (552,496)：实测最平且与边框环同调（见 `tile_backdrop`）
        ui.tile_backdrop(canvas, tc, dir, bg.0, bg.1, win, (552.0, 496.0));
        if let Some(sz) = ui.size(dir, bg.0, bg.1) {
            let (x, y) = Layout::bg_at(win, sz);
            ui.draw(canvas, tc, dir, bg.0, bg.1, x, y);
        }

        // ② 登录成功 ⇒ **先藏起登录小窗**，只留背景 + 开门动画。
        //
        // 原版 `OpenLoginDoor`（`IntroScn.pas:795-801`）就是这三步：
        //     m_boNowOpening := TRUE; HideLoginBox; PlaySound (s_rock_door_open);
        // 其中 `HideLoginBox` → `ChangeLoginState(lsCloseAll)` 把 `DLogin` 整块藏掉
        //（`IntroScn.pas:904/916/923` 都在设 `DLogin.Visible := FALSE`）。
        // 不藏的话门就画在登录框后面 —— 看起来"压根没有开门动画"（用户报的正是这个）。
        if let Some(t0) = self.opened_at {
            let f = (t0.elapsed().as_millis() / Art::DOOR_MS as u128) as u32;
            let idx = Art::DOOR.1 + f.min(Art::DOOR_FRAMES - 1);
            // ⚠️ `ChrSel[23]`（首帧）在本套素材里是空壳 ⇒ 那一帧什么都不画，这是素材
            // 事实不是 bug；取不到就跳过，别退回去把"门口"画没了。
            if ui.size(dir, Art::DOOR.0, idx).is_some() {
                // ⚠️ 门是 496×361 的**局部覆盖**，位置照原版（`IntroScn.pas:845-846`），
                // 不是"居中贴整屏背景"。
                let (x, y) = Layout::legacy_at(win, Layout::DOOR_AT);
                ui.draw(canvas, tc, dir, Art::DOOR.0, idx, x, y);
            }
            // 门在放：登录小窗（对话框/输入框/按钮/提示）**一律不画** —— 见上面的出处。
            return Ok(());
        }

        let Some(l) = Layout::build(win, |c, i| ui.size(dir, c, i)) else {
            let msg = "LOGIN ART MISSING - NEED Prguse.wzl";
            return text(canvas, msg, center_x(msg, 0.0, winf.0), winf.1 / 2.0, C_ERR);
        };

        // ③ 对话框
        ui.draw(
            canvas,
            tc,
            dir,
            Art::DIALOG.0,
            Art::DIALOG.1,
            l.dialog.x,
            l.dialog.y,
        );

        // ④ 输入框（原版是原生 TEdit：黑底白字 + 光标 ⇒ 自绘）
        let caret_on = (started.elapsed().as_millis() / CARET_MS).is_multiple_of(2);
        if self.signup() {
            // ⚠️ 建号面板的**面板图**原版是另一张 (`DNewAccount`)，本套素材里没有 ⇒
            // 复用登录对话框当底，标题与字段名用调试字体画（比原版"丑"，但能用；
            // 与"中文打不进来"是同一条已知限制）。
            let title = "NEW ACCOUNT";
            text(
                canvas,
                title,
                center_x(title, l.dialog.x, l.dialog.x + l.dialog.w),
                l.dialog.y + 60.0,
                C_TEXT,
            )?;
            for (label, r) in [("ID", l.account), ("PW", l.password), ("PW2", l.confirm)] {
                text(canvas, label, r.x - 26.0, r.y + 4.0, C_DIM)?;
            }
        }
        self.field(canvas, l.account, &self.account, self.focus == 0, caret_on)?;
        let masked = "*".repeat(self.password.chars().count());
        self.field(canvas, l.password, &masked, self.focus == 1, caret_on)?;
        if self.signup() {
            let masked = "*".repeat(self.confirm.chars().count());
            self.field(canvas, l.confirm, &masked, self.focus == 2, caret_on)?;
        }

        // ⑤ 按钮（按下时 +1 位移）
        let down = |b: Btn| self.pressed == Some(b);
        let mut put = |canvas: &mut WindowCanvas, idx: u32, r: Rect, pressed: bool| {
            let (dx, dy) = if pressed { (1.0, 1.0) } else { (0.0, 0.0) };
            ui.draw(canvas, tc, dir, Art::DIALOG.0, idx, r.x + dx, r.y + dy);
        };
        if self.signup() {
            // 建号面板：[确定] / [取消]（原版 `DNewAccountOk/Cancel`）
            put(
                canvas,
                Art::BTN_SIGNUP_OK.1,
                l.signup_ok,
                down(Btn::SignupOk),
            );
            put(
                canvas,
                Art::BTN_SIGNUP_CANCEL.1,
                l.signup_cancel,
                down(Btn::SignupCancel),
            );
        } else {
            put(canvas, Art::BTN_OK.1, l.ok, down(Btn::Ok));
            put(canvas, Art::BTN_NEW.1, l.new, down(Btn::New));
            put(canvas, Art::BTN_CHGPW.1, l.chgpw, down(Btn::ChgPw));
        }
        put(canvas, Art::BTN_CLOSE.1, l.close, down(Btn::Close));

        // ⑥ 报错弹窗（模态）
        //
        // ⚠️ 文字走**真字体**（`TextCache`），不是那个 8×8 调试字体：建号回执是**服务端
        // 给的中文**（「账号已建立，请登录」），而调试字体只认 ASCII ⇒ 画出来就是乱码
        //（2026-10-08 用户报的"信息看不清或是乱码"）。选角面板早就是按真字体画的。
        if let Some(err) = &self.error {
            ui.draw(
                canvas,
                tc,
                dir,
                Art::MSGBOX.0,
                Art::MSGBOX.1,
                l.msgbox.x,
                l.msgbox.y,
            );
            // 折行按**实测宽度**（不能按字符数：14px 的汉字 ≈14 宽、ASCII ≈7 宽，
            // 按"48 个字符"折出来的行会宽到框外去）；行数按框高截断，别压到 [确定] 上。
            // 提示可能带"下一步查什么"（`connect_hint`），所以行数留得比较宽。
            let lh = texts.line_height().max(1.0);
            let max_lines = (((l.msgbox.h - 96.0) / lh).floor() as usize).max(1);
            let lines = wrap_px(|s| texts.width(s), err, l.msgbox.w - 56.0);
            for (i, line) in lines.iter().take(max_lines).enumerate() {
                texts.draw(
                    canvas,
                    tc,
                    line,
                    l.msgbox.x + 28.0,
                    l.msgbox.y + 56.0 + i as f32 * lh,
                    RGB_TEXT,
                    Some(RGB_SHADOW),
                )?;
            }
            let (dx, dy) = if down(Btn::MsgOk) {
                (1.0, 1.0)
            } else {
                (0.0, 0.0)
            };
            ui.draw(
                canvas,
                tc,
                dir,
                Art::MSGBOX_OK.0,
                Art::MSGBOX_OK.1,
                l.msg_ok.x + dx,
                l.msg_ok.y + dy,
            );
        }

        // ⑦ busy / 提示（原版没有；这是开发查看器需要的可见性）
        if self.busy {
            text(
                canvas,
                "CONNECTING ...",
                l.dialog.x + 8.0,
                l.dialog.y + l.dialog.h - 16.0,
                C_OK,
            )?;
        } else if self.error.is_none() {
            let tip = "TAB NEXT   ENTER LOGIN";
            text(
                canvas,
                tip,
                center_x(tip, 0.0, winf.0),
                winf.1 - 44.0,
                C_DIM,
            )?;
        }
        Ok(())
    }

    /// 一个输入框：黑底（原版 TEdit 的颜色）+ 白字 + 闪烁光标。
    fn field(
        &self,
        canvas: &mut WindowCanvas,
        r: Rect,
        shown: &str,
        focused: bool,
        caret_on: bool,
    ) -> Result<(), sdl3::Error> {
        fill(canvas, r.x, r.y, r.w, r.h, C_FIELD)?;
        if focused && caret_on {
            text(
                canvas,
                "|",
                r.x + 2.0 + shown.chars().count() as f32 * 8.0,
                r.y + 4.0,
                C_ACTIVE,
            )?;
        }
        text(canvas, shown, r.x + 3.0, r.y + 4.0, C_TEXT)
    }
}

/// 按**字符数**折行（8x8 字体下"字符数"就是像素宽 / 8）。
/// 按**实测宽度**折行：贪心塞字，塞不下就换行；`\n` 是硬换行。
///
/// 为什么取一个 `width_of` 闭包而不是 `&TextCache`：这样它能**脱离 SDL 单测**
///（真字体要建纹理，测折行逻辑不该拉上那套）。调用处传 `|s| text.width(s)`。
fn wrap_px(width_of: impl Fn(&str) -> f32, s: &str, max_w: f32) -> Vec<String> {
    let mut out = Vec::new();
    for hard in s.split('\n') {
        let mut cur = String::new();
        for ch in hard.chars() {
            let mut cand = cur.clone();
            cand.push(ch);
            // 空行也要留（`cur` 为空时不换行，否则每个字符都独占一行）
            if !cur.is_empty() && width_of(&cand) > max_w {
                out.push(std::mem::take(&mut cur));
                cur.push(ch);
            } else {
                cur = cand;
            }
        }
        out.push(cur);
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Tab 切焦点、退格删自己那个框、回车提交。
    #[test]
    fn 键盘交互() {
        let mut l = Login::new();
        l.on_text("hero");
        assert_eq!(l.account, "hero");
        assert_eq!(l.on_key(Keycode::Tab), Action::None);
        assert_eq!(l.focus, 1);
        l.on_text("pw");
        assert_eq!(l.password, "pw");
        assert_eq!(l.on_key(Keycode::Backspace), Action::None);
        assert_eq!(l.password, "p", "退格只能删当前框");
        assert_eq!(l.account, "hero");
        assert_eq!(l.on_key(Keycode::Return), Action::Submit);
        assert_eq!(l.on_key(Keycode::Escape), Action::Quit);
    }

    /// 长度上限照原版（用户名 14、密码 10），且**只收 ASCII**。
    #[test]
    fn 输入上限与字符集() {
        let mut l = Login::new();
        l.on_text("0123456789abcdefGHIJ");
        assert_eq!(l.account.chars().count(), Art::MAX_ACCOUNT);
        l.on_key(Keycode::Tab);
        l.on_text("0123456789abc");
        assert_eq!(l.password.chars().count(), Art::MAX_PASSWORD);
        // 中文/控制字符进不来（字体认不了，进来只会画成乱码）
        l.on_text("勇士\u{1b}");
        assert_eq!(l.password.chars().count(), Art::MAX_PASSWORD);
    }

    /// 报错弹窗是**模态**的：弹着的时候按键只用来关它，输入框碰不到。
    #[test]
    fn 弹窗模态() {
        let mut l = Login::new();
        l.error = Some("Please enter your account name.".into());
        assert_eq!(l.on_key(Keycode::Tab), Action::None);
        assert_eq!(l.focus, 0, "弹窗时不切焦点");
        l.on_text("hero");
        assert!(l.account.is_empty(), "弹窗时不能往框里打字");
        assert_eq!(l.on_key(Keycode::Return), Action::Dismiss);
        assert!(l.error.is_none());
    }

    /// 弹窗那颗 [确定] **点了要真的关掉窗**（清 `error`）—— 与回车同一条路。
    ///
    /// 早先 `on_up` 只返回 `Dismiss` 而**不清 `error`** ⇒ 点了没反应（用户 2026-10-08 报的
    /// "鼠标点击确定，窗口不关闭"）。这里把两条路都钉住。
    #[test]
    fn 弹窗确定按钮点得关() {
        let l = {
            let mk = |c: &'static str, i: u32| match (c, i) {
                ("Prguse", 60) => Some((296, 254)),
                ("Prguse", 62) => Some((76, 33)),
                ("Prguse", 61) => Some((100, 32)),
                ("Prguse", 53) => Some((128, 33)),
                ("Prguse", 51) => Some((96, 34)),
                ("Prguse", 52) => Some((96, 33)),
                ("Prguse", 64) => Some((16, 23)),
                ("Prguse", 360) => Some((452, 179)),
                ("Prguse", 363) => Some((80, 34)),
                _ => None,
            };
            Layout::build((1024, 768), mk).unwrap()
        };
        let mut s = Login::new();
        s.error = Some("账号或口令不正确".into());
        // 点在 [确定] 上 ⇒ 关掉
        let c = (l.msg_ok.x + 4.0, l.msg_ok.y + 4.0);
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::Dismiss);
        assert!(s.error.is_none(), "点了[确定]必须把弹窗清掉");
        // 弹窗开着时点别处：不该误关（原版 `DMessageDlg` 只认它那颗按钮）
        s.error = Some("再来一次".into());
        let out = (2.0, 2.0);
        s.on_down(out, &l);
        assert_eq!(s.on_up(out, &l), Action::None);
        assert!(s.error.is_some(), "点弹窗外面不该关掉它");
    }

    /// 开门动画：10 帧 × 300ms（原版 `IntroScn.pas:826` 的 `> 300`，帧号 `ChrSel[23+n]`）。
    #[test]
    fn 开门动画时长与相位() {
        use std::time::Duration;
        let mut l = Login::new();
        assert!(l.opened_at.is_none(), "没登录前不该开门");
        assert!(!l.door_done());
        // 2.9 秒（第 10 帧还没走完）
        l.opened_at = Some(Instant::now() - Duration::from_millis(2900));
        assert!(!l.door_done(), "2.9 秒还没放完");
        // 3.0 秒 = 10 帧 × 300ms
        l.opened_at = Some(Instant::now() - Duration::from_millis(3000));
        assert!(l.door_done(), "3 秒（10 帧 × 300ms）该放完了");
    }

    /// 建号面板：本地校验（原版 `CheckUserEntrys`）——账号/口令 <3 位、两次不一致都拦住。
    ///
    /// ⚠️ 口令长度**只有这里把关**：服务端只收到校验值，见不见口令（D-32）。
    #[test]
    fn 建号面板本地校验() {
        let mut l = Login::new();
        l.enter_signup();
        assert!(l.signup());
        // 账号太短
        l.account = "ab".into();
        l.password = "pw123".into();
        l.confirm = "pw123".into();
        assert_eq!(l.on_key(Keycode::Return), Action::None, "账号太短不该提交");
        assert!(l.error.is_some(), "该弹模态框说原因");
        l.error = None;
        // 口令太短
        l.account = "newbie".into();
        l.password = "pw".into();
        l.confirm = "pw".into();
        assert_eq!(l.on_key(Keycode::Return), Action::None);
        assert!(l.error.is_some());
        l.error = None;
        // 两次不一致
        l.password = "pw123".into();
        l.confirm = "pw124".into();
        assert_eq!(l.on_key(Keycode::Return), Action::None);
        assert!(l.error.is_some());
        l.error = None;
        // 都对 ⇒ 提交（发送是 main 的事）
        l.confirm = "pw123".into();
        assert_eq!(l.on_key(Keycode::Return), Action::SubmitSignup);
    }

    /// 建号面板：三个框的焦点轮转、确认框能打字、"取消"回登录。
    #[test]
    fn 建号面板焦点与取消() {
        let mut l = Login::new();
        l.enter_signup();
        assert_eq!(l.focus, 0);
        l.on_key(Keycode::Tab);
        l.on_key(Keycode::Tab);
        assert_eq!(l.focus, 2, "建号面板有三个框");
        // 焦点在确认框时，打字进的是确认框
        l.on_text("a");
        assert_eq!((l.password.as_str(), l.confirm.as_str()), ("", "a"));
        l.on_key(Keycode::Tab);
        assert_eq!(l.focus, 0, "转回第一个");

        // ESC = 关掉面板回登录（不是退出程序）
        l.password = "x".into();
        l.confirm = "x".into();
        assert_eq!(l.on_key(Keycode::Escape), Action::CancelSignup);
        assert!(!l.signup(), "该切回登录面板");
        assert!(
            l.password.is_empty() && l.confirm.is_empty(),
            "回登录要清掉建号那两格"
        );
        assert_eq!(
            l.on_key(Keycode::Escape),
            Action::Quit,
            "登录面板的 ESC 才是退出"
        );
    }

    /// 建号面板的两颗按钮：`[确定]` 走校验、`[取消]` 回登录（面板切换在这里完成）。
    #[test]
    fn 建号按钮与面板切换() {
        let l = {
            let mk = |c: &'static str, i: u32| match (c, i) {
                ("Prguse", 60) => Some((296, 254)),
                ("Prguse", 62) => Some((76, 33)),
                ("Prguse", 61) => Some((100, 32)),
                ("Prguse", 53) => Some((128, 33)),
                ("Prguse", 51) => Some((96, 34)),
                ("Prguse", 52) => Some((96, 33)),
                ("Prguse", 64) => Some((16, 23)),
                ("Prguse", 360) => Some((452, 179)),
                ("Prguse", 363) => Some((80, 34)),
                _ => None,
            };
            Layout::build((1024, 768), mk).unwrap()
        };
        let mut s = Login::new();
        // 点 [新用户] ⇒ 面板切进建号（动作只是通知）
        let c = (l.new.x + 10.0, l.new.y + 10.0);
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::NewAccount);
        assert!(s.signup(), "面板该切到建号");
        // 点 [取消] ⇒ 回登录
        let c = (l.signup_cancel.x + 10.0, l.signup_cancel.y + 10.0);
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::CancelSignup);
        assert!(!s.signup());
    }

    /// 点按：只有"按下与抬起在同一颗按钮"才算点中（原版也是这个语义）。
    #[test]
    fn 点按命中() {
        let l = {
            let mk = |c: &'static str, i: u32| match (c, i) {
                ("Prguse", 60) => Some((296, 254)),
                ("Prguse", 62) => Some((76, 33)),
                ("Prguse", 61) => Some((100, 32)),
                ("Prguse", 53) => Some((128, 33)),
                ("Prguse", 51) => Some((96, 34)),
                ("Prguse", 52) => Some((96, 33)),
                ("Prguse", 64) => Some((16, 23)),
                ("Prguse", 360) => Some((452, 179)),
                ("Prguse", 363) => Some((80, 34)),
                _ => None,
            };
            Layout::build((1024, 768), mk).unwrap()
        };
        let mut s = Login::new();
        let c = (l.ok.x + 10.0, l.ok.y + 10.0);
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::Submit);
        // 按下之后滑出去再抬起 = 不算点中
        s.on_down(c, &l);
        assert_eq!(s.on_up((l.ok.x - 50.0, c.1), &l), Action::None);
        // 点输入框只改焦点，不产生动作
        s.on_down((l.password.x + 2.0, l.password.y + 2.0), &l);
        assert_eq!(s.focus, 1);
        assert_eq!(
            s.on_up((l.password.x + 2.0, l.password.y + 2.0), &l),
            Action::None
        );
    }

    /// 弹窗折行按**宽度**（真字体一个汉字 ≈14px，一个 ASCII ≈7px），`\n` 是硬换行。
    /// 按字符数折的老做法会把中文行折到框外去。
    #[test]
    fn 弹窗按宽度折行() {
        // 假装每个字符 10px 宽
        let w = |s: &str| s.chars().count() as f32 * 10.0;
        assert_eq!(wrap_px(w, "abcde", 50.0), vec!["abcde"]);
        assert_eq!(wrap_px(w, "abcdef", 50.0), vec!["abcde", "f"]);
        // 中文同规则：30px 宽 ⇒ 一行 3 个字
        assert_eq!(wrap_px(w, "账号已建立", 30.0), vec!["账号已", "建立"]);
        assert_eq!(wrap_px(w, "a\nb", 100.0), vec!["a", "b"]);
    }
}

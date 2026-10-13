//! 登录界面 —— **照原版**（`mir2standard/GameOfMir/MirClient/`）。
//!
//! 版式（"登录框长什么样"）在 `mir2_core::login_ui`：它是游戏知识，且必须能被
//! `client/e2e` 用同一份（那边无头合成一张图给人看）。本模块只管**交互状态**：焦点、输入、
//! 按键与按钮动作；SDL 绘制在 `login_view.rs`，通过 `Login::draw` 扩展实现提供。
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

use mir2_core::login_ui::{Art, Layout};

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
pub(crate) enum Btn {
    Ok,
    New,
    ChgPw,
    Close,
    MsgOk,
    /// 建号面板的 `[确定]` / `[取消]`（原版 `DNewAccountOk/Cancel`）。
    SignupOk,
    SignupCancel,
}

/// 点 `p` 落在 `r` 里吗？是 ⇒ 给引擎的 `(按钮, 矩形)`。
///
/// `widget::Rect` 是 `(x, y, w, h)`，而 `login_ui::Rect` 是结构体 ⇒ 在这里换一次。
fn pick(
    r: mir2_core::login_ui::Rect,
    p: crate::space::DPt,
    b: Btn,
) -> Option<(Btn, crate::widget::Rect)> {
    r.hit((p.0 .0, p.1 .0)).then_some((
        b,
        crate::widget::design_rect((
            crate::space::Dpx(r.x),
            crate::space::Dpx(r.y),
            crate::space::Dpx(r.w),
            crate::space::Dpx(r.h),
        )),
    ))
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
    }

    /// 关掉建号面板回登录（原版 `NewAccountClose` → `ChangeLoginState(lsLogin)`）。
    pub fn leave_signup(&mut self) {
        self.mode = Mode::Login;
        self.password.clear();
        self.confirm.clear();
        self.focus = 0;
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
            // 原版 `EdLoginPasswdKeyPress`（`IntroScn.pas:594-611`）把 `~` 与 `'` 换成 `_`
            //（`~`/`'` 在 6bit 报文里有特殊含义，原版就地规避；账号框也要，见 `:594`）。
            let ch = if ch == '~' || ch == '\'' { '_' } else { ch };
            field.push(ch);
        }
    }

    /// 登录/建号被服务端拒了：弹提示 + **把焦点还给账号框**。
    ///
    /// 原版 `PassWdFail`（`IntroScn.pas:613-619`）会重新显示两个 `TEdit` 并 `SetFocus`
    /// 到账号框 —— 用户接着就能重打，不用先按 Tab 摸回焦点。
    pub fn fail(&mut self, why: String) {
        self.error = Some(why);
        self.busy = false;
        // 焦点一律回**账号框**（原版 `PassWdFail` 就是 `m_EdId.SetFocus`）——
        // 建号失败时同理：三个框里第一个就是账号。
        self.focus = 0;
    }

    pub fn on_key(&mut self, k: crate::app_state::SceneKey) -> Action {
        // 报错弹窗是**模态**的
        if self.error.is_some() {
            return match k {
                crate::app_state::SceneKey::Enter | crate::app_state::SceneKey::Escape => {
                    self.error = None;
                    Action::Dismiss
                }
                _ => Action::None,
            };
        }
        match k {
            crate::app_state::SceneKey::Tab => {
                // 建号面板三个框（账号/口令/确认），登录面板两个
                let n = if self.signup() { 3 } else { 2 };
                self.focus = (self.focus + 1) % n;
                Action::None
            }
            crate::app_state::SceneKey::Backspace => {
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
            crate::app_state::SceneKey::Enter => {
                // 原版 `IntroScn.pas:583-611`：**账号框回车只把焦点跳到密码框**
                //（`m_EdPasswd.SetFocus`），**密码框回车才发 `SendLogin`**（两框都非空时）。
                // 我们原来任何框回车都直接提交 ⇒ 账号还没打完就发起登录了。
                if self.signup() {
                    self.try_signup()
                } else if self.focus == 0 {
                    self.focus = 1;
                    Action::None
                } else {
                    Action::Submit
                }
            }
            crate::app_state::SceneKey::Escape => {
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

    /// 鼠标按下：把"按住了哪颗"**交给控件引擎**（`widget::Ui`）。
    ///
    /// ⚠️ 原来是自己存一个 `self.pressed`，画的时候再看它 ⇒ 命中与绘制两处各判一次
    /// （而且没有"拖出去就不算"这条）。现在按官方 `TDButton` 的路子：按下记
    /// `(控件, 矩形)`，松开时对不上就不算点击 —— 登录界面也享受同一套规范。
    ///
    /// ⚠️ 输入框没有"按下"状态 —— 原版也只是把焦点交给那个 `TEdit`。
    pub fn on_down(&mut self, p: crate::space::DPt, l: &Layout, ui: &mut crate::widget::Ui) {
        let design_xy = (p.0 .0, p.1 .0);
        // 建号面板只有 [确定]/[取消]/[X] 可点；登录面板是 [提交]/[新用户]/[改密码]/[X]
        let hit = if self.error.is_some() {
            // 报错弹窗是**模态**的 ⇒ 只有它那颗 [Ok] 可点
            pick(l.msg_ok, p, Btn::MsgOk)
        } else if self.signup() {
            pick(l.signup_ok, p, Btn::SignupOk)
                .or_else(|| pick(l.signup_cancel, p, Btn::SignupCancel))
                .or_else(|| pick(l.close, p, Btn::Close))
        } else {
            pick(l.ok, p, Btn::Ok)
                .or_else(|| pick(l.new, p, Btn::New))
                .or_else(|| pick(l.chgpw, p, Btn::ChgPw))
                .or_else(|| pick(l.close, p, Btn::Close))
        };
        if let Some((b, r)) = hit {
            ui.press(crate::Press::Login(b), r);
            return;
        }
        // 没按在按钮上 ⇒ 输入框点一下就换焦点（原版也只是把焦点交给那个 TEdit）
        if l.account.hit(design_xy) {
            self.focus = 0;
        } else if l.password.hit(design_xy) {
            self.focus = 1;
        } else if self.signup() && l.confirm.hit(design_xy) {
            self.focus = 2;
        }
    }

    /// **点中了**某颗按钮（`widget::Ui` 已判定"按下与抬起落在同一颗"）⇒ 映射成动作。
    ///
    /// ⚠️ 原来这里还要自己再判一次矩形（命中判了第二遍）；现在那是引擎的事，
    /// 这里只管"这颗钮是什么意思"。
    pub fn click(&mut self, btn: Btn, l: &Layout) -> Action {
        // ⚠️ 弹窗的 [Ok] 要**真的把窗关掉**（清 `error`）：原来自绘的那颗只是画着好看 ——
        // 不清 `error` ⇒ 点了没反应。回车那条路（`on_key`）一直是清的，
        // 所以**两条路必须都清**（用户 2026-10-08 报的）。
        if btn == Btn::MsgOk {
            self.error = None;
            return Action::Dismiss;
        }
        let act = match btn {
            Btn::Ok => Action::Submit,
            Btn::New => Action::NewAccount,
            Btn::ChgPw => Action::ChangePassword,
            Btn::Close => Action::Quit,
            Btn::MsgOk => Action::Dismiss,
            Btn::SignupOk => Action::SubmitSignup,
            Btn::SignupCancel => Action::CancelSignup,
        };
        let _ = l; // 版式现在只用于"焦点/输入框"，按钮那部分已归引擎
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
}

/// 按**字符数**折行（8x8 字体下"字符数"就是像素宽 / 8）。
/// 按**实测宽度**折行：贪心塞字，塞不下就换行；`\n` 是硬换行。
///
/// 为什么取一个 `width_of` 闭包而不是 `&TextCache`：这样它能**脱离 SDL 单测**
///（真字体要建纹理，测折行逻辑不该拉上那套）。调用处传 `|s| text.width(s)`。
pub(crate) fn wrap_px(width_of: impl Fn(&str) -> f32, s: &str, max_w: f32) -> Vec<String> {
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
    /// 走一遍**真实的**按下 → 抬起（含控件引擎的 OnClick 判定）
    /// ⇒ 拖出去松开就不算点中，跟游戏里那条路是同一套。
    fn tap(s: &mut Login, l: &Layout, down_at: (f32, f32), up_at: (f32, f32)) -> Action {
        let mut ui = crate::widget::Ui::new();
        let down_design = crate::space::DPt::from_xy(down_at.0, down_at.1);
        let up_design = crate::space::DPt::from_xy(up_at.0, up_at.1);
        let down_canvas = crate::ui::ui_pt(down_design);
        let up_canvas = crate::ui::ui_pt(up_design);
        ui.set_mouse(down_canvas);
        s.on_down(down_design, l, &mut ui);
        match ui.release(up_canvas) {
            Some(crate::Press::Login(b)) => s.click(b, l),
            _ => Action::None,
        }
    }

    use super::*;

    /// Tab 切焦点、退格删自己那个框、回车提交。
    #[test]
    fn 键盘交互() {
        let mut l = Login::new();
        l.on_text("hero");
        assert_eq!(l.account, "hero");
        assert_eq!(l.on_key(crate::app_state::SceneKey::Tab), Action::None);
        assert_eq!(l.focus, 1);
        l.on_text("pw");
        assert_eq!(l.password, "pw");
        assert_eq!(
            l.on_key(crate::app_state::SceneKey::Backspace),
            Action::None
        );
        assert_eq!(l.password, "p", "退格只能删当前框");
        assert_eq!(l.account, "hero");
        assert_eq!(l.on_key(crate::app_state::SceneKey::Enter), Action::Submit);
        assert_eq!(l.on_key(crate::app_state::SceneKey::Escape), Action::Quit);
    }

    /// 长度上限照原版（用户名 14、密码 10），且**只收 ASCII**。
    #[test]
    fn 输入上限与字符集() {
        let mut l = Login::new();
        l.on_text("0123456789abcdefGHIJ");
        assert_eq!(l.account.chars().count(), Art::MAX_ACCOUNT);
        l.on_key(crate::app_state::SceneKey::Tab);
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
        assert_eq!(l.on_key(crate::app_state::SceneKey::Tab), Action::None);
        assert_eq!(l.focus, 0, "弹窗时不切焦点");
        l.on_text("hero");
        assert!(l.account.is_empty(), "弹窗时不能往框里打字");
        assert_eq!(l.on_key(crate::app_state::SceneKey::Enter), Action::Dismiss);
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
        assert_eq!(tap(&mut s, &l, c, c), Action::Dismiss);
        assert!(s.error.is_none(), "点了[确定]必须把弹窗清掉");
        // 弹窗开着时点别处：不该误关（原版 `DMessageDlg` 只认它那颗按钮）
        s.error = Some("再来一次".into());
        let out = (2.0, 2.0);
        assert_eq!(tap(&mut s, &l, out, out), Action::None);
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
        assert_eq!(
            l.on_key(crate::app_state::SceneKey::Enter),
            Action::None,
            "账号太短不该提交"
        );
        assert!(l.error.is_some(), "该弹模态框说原因");
        l.error = None;
        // 口令太短
        l.account = "newbie".into();
        l.password = "pw".into();
        l.confirm = "pw".into();
        assert_eq!(l.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(l.error.is_some());
        l.error = None;
        // 两次不一致
        l.password = "pw123".into();
        l.confirm = "pw124".into();
        assert_eq!(l.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(l.error.is_some());
        l.error = None;
        // 都对 ⇒ 提交（发送是 main 的事）
        l.confirm = "pw123".into();
        assert_eq!(
            l.on_key(crate::app_state::SceneKey::Enter),
            Action::SubmitSignup
        );
    }

    /// 建号面板：三个框的焦点轮转、确认框能打字、"取消"回登录。
    #[test]
    fn 建号面板焦点与取消() {
        let mut l = Login::new();
        l.enter_signup();
        assert_eq!(l.focus, 0);
        l.on_key(crate::app_state::SceneKey::Tab);
        l.on_key(crate::app_state::SceneKey::Tab);
        assert_eq!(l.focus, 2, "建号面板有三个框");
        // 焦点在确认框时，打字进的是确认框
        l.on_text("a");
        assert_eq!((l.password.as_str(), l.confirm.as_str()), ("", "a"));
        l.on_key(crate::app_state::SceneKey::Tab);
        assert_eq!(l.focus, 0, "转回第一个");

        // ESC = 关掉面板回登录（不是退出程序）
        l.password = "x".into();
        l.confirm = "x".into();
        assert_eq!(
            l.on_key(crate::app_state::SceneKey::Escape),
            Action::CancelSignup
        );
        assert!(!l.signup(), "该切回登录面板");
        assert!(
            l.password.is_empty() && l.confirm.is_empty(),
            "回登录要清掉建号那两格"
        );
        assert_eq!(
            l.on_key(crate::app_state::SceneKey::Escape),
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
        assert_eq!(tap(&mut s, &l, c, c), Action::NewAccount);
        assert!(s.signup(), "面板该切到建号");
        // 点 [取消] ⇒ 回登录
        let c = (l.signup_cancel.x + 10.0, l.signup_cancel.y + 10.0);
        assert_eq!(tap(&mut s, &l, c, c), Action::CancelSignup);
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
        assert_eq!(tap(&mut s, &l, c, c), Action::Submit);
        // 按下之后滑出去再抬起 = 不算点中
        assert_eq!(tap(&mut s, &l, c, (l.ok.x - 50.0, c.1)), Action::None);
        // 点输入框只改焦点，不产生动作
        s.on_down(
            crate::space::DPt::from_xy(l.password.x + 2.0, l.password.y + 2.0),
            &l,
            &mut crate::widget::Ui::new(),
        );
        assert_eq!(s.focus, 1);
        assert_eq!(
            tap(
                &mut s,
                &l,
                (l.password.x + 2.0, l.password.y + 2.0),
                (l.password.x + 2.0, l.password.y + 2.0)
            ),
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

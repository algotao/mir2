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

use crate::ui::UiCache;
use crate::{center_x, fill, text, C_ACTIVE, C_BG, C_DIM, C_ERR, C_FIELD, C_OK, C_TEXT};

/// 光标闪烁周期（原版是系统 `TEdit` 的光标；这里 500ms 闪一下）。
const CARET_MS: u128 = 500;

/// 界面上的动作（按键或点按产生；由 `main` 决定"接下来干什么"）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    None,
    /// 提交登录（`[提交]` 或回车）。
    Submit,
    /// 新建账号（原版开 `DLoginNew` 对话框；那条链还没接）。
    NewAccount,
    /// 修改密码（原版开 `DLoginChgPw` 对话框；同样还没接）。
    ChangePassword,
    /// 关掉报错弹窗。
    Dismiss,
    /// 退出（`[X]` 或 ESC）。
    Quit,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Btn {
    Ok,
    New,
    ChgPw,
    Close,
    MsgOk,
}

/// 登录界面的状态。
pub struct Login {
    pub account: String,
    pub password: String,
    /// 0 = 用户名框，1 = 密码框。
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
            focus: 0,
            error: None,
            busy: false,
            opened_at: None,
            pressed: None,
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
        let (field, max) = if self.focus == 0 {
            (&mut self.account, Art::MAX_ACCOUNT)
        } else {
            (&mut self.password, Art::MAX_PASSWORD)
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
                self.focus = 1 - self.focus;
                Action::None
            }
            Keycode::Backspace => {
                if self.focus == 0 {
                    self.account.pop();
                } else {
                    self.password.pop();
                }
                Action::None
            }
            Keycode::Return | Keycode::KpEnter => Action::Submit,
            Keycode::Escape => Action::Quit,
            _ => Action::None,
        }
    }

    /// 鼠标按下：记下按住的是哪颗（画的时候 +1 位移）。
    ///
    /// ⚠️ 输入框没有"按下"状态 —— 原版也只是把焦点交给那个 `TEdit`。
    pub fn on_down(&mut self, p: (f32, f32), l: &Layout) {
        self.pressed = if self.error.is_some() {
            l.msg_ok.hit(p).then_some(Btn::MsgOk)
        } else if l.ok.hit(p) {
            Some(Btn::Ok)
        } else if l.new.hit(p) {
            Some(Btn::New)
        } else if l.chgpw.hit(p) {
            Some(Btn::ChgPw)
        } else if l.close.hit(p) {
            Some(Btn::Close)
        } else {
            if l.account.hit(p) {
                self.focus = 0;
            } else if l.password.hit(p) {
                self.focus = 1;
            }
            None
        };
    }

    /// 鼠标抬起：只有"按下与抬起落在同一颗"才算点中。
    pub fn on_up(&mut self, p: (f32, f32), l: &Layout) -> Action {
        let Some(btn) = self.pressed.take() else {
            return Action::None;
        };
        let (r, act) = match btn {
            Btn::Ok => (l.ok, Action::Submit),
            Btn::New => (l.new, Action::NewAccount),
            Btn::ChgPw => (l.chgpw, Action::ChangePassword),
            Btn::Close => (l.close, Action::Quit),
            Btn::MsgOk => (l.msg_ok, Action::Dismiss),
        };
        if r.hit(p) {
            act
        } else {
            Action::None
        }
    }

    /// 画一帧。素材缺失时画一行提示（**不静默黑屏**）。
    pub fn draw<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        ui: &mut UiCache<'a>,
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

        // ① 背景：800×600 的图铺在 1024×768 的窗口里，四周留底色
        let bg = (Art::BG.0, Art::BG.1);
        if let Some(sz) = ui.size(dir, bg.0, bg.1) {
            let (x, y) = Layout::bg_at(win, sz);
            ui.draw(canvas, tc, dir, bg.0, bg.1, x, y);
        }

        // ② 开门动画（登录成功后盖在背景上）
        if let Some(t0) = self.opened_at {
            let f = (t0.elapsed().as_millis() / Art::DOOR_MS as u128) as u32;
            let idx = Art::DOOR.1 + f.min(Art::DOOR_FRAMES - 1);
            // ⚠️ `ChrSel[23]` 在本套素材里是空壳（给不出图）⇒ 取不到就跳过这一帧，
            // 别退回去把"门口"画没了。
            if let Some(sz) = ui.size(dir, Art::DOOR.0, idx) {
                let (x, y) = Layout::bg_at(win, sz);
                ui.draw(canvas, tc, dir, Art::DOOR.0, idx, x, y);
            }
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
        self.field(canvas, l.account, &self.account, self.focus == 0, caret_on)?;
        let masked = "*".repeat(self.password.chars().count());
        self.field(canvas, l.password, &masked, self.focus == 1, caret_on)?;

        // ⑤ 按钮（按下时 +1 位移）
        let down = |b: Btn| self.pressed == Some(b);
        let mut put = |canvas: &mut WindowCanvas, idx: u32, r: Rect, pressed: bool| {
            let (dx, dy) = if pressed { (1.0, 1.0) } else { (0.0, 0.0) };
            ui.draw(canvas, tc, dir, Art::DIALOG.0, idx, r.x + dx, r.y + dy);
        };
        put(canvas, Art::BTN_OK.1, l.ok, down(Btn::Ok));
        put(canvas, Art::BTN_NEW.1, l.new, down(Btn::New));
        put(canvas, Art::BTN_CHGPW.1, l.chgpw, down(Btn::ChgPw));
        put(canvas, Art::BTN_CLOSE.1, l.close, down(Btn::Close));

        // ⑥ 报错弹窗（模态）
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
            for (i, line) in wrap(err, 48).iter().take(4).enumerate() {
                text(
                    canvas,
                    line,
                    l.msgbox.x + 28.0,
                    l.msgbox.y + 56.0 + i as f32 * 16.0,
                    C_TEXT,
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
fn wrap(s: &str, cols: usize) -> Vec<String> {
    let mut out = Vec::new();
    let mut cur = String::new();
    for ch in s.chars() {
        if cur.chars().count() >= cols {
            out.push(std::mem::take(&mut cur));
        }
        cur.push(ch);
    }
    if !cur.is_empty() {
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

    /// 点按：只有"按下与抬起在同一颗按钮"才算点中（原版也是这个语义）。
    #[test]
    fn 点按命中() {
        let l = {
            let mk = |c: &'static str, i: u32| match (c, i) {
                ("Prguse", 60) => Some((296, 254)),
                ("Prguse", 62) => Some((76, 33)),
                ("Prguse", 61) => Some((100, 32)),
                ("Prguse", 53) => Some((128, 33)),
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
}

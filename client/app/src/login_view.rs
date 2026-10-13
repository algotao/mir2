//! 登录界面绘制：把 Login 的纯状态投影到 SDL 资源。

use std::time::Instant;

use sdl3::render::{TextureCreator, WindowCanvas};

use mir2_core::login_ui::{Art, Layout, Rect};

use crate::font::{Rgb, TextCache};
use crate::login::{wrap_px, Btn, Login};
use crate::ui::{ui_inv_px, ui_px, uifill, UiCache};
use crate::{center_x, text, C_BG, C_ERR, C_FIELD};

/// 光标闪烁周期（原版是系统 `TEdit` 的光标；这里 500ms 闪一下）。
const CARET_MS: u128 = 500;
/// 弹窗文字的真字体配色。
const RGB_TEXT: Rgb = (255, 255, 255);
const RGB_SHADOW: Rgb = (0, 0, 0);
/// 次要提示与当前输入光标颜色。
const RGB_DIM: Rgb = (120, 132, 156);
const RGB_ACTIVE: Rgb = (255, 236, 140);

impl Login {
    /// 画一帧。素材缺失时画一行提示（**不静默黑屏**）。
    /// ⚠️ 参数名是 `texts` 而不是 `text`：本文件其余地方还要调那个**自由函数** `text()`
    ///（8×8 调试字体，画 ASCII 用），参数一旦也叫 `text` 就会把函数遮住（编译期报
    /// "expected function, found &mut TextCache" —— 踩过）。
    #[allow(clippy::too_many_arguments)]
    // 与 `font::TextCache::draw` / `ui::UiCache::draw` 同一情况：坐标+配色+画布就是这么多
    /// `widget_ui`：控件引擎（按钮的按下态问它，不再自存一份）。
    pub fn draw<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        ui: &mut UiCache<'a>,
        texts: &mut TextCache<'a>,
        tc: &'a TextureCreator<T>,
        asset_dir: &Option<std::path::PathBuf>,
        win: (u32, u32),
        started: Instant,
        // 控件引擎：按钮的按下态问它（`held`），不再自存一份 `pressed`
        widget_ui: &crate::widget::Ui,
    ) -> Result<(), sdl3::Error> {
        let winf = (win.0 as f32, win.1 as f32);
        uifill(canvas, 0.0, 0.0, winf.0, winf.1, C_BG)?;
        let Some(dir) = asset_dir else {
            let msg = "ASSETS NOT FOUND - SET MIR2_ASSET_DIR";
            // ⚠️ `text()` 是 8×8 调试字体，**直接画在画布上**；而 `winf` 是**设计空间**。
            // 不换算的话这句话会落在 (400,300) 而不是画布中心 (512,384)（偏 112/84px）。
            return text(
                canvas,
                msg,
                center_x(msg, 0.0, ui_px(crate::space::Dpx(winf.0)).0),
                ui_px(crate::space::Dpx(winf.1)).0 / 2.0,
                C_ERR,
            );
        };

        // 登录背景只有客户端清单要求的 1024 原生图 `ui1[2850]`；缺失由启动预检阻止。
        assert!(
            ui.draw_fullscreen(canvas, tc, dir, Art::BG_1024.0, Art::BG_1024.1),
            "必需素材 ui1[2850] 登录背景缺失"
        );
        let doc = crate::layout::layout_document();
        let Some(base_layout) = Layout::build(win, |lib, idx| ui.size(dir, lib, idx)) else {
            let msg = "LOGIN ART MISSING - NEED Prguse.wzl";
            return text(
                canvas,
                msg,
                center_x(msg, 0.0, ui_px(crate::space::Dpx(winf.0)).0),
                ui_px(crate::space::Dpx(winf.1)).0 / 2.0,
                C_ERR,
            );
        };
        let l = doc.login_layout(base_layout);

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
            // 帧 23 是清单外的原版空占位，避免向严格图库索取未打包图号。
            if idx != 23 && ui.size(dir, Art::DOOR.0, idx).is_some() {
                // ⚠️ 门是 496×361 的**局部覆盖**，位置照原版（`IntroScn.pas:845-846`），
                // 不是"居中贴整屏背景"。
                // 位置是**设计空间**的（`DOOR_AT` 量在 800×600 上），`ui.draw` 自己乘 1.28
                ui.draw(
                    canvas,
                    tc,
                    dir,
                    Art::DOOR.0,
                    idx,
                    doc.login_door().x,
                    doc.login_door().y,
                );
            }
            // 门在放：登录小窗（对话框/输入框/按钮/提示）**一律不画** —— 见上面的出处。
            return Ok(());
        }

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
            texts.draw_ui(
                canvas,
                tc,
                title,
                // ⚠️ 标题宽是**画布像素**、对话框宽是**设计空间** ⇒ 先把字宽除回去
                l.dialog.x
                    + (l.dialog.w - ui_inv_px(crate::space::Cpx(texts.width(title))).0) / 2.0,
                l.dialog.y + 60.0,
                RGB_TEXT,
                None,
            )?;
            for (label, r) in [("ID", l.account), ("PW", l.password), ("PW2", l.confirm)] {
                texts.draw_ui(canvas, tc, label, r.x - 26.0, r.y + 4.0, RGB_DIM, None)?;
            }
        }
        self.field(
            canvas,
            tc,
            texts,
            l.account,
            &self.account,
            self.focus == 0,
            caret_on,
        )?;
        let masked = "*".repeat(self.password.chars().count());
        self.field(
            canvas,
            tc,
            texts,
            l.password,
            &masked,
            self.focus == 1,
            caret_on,
        )?;
        if self.signup() {
            let masked = "*".repeat(self.confirm.chars().count());
            self.field(
                canvas,
                tc,
                texts,
                l.confirm,
                &masked,
                self.focus == 2,
                caret_on,
            )?;
        }

        // ⑤ 按钮（按下时 +1 位移）
        let down = |b: Btn| widget_ui.held() == Some(crate::Press::Login(b));
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
            // ⚠️ 这里两套单位必须分清：`lh` 及其步进是**画布像素**（按 17.9px 的光栅化字号
            // 算出来），而 `l.msgbox` 是**设计空间**的框 ⇒ 框宽/框高先 `ui_px` 换算再相除，
            // 否则"能塞几行"会算成 1.28 倍。
            let lh = texts.line_height().max(1.0);
            let max_lines =
                ((ui_px(crate::space::Dpx(l.msgbox.h - 96.0)).0 / lh).floor() as usize).max(1);
            let lines = wrap_px(
                |s| texts.width(s),
                err,
                ui_px(crate::space::Dpx(l.msgbox.w - 56.0)).0,
            );
            for (i, line) in lines.iter().take(max_lines).enumerate() {
                texts.draw(
                    canvas,
                    tc,
                    line,
                    ui_px(crate::space::Dpx(l.msgbox.x + 28.0)).0,
                    ui_px(crate::space::Dpx(l.msgbox.y + 56.0)).0 + i as f32 * lh,
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

        // ⑦ 底部提示（原版没有；这是开发查看器需要的可见性）
        //
        // ⚠️ 2026-10-11：**"正在连接服务器 + 三点循环"那行删掉了** —— 原版压根没有
        // 这行字，登录成功后直接是石门打开的动画 + 开门音效 + 淡出（我们已接了开门
        // 动画，`ChrSel[23+n]` 共 10 帧）⇒ 门在开就是"正在进"，不需要文字说明。
        if self.error.is_none() {
            let tip = "TAB 下一项   ENTER 登录";
            let w = texts.width(tip);
            texts.draw(
                canvas,
                tc,
                tip,
                (ui_px(crate::space::Dpx(winf.0)).0 - w) / 2.0,
                ui_px(crate::space::Dpx(winf.1)).0 - 44.0,
                RGB_DIM,
                Some(RGB_SHADOW),
            )?;
        }
        Ok(())
    }

    /// 一个输入框：黑底（原版 TEdit 的颜色）+ 白字 + 闪烁光标。
    ///
    /// ⚠️ 文字走**真字体**（`TextCache::draw_ui`，坐标是设计空间）—— 原来用的 8×8 调试字体
    /// 在 1.28 倍下既小又只认 ASCII，而原版这里本来就是 GDI 的系统字体
    ///（`ClFunc.pas:605`，见 `core::text` 的说明）。光标 x 按**实测字宽**推进。
    #[allow(clippy::too_many_arguments)]
    fn field<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        texts: &mut TextCache<'a>,
        r: Rect,
        shown: &str,
        focused: bool,
        caret_on: bool,
    ) -> Result<(), sdl3::Error> {
        uifill(canvas, r.x, r.y, r.w, r.h, C_FIELD)?;
        if focused && caret_on {
            texts.draw_ui(
                canvas,
                tc,
                "|",
                // 同上：光标按**设计空间**的字宽推进（`draw_ui` 会再乘回 1.28）
                r.x + 2.0 + ui_inv_px(crate::space::Cpx(texts.width(shown))).0,
                // ⚠️ 用 `FIELD_TEXT_TOP`（=1，居中）而不是写死 4：+4 会让文字底顶出框底
                r.y + mir2_core::login_ui::FIELD_TEXT_TOP,
                RGB_ACTIVE,
                None,
            )?;
        }
        texts.draw_ui(
            canvas,
            tc,
            shown,
            r.x + 3.0,
            r.y + mir2_core::login_ui::FIELD_TEXT_TOP,
            RGB_TEXT,
            None,
        )
    }
}

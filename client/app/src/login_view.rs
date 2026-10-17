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
/// 自绘建号面板的配色（石板底 + 暗金描边；与登录/选角的石质美术同一调性）。
const RGB_PANEL: sdl3::pixels::Color = sdl3::pixels::Color::RGB(22, 24, 30);
const RGB_PANEL_BORDER: sdl3::pixels::Color = sdl3::pixels::Color::RGB(112, 94, 60);
const RGB_PANEL_LINE: sdl3::pixels::Color = sdl3::pixels::Color::RGB(58, 52, 40);
/// 自绘建号按钮：石面（四段竖向渐变）+ 暗金边框 + 内高光/内暗边 + 投影。
const RGB_BTN_BANDS: [sdl3::pixels::Color; 4] = [
    sdl3::pixels::Color::RGB(88, 82, 68),
    sdl3::pixels::Color::RGB(76, 70, 58),
    sdl3::pixels::Color::RGB(62, 58, 48),
    sdl3::pixels::Color::RGB(48, 45, 37),
];
const RGB_BTN_EDGE: sdl3::pixels::Color = sdl3::pixels::Color::RGB(112, 96, 62);
const RGB_BTN_HI: sdl3::pixels::Color = sdl3::pixels::Color::RGB(146, 136, 110);
const RGB_BTN_LO: sdl3::pixels::Color = sdl3::pixels::Color::RGB(22, 20, 16);
const RGB_BTN_SHADOW: sdl3::pixels::Color = sdl3::pixels::Color::RGB(8, 8, 10);
const RGB_BTN_TEXT: Rgb = (236, 228, 204);

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
            if idx != 23 {
                let r = doc.login_door();
                if let Some((dw, dh)) = ui.size(dir, Art::DOOR.0, idx) {
                    // ⚠️ 这里**必须**走 `draw_ui_src`（dst 是设计空间矩形，内部 ×1.28），
                    // 不能用 `ui.draw`：`ui.draw` 会用"素材尺寸 ×1.28"当目标尺寸（= 把 800 版
                    // 门帧按 1.28 放大），而 1024 底图的门洞只有 1.15 倍 ⇒ 门比洞大、对不齐
                    //（用户 2026-10-17 报的）。`DOOR_SIZE` 已经把 1.15 的拟合比例算进去了。
                    let _ = ui.draw_ui_src(
                        canvas,
                        tc,
                        dir,
                        Art::DOOR.0,
                        idx,
                        (0.0, 0.0, dw as f32, dh as f32),
                        (r.x, r.y, r.w, r.h),
                    );
                }
            }
            // 门在放：登录小窗（对话框/输入框/按钮/提示）**一律不画** —— 见上面的出处。
            return Ok(());
        }

        // ③ 对话框：登录用 `Prguse[60]`；建号**自绘面板**（见 `draw_signup_panel`）。
        if self.signup() {
            self.draw_signup_panel(canvas, tc, texts, &l)?;
        } else {
            ui.draw(
                canvas,
                tc,
                dir,
                Art::DIALOG.0,
                Art::DIALOG.1,
                l.dialog.x,
                l.dialog.y,
            );
        }

        // ④ 输入框（原版是原生 TEdit：黑底白字 + 光标 ⇒ 自绘）
        let caret_on = (started.elapsed().as_millis() / CARET_MS).is_multiple_of(2);
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
            // 建号面板：[确定] / [取消] 是**自绘**的 —— 这套素材里原版那两颗
            //（`Prguse[51]/[52]`）印的是「CANCEL」/「取消」，会露出一颗英文钮
            //（用户 2026-10-17 报的"一个 cancel、一个取消"）。素材里没有同风格的
            // 「确定」，就按面板的石板风画一对，见 `draw_signup_btn`。
            //
            // ⚠️ 报错弹窗是**模态**的（只认它那颗 [确定]）⇒ 弹窗开着时不画这两颗，
            // 免得"底下还有一排按钮、点了没反应"（用户 2026-10-17 看到的两个按钮）。
            if self.error.is_none() {
                Self::draw_signup_btn(canvas, tc, texts, l.signup_ok, "确定", down(Btn::SignupOk))?;
                Self::draw_signup_btn(
                    canvas,
                    tc,
                    texts,
                    l.signup_cancel,
                    "取消",
                    down(Btn::SignupCancel),
                )?;
            }
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

    /// 建号面板的**自绘按钮**（[确定] / [取消]，2026-10-17）。
    ///
    /// # 为什么自绘
    ///
    /// 原版这两颗是 `Prguse[51]/[52]`，但**这套素材的 `51` 印的是英文「CANCEL」** ——
    /// 与 `52` 的「取消」并排出现，就是用户报的"一个 cancel、一个取消"
    ///（功能上 `51` 确实是确认，只是印字不对）。素材里没有同风格的「确定」，
    /// 同风格的只有 `CANCEL`/`取消` 这对 ⇒ 干脆照面板的石板风画一对，
    /// 尺寸仍取原版那两颗的 96×33（[`mir2_core::login_ui::Art::SIGNUP_BTN_W`]）。
    ///
    /// 按下 = 整体 +1 位移 + 高光/暗边对调（与原版 `TDButton` 的观感一致）。
    fn draw_signup_btn<'a, T>(
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        texts: &mut TextCache<'a>,
        r: Rect,
        label: &str,
        pressed: bool,
    ) -> Result<(), sdl3::Error> {
        let (dx, dy) = if pressed { (1.0, 1.0) } else { (0.0, 0.0) };
        let (x, y, w, h) = (r.x + dx, r.y + dy, r.w, r.h);
        // 投影（右下 1px，让按钮从面板上"浮"起来）
        uifill(canvas, x + 1.0, y + h, w - 1.0, 1.0, RGB_BTN_SHADOW)?;
        uifill(canvas, x + w, y + 1.0, 1.0, h - 1.0, RGB_BTN_SHADOW)?;
        // 石面：四段竖向渐变（1.28 倍放大后过渡够平滑，不必逐像素算）
        for (i, col) in RGB_BTN_BANDS.iter().enumerate() {
            let y0 = y + (h * i as f32 / 4.0).floor();
            let y1 = y + (h * (i as f32 + 1.0) / 4.0).floor();
            uifill(canvas, x, y0, w, y1 - y0, *col)?;
        }
        // 外框
        for (ex, ey, ew, eh) in [
            (x, y, w, 1.0),
            (x, y + h - 1.0, w, 1.0),
            (x, y, 1.0, h),
            (x + w - 1.0, y, 1.0, h),
        ] {
            uifill(canvas, ex, ey, ew, eh, RGB_BTN_EDGE)?;
        }
        // 内高光/内暗边：按下时对调（凹进去）
        let (hi, lo) = if pressed {
            (RGB_BTN_LO, RGB_BTN_HI)
        } else {
            (RGB_BTN_HI, RGB_BTN_LO)
        };
        uifill(canvas, x + 1.0, y + 1.0, w - 2.0, 1.0, hi)?;
        uifill(canvas, x + 1.0, y + 1.0, 1.0, h - 2.0, hi)?;
        uifill(canvas, x + 1.0, y + h - 2.0, w - 2.0, 1.0, lo)?;
        uifill(canvas, x + w - 2.0, y + 1.0, 1.0, h - 2.0, lo)?;
        // 文字居中（宽/高都先把画布像素除回设计空间）
        let tw = ui_inv_px(crate::space::Cpx(texts.width(label))).0;
        let th = ui_inv_px(crate::space::Cpx(texts.line_height())).0;
        texts.draw_ui(
            canvas,
            tc,
            label,
            x + (w - tw) / 2.0,
            y + (h - th) / 2.0,
            RGB_BTN_TEXT,
            Some(RGB_SHADOW),
        )
    }

    /// 建号面板的**自绘背景**（2026-10-13，用户指定"自绘一个背景图"）。
    ///
    /// # 为什么自绘
    ///
    /// 原版建号面板 `DNewAccount` 是另一张面板图，**本套素材里没有**；复用登录框
    /// `Prguse[60]` 会带着"登录"的标题印字串味。现在没有合适素材，就用色块画一块
    /// 石板风的面板（近黑底 + 暗金双层描边 + 标题分隔线），标题与字段标签走真字体。
    ///
    /// ⚠️ **过渡品**：注册与改密码以后会移到网页端（用户 2026-10-13 定的方向），
    /// 这块自绘只求"看得清楚、不串味"，别再往上面投入。
    ///
    /// 几何完全沿用 `l.dialog`（296×254 设计空间、居中）与三个输入框的位置，
    /// 命中测试（`login.rs`）不用改一个字。
    fn draw_signup_panel<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        texts: &mut TextCache<'a>,
        l: &Layout,
    ) -> Result<(), sdl3::Error> {
        let d = l.dialog;
        // 底板
        uifill(canvas, d.x, d.y, d.w, d.h, RGB_PANEL)?;
        // 外描边（暗金，2px）：四条边各画一次
        let edges = [
            (d.x, d.y, d.w, 2.0),
            (d.x, d.y + d.h - 2.0, d.w, 2.0),
            (d.x, d.y + 2.0, 2.0, d.h - 4.0),
            (d.x + d.w - 2.0, d.y + 2.0, 2.0, d.h - 4.0),
        ];
        for (x, y, w, h) in edges {
            uifill(canvas, x, y, w, h, RGB_PANEL_BORDER)?;
        }
        // 内细线（1px，留 3px 深缝）：描边里再勾一圈
        let inset = 4.0;
        uifill(
            canvas,
            d.x + inset,
            d.y + inset,
            d.w - inset * 2.0,
            1.0,
            RGB_PANEL_LINE,
        )?;
        uifill(
            canvas,
            d.x + inset,
            d.y + d.h - inset - 1.0,
            d.w - inset * 2.0,
            1.0,
            RGB_PANEL_LINE,
        )?;
        uifill(
            canvas,
            d.x + inset,
            d.y + inset,
            1.0,
            d.h - inset * 2.0,
            RGB_PANEL_LINE,
        )?;
        uifill(
            canvas,
            d.x + d.w - inset - 1.0,
            d.y + inset,
            1.0,
            d.h - inset * 2.0,
            RGB_PANEL_LINE,
        )?;
        // 标题 + 分隔线
        let title = "创建新用户";
        // ⚠️ 字宽是**画布像素**、面板是**设计空间** ⇒ 先 `ui_inv_px` 除回去
        let tw = ui_inv_px(crate::space::Cpx(texts.width(title))).0;
        texts.draw_ui(
            canvas,
            tc,
            title,
            d.x + (d.w - tw) / 2.0,
            d.y + 24.0,
            RGB_ACTIVE,
            Some(RGB_SHADOW),
        )?;
        uifill(
            canvas,
            d.x + 16.0,
            d.y + 52.0,
            d.w - 32.0,
            1.0,
            RGB_PANEL_LINE,
        )?;
        // 字段标签：右对齐到输入框左侧留 8px（真字体，与输入框文字同一行高）
        for (label, r) in [
            ("账号", l.account),
            ("密码", l.password),
            ("确认密码", l.confirm),
        ] {
            let w = ui_inv_px(crate::space::Cpx(texts.width(label))).0;
            texts.draw_ui(
                canvas,
                tc,
                label,
                r.x - 8.0 - w,
                r.y + mir2_core::login_ui::FIELD_TEXT_TOP,
                RGB_DIM,
                None,
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

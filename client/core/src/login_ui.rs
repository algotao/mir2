//! 登录界面的**版式**：纯数据 + 纯计算，**不含任何绘制**。
//!
//! 放在 `core` 而不是 `app` 的理由与地图/actor 一样：**版式是游戏知识**
//! （"原版登录框长什么样"），且它必须能被 `client/e2e` 用上 —— 那边没有 SDL，
//! 却能按同一份版式合成一张图给人看（`cargo run -p mir2-e2e -- login -o x.png`）。
//! 两处共用一份 → "app 里看着对"与"e2e 合成出来对"不可能是两回事。
//!
//! # 出处（原版 `mir2standard/GameOfMir/MirClient/`）
//!
//! | 东西 | 位置 | 出处 |
//! |---|---|---|
//! | 对话框 `Prguse[60]`，**屏幕居中** | `(SCRW-w)/2, (SCRH-h)/2` | `FState.pas:771-775` |
//! | [提交] `Prguse[62]` | 对话框 +(169,163) | `FState.pas:790-792` |
//! | [新用户] `Prguse[61]` | 对话框 +(25,207) | `FState.pas:786-789` |
//! | [X] `Prguse[64]` | 对话框 +(252,28) | `FState.pas:796-798` |
//! | 用户名框 | 对话框 +(98,85)，112×16，≤14 字 | `IntroScn.pas:269,552-554` |
//! | 密码框 | 对话框 +(98,117)，112×16，≤10 字 | `IntroScn.pas:283,559-565` |
//! | 背景 `ChrSel[22]` | 屏幕居中（800×600 的图） | `IntroScn.pas:886-889` |
//! | 开门动画 `ChrSel[23..32]` | 10 帧 × 300ms | `IntroScn.pas:894-914` |
//! | 报错弹窗 `Prguse[360]` + `[363]` | 屏幕居中 | `FState.pas:750-760` |
//!
//! ⚠️ 输入框坐标原版写的是**相对屏幕中心**（`Left := cx-50; Top := cy-42 / cy-10`，
//! `cx/cy = SCREENWIDTH/2, SCREENHEIGHT/2`）。800×600 与 1024×768 各算一遍，
//! 换算成对话框内偏移**都是** `(98,85)` / `(98,117)` —— 所以这里直接存对话框内偏移，
//! 分辨率无关（本窗口 1024×768，而素材是 800×600 时代的，这层换算必须对）。

/// 素材编号：**原版写死的常量**，不是算出来的（所以它属于"规格"，与公式分开）。
pub struct Art;

impl Art {
    /// 旧 800×600 登录背景，保留作素材对照/历史测试；正式客户端不回退使用，且不进入有效包。
    pub const BG: (&'static str, u32) = ("ChrSel", 22);
    /// 登录场景背景的 **1024 原生版**（`ui1[2850]`，1024×768）。
    ///
    /// 2026-10-12 换上：与 `BG` **同一套构图**（石门 + 两侧火把 + 门楣），只是按
    /// 1024×768 重画 ⇒ 直接 1:1 铺满画布，**不再拉伸**（拉伸那版是 `ui::UI_SCALE` 的
    /// 1.28 倍线性放大，非整数倍 ⇒ 糊，见 `app/src/ui.rs` 的 `UI_SCALE` 说明）。
    ///
    /// 该图是客户端必需素材，在 `image-libraries.tsv` 与启动 preflight 中登记；缺失必须报错，
    /// 不回退使用旧的 800 图。
    pub const BG_1024: (&'static str, u32) = crate::image_manifest::assets::LOGIN_BACKGROUND.pair();
    /// 开门动画的首帧；`+ k`（k = 0..9）取第 k 帧。
    pub const DOOR: (&'static str, u32) = ("ChrSel", 23);
    /// 开门动画帧数。
    pub const DOOR_FRAMES: u32 = 10;
    /// 开门动画每帧毫秒（`IntroScn.pas:894-899`）。
    pub const DOOR_MS: u32 = 300;
    /// 登录对话框。
    pub const DIALOG: (&'static str, u32) = ("Prguse", 60);
    /// [提交]。
    pub const BTN_OK: (&'static str, u32) = ("Prguse", 62);
    /// [新用户]。
    pub const BTN_NEW: (&'static str, u32) = ("Prguse", 61);
    /// [修改密码]。
    pub const BTN_CHGPW: (&'static str, u32) = ("Prguse", 53);
    /// 建号面板 [确定] / [取消] 的**设计空间尺寸**（2026-10-17 起自绘，见下）。
    ///
    /// ⚠️ 不再用 `Prguse[51]/[52]`：原版那两颗是 `DNewAccountOk/Cancel`，但**这套素材里
    /// `51` 印的是英文「CANCEL」**（与 `52` 的「取消」并排出现 ⇒ 用户看到
    /// "一个 cancel、一个取消"）。素材里没有同风格的「确定」，所以这两颗改成自绘
    ///（见 `app::login_view::draw_signup_btn`），尺寸取原版那两颗的 96×33/34。
    ///
    /// 原版建号面板还有一颗 [X]（图号 83），但**本套素材里它是空壳**（0×0）——
    /// 所以关掉建号面板仍用登录框自己那颗 [X]（`BTN_CLOSE`）。
    pub const SIGNUP_BTN_W: f32 = 96.0;
    pub const SIGNUP_BTN_H: f32 = 33.0;
    /// [X]。
    pub const BTN_CLOSE: (&'static str, u32) = ("Prguse", 64);
    /// 通用消息框背景。
    pub const MSGBOX: (&'static str, u32) = ("Prguse", 360);
    /// 消息框的 [Ok]。
    pub const MSGBOX_OK: (&'static str, u32) = ("Prguse", 363);

    /// 输入框最多几个字符（`IntroScn.pas:269/283`）。
    pub const MAX_ACCOUNT: usize = 14;
    pub const MAX_PASSWORD: usize = 10;
}

/// 按钮/输入框在**对话框内**的偏移（`FState.pas:786-798`；`+1` 是原版按下位移的基准）。
const BTN_OK_AT: (f32, f32) = (169.0, 163.0);
const BTN_NEW_AT: (f32, f32) = (25.0, 207.0);
const BTN_CLOSE_AT: (f32, f32) = (252.0, 28.0);
const BTN_CHGPW_AT: (f32, f32) = (130.0, 207.0);
const FIELD_X: f32 = 98.0;
/// 三行输入框的**框顶**（对话框内偏移）—— **有出处**，别凭观感改。
///
/// `IntroScn.pas:552-565`（登录场景 `OpenScene` 里给那两个原生 `TEdit` 定位）：
///
/// ```text
/// cx := SCREENWIDTH  div 2;   cy := SCREENHEIGHT div 2;
/// m_EdId     : Left := cx-50; Top := cy-42; Height := 16; Width := 112;   // 用户名
/// m_EdPasswd : Left := cx-50; Top := cy-10; Height := 16; Width := 112;   // 密码
/// ```
///
/// 两处都按"屏幕居中"算，而对话框也是居中的 ⇒ 折进对话框内就是 `85` / `117`（差 32），
/// 与我们的一致（`cy-42` 折进 `(600-254)/2=173` 得 85）。
///
/// ⚠️ 2026-10-09 用户报"用户名框偏下 4~5 屏幕像素、密码偏下 2 像素"。核对下来**框顶没错**
/// —— 偏的是**框里的字**（见 [`FIELD_TEXT_TOP`]）：字号 14（画布 17.92px）却按 `+4` 落笔在
/// 高 16（画布 20.5px）的框里，文字底顶出框底 ⇒ 整块看着往下坠。
const FIELD_TOP_A: f32 = 85.0;
const FIELD_TOP_P: f32 = 117.0;
/// 建号面板的"确认口令"框。
///
/// ⚠️ 这一格**没有原版出处**：原版建号面板（`DNewAccount`）是另一张面板图 + 十几个字段
///（`IntroScn.pas:929-1067` 只做焦点流转，`Left/Top` 在编译过的 `FState.dfm` 里读不出来），
/// 我们只留 3 个字段 ⇒ 沿用登录框的行距（85/117/149，每行 32px），落在底下那排按钮
///（y=207）之上、不与它们相撞。
const FIELD_TOP_C: f32 = 149.0;
const FIELD_W: f32 = 112.0;
const FIELD_H: f32 = 16.0;
/// 框内文字的**顶**（相对框顶）—— **别写死 4**。
///
/// 框高 `FIELD_H = 16`（设计空间），字号 `UI_PX = 14`（画布上 17.92px，框 20.5px）⇒
/// 居中落笔是 `(16 - 17.92/1.28) / 2 ≈ 1.0`。
///
/// ⚠️ 原来是 `+4.0`：文字底会**顶出框底**，视觉上整块往下坠 —— 用户 2026-10-09 报的
/// "用户名输入框偏下 4~5 像素、密码偏下 2 像素"就是它（框顶本身与 `IntroScn.pas` 一致）。
pub const FIELD_TEXT_TOP: f32 = 1.0;

/// 一个矩形（屏幕绝对坐标）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Rect {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
}

impl Rect {
    pub fn hit(&self, (px, py): (f32, f32)) -> bool {
        px >= self.x && px < self.x + self.w && py >= self.y && py < self.y + self.h
    }
}

/// 一帧的版式（全部屏幕绝对坐标）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Layout {
    pub dialog: Rect,
    pub ok: Rect,
    pub new: Rect,
    /// [修改密码]（原版 `FState.pas:793-795`：`129+1, 206+1`）。
    pub chgpw: Rect,
    pub close: Rect,
    pub account: Rect,
    pub password: Rect,
    /// 建号面板的"确认口令"框（只在建号模式画；见 `FIELD_TOP_C` 的说明）。
    pub confirm: Rect,
    /// 建号面板的 [确定]（原版 `DNewAccountOk`）。
    pub signup_ok: Rect,
    /// 建号面板的 [取消]。
    pub signup_cancel: Rect,
    pub msgbox: Rect,
    pub msg_ok: Rect,
}

impl Layout {
    /// 按窗口尺寸与**实测到的素材尺寸**算版式。
    ///
    /// `measure` 由调用方给（`Wzl::record` 只读容器头、不解压像素 ⇒ 便宜到可以每帧调）。
    /// 任何一张素材缺失就整体 `None` —— 宁可画一行"素材缺失"，也不画一个歪掉的登录框。
    pub fn build(
        win: (u32, u32),
        mut measure: impl FnMut(&'static str, u32) -> Option<(u32, u32)>,
    ) -> Option<Layout> {
        let (cw, ch) = (win.0 as f32, win.1 as f32);
        let (dw, dh) = measure(Art::DIALOG.0, Art::DIALOG.1)?;
        let dialog = Rect {
            x: (cw - dw as f32) / 2.0,
            y: (ch - dh as f32) / 2.0,
            w: dw as f32,
            h: dh as f32,
        };
        // 按钮：位置 = "对话框 + 偏移"，尺寸照素材（三颗大小都不同，不能写死）。
        // ⚠️ 不抽成闭包：闭包要可变借 `measure`，三次调用之间会互相打架（借检查器不让），
        // 而这里只有三颗按钮 —— 摊开写反而最清楚。
        let (okw, okh) = measure(Art::DIALOG.0, Art::BTN_OK.1)?;
        let (nww, nwh) = measure(Art::DIALOG.0, Art::BTN_NEW.1)?;
        let (clw, clh) = measure(Art::DIALOG.0, Art::BTN_CLOSE.1)?;
        let (cpw, cph) = measure(Art::DIALOG.0, Art::BTN_CHGPW.1)?;
        let (mw, mh) = measure(Art::MSGBOX.0, Art::MSGBOX.1)?;
        // ⚠️ 名字必须与上面那组区分开：同叫 `okw/okh` 的话后者会**遮蔽**前者，
        // 于是"提交"按钮会拿到消息框 [Ok] 的尺寸（80×34 而不是 76×33）——
        // 第一次这么写就被编译器的 unused 警告抓住了。
        // 建号面板那两颗是**自绘**的（见 `Art::SIGNUP_BTN_W/H`），不依赖素材。
        let (mokw, mokh) = measure(Art::MSGBOX_OK.0, Art::MSGBOX_OK.1)?;
        let msgbox = Rect {
            x: (cw - mw as f32) / 2.0,
            y: (ch - mh as f32) / 2.0,
            w: mw as f32,
            h: mh as f32,
        };
        Some(Layout {
            dialog,
            ok: Rect {
                x: dialog.x + BTN_OK_AT.0,
                y: dialog.y + BTN_OK_AT.1,
                w: okw as f32,
                h: okh as f32,
            },
            new: Rect {
                x: dialog.x + BTN_NEW_AT.0,
                y: dialog.y + BTN_NEW_AT.1,
                w: nww as f32,
                h: nwh as f32,
            },
            chgpw: Rect {
                x: dialog.x + BTN_CHGPW_AT.0,
                y: dialog.y + BTN_CHGPW_AT.1,
                w: cpw as f32,
                h: cph as f32,
            },
            close: Rect {
                x: dialog.x + BTN_CLOSE_AT.0,
                y: dialog.y + BTN_CLOSE_AT.1,
                w: clw as f32,
                h: clh as f32,
            },
            account: Rect {
                x: dialog.x + FIELD_X,
                y: dialog.y + FIELD_TOP_A,
                w: FIELD_W,
                h: FIELD_H,
            },
            password: Rect {
                x: dialog.x + FIELD_X,
                y: dialog.y + FIELD_TOP_P,
                w: FIELD_W,
                h: FIELD_H,
            },
            confirm: Rect {
                x: dialog.x + FIELD_X,
                y: dialog.y + FIELD_TOP_C,
                w: FIELD_W,
                h: FIELD_H,
            },
            // [确定]/[取消] 摆在原版 [新用户]/[修改密码] 那两个槽位上（同一排，
            // 25/130 + 207）—— 建号模式下那两颗不画，位置正好空出来。
            signup_ok: Rect {
                x: dialog.x + BTN_NEW_AT.0,
                y: dialog.y + BTN_NEW_AT.1,
                w: Art::SIGNUP_BTN_W,
                h: Art::SIGNUP_BTN_H,
            },
            signup_cancel: Rect {
                x: dialog.x + BTN_CHGPW_AT.0,
                y: dialog.y + BTN_CHGPW_AT.1,
                w: Art::SIGNUP_BTN_W,
                h: Art::SIGNUP_BTN_H,
            },
            // ⚠️ 消息框的 [Ok] 位置**不是查证过的数字**：`FState.pas:750-760` 只列了素材，
            // 没给偏移。这里按"框底居中、留 8px"推 —— 与登录框那几颗按钮的可靠性不同。
            msg_ok: Rect {
                x: msgbox.x + (msgbox.w - mokw as f32) / 2.0,
                y: msgbox.y + msgbox.h - mokh as f32 - 8.0,
                w: mokw as f32,
                h: mokh as f32,
            },
            msgbox,
        })
    }

    /// 开门动画相对 800×600 画布的偏移。
    ///
    /// # ⚠️ 这个数字是**量出来的**，不是原版那两个字面量（2026-10-08 修正）
    ///
    /// ⚠️ 出处引**现行**那一行（`IntroScn.pas:914`），不是它上面被注释掉的旧写法：
    ///
    /// ```text
    /// // MSurface.Draw ((SCREENWIDTH - 800) div 2 + 252{152}, (SCREENHEIGHT - 600) div 2 + 106{96}, ...);  ← 已注释
    /// MSurface.Draw ((SCREENWIDTH - d.Width) div 2 + 1, (SCREENHEIGHT - d.Height) div 2 - 23, ...);  // Modified by Davy 2019-11-13
    /// ```
    ///
    /// 按现行公式、`d` = 496×361、画布 800×600 ⇒ `(800-496)/2+1 = 153`、
    /// `(600-361)/2-23 = 96.5` —— 与这里量出来的 `(153, 96)` 只差半像素（原版注释里的
    /// `+252 / +106` 是更早的写法，那才是"写给另一套素材"的那个）。
    ///
    /// 早先这段注释把出处引到被注释掉的旧行上，会让人以为我们和原版不一致。
    /// **我方的 `ChrSel[22]`（登录背景）是另一版**：门口更靠左。这是与 D-27 / D-42
    /// 同一类问题（**素材不是那一套** ⇒ 坐标必须重测）。照抄 252 的表现是
    /// **门整体右偏约 100px**（用户报的"门打开动画的位置偏了"）。
    ///
    /// 量法：把**关门那一帧**（`ChrSel[24]`，496×361）当模板在 `ChrSel[22]` 上滑
    ///（先 1/8 降采样粗筛、再全分辨率细化，只算不透明像素）。实测最小差 **12.2**
    /// 在 `(153,96)`，四邻都是 13.0~13.5（判别度 ~1.0）；而原版的 `(252,106)` 是 **23.6**、
    /// `(0,0)` 是 26.0 —— 差得很干净。用门上的两处恶魔脸浮雕独立复核得 `(149,96)`/`(145,88)`
    /// （同一侧，±5px 内）。**仅作历史对照**，现行判据见 `真素材_门帧贴合1024底图`。
    ///
    /// ⚠️ 门**不是**整屏背景：帧是 **496×361** 的局部覆盖（实测 `ChrSel[24..32]`），
    /// 按"居中贴"画会把它糊到屏幕正中间去（踩过）。
    ///
    /// ⚠️ 2026-10-17 重标。门动画帧是 800 版美术（496×361），登录底图却是 1024 重绘版
    /// （`ui1[2850]`）—— **两者的门洞不是严格 1.28 倍关系**。按 1.28 直接画，门框会
    /// 明显压到门洞外（用户报的"开门动画与背景对不齐"）。用整帧 NCC 在 1024 底图上重搜：
    /// scale **1.15**、原生落点 **(212,128)**（NCC 0.716，旧口径 1.28/(196,123) 明显更差），
    /// 折设计空间就是 `212/1.28`。旧 800 底图上的 (153,96) 只作历史对照
    ///（`真素材_门帧与800底图对齐` 仍在用）。
    pub const DOOR_AT: (f32, f32) = (212.0 / 1.28, 128.0 / 1.28);
    /// 门帧在设计空间里的**绘制**尺寸 = 原生 496×361 × 1.15 ÷ 1.28。
    ///
    /// ⚠️ 别再拿素材尺寸（`measure`）当绘制尺寸 —— 素材是 1024 原生像素，直接 ×1.28 画
    /// 就是"门比洞大"。
    pub const DOOR_SIZE: (f32, f32) = (496.0 * 1.15 / 1.28, 361.0 * 1.15 / 1.28);

    // ⚠️ 这里原来还有 `bg_at` / `legacy_at` 两个"把 800×600 居中摆进窗口"的换算 ——
    // 2026-10-09 删掉（D-52）：现在这套界面**整屏拉伸铺满**画布（跟官方一样），
    // 版式一律活在 800×600 的**设计空间**里，由 app 的 `ui::UI_SCALE` 一次乘到画布上。
    // ⇒ 调用方给 `Layout::build` 的"窗口尺寸"其实就是 `UI_WIN = (800, 600)`。
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 一份"照真素材量出来的"尺寸表（`Prguse[60]` 296×254 等，见 docs/assets.md）。
    /// 用它单测版式：**不需要真素材**，也就能在没装素材的机器上跑。
    fn fake(container: &'static str, idx: u32) -> Option<(u32, u32)> {
        match (container, idx) {
            ("Prguse", 60) => Some((296, 254)),
            ("Prguse", 62) => Some((76, 33)),
            ("Prguse", 61) => Some((100, 32)),
            ("Prguse", 64) => Some((16, 23)),
            ("Prguse", 53) => Some((128, 33)),
            ("Prguse", 360) => Some((452, 179)),
            ("Prguse", 363) => Some((80, 34)),
            _ => None,
        }
    }

    /// 按钮与输入框必须落在**对话框内** —— 这是"照原版"最容易错的地方
    /// （素材是 800×600 时代的，窗口是 1024×768）。
    #[test]
    fn 版式落在对话框内() {
        for win in [(800u32, 600u32), (1024, 768), (1280, 960)] {
            let l = Layout::build(win, fake).expect("素材齐全");
            let inside = |r: &Rect| {
                r.x >= l.dialog.x
                    && r.y >= l.dialog.y
                    && r.x + r.w <= l.dialog.x + l.dialog.w
                    && r.y + r.h <= l.dialog.y + l.dialog.h
            };
            for (name, r) in [
                ("[提交]", l.ok),
                ("[新用户]", l.new),
                ("[修改密码]", l.chgpw),
                ("[X]", l.close),
                ("用户名框", l.account),
                ("密码框", l.password),
                ("确认口令框", l.confirm),
                ("建号[确定]", l.signup_ok),
                ("建号[取消]", l.signup_cancel),
            ] {
                assert!(inside(&r), "{win:?} 里 {name} 跑到对话框外面了: {r:?}");
            }
            // 对话框居中（原版就是 `(SCRW - w) / 2`）
            assert_eq!(l.dialog.x, (win.0 as f32 - l.dialog.w) / 2.0);
            assert_eq!(l.dialog.y, (win.1 as f32 - l.dialog.h) / 2.0);
            // 两框同 x 同宽、密码在下面（原版两处都写 `cx-50` 与 112）
            assert_eq!((l.account.x, l.account.w), (l.password.x, l.password.w));
            assert!(l.password.y > l.account.y);
            // 建号两颗是**自绘**的固定尺寸，不再依赖 `Prguse[51]/[52]`
            //（这套素材的 51 印的是英文 CANCEL，用户 2026-10-17 报的）
            assert_eq!(
                (l.signup_ok.w, l.signup_ok.h),
                (Art::SIGNUP_BTN_W, Art::SIGNUP_BTN_H)
            );
            assert_eq!(
                (l.signup_cancel.w, l.signup_cancel.h),
                (Art::SIGNUP_BTN_W, Art::SIGNUP_BTN_H)
            );
            assert!(
                l.signup_ok.x + l.signup_ok.w <= l.signup_cancel.x,
                "建号 [确定]/[取消] 叠在一起了"
            );
        }
    }

    /// 开门动画的位置/尺寸都是**设计空间**的，且**不是**把素材按 1.28 放大。
    ///
    /// 踩过的两个坑：① 按"居中"画，门会跑到屏幕正中间；② 用 `ui.draw`
    ///（素材尺寸 ×1.28）画，800 版门帧会比 1024 底图的门洞大一圈（用户 2026-10-17 报的）。
    #[test]
    fn 门框按1024底图拟合而不是素材原尺寸() {
        let (x, y) = Layout::DOOR_AT;
        let (w, h) = Layout::DOOR_SIZE;
        assert!((0.0..800.0).contains(&x) && (0.0..600.0).contains(&y));
        // 画到画布后的实际尺寸 = 设计 ×1.28 = 素材 ×1.15
        assert!((w * 1.28 - 496.0 * 1.15).abs() < 0.05);
        assert!((h * 1.28 - 361.0 * 1.15).abs() < 0.05);
        assert!(w * 1.28 < 496.0 * 1.28, "不能退回素材 ×1.28");
        // 不是"居中贴"
        let centered = ((800.0 - w) / 2.0, (600.0 - h) / 2.0);
        assert!(
            (y - centered.1).abs() > 20.0,
            "y 该明显偏离居中：{y} vs {}",
            centered.1
        );
    }

    /// **真素材验收**：门帧要贴在 **1024 登录底图**（`ui1[2850]`）的门洞上。
    ///
    /// 用户 2026-10-17 报"开门动画与背景对不齐"：旧口径把 800 版门帧按素材原尺寸 ×1.28
    /// 画在 (196,123)，门框比 1024 底图的门洞大一圈。判据有两条：
    /// 1. 按现状（`DOOR_AT`/`DOOR_SIZE` 反算出的 1.15 拟合位置）与底图的差要够小；
    /// 2. **必须**明显好于旧口径 —— 谁把绘制改回"素材 ×1.28"这条就红。
    #[test]
    fn 真素材_门帧贴合1024底图() {
        use crate::wzl::{Sprite, Wzl};

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(&dir);
        let ui1 = Wzl::open(dir.join("ui1")).expect("ui1");
        let chr = Wzl::open(dir.join("ChrSel")).expect("ChrSel");
        let bg = ui1.decode(2850).expect("1024 登录背景");
        let frame = chr.decode((Art::DOOR.1 + 1) as usize).expect("关门帧");

        /// 最近邻缩放到画布尺寸（绘制用双线性，但判别位置/比例够用）。
        fn scaled(t: &Sprite, w: i32, h: i32) -> Sprite {
            let mut out = Sprite {
                width: w as u16,
                height: h as u16,
                anchor_x: 0,
                anchor_y: 0,
                rgba: vec![0; (w * h * 4) as usize],
            };
            for y in 0..h {
                for x in 0..w {
                    let sx = (x as f32 * t.width as f32 / w as f32) as i32;
                    let sy = (y as f32 * t.height as f32 / h as f32) as i32;
                    let si = ((sy * t.width as i32 + sx) * 4) as usize;
                    let di = ((y * w + x) * 4) as usize;
                    out.rgba[di..di + 4].copy_from_slice(&t.rgba[si..si + 4]);
                }
            }
            out
        }

        // 模板的**不透明像素**与背景（左上角在 (x,y)）的平均差（步长 2 采样够判别）。
        fn diff_at(bg: &Sprite, t: &Sprite, x: i32, y: i32) -> Option<f32> {
            let (mut sum, mut n) = (0u64, 0u64);
            for ty in (0..t.height as i32).step_by(2) {
                let py = y + ty;
                if py < 0 || py >= bg.height as i32 {
                    return None;
                }
                for tx in (0..t.width as i32).step_by(2) {
                    let px = x + tx;
                    if px < 0 || px >= bg.width as i32 {
                        return None;
                    }
                    let ti = ((ty * t.width as i32 + tx) * 4) as usize;
                    if t.rgba[ti + 3] < 128 {
                        continue;
                    }
                    let bi = ((py * bg.width as i32 + px) * 4) as usize;
                    for c in 0..3 {
                        sum +=
                            (bg.rgba[bi + c] as i32 - t.rgba[ti + c] as i32).unsigned_abs() as u64;
                    }
                    n += 3;
                }
            }
            (n > 0).then(|| sum as f32 / n as f32)
        }

        // 现状口径：绘制尺寸 = DOOR_SIZE ×1.28，落点 = DOOR_AT ×1.28
        let now = scaled(
            &frame,
            (Layout::DOOR_SIZE.0 * 1.28).round() as i32,
            (Layout::DOOR_SIZE.1 * 1.28).round() as i32,
        );
        let (dx, dy) = (
            (Layout::DOOR_AT.0 * 1.28).round() as i32,
            (Layout::DOOR_AT.1 * 1.28).round() as i32,
        );
        let here = diff_at(&bg, &now, dx, dy).expect("落点在图内");
        assert!(
            here < 24.0,
            "门帧在 ({dx},{dy}) 与 1024 底图差 {here:.2} —— 拟合错了？"
        );

        // 旧口径：素材原尺寸 ×1.28、落点 (196,123)
        let old = scaled(
            &frame,
            (496.0f32 * 1.28).round() as i32,
            (361.0f32 * 1.28).round() as i32,
        );
        let old_diff = diff_at(&bg, &old, 196, 123).expect("旧落点在图内");
        assert!(
            old_diff > here + 1.0,
            "1.15 拟合必须比旧的 1.28 口径更贴底图（{here:.2} vs {old_diff:.2}）"
        );
    }

    /// 素材缺一个就整体 `None`（宁可写"素材缺失"，也不画歪框）。
    #[test]
    fn 缺素材就不出版式() {
        let missing = |c: &'static str, i: u32| {
            if (c, i) == ("Prguse", 62) {
                None
            } else {
                fake(c, i)
            }
        };
        assert!(Layout::build((1024, 768), missing).is_none());
    }

    /// **真素材验收**：登录界面要的每一张图都真的在容器里。
    ///
    /// 这条是"照原版"的另一半：版式算得再对，素材缺一张就是画不出来。
    /// 素材不在时跳过（与 `actor.rs` 的真素材验收同一个门控）。
    #[test]
    fn 真素材_登录素材齐全() {
        use crate::wzl::{Sprite, Wzl};
        use std::collections::HashMap;
        use std::path::Path;

        // 按需打开容器并取图（**辅助函数**而不是闭包：闭包没法把 `&mut Wzl` 借出去）
        fn dec(
            libs: &mut HashMap<&'static str, Wzl>,
            dir: &Path,
            name: &'static str,
            idx: u32,
        ) -> Option<Sprite> {
            if !libs.contains_key(name) {
                let w = Wzl::open(dir.join(name)).ok()?;
                libs.insert(name, w);
            }
            libs.get(name)?.decode(idx as usize)
        }

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(dir);
        let mut libs: HashMap<&'static str, Wzl> = HashMap::new();

        for (name, idx, what) in [
            (Art::DIALOG.0, Art::DIALOG.1, "登录对话框"),
            (Art::BTN_OK.0, Art::BTN_OK.1, "[提交]"),
            (Art::BTN_NEW.0, Art::BTN_NEW.1, "[新用户]"),
            (Art::BTN_CHGPW.0, Art::BTN_CHGPW.1, "[修改密码]"),
            (Art::BTN_CLOSE.0, Art::BTN_CLOSE.1, "[X]"),
            (Art::MSGBOX.0, Art::MSGBOX.1, "消息框"),
            (Art::MSGBOX_OK.0, Art::MSGBOX_OK.1, "消息框 [Ok]"),
            (Art::BG.0, Art::BG.1, "登录背景"),
        ] {
            let s = dec(&mut libs, &dir, name, idx)
                .unwrap_or_else(|| panic!("{what} = {name}[{idx}] 取不出图"));
            assert!(!s.is_empty(), "{what} = {name}[{idx}] 是空图");
        }

        // 开门帧要落在 800×600 画布内：原版偏移是**硬编码**的（`IntroScn.pas:845-846`
        // 的 +252/+106），帧一旦变大就会画到屏幕外 —— 用实测尺寸兜住这条。
        let door = dec(&mut libs, &dir, Art::DOOR.0, Art::DOOR.1 + 1).expect("开门第二帧");
        assert!(
            Layout::DOOR_AT.0 + door.width as f32 <= 800.0
                && Layout::DOOR_AT.1 + door.height as f32 <= 600.0,
            "开门帧 {}×{} + 偏移 {:?} 会画出 800×600 画布",
            door.width,
            door.height,
            Layout::DOOR_AT
        );

        // 开门动画：首帧 `ChrSel[23]` 在本套素材里是**空壳**（跳过它是对的），
        // 后面那几帧要拿得到 —— 哪天 23 补上了这条会红，提醒把首帧也算进来。
        assert!(
            dec(&mut libs, &dir, Art::DOOR.0, Art::DOOR.1).is_none(),
            "ChrSel[23] 有图了 ⇒ 开门动画的首帧该算了（现在实际是从 24 起）"
        );
        let frames = (0..Art::DOOR_FRAMES)
            .filter(|k| {
                dec(&mut libs, &dir, Art::DOOR.0, Art::DOOR.1 + k).is_some_and(|s| !s.is_empty())
            })
            .count();
        assert!(frames >= 8, "开门动画只找到 {frames} 帧，期望 ≥8");
    }

    /// 点按命中：按钮上算中、紧挨着的边界外不算。
    #[test]
    fn 命中判定() {
        let l = Layout::build((1024, 768), fake).unwrap();
        let c = (l.ok.x + l.ok.w / 2.0, l.ok.y + l.ok.h / 2.0);
        assert!(l.ok.hit(c));
        assert!(!l.ok.hit((l.ok.x - 1.0, c.1)));
        assert!(!l.ok.hit((l.ok.x + l.ok.w, c.1)), "右边界不算命中");
        assert!(!l.ok.hit((c.0, l.ok.y + l.ok.h)));
        // 输入框也能点（原版点了就把焦点给那个 `TEdit`）
        assert!(l.account.hit((l.account.x + 1.0, l.account.y + 1.0)));
    }

    /// 800×600 下应**正好**复现原版那几个字面量（`FState.pas` 里写死的坐标）——
    /// 这是"我没有理解错那套公式"的钉子。
    #[test]
    fn 八百乘六百复现原版字面量() {
        let l = Layout::build((800, 600), fake).unwrap();
        // 对话框：`Left=(800-296)/2=252, Top=(600-254)/2=173`（FState.pas:774-775）
        assert_eq!((l.dialog.x, l.dialog.y), (252.0, 173.0));
        // [提交]：对话框 +(168+1, 162+1)（FState.pas:791-792）
        assert_eq!((l.ok.x, l.ok.y), (252.0 + 169.0, 173.0 + 163.0));
        // [新用户]：+(24+1, 206+1)（FState.pas:788-789）
        assert_eq!((l.new.x, l.new.y), (252.0 + 25.0, 173.0 + 207.0));
        // [X]：+(252, 28)（FState.pas:797-798，原版注释"不用加1"）
        assert_eq!((l.close.x, l.close.y), (252.0 + 252.0, 173.0 + 28.0));
        // 尺寸必须来自**各自那张图**（别被同名的局部变量遮蔽掉 —— 踩过）
        assert_eq!(
            (l.ok.w, l.ok.h),
            (76.0, 33.0),
            "[提交] 的尺寸该取自 Prguse[62]"
        );
        assert_eq!(
            (l.new.w, l.new.h),
            (100.0, 32.0),
            "[新用户] 该取自 Prguse[61]"
        );
        assert_eq!(
            (l.close.w, l.close.h),
            (16.0, 23.0),
            "[X] 该取自 Prguse[64]"
        );
        // 用户名框：`cx-50=350, cy-42=258`（IntroScn.pas:552-554）
        assert_eq!(
            (l.account.x, l.account.y, l.account.w),
            (350.0, 258.0, 112.0)
        );
        // 密码框：`cx-50=350, cy-10=290`（IntroScn.pas:561-565）
        assert_eq!((l.password.x, l.password.y), (350.0, 290.0));
    }
}

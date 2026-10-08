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
    /// 登录场景全屏背景（800×600，门口石头）。
    pub const BG: (&'static str, u32) = ("ChrSel", 22);
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
    /// 建号面板的 [确定]（原版 `DNewAccountOk`，`FState.pas:868`：`g_WMainImages` 51）。
    pub const BTN_SIGNUP_OK: (&'static str, u32) = ("Prguse", 51);
    /// 建号面板的 [取消]（原版 `DNewAccountCancel`，`FState.pas:872`：52）。
    ///
    /// ⚠️ 原版建号面板还有一颗 [X]（图号 83），但**本套素材里它是空壳**（0×0）——
    /// 与 `ChrSel[23]` 同一现象。所以这里只有 [确定]/[取消]，关掉建号面板用
    /// 登录框自己那颗 [X]（`BTN_CLOSE`）。
    pub const BTN_SIGNUP_CANCEL: (&'static str, u32) = ("Prguse", 52);
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
const FIELD_TOP_A: f32 = 85.0;
const FIELD_TOP_P: f32 = 117.0;
/// 建号面板的"确认口令"框。
///
/// ⚠️ 这一格**没有原版出处**：原版建号面板（`DNewAccount`）是另一张面板图 + 12 个
/// 字段（`IntroScn.pas:929-1067`），我们只留 3 个字段，所以直接沿用登录框的行距
/// （85/117/149 = 每行 32px），落在底下那排按钮（y=207）之上、不与它们相撞。
const FIELD_TOP_C: f32 = 149.0;
const FIELD_W: f32 = 112.0;
const FIELD_H: f32 = 16.0;

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
        // 建号面板那两颗（原版 `DNewAccountOk/Cancel`）—— 缺了就整体 None，
        // 与其它素材同一条纪律：宁可写"素材缺失"，也不画一个歪掉的登录框。
        let (sow, soh) = measure(Art::BTN_SIGNUP_OK.0, Art::BTN_SIGNUP_OK.1)?;
        let (scw, sch) = measure(Art::BTN_SIGNUP_CANCEL.0, Art::BTN_SIGNUP_CANCEL.1)?;
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
                w: sow as f32,
                h: soh as f32,
            },
            signup_cancel: Rect {
                x: dialog.x + BTN_CHGPW_AT.0,
                y: dialog.y + BTN_CHGPW_AT.1,
                w: scw as f32,
                h: sch as f32,
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

    /// 一张 800×600 的背景图在窗口里该画在哪（居中）。
    pub fn bg_at(win: (u32, u32), size: (u32, u32)) -> (f32, f32) {
        (
            (win.0 as f32 - size.0 as f32) / 2.0,
            (win.1 as f32 - size.1 as f32) / 2.0,
        )
    }

    /// 开门动画相对 800×600 画布的偏移（原版 `IntroScn.pas:845-846`）：
    ///
    /// ```text
    /// MSurface.Draw ((SCREENWIDTH - 800) div 2 + 252, (SCREENHEIGHT - 600) div 2 + 106, ...)
    /// ```
    ///
    /// ⚠️ 门**不是**整屏背景：帧是 **496×361** 的局部覆盖（实测 `ChrSel[24..32]`），
    /// 按"居中贴"画会把它糊到屏幕正中间去（踩过）。
    pub const DOOR_AT: (f32, f32) = (252.0, 106.0);

    /// 原版 800×600 画布上的坐标 → 实际窗口坐标（画布居中后加偏移）。
    pub fn legacy_at(win: (u32, u32), off: (f32, f32)) -> (f32, f32) {
        (
            (win.0 as f32 - 800.0) / 2.0 + off.0,
            (win.1 as f32 - 600.0) / 2.0 + off.1,
        )
    }
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
            ("Prguse", 51) => Some((96, 34)), // 建号面板 [确定]（D-32）
            ("Prguse", 52) => Some((96, 33)), // 建号面板 [取消]
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
        }
    }

    /// 开门动画的位置照原版（`IntroScn.pas:845-846`）—— 门是**局部覆盖**，不是居中贴。
    ///
    /// 踩过的坑：按 `bg_at` 居中画，496×361 的门会跑到屏幕正中间，看着像"没有开门动画"。
    #[test]
    fn 开门位置照原版偏移() {
        // 800×600 窗口 ⇒ 画布原点 (0,0)
        assert_eq!(
            Layout::legacy_at((800, 600), Layout::DOOR_AT),
            (252.0, 106.0)
        );
        // 1024×768 ⇒ 画布原点 (112, 84)，再加原版偏移
        assert_eq!(
            Layout::legacy_at((1024, 768), Layout::DOOR_AT),
            (112.0 + 252.0, 84.0 + 106.0)
        );
        // ⚠️ "门帧会不会画出 800×600 画布"不在这里断言：那要靠**实测帧尺寸**，
        // 归 `真素材_登录素材齐全`（写死数字会被 clippy 判成"恒真断言"）。
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
            (Art::BTN_SIGNUP_OK.0, Art::BTN_SIGNUP_OK.1, "建号 [确定]"),
            (
                Art::BTN_SIGNUP_CANCEL.0,
                Art::BTN_SIGNUP_CANCEL.1,
                "建号 [取消]",
            ),
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

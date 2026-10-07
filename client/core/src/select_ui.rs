//! 选角场景的**版式与动画状态机**：纯数据 + 纯计算，**不含任何绘制**。
//!
//! 放在 `core` 的理由与 `login_ui` / `actor` 一样：这是**游戏知识**
//! （"原版选角界面长什么样、小人怎么动"），且 `client/e2e` 要能按同一份合成图。
//!
//! # 出处（原版 `mir2standard/GameOfMir/Client/`，⚠️ GBK）
//!
//! | 东西 | 位置 | 出处 |
//! |---|---|---|
//! | 场景类 `TSelectChrScene` | `PlayScene` | `IntroScn.pas:1102-1548` |
//! | 背景 `Prguse[65]`，居中 | `((SCRW-w)/2, (SCRH-h)/2)` | `IntroScn.pas:1374-1385` |
//! | 两个**固定槽**（槽 1 = 槽 0 整体 +340,+2） | `bx := bx + 340; by := by + 2` | `IntroScn.pas:1431-1438` |
//! | 小人 `ChrSel.wil`（按 Job/Sex 预渲染） | 站立基址 `40+Job*40+Sex*120` | `IntroScn.pas:1483` |
//! | 石化基址 `60+Job*40+Sex*120` | 13 帧 | `IntroScn.pas:1440 / 1468 / 1499` |
//! | 选中光效 `ChrSel[4..]` | 14 帧 | `IntroScn.pas:1442` |
//! | 名字/等级/职业（白字黑边） | 槽 0 `(136,476/513/548)`、槽 1 `(586,476) (666,513) (638,548)` | `IntroScn.pas:1518-1538` |
//! | 按钮（`Prguse` 66..72）位置 | 见 [`BTN_AT`] | `FState.pas:904-925` |
//! | 职业名 | 战士 / 魔法师 / 道士 / 未知 | `MShare.pas:875-890` |
//!
//! # ⚠️ 两条最容易做错的
//!
//! 1. **按钮平时不画**。背景图 `Prguse[65]` 里**已经画好了**按钮外观；
//!    `Prguse[66..72]` 是**按下态**叠加图 —— 原版只在 `Downed` 时才画它们
//!    （`FState.pas:2693-2705` 的 `DscSelect1DirectPaint`）。所以照抄的正确做法是
//!    "按下才画"，而不是"按钮 = 66/67/68…"。
//! 2. **没有箭头、没有高亮框**。选中与否的差别是**小人**：
//!    选中的播站立动画、未选中的是**石化**定格（`FreezeState`）。
//!    原版另有一层按 `DarkLevel` 的压暗（`MakeDark`，`IntroScn.pas:1485-1494`），
//!    但 `MakeDark(dd, 0)` 直接 `exit`（`CliUtil.pas:749`：`if not darklevel in [1..30] then exit`），
//!    而 `SelectChr` 恰好把 `DarkLevel` 置 30 ⇒ 那层只在**过渡**时轻微可见。
//!    这里按"选中=站立 / 未选中=石化"实现，压暗略去（见 [`SlotAnim`] 的说明）。

/// 素材编号与节拍：**原版写死的常量**（所以它属于"规格"，与公式分开）。
pub struct Art;

impl Art {
    /// 选角背景（800×600，按钮外观已画在图里）。`IntroScn.pas:1376`
    pub const BG: (&'static str, u32) = ("Prguse", 65);
    /// 槽 0 / 槽 1 的**按下态**热区图。`FState.pas:904-905`
    pub const SLOT_DOWN: [(&'static str, u32); 2] = [("Prguse", 66), ("Prguse", 67)];
    /// [开始游戏] 的按下态。`FState.pas:906`
    pub const BTN_START: (&'static str, u32) = ("Prguse", 68);
    /// [新建角色]。`FState.pas:907`
    pub const BTN_NEW: (&'static str, u32) = ("Prguse", 69);
    /// [删除角色]。`FState.pas:908`
    pub const BTN_DEL: (&'static str, u32) = ("Prguse", 70);
    /// [退出]。`FState.pas:910`
    pub const BTN_EXIT: (&'static str, u32) = ("Prguse", 72);

    /// 小人容器（`CHRSELIMAGEFILE = 'Data\ChrSel.wil'`，`Share.pas:52`）。
    pub const CHR: &'static str = "ChrSel";

    /// 站立帧数（`SELECTEDFRAME`，`IntroScn.pas:12`）。
    pub const STAND_FRAMES: u32 = 16;
    /// 石化/解冻帧数（`FREEZEFRAME`，`IntroScn.pas:13`）。
    pub const FREEZE_FRAMES: u32 = 13;
    /// 光效帧数（`EFFECTFRAME`，`IntroScn.pas:14`）。
    pub const EFFECT_FRAMES: u32 = 14;

    /// 站立动画每帧毫秒（`IntroScn.pas:1503`：`> 300`）。
    pub const STAND_MS: u32 = 300;
    /// 解冻/石化每帧毫秒（`IntroScn.pas:1466 / 1447`：`> 50`）。
    pub const FREEZE_MS: u32 = 50;

    /// 每个账号最多几个角色槽（原版两个槽；`IntroScn.pas:1213` 的提示也写"两个"）。
    pub const SLOTS: usize = 2;
}

/// 按钮在 **800×600 坐标系**里的位置（`FState.pas:912-925`）。
///
/// ⚠️ 这里只是**位置**；尺寸与"画不画"都取决于素材与按下状态（见文件头第 1 条）。
const BTN_AT: [(f32, f32); 5] = [
    (374.0, 427.0), // [开始游戏]
    (349.0, 467.0), // [新建角色]
    (349.0, 505.0), // [删除角色]
    (349.0, 543.0), // [退出]
    (134.0, 424.0), // 槽 0 热区（槽 1 = +468）
];
/// 槽 1 热区相对槽 0 的横向偏移（`FState.pas:904-905`：134 → 602）。
const SLOT1_DX: f32 = 468.0;

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

    /// 中心（画图与命中都用得上）。
    pub fn center(&self) -> (f32, f32) {
        (self.x + self.w / 2.0, self.y + self.h / 2.0)
    }
}

/// 槽里那几个东西该画在哪（屏幕绝对坐标，已含窗口偏移与槽偏移）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct SlotAt {
    /// **石化**位置（`bx,by`）。
    pub frozen: (f32, f32),
    /// **站立**位置（`fx,fy`）—— 女性职业与石化位置不同（原版逐项写了偏移）。
    pub stand: (f32, f32),
    /// 选中光效位置（`ex,ey`）。
    pub effect: (f32, f32),
    /// 名字 / 等级 / 职业三行（左对齐基线同原版 `BoldTextOut`）。
    pub name: (f32, f32),
    pub level: (f32, f32),
    pub class: (f32, f32),
}

/// 一帧的版式（全部屏幕绝对坐标）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Layout {
    /// 背景左上角（居中）。
    pub bg: (f32, f32),
    /// 两个槽的**点击热区**（原版那两个 `TDButton` 决定的范围）。
    pub slot_hot: [Rect; 2],
    /// [开始游戏] / [新建角色] / [删除角色] / [退出]。
    pub start: Rect,
    pub new: Rect,
    pub del: Rect,
    pub exit: Rect,
}

impl Layout {
    /// 按窗口尺寸、素材实测尺寸与**两个槽的职业/性别**算版式。
    ///
    /// `measure` 由调用方给（只读容器头、不解压像素 ⇒ 可以每帧调）。
    /// 背景或任一按钮素材缺失 ⇒ 整体 `None`（宁可写一行"素材缺失"，
    /// 也不画一个按钮跑偏的界面 —— 与登录界面同一条纪律）。
    ///
    /// ⚠️ 版式**不含**"每个槽的 (job,sex)"—— 那属于 [`slot_at`]：
    /// 版式是"背景与按钮在哪"，而小人位置取决于职业/性别。分开的好处是
    /// 角色列表还没到时也能先把版式算出来。
    pub fn build(
        win: (u32, u32),
        mut measure: impl FnMut(&'static str, u32) -> Option<(u32, u32)>,
    ) -> Option<Layout> {
        let (bw, bh) = measure(Art::BG.0, Art::BG.1)?;
        // 背景居中 —— 与登录界面同一个换算（素材 800×600，窗口可能更大）
        let bg = (
            (win.0 as f32 - bw as f32) / 2.0,
            (win.1 as f32 - bh as f32) / 2.0,
        );
        let rect_at = |measure: &mut dyn FnMut(&'static str, u32) -> Option<(u32, u32)>,
                       lib: &'static str,
                       idx: u32,
                       at: (f32, f32),
                       dx: f32|
         -> Option<Rect> {
            let (w, h) = measure(lib, idx)?;
            Some(Rect {
                x: bg.0 + at.0 + dx,
                y: bg.1 + at.1,
                w: w as f32,
                h: h as f32,
            })
        };
        let m = &mut measure;
        let start = rect_at(m, Art::BTN_START.0, Art::BTN_START.1, BTN_AT[0], 0.0)?;
        let new = rect_at(m, Art::BTN_NEW.0, Art::BTN_NEW.1, BTN_AT[1], 0.0)?;
        let del = rect_at(m, Art::BTN_DEL.0, Art::BTN_DEL.1, BTN_AT[2], 0.0)?;
        let exit = rect_at(m, Art::BTN_EXIT.0, Art::BTN_EXIT.1, BTN_AT[3], 0.0)?;
        let hot0 = rect_at(m, Art::SLOT_DOWN[0].0, Art::SLOT_DOWN[0].1, BTN_AT[4], 0.0)?;
        let hot1 = rect_at(
            m,
            Art::SLOT_DOWN[1].0,
            Art::SLOT_DOWN[1].1,
            BTN_AT[4],
            SLOT1_DX,
        )?;
        Some(Layout {
            bg,
            slot_hot: [hot0, hot1],
            start,
            new,
            del,
            exit,
        })
    }
}

/// 站力图号：`40 + Job*40 + Sex*120 + k`（`IntroScn.pas:1483` 的 `120-80` 化简）。
///
/// `k` 会按 [`Art::STAND_FRAMES`] 取模（选中的槽循环播放）。
pub fn stand_index(job: u8, sex: u8, k: u32) -> u32 {
    40 + job as u32 * 40 + (sex as u32 % 2) * 120 + (k % Art::STAND_FRAMES)
}

/// 石化/解冻图号：`60 + Job*40 + Sex*120 + k`（`IntroScn.pas:1440` 的 `140-80` 化简）。
pub fn freeze_index(job: u8, sex: u8, k: u32) -> u32 {
    60 + job as u32 * 40 + (sex as u32 % 2) * 120 + (k % Art::FREEZE_FRAMES)
}

/// 选中光效图号：`4 + k`（`IntroScn.pas:1442`）。
pub fn effect_index(k: u32) -> u32 {
    4 + (k % Art::EFFECT_FRAMES)
}

/// 职业名（`GetJobName`，`MShare.pas:875-890`）。
///
/// ⚠️ 原版代码注释写"武士/魔法师/道士"，但**字面量**是"战士/魔法师/道士"
/// （`MShare.pas:138-141`）—— 以字面量为准。入参用**新协议的 `CharClass`**（1/2/3）。
pub fn class_name(class: i32) -> &'static str {
    match class {
        1 => "战士",
        2 => "魔法师",
        3 => "道士",
        _ => "未知",
    }
}

/// 一个槽该画哪张图、画在哪。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct SlotFrame {
    /// 图号（`Art::CHR` 容器里的）。
    pub index: u32,
    /// 画在**石化**位置（`true`）还是**站立**位置（`false`）。
    pub at_frozen: bool,
    /// 需要叠的光效图号（解冻时才亮）。
    pub effect: Option<u32>,
}

/// 槽的动画阶段。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SlotPhase {
    /// 站立（**选中**的那个在播循环动画；未选中的钉在第 0 帧）。
    Stand,
    /// 解冻（刚被选中：石化帧正放 13 帧）。
    Unfreezing,
    /// 石化定格（未选中）。
    Freeze,
    /// 正在石化（刚被取消选中：石化帧倒放 13 帧）。
    Freezing,
}

/// 一个槽的动画状态机。
///
/// # 为什么把"压暗"省掉
///
/// 原版未选中的槽会按 `DarkLevel` 压暗（`MakeDark`），但那是**过渡**效果：
/// `SelectChr` 把 `DarkLevel` 置 30，而 `MakeDark(dd, 30-30=0)` 直接 `exit`
/// （`CliUtil.pas:749` 只接受 1..30）⇒ 选中那一刻不压暗；随后每 25ms 减 1，
/// 只在中间那几百毫秒里"略暗一下"，最后落到 0 又回到不压暗。
/// 主视觉是**站立 vs 石化**，所以这里只实现它 —— 少一个猜测出来的曲线。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct SlotAnim {
    phase: SlotPhase,
    /// 当前阶段内的帧号（0 起）。
    k: u32,
    /// 阶段内累计毫秒（用来按 300/50ms 进帧）。
    acc: u32,
    /// 这一槽的所属角色（图号要用）。
    pub job: u8,
    pub sex: u8,
}

impl SlotAnim {
    /// 新槽：站立、第 0 帧、不选中。
    ///
    /// ⚠️ 新进选角场景时**两个槽都不石化**（原版 `OpenScene` 把 `DarkLevel` 置 0，
    /// 于是走"未石化"分支，两个人都站着）—— 只有你点了其中一个，
    /// 另一个才开始石化。
    pub fn new(job: u8, sex: u8) -> Self {
        SlotAnim {
            phase: SlotPhase::Stand,
            k: 0,
            acc: 0,
            job,
            sex,
        }
    }

    pub fn phase(&self) -> SlotPhase {
        self.phase
    }

    /// 玩家点了这个槽（`SelectChr`）：从石化态开始解冻；本来就是站立则原地不动。
    pub fn select(&mut self) {
        self.acc = 0;
        self.k = 0;
        // 无条件重来一遍解冻 —— 原版 `SelectChr` 也是无条件重置计时与帧号
        //（已经选中的槽再点一次，就再看一遍解冻动画）。
        self.phase = SlotPhase::Unfreezing;
    }

    /// 玩家选走了**别的**槽：这一槽开始石化（原版那条 `Freezing := TRUE`）。
    pub fn deselect(&mut self) {
        if self.phase == SlotPhase::Freeze || self.phase == SlotPhase::Freezing {
            return;
        }
        self.acc = 0;
        self.k = 0;
        self.phase = SlotPhase::Freezing;
    }

    /// 推进 `dt_ms` 毫秒；返回**这一帧该画什么**。
    ///
    /// `selected` 决定站立动画是否在走（原版只在 `Selected` 时进帧）。
    pub fn tick(&mut self, dt_ms: u32, selected: bool) -> SlotFrame {
        self.acc += dt_ms;
        let step = match self.phase {
            SlotPhase::Stand => Art::STAND_MS,
            _ => Art::FREEZE_MS,
        };
        while self.acc >= step {
            self.acc -= step;
            self.k += 1;
        }
        match self.phase {
            SlotPhase::Stand => {
                // 未选中：钉在第 0 帧（原版 `aniIndex` 不动）
                let k = if selected { self.k } else { 0 };
                SlotFrame {
                    index: stand_index(self.job, self.sex, k),
                    at_frozen: false,
                    effect: None,
                }
            }
            SlotPhase::Unfreezing => {
                if self.k >= Art::FREEZE_FRAMES {
                    // 解冻完了 ⇒ 站立（原版 `FreezeState := FALSE`）
                    self.phase = SlotPhase::Stand;
                    self.k = 0;
                    return self.tick(0, selected);
                }
                SlotFrame {
                    index: freeze_index(self.job, self.sex, self.k),
                    at_frozen: true,
                    effect: Some(effect_index(self.k)),
                }
            }
            SlotPhase::Freeze => SlotFrame {
                index: freeze_index(self.job, self.sex, 0),
                at_frozen: true,
                effect: None,
            },
            SlotPhase::Freezing => {
                if self.k >= Art::FREEZE_FRAMES {
                    self.phase = SlotPhase::Freeze;
                    self.k = 0;
                    return self.tick(0, selected);
                }
                // **倒放**：`FREEZEFRAME - aniIndex - 1`（`IntroScn.pas:1468`）
                SlotFrame {
                    index: freeze_index(self.job, self.sex, Art::FREEZE_FRAMES - 1 - self.k),
                    at_frozen: true,
                    effect: None,
                }
            }
        }
    }
}

/// 某个槽（0/1）的四个绘制位置。`slot` 越界按 0 算。
pub fn slot_at(slot: usize, job: u8, sex: u8, bg: (f32, f32)) -> SlotAt {
    // 原版逐项写死的 (bx,by) 与 (fx,fy)，`IntroScn.pas:1390-1429`
    let (bx, by, fx, fy) = match (job % 3, sex % 2) {
        (0, 0) => (71.0, 75.0 - 23.0, 71.0, 75.0 - 23.0),
        // 女：`fx := bx-28+28; fy := by-16+16` ⇒ 与原值相同（原版的 ±28 抵消了）
        (0, _) => (65.0, 75.0 - 2.0 - 18.0, 65.0, 75.0 - 2.0 - 18.0),
        (1, 0) => (77.0, 75.0 - 29.0, 77.0, 75.0 - 29.0),
        (1, _) => (
            141.0 + 30.0,
            85.0 + 14.0 - 2.0,
            141.0 + 30.0 - 30.0,
            85.0 + 14.0 - 2.0 - 14.0,
        ),
        (2, 0) => (85.0, 75.0 - 12.0, 85.0, 75.0 - 12.0),
        (_, _) => (
            141.0 + 23.0,
            85.0 + 20.0 - 2.0,
            141.0 + 23.0 - 23.0,
            85.0 + 20.0 - 2.0 - 20.0,
        ),
    };
    // 槽 1：整体 +340（人）+2（纵）后，光效与文字各按原版另算
    let dx = if slot == 1 { 340.0 } else { 0.0 };
    let dy = if slot == 1 { 2.0 } else { 0.0 };
    let (ex, ey) = if slot == 1 {
        (430.0, 60.0)
    } else {
        (90.0, 60.0 - 2.0)
    };
    // 文字：槽 0 三行都在 x=136；槽 1 名字 x=586、等级 x=666、职业 x=638（原版不对齐）
    let (nx, lx, cx) = if slot == 1 {
        (586.0, 666.0, 638.0)
    } else {
        (136.0, 136.0, 136.0)
    };
    let tx = |x: f32, y: f32| (bg.0 + x, bg.1 + y);
    SlotAt {
        frozen: (bg.0 + bx + dx, bg.1 + by + dy),
        stand: (bg.0 + fx + dx, bg.1 + fy + dy),
        effect: (bg.0 + ex, bg.1 + ey),
        name: tx(nx, 474.0 + 2.0),
        level: tx(lx, 513.0),
        class: tx(cx, 548.0),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 照真素材量出来的尺寸（`Prguse[65]` 800×600 等）。
    /// 不需要真素材就能单测版式（与 `login_ui` 同一手法）。
    fn fake(container: &'static str, idx: u32) -> Option<(u32, u32)> {
        match (container, idx) {
            ("Prguse", 65) => Some((800, 600)),
            ("Prguse", 66) => Some((96, 74)),
            ("Prguse", 67) => Some((96, 74)),
            ("Prguse", 68) => Some((52, 21)),
            ("Prguse", 69) => Some((78, 20)),
            ("Prguse", 70) => Some((78, 20)),
            ("Prguse", 72) => Some((78, 20)),
            _ => None,
        }
    }

    /// 800×600 下版式要**正好**复现原版那几个字面量 —— 这是"没理解错"的钉子。
    #[test]
    fn 八百乘六百复现原版字面量() {
        let l = Layout::build((800, 600), fake).expect("素材齐全");
        // 背景：`(800-800)/2 = 0`
        assert_eq!(l.bg, (0.0, 0.0));
        // 按钮位置就是 `FState.pas:912-925` 那几组字面量
        assert_eq!((l.start.x, l.start.y), (374.0, 427.0));
        assert_eq!((l.new.x, l.new.y), (349.0, 467.0));
        assert_eq!((l.del.x, l.del.y), (349.0, 505.0));
        assert_eq!((l.exit.x, l.exit.y), (349.0, 543.0));
        // 槽热区：134 与 602（= 134 + 468）
        assert_eq!((l.slot_hot[0].x, l.slot_hot[1].x), (134.0, 602.0));
        assert_eq!(l.slot_hot[0].y, l.slot_hot[1].y);
    }

    /// 窗口比素材大时，整套界面**跟着背景一起平移**（素材是 800×600 时代的）。
    #[test]
    fn 窗口变大时整体平移() {
        let win = (1024u32, 768u32);
        let l = Layout::build(win, fake).unwrap();
        // 背景居中：(1024-800)/2 = 112, (768-600)/2 = 84
        assert_eq!(l.bg, (112.0, 84.0));
        let l800 = Layout::build((800, 600), fake).unwrap();
        for (a, b) in [
            (l.start, l800.start),
            (l.new, l800.new),
            (l.del, l800.del),
            (l.exit, l800.exit),
            (l.slot_hot[0], l800.slot_hot[0]),
            (l.slot_hot[1], l800.slot_hot[1]),
        ] {
            assert_eq!((a.x - b.x, a.y - b.y), (112.0, 84.0), "{a:?} vs {b:?}");
        }
        // 位置也得跟着走（小人/文字都在同一坐标系里）
        let s0 = slot_at(0, 0, 0, l.bg);
        let s0_800 = slot_at(0, 0, 0, l800.bg);
        assert_eq!(
            (s0.stand.0 - s0_800.stand.0, s0.stand.1 - s0_800.stand.1),
            (112.0, 84.0)
        );
    }

    /// 小人/文字位置照原版那几个字面量（含女性职业与槽 1 的偏移）。
    #[test]
    fn 槽位与文字位置() {
        // 槽 0 男战士：`(71, 75-23)`
        let a = slot_at(0, 0, 0, (0.0, 0.0));
        assert_eq!(a.stand, (71.0, 52.0));
        assert_eq!(a.frozen, (71.0, 52.0));
        assert_eq!(a.effect, (90.0, 58.0));
        assert_eq!(a.name, (136.0, 476.0));
        assert_eq!(a.level, (136.0, 513.0));
        assert_eq!(a.class, (136.0, 548.0));

        // 槽 1 男战士：整体 +340/+2，光效与文字按原版另算
        let b = slot_at(1, 0, 0, (0.0, 0.0));
        assert_eq!(b.stand, (411.0, 54.0));
        assert_eq!(b.effect, (430.0, 60.0));
        assert_eq!(b.name, (586.0, 476.0));
        assert_eq!(b.level, (666.0, 513.0));
        assert_eq!(b.class, (638.0, 548.0));

        // 女法师（Job=1,Sex=1）：站立与石化**不是同一个点**（原版写了偏移）
        let c = slot_at(0, 1, 1, (0.0, 0.0));
        assert_eq!(c.frozen, (171.0, 97.0));
        assert_eq!(c.stand, (141.0, 83.0));
        assert_ne!(c.frozen, c.stand, "女法师的石化/站立位置本就不同");

        // 女道士（Job=2,Sex=1）
        let d = slot_at(0, 2, 1, (0.0, 0.0));
        assert_eq!(d.frozen, (164.0, 103.0));
        assert_eq!(d.stand, (141.0, 83.0));
    }

    /// 图号公式：基址 + 槽内偏移，且 0..15 / 0..12 都在容器范围内。
    #[test]
    fn 图号公式() {
        // 站立基址 40 + Job*40 + Sex*120（`IntroScn.pas:1483`）
        assert_eq!(stand_index(0, 0, 0), 40);
        assert_eq!(stand_index(1, 0, 0), 80);
        assert_eq!(stand_index(2, 0, 0), 120);
        assert_eq!(stand_index(0, 1, 0), 160);
        assert_eq!(stand_index(1, 1, 0), 200);
        assert_eq!(stand_index(2, 1, 0), 240);
        // 站立最后一帧不会越到石化段（40+15=55 < 60）
        assert_eq!(stand_index(0, 0, 15), 55);
        // 石化基址 60（`IntroScn.pas:1440`）
        assert_eq!(freeze_index(0, 0, 0), 60);
        assert_eq!(freeze_index(2, 1, 12), 60 + 80 + 120 + 12);
        // 站立段与石化段不重叠：所有 Job/Sex 都成立
        for job in 0..3u8 {
            for sex in 0..2u8 {
                assert!(
                    stand_index(job, sex, Art::STAND_FRAMES - 1) < freeze_index(job, sex, 0),
                    "Job={job} Sex={sex} 站立段压到石化段了"
                );
            }
        }
        // 光效 4 起
        assert_eq!(effect_index(0), 4);
        assert_eq!(effect_index(Art::EFFECT_FRAMES), 4);
        // 越界取模（选中的站立动画会一直进帧）
        assert_eq!(stand_index(0, 0, Art::STAND_FRAMES), stand_index(0, 0, 0));
    }

    /// 职业名照原版**字面量**（不是它注释里的"武士"）。
    #[test]
    fn 职业名() {
        assert_eq!(class_name(1), "战士");
        assert_eq!(class_name(2), "魔法师");
        assert_eq!(class_name(3), "道士");
        assert_eq!(class_name(0), "未知");
        assert_eq!(class_name(9), "未知");
    }

    /// 动画状态机：选中 ⇒ 解冻 13 帧 ⇒ 站立；取消 ⇒ 倒放石化的 13 帧 ⇒ 定格。
    #[test]
    fn 选中与取消的动画阶段() {
        let mut s = SlotAnim::new(0, 0);
        // 初始：站立第 0 帧（两个槽都不石化）
        let f = s.tick(0, false);
        assert_eq!(s.phase(), SlotPhase::Stand);
        assert_eq!(f.index, stand_index(0, 0, 0));
        assert!(!f.at_frozen);
        assert!(f.effect.is_none());

        // 选中 ⇒ 解冻；每 50ms 进帧，且这一阶段要亮光效
        s.select();
        assert_eq!(s.phase(), SlotPhase::Unfreezing);
        let f = s.tick(0, true);
        assert_eq!(f.index, freeze_index(0, 0, 0));
        assert_eq!(f.effect, Some(4));
        let f = s.tick(150, true);
        assert_eq!((f.index, f.effect), (freeze_index(0, 0, 3), Some(7)));
        // 13 帧（650ms）走完 ⇒ 站立
        for _ in 0..12 {
            s.tick(50, true);
        }
        assert_eq!(s.phase(), SlotPhase::Stand);
        assert_eq!(s.tick(0, true).index, stand_index(0, 0, 0));

        // 站立动画：选中的那个每 300ms 进一帧
        let mut s = SlotAnim::new(1, 0);
        s.select();
        for _ in 0..13 {
            s.tick(50, true);
        } // 解冻完
        assert_eq!(s.phase(), SlotPhase::Stand);
        s.tick(250, true);
        assert_eq!(s.tick(0, true).index, stand_index(1, 0, 0), "还没到 300ms");
        s.tick(50, true);
        assert_eq!(
            s.tick(0, true).index,
            stand_index(1, 0, 1),
            "过了 300ms 该进帧"
        );

        // 取消选中 ⇒ 石化**倒放**（图号递减）
        s.deselect();
        assert_eq!(s.phase(), SlotPhase::Freezing);
        assert_eq!(
            s.tick(0, false).index,
            freeze_index(1, 0, Art::FREEZE_FRAMES - 1),
            "石化第一帧应当是石化段的最后一帧（倒放）"
        );
        // ⚠️ 13 次而不是 12：上面那次 `tick(0)` 只取到第 0 帧，没进 k
        for _ in 0..Art::FREEZE_FRAMES {
            s.tick(50, false);
        }
        assert_eq!(s.phase(), SlotPhase::Freeze);
        assert_eq!(s.tick(0, false).index, freeze_index(1, 0, 0));
        // 定格后不再动
        let f1 = s.tick(999, false);
        let f2 = s.tick(999, false);
        assert_eq!(f1, f2);
    }

    /// 未选中的槽**不播站立动画**（原版只在 `Selected` 时进帧）。
    #[test]
    fn 未选中不播站立动画() {
        let mut s = SlotAnim::new(0, 0);
        let a = s.tick(1000, false);
        let b = s.tick(1000, false);
        assert_eq!(a, b, "未选中的槽该钉住不动");
        assert_eq!(a.index, stand_index(0, 0, 0));
    }

    /// 点空槽不算命中；按钮之间有缝（免得两颗按钮互相抢）。
    #[test]
    fn 命中判定() {
        let l = Layout::build((800, 600), fake).unwrap();
        assert!(l.slot_hot[0].hit(l.slot_hot[0].center()));
        assert!(l.slot_hot[1].hit(l.slot_hot[1].center()));
        assert!(!l.slot_hot[0].hit((l.slot_hot[0].x - 1.0, l.slot_hot[0].y)));
        // 两颗热区不重叠（134..230 与 602..698）
        assert!(l.slot_hot[0].x + l.slot_hot[0].w <= l.slot_hot[1].x);
        // 三颗竖排按钮不重叠（20 高、间距 18/38/38 ⇒ 467..487 / 505..525 / 543..563）
        assert!(l.new.y + l.new.h <= l.del.y);
        assert!(l.del.y + l.del.h <= l.exit.y);
    }

    /// 缺素材就不出版式（与登录界面同一条纪律）。
    #[test]
    fn 缺素材就不出版式() {
        let missing = |c: &'static str, i: u32| {
            if (c, i) == ("Prguse", 68) {
                None
            } else {
                fake(c, i)
            }
        };
        assert!(Layout::build((800, 600), missing).is_none());
    }

    /// **真素材验收**：选角要的每一张图都真的在容器里，而且
    /// **站立各帧确实不同**（否则动画是静帧）、**石化帧与站立帧不同**
    /// （否则"石化"看着像没变）。
    #[test]
    fn 真素材_选角素材齐全() {
        use crate::wzl::{Sprite, Wzl};
        use std::collections::HashMap;
        use std::path::Path;

        fn dec(
            libs: &mut HashMap<&'static str, Wzl>,
            dir: &Path,
            name: &'static str,
            idx: u32,
        ) -> Option<Sprite> {
            if !libs.contains_key(name) {
                libs.insert(name, Wzl::open(dir.join(name)).ok()?);
            }
            libs.get(name)?.decode(idx as usize)
        }

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(dir);
        let mut libs: HashMap<&'static str, Wzl> = HashMap::new();

        // 背景 + 六颗按钮（按下态）都要在
        for (name, idx, what) in [
            (Art::BG.0, Art::BG.1, "选角背景"),
            (Art::SLOT_DOWN[0].0, Art::SLOT_DOWN[0].1, "槽 0 热区"),
            (Art::SLOT_DOWN[1].0, Art::SLOT_DOWN[1].1, "槽 1 热区"),
            (Art::BTN_START.0, Art::BTN_START.1, "[开始游戏]"),
            (Art::BTN_NEW.0, Art::BTN_NEW.1, "[新建角色]"),
            (Art::BTN_DEL.0, Art::BTN_DEL.1, "[删除角色]"),
            (Art::BTN_EXIT.0, Art::BTN_EXIT.1, "[退出]"),
        ] {
            let s = dec(&mut libs, &dir, name, idx)
                .unwrap_or_else(|| panic!("{what} = {name}[{idx}] 取不出图"));
            assert!(!s.is_empty(), "{what} = {name}[{idx}] 是空图");
        }

        // 小人：三种职业 × 两种性别 × 站立 16 帧 / 石化 13 帧 / 光效 14 帧
        for job in 0..3u8 {
            for sex in 0..2u8 {
                for k in 0..Art::STAND_FRAMES {
                    let i = stand_index(job, sex, k);
                    assert!(
                        dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()),
                        "站立 Job={job} Sex={sex} 第 {k} 帧（ChrSel[{i}]）没有图"
                    );
                }
                for k in 0..Art::FREEZE_FRAMES {
                    let i = freeze_index(job, sex, k);
                    assert!(
                        dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()),
                        "石化 Job={job} Sex={sex} 第 {k} 帧（ChrSel[{i}]）没有图"
                    );
                }
                // 站立第 0 帧与第 1 帧必须**不是同一张**（否则"动画"是静帧）
                let a = dec(&mut libs, &dir, Art::CHR, stand_index(job, sex, 0)).unwrap();
                let b = dec(&mut libs, &dir, Art::CHR, stand_index(job, sex, 1)).unwrap();
                assert_ne!(
                    a.rgba, b.rgba,
                    "Job={job} Sex={sex}：站立第 0/1 帧一模一样（图号公式错了？）"
                );
                // 石化帧与站立帧也得不同（否则"石化"看不出来）
                let c = dec(&mut libs, &dir, Art::CHR, freeze_index(job, sex, 0)).unwrap();
                assert_ne!(a.rgba, c.rgba, "Job={job} Sex={sex}：石化帧与站立帧一样");
            }
        }
        for k in 0..Art::EFFECT_FRAMES {
            let i = effect_index(k);
            assert!(
                dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()),
                "选中光效第 {k} 帧（ChrSel[{i}]）没有图"
            );
        }
    }
}

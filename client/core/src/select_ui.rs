//! 选角场景的**版式与动画状态机**：纯数据 + 纯计算，**不含任何绘制**。
//!
//! # ⚠️ 坐标是**从这套素材量出来的**，不是照抄原版
//!
//! 原版（`mir2standard` 的 1.76）的坐标在 `FState.pas:904-925` / `IntroScn.pas:1390-1538`，
//! 但**我方的素材不是那一套**：`mir2c/data/Prguse.wzl` 的选角背景是另一个版式
//! （`No 1`/`No 2` 两块面板 + 中间竖排 5 项菜单），面板间距 552 而原版是 340，
//! 按钮行距约 20 而原版是 38 ⇒ 照抄原版坐标会让**点击区、按下态、小人、文字
//! 全部错位**（实测踩过）。所以这里全部改成本套素材的实测值（见 D-27）。
//!
//! 量法与依据（都是**有界**计算，毫秒级；工具用完即删）：
//!
//! | 量什么 | 怎么量 | 结果 |
//! |---|---|---|
//! | 两块「选择」按钮 | 把按下态图当模板在背景上滑，差异最小处即按钮矩形 | 左 `Prguse[66] @ (133,452)`、右 `Prguse[67] @ (685,453)`，**差 4.4**（次好 16+） |
//! | 面板间距 | 上面两个 x 之差 | **552** |
//! | 中间 5 个菜单项的行位置 | 面板内"亮像素行直方图"分组 | y ≈ 465 / 497 / 517 / 536 / 557 |
//! | 菜单项的按下态图 | 尺寸与文字宽度一一对应（2/4/4/3/2 个字 ↔ 44/120/120/92/56 像素宽） | `Prguse[68..72]` |
//! | 人物区（放小人） | 行/列**暗度剖面**（大块暗区即凹槽） | 左 `x 79..376`、右 `x 421..760`，`y 60..439` |
//! | 名字/等级/职业 三行 | 面板内亮框的行位置 | y ≈ 481 / 510 / 539（行距 29） |
//!
//! 动画部分（站立/石化/解冻）仍照原版 `IntroScn.pas`：
//! 站立基址 `40+Job*40+Sex*120`（16 帧、300ms）、石化基址 `60+…`（13 帧、50ms）、
//! 选中光效 `ChrSel[4..]`（14 帧）—— 这套素材的 `ChrSel.wil` 与原版**是同一套**
//!（真素材验收里逐帧验过）。

/// 素材编号与节拍。
pub struct Art;

impl Art {
    /// 选角背景（800×600，面板与按钮外观都画在图里）。`IntroScn.pas:1376`
    pub const BG: (&'static str, u32) = ("Prguse", 65);
    /// 左 / 右面板那颗「选择」的**按下态**。
    ///
    /// ⚠️ 是两张**不同**的图（不是同一张用两次）：实测 `66` 只匹配左边、`67` 只匹配右边。
    pub const SEL_DOWN: [(&'static str, u32); 2] = [("Prguse", 66), ("Prguse", 67)];
    /// 中间那列的 5 个菜单项**按下态**，顺序与 [`Menu`] 一一对应。
    /// 原版这几个号是「开始游戏/新建角色/删除角色/…/退出」，语义顺序相同、图不同。
    pub const MENU_DOWN: [(&'static str, u32); 5] = [
        ("Prguse", 68),
        ("Prguse", 69),
        ("Prguse", 70),
        ("Prguse", 71),
        ("Prguse", 72),
    ];
    /// 小人容器（`CHRSELIMAGEFILE`，`Share.pas:52`）。
    pub const CHR: &'static str = "ChrSel";

    /// 站立帧数（`SELECTEDFRAME`，`IntroScn.pas:12`）。
    pub const STAND_FRAMES: u32 = 16;
    /// 石化/解冻帧数（`FREEZEFRAME`，`IntroScn.pas:13`）。
    pub const FREEZE_FRAMES: u32 = 13;
    /// 光效帧数（`EFFECTFRAME`，`IntroScn.pas:14`）。
    pub const EFFECT_FRAMES: u32 = 14;
    /// 站立动画每帧毫秒（`IntroScn.pas:1503`）。
    pub const STAND_MS: u32 = 300;
    /// 解冻/石化每帧毫秒（`IntroScn.pas:1447 / 1466`）。
    pub const FREEZE_MS: u32 = 50;

    /// 每个账号几个角色槽（这套素材的面板就是两块）。
    pub const SLOTS: usize = 2;
}

/// 中间菜单的 5 项（顺序 = 从上到下 = [`Art::MENU_DOWN`] 的顺序）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Menu {
    /// 开始游戏。
    Start,
    /// 创建人物（新协议的服务端还没实现，见 protocol.md §11）。
    CreateChar,
    /// 删除人物（同上）。
    DeleteChar,
    /// 制作群（原版这个位置是 credits；这套素材写的是「制作群」）。
    Credits,
    /// 退出游戏。
    Exit,
}

/// 菜单顺序（给界面遍历用）。
pub const MENU: [Menu; 5] = [
    Menu::Start,
    Menu::CreateChar,
    Menu::DeleteChar,
    Menu::Credits,
    Menu::Exit,
];

// ---------- 实测版式（这套素材，800×600 坐标系）----------

/// 两块「选择」按钮：`(x, y, w, h)`，尺寸就是按下态图的尺寸（实测）。
const SEL_BTN: [(f32, f32, u32, u32); 2] = [(133.0, 452.0, 76, 33), (685.0, 453.0, 76, 33)];

/// 菜单项**横向中心**（5 项都居中在同一列上，实测 x 358..453）。
const MENU_X_CENTER: f32 = 405.0;
/// 菜单每一项的**顶**（实测：亮像素行分组的起点）。
const MENU_ROW_Y: [f32; 5] = [465.0, 497.0, 517.0, 536.0, 557.0];

/// 两块人物区（凹槽）：`(x0, y0, x1, y1)`。小人画在这里面。
const ALCOVE: [(f32, f32, f32, f32); 2] = [(79.0, 60.0, 376.0, 439.0), (421.0, 60.0, 760.0, 439.0)];
/// 小人的脚离凹槽底留多少像素（免得贴着面板边）。
const ALCOVE_FLOOR_MARGIN: f32 = 12.0;

/// 名字/等级/职业 三行的 **y**（面板内亮框的行位置，行距 29）。
const FIELD_Y: [f32; 3] = [481.0, 510.0, 539.0];
/// 三行文字的 **x**（左右两块面板各一个；值写在标签右边的框里）。
const FIELD_X: [f32; 2] = [98.0, 650.0];

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

    pub fn center(&self) -> (f32, f32) {
        (self.x + self.w / 2.0, self.y + self.h / 2.0)
    }
}

/// 一个槽里三行文字的位置。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct SlotText {
    pub name: (f32, f32),
    pub level: (f32, f32),
    pub class: (f32, f32),
}

/// 一帧的版式（全部屏幕绝对坐标；`bg` 是背景左上角）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct Layout {
    pub bg: (f32, f32),
    /// 两块「选择」按钮。
    pub sel: [Rect; 2],
    /// 中间 5 个菜单项（顺序同 [`MENU`]）。
    pub menu: [Rect; 5],
}

impl Layout {
    /// 按窗口尺寸与素材实测尺寸算版式。
    ///
    /// `measure` 由调用方给（只读容器头、不解压像素 ⇒ 可以每帧调）。
    /// 背景或任一按下态图缺失 ⇒ 整体 `None`（宁可写一行"素材缺失"，也不画一个
    /// 按钮跑偏的界面 —— 与登录界面同一条纪律）。
    pub fn build(
        win: (u32, u32),
        mut measure: impl FnMut(&'static str, u32) -> Option<(u32, u32)>,
    ) -> Option<Layout> {
        let (bw, bh) = measure(Art::BG.0, Art::BG.1)?;
        // 背景居中（素材 800×600，窗口可能更大）
        let bg = (
            (win.0 as f32 - bw as f32) / 2.0,
            (win.1 as f32 - bh as f32) / 2.0,
        );
        let at = |x: f32, y: f32, w: u32, h: u32| Rect {
            x: bg.0 + x,
            y: bg.1 + y,
            w: w as f32,
            h: h as f32,
        };
        // 「选择」按钮：位置是实测的，尺寸**再问一次素材**（改了素材就跟着走）
        let (w0, h0) = measure(Art::SEL_DOWN[0].0, Art::SEL_DOWN[0].1)?;
        let (w1, h1) = measure(Art::SEL_DOWN[1].0, Art::SEL_DOWN[1].1)?;
        let sel = [
            at(SEL_BTN[0].0, SEL_BTN[0].1, w0, h0),
            at(SEL_BTN[1].0, SEL_BTN[1].1, w1, h1),
        ];
        // 菜单：以实测尺寸居中到同一列上
        let mut menu = [Rect {
            x: 0.0,
            y: 0.0,
            w: 0.0,
            h: 0.0,
        }; 5];
        for i in 0..5 {
            let (mw, mh) = measure(Art::MENU_DOWN[i].0, Art::MENU_DOWN[i].1)?;
            menu[i] = at(MENU_X_CENTER - mw as f32 / 2.0, MENU_ROW_Y[i], mw, mh);
        }
        Some(Layout { bg, sel, menu })
    }
}

/// 站力图号：`40 + Job*40 + Sex*120 + k`（`IntroScn.pas:1483` 的 `120-80` 化简）。
pub fn stand_index(job: u8, sex: u8, k: u32) -> u32 {
    40 + job as u32 * 40 + (sex as u32 % 2) * 120 + (k % Art::STAND_FRAMES)
}

/// 石化/解冻图号：`60 + Job*40 + Sex*120 + k`（`IntroScn.pas:1440`）。
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
///（`MShare.pas:138-141`）—— 以字面量为准。入参用新协议的 `CharClass`（1/2/3）。
pub fn class_name(class: i32) -> &'static str {
    match class {
        1 => "战士",
        2 => "魔法师",
        3 => "道士",
        _ => "未知",
    }
}

/// 某个槽的**小人落点**（凹槽的底边中点）。
///
/// ⚠️ 原版是**按 (职业,性别) 各写死一套**坐标（`IntroScn.pas:1390-1429`），
/// 因为它的凹槽位置与人物图大小是配套调过的。这套素材的凹槽位置不同，
/// 与其猜 6 组偏移，不如**用同一条规则**：把小人图**不透明部分**的
/// 底边中点对齐到凹槽的底边中点（见 [`place_sprite`]）——
/// 它自动适应任何职业/性别（各自的图大小不同），也不需要维护一张表。
pub fn slot_anchor(slot: usize, bg: (f32, f32)) -> (f32, f32) {
    let (x0, _y0, x1, y1) = ALCOVE[slot.min(Art::SLOTS - 1)];
    (bg.0 + (x0 + x1) / 2.0, bg.1 + y1 - ALCOVE_FLOOR_MARGIN)
}

/// 一个槽里三行文字的位置。
pub fn slot_text(slot: usize, bg: (f32, f32)) -> SlotText {
    let i = slot.min(Art::SLOTS - 1);
    let x = bg.0 + FIELD_X[i];
    SlotText {
        name: (x, bg.1 + FIELD_Y[0]),
        level: (x, bg.1 + FIELD_Y[1]),
        class: (x, bg.1 + FIELD_Y[2]),
    }
}

/// 把一张**有透明边**的小人图摆到 `anchor`（底边中点）上。
///
/// `bbox` 是该图的**不透明包围盒**（`(x, y, w, h)`，图内坐标）——
/// 拿它而不是整张图的尺寸，是因为人物图的透明边各不相同，
/// 按整图对齐会让不同职业/性别的小人**高低不一**。
pub fn place_sprite(bbox: crate::wzl::BBox, anchor: (f32, f32)) -> (f32, f32) {
    let (bx, by, bw, bh) = bbox;
    (
        // 图内"底边中点" = (bx + bw/2, by + bh) ⇒ 落点 = 锚点 - 它
        anchor.0 - (bx as f32 + bw as f32 / 2.0),
        anchor.1 - (by + bh as i32) as f32,
    )
}

/// 一个槽该画哪张图、画在哪。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct SlotFrame {
    /// 图号（`Art::CHR` 容器里的）。
    pub index: u32,
    /// 画在**石化**位置（`true`）还是**站立**位置（`false`）。
    ///
    /// ⚠️ 这套素材里两者其实是**同一个落点**（凹槽），保留这个字段是为了
    /// 让状态机与绘制解耦：原版两者略有偏移，将来要还原也不用改接口。
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
///（`CliUtil.pas:749` 只接受 1..30）⇒ 选中那一刻不压暗；随后每 25ms 减 1，
/// 只在中间那几百毫秒里"略暗一下"，最后落到 0 又回到不压暗。
/// 主视觉是**站立 vs 石化**，所以这里只实现它 —— 少一个猜测出来的曲线。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct SlotAnim {
    phase: SlotPhase,
    k: u32,
    acc: u32,
    pub job: u8,
    pub sex: u8,
}

impl SlotAnim {
    /// 新槽：站立、第 0 帧、不选中。
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

    /// 玩家点了这个槽：重来一遍解冻（原版 `SelectChr` 也是无条件重置计时）。
    pub fn select(&mut self) {
        self.acc = 0;
        self.k = 0;
        self.phase = SlotPhase::Unfreezing;
    }

    /// 玩家选走了**别的**槽：这一槽开始石化。
    pub fn deselect(&mut self) {
        if self.phase == SlotPhase::Freeze || self.phase == SlotPhase::Freezing {
            return;
        }
        self.acc = 0;
        self.k = 0;
        self.phase = SlotPhase::Freezing;
    }

    /// 推进 `dt_ms` 毫秒；返回这一帧该画什么。
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
                let k = if selected { self.k } else { 0 };
                SlotFrame {
                    index: stand_index(self.job, self.sex, k),
                    at_frozen: false,
                    effect: None,
                }
            }
            SlotPhase::Unfreezing => {
                if self.k >= Art::FREEZE_FRAMES {
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
                SlotFrame {
                    index: freeze_index(self.job, self.sex, Art::FREEZE_FRAMES - 1 - self.k),
                    at_frozen: true,
                    effect: None,
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 照真素材量出来的尺寸（背景 800×600、按下态 66/67 76×33、菜单 68..72）。
    fn fake(container: &'static str, idx: u32) -> Option<(u32, u32)> {
        match (container, idx) {
            ("Prguse", 65) => Some((800, 600)),
            ("Prguse", 66) | ("Prguse", 67) => Some((76, 33)),
            ("Prguse", 68) => Some((44, 21)),
            ("Prguse", 69) | ("Prguse", 70) => Some((120, 21)),
            ("Prguse", 71) => Some((92, 20)),
            ("Prguse", 72) => Some((56, 20)),
            _ => None,
        }
    }

    /// 800×600 下版式要**正好**复现实测值 —— 这是"没抄错/没算错"的钉子。
    #[test]
    fn 八百乘六百复现实测值() {
        let l = Layout::build((800, 600), fake).expect("素材齐全");
        assert_eq!(l.bg, (0.0, 0.0));
        // 两块「选择」按钮（模板匹配定的）
        assert_eq!((l.sel[0].x, l.sel[0].y), (133.0, 452.0));
        assert_eq!((l.sel[1].x, l.sel[1].y), (685.0, 453.0));
        assert_eq!((l.sel[0].w, l.sel[0].h), (76.0, 33.0));
        // 面板间距 552（**不是**原版的 340）
        assert_eq!(l.sel[1].x - l.sel[0].x, 552.0);
        // 5 个菜单项：同一列居中，行位置是实测的
        for (i, r) in l.menu.iter().enumerate() {
            assert_eq!(r.y, MENU_ROW_Y[i], "第 {i} 项的顶不对");
            assert_eq!(
                r.x + r.w / 2.0,
                MENU_X_CENTER,
                "第 {i} 项没有居中到同一列上"
            );
        }
        // 菜单项之间的重叠**允许 2 像素**：素材的按钮高 21，而实测行距是 19~20
        //（497 → 517 → 536），本来就会叠 1~2 像素 —— 原版那套也是叠着的。
        // 叠超过 2 像素才说明行位置量错了（量的时候是按"亮像素行分组"取的，精度 ±1）。
        for i in 1..5 {
            let overlap = l.menu[i - 1].y + l.menu[i - 1].h - l.menu[i].y;
            assert!(overlap <= 2.0, "菜单第 {i} 项与上一项叠了 {overlap} 像素");
        }
    }

    /// 窗口变大时整套界面**跟着背景一起平移**。
    #[test]
    fn 窗口变大时整体平移() {
        let l800 = Layout::build((800, 600), fake).unwrap();
        let l = Layout::build((1024, 768), fake).unwrap();
        assert_eq!(l.bg, (112.0, 84.0));
        for i in 0..2 {
            assert_eq!(
                (l.sel[i].x - l800.sel[i].x, l.sel[i].y - l800.sel[i].y),
                (112.0, 84.0)
            );
        }
        for i in 0..5 {
            assert_eq!(
                (l.menu[i].x - l800.menu[i].x, l.menu[i].y - l800.menu[i].y),
                (112.0, 84.0)
            );
        }
        // 小人落点与文字也跟着走
        let a = slot_anchor(0, l.bg);
        let a800 = slot_anchor(0, l800.bg);
        assert_eq!((a.0 - a800.0, a.1 - a800.1), (112.0, 84.0));
        let t = slot_text(0, l.bg);
        let t800 = slot_text(0, l800.bg);
        assert_eq!(
            (t.name.0 - t800.name.0, t.name.1 - t800.name.1),
            (112.0, 84.0)
        );
    }

    /// 小人落点：凹槽的**底边中点**（左右各一块，且都在凹槽横向范围内）。
    #[test]
    fn 小人落点在凹槽里() {
        for (slot, &(x0, _y0, x1, y1)) in ALCOVE.iter().enumerate() {
            let (ax, ay) = slot_anchor(slot, (0.0, 0.0));
            assert!(
                (x0..=x1).contains(&ax),
                "槽 {slot} 的落点 {ax} 不在凹槽 {x0}..{x1} 里"
            );
            assert!(ay < y1, "槽 {slot} 的落点该在凹槽底边之上（留了脚距）");
            assert_eq!(ay, y1 - ALCOVE_FLOOR_MARGIN);
        }
        // 三行文字：从上到下 名字 → 等级 → 职业，且同一槽同 x
        let t = slot_text(0, (0.0, 0.0));
        assert!(t.name.1 < t.level.1 && t.level.1 < t.class.1);
        assert_eq!(
            (t.name.0, t.level.0, t.class.0),
            (FIELD_X[0], FIELD_X[0], FIELD_X[0])
        );
        // 左面板的文字 x 在面板内（面板 x 15..250 一带），右边同理
        assert!((15.0..=250.0).contains(&FIELD_X[0]));
        assert!((575.0..=800.0).contains(&FIELD_X[1]));
    }

    /// `place_sprite`：把**不透明包围盒**的底边中点摆到锚点上。
    #[test]
    fn 小人按不透明包围盒对齐() {
        // 图 100×120，不透明部分 (10,20,40,60) ⇒ 底边中点在图内 (30, 80)
        let (x, y) = place_sprite((10, 20, 40, 60), (200.0, 400.0));
        // 图内不透明部分的底边中点是 (10+20, 20+60) = (30, 80)
        assert_eq!(x, 170.0, "应让图中 x=30 落在锚点 x=200 上");
        assert_eq!(y, 320.0, "应让图中 y=80 落在锚点 y=400 上");
        // **不变式**：不管透明边在哪，不透明部分的底边中点都落在锚点上。
        //（这才是"不同职业/性别的小人不会高低不一"的依据 —— 各自的落点本来就该不同。）
        for bbox in [(0, 0, 40, 60), (50, 50, 40, 60), (10, 20, 40, 60)] {
            let (x, y) = place_sprite(bbox, (200.0, 400.0));
            let (bx, by, bw, bh) = bbox;
            assert_eq!(
                x + (bx + bw as i32 / 2) as f32,
                200.0,
                "{bbox:?} 的 x 没对上"
            );
            assert_eq!(y + (by + bh as i32) as f32, 400.0, "{bbox:?} 的底边没对上");
        }
    }

    /// 图号公式：基址 + 槽内偏移，站立段与石化段不重叠。
    #[test]
    fn 图号公式() {
        assert_eq!(stand_index(0, 0, 0), 40);
        assert_eq!(stand_index(1, 0, 0), 80);
        assert_eq!(stand_index(2, 0, 0), 120);
        assert_eq!(stand_index(0, 1, 0), 160);
        assert_eq!(stand_index(1, 1, 0), 200);
        assert_eq!(stand_index(2, 1, 0), 240);
        assert_eq!(stand_index(0, 0, 15), 55);
        assert_eq!(freeze_index(0, 0, 0), 60);
        assert_eq!(freeze_index(2, 1, 12), 60 + 80 + 120 + 12);
        for job in 0..3u8 {
            for sex in 0..2u8 {
                assert!(stand_index(job, sex, Art::STAND_FRAMES - 1) < freeze_index(job, sex, 0));
            }
        }
        assert_eq!(effect_index(0), 4);
        assert_eq!(effect_index(Art::EFFECT_FRAMES), 4);
        assert_eq!(stand_index(0, 0, Art::STAND_FRAMES), stand_index(0, 0, 0));
    }

    /// 职业名照原版**字面量**。
    #[test]
    fn 职业名() {
        assert_eq!(class_name(1), "战士");
        assert_eq!(class_name(2), "魔法师");
        assert_eq!(class_name(3), "道士");
        assert_eq!(class_name(0), "未知");
    }

    /// 动画状态机：选中 ⇒ 解冻 13 帧 ⇒ 站立；取消 ⇒ 倒放石化的 13 帧 ⇒ 定格。
    #[test]
    fn 选中与取消的动画阶段() {
        let mut s = SlotAnim::new(0, 0);
        let f = s.tick(0, false);
        assert_eq!(s.phase(), SlotPhase::Stand);
        assert_eq!(f.index, stand_index(0, 0, 0));
        assert!(f.effect.is_none());

        s.select();
        assert_eq!(s.phase(), SlotPhase::Unfreezing);
        let f = s.tick(0, true);
        assert_eq!(f.index, freeze_index(0, 0, 0));
        assert_eq!(f.effect, Some(4));
        let f = s.tick(150, true);
        assert_eq!((f.index, f.effect), (freeze_index(0, 0, 3), Some(7)));
        for _ in 0..12 {
            s.tick(50, true);
        }
        assert_eq!(s.phase(), SlotPhase::Stand);

        // 站立动画：每 300ms 进一帧
        let mut s = SlotAnim::new(1, 0);
        s.select();
        for _ in 0..13 {
            s.tick(50, true);
        }
        s.tick(250, true);
        assert_eq!(s.tick(0, true).index, stand_index(1, 0, 0), "还没到 300ms");
        s.tick(50, true);
        assert_eq!(
            s.tick(0, true).index,
            stand_index(1, 0, 1),
            "过了 300ms 该进帧"
        );

        // 取消 ⇒ 石化倒放
        s.deselect();
        assert_eq!(s.phase(), SlotPhase::Freezing);
        assert_eq!(
            s.tick(0, false).index,
            freeze_index(1, 0, Art::FREEZE_FRAMES - 1)
        );
        for _ in 0..Art::FREEZE_FRAMES {
            s.tick(50, false);
        }
        assert_eq!(s.phase(), SlotPhase::Freeze);
        assert_eq!(s.tick(0, false).index, freeze_index(1, 0, 0));
        let f1 = s.tick(999, false);
        assert_eq!(f1, s.tick(999, false));
    }

    /// 未选中的槽**不播站立动画**（原版只在 `Selected` 时进帧）。
    #[test]
    fn 未选中不播站立动画() {
        let mut s = SlotAnim::new(0, 0);
        let a = s.tick(1000, false);
        assert_eq!(a, s.tick(1000, false));
        assert_eq!(a.index, stand_index(0, 0, 0));
    }

    /// 点空处不算命中。
    #[test]
    fn 命中判定() {
        let l = Layout::build((800, 600), fake).unwrap();
        assert!(l.sel[0].hit(l.sel[0].center()));
        assert!(l.sel[1].hit(l.sel[1].center()));
        for r in &l.menu {
            assert!(r.hit(r.center()));
        }
        assert!(!l.sel[0].hit((l.sel[0].x - 1.0, l.sel[0].y)));
        // 两块「选择」互不重叠；菜单与「选择」也不重叠（一个在左上一个在中间）
        assert!(l.sel[0].x + l.sel[0].w < l.sel[1].x);
        for r in &l.menu {
            assert!(r.y >= 460.0 && r.y + r.h <= 600.0);
        }
    }

    /// 缺素材就不出版式。
    #[test]
    fn 缺素材就不出版式() {
        let missing = |c: &'static str, i: u32| {
            if (c, i) == ("Prguse", 69) {
                None
            } else {
                fake(c, i)
            }
        };
        assert!(Layout::build((800, 600), missing).is_none());
    }

    /// **真素材验收**：两件事都钉住 ——
    ///
    /// 1. 用到的图都真的在容器里，且小人 3×2×16/13 帧全解得出（与原版同一套 `ChrSel`）；
    /// 2. **最重要的**：两块「选择」按钮的**按下态图，在实测位置上与背景几乎逐像素相同**
    ///    —— 这一条直接证明"点击区/按下态与背景图对齐"（用户报的那个 bug），
    ///    而且改素材/改坐标时它会红。
    #[test]
    fn 真素材_按钮与背景对齐() {
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
        let dir = std::path::PathBuf::from(&dir);
        let mut libs: HashMap<&'static str, Wzl> = HashMap::new();

        // 图都在
        for (name, idx, what) in [
            (Art::BG.0, Art::BG.1, "选角背景"),
            (Art::SEL_DOWN[0].0, Art::SEL_DOWN[0].1, "左「选择」按下态"),
            (Art::SEL_DOWN[1].0, Art::SEL_DOWN[1].1, "右「选择」按下态"),
            (Art::MENU_DOWN[0].0, Art::MENU_DOWN[0].1, "[开始]"),
            (Art::MENU_DOWN[1].0, Art::MENU_DOWN[1].1, "[创建人物]"),
            (Art::MENU_DOWN[2].0, Art::MENU_DOWN[2].1, "[删除人物]"),
            (Art::MENU_DOWN[3].0, Art::MENU_DOWN[3].1, "[制作群]"),
            (Art::MENU_DOWN[4].0, Art::MENU_DOWN[4].1, "[退出]"),
        ] {
            assert!(
                dec(&mut libs, &dir, name, idx).is_some_and(|s| !s.is_empty()),
                "{what} = {name}[{idx}] 取不出图"
            );
        }

        // **按下态 vs 背景**：逐像素比，平均差要很小
        let bg = dec(&mut libs, &dir, Art::BG.0, Art::BG.1).expect("背景");
        for (slot, (name, idx)) in Art::SEL_DOWN.iter().enumerate() {
            let t = dec(&mut libs, &dir, name, *idx).expect("按下态");
            let (bx, by, _w, _h) = SEL_BTN[slot];
            let (mut sum, mut n) = (0u64, 0u64);
            for y in 0..t.height as i32 {
                for x in 0..t.width as i32 {
                    let ti = ((y * t.width as i32 + x) * 4) as usize;
                    if t.rgba[ti + 3] < 128 {
                        continue;
                    }
                    let px = bx as i32 + x;
                    let py = by as i32 + y;
                    assert!(
                        px < bg.width as i32 && py < bg.height as i32,
                        "按钮跑到图外了"
                    );
                    let bi = ((py * bg.width as i32 + px) * 4) as usize;
                    for c in 0..3 {
                        sum +=
                            (bg.rgba[bi + c] as i32 - t.rgba[ti + c] as i32).unsigned_abs() as u64;
                    }
                    n += 3;
                }
            }
            let diff = sum as f32 / n as f32;
            assert!(
                diff < 8.0,
                "槽 {slot} 的「选择」按下态与背景差 {diff:.2} —— 位置或图号错了？\
                 （实测应当是 4.4 左右；背景上那颗按钮就在实测位置）"
            );
        }

        // 小人：3 职业 × 2 性别 × 站立 16 帧 / 石化 13 帧 / 光效 14 帧
        for job in 0..3u8 {
            for sex in 0..2u8 {
                for k in 0..Art::STAND_FRAMES {
                    let i = stand_index(job, sex, k);
                    assert!(
                        dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()),
                        "站立 Job={job} Sex={sex} 第 {k} 帧（ChrSel[{i}]）没有图"
                    );
                }
                let a = dec(&mut libs, &dir, Art::CHR, stand_index(job, sex, 0)).unwrap();
                let b = dec(&mut libs, &dir, Art::CHR, stand_index(job, sex, 1)).unwrap();
                assert_ne!(a.rgba, b.rgba, "Job={job} Sex={sex}：站立第 0/1 帧一样");
                let c = dec(&mut libs, &dir, Art::CHR, freeze_index(job, sex, 0)).unwrap();
                assert_ne!(a.rgba, c.rgba, "Job={job} Sex={sex}：石化帧与站立帧一样");
            }
        }
        for k in 0..Art::EFFECT_FRAMES {
            let i = effect_index(k);
            assert!(dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()));
        }
    }
}

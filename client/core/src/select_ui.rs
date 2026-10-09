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
//! | 中间 5 个菜单项 | **同一套模板匹配**：`Prguse[68..72]` 当模板在背景上滑（差 16.6/8.9/12.1/14.3/14.2，次好 23+） | 左上角 = **(385/348/347/362/379, 456/486/506/527/547)**（2026-10-08 重测，见下） |
//! | 菜单项的按下态图 | 尺寸与文字宽度一一对应（2/4/4/3/2 个字 ↔ 44/120/120/92/56 像素宽） | `Prguse[68..72]` |
//! | 人物区（放小人） | 行/列**暗度剖面**（`lum<45` 占比 >0.75 的连续区 = 凹槽暗腔） | 左 `x 92..377`、右 `x 421..725`，`y 58..402`（底线 = 凹槽地台） |
//! | 名字/等级/职业 的值框 | 面板内**亮底线**（`lum>110` 的整行）定位框底，再按框高 20 反推框顶 | 框底 y ≈ 508.5 / 537.5 / 568，框 `x 108..211`（右面板 +552）；文字落点 = 框顶 + 2.5 |
//!
//! ⚠️ **2026-10-08 重测**（用户第二次报"按钮/文字位置偏了"）：菜单那 5 行的 y 原先是
//! `465/497/517/536/557`（用"亮像素行直方图分组"量的）——那套量法量的是**文字行**，
//! 而绘制用的是**按下态图**（图的顶比文字顶高 3px），于是按下态整体低 8~10px；
//! 值框那三行原先是 `481/510/539`（同样只量到"亮框行"），字被画在**框外上方**。
//! 现在两者都改成**用绘制时真正要用的那张图/那个框**去定位，并由
//! `真素材_菜单按下态与背景对齐` 与 `真素材_三行值框与文字落点` 两条测试钉住。
//!
//! ⚠️ **同日第二轮**（用户报"创建人物/制作群的按下态左偏 1px"）：五项的中心实测是
//! `407/408/407/408/407`，**不是一个数** ⇒ 改成逐项存左上角（见 `MENU_X`）。
//!
//! ⚠️ **同日第三轮**（用户报"左侧在石化状态下往左偏 2px"）：石化帧的落点改成
//! "站立第 0 帧的基准 + **原版的手调量**"（[`frozen_offset`]）——
//! 不再拿石化图自己的包围盒去居中（那样对男角会差 0.5~3.5px，因为包围盒中点 ≠ 人物看起来的中心）。
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
    /// 站立动画每帧毫秒。
    ///
    /// **原版是 300**（`IntroScn.pas:1503`：`if ChrArr[n].Selected then ... > 300`），
    /// 16 帧 = 4.8 秒一圈 —— 与用户口径一致（"选中后的动画间隔是 300ms"）。
    ///
    /// ⚠️ 曾被擅自改成 150（"看着顺眼"），2026-10-08 按原版与用户要求改回 300。
    /// 这不是审美问题：节奏是**契约**的一部分，改它要用户点头。
    pub const STAND_MS: u32 = 300;
    /// 解冻/石化每帧毫秒（`IntroScn.pas:1447 / 1466`）。
    pub const FREEZE_MS: u32 = 50;

    /// 每个账号几个角色槽（这套素材的面板就是两块）。
    pub const SLOTS: usize = 2;

    /// **「开始」那颗钮的石台**（用户 2026-10-09 要的：官方选角图里"开始"下面垫着一块石台）。
    ///
    /// # 它从哪来（实测，不是猜）
    ///
    /// 我们现在用的 1.76 底图 `Prguse[65]` 里**没有**这块台子 —— 那五项菜单就是烘在底图上的
    /// 纯文字。但它就在**同一个容器**里：新版底图 `Prguse2[206]`（无台）与 `Prguse2[480]`
    /// （有台）**只差这一块**：逐像素比对，差异区 `x 365..447, y 447..486`（83×40），
    /// **其余平均差 0.0**。而 `Prguse[65]` 与 `Prguse2[206]` 除菜单文字外也几乎相同
    ///（凹槽区平均差 0.0、整图 5.9）⇒ 两版底图是同一套版式 ⇒ 把这块**搬过来**正好落在
    /// "开始"上（台心 406 vs 钮心 407）。
    ///
    /// ⇒ **只搬台子，保留 1.76 的文案**（用户选的这条）。换整张新版底图虽然更"原样"，
    /// 但会把菜单第二项从"制作群"变成"恢复人物"（那是另一个功能）。
    ///
    /// ⚠️ 别把它当"按下态"用：它是**常态**。（1.76 原版的菜单图 `Prguse[68..72]` 是纯文字、
    /// 且 `TDControl.DirectPaint` 不判 `Downed` ⇒ 原版这几项本来就没有"按下换图"。）
    pub const START_PLATE: (&'static str, u32) = ("Prguse2", 480);
}

/// 中间菜单的 5 项（顺序 = 从上到下 = [`Art::MENU_DOWN`] 的顺序）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Menu {
    /// 开始游戏。
    Start,
    /// 创建人物（已接上：开 [`DialogBox`] 对话框，见 D-35）。
    CreateChar,
    /// 删除人物（已接上：弹"删了不可恢复"的确认，见 D-35）。
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

/// 菜单每一项的**左上角 x**（模板匹配实测的按下态图左边缘，逐项给）。
///
/// ⚠️ **不要**改回"同一个列中心"：五项的中心实测是 `407/408/407/408/407`（不是同一个数），
/// 取一个 407 会让 `创建人物`、`制作群` 各**左偏 1px**（用户看得出来的那种偏，
/// 2026-10-08 报过）。素材是逐项画在背景上的整数位置，就逐项存。
const MENU_X: [f32; 5] = [385.0, 348.0, 347.0, 362.0, 379.0];
/// 菜单每一项的**顶**（= 按下态图 `Prguse[68..72]` 的左上角 y，模板匹配实测）。
const MENU_ROW_Y: [f32; 5] = [456.0, 486.0, 506.0, 527.0, 547.0];

/// 两块人物区（凹槽）的**暗腔**：`(x0, y0, x1, y1)`。小人画在这里面。
///
/// `y1` = 凹槽地台（小人脚踩的那条线，实测 `lum<45` 的暗腔下沿）。
const ALCOVE: [(f32, f32, f32, f32); 2] = [(92.0, 58.0, 377.0, 402.0), (421.0, 58.0, 725.0, 402.0)];
/// 小人的脚离地台留多少像素（免得贴着石头边，1~2px 就够）。
const ALCOVE_FLOOR_MARGIN: f32 = 2.0;

/// 名字/等级/职业 三行的 **y**（= 值框的框顶 + 2.5，让 14px 的字在 20px 的框里居中）。
///
/// ⚠️ 框底实测 508.5 / 537.5 / 568（框高 20）⇒ 框顶 488.5 / 517.5 / 548。
const FIELD_Y: [f32; 3] = [491.0, 520.0, 551.0];
/// 三行文字的 **x**（左面板值框 `x 108..211` 内留 5px；右面板整个 +552）。
const FIELD_X: [f32; 2] = [113.0, 665.0];

/// [`Art::START_PLATE`] 在设计空间（800×600）里的**源矩形** `(x, y, w, h)`。
///
/// 就是上面量出来的差异区（阈值收紧到 8 后收敛到 `365..447 × 447..486`）。
/// 由 `真素材_开始石台就是那两块底图的差` 钉住 —— 换了底图/图号这条会红。
pub const START_PLATE_SRC: (f32, f32, f32, f32) = (365.0, 447.0, 83.0, 40.0);

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
    /// 窗口尺寸（`f32` 版）。
    ///
    /// 存在的理由：**建角对话框与消息框是"居中"的**，命中测试要按窗口尺寸算它们的位置，
    /// 而命中测试那条路上只有 `Layout`（`Select::on_up` 的签名）—— 少了它就得把
    /// `ui`/`dir` 一路传进去，或者让几何依赖素材（见 [`DialogBox`] 的说明）。
    pub win: (f32, f32),
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
        // 菜单：左上角是逐项实测的（见 `MENU_X`），尺寸**再问一次素材**
        let mut menu = [Rect {
            x: 0.0,
            y: 0.0,
            w: 0.0,
            h: 0.0,
        }; 5];
        for i in 0..5 {
            let (mw, mh) = measure(Art::MENU_DOWN[i].0, Art::MENU_DOWN[i].1)?;
            menu[i] = at(MENU_X[i], MENU_ROW_Y[i], mw, mh);
        }
        Some(Layout {
            bg,
            sel,
            menu,
            win: (win.0 as f32, win.1 as f32),
        })
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

/// 某个槽的**小人落点**（凹槽地台的中点）。
///
/// ⚠️ 原版是**按 (职业,性别) 各写死一套**坐标（`IntroScn.pas:1390-1429`），
/// 因为它的凹槽位置与人物图大小是配套调过的。这套素材的凹槽位置不同，
/// 与其猜 6 组偏移，不如**用同一条规则**：把小人图**不透明部分**的
/// 底边中点对齐到凹槽地台的中点（见 [`place_sprite`]）——
/// 它自动适应任何职业/性别（各自的图大小不同），也不需要维护一张表。
///
/// 校验：本套素材战士(男) = `IntroScn.pas` 里写死的 `by = 75-23 = 52`
/// （脚 y=400 = 地台），与这里算出来的**逐像素相同** —— 说明这条规则就是原版那条。
pub fn slot_anchor(slot: usize, bg: (f32, f32)) -> (f32, f32) {
    let (x0, _y0, x1, y1) = ALCOVE[slot.min(Art::SLOTS - 1)];
    (bg.0 + (x0 + x1) / 2.0, bg.1 + y1 - ALCOVE_FLOOR_MARGIN)
}

/// 「开始」那颗石台该画在哪（**设计空间**）—— 居中压在菜单第一项上。
///
/// 用 `menu[0]`（= [`Menu::Start`]）而不是写死 `(365,447)`：以后版式一动它跟着走；
/// 与实测的原位（台心 406 / 钮心 407）差不到 1px，肉眼看不出。
pub fn start_plate_at(menu0: Rect) -> Rect {
    let (_, _, w, h) = START_PLATE_SRC;
    Rect {
        x: menu0.x + menu0.w / 2.0 - w / 2.0,
        y: menu0.y + menu0.h / 2.0 - h / 2.0,
        w,
        h,
    }
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

/// 「**石化图相对站立图**」的偏移 —— 逐字照抄原版（`IntroScn.pas:1487-1524` 的
/// `bx - fx` / `by - fy`，两处都是「石化位置 − 站立位置」）。
///
/// 男角与战士女的石化图**取景与站立图一致**（原版那两处坐标完全相同）⇒ `(0,0)`；
/// 法师女 / 道士女的石化图是**紧贴裁切**的（包围盒从 `(0,0)` 起，
/// 而站立图留着 `(31,14)` 那样的透明边）⇒ 原版用手调量把人物摆回同一处：
///
/// ```text
/// 法师女：bx := 141+30;  fx := bx-30;   by := 85+14-2;  fy := by-14;   ⇒ (30, 14)
/// 道士女：bx := 141+23;  fx := bx-23;   by := 85+20-2;  fy := by-20;   ⇒ (23, 20)
/// ```
///
/// ⚠️ **不要**改成"按包围盒各自居中"：那样对男角会各差 0.5~3.5px
///（包围盒中点 ≠ 人物看起来的中心：石化是抱臂/低头，站立在摆手），
/// 表现就是用户 2026-10-08 报的"左侧在石化状态下往左偏了 2 像素"。
/// 原版给的是**手调量**，照抄它才与官网客户端逐像素一致。
pub fn frozen_offset(job: u8, sex: u8) -> (f32, f32) {
    match (job.min(2), sex & 1) {
        (1, 1) => (30.0, 14.0), // 法师女
        (2, 1) => (23.0, 20.0), // 道士女
        _ => (0.0, 0.0),        // 男角两个 + 战士女
    }
}

/// 一个槽的**小人 / 光效落点备忘**。
///
/// # 小人的规则（两条，都是"照原版"）
///
/// 1. **基准** = `(job, sex)` 的**站立第 0 帧**：把它"不透明包围盒的底边中点"落到锚点
///    （凹槽地台中点）。整个槽只在 `(job, sex)` 变了时重算一次。
///    ⚠️ 不能逐帧算：站立 16 帧的上半身在摆，包围盒中点 x 会游走
///    （实测战士男 185.0~188.0、战士女最多 **13.5px**）⇒ 已经站定的**脚**会被推着挪
///    （用户 2026-10-08 报的"动画时脚部在左右移动"）。
/// 2. **石化帧** = 基准 + [`frozen_offset`]（原版的手调量）。
///
/// ⚠️ 基准**固定**用站立第 0 帧，而不是"当前这一帧"：槽一进来就是石化的（`new_frozen`），
/// 拿当前帧当基准会随"先看到哪个状态"而漂 —— 也就是"选中那一刻整体跳一下"
/// （用户 2026-10-08 报的"右侧人物选中后向右了 20 像素"）。
///
/// # 光效
///
/// 独立一个格子（那套图与 `(job, sex)` 无关），同样一帧定死 ——
/// 光效自己那 14 帧的包围盒也在长，逐帧对齐会让整团光左右抖。
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub struct SlotPlace {
    /// 每个槽：`(job, sex, 锚点 → 站立图左上角的偏移)`。
    at: [Option<SlotKey>; Art::SLOTS],
    /// 每个槽：光效的 `锚点 → 图左上角`。
    fx: [Option<(f32, f32)>; Art::SLOTS],
}

/// 一个槽记住的东西：`(job, sex, 锚点 → 站立图左上角的偏移)`。
type SlotKey = (u8, u8, (f32, f32));

impl SlotPlace {
    /// 小人这一帧画在哪。`stand0` = **站立第 0 帧**的包围盒（基准，与当前帧无关）；
    /// `frozen` = 这一帧是不是石化家族。
    pub fn of(
        &mut self,
        slot: usize,
        job: u8,
        sex: u8,
        frozen: bool,
        stand0: crate::wzl::BBox,
        anchor: (f32, f32),
    ) -> (f32, f32) {
        let i = slot.min(Art::SLOTS - 1);
        let base = match self.at[i] {
            Some((j, s, b)) if (j, s) == (job, sex) => b,
            _ => {
                // 存**相对锚点的偏移**（而不是绝对落点）：窗口变大时锚点会动，
                // 绝对落点会僵在原地 —— 必须跟着锚点走。
                let b = place_sprite(stand0, (0.0, 0.0));
                self.at[i] = Some((job, sex, b));
                b
            }
        };
        let (dx, dy) = if frozen {
            frozen_offset(job, sex)
        } else {
            (0.0, 0.0)
        };
        (anchor.0 + base.0 + dx, anchor.1 + base.1 + dy)
    }

    /// 光效这一帧画在哪（一帧定死，见类型说明）。
    pub fn fx(&mut self, slot: usize, bbox: crate::wzl::BBox, anchor: (f32, f32)) -> (f32, f32) {
        let i = slot.min(Art::SLOTS - 1);
        let d = match self.fx[i] {
            Some(d) => d,
            None => {
                let d = place_sprite(bbox, (0.0, 0.0));
                self.fx[i] = Some(d);
                d
            }
        };
        (anchor.0 + d.0, anchor.1 + d.1)
    }
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

    /// 新槽，**直接是石化定格**。
    ///
    /// 进选角界面时用：原版 `OpenScene` 什么都不做 ⇒ 两个都站着、都不石化，
    /// 于是**看不出当前选的是谁**（而且这时点[开始]会被服务端拒掉，因为
    /// `Selected` 都还是 FALSE）。我们改成"默认选中第一个、其余石化"——
    /// 一眼能看出当前是哪个，也避免上面那个空选状态。
    pub fn new_frozen(job: u8, sex: u8) -> Self {
        SlotAnim {
            phase: SlotPhase::Freeze,
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

/// 通用**消息框**（`Prguse[360]` 背景 + `[363]` 的 [Ok]）的几何：**居中**，
/// [Ok] 在框底居中、离底 8px（与登录界面 `login_ui::Layout` 里那条规则同一份）。
///
/// ⚠️ 选角这边要它，是因为**弹窗的 [Ok] 得能点**：早先弹窗是画在 `draw_msgbox` 里的，
/// 几何只存在于绘制路径 ⇒ 命中测试（[`Select::on_up`]）压根不知道有这颗按钮，
/// 于是"确定按钮不能点击、只有回车有效"（用户 2026-10-08 报的）。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct MsgBox {
    /// 框（居中）。
    pub frame: Rect,
    /// [Ok] 那颗。
    pub ok: Rect,
}

impl MsgBox {
    /// `frame_size` / `ok_size` 由调用方从素材实测（缺素材就传 `(0,0)`）。
    pub fn build(win: (u32, u32), frame_size: (u32, u32), ok_size: (u32, u32)) -> Self {
        let (fw, fh) = (frame_size.0 as f32, frame_size.1 as f32);
        let (ow, oh) = (ok_size.0 as f32, ok_size.1 as f32);
        let (bx, by) = ((win.0 as f32 - fw) / 2.0, (win.1 as f32 - fh) / 2.0);
        MsgBox {
            frame: Rect {
                x: bx,
                y: by,
                w: fw,
                h: fh,
            },
            ok: Rect {
                x: bx + (fw - ow) / 2.0,
                y: by + fh - oh - 8.0,
                w: ow,
                h: oh,
            },
        }
    }

    /// 点在这颗 [Ok] 上吗。
    pub fn hit_ok(&self, p: (f32, f32)) -> bool {
        self.ok.w > 0.0 && self.ok.h > 0.0 && self.ok.hit(p)
    }
}

/// 建角对话框里被点中的东西（[`DialogBox::hit`]）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DialogHit {
    /// 姓名输入框（点它 = 把焦点交给名字）。
    Name,
    /// 三个职业按钮（0/1/2 = 战/法/道）。
    Job(usize),
    /// 两个性别按钮（0 男 / 1 女）。
    Sex(usize),
    /// `[确定]`（原版 `DccOk`，图 51）。
    Ok,
    /// `[关闭]`（原版 `DccClose`，图 52）。
    Close,
}

/// 「新建角色」对话框的版式（原版 `DCreateChr`，`IntroScn.pas:1268-1288`）。
///
/// ⚠️ **不依赖素材**：原版那个窗口的尺寸与控件坐标在 `FState.pas:932-965`，是 800×600
/// 的老界面；我们这套界面是 1024×768，而 `Prguse[73]`（那扇窗口的背景图）也还没接进
/// `Art` ⇒ 这里用**固定尺寸居中**画一个功能同构的小框（姓名行 + 职业三颗 + 性别两颗 +
/// 确定/关闭）。**没有逐像素对拍过** —— 记在这儿，免得以后有人当它是"照原版搬的"。
///
/// 为什么几何要放在 core：它得在**画**和**命中测试**两处用同一份坐标，而命中测试那条路
/// （`Select::on_up`）只有 `Layout` ⇒ 见 [`Layout::win`]。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct DialogBox {
    pub frame: Rect,
    /// 姓名那一行（输入框）。
    pub name: Rect,
    pub job: [Rect; 3],
    pub sex: [Rect; 2],
    pub ok: Rect,
    pub close: Rect,
}

impl DialogBox {
    /// 居中排一个（窗口再小也不会把框挤出屏幕外）。
    pub fn build(win: (u32, u32)) -> Self {
        const W: f32 = 360.0;
        const H: f32 = 236.0;
        let bx = ((win.0 as f32 - W) / 2.0).max(8.0);
        let by = ((win.1 as f32 - H) / 2.0).max(8.0);
        let pad = 24.0;
        let name = Rect {
            x: bx + pad,
            y: by + 52.0,
            w: W - pad * 2.0,
            h: 26.0,
        };
        let bw = 68.0;
        let job = std::array::from_fn(|i| Rect {
            x: bx + pad + i as f32 * (bw + 10.0),
            y: name.y + name.h + 26.0,
            w: bw,
            h: 24.0,
        });
        let sex = std::array::from_fn(|i| Rect {
            x: bx + pad + i as f32 * (bw + 10.0),
            y: job[0].y + job[0].h + 20.0,
            w: bw,
            h: 24.0,
        });
        let btn_w = 76.0;
        let ok = Rect {
            x: bx + W / 2.0 - btn_w - 8.0,
            y: by + H - 38.0,
            w: btn_w,
            h: 26.0,
        };
        let close = Rect {
            x: bx + W / 2.0 + 8.0,
            y: ok.y,
            w: btn_w,
            h: 26.0,
        };
        Self {
            frame: Rect {
                x: bx,
                y: by,
                w: W,
                h: H,
            },
            name,
            job,
            sex,
            ok,
            close,
        }
    }

    /// 点中了什么（先判下面那排按钮，再判输入框与选项；没点中 ⇒ `None`）。
    pub fn hit(&self, p: (f32, f32)) -> Option<DialogHit> {
        if self.ok.hit(p) {
            return Some(DialogHit::Ok);
        }
        if self.close.hit(p) {
            return Some(DialogHit::Close);
        }
        if self.name.hit(p) {
            return Some(DialogHit::Name);
        }
        for (i, r) in self.job.iter().enumerate() {
            if r.hit(p) {
                return Some(DialogHit::Job(i));
            }
        }
        for (i, r) in self.sex.iter().enumerate() {
            if r.hit(p) {
                return Some(DialogHit::Sex(i));
            }
        }
        None
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
        // 5 个菜单项：左上角是**逐项**实测的（中心本来就不在同一个 x 上：407/408/407/408/407）
        for (i, r) in l.menu.iter().enumerate() {
            assert_eq!(
                (r.x, r.y),
                (MENU_X[i], MENU_ROW_Y[i]),
                "第 {i} 项的左上角不对"
            );
        }
        // 五项横向几乎同一列：中心最多差 1px（差多了说明 `MENU_X` 抄错了）
        let centers: Vec<f32> = l.menu.iter().map(|r| r.x + r.w / 2.0).collect();
        let (lo, hi) = (
            centers.iter().cloned().fold(f32::MAX, f32::min),
            centers.iter().cloned().fold(f32::MIN, f32::max),
        );
        assert!(hi - lo <= 1.0, "五项的中心差了 {}px：{centers:?}", hi - lo);
        // 菜单项之间的重叠**允许 2 像素**：素材的按钮高 21，而实测行距是 20~21
        //（486 → 506 → 527 → 547），本来就会叠 1~2 像素 —— 原版那套也是叠着的。
        // 叠超过 2 像素才说明行位置量错了（量的时候用模板匹配，精度 ±1）。
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

    /// **落点一帧定死**：基准只认"站立第 0 帧"，之后再问多少次都不重算
    /// （上半身摆动让各帧的包围盒不同，实测中点 x 185.0~188.0 / 最多 13.5px，
    /// 但**脚不该跟着挪**）。
    #[test]
    fn 落点一帧定死不随包围盒游走() {
        let mut place = SlotPlace::default();
        let a = (234.5, 400.0);
        // 基准 = 站立第 0 帧 (121, 26, 134, 322) ⇒ 落点 (234.5-188, 400-348)
        let first = place.of(0, 0, 0, false, (121, 26, 134, 322), a);
        assert_eq!(first, (46.5, 52.0));
        // 后续帧包围盒在游走（中点 185.0 / 187.5 …）—— 落点必须**一个字都不变**
        for bbox in [
            (123, 22, 124, 326),
            (123, 23, 125, 325),
            (122, 24, 127, 324),
        ] {
            assert_eq!(
                place.of(0, 0, 0, false, bbox, a),
                first,
                "{bbox:?} 让落点漂了"
            );
        }
        // 锚点换了（窗口尺寸变了 / 背景动了）也仍用第一次那个偏移，**但整体跟着锚点走**
        let moved = place.of(0, 0, 0, false, (1, 1, 2, 2), (334.5, 500.0));
        assert_eq!(
            moved,
            (334.5 - 188.0, 500.0 - 348.0),
            "窗口变大时不该重算，但要跟着锚点平移"
        );
        // 换了职业/性别 ⇒ 重算（不同图的透明边本来就不一样）
        let other = place.of(0, 1, 0, false, (96, 58, 134, 294), a);
        assert_ne!(other, first);
        // 另一个槽各算各的
        let slot1 = place.of(1, 0, 0, false, (121, 26, 134, 322), a);
        assert_eq!(slot1, first, "同图不同槽：锚点相同时落点也相同");
    }

    /// **石化帧 = 站立基准 + 原版的手调量**（`frozen_offset`），且**逐字照抄**原版那张表。
    #[test]
    fn 石化偏移照抄原版() {
        // `IntroScn.pas:1487-1524` 的 `bx-fx` / `by-fy`（石化位置 − 站立位置）
        assert_eq!(frozen_offset(0, 0), (0.0, 0.0), "战士男：原版两处坐标相同");
        assert_eq!(
            frozen_offset(0, 1),
            (0.0, 0.0),
            "战士女：`bx-28+28` ⇒ 也是 0"
        );
        assert_eq!(frozen_offset(1, 0), (0.0, 0.0), "法师男");
        assert_eq!(
            frozen_offset(1, 1),
            (30.0, 14.0),
            "法师女：`141+30` / `85+14-2`"
        );
        assert_eq!(frozen_offset(2, 0), (0.0, 0.0), "道士男");
        assert_eq!(
            frozen_offset(2, 1),
            (23.0, 20.0),
            "道士女：`141+23` / `85+20-2`"
        );

        // 落到落点上：石化帧只比站立帧多这两个数（基准是同一个站立第 0 帧）
        let mut place = SlotPlace::default();
        let a = (573.0, 400.0);
        let stand0 = (31, 14, 129, 293); // 法师女 站立第 0 帧
        let stand = place.of(1, 1, 1, false, stand0, a);
        assert_eq!(stand, (573.0 - 95.5, 400.0 - 307.0));
        assert_eq!(
            place.of(1, 1, 1, true, stand0, a),
            (stand.0 + 30.0, stand.1 + 14.0)
        );

        // 男角：石化帧与站立帧**同一个**（原版两处坐标相同）
        let mut male = SlotPlace::default();
        let s = male.of(0, 2, 0, false, (89, 38, 131, 288), a); // 道士男 站立第 0 帧
        assert_eq!(
            male.of(0, 2, 0, true, (89, 38, 131, 288), a),
            s,
            "男角的石化图不该叠偏移"
        );
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
        // ⚠️ 用 `Art::STAND_MS` 而不是写死 300：节拍随原版/用户口径调（2026-10-08 改回
        // 300），写死的话一调节拍测试就红（踩过）。
        s.tick(Art::STAND_MS - 50, true);
        assert_eq!(
            s.tick(0, true).index,
            stand_index(1, 0, 0),
            "还没到一帧的时间"
        );
        s.tick(50, true);
        assert_eq!(
            s.tick(0, true).index,
            stand_index(1, 0, 1),
            "过了一帧该进帧"
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
        // 菜单那一列落在中间面板里（实测首项顶 456、末项底 568）
        for r in &l.menu {
            assert!(r.y >= 450.0 && r.y + r.h <= 600.0, "{r:?}");
            assert!(
                300.0 <= r.x && r.x + r.w <= 500.0,
                "菜单项跑到中间面板外: {r:?}"
            );
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
                // 16 帧**帧帧不同**（实测）—— 帧数/基址写错时这里会红
                let mut seen: Vec<Vec<u8>> = Vec::new();
                for k in 0..Art::STAND_FRAMES {
                    let s = dec(&mut libs, &dir, Art::CHR, stand_index(job, sex, k)).unwrap();
                    assert!(
                        !seen.contains(&s.rgba),
                        "Job={job} Sex={sex}：第 {k} 帧与前面某帧重复（不是 16 帧循环？）"
                    );
                    seen.push(s.rgba);
                }
                let c = dec(&mut libs, &dir, Art::CHR, freeze_index(job, sex, 0)).unwrap();
                assert_ne!(a.rgba, c.rgba, "Job={job} Sex={sex}：石化帧与站立帧一样");
            }
        }
        for k in 0..Art::EFFECT_FRAMES {
            let i = effect_index(k);
            assert!(dec(&mut libs, &dir, Art::CHR, i).is_some_and(|s| !s.is_empty()));
        }
    }

    /// **真素材验收**：中间 5 个菜单项的**按下态图**，在实测位置上与背景几乎重合 ——
    /// 而且**附近没有更好的位置**（否则就是量错了）。
    ///
    /// 这条是用户第二次报的"开始/创建人物等按钮位置偏了"的判据。上一版用
    /// "亮像素行直方图分组"量成了文字行，比按下态图的顶低 8~10px ⇒ 按下态整体偏低。
    #[test]
    fn 真素材_菜单按下态与背景对齐() {
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

        /// 模板 `t` 的**不透明像素**与 `bg`（左上角在 `(x,y)`）的平均差。
        fn diff_at(bg: &Sprite, t: &Sprite, x: i32, y: i32) -> Option<f32> {
            let (mut sum, mut n) = (0u64, 0u64);
            for ty in 0..t.height as i32 {
                let py = y + ty;
                if py < 0 || py >= bg.height as i32 {
                    return None;
                }
                for tx in 0..t.width as i32 {
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

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(&dir);
        let mut libs: HashMap<&'static str, Wzl> = HashMap::new();

        let bg = dec(&mut libs, &dir, Art::BG.0, Art::BG.1).expect("背景");
        // 版式：和 app 一样，用素材实测尺寸算（背景 800×600 ⇒ bg = (0,0)）
        let l = Layout::build((800, 600), |c, i| {
            let lib = libs
                .entry(c)
                .or_insert_with(|| Wzl::open(dir.join(c)).expect("图库都在"));
            lib.record(i as usize)
                .map(|r| (r.width as u32, r.height as u32))
        })
        .expect("素材齐全");

        for (i, r) in l.menu.iter().enumerate() {
            let (name, idx) = Art::MENU_DOWN[i];
            let t = dec(&mut libs, &dir, name, idx).expect("菜单按下态");
            let (x, y) = (r.x as i32, r.y as i32);
            let here = diff_at(&bg, &t, x, y).expect("位置在图内");
            assert!(
                here < 20.0,
                "菜单第 {i} 项（{name}[{idx}]）在 ({x},{y}) 与背景差 {here:.2} —— 位置或图号错了？"
            );
            // 附近（±10）**没有更贴的**：1px 的偏都能让这套素材的逐像素差从 9 跳到 27
            // ⇒ 这条能钉住"逐项存左上角"（用户报的"创建人物/制作群左偏 1px"就是它）。
            for dy in -10..=10 {
                for dx in -10..=10 {
                    if (dx, dy) == (0, 0) {
                        continue;
                    }
                    if let Some(d) = diff_at(&bg, &t, x + dx, y + dy) {
                        assert!(
                            d >= here - 1e-3,
                            "菜单第 {i} 项在 ({},{}) 比版式给的 ({x},{y}) 更贴背景（{d:.2} < {here:.2}）\
                             —— `MENU_X[{i}]`/`MENU_ROW_Y[{i}]` 该改",
                            x + dx,
                            y + dy
                        );
                    }
                }
            }
        }
    }

    /// **真素材验收**：名字/等级/职业 三行的**文字落点**要落在值框里
    ///（框底 = 一条亮线，实测在 `FIELD_Y[i] + 17.5` 上下）。
    ///
    /// 这条是用户报的"名字、等级、职业位置偏了"的判据：上一版把字画在框**外上方**。
    #[test]
    fn 真素材_三行值框与文字落点() {
        use crate::wzl::Wzl;

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let bg = Wzl::open(std::path::Path::new(&dir).join(Art::BG.0))
            .expect("选角背景")
            .decode(Art::BG.1 as usize)
            .expect("背景解码");

        let lum = |x: i32, y: i32| {
            let i = ((y * bg.width as i32 + x) * 4) as usize;
            (bg.rgba[i] as u32 * 299 + bg.rgba[i + 1] as u32 * 587 + bg.rgba[i + 2] as u32 * 114)
                / 1000
        };

        for slot in 0..2 {
            let t = slot_text(slot, (0.0, 0.0));
            for (i, want) in [t.name, t.level, t.class].iter().enumerate() {
                // 值框底 = 该行往下 30px 内"亮像素最多"的那条横线
                let (x0, x1) = (want.0 as i32 - 5, want.0 as i32 + 105);
                let mut best = (0i32, 0usize);
                for y in want.1 as i32 - 6..want.1 as i32 + 30 {
                    let n = (x0..x1).filter(|&x| lum(x, y) > 110).count();
                    if n > best.1 {
                        best = (y, n);
                    }
                }
                let expect = want.1 + 17.5;
                assert!(
                    (best.0 as f32 - expect).abs() <= 3.0,
                    "槽 {slot} 第 {i} 行：值框底在 y={} （期待 {expect:.1}）—— \
                     文字落点 {:.1} 不在框里",
                    best.0,
                    want.1
                );
            }
        }
    }

    /// **真素材验收**：站立家族与石化家族**取景不同**（`frozen_offset` 那几组手调量就是为此），
    /// 而且叠上原版的手调量之后，两套图的**脚**与**剪影质心**都回到同一处。
    ///
    /// 法师女：`ChrSel[200]`（站立 0）的包围盒从 `(31,14)` 起（四周留了透明边），
    /// `ChrSel[220]`（石化 0）是 `(0,0)` 起的**紧贴裁切** ⇒ 中点差 **30.5px**。
    /// 不叠那两组数 = 选中那一刻整体横跳（用户 2026-10-08 报的"向右了 20 像素"）；
    /// 而**按包围盒各自居中**又会差 0.5~3.5px（就是用户第三轮报的"往左偏 2 像素"）——
    /// 所以这里同时钉住"原版那张表"与"这套素材配得上那张表"。
    #[test]
    fn 真素材_站立与石化的取景不同() {
        use crate::wzl::{Sprite, Wzl};

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let lib = Wzl::open(std::path::Path::new(&dir).join(Art::CHR)).expect("ChrSel");
        let img = |i: u32| {
            lib.decode(i as usize)
                .unwrap_or_else(|| panic!("ChrSel[{i}] 无图"))
        };
        let bbox_of = |s: &Sprite| s.alpha_bbox().expect("非空图");
        /// 不透明像素的**质心 x** —— 比包围盒中点更接近"眼睛看到的人物中心"。
        fn centroid_x(s: &Sprite) -> f32 {
            let (mut sum, mut n) = (0u64, 0u64);
            for y in 0..s.height as i32 {
                for x in 0..s.width as i32 {
                    if s.rgba[((y * s.width as i32 + x) * 4 + 3) as usize] >= 128 {
                        sum += x as u64;
                        n += 1;
                    }
                }
            }
            sum as f32 / n.max(1) as f32
        }

        // 三件事实钉住：
        // ① 同一家族内各帧几乎一样（站立最多差 13.5px = 战士女摆手、石化完全相同）
        //    ⇒ "基准固定用站立第 0 帧" 才成立；
        // ② 两个家族之间差很多（女角中点 30.5px）⇒ 必须有 `frozen_offset`；
        // ③ 叠上那两组数后，脚与质心都**回到同一处**（±2px）⇒ 表是对的、不跳。
        let mut worst_gap = 0.0f32;
        for job in 0..3u8 {
            for sex in 0..2u8 {
                let mid = |i: u32| {
                    let b = bbox_of(&img(i));
                    b.0 as f32 + b.2 as f32 / 2.0
                };
                let stand: Vec<f32> = (0..Art::STAND_FRAMES)
                    .map(|k| mid(stand_index(job, sex, k)))
                    .collect();
                let freeze: Vec<f32> = (0..Art::FREEZE_FRAMES)
                    .map(|k| mid(freeze_index(job, sex, k)))
                    .collect();
                let span = |v: &[f32]| {
                    v.iter().cloned().fold(f32::MIN, f32::max)
                        - v.iter().cloned().fold(f32::MAX, f32::min)
                };
                assert!(
                    span(&stand) <= 15.0,
                    "Job={job} Sex={sex}：站立帧中点差 {}（实测最大 13.5：战士女）",
                    span(&stand)
                );
                assert!(
                    span(&freeze) <= 1.0,
                    "Job={job} Sex={sex}：石化帧中点差 {}（实测 0）",
                    span(&freeze)
                );
                worst_gap = worst_gap.max((stand[0] - freeze[0]).abs());

                // ③ 叠上原版的 (dx,dy)
                let (dx, dy) = frozen_offset(job, sex);
                let s = img(stand_index(job, sex, 0));
                let f = img(freeze_index(job, sex, 0));
                let (sb, fb) = (bbox_of(&s), bbox_of(&f));
                // 脚：站立底边 vs (石化底边 + dy)
                let foot = (sb.1 + sb.3 as i32) - (fb.1 + fb.3 as i32 + dy as i32);
                assert!(
                    foot.abs() <= 2,
                    "Job={job} Sex={sex}：叠上 (dx,dy)={dx},{dy} 后脚差了 {foot}px —— 石化图会浮起来/陷下去"
                );
                // 质心：站立质心 vs (石化质心 + dx)
                let mass = (centroid_x(&s) - (centroid_x(&f) + dx)).abs();
                assert!(
                    mass <= 2.0,
                    "Job={job} Sex={sex}：叠上 dx={dx} 后剪影质心差 {mass:.1}px —— \
                     用户看到的就是「石化时整体偏一点」"
                );
            }
        }
        assert!(
            worst_gap > 20.0,
            "站立与石化的中点最多只差 {worst_gap:.1}px —— 与实测（法师女 30.5px）不符，测的是同一套图？"
        );
    }

    /// 建角对话框的几何：控件都在框内、`[确定]`/`[关闭]` 不重叠、命中测试指哪打哪。
    #[test]
    fn 建角对话框的按钮都在框内且能点中() {
        let d = DialogBox::build((1024, 768));
        let inner = [&d.name, &d.ok, &d.close];
        for r in d
            .job
            .iter()
            .chain(d.sex.iter())
            .chain(inner.iter().copied())
        {
            assert!(r.x >= d.frame.x && r.y >= d.frame.y, "控件跑出框外: {r:?}");
            assert!(
                r.x + r.w <= d.frame.x + d.frame.w && r.y + r.h <= d.frame.y + d.frame.h,
                "控件伸出框外: {r:?}"
            );
        }
        assert!(d.ok.x + d.ok.w <= d.close.x, "[确定] 与 [关闭] 不该重叠");
        assert_eq!(d.hit(d.ok.center()), Some(DialogHit::Ok));
        assert_eq!(d.hit(d.close.center()), Some(DialogHit::Close));
        assert_eq!(d.hit(d.name.center()), Some(DialogHit::Name));
        assert_eq!(d.hit(d.job[2].center()), Some(DialogHit::Job(2)));
        assert_eq!(d.hit(d.sex[1].center()), Some(DialogHit::Sex(1)));
        assert_eq!(d.hit((2.0, 2.0)), None, "框外不该命中");
        // 窗口比框还小时也不能算出负坐标
        let small = DialogBox::build((320, 200));
        assert!(small.frame.x >= 0.0 && small.frame.y >= 0.0);
    }
    /// 「开始」那颗石台画在哪：**居中压在菜单第一项上**，而且整块落在设计空间里。
    #[test]
    fn 开始石台居中压在开始钮上() {
        // 实测的"开始"钮（`SEL_BTN` 那套量法的同一批数字）
        let r = Rect {
            x: 385.0,
            y: 456.0,
            w: 44.0,
            h: 21.0,
        };
        let p = start_plate_at(r);
        assert_eq!((p.w, p.h), (START_PLATE_SRC.2, START_PLATE_SRC.3));
        // 台心 = 钮心
        assert!(
            (p.center().0 - r.center().0).abs() <= 0.5,
            "{}",
            p.center().0
        );
        assert!(
            (p.center().1 - r.center().1).abs() <= 0.5,
            "{}",
            p.center().1
        );
        // 与实测的原位（`Prguse2[480]` 里那块）差不到 2px
        assert!((p.x - START_PLATE_SRC.0).abs() <= 2.0, "x = {}", p.x);
        assert!((p.y - START_PLATE_SRC.1).abs() <= 2.0, "y = {}", p.y);
        // 整块笔在设计空间（800×600）里
        assert!(p.x >= 0.0 && p.y >= 0.0 && p.x + p.w <= 800.0 && p.y + p.h <= 600.0);
    }

    /// **真素材**：`Art::START_PLATE` + [`START_PLATE_SRC`] 必须**正好**是
    /// 「无台版 `Prguse2[206]`」与「有台版 `Prguse2[480]`」的**差**：
    ///
    /// - 框**内**：两块差得很多（那就是台子）；
    /// - 框**外**：两块**一模一样**（换了图号或框偏一点，这条就红）；
    /// - 我们自己在用的 1.76 底图 `Prguse[65]` 在框内与"无台版"接近 ⇒ 它确实没台子
    ///   （这正是要"搬一块过来"的理由）。
    #[test]
    fn 真素材_开始石台就是那两块底图的差() {
        use crate::wzl::{Sprite, Wzl};
        use std::path::Path;

        fn mean_diff(a: &Sprite, b: &Sprite, rect: (f32, f32, f32, f32)) -> f32 {
            let (x0, y0) = (rect.0 as i32, rect.1 as i32);
            let (w, h) = (rect.2 as i32, rect.3 as i32);
            let (mut sum, mut n) = (0u64, 0u64);
            for y in y0..y0 + h {
                for x in x0..x0 + w {
                    if x < 0 || y < 0 || x >= a.width as i32 || y >= a.height as i32 {
                        continue;
                    }
                    let i = ((y * a.width as i32 + x) * 4) as usize;
                    for c in 0..3 {
                        sum += (a.rgba[i + c] as i32 - b.rgba[i + c] as i32).unsigned_abs() as u64;
                        n += 1;
                    }
                }
            }
            sum as f32 / n.max(1) as f32
        }

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let dir = std::path::PathBuf::from(&dir);
        let open = |name: &str| Wzl::open(Path::new(&dir).join(name)).expect("图库");
        let p2 = open("Prguse2");
        let no_plate = p2.decode(206).expect("Prguse2[206] 无台版");
        let plate = p2
            .decode(Art::START_PLATE.1 as usize)
            .expect("Prguse2[480] 有台版");
        // 图号必须指向"有台版"那张：反过来（[206]）这条的前半就会红
        let ours = open(Art::BG.0)
            .decode(Art::BG.1 as usize)
            .expect("1.76 底图");

        let inside = mean_diff(&no_plate, &plate, START_PLATE_SRC);
        assert!(
            inside > 20.0,
            "框内两块底图只差 {inside:.1} —— 这不是台子那块（`START_PLATE_SRC` 错了？）"
        );

        // 框外：逐像素**完全一样**（差的只有台子）
        let (x0, y0, w, h) = START_PLATE_SRC;
        let mut out_sum = 0u64;
        let mut out_n = 0u64;
        for y in 0..no_plate.height as i32 {
            for x in 0..no_plate.width as i32 {
                if (x0 as i32..x0 as i32 + w as i32).contains(&x)
                    && (y0 as i32..y0 as i32 + h as i32).contains(&y)
                {
                    continue;
                }
                let i = ((y * no_plate.width as i32 + x) * 4) as usize;
                for c in 0..3 {
                    out_sum += (no_plate.rgba[i + c] as i32 - plate.rgba[i + c] as i32)
                        .unsigned_abs() as u64;
                    out_n += 1;
                }
            }
        }
        let outside = out_sum as f32 / out_n.max(1) as f32;
        assert!(
            outside < 0.5,
            "框外两块底图还差 {outside:.2} ⇒ `START_PLATE_SRC` 没框住（或超出）那块台子"
        );

        // 我们在用的 1.76 底图：框内与"无台版"接近（它就是纯文字，没有台子）
        let ours_there = mean_diff(&ours, &no_plate, START_PLATE_SRC);
        assert!(
            ours_there < 30.0,
            "1.76 底图那块的差别是 {ours_there:.1} —— 难道它本来就有台子？那就不用搬了"
        );
    }
}

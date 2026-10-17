//! 选角场景的**版式与动画状态机**：纯数据 + 纯计算，**不含任何绘制**。
//!
//! # ⚠️ 坐标是**从这套素材量出来的**，不是照抄原版
//!
//! 原版（`mir2standard` 的 1.76）的坐标在 `FState.pas:934-953` / `IntroScn.pas:1390-1538`。
//!
//! 旧 800×600 图 `Prguse[65]` 与原版位置相同；当前正式界面使用 1024×768 `ui1[2851]`。
//! 2026-10-13 起七颗界面按钮也换用 **ui1 原生图**（`2852..2865`，常态/按下态成对）：
//! 它们就是从这套 1024 美术里切出来的整钮图（石台、文字、底纹都在图里），
//! 落点由模板匹配在 `ui1[2851]` 上实测（`真素材_按钮贴合新版背景` 验收），
//! 绘制时 1:1 贴回原位 —— 不再拉伸 1.28 倍，也不再需要"从 Prguse2[480] 搬石台"的补丁。
//! 旧 800 图与 `Prguse[66..72]` 按下态只作历史素材对照。
//!
//! 量法与依据（都是**有界**计算，毫秒级；工具用完即删）：
//!
//! | 量什么 | 怎么量 | 结果 |
//! |---|---|---|
//! | 两块「选择」按钮 | `ui1[2854/2856]` 常态图当模板在 `ui1[2851]` 上滑 | 左 `@ (176,589)`、右 `@ (887,586)`（1024 原生），**差 5.5/5.7** |
//! | 面板间距 | 上面两个 x 之差（×1.28 折设计空间） | **552**（保持不变） |
//! | 中间 5 个菜单项 | `ui1[2852/2858/2860/2862/2864]` 常态图逐个模板匹配 | 左上角 = **(476/448/448/448/487, 589/637/666/694/723)**（1024 原生） |
//! | 菜单项的按下态图 | 常态图号 +1（2853/2859/2861/2863/2865），尺寸一一对应 | 与常态同位同尺寸 |
//! | 人物区（放小人） | 行/列**暗度剖面**（`lum<45` 占比 >0.75 的连续区 = 凹槽暗腔） | 左 `x 92..377`、右 `x 421..725`，`y 58..402`（底线 = 凹槽地台） |
//! | 名字/等级/职业 的值框 | 面板内**亮底线**（`lum>110` 的整行）定位框底，再按框高 20 反推框顶 | 框底 y ≈ 508.5 / 537.5 / 568，框 `x 108..211`（右面板 +552）；文字落点 = 框顶 + 2.5 |
//!
//! 七颗按钮的原生落点由 `真素材_按钮贴合新版背景` 逐张钉住；
//! 名字/等级/职业三行仍按旧素材字段框底线测量，由 `真素材_三行值框与文字落点` 验收。
//! 旧 800 图的精确菜单中心记录仅留作历史版式对照，不再用作当前界面坐标。
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
    /// 旧 800×600 选角底图，留给素材对照/历史测试；正式客户端已不回退使用，且不进入有效包。
    pub const BG: (&'static str, u32) = ("Prguse", 65);
    /// 选角背景的 **1024 原生版**（`ui1[2851]`，1024×768）。
    ///
    /// 2026-10-12 换上：与 `BG` **同一套构图且比例一致** —— 实测两块凹槽在 800 图里宽
    /// 301/306、在 1024 图里宽 392/392（≈1.28×），分块亮度相关 **+0.989** ⇒ 它就是
    /// 800 那张的 1024 重绘版 ⇒ 版式（设计空间 ×`UI_SCALE`）**不用改**，只是底图变清晰。
    pub const BG_1024: (&'static str, u32) =
        crate::image_manifest::assets::SELECT_BACKGROUND.pair();
    /// 左 / 右面板那颗「选择」的**常态**（1024 原生图；按下态 = 图号 +1）。
    ///
    /// ⚠️ 仍是两张**不同**的图（不是同一张用两次）：实测 `2854` 只匹配左边、`2856` 只匹配右边。
    pub const SEL_UP: [(&'static str, u32); 2] = [("ui1", 2854), ("ui1", 2856)];
    /// 中间那列 5 个菜单项的**常态**，顺序与 [`Menu`] 一一对应
    ///（开始 / 创建人物 / 恢复人物 / 删除人物 / 退出；按下态 = 图号 +1）。
    /// 图号是用户 2026-10-13 给的：`2852..2865` 每颗钮两张（常态/按下态）。
    pub const MENU_UP: [(&'static str, u32); 5] = [
        ("ui1", 2852),
        ("ui1", 2858),
        ("ui1", 2860),
        ("ui1", 2862),
        ("ui1", 2864),
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

    // ---------- 建角窗（原版 `DCreateChr`，`FState.pas:957-1001`）----------
    //
    // 图号是用户 2026-10-11 给的；控件坐标是**从官方源码抄的**，不是量出来的：
    // `FState.pas:977-1001` 给了战/法/道、男/女、发型左右、[确定]、[关闭] 的位置，
    // 姓名框在 `IntroScn.pas:1377-1378`（`DCreateChr.Left + 71 / Top + 107`）。
    /// 建角窗**背景**（300×417，官方 `DCreateChr.Left/Top` 是**居中**摆的）。
    pub const NEW_CHR_BG: (&'static str, u32) = ("Prguse", 73);
    /// 职业按钮的**高亮**：`+0/+1/+2` = 战士/法师/道士（常态画在底图 73 里）。
    pub const NEW_CHR_JOB: (&'static str, u32) = ("Prguse", 74);
    /// 性别按钮的**高亮**：`+0/+1` = 男/女（同上）。
    pub const NEW_CHR_SEX: (&'static str, u32) = ("Prguse", 77);
    /// 发型**上一款**（`DccLeftHair`）。⚠️ 官方是**空实现**
    /// （`IntroScn.pas:1450-1452` 函数体是空的）⇒ 我们也不用它，只登记图号。
    pub const NEW_CHR_HAIR_PREV: (&'static str, u32) = ("Prguse", 79);
    /// 发型**下一款**（`DccRightHair`），同上（空实现）。
    pub const NEW_CHR_HAIR_NEXT: (&'static str, u32) = ("Prguse", 80);
    /// 建角窗的 [确定]（76×33）。
    pub const NEW_CHR_OK: (&'static str, u32) = ("Prguse", 62);
    /// 建角窗右上角的 [关闭]（红叉，16×23）。
    pub const NEW_CHR_CLOSE: (&'static str, u32) = ("Prguse", 64);
}

/// 中间菜单的 5 项（顺序 = 从上到下 = [`Art::MENU_UP`] 的顺序）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Menu {
    /// 开始游戏。
    Start,
    /// 创建人物（已接上：开 [`DialogBox`] 对话框，见 D-35）。
    CreateChar,
    /// 恢复人物（找回已删除的角色；服务端还没有这条路，先占位提示。
    /// 新版背景这颗钮顶掉了旧 1.76 的「制作群」—— 用户 2026-10-13 确认制作群不要了）。
    RestoreChar,
    /// 删除人物（已接上：弹"删了不可恢复"的确认，见 D-35）。
    DeleteChar,
    /// 退出游戏。
    Exit,
}

/// 菜单顺序（给界面遍历用）。
pub const MENU: [Menu; 5] = [
    Menu::Start,
    Menu::CreateChar,
    Menu::RestoreChar,
    Menu::DeleteChar,
    Menu::Exit,
];

// ---------- 实测版式（这套素材，800×600 坐标系）----------

/// 原生 1024 背景坐标 → 设计空间（800×600）的换算。
///
/// ⚠️ 写成 `25/32`（= 1/1.28）而不是 `1.0/1.28`：`25/32` 是二进制精确值，
/// `n * 25 / 32`（n ≤ 1024）在 `f32` 里**零误差**，实测坐标折算回设计空间不会漂。
pub const NATIVE_TO_DESIGN: f32 = 25.0 / 32.0;

/// 七颗按钮在**原生 1024 背景 `ui1[2851]`** 上的左上角（模板匹配实测）。
///
/// 顺序：开始 / 左选择 / 右选择 / 创建人物 / 恢复人物 / 删除人物 / 退出
///（前三对应 [`Art::BTN_START`] 与 [`Art::SEL_UP`]，后四对应 [`Art::MENU_UP`] 去掉首项）。
/// 匹配差 4.7~7.4、±3px 邻域内没有更贴的位置 —— 由 `真素材_按钮贴合新版背景` 钉住。
const BTN_AT_NATIVE: [(i32, i32); 7] = [
    (476, 589), // 开始 88×41（自带石台）
    (176, 589), // 左选择 102×45
    (887, 586), // 右选择 102×46
    (448, 637), // 创建人物 145×25
    (448, 666), // 恢复人物 145×25
    (448, 694), // 删除人物 145×25
    (487, 723), // 退出 67×24
];

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

/// 第 `slot` 个凹槽的矩形（与 [`slot_at`] **同源**：都出自 [`ALCOVE`]）。
///
/// 存在的理由：控件层要"按下的那块矩形"（放松手时不用再算一遍命中）——
/// 原来只有点判定 `slot_at`，没有矩形。
pub fn slot_rect(slot: usize, bg: (f32, f32)) -> Rect {
    let (x0, y0, x1, y1) = ALCOVE[slot.min(Art::SLOTS - 1)];
    Rect {
        x: bg.0 + x0,
        y: bg.1 + y0,
        w: x1 - x0,
        h: y1 - y0,
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
    /// 两个角色展示/命中区域（可由 app 的 widget 布局覆盖）。
    pub slots: [Rect; 2],
    /// 两块角色信息区域的三行文字落点（名字、等级、职业）。
    pub slot_texts: [SlotText; 2],
    /// 窗口尺寸（`f32` 版）。
    ///
    /// 存在的理由：**建角对话框与消息框是"居中"的**，命中测试要按窗口尺寸算它们的位置，
    /// 而命中测试那条路上只有 `Layout`（`Select::on_up` 的签名）—— 少了它就得把
    /// `ui`/`dir` 一路传进去，或者让几何依赖素材（见 [`DialogBox`] 的说明）。
    pub win: (f32, f32),
}

impl Layout {
    /// 按窗口尺寸与按钮素材尺寸算版式。
    ///
    /// 几何仍使用 800×600 设计空间，运行时背景固定是必需的 1024×768 `ui1[2851]`；
    /// 不探测旧 800 图 `Prguse[65]`。`measure` 只校准会实际绘制的按钮图。
    pub fn build(
        win: (u32, u32),
        mut measure: impl FnMut(&'static str, u32) -> Option<(u32, u32)>,
    ) -> Option<Layout> {
        let (bw, bh) = (800u32, 600u32);
        // 设计背景矩形居中（窗口可能更大）
        let bg = (
            (win.0 as f32 - bw as f32) / 2.0,
            (win.1 as f32 - bh as f32) / 2.0,
        );
        // 七颗按钮：位置是 1024 原生实测的（[`BTN_AT_NATIVE`]），尺寸**再问一次素材**；
        // 素材是 1024 原生图 ⇒ 尺寸也要折回设计空间（×25/32），画的时候 ×1.28 正好 1:1 回原位。
        let native_at = |i: usize, w: u32, h: u32| Rect {
            x: bg.0 + BTN_AT_NATIVE[i].0 as f32 * NATIVE_TO_DESIGN,
            y: bg.1 + BTN_AT_NATIVE[i].1 as f32 * NATIVE_TO_DESIGN,
            w: w as f32 * NATIVE_TO_DESIGN,
            h: h as f32 * NATIVE_TO_DESIGN,
        };
        let (w0, h0) = measure(Art::SEL_UP[0].0, Art::SEL_UP[0].1)?;
        let (w1, h1) = measure(Art::SEL_UP[1].0, Art::SEL_UP[1].1)?;
        let sel = [native_at(1, w0, h0), native_at(2, w1, h1)];
        // 菜单：左上角逐项实测（[`BTN_AT_NATIVE`] 的 0/3..6），尺寸**再问一次素材**
        let mut menu = [Rect {
            x: 0.0,
            y: 0.0,
            w: 0.0,
            h: 0.0,
        }; 5];
        for (i, native) in [0usize, 3, 4, 5, 6].into_iter().enumerate() {
            let (mw, mh) = measure(Art::MENU_UP[i].0, Art::MENU_UP[i].1)?;
            menu[i] = native_at(native, mw, mh);
        }
        Some(Layout {
            bg,
            sel,
            menu,
            slots: std::array::from_fn(|slot| slot_rect(slot, bg)),
            slot_texts: std::array::from_fn(|slot| slot_text(slot, bg)),
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
    slot_anchor_in(slot_rect(slot, bg))
}

/// 由可编辑的角色槽矩形计算人物脚底锚点。
pub fn slot_anchor_in(rect: Rect) -> (f32, f32) {
    (rect.x + rect.w / 2.0, rect.y + rect.h - ALCOVE_FLOOR_MARGIN)
}

/// 这个点（**设计空间**）落在哪个凹槽里 —— "点角色本体也能选中"（用户 2026-10-09 第 2 条）。
///
/// 判据用 [`ALCOVE`]（凹槽的**暗腔**矩形），不是小人的不透明包围盒：小人会动
///（站立 16 帧的上半身一直在摆、石化/光效还会换图），按包围盒判会让可点区域跟着抖；
/// 而原版点选本来就是"整块凹槽都算"。
///
/// ⚠️ 两块凹槽都在**面板上半部**，与中间的菜单、下方的「选择」钮**不重叠** ⇒
/// 放 `hit_at` 里按什么顺序判都行。
pub fn slot_at(p: (f32, f32), bg: (f32, f32)) -> Option<usize> {
    ALCOVE.iter().position(|&(x0, y0, x1, y1)| {
        let (x, y) = (p.0 - bg.0, p.1 - bg.1);
        x >= x0 && x < x1 && y >= y0 && y < y1
    })
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

    /// 推进 `dt_ms` 毫秒并返回当前画格（**更新阶段**调用，不要塞进绘制）。
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
            SlotPhase::Unfreezing if self.k >= Art::FREEZE_FRAMES => {
                self.phase = SlotPhase::Stand;
                self.k = 0;
            }
            SlotPhase::Freezing if self.k >= Art::FREEZE_FRAMES => {
                self.phase = SlotPhase::Freeze;
                self.k = 0;
            }
            _ => {}
        }
        self.frame(selected)
    }

    /// 只读地取当前画格（**绘制阶段调用**，不推进计时或改变相位）。
    pub fn frame(&self, selected: bool) -> SlotFrame {
        match self.phase {
            SlotPhase::Stand => {
                let k = if selected { self.k } else { 0 };
                SlotFrame {
                    index: stand_index(self.job, self.sex, k),
                    at_frozen: false,
                    effect: None,
                }
            }
            SlotPhase::Unfreezing => SlotFrame {
                index: freeze_index(self.job, self.sex, self.k.min(Art::FREEZE_FRAMES - 1)),
                at_frozen: true,
                effect: Some(effect_index(self.k.min(Art::FREEZE_FRAMES - 1))),
            },
            SlotPhase::Freeze => SlotFrame {
                index: freeze_index(self.job, self.sex, 0),
                at_frozen: true,
                effect: None,
            },
            SlotPhase::Freezing => SlotFrame {
                index: freeze_index(
                    self.job,
                    self.sex,
                    Art::FREEZE_FRAMES - 1 - self.k.min(Art::FREEZE_FRAMES - 1),
                ),
                at_frozen: true,
                effect: None,
            },
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

/// 「新建角色」对话框的版式（原版 `DCreateChr`）。
///
/// ⚠️ 2026-10-11 起**照官方源码摆**：窗口就是 `Prguse[73]`（300×417，居中），
/// 控件坐标抄 `FState.pas:977-1001`、姓名框抄 `IntroScn.pas:1377-1378`
/// （`Left+71 / Top+107`）—— 不再是自绘的固定几何。
/// 职业/性别的**常态画在底图里**，高亮图（74-78）只在选中/悬停时叠上去。
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
    /// 窗口尺寸 = `Prguse[73]` 的原生尺寸（300×417）。
    pub const W: f32 = 300.0;
    pub const H: f32 = 417.0;

    /// 居中排一个（窗口再小也不会把框挤出屏幕外）。
    pub fn build(win: (u32, u32)) -> Self {
        let bx = ((win.0 as f32 - Self::W) / 2.0).max(4.0);
        let by = ((win.1 as f32 - Self::H) / 2.0).max(4.0);
        // 官方控件坐标（**窗口内**）—— `FState.pas:977-1001`
        let name = Rect {
            x: bx + 71.0,
            y: by + 106.0,
            w: 158.0,
            h: 22.0,
        };
        let job = std::array::from_fn(|i| Rect {
            x: bx + 48.0 + i as f32 * 45.0,
            y: by + 157.0,
            w: 44.0,
            h: 36.0,
        });
        let sex = std::array::from_fn(|i| Rect {
            x: bx + 93.0 + i as f32 * 45.0,
            y: by + 231.0,
            w: 44.0,
            h: 35.0,
        });
        let ok = Rect {
            x: bx + 101.0,
            y: by + 359.0,
            w: 76.0,
            h: 33.0,
        };
        let close = Rect {
            x: bx + 247.0,
            y: by + 30.0,
            w: 16.0,
            h: 23.0,
        };
        Self {
            frame: Rect {
                x: bx,
                y: by,
                w: Self::W,
                h: Self::H,
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

    /// 照真素材量出来的尺寸（背景 800×600；ui1 原生按钮 2852/2854/2856/2858/2860/2862/2864）。
    fn fake(container: &'static str, idx: u32) -> Option<(u32, u32)> {
        match (container, idx) {
            ("Prguse", 65) => Some((800, 600)),
            ("ui1", 2852) => Some((88, 41)),
            ("ui1", 2854) => Some((102, 45)),
            ("ui1", 2856) => Some((102, 46)),
            ("ui1", 2858) | ("ui1", 2860) | ("ui1", 2862) => Some((145, 25)),
            ("ui1", 2864) => Some((67, 24)),
            _ => None,
        }
    }

    /// 800×600 下版式要**正好**复现实测值 —— 这是"没抄错/没算错"的钉子。
    /// 原生坐标 × `NATIVE_TO_DESIGN`（25/32，二进制精确）后逐位相等。
    #[test]
    fn 八百乘六百复现实测值() {
        let l = Layout::build((800, 600), fake).expect("素材齐全");
        assert_eq!(l.bg, (0.0, 0.0));
        // 两块「选择」按钮（原生 (176,589) / (887,586)，102×45 / 102×46）
        assert_eq!(
            (l.sel[0].x, l.sel[0].y, l.sel[0].w, l.sel[0].h),
            (
                176.0 * NATIVE_TO_DESIGN,
                589.0 * NATIVE_TO_DESIGN,
                102.0 * NATIVE_TO_DESIGN,
                45.0 * NATIVE_TO_DESIGN
            )
        );
        assert_eq!(
            (l.sel[1].x, l.sel[1].y, l.sel[1].w, l.sel[1].h),
            (
                887.0 * NATIVE_TO_DESIGN,
                586.0 * NATIVE_TO_DESIGN,
                102.0 * NATIVE_TO_DESIGN,
                46.0 * NATIVE_TO_DESIGN
            )
        );
        // 面板间距：原生 711px 折设计空间 555.47（旧 800 图时代是 552 —— 按钮图换了）
        assert_eq!(l.sel[1].x - l.sel[0].x, 711.0 * NATIVE_TO_DESIGN);
        // 5 个菜单项：左上角逐项实测（原生 (476/448/448/448/487, 589/637/666/694/723)）
        let native_x = [476.0, 448.0, 448.0, 448.0, 487.0];
        let native_y = [589.0, 637.0, 666.0, 694.0, 723.0];
        for (i, r) in l.menu.iter().enumerate() {
            assert_eq!(
                (r.x, r.y),
                (
                    native_x[i] * NATIVE_TO_DESIGN,
                    native_y[i] * NATIVE_TO_DESIGN
                ),
                "第 {i} 项的左上角不对"
            );
        }
        // 五颗钮的中心几乎同 x（406.25 / 406.64×4）—— 文字居中排的那一列
        let centers: Vec<f32> = l.menu.iter().map(|r| r.x + r.w / 2.0).collect();
        assert_eq!(
            centers,
            [
                476.0 * NATIVE_TO_DESIGN + 44.0 * NATIVE_TO_DESIGN,
                520.5 * NATIVE_TO_DESIGN,
                520.5 * NATIVE_TO_DESIGN,
                520.5 * NATIVE_TO_DESIGN,
                520.5 * NATIVE_TO_DESIGN
            ]
        );
        // 五个节点不重叠；菜单行距按新版底图逐项实测。
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

    #[test]
    fn 只读取当前画格不会推进动画() {
        let mut s = SlotAnim::new(0, 0);
        s.select();
        s.tick(150, true);
        let phase = s.phase();
        let a = s.frame(true);
        let b = s.frame(true);
        assert_eq!(a, b, "渲染重复读取应是纯读");
        assert_eq!(s.phase(), phase, "frame() 不得改变相位");
        assert_eq!(a, s.tick(0, true), "tick(0) 返回与只读画格一致");
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
            if (c, i) == ("ui1", 2858) {
                None
            } else {
                fake(c, i)
            }
        };
        assert!(Layout::build((800, 600), missing).is_none());
    }

    /// **真素材验收**：两件事都钉住 ——
    ///
    /// 1. 七颗按钮（7 张常态 + 7 张按下态）都真的在 `ui1` 里，且小人 3×2×16/13 帧全解得出；
    /// 2. **最重要的**：每张**常态图在实测位置上与 1024 背景 `ui1[2851]` 几乎逐像素相同**，
    ///    而且 ±3px 邻域里**没有更贴的位置** —— 这一条直接证明"按钮图、命中区与背景对齐"，
    ///    改素材/改坐标/换图号时它会红。
    #[test]
    fn 真素材_按钮贴合新版背景() {
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

        let bg = dec(&mut libs, &dir, Art::BG_1024.0, Art::BG_1024.1).expect("1024 选角背景");
        let buttons: Vec<(&'static str, u32, &str)> = vec![
            (Art::MENU_UP[0].0, Art::MENU_UP[0].1, "[开始]"),
            (Art::SEL_UP[0].0, Art::SEL_UP[0].1, "左「选择」"),
            (Art::SEL_UP[1].0, Art::SEL_UP[1].1, "右「选择」"),
            (Art::MENU_UP[1].0, Art::MENU_UP[1].1, "[创建人物]"),
            (Art::MENU_UP[2].0, Art::MENU_UP[2].1, "[恢复人物]"),
            (Art::MENU_UP[3].0, Art::MENU_UP[3].1, "[删除人物]"),
            (Art::MENU_UP[4].0, Art::MENU_UP[4].1, "[退出]"),
        ];

        /// 模板 `t` 的不透明像素与 `bg`（左上角 `(x,y)`，**原生 1024 坐标**）的平均差。
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

        for ((name, idx, what), (x, y)) in buttons.iter().zip(BTN_AT_NATIVE.iter()) {
            let up = dec(&mut libs, &dir, name, *idx)
                .unwrap_or_else(|| panic!("{what} = {name}[{idx}] 取不出图"));
            assert!(!up.is_empty(), "{what} = {name}[{idx}] 是空图");
            // 按下态（图号 +1）也要在
            let down = dec(&mut libs, &dir, name, *idx + 1)
                .unwrap_or_else(|| panic!("{what} 按下态 = {name}[{}] 取不出图", idx + 1));
            assert!(!down.is_empty(), "{what} 按下态是空图");
            let (x, y) = (*x, *y);
            let here = diff_at(&bg, &up, x, y).expect("实测位置在背景图内");
            assert!(
                here < 12.0,
                "{what}（{name}[{idx}]）在原生 ({x},{y}) 与背景差 {here:.2} —— \
                 位置或图号错了？（实测 4.7~7.4）"
            );
            // ±3 邻域里没有更贴的：1px 的偏都会让差明显抬升
            for dy in -3..=3 {
                for dx in -3..=3 {
                    if (dx, dy) == (0, 0) {
                        continue;
                    }
                    if let Some(d) = diff_at(&bg, &up, x + dx, y + dy) {
                        assert!(
                            d >= here - 1e-3,
                            "{what} 在 ({},{}) 比 BTN_AT_NATIVE 的 ({x},{y}) 更贴背景 \
                             （{d:.2} < {here:.2}）—— 实测坐标该改",
                            x + dx,
                            y + dy
                        );
                    }
                }
            }
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

    /// **建角窗的控件坐标是抄官方源码的**（`FState.pas:977-1001` +
    /// `IntroScn.pas:1377`）—— 这条把它们钉住，免得以后有人当成随手排的版式。
    #[test]
    fn 建角窗几何照官方源码() {
        let b = DialogBox::build((800, 600));
        // 窗 = Prguse[73] 的原生尺寸，居中
        assert_eq!((b.frame.w, b.frame.h), (300.0, 417.0));
        assert_eq!(b.frame.x, (800.0 - 300.0) / 2.0);
        assert_eq!(b.frame.y, (600.0 - 417.0) / 2.0);
        let rel = |r: Rect| (r.x - b.frame.x, r.y - b.frame.y);
        // 姓名框：`EdChrName.Left/Top := DCreateChr.Left/Top + 71/107`
        assert_eq!(rel(b.name), (71.0, 106.0));
        // 战/法/道：48 / 93 / 138，同一行 y=157
        for (i, x) in [48.0, 93.0, 138.0].iter().enumerate() {
            assert_eq!(rel(b.job[i]), (*x, 157.0), "职业 {i}");
        }
        // 男/女：93 / 138，y=231
        for (i, x) in [93.0, 138.0].iter().enumerate() {
            assert_eq!(rel(b.sex[i]), (*x, 231.0), "性别 {i}");
        }
        assert_eq!(rel(b.ok), (101.0, 359.0));
        assert_eq!(rel(b.close), (247.0, 30.0));
    }
}

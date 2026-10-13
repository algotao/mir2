//! HUD/信息条的版式常量与几何（落点全按原版 `FState.pas` 的口径，见 D-49/D-50）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use crate::window::{WIN_H, WIN_W};

/// 地图视图的顶部信息条高度。
///
/// ⚠️ **0**：调试信息从"一条不透明的信息条"改成**左上角叠加**（用户 2026-10-09 要求
/// 「移动至左上角排列，叠加在游戏内容上」）—— 世界因此铺满整屏（原版也是铺满的，
/// 底下那块是 HUD 面板**盖**上来的，不是把视口切掉）。
pub(crate) const BAR_TOP: f32 = 0.0;

/// 底部条高度：同样归零（那儿现在是 HUD 操作面板）。
pub(crate) const BAR_BOTTOM: f32 = 0.0;

/// 地图可视区高度。
pub(crate) const VIEW_H: f32 = WIN_H as f32 - BAR_TOP - BAR_BOTTOM;

// ---------- 底部操作面板（HUD）----------
//
// 版式与图号**全部照抄原版**（`FState.pas` 的 `TFrmDlg.DBottomDirectPaint`）：
//
// ```text
// BOTTOMBOARD800 = 1;                          // Prguse[1]，800×251，画在 SCREENHEIGHT - h
// 上半部 120px 透明（色键）/ 下半部不透明
// Images[4] 画在 (40, btop+91)                 // 球：左半红=HP、右半蓝=MP，按比例裁
//   HP 那一半：rc.Right := Right div 2 - 1; rc.Top := Round(rc.Bottom / MaxHP * (MaxHP - HP))
// PomiTextOut(660, SCREENHEIGHT-104)           // 等级（Prguse[30..39] = '0'..'9'、[40] = '-'）
// Images[7] 画在 (666, SCREENHEIGHT-73)        // 经验条
// 聊天文字 (209, SCREENHEIGHT-128)，行距 13
// ```
//
// ⚠️ 我们**没有** `Images[7]` 要用的经验值（协议 `Ability` 里没有 exp/max_exp）⇒
// 经验条先留空并记在 D-49；`[8]/[9]` 那类装饰件同理。

/// 底部面板的**高度**（`Prguse[1]` 实测 800×251）—— 对话面板要压在它上方，
/// 而事件处理那一侧拿不到画图用的 `ui`/素材目录 ⇒ 这里给它一个常量（与图一致）。
// HUD 背板高度（`Prguse[1]` 是 800×251）。对话窗改官方版式后不按它排版了，
// 但它是**版式事实**（测试引用它），留着。
#[allow(dead_code)]
pub(crate) const HUD_BOARD_H: f32 = 251.0;

/// 血/魔法球（`Prguse[4]`，92×90；左半红=HP、右半蓝=MP）。
pub(crate) const HUD_ORB: u32 = mir2_core::image_manifest::assets::HUD_ORB.index;

/// 经验条 / 负重条（`Prguse[7]`，76×13）：两条**共用**这张图。
///
/// 协议 `Ability` 的 `exp/max_exp`、`weight/max_weight` 补齐后就能画了
///（服务端 `protocolAbility` 会一起下发，见 `hud.rs` 的 `draw_prop_bar`）。
pub(crate) const HUD_EXP: u32 = mir2_core::image_manifest::assets::HUD_PROP_BAR.index;

/// 等级数字的第一张（`Prguse[30..39]` = '0'..'9'，8px 一位）。
pub(crate) const HUD_DIGIT0: u32 = mir2_core::image_manifest::assets::HUD_DIGIT_ZERO.index;

/// 球在**面板坐标**里的落点（原版 `(40, btop+91)`）。
/// ⚠️ 2026-10-12：换成 1024 底栏（`Prguse3[690]`）后重新量过球座 —— 用旧底栏的球座
/// 区域做模板匹配，新图上的最佳位置是 **(40, 92)**（旧值 91，差 1px）。
pub(crate) const ORB_AT: (f32, f32) = (40.0, 92.0);

/// **1024 宽的底栏整图**（`Prguse3[690]`，1024×251；2026-10-12 用户在素材里找到的
/// 官方 1024 版）。
///
/// ⚠️ 有了它就**不用再"左右 1:1 + 中段平铺"拼接**了 —— 整张贴上去，不拉伸、不变形
/// （`draw_hud` 里优先用它；取不到才回退到 800 图的拼接，见 `HUD_SIDE_W`）。
pub(crate) const HUD_BOARD_1024_LIB: &str =
    mir2_core::image_manifest::assets::HUD_BACKGROUND_1024.library;
pub(crate) const HUD_BOARD_1024: u32 = mir2_core::image_manifest::assets::HUD_BACKGROUND_1024.index;

/// 底部面板的宽度（`Prguse[1]` 实测 800×251；与 `HUD_BOARD_H` 成对）。
pub(crate) const HUD_BOARD_W: f32 = 800.0;

// ---------- 底栏右上那 4 个功能按钮 ----------
//
// 原版 `DMyState / DMyBag / DMyMagic / DOption`（`FState.pas:1289-1301`）；
// 图号是用户 2026-10-11 给的 **8 / 9 / 10 / 11**。
//
// ⚠️ 这四张图是**高亮态**：常态就画在底栏 `Prguse[1]` 里，官方只在
// `Downed`（按下）时才把这张叠上去（`DMyStateDirectPaint`，`FState.pas:3912-3926`）。
// 我们扩成"悬停**或**按下都高亮" —— 与交易窗/背包关闭钮那几个同一套手感。

/// 底栏右上的 4 个功能按钮。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum HudBtn {
    /// 人物 = 状态窗（与 **F10** 同一个动作）。
    State,
    /// 包裹（与 **F9** 同一个动作）。
    Bag,
    /// 技能（原版 F11；我们这边窗口还没做）。
    Magic,
    /// 选项（窗口还没做）。
    Option,
}

impl HudBtn {
    pub const ALL: [Self; 4] = [Self::State, Self::Bag, Self::Magic, Self::Option];

    /// 高亮图的图号。
    pub fn img(self) -> u32 {
        match self {
            Self::State => 8,
            Self::Bag => 9,
            Self::Magic => 10,
            Self::Option => 11,
        }
    }

    /// 在**面板坐标**里的落点（官方 `FState.pas:1290-1301` 的 `Left/Top`，
    /// 它们是相对 `DBottom` 的，而 `DBottom.Left=0 / Top=SCREENHEIGHT-h`）。
    ///
    /// 四个是斜着往上排的：人物最下、选项最上。
    pub fn at(self) -> (f32, f32) {
        match self {
            Self::State => (643.0, 61.0),
            Self::Bag => (682.0, 41.0),
            Self::Magic => (722.0, 21.0),
            Self::Option => (764.0, 11.0),
        }
    }

    /// 命中框（**屏幕坐标**；统一 24×24，比图略大一点好点）。
    ///
    /// 位置从 `LayoutDocument` 读取；常态图烘焙在底板中，所以只允许随底板整体移动。
    pub fn rect(self) -> crate::space::CRect {
        let l = layout_document();
        let id = match self {
            Self::State => "btn_state",
            Self::Bag => "btn_bag",
            Self::Magic => "btn_magic",
            Self::Option => "btn_option",
        };
        let r = l.get(id).expect("HUD button must have a layout node");
        crate::space::canvas_rect(r.x, r.y, r.w, r.h)
    }
}

/// 面板中间那段的**目标宽度**（窗口宽 − 左右两块；800 图的中段只有 400 ⇒ 至少不缩）。
///
/// ⚠️ 2026-10-12：中段**不再整体拉伸**（用户对照官方 1024 截图："看上去没有拉伸"）——
/// 顶带是**平铺**（城垛纹 8px 严格周期、`400 % 8 == 0` ⇒ 接缝正好落在整周期上）、
/// 聊天带是"左框 | 白区拉伸 | 右框"（白区是纯色，拉伸无害）⇒ 中段里的元素
/// （腰带 6 格）都按**原样**摆，不再乘拉伸比。
pub(crate) fn hud_mid_w() -> f32 {
    (crate::window::WIN_W as f32 - 2.0 * HUD_SIDE_W).max(HUD_BOARD_W - 2.0 * HUD_SIDE_W)
}

/// 面板局部 x → 屏幕 x。
///
/// ⚠️ 面板是**三段贴**的（`hud.rs` 的 `draw_hud`）：左右两段 1:1，中段在窗口更宽时
/// **向外拼** ⇒ 局部 x 落在右段要换落点，落在中段则原样（平铺不拉伸）。
pub(crate) fn hud_local_x(local: f32) -> f32 {
    let right = HUD_BOARD_W - HUD_SIDE_W;
    if local >= right {
        hud_right_x() + (local - right)
    } else {
        local
    }
}

// ---------- 快捷（腰带）栏 ----------
//
// 原版 `DBelt1..DBelt6`（`FState.pas:1340-1369`）：6 个格，x 285/328/371/415/459/503
// （间距 43）、y 59、每格 32×29；框**已经印在底栏 `Prguse[1]` 里** ⇒ 我们只画
// 物品图标（居中）和格号（`DBelt1DirectPaint`，`FState.pas:4010-4027`）。
//
// ⚠️ 数据是**客户端本地**的 `g_ItemArr[0..5]`（官方 `DBelt1Click` 直接读写它，
// 不发包）⇒ 我们也不用动协议：记住物品实例号，用时回背包找当前格位。

/// 快捷栏格数（原版 6 格，对应数字键 1..6）。
pub(crate) const BELT_SLOTS: usize = 6;
/// 首格在**面板坐标**里的落点。
/// 快捷按钮的间距（官方 219 / 249 / 279 / 309 / 339 ⇒ 30）。
const QUICK_STEP: f32 = 30.0;

/// 快捷按钮排的间距（**由整排宽算**，`n` = 这一排几颗；D-134）。
pub(crate) fn quick_step(row: LRect, n: f32) -> f32 {
    (row.w - 28.0) / if n > 1.0 { n - 1.0 } else { 1.0 }
}

pub(crate) const BELT_AT: (f32, f32) = (285.0, 59.0);
/// 格间距 / 格尺寸（面板坐标）。
pub(crate) const BELT_STEP: f32 = 43.0;
pub(crate) const BELT_W: f32 = 32.0;
pub(crate) const BELT_H: f32 = 29.0;

/// 快捷栏一格记住的东西（**只是引用**，不是物品本身）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct BeltSlot {
    /// 物品**实例号**（`UserItem.MakeIndex`）：用它回背包找当前格位
    /// （背包会被服务端重排 ⇒ 不能存格位号）。与 `BagItem::make_index` 同型（`i32`）。
    pub make_index: i32,
    /// 图标（`Items` 库的图号）—— 画出来用。
    pub looks: u32,
}

/// 腰带的格间距公式；当前背景烘焙节点锁定宽度，公式保持绘制/命中排布同源。
pub(crate) fn belt_step(row: LRect) -> f32 {
    (row.w - BELT_W) / (BELT_SLOTS as f32 - 1.0)
}

/// 第 `i` 格的命中框（**屏幕坐标**）。
///
/// ⚠️ 2026-10-12：中段改**平铺**后这 6 格不再被拉宽 ⇒ 宽高都按原样（32×29）。
pub(crate) fn belt_slot_rect(i: usize) -> crate::space::CRect {
    let b = layout_document()
        .get("belt")
        .expect("belt is a HUD child widget");
    crate::space::canvas_rect(b.x + i as f32 * belt_step(b), b.y, BELT_W, BELT_H)
}

/// 画布点落在快捷栏哪一格（没中 ⇒ `None`）。
pub(crate) fn belt_slot_at(p: crate::space::CPt) -> Option<usize> {
    (0..BELT_SLOTS).find(|i| crate::space::hit(belt_slot_rect(*i), p))
}

/// 画布点中了哪个功能按钮（没中 ⇒ `None`）。
pub(crate) fn hud_btn_at(p: crate::space::CPt) -> Option<HudBtn> {
    HudBtn::ALL
        .into_iter()
        .find(|b| crate::space::hit(b.rect(), p))
}

// ---------- 技能栏（F1..F8）----------
//
// 原版 1.76 的底栏**没有**这一排（技能只在 F1..F8 与技能窗里），参考图那个
// 引擎把技能栏摆成底栏上方一排 8 格 ⇒ 我们照它摆（用户 2026-10-12 的参考图）。
// 位置在**底栏之上**（底栏是最后贴的，压在它上面会互相盖住）。

/// 技能栏格数（F1..F8）。
pub(crate) const SKILL_SLOTS: usize = 8;
/// 一格 34×34，格间距 37。
pub(crate) const SKILL_SLOT_W: f32 = 34.0;
pub(crate) const SKILL_SLOT_H: f32 = 34.0;
pub(crate) const SKILL_SLOT_STEP: f32 = 37.0;

/// 技能栏左上角的**画布坐标**（贴在底栏上方，离底栏 6px）。
pub(crate) fn skill_bar_at() -> (f32, f32) {
    (
        8.0,
        crate::window::WIN_H as f32 - HUD_BOARD_H - SKILL_SLOT_H - 6.0,
    )
}

/// 第 `i` 格的命中框（**画布坐标**）。画与命中同源。
pub(crate) fn skill_step(row: LRect) -> f32 {
    (row.w - SKILL_SLOT_W) / (SKILL_SLOTS as f32 - 1.0)
}

pub(crate) fn skill_slot_rect(i: usize) -> crate::space::CRect {
    let b = layout_document()
        .get("skill_bar")
        .expect("skill bar is a HUD child widget");
    crate::space::canvas_rect(
        b.x + i as f32 * skill_step(b),
        b.y,
        SKILL_SLOT_W,
        SKILL_SLOT_H,
    )
}

/// 画布点落在技能栏哪一格（没中 ⇒ `None`）。
pub(crate) fn skill_slot_at(p: crate::space::CPt) -> Option<usize> {
    (0..SKILL_SLOTS).find(|i| crate::space::hit(skill_slot_rect(*i), p))
}

// ---------- 底栏**左下**那排快捷按钮 ----------
//
// 原版 `DBotMiniMap / DBotTrade / DBotGuild / DBotGroup / DBotPlusAbil / …`
// （`FState.pas:1305-1335`），一排横着摆，图号是**常态 / 按下**成对出现的
// （`DBotGroupDirectPaint`：`Downed` ⇒ `FaceIndex+1`，`FState.pas:3929-3947`）
// ⇒ 与右上那 4 颗（只有高亮态）**不是同一种**按钮。
//
// ⚠️ 好友那颗（官方 `DBotFriend` = 530）在本套素材里是空的 ⇒ 不画。

/// 底栏左下的快捷按钮。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum QuickBtn {
    /// 小地图（= **Tab** 同一个动作）。
    MiniMap,
    /// 交易（玩家之间；还没做）。
    Trade,
    /// 行会（还没做）。
    Guild,
    /// 组队（还没做）。
    Group,
    /// 属性 / 内挂（还没做）。
    PlusAbil,
    /// 注销（回登录界面）。
    Logout,
    /// 退出程序。
    Exit,
}

impl QuickBtn {
    pub const ALL: [Self; 7] = [
        Self::MiniMap,
        Self::Trade,
        Self::Guild,
        Self::Group,
        Self::PlusAbil,
        Self::Logout,
        Self::Exit,
    ];

    /// **常态**图的图号（按下态是 `+1`）。
    pub fn img(self) -> u32 {
        match self {
            Self::MiniMap => 130,
            Self::Trade => 132,
            Self::Guild => 134,
            Self::Group => 128,
            Self::PlusAbil => 140,
            Self::Logout => 136,
            Self::Exit => 138,
        }
    }

    /// 尺寸（30 一档的间距里，属性那颗宽一点）。
    pub fn size(self) -> (f32, f32) {
        match self {
            Self::PlusAbil => (36.0, 13.0),
            _ => (28.0, 13.0),
        }
    }

    /// 在**面板坐标**里的落点（官方 `FState.pas:1306-1320`：219 / 249 / 279 / 309 / 339，
    /// 同一行 y=104）—— 正好压在聊天框上方。
    pub fn at(self) -> (f32, f32) {
        let x = match self {
            Self::MiniMap => 219.0,
            Self::Trade => 249.0,
            Self::Guild => 279.0,
            Self::Group => 309.0,
            Self::PlusAbil => 339.0,
            // 官方 `FState.pas:1330-1335`：注销 560-30、退出 560
            Self::Logout => 530.0,
            Self::Exit => 560.0,
        };
        (x, 104.0)
    }

    /// 命中框（**屏幕坐标**）。
    ///
    /// 由布局 spec 中的整排节点统一计算首格/间距；常态图烘焙在底板，节点锁定并随底板移动。
    pub fn rect(self) -> crate::space::CRect {
        let l = layout_document();
        let (row_id, i, n) = match self {
            Self::MiniMap => ("quick_row", 0.0, 5.0),
            Self::Trade => ("quick_row", 1.0, 5.0),
            Self::Guild => ("quick_row", 2.0, 5.0),
            Self::Group => ("quick_row", 3.0, 5.0),
            Self::PlusAbil => ("quick_row", 4.0, 5.0),
            Self::Logout => ("quick_right", 0.0, 2.0),
            Self::Exit => ("quick_right", 1.0, 2.0),
        };
        let row = l
            .get(row_id)
            .expect("quick button row is a HUD child widget");
        let step = quick_step(row, n);
        let (w, h) = self.size();
        crate::space::canvas_rect(row.x + i * step, row.y, w, h)
    }
}

/// 画布点中了哪个快捷按钮（没中 ⇒ `None`）。
pub(crate) fn hud_quick_at(p: crate::space::CPt) -> Option<QuickBtn> {
    QuickBtn::ALL
        .into_iter()
        .find(|b| crate::space::hit(b.rect(), p))
}

/// 单球（只有血量那一支）的球底与血条美术，以及落点。
///
/// 官方对「**武士** 且 **等级 < 28**」用另一套：`Prguse[5]` 是球底、`Prguse[6]` 是血条
///（`FState.pas:3608-3619`，落点 `(38, btop+90)`，源矩形右边界都 `-2`）。
/// 我们另外把它用在「`MaxMP = 0`」上 —— 那种情况官方**什么都不画**，但用户要一个整球
///（2026-10-09 第 6 条），所以借这套单球美术显示血量，不显示魔量。
pub(crate) const HUD_ORB_SOLO: u32 = mir2_core::image_manifest::assets::HUD_ORB_SOLO.index;
pub(crate) const HUD_ORB_SOLO_FILL: u32 =
    mir2_core::image_manifest::assets::HUD_ORB_SOLO_FILL.index;
/// ⚠️ 现在单球也跟着 `LayoutDocument::orb` 走（D-134）⇒ 这个常量只作**对照**留着
///（原版单球落点比双球左 2 上 1）。
#[allow(dead_code)]
pub(crate) const ORB_SOLO_AT: (f32, f32) = (38.0, 90.0);

/// HP/MP 数值那一行（用户 2026-10-09 第 6 条："血量/魔量显示球体下，应显示数值"）。
///
/// ⚠️ 官方 1.76 的**底部面板**不画这两个数（只有人物状态窗口 `FState.pas:2866-2867` 画），
/// 这是按用户给的参考图加的一行：球体正下方，左 HP、右 MP。
pub(crate) const ORB_TEXT_DY: f32 = 210.0;

/// 数值文字的字高（`names` 用的是 `NAME_PX` = 12px）—— 板内**垂直居中**的分母。
pub(crate) const NUM_TEXT_H: f32 = 12.0;

/// 数值文字在板里的 Y：**板内垂直居中**（用户 2026-10-12："字 Y 应该在框的居中位置"）。
///
/// 抽成函数是为了能单测：字的 Y **由板算出来** ⇒ 编辑器里拖板/改板高，字都跟着动。
pub(crate) fn num_text_y_of(plate: LRect) -> f32 {
    plate.y + (plate.h - NUM_TEXT_H) / 2.0
}

/// 球下两块数值底板（HP / MP）的落点与显示宽（**面板坐标**，`.0` = 板**左缘**的 X，
/// `.1` = 板的显示宽）。
///
/// 照官方截图量的（2026-10-12）：两块板托住球（40..132）正下方，左板略压球左缘、
/// 右板右缘 ≈ 球右缘 + 6，中间留 ~8px 缝。底板图本身 64×14 ⇒ 显示宽 < 64 时水平
/// 略压缩（凹槽形状看不出变形）。**X 不对就改这四个数**，hp / mp 各自独立。
pub(crate) const NUM_PLATE_HP: (f32, f32) = (25.0, 47.0);
pub(crate) const NUM_PLATE_MP: (f32, f32) = (80.0, 58.0);

/// 面板**左右两块**的原始宽度。
///
/// ⚠️ 这两块必须 **1:1**（左边是球、右边是按钮/状态行，一拉就变形）；中间那块
/// （聊天框，本来就是矩形框）按窗口宽度**拉伸**。
/// 为什么需要这个：我们这套素材只有 **800 宽**的面板 —— 原版 1024 走
/// `BOTTOMBOARD1024 = Prguse[2]`，而本套的 `Prguse[2]` 是**空图号**（实测）⇒
/// 只能"左右保持原样、把中间那段拉宽"，而不是把整个面板放大 1.28 倍（那就又糊又变形了，
/// 正是用户 2026-10-09 说的那个观感）。
pub(crate) const HUD_SIDE_W: f32 = 200.0;

/// 右侧那块面板的左边缘（原版 800 版是 600 —— 我们这块宽 200，贴着右边）。
pub(crate) fn hud_right_x() -> f32 {
    WIN_W as f32 - HUD_SIDE_W
}

/// 等级在**屏幕坐标**里的落点（原版 800 版 `(660, SCREENHEIGHT-104)`；660 = 右块 + 60）。
pub(crate) fn level_at() -> (f32, f32) {
    (hud_right_x() + 60.0, WIN_H as f32 - 104.0)
}

/// 经验条在屏幕坐标里的落点（原版 800 版 `(666, SCREENHEIGHT-73)`；666 = 右块 + 66）。
pub(crate) fn exp_at() -> (f32, f32) {
    (hud_right_x() + 66.0, WIN_H as f32 - 73.0)
}

/// 负重条在屏幕坐标里的落点（原版 800 版 `(666, SCREENHEIGHT-40)`）。
pub(crate) fn weight_at() -> (f32, f32) {
    (hud_right_x() + 66.0, WIN_H as f32 - 40.0)
}

/// 聊天行距（原版 13px）。
pub(crate) const CHAT_LINE_H: f32 = 13.0;

// ---------- 聊天区（白底那块）----------
//
// ⚠️ 2026-10-11 用户报：「聊天文字区没对准、偏上」「背景应是白色」。
// 逐像素量了底栏 `Prguse[1]`（800×251）：**面板坐标 x 207..594 / y 120..243**
// 是一整块**纯白 (255,255,255)** —— 那就是聊天区。原来我们在这块白底上又铺了一层
// 深底（`RGB(24,24,30)`），而且文字从**白框顶部**往下排 ⇒ 只有一两行时看起来
// "飘在上面"。
//
// 现在：底色不动（底栏自带白底），文字**贴着白框底边往上排**（新的一行在最下面，
// 与原版"聊天从下往上滚"一致）。

/// 聊天区在**面板坐标**里的矩形（白底那块）。
///
/// ⚠️ 高度 2026-10-12 改成 **109**（原 124）：沿 `Prguse[1]` 的 x=300 逐像素扫过 ——
/// 白底文字区是 **y 120..228**，y 229..231 是一条深色分隔线，**232..243 才是打字区**
/// （再下面 244 又一条线）。原来按 124 算 ⇒ 最后一行落在 228..240，正好压在打字区上
/// （用户："聊天文字区域整体向上一行，最下端覆盖了打字区"）。
pub(crate) const CHAT_BOX_LOCAL: (f32, f32, f32, f32) = (207.0, 120.0, 388.0, 109.0);

/// 聊天区在**屏幕坐标**里的矩形。
///
/// ⚠️ 2026-10-12：底栏中段改"顶带平铺 + 聊天带三段"后（见 `hud_mid_w`），白区不再
/// 乘拉伸比 —— 聊天带是"左框 7px（原样）| 白区（拉伸）| 右框 6px（原样）"，
/// 白区宽 = 中段宽 − 13，左缘仍是面板 x=207。
pub(crate) fn chat_box() -> (f32, f32, f32, f32) {
    let (lx, ly, _, lh) = CHAT_BOX_LOCAL;
    (lx, WIN_H as f32 - HUD_BOARD_H + ly, hud_mid_w() - 13.0, lh)
}

/// 第 `i` 行（共 `shown` 行）的 y：**贴底** ⇒ 最后一行落在白框底边往上一行处。
pub(crate) fn chat_line_y(i: usize, shown: usize) -> f32 {
    // ⚠️ 读**当前布局**（D-134）而不是上面的 `chat_box()` 默认值 —— 否则在编辑器里
    // 挪了聊天框，框动了、字还在原地（用户 2026-10-12）。
    let c = layout_document()
        .get("chat_box")
        .expect("chat box is a HUD child widget");
    let (_, y, _, h) = (c.x, c.y, c.w, c.h);
    y + h - (shown - i) as f32 * CHAT_LINE_H - 3.0
}

/// **液面裁切**：按百分比取球的"下半部分"。
///
/// 原版就是这么一个式子（`FState.pas:3784-3795`）：
/// `rc.Top := Round(rc.Bottom / Max * (Max - Cur))`，落点也跟着下移同样的量
/// ⇒ 看得见的永远是**下面 `pct` 那一截**（像球里的液面）。
///
/// 返回 `(液面在球内的 y, 可见高度)`。
pub(crate) fn gauge_band(pct: f32, h: i32) -> (i32, i32) {
    let pct = pct.clamp(0.0, 1.0);
    let top = (h as f32 * (1.0 - pct)).round() as i32;
    (top, (h - top).max(0))
}

/// 内置字体等宽 8px ⇒ 一行能放多少列（两侧各留 1 列边距）。
///
/// 用窗口宽度算，而不是写死列数：窗口加宽后信息条/调试读数/提示条应当铺满，
/// 否则宽出来的部分白放着，长内容（比如探针读数）还会被无谓截断。
pub(crate) const TEXT_COLS: usize = WIN_W as usize / 8 - 2;

/// 右侧信息区每行最大列数（内置字体等宽 8px）。
pub(crate) const INFO_LINE_H: f32 = 16.0;

// ---------- 背包窗（`Prguse[3]`）----------

/// 背包背板图号：`Prguse[3]`（336×270）。
///
/// 素材里的东西（2026-10-09 放大 2 倍 + 扫像素量出来的）：**6 列 × 4 行 = 24 格**、
/// 左下角一个圆槽（原版是"手上的物品"）、右侧一条滚动条、右下 `USE`、右下角 **关闭 X**、
/// 下面两条宽横条（金币显示用）。版式常量都是**从这张图的像素量出来的**，不是猜的。
pub(crate) const BAG_BG: u32 = mir2_core::image_manifest::assets::BAG_BACKGROUND.index;
pub(crate) const BAG_W: f32 = 336.0;
pub(crate) const BAG_H: f32 = 270.0;

/// 网格：6 列 × 4 行，格子 35.5×32.5（经典 Mir2 的物品格就是 36×32），
/// 原点相对窗口 = (20.5, 8)。
pub(crate) const BAG_COLS: usize = 6;
pub(crate) const BAG_ROWS: usize = 4;
pub(crate) const BAG_CELL_W: f32 = 35.5;
pub(crate) const BAG_CELL_H: f32 = 32.5;
pub(crate) const BAG_GRID_X: f32 = 20.5;
pub(crate) const BAG_GRID_Y: f32 = 8.0;

/// 一页几格（`BAG_COLS * BAG_ROWS`）。服务端背包是 46 格（`MAXBAGITEM`），
/// 所以**要翻页**：滚轮在窗内翻页，页码画在网格右下角。
pub(crate) const BAG_PAGE_SLOTS: usize = BAG_COLS * BAG_ROWS;

/// 当前物品数需要几页（空背包也保留第 0 页）。
///
/// ⚠️ 画页码、滚轮翻页、`Pager` 夹取都走这一个算法；以前三处各算一遍，且滚轮
/// 的空背包页数是 0（虽然 `.max(1)` 勉强兜住了），容易以后只改一处。
pub(crate) fn bag_pages(items: usize) -> usize {
    items.div_ceil(BAG_PAGE_SLOTS).max(1)
}

/// 关闭按钮（`X`）在窗口内的位置 —— 素材右下角那个红叉。
///
/// ⚠️ 坐标是**拿 `wzldump` 导出 `Prguse[3]` 逐像素量出来的**（2026-10-10 第二次量，
/// 这次是**全图**扫红色像素）：整块板子上只有一处红区 **x 311..321 / y 206..221**。
/// 上一轮量错了 —— 当时只扫了局部、把 x264..294 那块**装饰**当成了关闭叉
///（用户再报"包裹窗口的关闭按钮不能点"就是这个原因）。
pub(crate) const BAG_CLOSE_X: f32 = 308.0;
pub(crate) const BAG_CLOSE_Y: f32 = 203.0;
pub(crate) const BAG_CLOSE_W: f32 = 20.0;
pub(crate) const BAG_CLOSE_H: f32 = 24.0;

/// **关闭钮的高亮图** = `Prguse[64]`（16×23 红叉；用户 2026-10-11 第 4 条给的
/// 编号 64/371 —— 逐像素比对**完全相同**，是同一张图拷了两份 ⇒ 用 64）。
pub(crate) const BAG_CLOSE_IMG: u32 = mir2_core::image_manifest::assets::BAG_CLOSE.index;

/// 金币文字在窗口内的落点（下方那条宽横条里）。
///
/// ⚠️ 只画**数字**不画"金币"两个字（用户 2026-10-10 第 8 条：原版横条左侧是
/// 一枚金币图标 + 数字；我们的板子上图标已自带，再写"金币"就重复了）。
/// y 取 184：横条本体在 y≈181..200，原来写 186 加上"金币 "前缀后视觉偏下。
/// 2026-10-12 用户再调：**右移 5px、上移 2px**。
pub(crate) const BAG_GOLD_X: f32 = 61.0;
pub(crate) const BAG_GOLD_Y: f32 = 182.0;

/// 金币**图标**（`Prguse[29]`，36×21 的金币堆）与落点。
///
/// ⚠️ 原版 `DGold` 控件（`FState.pas:1255-1257`）：`SetImgIndex(g_WMainImages, 29)`、
/// `Left := 10; Top := 190` —— 图号与落点照抄。**核查过：原版不按金额换图**。
pub(crate) const BAG_GOLD_ICON: u32 = mir2_core::image_manifest::assets::BAG_GOLD_ICON.index;
pub(crate) const BAG_GOLD_ICON_AT: (f32, f32) = (10.0, 190.0);

/// 背包窗落点。
///
/// 平时：**左侧、对话窗下面**（`(8, 188)`）—— 右上被小地图占着、左上被对话窗占着。
/// **商店开着时挪到右边**（用户 2026-10-10 第 7 条：原版买东西时包裹就在右侧，
/// 左边的位置让给商品列表）。
pub(crate) fn bag_pos(shop_open: bool) -> (f32, f32) {
    if shop_open {
        // ⚠️ 照原版截图（用户 2026-10-10）：卖出时包裹**紧贴卖窗右侧**——
        // 抓起物品 → 放进圆槽的鼠标移动距离最短。底部大致与卖窗底齐平
        //（卖窗 181 高、包裹 270 高 ⇒ 包裹顶部会高出卖窗一截，与原版一致）。
        let (sx, sy, sw, sh) = crate::shop::sell_panel();
        let x = sx + sw + 12.0;
        let y = (sy + sh - BAG_H).max(4.0);
        (x, y)
    } else {
        (8.0, 4.0 + crate::dialog::DIALOG_H + 8.0)
    }
}

/// 背包网格（**唯一来源**）：`bag_cell_rect` / `bag_slot_at` 都由它派生
/// ⇒ 不会出现"画在这一格、命中算到那一格"（官方 `TDGrid` 的矩形即命中区）。
pub(crate) fn bag_grid() -> crate::widget::Grid {
    crate::widget::Grid::new(
        crate::space::LPt(crate::space::Lpx(BAG_GRID_X), crate::space::Lpx(BAG_GRID_Y)),
        BAG_COLS,
        BAG_ROWS,
        crate::space::Lpx(BAG_CELL_W),
        crate::space::Lpx(BAG_CELL_H),
    )
}

/// 第 `slot` 格（**页内下标**）在窗口内的矩形 `(x, y, w, h)`。
pub(crate) fn bag_cell_rect(slot: usize) -> (f32, f32, f32, f32) {
    let r = bag_grid().cell_rect(slot);
    (r.0 .0, r.1 .0, r.2 .0, r.3 .0)
}

/// 窗口内的一点落在哪一格（`None` = 没落在网格里）。与 [`bag_cell_rect`] 同源。
pub(crate) fn bag_slot_at(local: (f32, f32)) -> Option<usize> {
    bag_grid().hit_cell(crate::space::LPt(
        crate::space::Lpx(local.0),
        crate::space::Lpx(local.1),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 背包页数边界统一且至少一页() {
        assert_eq!(bag_pages(0), 1, "空背包也有第 0 页");
        assert_eq!(bag_pages(1), 1);
        assert_eq!(bag_pages(BAG_PAGE_SLOTS), 1);
        assert_eq!(bag_pages(BAG_PAGE_SLOTS + 1), 2);
        assert_eq!(bag_pages(46), 2, "服务端背包 46 格 ⇒ 两页");
    }
}

// ---------- 跨场景布局文档（D-134/D-136）----------
//
// 节点 spec 给出默认树；唯一可变状态是 `LayoutDocument::overrides`。
// 场景投影是只读 adapter，编辑器与配置文件不会另存另一份 HUD/scene 几何。

/// 一个布局矩形；坐标属于其 `EditorScope`，子节点相对 parent，根节点相对 viewport。
#[derive(Clone, Copy, Debug, PartialEq)]
pub(crate) struct LRect {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
}

impl LRect {
    pub(crate) fn new(x: f32, y: f32, w: f32, h: f32) -> Self {
        Self { x, y, w, h }
    }
    /// 是否含这个点（`pad` 是外扩的手抖余量）。
    pub(crate) fn hit(self, p: (f32, f32), pad: f32) -> bool {
        p.0 >= self.x - pad
            && p.0 <= self.x + self.w + pad
            && p.1 >= self.y - pad
            && p.1 <= self.y + self.h + pad
    }
}

/// 布局作用域同时定义节点几何的坐标空间。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Default)]
pub(crate) enum EditorScope {
    #[default]
    World,
    Login,
    Select,
}

/// World 场景可见窗口与覆盖层的共用底到顶顺序；renderer 遍历此表，命中使用 `z()`。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum WorldUiLayer {
    Dialog,
    Bag,
    Shop,
    Sell,
    BagOverlay,
    Minimap,
    Bigmap,
    Status,
    Confirm,
}

impl WorldUiLayer {
    pub(crate) const fn z(self) -> i32 {
        match self {
            Self::Dialog => 10,
            Self::Bag => 20,
            Self::Shop => 30,
            Self::Sell => 31,
            Self::BagOverlay => 35,
            Self::Minimap => 40,
            Self::Bigmap => 41,
            Self::Status => 50,
            Self::Confirm => 100,
        }
    }
}

pub(crate) const WORLD_UI_STACK: [WorldUiLayer; 9] = [
    WorldUiLayer::Dialog,
    WorldUiLayer::Bag,
    WorldUiLayer::Shop,
    WorldUiLayer::Sell,
    WorldUiLayer::BagOverlay,
    WorldUiLayer::Minimap,
    WorldUiLayer::Bigmap,
    WorldUiLayer::Status,
    WorldUiLayer::Confirm,
];

const fn world_ui_stack_is_ordered() -> bool {
    let mut i = 1;
    while i < WORLD_UI_STACK.len() {
        if WORLD_UI_STACK[i - 1].z() >= WORLD_UI_STACK[i].z() {
            return false;
        }
        i += 1;
    }
    true
}

const _: () = assert!(world_ui_stack_is_ordered());

const EDITOR_SCOPES: [EditorScope; 3] =
    [EditorScope::World, EditorScope::Login, EditorScope::Select];

impl EditorScope {
    pub(crate) fn from_scene(scene: crate::app_state::Scene) -> Option<Self> {
        match scene {
            crate::app_state::Scene::World => Some(Self::World),
            crate::app_state::Scene::Login => Some(Self::Login),
            crate::app_state::Scene::Select => Some(Self::Select),
            crate::app_state::Scene::AssetView => None,
        }
    }

    pub(crate) fn viewport(self) -> LRect {
        let (w, h) = match self {
            Self::World => (WIN_W as f32, WIN_H as f32),
            Self::Login | Self::Select => (
                crate::window::UI_WIN.0 as f32,
                crate::window::UI_WIN.1 as f32,
            ),
        };
        LRect::new(0.0, 0.0, w, h)
    }
}

/// 布局 spec（WidgetTree）是场景默认值，`overrides` 是跨场景唯一的用户可变布局状态。
/// 绘制、命中、编辑与持久化都通过同一文档查询/更新，不另存 HUD 或 scene 可变几何字段。
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct LayoutDocument {
    overrides: std::collections::HashMap<&'static str, crate::widget::LayoutValue>,
}

pub(crate) const HUD_PANEL_ID: &str = "hud_panel";

/// 控件的中文名（设计模式叠加层）。
pub(crate) fn item_name(id: &str) -> &str {
    match id {
        "hud_panel" => "底栏窗口(拖动带动子控件)",
        "orb" => "血魔法球",
        "plate_hp" => "HP数值",
        "plate_mp" => "MP数值",
        "exp_bar" => "经验条",
        "weight_bar" => "负重条",
        "level_num" => "等级数字",
        "chat_box" => "聊天区",
        "btn_state" => "按钮人物",
        "btn_bag" => "按钮包裹",
        "btn_magic" => "按钮技能",
        "btn_option" => "按钮选项",
        "quick_row" => "快捷按钮5颗(改宽=改间距)",
        "quick_right" => "注销退出2颗(改宽=改间距)",
        "belt" => "腰带6格(改宽=改间距)",
        "skill_bar" => "技能栏8格(改宽=改间距)",
        "atk_text" => "攻击模式文字",
        "clock" => "时钟文字",
        "window_status" => "状态窗(原生尺寸)",
        "window_dialog" => "对话窗(原生尺寸)",
        "login.panel" => "登录/建号面板",
        "login.account" => "登录账号输入框",
        "login.password" => "登录密码输入框",
        "login.confirm" => "建号确认密码框",
        "login.ok" => "登录按钮",
        "login.new" => "新用户按钮",
        "login.chgpw" => "修改密码按钮",
        "login.close" => "登录关闭按钮",
        "login.signup_ok" => "建号确定按钮",
        "login.signup_cancel" => "建号取消按钮",
        "login.door" => "开门动画",
        "login.msgbox" => "登录消息框",
        "login.msg_ok" => "登录消息框确定",
        "select.panel" => "选角背景与界面",
        "select.slot.left" => "左侧角色位置",
        "select.slot.right" => "右侧角色位置",
        "select.info.left" => "左侧角色文字",
        "select.info.right" => "右侧角色文字",
        "select.menu.start" => "开始按钮",
        "select.menu.create" => "创建人物按钮",
        "select.menu.delete" => "删除人物按钮",
        "select.menu.credits" => "制作群按钮",
        "select.menu.exit" => "退出按钮",
        "select.newchar" => "新建角色窗口",
        "select.newchar.name" => "新建角色姓名框",
        "select.newchar.job.0" => "新建角色战士选项",
        "select.newchar.job.1" => "新建角色法师选项",
        "select.newchar.job.2" => "新建角色道士选项",
        "select.newchar.sex.0" => "新建角色男性选项",
        "select.newchar.sex.1" => "新建角色女性选项",
        "select.newchar.ok" => "新建角色确定按钮",
        "select.newchar.close" => "新建角色关闭按钮",
        "select.msgbox" => "选角消息框",
        "select.msg_ok" => "选角消息框确定",
        _ => id,
    }
}

fn scene_measure(lib: &'static str, index: u32) -> Option<(u32, u32)> {
    if let Some(size) = match (lib, index) {
        ("Prguse", 60) => Some((296, 254)),
        ("Prguse", 62) => Some((76, 33)),
        ("Prguse", 61) => Some((100, 32)),
        ("Prguse", 51) => Some((96, 34)),
        ("Prguse", 52) => Some((96, 33)),
        ("Prguse", 64) => Some((16, 23)),
        ("Prguse", 53) => Some((128, 33)),
        ("Prguse", 360) => Some((452, 179)),
        ("Prguse", 363) => Some((80, 34)),
        ("Prguse", 66 | 67) => Some((76, 33)),
        ("Prguse", 68) => Some((44, 21)),
        ("Prguse", 69 | 70) => Some((120, 21)),
        ("Prguse", 71) => Some((92, 20)),
        ("Prguse", 72) => Some((56, 20)),
        ("ChrSel", 24) => Some((496, 361)),
        _ => None,
    } {
        return Some(size);
    }
    None
}

pub(crate) fn core_login_layout() -> mir2_core::login_ui::Layout {
    mir2_core::login_ui::Layout::build(crate::window::UI_WIN, scene_measure)
        .expect("verified login widget metrics")
}

pub(crate) fn core_select_layout() -> mir2_core::select_ui::Layout {
    mir2_core::select_ui::Layout::build(crate::window::UI_WIN, scene_measure)
        .expect("verified select widget metrics")
}

fn core_msgbox() -> mir2_core::select_ui::MsgBox {
    mir2_core::select_ui::MsgBox::build(crate::window::UI_WIN, (452, 179), (80, 34))
}

fn to_lrect(r: mir2_core::login_ui::Rect) -> LRect {
    LRect::new(r.x, r.y, r.w, r.h)
}

fn select_rect(r: mir2_core::select_ui::Rect) -> LRect {
    LRect::new(r.x, r.y, r.w, r.h)
}

fn local_to(parent: LRect, child: LRect) -> LRect {
    LRect::new(child.x - parent.x, child.y - parent.y, child.w, child.h)
}

fn enforce_edit_size(policy: crate::widget::EditPolicy, default: LRect, mut rect: LRect) -> LRect {
    if policy.resizable {
        let max_w = policy.max_w.unwrap_or(f32::INFINITY).max(policy.min_w);
        let max_h = policy.max_h.unwrap_or(f32::INFINITY).max(policy.min_h);
        rect.w = rect.w.max(policy.min_w).min(max_w);
        rect.h = rect.h.max(policy.min_h).min(max_h);
    } else {
        rect.w = default.w;
        rect.h = default.h;
    }
    rect
}

#[allow(clippy::too_many_arguments)]
fn layout_node(
    id: &'static str,
    parent: Option<&'static str>,
    rect: LRect,
    align: crate::widget::Align,
    z: i32,
    clip_children: bool,
    input: crate::widget::InputPolicy,
    edit: crate::widget::EditPolicy,
    visual: crate::widget::VisualSource,
) -> crate::widget::LayoutNode {
    crate::widget::LayoutNode {
        id,
        parent,
        rect,
        align,
        z,
        clip_children,
        input,
        edit,
        visual,
    }
}

fn scene_node(
    id: &'static str,
    parent: Option<&'static str>,
    rect: LRect,
    z: i32,
    clip_children: bool,
    input: crate::widget::InputPolicy,
    edit: crate::widget::EditPolicy,
) -> crate::widget::LayoutNode {
    layout_node(
        id,
        parent,
        rect,
        crate::widget::Align::TOP_LEFT,
        z,
        clip_children,
        input,
        edit,
        crate::widget::VisualSource::Independent,
    )
}

fn scene_default_tree(scope: EditorScope) -> crate::widget::WidgetTree {
    use crate::widget::{EditPolicy, InputPolicy, WidgetTree};
    use mir2_core::select_ui::{DialogBox, Rect as SRect};

    let mut tree = WidgetTree::default();
    match scope {
        EditorScope::World => {}
        EditorScope::Login => {
            let l = core_login_layout();
            let panel = to_lrect(l.dialog);
            tree.push(scene_node(
                "login.panel",
                None,
                panel,
                0,
                true,
                InputPolicy::Block,
                EditPolicy::MOVE_ONLY,
            ));
            for (id, rect, resize) in [
                ("login.account", l.account, true),
                ("login.password", l.password, true),
                ("login.confirm", l.confirm, true),
                ("login.ok", l.ok, false),
                ("login.new", l.new, false),
                ("login.chgpw", l.chgpw, false),
                ("login.close", l.close, false),
                ("login.signup_ok", l.signup_ok, false),
                ("login.signup_cancel", l.signup_cancel, false),
            ] {
                tree.push(scene_node(
                    id,
                    Some("login.panel"),
                    local_to(panel, to_lrect(rect)),
                    1,
                    false,
                    InputPolicy::Target,
                    if resize {
                        EditPolicy::MOVE_RESIZE
                    } else {
                        EditPolicy::MOVE_ONLY
                    },
                ));
            }
            let door_size = scene_measure("ChrSel", 24).unwrap();
            tree.push(scene_node(
                "login.door",
                None,
                LRect::new(
                    mir2_core::login_ui::Layout::DOOR_AT.0,
                    mir2_core::login_ui::Layout::DOOR_AT.1,
                    door_size.0 as f32,
                    door_size.1 as f32,
                ),
                10,
                true,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
            let msg = to_lrect(l.msgbox);
            tree.push(scene_node(
                "login.msgbox",
                None,
                msg,
                20,
                true,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
            tree.push(scene_node(
                "login.msg_ok",
                Some("login.msgbox"),
                local_to(msg, to_lrect(l.msg_ok)),
                1,
                false,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
        }
        EditorScope::Select => {
            let l = core_select_layout();
            let panel = LRect::new(l.bg.0, l.bg.1, 800.0, 600.0);
            tree.push(scene_node(
                "select.panel",
                None,
                panel,
                0,
                false,
                InputPolicy::Block,
                EditPolicy::MOVE_ONLY,
            ));
            for (id, rect) in [
                ("select.slot.left", l.slots[0]),
                ("select.slot.right", l.slots[1]),
            ] {
                tree.push(scene_node(
                    id,
                    Some("select.panel"),
                    local_to(panel, select_rect(rect)),
                    2,
                    false,
                    InputPolicy::Target,
                    EditPolicy::MOVE_ONLY,
                ));
            }
            for (id, slot) in [("select.info.left", 0), ("select.info.right", 1)] {
                let text = l.slot_texts[slot];
                let info = LRect::new(text.name.0, text.name.1, 150.0, 78.0);
                tree.push(scene_node(
                    id,
                    Some("select.panel"),
                    local_to(panel, info),
                    2,
                    false,
                    InputPolicy::Target,
                    EditPolicy::MOVE_ONLY,
                ));
            }
            for (i, id) in [
                "select.menu.start",
                "select.menu.create",
                "select.menu.delete",
                "select.menu.credits",
                "select.menu.exit",
            ]
            .into_iter()
            .enumerate()
            {
                let mut node = scene_node(
                    id,
                    Some("select.panel"),
                    local_to(panel, select_rect(l.menu[i])),
                    3,
                    false,
                    InputPolicy::Target,
                    EditPolicy::LOCKED,
                );
                node.visual = crate::widget::VisualSource::BackgroundBakedWithOverlay;
                tree.push(node);
            }
            let dialog = DialogBox::build(crate::window::UI_WIN);
            let frame = select_rect(dialog.frame);
            tree.push(scene_node(
                "select.newchar",
                None,
                frame,
                20,
                true,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
            let sub = |r: SRect| local_to(frame, select_rect(r));
            tree.push(scene_node(
                "select.newchar.name",
                Some("select.newchar"),
                sub(dialog.name),
                1,
                false,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
            for (i, r) in dialog.job.into_iter().enumerate() {
                tree.push(scene_node(
                    [
                        "select.newchar.job.0",
                        "select.newchar.job.1",
                        "select.newchar.job.2",
                    ][i],
                    Some("select.newchar"),
                    sub(r),
                    1,
                    false,
                    InputPolicy::Ignore,
                    EditPolicy::MOVE_ONLY,
                ));
            }
            for (i, r) in dialog.sex.into_iter().enumerate() {
                tree.push(scene_node(
                    ["select.newchar.sex.0", "select.newchar.sex.1"][i],
                    Some("select.newchar"),
                    sub(r),
                    1,
                    false,
                    InputPolicy::Ignore,
                    EditPolicy::MOVE_ONLY,
                ));
            }
            for (id, rect) in [
                ("select.newchar.ok", dialog.ok),
                ("select.newchar.close", dialog.close),
            ] {
                tree.push(scene_node(
                    id,
                    Some("select.newchar"),
                    sub(rect),
                    1,
                    false,
                    InputPolicy::Ignore,
                    EditPolicy::MOVE_ONLY,
                ));
            }
            let msgbox = core_msgbox();
            let frame = select_rect(msgbox.frame);
            tree.push(scene_node(
                "select.msgbox",
                None,
                frame,
                30,
                true,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
            tree.push(scene_node(
                "select.msg_ok",
                Some("select.msgbox"),
                local_to(frame, select_rect(msgbox.ok)),
                1,
                false,
                InputPolicy::Ignore,
                EditPolicy::MOVE_ONLY,
            ));
        }
    }
    tree
}

fn default_world_tree() -> crate::widget::WidgetTree {
    use crate::widget::{Align, EditPolicy, InputPolicy, VisualSource, WidgetTree};
    let board_y = crate::window::WIN_H as f32 - HUD_BOARD_H;
    let panel = LRect::new(0.0, 0.0, crate::window::WIN_W as f32, HUD_BOARD_H);
    let viewport = layout_viewport();
    let mut tree = WidgetTree::default();
    tree.push(layout_node(
        HUD_PANEL_ID,
        None,
        panel,
        Align::BOTTOM_LEFT,
        0,
        false,
        InputPolicy::Block,
        EditPolicy::MOVE_ONLY,
        VisualSource::Independent,
    ));
    tree.push(layout_node(
        "window_status",
        None,
        LRect::new(
            (viewport.w - crate::status::BG_W).max(0.0),
            crate::status::TOP,
            crate::status::BG_W,
            crate::status::BG_H,
        ),
        Align::TOP_LEFT,
        20,
        true,
        InputPolicy::Target,
        EditPolicy::MOVE_ONLY,
        VisualSource::Independent,
    ));
    tree.push(layout_node(
        "window_dialog",
        None,
        LRect::new(
            crate::dialog::DIALOG_X,
            crate::dialog::DIALOG_Y,
            crate::dialog::DIALOG_W,
            crate::dialog::DIALOG_H,
        ),
        Align::TOP_LEFT,
        30,
        true,
        InputPolicy::Target,
        EditPolicy::MOVE_ONLY,
        VisualSource::Independent,
    ));

    let local_y = |screen_y: f32| screen_y - board_y;
    let child = |id, rect, edit, visual| {
        layout_node(
            id,
            Some(HUD_PANEL_ID),
            rect,
            Align::TOP_LEFT,
            1,
            false,
            InputPolicy::Target,
            edit,
            visual,
        )
    };
    for (id, rect, edit) in [
        (
            "orb",
            LRect::new(ORB_AT.0, ORB_AT.1, 92.0, 90.0),
            EditPolicy::MOVE_ONLY,
        ),
        (
            "plate_hp",
            LRect::new(NUM_PLATE_HP.0, ORB_TEXT_DY - 2.0, NUM_PLATE_HP.1, 20.0),
            EditPolicy::MOVE_RESIZE,
        ),
        (
            "plate_mp",
            LRect::new(NUM_PLATE_MP.0, ORB_TEXT_DY - 2.0, NUM_PLATE_MP.1, 20.0),
            EditPolicy::MOVE_RESIZE,
        ),
        (
            "exp_bar",
            LRect::new(exp_at().0, local_y(exp_at().1), 76.0, 13.0),
            EditPolicy::MOVE_ONLY,
        ),
        (
            "weight_bar",
            LRect::new(weight_at().0, local_y(weight_at().1), 76.0, 13.0),
            EditPolicy::MOVE_ONLY,
        ),
        (
            "level_num",
            LRect::new(level_at().0, local_y(level_at().1), 12.0, 10.0),
            EditPolicy::MOVE_ONLY,
        ),
    ] {
        tree.push(child(id, rect, edit, VisualSource::Independent));
    }

    let (chat_x, chat_y, chat_w, chat_h) = chat_box();
    tree.push(child(
        "chat_box",
        LRect::new(chat_x, local_y(chat_y), chat_w, chat_h),
        EditPolicy::MOVE_RESIZE,
        VisualSource::Independent,
    ));
    for (id, button) in [
        ("btn_state", HudBtn::State),
        ("btn_bag", HudBtn::Bag),
        ("btn_magic", HudBtn::Magic),
        ("btn_option", HudBtn::Option),
    ] {
        tree.push(child(
            id,
            LRect::new(hud_local_x(button.at().0), button.at().1, 24.0, 24.0),
            EditPolicy::LOCKED,
            VisualSource::BackgroundBakedWithOverlay,
        ));
    }
    for (id, x, count) in [
        ("quick_row", QuickBtn::MiniMap.at().0, 5.0),
        ("quick_right", QuickBtn::Logout.at().0, 2.0),
    ] {
        tree.push(child(
            id,
            LRect::new(
                hud_local_x(x),
                QuickBtn::MiniMap.at().1,
                (count - 1.0) * QUICK_STEP + 28.0,
                13.0,
            ),
            EditPolicy::LOCKED,
            VisualSource::BackgroundBakedWithOverlay,
        ));
    }
    tree.push(child(
        "belt",
        LRect::new(
            hud_local_x(BELT_AT.0),
            BELT_AT.1,
            5.0 * BELT_STEP + BELT_W,
            BELT_H,
        ),
        EditPolicy::LOCKED,
        VisualSource::BackgroundBaked,
    ));
    let (skill_x, skill_y) = skill_bar_at();
    tree.push(child(
        "skill_bar",
        LRect::new(
            skill_x,
            local_y(skill_y),
            7.0 * SKILL_SLOT_STEP + SKILL_SLOT_W,
            SKILL_SLOT_H,
        ),
        EditPolicy::MOVE_RESIZE,
        VisualSource::Independent,
    ));
    tree.push(child(
        "atk_text",
        LRect::new(
            hud_right_x() + 56.0,
            local_y(crate::window::WIN_H as f32 - 136.0),
            64.0,
            14.0,
        ),
        EditPolicy::MOVE_ONLY,
        VisualSource::Independent,
    ));
    tree.push(child(
        "clock",
        LRect::new(
            hud_right_x() + 66.0,
            local_y(crate::window::WIN_H as f32 - 22.0),
            64.0,
            14.0,
        ),
        EditPolicy::MOVE_ONLY,
        VisualSource::Independent,
    ));
    tree
}

fn default_tree_for_scope(scope: EditorScope) -> crate::widget::WidgetTree {
    match scope {
        EditorScope::World => default_world_tree(),
        EditorScope::Login | EditorScope::Select => scene_default_tree(scope),
    }
}

fn layout_specs() -> &'static std::collections::HashMap<EditorScope, crate::widget::WidgetTree> {
    static SPECS: std::sync::OnceLock<
        std::collections::HashMap<EditorScope, crate::widget::WidgetTree>,
    > = std::sync::OnceLock::new();
    SPECS.get_or_init(|| {
        let mut specs = std::collections::HashMap::new();
        let mut ids = Vec::new();
        for scope in EDITOR_SCOPES {
            let tree = default_tree_for_scope(scope);
            tree.resolve(scope.viewport())
                .expect("layout schema must have unique ids and valid parents");
            for node in tree.nodes() {
                assert!(
                    !ids.contains(&node.id),
                    "duplicate cross-scene layout id {}",
                    node.id
                );
                ids.push(node.id);
            }
            specs.insert(scope, tree);
        }
        specs
    })
}

/// 配置文件 ID 清单直接从全场景共用的不可变布局 spec 派生。
pub(crate) fn layout_item_ids() -> &'static [&'static str] {
    static IDS: std::sync::OnceLock<Vec<&'static str>> = std::sync::OnceLock::new();
    IDS.get_or_init(|| {
        EDITOR_SCOPES
            .into_iter()
            .flat_map(|scope| layout_specs()[&scope].nodes().iter().map(|node| node.id))
            .collect()
    })
}

impl LayoutDocument {
    pub(crate) fn default_layout() -> Self {
        let _ = layout_specs();
        Self {
            overrides: std::collections::HashMap::new(),
        }
    }

    fn scope_for_id(&self, id: &str) -> Option<EditorScope> {
        if let Some(scope) = scene_scope_for_id(id) {
            Some(scope)
        } else {
            layout_specs()
                .get(&EditorScope::World)
                .and_then(|tree| tree.node(id))
                .map(|_| EditorScope::World)
        }
    }

    fn tree(&self, scope: EditorScope) -> crate::widget::WidgetTree {
        let mut tree = layout_specs().get(&scope).cloned().unwrap_or_default();
        for node in tree.nodes_mut() {
            if let Some(value) = self.overrides.get(node.id) {
                node.rect = value.rect;
                node.align = value.align;
            }
        }
        tree
    }

    #[cfg(test)]
    pub(crate) fn panel_rect(&self) -> LRect {
        self.get(HUD_PANEL_ID).expect("HUD panel node exists")
    }

    pub(crate) fn get(&self, id: &str) -> Option<LRect> {
        let scope = self.scope_for_id(id)?;
        self.tree(scope).resolved_rect(scope.viewport(), id)
    }

    pub(crate) fn stored_rect(&self, id: &str) -> Option<LRect> {
        let scope = self.scope_for_id(id)?;
        self.tree(scope).node(id).map(|node| node.rect)
    }

    pub(crate) fn align(&self, id: &str) -> Option<crate::widget::Align> {
        let scope = self.scope_for_id(id)?;
        self.tree(scope).node(id).map(|node| node.align)
    }

    fn set_value(&mut self, id: &str, rect: LRect, align: crate::widget::Align) {
        let Some(scope) = self.scope_for_id(id) else {
            return;
        };
        let Some(spec) = layout_specs().get(&scope).and_then(|tree| tree.node(id)) else {
            return;
        };
        if (!spec.edit.movable && !spec.edit.resizable)
            || spec.visual != crate::widget::VisualSource::Independent
        {
            return;
        }
        let rect = enforce_edit_size(spec.edit, spec.rect, rect);
        if rect == spec.rect && align == spec.align {
            self.overrides.remove(id);
        } else {
            self.overrides
                .insert(spec.id, crate::widget::LayoutValue { rect, align });
        }
    }

    pub(crate) fn set_align(&mut self, id: &str, align: crate::widget::Align) {
        let Some(rect) = self.stored_rect(id) else {
            return;
        };
        self.set_value(id, rect, align);
    }

    pub(crate) fn set_stored(&mut self, id: &str, rect: LRect) {
        let Some(scope) = self.scope_for_id(id) else {
            return;
        };
        let Some(default_align) = layout_specs()
            .get(&scope)
            .and_then(|tree| tree.node(id))
            .map(|node| node.align)
        else {
            return;
        };
        let align = self.align(id).unwrap_or(default_align);
        self.set_value(id, rect, align);
    }

    /// 设置解析空间绝对矩形；写入统一文档时反算成 parent-local/anchor-offset。
    pub(crate) fn set(&mut self, id: &str, rect: LRect) {
        let Some(scope) = self.scope_for_id(id) else {
            return;
        };
        let tree = self.tree(scope);
        let Some(node) = tree.node(id) else { return };
        let viewport = scope.viewport();
        let parent = node
            .parent
            .and_then(|parent| tree.resolved_rect(viewport, parent))
            .unwrap_or(viewport);
        let align = node.align;
        let rect = enforce_edit_size(node.edit, node.rect, rect);
        let stored = crate::widget::unalign_rect(rect, parent, align);
        self.set_value(id, stored, align);
    }

    /// 运行时 HUD 输入窗树由同一布局文档派生；动态模态层由 `world_input` 添加。
    pub(crate) fn widget_tree(&self) -> crate::widget::WidgetTree {
        let mut runtime = crate::widget::WidgetTree::default();
        for node in self
            .tree(EditorScope::World)
            .nodes()
            .iter()
            .filter(|node| node.id != "window_status" && node.id != "window_dialog")
        {
            runtime.push(*node);
        }
        runtime
    }

    /// 当前场景的 widget 文档；默认 spec 与用户覆盖在同一个布局文档中解析。
    pub(crate) fn editor_tree(&self, scope: EditorScope) -> crate::widget::WidgetTree {
        self.tree(scope)
    }

    /// 编辑事务直接写回唯一布局文档；绘制与命中之后都从同一文档解析。
    pub(crate) fn apply_widget_tree(
        &mut self,
        tree: &crate::widget::WidgetTree,
        _scope: EditorScope,
    ) {
        for node in tree.nodes() {
            self.set_value(node.id, node.rect, node.align);
        }
    }

    /// 登录绘制和点击共用通用文档解析后的 core 几何。
    pub(crate) fn login_layout(
        &self,
        mut layout: mir2_core::login_ui::Layout,
    ) -> mir2_core::login_ui::Layout {
        let tree = self.editor_tree(EditorScope::Login);
        let viewport = EditorScope::Login.viewport();
        let rect = |id| {
            tree.resolved_rect(viewport, id)
                .expect("login widget exists")
        };
        let assign = |target: &mut mir2_core::login_ui::Rect, source: LRect| {
            *target = mir2_core::login_ui::Rect {
                x: source.x,
                y: source.y,
                w: source.w,
                h: source.h,
            };
        };
        assign(&mut layout.dialog, rect("login.panel"));
        assign(&mut layout.account, rect("login.account"));
        assign(&mut layout.password, rect("login.password"));
        assign(&mut layout.confirm, rect("login.confirm"));
        assign(&mut layout.ok, rect("login.ok"));
        assign(&mut layout.new, rect("login.new"));
        assign(&mut layout.chgpw, rect("login.chgpw"));
        assign(&mut layout.close, rect("login.close"));
        assign(&mut layout.signup_ok, rect("login.signup_ok"));
        assign(&mut layout.signup_cancel, rect("login.signup_cancel"));
        assign(&mut layout.msgbox, rect("login.msgbox"));
        assign(&mut layout.msg_ok, rect("login.msg_ok"));
        layout
    }

    /// 开门动画固定按帧选择；其位置来自一个独立的设计空间 widget。
    pub(crate) fn login_door(&self) -> LRect {
        self.get("login.door").expect("login door widget exists")
    }

    /// 选角背景、角色锚点/点击区、信息文字共享一份解析布局。
    pub(crate) fn select_layout(
        &self,
        mut layout: mir2_core::select_ui::Layout,
    ) -> mir2_core::select_ui::Layout {
        let tree = self.editor_tree(EditorScope::Select);
        let viewport = EditorScope::Select.viewport();
        let panel = tree
            .resolved_rect(viewport, "select.panel")
            .expect("select background widget exists");
        let (dx, dy) = (panel.x - layout.bg.0, panel.y - layout.bg.1);
        layout.bg = (panel.x, panel.y);
        for rect in &mut layout.sel {
            rect.x += dx;
            rect.y += dy;
        }
        for (index, id) in [
            "select.menu.start",
            "select.menu.create",
            "select.menu.delete",
            "select.menu.credits",
            "select.menu.exit",
        ]
        .into_iter()
        .enumerate()
        {
            let rect = tree
                .resolved_rect(viewport, id)
                .expect("select menu button widget exists");
            layout.menu[index] = mir2_core::select_ui::Rect {
                x: rect.x,
                y: rect.y,
                w: rect.w,
                h: rect.h,
            };
        }
        for (slot, id) in ["select.slot.left", "select.slot.right"]
            .into_iter()
            .enumerate()
        {
            let r = tree
                .resolved_rect(viewport, id)
                .expect("select slot widget exists");
            layout.slots[slot] = mir2_core::select_ui::Rect {
                x: r.x,
                y: r.y,
                w: r.w,
                h: r.h,
            };
        }
        for (slot, id) in ["select.info.left", "select.info.right"]
            .into_iter()
            .enumerate()
        {
            let r = tree
                .resolved_rect(viewport, id)
                .expect("select info widget exists");
            layout.slot_texts[slot] = mir2_core::select_ui::SlotText {
                name: (r.x, r.y),
                level: (r.x, r.y + 29.0),
                class: (r.x, r.y + 60.0),
            };
        }
        layout
    }

    pub(crate) fn select_dialog_box(&self) -> mir2_core::select_ui::DialogBox {
        use mir2_core::select_ui::{DialogBox, Rect};
        let mut dialog = DialogBox::build(crate::window::UI_WIN);
        let tree = self.editor_tree(EditorScope::Select);
        let viewport = EditorScope::Select.viewport();
        let rect = |id| {
            tree.resolved_rect(viewport, id)
                .expect("new-character widget exists")
        };
        let convert = |r: LRect| Rect {
            x: r.x,
            y: r.y,
            w: r.w,
            h: r.h,
        };
        dialog.frame = convert(rect("select.newchar"));
        dialog.name = convert(rect("select.newchar.name"));
        for (i, id) in [
            "select.newchar.job.0",
            "select.newchar.job.1",
            "select.newchar.job.2",
        ]
        .into_iter()
        .enumerate()
        {
            dialog.job[i] = convert(rect(id));
        }
        for (i, id) in ["select.newchar.sex.0", "select.newchar.sex.1"]
            .into_iter()
            .enumerate()
        {
            dialog.sex[i] = convert(rect(id));
        }
        dialog.ok = convert(rect("select.newchar.ok"));
        dialog.close = convert(rect("select.newchar.close"));
        dialog
    }

    pub(crate) fn select_msgbox(
        &self,
        mut msgbox: mir2_core::select_ui::MsgBox,
    ) -> mir2_core::select_ui::MsgBox {
        let tree = self.editor_tree(EditorScope::Select);
        let viewport = EditorScope::Select.viewport();
        let convert = |r: LRect| mir2_core::select_ui::Rect {
            x: r.x,
            y: r.y,
            w: r.w,
            h: r.h,
        };
        msgbox.frame = convert(tree.resolved_rect(viewport, "select.msgbox").unwrap());
        msgbox.ok = convert(tree.resolved_rect(viewport, "select.msg_ok").unwrap());
        msgbox
    }
}

fn scene_scope_for_id(id: &str) -> Option<EditorScope> {
    if id.starts_with("login.") {
        Some(EditorScope::Login)
    } else if id.starts_with("select.") {
        Some(EditorScope::Select)
    } else {
        None
    }
}

fn layout_viewport() -> LRect {
    LRect::new(
        0.0,
        0.0,
        crate::window::WIN_W as f32,
        crate::window::WIN_H as f32,
    )
}

/// 进程内**唯一**的当前布局。
///
/// ⚠️ 用全局而不是逐层传参：`draw_hud` 已经有 9 个参数，把布局再传一遍会把签名撑爆
/// （D-134 里记了这笔账）。编辑器改完更新它即刻生效。
static LAYOUT_DOCUMENT: std::sync::OnceLock<std::sync::RwLock<LayoutDocument>> =
    std::sync::OnceLock::new();

fn layout_document_lock() -> &'static std::sync::RwLock<LayoutDocument> {
    LAYOUT_DOCUMENT.get_or_init(|| std::sync::RwLock::new(LayoutDocument::default_layout()))
}

pub(crate) fn layout_document() -> LayoutDocument {
    layout_document_lock()
        .read()
        .map(|document| document.clone())
        .unwrap_or_else(|_| LayoutDocument::default_layout())
}

/// 改布局文档（编辑器/配置装载用）。
pub(crate) fn with_layout_document<R>(f: impl FnOnce(&mut LayoutDocument) -> R) -> R {
    match layout_document_lock().write() {
        Ok(mut g) => f(&mut g),
        Err(mut g) => f(g.get_mut()),
    }
}

#[cfg(test)]
pub(crate) static LAYOUT_TEST_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[cfg(test)]
mod layout_tests {
    use super::*;

    /// 用户 2026-10-12："调整框的大小和 Y 位置时，其 Y 位置没有变化；字 Y 应该在框的
    /// 居中位置" ⇒ 字的 Y 必须是**板的函数**（板动 ⇒ 字动、板内居中）。
    /// HUD 控件都应解析为 parent tree 中同一份画布矩形，且每个 child 坐标相对底栏存储。
    #[test]
    fn hud控件矩形与parent树解析一致() {
        let l = LayoutDocument::default_layout();
        let viewport = LRect::new(
            0.0,
            0.0,
            crate::window::WIN_W as f32,
            crate::window::WIN_H as f32,
        );
        let tree = l.widget_tree();
        let resolved = tree.resolve(viewport).unwrap();
        for node in tree.nodes() {
            let from_api = l
                .get(node.id)
                .unwrap_or_else(|| panic!("缺少布局节点 {}", node.id));
            let from_tree = resolved
                .iter()
                .find(|item| item.id == node.id)
                .unwrap()
                .rect;
            assert_eq!(from_api, from_tree, "{} 绘制/命中解析不一致", node.id);
        }
        assert_eq!(l.panel_rect().y, crate::window::WIN_H as f32 - HUD_BOARD_H);
        assert!(l.get("quick_right").is_some(), "所有登记节点必须可读写");
        let tree = l.widget_tree();
        assert_eq!(
            tree.node("orb").unwrap().edit,
            crate::widget::EditPolicy::MOVE_ONLY,
            "原生尺寸球只允许移动"
        );
        assert!(tree.node("plate_hp").unwrap().edit.resizable);
        for id in ["btn_state", "quick_row", "quick_right", "belt"] {
            let node = tree.node(id).unwrap();
            assert_eq!(
                node.edit,
                crate::widget::EditPolicy::LOCKED,
                "{id} 的正常视觉烘焙在底板"
            );
            assert_ne!(node.visual, crate::widget::VisualSource::Independent);
        }
    }

    #[test]
    fn 状态窗与对话窗根节点进入通用编辑树且保持固定尺寸() {
        let mut l = LayoutDocument::default_layout();
        l.set_align(
            "orb",
            crate::widget::Align {
                horizontal: crate::widget::AxisAlign::Center,
                vertical: crate::widget::AxisAlign::End,
            },
        );
        let tree = l.editor_tree(EditorScope::World);
        let resolved = tree.resolve(EditorScope::World.viewport()).unwrap();
        for id in layout_item_ids().iter().take(20).copied() {
            let from_api = l.get(id).unwrap();
            let from_tree = resolved.iter().find(|node| node.id == id).unwrap();
            assert_eq!(from_api, from_tree.rect, "{id} 绘制/编辑矩形同源");
        }
        for id in ["window_status", "window_dialog"] {
            let from_api = l.get(id).unwrap();
            let from_tree = resolved.iter().find(|node| node.id == id).unwrap();
            assert_eq!(from_api, from_tree.rect, "{id} 绘制/编辑矩形同源");
            assert_eq!(
                tree.node(id).unwrap().edit,
                crate::widget::EditPolicy::MOVE_ONLY
            );
            assert!(tree.node(id).unwrap().parent.is_none());
        }
        assert_eq!(tree.nodes().len(), 20);
        assert_eq!(
            tree.nodes()[..3]
                .iter()
                .map(|node| node.id)
                .collect::<Vec<_>>(),
            [HUD_PANEL_ID, "window_status", "window_dialog"]
        );
    }

    #[test]
    fn 登录石门与选角节点共享对应设计空间解析几何() {
        let layout = LayoutDocument::default_layout();
        for scope in [EditorScope::Login, EditorScope::Select] {
            let tree = layout.editor_tree(scope);
            let resolved = tree.resolve(scope.viewport()).unwrap();
            for node in tree.nodes() {
                assert_eq!(
                    layout.get(node.id),
                    resolved
                        .iter()
                        .find(|item| item.id == node.id)
                        .map(|item| item.rect),
                    "{} 在 {:?} 文档中绘制/命中/编辑必须同源",
                    node.id,
                    scope
                );
            }
        }
        let login = layout.login_layout(core_login_layout());
        assert_eq!(login.dialog, core_login_layout().dialog);
        assert_eq!(layout.login_door(), LRect::new(153.0, 96.0, 496.0, 361.0));
        let select = layout.select_layout(core_select_layout());
        assert_eq!(select.bg, (0.0, 0.0));
        assert_eq!(
            select.slots[0],
            mir2_core::select_ui::slot_rect(0, select.bg)
        );
        let tree = layout.editor_tree(EditorScope::Select);
        for (index, id) in [
            "select.menu.start",
            "select.menu.create",
            "select.menu.delete",
            "select.menu.credits",
            "select.menu.exit",
        ]
        .into_iter()
        .enumerate()
        {
            let rect = layout.get(id).expect("每个菜单项都是独立 widget");
            assert_eq!(
                (rect.x, rect.y, rect.w, rect.h),
                (
                    select.menu[index].x,
                    select.menu[index].y,
                    select.menu[index].w,
                    select.menu[index].h
                ),
                "菜单绘制高亮、输入命中、编辑框必须共用节点矩形"
            );
            assert_eq!(
                tree.node(id).unwrap().edit,
                crate::widget::EditPolicy::LOCKED,
                "常态烘焙在背景中的菜单禁止独立移动"
            );
            assert_eq!(
                tree.node(id).unwrap().visual,
                crate::widget::VisualSource::BackgroundBakedWithOverlay
            );
        }
    }

    #[test]
    fn 登录选角编辑文档覆盖所有已登记场景节点() {
        let layout = LayoutDocument::default_layout();
        for id in layout_item_ids().iter().copied() {
            assert!(layout.stored_rect(id).is_some(), "缺少布局节点存储 {id}");
            assert!(layout.align(id).is_some(), "缺少布局节点锚点 {id}");
        }
    }

    #[test]
    fn 单一布局文档跨作用域持有稀疏覆盖并由schema导出节点() {
        let mut document = LayoutDocument::default_layout();
        assert!(document.overrides.is_empty());
        let ids = layout_item_ids();
        let unique: std::collections::HashSet<_> = ids.iter().copied().collect();
        assert_eq!(unique.len(), ids.len(), "schema 导出的布局 ID 必须全局唯一");
        for scope in EDITOR_SCOPES {
            let tree = document.editor_tree(scope);
            tree.resolve(scope.viewport()).expect("默认布局树有效");
            for node in tree.nodes() {
                assert!(ids.contains(&node.id), "未注册布局 ID {}", node.id);
            }
        }

        for id in ["chat_box", "login.account", "select.info.left"] {
            let before = document.get(id).unwrap();
            document.set(
                id,
                LRect::new(before.x + 3.0, before.y + 2.0, before.w, before.h),
            );
        }
        assert_eq!(document.overrides.len(), 3, "多个场景共用同一个稀疏覆盖表");
        for id in ["chat_box", "login.account", "select.info.left"] {
            assert_eq!(
                document.get(id).unwrap().x,
                document
                    .editor_tree(scene_scope_for_id(id).unwrap_or(EditorScope::World))
                    .resolved_rect(
                        scene_scope_for_id(id)
                            .unwrap_or(EditorScope::World)
                            .viewport(),
                        id
                    )
                    .unwrap()
                    .x
            );
        }
    }

    #[test]
    fn 真素材尺寸与登录选角编辑默认指标一致() {
        let Some(dir) = mir2_core::paths::asset_dir() else {
            return;
        };
        for (lib, index, expected) in [
            ("Prguse", 60, (296, 254)),
            ("Prguse", 62, (76, 33)),
            ("Prguse", 61, (100, 32)),
            ("Prguse", 51, (96, 34)),
            ("Prguse", 52, (96, 33)),
            ("Prguse", 64, (16, 23)),
            ("Prguse", 53, (128, 33)),
            ("Prguse", 360, (452, 179)),
            ("Prguse", 363, (80, 34)),
            ("Prguse", 66, (76, 33)),
            ("Prguse", 67, (76, 33)),
            ("Prguse", 68, (44, 21)),
            ("Prguse", 69, (120, 21)),
            ("Prguse", 70, (120, 21)),
            ("Prguse", 71, (92, 20)),
            ("Prguse", 72, (56, 20)),
            ("ChrSel", 24, (496, 361)),
        ] {
            let lib_handle = mir2_core::wzl::Wzl::open(dir.join(lib))
                .unwrap_or_else(|error| panic!("打开 {lib}: {error}"));
            let record = lib_handle
                .record(index as usize)
                .unwrap_or_else(|| panic!("缺少 {lib}[{index}]"));
            assert_eq!(
                (record.width as u32, record.height as u32),
                expected,
                "{lib}[{index}] 的布局默认尺寸需与真实素材一致"
            );
        }
    }

    #[test]
    fn 数值文字在板内垂直居中() {
        let p = LRect::new(25.0, 700.0, 47.0, 14.0);
        assert_eq!(num_text_y_of(p), 701.0, "14 高的板、12 高的字 ⇒ 上边留 1");

        // 板往下挪 ⇒ 字跟着往下（同一个量）
        let down = LRect::new(25.0, 710.0, 47.0, 14.0);
        assert_eq!(num_text_y_of(down) - num_text_y_of(p), 10.0);

        // 板变高 ⇒ 字仍居中（不是贴着板顶）
        let tall = LRect::new(25.0, 700.0, 47.0, 30.0);
        assert_eq!(num_text_y_of(tall), 709.0);
    }

    /// 整排的格距统一由节点宽度公式推导；背景烘焙控件虽锁定宽度，计算口径仍由此测试守住。
    #[test]
    fn 整排节点宽度与格距公式保持一致() {
        let l = LayoutDocument::default_layout();
        // 缺省间距必须与老常量一致（否则一开编辑器界面就跳位）
        let quick_row = l.stored_rect("quick_row").unwrap();
        let quick_right = l.stored_rect("quick_right").unwrap();
        let belt = l.stored_rect("belt").unwrap();
        let skill = l.stored_rect("skill_bar").unwrap();
        assert!(
            (quick_step(quick_row, 5.0) - 30.0).abs() < 0.01,
            "快捷排 30"
        );
        assert!(
            (quick_step(quick_right, 2.0) - 30.0).abs() < 0.01,
            "注销/退出 30"
        );
        assert!((belt_step(belt) - 43.0).abs() < 0.01, "腰带 43");
        assert!((skill_step(skill) - 37.0).abs() < 0.01, "技能栏 37");

        // 把整排拉宽 20 ⇒ 间距增加 4（5 格之间有 5 段）。
        let wide = LRect::new(belt.x, belt.y, belt.w + 20.0, belt.h);
        assert!((belt_step(wide) - 47.0).abs() < 0.01, "5 段 ⇒ 每段 +4");
    }

    /// 缺省布局必须把字放在板里（不是飘在板上/下）。
    #[test]
    fn 缺省布局里字在板内() {
        let l = LayoutDocument::default_layout();
        for (plate, name) in [
            (l.stored_rect("plate_hp").unwrap(), "hp"),
            (l.stored_rect("plate_mp").unwrap(), "mp"),
        ] {
            let y = num_text_y_of(plate);
            assert!(
                y >= plate.y && y + NUM_TEXT_H <= plate.y + plate.h + 0.51,
                "{name} 的字（{y}..{}）超出板（{}..{}）",
                y + NUM_TEXT_H,
                plate.y,
                plate.y + plate.h
            );
        }
    }
}

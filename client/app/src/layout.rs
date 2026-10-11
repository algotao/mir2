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

/// 底部操作面板（`Prguse[1]`，800×251）。
pub(crate) const HUD_BOARD: u32 = 1;

/// 底部面板的**高度**（`Prguse[1]` 实测 800×251）—— 对话面板要压在它上方，
/// 而事件处理那一侧拿不到画图用的 `ui`/素材目录 ⇒ 这里给它一个常量（与图一致）。
// HUD 背板高度（`Prguse[1]` 是 800×251）。对话窗改官方版式后不按它排版了，
// 但它是**版式事实**（测试引用它），留着。
#[allow(dead_code)]
pub(crate) const HUD_BOARD_H: f32 = 251.0;

/// 血/魔法球（`Prguse[4]`，92×90；左半红=HP、右半蓝=MP）。
pub(crate) const HUD_ORB: u32 = 4;

/// 经验条 / 负重条（`Prguse[7]`，76×13）：两条**共用**这张图。
///
/// 协议 `Ability` 的 `exp/max_exp`、`weight/max_weight` 补齐后就能画了
///（服务端 `protocolAbility` 会一起下发，见 `hud.rs` 的 `draw_prop_bar`）。
pub(crate) const HUD_EXP: u32 = 7;

/// 等级数字的第一张（`Prguse[30..39]` = '0'..'9'，8px 一位）。
pub(crate) const HUD_DIGIT0: u32 = 30;

/// 球在**面板坐标**里的落点（原版 `(40, btop+91)`）。
pub(crate) const ORB_AT: (f32, f32) = (40.0, 91.0);

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
    pub fn rect(self) -> crate::space::CRect {
        let (lx, ly) = self.at();
        crate::space::canvas_rect(hud_local_x(lx), WIN_H as f32 - HUD_BOARD_H + ly, 24.0, 24.0)
    }
}

/// 面板中间那段的**横向拉伸比**（左右两段 1:1，只有中间被拉宽到窗口宽）。
pub(crate) fn hud_mid_scale() -> f32 {
    let src = HUD_BOARD_W - 2.0 * HUD_SIDE_W;
    let dst = (crate::window::WIN_W as f32 - 2.0 * HUD_SIDE_W).max(src);
    dst / src
}

/// 面板局部 x → 屏幕 x。
///
/// ⚠️ 面板是**三段贴**的（`hud.rs` 的 `draw_hud`）：左右两段 1:1、**中间那段拉宽**
/// ⇒ 局部 x 落在右段要换落点、落在中段要乘拉伸比，不然 1024 窗口下按钮/格子
/// 会跑到面板外（或错位到邻格）。
pub(crate) fn hud_local_x(local: f32) -> f32 {
    let right = HUD_BOARD_W - HUD_SIDE_W;
    if local >= right {
        hud_right_x() + (local - right)
    } else if local > HUD_SIDE_W {
        HUD_SIDE_W + (local - HUD_SIDE_W) * hud_mid_scale()
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

/// 第 `i` 格的命中框（**屏幕坐标**）。
///
/// ⚠️ 这 6 格落在面板**中间那段**（会被拉宽）⇒ 宽度也要跟着乘拉伸比。
pub(crate) fn belt_slot_rect(i: usize) -> crate::space::CRect {
    let s = hud_mid_scale();
    crate::space::canvas_rect(
        hud_local_x(BELT_AT.0 + i as f32 * BELT_STEP),
        crate::window::WIN_H as f32 - HUD_BOARD_H + BELT_AT.1,
        BELT_W * s,
        BELT_H,
    )
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
    pub fn rect(self) -> crate::space::CRect {
        let (lx, ly) = self.at();
        let (w, h) = self.size();
        crate::space::canvas_rect(hud_local_x(lx), WIN_H as f32 - HUD_BOARD_H + ly, w, h)
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
pub(crate) const HUD_ORB_SOLO: u32 = 5;
pub(crate) const HUD_ORB_SOLO_FILL: u32 = 6;
pub(crate) const ORB_SOLO_AT: (f32, f32) = (38.0, 90.0);

/// HP/MP 数值那一行（用户 2026-10-09 第 6 条："血量/魔量显示球体下，应显示数值"）。
///
/// ⚠️ 官方 1.76 的**底部面板**不画这两个数（只有人物状态窗口 `FState.pas:2866-2867` 画），
/// 这是按用户给的参考图加的一行：球体正下方，左 HP、右 MP。
pub(crate) const ORB_TEXT_DY: f32 = 92.0;

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
pub(crate) const CHAT_BOX_LOCAL: (f32, f32, f32, f32) = (207.0, 120.0, 388.0, 124.0);

/// 聊天区在**屏幕坐标**里的矩形（宽按底栏中段的拉伸比换算，见 `hud_local_x`）。
pub(crate) fn chat_box() -> (f32, f32, f32, f32) {
    let (lx, ly, lw, lh) = CHAT_BOX_LOCAL;
    let s = hud_mid_scale();
    (hud_local_x(lx), WIN_H as f32 - HUD_BOARD_H + ly, lw * s, lh)
}

/// 第 `i` 行（共 `shown` 行）的 y：**贴底** ⇒ 最后一行落在白框底边往上一行处。
pub(crate) fn chat_line_y(i: usize, shown: usize) -> f32 {
    let (_, y, _, h) = chat_box();
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
pub(crate) const BAG_BG: u32 = 3;
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
pub(crate) const BAG_CLOSE_IMG: u32 = 64;

/// 金币文字在窗口内的落点（下方那条宽横条里）。
///
/// ⚠️ 只画**数字**不画"金币"两个字（用户 2026-10-10 第 8 条：原版横条左侧是
/// 一枚金币图标 + 数字；我们的板子上图标已自带，再写"金币"就重复了）。
/// y 取 184：横条本体在 y≈181..200，原来写 186 加上"金币 "前缀后视觉偏下。
pub(crate) const BAG_GOLD_X: f32 = 56.0;
pub(crate) const BAG_GOLD_Y: f32 = 184.0;

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

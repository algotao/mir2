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

/// 聊天文字在屏幕坐标里的落点（原版 `(209, SCREENHEIGHT-128)`）与行距。
pub(crate) const CHAT_AT: (f32, f32) = (209.0, WIN_H as f32 - 128.0);

pub(crate) const CHAT_LINE_H: f32 = 13.0;

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
        (WIN_W as f32 - BAG_W - 8.0, 52.0)
    } else {
        (8.0, 4.0 + crate::input::DIALOG_H + 8.0)
    }
}

/// 第 `slot` 格（**页内下标**）在窗口内的矩形 `(x, y, w, h)`。
pub(crate) fn bag_cell_rect(slot: usize) -> (f32, f32, f32, f32) {
    let col = (slot % BAG_COLS) as f32;
    let row = (slot / BAG_COLS) as f32;
    (
        BAG_GRID_X + col * BAG_CELL_W,
        BAG_GRID_Y + row * BAG_CELL_H,
        BAG_CELL_W,
        BAG_CELL_H,
    )
}

/// 窗口内的一点落在哪一格（`None` = 没落在网格里）。与 [`bag_cell_rect`] 同源。
pub(crate) fn bag_slot_at(local: (f32, f32)) -> Option<usize> {
    for i in 0..BAG_PAGE_SLOTS {
        let (x, y, w, h) = bag_cell_rect(i);
        if local.0 >= x && local.0 < x + w && local.1 >= y && local.1 < y + h {
            return Some(i);
        }
    }
    None
}

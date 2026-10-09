//! 屏幕↔格子换算、相机、方向与补间落点（D-45：相机跟**渲染位置**）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::time::Instant;

use mir2_core::map::{UNIT_X, UNIT_Y};

use crate::actor::ActorAnim;
use crate::layout::{BAR_TOP, VIEW_H};
use crate::window::WIN_W;

/// 自己的**渲染位置**（浮点格）：有动画状态就走补间，否则就是服务端给的那一格。
///
/// ⚠️ 小地图/大地图取它、**不能取 `world.self_pos`**：后者只在**服务端确认到位**时才变
///（走一格要等一个来回）⇒ 图上是"到位才跳一格"（用户 2026-10-08 报的）。
/// 这里与画精灵用的是同一份补间（`ActorAnim::draw_pos`）⇒ 图上的移动和人的动作**同步**。
///
/// 抽成自由函数就是为了能单测（`Net` 不好在单测里造）。
pub(crate) fn self_render_pos(
    anim: Option<&ActorAnim>,
    to: (i32, i32),
    now: Instant,
) -> (f32, f32) {
    anim.map_or((to.0 as f32, to.1 as f32), |a| a.draw_pos(to, now))
}

/// 相机该在哪个**浮点格**：把自己的**渲染位置**摆到视口正中。
///
/// # 为什么必须是"渲染位置"而不是"服务端那一格"（用户 2026-10-08 报的走路/跑动手感）
///
/// 原版就是这么干的（`PlayScn.pas:1084-1097`）：
///
/// ```text
/// with Map.m_ClientRect do begin
///    Left := g_MySelf.m_nRx - 9;   // m_nRx = **渲染**坐标（补间后，不是 m_nCurrX）
///    Top  := g_MySelf.m_nRy - 9;
///    Right := g_MySelf.m_nRx + 9;  Bottom := g_MySelf.m_nRy + 8;
/// end;
/// Map.UpdateMapPos (g_MySelf.m_nRx, g_MySelf.m_nRy);
/// ```
///
/// ⇒ 表现是"**人物钉在屏幕中间不动，地图往前卷**"。相机若取整格，就变成
/// "人在视口里一格格往前蹭、蹭满一格镜头再猛跳一下" —— 完全不是那个手感。
///
/// ⚠️ x 用**浮点**除（`WIN_W/UNIT_X/2` = 8.33 格 ⇒ 人正落在 400px 中央）；用整数除
/// （8 格 = 384px）会让人偏左 16px。
pub(crate) fn follow_cam(render: (f32, f32)) -> (f32, f32) {
    (
        render.0 - (WIN_W as f32 / UNIT_X as f32) / 2.0,
        render.1 - (VIEW_H / UNIT_Y as f32) / 2.0,
    )
}

/// **屏幕坐标 → 地图格**（鼠标点哪走到哪要用它；与 [`cell_to_screen`] 互为逆）。
/// ⚠️ 必须是 `(cam + px/UNIT).floor()`，**不是** `cam.floor() + (px/UNIT).floor()`：
/// 相机是浮点格（走路时一直在动），拆开取整会在小数处差**一格** ——
/// "点哪走哪"就会偏（`屏幕与格子互为逆` 这条单测就是抓它的）。
pub(crate) fn screen_to_cell(cam: (f32, f32), px: f32, py: f32) -> (i32, i32) {
    (
        (cam.0 + px / UNIT_X as f32).floor() as i32,
        (cam.1 + (py - BAR_TOP) / UNIT_Y as f32).floor() as i32,
    )
}

/// 从 `from` 格朝 `to` 格的方向（协议值 = 原版 + 1，顺时针：1 上、2 右上 … 8 左上）。
///
/// 照原版 `GetNextDirection`（`ClFunc.pas:398-422`）：先把 Δ 取**三态符号**，再映到 8 方向；
/// **同一格**给 `None`（没有方向可言 —— 调用方据此判"到了"）。
pub(crate) fn dir_to(from: (i32, i32), to: (i32, i32)) -> Option<mir2_protocol::Direction> {
    use mir2_protocol::Direction as D;
    let (dx, dy) = ((to.0 - from.0).signum(), (to.1 - from.1).signum());
    Some(match (dx, dy) {
        (0, -1) => D::DirUp,
        (1, -1) => D::DirUpRight,
        (1, 0) => D::DirRight,
        (1, 1) => D::DirDownRight,
        (0, 1) => D::DirDown,
        (-1, 1) => D::DirDownLeft,
        (-1, 0) => D::DirLeft,
        (-1, -1) => D::DirUpLeft,
        _ => return None,
    })
}

/// 把方向转 45°：`steps = -1` 逆时针、`+1` 顺时针（原版 `PrivDir` / `NextDir`，
/// 见 `ClMain.pas:1911-1920` 的绕障）。
pub(crate) fn turn45(d: mir2_protocol::Direction, steps: i32) -> mir2_protocol::Direction {
    use mir2_protocol::Direction as D;
    const RING: [D; 8] = [
        D::DirUp,
        D::DirUpRight,
        D::DirRight,
        D::DirDownRight,
        D::DirDown,
        D::DirDownLeft,
        D::DirLeft,
        D::DirUpLeft,
    ];
    let idx = RING.iter().position(|&x| x == d).unwrap_or(0) as i32;
    RING[(idx + steps).rem_euclid(8) as usize]
}

/// 协议朝向（1..8）→ 屏幕增量。**1 = 上**（新枚举 = 原版 + 1，见 common.proto）。
///
/// ⚠️ 这张表与服务端 `entity.DirDelta` 是同一份顺序（原版 0..7 各 +1）——
/// 两处一旦不一致，人物会朝反方向走，而且不会报错。
/// 格子坐标 → 视口坐标（地图绘制用的同一套换算：`UNIT_X/UNIT_Y` + 顶部信息条）。
pub(crate) fn cell_to_screen(cam: (f32, f32), cx: i32, cy: i32) -> (f32, f32) {
    cell_to_screen_f(cam, cx as f32, cy as f32)
}

/// 同上，但两边都允许**小数**：相机是浮点格（见 [`follow_cam`]），格子也可能是
/// 补间到一半的（见 [`ActorAnim::draw_pos`]）。
pub(crate) fn cell_to_screen_f(cam: (f32, f32), cx: f32, cy: f32) -> (f32, f32) {
    (
        (cx - cam.0) * UNIT_X as f32,
        BAR_TOP + (cy - cam.1) * UNIT_Y as f32,
    )
}

/// 相机拆成"**整格 + 亚格像素**"。
///
/// 地图那条路（`Map::visible_tiles`）只认**整格**相机，它算出来的图块落点是"格 × UNIT"
/// 的整数像素 ⇒ 亚格那半格由绘制时减去 [`CamParts::sub`] 补上（见 `draw_tile`）。
/// 这样地图就能**逐帧平滑卷动**，而不是"攒够一格再猛跳一下"。
///
/// 别改成"把小数扔掉"：那就退回"人在视口里一格格蹭"的老样子了（用户 2026-10-08 报的）。
pub(crate) fn cam_parts(cam: (f32, f32)) -> CamParts {
    let cell = (cam.0.floor() as i32, cam.1.floor() as i32);
    CamParts {
        cell,
        sub: (
            (cam.0 - cell.0 as f32) * UNIT_X as f32,
            (cam.1 - cell.1 as f32) * UNIT_Y as f32,
        ),
    }
}

/// [`cam_parts`] 的结果：整格相机 + 要减掉的亚格像素。
pub(crate) struct CamParts {
    /// 整格相机（`visible_tiles` 用）。
    pub(crate) cell: (i32, i32),
    /// 亚格像素偏移：所有"以整格相机算出来"的落点都要减掉它。
    pub(crate) sub: (f32, f32),
}

pub(crate) fn dir_delta(dir: i32) -> (f32, f32) {
    match dir {
        1 => (0.0, -1.0),  // 上
        2 => (1.0, -1.0),  // 右上
        3 => (1.0, 0.0),   // 右
        4 => (1.0, 1.0),   // 右下
        5 => (0.0, 1.0),   // 下
        6 => (-1.0, 1.0),  // 左下
        7 => (-1.0, 0.0),  // 左
        8 => (-1.0, -1.0), // 左上
        _ => (0.0, 0.0),   // 未指定
    }
}

// ---------- actor 精灵 ----------
//
// 图号公式在 `mir2_core::actor`（原版 Delphi 的逐条翻译）。这里只做三件事：
// 取纹理、**按锚点定位**、按时间推进帧。
//
// ⚠️ 落点公式（原版 `PlayScn.pas:1236` 把**格子左上角**交给 actor，再由 `Actor.pas`
// 里的 `dx + m_nPx, dy + m_nPy` 落笔）：
//
//     精灵左上角 = 格子左上角 + 图自带的锚点
//
// 锚点**常是负的**（`Hum#0` 是 (8,-48)）：71 像素高的人站在 32 像素的格子上，
// 脑袋当然得画到格子上面去。别改成"底边对齐格底" —— 那样每一帧都对不上。

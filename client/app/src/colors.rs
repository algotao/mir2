//! 全套调色常量（画布坐标那套；界面素材的配色在 `ui`/`login`/`select` 里）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use sdl3::pixels::Color;

pub(crate) const C_BG: Color = Color::RGB(10, 14, 28);

pub(crate) const C_PANEL: Color = Color::RGB(22, 30, 56);

pub(crate) const C_PANEL_BORDER: Color = Color::RGB(90, 120, 170);

pub(crate) const C_TITLE: Color = Color::RGB(232, 200, 96);

pub(crate) const C_TEXT: Color = Color::RGB(206, 212, 226);

pub(crate) const C_DIM: Color = Color::RGB(120, 132, 156);

pub(crate) const C_FIELD: Color = Color::RGB(8, 10, 20);

pub(crate) const C_CHECKER_A: Color = Color::RGB(34, 38, 52);

pub(crate) const C_CHECKER_B: Color = Color::RGB(26, 30, 42);

pub(crate) const C_OK: Color = Color::RGB(120, 220, 150);

pub(crate) const C_ERR: Color = Color::RGB(232, 120, 120);
// 调试叠加层

pub(crate) const C_GRID: Color = Color::RGB(40, 48, 70);

pub(crate) const C_GRID_GROUND: Color = Color::RGB(70, 110, 200);

pub(crate) const C_GRID_MID: Color = Color::RGB(60, 170, 170);

pub(crate) const C_GRID_FRONT: Color = Color::RGB(210, 140, 60);

pub(crate) const C_CELLBASE: Color = Color::RGB(255, 220, 80);

pub(crate) const C_CROSS: Color = Color::RGB(255, 255, 255);

/// 鼠标下那张图**自己那一格**的高亮色（与鼠标格区分开）
pub(crate) const C_TOPMOST: Color = Color::RGB(255, 90, 220);

/// 联网实体标记的配色（按 `EntityState.kind`：0=玩家 1=怪物 2=NPC）。
// 名字颜色 —— **照用户 2026-10-09 给的规则**（出处见 docs/decisions.md D-69）：
//   玩家 = 白名（红名时换 `C_RED_NAME`）；NPC = **绿名**；怪 = **不显示名字**（只血条）。
pub(crate) const C_ENT_PLAYER: Color = Color::RGB(255, 255, 255);

pub(crate) const C_ENT_MONSTER: Color = Color::RGB(255, 110, 110);

pub(crate) const C_ENT_NPC: Color = Color::RGB(0, 255, 0);
/// **红名**玩家的名字颜色（PK 值到红名档，服务端用 `status_bits` 的
/// `mir2_core::world::STATE_RED_NAME` 位告诉我们）。
pub(crate) const C_RED_NAME: Color = Color::RGB(255, 64, 64);

/// 自己（相机跟着它）。
pub(crate) const C_ENT_SELF: Color = Color::RGB(255, 255, 255);

/// 尸体（`Death` 之后、`EntityDisappear` 之前 —— 原版里尸骨会留一会儿）。
pub(crate) const C_ENT_DEAD: Color = Color::RGB(120, 120, 120);

/// **锁定的攻击目标**（左键点怪锁住的那个）—— 名字换成这个颜色，一眼看得出在打谁。
///
/// 原版是用光标/血条高亮标的（`ClMain.pas` 的 `g_TargetCret` + 光标）；我们先用名字色，
/// 省一套贴图（本套素材里也没有"目标框"那种图）。
pub(crate) const C_ENT_TARGET: Color = Color::RGB(255, 236, 140);

/// 鼠标**悬停**那个实体的名字色（比锁定目标再亮一档 —— 两个状态同时出现时要分得出）。
///
/// 照原版：悬停是 `g_FocusCret` + 身体**再画一遍**（`PlayScn.pas:1369-1376`）；
/// Crystal 是 `MouseObject.DrawName()` + `DrawBlend()`（`GameScene.cs:10605 / 10973`）。
pub(crate) const C_HOVER_NAME: Color = Color::RGB(255, 255, 210);

/// HUD 聊天区的配色：系统消息 / 坏消息（原版 `ChatStrs` 每行自带一色，我们只用三档）。
pub(crate) const C_CHAT_SYS: Color = Color::RGB(230, 230, 210);

pub(crate) const C_CHAT_BAD: Color = Color::RGB(255, 120, 120);

/// HUD 里"地图名 + 坐标"那行（左下角）。
pub(crate) const C_HUD_COORD: Color = Color::RGB(255, 236, 180);

/// 伤害飘字的三档亮度（8x8 调试字体只有一档颜色 ⇒ 用亮度代替透明度淡出）。
pub(crate) const C_DMG_HOT: Color = Color::RGB(255, 240, 120);

pub(crate) const C_DMG_MID: Color = Color::RGB(255, 170, 60);

pub(crate) const C_DMG_DIM: Color = Color::RGB(190, 90, 40);

// 图层可见性掩码定义在 core（`map::LAYERS_ALL` / `Layer::bit`）——
// app 与 e2e 都要用它过滤绘制指令，各写一份迟早不一致（plan §4.2 / R-10）。
// 语义：bit0 = 地表、bit1 = 中间、bit2 = 前景。

// ---------- 调试功能的开关（**关掉，不是删掉**）----------
//
// 这两项是当初为**排查贴图/错位问题**做的：一个按层拆开看、一个把格网与
// 各层落点框（辅助线 + 格子坐标）叠在画面上。日常游玩时它们只会碍事
//（遮挡画面、还容易手滑把某一层关掉而以为是渲染坏了）。
//
// ⇒ **默认关闭**，代码一行不动地留着。要调试时把下面改成 `true` 重建即可
//（改这一处就够了：按键、绘制、提示条三处都跟着它走 —— 别去各处注释代码）。

/// 自己在缩略图上的标记色（原版是把那个像素直接写 255，`PlayScn.pas:790+32`）。
pub(crate) const C_SELF_DOT: Color = Color::RGB(255, 255, 255);

/// 悬停高亮那一遍的**颜色 + 透明度**。
///
/// # 为什么不照抄原版"原样再画一遍半透明"
///
/// 原版（`PlayScn.pas:1369-1376`）与 Crystal（`GameScene.cs:10973`）都是**把同一张图
/// 再画一遍**（原版 `DrawBlend` 走 `Color256Mix` 表、Crystal `SetBlend(true, 0.3F)`）。
/// 但那个做法对我们**不产生任何视觉变化**：`dst = src*a + dst*(1-a)`，而 dst 本来就是
/// 第一遍画上去的 src ⇒ `dst` 不变（只有贴图的半透明边缘会变实一点）。
///
/// 所以这里用**暖色 + 半透明**：机制仍是"再画一遍"（能与别处对齐），但反馈看得见。
/// ⚠️ 颜色是**粘在贴图上**的（贴图是缓存共用的）⇒ 每次 `copy` 前都要显式设一遍。
pub(crate) const HOVER_TINT: (u8, u8, u8, u8) = (255, 232, 150, 140);

//! 窗口尺寸 / 逻辑呈现（D-50、D-52 那条"装得下就等比、装不下就 1:1 裁掉"就在这）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use sdl3::render::WindowCanvas;

/// 界面**设计尺寸**（原版素材与版式都是按 800×600 做的：登录背景、选角面板、地图视口…）。
///
/// # ⚠️ 现在它**就是**窗口大小（1024×768），画面 1:1，不再缩放（2026-10-09 改）
///
/// 之前是 800×600 的设计空间 + SDL 逻辑呈现放大到 1024×768（×1.28）—— 用户一眼看出
/// 来了：「感觉被拉伸了？画质比原版更粗糙，且图形更大」。**放大 1.28 倍**正是那个观感：
/// 像素被抽成 1.28 倍、又不是整数倍 ⇒ 又糊又"大」（原版 1024×768 是真按 1024 画的：
/// 视口 ±12 格、界面素材居中不缩放 —— `PlayScn.pas` 的 `SWH1024` 分支、以及登录界面
/// `(SCREENWIDTH-800) div 2` 那句）。
///
/// 于是：**世界按 1024×768 原生渲染**（视口更大更清楚），登录/选角那套 800×600 素材
/// 由各自的 `Layout::build(win, ...)` **居中**摆（它们本来就是这么写的，见 D-27/D-42）。
///
/// 窗口仍可拉大拉小（`.resizable()`）；那时才轮到 SDL 的逻辑呈现去等比缩放 + 留边。
/// 鼠标事件用 `Event::get_converted_coords` 换算回这个空间。
pub(crate) const WIN_W: u32 = 1024;

pub(crate) const WIN_H: u32 = 768;

/// 登录/选角那两屏的**设计空间**（素材原生 800×600）。
///
/// `login_ui::Layout` / `select_ui::Layout` 都在这个空间里算（它们只认"窗口"尺寸），
/// 画到 [`WIN_W`]×[`WIN_H`] 的画布上时由 [`ui::UI_SCALE`] 整体乘 1.28 —— 也就是
/// **整屏拉伸铺满**，和官方客户端一样（见 `ui::UI_SCALE` 的说明与 D-52）。
///
/// ⚠️ 所以那两屏的**鼠标坐标**也得先除回这个空间（`ui::ui_inv_pt`），
/// 而世界那套（`screen_to_cell` 等）用的是画布坐标，**不要**混。
pub(crate) const UI_WIN: (u32, u32) = (800, 600);

/// 启动时的窗口大小（用户随后可以随意拉大拉小；改这里**不影响版式**，见 [`WIN_W`]）。
///
/// ⚠️ 别设得比设计尺寸还小：逻辑呈现只保证"完整可见 + 不变形"，不保证 1:1 以上
/// —— 比 800×600 小就会把界面**缩小**（糊）。这条**编译期**就钉住。
///
/// ⚠️ 窗口与设计尺寸**不需要**同比例：`LETTERBOX` 会等比缩放并在多出来的那一边留黑边
///（比例不等只意味着有黑边，不会变形）。
pub(crate) const WINDOW_W: u32 = 1024;

pub(crate) const WINDOW_H: u32 = 768;

const _: () = assert!(
    WINDOW_W >= WIN_W && WINDOW_H >= WIN_H,
    "默认窗口比设计尺寸小 ⇒ 界面会被缩小"
);

/// 启动窗口大小：`MIR2_WINDOW=<宽>x<高>`（例 `1280x960`；`docs/use.md` 的三档是
/// 800×600 / 1024×768 / 1280×960）。填错或比设计尺寸小就忽略并退回默认值。
///
/// ⚠️ 这三档**不是三个不同的版式** —— 版式永远活在 800×600 里，改的只是"铺到多大"。
/// 窗口本身也可拉（`.resizable()`）。
pub(crate) fn window_size() -> (u32, u32) {
    let Ok(s) = std::env::var("MIR2_WINDOW") else {
        return (WINDOW_W, WINDOW_H);
    };
    match parse_window(&s) {
        Some((w, h)) => {
            println!("[mir2-app] 窗口 = {w}×{h}（MIR2_WINDOW）");
            (w, h)
        }
        None => {
            println!(
                "[mir2-app] MIR2_WINDOW=\"{s}\" 不认（要 <宽>x<高> 且不小于 800×600），用默认"
            );
            (WINDOW_W, WINDOW_H)
        }
    }
}

/// `MIR2_WINDOW` 的解析（纯函数，便于单测）：`"1280x960" → Some((1280,960))`；
/// 格式不对、或比设计尺寸小 ⇒ `None`（宁可退回默认，也不把界面缩小）。
pub(crate) fn parse_window(s: &str) -> Option<(u32, u32)> {
    let (w, h) = s.split_once(['x', 'X'])?;
    let (w, h): (u32, u32) = (w.trim().parse().ok()?, h.trim().parse().ok()?);
    (w >= WIN_W && h >= WIN_H).then_some((w, h))
}

/// 主显示器上**真正能放窗口**的那块（去掉菜单栏 / 任务栏 / Dock）。拿不到 ⇒ `None`。
///
/// 存在理由：`1024×768` 的屏上，标题栏一占，客户区就放不下 1024×768 了 ——
/// 那时要么缩画面（糊）、要么裁边（见 [`apply_presentation`]）。
pub(crate) fn usable_bounds(video: &sdl3::VideoSubsystem) -> Option<(u32, u32)> {
    let d = video.get_primary_display().ok()?;
    let r = d.get_usable_bounds().ok()?;
    Some((r.width(), r.height()))
}

/// 把想要的窗口尺寸夹进可用区域（纯函数，便于单测）。
pub(crate) fn fit_window(want: (u32, u32), usable: (u32, u32)) -> (u32, u32) {
    (want.0.min(usable.0), want.1.min(usable.1))
}

/// 挑呈现模式（纯函数，便于单测）：窗口装得下 [`WIN_W`]×[`WIN_H`] 才允许等比缩放。
pub(crate) fn present_mode(win: (u32, u32)) -> sdl3_sys::render::SDL_RendererLogicalPresentation {
    if win.0 >= WIN_W && win.1 >= WIN_H {
        sdl3_sys::render::SDL_LOGICAL_PRESENTATION_LETTERBOX
    } else {
        sdl3_sys::render::SDL_LOGICAL_PRESENTATION_DISABLED
    }
}

/// 呈现策略（用户 2026-10-09 第 4 条）：**装得下就等比放大，装不下就 1:1 裁切，
/// 绝不缩小、绝不拉伸**。
///
/// - 窗口 ≥ 1024×768 ⇒ `LETTERBOX`：等比放大 + 留边（窗口更大时画面跟着变大，不变形）；
/// - 窗口 < 1024×768（例：1024×768 的屏 + 标题栏/菜单栏 ⇒ 客户区只有 1024×743）
///   ⇒ `DISABLED`：**1:1 画**，右边/下边多出去的那点直接裁掉。
///   注意 `LETTERBOX` 在这种情形下**会连画面一起缩**（1024×743 ⇒ 0.967 倍）——
///   整屏糊一档，而在它和"裁掉最外圈的石头边框"之间，用户明确要后者。
///
/// ⚠️ 两种模式下**鼠标换算都不用自己写**：SDL 的 `Event::get_converted_coords`
/// 认的正是这里的口径（缩放 / 留边 / 裁切它都管）。
pub(crate) fn apply_presentation(canvas: &mut WindowCanvas) -> Result<(), String> {
    let (w, h) = canvas.output_size().map_err(|e| e.to_string())?;
    canvas
        .set_logical_size(WIN_W, WIN_H, present_mode((w, h)))
        .map_err(|e| format!("设置逻辑呈现失败: {e}"))
}

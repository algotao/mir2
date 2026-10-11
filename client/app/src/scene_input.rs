//! 登录与选角两屏的**鼠标边界适配**（架构方向 ⑥）。
//!
//! `login_ui` / `select_ui` 的布局与命中使用 800×600 设计空间；通用控件引擎
//! 只接收 `CPt/CRect` 画布空间。转换集中在这里，避免四个 MouseDown/MouseUp
//! 分支各自偷偷换坐标（D-87/D-89）。

use std::path::PathBuf;

use crate::app_state::AppState;
use crate::ui::UiCache;
use crate::{login, select, ui};
fn design_xy(canvas_xy: (f32, f32)) -> (f32, f32) {
    let p = ui::ui_inv_pt(canvas_xy);
    (p.0 .0, p.1 .0)
}

/// 登录/建号登录面板按下：布局命中用设计坐标；`Login` 内部将按钮矩形转成 `CRect`。
pub(crate) fn login_down(
    app: &mut AppState,
    ui_cache: &mut UiCache<'_>,
    asset_dir: &Option<PathBuf>,
    canvas_xy: (f32, f32),
) {
    let Some(dir) = asset_dir.as_ref() else {
        return;
    };
    let p = design_xy(canvas_xy);
    let layout = mir2_core::login_ui::Layout::build(crate::window::UI_WIN, |lib, idx| {
        ui_cache.size(dir, lib, idx)
    });
    if let Some(layout) = layout {
        app.login.on_down(p, &layout, &mut app.ui_click);
    }
}

/// 登录面板松开：控件引擎用原始画布点判定 OnClick，返回语义动作。
pub(crate) fn login_up(
    app: &mut AppState,
    ui_cache: &mut UiCache<'_>,
    asset_dir: &Option<PathBuf>,
    canvas_xy: (f32, f32),
) -> login::Action {
    let Some(dir) = asset_dir.as_ref() else {
        return login::Action::None;
    };
    let canvas_p = crate::widget::canvas_pt(canvas_xy);
    let layout = mir2_core::login_ui::Layout::build(crate::window::UI_WIN, |lib, idx| {
        ui_cache.size(dir, lib, idx)
    });
    let Some(layout) = layout else {
        return login::Action::None;
    };
    match app.ui_click.release(canvas_p) {
        Some(crate::Press::Login(button)) => app.login.click(button, &layout),
        _ => login::Action::None,
    }
}

/// 选角/建号面板按下：设计坐标命中，按钮矩形在 `Select` 内显式转 `CRect`。
pub(crate) fn select_down(
    app: &mut AppState,
    ui_cache: &mut UiCache<'_>,
    asset_dir: &Option<PathBuf>,
    canvas_xy: (f32, f32),
) {
    let (Some(dir), Some(scene)) = (asset_dir.as_ref(), app.select_scene.as_mut()) else {
        return;
    };
    let p = design_xy(canvas_xy);
    let msg = select::view::msgbox_geom(ui_cache, dir, crate::window::UI_WIN);
    if let Some(layout) = mir2_core::select_ui::Layout::build(crate::window::UI_WIN, |lib, idx| {
        ui_cache.size(dir, lib, idx)
    }) {
        scene.on_down(p, &layout, msg, &mut app.ui_click);
    }
}

/// 选角/建号面板松开：`Ui` 只用画布点；core 的语义处理继续使用设计布局。
pub(crate) fn select_up(
    app: &mut AppState,
    ui_cache: &mut UiCache<'_>,
    asset_dir: &Option<PathBuf>,
    canvas_xy: (f32, f32),
) -> select::Action {
    let (Some(dir), Some(scene)) = (asset_dir.as_ref(), app.select_scene.as_mut()) else {
        return select::Action::None;
    };
    let key = app.ui_click.release(crate::widget::canvas_pt(canvas_xy));
    let Some(layout) = mir2_core::select_ui::Layout::build(crate::window::UI_WIN, |lib, idx| {
        ui_cache.size(dir, lib, idx)
    }) else {
        return select::Action::None;
    };
    match key {
        Some(crate::Press::Select(hit)) => scene.click_select(hit, &layout),
        Some(crate::Press::NewChar(hit)) => scene.click_newchar(hit),
        // 没按在任何控件上 ⇒ 弹窗（模态）就此关掉；建角框点外面不关（见状态机）。
        _ => scene.click_outside(),
    }
}

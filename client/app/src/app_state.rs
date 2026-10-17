//! **客户端运行态的单一所有者**（架构方向 ⑥）。
//!
//! `main()` 原本在 SDL 初始化后声明了几十个 `let mut`，然后让它们横跨事件处理、
//! 网络推进和渲染三大段。它们其实都属于同一个客户端会话；放进 `AppState` 后，
//! `main` 里保留资源创建与循环编排，运行状态则有明确归属。
//!
//! ⚠️ SDL/纹理/画布等有平台句柄或复杂生命周期的资源仍由 `main` 管理；这里放的是
//! **可变运行态**（场景、窗口、世界交互、地图状态、开关）。之后再把事件/渲染阶段
//! 接到 `AppState` 的方法上，避免一次搬动所有 SDL 生命周期。

use std::time::Instant;

use crate::{
    assetview_state, bagdrag, confirm, login, pager, qty, scrollview, select, shopui, windows,
    world_control, Ui,
};
use mir2_core::map::Map;

/// 当前客户端场景（替代裸 `u8` 模式号）。
///
/// ⚠️ 输入分发和状态转换用这个枚举；渲染只读取它，不再从绘制函数里改场景。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Scene {
    Login,
    World,
    AssetView,
    Select,
}

/// 登录/选角状态机接受的**平台无关按键**。
///
/// SDL `Keycode` 只在 keyboard 适配层映射成它；表单状态机和测试不依赖 SDL。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SceneKey {
    Tab,
    Backspace,
    Enter,
    Escape,
    Left,
    Right,
    Up,
    Down,
    Space,
    Other,
}

/// 可变客户端会话状态（SDL 资源不在此处）。
pub(crate) struct AppState {
    // Scene/session
    pub login: login::Login,
    pub select_scene: Option<select::Select>,
    pub scene: Scene,
    pub net: Option<crate::net::Net>,
    /// 供登录/资产观察器与开发诊断显示的状态文字。
    pub status: String,
    /// F3 素材观察器的纯选择状态（图库/图号；WZL 与纹理资源不在此处）。
    pub asset_view: assetview_state::AssetViewState,

    // World interaction state machine (移动/追打/挖肉/拾取)
    pub world_control: world_control::WorldControl,
    /// 进世界时是否已经把开发键位提示推到聊天区。
    pub hint_pushed: bool,

    // Overlay/UI interaction
    pub win: windows::Windows,
    pub bag_page: pager::Pager,
    pub status_page: pager::Pager,
    /// **技能页**翻到第几页（技能多于 5 条时；翻页键在技能页翻它）。
    pub magic_page: pager::Pager,
    /// 技能窗里**选中**的那条的 `skill_id`（选中后按 F1..F8 = 设快捷键）。
    pub magic_selected: Option<u32>,
    pub shop_seen_seq: u64,
    pub shop_ui: shopui::ShopUi,
    /// 商店「买几个 / 卖几个」面板开着时的状态（`None` = 没开）。
    pub qty: Option<qty::Qty>,
    pub ui_click: Ui,
    pub drag: bagdrag::BagDrag,
    pub dlg_scroll: scrollview::ScrollView,
    pub dialog_seq_seen: u64,
    pub confirm: confirm::Confirm,
    pub belt: [Option<crate::layout::BeltSlot>; crate::layout::BELT_SLOTS],
    /// F-Ctrl+L **界面开发器**的运行态（D-134）。
    pub layout_edit: crate::layoutedit::LayoutEdit,
    pub minimap_on: bool,
    pub bigmap_on: bool,
    pub sfx_on: bool,
    pub music_on: bool,

    // Map state (map renderer/cache objects remain with main's SDL resources)
    pub map_i: usize,
    pub map: Option<Map>,
    pub map_err: String,
    pub cam: (f32, f32),
    pub debug: bool,
    pub layers: u8,
    pub mouse: (f32, f32),

    // Timers / ambient effects
    pub sfx_rng: u32,
    pub last_monster_sfx: Instant,
    pub started: Instant,
}

impl AppState {
    pub(crate) fn new() -> Self {
        let now = Instant::now();
        Self {
            login: login::Login::new(),
            select_scene: None,
            scene: Scene::Login,
            net: None,
            status: String::from("READY"),
            asset_view: assetview_state::AssetViewState::default(),

            world_control: world_control::WorldControl::new(now),
            hint_pushed: false,

            win: windows::Windows::new(),
            bag_page: pager::Pager::new(),
            status_page: pager::Pager::new(),
            magic_page: pager::Pager::new(),
            magic_selected: None,
            shop_seen_seq: 0,
            shop_ui: shopui::ShopUi::new(),
            qty: None,
            ui_click: Ui::new(),
            drag: bagdrag::BagDrag::new(),
            dlg_scroll: scrollview::ScrollView::new(),
            dialog_seq_seen: 0,
            confirm: confirm::Confirm::new(),
            belt: [None; crate::layout::BELT_SLOTS],
            layout_edit: crate::layoutedit::LayoutEdit::default(),
            minimap_on: false,
            bigmap_on: false,
            sfx_on: true,
            music_on: true,

            map_i: 0,
            map: None,
            map_err: String::new(),
            cam: (0.0, 0.0),
            debug: false,
            layers: mir2_core::map::LAYERS_ALL,
            mouse: (0.0, 0.0),

            sfx_rng: 0x1234_5678,
            last_monster_sfx: now,
            started: now,
        }
    }

    pub(crate) fn begin_frame(&mut self) {
        // 所有动画/移动/开门逻辑共用每帧时钟；严禁只在构造时赋一次（会冻住）。
        self.started = Instant::now();
    }
}

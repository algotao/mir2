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

use sdl3::mouse::MouseButton;

use crate::{bagdrag, confirm, login, pager, scrollview, select, shopui, windows, Ui};
use mir2_core::map::Map;

/// 可变客户端会话状态（SDL 资源不在此处）。
pub(crate) struct AppState {
    // Scene/session
    pub login: login::Login,
    pub select_scene: Option<select::Select>,
    pub mode: u8,
    pub net: Option<crate::net::Net>,

    // World interaction
    pub move_target: Option<(i32, i32, bool)>,
    pub move_at: Instant,
    pub press_at: Option<Instant>,
    pub press_cell: (i32, i32),
    pub held_move: Option<MouseButton>,
    pub retarget_at: Instant,
    pub last_move_fail: u64,
    pub detour: Option<u8>,
    pub last_self_pos: (i32, i32),
    pub move_block_until: Instant,
    pub hover: Option<u64>,
    pub hint_pushed: bool,
    pub alt_down: bool,
    pub butch_target: Option<u64>,
    pub butch_next: Instant,
    pub pending_pickup: Option<(u64, (i32, i32))>,
    pub combat_target: Option<u64>,
    pub attack_at: Instant,
    pub last_foot_frame: Option<u16>,

    // Overlay/UI interaction
    pub win: windows::Windows,
    pub bag_page: pager::Pager,
    pub status_page: pager::Pager,
    pub shop_seen_seq: u64,
    pub shop_ui: shopui::ShopUi,
    pub ui_click: Ui,
    pub drag: bagdrag::BagDrag,
    pub dlg_scroll: scrollview::ScrollView,
    pub dialog_seq_seen: u64,
    pub confirm: confirm::Confirm,
    pub belt: [Option<crate::layout::BeltSlot>; crate::layout::BELT_SLOTS],
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
    pub cursor_is_cross: bool,
    pub started: Instant,
}

impl AppState {
    pub(crate) fn new() -> Self {
        let now = Instant::now();
        Self {
            login: login::Login::new(),
            select_scene: None,
            mode: 1,
            net: None,

            move_target: None,
            move_at: now,
            press_at: None,
            press_cell: (0, 0),
            held_move: None,
            retarget_at: now,
            last_move_fail: 0,
            detour: None,
            last_self_pos: (0, 0),
            move_block_until: now,
            hover: None,
            hint_pushed: false,
            alt_down: false,
            butch_target: None,
            butch_next: now,
            pending_pickup: None,
            combat_target: None,
            attack_at: now,
            last_foot_frame: None,

            win: windows::Windows::new(),
            bag_page: pager::Pager::new(),
            status_page: pager::Pager::new(),
            shop_seen_seq: 0,
            shop_ui: shopui::ShopUi::new(),
            ui_click: Ui::new(),
            drag: bagdrag::BagDrag::new(),
            dlg_scroll: scrollview::ScrollView::new(),
            dialog_seq_seen: 0,
            confirm: confirm::Confirm::new(),
            belt: [None; crate::layout::BELT_SLOTS],
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
            cursor_is_cross: false,
            started: now,
        }
    }

    pub(crate) fn begin_frame(&mut self) {
        // 所有动画/移动/开门逻辑共用每帧时钟；严禁只在构造时赋一次（会冻住）。
        self.started = Instant::now();
    }
}

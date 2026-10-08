//! MIR2 客户端核心 —— **纯函数，无 SDL**（见 [docs/plan.md §4.2](../../../docs/plan.md) 与 D-18）。
//!
//! 本 crate 由 `client/app`（SDL3 GUI）与 `client/e2e`（headless CLI）**共用**：
//! 协议编解码、会话状态机、资产解码、配置。
//!
//! **纪律**：这里不出现窗口 / **音频设备** / 网络线程。能跑满单测是硬要求。
//! （音频**规格**是个例外里的纯数据：`sound.rs` 只有编号表与 `sound.lst` 解析，
//! 不碰声卡 —— SDK 那一层在 `client/app/src/audio.rs`。）

pub mod actor;
pub mod auth;
pub mod blend;
/// 进世界的握手流程（纯状态机）：喂进收到的信封，吐出下一条要发的消息。
pub mod entrance;
pub mod login_ui;
pub mod m2pk;
pub mod map;
pub mod palette;
pub mod paths;
pub mod select_ui;
/// 音效/音乐的**规格**：编号表、地形→脚步、`sound.lst` 解析（不出声）。
pub mod sound;
pub mod text;
/// 会话世界状态：收到一条信封 → 世界变成什么样（纯函数）。
///
/// `client/app` 与 `client/e2e` **共用这一份** —— 见 D-18 与 `world.rs` 的文件头。
pub mod world;
pub mod wzl;
pub mod wzx;

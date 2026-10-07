//! MIR2 客户端核心 —— **纯函数，无 SDL**（见 [docs/plan.md §4.2](../../../docs/plan.md) 与 D-18）。
//!
//! 本 crate 由 `client/app`（SDL3 GUI）与 `client/e2e`（headless CLI）**共用**：
//! 协议编解码、会话状态机、资产解码、配置。
//!
//! **纪律**：这里不出现窗口 / 音频 / 网络线程。能跑满单测是硬要求。

pub mod blend;
/// 进世界的握手流程（纯状态机）：喂进收到的信封，吐出下一条要发的消息。
pub mod entrance;
pub mod m2pk;
pub mod map;
pub mod palette;
pub mod paths;
/// 会话世界状态：收到一条信封 → 世界变成什么样（纯函数）。
///
/// `client/app` 与 `client/e2e` **共用这一份** —— 见 D-18 与 `world.rs` 的文件头。
pub mod world;
pub mod wzl;
pub mod wzx;

//! MIR2 客户端核心 —— **纯函数，无 SDL**（见 [docs/plan.md §4.2](../../../docs/plan.md) 与 D-18）。
//!
//! 本 crate 由 `client/app`（SDL3 GUI）与 `client/e2e`（headless CLI）**共用**：
//! 协议编解码、会话状态机、资产解码、配置。
//!
//! **纪律**：这里不出现窗口 / 音频 / 网络线程。能跑满单测是硬要求。

pub mod palette;
pub mod wzl;
pub mod wzx;

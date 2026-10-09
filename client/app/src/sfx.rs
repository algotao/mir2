//! 音频胶水：场景 BGM、按编号播音效、脚步/挥砍的边沿判定。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use crate::net::Net;

use crate::audio;

/// 挥刀声：按**武器形状**分类（`Actor.pas:2254-2261`）。
///
/// ⚠️ 原版在攻击动画的**帧 2** 播（`:2396-2401`），我们在发起这一帧就播 —— 差几十毫秒，
/// 接线简单得多（这条差异记在这儿，别当"照原版"）。
pub(crate) fn swing_sfx(
    n: &Net,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
) {
    let shape = n.world.self_feature.as_ref().map_or(0, |f| f.weapon / 2) as u16;
    sfx(sound, sounds, mir2_core::sound::swing(shape));
}

/// 走路动画帧号 → 这一步该不该响、响的是不是**第二只脚**。
///
/// 原版：走路动画的**帧 1** 响一声、**帧 4** 再响一声（`Actor.pas:2659-2660`），
/// 两声差 1 号（`_l` / `_r`）。`last` 是上一帧的帧号 —— 同一个帧号只响一次
/// （帧率比 `ftime` 快时，帧号会连续几帧不变）。
///
/// 抽成函数是为了能单测这条"边沿判定"（循环里测不到）。
pub(crate) fn footstep_of(frame: u16, last: Option<u16>) -> Option<bool> {
    if last == Some(frame) {
        return None;
    }
    match frame {
        1 => Some(false),
        4 => Some(true),
        _ => None,
    }
}

/// 按原版编号播一条音效：**没有资产 / 没有这一条 ⇒ 静默跳过**（不报错、不崩）。
///
/// ⚠️ 静默是**有意的**：原版 `PlaySound` 也是先 `FileExists` 再放
/// （`SoundUtil.pas:183-186`），缺素材是常态（清单里就有 13 条编号在源里没文件）。
pub(crate) fn sfx(
    sound: &audio::Audio,
    assets: &Option<mir2_core::sound::SoundAssets>,
    number: u16,
) {
    if let Some(a) = assets {
        sound.play_idx(a, number);
    }
}

/// 切场景 BGM（循环）。资产里没有 ⇒ 静默（同上）。
///
/// 真的换上了一首就打一行 —— 听不见的时候，"有没有音乐"总得有个可观测的东西。
pub(crate) fn bgm(
    sound: &audio::Audio,
    assets: &Option<mir2_core::sound::SoundAssets>,
    name: &str,
) {
    if let Some(a) = assets {
        if sound.bgm_name(a, name) {
            println!("[audio] BGM = {name}");
        }
    }
}

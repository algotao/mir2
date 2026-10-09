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
    // ⚠️ 形状 = feature 里的 `weapon` 字节**本身**（D-66 起服务端发的就是 `Shape`）。
    // 这里原来写着 `f.weapon / 2` —— 那是按"weapon = Shape*2+性别"推的，但服务端现在发
    // Shape ⇒ 木剑(Shape=1) 被算成 0 ⇒ **挥刀声落到了最不像木剑的那一档**
    //（用户 2026-10-09 第 3 条：「木剑攻击声音不对，更像挖矿」）。
    let shape = n.world.self_feature.as_ref().map_or(0, |f| f.weapon) as u16;
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

/// 怪物的"正常声"（鸡叫、鹿鸣…）。
///
/// 官方出处（`Actor.pas:2344-2352` + `:2463-2470`）：
///
/// ```pascal
/// m_nNormalSound := 200 + m_wAppearance * 10 + 1;
/// // 走动/转身时：
/// if (m_nCurrentAction = SM_WALK) or (m_nCurrentAction = SM_TURN) then
///    if (frame = 1) and (Random(8) = 1) then PlaySound (m_nNormalSound);
/// ```
///
/// ⚠️ **与原版的差别**（记在这儿，别当"照原版"）：别的实体的动画帧号我们拿不到
///（帧号是在精灵采样里算的，音频层看不到），这里用"这只**正在移动** + 1/8 概率"
/// 近似，每帧调一次、一帧最多响一声。要完全对齐得把帧号从 `actor.rs` 的采样里导出来。
pub(crate) fn monster_ambient(
    n: &Net,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
    now: std::time::Instant,
    rng: &mut u32,
) {
    for e in n.world.entities.values() {
        // 只有**怪**（玩家/NPC 没有这套音），且只在这只正在移动时才可能叫
        if e.kind != 1 || e.dead {
            continue;
        }
        let Some(a) = n.anims.get(&e.id) else { continue };
        if !a.moving(now) {
            continue;
        }
        let Some(f) = e.feature.as_ref() else { continue };
        // 1/8（原版 `Random(8) = 1`）—— 用个便宜的 LCG，不为这一处引 rand 依赖
        *rng = rng.wrapping_mul(1_664_525).wrapping_add(1_013_904_223);
        if *rng >> 29 != 0 {
            continue;
        }
        sfx(
            sound,
            sounds,
            mir2_core::sound::monster(f.appr as u16, mir2_core::sound::MonsterSound::Normal),
        );
        return; // 一帧最多叫一声（一屏几十只怪同时叫会炸）
    }
}

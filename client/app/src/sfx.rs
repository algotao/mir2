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

// ---------- 怪物"正常声"（鸡叫/鹿鸣…）的三道闸 ----------
//
// 原版只有前两条（一轮走步一次 + 1/8 概率），第三条是我们加的：我们一屏常常几十只怪
// 同时走，按原版判也会变成一锅粥（用户 2026-10-10 报的）。
/// 代 `frame = 1` 的窗口：只在这一格**刚起步**的这段时间里判定 ⇒ 一轮走步至多一次。
const AMBIENT_MOVE_WINDOW: u64 = 150;
/// 只听**身边**多少格内的怪（切比雪夫距离）。
const AMBIENT_RANGE: i32 = 9;
/// 两次怪声之间的**全局**最小间隔（毫秒）—— 这才是"极少数发声"的那一道。
const AMBIENT_GAP_MS: u64 = 900;

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
/// ⚠️ **判定频率**（2026-10-10 改，用户报"多只怪时声音乱套"）：
///
/// 原版是 `if (frame = 1) and (Random(8) = 1)` ⇒ **一轮走步只判定一次**（`frame` 是
/// 走路动画的帧号，一轮 6 帧 × 90 ms ≈ 540 ms），不是每帧判。我们音频层拿不到帧号，
/// 于是用"**这一格刚刚开始走**"（`anim.changed_at` 距今 ≤ `AMBIENT_MOVE_WINDOW`）来代
/// `frame = 1` —— 同样是一轮一次。早先写成"每帧判一次"，等于把概率放大了一个数量级
///（60 fps × 1/8 ≈ 每秒 7 次）⇒ 一屏几十只怪时就是一锅粥。
///
/// 另外加了原版没有的**两道闸**（原版通常没那么多怪同时走，而我们有）：
///   - `AMBIENT_RANGE`：只听**身边**的怪（远处的不响 —— 视野里二十只一起响没有意义）；
///   - `AMBIENT_GAP_MS`：**全局**最小间隔，两次怪声之间至少隔这么久 ⇒ 实际效果就是
///     "极少数发声"。
pub(crate) fn monster_ambient(
    n: &Net,
    sound: &audio::Audio,
    sounds: &Option<mir2_core::sound::SoundAssets>,
    now: std::time::Instant,
    rng: &mut u32,
    last: &mut std::time::Instant,
) {
    // 全局间隔没到 ⇒ 这一帧谁都不许叫（一屏几十只怪时的总闸）
    if now.duration_since(*last) < std::time::Duration::from_millis(AMBIENT_GAP_MS) {
        return;
    }
    if !n.world.in_world() {
        return;
    }
    let p = n.world.self_pos;
    for e in n.world.entities.values() {
        // 只有**怪**（玩家/NPC 没有这套音）
        if e.kind != 1 || e.dead {
            continue;
        }
        let Some(a) = n.anims.get(&e.id) else {
            continue;
        };
        if !a.moving(now) {
            continue;
        }
        // 代 `frame = 1`：只在这一格**刚起步**的那一小段里判定（一轮一次）
        if now.duration_since(a.changed_at) > std::time::Duration::from_millis(AMBIENT_MOVE_WINDOW)
        {
            continue;
        }
        // 只听身边的（切比雪夫距离）
        if (e.x - p.0).abs().max((e.y - p.1).abs()) > AMBIENT_RANGE {
            continue;
        }
        let Some(f) = e.feature.as_ref() else {
            continue;
        };
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
        *last = now;
        return; // 一帧最多叫一声
    }
}

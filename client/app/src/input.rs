//! 鼠标/键盘的"意图"：点一下算什么、按住怎么重取目标、出手节流（D-45/D-46）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::time::Duration;

use crate::geom::dir_to;

/// 一次"鼠标点下去"算什么：`(要锁的目标, 要走的格子)`。
///
/// 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：**先清掉旧目标**，再看光标那格有什么 ——
/// 有**活怪** ⇒ 锁它（之后每帧自动靠近/出手，见 `World::combat_step`）；
/// 空地（或玩家/NPC：那要 Shift 才是 PK 那条线，还没做）⇒ 走/跑到那一格。
///
/// 抽成纯函数是为了能单测：这条规则在原版里散在几百行事件代码里，改一次踩一次。
pub(crate) fn mouse_intent(
    world: &mir2_core::world::World,
    cell: (i32, i32),
    run: bool,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    match world.attack_target_at(cell) {
        Some(id) => (Some(id), None),
        None => (None, Some((cell.0, cell.1, run))),
    }
}

/// **按住不放**时每 [`MOUSE_REPEAT_MS`] 重取一次目标 —— 与新鲜按下（[`mouse_intent`]）
/// 只差一条：光标底下什么都没有时，**不取消**已经锁住的那个怪物。
///
/// # 为什么要差这一条（用户 2026-10-08 报的"点怪之后站不住、掉头去走路"）
///
/// 原版重跑按下逻辑会先把 `g_TargetCret := nil`（`ClMain.pas:2813`），光标不在怪身上就不再锁
/// —— 而**镜头跟着人走**（[`follow_cam`]）⇒ 跑向怪的路上光标自己就从怪身上滑开了，
/// 于是"按住左键点怪"会半路把目标弄丢、改成走去光标那格。
/// 单击（按下就抬）不走这条路，所以原版"单击点怪 ⇒ 一路打到它死"是好的 ——
/// 我们这里把**按住**也保住：已经锁着、而且那个怪还活着（`combat_step` 还有得出）就继续打。
/// 想取消？松手再点一下地（新鲜按下还是照原版**清目标**）。
pub(crate) fn mouse_repeat(
    world: &mir2_core::world::World,
    cell: (i32, i32),
    run: bool,
    locked: Option<u64>,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    match world.attack_target_at(cell) {
        Some(id) => (Some(id), None),
        // 锁着的那个还在（活着且还在视野里）⇒ 继续打它
        None if locked.is_some_and(|id| world.combat_step(id).is_some()) => (locked, None),
        None => (None, Some((cell.0, cell.1, run))),
    }
}

/// **服务端**的出手间隔基数（`netgate.go` 的 `AttackIntervalFor`：`520 − 攻速×25`）。
///
/// 我们的节拍必须**慢于**它，否则那一刀会被服务端丢掉：丢掉的那次服务端**不发
/// `EntityAction`** ⇒ 挥砍动画（完全来自服务端回包）断一拍 —— 正是用户报的
/// "砍几下停下、但怪在掉血"。
/// ⚠️ 只有测试读它（断言"任何等级的节拍都不快于服务端"）—— 生产代码里
/// [`attack_gap`] 用的是 [`HIT_WINDOW_MS`]。留着是因为它是**服务端口径的唯一出处**。
#[allow(dead_code)]
pub(crate) const HIT_BASE_MS: u64 = 520;

/// **客户端**的出手窗口基数（原版 `g_nHitTime`）：`ClMain.pas:6997` 取
/// `ClientConf.wHitIime`，而 `ObjBase.pas:16838` 定义 `wHitIime := dwHitIntervalTime{520} + 500`
/// ⇒ **1020**。
pub(crate) const HIT_WINDOW_MS: u64 = 1020;

/// 连续攻击的最小节拍（原版 `CanNextHit`，`ClMain.pas:3986-4003`）：
///
/// ```text
/// LevelFastTime := min(370, level * 14);
/// LevelFastTime := min(800, LevelFastTime + m_nHitSpeed * g_nItemSpeed{60});
/// NextHitTime   := g_nHitTime{1020} - LevelFastTime;
/// ```
///
/// ⇒ 1 级 ≈ **1006ms**、27 级及以上（无攻速装备）≈ **650ms**。
///
/// ⚠️ 我们原来**写死 560**（= 服务端 520 + 40 余量）⇒ 低级玩家**比原版快近一倍**
///（用户 2026-10-09 第 3 条"普通攻击的正确性"）。
///
/// ⚠️ **攻速（`m_nHitSpeed`）协议里没下发**，第二项暂时算不了：等它进 `Ability`
/// 再补 `min(800, level*14 + hitSpeed*60)`（`g_nItemSpeed = 60`）。
///
/// ⚠️ 这一条同时就是 `IsIdle`：原版 `CanNextAction` 要求"上一条动作播完"
///（`ClMain.pas:3975-3984`），而出手动画 510ms **短于**窗口（≥650ms）⇒ 节拍本身
/// 已经把动作盖住了，不必再单独判一次。
pub(crate) fn attack_gap(level: u32) -> Duration {
    let fast = (level as u64 * 14).min(370);
    Duration::from_millis(HIT_WINDOW_MS.saturating_sub(fast))
}

/// 一次播完的动作（挥砍/受击）**多留最后一帧**多久（毫秒）。
///
/// # 为什么需要（用户 2026-10-09 报的"砍几下停下"的第二半）
///
/// 原版**看不出缝**：它的动画时长就是服务端下发的 `ClientConfig.HitTime`（`ClMain.pas:2700`），
/// 与攻击间隔同源（`ActHit` 6×85 = 510ms vs 间隔 520ms ⇒ 缝 10ms，眼睛看不见）。
/// 我们的动作表是写死的 510ms，而节拍取的是**服务端的基数 + 余量**（`attack_gap()` = 560ms，
/// 不能更小 —— 更小会被服务端丢掉整刀）⇒ 缝 50ms ≈ 三帧"站立"，看着就是一顿。
///
/// 所以把最后一帧多留这么久：下一刀在 560ms 到，正好接上（也不再需要把节拍压到危险区）。
pub(crate) const ACTION_TAIL_MS: u32 = 80;

/// 这一刻能不能**出手**：①手上这一步走完了 ②距上次出手够久了。
///
/// # ① 为什么必须有（用户 2026-10-08 报的"还没靠近怪就开始攻击"）
///
/// 原版的门是 `CanNextAction` = `g_MySelf.IsIdle`（`ClMain.pas:3975-3984` +
/// `Actor.pas:1722-1736`）：`m_nCurrentAction <> 0`（**动作没完**）就 `IsIdle = FALSE`
/// ⇒ 走路的动作还没播完，**攻击发不出去**。
///
/// 我们不做移动预测（位置是服务端权威的），所以特别需要这道门：服务端确认"到位"时，
/// 画面上的补间才刚起步 —— `combat_step` 已经说"相邻、该出手"，挥砍就播在半路上了。
pub(crate) fn can_attack(stepping: bool, since_last_attack: Duration, level: u32) -> bool {
    !stepping && since_last_attack >= attack_gap(level)
}

/// 按住了 Ctrl 吗（`keymod` 那套；开发键都收在 Ctrl+ 里）。
pub(crate) fn ctrl(m: sdl3::keyboard::Mod) -> bool {
    m.intersects(sdl3::keyboard::Mod::LCTRLMOD) || m.intersects(sdl3::keyboard::Mod::RCTRLMOD)
}

// ---------- 小地图 / 大地图（原版 `PlayScn.pas:791` 的 `DrawMiniMap`）----------

/// 鼠标连续走路：**这一步该走还是该跑**（`None` = 已经站在目标格上、收工）。
///
/// 照原版 `ClMain.pas:1863-1978`（`ProcessActionMessages`）：
///
/// ```text
/// if (g_nTargetX <> 自己.x) or (g_nTargetY <> 自己.y) then   // 还没到
///    caRun: if (GetDistance(自己, 目标) >= 2) and ... then 发 CM_RUN
///           else 只转身 + 清目标, goto LB_WALK（**走一步**）
/// ```
///
/// 也就是说**跑只在"距离 ≥ 2"时才发**（`GetDistance` = 切比雪夫距离，
/// `ClFunc.pas:352-355`：`_MAX(abs(dx), abs(dy))`）。
/// ⚠️ 少了这条，目标格是**奇数距离**时跑步（一次跨 2 格）会**跨过去再跨回来**，
/// 表现就是"奔跑位置左右乱换"（用户 2026-10-08 报的）；距离 < 2 时改成走 1 格正好落到。
pub(crate) fn next_move_step(
    from: (i32, i32),
    to: (i32, i32),
    want_run: bool,
) -> Option<(mir2_protocol::Direction, bool)> {
    let dir = dir_to(from, to)?; // 同一格 ⇒ None ⇒ 调用方收工
    let far = (to.0 - from.0).abs().max((to.1 - from.1).abs()) >= 2; // GetDistance（切比雪夫）
    Some((dir, want_run && far))
}

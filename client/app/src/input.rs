//! 鼠标/键盘的"意图"：点一下算什么、按住怎么重取目标、出手节流（D-45/D-46）。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::{HashMap, HashSet, VecDeque};
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
    hover: Option<u64>,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    // ⚠️ **锁怪优先用 `hover`**（用户 2026-10-10 第 4 条）：`hover` 是上一帧按
    // **画出来的精灵框**算的（名字条就是靠它显示出来的），怪的脚在哪一格不重要；
    // 而 `cell` 是光标**脚下那格** —— 怪比格子高、又站在斜前方时，脚下那格常常
    // 是它身后/旁边的空地 ⇒ "明明指着它却锁不上、人还往空地跑"。
    // `hover` 为空才退回按格子找。
    if let Some(id) = hover.filter(|id| world.attackable(*id)) {
        return (Some(id), None);
    }
    match world.attack_target_at(cell) {
        Some(id) => (Some(id), None),
        None => (None, Some((cell.0, cell.1, run))),
    }
}

/// **单击**（按下即抬）算什么：往光标那个方向**走一格**（用户 2026-10-09 第 3 条）。
///
/// 与 [`mouse_intent`]（按下 = 把光标那格当目标、一路走过去）的区别只有一处：
/// 目标被换成**紧邻的那一格** ⇒ 走一步就"到达"、目标自清（`next_move_step` 的
/// `dir_to` 返回 `None` 那支），于是表现就是"点一下走一格"。
///
/// 点的是**活怪**时与按下同一条规则：锁它，不动脚。
/// 点在自己身上（同一格）⇒ 什么都不做。
pub(crate) fn click_step(
    world: &mir2_core::world::World,
    from: (i32, i32),
    cell: (i32, i32),
    run: bool,
    hover: Option<u64>,
) -> (Option<u64>, Option<(i32, i32, bool)>) {
    // 与 `mouse_intent` 同一条：**锁怪优先 `hover`**（见那里的说明）
    if let Some(id) = hover.filter(|id| world.attackable(*id)) {
        return (Some(id), None);
    }
    if let Some(id) = world.attack_target_at(cell) {
        return (Some(id), None);
    }
    let (dx, dy) = ((cell.0 - from.0).signum(), (cell.1 - from.1).signum());
    if dx == 0 && dy == 0 {
        return (None, None);
    }
    (None, Some((from.0 + dx, from.1 + dy, run)))
}

/// 按下到抬起**多快**才算"单击"（而不是"按住走"）。
///
/// 判据取 200ms：比它短 ⇒ 一格（[`click_step`]）；按住会走 [`MOUSE_REPEAT_MS`]（300ms）
/// 那条重取目标的链 ⇒ 阈值卡在两者之间，手感上"点一下"与"按住"不会互相误判。
pub(crate) const CLICK_MS: u64 = 200;

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
#[allow(dead_code)] // 保留：没地图时的那条老路（`route_step(None, …)`）
pub(crate) fn next_move_step(
    from: (i32, i32),
    to: (i32, i32),
    want_run: bool,
) -> Option<(mir2_protocol::Direction, bool)> {
    route_step(None, from, to, want_run)
}

/// 朝 `to` 走一步：**先 BFS 找路**，找不到才退回"直着朝它走"。
///
/// ⚠️ 为什么要有 BFS（用户 2026-10-10 第 3 条）：原来只看 `dir_to`（一格一步的
/// 贪心），前面横着一道墙/一棵树就**永远贴着障碍推**，追怪的时候表现就是
/// "跑向怪物的寻路能力很差"。BFS 找的是真正的通路（8 邻，**斜向不许穿墙角**），
/// 取路径上的第一步 —— 服务端照样逐格校验，所以只是"别犯傻"，不是作弊。
///
/// `map` 为 `None`（地图没加载）或 BFS 到不了（目标本身被围死）⇒ 退回贪心：
/// 走过去被服务端拒了，那条路会照原版锁 1 秒再试（`MoveRejected`）。
pub(crate) fn route_step(
    map: Option<&mir2_core::map::Map>,
    from: (i32, i32),
    to: (i32, i32),
    want_run: bool,
) -> Option<(mir2_protocol::Direction, bool)> {
    let dir = dir_to(from, to)?; // 同一格 ⇒ None ⇒ 调用方收工
    let far = (to.0 - from.0).abs().max((to.1 - from.1).abs()) >= 2; // GetDistance（切比雪夫）
    let fallback = Some((dir, want_run && far));
    let Some(m) = map else {
        return fallback; // 没有地图 ⇒ 老办法（直着走）
    };
    // 目标格自己不可走（怪站在墙里/图外）⇒ 别白搜
    if !walkable(m, to) {
        return fallback;
    }
    let first = bfs_first_step(m, from, to)?;
    // 跑不跑按**这一步**的距离算（与原来同一个口径：≥2 才跑）
    let (dx, dy) = (first.0 - from.0, first.1 - from.1);
    let run = want_run && dx.abs().max(dy.abs()) >= 2;
    Some((dir_to(from, first)?, run))
}

/// 一格能不能走（`Map::can_walk` 只收 `usize` ⇒ 这里顺手挡掉负数与越界）。
fn walkable(m: &mir2_core::map::Map, c: (i32, i32)) -> bool {
    c.0 >= 0 && c.1 >= 0 && m.can_walk(c.0 as usize, c.1 as usize)
}

/// 从 `from` 广搜到 `to`，返回路径上的**第一格**（`from` 的相邻格）。
///
/// 上限 `ROUTE_MAX_CELLS` 格：够绕开一屏内的所有障碍，又不会在"根本到不了"
/// 的情况下把整张图搜一遍（追怪是每几百毫秒调一次）。
const ROUTE_MAX_CELLS: usize = 4096;

fn bfs_first_step(m: &mir2_core::map::Map, from: (i32, i32), to: (i32, i32)) -> Option<(i32, i32)> {
    if !walkable(m, from) || from == to {
        return None;
    }
    let mut prev: HashMap<(i32, i32), (i32, i32)> = HashMap::new();
    let mut q: VecDeque<(i32, i32)> = VecDeque::new();
    let mut seen: HashSet<(i32, i32)> = HashSet::new();
    seen.insert(from);
    q.push_back(from);
    let mut visited = 0usize;

    while let Some(cur) = q.pop_front() {
        if cur == to {
            // 回溯到起点，记下**起点迈出去的那一格**
            let mut node = to;
            while prev.get(&node) != Some(&from) {
                node = *prev.get(&node)?;
            }
            return Some(node);
        }
        visited += 1;
        if visited > ROUTE_MAX_CELLS {
            return None;
        }
        for (dx, dy) in [
            (1, 0),
            (-1, 0),
            (0, 1),
            (0, -1),
            (1, 1),
            (1, -1),
            (-1, 1),
            (-1, -1),
        ] {
            let next = (cur.0 + dx, cur.1 + dy);
            if seen.contains(&next) || !walkable(m, next) {
                continue;
            }
            // 斜向：两边至少有一格通，别从墙缝里钻过去
            if dx != 0
                && dy != 0
                && !walkable(m, (cur.0 + dx, cur.1))
                && !walkable(m, (cur.0, cur.1 + dy))
            {
                continue;
            }
            seen.insert(next);
            prev.insert(next, cur);
            q.push_back(next);
        }
    }
    None
}

// ---------- NPC 对话面板（用户 2026-10-09：点 NPC 要出对话）----------

/// 对话窗的**版式常量** —— 照抄官方（用户 2026-10-09 给的截图）。
///
/// 与旧版的区别：旧版是自己造的"居中悬浮半透明黑板" ✗；官方是**一块固定尺寸、
/// 固定在屏幕左上角**的背板（`Prguse[384]`，416×176 —— 就是图1/图2/图4 那张
/// "青铜框 + 深色内里"），正文在左上、选项排在正文下面（黄字 + 绿点、**不编号**）。
///
/// 背板是原生像素尺寸，**不缩放**（用户："界面显示文字在 1024×768 下不要缩放" ——
/// 背板也一样，画成 416×176 正好是官方大小）。
pub(crate) const DIALOG_BG: u32 = 384;

/// 背板尺寸（`Prguse[384]` 的原生尺寸）。
pub(crate) const DIALOG_W: f32 = 416.0;
pub(crate) const DIALOG_H: f32 = 176.0;

/// 落点：**贴屏幕左上角**。官方就是贴角（留 8/4 的缝才不压住窗口边）。
pub(crate) const DIALOG_X: f32 = 8.0;
pub(crate) const DIALOG_Y: f32 = 4.0;

/// 正文在背板里的内边距（避开青铜框内沿）。
pub(crate) const DIALOG_PAD_X: f32 = 24.0;
pub(crate) const DIALOG_PAD_Y: f32 = 16.0;

/// 行高（14px 字 + 4px 行距，与截图里的疏密一致）。
///
/// 正文与行内选项**同一行高** —— 它们本来就同行（以前那份"底部选项列表"是另一套）。
pub(crate) const DIALOG_LINE_H: f32 = 18.0;

/// 正文最多画几行 —— **按背板几何算出来**，不是拍脑袋定的。
///
/// 背板固定高 176、上下各留 `DIALOG_PAD_Y` ⇒ `(176-32)/18 = 8` 行。
///
/// ⚠️ 原来写死 5，结果陈家铺老板那段（正文 1 行 + 空行 + 5 个行内选项 = 7 行）
/// **把「退出」截掉了** —— 用户 2026-10-09：「对话的『退出』按钮怎么没有？」
/// 就是这个问题。改成按几何算，背板尺寸一变它自己跟着变，不会再漂。
pub(crate) const DIALOG_MAX_LINES: usize =
    ((DIALOG_H - 2.0 * DIALOG_PAD_Y) / DIALOG_LINE_H) as usize;

/// 正文装不下时**最多能滚几行**。
///
/// ⚠️ 为什么非滚不可：脚本的 `[@main]` 折行之后**远超**一屏 —— 2026-10-10 量过
/// 298 个脚本，**114 个**超过 8 行，最长的 `2Arms_dealer-0103` 有 **46 行**
///（而且折行只算纯文字行）。背板是固定高的（`Prguse[384]` 416×176）⇒
/// 换成更高的板也装不下，只能滚。滚不动的后果是**装不下的行既画不出也点不到** ——
/// 「退出」常常就落在第 9、10 行（用户 2026-10-10 报的次生问题）。
pub(crate) fn dialog_max_scroll(lines: usize) -> usize {
    lines.saturating_sub(DIALOG_MAX_LINES)
}

/// 把滚动位置夹回合法范围（换了段更短的对话、或滚过头了都要用它）。
pub(crate) fn dialog_scroll_clamp(scroll: usize, lines: usize) -> usize {
    scroll.min(dialog_max_scroll(lines))
}

/// 滚轮一格（`y` 是 SDL 的滚轮量：**> 0 = 往上滚**）。
///
/// 抽成纯函数是为了能单测（事件循环里不好造 `Event`）。
pub(crate) fn dialog_scroll_step(scroll: usize, lines: usize, y: f32) -> usize {
    let cur = dialog_scroll_clamp(scroll, lines);
    if y > 0.0 {
        cur.saturating_sub(1)
    } else if y < 0.0 {
        dialog_scroll_clamp(cur + 1, lines)
    } else {
        cur
    }
}

/// 滚动条的宽度（细条，贴在正文区右侧）。
pub(crate) const DIALOG_BAR_W: f32 = 4.0;

/// 滚动条与正文右缘的间距。
const DIALOG_BAR_GAP: f32 = 8.0;

/// 滚动条的**轨道**矩形（一直存在，只是内容装得下时不画）。
pub(crate) fn dialog_bar_track(panel: (f32, f32, f32, f32)) -> (f32, f32, f32, f32) {
    let (px, py, pw, _) = panel;
    (
        px + pw - DIALOG_BAR_GAP - DIALOG_BAR_W,
        py + DIALOG_PAD_Y,
        DIALOG_BAR_W,
        DIALOG_MAX_LINES as f32 * DIALOG_LINE_H,
    )
}

/// 滚动条的**滑块**矩形：`None` = 内容装得下、不用画。
///
/// 滑块高度按"可见 / 总数"的比例缩（内容越长滑块越短，与常见滚动条一致），
/// 最短 12px —— 46 行内容按 8/46 缩出来只有 ~4px，手指看不清。
pub(crate) fn dialog_scroll_thumb(
    panel: (f32, f32, f32, f32),
    lines: usize,
    scroll: usize,
) -> Option<(f32, f32, f32, f32)> {
    if lines <= DIALOG_MAX_LINES {
        return None;
    }
    let (tx, ty, tw, th) = dialog_bar_track(panel);
    let visible = DIALOG_MAX_LINES as f32;
    let total = lines as f32;
    let h = (th * visible / total).max(12.0).min(th);
    let room = (th - h).max(0.0);
    let s = dialog_scroll_clamp(scroll, lines) as f32;
    let max_s = dialog_max_scroll(lines) as f32;
    let y = ty + if max_s > 0.0 { room * s / max_s } else { 0.0 };
    Some((tx, y, tw, h))
}

/// 正文一行最多几个字。背板内宽 ≈ `416 - 2*24 = 368px`，14px 一个字 ⇒ 约 26 个，
/// 留点余量取 24（`\n` 仍然强制换行）。
pub(crate) const DIALOG_WRAP_CHARS: usize = 24;

/// 关闭按钮（右上角那个红 X）在**面板内**的位置。
///
/// 量自 `Prguse[384]` 的像素（`wzldump` 扫出来）：红叉落在 `x 401..411`、`y 0..15`，
/// 命中框放宽一圈，手指点得中。
pub(crate) const DIALOG_CLOSE_X: f32 = 397.0;
pub(crate) const DIALOG_CLOSE_Y: f32 = 0.0;
pub(crate) const DIALOG_CLOSE_W: f32 = 19.0;
pub(crate) const DIALOG_CLOSE_H: f32 = 19.0;

/// 鼠标点在右上角的关闭 X 上吗。
pub(crate) fn dialog_close_hit(panel: (f32, f32, f32, f32), mouse: (f32, f32)) -> bool {
    let (px, py, _, _) = panel;
    let (lx, ly) = (mouse.0 - px, mouse.1 - py);
    (DIALOG_CLOSE_X..DIALOG_CLOSE_X + DIALOG_CLOSE_W).contains(&lx)
        && (DIALOG_CLOSE_Y..DIALOG_CLOSE_Y + DIALOG_CLOSE_H).contains(&ly)
}

/// 对话面板的矩形 `(x, y, w, h)`（画布坐标）—— 固定尺寸、固定左上角。
pub(crate) fn dialog_panel() -> (f32, f32, f32, f32) {
    (DIALOG_X, DIALOG_Y, DIALOG_W, DIALOG_H)
}

/// 对话正文里的一个片段：普通文字，或者一段**行内可点文字**。
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum DialSeg {
    Text(String),
    /// `index` 是回包要带的**选项序号**（1 起）。
    ///
    /// 服务端把脚本里的 `<打开/@trading>` 改写成 `<打开/@1>`（见
    /// `server/internal/script/script.go` 的 `Label.Lines`）⇒ 客户端不用猜标签，
    /// 直接拿它回 `NpcSelect{index}`。
    Link {
        index: u32,
        text: String,
    },
}

/// 链接前面那个绿方块 + 间距占的宽度（官方样式：方块在文字左侧）。
pub(crate) const DIALOG_BULLET_W: f32 = 12.0;

/// 把服务端下发的正文切成**行 → 片段**（画与命中都用它，别各算一份）。
///
/// - 正文里带 `<文字/@序号>` ⇒ **留在原行**。用户 2026-10-09：「交易窗口渲染不对，
///   应该为『打开 交易市场』在一行，其中『打开』可点击」—— 脚本本来就是这么写的
///   （` <打开/@trading> 交易市场\`），旧版把链接抽到下面单列，所以看着不对。
/// - 正文里**没有**标记但有 `options`（老服务端 / 没有行内链接的脚本）⇒ 把选项各排一行，
///   与以前那个"底部选项列表"的行为一致。
///
/// ⚠️ 折行：**纯文字行**按 [`DIALOG_WRAP_CHARS`] 折（脚本正文常常很长）；
/// **带链接的行**原样保留 —— 脚本作者自己用 `\` 断行，硬折会把链接与它修饰的
/// 文字拆散（"打开 / 交易市场"分两行就回到老毛病了）。
pub(crate) fn dialog_lines(text: &str, options: &[(u32, String)]) -> Vec<Vec<DialSeg>> {
    let mut lines: Vec<Vec<DialSeg>> = Vec::new();
    let mut any_link = false;
    // ⚠️ 脚本里的 `\` 是**换行**（`[@main]` 一行写不下就换行接着写），不是正文
    // —— 服务端发的是"原样行" ⇒ `\` 会原样带到客户端。用户 2026-10-10 第 3 条：
    // 对话里最后那个反斜杠不该出现 ⇒ 把它当换行吃掉（原版就是换行）。
    let text = text.replace('\\', "\n");
    for raw in text.split('\n') {
        let mut segs = Vec::new();
        let has = parse_marked_line(raw, &mut segs);
        if has {
            any_link = true;
            lines.push(segs);
            continue;
        }
        if raw.trim().is_empty() {
            lines.push(vec![DialSeg::Text(String::new())]); // 空行占位（脚本用它分段）
            continue;
        }
        for chunk in wrap_text(raw.trim(), DIALOG_WRAP_CHARS) {
            lines.push(vec![DialSeg::Text(chunk)]);
        }
    }
    if !any_link && !options.is_empty() {
        for (idx, t) in options {
            lines.push(vec![DialSeg::Link {
                index: *idx,
                text: t.clone(),
            }]);
        }
    }
    lines
}

/// 解析一行里的 `<文字/@序号>`，把片段推进 `out`；返回**本行有没有链接**。
fn parse_marked_line(s: &str, out: &mut Vec<DialSeg>) -> bool {
    let mut rest = s;
    let mut has = false;
    while let Some(lt) = rest.find('<') {
        let Some(gt) = rest[lt..].find('>') else {
            break;
        };
        let inner = &rest[lt + 1..lt + gt];
        let Some((text, index)) = inner
            .split_once("/@")
            .and_then(|(t, n)| n.trim().parse::<u32>().ok().map(|i| (t.to_string(), i)))
        else {
            break; // 不是标记：后面整段当文字
        };
        if lt > 0 {
            out.push(DialSeg::Text(rest[..lt].to_string()));
        }
        out.push(DialSeg::Link { index, text });
        has = true;
        rest = &rest[lt + gt + 1..];
    }
    if !rest.is_empty() {
        out.push(DialSeg::Text(rest.to_string()));
    }
    has
}

/// 一个片段排好之后的落点。
pub(crate) struct DialPiece {
    /// 完整命中矩形（链接**含**前面的绿方块）。
    pub rect: (f32, f32, f32, f32),
    /// 文字落笔的 x（链接是方块之后的那一点）。
    pub text_x: f32,
    pub seg: DialSeg,
}

/// 某一行的排版：`line_no` 从 0 起。
///
/// **画与命中同源** —— `draw_dialog` 与点击判定都调它。装不下的行返回空
///（背板固定高 ⇒ 画不出来的东西也不该点得到）。
pub(crate) fn dialog_line_pieces(
    panel: (f32, f32, f32, f32),
    line_no: usize,
    segs: &[DialSeg],
    measure: &mut dyn FnMut(&str) -> f32,
) -> Vec<DialPiece> {
    let (px, py, _pw, ph) = panel;
    if line_no >= DIALOG_MAX_LINES {
        return Vec::new();
    }
    let y = py + DIALOG_PAD_Y + line_no as f32 * DIALOG_LINE_H;
    if y + DIALOG_LINE_H > py + ph {
        return Vec::new();
    }
    let mut x = px + DIALOG_PAD_X;
    let mut out = Vec::with_capacity(segs.len());
    for seg in segs {
        match seg {
            DialSeg::Text(t) => {
                let w = measure(t);
                out.push(DialPiece {
                    rect: (x, y, w, DIALOG_LINE_H),
                    text_x: x,
                    seg: seg.clone(),
                });
                x += w;
            }
            DialSeg::Link { text, .. } => {
                let w = measure(text) + DIALOG_BULLET_W;
                out.push(DialPiece {
                    rect: (x, y, w, DIALOG_LINE_H),
                    text_x: x + DIALOG_BULLET_W,
                    seg: seg.clone(),
                });
                x += w;
            }
        }
    }
    out
}

/// 鼠标点在某段**行内链接**上 ⇒ 返回选项序号（1 起）。
/// 当前**看得见**的行：`(窗口内行号, 该行的片段)`。
///
/// **画与命中都走它** —— 滚出窗外的行既画不出来也点不到（与 `dialog_line_pieces`
/// 里"装不下就返回空"同一条纪律：画不出来的东西不该点得到）。
pub(crate) fn dialog_visible_lines(
    lines: &[Vec<DialSeg>],
    scroll: usize,
) -> impl Iterator<Item = (usize, &[DialSeg])> {
    let s = dialog_scroll_clamp(scroll, lines.len());
    lines
        .iter()
        .skip(s)
        .take(DIALOG_MAX_LINES)
        .enumerate()
        .map(|(row, segs)| (row, segs.as_slice()))
}

/// 鼠标点在哪条链接上（`scroll` = 当前卷动到第几行）。
pub(crate) fn dialog_link_at(
    panel: (f32, f32, f32, f32),
    lines: &[Vec<DialSeg>],
    mouse: (f32, f32),
    scroll: usize,
    measure: &mut dyn FnMut(&str) -> f32,
) -> Option<u32> {
    for (row, segs) in dialog_visible_lines(lines, scroll) {
        for p in dialog_line_pieces(panel, row, segs, measure) {
            let DialSeg::Link { index, .. } = p.seg else {
                continue;
            };
            let (x, y, w, h) = p.rect;
            if mouse.0 >= x && mouse.0 <= x + w && mouse.1 >= y && mouse.1 <= y + h {
                return Some(index);
            }
        }
    }
    None
}

/// 点是否落在面板里（落在面板里但没点在选项上 ⇒ **别走路**）。
// 点落在对话窗里吗（窗里但没点在选项上 ⇒ 别走路）。目前只有测试用它，
// 但它是「点对话窗不算走路」这条规则的判据，留着。
#[allow(dead_code)]
pub(crate) fn dialog_hit(panel: (f32, f32, f32, f32), mouse: (f32, f32)) -> bool {
    let (x, y, w, h) = panel;
    mouse.0 >= x && mouse.0 <= x + w && mouse.1 >= y && mouse.1 <= y + h
}

/// 把一段文本按**字数**硬折行（中文按字算）。原版靠字体量宽折行，我们先用粗版：
/// 每行 `max` 个字，`\n` 强制换行。返回折好的行。
pub(crate) fn wrap_text(text: &str, max: usize) -> Vec<String> {
    let mut out = Vec::new();
    for raw in text.split('\n') {
        let chars: Vec<char> = raw.chars().collect();
        if chars.is_empty() {
            out.push(String::new());
            continue;
        }
        for chunk in chars.chunks(max.max(1)) {
            out.push(chunk.iter().collect());
        }
    }
    out
}

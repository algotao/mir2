//! 世界交互控制状态：鼠标移动、追打、挖肉、待捡取与节拍。
//!
//! SDL 鼠标按钮在 `world_input` 边界翻译成 [`MoveButton`]；这里不持 SDL 类型。
//! 地图/网络权威数据不归此处，控制器只记目标与下一次动作时间。

use std::time::Instant;

/// 按住鼠标的移动意图（平台无关）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum MoveButton {
    Walk,
    Run,
}

/// 可测试的世界交互运行状态（而非 SDL 事件状态）。
#[derive(Debug)]
pub(crate) struct WorldControl {
    /// 目标格与是否跑步。
    pub move_target: Option<(i32, i32, bool)>,
    pub move_at: Instant,
    /// 鼠标按下时的目标快照：松开快于 `CLICK_MS` 时退化成走一格。
    pub press_at: Option<Instant>,
    pub press_cell: (i32, i32),
    pub held_move: Option<MoveButton>,
    pub retarget_at: Instant,
    /// `MoveRejected` 计数快照。
    pub last_move_fail: u64,
    /// 绕障步骤（先逆时针、再顺时针）。
    pub detour: Option<u8>,
    pub last_self_pos: (i32, i32),
    pub move_block_until: Instant,
    /// Alt 挖肉与目标尸体。
    pub alt_down: bool,
    pub butch_target: Option<u64>,
    pub butch_next: Instant,
    /// 走到地面物品格后再发拾取。
    pub pending_pickup: Option<(u64, (i32, i32))>,
    /// 自动追打目标与攻击节拍。
    pub combat_target: Option<u64>,
    pub attack_at: Instant,
    /// 本地脚步动画音效去重。
    pub last_foot_frame: Option<u16>,
}

impl WorldControl {
    pub(crate) fn new(now: Instant) -> Self {
        Self {
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
            alt_down: false,
            butch_target: None,
            butch_next: now,
            pending_pickup: None,
            combat_target: None,
            attack_at: now,
            last_foot_frame: None,
        }
    }

    /// 松开鼠标时清理按住态，并取出单击计时（仍保留点击后执行的移动目标）。
    pub(crate) fn release_mouse(&mut self) -> Option<Instant> {
        self.held_move = None;
        self.press_at.take()
    }

    /// 命中浮窗后取消本次世界点击可能产生的目标/待拾取状态。
    pub(crate) fn consume_world_click(&mut self) {
        self.move_target = None;
        self.combat_target = None;
        self.press_at = None;
        self.pending_pickup = None;
        self.held_move = None;
    }

    /// 清除走路与追打目标，但保留绕障计数。
    pub(crate) fn clear_targets(&mut self) {
        self.move_target = None;
        self.combat_target = None;
    }

    /// 取消当前追打/走路意图（如移动被拒）。
    pub(crate) fn cancel_target(&mut self) {
        self.move_target = None;
        self.combat_target = None;
        self.detour = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn 松开鼠标取消按住态但保留单击目标() {
        let now = Instant::now();
        let mut s = WorldControl::new(now);
        s.held_move = Some(MoveButton::Run);
        s.press_at = Some(now);
        s.press_cell = (12, 8);
        s.move_target = Some((15, 8, true));

        assert_eq!(s.release_mouse(), Some(now), "松开时取出本次单击计时");
        assert_eq!(s.held_move, None);
        assert_eq!(s.press_at, None);
        assert_eq!(
            s.move_target,
            Some((15, 8, true)),
            "点击后的走法目标继续执行"
        );
    }

    #[test]
    fn 命中浮窗时清掉本次世界点击与待拾取() {
        let now = Instant::now();
        let mut s = WorldControl::new(now);
        s.move_target = Some((4, 5, false));
        s.combat_target = Some(42);
        s.press_at = Some(now);
        s.pending_pickup = Some((7, (4, 5)));
        s.held_move = Some(MoveButton::Walk);

        s.consume_world_click();
        assert_eq!(s.move_target, None);
        assert_eq!(s.combat_target, None);
        assert_eq!(s.press_at, None);
        assert_eq!(s.pending_pickup, None);
        assert_eq!(s.held_move, None);
    }

    #[test]
    fn 取消目标同时清走法追打和绕障() {
        let now = Instant::now();
        let mut s = WorldControl::new(now);
        s.move_target = Some((4, 5, false));
        s.combat_target = Some(42);
        s.detour = Some(1);
        s.move_block_until = now + Duration::from_secs(1);

        s.cancel_target();
        assert_eq!(s.move_target, None);
        assert_eq!(s.combat_target, None);
        assert_eq!(s.detour, None);
        assert!(s.move_block_until > now, "拒步冷却不由清目标动作重置");
    }
}

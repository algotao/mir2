//! 会话世界状态：**收到一条信封 → 世界变成什么样**（纯函数，无 IO、无 SDL）。
//!
//! 为什么放在 `core`：按 [D-18] 的纪律，`client/app`（GUI）与 `client/e2e`（无头）
//! 必须走**同一份**协议编解码与会话状态机 —— 否则契约测试检验的就不是真客户端，
//! 而"app 自己另写一份解释逻辑"正是双实现漂移的温床（plan R-1/R-10）。
//!
//! 这里只管**状态**，不管怎么画：渲染需要的字段（坐标/朝向/外观/血量）都留在
//! `Entity` 上，具体画法由调用方决定（app 目前画标记，将来换精灵是渲染层的事）。
//!
//! [D-18]: ../../../docs/decisions.md

use std::collections::BTreeMap;

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

/// 视野里的一个实体（**不含自己**：自己在 `World` 上单列，见 `EnterWorld`）。
#[derive(Debug, Clone, PartialEq)]
pub struct Entity {
    pub id: u64,
    /// 0=玩家 1=怪物 2=NPC（`EntityState.kind`）。
    pub kind: u32,
    pub name: String,
    pub x: i32,
    pub y: i32,
    /// `proto::Direction` 的原值（1..8；0 = 未指定）。**保持线上编号不乱转**，
    /// 免得"哪一层减了 1"这种错在两端之间来回漂。
    pub dir: i32,
    pub feature: Option<proto::EntityFeature>,
    pub hp: u32,
    pub max_hp: u32,
    /// 最近一次移动是不是**跑**（`EntityMove.run`）—— 决定播 `ActWalk` 还是 `ActRun`。
    ///
    /// ⚠️ 它是"这一步"的属性（不是"这个人常常跑"），所以每次移动都会被重写；
    /// 停下来之后这个值虽然还留着，但渲染层只在**移动中**看它（`ActorAnim::moving`）。
    pub run: bool,
    pub status_bits: u64,
    /// 已经死了（收到 `Death` 之后、`EntityDisappear` 之前的那段时间 = 尸骨）。
    ///
    /// ⚠️ 死亡**不等于**消失：原版里尸骨会留一会儿（法师还能打尸骨），
    /// 所以这里只标记，实体仍在 `entities` 里 —— 由渲染层决定怎么画（半透明/躺下）。
    pub dead: bool,
    /// 最近一次动作（见 `protocol.md` §9.5 的动作 id 值域：1..8 攻击 51 受击 52 死亡）。
    pub action: Option<u32>,
}

/// 一次伤害事件（`Damage`）。**由调用方取走**（`World::take_damage`）并决定怎么表现。
///
/// 为什么不在这里做"飘字计时"：`World` 是纯状态、**没有时钟**（见文件头）。
/// 计时与淡出是渲染层的事，那里本来就有帧时钟。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct DamageEvent {
    pub attacker_id: u64,
    pub target_id: u64,
    /// 伤害值（`flags` 里的位说明暴击/闪避等，见 combat.proto）。
    pub value: i32,
    pub flags: u32,
}

impl Entity {
    fn from_state(s: &proto::EntityState) -> Self {
        let pos = s.position.unwrap_or_default();
        Entity {
            id: s.entity_id,
            kind: s.kind,
            name: s.name.clone(),
            x: pos.x,
            y: pos.y,
            dir: s.direction,
            feature: s.feature,
            hp: s.hp,
            max_hp: s.max_hp,
            // 快照不带"跑"这个信息（它只属于一次移动）⇒ 出现时一律先当走的
            run: false,
            status_bits: s.status_bits,
            dead: false,
            action: None,
        }
    }

    /// 位置更新（`EntityMove` 只给 from/to，不给 kind/名字 ⇒ 就地改）。
    ///
    /// `run` 是**这一步**是不是跑的 —— 渲染层靠它决定播 `ActWalk` 还是 `ActRun`
    ///（两段的图号差 64，见 `actor::HAct`）。原地转身（`from == to`）时服务端给 `false`。
    fn set_pos(&mut self, x: i32, y: i32, dir: i32, run: bool) {
        self.x = x;
        self.y = y;
        self.run = run;
        if dir != proto::Direction::Unspecified as i32 {
            self.dir = dir;
        }
    }
}

/// 自己的能力值（客户端要画血条/经验条/负重条）。
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub struct Ability {
    pub level: u32,
    pub hp: u32,
    pub max_hp: u32,
    pub mp: u32,
    pub max_mp: u32,
    pub gold: u64,
}

/// 一条信封带来的变化（调用方据此决定要不要重绘）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Change {
    /// 与画面无关（心跳、错误、未知消息…）。
    None,
    /// 世界变了：至少一个实体的位置/存在性/自身状态变了。
    World,
}

/// 一条会话的世界状态。
#[derive(Debug, Default, Clone)]
pub struct World {
    /// 自己的实体 id（0 = 还没进世界）。
    pub self_id: u64,
    /// 地图**名字**（本项目地图按名字索引，D-22）。
    pub map_name: String,
    /// 当前地图的**小地图图号**（0 = 该图没有小地图）。
    ///
    /// 服务端从 `MiniMap.txt` 查出来，随 `EnterWorld` / `ChangeMap` 一起下发
    ///（`protocol/scene.proto` 的 `minimap_index`）；客户端拿它当 `mmap` 图库的下标
    ///（**图号 − 1**，与 legacy 的 `ClMain.pas:6045-6051` 同一条规则）。
    pub minimap_index: u32,
    pub self_pos: (i32, i32),
    pub self_dir: i32,
    /// 视野内的实体，键为实体 id（`BTreeMap` ⇒ 遍历顺序稳定，画面不会每帧乱序）。
    pub entities: BTreeMap<u64, Entity>,
    pub ability: Option<Ability>,
    /// 服务端最近一次给的 tick（`EnterWorld` 的 server_tick）。
    pub server_tick: u32,
    /// 累计收到多少条**实体事件**（出现/消失/移动）。
    /// 这是"世界在动"最直接的观测量：联调时看它涨没涨，比盯着画面猜靠谱。
    pub entity_events: u64,
    /// 累计收到多少条**看不懂**的消息（§4.1：记数 + 忽略，不 panic）。
    pub unknown: u64,
    /// 累计收到多少条伤害事件（"在打架"的观测量）。
    pub damage_events: u64,
    /// 累计收到多少条动作（挥砍/受击/死亡）。
    pub actions: u64,
    /// 自己的 hp/max_hp（`EntityHealth` 与 `AbilityUpdate` 都会更新它）。
    ///
    /// ⚠️ 为什么不复用 `ability.hp`：`EntityHealth` **不带等级/金币**，
    /// 往 `ability` 里塞会让"等级=0"这种假值冒出来。两个来源各写各的字段。
    pub self_hp: Option<(u32, u32)>,
    /// 自己是不是死了（回城 / `Revive` 之后由 `ChangeMap` 那条恢复）。
    pub self_dead: bool,
    /// 自己最近一次移动是不是**跑**（与 `Entity::run` 同义，自己不在 `entities` 里）。
    pub self_run: bool,
    /// 自己最近一次动作（挥砍…）—— 自己不在 `entities` 里，所以单列。
    pub self_action: Option<u32>,
    /// 自己的外观（`EnterWorld` / `ChangeMap` 里的 `self_feature`）。
    ///
    /// ⚠️ 它**不在** `entities` 里：快照刻意不含自己（见 `snapshot_drops_self`）。
    /// 但没有它，客户端连"自己长什么样"都不知道（画不出自己的精灵）。
    pub self_feature: Option<proto::EntityFeature>,
    /// 待消费的伤害事件（调用方 `take_damage()` 取走）。
    damage: Vec<DamageEvent>,
}

impl World {
    pub fn in_world(&self) -> bool {
        self.self_id != 0
    }

    /// 应用一条信封。返回值只表示"画面要不要重画"。
    pub fn apply(&mut self, env: &Envelope) -> Change {
        let Some(body) = env.body.as_ref() else {
            // §4.1：未知消息（未来版本新增的）case 未设置 ⇒ 记数 + 忽略，禁止 panic。
            self.unknown += 1;
            return Change::None;
        };
        match body {
            Body::EnterWorld(ew) => {
                self.self_id = ew.self_entity_id;
                self.map_name = ew.map_name.clone();
                self.minimap_index = ew.minimap_index;
                let pos = ew.position.unwrap_or_default();
                self.self_pos = (pos.x, pos.y);
                self.self_dir = ew.direction;
                self.server_tick = ew.server_tick;
                // 自己的外观（服务端单独给，不在 entities 里 —— 见字段说明）
                self.self_feature = ew.self_feature;
                // 初始快照：整份替换（换图/重进都走这里）。
                self.self_dead = false; // 进图（含复活后重新进）自己一定是活的
                self.entities.clear();
                for s in &ew.entities {
                    // ⚠️ 服务端保证快照里没有自己，但客户端也不能因此就假设它一定没有：
                    // 真收到的话忽略掉，免得画面上出现两个"自己"。
                    if s.entity_id == self.self_id {
                        continue;
                    }
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                self.entity_events += 0; // 快照不算"事件"
                Change::World
            }
            Body::ChangeMap(cm) => {
                self.map_name = cm.map_name.clone();
                self.minimap_index = cm.minimap_index; // 换图 = 换一张缩略图
                self.self_feature = cm.self_feature;
                // 回城/传送（含死亡回城）走的就是这条 ⇒ 自己恢复为活着的。
                self.self_dead = false;
                let pos = cm.position.unwrap_or_default();
                self.self_pos = (pos.x, pos.y);
                self.entities.clear();
                for s in &cm.entities {
                    if s.entity_id == self.self_id {
                        continue;
                    }
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                Change::World
            }
            Body::EntityAppear(a) => {
                let Some(s) = a.entity.as_ref() else {
                    self.unknown += 1;
                    return Change::None;
                };
                // 自己"出现"在别的实体事件里是正常的（别人看见我）；自己那条只更新自身。
                if s.entity_id == self.self_id {
                    let pos = s.position.unwrap_or_default();
                    self.self_pos = (pos.x, pos.y);
                    self.self_dir = s.direction;
                } else {
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                self.entity_events += 1;
                Change::World
            }
            Body::EntityDisappear(d) => {
                if d.entity_id == self.self_id {
                    // 自己"消失"不该发生（下线是断连）。记数不崩。
                    self.unknown += 1;
                    return Change::None;
                }
                self.entities.remove(&d.entity_id);
                self.entity_events += 1;
                Change::World
            }
            Body::EntityMove(m) => {
                let to = m.to.unwrap_or_default();
                if m.entity_id == self.self_id {
                    // 自己的权威回显：客户端预测过，这里以服务端为准。
                    self.self_pos = (to.x, to.y);
                    self.self_run = m.run;
                    if m.direction != proto::Direction::Unspecified as i32 {
                        self.self_dir = m.direction;
                    }
                } else if let Some(e) = self.entities.get_mut(&m.entity_id) {
                    e.set_pos(to.x, to.y, m.direction, m.run);
                } else {
                    // ⚠️ 没见过的实体在移动：**不要凭空造一个**（那会造出没有名字/外观的幽灵）。
                    // 正常不会发生（先出现后移动），发生了就记数——这通常是服务端的账本有漏。
                    self.unknown += 1;
                    return Change::None;
                }
                self.entity_events += 1;
                Change::World
            }
            Body::AbilityUpdate(a) => {
                if let Some(ab) = a.ability.as_ref() {
                    self.ability = Some(Ability {
                        level: ab.level,
                        hp: ab.hp,
                        max_hp: ab.max_hp,
                        mp: ab.mp,
                        max_mp: ab.max_mp,
                        gold: ab.gold,
                    });
                    self.self_hp = Some((ab.hp, ab.max_hp));
                    return Change::World;
                }
                Change::None
            }
            Body::LevelUp(l) => {
                // 升级带**完整能力值**（一条消息顶 legacy 的 SM_LEVELUP + SM_ABILITY 两条）。
                if let Some(ab) = l.ability.as_ref() {
                    self.ability = Some(Ability {
                        level: ab.level,
                        hp: ab.hp,
                        max_hp: ab.max_hp,
                        mp: ab.mp,
                        max_mp: ab.max_mp,
                        gold: ab.gold,
                    });
                    self.self_hp = Some((ab.hp, ab.max_hp));
                    return Change::World;
                }
                Change::None
            }
            Body::EntityHealth(h) => {
                if h.entity_id == self.self_id {
                    // 自己的血量：**单独存一份**，不往 `ability` 里塞 —— 因为
                    // `EntityHealth` 不带等级/金币，拿它去凑一个"半个 Ability"会让
                    // 等级显示成 0。两边都写 `self_hp`，谁先到都不丢。
                    self.self_hp = Some((h.hp, h.max_hp));
                } else if let Some(e) = self.entities.get_mut(&h.entity_id) {
                    e.hp = h.hp;
                    e.max_hp = h.max_hp;
                } else {
                    self.unknown += 1;
                    return Change::None;
                }
                Change::World
            }
            Body::Damage(d) => {
                self.damage.push(DamageEvent {
                    attacker_id: d.attacker_id,
                    target_id: d.target_id,
                    value: d.value,
                    flags: d.flags,
                });
                self.damage_events += 1;
                Change::World
            }
            Body::Death(x) => {
                if x.entity_id == self.self_id {
                    // 自己死了：位置与血量由 `ChangeMap`（回城）/`EntityHealth` 收尾，
                    // 这里只记数，不去动 `entities`。
                    self.self_dead = true;
                    return Change::World;
                }
                match self.entities.get_mut(&x.entity_id) {
                    Some(e) => {
                        // ⚠️ 只标记、**不删**：尸骨会留一会儿（原版还能打尸骨），
                        // 真正的移除是随后那条 `EntityDisappear`。
                        e.dead = true;
                        e.action = Some(action::DEATH);
                    }
                    None => {
                        self.unknown += 1;
                        return Change::None;
                    }
                }
                Change::World
            }
            Body::EntityAction(a) => {
                self.actions += 1;
                if let Some(e) = self.entities.get_mut(&a.entity_id) {
                    e.action = Some(a.action);
                } else if a.entity_id == self.self_id {
                    // 自己的动作（挥砍）—— 自己不在 `entities` 里，单独存一份。
                    self.self_action = Some(a.action);
                } else {
                    self.unknown += 1;
                    return Change::None;
                }
                Change::World
            }
            _ => Change::None,
        }
    }

    /// 取走累计的伤害事件（渲染层用来弹伤害数字）。
    ///
    /// 取走而非"读后保留"，是因为调用方**每帧**都会来取一次，留着只会无限膨胀。
    pub fn take_damage(&mut self) -> Vec<DamageEvent> {
        std::mem::take(&mut self.damage)
    }
}

/// 动作 id 的值域（与 `protocol.md` §9.5 一致：1..8 与 `AttackAction` 同值）。
pub mod action {
    /// 受击。
    pub const HURT: u32 = 51;
    /// 死亡。
    pub const DEATH: u32 = 52;
    /// 「这个动作是不是攻击」（1..8 都算）。
    pub fn is_attack(a: u32) -> bool {
        (1..=8).contains(&a)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use proto::envelope::Body;

    fn env(body: Body) -> Envelope {
        Envelope {
            seq: 1,
            ack_seq: 0,
            request_id: 0,
            body: Some(body),
        }
    }

    fn state(id: u64, kind: u32, name: &str, x: i32, y: i32, dir: i32) -> proto::EntityState {
        proto::EntityState {
            entity_id: id,
            kind,
            name: name.into(),
            position: Some(proto::Vec2 { x, y }),
            direction: dir,
            ..Default::default()
        }
    }

    fn enter_world() -> Body {
        Body::EnterWorld(proto::EnterWorld {
            self_entity_id: 1,
            map_id: 0,
            map_name: "0".into(),
            position: Some(proto::Vec2 { x: 1, y: 1 }),
            direction: proto::Direction::DirRight as i32,
            entities: vec![
                state(1_000_001, 1, "鸡", 3, 2, proto::Direction::DirDown as i32), // 自己？不是
                state(7, 0, "别人", 5, 5, proto::Direction::DirUp as i32),
            ],
            server_tick: 42,
            self_feature: None,
            // 小地图图号：0 号图在 `MiniMap.txt` 里是 101（图库下标 = 100）
            minimap_index: 101,
        })
    }

    #[test]
    fn enter_world_builds_snapshot() {
        let mut w = World::default();
        assert_eq!(w.apply(&env(enter_world())), Change::World);
        assert!(w.in_world());
        assert_eq!((w.self_id, w.map_name.as_str()), (1, "0"));
        assert_eq!(w.self_pos, (1, 1));
        // 小地图图号要跟着进世界一起到（TAB 小地图靠它取图，见 `World::minimap_index`）
        assert_eq!(w.minimap_index, 101, "进世界该带上小地图图号");
        assert_eq!(w.server_tick, 42);
        assert_eq!(w.entities.len(), 2);
        assert_eq!(w.entities[&1_000_001].name, "鸡");
        // 快照本身不算"实体事件"（它是状态，不是增量）。
        assert_eq!(w.entity_events, 0);
    }

    #[test]
    fn snapshot_drops_self_if_server_ever_sends_it() {
        let mut w = World::default();
        let Body::EnterWorld(mut ew) = enter_world() else {
            unreachable!()
        };
        ew.entities
            .push(state(1, 0, "自己", 1, 1, proto::Direction::DirRight as i32));
        w.apply(&env(Body::EnterWorld(ew)));
        assert!(!w.entities.contains_key(&1), "画面上不该出现两个「自己」");
        assert_eq!(w.entities.len(), 2);
    }

    #[test]
    fn appear_move_disappear() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 出现
        w.apply(&env(Body::EntityAppear(proto::EntityAppear {
            entity: Some(state(
                1_000_002,
                1,
                "鹿",
                4,
                4,
                proto::Direction::DirLeft as i32,
            )),
        })));
        assert_eq!(w.entities.len(), 3);
        assert_eq!(w.entities[&1_000_002].x, 4);

        // 移动（只给 from/to + 朝向）
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 1_000_002,
            from: Some(proto::Vec2 { x: 4, y: 4 }),
            to: Some(proto::Vec2 { x: 5, y: 4 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 1,
            run: false,
        })));
        let e = &w.entities[&1_000_002];
        assert_eq!((e.x, e.y, e.dir), (5, 4, proto::Direction::DirRight as i32));
        // 名字/类别不能被移动消息抹掉（它只带位置）
        assert_eq!((e.name.as_str(), e.kind), ("鹿", 1));

        assert_eq!(w.entity_events, 2, "出现 + 移动 = 2 条实体事件");

        // 消失
        w.apply(&env(Body::EntityDisappear(proto::EntityDisappear {
            entity_id: 1_000_002,
            reason: proto::DisappearReason::DisappearLeftView as i32,
        })));
        assert!(!w.entities.contains_key(&1_000_002));
        assert_eq!(w.entity_events, 3);
    }

    #[test]
    fn self_move_updates_self_only() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 1,
            from: Some(proto::Vec2 { x: 1, y: 1 }),
            to: Some(proto::Vec2 { x: 2, y: 1 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 2,
            run: true,
        })));
        assert_eq!(w.self_pos, (2, 1));
        assert_eq!(w.entities.len(), 2, "自己的移动不该往实体表里塞东西");
        // "这一步是跑的"也要落到自己身上（渲染层靠它播 ActRun）
        assert!(w.self_run);
        // ... 而**不是**塞进实体表（自己不在表里）
        assert_eq!(w.self_action, None);
    }

    /// `run` 是**这一步**的属性：别人跑步、原地转身，两种都要传对。
    ///
    /// ⚠️ 用户 2026-10-08 报的"跑起来不像跑"根因就在这里：`EntityMove` 原先没有这个字段，
    /// 客户端只能一律播 `ActWalk`（而两段图差 64，看着就是另一套动作）。
    #[test]
    fn 移动的跑标记来自服务端() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        // 别人（快照里的 7 号）跑一步
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 7,
            from: Some(proto::Vec2 { x: 5, y: 5 }),
            to: Some(proto::Vec2 { x: 7, y: 5 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 3,
            run: true,
        })));
        let e = &w.entities[&7];
        assert_eq!((e.x, e.y, e.run), (7, 5, true), "跑一步该带上 run");

        // 同一实体改成走一步 ⇒ 标记要跟着变（它是"这一步"的属性，不是"这个人"的属性）
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 7,
            from: Some(proto::Vec2 { x: 7, y: 5 }),
            to: Some(proto::Vec2 { x: 8, y: 5 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 4,
            run: false,
        })));
        let e = &w.entities[&7];
        assert_eq!((e.x, e.run), (8, false), "走一步必须把 run 抹掉");
    }

    #[test]
    fn move_of_unknown_entity_is_counted_not_invented() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 999,
            from: Some(proto::Vec2 { x: 0, y: 0 }),
            to: Some(proto::Vec2 { x: 1, y: 0 }),
            direction: 0,
            server_tick: 1,
            run: false,
        })));
        assert!(
            !w.entities.contains_key(&999),
            "不该凭空造一个没有名字的实体"
        );
        assert_eq!(w.unknown, 1, "要记数（这通常是服务端的账本有漏）");
    }

    #[test]
    fn unknown_and_control_messages_do_not_touch_world() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        let before = w.clone();
        assert_eq!(w.apply(&Envelope::default()), Change::None); // oneof 未设置
        assert_eq!(
            w.apply(&env(Body::Ping(proto::Ping { client_time_ms: 1 }))),
            Change::None
        );
        assert_eq!(
            w.apply(&env(Body::Raw(proto::Raw {
                msg_id: 1,
                body: vec![]
            }))),
            Change::None
        );
        assert_eq!(w.unknown, 1);
        assert_eq!(w.entities, before.entities);
    }

    #[test]
    fn ability_update() {
        let mut w = World::default();
        w.apply(&env(Body::AbilityUpdate(proto::AbilityUpdate {
            ability: Some(proto::Ability {
                level: 7,
                hp: 30,
                max_hp: 40,
                mp: 5,
                max_mp: 9,
                gold: 123,
                ..Default::default()
            }),
        })));
        let a = w.ability.expect("能力值");
        assert_eq!(
            (a.level, a.hp, a.max_hp, a.mp, a.max_mp, a.gold),
            (7, 30, 40, 5, 9, 123)
        );
    }

    /// 自己的外观由 `self_feature` **单独**给（快照里不含自己）—— 客户端画自己要用它。
    #[test]
    fn self_feature_comes_from_snapshot() {
        let mut w = World::default();
        let Body::EnterWorld(mut ew) = enter_world() else {
            panic!("enter_world() 应给出 EnterWorld");
        };
        ew.self_feature = Some(proto::EntityFeature {
            dress: 12,
            weapon: 3,
            ..Default::default()
        });
        w.apply(&env(Body::EnterWorld(ew)));
        let f = w.self_feature.as_ref().expect("进图应带上自己的外观");
        assert_eq!((f.dress, f.weapon), (12, 3));

        // 换图会重发一次（装备/性别显示都可能变了）
        w.apply(&env(Body::ChangeMap(proto::ChangeMap {
            map_name: "1".into(),
            self_feature: Some(proto::EntityFeature {
                dress: 14,
                ..Default::default()
            }),
            ..Default::default()
        })));
        assert_eq!(w.self_feature.unwrap().dress, 14);
    }

    #[test]
    fn combat_damage_health_death() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 伤害：记进待取事件，**取走之后要清空**（否则每帧重复弹字）
        w.apply(&env(Body::Damage(proto::Damage {
            attacker_id: 1,
            target_id: 1_000_001,
            value: 7,
            flags: 0,
        })));
        let ev = w.take_damage();
        assert_eq!(ev.len(), 1);
        assert_eq!(
            (ev[0].attacker_id, ev[0].target_id, ev[0].value),
            (1, 1_000_001, 7)
        );
        assert!(w.take_damage().is_empty(), "take_damage 必须把队列清空");
        assert_eq!(w.damage_events, 1);

        // 血量
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 1_000_001,
            hp: 8,
            max_hp: 15,
        })));
        assert_eq!(
            (w.entities[&1_000_001].hp, w.entities[&1_000_001].max_hp),
            (8, 15)
        );

        // 死亡：**只标记、不删**（尸骨要留一会儿，真移除是随后的 EntityDisappear）
        w.apply(&env(Body::Death(proto::Death {
            entity_id: 1_000_001,
            killer_id: 1,
        })));
        assert!(w.entities[&1_000_001].dead);
        assert_eq!(w.entities[&1_000_001].action, Some(action::DEATH));
        assert_eq!(w.entities.len(), 2, "死亡不等于消失");

        // 消失才是移除
        w.apply(&env(Body::EntityDisappear(proto::EntityDisappear {
            entity_id: 1_000_001,
            reason: proto::DisappearReason::DisappearDead as i32,
        })));
        assert_eq!(w.entities.len(), 1);
    }

    #[test]
    fn self_health_and_action_are_tracked_separately() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 自己的血量单独存：不能用 EntityHealth 去凑一个"半个 ability"（会显示 0 级）
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 1,
            hp: 30,
            max_hp: 40,
        })));
        assert_eq!(w.self_hp, Some((30, 40)));
        assert!(w.ability.is_none(), "别为了 hp 造一个等级=0 的能力值");

        // 能力值到达时两边都要更新
        w.apply(&env(Body::AbilityUpdate(proto::AbilityUpdate {
            ability: Some(proto::Ability {
                level: 7,
                hp: 25,
                max_hp: 40,
                ..Default::default()
            }),
        })));
        assert_eq!(w.self_hp, Some((25, 40)));
        assert_eq!(w.ability.unwrap().level, 7);

        // 自己的动作（挥砍）：自己不在 entities 里 ⇒ 单列
        w.apply(&env(Body::EntityAction(proto::EntityAction {
            entity_id: 1,
            action: 1,
            server_tick: 9,
        })));
        assert_eq!(w.self_action, Some(1));
        assert_eq!(w.actions, 1);
    }

    #[test]
    fn level_up_carries_full_ability() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::LevelUp(proto::LevelUp {
            level: 8,
            ability: Some(proto::Ability {
                level: 8,
                hp: 50,
                max_hp: 60,
                mp: 20,
                max_mp: 30,
                gold: 999,
                ..Default::default()
            }),
        })));
        let ab = w.ability.expect("升级要带完整能力值");
        assert_eq!((ab.level, ab.max_hp, ab.max_mp, ab.gold), (8, 60, 30, 999));
        assert_eq!(w.self_hp, Some((50, 60)));
    }

    #[test]
    fn health_action_of_unknown_entity_are_counted() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        let before = w.clone();
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 999,
            hp: 1,
            max_hp: 1,
        })));
        w.apply(&env(Body::EntityAction(proto::EntityAction {
            entity_id: 999,
            action: 1,
            server_tick: 1,
        })));
        w.apply(&env(Body::Death(proto::Death {
            entity_id: 999,
            killer_id: 0,
        })));
        assert_eq!(w.unknown, 3, "没见过的实体不该被凭空造出来，但要记数");
        assert_eq!(w.entities.len(), before.entities.len());
    }

    #[test]
    fn action_id_space() {
        // 1..8 = 攻击（与 AttackAction 同值），51/52 是非攻击 —— 见 protocol.md §9.5
        for a in 1..=8 {
            assert!(action::is_attack(a), "动作 {a} 应判为攻击");
        }
        assert!(!action::is_attack(action::HURT));
        assert!(!action::is_attack(action::DEATH));
        assert_ne!(action::HURT, action::DEATH);
    }
}

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
    pub status_bits: u64,
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
            status_bits: s.status_bits,
        }
    }

    /// 位置更新（`EntityMove` 只给 from/to，不给 kind/名字 ⇒ 就地改）。
    fn set_pos(&mut self, x: i32, y: i32, dir: i32) {
        self.x = x;
        self.y = y;
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
                let pos = ew.position.unwrap_or_default();
                self.self_pos = (pos.x, pos.y);
                self.self_dir = ew.direction;
                self.server_tick = ew.server_tick;
                // 初始快照：整份替换（换图/重进都走这里）。
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
                    if m.direction != proto::Direction::Unspecified as i32 {
                        self.self_dir = m.direction;
                    }
                } else if let Some(e) = self.entities.get_mut(&m.entity_id) {
                    e.set_pos(to.x, to.y, m.direction);
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
                    return Change::World;
                }
                Change::None
            }
            _ => Change::None,
        }
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
        })
    }

    #[test]
    fn enter_world_builds_snapshot() {
        let mut w = World::default();
        assert_eq!(w.apply(&env(enter_world())), Change::World);
        assert!(w.in_world());
        assert_eq!((w.self_id, w.map_name.as_str()), (1, "0"));
        assert_eq!(w.self_pos, (1, 1));
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
        })));
        assert_eq!(w.self_pos, (2, 1));
        assert_eq!(w.entities.len(), 2, "自己的移动不该往实体表里塞东西");
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
}

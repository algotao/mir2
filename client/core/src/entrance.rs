//! 进世界的**握手流程**（纯状态机）：喂进收到的信封，吐出下一条要发的消息。
//!
//! 为什么单独拎出来：§5 的序列（`Reconnect → ListCharacters → SelectCharacter → EnterWorld`）
//! 是**每一步都依赖上一步应答**的对话。`client/app` 与 `client/e2e` 都要走它 ——
//! 各写一遍就等着两边漂移（比如一边忘了先等 `CharacterList` 就发 `SelectCharacter`）。
//!
//! 这里不碰网络：调用方负责"把 `next()` 吐出的消息发出去、把收到的信封喂回来"。
//!
//! ⚠️ v0 的入口是 `Reconnect`（认领**既有会话**），不是 `Login` ——
//! 口令怎么过网络还没定（见 D-24），所以这里不实现它。
//!
//! [D-24]: ../../../docs/decisions.md

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

/// 握手走到了哪一步。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Stage {
    /// 等着发第一条（`Reconnect`）。
    Start,
    /// 已发 `Reconnect`，等 `ReconnectResult`。
    AwaitReconnect,
    /// 已发 `ListCharacters`，等 `CharacterList`。
    AwaitList,
    /// 已发 `SelectCharacter`，等 `SelectCharacterResult`（之后服务端会推 `EnterWorld`）。
    AwaitSelect,
    /// 进世界了（收到 `EnterWorld`）。
    InWorld,
    /// 走不下去（服务端明确拒绝 / 协议错）。
    Failed(String),
}

/// 进世界的握手状态机。
pub struct Entrance {
    session: i32,
    /// 想选哪个角色（`None` = 列表里第一个）。
    want_char: Option<u64>,
    /// 上一次 `CharacterList` 里第一个角色的 id（`want_char` 为空时用它）。
    first_char: Option<u64>,
    stage: Stage,
}

impl Entrance {
    /// `session` 是 v0 的会话号（由 accountsvc 建立）；`want_char` 为 `None` 时选列表第一个。
    pub fn new(session: i32, want_char: Option<u64>) -> Self {
        Entrance {
            session,
            want_char,
            first_char: None,
            stage: Stage::Start,
        }
    }

    pub fn stage(&self) -> &Stage {
        &self.stage
    }

    pub fn in_world(&self) -> bool {
        self.stage == Stage::InWorld
    }

    pub fn failed(&self) -> Option<&str> {
        match &self.stage {
            Stage::Failed(why) => Some(why),
            _ => None,
        }
    }

    /// 下一条要发的消息（`None` = 现在什么都不用发：在等对端，或已经到站/已失败）。
    ///
    /// 调用方把它发出去即可 —— 也**必须**发出去，状态机不会自己发。
    pub fn next_cmd(&mut self) -> Option<Body> {
        match self.stage.clone() {
            Stage::Start => {
                self.stage = Stage::AwaitReconnect;
                Some(Body::Reconnect(proto::Reconnect {
                    // v0 的临时编码：4 字节小端会话号（正式 token 由 `LoginResult` 签发，
                    // 见 protocol.md §11）。
                    session_token: self.session.to_le_bytes().to_vec(),
                    last_ack_seq: 0,
                }))
            }
            Stage::AwaitReconnect | Stage::AwaitList | Stage::AwaitSelect => None,
            Stage::InWorld | Stage::Failed(_) => None,
        }
    }

    /// 喂进一条收到的信封；返回**接下来要发的**消息（通常是 `None`）。
    ///
    /// 与请求无关的消息（心跳、实体事件…）会被忽略 —— 它们的去处是 `World::apply`。
    pub fn on(&mut self, env: &Envelope) -> Option<Body> {
        match env.body.as_ref() {
            Some(Body::ServerError(se)) => {
                self.stage = Stage::Failed(format!("服务端错误 {}：{}", se.code, se.message));
                None
            }
            Some(Body::Disconnect(d)) => {
                self.stage = Stage::Failed(format!("被服务端断开 {}：{}", d.code, d.reason));
                None
            }
            Some(Body::ReconnectResult(r)) => {
                if self.stage != Stage::AwaitReconnect {
                    return None; // 重复的/意外的，忽略
                }
                match proto::ReconnectStatus::try_from(r.status) {
                    Ok(proto::ReconnectStatus::ReconnectFailed) => {
                        self.stage = Stage::Failed("会话无效（token 被拒）".into());
                        None
                    }
                    // 「租约仍在 ⇒ 直接回世界」：服务端接下来会推 EnterWorld，
                    // 中间不需要我们再发什么。
                    Ok(proto::ReconnectStatus::ReconnectOkInWorld) => {
                        self.stage = Stage::AwaitSelect; // 等 EnterWorld
                        None
                    }
                    // 「回落到选角」：正常的新登录路径。
                    _ => {
                        self.stage = Stage::AwaitList;
                        Some(Body::ListCharacters(proto::ListCharacters {}))
                    }
                }
            }
            Some(Body::CharacterList(l)) => {
                if self.stage != Stage::AwaitList {
                    return None;
                }
                self.first_char = l.characters.first().map(|c| c.character_id);
                let Some(id) = self.want_char.or(self.first_char) else {
                    self.stage = Stage::Failed("这个账号还没有角色".into());
                    return None;
                };
                self.stage = Stage::AwaitSelect;
                Some(Body::SelectCharacter(proto::SelectCharacter {
                    character_id: id,
                }))
            }
            Some(Body::SelectCharacterResult(r)) => {
                if self.stage != Stage::AwaitSelect {
                    return None;
                }
                if r.code != proto::SelectCharCode::SelectCharOk as i32 {
                    self.stage = Stage::Failed(format!(
                        "选角失败：code={} {}（租约被占时服务端会明确拒绝，不顶号）",
                        r.code, r.message
                    ));
                }
                // 成功：等 `EnterWorld`（服务端推），所以这里不发东西。
                None
            }
            Some(Body::EnterWorld(_)) => {
                self.stage = Stage::InWorld;
                None
            }
            _ => None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(body: Body) -> Envelope {
        Envelope {
            seq: 1,
            ack_seq: 0,
            request_id: 0,
            body: Some(body),
        }
    }

    fn reconnect_result(status: proto::ReconnectStatus) -> Envelope {
        env(Body::ReconnectResult(proto::ReconnectResult {
            status: status as i32,
        }))
    }

    #[test]
    fn full_flow_back_to_select() {
        let mut e = Entrance::new(7, None);
        // 第一条：Reconnect
        let first = e.next_cmd().expect("第一条是 Reconnect");
        assert!(matches!(first, Body::Reconnect(_)));
        assert_eq!(e.next_cmd(), None, "发完就等应答，不该重复发");
        assert_eq!(*e.stage(), Stage::AwaitReconnect);

        // 回落到选角 ⇒ 发 ListCharacters
        let next = e.on(&reconnect_result(
            proto::ReconnectStatus::ReconnectBackToSelect,
        ));
        assert!(matches!(next, Some(Body::ListCharacters(_))));
        assert_eq!(*e.stage(), Stage::AwaitList);

        // 角色列表 ⇒ 发 SelectCharacter（选中第一个）
        let next = e.on(&env(Body::CharacterList(proto::CharacterList {
            characters: vec![
                proto::CharacterSummary {
                    character_id: 42,
                    name: "甲".into(),
                    ..Default::default()
                },
                proto::CharacterSummary {
                    character_id: 43,
                    name: "乙".into(),
                    ..Default::default()
                },
            ],
        })));
        match next {
            Some(Body::SelectCharacter(s)) => assert_eq!(s.character_id, 42),
            other => panic!("应发 SelectCharacter，实得 {other:?}"),
        }

        // 选角成功 ⇒ 什么都不发（等服务端推 EnterWorld）
        assert_eq!(
            e.on(&env(Body::SelectCharacterResult(
                proto::SelectCharacterResult {
                    code: proto::SelectCharCode::SelectCharOk as i32,
                    character_id: 42,
                    ..Default::default()
                }
            ))),
            None
        );
        assert!(!e.in_world());
        e.on(&env(Body::EnterWorld(proto::EnterWorld {
            self_entity_id: 1,
            ..Default::default()
        })));
        assert!(e.in_world());
        assert_eq!(e.next_cmd(), None, "到站之后不该再发东西");
    }

    #[test]
    fn reconnect_ok_in_world_skips_select() {
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        // 租约仍在 ⇒ 服务端会直接推 EnterWorld，中间不该有 ListCharacters
        assert_eq!(
            e.on(&reconnect_result(
                proto::ReconnectStatus::ReconnectOkInWorld
            )),
            None
        );
        e.on(&env(Body::EnterWorld(proto::EnterWorld {
            self_entity_id: 9,
            ..Default::default()
        })));
        assert!(e.in_world());
    }

    #[test]
    fn want_char_wins_over_first() {
        let mut e = Entrance::new(7, Some(99));
        e.next_cmd();
        e.on(&reconnect_result(
            proto::ReconnectStatus::ReconnectBackToSelect,
        ));
        match e.on(&env(Body::CharacterList(proto::CharacterList {
            characters: vec![proto::CharacterSummary {
                character_id: 42,
                ..Default::default()
            }],
        }))) {
            Some(Body::SelectCharacter(s)) => assert_eq!(s.character_id, 99, "指定了就用指定的"),
            other => panic!("应发 SelectCharacter，实得 {other:?}"),
        }
    }

    #[test]
    fn failures_are_explicit() {
        // token 被拒
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        e.on(&reconnect_result(proto::ReconnectStatus::ReconnectFailed));
        assert!(e.failed().is_some(), "失败必须显式记下原因");
        assert_eq!(e.next_cmd(), None, "失败之后不再发东西");

        // 账号没角色
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        e.on(&reconnect_result(
            proto::ReconnectStatus::ReconnectBackToSelect,
        ));
        e.on(&env(Body::CharacterList(proto::CharacterList {
            characters: vec![],
        })));
        assert!(e.failed().unwrap().contains("没有角色"));

        // 选角被拒（租约被占）
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        e.on(&reconnect_result(
            proto::ReconnectStatus::ReconnectBackToSelect,
        ));
        e.on(&env(Body::CharacterList(proto::CharacterList {
            characters: vec![proto::CharacterSummary {
                character_id: 42,
                ..Default::default()
            }],
        })));
        e.on(&env(Body::SelectCharacterResult(
            proto::SelectCharacterResult {
                code: proto::SelectCharCode::SelectCharLeaseHeld as i32,
                message: "账号仍在其他游戏连接中".into(),
                ..Default::default()
            },
        )));
        assert!(e.failed().unwrap().contains("选角失败"));

        // 服务端错误 / 断开
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        e.on(&env(Body::ServerError(proto::ServerError {
            code: 2,
            message: "尚未认领会话".into(),
        })));
        assert!(e.failed().unwrap().contains("服务端错误"));
    }

    #[test]
    fn ignores_unrelated_messages() {
        // 心跳/实体事件不该推动握手状态机（它们归 World::apply 管）。
        let mut e = Entrance::new(7, None);
        e.next_cmd();
        assert_eq!(
            e.on(&env(Body::Ping(proto::Ping { client_time_ms: 1 }))),
            None
        );
        assert_eq!(e.on(&Envelope::default()), None);
        assert_eq!(
            *e.stage(),
            Stage::AwaitReconnect,
            "状态不该被无关消息推着走"
        );
    }
}

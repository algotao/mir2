//! 进世界的**握手流程**（纯状态机）：喂进收到的信封，吐出下一条要发的消息。
//!
//! 为什么单独拎出来：§5 的序列（`Reconnect → ListCharacters → SelectCharacter → EnterWorld`）
//! 是**每一步都依赖上一步应答**的对话。`client/app` 与 `client/e2e` 都要走它 ——
//! 各写一遍就等着两边漂移（比如一边忘了先等 `CharacterList` 就发 `SelectCharacter`）。
//!
//! 这里不碰网络：调用方负责"把 `next()` 吐出的消息发出去、把收到的信封喂回来"。
//!
//! 两个入口，走的是**同一条尾巴**（列角色 → 选角 → 进世界）：
//!
//! - `Entrance::new_with_password(account, password)` —— 口令登录（D-24① 挑战应答）：
//!
//!    `LoginSaltRequest` → `LoginSalt` →（客户端算证明）→ `Login` → `LoginResult`
//!
//! - `Entrance::new(session, want_char)` —— 认领**既有会话**（重连 / 已登录过）：
//!
//!    `Reconnect` → `ReconnectResult`
//!
//! ⚠️ 挑战应答的密码学在 [`crate::auth`]（HMAC/PBKDF2 + 公开测试向量）。
//! `Entrance` 只负责**顺序**：先要盐、再发证明、然后接着走选角那条路。
//!
//! [D-24]: ../../../docs/decisions.md

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

/// 握手走到了哪一步。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Stage {
    /// 等着发第一条（`Reconnect` 或 `LoginSaltRequest`）。
    Start,
    /// 已发 `Reconnect`，等 `ReconnectResult`。
    AwaitReconnect,
    /// 已发 `LoginSaltRequest`，等 `LoginSalt`（盐 + 迭代数 + 派生长）。
    AwaitSalt,
    /// 已发 `Login`（口令证明），等 `LoginResult`。
    AwaitLogin,
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
    /// 口令登录那一路：账号与口令（`None` = 走 `Reconnect`）。
    ///
    /// ⚠️ 口令只在内存里活到"算出证明"那一刻（见 `on_salt`），算完就丢掉 ——
    /// 状态机不该长期握着它。
    login: Option<LoginState>,
    /// 握手 nonce（`ServerHello.session_key`），由调用方在收到 `Ev::Connected` 时喂进来。
    ///
    /// ⚠️ 它必须**每条连接一份**：口令证明绑在它上面，所以证明重放不了（D-24①）。
    nonce: Vec<u8>,
    /// 登录成功后服务端签发的会话号（之后重连用它，不必再输口令）。
    session_token: Option<i32>,
}

struct LoginState {
    account: String,
    password: String,
}

impl Entrance {
    /// 认领**既有会话**（重连 / 已有会话号）；`want_char` 为 `None` 时选列表第一个。
    pub fn new(session: i32, want_char: Option<u64>) -> Self {
        Entrance {
            session,
            want_char,
            first_char: None,
            stage: Stage::Start,
            login: None,
            nonce: Vec::new(),
            session_token: None,
        }
    }

    /// 用**口令**登录（D-24① 挑战应答）。
    ///
    /// `want_char` 为 `None` 时选角色列表里的第一个。
    pub fn new_with_password(account: String, password: String, want_char: Option<u64>) -> Self {
        Entrance {
            session: 0,
            want_char,
            first_char: None,
            stage: Stage::Start,
            login: Some(LoginState { account, password }),
            nonce: Vec::new(),
            session_token: None,
        }
    }

    /// 记下握手 nonce（`Ev::Connected` 带回来的）。**登录前必须喂进来**，否则发不出证明。
    pub fn on_nonce(&mut self, nonce: &[u8]) {
        self.nonce = nonce.to_vec();
    }

    /// 登录成功后服务端签发的会话号（`None` = 还没登录成功）。
    ///
    /// 拿到它之后就能走 `Reconnect` 那条路（重连不必再输口令）。
    pub fn session_token(&self) -> Option<i32> {
        self.session_token
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
                if let Some(l) = &self.login {
                    // 口令登录第一步：**先要盐**（客户端拿不到服务端的随机盐，
                    // 就没有 K，也就没有证明 —— 这正是 D-24 当初卡住的地方）
                    self.stage = Stage::AwaitSalt;
                    return Some(Body::LoginSaltRequest(proto::LoginSaltRequest {
                        account: l.account.clone(),
                    }));
                }
                self.stage = Stage::AwaitReconnect;
                Some(Body::Reconnect(proto::Reconnect {
                    // ⚠️ v0 的临时编码：4 字节小端会话号（正式 token 之后由 `LoginResult`
                    // 签发 —— 见下面的 `on_login_result`，那条路已经通了）。
                    session_token: self.session.to_le_bytes().to_vec(),
                    last_ack_seq: 0,
                }))
            }
            Stage::AwaitReconnect | Stage::AwaitSalt | Stage::AwaitLogin => None,
            Stage::AwaitList | Stage::AwaitSelect => None,
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
            Some(Body::LoginSalt(salt)) => self.on_salt(salt),
            Some(Body::LoginResult(r)) => self.on_login_result(r),
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

    /// 收到盐 ⇒ 算出口令证明并发出去（D-24① 的第二步）。
    fn on_salt(&mut self, salt: &proto::LoginSalt) -> Option<Body> {
        if self.stage != Stage::AwaitSalt {
            return None; // 意外的/重复的，忽略
        }
        let Some(l) = self.login.as_mut() else {
            self.stage = Stage::Failed("没在登录流程里却收到了盐".into());
            return None;
        };
        if self.nonce.is_empty() {
            // 没有 nonce 就发不出证明。这属调用方的错（忘了喂 `Ev::Connected` 的 nonce），
            // 明说比"发一个空证明被服务端拒掉"好定位。
            self.stage = Stage::Failed("还没拿到握手 nonce（调用方要先喂 on_nonce）".into());
            return None;
        }
        // K = PBKDF2(口令, 盐, 迭代数, 派生长)；证明 = HMAC(K, nonce‖account)。
        // ⚠️ 迭代数与派生长**用服务端下发的**，客户端不写死 —— 服务端将来调参时
        // 只有它要改（写死的话所有客户端会一起登不上去）。
        let k = crate::auth::pbkdf2_sha256(
            l.password.as_bytes(),
            &salt.salt,
            salt.iterations,
            salt.key_len as usize,
        );
        let proof = crate::auth::proof(&k, &self.nonce, &l.account);
        let account = l.account.clone();
        // 证明算完就把口令丢掉：状态机不该长期握着它（内存转储/日志泄漏面都小一点）
        l.password.clear();
        self.stage = Stage::AwaitLogin;
        Some(Body::Login(proto::Login {
            account,
            password_hash: crate::auth::to_hex(&proof),
            client_build: String::new(),
        }))
    }

    /// 收到登录结果：成功就接着走选角那条尾巴（与 `Reconnect` 的回落路径同一个出口）。
    fn on_login_result(&mut self, r: &proto::LoginResult) -> Option<Body> {
        if self.stage != Stage::AwaitLogin {
            return None;
        }
        if r.code != proto::LoginCode::LoginOk as i32 {
            let code =
                proto::LoginCode::try_from(r.code).unwrap_or(proto::LoginCode::LoginBadCredentials);
            self.stage = Stage::Failed(format!("登录失败（{code:?}）：{}", r.message));
            return None;
        }
        // 服务端签发的会话号（4 字节小端）——记下来，之后重连用它。
        if r.session_token.len() == 4 {
            self.session_token = Some(i32::from_le_bytes([
                r.session_token[0],
                r.session_token[1],
                r.session_token[2],
                r.session_token[3],
            ]));
        }
        self.login = None; // 登录那一路到此结束
        self.stage = Stage::AwaitList;
        Some(Body::ListCharacters(proto::ListCharacters {}))
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

    /// 口令登录的**顺序**：先要盐 → 用服务端给的参数算证明 → 成功后才去列角色。
    ///
    /// ⚠️ 证明必须与 `auth::proof(pbkdf2(...))` 逐字节相同 —— 这条把"顺序"与"算得对"
    /// 一起钉住（算错了服务端会拒，但那只在联调时才看得出来）。
    #[test]
    fn 口令登录的顺序与证明() {
        let nonce = vec![0x10, 0x11, 0x12, 0x13];
        let salt = vec![0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15];
        let mut e = Entrance::new_with_password("tester".into(), "pw".into(), None);
        e.on_nonce(&nonce);

        // 第一步：要盐（而不是 Reconnect！）
        match e.next_cmd().expect("第一条要发") {
            Body::LoginSaltRequest(r) => assert_eq!(r.account, "tester"),
            other => panic!("第一条应是 LoginSaltRequest，实得 {other:?}"),
        }
        assert_eq!(e.stage(), &Stage::AwaitSalt);

        // 第二步：收到盐 ⇒ 发证明
        let body = e
            .on(&env(Body::LoginSalt(proto::LoginSalt {
                salt: salt.clone(),
                iterations: 100_000,
                key_len: 32,
            })))
            .expect("收到盐后要发证明");
        let k = crate::auth::pbkdf2_sha256(b"pw", &salt, 100_000, 32);
        match body {
            Body::Login(l) => {
                assert_eq!(l.account, "tester");
                assert_eq!(
                    l.password_hash,
                    crate::auth::to_hex(&crate::auth::proof(&k, &nonce, "tester"))
                );
            }
            other => panic!("该发 Login，实得 {other:?}"),
        }

        // 第三步：服务端 OK ⇒ 记下会话号 + 接着走选角那条尾巴
        let body = e
            .on(&env(Body::LoginResult(proto::LoginResult {
                code: proto::LoginCode::LoginOk as i32,
                message: String::new(),
                session_token: 7i32.to_le_bytes().to_vec(),
            })))
            .expect("登录成功要接着列角色");
        assert!(matches!(body, Body::ListCharacters(_)));
        assert_eq!(
            e.session_token(),
            Some(7),
            "签发的会话号要记下来（之后重连用它）"
        );
        assert_eq!(e.stage(), &Stage::AwaitList);
    }

    /// 口令错 ⇒ 进 `Failed`（**不是**卡在原地），并且带上服务端的原因。
    #[test]
    fn 口令错就失败() {
        let mut e = Entrance::new_with_password("tester".into(), "bad".into(), None);
        e.on_nonce(&[1, 2, 3]);
        let _ = e.next_cmd();
        let _ = e.on(&env(Body::LoginSalt(proto::LoginSalt {
            salt: vec![9; 16],
            iterations: 1000,
            key_len: 32,
        })));
        let _ = e.on(&env(Body::LoginResult(proto::LoginResult {
            code: proto::LoginCode::LoginBadCredentials as i32,
            message: "账号或口令不正确".into(),
            session_token: Vec::new(),
        })));
        assert!(e.failed().is_some_and(|w| w.contains("账号或口令不正确")));
    }

    /// 没有 nonce 就**发不出**正确证明 ⇒ 明说，而不是发个空证明被服务端拒掉。
    #[test]
    fn 没有_nonce_就登不了() {
        let mut e = Entrance::new_with_password("tester".into(), "pw".into(), None);
        let _ = e.next_cmd();
        assert!(e
            .on(&env(Body::LoginSalt(proto::LoginSalt {
                salt: vec![1; 16],
                iterations: 1000,
                key_len: 32,
            })))
            .is_none());
        assert!(e.failed().is_some_and(|w| w.contains("nonce")));
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

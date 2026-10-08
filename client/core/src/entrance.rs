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
    /// 角色列表到手，**等调用方选**（app 的选角界面）。
    ///
    /// ⚠️ 只有开了 [`Entrance::set_manual_pick`] 才会进这个阶段；默认是
    /// "自动选列表第一个"（e2e / 无头驱动用），那时根本不会停在这里。
    AwaitPick,
    /// 已发 `SelectCharacter`，等 `SelectCharacterResult`（之后服务端会推 `EnterWorld`）。
    AwaitSelect,
    /// 已发 `CreateCharacter`，等 `CreateCharacterResult`（之后要重新拉一次列表）。
    AwaitCreate,
    /// 已发 `DeleteCharacter`，等 `DeleteCharacterResult`（之后要重新拉一次列表）。
    AwaitDelete,
    /// 已发 `LoginSaltRequest`，等盐（**建号**那一路，D-32）。
    AwaitSignupSalt,
    /// 已发 `CreateAccount`（口令校验值），等 `CreateAccountResult`。
    AwaitSignup,
    /// 建号有了结果，**停在原地等调用方**。
    ///
    /// ⚠️ 刻意不自动登录（原版 `SM_NEWID_SUCCESS` 也只是弹个提示，
    /// `ClMain.pas:3684-3691`）：取走提示后由调用方决定是 `begin_login` 还是再建一个。
    SignedUp,
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
    /// 最近一次 `CharacterList` 的**全部**角色（选角界面要显示它）。
    chars: Vec<proto::CharacterSummary>,
    /// 要不要**由调用方选**角色（`true` = app 的选角界面；`false` = 自动选第一个）。
    manual: bool,
    /// 选角失败的原因（如租约被占）。**不致命**：回到 `AwaitPick` 让用户重选，
    /// 调用方取走这条消息弹窗（原版也是弹个 `DMessageDlg` 接着选）。
    pick_err: Option<String>,
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
    /// 建号那一路（D-32）：账号与口令。`None` = 不在建号。
    ///
    /// ⚠️ 口令同样只在内存里活到"算出校验值"那一刻（见 `on_signup_salt`）。
    signup: Option<LoginState>,
    /// 建号结果的提示：`(是否成功, 一句话)`。由调用方取走弹窗（同 `pick_err` 的路数）。
    signup_msg: Option<(bool, String)>,
    /// 登录时算出的**口令证明**（hex）。
    ///
    /// ⚠️ 留它只为删角色的**二次确认**：`DeleteCharacter.password_hash` 要的就是它。
    /// 证明绑在**本条连接的 nonce** 上（D-24①）⇒ 换个连接就失效、重放不了；
    /// 而且它是**证明**不是口令（口令本身在算完那一刻就丢了，见 `on_salt`）。
    proof: Option<String>,
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
            chars: Vec::new(),
            manual: false,
            pick_err: None,
            stage: Stage::Start,
            login: None,
            signup: None,
            signup_msg: None,
            nonce: Vec::new(),
            session_token: None,
            proof: None,
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
            chars: Vec::new(),
            manual: false,
            pick_err: None,
            stage: Stage::Start,
            login: Some(LoginState { account, password }),
            signup: None,
            signup_msg: None,
            nonce: Vec::new(),
            session_token: None,
            proof: None,
        }
    }

    /// 用**新建账号**（D-32）：`LoginSaltRequest` →（客户端算校验值）→ `CreateAccount`。
    ///
    /// ⚠️ 建完**不自动登录**（与原版一致）：走到 [`Stage::SignedUp`] 就停住，
    /// 调用方取走提示、再自己走一次登录（[`Entrance::begin_login`]）。
    pub fn new_for_signup(account: String, password: String) -> Self {
        let mut e = Self::new(0, None);
        e.signup = Some(LoginState { account, password });
        e
    }

    /// 就地开始一次**建号**（同一条连接 ⇒ nonce 不变；建号不碰 nonce，但省得重连）。
    pub fn begin_signup(&mut self, account: String, password: String) {
        self.signup = Some(LoginState { account, password });
        self.login = None;
        self.stage = Stage::Start;
    }

    /// 就地开始一次**口令登录**（`Reconnect` 之外的另一条路）。
    ///
    /// 建号成功后界面就是用它接着登的（不重开连接 ⇒ nonce 还是那一个）。
    pub fn begin_login(&mut self, account: String, password: String) {
        self.login = Some(LoginState { account, password });
        self.signup = None;
        self.stage = Stage::Start;
    }

    /// 取走建号结果的提示：`(是否成功, 一句话)`。没有则 `None`。
    ///
    /// 与 `pick_err` 同一套路数：状态机只记账，弹窗是界面的事。
    pub fn take_signup_msg(&mut self) -> Option<(bool, String)> {
        self.signup_msg.take()
    }

    /// 登录时那条口令证明（hex，没登录过就是 `None`）。
    ///
    /// 删角色要用它做二次确认（见 [`Entrance::delete_character`]）。
    pub fn proof(&self) -> Option<&str> {
        self.proof.as_deref()
    }

    /// **建角色**（选角界面用）。只在停在选角时有效。
    ///
    /// `class` 用新协议的值（1 战士 / 2 法师 / 3 道士），`gender` 用 1 男 / 2 女 ——
    /// 与 `CharacterSummary` 同一套编码，界面不必再翻译一层。
    pub fn create_character(
        &mut self,
        name: &str,
        class: i32,
        gender: i32,
        hair: u32,
    ) -> Option<Body> {
        if self.stage != Stage::AwaitPick {
            return None;
        }
        self.stage = Stage::AwaitCreate;
        Some(Body::CreateCharacter(proto::CreateCharacter {
            name: name.to_string(),
            class,
            gender,
            hair,
        }))
    }

    /// **删角色**（选角界面用）。二次确认用登录时那条证明（[`Entrance::proof`]）。
    ///
    /// 没有证明就发不出去（`None`）—— 与其发一条注定被拒的消息，不如让调用方
    /// 明说"这次没带确认"。
    pub fn delete_character(&mut self, character_id: u64) -> Option<Body> {
        if self.stage != Stage::AwaitPick {
            return None;
        }
        let proof = self.proof.clone()?;
        self.stage = Stage::AwaitDelete;
        Some(Body::DeleteCharacter(proto::DeleteCharacter {
            character_id,
            password_hash: proof,
        }))
    }

    /// 改成"**由调用方选**角色"（app 的选角界面）。
    ///
    /// 默认是自动选列表第一个（e2e 与无头驱动不想为选角多写一层）——
    /// 所以这是一个**显式开关**，而不是"有列表就停"：那样会让 e2e 也停下来等。
    pub fn set_manual_pick(&mut self, on: bool) {
        self.manual = on;
    }

    /// 最近一次角色列表（选角界面显示用）。自动选角模式下也有值。
    pub fn characters(&self) -> &[proto::CharacterSummary] {
        &self.chars
    }

    /// 选角界面的"就是他了"：把用户选的那个告诉状态机。
    ///
    /// 只有停在 [`Stage::AwaitPick`] 时才算数（别处的调用是调用方的 bug ⇒ `None`）。
    pub fn pick(&mut self, character_id: u64) -> Option<Body> {
        if self.stage != Stage::AwaitPick {
            return None;
        }
        self.stage = Stage::AwaitSelect;
        Some(Body::SelectCharacter(proto::SelectCharacter {
            character_id,
        }))
    }

    /// 取走"上一次选角失败的原因"（取走后清空）。
    pub fn take_pick_error(&mut self) -> Option<String> {
        self.pick_err.take()
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
                if let Some(s) = &self.signup {
                    // 建号第一步同样是**先要盐**（D-32 复用 D-24① 的第一步）：
                    // 盐是服务端随机给的，客户端拿不到就算不出与服务端存储一致的 K。
                    self.stage = Stage::AwaitSignupSalt;
                    return Some(Body::LoginSaltRequest(proto::LoginSaltRequest {
                        account: s.account.clone(),
                    }));
                }
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
            // 建号一路也都是"等对端"；`SignedUp` 是**到站**（等界面决定下一步）。
            Stage::AwaitSignupSalt | Stage::AwaitSignup | Stage::SignedUp => None,
            Stage::AwaitList | Stage::AwaitSelect => None,
            // 停在选角：**没有待发命令** —— 要发什么由调用方 `pick()` 决定
            //（这就是"手动选角"的全部机制：把选择权交出去）。
            Stage::AwaitPick => None,
            // 建/删角等回执，也没待发命令（回执到了会自己接上"重拉列表"）
            Stage::AwaitCreate | Stage::AwaitDelete => None,
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
            // 建号结果（D-32）：不管成败都停在 `SignedUp` —— 不自动登录
            //（原版 `SM_NEWID_SUCCESS` 也只是弹个提示），提示给界面，下一步界面定。
            Some(Body::CreateAccountResult(r)) => {
                if self.stage != Stage::AwaitSignup {
                    return None;
                }
                let a = r.result.clone().unwrap_or_default();
                let msg = if !a.message.is_empty() {
                    a.message.clone()
                } else if a.ok {
                    "账号已建立，请登录".to_string()
                } else {
                    format!("建号失败（code={}）", a.code)
                };
                self.signup = None; // 丢掉口令那一份
                self.signup_msg = Some((a.ok, msg));
                self.stage = Stage::SignedUp;
                None
            }
            Some(Body::LoginSalt(salt)) => self.on_salt(salt),
            Some(Body::LoginResult(r)) => self.on_login_result(r),
            Some(Body::CharacterList(l)) => {
                if self.stage != Stage::AwaitList {
                    return None;
                }
                self.first_char = l.characters.first().map(|c| c.character_id);
                self.chars = l.characters.clone();
                if self.manual {
                    // 交给调用方选（列表为空也停在这里：界面上还有"新建角色"一条路，
                    // 直接判死会把用户堵死 —— 这是有意的行为差异，见 `set_manual_pick`）
                    self.stage = Stage::AwaitPick;
                    return None;
                }
                let Some(id) = self.want_char.or(self.first_char) else {
                    self.stage = Stage::Failed("这个账号还没有角色".into());
                    return None;
                };
                self.stage = Stage::AwaitSelect;
                Some(Body::SelectCharacter(proto::SelectCharacter {
                    character_id: id,
                }))
            }
            // 建/删角色：不管成败都**退回去重新拉列表**（原版也是"建完要重查"，
            // `boChrQueryed := False`）。失败时把原因留在 `pick_err` 里给界面弹窗。
            Some(Body::CreateCharacterResult(r)) => {
                if self.stage != Stage::AwaitCreate {
                    return None;
                }
                if !r.result.as_ref().is_some_and(|a| a.ok) {
                    let a = r.result.clone().unwrap_or_default();
                    self.pick_err = Some(describe_action_error("建角", &a));
                }
                self.stage = Stage::AwaitList;
                Some(Body::ListCharacters(proto::ListCharacters {}))
            }
            Some(Body::DeleteCharacterResult(r)) => {
                if self.stage != Stage::AwaitDelete {
                    return None;
                }
                if !r.result.as_ref().is_some_and(|a| a.ok) {
                    let a = r.result.clone().unwrap_or_default();
                    self.pick_err = Some(describe_action_error("删角", &a));
                }
                self.stage = Stage::AwaitList;
                Some(Body::ListCharacters(proto::ListCharacters {}))
            }
            Some(Body::SelectCharacterResult(r)) => {
                if self.stage != Stage::AwaitSelect {
                    return None;
                }
                if r.code != proto::SelectCharCode::SelectCharOk as i32 {
                    let why = format!(
                        "选角失败：code={} {}（租约被占时服务端会明确拒绝，不顶号）",
                        r.code, r.message
                    );
                    // 手动选角：**不判死**，退回去让用户换一个（原版也是弹个框接着选）。
                    // 自动选角（e2e）：保持原来的"直接 Failed"，别把错误吞掉。
                    if self.manual {
                        self.pick_err = Some(why);
                        self.stage = Stage::AwaitPick;
                    } else {
                        self.stage = Stage::Failed(why);
                    }
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
        if self.stage == Stage::AwaitSignupSalt {
            return self.on_signup_salt(salt);
        }
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
        // ⚠️ 证明留一份（hex）：删角色的二次确认要的就是它。
        // 口令本身仍然在下面这一句之后丢掉（`LoginState` 不再被读）—— 留的是**证明**。
        self.proof = Some(crate::auth::to_hex(&proof));
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

    /// 建号的盐回来了 ⇒ 算 `K` 并把**校验值**发出去（D-32）。
    ///
    /// ⚠️ 这里发的 `hex(K)` 是**口令的等价物**，与登录路径"只发绑在 nonce 上的证明"
    /// 不同 —— 建号时服务端手里什么都没有，它得拿到一个以后能验登录的值才能落库。
    /// 协议注释（`account.proto` 的 `CreateAccount`）把这条差别写在明面上。
    fn on_signup_salt(&mut self, salt: &proto::LoginSalt) -> Option<Body> {
        let Some(s) = self.signup.as_mut() else {
            self.stage = Stage::Failed("没在建号流程里却收到了盐".into());
            return None;
        };
        // 迭代数与派生长**用服务端下发的**（同登录：客户端不写死）。
        let k = crate::auth::pbkdf2_sha256(
            s.password.as_bytes(),
            &salt.salt,
            salt.iterations,
            salt.key_len as usize,
        );
        let account = s.account.clone();
        // 口令用完就丢（与登录同一条纪律：状态机不该长期握着它）
        s.password.clear();
        self.stage = Stage::AwaitSignup;
        Some(Body::CreateAccount(proto::CreateAccount {
            account,
            verifier: crate::auth::to_hex(&k),
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

/// 把 `ActionResult` 翻成给人看的一句话（建/删角失败时弹给用户）。
///
/// ⚠️ 服务端的 `code` 是**我们定的**（协议里只写了"非 0 见各消息的注释"），
/// 两边的取值约定写在 `gamesvr/netproto.go` 那组常量上 —— 这里只做兜底描述，
/// 真正的解释用服务端给的 `message`（它更具体，比如"这个名字已经有人用了"）。
fn describe_action_error(what: &str, a: &proto::ActionResult) -> String {
    if !a.message.is_empty() {
        return format!("{what}失败：{}（code={}）", a.message, a.code);
    }
    format!("{what}失败：code={}", a.code)
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

    /// 一份角色列表（两个角色）。
    fn char_list(ids: &[u64]) -> Envelope {
        env(Body::CharacterList(proto::CharacterList {
            characters: ids
                .iter()
                .map(|&id| proto::CharacterSummary {
                    character_id: id,
                    name: format!("角色{id}"),
                    level: 7,
                    ..Default::default()
                })
                .collect(),
        }))
    }

    /// 走到"角色列表到手"那一步（口令登录那条路太绕，这里用 Reconnect 那条）。
    fn to_list(manual: bool) -> Entrance {
        let mut e = Entrance::new(7, None);
        if manual {
            e.set_manual_pick(true);
        }
        e.next_cmd().expect("Reconnect");
        e.on(&reconnect_result(
            proto::ReconnectStatus::ReconnectBackToSelect,
        ));
        e
    }

    /// **手动选角**：列表到手后停住等用户，`pick` 才发 `SelectCharacter`。
    ///
    /// 这是选角界面的地基：没有它，界面上的"选哪个"根本来不及发生
    /// （状态机会自己把第一个选掉）。
    #[test]
    fn 手动选角_停在选角阶段() {
        let mut e = to_list(true);
        assert_eq!(e.on(&char_list(&[11, 22])), None, "手动模式不该自己选");
        assert_eq!(*e.stage(), Stage::AwaitPick);
        assert_eq!(
            e.characters()
                .iter()
                .map(|c| c.character_id)
                .collect::<Vec<_>>(),
            vec![11, 22],
            "列表要留给界面显示"
        );

        // 用户选了**第二个**
        let cmd = e.pick(22).expect("该吐出 SelectCharacter");
        match cmd {
            Body::SelectCharacter(sc) => assert_eq!(sc.character_id, 22),
            other => panic!("期望 SelectCharacter，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitSelect);
        // 只能选一次
        assert!(e.pick(11).is_none(), "已经在等了，不该再发一条");
    }

    /// 自动模式（e2e / 无头驱动）**不受影响**：还是选列表第一个。
    #[test]
    fn 自动选角照旧() {
        let mut e = to_list(false);
        let cmd = e.on(&char_list(&[11, 22])).expect("自动模式该自己选");
        match cmd {
            Body::SelectCharacter(sc) => assert_eq!(sc.character_id, 11, "自动选第一个"),
            other => panic!("期望 SelectCharacter，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitSelect);
    }

    /// 手动模式下**空列表不判死**：界面上还有"新建角色"一条路，
    /// 直接 Failed 会把用户堵死在选角界面（而且连弹窗都出不来）。
    #[test]
    fn 手动选角_空列表不判死() {
        let mut e = to_list(true);
        assert_eq!(e.on(&char_list(&[])), None);
        assert_eq!(*e.stage(), Stage::AwaitPick);
        assert!(e.characters().is_empty());
        assert!(e.failed().is_none(), "空列表在手动模式下不是错误");
    }

    /// 选角被拒（比如租约被占）⇒ **退回选角**让人换一个，而不是判死。
    #[test]
    fn 选角失败退回选角() {
        let mut e = to_list(true);
        e.on(&char_list(&[11, 22]));
        e.pick(11);
        let rejected = env(Body::SelectCharacterResult(proto::SelectCharacterResult {
            code: proto::SelectCharCode::SelectCharLeaseHeld as i32,
            message: "已被占用".into(),
            character_id: 11,
        }));
        assert_eq!(e.on(&rejected), None);
        assert_eq!(*e.stage(), Stage::AwaitPick, "该退回选角让用户换一个");
        let why = e.take_pick_error().expect("要说清为什么被拒");
        assert!(why.contains("租约") || why.contains("选角失败"), "{why}");
        assert!(e.take_pick_error().is_none(), "取走后就该清空");
        assert!(e.failed().is_none(), "这不是致命错误");

        // 换一个还能继续（服务端才是权威，这里不该自己拦）
        assert!(e.pick(22).is_some());
    }

    /// 自动模式下选角失败**仍然是致命**的（保持原行为，别把错误吞掉）。
    #[test]
    fn 自动选角失败仍判死() {
        let mut e = to_list(false);
        e.on(&char_list(&[11]));
        let rejected = env(Body::SelectCharacterResult(proto::SelectCharacterResult {
            code: proto::SelectCharCode::SelectCharLeaseHeld as i32,
            message: "已被占用".into(),
            character_id: 11,
        }));
        e.on(&rejected);
        assert!(e.failed().is_some());
    }

    /// 建号走的是 D-24① 的同一条取盐链，但第二步发的是**校验值**（D-32）。
    #[test]
    fn 建号_取盐后发校验值() {
        let mut e = Entrance::new_for_signup("newbie".into(), "pw123".into());
        // 第一步：要盐
        match e.next_cmd().expect("该吐出 LoginSaltRequest") {
            Body::LoginSaltRequest(r) => assert_eq!(r.account, "newbie"),
            other => panic!("期望 LoginSaltRequest，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitSignupSalt);

        // 盐回来 ⇒ 吐 CreateAccount，校验值 = hex(PBKDF2(口令, 盐, 迭代, 派生长))
        let salt = proto::LoginSalt {
            salt: vec![1, 2, 3, 4],
            iterations: 1000,
            key_len: 32,
        };
        let cmd = e
            .on(&env(Body::LoginSalt(salt.clone())))
            .expect("该吐出 CreateAccount");
        let want = crate::auth::to_hex(&crate::auth::pbkdf2_sha256(
            b"pw123",
            &salt.salt,
            salt.iterations,
            salt.key_len as usize,
        ));
        match cmd {
            Body::CreateAccount(c) => {
                assert_eq!(c.account, "newbie");
                assert_eq!(c.verifier, want, "校验值必须与服务端存储的那一份算法一致");
            }
            other => panic!("期望 CreateAccount，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitSignup);
        // 口令用完就丢：状态机不长期握着它
        assert!(e.signup.as_ref().is_some_and(|s| s.password.is_empty()));
    }

    /// 建号回执：**不自动登录**，只留一条提示；随后由界面接着登（同一条连接）。
    #[test]
    fn 建号回执_只给提示不自动登录() {
        let mut e = Entrance::new_for_signup("newbie".into(), "pw123".into());
        e.next_cmd();
        e.on(&env(Body::LoginSalt(proto::LoginSalt {
            salt: vec![9; 16],
            iterations: 1000,
            key_len: 32,
        })));

        let sent = e.on(&env(Body::CreateAccountResult(
            proto::CreateAccountResult {
                result: Some(proto::ActionResult {
                    ok: true,
                    code: 0,
                    message: "账号已建立，请登录".into(),
                }),
            },
        )));
        assert!(sent.is_none(), "建号成功不该顺带发登录");
        assert_eq!(*e.stage(), Stage::SignedUp);
        assert!(e.next_cmd().is_none(), "到站之后没有待发命令");
        assert_eq!(
            e.take_signup_msg(),
            Some((true, "账号已建立，请登录".into()))
        );
        assert!(e.take_signup_msg().is_none(), "提示取走一次就没了");

        // 接着登录：同一条连接（nonce 不变）继续走 D-24① 那一路
        e.begin_login("newbie".into(), "pw123".into());
        assert!(matches!(e.next_cmd(), Some(Body::LoginSaltRequest(_))));
        assert_eq!(*e.stage(), Stage::AwaitSalt);
    }

    /// 建号失败也一样**停住**（不断连接、不判死）—— 用户还要重填或去登录。
    #[test]
    fn 建号失败_停住不判死() {
        let mut e = Entrance::new_for_signup("newbie".into(), "pw123".into());
        e.next_cmd();
        e.on(&env(Body::LoginSalt(proto::LoginSalt {
            salt: vec![9; 16],
            iterations: 1000,
            key_len: 32,
        })));
        e.on(&env(Body::CreateAccountResult(
            proto::CreateAccountResult {
                result: Some(proto::ActionResult {
                    ok: false,
                    code: 1,
                    message: "这个账号已经被使用了".into(),
                }),
            },
        )));
        assert_eq!(*e.stage(), Stage::SignedUp, "失败也停在原地，不是 Failed");
        assert_eq!(
            e.take_signup_msg(),
            Some((false, "这个账号已经被使用了".into()))
        );
        assert!(e.failed().is_none(), "建号失败不是致命错误");
        assert!(e.signup.is_none(), "失败也要把口令丢掉");
    }

    /// 建角 / 删角：发出去 → 等回执 → **自动重拉列表**（原版建完也要重查，
    /// `boChrQueryed := False`）。失败时要把原因留下来给界面弹窗。
    #[test]
    fn 建角删角走同一条尾巴() {
        let mut e = to_list(true);
        e.on(&char_list(&[11]));
        assert_eq!(*e.stage(), Stage::AwaitPick);

        // 手上还没有证明时，删角发不出去（别发一条注定被拒的消息）
        assert!(e.delete_character(11).is_none(), "没证明就该拒绝发");

        // 建角
        let cmd = e
            .create_character("小法", 2, 2, 2)
            .expect("该吐出 CreateCharacter");
        match cmd {
            Body::CreateCharacter(c) => {
                assert_eq!(
                    (c.name.as_str(), c.class, c.gender, c.hair),
                    ("小法", 2, 2, 2)
                );
            }
            other => panic!("期望 CreateCharacter，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitCreate);

        // 回执 ok ⇒ 退回等列表，并且**自己把 ListCharacters 发出去**
        let refresh = e
            .on(&env(Body::CreateCharacterResult(
                proto::CreateCharacterResult {
                    result: Some(proto::ActionResult {
                        ok: true,
                        code: 0,
                        message: String::new(),
                    }),
                    character_id: 22,
                },
            )))
            .expect("该自动重拉列表");
        assert!(matches!(refresh, Body::ListCharacters(_)));
        assert_eq!(*e.stage(), Stage::AwaitList);

        // 列表回来 ⇒ 又停在选角，名单是新的（多了一个）
        e.on(&char_list(&[11, 22]));
        assert_eq!(*e.stage(), Stage::AwaitPick);
        assert_eq!(e.characters().len(), 2);

        // 建角失败：原因要留下来（界面上弹出来），但**照样**重拉列表
        e.create_character("重名", 1, 1, 1).expect("该吐出消息");
        let refresh = e.on(&env(Body::CreateCharacterResult(
            proto::CreateCharacterResult {
                result: Some(proto::ActionResult {
                    ok: false,
                    code: 3,
                    message: "这个名字已经有人用了".into(),
                }),
                character_id: 0,
            },
        )));
        assert!(
            matches!(refresh, Some(Body::ListCharacters(_))),
            "失败也要重拉列表"
        );
        let why = e.take_pick_error().expect("失败原因要能取到");
        assert!(why.contains("这个名字已经有人用了"), "{why}");
    }

    /// 删角：证明是**登录时那条**（绑在连接 nonce 上），发出去时原样带上。
    ///
    /// ⚠️ 直接改 `e.proof` 是因为测试就在这个模块里（同一文件能碰私有字段）；
    /// 生产路径只有 `on_salt` 会写它 —— 也就是"登录过一次"。
    #[test]
    fn 删角带上登录证明() {
        let mut e = to_list(true);
        e.on(&char_list(&[11, 22]));
        e.proof = Some("ab".repeat(32));

        let cmd = e.delete_character(22).expect("有证明就该发得出去");
        match cmd {
            Body::DeleteCharacter(d) => {
                assert_eq!(d.character_id, 22);
                assert_eq!(d.password_hash, "ab".repeat(32), "证明要原样带上");
            }
            other => panic!("期望 DeleteCharacter，实得 {other:?}"),
        }
        assert_eq!(*e.stage(), Stage::AwaitDelete);
        // 回执（失败）⇒ 退回选角 + 留下原因
        let refresh = e.on(&env(Body::DeleteCharacterResult(
            proto::DeleteCharacterResult {
                result: Some(proto::ActionResult {
                    ok: false,
                    code: 1,
                    message: "口令确认没通过".into(),
                }),
            },
        )));
        assert!(matches!(refresh, Some(Body::ListCharacters(_))));
        assert_eq!(*e.stage(), Stage::AwaitList);
        assert!(e.take_pick_error().unwrap().contains("口令确认没通过"));
    }

    /// 建角/删角只在"停在选角"时有效（别处调用 = 调用方的 bug）。
    #[test]
    fn 建角删角只在选角阶段有效() {
        let mut e = to_list(true); // 还没拿到列表 ⇒ 阶段是 AwaitList
        assert!(e.create_character("小法", 2, 2, 2).is_none());
        e.proof = Some("cd".repeat(32));
        assert!(e.delete_character(1).is_none());
    }

    /// 没停在选角阶段时 `pick` 无效（调用方的 bug 不该静默发一条怪消息）。
    #[test]
    fn 没到选角就_pick_无效() {
        let mut e = to_list(true);
        assert!(e.pick(11).is_none(), "列表还没到就 pick");
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

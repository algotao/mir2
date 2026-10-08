//! 后台会话：**连接 + 消息泵**（独立线程 → channel）。
//!
//! plan §4.1 的分工：`core` 是纯函数（含会话状态机），**收发在这里**——
//! 收包线程只做"拆包 → channel"，调用方在自己的循环里按帧消费。
//! 这样 GUI 主循环永远不会被网络阻塞（`client/app` 与 `client/e2e` 共用这一份）。
//!
//! 线程模型：
//!
//! ```text
//! spawn() ──> [线程 A] 连接 + 握手 ──> Ev::Connected
//!                    │                 └─> 之后 A 变成**收包线程**：读帧 → Ev::Envelope
//!                    └─> 起 [线程 B] 发**命令**线程：Cmd → 写帧
//! ```
//!
//! 两个线程各持一个 `TcpStream` 克隆（`try_clone`），一个只管读、一个只管写 ——
//! 避免"一边阻塞读、一边想发"这种必须靠非阻塞轮询才能解的局。

use std::net::TcpStream;
use std::sync::mpsc::{channel, Receiver, Sender};
use std::thread;
use std::time::{Duration, Instant};

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

use crate::{Conn, NetError};

/// 发给服务端的命令。
///
/// 分成两类：**进世界的握手**（前三条，走一遍就完）与**玩法输入**（Move/Ping）。
/// ⚠️ 握手几步的顺序由 `core::entrance::Entrance` 决定，不在这里 —— 免得两端
/// （app 与 e2e）各排一遍序列。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Cmd {
    /// 认领会话（token 就是会话号，见 `protocol.md §11`）。
    ///
    /// ⚠️ 与 `Login` 的关系：`Login` 成功后服务端会把**新开的会话号**当 token 回给你，
    /// 那之后重连就走这条（不必再输口令）。v0 之前它也是**唯一**的入口。
    Reconnect(i32),
    /// 登录第一步：要 KDF 参数（盐/迭代/派生长）。见 `core::auth` 与 D-24①。
    LoginSaltRequest(String),
    /// 登录第二步：发口令的**证明**（`hex(HMAC(K, nonce‖account))`，不是口令）。
    Login { account: String, proof_hex: String },
    /// 建号（D-32）：发口令的**校验值** `hex(K)`。
    ///
    /// ⚠️ 与 `Login` 的差别要记牢：证明绑在本连接的 nonce 上（重放不了），而校验值
    /// **就是口令的等价物** —— 服务端得拿它落库才能验以后的登录（见 `account.proto`
    /// 的 `CreateAccount`）。所以这条只该在"取盐之后、建号那一次"发。
    CreateAccount {
        account: String,
        verifier_hex: String,
    },
    /// 列出该账号的角色。
    ListCharacters,
    /// 选角（服务端在**这一步**申请角色租约）。
    SelectCharacter(u64),
    /// 走一步。方向用**线上编号**（`proto::Direction` 的值），不在这里做 ±1 转换 ——
    /// 转换只允许在"游戏逻辑 ↔ 协议"的那一处发生，免得来回漂。
    Move(i32),
    /// 打一下（`AttackInput`）。
    ///
    /// ⚠️ `target_id` 是**目标实体的 ActorId**（新协议显式给目标，legacy 靠朝向格）；
    /// `action` 用线上编号（`proto::AttackAction` 的值）。
    Attack { target_id: u64, action: i32 },
    /// 心跳（`Ping`）。
    Ping,
    /// 主动关闭。
    Close,
}

/// 从会话线程送回来的事件。
#[derive(Debug)]
pub enum Ev {
    /// 握手完成（含协议版本与服务端能力）。
    Connected {
        version: u32,
        capabilities: Vec<String>,
        /// 握手 nonce（`ServerHello.session_key`）—— 登录时要把它拼进口令证明里
        /// （`core::auth::proof`）。**每条连接一次**，所以证明重放不了。
        nonce: Vec<u8>,
    },
    /// 收到一条信封。**调用方负责解释它**（`core::world::World::apply`）。
    Envelope(Box<Envelope>),
    /// 连接结束（正常关闭 / 出错），附原因。
    Closed(String),
}

/// 一条后台会话。字段直接是 channel 两端 —— 刻意**不加一层封装方法**：
/// 调用方要的是"发命令 / 收事件"，多一层只会让 `try_iter` 这类批量消费变别扭。
pub struct Session {
    pub cmds: Sender<Cmd>,
    pub evs: Receiver<Ev>,
}

/// 心跳间隔：一个字节都不用我们操心（服务端 30 分钟才算空闲），
/// 它存在的意义是让"半死的连接"尽早被 TCP 发现。
const PING_EVERY: Duration = Duration::from_secs(20);

impl Session {
    /// 起一个后台会话。**立刻返回**；连接结果通过 `Ev::Connected` / `Ev::Closed` 回来。
    ///
    /// `addr` 形如 `127.0.0.1:7500`（`gamesvr -proto-addr`）。
    pub fn spawn(addr: String, build: String, locale: String) -> Session {
        let (cmd_tx, cmd_rx) = channel::<Cmd>();
        let (ev_tx, ev_rx) = channel::<Ev>();

        thread::spawn(move || {
            let mut conn = match Conn::connect(&addr, &build, &locale) {
                Ok(c) => c,
                Err(e) => {
                    let _ = ev_tx.send(Ev::Closed(format!("连接 {addr} 失败：{e}")));
                    return;
                }
            };
            // ⚠️ 收包线程不能带读超时：一挂机就超时会**误报断开**。
            // 断开靠 TCP 自己（EOF / RST）发现，另外有心跳在跑。
            if let Err(e) = conn.set_read_timeout(None) {
                let _ = ev_tx.send(Ev::Closed(format!("设置读超时失败：{e}")));
                return;
            }
            let _ = ev_tx.send(Ev::Connected {
                version: proto::VERSION,
                capabilities: conn.capabilities.clone(),
                nonce: conn.session_key.clone(),
            });

            // 写线程：只管命令 → 写帧。
            let mut wr = match conn.try_clone_stream() {
                Ok(s) => s,
                Err(e) => {
                    let _ = ev_tx.send(Ev::Closed(format!("复制连接失败：{e}")));
                    return;
                }
            };
            thread::spawn(move || writer_loop(&mut wr, cmd_rx));

            // 本线程变成收包线程。
            loop {
                match conn.recv() {
                    Ok(env) => {
                        if ev_tx.send(Ev::Envelope(Box::new(env))).is_err() {
                            return; // 调用方已经不要了
                        }
                    }
                    Err(e) => {
                        let _ = ev_tx.send(Ev::Closed(describe(&e)));
                        return;
                    }
                }
            }
        });

        Session {
            cmds: cmd_tx,
            evs: ev_rx,
        }
    }
}

/// 写线程：命令 → 帧。`Close` 或对端断开就收手。
fn writer_loop(stream: &mut TcpStream, cmds: Receiver<Cmd>) {
    let mut seq = 0u32;
    let mut last_ping = Instant::now();
    loop {
        let cmd = match cmds.recv_timeout(Duration::from_millis(250)) {
            Ok(c) => c,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                // 到点补一次心跳（`seq` 由发方单调递增，见 protocol.md §2）。
                if last_ping.elapsed() >= PING_EVERY {
                    last_ping = Instant::now();
                    if write_body(
                        stream,
                        &mut seq,
                        Body::Ping(proto::Ping { client_time_ms: 0 }),
                    )
                    .is_err()
                    {
                        return;
                    }
                }
                continue;
            }
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return,
        };
        let body = match cmd {
            Cmd::Reconnect(session) => Body::Reconnect(proto::Reconnect {
                // v0 的临时编码：4 字节小端会话号（正式 token 由 `LoginResult` 签发）。
                session_token: session.to_le_bytes().to_vec(),
                last_ack_seq: 0,
            }),
            Cmd::LoginSaltRequest(account) => {
                Body::LoginSaltRequest(proto::LoginSaltRequest { account })
            }
            Cmd::Login { account, proof_hex } => Body::Login(proto::Login {
                account,
                password_hash: proof_hex,
                client_build: String::new(),
            }),
            Cmd::CreateAccount {
                account,
                verifier_hex,
            } => Body::CreateAccount(proto::CreateAccount {
                account,
                verifier: verifier_hex,
            }),
            Cmd::ListCharacters => Body::ListCharacters(proto::ListCharacters {}),
            Cmd::SelectCharacter(id) => {
                Body::SelectCharacter(proto::SelectCharacter { character_id: id })
            }
            Cmd::Move(dir) => Body::MoveInput(proto::MoveInput {
                direction: dir,
                client_tick: 0,
                ..Default::default()
            }),
            Cmd::Attack { target_id, action } => Body::AttackInput(proto::AttackInput {
                target_entity_id: target_id,
                action,
                client_tick: 0,
            }),
            Cmd::Ping => Body::Ping(proto::Ping { client_time_ms: 0 }),
            Cmd::Close => return,
        };
        if write_body(stream, &mut seq, body).is_err() {
            return;
        }
    }
}

fn write_body(stream: &mut TcpStream, seq: &mut u32, body: Body) -> Result<(), proto::FrameError> {
    *seq += 1;
    let env = Envelope {
        seq: *seq,
        ack_seq: 0,
        request_id: 0,
        body: Some(body),
    };
    proto::write_frame(stream, &env)
}

/// 把连接层错误转成给人看的一句话。
fn describe(e: &NetError) -> String {
    match e {
        NetError::Io(io) if io.kind() == std::io::ErrorKind::UnexpectedEof => {
            "服务端关闭了连接".to_string()
        }
        other => other.to_string(),
    }
}

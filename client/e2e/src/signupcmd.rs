//! `mir2-e2e signup` —— **建号的跨语言验收**（D-32）。
//!
//! 走的与 app 是**同一台状态机**（`core::entrance`）：取盐 → 发口令校验值 → 建号回执
//! → 就地再用同一个口令登录 → 停在选角（新账号还没有角色）。
//!
//! ```text
//! cargo run -p mir2-e2e -- signup -addr 127.0.0.1:7500 -account newbie -password pw123
//! ```
//!
//! 由 Go 侧的 `TestProtoRustSignup` 驱动（与 `TestProtoRustLogin` 同一套路）——
//! 那条测试**必须**把服务端开成 `-allow-new-account`（默认关，见 D-32）。
//!
//! ⚠️ 建号那一次会把 `hex(K)` 发出去（等价于口令，见 `net::session::Cmd::CreateAccount`）；
//! 这条命令是验收工具，别把真实口令喂给不可信的服务器。

use std::time::{Duration, Instant};

use mir2_core::entrance::{Entrance, Stage};
use mir2_net::session::{Cmd, Ev, Session};
use mir2_protocol::envelope::Body;

pub fn main(argv: &[String]) -> i32 {
    match run(argv) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("signup: {e}");
            1
        }
    }
}

struct Args {
    addr: String,
    account: String,
    password: String,
    expect_msg: Option<String>,
    timeout_ms: u64,
}

/// 取下一个参数（下标由调用方推进）。
fn arg(argv: &[String], i: &mut usize, key: &str) -> Result<String, String> {
    *i += 1;
    argv.get(*i).cloned().ok_or_else(|| format!("{key} 缺参数"))
}

fn parse_args(argv: &[String]) -> Result<Args, String> {
    let (mut addr, mut account, mut password, mut expect_msg) = (None, None, None, None);
    let mut timeout_ms = 8000u64;
    let mut i = 0;
    while i < argv.len() {
        match argv[i].as_str() {
            "-addr" => addr = Some(arg(argv, &mut i, "-addr")?),
            "-account" => account = Some(arg(argv, &mut i, "-account")?),
            "-password" => password = Some(arg(argv, &mut i, "-password")?),
            "-expect-msg" => expect_msg = Some(arg(argv, &mut i, "-expect-msg")?),
            "-timeout-ms" => {
                timeout_ms = arg(argv, &mut i, "-timeout-ms")?
                    .parse()
                    .map_err(|_| "-timeout-ms 要是整数".to_string())?;
            }
            other => return Err(format!("未知参数 {other}（见 mir2-e2e 用法）")),
        }
        i += 1;
    }
    Ok(Args {
        addr: addr.ok_or("缺少 -addr <host:port>（gamesvr -proto-addr）")?,
        account: account.ok_or("缺少 -account")?,
        password: password.ok_or("缺少 -password")?,
        expect_msg,
        timeout_ms,
    })
}

fn run(argv: &[String]) -> Result<(), String> {
    let a = parse_args(argv)?;
    let sess = Session::spawn(a.addr.clone(), "mir2-e2e-signup".into(), "zh-CN".into());
    let mut entrance = Entrance::new_for_signup(a.account.clone(), a.password.clone());
    // ⚠️ 新账号**还没有角色**：自动选角那条路会判"这个账号还没有角色"直接失败。
    // 手动选角停在 `AwaitPick` —— 那正是"建号 + 登录都成了"的证据。
    entrance.set_manual_pick(true);

    let deadline = Instant::now() + Duration::from_millis(a.timeout_ms);
    let mut signed_up = false;

    while Instant::now() < deadline {
        match sess.evs.recv_timeout(Duration::from_millis(50)) {
            Ok(Ev::Connected { nonce, .. }) => entrance.on_nonce(&nonce),
            Ok(Ev::Envelope(env)) => {
                if let Some(b) = entrance.on(&env) {
                    send(&sess, &b)?;
                }
            }
            Ok(Ev::Closed(why)) => return Err(format!("连接结束：{why}")),
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                return Err("会话线程结束了".into());
            }
        }
        // 状态机可能又攒了待发的（取盐 → 建号 → 登录三步各有一条）
        while let Some(b) = entrance.next_cmd() {
            send(&sess, &b)?;
        }
        if let Some(why) = entrance.failed() {
            return Err(why.to_string());
        }

        if !signed_up {
            if let Some((ok, msg)) = entrance.take_signup_msg() {
                if !ok {
                    return Err(format!("建号被拒：{msg}"));
                }
                if let Some(want) = &a.expect_msg {
                    if &msg != want {
                        return Err(format!("建号提示不符：{msg:?} ≠ {want:?}"));
                    }
                }
                println!("[signup] 建号成功：{msg}");
                signed_up = true;
                // 就地接着登录：同一条连接 ⇒ 同一条 nonce。
                //（app 那边是重开一条连接再登，两条路都该通。）
                entrance.begin_login(a.account.clone(), a.password.clone());
                continue;
            }
        } else if matches!(entrance.stage(), Stage::AwaitPick) {
            println!("[signup] 用新账号登录成功（停在选角：新账号还没有角色）");
            println!("[signup] 跨语言链路 OK（盐 → 校验值 → 落库 → 再登录）");
            return Ok(());
        }
    }
    if !signed_up {
        return Err("超时：没等到建号回执".into());
    }
    Err("超时：建号后用同一个口令登录没走到选角".into())
}

/// 把状态机吐出的 `Body` 翻成会话命令（只列建号这条路要用的几条）。
fn send(sess: &Session, body: &Body) -> Result<(), String> {
    let cmd = match body {
        Body::LoginSaltRequest(r) => Cmd::LoginSaltRequest(r.account.clone()),
        Body::CreateAccount(c) => Cmd::CreateAccount {
            account: c.account.clone(),
            verifier_hex: c.verifier.clone(),
        },
        Body::Login(l) => Cmd::Login {
            account: l.account.clone(),
            proof_hex: l.password_hash.clone(),
        },
        Body::ListCharacters(_) => Cmd::ListCharacters,
        other => {
            return Err(format!(
                "不支持在会话里发 {}（见 net::session::Cmd）",
                mir2_protocol::body_name(other)
            ))
        }
    };
    sess.cmds
        .send(cmd)
        .map_err(|_| "命令通道已关闭".to_string())
}

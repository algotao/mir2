//! MIR2 连接层：TCP + 握手 + 消息泵。
//!
//! **不依赖 SDL**（plan §4.1 / D-18）：`client/app`（GUI）与 `client/e2e`（无头）
//! 都必须走这一层，否则契约测试检验的就不是真客户端了。
//!
//! 分工：
//!
//! - `mir2-protocol`：生成的消息类型 + `[u32 长度][Envelope]` 分帧（纯数据，无 IO）
//! - **本 crate**：连接、握手（含版本校验）、发一收一
//! - 会话状态机（阶段推进、预测、重连决策）留在调用方；等 `client/app` 的交互逻辑
//!   成型后再收进 `mir2-core`（那里是"纯函数"的地盘，见 plan §4.2）

use std::io::{self, BufReader};
use std::net::{TcpStream, ToSocketAddrs};
use std::time::Duration;

pub mod session;
pub use session::{Cmd, Ev, Session};

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::{ClientHello, Envelope};

/// 读写超时：契约测试里"服务端该答没答"必须**尽快**变成错误，
/// 而不是让无头产物在 CI 上挂到超时上限。
pub const IO_TIMEOUT: Duration = Duration::from_secs(10);

/// 连接层错误。
#[derive(Debug)]
pub enum NetError {
    Io(io::Error),
    Frame(proto::FrameError),
    /// 协议版本不匹配 —— **明确拒绝**，不做"尽力而为"（protocol.md §5）。
    Version {
        client: u32,
        server: u32,
    },
    /// 握手阶段收到的不是 `ServerHello`。
    Handshake(String),
    /// 期望某条消息，收到的是别的（契约测试最常在这里红）。
    Unexpected(String),
    /// 服务端回了 `ServerError`。
    Server {
        code: u32,
        message: String,
    },
    /// 服务端回了 `Disconnect`（连接随后会断）。
    Disconnected {
        code: u32,
        reason: String,
    },
}

impl std::fmt::Display for NetError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            NetError::Io(e) => write!(f, "IO: {e}"),
            NetError::Frame(e) => write!(f, "分帧: {e}"),
            NetError::Version { client, server } => write!(
                f,
                "协议版本不匹配：客户端 {client} ≠ 服务端 {server}（重新生成两端代码或升/降客户端）"
            ),
            NetError::Handshake(m) => write!(f, "握手失败：{m}"),
            NetError::Unexpected(m) => write!(f, "收到的消息不符：{m}"),
            NetError::Server { code, message } => write!(f, "服务端错误 {code}: {message}"),
            NetError::Disconnected { code, reason } => write!(f, "被服务端断开 {code}: {reason}"),
        }
    }
}

impl std::error::Error for NetError {}

impl From<io::Error> for NetError {
    fn from(e: io::Error) -> Self {
        NetError::Io(e)
    }
}
impl From<proto::FrameError> for NetError {
    fn from(e: proto::FrameError) -> Self {
        NetError::Frame(e)
    }
}

/// 一条已握手的连接。
///
/// `seq` 是**发送方单调递增**的计数器（信封字段，protocol.md §2）——
/// 由本层统一维护，调用方不必关心。
pub struct Conn {
    wr: TcpStream,
    rd: BufReader<TcpStream>,
    seq: u32,
    /// 服务端在握手里报的能力（如 `enter-world`）。
    pub capabilities: Vec<String>,
    /// 握手 nonce（将来口令挑战应答要用，见 common/control.proto）。
    pub session_key: Vec<u8>,
}

impl Conn {
    /// 连接 + 完成握手（§5）。
    ///
    /// 版本不匹配**立即失败**，不返回一个"看起来能用"的连接。
    pub fn connect(
        addr: impl ToSocketAddrs,
        client_build: &str,
        locale: &str,
    ) -> Result<Self, NetError> {
        let s = TcpStream::connect(addr)?;
        // 交互式协议：小块消息不要等 Nagle 攒包。
        s.set_nodelay(true)?;
        s.set_read_timeout(Some(IO_TIMEOUT))?;
        s.set_write_timeout(Some(IO_TIMEOUT))?;

        let mut c = Conn {
            rd: BufReader::new(s.try_clone()?),
            wr: s,
            seq: 0,
            capabilities: Vec::new(),
            session_key: Vec::new(),
        };

        c.send(Body::ClientHello(ClientHello {
            protocol_version: proto::VERSION,
            client_build: client_build.to_string(),
            locale: locale.to_string(),
        }))?;

        let env = c.recv()?;
        match env.body.as_ref() {
            Some(Body::ServerHello(sh)) => {
                if sh.protocol_version != proto::VERSION {
                    return Err(NetError::Version {
                        client: proto::VERSION,
                        server: sh.protocol_version,
                    });
                }
                c.capabilities = sh.capabilities.clone();
                c.session_key = sh.session_key.clone();
                Ok(c)
            }
            Some(Body::ServerError(se)) => Err(NetError::Server {
                code: se.code,
                message: se.message.clone(),
            }),
            other => Err(NetError::Handshake(format!(
                "首包应答应为 ServerHello，实得 {}",
                other.map_or("none", proto::body_name)
            ))),
        }
    }

    /// 发一条消息（`seq` 自动 +1）。
    pub fn send(&mut self, body: Body) -> Result<(), NetError> {
        self.seq += 1;
        let env = Envelope {
            seq: self.seq,
            ack_seq: 0,
            request_id: 0,
            body: Some(body),
        };
        proto::write_frame(&mut self.wr, &env)?;
        Ok(())
    }

    /// 收一条消息。
    pub fn recv(&mut self) -> Result<Envelope, NetError> {
        Ok(proto::read_frame(&mut self.rd)?)
    }

    /// 调整读超时（`None` = 不超时）。
    ///
    /// ⚠️ 后台会话（`session::Session`）必须设成 `None`：一挂机就超时会**误报断开**。
    /// 契约测试那种"一条一条对齐"的用法则要保留超时（否则写错了会挂到天荒地老）。
    pub fn set_read_timeout(&self, t: Option<Duration>) -> io::Result<()> {
        // `rd`/`wr` 是同一个 socket 的两次 `try_clone`（dup 出来的 fd 共享同一份
        // 文件描述，SO_RCVTIMEO 是 socket 级选项）⇒ 设哪一个都一样。
        self.wr.set_read_timeout(t)
    }

    /// 拿一份底层 socket 的克隆（给写线程用：读线程与写线程各持一个句柄）。
    pub fn try_clone_stream(&self) -> io::Result<TcpStream> {
        self.wr.try_clone()
    }

    /// 发一条并等"期望的那一类"应答。
    ///
    /// 收到别的就报错 —— 契约测试要的正是这种严格：**顺序与类型都是契约**。
    /// （真客户端可以宽松（记数 + 忽略未知），但契约测试宽松就没意义了。）
    pub fn round_trip<T>(
        &mut self,
        req: Body,
        pick: impl Fn(&Envelope) -> Option<T>,
    ) -> Result<T, NetError> {
        self.send(req)?;
        let env = self.recv()?;
        pick(&env).ok_or_else(|| NetError::Unexpected(proto::msg_name(&env).to_string()))
    }
}

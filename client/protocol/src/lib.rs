//! MIR2 协议（Rust 侧）：**由 `protocol/*.proto` 生成**的类型 + 分帧。
//!
//! 单一真源与硬规则见 `docs/protocol.md` §3：**禁止手写编解码**，
//! schema 变更 = 两端同时重新生成 + bump `protocol/version.txt`。
//!
//! 本 crate 刻意**不含 IO**（除了对 `io::Read/Write` 的读写）：
//! TCP、握手、重连在 `mir2-net`；这样契约测试与无头产物都不需要网络也能测分帧。

use std::io::{self, Read, Write};

use prost::Message;

include!(concat!(env!("OUT_DIR"), "/mir2.rs"));

mod version {
    include!(concat!(env!("OUT_DIR"), "/version.rs"));
}
pub use version::VERSION;

/// 单帧上限（`protocol.md` §2）：超限**立即断开**，不做"尽力而为"（防 DoS）。
pub const MAX_FRAME: usize = 64 << 10;

/// 分帧错误。
#[derive(Debug)]
pub enum FrameError {
    /// 对端在长度域没读完就断了。
    Short(io::Error),
    /// 长度为 0：空帧无意义，视为协议错误而不是"无事发生"。
    Empty,
    /// 声明长度超限。**先判长度再分配** —— 否则一个伪造的长度域就能让对方申请 4 GiB。
    TooLarge(u32),
    /// 帧体不完整。
    Body(u32, io::Error),
    /// 帧体不是合法的 Envelope。
    Decode(prost::DecodeError),
    Io(io::Error),
}

impl std::fmt::Display for FrameError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            FrameError::Short(e) => write!(f, "长度域不完整: {e}"),
            FrameError::Empty => write!(f, "空帧"),
            FrameError::TooLarge(n) => write!(f, "帧超过 {} 字节上限（声明 {n}）", MAX_FRAME),
            FrameError::Body(n, e) => write!(f, "帧体不完整（声明 {n} 字节）: {e}"),
            FrameError::Decode(e) => write!(f, "解析信封: {e}"),
            FrameError::Io(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for FrameError {}

/// 写一条信封：`[u32 小端长度][Envelope]`。
pub fn write_frame<W: Write>(w: &mut W, env: &Envelope) -> Result<(), FrameError> {
    let body = env.encode_to_vec();
    if body.is_empty() {
        return Err(FrameError::Empty);
    }
    if body.len() > MAX_FRAME {
        return Err(FrameError::TooLarge(body.len() as u32));
    }
    w.write_all(&(body.len() as u32).to_le_bytes())
        .map_err(FrameError::Io)?;
    w.write_all(&body).map_err(FrameError::Io)?;
    w.flush().map_err(FrameError::Io)?;
    Ok(())
}

/// 读一条信封。
pub fn read_frame<R: Read>(r: &mut R) -> Result<Envelope, FrameError> {
    let mut hdr = [0u8; 4];
    r.read_exact(&mut hdr).map_err(FrameError::Short)?;
    let n = u32::from_le_bytes(hdr);
    match n {
        0 => return Err(FrameError::Empty),
        n if n as usize > MAX_FRAME => return Err(FrameError::TooLarge(n)),
        _ => {}
    }
    let mut body = vec![0u8; n as usize];
    r.read_exact(&mut body)
        .map_err(|e| FrameError::Body(n, e))?;
    Envelope::decode(&body[..]).map_err(FrameError::Decode)
}

/// 取一条消息的可读名字（对应 Go 侧 `frame.MsgName`，给日志与断言用）。
///
/// ⚠️ 这里**手写**了映射表 —— 它不违反"禁止手写编解码"：名字不参与线上格式，
/// 只是日志。但为免它随 schema 漂移得太远，**只列我们真正处理的消息**，
/// 其余一律 `"other"`（收到 unknown 时本来就只该记数 + 忽略，见 protocol.md §4.1）。
pub fn msg_name(env: &Envelope) -> &'static str {
    match &env.body {
        None => "none",
        Some(b) => body_name(b),
    }
}

/// 同上，但直接给 `oneof` 的 body（还没有信封时也能取名字）。
pub fn body_name(body: &envelope::Body) -> &'static str {
    use envelope::Body;
    match body {
        Body::ClientHello(_) => "ClientHello",
        Body::ServerHello(_) => "ServerHello",
        Body::Ping(_) => "Ping",
        Body::Pong(_) => "Pong",
        Body::Disconnect(_) => "Disconnect",
        Body::ServerError(_) => "ServerError",
        Body::Reconnect(_) => "Reconnect",
        Body::ReconnectResult(_) => "ReconnectResult",
        Body::ListCharacters(_) => "ListCharacters",
        Body::CharacterList(_) => "CharacterList",
        Body::SelectCharacter(_) => "SelectCharacter",
        Body::SelectCharacterResult(_) => "SelectCharacterResult",
        Body::EnterWorld(_) => "EnterWorld",
        Body::AbilityUpdate(_) => "AbilityUpdate",
        Body::ChangeMap(_) => "ChangeMap",
        Body::EntityAppear(_) => "EntityAppear",
        Body::EntityDisappear(_) => "EntityDisappear",
        Body::EntityMove(_) => "EntityMove",
        _ => "other",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hello() -> Envelope {
        Envelope {
            seq: 1,
            ack_seq: 0,
            request_id: 0,
            body: Some(envelope::Body::ClientHello(ClientHello {
                protocol_version: VERSION,
                client_build: "test".into(),
                locale: "zh-CN".into(),
            })),
        }
    }

    /// 黄金报文（protocol.md §9.3）：与 Go 侧 `TestGoldenFrameBytes` **同一组字节**。
    ///
    /// 这是防双实现漂移最硬的一道：两端各自编同一封信，必须逐字节相同。
    /// ⚠️ `VERSION` 变了这里就会红 —— 那正是它存在的意义。
    #[test]
    fn golden_frame_bytes_match_go() {
        // 与 Go 侧 `TestGoldenFrameBytes` **逐字节对齐**（两边各钉一份，谁改协议都得两边一起动）：
        //   1400000008018a100f08031204746573741a057a682d434e
        let ver = VERSION;
        assert_eq!(ver, 3, "version.txt 变了就要连着确认这条黄金报文");
        let mut buf = Vec::new();
        write_frame(&mut buf, &hello()).expect("写帧");
        let got: String = buf.iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!(got, "1400000008018a100f08031204746573741a057a682d434e");
    }

    /// 分帧边界：超限**先判长度再分配**、空帧报错、往返一致。
    #[test]
    fn frame_rejections_and_roundtrip() {
        // 声明 4 GiB：必须在分配前拒掉
        let mut bad = (0xFFFF_FFFFu32).to_le_bytes().to_vec();
        bad.extend_from_slice(&[0u8; 8]);
        match read_frame(&mut &bad[..]) {
            Err(FrameError::TooLarge(_)) => {}
            other => panic!("应报 TooLarge，实得 {other:?}"),
        }
        // 长度 0
        match read_frame(&mut &(0u32).to_le_bytes()[..]) {
            Err(FrameError::Empty) => {}
            other => panic!("应报 Empty，实得 {other:?}"),
        }
        // 往返
        let env = hello();
        let mut buf = Vec::new();
        write_frame(&mut buf, &env).expect("写");
        let back = read_frame(&mut &buf[..]).expect("读");
        assert_eq!(msg_name(&back), "ClientHello");
        assert_eq!(back, env);
        assert_eq!(msg_name(&Envelope::default()), "none");
    }
}

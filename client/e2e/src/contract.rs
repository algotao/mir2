//! `mir2-e2e contract` —— 契约测试的**客户端那一半**（docs/protocol.md §9.2）。
//!
//! 连上一个真服务端（`gamesvr -proto-addr`），把 §5 的序列走一遍：
//!
//! ```text
//! ClientHello → ServerHello → Reconnect → ListCharacters → SelectCharacter
//!   → EnterWorld → AbilityUpdate → Ping/Pong
//! ```
//!
//! 并**断言**每一步的消息类型与关键字段；`-expect-*` 给的是"服务端那侧已知的真值"
//! （由驱动方传入，如 Go 的契约测试），用来把断言钉到具体数值上。
//!
//! ⚠️ 它的价值在于走的是与 `client/app` **完全相同**的协议编解码（`mir2-protocol`）
//! 与连接层（`mir2-net`）—— D-18 的纪律；否则它检验的就不是真客户端了。

use mir2_net::Conn;
use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

pub fn main(argv: &[String]) -> i32 {
    match run(argv) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("[e2e] 契约失败：{e}");
            1
        }
    }
}

struct Args {
    addr: String,
    session: i32,
    char_id: Option<u64>,
    build: String,
    expect_map: Option<String>,
    expect_pos: Option<(i32, i32)>,
    expect_dir: Option<i32>,
    expect_entities: Option<usize>,
}

impl Args {
    fn parse(argv: &[String]) -> Result<Self, String> {
        let (mut addr, mut session, mut char_id) = (None, None, None);
        let (mut build, mut expect_map, mut expect_pos) = ("mir2-e2e".to_string(), None, None);
        let (mut expect_dir, mut expect_entities) = (None, None);

        let mut i = 0;
        while i < argv.len() {
            let key = argv[i].clone();
            let mut val = |what: &str| -> Result<String, String> {
                i += 1;
                argv.get(i)
                    .cloned()
                    .ok_or_else(|| format!("{key} 后面要跟{what}"))
            };
            match key.as_str() {
                "-addr" => addr = Some(val("地址")?),
                "-session" => {
                    session = Some(
                        val("会话号")?
                            .parse::<i32>()
                            .map_err(|_| "-session 必须是十进制整数".to_string())?,
                    )
                }
                "-char" => {
                    char_id = Some(
                        val("角色 id")?
                            .parse::<u64>()
                            .map_err(|_| "-char 必须是十进制整数".to_string())?,
                    )
                }
                "-build" => build = val("客户端标识")?,
                "-expect-map" => expect_map = Some(val("地图名")?),
                "-expect-pos" => expect_pos = Some(parse_pos(&val("X,Y")?)?),
                "-expect-dir" => {
                    expect_dir = Some(
                        val("朝向号")?
                            .parse::<i32>()
                            .map_err(|_| "-expect-dir 必须是整数".to_string())?,
                    )
                }
                "-expect-entities" => {
                    expect_entities = Some(
                        val("实体数")?
                            .parse::<usize>()
                            .map_err(|_| "-expect-entities 必须是非负整数".to_string())?,
                    )
                }
                other => return Err(format!("未知参数 {other}（-h 看用法）")),
            }
            i += 1;
        }

        Ok(Args {
            addr: addr.ok_or("缺少 -addr <host:port>（gamesvr -proto-addr 的地址）")?,
            session: session.ok_or("缺少 -session <会话号>（v0 的 session_token）")?,
            char_id,
            build,
            expect_map,
            expect_pos,
            expect_dir,
            expect_entities,
        })
    }
}

fn parse_pos(s: &str) -> Result<(i32, i32), String> {
    let (x, y) = s
        .split_once(',')
        .ok_or_else(|| format!("坐标要写成 X,Y，实得 {s:?}"))?;
    let px = x
        .trim()
        .parse::<i32>()
        .map_err(|_| "X 不是整数".to_string())?;
    let py = y
        .trim()
        .parse::<i32>()
        .map_err(|_| "Y 不是整数".to_string())?;
    Ok((px, py))
}

fn run(argv: &[String]) -> Result<(), String> {
    let a = Args::parse(argv)?;
    let mut c = Conn::connect(&a.addr, &a.build, "zh-CN").map_err(|e| e.to_string())?;
    println!(
        "[1] 握手完成：协议版本 {}，能力 {:?}，nonce {} 字节",
        proto::VERSION,
        c.capabilities,
        c.session_key.len()
    );

    // [2] 认领会话（v0：session_token = 4 字节小端会话号）
    c.send(Body::Reconnect(proto::Reconnect {
        session_token: a.session.to_le_bytes().to_vec(),
        last_ack_seq: 0,
    }))
    .map_err(|e| e.to_string())?;
    let env = recv_ok(&mut c)?;
    let status = want(&env, |b| match b {
        Body::ReconnectResult(r) => Some(r.status),
        _ => None,
    })?;
    println!("[2] 认领会话 {} → {}", a.session, status_name(status));
    let in_world = status == proto::ReconnectStatus::ReconnectOkInWorld as i32;

    let mut expect_char = a.char_id;
    if !in_world {
        // [3] 列角色
        c.send(Body::ListCharacters(proto::ListCharacters {}))
            .map_err(|e| e.to_string())?;
        let env = recv_ok(&mut c)?;
        let chars = want(&env, |b| match b {
            Body::CharacterList(l) => Some(l.characters.clone()),
            _ => None,
        })?;
        println!("[3] 角色列表 {} 个：", chars.len());
        for ch in &chars {
            println!(
                "      id={} 名={} {} {} 级  发型={}",
                ch.character_id,
                ch.name,
                class_name(ch.r#class),
                ch.level,
                ch.gender_hair
            );
        }
        if chars.is_empty() {
            return Err("角色列表为空：这个账号还没有角色（先建一个新角色）".into());
        }

        // [4] 选角（服务端在**这一步**申请角色租约）
        let target = a.char_id.unwrap_or(chars[0].character_id);
        c.send(Body::SelectCharacter(proto::SelectCharacter {
            character_id: target,
        }))
        .map_err(|e| e.to_string())?;
        let env = recv_ok(&mut c)?;
        let res = want(&env, |b| match b {
            Body::SelectCharacterResult(r) => Some(r.clone()),
            _ => None,
        })?;
        if res.code != proto::SelectCharCode::SelectCharOk as i32 {
            return Err(format!(
                "选角失败：code={} {}（租约被占时服务端会**明确拒绝**，不顶号）",
                res.code, res.message
            ));
        }
        println!("[4] 选角成功：角色 id={}", res.character_id);
        expect_char = Some(res.character_id);
    }

    // [5] 进图快照
    let env = recv_ok(&mut c)?;
    let ew = want(&env, |b| match b {
        Body::EnterWorld(e) => Some(e.clone()),
        _ => None,
    })?;
    if ew.self_entity_id == 0 {
        return Err("EnterWorld 没有带 self_entity_id".into());
    }
    if ew.map_name.is_empty() {
        return Err("EnterWorld 没有带地图名（本项目地图按**名字**索引，见 D-22）".into());
    }
    let pos = ew.position.unwrap_or_default();
    println!(
        "[5] 进图：自己 ActorId={} 地图={} 坐标=({},{}) 朝向={} 视野实体 {} 个",
        ew.self_entity_id,
        ew.map_name,
        pos.x,
        pos.y,
        dir_name(ew.direction),
        ew.entities.len()
    );
    for e in &ew.entities {
        println!(
            "      {} id={} 名={} ({},{}) 朝向={} hp={}/{}",
            kind_name(e.kind),
            e.entity_id,
            e.name,
            e.position.as_ref().map_or(0, |p| p.x),
            e.position.as_ref().map_or(0, |p| p.y),
            dir_name(e.direction),
            e.hp,
            e.max_hp
        );
        if e.entity_id == ew.self_entity_id {
            return Err("初始快照里不该有自己（self_entity_id 已单列）".into());
        }
    }

    // [6] 自身能力值
    let env = recv_ok(&mut c)?;
    let ab = want(&env, |b| match b {
        Body::AbilityUpdate(a) => Some(a.ability.unwrap_or_default()),
        _ => None,
    })?;
    println!(
        "[6] 能力值：{} 级 hp={}/{} mp={}/{} dc={}-{} ac={} 金币={}",
        ab.level, ab.hp, ab.max_hp, ab.mp, ab.max_mp, ab.dc_min, ab.dc_max, ab.ac, ab.gold
    );

    // [7] 心跳：`seq` 由连接层维护，回显必须一致
    c.send(Body::Ping(proto::Ping {
        client_time_ms: 4242,
    }))
    .map_err(|e| e.to_string())?;
    let env = recv_ok(&mut c)?;
    let pong = want(&env, |b| match b {
        Body::Pong(p) => Some(*p),
        _ => None,
    })?;
    if pong.client_time_ms != 4242 {
        return Err(format!("Pong 回显 = {}，应为 4242", pong.client_time_ms));
    }
    println!("[7] 心跳往返正常（回显 {}）", pong.client_time_ms);

    // [8] 未知/未实现的消息：服务端必须**记数 + 忽略**，不断连（protocol.md §4.1）
    c.send(Body::Raw(proto::Raw {
        msg_id: 0x0F01,
        body: vec![1, 2, 3],
    }))
    .map_err(|e| e.to_string())?;
    c.send(Body::Ping(proto::Ping {
        client_time_ms: 4243,
    }))
    .map_err(|e| e.to_string())?;
    let env = recv_ok(&mut c)?;
    let pong = want(&env, |b| match b {
        Body::Pong(p) => Some(*p),
        _ => None,
    })?;
    if pong.client_time_ms != 4243 {
        return Err("发过未知消息之后连接应当仍在（服务端只该记数 + 忽略）".into());
    }
    println!("[8] 未知消息被忽略，连接仍在");

    // [9] 把驱动方给的真值对上（由 Go 那边的契约测试传入）
    assert_expectations(&a, expect_char, &ew)?;
    println!(
        "契约通过：握手 → 认领会话 → 选角 → 进图（{} 个实体）→ 能力值 → 心跳",
        ew.entities.len()
    );
    Ok(())
}

/// 把 `-expect-*`（驱动方已知的真值）与实收对账。
///
/// 为什么让驱动方传：Rust 这一侧**不该**知道服务端的数据（它是通用客户端）；
/// 而"位置/朝向/实体数对不对"这类断言只有知道播种数据的那一侧才能下。
fn assert_expectations(
    a: &Args,
    char_id: Option<u64>,
    ew: &proto::EnterWorld,
) -> Result<(), String> {
    if let (Some(want), Some(got)) = (&a.expect_map, Some(ew.map_name.clone())) {
        if *want != got {
            return Err(format!("地图 = {got:?}，应为 {want:?}"));
        }
    }
    if let (Some((wx, wy)), Some(p)) = (a.expect_pos, ew.position.as_ref()) {
        if p.x != wx || p.y != wy {
            return Err(format!("坐标 = ({},{})，应为 ({wx},{wy})", p.x, p.y));
        }
    }
    if let Some(wd) = a.expect_dir {
        if ew.direction != wd {
            return Err(format!(
                "朝向 = {}（{}），应为 {wd}（{}）",
                ew.direction,
                dir_name(ew.direction),
                dir_name(wd)
            ));
        }
    }
    if let Some(wn) = a.expect_entities {
        if ew.entities.len() != wn {
            return Err(format!("视野实体 = {} 个，应为 {wn} 个", ew.entities.len()));
        }
    }
    if let (Some(want), Some(got)) = (a.char_id, char_id) {
        if want != got {
            return Err(format!("选中的角色 id = {got}，应为 {want}"));
        }
    }
    Ok(())
}

/// 收一条；`ServerError` / `Disconnect` 直接当失败（契约测试里它们是失败信号）。
fn recv_ok(c: &mut Conn) -> Result<Envelope, String> {
    let env = c.recv().map_err(|e| e.to_string())?;
    match &env.body {
        Some(Body::ServerError(se)) => Err(format!("服务端错误 {}：{}", se.code, se.message)),
        Some(Body::Disconnect(d)) => Err(format!("被服务端断开 {}：{}", d.code, d.reason)),
        _ => Ok(env),
    }
}

/// 从收到的信封里取期望的那一类；取不到就报"实得什么"（顺序与类型都是契约）。
fn want<T>(env: &Envelope, f: impl Fn(&Body) -> Option<T>) -> Result<T, String> {
    env.body
        .as_ref()
        .and_then(f)
        .ok_or_else(|| format!("收到的消息不符：{}", proto::msg_name(env)))
}

fn dir_name(v: i32) -> &'static str {
    match proto::Direction::try_from(v) {
        Ok(proto::Direction::DirUp) => "上",
        Ok(proto::Direction::DirUpRight) => "右上",
        Ok(proto::Direction::DirRight) => "右",
        Ok(proto::Direction::DirDownRight) => "右下",
        Ok(proto::Direction::DirDown) => "下",
        Ok(proto::Direction::DirDownLeft) => "左下",
        Ok(proto::Direction::DirLeft) => "左",
        Ok(proto::Direction::DirUpLeft) => "左上",
        _ => "未指定",
    }
}

fn status_name(v: i32) -> &'static str {
    match proto::ReconnectStatus::try_from(v) {
        Ok(proto::ReconnectStatus::ReconnectOkInWorld) => "回到世界（租约仍在）",
        Ok(proto::ReconnectStatus::ReconnectBackToSelect) => "回落到选角",
        Ok(proto::ReconnectStatus::ReconnectFailed) => "失败",
        _ => "未指定",
    }
}

fn kind_name(v: u32) -> &'static str {
    match v {
        0 => "玩家",
        1 => "怪物",
        2 => "NPC",
        _ => "未知类别",
    }
}

fn class_name(v: i32) -> &'static str {
    match proto::CharClass::try_from(v) {
        Ok(proto::CharClass::Warrior) => "战士",
        Ok(proto::CharClass::Wizard) => "法师",
        Ok(proto::CharClass::Taoist) => "道士",
        _ => "未指定",
    }
}

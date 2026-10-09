//! `mir2-e2e contract` —— 契约测试的**客户端那一半**（docs/protocol.md §9.2）。
//!
//! 连上一个真服务端（`gamesvr -proto-addr`），把 §5 的序列走一遍：
//!
//! ```text
//! ClientHello → ServerHello → Reconnect → ListCharacters → SelectCharacter
//!   → EnterWorld → AbilityUpdate → BagItems/EquippedItems → Ping/Pong → MoveInput → EntityMove
//! ```
//!
//! 并**断言**每一步的消息类型与关键字段；`-expect-*` 给的是"服务端那侧已知的真值"
//! （由驱动方传入，如 Go 的契约测试），用来把断言钉到具体数值上。
//!
//! ⚠️ 两条纪律，都是本轮踩出来的：
//!
//! 1. **不能假设"下一条就是我等的那条"**：进世界之后实体事件是**随时**来的
//!    （怪物 AI、别人的移动、周期性视野同步）。真客户端按类型分派，
//!    所以这里也按类型分派（`recv_ctl` 把推送事件记下来继续读）。
//! 2. 走的必须是 `client/app` **同一份**协议编解码（`mir2-protocol`）与连接层
//!    （`mir2-net`）—— D-18；否则它检验的就不是真客户端了。

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
    /// 走一步的落点（服务端权威回显里应从 `expect_pos` 走到这里）。
    expect_walk_to: Option<(i32, i32)>,
    /// 至少要收到几条"服务端主动推"的实体事件（驱动方会在进图后触发）。
    expect_pushed: usize,
}

impl Args {
    fn parse(argv: &[String]) -> Result<Self, String> {
        let (mut addr, mut session, mut char_id) = (None, None, None);
        let (mut build, mut expect_map, mut expect_pos) = ("mir2-e2e".to_string(), None, None);
        let (mut expect_dir, mut expect_entities) = (None, None);
        let (mut expect_walk_to, mut expect_pushed) = (None, 0usize);

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
                "-expect-walk-to" => expect_walk_to = Some(parse_pos(&val("X,Y")?)?),
                "-expect-pushed" => {
                    expect_pushed = val("条数")?
                        .parse::<usize>()
                        .map_err(|_| "-expect-pushed 必须是非负整数".to_string())?
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
            expect_walk_to,
            expect_pushed,
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

// ---------- 服务端主动推来的实体事件 ----------

/// `Pushed` 记账"服务端主动推"的实体事件。
///
/// ⚠️ 它们与请求-应答**无关**，随时会来（怪物 AI / 别人的移动 / 周期性视野同步）
/// ⇒ 收到就记下、继续读，绝不能当成"顺序不符"。
#[derive(Default)]
struct Pushed {
    appears: Vec<String>,
    moves: Vec<String>,
    disappears: Vec<String>,
}

impl Pushed {
    fn len(&self) -> usize {
        self.appears.len() + self.moves.len() + self.disappears.len()
    }

    /// 是实体事件就打印 + 记账，返回 true（调用方据此跳过它继续读）。
    fn note(&mut self, env: &Envelope) -> bool {
        match &env.body {
            Some(Body::EntityAppear(a)) => {
                let e = a.entity.clone().unwrap_or_default();
                let s = format!(
                    "出现 {} id={} 名={} ({},{}) 朝向={} hp={}/{}",
                    kind_name(e.kind),
                    e.entity_id,
                    e.name,
                    e.position.as_ref().map_or(0, |p| p.x),
                    e.position.as_ref().map_or(0, |p| p.y),
                    dir_name(e.direction),
                    e.hp,
                    e.max_hp
                );
                println!("      推送 · {s}");
                self.appears.push(s);
                true
            }
            Some(Body::EntityMove(m)) => {
                let f = m.from.unwrap_or_default();
                let t = m.to.unwrap_or_default();
                let s = format!(
                    "移动 id={} ({},{})→({},{}) 朝向={}",
                    m.entity_id,
                    f.x,
                    f.y,
                    t.x,
                    t.y,
                    dir_name(m.direction)
                );
                println!("      推送 · {s}");
                self.moves.push(s);
                true
            }
            Some(Body::EntityDisappear(d)) => {
                let s = format!("消失 id={} 原因={}", d.entity_id, d.reason);
                println!("      推送 · {s}");
                self.disappears.push(s);
                true
            }
            _ => false,
        }
    }
}

// ---------- 收包 ----------

/// 收一条**控制类**应答；途中遇到的实体事件记进 `pushed`（见 `Pushed` 的说明）。
fn recv_ctl(c: &mut Conn, pushed: &mut Pushed) -> Result<Envelope, String> {
    for _ in 0..256 {
        let env = c.recv().map_err(|e| e.to_string())?;
        check_fatal(&env)?;
        if pushed.note(&env) {
            continue;
        }
        return Ok(env);
    }
    Err("连读 256 条都是推送事件，始终没等到应答".into())
}

/// 一直读到"指定实体的 EntityMove"为止（途中的推送照常记账）。
fn wait_move_of(c: &mut Conn, pushed: &mut Pushed, id: u64) -> Result<proto::EntityMove, String> {
    for _ in 0..256 {
        let env = c.recv().map_err(|e| e.to_string())?;
        check_fatal(&env)?;
        if let Some(Body::EntityMove(m)) = &env.body {
            if m.entity_id == id {
                return Ok(*m);
            }
        }
        if pushed.note(&env) {
            continue;
        }
        return Err(format!(
            "等 ActorId={id} 的 EntityMove 时收到 {}",
            proto::msg_name(&env)
        ));
    }
    Err(format!("等 ActorId={id} 的 EntityMove 超时"))
}

/// 一直读到 `MoveRejected` 为止。
fn wait_reject(c: &mut Conn, pushed: &mut Pushed) -> Result<proto::MoveRejected, String> {
    for _ in 0..256 {
        let env = c.recv().map_err(|e| e.to_string())?;
        check_fatal(&env)?;
        if let Some(Body::MoveRejected(r)) = &env.body {
            return Ok(*r);
        }
        if pushed.note(&env) {
            continue;
        }
        return Err(format!("等 MoveRejected 时收到 {}", proto::msg_name(&env)));
    }
    Err("等 MoveRejected 超时".into())
}

/// 一直读到"至少看过 `want` 条推送事件"为止。
fn wait_pushed(c: &mut Conn, pushed: &mut Pushed, want: usize) -> Result<(), String> {
    for _ in 0..256 {
        if pushed.len() >= want {
            return Ok(());
        }
        let env = c.recv().map_err(|e| e.to_string())?;
        check_fatal(&env)?;
        pushed.note(&env);
    }
    Err(format!("等推送事件超时（只收到 {} 条）", pushed.len()))
}

/// `ServerError` / `Disconnect` 在契约测试里都是失败信号。
fn check_fatal(env: &Envelope) -> Result<(), String> {
    match &env.body {
        Some(Body::ServerError(se)) => Err(format!("服务端错误 {}：{}", se.code, se.message)),
        Some(Body::Disconnect(d)) => Err(format!("被服务端断开 {}：{}", d.code, d.reason)),
        _ => Ok(()),
    }
}

/// 从收到的信封里取期望的那一类；取不到就报"实得什么"。
fn want<T>(env: &Envelope, f: impl Fn(&Body) -> Option<T>) -> Result<T, String> {
    env.body
        .as_ref()
        .and_then(f)
        .ok_or_else(|| format!("收到的消息不符：{}", proto::msg_name(env)))
}

// ---------- 主流程 ----------

fn run(argv: &[String]) -> Result<(), String> {
    let a = Args::parse(argv)?;
    let mut c = Conn::connect(&a.addr, &a.build, "zh-CN").map_err(|e| e.to_string())?;
    let mut pushed = Pushed::default();
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
    let env = recv_ctl(&mut c, &mut pushed)?;
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
        let env = recv_ctl(&mut c, &mut pushed)?;
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
        let env = recv_ctl(&mut c, &mut pushed)?;
        let res = want(&env, |b| match b {
            Body::SelectCharacterResult(r) => Some(r.clone()), // 带 String，非 Copy
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
    let env = recv_ctl(&mut c, &mut pushed)?;
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
    let env = recv_ctl(&mut c, &mut pushed)?;
    let ab = want(&env, |b| match b {
        Body::AbilityUpdate(a) => Some(a.ability.unwrap_or_default()),
        _ => None,
    })?;
    println!(
        "[6] 能力值：{} 级 hp={}/{} mp={}/{} dc={}-{} ac={} 金币={}",
        ab.level, ab.hp, ab.max_hp, ab.mp, ab.max_mp, ab.dc_min, ab.dc_max, ab.ac, ab.gold
    );

    // [6′] 背包与已穿戴（D-65）。角色没有物品 ⇒ 都是空表，但**消息必须在**：
    // 客户端靠它们初始化背包格与装备栏（原来这两条只在 legacy 那条路上发，
    // proto 玩家进图后背包永远是空的）。
    let env = recv_ctl(&mut c, &mut pushed)?;
    let bag = want(&env, |b| match b {
        Body::BagItems(x) => Some(x.items.len()),
        _ => None,
    })?;
    let env = recv_ctl(&mut c, &mut pushed)?;
    let equip = want(&env, |b| match b {
        Body::EquippedItems(x) => Some(x.items.len()),
        _ => None,
    })?;
    println!("[6′] 背包 {bag} 格、已穿戴 {equip} 槽");

    // [7] 心跳
    c.send(Body::Ping(proto::Ping {
        client_time_ms: 4242,
    }))
    .map_err(|e| e.to_string())?;
    let env = recv_ctl(&mut c, &mut pushed)?;
    let pong = want(&env, |b| match b {
        Body::Pong(p) => Some(*p), // Pong 是 Copy
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
    let env = recv_ctl(&mut c, &mut pushed)?;
    let pong = want(&env, |b| match b {
        Body::Pong(p) => Some(*p), // Pong 是 Copy
        _ => None,
    })?;
    if pong.client_time_ms != 4243 {
        return Err("发过未知消息之后连接应当仍在（服务端只该记数 + 忽略）".into());
    }
    println!("[8] 未知消息被忽略，连接仍在");

    // [9] **实时性**：等服务端主动推来的实体事件。
    // 驱动方（Go 契约测试）在玩家进图后触发一条；它们与请求-应答无关，
    // 所以上面几步里收到的也算数（`Pushed` 已经在记账）。
    wait_pushed(&mut c, &mut pushed, a.expect_pushed)?;
    println!(
        "[9] 实时推送：收到 {} 条实体事件（出现 {} / 移动 {} / 消失 {}）",
        pushed.len(),
        pushed.appears.len(),
        pushed.moves.len(),
        pushed.disappears.len()
    );
    if pushed.len() < a.expect_pushed {
        return Err(format!(
            "推送事件只有 {} 条，应为至少 {} 条",
            pushed.len(),
            a.expect_pushed
        ));
    }

    // [10] 自己走一步：MoveInput → 收到**自己的权威回显**（客户端预测的纠偏依据）
    c.send(Body::MoveInput(proto::MoveInput {
        direction: proto::Direction::DirRight as i32,
        client_tick: 1,
        ..Default::default()
    }))
    .map_err(|e| e.to_string())?;
    let mv = wait_move_of(&mut c, &mut pushed, ew.self_entity_id)?;
    let f = mv.from.unwrap_or_default();
    let t = mv.to.unwrap_or_default();
    println!(
        "[10] 自己走一步：({},{})→({},{}) 朝向={}",
        f.x,
        f.y,
        t.x,
        t.y,
        dir_name(mv.direction)
    );
    if let (Some((wx, wy)),) = (a.expect_walk_to,) {
        if t.x != wx || t.y != wy {
            return Err(format!(
                "走一步的落点 = ({},{})，应为 ({wx},{wy})",
                t.x, t.y
            ));
        }
    }

    // [11] 紧接着再走一次 ⇒ 限流拒绝。**必须回一条**：客户端已经预测着走过去了，
    // 服务端不吭声它的位置就永久分叉（legacy 不预测，那边静默忽略是对的）。
    c.send(Body::MoveInput(proto::MoveInput {
        direction: proto::Direction::DirRight as i32,
        client_tick: 2,
        ..Default::default()
    }))
    .map_err(|e| e.to_string())?;
    let rej = wait_reject(&mut c, &mut pushed)?;
    let p = rej.authoritative_position.unwrap_or_default();
    println!(
        "[11] 超速被拒：reason={} 权威位置=({},{})",
        rej.reason, p.x, p.y
    );
    if rej.reason != 1 {
        return Err(format!(
            "第二次移动应被限流（reason 1），实得 {}",
            rej.reason
        ));
    }

    // [12] 把驱动方给的真值对上
    assert_expectations(&a, expect_char, &ew)?;
    println!(
        "契约通过：握手 → 认领会话 → 选角 → 进图（{} 个实体）→ 能力值 → 心跳 → \
         实时推送 {} 条 → 自己走一步 → 超速被拒",
        ew.entities.len(),
        pushed.len()
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
    if let Some(want) = &a.expect_map {
        if *want != ew.map_name {
            return Err(format!("地图 = {:?}，应为 {want:?}", ew.map_name));
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

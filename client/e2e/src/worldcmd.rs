//! `mir2-e2e world` —— 用**会话层**（`mir2-net` 的消息泵 + `core` 的握手状态机与世界状态）
//! 连一个真服务端，把世界跑起来并断言。
//!
//! 与 `mir2-e2e contract` 的分工：
//!
//! - `contract`：逐条消息的**顺序与字段**（"线路对不对"）
//! - `world`：**状态机串起来能不能用**（"连上之后世界是什么样"）——
//!   它走的是 `client/app` 用的是**同一份** `core::entrance` + `core::world` + `net::Session`
//!   （D-18），所以它能替 app 守住"连上服务端"这条链。
//!
//! 无头、无 SDL：CI/容器里也能跑。

use std::time::{Duration, Instant};

use mir2_core::entrance::Entrance;
use mir2_core::world::{Change, World};
use mir2_net::{Cmd, Ev, Session};
use mir2_protocol as proto;
use proto::envelope::Body;

pub fn main(argv: &[String]) -> i32 {
    match run(argv) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("[e2e] world 失败：{e}");
            1
        }
    }
}

struct Args {
    addr: String,
    session: i32,
    char_id: Option<u64>,
    /// 整轮的超时（毫秒）。
    timeout_ms: u64,
    /// 进世界之后再走几步（每步之间要等过服务端的限流窗口）。
    move_steps: u32,
    /// 进世界之后打这个 ActorId 一下（A′：攻击 → 伤害 → 血量）。
    attack: Option<u64>,
    /// 至少收到几条伤害事件。
    expect_damage: u64,
    /// 攻击目标打完之后应当死掉（`-attack` 配套）。
    expect_kill: bool,
    expect_map: Option<String>,
    expect_pos: Option<(i32, i32)>,
    expect_entities: Option<usize>,
    /// 期待视野里出现这个坐标上的实体（驱动方推一条移动之后用它断言"世界真的动了"）。
    expect_entity_at: Option<(i32, i32)>,
}

impl Args {
    fn parse(argv: &[String]) -> Result<Self, String> {
        let (mut addr, mut session, mut char_id) = (None, None, None);
        let (mut timeout_ms, mut move_steps) = (8000u64, 0u32);
        let (mut attack, mut expect_damage, mut expect_kill) = (None, 0u64, false);
        let (mut expect_map, mut expect_pos, mut expect_entities) = (None, None, None);
        let mut expect_entity_at = None;

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
                "-session" => session = Some(parse(&val("会话号")?, "-session")?),
                "-char" => char_id = Some(parse(&val("角色 id")?, "-char")?),
                "-timeout-ms" => timeout_ms = parse(&val("毫秒")?, "-timeout-ms")?,
                "-move-steps" => move_steps = parse(&val("步数")?, "-move-steps")?,
                "-attack" => attack = Some(parse(&val("目标 ActorId")?, "-attack")?),
                "-expect-damage" => expect_damage = parse(&val("条数")?, "-expect-damage")?,
                "-expect-kill" => expect_kill = true,
                "-expect-map" => expect_map = Some(val("地图名")?),
                "-expect-pos" => expect_pos = Some(parse_pos(&val("X,Y")?)?),
                "-expect-entities" => {
                    expect_entities = Some(parse(&val("个数")?, "-expect-entities")?)
                }
                "-expect-entity-at" => expect_entity_at = Some(parse_pos(&val("X,Y")?)?),
                other => return Err(format!("未知参数 {other}（-h 看用法）")),
            }
            i += 1;
        }
        Ok(Args {
            addr: addr.ok_or("缺少 -addr <host:port>（gamesvr -proto-addr 的地址）")?,
            session: session.ok_or("缺少 -session <会话号>")?,
            char_id,
            timeout_ms,
            move_steps,
            attack,
            expect_damage,
            expect_kill,
            expect_map,
            expect_pos,
            expect_entities,
            expect_entity_at,
        })
    }
}

fn parse<T: std::str::FromStr>(s: &str, key: &str) -> Result<T, String> {
    s.parse::<T>()
        .map_err(|_| format!("{key} 的值不合法：{s:?}"))
}

fn parse_pos(s: &str) -> Result<(i32, i32), String> {
    let (x, y) = s
        .split_once(',')
        .ok_or_else(|| format!("坐标要写成 X,Y，实得 {s:?}"))?;
    Ok((parse(x.trim(), "X")?, parse(y.trim(), "Y")?))
}

fn run(argv: &[String]) -> Result<(), String> {
    let a = Args::parse(argv)?;
    let sess = Session::spawn(a.addr.clone(), "mir2-e2e-world".into(), "zh-CN".into());
    let mut entrance = Entrance::new(a.session, a.char_id);
    let mut world = World::default();

    let deadline = Instant::now() + Duration::from_millis(a.timeout_ms);
    let mut entered_pos: Option<(i32, i32)> = None;
    let mut entered_map: Option<String> = None;
    let mut changes = 0u32;

    // 第一步：把 Reconnect 发出去。`Session` 会在握手完成后才开始写（命令排队），
    // 所以这里不必等 `Connected`。
    if let Some(b) = entrance.next_cmd() {
        send(&sess, a.session, &b)?;
    }

    while Instant::now() < deadline {
        // 批量收事件（50ms 一批）：**非阻塞**地喂状态机，主循环不会被网络按住。
        match sess.evs.recv_timeout(Duration::from_millis(50)) {
            Ok(ev) => handle_ev(
                &sess,
                a.session,
                &mut entrance,
                &mut world,
                ev,
                &mut changes,
            )?,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                return Err("会话线程结束了".into());
            }
        }

        if let Some(why) = entrance.failed() {
            return Err(why.to_string());
        }
        if entrance.in_world() {
            if entered_pos.is_none() {
                entered_pos = Some(world.self_pos);
                entered_map = Some(world.map_name.clone());
                print_world(&world);
            }
            // 进世界之后：走几步（每步等过服务端的限流窗口），再打一下，然后收工。
            if a.move_steps > 0 {
                walk(&a, &sess, &mut world, &mut changes)?;
            }
            if let Some(target) = a.attack {
                attack(&a, &sess, target)?;
            }
            break;
        }
    }

    if !entrance.in_world() {
        return Err(format!(
            "超时（{}ms）还没进世界：握手停在 {:?}",
            a.timeout_ms,
            entrance.stage()
        ));
    }

    // 再收一小会儿：驱动方（Go 契约测试）会在玩家进图后推一条实体事件，
    // 攻击的伤害/血量/死亡应答也是在这一段里到齐的。
    let mut damages = Vec::new();
    let tail = Instant::now() + Duration::from_millis(1500);
    while Instant::now() < tail {
        if let Ok(Ev::Envelope(env)) = sess.evs.recv_timeout(Duration::from_millis(50)) {
            if world.apply(&env) == Change::World {
                changes += 1;
            }
            damages.extend(world.take_damage());
        }
    }
    for d in &damages {
        println!(
            "[world] 伤害：ActorId={} 打了 ActorId={} {} 点",
            d.attacker_id, d.target_id, d.value
        );
    }
    print_world(&world);

    // ---------- 断言（只用驱动方给的真值）----------
    if let (Some(want), Some(got)) = (&a.expect_map, &entered_map) {
        if want != got {
            return Err(format!("地图 = {got:?}，应为 {want:?}"));
        }
    }
    if let (Some(want), Some(got)) = (a.expect_pos, entered_pos) {
        if want != got {
            return Err(format!("进图坐标 = {got:?}，应为 {want:?}"));
        }
    }
    if let Some(want) = a.expect_entities {
        if world.entities.len() != want {
            return Err(format!(
                "视野实体 = {} 个，应为 {want} 个",
                world.entities.len()
            ));
        }
    }
    if let Some((wx, wy)) = a.expect_entity_at {
        let hit = world.entities.values().any(|e| e.x == wx && e.y == wy);
        if !hit {
            return Err(format!(
                "视野里没有实体在 ({wx},{wy})（世界没动？见上面「视野实体」那几行）"
            ));
        }
    }
    if world.damage_events < a.expect_damage {
        return Err(format!(
            "伤害事件只有 {} 条，应为至少 {} 条（`-attack` 打了没？）",
            world.damage_events, a.expect_damage
        ));
    }
    if a.expect_kill {
        let target = a.attack.unwrap_or(0);
        match world.entities.get(&target) {
            Some(e) if e.dead => {}
            Some(e) => {
                return Err(format!(
                    "目标 ActorId={target} 还活着（hp={}/{}）—— 应当被打死",
                    e.hp, e.max_hp
                ))
            }
            None => {
                return Err(format!("目标 ActorId={target} 已经不在视野里了"));
            }
        }
    }
    if world.unknown > 0 {
        return Err(format!(
            "有 {} 条消息没看懂（未知消息/无主的移动）—— 那通常意味着两边对不上",
            world.unknown
        ));
    }

    println!(
        "世界状态通过：地图={} 进图={:?} 现在={:?} 视野={} 个实体 变更={} 次 实体事件={} 伤害={}",
        world.map_name,
        entered_pos,
        world.self_pos,
        world.entities.len(),
        changes,
        world.entity_events,
        world.damage_events
    );
    Ok(())
}

fn handle_ev(
    sess: &Session,
    session_id: i32,
    entrance: &mut Entrance,
    world: &mut World,
    ev: Ev,
    changes: &mut u32,
) -> Result<(), String> {
    match ev {
        Ev::Connected {
            version,
            capabilities,
        } => {
            println!("[world] 已连接：协议版本 {version}，能力 {capabilities:?}");
        }
        Ev::Closed(why) => return Err(format!("连接结束：{why}")),
        Ev::Envelope(env) => {
            // 两条线各管一段，**互不干扰**（都吃同一条信封）：
            //   Entrance —— 握手那几步；World —— 实体与自身状态。
            if entrance.failed().is_none() && !entrance.in_world() {
                if let Some(b) = entrance.on(&env) {
                    send(sess, session_id, &b)?;
                }
            }
            if world.apply(&env) == Change::World {
                *changes += 1;
                if entrance.in_world() {
                    println!(
                        "[world] 变化 #{}：自己={:?} 视野实体 {} 个 实体事件 {}",
                        changes,
                        world.self_pos,
                        world.entities.len(),
                        world.entity_events
                    );
                }
            }
        }
    }
    Ok(())
}

/// 进世界之后走几步（**必须**等过限流窗口：走路 600ms，见 `entity.NewMoveLimiter`）。
fn walk(a: &Args, sess: &Session, world: &mut World, changes: &mut u32) -> Result<(), String> {
    for step in 0..a.move_steps {
        // 向右走：线上编号 = `proto::Direction::DirRight`（新枚举 = 原版 + 1）。
        sess.cmds
            .send(Cmd::Move(proto::Direction::DirRight as i32))
            .map_err(|_| "命令通道已关闭".to_string())?;
        let until = Instant::now() + Duration::from_millis(900);
        while Instant::now() < until {
            match sess.evs.recv_timeout(Duration::from_millis(50)) {
                Ok(Ev::Envelope(env)) => {
                    if world.apply(&env) == Change::World {
                        *changes += 1;
                    }
                }
                Ok(Ev::Closed(why)) => return Err(format!("连接结束：{why}")),
                _ => {}
            }
        }
        println!(
            "[world] 走第 {} 步：自己={:?}（视野实体 {} 个）",
            step + 1,
            world.self_pos,
            world.entities.len()
        );
    }
    Ok(())
}

/// 把握手状态机吐出来的信封**翻译成会话命令**发出去。
///
/// 握手消息与世界消息都从**命令通道**走：这样写 socket 的只有那一个写线程，
/// 顺序天然是对的（也顺带验证了 `Cmd` 这条路是通的）。
/// 打一下目标：发 `AttackInput`，然后由调用方在收尾那段里收伤害/血量/死亡应答。
///
/// ⚠️ 方向不由我们给：新协议给的是**目标 ActorId**，服务端自己算出朝向那一格
/// （见服务端 `onAttackInput`）。
fn attack(a: &Args, sess: &Session, target: u64) -> Result<(), String> {
    sess.cmds
        .send(Cmd::Attack {
            target_id: target,
            action: proto::AttackAction::AttackHit as i32,
        })
        .map_err(|_| "命令通道已关闭".to_string())?;
    println!("[world] 攻击 ActorId={target}（普通砍）");
    let _ = a;
    Ok(())
}

fn send(sess: &Session, session_id: i32, body: &Body) -> Result<(), String> {
    let cmd = match body {
        // v0 的 token 就是会话号 ⇒ 不必从 `session_token` 里解回来（那会把
        // "编码"这一件事在两个地方各写一遍）。
        Body::Reconnect(_) => Cmd::Reconnect(session_id),
        Body::ListCharacters(_) => Cmd::ListCharacters,
        Body::SelectCharacter(s) => Cmd::SelectCharacter(s.character_id),
        other => {
            return Err(format!(
                "不支持在会话里发 {}（见 net::session::Cmd）",
                proto::body_name(other)
            ))
        }
    };
    sess.cmds
        .send(cmd)
        .map_err(|_| "命令通道已关闭".to_string())
}

fn print_world(w: &World) {
    if !w.in_world() {
        println!("[world] 还没进世界");
        return;
    }
    println!(
        "[world] 进图：自己 ActorId={} 地图={} 坐标={:?} 朝向={}",
        w.self_id,
        w.map_name,
        w.self_pos,
        dir_name(w.self_dir)
    );
    if let Some(ab) = w.ability {
        println!(
            "[world] 能力值：{} 级 hp={}/{} mp={}/{} 金币={}",
            ab.level, ab.hp, ab.max_hp, ab.mp, ab.max_mp, ab.gold
        );
    }
    println!("[world] 视野实体 {} 个：", w.entities.len());
    for e in w.entities.values() {
        println!(
            "          {} id={} 名={} ({},{}) 朝向={} hp={}/{}",
            kind_name(e.kind),
            e.id,
            e.name,
            e.x,
            e.y,
            dir_name(e.dir),
            e.hp,
            e.max_hp
        );
    }
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

fn kind_name(v: u32) -> &'static str {
    match v {
        0 => "玩家",
        1 => "怪物",
        2 => "NPC",
        _ => "未知类别",
    }
}

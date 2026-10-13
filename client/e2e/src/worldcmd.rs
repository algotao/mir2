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
//!
//! # 步骤模型（2026-10-13，从 Go 的 `mir2cli` 搬过来）
//!
//! 原来这条命令是**固定顺序**的（走 → 打 → 捡 → 点 NPC → 卖），加一个用例就得改一遍
//! 流程。`mir2cli` 的用例是"先 `@spawn` 再打、先 `@give` 再学"，顺序本身就是用例的一部分
//! ⇒ 这里改成**按命令行出现的顺序**执行 [`Step`]，每步之后收一小会儿包（`-settle-ms`）。
//!
//! 带 `nearest` 的步骤（[`Step::AttackNearest`] / [`Step::ButchNearest`] /
//! [`Step::NpcClickNearest`]）在**执行时**才从 `World` 里挑目标 —— 那是 mir2cli 里
//! "刷完怪取最近那只"的同一件事（它的 `spawnAndFindMonster`）。
//!
//! 断言一律只看"服务端真发过来的东西"：金币/背包/实体/系统消息，见 [`Expects`]。

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

// ---------- 参数 ----------

struct Args {
    addr: String,
    /// 认领既有会话（`-session`）；与 `-account`/`-password` **二选一**。
    session: Option<i32>,
    /// 口令登录（D-24① 挑战应答）—— 与 `-session` 二选一。
    account: Option<String>,
    password: Option<String>,
    char_id: Option<u64>,
    /// 整轮的超时（毫秒）。
    timeout_ms: u64,
    /// 每一步之后收多久的包（毫秒）。GM 命令的回执通常瞬间到，但刷怪/切图有广播。
    settle_ms: u64,
    /// **手动选角**：角色列表到手后选这个 id（模拟 app 的选角界面点一下）。
    pick: Option<u64>,
    /// 按命令行顺序执行的动作。
    steps: Vec<Step>,
    expects: Expects,
}

/// 一个动作。**顺序 = 命令行里的顺序**（用例本身就是一串动作，见文件头）。
#[derive(Debug)]
enum Step {
    /// 说一句话 / 发一条命令（原版 `CM_SAY`）。`@` 开头在服务端走命令分发
    /// （`@map` / `@give` / `@spawn` / `@die` / `@level` / `@gold` / `@magic` …）。
    ///
    /// ⚠️ 这是**新协议上唯一的 GM 入口**（服务端 `onChatInput` 只接 `@` 开头），
    /// 对应 mir2cli 里的 `say()` —— 它那些用例几乎全靠它铺前置条件。
    Say(String),
    /// 纯等待（毫秒）：什么都不发，只把这段时间的包收干净。
    Wait(u64),
    Move(u32),
    Run(u32),
    Attack(u64),
    /// 打**最近的一只活怪**；`until_dead` = 一直砍到它死（或超预算）。
    AttackNearest { until_dead: bool },
    Pickup(u64),
    /// 把脚下/视野里能捡的全捡一遍（`-die` 掉了几件就捡几件）。
    PickupAll,
    NpcClick(u64),
    /// 点**最近的 NPC**（屠夫/铁匠那类；mir2cli 是按坐标找，这里按实体）。
    NpcClickNearest,
    NpcSelect(u32),
    /// 卖背包里实例号最大的那件（D-75：认物只认实例号）。
    Sell,
    /// 按**名字**卖一件。
    SellName(String),
    Buy { name: String, count: u32 },
    Use(u32),
    /// 按名字用一件（`@give` 来的技能书/药水，不用先查槽位）。
    UseName(String),
    Equip(u32),
    Unequip(u32),
    Repair(u32),
    Cast { skill: u32, target: u64 },
    Butch(u64),
    /// 挖**最近的尸体**（原版 `CM_BUTCH`，死了的动物）。
    ButchNearest,
}

#[derive(Default)]
struct Expects {
    map: Option<String>,
    pos: Option<(i32, i32)>,
    entities: Option<usize>,
    entity_at: Option<(i32, i32)>,
    damage: u64,
    kill: bool,
    bag: Option<usize>,
    gold: Option<u64>,
    name_rule: bool,
    /// 系统消息里必须**出现过**这些子串（mir2cli 的 `waitSysMsgText`）。
    notices: Vec<String>,
    /// 系统消息里必须**不出现**这些子串（"不该生效"那一半，例：GM 权限门）。
    no_notices: Vec<String>,
    /// 自己死过（`@die` / 被打死）。
    death: bool,
    /// 地面上至少有几件东西（死亡掉落/扔金币）。
    ground: Option<usize>,
    /// 背包里必须有的物品名。
    bag_names: Vec<String>,
    /// `(物品名, 至少几件)` —— 叠加用例（`@give 1` + `@give 5` ⇒ 一格 6 件）。
    item_counts: Vec<(String, u32)>,
    /// 必须学会这个技能号。
    magic: Option<u32>,
    /// 视野里必须有**变成骷髅的尸体**（挖肉成功：服务端发 `Skeleton`）。
    skeleton: bool,
    /// 地图**换过**（`@map` / 死亡回城）。
    map_changed: bool,
}

impl Args {
    fn parse(argv: &[String]) -> Result<Self, String> {
        let (mut addr, mut session, mut char_id) = (None, None, None);
        let (mut account, mut password) = (None::<String>, None::<String>);
        let (mut timeout_ms, mut settle_ms) = (8000u64, 800u64);
        let mut pick = None;
        let mut steps: Vec<Step> = Vec::new();
        let mut ex = Expects::default();

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
                "-account" => account = Some(val("账号")?),
                "-password" => password = Some(val("口令")?),
                "-char" => char_id = Some(parse(&val("角色 id")?, "-char")?),
                "-timeout-ms" => timeout_ms = parse(&val("毫秒")?, "-timeout-ms")?,
                "-settle-ms" => settle_ms = parse(&val("毫秒")?, "-settle-ms")?,
                "-pick" => pick = Some(parse(&val("角色 id")?, "-pick")?),

                // ---------- 动作 ----------
                "-say" => steps.push(Step::Say(val("要说的话")?)),
                "-wait" => steps.push(Step::Wait(parse(&val("毫秒")?, "-wait")?)),
                "-move-steps" => steps.push(Step::Move(parse(&val("步数")?, "-move-steps")?)),
                "-run-steps" => steps.push(Step::Run(parse(&val("步数")?, "-run-steps")?)),
                "-attack" => steps.push(Step::Attack(parse(&val("目标 ActorId")?, "-attack")?)),
                "-attack-nearest" => {
                    let until = matches!(argv.get(i + 1).map(String::as_str), Some("-until-dead"));
                    if until {
                        i += 1;
                    }
                    steps.push(Step::AttackNearest { until_dead: until });
                }
                "-pickup" => steps.push(Step::Pickup(parse(&val("地面物品 gid")?, "-pickup")?)),
                "-pickup-all" => steps.push(Step::PickupAll),
                "-npc" => steps.push(Step::NpcClick(parse(&val("NPC ActorId")?, "-npc")?)),
                "-npc-nearest" => steps.push(Step::NpcClickNearest),
                "-npc-select" => steps.push(Step::NpcSelect(parse(&val("1-based 选项")?, "-npc-select")?)),
                "-sell" => steps.push(Step::Sell),
                "-sell-name" => steps.push(Step::SellName(val("物品名")?)),
                // `-buy 金创药(小量)` 或 `-buy 金创药(小量):5`
                "-buy" => steps.push(parse_buy(&val("商品名[:数量]")?)?),
                "-use" => steps.push(Step::Use(parse(&val("槽位")?, "-use")?)),
                "-use-name" => steps.push(Step::UseName(val("物品名")?)),
                "-equip" => steps.push(Step::Equip(parse(&val("槽位")?, "-equip")?)),
                "-unequip" => steps.push(Step::Unequip(parse(&val("装备槽")?, "-unequip")?)),
                "-repair" => steps.push(Step::Repair(parse(&val("槽位")?, "-repair")?)),
                // `-cast 3` 打空地 / `-cast 3:1000001` 打某只怪
                "-cast" => steps.push(parse_cast(&val("技能号[:目标]")?)?),
                "-butch" => steps.push(Step::Butch(parse(&val("尸体 ActorId")?, "-butch")?)),
                "-butch-nearest" => steps.push(Step::ButchNearest),
                // 用例预设（从 `server/cmd/mir2cli` 搬过来的那批，见 `scenario`）
                "-scenario" => {
                    let name = val("用例名")?;
                    let (s, e) = scenario(&name)?;
                    steps.extend(s);
                    merge_expects(&mut ex, e);
                }

                // ---------- 断言 ----------
                "-expect-map" => ex.map = Some(val("地图名")?),
                "-expect-pos" => ex.pos = Some(parse_pos(&val("X,Y")?)?),
                "-expect-entities" => ex.entities = Some(parse(&val("个数")?, "-expect-entities")?),
                "-expect-entity-at" => ex.entity_at = Some(parse_pos(&val("X,Y")?)?),
                "-expect-damage" => ex.damage = parse(&val("条数")?, "-expect-damage")?,
                "-expect-kill" => ex.kill = true,
                "-expect-bag" => ex.bag = Some(parse(&val("件数")?, "-expect-bag")?),
                "-expect-gold" => ex.gold = Some(parse(&val("金币数")?, "-expect-gold")?),
                "-expect-name-rule" => ex.name_rule = true,
                "-expect-notice" => ex.notices.push(val("子串")?),
                "-expect-no-notice" => ex.no_notices.push(val("子串")?),
                "-expect-death" => ex.death = true,
                "-expect-ground" => ex.ground = Some(parse(&val("件数")?, "-expect-ground")?),
                "-expect-bag-name" => ex.bag_names.push(val("物品名")?),
                "-expect-item" => ex.item_counts.push(parse_item(&val("物品名[:数量]")?)?),
                "-expect-magic" => ex.magic = Some(parse(&val("技能号")?, "-expect-magic")?),
                "-expect-map-change" => ex.map_changed = true,
                other => return Err(format!("未知参数 {other}（-h 看用法）")),
            }
            i += 1;
        }
        Ok(Args {
            addr: addr.ok_or("缺少 -addr <host:port>（gamesvr -proto-addr 的地址）")?,
            session,
            account,
            password,
            char_id,
            timeout_ms,
            settle_ms,
            pick,
            steps,
            expects: ex,
        })
    }
}

/// 用例预设：从 `server/cmd/mir2cli` 搬过来的、**新协议这边两端都齐**的那批。
///
/// ⚠️ 只搬"单角色 + GM 命令"能跑的：需要第二个角色（`-peer`）的那批
/// （聊天/PvP/组队/行会/争霸/城门）在新协议上没有下行出口，搬不了（见 README）。
fn scenario(name: &str) -> Result<(Vec<Step>, Expects), String> {
    let mut ex = Expects::default();
    let steps = match name {
        // 原 `-maptest`：@map 过去再 @map 回来，两边都要收到 ChangeMap
        "maptest" => {
            ex.map_changed = true;
            vec![
                Step::Say("@map 0101".into()),
                Step::Say("@map 0".into()),
            ]
        }
        // 原 `-stack`：可叠加物再给 5 个，**占的格数不增加**（合成一格 6 个）
        "stack" => {
            ex.item_counts.push(("金创药(小量)".into(), 6));
            vec![
                Step::Say("@give 金创药(小量) 1".into()),
                Step::Say("@give 金创药(小量) 5".into()),
            ]
        }
        // 原 `-gold`：@gold 10000 ⇒ 金币真的到 10000
        "gold" => {
            ex.gold = Some(10_000);
            vec![Step::Say("@gold 10000".into())]
        }
        // 原 `-die`：死在远处 ⇒ 掉东西 + 回城（换图）
        "die" => {
            ex.death = true;
            ex.map_changed = true;
            vec![
                Step::Say("@map 3".into()),
                Step::Say("@give 金创药(小量) 3".into()),
                Step::Say("@die".into()),
                Step::Wait(1200),
            ]
        }
        // 原 `-butch`：刷只鸡 → 砍死 → 挖肉 ⇒ 尸体变骷髅
        // 挖完服务端发 `Skeleton`（尸体变骷髅）⇒ 这句就是它的判据
        "butch" => {
            ex.skeleton = true;
            vec![
                Step::Say("@level 40".into()),
                Step::Say("@spawn 鸡 1".into()),
                Step::AttackNearest { until_dead: true },
                Step::ButchNearest,
            ]
        }
        // 原 `-shop`：走到屠夫 → 开对话 → 买一件 → 再卖掉 ⇒ 金币回来
        "shop" => {
            ex.gold = Some(1);
            vec![
                Step::Say("@gold 10000".into()),
                Step::Say("@map 0 313 271".into()),
                Step::Wait(1200),
                Step::NpcClickNearest,
                Step::Buy {
                    name: "金创药(小量)".into(),
                    count: 1,
                },
                Step::SellName("金创药(小量)".into()),
            ]
        }
        // 原 `-learn`：等级不够吃书不该学会，@level 之后吃必须"学会了"
        "learn" => {
            ex.notices.push("学会了".into());
            vec![
                Step::Say("@give 雷电术".into()),
                Step::UseName("雷电术".into()),
                Step::Say("@level 20".into()),
                Step::UseName("雷电术".into()),
            ]
        }
        // 原 `-gmtest`：权限门。判据由驱动方给
        // （在名单里 ⇒ `-expect-notice 权限` 之外；不在 ⇒ `-expect-notice 没有足够的权限`）
        "gm" => vec![Step::Say("@level 30".into())],
        other => {
            return Err(format!(
                "未知用例 {other}（有：maptest / stack / gold / die / butch / shop / learn / gm）"
            ))
        }
    };
    Ok((steps, ex))
}

fn merge_expects(dst: &mut Expects, src: Expects) {
    dst.map = dst.map.take().or(src.map);
    dst.pos = dst.pos.or(src.pos);
    dst.entities = dst.entities.or(src.entities);
    dst.entity_at = dst.entity_at.or(src.entity_at);
    dst.damage = dst.damage.max(src.damage);
    dst.kill |= src.kill;
    dst.bag = dst.bag.or(src.bag);
    dst.gold = dst.gold.or(src.gold);
    dst.name_rule |= src.name_rule;
    dst.death |= src.death;
    dst.ground = dst.ground.or(src.ground);
    dst.magic = dst.magic.or(src.magic);
    dst.map_changed |= src.map_changed;
    dst.notices.extend(src.notices);
    dst.no_notices.extend(src.no_notices);
    dst.bag_names.extend(src.bag_names);
    dst.item_counts.extend(src.item_counts);
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

/// `-buy 名字` / `-buy 名字:数量`
fn parse_buy(s: &str) -> Result<Step, String> {
    let (name, count) = match s.rsplit_once(':') {
        Some((n, c)) => (n, parse(c, "-buy 数量")?),
        None => (s, 1u32),
    };
    Ok(Step::Buy {
        name: name.into(),
        count,
    })
}

/// `-cast 技能号` / `-cast 技能号:目标ActorId`
fn parse_cast(s: &str) -> Result<Step, String> {
    let (skill, target) = match s.split_once(':') {
        Some((a, b)) => (parse(a, "-cast 技能号")?, parse(b, "-cast 目标")?),
        None => (parse(s, "-cast 技能号")?, 0u64),
    };
    Ok(Step::Cast { skill, target })
}

/// `-expect-item 名字` / `-expect-item 名字:数量`
fn parse_item(s: &str) -> Result<(String, u32), String> {
    match s.rsplit_once(':') {
        Some((n, c)) => Ok((n.into(), parse(c, "-expect-item 数量")?)),
        None => Ok((s.into(), 1)),
    }
}

// ---------- 主流程 ----------

fn run(argv: &[String]) -> Result<(), String> {
    let a = Args::parse(argv)?;
    let sess = Session::spawn(a.addr.clone(), "mir2-e2e-world".into(), "zh-CN".into());
    // 两个入口，之后是同一条尾巴（见 `core::entrance` 的文件头）：
    //   -account/-password ⇒ 口令登录（D-24① 挑战应答）
    //   -session           ⇒ 认领既有会话
    let mut entrance = match (&a.account, &a.password) {
        (Some(acc), Some(pw)) => Entrance::new_with_password(acc.clone(), pw.clone(), a.char_id),
        (None, None) => Entrance::new(
            a.session
                .ok_or("要么给 -session <会话号>，要么给 -account/-password")?,
            a.char_id,
        ),
        _ => return Err("-account 与 -password 要一起给".into()),
    };
    // `-pick` 给了就**自己选**（模拟 app 的选角界面）；不给就沿用状态机的自动选角
    if a.pick.is_some() {
        entrance.set_manual_pick(true);
    }
    let mut world = World::default();

    let deadline = Instant::now() + Duration::from_millis(a.timeout_ms);
    let (mut entered_pos, mut entered_map) = (None, None);
    let mut changes = 0u32;
    let mut notices: Vec<String> = Vec::new();

    // 第一步：把 Reconnect 发出去。`Session` 会在握手完成后才开始写（命令排队），
    // 所以这里不必等 `Connected`。
    if let Some(b) = entrance.next_cmd() {
        send(&sess, a.session.unwrap_or(0), &b)?;
    }

    while Instant::now() < deadline {
        match sess.evs.recv_timeout(Duration::from_millis(50)) {
            Ok(ev) => handle_ev(
                &sess,
                a.session.unwrap_or(0),
                &mut entrance,
                &mut world,
                ev,
                &mut changes,
                a.pick,
            )?,
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                return Err("会话线程结束了".into());
            }
        }
        notices.extend(world.take_notices());

        if let Some(why) = entrance.failed() {
            return Err(why.to_string());
        }
        if entrance.in_world() {
            if entered_pos.is_none() {
                entered_pos = Some(world.self_pos);
                entered_map = Some(world.map_name.clone());
                print_world(&world);
            }
            break;
        }
    }

    if a.expects.name_rule {
        let rule = entrance
            .name_rule
            .as_ref()
            .ok_or_else(|| "CharacterList 没有下发 NameRule（D-84）".to_string())?;
        if rule.min_bytes == 0 || rule.bad_chars.is_empty() || rule.message.is_empty() {
            return Err(format!("服务端下发的 NameRule 不完整：{rule:?}"));
        }
        println!(
            "[world] 角色名规则来自服务端：min_bytes={} bad_chars={:?}",
            rule.min_bytes, rule.bad_chars
        );
    }
    if !entrance.in_world() {
        return Err(format!(
            "超时（{}ms）还没进世界：握手停在 {:?}",
            a.timeout_ms,
            entrance.stage()
        ));
    }

    // ---------- 按命令行顺序跑步骤 ----------
    for step in &a.steps {
        run_step(&sess, &mut world, step, a.settle_ms, &mut changes)?;
        notices.extend(world.take_notices());
    }
    // 再收一小会儿：最后一步的回执（掉落/背包刷新/金币）常常落在这一步之后。
    settle(&sess, &mut world, 1500, &mut changes);
    notices.extend(world.take_notices());

    print_world(&world);
    if !notices.is_empty() {
        println!("[world] 系统消息 {} 条：", notices.len());
        for n in &notices {
            println!("          {n}");
        }
    }

    // ---------- 断言（只用服务端真发过来的东西）----------
    let ex = &a.expects;
    if let (Some(want), Some(got)) = (&ex.map, &entered_map) {
        if want != got {
            return Err(format!("地图 = {got:?}，应为 {want:?}"));
        }
    }
    if let (Some(want), Some(got)) = (ex.pos, entered_pos) {
        if want != got {
            return Err(format!("进图坐标 = {got:?}，应为 {want:?}"));
        }
    }
    if let Some(want) = ex.entities {
        if world.entities.len() != want {
            return Err(format!(
                "视野实体 = {} 个，应为 {want} 个",
                world.entities.len()
            ));
        }
    }
    if let Some((wx, wy)) = ex.entity_at {
        let hit = world.entities.values().any(|e| e.x == wx && e.y == wy);
        if !hit {
            return Err(format!("视野里没有实体在 ({wx},{wy})"));
        }
    }
    if world.damage_events < ex.damage {
        return Err(format!(
            "伤害事件只有 {} 条，应为至少 {} 条",
            world.damage_events, ex.damage
        ));
    }
    if ex.kill {
        let dead = world.entities.values().any(|e| e.dead);
        if !dead {
            return Err("没有目标被打死（`-attack-nearest -until-dead` 打了没？）".into());
        }
    }
    if let Some(want) = ex.bag {
        let got = bag_count(&world);
        if got < want {
            return Err(format!("背包里只有 {got} 件，应为至少 {want} 件"));
        }
    }
    for name in &ex.bag_names {
        if !world.bag.iter().flatten().any(|it| it.name == *name) {
            return Err(format!("背包里没有「{name}」"));
        }
    }
    for (name, want) in &ex.item_counts {
        let got = world
            .bag
            .iter()
            .flatten()
            .filter(|it| it.name == *name)
            .map(|it| it.count)
            .sum::<u32>();
        if got < *want {
            return Err(format!("「{name}」只有 {got} 个，应为至少 {want} 个（叠加了没？）"));
        }
        println!("[world] 「{name}」{got} 个（要求至少 {want}）");
    }
    if let Some(want) = ex.gold {
        let gold = world.ability.as_ref().map_or(0, |ab| ab.gold);
        if gold < want {
            return Err(format!("金币只有 {gold}，应为至少 {want}"));
        }
        println!("[world] 金币：{gold}（要求至少 {want}）");
    }
    for sub in &ex.notices {
        if !notices.iter().any(|n| n.contains(sub.as_str())) {
            return Err(format!(
                "系统消息里没有出现「{sub}」（共 {} 条：{:?}）",
                notices.len(),
                &notices[..notices.len().min(5)]
            ));
        }
    }
    for sub in &ex.no_notices {
        if notices.iter().any(|n| n.contains(sub.as_str())) {
            return Err(format!("系统消息里**不该**出现「{sub}」，但出现了"));
        }
    }
    if ex.death && world.self_deaths == 0 {
        return Err("自己没死过（`@die` 打了没？）".into());
    }
    if let Some(want) = ex.ground {
        if world.ground.len() < want {
            return Err(format!(
                "地面上只有 {} 件，应为至少 {want} 件（掉落了吗？）",
                world.ground.len()
            ));
        }
    }
    if let Some(skill) = ex.magic {
        if !world.magics.iter().any(|m| m.skill_id == skill) {
            return Err(format!(
                "没有学会技能 {skill}（已会 {} 个：{:?}）",
                world.magics.len(),
                world.magics.iter().map(|m| m.skill_id).collect::<Vec<_>>()
            ));
        }
    }
    if ex.skeleton && !world.entities.values().any(|e| e.skeleton) {
        return Err("没有尸体变成骷髅（挖肉没成功？）".into());
    }
    if ex.map_changed && world.map_changes == 0 {
        return Err("地图没换过（`@map` 生效了吗？）".into());
    }
    // ⚠️ 硬断言只针对**解不出的信封**（协议漂移）；"无主的实体更新"另算，只报告
    // （它是"服务端账本比视野大一点"，不是两边对不上 —— 混在一起会让场景测试乱响）。
    if world.unknown > 0 {
        return Err(format!(
            "有 {} 条消息**解不出来** —— 那通常意味着两端协议对不上",
            world.unknown
        ));
    }
    if world.orphan_events > 0 {
        println!("[world] 注：{} 条无主更新（没见过的实体在动/掉血）—— 只报告不断言", world.orphan_events);
    }

    println!(
        "世界状态通过：地图={} 进图={:?} 现在={:?} 视野={} 个实体 变更={} 次 伤害={} 换图={} 系统消息={}",
        world.map_name,
        entered_pos,
        world.self_pos,
        world.entities.len(),
        changes,
        world.damage_events,
        world.map_changes,
        notices.len()
    );
    Ok(())
}

/// 收 `ms` 毫秒的包（**每步之后都要收**：回执/广播都在这段时间里到）。
fn settle(sess: &Session, world: &mut World, ms: u64, changes: &mut u32) {
    let until = Instant::now() + Duration::from_millis(ms);
    while Instant::now() < until {
        if let Ok(Ev::Envelope(env)) = sess.evs.recv_timeout(Duration::from_millis(50)) {
            if world.apply(&env) == Change::World {
                *changes += 1;
            }
        }
    }
}

fn bag_count(w: &World) -> usize {
    w.bag.iter().flatten().count()
}

/// 最近的一只**活着的怪**（mir2cli 的 `spawnAndFindMonster` 是"刷完取第一个 SM_TURN"，
/// 这里按距离取 —— 新协议有坐标，不用靠到达顺序猜）。
fn nearest_monster(w: &World) -> Option<u64> {
    let (sx, sy) = w.self_pos;
    w.entities
        .values()
        .filter(|e| e.kind == KIND_MONSTER && !e.dead)
        .min_by_key(|e| (e.x - sx).abs() + (e.y - sy).abs())
        .map(|e| e.id)
}

/// 朝目标走一步该用哪个方向；**已经贴身**（曼哈顿距离 ≤1）就返回 `None`。
///
/// 只走单轴（上下左右），不走斜向 —— 见 [`Step::AttackNearest`] 里那条说明。
fn step_toward(w: &World, target: u64) -> Option<i32> {
    let e = w.entities.get(&target)?;
    let (dx, dy) = (e.x - w.self_pos.0, e.y - w.self_pos.1);
    if dx.abs() + dy.abs() <= 1 {
        return None;
    }
    // 取差得多的那个轴；相等时先走横向（与 mir2cli 的 `dirTowardAxis` 一致）
    Some(if dx.abs() >= dy.abs() {
        if dx > 0 {
            proto::Direction::DirRight as i32
        } else {
            proto::Direction::DirLeft as i32
        }
    } else if dy > 0 {
        proto::Direction::DirDown as i32
    } else {
        proto::Direction::DirUp as i32
    })
}

/// 最近的**尸体**（挖肉的对象：死了、还没变成骷髅）。
fn nearest_corpse(w: &World) -> Option<u64> {
    let (sx, sy) = w.self_pos;
    w.entities
        .values()
        .filter(|e| e.kind == KIND_MONSTER && e.dead && !e.skeleton)
        .min_by_key(|e| (e.x - sx).abs() + (e.y - sy).abs())
        .map(|e| e.id)
}

fn nearest_npc(w: &World) -> Option<u64> {
    let (sx, sy) = w.self_pos;
    w.entities
        .values()
        .filter(|e| e.kind == KIND_NPC && !e.dead)
        .min_by_key(|e| (e.x - sx).abs() + (e.y - sy).abs())
        .map(|e| e.id)
}

/// 背包里这件物品在**哪个槽位**（按名字找；`MakeIndex` 才是身份，但发命令用的是槽位）。
fn slot_of_name(w: &World, name: &str) -> Option<u32> {
    w.bag
        .iter()
        .position(|it| it.as_ref().is_some_and(|i| i.name == name))
        .map(|p| p as u32)
}

fn run_step(
    sess: &Session,
    world: &mut World,
    step: &Step,
    settle_ms: u64,
    changes: &mut u32,
) -> Result<(), String> {
    match step {
        Step::Say(text) => {
            cmd(sess, Cmd::Say(text.clone()))?;
            println!("[world] 说：{text}");
            settle(sess, world, settle_ms, changes);
        }
        Step::Wait(ms) => settle(sess, world, *ms, changes),
        Step::Move(n) => {
            for step_no in 0..*n {
                // 向右走：线上编号 = `proto::Direction::DirRight`（新枚举 = 原版 + 1）。
                cmd(sess, Cmd::Move(proto::Direction::DirRight as i32))?;
                settle(sess, world, 900, changes);
                println!(
                    "[world] 走第 {} 步：自己={:?}（视野实体 {} 个）",
                    step_no + 1,
                    world.self_pos,
                    world.entities.len()
                );
            }
        }
        Step::Run(n) => {
            for step_no in 0..*n {
                cmd(sess, Cmd::Run(proto::Direction::DirRight as i32))?;
                settle(sess, world, 900, changes);
                println!(
                    "[world] 跑第 {} 步：自己={:?}（视野实体 {} 个）",
                    step_no + 1,
                    world.self_pos,
                    world.entities.len()
                );
            }
        }
        Step::Attack(target) => {
            cmd(sess, Cmd::Attack {
                target_id: *target,
                action: proto::AttackAction::AttackHit as i32,
            })?;
            println!("[world] 攻击 ActorId={target}（普通砍）");
            settle(sess, world, settle_ms, changes);
        }
        Step::AttackNearest { until_dead } => {
            let mut hits = 0;
            let mut walks = 0;
            loop {
                let Some(id) = nearest_monster(world) else {
                    return Err("视野里没有活着的怪（`@spawn` 刷了吗？）".into());
                };
                // ⚠️ 近战**只打相邻格**（mir2cli 也踩过：`@spawn` 出来的怪不一定贴身，
                // 站远处挥刀服务端直接不结算）⇒ 先沿**单轴**走过去再打。
                // 单轴而非斜向：斜向要求横竖两格同时可走，密集地形会一直撞墙
                //（mir2cli 的 `dirTowardAxis` 就是为这个分的家）。
                if let Some(dir) = step_toward(world, id) {
                    if walks >= 20 {
                        return Err(format!("走了 {walks} 步还是没贴到 ActorId={id}（被地形挡住了？）"));
                    }
                    walks += 1;
                    cmd(sess, Cmd::Move(dir))?;
                    settle(sess, world, 700, changes);
                    continue;
                }
                cmd(sess, Cmd::Attack {
                    target_id: id,
                    action: proto::AttackAction::AttackHit as i32,
                })?;
                hits += 1;
                settle(sess, world, 700, changes);
                // 不在视野里也算"打没了"（服务端会把尸体收走）⇒ 用 `map_or`
                let dead = world.entities.get(&id).map_or(true, |e| e.dead);
                if dead || !until_dead || hits >= 40 {
                    println!("[world] 砍 ActorId={id}：{hits} 刀，dead={dead}");
                    break;
                }
            }
        }
        Step::Pickup(gid) => {
            cmd(sess, Cmd::PickupItem { ground_id: *gid })?;
            println!("[world] 捡起地面物品 gid={gid}");
            settle(sess, world, settle_ms, changes);
        }
        Step::PickupAll => {
            let ids: Vec<u64> = world.ground.keys().copied().collect();
            if ids.is_empty() {
                println!("[world] 地面上没有东西可捡");
            }
            for gid in ids {
                cmd(sess, Cmd::PickupItem { ground_id: gid })?;
                settle(sess, world, 400, changes);
                println!("[world] 捡起 gid={gid}（背包 {} 件）", bag_count(world));
            }
        }
        Step::NpcClick(id) => {
            cmd(sess, Cmd::NpcClick(*id))?;
            println!("[world] 点 NPC ActorId={id} 开对话");
            settle(sess, world, settle_ms, changes);
        }
        Step::NpcClickNearest => {
            let id = nearest_npc(world).ok_or("视野里没有 NPC")?;
            cmd(sess, Cmd::NpcClick(id))?;
            println!("[world] 点最近的 NPC ActorId={id} 开对话");
            settle(sess, world, settle_ms, changes);
        }
        Step::NpcSelect(index) => {
            let npc_id = world.dialog.as_ref().map(|d| d.npc_id).unwrap_or(0);
            cmd(sess, Cmd::NpcSelect { npc_id, index: *index })?;
            println!("[world] 选择 NPC 对话选项 #{index}");
            settle(sess, world, settle_ms, changes);
        }
        Step::Sell => {
            let npc_id = world.dialog.as_ref().map(|d| d.npc_id).unwrap_or(0);
            // ⚠️ 认物**只认实例号**（D-75）：背包会被服务端重排，下标会漂。
            let make = world
                .bag
                .iter()
                .flatten()
                .map(|it| it.make_index)
                .max()
                .unwrap_or(0);
            if make == 0 {
                return Err("背包里没有可卖的东西".into());
            }
            cmd(sess, Cmd::ShopSell { npc_id, make_index: make, count: 1 })?;
            println!("[world] 卖给 NPC={npc_id}：实例号 {make} ×1");
            settle(sess, world, settle_ms, changes);
        }
        Step::SellName(name) => {
            let npc_id = world.dialog.as_ref().map(|d| d.npc_id).unwrap_or(0);
            let make = world
                .bag
                .iter()
                .flatten()
                .find(|it| it.name == *name)
                .map(|it| it.make_index)
                .unwrap_or(0);
            if make == 0 {
                return Err(format!("背包里没有「{name}」可卖"));
            }
            cmd(sess, Cmd::ShopSell { npc_id, make_index: make, count: 1 })?;
            println!("[world] 卖出「{name}」实例号 {make}");
            settle(sess, world, settle_ms, changes);
        }
        Step::Buy { name, count } => {
            let npc_id = world.dialog.as_ref().map(|d| d.npc_id).unwrap_or(0);
            cmd(sess, Cmd::ShopBuy { npc_id, name: name.clone(), count: *count })?;
            println!("[world] 买入「{name}」×{count}");
            settle(sess, world, settle_ms, changes);
        }
        Step::Use(slot) => {
            cmd(sess, Cmd::UseItem { slot: *slot })?;
            println!("[world] 使用背包槽位 {slot}");
            settle(sess, world, settle_ms, changes);
        }
        Step::UseName(name) => {
            let slot = slot_of_name(world, name)
                .ok_or_else(|| format!("背包里没有「{name}」（`@give` 了吗？）"))?;
            cmd(sess, Cmd::UseItem { slot })?;
            println!("[world] 使用「{name}」（槽位 {slot}）");
            settle(sess, world, settle_ms, changes);
        }
        Step::Equip(slot) => {
            cmd(sess, Cmd::EquipItem { slot: *slot })?;
            settle(sess, world, settle_ms, changes);
        }
        Step::Unequip(slot) => {
            cmd(sess, Cmd::UnequipItem { equip_slot: *slot })?;
            settle(sess, world, settle_ms, changes);
        }
        Step::Repair(slot) => {
            cmd(sess, Cmd::RepairItem { slot: *slot, special: false })?;
            settle(sess, world, settle_ms, changes);
        }
        Step::Cast { skill, target } => {
            let (x, y) = world.self_pos;
            cmd(sess, Cmd::CastSkill { skill_id: *skill, target_entity_id: *target, x, y })?;
            println!("[world] 施放技能 {skill}（目标 {target}）");
            settle(sess, world, settle_ms, changes);
        }
        Step::Butch(id) => {
            let (x, y) = world
                .entities
                .get(id)
                .map_or(world.self_pos, |e| (e.x, e.y));
            cmd(sess, Cmd::Butch { target_id: *id, x, y, direction: 0 })?;
            println!("[world] 挖肉 ActorId={id}");
            settle(sess, world, settle_ms, changes);
        }
        Step::ButchNearest => {
            let id = nearest_corpse(world).ok_or("视野里没有可挖的尸体（怪砍死了吗？）")?;
            let (x, y) = world.entities.get(&id).map_or(world.self_pos, |e| (e.x, e.y));
            cmd(sess, Cmd::Butch { target_id: id, x, y, direction: 0 })?;
            println!("[world] 挖最近的尸体 ActorId={id}");
            settle(sess, world, settle_ms, changes);
        }
    }
    Ok(())
}

fn cmd(sess: &Session, c: Cmd) -> Result<(), String> {
    sess.cmds.send(c).map_err(|_| "命令通道已关闭".to_string())
}

// ---------- 握手 ----------

#[allow(clippy::too_many_arguments)]
fn handle_ev(
    sess: &Session,
    session_id: i32,
    entrance: &mut Entrance,
    world: &mut World,
    ev: Ev,
    changes: &mut u32,
    pick: Option<u64>,
) -> Result<(), String> {
    match ev {
        Ev::Connected {
            version,
            capabilities,
            // ⚠️ 登录要把口令证明绑在它上面（`core::auth::proof`）—— 每条连接一次，
            // 所以必须立刻交给握手状态机，晚了就发不出证明。
            nonce,
        } => {
            entrance.on_nonce(&nonce);
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
            // **手动选角**：状态机停在"等你选"时，按 `-pick` 选一个。
            if entrance.stage() == &my_stage_pick() {
                for c in entrance.characters() {
                    println!(
                        "[world] 角色 id={} 名={} 等级={} 职业={} 性别={}",
                        c.character_id, c.name, c.level, c.class, c.gender
                    );
                }
                let id = pick.or_else(|| entrance.characters().first().map(|c| c.character_id));
                match id {
                    Some(id) => {
                        println!("[world] 选角：选 id={id}");
                        if let Some(b) = entrance.pick(id) {
                            send(sess, session_id, &b)?;
                        }
                    }
                    None => println!("[world] 选角：这个账号一个角色都没有"),
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

/// 状态机那个"等你选"的阶段（抽一下，省得每个调用点写全路径）。
fn my_stage_pick() -> mir2_core::entrance::Stage {
    mir2_core::entrance::Stage::AwaitPick
}

fn send(sess: &Session, session_id: i32, body: &Body) -> Result<(), String> {
    let cmd = match body {
        // v0 的 token 就是会话号 ⇒ 不必从 `session_token` 里解回来（那会把
        // "编码"这一件事在两个地方各写一遍）。
        Body::Reconnect(_) => Cmd::Reconnect(session_id),
        // 口令登录（D-24①）：两步都是状态机吐出来的，这里只做"翻译"。
        // ⚠️ 证明是**算好的十六进制串**，不是口令 —— 口令到此为止没离开过 `core::auth`。
        Body::LoginSaltRequest(r) => Cmd::LoginSaltRequest(r.account.clone()),
        Body::Login(l) => Cmd::Login {
            account: l.account.clone(),
            proof_hex: l.password_hash.clone(),
        },
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

const KIND_MONSTER: u32 = 1;
const KIND_NPC: u32 = 2;

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

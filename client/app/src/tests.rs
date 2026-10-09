//! `mir2-app` 的单元测试（从 `main.rs` 拆出来，2026-10-09）。
//!
//! 全是纯函数/纯逻辑的判据（渲染要真窗口，不在这一层）。
//! 用了 `use crate::*;` —— 本 crate 顶层项改名/搬家时这里会**编译期**报出来。

use crate::*;
use mir2_core::map::Layer;
use mir2_core::world::Entity;
use sdl3::render::FRect;
// ⚠️ 这条被 `cargo fix` 当成「没用到的导入」删过一次（它只在各个 `#[test]` 体里才用到）
// —— 拆文件时踩过：测试目标直接编译不过。别删。
use mir2_core::actor as A;

fn ent(kind: u32, f: mir2_protocol::EntityFeature) -> Entity {
    Entity {
        id: 7,
        kind,
        name: "甲".into(),
        x: 3,
        y: 4,
        dir: 5, // 协议方向 5 = 下 ⇒ 原版 4
        feature: Some(f),
        hp: 10,
        max_hp: 20,
        run: false,
        status_bits: 0,
        dead: false,
        action: None,
    }
}

/// 「待发命令要**每帧**排空，不能只在收到信封时排」——
/// 这曾经是个真 bug：`Ev::Connected` 之后要发的 `LoginSaltRequest` 被塞在
/// 信封 arm 里拉，于是得等 20 秒后的心跳应答才出去（输完账号等 20 秒才开门）。
#[test]
fn 待发命令不依赖入站包() {
    use mir2_protocol::envelope::Body;
    let mut e = mir2_core::entrance::Entrance::new_with_password("test".into(), "pw".into(), None);

    // 第一帧（还没收到任何信封）就该把"要盐"的请求发出去
    let mut sent = Vec::new();
    flush_entrance(&mut e, &mut |b| sent.push(b.clone()));
    assert_eq!(sent.len(), 1, "第一帧就该发 LoginSaltRequest");
    match &sent[0] {
        Body::LoginSaltRequest(r) => assert_eq!(r.account, "test"),
        other => panic!("第一条应当是 LoginSaltRequest，实得 {other:?}"),
    }

    // 阶段已前进到 AwaitSalt ⇒ 再排也不能重复发
    let mut again = Vec::new();
    flush_entrance(&mut e, &mut |b| again.push(b.clone()));
    assert!(again.is_empty(), "同一阶段不该重复发命令");
}

/// 调试功能关掉之后，提示条**不能**还写着那些键。
///
/// 这条盯的是"关掉了但界面还在教人按"这种半拉子状态：翻开关时容易忘了
/// 同步提示条，而症状是"按了没反应"（用户会当成 bug 来报）。
#[test]
fn 提示条跟着调试开关走() {
    let h = hint_text(2);
    assert!(
        h.contains("WALK") && h.contains("CONNECT"),
        "正常玩法提示要还在：{h}"
    );
    if DEBUG_LAYERS || DEBUG_OVERLAY {
        // 开着的时候要**写着**（否则等于藏了一个没人知道的调试入口）
        if DEBUG_OVERLAY {
            assert!(h.contains("D DEBUG"), "{h}");
        }
    } else {
        assert!(!h.contains("1/2/3"), "图层键已关，提示里不该还有：{h}");
        assert!(!h.contains("D DEBUG"), "叠加层已关，提示里不该还有：{h}");
    }
}

/// 脚步的边沿判定：**帧 1 / 帧 4 各一次，同一帧号只响一次**。
///
/// 原版就是这么对齐的（`Actor.pas:2659-2660`）；帧 4 那一声是第二只脚。
#[test]
fn 脚步只在帧1帧4响() {
    assert_eq!(footstep_of(1, Some(0)), Some(false), "帧 1 ⇒ 第一只脚");
    assert_eq!(footstep_of(1, Some(1)), None, "同一个帧号只响一次");
    assert_eq!(footstep_of(2, Some(1)), None);
    assert_eq!(footstep_of(3, Some(2)), None);
    assert_eq!(footstep_of(4, Some(3)), Some(true), "帧 4 ⇒ 第二只脚");
    assert_eq!(footstep_of(4, Some(4)), None);
    assert_eq!(footstep_of(0, None), None, "站立/起手不响");
}

/// 连不上时要给出"下一步查什么"，而且**不能**把人往"密码错"上引。
#[test]
fn 连接失败的提示() {
    let h = connect_hint("连接 127.0.0.1:7500 失败：IO: Connection refused (os error 61)");
    assert!(h.contains("-proto-addr"), "该提示去查新协议入口：{h}");
    assert!(h.contains("Connection refused"), "原始原因要留着");
    let t = connect_hint("read tcp: i/o timeout");
    assert!(t.contains("超时"));
    assert_eq!(connect_hint("被服务端断开 105"), "被服务端断开 105");
}

/// 玩家的本体：容器是 `Hum`，图号 = `600*Dress + 站立段 + 方向步长`。
#[test]
fn 玩家本体走_hum() {
    let f = mir2_protocol::EntityFeature {
        dress: 10,
        ..Default::default()
    };
    let (lib, idx) = body_sprite(&ent(0, f), None, Instant::now()).expect("玩家该有精灵");
    assert_eq!(lib, A::HUM_LIB);
    assert_eq!(idx, A::human_index(10, A::HAct::Stand, 4, 0));
    assert_eq!(idx, 600 * 10 + 4 * 8);
}

/// 武器层只有**手上有东西**时才画（`weapon == 0` 是空手）。
#[test]
fn 武器层_空手不画() {
    let bare = mir2_protocol::EntityFeature {
        dress: 1,
        ..Default::default()
    };
    assert!(weapon_sprite(&ent(0, bare), None, Instant::now()).is_none());
    let armed = mir2_protocol::EntityFeature {
        dress: 1,
        weapon: 21,
        ..Default::default()
    };
    let (lib, idx) = weapon_sprite(&ent(0, armed), None, Instant::now()).unwrap();
    assert_eq!(
        (lib, idx),
        (A::WEAPON_LIB, A::human_index(21, A::HAct::Stand, 4, 0))
    );
}

/// 怪物：容器由图里的 `Appr` 定、动作表由 `RaceImg` 定。
#[test]
fn 怪物走_appr() {
    let f = mir2_protocol::EntityFeature {
        race_img: 19,
        appr: 151,
        ..Default::default()
    };
    let (lib, idx) = body_sprite(&ent(1, f), None, Instant::now()).unwrap();
    assert_eq!(lib, A::mon_container(151).unwrap());
    assert_eq!(idx, A::monster_index(151, 19, A::MAct::Stand, 4, 0));
    assert!(weapon_sprite(&ent(1, f), None, Instant::now()).is_none());
}

/// NPC 没有精灵（`Npc.wzl` 缺失）⇒ 退回标记，而不是画个错的东西。
#[test]
fn npc_退回标记() {
    assert!(body_sprite(&ent(2, Default::default()), None, Instant::now()).is_none());
}

/// 没有外观信息（旧服务端 / 快照还没到）⇒ 退回标记。
#[test]
fn 缺外观信息退回标记() {
    let mut e = ent(0, Default::default());
    e.feature = None;
    assert!(body_sprite(&e, None, Instant::now()).is_none());
}

/// 补间：刚移动时画在旧格与新格之间，过了时长就到位（且不再播走路）。
#[test]
fn 移动补间() {
    let now = Instant::now();
    let a = ActorAnim {
        cell: (5, 5),
        from: Some((4, 5)),
        action: None,
        changed_at: now,
        action_at: now,
        move_ms: move_ms(1, 0, false), // 走一格 = 600 ms（见 `move_ms`）
        walk_since: now,
    };
    let (x, y) = a.draw_pos((5, 5), now);
    assert!(
        (x - 4.0).abs() < 0.01 && (y - 5.0).abs() < 0.01,
        "刚开始还该在来处"
    );
    // 半路在半格附近（600ms 的一半 ⇒ 第 4.5 格）
    let (hx, _) = a.draw_pos((5, 5), now + Duration::from_millis(300));
    assert!((hx - 4.5).abs() < 0.01, "300ms 该走到第 4.5 格，实得 {hx}");
    let later = now + Duration::from_millis(a.move_ms as u64 + 10);
    assert_eq!(
        a.draw_pos((5, 5), later),
        (5.0, 5.0),
        "过了补间时长就该到位"
    );
    assert!(!a.moving(later));
}

/// 补间时长**跟着服务端的移动节流走**（`entity.MoveLimiter`：走 600 / 跑 400 一步）。
///
/// ⚠️ 这条是用户 2026-10-08 报的"一瘸一拐"的根源：原先写死 320 ms，
/// 每格都提前到位再干等 280 ms。
#[test]
fn 补间时长跟服务端节流() {
    assert_eq!(move_ms(1, 0, false), WALK_STEP_MS, "走一格 = 600 ms");
    assert_eq!(move_ms(0, -1, false), WALK_STEP_MS, "四个方向一样");
    assert_eq!(
        move_ms(1, 1, false),
        WALK_STEP_MS,
        "斜着走也是一格（切比雪夫）"
    );
    assert_eq!(move_ms(2, 0, true), RUN_STEP_MS, "跑一步 2 格 = 400 ms");
    assert_eq!(move_ms(0, 2, true), RUN_STEP_MS);
    assert_eq!(move_ms(2, 2, true), RUN_STEP_MS, "斜着跑也是两格");
    // 每格：走 600 ms、跑 200 ms ⇒ 跑确实快三倍（原版 GetNextRunXY 一步 2 格）
    // 跑每格 300ms（原版：节流 600ms 走 2 格）
    assert_eq!(RUN_STEP_MS / RUN_STEPS as u32, 300);
    assert!(RUN_STEP_MS / RUN_STEPS as u32 * 2 < WALK_STEP_MS * 2);
    // 距离算不出来（原地转身）也不能是 0 ⇒ 会除零
    assert!(move_ms(0, 0, false) > 0);
}

/// 走路动画的相位**不随每格重置**（原版就是这么推的：`Actor.pas:3230-3263`）。
#[test]
fn 走路相位不随每格重置() {
    let t0 = Instant::now();
    let t1 = t0 + Duration::from_millis(600);
    // 上一格还没走到位（连贯地走）⇒ 接着推：相位起点不动
    assert_eq!(next_walk_since(true, t0, t1), t0, "连着走时相位要接着推");
    // 上一格已经走完（停过一下/刚开始走）⇒ 从头开始
    assert_eq!(next_walk_since(false, t0, t1), t1, "停下来再走要从头");
}

/// 走/跑动画按**移动相位**推帧：6 帧要能播满（而不是每格从第 0 帧重来）。
///
/// `ActWalk` 6 帧 × 90 ms、`ActRun` 6 帧 × 120 ms（`Actor.pas:77-78`）。
#[test]
fn 走跑动画按相位推帧() {
    // 走：相位 0 → 0 帧、90 → 1 帧、540 → 又回到 0（一轮 = 6×90）
    assert_eq!(
        human_sample(None, 0, true, false, 0, false),
        (A::HAct::Walk, 0)
    );
    assert_eq!(
        human_sample(None, 0, true, false, 90, false),
        (A::HAct::Walk, 1)
    );
    assert_eq!(
        human_sample(None, 0, true, false, 450, false),
        (A::HAct::Walk, 5)
    );
    assert_eq!(
        human_sample(None, 0, true, false, 540, false),
        (A::HAct::Walk, 0)
    );
    // 跑：同一套相位走在**另一段图**上（120 ms 一帧）
    assert_eq!(
        human_sample(None, 0, true, true, 0, false),
        (A::HAct::Run, 0)
    );
    assert_eq!(
        human_sample(None, 0, true, true, 480, false),
        (A::HAct::Run, 4)
    );
    // 站着：相位无关（stand 的帧按自己的 200 ms 走）
    assert_eq!(
        human_sample(None, 0, false, true, 450, false).0,
        A::HAct::Stand
    );
}

/// 动作播完回站立 —— 否则实体会永远停在那一刀的末帧。
#[test]
fn 动作播完回站立() {
    assert_eq!(
        human_sample(Some(1), 0, false, false, 0, false).0,
        A::HAct::Hit
    );
    // **尾巴**内（510+80=590ms 之前）：仍停在挥砍的最后一帧 —— 两刀之间不留缝
    // （节拍 560ms，动画只有 510ms；不留尾巴就会闪 3 帧站立，看着像"砍一下停一下"）
    assert_eq!(
        human_sample(Some(1), 520, false, false, 0, false),
        (A::HAct::Hit, A::HAct::Hit.act().last_frame()),
        "尾巴内该停在最后一帧"
    );

    // ActHit 是 6 帧 × 85ms = 510ms ⇒ 过了尾巴（600ms）应回到站立
    assert_eq!(
        human_sample(Some(1), 600, false, false, 0, false),
        (A::HAct::Stand, 0)
    );
}

/// 手上的动作播完后，**走路**优先于站立（在走就别站着）。
#[test]
fn 动作播完且在走就播走路() {
    assert_eq!(
        human_sample(Some(1), 600, true, false, 0, false).0,
        A::HAct::Walk
    );
    // 跑也一样优先于站立，只是换成 ActRun
    assert_eq!(
        human_sample(Some(1), 600, true, true, 0, false).0,
        A::HAct::Run
    );
}

/// **建号那条路的每一步都必须有翻译** —— 用户报的"点了建号没反应"根因就是
/// 状态机吐了 `CreateAccount`，而 `send()` 用 `_ => None` 把它静默吞了
///（服务端日志里只有 `握手完成`，`CreateAccount` 一条都没到）。
#[test]
fn 建号那条路的每条命令都有翻译() {
    use mir2_core::entrance::{Entrance, Stage};
    use mir2_protocol::envelope::Body;
    let mut e = Entrance::new_for_signup("algo".into(), "pw123".into());

    // ① 取盐
    let cmd = e.next_cmd().expect("第一步是取盐");
    assert!(matches!(cmd, Body::LoginSaltRequest(_)));
    assert_eq!(*e.stage(), Stage::AwaitSignupSalt);
    assert!(to_cmd(&cmd, 0).is_some(), "取盐没有翻译 ⇒ 会被静默吞掉");

    // ② 回盐 ⇒ 该吐 `CreateAccount`（**就是漏掉的那一条**）
    let cmd = e
        .on(&mir2_protocol::Envelope {
            body: Some(Body::LoginSalt(mir2_protocol::LoginSalt {
                salt: vec![9; 16],
                iterations: 1000,
                key_len: 32,
            })),
            ..Default::default()
        })
        .expect("回盐之后该吐 CreateAccount");
    assert!(matches!(cmd, Body::CreateAccount(_)));
    assert_eq!(*e.stage(), Stage::AwaitSignup);
    assert!(to_cmd(&cmd, 0).is_some(), "建号没有翻译 ⇒ 点了没反应");

    // ③ 回执 ⇒ 留一条提示给界面（弹窗 + 切回登录面板）
    let _ = e.on(&mir2_protocol::Envelope {
        body: Some(Body::CreateAccountResult(
            mir2_protocol::CreateAccountResult {
                result: Some(mir2_protocol::ActionResult {
                    ok: true,
                    code: 0,
                    message: "账号已建立，请登录".into(),
                }),
            },
        )),
        ..Default::default()
    });
    assert_eq!(
        e.take_signup_msg(),
        Some((true, "账号已建立，请登录".into()))
    );
}

/// 翻译表**没有缺项**：状态机能吐出来的每一条命令都得在里面
///（上面那条钉"建号这条路"，这条钉"表本身"）。
#[test]
fn 状态机命令的翻译表没有缺项() {
    use mir2_protocol::envelope::Body;
    let cases: Vec<Body> = vec![
        Body::Reconnect(mir2_protocol::Reconnect {
            session_token: 7i32.to_le_bytes().to_vec(),
            last_ack_seq: 0,
        }),
        Body::LoginSaltRequest(mir2_protocol::LoginSaltRequest {
            account: "algo".into(),
        }),
        Body::Login(mir2_protocol::Login {
            account: "algo".into(),
            password_hash: "ab".repeat(32),
            client_build: String::new(),
        }),
        Body::CreateAccount(mir2_protocol::CreateAccount {
            account: "algo".into(),
            verifier: "cd".repeat(32),
        }),
        Body::ListCharacters(mir2_protocol::ListCharacters {}),
        Body::SelectCharacter(mir2_protocol::SelectCharacter { character_id: 42 }),
        Body::CreateCharacter(mir2_protocol::CreateCharacter {
            name: "新角色".into(),
            class: 1,
            gender: 1,
            hair: 3,
        }),
        Body::DeleteCharacter(mir2_protocol::DeleteCharacter {
            character_id: 42,
            password_hash: "ef".repeat(32),
        }),
    ];
    for b in &cases {
        assert!(to_cmd(b, 7).is_some(), "没有翻译：{b:?}");
    }
    // 反向：不是命令的单向消息**不该**被翻译（否则会把服务端的话原样发回去）
    assert!(to_cmd(
        &Body::ServerError(mir2_protocol::ServerError {
            code: 1,
            message: "x".into(),
        }),
        7
    )
    .is_none());
}

/// 角色列表**没变**就不重建场景 —— 用户报的日志刷屏（门还在放的那三秒里每帧重建 +
/// 每帧 `println`）与"选中位置被每帧抹回第 0 个"根因都是这条判断。
#[test]
fn 列表没变就不重建选角场景() {
    let a = vec![select::CharEntry {
        id: 1,
        name: "甲".into(),
        level: 1,
        class: 1,
        sex: 0,
    }];
    let scene = select::Select::new(a.clone());
    assert!(!list_changed(Some(&scene), &a), "同一个列表不该重建");
    assert!(list_changed(None, &a), "还没有场景时必须建");
    let mut b = a.clone();
    b.push(select::CharEntry {
        id: 2,
        name: "乙".into(),
        level: 2,
        class: 2,
        sex: 1,
    });
    assert!(list_changed(Some(&scene), &b), "列表变了要重建");
}

/// 换屏判定 —— **这两条各漏过一次**（见 `plan` 的说明），所以逐条钉住。
///
/// 这条测试是**真会红**的那种（已实际验过：把 bug 改回去 ⇒ 红；改回来 ⇒ 绿）：
///   · `rebuild_select` 改回 `awaiting_pick && changed && mode != 4` ⇒ 第①行红（bug ①）；
///   · `enter_play` 写死 `false`（或删掉）⇒ 第②行红（bug ②）。
#[test]
fn 换屏判定() {
    // ① **已经在选角屏**（mode 4）+ 列表变了 ⇒ 必须重建（建/删角到手的新列表）
    let p = plan(4, true, false, true, false);
    assert!(p.rebuild_select, "在选角屏里也必须按新列表重建");
    assert!(p.enter_select, "画面还得停在选角上");
    // ② 选角通过（在选角屏 + 状态机说进世界）⇒ 切游戏主场景
    let p = plan(4, false, true, false, false);
    assert!(p.enter_play, "选角通过必须换到游戏主场景");
    assert!(!p.rebuild_select && !p.enter_select);
    // ③ 门还在放 ⇒ 不切走（切了会把开门动画掐掉），但列表可以先备好
    let p = plan(1, true, false, true, true);
    assert!(!p.enter_select, "门没放完不该切走");
    assert!(p.rebuild_select, "但列表可以先建好");
    // ④ 已经在游戏里 ⇒ 什么都不动（别把玩家踢回选角）
    let p = plan(2, false, true, false, false);
    assert!(!p.rebuild_select && !p.enter_select && !p.enter_play);
    // ⑤ 列表没变 ⇒ 不重建（否则每帧重建：选中位置被抹掉、日志还会刷屏）
    assert!(!plan(4, true, false, false, false).rebuild_select);
    // ⑥ 门那屏（mode 1）收到 EnterWorld 也不该切游戏：门放完由 `mode == 1` 那条分支接手
    assert!(!plan(1, false, true, false, false).enter_play);
}

/// 鼠标走路的方向：8 个方位 + "同一格没有方向"（照原版 `GetNextDirection`，
/// `ClFunc.pas:398-422`）。协议值 = 原版 + 1（1 上、2 右上 … 8 左上，顺时针）。
#[test]
fn 鼠标方位给方向() {
    use mir2_protocol::Direction as D;
    let at = (10, 10);
    assert_eq!(dir_to(at, (10, 5)), Some(D::DirUp), "正上方");
    assert_eq!(dir_to(at, (15, 5)), Some(D::DirUpRight));
    assert_eq!(dir_to(at, (15, 10)), Some(D::DirRight));
    assert_eq!(dir_to(at, (15, 15)), Some(D::DirDownRight));
    assert_eq!(dir_to(at, (10, 15)), Some(D::DirDown));
    assert_eq!(dir_to(at, (5, 15)), Some(D::DirDownLeft));
    assert_eq!(dir_to(at, (5, 10)), Some(D::DirLeft));
    assert_eq!(dir_to(at, (5, 5)), Some(D::DirUpLeft));
    assert_eq!(
        dir_to(at, at),
        None,
        "同一格没有方向 ⇒ 调用方据此判\"到了\""
    );
    // 远距离也只看方位（不是只看相邻格）
    assert_eq!(dir_to(at, (99, 10)), Some(D::DirRight));
}

/// 鼠标连续走路：**距离 < 2 时不许跑**（照原版 `ClMain.pas:1950-1978`）。
///
/// 少了这条，跑步（一次跨 2 格）遇到**奇数距离**的目标就会跨过去再跨回来 ——
/// 用户 2026-10-08 报的"奔跑位置左右乱换"。距离 < 2 时改成走一步，正好落到目标格。
#[test]
fn 鼠标走路距离近了不许跑() {
    use mir2_protocol::Direction as D;
    let at = (10, 10);
    // 已经站在目标格 ⇒ 没有下一步（调用方收工）
    assert_eq!(next_move_step(at, at, true), None);
    assert_eq!(next_move_step(at, at, false), None);

    // 相邻（距离 1，含斜向）⇒ **走**，哪怕想要跑（跑会跨过去）
    assert_eq!(
        next_move_step(at, (11, 10), true),
        Some((D::DirRight, false))
    );
    assert_eq!(
        next_move_step(at, (11, 11), true),
        Some((D::DirDownRight, false))
    );
    assert_eq!(next_move_step(at, (10, 10), false), None);

    // 距离 ≥ 2（切比雪夫：`ClFunc.pas:352` 的 `MAX(abs(dx),abs(dy))`）⇒ 想跑就真跑
    assert_eq!(
        next_move_step(at, (12, 10), true),
        Some((D::DirRight, true))
    );
    assert_eq!(next_move_step(at, (10, 13), true), Some((D::DirDown, true)));
    // 斜向 (2,2)：切比雪夫距离是 2 ⇒ 也算"够远"（跑一次正好 2 格斜向）
    assert_eq!(
        next_move_step(at, (12, 12), true),
        Some((D::DirDownRight, true))
    );
    // 不想跑就永远走（左键）
    assert_eq!(
        next_move_step(at, (99, 10), false),
        Some((D::DirRight, false))
    );
}

/// 小地图的换算：**X 是 1.5 倍、Y 是 1 倍**（原版 `PlayScn.pas:808-813`）——
/// 这个不对称是原版就有的，写成 `*1.5/*1.5` 会让点位系统性偏左。
#[test]
fn 小地图换算x是一倍半y是一倍() {
    assert_eq!(minimap_point(0.0, 0.0), (0.0, 0.0));
    assert_eq!(minimap_point(2.0, 2.0), (3.0, 2.0)); // 2*48/32 = 3
    assert_eq!(minimap_point(289.0, 618.0), (433.5, 618.0)); // 边界村
    assert_eq!(minimap_point(650.0, 631.0), (975.0, 631.0)); // 银杏山谷
                                                             // **小数格**也要能用：走路补间落在两格之间（图上才能滑，而不是一格一格跳）
    assert_eq!(minimap_point(10.5, 20.25), (15.75, 20.25));
}

/// 小地图的裁剪框：以自己为中心 `120` 见方，**贴边时夹回图内**（原版也是这么夹的）。
#[test]
fn 小地图裁剪贴边要夹住() {
    let img = (1000u32, 800u32);
    assert_eq!(
        minimap_crop(500.0, 400.0, img, 120.0),
        (440.0, 340.0, 120.0, 120.0),
        "居中时是 120 见方"
    );
    assert_eq!(
        minimap_crop(10.0, 400.0, img, 120.0),
        (0.0, 340.0, 120.0, 120.0),
        "贴左边界"
    );
    assert_eq!(
        minimap_crop(500.0, 5.0, img, 120.0),
        (440.0, 0.0, 120.0, 120.0),
        "贴上边界"
    );
    assert_eq!(
        minimap_crop(999.0, 799.0, img, 120.0),
        (880.0, 680.0, 120.0, 120.0),
        "贴右下角"
    );
    // 图比窗口还小 ⇒ 给整张图（w/h 跟着缩），不会出现负数或越界
    assert_eq!(
        minimap_crop(5.0, 5.0, (50, 40), 120.0),
        (0.0, 0.0, 50.0, 40.0)
    );
    // 补间中的小数位置 ⇒ 裁剪框**连续**（"图滑得平滑"就是这儿来的）
    assert_eq!(
        minimap_crop(500.5, 400.0, img, 120.0),
        (440.5, 340.0, 120.0, 120.0)
    );
}

/// 大地图：**人物永远在窗口正中**，图比窗口大 ⇒ 跑动时图会滑（用户 2026-10-08 要的手感）。
#[test]
fn 大地图人物永远在正中() {
    let img = (540u32, 360u32);
    let win = (800u32, 600u32);
    let (px, py) = minimap_point(100.0, 200.0);
    let (dx, dy, dw, dh) = bigmap_dst(px, py, img, win, BIGMAP_ZOOM);
    // 人物在贴图上的位置 == 窗口中心（`bigmap_dst` 的全部意义）
    assert_eq!(
        (dx + px * BIGMAP_ZOOM, dy + py * BIGMAP_ZOOM),
        (400.0, 300.0)
    );
    // 放大 2 倍 ⇒ 图（1080×720）比窗口（800×600）大 ⇒ 有得滑
    assert_eq!((dw, dh), (1080.0, 720.0));
    assert!(
        dw > win.0 as f32 && dh > win.1 as f32,
        "图必须比窗口大，否则滑不起来"
    );
    // 走一格 ⇒ 图整体反向平移，而且是**连续**的（不是跳一格）
    let (dx2, _, _, _) = bigmap_dst(px + 1.5, py, img, win, BIGMAP_ZOOM);
    assert_eq!(
        dx2,
        dx - 3.0,
        "X 走一格（1.5 缩略图像素）⇒ 图反向滑 3 像素（×2）"
    );
    // ⚠️ 故意**不夹**：图外就是空的，但人物必须还在正中（用户的原话）
    let (dx3, _, _, _) = bigmap_dst(0.0, 0.0, img, win, BIGMAP_ZOOM);
    assert_eq!(dx3, 400.0, "到了地图左上角，贴图也照样把人物摆在正中");
}

/// 走路时地图取**补间位置**：没动画状态 ⇒ 服务端那一格；走起来 ⇒ 落在两格之间。
///
/// 这条钉的就是用户 2026-10-08 报的手感问题：图跟着**动作**走，不是"到位才跳一格"。
#[test]
fn 走动时地图取的是补间位置() {
    let now = Instant::now();
    // 还没有动画状态（刚进图）⇒ 原样的格子
    assert_eq!(self_render_pos(None, (10, 20), now), (10.0, 20.0));

    // 刚迈出一步：(10,20) → (11,20)
    let anim = ActorAnim {
        cell: (11, 20),
        from: Some((10, 20)),
        action: None,
        changed_at: now,
        action_at: now,
        move_ms: move_ms(1, 0, false),
        walk_since: now,
    };
    // 起点那一刻
    assert_eq!(self_render_pos(Some(&anim), (11, 20), now), (10.0, 20.0));
    // 半路（补间中）—— 这一条就是"平滑"的证据
    let mid = now + Duration::from_millis(anim.move_ms as u64 / 2);
    let (mx, _) = self_render_pos(Some(&anim), (11, 20), mid);
    assert!(
        (10.4..=10.6).contains(&mx),
        "半路该在第 10.5 格附近，实得 {mx}"
    );
    // 过了补间时长 ⇒ 到位
    let done = now + Duration::from_millis(anim.move_ms as u64 + 10);
    assert_eq!(self_render_pos(Some(&anim), (11, 20), done), (11.0, 20.0));
}

/// `MIR2_WINDOW` 的解析：比设计尺寸小 / 格式不对 ⇒ `None`（退回默认）。
///
/// ⚠️ 设计尺寸 2026-10-09 起是 **1024×768**（之前 800×600 是被放大到 1024 的那档）⇒
/// `800x600` 现在**不认**了：那会把 1024 的版式缩小（HUD 面板都放不下）。
#[test]
fn 窗口尺寸参数解析() {
    for (s, want) in [
        ("1024x768", Some((1024, 768))),
        ("1280x960", Some((1280, 960))),
        ("1600x1200", Some((1600, 1200))),
        // 大小写、空格都容错
        (" 1280 X 960 ", Some((1280, 960))),
        // 比设计尺寸（1024×768）小 ⇒ 不认
        ("800x600", None),
        ("1024x700", None),
        ("640x480", None),
        ("1024x500", None),
        // 格式不对
        ("1280*960", None),
        ("1280", None),
        ("abcxdef", None),
        ("", None),
    ] {
        assert_eq!(parse_window(s), want, "「{s}」解析不对");
    }
}

/// **登录/选角背景是 800×600**（真素材）—— 它们在 1024×768 的窗口里**居中**摆。
///
/// 2026-10-09 之前设计空间就是 800×600，这条断言写作"素材 == 设计尺寸"；
/// 现在设计空间改成 1024×768（原生渲染、不放大），这条只钉素材尺寸。
#[test]
fn 登录与选角背景正好是设计尺寸() {
    let Ok(dir) = std::env::var("MIR2_ASSET_DIR").or_else(|_| std::env::var("MIR2C_DATA")) else {
        eprintln!("跳过：未设置 MIR2_ASSET_DIR / MIR2C_DATA");
        return;
    };
    let dir = std::path::PathBuf::from(dir);
    let mut ui = ui::UiCache::new();
    for (what, lib, idx) in [
        (
            "登录背景",
            mir2_core::login_ui::Art::BG.0,
            mir2_core::login_ui::Art::BG.1,
        ),
        (
            "选角背景",
            mir2_core::select_ui::Art::BG.0,
            mir2_core::select_ui::Art::BG.1,
        ),
    ] {
        // ⚠️ 登录/选角那套素材是 **800×600**，而窗口/设计空间是 1024×768 ⇒
        // 它们是**居中摆**、不缩放（原版也是这么干的：`(SCREENWIDTH-800) div 2`）。
        // 这条钉住"素材尺寸没变" —— 变了各自的 `Layout::build` 会自动跟着居中。
        assert_eq!(
            ui.size(&dir, lib, idx),
            Some((800, 600)),
            "{what}（{lib}[{idx}]）该是 800×600（居中摆在 {WIN_W}×{WIN_H} 里）"
        );
    }
}

// 鼠标坐标换算**交给 SDL**（事件循环头的 `Event::get_converted_coords`，它同时管
// 逻辑呈现的缩放与留边）⇒ 这边没有可单测的纯函数了。
//
// ⚠️ 别再手写 `x / 某常数`：那个写法假设窗口是 4:3，窗口一改（现在可拉大拉小）
// 就会"点哪走哪偏一截"。

/// **走动/跑动时画面边缘不能露黑底**（用户 2026-10-08 报的"边缘一整片半格黑底，
/// 像是走动时叠瓦没盖全"）。
///
/// 病根：视口剔除拿的是**没减亚格偏移**的框，而图块实际画在 `-sub` 处 ⇒
/// 视口右/下边缘那一溜图块被判成"在视口外"剔掉 ⇒ 露黑底（相机是整格时 `sub = 0`，
/// 所以只在**走动中**出现 —— 必须拿小数相机测）。
///
/// 判据是"盖满"：真地图 + 真图库，扫视口里的像素，要求每一处都有**保留下来的
/// 地表图块**压着。用的还是剔除那条路（`draw_rect_cold`），所以它一退化就红。
#[test]
fn 走动时画面边缘不露黑底() {
    let (Some(dir), Some(pack)) = (
        mir2_core::paths::asset_dir(),
        mir2_core::paths::map_container(),
    ) else {
        eprintln!("跳过：没有资产目录 / 地图容器");
        return;
    };
    let Ok(a) = Archive::open(&pack) else {
        eprintln!("跳过：地图容器打不开");
        return;
    };
    // 优先用 "0"（新手村，地表是满的）；没有就用第一张
    let name = if a.lookup("0").is_some() {
        "0".to_string()
    } else {
        match a.entries().first() {
            Some(e) => e.name.clone(),
            None => return,
        }
    };
    let Ok(m) = Map::load(&a, &name) else {
        eprintln!("跳过：地图 {name} 打不开");
        return;
    };

    let cols = WIN_W as i32 / UNIT_X + 3;
    let rows = VIEW_H as i32 / UNIT_Y + 3;
    let view = viewport_rect();
    // 相机放在地图中央（保证视口整块都在图内 —— 图外的黑是应该的，不算漏画）
    let cx = m.width as i32 / 2 - WIN_W as i32 / UNIT_X / 2;
    let cy = m.height as i32 / 2 - VIEW_H as i32 / UNIT_Y / 2;
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    for frac in [0.0f32, 0.25, 0.5, 0.75] {
        let cam = (cx as f32 + frac, cy as f32 + frac);
        let cp = cam_parts(cam);
        m.visible_tiles(cp.cell.0, cp.cell.1, cols, rows, 0, &mut draws);
        // "保留下来的地表图块" —— 走的就是绘制时那条剔除（同一个 `sub`）
        // ⚠️ 用的是**绘制时那条判据本身**（`tile_in_view`），不是自己重算一遍 ——
        // 自己重算就等于"只测了 `intersects`"，调用点漏减一次 `sub` 照样绿。
        let mut kept: Vec<FRect> = Vec::new();
        for d in draws.iter().filter(|d| d.layer == Layer::Ground) {
            if !tile_in_view(&mut libs, &dir, d, &cp, LAYERS_ALL, &view) {
                continue;
            }
            if let Some(r) = draw_rect_cold(&mut libs, &dir, d, cp.sub) {
                kept.push(r);
            }
        }
        assert!(!kept.is_empty(), "相机 {cam:?} 一块地表都没留下？");
        // 4px 网格扫视口：够密（黑带至少几十像素宽），又不至于慢
        let mut px = 0.5;
        while px < WIN_W as f32 {
            let mut py = BAR_TOP + 0.5;
            while py < BAR_TOP + VIEW_H {
                assert!(
                    kept.iter()
                        .any(|r| r.x <= px && px < r.x + r.w && r.y <= py && py < r.y + r.h),
                    "相机 {cam:?}（亚格偏移 {:.0}px）：视口像素 ({px},{py}) 没有地表图块盖着 \
                         —— 走动时这里就是一条黑底",
                    cp.sub.0
                );
                py += 4.0;
            }
            px += 4.0;
        }
    }
}

/// 屏幕坐标 ↔ 格子互为逆（"点哪走到哪"靠这一对；鼠标那条路用的是反算）。
#[test]
fn 屏幕与格子互为逆() {
    let cam = (100.0, 200.0);
    for (cx, cy) in [(100, 200), (103, 205), (99, 199), (140, 260)] {
        let (px, py) = cell_to_screen(cam, cx, cy);
        // 取格内一点（+1px）再反算，避开格边界
        assert_eq!(
            screen_to_cell(cam, px + 1.0, py + 1.0),
            (cx, cy),
            "({cx},{cy}) 往返失败"
        );
    }
    // 相机带小数时也一样（走路时相机就是小数格）
    let camf = (100.25, 200.75);
    let (px, py) = cell_to_screen(camf, 103, 205);
    assert_eq!(screen_to_cell(camf, px + 1.0, py + 1.0), (103, 205));
}

/// **走路/跑动时人物钉在屏幕中间不动、地图往前卷**（原版 `PlayScn.pas:1084-1089`：
/// `m_ClientRect.Left := g_MySelf.m_nRx - 9`，`m_nRx` 是**渲染**坐标）。
///
/// 这条是用户 2026-10-08 报的那个手感的判据：相机若取"服务端那一格"，
/// 人就变成"在视口里一格格蹭、蹭满一格镜头再跳一下"。
#[test]
fn 走路时人物钉在屏幕中间地图往前卷() {
    let now = Instant::now();
    let step = |anim: &ActorAnim, ms: u64| {
        let render = anim.draw_pos((11, 10), now + Duration::from_millis(ms));
        (render, follow_cam(render))
    };
    // 一跳：格 (10,10) → (11,10)，整跳 600ms
    let a = ActorAnim {
        cell: (11, 10),
        from: Some((10, 10)),
        action: None,
        changed_at: now,
        action_at: now,
        move_ms: 600,
        walk_since: now,
    };
    let mut cams = Vec::new();
    for ms in [0u64, 150, 300, 450, 599] {
        let (render, cam) = step(&a, ms);
        let (px, py) = cell_to_screen_f(cam, render.0, render.1);
        assert!(
            (px - WIN_W as f32 / 2.0).abs() < 0.01 && (py - VIEW_H / 2.0 - BAR_TOP).abs() < 0.01,
            "{ms}ms 时人物不在视口正中：({px},{py})"
        );
        cams.push(cam.0);
    }
    // 相机确实在往前走（而且**每帧走一点**，不是攒够一整格才跳）
    assert!(
        cams[0] < cams[1] && cams[1] < cams[2],
        "相机该平滑前进：{cams:?}"
    );
    let per_step = cams[1] - cams[0];
    assert!(
        per_step > 0.0 && per_step < 0.5,
        "150ms 只该走 0.25 格（一格 600ms），实得 {per_step}"
    );
}

/// 相机拆成"整格 + 亚格像素"：地图那条路只认整格，亚格靠绘制时减回来。
#[test]
fn 相机拆成整格加亚格() {
    let cp = cam_parts((10.0, 20.0));
    assert_eq!(cp.cell, (10, 20));
    assert_eq!(cp.sub, (0.0, 0.0));
    // 0.25 格 ⇒ 12px（UNIT_X = 48）；0.5 格 ⇒ 16px（UNIT_Y = 32）
    let cp = cam_parts((10.25, 20.5));
    assert_eq!(cp.cell, (10, 20));
    assert_eq!(cp.sub, (12.0, 16.0));
    // 负数相机（地图边缘会露出来一格）也要往下取整 —— 否则整格与亚格对不上
    let cp = cam_parts((-0.25, -0.5));
    assert_eq!(cp.cell, (-1, -1));
    assert_eq!(cp.sub, (36.0, 16.0));
}

/// **出手不能比服务端允许的更快**（用户 2026-10-09 报的"砍几下就停"）。
///
/// 服务端按 `520ms − 攻速×25ms` 限流（`netgate.go:63/112`），比它快的攻击**被丢掉**、
/// 而且**不发 `EntityAction`** ⇒ 我们自己的挥砍动画断一拍（动画完全来自服务端回包）。
/// 旧值取的是动作表时长 510ms ⇒ 每一刀丢一刀。
#[test]
fn 出手不比服务端快() {
    // 出手节拍按**等级**算（原版 `CanNextHit`），任何等级都要慢于服务端基数，
    // 否则那一刀会被服务端丢掉、挥砍动画断拍。
    for level in [1u32, 7, 27, 50] {
        let gap = attack_gap(level).as_millis() as u64;
        assert!(
            gap >= HIT_BASE_MS,
            "{level} 级的出手间隔 {gap}ms 比服务端基数 {HIT_BASE_MS}ms 还快 ⇒ 会被丢掉"
        );
    }
    // 1 级 ≈ 1006ms、27 级及以上 = 650ms（`1020 − min(370, level*14)`）
    assert_eq!(attack_gap(1).as_millis() as u64, 1020 - 14);
    assert_eq!(attack_gap(27).as_millis() as u64, 650);
    assert_eq!(attack_gap(99).as_millis() as u64, 650);
    // 也要够长到让挥砍动画播完（不然下一刀会把动画从头拽）
    let hit_ms = u64::from(mir2_core::actor::HAct::Hit.act().duration_ms());
    for level in [1u32, 7, 27, 50] {
        let gap = attack_gap(level).as_millis() as u64;
        assert!(
            gap >= hit_ms,
            "{level} 级的出手间隔 {gap}ms 比挥砍动画 {hit_ms}ms 还短"
        );
    }
}

/// **死了要一直画尸骨**（用户 2026-10-09 报的"死后没有显示尸体状态"）。
///
/// 病根：`Die` 是"一次播完"的动作，播完（`held_ms >= duration`）原来的代码会退回
/// **站立** ⇒ 画面上"死而复生"。现在 `dead = true` 一律钉在 `Die` 的最后一帧。
#[test]
fn 死了停在尸骨那帧() {
    use mir2_core::actor as A;
    // 任选一个有死亡段的品种（10 = 鸡/鹿那一族）
    let race = 10u8;
    let die = A::mon_actions(race)[A::MAct::Die as usize];
    assert!(die.frame > 0, "这条用例要求该品种有死亡段");
    let (act, frame) = monster_sample(
        race,
        Some(mir2_core::world::action::DEATH),
        10_000_000,
        false,
        true,
    );
    assert_eq!(act, A::MAct::Die, "死了该是 Die");
    assert_eq!(frame, die.last_frame(), "死了要停在**最后一帧**（尸骨）");
    // 不管过了多久都不许动（原来就是这里退回站立的）
    for ms in [0u32, die.duration_ms(), 10_000_000] {
        let (act, frame) =
            monster_sample(race, Some(mir2_core::world::action::DEATH), ms, false, true);
        assert_eq!(
            (act, frame),
            (A::MAct::Die, die.last_frame()),
            "{ms}ms 时不是尸骨"
        );
    }
    // 玩家那条路同理
    let (act, frame) = human_sample(None, 0, false, false, 0, true);
    assert_eq!(act, A::HAct::Die);
    assert_eq!(frame, A::HAct::Die.act().last_frame());

    // 还在播的过程中（`dead = false`：刚收到动作、`Death` 包还没到）照旧按死亡动作播；
    // ⚠️ 别把"播完回站立"这条也一起钉死 —— 那是攻击/受击该有的行为（只有 `dead` 才钉住）
    let (act, frame) = monster_sample(race, Some(mir2_core::world::action::DEATH), 0, false, false);
    assert_eq!(act, A::MAct::Die, "收到死亡动作就按它播");
    assert_eq!(frame, 0, "刚开始播是第一帧");
}

/// **移动不能让挥砍动作重播**（用户 2026-10-09："杀了怪，一跑起来又在砍"）。
///
/// 病根是一个时钟干两件事：动作进度与移动补间共用 `changed_at` ⇒ 每走一格就把
/// 动作进度清零 ⇒ 那个早该过期的 `Some(1)`（`world.self_action` 一直留着）又播一遍。
#[test]
fn 移动不重播挥砍() {
    let now = Instant::now();
    // 只看"手上的动作"取到什么姿势：`Some(1)` = 挥砍
    let hit = |anim: &ActorAnim, at: Instant| {
        let (held, ms) = (anim.action, anim.action_ms(at));
        human_sample(held, ms, anim.moving(at), false, anim.walk_ms(at), false).0
    };
    // t0 收到挥砍（动作钟 t0），t0+700ms 走了一步（**移动钟**被重置）
    let mut a = ActorAnim {
        cell: (3, 4),
        from: None,
        action: Some(1),
        changed_at: now,
        action_at: now,
        move_ms: 600,
        walk_since: now,
    };
    assert_eq!(hit(&a, now), A::HAct::Hit, "刚砍：是挥砍");
    a.from = Some((2, 4));
    a.changed_at = now + Duration::from_millis(700); // 移动**只**动移动钟
    a.move_ms = 600;
    assert_eq!(
        hit(&a, now + Duration::from_millis(750)),
        A::HAct::Walk,
        "挥砍早过期了 ⇒ 走动时该是走路，不能又砍一刀"
    );
    // 而**新来**一个动作（服务端再发一次挥砍）当然要正常播
    a.action_at = now + Duration::from_millis(750);
    assert_eq!(hit(&a, now + Duration::from_millis(760)), A::HAct::Hit);
}

/// 球的"液面"裁切：**看得见的永远是下面那一截**（原版 `FState.pas:3784-3795`）。
///
/// 判据是三条边界：满血 = 整张图不动；空 = 什么都不画；一半 = 下面一半。
#[test]
fn 球的液面裁切() {
    assert_eq!(gauge_band(1.0, 90), (0, 90), "满：整张图");
    assert_eq!(gauge_band(0.0, 90), (90, 0), "空：什么都不画");
    assert_eq!(gauge_band(0.5, 90), (45, 45), "一半：下面一半");
    assert_eq!(gauge_band(0.25, 90), (68, 22), "四分之一：下面四分之一");
    // 越界要夹住（服务端给的 HP 可能一时大于 MaxHP）
    assert_eq!(gauge_band(1.5, 90), (0, 90));
    assert_eq!(gauge_band(-1.0, 90), (90, 0));
    // 可视高度 + 液面 = 球高（画的两个矩形必须正好拼上）
    for pct in [0.0, 0.1, 0.33, 0.5, 0.97, 1.0] {
        let (top, h) = gauge_band(pct, 90);
        assert_eq!(top + h, 90, "pct={pct} 时拼不上");
    }
}

/// 出手的两重门：**手上这一步没走完不能打**（原版 `IsIdle`），
/// 以及出手冷却（原版 `CanNextHit`）。
#[test]
fn 走完这一步才出手() {
    let gap = attack_gap(1);
    assert!(can_attack(false, gap, 1), "站着 + 冷却到点 ⇒ 能打");
    assert!(
        !can_attack(true, gap * 2, 1),
        "还在走这一步 ⇒ 不许出手（否则挥砍会在半路上播）"
    );
    assert!(!can_attack(false, gap / 2, 1), "冷却没到 ⇒ 不许出手");
    // 节拍按等级缩短（原版 `1020 − min(370, level*14)`）：20 级明显快过 1 级
    assert!(attack_gap(20) < attack_gap(1));
}

/// 自己这一步的补间**盖满**自己的发送步频 —— 否则每格末尾会空出几十毫秒
/// （走路动画闪回站立帧、镜头停一下），用户报的"走路没做好"里就有它。
///
/// 别人用服务端的节流（`move_ms`，比自己的短）—— 它们的下一条由服务端驱动。
#[test]
fn 自己的补间盖满发送步频() {
    assert_eq!(
        self_move_ms(1, 0, false),
        WALK_MS as u32,
        "走一格 = 一个步频"
    );
    assert_eq!(self_move_ms(0, -1, false), WALK_MS as u32);
    assert_eq!(
        self_move_ms(2, 0, true),
        RUN_MS as u32,
        "跑一步 2 格 = 一个步频"
    );
    assert_eq!(self_move_ms(2, 2, true), RUN_MS as u32, "斜着跑也是 2 格");
    assert_eq!(
        self_move_ms(1, 0, true),
        (RUN_MS / 2) as u32,
        "被挡成 1 格的跑：按格数摊"
    );
    assert!(
        move_ms(1, 0, false) < self_move_ms(1, 0, false),
        "别人的补间该比自己短（600 < 650）"
    );
}

/// 鼠标点一格算什么：**活怪 ⇒ 锁它**；空地/死怪/玩家/NPC ⇒ 走/跑到那格。
///
/// 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）与 `AttackTarget`（`:2691`）。
#[test]
fn 点鼠标算什么() {
    use mir2_core::world::{World, KIND_MONSTER};
    let mut w = World::default();
    w.self_id = 1;
    w.self_pos = (10, 10);
    let mut put = |id: u64, kind: u32, x: i32, y: i32, dead: bool| {
        w.entities.insert(
            id,
            Entity {
                id,
                kind,
                name: "甲".into(),
                x,
                y,
                dir: 1,
                feature: None,
                hp: 10,
                max_hp: 10,
                run: false,
                status_bits: 0,
                dead,
                action: None,
            },
        );
    };
    put(100, KIND_MONSTER, 12, 10, false);
    put(200, 0, 13, 10, false); // 玩家（要 Shift，那条线还没做）
    put(300, KIND_MONSTER, 14, 10, true); // 死怪（尸骨）

    // 点活怪 ⇒ 锁它，不去走
    assert_eq!(mouse_intent(&w, (12, 10), false), (Some(100), None));
    // 点空地 ⇒ 走那一格（右键 = 跑）
    assert_eq!(
        mouse_intent(&w, (11, 10), false),
        (None, Some((11, 10, false)))
    );
    assert_eq!(
        mouse_intent(&w, (11, 10), true),
        (None, Some((11, 10, true)))
    );
    // 玩家 / 死怪 / 自己那格 ⇒ 走
    assert_eq!(mouse_intent(&w, (13, 10), false).0, None);
    assert_eq!(mouse_intent(&w, (14, 10), false).0, None);
    assert_eq!(
        mouse_intent(&w, (10, 10), false),
        (None, Some((10, 10, false)))
    );
}

/// **按住左键点怪**：光标滑开了也**不许把锁住的怪弄丢**（用户 2026-10-08 报的
/// "点怪之后站不住、掉头去走路"）。
///
/// 病根是镜头跟着人走（`follow_cam`）—— 跑向怪的路上，光标（屏幕位置不动）
/// 对应的格子会从怪身上滑开，而按住时每 300ms 重取目标、照原版会先 `g_TargetCret := nil`
/// ⇒ 目标半路被丢。
#[test]
fn 按住不丢已锁的怪() {
    use mir2_core::world::{World, KIND_MONSTER};
    let mut w = World::default();
    w.self_id = 1;
    w.self_pos = (10, 10);
    let put = |w: &mut World, id: u64, kind: u32, x: i32, y: i32, dead: bool| {
        w.entities.insert(
            id,
            Entity {
                id,
                kind,
                name: "甲".into(),
                x,
                y,
                dir: 1,
                feature: None,
                hp: 10,
                max_hp: 10,
                run: false,
                status_bits: 0,
                dead,
                action: None,
            },
        );
    };
    put(&mut w, 100, KIND_MONSTER, 12, 10, false);

    // 新鲜按下：光标在怪身上 ⇒ 锁它
    assert_eq!(mouse_intent(&w, (12, 10), false), (Some(100), None));
    // 按住重取：光标已经滑到**空地** ⇒ **仍然锁着它**（不是掉头去走）
    assert_eq!(
        mouse_repeat(&w, (15, 10), false, Some(100)),
        (Some(100), None),
        "按住时不该因为光标离开怪而丢目标"
    );
    // 光标滑到**另一只**怪身上 ⇒ 换目标
    put(&mut w, 200, KIND_MONSTER, 11, 10, false);
    assert_eq!(
        mouse_repeat(&w, (11, 10), false, Some(100)),
        (Some(200), None)
    );
    // 锁的那个死了 ⇒ 回到"走去光标那格"（光标在空地）
    w.entities.get_mut(&100).unwrap().dead = true;
    assert_eq!(
        mouse_repeat(&w, (15, 10), false, Some(100)),
        (None, Some((15, 10, false)))
    );
    // 没锁东西时与新鲜按下同一条路
    assert_eq!(
        mouse_repeat(&w, (15, 10), false, None),
        (None, Some((15, 10, false)))
    );
}

/// 登录/选角那两屏：设计空间 800×600 铺到 1024×768 画布上应当是**整数关系**的
/// 正好铺满（800×1.28 = 1024、600×1.28 = 768）—— 这是"拉伸"那条政策的地基。
#[test]
fn 设计空间拉伸正好铺满画布() {
    assert_eq!(ui::UI_SCALE, 1024.0 / 800.0);
    assert_eq!(800.0 * ui::UI_SCALE, WIN_W as f32);
    assert_eq!(600.0 * ui::UI_SCALE, WIN_H as f32);
    // 换算互为逆（鼠标那条路：画布 → 设计）
    for p in [(0.0, 0.0), (252.0, 173.0), (800.0, 600.0)] {
        let back = ui::ui_inv_pt(ui::ui_pt(p));
        assert!((back.0 - p.0).abs() < 0.01 && (back.1 - p.1).abs() < 0.01);
    }
    // 版式里那两个字面量：登录框在设计空间居中 ⇒ 画布上也居中
    let (cx, cy) = ui::ui_pt((252.0, 173.0));
    assert!(((WIN_W as f32 - 296.0 * ui::UI_SCALE) / 2.0 - cx).abs() < 0.01);
    assert!(((WIN_H as f32 - 254.0 * ui::UI_SCALE) / 2.0 - cy).abs() < 0.01);
}

/// 窗口尺寸夹进屏幕可用区域（1024×768 的屏 + 标题栏 ⇒ 客户区只有 1024×743 那种）。
#[test]
fn 窗口夹进可用区域() {
    assert_eq!(fit_window((1024, 768), (1920, 1080)), (1024, 768));
    assert_eq!(fit_window((1024, 768), (1024, 743)), (1024, 743));
    assert_eq!(fit_window((1280, 960), (1280, 800)), (1280, 800));
}

/// 呈现模式：**装得下就等比缩放，装不下就 1:1 裁切**（绝不缩小 —— 用户第 4 条）。
#[test]
fn 呈现模式不缩小() {
    let lb = sdl3_sys::render::SDL_LOGICAL_PRESENTATION_LETTERBOX;
    let dis = sdl3_sys::render::SDL_LOGICAL_PRESENTATION_DISABLED;
    assert_eq!(present_mode((1024, 768)), lb, "正好装得下 ⇒ 等比");
    assert_eq!(present_mode((1920, 1200)), lb, "更大 ⇒ 等比放大");
    assert_eq!(present_mode((1024, 743)), dis, "矮一点 ⇒ 1:1 裁切（不缩）");
    assert_eq!(present_mode((900, 768)), dis, "窄一点 ⇒ 1:1 裁切（不缩）");
}

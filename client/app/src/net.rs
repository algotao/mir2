//! 网络胶水：连接与握手状态机、收发泵、会话命令翻译、连接失败提示。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::HashMap;
use std::time::{Duration, Instant};

use crate::actor::{move_ms, next_walk_since, self_move_ms, ActorAnim, SeenEntity, WALK_STEP_MS};
use crate::colors::{C_CHAT_BAD, C_CHAT_SYS};
use crate::geom::self_render_pos;
use crate::hud::Chat;

/// app 侧的联网状态。**薄薄一层**：
///
/// - 连接与消息泵在 `mir2-net`（独立线程 → channel）
/// - 握手状态机与世界状态在 `mir2-core`（纯函数，`client/e2e` 用的是**同一份**，见 D-18）
///
/// 这里只负责"把它们按帧推一下、把状态交给渲染"，**不放任何游戏规则**。
pub(crate) struct Net {
    pub(crate) sess: mir2_net::Session,
    pub(crate) entrance: mir2_core::entrance::Entrance,
    pub(crate) world: mir2_core::world::World,
    /// 会话号（v0 的 `session_token` 就是它；`Entrance` 内部也持一份，这里留一份
    /// 是为了把 `Reconnect` 翻成 `Cmd::Reconnect` 时不必从 token 字节里解回来）。
    pub(crate) session: i32,
    /// 给人看的连接状态（连不上/已连接/进图/出错）。
    pub(crate) status: String,
    /// 累计世界变更次数（"世界在动"最直接的观测量）。
    pub(crate) changes: u32,
    /// 伤害飘字：文本 + 格子坐标 + 出生时刻。
    ///
    /// ⚠️ 世界模型（`core::world`）是**没有时钟**的纯状态，只负责把 `Damage` 记进
    /// 一个队列；计时与淡出是渲染层的事（这里才有帧时钟）。
    pub(crate) floaters: Vec<(String, i32, i32, Instant)>,
    /// 连接层给出的结束原因（连不上 / 被断开）。**登录界面靠它弹窗** ——
    /// 少了它，连不上时界面会一直卡在 `CONNECTING ...`（踩过）。
    pub(crate) fail: Option<String>,
    /// 是否已经处理过"刚进世界"那一帧。
    ///
    /// ⚠️ 必须有这个标志：`entrance.in_world()` **每帧都为真**，而下面那个
    /// "状态行变了没"的判据在 `map_name` 为空时（重连直接回世界那条路不带
    /// `ChangeMap`）**也**恒真 ⇒ 直接 `println!` 会变成每帧一行（实测刷了几百行）。
    pub(crate) entered_once: bool,
    /// 建 `Net` 的时刻：只为算"连接 → 进世界"用了多久。
    ///
    /// ⚠️ 这个数字是有用的：曾经有个 bug 让这一段整整多花 20 秒（`flush_entrance`
    /// 的说明），当时是**靠翻服务端日志的时间戳**才发现的。现在它直接打在终端上。
    pub(crate) started: Instant,
    /// 每个实体的**动画状态**（移动的补间进度、动作播放到哪了）。
    ///
    /// ⚠️ 同样只在渲染层：世界模型只存事实（在哪、什么动作），"什么时候发生的"归这里。
    pub(crate) anims: HashMap<u64, ActorAnim>,
    /// 世界侧排出来的音效编号（挨打 / 死亡），由主循环取走播放。
    ///
    /// ⚠️ 为什么要绕这一道：`pump()` 在 `Net` 里，**拿不到音频设备**（那在 `main`
    /// 的作用域）—— 与 `take_damage`（伤害飘字）同一套路：世界只记账，
    /// 谁有时钟/设备谁去表现。
    pub(crate) pending_sfx: Vec<u16>,
    /// HUD 聊天区的行（原版 `ChatStrs`）—— 见 [`Chat`]。
    pub(crate) chat: Chat,
    /// 已经为自己死放过一次声（`self_dead` 会一直为真，不能每帧放）。
    pub(crate) died_once: bool,
    /// 刚死 ⇒ 主循环切 game over 音乐（原版 `Actor.pas:2373-2374`）。
    pub(crate) gameover: bool,
}

impl Net {
    /// 读环境变量连一个服务端。返回 `Net`（**连接是异步的**：结果从事件里回来）。
    ///
    /// ```
    /// MIR2_SERVER=127.0.0.1:7500 MIR2_SESSION=7 cargo run -p mir2-app
    /// ```
    ///
    /// ⚠️ 为什么还要 `MIR2_SESSION`：新协议的 `Login` 还没实现（口令怎么过网络未定，
    /// 见 D-24），所以客户端只能认领一个**既有会话** —— 它由账户服务（或 e2e 测试）建立。
    /// 用**口令**登录（D-24① 挑战应答；口令不上网络，只上证明）。
    ///
    /// ⚠️ 与 `connect()`（认领既有会话）的区别只有"入口不同"：两条路之后
    /// 走的是**同一条尾巴**（列角色 → 选角 → 进世界），见 `core::entrance` 的文件头。
    pub(crate) fn connect_with_password(
        addr: &str,
        account: &str,
        password: &str,
    ) -> Result<Net, String> {
        if account.is_empty() {
            return Err("账号不能为空".into());
        }
        if password.is_empty() {
            return Err("口令不能为空".into());
        }
        let char_id: Option<u64> = match std::env::var("MIR2_CHAR") {
            Ok(s) => Some(s.parse().map_err(|_| "MIR2_CHAR 必须是整数".to_string())?),
            Err(_) => None,
        };
        println!("[net] 连接 {addr}（账号 {account}，口令登录）…");
        let sess = mir2_net::Session::spawn(addr.to_string(), "mir2-app".into(), "zh-CN".into());
        let mut entrance = mir2_core::entrance::Entrance::new_with_password(
            account.to_string(),
            password.to_string(),
            char_id,
        );
        // ⚠️ 把"选哪个角色"交给 app 的选角界面：不设这个，状态机会**自己把列表第一个
        // 选掉**（那是 e2e / 无头驱动的默认行为，见 `Entrance::set_manual_pick`）。
        entrance.set_manual_pick(true);
        Ok(Net {
            sess,
            entrance,
            world: mir2_core::world::World::default(),
            session: 0,
            status: format!("登录 {account} …"),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    /// **建号**（D-32）：与口令登录同一条连接流程，只是状态机的第一步变成
    /// "取盐 → 发口令校验值"。建完不自动登录，所以拿到回执后这条连接就没用了。
    pub(crate) fn connect_for_signup(
        addr: &str,
        account: &str,
        password: &str,
    ) -> Result<Net, String> {
        if account.is_empty() {
            return Err("账号不能为空".into());
        }
        if password.is_empty() {
            return Err("口令不能为空".into());
        }
        println!("[net] 连接 {addr}（建号 {account}）…");
        let sess = mir2_net::Session::spawn(addr.to_string(), "mir2-app".into(), "zh-CN".into());
        let mut entrance = mir2_core::entrance::Entrance::new_for_signup(
            account.to_string(),
            password.to_string(),
        );
        // 建号不涉及选角，但保持与另两条入口一致（免得将来复用这条连接时行为不同）。
        entrance.set_manual_pick(true);
        Ok(Net {
            sess,
            entrance,
            world: mir2_core::world::World::default(),
            session: 0,
            status: format!("建号 {account} …"),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    pub(crate) fn connect() -> Result<Net, String> {
        let addr = std::env::var("MIR2_SERVER").unwrap_or_else(|_| "127.0.0.1:7500".into());
        let session: i32 = std::env::var("MIR2_SESSION")
            .map_err(|_| {
                "缺 MIR2_SESSION（这条是\"认领既有会话\"的入口；用登录界面输入账号口令则不需要它）"
                    .to_string()
            })?
            .parse()
            .map_err(|_| "MIR2_SESSION 必须是十进制整数".to_string())?;
        let char_id: Option<u64> = match std::env::var("MIR2_CHAR") {
            Ok(s) => Some(s.parse().map_err(|_| "MIR2_CHAR 必须是整数".to_string())?),
            Err(_) => None,
        };

        println!("[net] 连接 {addr}（会话 {session}）…");
        let sess = mir2_net::Session::spawn(addr, "mir2-app".into(), "zh-CN".into());
        Ok(Net {
            sess,
            entrance: {
                let mut e = mir2_core::entrance::Entrance::new(session, char_id);
                // 与口令那条路一致：由选角界面来选（见 `connect_with_password` 的说明）
                e.set_manual_pick(true);
                e
            },
            world: mir2_core::world::World::default(),
            session,
            status: "连接中…".into(),
            changes: 0,
            floaters: Vec::new(),
            fail: None,
            entered_once: false,
            started: Instant::now(),
            chat: Chat::default(),
            anims: HashMap::new(),
            pending_sfx: Vec::new(),
            died_once: false,
            gameover: false,
        })
    }

    /// 把"网络线程收到的东西"推进两个状态机（握手 + 世界）。**每帧调一次**。
    ///
    /// 这是 plan §4.1 的"收包线程 → channel → 主循环按帧消费"：
    /// 主循环永远不会被网络阻塞。
    pub(crate) fn pump(&mut self) {
        while let Ok(ev) = self.sess.evs.try_recv() {
            match ev {
                mir2_net::Ev::Connected {
                    version,
                    capabilities,
                    nonce,
                } => {
                    self.chat
                        .push(format!("已连接（协议 {version}）"), C_CHAT_SYS);
                    // ⚠️ nonce 是**这条连接一次**的握手随机值，登录时要把口令证明绑在它上面
                    //（D-24①）⇒ 必须立刻交给握手状态机，晚了就发不出证明。
                    self.entrance.on_nonce(&nonce);
                    self.status =
                        format!("已连接（协议 {version}，能力 {}）", capabilities.join(","));
                    println!("[net] {}", self.status);
                }
                mir2_net::Ev::Closed(why) => {
                    self.chat.push(format!("断开：{why}"), C_CHAT_BAD);
                    self.status = format!("断开：{why}");
                    self.fail = Some(why);
                    println!("[net] {}", self.status);
                }
                mir2_net::Ev::Envelope(env) => {
                    // 两条线各吃同一条信封：握手状态机管那几步，世界状态机管实体。
                    // ⚠️ 待发命令**不在这里**拉 —— 见 `pump` 末尾的 `flush_entrance`
                    //（拉在信封里会漏掉"非信封推动的转折"，那是一个实测过的真 bug）。
                    if let Some(b) = self.entrance.on(&env) {
                        self.send(&b);
                    }
                    if self.world.apply(&env) == mir2_core::world::Change::World {
                        self.changes += 1;
                    }
                    if self.entrance.in_world() && !self.entered_once {
                        self.entered_once = true;
                        self.chat.push(
                            format!(
                                "进入 {} ({},{})",
                                self.world.map_name, self.world.self_pos.0, self.world.self_pos.1
                            ),
                            C_CHAT_SYS,
                        );
                        println!("[net] 进世界：连接到现在 {:.2?}", self.started.elapsed());
                        // 进图后把状态行换成"世界摘要"（比"已连接"有用得多）。
                        self.status = format!(
                            "{} @{} ({},{})",
                            self.world.map_name,
                            self.world.self_id,
                            self.world.self_pos.0,
                            self.world.self_pos.1
                        );
                    }
                }
            }
        }
        // 状态机的待发命令：**每帧**排空，与有没有入站包无关。
        //
        // ⚠️ 这里曾经是错的：`next_cmd()` 被塞在上面那个 `Ev::Envelope` arm 里拉。
        // 于是"收到握手 nonce（`Ev::Connected`）⇒ 要发 `LoginSaltRequest`"这一步
        // 得**等下一个入站包**才出去 —— 而写线程的心跳是 `PING_EVERY = 20s`，
        // 服务端回 Pong 才构成那个包：表现是**输完账号要等 20 秒才开始开门**
        // （服务端日志实测：`握手完成` 与 `登录成功` 之间正好 21 秒）。
        //
        // e2e 抓不到这个：`worldcmd.rs:150` 是**开局就先拉一次**（不依赖入站包），
        // 天然不会漏 —— 这也正是它 1 秒、而 app 21 秒的原因。
        //（先收进一个小 Vec 再发：至多一两条，免得闭包借 `self` 与 `&mut self.entrance` 打架。）
        let mut pending: Vec<mir2_protocol::envelope::Body> = Vec::new();
        flush_entrance(&mut self.entrance, &mut |b| pending.push(b.clone()));
        for b in &pending {
            self.send(b);
        }

        // 伤害飘字：世界只记账，这里取走并计时。
        //
        // ⚠️ **延后 [`STRIKE_DELAY_MS`] 再开始飘**：原版挨打那一条是
        // `SendDelayMsg(RM_STRUCK, …, 200)`（`ObjBase.pas:22263`）——
        // 挥砍动画**先**到、伤害/飘字**晚 200ms** 到。我们原来是同帧 ⇒
        // "还没砍到就掉血"（用户 2026-10-09 第 3 条）。
        // 判定用的是 `f.3.elapsed()`；未来的 Instant 会先饱和成 0 ⇒ 到点才冒出来。
        for d in self.world.take_damage() {
            let (x, y) = self.pos_of(d.target_id);
            self.floaters.push((
                format!("{}", d.value),
                x,
                y,
                Instant::now() + Duration::from_millis(STRIKE_DELAY_MS),
            ));
            // 挨打的是自己 ⇒ 惨叫（按性别，`Actor.pas:2243-2247`）
            if d.target_id == self.world.self_id {
                self.pending_sfx
                    .push(mir2_core::sound::scream(self.self_sex()));
            }
        }
        // 自己死了 ⇒ 死亡声 + game over 音乐（`Actor.pas:2368-2376`）。
        // **只放一次**：`self_dead` 会一直为真（尸体还在），每帧放就成了噪音。
        if self.world.self_dead && !self.died_once {
            self.died_once = true;
            self.pending_sfx
                .push(mir2_core::sound::die(self.self_sex()));
            self.gameover = true;
        }
        self.floaters
            .retain(|f| f.3.elapsed() < Duration::from_millis(900));

        self.sync_anims();

        if let Some(why) = self.entrance.failed() {
            if !self.status.starts_with("失败") {
                self.status = format!("失败：{why}");
                println!("[net] {}", self.status);
            }
        }
    }

    /// 把握手状态机吐出来的信封翻译成会话命令发出去。
    pub(crate) fn send(&self, body: &mir2_protocol::envelope::Body) {
        match to_cmd(body, self.session) {
            Some(c) => {
                let _ = self.sess.cmds.send(c);
            }
            // ⚠️ **绝不静默吞**：这条路上漏一项就是"点了按钮没反应、服务端连请求都没收到"
            //（`LoginSaltRequest` 与 `CreateAccount` 都这么丢过，见 `to_cmd` 的说明）。
            None => eprintln!("[net] ⚠️ 这条命令没有翻译，已丢弃：{body:?}"),
        }
    }

    /// 取走"这一帧该响的音效"（世界只记账，设备在主循环里）。
    pub(crate) fn take_sfx(&mut self) -> Vec<u16> {
        std::mem::take(&mut self.pending_sfx)
    }

    /// 是不是**刚**死了（取走后清空）：主循环据此切 game over 音乐。
    pub(crate) fn take_gameover(&mut self) -> bool {
        std::mem::take(&mut self.gameover)
    }

    /// 取走"这次失败的原因"，**只给一次**（连接层断开 / 握手失败）。
    ///
    /// ⚠️ 这两样都是**粘性**的：`entrance.stage == Failed(..)` 会一直挂着、`fail` 也一直不空
    /// ⇒ 界面必须**取走**，不能每帧读 —— 否则弹窗刚点掉，下一帧又弹回来
    ///（用户 2026-10-08 报的"登录失败弹窗关不掉"）。
    pub(crate) fn take_fail(&mut self) -> Option<String> {
        if let Some(why) = self.fail.take() {
            return Some(connect_hint(&why));
        }
        self.entrance.take_failed()
    }

    /// 自己的性别（`0` 男 `1` 女）——协议里 `dress = 形状*2 + 性别`
    /// （`core/src/actor.rs:31`）；拿不到特征时按男（原版 `m_btSex = 0` 是男）。
    pub(crate) fn self_sex(&self) -> u8 {
        self.world
            .self_feature
            .as_ref()
            .map_or(0, |f| (f.dress & 1) as u8)
    }

    /// 自己这一帧的**走路动画帧号**（原版脚步按帧 1 / 帧 4 播，`Actor.pas:2659-2660`）。
    ///
    /// `None` = 没在走（站着 / 攻击 / 受击…）⇒ 调用方清掉"上一帧"的记录，
    /// 免得停下再走时被当成"帧号没变"。
    pub(crate) fn self_walk_frame(&self, now: Instant) -> Option<(u16, bool)> {
        let a = self.anims.get(&self.world.self_id)?;
        if !a.moving(now) {
            return None;
        }
        let run = self.world.self_run;
        let pose = mir2_core::actor::human_pose(a.action, true, run);
        if !matches!(
            pose.act,
            mir2_core::actor::HAct::Walk | mir2_core::actor::HAct::Run
        ) {
            return None;
        }
        // 跑在原版是**另一段动作、脚步基号 +2**（`Actor.pas:2237`）⇒ 两个都返回。
        // ⚠️ 帧号取**移动相位**（与画精灵同一份）：拿"这一格走了多久"会让脚步声
        // 与动画错开（动画是连续推的，见 `ActorAnim::walk_since`）。
        Some((pose.act.act().frame_at(a.walk_ms(now)), run))
    }

    /// 发一次移动输入（走）。方向用**线上编号**（`core::world` 里也不做 ±1 转换）。
    pub(crate) fn walk(&self, dir: mir2_protocol::Direction) {
        let _ = self.sess.cmds.send(mir2_net::Cmd::Move(dir as i32));
    }

    /// **跑**一步（原版 `CM_RUN`；服务端一步 2 格）。
    pub(crate) fn run(&self, dir: mir2_protocol::Direction) {
        let _ = self.sess.cmds.send(mir2_net::Cmd::Run(dir as i32));
    }

    /// 把"这一帧看到的"折进各实体的动画状态：移动了就给补间的起止，动作变了就重置计时。
    ///
    /// ⚠️ 只在**变化时**刷新 `changed_at`：`EntityMove`/`EntityAction` 不是每帧都来，
    /// 每帧重置的话走路会永远停在第一帧、动作永远播不完。
    pub(crate) fn sync_anims(&mut self) {
        let now = Instant::now();
        // `run` 也要带上：它决定走/跑播哪段图（见 `human_pose` 的 run 分支）
        let mut live: Vec<SeenEntity> = self
            .world
            .entities
            .values()
            .map(|e| (e.id, (e.x, e.y), e.action, e.run, e.action_seq))
            .collect();
        if self.world.in_world() {
            live.push((
                self.world.self_id,
                self.world.self_pos,
                self.world.self_action,
                self.world.self_run,
                self.world.self_action_seq,
            ));
        }
        let ids: std::collections::HashSet<u64> = live.iter().map(|(id, ..)| *id).collect();
        for (id, cell, action, run, action_seq) in live {
            let a = self.anims.entry(id).or_insert(ActorAnim {
                cell,
                from: None,
                action,
                action_seq,
                pending_action: None,
                changed_at: now,
                action_at: now,
                // 刚出现/刚进视野：先按"走一格"算，下一步会据实重算
                move_ms: WALK_STEP_MS,
                walk_since: now,
            });
            if a.cell != cell {
                // ⚠️ 顺序要紧：`was_moving` 必须在改 `cell`/`from` **之前**问 ——
                // 它决定走路动画"接着推"还是"从头开始"（见 `next_walk_since`）。
                let was_moving = a.moving(now);
                let from = a.cell;
                // 服务端用 `from == to` 表达**原地转身**（没有独立的转身消息）——
                // 那不是移动：只更新朝向，别动补间/相位。
                if from != cell {
                    a.from = Some(from);
                    a.changed_at = now;
                    // 自己按**本客户端的步频**补间（否则每格末尾空 50ms：动画闪回站立 +
                    // 镜头停一下），别人按服务端的节流 —— 见 `self_move_ms`。
                    a.move_ms = if id == self.world.self_id {
                        self_move_ms(cell.0 - from.0, cell.1 - from.1, run)
                    } else {
                        move_ms(cell.0 - from.0, cell.1 - from.1, run)
                    };
                    a.walk_since = next_walk_since(was_moving, a.walk_since, now);
                }
                a.cell = cell;
            }
            // ⚠️ 判据是**动作事件计数**（`action_seq`），不是动作值：普通攻击的值恒为 1，
            // 按值判 ⇒ 第二次以后的每一刀都不重播挥砍（用户 2026-10-09 反复报的那条），
            // 换目标也一样漏。计数每收到一条 `EntityAction` 就 +1，所以每刀都重播。
            //
            // ⚠️ 并且**走/跑没走完时不释放攻击动作**（用户同日补充的第 2 条）：那一段
            // 挥砍先存进 `pending_action`，等这一步走完再补播（连 `action_at` 一起推迟，
            // 否则走路的 600ms 里挥砍就播完了）。**受击/死亡不推迟** —— 被打/死要立刻表现。
            let urgent = action.is_some_and(|v| !mir2_core::world::action::is_attack(v));
            if a.action_seq != action_seq {
                a.action_seq = action_seq;
                if a.moving(now) && !urgent {
                    a.pending_action = Some((action, action_seq));
                } else {
                    a.action = action;
                    // 立刻表现的那一类（受击/死亡）把压着的挥砍作废 —— 挨打优先
                    a.pending_action = None;
                    // ⚠️ 只动**动作钟**（见 `action_at` 的说明）：动 `changed_at` 会让补间从头开始
                    a.action_at = now;
                    // 换动作（砍/受击…）就从"走路的相位"里出来了 ⇒ 相位重开
                    a.walk_since = now;
                }
            } else if a.action != action && !a.moving(now) {
                // 计数没变而值变了（理论上不该发生）：照样认账，免得漏动作。
                a.action = action;
                a.action_at = now;
                a.walk_since = now;
            }
            // 这一步走完了 ⇒ 把压着的挥砍补上。之后要不要走/跑，由 main 的追打循环
            // 重新判（"攻击完后再次判断是不是要走/跑"）。
            if !a.moving(now) {
                if let Some((act, _)) = a.pending_action.take() {
                    a.action = act;
                    a.action_at = now;
                    a.walk_since = now;
                }
            }
        }
        // 视野外的实体不再留着（否则跑一圈地图会攒下几百条死账）
        self.anims.retain(|id, _| ids.contains(id));
    }

    /// 某个实体当前所在的格子（用来把飘字摆在它头上）。
    ///
    /// 找不到（已经消失）就退回自己的位置 —— 总比不画好。
    pub(crate) fn pos_of(&self, id: u64) -> (i32, i32) {
        if id == self.world.self_id {
            return self.world.self_pos;
        }
        self.world
            .entities
            .get(&id)
            .map(|e| (e.x, e.y))
            .unwrap_or(self.world.self_pos)
    }

    /// 打一下**紧邻**（八格）的那个实体。
    ///
    /// ⚠️ 只认相邻：服务端的 `AttackInput` 也只在相邻八格里才认（见那边的说明），
    /// 目标太远服务端会静默忽略。返回 false = 身边没有可打的目标。
    pub(crate) fn attack_adjacent(&self) -> bool {
        let (sx, sy) = self.world.self_pos;
        let target = self.world.entities.values().find(|e| {
            !e.dead && (e.x - sx).abs() <= 1 && (e.y - sy).abs() <= 1 && (e.x != sx || e.y != sy)
        });
        match target {
            Some(e) => {
                println!("[net] 攻击 {} (ActorId={})", e.name, e.id);
                self.attack_target(e.id)
            }
            None => false,
        }
    }

    /// 打**指定的**目标（左键点怪锁住之后每帧来一次）。
    ///
    /// 消息号固定 `ATTACK_HIT`（普通挥砍）：原版会按武器/技能挑 `CM_HEAVYHIT/CM_POWERHIT/…`
    ///（`ClMain.pas:2695-2722`），那些（重击/攻杀/刺杀）都还没接 —— 这里只发基础那一种。
    pub(crate) fn attack_target(&self, id: u64) -> bool {
        self.sess
            .cmds
            .send(mir2_net::Cmd::Attack {
                target_id: id,
                action: mir2_protocol::AttackAction::AttackHit as i32,
            })
            .is_ok()
    }

    /// 自己的**渲染位置**（补间后的浮点格）；没进世界时给服务端那一格。
    ///
    /// 相机、小地图、大地图都用它 —— 它们**必须**和画精灵用同一份（`ActorAnim::draw_pos`），
    /// 否则"人在这、图心在那"。
    pub(crate) fn self_render(&self, now: Instant) -> Option<(f32, f32)> {
        if !self.world.in_world() {
            return None;
        }
        Some(self_render_pos(
            self.anims.get(&self.world.self_id),
            self.world.self_pos,
            now,
        ))
    }
}

/// 方向键：联网且在世界里 ⇒ 走一步并返回 `true`（调用方就别动镜头了）。
///
/// 离线时返回 `false` ⇒ 保持原来的"方向键平移镜头"（开发查看器最常用的动作）。
pub(crate) fn walk_if_online(net: &Option<Net>, dir: mir2_protocol::Direction) -> bool {
    move_if_online(net, dir, false)
}

/// 同上，但可以**跑**（原版：左键走、右键跑/按住 Ctrl 走改为跑）。
pub(crate) fn move_if_online(net: &Option<Net>, dir: mir2_protocol::Direction, run: bool) -> bool {
    match net {
        Some(n) if n.world.in_world() => {
            if run {
                n.run(dir);
            } else {
                n.walk(dir);
            }
            true
        }
        _ => false,
    }
}

/// 鼠标连续走路的**步频**（毫秒）：比服务端的节流稍慢一点，免得每步都被拒。
///
/// ⚠️ 服务端 `entity.Limiter` 现在是 `MinWalk = MinRun = 600ms`
///（**照原版**：`GameConfig.pas:1038-1052` 的 `dwWalkIntervalTime` 与
/// `dwRunIntervalTime` 同为 600，而原版走跑读的是**同一个** `m_dwMoveTick`）——
/// 跑不是"间隔更短"，而是"同样 600ms 走 2 格"。
/// 原来这里 `RUN_MS = 450`、服务端 `MinRun = 400` ⇒ **跑比原版快 50%**，
/// 表现就是"滑步"（用户 2026-10-09 第 4 条"走、跑的处理逻辑问题"）。
pub(crate) const WALK_MS: u64 = 650;

/// 跑一步（2 格）的**发送**间隔：与走同一档（服务端 `MinRun` 也是 600）⇒ 650 留余量。
pub(crate) const RUN_MS: u64 = 650;

/// **按住鼠标时**重取目标的间隔（毫秒）—— 原版 `ClMain.pas:2678-2679`：
/// `if (ssLeft in Shift) or (ssRight in Shift)) and (GetTickCount - mousedowntime > 300)`.
pub(crate) const MOUSE_REPEAT_MS: u64 = 300;

/// 移动**被服务端拒了**之后，多久不许再发（毫秒）—— 原版 `ActionFailed` 的
/// `ActionFailLock`：`GetTickCount - ActionFailLockTime > 1000` 才解锁（`ClMain.pas:4005-4020`）。
///
/// 不锁的表现：朝一个撞墙的方向**每 `WALK_MS` 发一次**、每次都被拒 ⇒ 人物"卡在那儿不动"
/// 而且服务端日志刷满（用户 2026-10-08 报的"跑不到怪身边"多半就有它）。
pub(crate) const MOVE_FAIL_LOCK_MS: u64 = 1000;

/// 受击那一条（伤害数字 / 受击动作）**延后**多久 —— 原版
/// `SendDelayMsg(RM_STRUCK, …, 200)`（`ObjBase.pas:22263`）。
///
/// 挥砍动画立即到、伤害晚 200ms 到：这样"砍到哪一帧才掉血"与原版一致，
/// 否则伤害数字在挥砍第一帧就冒出来（看着像没砍中就掉血）。
pub(crate) const STRIKE_DELAY_MS: u64 = 200;

/// 把握手状态机的**待发命令排空**（`Entrance::next_cmd`）。
///
/// ⚠️ 语义就是"**每帧**调一次"：有的阶段转折不是被信封推动的 —— 最典型的是
/// `Ev::Connected`（握手 nonce 到了）之后要发 `LoginSaltRequest`。
/// 只在"收到信封"时拉，这一步就得等下一个入站包（心跳是 20 秒一次）。
///
/// 抽成独立函数是为了能单测这条契约（不需要真的网络）。
pub(crate) fn flush_entrance(
    entrance: &mut mir2_core::entrance::Entrance,
    send: &mut impl FnMut(&mir2_protocol::envelope::Body),
) {
    if entrance.failed().is_some() || entrance.in_world() {
        return;
    }
    // `next_cmd` 只在 `Stage::Start` 有货（之后就 `None`）⇒ 不会在这里打转。
    while let Some(b) = entrance.next_cmd() {
        send(&b);
    }
}

/// 状态机吐出来的信封 → 会话命令（**这张表就是"接线"本身**）。
///
/// ⚠️ 漏一项的后果不是报错，而是**静默吞掉**：界面照常转圈，而服务端一条请求都收不到。
/// 这个坑已经栽过两次：
///   - `LoginSaltRequest`/`Login` ⇒「点了登录一直转圈」（e2e 的 `TestProtoRustLogin` 抓到的）；
///   - `CreateAccount` ⇒「点了建号没反应」（2026-10-08 用户报的：服务端日志里只有
///     `握手完成`，`LoginSaltRequest`/`CreateAccount` 一条都没到）。
///
/// ⇒ 两道防线：① `Net::send` 对 `None` **大声打日志**（不再 `_ => None` 悄悄丢）；
/// ② 单测 `建号那条路的每条命令都有翻译` 把这条链钉住。
///
/// 抽成自由函数就是为了能单测：`Reconnect` 要用会话号，所以 `session` 从外面传进来。
pub(crate) fn to_cmd(body: &mir2_protocol::envelope::Body, session: i32) -> Option<mir2_net::Cmd> {
    use mir2_protocol::envelope::Body;
    Some(match body {
        Body::Reconnect(_) => mir2_net::Cmd::Reconnect(session),
        Body::LoginSaltRequest(r) => mir2_net::Cmd::LoginSaltRequest(r.account.clone()),
        Body::Login(l) => mir2_net::Cmd::Login {
            account: l.account.clone(),
            proof_hex: l.password_hash.clone(),
        },
        // 建号（D-32）：发的是口令的**校验值** `hex(K)`，不是证明 —— 建号时服务端
        // 手里什么都没有，得拿这个值落库才能验以后的登录（见 `account.proto` 的说明）。
        Body::CreateAccount(c) => mir2_net::Cmd::CreateAccount {
            account: c.account.clone(),
            verifier_hex: c.verifier.clone(),
        },
        Body::ListCharacters(_) => mir2_net::Cmd::ListCharacters,
        Body::SelectCharacter(s) => mir2_net::Cmd::SelectCharacter(s.character_id),
        // 建/删角（选角界面）：状态机吐的是**协议 body**，会话层再翻成 `Cmd`。
        Body::CreateCharacter(c) => mir2_net::Cmd::CreateCharacter {
            name: c.name.clone(),
            class: c.class,
            gender: c.gender,
            hair: c.hair,
        },
        Body::DeleteCharacter(d) => mir2_net::Cmd::DeleteCharacter {
            character_id: d.character_id,
            proof_hex: d.password_hash.clone(),
        },
        // 世界输入不走这里（`Cmd::Move`/`Attack` 由输入那条路直发）；服务端单向消息
        // （实体事件、心跳…）本来就不是命令 ⇒ 到这里是 `None`，由调用方打日志。
        _ => return None,
    })
}

/// 把连接层的原因翻成"人话 + 下一步该查什么"。
///
/// ⚠️ `Connection refused` 与"口令错"是**两回事**：前者是 TCP 层没人监听
/// （服务端没起、或者起的时候没带 `-proto-addr`），根本还没走到鉴权。
/// 这一条就是为这个区分写的 —— 别让人对着"连不上"去怀疑密码。
pub(crate) fn connect_hint(why: &str) -> String {
    let w = why.to_ascii_lowercase();
    if w.contains("connection refused") || w.contains("os error 61") {
        return format!(
            "{why}\n\n(服务端没在监听：gamesvr 要带 -proto-addr 127.0.0.1:7500 才开新协议入口)"
        );
    }
    if w.contains("timed out") || w.contains("timeout") {
        return format!("{why}\n\n(超时：地址/防火墙？服务端卡住了？)");
    }
    why.to_string()
}

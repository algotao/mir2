//! 会话世界状态：**收到一条信封 → 世界变成什么样**（纯函数，无 IO、无 SDL）。
//!
//! 为什么放在 `core`：按 [D-18] 的纪律，`client/app`（GUI）与 `client/e2e`（无头）
//! 必须走**同一份**协议编解码与会话状态机 —— 否则契约测试检验的就不是真客户端，
//! 而"app 自己另写一份解释逻辑"正是双实现漂移的温床（plan R-1/R-10）。
//!
//! 这里只管**状态**，不管怎么画：渲染需要的字段（坐标/朝向/外观/血量）都留在
//! `Entity` 上，具体画法由调用方决定（app 目前画标记，将来换精灵是渲染层的事）。
//!
//! [D-18]: ../../../docs/decisions.md

use std::collections::BTreeMap;

use mir2_protocol as proto;
use proto::envelope::Body;
use proto::Envelope;

/// 视野里的一个实体（**不含自己**：自己在 `World` 上单列，见 `EnterWorld`）。
#[derive(Debug, Clone, PartialEq)]
pub struct Entity {
    pub id: u64,
    /// 0=玩家 1=怪物 2=NPC（`EntityState.kind`）。
    pub kind: u32,
    pub name: String,
    pub x: i32,
    pub y: i32,
    /// `proto::Direction` 的原值（1..8；0 = 未指定）。**保持线上编号不乱转**，
    /// 免得"哪一层减了 1"这种错在两端之间来回漂。
    pub dir: i32,
    pub feature: Option<proto::EntityFeature>,
    pub hp: u32,
    pub max_hp: u32,
    /// 最近一次移动是不是**跑**（`EntityMove.run`）—— 决定播 `ActWalk` 还是 `ActRun`。
    ///
    /// ⚠️ 它是"这一步"的属性（不是"这个人常常跑"），所以每次移动都会被重写；
    /// 停下来之后这个值虽然还留着，但渲染层只在**移动中**看它（`ActorAnim::moving`）。
    pub run: bool,
    pub status_bits: u64,
    /// 已经死了（收到 `Death` 之后、`EntityDisappear` 之前的那段时间 = 尸骨）。
    ///
    /// ⚠️ 死亡**不等于**消失：原版里尸骨会留一会儿（法师还能打尸骨），
    /// 所以这里只标记，实体仍在 `entities` 里 —— 由渲染层决定怎么画（半透明/躺下）。
    pub dead: bool,
    /// 最近一次动作（见 `protocol.md` §9.5 的动作 id 值域：1..8 攻击 51 受击 52 死亡）。
    pub action: Option<u32>,
    /// 收到过多少条 `EntityAction`（**动作事件计数**，不是值）。
    ///
    /// # ⚠️ 为什么需要它（用户 2026-10-09 报的"攻击只有第 1 下有挥砍"）
    ///
    /// 服务端**每次出手**都发一条 `EntityAction`，但普通攻击的 action 值**恒为 1**
    ///（见 `netproto.go` 的 `EntityAction` 值域说明）⇒ 渲染层若按"值变没变"决定要不要
    /// 重播动画，第二次以后的每一刀都是"值没变 ⇒ 不播"⇒ 表现就是**只有第一下有挥砍动作**，
    /// 掉血照常。换目标也一样（值还是 1，仍然"没变"）。
    ///
    /// 所以判据必须是**消息**而不是值：每收到一条就 +1，渲染层按它重播动画。
    pub action_seq: u64,
}

/// 一个**开着的 NPC 对话**（`NpcSay`）。
///
/// 原版把"正文 + 编号选项"拼成一整块文本发过来（`SM_*`），客户端再靠玩家点文本里的
/// `<文字/@标签>` 回包；新协议把两者**拆开**给（`protocol/npc.proto` 的说明），
/// 所以这里存结构化的 `options`，客户端不必去解析中文里的编号。
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct NpcDialog {
    pub npc_id: u64,
    /// 正文（可能多行）。
    pub text: String,
    /// `(index, 文字)`，`index` 从 1 起（回包时原样带回去）。
    pub options: Vec<(u32, String)>,
}

/// 一次伤害事件（`Damage`）。**由调用方取走**（`World::take_damage`）并决定怎么表现。
///
/// 为什么不在这里做"飘字计时"：`World` 是纯状态、**没有时钟**（见文件头）。
/// 计时与淡出是渲染层的事，那里本来就有帧时钟。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct DamageEvent {
    pub attacker_id: u64,
    pub target_id: u64,
    /// 伤害值（`flags` 里的位说明暴击/闪避等，见 combat.proto）。
    pub value: i32,
    pub flags: u32,
}

impl Entity {
    fn from_state(s: &proto::EntityState) -> Self {
        let pos = s.position.unwrap_or_default();
        Entity {
            id: s.entity_id,
            kind: s.kind,
            name: s.name.clone(),
            x: pos.x,
            y: pos.y,
            dir: s.direction,
            feature: s.feature,
            hp: s.hp,
            max_hp: s.max_hp,
            // 快照不带"跑"这个信息（它只属于一次移动）⇒ 出现时一律先当走的
            run: false,
            status_bits: s.status_bits,
            dead: false,
            action: None,
            action_seq: 0,
        }
    }

    /// 位置更新（`EntityMove` 只给 from/to，不给 kind/名字 ⇒ 就地改）。
    ///
    /// `run` 是**这一步**是不是跑的 —— 渲染层靠它决定播 `ActWalk` 还是 `ActRun`
    ///（两段的图号差 64，见 `actor::HAct`）。原地转身（`from == to`）时服务端给 `false`。
    fn set_pos(&mut self, x: i32, y: i32, dir: i32, run: bool) {
        self.x = x;
        self.y = y;
        self.run = run;
        if dir != proto::Direction::Unspecified as i32 {
            self.dir = dir;
        }
    }
}

/// 背包/装备里的一件物品（`ItemStack` 的客户端形态）。
///
/// 图标怎么取：**`Items.wzl[looks]`** —— 2026-10-09 用 `wzldump` 逐张比对确认过
///（`looks=398` 是红瓶 = 金创药(小量)、`394` 是蓝瓶 = 魔法药(小量)，与服务端物品表的
/// `Looks` 列一致）。所以这里只需把 `looks` 原样带过来，图号换算在渲染层。
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct BagItem {
    /// 物品模板索引（1-based；0 = 空槽）。
    pub index: u32,
    pub name: String,
    /// `Items.wzl` 的图号。
    pub looks: u32,
    /// 叠加数量。**可叠加物的数量来自 `dura`**（原版没有数量字段，见服务端 `itemStack`）。
    pub count: u32,
    pub dura: u32,
    pub dura_max: u32,
}

impl BagItem {
    fn from_proto(it: &proto::ItemStack) -> Self {
        Self {
            index: it.index,
            name: it.name.clone(),
            looks: it.looks,
            count: it.count,
            dura: it.dura,
            dura_max: it.dura_max,
        }
    }

    /// 空槽（`index = 0` 或没有名字）。
    pub fn is_empty(&self) -> bool {
        self.index == 0 && self.name.is_empty()
    }
}

/// 自己的能力值（客户端要画血条/经验条/负重条）。
#[derive(Debug, Clone, Copy, PartialEq, Default)]
pub struct Ability {
    pub level: u32,
    pub hp: u32,
    pub max_hp: u32,
    pub mp: u32,
    pub max_mp: u32,
    pub gold: u64,
    /// 负重 / 最大负重（`Prguse[7]` 那条**负重条**，原版 `FState.pas:3663-3671`）。
    pub weight: u32,
    pub max_weight: u32,
    /// 经验：`exp` 是**级内**经验，`max_exp` 是本级升下一级所需
    ///（HUD 的经验条 = `Prguse[7]` 按 `exp/max_exp` 裁右边界，原版 `FState.pas:3646-3659`）。
    ///
    /// ⚠️ 用户 2026-10-09：「经验比例、当前等级、包裹负重现在也没有显示」——
    /// 等级一直有（`level`），经验/负重是**协议一直没下发这两个数**，服务端补齐后才画得出。
    pub exp: u32,
    pub max_exp: u32,
    /// 攻击 / 魔法 / 道术的上下限（状态窗那几行"攻击 2-5"）。
    ///
    /// ⚠️ 这些字段**协议里一直都有**（`common.proto` 的 `Ability.dc_min..mac`），服务端
    /// 也一直在填（`netproto.go` 的 `protocolAbility`）—— 只是客户端到 2026-10-10 做状态窗
    /// 才解析。想加"界面上要显示的数值"时，**先看协议里有没有**，别急着加协议字段。
    pub dc_min: u32,
    pub dc_max: u32,
    pub mc_min: u32,
    pub mc_max: u32,
    pub sc_min: u32,
    pub sc_max: u32,
    /// 防御 / 魔防（原版是 (min,max) 对偶，新协议的 `ac`/`mac` 是单值，服务端取 min；
    /// 见 `protocolAbility` 的说明）。
    pub ac: u32,
    pub mac: u32,
}

impl Ability {
    /// 从协议的 `Ability` 取我们画界面要用的那几个字段（`AbilityUpdate` 与 `LevelUp`
    /// 共用一份映射 —— 两个地方各写一遍，正是"加字段时漏一个"的老路）。
    fn from_proto(ab: &proto::Ability) -> Self {
        Self {
            level: ab.level,
            hp: ab.hp,
            max_hp: ab.max_hp,
            mp: ab.mp,
            max_mp: ab.max_mp,
            gold: ab.gold,
            weight: ab.weight,
            max_weight: ab.max_weight,
            exp: ab.exp,
            max_exp: ab.max_exp,
            dc_min: ab.dc_min,
            dc_max: ab.dc_max,
            mc_min: ab.mc_min,
            mc_max: ab.mc_max,
            sc_min: ab.sc_min,
            sc_max: ab.sc_max,
            ac: ab.ac,
            mac: ab.mac,
        }
    }
}

/// 一条信封带来的变化（调用方据此决定要不要重绘）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Change {
    /// 与画面无关（心跳、错误、未知消息…）。
    None,
    /// 世界变了：至少一个实体的位置/存在性/自身状态变了。
    World,
}

/// 一条会话的世界状态。
#[derive(Debug, Default, Clone)]
pub struct World {
    /// 自己的实体 id（0 = 还没进世界）。
    pub self_id: u64,
    /// 自己的职业（`CharClass` 的原值：0 武士 / 1 法师 / 2 道士）。
    ///
    /// ⚠️ 协议里**没有**这个字段（官方客户端是从角色信息里拿的）。我们在**选角那一刻**
    /// 就知道（`CharacterSummary.class`）⇒ 与 `self_name` 一起记住，见 `remember_self_class`。
    /// 用途：HUD 的球体 —— 官方对"武士且未满 28 级"用另一套单球美术（`FState.pas:3608-3632`）。
    pub self_class: Option<i32>,
    /// 自己的**角色名**（`EnterWorld.self_name`）—— 画在自己头顶。
    ///
    /// ⚠️ 它**不在** `entities` 里（快照刻意不含自己），协议也不在别处再给 ⇒
    /// 只有 `EnterWorld` 那一条给了它。换图（`ChangeMap`）**不要清**它：名字不会变。
    ///
    /// 用户 2026-10-09：「显示玩家自己时，不要使用"自己"这个词，应显示玩家角色名称」
    /// ⇒ 头顶永远是**角色名**，一个字都不许拿占位词凑。名字在**选角那一刻**客户端
    /// 就已经知道（`CharacterSummary.name`），所以先用 `remember_self_name` 记下，
    /// `EnterWorld.self_name` 是空的时候也画得出真名（老服务端/异常路径）。
    pub self_name: String,
    /// 地图**名字**（本项目地图按名字索引，D-22）。
    pub map_name: String,
    /// 开着的 NPC 对话（`NpcSay` 带来、`NpcClose`/选到 `@exit` 时清）。
    pub dialog: Option<NpcDialog>,
    /// 地图的**显示名**（`EnterWorld`/`ChangeMap` 的 `map_title`，如"比奇省"）。
    ///
    /// 官方客户端左下角那行抬头用它（`g_sMapTitle`，`DrawScrn.pas:513`：
    /// `抬头 + ' ' + X + ':' + Y`）—— 服务端下发，不是客户端本地表。
    pub map_title: String,
    /// 当前地图的**小地图图号**（0 = 该图没有小地图）。
    ///
    /// 服务端从 `MiniMap.txt` 查出来，随 `EnterWorld` / `ChangeMap` 一起下发
    ///（`protocol/scene.proto` 的 `minimap_index`）；客户端拿它当 `mmap` 图库的下标
    ///（**图号 − 1**，与 legacy 的 `ClMain.pas:6045-6051` 同一条规则）。
    pub minimap_index: u32,
    pub self_pos: (i32, i32),
    pub self_dir: i32,
    /// 视野内的实体，键为实体 id（`BTreeMap` ⇒ 遍历顺序稳定，画面不会每帧乱序）。
    pub entities: BTreeMap<u64, Entity>,
    pub ability: Option<Ability>,
    /// 背包（`BagItems` 全量下发）。`None` = 该格是空的。
    ///
    /// ⚠️ 用户 2026-10-09 之前一直"看不到包裹"：`BagItems` 这条协议**两端都齐、
    /// 服务端却从不构造**（只发 legacy，而 legacy 下行会被 `protoDown` 丢掉）。
    /// 服务端补齐后（见 `docs/decisions.md` D-65）这里才拿得到东西。
    pub bag: Vec<Option<BagItem>>,
    /// 已穿戴（`EquippedItems`）。**按下标 = 槽位**（空槽是 `None`）。
    ///
    /// 与背包不同：装备槽的位置本身有意义（武器/衣服/项链…），所以服务端会为
    /// 空槽也留位（见 `sendUseItems` 的说明）。
    pub equip: Vec<Option<BagItem>>,
    /// 服务端最近一次给的 tick（`EnterWorld` 的 server_tick）。
    pub server_tick: u32,
    /// 累计收到多少条**实体事件**（出现/消失/移动）。
    /// 这是"世界在动"最直接的观测量：联调时看它涨没涨，比盯着画面猜靠谱。
    pub entity_events: u64,
    /// 累计收到多少条**看不懂**的消息（§4.1：记数 + 忽略，不 panic）。
    pub unknown: u64,
    /// 累计收到多少条伤害事件（"在打架"的观测量）。
    pub damage_events: u64,
    /// 累计收到多少条动作（挥砍/受击/死亡）。
    pub actions: u64,
    /// 自己的 hp/max_hp（`EntityHealth` 与 `AbilityUpdate` 都会更新它）。
    ///
    /// ⚠️ 为什么不复用 `ability.hp`：`EntityHealth` **不带等级/金币**，
    /// 往 `ability` 里塞会让"等级=0"这种假值冒出来。两个来源各写各的字段。
    pub self_hp: Option<(u32, u32)>,
    /// 自己是不是死了（回城 / `Revive` 之后由 `ChangeMap` 那条恢复）。
    pub self_dead: bool,
    /// 自己最近一次移动是不是**跑**（与 `Entity::run` 同义，自己不在 `entities` 里）。
    pub self_run: bool,
    /// 自己最近一次动作（挥砍…）—— 自己不在 `entities` 里，所以单列。
    pub self_action: Option<u32>,
    /// 自己收到过多少条 `EntityAction`（同 `Entity::action_seq`：判"又砍了一刀"用的是
    /// **消息**而不是动作值 —— 普通攻击的值恒为 1，按值判会漏掉第二次以后的每一刀）。
    pub self_action_seq: u64,
    /// 自己的外观（`EnterWorld` / `ChangeMap` 里的 `self_feature`）。
    ///
    /// ⚠️ 它**不在** `entities` 里：快照刻意不含自己（见 `snapshot_drops_self`）。
    /// 但没有它，客户端连"自己长什么样"都不知道（画不出自己的精灵）。
    pub self_feature: Option<proto::EntityFeature>,
    /// 待消费的伤害事件（调用方 `take_damage()` 取走）。
    damage: Vec<DamageEvent>,
    /// 累计收到多少条**移动被拒**（`MoveRejected`）。
    ///
    /// ⚠️ 调用方要拿它**变没变**来判断"刚刚被拒了一次"（新协议这条是原版 `SM_MOVEFAIL`
    /// 的对应物；原版收到就 `ActionFailed`：清掉走法目标 + 锁 1 秒，见 `ClMain.pas:4005-4012`）。
    /// 少了这个反应，客户端会朝一个撞墙的方向**每 `WALK_MS` 发一次**，表现就是"卡在那儿不动"。
    pub move_fail: u64,
    /// 最近一次移动被拒的**原因**（`scene.proto`：1=超速 2=越界 3=阻挡）。
    pub move_fail_reason: u32,
}

impl World {
    pub fn in_world(&self) -> bool {
        self.self_id != 0
    }

    /// 记下"这次要进的那个角色的名字"（选角那一刻就知道，见 `CharacterSummary.name`）。
    ///
    /// 用户 2026-10-09：头顶（以及任何显示"玩家自己"的地方）一律用**角色名**，
    /// 不许出现"自己"这种占位词。调用点：选角成功、发出 `SelectCharacter` 之后。
    pub fn remember_self_name(&mut self, name: &str) {
        if !name.is_empty() {
            self.self_name = name.to_string();
        }
    }

    /// 某个格子上有没有 **NPC**（`kind == 2`）—— 点击时先用它判"是不是在跟 NPC 说话"。
    ///
    /// ⚠️ 为什么不能靠 `attack_target_at`：那个只认**可打的活怪**（原版
    /// `GetAttackFocusCharacter`），NPC 不在其中 ⇒ 点在 NPC 上会被当成"点空地走路"，
    /// 于是客户端反复往 NPC 那一格走、被挡（`reason=3`）、再试（用户 2026-10-09 报的）。
    pub fn npc_at(&self, x: i32, y: i32) -> Option<u64> {
        self.entities
            .values()
            .find(|e| e.kind == KIND_NPC && !e.dead && e.x == x && e.y == y)
            .map(|e| e.id)
    }

    /// 这个 ActorId 是不是一个**活着的 NPC**。
    ///
    /// ⚠️ 点 NPC 的判据必须是**它画出来的框**，不是"光标落在哪一格"：NPC 的精灵比
    /// 格子高（锚点在脚下），点它的头/肩时 `screen_to_cell` 得到的是**上面那一格**
    /// ⇒ 按格子判就成了"点了没反应、人还往那边走"（用户 2026-10-09 报的）。
    /// 画面那一侧有 `actor_rect`（悬停高亮用的同一份判据），这里只负责认"是不是 NPC"。
    pub fn npc_kind(&self, id: u64) -> bool {
        self.entities
            .get(&id)
            .is_some_and(|e| e.kind == KIND_NPC && !e.dead)
    }

    /// 关掉对话（本地清掉；要不要告诉服务端由调用方决定）。
    pub fn close_dialog(&mut self) {
        self.dialog = None;
    }

    /// 记下"这次进的那个角色的职业"（选角列表里就有，见 `CharacterSummary.class`）。
    ///
    /// 用途只有一处：HUD 的球体按官方规则要分"武士且 <28 级"那一支
    ///（`FState.pas:3608` 用 `m_btJob = 0`）—— 而协议没带职业，只能在这里记。
    pub fn remember_self_class(&mut self, class: i32) {
        self.self_class = Some(class);
    }

    /// 应用一条信封。返回值只表示"画面要不要重画"。
    pub fn apply(&mut self, env: &Envelope) -> Change {
        let Some(body) = env.body.as_ref() else {
            // §4.1：未知消息（未来版本新增的）case 未设置 ⇒ 记数 + 忽略，禁止 panic。
            self.unknown += 1;
            return Change::None;
        };
        match body {
            Body::EnterWorld(ew) => {
                self.self_id = ew.self_entity_id;
                // ⚠️ 只在服务端**给了名字**时覆盖：老服务端/异常路径给空串时，
                // 不许把已经记住的真名冲掉（否则头顶会空着或退回占位词）。
                if !ew.self_name.is_empty() {
                    self.self_name = ew.self_name.clone();
                }
                self.map_name = ew.map_name.clone();
                self.map_title = ew.map_title.clone();
                self.minimap_index = ew.minimap_index;
                let pos = ew.position.unwrap_or_default();
                self.self_pos = (pos.x, pos.y);
                self.self_dir = ew.direction;
                self.server_tick = ew.server_tick;
                // 自己的外观（服务端单独给，不在 entities 里 —— 见字段说明）
                self.self_feature = ew.self_feature;
                // 初始快照：整份替换（换图/重进都走这里）。
                self.self_dead = false; // 进图（含复活后重新进）自己一定是活的
                self.entities.clear();
                for s in &ew.entities {
                    // ⚠️ 服务端保证快照里没有自己，但客户端也不能因此就假设它一定没有：
                    // 真收到的话忽略掉，免得画面上出现两个"自己"。
                    if s.entity_id == self.self_id {
                        continue;
                    }
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                self.entity_events += 0; // 快照不算"事件"
                Change::World
            }
            Body::ChangeMap(cm) => {
                self.map_name = cm.map_name.clone();
                self.map_title = cm.map_title.clone();
                self.minimap_index = cm.minimap_index; // 换图 = 换一张缩略图
                self.self_feature = cm.self_feature;
                // 回城/传送（含死亡回城）走的就是这条 ⇒ 自己恢复为活着的。
                self.self_dead = false;
                let pos = cm.position.unwrap_or_default();
                self.self_pos = (pos.x, pos.y);
                self.entities.clear();
                for s in &cm.entities {
                    if s.entity_id == self.self_id {
                        continue;
                    }
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                Change::World
            }
            Body::EntityAppear(a) => {
                let Some(s) = a.entity.as_ref() else {
                    self.unknown += 1;
                    return Change::None;
                };
                // 自己"出现"在别的实体事件里是正常的（别人看见我）；自己那条只更新自身。
                if s.entity_id == self.self_id {
                    let pos = s.position.unwrap_or_default();
                    self.self_pos = (pos.x, pos.y);
                    self.self_dir = s.direction;
                } else {
                    let e = Entity::from_state(s);
                    self.entities.insert(e.id, e);
                }
                self.entity_events += 1;
                Change::World
            }
            Body::EntityDisappear(d) => {
                if d.entity_id == self.self_id {
                    // 自己"消失"不该发生（下线是断连）。记数不崩。
                    self.unknown += 1;
                    return Change::None;
                }
                self.entities.remove(&d.entity_id);
                self.entity_events += 1;
                Change::World
            }
            Body::EntityMove(m) => {
                let to = m.to.unwrap_or_default();
                if m.entity_id == self.self_id {
                    // 自己的权威回显：客户端预测过，这里以服务端为准。
                    self.self_pos = (to.x, to.y);
                    self.self_run = m.run;
                    if m.direction != proto::Direction::Unspecified as i32 {
                        self.self_dir = m.direction;
                    }
                } else if let Some(e) = self.entities.get_mut(&m.entity_id) {
                    e.set_pos(to.x, to.y, m.direction, m.run);
                } else {
                    // ⚠️ 没见过的实体在移动：**不要凭空造一个**（那会造出没有名字/外观的幽灵）。
                    // 正常不会发生（先出现后移动），发生了就记数——这通常是服务端的账本有漏。
                    self.unknown += 1;
                    return Change::None;
                }
                self.entity_events += 1;
                Change::World
            }
            Body::AbilityUpdate(a) => {
                if let Some(ab) = a.ability.as_ref() {
                    self.ability = Some(Ability::from_proto(ab));
                    self.self_hp = Some((ab.hp, ab.max_hp));
                    return Change::World;
                }
                Change::None
            }
            Body::LevelUp(l) => {
                // 升级带**完整能力值**（一条消息顶 legacy 的 SM_LEVELUP + SM_ABILITY 两条）。
                if let Some(ab) = l.ability.as_ref() {
                    self.ability = Some(Ability::from_proto(ab));
                    self.self_hp = Some((ab.hp, ab.max_hp));
                    return Change::World;
                }
                Change::None
            }
            Body::EntityHealth(h) => {
                if h.entity_id == self.self_id {
                    // 自己的血量：**单独存一份**，不往 `ability` 里塞 —— 因为
                    // `EntityHealth` 不带等级/金币，拿它去凑一个"半个 Ability"会让
                    // 等级显示成 0。两边都写 `self_hp`，谁先到都不丢。
                    self.self_hp = Some((h.hp, h.max_hp));
                } else if let Some(e) = self.entities.get_mut(&h.entity_id) {
                    e.hp = h.hp;
                    e.max_hp = h.max_hp;
                } else {
                    self.unknown += 1;
                    return Change::None;
                }
                Change::World
            }
            Body::Damage(d) => {
                self.damage.push(DamageEvent {
                    attacker_id: d.attacker_id,
                    target_id: d.target_id,
                    value: d.value,
                    flags: d.flags,
                });
                self.damage_events += 1;
                Change::World
            }
            Body::Death(x) => {
                if x.entity_id == self.self_id {
                    // 自己死了：位置与血量由 `ChangeMap`（回城）/`EntityHealth` 收尾，
                    // 这里只记数，不去动 `entities`。
                    self.self_dead = true;
                    return Change::World;
                }
                match self.entities.get_mut(&x.entity_id) {
                    Some(e) => {
                        // ⚠️ 只标记、**不删**：尸骨会留一会儿（原版还能打尸骨），
                        // 真正的移除是随后那条 `EntityDisappear`。
                        e.dead = true;
                        e.action = Some(action::DEATH);
                    }
                    None => {
                        self.unknown += 1;
                        return Change::None;
                    }
                }
                Change::World
            }
            Body::EntityAction(a) => {
                self.actions += 1;
                if let Some(e) = self.entities.get_mut(&a.entity_id) {
                    e.action = Some(a.action);
                    // 每来一条就 +1：渲染层靠它重播动画（值可能没变，见 `action_seq`）。
                    e.action_seq = e.action_seq.wrapping_add(1);
                } else if a.entity_id == self.self_id {
                    // 自己的动作（挥砍）—— 自己不在 `entities` 里，单独存一份。
                    self.self_action = Some(a.action);
                    self.self_action_seq = self.self_action_seq.wrapping_add(1);
                } else {
                    self.unknown += 1;
                    return Change::None;
                }
                Change::World
            }
            Body::NpcSay(say) => {
                self.dialog = Some(NpcDialog {
                    npc_id: say.npc_id,
                    text: say.text.clone(),
                    options: say
                        .options
                        .iter()
                        .map(|o| (o.index, o.text.clone()))
                        .collect(),
                });
                Change::World
            }
            Body::BagItems(b) => {
                self.bag.clear();
                for it in &b.items {
                    self.bag.push(if it.index == 0 {
                        None
                    } else {
                        Some(BagItem::from_proto(it))
                    });
                }
                Change::World
            }
            Body::EquippedItems(e) => {
                self.equip.clear();
                for it in &e.items {
                    self.equip.push(if it.index == 0 {
                        None
                    } else {
                        Some(BagItem::from_proto(it))
                    });
                }
                Change::World
            }
            // 单件增删改：服务端目前发的是**全量** `BagItems`（`sendBagItems` 一个出口），
            // 这三条留着照样实现 —— 将来做拖放优化时改发增量，客户端不用再改。
            Body::AddItem(a) => {
                let slot = a.slot as usize;
                if let Some(it) = &a.item {
                    while self.bag.len() <= slot {
                        self.bag.push(None);
                    }
                    self.bag[slot] = if it.index == 0 {
                        None
                    } else {
                        Some(BagItem::from_proto(it))
                    };
                }
                Change::World
            }
            Body::RemoveItem(r) => {
                let slot = r.slot as usize;
                if slot < self.bag.len() {
                    self.bag[slot] = None;
                }
                Change::World
            }
            Body::UpdateItem(u) => {
                let slot = u.slot as usize;
                if slot < self.bag.len() {
                    self.bag[slot] = u.item.as_ref().filter(|i| i.index != 0).map(BagItem::from_proto);
                }
                Change::World
            }
            Body::MoveRejected(r) => {
                // 这一步没成：服务端给了**权威位置**（我们不做预测 ⇒ 通常与 `self_pos` 相同，
                // 但"越界/阻挡"那条也可能把我们从错的位置拽回来）。照原版 `SM_MOVEFAIL`
                // (`ClMain.pas:4639-4648`)：位置按服务端给的走，其余交给调用方（清目标 + 锁 1 秒）。
                let p = r.authoritative_position.unwrap_or_default();
                self.self_pos = (p.x, p.y);
                self.move_fail += 1;
                self.move_fail_reason = r.reason;
                Change::World
            }
            _ => Change::None,
        }
    }

    /// 取走累计的伤害事件（渲染层用来弹伤害数字）。
    ///
    /// 取走而非"读后保留"，是因为调用方**每帧**都会来取一次，留着只会无限膨胀。
    pub fn take_damage(&mut self) -> Vec<DamageEvent> {
        std::mem::take(&mut self.damage)
    }

    /// `cell` 上有没有**可攻击的活目标**（返回它的 ActorId）—— 左键点怪就锁它。
    ///
    /// 照原版 `ClMain.pas:2863-2878`：只认**怪物**（玩家/守卫/商人要按住 Shift 才打，
    /// 那是 PK 那条线，见 `docs/use.md`）；死了的（尸骨）不算。
    ///
    /// ⚠️ 同一格上叠着好几个时取 `id` 最小的那个（`entities` 是 `BTreeMap`，顺序稳定
    /// ⇒ 每次点都锁同一个，不会"点一下换一个"）。
    pub fn attack_target_at(&self, cell: (i32, i32)) -> Option<u64> {
        self.entities
            .values()
            .find(|e| e.kind == KIND_MONSTER && !e.dead && (e.x, e.y) == cell)
            .map(|e| e.id)
    }

    /// 打 `target` 这一步该干什么（`None` = 目标没了/死了 ⇒ 调用方清掉目标）。
    ///
    /// 照原版 `TfrmMain.AttackTarget`（`ClMain.pas:2691-2743`）：
    ///
    /// ```text
    /// if |Δx| <= 1 and |Δy| <= 1 then  出手（受 CanNextHit 节流）
    /// else begin
    ///    if |Δx| <= 2 and |Δy| <= 2 then 走（caWalk） else 跑（caRun）   // 跑步砍
    ///    GetBackPosition(目标, 朝向) ⇒ 目标**旁边**那一格
    /// end
    /// ```
    ///
    /// 也就是说"够不着就先凑上去，而且凑的是**挨着它的那一格**"（不是它脚下那格：
    /// 那格被它站着，走过去也会被服务端挡）。
    pub fn combat_step(&self, target: u64) -> Option<CombatStep> {
        let e = self.entities.get(&target)?;
        if e.dead {
            return None;
        }
        let (dx, dy) = (e.x - self.self_pos.0, e.y - self.self_pos.1);
        if dx.abs() <= 1 && dy.abs() <= 1 {
            return Some(CombatStep::Attack);
        }
        Some(CombatStep::Approach {
            // 目标旁边、靠我这一侧的那一格（`GetBackPosition` 的等价写法）
            x: e.x - dx.signum(),
            y: e.y - dy.signum(),
            // 原版：距离 ≤ 2 走着过去，再远就"跑步砍"
            run: dx.abs().max(dy.abs()) > 2,
        })
    }
}

/// 怪物的 `kind`（`EntityState.kind`：0=玩家 1=怪物 2=NPC）。
pub const KIND_MONSTER: u32 = 1;
/// `EntityState.kind` = NPC（商人/功能 NPC）。**不可打**、点它是"说话"。
pub const KIND_NPC: u32 = 2;

/// `EntityState.status_bits` 里的**红名**位。
///
/// ⚠️ 必须与服务端 `entity.StateRedName` **同值**（0x00008000）——
/// 原版是 `SM_CHANGENAMECOLOR` 单独下发名字颜色（`m_nNameColor`，默认白），
/// 我们这条协议只有一个 `status_bits`，就借它传"这个玩家是红名"。
pub const STATE_RED_NAME: u64 = 0x0000_8000;

/// 打一个目标时"这一步"干什么（[`World::combat_step`]）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CombatStep {
    /// 已经在攻击范围（相邻八格）⇒ 出手。
    Attack,
    /// 还不够近 ⇒ **朝这一格走/跑过去**（到了下一帧就变成 [`CombatStep::Attack`]）。
    Approach { x: i32, y: i32, run: bool },
}

/// 动作 id 的值域（与 `protocol.md` §9.5 一致：1..8 与 `AttackAction` 同值）。
pub mod action {
    /// 受击。
    pub const HURT: u32 = 51;
    /// 死亡。
    pub const DEATH: u32 = 52;
    /// 「这个动作是不是攻击」（1..8 都算）。
    pub fn is_attack(a: u32) -> bool {
        (1..=8).contains(&a)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use proto::envelope::Body;

    fn env(body: Body) -> Envelope {
        Envelope {
            seq: 1,
            ack_seq: 0,
            request_id: 0,
            body: Some(body),
        }
    }

    fn state(id: u64, kind: u32, name: &str, x: i32, y: i32, dir: i32) -> proto::EntityState {
        proto::EntityState {
            entity_id: id,
            kind,
            name: name.into(),
            position: Some(proto::Vec2 { x, y }),
            direction: dir,
            ..Default::default()
        }
    }

    fn enter_world() -> Body {
        Body::EnterWorld(proto::EnterWorld {
            self_entity_id: 1,
            self_name: "勇士".into(),
            map_id: 0,
            map_name: "0".into(),
            position: Some(proto::Vec2 { x: 1, y: 1 }),
            direction: proto::Direction::DirRight as i32,
            entities: vec![
                state(1_000_001, 1, "鸡", 3, 2, proto::Direction::DirDown as i32), // 自己？不是
                state(7, 0, "别人", 5, 5, proto::Direction::DirUp as i32),
            ],
            server_tick: 42,
            self_feature: None,
            // 小地图图号：0 号图在 `MiniMap.txt` 里是 101（图库下标 = 100）
            minimap_index: 101,
            map_title: "比奇省".into(),
        })
    }

    /// 用户 2026-10-09：「显示玩家自己时，不要使用"自己"这个词，应显示玩家角色名称」。
    ///
    /// 名字在**选角那一刻**客户端就知道（`CharacterSummary.name`）⇒ 即使
    /// `EnterWorld.self_name` 是空串（老服务端/异常路径），头顶也要画真名；
    /// 而服务端**给了**名字时以它为准（那是权威）。
    #[test]
    fn self_name_keeps_character_name() {
        // ① 选角时先记下名字（`app/src/flow.rs` 在发出 SelectCharacter 前这么调）
        let mut w = World::default();
        w.remember_self_name("勇士");
        // ② 老服务端没填 self_name ⇒ **不许**被空串冲掉
        let mut body = enter_world();
        if let Body::EnterWorld(ew) = &mut body {
            ew.self_name.clear();
        }
        w.apply(&env(body));
        assert_eq!(w.self_name, "勇士", "空 self_name 不该把已知的角色名抹掉");

        // ③ 服务端给了名字 ⇒ 以服务端为准（换角色/重连都走这条）
        let mut w2 = World::default();
        w2.remember_self_name("旧角色");
        w2.apply(&env(enter_world()));
        assert_eq!(w2.self_name, "勇士", "服务端那份才是权威");
    }

    #[test]
    fn enter_world_builds_snapshot() {
        let mut w = World::default();
        assert_eq!(w.apply(&env(enter_world())), Change::World);
        assert!(w.in_world());
        assert_eq!((w.self_id, w.map_name.as_str()), (1, "0"));
        assert_eq!(w.self_pos, (1, 1));
        // 左下角那行的抬头来自服务端下发的地图描述（官方 `ClientGetMapDescription`）
        assert_eq!(w.map_title, "比奇省");
        // 小地图图号要跟着进世界一起到（TAB 小地图靠它取图，见 `World::minimap_index`）
        assert_eq!(w.minimap_index, 101, "进世界该带上小地图图号");
        assert_eq!(w.server_tick, 42);
        assert_eq!(w.entities.len(), 2);
        assert_eq!(w.entities[&1_000_001].name, "鸡");
        // 快照本身不算"实体事件"（它是状态，不是增量）。
        assert_eq!(w.entity_events, 0);
    }

    #[test]
    fn snapshot_drops_self_if_server_ever_sends_it() {
        let mut w = World::default();
        let Body::EnterWorld(mut ew) = enter_world() else {
            unreachable!()
        };
        ew.entities
            .push(state(1, 0, "自己", 1, 1, proto::Direction::DirRight as i32));
        w.apply(&env(Body::EnterWorld(ew)));
        assert!(!w.entities.contains_key(&1), "画面上不该出现两个「自己」");
        assert_eq!(w.entities.len(), 2);
    }

    #[test]
    fn appear_move_disappear() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 出现
        w.apply(&env(Body::EntityAppear(proto::EntityAppear {
            entity: Some(state(
                1_000_002,
                1,
                "鹿",
                4,
                4,
                proto::Direction::DirLeft as i32,
            )),
        })));
        assert_eq!(w.entities.len(), 3);
        assert_eq!(w.entities[&1_000_002].x, 4);

        // 移动（只给 from/to + 朝向）
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 1_000_002,
            from: Some(proto::Vec2 { x: 4, y: 4 }),
            to: Some(proto::Vec2 { x: 5, y: 4 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 1,
            run: false,
        })));
        let e = &w.entities[&1_000_002];
        assert_eq!((e.x, e.y, e.dir), (5, 4, proto::Direction::DirRight as i32));
        // 名字/类别不能被移动消息抹掉（它只带位置）
        assert_eq!((e.name.as_str(), e.kind), ("鹿", 1));

        assert_eq!(w.entity_events, 2, "出现 + 移动 = 2 条实体事件");

        // 消失
        w.apply(&env(Body::EntityDisappear(proto::EntityDisappear {
            entity_id: 1_000_002,
            reason: proto::DisappearReason::DisappearLeftView as i32,
        })));
        assert!(!w.entities.contains_key(&1_000_002));
        assert_eq!(w.entity_events, 3);
    }

    #[test]
    fn self_move_updates_self_only() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 1,
            from: Some(proto::Vec2 { x: 1, y: 1 }),
            to: Some(proto::Vec2 { x: 2, y: 1 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 2,
            run: true,
        })));
        assert_eq!(w.self_pos, (2, 1));
        assert_eq!(w.entities.len(), 2, "自己的移动不该往实体表里塞东西");
        // "这一步是跑的"也要落到自己身上（渲染层靠它播 ActRun）
        assert!(w.self_run);
        // ... 而**不是**塞进实体表（自己不在表里）
        assert_eq!(w.self_action, None);
    }

    /// `run` 是**这一步**的属性：别人跑步、原地转身，两种都要传对。
    ///
    /// ⚠️ 用户 2026-10-08 报的"跑起来不像跑"根因就在这里：`EntityMove` 原先没有这个字段，
    /// 客户端只能一律播 `ActWalk`（而两段图差 64，看着就是另一套动作）。
    #[test]
    fn 移动的跑标记来自服务端() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        // 别人（快照里的 7 号）跑一步
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 7,
            from: Some(proto::Vec2 { x: 5, y: 5 }),
            to: Some(proto::Vec2 { x: 7, y: 5 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 3,
            run: true,
        })));
        let e = &w.entities[&7];
        assert_eq!((e.x, e.y, e.run), (7, 5, true), "跑一步该带上 run");

        // 同一实体改成走一步 ⇒ 标记要跟着变（它是"这一步"的属性，不是"这个人"的属性）
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 7,
            from: Some(proto::Vec2 { x: 7, y: 5 }),
            to: Some(proto::Vec2 { x: 8, y: 5 }),
            direction: proto::Direction::DirRight as i32,
            server_tick: 4,
            run: false,
        })));
        let e = &w.entities[&7];
        assert_eq!((e.x, e.run), (8, false), "走一步必须把 run 抹掉");
    }

    #[test]
    fn move_of_unknown_entity_is_counted_not_invented() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::EntityMove(proto::EntityMove {
            entity_id: 999,
            from: Some(proto::Vec2 { x: 0, y: 0 }),
            to: Some(proto::Vec2 { x: 1, y: 0 }),
            direction: 0,
            server_tick: 1,
            run: false,
        })));
        assert!(
            !w.entities.contains_key(&999),
            "不该凭空造一个没有名字的实体"
        );
        assert_eq!(w.unknown, 1, "要记数（这通常是服务端的账本有漏）");
    }

    #[test]
    fn unknown_and_control_messages_do_not_touch_world() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        let before = w.clone();
        assert_eq!(w.apply(&Envelope::default()), Change::None); // oneof 未设置
        assert_eq!(
            w.apply(&env(Body::Ping(proto::Ping { client_time_ms: 1 }))),
            Change::None
        );
        assert_eq!(
            w.apply(&env(Body::Raw(proto::Raw {
                msg_id: 1,
                body: vec![]
            }))),
            Change::None
        );
        assert_eq!(w.unknown, 1);
        assert_eq!(w.entities, before.entities);
    }

    #[test]
    fn ability_update() {
        let mut w = World::default();
        w.apply(&env(Body::AbilityUpdate(proto::AbilityUpdate {
            ability: Some(proto::Ability {
                level: 7,
                hp: 30,
                max_hp: 40,
                mp: 5,
                max_mp: 9,
                gold: 123,
                ..Default::default()
            }),
        })));
        let a = w.ability.expect("能力值");
        assert_eq!(
            (a.level, a.hp, a.max_hp, a.mp, a.max_mp, a.gold),
            (7, 30, 40, 5, 9, 123)
        );
    }

    /// 自己的外观由 `self_feature` **单独**给（快照里不含自己）—— 客户端画自己要用它。
    #[test]
    fn self_feature_comes_from_snapshot() {
        let mut w = World::default();
        let Body::EnterWorld(mut ew) = enter_world() else {
            panic!("enter_world() 应给出 EnterWorld");
        };
        ew.self_feature = Some(proto::EntityFeature {
            dress: 12,
            weapon: 3,
            ..Default::default()
        });
        w.apply(&env(Body::EnterWorld(ew)));
        let f = w.self_feature.as_ref().expect("进图应带上自己的外观");
        assert_eq!((f.dress, f.weapon), (12, 3));

        // 换图会重发一次（装备/性别显示都可能变了）
        w.apply(&env(Body::ChangeMap(proto::ChangeMap {
            map_name: "1".into(),
            self_feature: Some(proto::EntityFeature {
                dress: 14,
                ..Default::default()
            }),
            ..Default::default()
        })));
        assert_eq!(w.self_feature.unwrap().dress, 14);
    }

    #[test]
    fn combat_damage_health_death() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 伤害：记进待取事件，**取走之后要清空**（否则每帧重复弹字）
        w.apply(&env(Body::Damage(proto::Damage {
            attacker_id: 1,
            target_id: 1_000_001,
            value: 7,
            flags: 0,
        })));
        let ev = w.take_damage();
        assert_eq!(ev.len(), 1);
        assert_eq!(
            (ev[0].attacker_id, ev[0].target_id, ev[0].value),
            (1, 1_000_001, 7)
        );
        assert!(w.take_damage().is_empty(), "take_damage 必须把队列清空");
        assert_eq!(w.damage_events, 1);

        // 血量
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 1_000_001,
            hp: 8,
            max_hp: 15,
        })));
        assert_eq!(
            (w.entities[&1_000_001].hp, w.entities[&1_000_001].max_hp),
            (8, 15)
        );

        // 死亡：**只标记、不删**（尸骨要留一会儿，真移除是随后的 EntityDisappear）
        w.apply(&env(Body::Death(proto::Death {
            entity_id: 1_000_001,
            killer_id: 1,
        })));
        assert!(w.entities[&1_000_001].dead);
        assert_eq!(w.entities[&1_000_001].action, Some(action::DEATH));
        assert_eq!(w.entities.len(), 2, "死亡不等于消失");

        // 消失才是移除
        w.apply(&env(Body::EntityDisappear(proto::EntityDisappear {
            entity_id: 1_000_001,
            reason: proto::DisappearReason::DisappearDead as i32,
        })));
        assert_eq!(w.entities.len(), 1);
    }

    #[test]
    fn self_health_and_action_are_tracked_separately() {
        let mut w = World::default();
        w.apply(&env(enter_world()));

        // 自己的血量单独存：不能用 EntityHealth 去凑一个"半个 ability"（会显示 0 级）
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 1,
            hp: 30,
            max_hp: 40,
        })));
        assert_eq!(w.self_hp, Some((30, 40)));
        assert!(w.ability.is_none(), "别为了 hp 造一个等级=0 的能力值");

        // 能力值到达时两边都要更新
        w.apply(&env(Body::AbilityUpdate(proto::AbilityUpdate {
            ability: Some(proto::Ability {
                level: 7,
                hp: 25,
                max_hp: 40,
                ..Default::default()
            }),
        })));
        assert_eq!(w.self_hp, Some((25, 40)));
        assert_eq!(w.ability.unwrap().level, 7);

        // 自己的动作（挥砍）：自己不在 entities 里 ⇒ 单列
        w.apply(&env(Body::EntityAction(proto::EntityAction {
            entity_id: 1,
            action: 1,
            server_tick: 9,
        })));
        assert_eq!(w.self_action, Some(1));
        assert_eq!(w.actions, 1);
    }

    /// 用户 2026-10-09 报的"攻击只有第 1 下有挥砍动作、后续掉血但没动作"：
    ///
    /// 服务端**每次出手**都发一条 `EntityAction`，但普通攻击的 action 值**恒为 1**
    /// ⇒ 渲染层若按"值变没变"判重播，第二次以后的每一刀都会被判成"没变"而漏掉。
    /// 这里锁住 `action_seq`：**值不变也要 +1**（判据是消息，不是值）。
    #[test]
    fn repeated_same_action_still_bumps_seq() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        let act = |id: u64| {
            env(Body::EntityAction(proto::EntityAction {
                entity_id: id,
                action: 1,
                server_tick: 1,
            }))
        };
        // 自己：连砍三刀（值都是 1）
        for _ in 0..3 {
            w.apply(&act(1));
        }
        assert_eq!(w.self_action, Some(1));
        assert_eq!(w.self_action_seq, 3, "自己：每一条 EntityAction 都要计数");
        // 别人（快照里的 `鸡`）：同样按消息计数
        for _ in 0..2 {
            w.apply(&act(1_000_001));
        }
        assert_eq!(w.entities[&1_000_001].action_seq, 2);
        assert_eq!(w.entities[&1_000_001].action, Some(1));
    }

    #[test]
    fn level_up_carries_full_ability() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::LevelUp(proto::LevelUp {
            level: 8,
            ability: Some(proto::Ability {
                level: 8,
                hp: 50,
                max_hp: 60,
                mp: 20,
                max_mp: 30,
                gold: 999,
                ..Default::default()
            }),
        })));
        let ab = w.ability.expect("升级要带完整能力值");
        assert_eq!((ab.level, ab.max_hp, ab.max_mp, ab.gold), (8, 60, 30, 999));
        assert_eq!(w.self_hp, Some((50, 60)));
    }

    #[test]
    fn health_action_of_unknown_entity_are_counted() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        let before = w.clone();
        w.apply(&env(Body::EntityHealth(proto::EntityHealth {
            entity_id: 999,
            hp: 1,
            max_hp: 1,
        })));
        w.apply(&env(Body::EntityAction(proto::EntityAction {
            entity_id: 999,
            action: 1,
            server_tick: 1,
        })));
        w.apply(&env(Body::Death(proto::Death {
            entity_id: 999,
            killer_id: 0,
        })));
        assert_eq!(w.unknown, 3, "没见过的实体不该被凭空造出来，但要记数");
        assert_eq!(w.entities.len(), before.entities.len());
    }

    #[test]
    fn action_id_space() {
        // 1..8 = 攻击（与 AttackAction 同值），51/52 是非攻击 —— 见 protocol.md §9.5
        for a in 1..=8 {
            assert!(action::is_attack(a), "动作 {a} 应判为攻击");
        }
        assert!(!action::is_attack(action::HURT));
        assert!(!action::is_attack(action::DEATH));
        assert_ne!(action::HURT, action::DEATH);
    }

    /// 左键点怪：**只认活着的怪物**（玩家/NPC/尸骨都不算）—— 照原版 `ClMain.pas:2863-2878`。
    #[test]
    fn 点怪只锁活怪物() {
        let mut w = World::default();
        w.apply(&env(enter_world())); // 自己在 (1,1)
        for (id, kind, name, x, y) in [
            (100u64, 1u32, "鹿", 3, 3),
            (200, 0, "别人", 4, 4), // 玩家：要 Shift 才打（PK 那条线，没做）
            (300, 2, "商人", 5, 5), // NPC
        ] {
            w.apply(&env(Body::EntityAppear(proto::EntityAppear {
                entity: Some(state(id, kind, name, x, y, 1)),
            })));
        }
        assert_eq!(w.attack_target_at((3, 3)), Some(100), "怪物 ⇒ 锁它");
        assert_eq!(w.attack_target_at((4, 4)), None, "玩家 ≠ 可攻击目标");
        assert_eq!(w.attack_target_at((5, 5)), None, "NPC ≠ 可攻击目标");
        assert_eq!(w.attack_target_at((9, 9)), None, "空地没有目标");

        // 死了的（尸骨还在 entities 里）也不算
        w.apply(&env(Body::Death(proto::Death {
            entity_id: 100,
            killer_id: 1,
        })));
        assert!(w.entities.contains_key(&100), "尸骨仍然在（要留着画）");
        assert_eq!(w.attack_target_at((3, 3)), None, "死了的不该再被锁");
        assert_eq!(w.combat_step(100), None, "死了 ⇒ 目标作废");
    }

    /// 打目标这一步该"出手"还是"凑近"：相邻八格出手；否则朝**目标旁边**那格走/跑。
    #[test]
    fn 打目标先凑近再出手() {
        let mut w = World::default();
        w.apply(&env(enter_world()));
        w.apply(&env(Body::EntityAppear(proto::EntityAppear {
            entity: Some(state(100, 1, "鹿", 5, 5, 1)),
        })));
        // 自己在 (1,1)：距离 4 ⇒ 跑过去，落在**鹿旁边靠我这一侧**那一格 (4,4)
        assert_eq!(
            w.combat_step(100),
            Some(CombatStep::Approach {
                x: 4,
                y: 4,
                run: true
            })
        );

        // 距离 2（原版：≤2 就走着过去）
        w.self_pos = (3, 3);
        assert_eq!(
            w.combat_step(100),
            Some(CombatStep::Approach {
                x: 4,
                y: 4,
                run: false
            })
        );

        // 斜向相邻（八格之内）⇒ 出手
        w.self_pos = (4, 4);
        assert_eq!(w.combat_step(100), Some(CombatStep::Attack));
        w.self_pos = (6, 6);
        assert_eq!(
            w.combat_step(100),
            Some(CombatStep::Attack),
            "斜着贴上去也算够近"
        );

        // 目标不在视野里 ⇒ 作废（调用方据此清掉锁）
        assert_eq!(w.combat_step(999), None);
    }

    /// **移动被拒**（`MoveRejected`）：采用服务端给的权威位置 + 记数 + 留下原因。
    ///
    /// ⚠️ 新协议这条是原版 `SM_MOVEFAIL` 的对应物，原版收到就 `ActionFailed`
    ///（清走法目标 + 锁 1 秒，`ClMain.pas:4005-4012 / 4639-4648`）—— 调用方拿这里的
    /// 计数**变没变**来判断"刚刚被拒了一次"。少了它，撞墙时会一直朝墙每 650ms 发一次。
    #[test]
    fn 移动被拒采用权威位置并记数() {
        let mut w = World::default();
        w.apply(&env(enter_world())); // 自己在 (1,1)
        assert_eq!(w.move_fail, 0);
        assert_eq!(w.self_pos, (1, 1));

        w.apply(&env(Body::MoveRejected(proto::MoveRejected {
            authoritative_position: Some(proto::Vec2 { x: 7, y: 9 }),
            reason: 3, // 阻挡
        })));
        assert_eq!(w.move_fail, 1, "被拒要记数");
        assert_eq!(w.move_fail_reason, 3, "原因要留着（3 = 阻挡）");
        assert_eq!(w.self_pos, (7, 9), "位置以服务端给的权威位置为准");

        // 再拒一次：计数继续涨（调用方每帧比对"变没变"）
        w.apply(&env(Body::MoveRejected(proto::MoveRejected {
            authoritative_position: Some(proto::Vec2 { x: 7, y: 9 }),
            reason: 1, // 超速
        })));
        assert_eq!(w.move_fail, 2);
        assert_eq!(w.move_fail_reason, 1);
    }
}

#[cfg(test)]
mod bag_tests {
    use super::*;

    fn env(body: Body) -> Envelope {
        Envelope {
            seq: 1,
            ack_seq: 0,
            request_id: 0,
            body: Some(body),
        }
    }

    fn stack(index: u32, name: &str, looks: u32, count: u32) -> proto::ItemStack {
        proto::ItemStack {
            index,
            name: name.into(),
            looks,
            count,
            dura: count,
            dura_max: 1,
            ..Default::default()
        }
    }

    /// 背包与已穿戴的解析（D-65）。
    ///
    /// ⚠️ 这条链原来是**两端都齐、服务端却从不构造**：客户端 core 里没有背包字段、
    /// 没人在 `match body` 里接 `BagItems` ⇒ 玩家永远看不到包裹。服务端补齐后，
    /// 这里把"收到之后世界变成什么样"钉住。
    #[test]
    fn 背包与已穿戴的解析() {
        let mut w = World::default();

        // 全量背包：两件药 + 名字为空的空槽（服务端按槽位发，空槽也要占位）
        w.apply(&env(Body::BagItems(proto::BagItems {
            items: vec![
                stack(1, "金创药(小量)", 398, 3),
                stack(2, "魔法药(小量)", 394, 1),
                proto::ItemStack::default(),
            ],
        })));
        assert_eq!(w.bag.len(), 3, "空槽也要占位（槽位不能整体前移）");
        let first = w.bag[0].as_ref().expect("第 1 格应是金创药");
        assert_eq!(
            (first.name.as_str(), first.looks, first.count),
            ("金创药(小量)", 398, 3),
            "图标靠 looks 取 `Items.wzl[looks]`，数量在 count"
        );
        assert!(w.bag[2].is_none(), "index=0 的槽是空的");
        assert_eq!(w.bag[1].as_ref().unwrap().count, 1);

        // 已穿戴：第 0 槽空、第 1 槽有东西 —— 位置本身有意义，不能压缩
        w.apply(&env(Body::EquippedItems(proto::EquippedItems {
            items: vec![proto::ItemStack::default(), stack(9, "铁剑", 100, 1)],
        })));
        assert_eq!(w.equip.len(), 2);
        assert!(w.equip[0].is_none(), "第 0 槽（武器？）空着");
        assert_eq!(w.equip[1].as_ref().unwrap().name, "铁剑");

        // 单件增删改：服务端现在发全量，但增量那条路也照实现（将来做拖放优化不用改客户端）
        w.apply(&env(Body::AddItem(proto::AddItem {
            slot: 5,
            item: Some(stack(3, "回城卷", 402, 6)),
        })));
        assert_eq!(w.bag.len(), 6, "中间空出来的槽要补齐");
        assert_eq!(w.bag[5].as_ref().unwrap().name, "回城卷");

        w.apply(&env(Body::UpdateItem(proto::UpdateItem {
            slot: 5,
            item: Some(stack(3, "回城卷", 402, 5)),
        })));
        assert_eq!(w.bag[5].as_ref().unwrap().count, 5, "数量被改成 5");

        w.apply(&env(Body::RemoveItem(proto::RemoveItem { slot: 5 })));
        assert!(w.bag[5].is_none(), "删掉之后该格为空");
    }
}

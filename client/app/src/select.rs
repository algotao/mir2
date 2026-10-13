//! **选角场景**（原版 `IntroScn.pas` 的 `TSelectChrScene`）。
//!
//! 版式、图号公式与动画状态机都在 `mir2_core::select_ui`（纯计算、可单测）；
//! 这里只做"把状态画出来 + 把输入翻成动作"。
//!
//! # ⚠️ 坐标不是照抄原版的（见 D-27）
//!
//! 原版的 `FState.pas` 坐标对应它自己那套 `Prguse.wil`；我方素材是另一版式
//! （面板间距 552 而非 340、菜单行距 20 而非 38）⇒ 照抄会让点击区、按下态、
//! 小人、文字**全部错位**（实测踩过）。所以布局全部是**量出来的**，
//! 见 `core::select_ui` 的表与 `真素材_按钮与背景对齐` 那条测试。
//!
//! # 与原版的几处差异（都是有意的）
//!
//! 1. **补了键盘**：原版选角只认鼠标。这里 ←→ 选槽、↑↓ 选菜单项、回车确认
//!    （布局是"左右两槽 + 中间竖排菜单"，键盘正好这么走）。
//! 2. **[创建人物] / [删除人物] 已接上**（2026-10-08，D-35）：建角开一个对话框
//!    （姓名/职业/性别，键鼠都能用），删角弹"删了不可恢复"的确认。**[制作群] 仍是占位**。
//!    ⚠️ 对话框是**自绘的固定几何**（`su::DialogBox`），没照抄原版的窗口素材图号
//!    （原版 `Prguse[73..78]`，我方素材不是那一套，见 protocol.md §11 与 D-27）。
//! 3. **空槽点了没反应**（原版会选中它、然后[开始]发一个空名字被服务端拒掉）。
//! 4. 压暗过渡略去（理由见 `core::select_ui::SlotAnim`）。

use std::time::Instant;

use mir2_core::entrance::NameRule;
use mir2_core::select_ui as su;
use mir2_protocol as proto;

#[path = "select_view.rs"]
pub(crate) mod view;

/// 界面上一个角色需要的全部信息。
#[derive(Debug, Clone, PartialEq)]
pub struct CharEntry {
    pub id: u64,
    pub name: String,
    pub level: u32,
    /// 新协议的 `CharClass`（1/2/3）。`class_name` 直接吃它。
    pub class: i32,
    /// 原版的 `Sex`（0 男 / 1 女）—— 挑小人图要用。
    pub sex: u8,
    /// **选角槽位**（服务端按"含已删除的创建次序"编号，删人后不重排）。
    ///
    /// ⚠️ 槽位**不等于** `chars` 里的下标：删掉 1 号之后 2 号还待在 2 号位，
    /// 中间那个位置空着（用户 2026-10-10 第 1 条：删了角色1，角色2 不该左移）。
    pub slot: u32,
}

impl CharEntry {
    pub fn from_summary(c: &proto::CharacterSummary) -> Self {
        CharEntry {
            id: c.character_id,
            name: c.name.clone(),
            level: c.level,
            class: c.class,
            // 协议里 1=男 2=女；0（未指定）与脏值都按男画
            sex: match proto::Gender::try_from(c.gender) {
                Ok(proto::Gender::Female) => 1,
                _ => 0,
            },
            slot: c.slot,
        }
    }

    /// 原版的 `Job`（0/1/2）：协议是 1/2/3。
    fn job(&self) -> u8 {
        (self.class - 1).clamp(0, 2) as u8
    }
}

/// 场景里做出来的动作（交给主循环去发消息/退出）。
///
/// ⚠️ **不派生 `Copy`**：[`Action::Create`] 带一个 `String`（角色名）。`PartialEq` 留着 ——
/// 渲染那条路要靠它跟 [`Action::None`] 比。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Action {
    None,
    /// 进游戏（带上选中的角色 id）。
    Enter(u64),
    /// **新建角色**（对话框点了[确定]，且本地校验已过）。
    ///
    /// `class`/`gender` 已经是**协议值**（1/2/3 与 1/2）—— 界面里那套 0/1/2 的索引在
    /// `NewChar::confirm` 里就换掉了，免得两套编号在下游来回漂。
    Create {
        name: String,
        class: i32,
        gender: i32,
        hair: u32,
    },
    /// **删除角色**（**已经过一次确认**；二次确认的口令证明由状态机自己带，
    /// 见 `Entrance::delete_character`）。
    Delete(u64),
    /// 关掉窗口。
    Exit,
}

/// "按下的东西"（按下才画叠加图 —— 原版 `Downed` 的语义）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Hit {
    /// 某一块面板的「选择」按钮（= 选这个槽）。
    Sel(usize),
    /// **点角色本体**（凹槽那一块）也算选中这个槽。
    ///
    /// ⚠️ 原版只认下方那颗「选择」钮（`TSelectChrScene` 没有自定义 `OnInRealArea`），
    /// 这是用户 2026-10-09 要的增强："应支持鼠标点击角色选择，而不仅是下方的选择按钮"。
    /// 命中范围见 `su::slot_at`。
    Slot(usize),
    /// 中间菜单的第 i 项（顺序同 `su::MENU`）。
    Menu(usize),
    /// 弹窗那颗 [Ok]（`Prguse[363]`）。
    ///
    /// ⚠️ 单独一项是为了守住"按下与抬起在同一颗"：弹窗里那颗会**真的删角色**
    ///（不可恢复）⇒ 只认一次干净的点击，不接受"从别处拖到按钮上松手"。
    MsgOk,
}

/// 选角场景的状态。
pub struct Select {
    pub chars: Vec<CharEntry>,
    /// 当前选中的槽（0/1）。初始不选（原版进场景时两个都没选中）。
    picked: usize,
    anims: [su::SlotAnim; su::Art::SLOTS],
    /// 键盘光标在菜单第几项。
    menu_cursor: usize,
    /// 弹窗文本（原版 `DMessageDlg`）。`Some` 时挡住其它点击。
    pub msg: Option<String>,
    /// 建角对话框（原版 `DCreateChr`）。`Some` 时它是**模态**：吃掉所有输入。
    newchar: Option<NewChar>,
    /// **角色名规则**（服务端下发的那份；`None` = 还没收到 ⇒ 用内置兜底）
    name_rule: Option<NameRule>,
    /// "要确认的动作"（原版 `DMessageDlg` 的 Yes/No）：回车/[确定]才执行，其余输入 = 取消。
    /// 目前只有删角用它 —— 删除不可恢复，宁可要求一个明确的"是"。
    pending: Option<Action>,
    /// 每个槽上一次看到的相位 —— 用来在**解冻那一刻**响一声 `101`
    ///（原版是"点了某个槽"就响一次：`IntroScn.pas:1170/1187` 的 `PlaySound(s_meltstone)`）。
    last_phase: [su::SlotPhase; su::Art::SLOTS],
    /// 小人 / 光效的**落点备忘**（见 `su::SlotPlace`）：基准是"站立第 0 帧"，
    /// 石化帧再叠原版的手调量 —— 逐帧按包围盒对齐会让"已经站定的脚"随上半身摆动左右挪，
    /// 而拿石化图自己的包围盒去居中又会差 0.5~3.5px（用户两轮都报过）。
    last: Instant,
}

impl Select {
    pub fn new(chars: Vec<CharEntry>) -> Self {
        // 第 0 个槽 = **默认选中**（站立、播动画）；其余**直接石化**。
        // ⚠️ 原版进场景时两个都站着（`OpenScene` 只开窗口放 BGM），于是看不出
        // 当前选的是谁 —— 那时点[开始]还会被拒（两边 `Selected` 都是 FALSE）。
        // 我们有意改成"默认选中第一个"，一眼能看出当前是哪个。
        let first_slot = chars.iter().map(|c| c.slot as usize).min().unwrap_or(0);
        let anims = std::array::from_fn(|i| {
            // ⚠️ 按**槽位**取，不是按下标：删过人之后两者不再一致
            let (job, sex) = chars
                .iter()
                .find(|c| c.slot as usize == i)
                .map_or((0, 0), |c| (c.job(), c.sex));
            if i == first_slot {
                su::SlotAnim::new(job, sex)
            } else {
                su::SlotAnim::new_frozen(job, sex)
            }
        });
        // 初值就是"现在这两个槽的相位"（第 0 个站立、其余石化）—— 不能写死成
        // `Freeze`，否则进场景那一帧会被当成"刚从石化里化出来"、白响一声。
        let last_phase = std::array::from_fn(|i| anims[i].phase());
        Self {
            chars,
            picked: first_slot,
            anims,
            menu_cursor: 0,
            msg: None,
            newchar: None,
            name_rule: None,
            pending: None,
            last_phase,
            last: Instant::now(),
        }
    }

    /// 推进选角动画（**更新阶段调用**；绘制只读 `SlotAnim::frame`，不会改变相位）。
    pub(crate) fn tick(&mut self, now: Instant) {
        let dt = now.saturating_duration_since(self.last).as_millis() as u32;
        self.last = now;
        for slot in 0..su::Art::SLOTS {
            let occupied = self.chars.iter().any(|c| c.slot as usize == slot);
            if occupied {
                let _ = self.anims[slot].tick(dt, slot == self.picked);
            }
        }
    }

    /// 当前选中的角色（空槽 ⇒ `None`）。**按槽位找**（见 `CharEntry::slot`）。
    pub fn picked_char(&self) -> Option<&CharEntry> {
        self.char_at_slot(self.picked)
    }

    /// 某个**槽位**上的角色（没有 ⇒ 空位）。
    fn char_at_slot(&self, slot: usize) -> Option<&CharEntry> {
        self.chars.iter().find(|c| c.slot as usize == slot)
    }

    /// 槽位 → `chars` 下标（选中/取数据那些老代码还在用下标）。
    #[allow(dead_code)] // 留着：`pick_slot` 之外的调用点将来要用（见 `char_at_slot`）
    fn idx_of_slot(&self, slot: usize) -> Option<usize> {
        self.chars.iter().position(|c| c.slot as usize == slot)
    }

    /// 取走这一帧该响的音效（原版编号，交给主循环去播）。
    ///
    /// 目前只有**石化解冻**那一声 `101`：原版在"点中某个槽"时就放
    /// （`IntroScn.pas:1170/1187`），我们等效成"相位从别处变成 `Unfreezing`"。
    /// 点按钮的通用声（`103`）由主循环在拿到 [`Action`] 时放 —— 那属于交互，
    /// 不属于这个场景的动画状态。
    pub fn take_sfx(&mut self) -> Vec<u16> {
        let mut out = Vec::new();
        for (i, a) in self.anims.iter().enumerate() {
            let p = a.phase();
            if p != self.last_phase[i] {
                if p == su::SlotPhase::Unfreezing {
                    out.push(mir2_core::sound::idx::MELTSTONE);
                }
                self.last_phase[i] = p;
            }
        }
        out
    }

    /// 弹一条提示（错误/占位说明都走它）。
    pub fn say(&mut self, what: impl Into<String>) {
        self.msg = Some(what.into());
    }

    /// 建角对话框开着吗（测试与冒烟要看它；主循环那条路是 `mode == 4` + `on_text` 自判）。
    #[allow(dead_code)]
    pub fn newchar_open(&self) -> bool {
        self.newchar.is_some()
    }

    /// 文字输入：只有姓名那个框吃（其余焦点忽略）。
    pub fn on_text(&mut self, t: &str) {
        let Some(d) = self.newchar.as_mut() else {
            return;
        };
        if d.focus != 0 {
            return;
        }
        for ch in t.chars() {
            if d.name.chars().count() >= NAME_MAX {
                break; // 原版 `EdChrName.MaxLength := 14`（`IntroScn.pas:1118`）
            }
            if !ch.is_control() {
                d.name.push(ch);
            }
        }
    }

    /// 「新建角色」：原版先看有没有空槽，没有就弹"只能创建两个角色"（`IntroScn.pas:1208-1215`）。
    fn open_newchar(&mut self) -> Action {
        if self.chars.len() >= su::Art::SLOTS {
            self.say(format!(
                "每个账号最多 {} 个角色（原版同）。要建新的，先删掉一个。",
                su::Art::SLOTS
            ));
            return Action::None;
        }
        self.newchar = Some(NewChar::default());
        Action::None
    }

    /// 「删除角色」：得先选中一个角色，再弹确认（措辞照抄原版 `IntroScn.pas:1217-1231`）。
    fn ask_delete(&mut self) -> Action {
        let Some(c) = self.char_at_slot(self.picked) else {
            self.say("先选中一个角色，再删。");
            return Action::None;
        };
        let (name, id) = (c.name.clone(), c.id);
        self.say(format!(
            "\"{name}\" 删除角色是不可以恢复的。\n\
             一段时间内，你将会不可以使用相同的角色名字。\n\
             你真的想删除角色吗?\n\
             （回车 = 删除；其它任意键/点击 = 取消）"
        ));
        self.pending = Some(Action::Delete(id));
        Action::None
    }

    /// 建角对话框的按键（模态，全吃）。
    fn newchar_key(&mut self, k: crate::app_state::SceneKey) -> Action {
        if self.newchar.is_none() {
            return Action::None;
        }
        match k {
            crate::app_state::SceneKey::Escape => {
                self.newchar = None;
                Action::None
            }
            crate::app_state::SceneKey::Enter => self.confirm_newchar(),
            crate::app_state::SceneKey::Tab => {
                let d = self.newchar.as_mut().expect("上面判过");
                d.focus = (d.focus + 1) % 3;
                Action::None
            }
            crate::app_state::SceneKey::Backspace => {
                let d = self.newchar.as_mut().expect("上面判过");
                if d.focus == 0 {
                    d.name.pop();
                }
                Action::None
            }
            crate::app_state::SceneKey::Left | crate::app_state::SceneKey::Right => {
                let dir = if k == crate::app_state::SceneKey::Right {
                    1
                } else {
                    -1
                };
                let d = self.newchar.as_mut().expect("上面判过");
                match d.focus {
                    1 => d.job = (d.job as i32 + dir).rem_euclid(3) as usize,
                    2 => d.sex = (d.sex as i32 + dir).rem_euclid(2) as usize,
                    _ => {}
                }
                Action::None
            }
            _ => Action::None,
        }
    }

    /// 对话框点/键[确定]：本地校验过了才产生 [`Action::Create`]；不过就**留着框**让用户改。
    fn confirm_newchar(&mut self) -> Action {
        let Some(d) = self.newchar.take() else {
            return Action::None;
        };
        match valid_char_name(&d.name, self.name_rule.as_ref()) {
            Ok(name) => Action::Create {
                name,
                // 界面索引 → 协议值（战/法/道 = 1/2/3，男/女 = 1/2）
                class: d.job as i32 + 1,
                gender: d.sex as i32 + 1,
                // 发型：原版那两颗按钮是空实现，确定时 `1 + Random(5)`（`IntroScn.pas:1332`）
                hair: hair_roll(),
            },
            Err(why) => {
                self.say(why);
                self.newchar = Some(d);
                Action::None
            }
        }
    }

    /// 对话框里的鼠标：只认它那几颗（框内空白无动作；框外也吃掉 —— 模态）。
    /// **角色名规则**（服务端下发的那份）—— 建角框的本地预检照它判（D-84）。
    ///
    /// ⚠️ 不在这儿的后果：客户端只能用自己的内置口径，改一处忘一处 ⇒
    /// 玩家在客户端看到"A 不行"、服务端却说"B 不行"。
    pub fn set_name_rule(&mut self, r: NameRule) {
        self.name_rule = Some(r);
    }

    /// 按角色 id 预选（`MIR2_CHAR` 指的那个）。找不到就什么都不做。
    pub fn pick_id(&mut self, id: u64) {
        if let Some(c) = self.chars.iter().find(|c| c.id == id) {
            let slot = c.slot as usize;
            self.pick_slot(slot);
        }
    }

    /// 命中 + **那块的矩形**（给控件引擎：按下记矩形，松开就不用再算一遍）。
    fn hit_rect(&self, p: crate::space::DPt, l: &su::Layout) -> Option<(Hit, su::Rect)> {
        let p = (p.0 .0, p.1 .0);
        // 先判"点角色本体"（凹槽整块）—— 与下面那两类不重叠，顺序无所谓（见 `su::slot_at`）
        if let Some((i, r)) = l
            .slots
            .iter()
            .copied()
            .enumerate()
            .find(|(_, rect)| rect.hit(p))
        {
            return Some((Hit::Slot(i), r));
        }
        for (i, r) in l.sel.iter().enumerate() {
            if r.hit(p) {
                return Some((Hit::Sel(i), *r));
            }
        }
        // ⚠️ 「开始」那颗**石台**（83×40）比钮本身（44×21）大得多 ⇒ 先按台子判一次，
        // 否则"看得见台子、点不中"（用户 2026-10-09 第 1 条里那份观感）。
        let plate = su::start_plate_at(l.menu[0]);
        if plate.hit(p) {
            return Some((Hit::Menu(0), plate));
        }
        for (i, r) in l.menu.iter().enumerate() {
            if r.hit(p) {
                return Some((Hit::Menu(i), *r));
            }
        }
        None
    }

    /// `msg` 同 [`Select::click_select`]：弹窗的几何（`None` = 量不到素材 ⇒ 不认那颗 [Ok]）。
    ///
    /// ⚠️ 2026-10-11 转控件层：原来自己存 `self.down`、抬起时再判一遍 ⇒ 命中算了两遍。
    /// 现在按官方 `TDButton` 的路子：按下把 `(控件, 矩形)` 交给 `widget::Ui`，
    /// 松开由引擎判"是不是同一块" —— 选角也享受同一套 OnClick 规范。
    ///
    /// ⚠️ 建角框与弹窗都是**模态**的 ⇒ 开着的时候只认它们自己的控件。
    #[allow(dead_code)]
    pub fn on_down(
        &mut self,
        p: crate::space::DPt,
        l: &su::Layout,
        msg: Option<su::MsgBox>,
        ui: &mut crate::widget::Ui,
    ) {
        let dialog = su::DialogBox::build((l.win.0 as u32, l.win.1 as u32));
        self.on_down_with_dialog(p, l, msg, dialog, ui);
    }

    /// 场景适配器传入与绘制相同的可编辑建角窗口几何。
    pub fn on_down_with_dialog(
        &mut self,
        p: crate::space::DPt,
        l: &su::Layout,
        msg: Option<su::MsgBox>,
        dialog: su::DialogBox,
        ui: &mut crate::widget::Ui,
    ) {
        let design_xy = (p.0 .0, p.1 .0);
        if self.newchar.is_some() {
            if let Some(dh) = dialog.hit(design_xy) {
                ui.press(
                    crate::Press::NewChar(dh),
                    design_rect(dlg_rect(&dialog, dh)),
                );
            }
            return;
        }
        if self.pending.is_some() || self.msg.is_some() {
            if let Some(m) = msg {
                if m.hit_ok(design_xy) {
                    ui.press(crate::Press::Select(Hit::MsgOk), design_rect(m.ok));
                }
            }
            return;
        }
        if let Some((h, r)) = self.hit_rect(p, l) {
            ui.press(crate::Press::Select(h), design_rect(r));
        }
    }

    /// **点中了**一个选角控件（引擎已判定按下与抬起在同一块）⇒ 执行。
    pub fn click_select(&mut self, hit: Hit, _l: &su::Layout) -> Action {
        if hit == Hit::MsgOk {
            // 删角那条"待确认动作"：点 [Ok] 与回车同义
            self.msg = None;
            return self.pending.take().unwrap_or(Action::None);
        }
        // 鼠标点菜单项时把键盘光标也挪过去（两条输入共用一份状态，免得视觉上打架）
        if let Hit::Menu(i) = hit {
            self.menu_cursor = i;
        }
        self.activate(hit)
    }

    /// **点中了**建角框里的控件（引擎已判定）⇒ 执行。
    pub fn click_newchar(&mut self, hit: su::DialogHit) -> Action {
        match hit {
            su::DialogHit::Ok => self.confirm_newchar(),
            su::DialogHit::Close => {
                self.newchar = None;
                Action::None
            }
            su::DialogHit::Name => {
                if let Some(d) = self.newchar.as_mut() {
                    d.focus = 0;
                }
                Action::None
            }
            su::DialogHit::Job(i) => {
                if let Some(d) = self.newchar.as_mut() {
                    d.job = i;
                    d.focus = 1;
                }
                Action::None
            }
            su::DialogHit::Sex(i) => {
                if let Some(d) = self.newchar.as_mut() {
                    d.sex = i;
                    d.focus = 2;
                }
                Action::None
            }
        }
    }

    /// 点在**没有控件**的地方：弹窗（模态）就此关掉；建角框是模态的 ⇒ 点外面不关它。
    pub fn click_outside(&mut self) -> Action {
        if self.newchar.is_some() {
            return Action::None;
        }
        self.msg = None;
        self.pending = None;
        Action::None
    }

    /// 键盘（**我们的扩展**）。
    pub fn on_key(&mut self, k: crate::app_state::SceneKey) -> Action {
        // ① 建角对话框（模态）：所有按键都归它
        if self.newchar.is_some() {
            return self.newchar_key(k);
        }
        // ② "要确认的动作"（删角）：回车 = 执行，其它键 = 取消
        if let Some(act) = self.pending.take() {
            self.msg = None;
            return if matches!(k, crate::app_state::SceneKey::Enter) {
                act
            } else {
                Action::None
            };
        }
        if self.msg.is_some() {
            self.msg = None; // 弹窗开着：任意键关掉
            return Action::None;
        }
        match k {
            // 左右 = 换槽（布局就是左右两块面板）
            crate::app_state::SceneKey::Left => {
                self.move_pick(-1);
                Action::None
            }
            crate::app_state::SceneKey::Right => {
                self.move_pick(1);
                Action::None
            }
            // 上下 = 换菜单项（布局就是中间竖排）
            crate::app_state::SceneKey::Up => {
                self.menu_cursor = (self.menu_cursor + su::MENU.len() - 1) % su::MENU.len();
                Action::None
            }
            crate::app_state::SceneKey::Down => {
                self.menu_cursor = (self.menu_cursor + 1) % su::MENU.len();
                Action::None
            }
            crate::app_state::SceneKey::Enter | crate::app_state::SceneKey::Space => {
                self.activate(Hit::Menu(self.menu_cursor))
            }
            _ => Action::None,
        }
    }

    /// 真正做一件事（鼠标/键盘共用）。
    fn activate(&mut self, hit: Hit) -> Action {
        match hit {
            // 点「选择」钮与点角色本体是**同一件事**：选这个槽
            Hit::Sel(i) | Hit::Slot(i) => {
                self.pick_slot(i);
                Action::None
            }
            Hit::Menu(i) => match su::MENU[i] {
                su::Menu::Start => self.start(),
                su::Menu::CreateChar => self.open_newchar(),
                su::Menu::DeleteChar => self.ask_delete(),
                su::Menu::Credits => {
                    self.say("这套素材这里写的是「制作群」（原版那个位置是 credits）。未接。");
                    Action::None
                }
                su::Menu::Exit => Action::Exit,
            },
            // 弹窗那颗 [Ok] 由 `on_up` 的弹窗分支自己处理（模态），走不到这里
            Hit::MsgOk => Action::None,
        }
    }

    /// 换槽（跳过空槽 —— 空槽进不了游戏）。
    fn move_pick(&mut self, dir: i32) {
        if self.chars.is_empty() {
            return;
        }
        for step in 1..=su::Art::SLOTS as i32 {
            let i = (self.picked as i32 + dir * step).rem_euclid(su::Art::SLOTS as i32) as usize;
            if self.char_at_slot(i).is_some() {
                self.pick_slot(i);
                return;
            }
        }
    }

    /// 选中**槽位** `i`（不是 `chars` 下标 —— 见 `CharEntry::slot`）。
    fn pick_slot(&mut self, i: usize) {
        if self.char_at_slot(i).is_none() {
            return; // 空槽：点了没反应（见文件头第 3 条）
        }
        if self.picked == i && self.anims[i].phase() != su::SlotPhase::Freeze {
            return; // 已经是它了（免得反复重播解冻）
        }
        self.picked = i;
        self.anims[i].select();
        for j in 0..su::Art::SLOTS {
            if j != i {
                self.anims[j].deselect();
            }
        }
    }

    fn start(&mut self) -> Action {
        // ⚠️ 先取出 id 再改 `self`（否则不可变借会拖进分支里）
        let picked = self.picked_char().map(|c| c.id);
        match picked {
            Some(id) => Action::Enter(id),
            None => {
                // 原版那句提示（`IntroScn.pas:1198-1205`）+ 指路（现在真能建了）
                self.say(
                    "一开始你应该创建一个新角色：在中间的菜单里选<新建角色>，\
                     填个名字、挑个职业就行。",
                );
                Action::None
            }
        }
    }
}

/// 建角对话框的内容（原版 `DCreateChr`，`IntroScn.pas:1268-1288`）。
///
/// 原版四个字段：姓名（`EdChrName`，`MaxLength = 14`）、职业（战/法/道）、性别（男/女）、
/// 发型（那两颗按钮在原版里是**空实现**：确定时 `shair := 1 + Random(5)`，
/// `IntroScn.pas:1332`）⇒ 我们照抄这套取舍：发型不进界面，提交时随机。
#[derive(Debug, Clone, Default, PartialEq)]
struct NewChar {
    name: String,
    /// 焦点：0 = 姓名（唯一的输入框），1 = 职业，2 = 性别。
    focus: usize,
    /// 职业索引 0/1/2（原版 `job`）。
    job: usize,
    /// 性别索引 0 男 / 1 女（原版 `sex`）。
    sex: usize,
}

/// 角色名长度上限（原版 `EdChrName.MaxLength := 14`，`IntroScn.pas:1118`）。
const NAME_MAX: usize = 14;

/// 角色名的**本地校验** —— 规则与措辞**都来自服务端**（`NameRule`，D-84）。
///
/// 为什么要本地先判：省一次往返、错误立刻可见。**服务端那道门照样在**（它才是权威），
/// 这里只是把**同一份规则**提前说一遍 —— 措辞也用服务端给的，玩家不会看到两种说法。
///
/// ⚠️ 服务端没下发规则（老服务端 / 还没收到列表）⇒ 退回到内置的同一套口径，
/// 宁可"可能说得不一样"，也不能把人卡住不让他建角。
fn valid_char_name(raw: &str, rule: Option<&NameRule>) -> Result<String, String> {
    let fallback = NameRule::builtin();
    let r = rule.unwrap_or(&fallback);
    let name = raw.trim();
    let has_bad = name.chars().any(|c| r.bad_chars.contains(&c));
    if name.len() < r.min_bytes || has_bad {
        return Err(r.message.clone());
    }
    Ok(name.to_string())
}

/// 发型：原版客户端在确定时取 `1 + Random(5)`（`IntroScn.pas:1332`），服务端不校验。
///
/// 为这一处不引 `rand`（不值一个依赖），用**系统时间的纳秒位**取 1..=5 ——
/// 发型纯外观，不需要密码学随机。
fn hair_roll() -> u32 {
    let n = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.subsec_nanos())
        .unwrap_or(0);
    1 + n % 5
}

/// 按**字符数**折行（中文按一个字算）。
fn wrap(s: &str, cols: usize) -> Vec<String> {
    let mut out = Vec::new();
    let mut cur = String::new();
    let mut n = 0;
    for ch in s.chars() {
        if ch == '\n' {
            out.push(std::mem::take(&mut cur));
            n = 0;
            continue;
        }
        if n >= cols {
            out.push(std::mem::take(&mut cur));
            n = 0;
        }
        cur.push(ch);
        n += 1;
    }
    if !cur.is_empty() {
        out.push(cur);
    }
    out
}

/// 建角框里某个控件对应的**设计空间**矩形。
fn dlg_rect(b: &su::DialogBox, h: su::DialogHit) -> su::Rect {
    match h {
        su::DialogHit::Name => b.name,
        su::DialogHit::Job(i) => b.job[i],
        su::DialogHit::Sex(i) => b.sex[i],
        su::DialogHit::Ok => b.ok,
        su::DialogHit::Close => b.close,
    }
}

/// 设计空间矩形进入全局 widget 引擎前，显式转换到画布空间。
fn design_rect(r: su::Rect) -> crate::widget::Rect {
    crate::widget::design_rect((
        crate::space::Dpx(r.x),
        crate::space::Dpx(r.y),
        crate::space::Dpx(r.w),
        crate::space::Dpx(r.h),
    ))
}

#[cfg(test)]
mod tests {
    /// 走一遍**真实的**按下 → 抬起（含控件引擎的 OnClick 判定）
    /// ⇒ 拖出去松开不算点中，跟游戏里那条路是同一套。
    fn tap(
        s: &mut Select,
        l: &su::Layout,
        msg: Option<su::MsgBox>,
        down_at: (f32, f32),
        up_at: (f32, f32),
    ) -> Action {
        let down_design = crate::space::DPt::from_xy(down_at.0, down_at.1);
        let up_design = crate::space::DPt::from_xy(up_at.0, up_at.1);
        let mut ui = crate::widget::Ui::new();
        ui.set_mouse(crate::ui::ui_pt(down_design));
        s.on_down(down_design, l, msg, &mut ui);
        match ui.release(crate::ui::ui_pt(up_design)) {
            Some(crate::Press::Select(h)) => s.click_select(h, l),
            Some(crate::Press::NewChar(dh)) => s.click_newchar(dh),
            _ => s.click_outside(),
        }
    }

    use super::*;

    /// ⚠️ 槽位**单独给**（默认取 id 当槽位）：真实下发里它是服务端按"含已删除的
    /// 创建次序"编的号，跟 id 无关 —— 单测要能造出"删掉 1 号、2 号还待在 2 号位"
    /// 这种列表（见 `删掉前面的角色后面的人不左移`）。
    fn summary(id: u64, name: &str, class: i32, gender: proto::Gender) -> proto::CharacterSummary {
        summary_slot(id, name, class, gender, id as u32)
    }

    fn summary_slot(
        id: u64,
        name: &str,
        class: i32,
        gender: proto::Gender,
        slot: u32,
    ) -> proto::CharacterSummary {
        proto::CharacterSummary {
            character_id: id,
            name: name.into(),
            class,
            gender: gender as i32,
            level: 7,
            slot,
            ..Default::default()
        }
    }

    /// 摘要 → 界面条目：**职业与性别要翻对**（它们决定挑哪张小人）。
    #[test]
    fn 摘要折算() {
        let c = CharEntry::from_summary(&summary(11, "勇士", 1, proto::Gender::Male));
        assert_eq!((c.id, c.job(), c.sex, c.level), (11, 0, 0, 7));
        assert_eq!(su::class_name(c.class), "战士");
        let c = CharEntry::from_summary(&summary(12, "小法", 2, proto::Gender::Female));
        assert_eq!((c.job(), c.sex), (1, 1));
        let c = CharEntry::from_summary(&summary(13, "无名", 3, proto::Gender::Unspecified));
        assert_eq!((c.job(), c.sex), (2, 0), "道士 + 默认性别");
    }

    /// 键盘：←→ 换槽（跳过空槽）、↑↓ 换菜单、回车确认光标那一项。
    #[test]
    fn 键盘走位() {
        let two = || {
            vec![
                // 槽位 0 / 1（真实下发里槽位由服务端编，跟 id 无关 ⇒ 这里显式给）
                c_slot(11, "甲", 0),
                c_slot(22, "乙", 1),
            ]
        };
        let mut s = Select::new(two());
        s.on_key(crate::app_state::SceneKey::Right);
        assert_eq!(s.picked_char().map(|c| c.id), Some(22));
        s.on_key(crate::app_state::SceneKey::Left);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));

        // 菜单光标：默认第 0 项（开始），↓ 走一项、↑ 转回最后一项
        assert_eq!(s.menu_cursor, 0);
        s.on_key(crate::app_state::SceneKey::Down);
        assert_eq!(s.menu_cursor, 1);
        s.on_key(crate::app_state::SceneKey::Up);
        assert_eq!(s.menu_cursor, 0);
        s.on_key(crate::app_state::SceneKey::Up);
        assert_eq!(s.menu_cursor, su::MENU.len() - 1);

        // 只有一个角色时，左右都停在它身上（空槽跳过）
        let mut s = Select::new(vec![c_slot(11, "甲", 0)]);
        s.on_key(crate::app_state::SceneKey::Right);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));
    }

    /// 菜单项：开始 ⇒ `Enter`；其余是**诚实的占位**（说清楚现状与替代做法）。
    #[test]
    fn 菜单项行为() {
        let mut s = Select::new(vec![c_slot(11, "甲", 0)]);
        // 第 0 项 = 开始
        assert_eq!(
            s.on_key(crate::app_state::SceneKey::Enter),
            Action::Enter(11)
        );

        // 第 1 项 = 新建角色：开建角对话框（原版 `DCreateChr`），不再有那套占位提示
        s.on_key(crate::app_state::SceneKey::Down);
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(s.newchar_open(), "该开建角对话框");
        s.on_key(crate::app_state::SceneKey::Escape); // 关掉它
        assert!(!s.newchar_open() && s.msg.is_none());

        // 第 2 项 = 删除角色：弹确认（措辞照原版），ESC 取消
        s.on_key(crate::app_state::SceneKey::Down);
        s.on_key(crate::app_state::SceneKey::Enter);
        assert!(s.msg.clone().unwrap().contains("删除角色是不可以恢复的"));
        s.on_key(crate::app_state::SceneKey::Escape);
        assert!(s.msg.is_none() && s.pending.is_none(), "取消要把确认清干净");

        // 第 3 项 = 制作群
        s.on_key(crate::app_state::SceneKey::Down);
        s.on_key(crate::app_state::SceneKey::Enter);
        assert!(s.msg.clone().unwrap().contains("制作群"));

        // 第 4 项 = 退出
        s.on_key(crate::app_state::SceneKey::Escape); // 关掉"制作群"那句
        s.on_key(crate::app_state::SceneKey::Down);
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::Exit);
    }

    /// [开始] 在空账号上要弹提示（原版那句 + 我们的现状）。
    #[test]
    fn 空账号提示() {
        let mut s = Select::new(vec![]);
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::None);
        let m = s.msg.clone().expect("该弹提示");
        assert!(m.contains("创建"), "{m}");
        // 现在**真能建**了 ⇒ 提示要指路（中间菜单那颗），不再指向 `mir2cli`
        assert!(m.contains("新建角色"), "该指路：{m}");
    }

    /// 弹窗是**模态**的：开着时点击先关它，不会连带把那一颗按下。
    #[test]
    fn 弹窗模态() {
        let mut s = Select::new(vec![c_slot(11, "甲", 0)]);
        s.say("先看这个");
        let r = su::Rect {
            x: 10.0,
            y: 10.0,
            w: 50.0,
            h: 20.0,
        };
        let empty = su::Rect {
            x: 9000.0,
            y: 9000.0,
            w: 1.0,
            h: 1.0,
        };
        let l = su::Layout {
            bg: (0.0, 0.0),
            sel: [empty, empty],
            menu: [r, empty, empty, empty, empty],
            slots: [empty, empty],
            slot_texts: [
                su::SlotText {
                    name: (0.0, 0.0),
                    level: (0.0, 0.0),
                    class: (0.0, 0.0),
                },
                su::SlotText {
                    name: (0.0, 0.0),
                    level: (0.0, 0.0),
                    class: (0.0, 0.0),
                },
            ],
            win: (1024.0, 768.0),
        };
        let c = r.center();
        assert_eq!(
            tap(&mut s, &l, None, c, c),
            Action::None,
            "第一次点击只关弹窗"
        );
        assert!(s.msg.is_none());
        assert_eq!(tap(&mut s, &l, None, c, c), Action::Enter(11));
    }

    /// **弹窗的 [Ok] 必须能点**（用户 2026-10-08 报的"确定按钮不能点击、只有回车有效"）。
    ///
    /// 判据两条：① 点 [Ok] 上行 = 确认（删角那条待确认动作**真的执行**）；
    /// ② 点框内别处 = 关掉/取消（原版 `DMessageDlg` 是模态的）。
    #[test]
    fn 弹窗确定按钮能点中() {
        let mk = |mw: u32, mh: u32, ok: u32, okh: u32| {
            su::MsgBox::build((1024, 768), (mw, mh), (ok, okh))
        };
        let b = mk(452, 179, 80, 34);
        let l = su::Layout {
            bg: (0.0, 0.0),
            sel: [su::Rect {
                x: 9000.0,
                y: 9000.0,
                w: 1.0,
                h: 1.0,
            }; 2],
            menu: [su::Rect {
                x: 9000.0,
                y: 9000.0,
                w: 1.0,
                h: 1.0,
            }; 5],
            slots: [su::Rect {
                x: 9000.0,
                y: 9000.0,
                w: 1.0,
                h: 1.0,
            }; 2],
            slot_texts: [
                su::SlotText {
                    name: (0.0, 0.0),
                    level: (0.0, 0.0),
                    class: (0.0, 0.0),
                },
                su::SlotText {
                    name: (0.0, 0.0),
                    level: (0.0, 0.0),
                    class: (0.0, 0.0),
                },
            ],
            win: (1024.0, 768.0),
        };
        // ① 删角的确认：点 [Ok] = 真删
        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(crate::app_state::SceneKey::Down);
        s.on_key(crate::app_state::SceneKey::Down); // 菜单光标到「删除人物」
        assert_eq!(
            s.on_key(crate::app_state::SceneKey::Enter),
            Action::None,
            "先弹确认框"
        );
        let ok = b.ok.center();
        assert_eq!(
            tap(&mut s, &l, Some(b), ok, ok),
            Action::Delete(1),
            "[确定] 该与回车同义（真删）"
        );
        assert!(
            s.msg.is_none() && s.pending.is_none(),
            "确认后要把弹窗清干净"
        );

        // ② 点框内**别处**（不是 [Ok]）= 取消
        s.on_key(crate::app_state::SceneKey::Enter);
        assert!(s.pending.is_some());
        let elsewhere = (b.frame.x + 10.0, b.frame.y + 10.0);
        assert_eq!(
            tap(&mut s, &l, Some(b), elsewhere, elsewhere),
            Action::None,
            "别处 = 取消"
        );
        assert!(s.msg.is_none() && s.pending.is_none(), "取消也要清干净");

        // ③ "从别处**拖到** [Ok] 上松手"不算点中：删角不可恢复，只认一次干净的点击
        s.on_key(crate::app_state::SceneKey::Enter);
        assert!(s.pending.is_some());
        assert_eq!(
            tap(&mut s, &l, Some(b), elsewhere, ok),
            Action::None,
            "按下不在 [Ok] 上就不算点中它"
        );
        assert!(s.pending.is_none(), "这一下只算取消");

        // ④ 量不到弹窗素材（`None`）时**不认**那颗按钮：只关掉，不会误删
        s.on_key(crate::app_state::SceneKey::Enter);
        assert_eq!(
            tap(&mut s, &l, None, ok, ok),
            Action::None,
            "没有几何就不该确认"
        );
        assert!(s.pending.is_none());
    }

    /// 折行：按字符数、`\n` 硬断行。
    #[test]
    fn 折行() {
        assert_eq!(wrap("abc", 8), vec!["abc"]);
        assert_eq!(wrap("abcdefghij", 4), vec!["abcd", "efgh", "ij"]);
        assert_eq!(wrap("a\nb", 8), vec!["a", "b"]);
    }

    fn c(id: u64, name: &str) -> CharEntry {
        c_slot(id, name, id as u32) // 测试里默认"槽位 = id"，方便对位
    }

    fn c_slot(id: u64, name: &str, slot: u32) -> CharEntry {
        CharEntry {
            id,
            name: name.into(),
            level: 1,
            class: 1,
            sex: 0,
            slot,
        }
    }

    /// 角色名的本地校验必须**跟服务端同规则**（`chargen.ValidName`：≥3 字节、不含 `" \t/@?'"`）。
    #[test]
    fn 角色名校验照服务端() {
        assert!(valid_char_name("abc", None).is_ok());
        assert!(
            valid_char_name("勇士", None).is_ok(),
            "一个汉字 3 字节 ⇒ 够 3"
        );
        assert_eq!(
            valid_char_name("  abc  ", None).unwrap(),
            "abc",
            "两端空白 Trim"
        );
        assert!(valid_char_name("ab", None).is_err());
        for bad in ["a b", "a\tb", "a/b", "a@b", "a?b", "a'b"] {
            assert!(valid_char_name(bad, None).is_err(), "「{bad}」不该放行");
        }
    }

    /// 建角那条链：开框 → 打字 → 确定 ⇒ `Action::Create`，且编号已换成**协议值**。
    #[test]
    fn 建角对话框走完出动作() {
        let mut s = Select::new(Vec::new());
        assert!(!s.newchar_open());
        s.on_key(crate::app_state::SceneKey::Down); // 光标挪到 MENU[1] = 新建角色
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(s.newchar_open(), "点「新建角色」该开对话框");
        // 打字：超长按原版 `MaxLength = 14`（字符数）截断
        s.on_text("ABCDEFGHIJKLMNOPQRST"); // 20 个 ⇒ 只留 14
        assert_eq!(
            s.newchar.as_ref().unwrap().name.chars().count(),
            NAME_MAX,
            "超长要截到上限"
        );
        s.newchar.as_mut().unwrap().name.clear();
        s.on_text("勇者一号");
        // Tab 到职业、右移一格 ⇒ 法师；Tab 到性别、右移一格 ⇒ 女
        s.on_key(crate::app_state::SceneKey::Tab);
        s.on_key(crate::app_state::SceneKey::Right);
        s.on_key(crate::app_state::SceneKey::Tab);
        s.on_key(crate::app_state::SceneKey::Right);
        match s.on_key(crate::app_state::SceneKey::Enter) {
            Action::Create {
                name,
                class,
                gender,
                hair,
            } => {
                assert!(name.ends_with("勇者一号"), "名字该带上刚打的字：{name}");
                assert!(name.chars().count() <= NAME_MAX, "长度上限：{name}");
                assert_eq!(class, 2, "职业索引 1 ⇒ 协议值 2");
                assert_eq!(gender, 2, "性别索引 1 ⇒ 协议值 2");
                assert!((1..=5).contains(&hair), "发型 1..=5：{hair}");
            }
            other => panic!("该产生 Action::Create，实得 {other:?}"),
        }
        assert!(!s.newchar_open(), "确定之后对话框该关掉");
    }

    /// 名字不合规：**不关框**、只弹一句（服务端那道门我们本地先拦一遍）。
    #[test]
    fn 建角名字不合规就不出动作() {
        let mut s = Select::new(Vec::new());
        s.on_key(crate::app_state::SceneKey::Down);
        s.on_key(crate::app_state::SceneKey::Enter);
        s.on_text("ab"); // 少于 3 字节
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(s.newchar_open(), "校验不过不该关掉对话框");
        assert!(s.msg.is_some(), "校验不过要弹一句为什么");
        // 补成合法的，再确定
        s.msg = None;
        s.on_text("c");
        assert!(matches!(
            s.on_key(crate::app_state::SceneKey::Enter),
            Action::Create { .. }
        ));
    }

    /// 删角：**得先选中一个角色**，再弹确认；回车才真删，其它键 = 取消。
    #[test]
    fn 删角要确认且要选中角色() {
        // 没有角色 ⇒ 直接提示，不开确认
        let mut empty = Select::new(Vec::new());
        empty.on_key(crate::app_state::SceneKey::Down);
        empty.on_key(crate::app_state::SceneKey::Down); // MENU[2] = 删除角色
        assert_eq!(
            empty.on_key(crate::app_state::SceneKey::Enter),
            Action::None
        );
        assert!(empty.msg.is_some(), "没角色可删该提示一句");

        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(crate::app_state::SceneKey::Down);
        s.on_key(crate::app_state::SceneKey::Down);
        assert_eq!(
            s.on_key(crate::app_state::SceneKey::Enter),
            Action::None,
            "只是弹确认框"
        );
        assert!(s.msg.is_some(), "该弹确认框");
        assert_eq!(
            s.on_key(crate::app_state::SceneKey::Escape),
            Action::None,
            "ESC = 取消"
        );
        assert!(s.msg.is_none() && s.pending.is_none(), "取消要把确认清干净");
        // 再来一次，回车 = 真删（默认选中的是槽 0 ⇒ id=1）
        s.on_key(crate::app_state::SceneKey::Enter);
        assert_eq!(
            s.on_key(crate::app_state::SceneKey::Enter),
            Action::Delete(1)
        );
    }

    /// 槽满了不让建（原版：每个账号只能创建两个角色）。
    #[test]
    fn 满槽不让建() {
        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(crate::app_state::SceneKey::Down);
        assert_eq!(s.on_key(crate::app_state::SceneKey::Enter), Action::None);
        assert!(!s.newchar_open(), "满了不该开对话框");
        assert!(s.msg.is_some(), "该说一句为什么");
    }
}

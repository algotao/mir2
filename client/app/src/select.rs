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

use std::path::Path;
use std::time::Instant;

use sdl3::keyboard::Keycode;
use sdl3::pixels::Color;
use sdl3::render::{TextureCreator, WindowCanvas};

use mir2_core::select_ui as su;
use mir2_protocol as proto;

use crate::font::{Rgb, TextCache};
use crate::ui::{ui_inv_px, ui_px, uifill, UiCache};

/// 名字/等级/职业 的字色（原版 `clWhite` + `clBlack` 描边，`IntroScn.pas:1519`）。
const C_TEXT: Rgb = (255, 255, 255);
/// 次要文字（对话框里的字段标签与操作提示）。
const C_DIM: Rgb = (176, 172, 164);
const C_SHADOW: Rgb = (0, 0, 0);
/// 键盘选中的菜单项用**它的按下态图**当高亮（这套素材没有单独的"选中"图）。
const C_SEL_HL: Rgb = (255, 255, 255);

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
enum Hit {
    /// 某一块面板的「选择」按钮（= 选这个槽）。
    Sel(usize),
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
    /// "要确认的动作"（原版 `DMessageDlg` 的 Yes/No）：回车/[确定]才执行，其余输入 = 取消。
    /// 目前只有删角用它 —— 删除不可恢复，宁可要求一个明确的"是"。
    pending: Option<Action>,
    down: Option<Hit>,
    /// 每个槽上一次看到的相位 —— 用来在**解冻那一刻**响一声 `101`
    ///（原版是"点了某个槽"就响一次：`IntroScn.pas:1170/1187` 的 `PlaySound(s_meltstone)`）。
    last_phase: [su::SlotPhase; su::Art::SLOTS],
    /// 小人 / 光效的**落点备忘**（见 `su::SlotPlace`）：基准是"站立第 0 帧"，
    /// 石化帧再叠原版的手调量 —— 逐帧按包围盒对齐会让"已经站定的脚"随上半身摆动左右挪，
    /// 而拿石化图自己的包围盒去居中又会差 0.5~3.5px（用户两轮都报过）。
    place: su::SlotPlace,
    fx_place: su::SlotPlace,
    last: Instant,
}

impl Select {
    pub fn new(chars: Vec<CharEntry>) -> Self {
        // 第 0 个槽 = **默认选中**（站立、播动画）；其余**直接石化**。
        // ⚠️ 原版进场景时两个都站着（`OpenScene` 只开窗口放 BGM），于是看不出
        // 当前选的是谁 —— 那时点[开始]还会被拒（两边 `Selected` 都是 FALSE）。
        // 我们有意改成"默认选中第一个"，一眼能看出当前是哪个。
        let anims = std::array::from_fn(|i| {
            let (job, sex) = chars.get(i).map_or((0, 0), |c| (c.job(), c.sex));
            if i == 0 {
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
            picked: 0,
            anims,
            menu_cursor: 0,
            msg: None,
            newchar: None,
            pending: None,
            down: None,
            last_phase,
            place: su::SlotPlace::default(),
            fx_place: su::SlotPlace::default(),
            last: Instant::now(),
        }
    }

    /// 当前选中的角色（空槽 ⇒ `None`）。
    pub fn picked_char(&self) -> Option<&CharEntry> {
        self.chars.get(self.picked)
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
        let Some(c) = self.chars.get(self.picked) else {
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
    fn newchar_key(&mut self, k: Keycode) -> Action {
        if self.newchar.is_none() {
            return Action::None;
        }
        match k {
            Keycode::Escape => {
                self.newchar = None;
                Action::None
            }
            Keycode::Return | Keycode::KpEnter => self.confirm_newchar(),
            Keycode::Tab => {
                let d = self.newchar.as_mut().expect("上面判过");
                d.focus = (d.focus + 1) % 3;
                Action::None
            }
            Keycode::Backspace => {
                let d = self.newchar.as_mut().expect("上面判过");
                if d.focus == 0 {
                    d.name.pop();
                }
                Action::None
            }
            Keycode::Left | Keycode::Right => {
                let dir = if k == Keycode::Right { 1 } else { -1 };
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
        match valid_char_name(&d.name) {
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
    fn newchar_click(&mut self, p: (f32, f32), l: &su::Layout) -> Action {
        match su::DialogBox::build((l.win.0 as u32, l.win.1 as u32)).hit(p) {
            Some(su::DialogHit::Ok) => self.confirm_newchar(),
            Some(su::DialogHit::Close) => {
                self.newchar = None;
                Action::None
            }
            Some(su::DialogHit::Name) => {
                if let Some(d) = self.newchar.as_mut() {
                    d.focus = 0;
                }
                Action::None
            }
            Some(su::DialogHit::Job(i)) => {
                if let Some(d) = self.newchar.as_mut() {
                    d.job = i;
                    d.focus = 1;
                }
                Action::None
            }
            Some(su::DialogHit::Sex(i)) => {
                if let Some(d) = self.newchar.as_mut() {
                    d.sex = i;
                    d.focus = 2;
                }
                Action::None
            }
            None => Action::None,
        }
    }

    /// 按角色 id 预选（`MIR2_CHAR` 指的那个）。找不到就什么都不做。
    pub fn pick_id(&mut self, id: u64) {
        if let Some(i) = self.chars.iter().position(|c| c.id == id) {
            self.pick_slot(i);
        }
    }

    fn hit_at(&self, p: (f32, f32), l: &su::Layout) -> Option<Hit> {
        for (i, r) in l.sel.iter().enumerate() {
            if r.hit(p) {
                return Some(Hit::Sel(i));
            }
        }
        for (i, r) in l.menu.iter().enumerate() {
            if r.hit(p) {
                return Some(Hit::Menu(i));
            }
        }
        None
    }

    /// `msg` 同 [`Select::on_up`]：弹窗的几何（`None` = 量不到素材 ⇒ 不认那颗 [Ok]）。
    pub fn on_down(&mut self, p: (f32, f32), l: &su::Layout, msg: Option<su::MsgBox>) {
        // 弹窗开着时只认它那颗 [Ok]（模态），其余位置一律记 `None` ⇒ 抬起时不会触发别的
        self.down = if self.pending.is_some() || self.msg.is_some() {
            msg.filter(|m| m.hit_ok(p)).map(|_| Hit::MsgOk)
        } else {
            self.hit_at(p, l)
        };
    }

    /// 抬起：只有"按下与抬起在同一颗"才算点中。
    ///
    /// `msg` = 弹窗（[`su::MsgBox`]）的几何，`None` = 量不到素材（那就不认它那颗 [Ok]）。
    /// 它必须由调用方给进来：弹窗的几何属于素材尺寸，场景里只有 `Layout`（见 [`su::MsgBox`]）。
    pub fn on_up(&mut self, p: (f32, f32), l: &su::Layout, msg: Option<su::MsgBox>) -> Action {
        // 建角对话框是模态的：只认它那几颗按钮，其余点击一律吃掉。
        if self.newchar.is_some() {
            self.down = None;
            return self.newchar_click(p, l);
        }
        // 弹窗是**模态**的（原版 `DMessageDlg`）：先判它那颗 [Ok] ——
        // 点 [Ok]（且**按下也在它上面**）= 确认（删角：真删；其余：只是关掉），
        // 点别处 = 关掉/取消。
        //
        // ⚠️ 这段必须在"按下与抬起同一颗"那条之前：弹窗的 [Ok] 不在 `hit_at` 的范围内
        //（那里只有「选择」与菜单），照旧顺序走会**永远判不出点中它** ——
        // 表现就是"确定按钮不能点击、只有回车有效"（用户 2026-10-08 报的）。
        if self.pending.is_some() || self.msg.is_some() {
            let on_ok = self.down == Some(Hit::MsgOk) && msg.is_some_and(|m| m.hit_ok(p));
            self.down = None;
            if on_ok {
                self.msg = None;
                // 删角的那条"待确认动作"：点 [Ok] 与回车同义
                return self.pending.take().unwrap_or(Action::None);
            }
            self.msg = None;
            self.pending = None;
            return Action::None;
        }
        let hit = self.hit_at(p, l);
        let same = hit.is_some() && hit == self.down;
        self.down = None;
        if !same {
            return Action::None;
        }
        let hit = hit.expect("上面判过");
        // 鼠标点菜单项时把键盘光标也挪过去（两条输入共用一份状态，免得视觉上打架）
        if let Hit::Menu(i) = hit {
            self.menu_cursor = i;
        }
        self.activate(hit)
    }

    /// 键盘（**我们的扩展**）。
    pub fn on_key(&mut self, k: Keycode) -> Action {
        // ① 建角对话框（模态）：所有按键都归它
        if self.newchar.is_some() {
            return self.newchar_key(k);
        }
        // ② "要确认的动作"（删角）：回车 = 执行，其它键 = 取消
        if let Some(act) = self.pending.take() {
            self.msg = None;
            return if matches!(k, Keycode::Return | Keycode::KpEnter) {
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
            Keycode::Left => {
                self.move_pick(-1);
                Action::None
            }
            Keycode::Right => {
                self.move_pick(1);
                Action::None
            }
            // 上下 = 换菜单项（布局就是中间竖排）
            Keycode::Up => {
                self.menu_cursor = (self.menu_cursor + su::MENU.len() - 1) % su::MENU.len();
                Action::None
            }
            Keycode::Down => {
                self.menu_cursor = (self.menu_cursor + 1) % su::MENU.len();
                Action::None
            }
            Keycode::Return | Keycode::KpEnter | Keycode::Space => {
                self.activate(Hit::Menu(self.menu_cursor))
            }
            _ => Action::None,
        }
    }

    /// 真正做一件事（鼠标/键盘共用）。
    fn activate(&mut self, hit: Hit) -> Action {
        match hit {
            Hit::Sel(i) => {
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
            if i < self.chars.len() {
                self.pick_slot(i);
                return;
            }
        }
    }

    fn pick_slot(&mut self, i: usize) {
        if i >= self.chars.len() {
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

    /// 画一帧。
    #[allow(clippy::too_many_arguments)]
    pub fn draw<'a, T>(
        &mut self,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        ui: &mut UiCache<'a>,
        text: &mut TextCache<'a>,
        asset_dir: &Option<std::path::PathBuf>,
        win: (u32, u32),
        now: Instant,
    ) -> Result<(), sdl3::Error> {
        let Some(dir) = asset_dir.as_deref() else {
            canvas.set_draw_color(sdl3::pixels::Color::RGB(80, 0, 0));
            canvas.clear();
            return text.draw_ui(
                canvas,
                tc,
                "选角素材不在：设置 MIR2_ASSET_DIR",
                16.0,
                16.0,
                (255, 220, 220),
                None,
            );
        };
        let Some(l) = su::Layout::build(win, |lib, idx| ui.size(dir, lib, idx)) else {
            canvas.set_draw_color(sdl3::pixels::Color::RGB(80, 0, 0));
            canvas.clear();
            return text.draw_ui(
                canvas,
                tc,
                "选角素材缺失：需要 Prguse[65..72]（见 MIR2_ASSET_DIR）",
                16.0,
                16.0,
                (255, 220, 220),
                None,
            );
        };

        canvas.set_draw_color(sdl3::pixels::Color::RGB(0, 0, 0));
        canvas.clear();
        // 底图**拉伸 1.28 倍铺满**画布（2026-10-09 改，见 `ui::UI_SCALE`）：落点就是设计空间的
        // (0,0)（`l.bg` 在 `win = 800×600` 下恒为 (0,0)），`ui.draw` 把位置与尺寸一起乘 1.28
        // ⇒ 800×1.28 = 1024、600×1.28 = 768，正好铺满。D-51 那套"1:1 居中 + 石纹平铺补边"删掉。
        ui.draw(
            canvas,
            tc,
            dir,
            su::Art::BG.0,
            su::Art::BG.1,
            l.bg.0,
            l.bg.1,
        );

        // 「开始」那颗**石台**：只存在于新版底图 `Prguse2[480]` 里，1.76 的底图没有 ⇒ 裁过来叠上。
        // 用户 2026-10-09 要的（官方选角图里"开始"下面垫着台子）；出处与量法见
        // `core::select_ui::START_PLATE`。画在**底图之上、小人之下**（台子是面板的一部分）。
        {
            let p = su::start_plate_at(l.menu[0]);
            let (sx, sy, sw, sh) = su::START_PLATE_SRC;
            let _ = ui.draw_ui_src(
                canvas,
                tc,
                dir,
                su::Art::START_PLATE.0,
                su::Art::START_PLATE.1,
                (sx, sy, sw, sh),
                (p.x, p.y, p.w, p.h),
            );
        }

        // 这一帧的动画（dt 用真实时间；`tick` 内部按 300/50ms 进帧）
        let dt = now.saturating_duration_since(self.last).as_millis() as u32;
        self.last = now;

        for slot in 0..su::Art::SLOTS {
            let Some(c) = self.chars.get(slot).cloned() else {
                continue; // 空槽：什么都不画
            };
            let f = self.anims[slot].tick(dt, slot == self.picked);
            let anchor = su::slot_anchor(slot, l.bg);
            // ⚠️ 落点的**基准永远是"站立第 0 帧"**，不是当前这一帧：
            // 站立 16 帧的上半身在摆（包围盒中点游走，战士女最多 13.5px）⇒ 逐帧对齐会把
            // 站定的脚推着挪；而槽一进来就是石化的，拿当前帧当基准还会"选中那一刻跳一下"。
            // 石化帧 = 这个基准 + 原版的手调量（`su::frozen_offset`），见 `su::SlotPlace`。
            if let Some(stand0) = ui.bbox(dir, su::Art::CHR, su::stand_index(c.job(), c.sex, 0)) {
                let (sx, sy) = self
                    .place
                    .of(slot, c.job(), c.sex, f.at_frozen, stand0, anchor);
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::CHR,
                    f.index,
                    sx,
                    sy,
                    (255, 255, 255),
                );
            }
            // 解冻时叠一层选中光效（原版 `DrawBlend`，`IntroScn.pas:1442`）。
            //
            // ⚠️ 位置用**同一条对齐规则**：把光效图不透明部分的底边中点也摆到
            // 同一个锚点上（= 居中压在人物身上、底边落在脚下）；同样**一帧定死**
            //（光效自己那 14 帧的包围盒也在长，逐帧对齐会让整团光左右抖）。
            // 原来这里是 `(anchor.0 - 30, anchor.1 - 60)` 这种**猜出来的偏移**，
            // 结果光效偏右、也不在脚下（用户实测报的）。
            if let Some(e) = f.effect {
                if let Some(eb) = ui.bbox(dir, su::Art::CHR, e) {
                    let (ex, ey) = self.fx_place.fx(slot, eb, anchor);
                    ui.draw_tint(canvas, tc, dir, su::Art::CHR, e, ex, ey, C_SEL_HL);
                }
            }
            // 名字 / 等级 / 职业（白字黑边，写在面板的字段框里）
            let t = su::slot_text(slot, l.bg);
            text.draw_ui(
                canvas,
                tc,
                &c.name,
                t.name.0,
                t.name.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw_ui(
                canvas,
                tc,
                &c.level.to_string(),
                t.level.0,
                t.level.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw_ui(
                canvas,
                tc,
                su::class_name(c.class),
                t.class.0,
                t.class.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
        }

        // 按下态 / 键盘高亮：**只在按住或键盘选中时**叠那张图
        //（原版 `DscSelect1DirectPaint` 只认 `Downed`；键盘是我们的扩展）
        match self.down {
            // 弹窗那颗 [Ok] 没有按下态图（原版那颗也只是同一张：登录界面那边按下是 +1 位移，
            // 选角这边按下的反馈就是"弹窗被点掉了"，不必再画一层）
            Some(Hit::MsgOk) => {}
            Some(Hit::Sel(i)) => {
                let r = l.sel[i];
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::SEL_DOWN[i].0,
                    su::Art::SEL_DOWN[i].1,
                    r.x,
                    r.y,
                    C_SEL_HL,
                );
            }
            Some(Hit::Menu(i)) => {
                let r = l.menu[i];
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::MENU_DOWN[i].0,
                    su::Art::MENU_DOWN[i].1,
                    r.x,
                    r.y,
                    C_SEL_HL,
                );
            }
            None => {
                // 键盘光标那一项也高亮（否则键盘用户看不出选中的是哪一项）
                let i = self.menu_cursor;
                let r = l.menu[i];
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::MENU_DOWN[i].0,
                    su::Art::MENU_DOWN[i].1,
                    r.x,
                    r.y,
                    (170, 170, 170),
                );
            }
        }

        // 建角对话框（模态；原版 `DCreateChr`）—— 画在弹窗下面一层
        if let Some(d) = self.newchar.as_ref() {
            draw_newchar(canvas, tc, text, win, d)?;
        }

        // 弹窗
        if let Some(m) = self.msg.clone() {
            draw_msgbox(canvas, tc, ui, text, dir, win, &m)?;
        }
        Ok(())
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

/// 角色名的**本地校验** —— 照服务端那份规则与措辞
///（`chargen.ValidName` + `server/internal/gamesvr/netproto.go` 的那句话）：
/// Trim 之后**至少 3 个字节**（一个汉字算 3 个），且不含 `空格 \t / @ ? '`。
///
/// 为什么要本地先判：省一次往返、错误立刻可见。**服务端那道门照样在**（它才是权威），
/// 这里只是把同一套规则提前说一遍 —— 两边措辞保持一致，用户不会看到两种说法。
fn valid_char_name(raw: &str) -> Result<String, &'static str> {
    const BAD: &str = "角色名不合适（至少 3 个字节，且不能含空格与 /@?'）";
    let name = raw.trim();
    let has_bad = name
        .chars()
        .any(|c| c == ' ' || c == '\t' || "/@?'".contains(c));
    if name.len() < 3 || has_bad {
        return Err(BAD);
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

/// 画建角对话框（原版 `DCreateChr`；几何见 [`su::DialogBox`]）。
///
/// ⚠️ 素材里**没有**这扇窗口的背景图（原版是 `Prguse[73]`，还没接进 `Art`）⇒ 这里是
/// 实心框 + 边框，**不是照原版搬的窗口**；文字走真字体（中文姓名/职业名要能画）。
fn draw_newchar<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    text: &mut TextCache<'a>,
    win: (u32, u32),
    d: &NewChar,
) -> Result<(), sdl3::Error> {
    let b = su::DialogBox::build(win);
    let f = b.frame;
    uifill(
        canvas,
        f.x - 2.0,
        f.y - 2.0,
        f.w + 4.0,
        f.h + 4.0,
        Color::RGB(0, 0, 0),
    )?;
    uifill(canvas, f.x, f.y, f.w, f.h, Color::RGB(32, 28, 24))?;
    text.draw_ui(
        canvas,
        tc,
        "新建角色",
        f.x + 24.0,
        f.y + 16.0,
        C_TEXT,
        Some(C_SHADOW),
    )?;
    // 姓名（唯一输入框：光标用一个方块代替闪动 —— 够用就好）
    text.draw_ui(canvas, tc, "姓名", f.x + 24.0, b.name.y + 4.0, C_DIM, None)?;
    uifill(
        canvas,
        b.name.x,
        b.name.y,
        b.name.w,
        b.name.h,
        Color::RGB(14, 14, 18),
    )?;
    text.draw_ui(
        canvas,
        tc,
        &format!("{}▌", d.name),
        b.name.x + 6.0,
        b.name.y + 4.0,
        C_TEXT,
        None,
    )?;
    // 职业（选中那颗用亮底）
    text.draw_ui(
        canvas,
        tc,
        "职业",
        f.x + 24.0,
        b.job[0].y + 4.0,
        C_DIM,
        None,
    )?;
    for (i, r) in b.job.iter().enumerate() {
        if d.job == i {
            uifill(canvas, r.x, r.y, r.w, r.h, Color::RGB(92, 72, 40))?;
        }
        text.draw_ui(
            canvas,
            tc,
            su::class_name(i as i32 + 1),
            r.x + 6.0,
            r.y + 4.0,
            C_TEXT,
            None,
        )?;
    }
    // 性别
    text.draw_ui(
        canvas,
        tc,
        "性别",
        f.x + 24.0,
        b.sex[0].y + 4.0,
        C_DIM,
        None,
    )?;
    for (i, r) in b.sex.iter().enumerate() {
        if d.sex == i {
            uifill(canvas, r.x, r.y, r.w, r.h, Color::RGB(92, 72, 40))?;
        }
        text.draw_ui(
            canvas,
            tc,
            if i == 0 { "男" } else { "女" },
            r.x + 6.0,
            r.y + 4.0,
            C_TEXT,
            None,
        )?;
    }
    // 两颗按钮
    uifill(
        canvas,
        b.ok.x,
        b.ok.y,
        b.ok.w,
        b.ok.h,
        Color::RGB(74, 62, 40),
    )?;
    text.draw_ui(
        canvas,
        tc,
        "确定",
        b.ok.x + 20.0,
        b.ok.y + 4.0,
        C_TEXT,
        None,
    )?;
    uifill(
        canvas,
        b.close.x,
        b.close.y,
        b.close.w,
        b.close.h,
        Color::RGB(52, 46, 40),
    )?;
    text.draw_ui(
        canvas,
        tc,
        "关闭",
        b.close.x + 20.0,
        b.close.y + 4.0,
        C_TEXT,
        None,
    )?;
    // 操作提示（键盘为主；鼠标也能点那几颗）
    text.draw_ui(
        canvas,
        tc,
        "TAB 切换   ←→ 改   回车确定   ESC 取消",
        f.x + 24.0,
        f.y + f.h - 60.0,
        C_DIM,
        None,
    )?;
    Ok(())
}

/// 消息框（`Prguse[360]` + `[363]` 的 [Ok]，居中）—— 与登录界面同一套素材。
///
/// ⚠️ 几何走 [`su::MsgBox`]（**同一份**也用于命中测试）：绘制与点击必须同一个矩形，
/// 否则就是"看得见、点不中"（用户 2026-10-08 报的）。`login.rs` 那边是 `login_ui::Layout`
/// 内联算的同一套规则。
fn draw_msgbox<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    text: &mut TextCache<'a>,
    dir: &Path,
    win: (u32, u32),
    msg: &str,
) -> Result<(), sdl3::Error> {
    let b = match msgbox_geom(ui, dir, win) {
        Some(b) => b,
        None => return Ok(()), // 量不到素材：什么都不画（与"素材缺失"同一条纪律）
    };
    ui.draw(canvas, tc, dir, "Prguse", 360, b.frame.x, b.frame.y);
    if b.ok.w > 0.0 {
        ui.draw(canvas, tc, dir, "Prguse", 363, b.ok.x, b.ok.y);
    }
    // ⚠️ 框宽是**设计空间**、字宽是**画布像素**（字号按 1.28 放大过）⇒ 先 `ui_px` 换算，
    // 否则算出的列数偏大 1.28 倍，行会顶出框外。
    let cols = ((ui_px(b.frame.w - 24.0)) / text.width("中").max(1.0)) as usize;
    let mut y = b.frame.y + 20.0;
    for line in wrap(msg, cols.max(8)) {
        text.draw_ui(
            canvas,
            tc,
            &line,
            b.frame.x + 12.0,
            y,
            (255, 255, 255),
            Some((0, 0, 0)),
        )?;
        // ⚠️ 行高是**画布像素**（17.9px 字号算出来的），而 `y` 在设计空间里 ⇒ 除回去
        y += ui_inv_px(text.line_height() + 2.0);
    }
    Ok(())
}

/// 弹窗的几何（尺寸来自素材；`Prguse[360]` 缺了就 `None`）。
///
/// 抽成函数是为了让**画**（[`draw_msgbox`]）与**点**（[`Select::on_up`]）用同一个矩形 ——
/// 少一处算错就多一个"看得见点不中"。
pub fn msgbox_geom(ui: &mut UiCache, dir: &Path, win: (u32, u32)) -> Option<su::MsgBox> {
    let frame = ui.size(dir, "Prguse", 360)?;
    let ok = ui.size(dir, "Prguse", 363).unwrap_or((0, 0));
    Some(su::MsgBox::build(win, frame, ok))
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

#[cfg(test)]
mod tests {
    use super::*;

    fn summary(id: u64, name: &str, class: i32, gender: proto::Gender) -> proto::CharacterSummary {
        proto::CharacterSummary {
            character_id: id,
            name: name.into(),
            class,
            gender: gender as i32,
            level: 7,
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
                CharEntry::from_summary(&summary(11, "甲", 1, proto::Gender::Male)),
                CharEntry::from_summary(&summary(22, "乙", 2, proto::Gender::Female)),
            ]
        };
        let mut s = Select::new(two());
        s.on_key(Keycode::Right);
        assert_eq!(s.picked_char().map(|c| c.id), Some(22));
        s.on_key(Keycode::Left);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));

        // 菜单光标：默认第 0 项（开始），↓ 走一项、↑ 转回最后一项
        assert_eq!(s.menu_cursor, 0);
        s.on_key(Keycode::Down);
        assert_eq!(s.menu_cursor, 1);
        s.on_key(Keycode::Up);
        assert_eq!(s.menu_cursor, 0);
        s.on_key(Keycode::Up);
        assert_eq!(s.menu_cursor, su::MENU.len() - 1);

        // 只有一个角色时，左右都停在它身上（空槽跳过）
        let mut s = Select::new(vec![CharEntry::from_summary(&summary(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        s.on_key(Keycode::Right);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));
    }

    /// 菜单项：开始 ⇒ `Enter`；其余是**诚实的占位**（说清楚现状与替代做法）。
    #[test]
    fn 菜单项行为() {
        let mut s = Select::new(vec![CharEntry::from_summary(&summary(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        // 第 0 项 = 开始
        assert_eq!(s.on_key(Keycode::Return), Action::Enter(11));

        // 第 1 项 = 新建角色：开建角对话框（原版 `DCreateChr`），不再有那套占位提示
        s.on_key(Keycode::Down);
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        assert!(s.newchar_open(), "该开建角对话框");
        s.on_key(Keycode::Escape); // 关掉它
        assert!(!s.newchar_open() && s.msg.is_none());

        // 第 2 项 = 删除角色：弹确认（措辞照原版），ESC 取消
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Return);
        assert!(s.msg.clone().unwrap().contains("删除角色是不可以恢复的"));
        s.on_key(Keycode::Escape);
        assert!(s.msg.is_none() && s.pending.is_none(), "取消要把确认清干净");

        // 第 3 项 = 制作群
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Return);
        assert!(s.msg.clone().unwrap().contains("制作群"));

        // 第 4 项 = 退出
        s.on_key(Keycode::Escape); // 关掉"制作群"那句
        s.on_key(Keycode::Down);
        assert_eq!(s.on_key(Keycode::Return), Action::Exit);
    }

    /// [开始] 在空账号上要弹提示（原版那句 + 我们的现状）。
    #[test]
    fn 空账号提示() {
        let mut s = Select::new(vec![]);
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        let m = s.msg.clone().expect("该弹提示");
        assert!(m.contains("创建"), "{m}");
        // 现在**真能建**了 ⇒ 提示要指路（中间菜单那颗），不再指向 `mir2cli`
        assert!(m.contains("新建角色"), "该指路：{m}");
    }

    /// 弹窗是**模态**的：开着时点击先关它，不会连带把那一颗按下。
    #[test]
    fn 弹窗模态() {
        let mut s = Select::new(vec![CharEntry::from_summary(&summary(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
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
            win: (1024.0, 768.0),
        };
        let c = r.center();
        s.on_down(c, &l, None);
        assert_eq!(s.on_up(c, &l, None), Action::None, "第一次点击只关弹窗");
        assert!(s.msg.is_none());
        s.on_down(c, &l, None);
        assert_eq!(s.on_up(c, &l, None), Action::Enter(11));
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
            win: (1024.0, 768.0),
        };
        // ① 删角的确认：点 [Ok] = 真删
        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Down); // 菜单光标到「删除人物」
        assert_eq!(s.on_key(Keycode::Return), Action::None, "先弹确认框");
        let ok = b.ok.center();
        s.on_down(ok, &l, Some(b));
        assert_eq!(
            s.on_up(ok, &l, Some(b)),
            Action::Delete(1),
            "[确定] 该与回车同义（真删）"
        );
        assert!(
            s.msg.is_none() && s.pending.is_none(),
            "确认后要把弹窗清干净"
        );

        // ② 点框内**别处**（不是 [Ok]）= 取消
        s.on_key(Keycode::Return);
        assert!(s.pending.is_some());
        let elsewhere = (b.frame.x + 10.0, b.frame.y + 10.0);
        s.on_down(elsewhere, &l, Some(b));
        assert_eq!(s.on_up(elsewhere, &l, Some(b)), Action::None, "别处 = 取消");
        assert!(s.msg.is_none() && s.pending.is_none(), "取消也要清干净");

        // ③ "从别处**拖到** [Ok] 上松手"不算点中：删角不可恢复，只认一次干净的点击
        s.on_key(Keycode::Return);
        assert!(s.pending.is_some());
        s.on_down(elsewhere, &l, Some(b));
        assert_eq!(
            s.on_up(ok, &l, Some(b)),
            Action::None,
            "按下不在 [Ok] 上就不算点中它"
        );
        assert!(s.pending.is_none(), "这一下只算取消");

        // ④ 量不到弹窗素材（`None`）时**不认**那颗按钮：只关掉，不会误删
        s.on_key(Keycode::Return);
        s.on_down(ok, &l, None);
        assert_eq!(s.on_up(ok, &l, None), Action::None, "没有几何就不该确认");
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
        CharEntry {
            id,
            name: name.into(),
            level: 1,
            class: 1,
            sex: 0,
        }
    }

    /// 角色名的本地校验必须**跟服务端同规则**（`chargen.ValidName`：≥3 字节、不含 `" \t/@?'"`）。
    #[test]
    fn 角色名校验照服务端() {
        assert!(valid_char_name("abc").is_ok());
        assert!(valid_char_name("勇士").is_ok(), "一个汉字 3 字节 ⇒ 够 3");
        assert_eq!(valid_char_name("  abc  ").unwrap(), "abc", "两端空白 Trim");
        assert!(valid_char_name("ab").is_err());
        for bad in ["a b", "a\tb", "a/b", "a@b", "a?b", "a'b"] {
            assert!(valid_char_name(bad).is_err(), "「{bad}」不该放行");
        }
    }

    /// 建角那条链：开框 → 打字 → 确定 ⇒ `Action::Create`，且编号已换成**协议值**。
    #[test]
    fn 建角对话框走完出动作() {
        let mut s = Select::new(Vec::new());
        assert!(!s.newchar_open());
        s.on_key(Keycode::Down); // 光标挪到 MENU[1] = 新建角色
        assert_eq!(s.on_key(Keycode::Return), Action::None);
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
        s.on_key(Keycode::Tab);
        s.on_key(Keycode::Right);
        s.on_key(Keycode::Tab);
        s.on_key(Keycode::Right);
        match s.on_key(Keycode::Return) {
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
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Return);
        s.on_text("ab"); // 少于 3 字节
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        assert!(s.newchar_open(), "校验不过不该关掉对话框");
        assert!(s.msg.is_some(), "校验不过要弹一句为什么");
        // 补成合法的，再确定
        s.msg = None;
        s.on_text("c");
        assert!(matches!(s.on_key(Keycode::Return), Action::Create { .. }));
    }

    /// 删角：**得先选中一个角色**，再弹确认；回车才真删，其它键 = 取消。
    #[test]
    fn 删角要确认且要选中角色() {
        // 没有角色 ⇒ 直接提示，不开确认
        let mut empty = Select::new(Vec::new());
        empty.on_key(Keycode::Down);
        empty.on_key(Keycode::Down); // MENU[2] = 删除角色
        assert_eq!(empty.on_key(Keycode::Return), Action::None);
        assert!(empty.msg.is_some(), "没角色可删该提示一句");

        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Down);
        assert_eq!(s.on_key(Keycode::Return), Action::None, "只是弹确认框");
        assert!(s.msg.is_some(), "该弹确认框");
        assert_eq!(s.on_key(Keycode::Escape), Action::None, "ESC = 取消");
        assert!(s.msg.is_none() && s.pending.is_none(), "取消要把确认清干净");
        // 再来一次，回车 = 真删（默认选中的是槽 0 ⇒ id=1）
        s.on_key(Keycode::Return);
        assert_eq!(s.on_key(Keycode::Return), Action::Delete(1));
    }

    /// 槽满了不让建（原版：每个账号只能创建两个角色）。
    #[test]
    fn 满槽不让建() {
        let mut s = Select::new(vec![c(1, "甲"), c(2, "乙")]);
        s.on_key(Keycode::Down);
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        assert!(!s.newchar_open(), "满了不该开对话框");
        assert!(s.msg.is_some(), "该说一句为什么");
    }
}

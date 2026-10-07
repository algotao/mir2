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
//! 2. **[创建人物] / [删除人物] / [制作群] 是诚实的占位**：新协议下服务端还没实现
//!    `CreateCharacter` / `DeleteCharacter`（`gamesvr` 的 dispatch 里没有）⇒ 点了
//!    会说明现状与替代做法，而不是静默没反应。
//! 3. **空槽点了没反应**（原版会选中它、然后[开始]发一个空名字被服务端拒掉）。
//! 4. 压暗过渡略去（理由见 `core::select_ui::SlotAnim`）。

use std::path::Path;
use std::time::Instant;

use sdl3::keyboard::Keycode;
use sdl3::render::{TextureCreator, WindowCanvas};

use mir2_core::select_ui as su;
use mir2_protocol as proto;

use crate::font::{Rgb, TextCache};
use crate::ui::UiCache;

/// 名字/等级/职业 的字色（原版 `clWhite` + `clBlack` 描边，`IntroScn.pas:1519`）。
const C_TEXT: Rgb = (255, 255, 255);
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
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    None,
    /// 进游戏（带上选中的角色 id）。
    Enter(u64),
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
    down: Option<Hit>,
    last: Instant,
    /// 点过开始了（进世界后由主循环切场景）。
    pub start_clicked: bool,
}

impl Select {
    pub fn new(chars: Vec<CharEntry>) -> Self {
        let anims = std::array::from_fn(|i| {
            let (job, sex) = chars.get(i).map_or((0, 0), |c| (c.job(), c.sex));
            su::SlotAnim::new(job, sex)
        });
        Self {
            chars,
            picked: 0,
            anims,
            menu_cursor: 0,
            msg: None,
            down: None,
            last: Instant::now(),
            start_clicked: false,
        }
    }

    /// 当前选中的角色（空槽 ⇒ `None`）。
    pub fn picked_char(&self) -> Option<&CharEntry> {
        self.chars.get(self.picked)
    }

    /// 弹一条提示（错误/占位说明都走它）。
    pub fn say(&mut self, what: impl Into<String>) {
        self.msg = Some(what.into());
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

    pub fn on_down(&mut self, p: (f32, f32), l: &su::Layout) {
        self.down = self.hit_at(p, l);
    }

    /// 抬起：只有"按下与抬起在同一颗"才算点中。
    pub fn on_up(&mut self, p: (f32, f32), l: &su::Layout) -> Action {
        let hit = self.hit_at(p, l);
        let same = hit.is_some() && hit == self.down;
        self.down = None;
        if !same {
            return Action::None;
        }
        // 弹窗开着时：点哪儿都是"关掉它"（原版 `DMessageDlg` 是模态的）
        if self.msg.take().is_some() {
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
                su::Menu::CreateChar => {
                    self.say(
                        "新协议的建角还没接（服务端 `CreateCharacter` 未实现）。\
                         现在用 `mir2cli -new-char 名字 -job 0|1|2` 建，见 README「本地跑起来」。",
                    );
                    Action::None
                }
                su::Menu::DeleteChar => {
                    self.say(
                        "新协议的删角还没接（服务端 `DeleteCharacter` 未实现）。\
                         原版这里会先弹一个「删了不可恢复」的确认框。",
                    );
                    Action::None
                }
                su::Menu::Credits => {
                    self.say("这套素材这里写的是「制作群」（原版那个位置是 credits）。未接。");
                    Action::None
                }
                su::Menu::Exit => Action::Exit,
            },
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
            Some(id) => {
                self.start_clicked = true;
                Action::Enter(id)
            }
            None => {
                // 原版那句提示（`IntroScn.pas:1198-1205`）+ 我们的现状
                self.say(
                    "一开始你应该创建一个新角色。如果你选择了<创建人物>还进入不了游戏，\
                     那说明你还没有创建角色。\n（现状：新协议的建角还没接，\
                     先用 `mir2cli -new-char 名字` 建一个。）",
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
            return text.draw(
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
            return text.draw(
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
        ui.draw(
            canvas,
            tc,
            dir,
            su::Art::BG.0,
            su::Art::BG.1,
            l.bg.0,
            l.bg.1,
        );

        // 这一帧的动画（dt 用真实时间；`tick` 内部按 300/50ms 进帧）
        let dt = now.saturating_duration_since(self.last).as_millis() as u32;
        self.last = now;

        for slot in 0..su::Art::SLOTS {
            let Some(c) = self.chars.get(slot).cloned() else {
                continue; // 空槽：什么都不画
            };
            let f = self.anims[slot].tick(dt, slot == self.picked);
            let anchor = su::slot_anchor(slot, l.bg);
            // ⚠️ 按**不透明包围盒**的底边中点落到凹槽上：人物图的透明边各不相同，
            // 按整图对齐会让不同职业/性别的小人高低不一。
            if let Some(bbox) = ui.bbox(dir, su::Art::CHR, f.index) {
                let (sx, sy) = su::place_sprite(bbox, anchor);
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
            // 解冻时叠一层选中光效（原版 `DrawBlend`，`IntroScn.pas:1442`）
            if let Some(e) = f.effect {
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::CHR,
                    e,
                    anchor.0 - 30.0,
                    anchor.1 - 60.0,
                    C_SEL_HL,
                );
            }
            // 名字 / 等级 / 职业（白字黑边，写在面板的字段框里）
            let t = su::slot_text(slot, l.bg);
            text.draw(
                canvas,
                tc,
                &c.name,
                t.name.0,
                t.name.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw(
                canvas,
                tc,
                &c.level.to_string(),
                t.level.0,
                t.level.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw(
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

        // 弹窗
        if let Some(m) = self.msg.clone() {
            draw_msgbox(canvas, tc, ui, text, dir, win, &m)?;
        }
        Ok(())
    }
}

/// 消息框（`Prguse[360]` + `[363]` 的 [Ok]，居中）—— 与登录界面同一套素材。
///
/// ⚠️ `login.rs` 里还有一份自己的实现（那里的弹窗要参与它的命中测试）。
/// 两处画的是同一个框，等选角稳定后应该收成一份 —— 记在这里免得漏掉。
fn draw_msgbox<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    text: &mut TextCache<'a>,
    dir: &Path,
    win: (u32, u32),
    msg: &str,
) -> Result<(), sdl3::Error> {
    let Some((mw, mh)) = ui.size(dir, "Prguse", 360) else {
        return Ok(());
    };
    let (bx, by) = (
        (win.0 as f32 - mw as f32) / 2.0,
        (win.1 as f32 - mh as f32) / 2.0,
    );
    ui.draw(canvas, tc, dir, "Prguse", 360, bx, by);
    if let Some((ow, oh)) = ui.size(dir, "Prguse", 363) {
        ui.draw(
            canvas,
            tc,
            dir,
            "Prguse",
            363,
            bx + (mw as f32 - ow as f32) / 2.0,
            by + mh as f32 - oh as f32 - 8.0,
        );
    }
    let cols = ((mw as f32 - 24.0) / text.width("中").max(1.0)) as usize;
    let mut y = by + 20.0;
    for line in wrap(msg, cols.max(8)) {
        text.draw(
            canvas,
            tc,
            &line,
            bx + 12.0,
            y,
            (255, 255, 255),
            Some((0, 0, 0)),
        )?;
        y += text.line_height() + 2.0;
    }
    Ok(())
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

        // 创建人物：弹窗里要提到 mir2cli（不然用户只能干等）
        s.on_key(Keycode::Down);
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        let m = s.msg.clone().expect("该弹提示");
        assert!(m.contains("mir2cli"), "{m}");
        s.on_key(Keycode::Return); // 关掉弹窗

        // 删除人物
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Return);
        assert!(s.msg.clone().unwrap().contains("DeleteCharacter"));

        // 制作群
        s.on_key(Keycode::Return);
        s.on_key(Keycode::Down);
        s.on_key(Keycode::Return);
        assert!(s.msg.clone().unwrap().contains("制作群"));

        // 退出
        s.on_key(Keycode::Return);
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
        assert!(m.contains("mir2cli"), "现状要说清楚：{m}");
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
        };
        let c = r.center();
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::None, "第一次点击只关弹窗");
        assert!(s.msg.is_none());
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::Enter(11));
    }

    /// 折行：按字符数、`\n` 硬断行。
    #[test]
    fn 折行() {
        assert_eq!(wrap("abc", 8), vec!["abc"]);
        assert_eq!(wrap("abcdefghij", 4), vec!["abcd", "efgh", "ij"]);
        assert_eq!(wrap("a\nb", 8), vec!["a", "b"]);
    }
}

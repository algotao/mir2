//! **选角场景**（原版 `IntroScn.pas` 的 `TSelectChrScene`）。
//!
//! 版式、图号公式与动画状态机都在 `mir2_core::select_ui`（纯计算、可单测）；
//! 这里只做"把状态画出来 + 把输入翻成动作"。
//!
//! # 与原版的四处差异（都是有意的）
//!
//! 1. **加了键盘**：原版选角场景**只认鼠标**（`TSelectChrScene` 没覆写
//!    `KeyDown/KeyPress`）。这里补了 ←→↑↓ 切换 + 回车进入 ——
//!    开发时不用摸鼠标，而且不改变鼠标的任何行为。
//! 2. **[新建角色] / [删除角色] 是诚实的占位**：新协议下服务端还没实现
//!    `CreateCharacter` / `DeleteCharacter`（`gamesvr` 的 dispatch 里没有这两条，
//!    会落到 `noteUnknown`）。**但这两颗按钮画在背景图里**
//!    （`Prguse[65]` 已经把按钮外观画上去了，66..72 只是按下态）⇒ 没法"不显示"。
//!    所以点它们会弹一条说明了现状的提示，而不是静默什么都不做。
//! 3. **空槽点了没反应**（原版会选中一个空槽、然后[开始游戏]发出一个空名字 ⇒
//!    被服务端拒掉）。这里直接挡住，代价是少一次能弹提示的机会 ——
//!    点[开始游戏]时照样会提示。
//! 4. 压暗过渡略去（理由见 `core::select_ui::SlotAnim` 的说明）。

use std::path::Path;
use std::time::Instant;

use sdl3::keyboard::Keycode;
use sdl3::render::{TextureCreator, WindowCanvas};

use mir2_core::select_ui as su;
use mir2_protocol as proto;

use crate::font::{Rgb, TextCache};
use crate::ui::UiCache;

/// 角色名与等级那一栏的字色（原版 `clWhite` + `clBlack` 描边，`IntroScn.pas:1519`）。
const C_TEXT: Rgb = (255, 255, 255);
const C_SHADOW: Rgb = (0, 0, 0);

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
            // 协议里 1=男 2=女；0（未指定）与脏值都按男画（宁可画默认性别，也不画错）
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

/// "按下的东西"（按下才画叠加图 —— 原版 `Downed` 的语义，`FState.pas:2693-2705`）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Hit {
    Slot(usize),
    Start,
    New,
    Del,
    Exit,
}

/// 选角场景的状态。
pub struct Select {
    pub chars: Vec<CharEntry>,
    /// 当前选中的槽（0/1）。初始不选（原版进入场景时两个都没选中）。
    picked: usize,
    anims: [su::SlotAnim; su::Art::SLOTS],
    /// 弹窗文本（原版 `DMessageDlg`）。`Some` 时挡住其它点击。
    pub msg: Option<String>,
    down: Option<Hit>,
    last: Instant,
    /// 进入世界后被推到的场景靠主循环切走，这里只记"已经点过开始了"。
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

    fn hit_at(&self, p: (f32, f32), l: &su::Layout) -> Option<Hit> {
        if l.start.hit(p) {
            return Some(Hit::Start);
        }
        if l.new.hit(p) {
            return Some(Hit::New);
        }
        if l.del.hit(p) {
            return Some(Hit::Del);
        }
        if l.exit.hit(p) {
            return Some(Hit::Exit);
        }
        for i in 0..su::Art::SLOTS {
            if l.slot_hot[i].hit(p) {
                return Some(Hit::Slot(i));
            }
        }
        None
    }

    pub fn on_down(&mut self, p: (f32, f32), l: &su::Layout) {
        self.down = self.hit_at(p, l);
    }

    /// 抬起：只有"按下与抬起在同一颗"才算点中（与登录界面同一条纪律）。
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
        match hit.expect("上面判过") {
            Hit::Slot(i) => {
                self.pick_slot(i);
                Action::None
            }
            Hit::Start => self.start(),
            Hit::New => {
                self.say(
                    "新协议的建角还没接（服务端 `CreateCharacter` 未实现）。\
                     现在用 `mir2cli -new-char 名字` 建，见 README「本地跑起来」。",
                );
                Action::None
            }
            Hit::Del => {
                self.say(
                    "新协议的删角还没接（服务端 `DeleteCharacter` 未实现）。\
                     原版这里会先弹一个「删了不可恢复」的确认框。",
                );
                Action::None
            }
            Hit::Exit => Action::Exit,
        }
    }

    /// 键盘（**我们的扩展**，原版没有 —— 见文件头第 1 条）。
    pub fn on_key(&mut self, k: Keycode) -> Action {
        if self.msg.is_some() {
            // 弹窗开着：任意键关掉（免得键盘用户被弹窗困住）
            self.msg = None;
            return Action::None;
        }
        match k {
            Keycode::Left | Keycode::Up => {
                self.move_pick(-1);
                Action::None
            }
            Keycode::Right | Keycode::Down => {
                self.move_pick(1);
                Action::None
            }
            Keycode::Return | Keycode::KpEnter | Keycode::Space => self.start(),
            _ => Action::None,
        }
    }

    /// 换槽（跳过空槽 —— 空槽本来就不能进游戏）。
    fn move_pick(&mut self, dir: i32) {
        let n = self.chars.len();
        if n == 0 {
            return;
        }
        for step in 1..=su::Art::SLOTS as i32 {
            let i = (self.picked as i32 + dir * step).rem_euclid(su::Art::SLOTS as i32) as usize;
            if i < n {
                self.pick_slot(i);
                return;
            }
        }
    }

    /// 按角色 id 预选（`MIR2_CHAR` 指的那个）。找不到就什么都不做。
    ///
    /// ⚠️ 手动选角之后，`MIR2_CHAR` 就不能再交给 `Entrance` 了（那条路只在
    /// 自动选角时看它）—— 所以这里把它落到界面的初始选中上，免得那个环境变量
    /// 悄悄失效。
    pub fn pick_id(&mut self, id: u64) {
        if let Some(i) = self.chars.iter().position(|c| c.id == id) {
            self.pick_slot(i);
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
        // ⚠️ 先取出 id 再改 `self`：直接 match `self.picked_char()` 会把不可变借
        // 拖进分支里，赋值 `start_clicked` 就借不过去了。
        let picked = self.picked_char().map(|c| c.id);
        match picked {
            Some(id) => {
                self.start_clicked = true;
                Action::Enter(id)
            }
            None => {
                // 原版那句提示（`IntroScn.pas:1198-1205`），后面接上我们的现状
                self.say(
                    "一开始你应该创建一个新角色。如果你选择了<创建角色>还进入不了游戏，\
                     那说明你还没有创建角色。\n（现状：新协议的建角还没接，\
                     先用 `mir2cli -new-char 名字` 建一个。）",
                );
                Action::None
            }
        }
    }

    /// 画一帧。`win` 是窗口尺寸（版式要按它把背景居中）。
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
            // 素材缺失就说清楚，而不是画一个歪掉的界面
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

        // 先算这一帧的动画（dt 用真实时间；`tick` 内部按 300/50ms 进帧）
        let dt = now.saturating_duration_since(self.last).as_millis() as u32;
        self.last = now;

        for i in 0..su::Art::SLOTS {
            let Some(c) = self.chars.get(i).cloned() else {
                continue; // 空槽：**什么都不画**（原版 `ChrArr[n].Valid` 也是这个意思）
            };
            let f = self.anims[i].tick(dt, i == self.picked);
            let at = su::slot_at(i, c.job(), c.sex, l.bg);
            let (sx, sy) = if f.at_frozen { at.frozen } else { at.stand };
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
            // 解冻时叠一层选中光效（原版 `DrawBlend`，`IntroScn.pas:1442`）
            if let Some(e) = f.effect {
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::CHR,
                    e,
                    at.effect.0,
                    at.effect.1,
                    (255, 255, 255),
                );
            }
            // 名字 / 等级 / 职业（白字黑边，位置逐项照原版）
            text.draw(
                canvas,
                tc,
                &c.name,
                at.name.0,
                at.name.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw(
                canvas,
                tc,
                &c.level.to_string(),
                at.level.0,
                at.level.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw(
                canvas,
                tc,
                su::class_name(c.class),
                at.class.0,
                at.class.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
        }

        // 按下态：**只在按住时**叠那张图（原版 `DscSelect1DirectPaint`）
        match self.down {
            Some(Hit::Slot(i)) => {
                let r = l.slot_hot[i];
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::SLOT_DOWN[i].0,
                    su::Art::SLOT_DOWN[i].1,
                    r.x,
                    r.y,
                    (255, 255, 255),
                );
            }
            Some(h) => {
                let (art, r) = match h {
                    Hit::Start => (su::Art::BTN_START, l.start),
                    Hit::New => (su::Art::BTN_NEW, l.new),
                    Hit::Del => (su::Art::BTN_DEL, l.del),
                    Hit::Exit => (su::Art::BTN_EXIT, l.exit),
                    Hit::Slot(_) => unreachable!("上面那支管了"),
                };
                ui.draw_tint(canvas, tc, dir, art.0, art.1, r.x, r.y, (255, 255, 255));
            }
            None => {}
        }

        // 弹窗（原版 `DMessageDlg`）
        if let Some(m) = self.msg.clone() {
            draw_msgbox(canvas, tc, ui, text, dir, win, &m)?;
        }
        Ok(())
    }
}

/// 消息框（`Prguse[360]` + `[363]` 的 [Ok]，居中）—— 与登录界面那套同一份素材。
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
    // 先量出框（尺寸来自素材）
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
    // 文字：按框宽折行（与登录界面同一条做法）
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

/// 按**像素宽**折行（`text.width` 给的是像素，中文比英文宽得多）。
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

    fn entry(id: u64, name: &str, class: i32, gender: proto::Gender) -> proto::CharacterSummary {
        proto::CharacterSummary {
            character_id: id,
            name: name.into(),
            class,
            gender: gender as i32,
            level: 7,
            ..Default::default()
        }
    }

    /// 摘要 → 界面条目：**职业与性别要翻对**（它们决定挑哪张小人与哪套坐标）。
    #[test]
    fn 摘要折算() {
        let c = CharEntry::from_summary(&entry(11, "勇士", 1, proto::Gender::Male));
        assert_eq!((c.id, c.job(), c.sex, c.level), (11, 0, 0, 7));
        assert_eq!(su::class_name(c.class), "战士");

        // 法师 + 女
        let c = CharEntry::from_summary(&entry(12, "小法", 2, proto::Gender::Female));
        assert_eq!((c.job(), c.sex), (1, 1));

        // 性别未指定（服务端没填/脏数据）⇒ 按男画，不崩
        let c = CharEntry::from_summary(&entry(13, "无名", 3, proto::Gender::Unspecified));
        assert_eq!((c.job(), c.sex), (2, 0), "道士 + 默认性别");
    }

    /// 选槽：点空槽没反应；换槽会**跳过空槽**（空槽进不了游戏）。
    #[test]
    fn 选槽与换槽() {
        let mut s = Select::new(vec![CharEntry::from_summary(&entry(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        // 只有一个角色（占槽 0）：左右都得停在 0
        s.move_pick(1);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));
        s.move_pick(-1);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));

        // 两个角色：能换到槽 1 再换回来
        let mut s = Select::new(vec![
            CharEntry::from_summary(&entry(11, "甲", 1, proto::Gender::Male)),
            CharEntry::from_summary(&entry(22, "乙", 2, proto::Gender::Female)),
        ]);
        s.move_pick(1);
        assert_eq!(s.picked_char().map(|c| c.id), Some(22));
        s.move_pick(-1);
        assert_eq!(s.picked_char().map(|c| c.id), Some(11));

        // 空槽：`pick_slot(1)` 在只有一个角色时不该选中任何东西
        let mut s = Select::new(vec![CharEntry::from_summary(&entry(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        s.pick_slot(1);
        assert_eq!(
            s.picked_char().map(|c| c.id),
            Some(11),
            "槽 1 是空的，点了不该选中"
        );
    }

    /// [开始游戏]：有角色 ⇒ 出 `Enter`；没有 ⇒ 弹提示（原版那句 + 我们的现状）。
    #[test]
    fn 开始与空账号提示() {
        let mut s = Select::new(vec![CharEntry::from_summary(&entry(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        match s.on_key(Keycode::Return) {
            Action::Enter(11) => {}
            other => panic!("期望 Enter(11)，实得 {other:?}"),
        }

        let mut s = Select::new(vec![]);
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        let m = s.msg.clone().expect("该弹提示");
        assert!(m.contains("创建"), "{m}");
        assert!(m.contains("mir2cli"), "现状要说清楚，别让人干等：{m}");
        // 弹窗开着时按键只关弹窗，不会又触发一次"开始"
        assert_eq!(s.on_key(Keycode::Return), Action::None);
        assert!(s.msg.is_none());
    }

    /// 弹窗是**模态**的：开着时点按钮先关它，不会连带把那一颗按下。
    #[test]
    fn 弹窗模态() {
        let mut s = Select::new(vec![CharEntry::from_summary(&entry(
            11,
            "甲",
            1,
            proto::Gender::Male,
        ))]);
        s.say("先看这个");
        // 造一个只含 [开始] 那颗的版式
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
            slot_hot: [empty, empty],
            start: r,
            new: empty,
            del: empty,
            exit: empty,
        };
        let c = r.center();
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::None, "第一次点击只关弹窗");
        assert!(s.msg.is_none());
        // 第二次才真的"开始"
        s.on_down(c, &l);
        assert_eq!(s.on_up(c, &l), Action::Enter(11));
    }

    /// 折行：中文按字符数、`\n` 硬断行。
    #[test]
    fn 折行() {
        assert_eq!(wrap("abc", 8), vec!["abc"]);
        assert_eq!(wrap("abcdefghij", 4), vec!["abcd", "efgh", "ij"]);
        assert_eq!(wrap("a\nb", 8), vec!["a", "b"]);
    }
}

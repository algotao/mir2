//! 底部操作面板（D-49）与聊天缓冲。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::path::Path;

use chrono::Timelike;
use sdl3::pixels::Color;
use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use crate::colors::C_HUD_TEXT;
use crate::gfx::trunc;
use crate::layout::{
    gauge_band, HUD_BOARD_1024, HUD_BOARD_1024_LIB, HUD_DIGIT0, HUD_EXP, HUD_ORB, HUD_ORB_SOLO,
    HUD_ORB_SOLO_FILL,
};
use crate::net::Net;
use crate::window::WIN_H;

use crate::{font, ui};

/// HUD 聊天区里的那几行（原版 `ChatStrs`：每行自带前景/背景色，`FState.pas:3868-3886`）。
///
/// ⚠️ 与终端里的 `println!` **不重复也不替代**：终端是给开发看的流水账，
/// 这里是给玩家看的"最近发生了什么"（有上限、旧的滚掉）。
/// 一行聊天：`(文字, 前景色, 背景色)`。
///
/// 背景色是原版 `AddChatBoardString(str, fcolor, bcolor)` 的第三个参数
///（`ClMain.pas:4822`：经验提示 `clWhite, clRed` ⇒ **红底白字**）。`None` = 不铺底
///（普通聊天行就是白底黑字，底是面板那块白框给的）。
pub(crate) type ChatLine = (String, Color, Option<Color>);

#[derive(Default)]
pub(crate) struct Chat {
    pub(crate) lines: std::collections::VecDeque<ChatLine>,
}

impl Chat {
    /// 推一行（不铺底）。超过 [`CHAT_CAP`] 就丢最旧的。
    pub(crate) fn push(&mut self, s: impl Into<String>, col: Color) {
        self.lines.push_back((s.into(), col, None));
        self.trim();
    }

    /// 推一行**带背景色**的（原版 `AddChatBoardString` 的第三个参数）。
    pub(crate) fn push_bg(&mut self, s: impl Into<String>, col: Color, bg: Color) {
        self.lines.push_back((s.into(), col, Some(bg)));
        self.trim();
    }

    fn trim(&mut self) {
        while self.lines.len() > CHAT_CAP {
            self.lines.pop_front();
        }
    }
}

/// 聊天区最多留几行（画出来的只有最后 [`CHAT_VIEW_LINES`] 行，多留些好回看）。
pub(crate) const CHAT_CAP: usize = 64;

/// 聊天区一屏几行（原版那块框高 105px ÷ 行距 13 ≈ 8）。
pub(crate) const CHAT_VIEW_LINES: usize = 8;

/// 底部操作面板（HUD）：面板 + 血/魔法球 + 等级 + 经验条 + 聊天行 + 地图名/坐标。
///
/// 版式与图号**照抄原版**（`FState.pas` 的 `TFrmDlg.DBottomDirectPaint`，引文见上面那组常量）。
/// 它画在**世界之上**（原版也是最后贴上去的），所以在 `draw_map_view` 末尾调。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_hud<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    names: &mut font::TextCache<'a>,
    // 界面字号（`UI_PX` 原生 14px）的文本缓存 —— 对话窗用它（世界里的名字用 `names`）
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    // 地图名（容器里的标题；空则退回服务端那个代号）
    map_title: &str,
    // NPC 对话窗卷到第几行（见 `dialog::dialog_max_scroll`）
    dialog_scroll: usize,
    // 控件引擎（hover / 按下都由它算；规范见 `widget` 模块头）
    widget_ui: &crate::widget::Ui,
    // 鼠标在**画布**里的位置（经验条/负重条的悬停提示要按它判）
    mouse: (f32, f32),
) -> Result<(), sdl3::Error> {
    // ★ 底栏布局（D-134）：`hud_panel` 是 parent，孩子坐标经 get() 解析成画布坐标。
    let lay = crate::layout::hud_layout();
    let panel = lay
        .get(crate::layout::HUD_PANEL_ID)
        .expect("底栏 parent 必须存在");
    let orb = lay.get("orb").expect("血魔球应是底栏子控件");
    let plate_hp = lay.get("plate_hp").expect("HP 数值应是底栏子控件");
    let plate_mp = lay.get("plate_mp").expect("MP 数值应是底栏子控件");
    let level_num = lay.get("level_num").expect("等级应是底栏子控件");
    let exp_bar = lay.get("exp_bar").expect("经验条应是底栏子控件");
    let weight_bar = lay.get("weight_bar").expect("负重条应是底栏子控件");
    let atk_text = lay.get("atk_text").expect("攻击模式文字应是底栏子控件");
    let clock = lay.get("clock").expect("时钟应是底栏子控件");
    // ★ 底栏只有一个有效资源：清单要求的 `Prguse3[690]`（1024×251）。
    // 缺失已在启动预检时阻止运行；这里再用 expect 作最后一道断言，不拼接、不回退。
    let (bw, bh) = ui
        .size(dir, HUD_BOARD_1024_LIB, HUD_BOARD_1024)
        .expect("素材清单要求的 Prguse3[690] 底栏图必须存在");
    let (bw, bh) = (bw as f32, bh as f32);
    let (board_x, board_y) = (panel.x, panel.y);
    ui.draw_src(
        canvas,
        tc,
        dir,
        HUD_BOARD_1024_LIB,
        HUD_BOARD_1024,
        FRect::new(0.0, 0.0, bw, bh),
        FRect::new(board_x, board_y, bw, bh),
        255,
    );

    // **底栏右上那 4 个功能按钮**（人物/包裹/技能/选项，图号 8-11）：
    // 常态就在底栏 `Prguse[1]` 里 ⇒ 只在**悬停**时把高亮图叠上去。
    //
    // ⚠️⚠️ **必须用 `draw_src`（画布坐标），不能用 `ui.draw`（设计坐标）**：
    // `ui.draw` 内部会把 x/y **再乘一次 `UI_SCALE`(1.28)**（`ui.rs:203-210`），
    // 而这里的矩形是按 **画布** 1024×768 算的 ⇒ 乘完就飞到画布外去了
    // （右上 4 颗 x 867→1110、左下 5 颗 y 621→795，全都看不见）
    // —— 2026-10-11 用户报"完全看不到"就是这个。底栏/球/等级一路也都是画布坐标。

    for b in crate::layout::HudBtn::ALL {
        // 常态画在底栏里 ⇒ 只有高亮（按下）图是单独的
        let btn = crate::widget::Button {
            key: crate::Press::Hud(b),
            rect: b.rect(),
            art: crate::widget::ButtonArt::highlight_only(("Prguse", b.img())),
            enabled: true,
        };
        crate::widget::draw_button(canvas, tc, ui, dir, &btn, &btn.response(widget_ui));
    }
    // **底栏左下那排快捷按钮**（小地图/交易/行会/组队/属性/注销/退出）：两态按钮
    // （常态 = `img`、按下 = `img+1`；官方 `DBotGroupDirectPaint`）⇒ **常态总是画**，
    // 悬停时换成 +1 那张（与右上那 4 颗"只在悬停叠高亮"不是同一种）。

    for b in crate::layout::QuickBtn::ALL {
        // 左下这排是**两态**按钮：常态图 + 按下图(+1)（官方 `DBotGroupDirectPaint`）
        let btn = crate::widget::Button {
            key: crate::Press::Quick(b),
            rect: b.rect(),
            art: crate::widget::ButtonArt::two_state(("Prguse", b.img()), ("Prguse", b.img() + 1)),
            enabled: true,
        };
        crate::widget::draw_button(canvas, tc, ui, dir, &btn, &btn.response(widget_ui));
    }

    // 血 / 魔法球。官方分**两支**（`FState.pas:3606-3640`）：
    //
    //   ① **武士 且 等级 < 28** ⇒ `Prguse[5]` 球底 + `Prguse[6]` 血条，**只有血量**，
    //      落点 `(38, btop+90)`、源矩形右边界都 `-2`；
    //   ② 其余 ⇒ `Prguse[4]`：左半红 = HP、右半蓝 = MP，各自按比例从**下面**留一截。
    //
    // ⚠️ 我们多接一支：**`MaxMP = 0` 时也用单球**（官方那种情况什么都不画）。
    // 用户 2026-10-09 第 6 条："当职业当前状态只有血量时，显示为整球体，不显示魔量"。
    let (hp, max_hp) = net.map_or((0, 0), |n| n.world.self_hp.unwrap_or((0, 0)));
    let ability = net.and_then(|n| n.world.ability.as_ref());
    let (mp, max_mp) = ability.map_or((0, 0), |a| (a.mp, a.max_mp));
    // 武士 = `CharClass` 0（选角时记下来的，协议不带职业，见 `World::self_class`）
    let solo = (max_hp > 0 && max_mp == 0)
        || (net.and_then(|n| n.world.self_class) == Some(0)
            && ability.is_some_and(|a| a.level < 28)
            && max_hp > 0);
    if solo {
        let (ow, oh) = ui
            .size(dir, "Prguse", HUD_ORB_SOLO)
            .expect("清单必需图 Prguse[5] 单球底图缺失");
        let (ow, oh) = (ow as i32, oh as i32);
        let ball_y = orb.y;
        // 球底（原版 `rc.Right := d.ClientRect.Right - 2`）
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB_SOLO,
            FRect::new(0.0, 0.0, (ow - 2) as f32, oh as f32),
            FRect::new(orb.x, ball_y, (ow - 2) as f32, oh as f32),
            255,
        );
        // 血条（同一套裁切：`rc.Top := Round(rc.Bottom / Max * (Max - Cur))`）
        let (top, h) = gauge_band(hp as f32 / max_hp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB_SOLO_FILL,
            FRect::new(0.0, top as f32, (ow - 2) as f32, h as f32),
            FRect::new(orb.x, ball_y + top as f32, (ow - 2) as f32, h as f32),
            255,
        );
    } else if max_hp > 0 && max_mp > 0 {
        let (ow, oh) = ui
            .size(dir, "Prguse", HUD_ORB)
            .expect("清单必需图 Prguse[4] 血魔球缺失");
        let (ow, oh) = (ow as i32, oh as i32);
        let half = ow / 2 - 1; // 原版：`rc.Right := d.ClientRect.Right div 2 - 1`
        let ball_y = orb.y;
        // HP：源矩形右边界砍到中线
        let (top, h) = gauge_band(hp as f32 / max_hp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB,
            FRect::new(0.0, top as f32, half as f32, h as f32),
            FRect::new(orb.x, ball_y + top as f32, half as f32, h as f32),
            255,
        );
        // MP：源矩形左边界从中线 +1 起，落点 x 也加它（原版 `40 + rc.Left`）
        let left = ow / 2 + 1;
        let (top, h) = gauge_band(mp as f32 / max_mp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB,
            FRect::new(left as f32, top as f32, (ow - 1 - left) as f32, h as f32),
            FRect::new(
                orb.x + left as f32,
                ball_y + top as f32,
                (ow - 1 - left) as f32,
                h as f32,
            ),
            255,
        );
    }

    // 球体下方的 HP / MP 数值（用户 2026-10-09 第 6 条）。
    //
    // ⚠️ 官方 1.76 的**底部面板**没有这两个数（只有人物状态窗口 `FState.pas:2866-2867`），
    // 这是照用户给的参考图加的：左 HP、右 MP，各自贴着自己那半边。
    if max_hp > 0 {
        // ★ 字的 Y **由板算出来**（板内垂直居中）—— 拖板/改板高，字都跟着走
        //（用户 2026-10-12："调整框的大小和 Y 时，其 Y 位置没有变化"）。
        // ⚠️ 曾经有独立的 `num_text_y`（"数值行"）项，它只是挡事的重复把手 ⇒ 已删。
        let text_y = crate::layout::num_text_y_of;
        // 数值不单独画背景：`Prguse3[690]` 的底栏已自带相应底纹（用户 2026-10-12）。
        // `plate_hp` / `plate_mp` 现在仅作为**文字布局框**，用于定位与垂直居中。
        let hp_s = format!("{hp}/{max_hp}");
        let (hp_x, hp_w) = (plate_hp.x, plate_hp.w);
        names.draw_centered(
            canvas,
            tc,
            &hp_s,
            hp_x + hp_w / 2.0,
            text_y(plate_hp),
            (C_HUD_TEXT.r, C_HUD_TEXT.g, C_HUD_TEXT.b),
            Some((0, 0, 0)),
        )?;

        // MP：板接在 HP 那块右边
        if max_mp > 0 {
            let mp_s = format!("{mp}/{max_mp}");
            let (mp_x, mp_w) = (plate_mp.x, plate_mp.w);
            names.draw_centered(
                canvas,
                tc,
                &mp_s,
                mp_x + mp_w / 2.0,
                text_y(plate_mp),
                (C_HUD_TEXT.r, C_HUD_TEXT.g, C_HUD_TEXT.b),
                Some((0, 0, 0)),
            )?;
        }
    }

    // 等级：原版 `PomiTextOut` —— 数字图（`Prguse[30..39]`，12×10）**每 8px 一位**，
    // 且**第一位画在 `x + 8`**（`for i := 1 to Length(str)`），这里照做。
    let level = net
        .and_then(|n| n.world.ability.as_ref().map(|a| a.level))
        .unwrap_or(1);
    let (dw, dh) = ui
        .size(dir, "Prguse", HUD_DIGIT0)
        .expect("清单必需图 Prguse[30] 等级数字缺失");
    let level_at = (level_num.x, level_num.y);
    for (i, ch) in level.to_string().chars().enumerate() {
        let Some(d) = ch.to_digit(10) else { continue };
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_DIGIT0 + d,
            FRect::new(0.0, 0.0, dw as f32, dh as f32),
            FRect::new(
                level_at.0 + (i as f32 + 1.0) * 8.0,
                level_at.1,
                dw as f32,
                dh as f32,
            ),
            255,
        );
    }

    // 经验条 / 负重条：**同一张** `Prguse[7]`（76×13），按 `当前/最大` 裁源矩形右边界、
    // 从左往右填 —— 逐字照原版（`FState.pas:3646-3671`，800 版落点 `(666, H-73)` 与
    // `(666, H-40)`）：
    //
    //   if MaxExp > 0 then r := MaxExp / Exp;  rc.Right := Round(rc.Right / r)
    //
    // ⚠️ 原版把两条一起挂在 `if (MaxExp > 0) and (MaxWeight > 0)` 下（同一段 copy-paste
    // 味儿的写法）；这里**每条自己判**（`Max<=0` 或 `Cur<=0` 就不画那一条）。实战一致：
    // 1 级的 MaxExp 是 100、MaxWeight 也 > 0。
    if let Some(a) = net.and_then(|n| n.world.ability.as_ref()) {
        draw_prop_bar(
            canvas,
            ui,
            tc,
            dir,
            a.exp,
            a.max_exp,
            (exp_bar.x, exp_bar.y),
        );
        draw_prop_bar(
            canvas,
            ui,
            tc,
            dir,
            a.weight,
            a.max_weight,
            (weight_bar.x, weight_bar.y),
        );
        // **悬停提示**（用户 2026-10-12 第 8/9 条）：鼠标压在哪条上，就在它**下方**
        // 写一行 —— 原版是 `TFrmDlg` 里那两条 `SetDlgItemText`，落点也是条下面。
        let (bar_w, bar_h) = ui
            .size(dir, "Prguse", HUD_EXP)
            .expect("清单必需图 Prguse[7] 经验/负重条缺失");
        for (at, s) in [
            (
                (exp_bar.x, exp_bar.y),
                format!("当前经验 {} {}%", a.exp, percent(a.exp, a.max_exp)),
            ),
            (
                (weight_bar.x, weight_bar.y),
                format!("包裹负重{}/{}", a.weight, a.max_weight),
            ),
        ] {
            let hit = mouse.0 >= at.0
                && mouse.0 < at.0 + bar_w as f32
                && mouse.1 >= at.1
                && mouse.1 < at.1 + bar_h as f32;
            if hit {
                names.draw(
                    canvas,
                    tc,
                    &s,
                    at.0,
                    at.1 + bar_h as f32 + 1.0,
                    (255, 255, 255),
                    Some((0, 0, 0)),
                )?;
            }
        }
    }

    // **攻击模式 + 当前时间**（底栏右块，用户 2026-10-12 第 5 条给的参考图：
    // 右下角有 `[全体攻击]`、等级、经验、负重、时钟）。等级与两条上面已经画了；
    // 这里补缺的两样。
    //
    // - 攻击模式：服务端是权威，切模式走 Ctrl+H ⇒ `@atkmode`，模式名从服务端的
    //   系统提示同步回来（见 `net::Net::attack_mode` 的说明）。
    // - 时钟：本地当前时间（参考图是 GOM 引擎后加的，原版 1.76 没有 ⇒ 位置照
    //   参考图摆在负重条下面）。
    let mode = net.map_or(0, |n| n.attack_mode.min(crate::ATTACK_MODE_NAMES.len() - 1));
    names.draw(
        canvas,
        tc,
        &format!("[{}]", crate::ATTACK_MODE_NAMES[mode]),
        atk_text.x,
        atk_text.y,
        (C_HUD_TEXT.r, C_HUD_TEXT.g, C_HUD_TEXT.b),
        Some((0, 0, 0)),
    )?;
    {
        let now = chrono::Local::now();
        names.draw(
            canvas,
            tc,
            &format!("{:02}:{:02}:{:02}", now.hour(), now.minute(), now.second()),
            clock.x,
            clock.y,
            (C_HUD_TEXT.r, C_HUD_TEXT.g, C_HUD_TEXT.b),
            Some((0, 0, 0)),
        )?;
    }

    // 聊天行：原版是"每行自带背景色、OPAQUE 画"（`FState.pas:3868-3886`）——
    // 所以面板上那块浅色框只是**垫底**，真正的深底是文字自己铺出来的。
    // 我们铺一次深底 + 逐行前景色，效果一致。
    if let Some(n) = net {
        let chat = &n.chat;
        let shown = chat.lines.len().min(CHAT_VIEW_LINES);
        if shown > 0 {
            let skip = chat.lines.len() - shown;
            // 1024 底栏 `Prguse3[690]` 的这块**不是纯白**；聊天视口由控件布局定义，
            // 始终先铺白底，避免文字压在石纹背景上。无素材回退已移除，见启动预检。
            let (bx, by, bw, bh) = (
                lay.get("chat_box").expect("聊天框应是底栏子控件").x,
                lay.get("chat_box").expect("聊天框应是底栏子控件").y,
                lay.get("chat_box").expect("聊天框应是底栏子控件").w,
                lay.get("chat_box").expect("聊天框应是底栏子控件").h,
            );
            canvas.set_draw_color(Color::RGB(255, 255, 255));
            canvas.fill_rect(FRect::new(bx, by, bw, bh))?;
            // 文字**贴着白框底边往上排**（新的一行在最下面）—— 原来固定从框顶
            // 开始 ⇒ 只有一两行时看着"飘在上面"（用户 2026-10-11：位置偏上）。
            for (i, (line, col, bg)) in chat.lines.iter().skip(skip).enumerate() {
                let y = crate::layout::chat_line_y(i, shown);
                // 带底色的行（原版 `AddChatBoardString` 的 `bcolor`）：先铺一条**文字宽**
                // 的色带再画字 —— `Prguse[1]` 那块是纯白聊天区，红底就是这么来的
                //（用户 2026-10-12 第 6 条：经验增加"红色背景白色字"）。
                if let Some(bg) = bg {
                    let w = names.width(line) + 4.0;
                    canvas.set_draw_color(*bg);
                    canvas.fill_rect(FRect::new(
                        bx + 2.0,
                        y - 1.0,
                        w,
                        crate::layout::CHAT_LINE_H,
                    ))?;
                }
                names.draw(canvas, tc, line, bx + 4.0, y, (col.r, col.g, col.b), None)?;
            }
        }
    }

    // NPC 对话（有就画，压在所有东西之上）
    draw_dialog(canvas, tc, ui, texts, dir, net, dialog_scroll)?;

    // 左下角一行：`地图名 X : Y`（用户 2026-10-09 第 5 条，例 "银杏山谷 641 : 642"）。
    //
    // 官方出处：`DrawScrn.pas:513` 的
    // `BoldTextOut(MSurface, 8, SCREENHEIGHT-20, …, g_sMapTitle + ' ' + X + ':' + Y)`
    // —— 抬头是**服务端下发的地图描述**（`ClMain.pas:5215-5224 ClientGetMapDescription`）。
    // 所以顺序是：服务端的 `map_title`（官方机制）→ 容器里那张图的标题 → 地图号。
    // ⚠️ 分隔符按用户给的样例写成 `" : "`（官方源码里是紧贴的 `':'`，见上面引文）。
    if let Some(n) = net {
        if n.world.in_world() {
            let title = if !n.world.map_title.is_empty() {
                n.world.map_title.as_str()
            } else if !map_title.is_empty() {
                map_title
            } else {
                n.world.map_name.as_str()
            };
            names.draw(
                canvas,
                tc,
                &format!(
                    "{} {} : {}",
                    trunc(title, 12),
                    n.world.self_pos.0,
                    n.world.self_pos.1
                ),
                8.0,
                WIN_H as f32 - 20.0,
                (C_HUD_TEXT.r, C_HUD_TEXT.g, C_HUD_TEXT.b),
                Some((0, 0, 0)),
            )?;
        }
    }
    Ok(())
}

/// 百分比（整数，向下取整）；`max == 0` 时给 0 —— 只用在提示文案里。
fn percent(cur: u32, max: u32) -> u32 {
    if max == 0 {
        0
    } else {
        (cur as u64 * 100 / max as u64) as u32
    }
}

/// 画一条 `Prguse[7]` 的比例条（**经验条**与**负重条**同款图，原版也共用）。
///
/// 原版 `FState.pas:3646-3671` 的算法：
/// `rc.Right := Round(rc.Right / (Max / Cur))` ⇒ 可见宽度 = `W * Cur / Max`，
/// 源矩形从左边起、右边界裁掉 ⇒ **从左往右填**。`Cur`/`Max` 有 0 就不画。
fn draw_prop_bar<'a, T>(
    canvas: &mut WindowCanvas,
    ui: &mut ui::UiCache<'a>,
    tc: &'a TextureCreator<T>,
    dir: &Path,
    cur: u32,
    max: u32,
    at: (f32, f32),
) {
    if cur == 0 || max == 0 {
        return;
    }
    let (w, h) = ui
        .size(dir, "Prguse", HUD_EXP)
        .expect("清单必需图 Prguse[7] 经验/负重条缺失");
    // 整数算法：W * Cur / Max（原版是浮点除法 + Round，差最多一像素）。
    let fill = ((w as u64 * cur as u64) / max as u64).min(w as u64) as u32;
    if fill == 0 {
        return;
    }
    ui.draw_src(
        canvas,
        tc,
        dir,
        "Prguse",
        HUD_EXP,
        FRect::new(0.0, 0.0, fill as f32, h as f32),
        FRect::new(at.0, at.1, fill as f32, h as f32),
        255,
    );
}

// ---------- 背包窗（`Prguse[3]`，F9 开关）----------

/// 画面上的背包窗几何 `(窗口矩形, 当前页码)`（页码从 0 起）。
///
/// **画与点命中都用它**（与对话窗同一条纪律）。`shop_open`：商店开着时包裹
/// 让位到右边（见 `layout::bag_pos`）。
pub(crate) fn bag_geom(
    bag_len: usize,
    page: usize,
    shop_open: bool,
) -> ((f32, f32, f32, f32), usize) {
    let (x, y) = crate::layout::bag_pos(shop_open);
    let pages = crate::layout::bag_pages(bag_len);
    let page = page.min(pages - 1);
    ((x, y, crate::layout::BAG_W, crate::layout::BAG_H), page)
}

/// 画背包窗：背板 + 24 格物品图标 + 叠加数 + 金币 + 页码。
///
/// 图标取 `Items.wzl[looks]`（2026-10-09 用 `wzldump` 逐张比对确认：`looks=398` 是
/// 金创药(小量) 的红瓶、`394` 是魔法药(小量) 的蓝瓶，与服务端物品表的 `Looks` 列一致）。
/// **物品悬停提示**（名字 / 类型 / 数量 / 耐久 / 卖价）。
///
/// 鼠标压在包裹格上就出，跟在光标右下（出屏就翻到左上）。
/// 官方也是这么一块提示（`FState`/`PlayScn` 的 `ShowItemInfo`）；素材是
/// `Prguse[394]` 那块板，官方作者在状态窗里把它注释掉了 ⇒ 先照窗的风格自绘底板。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_item_tip<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    texts: &mut font::TextCache<'a>,
    mouse: (f32, f32),
    it: &mir2_core::world::BagItem,
) -> Result<(), sdl3::Error> {
    let lines = tip_lines(it);
    let pad = 8.0;
    let lh = 18.0;
    let mut w = 0.0f32;
    for l in &lines {
        w = w.max(texts.width(&l.0));
    }
    let w = w + pad * 2.0;
    let h = lines.len() as f32 * lh + pad * 2.0 - 4.0;
    // 光标右下；越界就翻到另一侧（别画到窗口外）
    let mut x = mouse.0 + 14.0;
    let mut y = mouse.1 + 16.0;
    if x + w > crate::window::WIN_W as f32 {
        x = (mouse.0 - w - 8.0).max(4.0);
    }
    if y + h > crate::window::WIN_H as f32 {
        y = (mouse.1 - h - 8.0).max(4.0);
    }

    canvas.set_blend_mode(BlendMode::Blend);
    canvas.set_draw_color(Color::RGBA(14, 12, 10, 236));
    canvas.fill_rect(FRect::new(x, y, w, h))?;
    canvas.set_draw_color(Color::RGB(150, 135, 100));
    canvas.draw_rect(FRect::new(x, y, w, h))?;
    canvas.set_blend_mode(BlendMode::None);

    for (i, (text, color)) in lines.iter().enumerate() {
        texts.draw(
            canvas,
            tc,
            text,
            x + pad,
            y + pad + i as f32 * lh,
            *color,
            Some((0, 0, 0)),
        )?;
    }
    Ok(())
}

/// 提示框的每一行（文本 + 颜色）。
fn tip_lines(it: &mir2_core::world::BagItem) -> Vec<(String, crate::font::Rgb)> {
    let mut out = Vec::new();
    out.push((it.name.clone(), (235, 225, 190)));
    out.push((format!("类别：{}", kind_name(it.std_mode)), (170, 170, 170)));
    if it.count > 1 {
        out.push((format!("数量：{}", it.count), (200, 200, 200)));
    }
    if it.dura_max > 0 {
        out.push((
            format!("耐久：{} / {}", it.dura, it.dura_max),
            (200, 200, 200),
        ));
    }
    out.push((format!("卖价：{} 金币", it.sell_price), (225, 190, 90)));
    out
}

/// `StdMode` → 类别名（原版 `ItemClickSound` 那张表也是按 StdMode 分的）。
fn kind_name(std_mode: u32) -> &'static str {
    match std_mode {
        0 => "药品",
        1 => "食物",
        2..=3 => "其他",
        4 => "技能书",
        5..=6 => "武器",
        10..=11 => "衣服",
        15 => "头盔",
        19..=21 => "项链",
        22..=23 => "戒指",
        24 | 26 => "手镯",
        25 | 30 => "其他",
        31 => "宝石",
        40 => "肉",
        41 => "金币",
        42..=43 => "矿石",
        _ => "物品",
    }
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_bag<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    page: usize,
    shop_open: bool,
    hide: &[usize],
    // 控件引擎（hover / 按下都由它算）
    widget_ui: &crate::widget::Ui,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let (rect, page) = bag_geom(n.world.bag.len(), page, shop_open);
    let (x, y, w, h) = rect;
    canvas.set_blend_mode(BlendMode::Blend);
    let (bg_w, bg_h) = ui
        .size(dir, "Prguse", crate::layout::BAG_BG)
        .expect("素材清单要求 Prguse[3] 背包窗背景");
    assert_eq!(
        (bg_w as f32, bg_h as f32),
        (w, h),
        "背包背景尺寸与窗口布局不一致"
    );
    ui.draw_src(
        canvas,
        tc,
        dir,
        "Prguse",
        crate::layout::BAG_BG,
        FRect::new(0.0, 0.0, bg_w as f32, bg_h as f32),
        FRect::new(x, y, w, h),
        255,
    );

    // **关闭钮走控件层**（`widget::Button`，素材 64 是**按下**态的高亮图；
    // 常态那个红叉印在底图 `Prguse[3]` 里 ⇒ 只在按下时叠）。
    let close_btn = crate::widget::Button {
        key: crate::Press::BagClose,
        rect: crate::widget::offset_local_to_canvas(
            (
                crate::layout::BAG_CLOSE_X,
                crate::layout::BAG_CLOSE_Y,
                crate::layout::BAG_CLOSE_W,
                crate::layout::BAG_CLOSE_H,
            ),
            crate::space::CPt(crate::space::Cpx(x), crate::space::Cpx(y)),
        ),
        art: crate::widget::ButtonArt::highlight_only(("Prguse", crate::layout::BAG_CLOSE_IMG)),
        enabled: true,
    };
    crate::widget::draw_button(
        canvas,
        tc,
        ui,
        dir,
        &close_btn,
        &close_btn.response(widget_ui),
    );
    canvas.set_blend_mode(BlendMode::None);

    // 24 格
    let base = page * crate::layout::BAG_PAGE_SLOTS;
    for i in 0..crate::layout::BAG_PAGE_SLOTS {
        let Some(Some(item)) = n.world.bag.get(base + i) else {
            continue;
        };
        // ⚠️ 抓在手上 / 放进卖窗圆槽的那件 ⇒ **原格子不画**（用户 2026-10-10：
        // 「拾取包裹物品后，应在包裹对应格子隐藏该物品」）。服务端没有中间态
        //（卖出才结算，见 `onShopSell`），这纯粹是客户端表现 —— 与原版一致：
        // 拿在手里时格子是空的，取消/卖出后才真正动背包。
        if hide.contains(&(base + i)) {
            continue;
        }
        let (cx, cy, cw, ch) = crate::layout::bag_cell_rect(i);
        let (cx, cy) = (x + cx, y + cy);
        // 图标：`Items.wzl[looks]`，在格子里居中。
        // ⚠️ 对齐照**原版** `TFrmDlg.DItemGridGridPaint`（FState.pas:4844-4845）：
        // 居中之后 **x 再 -1、y 再 +1**（用户 2026-10-12："整体偏上 2-5 像素"）。
        if let Some((iw, ih)) = ui.size(dir, "Items", item.looks) {
            let (iw, ih) = (iw as f32, ih as f32);
            ui.draw_src(
                canvas,
                tc,
                dir,
                "Items",
                item.looks,
                FRect::new(0.0, 0.0, iw, ih),
                FRect::new(
                    cx + (cw - iw) / 2.0 - 1.0,
                    cy + (ch - ih) / 2.0 + 1.0,
                    iw,
                    ih,
                ),
                255,
            );
        }
        // 叠加数（右下角）+ 名称只留一行到鼠标提示里（这里不画名字，格子太小）
        if item.count > 1 {
            texts.draw(
                canvas,
                tc,
                &item.count.to_string(),
                cx + cw - 16.0,
                cy + ch - 13.0,
                (255, 255, 140),
                Some((0, 0, 0)),
            )?;
        }
    }

    // 金币图标（原版 `DGold` 控件：`FState.pas:1255` `DGold.SetImgIndex(g_WMainImages,
    // 29)`、落点 (10,190) —— **核查过：原版不按金额换图**，全工程没有"数量大换
    // 金条/金盒"的逻辑；我们这套素材里同一张金币堆图就在 `Prguse[29]`，36×21）。
    // 2026-10-12 用户第 4 条：左下角圆圈要显示金币图标。
    if let Some((gw, gh)) = ui.size(dir, "Prguse", crate::layout::BAG_GOLD_ICON) {
        let (gw, gh) = (gw as f32, gh as f32);
        let (ix, iy) = crate::layout::BAG_GOLD_ICON_AT;
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            crate::layout::BAG_GOLD_ICON,
            FRect::new(0.0, 0.0, gw, gh),
            FRect::new(x + ix, y + iy, gw, gh),
            255,
        );
    }
    // 金额数字（服务端 `Ability.gold`）—— **只画数字**（原版横条左端自带金币图标；
    // 用户 2026-10-10 第 8 条：不要"金币"字样，数字也别贴着横条下沿）
    if let Some(ab) = n.world.ability {
        texts.draw(
            canvas,
            tc,
            &ab.gold.to_string(),
            x + crate::layout::BAG_GOLD_X,
            y + crate::layout::BAG_GOLD_Y,
            (255, 230, 130),
            Some((0, 0, 0)),
        )?;
    }
    // 页码（多于 1 页才画）
    let pages = crate::layout::bag_pages(n.world.bag.len());
    if pages > 1 {
        texts.draw(
            canvas,
            tc,
            &format!("{}/{}", page + 1, pages),
            x + crate::layout::BAG_W - 96.0,
            y + crate::layout::BAG_GOLD_Y,
            (210, 210, 210),
            Some((0, 0, 0)),
        )?;
    }
    Ok(())
}

// ---------- NPC 对话窗（用户 2026-10-09：点 NPC 要出对话；版式照官方截图改）----------

/// 对话窗的几何 + 折好的正文行 —— **画与点命中都用它**（两边一致，别各算一份）。
///
/// 返回 `(面板矩形, 正文行, **夹过的**滚动位置)`；没对话 ⇒ `None`。
///
/// 正文折行后**常常超出背板**（298 个脚本里 114 个超一屏）⇒ 超出的部分靠**滚动**
/// 看，不是截掉（截掉 = 「退出」点不到，见 `crate::dialog::dialog_max_scroll`）。
/// `dialog_geom` 的返回值：`(面板矩形, 正文行, 夹过的滚动位置)`。
///
/// 抽出来的理由：这个三元组在画与命中两条路上都要写一遍，嵌套的 `Vec<Vec<…>>`
/// 让签名一眼看不懂（clippy 也嫌它"very complex type"）。
pub(crate) type DialogGeom = (
    (f32, f32, f32, f32),
    Vec<Vec<crate::dialog::DialSeg>>,
    usize,
);

pub(crate) fn dialog_geom(net: Option<&Net>, scroll: usize) -> Option<DialogGeom> {
    let n = net?;
    let d = n.world.dialog.as_ref()?;
    // 正文切成"行 → 片段"：行内链接留在原行（用户 2026-10-09 要的「打开 交易市场」一行）；
    // 正文没有标记时把 `options` 排成底部列表（老服务端/简易脚本）。
    let lines = crate::dialog::dialog_lines(&d.text, &d.options);
    // 滚动位置**就地夹回**：换了一段更短的对话时，旧的偏移可能已经越界。
    let scroll = crate::dialog::dialog_scroll_clamp(scroll, lines.len());
    Some((crate::dialog::dialog_panel(), lines, scroll))
}

/// 画 NPC 对话窗：官方 `Prguse[384]` 背板（左上角、原生尺寸）+ 正文 + 可点选项。
///
/// 文字样式照截图：正文近白、选项**黄色**且前面一个**绿点**（官方不编号，
/// 靠颜色认"这是能点的"）。
fn draw_dialog<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    scroll: usize,
) -> Result<(), sdl3::Error> {
    let Some((panel, lines, scroll)) = dialog_geom(net, scroll) else {
        return Ok(());
    };
    let (x, y, w, h) = panel;
    canvas.set_blend_mode(BlendMode::Blend);
    // 背板 `Prguse[384]` 是启动必需素材；缺图由 preflight 阻止，不用自绘兜底遮盖错误。
    let (bg_w, bg_h) = ui
        .size(dir, "Prguse", crate::dialog::DIALOG_BG)
        .expect("素材清单要求 Prguse[384] NPC 对话窗背板");
    ui.draw_src(
        canvas,
        tc,
        dir,
        "Prguse",
        crate::dialog::DIALOG_BG,
        FRect::new(0.0, 0.0, bg_w as f32, bg_h as f32),
        FRect::new(x, y, w, h),
        255,
    );
    canvas.set_blend_mode(BlendMode::None);
    // 正文 + **行内可点文字**。先排版（只读字体量宽）、再落笔（可变借用）—— 两段借用分开。
    // ⚠️ 只排**看得见**的那些行（滚动窗口内的行号从 0 起）—— 直接拿 `lines` 的下标
    // 当行号的话，滚到第 9 行之后所有行都会落在窗外的 y 上（画不出来也点不到）。
    let layout: Vec<Vec<crate::dialog::DialPiece>> = {
        let mut measure = |t: &str| texts.width(t);
        crate::dialog::dialog_visible_lines(&lines, scroll)
            .map(|(row, segs)| crate::dialog::dialog_line_pieces(panel, row, segs, &mut measure))
            .collect()
    };
    for pieces in &layout {
        for p in pieces {
            let (rx, ry, _rw, rh) = p.rect;
            match &p.seg {
                crate::dialog::DialSeg::Text(t) => {
                    texts.draw(
                        canvas,
                        tc,
                        t,
                        p.text_x,
                        ry,
                        (238, 238, 214),
                        Some((0, 0, 0)),
                    )?;
                }
                crate::dialog::DialSeg::Link { text, .. } => {
                    // 官方样式：绿方块 + 黄字（方块也算在这片的命中矩形里）
                    canvas.set_draw_color(Color::RGB(90, 205, 90));
                    canvas.fill_rect(FRect::new(rx + 1.0, ry + rh / 2.0 - 2.5, 5.0, 5.0))?;
                    texts.draw(
                        canvas,
                        tc,
                        text,
                        p.text_x,
                        ry + 1.0,
                        (232, 220, 96),
                        Some((0, 0, 0)),
                    )?;
                }
            }
        }
    }
    // 滚动条：正文装不下时才画（298 个脚本里 114 个超一屏 ⇒ 「退出」常常落在第 9 行以后）。
    // 光有一块高背板解决不了：最长的对话折行后有 **46 行**（`dialog::dialog_max_scroll`）。
    if let Some((bx, by, bw, bh)) = crate::dialog::dialog_scroll_thumb(panel, lines.len(), scroll) {
        let (tx, ty, tw, th) = crate::dialog::dialog_bar_track(panel);
        canvas.set_draw_color(Color::RGB(52, 44, 32));
        canvas.fill_rect(FRect::new(tx, ty, tw, th))?;
        canvas.set_draw_color(Color::RGB(198, 176, 112));
        canvas.fill_rect(FRect::new(bx, by, bw, bh))?;
    }
    Ok(())
}

// ---------- 退出 / 注销 的确认框 ----------
//
// ⚠️ 官方这两个按钮**没有确认框**（`DBotExitClick` 直接 `AppExit`，
// `FState.pas:5789-5803`；只有"战斗中不能退出"那道守卫）—— 但点一下就把游戏
// 关了，误触代价太大 ⇒ 我们加一道确认。素材全用官方现成的：
// 消息框 `Prguse[360]`（452×179）+ [确定] 363（80×34）+ [取消] 52（96×33）。

/// 确认框的几何（框 + [确定] + [取消]）。
#[derive(Debug, Clone, Copy)]
pub(crate) struct Confirm {
    pub frame: mir2_core::select_ui::Rect,
    pub ok: mir2_core::select_ui::Rect,
    pub cancel: mir2_core::select_ui::Rect,
}

/// 量确认框几何；三张图都是启动清单必需素材，缺失由预检/expect 报错，禁止自绘或跳过。
pub(crate) fn confirm_geom(ui: &mut ui::UiCache, dir: &Path, win: (u32, u32)) -> Confirm {
    let frame = ui
        .size(dir, "Prguse", 360)
        .expect("清单要求 Prguse[360] 确认框");
    let ok = ui
        .size(dir, "Prguse", 363)
        .expect("清单要求 Prguse[363] 确认按钮");
    let cancel = ui
        .size(dir, "Prguse", 52)
        .expect("清单要求 Prguse[52] 取消按钮");
    let b = mir2_core::select_ui::MsgBox::build(win, frame, ok);
    // [确定] 那颗是**居中**的 ⇒ [取消] 摆在它左边，两颗合起来看着才居中
    let (cw, ch) = (cancel.0 as f32, cancel.1 as f32);
    let gap = 12.0;
    Confirm {
        frame: b.frame,
        ok: b.ok,
        cancel: mir2_core::select_ui::Rect {
            x: b.ok.x - gap - cw,
            y: b.ok.y + (b.ok.h - ch) / 2.0,
            w: cw,
            h: ch,
        },
    }
}

/// 画确认框（框 + 一句话 + [确定]/[取消]）。
pub(crate) fn draw_confirm<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    win: (u32, u32),
    msg: &str,
) -> Result<(), sdl3::Error> {
    let c = confirm_geom(ui, dir, win);
    // ⚠️ 同 `hud_img` 那条：这里的框/按钮都在**画布坐标** ⇒ 走 `draw_src`，
    // 不能用 `ui.draw`（它内部还会再乘 `UI_SCALE`）。
    hud_img_rect(canvas, tc, ui, dir, 360, c.frame);
    if c.ok.w > 0.0 {
        hud_img_rect(canvas, tc, ui, dir, 363, c.ok);
    }
    if c.cancel.w > 0.0 {
        hud_img_rect(canvas, tc, ui, dir, 52, c.cancel);
    }
    let w = texts.width(msg);
    texts.draw(
        canvas,
        tc,
        msg,
        c.frame.x + (c.frame.w - w) / 2.0,
        c.frame.y + 48.0,
        (255, 255, 255),
        Some((0, 0, 0)),
    )?;
    Ok(())
}

/// 在**画布坐标**的 `r` 处贴一张界面图（**按 `r` 的尺寸**，不按原生尺寸）。
fn hud_img_rect<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    dir: &Path,
    idx: u32,
    r: mir2_core::select_ui::Rect,
) {
    let (iw, ih) = ui
        .size(dir, "Prguse", idx)
        .unwrap_or_else(|| panic!("素材清单内 Prguse[{idx}] 缺失；禁止静默跳过 HUD 控件"));
    let _ = ui.draw_src(
        canvas,
        tc,
        dir,
        "Prguse",
        idx,
        FRect::new(0.0, 0.0, iw as f32, ih as f32),
        FRect::new(r.x, r.y, r.w, r.h),
        255,
    );
}

/// 画**快捷（腰带）栏**的 6 格：框是底栏 `Prguse[1]` 自带的 ⇒ 这里只画
/// **物品图标（居中）** 与格号 1..6（原版 `DBelt1DirectPaint`，`FState.pas:4010-4027`）。
///
/// ⚠️ 数据是客户端本地的（官方 `g_ItemArr`）⇒ 只认 `BeltSlot` 里那两个字段，
/// 物品真身始终在背包里；背包重排后由调用方把失效的格清掉。
#[allow(clippy::too_many_arguments)]
/// **技能栏**（F1..F8 那 8 格）：画在底栏之上，每格显示绑上去的技能图标。
///
/// ⚠️ 原版 1.76 底栏**没有**这一排（技能只在 F1..F8 与技能窗里）；这是照用户
/// 2026-10-12 给的参考图补的。图标取 `MagIcon.wzl[icon]`（`icon` 由服务端下发
/// = `effect * 2`），按下态是 `icon + 1`（原版 FState.pas:3655-3662）。
pub(crate) fn draw_skill_bar<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    // 当前按住的是第几格（按下反馈，与其它控件同一条规矩）
    held: Option<usize>,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    for i in 0..crate::layout::SKILL_SLOTS {
        let r = crate::layout::skill_slot_rect(i);
        let (x, y, w, h) = (r.0 .0, r.1 .0, r.2 .0, r.3 .0);
        // 格子底（半透明深底 + 石色边框）—— 素材里没有技能栏的板，自绘一块
        canvas.set_blend_mode(BlendMode::Blend);
        canvas.set_draw_color(Color::RGBA(16, 14, 12, 200));
        canvas.fill_rect(FRect::new(x, y, w, h))?;
        canvas.set_draw_color(if held == Some(i) {
            Color::RGB(255, 236, 140)
        } else {
            Color::RGB(120, 108, 82)
        });
        canvas.draw_rect(FRect::new(x, y, w, h))?;
        canvas.set_blend_mode(BlendMode::None);

        let key = b'1' as u32 + i as u32;
        let bound = n.world.magics.iter().find(|m| m.key == key);
        if let Some(m) = bound {
            // 按下时叠 `icon+1` 那张（原版按下态），常态用 `icon`
            let idx = if held == Some(i) { m.icon + 1 } else { m.icon };
            if m.icon != 0 {
                if let Some((iw, ih)) = ui.size(dir, crate::status::MAG_ICON_LIB, idx) {
                    let (iw, ih) = (iw as f32, ih as f32);
                    let _ = ui.draw_src(
                        canvas,
                        tc,
                        dir,
                        crate::status::MAG_ICON_LIB,
                        idx,
                        FRect::new(0.0, 0.0, iw, ih),
                        FRect::new(x + (w - iw) / 2.0, y + (h - ih) / 2.0, iw, ih),
                        255,
                    );
                }
            }
        }
        // 键名（F1..F8）
        texts.draw(
            canvas,
            tc,
            &format!("F{}", i + 1),
            x + 2.0,
            y + h - 11.0,
            (214, 200, 150),
            Some((0, 0, 0)),
        )?;
    }
    Ok(())
}

pub(crate) fn draw_belt<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    belt: &[Option<crate::layout::BeltSlot>; crate::layout::BELT_SLOTS],
    // 当前按住的是第几格（`None` = 没有/已拖出）
    held: Option<usize>,
) -> Result<(), sdl3::Error> {
    for (i, slot) in belt.iter().enumerate() {
        let r = crate::layout::belt_slot_rect(i);
        let (x, y, w, h) = (r.0 .0, r.1 .0, r.2 .0, r.3 .0);
        // 按住时给个浅色底（原版腰带格没有高亮图；这是我们自己加的按下反馈，
        // 与其它按钮同一条规矩：只在按住期间展现）
        if held == Some(i) {
            canvas.set_blend_mode(BlendMode::Blend);
            canvas.set_draw_color(sdl3::pixels::Color::RGBA(255, 236, 140, 46));
            let _ = canvas.fill_rect(FRect::new(x, y, w, h));
            canvas.set_blend_mode(BlendMode::None);
        }
        if let Some(b) = slot {
            if let Some((iw, ih)) = ui.size(dir, "Items", b.looks) {
                let (iw, ih) = (iw as f32, ih as f32);
                let _ = ui.draw_src(
                    canvas,
                    tc,
                    dir,
                    "Items",
                    b.looks,
                    FRect::new(0.0, 0.0, iw, ih),
                    FRect::new(x + (w - iw) / 2.0, y + (h - ih) / 2.0, iw, ih),
                    255,
                );
            }
        }
        // 格号（原版 `PomiTextOut` 画在格内）。
        //
        // ⚠️ 2026-10-12 用户：**写在每格的右下方、白字黑描边**（原来在左下方、米黄）
        // ⇒ 右对齐要按字宽回退（`texts.width`）。
        let num = format!("{}", i + 1);
        texts.draw(
            canvas,
            tc,
            &num,
            x + w - texts.width(&num) - 3.0,
            y + h - 12.0,
            (255, 255, 255),
            Some((0, 0, 0)),
        )?;
    }
    Ok(())
}

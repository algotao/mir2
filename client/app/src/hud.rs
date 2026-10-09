//! 底部操作面板（D-49）与聊天缓冲。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::path::Path;

use sdl3::pixels::Color;
use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use crate::colors::C_HUD_COORD;
use crate::gfx::trunc;
use crate::layout::{exp_at, gauge_band, hud_right_x, level_at, weight_at, CHAT_AT, CHAT_LINE_H, HUD_BOARD, HUD_DIGIT0, HUD_EXP, HUD_ORB, HUD_ORB_SOLO, HUD_ORB_SOLO_FILL, HUD_SIDE_W, ORB_AT, ORB_SOLO_AT, ORB_TEXT_DY, };
use crate::net::Net;
use crate::window::{WIN_H, WIN_W};

use crate::{font, ui};

/// HUD 聊天区里的那几行（原版 `ChatStrs`：每行自带前景/背景色，`FState.pas:3868-3886`）。
///
/// ⚠️ 与终端里的 `println!` **不重复也不替代**：终端是给开发看的流水账，
/// 这里是给玩家看的"最近发生了什么"（有上限、旧的滚掉）。
#[derive(Default)]
pub(crate) struct Chat {
    pub(crate) lines: std::collections::VecDeque<(String, Color)>,
}

impl Chat {
    /// 推一行。超过 [`CHAT_CAP`] 就丢最旧的。
    pub(crate) fn push(&mut self, s: impl Into<String>, col: Color) {
        self.lines.push_back((s.into(), col));
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
) -> Result<(), sdl3::Error> {
    let (bw, bh) = ui.size(dir, "Prguse", HUD_BOARD).unwrap_or((800, 251));
    let (bw, bh) = (bw as f32, bh as f32);
    // 原版：`btop := SCREENHEIGHT - d.height`
    let board_y = WIN_H as f32 - bh;
    // ⚠️ 一张 alpha 贴图就等价于原版那两刀（上半 120px 走色键、下半不透明）：
    // 我们的解码把调色板索引 0 当透明，上半正好是空的。
    //
    // 面板分三段贴（见 `HUD_SIDE_W`）：左右 1:1，中间那段拉宽到窗口宽 ——
    // 于是 1024 窗口下球和按钮都不变形，只有聊天框变宽。
    let mid_src_w = (bw - 2.0 * HUD_SIDE_W).max(1.0);
    let mid_dst_w = (WIN_W as f32 - 2.0 * HUD_SIDE_W).max(mid_src_w);
    for (sx, sw, dx, dw) in [
        (0.0, HUD_SIDE_W, 0.0, HUD_SIDE_W),             // 左：球那一块
        (HUD_SIDE_W, mid_src_w, HUD_SIDE_W, mid_dst_w), // 中：聊天框（拉宽）
        (bw - HUD_SIDE_W, HUD_SIDE_W, hud_right_x(), HUD_SIDE_W), // 右：按钮/状态
    ] {
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_BOARD,
            FRect::new(sx, 0.0, sw, bh),
            FRect::new(dx, board_y, dw, bh),
            255,
        );
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
        let (ow, oh) = ui.size(dir, "Prguse", HUD_ORB_SOLO).unwrap_or((92, 90));
        let (ow, oh) = (ow as i32, oh as i32);
        let ball_y = board_y + ORB_SOLO_AT.1;
        // 球底（原版 `rc.Right := d.ClientRect.Right - 2`）
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB_SOLO,
            FRect::new(0.0, 0.0, (ow - 2) as f32, oh as f32),
            FRect::new(ORB_SOLO_AT.0, ball_y, (ow - 2) as f32, oh as f32),
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
            FRect::new(ORB_SOLO_AT.0, ball_y + top as f32, (ow - 2) as f32, h as f32),
            255,
        );
    } else if max_hp > 0 && max_mp > 0 {
        let (ow, oh) = ui.size(dir, "Prguse", HUD_ORB).unwrap_or((92, 90));
        let (ow, oh) = (ow as i32, oh as i32);
        let half = ow / 2 - 1; // 原版：`rc.Right := d.ClientRect.Right div 2 - 1`
        let ball_y = board_y + ORB_AT.1;
        // HP：源矩形右边界砍到中线
        let (top, h) = gauge_band(hp as f32 / max_hp as f32, oh);
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            HUD_ORB,
            FRect::new(0.0, top as f32, half as f32, h as f32),
            FRect::new(ORB_AT.0, ball_y + top as f32, half as f32, h as f32),
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
                ORB_AT.0 + left as f32,
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
        let text_y = board_y + ORB_TEXT_DY;
        names.draw(
            canvas,
            tc,
            &format!("{hp}/{max_hp}"),
            ORB_SOLO_AT.0 - 26.0,
            text_y,
            (C_HUD_COORD.r, C_HUD_COORD.g, C_HUD_COORD.b),
            Some((0, 0, 0)),
        )?;
        if max_mp > 0 {
            names.draw(
                canvas,
                tc,
                &format!("{mp}/{max_mp}"),
                ORB_AT.0 + 36.0,
                text_y,
                (C_HUD_COORD.r, C_HUD_COORD.g, C_HUD_COORD.b),
                Some((0, 0, 0)),
            )?;
        }
    }

    // 等级：原版 `PomiTextOut` —— 数字图（`Prguse[30..39]`，12×10）**每 8px 一位**，
    // 且**第一位画在 `x + 8`**（`for i := 1 to Length(str)`），这里照做。
    let level = net
        .and_then(|n| n.world.ability.as_ref().map(|a| a.level))
        .unwrap_or(1);
    let (dw, dh) = ui.size(dir, "Prguse", HUD_DIGIT0).unwrap_or((12, 10));
    let level_at = level_at();
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
        draw_prop_bar(canvas, ui, tc, dir, a.exp, a.max_exp, exp_at());
        draw_prop_bar(canvas, ui, tc, dir, a.weight, a.max_weight, weight_at());
    }

    // 聊天行：原版是"每行自带背景色、OPAQUE 画"（`FState.pas:3868-3886`）——
    // 所以面板上那块浅色框只是**垫底**，真正的深底是文字自己铺出来的。
    // 我们铺一次深底 + 逐行前景色，效果一致。
    if let Some(n) = net {
        let chat = &n.chat;
        let shown = chat.lines.len().min(CHAT_VIEW_LINES);
        if shown > 0 {
            let skip = chat.lines.len() - shown;
            canvas.set_draw_color(Color::RGB(24, 24, 30));
            canvas.fill_rect(FRect::new(
                CHAT_AT.0 - 4.0,
                CHAT_AT.1 - 2.0,
                // 聊天框在中间那段里（面板那一段是被拉宽的 ⇒ 底色也跟着宽）
                (WIN_W as f32 - 2.0 * HUD_SIDE_W) - 12.0,
                shown as f32 * CHAT_LINE_H + 4.0,
            ))?;
            for (i, (line, col)) in chat.lines.iter().skip(skip).enumerate() {
                names.draw(
                    canvas,
                    tc,
                    line,
                    CHAT_AT.0,
                    CHAT_AT.1 + i as f32 * CHAT_LINE_H,
                    (col.r, col.g, col.b),
                    Some((0, 0, 0)),
                )?;
            }
        }
    }

    // NPC 对话（有就画，压在所有东西之上）
    draw_dialog(canvas, tc, ui, texts, dir, net)?;

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
                (C_HUD_COORD.r, C_HUD_COORD.g, C_HUD_COORD.b),
                Some((0, 0, 0)),
            )?;
        }
    }
    Ok(())
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
    let (w, h) = ui.size(dir, "Prguse", HUD_EXP).unwrap_or((76, 13));
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
/// **画与点命中都用它**（与对话窗同一条纪律）。
pub(crate) fn bag_geom(bag_len: usize, page: usize) -> ((f32, f32, f32, f32), usize) {
    let (x, y) = crate::layout::bag_rect();
    let pages = bag_len.div_ceil(crate::layout::BAG_PAGE_SLOTS).max(1);
    let page = page.min(pages - 1);
    (
        (x, y, crate::layout::BAG_W, crate::layout::BAG_H),
        page,
    )
}

/// 画背包窗：背板 + 24 格物品图标 + 叠加数 + 金币 + 页码。
///
/// 图标取 `Items.wzl[looks]`（2026-10-09 用 `wzldump` 逐张比对确认：`looks=398` 是
/// 金创药(小量) 的红瓶、`394` 是魔法药(小量) 的蓝瓶，与服务端物品表的 `Looks` 列一致）。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_bag<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    page: usize,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let (rect, page) = bag_geom(n.world.bag.len(), page);
    let (x, y, w, h) = rect;
    canvas.set_blend_mode(BlendMode::Blend);
    if ui.size(dir, "Prguse", crate::layout::BAG_BG).is_some() {
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            crate::layout::BAG_BG,
            FRect::new(0.0, 0.0, w, h),
            FRect::new(x, y, w, h),
            255,
        );
    } else {
        // 素材不在：画个深底 + 边框，版式不变（开发机上常见）
        canvas.set_blend_mode(BlendMode::Blend);
        canvas.set_draw_color(Color::RGBA(0, 0, 0, 215));
        canvas.fill_rect(FRect::new(x, y, w, h))?;
        canvas.set_draw_color(Color::RGB(190, 170, 120));
        canvas.draw_rect(FRect::new(x, y, w, h))?;
    }
    canvas.set_blend_mode(BlendMode::None);

    // 24 格
    let base = page * crate::layout::BAG_PAGE_SLOTS;
    for i in 0..crate::layout::BAG_PAGE_SLOTS {
        let Some(Some(item)) = n.world.bag.get(base + i) else {
            continue;
        };
        let (cx, cy, cw, ch) = crate::layout::bag_cell_rect(i);
        let (cx, cy) = (x + cx, y + cy);
        // 图标：`Items.wzl[looks]`，在格子里居中
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
                    cx + (cw - iw) / 2.0,
                    cy + (ch - ih) / 2.0,
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

    // 金币（服务端 `Ability.gold`）
    if let Some(ab) = n.world.ability {
        texts.draw(
            canvas,
            tc,
            &format!("金币 {}", ab.gold),
            x + crate::layout::BAG_GOLD_X,
            y + crate::layout::BAG_GOLD_Y,
            (255, 230, 130),
            Some((0, 0, 0)),
        )?;
    }
    // 页码（多于 1 页才画）
    let pages = n.world.bag.len().div_ceil(crate::layout::BAG_PAGE_SLOTS);
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
/// 返回 `(面板矩形, 正文行)`；没对话 ⇒ `None`。正文折行后**截到
/// [`crate::input::DIALOG_MAX_LINES`]**（背板是固定高的）。
pub(crate) fn dialog_geom(
    net: Option<&Net>,
) -> Option<((f32, f32, f32, f32), Vec<Vec<crate::input::DialSeg>>)> {
    let n = net?;
    let d = n.world.dialog.as_ref()?;
    // 正文切成"行 → 片段"：行内链接留在原行（用户 2026-10-09 要的「打开 交易市场」一行）；
    // 正文没有标记时把 `options` 排成底部列表（老服务端/简易脚本）。
    let lines = crate::input::dialog_lines(&d.text, &d.options);
    Some((crate::input::dialog_panel(), lines))
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
) -> Result<(), sdl3::Error> {
    let Some((panel, lines)) = dialog_geom(net) else {
        return Ok(());
    };
    let (x, y, w, h) = panel;
    canvas.set_blend_mode(BlendMode::Blend);
    // 背板：`Prguse[384]`（416×176，**原生尺寸不缩放**）。素材取不到就退回
    // "半透明黑底 + 描边"——版式不变，只是不好看（开发机上常见）。
    if ui.size(dir, "Prguse", crate::input::DIALOG_BG).is_some() {
        ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            crate::input::DIALOG_BG,
            FRect::new(0.0, 0.0, w, h),
            FRect::new(x, y, w, h),
            255,
        );
    } else {
        canvas.set_draw_color(Color::RGBA(0, 0, 0, 210));
        canvas.fill_rect(FRect::new(x, y, w, h))?;
        canvas.set_draw_color(Color::RGB(200, 180, 120));
        canvas.draw_rect(FRect::new(x, y, w, h))?;
    }
    canvas.set_blend_mode(BlendMode::None);
    // 正文 + **行内可点文字**。先排版（只读字体量宽）、再落笔（可变借用）—— 两段借用分开。
    let layout: Vec<Vec<crate::input::DialPiece>> = {
        let mut measure = |t: &str| texts.width(t);
        lines
            .iter()
            .enumerate()
            .map(|(i, segs)| crate::input::dialog_line_pieces(panel, i, segs, &mut measure))
            .collect()
    };
    for pieces in &layout {
        for p in pieces {
            let (rx, ry, _rw, rh) = p.rect;
            match &p.seg {
                crate::input::DialSeg::Text(t) => {
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
                crate::input::DialSeg::Link { text, .. } => {
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
    Ok(())
}

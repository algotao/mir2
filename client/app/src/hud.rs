//! 底部操作面板（D-49）与聊天缓冲。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::path::Path;

use sdl3::pixels::Color;
use sdl3::render::{FRect, TextureCreator, WindowCanvas};

use crate::colors::C_HUD_COORD;
use crate::gfx::trunc;
use crate::layout::{
    gauge_band, hud_right_x, level_at, CHAT_AT, CHAT_LINE_H, HUD_BOARD, HUD_DIGIT0, HUD_ORB,
    HUD_SIDE_W, ORB_AT,
};
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

    // 血 / 魔法球：左半红 = HP、右半蓝 = MP，各自按比例从**下面**留一截（`gauge_band`）
    let (hp, max_hp) = net.map_or((0, 0), |n| n.world.self_hp.unwrap_or((0, 0)));
    let (mp, max_mp) = net.map_or((0, 0), |n| {
        n.world
            .ability
            .as_ref()
            .map_or((0, 0), |a| (a.mp, a.max_mp))
    });
    if max_hp > 0 && max_mp > 0 {
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

    // 经验条：⚠️ **画不出来** —— 协议 `Ability` 里没有 exp/max_exp（原版 `SM_ABILITY` 有），
    // 服务端也没下发 ⇒ 这里如实留空（`HUD_EXP` / `EXP_AT` 先备着，见 D-49）。

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

    // 地图名 + 坐标（用户参考图里在左下角；原版 1.76 也在这块地方）
    if let Some(n) = net {
        if n.world.in_world() {
            let title = if map_title.is_empty() {
                n.world.map_name.as_str()
            } else {
                map_title
            };
            names.draw(
                canvas,
                tc,
                &format!(
                    "{}  {}:{}",
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

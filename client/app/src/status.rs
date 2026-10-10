//! F10 状态窗（人物信息）：背板 + 角色小人 + 装备槽 + 属性数值。
//!
//! # 为什么先做这一屏（2026-10-10）
//!
//! 用户点名「把 F10 补齐」。以前按 F10 **什么都不会发生** —— 而数据其实**早就在客户端里**：
//! `AbilityUpdate` 一直带着 `dc/mc/sc/ac/mac/weight/exp`（`common.proto` 的 `Ability`，
//! 服务端 `protocolAbility` 也一直在填），`EquippedItems` 带着 13 个槽位。
//! 所以这一屏**没动协议**，只是把已有数据摆出来 —— 教训见 `docs/authority.md`：
//! "要显示的数值先看协议里有没有，别急着加字段"。
//!
//! # 版式
//!
//! - **背板** `Prguse3[4]`（404×452，带标题栏的雕花面板）—— 2026-10-10 用 `wzldump`
//!   在 `Prguse`/`Prguse2`/`Prguse3` 的几十张候选里按尺寸筛出来、逐张看过。
//! - **角色小人**用**自己**的三层精灵（身体/头发/武器，与地图上同一套公式），
//!   姿势 = 站着朝下的第 0 帧（`anim = None`，见 `body_sprite` 的说明）—— 就是官方的"元神"。
//! - **装备槽**按 `U_*` 下标排（`server/internal/proto/contract.go`：`SlotDress=0 … SlotBoots=11`，
//!   原版 `THumItems` 是 `array[0..12]` ⇒ 13 格），图标取 `Items.wzl[looks]`（与背包同一套）。
//! - **属性**两列摆在下方；数值取 `world.ability`（`AbilityUpdate`）。

use std::path::Path;
use std::time::Instant;

use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use crate::actor::{SpriteCache, body_sprite, draw_actor_layer, hair_sprite, weapon_sprite};
use crate::font;
use crate::net::Net;
use crate::ui;

/// 背板：`Prguse3[4]`（404×452）。素材取不到时退回"深底 + 描边"，**版式不变**。
pub(crate) const BG_LIB: &str = "Prguse3";
pub(crate) const BG: u32 = 4;
pub(crate) const W: f32 = 404.0;
pub(crate) const H: f32 = 452.0;

/// 窗口落点：**右上角**（用户 2026-10-10 的截图里状态窗在屏幕右侧）。
///
/// ⚠️ 与背包窗（左上角、对话窗正下方）**刻意错开**：两个窗口同时开着不该叠在一起。
pub(crate) fn panel(win: (u32, u32)) -> (f32, f32, f32, f32) {
    let x = (win.0 as f32 - W - 8.0).max(0.0);
    (x, 8.0, W, H)
}

/// 关闭（X）按钮 —— 素材标题栏右上角那个红方块（与背包窗 `X` 的取法一致）。
pub(crate) const CLOSE_X: f32 = W - 26.0;
pub(crate) const CLOSE_Y: f32 = 6.0;
pub(crate) const CLOSE_W: f32 = 20.0;
pub(crate) const CLOSE_H: f32 = 18.0;

/// 已穿戴的槽位数（原版 `THumItems = array[0..12]` ⇒ 13 格，与 `MaxEquipSlot` 一致）。
pub(crate) const EQUIP_SLOTS: usize = 13;
const EQUIP_COLS: usize = 2;
const CELL_W: f32 = 40.0;
/// 格子高 36：13 格 = 2 列 × 7 行，占 y 40..292，正好把下面的属性文本（y 300）让开。
const CELL_H: f32 = 36.0;
const EQUIP_X: f32 = 168.0;
const EQUIP_Y: f32 = 40.0;

/// 第 `i` 个装备槽在**窗口内**的矩形 `(x, y, w, h)`。
pub(crate) fn equip_slot_rect(i: usize) -> (f32, f32, f32, f32) {
    let col = (i % EQUIP_COLS) as f32;
    let row = (i / EQUIP_COLS) as f32;
    (
        EQUIP_X + col * CELL_W,
        EQUIP_Y + row * CELL_H,
        CELL_W,
        CELL_H,
    )
}

/// 窗口内的一点落在哪个装备槽（与 [`equip_slot_rect`] 同源）。
pub(crate) fn equip_slot_at(local: (f32, f32)) -> Option<usize> {
    (0..EQUIP_SLOTS).find(|&i| {
        let (x, y, w, h) = equip_slot_rect(i);
        local.0 >= x && local.0 < x + w && local.1 >= y && local.1 < y + h
    })
}

/// 点是否落在关闭按钮上。
pub(crate) fn on_close(local: (f32, f32)) -> bool {
    local.0 >= CLOSE_X
        && local.0 < CLOSE_X + CLOSE_W
        && local.1 >= CLOSE_Y
        && local.1 < CLOSE_Y + CLOSE_H
}

/// "角色小人"的落点区（精灵锚点在**区底中部**，与地图上同一套锚点语义）。
const FIGURE_X: f32 = 14.0;
const FIGURE_Y: f32 = 40.0;
const FIGURE_W: f32 = 130.0;
const FIGURE_H: f32 = 175.0;

/// 属性文本的落点 / 行距。
pub(crate) const TEXT_X: f32 = 16.0;
pub(crate) const TEXT_Y: f32 = 300.0;
const TEXT_LINE_H: f32 = 19.0;
const TEXT_COL2_X: f32 = 212.0;

/// 画 F10 状态窗。装备/属性都取自 `Net` 里已有的世界状态（没有就不画）。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    sprites: &mut SpriteCache<'a>,
    dir_assets: &Path,
    net: Option<&Net>,
    now: Instant,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let (x, y, w, h) = panel((crate::window::WIN_W, crate::window::WIN_H));

    // ---- 背板 ----
    canvas.set_blend_mode(BlendMode::Blend);
    if ui.size(dir_assets, BG_LIB, BG).is_some() {
        ui.draw_src(
            canvas,
            tc,
            dir_assets,
            BG_LIB,
            BG,
            FRect::new(0.0, 0.0, w, h),
            FRect::new(x, y, w, h),
            255,
        );
    } else {
        canvas.set_draw_color(sdl3::pixels::Color::RGBA(0, 0, 0, 215));
        canvas.fill_rect(FRect::new(x, y, w, h))?;
        canvas.set_draw_color(sdl3::pixels::Color::RGB(190, 170, 120));
        canvas.draw_rect(FRect::new(x, y, w, h))?;
    }
    canvas.set_blend_mode(BlendMode::None);
    let white = (236, 232, 208);
    let label = (206, 190, 140);
    let shadow = Some((0, 0, 0));
    texts.draw(canvas, tc, "状态", x + 12.0, y + 8.0, white, shadow)?;

    // ---- 角色小人（自己）----
    if let Some(e) = n.world.entities.get(&n.world.self_id) {
        let (ox, oy) = (x + FIGURE_X + FIGURE_W / 2.0, y + FIGURE_Y + FIGURE_H);
        let layers = [
            body_sprite(e, None, now),
            hair_sprite(e, None, now),
            weapon_sprite(e, None, now),
        ];
        for &(lib, idx) in layers.iter().flatten() {
            sprites.ensure(tc, dir_assets, lib, idx);
        }
        for &(lib, idx) in layers.iter().flatten() {
            draw_actor_layer(canvas, sprites, lib, idx, ox, oy, None)?;
        }
    }

    // ---- 装备槽（13 格，图标取 `Items.wzl[looks]`）----
    for i in 0..EQUIP_SLOTS {
        let (sx, sy, sw, sh) = equip_slot_rect(i);
        let (sx, sy) = (x + sx, y + sy);
        canvas.set_blend_mode(BlendMode::Blend);
        canvas.set_draw_color(sdl3::pixels::Color::RGBA(18, 16, 12, 170));
        canvas.fill_rect(FRect::new(sx, sy, sw, sh))?;
        canvas.set_draw_color(sdl3::pixels::Color::RGB(96, 86, 62));
        canvas.draw_rect(FRect::new(sx, sy, sw, sh))?;
        canvas.set_blend_mode(BlendMode::None);
        if let Some(Some(item)) = n.world.equip.get(i) {
            if item.looks != 0 {
                if let Some((iw, ih)) = ui.size(dir_assets, "Items", item.looks) {
                    let (iw, ih) = (iw as f32, ih as f32);
                    ui.draw_src(
                        canvas,
                        tc,
                        dir_assets,
                        "Items",
                        item.looks,
                        FRect::new(0.0, 0.0, iw, ih),
                        FRect::new(
                            sx + (sw - iw) / 2.0,
                            sy + (sh - ih) / 2.0,
                            iw,
                            ih,
                        ),
                        255,
                    );
                }
            }
        }
    }

    // ---- 属性两列 ----
    let a = n.world.ability.unwrap_or_default();
    let left = [
        format!("等级  {}", a.level),
        format!("生命  {}/{}", a.hp, a.max_hp),
        format!("魔法  {}/{}", a.mp, a.max_mp),
        format!("经验  {}/{}", a.exp, a.max_exp),
        format!("负重  {}/{}", a.weight, a.max_weight),
        format!("金币  {}", a.gold),
    ];
    let right = [
        ("攻击", format!("{}-{}", a.dc_min, a.dc_max)),
        ("魔法", format!("{}-{}", a.mc_min, a.mc_max)),
        ("道术", format!("{}-{}", a.sc_min, a.sc_max)),
        ("防御", format!("{}", a.ac)),
        ("魔御", format!("{}", a.mac)),
    ];
    for (i, line) in left.iter().enumerate() {
        texts.draw(
            canvas,
            tc,
            line,
            x + TEXT_X,
            y + TEXT_Y + i as f32 * TEXT_LINE_H,
            white,
            shadow,
        )?;
    }
    for (i, (tag, val)) in right.iter().enumerate() {
        let ly = y + TEXT_Y + i as f32 * TEXT_LINE_H;
        texts.draw(canvas, tc, tag, x + TEXT_COL2_X, ly, label, shadow)?;
        texts.draw(canvas, tc, val, x + TEXT_COL2_X + 44.0, ly, white, shadow)?;
    }
    Ok(())
}

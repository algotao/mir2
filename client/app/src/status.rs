//! F10 状态窗 —— **照官方版式**（`FState.pas` 的 `TFrmDlg.DStateWin`）。
//!
//! # 内容与出处（2026-10-10；官方源码 `mir2standard/GameOfMir/MirClient`）
//!
//! | 部件 | 素材 | 出处 |
//! |---|---|---|
//! | 窗框 / 底板 | `Prguse[370]`（232×325，**AC/MAC/DC/MC/SC/HP/MP 的标签烤在图里**） | `FState.pas:1017-1022` |
//! | 落点 | **贴屏幕右缘**、距顶 **52**；内容偏移 `(38, 52)` | `FState.pas:1020-1021/2907-2908` |
//! | 关闭 X | `Prguse[371]` @(8,39) | `FState.pas:1157-1159` |
//! | 翻页箭头 | `Prguse[373]` @(7,128) 上页、`Prguse[372]` @(7,187) 下页 | `FState.pas:1141-1146` |
//! | 小人底图 | `Prguse[376 男]` / `[377 女]`（168×199，**6 个槽框也烤在里面**） | `FState.pas:2918-2927` |
//! | 头发 | `Prguse[440 + 发型*2 + 性别]` | `FState.pas:2933-2938` |
//! | 衣服 / 武器 / 头盔 | **`StateItem.wil[Looks]`**（大图，不是 `Items.wil` 的背包小图） | `FState.pas:2941-2969` |
//! | 装备槽 | **围着人形一圈的固定坐标**（不是网格） | `FState.pas:1025-1070` |
//! | 属性数值 | 打在第 `(115, 98)` 起、行距 20 —— **正落进底图烤好的框里** | `FState.pas:2971-2985` |
//!
//! # 两条硬规则（2026-10-10 验证出来的）
//!
//! 1. **所有图层画在「内容原点 + 各自锚点」**：锚点是素材作者按身体烤进图里的
//!    （`ui.anchor` 读它）。离线合成对照过：改成"相对底图"就整体错位（裤子穿不到腿上）。
//! 2. **槽内图标取 `StateItem.wil[Looks]`**（官方 `GetWStateImg`，`ClMain.pas:6846`），
//!    与背包/快捷栏的 `Items.wil` 是**两套尺寸**（木剑 `looks=30`：StateItem 28×57、
//!    Items 36×24）。
//!
//! ⚠️ 早期版本（当天上午）用的是"拿世界里的自己实体、画 Hum 三层精灵"，那条路有两个坑：
//! ① `world.entities` 里**没有自己**（`app/src/world.rs` 是临时造实体走的同一条绘制路径）
//! ⇒ 小人永远空白；② `draw_actor_layer` 的 `(px,py)` 是**格子左上角**，塞"区域底中"会整体偏半格。
//! 换成官方那套（底图 + StateItem 大图）后这两个问题都不存在了。
//!
//! # 我们做的取舍
//!
//! - 官方一共 4 页（装备 / 攻击属性 / 经验负重 / 魔法技能）。我们做**前两页**：
//!   页 0 = 装备（人形 + 槽），页 1 = 属性；魔法技能页要等技能系统。
//! - 属性页比官方多印「等级 / 经验 / 负重 / 金币」（官方在别的页，而我们的 `Ability`
//!   已经带着），摆在底图下方那块空框里。
//! - ⚠️ 官方是**先写走/跑意图、再用窗口命中清掉**（`main.rs` 的既有链路），
//!   所以点窗口仍可能让同位置的 NPC 挨一下 —— 与背包窗同一个已知问题（`todo.md` §0-5）。

use std::path::Path;

use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use crate::font;
use crate::net::Net;
use crate::ui;

/// 窗框（`Prguse[370]`，232×325）。
pub(crate) const BG_LIB: &str = "Prguse";
pub(crate) const BG: u32 = 370;
/// 页数（官方 4 页，我们做前两页）。
pub(crate) const PAGES: usize = 2;
/// 官方落点：贴右缘、距顶 52（`FState.pas:1020-1021`）。
pub(crate) const TOP: f32 = 52.0;
/// 官方内容偏移（`FState.pas:2907-2908`）。
pub(crate) const CONTENT_X: f32 = 38.0;
pub(crate) const CONTENT_Y: f32 = 52.0;

/// 小人/衣服等大图的库（**`StateItem`**，见模块头硬规则 2）。
pub(crate) const ITEM_LIB: &str = "StateItem";

const CLOSE_IMG: u32 = 371;
const ARROW_UP: u32 = 373;
const ARROW_DOWN: u32 = 372;
const FIG_M: u32 = 376;
const FIG_F: u32 = 377;
/// 发型图号基址：`440 + 发型*2 + 性别`（男 0 / 女 1）。
const HAIR_BASE: u32 = 440;

/// 窗口矩形 `(x, y, w, h)`：尺寸 = 素材原始像素（**不缩放**）。
pub(crate) fn panel(bg: (u32, u32)) -> (f32, f32, f32, f32) {
    let (w, h) = (bg.0 as f32, bg.1 as f32);
    ((crate::window::WIN_W as f32 - w).max(0.0), TOP, w, h)
}

/// 装备槽：`(槽位下标, x, y, w, h)`，坐标**相对窗口**，照官方 `FState.pas:1025-1070`。
///
/// 衣服/武器/头盔是**压在人形上的大命中区**（内容画在小人身上，不在槽里）；
/// 其余 6 个是左右两列的小槽。官方没接 `belt/boots/charm`（命中判定在官方也被注释掉了）。
pub(crate) const SLOTS: [(usize, f32, f32, f32, f32); 9] = [
    (0, 96.0, 122.0, 53.0, 112.0),  // 衣服
    (1, 47.0, 70.0, 47.0, 87.0),    // 武器
    (4, 115.0, 85.0, 18.0, 18.0),   // 头盔
    (3, 169.0, 88.0, 34.0, 30.0),   // 项链
    (2, 169.0, 127.0, 34.0, 30.0),  // 右手（火把/蜡烛）
    (5, 169.0, 177.0, 34.0, 30.0),  // 左手镯
    (7, 169.0, 217.0, 34.0, 30.0),  // 左戒指
    (6, 43.0, 177.0, 34.0, 30.0),   // 右手镯
    (8, 43.0, 217.0, 34.0, 30.0),   // 右戒指
];

/// 会在槽里**画图标**的那几个（衣服/武器/头盔画在人形上，不重复画）。
const SMALL_SLOTS: [usize; 6] = [3, 2, 5, 7, 6, 8];

/// 关闭 X / 翻页箭头（官方坐标与尺寸）。
pub(crate) const CLOSE: (f32, f32, f32, f32) = (8.0, 39.0, 16.0, 23.0);
pub(crate) const ARROW_UP_RECT: (f32, f32, f32, f32) = (7.0, 128.0, 24.0, 23.0);
pub(crate) const ARROW_DOWN_RECT: (f32, f32, f32, f32) = (7.0, 187.0, 24.0, 23.0);

/// 属性数值的落点（官方 `FState.pas:2971-2985`：`Left+115, Top+98`、行距 20）。
const ATTR_X: f32 = 115.0;
const ATTR_Y: f32 = 98.0;
const ATTR_STEP: f32 = 20.0;

/// 窗口内一次点击落在谁身上。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Hit {
    Close,
    /// 翻页：`-1` 上一页 / `+1` 下一页。
    Arrow(i8),
    /// 命中装备槽（值是**服务端槽位下标**）。
    Slot(usize),
    None,
}

fn in_rect(r: (f32, f32, f32, f32), p: (f32, f32)) -> bool {
    p.0 >= r.0 && p.0 < r.0 + r.2 && p.1 >= r.1 && p.1 < r.1 + r.3
}

/// 窗口内一点命中谁（**画与命中同源**：坐标都来自上面那几张常量表）。
pub(crate) fn hit(local: (f32, f32)) -> Hit {
    if in_rect(CLOSE, local) {
        return Hit::Close;
    }
    if in_rect(ARROW_UP_RECT, local) {
        return Hit::Arrow(-1);
    }
    if in_rect(ARROW_DOWN_RECT, local) {
        return Hit::Arrow(1);
    }
    for &(slot, sx, sy, sw, sh) in SLOTS.iter() {
        if in_rect((sx, sy, sw, sh), local) {
            return Hit::Slot(slot);
        }
    }
    Hit::None
}

/// 翻页：`dir = +1` 下一页 / `-1` 上一页，在 `PAGES` 里**循环**（官方也是循环翻）。
pub(crate) fn page_step(page: usize, dir: i8) -> usize {
    if dir >= 0 {
        (page + 1) % PAGES
    } else {
        (page + PAGES - 1) % PAGES
    }
}

/// 画状态窗。`page` = 0 装备页 / 1 属性页。
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut font::TextCache<'a>,
    dir_assets: &Path,
    net: Option<&Net>,
    page: usize,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let Some(bg) = ui.size(dir_assets, BG_LIB, BG) else {
        return Ok(()); // 素材不在：什么都不画（那屏另有横幅提示）
    };
    let (x, y, w, h) = panel(bg);
    canvas.set_blend_mode(BlendMode::Blend);
    let _ = ui.draw_src(
        canvas,
        tc,
        dir_assets,
        BG_LIB,
        BG,
        FRect::new(0.0, 0.0, w, h),
        FRect::new(x, y, w, h),
        255,
    );
    canvas.set_blend_mode(BlendMode::None);

    // 三个控件按钮（官方给了显式矩形，不按锚点）
    button(canvas, tc, ui, dir_assets, ARROW_UP, x, y, ARROW_UP_RECT);
    button(canvas, tc, ui, dir_assets, ARROW_DOWN, x, y, ARROW_DOWN_RECT);
    button(canvas, tc, ui, dir_assets, CLOSE_IMG, x, y, CLOSE);

    if page == 0 {
        draw_figure(canvas, tc, ui, dir_assets, n, x, y);
    } else {
        draw_attrs(canvas, tc, texts, n, x, y);
    }
    Ok(())
}

/// 一页里的**装备页**：裸体底图 + 头发 + 衣服/武器/头盔 + 6 个小槽的图标。
///
/// ⚠️ 全部画在「内容原点 + 图自带锚点」上（模块头硬规则 1）。
fn draw_figure<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    dir_assets: &Path,
    n: &Net,
    x: f32,
    y: f32,
) {
    let (cx, cy) = (x + CONTENT_X, y + CONTENT_Y);
    let (hair, sex) = feature_of(n);
    // 裸体底图（男/女）
    anchored(canvas, tc, ui, dir_assets, BG_LIB, if sex == 1 { FIG_F } else { FIG_M }, cx, cy);
    // 头发（发型 0 = 光头：素材里那块本来就取不到，取不到就跳过）
    anchored(
        canvas,
        tc,
        ui,
        dir_assets,
        BG_LIB,
        HAIR_BASE + hair * 2 + sex as u32,
        cx,
        cy,
    );
    // 衣服 / 武器 / 头盔（官方顺序，都是 `StateItem.wil[Looks]` 的大图）
    for slot in [0usize, 1, 4] {
        let Some(Some(item)) = n.world.equip.get(slot) else {
            continue;
        };
        if item.looks != 0 {
            anchored(canvas, tc, ui, dir_assets, ITEM_LIB, item.looks, cx, cy);
        }
    }
    // 6 个小槽里的图标：居中放进槽框（官方不缩放，我们也照原样贴）
    for &slot in SMALL_SLOTS.iter() {
        let Some(Some(item)) = n.world.equip.get(slot) else {
            continue;
        };
        if item.looks == 0 {
            continue;
        }
        let Some(&(_, sx, sy, sw, sh)) = SLOTS.iter().find(|s| s.0 == slot) else {
            continue;
        };
        if let Some((iw, ih)) = ui.size(dir_assets, ITEM_LIB, item.looks) {
            let (iw, ih) = (iw as f32, ih as f32);
            let _ = ui.draw_src(
                canvas,
                tc,
                dir_assets,
                ITEM_LIB,
                item.looks,
                FRect::new(0.0, 0.0, iw, ih),
                FRect::new(x + sx + (sw - iw) / 2.0, y + sy + (sh - ih) / 2.0, iw, ih),
                255,
            );
        }
    }
}

/// **属性页**：数值打进底图烤好的框里（官方只印数值，标签在 `Prguse[370]` 里）。
fn draw_attrs<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    texts: &mut font::TextCache<'a>,
    n: &Net,
    x: f32,
    y: f32,
) {
    let Some(a) = n.world.ability else {
        return; // 还没收到 `AbilityUpdate`：别显示一片 0（背包窗也是这个口径）
    };
    let white = (232, 228, 204);
    let shadow = Some((0, 0, 0));
    // 与底图烤的标签**逐行对应**：AC / MAC / DC / MC / SC / HP / MP
    let rows = [
        format!("{}", a.ac),
        format!("{}", a.mac),
        format!("{}-{}", a.dc_min, a.dc_max),
        format!("{}-{}", a.mc_min, a.mc_max),
        format!("{}-{}", a.sc_min, a.sc_max),
        format!("{}/{}", a.hp, a.max_hp),
        format!("{}/{}", a.mp, a.max_mp),
    ];
    for (i, line) in rows.iter().enumerate() {
        let _ = texts.draw(
            canvas,
            tc,
            line,
            x + ATTR_X,
            y + ATTR_Y + i as f32 * ATTR_STEP,
            white,
            shadow,
        );
    }
    // 底图下方那块空框：我们的"等级/经验/负重/金币"（官方在别的页）
    let extra = [
        format!("等级 {}", a.level),
        format!("经验 {}/{}", a.exp, a.max_exp),
        format!("负重 {}/{}", a.weight, a.max_weight),
        format!("金币 {}", a.gold),
    ];
    for (i, line) in extra.iter().enumerate() {
        let _ = texts.draw(
            canvas,
            tc,
            line,
            x + 24.0,
            y + 248.0 + i as f32 * 16.0,
            white,
            shadow,
        );
    }
}

/// 画一张界面图，落点 = `(x, y) + 图自带锚点`（官方合成规则的**唯一**实现）。
fn anchored<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    dir_assets: &Path,
    lib: &'static str,
    idx: u32,
    x: f32,
    y: f32,
) {
    let (Some((w, h)), Some((ax, ay))) = (
        ui.size(dir_assets, lib, idx),
        ui.anchor(dir_assets, lib, idx),
    ) else {
        return;
    };
    let _ = ui.draw_src(
        canvas,
        tc,
        dir_assets,
        lib,
        idx,
        FRect::new(0.0, 0.0, w as f32, h as f32),
        FRect::new(x + ax as f32, y + ay as f32, w as f32, h as f32),
        255,
    );
}

/// 画一个"控件按钮"（关闭 X / 翻页箭头）：官方给的是显式矩形，不按锚点。
fn button<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    dir_assets: &Path,
    idx: u32,
    x: f32,
    y: f32,
    r: (f32, f32, f32, f32),
) {
    let Some((w, h)) = ui.size(dir_assets, BG_LIB, idx) else {
        return;
    };
    let _ = ui.draw_src(
        canvas,
        tc,
        dir_assets,
        BG_LIB,
        idx,
        FRect::new(0.0, 0.0, w as f32, h as f32),
        FRect::new(x + r.0, y + r.1, r.2, r.3),
        255,
    );
}

/// 自己的发型与性别：都从 `self_feature` 取（`dress` 的低位是性别，见 `equip.go`）。
///
/// 取不到（还没进世界/没特征）⇒ 发型 0、性别 0（男）。
pub(crate) fn feature_of(n: &Net) -> (u32, u8) {
    match n.world.self_feature.as_ref() {
        Some(f) => (f.hair as u32, (f.dress & 1) as u8),
        None => (0, 0),
    }
}

/// 槽位的中文名（官方常量表 `Grobal2.pas:26-38`；只用于聊天区反馈，不参与版式）。
pub(crate) fn slot_name(slot: usize) -> &'static str {
    match slot {
        0 => "衣服",
        1 => "武器",
        2 => "右手",
        3 => "项链",
        4 => "头盔",
        5 => "左手镯",
        6 => "右手镯",
        7 => "左戒指",
        8 => "右戒指",
        9 => "护身符",
        10 => "腰带",
        11 => "靴子",
        _ => "宝石",
    }
}

//! NPC 商店窗：点商人后跟着对话一起开（原版"对话 + 货架"是同一次点击来的）。
//!
//! 数据来自服务端下发的 `ShopList`（`world.shop`），买/卖发 `ShopBuy` / `ShopSell`
//! —— 原版是 `CM_USERBUYITEM` / `CM_USERSELLITEM`，两条都按**名字 / 实例号**认物。
//!
//! ⚠️ 窗里点一下 = 买/卖 **1 个**（原版就是一次一件：货架存量那一栏不是数量，
//! 见服务端 `shop.go` 的 `handleBuyItem`）。

use sdl3::pixels::Color;
use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use std::path::Path;

use crate::gfx::fill;
use crate::net::Net;
use crate::ui;

/// 背板：先用**背包窗那张**（`Prguse[3]`，336×270，已确认是一块窗）。
///
/// ⚠️ 原版的"商店窗"是独立的一张，本仓还没比对出它对应的图号 ⇒ 先用一块
/// **确认长得像窗**的板，版式（列表 + 价格）照原版。找到真图号后改这一个常量即可，
/// 几何与命中都不用动。
const BG_LIB: &str = "Prguse";
const BG: u32 = 3;

/// 背板的原生尺寸。
const W: f32 = 336.0;
const H: f32 = 270.0;

/// 一页几行（行高 20，留出顶部标题与底部金币条）。
pub(crate) const ROWS: usize = 10;
const ROW_H: f32 = 20.0;

/// 列表左上角（窗口内坐标）。
const LIST_X: f32 = 12.0;
const LIST_Y: f32 = 28.0;

/// 价格右对齐的 x（窗口内坐标）。
const PRICE_X: f32 = 232.0;

/// 标题与金币的落点（窗口内坐标）。
const TITLE_AT: (f32, f32) = (12.0, 6.0);
const GOLD_AT: (f32, f32) = (12.0, 246.0);

/// 关闭按钮（右上角那个红叉；**自己画**，不依赖背板上的图案）。
const CLOSE: (f32, f32, f32, f32) = (306.0, 4.0, 24.0, 20.0);

/// 文字颜色（与对话窗同一套：正文近白、价格/标题黄）。
const C_TEXT: (u8, u8, u8) = (238, 238, 214);
const C_PRICE: (u8, u8, u8) = (232, 220, 96);

/// 窗的落点：**对话窗右边**（对话窗占 `8..424` ⇒ 这里从 432 起；不与背包窗
/// （`8..344`）、状态窗（`792..1024`）重叠）。
pub(crate) fn panel() -> (f32, f32, f32, f32) {
    (8.0 + crate::input::DIALOG_W + 8.0, 4.0, W, H)
}

/// 第 `row` 行（**页内下标**）在窗口内的矩形。画与命中同源。
pub(crate) fn row_rect(row: usize) -> (f32, f32, f32, f32) {
    (LIST_X, LIST_Y + row as f32 * ROW_H, W - LIST_X * 2.0, ROW_H)
}

/// 一共几页（服务端一个商人最多 20 件 ⇒ 2 页）。
pub(crate) fn pages(items: usize) -> usize {
    items.div_ceil(ROWS).max(1)
}

/// 鼠标点中了什么（窗口内坐标）。
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Hit {
    /// 第 `row` 行（页内下标）。
    Row(usize),
    Close,
    None,
}

/// 窗口内的一点落在哪里。与 [`row_rect`] 同源。
pub(crate) fn hit(local: (f32, f32), rows: usize) -> Hit {
    let (cx, cy, cw, ch) = CLOSE;
    if local.0 >= cx && local.0 < cx + cw && local.1 >= cy && local.1 < cy + ch {
        return Hit::Close;
    }
    for i in 0..rows.min(ROWS) {
        let (x, y, w, h) = row_rect(i);
        if local.0 >= x && local.0 < x + w && local.1 >= y && local.1 < y + h {
            return Hit::Row(i);
        }
    }
    Hit::None
}

/// 页码推进（与状态窗的 `page_step` 同一条：到头就绕回）。
pub(crate) fn page_step(page: usize, dir: i8, items: usize) -> usize {
    let n = pages(items);
    if dir > 0 {
        (page + 1) % n
    } else if dir < 0 {
        (page + n - 1) % n
    } else {
        page.min(n - 1)
    }
}

/// 画商店窗：背板 + 商品名/价格列表 + 金币 + 页码 + 关闭叉。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut crate::font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    page: usize,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let Some(shop) = n.world.shop.as_ref() else {
        return Ok(()); // 没有货架（没点过商人 / 对话关了）⇒ 不画
    };
    let (x, y, w, h) = panel();
    canvas.set_blend_mode(BlendMode::Blend);
    if ui.size(dir, BG_LIB, BG).is_some() {
        let _ = ui.draw_src(
            canvas,
            tc,
            dir,
            BG_LIB,
            BG,
            FRect::new(0.0, 0.0, w, h),
            FRect::new(x, y, w, h),
            255,
        );
    } else {
        // 素材不在 ⇒ 画个深底 + 边框，版式不变（开发机上常见）
        canvas.set_draw_color(Color::RGBA(0, 0, 0, 215));
        canvas.fill_rect(FRect::new(x, y, w, h))?;
        canvas.set_draw_color(Color::RGB(190, 170, 120));
        canvas.draw_rect(FRect::new(x, y, w, h))?;
    }
    canvas.set_blend_mode(BlendMode::None);

    let page = page.min(pages(shop.items.len()) - 1);
    let base = page * ROWS;
    texts.draw(
        canvas,
        tc,
        "商 店",
        x + TITLE_AT.0,
        y + TITLE_AT.1,
        C_PRICE,
        None,
    )?;
    for i in 0..ROWS {
        let Some(it) = shop.items.get(base + i) else {
            break;
        };
        let (rx, ry, rw, rh) = row_rect(i);
        // 每行一条暗底：让人看出"这是一行能点的"
        fill(
            canvas,
            x + rx,
            y + ry,
            rw,
            rh - 1.0,
            Color::RGBA(60, 52, 36, 110),
        )?;
        texts.draw(canvas, tc, &it.name, x + rx, y + ry + 2.0, C_TEXT, None)?;
        // 价格**右对齐**（文字缓存只给落点 ⇒ 按 14px 字号估宽）
        let price = it.price.to_string();
        let px = x + PRICE_X - price.chars().count() as f32 * 7.0;
        texts.draw(canvas, tc, &price, px, y + ry + 2.0, C_PRICE, None)?;
    }
    // 金币（下方那条横条；`Ability.gold`，与背包窗同一个来源）与页码
    let gold = n.world.ability.map_or(0, |ab| ab.gold);
    texts.draw(
        canvas,
        tc,
        &format!("金币 {gold}"),
        x + GOLD_AT.0,
        y + GOLD_AT.1,
        C_TEXT,
        None,
    )?;
    texts.draw(
        canvas,
        tc,
        &format!("{}/{}", page + 1, pages(shop.items.len())),
        x + PRICE_X,
        y + GOLD_AT.1,
        (190, 170, 120),
        None,
    )?;
    // 关闭叉：两条斜线，自己画（不依赖背板上的图案）
    let (cx, cy, cw, _) = CLOSE;
    let (cx, cy) = (x + cx, y + cy);
    canvas.set_draw_color(Color::RGB(220, 60, 50));
    for i in 0..6 {
        let f = i as f32;
        canvas.fill_rect(FRect::new(cx + 6.0 + f, cy + 4.0 + f, 2.0, 2.0))?;
        canvas.fill_rect(FRect::new(cx + cw - 8.0 - f, cy + 4.0 + f, 2.0, 2.0))?;
    }
    Ok(())
}

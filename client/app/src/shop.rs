//! NPC 商店窗：点对话里的「打开 交易市场」（`@buy`）才弹出 —— **不是**点 NPC 就出
//!（用户 2026-10-10：「商店图应在"打开 交易市场"时弹出，而不是开启对话就出」）。
//!
//! 数据来自服务端下发的 `ShopList`（`world.shop`），买/卖发 `ShopBuy` / `ShopSell`
//! —— 原版是 `CM_USERBUYITEM` / `CM_USERSELLITEM`，两条都按**名字 / 实例号**认物。
//!
//! ⚠️ 窗里点一下 = 买/卖 **1 个**（原版就是一次一件：货架存量那一栏不是数量，
//! 见服务端 `shop.go` 的 `handleBuyItem`）。卖 = 商店窗开着时**点背包里的东西**。
//!
//! 版式：用**对话窗同一块官方背板**（`Prguse[384]`，416×176，青铜框内沿、行高、
//! 关闭叉的几何都是现成的）—— 用户 2026-10-10 报过「商品列表与窗口错位了，且不应
//! 增加背景」：早先借用背包板（`Prguse[3]`）自己排行还画了行底色，两边都对不上。
//! 换成对话板之后：行高/内边距/关闭钮**全部复用 `input::DIALOG_*` 那一套常量**，
//! 背板一变它们自己跟着变；行底色不再画（官方的列表就是白字直接排在板面上）。

use sdl3::pixels::Color;
use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use std::path::Path;

use crate::net::Net;
use crate::ui;

use crate::input::{
    DIALOG_BG, DIALOG_CLOSE_H, DIALOG_CLOSE_W, DIALOG_CLOSE_X, DIALOG_CLOSE_Y, DIALOG_H,
    DIALOG_LINE_H, DIALOG_MAX_LINES, DIALOG_PAD_X, DIALOG_PAD_Y, DIALOG_W,
};

/// 一页几行：**顶部一行是"商店 + 金币"的标题行**，剩下才是商品
///（`(176-32)/18 = 8` 行 ⇒ 商品 7 行）。
pub(crate) const ROWS: usize = DIALOG_MAX_LINES - 1;

/// 文字颜色（与对话窗同一套：正文近白、价格/标题黄）。
const C_TEXT: (u8, u8, u8) = (238, 238, 214);
const C_PRICE: (u8, u8, u8) = (232, 220, 96);

/// 窗的落点：**对话窗右边**（对话窗占 `8..424` ⇒ 这里从 432 起，同一行排开）。
///
/// 背包窗（`8..344`）也在下面一排，互不相压。状态窗（792 起）开着时会叠上来 ——
/// 浮窗叠浮窗原版也常见（它俩本来都可关），不做互斥。
pub(crate) fn panel() -> (f32, f32, f32, f32) {
    (8.0 + DIALOG_W + 8.0, 4.0, DIALOG_W, DIALOG_H)
}

/// 第 `row` 行（**页内下标**）在窗口内的矩形。画与命中同源。
///
/// ⚠️ `y` 起点是**标题行下面**：`DIALOG_PAD_Y + 行高`。
pub(crate) fn row_rect(row: usize) -> (f32, f32, f32, f32) {
    (
        DIALOG_PAD_X,
        DIALOG_PAD_Y + DIALOG_LINE_H + row as f32 * DIALOG_LINE_H,
        DIALOG_W - 2.0 * DIALOG_PAD_X,
        DIALOG_LINE_H,
    )
}

/// 一共几页（服务端一个商人最多 20 件 ⇒ 3 页）。
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

/// 窗口内的一点落在哪里。与 [`row_rect`]、对话窗的关闭钮**同一套几何**。
pub(crate) fn hit(local: (f32, f32), rows: usize) -> Hit {
    let (lx, ly) = local;
    if (DIALOG_CLOSE_X..DIALOG_CLOSE_X + DIALOG_CLOSE_W).contains(&lx)
        && (DIALOG_CLOSE_Y..DIALOG_CLOSE_Y + DIALOG_CLOSE_H).contains(&ly)
    {
        return Hit::Close;
    }
    for i in 0..rows.min(ROWS) {
        let (x, y, w, h) = row_rect(i);
        if lx >= x && lx < x + w && ly >= y && ly < y + h {
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

/// 画商店窗：官方对话背板 + 标题行（商店/金币）+ 商品名/价格 + 页码。
///
/// ⚠️ **不画行底色**（用户 2026-10-10：不该增加背景）——白字直接排在板面上，
/// 与对话窗的正文一个画法。关闭叉是**背板自带的**（对话窗那个），不用自己画。
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
        return Ok(()); // 没有货架（没点过"交易市场" / 对话关了）⇒ 不画
    };
    let (x, y, w, h) = panel();
    canvas.set_blend_mode(BlendMode::Blend);
    if ui.size(dir, "Prguse", DIALOG_BG).is_some() {
        let _ = ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            DIALOG_BG,
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

    // 标题行：左"商 店"、右"金币 N"（金币来自 `Ability.gold`，与背包窗同一来源）
    texts.draw(
        canvas,
        tc,
        "商 店",
        x + DIALOG_PAD_X,
        y + DIALOG_PAD_Y,
        C_PRICE,
        None,
    )?;
    let gold = n.world.ability.map_or(0, |ab| ab.gold);
    let gold_text = format!("金币 {gold}");
    let gx = x + DIALOG_W - DIALOG_PAD_X - gold_text.chars().count() as f32 * 7.0;
    texts.draw(canvas, tc, &gold_text, gx, y + DIALOG_PAD_Y, C_PRICE, None)?;

    // 商品行：名字靠左、价格**右对齐**（14px 字号 ⇒ 每字 ~7px，`text` 只给落点）
    for i in 0..ROWS {
        let Some(it) = shop.items.get(base + i) else {
            break;
        };
        let (rx, ry, _, _) = row_rect(i);
        texts.draw(canvas, tc, &it.name, x + rx, y + ry + 2.0, C_TEXT, None)?;
        let price = it.price.to_string();
        let px = x + rx + (DIALOG_W - 2.0 * DIALOG_PAD_X) - price.chars().count() as f32 * 7.0;
        texts.draw(canvas, tc, &price, px, y + ry + 2.0, C_PRICE, None)?;
    }
    // 页码：右下角（正文区外的那一圈板边上）
    let page_text = format!("{}/{}", page + 1, pages(shop.items.len()));
    texts.draw(
        canvas,
        tc,
        &page_text,
        x + DIALOG_W - DIALOG_PAD_X - page_text.chars().count() as f32 * 7.0,
        y + DIALOG_H - DIALOG_PAD_Y,
        (190, 170, 120),
        None,
    )?;
    Ok(())
}

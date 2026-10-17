//! 物品悬停提示（原版 `DScreen.ShowHint`）。
//!
//! 原版在背包/装备格上停一会儿会弹一小块黄字，写着物品名与属性（`GetMouseItemInfo`，
//! `FState.pas` 里 SellMenu/StorageMenu 都调它）。这里先只覆盖**背包**：
//! 我们的 `BagItem` 只有名字/数量/持久/卖价，没有攻击魔法道术那些词条
//!（服务端 `ItemStack` 也没下发），所以提示内容就这四项，不编造。
//!
//! 版式：深底 + 1px 金边；鼠标右下 12px，贴边翻到另一侧（不让它跑出屏幕）。

use sdl3::pixels::Color;
use sdl3::render::{TextureCreator, WindowCanvas};

use crate::gfx::fill;

/// 内边距（四边同宽）。
pub(crate) const PAD: f32 = 5.0;
/// 与鼠标的距离。
const GAP: f32 = 12.0;

/// 提示框里要写的几行（**纯函数**：内容由数据决定，方便测）。
///
/// 顺序照原版的信息密度：名字 → 数量 → 持久 → 卖价；没有的项直接不写
///（`dura_max = 0` 是药水/卷轴这类没有持久的物品）。
pub(crate) fn item_lines(
    name: &str,
    count: u32,
    dura: u32,
    dura_max: u32,
    sell_price: u32,
) -> Vec<String> {
    let mut out = vec![name.to_string()];
    if count > 1 {
        out.push(format!("数量 {count}"));
    }
    if dura_max > 0 {
        out.push(format!("持久 {dura}/{dura_max}"));
    }
    if sell_price > 0 {
        out.push(format!("卖价 {sell_price}"));
    }
    out
}

/// 货架上一件商品的提示行：名字 → 价格 → 持久 → 存量。
///
/// 货架行只有 `ShopItem`（名字/价格/存量/持久上限），没有数量与实例数据，
/// 所以不复用 [`item_lines`] 的"数量/卖价"口径，免得把"存量"说成"数量"。
pub(crate) fn shop_lines(name: &str, price: u64, dura_max: u32, stock: u32) -> Vec<String> {
    let mut out = vec![name.to_string(), format!("价格 {price}")];
    if dura_max > 0 {
        out.push(format!("持久 {dura_max}"));
    }
    if stock > 0 {
        out.push(format!("存量 {stock}"));
    }
    out
}

/// 框的大小：最宽那行 + 两边内边距，行数 × 行高 + 上下内边距。
pub(crate) fn size(max_line_w: f32, line_h: f32, lines: usize) -> (f32, f32) {
    (
        max_line_w.max(40.0) + 2.0 * PAD + 2.0,
        lines as f32 * line_h + 2.0 * PAD,
    )
}

/// 框的落点：鼠标右下；右/下贴边就翻到左侧/上方；最后夹回屏幕内。
pub(crate) fn place(cursor: (f32, f32), size: (f32, f32), win: (u32, u32)) -> (f32, f32, f32, f32) {
    let (cw, ch) = cursor;
    let (w, h) = size;
    let (ww, wh) = (win.0 as f32, win.1 as f32);
    let mut x = cw + GAP;
    if x + w > ww {
        x = cw - GAP - w;
    }
    let mut y = ch + GAP;
    if y + h > wh {
        y = ch - GAP - h;
    }
    (
        x.clamp(0.0, (ww - w).max(0.0)),
        y.clamp(0.0, (wh - h).max(0.0)),
        w,
        h,
    )
}

/// 画提示框（纯绘制；内容与落点已经由上面的纯函数算好）。
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    texts: &mut crate::font::TextCache<'a>,
    lines: &[String],
    cursor: (f32, f32),
    win: (u32, u32),
) -> Result<(), sdl3::Error> {
    // 提示框不贴图（原版也是纯底色 + 黄字，没比对出专用底图图号）。
    let max_w = lines.iter().map(|l| texts.width(l)).fold(0.0_f32, f32::max);
    let lh = texts.line_height();
    let (w, h) = size(max_w, lh, lines.len());
    let (x, y, w, h) = place(cursor, (w, h), win);

    fill(canvas, x, y, w, h, Color::RGBA(8, 10, 14, 232))?;
    // 1px 金边（原版提示框的黑底黄字；不加整框描边，免得抢字）
    let edge = Color::RGBA(170, 140, 70, 255);
    fill(canvas, x, y, w, 1.0, edge)?;
    fill(canvas, x, y + h - 1.0, w, 1.0, edge)?;
    fill(canvas, x, y, 1.0, h, edge)?;
    fill(canvas, x + w - 1.0, y, 1.0, h, edge)?;

    for (i, line) in lines.iter().enumerate() {
        texts.draw(
            canvas,
            tc,
            line,
            x + PAD + 1.0,
            y + PAD + i as f32 * lh,
            (255, 232, 140),
            Some((0, 0, 0)),
        )?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 背包物品的提示行随数据增减() {
        assert_eq!(item_lines("金创药", 1, 0, 0, 0), vec!["金创药"]);
        assert_eq!(
            item_lines("金创药", 7, 0, 0, 10),
            vec!["金创药", "数量 7", "卖价 10"]
        );
        assert_eq!(item_lines("木剑", 1, 3, 4, 0), vec!["木剑", "持久 3/4"]);
    }

    #[test]
    fn 货架提示写价格与存量() {
        assert_eq!(
            shop_lines("金创药", 20, 0, 7),
            vec!["金创药", "价格 20", "存量 7"]
        );
        // 没有持久上限的货（药品）不写持久；存量 0 也不编一行
        assert_eq!(
            shop_lines("木剑", 100, 4, 0),
            vec!["木剑", "价格 100", "持久 4"]
        );
    }

    #[test]
    fn 提示框默认贴在鼠标右下() {
        let (x, y, w, h) = place((100.0, 100.0), (60.0, 40.0), (1024, 768));
        assert_eq!((x, y, w, h), (112.0, 112.0, 60.0, 40.0));
    }

    #[test]
    fn 贴右边和下边时翻到另一侧() {
        // 右边装不下 ⇒ 翻到鼠标左侧
        let (x, _, _, _) = place((1000.0, 100.0), (60.0, 40.0), (1024, 768));
        assert_eq!(x, 1000.0 - 12.0 - 60.0);
        // 下边装不下 ⇒ 翻到鼠标上方
        let (_, y, _, _) = place((100.0, 760.0), (60.0, 40.0), (1024, 768));
        assert_eq!(y, 760.0 - 12.0 - 40.0);
    }

    #[test]
    fn 贴左上角时不会跑到屏幕外() {
        let (x, y, _, _) = place((0.0, 0.0), (60.0, 40.0), (1024, 768));
        assert!(x >= 0.0 && y >= 0.0);
        // 鼠标在角落、框比屏幕还大 ⇒ 夹到 0，不出现负坐标
        let (x, y, _, _) = place((0.0, 0.0), (2000.0, 2000.0), (1024, 768));
        assert_eq!((x, y), (0.0, 0.0));
    }

    #[test]
    fn 框尺寸跟着最宽行与行数走() {
        let (w, h) = size(100.0, 14.0, 3);
        assert_eq!(w, 100.0 + 2.0 * PAD + 2.0);
        assert_eq!(h, 3.0 * 14.0 + 2.0 * PAD);
    }
}

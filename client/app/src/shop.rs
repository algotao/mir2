//! NPC 商店窗：点对话里的「打开 交易市场」（`@buy`）才弹出 —— **不是**点 NPC 就出
//!（用户 2026-10-10：「商店图应在"打开 交易市场"时弹出，而不是开启对话就出」）。
//!
//! 版式照**原版截图**（用户同日给的）：三列 `物品列表 | 价格 | 持久`，底下
//! **左右箭头翻页**，**点一行选中、点 OK 才买**（不是点行就买）；买的时候
//! 包裹窗同时在右边开着（见 `layout::bag_pos`）。数据来自 `ShopList`
//!（`world.shop`），买发 `ShopBuy`；卖 = 商店开着时**点背包里的东西**。

use sdl3::pixels::Color;
use sdl3::render::{BlendMode, FRect, TextureCreator, WindowCanvas};

use std::path::Path;

use crate::gfx::fill;
use crate::net::Net;
use crate::ui;

/// 一页几行（原版一屏 6 行：上面留表头、下面留翻页/OK 那条）。
pub(crate) const ROWS: usize = 6;
const ROW_H: f32 = 20.0;

/// 面板尺寸（照原版截图的比例：比对话窗窄、矮）。
const W: f32 = 260.0;
const H: f32 = 170.0;

/// 三列的列头/列内容 x（窗口内坐标）。
const COL_NAME: f32 = 10.0;
const COL_PRICE: f32 = 128.0;
const COL_DURA: f32 = 204.0;

/// 表头与首行的 y。
const HEAD_Y: f32 = 8.0;
const LIST_Y: f32 = 28.0;

/// 底部那条：左/右箭头与 OK 按钮（窗口内矩形）。
const PREV: (f32, f32, f32, f32) = (12.0, 146.0, 28.0, 18.0);
const NEXT: (f32, f32, f32, f32) = (46.0, 146.0, 28.0, 18.0);
const OK: (f32, f32, f32, f32) = (192.0, 145.0, 58.0, 20.0);

/// 文字颜色（与对话窗同一套：表头/价格黄、正文近白）。
const C_HEAD: (u8, u8, u8) = (232, 220, 96);
const C_TEXT: (u8, u8, u8) = (238, 238, 214);
const C_SEL: (u8, u8, u8) = (255, 230, 130);

/// 窗的落点：**对话窗正下方**（对话窗占 `8..424` 的上排 ⇒ 这里接在下面）。
///
/// 包裹窗在商店开着时会让位到右边（`layout::bag_pos`），正好把这个位置让出来 ——
/// 与原版截图同款布局：左上对话、左下商品列表、右侧包裹。
pub(crate) fn panel() -> (f32, f32, f32, f32) {
    (8.0, 4.0 + crate::input::DIALOG_H + 8.0, W, H)
}

/// 第 `row` 行（**页内下标**）在窗口内的矩形。画与命中同源。
pub(crate) fn row_rect(row: usize) -> (f32, f32, f32, f32) {
    (COL_NAME, LIST_Y + row as f32 * ROW_H, W - 20.0, ROW_H)
}

/// 一共几页（服务端一个商人最多 20 件 ⇒ 4 页）。
pub(crate) fn pages(items: usize) -> usize {
    items.div_ceil(ROWS).max(1)
}

/// 鼠标点中了什么（窗口内坐标）。
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Hit {
    /// 第 `row` 行（页内下标）⇒ **选中**它（买要再点 OK）。
    Row(usize),
    Prev,
    Next,
    Ok,
    None,
}

/// 窗口内的一点落在哪里。与 [`row_rect`] 同源。
pub(crate) fn hit(local: (f32, f32), rows: usize) -> Hit {
    let (lx, ly) = local;
    let inside =
        |r: (f32, f32, f32, f32)| lx >= r.0 && lx < r.0 + r.2 && ly >= r.1 && ly < r.1 + r.3;
    if inside(OK) {
        return Hit::Ok;
    }
    if inside(PREV) {
        return Hit::Prev;
    }
    if inside(NEXT) {
        return Hit::Next;
    }
    for i in 0..rows.min(ROWS) {
        if inside(row_rect(i)) {
            return Hit::Row(i);
        }
    }
    Hit::None
}

/// 页码推进（到头就绕回）。
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

/// 画商店窗：表头三列 + 商品行（选中高亮）+ 左右翻页 + OK。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    _ui: &mut ui::UiCache<'a>,
    texts: &mut crate::font::TextCache<'a>,
    _dir: &Path,
    net: Option<&Net>,
    page: usize,
    selected: Option<usize>,
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
    // 这块板没有对应的官方图（原版是带网格的"货架"大窗，素材还没比对出来）⇒
    // 画个深底 + 边框，版式（三列 + 翻页 + OK）照原版截图。
    canvas.set_draw_color(Color::RGBA(10, 8, 6, 225));
    canvas.fill_rect(FRect::new(x, y, w, h))?;
    canvas.set_draw_color(Color::RGB(190, 170, 120));
    canvas.draw_rect(FRect::new(x, y, w, h))?;
    canvas.set_blend_mode(BlendMode::None);

    let page = page.min(pages(shop.items.len()) - 1);
    let base = page * ROWS;

    // 表头三列
    texts.draw(
        canvas,
        tc,
        "物品列表",
        x + COL_NAME,
        y + HEAD_Y,
        C_HEAD,
        None,
    )?;
    texts.draw(canvas, tc, "价格", x + COL_PRICE, y + HEAD_Y, C_HEAD, None)?;
    texts.draw(canvas, tc, "持久", x + COL_DURA, y + HEAD_Y, C_HEAD, None)?;

    for i in 0..ROWS {
        let Some(it) = shop.items.get(base + i) else {
            break;
        };
        let (rx, ry, rw, rh) = row_rect(i);
        // 选中行给一条亮底（原版选中就是整行点亮）
        if selected == Some(base + i) {
            fill(
                canvas,
                x + rx - 2.0,
                y + ry,
                rw + 4.0,
                rh - 1.0,
                Color::RGBA(120, 100, 40, 150),
            )?;
        }
        let col = if selected == Some(base + i) {
            C_SEL
        } else {
            C_TEXT
        };
        texts.draw(canvas, tc, &it.name, x + COL_NAME, y + ry + 2.0, col, None)?;
        // 价格**不带"金币"字样**（用户 2026-10-10 第 8 条的同一口径：数字即价格）
        texts.draw(
            canvas,
            tc,
            &it.price.to_string(),
            x + COL_PRICE,
            y + ry + 2.0,
            C_PRICE_COL,
            None,
        )?;
        texts.draw(
            canvas,
            tc,
            &it.dura_max.to_string(),
            x + COL_DURA,
            y + ry + 2.0,
            C_TEXT,
            None,
        )?;
    }

    // 翻页箭头（两个方钮：◀ ▶）与 OK / 页码
    for (r, left) in [(PREV, true), (NEXT, false)] {
        let (bx, by, bw, bh) = r;
        let (bx, by) = (x + bx, y + by);
        canvas.set_draw_color(Color::RGB(60, 70, 150));
        let _ = canvas.fill_rect(FRect::new(bx, by, bw, bh));
        fill_tri(canvas, bx, by, bw, bh, left)?;
    }
    // OK 按钮（原版就是一块蓝底白字）
    let (okx, oky, okw, okh) = OK;
    canvas.set_draw_color(Color::RGB(60, 70, 150));
    let _ = canvas.fill_rect(FRect::new(x + okx, y + oky, okw, okh));
    texts.draw(
        canvas,
        tc,
        "OK",
        x + okx + okw / 2.0 - 8.0,
        y + oky + 2.0,
        (240, 240, 255),
        None,
    )?;
    // 页码（右下、OK 左边）
    let page_text = format!("{}/{}", page + 1, pages(shop.items.len()));
    texts.draw(
        canvas,
        tc,
        &page_text,
        x + COL_DURA,
        y + 148.0,
        (190, 170, 120),
        None,
    )?;
    Ok(())
}

const C_PRICE_COL: (u8, u8, u8) = (232, 220, 96);

/// 在按钮里画一个**三角**（`left` = 尖朝左）：逐列收窄的实心条拼出来的。
/// 不用图形库路径 —— 项目里所有自绘控件都是 `fill_rect` 拼的，保持同一套路。
fn fill_tri(
    canvas: &mut WindowCanvas,
    bx: f32,
    by: f32,
    bw: f32,
    bh: f32,
    left: bool,
) -> Result<(), sdl3::Error> {
    canvas.set_draw_color(Color::RGB(225, 225, 245));
    let cy = by + bh / 2.0;
    let half0 = bh / 2.0 - 3.0;
    let steps = (half0.ceil() as i32).max(1);
    for k in 0..=steps {
        let f = k as f32;
        let half = (half0 - f).max(0.5);
        let cx0 = if left {
            bx + bw / 2.0 - f
        } else {
            bx + bw / 2.0 + f - 1.0
        };
        canvas.fill_rect(FRect::new(cx0, cy - half, TRI_W, half * 2.0))?;
    }
    Ok(())
}

/// 三角每一列的宽度（`fill_tri` 用）。
const TRI_W: f32 = 1.5;

// ---------- 卖货窗（原版拖放式，用户 2026-10-10 第 3 条） ----------
//
// 流程照原版截图：点背包里的物品**抓到手上** → 移到这个窗的**放物品槽**里点一下放下
// → 物品显示在槽中间、"卖:"后面显示能卖多少钱 → 点 **OK** 才真卖。
//
// 窗的位置：照原版截图（用户 2026-10-10「卖.png」）它不是一块大板，是**三个浮件**
// ——「"卖:"横条（带红 X）」挂对话窗正下方，放物品的圆槽**悬挂**在条下，OK 在圈右下。

/// 卖货窗的落点与尺寸 = 素材 `Prguse[392]` 的原生尺寸（140×181），不再自留边距。
pub(crate) fn sell_panel() -> (f32, f32, f32, f32) {
    let (dx, dy, dw, dh) = crate::input::dialog_panel();
    // **右对齐**上方对话窗（用户 2026-10-10）：右缘跟对话窗的右缘齐平
    (
        dx + dw - SELL_SLOT.2,
        dy + dh + 8.0,
        SELL_SLOT.2,
        SELL_SLOT.3,
    )
}
/// **放物品的槽** = `Prguse[392]`（140×181，链子挂坠+圆槽，物品放进去显示在中间）。
///
/// 2026-10-10 用户比对素材给出的图号；之前是自绘的金属圈，现在换成官方槽。
pub(crate) const SELL_SLOT_IMG: u32 = 392;
/// 槽在窗口内的落点：素材 392 **本身就是整个卖窗**（标题栏/圆槽/OK/关闭叉都在
/// 图里）⇒ 整张原样贴在窗口原点，不再自己挪。
pub(crate) const SELL_SLOT: (f32, f32, f32, f32) = (0.0, 0.0, 140.0, 181.0);
/// **官方素材自带的三个区**（`Prguse[392]` 140×181 里就画好了，我们**不重画**，
/// 只做命中）—— 坐标是拿 `wzldump` 导出后逐像素量出来的（2026-10-10）：
///
/// - 右上角的**红色关闭叉**：像素落在 x 117..127 / y 3..17；
/// - 下方的**蓝底白字圆形 OK**：蓝色像素落在 x 56..112 / y 144..171；
/// - 中间那个**大圆槽**：物品放进去的地方（圆心 [`SELL_CIRCLE`]）。
pub(crate) const SELL_CLOSE: (f32, f32, f32, f32) = (114.0, 1.0, 16.0, 20.0);
pub(crate) const SELL_OK: (f32, f32, f32, f32) = (56.0, 144.0, 58.0, 28.0);
/// 大圆槽的圆心与半径（素材内坐标）：物品放进去时按它**居中**。
///
/// ⚠️ 圆在素材的**中下部**（链条从顶部标题栏垂下来，圆吊在下面）——
/// 中心 (72, 94) 是按用户给的原版截图（卖.png）比例量出来的，不是素材正中。
pub(crate) const SELL_CIRCLE: (f32, f32, f32) = (72.0, 94.0, 54.0);
/// 金额文字的位置（素材标题栏上；**只画文字**，不自绘控件）。
pub(crate) const SELL_TITLE: (f32, f32) = (48.0, 4.0);

/// 卖货窗里点中了什么。
#[derive(Debug, PartialEq, Eq)]
pub(crate) enum SellHit {
    /// 放物品的槽：把手上的东西放进来 / **点已放进去的东西 ⇒ 再拿起来**。
    Slot,
    /// 素材右下角那个圆形 OK ⇒ 真卖。
    Ok,
    /// 素材右上角那个红色关闭叉。
    Close,
    None,
}

/// 窗口内的一点落在哪里。
pub(crate) fn sell_hit(local: (f32, f32)) -> SellHit {
    let (lx, ly) = local;
    // ⚠️ 顺序：**关闭叉 → OK 圆钮 → 大圆槽**（OK 压在圆槽下缘 ⇒ 先判 OK）
    let (cx, cy, cw, ch) = SELL_CLOSE;
    if lx >= cx && lx < cx + cw && ly >= cy && ly < cy + ch {
        return SellHit::Close;
    }
    let (ox, oy, ow, oh) = SELL_OK;
    if lx >= ox && lx < ox + ow && ly >= oy && ly < oy + oh {
        return SellHit::Ok;
    }
    // 圆槽：按圆心距离判（素材内坐标 ⇒ 减掉槽的落点）
    let (ccx, ccy, r) = SELL_CIRCLE;
    let (sx, sy, _, _) = SELL_SLOT;
    let (dx, dy) = (lx - (sx + ccx), ly - (sy + ccy));
    if dx * dx + dy * dy <= r * r {
        return SellHit::Slot;
    }
    SellHit::None
}

/// 画卖货窗：标题条（"卖:" + 金额）+ 放物品槽 `Prguse[392]`（物品居中）+ OK。
///
/// 板子同样是自绘深底（原版那块小窗的图号没比对出来），版式照参考截图。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_sell<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut crate::font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    placed: Option<usize>,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() || n.world.shop.is_none() {
        return Ok(()); // 没有货架（没在跟商人谈）⇒ 卖货窗也不出
    }
    let (x, y, _w, _h) = sell_panel();
    // ⚠️ **不画自绘的底板/金额框**（用户 2026-10-10：「多了一个你自绘的卖出金额图」）
    // ⇒ 原版卖窗就是"槽 + OK"两个浮件，这里只画文字与官方素材。

    // 放物品的槽（`Prguse[392]`，140×181，原生尺寸贴上去）。
    // ⚠️ **先贴素材**：素材整张盖上会盖掉先画的字（用户 2026-10-10：
    // 「预估金额没有显示」就是"卖:"和金额先画、被素材盖了）。
    let (sx, sy, sw, sh) = SELL_SLOT;
    if let Some((iw, ih)) = ui.size(dir, "Prguse", SELL_SLOT_IMG) {
        let _ = ui.draw_src(
            canvas,
            tc,
            dir,
            "Prguse",
            SELL_SLOT_IMG,
            FRect::new(0.0, 0.0, iw as f32, ih as f32),
            FRect::new(x + sx, y + sy, sw, sh),
            255,
        );
    }

    // "卖:" + 金额（放进槽里的那件能卖多少钱）—— 画在素材标题栏上（只有文字）
    texts.draw(
        canvas,
        tc,
        "卖:",
        x + SELL_TITLE.0,
        y + SELL_TITLE.1,
        C_HEAD,
        None,
    )?;
    let amount = placed
        .and_then(|i| n.world.bag.get(i))
        .and_then(|it| it.as_ref())
        .map_or(0, |it| it.sell_price * it.count.max(1));
    texts.draw(
        canvas,
        tc,
        &amount.to_string(),
        x + SELL_TITLE.0 + 34.0,
        y + SELL_TITLE.1,
        C_SEL,
        None,
    )?;

    // 槽里的物品：**按圆心居中**（用户 2026-10-10「放到出售圆圈内后要自动居中」；
    // 不放缩 —— 官方也是原样贴）。圆心是相对素材的 ⇒ 加上槽的落点。
    let (ccx, ccy, _) = SELL_CIRCLE;
    if let Some(idx) = placed {
        if let Some(Some(it)) = n.world.bag.get(idx) {
            if let Some((iw, ih)) = ui.size(dir, "Items", it.looks) {
                let (iw, ih) = (iw as f32, ih as f32);
                let _ = ui.draw_src(
                    canvas,
                    tc,
                    dir,
                    "Items",
                    it.looks,
                    FRect::new(0.0, 0.0, iw, ih),
                    FRect::new(x + sx + ccx - iw / 2.0, y + sy + ccy - ih / 2.0, iw, ih),
                    255,
                );
            }
        }
    }
    // ⚠️ **不画自绘的 OK**：`Prguse[392]` 里自带蓝底白字的圆形 OK（像素 x 56..112 /
    // y 144..171），命中区见 [`SELL_OK`]（用户 2026-10-10：卖窗不应有自绘元素）。
    Ok(())
}

/// **关掉购买窗**那一下的纯规则（抽出来是为了能单测；事件循环里不好造 `Event`）。
pub(crate) mod shop_close {
    /// 关购买窗时，**包裹要不要一起关**。
    ///
    /// 只关"因为买东西才自动开起来"的那一份（`bag_by_shop`）—— 玩家自己按 F9 开的
    /// 包裹不该被顺手关掉。用户 2026-10-10 第 1 条要的是**收摊**：
    /// 「购买对话框关闭时，物品列表及包裹窗口也关闭」。
    pub fn should_close_bag(bag_by_shop: bool) -> bool {
        bag_by_shop
    }

    /// 商品列表本身：货架没了（`world.shop == None`）⇒ `draw` 什么都不画。
    ///
    /// ⚠️ 这条是**注释性的**（真正的判据在 `draw` 开头的那个 `let Some(shop) = …`
    /// 早退）—— 留着它是为了把"收摊时两个窗一起没"这条规则写全，别只记得包裹。
    #[allow(dead_code)]
    pub fn should_close_list(shop_is_none: bool) -> bool {
        shop_is_none
    }
}

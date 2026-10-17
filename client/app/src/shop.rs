//! NPC 商店窗：点对话里的「打开 交易市场」（`@buy`）才弹出 —— **不是**点 NPC 就出
//!（用户 2026-10-10：「商店图应在"打开 交易市场"时弹出，而不是开启对话就出」）。
//!
//! 版式照**原版截图**（用户同日给的）：三列 `物品列表 | 价格 | 持久`，底下
//! **左右箭头翻页**，**点一行选中、点 OK 才买**（不是点行就买）；买的时候
//! 包裹窗同时在右边开着（见 `layout::bag_pos`）。数据来自 `ShopList`
//!（`world.shop`），买发 `ShopBuy`；卖 = 商店开着时**点背包里的东西**。

use sdl3::pixels::Color;
use sdl3::render::{FRect, TextureCreator, WindowCanvas};

use std::path::Path;

use crate::gfx::fill;
use crate::net::Net;
use crate::ui;

/// **买窗底图** = `Prguse[385]`（308×205，用户 2026-10-11 比对出来的）。
/// 它的三个按钮的**高亮图**（用户 2026-10-11 给的编号）：
/// [`BUY_OK_HOVER`] = 386、**左** = 388、**右** = 387。
///
/// ⚠️ 三个按钮的位置是拿**高亮图的主色**回到底图里反查出来的（2026-10-11）：
/// 386 的主色是 `#106684` ⇒ 底图里同色那块就是 OK；
/// 387/388 的主色是 `#291884` ⇒ 底图里同色的两个圆钮就是左/右。
/// （之前按"底部左/右各一块 47 宽"猜的位置是错的 —— 左/右是 24×24 圆钮，
/// 而且那两个 47 宽的框正好压在它们上面。）
pub(crate) const BUY_BG: u32 = mir2_core::image_manifest::assets::SHOP_BUY_BACKGROUND.index;
pub(crate) const BUY_OK_HOVER: u32 = mir2_core::image_manifest::assets::SHOP_BUY_OK_HOVER.index;
/// **左**（翻上一页）圆钮的 hover 态。
///
/// ⚠️ 2026-10-12 改：**原来写的是 387，两张接反了**（用户："左右箭头按下图片反了"）。
/// 逐像素看过（`wzldump` 导出 + 只取纯白笔画）：**388 是左箭头**（竖边在 x≈10、
/// 尖端朝左到 x=5，右侧 x11..16 是箭杆），**387 是右箭头**（竖边在 x≈11、尖端朝右
/// 到 x=16，左侧 x5..9 是箭杆）—— 白色重心 388 偏左（9.88）、387 偏右（11.81）。
pub(crate) const BUY_PREV_HOVER: u32 = mir2_core::image_manifest::assets::SHOP_BUY_PREV_HOVER.index;
/// **右**（翻下一页）圆钮的 hover 态（同上：= 387）。
pub(crate) const BUY_NEXT_HOVER: u32 = mir2_core::image_manifest::assets::SHOP_BUY_NEXT_HOVER.index;

/// 一页几行。
pub(crate) const ROWS: usize = 6;
const ROW_H: f32 = 20.0;

/// 面板尺寸 = 素材原生尺寸（不缩放）。
const W: f32 = 308.0;
const H: f32 = 205.0;

/// 三列的列头/列内容 x（窗口内坐标，按 308 宽排）。
///
/// ⚠️ 按 `Prguse[385]` **逐像素量出来的**（2026-10-12，`wzldump` 导出后扫色块）：
/// 内层三块深色面板分别是 **x 10..149（物品）/ 152..238（价格）/ 241..281（持久）**，
/// 可见区域右缘 ≈ 288。文字起点取各面板内缩 2px；原先 16/150/240 是"按 308 宽排"的
/// 估値 ⇒ 行矩形一直伸到 304、压出窗外（用户 2026-10-12：列表过宽）。
const COL_NAME: f32 = 12.0;
const COL_PRICE: f32 = 154.0;
const COL_DURA: f32 = 243.0;

/// 表头与首行的 y。
const HEAD_Y: f32 = 8.0;
const LIST_Y: f32 = 30.0;

/// 列表行的矩形（窗口内）：从左面板左缘（10）到可见区域右缘（≈286）。
///
/// ⚠️ 画与命中同源（[`row_rect`] 只此一份）—— 别再按"308 − 边距"估宽度。
const LIST_X: f32 = 10.0;
const LIST_W: f32 = 276.0;

/// 素材自带的区（窗口内矩形）—— 位置见 [`BUY_BG`] 那条说明。
pub(crate) const CLOSE: (f32, f32, f32, f32) = (291.0, 1.0, 16.0, 20.0);
/// **OK**（右下；底图里同色块 x 216..255 / y 172..184，命中区略放大一圈）。
pub(crate) const OK: (f32, f32, f32, f32) = (214.0, 170.0, 44.0, 17.0);
/// **左**圆钮（底图里圆心 (54,187)，24×24）。
pub(crate) const PREV_R: (f32, f32, f32, f32) = (42.0, 175.0, 24.0, 24.0);
/// **右**圆钮（底图里圆心 (101,187)，24×24）。
pub(crate) const NEXT_R: (f32, f32, f32, f32) = (89.0, 175.0, 24.0, 24.0);
const PREV: (f32, f32, f32, f32) = PREV_R;
const NEXT: (f32, f32, f32, f32) = NEXT_R;

/// 文字颜色（与对话窗同一套：表头/价格黄、正文近白）。
const C_HEAD: (u8, u8, u8) = (232, 220, 96);
const C_TEXT: (u8, u8, u8) = (238, 238, 214);
const C_SEL: (u8, u8, u8) = (255, 230, 130);

/// 窗的落点：**对话窗正下方**（对话窗占 `8..424` 的上排 ⇒ 这里接在下面）。
///
/// 包裹窗在商店开着时会让位到右边（`layout::bag_pos`），正好把这个位置让出来 ——
/// 与原版截图同款布局：左上对话、左下商品列表、右侧包裹。
pub(crate) fn panel() -> (f32, f32, f32, f32) {
    let (dx, dy, dw, dh) = crate::dialog::dialog_panel();
    // 与卖窗同一条：**右对齐**上方对话窗
    (dx + dw - W, dy + dh + 8.0, W, H)
}

/// 第 `row` 行（**页内下标**）在窗口内的矩形。画与命中同源。
pub(crate) fn row_rect(row: usize) -> (f32, f32, f32, f32) {
    (LIST_X, LIST_Y + row as f32 * ROW_H, LIST_W, ROW_H)
}

/// 一共几页（服务端一个商人最多 20 件 ⇒ 4 页）。
pub(crate) fn pages(items: usize) -> usize {
    items.div_ceil(ROWS).max(1)
}

/// 鼠标点中了什么（窗口内坐标）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Hit {
    /// 第 `row` 行（页内下标）⇒ **选中**它（买要再点 OK）。
    Row(usize),
    Prev,
    Next,
    /// 底部左边那个按钮（素材自带）⇒ 买选中的那件。
    Ok,
    /// 右上角红叉 / 底部右边那个按钮 ⇒ 关窗。
    Close,
    None,
}

/// 窗口内的一点落在哪里。与 [`row_rect`] 同源。
pub(crate) fn hit(local: (f32, f32), rows: usize) -> Hit {
    let (lx, ly) = local;
    let inside =
        |r: (f32, f32, f32, f32)| lx >= r.0 && lx < r.0 + r.2 && ly >= r.1 && ly < r.1 + r.3;
    // ⚠️ 只有**右上角红叉**关窗：原来还有个自绘的“取消”框压在右圆钮上
    // ⇒ 点“右/下一页”会变成关窗。底图里根本没有第二个关闭钮。
    if inside(CLOSE) {
        return Hit::Close;
    }
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

/// 画商店窗：表头三列 + 商品行（选中高亮）+ 左右翻页 + OK。
#[allow(clippy::too_many_arguments)]
pub(crate) fn draw<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut ui::UiCache<'a>,
    texts: &mut crate::font::TextCache<'a>,
    dir: &Path,
    net: Option<&Net>,
    page: usize,
    selected: Option<usize>,
    // 鼠标**按住**着哪个控件（高亮图只在按住时展现 —— 原版 `TDButton.Downed`，
    // 用户 2026-10-11 更正：不是 hover）。
    // 控件引擎（hover / 按下都由它算 ⇒ 不再单独传"按住的是谁"）
    widget_ui: &crate::widget::Ui,
) -> Result<(), sdl3::Error> {
    let Some(n) = net else { return Ok(()) };
    if !n.world.in_world() {
        return Ok(());
    }
    let Some(shop) = n.world.shop.as_ref() else {
        return Ok(()); // 没有货架（没点过"交易市场" / 对话关了）⇒ 不画
    };
    let (x, y, w, h) = panel();
    // 底图 = `Prguse[385]`（原生尺寸贴上去：窗框、右上红叉、底部两个按钮都在图里），
    // 我们只往上面排文字与商品行。
    let (iw, ih) = ui
        .size(dir, "Prguse", BUY_BG)
        .expect("素材清单要求 Prguse[385] 买窗背景");
    assert_eq!((iw as f32, ih as f32), (w, h), "买窗素材尺寸与布局不一致");
    let _ = ui.draw_src(
        canvas,
        tc,
        dir,
        "Prguse",
        BUY_BG,
        FRect::new(0.0, 0.0, iw as f32, ih as f32),
        FRect::new(x, y, w, h),
        255,
    );

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
        // 选中行给一条亮底（原版选中就是整行点亮；贴着量出来的列表区，别压到窗框）
        if selected == Some(base + i) {
            fill(
                canvas,
                x + rx,
                y + ry,
                rw,
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

    // **三个按钮走控件层**（`widget::Button`，图号 386/387/388 是用户 2026-10-11 给的
    // 高亮图；常态在底图里）。命中与绘制同源 —— 都由 [`buy_buttons`] 那份描述决定。
    for b in buy_buttons(x, y) {
        crate::widget::draw_button(canvas, tc, ui, dir, &b, &b.response(widget_ui));
    }
    // 页码（紧跟两个圆钮右边）
    let page_text = format!("{}/{}", page + 1, pages(shop.items.len()));
    texts.draw(
        canvas,
        tc,
        &page_text,
        x + 124.0,
        y + 180.0,
        (190, 170, 120),
        None,
    )?;
    Ok(())
}

/// 交易窗（买）的三个按钮 + 关闭钮（**窗口内** → 屏幕坐标在这里换算一次）。
///
/// ⚠️ 抽出来是为了"画"和"命中"用同一份（官方 `TDControl` 的矩形即命中区）。
pub(crate) fn buy_buttons(ox: f32, oy: f32) -> [crate::widget::Button; 4] {
    use crate::widget::{Button, ButtonArt};
    let mk = |r: (f32, f32, f32, f32), k: Hit, art: ButtonArt| Button {
        key: crate::Press::Shop(k),
        rect: crate::widget::offset_local_to_canvas(
            r,
            crate::space::CPt(crate::space::Cpx(ox), crate::space::Cpx(oy)),
        ),
        art,
        enabled: true,
    };
    [
        // [OK] 的高亮图 386
        mk(
            OK,
            Hit::Ok,
            ButtonArt::highlight_only(("Prguse", BUY_OK_HOVER)),
        ),
        // 左 / 右翻页的高亮图 387 / 388
        mk(
            PREV_R,
            Hit::Prev,
            ButtonArt::highlight_only(("Prguse", BUY_PREV_HOVER)),
        ),
        mk(
            NEXT_R,
            Hit::Next,
            ButtonArt::highlight_only(("Prguse", BUY_NEXT_HOVER)),
        ),
        // 素材自带的关闭钮（没单独高亮图）
        mk(CLOSE, Hit::Close, ButtonArt::default()),
    ]
}

const C_PRICE_COL: (u8, u8, u8) = (232, 220, 96);

// ---------- 卖货窗（原版拖放式，用户 2026-10-10 第 3 条） ----------
//
// 流程照原版截图：点背包里的物品**抓到手上** → 移到这个窗的**放物品槽**里点一下放下
// → 物品显示在槽中间、"卖:"后面显示能卖多少钱 → 点 **OK** 才真卖。
//
// 窗的位置：照原版截图（用户 2026-10-10「卖.png」）它不是一块大板，是**三个浮件**
// ——「"卖:"横条（带红 X）」挂对话窗正下方，放物品的圆槽**悬挂**在条下，OK 在圈右下。

/// 卖货窗的落点与尺寸 = 素材 `Prguse[392]` 的原生尺寸（140×181），不再自留边距。
pub(crate) fn sell_panel() -> (f32, f32, f32, f32) {
    let (dx, dy, dw, dh) = crate::dialog::dialog_panel();
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
pub(crate) const SELL_SLOT_IMG: u32 = mir2_core::image_manifest::assets::SHOP_SELL_BACKGROUND.index;
/// **OK 的高亮态** = `Prguse[393]`（24×13，用户 2026-10-11 第 3 条给的编号；
/// 逐像素看过：蓝底白字"OK"，比底图 392 里那枚亮）。
pub(crate) const SELL_OK_IMG: u32 = mir2_core::image_manifest::assets::SHOP_SELL_OK_HOVER.index;
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
/// ⚠️ 圆在素材的**中下部偏左**（链条从顶部标题栏垂下来，圆吊在下面）——
/// 用户 2026-10-10 实测「偏右约 10-20 像素」⇒ x 从 72 收到 57。
pub(crate) const SELL_CIRCLE: (f32, f32, f32) = (57.0, 94.0, 54.0);
/// 金额文字的位置（素材标题栏上；**只画文字**，不自绘控件）。
pub(crate) const SELL_TITLE: (f32, f32) = (48.0, 4.0);

/// 卖货窗里点中了什么。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
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
    // 鼠标**按住**着哪个控件（高亮图只在按住时展现；见 `Hit` 那条的说明）。
    // 控件引擎（hover / 按下都由它算 ⇒ 不再单独传"按住的是谁"）
    widget_ui: &crate::widget::Ui,
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
    let (iw, ih) = ui
        .size(dir, "Prguse", SELL_SLOT_IMG)
        .expect("素材清单要求 Prguse[392] 卖窗背景");
    assert_eq!((iw as f32, ih as f32), (sw, sh), "卖窗素材尺寸与布局不一致");
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
    // **三个按钮走控件层**（`widget::Button`）：常态都画在底图 `Prguse[392]` 里，
    // 只有 [OK] 那张高亮图 393 是单独的（用户 2026-10-11 第 3 条）。
    // 命中与绘制同源 —— 都由 [`sell_buttons`] 那份描述决定。
    for b in sell_buttons(x, y) {
        crate::widget::draw_button(canvas, tc, ui, dir, &b, &b.response(widget_ui));
    }
    Ok(())
}

/// 卖货窗的三个按钮（**窗口内** → 屏幕坐标的换算在这里做一次）。
///
/// ⚠️ 抽出来是为了"画"和"命中"用同一份（官方 `TDControl` 的矩形即命中区）。
pub(crate) fn sell_buttons(ox: f32, oy: f32) -> [crate::widget::Button; 3] {
    use crate::widget::{Button, ButtonArt};
    let mk = |r: (f32, f32, f32, f32), k: SellHit, art: ButtonArt| Button {
        key: crate::Press::Sell(k),
        rect: crate::widget::offset_local_to_canvas(
            r,
            crate::space::CPt(crate::space::Cpx(ox), crate::space::Cpx(oy)),
        ),
        art,
        enabled: true,
    };
    [
        // 素材自带的红叉（没单独的高亮图 ⇒ 常态/按下都不额外画）
        mk(SELL_CLOSE, SellHit::Close, ButtonArt::default()),
        // 放物品的槽
        mk(SELL_SLOT, SellHit::Slot, ButtonArt::default()),
        // [OK]：393 是高亮（按下）图
        mk(
            SELL_OK,
            SellHit::Ok,
            ButtonArt::highlight_only(("Prguse", SELL_OK_IMG)),
        ),
    ]
}

// ---------- 买/卖数量面板（D-148） ----------
//
// ⚠️ 原版**没有**"买几个"的输入框（`submenu` 是商品明细列表，见 D-148 订正）；这块板
// 属私服便利，与 `confirm` 窗同一档自绘样式，四颗按钮走控件层。
// 键盘：面板是模态 ⇒ 数字键直接打件数、退格删一位（D-151），不会和快捷栏 1..6 打架。

pub(crate) const QTY_W: f32 = 176.0;
pub(crate) const QTY_H: f32 = 84.0;
/// `[−]`：数字左边。
const QTY_MINUS: (f32, f32, f32, f32) = (8.0, 42.0, 26.0, 20.0);
/// `[+]`：数字右边。
const QTY_PLUS: (f32, f32, f32, f32) = (142.0, 42.0, 26.0, 20.0);
const QTY_OK: (f32, f32, f32, f32) = (22.0, 64.0, 62.0, 16.0);
const QTY_CANCEL: (f32, f32, f32, f32) = (92.0, 64.0, 62.0, 16.0);

/// 面板落点：**买窗正下方**那一块（买窗 116..424 × 134..339，下面是空的；
/// 包裹在右侧 436 起、卖窗在 284..424，都不撞）。卖模式下买窗不画，这块同样空着。
pub(crate) fn qty_panel() -> (f32, f32, f32, f32) {
    let (px, py, pw, ph) = panel();
    (px + (pw - QTY_W) / 2.0, py + ph + 6.0, QTY_W, QTY_H)
}

/// 数量面板的四颗按钮（窗口内 → 画布坐标做一次）。
pub(crate) fn qty_buttons(ox: f32, oy: f32) -> [crate::widget::Button; 4] {
    use crate::qty::Hit as Q;
    use crate::widget::{Button, ButtonArt};
    let mk = |r: (f32, f32, f32, f32), k: Q| Button {
        key: crate::Press::Qty(k),
        rect: crate::widget::offset_local_to_canvas(
            r,
            crate::space::CPt(crate::space::Cpx(ox), crate::space::Cpx(oy)),
        ),
        // 常态/按下都由 `draw_qty` 自己上色（没有官方图）
        art: ButtonArt::default(),
        enabled: true,
    };
    [
        mk(QTY_MINUS, Q::Minus),
        mk(QTY_PLUS, Q::Plus),
        mk(QTY_OK, Q::Ok),
        mk(QTY_CANCEL, Q::Cancel),
    ]
}

/// 画数量面板：标题 + "共 N" + 中间那个件数 + 四颗按钮。
///
/// 只画，不改状态；按钮的高亮来自控件引擎（`Response`）⇒ 与点击同一套命中。
pub(crate) fn draw_qty<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    texts: &mut crate::font::TextCache<'a>,
    q: &crate::qty::Qty,
    widget_ui: &crate::widget::Ui,
) -> Result<(), sdl3::Error> {
    let (x, y, w, h) = qty_panel();
    fill(canvas, x, y, w, h, Color::RGBA(12, 14, 20, 236))?;
    // 1px 金边（四根线；不画整框填充，省得盖住底下的对话文字）
    let edge = Color::RGBA(154, 126, 62, 255);
    fill(canvas, x, y, w, 1.0, edge)?;
    fill(canvas, x, y + h - 1.0, w, 1.0, edge)?;
    fill(canvas, x, y, 1.0, h, edge)?;
    fill(canvas, x + w - 1.0, y, 1.0, h, edge)?;

    texts.draw(canvas, tc, q.title(), x + 8.0, y + 5.0, C_HEAD, None)?;
    texts.draw(
        canvas,
        tc,
        &format!("共 {}", q.total()),
        x + 8.0,
        y + 24.0,
        (190, 170, 120),
        None,
    )?;
    // 正在用键盘打件数时后缀一个下划线当光标（不然看不出"还能继续打"）
    let shown = if q.typed().is_empty() {
        q.count().to_string()
    } else {
        format!("{}_", q.count())
    };
    texts.draw_centered(canvas, tc, &shown, x + w / 2.0, y + 45.0, C_SEL, None)?;

    for b in qty_buttons(x, y) {
        let r = b.response(widget_ui);
        let bg = if r.pressed {
            Color::RGBA(74, 62, 30, 255)
        } else if r.hovered {
            Color::RGBA(44, 40, 26, 255)
        } else {
            Color::RGBA(26, 26, 34, 255)
        };
        fill(
            canvas,
            b.rect.0 .0,
            b.rect.1 .0,
            b.rect.2 .0,
            b.rect.3 .0,
            bg,
        )?;
        fill(
            canvas,
            b.rect.0 .0,
            b.rect.1 .0,
            b.rect.2 .0,
            1.0,
            Color::RGBA(96, 84, 52, 255),
        )?;
        let label = match b.key {
            crate::Press::Qty(crate::qty::Hit::Minus) => "-",
            crate::Press::Qty(crate::qty::Hit::Plus) => "+",
            crate::Press::Qty(crate::qty::Hit::Ok) => "确定",
            crate::Press::Qty(crate::qty::Hit::Cancel) => "取消",
            _ => "",
        };
        texts.draw_centered(
            canvas,
            tc,
            label,
            b.rect.0 .0 + b.rect.2 .0 / 2.0,
            b.rect.1 .0 + 4.0,
            C_TEXT,
            None,
        )?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 数量面板落在买窗下方且不压包裹与卖窗() {
        let (qx, qy, qw, qh) = qty_panel();
        let (px, py, pw, ph) = panel();
        assert!(qy >= py + ph, "应在买窗下方（否则盖住货架）");
        assert!(qx >= px && qx + qw <= px + pw, "横向落在买窗范围内");
        // 与 `layout::bag_pos(shop_open = true)` 同一条：包裹 x = 卖窗右缘 + 12
        let (sx, _, sw, _) = sell_panel();
        assert!(qx + qw <= sx + sw + 12.0, "右缘不能压到包裹窗");
        assert!(qy + qh <= crate::WIN_H as f32, "不能掉出屏幕");
    }

    #[test]
    fn 数量面板四颗按钮互不重叠且都在面板内() {
        let (px, py, pw, ph) = qty_panel();
        let bs = qty_buttons(px, py);
        for (i, b) in bs.iter().enumerate() {
            let (x, y, w, h) = (b.rect.0 .0, b.rect.1 .0, b.rect.2 .0, b.rect.3 .0);
            assert!(x >= px && x + w <= px + pw, "第 {i} 颗超出面板横向");
            assert!(y >= py && y + h <= py + ph, "第 {i} 颗超出面板纵向");
            for other in &bs[i + 1..] {
                let overlap = x < other.rect.0 .0 + other.rect.2 .0
                    && other.rect.0 .0 < x + w
                    && y < other.rect.1 .0 + other.rect.3 .0
                    && other.rect.1 .0 < y + h;
                assert!(!overlap, "第 {i} 颗与后面的按钮重叠了");
            }
        }
    }
}

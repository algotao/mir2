//! **纯对话排版与几何模型**：正文解析、折行、滚动和链接命中。
//!
//! 不依赖 SDL / 字体缓存。字宽由调用方注入 `measure`，因此画和命中仍消费同一套
//! `DialPiece` 几何；UI 事件适配和实际绘制留在 `world_input` / `hud`。

/// 对话窗的**版式常量** —— 照抄官方（用户 2026-10-09 给的截图）。
///
/// 与旧版的区别：旧版是自己造的"居中悬浮半透明黑板" ✗；官方是**一块固定尺寸、
/// 固定在屏幕左上角**的背板（`Prguse[384]`，416×176 —— 就是图1/图2/图4 那张
/// "青铜框 + 深色内里"），正文在左上、选项排在正文下面（黄字 + 绿点、**不编号**）。
///
/// 背板是原生像素尺寸，**不缩放**（用户："界面显示文字在 1024×768 下不要缩放" ——
/// 背板也一样，画成 416×176 正好是官方大小）。
pub(crate) const DIALOG_BG: u32 = mir2_core::image_manifest::assets::NPC_DIALOG_BACKGROUND.index;

/// 背板尺寸（`Prguse[384]` 的原生尺寸）。
pub(crate) const DIALOG_W: f32 = 416.0;
pub(crate) const DIALOG_H: f32 = 176.0;

/// 落点：**贴屏幕左上角**。官方就是贴角（留 8/4 的缝才不压住窗口边）。
pub(crate) const DIALOG_X: f32 = 8.0;
pub(crate) const DIALOG_Y: f32 = 4.0;

/// 正文在背板里的内边距（避开青铜框内沿）。
pub(crate) const DIALOG_PAD_X: f32 = 24.0;
pub(crate) const DIALOG_PAD_Y: f32 = 16.0;

/// 行高（14px 字 + 4px 行距，与截图里的疏密一致）。
///
/// 正文与行内选项**同一行高** —— 它们本来就同行（以前那份"底部选项列表"是另一套）。
pub(crate) const DIALOG_LINE_H: f32 = 18.0;

/// 正文最多画几行 —— **按背板几何算出来**，不是拍脑袋定的。
///
/// 背板固定高 176、上下各留 `DIALOG_PAD_Y` ⇒ `(176-32)/18 = 8` 行。
///
/// ⚠️ 原来写死 5，结果陈家铺老板那段（正文 1 行 + 空行 + 5 个行内选项 = 7 行）
/// **把「退出」截掉了** —— 用户 2026-10-09：「对话的『退出』按钮怎么没有？」
/// 就是这个问题。改成按几何算，背板尺寸一变它自己跟着变，不会再漂。
pub(crate) const DIALOG_MAX_LINES: usize =
    ((DIALOG_H - 2.0 * DIALOG_PAD_Y) / DIALOG_LINE_H) as usize;

/// 正文装不下时**最多能滚几行**。
///
/// ⚠️ 为什么非滚不可：脚本的 `[@main]` 折行之后**远超**一屏 —— 2026-10-10 量过
/// 298 个脚本，**114 个**超过 8 行，最长的 `2Arms_dealer-0103` 有 **46 行**
///（而且折行只算纯文字行）。背板是固定高的（`Prguse[384]` 416×176）⇒
/// 换成更高的板也装不下，只能滚。滚不动的后果是**装不下的行既画不出也点不到** ——
/// 「退出」常常就落在第 9、10 行（用户 2026-10-10 报的次生问题）。
pub(crate) fn dialog_max_scroll(lines: usize) -> usize {
    lines.saturating_sub(DIALOG_MAX_LINES)
}

/// 把滚动位置夹回合法范围（换了段更短的对话、或滚过头了都要用它）。
pub(crate) fn dialog_scroll_clamp(scroll: usize, lines: usize) -> usize {
    scroll.min(dialog_max_scroll(lines))
}

/// 滚轮一格（`y` 是 SDL 的滚轮量：**> 0 = 往上滚**）。
///
/// 抽成纯函数是为了能单测（事件循环里不好造 `Event`）。
pub(crate) fn dialog_scroll_step(scroll: usize, lines: usize, y: f32) -> usize {
    let cur = dialog_scroll_clamp(scroll, lines);
    if y > 0.0 {
        cur.saturating_sub(1)
    } else if y < 0.0 {
        dialog_scroll_clamp(cur + 1, lines)
    } else {
        cur
    }
}

/// 滚动条的宽度（细条，贴在正文区右侧）。
pub(crate) const DIALOG_BAR_W: f32 = 4.0;

/// 滚动条与正文右缘的间距。
const DIALOG_BAR_GAP: f32 = 8.0;

/// 滚动条的**轨道**矩形（一直存在，只是内容装得下时不画）。
pub(crate) fn dialog_bar_track(panel: (f32, f32, f32, f32)) -> (f32, f32, f32, f32) {
    let (px, py, pw, _) = panel;
    (
        px + pw - DIALOG_BAR_GAP - DIALOG_BAR_W,
        py + DIALOG_PAD_Y,
        DIALOG_BAR_W,
        DIALOG_MAX_LINES as f32 * DIALOG_LINE_H,
    )
}

/// 滚动条的**滑块**矩形：`None` = 内容装得下、不用画。
///
/// 滑块高度按"可见 / 总数"的比例缩（内容越长滑块越短，与常见滚动条一致），
/// 最短 12px —— 46 行内容按 8/46 缩出来只有 ~4px，手指看不清。
pub(crate) fn dialog_scroll_thumb(
    panel: (f32, f32, f32, f32),
    lines: usize,
    scroll: usize,
) -> Option<(f32, f32, f32, f32)> {
    if lines <= DIALOG_MAX_LINES {
        return None;
    }
    let (tx, ty, tw, th) = dialog_bar_track(panel);
    let visible = DIALOG_MAX_LINES as f32;
    let total = lines as f32;
    let h = (th * visible / total).max(12.0).min(th);
    let room = (th - h).max(0.0);
    let s = dialog_scroll_clamp(scroll, lines) as f32;
    let max_s = dialog_max_scroll(lines) as f32;
    let y = ty + if max_s > 0.0 { room * s / max_s } else { 0.0 };
    Some((tx, y, tw, h))
}

/// 正文一行最多几个字。背板内宽 ≈ `416 - 2*24 = 368px`，14px 一个字 ⇒ 约 26 个，
/// 留点余量取 24（`\n` 仍然强制换行）。
pub(crate) const DIALOG_WRAP_CHARS: usize = 24;

/// 关闭按钮（右上角那个红 X）在**面板内**的位置。
///
/// 量自 `Prguse[384]` 的像素（`wzldump` 扫出来）：红叉落在 `x 401..411`、`y 0..15`，
/// 命中框放宽一圈，手指点得中。
pub(crate) const DIALOG_CLOSE_X: f32 = 397.0;
pub(crate) const DIALOG_CLOSE_Y: f32 = 0.0;
pub(crate) const DIALOG_CLOSE_W: f32 = 19.0;
pub(crate) const DIALOG_CLOSE_H: f32 = 19.0;

/// 鼠标点在右上角的关闭 X 上吗。
pub(crate) fn dialog_close_hit(panel: (f32, f32, f32, f32), mouse: (f32, f32)) -> bool {
    let (px, py, _, _) = panel;
    let (lx, ly) = (mouse.0 - px, mouse.1 - py);
    (DIALOG_CLOSE_X..DIALOG_CLOSE_X + DIALOG_CLOSE_W).contains(&lx)
        && (DIALOG_CLOSE_Y..DIALOG_CLOSE_Y + DIALOG_CLOSE_H).contains(&ly)
}

/// 对话面板的矩形 `(x, y, w, h)`（画布坐标）—— 固定尺寸、固定左上角。
pub(crate) fn dialog_panel() -> (f32, f32, f32, f32) {
    (DIALOG_X, DIALOG_Y, DIALOG_W, DIALOG_H)
}

/// 对话正文里的一个片段：普通文字，或者一段**行内可点文字**。
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum DialSeg {
    Text(String),
    /// `index` 是回包要带的**选项序号**（1 起）。
    ///
    /// 服务端把脚本里的 `<打开/@trading>` 改写成 `<打开/@1>`（见
    /// `server/internal/script/script.go` 的 `Label.Lines`）⇒ 客户端不用猜标签，
    /// 直接拿它回 `NpcSelect{index}`。
    Link {
        index: u32,
        text: String,
    },
}

/// 链接前面那个绿方块 + 间距占的宽度（官方样式：方块在文字左侧）。
pub(crate) const DIALOG_BULLET_W: f32 = 12.0;

/// 把服务端下发的正文切成**行 → 片段**（画与命中都用它，别各算一份）。
///
/// - 正文里带 `<文字/@序号>` ⇒ **留在原行**。用户 2026-10-09：「交易窗口渲染不对，
///   应该为『打开 交易市场』在一行，其中『打开』可点击」—— 脚本本来就是这么写的
///   （` <打开/@trading> 交易市场\`），旧版把链接抽到下面单列，所以看着不对。
/// - 正文里**没有**标记但有 `options`（老服务端 / 没有行内链接的脚本）⇒ 把选项各排一行，
///   与以前那个"底部选项列表"的行为一致。
///
/// ⚠️ 折行：**纯文字行**按 [`DIALOG_WRAP_CHARS`] 折（脚本正文常常很长）；
/// **带链接的行**原样保留 —— 脚本作者自己用 `\` 断行，硬折会把链接与它修饰的
/// 文字拆散（"打开 / 交易市场"分两行就回到老毛病了）。
pub(crate) fn dialog_lines(text: &str, options: &[(u32, String)]) -> Vec<Vec<DialSeg>> {
    let mut lines: Vec<Vec<DialSeg>> = Vec::new();
    let mut any_link = false;
    // ⚠️ 脚本里的 `\` 是**换行**（`[@main]` 一行写不下就换行接着写），不是正文
    // —— 服务端发的是"原样行" ⇒ `\` 会原样带到客户端。用户 2026-10-10 第 3 条：
    // 对话里最后那个反斜杠不该出现 ⇒ 把它当换行吃掉（原版就是换行）。
    let text = text.replace('\\', "\n");
    for raw in text.split('\n') {
        let mut segs = Vec::new();
        let has = parse_marked_line(raw, &mut segs);
        if has {
            any_link = true;
            lines.push(segs);
            continue;
        }
        if raw.trim().is_empty() {
            lines.push(vec![DialSeg::Text(String::new())]); // 空行占位（脚本用它分段）
            continue;
        }
        for chunk in wrap_text(raw.trim(), DIALOG_WRAP_CHARS) {
            lines.push(vec![DialSeg::Text(chunk)]);
        }
    }
    if !any_link && !options.is_empty() {
        for (idx, t) in options {
            lines.push(vec![DialSeg::Link {
                index: *idx,
                text: t.clone(),
            }]);
        }
    }
    lines
}

/// 解析一行里的 `<文字/@序号>`，把片段推进 `out`；返回**本行有没有链接**。
fn parse_marked_line(s: &str, out: &mut Vec<DialSeg>) -> bool {
    let mut rest = s;
    let mut has = false;
    while let Some(lt) = rest.find('<') {
        let Some(gt) = rest[lt..].find('>') else {
            break;
        };
        let inner = &rest[lt + 1..lt + gt];
        let Some((text, index)) = inner
            .split_once("/@")
            .and_then(|(t, n)| n.trim().parse::<u32>().ok().map(|i| (t.to_string(), i)))
        else {
            break; // 不是标记：后面整段当文字
        };
        if lt > 0 {
            out.push(DialSeg::Text(rest[..lt].to_string()));
        }
        out.push(DialSeg::Link { index, text });
        has = true;
        rest = &rest[lt + gt + 1..];
    }
    if !rest.is_empty() {
        out.push(DialSeg::Text(rest.to_string()));
    }
    has
}

/// 一个片段排好之后的落点。
pub(crate) struct DialPiece {
    /// 完整命中矩形（链接**含**前面的绿方块）。
    pub rect: (f32, f32, f32, f32),
    /// 文字落笔的 x（链接是方块之后的那一点）。
    pub text_x: f32,
    pub seg: DialSeg,
}

/// 某一行的排版：`line_no` 从 0 起。
///
/// **画与命中同源** —— `draw_dialog` 与点击判定都调它。装不下的行返回空
///（背板固定高 ⇒ 画不出来的东西也不该点得到）。
pub(crate) fn dialog_line_pieces(
    panel: (f32, f32, f32, f32),
    line_no: usize,
    segs: &[DialSeg],
    measure: &mut dyn FnMut(&str) -> f32,
) -> Vec<DialPiece> {
    let (px, py, _pw, ph) = panel;
    if line_no >= DIALOG_MAX_LINES {
        return Vec::new();
    }
    let y = py + DIALOG_PAD_Y + line_no as f32 * DIALOG_LINE_H;
    if y + DIALOG_LINE_H > py + ph {
        return Vec::new();
    }
    let mut x = px + DIALOG_PAD_X;
    let mut out = Vec::with_capacity(segs.len());
    for seg in segs {
        match seg {
            DialSeg::Text(t) => {
                let w = measure(t);
                out.push(DialPiece {
                    rect: (x, y, w, DIALOG_LINE_H),
                    text_x: x,
                    seg: seg.clone(),
                });
                x += w;
            }
            DialSeg::Link { text, .. } => {
                let w = measure(text) + DIALOG_BULLET_W;
                out.push(DialPiece {
                    rect: (x, y, w, DIALOG_LINE_H),
                    text_x: x + DIALOG_BULLET_W,
                    seg: seg.clone(),
                });
                x += w;
            }
        }
    }
    out
}

/// 鼠标点在某段**行内链接**上 ⇒ 返回选项序号（1 起）。
/// 当前**看得见**的行：`(窗口内行号, 该行的片段)`。
///
/// **画与命中都走它** —— 滚出窗外的行既画不出来也点不到（与 `dialog_line_pieces`
/// 里"装不下就返回空"同一条纪律：画不出来的东西不该点得到）。
pub(crate) fn dialog_visible_lines(
    lines: &[Vec<DialSeg>],
    scroll: usize,
) -> impl Iterator<Item = (usize, &[DialSeg])> {
    let s = dialog_scroll_clamp(scroll, lines.len());
    lines
        .iter()
        .skip(s)
        .take(DIALOG_MAX_LINES)
        .enumerate()
        .map(|(row, segs)| (row, segs.as_slice()))
}

/// 鼠标点在哪条链接上（`scroll` = 当前卷动到第几行）。
pub(crate) fn dialog_link_at(
    panel: (f32, f32, f32, f32),
    lines: &[Vec<DialSeg>],
    mouse: (f32, f32),
    scroll: usize,
    measure: &mut dyn FnMut(&str) -> f32,
) -> Option<u32> {
    for (row, segs) in dialog_visible_lines(lines, scroll) {
        for p in dialog_line_pieces(panel, row, segs, measure) {
            let DialSeg::Link { index, .. } = p.seg else {
                continue;
            };
            let (x, y, w, h) = p.rect;
            if mouse.0 >= x && mouse.0 <= x + w && mouse.1 >= y && mouse.1 <= y + h {
                return Some(index);
            }
        }
    }
    None
}

/// 点是否落在面板里（落在面板里但没点在选项上 ⇒ **别走路**）。
// 点落在对话窗里吗（窗里但没点在选项上 ⇒ 别走路）。目前只有测试用它，
// 但它是「点对话窗不算走路」这条规则的判据，留着。
#[allow(dead_code)]
pub(crate) fn dialog_hit(panel: (f32, f32, f32, f32), mouse: (f32, f32)) -> bool {
    let (x, y, w, h) = panel;
    mouse.0 >= x && mouse.0 <= x + w && mouse.1 >= y && mouse.1 <= y + h
}

/// 把一段文本按**字数**硬折行（中文按字算）。原版靠字体量宽折行，我们先用粗版：
/// 每行 `max` 个字，`\n` 强制换行。返回折好的行。
pub(crate) fn wrap_text(text: &str, max: usize) -> Vec<String> {
    let mut out = Vec::new();
    for raw in text.split('\n') {
        let chars: Vec<char> = raw.chars().collect();
        if chars.is_empty() {
            out.push(String::new());
            continue;
        }
        for chunk in chars.chunks(max.max(1)) {
            out.push(chunk.iter().collect());
        }
    }
    out
}

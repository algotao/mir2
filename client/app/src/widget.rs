//! 界面控件层 —— 照官方 `DWinCtl.pas` 的 `TDControl` 体系来，但用 Rust 的方式落地。
//!
//! 官方那一套（我们照它的语义，不照它的继承）：
//!
//! ```text
//! TDControl   rect / Visible / WLib+FaceIndex / SurfaceX(父偏移) / InRange(hit)
//!             OnDirectPaint / OnMouseMove / OnMouseDown / OnMouseUp / OnClick / OnDblClick
//!   ├─ TDButton   Downed（按下态）
//!   ├─ TDWindow   Floating（可拖）
//!   └─ TDGrid     ColCount/RowCount/…（格子）
//! ```
//!
//! ⚠️ Rust 里**不照搬回调**：事件用**返回值**（[`Response`]）而不是闭包 —— 闭包要
//! 抓 `&mut` 一堆状态，在这套"几何在 `layout.rs`、动作在主循环里 match"的结构下
//! 会非常别扭。控件**只是数据**（属性），交互由 [`Ui`] 这一个引擎统一算。
//!
//! 坐标：**控件引擎一律 `CPt/CRect` 画布坐标**（1024×768）。登录/选角的布局仍在
//! 设计空间 800×600 中计算，但进入 `Ui::press` **之前**显式转成画布 `CRect`，
//! 鼠标始终以画布 `CPt` 进入引擎；设计/画布/窗口局部三种坐标不能混用（D-64）。

use crate::ui::UiCache;
use sdl3::render::{FRect, TextureCreator, WindowCanvas};
use std::path::Path;

pub(crate) use crate::widget_state::{
    canvas_pt, canvas_rect, design_rect, offset_local_to_canvas, Press, Rect, Response, Ui,
};

/// 一张界面图（容器 + 图号）—— 官方的 `WLib` + `FaceIndex`。
pub(crate) type Img = (&'static str, u32);

// ---------- Button ----------

/// 一个按钮的几张图。
///
/// ⚠️ **官方只有"常态 + 按下态"两张**（`FaceIndex` 与 `FaceIndex+1`），**没有 hover**
/// —— 用户 2026-10-11 敲定：**hover 允许配，但默认不配**（`None`）⇒ 行为与官方一致。
///
/// `normal == None` 是合法的：很多按钮的**常态就画在窗口底图里**，只有高亮/按下
/// 那张才是单独的图（底栏 8-11、交易窗 386-388、卖窗 393 都是这种）。
#[derive(Debug, Clone, Copy, Default)]
pub(crate) struct ButtonArt {
    /// 常态（`None` = 常态在底图里 ⇒ 什么都不画）。
    pub normal: Option<Img>,
    /// 按下态（高亮图）。
    pub down: Option<Img>,
    /// 悬停态（**默认 `None`**，官方没有；要就配）。
    pub hover: Option<Img>,
    /// 禁用态。
    pub disabled: Option<Img>,
}

impl ButtonArt {
    /// 只有一张"按下/高亮"图（常态在底图里）—— 官方最常见的那种。
    pub(crate) fn highlight_only(down: Img) -> Self {
        Self {
            normal: None,
            down: Some(down),
            hover: None,
            disabled: None,
        }
    }

    /// 只有一张常态图（没有高亮图 —— 比如状态窗的翻页箭头）。
    pub(crate) fn plain(normal: Img) -> Self {
        Self {
            normal: Some(normal),
            down: None,
            hover: None,
            disabled: None,
        }
    }

    /// 常态图 + 按下图（左下那排快捷钮那种）。
    pub(crate) fn two_state(normal: Img, down: Img) -> Self {
        Self {
            normal: Some(normal),
            down: Some(down),
            hover: None,
            disabled: None,
        }
    }

    /// 按当前状态挑该画哪张。
    pub(crate) fn pick(self, r: &Response, enabled: bool) -> Option<Img> {
        if !enabled {
            return self.disabled.or(self.normal);
        }
        if r.pressed {
            self.down.or(self.normal)
        } else if r.hovered {
            self.hover.or(self.normal)
        } else {
            self.normal
        }
    }
}

/// 一个按钮（**只是数据**：谁来画、点中算什么，都由调用方决定）。
#[derive(Debug, Clone, Copy)]
pub(crate) struct Button {
    /// 按下/点击的标识。
    pub key: Press,
    pub rect: Rect,
    pub art: ButtonArt,
    /// 禁用（画 `art.disabled`，且不响应点击）。
    pub enabled: bool,
}

impl Button {
    /// 算这一帧的交互结果（不画）。
    pub(crate) fn response(&self, ui: &Ui) -> Response {
        Response {
            hovered: ui.hovered(self.rect),
            pressed: ui.held() == Some(self.key),
            clicked: false, // OnClick 是"松开"那一刻的事，由 `Ui::release` 给
        }
    }
}

/// 画一个按钮（**画布坐标**，走 `draw_src` 不做换算 —— 见 `hud::hud_img` 那条说明）。
pub(crate) fn draw_button<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui_img: &mut UiCache<'a>,
    dir: &Path,
    b: &Button,
    r: &Response,
) {
    let Some((lib, idx)) = b.art.pick(r, b.enabled) else {
        return; // 常态在底图里、又没在按下 ⇒ 什么都不画
    };
    let Some((iw, ih)) = ui_img.size(dir, lib, idx) else {
        return;
    };
    let _ = ui_img.draw_src(
        canvas,
        tc,
        dir,
        lib,
        idx,
        FRect::new(0.0, 0.0, iw as f32, ih as f32),
        FRect::new(b.rect.0 .0, b.rect.1 .0, iw as f32, ih as f32),
        255,
    );
}

// ---------- Grid（官方 TDGrid）----------

/// 网格（`TDGrid`）：列数/行数 + 格子尺寸 ⇒ `cell_rect(i)` / `hit_cell(p)`。
///
/// ⚠️ 官方的 `TDGrid` 语义是**选中 / 拖拽起点**，不是 `OnClick` 按钮 ⇒
/// 它不进 [`Press`]；而且背包网格的原点是**窗口局部坐标**，所以使用 `LRect/LPt`，
/// 不能把它伪装成按钮使用的全局画布 `Rect`。
#[derive(Debug, Clone, Copy)]
pub(crate) struct Grid {
    origin: crate::space::LPt,
    cols: usize,
    rows: usize,
    cell_w: crate::space::Lpx,
    cell_h: crate::space::Lpx,
}

impl Grid {
    pub(crate) fn new(
        origin: crate::space::LPt,
        cols: usize,
        rows: usize,
        cell_w: crate::space::Lpx,
        cell_h: crate::space::Lpx,
    ) -> Self {
        Self {
            origin,
            cols,
            rows,
            cell_w,
            cell_h,
        }
    }

    /// 格子总数。
    pub(crate) fn len(&self) -> usize {
        self.cols * self.rows
    }

    /// 第 `i` 格的**窗口局部**矩形。
    pub(crate) fn cell_rect(&self, i: usize) -> crate::space::LRect {
        let (c, r) = (i % self.cols, i / self.cols);
        (
            crate::space::Lpx(self.origin.0 .0 + c as f32 * self.cell_w.0),
            crate::space::Lpx(self.origin.1 .0 + r as f32 * self.cell_h.0),
            self.cell_w,
            self.cell_h,
        )
    }

    /// `p` 落在第几格（`None` = 不在网格里）。与 [`Grid::cell_rect`] **同源**。
    pub(crate) fn hit_cell(&self, p: crate::space::LPt) -> Option<usize> {
        (0..self.len()).find(|i| crate::space::hit_local(self.cell_rect(*i), p))
    }
}

// ---------- 一个物品格（拖拽源 + 放置目标）----------

/// 物品从哪个槽位来（官方 `g_MovingItem.Index` 的正规化）。
///
/// ⚠️ **下一步**才接线：现在"手上拿着东西"还是 `sell_held: Option<usize>` +
/// `sell_placed: Option<usize>`（只认背包格）⇒ 换成 `Option<Drag>` 之后，
/// 背包 ↔ 卖窗槽 ↔ 腰带 ↔ 装备槽 就能共用一套拖拽，不用每种组合各写一遍。
///
/// ⚠️ 现在只有 `Bag` / `Sell` / `Belt` / `Equip` 四种；以后加仓库/交易窗只要加变体，
/// 拖拽逻辑不用再改 —— 这正是把"手上拿着东西"从一个 `Option<usize>` 升级成
/// 带**来源标记**的结构的意义。
#[allow(dead_code)] // 见上面"下一步"（迁移完 `sell_held` 就去掉）
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SlotRef {
    /// 背包第 `idx` 格（**服务端那一格的下标**，不是页内下标）。
    Bag(usize),
    /// 卖货窗那个圆槽。
    Sell,
    /// 快捷（腰带）栏第 `i` 格。
    Belt(usize),
    /// 装备槽（服务端槽位下标）。
    Equip(usize),
}

/// **拖拽会话**（官方 `g_boItemMoving` + `g_MovingItem` 的正规化）。
///
/// 一个"手上拿着东西"的状态 = 拿着的**是什么**（`make` 实例号）+ **从哪来**（`from`）。
///
/// ⚠️ `from` 里的**下标会过期**：服务端随时可能重排背包（用了东西、买了东西、
/// 捡了地上的）⇒ 那一格可能已经换了东西。这时若还按老下标去"放下/卖出"，
/// 动的会是**别的物品**（卖错东西不可恢复）⇒ 每帧用 [`Drag::rebind`] 按
/// 实例号重新定位（用户 2026-10-11 盲查发现，见 D-75）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Drag {
    pub from: SlotRef,
    /// 物品实例号（服务端 `MakeIndex`）—— 认物**只认它**，不认下标。
    pub make: i32,
}

impl Drag {
    pub(crate) fn new(from: SlotRef, make: i32) -> Self {
        Self { from, make }
    }

    /// **按实例号重新定位**（背包被服务端重排之后）。
    ///
    /// `find(make) -> 新的下标`；找不到了 ⇒ `None`（这件东西没了：用光/卖掉/被拿走）
    /// ⇒ 调用方把"手上拿着"清掉，免得继续拿着一个幽灵。
    pub(crate) fn rebind(self, find: impl Fn(i32) -> Option<usize>) -> Option<Self> {
        let i = find(self.make)?;
        Some(Self {
            from: SlotRef::Bag(i),
            make: self.make,
        })
    }

    /// **卖窗圆槽的点按**（原版 `DSellItemSlotDirectPaint` 的语义）：
    /// 空手点槽 = 把槽里那件**拿回手上**；拿着东西点槽 = **放进去**。
    /// 返回 `(手上, 槽里)`。
    ///
    /// ⚠️ 抽出来是因为它原来埋在事件循环里（测不到）；"同时拿着又放着"
    /// 是不该出现的状态，这里保证不会出现。
    pub(crate) fn toggle_slot(
        held: Option<Drag>,
        placed: Option<Drag>,
    ) -> (Option<Drag>, Option<Drag>) {
        match (held, placed) {
            (Some(d), None) => (None, Some(d)),
            (None, Some(d)) => (Some(d), None),
            (h, p) => (h, p),
        }
    }

    /// 它是从背包第几格拿起来的（不是背包来的 ⇒ `None`）。
    pub(crate) fn bag_index(self) -> Option<usize> {
        match self.from {
            SlotRef::Bag(i) => Some(i),
            _ => None,
        }
    }
}

// ---------- Parent / align / clip / layer layout ----------

/// 沿一个轴贴齐父控件：起始边 / 居中 / 结束边。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum AxisAlign {
    Start,
    Center,
    End,
}

/// 锚点对齐属性。rect 的 x/y 是相对锚点的偏移，w/h 保持像素尺寸。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Align {
    pub horizontal: AxisAlign,
    pub vertical: AxisAlign,
}

impl Align {
    pub(crate) const TOP_LEFT: Self = Self {
        horizontal: AxisAlign::Start,
        vertical: AxisAlign::Start,
    };
    pub(crate) const BOTTOM_LEFT: Self = Self {
        horizontal: AxisAlign::Start,
        vertical: AxisAlign::End,
    };
}

/// 控件命中策略：忽略自身、作为可点目标、或拦截其区域内的地图输入。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum InputPolicy {
    Ignore,
    Target,
    Block,
}

/// 通用布局编辑约束；绘制和业务交互不属于此策略。
#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct EditPolicy {
    pub movable: bool,
    pub resizable: bool,
    pub min_w: f32,
    pub min_h: f32,
    pub max_w: Option<f32>,
    pub max_h: Option<f32>,
}

impl EditPolicy {
    pub(crate) const LOCKED: Self = Self {
        movable: false,
        resizable: false,
        min_w: 1.0,
        min_h: 1.0,
        max_w: None,
        max_h: None,
    };
    pub(crate) const MOVE_ONLY: Self = Self {
        movable: true,
        resizable: false,
        ..Self::LOCKED
    };
    pub(crate) const MOVE_RESIZE: Self = Self {
        movable: true,
        resizable: true,
        min_w: 2.0,
        min_h: 2.0,
        max_w: None,
        max_h: None,
    };

    fn clamp_size(self, w: f32, h: f32) -> (f32, f32) {
        let max_w = self.max_w.unwrap_or(f32::INFINITY).max(self.min_w);
        let max_h = self.max_h.unwrap_or(f32::INFINITY).max(self.min_h);
        (w.max(self.min_w).min(max_w), h.max(self.min_h).min(max_h))
    }
}

/// 可编辑节点的局部几何与锚点快照。
#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct LayoutValue {
    pub rect: crate::layout::LRect,
    pub align: Align,
}

/// 一次完整的布局变更，可用于撤销/重做。
#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct EditOperation {
    pub id: &'static str,
    pub before: LayoutValue,
    pub after: LayoutValue,
}

/// 通用拖动/改尺寸事务。预览可逐帧写入，提交时只产生一个操作。
#[derive(Debug, Clone, Copy)]
pub(crate) struct EditSession {
    id: &'static str,
    mode: EditMode,
    from: (f32, f32),
    start: crate::layout::LRect,
    before: LayoutValue,
    policy: EditPolicy,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum EditMode {
    Move,
    Resize,
}

impl EditSession {
    pub(crate) fn begin(
        tree: &WidgetTree,
        viewport: crate::layout::LRect,
        id: &'static str,
        pointer: (f32, f32),
        grip: f32,
    ) -> Option<Self> {
        let node = tree.node(id)?;
        let policy = node.edit;
        if node.visual != VisualSource::Independent || (!policy.movable && !policy.resizable) {
            return None;
        }
        let start = tree.resolved_rect(viewport, id)?;
        let resizing = policy.resizable
            && (pointer.0 >= start.x + start.w - grip || pointer.1 >= start.y + start.h - grip);
        Some(Self {
            id,
            mode: if resizing {
                EditMode::Resize
            } else {
                EditMode::Move
            },
            from: pointer,
            start,
            before: tree.value(id)?,
            policy,
        })
    }

    pub(crate) fn update(
        &self,
        tree: &mut WidgetTree,
        viewport: crate::layout::LRect,
        pointer: (f32, f32),
        grid: f32,
    ) -> Result<crate::layout::LRect, &'static str> {
        let step = grid.max(1.0);
        let snap = |v: f32| (v / step).round() * step;
        let (dx, dy) = (snap(pointer.0 - self.from.0), snap(pointer.1 - self.from.1));
        let rect = match self.mode {
            // 只吸附**位移**、不吸附绝对坐标：默认版式里存在 165.625 这种"拟合坐标"
            //（门框按 1.15 折回设计空间），绝对吸附会把它吸成整数，一拖就丢精度。
            EditMode::Move if self.policy.movable => crate::layout::LRect::new(
                self.start.x + dx,
                self.start.y + dy,
                self.start.w,
                self.start.h,
            ),
            EditMode::Resize if self.policy.resizable => {
                let (w, h) = self.policy.clamp_size(self.start.w + dx, self.start.h + dy);
                let (w, h) = self.policy.clamp_size(snap(w), snap(h));
                crate::layout::LRect::new(self.start.x, self.start.y, w, h)
            }
            _ => self.start,
        };
        tree.set_resolved_rect(viewport, self.id, rect)?;
        Ok(rect)
    }

    pub(crate) fn commit(self, tree: &WidgetTree) -> Option<EditOperation> {
        let after = tree.value(self.id)?;
        (after != self.before).then_some(EditOperation {
            id: self.id,
            before: self.before,
            after,
        })
    }

    pub(crate) fn cancel(self, tree: &mut WidgetTree) -> Result<(), &'static str> {
        tree.set_value(self.id, self.before)
    }
}

/// 布局操作历史；一段拖动只入栈一次。
#[derive(Debug, Default)]
pub(crate) struct EditHistory {
    undo: Vec<EditOperation>,
    redo: Vec<EditOperation>,
}

impl EditHistory {
    pub(crate) fn record(&mut self, operation: EditOperation) {
        const HISTORY_LIMIT: usize = 256;
        if self.undo.len() == HISTORY_LIMIT {
            self.undo.remove(0);
        }
        self.undo.push(operation);
        self.redo.clear();
    }

    pub(crate) fn undo(
        &mut self,
        tree: &mut WidgetTree,
    ) -> Result<Option<&'static str>, &'static str> {
        let Some(operation) = self.undo.pop() else {
            return Ok(None);
        };
        if let Err(error) = tree.set_value(operation.id, operation.before) {
            self.undo.push(operation);
            return Err(error);
        }
        let id = operation.id;
        self.redo.push(operation);
        Ok(Some(id))
    }

    pub(crate) fn redo(
        &mut self,
        tree: &mut WidgetTree,
    ) -> Result<Option<&'static str>, &'static str> {
        let Some(operation) = self.redo.pop() else {
            return Ok(None);
        };
        if let Err(error) = tree.set_value(operation.id, operation.after) {
            self.redo.push(operation);
            return Err(error);
        }
        let id = operation.id;
        self.undo.push(operation);
        Ok(Some(id))
    }
}

/// 节点正常视觉的来源；它决定编辑位置对渲染能否产生完整可见效果。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub(crate) enum VisualSource {
    /// 内容由绘制适配器按该节点的解析矩形独立绘制。
    #[default]
    Independent,
    /// 正常外观已烘焙在父背景中，移动节点不会移动那层像素。
    BackgroundBaked,
    /// 常态烘焙在背景，按下/悬停时另绘制 overlay。
    BackgroundBakedWithOverlay,
}

/// 场景树中的一个布局节点。
///
/// `parent` 为空时相对 viewport 对齐；有父控件时 `rect` 是父控件局部坐标。
/// `z` 越大越靠上；`clip_children` 将子控件绘制/命中裁剪在自身矩形中。
#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct LayoutNode {
    pub id: &'static str,
    pub parent: Option<&'static str>,
    pub rect: crate::layout::LRect,
    pub align: Align,
    pub z: i32,
    pub clip_children: bool,
    pub input: InputPolicy,
    pub edit: EditPolicy,
    pub visual: VisualSource,
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct ResolvedNode {
    pub id: &'static str,
    pub rect: crate::layout::LRect,
    pub clip: Option<crate::layout::LRect>,
    pub z: i32,
    pub input: InputPolicy,
    order: usize,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub(crate) struct WidgetTree {
    nodes: Vec<LayoutNode>,
}

impl WidgetTree {
    pub(crate) fn push(&mut self, node: LayoutNode) {
        self.nodes.push(node);
    }

    pub(crate) fn nodes(&self) -> &[LayoutNode] {
        &self.nodes
    }

    pub(crate) fn nodes_mut(&mut self) -> &mut [LayoutNode] {
        &mut self.nodes
    }

    pub(crate) fn node(&self, id: &str) -> Option<&LayoutNode> {
        self.nodes.iter().find(|node| node.id == id)
    }

    pub(crate) fn value(&self, id: &str) -> Option<LayoutValue> {
        let node = self.node(id)?;
        Some(LayoutValue {
            rect: node.rect,
            align: node.align,
        })
    }

    pub(crate) fn set_value(&mut self, id: &str, value: LayoutValue) -> Result<(), &'static str> {
        let node = self.node_mut(id).ok_or("editable widget does not exist")?;
        node.rect = value.rect;
        node.align = value.align;
        Ok(())
    }

    pub(crate) fn resolved_rect(
        &self,
        viewport: crate::layout::LRect,
        id: &str,
    ) -> Option<crate::layout::LRect> {
        self.resolve(viewport)
            .ok()?
            .into_iter()
            .find(|node| node.id == id)
            .map(|node| node.rect)
    }

    pub(crate) fn set_resolved_rect(
        &mut self,
        viewport: crate::layout::LRect,
        id: &str,
        rect: crate::layout::LRect,
    ) -> Result<(), &'static str> {
        let node = self.node(id).ok_or("editable widget does not exist")?;
        let (parent, align) = (node.parent, node.align);
        let base = if let Some(parent_id) = parent {
            self.resolved_rect(viewport, parent_id)
                .ok_or("editable widget parent does not resolve")?
        } else {
            viewport
        };
        let local = unalign_rect(rect, base, align);
        self.node_mut(id)
            .ok_or("editable widget does not exist")?
            .rect = local;
        Ok(())
    }

    /// 设计器修改 anchor/局部矩形时更新节点。
    #[allow(dead_code)]
    pub(crate) fn node_mut(&mut self, id: &str) -> Option<&mut LayoutNode> {
        self.nodes.iter_mut().find(|node| node.id == id)
    }

    /// 统一解析 parent + align + clip；绘制与命中应消费同一份 `ResolvedNode`。
    pub(crate) fn resolve(
        &self,
        viewport: crate::layout::LRect,
    ) -> Result<Vec<ResolvedNode>, &'static str> {
        for i in 0..self.nodes.len() {
            if self.nodes[..i]
                .iter()
                .any(|node| node.id == self.nodes[i].id)
            {
                return Err("duplicate widget id");
            }
        }
        let mut out: Vec<Option<ResolvedNode>> = vec![None; self.nodes.len()];
        let mut visiting = vec![false; self.nodes.len()];
        for i in 0..self.nodes.len() {
            self.resolve_one(i, viewport, &mut visiting, &mut out)?;
        }
        Ok(out.into_iter().flatten().collect())
    }

    fn resolve_one(
        &self,
        i: usize,
        viewport: crate::layout::LRect,
        visiting: &mut [bool],
        out: &mut [Option<ResolvedNode>],
    ) -> Result<(), &'static str> {
        if out[i].is_some() {
            return Ok(());
        }
        if visiting[i] {
            return Err("widget parent cycle");
        }
        visiting[i] = true;
        let node = self.nodes[i];
        let (base, clip, parent_z) = if let Some(parent_id) = node.parent {
            let p = self
                .nodes
                .iter()
                .position(|candidate| candidate.id == parent_id)
                .ok_or("widget parent does not exist")?;
            self.resolve_one(p, viewport, visiting, out)?;
            let parent = out[p].ok_or("widget parent resolution failed")?;
            let child_clip = if self.nodes[p].clip_children {
                intersect(parent.clip, parent.rect)
            } else {
                parent.clip
            };
            (parent.rect, child_clip, parent.z)
        } else {
            (viewport, None, 0)
        };
        let rect = align_rect(node.rect, base, node.align);
        out[i] = Some(ResolvedNode {
            id: node.id,
            rect,
            clip,
            z: parent_z.saturating_add(node.z),
            input: node.input,
            order: i,
        });
        visiting[i] = false;
        Ok(())
    }

    /// 返回最上层命中项；Block 作为一个正常命中返回，调用方据此拦截地图输入。
    pub(crate) fn hit_test(
        &self,
        viewport: crate::layout::LRect,
        p: (f32, f32),
    ) -> Result<Option<ResolvedNode>, &'static str> {
        let mut nodes = self.resolve(viewport)?;
        nodes.retain(|node| {
            node.input != InputPolicy::Ignore
                && node.rect.hit(p, 0.0)
                && node.clip.is_none_or(|clip| clip.hit(p, 0.0))
        });
        nodes.sort_by_key(|node| (node.z, node.order));
        Ok(nodes.pop())
    }

    /// 编辑命中与业务 hit-test 分离：可见节点包括 LOCKED 节点，避免落到后面的可编辑 parent。
    pub(crate) fn edit_hit_test(
        &self,
        viewport: crate::layout::LRect,
        p: (f32, f32),
        visible: Option<&[&str]>,
    ) -> Result<Option<ResolvedNode>, &'static str> {
        let mut nodes = self.resolve(viewport)?;
        nodes.retain(|resolved| {
            visible.is_none_or(|ids| ids.contains(&resolved.id))
                && resolved.rect.hit(p, 0.0)
                && resolved.clip.is_none_or(|clip| clip.hit(p, 0.0))
        });
        nodes.sort_by_key(|node| (node.z, node.order));
        Ok(nodes.pop())
    }
}

pub(crate) fn align_rect(
    local: crate::layout::LRect,
    parent: crate::layout::LRect,
    align: Align,
) -> crate::layout::LRect {
    use AxisAlign::{Center, End, Start};
    let x = match align.horizontal {
        Start => parent.x + local.x,
        Center => parent.x + (parent.w - local.w) / 2.0 + local.x,
        End => parent.x + parent.w - local.w - local.x,
    };
    let y = match align.vertical {
        Start => parent.y + local.y,
        Center => parent.y + (parent.h - local.h) / 2.0 + local.y,
        End => parent.y + parent.h - local.h - local.y,
    };
    crate::layout::LRect::new(x, y, local.w, local.h)
}

/// 将屏幕矩形反算成对齐偏移（编辑器拖动父窗口后仍保持同一 anchor）。
pub(crate) fn unalign_rect(
    screen: crate::layout::LRect,
    parent: crate::layout::LRect,
    align: Align,
) -> crate::layout::LRect {
    use AxisAlign::{Center, End, Start};
    let x = match align.horizontal {
        Start => screen.x - parent.x,
        Center => screen.x - parent.x - (parent.w - screen.w) / 2.0,
        End => parent.x + parent.w - screen.x - screen.w,
    };
    let y = match align.vertical {
        Start => screen.y - parent.y,
        Center => screen.y - parent.y - (parent.h - screen.h) / 2.0,
        End => parent.y + parent.h - screen.y - screen.h,
    };
    crate::layout::LRect::new(x, y, screen.w, screen.h)
}

fn intersect(
    parent: Option<crate::layout::LRect>,
    child: crate::layout::LRect,
) -> Option<crate::layout::LRect> {
    let Some(parent) = parent else {
        return Some(child);
    };
    let x = parent.x.max(child.x);
    let y = parent.y.max(child.y);
    let right = (parent.x + parent.w).min(child.x + child.w);
    let bottom = (parent.y + parent.h).min(child.y + child.h);
    (right > x && bottom > y).then(|| crate::layout::LRect::new(x, y, right - x, bottom - y))
}

#[cfg(test)]
mod grid_tests {
    use super::*;

    #[test]
    fn 网格的格子排布与命中() {
        let g = Grid::new(
            crate::space::LPt(crate::space::Lpx(10.0), crate::space::Lpx(20.0)),
            6,
            4,
            crate::space::Lpx(35.5),
            crate::space::Lpx(32.5),
        );
        assert_eq!(g.len(), 24);
        // 第 0 格（窗口局部空间）
        assert_eq!(
            g.cell_rect(0),
            (
                crate::space::Lpx(10.0),
                crate::space::Lpx(20.0),
                crate::space::Lpx(35.5),
                crate::space::Lpx(32.5)
            )
        );
        // 第 6 格 = 第二行第一列
        assert_eq!(g.cell_rect(6).0, crate::space::Lpx(10.0));
        assert_eq!(g.cell_rect(6).1, crate::space::Lpx(52.5));
        // 第 7 格 = 第二行第二列
        assert_eq!(g.cell_rect(7).0, crate::space::Lpx(45.5));
        assert_eq!(g.cell_rect(7).1, crate::space::Lpx(52.5));
        // 命中与矩形同源
        for i in 0..g.len() {
            let (x, y, w, h) = g.cell_rect(i);
            assert_eq!(
                g.hit_cell(crate::space::LPt(
                    crate::space::Lpx(x.0 + w.0 / 2.0),
                    crate::space::Lpx(y.0 + h.0 / 2.0),
                )),
                Some(i),
                "第 {i} 格"
            );
        }
        assert_eq!(
            g.hit_cell(crate::space::LPt(
                crate::space::Lpx(0.0),
                crate::space::Lpx(0.0)
            )),
            None,
            "网格外 ⇒ None"
        );
    }

    #[test]
    fn 卖窗圆槽的点按在手上与槽之间搬() {
        let d = Drag::new(SlotRef::Bag(3), 91);
        // 拿着 ⇒ 点槽 = 放进去
        assert_eq!(Drag::toggle_slot(Some(d), None), (None, Some(d)));
        // 空手 ⇒ 点槽 = 拿回手上
        assert_eq!(Drag::toggle_slot(None, Some(d)), (Some(d), None));
        // 两边都空 / 两边都有 ⇒ 原样（不该出现，也不能变出东西来）
        assert_eq!(Drag::toggle_slot(None, None), (None, None));
        assert_eq!(Drag::toggle_slot(Some(d), Some(d)), (Some(d), Some(d)));
    }

    #[test]
    fn 拖拽会话带来源() {
        let d = Drag::new(SlotRef::Bag(7), 91);
        assert_eq!(d.bag_index(), Some(7));
        assert_eq!(Drag::new(SlotRef::Sell, 1).bag_index(), None);
        assert_eq!(Drag::new(SlotRef::Belt(2), 1).bag_index(), None);
    }

    #[test]
    fn 背包重排后按实例号重新定位() {
        // 实例号 91 原来在第 7 格，服务端重排后跑到了第 2 格
        let d = Drag::new(SlotRef::Bag(7), 91);
        let bag: [(i32, usize); 2] = [(90, 5), (91, 2)];
        let got = d
            .rebind(|mk| bag.iter().find(|(m, _)| *m == mk).map(|(_, i)| *i))
            .expect("还在背包里");
        assert_eq!(got.bag_index(), Some(2), "下标要跟着重排走");
        assert_eq!(got.make, 91, "实例号不变");

        // 找不到了（用光/卖掉/被捡走）⇒ None，免得拿着一个幽灵继续操作
        assert_eq!(d.rebind(|_| None), None);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const R: Rect = (
        crate::space::Cpx(10.0),
        crate::space::Cpx(10.0),
        crate::space::Cpx(20.0),
        crate::space::Cpx(20.0),
    );
    const IN: crate::space::CPt =
        crate::space::CPt(crate::space::Cpx(15.0), crate::space::Cpx(15.0));
    const OUT: crate::space::CPt =
        crate::space::CPt(crate::space::Cpx(99.0), crate::space::Cpx(99.0));
    const A: Press = Press::BagClose;

    #[test]
    fn 按下与松开在同一控件才算点击() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.release(IN), Some(A));
        assert_eq!(ui.release(IN), None, "松开过就该清掉");
    }

    #[test]
    fn 拖出控件再松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(OUT);
        assert_eq!(ui.release(OUT), None);
        assert_eq!(ui.held(), None);
    }

    #[test]
    fn 只按下不松开不触发() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.held(), Some(A), "按着没松 ⇒ 高亮还在");
        assert_eq!(ui.release(OUT), None);
    }

    #[test]
    fn 高亮随拖出而灭() {
        let mut ui = Ui::new();
        ui.press(A, R);
        ui.set_mouse(IN);
        assert_eq!(ui.held(), Some(A));
        ui.set_mouse(crate::space::CPt(
            crate::space::Cpx(50.0),
            crate::space::Cpx(50.0),
        ));
        assert_eq!(ui.held(), None, "拖出去 ⇒ 灭");
        ui.set_mouse(IN);
        assert_eq!(ui.held(), Some(A), "拖回来 ⇒ 又亮");
    }

    #[test]
    fn 按钮图的挑选顺序() {
        // 只有高亮图：常态/悬停都不画，按下才画
        let art = ButtonArt::highlight_only(("Prguse", 393));
        assert_eq!(
            art.pick(&Response::default(), true),
            None,
            "常态 ⇒ 不画（在底图里）"
        );
        assert_eq!(
            art.pick(
                &Response {
                    hovered: true,
                    ..Default::default()
                },
                true
            ),
            None,
            "没配 hover ⇒ 悬停也不画"
        );
        assert_eq!(
            art.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 393))
        );

        // 两态：常态常画，按下换图
        let two = ButtonArt::two_state(("Prguse", 130), ("Prguse", 131));
        assert_eq!(two.pick(&Response::default(), true), Some(("Prguse", 130)));
        assert_eq!(
            two.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 131))
        );

        // 配了 hover 才有悬停图
        let mut h = ButtonArt::two_state(("Prguse", 1), ("Prguse", 2));
        h.hover = Some(("Prguse", 3));
        assert_eq!(
            h.pick(
                &Response {
                    hovered: true,
                    ..Default::default()
                },
                true
            ),
            Some(("Prguse", 3))
        );
    }

    #[test]
    fn 禁用时画禁用图且不响应() {
        let mut art = ButtonArt::two_state(("Prguse", 1), ("Prguse", 2));
        art.disabled = Some(("Prguse", 9));
        assert_eq!(art.pick(&Response::default(), true), Some(("Prguse", 1)));
        assert_eq!(
            art.pick(
                &Response {
                    pressed: true,
                    ..Default::default()
                },
                false
            ),
            Some(("Prguse", 9))
        );
    }

    #[test]
    fn 矩形是半开区间() {
        assert!(crate::widget_state::hit(
            R,
            crate::space::CPt(crate::space::Cpx(10.0), crate::space::Cpx(10.0))
        ));
        assert!(!crate::widget_state::hit(
            R,
            crate::space::CPt(crate::space::Cpx(30.0), crate::space::Cpx(30.0))
        ));
    }

    #[test]
    fn 设计空间按钮矩形与画布鼠标同空间判定() {
        let r = design_rect((
            crate::space::Dpx(10.0),
            crate::space::Dpx(10.0),
            crate::space::Dpx(20.0),
            crate::space::Dpx(20.0),
        ));
        let p = crate::ui::ui_pt(crate::space::DPt::from_xy(15.0, 15.0));
        let mut ui = Ui::new();
        ui.set_mouse(p);
        ui.press(A, r);
        assert_eq!(
            ui.held(),
            Some(A),
            "设计矩形须显式换到画布，才能和鼠标同空间命中"
        );
        assert_eq!(ui.release(p), Some(A));
    }
}

#[cfg(test)]
mod layout_tree_tests {
    use super::*;
    use crate::layout::LRect;

    fn node(
        id: &'static str,
        parent: Option<&'static str>,
        rect: LRect,
        align: Align,
        z: i32,
        clip_children: bool,
        input: InputPolicy,
    ) -> LayoutNode {
        LayoutNode {
            id,
            parent,
            rect,
            align,
            z,
            clip_children,
            input,
            edit: EditPolicy::MOVE_RESIZE,
            visual: VisualSource::Independent,
        }
    }

    #[test]
    fn parent_移动时子控件跟随_align按父矩形计算() {
        let mut tree = WidgetTree::default();
        tree.push(node(
            "panel",
            None,
            LRect::new(10.0, 0.0, 100.0, 60.0),
            Align::BOTTOM_LEFT,
            0,
            false,
            InputPolicy::Block,
        ));
        tree.push(node(
            "child",
            Some("panel"),
            LRect::new(4.0, 6.0, 20.0, 10.0),
            Align::TOP_LEFT,
            1,
            false,
            InputPolicy::Target,
        ));
        let viewport = LRect::new(0.0, 0.0, 400.0, 300.0);
        let first = tree.resolve(viewport).unwrap();
        assert_eq!(first[0].rect, LRect::new(10.0, 240.0, 100.0, 60.0));
        assert_eq!(first[1].rect, LRect::new(14.0, 246.0, 20.0, 10.0));

        tree.node_mut("panel").unwrap().rect.x = 30.0;
        let moved = tree.resolve(viewport).unwrap();
        assert_eq!(moved[1].rect, LRect::new(34.0, 246.0, 20.0, 10.0));

        tree.node_mut("child").unwrap().align = Align {
            horizontal: AxisAlign::Center,
            vertical: AxisAlign::Center,
        };
        assert_eq!(
            tree.resolve(viewport).unwrap()[1].rect,
            LRect::new(74.0, 271.0, 20.0, 10.0),
            "子控件可相对父控件中心锚定"
        );
    }

    #[test]
    fn clip和z序共同决定命中优先级() {
        let mut tree = WidgetTree::default();
        tree.push(node(
            "window",
            None,
            LRect::new(0.0, 0.0, 50.0, 50.0),
            Align::TOP_LEFT,
            0,
            true,
            InputPolicy::Block,
        ));
        tree.push(node(
            "inside",
            Some("window"),
            LRect::new(40.0, 10.0, 20.0, 20.0),
            Align::TOP_LEFT,
            1,
            false,
            InputPolicy::Target,
        ));
        tree.push(node(
            "top_overlay",
            None,
            LRect::new(10.0, 10.0, 10.0, 10.0),
            Align::TOP_LEFT,
            5,
            false,
            InputPolicy::Target,
        ));
        let vp = LRect::new(0.0, 0.0, 100.0, 100.0);
        assert_eq!(
            tree.hit_test(vp, (15.0, 15.0)).unwrap().unwrap().id,
            "top_overlay"
        );
        assert_eq!(
            tree.hit_test(vp, (45.0, 15.0)).unwrap().unwrap().id,
            "inside"
        );
        assert_eq!(
            tree.hit_test(vp, (25.0, 15.0)).unwrap().unwrap().id,
            "window",
            "窗口空白也拦截地图输入"
        );
        assert_eq!(
            tree.hit_test(vp, (55.0, 15.0)).unwrap(),
            None,
            "子控件超出父 clip 的部分不可命中"
        );
        assert_eq!(tree.hit_test(vp, (80.0, 80.0)).unwrap(), None);
    }

    #[test]
    fn 编辑命中支持忽略输入节点并排除隐藏覆盖节点() {
        let mut tree = WidgetTree::default();
        let viewport = LRect::new(0.0, 0.0, 100.0, 100.0);
        tree.push(node(
            "panel",
            None,
            LRect::new(0.0, 0.0, 80.0, 80.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Block,
        ));
        tree.push(node(
            "visible_button",
            Some("panel"),
            LRect::new(10.0, 10.0, 30.0, 30.0),
            Align::TOP_LEFT,
            1,
            false,
            InputPolicy::Target,
        ));
        tree.push(node(
            "hidden_modal",
            None,
            LRect::new(5.0, 5.0, 40.0, 40.0),
            Align::TOP_LEFT,
            9,
            false,
            InputPolicy::Ignore,
        ));
        let visible = ["panel", "visible_button"];
        assert_eq!(
            tree.edit_hit_test(viewport, (20.0, 20.0), Some(&visible))
                .unwrap()
                .unwrap()
                .id,
            "visible_button",
            "隐藏且层级更高的弹窗不能劫持可见按钮编辑命中"
        );
        assert_eq!(
            tree.edit_hit_test(viewport, (6.0, 6.0), Some(&visible))
                .unwrap()
                .unwrap()
                .id,
            "panel",
            "Block 根节点同样可以从画布直接编辑"
        );
        let door = ["hidden_modal"];
        assert_eq!(
            tree.edit_hit_test(viewport, (6.0, 6.0), Some(&door))
                .unwrap()
                .unwrap()
                .id,
            "hidden_modal",
            "Ignore 业务 hit-test 节点仍可作为可见编辑目标"
        );
        let baked = tree.node_mut("hidden_modal").unwrap();
        baked.visual = VisualSource::BackgroundBakedWithOverlay;
        baked.edit = EditPolicy::MOVE_ONLY;
        assert_eq!(
            tree.edit_hit_test(viewport, (6.0, 6.0), Some(&door))
                .unwrap()
                .unwrap()
                .id,
            "hidden_modal",
            "锁定节点命中后阻止后层 parent 被误拖"
        );
        assert!(
            EditSession::begin(&tree, viewport, "hidden_modal", (6.0, 6.0), 8.0).is_none(),
            "烘焙视觉不允许只移动编辑框和按下 overlay"
        );
    }

    #[test]
    fn parent环和缺失parent拒绝解析() {
        let mut tree = WidgetTree::default();
        tree.push(node(
            "a",
            Some("b"),
            LRect::new(0.0, 0.0, 1.0, 1.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Target,
        ));
        tree.push(node(
            "b",
            Some("a"),
            LRect::new(0.0, 0.0, 1.0, 1.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Target,
        ));
        assert_eq!(
            tree.resolve(LRect::new(0.0, 0.0, 10.0, 10.0)),
            Err("widget parent cycle")
        );
    }

    #[test]
    fn 通用编辑事务约束并可撤销重做() {
        let viewport = LRect::new(0.0, 0.0, 400.0, 300.0);
        let mut tree = WidgetTree::default();
        let mut panel = node(
            "panel",
            None,
            LRect::new(10.0, 20.0, 100.0, 60.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Block,
        );
        panel.edit = EditPolicy {
            max_w: Some(120.0),
            max_h: Some(80.0),
            ..EditPolicy::MOVE_RESIZE
        };
        tree.push(panel);

        let session = EditSession::begin(&tree, viewport, "panel", (109.0, 69.0), 8.0).unwrap();
        session
            .update(&mut tree, viewport, (300.0, 200.0), 1.0)
            .unwrap();
        let operation = session.commit(&tree).unwrap();
        assert_eq!(
            tree.resolved_rect(viewport, "panel"),
            Some(LRect::new(10.0, 20.0, 120.0, 80.0))
        );

        let mut history = EditHistory::default();
        history.record(operation);
        assert_eq!(history.undo(&mut tree).unwrap(), Some("panel"));
        assert_eq!(
            tree.resolved_rect(viewport, "panel"),
            Some(LRect::new(10.0, 20.0, 100.0, 60.0))
        );
        assert_eq!(history.redo(&mut tree).unwrap(), Some("panel"));
        assert_eq!(
            tree.resolved_rect(viewport, "panel"),
            Some(LRect::new(10.0, 20.0, 120.0, 80.0))
        );
    }

    #[test]
    fn 固定尺寸编辑策略在边缘仍只移动() {
        let viewport = LRect::new(0.0, 0.0, 400.0, 300.0);
        let mut tree = WidgetTree::default();
        let mut window = node(
            "window",
            None,
            LRect::new(10.0, 20.0, 100.0, 60.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Block,
        );
        window.edit = EditPolicy::MOVE_ONLY;
        tree.push(window);
        let session = EditSession::begin(&tree, viewport, "window", (109.0, 69.0), 8.0).unwrap();
        session
            .update(&mut tree, viewport, (115.0, 75.0), 1.0)
            .unwrap();
        assert_eq!(
            tree.resolved_rect(viewport, "window"),
            Some(LRect::new(16.0, 26.0, 100.0, 60.0))
        );
    }

    #[test]
    fn 编辑事务取消恢复原矩形() {
        let viewport = LRect::new(0.0, 0.0, 100.0, 100.0);
        let mut tree = WidgetTree::default();
        tree.push(node(
            "item",
            None,
            LRect::new(5.0, 7.0, 20.0, 10.0),
            Align::TOP_LEFT,
            0,
            false,
            InputPolicy::Target,
        ));
        let session = EditSession::begin(&tree, viewport, "item", (10.0, 10.0), 8.0).unwrap();
        session
            .update(&mut tree, viewport, (30.0, 40.0), 1.0)
            .unwrap();
        session.cancel(&mut tree).unwrap();
        assert_eq!(
            tree.resolved_rect(viewport, "item"),
            Some(LRect::new(5.0, 7.0, 20.0, 10.0))
        );
    }
}

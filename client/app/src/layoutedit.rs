//! **通用界面布局编辑器**（D-134/D-136）：Ctrl+L 后从 widget tree 点选节点，按节点策略
//! 移动/改大小；接入 HUD、世界浮窗、登录/建号、开门动画与选角场景，统一保存到 `assets/ui/layout.json`。
//!
//! 只做三件事，其它一概不管：
//!
//! 1. **选**：点在控件上就选中（重叠时取"光标离中心最近"的那个，与世界里的锁怪同一条）；
//! 2. **调**：按住拖动 = 移动；压在右/下边缘 8px 内拖动 = 改大小；网格吸附 `G` 键切 1/8px；
//! 3. **存**：`S` 写文件（只写与缺省不同的项），`R` 把选中项恢复缺省，`Esc` 退出。
//!
//! 编辑结果写入统一的 `layout::layout_document()` 文档；场景绘制/命中 adapter 读取同一解析几何。

use sdl3::pixels::Color;
use sdl3::render::{FRect, WindowCanvas};

use crate::layout::{layout_document, with_layout_document, EditorScope, LRect, LayoutDocument};
use crate::layoutcfg;
use crate::widget::{EditHistory, EditSession, WidgetTree};

/// 边缘把手的宽度（右/下边缘这么多像素内拖动 = 改大小）。
const GRIP: f32 = 8.0;
/// 放大上限（`+` / `-` 在这之间切）。
const ZOOM_MAX: f32 = 8.0;
/// 设计器层级树固定在屏幕左上（不随画面放大），鼠标点击行直接选控件。
const TREE_X: f32 = 8.0;
const TREE_Y: f32 = 28.0;
const TREE_W: f32 = 218.0;
const TREE_HEADER_H: f32 = 18.0;
const TREE_ROW_H: f32 = 14.0;
/// 画框的颜色（未选中 / 选中）。
const C_FRAME: Color = Color::RGBA(90, 170, 255, 200);
const C_SEL: Color = Color::RGBA(255, 210, 60, 255);

/// 编辑器的运行态（挂在 `AppState` 上）。
#[derive(Debug, Default)]
pub(crate) struct LayoutEdit {
    pub on: bool,
    scope: EditorScope,
    visible: Option<Vec<&'static str>>,
    sel: Option<&'static str>,
    drag: Option<EditSession>,
    history: EditHistory,
    grid: f32,
    /// **放大倍数**（1..8；>1 时整个画面按 [`Self::zoom_center`] 放大 —— 精确对像素用）。
    zoom: f32,
    /// 放大中心（**屏幕坐标**）：缩放那一刻的鼠标位置。
    zoom_center: (f32, f32),
    /// 上一次保存的结果（显示在编辑模式的提示行里）。
    pub last: String,
}

impl LayoutEdit {
    /// 放大倍数（1..8）。
    pub(crate) fn zoom(&self) -> f32 {
        self.zoom.clamp(1.0, ZOOM_MAX)
    }

    /// 放大中心（屏幕坐标）。
    pub(crate) fn zoom_center(&self) -> (f32, f32) {
        self.zoom_center
    }

    /// 当前显示场景决定编辑文档与坐标空间；切场景时取消未提交拖动。
    pub(crate) fn set_scene(&mut self, scene: crate::app_state::Scene) {
        let Some(scope) = EditorScope::from_scene(scene) else {
            if self.on {
                self.toggle();
            }
            return;
        };
        if scope == self.scope {
            return;
        }
        if let Some(session) = self.drag.take() {
            with_layout_document(|layout| {
                let mut tree = layout.editor_tree(self.scope);
                if session.cancel(&mut tree).is_ok() {
                    layout.apply_widget_tree(&tree, self.scope);
                }
            });
        }
        self.scope = scope;
        self.sel = None;
        self.visible = match scope {
            EditorScope::World => None,
            EditorScope::Login => Some(vec![
                "login.panel",
                "login.account",
                "login.password",
                "login.ok",
                "login.new",
                "login.chgpw",
                "login.close",
            ]),
            EditorScope::Select => Some(vec![
                "select.panel",
                "select.slot.left",
                "select.slot.right",
                "select.info.left",
                "select.info.right",
                "select.menu.start",
                "select.menu.create",
                "select.menu.restore",
                "select.menu.delete",
                "select.menu.exit",
            ]),
        };
        self.zoom = 1.0;
        self.history = EditHistory::default();
    }

    pub(crate) fn scope(&self) -> EditorScope {
        self.scope
    }

    /// 当前场景实际绘制的节点；None 表示该文档的所有节点都可见。
    pub(crate) fn set_visible(&mut self, visible: Option<Vec<&'static str>>) {
        if self.visible == visible {
            return;
        }
        if let Some(session) = self.drag.take() {
            with_layout_document(|layout| {
                let mut tree = layout.editor_tree(self.scope);
                if session.cancel(&mut tree).is_ok() {
                    layout.apply_widget_tree(&tree, self.scope);
                }
            });
        }
        if self
            .sel
            .is_some_and(|id| visible.as_ref().is_some_and(|ids| !ids.contains(&id)))
        {
            self.sel = None;
            self.history = EditHistory::default();
        }
        self.visible = visible;
    }

    fn is_visible(&self, id: &str) -> bool {
        self.visible.as_ref().is_none_or(|ids| ids.contains(&id))
    }

    /// 屏幕画布点反算到带当前作用域类型的布局点（包含编辑器 zoom）。
    pub(crate) fn pointer_to_layout(
        &self,
        screen: crate::space::CPt,
    ) -> crate::layout::LayoutPoint {
        let logical = self.to_logic((screen.0 .0, screen.1 .0));
        self.scope
            .layout_point(crate::space::CPt::from_xy(logical.0, logical.1))
    }

    /// 有类型的输入边界；空间标签必须与当前编辑文档匹配。
    pub(crate) fn mouse_move_point(&mut self, point: crate::layout::LayoutPoint) {
        if let Some(xy) = point.coordinates(self.scope) {
            self.mouse_move_xy(xy);
        }
    }

    /// `+` / `-`：以 `at`（屏幕坐标）为中心改倍数 ⇒ 中心不变，看着就是"从鼠标处放大"。
    pub(crate) fn zoom_by(&mut self, step: f32, at: (f32, f32)) {
        let z = (self.zoom() + step).clamp(1.0, ZOOM_MAX);
        self.zoom = z;
        self.zoom_center = at;
        self.last = if z <= 1.0 {
            "倍数 1x（原始大小）".to_string()
        } else {
            format!("倍数 {z:.0}x（中心 {} , {}）", at.0 as i32, at.1 as i32)
        };
    }

    /// 屏幕坐标 → **逻辑**（未放大）坐标：编辑器的命中/拖动都要先换算。
    pub(crate) fn to_logic(&self, p: (f32, f32)) -> (f32, f32) {
        let z = self.zoom();
        if z <= 1.0 {
            return p;
        }
        let (cx, cy) = self.zoom_center;
        (cx + (p.0 - cx) / z, cy + (p.1 - cy) / z)
    }

    /// 网格（0 表示还没选过 ⇒ 默认 1px）。
    pub(crate) fn grid(&self) -> f32 {
        if self.grid <= 0.0 {
            1.0
        } else {
            self.grid
        }
    }

    pub(crate) fn toggle(&mut self) {
        self.on = !self.on;
        if let Some(session) = self.drag.take() {
            with_layout_document(|layout| {
                let mut tree = layout.editor_tree(self.scope);
                if session.cancel(&mut tree).is_ok() {
                    layout.apply_widget_tree(&tree, self.scope);
                }
            });
        }
        if self.on {
            self.last = "Ctrl+L 编辑中：点选拖动 / 边缘=按节点策略改大小 / A锚点(Shift+A横向) / Ctrl+Z撤销 Ctrl+Y重做 / G网格 / S保存 / R复位 / Esc退出".to_string();
        }
    }

    /// `S`：把当前布局写回文件（只写被改过的项）。
    pub(crate) fn save(&mut self) {
        let cur = layout_document();
        let path = layoutcfg::layout_path();
        match layoutcfg::save(&path, &cur) {
            Ok(true) => self.last = format!("已保存 {}", path.display()),
            Ok(false) => self.last = "没改动（与缺省一致）⇒ 不落文件".to_string(),
            Err(e) => self.last = format!("保存失败：{e}"),
        }
        println!("[layout] {}", self.last);
    }

    /// `R`：选中项恢复缺省，作为一次通用布局操作记录。
    pub(crate) fn reset_selected(&mut self) {
        let Some(id) = self.sel else { return };
        let default = LayoutDocument::default_layout().editor_tree(self.scope);
        let Some(after) = default.value(id) else {
            return;
        };
        let operation = with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            let before = tree.value(id)?;
            tree.set_value(id, after).ok()?;
            layout.apply_widget_tree(&tree, self.scope);
            Some(crate::widget::EditOperation { id, before, after })
        });
        if let Some(operation) = operation.filter(|op| op.before != op.after) {
            self.history.record(operation);
            self.last = format!("{id} 已复位");
        }
    }

    fn pick(&self, tree: &WidgetTree, p: (f32, f32)) -> Option<&'static str> {
        tree.edit_hit_test(viewport(self.scope), p, self.visible.as_deref())
            .expect("editable widget tree must be valid")
            .map(|hit| hit.id)
    }

    /// 层级面板与实际 widget tree 同源；添加节点后无需另改编辑器白名单。
    pub(crate) fn mouse_down_screen(
        &mut self,
        screen: crate::space::CPt,
        point: crate::layout::LayoutPoint,
    ) -> bool {
        if !self.on {
            return false;
        }
        let screen_xy = (screen.0 .0, screen.1 .0);
        let tree = layout_document().editor_tree(self.scope);
        let row_y = TREE_Y + TREE_HEADER_H;
        let tree_h = TREE_HEADER_H + TREE_ROW_H * tree.nodes().len() as f32 + 4.0;
        if screen_xy.0 >= TREE_X
            && screen_xy.0 < TREE_X + TREE_W
            && screen_xy.1 >= TREE_Y
            && screen_xy.1 < TREE_Y + tree_h
        {
            if screen_xy.1 >= row_y {
                let row = ((screen_xy.1 - row_y) / TREE_ROW_H).floor() as usize;
                if let Some(node) = tree.nodes().get(row) {
                    self.sel = Some(node.id);
                    self.last = format!("选中 {} ({})", crate::layout::item_name(node.id), node.id);
                }
            }
            self.drag = None;
            return true;
        }
        let Some(point) = point.coordinates(self.scope) else {
            self.drag = None;
            return true;
        };
        self.mouse_down_xy(point)
    }

    /// 节点命中内部只接受已与文档 scope 对齐的坐标。
    fn mouse_down_xy(&mut self, p: (f32, f32)) -> bool {
        if !self.on {
            return false;
        }
        let tree = layout_document().editor_tree(self.scope);
        let Some(id) = self.pick(&tree, p) else {
            self.sel = None;
            self.drag = None;
            return true;
        };
        self.sel = Some(id);
        self.drag = EditSession::begin(&tree, viewport(self.scope), id, p, GRIP);
        true
    }

    #[cfg(test)]
    pub(crate) fn mouse_down(&mut self, p: (f32, f32)) -> bool {
        self.mouse_down_xy(p)
    }

    /// 编辑事务逐帧预览，mouse-up 才压入一次 undo 历史。
    fn mouse_move_xy(&mut self, p: (f32, f32)) {
        let Some(session) = self.drag else { return };
        let grid = self.grid();
        with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            if session
                .update(&mut tree, viewport(self.scope), p, grid)
                .is_ok()
            {
                layout.apply_widget_tree(&tree, self.scope);
            }
        });
    }

    #[cfg(test)]
    pub(crate) fn mouse_move(&mut self, p: (f32, f32)) {
        self.mouse_move_xy(p);
    }

    pub(crate) fn mouse_up(&mut self) {
        let Some(session) = self.drag.take() else {
            return;
        };
        let tree = layout_document().editor_tree(self.scope);
        if let Some(operation) = session.commit(&tree) {
            self.history.record(operation);
            if let Some(rect) = tree.resolved_rect(viewport(self.scope), operation.id) {
                self.last = format!(
                    "{} x={} y={} w={} h={}",
                    operation.id, rect.x, rect.y, rect.w, rect.h
                );
            }
        }
    }

    /// 撤销/重做通过 widget 层的几何快照执行。
    pub(crate) fn undo(&mut self) {
        let id = with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            let id = self.history.undo(&mut tree).ok().flatten()?;
            layout.apply_widget_tree(&tree, self.scope);
            Some(id)
        });
        if let Some(id) = id {
            self.last = format!("已撤销 {id}");
        }
    }

    pub(crate) fn redo(&mut self) {
        let id = with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            let id = self.history.redo(&mut tree).ok().flatten()?;
            layout.apply_widget_tree(&tree, self.scope);
            Some(id)
        });
        if let Some(id) = id {
            self.last = format!("已重做 {id}");
        }
    }

    /// 方向键微调也作为单个通用操作进入历史。
    pub(crate) fn nudge(&mut self, dx: f32, dy: f32) {
        let Some(id) = self.sel else { return };
        let operation = with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            let node = tree.node(id)?;
            if !node.edit.movable || node.visual != crate::widget::VisualSource::Independent {
                return None;
            }
            let before = tree.value(id)?;
            let rect = tree.resolved_rect(viewport(self.scope), id)?;
            tree.set_resolved_rect(
                viewport(self.scope),
                id,
                LRect::new(rect.x + dx, rect.y + dy, rect.w, rect.h),
            )
            .ok()?;
            let after = tree.value(id)?;
            layout.apply_widget_tree(&tree, self.scope);
            Some(crate::widget::EditOperation { id, before, after })
        });
        if let Some(operation) = operation.filter(|op| op.before != op.after) {
            self.history.record(operation);
            if let Some(rect) = layout_document().get(id) {
                self.last = format!("{} x={} y={} w={} h={}", id, rect.x, rect.y, rect.w, rect.h);
            }
        }
    }

    /// `A` 改变当前选中节点的锚点；节点层级和锚点轴均由 widget 文档决定。
    pub(crate) fn cycle_align(&mut self, horizontal: bool) {
        use crate::widget::AxisAlign::{Center, End, Start};
        let Some(id) = self.sel else { return };
        let axes = [Start, Center, End];
        let operation = with_layout_document(|layout| {
            let mut tree = layout.editor_tree(self.scope);
            let node = tree.node(id)?;
            if !node.edit.movable || node.visual != crate::widget::VisualSource::Independent {
                return None;
            }
            let before = tree.value(id)?;
            let mut after = before;
            let axis = if horizontal {
                &mut after.align.horizontal
            } else {
                &mut after.align.vertical
            };
            let index = axes
                .iter()
                .position(|candidate| candidate == axis)
                .unwrap_or(0);
            *axis = axes[(index + 1) % axes.len()];
            tree.set_value(id, after).ok()?;
            layout.apply_widget_tree(&tree, self.scope);
            Some(crate::widget::EditOperation { id, before, after })
        });
        if let Some(operation) = operation.filter(|op| op.before != op.after) {
            self.history.record(operation);
            self.last = format!(
                "{id} 锚点：{:?}/{:?}",
                operation.after.align.horizontal, operation.after.align.vertical
            );
        }
    }

    pub(crate) fn cycle_grid(&mut self) {
        self.grid = if self.grid() >= 8.0 { 1.0 } else { 8.0 };
        self.last = format!("网格 {} px", self.grid());
    }

    /// 选中的控件（叠加层与测试都用；`#[allow]` 之外无调用者是正常的）。
    #[allow(dead_code)]
    pub(crate) fn selected(&self) -> Option<&'static str> {
        self.sel
    }

    /// 画叠加层：所有项的框 + 选中项高亮（带把手）+ 一行数值提示。
    pub(crate) fn draw<'a, T>(
        &self,
        canvas: &mut WindowCanvas,
        names: &mut crate::font::TextCache<'a>,
        tc: &'a sdl3::render::TextureCreator<T>,
    ) -> Result<(), sdl3::Error> {
        if !self.on {
            return Ok(());
        }
        let z = self.zoom();
        let (cx, cy) = self.zoom_center;
        let space_scale = if self.scope == EditorScope::World {
            1.0
        } else {
            crate::ui::UI_SCALE
        };
        // 文档坐标→画布坐标→编辑器放大屏幕坐标。文本标签只换位置、不拉伸。
        let zx = |x: f32| cx + (x - cx) * z;
        let zy = |y: f32| cy + (y - cy) * z;
        let tree = layout_document().editor_tree(self.scope);
        let resolved = tree
            .resolve(viewport(self.scope))
            .expect("editable widget tree must be valid");
        for node in tree.nodes() {
            let id = node.id;
            if !self.is_visible(id) {
                continue;
            }
            let Some(r) = resolved
                .iter()
                .find(|item| item.id == id)
                .map(|item| item.rect)
            else {
                continue;
            };
            let shown = LRect::new(
                r.x * space_scale,
                r.y * space_scale,
                r.w * space_scale,
                r.h * space_scale,
            );
            let sel = self.sel == Some(id);
            let c = if sel { C_SEL } else { C_FRAME };
            frame(canvas, shown, c, z, (cx, cy))?;
            if sel {
                if node.edit.resizable {
                    for (gx, gy) in [
                        (shown.x, shown.y),
                        (shown.x + shown.w - 3.0, shown.y),
                        (shown.x, shown.y + shown.h - 3.0),
                        (shown.x + shown.w - 3.0, shown.y + shown.h - 3.0),
                    ] {
                        canvas.set_draw_color(c);
                        canvas.fill_rect(FRect::new(zx(gx), zy(gy), 3.0, 3.0))?;
                    }
                }
                let s = format!(
                    "{} {}  x={} y={} w={} h={}",
                    crate::layout::item_name(id),
                    id,
                    r.x,
                    r.y,
                    r.w,
                    r.h
                );
                let (lx, ly) = (zx(shown.x).max(2.0), zy(shown.y - 14.0).max(2.0));
                canvas.set_draw_color(Color::RGBA(0, 0, 0, 225));
                canvas.fill_rect(FRect::new(lx - 2.0, ly - 1.0, names.width(&s) + 4.0, 14.0))?;
                names.draw(canvas, tc, &s, lx, ly, (255, 255, 255), None)?;
            }
        }
        // 列表保留全部节点，隐藏节点可从这里选择；画布命中只考虑 self.visible。
        let tree_h = TREE_HEADER_H + TREE_ROW_H * tree.nodes().len() as f32 + 4.0;
        canvas.set_draw_color(Color::RGB(0, 0, 0));
        canvas.fill_rect(FRect::new(TREE_X, TREE_Y, TREE_W, tree_h))?;
        canvas.set_draw_color(C_FRAME);
        canvas.draw_rect(FRect::new(TREE_X, TREE_Y, TREE_W, tree_h))?;
        names.draw(
            canvas,
            tc,
            "控件层级（点击选择）",
            TREE_X + 5.0,
            TREE_Y + 2.0,
            (255, 230, 140),
            None,
        )?;
        for (i, node) in tree.nodes().iter().enumerate() {
            let id = node.id;
            let y = TREE_Y + TREE_HEADER_H + i as f32 * TREE_ROW_H;
            if self.sel == Some(id) {
                canvas.set_draw_color(Color::RGB(64, 56, 30));
                canvas.fill_rect(FRect::new(TREE_X + 1.0, y, TREE_W - 2.0, TREE_ROW_H))?;
            }
            let depth = if node.parent.is_some() {
                "  - "
            } else {
                "[-] "
            };
            let marker = match node.visual {
                crate::widget::VisualSource::Independent => "",
                crate::widget::VisualSource::BackgroundBaked => " [背景烘焙]",
                crate::widget::VisualSource::BackgroundBakedWithOverlay => " [背景烘焙/按下层]",
            };
            let text = format!("{depth}{}{marker}", crate::layout::item_name(id));
            names.draw(
                canvas,
                tc,
                &text,
                TREE_X + 5.0,
                y + 1.0,
                (255, 255, 255),
                None,
            )?;
        }
        // 顶部操作提示。
        label(canvas, names, tc, &self.last, 8.0, 8.0)?;
        Ok(())
    }
}

fn viewport(scope: EditorScope) -> LRect {
    scope.viewport()
}

/// 一行**黑底白字**的标签（编辑器的所有文字都走它 ⇒ 在任何底图上都读得清）。
fn label<'a, T>(
    canvas: &mut WindowCanvas,
    names: &mut crate::font::TextCache<'a>,
    tc: &'a sdl3::render::TextureCreator<T>,
    s: &str,
    x: f32,
    y: f32,
) -> Result<(), sdl3::Error> {
    canvas.set_draw_color(Color::RGBA(0, 0, 0, 225));
    canvas.fill_rect(FRect::new(x - 2.0, y - 1.0, names.width(s) + 4.0, 14.0))?;
    names.draw(canvas, tc, s, x, y, (255, 255, 255), None)
}

/// 画一个 1px 的矩形框（四条边，避免 fill 盖住底下的图）。
///
/// `z` / `center` 是编辑器的放大参数（见 [`LayoutEdit::draw`] 的换算）。
fn frame(
    canvas: &mut WindowCanvas,
    r: LRect,
    c: Color,
    z: f32,
    center: (f32, f32),
) -> Result<(), sdl3::Error> {
    let (cx, cy) = center;
    let x = cx + (r.x - cx) * z;
    let y = cy + (r.y - cy) * z;
    let w = r.w * z;
    let h = r.h * z;
    canvas.set_draw_color(c);
    canvas.fill_rect(FRect::new(x, y, w, 1.0))?;
    canvas.fill_rect(FRect::new(x, y + h - 1.0, w, 1.0))?;
    canvas.fill_rect(FRect::new(x, y, 1.0, h))?;
    canvas.fill_rect(FRect::new(x + w - 1.0, y, 1.0, h))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 持锁期间独占全局布局，离开测试（包括 panic）时恢复默认值。
    struct LayoutTestGuard {
        _guard: std::sync::MutexGuard<'static, ()>,
    }

    impl Drop for LayoutTestGuard {
        fn drop(&mut self) {
            crate::layout::with_layout_document(|layout| {
                *layout = crate::layout::LayoutDocument::default_layout();
            });
        }
    }

    fn begin() -> LayoutTestGuard {
        let guard = crate::layout::LAYOUT_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        crate::layout::with_layout_document(|layout| {
            *layout = crate::layout::LayoutDocument::default_layout();
        });
        LayoutTestGuard { _guard: guard }
    }

    /// 编辑器的命中/拖动是纯几何 ⇒ 用一个假布局单测（不碰 SDL）。
    #[test]
    fn 点在控件上才选中_重叠时取离中心近的() {
        let _g = begin();
        // `orb` 默认在 (40, 608, 92, 90)，中心 (86, 653)
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        // 控件内 ⇒ 选中
        assert!(e.mouse_down((86.0, 653.0)));
        assert_eq!(e.selected(), Some("orb"));
        // 控件外 ⇒ 取消选中（但仍消费点击，不穿透到世界）
        assert!(e.mouse_down((500.0, 100.0)));
        assert_eq!(e.selected(), None);
    }

    #[test]
    fn 拖动按网格吸附且一次撤销还原() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        e.grid = 8.0;
        e.mouse_down((86.0, 653.0)); // 选中 orb
        let before = layout_document().get("orb").unwrap();
        e.mouse_move((86.0 + 5.0, 653.0 + 5.0)); // 位移 5 ⇒ 吸附到 8
        e.mouse_up();
        let after = layout_document().get("orb").unwrap();
        // 吸附的是**位移**（5 → 8），不是绝对落点：默认版式里有 165.625 这类拟合坐标，
        // 按绝对坐标吸附会在第一次拖动时把它抹成整数（见 `EditSession::update`）。
        assert_eq!(after.x, before.x + 8.0);
        assert_eq!(after.y, before.y + 8.0);
        assert_eq!(after.w, before.w, "移动不该改大小");
        e.undo();
        assert_eq!(layout_document().get("orb").unwrap(), before);
        e.redo();
        assert_eq!(layout_document().get("orb").unwrap(), after);
    }

    #[test]
    fn 压在右下边缘是改大小() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        let r = layout_document().get("plate_hp").unwrap();
        // 这个文字布局框声明允许 resize；原生像素球则是 MoveOnly。
        e.mouse_down((r.x + r.w, r.y + r.h));
        let before = layout_document().get("plate_hp").unwrap();
        e.mouse_move((r.x + r.w + 10.0, r.y + r.h + 6.0));
        e.mouse_up();
        let after = layout_document().get("plate_hp").unwrap();
        assert_eq!(after.w, before.w + 10.0);
        assert_eq!(after.h, before.h + 6.0);
        assert_eq!(after.x, before.x, "改大小不该挪位置");
        // 复原，免得污染别的用例
        e.sel = Some("plate_hp");
        e.reset_selected();
    }

    /// 放大镜的坐标换算（用户 2026-10-12："+- 以鼠标位置为中心放大，最高 8 倍"）：
    /// 屏幕 → 逻辑 必须是 `中心 + (屏幕 − 中心)/倍`，且倍数夹在 1..8。
    #[test]
    fn 放大后鼠标要反算回逻辑坐标() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        // 1x ⇒ 原样
        assert_eq!(e.to_logic((300.0, 400.0)), (300.0, 400.0));

        // 以 (500,600) 为中心放大 4 倍
        e.zoom_by(3.0, (500.0, 600.0)); // 1 + 3 = 4
        assert_eq!(e.zoom(), 4.0);
        // 中心不动
        assert_eq!(e.to_logic((500.0, 600.0)), (500.0, 600.0));
        // 中心右边 40 屏幕像素 ⇒ 逻辑上只偏移 10
        assert_eq!(e.to_logic((540.0, 600.0)), (510.0, 600.0));

        // 上限 8、下限 1
        e.zoom_by(100.0, (0.0, 0.0));
        assert_eq!(e.zoom(), 8.0);
        e.zoom_by(-100.0, (0.0, 0.0));
        assert_eq!(e.zoom(), 1.0);

        let canvas_point = crate::space::CPt::from_xy(800.0, 640.0);
        assert_eq!(
            e.pointer_to_layout(canvas_point),
            crate::layout::LayoutPoint::World(canvas_point)
        );
        e.set_scene(crate::app_state::Scene::Login);
        assert_eq!(
            e.pointer_to_layout(canvas_point),
            crate::layout::LayoutPoint::Ui(crate::space::DPt::from_xy(625.0, 500.0))
        );
    }

    /// 闭环：编辑器改 → 存文件 → 重新载入 ⇒ 拿到同一个布局（D-134 的命脉）。
    #[test]
    fn 保存再载入回到同一个布局() {
        let _g = begin();
        let dir = std::env::temp_dir().join("mir2_layout_roundtrip");
        let _ = std::fs::create_dir_all(&dir);
        let path = dir.join("layout.json");
        let _ = std::fs::remove_file(&path);

        // 编辑器改一下底板
        crate::layout::with_layout_document(|l| {
            l.set(
                "plate_hp",
                crate::layout::LRect::new(30.0, 700.0, 50.0, 14.0),
            )
        });
        assert!(crate::layoutcfg::save(&path, &crate::layout::layout_document()).unwrap());

        // 复位成缺省，再从文件载回来
        crate::layout::with_layout_document(|l| {
            *l = crate::layout::LayoutDocument::default_layout()
        });
        assert_ne!(
            crate::layout::layout_document().get("plate_hp"),
            Some(crate::layout::LRect::new(30.0, 700.0, 50.0, 14.0))
        );
        if let Some(l) = crate::layoutcfg::load(&path) {
            crate::layout::with_layout_document(|cur| *cur = l);
        }
        assert_eq!(
            crate::layout::layout_document().get("plate_hp"),
            Some(crate::layout::LRect::new(30.0, 700.0, 50.0, 14.0))
        );
        let _ = std::fs::remove_file(&path);
    }

    /// 层级树行可直接点选，不依赖 Tab 顺序，也不启动画布拖动。
    #[test]
    fn 层级树点击行选择控件() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        let row = layout_document()
            .editor_tree(e.scope)
            .nodes()
            .iter()
            .position(|node| node.id == "plate_hp")
            .unwrap();
        let screen = (
            TREE_X + 10.0,
            TREE_Y + TREE_HEADER_H + row as f32 * TREE_ROW_H + 2.0,
        );
        assert!(e.mouse_down_screen(
            crate::widget::canvas_pt(screen),
            crate::layout::LayoutPoint::World(crate::widget::canvas_pt((900.0, 700.0)))
        ));
        assert_eq!(e.selected(), Some("plate_hp"));
        assert!(e.drag.is_none(), "层级面板点击只选择，不开始拖画布控件");
    }

    /// 当前 HUD 真实使用的 parent 关系：拖底栏空白区域，底图与所有孩子一起移动；
    /// 点在子控件上时由子节点优先命中（WidgetTree 的 z/输入优先级）。
    #[test]
    fn 拖底栏parent时子控件跟随() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        let before = layout_document();
        let panel0 = before.get(crate::layout::HUD_PANEL_ID).unwrap();
        let orb0 = before.get("orb").unwrap();

        // 点在 parent 内、但避开现有子控件的空白处。
        let p = (panel0.x + 10.0, panel0.y + 180.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some(crate::layout::HUD_PANEL_ID));
        e.mouse_move((p.0 + 16.0, p.1 + 8.0));
        e.mouse_up();

        let after = layout_document();
        let panel1 = after.get(crate::layout::HUD_PANEL_ID).unwrap();
        let orb1 = after.get("orb").unwrap();
        assert_eq!(panel1.x, panel0.x + 16.0);
        assert_eq!(panel1.y, panel0.y + 8.0);
        assert_eq!(
            orb1.x - panel1.x,
            orb0.x - panel0.x,
            "球相对 parent 的局部位置不变"
        );
        assert_eq!(
            orb1.y - panel1.y,
            orb0.y - panel0.y,
            "球相对 parent 的局部位置不变"
        );
    }

    #[test]
    fn 复位与微调保留parent锚点下的子项局部位置() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        with_layout_document(|l| {
            l.set_align(
                crate::layout::HUD_PANEL_ID,
                crate::widget::Align {
                    horizontal: crate::widget::AxisAlign::Center,
                    vertical: crate::widget::AxisAlign::End,
                },
            );
            let panel = l.panel_rect();
            l.set(
                crate::layout::HUD_PANEL_ID,
                LRect::new(panel.x + 16.0, panel.y - 8.0, panel.w, panel.h),
            );
        });
        e.sel = Some("exp_bar");
        let before = layout_document().get("exp_bar").unwrap();
        let expected_local = LayoutDocument::default_layout()
            .stored_rect("exp_bar")
            .unwrap();
        e.nudge(4.0, 0.0);
        assert_eq!(layout_document().get("exp_bar").unwrap().x, before.x + 4.0);
        e.reset_selected();
        let after = layout_document();
        assert_eq!(after.get("exp_bar").unwrap(), before);
        assert_eq!(after.stored_rect("exp_bar"), Some(expected_local));
        assert_eq!(
            after.align(crate::layout::HUD_PANEL_ID).unwrap().vertical,
            crate::widget::AxisAlign::End
        );
    }

    #[test]
    fn 通用编辑器移动状态窗和对话窗并保留原生尺寸() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        let before = layout_document().get("window_status").unwrap();
        let p = (before.x + before.w / 2.0, before.y + before.h / 2.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("window_status"));
        e.mouse_move((p.0 + 18.0, p.1 - 9.0));
        e.mouse_up();
        let moved = layout_document().get("window_status").unwrap();
        assert_eq!(
            moved,
            LRect::new(before.x + 18.0, before.y - 9.0, before.w, before.h)
        );
        assert_eq!(
            crate::status::panel_rect(),
            (moved.x, moved.y, moved.w, moved.h)
        );
        e.undo();
        assert_eq!(layout_document().get("window_status"), Some(before));
        e.redo();
        assert_eq!(layout_document().get("window_status"), Some(moved));

        e.sel = Some("window_dialog");
        let dialog = layout_document().get("window_dialog").unwrap();
        e.nudge(-11.0, 6.0);
        assert_eq!(
            crate::dialog::dialog_panel(),
            (dialog.x - 11.0, dialog.y + 6.0, dialog.w, dialog.h)
        );
        e.undo();
        assert_eq!(
            crate::dialog::dialog_panel(),
            (dialog.x, dialog.y, dialog.w, dialog.h)
        );
    }

    #[test]
    fn 登录面板账号框和开门动画使用可编辑文档几何() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        e.set_scene(crate::app_state::Scene::Login);
        let before = layout_document();
        let panel = before.get("login.panel").unwrap();
        let account = before.get("login.account").unwrap();
        let p = (panel.x + 20.0, panel.y + 20.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("login.panel"));
        e.mouse_move((p.0 + 20.0, p.1 + 9.0));
        e.mouse_up();

        let moved = layout_document();
        let login = moved.login_layout(crate::layout::core_login_layout());
        assert_eq!(
            (login.dialog.x, login.dialog.y),
            (panel.x + 20.0, panel.y + 9.0)
        );
        assert_eq!(
            (login.account.x, login.account.y),
            (account.x + 20.0, account.y + 9.0)
        );
        let mut login_state = crate::login::Login::new();
        login_state.on_down(
            crate::space::DPt::from_xy(login.password.x + 2.0, login.password.y + 2.0),
            &login,
            &mut crate::widget::Ui::new(),
        );
        assert_eq!(login_state.focus, 1, "点击编辑后密码框实际位置应仍能命中");
        assert_eq!(
            moved.login_door(),
            before.login_door(),
            "独立门动画不跟登录面板动"
        );
        e.undo();
        assert_eq!(layout_document().get("login.panel"), Some(panel));

        let door = layout_document().get("login.door").unwrap();
        e.set_visible(Some(vec!["login.door"]));
        e.sel = Some("login.door");
        let p = (door.x + 20.0, door.y + 20.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("login.door"));
        e.mouse_move((p.0 + 7.0, p.1 + 5.0));
        e.mouse_up();
        assert_eq!(layout_document().login_door().x, door.x + 7.0);
        e.undo();
        assert_eq!(layout_document().login_door(), door);
    }

    #[test]
    fn 选角背景槽位文字及模态窗口使用可编辑文档几何() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        e.set_scene(crate::app_state::Scene::Select);
        let before = layout_document();
        let base = crate::layout::core_select_layout();
        let select0 = before.select_layout(base);
        let root = before.get("select.panel").unwrap();
        let p = (root.x + 780.0, root.y + 10.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("select.panel"));
        e.mouse_move((p.0 + 12.0, p.1 + 6.0));
        e.mouse_up();

        let moved = layout_document();
        let select1 = moved.select_layout(base);
        assert_eq!(select1.bg, (12.0, 6.0));
        assert_eq!(select1.menu[0].x, select0.menu[0].x + 12.0);
        assert_eq!(select1.slots[0].x, select0.slots[0].x + 12.0);
        assert_eq!(
            select1.slot_texts[0].name.1,
            select0.slot_texts[0].name.1 + 6.0
        );
        e.undo();
        assert_eq!(layout_document().select_layout(base).bg, (0.0, 0.0));

        let slot = layout_document().select_layout(base).slots[0];
        let p = (slot.x + slot.w / 2.0, slot.y + slot.h / 2.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("select.slot.left"));
        e.mouse_move((p.0 + 8.0, p.1 + 4.0));
        e.mouse_up();
        let select2 = layout_document().select_layout(base);
        assert_eq!(select2.slots[0].x, slot.x + 8.0);
        assert_eq!(select2.slots[0].y, slot.y + 4.0);
        assert_eq!(select2.slots[1], select0.slots[1]);
        let mut select_state = crate::select::Select::new(Vec::new());
        let mut widget_ui = crate::widget::Ui::new();
        let hit_at = (
            select2.slots[0].x + select2.slots[0].w / 2.0,
            select2.slots[0].y + select2.slots[0].h / 2.0,
        );
        let hit_design = crate::space::DPt::from_xy(hit_at.0, hit_at.1);
        let canvas_point = crate::ui::ui_pt(hit_design);
        widget_ui.set_mouse(canvas_point);
        select_state.on_down_with_dialog(
            hit_design,
            &select2,
            None,
            layout_document().select_dialog_box(),
            &mut widget_ui,
        );
        assert_eq!(
            widget_ui.release(canvas_point),
            Some(crate::Press::Select(crate::select::Hit::Slot(0)))
        );

        let create = layout_document().select_layout(base).menu[1];
        let menu_tree = layout_document().editor_tree(EditorScope::Select);
        let menu_node = menu_tree.node("select.menu.create").unwrap();
        assert_eq!(
            menu_node.visual,
            crate::widget::VisualSource::BackgroundBakedWithOverlay
        );
        assert_eq!(menu_node.edit, crate::widget::EditPolicy::LOCKED);
        let p = (create.x + create.w / 2.0, create.y + create.h / 2.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("select.menu.create"));
        assert!(e.drag.is_none(), "背景烘焙菜单选中后不可独立拖动");
        assert_eq!(
            layout_document().select_layout(base).menu[1],
            create,
            "背景烘焙文字不能只移动命中/高亮层"
        );
        e.sel = Some("select.menu.create");
        e.nudge(5.0, 2.0);
        e.cycle_align(false);
        assert_eq!(layout_document().select_layout(base).menu[1], create);

        let mut menu_state = crate::select::Select::new(Vec::new());
        let mut menu_ui = crate::widget::Ui::new();
        let menu_point = (
            select2.menu[1].x + select2.menu[1].w / 2.0,
            select2.menu[1].y + select2.menu[1].h / 2.0,
        );
        let menu_design = crate::space::DPt::from_xy(menu_point.0, menu_point.1);
        let menu_canvas_point = crate::ui::ui_pt(menu_design);
        menu_ui.set_mouse(menu_canvas_point);
        menu_state.on_down_with_dialog(
            menu_design,
            &select2,
            None,
            layout_document().select_dialog_box(),
            &mut menu_ui,
        );
        assert_eq!(
            menu_ui.release(menu_canvas_point),
            Some(crate::Press::Select(crate::select::Hit::Menu(1)))
        );
        assert_eq!(
            menu_state.click_select(crate::select::Hit::Menu(1), &select2),
            crate::select::Action::None
        );
        assert!(menu_state.newchar_open(), "创建人物按钮执行原有业务动作");

        let dialog0 = layout_document().select_dialog_box();
        let d = dialog0.frame;
        e.set_visible(Some(vec![
            "select.newchar",
            "select.newchar.name",
            "select.newchar.job.0",
            "select.newchar.job.1",
            "select.newchar.job.2",
            "select.newchar.sex.0",
            "select.newchar.sex.1",
            "select.newchar.ok",
            "select.newchar.close",
        ]));
        e.sel = Some("select.newchar");
        let p = (d.x + d.w / 2.0, d.y + d.h / 2.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some("select.newchar"));
        e.mouse_move((p.0 - 9.0, p.1 + 11.0));
        e.mouse_up();
        let dialog1 = layout_document().select_dialog_box();
        assert_eq!(dialog1.frame.x, d.x - 9.0);
        // 只吸附位移、不吸附绝对坐标：建角窗默认 y 是 91.5（居中），拖动后保留 0.5
        assert_eq!(dialog1.frame.y, d.y + 11.0);
        assert_eq!(dialog1.name.x, dialog0.name.x - 9.0);
        assert_eq!(dialog1.name.y, dialog0.name.y + (dialog1.frame.y - d.y));
    }
}

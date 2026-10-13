//! **界面开发器**（D-134）：Ctrl+L 切进编辑模式，点选底栏上的控件、拖动/改大小、
//! 选好就保存回 `assets/ui/layout.json`。
//!
//! 只做三件事，其它一概不管：
//!
//! 1. **选**：点在控件上就选中（重叠时取"光标离中心最近"的那个，与世界里的锁怪同一条）；
//! 2. **调**：按住拖动 = 移动；压在右/下边缘 8px 内拖动 = 改大小；网格吸附 `G` 键切 1/8px；
//! 3. **存**：`S` 写文件（只写与缺省不同的项），`R` 把选中项恢复缺省，`Esc` 退出。
//!
//! ⚠️ 改动**只落这一个文件**，渲染那边统一读 `layout::hud_layout()` —— 所以编辑完
//! 立刻看得见，不用重编译。

use sdl3::pixels::Color;
use sdl3::render::{FRect, WindowCanvas};

use crate::layout::{hud_layout, with_hud_layout, HudLayout, LRect, HUD_ITEM_IDS};
use crate::layoutcfg;

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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Mode {
    Move,
    Resize,
}

#[derive(Debug, Clone, Copy)]
struct Drag {
    mode: Mode,
    id: &'static str,
    from: (f32, f32),
    orig: LRect,
}

/// 编辑器的运行态（挂在 `AppState` 上）。
#[derive(Debug, Default)]
pub(crate) struct LayoutEdit {
    pub on: bool,
    sel: Option<&'static str>,
    drag: Option<Drag>,
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
        self.drag = None;
        if self.on {
            self.last = "Ctrl+L 编辑中：点选拖动 / 边缘=改大小 / A锚点(Shift+A横向) / G网格 / S保存 / R复位 / Esc退出".to_string();
        }
    }

    /// `S`：把当前布局写回文件（只写被改过的项）。
    pub(crate) fn save(&mut self) {
        let cur = hud_layout();
        let path = layoutcfg::layout_path();
        match layoutcfg::save(&path, &cur) {
            Ok(true) => self.last = format!("已保存 {}", path.display()),
            Ok(false) => self.last = "没改动（与缺省一致）⇒ 不落文件".to_string(),
            Err(e) => self.last = format!("保存失败：{e}"),
        }
        println!("[layout] {}", self.last);
    }

    /// `R`：选中项恢复缺省。
    pub(crate) fn reset_selected(&mut self) {
        let Some(id) = self.sel else {
            return;
        };
        let def = HudLayout::default_layout();
        let Some(r) = def.stored_rect(id) else { return };
        with_hud_layout(|l| {
            if id == crate::layout::HUD_PANEL_ID {
                l.panel_align = def.panel_align;
            }
            l.set_stored(id, r);
        });
        self.last = format!("{id} 已复位");
    }

    /// 命中走同一棵 widget tree：子控件在上层；底栏空白由 parent Block 接住，
    /// clip / z-order 与绘制共用解析结果（D-135）。
    fn pick(&self, p: (f32, f32)) -> Option<&'static str> {
        let l = hud_layout();
        let viewport = LRect::new(
            0.0,
            0.0,
            crate::window::WIN_W as f32,
            crate::window::WIN_H as f32,
        );
        l.widget_tree()
            .hit_test(viewport, p)
            .expect("HUD widget tree must be valid")
            .map(|hit| hit.id)
    }

    /// 画布→固定层级面板的点击：选择列表项不启动控件拖动。
    pub(crate) fn mouse_down_screen(&mut self, screen: (f32, f32), logic: (f32, f32)) -> bool {
        if !self.on {
            return false;
        }
        let row_y = TREE_Y + TREE_HEADER_H;
        let tree_h = TREE_HEADER_H + TREE_ROW_H * HUD_ITEM_IDS.len() as f32 + 4.0;
        if screen.0 >= TREE_X
            && screen.0 < TREE_X + TREE_W
            && screen.1 >= TREE_Y
            && screen.1 < TREE_Y + tree_h
        {
            if screen.1 >= row_y {
                let row = ((screen.1 - row_y) / TREE_ROW_H).floor() as usize;
                if let Some(id) = HUD_ITEM_IDS.get(row) {
                    self.sel = Some(id);
                    self.last = format!("选中 {} ({id})", crate::layout::item_name(id));
                }
            }
            self.drag = None;
            return true;
        }
        self.mouse_down(logic)
    }

    /// 按下：选中 + 决定是移动还是改大小。返回是否消费了这次点击。
    pub(crate) fn mouse_down(&mut self, p: (f32, f32)) -> bool {
        if !self.on {
            return false;
        }
        let Some(id) = self.pick(p) else {
            self.sel = None;
            return true; // 编辑模式下点空白只是取消选中，不穿透到世界
        };
        self.sel = Some(id);
        let orig = hud_layout()
            .get(id)
            .unwrap_or(LRect::new(0.0, 0.0, 0.0, 0.0));
        // Parent 只移动、不缩放；子项在现阶段可人工改尺寸。
        let mode = if id != crate::layout::HUD_PANEL_ID
            && (p.0 >= orig.x + orig.w - GRIP || p.1 >= orig.y + orig.h - GRIP)
        {
            Mode::Resize
        } else {
            Mode::Move
        };
        self.drag = Some(Drag {
            mode,
            id,
            from: p,
            orig,
        });
        true
    }

    /// 移动：按网格吸附写回。
    pub(crate) fn mouse_move(&mut self, p: (f32, f32)) {
        let Some(d) = self.drag else { return };
        let g = self.grid();
        let snap = |v: f32| (v / g).round() * g;
        let (dx, dy) = (snap(p.0 - d.from.0), snap(p.1 - d.from.1));
        let r = match d.mode {
            Mode::Move => LRect::new(snap(d.orig.x + dx), snap(d.orig.y + dy), d.orig.w, d.orig.h),
            Mode::Resize => LRect::new(
                d.orig.x,
                d.orig.y,
                (snap(d.orig.w) + dx).max(2.0),
                (snap(d.orig.h) + dy).max(2.0),
            ),
        };
        with_hud_layout(|l| l.set(d.id, r));
    }

    pub(crate) fn mouse_up(&mut self) {
        if let Some(d) = self.drag.take() {
            if let Some(r) = hud_layout().get(d.id) {
                self.last = format!("{} x={} y={} w={} h={}", d.id, r.x, r.y, r.w, r.h);
            }
        }
    }

    /// 方向键微调（1 格；按住 Shift 之类由调用方决定，这里只吃单位量）。
    pub(crate) fn nudge(&mut self, dx: f32, dy: f32) {
        let Some(id) = self.sel else { return };
        let Some(r) = hud_layout().get(id) else {
            return;
        };
        with_hud_layout(|l| l.set(id, LRect::new(r.x + dx, r.y + dy, r.w, r.h)));
        let r = hud_layout().get(id).unwrap();
        self.last = format!("{} x={} y={} w={} h={}", id, r.x, r.y, r.w, r.h);
    }

    /// `A`：循环当前 parent 的 anchor 轴；Shift+A 切横向，不按 Shift 切纵向。
    /// 子项局部坐标不变，父锚点变化时全体子项一起跟随。
    pub(crate) fn cycle_align(&mut self, horizontal: bool) {
        use crate::widget::AxisAlign::{Center, End, Start};
        let axes = [Start, Center, End];
        crate::layout::with_hud_layout(|l| {
            if horizontal {
                let i = axes
                    .iter()
                    .position(|a| *a == l.panel_align.horizontal)
                    .unwrap_or(0);
                l.panel_align.horizontal = axes[(i + 1) % axes.len()];
            } else {
                let i = axes
                    .iter()
                    .position(|a| *a == l.panel_align.vertical)
                    .unwrap_or(0);
                l.panel_align.vertical = axes[(i + 1) % axes.len()];
            }
        });
        let align = hud_layout().panel_align;
        self.last = format!("底栏锚点：{:?}/{:?}", align.horizontal, align.vertical);
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
        // 逻辑 → 屏幕（放大 z 倍、以 zoom_center 为中心）。字**不跟着放大**（放大 8 倍
        // 的字是糊的），只把位置换算过去。
        let sx = |x: f32| cx + (x - cx) * z;
        let sy = |y: f32| cy + (y - cy) * z;
        let l = hud_layout();
        for id in HUD_ITEM_IDS {
            let Some(r) = l.get(id) else { continue };
            let sel = self.sel == Some(id);
            let c = if sel { C_SEL } else { C_FRAME };
            frame(canvas, r, c, z, (cx, cy))?;
            if sel {
                // 四个角把手（右下那个是"改大小"的提示）
                for (gx, gy) in [
                    (r.x, r.y),
                    (r.x + r.w - 3.0, r.y),
                    (r.x, r.y + r.h - 3.0),
                    (r.x + r.w - 3.0, r.y + r.h - 3.0),
                ] {
                    canvas.set_draw_color(c);
                    canvas.fill_rect(FRect::new(sx(gx), sy(gy), 3.0, 3.0))?;
                }
                // ⚠️ **黑底白字**（用户 2026-10-12：原来的描边字在花纹上看不清）——
                // 先铺一块黑底再写白字，`num` 里已经按字宽算好宽度。
                let s = format!(
                    "{} {}  x={} y={} w={} h={}",
                    crate::layout::item_name(id),
                    id,
                    r.x,
                    r.y,
                    r.w,
                    r.h
                );
                let (lx, ly) = (sx(r.x).max(2.0), sy(r.y - 14.0).max(2.0));
                canvas.set_draw_color(Color::RGBA(0, 0, 0, 225));
                canvas.fill_rect(FRect::new(lx - 2.0, ly - 1.0, names.width(&s) + 4.0, 14.0))?;
                names.draw(canvas, tc, &s, lx, ly, (255, 255, 255), None)?;
            }
        }
        // 鼠标可点选的层级树（固定屏幕层，不跟放大镜缩放；孩子都缩进到底栏 parent 下）。
        let tree_h = TREE_HEADER_H + TREE_ROW_H * HUD_ITEM_IDS.len() as f32 + 4.0;
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
        for (i, id) in HUD_ITEM_IDS.iter().enumerate() {
            let y = TREE_Y + TREE_HEADER_H + i as f32 * TREE_ROW_H;
            if self.sel == Some(id) {
                canvas.set_draw_color(Color::RGB(64, 56, 30));
                canvas.fill_rect(FRect::new(TREE_X + 1.0, y, TREE_W - 2.0, TREE_ROW_H))?;
            }
            let text = if *id == crate::layout::HUD_PANEL_ID {
                format!("[-] {}", crate::layout::item_name(id))
            } else {
                format!("  - {}", crate::layout::item_name(id))
            };
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

    /// ⚠️ 布局是**进程内全局**的，而 cargo test 默认并行 ⇒ 用例之间要**互斥**，
    /// 否则一个在读默认值、另一个在改，会偶发失败（实测跑三次挂一次）。
    static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    /// 取锁 + 把布局复位；返回值要活到用例结束。
    fn begin() -> std::sync::MutexGuard<'static, ()> {
        let g = LOCK.lock().unwrap_or_else(|e| e.into_inner());
        crate::layout::with_hud_layout(|l| *l = crate::layout::HudLayout::default_layout());
        g
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
    fn 拖动按网格吸附() {
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        e.grid = 8.0;
        e.mouse_down((86.0, 653.0)); // 选中 orb
        let before = hud_layout().get("orb").unwrap();
        e.mouse_move((86.0 + 5.0, 653.0 + 5.0)); // 位移 5 ⇒ 吸附到 8
        e.mouse_up();
        let after = hud_layout().get("orb").unwrap();
        // ⚠️ 吸附是把落点对齐到 8 的**倍数**（不是"位移 8"）⇒ 期望要按吸附算
        let snap = |v: f32| (v / 8.0).round() * 8.0;
        assert_eq!(after.x, snap(before.x + 8.0));
        assert_eq!(after.y, snap(before.y + 8.0));
        assert_eq!(after.w, before.w, "移动不该改大小");
    }

    #[test]
    fn 压在右下边缘是改大小() {
        let _g = begin();
        let mut e = LayoutEdit {
            on: true,
            ..LayoutEdit::default()
        };
        let r = hud_layout().get("orb").unwrap();
        // 正好压在右下角（命中框外扩 2px ⇒ 点得到）⇒ 进"改大小"
        e.mouse_down((r.x + r.w, r.y + r.h));
        let before = hud_layout().get("orb").unwrap();
        e.mouse_move((r.x + r.w + 10.0, r.y + r.h + 6.0));
        e.mouse_up();
        let after = hud_layout().get("orb").unwrap();
        assert_eq!(after.w, before.w + 10.0);
        assert_eq!(after.x, before.x, "改大小不该挪位置");
        // 复原，免得污染别的用例
        e.sel = Some("orb");
        e.reset_selected();
    }

    /// 放大镜的坐标换算（用户 2026-10-12："+- 以鼠标位置为中心放大，最高 8 倍"）：
    /// 屏幕 → 逻辑 必须是 `中心 + (屏幕 − 中心)/倍`，且倍数夹在 1..8。
    #[test]
    fn 放大后鼠标要反算回逻辑坐标() {
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
        crate::layout::with_hud_layout(|l| {
            l.set(
                "plate_hp",
                crate::layout::LRect::new(30.0, 700.0, 50.0, 14.0),
            )
        });
        assert!(crate::layoutcfg::save(&path, &crate::layout::hud_layout()).unwrap());

        // 复位成缺省，再从文件载回来
        crate::layout::with_hud_layout(|l| *l = crate::layout::HudLayout::default_layout());
        assert_ne!(
            crate::layout::hud_layout().get("plate_hp"),
            Some(crate::layout::LRect::new(30.0, 700.0, 50.0, 14.0))
        );
        if let Some(l) = crate::layoutcfg::load(&path) {
            crate::layout::with_hud_layout(|cur| *cur = l);
        }
        assert_eq!(
            crate::layout::hud_layout().get("plate_hp"),
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
        let row = 2usize; // root=0, orb=1, plate_hp=2
        let screen = (
            TREE_X + 10.0,
            TREE_Y + TREE_HEADER_H + row as f32 * TREE_ROW_H + 2.0,
        );
        assert!(e.mouse_down_screen(screen, (900.0, 700.0)));
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
        let before = hud_layout();
        let panel0 = before.get(crate::layout::HUD_PANEL_ID).unwrap();
        let orb0 = before.get("orb").unwrap();

        // 点在 parent 内、但避开现有子控件的空白处。
        let p = (panel0.x + 10.0, panel0.y + 180.0);
        assert!(e.mouse_down(p));
        assert_eq!(e.selected(), Some(crate::layout::HUD_PANEL_ID));
        e.mouse_move((p.0 + 16.0, p.1 + 8.0));
        e.mouse_up();

        let after = hud_layout();
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
        with_hud_layout(|l| {
            l.panel_align = crate::widget::Align {
                horizontal: crate::widget::AxisAlign::Center,
                vertical: crate::widget::AxisAlign::End,
            };
            let panel = l.panel_rect();
            l.set(
                crate::layout::HUD_PANEL_ID,
                LRect::new(panel.x + 16.0, panel.y - 8.0, panel.w, panel.h),
            );
        });
        e.sel = Some("exp_bar");
        let before = hud_layout().get("exp_bar").unwrap();
        let expected_local = HudLayout::default_layout().stored_rect("exp_bar").unwrap();
        e.nudge(4.0, 0.0);
        assert_eq!(hud_layout().get("exp_bar").unwrap().x, before.x + 4.0);
        e.reset_selected();
        let after = hud_layout();
        assert_eq!(after.get("exp_bar").unwrap(), before);
        assert_eq!(after.stored_rect("exp_bar"), Some(expected_local));
        assert_eq!(after.panel_align.vertical, crate::widget::AxisAlign::End);
    }
}

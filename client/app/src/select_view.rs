//! 选角场景 SDL 视图：纯状态由父模块 `select` 拥有；本模块只负责素材、字体与绘制。

use std::path::Path;

use sdl3::render::{FRect, TextureCreator, WindowCanvas};

use crate::font::{Rgb, TextCache};
use crate::ui::{ui_inv_px, ui_px, UiCache};

use super::su;
use super::{wrap, Hit, NewChar, Select};

const C_TEXT: Rgb = (255, 255, 255);
#[allow(dead_code)]
const C_DIM: Rgb = (176, 172, 164);
const C_SHADOW: Rgb = (0, 0, 0);
const C_SEL_HL: Rgb = (255, 255, 255);

/// 弹窗的几何（尺寸来自素材；`Prguse[360]` 缺了就 `None`）。
///
/// 抽成函数是为了让**画**（[`draw_msgbox`]）与**点**（[`Select::on_up`]）用同一个矩形 ——
/// 少一处算错就多一个"看得见点不中"。
pub(crate) fn msgbox_geom(ui: &mut UiCache, dir: &Path, win: (u32, u32)) -> Option<su::MsgBox> {
    let frame = ui.size(dir, "Prguse", 360)?;
    let ok = ui.size(dir, "Prguse", 363).unwrap_or((0, 0));
    Some(crate::layout::layout_document().select_msgbox(su::MsgBox::build(win, frame, ok)))
}

/// 画一颗 **ui1 原生按钮**（1024 原生图，图里自带石台/底纹/文字）。
///
/// `r` 是设计空间矩形（`core::select_ui` 已按实测原生坐标 ×25/32 折算）；
/// `draw_ui_src` 内部 ×1.28 折回画布后，目标矩形正好等于这张图被切出来的那块原生区域
/// ⇒ **1:1 无缩放**，与背景上烘焙的同一块逐像素重合（真素材回归：
/// `core::select_ui::tests::真素材_按钮贴合新版背景`）。
fn draw_native_btn<'a, T>(
    ui: &mut UiCache<'a>,
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    dir: &Path,
    btn: (&'static str, u32),
    r: su::Rect,
) {
    let Some((w, h)) = ui.size(dir, btn.0, btn.1) else {
        return; // 清单已校验；真缺图时宁可不画，也不拉伸别的图顶上
    };
    let _ = ui.draw_ui_src(
        canvas,
        tc,
        dir,
        btn.0,
        btn.1,
        (0.0, 0.0, w as f32, h as f32),
        (r.x, r.y, r.w, r.h),
    );
}

/// 视图专属的惰性素材几何缓存；不属于角色选择状态。
#[derive(Default)]
pub(crate) struct ViewState {
    place: su::SlotPlace,
    fx_place: su::SlotPlace,
}

impl Select {
    /// 画一帧（只读场景状态；惰性几何缓存单独归 `ViewState`）。
    #[allow(clippy::too_many_arguments)]
    pub fn draw<'a, T>(
        &self,
        view_state: &mut ViewState,
        canvas: &mut WindowCanvas,
        tc: &'a TextureCreator<T>,
        ui: &mut UiCache<'a>,
        text: &mut TextCache<'a>,
        asset_dir: &Option<std::path::PathBuf>,
        win: (u32, u32),
        mouse: (f32, f32),
        // 控件引擎：按下态问它（不再自存 `down`）
        widget_ui: &crate::widget::Ui,
    ) -> Result<(), sdl3::Error> {
        let Some(dir) = asset_dir.as_deref() else {
            canvas.set_draw_color(sdl3::pixels::Color::RGB(80, 0, 0));
            canvas.clear();
            return text.draw_ui(
                canvas,
                tc,
                "选角素材不在：设置 MIR2_ASSET_DIR",
                16.0,
                16.0,
                (255, 220, 220),
                None,
            );
        };
        let doc = crate::layout::layout_document();
        let Some(base_layout) = su::Layout::build(win, |lib, idx| ui.size(dir, lib, idx)) else {
            canvas.set_draw_color(sdl3::pixels::Color::RGB(80, 0, 0));
            canvas.clear();
            return text.draw_ui(
                canvas,
                tc,
                "选角必需素材缺失：需要 ui1[2851] 与 ui1[2852..2865]（清单已校验）",
                16.0,
                16.0,
                (255, 220, 220),
                None,
            );
        };

        let l = doc.select_layout(base_layout);
        canvas.set_draw_color(sdl3::pixels::Color::RGB(0, 0, 0));
        canvas.clear();
        // 选角背景以原生像素绘制；编辑器移动的是其 800×600 设计空间根节点。
        let (bw, bh) = ui
            .size(dir, su::Art::BG_1024.0, su::Art::BG_1024.1)
            .expect("素材清单要求 ui1[2851]");
        assert_eq!((bw, bh), (crate::window::WIN_W, crate::window::WIN_H));
        let bg_x = ui_px(crate::space::Dpx(l.bg.0)).0;
        let bg_y = ui_px(crate::space::Dpx(l.bg.1)).0;
        let src = FRect::new(0.0, 0.0, bw as f32, bh as f32);
        let dst = FRect::new(bg_x, bg_y, bw as f32, bh as f32);
        assert!(
            ui.draw_src(
                canvas,
                tc,
                dir,
                su::Art::BG_1024.0,
                su::Art::BG_1024.1,
                src,
                dst,
                255
            )
            .is_some(),
            "必需素材 ui1[2851] 选角背景缺失"
        );

        // 七颗界面按钮（2026-10-13 起用 ui1 原生图）：常态整图 1:1 贴回背景上的实测位置。
        // 图里自带石台/底纹/文字（就是从这套 1024 美术里切出来的），×1.28 折回画布后
        // 正好逐像素覆盖背景上烘焙的同一块 —— 不缩放、不重采样，也不再需要搬石台的补丁。
        // 画在**底图之上、小人之下**（按钮与凹槽不重叠，顺序只影响编辑器层级观感）。
        for (i, r) in l.sel.iter().enumerate() {
            draw_native_btn(ui, canvas, tc, dir, su::Art::SEL_UP[i], *r);
        }
        for (i, r) in l.menu.iter().enumerate() {
            draw_native_btn(ui, canvas, tc, dir, su::Art::MENU_UP[i], *r);
        }

        for slot in 0..su::Art::SLOTS {
            let Some(c) = self.char_at_slot(slot).cloned() else {
                continue; // 空槽：什么都不画（删掉的那个位置就留空）
            };
            let f = self.anims[slot].frame(slot == self.picked);
            let anchor = su::slot_anchor_in(l.slots[slot]);
            // ⚠️ 落点的**基准永远是"站立第 0 帧"**，不是当前这一帧：
            // 站立 16 帧的上半身在摆（包围盒中点游走，战士女最多 13.5px）⇒ 逐帧对齐会把
            // 站定的脚推着挪；而槽一进来就是石化的，拿当前帧当基准还会"选中那一刻跳一下"。
            // 石化帧 = 这个基准 + 原版的手调量（`su::frozen_offset`），见 `su::SlotPlace`。
            if let Some(stand0) = ui.bbox(dir, su::Art::CHR, su::stand_index(c.job(), c.sex, 0)) {
                let (sx, sy) =
                    view_state
                        .place
                        .of(slot, c.job(), c.sex, f.at_frozen, stand0, anchor);
                ui.draw_tint(
                    canvas,
                    tc,
                    dir,
                    su::Art::CHR,
                    f.index,
                    sx,
                    sy,
                    (255, 255, 255),
                );
            }
            // 解冻时叠一层选中光效（原版 `DrawBlend`，`IntroScn.pas:1442`）。
            //
            // ⚠️ 位置用**同一条对齐规则**：把光效图不透明部分的底边中点也摆到
            // 同一个锚点上（= 居中压在人物身上、底边落在脚下）；同样**一帧定死**
            //（光效自己那 14 帧的包围盒也在长，逐帧对齐会让整团光左右抖）。
            // 原来这里是 `(anchor.0 - 30, anchor.1 - 60)` 这种**猜出来的偏移**，
            // 结果光效偏右、也不在脚下（用户实测报的）。
            if let Some(e) = f.effect {
                if let Some(eb) = ui.bbox(dir, su::Art::CHR, e) {
                    let (ex, ey) = view_state.fx_place.fx(slot, eb, anchor);
                    ui.draw_tint(canvas, tc, dir, su::Art::CHR, e, ex, ey, C_SEL_HL);
                }
            }
            // 名字 / 等级 / 职业（白字黑边，写在面板的字段框里）
            let t = l.slot_texts[slot];
            text.draw_ui(
                canvas,
                tc,
                &c.name,
                t.name.0,
                t.name.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw_ui(
                canvas,
                tc,
                &c.level.to_string(),
                t.level.0,
                t.level.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
            text.draw_ui(
                canvas,
                tc,
                su::class_name(c.class),
                t.class.0,
                t.class.1,
                C_TEXT,
                Some(C_SHADOW),
            )?;
        }

        // 按下态 / 键盘高亮：**只在按住或键盘选中时**把那张图换成按下态（常态图号 +1）
        //（原版 `DscSelect1DirectPaint` 只认 `Downed`；键盘是我们的扩展）
        let down: Option<Hit> = match widget_ui.held() {
            Some(crate::Press::Select(h)) => Some(h),
            _ => None,
        };
        match down {
            // 弹窗那颗 [Ok] 没有按下态图（原版那颗也只是同一张：登录界面那边按下是 +1 位移，
            // 选角这边按下的反馈就是"弹窗被点掉了"，不必再画一层）
            Some(Hit::MsgOk) => {}
            // 点角色本体：不额外叠图（选中的反馈就是"这个小人不石化/开始摆手"）
            Some(Hit::Slot(_)) => {}
            Some(Hit::Sel(i)) => {
                let (name, idx) = su::Art::SEL_UP[i];
                draw_native_btn(ui, canvas, tc, dir, (name, idx + 1), l.sel[i]);
            }
            Some(Hit::Menu(i)) => {
                let (name, idx) = su::Art::MENU_UP[i];
                draw_native_btn(ui, canvas, tc, dir, (name, idx + 1), l.menu[i]);
            }
            None => {
                // 键盘光标那一项也亮起按下态（否则键盘用户看不出选中的是哪一项）
                let i = self.menu_cursor;
                let (name, idx) = su::Art::MENU_UP[i];
                draw_native_btn(ui, canvas, tc, dir, (name, idx + 1), l.menu[i]);
            }
        }

        // 建角对话框（模态；原版 `DCreateChr`）—— 画在弹窗下面一层
        if let Some(d) = self.newchar.as_ref() {
            let dialog = doc.select_dialog_box();
            let hover = dialog.hit(mouse);
            draw_newchar(canvas, tc, ui, text, dir, dialog, d, hover)?;
        }

        // 弹窗
        if let Some(m) = self.msg.clone() {
            draw_msgbox(canvas, tc, ui, text, dir, win, &m)?;
        }
        Ok(())
    }
}

/// 画建角对话框（原版 `DCreateChr`）。
///
/// ⚠️ 2026-10-11 起**用官方素材**：窗底图 `Prguse[73]`（300×417 居中），职业/性别的
/// 高亮图 74-78 只在**选中或悬停**时叠上去（常态就在底图里），[确定] = 62、
/// [关闭] = 64。坐标全部来自 [`su::DialogBox`]（抄 `FState.pas:977-1001`）。
#[allow(clippy::too_many_arguments)]
fn draw_newchar<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    text: &mut TextCache<'a>,
    dir: &Path,
    b: su::DialogBox,
    d: &NewChar,
    hover: Option<su::DialogHit>,
) -> Result<(), sdl3::Error> {
    // ① 窗底图（`Prguse[73]`，静态必需素材；不以空窗或自绘板兜底）。
    //
    // ⚠️ `b.frame` 是**设计空间**（800×600）矩形 ⇒ 必须走 `draw_ui_src`（内部 ×1.28），
    // 不能走 `draw_src`（画布像素）。2026-10-17 之前这里用的是 `draw_src`：
    // 窗底按 1:1 画在画布左上角，而职业/性别/关闭按钮都按设计空间 ×1.28 ⇒
    // 窗口"不居中、关闭按钮飞出去"（用户报的那两条）。
    let (iw, ih) = ui
        .size(dir, su::Art::NEW_CHR_BG.0, su::Art::NEW_CHR_BG.1)
        .expect("素材清单要求 Prguse[73] 建角窗背景");
    assert_eq!(
        (iw as f32, ih as f32),
        (b.frame.w, b.frame.h),
        "建角窗素材尺寸与布局不一致"
    );
    let _ = ui.draw_ui_src(
        canvas,
        tc,
        dir,
        su::Art::NEW_CHR_BG.0,
        su::Art::NEW_CHR_BG.1,
        (0.0, 0.0, iw as f32, ih as f32),
        (b.frame.x, b.frame.y, b.frame.w, b.frame.h),
    );
    // ② 职业 / 性别的高亮（选中**或**悬停）
    for (i, r) in b.job.iter().enumerate() {
        if d.job == i || hover == Some(su::DialogHit::Job(i)) {
            let _ = ui.draw(
                canvas,
                tc,
                dir,
                su::Art::NEW_CHR_JOB.0,
                su::Art::NEW_CHR_JOB.1 + i as u32,
                r.x,
                r.y,
            );
        }
    }
    for (i, r) in b.sex.iter().enumerate() {
        if d.sex == i || hover == Some(su::DialogHit::Sex(i)) {
            let _ = ui.draw(
                canvas,
                tc,
                dir,
                su::Art::NEW_CHR_SEX.0,
                su::Art::NEW_CHR_SEX.1 + i as u32,
                r.x,
                r.y,
            );
        }
    }
    // ③ [确定] / [关闭]（这两颗不在底图里 ⇒ 常态就画）
    let _ = ui.draw(
        canvas,
        tc,
        dir,
        su::Art::NEW_CHR_OK.0,
        su::Art::NEW_CHR_OK.1,
        b.ok.x,
        b.ok.y,
    );
    let _ = ui.draw(
        canvas,
        tc,
        dir,
        su::Art::NEW_CHR_CLOSE.0,
        su::Art::NEW_CHR_CLOSE.1,
        b.close.x,
        b.close.y,
    );

    // ④ 姓名（唯一的输入框；光标用方块代替闪动）
    text.draw_ui(
        canvas,
        tc,
        &format!("{}\u{2588}", d.name),
        b.name.x + 4.0,
        b.name.y + 3.0,
        C_TEXT,
        None,
    )?;
    Ok(())
}

fn draw_msgbox<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    ui: &mut UiCache<'a>,
    text: &mut TextCache<'a>,
    dir: &Path,
    win: (u32, u32),
    msg: &str,
) -> Result<(), sdl3::Error> {
    let b = match msgbox_geom(ui, dir, win) {
        Some(b) => b,
        None => return Ok(()), // 量不到素材：什么都不画（与"素材缺失"同一条纪律）
    };
    ui.draw(canvas, tc, dir, "Prguse", 360, b.frame.x, b.frame.y);
    if b.ok.w > 0.0 {
        ui.draw(canvas, tc, dir, "Prguse", 363, b.ok.x, b.ok.y);
    }
    // ⚠️ 框宽是**设计空间**、字宽是**画布像素**（字号按 1.28 放大过）⇒ 先 `ui_px` 换算，
    // 否则算出的列数偏大 1.28 倍，行会顶出框外。
    let cols =
        ((ui_px(crate::space::Dpx(b.frame.w - 24.0)).0) / text.width("中").max(1.0)) as usize;
    let mut y = b.frame.y + 20.0;
    for line in wrap(msg, cols.max(8)) {
        text.draw_ui(
            canvas,
            tc,
            &line,
            b.frame.x + 12.0,
            y,
            (255, 255, 255),
            Some((0, 0, 0)),
        )?;
        // ⚠️ 行高是**画布像素**（17.9px 字号算出来的），而 `y` 在设计空间里 ⇒ 除回去
        y += ui_inv_px(crate::space::Cpx(text.line_height() + 2.0)).0;
    }
    Ok(())
}

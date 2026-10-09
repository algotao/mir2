//! 世界屏的总装：切地图 + 把地图/实体/飘字/HUD 画到一帧上。
//!
//! ⚠️ 从 `main.rs` 拆出来的（2026-10-09，纯搬移、没改逻辑）。

use std::collections::HashMap;
use std::path::PathBuf;
use std::time::Instant;

use mir2_core::m2pk::Archive;
use mir2_core::map::{Map, TileDraw, LAYERS_ALL, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::rect::Rect;
use sdl3::render::{TextureCreator, WindowCanvas};

use crate::actor::{actor_rect, draw_actor, SpriteCache};
use crate::colors::{
    C_DIM, C_DMG_DIM, C_DMG_HOT, C_DMG_MID, C_ENT_DEAD, C_ENT_MONSTER, C_ENT_NPC, C_ENT_PLAYER,
    C_ENT_SELF, C_ENT_TARGET, C_ERR, C_PANEL, C_TITLE,
};
use crate::debug::{draw_debug_overlay, layers_desc, DEBUG_OVERLAY};
use crate::geom::{cam_parts, cell_to_screen};
use crate::gfx::{fill, text, trunc, viewport_rect};
use crate::hud::draw_hud;
use crate::layout::{BAR_TOP, VIEW_H};
use crate::net::Net;
use crate::tiles::{draw_tile, tile_in_view, TileKey, TileTex};
use crate::window::WIN_W;

use crate::{font, ui};

/// 切换到第 `i` 张地图（按容器内名字升序）。
pub(crate) fn load_map(
    a: &Archive,
    i: usize,
    map: &mut Option<Map>,
    err: &mut String,
    cam: &mut (f32, f32),
) {
    let Some(e) = a.entries().get(i) else {
        return;
    };
    let name = e.name.clone();
    match Map::load(a, &name) {
        Ok(m) => {
            println!(
                "[map] {} {}x{} {} B/格{}",
                name,
                m.width,
                m.height,
                m.cell_len,
                if m.is_extended() {
                    "（扩展布局）"
                } else {
                    ""
                }
            );
            // 镜头对准离中心最近的前景物件：中心常常是空地，
            // 一进去看到空白会让人以为渲染坏了。
            let (w, h) = (m.width as i32, m.height as i32);
            let (tx, ty) = m.nearest_front_tile(w / 2, h / 2).unwrap_or((w / 2, h / 2));
            let cols = WIN_W as i32 / UNIT_X;
            let rows = VIEW_H as i32 / UNIT_Y;
            *cam = (tx as f32 - cols as f32 / 2.0, ty as f32 - rows as f32 / 2.0);
            *err = String::new();
            *map = Some(m);
        }
        Err(e) => {
            // 已知缺口：EM*/T2* 族布局未定（assets.md §3.3b）——这里如实显示，不猜。
            println!("[map] {name} 解析失败：{e}");
            *err = e.to_string();
            *map = None;
        }
    }
}

#[allow(clippy::too_many_arguments)]
pub(crate) fn draw_map_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    names: &mut font::TextCache<'a>,
    // 界面字号（`UI_PX` 原生 14px）—— 对话窗用它；世界里的名字/飘字仍走 `names`
    ui_texts: &mut font::TextCache<'a>,
    ui: &mut ui::UiCache<'a>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<TileKey, TileTex<'a>>,
    draws: &mut Vec<TileDraw>,
    sprites: &mut SpriteCache<'a>,
    asset_dir: &Option<PathBuf>,
    map: &Option<Map>,
    map_err: &str,
    cam: (f32, f32),
    map_i: usize,
    map_count: usize,
    debug: bool,
    layers: u8,
    mouse: (f32, f32),
    ani_count: u32,
    net: Option<&Net>,
    // 锁定中的攻击目标（`None` = 没锁）—— 只用来给它的名字换色，让人看得出在打谁。
    combat_target: Option<u64>,
) -> Result<Option<u64>, sdl3::Error> {
    fill(canvas, 0.0, 0.0, WIN_W as f32, BAR_TOP, C_PANEL)?;

    let Some(dir) = asset_dir else {
        text(
            canvas,
            "ASSETS NOT FOUND - SET MIR2_ASSET_DIR",
            4.0,
            8.0,
            C_ERR,
        )?;
        return Ok(None);
    };

    if map.is_none() {
        let msg = if map_err.is_empty() {
            "NO MAP CONTAINER - RUN tools/m2pk/build.sh".to_string()
        } else {
            format!("PARSE FAILED: {}", trunc(map_err, 60))
        };
        text(canvas, &msg, 4.0, 8.0, C_ERR)?;
        return Ok(None);
    }
    let m = map.as_ref().unwrap();

    // 「画什么、按什么顺序画」是游戏知识，放在 core（map::visible_tiles，
    // 有单测守着三层顺序与隔格规则）；这里只负责取纹理 + 上屏。
    let cols = WIN_W as i32 / UNIT_X + 3;
    let rows = VIEW_H as i32 / UNIT_Y + 3;
    // 相机拆成"整格 + 亚格"：`visible_tiles` 只认整格（它给的落点是整数像素），
    // 亚格那半格由 `draw_tile` 减掉 ⇒ 地图逐帧平滑卷动（见 `cam_parts`）。
    let cp = cam_parts(cam);
    m.visible_tiles(cp.cell.0, cp.cell.1, cols, rows, ani_count, draws);
    // 裁剪到地图视口：`visible_tiles` 左上会多给一格（坐标可能为负），
    // 且高图块（树/墙）本身上端会超出视口——不裁剪就会画到上下信息条上。
    canvas.set_clip_rect(Some(Rect::new(0, BAR_TOP as i32, WIN_W, VIEW_H as u32)));
    let view = viewport_rect();
    for d in draws.iter() {
        // 逐层显隐（CTRL+1/2/3 / L）：关掉的层**既不画图块也不画调试框**。
        // 视口剔除：前景向下多扫了 35 行，那批候选多半够不着视口。
        // 先按 WZL 记录（不解码像素）判掉，省下解码与贴图上传。
        // ⚠️ 判据与落点必须是**同一份**（都含亚格偏移）——见 `tile_in_view`。
        if !tile_in_view(libs, dir, d, &cp, layers, &view) {
            continue;
        }
        draw_tile(canvas, tc, libs, tiles, dir, d, BAR_TOP, cp.sub)?;
    }
    canvas.set_clip_rect(None::<Rect>);

    // **悬停命中**（照原版 `g_FocusCret` / Crystal `MouseObject`）：按**精灵落点**
    // 而不是按格子（见 `actor_rect` 的说明）。重叠时取**脚最靠下**的那个 ——
    // Mir2 的 Y 序里它画在最前面，"指着谁就亮谁"。
    let mut hover: Option<u64> = None;
    if let Some(n) = net {
        if n.world.in_world() {
            let now = Instant::now();
            let mut best = f32::MIN;
            for e in n.world.entities.values() {
                let Some(r) = actor_rect(tc, sprites, dir, cam, e, n.anims.get(&e.id), now) else {
                    continue;
                };
                if mouse.0 >= r.x
                    && mouse.0 < r.x + r.w
                    && mouse.1 >= r.y
                    && mouse.1 < r.y + r.h
                    && e.y as f32 > best
                {
                    best = e.y as f32;
                    hover = Some(e.id);
                }
            }
        }
    }

    // 实体标记（B：联网之后"看得见世界"）。画在世界之上、调试叠加层之下 ——
    // 这样按 D 打开叠加层时，格网仍然压在最上面（否则标记会盖住格线，很难读）。
    if let Some(n) = net {
        if n.world.in_world() {
            let now = Instant::now();
            for e in n.world.entities.values() {
                // 颜色只用于**降级标记**（精灵走的是图本身）；尸体另给一色，
                // 这样"素材缺失 + 已死"也能一眼看出来。
                let color = if e.dead {
                    C_ENT_DEAD
                } else if combat_target == Some(e.id) {
                    // 锁定的目标：名字换成高亮色（"在打谁"要有反馈）
                    C_ENT_TARGET
                } else {
                    match e.kind {
                        0 => C_ENT_PLAYER,
                        2 => C_ENT_NPC,
                        _ => C_ENT_MONSTER,
                    }
                };
                // ⚠️ NPC **不画血条/数值**（用户 2026-10-09 第 1 条：截图里"夏家店老板 7/35"
                // 就是给它画了血条）。原版只给可打的对象画血条，NPC 只显示名字 ——
                // 传 0/0 给 `draw_name_bar` 就不会铺那条血条（它按 max_hp > 0 判）。
                let npc = e.kind == 2;
                draw_actor(
                    canvas,
                    tc,
                    names,
                    sprites,
                    dir,
                    cam,
                    e,
                    n.anims.get(&e.id),
                    now,
                    &e.name,
                    if npc { 0 } else { e.hp },
                    if npc { 0 } else { e.max_hp },
                    color,
                    // 悬停高亮（照原版 `g_FocusCret.DrawChr(..., blend=TRUE)`：
                    // **再画一遍**、半透明 —— 见 `HOVER_TINT`）
                    hover == Some(e.id),
                    // 别人不画数值（参考图里只有玩家头顶带）
                    false,
                )?;
            }

            // 自己：`entities` 里**没有自己**（快照刻意不含，见 core::world 的 self_feature）
            // ⇒ 在这里造一个临时实体走**同一条**绘制路径，免得"自己的画法"与别人漂成两套。
            let (hp, max_hp) = n.world.self_hp.unwrap_or((0, 0));
            let me = mir2_core::world::Entity {
                id: n.world.self_id,
                kind: 0,
                // **角色名**（`EnterWorld.self_name`，或选角时 `remember_self_name` 记下的）。
                // ⚠️ 用户 2026-10-09：这里**不许**再退回 "[自己]" 这种占位词 —— 名字真拿不到时
                // 宁可空着（`draw_name_bar` 对空串什么都不画），也不能显示一个假名字。
                name: n.world.self_name.clone(),
                x: n.world.self_pos.0,
                y: n.world.self_pos.1,
                dir: n.world.self_dir,
                feature: n.world.self_feature,
                hp,
                // 自己那份"跑"标记（决定播 ActWalk 还是 ActRun）
                run: n.world.self_run,
                max_hp,
                status_bits: 0,
                dead: n.world.self_dead,
                action: n.world.self_action,
                // 自己的动作事件计数（判"又砍了一刀"用它，不看值 —— 普通攻击恒为 1）
                action_seq: n.world.self_action_seq,
            };
            draw_actor(
                canvas,
                tc,
                names,
                sprites,
                dir,
                cam,
                &me,
                n.anims.get(&me.id),
                now,
                &me.name,
                hp,
                max_hp,
                C_ENT_SELF,
                false, // 自己永不算是"悬停高亮"（原版 `g_FocusCret <> g_MySelf`）
                true,  // 自己头顶画 `当前/总量`（用户参考图）
            )?;

            // 伤害飘字（A′：打怪要看得见数字）。往上飘，三档亮度代替淡出 ——
            // 8x8 调试字体只有一档颜色，靠 alpha 淡化在 `draw_debug_text` 上不一定生效。
            for (txt, fx, fy, born) in &n.floaters {
                let (px, py) = cell_to_screen(cam, *fx, *fy);
                let age = born.elapsed().as_millis();
                let col = if age < 300 {
                    C_DMG_HOT
                } else if age < 600 {
                    C_DMG_MID
                } else {
                    C_DMG_DIM
                };
                names.draw(
                    canvas,
                    tc,
                    txt,
                    px + 18.0,
                    py - 10.0 - age as f32 / 60.0,
                    (col.r, col.g, col.b),
                    Some((0, 0, 0)),
                )?;
            }
        }
    }

    // 辅助线/坐标叠加层（默认关闭，见 `DEBUG_OVERLAY`）
    if debug && DEBUG_OVERLAY {
        draw_debug_overlay(canvas, draws, tiles, cam, mouse, layers)?;
    }

    // 信息条
    let info = format!(
        "MAP {} [{}]  {}x{}  {}B/cell  CAM {},{}{}",
        m.title,
        map_i + 1,
        m.width,
        m.height,
        m.cell_len,
        cam.0,
        cam.1,
        if map_count == 0 {
            String::new()
        } else {
            format!(" /{map_count}")
        }
    );
    // **左上角叠加**（用户 2026-10-09：调试信息移到左上角、叠在游戏内容上）——
    // 不带底条，靠黑边压住底下的地图；两行：地图信息 / 世界摘要。
    let lh = names.line_height();
    names.draw(
        canvas,
        tc,
        &trunc(&info, 64),
        4.0,
        4.0,
        (C_TITLE.r, C_TITLE.g, C_TITLE.b),
        Some((0, 0, 0)),
    )?;
    // 右上角：层可见性（三层全开时不显示，免得占地方）+ 纹理缓存数
    let base = if layers == LAYERS_ALL {
        format!("TILES {}", tiles.len())
    } else {
        format!("LAYER {}   TILES {}", layers_desc(layers), tiles.len())
    };
    // 联网时把世界摘要接在后面：状态 / 视野实体数 / 世界变更次数 / 未识别消息数。
    // ⚠️ `CHG` 是"世界在动"最直接的观测量（联调时盯它涨没涨，比盯着画面猜靠谱）；
    // `UNK` 不该大于 0 —— 涨了就说明两边对不上（见 core::world）。
    let right = match net {
        Some(n) => format!(
            "NET {}  ENT {}  CHG {}  UNK {}   {}",
            trunc(&n.status, 40),
            n.world.entities.len(),
            n.changes,
            n.world.unknown,
            base
        ),
        None => base,
    };
    names.draw(
        canvas,
        tc,
        &trunc(&right, 64),
        4.0,
        4.0 + lh,
        (C_DIM.r, C_DIM.g, C_DIM.b),
        Some((0, 0, 0)),
    )?;

    // **底部操作面板**：最后贴（原版也是最后贴的），盖住世界下沿
    draw_hud(canvas, tc, ui, names, ui_texts, dir, net, &m.title)?;

    // 悬停结果还给主循环：光标要跟着它换（Crystal 的 Attack 光标）
    Ok(hover)
}

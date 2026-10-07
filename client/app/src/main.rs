//! MIR2 1.76 客户端 —— 开发期查看器（两个模式）
//!
//! * **登录界面**：窗口 / 文本输入 / 2D 渲染 / 程序化音乐（M0 的 SDL3 落地验证）
//! * **地图视图**：从 M2PK 容器加载真实 `.map`，绘制**三层**（地表 `Tiles` /
//!   中间 `SmTiles` / 前景 `Objects<N>`）—— M1「地图加载」的验收
//!
//! 渲染几何来自官方客户端 `Grobal2.pas:45`：`UNITX=48`、`UNITY=32`（逻辑格 48×32）。
//! 实测图块尺寸：`Tiles` = 96×64（**2×2 格** ⇒ 只在 x、y 皆为偶数的格上画）、
//! `SmTiles` = 48×32、`Objects` = 48×宽×不定高（后两者每格都画）。
//!
//! 资产目录解析：`$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data`；
//! 容器路径：`$MIR2_MAP_CONTAINER` → `assets/map/maps.m2pk`。找不到就降级显示，不崩。
//!
//! 屏幕文字用 SDL3 内置 8x8 调试字体，**只认 ASCII**。

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use mir2_core::m2pk::Archive;
use mir2_core::map::{Lib, Map, TileDraw, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream};
use sdl3::event::Event;
use sdl3::keyboard::Keycode;
use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
// 注：`WindowContext` 在 sdl3 里是私有类型、不可具名，
// 故凡是需要纹理创建器的地方一律对类型参数 `T` 泛化。
use sdl3::render::{
    BlendMode, FPoint, FRect, ScaleMode, Texture, TextureAccess, TextureCreator, WindowCanvas,
};
use sdl3::EventPump;

const WIN_W: u32 = 640;
const WIN_H: u32 = 480;
const SAMPLE_RATE: i32 = 44_100;

/// 地图视图的顶部信息条高度。
const BAR_TOP: f32 = 24.0;
/// 底部提示条高度。
const BAR_BOTTOM: f32 = 22.0;
/// 地图可视区高度。
const VIEW_H: f32 = WIN_H as f32 - BAR_TOP - BAR_BOTTOM;

/// 右侧信息区每行最大列数（内置字体等宽 8px）。
const INFO_COLS: usize = 28;
const INFO_LINE_H: f32 = 16.0;

/// 图块纹理缓存上限；超出就整批丢掉重建（开发期查看器，够用且简单）。
const TILE_CACHE_CAP: usize = 4000;

/// 登录模式可浏览的图库。
const LIBS: &[&str] = &[
    "Prguse", "Hum", "Items", "Mon1", "Tiles", "Magic", "ChrSel", "Effect", "Weapon",
];

// ---------- 配色 ----------
const C_BG: Color = Color::RGB(10, 14, 28);
const C_PANEL: Color = Color::RGB(22, 30, 56);
const C_PANEL_BORDER: Color = Color::RGB(90, 120, 170);
const C_TITLE: Color = Color::RGB(232, 200, 96);
const C_TEXT: Color = Color::RGB(206, 212, 226);
const C_DIM: Color = Color::RGB(120, 132, 156);
const C_FIELD: Color = Color::RGB(8, 10, 20);
const C_ACTIVE: Color = Color::RGB(255, 236, 140);
const C_BTN: Color = Color::RGB(46, 70, 116);
const C_BTN_BORDER: Color = Color::RGB(150, 186, 236);
const C_CHECKER_A: Color = Color::RGB(34, 38, 52);
const C_CHECKER_B: Color = Color::RGB(26, 30, 42);
const C_OK: Color = Color::RGB(120, 220, 150);
const C_ERR: Color = Color::RGB(232, 120, 120);

// ---------- 程序化音乐 ----------
const TEMPO_SEC: f32 = 0.34;
/// (MIDI 音高, 拍数)；音高 0 表示休止
const MELODY: &[(u8, f32)] = &[
    (72, 1.0),
    (74, 1.0),
    (76, 1.0),
    (79, 1.0),
    (76, 1.0),
    (74, 1.0),
    (72, 2.0),
    (69, 1.0),
    (72, 1.0),
    (76, 1.0),
    (74, 2.0),
    (72, 1.0),
    (69, 1.0),
    (67, 2.0),
    (0, 1.0),
];

struct Music {
    sr: f32,
    phase: f32,
    note: usize,
    elapsed: f32,
    muted: Arc<AtomicBool>,
}

impl Music {
    fn new(sr: f32, muted: Arc<AtomicBool>) -> Self {
        Self {
            sr,
            phase: 0.0,
            note: 0,
            elapsed: 0.0,
            muted,
        }
    }

    fn freq(midi: u8) -> f32 {
        if midi == 0 {
            0.0
        } else {
            440.0 * 2f32.powf((midi as f32 - 69.0) / 12.0)
        }
    }
}

impl AudioCallback<f32> for Music {
    fn callback(&mut self, stream: &mut AudioStream, requested: i32) {
        let n = requested.max(0) as usize;
        let mut out = Vec::with_capacity(n);
        let silent = self.muted.load(Ordering::Relaxed);

        for _ in 0..n {
            let (midi, beats) = MELODY[self.note];
            let dur = (beats * TEMPO_SEC).max(0.05);
            let t = self.elapsed / dur;
            let env = if t < 0.03 {
                t / 0.03
            } else {
                (1.0 - (t - 0.03) / 0.97).clamp(0.0, 1.0)
            };

            let f = Self::freq(midi);
            let s = if f > 0.0 && !silent {
                let sq = if (self.phase % 1.0) < 0.5 { 1.0 } else { -1.0 };
                sq * env * 0.10
            } else {
                0.0
            };
            out.push(s);

            if f > 0.0 {
                self.phase += f / self.sr;
                if self.phase >= 1.0 {
                    self.phase -= 1.0;
                }
            }
            self.elapsed += 1.0 / self.sr;
            if self.elapsed >= dur {
                self.elapsed = 0.0;
                self.note = (self.note + 1) % MELODY.len();
            }
        }

        let _ = stream.put_data_f32(&out);
    }
}

// ---------- 绘制辅助 ----------
fn fill(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    col: Color,
) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.fill_rect(FRect::new(x, y, w, h))
}

fn frame(
    c: &mut WindowCanvas,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    col: Color,
) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_rect(FRect::new(x, y, w, h))
}

fn text(c: &mut WindowCanvas, s: &str, x: f32, y: f32, col: Color) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_debug_text(s, FPoint::new(x, y))
}

fn center_x(s: &str, area_x: f32, area_w: f32) -> f32 {
    area_x + (area_w - s.chars().count() as f32 * 8.0) / 2.0
}

fn trunc(s: &str, cols: usize) -> String {
    s.chars().take(cols).collect()
}

/// 在预览面板里画棋盘格底（证明透明区真的透明）。
fn checkerboard(c: &mut WindowCanvas, x: f32, y: f32, w: f32, h: f32) -> Result<(), sdl3::Error> {
    const T: f32 = 8.0;
    let mut yy = 0.0;
    while yy < h {
        let mut xx = 0.0;
        while xx < w {
            let col = if ((xx / T) as i32 + (yy / T) as i32) % 2 == 0 {
                C_CHECKER_A
            } else {
                C_CHECKER_B
            };
            fill(c, x + xx, y + yy, T.min(w - xx), T.min(h - yy), col)?;
            xx += T;
        }
        yy += T;
    }
    Ok(())
}

// ---------- 路径解析 ----------
/// `$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data`。
fn resolve_asset_dir() -> Option<PathBuf> {
    for key in ["MIR2_ASSET_DIR", "MIR2C_DATA"] {
        if let Ok(v) = std::env::var(key) {
            let p = PathBuf::from(v);
            if p.is_dir() {
                return Some(p);
            }
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../mir2c/data");
    guess.canonicalize().ok().filter(|p| p.is_dir())
}

/// `$MIR2_MAP_CONTAINER` → 仓库的 `assets/map/maps.m2pk`。
fn resolve_container() -> Option<PathBuf> {
    if let Ok(v) = std::env::var("MIR2_MAP_CONTAINER") {
        let p = PathBuf::from(v);
        if p.is_file() {
            return Some(p);
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/map/maps.m2pk");
    guess.canonicalize().ok().filter(|p| p.is_file())
}

// ---------- 图块纹理缓存 ----------
/// 图块缓存键：图库 + `Objects` 的编号 + 图号。
type TileKey = (Lib, u8, u16);

/// 保证 `cache` 里有该图块的纹理；解不出来就返回 `None`（原版也有大量空壳图）。
fn ensure_tile<'a, T>(
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    cache: &mut HashMap<TileKey, Texture<'a>>,
    dir: &Path,
    lib_kind: Lib,
    area: u8,
    idx: u16,
) -> Option<()> {
    let key = (lib_kind, area, idx);
    if cache.contains_key(&key) {
        return Some(());
    }
    if cache.len() >= TILE_CACHE_CAP {
        cache.clear();
    }
    // 文件名规则是游戏知识，放在 core（Lib::file_name，对应 GetObjs）
    let name = lib_kind.file_name(area);
    let lib = libs
        .entry(name.clone())
        .or_insert_with(|| Wzl::open(dir.join(&name)).ok())
        .as_ref()?;
    let sprite = lib.decode(idx as usize)?;
    if sprite.is_empty() {
        return None;
    }
    let mut t = tc
        .create_texture(
            PixelFormat::RGBA32,
            TextureAccess::Streaming,
            sprite.width as u32,
            sprite.height as u32,
        )
        .ok()?;
    t.set_blend_mode(BlendMode::Blend);
    t.set_scale_mode(ScaleMode::Nearest);
    t.update(None::<Rect>, &sprite.rgba, sprite.width as usize * 4)
        .ok()?;
    cache.insert(key, t);
    Some(())
}

/// 按一条 [`TileDraw`] 把图块画出来。
///
/// `'a` 把纹理创建器与缓存绑在一起——`Texture<'a>` 借的是创建器，
/// 少了这层关联编译器就没法确认缓存不会比创建器活得久。
fn draw_tile<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    cache: &mut HashMap<TileKey, Texture<'a>>,
    dir: &Path,
    d: &TileDraw,
    origin_y: f32,
) -> Result<(), sdl3::Error> {
    let _ = ensure_tile(tc, libs, cache, dir, d.lib, d.area, d.index);
    if let Some(t) = cache.get(&(d.lib, d.area, d.index)) {
        let q = t.query();
        canvas.copy(
            t,
            None::<FRect>,
            FRect::new(
                d.x as f32,
                origin_y + d.y as f32,
                q.width as f32,
                q.height as f32,
            ),
        )?;
    }
    Ok(())
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let sdl = sdl3::init()?;
    let video = sdl.video()?;

    let window = video
        .window("MIR2 1.76 CLIENT - DEV VIEWER", WIN_W, WIN_H)
        .position_centered()
        .build()
        .map_err(|e| format!("创建窗口失败: {e}"))?;

    video.text_input().start(&window);
    let mut canvas = window.into_canvas();
    let tex_creator = canvas.texture_creator();

    // ---------- 音频 ----------
    let audio = sdl.audio()?;
    let muted = Arc::new(AtomicBool::new(false));
    let spec = AudioSpec {
        freq: Some(SAMPLE_RATE),
        channels: Some(1),
        format: Some(AudioFormat::F32LE),
    };
    let device =
        audio.open_playback_stream(&spec, Music::new(SAMPLE_RATE as f32, muted.clone()))?;
    device.resume()?;

    // ---------- 资产 ----------
    let asset_dir = resolve_asset_dir();
    let container_path = resolve_container();
    let archive = match &container_path {
        Some(p) => match Archive::open(p) {
            Ok(a) => {
                println!("[mir2-app] 地图容器 = {}（{} 张）", p.display(), a.len());
                Some(a)
            }
            Err(e) => {
                println!("[mir2-app] 地图容器打不开：{e}");
                None
            }
        },
        None => {
            println!("[mir2-app] 未找到地图容器（先跑 tools/m2pk/build.sh）");
            None
        }
    };
    match &asset_dir {
        Some(d) => println!("[mir2-app] 资产目录 = {}", d.display()),
        None => println!("[mir2-app] 未找到资产目录：设 MIR2_ASSET_DIR=<mir2c/data>"),
    }
    println!("[mir2-app] 音频驱动 = {}", audio.current_audio_driver());
    println!("[mir2-app] 操作：F1 登录界面 / F2 地图视图 / M 音乐 / ESC 退出");

    let mut events: EventPump = sdl.event_pump()?;

    // 登录模式的状态
    let mut id = String::new();
    let mut pw = String::new();
    let mut active: usize = 0;
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // 地图模式的状态
    let mut mode: u8 = 2; // 1 = 登录，2 = 地图（直接开在地图视图上）
    let mut map_i: usize = 0;
    let mut map: Option<Map> = None;
    let mut map_err = String::new();
    let mut cam = (0i32, 0i32);
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, Texture<'_>> = HashMap::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    let mut music_on = true;
    let started = Instant::now();

    // 载入初始地图
    if let Some(a) = &archive {
        load_map(a, map_i, &mut map, &mut map_err, &mut cam);
    }

    'main: loop {
        for ev in events.poll_iter() {
            match ev {
                Event::Quit { .. } => break 'main,
                Event::KeyDown { keycode, .. } => match keycode {
                    Some(Keycode::Escape) => break 'main,
                    Some(Keycode::F1) => mode = 1,
                    Some(Keycode::F2) => mode = 2,
                    Some(Keycode::M) => {
                        music_on = !music_on;
                        muted.store(!music_on, Ordering::Relaxed);
                        status = format!("MUSIC {}", if music_on { "ON" } else { "OFF" });
                    }
                    _ if mode == 1 => match keycode {
                        Some(Keycode::Tab) => active = 1 - active,
                        Some(Keycode::Backspace) => {
                            if active == 0 {
                                id.pop();
                            } else {
                                pw.pop();
                            }
                        }
                        Some(Keycode::Return) => {
                            let who = if id.is_empty() { "GUEST" } else { id.as_str() };
                            println!(
                                "[login] 用户名={:?} 密码长度={} → 桩实现（尚未连接服务端）",
                                who,
                                pw.chars().count()
                            );
                            status = format!("LOGIN AS {} ... STUB OK", who);
                        }
                        Some(Keycode::LeftBracket) => {
                            lib_idx = (lib_idx + LIBS.len() - 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::RightBracket) => {
                            lib_idx = (lib_idx + 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::Comma) | Some(Keycode::Left) => {
                            img_idx = img_idx.saturating_sub(1);
                        }
                        Some(Keycode::Period) | Some(Keycode::Right) => img_idx += 1,
                        _ => {}
                    },
                    _ if mode == 2 => match keycode {
                        Some(Keycode::Left) => cam.0 -= 2,
                        Some(Keycode::Right) => cam.0 += 2,
                        Some(Keycode::Up) => cam.1 -= 2,
                        Some(Keycode::Down) => cam.1 += 2,
                        Some(Keycode::Home) => cam = (0, 0),
                        Some(Keycode::LeftBracket) | Some(Keycode::RightBracket) => {
                            if let Some(a) = &archive {
                                let step: i64 = if keycode == Some(Keycode::RightBracket) {
                                    1
                                } else {
                                    -1
                                };
                                let n = a.len() as i64;
                                map_i = (((map_i as i64 + step) % n + n) % n) as usize;
                                load_map(a, map_i, &mut map, &mut map_err, &mut cam);
                            }
                        }
                        _ => {}
                    },
                    _ => {}
                },
                Event::TextInput { text: t, .. } if mode == 1 => {
                    for ch in t.chars() {
                        if ch.is_ascii_graphic() || ch == ' ' {
                            if active == 0 && id.chars().count() < 12 {
                                id.push(ch);
                            } else if active == 1 && pw.chars().count() < 12 {
                                pw.push(ch);
                            }
                        }
                    }
                }
                _ => {}
            }
        }

        // 地图镜头夹在合理范围内（允许露出边缘一格）
        if let Some(m) = &map {
            let max_x = (m.width as i32 - (WIN_W as i32 / UNIT_X) + 2).max(0);
            let max_y = (m.height as i32 - (VIEW_H as i32 / UNIT_Y) + 2).max(0);
            cam.0 = cam.0.clamp(-2, max_x);
            cam.1 = cam.1.clamp(-2, max_y);
        }

        canvas.set_draw_color(C_BG);
        canvas.clear();

        if mode == 2 {
            draw_map_view(
                &mut canvas,
                &tex_creator,
                &mut libs,
                &mut tiles,
                &mut draws,
                &asset_dir,
                &map,
                &map_err,
                cam,
                map_i,
                archive.as_ref().map(|a| a.len()).unwrap_or(0),
            )?;
        } else {
            draw_login_view(
                &mut canvas,
                &tex_creator,
                &asset_dir,
                &mut loaded,
                &mut sprite_tex,
                &id,
                &pw,
                active,
                &status,
                music_on,
                lib_idx,
                &mut img_idx,
                started,
            )?;
        }

        // 顶部/底部公共条
        let hint = if mode == 2 {
            "ARROWS PAN   [ ] MAP   HOME RESET   F1 LOGIN   M MUSIC   ESC QUIT"
        } else {
            "TAB FIELD   ENTER LOGIN   [ ] LIB   , . IMG   F2 MAP   M MUSIC   ESC QUIT"
        };
        fill(
            &mut canvas,
            0.0,
            WIN_H as f32 - BAR_BOTTOM,
            WIN_W as f32,
            BAR_BOTTOM,
            C_PANEL,
        )?;
        text(
            &mut canvas,
            hint,
            4.0,
            WIN_H as f32 - BAR_BOTTOM + 6.0,
            C_DIM,
        )?;

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

/// 切换到第 `i` 张地图（按容器内名字升序）。
fn load_map(a: &Archive, i: usize, map: &mut Option<Map>, err: &mut String, cam: &mut (i32, i32)) {
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
            *cam = (m.width as i32 / 2, m.height as i32 / 2);
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
fn draw_map_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    libs: &mut HashMap<String, Option<Wzl>>,
    tiles: &mut HashMap<TileKey, Texture<'a>>,
    draws: &mut Vec<TileDraw>,
    asset_dir: &Option<PathBuf>,
    map: &Option<Map>,
    map_err: &str,
    cam: (i32, i32),
    map_i: usize,
    map_count: usize,
) -> Result<(), sdl3::Error> {
    fill(canvas, 0.0, 0.0, WIN_W as f32, BAR_TOP, C_PANEL)?;

    let Some(dir) = asset_dir else {
        text(
            canvas,
            "ASSETS NOT FOUND - SET MIR2_ASSET_DIR",
            4.0,
            8.0,
            C_ERR,
        )?;
        return Ok(());
    };

    if map.is_none() {
        let msg = if map_err.is_empty() {
            "NO MAP CONTAINER - RUN tools/m2pk/build.sh".to_string()
        } else {
            format!("PARSE FAILED: {}", trunc(map_err, 60))
        };
        text(canvas, &msg, 4.0, 8.0, C_ERR)?;
        return Ok(());
    }
    let m = map.as_ref().unwrap();

    // 「画什么、按什么顺序画」是游戏知识，放在 core（map::visible_tiles，
    // 有单测守着三层顺序与隔格规则）；这里只负责取纹理 + 上屏。
    let cols = WIN_W as i32 / UNIT_X + 3;
    let rows = VIEW_H as i32 / UNIT_Y + 3;
    m.visible_tiles(cam.0, cam.1, cols, rows, draws);
    for d in draws.iter() {
        draw_tile(canvas, tc, libs, tiles, dir, d, BAR_TOP)?;
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
    text(canvas, &trunc(&info, 79), 4.0, 8.0, C_TITLE)?;
    let right = format!("TILES {}", tiles.len());
    text(canvas, &right, WIN_W as f32 - 96.0, 8.0, C_DIM)?;
    Ok(())
}

#[allow(clippy::too_many_arguments)]
fn draw_login_view<'a, T>(
    canvas: &mut WindowCanvas,
    tc: &'a TextureCreator<T>,
    asset_dir: &Option<PathBuf>,
    loaded: &mut Option<(usize, Wzl)>,
    sprite_tex: &mut Option<Texture<'a>>,
    id: &str,
    pw: &str,
    active: usize,
    status: &str,
    music_on: bool,
    lib_idx: usize,
    img_idx: &mut usize,
    started: Instant,
) -> Result<(), sdl3::Error> {
    // 右侧：真实精灵
    let mut info_lines: Vec<(String, Color)> = vec![("NO ASSETS".to_string(), C_ERR)];
    let mut sprite_dims = (0u32, 0u32);
    let mut ready = false;

    if let Some(dir) = asset_dir {
        let name = LIBS[lib_idx];
        if loaded.as_ref().map(|(i, _)| *i) != Some(lib_idx) {
            *loaded = Wzl::open(dir.join(name)).ok().map(|l| (lib_idx, l));
            *sprite_tex = None;
        }
        if let Some((_, lib)) = loaded.as_ref() {
            let total = lib.len();
            if *img_idx >= total {
                *img_idx = 0;
            }
            let mut found = None;
            for k in 0..64 {
                let i = (*img_idx + k) % total.max(1);
                if let Some(s) = lib.decode(i) {
                    found = Some((i, s));
                    break;
                }
            }
            if let Some((i, s)) = found {
                *img_idx = i;
                let is16 = lib.record(i).map(|r| r.is_16bit()).unwrap_or(false);
                sprite_dims = (s.width as u32, s.height as u32);
                info_lines = vec![
                    (format!("{}  #{} / {}", name, i, total), C_TEXT),
                    (
                        format!(
                            "{}x{}   ANCHOR({},{})",
                            s.width, s.height, s.anchor_x, s.anchor_y
                        ),
                        C_DIM,
                    ),
                    (
                        if is16 { "DIRECT 16BIT" } else { "PALETTE 8BIT" }.to_string(),
                        C_OK,
                    ),
                ];
                ready = true;

                let need_new = match sprite_tex.as_ref() {
                    Some(t) => {
                        let q = t.query();
                        q.width != s.width as u32 || q.height != s.height as u32
                    }
                    None => true,
                };
                if need_new {
                    *sprite_tex = None;
                }
                if sprite_tex.is_none() {
                    if let Ok(mut t) = tc.create_texture(
                        PixelFormat::RGBA32,
                        TextureAccess::Streaming,
                        s.width as u32,
                        s.height as u32,
                    ) {
                        t.set_blend_mode(BlendMode::Blend);
                        t.set_scale_mode(ScaleMode::Nearest);
                        *sprite_tex = Some(t);
                    }
                }
                if let Some(t) = sprite_tex.as_mut() {
                    if t.update(None::<Rect>, &s.rgba, s.width as usize * 4)
                        .is_err()
                    {
                        ready = false;
                    }
                }
            } else {
                info_lines = vec![(format!("{}  #{}  (空壳图)", name, img_idx), C_DIM)];
            }
        } else {
            info_lines = vec![(format!("{name}.wzl 打不开"), C_ERR)];
        }
    }

    let t1 = "MIR2  1.76  CLIENT";
    text(canvas, t1, center_x(t1, 0.0, WIN_W as f32), 16.0, C_TITLE)?;
    let t2 = "SDL3  DEV  VIEWER  (F2 = MAP)";
    text(canvas, t2, center_x(t2, 0.0, WIN_W as f32), 34.0, C_DIM)?;

    const LX: f32 = 20.0;
    const LW: f32 = 336.0;
    fill(canvas, LX, 60.0, LW, 250.0, C_PANEL)?;
    frame(canvas, LX, 60.0, LW, 250.0, C_PANEL_BORDER)?;

    text(canvas, "ACCOUNT", LX + 14.0, 96.0, C_TEXT)?;
    fill(canvas, LX + 14.0, 112.0, LW - 28.0, 22.0, C_FIELD)?;
    frame(
        canvas,
        LX + 14.0,
        112.0,
        LW - 28.0,
        22.0,
        if active == 0 {
            C_ACTIVE
        } else {
            C_PANEL_BORDER
        },
    )?;
    text(canvas, id, LX + 20.0, 119.0, C_TEXT)?;

    text(canvas, "PASSWORD", LX + 14.0, 152.0, C_TEXT)?;
    fill(canvas, LX + 14.0, 168.0, LW - 28.0, 22.0, C_FIELD)?;
    frame(
        canvas,
        LX + 14.0,
        168.0,
        LW - 28.0,
        22.0,
        if active == 1 {
            C_ACTIVE
        } else {
            C_PANEL_BORDER
        },
    )?;
    let masked = "*".repeat(pw.chars().count());
    text(canvas, &masked, LX + 20.0, 175.0, C_TEXT)?;

    fill(canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN)?;
    frame(canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
    text(
        canvas,
        "LOGIN",
        LX + 14.0 + (140.0 - 40.0) / 2.0,
        220.0,
        C_ACTIVE,
    )?;
    fill(canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN)?;
    frame(canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
    text(
        canvas,
        "EXIT",
        LX + 182.0 + (140.0 - 32.0) / 2.0,
        220.0,
        C_TEXT,
    )?;

    let mus = format!("MUSIC: {}", if music_on { "ON" } else { "OFF" });
    text(
        canvas,
        &mus,
        LX + 14.0,
        256.0,
        if music_on { C_ACTIVE } else { C_DIM },
    )?;
    text(
        canvas,
        &format!("UPTIME {:.0}s", started.elapsed().as_secs_f32()),
        LX + 200.0,
        256.0,
        C_DIM,
    )?;
    text(
        canvas,
        &format!("STATUS: {status}"),
        LX + 14.0,
        280.0,
        C_TEXT,
    )?;

    // 右侧精灵面板
    const RX: f32 = 372.0;
    const RW: f32 = 248.0;
    fill(canvas, RX, 60.0, RW, 250.0, C_PANEL)?;
    frame(canvas, RX, 60.0, RW, 250.0, C_PANEL_BORDER)?;
    text(canvas, "SPRITE (REAL .WZL)", RX + 12.0, 72.0, C_TITLE)?;

    const PX: f32 = RX + 12.0;
    const PY: f32 = 92.0;
    const PW: f32 = RW - 24.0;
    const PH: f32 = 132.0;
    checkerboard(canvas, PX, PY, PW, PH)?;
    frame(canvas, PX, PY, PW, PH, C_PANEL_BORDER)?;

    if ready {
        if let Some(t) = sprite_tex.as_ref() {
            let (sw, sh) = (sprite_dims.0 as f32, sprite_dims.1 as f32);
            let scale = (PW / sw).min(PH / sh).floor().clamp(1.0, 6.0);
            let (dw, dh) = (sw * scale, sh * scale);
            canvas.copy(
                t,
                None::<FRect>,
                FRect::new(PX + (PW - dw) / 2.0, PY + (PH - dh) / 2.0, dw, dh),
            )?;
        }
    } else {
        let msg = if asset_dir.is_none() {
            "ASSETS NOT FOUND"
        } else {
            "NO SPRITE"
        };
        text(canvas, msg, center_x(msg, PX, PW), PY + 60.0, C_ERR)?;
    }

    for (i, (line, col)) in info_lines.iter().enumerate().take(3) {
        text(
            canvas,
            &trunc(line, INFO_COLS),
            PX,
            PY + PH + 8.0 + i as f32 * INFO_LINE_H,
            *col,
        )?;
    }
    let libl = format!("LIB [{}/{}]   IMG {}", lib_idx + 1, LIBS.len(), *img_idx);
    text(canvas, &libl, PX, PY + PH + 8.0 + 3.0 * INFO_LINE_H, C_TEXT)?;
    Ok(())
}

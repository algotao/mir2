//! MIR2 1.76 客户端 —— 登录界面 + **真实美术精灵预览**
//!
//! 两件事：
//!   1. 登录界面骨架（窗口 / 文本输入 / 按钮 / 程序化音乐）—— M0 的 SDL3 落地验证
//!   2. **真实 `.wzl` 精灵渲染** —— M1 第一条验收「调色板查表出图**无色差**」
//!
//! 精灵来自 `mir2-core` 的 WZL/WZX 解码器（规格见 `docs/assets.md §3.2b`），
//! 调色板是经典 MIR2 256 色（`mir2_core::palette`），**与 Python 参考实现逐字节一致**
//! （core 里有黄金哈希测试）。
//!
//! 资产目录按顺序解析：`$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁的 `mir2c/data`。
//! **找不到就降级**：右侧面板显示 `ASSETS NOT FOUND`，其余功能照常。
//!
//! 说明：屏幕文字用 SDL3 内置 8x8 调试字体（正式版换自带点阵字库），**只认 ASCII**。

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use mir2_core::wzl::{Sprite, Wzl};

use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream};
use sdl3::event::Event;
use sdl3::keyboard::Keycode;
use sdl3::pixels::{Color, PixelFormat};
use sdl3::rect::Rect;
use sdl3::render::{BlendMode, FPoint, FRect, ScaleMode, Texture, TextureAccess, WindowCanvas};
use sdl3::EventPump;

const WIN_W: u32 = 640;
const WIN_H: u32 = 480;
const SAMPLE_RATE: i32 = 44_100;

/// 可浏览的图库（都在 `data/` 下）。按 `[` / `]` 切换。
const LIBS: &[&str] = &[
    "Prguse", "Hum", "Items", "Mon1", "Tiles", "Magic", "ChrSel", "Effect", "Weapon",
];

// ---------- 配色（1.76 的深蓝 / 暗金风格）----------
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
const TEMPO_SEC: f32 = 0.34; // 每拍秒数
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
            // 简易包络：快速起音 + 线性衰减（避免爆音）
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

/// 内置字体固定 8px 宽，用于水平居中
fn center_x(s: &str, area_x: f32, area_w: f32) -> f32 {
    area_x + (area_w - s.chars().count() as f32 * 8.0) / 2.0
}

// ---------- 资产 ----------
/// 按 `$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data` 的顺序解析资产目录。
fn resolve_asset_dir() -> Option<PathBuf> {
    for key in ["MIR2_ASSET_DIR", "MIR2C_DATA"] {
        if let Ok(v) = std::env::var(key) {
            let p = PathBuf::from(v);
            if p.is_dir() {
                return Some(p);
            }
        }
    }
    // 开发期默认：<repo>/../mir2c/data（client/app → 上跳三级到 $WS）
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../mir2c/data");
    guess.canonicalize().ok().filter(|p| p.is_dir())
}

fn open_lib(dir: &Path, name: &str) -> Option<Wzl> {
    Wzl::open(dir.join(name)).ok()
}

/// 从 `start` 起向后找第一张能解出像素的图（很多库前面是空壳）。
fn first_decodable(lib: &Wzl, start: usize, tries: usize) -> Option<(usize, Sprite)> {
    let n = lib.len();
    if n == 0 {
        return None;
    }
    for k in 0..tries {
        let i = (start + k) % n;
        if let Some(s) = lib.decode(i) {
            return Some((i, s));
        }
    }
    None
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

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let sdl = sdl3::init()?;
    let video = sdl.video()?;

    let window = video
        .window("MIR2 1.76 CLIENT - LOGIN", WIN_W, WIN_H)
        .position_centered()
        .build()
        .map_err(|e| format!("创建窗口失败: {e}"))?;

    // 必须先启用文本输入，再让 window 被 canvas 消费
    video.text_input().start(&window);
    let mut canvas = window.into_canvas();
    let tex_creator = canvas.texture_creator();

    // ---------- 音频：程序化循环旋律 ----------
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
    match &asset_dir {
        Some(d) => println!("[mir2-app] 资产目录 = {}", d.display()),
        None => {
            println!("[mir2-app] 未找到资产目录：设 MIR2_ASSET_DIR=<mir2c/data> 可启用精灵预览")
        }
    }

    println!("[mir2-app] SDL3 登录界面启动");
    println!("[mir2-app] 音频驱动 = {}", audio.current_audio_driver());
    println!(
        "[mir2-app] 操作：TAB 切换输入框 / ENTER 登录 / M 音乐 / [ ] 换图库 / , . 换图 / ESC 退出"
    );

    let mut events: EventPump = sdl.event_pump()?;

    let mut id = String::new();
    let mut pw = String::new();
    let mut active: usize = 0; // 0 = ID，1 = PASSWORD
    let mut status = String::from("READY");
    let mut music_on = true;

    let started = Instant::now();

    // 精灵浏览状态
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None; // (lib_idx, lib)
    let mut sprite_tex: Option<Texture> = None;

    'main: loop {
        for ev in events.poll_iter() {
            match ev {
                Event::Quit { .. } => break 'main,
                Event::KeyDown { keycode, .. } => match keycode {
                    Some(Keycode::Escape) | Some(Keycode::F4) => break 'main,
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
                    Some(Keycode::M) => {
                        music_on = !music_on;
                        muted.store(!music_on, Ordering::Relaxed);
                        status = format!("MUSIC {}", if music_on { "ON" } else { "OFF" });
                    }
                    // ---- 图库切换 ----
                    Some(Keycode::LeftBracket) => {
                        lib_idx = (lib_idx + LIBS.len() - 1) % LIBS.len();
                        img_idx = 0;
                    }
                    Some(Keycode::RightBracket) => {
                        lib_idx = (lib_idx + 1) % LIBS.len();
                        img_idx = 0;
                    }
                    // ---- 图像切换 ----
                    Some(Keycode::Comma) | Some(Keycode::Left) => {
                        img_idx = img_idx.saturating_sub(1);
                    }
                    Some(Keycode::Period) | Some(Keycode::Right) => img_idx += 1,
                    _ => {}
                },
                Event::TextInput { text: t, .. } => {
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

        // ---------- 精灵：按需加载 / 解码 / 上传纹理 ----------
        let mut sprite_info = String::from("NO ASSETS");
        let mut sprite_stat = C_ERR;
        let mut sprite_dims = (0u32, 0u32, 0i32, 0i32, false);
        let mut sprite_ready = false;

        if let Some(dir) = &asset_dir {
            let name = LIBS[lib_idx];
            if loaded.as_ref().map(|(i, _)| *i) != Some(lib_idx) {
                match open_lib(dir, name) {
                    Some(l) => loaded = Some((lib_idx, l)),
                    None => loaded = None,
                }
                sprite_tex = None;
            }

            if let Some((_, lib)) = &loaded {
                let total = lib.len();
                if img_idx >= total {
                    img_idx = 0;
                }
                if let Some((found, s)) = first_decodable(lib, img_idx, 64) {
                    if found != img_idx {
                        img_idx = found;
                    }
                    sprite_dims = (
                        s.width as u32,
                        s.height as u32,
                        s.anchor_x as i32,
                        s.anchor_y as i32,
                        false,
                    );
                    let rec = lib.record(img_idx);
                    let is16 = rec.map(|r| r.is_16bit()).unwrap_or(false);
                    sprite_dims.4 = is16;
                    sprite_info = format!(
                        "{} #{} / {}  {}x{}  ANCHOR({},{})  {}",
                        name,
                        img_idx,
                        total,
                        s.width,
                        s.height,
                        s.anchor_x,
                        s.anchor_y,
                        if is16 { "16BIT" } else { "8BIT+PAL" }
                    );
                    sprite_stat = C_OK;
                    sprite_ready = true;

                    // 纹理：尺寸变了就重建
                    let need_new = match &sprite_tex {
                        Some(t) => {
                            let q = t.query();
                            q.width != s.width as u32 || q.height != s.height as u32
                        }
                        None => true,
                    };
                    if need_new {
                        sprite_tex = None;
                    }
                    if sprite_tex.is_none() {
                        match tex_creator.create_texture(
                            PixelFormat::RGBA32,
                            TextureAccess::Streaming,
                            s.width as u32,
                            s.height as u32,
                        ) {
                            Ok(mut t) => {
                                t.set_blend_mode(BlendMode::Blend);
                                t.set_scale_mode(ScaleMode::Nearest); // 像素风：禁止插值
                                sprite_tex = Some(t);
                            }
                            Err(e) => {
                                sprite_info = format!("TEXTURE ERR: {e}");
                                sprite_stat = C_ERR;
                                sprite_ready = false;
                            }
                        }
                    }
                    if sprite_ready {
                        if let Some(t) = sprite_tex.as_mut() {
                            if let Err(e) = t.update(None::<Rect>, &s.rgba, s.width as usize * 4) {
                                sprite_info = format!("UPLOAD ERR: {e}");
                                sprite_stat = C_ERR;
                                sprite_ready = false;
                            }
                        }
                    }
                } else {
                    sprite_info = format!("{} #{}  (空壳图)", name, img_idx);
                    sprite_stat = C_DIM;
                }
            } else {
                sprite_info = format!("{name}.wzl 打不开");
            }
        }

        let blink = (started.elapsed().as_millis() / 500).is_multiple_of(2);

        // ---------- 绘制 ----------
        canvas.set_draw_color(C_BG);
        canvas.clear();

        let t1 = "MIR2  1.76  CLIENT";
        text(
            &mut canvas,
            t1,
            center_x(t1, 0.0, WIN_W as f32),
            16.0,
            C_TITLE,
        )?;
        let t2 = "SDL3  LOGIN  +  REAL  WZL  SPRITE";
        text(
            &mut canvas,
            t2,
            center_x(t2, 0.0, WIN_W as f32),
            34.0,
            C_DIM,
        )?;

        // ===== 左：登录面板 =====
        const LX: f32 = 20.0;
        const LW: f32 = 336.0;
        fill(&mut canvas, LX, 60.0, LW, 250.0, C_PANEL)?;
        frame(&mut canvas, LX, 60.0, LW, 250.0, C_PANEL_BORDER)?;

        text(&mut canvas, "ACCOUNT", LX + 14.0, 96.0, C_TEXT)?;
        fill(&mut canvas, LX + 14.0, 112.0, LW - 28.0, 22.0, C_FIELD)?;
        frame(
            &mut canvas,
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
        text(&mut canvas, &id, LX + 20.0, 119.0, C_TEXT)?;
        if active == 0 && blink {
            fill(
                &mut canvas,
                LX + 20.0 + id.chars().count() as f32 * 8.0,
                117.0,
                8.0,
                12.0,
                C_ACTIVE,
            )?;
        }

        text(&mut canvas, "PASSWORD", LX + 14.0, 152.0, C_TEXT)?;
        fill(&mut canvas, LX + 14.0, 168.0, LW - 28.0, 22.0, C_FIELD)?;
        frame(
            &mut canvas,
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
        text(&mut canvas, &masked, LX + 20.0, 175.0, C_TEXT)?;
        if active == 1 && blink {
            fill(
                &mut canvas,
                LX + 20.0 + masked.chars().count() as f32 * 8.0,
                173.0,
                8.0,
                12.0,
                C_ACTIVE,
            )?;
        }

        // 按钮
        fill(&mut canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN)?;
        frame(&mut canvas, LX + 14.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
        text(
            &mut canvas,
            "LOGIN",
            LX + 14.0 + (140.0 - 5.0 * 8.0) / 2.0,
            220.0,
            C_ACTIVE,
        )?;

        fill(&mut canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN)?;
        frame(&mut canvas, LX + 182.0, 210.0, 140.0, 28.0, C_BTN_BORDER)?;
        text(
            &mut canvas,
            "EXIT",
            LX + 182.0 + (140.0 - 4.0 * 8.0) / 2.0,
            220.0,
            C_TEXT,
        )?;

        let mus = format!("MUSIC: {}", if music_on { "ON" } else { "OFF" });
        text(
            &mut canvas,
            &mus,
            LX + 14.0,
            256.0,
            if music_on { C_ACTIVE } else { C_DIM },
        )?;
        let up = format!("UPTIME {:.0}s", started.elapsed().as_secs_f32());
        text(&mut canvas, &up, LX + 200.0, 256.0, C_DIM)?;

        text(
            &mut canvas,
            &format!("STATUS: {status}"),
            LX + 14.0,
            280.0,
            C_TEXT,
        )?;

        // ===== 右：真实精灵预览 =====
        const RX: f32 = 372.0;
        const RW: f32 = 248.0;
        fill(&mut canvas, RX, 60.0, RW, 250.0, C_PANEL)?;
        frame(&mut canvas, RX, 60.0, RW, 250.0, C_PANEL_BORDER)?;
        text(&mut canvas, "SPRITE (REAL .WZL)", RX + 12.0, 72.0, C_TITLE)?;

        // 预览区
        const PX: f32 = RX + 12.0;
        const PY: f32 = 92.0;
        const PW: f32 = RW - 24.0;
        const PH: f32 = 150.0;
        checkerboard(&mut canvas, PX, PY, PW, PH)?;
        frame(&mut canvas, PX, PY, PW, PH, C_PANEL_BORDER)?;

        if sprite_ready {
            if let Some(t) = &sprite_tex {
                let (sw, sh) = (sprite_dims.0 as f32, sprite_dims.1 as f32);
                // 整数倍放大，尽量填满，最多 6 倍
                let scale = (PW / sw).min(PH / sh).floor().clamp(1.0, 6.0);
                let dw = sw * scale;
                let dh = sh * scale;
                let dx = PX + (PW - dw) / 2.0;
                let dy = PY + (PH - dh) / 2.0;
                canvas.copy(t, None::<FRect>, FRect::new(dx, dy, dw, dh))?;
            }
        } else {
            let msg = if asset_dir.is_none() {
                "ASSETS NOT FOUND"
            } else {
                "NO SPRITE"
            };
            text(&mut canvas, msg, center_x(msg, PX, PW), PY + 66.0, C_ERR)?;
            if asset_dir.is_none() {
                let h1 = "SET  MIR2_ASSET_DIR";
                let h2 = "TO  mir2c/data";
                text(&mut canvas, h1, center_x(h1, PX, PW), PY + 84.0, C_DIM)?;
                text(&mut canvas, h2, center_x(h2, PX, PW), PY + 96.0, C_DIM)?;
            }
        }

        // 精灵信息（超宽则截断，避免画出面板）
        let info = if sprite_info.chars().count() > 29 {
            sprite_info.chars().take(29).collect::<String>()
        } else {
            sprite_info.clone()
        };
        text(&mut canvas, &info, PX, PY + PH + 8.0, sprite_stat)?;
        let libl = format!(
            "[{}/{}]  {}  IMG {}",
            lib_idx + 1,
            LIBS.len(),
            LIBS[lib_idx],
            img_idx
        );
        text(&mut canvas, &libl, PX, PY + PH + 24.0, C_TEXT)?;
        let nav = "[[ ]] LIB   [, .] OR ARROW  IMG";
        text(&mut canvas, nav, PX, PY + PH + 40.0, C_DIM)?;

        // ===== 底部提示 =====
        let hint = "TAB SWITCH  ENTER LOGIN  M MUSIC  ESC QUIT";
        text(
            &mut canvas,
            hint,
            center_x(hint, 0.0, WIN_W as f32),
            336.0,
            C_DIM,
        )?;
        let note = "SPRITE = CLASSIC MIR2 256-COLOR PALETTE (BYTE-IDENTICAL TO REFERENCE DECODER)";
        text(
            &mut canvas,
            note,
            center_x(note, 0.0, WIN_W as f32),
            356.0,
            C_DIM,
        )?;
        let note2 = "MUSIC IS PROCEDURAL PLACEHOLDER (ASSET AUDIO NOT WIRED YET)";
        text(
            &mut canvas,
            note2,
            center_x(note2, 0.0, WIN_W as f32),
            372.0,
            C_DIM,
        )?;

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出登录界面");
    Ok(())
}

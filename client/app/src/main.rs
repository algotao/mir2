//! MIR2 1.76 客户端 —— SDL3 极简登录界面（环境验证用）
//!
//! 目的：在还没开工正式客户端之前，先验证本机 SDL3 的四项能力：
//!   1. 窗口 + 事件泵 + 键盘 / 文本输入
//!   2. 2D 渲染（矩形绘制 + SDL3 内置 8x8 调试点阵字体 `SDL_RenderDebugText`）
//!   3. 音频播放（程序化合成的循环旋律）
//!   4. 定帧主循环
//!
//! 说明：美术与音频素材当前缺失（D-15），因此本版：
//!   - 文字用 SDL3 内置调试字体充当（正式版换成自带点阵字库）
//!   - 背景音乐用程序化方波旋律充当（正式版换成 WAV/MP3 素材）
//! 屏幕文字一律 ASCII（内置字体只认 ASCII）。

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream};
use sdl3::event::Event;
use sdl3::keyboard::Keycode;
use sdl3::pixels::Color;
use sdl3::render::{FPoint, FRect, WindowCanvas};
use sdl3::EventPump;

const WIN_W: u32 = 640;
const WIN_H: u32 = 480;
const SAMPLE_RATE: i32 = 44_100;

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

// ---------- 程序化音乐 ----------
const TEMPO_SEC: f32 = 0.34; // 每拍秒数
/// (MIDI 音高, 拍数)；音高 0 表示休止
const MELODY: &[(u8, f32)] = &[
    (72, 1.0), (74, 1.0), (76, 1.0), (79, 1.0),
    (76, 1.0), (74, 1.0), (72, 2.0),
    (69, 1.0), (72, 1.0), (76, 1.0), (74, 2.0),
    (72, 1.0), (69, 1.0), (67, 2.0),
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
        Self { sr, phase: 0.0, note: 0, elapsed: 0.0, muted }
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
fn fill(c: &mut WindowCanvas, x: f32, y: f32, w: f32, h: f32, col: Color) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.fill_rect(FRect::new(x, y, w, h))
}

fn frame(c: &mut WindowCanvas, x: f32, y: f32, w: f32, h: f32, col: Color) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_rect(FRect::new(x, y, w, h))
}

fn text(c: &mut WindowCanvas, s: &str, x: f32, y: f32, col: Color) -> Result<(), sdl3::Error> {
    c.set_draw_color(col);
    c.draw_debug_text(s, FPoint::new(x, y))
}

/// 内置字体固定 8px 宽，用于水平居中
fn center_x(s: &str) -> f32 {
    (WIN_W as f32 - s.chars().count() as f32 * 8.0) / 2.0
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

    // ---------- 音频：程序化循环旋律 ----------
    let audio = sdl.audio()?;
    let muted = Arc::new(AtomicBool::new(false));
    let spec = AudioSpec {
        freq: Some(SAMPLE_RATE),
        channels: Some(1),
        format: Some(AudioFormat::F32LE),
    };
    let device = audio.open_playback_stream(&spec, Music::new(SAMPLE_RATE as f32, muted.clone()))?;
    device.resume()?;

    println!("[mir2-app] SDL3 登录界面启动");
    println!("[mir2-app] 音频驱动 = {}", audio.current_audio_driver());
    println!("[mir2-app] 操作：TAB 切换输入框 / ENTER 登录 / M 开关音乐 / ESC 或关闭窗口退出");
    println!("[mir2-app] 注意：音乐为程序化合成占位（素材缺失 D-15）");

    let mut events: EventPump = sdl.event_pump()?;

    let mut id = String::new();
    let mut pw = String::new();
    let mut active: usize = 0; // 0 = ID，1 = PASSWORD
    let mut status = String::from("READY");
    let mut music_on = true;

    let started = Instant::now();

    'main: loop {
        for ev in events.poll_iter() {
            match ev {
                Event::Quit { .. } => break 'main,
                Event::KeyDown { keycode, .. } => match keycode {
                    Some(Keycode::Escape) | Some(Keycode::F4) => break 'main,
                    Some(Keycode::Tab) => active = 1 - active,
                    Some(Keycode::Backspace) => {
                        if active == 0 { id.pop(); } else { pw.pop(); }
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
                    _ => {}
                },
                Event::TextInput { text, .. } => {
                    for ch in text.chars() {
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

        let blink = (started.elapsed().as_millis() / 500) % 2 == 0;

        // ---------- 绘制 ----------
        canvas.set_draw_color(C_BG);
        canvas.clear();

        let t1 = "MIR2  1.76  CLIENT";
        text(&mut canvas, t1, center_x(t1), 40.0, C_TITLE)?;
        let t2 = "SDL3  LOGIN  SCREEN  (ENV TEST)";
        text(&mut canvas, t2, center_x(t2), 58.0, C_DIM)?;

        // 面板
        fill(&mut canvas, 96.0, 118.0, 448.0, 196.0, C_PANEL)?;
        frame(&mut canvas, 96.0, 118.0, 448.0, 196.0, C_PANEL_BORDER)?;

        // ID 输入框
        text(&mut canvas, "ID", 122.0, 158.0, C_TEXT)?;
        fill(&mut canvas, 200.0, 152.0, 316.0, 20.0, C_FIELD)?;
        frame(
            &mut canvas,
            200.0, 152.0, 316.0, 20.0,
            if active == 0 { C_ACTIVE } else { C_PANEL_BORDER },
        )?;
        text(&mut canvas, &id, 206.0, 158.0, C_TEXT)?;
        if active == 0 && blink {
            fill(&mut canvas, 206.0 + id.chars().count() as f32 * 8.0, 156.0, 8.0, 12.0, C_ACTIVE)?;
        }

        // PASSWORD 输入框
        text(&mut canvas, "PASSWORD", 122.0, 208.0, C_TEXT)?;
        fill(&mut canvas, 200.0, 202.0, 316.0, 20.0, C_FIELD)?;
        frame(
            &mut canvas,
            200.0, 202.0, 316.0, 20.0,
            if active == 1 { C_ACTIVE } else { C_PANEL_BORDER },
        )?;
        let masked = "*".repeat(pw.chars().count());
        text(&mut canvas, &masked, 206.0, 208.0, C_TEXT)?;
        if active == 1 && blink {
            fill(&mut canvas, 206.0 + masked.chars().count() as f32 * 8.0, 206.0, 8.0, 12.0, C_ACTIVE)?;
        }

        // 按钮
        fill(&mut canvas, 200.0, 258.0, 140.0, 28.0, C_BTN)?;
        frame(&mut canvas, 200.0, 258.0, 140.0, 28.0, C_BTN_BORDER)?;
        text(&mut canvas, "LOGIN", 200.0 + (140.0 - 5.0 * 8.0) / 2.0, 268.0, C_ACTIVE)?;

        fill(&mut canvas, 372.0, 258.0, 140.0, 28.0, C_BTN)?;
        frame(&mut canvas, 372.0, 258.0, 140.0, 28.0, C_BTN_BORDER)?;
        text(&mut canvas, "EXIT (ESC)", 372.0 + (140.0 - 10.0 * 8.0) / 2.0, 268.0, C_TEXT)?;

        // 提示 / 状态
        let hint = "TAB SWITCH   ENTER LOGIN   M MUSIC   ESC QUIT";
        text(&mut canvas, hint, center_x(hint), 344.0, C_DIM)?;
        text(&mut canvas, &format!("STATUS: {status}"), 96.0, 372.0, C_TEXT)?;

        // 音乐指示 + 运行时长
        let mus = format!("MUSIC: {}", if music_on { "ON" } else { "OFF" });
        text(&mut canvas, &mus, 96.0, 400.0, if music_on { C_ACTIVE } else { C_DIM })?;
        let up = format!("UPTIME: {:.1}s", started.elapsed().as_secs_f32());
        text(&mut canvas, &up, 400.0, 400.0, C_DIM)?;

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出登录界面");
    Ok(())
}

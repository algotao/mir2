//! SDL3 音频引擎：**软件混音** + 循环 BGM。
//!
//! 为什么自己混音：原版是"每个音效 `new` 一个 DirectSound buffer 叠着播"
//! （`DXSounds.pas:1852-1881`）—— 也就是**天然多路叠加、无固定通道数**。
//! SDL3 的流回调给我们的是一块"要填满的缓冲"，所以叠加这件事得自己做：
//! 把每条正在响的音效按自己的进度**加**进同一块缓冲。
//!
//! 规格（哪条编号是什么音）在 `mir2_core::sound`（纯数据、可单测）；
//! 这里只管"把 f32 采样混出来"与"设备开关"。
//!
//! 原版**没有**的，我们也不加：音量滑条、距离衰减、左右声道 pan。

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use sdl3::audio::{
    AudioCallback, AudioFormat, AudioSpec, AudioSpecWAV, AudioStream, AudioStreamWithCallback,
};

/// 设备采样率（与原来的测试音一致）。
pub const SAMPLE_RATE: i32 = 44_100;
/// 我们固定输出**立体声交错** f32（原版 DirectSound 也是立体声缓冲）。
const CHANNELS: usize = 2;

/// 同时最多几路音效。
///
/// 原版从不设上限（每个音效一个 buffer）；软件混音总得有条防火墙，
/// 超了就丢**最旧**的一条（听感上最不容易被察觉）。
const MAX_SFX: usize = 24;

/// 同一条音效在多少毫秒内不重复触发。
///
/// ⚠️ 原版**没有**这个（它的去重是**动作级**的 `m_boRunSound`，
/// `Actor.pas:2357-2359`，一招只响一次）。但我们的触发点是"每帧看一次世界状态"，
/// 少了它，一次按键重复或几帧内的状态抖动就会叠出七八声同一个音。
const SAME_SFX_MS: u64 = 30;

/// 一段已解码的音频：**交错立体声**、已重采样到 [`SAMPLE_RATE`]。
type Clip = Arc<Vec<f32>>;

// ---------- 采样格式 → f32 ----------

/// 把 wav 的原始字节按它的格式摊成 f32（`-1.0..=1.0`）。
///
/// 支持的格式就是 SDL 给的 `AudioFormat` 那些；`UNKNOWN` 或长度不整 ⇒ `None`
/// （**不猜**：宁可没这声，也不放出杂音）。
fn samples_of(format: AudioFormat, bytes: &[u8]) -> Option<Vec<f32>> {
    match format {
        AudioFormat::U8 => Some(bytes.iter().map(|b| (*b as f32 - 128.0) / 128.0).collect()),
        AudioFormat::S8 => Some(bytes.iter().map(|b| (*b as i8) as f32 / 128.0).collect()),
        // `as_chunks::<N>()` 直接从 `&[u8]` 得到 `&[[u8; N]]`（尾巴不足 N 的丢掉，
        // 与 `chunks_exact` 同义）—— clippy 也要求用这个写法。
        AudioFormat::S16LE => Some(
            bytes
                .as_chunks::<2>()
                .0
                .iter()
                .map(|c| i16::from_le_bytes(*c) as f32 / 32768.0)
                .collect(),
        ),
        AudioFormat::S16BE => Some(
            bytes
                .as_chunks::<2>()
                .0
                .iter()
                .map(|c| i16::from_be_bytes(*c) as f32 / 32768.0)
                .collect(),
        ),
        AudioFormat::S32LE => Some(
            bytes
                .as_chunks::<4>()
                .0
                .iter()
                .map(|c| i32::from_le_bytes(*c) as f32 / 2_147_483_648.0)
                .collect(),
        ),
        AudioFormat::S32BE => Some(
            bytes
                .as_chunks::<4>()
                .0
                .iter()
                .map(|c| i32::from_be_bytes(*c) as f32 / 2_147_483_648.0)
                .collect(),
        ),
        AudioFormat::F32LE => Some(
            bytes
                .as_chunks::<4>()
                .0
                .iter()
                .map(|c| f32::from_le_bytes(*c))
                .collect(),
        ),
        AudioFormat::F32BE => Some(
            bytes
                .as_chunks::<4>()
                .0
                .iter()
                .map(|c| f32::from_be_bytes(*c))
                .collect(),
        ),
        AudioFormat::UNKNOWN => None,
    }
}

/// 重采样成 `SAMPLE_RATE` 的**交错立体声**（单声道复制成两声道）。
///
/// 线性插值就够了：原版这些 wav 本来就是 22k/44k 的短音效，且原版是**直接交给
/// 声卡**（DirectSound 自己转）—— 我们只是把这一步挪到自己手上，别指望高保真。
/// 采样率相同、声道数相同的情况走的是"原样拷贝"（`ratio = 1`、`frac = 0`）。
fn to_stereo_44k(src: &[f32], channels: usize, rate: i32) -> Vec<f32> {
    if channels == 0 || src.is_empty() || rate <= 0 {
        return Vec::new();
    }
    let frames = src.len() / channels;
    if frames == 0 {
        return Vec::new();
    }
    let ratio = rate as f64 / SAMPLE_RATE as f64; // 源帧 / 目标帧
    let out_frames = ((frames as f64) / ratio).round().max(1.0) as usize;
    let mut out = Vec::with_capacity(out_frames * CHANNELS);
    let at = |i: usize, ch: usize| -> f32 {
        let c = if channels == 1 {
            0
        } else {
            ch.min(channels - 1)
        };
        src.get(i * channels + c).copied().unwrap_or(0.0)
    };
    for j in 0..out_frames {
        let pos = j as f64 * ratio;
        let i0 = pos.floor() as usize;
        let frac = (pos - i0 as f64) as f32;
        let i1 = (i0 + 1).min(frames - 1);
        for ch in 0..CHANNELS {
            let a = at(i0, ch);
            let b = at(i1, ch);
            out.push(a + (b - a) * frac);
        }
    }
    out
}

/// 读一个 wav 成 [`Clip`]。读不动（缺文件 / 格式不认）⇒ `None`。
fn decode_wav(path: &Path) -> Option<Clip> {
    let wav = AudioSpecWAV::load_wav(path).ok()?;
    let flat = samples_of(wav.format, wav.buffer())?;
    let stereo = to_stereo_44k(&flat, wav.channels as usize, wav.freq);
    (!stereo.is_empty()).then(|| Arc::new(stereo))
}

// ---------- 混音 ----------

/// 一条正在响的声音。
struct Voice {
    clip: Clip,
    /// 读到第几**帧**（一帧 = 两个 f32）。
    frame: usize,
    /// 播完从头再来（BGM 是 `true`，音效是 `false`）。
    looped: bool,
    gain: f32,
}

impl Voice {
    fn frames(&self) -> usize {
        self.clip.len() / CHANNELS
    }

    fn finished(&self) -> bool {
        !self.looped && self.frame >= self.frames()
    }

    /// 把这条声音**加**进输出缓冲（`out` 是交错立体声）。`bus_gain` = 该组开关。
    fn mix_into(&mut self, out: &mut [f32], bus_gain: f32) {
        let total = self.frames();
        if total == 0 {
            self.frame = 0;
            return;
        }
        let frames_out = out.len() / CHANNELS;
        let g = self.gain * bus_gain;
        for i in 0..frames_out {
            let f = if self.looped {
                self.frame % total
            } else if self.frame < total {
                self.frame
            } else {
                break;
            };
            let (l, r) = (self.clip[f * CHANNELS], self.clip[f * CHANNELS + 1]);
            out[i * CHANNELS] += l * g;
            out[i * CHANNELS + 1] += r * g;
            self.frame += 1;
            if self.looped && self.frame >= total {
                self.frame = 0;
            }
        }
    }
}

/// 混音器的全部状态（回调线程与主线程共享）。
struct Inner {
    /// 解码缓存：`None` = 试过、读不动（别每次播放都去读一遍磁盘）。
    cache: HashMap<PathBuf, Option<Clip>>,
    sfx: Vec<Voice>,
    music: Option<Voice>,
    /// 当前 BGM 的文件（判断"要不要换一首"；同一首不重开）。
    bgm_path: Option<PathBuf>,
    /// 同一条音效的上次播放时刻（见 [`SAME_SFX_MS`]）。
    last_played: HashMap<PathBuf, Instant>,
    music_on: bool,
    sfx_on: bool,
    /// 统计：播过多少次、丢了几个（调试用，终端里打一眼就知道有没有在响）。
    played: u64,
    dropped: u64,
}

impl Inner {
    fn new(music_on: bool, sfx_on: bool) -> Self {
        Self {
            cache: HashMap::new(),
            sfx: Vec::new(),
            music: None,
            bgm_path: None,
            last_played: HashMap::new(),
            music_on,
            sfx_on,
            played: 0,
            dropped: 0,
        }
    }

    fn clip(&mut self, path: &Path) -> Option<Clip> {
        if let Some(c) = self.cache.get(path) {
            return c.clone();
        }
        let c = decode_wav(path);
        self.cache.insert(path.to_path_buf(), c.clone());
        c
    }

    fn play_sfx(&mut self, path: &Path, gain: f32) {
        // 节流：同一条音效紧接着重复触发就不放（见 `SAME_SFX_MS` 的说明）。
        let now = Instant::now();
        if let Some(t) = self.last_played.get(path) {
            if now.duration_since(*t) < Duration::from_millis(SAME_SFX_MS) {
                return;
            }
        }
        let Some(clip) = self.clip(path) else {
            return;
        };
        self.last_played.insert(path.to_path_buf(), now);
        if self.sfx.len() >= MAX_SFX {
            self.sfx.remove(0);
            self.dropped += 1;
        }
        self.sfx.push(Voice {
            clip,
            frame: 0,
            looped: false,
            gain,
        });
        self.played += 1;
    }

    /// 返回"是不是真的换了/开始播了"（同一首正在播 ⇒ `false`，缺文件 ⇒ `false`）。
    fn play_bgm(&mut self, path: &Path) -> bool {
        // 同一首要播着就别重开（调用方按场景每帧调都可能调到这里）
        if self.bgm_path.as_deref() == Some(path) && self.music.is_some() {
            return false;
        }
        let Some(clip) = self.clip(path) else {
            return false;
        };
        self.bgm_path = Some(path.to_path_buf());
        self.music = Some(Voice {
            clip,
            frame: 0,
            looped: true,
            gain: 1.0,
        });
        self.played += 1;
        true
    }

    /// 返回"是不是真的停掉了"（本来就没在放 ⇒ `false`）。调用方每帧调也不怕。
    fn stop_bgm(&mut self) -> bool {
        let had = self.music.is_some();
        self.music = None;
        self.bgm_path = None;
        had
    }

    fn mix(&mut self, out: &mut [f32]) {
        let music_gain = if self.music_on { 1.0 } else { 0.0 };
        let sfx_gain = if self.sfx_on { 1.0 } else { 0.0 };

        if let Some(v) = self.music.as_mut() {
            v.mix_into(out, music_gain);
            if v.finished() {
                self.music = None;
                self.bgm_path = None;
            }
        }
        // `retain_mut`：边混边清掉播完的（原版也是靠定时器回收 buffer，
        // `DXSounds.pas:1905-1915`）
        self.sfx.retain_mut(|v| {
            v.mix_into(out, sfx_gain);
            !v.finished()
        });

        // 多路叠加会越过 ±1（原版交给声卡饱和）；硬件要的是规范值，这里削顶。
        for s in out.iter_mut() {
            *s = s.clamp(-1.0, 1.0);
        }
    }
}

/// SDL 回调：SDL 的音频线程调它来"要数据"。
pub struct Mixer {
    inner: Arc<Mutex<Inner>>,
}

impl AudioCallback<f32> for Mixer {
    fn callback(&mut self, stream: &mut AudioStream, requested: i32) {
        // ⚠️ `requested` 是**样本值个数**（含声道），不是帧数：SDL3 的桥是
        // `len / size_of::<Channel>()`（sdl3-0.20 `audio.rs:1002`）。立体声 ⇒ 帧 = 一半。
        let n = requested.max(0) as usize;
        let mut out = vec![0.0f32; n];
        if let Ok(mut g) = self.inner.lock() {
            g.mix(&mut out);
        }
        let _ = stream.put_data_f32(&out);
    }
}

/// 主线程侧的门面：往混音器里塞声音、开关两组。
pub struct Audio {
    inner: Arc<Mutex<Inner>>,
}

/// 播放流的参数：44.1kHz、立体声、f32。
///
/// ⚠️ `sdl3::audio::AudioSubsystem` 是**私有类型**（与 `WindowContext` 同一类问题，
/// 见 `main.rs` 里对纹理创建器的注释）⇒ 本模块**不能**把这个类型写进签名，
/// 也就不能自己调 `open_playback_stream`。所以：参数由这里给，
/// **开流那一步由调用方用闭包补**（它才拿得到那个类型）。
pub fn spec() -> AudioSpec {
    AudioSpec {
        freq: Some(SAMPLE_RATE),
        channels: Some(CHANNELS as i32),
        format: Some(AudioFormat::F32LE),
    }
}

impl Audio {
    /// 开一条播放流：`open(mixer)` 由调用方写（`|m| sdl_audio.open_playback_stream(&audio::spec(), m)`）。
    ///
    /// ⚠️ 返回的 `AudioStreamWithCallback` **就是设备**：调用方必须把它活在手里，
    /// drop 掉就等于关声卡（静音）。
    pub fn open_with<F, E>(
        music_on: bool,
        sfx_on: bool,
        open: F,
    ) -> Result<(Audio, AudioStreamWithCallback<Mixer>), E>
    where
        F: FnOnce(Mixer) -> Result<AudioStreamWithCallback<Mixer>, E>,
    {
        let inner = Arc::new(Mutex::new(Inner::new(music_on, sfx_on)));
        let stream = open(Mixer {
            inner: inner.clone(),
        })?;
        // 设备默认是暂停的，要显式开跑（`resume` 失败不致命：没声卡时本来也没声）。
        let _ = stream.resume();
        Ok((Audio { inner }, stream))
    }

    /// 播一条音效（不循环）。返回"是否真的放上了"（缺文件 / 格式不认 ⇒ `false`）。
    pub fn play(&self, path: &Path) -> bool {
        match self.inner.lock() {
            Ok(mut g) => {
                let before = g.played;
                g.play_sfx(path, 1.0);
                g.played != before
            }
            Err(_) => false,
        }
    }

    /// 按原版编号播放（`sound.lst` 里查不到就什么都不做）。
    pub fn play_idx(&self, lib: &mir2_core::sound::Library, number: u16) -> bool {
        match lib.playable(number) {
            Some(p) => self.play(p),
            None => false,
        }
    }

    /// 切 BGM（循环）。同一首正在播就不重开。
    ///
    /// 返回"这一下**真的**开始播了" —— 调用方每帧调，只有变化时才是 `true`
    ///（听不见的时候，这是唯一能看出"到底有没有在放"的线索）。
    pub fn bgm(&self, path: &Path) -> bool {
        match self.inner.lock() {
            Ok(mut g) => g.play_bgm(path),
            Err(_) => false,
        }
    }

    /// 停 BGM。返回"这一下**真的**停掉了"。
    pub fn stop_bgm(&self) -> bool {
        match self.inner.lock() {
            Ok(mut g) => g.stop_bgm(),
            Err(_) => false,
        }
    }

    pub fn set_music_on(&self, on: bool) {
        if let Ok(mut g) = self.inner.lock() {
            g.music_on = on;
        }
    }

    pub fn set_sfx_on(&self, on: bool) {
        if let Ok(mut g) = self.inner.lock() {
            g.sfx_on = on;
        }
    }

    /// `(正在响的音效数, 有没有 BGM, 播过多少次, 丢过多少)` —— 调试与 e2e 断言用。
    pub fn stats(&self) -> (usize, bool, u64, u64) {
        match self.inner.lock() {
            Ok(g) => (g.sfx.len(), g.music.is_some(), g.played, g.dropped),
            Err(_) => (0, false, 0, 0),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 采样格式转换：三种典型格式的边界值。
    #[test]
    fn 采样格式() {
        // U8：128 是零点
        assert_eq!(
            samples_of(AudioFormat::U8, &[128, 255, 0]).unwrap(),
            vec![0.0, 0.9921875, -1.0]
        );
        // S8
        assert_eq!(
            samples_of(AudioFormat::S8, &[0, 127, 0x80]).unwrap(),
            vec![0.0, 127.0 / 128.0, -1.0]
        );
        // S16LE：0 与满量程
        let s16 = samples_of(AudioFormat::S16LE, &[0, 0, 0xFF, 0x7F, 0x00, 0x80]).unwrap();
        assert_eq!(s16[0], 0.0);
        assert!((s16[1] - 32767.0 / 32768.0).abs() < 1e-6);
        assert_eq!(s16[2], -1.0);
        // 不认的格式 ⇒ None（不猜、不放杂音）
        assert!(samples_of(AudioFormat::UNKNOWN, &[1, 2, 3, 4]).is_none());
    }

    /// 重采样：单声道 → 立体声、22k → 44k（帧数翻倍，且两声道相同）。
    #[test]
    fn 重采样与声道() {
        // 44100 单声道：原样复制到左右
        let src = [0.5f32, -0.5, 1.0];
        let out = to_stereo_44k(&src, 1, SAMPLE_RATE);
        assert_eq!(out, vec![0.5, 0.5, -0.5, -0.5, 1.0, 1.0]);

        // 22050 → 44100：帧数翻倍（线性插值的中点）
        let out = to_stereo_44k(&[0.0f32, 1.0], 1, 22_050);
        assert_eq!(out.len(), 4 * CHANNELS, "两帧 → 四帧、每帧两声道");
        assert_eq!(out[0], 0.0);
        assert!((out[2] - 0.5).abs() < 1e-6, "中点插值");
        assert!((out[4] - 1.0).abs() < 1e-6);

        // 立体声输入：左右不许串（左 1/0、右 0/1）
        let src = [1.0f32, 0.0, 0.0, 1.0];
        let out = to_stereo_44k(&src, 2, SAMPLE_RATE);
        assert_eq!(out, vec![1.0, 0.0, 0.0, 1.0]);

        // 空输入不炸
        assert!(to_stereo_44k(&[], 1, SAMPLE_RATE).is_empty());
        assert!(to_stereo_44k(&[1.0], 0, SAMPLE_RATE).is_empty());
    }

    /// 混音与两组开关：音效与音乐**互不牵连** —— 原版就是两个独立开关
    /// （`MShare.pas:213-214`）。
    #[test]
    fn 混音与开关() {
        let mut g = Inner::new(true, true);
        let clip: Clip = Arc::new(vec![0.5f32; 16 * CHANNELS]); // 16 帧的常数
        g.sfx.push(Voice {
            clip: clip.clone(),
            frame: 0,
            looped: false,
            gain: 1.0,
        });
        g.music = Some(Voice {
            clip: clip.clone(),
            frame: 0,
            looped: true,
            gain: 1.0,
        });

        // 两路同时响 ⇒ 1.0（0.5 + 0.5，刚好不削顶）
        let mut out = vec![0.0f32; 8 * CHANNELS];
        g.mix(&mut out);
        assert!(out.iter().all(|s| (*s - 1.0).abs() < 1e-6), "叠加：{out:?}");

        // 关音效 ⇒ 只剩音乐那一路
        let mut out2 = vec![0.0f32; 4 * CHANNELS];
        g.sfx_on = false;
        g.mix(&mut out2);
        assert!(
            out2.iter().all(|s| (*s - 0.5).abs() < 1e-6),
            "只关音效：{out2:?}"
        );

        // 关音乐 ⇒ 静音（音乐那路还在，但不混）
        g.music_on = false;
        let mut out3 = vec![0.0f32; 4 * CHANNELS];
        g.mix(&mut out3);
        assert!(out3.iter().all(|s| *s == 0.0), "两组都关就是静音");

        // 削顶：三路 0.5 叠起来 = 1.5 ⇒ 夹到 1.0
        g.sfx_on = true;
        g.music_on = true;
        for _ in 0..2 {
            g.sfx.push(Voice {
                clip: clip.clone(),
                frame: 0,
                looped: false,
                gain: 1.0,
            });
        }
        let mut out4 = vec![0.0f32; 4 * CHANNELS];
        g.mix(&mut out4);
        assert!(out4.iter().all(|s| *s <= 1.0 && *s > 0.0), "削顶：{out4:?}");
    }

    /// 播完的自动回收、循环的自动绕回。
    #[test]
    fn 播完回收与循环() {
        let mut g = Inner::new(true, true);
        g.sfx.push(Voice {
            clip: Arc::new(vec![0.25f32; 2 * CHANNELS]), // 只有 2 帧
            frame: 0,
            looped: false,
            gain: 1.0,
        });
        let mut out = vec![0.0f32; 8 * CHANNELS];
        g.mix(&mut out);
        assert!(g.sfx.is_empty(), "一次性的播完就该回收");

        let mut g = Inner::new(true, true);
        g.music = Some(Voice {
            clip: Arc::new(vec![0.25f32; 2 * CHANNELS]),
            frame: 0,
            looped: true,
            gain: 1.0,
        });
        let mut out = vec![0.0f32; 8 * CHANNELS];
        g.mix(&mut out);
        assert!(g.music.is_some(), "循环的不该被回收");
        assert!(
            out.iter().all(|s| (*s - 0.25).abs() < 1e-6),
            "绕回后照样有声音：{out:?}"
        );
    }

    /// 节流：同一条音效紧接着重复触发只放一次（见 `SAME_SFX_MS`）。
    #[test]
    fn 同一音效不叠加() {
        let dir = std::env::temp_dir().join("mir2-audio-节流");
        let _ = std::fs::create_dir_all(&dir);
        // 造一个 44.1k 单声道 16 位的小 wav（8 帧常数 0.5）
        let path = dir.join("t.wav");
        let mut wav = Vec::new();
        wav.extend_from_slice(b"RIFF");
        let data: Vec<u8> = (0..8).flat_map(|_| 16384i16.to_le_bytes()).collect();
        wav.extend_from_slice(&((36 + data.len()) as u32).to_le_bytes());
        wav.extend_from_slice(b"WAVEfmt ");
        wav.extend_from_slice(&16u32.to_le_bytes());
        wav.extend_from_slice(&1u16.to_le_bytes()); // PCM
        wav.extend_from_slice(&1u16.to_le_bytes()); // 单声道
        wav.extend_from_slice(&(SAMPLE_RATE as u32).to_le_bytes());
        wav.extend_from_slice(&(SAMPLE_RATE as u32 * 2).to_le_bytes()); // byte rate
        wav.extend_from_slice(&2u16.to_le_bytes()); // block align
        wav.extend_from_slice(&16u16.to_le_bytes()); // bits
        wav.extend_from_slice(b"data");
        wav.extend_from_slice(&(data.len() as u32).to_le_bytes());
        wav.extend_from_slice(&data);
        std::fs::write(&path, &wav).unwrap();

        let mut g = Inner::new(true, true);
        g.play_sfx(&path, 1.0);
        assert_eq!(g.sfx.len(), 1);
        g.play_sfx(&path, 1.0);
        assert_eq!(g.sfx.len(), 1, "30ms 内的第二次不该再叠一条");
        assert_eq!(g.played, 1);
        // 上限：塞满之后丢最旧的（⚠️ 每轮要清掉节流表，否则全被 30ms 那条挡住，
        // 测的就不是上限而是节流了 —— 第一版就是这么写错的）
        for _ in 0..MAX_SFX + 3 {
            g.last_played.clear();
            g.play_sfx(&path, 1.0);
        }
        assert!(g.sfx.len() <= MAX_SFX);
        assert!(g.dropped > 0, "超上限要丢最旧的");
    }

    /// **真素材**：读我们自己的 `103.wav`（UI 按钮声）—— 证明"文件 → 采样"这条路通，
    /// 不需要声卡。门控同 core：设了 `MIR2C_DATA` 才跑。
    #[test]
    fn 真素材_能解码按钮声() {
        let Some(dir) = mir2_core::paths::audio_dir() else {
            eprintln!("跳过：没找到音频目录");
            return;
        };
        let lib = mir2_core::sound::Library::load(&dir).expect("该有 sound.lst");
        let path = lib
            .playable(mir2_core::sound::idx::NORM_BUTTON_CLICK)
            .expect("103.wav 该在");
        let clip = decode_wav(path).expect("该能解码");
        assert!(clip.len() >= 2 * CHANNELS, "至少一帧");
        let peak = clip.iter().fold(0.0f32, |m, s| m.max(s.abs()));
        assert!(peak > 0.02, "整段全静音不正常（峰值 {peak}）");

        // BGM 也能解（登录那首），且长度是"一首歌"的量级
        let bgm = decode_wav(&dir.join(mir2_core::sound::BGM_LOGIN)).expect("该能解码 BGM");
        let secs = (bgm.len() / CHANNELS) as f32 / SAMPLE_RATE as f32;
        assert!(secs > 5.0, "登录 BGM 该有几十秒，实测 {secs:.1}s");
    }
}

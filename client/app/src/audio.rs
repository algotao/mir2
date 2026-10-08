//! SDL3 音频引擎：**软件混音** + 循环 BGM。
//!
//! 分工（三块刻意分开）：
//!
//! | 层 | 在哪 | 管什么 |
//! |---|---|---|
//! | 规格 | `mir2_core::sound` | 编号表、地形→脚步、资产从哪取（容器/目录）|
//! | 解码 | `mir2_core::wave` | WAV 字节 → PCM（PCM 8/16bit + **IMA ADPCM**，纯逻辑可测）|
//! | 发声 | 本模块 | SDL3 设备 + 软件混音（多路叠加、循环、两组开关）|
//!
//! 为什么自己混音：原版是"每个音效 `new` 一个 DirectSound buffer 叠着播"
//! （`DXSounds.pas:1852-1881`）—— 天然多路叠加、无固定通道数。SDL3 的流回调
//! 给我们的是一块"要填满的缓冲"，叠加就得自己做：把每条正在响的音效按自己的
//! 进度**加**进同一块缓冲。
//!
//! 原版**没有**的，我们也不加：音量滑条、距离衰减、左右声道 pan（只有两个开关，
//! `MShare.pas:213-214`）。

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use mir2_core::sound::SoundAssets;
use sdl3::audio::{AudioCallback, AudioFormat, AudioSpec, AudioStream, AudioStreamWithCallback};

/// 设备采样率。
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
/// ⚠️ 原版**没有**这个（它的去重是**动作级**的 `m_boRunSound`，`Actor.pas:2357-2359`，
/// 一招只响一次）。但我们的触发点是"每帧看一次世界状态"，少了它，一次按键重复
/// 或几帧内的状态抖动就会叠出七八声同一个音。
const SAME_SFX_MS: u64 = 30;

/// 一段已解码的音频：**交错立体声**、已重采样到 [`SAMPLE_RATE`]。
type Clip = Arc<Vec<f32>>;

/// 重采样成 `SAMPLE_RATE` 的**交错立体声**（单声道复制成两声道）。
///
/// 线性插值就够了：原版这些本来就是 22k/44k 的短音效，且原版是**直接交给声卡**
/// （DirectSound 自己转）—— 我们只是把这一步挪到自己手上，别指望高保真。
/// 采样率与声道数都相同的情况走的是"原样拷贝"（`ratio = 1`、`frac = 0`）。
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

/// 把一份资产字节（容器里取出来的 WAV：PCM 或 IMA ADPCM）解成 [`Clip`]。
///
/// 读不动（坏数据 / 不认的编码）⇒ `None`：**宁可没这声，也不放杂音**。
fn decode_asset(bytes: &[u8]) -> Option<Clip> {
    let pcm = mir2_core::wave::decode(bytes).ok()?;
    let flat: Vec<f32> = pcm.samples.iter().map(|s| *s as f32 / 32768.0).collect();
    let stereo = to_stereo_44k(&flat, pcm.channels as usize, pcm.rate as i32);
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
    /// 解码缓存：键是**资产键**（`sound::asset_key`），`None` = 试过、读不动。
    cache: HashMap<String, Option<Clip>>,
    sfx: Vec<Voice>,
    music: Option<Voice>,
    /// 当前 BGM 的键（判断"要不要换一首"；同一首不重开）。
    bgm_key: Option<String>,
    /// 同一条音效的上次播放时刻（见 [`SAME_SFX_MS`]）。
    last_played: HashMap<String, Instant>,
    music_on: bool,
    sfx_on: bool,
    /// 统计：播过多少次、丢了几个。
    played: u64,
    dropped: u64,
}

impl Inner {
    fn new(music_on: bool, sfx_on: bool) -> Self {
        Self {
            cache: HashMap::new(),
            sfx: Vec::new(),
            music: None,
            bgm_key: None,
            last_played: HashMap::new(),
            music_on,
            sfx_on,
            played: 0,
            dropped: 0,
        }
    }

    fn clip(&mut self, key: &str, bytes: &[u8]) -> Option<Clip> {
        if let Some(c) = self.cache.get(key) {
            return c.clone();
        }
        let c = decode_asset(bytes);
        self.cache.insert(key.to_string(), c.clone());
        c
    }

    fn play_sfx(&mut self, key: &str, bytes: &[u8], gain: f32) {
        // 节流：同一条音效紧接着重复触发就不放（见 `SAME_SFX_MS` 的说明）。
        let now = Instant::now();
        if let Some(t) = self.last_played.get(key) {
            if now.duration_since(*t) < Duration::from_millis(SAME_SFX_MS) {
                return;
            }
        }
        let Some(clip) = self.clip(key, bytes) else {
            return;
        };
        self.last_played.insert(key.to_string(), now);
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

    /// 返回"是不是真的换了/开始播了"（同一首正在播 ⇒ `false`，解不开 ⇒ `false`）。
    fn play_bgm(&mut self, key: &str, bytes: &[u8]) -> bool {
        if self.bgm_key.as_deref() == Some(key) && self.music.is_some() {
            return false;
        }
        let Some(clip) = self.clip(key, bytes) else {
            return false;
        };
        self.bgm_key = Some(key.to_string());
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
        self.bgm_key = None;
        had
    }

    fn mix(&mut self, out: &mut [f32]) {
        let music_gain = if self.music_on { 1.0 } else { 0.0 };
        let sfx_gain = if self.sfx_on { 1.0 } else { 0.0 };

        if let Some(v) = self.music.as_mut() {
            v.mix_into(out, music_gain);
            if v.finished() {
                self.music = None;
                self.bgm_key = None;
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

/// 主线程侧的门面：往混音器里塞声音、开关两组。
pub struct Audio {
    inner: Arc<Mutex<Inner>>,
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

    /// 播一条音效（不循环）。`key` = 资产键（做缓存与去重），`bytes` = 它的字节。
    /// 返回"是否真的放上了"（解不开 / 被节流 ⇒ `false`）。
    pub fn play_bytes(&self, key: &str, bytes: &[u8]) -> bool {
        match self.inner.lock() {
            Ok(mut g) => {
                let before = g.played;
                g.play_sfx(key, bytes, 1.0);
                g.played != before
            }
            Err(_) => false,
        }
    }

    /// 按原版编号播放：从资产包里取字节（取不到就什么都不做）。
    pub fn play_idx(&self, assets: &SoundAssets, number: u16) -> bool {
        match assets.bytes(number) {
            Some((key, bytes)) => self.play_bytes(&key, &bytes),
            None => false,
        }
    }

    /// 切 BGM（循环）。同一首正在播就不重开。
    ///
    /// 返回"这一下**真的**开始播了" —— 调用方每帧调，只有变化时才是 `true`
    ///（听不见的时候，这是唯一能看出"到底有没有在放"的线索）。
    pub fn bgm_bytes(&self, key: &str, bytes: &[u8]) -> bool {
        match self.inner.lock() {
            Ok(mut g) => g.play_bgm(key, bytes),
            Err(_) => false,
        }
    }

    /// 按**文件名**切 BGM（原版那三首是写死文件名的，不是编号）。
    pub fn bgm_name(&self, assets: &SoundAssets, name: &str) -> bool {
        match assets.bytes_name(name) {
            Some((key, bytes)) => self.bgm_bytes(&key, &bytes),
            None => false,
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

    /// `(正在响的音效数, 有没有 BGM, 播过多少次, 丢过多少)` —— 调试用。
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

    /// 节流：同一条音效紧接着重复触发只放一次（见 [`SAME_SFX_MS`]）。
    #[test]
    fn 同一音效不叠加() {
        // 造一个 44.1k 单声道 16 位的小 wav（8 帧常数 0.5）
        let wav = {
            let data: Vec<u8> = (0..8).flat_map(|_| 16384i16.to_le_bytes()).collect();
            let mut v = Vec::new();
            v.extend_from_slice(b"RIFF");
            v.extend_from_slice(&((36 + data.len()) as u32).to_le_bytes());
            v.extend_from_slice(b"WAVEfmt ");
            v.extend_from_slice(&16u32.to_le_bytes());
            v.extend_from_slice(&1u16.to_le_bytes()); // PCM
            v.extend_from_slice(&1u16.to_le_bytes()); // 单声道
            v.extend_from_slice(&(SAMPLE_RATE as u32).to_le_bytes());
            v.extend_from_slice(&(SAMPLE_RATE as u32 * 2).to_le_bytes());
            v.extend_from_slice(&2u16.to_le_bytes());
            v.extend_from_slice(&16u16.to_le_bytes());
            v.extend_from_slice(b"data");
            v.extend_from_slice(&(data.len() as u32).to_le_bytes());
            v.extend_from_slice(&data);
            v
        };

        let mut g = Inner::new(true, true);
        g.play_sfx("t", &wav, 1.0);
        assert_eq!(g.sfx.len(), 1);
        g.play_sfx("t", &wav, 1.0);
        assert_eq!(g.sfx.len(), 1, "30ms 内的第二次不该再叠一条");
        assert_eq!(g.played, 1);
        // 上限：塞满之后丢最旧的（⚠️ 每轮要清掉节流表，否则测的是节流不是上限）
        for _ in 0..MAX_SFX + 3 {
            g.last_played.clear();
            g.play_sfx("t", &wav, 1.0);
        }
        assert!(g.sfx.len() <= MAX_SFX);
        assert!(g.dropped > 0, "超上限要丢最旧的");
    }

    /// **真素材**：从资产包（容器或目录）取 `103.wav`（UI 按钮声）与登录 BGM，
    /// 走完整链路 —— **Go 写的容器 → Rust 解 ADPCM → 重采样 → 混音器**。
    /// 不需要声卡。没装资产就跳过。
    #[test]
    fn 真素材_能解码按钮声() {
        let Some(assets) = SoundAssets::open() else {
            eprintln!("跳过：没找到音频资产（先跑 tools/wavpack/build.sh）");
            return;
        };
        eprintln!("音频资产：{}", assets.describe());

        let (key, bytes) = assets
            .bytes(mir2_core::sound::idx::NORM_BUTTON_CLICK)
            .expect("103.wav 该在");
        let clip = decode_asset(&bytes).expect("该能解码");
        assert!(clip.len() >= 2 * CHANNELS, "至少一帧");
        let peak = clip.iter().fold(0.0f32, |m, s| m.max(s.abs()));
        assert!(peak > 0.02, "整段全静音不正常（峰值 {peak}）");
        assert_eq!(key, "103");

        // BGM 也能解（登录那首），且长度是"一首歌"的量级
        let (bgm_key, bgm) = assets
            .bytes_name(mir2_core::sound::BGM_LOGIN)
            .expect("该有登录 BGM");
        let clip = decode_asset(&bgm).expect("该能解码 BGM");
        let secs = (clip.len() / CHANNELS) as f32 / SAMPLE_RATE as f32;
        assert!(secs > 5.0, "登录 BGM 该有几十秒，实测 {secs:.1}s");
        assert_eq!(bgm_key, "log-in-long2");
    }
}

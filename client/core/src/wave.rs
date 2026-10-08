//! WAV 解码：**PCM（8/16bit）+ IMA ADPCM（4bit）** —— 纯逻辑，不碰声卡。
//!
//! # 为什么自己解
//!
//! 客户端的音频产物是**一个容器**（`assets/audio/sounds.m2pk`，见
//! [`crate::sound::SoundBank`]），里面的音效是 **IMA ADPCM 4bit**（体积 1/4，
//! 决策见 D-29），而 SDL3 自带的 WAV 加载器**只认 PCM** ⇒ 必须有一份自写解码器。
//!
//! 规格 = 微软 WAVE 的 `wFormatTag = 0x11`，结构与写侧（Go 的
//! `tools/wavpack/adpcm.go`）**严格镜像**：
//!
//! ```text
//! 每块 blockAlign 字节：4*声道数 字节块头（每声道 i16 predictor + u8 index + u8 保留）
//!                      后面是 nibble（低半字节先，立体声按 L,R,L,R… 交替）
//! samplesPerBlock = ((blockAlign - 4*channels) * 2 / channels) + 1
//! ```
//!
//! ⚠️ 最后一块会补零到块长，补的 0 nibble **不是静音**（它表示"升一点"）⇒
//! 帧数必须按 `fact` 段截断（这也是压缩格式必须写 `fact` 的原因）。
//!
//! 两边的正确性靠两件事守着：本模块的**手工向量**测试，以及构建期的
//! **解码回比 SNR**（`tools/wavpack` 的自检①）。

use std::io;

/// 解出来的 PCM。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Pcm {
    pub rate: u32,
    pub channels: u16,
    /// 交错样本（16bit 有符号）。
    pub samples: Vec<i16>,
}

impl Pcm {
    /// 帧数（= 每声道样本数）。
    pub fn frames(&self) -> usize {
        if self.channels == 0 {
            0
        } else {
            self.samples.len() / self.channels as usize
        }
    }

    /// 峰值（|样本| 最大）。全静音 ⇒ 0。
    pub fn peak(&self) -> i32 {
        self.samples
            .iter()
            .map(|s| (*s as i32).abs())
            .max()
            .unwrap_or(0)
    }
}

/// WAVE 的 IMA ADPCM 编码号。
const FORMAT_IMA: u16 = 0x11;

fn bad(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.into())
}

/// IMA 的步长索引调整表与步长表（与 Go 侧同一张表，标准值）。
const IMA_INDEX_TABLE: [i32; 16] = [-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8];

const IMA_STEP_TABLE: [i32; 89] = [
    7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45, 50, 55, 60, 66,
    73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230, 253, 279, 307, 337, 371, 408, 449,
    494, 544, 598, 658, 724, 796, 876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272,
    2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493,
    10442, 11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767,
];

/// 一个 nibble ⇒ 新预测器与新索引。**与 Go 侧 `imaReconstruct` 逐位一致**
/// （编码器也用同一个式子更新状态，否则两边会漂移）。
fn ima_step(predictor: i32, index: i32, nibble: u8) -> (i32, i32) {
    let step = IMA_STEP_TABLE[index as usize];
    let mut diff = step >> 3;
    if nibble & 1 != 0 {
        diff += step >> 2;
    }
    if nibble & 2 != 0 {
        diff += step >> 1;
    }
    if nibble & 4 != 0 {
        diff += step;
    }
    let mut pred = if nibble & 8 != 0 {
        predictor - diff
    } else {
        predictor + diff
    };
    pred = pred.clamp(-32768, 32767);
    let idx = (index + IMA_INDEX_TABLE[nibble as usize]).clamp(0, 88);
    (pred, idx)
}

/// 解一个 IMA ADPCM 的 `data` 段（不含 WAV 头）。
///
/// `block_align` / `samples_per_block` 来自 `fmt` 段（缺省时按标准推导）。
/// 返回交错样本；**最后一块的补零要多解出来**，调用方按 `fact` 截断。
fn decode_ima(
    data: &[u8],
    channels: usize,
    block_align: usize,
    samples_per_block: usize,
) -> Vec<i16> {
    if channels == 0 || block_align <= 4 * channels {
        return Vec::new();
    }
    let per_block = if samples_per_block > 0 {
        samples_per_block
    } else {
        (block_align - 4 * channels) * 2 / channels + 1
    };
    let mut out: Vec<i16> = Vec::with_capacity(data.len() * 2 / channels.max(1) + per_block);
    let mut off = 0usize;
    while off + block_align <= data.len() {
        let block = &data[off..off + block_align];
        off += block_align;

        let mut pred = vec![0i32; channels];
        let mut idx = vec![0i32; channels];
        for c in 0..channels {
            let b = 4 * c;
            pred[c] = i16::from_le_bytes([block[b], block[b + 1]]) as i32;
            idx[c] = (block[b + 2] as i32).min(88);
            out.push(pred[c] as i16); // 块头那个样本本身不计 nibble
        }

        let nibbles = &block[4 * channels..];
        let mut produced = 1usize; // 本块已产出的帧数（含块头那一个）
        let mut ch = 0usize;
        'outer: for &byte in nibbles {
            for nib in [byte & 0x0F, byte >> 4] {
                if produced >= per_block {
                    break 'outer;
                }
                let (p, i) = ima_step(pred[ch], idx[ch], nib);
                pred[ch] = p;
                idx[ch] = i;
                out.push(p as i16);
                ch += 1;
                if ch == channels {
                    ch = 0;
                    produced += 1;
                }
            }
        }
    }
    out
}

/// 解一份 wav 字节（PCM 或 IMA ADPCM）。
pub fn decode(bytes: &[u8]) -> io::Result<Pcm> {
    if bytes.len() < 12 || &bytes[0..4] != b"RIFF" || &bytes[8..12] != b"WAVE" {
        return Err(bad("不是 RIFF/WAVE"));
    }
    let mut format: Option<(u16, u16, u32, u16, usize, usize)> = None; // tag,ch,rate,bits,ba,spb
    let mut data: Option<&[u8]> = None;
    let mut fact: Option<usize> = None;

    let mut off = 12usize;
    while off + 8 <= bytes.len() {
        let id = &bytes[off..off + 4];
        let size = u32::from_le_bytes([
            bytes[off + 4],
            bytes[off + 5],
            bytes[off + 6],
            bytes[off + 7],
        ]) as usize;
        let body = off + 8;
        let end = (body + size).min(bytes.len());
        match id {
            b"fmt " => {
                if size < 16 {
                    return Err(bad("fmt 段太小"));
                }
                let tag = u16::from_le_bytes([bytes[body], bytes[body + 1]]);
                let ch = u16::from_le_bytes([bytes[body + 2], bytes[body + 3]]);
                let rate = u32::from_le_bytes([
                    bytes[body + 4],
                    bytes[body + 5],
                    bytes[body + 6],
                    bytes[body + 7],
                ]);
                let ba = u16::from_le_bytes([bytes[body + 12], bytes[body + 13]]) as usize;
                let bits = u16::from_le_bytes([bytes[body + 14], bytes[body + 15]]);
                // IMA 的 `wSamplesPerBlock` 在 cbSize 之后（fmt 段 ≥ 20 字节时才有）
                let spb = if size >= 20 {
                    u16::from_le_bytes([bytes[body + 18], bytes[body + 19]]) as usize
                } else {
                    0
                };
                format = Some((tag, ch, rate, bits, ba, spb));
            }
            b"fact" => {
                if size >= 4 {
                    fact = Some(u32::from_le_bytes([
                        bytes[body],
                        bytes[body + 1],
                        bytes[body + 2],
                        bytes[body + 3],
                    ]) as usize);
                }
            }
            b"data" => data = Some(&bytes[body..end]),
            _ => {}
        }
        off = body + size + (size & 1);
    }

    let (tag, ch, rate, bits, ba, spb) = format.ok_or_else(|| bad("缺 fmt 段"))?;
    let data = data.ok_or_else(|| bad("缺 data 段"))?;
    if ch == 0 {
        return Err(bad("声道数为 0"));
    }
    let channels = ch as usize;

    let samples: Vec<i16> = match tag {
        1 => match bits {
            16 => data
                .as_chunks::<2>()
                .0
                .iter()
                .map(|c| i16::from_le_bytes(*c))
                .collect(),
            // 8bit PCM 是**无符号**的（128 = 静音）；与写侧同一缩放（<<8）
            8 => data
                .iter()
                .map(|v| ((*v as i32 - 128) << 8) as i16)
                .collect(),
            other => return Err(bad(format!("不支持的 PCM 位深 {other}"))),
        },
        FORMAT_IMA => decode_ima(data, channels, ba, spb),
        other => return Err(bad(format!("不支持的编码 0x{other:X}"))),
    };

    // `fact` 才是真实帧数（最后一块的补零不算）
    let samples = match fact {
        Some(f) if f > 0 && f * channels <= samples.len() => samples[..f * channels].to_vec(),
        _ => samples,
    };

    Ok(Pcm {
        rate,
        channels: ch,
        samples,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 拼一个最小的 WAV（PCM 或 IMA）。
    ///
    /// 参数确实多（各段长度/编码都要能独立摆），但它是**测试专用**的拼装器：
    /// 拆成结构体反而更绕，这里明说允许。
    #[allow(clippy::too_many_arguments)]
    fn wav(
        tag: u16,
        ch: u16,
        rate: u32,
        bits: u16,
        ba: u16,
        spb: u16,
        fmt_extra: bool,
        fact: Option<u32>,
        data: &[u8],
    ) -> Vec<u8> {
        let fmt_len = if fmt_extra { 20 } else { 16 };
        let fact_len = if fact.is_some() { 12 } else { 0 };
        let mut v = Vec::new();
        v.extend_from_slice(b"RIFF");
        v.extend_from_slice(&((4 + 8 + fmt_len + fact_len + 8 + data.len()) as u32).to_le_bytes());
        v.extend_from_slice(b"WAVE");
        v.extend_from_slice(b"fmt ");
        v.extend_from_slice(&(fmt_len as u32).to_le_bytes());
        v.extend_from_slice(&tag.to_le_bytes());
        v.extend_from_slice(&ch.to_le_bytes());
        v.extend_from_slice(&rate.to_le_bytes());
        v.extend_from_slice(&(rate * ch as u32 * 2).to_le_bytes());
        v.extend_from_slice(&ba.to_le_bytes());
        v.extend_from_slice(&bits.to_le_bytes());
        if fmt_extra {
            v.extend_from_slice(&2u16.to_le_bytes()); // cbSize
            v.extend_from_slice(&spb.to_le_bytes());
        }
        if let Some(f) = fact {
            v.extend_from_slice(b"fact");
            v.extend_from_slice(&4u32.to_le_bytes());
            v.extend_from_slice(&f.to_le_bytes());
        }
        v.extend_from_slice(b"data");
        v.extend_from_slice(&(data.len() as u32).to_le_bytes());
        v.extend_from_slice(data);
        v
    }

    /// 16bit PCM：立体声交错原样取出。
    #[test]
    fn pcm16() {
        let data = [1i16, -2, 3, -4]
            .iter()
            .flat_map(|s| s.to_le_bytes())
            .collect::<Vec<_>>();
        let p = decode(&wav(1, 2, 22050, 16, 4, 0, false, None, &data)).unwrap();
        assert_eq!(p.channels, 2);
        assert_eq!(p.rate, 22050);
        assert_eq!(p.samples, vec![1, -2, 3, -4]);
        assert_eq!(p.frames(), 2);
        assert_eq!(p.peak(), 4);
    }

    /// 8bit PCM 是**无符号**的：128 = 静音（与写侧同一缩放）。
    #[test]
    fn pcm8() {
        let p = decode(&wav(1, 1, 11025, 8, 1, 0, false, None, &[128, 255, 0])).unwrap();
        assert_eq!(p.samples, vec![0, 32512, -32768]);
    }

    /// IMA ADPCM：**手工向量**。
    ///
    /// 块头 `predictor = 1000`、`index = 0`（⇒ step = 7），nibble 依次 0、4：
    ///   nibble 0 ⇒ diff = 7>>3 = 0 ⇒ 1000（index 落回 0）
    ///   nibble 4 ⇒ diff = 0 + 7 = 7 ⇒ 1007（index = 0+2 = 2）
    /// `fact = 3` ⇒ 只取 3 帧（最后一块补的零 nibble 不算）。
    #[test]
    fn ima_adpcm_手工向量() {
        let mut block = vec![0u8; 256]; // blockAlign = 256
        block[0..2].copy_from_slice(&1000i16.to_le_bytes());
        block[2] = 0; // index
        block[3] = 0; // 保留
        block[4] = 0x40; // 低半字节 0、高半字节 4
        let p = decode(&wav(
            FORMAT_IMA,
            1,
            22050,
            4,
            256,
            505,
            true,
            Some(3),
            &block,
        ))
        .unwrap();
        assert_eq!(p.frames(), 3, "fact=3 ⇒ 三帧");
        assert_eq!(p.samples, vec![1000, 1000, 1007]);
        assert_eq!(p.rate, 22050);
    }

    /// 立体声 IMA：nibble 按 L,R 交替（第二帧起是编码出来的）。
    #[test]
    fn ima_adpcm_立体声交错() {
        let mut block = vec![0u8; 512]; // 立体声 blockAlign = 512
        block[0..2].copy_from_slice(&100i16.to_le_bytes()); // L predictor
        block[4..6].copy_from_slice(&200i16.to_le_bytes()); // R predictor
        block[8] = 0x00; // L nibble = 0, R nibble = 0（各不动）
        let p = decode(&wav(FORMAT_IMA, 2, 22050, 4, 512, 505, true, None, &block)).unwrap();
        assert_eq!(p.channels, 2);
        assert_eq!(&p.samples[0..2], &[100, 200], "块头两声道各一个样本");
        assert_eq!(&p.samples[2..4], &[100, 200], "nibble 0 不改变预测器");
    }

    /// 坏输入要说得出话，而不是崩或者悄悄给空。
    #[test]
    fn 坏输入() {
        assert!(decode(b"").is_err());
        assert!(decode(b"RIFFxxxxWAVE").is_err(), "没有 fmt/data");
        // 不认识的编码
        let e = decode(&wav(0x55, 1, 22050, 16, 2, 0, false, None, &[0, 0]));
        assert!(e.unwrap_err().to_string().contains("不支持的编码"));
        // 位深不支持
        let e = decode(&wav(1, 1, 22050, 24, 3, 0, false, None, &[0, 0, 0]));
        assert!(e.unwrap_err().to_string().contains("位深"));
    }
}

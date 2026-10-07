//! 极简 PNG 写出（24 位真彩）。
//!
//! 无头产物只需要"能出图、能被工具和肉眼打开"，所以只手写必要 chunk
//! （IHDR / IDAT / IEND），压缩用 workspace 已有的纯 Rust `flate2`。
//! **不引第三方图像库** —— e2e 要能在无显示环境静态跑（plan §4.2）。

use std::fs::File;
use std::io::{self, Write};
use std::path::Path;

/// 写一张 24 位 PNG；`rgb` 长度必须正好是 `w * h * 3`。
pub fn write_rgb(path: &Path, w: u32, h: u32, rgb: &[u8]) -> io::Result<()> {
    assert_eq!(
        rgb.len(),
        w as usize * h as usize * 3,
        "RGB 缓冲尺寸与 {w}x{h} 不符"
    );

    let mut out = vec![0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A];

    let mut ihdr = Vec::with_capacity(13);
    ihdr.extend_from_slice(&w.to_be_bytes());
    ihdr.extend_from_slice(&h.to_be_bytes());
    ihdr.extend_from_slice(&[8, 2, 0, 0, 0]); // 8 位/通道、颜色类型 2 = 真彩、无隔行
    chunk(&mut out, b"IHDR", &ihdr);

    // 每行前面加一个 filter 字节（0 = None，即不做预测）
    let stride = w as usize * 3;
    let mut raw = Vec::with_capacity(rgb.len() + h as usize);
    for y in 0..h as usize {
        raw.push(0);
        raw.extend_from_slice(&rgb[y * stride..(y + 1) * stride]);
    }
    let mut z = flate2::write::ZlibEncoder::new(Vec::new(), flate2::Compression::default());
    z.write_all(&raw)?;
    chunk(&mut out, b"IDAT", &z.finish()?);

    chunk(&mut out, b"IEND", &[]);
    File::create(path)?.write_all(&out)
}

fn chunk(out: &mut Vec<u8>, kind: &[u8; 4], data: &[u8]) {
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    out.extend_from_slice(kind);
    out.extend_from_slice(data);
    let mut crc = Crc::new();
    crc.update(kind);
    crc.update(data);
    out.extend_from_slice(&crc.finish().to_be_bytes());
}

/// PNG 用的 CRC-32（IEEE 多项式）。自己写十几行比为此引一个依赖划算。
struct Crc(u32);

impl Crc {
    fn new() -> Self {
        Crc(0xFFFF_FFFF)
    }

    fn update(&mut self, data: &[u8]) {
        for &b in data {
            self.0 ^= u32::from(b);
            for _ in 0..8 {
                let mask = (self.0 & 1).wrapping_neg();
                self.0 = (self.0 >> 1) ^ (0xEDB8_8320 & mask);
            }
        }
    }

    fn finish(self) -> u32 {
        self.0 ^ 0xFFFF_FFFF
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn crc32_matches_standard_vector() {
        // CRC-32/ISO-HDLC 的标准自检向量
        let mut c = super::Crc::new();
        c.update(b"123456789");
        assert_eq!(c.finish(), 0xCBF4_3926);
    }

    #[test]
    fn writes_png_signature_and_chunks() {
        let dir = std::env::temp_dir().join("mir2-e2e-png-test");
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("t.png");
        super::write_rgb(&p, 2, 1, &[1, 2, 3, 4, 5, 6]).unwrap();
        let b = std::fs::read(&p).unwrap();
        assert_eq!(&b[0..8], &[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A]);
        assert_eq!(&b[12..16], b"IHDR");
        assert!(b.windows(4).any(|w| w == b"IDAT"));
        assert!(b.windows(4).any(|w| w == b"IEND"));
    }
}

//! `.wzl` —— 盛大图库**数据**文件（每张图独立 zlib 压缩）。
//!
//! 规格见 `docs/assets.md §3.2b`；参考实现 `$WS/Crystal/LibraryEditor/Graphics/WeMadeLibrary.cs`。
//!
//! ```text
//! 头部 64 字节（可忽略——偏移一律以 .wzx 为准）
//! 每条记录（16 字节）：
//!   u8   type_flag    // 5 = 16 位 RGB565；其它值 = 8 位调色板索引
//!   u8[3] 跳过
//!   i16  width, height
//!   i16  anchor_x, anchor_y      // ★ 渲染对齐必须保留
//!   i32  packed_size             // zlib 压缩后字节数
//!   u8[packed_size]              // zlib 流
//! ```
//!
//! 解压后为原始像素：**行按 4 字节对齐**、**数据自下而上**（第一行是图像底行）。
//! 8 位图用经典 MIR2 256 色（[`crate::palette::PALETTE`]），**索引 0 = 透明**；
//! 16 位图是 RGB565 小端，不需要调色板。

use std::fs;
use std::io::{self, Read};
use std::path::Path;

use crate::palette::PALETTE;
use crate::wzx::Wzx;

/// `.wzl` 头部长度。记录从 `.wzx` 给出的偏移处开始，因此该值只用于合法性检查。
pub const HEADER_LEN: usize = 64;
/// 单条记录长度。
pub const RECORD_LEN: usize = 16;
/// `type_flag` 等于此值 ⇒ 16 位 RGB565。
pub const TYPE_16BIT: u8 = 5;

/// 一张解码后的图（RGBA8888，行序**自上而下**，可直接送 GPU）。
#[derive(Debug, Clone)]
pub struct Sprite {
    pub width: u16,
    pub height: u16,
    /// 锚点偏移（原版 px/py）——精灵对齐要用，**不能丢**。
    pub anchor_x: i16,
    pub anchor_y: i16,
    pub rgba: Vec<u8>,
}

impl Sprite {
    pub fn is_empty(&self) -> bool {
        self.width == 0 || self.height == 0
    }
}

/// 单条图像记录（16 字节）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Record {
    pub type_flag: u8,
    pub width: u16,
    pub height: u16,
    pub anchor_x: i16,
    pub anchor_y: i16,
    pub packed_size: u32,
}

impl Record {
    pub fn parse(b: &[u8]) -> Self {
        debug_assert!(b.len() >= RECORD_LEN);
        Self {
            type_flag: b[0],
            width: u16::from_le_bytes([b[4], b[5]]),
            height: u16::from_le_bytes([b[6], b[7]]),
            anchor_x: i16::from_le_bytes([b[8], b[9]]),
            anchor_y: i16::from_le_bytes([b[10], b[11]]),
            packed_size: u32::from_le_bytes([b[12], b[13], b[14], b[15]]),
        }
    }

    pub fn is_16bit(&self) -> bool {
        self.type_flag == TYPE_16BIT
    }

    /// 每行在解压数据中占用的字节数（**4 字节对齐**）。
    fn stride(&self) -> usize {
        let bits = if self.is_16bit() { 16 } else { 8 };
        ((self.width as usize * bits + 31) >> 5) * 4
    }
}

pub struct Wzl {
    data: Vec<u8>,
    index: Wzx,
}

impl Wzl {
    /// 打开一对 `{base}.wzl` + `{base}.wzx`。
    pub fn open<P: AsRef<Path>>(base: P) -> io::Result<Self> {
        let base = base.as_ref();
        let data = fs::read(base.with_extension("wzl"))?;
        let wzx = fs::read(base.with_extension("wzx"))?;
        Self::from_bytes(data, &wzx)
    }

    pub fn from_bytes(data: Vec<u8>, wzx_bytes: &[u8]) -> io::Result<Self> {
        let index = Wzx::parse(wzx_bytes)?;
        if data.len() < HEADER_LEN {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!(".wzl 太短（{} 字节，至少需 {HEADER_LEN}）", data.len()),
            ));
        }
        Ok(Self { data, index })
    }

    pub fn len(&self) -> usize {
        self.index.len()
    }

    pub fn is_empty(&self) -> bool {
        self.index.is_empty()
    }

    pub fn record(&self, index: usize) -> Option<Record> {
        let o = self.index.offset(index)? as usize;
        let end = o.checked_add(RECORD_LEN)?;
        if o < HEADER_LEN || end > self.data.len() {
            return None;
        }
        Some(Record::parse(&self.data[o..end]))
    }

    /// 解码第 `index` 张图。空白图（宽/高为 0 或压缩长度为 0）返回 `None`。
    pub fn decode(&self, index: usize) -> Option<Sprite> {
        let o = self.index.offset(index)? as usize;
        let rec_end = o.checked_add(RECORD_LEN)?;
        if o < HEADER_LEN || rec_end > self.data.len() {
            return None;
        }
        let rec = Record::parse(&self.data[o..rec_end]);
        let (w, h) = (rec.width as usize, rec.height as usize);
        if w == 0 || h == 0 || rec.packed_size == 0 {
            return None;
        }
        let start = rec_end;
        let end = start.checked_add(rec.packed_size as usize)?;
        if end > self.data.len() {
            return None;
        }
        let raw = inflate(&self.data[start..end]).ok()?;
        let stride = rec.stride();
        if raw.len() < stride * h {
            return None;
        }

        let mut rgba = vec![0u8; w * h * 4];
        for row in 0..h {
            let y = h - 1 - row; // ★ 数据自下而上
            let ro = row * stride;
            for x in 0..w {
                let (r, g, b, a) = if rec.is_16bit() {
                    let i = ro + x * 2;
                    let v = u16::from_le_bytes([raw[i], raw[i + 1]]);
                    // 与参考实现一致：5/6/5 → 8 位用左移（不做位复制）
                    (
                        (((v >> 11) & 0x1F) as u8) << 3,
                        (((v >> 5) & 0x3F) as u8) << 2,
                        ((v & 0x1F) as u8) << 3,
                        255,
                    )
                } else {
                    let px = raw[ro + x] as usize;
                    let [r, g, b] = PALETTE[px];
                    (r, g, b, if px == 0 { 0 } else { 255 })
                };
                let q = (y * w + x) * 4;
                rgba[q] = r;
                rgba[q + 1] = g;
                rgba[q + 2] = b;
                rgba[q + 3] = a;
            }
        }
        Some(Sprite {
            width: rec.width,
            height: rec.height,
            anchor_x: rec.anchor_x,
            anchor_y: rec.anchor_y,
            rgba,
        })
    }
}

fn inflate(src: &[u8]) -> io::Result<Vec<u8>> {
    let mut out = Vec::new();
    flate2::read::ZlibDecoder::new(src).read_to_end(&mut out)?;
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use flate2::write::ZlibEncoder;
    use flate2::Compression;
    use std::io::Write;

    fn deflate(raw: &[u8]) -> Vec<u8> {
        let mut e = ZlibEncoder::new(Vec::new(), Compression::default());
        e.write_all(raw).unwrap();
        e.finish().unwrap()
    }

    /// 组装一对 (wzl, wzx)，内含一张图。
    fn pack(r#type: u8, w: i16, h: i16, ax: i16, ay: i16, raw: &[u8]) -> (Vec<u8>, Vec<u8>) {
        let payload = deflate(raw);
        let mut wzx = vec![0u8; 48];
        wzx[44..48].copy_from_slice(&1u32.to_le_bytes());
        wzx.extend_from_slice(&64u32.to_le_bytes());

        let mut wzl = vec![0u8; 64];
        wzl.push(r#type);
        wzl.extend_from_slice(&[0, 0, 0]);
        wzl.extend_from_slice(&w.to_le_bytes());
        wzl.extend_from_slice(&h.to_le_bytes());
        wzl.extend_from_slice(&ax.to_le_bytes());
        wzl.extend_from_slice(&ay.to_le_bytes());
        wzl.extend_from_slice(&(payload.len() as u32).to_le_bytes());
        wzl.extend_from_slice(&payload);
        (wzl, wzx)
    }

    #[test]
    fn 解码八位图_自下而上且索引零透明() {
        // 2x2：数据首行 = 图像底行；每行 4 字节（宽 2 ⇒ stride 4，行尾 2 字节对齐填充）
        //   raw 行0 = 底行 [1, 2, pad, pad]；raw 行1 = 顶行 [3, 0, pad, pad]
        //   期望自上而下：顶行 [3, 透明] ; 底行 [pal1, pal2]
        let raw = [1u8, 2, 0, 0, 3, 0, 0, 0];
        let (wzl, wzx) = pack(3, 2, 2, 7, -44, &raw);
        let lib = Wzl::from_bytes(wzl, &wzx).unwrap();

        let rec = lib.record(0).unwrap();
        assert_eq!(
            (rec.width, rec.height, rec.anchor_x, rec.anchor_y),
            (2, 2, 7, -44)
        );
        assert!(!rec.is_16bit());

        let s = lib.decode(0).unwrap();
        assert_eq!((s.width, s.height), (2, 2));
        // 顶行左：索引 3
        let p = |x: usize, y: usize| {
            let q = (y * 2 + x) * 4;
            [s.rgba[q], s.rgba[q + 1], s.rgba[q + 2], s.rgba[q + 3]]
        };
        let [r3, g3, b3] = PALETTE[3];
        assert_eq!(p(0, 0), [r3, g3, b3, 255]);
        // 顶行右：索引 0 ⇒ 透明
        assert_eq!(p(1, 0)[3], 0);
        let [r1, g1, b1] = PALETTE[1];
        assert_eq!(p(0, 1), [r1, g1, b1, 255]);
        // 行对齐：宽 2 ⇒ 8 位 stride = ((2*8+31)>>5)*4 = 4（即每行 4 字节，含 2 字节填充）
        assert_eq!(rec.stride(), 4);
    }

    #[test]
    fn 解码十六位图() {
        // 2x1，RGB565：纯红 0xF800、纯绿 0x07E0
        let raw = [0x00u8, 0xF8, 0xE0, 0x07];
        let (wzl, wzx) = pack(TYPE_16BIT, 2, 1, 0, 0, &raw);
        let lib = Wzl::from_bytes(wzl, &wzx).unwrap();
        let rec = lib.record(0).unwrap();
        assert!(rec.is_16bit());

        let s = lib.decode(0).unwrap();
        assert_eq!((s.width, s.height), (2, 1));
        assert_eq!(&s.rgba[0..4], &[0xF8, 0x00, 0x00, 255]);
        // 绿分量 6 位 0x3F 左移两位 = 0xFC（与 Crystal 的换算一致）
        assert_eq!(&s.rgba[4..8], &[0x00, 0xFC, 0x00, 255]);
    }

    #[test]
    fn 空图返回none() {
        let (wzl, wzx) = pack(3, 0, 0, 0, 0, &[]);
        let lib = Wzl::from_bytes(wzl, &wzx).unwrap();
        assert!(lib.decode(0).is_none());
    }

    /// 真实素材回归（本机专用）：设 `MIR2C_DATA=$WS/mir2c/data` 后 `cargo test` 即会跑。
    /// 期望值与 Python 参考实现一致（见 docs/assets.md §3.2b）。
    #[test]
    fn 真实素材_prguse_首图() {
        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let lib = Wzl::open(Path::new(&dir).join("Prguse")).unwrap();
        assert_eq!(lib.len(), 1851, "Prguse 应有 1851 张");

        let rec = lib.record(0).unwrap();
        assert_eq!(
            (rec.width, rec.height, rec.anchor_x, rec.anchor_y),
            (24, 22, 7, -44)
        );
        assert_eq!(rec.packed_size, 59);
        assert!(!rec.is_16bit());

        let s = lib.decode(0).unwrap();
        assert_eq!(s.rgba.len(), 24 * 22 * 4);

        // Items 是 16 位（免调色板）——位深判别回归
        let items = Wzl::open(Path::new(&dir).join("Items")).unwrap();
        assert!(items.record(0).unwrap().is_16bit(), "Items 应为 16 位");

        // Hum 张数
        let hum = Wzl::open(Path::new(&dir).join("Hum")).unwrap();
        assert_eq!(hum.len(), 14400);
    }

    /// **黄金哈希**：Rust 解码结果必须与独立实现（Python 参考脚本）**逐字节一致**。
    /// 这是"双实现漂移"的护栏（[docs/protocol.md §9](../../../../docs/protocol.md) 的同类思路）。
    #[test]
    fn 真实素材_黄金哈希() {
        use sha2::{Digest, Sha256};

        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };

        // (库, 图序号, RGBA 的 sha256 —— 由 Python 参考实现产出)
        let cases: &[(&str, usize, &str)] = &[
            (
                "Prguse",
                0,
                "60195a129fba3cc4d1e19a63d4818dc20cd28eafeb6753e735d92aac931900e7",
            ),
            (
                "Prguse",
                1,
                "9b5d729c1a3542c7ab4d5f968333e1b8bb2324786e8557658636dea78fae5f1e",
            ),
            (
                "Hum",
                0,
                "62b09c2b7209394a0b64495fb47dae45228301698c741e58e3ce508719c74302",
            ),
            (
                "Items",
                0,
                "d58706c951e953c46389838b39b075d53daad90b524373b5ae0ae5da2e30fdd4",
            ),
            (
                "Mon1",
                0,
                "72b776133b93b91753dbfd12737b4ba8614c11009d321147923e16d1bb7dc2d4",
            ),
        ];

        for (name, idx, want) in cases {
            let lib = Wzl::open(Path::new(&dir).join(name)).unwrap();
            let s = lib
                .decode(*idx)
                .unwrap_or_else(|| panic!("{name}#{idx} 解码失败"));
            let got = format!("{:x}", Sha256::digest(&s.rgba));
            assert_eq!(&got, want, "{name}#{idx} 与 Python 参考实现不一致");
        }
        println!("黄金哈希 {} 例全部一致", cases.len());
    }
}

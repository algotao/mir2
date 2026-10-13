//! 图库读取：生产客户端**只读清单限定的** `assets/image/images.m2pk`；裸 `.wzl/.wzx`
//! 入口仅供离线工具/真素材测试，不参与运行时回退。
//!
//! - 裸图库：每张图各自一个 zlib 流（`Raw`，离线测试用）。
//! - 美术容器（`kind=3`）：每 N 张一组、每组一条 brotli 流；稀疏图号以空记录占位，
//!   不压缩重编号（`Packed`，见 `crate::image_lib` 与 `client/assets/image-libraries.tsv`）。
//!
//! ```text
//! 头部 64 字节（可忽略——偏移一律以 .wzx 为准）
//! 每条记录（16 字节）：
//!   u8   type_flag    // 5 = 16 位 RGB565；其它值 = 8 位调色板索引
//!   u8[3] 跳过
//!   i16  width, height
//!   i16  anchor_x, anchor_y      // ★ 渲染对齐必须保留
//!   i32  packed_size             // zlib 压缩后字节数；★ 0 且宽高非 0 = **未压缩直存**
//!   u8[packed_size]              // zlib 流；未压缩时这里就是 raw 像素（stride × height）
//! ```
//!
//! 解压后为原始像素：**行按 4 字节对齐**、**数据自下而上**（第一行是图像底行）。
//! 8 位图用经典 MIR2 256 色（[`crate::palette::PALETTE`]），**索引 0 = 透明**；
//! 16 位图是 RGB565 小端，不需要调色板，**`0x0000` = 透明**。
//!
//! ⚠️ 那条 16 位的透明规则是 2026-10-09 补的（用户报的"半兽人在地图上显示黑色底框，
//! 更下层的图形被盖住"）：这套素材里的 `Mon11`/`Mon34`（半兽人那几只）**整个是 16 位**，
//! 而 16 位图没有调色板索引 ⇒ 原来一律当不透明 ⇒ **精灵带着它的黑底一起贴上屏**。
//! 证据：`Mon11[0]` 四角与第一行全是 `0x0000`，按"0 = 透明"抠掉之后就是干净的剪影
//!（`tools` 侧的 Python 参考实现同步改了，见 `真实素材_黄金哈希`）。
//!
//! ⚠️ **两条载体必须解出逐字节相同的结果**：所以"raw → RGBA"只有一份实现
//!（[`to_rgba`]），行距也只有一份（[`stride_of`]）—— 容器那条路只是换了个
//! "从哪儿拿到 raw"，绝不许自己再写一遍像素换算。

use std::collections::HashSet;
use std::fs;
use std::io::{self, Read};
use std::path::Path;
use std::rc::Rc;
use std::sync::{Mutex, OnceLock};

use crate::m2pk::Archive;
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

    /// **不透明像素**的包围盒 `(x, y, w, h)`（图内坐标）；整张全透明返回 `None`。
    ///
    /// 用途：把精灵"按内容"对齐（例：选角的小人要按不透明部分的底边中点落到凹槽上）。
    /// 人物图四周的透明边各图不同，按整图尺寸对齐会让不同职业/性别的小人**高低不一**。
    pub fn alpha_bbox(&self) -> Option<BBox> {
        if self.is_empty() {
            return None;
        }
        let (w, h) = (self.width as i32, self.height as i32);
        let (mut x0, mut y0, mut x1, mut y1) = (w, h, -1i32, -1i32);
        for y in 0..h {
            for x in 0..w {
                if self.rgba[((y * w + x) * 4 + 3) as usize] < 128 {
                    continue;
                }
                x0 = x0.min(x);
                y0 = y0.min(y);
                x1 = x1.max(x);
                y1 = y1.max(y);
            }
        }
        (x1 >= 0).then(|| (x0, y0, (x1 - x0 + 1) as u32, (y1 - y0 + 1) as u32))
    }
}

/// 不透明像素的包围盒：`(x, y, w, h)`（图内坐标）。
///
/// 起个名字是因为它横跨三处（`Sprite::alpha_bbox` / `select_ui::place_sprite` /
/// `app::ui::UiCache::bbox`）—— 裸元组写到第三处就看不懂了。
pub type BBox = (i32, i32, u32, u32);

/// 单条图像记录（16 字节）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
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

    /// 空白图（直读路径的判据：**只看宽/高为 0**）。
    ///
    /// ⚠️ `packed_size == 0` **不是**空白：那是"像素未压缩直存"（见
    /// [`Raw::decode`] 的第二条分支）。2026-10-12 换素材集后发现这套库里有
    /// **4096 张**这样的图（数字字形、头发、武器、地图物件……），旧判据把它们
    /// 一律当空白 ⇒ 整片不显示（等级数字就是这么没的）。
    fn is_blank(&self) -> bool {
        self.width == 0 || self.height == 0
    }
}

/// 每行在 raw 数据里占的字节数（**4 字节对齐**）。
///
/// 与 Go 侧 `m2pk.ImageRecord.Stride` 同一条公式：`((w*bits + 31) >> 5) * 4`。
pub fn stride_of(type_flag: u8, width: u16) -> usize {
    let bits = if type_flag == TYPE_16BIT { 16 } else { 8 };
    ((width as usize * bits + 31) >> 5) * 4
}

/// raw 像素 → RGBA8888（行序**自上而下**）。
///
/// 直读 `.wzl` 与容器里的 IMGP 载荷**共用**这一份：两条路的解码结果必须逐字节
/// 一致（`真实素材_黄金哈希` 与 `image_lib` 的对拍测试都盯着这点）。
pub fn to_rgba(type_flag: u8, width: u16, height: u16, raw: &[u8]) -> Option<Vec<u8>> {
    let (w, h) = (width as usize, height as usize);
    if w == 0 || h == 0 {
        return None;
    }
    let stride = stride_of(type_flag, width);
    if raw.len() < stride * h {
        return None;
    }
    let is16 = type_flag == TYPE_16BIT;
    let mut rgba = vec![0u8; w * h * 4];
    for row in 0..h {
        let y = h - 1 - row; // ★ 数据自下而上
        let ro = row * stride;
        for x in 0..w {
            let (r, g, b, a) = if is16 {
                let i = ro + x * 2;
                let v = u16::from_le_bytes([raw[i], raw[i + 1]]);
                // 与参考实现一致：5/6/5 → 8 位用左移（不做位复制）
                //
                // `0x0000` = 透明（见文件头那条说明）：这是 16 位图**唯一**的"背景"表达 ——
                // 没有调色板索引可用。副作用是"精灵内部真正的纯黑像素也会被抠掉"，
                // 但实测这套素材的轮廓不是 0（抠完剪影完整），且原版同样是按色键处理的。
                (
                    (((v >> 11) & 0x1F) as u8) << 3,
                    (((v >> 5) & 0x3F) as u8) << 2,
                    ((v & 0x1F) as u8) << 3,
                    if v == 0 { 0 } else { 255 },
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
    Some(rgba)
}

/// 一个图库的句柄 —— **同一套 API，两种载体**。
///
/// 上层（`app` / `e2e`）只认这个类型：`len` / `record` / `decode` 的语义与返回值
/// 在两种载体下**完全一致**，所以换载体不需要改渲染代码。
pub enum Wzl {
    /// 裸目录里的 `.wzl` + `.wzx`（仅测试/离线工具可用；客户端运行时不走这条）。
    Raw(Raw),
    /// 容器里的 IMGP 分组载荷（见 [`crate::image_lib`]）。
    Packed(crate::image_lib::PackedLib),
}

static REPORTED_IMAGE_ISSUES: OnceLock<Mutex<HashSet<(String, usize)>>> = OnceLock::new();

fn manifest_allows(library: &str, index: usize) -> bool {
    crate::image_manifest::ImageManifest::shared().is_ok_and(|m| m.allows_image(library, index))
}

fn report_image_issue(library: &str, index: usize, reason: &str) {
    let issues = REPORTED_IMAGE_ISSUES.get_or_init(|| Mutex::new(HashSet::new()));
    let Ok(mut seen) = issues.lock() else { return };
    if seen.insert((library.to_string(), index)) {
        eprintln!("[asset:error] {library}[{index}] {reason}; 修正 client/assets/image-libraries.tsv 并重打 images.m2pk");
    }
}

impl Wzl {
    /// 打开一对 `{base}.wzl` + `{base}.wzx`。
    pub fn open<P: AsRef<Path>>(base: P) -> io::Result<Self> {
        Ok(Wzl::Raw(Raw::open(base)?))
    }

    pub fn from_bytes(data: Vec<u8>, wzx_bytes: &[u8]) -> io::Result<Self> {
        Ok(Wzl::Raw(Raw::from_bytes(data, wzx_bytes)?))
    }

    /// 从美术容器里打开（`Ok(None)` = 容器里没有这个库）。
    pub fn open_packed(archive: Rc<Archive>, name: &str) -> io::Result<Option<Self>> {
        Ok(crate::image_lib::PackedLib::open(archive, name)?.map(Wzl::Packed))
    }

    pub fn len(&self) -> usize {
        match self {
            Wzl::Raw(r) => r.len(),
            Wzl::Packed(p) => p.len(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    pub fn record(&self, index: usize) -> Option<Record> {
        match self {
            Wzl::Raw(r) => r.record(index),
            Wzl::Packed(p) => {
                if !manifest_allows(p.name(), index) {
                    report_image_issue(p.name(), index, "图号未列入客户端素材清单 / 未打包");
                    return None;
                }
                let rec = p.record(index);
                if rec.is_none() {
                    report_image_issue(p.name(), index, "索引越界或容器记录缺失");
                }
                rec
            }
        }
    }

    /// 解码第 `index` 张图。空白图（宽/高为 0 或没有像素数据）返回 `None`。
    pub fn decode(&self, index: usize) -> Option<Sprite> {
        match self {
            Wzl::Raw(r) => r.decode(index),
            Wzl::Packed(p) => {
                let name = p.name();
                if !manifest_allows(name, index) {
                    report_image_issue(name, index, "图号未列入客户端素材清单 / 未打包");
                    return None;
                }
                let Some(rec) = p.record(index) else {
                    report_image_issue(name, index, "图号超出容器记录范围");
                    return None;
                };
                if rec.is_blank() {
                    return None; // 清单内合法空白帧，不当作缺素材
                }
                let sprite = p.decode(index);
                if sprite.is_none() {
                    report_image_issue(name, index, "容器记录有尺寸，但像素缺失/损坏");
                }
                sprite
            }
        }
    }
}

/// 裸目录载体：`.wzl` + `.wzx`。
pub struct Raw {
    data: Vec<u8>,
    index: Wzx,
}

impl Raw {
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

    /// 解码第 `index` 张图。空白图（宽/高为 0）返回 `None`。
    ///
    /// 两条取像素的路：
    ///
    /// 1. `packed_size > 0` ⇒ 记录头后是一条 zlib 流，解开就是 raw 像素；
    /// 2. `packed_size == 0` **且宽高非 0** ⇒ 像素**未压缩直存**在记录头后面，
    ///    长度 = 行距 × 高（见 [`Raw::raw_span`] 的判据）。
    pub fn decode(&self, index: usize) -> Option<Sprite> {
        let o = self.index.offset(index)? as usize;
        if o < HEADER_LEN {
            return None;
        }
        let rec = self.record(index)?;
        if rec.is_blank() {
            return None;
        }
        let start = o + RECORD_LEN;
        let raw: Vec<u8> = if rec.packed_size == 0 {
            let len = stride_of(rec.type_flag, rec.width) * rec.height as usize;
            let (s, e) = self.raw_span(index, o, start, len)?;
            self.data[s..e].to_vec()
        } else {
            let end = start.checked_add(rec.packed_size as usize)?;
            if end > self.data.len() {
                return None;
            }
            inflate(&self.data[start..end]).ok()?
        };
        let rgba = to_rgba(rec.type_flag, rec.width, rec.height, &raw)?;
        Some(Sprite {
            width: rec.width,
            height: rec.height,
            anchor_x: rec.anchor_x,
            anchor_y: rec.anchor_y,
            rgba,
        })
    }

    /// "未压缩直存"的像素区间 `(start, end)`；这张图不是这么存的就返回 `None`。
    ///
    /// 素材里有一批 `packed_size == 0` 但**宽高非 0** 的图：像素原样摆在 16 字节记录头
    /// 后面（`Prguse[30..40]` 的数字字形 —— 12×10 正好 120 字节，下一条记录的偏移
    /// 也正好是 本条 + 16 + 120）。2026-10-12 换素材集后统计：这套库 **4096 张**这么存
    /// （数字、头发 `hair2`、武器 `Weapon`、地图物件 `Objects29/30`……）。
    ///
    /// 判据（**必须与 Go 侧 `internal/m2pk/image.go` 的 `rawSpan` 一致**，否则容器与
    /// 直读两条路会解出不同的图）：
    ///   · 下一张图的偏移**正好**等于 本条 + 16 + stride×高 ⇒ 数据确实占这么多；
    ///   · 或这张是**最后一张**（下一条偏移为 0 / 回退）且区间落在文件内。
    ///
    /// 反向的例子：`Objects25~27` 那批 1×1 / 4×1 的**真空白图**下一条偏移对不上 ⇒ 判为
    /// 空白 —— 不会把下一条记录头的头几个字节当成像素，在地图上画出一堆杂点。
    fn raw_span(
        &self,
        index: usize,
        rec_off: usize,
        start: usize,
        len: usize,
    ) -> Option<(usize, usize)> {
        match self.index.offset(index + 1).map(|v| v as usize) {
            Some(next) if next > rec_off => (next == start + len).then_some((start, start + len)),
            _ => {
                let end = start.checked_add(len)?;
                (end <= self.data.len()).then_some((start, end))
            }
        }
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
        assert_eq!(stride_of(rec.type_flag, rec.width), 4);
        assert_eq!(stride_of(TYPE_16BIT, 3), 8);
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

    /// 组装一对 (wzl, wzx)：记录头后**直接**摆 raw 像素（`packed_size` 写 0）。
    ///
    /// `next_off` = 索引里"第二张图"的偏移（`None` = 这个库只有一张图）——
    /// 决定 [`Raw::raw_span`] 走"下一张紧随"还是"末张"那条判据。
    fn pack_uncompressed(
        r#type: u8,
        w: i16,
        h: i16,
        raw: &[u8],
        next_off: Option<u32>,
    ) -> (Vec<u8>, Vec<u8>) {
        let mut wzl = vec![0u8; 64];
        wzl.push(r#type);
        wzl.extend_from_slice(&[0, 0, 0]);
        wzl.extend_from_slice(&w.to_le_bytes());
        wzl.extend_from_slice(&h.to_le_bytes());
        wzl.extend_from_slice(&0i16.to_le_bytes());
        wzl.extend_from_slice(&0i16.to_le_bytes());
        wzl.extend_from_slice(&0u32.to_le_bytes()); // ★ packed_size = 0
        wzl.extend_from_slice(raw);
        let mut wzx = vec![0u8; 48];
        let count: u32 = if next_off.is_some() { 2 } else { 1 };
        wzx[44..48].copy_from_slice(&count.to_le_bytes());
        wzx.extend_from_slice(&64u32.to_le_bytes());
        if let Some(o) = next_off {
            wzx.extend_from_slice(&o.to_le_bytes());
        }
        (wzl, wzx)
    }

    /// 2026-10-12 换素材集后补的：这套库里有 4096 张图是**未压缩直存**
    /// （`Prguse[30..40]` 的数字字形就是），旧判据把它们当空白 ⇒ 等级数字整片不显示。
    #[test]
    fn 未压缩直存的图也要解出来() {
        // 8 位 2x2：stride 4 ⇒ 8 字节。raw 行0 = 底行 [1, 2]，行1 = 顶行 [3, 4]
        let raw = [1u8, 2, 0, 0, 3, 4, 0, 0];
        // 只有一张图 ⇒ 走"末张"判据
        let (wzl, wzx) = pack_uncompressed(3, 2, 2, &raw, None);
        let lib = Wzl::from_bytes(wzl, &wzx).unwrap();
        let s = lib.decode(0).expect("未压缩图应解得出");
        assert_eq!((s.width, s.height), (2, 2));
        let p = |x: usize, y: usize| {
            let q = (y * 2 + x) * 4;
            [s.rgba[q], s.rgba[q + 1], s.rgba[q + 2], s.rgba[q + 3]]
        };
        let [r3, g3, b3] = PALETTE[3];
        assert_eq!(p(0, 0), [r3, g3, b3, 255]); // 数据自下而上 ⇒ 屏幕上第一行是 raw 的顶行
        let [r4, g4, b4] = PALETTE[4];
        assert_eq!(p(1, 0), [r4, g4, b4, 255]);
        let [r1, g1, b1] = PALETTE[1];
        assert_eq!(p(0, 1), [r1, g1, b1, 255]);

        // 下一条记录**正好**紧随其后 ⇒ 走"下一张紧随"判据，同样要解得出
        let (wzl, wzx) = pack_uncompressed(3, 2, 2, &raw, Some(64 + 16 + 8));
        let lib = Wzl::from_bytes(wzl, &wzx).unwrap();
        assert!(lib.decode(0).is_some());
    }

    /// `Objects25~27` 那批 1x1 / 4x1 是**真空白**：下一条记录的偏移对不上"记录头+像素"
    /// ⇒ 必须判空，不能把下一条记录头的头几个字节当像素（会在地图上画出杂点）。
    #[test]
    fn 真空白图不把下一条记录头当像素() {
        let raw = [1u8, 2, 0, 0, 3, 4, 0, 0];
        let (wzl, wzx) = pack_uncompressed(3, 2, 2, &raw, Some(64 + 16 + 8 + 4));
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

    /// **16 位精灵不许带黑底**（用户 2026-10-09 报的"半兽人显示黑色底框、盖住下层图形"）。
    ///
    /// `Mon11`/`Mon34`（半兽人那几只）整库都是 16 位：背景是 `0x0000`。
    /// 判据用**不透明包围盒**：抠对了 ⇒ 四周一圈透明、bbox 比整图小；
    /// 抠错了（原来那样一律不透明）⇒ bbox 就是整张图 —— 那正是"一块黑底"。
    #[test]
    fn 真实素材_十六位精灵不带黑底() {
        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        // `Mon34` 的前几张是空记录（实测）⇒ 用各自**确实有图**的那一张
        for (name, idx) in [("Mon11", 0usize), ("Mon34", 4)] {
            let lib = Wzl::open(Path::new(&dir).join(name)).unwrap();
            let s = lib
                .decode(idx)
                .unwrap_or_else(|| panic!("{name}#{idx} 解码失败"));
            assert!(
                lib.record(idx).unwrap().is_16bit(),
                "{name} 该是 16 位（这条测的就是那条透明度规则）"
            );
            // 四角必须透明（这就是"背景"）
            let px = |x: u16, y: u16| {
                let q = ((y as usize * s.width as usize) + x as usize) * 4;
                s.rgba[q + 3]
            };
            assert_eq!(px(0, 0), 0, "{name}#{idx} 左上角该是透明的背景");
            assert_eq!(px(s.width - 1, 0), 0, "{name}#{idx} 右上角该是透明的背景");
            // 透明像素得占**大头**（实测这两张分别是 56.6% / 80.6%）—— 那就是"背景被抠掉了"。
            // ⚠️ 别拿"包围盒比整图小"当判据：`Mon34#4` 的角/耳会顶到框边，bbox 就是整图，
            // 但那不是黑底（它的外圈 2px 里 94.9% 是透明的）。
            let clear = s.rgba.chunks(4).filter(|c| c[3] == 0).count();
            let frac = clear as f32 / (s.width as usize * s.height as usize) as f32;
            assert!(
                frac > 0.25,
                "{name}#{idx} 只有 {:.1}% 透明 —— 背景没抠掉（就是那块黑底）",
                frac * 100.0
            );
        }
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
                // ⚠️ 2026-10-09 变过：16 位图改成"`0x0000` = 透明"（见文件头）⇒
                // 含 0 像素的那几张的哈希跟着变。这一份是**参考实现重算**的。
                "75c32bd31bc0934ebab8fffe2c589778e34a5e442ef9b60f3f0ee3dc2d9bb461",
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

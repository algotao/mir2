//! IMGP —— 美术图库的**分组载荷**读取（M2PK 容器 `kind=3` 的块内容）。
//!
//! 规格的权威定义在写侧：`internal/m2pk/image.go`（Go）。这里是只读实现。
//!
//! ```text
//! Header（16 B）
//!   0   magic      [4]  "IMGP"
//!   4   version    u8   1
//!   5   reserved   [3]
//!   8   count      u32  图数
//!   12  groupSize  u16  每组图数
//!   14  reserved2  u16
//! 组表（groups × 12 B）：offset / compLen / rawLen（offset 相对数据区）
//! 记录表（count × 16 B）：与 .wzl 记录同构，`packed_size` 恒为 0（不适用）
//! 数据区（8 对齐）：每组一条 brotli 流，组内是各图 raw 的顺序拼接
//! ```
//!
//! 为什么值得这么绕：`.wzl` 是"每张图各自一个 zlib 流"，deflate 的 32 KB 窗口
//! 跨不了图；把 raw 解出来按组拼一条 brotli 流，实测整体省 ~20%
//!（type 3 88%→83%，type 5 70%→65%，见 `docs/assets.md §5c`）。
//!
//! **访问模式**：一组是一个随机访问单位 —— 首次取组内任意一张时解压整组并缓存
//!（[`CACHE_GROUPS`] 组的 FIFO），因此"取一张"的代价是"解一组"，而不是"解一个库"。
//!
//! **像素解码只有一份**：这里拿到 raw 后调 [`crate::wzl::to_rgba`]，
//! 与直读 `.wzl` 完全同一段代码 ⇒ 两条载体逐字节一致。

use std::cell::{OnceCell, RefCell};
use std::io::{self, Cursor, Read};
use std::rc::Rc;

use brotli_decompressor::Decompressor;

use crate::m2pk::{Archive, Entry};
use crate::wzl::{stride_of, to_rgba, Record, Sprite, RECORD_LEN};

thread_local! {
    /// 进程级懒打开的美术容器（`None` = 没容器 / 打不开）。
    ///
    /// 用 `thread_local` 而不是全局 `static`：`Archive` 里是 `Rc` + `RefCell`
    ///（非 `Sync`），而这本来就是**渲染线程自己的缓存** —— 与 app 里那些
    /// 图库/纹理缓存同一线程。
    static ART: OnceCell<Option<Rc<Archive>>> = const { OnceCell::new() };
}

/// 打开（并缓存）美术容器；没有容器或打不开时返回 `None`，调用方回退裸目录。
///
/// 只把索引区读进内存（全量 1.4 GB，不能整文件读；见 [`Archive::open_lazy`]）。
pub fn art_archive() -> Option<Rc<Archive>> {
    ART.with(|cell| {
        cell.get_or_init(|| match crate::paths::image_container() {
            None => None,
            Some(p) => match Archive::open_lazy(&p) {
                Ok(a) => Some(Rc::new(a)),
                Err(e) => {
                    eprintln!("[art] {} 打不开（{e}）⇒ 回退裸 .wzl", p.display());
                    None
                }
            },
        })
        .clone()
    })
}

/// 魔数。
pub const MAGIC: &[u8; 4] = b"IMGP";
/// 格式版本。
pub const VERSION: u8 = 1;
/// 头部长度。
pub const HEADER_LEN: usize = 16;
/// 组表每条长度。
pub const GROUP_ENTRY_LEN: usize = 12;
/// 缓存多少组（每组几十到几百 KB：太平会反复解压，太大白占内存）。
///
/// 与 Go 侧 `m2pk.MaxImageGroup` 无关：那是**每组的图数**上限，这是**驻留的组数**。
const CACHE_GROUPS: usize = 24;

fn bad(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.into())
}

fn align8(n: usize) -> usize {
    (n + 7) & !7
}

fn u32_at(b: &[u8], off: usize) -> u32 {
    u32::from_le_bytes([b[off], b[off + 1], b[off + 2], b[off + 3]])
}

fn u16_at(b: &[u8], off: usize) -> u16 {
    u16::from_le_bytes([b[off], b[off + 1]])
}

/// 一组在数据区里的位置。
#[derive(Debug, Clone, Copy)]
struct GroupRef {
    off: u32,
    comp_len: u32,
    raw_len: u32,
}

/// 容器里的一个图库（IMGP 载荷的只读视图 + 组缓存）。
pub struct PackedLib {
    archive: Rc<Archive>,
    entry: Entry,
    count: usize,
    group_size: usize,
    groups: Vec<GroupRef>,
    records: Vec<Record>,
    /// 数据区在块内的起点（组表的 `offset` 相对它）。
    data_off: u32,
    /// 组号 → 解压后的像素（FIFO）。
    cache: RefCell<Vec<(usize, Rc<Vec<u8>>)>>,
}

impl PackedLib {
    /// 打开容器里的一个图库；`Ok(None)` = 容器里没有这个名字。
    pub fn open(archive: Rc<Archive>, name: &str) -> io::Result<Option<Self>> {
        let Some(entry) = archive.lookup(name).cloned() else {
            return Ok(None);
        };
        if (entry.raw_size as usize) < HEADER_LEN {
            return Err(bad(format!(
                "IMGP {:?} 块只有 {} 字节",
                entry.name, entry.raw_size
            )));
        }
        let head = archive.read_range(&entry, 0, HEADER_LEN as u32)?;
        if &head[0..4] != MAGIC {
            return Err(bad(format!("IMGP {:?} 魔数不对", entry.name)));
        }
        if head[4] != VERSION {
            return Err(bad(format!(
                "IMGP {:?} 版本 {} 不支持",
                entry.name, head[4]
            )));
        }
        let count = u32_at(&head, 8) as usize;
        let group_size = u16_at(&head, 12) as usize;
        if count == 0 || group_size == 0 {
            return Err(bad(format!(
                "IMGP {:?} 表头不自洽：count={count} groupSize={group_size}",
                entry.name
            )));
        }
        let groups_n = count.div_ceil(group_size);
        let table_len = HEADER_LEN + GROUP_ENTRY_LEN * groups_n + RECORD_LEN * count;
        if table_len > entry.raw_size as usize {
            return Err(bad(format!(
                "IMGP {:?} 表区越界（需 {table_len}，块有 {}）",
                entry.name, entry.raw_size
            )));
        }
        let table = archive.read_range(&entry, 0, table_len as u32)?;
        let data_off = align8(table_len) as u32;

        let mut groups = Vec::with_capacity(groups_n);
        for g in 0..groups_n {
            let e = &table[HEADER_LEN + GROUP_ENTRY_LEN * g..];
            let r = GroupRef {
                off: u32_at(e, 0),
                comp_len: u32_at(e, 4),
                raw_len: u32_at(e, 8),
            };
            // 组流必须落在块内（越界就地拦住，不要等到解压时才炸）
            if data_off as u64 + r.off as u64 + r.comp_len as u64 > entry.raw_size as u64 {
                return Err(bad(format!(
                    "IMGP {:?} 第 {g} 组流越界（off={} len={}，块 {}）",
                    entry.name, r.off, r.comp_len, entry.raw_size
                )));
            }
            groups.push(r);
        }
        let rec_off = HEADER_LEN + GROUP_ENTRY_LEN * groups_n;
        let mut records = Vec::with_capacity(count);
        for i in 0..count {
            records.push(Record::parse(&table[rec_off + RECORD_LEN * i..]));
        }

        Ok(Some(Self {
            archive,
            entry,
            count,
            group_size,
            groups,
            records,
            data_off,
            cache: RefCell::new(Vec::new()),
        }))
    }

    /// 图数（与源 `.wzx` 一致，索引语义按位对齐）。
    pub fn len(&self) -> usize {
        self.count
    }

    pub fn is_empty(&self) -> bool {
        self.count == 0
    }

    /// 每组图数（体检/调试用）。
    pub fn group_size(&self) -> usize {
        self.group_size
    }

    /// 第 `index` 张的记录。
    ///
    /// ⚠️ 与直读路径的差别：`packed_size` 恒为 0（容器里没有内层压缩流）。
    /// 上层只用宽高/锚点（见 `app::ui` 的说明），不受影响。
    pub fn record(&self, index: usize) -> Option<Record> {
        self.records.get(index).copied()
    }

    /// 解码第 `index` 张图；空白图（宽或高为 0）返回 `None`。
    pub fn decode(&self, index: usize) -> Option<Sprite> {
        if index >= self.count {
            return None;
        }
        let rec = self.records[index];
        if rec.width == 0 || rec.height == 0 {
            return None;
        }
        let raw = self.group(index / self.group_size)?;
        let off = self.offset_in_group(index)?;
        let n = stride_of(rec.type_flag, rec.width) * rec.height as usize;
        if off + n > raw.len() {
            return None;
        }
        let rgba = to_rgba(rec.type_flag, rec.width, rec.height, &raw[off..off + n])?;
        Some(Sprite {
            width: rec.width,
            height: rec.height,
            anchor_x: rec.anchor_x,
            anchor_y: rec.anchor_y,
            rgba,
        })
    }

    /// `index` 在本组解压结果里的偏移（= 组内前若干张 raw 长度之和）。
    ///
    /// 组内图数 ≤ 1024，直接前缀和比存偏移表省 4 字节/图（全量 63 万图 ≈ 2.5 MB）。
    fn offset_in_group(&self, index: usize) -> Option<usize> {
        let lo = index / self.group_size * self.group_size;
        let mut off = 0usize;
        for i in lo..index {
            let r = self.records.get(i)?;
            off += stride_of(r.type_flag, r.width) * r.height as usize;
        }
        Some(off)
    }

    /// 取第 `g` 组的 raw 像素（解压一次，之后走缓存）。
    fn group(&self, g: usize) -> Option<Rc<Vec<u8>>> {
        if let Some((_, buf)) = self.cache.borrow().iter().find(|(gi, _)| *gi == g) {
            return Some(buf.clone());
        }
        let r = *self.groups.get(g)?;
        let comp = self
            .archive
            .read_range(&self.entry, self.data_off + r.off, r.comp_len)
            .ok()?;
        let mut raw = Vec::with_capacity(r.raw_len as usize);
        let mut dec = Decompressor::new(Cursor::new(comp), 4096);
        dec.read_to_end(&mut raw).ok()?;
        if raw.len() != r.raw_len as usize {
            return None;
        }
        let rc = Rc::new(raw);
        let mut cache = self.cache.borrow_mut();
        if cache.len() >= CACHE_GROUPS {
            cache.remove(0);
        }
        cache.push((g, rc.clone()));
        Some(rc)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::m2pk::{
        CODEC_STORE, ENTRY_LEN, HEADER_LEN as M2PK_HEADER, KIND_IMAGE, MAGIC,
        VERSION as M2PK_VERSION,
    };
    use crate::palette::PALETTE;
    use crate::wzl::Wzl;
    use std::path::Path;

    /// Go 侧 `TestDumpSamplePayload` 产出的载荷（5 张图 / 每组 2 张）：
    /// 0 号 2×2 八位（锚点 7,-44）、1 号空白、2 号 3×1 十六位、
    /// 3 号 5×3 全透明、4 号 1×1 索引 9。
    ///
    /// 为什么内嵌字节：客户端没有 brotli **编码器**（从不编码，见 Cargo.toml），
    /// 所以这份载荷只能在 Go 侧产出；内嵌之后这个测试**不依赖容器与素材**就能跑，
    /// 同时钉住两端格式（谁改协议这里立刻红）。
    ///
    ///     DUMP_PAYLOAD=1 go test ./internal/m2pk/ -run TestDumpSamplePayload -v
    #[rustfmt::skip]
    const SAMPLE: &[u8] = &[
        0x49, 0x4d, 0x47, 0x50, 0x01, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x00,
        0x0f, 0x00, 0x00, 0x00, 0x20, 0x00, 0x00, 0x00, 0x20, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00,
        0x04, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x02, 0x00, 0x02, 0x00, 0x07, 0x00, 0xd4, 0xff,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x05, 0x00, 0x03, 0x00, 0xff, 0xff, 0x02, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
        0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x8f, 0x03, 0x80, 0x01, 0x02, 0x00, 0x00, 0x03,
        0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00, 0x1f, 0x1f, 0x00, 0xf8, 0xa7, 0x01, 0x0e, 0xc0,
        0xf1, 0x07, 0x1f, 0x01, 0x00, 0xf9, 0x02, 0x00, 0x8f, 0x01, 0x80, 0x09, 0x00, 0x00, 0x00, 0x03,
    ];

    /// 把载荷包成一个只含一块的容器（`kind=3`、`codec=store`），好走正常入口。
    fn container_with(name: &str, payload: &[u8]) -> Rc<Archive> {
        let names_off = M2PK_HEADER + ENTRY_LEN;
        let data_off = (names_off + name.len() + 7) & !7;
        let mut d = vec![0u8; data_off];
        d[0..4].copy_from_slice(MAGIC);
        d[4..6].copy_from_slice(&M2PK_VERSION.to_le_bytes());
        d[6] = KIND_IMAGE;
        d[7] = CODEC_STORE;
        d[8..12].copy_from_slice(&1u32.to_le_bytes());
        d[12..16].copy_from_slice(&(names_off as u32).to_le_bytes());
        d[16..20].copy_from_slice(&(name.len() as u32).to_le_bytes());
        d[20..24].copy_from_slice(&(data_off as u32).to_le_bytes());
        let e = &mut d[M2PK_HEADER..M2PK_HEADER + ENTRY_LEN];
        e[4] = name.len() as u8;
        e[8..16].copy_from_slice(&(data_off as u64).to_le_bytes());
        e[16..20].copy_from_slice(&(payload.len() as u32).to_le_bytes());
        e[20..24].copy_from_slice(&(payload.len() as u32).to_le_bytes());
        d[names_off..names_off + name.len()].copy_from_slice(name.as_bytes());
        d.extend_from_slice(payload);
        Rc::new(Archive::from_bytes(d).expect("容器应能解析"))
    }

    #[test]
    fn 内嵌载荷_解出与直读相同的语义() {
        let lib = PackedLib::open(container_with("hum", SAMPLE), "hum")
            .unwrap()
            .expect("容器里应有 hum");
        assert_eq!(lib.len(), 5);
        assert_eq!(lib.group_size(), 2);

        // 0 号：2×2 八位 —— 数据自下而上、索引 0 透明
        let s = lib.decode(0).expect("0 号应能解");
        assert_eq!((s.width, s.height, s.anchor_x, s.anchor_y), (2, 2, 7, -44));
        let px = |x: usize, y: usize| {
            let q = (y * 2 + x) * 4;
            [s.rgba[q], s.rgba[q + 1], s.rgba[q + 2], s.rgba[q + 3]]
        };
        let [r1, g1, b1] = PALETTE[1];
        assert_eq!(px(0, 1), [r1, g1, b1, 255], "底行左 = 索引 1");
        // raw 首行是**底行**：底行 [1, 2]，顶行 [3, 0]
        let [r3, g3, b3] = PALETTE[3];
        assert_eq!(px(0, 0), [r3, g3, b3, 255], "顶行左 = 索引 3");
        assert_eq!(px(1, 0)[3], 0, "顶行右是索引 0 ⇒ 透明");
        assert_eq!(px(1, 1)[3], 255, "底行右是索引 2 ⇒ 不透明");

        // 1 号：空白（容器里记录是零值）⇒ 解不出，但**索引不塌**
        assert!(lib.decode(1).is_none());
        let r1 = lib.record(1).unwrap();
        assert_eq!((r1.width, r1.height), (0, 0));

        // 2 号：3×1 十六位（红/绿/黑）
        //
        // ⚠️ 黑（`0x0000`）现在是**透明**的：16 位图没有调色板索引，`0x0000` 就是它的
        // "背景"，不抠的话精灵会带着一块黑底贴上屏（`Mon11`/`Mon34` 那几只，见 `wzl.rs` 文件头）。
        let s2 = lib.decode(2).unwrap();
        assert_eq!(&s2.rgba[0..4], &[0xF8, 0x00, 0x00, 255]);
        assert_eq!(&s2.rgba[4..8], &[0x00, 0xFC, 0x00, 255]);
        assert_eq!(&s2.rgba[8..12], &[0x00, 0x00, 0x00, 0]);

        // 3 号：5×3 全是索引 0 ⇒ 全透明但**仍然解码成功**
        let s3 = lib.decode(3).unwrap();
        assert!(s3.rgba.chunks(4).all(|c| c[3] == 0));

        // 4 号：1×1 索引 9
        let s4 = lib.decode(4).unwrap();
        let [r9, g9, b9] = PALETTE[9];
        assert_eq!(&s4.rgba[0..4], &[r9, g9, b9, 255]);

        // 越界与不存在的库
        assert!(lib.decode(5).is_none());
        assert!(PackedLib::open(container_with("hum", SAMPLE), "nope")
            .unwrap()
            .is_none());
    }

    /// 真素材对拍（要先跑 `tools/artpack/build.sh`）：**容器与直读逐字节一致**。
    ///
    /// 这是这套载体唯一真正重要的验收：换存储不能换像素。小库**全量**比
    ///（Prguse 1851 张、Items 若干），大库抽样（Hum 14400 张取几处）。
    #[test]
    fn 真素材_容器与直读逐字节一致() {
        let Ok(dir) = std::env::var("MIR2C_DATA") else {
            eprintln!("跳过：未设置 MIR2C_DATA");
            return;
        };
        let Some(art) = art_archive() else {
            eprintln!("跳过：没有美术容器（跑 tools/artpack/build.sh，或设 MIR2_IMAGE_CONTAINER）");
            return;
        };
        // 完整性闸门：`artpack` 先把**占位索引**（名字已写、偏移全是 0）落盘，
        // 全部块写完才 Seek 回来回填 ⇒ "有条目 compSize 为 0" = 还在写/没建完。
        // 不这么判的话，跑到一半的容器会以"能查到名字、内容是 0"的形态骗过 lookup。
        if art.entries().iter().any(|e| e.comp_size == 0) {
            eprintln!("跳过：美术容器还没建完（有占位条目）—— 跑 tools/artpack/build.sh");
            return;
        }

        let dir = Path::new(&dir);
        let mut compared = 0usize;

        let full = ["Prguse", "Items", "Weapon"];
        for name in full {
            let Ok(raw) = Wzl::open(dir.join(name)) else {
                eprintln!("跳过 {name}：直读打不开");
                continue;
            };
            // 容器是**构建产物**：可能是旧的、或还没建全 ⇒ 缺库就跳过，不许假失败。
            let Some(packed) = Wzl::open_packed(art.clone(), name).unwrap() else {
                eprintln!("跳过 {name}：容器里没有（容器可能没建全）");
                continue;
            };
            assert_eq!(raw.len(), packed.len(), "{name} 图数不一致");
            for i in 0..raw.len() {
                match (raw.decode(i), packed.decode(i)) {
                    (None, None) => {}
                    (Some(a), Some(b)) => {
                        assert_eq!(
                            (a.width, a.height, a.anchor_x, a.anchor_y),
                            (b.width, b.height, b.anchor_x, b.anchor_y),
                            "{name}#{i} 记录不一致"
                        );
                        assert!(a.rgba == b.rgba, "{name}#{i} 像素不一致");
                    }
                    (a, b) => panic!(
                        "{name}#{i} 一边能解一边不能：直读 {}，容器 {}",
                        a.is_some(),
                        b.is_some()
                    ),
                }
                compared += 1;
            }
        }

        // 大库抽样（全量 14400 张没必要：组是连续切片，抽到的组已覆盖解码路径）
        if let (Ok(hum_raw), Ok(Some(hum_packed))) = (
            Wzl::open(dir.join("Hum")),
            Wzl::open_packed(art.clone(), "Hum"),
        ) {
            assert_eq!(hum_raw.len(), hum_packed.len());
            for i in [0usize, 1, 2, 31, 32, 33, 1000, 5000, 14399] {
                // ⚠️ 抽样会撞上**空白占位图**（全量 287 万张里 224 万是空白），
                // 那时两边都该是 `None` —— 不是失败。
                match (hum_raw.decode(i), hum_packed.decode(i)) {
                    (None, None) => {}
                    (Some(a), Some(b)) => assert!(a.rgba == b.rgba, "Hum#{i} 像素不一致"),
                    (a, b) => panic!(
                        "Hum#{i} 一边能解一边不能：直读 {}，容器 {}",
                        a.is_some(),
                        b.is_some()
                    ),
                }
                compared += 1;
            }
        }
        println!("容器/直读对拍：{compared} 张逐字节一致");
    }
}

//! M2PK —— 资产容器读取（**只读；写侧在 Go 的 `tools/m2pk` / `tools/wavpack`**）。
//!
//! 目前两种用途（同一个格式，靠头里的 `kind` / `codec` 区分）：
//!
//! | kind | codec | 谁写 | 内容 |
//! |---|---|---|---|
//! | 1 地图 | 0 brotli | `tools/m2pk` | `.map`（压缩收益大）|
//! | 2 音频 | 1 原样 | `tools/wavpack` | 音效/BGM + 编号表（**不压缩**：实测音频压不动，见 D-29）|
//!
//! 规格见 `docs/assets.md §5`，决策见 `docs/decisions.md D-11 / D-22`。
//! 单一真源 = 原始 `.map` 文件；容器是它的**逐字节无损**打包（可用 `m2pk verify` 回验）。
//!
//! ```text
//! Header（32 B，小端）
//!   0   magic      [4]  "M2PK"
//!   4   version    u16  1
//!   6   kind       u8   1 = 地图 / 2 = 音频
//!   7   codec      u8   0 = brotli / 1 = 原样存储
//!   8   count      u32  块数
//!   12  namesOff   u32  名字池偏移（相对文件头）
//!   16  namesLen   u32  名字池字节数
//!   20  dataOff    u32  数据区起始偏移
//!   24  reserved   [8]
//!
//! Entry（24 B × count，紧随 Header，按名字升序 ⇒ 可二分）
//!   0   nameOff    u32  相对 namesOff
//!   4   nameLen    u8
//!   5   reserved   [3]
//!   8   offset     u64  块偏移（相对文件头，8 字节对齐）
//!   16  rawSize    u32  解压后字节数（= 源文件字节数）
//!   20  compSize   u32  压缩后字节数
//! ```
//!
//! **块内容 = 源文件字节**：本模块不理解块内语义（那是 [`crate::map`] 的事），
//! 因此容器对 `.map` 的三种布局变体一视同仁。

use std::fs;
use std::io::{self, Cursor, Read};
use std::path::Path;

use brotli_decompressor::Decompressor;

/// 魔数。
pub const MAGIC: &[u8; 4] = b"M2PK";
/// 格式版本。
pub const VERSION: u16 = 1;
/// 头部长度。
pub const HEADER_LEN: usize = 32;
/// 每条索引长度。
pub const ENTRY_LEN: usize = 24;
/// `kind` = 地图容器。
pub const KIND_MAP: u8 = 1;
/// `kind` = 音频容器（音效/BGM + 编号表，见 `crate::sound`）。
pub const KIND_AUDIO: u8 = 2;
/// `codec` = brotli（地图）。
pub const CODEC_BROTLI: u8 = 0;
/// `codec` = 原样存储（音频：压不动，别浪费 CPU）。
pub const CODEC_STORE: u8 = 1;

fn bad(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.into())
}

/// 容器内一块的元信息。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Entry {
    /// 规范化名字（小写、无扩展名）。
    pub name: String,
    /// 块偏移（相对文件头）。
    pub offset: u64,
    /// 解压后字节数（= 源文件字节数）。
    pub raw_size: u32,
    /// 压缩后字节数。
    pub comp_size: u32,
}

/// 把文件名规范化为容器内的键：去掉最后一个扩展名并转小写。
///
/// 与 Go 侧 `m2pk.CanonicalName` 同一规则（D-03 / D-20）：
/// 取 basename、砍掉最后一个点之后的部分、转小写。
pub fn canonical_name(file_name: &str) -> String {
    let base = file_name.rsplit(['/', '\\']).next().unwrap_or(file_name);
    let stem = match base.rfind('.') {
        Some(i) if i > 0 => &base[..i],
        _ => base,
    };
    stem.to_ascii_lowercase()
}

/// 打开的容器（整文件读入内存：地图容器 ~9 MB 量级，见 assets.md §5）。
pub struct Archive {
    data: Vec<u8>,
    entries: Vec<Entry>,
    kind: u8,
    codec: u8,
}

impl Archive {
    /// 打开容器文件。
    pub fn open<P: AsRef<Path>>(path: P) -> io::Result<Self> {
        Self::from_bytes(fs::read(path)?)
    }

    /// 从内存构造并校验。
    pub fn from_bytes(data: Vec<u8>) -> io::Result<Self> {
        if data.len() < HEADER_LEN {
            return Err(bad(format!(
                "M2PK 太短（{} 字节，至少需 {HEADER_LEN}）",
                data.len()
            )));
        }
        if &data[0..4] != MAGIC {
            return Err(bad("M2PK 魔数不匹配"));
        }
        let version = u16::from_le_bytes([data[4], data[5]]);
        if version != VERSION {
            return Err(bad(format!("M2PK 版本不支持：{version}")));
        }
        let kind = data[6];
        if kind != KIND_MAP && kind != KIND_AUDIO {
            return Err(bad(format!("M2PK kind 不支持：{kind}")));
        }
        let codec = data[7];
        if codec != CODEC_BROTLI && codec != CODEC_STORE {
            return Err(bad(format!("M2PK codec 不支持：{codec}")));
        }
        let count = u32::from_le_bytes([data[8], data[9], data[10], data[11]]) as usize;
        let names_off = u32::from_le_bytes([data[12], data[13], data[14], data[15]]) as usize;
        let names_len = u32::from_le_bytes([data[16], data[17], data[18], data[19]]) as usize;
        let data_off = u32::from_le_bytes([data[20], data[21], data[22], data[23]]) as usize;

        if count == 0 || names_off != HEADER_LEN + ENTRY_LEN * count {
            return Err(bad(format!(
                "M2PK 索引区不自洽：count={count} namesOff={names_off}"
            )));
        }
        let names_end = names_off
            .checked_add(names_len)
            .ok_or_else(|| bad("M2PK 名字池长度溢出"))?;
        if names_end > data.len() || data_off < names_end || data_off > data.len() {
            return Err(bad("M2PK 索引区越界"));
        }

        let mut entries: Vec<Entry> = Vec::with_capacity(count);
        for i in 0..count {
            let e = &data[HEADER_LEN + ENTRY_LEN * i..][..ENTRY_LEN];
            let name_off = u32::from_le_bytes([e[0], e[1], e[2], e[3]]) as usize;
            let name_len = e[4] as usize;
            if name_len == 0 || name_off + name_len > names_len {
                return Err(bad(format!("M2PK 第 {i} 项名字越界")));
            }
            let offset = u64::from_le_bytes([e[8], e[9], e[10], e[11], e[12], e[13], e[14], e[15]]);
            let raw_size = u32::from_le_bytes([e[16], e[17], e[18], e[19]]);
            let comp_size = u32::from_le_bytes([e[20], e[21], e[22], e[23]]);
            if offset + comp_size as u64 > data.len() as u64 {
                return Err(bad(format!("M2PK 第 {i} 项数据越界")));
            }
            let name =
                String::from_utf8_lossy(&data[names_off + name_off..][..name_len]).into_owned();
            // 读侧依赖升序做二分；损坏或非本工具产物要立刻报错，不要"看起来能跑"。
            if let Some(prev) = entries.last() {
                if prev.name >= name {
                    return Err(bad(format!(
                        "M2PK 条目未按名字升序：{:?} 之后是 {:?}",
                        prev.name, name
                    )));
                }
            }
            entries.push(Entry {
                name,
                offset,
                raw_size,
                comp_size,
            });
        }
        Ok(Self {
            data,
            entries,
            kind,
            codec,
        })
    }

    /// 容器类型（[`KIND_MAP`] / [`KIND_AUDIO`]）。
    pub fn kind(&self) -> u8 {
        self.kind
    }

    /// 块编码（[`CODEC_BROTLI`] / [`CODEC_STORE`]）。
    pub fn codec(&self) -> u8 {
        self.codec
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    pub fn entries(&self) -> &[Entry] {
        &self.entries
    }

    /// 按名字查找（会把传入的名字规范化为小写）。
    pub fn lookup(&self, name: &str) -> Option<&Entry> {
        let key = name.to_ascii_lowercase();
        self.entries
            .binary_search_by(|e| e.name.as_str().cmp(key.as_str()))
            .ok()
            .map(|i| &self.entries[i])
    }

    /// 取出一块，并校验长度与 `raw_size` 一致。
    ///
    /// 块编码看容器头（[`CODEC_STORE`] 直接返回原字节，[`CODEC_BROTLI`] 才解压）。
    pub fn read(&self, e: &Entry) -> io::Result<Vec<u8>> {
        let off = e.offset as usize;
        let comp = &self.data[off..off + e.comp_size as usize];
        let mut out = Vec::with_capacity(e.raw_size as usize);
        if self.codec == CODEC_STORE {
            // 音频容器走这条：原样切片（`Cursor`/`Decompressor` 都不需要）
            out.extend_from_slice(comp);
        } else {
            let mut dec = Decompressor::new(Cursor::new(comp), 4096);
            dec.read_to_end(&mut out)
                .map_err(|err| bad(format!("M2PK 解压 {:?} 失败：{err}", e.name)))?;
        }
        if out.len() != e.raw_size as usize {
            return Err(bad(format!(
                "M2PK {:?} 解压后 {} 字节，rawSize 声明 {}",
                e.name,
                out.len(),
                e.raw_size
            )));
        }
        Ok(out)
    }

    /// 按名字解压；`Ok(None)` 表示容器里没有这一项。
    pub fn read_name(&self, name: &str) -> io::Result<Option<Vec<u8>>> {
        match self.lookup(name) {
            None => Ok(None),
            Some(e) => self.read(e).map(Some),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canonical_name_matches_go_side() {
        // 与 Go 侧 m2pk.CanonicalName 的用例一一对应，防止两端漂移
        assert_eq!(canonical_name("0.map"), "0");
        assert_eq!(canonical_name("2D.MAP"), "2d");
        assert_eq!(canonical_name("T3063~01.map"), "t3063~01");
        assert_eq!(canonical_name("ygfx1.map"), "ygfx1");
        assert_eq!(canonical_name("a_b.map"), "a_b");
        assert_eq!(canonical_name("noext"), "noext");
        assert_eq!(canonical_name("sub/dir/a.map"), "a", "带路径取 basename");
    }

    #[test]
    fn reject_bad_container() {
        assert!(Archive::from_bytes(vec![]).is_err(), "空文件应报错");
        assert!(
            Archive::from_bytes(vec![0u8; 64]).is_err(),
            "魔数错误应报错"
        );

        // 魔数/版本对，但 count 与 namesOff 不自洽
        let mut v = vec![0u8; 64];
        v[0..4].copy_from_slice(MAGIC);
        v[4..6].copy_from_slice(&VERSION.to_le_bytes());
        v[6] = KIND_MAP;
        v[7] = CODEC_BROTLI;
        v[8..12].copy_from_slice(&3u32.to_le_bytes()); // count = 3
        v[12..16].copy_from_slice(&32u32.to_le_bytes()); // namesOff 本应是 32 + 24*3
        assert!(Archive::from_bytes(v).is_err(), "索引区不自洽应报错");

        // 版本与 codec 也要拦
        let mut v = vec![0u8; 64];
        v[0..4].copy_from_slice(MAGIC);
        v[4..6].copy_from_slice(&99u16.to_le_bytes());
        assert!(Archive::from_bytes(v).is_err(), "版本不支持应报错");
    }
}

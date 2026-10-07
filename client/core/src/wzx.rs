//! `.wzx` —— WZL 图库的**索引**文件。
//!
//! 与 `.wix` 同构（见 `docs/assets.md §3.2b`），头 **48 字节**：
//!
//! ```text
//! Title        String[40]   // 41 字节，补齐到 44
//! ImageCount   u32          // 位于偏移 44
//! u32[ImageCount]           // 每项 = 对应图在 .wzl 中的字节偏移（从偏移 48 开始）
//! ```
//!
//! ⚠️ 数组**每项 4 字节**（不是 8）——原版 Delphi 里有 `Size` 字段但从未写入。

use std::fs;
use std::io;
use std::path::Path;

/// `.wzx` / `.wix` 的头部长度（`Title[40]` 补齐到 44 + `ImageCount` 4）。
pub const HEADER_LEN: usize = 48;

#[derive(Debug, Clone)]
pub struct Wzx {
    offsets: Vec<u32>,
}

impl Wzx {
    /// 从 `.wzx` 文件读取。
    pub fn open<P: AsRef<Path>>(path: P) -> io::Result<Self> {
        Self::parse(&fs::read(path)?)
    }

    pub fn parse(bytes: &[u8]) -> io::Result<Self> {
        if bytes.len() < HEADER_LEN {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!(".wzx 太短（{} 字节，至少需 {HEADER_LEN}）", bytes.len()),
            ));
        }
        let count = u32::from_le_bytes(bytes[44..48].try_into().unwrap()) as usize;
        let need = HEADER_LEN + count * 4;
        if bytes.len() < need {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!(
                    ".wzx 索引不完整：声明 {count} 项，需 {need} 字节，实有 {}",
                    bytes.len()
                ),
            ));
        }
        let mut offsets = Vec::with_capacity(count);
        for i in 0..count {
            let o = HEADER_LEN + i * 4;
            offsets.push(u32::from_le_bytes(bytes[o..o + 4].try_into().unwrap()));
        }
        Ok(Self { offsets })
    }

    pub fn len(&self) -> usize {
        self.offsets.len()
    }

    pub fn is_empty(&self) -> bool {
        self.offsets.is_empty()
    }

    /// 第 `index` 张图在 `.wzl` 中的字节偏移。
    pub fn offset(&self, index: usize) -> Option<u32> {
        self.offsets.get(index).copied()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn wzx_with(offsets: &[u32]) -> Vec<u8> {
        let mut b = vec![0u8; HEADER_LEN];
        b[44..48].copy_from_slice(&(offsets.len() as u32).to_le_bytes());
        for o in offsets {
            b.extend_from_slice(&o.to_le_bytes());
        }
        b
    }

    #[test]
    fn 解析偏移表() {
        let w = Wzx::parse(&wzx_with(&[64, 139, 4000])).unwrap();
        assert_eq!(w.len(), 3);
        assert_eq!(w.offset(0), Some(64));
        assert_eq!(w.offset(2), Some(4000));
        assert_eq!(w.offset(3), None);
    }

    #[test]
    fn 头部过短应报错() {
        assert!(Wzx::parse(&[0u8; 10]).is_err());
    }

    #[test]
    fn 索引被截断应报错() {
        let mut b = wzx_with(&[64, 139]);
        b.truncate(HEADER_LEN + 4); // 只留 1 项，但声明 2 项
        assert!(Wzx::parse(&b).is_err());
    }
}

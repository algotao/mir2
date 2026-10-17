//! `mir2-imgtool` —— 从图库（`.wzl` + `.wzx`）里按**宽度**挑图并导出 PNG。
//!
//! 存在理由：找素材。官方客户端有 800 与 1024 两套界面图（`Prguse3[690]` 那种
//! 1024×251 的底栏整图就在 `data2` 里），想知道"哪些图是 1024 宽"只能逐个库翻，
//! 而翻图库这件事必须由程序做 —— 252 个库、近 300 万张。
//!
//! 用法：
//!
//! ```text
//! cargo run -p mir2-imgtool -- -src $WS/mir2c/data2 -out ../assets/check     # 默认宽 1024
//! cargo run -p mir2-imgtool -- -src $WS/mir2c/data2 -w 800 -dry              # 只列不写
//! cargo run -p mir2-imgtool -- -src $WS/mir2c/data2 -out ../assets/check \
//!     -only Prguse:73,Prguse:74                                              # 点名导出
//! ```
//!
//! 产物命名 `<库名>_<图号>.png`（例 `Prguse_30.png`），RGBA（透明像素真的透明 ——
//! 8 位图的调色板索引 0、16 位图的 `0x0000` 都是背景，见 `core/src/wzl.rs`）。
//!
//! ⚠️ `-dry` 先跑一遍：命中数可能很多（整屏界面图都是 1024×768 量级）。

use std::fs;
use std::io::{self, Write};
use std::path::{Path, PathBuf};

use mir2_core::wzl::Wzl;

fn main() {
    let mut src: Option<PathBuf> = None;
    let mut out: Option<PathBuf> = None;
    let mut width = 1024u16;
    let mut dry = false;
    let mut only: Vec<(String, u32)> = Vec::new();
    let mut i = 1;
    let argv: Vec<String> = std::env::args().collect();
    while i < argv.len() {
        match argv[i].as_str() {
            "-src" => src = argv.get(i + 1).map(PathBuf::from),
            "-out" => out = argv.get(i + 1).map(PathBuf::from),
            "-w" => {
                width = argv
                    .get(i + 1)
                    .and_then(|s| s.parse().ok())
                    .unwrap_or(width)
            }
            "-dry" => dry = true,
            "-only" => {
                // `-only 库:图号[,库:图号…]`：点名导出，不受 `-w` 限制。
                if let Some(spec) = argv.get(i + 1) {
                    for item in spec.split(',') {
                        let Some((lib, idx)) = item.rsplit_once(':') else {
                            die(&format!("-only 里 `{item}` 不是 `库:图号`"));
                        };
                        let idx: u32 = idx
                            .parse()
                            .unwrap_or_else(|e| die(&format!("-only `{item}`：{e}")));
                        only.push((lib.to_string(), idx));
                    }
                }
            }
            other => {
                eprintln!("未知参数 {other}");
                usage();
                std::process::exit(2);
            }
        }
        i += 2;
    }
    let (Some(src), Some(out)) = (src, out) else {
        usage();
        std::process::exit(2);
    };

    let mut libs: Vec<PathBuf> = fs::read_dir(&src)
        .unwrap_or_else(|e| die(&format!("读目录 {} 失败：{e}", src.display())))
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| {
            p.extension()
                .and_then(|s| s.to_str())
                .is_some_and(|s| s.eq_ignore_ascii_case("wzl"))
        })
        .collect();
    libs.sort();

    if !dry {
        fs::create_dir_all(&out).unwrap_or_else(|e| die(&format!("建输出目录失败：{e}")));
    }

    let (mut hits, mut wrote, mut failed, mut libs_opened) = (0usize, 0usize, 0usize, 0usize);
    for path in &libs {
        let stem = path.file_stem().unwrap().to_string_lossy().into_owned();
        // 打不开的库（空壳 `.wzl`、缺 `.wzx`）跳过并说明 —— 素材集里有一批是
        // 登录器按需下载的空文件，不是错误。
        let lib = match Wzl::open(src.join(&stem)) {
            Ok(l) => l,
            Err(e) => {
                println!("{stem}: 跳过（{e}）");
                continue;
            }
        };
        libs_opened += 1;
        // 点名模式：只扫 `-only` 里点了这个库的那些图号；否则全库按 `-w` 过滤。
        let named: Vec<u32> = only
            .iter()
            .filter(|(lib, _)| *lib == stem)
            .map(|(_, idx)| *idx)
            .collect();
        let wanted = |idx: u32| named.contains(&idx);
        if !only.is_empty() && named.is_empty() {
            continue;
        }
        for idx in 0..lib.len() {
            let idx = idx as u32;
            let Some(rec) = lib.record(idx as usize) else {
                continue;
            };
            if !only.is_empty() {
                if !wanted(idx) {
                    continue;
                }
            } else if rec.width != width {
                continue;
            }
            hits += 1;
            let name = format!("{stem}_{idx}.png");
            if dry {
                println!("{stem}[{idx}] {}x{} → {name}", rec.width, rec.height);
                continue;
            }
            let Some(sp) = lib.decode(idx as usize) else {
                failed += 1;
                println!("{stem}[{idx}] 宽 {} 但解码失败 → 跳过", rec.width);
                continue;
            };
            match write_rgba(
                &out.join(&name),
                sp.width as u32,
                sp.height as u32,
                &sp.rgba,
            ) {
                Ok(()) => wrote += 1,
                Err(e) => {
                    failed += 1;
                    println!("{name}: 写失败 {e}");
                }
            }
        }
    }

    if !only.is_empty() {
        println!(
            "点名导出 {} 张（{}）→ {}，失败 {failed}",
            wrote,
            only.iter()
                .map(|(l, i)| format!("{l}[{i}]"))
                .collect::<Vec<_>>()
                .join(","),
            out.display()
        );
    } else {
        println!(
            "扫描 {} 个库（打开 {libs_opened}）→ 宽 {width} 的图 {hits} 张{}",
            libs.len(),
            if dry {
                "（dry-run，未写文件）".to_string()
            } else {
                format!("，写出 {wrote} 张到 {}，失败 {failed}", out.display())
            }
        );
    }
}

fn usage() {
    eprintln!("用法：mir2-imgtool -src <图库目录> -out <PNG 目录> [-w 宽度(默认1024)] [-dry]");
}

fn die(msg: &str) -> ! {
    eprintln!("{msg}");
    std::process::exit(1);
}

/// 写一张 32 位 PNG（RGBA，`rgba` 长度必须正好 `w*h*4`）。
///
/// 与 `e2e/src/png.rs` 同源思路：只手写必要 chunk（IHDR/IDAT/IEND）+ 纯 Rust
/// deflate，**不引第三方图像库**。区别是颜色类型 6（带 alpha）—— 界面图抠底
/// 之后才能直接铺在深色背景上看。
fn write_rgba(path: &Path, w: u32, h: u32, rgba: &[u8]) -> io::Result<()> {
    assert_eq!(
        rgba.len(),
        w as usize * h as usize * 4,
        "RGBA 缓冲尺寸不符 {w}x{h}"
    );

    let mut out = vec![0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A];
    let mut ihdr = Vec::with_capacity(13);
    ihdr.extend_from_slice(&w.to_be_bytes());
    ihdr.extend_from_slice(&h.to_be_bytes());
    ihdr.extend_from_slice(&[8, 6, 0, 0, 0]); // 8 位/通道、颜色类型 6 = RGBA、无隔行
    chunk(&mut out, b"IHDR", &ihdr);

    let stride = w as usize * 4;
    let mut raw = Vec::with_capacity(rgba.len() + h as usize);
    for y in 0..h as usize {
        raw.push(0); // filter = None（不做预测）
        raw.extend_from_slice(&rgba[y * stride..(y + 1) * stride]);
    }
    let mut z = flate2::write::ZlibEncoder::new(Vec::new(), flate2::Compression::default());
    z.write_all(&raw)?;
    chunk(&mut out, b"IDAT", &z.finish()?);
    chunk(&mut out, b"IEND", &[]);
    fs::write(path, &out)
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

/// PNG 用的 CRC-32（IEEE 多项式）。十几行自己写，不为它引依赖。
struct Crc(u32);

impl Crc {
    fn new() -> Self {
        Self(0xFFFF_FFFF)
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
        let mut c = super::Crc::new();
        c.update(b"123456789");
        assert_eq!(c.finish(), 0xCBF4_3926);
    }

    #[test]
    fn 写出的png头与块齐全() {
        let dir = std::env::temp_dir().join("mir2-imgtool-test");
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("t.png");
        super::write_rgba(&p, 1, 1, &[1, 2, 3, 255]).unwrap();
        let b = std::fs::read(&p).unwrap();
        assert_eq!(&b[0..8], &[0x89, b'P', b'N', b'G', 0x0D, 0x0A, 0x1A, 0x0A]);
        assert_eq!(&b[12..16], b"IHDR");
        assert_eq!(b[25], 6, "颜色类型该是 6（RGBA）");
        assert!(b.windows(4).any(|w| w == b"IDAT"));
        assert!(b.windows(4).any(|w| w == b"IEND"));
    }
}

//! 把 `.wzl` 图库里的图导成 **BMP**，用来"找素材"（人眼比对，而不是猜图号）。
//!
//! 用法：
//! ```text
//! cargo run -p mir2-core --example wzldump -- <素材目录> <图库名> <起始图号> <结束图号> <输出目录>
//! cargo run -p mir2-core --example wzldump -- /path/to/mir2c/data Prguse 0 90 /tmp/ui
//! ```
//!
//! # 为什么导 BMP 而不是 PNG
//!
//! 这个 crate **没有 PNG 编码器**（客户端是直接画到 SDL 画布上的）。BMP 可以手写
//! 头 + 逐行像素，零依赖；macOS 上用 `sips -s format png` 一转就能看。
//! 透明像素**合到浅灰棋盘上**（不然全透明的地方在深色看图器里什么都看不见）。
//!
//! 输出文件名带锚点与尺寸：`<图号>_<宽>x<高>_ax<锚点x>_ay<锚点y>.bmp` ——
//! 拼窗口时就是靠锚点对齐的（见 `mir2_core::wzl::Sprite`）。

use std::env;
use std::fs;
use std::path::PathBuf;

use mir2_core::wzl::Wzl;

fn main() {
    let a: Vec<String> = env::args().collect();
    if a.len() < 6 {
        eprintln!("用法: wzldump <素材目录> <图库名> <起始> <结束> <输出目录>");
        std::process::exit(2);
    }
    let dir = PathBuf::from(&a[1]);
    let lib = &a[2];
    let start: usize = a[3].parse().expect("起始图号");
    let end: usize = a[4].parse().expect("结束图号");
    let out = PathBuf::from(&a[5]);
    fs::create_dir_all(&out).expect("建输出目录");

    let w = Wzl::open(dir.join(lib)).unwrap_or_else(|e| panic!("打开 {lib}.wzl: {e}"));
    println!("{lib}: {} 张图", w.len());

    // `only-files` 模式：把**指定图号**逐个存成自己的文件（命名同上面的导出）。
    //
    // 与 `--only`（拼一张网格）的区别：这个适合"要一批散图给别人看"
    //（比如把每把武器的外观单独导出成 PNG 让你认哪把是木剑）。
    if let Some(pos) = env::args().position(|x| x == "--only-files") {
        let all: Vec<String> = env::args().collect();
        let want: Vec<usize> = all[pos + 1]
            .split(',')
            .filter_map(|x| x.trim().parse().ok())
            .collect();
        let mut wrote = 0;
        for i in want {
            let Some(sp) = w.decode(i) else {
                println!("  [{i}] 取不到");
                continue;
            };
            if sp.width == 0 || sp.height == 0 {
                println!("  [{i}] 空图");
                continue;
            }
            let name = format!(
                "{i:05}_{}x{}_ax{}_ay{}.bmp",
                sp.width, sp.height, sp.anchor_x, sp.anchor_y
            );
            write_bmp(&out.join(name), sp.width as u32, sp.height as u32, &sp.rgba)
                .expect("写 BMP");
            wrote += 1;
        }
        println!("逐个导出 {wrote} 张 → {}", out.display());
        return;
    }

    // `avg` 模式：**逐张算平均色**并打印 `图号 宽 高 R G B 不透明像素数`。
    //
    // 为什么需要它：图库动辄几万张，"哪一块是浅棕色的木剑"靠一张张翻不现实。
    // 先把每张的主色算出来，再用一行 awk/python 按颜色筛（木剑 ≈ 浅棕 R>G>B、R-B 大），
    // 候选就从几万张缩到几十张 —— 然后再 `--only` 拼出来看。
    if env::args().any(|x| x == "--avg") {
        for i in start..end.min(w.len()) {
            let Some(sp) = w.decode(i) else { continue };
            if sp.width == 0 || sp.height == 0 {
                continue;
            }
            let (mut r, mut g, mut b, mut n) = (0u64, 0u64, 0u64, 0u64);
            for px in sp.rgba.chunks(4) {
                if px[3] < 128 {
                    continue; // 只算不透明的像素（半透明描边会把主色拉黑）
                }
                r += px[0] as u64;
                g += px[1] as u64;
                b += px[2] as u64;
                n += 1;
            }
            if n == 0 {
                continue;
            }
            println!(
                "{i} {} {} {} {} {} {}",
                sp.width,
                sp.height,
                r / n,
                g / n,
                b / n,
                n
            );
        }
        return;
    }

    // `only` 模式：只看**指定图号**（逗号分隔，拼一张网格）—— 精挑细看用。
    // 用法：... Prguse 0 0 /tmp/ui --only 384,387,360
    if let Some(pos) = env::args().position(|x| x == "--only") {
        let all: Vec<String> = env::args().collect();
        let want: Vec<usize> = all[pos + 1]
            .split(',')
            .filter_map(|x| x.trim().parse().ok())
            .collect();
        let mut cells: Vec<(usize, mir2_core::wzl::Sprite)> = Vec::new();
        for i in want {
            match w.decode(i) {
                Some(sp) if sp.width > 0 => cells.push((i, sp)),
                _ => println!("  [{i}] 取不到"),
            }
        }
        let cw = cells.iter().map(|c| c.1.width as u32).max().unwrap_or(1) + 8;
        let ch = cells.iter().map(|c| c.1.height as u32).max().unwrap_or(1) + 8;
        let (sw, sh) = (cw * cells.len() as u32, ch);
        let mut buf = vec![0u8; (sw as usize) * (sh as usize) * 4];
        for px in buf.chunks_mut(4) {
            px[0] = 40;
            px[1] = 40;
            px[2] = 40;
            px[3] = 255;
        }
        for (n, (i, sp)) in cells.iter().enumerate() {
            let ox = n as u32 * cw + 4;
            for y in 0..sp.height as u32 {
                for x in 0..sp.width as u32 {
                    let so = ((y * sp.width as u32 + x) * 4) as usize;
                    let a = sp.rgba[so + 3] as u32;
                    if a == 0 {
                        continue;
                    }
                    let dofs = ((y * sw + ox + x) * 4) as usize;
                    for c in 0..3 {
                        buf[dofs + c] = ((sp.rgba[so + c] as u32 * a
                            + buf[dofs + c] as u32 * (255 - a))
                            / 255) as u8;
                    }
                }
            }
            println!("  第 {} 格 = [{i}] {}x{}", n + 1, sp.width, sp.height);
        }
        let out = out.join("_only.bmp");
        write_bmp(&out, sw, sh, &buf).expect("写 BMP");
        println!("→ {}", out.display());
        return;
    }

    // `list` 模式：只列图号 + 尺寸（找"某形状的窗口"最快 —— 先筛尺寸再看图）。
    if env::args().any(|x| x == "--list") {
        for i in start..end.min(w.len()) {
            let Some(rec) = w.record(i) else { continue };
            println!("  [{i}] {}x{}", rec.width, rec.height);
        }
        return;
    }

    // `sheet` 模式：把一批图拼成一张**规整网格**（每行 6 张，格子取本批最大尺寸），
    // 并按下标顺序打印，这样一条 `read_file` 就能看几十张 —— 找素材靠它。
    if env::args().any(|x| x == "--sheet") {
        write_sheet(&w, start, end, &out.join("_sheet.bmp"), 6, 400);
        return;
    }
    let mut wrote = 0;
    for i in start..end.min(w.len()) {
        let Some(raw) = w.decode(i) else {
            println!("  [{i}] 解不出来");
            continue;
        };
        if raw.width == 0 || raw.height == 0 {
            println!("  [{i}] 空图（{}x{}）", raw.width, raw.height);
            continue;
        }
        if raw.rgba.len() != raw.width as usize * raw.height as usize * 4 {
            println!("  [{i}] 像素长度不对（{} 字节）", raw.rgba.len());
            continue;
        }
        let name = format!(
            "{i:04}_{}x{}_ax{}_ay{}.bmp",
            raw.width, raw.height, raw.anchor_x, raw.anchor_y
        );
        write_bmp(&out.join(name), raw.width as u32, raw.height as u32, &raw.rgba).expect("写 BMP");
        wrote += 1;
    }
    println!("导出 {wrote} 张 → {}", out.display());
}

/// 写 24 位 BMP（透明合到浅灰棋盘：不透明的地方照原色，透明的地方看出"哪儿是空的"）。
fn write_bmp(path: &PathBuf, w: u32, h: u32, rgba: &[u8]) -> std::io::Result<()> {
    let row_pad = (4 - (w as usize * 3) % 4) % 4;
    let row = w as usize * 3 + row_pad;
    let pixels = row * h as usize;
    let mut out = Vec::with_capacity(54 + pixels);

    // BITMAPFILEHEADER
    out.extend_from_slice(b"BM");
    out.extend_from_slice(&((54 + pixels) as u32).to_le_bytes());
    out.extend_from_slice(&0u32.to_le_bytes());
    out.extend_from_slice(&54u32.to_le_bytes());
    // BITMAPINFOHEADER
    out.extend_from_slice(&40u32.to_le_bytes());
    out.extend_from_slice(&(w as i32).to_le_bytes());
    out.extend_from_slice(&(h as i32).to_le_bytes()); // 正高度 = 自下而上
    out.extend_from_slice(&1u16.to_le_bytes());
    out.extend_from_slice(&24u16.to_le_bytes());
    out.extend_from_slice(&0u32.to_le_bytes());
    out.extend_from_slice(&(pixels as u32).to_le_bytes());
    out.extend_from_slice(&[0u8; 16]);

    for y in (0..h as usize).rev() {
        for x in 0..w as usize {
            let o = (y * w as usize + x) * 4;
            let (r, g, b, a) = (rgba[o], rgba[o + 1], rgba[o + 2], rgba[o + 3]);
            // 棋盘：8px 一格，浅灰/白
            let chk = if ((x / 8) + (y / 8)) % 2 == 0 { 200u8 } else { 235u8 };
            let mix = |c: u8, bg: u8| ((c as u32 * a as u32 + bg as u32 * (255 - a as u32)) / 255) as u8;
            out.push(mix(b, chk));
            out.push(mix(g, chk));
            out.push(mix(r, chk));
        }
        for _ in 0..row_pad {
            out.push(0);
        }
    }
    fs::write(path, out)
}

/// 把 `[start,end)` 拼成一张网格 BMP（格子 = 本批最大宽/高 + 8 边距）。
///
/// 只收 `max_dim` 以内的图（大背景图会把网格撑爆 —— 那种单独导）。
fn write_sheet(w: &Wzl, start: usize, end: usize, path: &PathBuf, per_row: usize, max_dim: u16) {
    let mut cells: Vec<(usize, mir2_core::wzl::Sprite)> = Vec::new();
    for i in start..end.min(w.len()) {
        let Some(sp) = w.decode(i) else { continue };
        if sp.width == 0 || sp.height == 0 {
            continue;
        }
        if sp.width > max_dim || sp.height > max_dim {
            println!("  [{i}] 跳过（{}x{} 太大，单独导）", sp.width, sp.height);
            continue;
        }
        cells.push((i, sp));
    }
    if cells.is_empty() {
        println!("没有可拼的图");
        return;
    }
    let cw = cells.iter().map(|c| c.1.width as u32).max().unwrap() + 8;
    let ch = cells.iter().map(|c| c.1.height as u32).max().unwrap() + 8;
    let rows = (cells.len() + per_row - 1) / per_row;
    let (sw, sh) = (cw * per_row as u32, ch * rows as u32);
    // 合成在深灰底上（UI 素材多为浅色/半透明边，深底看得清）
    let mut buf = vec![0u8; (sw as usize) * (sh as usize) * 4];
    for px in buf.chunks_mut(4) {
        px[0] = 40;
        px[1] = 40;
        px[2] = 40;
        px[3] = 255;
    }
    let mut line = String::new();
    for (n, (i, sp)) in cells.iter().enumerate() {
        let (col, row) = (n % per_row, n / per_row);
        let (ox, oy) = (col as u32 * cw + 4, row as u32 * ch + 4);
        for y in 0..sp.height as u32 {
            for x in 0..sp.width as u32 {
                let so = ((y * sp.width as u32 + x) * 4) as usize;
                let a = sp.rgba[so + 3] as u32;
                if a == 0 {
                    continue;
                }
                let dofs = (((oy + y) * sw + ox + x) * 4) as usize;
                for c in 0..3 {
                    buf[dofs + c] = ((sp.rgba[so + c] as u32 * a
                        + buf[dofs + c] as u32 * (255 - a))
                        / 255) as u8;
                }
            }
        }
        line.push_str(&format!("{i:>4}"));
        if (n + 1) % per_row == 0 {
            line.push('\n');
        }
    }
    println!("网格 {per_row} 列，格 {cw}x{ch}，下标顺序：\n{line}");
    write_bmp(path, sw, sh, &buf).expect("写网格 BMP");
    println!("网格 → {}", path.display());
}

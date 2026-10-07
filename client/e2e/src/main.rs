//! `mir2-e2e` —— 无头 CLI（**禁止依赖 SDL3**，plan §4.2 / C-8）。
//!
//! 与 `client/app` **共用 `client/core`**：地图解析、绘制清单（`visible_tiles`）、
//! 落点规则（`top_y`/`left_x`）、混合语义（`blend::screen_source`）都来自 core，
//! 这里只是把它渲染到内存而不是 SDL 窗口。CI 门禁：
//! `cargo tree -p mir2-e2e | grep -q sdl3 && exit 1`。
//!
//! ```text
//! mir2-e2e render  -map 0 -cam 340,331 -out /tmp/a.png
//! mir2-e2e panself -map 0 -cam 340,331
//! ```
//!
//! `panself` 是这套东西目前最值钱的部分：同一块地图在两个只差平移的机位各渲染一遍，
//! 校验**重叠区逐像素相同**。它一次就能抓住"该画却没画"这类**只在镜头移动时暴露**
//! 的漏画 —— 2026-10-07 的"高树随镜头凭空出现"就是靠人工比对两张截图才发现的，
//! 本命令把它变成一条自动判据。

mod canvas;
mod png;

use std::path::{Path, PathBuf};

use canvas::{Canvas, Tiles};
use mir2_core::m2pk::Archive;
use mir2_core::map::{Map, LAYERS_ALL, UNIT_X, UNIT_Y};

/// 默认视口：与 `client/app` 当前的地图可视区一致（1024 宽 × 722 高），
/// 这样无头产物可以和截图直接对照。
const DEFAULT_W: i32 = 1024;
const DEFAULT_H: i32 = 722;
/// 背景色与 app 的 `C_BG` 一致（同样为了能直接对照）。
const BG: [u8; 3] = [10, 14, 28];
/// `panself` 默认下移的行数（2 行 = 64px）。
const DEFAULT_PAN_ROWS: i32 = 2;

fn main() {
    let argv: Vec<String> = std::env::args().skip(1).collect();
    let code = match argv.first().map(String::as_str) {
        Some("render") => run(&argv[1..], cmd_render),
        Some("panself") => run(&argv[1..], cmd_panself),
        Some("-h" | "--help" | "help") | None => {
            usage();
            0
        }
        Some(other) => {
            eprintln!("未知子命令：{other}\n");
            usage();
            2
        }
    };
    std::process::exit(code);
}

fn run(argv: &[String], f: fn(&Args) -> Result<(), String>) -> i32 {
    match Args::parse(argv).and_then(|a| f(&a)) {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("[e2e] 失败：{e}");
            1
        }
    }
}

// ---------- 参数 ----------

struct Args {
    map: String,
    cam: (i32, i32),
    out: Option<PathBuf>,
    w: i32,
    h: i32,
    layers: u8,
    ani: u32,
    rows: i32,
    grid: bool,
    assets: Option<PathBuf>,
    container: Option<PathBuf>,
}

impl Args {
    fn parse(argv: &[String]) -> Result<Self, String> {
        let (mut map, mut cam, mut out) = (None, None, None);
        let (mut w, mut h) = (DEFAULT_W, DEFAULT_H);
        let (mut layers, mut ani, mut rows) = (LAYERS_ALL, 0u32, DEFAULT_PAN_ROWS);
        let (mut grid, mut assets, mut container) = (false, None, None);

        let mut i = 0;
        while i < argv.len() {
            let key = argv[i].clone();
            let mut val = |what: &str| -> Result<String, String> {
                i += 1;
                argv.get(i)
                    .cloned()
                    .ok_or_else(|| format!("{key} 后面要跟{what}"))
            };
            match key.as_str() {
                "-map" => map = Some(val("地图名")?),
                "-cam" => cam = Some(parse_cam(&val("坐标")?)?),
                "-out" => out = Some(PathBuf::from(val("路径")?)),
                "-w" => w = parse_num(&val("宽度")?, "-w")?,
                "-h" => h = parse_num(&val("高度")?, "-h")?,
                "-ani" => ani = parse_num::<u32>(&val("帧计数")?, "-ani")?,
                "-rows" => rows = parse_num(&val("行数")?, "-rows")?,
                "-layers" => layers = parse_layers(&val("层掩码")?)?,
                "-assets" => assets = Some(PathBuf::from(val("目录")?)),
                "-container" => container = Some(PathBuf::from(val("文件")?)),
                "-grid" => grid = true,
                other => return Err(format!("未知参数 {other}（-h 看用法）")),
            }
            i += 1;
        }

        if w <= 0 || h <= 0 {
            return Err(format!("视口尺寸不合法：{w}x{h}"));
        }
        if rows <= 0 {
            return Err("-rows 必须为正".into());
        }
        Ok(Args {
            map: map.ok_or("缺少 -map <名字>（容器里的地图名，如 0）")?,
            cam: cam.ok_or("缺少 -cam X,Y")?,
            out,
            w,
            h,
            layers,
            ani,
            rows,
            grid,
            assets,
            container,
        })
    }
}

fn parse_num<T: std::str::FromStr>(s: &str, key: &str) -> Result<T, String>
where
    T::Err: std::fmt::Display,
{
    s.parse::<T>()
        .map_err(|e| format!("{key} 取值非法（{s}）：{e}"))
}

fn parse_cam(s: &str) -> Result<(i32, i32), String> {
    let (a, b) = s
        .split_once(',')
        .ok_or_else(|| format!("-cam 要写成 X,Y（收到 {s}）"))?;
    Ok((
        parse_num(a.trim(), "-cam X")?,
        parse_num(b.trim(), "-cam Y")?,
    ))
}

/// `GMF` 里出现哪些字母就开哪些层（顺序随意，重复无害）。
fn parse_layers(s: &str) -> Result<u8, String> {
    let mut m = 0u8;
    for c in s.chars() {
        m |= match c.to_ascii_uppercase() {
            'G' => 1,
            'M' => 2,
            'F' => 4,
            other => return Err(format!("-layers 只认 G/M/F（收到 {other}）")),
        };
    }
    if m == 0 {
        return Err("-layers 至少要开一层".into());
    }
    Ok(m)
}

// ---------- 上下文 ----------

fn context(a: &Args) -> Result<(PathBuf, Archive), String> {
    let dir = a
        .assets
        .clone()
        .or_else(mir2_core::paths::asset_dir)
        .ok_or("找不到美术目录：设 $MIR2_ASSET_DIR，或用 -assets 指定")?;
    let cpath = a
        .container
        .clone()
        .or_else(mir2_core::paths::map_container)
        .ok_or("找不到地图容器：先跑 tools/m2pk/build.sh，或用 -container 指定")?;
    let ar = Archive::open(&cpath).map_err(|e| format!("打开容器 {}：{e}", cpath.display()))?;
    Ok((dir, ar))
}

struct Rendered {
    canvas: Canvas,
    draws: usize,
    drawn: usize,
    decoded: usize,
}

fn render(m: &Map, dir: &Path, cam: (i32, i32), a: &Args) -> Rendered {
    // 比视口多取一格（core 自己在左/上还会多扫），越界像素由合成器丢弃
    // （`i32::div_ceil` 在 stable 上还没放开，手算）
    let cols = (a.w + UNIT_X - 1) / UNIT_X + 1;
    let rows = (a.h + UNIT_Y - 1) / UNIT_Y + 1;
    let mut draws = Vec::new();
    m.visible_tiles(cam.0, cam.1, cols, rows, a.ani, &mut draws);
    // 层显隐：与 app 的 CTRL+1/2/3 用**同一个**掩码语义（core::Layer::bit）
    draws.retain(|d| a.layers & d.layer.bit() != 0);

    let mut tiles = Tiles::new(dir);
    let mut cv = Canvas::new(a.w, a.h, BG);
    let drawn = canvas::render(&draws, &mut tiles, &mut cv);
    Rendered {
        canvas: cv,
        draws: draws.len(),
        drawn,
        decoded: tiles.decoded,
    }
}

// ---------- 子命令 ----------

fn cmd_render(a: &Args) -> Result<(), String> {
    let (dir, archive) = context(a)?;
    let m = Map::load(&archive, &a.map).map_err(|e| format!("加载地图 {}：{e}", a.map))?;
    let mut r = render(&m, &dir, a.cam, a);
    if a.grid {
        r.canvas.draw_grid([40, 48, 70]);
    }
    dump(a, &r.canvas)?;
    println!(
        "[render] 地图 {} （{}x{}，{}/格） 机位 {:?} 视口 {}x{}\n\
         \x20        绘制指令 {} 条、实画 {} 条、解码 {} 张{}",
        a.map,
        m.width,
        m.height,
        m.cell_len,
        a.cam,
        a.w,
        a.h,
        r.draws,
        r.drawn,
        r.decoded,
        if a.grid {
            "\n        格网已叠加"
        } else {
            ""
        }
    );
    println!("        已写 {}", a.out.as_ref().unwrap().display());
    Ok(())
}

fn dump(a: &Args, cv: &Canvas) -> Result<(), String> {
    let path = a.out.as_ref().ok_or("缺少 -out FILE.png")?;
    png::write_rgb(path, a.w as u32, a.h as u32, &cv.to_rgb())
        .map_err(|e| format!("写 {}：{e}", path.display()))
}

fn cmd_panself(a: &Args) -> Result<(), String> {
    let (dir, archive) = context(a)?;
    let m = Map::load(&archive, &a.map).map_err(|e| format!("加载地图 {}：{e}", a.map))?;
    let dy = a.rows * UNIT_Y;
    if dy >= a.h {
        return Err(format!(
            "-rows {} 太大：位移 {dy}px 不小于视口高 {}px",
            a.rows, a.h
        ));
    }

    let up = render(&m, &dir, a.cam, a);
    let down = render(&m, &dir, (a.cam.0, a.cam.1 + a.rows), a);

    let (diffs, samples) = diff_shifted(&up.canvas, &down.canvas, dy);
    let overlap = (a.h - dy) as i64 * i64::from(a.w);
    println!(
        "[panself] 地图 {}  机位 {:?} → ({},{})  视口 {}x{}\n\
         \x20         重叠区 {}x{} = {} 像素，逐像素比对：{}",
        a.map,
        a.cam,
        a.cam.0,
        a.cam.1 + a.rows,
        a.w,
        a.h,
        a.w,
        a.h - dy,
        overlap,
        if diffs == 0 {
            "一致 ✓".to_string()
        } else {
            format!(
                "**{diffs} 个不一致**（{:.3}%）",
                100.0 * diffs as f64 / overlap as f64
            )
        }
    );

    if diffs == 0 {
        return Ok(());
    }
    println!("  两者内容应当**只差平移**（下移 {dy}px）。不一致说明有该画没画的图块。");
    println!("  前 {} 处：", samples.len());
    for (x, y, pa, pb) in &samples {
        println!("    ({x:4},{y:4})  上机位={pa:?}  下机位={pb:?}");
    }
    if let Some(p) = &a.out {
        let (pa, pb) = (with_suffix(p, "up"), with_suffix(p, "down"));
        png::write_rgb(&pa, a.w as u32, a.h as u32, &up.canvas.to_rgb())
            .map_err(|e| format!("写 {}：{e}", pa.display()))?;
        png::write_rgb(&pb, a.w as u32, a.h as u32, &down.canvas.to_rgb())
            .map_err(|e| format!("写 {}：{e}", pb.display()))?;
        println!("  两份都留下来了：{} / {}", pa.display(), pb.display());
    }
    Err(format!("{diffs} 个像素不一致"))
}

fn with_suffix(p: &Path, tag: &str) -> PathBuf {
    let stem = p
        .file_stem()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_default();
    let ext = p
        .extension()
        .map(|s| s.to_string_lossy().to_string())
        .unwrap_or_else(|| "png".into());
    p.with_file_name(format!("{stem}.{tag}.{ext}"))
}

/// 一处不一致的样本：(视口 x, 视口 y, 上机位像素, 下机位像素)
type Diff = (i32, i32, [u8; 3], [u8; 3]);

/// 比较"上机位"与"下机位"的重叠区：`up(x, y)` 应等于 `down(x, y - dy)`。
///
/// 返回（不一致像素数，最多 8 处样本）。
fn diff_shifted(up: &Canvas, down: &Canvas, dy: i32) -> (usize, Vec<Diff>) {
    let mut n = 0;
    let mut samples = Vec::new();
    for y in dy..up.h {
        for x in 0..up.w {
            let a = up.pixel(x, y);
            let b = down.pixel(x, y - dy);
            if a != b {
                n += 1;
                if samples.len() < 8 {
                    samples.push((x, y, a, b));
                }
            }
        }
    }
    (n, samples)
}

fn usage() {
    println!(
        "mir2-e2e —— MIR2 无头 CLI（不依赖 SDL3）\n\
         \n\
         用法：\n\
         \x20 mir2-e2e render  -map <名字> -cam X,Y -out FILE.png [选项]\n\
         \x20 mir2-e2e panself -map <名字> -cam X,Y             [选项]\n\
         \n\
         选项：\n\
         \x20 -w N -h N        视口尺寸（默认 {DEFAULT_W}x{DEFAULT_H}，与 app 的地图区一致）\n\
         \x20 -layers GMF      只画指定层：G=地表 M=中间 F=前景（默认三层）\n\
         \x20 -ani N           前景动画的节拍计数（官方 50ms 一加；默认 0）\n\
         \x20 -rows N          panself：下移几行（默认 {DEFAULT_PAN_ROWS}）\n\
         \x20 -grid            render：叠加格网（每题 48x32 一条）\n\
         \x20 -out FILE.png    panself：不一致时把两份都写出来\n\
         \x20 -assets DIR      美术目录（默认 $MIR2_ASSET_DIR → $MIR2C_DATA → 仓库旁 mir2c/data）\n\
         \x20 -container FILE  地图容器（默认 $MIR2_MAP_CONTAINER → assets/map/maps.m2pk）"
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cv(w: i32, h: i32, f: impl Fn(i32, i32) -> [u8; 3]) -> Canvas {
        let mut c = Canvas::new(w, h, [0, 0, 0]);
        for y in 0..h {
            for x in 0..w {
                c.fill_rect(x, y, 1, 1, f(x, y));
            }
        }
        c
    }

    #[test]
    fn identical_shift_passes() {
        // down 就是 up 上移 dy：up(x,y) == down(x, y-dy) 应处处成立
        let dy = 2;
        let up = cv(8, 8, |x, y| [x as u8, y as u8, 0]);
        let down = cv(8, 8, |x, y| [x as u8, (y + dy) as u8, 0]);
        assert_eq!(diff_shifted(&up, &down, dy).0, 0);
    }

    #[test]
    fn single_pixel_difference_is_caught() {
        // 反向自检：只差一个像素也必须报出来，否则这条判据是空的
        let up = cv(8, 8, |x, y| [x as u8, y as u8, 0]);
        let mut down = cv(8, 8, |x, y| [x as u8, (y + 1) as u8, 0]);
        down.fill_rect(3, 4, 1, 1, [255, 0, 0]);
        let (n, s) = diff_shifted(&up, &down, 1);
        assert_eq!(n, 1);
        assert_eq!(s[0].0, 3);
        assert_eq!(s[0].1, 5, "上机位的坐标应是 3,5（= 下机位的 4 + dy）");
    }

    /// 真实素材回归：**平移自检**。需要 `tools/m2pk/build.sh` 与 `mir2c` 素材。
    ///
    /// 素材/容器缺失时**跳过**（与 core 的 `real_container_if_present` 同一约定）。
    /// 这条测试的价值：它守的是"该画却没画"这类**只在镜头移动时暴露**的漏画 ——
    /// 2026-10-07 的 `FRONT_ROW_MARGIN`（前景少扫 35 行）就是这么被人工比对发现的，
    /// 现在它是一条自动判据（把 margin 改回 1 这条测试立刻红）。
    #[test]
    fn real_pan_selfcheck() {
        let (Some(dir), Some(container)) = (
            mir2_core::paths::asset_dir(),
            mir2_core::paths::map_container(),
        ) else {
            eprintln!("跳过：找不到素材或容器（先跑 tools/m2pk/build.sh）");
            return;
        };
        let archive = Archive::open(&container).expect("容器应可打开");
        let m = Map::load(&archive, "0").expect("0.map 应可解析");
        let a = Args {
            map: "0".into(),
            // 视口取小一点：判据与尺寸无关，跑得快更重要
            cam: (340, 331),
            out: None,
            w: 320,
            h: 240,
            layers: LAYERS_ALL,
            ani: 0,
            rows: 2,
            grid: false,
            assets: Some(dir.clone()),
            container: Some(container),
        };
        let up = render(&m, &dir, a.cam, &a);
        let down = render(&m, &dir, (a.cam.0, a.cam.1 + a.rows), &a);
        let (n, s) = diff_shifted(&up.canvas, &down.canvas, a.rows * UNIT_Y);
        let head = &s[..s.len().min(3)];
        assert_eq!(n, 0, "平移后重叠区必须逐像素相同；前几处：{head:?}");
    }

    #[test]
    fn layers_parser() {
        assert_eq!(parse_layers("GMF").unwrap(), LAYERS_ALL);
        assert_eq!(parse_layers("mf").unwrap(), 6);
        assert!(parse_layers("X").is_err());
        assert!(parse_layers("").is_err());
    }

    #[test]
    fn cam_parser() {
        assert_eq!(parse_cam("340,331").unwrap(), (340, 331));
        assert!(parse_cam("340").is_err());
    }
}

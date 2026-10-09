//! 无头**软件合成器**：把 core 给出的绘制指令画进一块 RGB 缓冲。
//!
//! 定位、层级、混合**全部取自 core**（[`TileDraw::top_y`] / [`TileDraw::left_x`] /
//! [`mir2_core::blend::screen_source`]），与 `client/app` 的 SDL 路径**同源**；
//! 这里只是"没有 GPU 时的那条腿"。plan §4.2 / R-10 明确要求两个产物不许走两条实现路径。
//!
//! ⚠️ 唯一与 app 不同的一点：app 把混合交给 GPU（`BLEND` / 预乘两种模式），
//! 这里用**同一公式**在 CPU 上算。公式同形（推导见 `mir2_core::blend`），
//! 差别只可能是整数舍入，不会改变"画了什么"。

use std::collections::HashMap;
use std::path::{Path, PathBuf};

use mir2_core::blend::screen_source;
use mir2_core::map::{Lib, TileDraw, UNIT_X, UNIT_Y};
use mir2_core::wzl::{Sprite, Wzl};

/// 一张备好的图块。混合件的 `px` **已经过 [`screen_source`]**，可直接按预乘公式合成。
struct Tile {
    w: i32,
    h: i32,
    anchor_x: i16,
    anchor_y: i16,
    px: Vec<u8>,
    screen: bool,
}

/// 图块仓库：按需打开图库、解码、缓存。
///
/// 缓存键含 `blend` —— 同一张图在"混合/不混合"下要用的像素不同（与 app 的贴图缓存同理）。
pub struct Tiles {
    dir: PathBuf,
    libs: HashMap<String, Option<Wzl>>,
    cache: HashMap<(Lib, u8, u16, bool), Option<Tile>>,
    /// 真正解码出来的张数（供 `render` 汇报，便于发现"整库打不开"这类问题）。
    pub decoded: usize,
}

impl Tiles {
    pub fn new(dir: &Path) -> Self {
        Tiles {
            dir: dir.to_path_buf(),
            libs: HashMap::new(),
            cache: HashMap::new(),
            decoded: 0,
        }
    }

    /// 取（必要时解码）该指令对应的图块；空壳图返回 `None`（原版大量存在）。
    ///
    /// Hmm —— 这里**不做视口剔除**：剔除是渲染端的优化，而合成器对越界像素
    /// 本来就会丢弃（结果不变）。少一条分支，自检就少一个可能出错的地方。
    fn get(&mut self, d: &TileDraw) -> Option<&Tile> {
        let key = (d.lib, d.area, d.index, d.blend);
        if !self.cache.contains_key(&key) {
            let t = self.load(d);
            if t.is_some() {
                self.decoded += 1;
            }
            self.cache.insert(key, t);
        }
        self.cache.get(&key)?.as_ref()
    }

    fn load(&mut self, d: &TileDraw) -> Option<Tile> {
        let name = d.lib.file_name(d.area);
        let path = self.dir.join(&name);
        let lib = self
            .libs
            .entry(name)
            .or_insert_with(|| Wzl::open(&path).ok())
            .as_ref()?;
        let s: Sprite = lib.decode(d.index as usize)?;
        if s.is_empty() {
            return None;
        }
        Some(Tile {
            w: s.width as i32,
            h: s.height as i32,
            anchor_x: s.anchor_x,
            anchor_y: s.anchor_y,
            px: if d.blend {
                screen_source(&s.rgba)
            } else {
                s.rgba
            },
            screen: d.blend,
        })
    }
}

/// 一块 RGB 画布。
pub struct Canvas {
    pub w: i32,
    pub h: i32,
    px: Vec<[u8; 3]>,
}

impl Canvas {
    pub fn new(w: i32, h: i32, bg: [u8; 3]) -> Self {
        Canvas {
            w,
            h,
            px: vec![bg; (w * h) as usize],
        }
    }

    pub fn pixel(&self, x: i32, y: i32) -> [u8; 3] {
        self.px[(y * self.w + x) as usize]
    }

    /// 压成 PNG 要的 RGB 缓冲。
    /// 放大到 `w×h`（**最近邻**）—— 等价于 app 里 `ui::UI_SCALE` 那一步
    ///（800×600 的设计空间铺到 1024×768；app 用线性采样、这里用最近邻，
    /// e2e 只是拿来看版式的，不逐像素对拍）。
    pub fn to_rgb_scaled(&self, w: i32, h: i32) -> Vec<u8> {
        let mut out = vec![0u8; (w * h * 3) as usize];
        for y in 0..h {
            let sy = (y as i64 * self.h as i64 / h as i64) as i32;
            for x in 0..w {
                let sx = (x as i64 * self.w as i64 / w as i64) as i32;
                let p = self.pixel(sx, sy);
                let o = ((y * w + x) * 3) as usize;
                out[o..o + 3].copy_from_slice(&p);
            }
        }
        out
    }

    pub fn to_rgb(&self) -> Vec<u8> {
        let mut out = Vec::with_capacity(self.px.len() * 3);
        for p in &self.px {
            out.extend_from_slice(p);
        }
        out
    }

    pub fn fill_rect(&mut self, x: i32, y: i32, w: i32, h: i32, c: [u8; 3]) {
        for yy in y..(y + h).min(self.h) {
            if yy < 0 {
                continue;
            }
            for xx in x..(x + w).min(self.w) {
                if xx < 0 {
                    continue;
                }
                self.px[(yy * self.w + xx) as usize] = c;
            }
        }
    }

    /// 把一张 RGBA 图贴到 `(x, y)`（最近邻 1:1，索引 0 / alpha=0 跳过）。
    ///
    /// 界面素材（`Prguse`/`ChrSel`）与地图图块同一个来源（`.wzl` 解码成 RGBA），
    /// 只是没有"Alpha 物件"那套滤色 —— 素材里的透明只有"全透"这一种。
    pub fn blit_rgba(&mut self, rgba: &[u8], w: i32, h: i32, x: i32, y: i32) {
        for sy in 0..h {
            let dy = y + sy;
            if dy < 0 || dy >= self.h {
                continue;
            }
            for sx in 0..w {
                let dx = x + sx;
                if dx < 0 || dx >= self.w {
                    continue;
                }
                let i = ((sy * w + sx) * 4) as usize;
                let a = u32::from(rgba[i + 3]);
                if a == 0 {
                    continue;
                }
                let keep = 255 - a;
                let dst = &mut self.px[(dy * self.w + dx) as usize];
                let mix =
                    |s: u8, d: u8| ((u32::from(s) * a + u32::from(d) * keep) / 255).min(255) as u8;
                dst[0] = mix(rgba[i], dst[0]);
                dst[1] = mix(rgba[i + 1], dst[1]);
                dst[2] = mix(rgba[i + 2], dst[2]);
            }
        }
    }

    /// 画格网（每 `UNIT_X` / `UNIT_Y` 一条），用于与 app 的 `D` 叠加层对照。
    pub fn draw_grid(&mut self, c: [u8; 3]) {
        let mut x = 0;
        while x < self.w {
            self.fill_rect(x, 0, 1, self.h, c);
            x += UNIT_X;
        }
        let mut y = 0;
        while y < self.h {
            self.fill_rect(0, y, self.w, 1, c);
            y += UNIT_Y;
        }
    }

    /// 按 core 的落点规则画一张图块（1:1 最近邻）。
    fn draw(&mut self, d: &TileDraw, t: &Tile) {
        let top = d.top_y(t.w, t.h, i32::from(t.anchor_y));
        let left = d.left_x(i32::from(t.anchor_x));
        for y in 0..t.h {
            let ty = top + y;
            if ty < 0 || ty >= self.h {
                continue;
            }
            for x in 0..t.w {
                let tx = left + x;
                if tx < 0 || tx >= self.w {
                    continue;
                }
                let i = ((y * t.w + x) * 4) as usize;
                let a = u32::from(t.px[i + 3]);
                if a == 0 {
                    continue; // 索引 0 = 全透明
                }
                let dst = &mut self.px[(ty * self.w + tx) as usize];
                let (sr, sg, sb) = (
                    u32::from(t.px[i]),
                    u32::from(t.px[i + 1]),
                    u32::from(t.px[i + 2]),
                );
                let (dr, dg, db) = (u32::from(dst[0]), u32::from(dst[1]), u32::from(dst[2]));
                let out = if t.screen {
                    // 预乘（= 官方 DrawBlend 的滤色）：out = src + dst*(1 - a/255)
                    let keep = 255 - a;
                    [
                        (sr + dr * keep / 255).min(255),
                        (sg + dg * keep / 255).min(255),
                        (sb + db * keep / 255).min(255),
                    ]
                } else {
                    // 直通 alpha：out = src*a/255 + dst*(1 - a/255)
                    let inv = 255 - a;
                    [
                        (sr * a + dr * inv) / 255,
                        (sg * a + dg * inv) / 255,
                        (sb * a + db * inv) / 255,
                    ]
                };
                *dst = [out[0] as u8, out[1] as u8, out[2] as u8];
            }
        }
    }
}

/// 画布与图块仓库一起把一份绘制清单渲染出来。
pub fn render(draws: &[TileDraw], tiles: &mut Tiles, canvas: &mut Canvas) -> usize {
    let mut drawn = 0;
    for d in draws {
        if let Some(t) = tiles.get(d) {
            canvas.draw(d, t);
            drawn += 1;
        }
    }
    drawn
}

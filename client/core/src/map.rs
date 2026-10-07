//! `.map` —— 地图格子表（**不含像素**）。
//!
//! 规格与语义见 `docs/assets.md §3.3 / §3.3a / §3.3b`。
//! **权威语义来自官方客户端**：`mir2standard/.../MirClient/PlayScn.pas` + `MapUnit.pas`。
//!
//! 一个 `.map` 只是"哪张图贴在哪一格"的索引表，像素在 `.wzl` 里，三层各指一本：
//!
//! | 字段 | 图层 | 图库 |
//! |---|---|---|
//! | [`Cell::bk_img`] | 地表 | `Tiles.wzl` |
//! | [`Cell::mid_img`] | 中间 | `SmTiles.wzl` |
//! | [`Cell::fr_img`] | 前景 | `Objects<N>.wzl`，**N = [`Cell::area`]** |
//!
//! 三条必须照做的语义（`PlayScn.pas:560/586/1143`）：
//!
//! 1. **图号是 1 基**：取图要 `-1`，`0` 表示该层不画（故有 [`Cell::bk_tile`] 等辅助）。
//! 2. **地表层只在 `x % 2 == 0 && y % 2 == 0` 的格上绘制** —— 地块是 48×32，横向跨 2 个逻辑格。
//! 3. **通行** = `(bk_img & 0x8000) + (fr_img & 0x8000) == 0`（见 [`Cell::can_walk`]）。

use std::io;

use crate::m2pk::Archive;

/// 头部长度。
pub const HEADER_LEN: usize = 52;
/// 经典布局：每格 12 字节（`TMapInfo` 是 Delphi `packed record`，无对齐填充）。
pub const CELL_LEN_CLASSIC: usize = 12;
/// 扩展布局：每格 36 字节（前 12 字节语义与经典**相同**，后 24 未知——见 assets.md §3.3b）。
pub const CELL_LEN_EXTENDED: usize = 36;

/// `bk_img` / `fr_img` 的阻挡位。
pub const BLOCK_BIT: u16 = 0x8000;
/// `door_index` 的门位（低 7 位是门组号）。
pub const DOOR_BIT: u8 = 0x80;
/// `door_offset` 的"有位移"位（开门动画；低 7 位是偏移量）。
pub const DOOR_OFFSET_BIT: u8 = 0x80;
/// `ani_frame` 的 Alpha 混合位（低 7 位是动画帧数）。
pub const ANI_ALPHA_BIT: u8 = 0x80;

fn bad(msg: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg.into())
}

/// 磁盘上的单格（经典 12 字节；36 字节布局只取前 12）。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Cell {
    /// 地表层图号（**1 基**，`0` = 不画）；`& 0x8000` ⇒ 阻挡。
    pub bk_img: u16,
    /// 中间层图号（**1 基**，`0` = 不画）。
    ///
    /// 注意：官方客户端取图时**没有**对 `mid_img` 做 `& 0x7FFF` 掩码
    /// （`PlayScn.pas:586` 直接用整个 16 位），故这里也不掩。
    pub mid_img: u16,
    /// 前景层图号（**1 基**，`0` = 不画）；`& 0x8000` ⇒ 阻挡。
    pub fr_img: u16,
    /// `& 0x80` ⇒ 门；低 7 位 = 门组号。
    pub door_index: u8,
    /// `& 0x80` ⇒ 有位移；低 7 位 = 偏移量。
    pub door_offset: u8,
    /// `& 0x80` ⇒ Alpha 混合；低 7 位 = 动画帧数。
    pub ani_frame: u8,
    /// 动画节拍。
    pub ani_tick: u8,
    /// **前景层选哪本 `Objects<N>.wzl`**（`GetObjs` 的 `nUnit`）——不是"区域号"。
    pub area: u8,
    /// 光照等级（注释写 `0..1..4`）。
    pub light: u8,
}

impl Cell {
    /// 该格是否可通行。
    pub fn can_walk(&self) -> bool {
        self.bk_img & BLOCK_BIT == 0 && self.fr_img & BLOCK_BIT == 0
    }

    /// 只看前景层的通行判定（`MapUnit.pas:337`）。
    pub fn fr_passable(&self) -> bool {
        self.fr_img & BLOCK_BIT == 0
    }

    pub fn is_door(&self) -> bool {
        self.door_index & DOOR_BIT != 0
    }

    /// 门组号（低 7 位）。
    pub fn door_group(&self) -> u8 {
        self.door_index & 0x7F
    }

    /// 门是否处于"已位移"状态。
    pub fn door_shifted(&self) -> bool {
        self.door_offset & DOOR_OFFSET_BIT != 0
    }

    /// 门的位移量（低 7 位）。
    pub fn door_shift(&self) -> u8 {
        self.door_offset & 0x7F
    }

    /// 是否要求 Alpha 混合。
    pub fn ani_blend(&self) -> bool {
        self.ani_frame & ANI_ALPHA_BIT != 0
    }

    /// 动画帧数（低 7 位）。
    pub fn ani_frames(&self) -> u8 {
        self.ani_frame & 0x7F
    }

    /// 地表层图号（已剥阻挡位、已转 0 基）；`None` 表示该层不画。
    pub fn bk_tile(&self) -> Option<u16> {
        tile(self.bk_img)
    }

    /// 中间层图号（0 基）；`None` 表示不画。
    pub fn mid_tile(&self) -> Option<u16> {
        tile(self.mid_img)
    }

    /// 前景层图号（已剥阻挡位、已转 0 基）；`None` 表示不画。
    pub fn fr_tile(&self) -> Option<u16> {
        tile(self.fr_img)
    }

    /// 地表层是否需要绘制：地块只在 `x`、`y` 皆为偶数的格上画（见模块文档第 2 条）。
    pub fn draws_ground_at(&self, x: usize, y: usize) -> bool {
        x.is_multiple_of(2) && y.is_multiple_of(2) && self.bk_tile().is_some()
    }
}

fn tile(raw: u16) -> Option<u16> {
    let v = raw & 0x7FFF;
    if v == 0 {
        None
    } else {
        Some(v - 1)
    }
}

/// 解析后的地图。**内部按行主序存放**（`index = y * width + x`）。
///
/// 文件本身是**列主序**（`x` 在外层循环）；加载时转置，这样按 `x` 连续访问时缓存友好
/// （与 `mir2go/internal/world/map.go` 同一取舍）。
#[derive(Debug)]
pub struct Map {
    /// 地图宽度（格）。
    pub width: u16,
    /// 地图高度（格）。
    pub height: u16,
    /// 头部标题（原版是定长 16 字节短串；非 UTF-8 时按替换字符处理）。
    pub title: String,
    /// 磁盘上每格的字节数：12（经典）或 36（扩展）。
    pub cell_len: usize,
    cells: Vec<Cell>,
}

impl Map {
    /// 解析一份 `.map` 字节流。
    pub fn parse(bytes: &[u8]) -> io::Result<Self> {
        if bytes.len() < HEADER_LEN {
            return Err(bad(format!(
                ".map 太短（{} 字节，至少需 {HEADER_LEN}）",
                bytes.len()
            )));
        }
        let width = u16::from_le_bytes([bytes[0], bytes[1]]);
        let height = u16::from_le_bytes([bytes[2], bytes[3]]);
        if width == 0 || height == 0 {
            return Err(bad(format!(".map 尺寸非法：{width}×{height}")));
        }
        let cells = width as usize * height as usize;
        let cell_len = detect_cell_len(bytes.len(), cells).ok_or_else(|| {
            bad(format!(
                ".map 布局未知：{} 字节，头声明 {}×{}；期望 {}（经典 12 B/格）或 {}（扩展 36 B/格）。\
                 见 docs/assets.md §3.3b",
                bytes.len(),
                width,
                height,
                HEADER_LEN + cells * CELL_LEN_CLASSIC,
                HEADER_LEN + cells * CELL_LEN_EXTENDED
            ))
        })?;

        let title_len = (bytes[4] as usize).min(16);
        let title = String::from_utf8_lossy(&bytes[5..5 + title_len]).into_owned();

        let mut out = Vec::with_capacity(cells);
        let mut off = HEADER_LEN;
        for _x in 0..width {
            for _y in 0..height {
                let c = &bytes[off..off + CELL_LEN_CLASSIC];
                out.push(Cell {
                    bk_img: u16::from_le_bytes([c[0], c[1]]),
                    mid_img: u16::from_le_bytes([c[2], c[3]]),
                    fr_img: u16::from_le_bytes([c[4], c[5]]),
                    door_index: c[6],
                    door_offset: c[7],
                    ani_frame: c[8],
                    ani_tick: c[9],
                    area: c[10],
                    light: c[11],
                });
                off += cell_len;
            }
        }
        // 上面是按列主序读入的；转置成行主序。
        let mut cells_row_major = vec![Cell::default(); cells];
        let w = width as usize;
        for x in 0..w {
            for y in 0..height as usize {
                cells_row_major[y * w + x] = out[x * height as usize + y];
            }
        }
        Ok(Self {
            width,
            height,
            title,
            cell_len,
            cells: cells_row_major,
        })
    }

    /// 从容器里取一张地图（`read_name` + `parse` 的组合）。
    pub fn load(archive: &Archive, name: &str) -> io::Result<Self> {
        let bytes = archive.read_name(name)?.ok_or_else(|| {
            io::Error::new(io::ErrorKind::NotFound, format!("容器里没有地图 {name:?}"))
        })?;
        Self::parse(&bytes)
    }

    /// 是否用了 36 字节/格的扩展布局。
    pub fn is_extended(&self) -> bool {
        self.cell_len == CELL_LEN_EXTENDED
    }

    pub fn cell_count(&self) -> usize {
        self.cells.len()
    }

    pub fn at(&self, x: usize, y: usize) -> Option<&Cell> {
        if x >= self.width as usize || y >= self.height as usize {
            return None;
        }
        Some(&self.cells[y * self.width as usize + x])
    }

    /// 可通行？越界按不可通行处理。
    pub fn can_walk(&self, x: usize, y: usize) -> bool {
        self.at(x, y).map(Cell::can_walk).unwrap_or(false)
    }

    pub fn count_blocked(&self) -> usize {
        self.cells.iter().filter(|c| !c.can_walk()).count()
    }

    /// 遍历全部格子（行主序）。
    pub fn cells(&self) -> &[Cell] {
        &self.cells
    }
}

// ---------- 视口绘制指令 ----------

/// 逻辑格宽（官方 `Grobal2.pas:45` `UNITX`）。
pub const UNIT_X: i32 = 48;
/// 逻辑格高（官方 `Grobal2.pas:45` `UNITY`）。
pub const UNIT_Y: i32 = 32;

/// 三层各自对应的图库。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Lib {
    /// 地表层 → `Tiles.wzl`（图块 96×64，覆盖 2×2 格）。
    Tiles,
    /// 中间层 → `SmTiles.wzl`（图块 48×32，一格一块）。
    SmTiles,
    /// 前景层 → `Objects<N>.wzl`（`N` 由 [`Cell::area`] 决定）。
    Objects,
}

impl Lib {
    /// 图库文件名。`area` 只对 [`Lib::Objects`] 有意义。
    ///
    /// 命名规则来自官方 `MShare.pas:790` `GetObjs`：
    /// `nUnit == 0` ⇒ `Objects`，`nUnit == N` ⇒ `Objects{N+1}`。
    pub fn file_name(self, area: u8) -> String {
        match self {
            Lib::Tiles => "Tiles".to_string(),
            Lib::SmTiles => "SmTiles".to_string(),
            Lib::Objects if area == 0 => "Objects".to_string(),
            Lib::Objects => format!("Objects{}", area as u16 + 1),
        }
    }
}

/// 图层。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Layer {
    Ground,
    Mid,
    Front,
}

/// 一条绘制指令：从哪个库取哪张图，画在视口内的哪个像素位置。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TileDraw {
    pub layer: Layer,
    pub lib: Lib,
    /// 前景层选库用的编号（其余层恒为 0）。
    pub area: u8,
    /// 图号（**已转 0 基**）。
    pub index: u16,
    /// 该格左上角在视口内的像素坐标（`x`、`y`）。
    ///
    /// ⚠️ 对**前景层**，这**不是**图块的落点——前景要按图块实际高度做**底边对齐**
    /// （见 [`TileDraw::top_y`]）。
    pub x: i32,
    pub y: i32,
    /// 前景层的动画帧数（`ani_frame & 0x7F`）；其余层恒为 0。
    pub ani_frames: u8,
}

impl TileDraw {
    /// 图块落点的 Y（相对视口）。
    ///
    /// * **地表 / 中间**：左上角对齐格 ⇒ 就是 [`TileDraw::y`]。
    /// * **前景**：
    ///   - "平"图块（48×32 或 96×64）且**无动画** ⇒ 左上角对齐（官方 floor 趟）；
    ///   - 其余（树、墙、Alpha 混合…）⇒ **底边对齐格的底边**。
    ///
    /// 依据：Delphi `PlayScn.pas:1172`（`mmm := m + UNITY - DSurface.Height`）与
    /// Crystal `GameScene.cs:10814/10933`（`drawY` 取格底，再 `- s.Height`）。
    /// 这两份实现在"平的"情形下**天然等价**（`height == 32` 时两种算法同解），
    /// 差异只出现在高于一格的图块上——正是之前错位的地方。
    pub fn top_y(&self, width: i32, height: i32) -> i32 {
        if self.layer != Layer::Front {
            return self.y;
        }
        let flat =
            (width == UNIT_X && height == UNIT_Y) || (width == 2 * UNIT_X && height == 2 * UNIT_Y);
        if flat && self.ani_frames == 0 {
            self.y
        } else {
            self.y + UNIT_Y - height
        }
    }
}

impl Map {
    /// 生成视口内的绘制指令（**顺序即绘制顺序**）。
    ///
    /// 画序：地表 → 中间 → 前景，每层内部按 `y` 递增 ——
    /// 于是靠下的前景物件自然覆盖靠上的，**Y 序遮挡不需要额外排序**。
    ///
    /// `out` 会被清空后复用（避免每帧分配）。
    pub fn visible_tiles(
        &self,
        cam_x: i32,
        cam_y: i32,
        cols: i32,
        rows: i32,
        out: &mut Vec<TileDraw>,
    ) {
        out.clear();
        let (w, h) = (self.width as i32, self.height as i32);
        for layer in [Layer::Ground, Layer::Mid, Layer::Front] {
            for dy in 0..rows {
                for dx in 0..cols {
                    let (x, y) = (cam_x + dx, cam_y + dy);
                    if x < 0 || y < 0 || x >= w || y >= h {
                        continue;
                    }
                    let c = &self.cells[(y * w + x) as usize];
                    let (lib, area, index) = match layer {
                        Layer::Ground => {
                            // 地块是 96×64、跨 2×2 格 ⇒ 只在偶数格画
                            if !c.draws_ground_at(x as usize, y as usize) {
                                continue;
                            }
                            match c.bk_tile() {
                                Some(t) => (Lib::Tiles, 0, t),
                                None => continue,
                            }
                        }
                        Layer::Mid => match c.mid_tile() {
                            Some(t) => (Lib::SmTiles, 0, t),
                            None => continue,
                        },
                        Layer::Front => match c.fr_tile() {
                            Some(t) => (Lib::Objects, c.area, t),
                            None => continue,
                        },
                    };
                    out.push(TileDraw {
                        layer,
                        lib,
                        area,
                        index,
                        x: dx * UNIT_X,
                        y: dy * UNIT_Y,
                        ani_frames: if layer == Layer::Front {
                            c.ani_frames()
                        } else {
                            0
                        },
                    });
                }
            }
        }
    }
}

impl Map {
    /// 找**离 `(cx, cy)` 最近、且前景层有图**的格。
    ///
    /// 用途：打开地图时把镜头放到"有东西可看"的位置——几何中心常常是一片空地，
    /// 一进去看到空白会让人怀疑渲染坏了。
    pub fn nearest_front_tile(&self, cx: i32, cy: i32) -> Option<(i32, i32)> {
        let (w, h) = (self.width as i32, self.height as i32);
        let mut best: Option<(i32, i32, i64)> = None;
        for y in 0..h {
            for x in 0..w {
                if self.cells[(y * w + x) as usize].fr_tile().is_none() {
                    continue;
                }
                let (dx, dy) = ((x - cx) as i64, (y - cy) as i64);
                let d = dx * dx + dy * dy;
                if best.is_none_or(|(_, _, bd)| d < bd) {
                    best = Some((x, y, d));
                }
            }
        }
        best.map(|(x, y, _)| (x, y))
    }
}

/// 由文件长度与格数推断每格字节数；未知布局返回 `None`。
fn detect_cell_len(len: usize, cells: usize) -> Option<usize> {
    [CELL_LEN_CLASSIC, CELL_LEN_EXTENDED]
        .into_iter()
        .find(|&cl| {
            cells
                .checked_mul(cl)
                .and_then(|n| n.checked_add(HEADER_LEN))
                == Some(len)
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 造一份经典布局的 `.map`（列主序写入，供转置测试）。
    fn build(w: u16, h: u16, cell_len: usize, fill: impl Fn(usize, usize) -> [u8; 12]) -> Vec<u8> {
        let mut v = vec![0u8; HEADER_LEN];
        v[0..2].copy_from_slice(&w.to_le_bytes());
        v[2..4].copy_from_slice(&h.to_le_bytes());
        v[4] = 13;
        v[5..18].copy_from_slice(b"Legend of mir");
        // 列主序：x 外层
        for x in 0..w as usize {
            for y in 0..h as usize {
                let body = fill(x, y);
                v.extend_from_slice(&body);
                v.extend(std::iter::repeat_n(0u8, cell_len - 12));
            }
        }
        v
    }

    #[test]
    fn cell_flags() {
        let mut c = Cell::default();
        assert!(c.can_walk(), "全零格应可通行");
        assert!(!c.is_door());
        assert_eq!(c.bk_tile(), None, "图号 0 = 不画");

        // 图号 1 基：raw=1 → tile 0
        c.bk_img = 1;
        assert_eq!(c.bk_tile(), Some(0));

        // 阻挡位不影响图号
        c.bk_img = BLOCK_BIT | 5;
        assert_eq!(c.bk_tile(), Some(4));
        assert!(!c.can_walk(), "bk 的 0x8000 ⇒ 阻挡");

        c.bk_img = 5;
        c.fr_img = BLOCK_BIT;
        assert!(!c.can_walk(), "fr 的 0x8000 ⇒ 阻挡");
        assert_eq!(c.fr_tile(), None, "fr 只有阻挡位 ⇒ 该层不画");
        assert!(!c.fr_passable());

        c.door_index = DOOR_BIT | 7;
        assert!(c.is_door());
        assert_eq!(c.door_group(), 7);

        c.door_offset = DOOR_OFFSET_BIT | 3;
        assert!(c.door_shifted());
        assert_eq!(c.door_shift(), 3);

        c.ani_frame = ANI_ALPHA_BIT | 4;
        assert!(c.ani_blend());
        assert_eq!(c.ani_frames(), 4);

        // 地表层隔格绘制
        c.bk_img = 9;
        assert!(c.draws_ground_at(0, 0));
        assert!(!c.draws_ground_at(1, 0));
        assert!(!c.draws_ground_at(0, 1));
    }

    #[test]
    fn parse_classic_and_transpose() {
        // 4×3，把 (x,y) 编码进 bk_img 以便验证列主序 → 行主序的转置
        let bytes = build(4, 3, CELL_LEN_CLASSIC, |x, y| {
            let mut b = [0u8; 12];
            let v = (y as u16 * 10 + x as u16) + 1;
            b[0..2].copy_from_slice(&v.to_le_bytes());
            b
        });
        assert_eq!(bytes.len(), HEADER_LEN + 4 * 3 * 12);

        let m = Map::parse(&bytes).expect("应能解析");
        assert_eq!((m.width, m.height), (4, 3));
        assert_eq!(m.title, "Legend of mir");
        assert_eq!(m.cell_len, CELL_LEN_CLASSIC);
        assert!(!m.is_extended());
        assert_eq!(m.cell_count(), 12);

        for y in 0..3u16 {
            for x in 0..4u16 {
                let want = y * 10 + x; // 0 基图号
                assert_eq!(
                    m.at(x as usize, y as usize).unwrap().bk_tile(),
                    Some(want),
                    "格 ({x},{y}) 图号不符（列主序转置错？）"
                );
            }
        }
        assert!(m.at(4, 0).is_none(), "越界应返回 None");
        assert!(!m.can_walk(9, 9), "越界按不可通行");
        assert_eq!(m.count_blocked(), 0);
    }

    #[test]
    fn parse_extended_36() {
        let bytes = build(2, 2, CELL_LEN_EXTENDED, |x, y| {
            let mut b = [0u8; 12];
            b[0..2].copy_from_slice(&(1u16 + (y * 2 + x) as u16).to_le_bytes()); // bk
            b[2..4].copy_from_slice(&(100u16).to_le_bytes()); // mid
            b[4..6].copy_from_slice(&BLOCK_BIT.to_le_bytes()); // fr 阻挡
            b[10] = 3; // area
            b
        });
        let m = Map::parse(&bytes).expect("36 B/格 也要能解析");
        assert_eq!(m.cell_len, CELL_LEN_EXTENDED);
        assert!(m.is_extended());
        assert_eq!(m.at(1, 1).unwrap().bk_tile(), Some(3));
        assert_eq!(
            m.at(0, 0).unwrap().mid_img,
            100,
            "mid 不掩码，整 16 位都是图号"
        );
        assert_eq!(
            m.at(0, 0).unwrap().area,
            3,
            "area = 前景层选哪本 Objects<N>"
        );
        assert!(!m.can_walk(0, 0), "fr 的阻挡位生效");
        assert_eq!(m.count_blocked(), 4);
    }

    #[test]
    fn reject_unknown_layout() {
        // 模拟 EM* 族：定长 20692、头声明 30×35（assets.md §3.3b 记为"布局未定"）
        let mut v = vec![0u8; 20692];
        v[0..2].copy_from_slice(&30u16.to_le_bytes());
        v[2..4].copy_from_slice(&35u16.to_le_bytes());
        let err = Map::parse(&v).expect_err("未知布局必须报错，不能猜");
        let msg = err.to_string();
        assert!(msg.contains("布局未知"), "错误信息应指明原因：{msg}");
    }

    #[test]
    fn reject_short_and_bad_dims() {
        assert!(Map::parse(&[]).is_err());
        assert!(Map::parse(&[0u8; 52]).is_err(), "0×0 应报错");
    }

    #[test]
    fn visible_tiles_three_layers_and_order() {
        // 4×4，所有格三层都有图：bk=2→图号1、mid=3→2、fr=4→3，area=5
        let bytes = build(4, 4, CELL_LEN_CLASSIC, |_x, _y| {
            let mut b = [0u8; 12];
            b[0..2].copy_from_slice(&2u16.to_le_bytes());
            b[2..4].copy_from_slice(&3u16.to_le_bytes());
            b[4..6].copy_from_slice(&4u16.to_le_bytes());
            b[10] = 5; // area ⇒ Objects 库编号
            b
        });
        let m = Map::parse(&bytes).unwrap();
        let mut out = Vec::new();
        m.visible_tiles(0, 0, 4, 4, &mut out);

        let n = |out: &Vec<TileDraw>, l: Layer| out.iter().filter(|d| d.layer == l).count();
        assert_eq!(n(&out, Layer::Ground), 4, "地表只在偶数格：2×2=4");
        assert_eq!(n(&out, Layer::Mid), 16);
        assert_eq!(n(&out, Layer::Front), 16);
        assert_eq!(out.len(), 36);

        // 顺序即绘制顺序：地表 → 中间 → 前景
        assert_eq!(out[0].layer, Layer::Ground);
        assert_eq!(out[4].layer, Layer::Mid);
        assert_eq!(out[20].layer, Layer::Front);

        // 图库、0 基图号、像素位置
        assert_eq!(
            (out[0].lib, out[0].index, out[0].x, out[0].y),
            (Lib::Tiles, 1, 0, 0)
        );
        let mid = out.iter().find(|d| d.layer == Layer::Mid).unwrap();
        assert_eq!((mid.lib, mid.index), (Lib::SmTiles, 2));
        // 前景 x=1,y=0 ⇒ 屏幕 (UNIT_X, 0)
        let fr = out
            .iter()
            .find(|d| d.layer == Layer::Front && d.x == UNIT_X && d.y == 0)
            .unwrap();
        assert_eq!((fr.lib, fr.index, fr.area), (Lib::Objects, 3, 5));

        // 负镜头：越界格被裁掉，不产生负坐标
        m.visible_tiles(-1, -1, 3, 3, &mut out);
        assert!(out.iter().all(|d| d.x >= 0 && d.y >= 0));
        assert_eq!(n(&out, Layer::Mid), 4, "(-1,-1) 被裁 ⇒ 只剩 2×2");
    }

    #[test]
    fn nearest_front_tile_picks_closest_object() {
        // 只有 (1,1) 与 (3,0) 两格有前景物件
        let bytes = build(4, 4, CELL_LEN_CLASSIC, |x, y| {
            let mut b = [0u8; 12];
            if (x, y) == (1, 1) || (x, y) == (3, 0) {
                b[4..6].copy_from_slice(&9u16.to_le_bytes());
            }
            b
        });
        let m = Map::parse(&bytes).unwrap();
        assert_eq!(m.nearest_front_tile(1, 1), Some((1, 1)));
        assert_eq!(m.nearest_front_tile(3, 0), Some((3, 0)));
        // 从 (2,0) 看：(3,0) 距离 1，(1,1) 距离 2 ⇒ 取 (3,0)
        assert_eq!(m.nearest_front_tile(2, 0), Some((3, 0)));

        // 完全没有前景物件时返回 None（镜头退回几何中心）
        let empty = Map::parse(&build(2, 2, CELL_LEN_CLASSIC, |_, _| [0u8; 12])).unwrap();
        assert_eq!(empty.nearest_front_tile(0, 0), None);
    }

    #[test]
    fn front_layer_bottom_alignment() {
        // 地表 / 中间：左上角对齐，不受高度影响
        for layer in [Layer::Ground, Layer::Mid] {
            let d = TileDraw {
                layer,
                lib: Lib::Tiles,
                area: 0,
                index: 0,
                x: 0,
                y: 64,
                ani_frames: 0,
            };
            assert_eq!(d.top_y(48, 32), 64);
            assert_eq!(d.top_y(48, 200), 64, "非前景层不做底边对齐");
        }

        let front = |ani: u8| TileDraw {
            layer: Layer::Front,
            lib: Lib::Objects,
            area: 0,
            index: 0,
            x: 0,
            y: 64,
            ani_frames: ani,
        };

        // "平"图块（48×32）且无动画 ⇒ 左上对齐；两种算法同解
        assert_eq!(front(0).top_y(48, 32), 64);
        // 96×64 的平图块同理走左上对齐
        assert_eq!(front(0).top_y(96, 64), 64);
        // 高图块 ⇒ 底边对齐格的底边（y + 32 - h）
        assert_eq!(front(0).top_y(48, 86), 64 + 32 - 86);
        assert_eq!(front(0).top_y(48, 200), 64 + 32 - 200);
        // 有动画时即使尺寸是 48×32，结果也不变（因为 h == 32）
        assert_eq!(front(7).top_y(48, 32), 64);
        // 但 96×64 有动画 ⇒ 走底边对齐（与官方第二趟一致）
        assert_eq!(front(7).top_y(96, 64), 64 + 32 - 64);
    }

    #[test]
    fn lib_file_name_follows_getobjs() {
        // 官方 MShare.pas:790：nUnit=0 ⇒ Objects，nUnit=N ⇒ Objects{N+1}
        assert_eq!(Lib::Tiles.file_name(0), "Tiles");
        assert_eq!(Lib::SmTiles.file_name(0), "SmTiles");
        assert_eq!(Lib::Objects.file_name(0), "Objects");
        assert_eq!(Lib::Objects.file_name(1), "Objects2");
        assert_eq!(Lib::Objects.file_name(9), "Objects10");
    }

    /// 真实数据回归：需要先跑 `tools/m2pk/build.sh`（产物不入库，见 assets.md §6）。
    #[test]
    fn real_container_if_present() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../../assets/map/maps.m2pk");
        let archive = match Archive::open(path) {
            Ok(a) => a,
            Err(_) => {
                eprintln!("跳过：{path} 不存在（先跑 tools/m2pk/build.sh）");
                return;
            }
        };
        assert_eq!(archive.len(), 770, "客户端集应有 770 张地图（D-22）");

        // 0.map：700×700 经典布局
        let m = Map::load(&archive, "0").expect("0.map 应可解析");
        assert_eq!((m.width, m.height), (700, 700));
        assert_eq!(m.cell_len, CELL_LEN_CLASSIC);
        assert_eq!(m.title, "Legend of mir");
        assert!(m.count_blocked() > 0, "地面图应有阻挡格");

        // 4.map：36 B/格（D-22 落地要求 4 —— 必须支持，否则 M1 一进主城就撞上）
        let m4 = Map::load(&archive, "4").expect("4.map 应可解析");
        assert_eq!((m4.width, m4.height), (500, 500));
        assert!(m4.is_extended(), "4.map 是 36 B/格 布局");

        // 大小写不敏感查找（客户端集里是有大写名的，如 D2071.map）
        assert!(archive.lookup("D2071").is_some());
        assert!(archive.lookup("d2071").is_some());
        // `~` 也要能查（真实数据里唯一带特殊字符的名字）
        assert!(archive.lookup("T3063~01").is_some());
        // 只有服务端集才有的名字，客户端集里必须没有（D-22 的边界）
        assert!(archive.lookup("2d").is_none());
    }
}

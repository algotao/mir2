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

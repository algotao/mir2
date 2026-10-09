#!/usr/bin/env python3
"""把小地图的**区域标注**从原版客户端数据抽成 Rust 表。

源：`$WS/mir2c/data/MapDesc1.dat`（**GBK**，250 行 / 182 条）

格式（`;` 开头的行是注释，例如 `;比奇大地图`）：

    地图描述,x,y,文本,$BBGGRR,flag

- 第一列是**地图显示名**（"比奇省"…），正好等于服务端下发的 `map_title`
  （见 `core::world::map_title`）—— 所以客户端拿它当键，不用再引地图号；
- `x,y` 是**格坐标**（与 `StartPoint.txt`/`mongen.txt` 同一套：银杏山谷那两条标在
  (620,626) 与边界村 (294,630)，都对得上）；
- 颜色是 Delphi 的 `$BBGGRR`（R 在最低字节）⇒ 转成画图用的 `0xRRGGBB`；
- 最后一列原版给 0/1（0 = 区域名、1 = 店铺/建筑 —— 见文件里的段落注释），
  我们**都画**（原版小地图上两者都出）。

用法：

    python3 client/core/tools/gen_map_labels.py $WS/mir2c/data/MapDesc1.dat \\
        > client/core/src/map_labels.rs
"""
import pathlib
import sys

HEAD = '''//! 小地图上的**区域标注** —— 由 `tools/gen_map_labels.py` 从原版客户端的
//! `data/MapDesc1.dat`（GBK）生成，**不要手改**。
//!
//! 用户 2026-10-09 选了"把那套区域标注画到小地图上"：原版小地图上就带区域名
//! （"银杏山谷"/"边界村"/"比奇城"…），而左下角那行抬头是**地图描述**
//! （`map_title`，见 D-59 ④ 的说明）—— 两者不是一回事。
//!
//! 表的键是**地图显示名**（`MapDesc1.dat` 的第一列就是它），恰好等于服务端下发的
//! `map_title`，所以查询用 `labels_for(world.map_title)`。
//!
//! 每条：`(地图, x, y, 文本, 颜色 0xRRGGBB)`；`x,y` 是**格坐标**。

/// 一条区域标注（`地图` / `格坐标 x,y` / `文本` / `颜色`）。
pub type MapLabel = (&'static str, u16, u16, &'static str, u32);

/// 全部区域标注（原版 `MapDesc1.dat` 的 182 条）。
pub static MAP_LABELS: &[MapLabel] = &[
'''


def parse(path: pathlib.Path):
    seen = set()
    out = []
    for raw in path.read_text(encoding="gbk", errors="replace").splitlines():
        line = raw.strip()
        if not line or line.startswith(";"):
            continue
        f = [x.strip() for x in line.split(",")]
        if len(f) < 5:
            continue
        mp, x, y, text, color = f[0], f[1], f[2], f[3], f[4]
        if not (x.isdigit() and y.isdigit()) or not text:
            continue
        c = int(color.lstrip("$"), 16) if color.lstrip("$") and all(
            ch in "0123456789abcdefABCDEF" for ch in color.lstrip("$")
        ) else 0xFFFFFF
        # Delphi `$BBGGRR` → `0xRRGGBB`
        rr, gg, bb = c & 0xFF, (c >> 8) & 0xFF, (c >> 16) & 0xFF
        r = (mp, int(x), int(y), text, (rr << 16) | (gg << 8) | bb)
        if r in seen:
            continue
        seen.add(r)
        out.append(r)
    return out


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    rows = parse(pathlib.Path(sys.argv[1]))
    src = [HEAD]
    for mp, x, y, text, rgb in rows:
        src.append(f'    ("{mp}", {x}, {y}, "{text}", 0x{rgb:06X}),\n')
    src.append("];\n")
    src.append(
        """
/// 取某张地图上的全部标注（`map` = 地图显示名，如 "比奇省"）。
///
/// 返回 `Vec` 而不是 `impl Iterator`：那是**借用** `map` 的迭代器，调用方要把它
/// 用在语法的不同层（`for … in labels_for(title)` 里 title 还活着，但签名上写不出
/// 那个生命周期）—— 一屏最多几十条，直接给个 Vec 更省事也更好读。
pub fn labels_for(map: &str) -> Vec<&'static MapLabel> {
    MAP_LABELS.iter().filter(|l| l.0 == map).collect()
}
"""
    )
    sys.stdout.write("".join(src))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

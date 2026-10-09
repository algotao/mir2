#!/usr/bin/env python3
"""imgtool —— 像素画对比工具（找素材用）。

为什么不用 PIL/sips：像素画放大**必须最近邻**，插值会把 12x23 的剑糊成一团；
而系统里没有 PIL。这里只处理 24 位 BMP（`sips -s format bmp` 转出来），
读/写都是裸字节，不依赖任何第三方库。

子命令：
  crop  IN OUT X Y W H SCALE          裁一块并放大 SCALE 倍
  sheet OUT SCALE COLS BG  IN...      把多张图横向/网格拼成一张（统一底色 BG，如 40,40,40）
  info  IN                            打印尺寸

约定：所有输入都是 24 位 BMP（自下而上存储），输出也是 BMP。
"""

import struct
import sys


def load_bmp(path):
    b = open(path, "rb").read()
    off = struct.unpack("<I", b[10:14])[0]
    w = struct.unpack("<i", b[18:22])[0]
    h = struct.unpack("<i", b[22:26])[0]
    flip = h > 0  # 正高度 = 自下而上
    h = abs(h)
    row = ((w * 3 + 3) // 4) * 4
    px = []
    for y in range(h):
        o = off + y * row
        line = []
        for x in range(w):
            p = o + x * 3
            line.append((b[p + 2], b[p + 1], b[p]))  # BMP 是 BGR
        px.append(line)
    if flip:
        px.reverse()  # 统一成"第 0 行 = 图像顶部"
    return w, h, px


def save_bmp(path, w, h, px):
    row_pad = (4 - (w * 3) % 4) % 4
    row = w * 3 + row_pad
    pixels = row * h
    out = bytearray()
    out += b"BM" + struct.pack("<IHHI", 54 + pixels, 0, 0, 54)
    # BITMAPINFOHEADER 40 字节：size/w/h/planes/bitcount/compression/sizeimage
    # 之后还有 xppm/yppm/clrUsed/clrImportant 四个字段（共 16 字节，全 0）
    out += struct.pack("<IiiHHII", 40, w, h, 1, 24, 0, pixels)
    out += bytes(16)
    for y in range(h - 1, -1, -1):
        for (r, g, bb) in px[y]:
            out += bytes((bb, g, r))
        out += bytes(row_pad)
    open(path, "wb").write(out)


def upscale(w, h, px, n):
    out = []
    for y in range(h):
        line = []
        for x in range(w):
            line.extend([px[y][x]] * n)
        for _ in range(n):
            out.append(list(line))
    return w * n, h * n, out


def crop(w, h, px, x, y, cw, ch):
    out = []
    for yy in range(y, min(y + ch, h)):
        line = []
        for xx in range(x, min(x + cw, w)):
            line.append(px[yy][xx])
        out.append(line)
    return len(out[0]) if out else 0, len(out), out


def solid(w, h, rgb):
    return [[rgb for _ in range(w)] for _ in range(h)]


def paste(dst, dw, dh, src, sw, sh, ox, oy):
    for y in range(sh):
        if oy + y >= dh:
            break
        for x in range(sw):
            if ox + x >= dw:
                break
            dst[oy + y][ox + x] = src[y][x]


def cmd_crop(a):
    inp, outp, x, y, cw, ch, scale = a[0], a[1], *map(int, a[2:7])
    w, h, px = load_bmp(inp)
    cw2, ch2, c = crop(w, h, px, x, y, cw, ch)
    w2, h2, c = upscale(cw2, ch2, c, scale)
    save_bmp(outp, w2, h2, c)
    print(f"{inp} {w}x{h} → 裁 ({x},{y},{cw},{ch}) ×{scale} → {outp} {w2}x{h2}")


def cmd_sheet(a):
    outp, scale, cols, bg = a[0], int(a[1]), int(a[2]), tuple(map(int, a[3].split(",")))
    files = a[4:]
    imgs = []
    for f in files:
        w, h, px = load_bmp(f)
        # 把棋盘底/浅底换成统一底色：亮度接近棋盘灰的像素视为"背景"。
        flat = []
        for line in px:
            flat.append([
                (bg if abs(p[0] - p[1]) < 12 and abs(p[1] - p[2]) < 12 and p[0] > 150 else p)
                for p in line
            ])
        imgs.append(upscale(w, h, flat, scale))
    cw = max(i[0] for i in imgs) + 8
    ch = max(i[1] for i in imgs) + 8
    rows = (len(imgs) + cols - 1) // cols
    dw, dh = cw * min(cols, len(imgs)), ch * rows
    out = solid(dw, dh, bg)
    for n, (w, h, px) in enumerate(imgs):
        ox, oy = (n % cols) * cw + 4, (n // cols) * ch + 4
        paste(out, dw, dh, px, w, h, ox, oy)
        print(f"  格 {n + 1}: {files[n].rsplit('/', 1)[-1]}")
    save_bmp(outp, dw, dh, out)
    print(f"拼版 {cols} 列 × {rows} 行，每格 {cw}x{ch} → {outp} {dw}x{dh}")


def cmd_info(a):
    w, h, _ = load_bmp(a[0])
    print(f"{a[0]}: {w}x{h}")


if __name__ == "__main__":
    cmd = sys.argv[1]
    {"crop": cmd_crop, "sheet": cmd_sheet, "info": cmd_info}[cmd](sys.argv[2:])

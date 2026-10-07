//! 官方客户端的**混合语义**（`DrawBlend` 的 `Color256Mix` / `Color256Anti`）。
//!
//! 出处（`mir2standard/GameOfMir/MirClient`）：
//!
//! - `PlayScn.pas:1141 / 1247`：前景层带 `$80`（`btAniFrame and $80`）的物件走
//!   `DrawBlend(surface, x, y, sprite, 1)` —— **blendmode = 1**；
//! - `cliUtil.pas:957`：`blendmode` 为 0 取 `Color256Mix`，**否则取 `Color256Anti`**；
//! - `cliUtil.pas` 的 `BuildMix` / `BuildAnti` 生成这两张 256×256 表：
//!
//! ```text
//! BuildMix  （blendmode 0）: out = (src + dst) / 2              ← 50% 平均
//! BuildAnti （blendmode 1）: out = src + (255-src)/255 * dst    ← SCREEN（滤色）
//! ```
//!
//! ⚠️ **两条都不是「alpha 混合」**。把 `BuildAnti` 当成 50% 透明去画是个实测踩过的坑：
//! 光源贴图（如 `Objects.wzl #2723`，100×100 的径向光晕）里**近黑的外圈占 56% 像素**，
//! 在 SCREEN 下它是恒等（`src + dst·(1-0/255) ≈ dst`，「等于透明」），
//! 在 50% alpha 下却把地面**压暗一半** —— 于是一盏灯变成一大坨半透明黑斑。
//!
//! **SDL3 侧的等价实现**：SDL 没有 SCREEN，但
//! `SDL_BLENDMODE_BLEND_PREMULTIPLIED` 的公式是 `dstRGBA = srcRGBA + dstRGBA*(1-srcA)`
//! （见 `SDL_blendmode.h`），与官方同形：只要把源像素的 **alpha 换成它的亮度**，
//! 就得到 `out = src + dst*(1-brightness/255)` —— 逐通道正是官方的 SCREEN。
//!
//! ⚠️ **已知偏差**：官方对 R/G/B **各自**算 `1-src_c/255`，而 SDL 只有**一个** alpha 因子，
//! 故这里取三通道均值。对"光"这类中性/暖色的径向光晕误差在个位数以内；
//! 色偏大的像素会有肉眼难辨的偏差（SDL 定点混合无法表达逐通道 SCREEN）。

/// 把解码好的 RGBA 精灵转成 **SCREEN 混合源**：RGB 原样保留，**alpha 换成该像素的亮度**。
///
/// 全透明像素归零为 `(0,0,0,0)`：预乘公式给出 `out = dst`，背景分毫不动 —— 与官方一致
/// （官方表里 `src` 为透明色时同样不参与混色）。
///
/// 用它与 [`sdl3::render::BlendMode::BlendPremultiplied`] 配对即可复现官方混合。
pub fn screen_source(rgba: &[u8]) -> Vec<u8> {
    let mut out = rgba.to_vec();
    for p in out.as_chunks_mut::<4>().0 {
        if p[3] == 0 {
            p[0] = 0;
            p[1] = 0;
            p[2] = 0;
            continue;
        }
        p[3] = ((p[0] as u32 + p[1] as u32 + p[2] as u32) / 3) as u8;
    }
    out
}

#[cfg(test)]
mod tests {
    use super::screen_source;

    /// 按 SDL 的预乘公式算一遍（与 `SDL_blendmode.h` 的定义一致）。
    fn premultiplied(src: &[u8], dst: [f32; 3]) -> [f32; 3] {
        let a = src[3] as f32 / 255.0;
        [
            src[0] as f32 + dst[0] * (1.0 - a),
            src[1] as f32 + dst[1] * (1.0 - a),
            src[2] as f32 + dst[2] * (1.0 - a),
        ]
    }

    /// 官方的**逐通道** SCREEN（`Color256Anti` 的连续形式，`cliUtil.pas` `BuildAnti`）。
    fn official_screen(src: [f32; 3], dst: [f32; 3]) -> [f32; 3] {
        [
            src[0] + dst[0] * (1.0 - src[0] / 255.0),
            src[1] + dst[1] * (1.0 - src[1] / 255.0),
            src[2] + dst[2] * (1.0 - src[2] / 255.0),
        ]
    }

    /// 典型沙地色（实测那盏灯底下就是沙地）。
    const SAND: [f32; 3] = [180.0, 160.0, 130.0];

    #[test]
    fn keeps_rgb_and_sets_alpha_to_luma() {
        let out = screen_source(&[200, 100, 40, 255, 10, 20, 30, 255]);
        assert_eq!(&out[0..4], &[200, 100, 40, 113]); // (200+100+40)/3 = 113
        assert_eq!(&out[4..8], &[10, 20, 30, 20]); // (10+20+30)/3 = 20
    }

    #[test]
    fn transparent_pixel_becomes_full_no_op() {
        // 必须归零：否则会把原色"加"进背景
        assert_eq!(screen_source(&[255, 255, 255, 0]), vec![0, 0, 0, 0]);
    }

    /// 回归：**这一条就是"灯变成一坨黑斑"的那个 bug**。
    ///
    /// 光源贴图（`Objects.wzl #2723`）的外圈占 56% 像素、亮度 <32，SCREEN 下
    /// 必须几乎不动背景；一旦退回 50% alpha，背景会被压到 ~90，肉眼就是黑斑。
    #[test]
    fn near_black_ring_does_not_darken_the_ground() {
        let res = premultiplied(&screen_source(&[8, 0, 0, 255]), SAND);
        for (i, c) in res.iter().enumerate() {
            assert!(
                (c - SAND[i]).abs() < 8.0,
                "近黑外圈不应改变背景，通道{i}: {c} vs {}",
                SAND[i]
            );
        }
    }

    /// 与官方逐通道公式对拍 —— 覆盖贴图实测出现的三类像素。
    ///
    /// 容差 24/255（≈9%）是**已知且刻意接受**的单 alpha 近似误差：
    /// 官方对 R/G/B 各自算 `1-src_c/255`，SDL 只有一个 alpha 因子。
    /// 误差随像素色偏增大（纯灰像素误差为 0），实测该贴图最大的
    /// `(66,49,16)` 差 16/255；平均值远小于此。
    #[test]
    fn approximates_official_screen_for_real_palette_pixels() {
        // 取自 #2723 的高频索引：近黑外圈 / 暗绿 / 中段 / 中心亮
        for px in [[8u8, 0, 0], [16, 8, 8], [49, 41, 16], [208, 200, 180]] {
            let got = premultiplied(&screen_source(&[px[0], px[1], px[2], 255]), SAND);
            let want = official_screen([px[0] as f32, px[1] as f32, px[2] as f32], SAND);
            for c in 0..3 {
                assert!(
                    (got[c] - want[c]).abs() < 24.0,
                    "像素 {px:?} 通道{c}: 我们 {} vs 官方 {}",
                    got[c],
                    want[c]
                );
            }
        }
    }

    #[test]
    fn bright_center_brightens_without_blowing_out() {
        // 中心亮像素：应显著提亮，但**不该直接饱和成纯白** ——
        // 那说明用成了 ADD（`src+dst`）而不是官方的 SCREEN（`src+dst-src*dst/255`）。
        let res = premultiplied(&screen_source(&[208, 200, 180, 255]), SAND);
        assert!(res[0] > SAND[0] + 30.0, "应明显提亮，实得 {res:?}");
        assert!(res[0] < 255.0, "SCREEN 不应直接爆成纯白，实得 {res:?}");
    }
}

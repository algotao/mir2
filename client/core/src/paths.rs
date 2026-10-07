//! 资产与容器的**路径解析** —— `client/app` 与 `client/e2e` 共用。
//!
//! 抽出来不是洁癖：两个产物如果各写一套"去哪找素材"，迟早会在某台机器上
//! 表现不一致（plan §4.2 的 R-10 就是这个风险）。
//!
//! 优先级一律是 **环境变量 → 与仓库并列的约定位置**；都不命中返回 `None`，
//! 由调用方降级（**不猜、不硬编码绝对路径**）。

use std::path::{Path, PathBuf};

/// 美术目录（`.wzl` / `.wzx` 所在）。
///
/// `$MIR2_ASSET_DIR` → `$MIR2C_DATA` → **与仓库并列**的 `mir2c/data`。
pub fn asset_dir() -> Option<PathBuf> {
    for key in ["MIR2_ASSET_DIR", "MIR2C_DATA"] {
        if let Ok(v) = std::env::var(key) {
            let p = PathBuf::from(v);
            if p.is_dir() {
                return Some(p);
            }
        }
    }
    // 检出布局：<ws>/mir2（本仓库）与 <ws>/mir2c（素材）**并列**。
    // 由 CARGO_MANIFEST_DIR 反推：client/{core,app} → 上三级 = <ws>
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../mir2c/data");
    guess.canonicalize().ok().filter(|p| p.is_dir())
}

/// 地图容器（`tools/m2pk/build.sh` 的产物，见 docs/assets.md §5）。
///
/// `$MIR2_MAP_CONTAINER` → 仓库的 `assets/map/maps.m2pk`。
///
/// ⚠️ 产物**不入库**（`.gitignore` 里有 `/assets/`），所以它可能不存在 ——
/// 调用方必须处理 `None`。
pub fn map_container() -> Option<PathBuf> {
    if let Ok(v) = std::env::var("MIR2_MAP_CONTAINER") {
        let p = PathBuf::from(v);
        if p.is_file() {
            return Some(p);
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/map/maps.m2pk");
    guess.canonicalize().ok().filter(|p| p.is_file())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 不做环境变量注入（测试并行跑，改全局 env 会互相干扰）；
    /// 只校验"命中时给的确实是对的东西"。
    #[test]
    fn resolved_paths_have_expected_shape() {
        if let Some(p) = map_container() {
            assert_eq!(p.file_name().unwrap(), "maps.m2pk");
            assert!(p.is_file());
        }
        if let Some(p) = asset_dir() {
            assert!(p.is_dir(), "资产目录必须是目录");
        }
    }
}

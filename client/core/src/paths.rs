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

/// 音频目录（`sound.lst` + 那些 `.wav` 所在）。
///
/// 顺序：`$MIR2_AUDIO_DIR` / `$MIR2C_WAV` → **仓库的 `assets/audio`**（脚本产物，
/// `tools/wavpack/build.sh`）→ 美术目录旁边的 `mir2c/wav`（原始素材，两种布局都试）
/// → 与仓库并列的 `mir2c/wav`。
///
/// ⚠️ 两个坑：
///
/// 1. **不能**用 `asset_dir()` 直接拼：美术目录是 `mir2c/data`，而音频在
///    `mir2c/wav`（同级，不是子目录）—— 原版清单里的路径 `wav\103.wav` 正是
///    **相对游戏根目录**写的（`SoundUtil.pas:151-178`）。
/// 2. **产物优先于原始素材**（与 `maps.m2pk` 同一规矩）：`assets/audio` 是转换过的
///    （音效 22.05k 单声道，209 MB → 72 MB，见 `tools/wavpack`），`mir2c/wav` 是
///    未压缩原件。两者都是同一批编号（同一份 `sound.lst`）⇒ 谁在都能跑。
pub fn audio_dir() -> Option<PathBuf> {
    for key in ["MIR2_AUDIO_DIR", "MIR2C_WAV"] {
        if let Ok(v) = std::env::var(key) {
            let p = PathBuf::from(v);
            if p.is_dir() {
                return Some(p);
            }
        }
    }
    // 脚本产物：<ws>/mir2/assets/audio —— ⚠️ 只有当它**真的是散装 wav 目录**时才算数。
    // 现在的产物是 `assets/audio/sounds.m2pk`（一个容器，见 D-30），那个目录里
    // 没有 wav 也没有 sound.lst ⇒ 这里必须跳过，否则会返回一个"取不到任何音效"的目录
    //（测试 `resolved_paths_have_expected_shape` 抓的就是这个）。
    let built = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/audio");
    if let Some(p) = built.canonicalize().ok().filter(|p| looks_like_wav_dir(p)) {
        return Some(p);
    }
    if let Some(a) = asset_dir() {
        for cand in [
            a.join("wav"),
            a.parent().map(|p| p.join("wav")).unwrap_or(a.clone()),
        ] {
            if cand.is_dir() {
                return Some(cand);
            }
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../mir2c/wav");
    guess.canonicalize().ok().filter(|p| p.is_dir())
}

/// 音频容器（`tools/wavpack/build.sh` 的产物，见 docs/assets.md §6b）。
///
/// `$MIR2_AUDIO_CONTAINER` → 仓库的 `assets/audio/sounds.m2pk`。
///
/// ⚠️ 与地图容器一样**不入库**（`.gitignore` 里有 `/assets/`），所以它可能不存在 ——
/// 调用方要能退化到目录（[`audio_dir`]）：那条路跑的是**原始**未转换的素材。
pub fn audio_container() -> Option<PathBuf> {
    if let Ok(v) = std::env::var("MIR2_AUDIO_CONTAINER") {
        let p = PathBuf::from(v);
        if p.is_file() {
            return Some(p);
        }
    }
    let guess = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/audio/sounds.m2pk");
    guess.canonicalize().ok().filter(|p| p.is_file())
}

/// 这个目录像不像"散装 wav 目录"：有 `sound.lst`，或者至少有一个 `.wav`。
///
/// `assets/audio` 现在放的是**容器**（`sounds.m2pk`）⇒ 它不该被当成音频目录。
fn looks_like_wav_dir(dir: &Path) -> bool {
    if !dir.is_dir() {
        return false;
    }
    if dir.join("sound.lst").is_file() {
        return true;
    }
    std::fs::read_dir(dir)
        .map(|it| {
            it.flatten().any(|e| {
                e.file_name()
                    .to_string_lossy()
                    .to_ascii_lowercase()
                    .ends_with(".wav")
            })
        })
        .unwrap_or(false)
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
        if let Some(p) = audio_dir() {
            assert!(p.is_dir(), "音频目录必须是目录");
            // 光有目录不算：清单文件才是"这批 wav 是原版那套"的证据
            assert!(p.join("sound.lst").is_file(), "音频目录里该有 sound.lst");
        }
        if let Some(p) = audio_container() {
            assert_eq!(p.file_name().unwrap(), "sounds.m2pk");
            assert!(p.is_file(), "音频容器必须是个文件");
        }
    }
}

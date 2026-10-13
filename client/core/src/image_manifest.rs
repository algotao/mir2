//! 客户端图片资源清单（与 `tools/artpack` 共用 `client/assets/image-libraries.tsv`）。
//!
//! 规则在一个可版本控制的 TSV 中集中管理：图库选择、静态必需图、动态图库范围。
//! 不引 JSON 解析依赖；Go 与 Rust 读同一个文件、采用同一套小语法。

use std::collections::BTreeSet;
use std::sync::OnceLock;

const SOURCE: &str = include_str!("../../assets/image-libraries.tsv");
static CLIENT_MANIFEST: OnceLock<Result<ImageManifest, String>> = OnceLock::new();

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LibraryRule {
    pub name: String,
    pub prefix: bool,
    pub all: bool,
    pub indices: BTreeSet<u32>,
    pub purpose: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RequiredImage {
    pub library: String,
    pub index: u32,
    pub purpose: String,
}

/// 运行时代码引用的**静态素材 ID**，集中在 core，TSV 则声明它们的选择/预检规则。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ImageRef {
    pub library: &'static str,
    pub index: u32,
}

impl ImageRef {
    pub const fn new(library: &'static str, index: u32) -> Self {
        Self { library, index }
    }

    pub const fn pair(self) -> (&'static str, u32) {
        (self.library, self.index)
    }
}

/// 当前客户端最常共享的静态素材 ID；避免 UI、选角、HUD 各自重复写库名/图号。
pub mod assets {
    use super::ImageRef;

    pub const LOGIN_BACKGROUND: ImageRef = ImageRef::new("ui1", 2850);
    pub const SELECT_BACKGROUND: ImageRef = ImageRef::new("ui1", 2851);
    pub const HUD_BACKGROUND_1024: ImageRef = ImageRef::new("Prguse3", 690);
    pub const HUD_ORB: ImageRef = ImageRef::new("Prguse", 4);
    pub const HUD_ORB_SOLO: ImageRef = ImageRef::new("Prguse", 5);
    pub const HUD_ORB_SOLO_FILL: ImageRef = ImageRef::new("Prguse", 6);
    pub const HUD_PROP_BAR: ImageRef = ImageRef::new("Prguse", 7);
    pub const HUD_DIGIT_ZERO: ImageRef = ImageRef::new("Prguse", 30);
    pub const BAG_BACKGROUND: ImageRef = ImageRef::new("Prguse", 3);
    pub const BAG_CLOSE: ImageRef = ImageRef::new("Prguse", 64);
    pub const BAG_GOLD_ICON: ImageRef = ImageRef::new("Prguse", 29);
    pub const STATUS_BACKGROUND: ImageRef = ImageRef::new("Prguse", 370);
    pub const STATUS_CLOSE: ImageRef = ImageRef::new("Prguse", 371);
    pub const STATUS_ARROW_DOWN: ImageRef = ImageRef::new("Prguse", 372);
    pub const STATUS_ARROW_UP: ImageRef = ImageRef::new("Prguse", 373);
    pub const STATUS_FIGURE_MALE: ImageRef = ImageRef::new("Prguse", 376);
    pub const STATUS_FIGURE_FEMALE: ImageRef = ImageRef::new("Prguse", 377);
    pub const NPC_DIALOG_BACKGROUND: ImageRef = ImageRef::new("Prguse", 384);
    pub const SHOP_BUY_BACKGROUND: ImageRef = ImageRef::new("Prguse", 385);
    pub const SHOP_BUY_OK_HOVER: ImageRef = ImageRef::new("Prguse", 386);
    pub const SHOP_BUY_NEXT_HOVER: ImageRef = ImageRef::new("Prguse", 387);
    pub const SHOP_BUY_PREV_HOVER: ImageRef = ImageRef::new("Prguse", 388);
    pub const SHOP_SELL_BACKGROUND: ImageRef = ImageRef::new("Prguse", 392);
    pub const SHOP_SELL_OK_HOVER: ImageRef = ImageRef::new("Prguse", 393);
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ImageManifest {
    pub rules: Vec<LibraryRule>,
    pub required: Vec<RequiredImage>,
}

impl ImageManifest {
    pub fn parse(source: &str) -> Result<Self, String> {
        let mut manifest = Self {
            rules: Vec::new(),
            required: Vec::new(),
        };
        for (line_no, raw) in source.lines().enumerate() {
            let line = raw.trim();
            if line.is_empty() || line.starts_with('#') {
                continue;
            }
            let cols: Vec<&str> = line.split('\t').collect();
            match cols.first().copied() {
                Some("library") if cols.len() == 5 => {
                    let mut name = cols[1].trim().to_ascii_lowercase();
                    let prefix = match cols[2].trim() {
                        "exact" => false,
                        "prefix" => true,
                        other => return Err(format!("行 {}：未知匹配方式 {other:?}", line_no + 1)),
                    };
                    if prefix {
                        name = name.trim_end_matches('*').to_string();
                    }
                    if name.is_empty() {
                        return Err(format!("行 {}：图库名为空", line_no + 1));
                    }
                    if manifest
                        .rules
                        .iter()
                        .any(|r| r.name == name && r.prefix == prefix)
                    {
                        return Err(format!("行 {}：重复图库规则 {name}", line_no + 1));
                    }
                    let selector = cols[3].trim();
                    let (all, indices) =
                        parse_selector(selector).map_err(|e| format!("行 {}：{e}", line_no + 1))?;
                    manifest.rules.push(LibraryRule {
                        name,
                        prefix,
                        all,
                        indices,
                        purpose: cols[4].trim().to_string(),
                    });
                }
                Some("image") if cols.len() == 4 => {
                    let library = cols[1].trim().to_ascii_lowercase();
                    let (all, ids) = parse_selector(cols[2].trim())
                        .map_err(|e| format!("行 {}：{e}", line_no + 1))?;
                    if all {
                        return Err(format!("行 {}：必需图不能写 all", line_no + 1));
                    }
                    for index in ids {
                        manifest.required.push(RequiredImage {
                            library: library.clone(),
                            index,
                            purpose: cols[3].trim().to_string(),
                        });
                    }
                }
                _ => return Err(format!("行 {}：无法解析素材清单记录", line_no + 1)),
            }
        }
        if manifest.rules.is_empty() {
            return Err("素材清单没有 library 规则".into());
        }
        for req in &manifest.required {
            let Some(rule) = manifest
                .rules
                .iter_mut()
                .find(|r| !r.prefix && r.name == req.library)
            else {
                return Err(format!(
                    "必需图 {}[{}] 没有 exact library 规则",
                    req.library, req.index
                ));
            };
            rule.indices.insert(req.index);
        }
        Ok(manifest)
    }

    pub fn compiled() -> Result<Self, String> {
        Self::parse(SOURCE)
    }

    /// 运行时共享清单；只解析一次（每帧每个 sprite 都会查清单，不能反复 parse TSV）。
    pub fn shared() -> Result<&'static Self, &'static str> {
        CLIENT_MANIFEST
            .get_or_init(Self::compiled)
            .as_ref()
            .map_err(String::as_str)
    }

    pub fn rule(&self, library: &str) -> Option<&LibraryRule> {
        let name = library.to_ascii_lowercase();
        self.rules.iter().find(|r| {
            if r.prefix {
                name.starts_with(&r.name)
            } else {
                name == r.name
            }
        })
    }

    pub fn allows_library(&self, library: &str) -> bool {
        self.rule(library).is_some()
    }

    pub fn allows_image(&self, library: &str, index: usize) -> bool {
        self.rule(library)
            .is_some_and(|r| r.all || r.indices.contains(&(index as u32)))
    }
}

fn parse_selector(s: &str) -> Result<(bool, BTreeSet<u32>), String> {
    if s == "all" {
        return Ok((true, BTreeSet::new()));
    }
    let mut out = BTreeSet::new();
    for part in s.split(',').map(str::trim).filter(|p| !p.is_empty()) {
        let mut pair = part.split('-');
        let lo = pair
            .next()
            .unwrap_or_default()
            .parse::<u32>()
            .map_err(|_| format!("无效图号 {part:?}"))?;
        let hi = match pair.next() {
            Some(x) => x
                .parse::<u32>()
                .map_err(|_| format!("无效图号范围 {part:?}"))?,
            None => lo,
        };
        if pair.next().is_some() || hi < lo || hi - lo > 100_000 {
            return Err(format!("无效图号范围 {part:?}"));
        }
        out.extend(lo..=hi);
    }
    if out.is_empty() {
        return Err("空的图号选择".into());
    }
    Ok((false, out))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 编译期清单可解析且覆盖静态引用() {
        let m = ImageManifest::compiled().expect("共享 TSV 清单必须有效");
        assert!(m.allows_image("Prguse", 60));
        for image in [
            assets::LOGIN_BACKGROUND,
            assets::SELECT_BACKGROUND,
            assets::HUD_BACKGROUND_1024,
            assets::HUD_ORB,
            assets::HUD_ORB_SOLO,
            assets::HUD_ORB_SOLO_FILL,
            assets::HUD_PROP_BAR,
            assets::HUD_DIGIT_ZERO,
            assets::BAG_BACKGROUND,
            assets::BAG_CLOSE,
            assets::BAG_GOLD_ICON,
            assets::STATUS_BACKGROUND,
            assets::STATUS_CLOSE,
            assets::STATUS_ARROW_DOWN,
            assets::STATUS_ARROW_UP,
            assets::STATUS_FIGURE_MALE,
            assets::STATUS_FIGURE_FEMALE,
            assets::NPC_DIALOG_BACKGROUND,
            assets::SHOP_BUY_BACKGROUND,
            assets::SHOP_BUY_OK_HOVER,
            assets::SHOP_BUY_NEXT_HOVER,
            assets::SHOP_BUY_PREV_HOVER,
            assets::SHOP_SELL_BACKGROUND,
            assets::SHOP_SELL_OK_HOVER,
        ] {
            assert!(
                m.allows_image(image.library, image.index as usize),
                "{:?}",
                image
            );
        }
        assert!(m.allows_image("Objects29", 123));
        assert!(!m.allows_library("unused-sample-library"));
    }

    #[test]
    fn 精确图号不重排也不意外放行邻图() {
        let m = ImageManifest::parse(
            "library\tprguse3\texact\t690\t1024 底栏\nimage\tprguse3\t690\t底栏\n",
        )
        .unwrap();
        assert!(m.allows_image("Prguse3", 690));
        assert!(!m.allows_image("Prguse3", 689));
        assert!(!m.allows_image("Prguse", 690));
    }

    #[test]
    fn 空清单和坏索引拒绝加载() {
        assert!(ImageManifest::parse("").is_err());
        assert!(ImageManifest::parse("library\tx\texact\t1-x\treason").is_err());
    }
}

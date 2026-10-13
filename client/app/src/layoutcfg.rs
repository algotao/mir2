//! 布局配置的读与写（`assets/ui/layout.json`，D-134 界面开发器）。
//!
//! ⚠️ **刻意不引 serde**：格式就这么一点（对象 → 数组[4]），手写 80 行比拉一个
//! 依赖树划算，也守住"运行期零依赖"（C-7）与体积优先（D-01）。
//!
//! 文件长这样（**只存被改过的项**，缺省靠 `HudLayout::default_layout`）：child 保存 parent-local 矩形，
//! `hud_panel` 保存相对 anchor 的偏移；parent 宽高固定，不写尺寸。
//!
//! ```json
//! {
//!   "plate_hp": [25, 208, 47, 20]
//! }
//! ```

use std::fs;
use std::path::{Path, PathBuf};

use crate::layout::{HudLayout, LRect, HUD_ITEM_IDS};

/// 布局文件的位置（**入库**，调完直接提交）。
pub(crate) fn layout_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/ui/layout.json")
}

/// 解析出来的条目：`(id, [x, y, w, h])`。认不出的条目**整条丢掉**（不猜）。
pub(crate) fn parse(text: &str) -> Vec<(String, [f32; 4])> {
    let b = text.as_bytes();
    let mut out = Vec::new();
    let mut i = 0usize;
    while let Some(q1) = text[i..].find('"') {
        let key_start = i + q1 + 1;
        let Some(q2) = text[key_start..].find('"') else {
            break;
        };
        let key = &text[key_start..key_start + q2];
        let after = key_start + q2 + 1;
        // 键后面必须是 `: [ ... ]`
        let rest = &text[after..];
        let Some(colon) = rest.find(':') else { break };
        let rest = &rest[colon + 1..];
        let Some(b1) = rest.find('[') else { break };
        let Some(b2) = rest[b1..].find(']') else {
            break;
        };
        let body = &rest[b1 + 1..b1 + b2];
        let mut nums = [0f32; 4];
        let mut n = 0usize;
        for part in body.split(',') {
            if n >= 4 {
                break;
            }
            let t = part.trim();
            if t.is_empty() {
                continue;
            }
            match t.parse::<f32>() {
                Ok(v) => {
                    nums[n] = v;
                    n += 1;
                }
                Err(_) => {
                    n = 0;
                    break;
                }
            }
        }
        if n == 4 && HUD_ITEM_IDS.contains(&key) {
            out.push((key.to_string(), nums));
        }
        i = after + colon + 1 + b1 + b2 + 1;
        if i >= b.len() {
            break;
        }
    }
    out
}

/// 解析 parent 的 anchor 属性。缺失时保留默认 BottomLeft。
fn parse_panel_align(text: &str) -> Option<crate::widget::Align> {
    let key = text.find("\"hud_panel_align\"")?;
    let rest = &text[key..];
    let left = rest.find('[')? + 1;
    let right = rest[left..].find(']')? + left;
    let mut axes = rest[left..right]
        .split(',')
        .map(|s| s.trim().trim_matches('"'));
    let parse_axis = |s: &str| match s {
        "start" => Some(crate::widget::AxisAlign::Start),
        "center" => Some(crate::widget::AxisAlign::Center),
        "end" => Some(crate::widget::AxisAlign::End),
        _ => None,
    };
    Some(crate::widget::Align {
        horizontal: parse_axis(axes.next()?)?,
        vertical: parse_axis(axes.next()?)?,
    })
}

fn axis_name(axis: crate::widget::AxisAlign) -> &'static str {
    match axis {
        crate::widget::AxisAlign::Start => "start",
        crate::widget::AxisAlign::Center => "center",
        crate::widget::AxisAlign::End => "end",
    }
}

/// 读文件 → 布局（文件不存在/全认不出 ⇒ `None`，调用方用缺省值）。
pub(crate) fn load(path: &Path) -> Option<HudLayout> {
    let text = fs::read_to_string(path).ok()?;
    let mut l = HudLayout::default_layout();
    let align = parse_panel_align(&text);
    if let Some(align) = align {
        l.panel_align = align;
    }
    let entries = parse(&text);
    let count = entries.len();
    for (id, v) in entries {
        l.set_stored(&id, LRect::new(v[0], v[1], v[2], v[3]));
    }
    if count > 0 || align.is_some() {
        println!(
            "[layout] 已载入 {}（{} 项覆盖）",
            path.display(),
            count + usize::from(align.is_some())
        );
        Some(l)
    } else {
        None
    }
}

/// 写回文件：**只写与缺省不同的项**。没人动过 ⇒ 不落文件（保持仓库干净）。
pub(crate) fn save(path: &Path, cur: &HudLayout) -> std::io::Result<bool> {
    let def = HudLayout::default_layout();
    let mut body = String::new();
    for id in HUD_ITEM_IDS {
        let (a, b) = match (cur.stored_rect(id), def.stored_rect(id)) {
            (Some(a), Some(b)) => (a, b),
            _ => continue,
        };
        if a == b {
            continue;
        }
        body.push_str(&format!(
            "  \"{id}\": [{}, {}, {}, {}],\n",
            num(a.x),
            num(a.y),
            num(a.w),
            num(a.h)
        ));
    }
    if cur.panel_align != def.panel_align {
        body.push_str(&format!(
            "  \"hud_panel_align\": [\"{}\", \"{}\"],\n",
            axis_name(cur.panel_align.horizontal),
            axis_name(cur.panel_align.vertical)
        ));
    }
    if body.is_empty() {
        // 全是缺省值 ⇒ 删掉文件（若存在）
        if path.exists() {
            fs::remove_file(path)?;
        }
        return Ok(false);
    }
    body.pop(); // 最后一个逗号
    body.pop(); // 换行
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
    }
    fs::write(path, format!("{{\n{body}\n}}\n"))?;
    Ok(true)
}

/// 整数就不带小数点（手改文件时好读）。
fn num(v: f32) -> String {
    if v.fract() == 0.0 && v.abs() < 1e7 {
        format!("{}", v as i64)
    } else {
        format!("{:.1}", v)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 解析四个数() {
        let got = parse("{\n  \"plate_hp\": [25, 725, 47, 14]\n}\n");
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].0, "plate_hp");
        assert_eq!(got[0].1, [25.0, 725.0, 47.0, 14.0]);
    }

    #[test]
    fn 空值与坏值整条丢掉() {
        assert!(parse("").is_empty());
        // 少一个数 ⇒ 丢
        assert!(parse("{\"orb\": [1,2,3]}").is_empty());
        // 不是数字 ⇒ 丢
        assert!(parse("{\"orb\": [a,2,3,4]}").is_empty());
        // 不认识的 id ⇒ 丢
        assert!(parse("{\"nope\": [1,2,3,4]}").is_empty());
    }

    #[test]
    fn 小数与负数也认() {
        let got = parse("{\"exp_bar\": [890.5, -3, 76, 13]}");
        assert_eq!(got[0].1, [890.5, -3.0, 76.0, 13.0]);
    }

    #[test]
    fn 只存与缺省不同的项() {
        let mut l = HudLayout::default_layout();
        let def = l.clone();
        let dir = std::env::temp_dir().join("mir2_layout_test");
        let _ = fs::create_dir_all(&dir);
        let p = dir.join("layout.json");

        // 没人动过 ⇒ 不落文件
        assert!(!save(&p, &l).unwrap());
        assert!(!p.exists());

        // 改一项 ⇒ 保存该子控件的 parent-local 矩形。
        let panel = l.panel_rect();
        l.set(
            "plate_hp",
            LRect::new(panel.x + 30.0, panel.y + 120.0, 50.0, 14.0),
        );
        assert!(save(&p, &l).unwrap());
        let text = fs::read_to_string(&p).unwrap();
        assert!(text.contains("\"plate_hp\""));
        assert!(!text.contains("\"orb\""), "没改的项不该写进文件");

        // 读回来一致，且其它项仍是缺省
        let back = load(&p).unwrap();
        assert_eq!(
            back.get("plate_hp"),
            Some(LRect::new(panel.x + 30.0, panel.y + 120.0, 50.0, 14.0))
        );
        assert_eq!(
            fs::read_to_string(&p)
                .unwrap()
                .lines()
                .filter(|line| line.contains("plate_hp"))
                .count(),
            1,
            "配置只应保存单一条目"
        );
        assert_eq!(back.get("orb"), def.get("orb"));
        let _ = fs::remove_file(&p);
    }

    #[test]
    fn parent锚点和局部子项持久化() {
        let mut l = HudLayout::default_layout();
        let mut expected = l.get("orb").unwrap();
        expected.x += 24.0;
        expected.y += 12.0;
        l.set("orb", expected);
        l.panel_align = crate::widget::Align {
            horizontal: crate::widget::AxisAlign::Center,
            vertical: crate::widget::AxisAlign::End,
        };
        let expected_panel = l.panel_rect();
        let expected_orb = l.get("orb").unwrap();
        let expected_local_orb = l.stored_rect("orb").unwrap();

        let path = std::env::temp_dir().join("mir2_layout_parent_test.json");
        let _ = fs::remove_file(&path);
        assert!(save(&path, &l).unwrap());
        let text = fs::read_to_string(&path).unwrap();
        assert!(text.contains("\"hud_panel_align\": [\"center\", \"end\"]"));

        let loaded = load(&path).unwrap();
        assert_eq!(loaded.panel_align, l.panel_align);
        assert_eq!(
            loaded.get(crate::layout::HUD_PANEL_ID),
            Some(expected_panel)
        );
        assert_eq!(loaded.get("orb"), Some(expected_orb));
        assert_eq!(loaded.stored_rect("orb"), Some(expected_local_orb));
        let _ = fs::remove_file(&path);
    }
}

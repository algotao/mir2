//! 布局配置的读与写（`assets/ui/layout.json`，D-134 界面开发器）。
//!
//! ⚠️ **刻意不引 serde**：格式就这么一点（对象 → 数组[4]），手写 80 行比拉一个
//! 依赖树划算，也守住"运行期零依赖"（C-7）与体积优先（D-01）。
//!
//! 文件只保存相对默认值改过的通用 widget 节点；child 保存 parent-local 矩形，根节点保存
//! 相对 viewport/anchor 的偏移。固定尺寸节点不会写入可变尺寸。
//!
//! ```json
//! {
//!   "plate_hp": [25, 208, 47, 20]
//! }
//! ```

use std::fs;
use std::path::{Path, PathBuf};

use crate::layout::{layout_item_ids, LRect, LayoutDocument};

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
        if n == 4
            && layout_item_ids().contains(&key)
            && nums.iter().all(|value| value.is_finite())
            && nums[2] > 0.0
            && nums[3] > 0.0
        {
            out.push((key.to_string(), nums));
        }
        i = after + colon + 1 + b1 + b2 + 1;
        if i >= b.len() {
            break;
        }
    }
    out
}

/// 通用节点锚点键为 `{id}_align`；`hud_panel_align` 与旧格式保持兼容。
fn parse_align(text: &str, id: &str) -> Option<crate::widget::Align> {
    let key = format!("\"{id}_align\"");
    let start = text.find(&key)?;
    let rest = &text[start + key.len()..];
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
pub(crate) fn load(path: &Path) -> Option<LayoutDocument> {
    let text = fs::read_to_string(path).ok()?;
    let mut l = LayoutDocument::default_layout();
    let entries = parse(&text);
    let mut overrides = entries.len();
    for (id, v) in entries {
        l.set_stored(&id, LRect::new(v[0], v[1], v[2], v[3]));
    }
    for id in layout_item_ids().iter().copied() {
        if let Some(align) = parse_align(&text, id) {
            l.set_align(id, align);
            overrides += 1;
        }
    }
    if overrides > 0 {
        println!("[layout] 已载入 {}（{} 项覆盖）", path.display(), overrides);
        Some(l)
    } else {
        None
    }
}

/// 写回文件：**只写与缺省不同的项**。没人动过 ⇒ 不落文件（保持仓库干净）。
pub(crate) fn save(path: &Path, cur: &LayoutDocument) -> std::io::Result<bool> {
    let def = LayoutDocument::default_layout();
    let mut body = String::new();
    for id in layout_item_ids().iter().copied() {
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
    for id in layout_item_ids().iter().copied() {
        let (Some(current), Some(default)) = (cur.align(id), def.align(id)) else {
            continue;
        };
        if current != default {
            body.push_str(&format!(
                "  \"{id}_align\": [\"{}\", \"{}\"],\n",
                axis_name(current.horizontal),
                axis_name(current.vertical)
            ));
        }
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
    fn 小数与负数也认但拒绝非有限或非正尺寸() {
        let got = parse("{\"exp_bar\": [890.5, -3, 76, 13]}");
        assert_eq!(got[0].1, [890.5, -3.0, 76.0, 13.0]);
        assert!(parse("{\"exp_bar\": [NaN, 0, 76, 13]}").is_empty());
        assert!(parse("{\"exp_bar\": [1, 2, 0, 13]}").is_empty());
        assert!(parse("{\"exp_bar\": [1, 2, 76, -1]}").is_empty());
    }

    #[test]
    fn 只存与缺省不同的项() {
        let mut l = LayoutDocument::default_layout();
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
        let mut l = LayoutDocument::default_layout();
        let mut expected = l.get("orb").unwrap();
        expected.x += 24.0;
        expected.y += 12.0;
        l.set("orb", expected);
        l.set_align(
            "hud_panel",
            crate::widget::Align {
                horizontal: crate::widget::AxisAlign::Center,
                vertical: crate::widget::AxisAlign::End,
            },
        );
        l.set_align(
            "orb",
            crate::widget::Align {
                horizontal: crate::widget::AxisAlign::Center,
                vertical: crate::widget::AxisAlign::Start,
            },
        );
        let expected_panel = l.panel_rect();
        let expected_orb = l.get("orb").unwrap();
        let expected_local_orb = l.stored_rect("orb").unwrap();

        let path = std::env::temp_dir().join("mir2_layout_parent_test.json");
        let _ = fs::remove_file(&path);
        assert!(save(&path, &l).unwrap());
        let text = fs::read_to_string(&path).unwrap();
        assert!(text.contains("\"hud_panel_align\": [\"center\", \"end\"]"));
        assert!(text.contains("\"orb_align\": [\"center\", \"start\"]"));

        let loaded = load(&path).unwrap();
        assert_eq!(loaded.align("hud_panel"), l.align("hud_panel"));
        assert_eq!(
            loaded.get(crate::layout::HUD_PANEL_ID),
            Some(expected_panel)
        );
        assert_eq!(loaded.get("orb"), Some(expected_orb));
        assert_eq!(loaded.stored_rect("orb"), Some(expected_local_orb));
        assert_eq!(loaded.align("orb"), l.align("orb"));
        let _ = fs::remove_file(&path);
    }

    #[test]
    fn 通用节点配置持久化状态窗和对话窗位置锚点() {
        let mut l = LayoutDocument::default_layout();
        let status = l.get("window_status").unwrap();
        l.set(
            "window_status",
            LRect::new(status.x - 24.0, status.y + 13.0, status.w, status.h),
        );
        l.set_align(
            "window_status",
            crate::widget::Align {
                horizontal: crate::widget::AxisAlign::Center,
                vertical: crate::widget::AxisAlign::Start,
            },
        );
        let dialog = l.get("window_dialog").unwrap();
        l.set(
            "window_dialog",
            LRect::new(dialog.x + 31.0, dialog.y + 19.0, dialog.w, dialog.h),
        );
        let expected_status = l.get("window_status").unwrap();
        let expected_dialog = l.get("window_dialog").unwrap();

        let path = std::env::temp_dir().join("mir2_layout_generic_windows.json");
        let _ = fs::remove_file(&path);
        assert!(save(&path, &l).unwrap());
        let text = fs::read_to_string(&path).unwrap();
        assert!(text.contains("\"window_status\""));
        assert!(text.contains("\"window_status_align\": [\"center\", \"start\"]"));
        assert!(text.contains("\"window_dialog\""));

        let loaded = load(&path).unwrap();
        assert_eq!(loaded.get("window_status"), Some(expected_status));
        assert_eq!(loaded.get("window_dialog"), Some(expected_dialog));
        assert_eq!(loaded.align("window_status"), l.align("window_status"));
        let _ = fs::remove_file(path);
    }

    #[test]
    fn 登录门和选角节点稀疏配置往返() {
        let mut layout = LayoutDocument::default_layout();
        let panel = layout.get("login.panel").unwrap();
        layout.set(
            "login.panel",
            LRect::new(panel.x + 12.0, panel.y + 7.0, panel.w, panel.h),
        );
        let door = layout.stored_rect("login.door").unwrap();
        layout.set_stored(
            "login.door",
            LRect::new(door.x - 5.0, door.y + 9.0, door.w, door.h),
        );
        let account = layout.stored_rect("login.account").unwrap();
        layout.set_stored(
            "login.account",
            LRect::new(account.x + 3.0, account.y + 2.0, account.w, account.h),
        );
        let select_panel = layout.get("select.panel").unwrap();
        layout.set(
            "select.panel",
            LRect::new(
                select_panel.x + 8.0,
                select_panel.y + 4.0,
                select_panel.w,
                select_panel.h,
            ),
        );
        let slot = layout.stored_rect("select.slot.left").unwrap();
        layout.set_stored(
            "select.slot.left",
            LRect::new(slot.x + 6.0, slot.y - 3.0, slot.w, slot.h),
        );
        let baked_menu = layout.stored_rect("select.menu.create").unwrap();
        layout.set_stored(
            "select.menu.create",
            LRect::new(
                baked_menu.x + 5.0,
                baked_menu.y + 2.0,
                baked_menu.w,
                baked_menu.h,
            ),
        );
        assert_eq!(
            layout.stored_rect("select.menu.create"),
            Some(baked_menu),
            "背景烘焙菜单忽略脱离背景的 stale layout override"
        );
        let expected = [
            ("login.panel", layout.get("login.panel").unwrap()),
            ("login.account", layout.get("login.account").unwrap()),
            ("login.door", layout.get("login.door").unwrap()),
            ("select.panel", layout.get("select.panel").unwrap()),
            ("select.slot.left", layout.get("select.slot.left").unwrap()),
        ];

        let path = std::env::temp_dir().join("mir2_layout_login_select_test.json");
        let _ = fs::remove_file(&path);
        assert!(save(&path, &layout).unwrap());
        let text = fs::read_to_string(&path).unwrap();
        for (id, _) in expected {
            assert!(text.contains(&format!("\"{id}\"")), "缺少持久化项 {id}");
        }
        assert!(
            !text.contains("select.menu.create"),
            "背景烘焙菜单节点的旧 override 不应重新持久化"
        );
        let loaded = load(&path).unwrap();
        for (id, rect) in expected {
            assert_eq!(loaded.get(id), Some(rect), "{id} 载入后要保持同一解析矩形");
        }
        let _ = fs::remove_file(path);
    }

    #[test]
    fn 背景烘焙菜单的历史坐标覆盖载入后被清理() {
        let path = std::env::temp_dir().join("mir2_layout_baked_menu_stale.json");
        fs::write(
            &path,
            "{\n  \"select.menu.create\": [352, 498, 120, 21]\n}\n",
        )
        .unwrap();
        let loaded = load(&path).expect("识别到历史菜单布局项");
        let defaults = LayoutDocument::default_layout();
        assert_eq!(
            loaded.stored_rect("select.menu.create"),
            defaults.stored_rect("select.menu.create")
        );
        assert!(!save(&path, &loaded).unwrap());
        assert!(!path.exists(), "无效的 baked menu override 应从配置清掉");
    }
}

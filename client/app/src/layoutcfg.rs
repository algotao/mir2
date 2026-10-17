//! 布局配置的读与写（`assets/ui/layout.json`，D-134 界面开发器）。
//!
//! ⚠️ **刻意不引 serde**：格式就这么一点（对象 → 数组[4]），手写 80 行比拉一个
//! 依赖树划算，也守住"运行期零依赖"（C-7）与体积优先（D-01）。
//!
//! 文件只保存相对默认值改过的通用 widget 节点；child 保存 parent-local 矩形，根节点保存
//! 相对 viewport/anchor 的偏移。固定尺寸节点不会写入可变尺寸。`schema_version` 标记坐标语义。
//!
//! ```json
//! {
//!   "schema_version": 1,
//!   "plate_hp": [25, 208, 47, 20]
//! }
//! ```

use std::fs;
use std::path::{Path, PathBuf};

use crate::layout::{layout_item_ids, LRect, LayoutDocument};

const CURRENT_SCHEMA_VERSION: u32 = 1;

/// 布局文件的位置（**入库**，调完直接提交）。
pub(crate) fn layout_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../assets/ui/layout.json")
}

/// 解析数组型矩形条目；标量元数据会跳过，不会吞掉后面的布局项。
pub(crate) fn parse(text: &str) -> Vec<(String, [f32; 4])> {
    let bytes = text.as_bytes();
    let mut out = Vec::new();
    let mut cursor = 0usize;
    while cursor < bytes.len() {
        let Some(quote) = text[cursor..].find('"') else {
            break;
        };
        let key_start = cursor + quote + 1;
        let Some(key_len) = text[key_start..].find('"') else {
            break;
        };
        let key = &text[key_start..key_start + key_len];
        let mut value = key_start + key_len + 1;
        while value < bytes.len() && bytes[value].is_ascii_whitespace() {
            value += 1;
        }
        if value >= bytes.len() || bytes[value] != b':' {
            cursor = value.saturating_add(1);
            continue;
        }
        value += 1;
        while value < bytes.len() && bytes[value].is_ascii_whitespace() {
            value += 1;
        }

        if value < bytes.len() && bytes[value] == b'[' {
            let body_start = value + 1;
            let Some(end_rel) = text[body_start..].find(']') else {
                break;
            };
            let end = body_start + end_rel;
            let parts: Vec<_> = text[body_start..end]
                .split(',')
                .map(str::trim)
                .filter(|part| !part.is_empty())
                .collect();
            if parts.len() == 4 {
                let parsed: Option<Vec<f32>> =
                    parts.iter().map(|part| part.parse::<f32>().ok()).collect();
                if let Some(values) = parsed {
                    let rect = [values[0], values[1], values[2], values[3]];
                    if layout_item_ids().contains(&key)
                        && rect.iter().all(|number| number.is_finite())
                        && rect[2] > 0.0
                        && rect[3] > 0.0
                    {
                        out.push((key.to_string(), rect));
                    }
                }
            }
            cursor = end + 1;
        } else {
            // 例如 `"schema_version": 1`。跳到该属性分隔符再继续找下一个 key。
            while value < bytes.len() && bytes[value] != b',' && bytes[value] != b'}' {
                value += 1;
            }
            cursor = value.saturating_add(1);
        }
    }
    out
}

/// 无 version 字段的旧配置按 v0 屏幕绝对矩形迁移；未知/损坏版本拒绝加载。
fn schema_version(text: &str) -> Result<u32, ()> {
    let Some(key) = text.find("\"schema_version\"") else {
        return Ok(0);
    };
    let rest = &text[key + "\"schema_version\"".len()..];
    let colon = rest.find(':').ok_or(())?;
    let value = rest[colon + 1..].trim_start();
    let digits = value.bytes().take_while(u8::is_ascii_digit).count();
    if digits == 0 {
        return Err(());
    }
    let suffix = value[digits..].trim_start();
    if !suffix.starts_with(',') && !suffix.starts_with('}') {
        return Err(());
    }
    value[..digits].parse().map_err(|_| ())
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
    let version = schema_version(&text).ok()?;
    if version > CURRENT_SCHEMA_VERSION {
        return None;
    }
    let mut l = LayoutDocument::default_layout();
    let mut entries = parse(&text);
    let mut overrides = entries.len();

    // 锚点先于矩形应用。v0 是旧的解析后绝对坐标，且 parent 必须先转换，
    // 再将 children 反算为 parent-local；v1 起数组本身就是存储坐标。
    for id in layout_item_ids().iter().copied() {
        if let Some(align) = parse_align(&text, id) {
            l.set_align(id, align);
            overrides += 1;
        }
    }
    if version == 0 {
        let ids = layout_item_ids();
        entries.sort_by_key(|(id, _)| {
            ids.iter()
                .position(|candidate| *candidate == id)
                .unwrap_or(usize::MAX)
        });
    }
    for (id, values) in entries {
        let rect = LRect::new(values[0], values[1], values[2], values[3]);
        if version == 0 {
            l.set(&id, rect);
        } else {
            l.set_stored(&id, rect);
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
    fs::write(
        path,
        format!("{{\n  \"schema_version\": {CURRENT_SCHEMA_VERSION},\n{body}\n}}\n"),
    )?;
    Ok(true)
}

/// 整数就不带小数点（手改文件时好读）；非整数用 `f32` 的**最短往返**表示。
///
/// ⚠️ 别改成 `{:.1}`：设计空间里存在 160.625 这种坐标（门框按 1.15 拟合后就不是整数了），
/// 只留一位小数会在"存盘→读回"时漂 0.025，解析矩形和默认值不再相等。
fn num(v: f32) -> String {
    if v.fract() == 0.0 && v.abs() < 1e7 {
        format!("{}", v as i64)
    } else {
        format!("{v}")
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
    fn 标量schema版本不会跳过第一个布局节点() {
        let got = parse(
            "{\n  \"schema_version\": 1,\n  \"orb_align\": [\"center\", \"start\"],\n  \"plate_hp\": [25, 208, 47, 20]\n}",
        );
        assert_eq!(got, [("plate_hp".to_string(), [25.0, 208.0, 47.0, 20.0])]);
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

    #[test]
    fn v0绝对坐标按parent先后迁移并写回v1局部坐标() {
        let path = std::env::temp_dir().join("mir2_layout_schema_migration.json");
        let mut expected = LayoutDocument::default_layout();
        let align = crate::widget::Align {
            horizontal: crate::widget::AxisAlign::Center,
            vertical: crate::widget::AxisAlign::End,
        };
        expected.set_align("hud_panel", align);
        let default_panel = expected.get("hud_panel").unwrap();
        let absolute_panel = LRect::new(
            default_panel.x + 16.0,
            default_panel.y - 8.0,
            default_panel.w,
            default_panel.h,
        );
        expected.set("hud_panel", absolute_panel);
        let default_hp = expected.get("plate_hp").unwrap();
        let absolute_hp = LRect::new(
            default_hp.x + 5.0,
            default_hp.y + 3.0,
            default_hp.w,
            default_hp.h,
        );
        expected.set("plate_hp", absolute_hp);

        // 故意 child 在 parent 前：迁移器必须按 spec 顺序转换绝对坐标。
        let legacy = format!(
            "{{\n  \"plate_hp\": [{}, {}, {}, {}],\n  \"hud_panel\": [{}, {}, {}, {}],\n  \"hud_panel_align\": [\"center\", \"end\"]\n}}\n",
            num(absolute_hp.x),
            num(absolute_hp.y),
            num(absolute_hp.w),
            num(absolute_hp.h),
            num(absolute_panel.x),
            num(absolute_panel.y),
            num(absolute_panel.w),
            num(absolute_panel.h),
        );
        fs::write(&path, legacy).unwrap();
        let migrated = load(&path).expect("v0 配置可迁移");
        assert_eq!(migrated.get("hud_panel"), Some(absolute_panel));
        assert_eq!(migrated.get("plate_hp"), Some(absolute_hp));
        assert_eq!(migrated.align("hud_panel"), Some(align));

        assert!(save(&path, &migrated).unwrap());
        let written = fs::read_to_string(&path).unwrap();
        assert!(written.contains("\"schema_version\": 1"));
        let reloaded = load(&path).expect("v1 parent-local 配置应可读回");
        assert_eq!(reloaded.get("hud_panel"), Some(absolute_panel));
        assert_eq!(reloaded.get("plate_hp"), Some(absolute_hp));
        assert_eq!(reloaded.align("hud_panel"), Some(align));
        let _ = fs::remove_file(path);
    }

    #[test]
    fn 拒绝不支持或格式错误的schema版本() {
        let path = std::env::temp_dir().join("mir2_layout_future_schema.json");
        fs::write(
            &path,
            "{\n  \"schema_version\": 99,\n  \"plate_hp\": [1, 2, 3, 4]\n}\n",
        )
        .unwrap();
        assert!(load(&path).is_none());
        fs::write(
            &path,
            "{\n  \"schema_version\": 1oops,\n  \"plate_hp\": [1, 2, 3, 4]\n}\n",
        )
        .unwrap();
        assert!(load(&path).is_none());
        let _ = fs::remove_file(path);
    }

    #[test]
    fn 仓库内无版本布局迁移后控件仍在画布范围() {
        let path = layout_path();
        let layout = load(&path).expect("仓库布局文件可读");
        for id in ["plate_hp", "plate_mp", "level_num", "atk_text", "clock"] {
            let rect = layout.get(id).expect("登记的 HUD 节点应有布局");
            assert!(
                rect.x >= 0.0
                    && rect.y >= 0.0
                    && rect.x + rect.w <= crate::window::WIN_W as f32
                    && rect.y + rect.h <= crate::window::WIN_H as f32,
                "v0 坐标迁移后 {id} 应仍在画布范围：{rect:?}"
            );
        }
    }
}

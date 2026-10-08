//! `mir2-e2e login` —— **无头合成登录界面**。
//!
//! 这是"照原版"这条要求的**可视验收**：版式来自 `mir2_core::login_ui`（app 用的是
//! 同一份），素材来自真容器，合成出一张 PNG 给人看。
//!
//! ```text
//! cargo run -p mir2-e2e -- login -out /tmp/login.png
//! cargo run -p mir2-e2e -- login -error "Please enter your account name." -out /tmp/err.png
//! ```
//!
//! ⚠️ 这里**不画文字**（无头合成器没有字体）：输入框画成黑底方块，报错弹窗只画框。
//! 文字由 app 用 SDL3 的 8x8 字体画 —— 也就是说这张图能验"素材与版式摆对了"，
//! 验不了"字写在哪儿"。

use std::collections::HashMap;
use std::path::Path;

use mir2_core::login_ui::{Art, Layout, Rect};
use mir2_core::paths;
use mir2_core::wzl::Wzl;

use crate::canvas::Canvas;
use crate::png;

/// 取一张界面图的 RGBA（容器按需打开并缓存）。
struct Libs<'a> {
    dir: &'a Path,
    open: HashMap<&'static str, Option<Wzl>>,
}

impl<'a> Libs<'a> {
    fn new(dir: &'a Path) -> Self {
        Self {
            dir,
            open: HashMap::new(),
        }
    }

    fn lib(&mut self, name: &'static str) -> Option<&Wzl> {
        self.open
            .entry(name)
            .or_insert_with(|| Wzl::open(self.dir.join(name)).ok())
            .as_ref()
    }

    /// 只读容器头拿尺寸（与 app 的 `UiCache::size` 同一个来源）。
    fn size(&mut self, name: &'static str, idx: u32) -> Option<(u32, u32)> {
        let r = self.lib(name)?.record(idx as usize)?;
        Some((r.width as u32, r.height as u32))
    }

    fn blit(&mut self, cv: &mut Canvas, name: &'static str, idx: u32, x: i32, y: i32) -> bool {
        let Some(s) = self.lib(name).and_then(|w| w.decode(idx as usize)) else {
            println!("  ⚠️ {name}[{idx}] 取不出图（缺失或空壳）—— 跳过");
            return false;
        };
        cv.blit_rgba(&s.rgba, s.width as i32, s.height as i32, x, y);
        true
    }

    /// 居中贴（原版居中就是 `(SCRW-w)/2`）。
    fn blit_centered(
        &mut self,
        cv: &mut Canvas,
        name: &'static str,
        idx: u32,
        win: (i32, i32),
    ) -> bool {
        let Some((w, h)) = self.size(name, idx) else {
            println!("  ⚠️ {name}[{idx}] 量不到尺寸 —— 跳过");
            return false;
        };
        let (x, y) = Layout::bg_at((win.0 as u32, win.1 as u32), (w, h));
        self.blit(cv, name, idx, x as i32, y as i32)
    }
}

/// 输入框：黑底方块（原版是原生 `TEdit`，素材里没有这张图；文字由 app 画）。
fn field_box(cv: &mut Canvas, r: Rect) {
    cv.fill_rect(r.x as i32, r.y as i32, r.w as i32, r.h as i32, [0, 0, 0]);
}

pub fn main(argv: &[String]) -> Result<(), String> {
    let mut out = "/tmp/login.png".to_string();
    let mut win = (1024i32, 768i32);
    let mut err: Option<String> = None;
    // `-door <ms>`：合成"登录已通过、门正在开"的那一屏（**登录小窗必须消失**）。
    let mut door_ms: Option<u64> = None;
    let mut i = 0;
    while i < argv.len() {
        match argv[i].as_str() {
            "-out" => {
                i += 1;
                out = argv.get(i).ok_or("-out 缺参数")?.clone();
            }
            "-door" => {
                i += 1;
                door_ms = Some(
                    argv.get(i)
                        .ok_or("-door 缺参数（毫秒）")?
                        .parse()
                        .map_err(|_| "-door 要是整数毫秒")?,
                );
            }
            "-w" => {
                i += 1;
                win.0 = argv
                    .get(i)
                    .ok_or("-w 缺参数")?
                    .parse()
                    .map_err(|_| "-w 要是整数")?;
            }
            "-h" => {
                i += 1;
                win.1 = argv
                    .get(i)
                    .ok_or("-h 缺参数")?
                    .parse()
                    .map_err(|_| "-h 要是整数")?;
            }
            "-error" => {
                i += 1;
                err = Some(argv.get(i).ok_or("-error 缺参数")?.clone());
            }
            other => return Err(format!("未知参数 {other}（见 mir2-e2e 用法）")),
        }
        i += 1;
    }

    let dir = paths::asset_dir().ok_or("找不到素材目录（设 MIR2_ASSET_DIR 或 MIR2C_DATA）")?;
    let mut libs = Libs::new(&dir);

    // 版式：与 app **同一份**（`core::login_ui`）
    let l = Layout::build((win.0 as u32, win.1 as u32), |c, i| libs.size(c, i))
        .ok_or("登录素材不全（需要 Prguse[60/61/62/64/360/363]）")?;

    let mut cv = Canvas::new(win.0, win.1, [10, 14, 28]);

    // ① 背景（ChrSel[22]）
    if !libs.blit_centered(&mut cv, Art::BG.0, Art::BG.1, win) {
        println!("提示：背景缺失，只画对话框");
    }
    // ② 开门动画（`-door <ms>`）：原版 `OpenLoginDoor` = **先藏小窗**再开门
    //（`IntroScn.pas:795-801`：`HideLoginBox` → `PlaySound(s_rock_door_open)`），
    // 门的位置照 `IntroScn.pas:845-846` 的偏移（局部覆盖，不是居中）。
    if let Some(ms) = door_ms {
        let f = (ms / Art::DOOR_MS as u64) as u32;
        let idx = Art::DOOR.1 + f.min(Art::DOOR_FRAMES - 1);
        let (x, y) = Layout::legacy_at((win.0 as u32, win.1 as u32), Layout::DOOR_AT);
        let ok = libs.blit(&mut cv, Art::DOOR.0, idx, x as i32, y as i32);
        println!(
            "  开门第 {f} 帧（{ms}ms / 每帧 {}ms）= {}[{}] @ ({:.0},{:.0}){}",
            Art::DOOR_MS,
            Art::DOOR.0,
            idx,
            x,
            y,
            if ok {
                ""
            } else {
                "  ← 空壳帧（本套素材 [23] 就是空壳）"
            }
        );
        png::write_rgb(Path::new(&out), win.0 as u32, win.1 as u32, &cv.to_rgb())
            .map_err(|e| format!("写 {out}: {e}"))?;
        println!("开门屏合成完成：{out}（登录小窗**没画** —— 与 app 的 login::draw 同一规则）");
        return Ok(());
    }

    // ③ 对话框
    libs.blit(
        &mut cv,
        Art::DIALOG.0,
        Art::DIALOG.1,
        l.dialog.x as i32,
        l.dialog.y as i32,
    );
    // ③ 输入框（黑底）
    field_box(&mut cv, l.account);
    field_box(&mut cv, l.password);
    // ④ 三颗按钮
    for (name, idx, r) in [
        ("[提交]", Art::BTN_OK.1, l.ok),
        ("[新用户]", Art::BTN_NEW.1, l.new),
        ("[修改密码]", Art::BTN_CHGPW.1, l.chgpw),
        ("[X]", Art::BTN_CLOSE.1, l.close),
    ] {
        let ok = libs.blit(&mut cv, Art::DIALOG.0, idx, r.x as i32, r.y as i32);
        println!(
            "  {name}: {}[{}] 对话框内偏移=({:.0},{:.0}) 尺寸={:.0}x{:.0}{}",
            Art::DIALOG.0,
            idx,
            r.x - l.dialog.x,
            r.y - l.dialog.y,
            r.w,
            r.h,
            if ok { "" } else { "  ← 取不到图" }
        );
    }
    // ⑤ 报错弹窗（可选）
    if let Some(text) = &err {
        libs.blit(
            &mut cv,
            Art::MSGBOX.0,
            Art::MSGBOX.1,
            l.msgbox.x as i32,
            l.msgbox.y as i32,
        );
        libs.blit(
            &mut cv,
            Art::MSGBOX_OK.0,
            Art::MSGBOX_OK.1,
            l.msg_ok.x as i32,
            l.msg_ok.y as i32,
        );
        println!("  弹窗文字（app 画）：{text:?}");
    }

    png::write_rgb(Path::new(&out), win.0 as u32, win.1 as u32, &cv.to_rgb())
        .map_err(|e| format!("写 {out}: {e}"))?;
    println!(
        "登录界面合成完成：{out}（{}x{}）\n  对话框 {:.0}x{:.0} @ ({:.0},{:.0})  用户名框 ({:.0},{:.0})  密码框 ({:.0},{:.0})",
        win.0,
        win.1,
        l.dialog.w,
        l.dialog.h,
        l.dialog.x,
        l.dialog.y,
        l.account.x,
        l.account.y,
        l.password.x,
        l.password.y,
    );
    Ok(())
}

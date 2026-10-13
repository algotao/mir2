//! MIR2 1.76 客户端 —— 开发期查看器（两个模式）
//!
//! * **登录界面**：窗口 / 文本输入 / 2D 渲染（M0 的 SDL3 落地验证）
//! * **地图视图**：从 M2PK 容器加载真实 `.map`，绘制**三层**（地表 `Tiles` /
//!   中间 `SmTiles` / 前景 `Objects<N>`）—— M1「地图加载」的验收
//!
//! 渲染几何来自官方客户端 `Grobal2.pas:45`：`UNITX=48`、`UNITY=32`（逻辑格 48×32）。
//! 实测图块尺寸：`Tiles` = 96×64（**2×2 格** ⇒ 只在 x、y 皆为偶数的格上画）、
//! `SmTiles` = 48×32、`Objects` = 48×宽×不定高（后两者每格都画）。
//!
//! 资产目录解析：`$MIR2_ASSET_DIR` → `$MIR2C_DATA` → 仓库旁 `mir2c/data`；
//! 容器路径：`$MIR2_MAP_CONTAINER` → `assets/map/maps.m2pk`。找不到就降级显示，不崩。
//!
//! 屏幕文字用 SDL3 内置 8x8 调试字体，**只认 ASCII**。
//!
//! **音频**（`M` 切音乐 / `N` 切音效）：
//!
//! * **规格**在 `mir2_core::sound`（原版 `SoundUtil.pas` 的编号表、`Actor.pas` 的地形→脚步、
//!   `sound.lst` 的"编号 → 文件"）；**发声**在 `audio.rs`（SDL3 软件混音：多路叠加 + 循环）；
//! * 音频目录：`$MIR2_AUDIO_DIR` / `$MIR2C_WAV` → 美术目录旁 → 仓库旁的 `mir2c/wav`；
//! * 场景 BGM：登录 `log-in-long2.wav`、选角 `sellect-loop2.wav`、自己死亡 `game over2.wav`
//!   （`SoundUtil.pas:31-34`）；
//! * ⚠️ **进图音乐没接**：原版走"服务端下发地图音乐号 + `Music/<号>.mp3`"
//!   （`SoundUtil.pas:219`、`ClMain.pas:5222`），我们协议里还没有那个字段、
//!   手上也没有 mp3（`sound::map_music` 里有实测）。
//!
//! **连服务端**（B 阶段，`C` 键或环境变量）：
//!
//! ```text
//! MIR2_SERVER=127.0.0.1:7500 MIR2_SESSION=7 cargo run -p mir2-app
//! ```
//!
//! * `MIR2_SESSION` 是**已认证的会话号** —— 新协议的 `Login` 还没实现
//!   （口令怎么过网络未定，见 D-24），所以只能认领一个既有会话；
//! * 连上之后：相机跟着自己、方向键 = 走一步（离线时仍是平移镜头）、
//!   视野内的实体画成**标记**（位置/朝向/名字/血量）；
//! * 实体画的是**真精灵**（角色/怪物/**NPC**）：图号公式在 `mir2_core::actor`（原版逐条
//!   翻译，出处都注在那边）。取不到精灵时**退回标记**（`draw_entity_marker`）。
//!   本套素材**真正缺的只有头发库**（官方要 `Hair.wil`，不存在；但有 `hair2.wzl` 待验）
//!   与 `Dragon.wil` —— `Npc.wzl` / `HumEffect` / `WeaponEffect` / `StateItem` **都有真数据**。
//!   ⚠️ 早先这里写"NPC 要 `Npc.wzl`、本套素材缺失"是**错的**（2026-10-09 更正：
//!   没核验就记了结论）。来源与核验命令见 `docs/authority.md`。

#[cfg(test)]
use std::collections::HashMap;
#[cfg(test)]
use std::time::{Duration, Instant};

use mir2_core::m2pk::Archive;
use mir2_core::map::{TileDraw, LAYERS_ALL, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::event::{Event, WindowEvent};
use sdl3::mouse::{Cursor, MouseButton, SystemCursor};

mod audio;
mod font;
mod login;
mod login_view;
mod select;
mod ui;
// 注：`WindowContext` 在 sdl3 里是私有类型、不可具名，
// 故凡是需要纹理创建器的地方一律对类型参数 `T` 泛化。
use sdl3::render::{FRect, Texture};
use sdl3::EventPump;

// ---------- 拆分出来的模块（2026-10-09）----------
//
// 原来 `main.rs` 一个文件 5700 行：几何/图块/actor/HUD/网络/输入/决策全挤在一起。
// 现在按职责切开，本文件只留**入口与主循环**（下一步可以把主循环再收进一个 `Game` 状态机）。
pub(crate) mod window;
pub(crate) use window::*;

pub(crate) mod assets;
pub(crate) use assets::*;
pub(crate) mod colors;
pub(crate) use colors::*;
pub(crate) mod layout;
mod layoutcfg;
pub(crate) use layout::*;
pub(crate) mod gfx;
pub(crate) use gfx::*;
pub(crate) mod geom;
pub(crate) use geom::*;
pub(crate) mod tiles;
pub(crate) use tiles::*;
pub(crate) mod debug;
mod dialog;
pub(crate) use debug::*;
pub(crate) mod actor;
pub(crate) use actor::*;
mod app_state;
pub(crate) mod assetview;
mod assetview_state;
mod bagdrag;
mod confirm;
pub(crate) mod hud;
pub(crate) mod minimap;
mod pager;
mod render;
mod runtime;
mod scene_input;
mod scrollview;
pub(crate) mod shop;
mod shopui;
mod simulation;
mod space;
pub(crate) mod status;
mod update;
mod windows;
mod world_control;
pub(crate) mod world_input;
pub(crate) use minimap::*;
pub(crate) mod net;
pub(crate) use net::*;
pub(crate) mod input;
mod keyboard;
mod layoutedit;
mod network_tick;
mod overlay_state;
pub(crate) use input::*;
pub(crate) mod flow;
pub(crate) use flow::*;
pub(crate) mod sfx;
pub(crate) use sfx::*;
pub(crate) mod world;
pub(crate) use world::*;
// ⚠️ 必须带 `#[cfg(test)]`：不然 `tests.rs`（里面全是 `#[test]`）会被编进正式二进制，
// 它的 `use` 在非测试构建里就「没用」—— `cargo fix` 会顺手把它们删掉，测试目标随后
// 直接编译不过（这个坑刚踩过）。拆文件时最容易漏的一处。
#[cfg(test)]
mod tests;
mod widget;
mod widget_state;
pub(crate) use widget_state::{Press, Ui};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // ---------- 命令行：`--assetview [库名]` ⇒ 打开**素材观察器**而不是进游戏 ----------
    //
    // 找素材用的：图号是散在代码里的魔法数字（`Prguse[385]`…），人眼认不出第 N 张
    // 长什么样。摊开图库、点一下就显示 ID 并复制。
    let argv: Vec<String> = std::env::args().collect();
    if let Some(pos) = argv.iter().position(|a| a == "--assetview" || a == "-a") {
        let lib = argv.get(pos + 1).filter(|s| !s.starts_with('-')).cloned();
        // F3 观察器只看客户端 images.m2pk 里、清单允许的图库；不扫描/回退裸 WZL。
        let dir = mir2_core::paths::asset_dir().unwrap_or_else(|| std::path::PathBuf::from("."));
        let libs = assetview::list_libs(&dir);
        if libs.is_empty() {
            eprintln!("images.m2pk 缺失/无清单图库；运行 tools/artpack/build.sh");
            std::process::exit(2);
        }
        let want = lib.unwrap_or_else(|| "Prguse".to_string());
        let idx = libs.iter().position(|l| *l == want).unwrap_or(0);
        return assetview::run(&dir, &libs, idx);
    }

    let sdl = sdl3::init()?;
    let video = sdl.video()?;

    let (win_w, win_h) = window_size();
    let mut window = video
        .window("MIR2 1.76 CLIENT - DEV VIEWER", win_w, win_h)
        .position_centered()
        // 可拉大拉小：逻辑呈现会把 800×600 的界面**等比**铺到新尺寸（见 [`WIN_W`]）
        .resizable()
        .build()
        .map_err(|e| format!("创建窗口失败: {e}"))?;

    video.text_input().start(&window);
    // 屏幕放不下"1024×768 的**客户区**"时（1024×768 的屏加上标题栏/菜单栏就放不下），
    // 把窗口夹进**可用区域**：宁可窗口小一点、画面按 1:1 画、多出去的边裁掉，
    // 也不把整屏缩小或拉变形（用户 2026-10-09 第 4 条）。
    if let Some(u) = usable_bounds(&video) {
        let fit = fit_window((win_w, win_h), u);
        if fit != (win_w, win_h) {
            println!(
                "[mir2-app] 屏幕可用区域 {}×{} 装不下 {}×{} ⇒ 窗口取 {}×{}（画面 1:1、多出的边裁掉）",
                u.0, u.1, win_w, win_h, fit.0, fit.1
            );
            window
                .set_size(fit.0, fit.1)
                .map_err(|e| format!("调整窗口大小失败: {e}"))?;
        }
    }
    let mut canvas = window.into_canvas();
    apply_presentation(&mut canvas)?;
    let tex_creator = canvas.texture_creator();

    // ---------- 音频 ----------
    // 规格（编号表 / 地形 → 脚步 / `sound.lst`）在 `mir2_core::sound`，这里只开设备。
    // ⚠️ `_stream` 就是声卡：**必须活在这个作用域里** —— drop 掉即静音。
    let sdl_audio = sdl.audio()?;
    // `AudioSubsystem` 是 sdl3 的私有类型 ⇒ 开流这一步只能在这里写（见 `audio::spec`）
    let (sound, _stream) = audio::Audio::open_with(true, true, |m| {
        sdl_audio.open_playback_stream(&audio::spec(), m)
    })?;

    // ---------- 资产 ----------
    // 资源必须来自清单驱动的 `images.m2pk`；源目录只作为旧 API 的无意义路径参数传递，
    // **绝不回退读取裸 WZL**。容器缺失、图库多余/缺失、任一静态必需图缺失 => 立即退出。
    let source_dir = mir2_core::paths::asset_dir().unwrap_or_else(|| std::path::PathBuf::from("."));
    let asset_dir = Some(source_dir);
    crate::assets::preflight().map_err(|e| {
        std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            format!("美术资源检查失败：{e}"),
        )
    })?;
    println!("[mir2-app] 图片资源：清单与 images.m2pk 一致");
    // 布局配置（D-134）：单文件入库配置只覆盖被编辑过的项。
    if let Some(l) = crate::layoutcfg::load(&crate::layoutcfg::layout_path()) {
        crate::layout::with_hud_layout(|cur| *cur = l);
    }
    let container_path = mir2_core::paths::map_container();
    let archive = match &container_path {
        Some(p) => match Archive::open(p) {
            Ok(a) => {
                println!("[mir2-app] 地图容器 = {}（{} 张）", p.display(), a.len());
                Some(a)
            }
            Err(e) => {
                println!("[mir2-app] 地图容器打不开：{e}");
                None
            }
        },
        None => {
            println!("[mir2-app] 未找到地图容器（先跑 tools/m2pk/build.sh）");
            None
        }
    };
    match &asset_dir {
        Some(d) => println!("[mir2-app] 资产目录 = {}", d.display()),
        None => println!("[mir2-app] 未找到资产目录：设 MIR2_ASSET_DIR=<mir2c/data>"),
    }
    // 音频资产：**一个容器**（`assets/audio/sounds.m2pk`，`tools/wavpack` 产出），
    // 取不到就退化到目录（原始素材 / 旧产物）。都没有 ⇒ 静音降级，界面照旧能用。
    let sounds: Option<mir2_core::sound::SoundAssets> = match mir2_core::sound::SoundAssets::open()
    {
        Some(a) => {
            println!("[mir2-app] 音频资产 = {}", a.describe());
            Some(a)
        }
        None => {
            println!("[mir2-app] 未找到音频资产：跑 tools/wavpack/build.sh（或设 MIR2_AUDIO_DIR）");
            None
        }
    };
    println!("[mir2-app] 音频驱动 = {}", sdl_audio.current_audio_driver());
    println!(
        "[mir2-app] 操作：F1 登录界面 / F2 地图视图 / F3 素材浏览器 / M 音乐 / N 音效 / ESC 退出"
    );

    let events: EventPump = sdl.event_pump()?;
    runtime::run_app(
        app_state::AppState::new(),
        events,
        canvas,
        tex_creator,
        sound,
        sounds,
        asset_dir,
        archive,
    )?;
    Ok(())
}

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

use std::collections::HashMap;
use std::time::{Duration, Instant};

use mir2_core::m2pk::Archive;
use mir2_core::map::{Map, TileDraw, LAYERS_ALL, UNIT_X, UNIT_Y};
use mir2_core::wzl::Wzl;

use sdl3::event::{Event, WindowEvent};
use sdl3::keyboard::{Keycode, Mod};
use sdl3::mouse::{Cursor, MouseButton, SystemCursor};

mod audio;
mod font;
mod login;
mod select;
mod ui;
// 注：`WindowContext` 在 sdl3 里是私有类型、不可具名，
// 故凡是需要纹理创建器的地方一律对类型参数 `T` 泛化。
use sdl3::render::Texture;
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
pub(crate) use layout::*;
pub(crate) mod gfx;
pub(crate) use gfx::*;
pub(crate) mod geom;
pub(crate) use geom::*;
pub(crate) mod tiles;
pub(crate) use tiles::*;
pub(crate) mod debug;
pub(crate) use debug::*;
pub(crate) mod actor;
pub(crate) use actor::*;
pub(crate) mod hud;
pub(crate) mod minimap;
pub(crate) mod status;
pub(crate) use minimap::*;
pub(crate) mod net;
pub(crate) use net::*;
pub(crate) mod input;
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

fn main() -> Result<(), Box<dyn std::error::Error>> {
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
    let asset_dir = mir2_core::paths::asset_dir();
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

    let mut events: EventPump = sdl.event_pump()?;

    // 登录界面的状态（照原版的那套版式与交互，见 `login.rs`）
    let mut login = login::Login::new();
    // 界面素材缓存（`Prguse` / `ChrSel`）—— 与地图图块、actor 精灵的缓存分开
    let mut ui = ui::UiCache::new();
    // 选角场景（登录成功、状态机停在"等你选"时才建）与真字体绘制器
    let mut select_scene: Option<select::Select> = None;
    // 界面文字（登录/选角/对话窗）**按原生字号光栅化，一像素都不缩放**
    // （用户 2026-10-09 第 6 条：「界面显示文字在 1024×768 下不要缩放大小，
    // 就维持正常输出，否则字会糊」）。
    //
    // ⚠️ 原来这里给的是 `UI_PX * 1.28 ≈ 17.9px` —— 想法是"素材拉伸 1.28，字不能跟着拉、
    // 所以要按放大后的字号重光栅化"。**那也是缩放**：17.9px 既非整数倍、又与素材的像素
    // 网格错位 ⇒ 看着就是糊的。现在字就是 14px 原生像素，落点仍按设计坐标 ×`UI_SCALE`
    // 换算（位置跟着版式走，字形不变）。
    let mut ui_texts = font::TextCache::new(mir2_core::text::UI_PX);
    // 世界里的字（怪物名字/伤害飘字）单独一份、小一档 —— 见 `text::NAME_PX`。
    let mut names = font::TextCache::new(mir2_core::text::NAME_PX);

    // 素材浏览器的状态（F3）
    let mut status = String::from("READY");
    let mut lib_idx: usize = 0;
    let mut img_idx: usize = 0;
    let mut loaded: Option<(usize, Wzl)> = None;
    let mut sprite_tex: Option<Texture<'_>> = None;

    // 地图模式的状态
    let mut mode: u8 = 1; // 1 = 登录界面（从头开始就是它），2 = 地图，3 = 素材浏览器
                          // 鼠标走路：目标格 + 是否跑（左键走、右键跑；松开清空）。`move_at` 是步频节流。
    let mut move_target: Option<(i32, i32, bool)> = None;
    let mut move_at = Instant::now();
    // 「单击」判据（用户 2026-10-09 第 3 条）：按下那一刻的时刻与光标那格，
    // 抬起时若没超过 `input::CLICK_MS` 就改判成"走一格"（`input::click_step`）。
    let mut press_at: Option<Instant> = None;
    let mut press_cell = (0, 0);
    // **按住的是哪个键**（松开清掉）+ 上次"重取目标"的时刻 —— 按住时每
    // [`MOUSE_REPEAT_MS`] 重跑一遍按下逻辑（照原版 `DXDrawMouseMove`）。
    let mut held_move: Option<MouseButton> = None;
    let mut retarget_at = Instant::now();
    // 移动被拒的"记账"（`World::move_fail` 的上一帧读数）与**锁到什么时候**
    //（照原版 `ActionFailLock`，见 [`MOVE_FAIL_LOCK_MS`]）。
    let mut last_move_fail = 0u64;
    // 撞墙后的**绕障**状态：`None` = 没在绕；`Some(0)` = 正在试逆时针 45°；
    // `Some(1)` = 试顺时针 45°（原版 `_DXDrawMouseDown` 的 `PrivDir`/`NextDir`，
    // `ClMain.pas:1886-1923`：直路被挡就先试一侧、再试另一侧）。
    let mut detour: Option<u8> = None;
    // 上一次看到自己的位置：位置一变就说明"这一步走成了" ⇒ 绕障状态清掉。
    let mut last_self_pos = (0i32, 0i32);
    let mut move_block_until = Instant::now();
    // 悬停的那个实体（由 `draw_map_view` 每帧算出来 —— 要精灵落点，见 `actor_rect`）。
    // ⚠️ 必须初始化：事件处理排在画图**之前**，点 NPC 要靠"上一帧算出的悬停"做
    // 像素级命中（与官方 `g_FocusCret` 同一套用法）。
    let mut hover: Option<u64> = None;
    // 进世界的按键提示只推一次（见下面那段）
    let mut hint_pushed = false;
    // 背包窗（F9）与它的页码 —— **纯客户端窗口状态**，不跟服务端同步
    //（原版背包也是本地开合；服务端只管背包内容）。
    let mut bag_open = false;
    let mut bag_page = 0usize;
    // 状态窗（F10）—— 同样是**纯客户端窗口状态**：数据全在 `world.ability` / `world.equip` 里
    //（服务端一直在下发，见 `status.rs` 的说明），开关不跟服务端同步。
    let mut status_open = false;
    // 状态窗的页码（官方 4 页，我们做 2 页：0 装备 / 1 属性）
    let mut status_page = 0usize;
    // 怪声音的随机源（`sfx::monster_ambient` 的 1/8 判定；不为这一处引 rand 依赖）
    let mut sfx_rng: u32 = 0x1234_5678;
    // 悬停可攻击目标时把光标换成"准星"（Crystal 是 `MouseCursor.Attack`，
    // `GameScene.cs:432-433`）；只在**状态变了**才设，别每帧调。
    let cursor_arrow = Cursor::from_system(SystemCursor::Arrow)?;
    let cursor_cross = Cursor::from_system(SystemCursor::Crosshair)?;
    let mut cursor_is_cross = false;
    // **锁定的攻击目标**（左键点怪锁住）：之后每帧自动"靠近 / 出手"，直到它死掉或消失
    //（照原版 `g_TargetCret` + `MouseTimerTimer`，`ClMain.pas:2863-2878 / 2962-2997`）。
    // `attack_at` 是出手节流 —— 原版是 `CanNextHit`。
    let mut combat_target: Option<u64> = None;
    let mut attack_at = Instant::now();
    let mut map_i: usize = 0;
    let mut map: Option<Map> = None;
    let mut map_err = String::new();
    // 相机：**浮点格**（进了世界由 [`follow_cam`] 每帧算；离线时是手动镜头）。
    let mut cam = (0f32, 0f32);
    let mut libs: HashMap<String, Option<Wzl>> = HashMap::new();
    let mut tiles: HashMap<TileKey, TileTex<'_>> = HashMap::new();
    let mut sprites = SpriteCache::new();
    let mut draws: Vec<TileDraw> = Vec::new();

    // 调试叠加层（D 开关）：画格网 + 每层落点框 + 鼠标十字线，并"点哪读哪"
    let mut debug = false;
    let mut layers: u8 = LAYERS_ALL; // 三层显隐掩码（CTRL+1/2/3 独立开关，L 循环单选）
    let mut mouse = (0.0f32, 0.0f32);

    let mut music_on = true;
    // 小地图（Tab）/ 大地图（M）—— 原版 1.76 的键位，默认都关（原版也要按才出来）
    let mut minimap_on = false;
    let mut bigmap_on = false;
    // 音效开关：原版是**两个独立开关**（音效 / 音乐，`MShare.pas:213-214`），
    // 所以这里也是两个（`N` 切音效）。
    let mut sfx_on = true;
    // 上一步的走路动画帧号 —— 脚步只在**帧号变到 1 / 4** 时响一次
    //（原版就是这么对齐的，`Actor.pas:2659-2660`）。
    let mut last_foot_frame: Option<u16> = None;
    // ⚠️ "现在"必须在**每帧开头**取（见循环里的重取）。原来只在循环外取一次，
    // 于是它是个常量：选角场景按 `now - last` 算 dt ⇒ dt 恒为 0 ⇒ **动画永不推进**
    //（实测踩过：选中角色后小人一动不动）。
    // 这里不写初值：唯一的作用域就是循环体内，初值只会是"读了但没人用"的警告。
    let mut started;

    // 联网状态（`C` 键连接/断开）。地址与会话号走环境变量，见 `Net::connect`。
    let mut net: Option<Net> = None;
    // 环境变量给了会话号就**开机自动连**（省得每次手按 `C`；开发时最常用）。
    if std::env::var("MIR2_SESSION").is_ok() {
        match Net::connect() {
            Ok(n) => {
                println!("[net] {}", n.status);
                net = Some(n);
            }
            Err(e) => println!("[net] 连不上：{e}"),
        }
    }

    // 给了会话号就说明"我已经有会话了" ⇒ 直接进地图（登录界面留给真登录用）
    if net.is_some() {
        mode = 2;
    }

    // 载入初始地图
    if let Some(a) = &archive {
        load_map(a, map_i, &mut map, &mut map_err, &mut cam);
    }

    'main: loop {
        // 每帧重新取"现在"：所有按时间推进的东西（选角动画、开门动画、移动补间）
        // 都拿它当基准。⚠️ 漏了这行 = 动画全部静止（踩过）。
        started = Instant::now();

        for ev in events.poll_iter() {
            // ⚠️ 事件里的 `x/y` 是**窗口坐标**（高 DPI 下还是物理像素），而界面画在
            // 800×600 的逻辑空间里 ⇒ 一律让 SDL 换算（它同时管逻辑呈现的缩放与留边）。
            // 漏了它的表现是"按钮点不中 / 点哪走哪偏一截"（踩过）。
            let ev = ev.get_converted_coords(&canvas).unwrap_or(ev);
            match ev {
                Event::Quit { .. } => break 'main,
                // 窗口尺寸/像素尺寸变了 ⇒ 重挑呈现模式（见 `apply_presentation`）。
                // ⚠️ 必须挂上：不然后面那次 resize 还把画布按老尺寸缩放（画面拉变形）。
                Event::Window { win_event, .. } => {
                    let changed = matches!(
                        win_event,
                        WindowEvent::Resized(..) | WindowEvent::PixelSizeChanged(..)
                    );
                    if changed {
                        if let Err(e) = apply_presentation(&mut canvas) {
                            eprintln!("[mir2-app] {e}");
                        }
                    }
                }
                Event::KeyDown {
                    keycode, keymod, ..
                } => match keycode {
                    // ⚠️ **登录/选角里 ESC 先交给场景**（2026-10-09 修）。
                    //
                    // 这条 arm 排在所有 mode 分支之前，原来无条件 `break 'main` ⇒
                    // 那两个界面里按 ESC **直接退客户端**，于是
                    // `select.rs` 的"ESC 关建角框 / 关弹窗 / 取消待确认"与 `login.rs`
                    // 的"ESC 退建号面板 / 关报错弹窗"全是**不可达死代码**。
                    // 现在放它们落到下面的 mode 分支去；场景要退（`Action::Exit` /
                    // `Action::Quit`）才真退。其余模式（世界/素材浏览器）保持原样。
                    // 背包窗开着 ⇒ ESC **先关背包**
                    Some(Keycode::Escape) if mode == 2 && bag_open => bag_open = false,
                    // 状态窗同理 ⇒ ESC **先关状态窗**（别顺手把客户端退了）
                    Some(Keycode::Escape) if mode == 2 && status_open => status_open = false,
                    // NPC 对话开着 ⇒ ESC **先关对话**（原版 `@exit`），别顺手退了客户端
                    Some(Keycode::Escape)
                        if mode == 2
                            && net.as_ref().is_some_and(|n| n.world.dialog.is_some()) =>
                    {
                        if let Some(n) = net.as_ref() {
                            if let Some(d) = n.world.dialog.as_ref() {
                                n.npc_close(d.npc_id);
                            }
                        }
                        if let Some(n) = net.as_mut() {
                            n.world.close_dialog();
                        }
                    }
                    Some(Keycode::Escape) if mode != 1 && mode != 4 => break 'main,
                    // ⚠️ **开发键让开原版键位**（口径见 `docs/use.md`）：F1~F8 是技能、
                    // F9~F12 是包裹/属性/技能/内挂、M 是大地图、Tab 是小地图、数字是快捷物品
                    // ⇒ 这些"开发查看器"入口统统收进 **Ctrl+**，原版键位留给真功能。
                    // F9 = 背包窗（原版键位，见上面那条注释的口径）
                    Some(Keycode::F9) if mode == 2 => {
                        bag_open = !bag_open;
                        bag_page = 0;
                        println!("[ui] 背包窗 {}", if bag_open { "打开" } else { "关闭" });
                    }
                    // F10 = 状态窗（原版键位：F9 包裹 / F10 属性 / F11 技能 / F12 内挂）
                    Some(Keycode::F10) if mode == 2 => {
                        status_open = !status_open;
                        println!("[ui] 状态窗 {}", if status_open { "打开" } else { "关闭" });
                    }
                    Some(Keycode::F1) if ctrl(keymod) => mode = 1,
                    Some(Keycode::F2) if ctrl(keymod) => mode = 2,
                    Some(Keycode::F3) if ctrl(keymod) => mode = 3,
                    Some(Keycode::M) if ctrl(keymod) => {
                        music_on = !music_on;
                        sound.set_music_on(music_on);
                        // 带上"正在响几路 / 有没有 BGM"：一眼看出混音器是不是活的
                        let (voices, bgm, ..) = sound.stats();
                        status = format!(
                            "MUSIC {}  [sfx {} voices, bgm {}]",
                            if music_on { "ON" } else { "OFF" },
                            voices,
                            if bgm { "ON" } else { "OFF" }
                        );
                    }
                    Some(Keycode::N) if ctrl(keymod) => {
                        sfx_on = !sfx_on;
                        sound.set_sfx_on(sfx_on);
                        let (voices, bgm, ..) = sound.stats();
                        status = format!(
                            "SOUND {}  [sfx {} voices, bgm {}]",
                            if sfx_on { "ON" } else { "OFF" },
                            voices,
                            if bgm { "ON" } else { "OFF" }
                        );
                    }
                    // 选角：键盘是**我们的扩展**（原版选角场景只认鼠标）
                    _ if mode == 4 => {
                        if let Some(k) = keycode {
                            let act = match select_scene.as_mut() {
                                Some(s) => s.on_key(k),
                                None => select::Action::None,
                            };
                            if do_select_action(act, &mut net, &mut select_scene, &sound, &sounds)?
                            {
                                break 'main;
                            }
                        }
                    }
                    // 登录界面：全部交互在 `login` 里（Tab/退格/回车/ESC），这里只把
                    // 它给出的动作翻译成"接下来干什么"。
                    _ if mode == 1 => {
                        if let Some(k) = keycode {
                            let act = login.on_key(k);
                            // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                            if act != login::Action::None {
                                sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                            }
                            match act {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::SubmitSignup => {
                                    submit_signup(&mut login, &mut net, &mut status)
                                }
                                login::Action::NewAccount => {
                                    // 面板切换在 `login` 里已经做完了（`enter_signup`），
                                    // 这里只给状态栏一句人话（原版是开 `DNewAccount`）。
                                    status = "NEW ACCOUNT".into();
                                }
                                login::Action::CancelSignup => status = String::new(),
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::Quit => break 'main,
                                login::Action::None | login::Action::Dismiss => {}
                            }
                        }
                    }
                    // 素材浏览器（开发用）
                    _ if mode == 3 => match keycode {
                        Some(Keycode::LeftBracket) => {
                            lib_idx = (lib_idx + LIBS.len() - 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::RightBracket) => {
                            lib_idx = (lib_idx + 1) % LIBS.len();
                            img_idx = 0;
                        }
                        Some(Keycode::Comma) | Some(Keycode::Left) => {
                            img_idx = img_idx.saturating_sub(1);
                        }
                        Some(Keycode::Period) | Some(Keycode::Right) => img_idx += 1,
                        _ => {}
                    },
                    _ if mode == 2 => match keycode {
                        // 方向键：**联网且在世界里 ⇒ 走一步**（相机跟着自己）；否则平移镜头。
                        Some(Keycode::Left) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirLeft) {
                                cam.0 -= 2.0
                            }
                        }
                        Some(Keycode::Right) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirRight) {
                                cam.0 += 2.0
                            }
                        }
                        Some(Keycode::Up) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirUp) {
                                cam.1 -= 2.0
                            }
                        }
                        Some(Keycode::Down) => {
                            if !walk_if_online(&net, mir2_protocol::Direction::DirDown) {
                                cam.1 += 2.0
                            }
                        }
                        // Tab = **小地图**开关、M = **大地图**开关（原版 1.76 的键位，
                        // 见 `docs/use.md`；音乐已经挪到 Ctrl+M，不再抢 M）。
                        Some(Keycode::Tab) => minimap_on = !minimap_on,
                        Some(Keycode::M) => bigmap_on = !bigmap_on,
                        // 空格：打一下身边的目标（A′：走 + 砍 = 能玩）。**左键点怪**才是
                        // 主路（会锁住目标、自动靠近）—— 见下面 `MouseButtonDown` 那段。
                        Some(Keycode::Space) => {
                            if let Some(n) = net.as_ref().filter(|n| n.world.in_world()) {
                                if n.attack_adjacent() {
                                    swing_sfx(n, &sound, &sounds);
                                } else {
                                    println!("[net] 身边没有可打的目标（八格内）");
                                }
                            }
                        }
                        // C：连接/断开新协议服务端（地址与会话号走环境变量，见 `Net::connect`）。
                        Some(Keycode::C) => {
                            if net.is_some() {
                                println!("[net] 主动断开");
                                net = None;
                            } else {
                                match Net::connect() {
                                    Ok(n) => {
                                        println!("[net] {}", n.status);
                                        net = Some(n);
                                    }
                                    Err(e) => println!("[net] 连不上：{e}"),
                                }
                            }
                        }
                        Some(Keycode::Home) => cam = (0.0, 0.0),
                        // ---- 调试叠加层（只在地图模式，避免污染登录输入框）----
                        // 辅助线/坐标叠加层：默认关闭（见 `DEBUG_OVERLAY`）
                        Some(Keycode::D) if DEBUG_OVERLAY => {
                            debug = !debug;
                            println!(
                                "[debug] 叠加层 {}（L 或 CTRL+1/2/3 控制图层显隐 / P 打印绘制清单 / 左键点哪读哪）",
                                if debug { "ON" } else { "OFF" }
                            );
                        }
                        // 逐层独立显隐：排查错位时最常用的是"关掉一层看底下那层"
                        Some(Keycode::_1) | Some(Keycode::_2) | Some(Keycode::_3)
                            if DEBUG_LAYERS
                                && (keymod.intersects(Mod::LCTRLMOD)
                                    || keymod.intersects(Mod::RCTRLMOD)) =>
                        {
                            let (bit, name) = match keycode {
                                Some(Keycode::_1) => (1u8, "地表 Tiles"),
                                Some(Keycode::_2) => (2u8, "中间 SmTiles"),
                                _ => (4u8, "前景 Objects"),
                            };
                            layers ^= bit;
                            println!(
                                "[layer] {} {}   →   当前可见 {}（G=地表 M=中间 F=前景）",
                                name,
                                if layers & bit != 0 {
                                    "显示"
                                } else {
                                    "隐藏"
                                },
                                layers_desc(layers)
                            );
                        }
                        // `L` 是同一个功能的"循环"绑定 —— 只关 CTRL 那三个键等于
                        // 留了后门，所以一起跟着 `DEBUG_LAYERS` 走。
                        Some(Keycode::L) if DEBUG_LAYERS => {
                            // 循环：全部 → 仅地表 → 仅中间 → 仅前景 → 全部
                            layers = match layers {
                                LAYERS_ALL => 1,
                                1 => 2,
                                2 => 4,
                                _ => LAYERS_ALL,
                            };
                            println!("[layer] 过滤循环   →   当前可见 {}", layers_desc(layers));
                        }
                        Some(Keycode::P) => {
                            dump_draws(&draws, cam_parts(cam).cell, &tiles, layers);
                        }
                        Some(Keycode::LeftBracket) | Some(Keycode::RightBracket) => {
                            if let Some(a) = &archive {
                                let step: i64 = if keycode == Some(Keycode::RightBracket) {
                                    1
                                } else {
                                    -1
                                };
                                let n = a.len() as i64;
                                map_i = (((map_i as i64 + step) % n + n) % n) as usize;
                                load_map(a, map_i, &mut map, &mut map_err, &mut cam);
                            }
                        }
                        _ => {}
                    },
                    _ => {}
                },
                // 鼠标位置（已在循环头换算成界面的 800×600 空间）
                Event::MouseMotion { x, y, .. } => mouse = (x, y),
                // 背包窗内滚轮翻页（服务端背包 46 格 = 2 页，窗口一次只画 24 格）
                Event::MouseWheel { y, .. } if bag_open && mode == 2 => {
                    let (bx, by) = crate::layout::bag_rect();
                    let inside = mouse.0 >= bx
                        && mouse.0 < bx + crate::layout::BAG_W
                        && mouse.1 >= by
                        && mouse.1 < by + crate::layout::BAG_H;
                    if inside {
                        let pages = net
                            .as_ref()
                            .map_or(1, |n| {
                                n.world
                                    .bag
                                    .len()
                                    .div_ceil(crate::layout::BAG_PAGE_SLOTS)
                            })
                            .max(1);
                        if y > 0.0 {
                            bag_page = (bag_page + 1) % pages;
                        } else if y < 0.0 {
                            bag_page = (bag_page + pages - 1) % pages;
                        }
                    }
                }
                // 鼠标移动：**在世界里 ⇒ 左键走 / 右键跑**（照原版 `ClMain.pas:2246-2352`：
                // 左键 = 走，右键 = 跑；方向由鼠标相对角色的方位定，见 `dir_to`）。
                // 离线看地图（没进世界）时左键仍是那个诊断探针 —— 它是开发用的，
                // 别和"走路"抢同一个键。
                Event::MouseButtonDown {
                    mouse_btn, x, y, ..
                } if mode == 2 => {
                    let in_world = net.as_ref().is_some_and(|n| n.world.in_world());
                    match mouse_btn {
                        MouseButton::Left | MouseButton::Right if in_world => {
                            let cell = screen_to_cell(cam, x, y);
                            let run = mouse_btn == MouseButton::Right;
                            // 按住时每 300ms 要拿"当前鼠标位置"重取目标（见下面 `MOUSE_REPEAT_MS`
                            // 那段）⇒ 按下这一刻就得把位置记上（光等 `MouseMotion` 会漏掉
                            // "按下后一动不动"的那种按住）。
                            mouse = (x, y);
                            // 左键才判"单击"（用户第 3 条说的是左键）；右键仍是按住=跑
                            if mouse_btn == MouseButton::Left {
                                press_at = Some(Instant::now());
                                press_cell = cell;
                            } else {
                                press_at = None;
                            }
                            // 照原版 `_DXDrawMouseDown`（`ClMain.pas:2805-2878`）：
                            // **先清掉旧目标**，点到**活怪**就锁住它（之后每帧自动靠近/出手，
                            // 直到它死掉或消失）；点空地 ⇒ 走/跑到那一格。
                            let (ct, mt) = net
                                .as_ref()
                                .map(|n| mouse_intent(&n.world, cell, run))
                                .unwrap_or((None, None));
                            combat_target = ct;
                            move_target = mt;
                            attack_at = Instant::now(); // 立刻判"该出手还是该靠近"
                                                        // ⚠️ `move_at` **不清零**：原版的客户端是"停等"的
                                                        //（发一条动作后 `ActionLock := TRUE`，等服务端
                                                        // `sSTATUS_GOOD/FAIL` 才解锁并计下一拍，`ClMain.pas:3575-3584/4214-4222`）。
                                                        // 我们每次按下都清零 ⇒ 连点会把间隔压到 0 ⇒ 服务端按到达时间限流
                                                        // 直接回 `reason=1`，客户端再锁 1 秒 ⇒ "点了没反应"。
                                                        // ⚠️ **按住不放要能一直走**：原版靠 `DXDrawMouseMove`
                                                        //（`ClMain.pas:2678-2679`）—— 按住时只要距上次 >300ms 就**重跑一遍
                                                        // 按下逻辑**，于是"鼠标那一格"被反复重新当成目标。少了它，
                                                        // 目标格是按下那一刻定死的 ⇒ 走到那儿就停（用户 2026-10-08 报的
                                                        // "按住只能走数次"）。
                            held_move = Some(mouse_btn);
                            retarget_at = Instant::now();
                            match ct {
                                Some(id) => {
                                    println!("[net] 锁定目标 ActorId={id}（靠近后自动出手）");
                                }
                                None => println!(
                                    "[move] 目标格 ({},{})：{}",
                                    cell.0,
                                    cell.1,
                                    if run { "跑" } else { "走" }
                                ),
                            }
                            // ② 对话开着：点选项 = 选它；点面板别处 = 什么也别做（**别走路**）
                            //
                            // ⚠️ 这两条排在"走路/锁怪"那段**之后**，靠"清掉已写好的意图"
                            // 实现（把上面那一大段包进 if 会多一层缩进、更容易漏改）。
                            if let Some(n) = net.as_ref() {
                                if let Some((panel, lines)) =
                                    hud::dialog_geom(net.as_ref())
                                {
                                    // **行内可点文字**：命中矩形由 `input::dialog_line_pieces`
                                    // 算（与 `draw_dialog` 是同一份 ⇒ 画哪点哪），
                                    // 片段自带**选项序号**（服务端把 `<打开/@trading>` 改写成了
                                    // `<打开/@1>`），直接拿它回包。
                                    let hit = {
                                        let mut measure = |t: &str| ui_texts.width(t);
                                        input::dialog_link_at(panel, &lines, (x, y), &mut measure)
                                    };
                                    if let Some(idx) = hit {
                                        if let Some(d) = n.world.dialog.as_ref() {
                                            n.npc_select(d.npc_id, idx);
                                            println!("[net] 对话选项 {idx}");
                                        }
                                    } else if input::dialog_close_hit(panel, (x, y)) {
                                        // 右上角那个红 X（用户 2026-10-09 第 1 条：
                                        // 「对话窗口右上角有个X按钮，现在你没有接上"关闭/退出"」）
                                        // —— 与 ESC 走同一条路：告诉服务端 + 本地清。
                                        if let Some(d) = n.world.dialog.as_ref() {
                                            n.npc_close(d.npc_id);
                                        }
                                        if let Some(n) = net.as_mut() {
                                            n.world.close_dialog();
                                        }
                                        println!("[net] 对话关闭（点 X）");
                                    }
                                    // ⚠️ **只有点在面板里**才吞掉这次点击。
                                    // 用户 2026-10-09：「对话期间应仍能操作（走、跑、打架），
                                    // 现在不能跑不能走，人被定在那里」—— 原来是无条件清空意图，
                                    // 等于把整个屏幕都变成"对话区"。原版的对话窗是**非模态**的
                                    //（`NPCDialog` 就是个窗口，`DrawScrn.pas` 里它跟世界各画各的），
                                    // 外面照常点：此时才拦。
                                    if input::dialog_hit(panel, (x, y)) {
                                        combat_target = None;
                                        move_target = None;
                                        press_at = None;
                                        held_move = None;
                                    }
                                }
                            }
                            // ②″ F10 状态窗：点 X = 关窗、点箭头 = 翻页、点槽 = 报一下那件；
                            // 点在窗里别处 = 吞掉（与背包窗同一套）
                            if status_open {
                                if let Some(dir) = asset_dir.as_deref() {
                                    if let Some(bg) =
                                        ui.size(dir, crate::status::BG_LIB, crate::status::BG)
                                    {
                                        let (wx, wy, ww, wh) = crate::status::panel(bg);
                                        if x >= wx && x < wx + ww && y >= wy && y < wy + wh {
                                            match crate::status::hit((x - wx, y - wy)) {
                                                crate::status::Hit::Close => {
                                                    status_open = false;
                                                    println!("[ui] 状态窗关闭（点 X）");
                                                }
                                                crate::status::Hit::Arrow(d) => {
                                                    status_page =
                                                        crate::status::page_step(status_page, d);
                                                    println!("[ui] 状态窗翻到第 {} 页", status_page);
                                                }
                                                crate::status::Hit::Slot(slot) => {
                                                    if let Some(n) = net.as_ref() {
                                                        let name = crate::status::slot_name(slot);
                                                        let msg = match n.world.equip.get(slot) {
                                                            Some(Some(it)) => format!(
                                                                "装备槽 {}（{}）：{}",
                                                                slot + 1,
                                                                name,
                                                                it.name
                                                            ),
                                                            _ => format!(
                                                                "装备槽 {}（{}）是空的",
                                                                slot + 1,
                                                                name
                                                            ),
                                                        };
                                                        println!("[ui] {msg}");
                                                        let shown = trunc(&msg, 30);
                                                        if let Some(n) = net.as_mut() {
                                                            n.chat.push(shown, C_CHAT_SYS);
                                                        }
                                                    }
                                                }
                                                crate::status::Hit::None => {}
                                            }
                                            combat_target = None;
                                            move_target = None;
                                            press_at = None;
                                            held_move = None;
                                        }
                                    }
                                }
                            }
                            // ②′ 背包窗开着：点格子 = 把"看到的是哪件"反馈到聊天区
                            //（`UseItem` 还没接新协议，见 `docs/todo.md`）；点关闭 X = 关窗；
                            // 点在窗里别处 = 吞掉这次点击，**别走路**。
                            if bag_open {
                                let (bx, by) = crate::layout::bag_rect();
                                let inside = x >= bx
                                    && x < bx + crate::layout::BAG_W
                                    && y >= by
                                    && y < by + crate::layout::BAG_H;
                                if inside {
                                    let (lx, ly) = (x - bx, y - by);
                                    let on_close = lx >= crate::layout::BAG_CLOSE_X
                                        && lx < crate::layout::BAG_CLOSE_X + crate::layout::BAG_CLOSE_W
                                        && ly >= crate::layout::BAG_CLOSE_Y
                                        && ly < crate::layout::BAG_CLOSE_Y + crate::layout::BAG_CLOSE_H;
                                    if on_close {
                                        bag_open = false;
                                        println!("[ui] 背包窗关闭（点 X）");
                                    } else if let Some(slot) = crate::layout::bag_slot_at((lx, ly)) {
                                        let idx = bag_page * crate::layout::BAG_PAGE_SLOTS + slot;
                                        if let Some(n) = net.as_ref() {
                                            let msg = match n.world.bag.get(idx) {
                                                Some(Some(it)) => format!(
                                                    "背包第 {} 格：{} x{}（外观图号 {}）",
                                                    idx + 1,
                                                    it.name,
                                                    it.count,
                                                    it.looks
                                                ),
                                                _ => format!("背包第 {} 格是空的", idx + 1),
                                            };
                                            println!("[ui] {msg}");
                                            let shown = trunc(&msg, 30);
                                            if let Some(n) = net.as_mut() {
                                                n.chat.push(shown, C_CHAT_SYS);
                                            }
                                        }
                                    }
                                    combat_target = None;
                                    move_target = None;
                                    press_at = None;
                                    held_move = None;
                                }
                            }
                            // ③ 点 NPC ⇒ **说话**，不是往它那格走。
                            //
                            // ⚠️ 判据分两层（用户 2026-10-09 报"点 NPC 没反应"）：
                            //   1. `hover`（上一帧用 `actor_rect` = **画出来的框**算的）——
                            //      NPC 的精灵比格子高，点它的头/肩时格子是**上面那一格**，
                            //      只看格子就是"点了没反应、人还往那边走"；
                            //   2. 兜底再用格子（光标正好压在它脚下那格时，`hover` 可能为空）。
                            if let Some(n) = net.as_ref() {
                                let hit = hover
                                    .filter(|id| n.world.npc_kind(*id))
                                    .or_else(|| n.world.npc_at(cell.0, cell.1));
                                if let Some(id) = hit {
                                    n.npc_click(id);
                                    combat_target = None;
                                    move_target = None;
                                    press_at = None; // 松开时别再改判成"走一格"
                                    held_move = None;
                                    println!("[net] 点 NPC ActorId={id}（等 NpcSay）");
                                }
                            }
                        }
                        MouseButton::Left => probe_at(x, y, cam, &draws, &tiles, layers),
                        _ => {}
                    }
                }
                // 松开：**停止"按住"那条重取目标的链**，但**不清走法目标**。
                //
                // ⚠️ 2026-10-09 改（用户第 7 条）：原来这里 `move_target = None` ⇒
                // **单击只走一步**（目标只在按住时才被一次次重取）。用户要的是
                // 「单击地图位置 ⇒ 跑动+走动到该位置；那格有障碍 ⇒ 就近停下」——
                // 于是目标留着，由下面的走法逻辑一直推到**到达**或**被拒**为止：
                //
                //   · 到达  ⇒ `next_move_step` 给不出下一步，自己就清了（见它的说明）；
                //   · 被挡  ⇒ 服务端回 `MoveRejected` ⇒ 那条路清目标 + 锁 1 秒
                //           （见下面 `world.move_fail` 那段，口径照原版 `ActionFailed`）。
                //
                // ⚠️ **不清** `combat_target`：原版锁住的目标是"打到它死/消失"为止
                //（`MouseTimerTimer`，`ClMain.pas:2962-2997`），不是"松手就取消"。
                Event::MouseButtonUp { mouse_btn, .. }
                    if mode == 2 && matches!(mouse_btn, MouseButton::Left | MouseButton::Right) =>
                {
                    held_move = None;
                    // ⚠️ 单击 = 走**一格**（用户 2026-10-09 第 3 条）：按下那一刻已经把
                    // "走到光标那格"当成目标了（`mouse_intent`），够快松手就把它换成
                    // **紧邻那一格** ⇒ 一步之后"到达"、目标自清。按住（> `CLICK_MS`）
                    // 仍是一路走过去。
                    if mouse_btn == MouseButton::Left {
                        if let Some(t) = press_at.take() {
                            if t.elapsed() < Duration::from_millis(input::CLICK_MS) {
                                if let Some(n) = net.as_mut() {
                                    if n.world.in_world() {
                                        let (ct, mt) = input::click_step(
                                            &n.world,
                                            n.world.self_pos,
                                            press_cell,
                                            false,
                                        );
                                        if ct.is_some() || mt.is_some() {
                                            combat_target = ct;
                                            move_target = mt;
                                            attack_at = Instant::now();
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    // 这两屏的命中测试活在**设计空间**（800×600）里 ⇒ 鼠标先除回去
                    let (x, y) = ui::ui_inv_pt((x, y));
                    if let (Some(dir), Some(scene)) = (asset_dir.as_ref(), select_scene.as_mut()) {
                        // 弹窗的几何要给进去：它那颗 [确定] 也走"按下与抬起同一颗"
                        let msg = select::msgbox_geom(&mut ui, dir, UI_WIN);
                        if let Some(l) =
                            mir2_core::select_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i))
                        {
                            scene.on_down((x, y), &l, msg);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 4 => {
                    let (x, y) = ui::ui_inv_pt((x, y)); // 设计空间（见上面那条）
                    let act = match (asset_dir.as_ref(), select_scene.as_mut()) {
                        (Some(dir), Some(scene)) => {
                            // 弹窗的几何也要给进去：不然它那颗 [确定] 点不中（见 `Select::on_up`）
                            let msg = select::msgbox_geom(&mut ui, dir, UI_WIN);
                            match mir2_core::select_ui::Layout::build(UI_WIN, |c, i| {
                                ui.size(dir, c, i)
                            }) {
                                Some(l) => scene.on_up((x, y), &l, msg),
                                None => select::Action::None,
                            }
                        }
                        _ => select::Action::None,
                    };
                    if do_select_action(act, &mut net, &mut select_scene, &sound, &sounds)? {
                        break 'main;
                    }
                }
                Event::MouseButtonDown {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 1 => {
                    // 版式每帧现算（尺寸来自容器头，不解压 ⇒ 很便宜），用于命中判定。
                    // ⚠️ 版式在**设计空间**（800×600）里 ⇒ 鼠标先除回去（见 `UI_WIN`）
                    let (x, y) = ui::ui_inv_pt((x, y));
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            login.on_down((x, y), &l);
                        }
                    }
                }
                Event::MouseButtonUp {
                    mouse_btn: MouseButton::Left,
                    x,
                    y,
                    ..
                } if mode == 1 => {
                    let (x, y) = ui::ui_inv_pt((x, y)); // 设计空间（见上面那条）
                    if let Some(dir) = asset_dir.as_ref() {
                        let l =
                            mir2_core::login_ui::Layout::build(UI_WIN, |c, i| ui.size(dir, c, i));
                        if let Some(l) = l {
                            let act = login.on_up((x, y), &l);
                            // 按钮声（原版 `FState.pas:2376-2382` 的 `csNorm` ⇒ 103）
                            if act != login::Action::None {
                                sfx(&sound, &sounds, mir2_core::sound::idx::NORM_BUTTON_CLICK);
                            }
                            match act {
                                login::Action::Submit => {
                                    submit_login(&mut login, &mut net, &mut status)
                                }
                                login::Action::SubmitSignup => {
                                    submit_signup(&mut login, &mut net, &mut status)
                                }
                                login::Action::Quit => break 'main,
                                login::Action::Dismiss => {}
                                login::Action::NewAccount => {
                                    status = "NEW ACCOUNT".into();
                                }
                                login::Action::CancelSignup => status = String::new(),
                                login::Action::ChangePassword => {
                                    status = "CHANGE PASSWORD: NOT WIRED YET".into();
                                }
                                login::Action::None => {}
                            }
                        }
                    }
                }
                Event::TextInput { text: t, .. } if mode == 1 => login.on_text(&t),
                // 选角界面的**建角对话框**也吃文字（`Select::on_text` 自己判断对话框开没开、
                // 焦点在不在姓名上）—— 少了这条，姓名框打字没反应（原版是 `TEdit` 收键）。
                Event::TextInput { text: t, .. } if mode == 4 => {
                    if let Some(s) = select_scene.as_mut() {
                        s.on_text(&t);
                    }
                }
                _ => {}
            }
        }

        // 联网：把网络线程收到的东西推进状态机（**每帧一次**，永不阻塞）。
        if let Some(n) = &mut net {
            n.pump();
            // 登录界面与网络状态互相照应。三件事：
            //   ① 失败 ⇒ 弹窗（原版也是 `DMessageDlg`），并把"登录中"解掉；
            //   ② 进世界 ⇒ 开始播开门动画（原版 `IntroScn.pas:907-914`），播完切地图；
            //   ③ 成功拿到的会话号存下来 —— 之后重连走 `Reconnect`，不必再输口令。
            // ⚠️ **取走**失败原因，而不是每帧读 `entrance.failed()` / `n.fail`：
            // 两者都是粘性状态（`stage == Failed` 会一直挂着）⇒ 每帧读的话，用户点了[确定]
            // 之后下一帧它又弹回来，看着就是"弹窗关不掉"（用户 2026-10-08 报的）。
            // 弹窗正开着就不取（那条失败会等关掉之后再弹）—— 免得把两条原因挤掉一条。
            if login.error.is_none() {
                if let Some(why) = n.take_fail() {
                    // 失败：弹提示 + 焦点回账号框（原版 `PassWdFail`，`IntroScn.pas:613-619`）
                    login.fail(why);
                }
            }
            // 登录**通过**（清单在手）⇒ 原版 `OpenLoginDoor`：藏小窗 + 开门 + 开门声。
            //
            // ⚠️ 触发点是"登录通过"，**不是"进世界"**：原版由登录成功那条路调
            // `OpenLoginDoor`（`IntroScn.pas:795-801`），门放完才 `ChangeScene(stSelectChr)`
            //（`IntroScn.pas:838-847`）。早先挂在 `in_world()` 上 ⇒ 门排在选角与进图之后，
            // 而且门画在登录框背后 ⇒ 看起来"压根没有开门动画"（用户报的就是这个）。
            // `login.busy` 的含义正是"这次是口令登录"（`submit_login` 设的）。
            if login.busy && n.entrance.stage() == &mir2_core::entrance::Stage::AwaitList {
                login.opened_at = Some(Instant::now());
                login.busy = false;
                select_scene = None;
                mode = 1; // 开门动画在登录屏上播（见 `login::draw` 的说明）
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 兜底：**认领会话**那条路（没有口令登录这一步）落到世界里时才补一次开门。
            if n.entrance.in_world() && login.opened_at.is_none() {
                login.opened_at = Some(Instant::now());
                login.busy = false;
                select_scene = None;
                mode = 1;
                sfx(&sound, &sounds, mir2_core::sound::idx::ROCK_DOOR_OPEN);
            }
            // 登录成功后会停在 `AwaitPick`（`set_manual_pick`）⇒ 切到选角场景。
            //
            // ⚠️ "停在等你选"是**状态机说的**，不是我们猜的时机：角色列表就在它手上
            //（`entrance.characters()`），界面只负责显示与选择。
            // 每帧"网络 → 画面"的决策：**纯函数**（见 `plan` 的说明 —— 决策必须放在
            // 能单测的地方，主循环只负责执行）。判据全是状态机说的事实，没有一处是"猜时机"。
            let chars: Vec<select::CharEntry> = n
                .entrance
                .characters()
                .iter()
                .map(select::CharEntry::from_summary)
                .collect();
            let p = plan(
                mode,
                n.entrance.stage() == &mir2_core::entrance::Stage::AwaitPick,
                n.entrance.in_world(),
                list_changed(select_scene.as_ref(), &chars),
                // "门挡着" = 开过门（`opened_at` 有值）但还没放完
                login.opened_at.is_some() && !login.door_done(),
            );
            // ⚠️ **只在列表真的变了**的时候才重建场景。这段在"门还在放"的那三秒里每帧都会
            // 走到 —— 早先每帧重建 + 每帧 println，表现是**日志刷屏**（用户报过），
            // 而且把用户已经移好的选中位置**每帧抹回第 0 个**。
            // 列表真的会变的场合只有两种：建/删角回执后的重拉、重连回落。
            if p.rebuild_select {
                println!("[net] 角色列表：{} 个", chars.len());
                let mut scene = select::Select::new(chars);
                // `MIR2_CHAR=<id>` 指定初选（手动模式下状态机不看它了，落到界面上）
                if let Ok(v) = std::env::var("MIR2_CHAR") {
                    match v.parse::<u64>() {
                        Ok(id) => scene.pick_id(id),
                        Err(_) => println!("[net] MIR2_CHAR={v} 不是整数，忽略"),
                    }
                }
                select_scene = Some(scene);
            }
            // 门还在放就先不切：原版顺序是"门放完 → `ChangeScene(stSelectChr)`"
            //（`IntroScn.pas:838-847`），一上来就切会把门当场掐掉（用户看到的就是"没有开门动画"）。
            // 门放完那一刻由主循环里 `mode == 1` 那条分支接手。
            if p.enter_select {
                mode = 4;
            }
            // 选角通过 ⇒ 换到游戏主场景（原版在这里是 `ChangeScene(stPlay)`）
            if p.enter_play {
                mode = 2;
                // 选角场景用不上了（它的槽动画也不必再跑）
                select_scene = None;
            }
            // 选角被拒（例如租约被占）⇒ 弹给用户换一个（状态机会退回 `AwaitPick`）。
            if let Some(why) = n.entrance.take_pick_error() {
                if let Some(scene) = select_scene.as_mut() {
                    scene.say(why);
                }
            }
            // 在选角场景里出了**致命**错（服务端断开…）⇒ 回登录界面，
            // 用那套已有的弹窗说清楚（否则用户会在选角界面上干等）。
            if mode == 4 && n.entrance.failed().is_some() {
                mode = 1;
            }
            if let Some(tok) = n.entrance.session_token() {
                n.session = tok;
            }
        }
        // 鼠标走路：按住时**一步步**朝目标格走。
        //
        // 原版是"按住每 ≥300ms 重新触发一次 `_DXDrawMouseDown`、松开清目标"
        //（`ClMain.pas:2115-2116 / 2384-2389`），步频本身受服务端节流限制 ⇒
        // 我们按 `WALK_MS`/`RUN_MS` 发，到了目标格就停。
        //
        // ⚠️ "这一步走还是跑"由 [`next_move_step`] 定：**距离 < 2 就不许跑** ——
        // 跑步一次跨 2 格，奇数距离时不许跑才不会"跨过去再跨回来"（用户报的"左右乱换"）。

        // **按住鼠标不放 ⇒ 一直走/跑**：每 [`MOUSE_REPEAT_MS`] 拿**当前鼠标那一格**重新定目标。
        //
        // 照原版 `ClMain.pas:2678-2679`（`DXDrawMouseMove` 里"按住且距上次 >300ms ⇒
        // 重跑一遍 `_DXDrawMouseDown`"）。少了这一步，目标格就是按下那一刻定死的 ⇒
        // 走到那儿就停（用户 2026-10-08 报的"按住只能走数次"）。
        //
        // ⚠️ 它必须与"人物不动、地图卷动"（[`follow_cam`]）配套：镜头跟着人走，鼠标**屏幕**
        // 位置不变时它对应的**格子**会往前跑 ⇒ 按住不放就是**一直走下去**（原版的"按住跑直线"）。
        // 镜头不卷或者目标不重取，两样单独都做不出这个手感。
        if mode == 2
            && held_move.is_some()
            && retarget_at.elapsed() >= Duration::from_millis(MOUSE_REPEAT_MS)
        {
            retarget_at = Instant::now();
            if let Some(n) = net.as_ref().filter(|n| n.world.in_world()) {
                let cell = screen_to_cell(cam, mouse.0, mouse.1);
                let run = held_move == Some(MouseButton::Right);
                // ⚠️ 用 `mouse_repeat` 而不是 `mouse_intent`：按住时**不能**因为
                // "光标不在怪身上了"就把锁住的怪丢掉（镜头跟着人走，光标一定会滑开，
                // 否则按住点怪会半路变成走路）。见 `mouse_repeat` 的说明。
                let (ct, mt) = mouse_repeat(&n.world, cell, run, combat_target);
                combat_target = ct;
                move_target = mt;
            }
        }

        // **锁定的攻击目标**：够近就出手、不够近就靠近。
        //
        // 照原版：`_DXDrawMouseDown` 点到怪就 `g_TargetCret := target`（`ClMain.pas:2866`），
        // 之后 `MouseTimerTimer`（`ClMain.pas:2962-2997`）**每帧**对那个目标调 `AttackTarget`
        // —— `AttackTarget`（`:2691-2743`）自己判"相邻就砍、不够近就朝它旁边那格走/跑"。
        // 也就是原版**没有"按住才打"这一说**：锁住了就一直打，直到它死掉或消失。
        if mode == 2 && !net.as_ref().is_some_and(|n| n.world.self_dead) {
            if let Some(id) = combat_target {
                let step = net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .and_then(|n| n.world.combat_step(id));
                // 手上的挥砍还没播完？原版 `CanNextAction`（`IsIdle`）——"砍完再迈步"，
                // 也是用户 2026-10-09 补充的第 2 条后半句（"攻击完后再次判断是不是要走/跑"）。
                let swing_busy = net
                    .as_ref()
                    .and_then(|n| n.anims.get(&n.world.self_id))
                    .is_some_and(|a| a.attack_busy(started));
                match step {
                    // 死了 / 消失了（尸体被清）/ 自己掉线了 ⇒ 解除锁定
                    None => {
                        println!("[net] 目标不在了，解除锁定");
                        combat_target = None;
                        move_target = None;
                    }
                    Some(mir2_core::world::CombatStep::Attack) => {
                        // ⚠️ **这一步走完再出手** —— 原版 `CanNextAction` = `g_MySelf.IsIdle`
                        //（`Actor.pas:1722-1736`：`m_nCurrentAction <> 0` 就"不空"，不许发下一个动作），
                        // 而 `ActionFinished` 看的是**动画有没有播到 `m_nEndFrame`**
                        // ⇒ 走路那段动作没播完，原版**发不出攻击**。
                        //
                        // 为什么我们特别需要它：我们不做移动预测，位置是**服务端权威**的 ——
                        // 服务端确认"到了"时，画面上的补间才刚起步 ⇒ `combat_step` 已经说"相邻、
                        // 该出手"，于是挥砍动画在人还没走到时就播了（用户 2026-10-08 报的）。
                        let stepping = net
                            .as_ref()
                            .and_then(|n| n.anims.get(&n.world.self_id))
                            .is_some_and(|a| a.moving(started));
                        // 节拍按**自己的等级**算（原版 `CanNextHit`：`1020 − min(370, level*14)`）
                        let level = net
                            .as_ref()
                            .and_then(|n| n.world.ability.as_ref().map(|a| a.level))
                            .unwrap_or(1)
                            .max(1);
                        if can_attack(stepping || swing_busy, attack_at.elapsed(), level) {
                            if let Some(n) = net.as_ref() {
                                let _ = n.attack_target(id);
                                swing_sfx(n, &sound, &sounds);
                            }
                            attack_at = Instant::now();
                        }
                    }
                    // 够不着：复用鼠标走路那条路（步频/节流/"到了就停"都在下面那段里）
                    //
                    // ⚠️ 手上的挥砍没播完就先站着（用户第 2 条："即使是在'追打'时，
                    // 也应在移动结束后再补攻击动作，攻击完后再次判断是不是要走/跑"）。
                    // 不挡的话就是"边走边砍/同手同脚"。
                    Some(mir2_core::world::CombatStep::Approach { x, y, run }) => {
                        if !swing_busy {
                            move_target = Some((x, y, run));
                        }
                    }
                }
            }
        }

        // 服务端**拒了这一步**（`MoveRejected`）⇒ 照原版 `ActionFailed`
        //（`ClMain.pas:4005-4012`：清走法目标 + 锁 1 秒不许再发）。
        //
        // ⚠️ 少了这个反应，撞墙时客户端会朝同一个方向每 `WALK_MS` 发一次、每次都被拒，
        // 人物就"卡在那儿不动"，而且服务端日志刷满 —— "跑不到怪身边"多半有它一份。
        // 计数**变没变**就是"刚刚被拒了一次"的信号（见 `World::move_fail`）。
        let fails = net.as_ref().map_or(0, |n| n.world.move_fail);
        if fails != last_move_fail {
            let reason = net.as_ref().map_or(0, |n| n.world.move_fail_reason);
            last_move_fail = fails;
            move_at = Instant::now();
            if reason == 3 && move_target.is_some() && detour != Some(1) {
                // **撞墙先绕一步**（原版 `ClMain.pas:1886-1923`：直走那格过不去时
                // 先试 `PrivDir`（逆时针 45°），再试 `NextDir`（顺时针 45°），
                // 能走就走），而不是像原来那样"清目标 + 锁 1 秒"——
                // 那正是用户 2026-10-09 第 4 条说的"点哪走哪"手感问题。
                // 两次都撞不上才认输（下面那条），并且**不加 1 秒锁**（只等一个正常步频）。
                detour = Some(detour.map_or(0, |k| k + 1));
                println!(
                    "[move] 前方被挡（reason=3）⇒ 侧移 45° 试第 {} 次",
                    detour.unwrap_or(1) + 1
                );
            } else {
                // 越界(2)/超速(1)，或绕障两次都不行 ⇒ 照原版 `ActionFailed`：
                // 清走法目标 + 锁 [`MOVE_FAIL_LOCK_MS`]（"就近停下"）
                move_target = None;
                detour = None;
                move_block_until = Instant::now() + Duration::from_millis(MOVE_FAIL_LOCK_MS);
                println!(
                    "[move] 服务端拒绝了这一步（reason={reason}，1=超速 2=越界 3=阻挡）\
                     —— 清走法目标 + 锁 {MOVE_FAIL_LOCK_MS}ms"
                );
            }
        }

        // 位置一变 ⇒ 上一步走成了 ⇒ 绕障状态清掉，回到"朝目标直走"
        if let Some(n) = net.as_ref() {
            let p = n.world.self_pos;
            if p != last_self_pos {
                last_self_pos = p;
                detour = None;
            }
        }

        if mode == 2 {
            if let Some((tx, ty, run)) = move_target {
                let here = net
                    .as_ref()
                    .filter(|n| n.world.in_world())
                    .map(|n| (n.world.self_pos.0, n.world.self_pos.1));
                match here {
                    // 没进世界（掉线/还没到）⇒ 目标作废，别攒着一堆移动
                    None => move_target = None,
                    Some(pos) => match next_move_step(pos, (tx, ty), run) {
                        // 已经站在目标格上 ⇒ 收工（原版到点也停）
                        None => move_target = None,
                        Some((dir, step_run)) => {
                            // 绕障：把这一步转 45°（先逆后顺），并且**用走的**
                            //（侧移一小步就够，跑一次 2 格反而更容易顶死在障碍上）。
                            let (dir, step_run) = match detour {
                                Some(0) => (turn45(dir, -1), false),
                                Some(1) => (turn45(dir, 1), false),
                                _ => (dir, step_run),
                            };
                            let gap = if step_run { RUN_MS } else { WALK_MS };
                            // 被拒后的锁还没到期 ⇒ 先别发（原版 `IsUnLockAction`，
                            // `ClMain.pas:4014-4021`：锁着就一律不许动）
                            if move_block_until <= Instant::now()
                                && move_at.elapsed() >= Duration::from_millis(gap)
                            {
                                move_if_online(&net, dir, step_run);
                                move_at = Instant::now();
                            }
                        }
                    },
                }
            }
        }

        // 建号回执（D-32）：**不自动登录**（与原版一致 —— 它也只是弹个提示，
        // `ClMain.pas:3684-3691`）。提示用登录界面那套模态框说，面板切回登录；
        // 那条"只为建号用过的连接"顺手收尾：登录会另开一条（nonce 要重新握手）。
        let signup = net.as_mut().and_then(|n| n.entrance.take_signup_msg());
        if let Some((ok, msg)) = signup {
            login.leave_signup();
            login.busy = false;
            net = None;
            status = if ok {
                "ACCOUNT CREATED - PLEASE SIGN IN".into()
            } else {
                "SIGN UP FAILED".into()
            };
            login.fail(msg.clone());
            println!("[login] 建号{}：{msg}", if ok { "成功" } else { "失败" });
        }
        // 网络侧排出来的音效（挨打 / 死亡）：`pump` 在 `Net` 里，拿不到音频设备。
        if let Some(n) = net.as_mut() {
            for s in n.take_sfx() {
                sfx(&sound, &sounds, s);
            }
            if n.take_gameover() {
                // 自己死亡 ⇒ game over 音乐（原版 `Actor.pas:2373-2374`）
                bgm(&sound, &sounds, mir2_core::sound::BGM_GAMEOVER);
            }
        }
        // 选角场景排出来的音效（选中一个槽 ⇒ 解冻声 `101`，`IntroScn.pas:1170`）
        if let Some(scene) = select_scene.as_mut() {
            for s in scene.take_sfx() {
                sfx(&sound, &sounds, s);
            }
        }
        // 场景 BGM：登录（`IntroScn.pas:518`）与选角（`:1152`）各一首**循环**。
        // 进图音乐 `Music/<地图音乐号>.mp3` **暂时没有声音**（协议没有那个字段、
        // 手上也没有 mp3）⇒ 进地图就停掉上一首，而不是放错一首。
        // 自己死了就别停：那时该响的是 game over 那首（`Actor.pas:2373-2374`）。
        if mode == 4 {
            bgm(&sound, &sounds, mir2_core::sound::BGM_SELECT);
        } else if mode == 1 {
            // ⚠️ 开门动画期间**继续放**登录曲（原版 `PlayBGM(bmg_intro)` 一放放到换场景，
            // `IntroScn.pas:518`；门放完 `ChangeScene(stSelectChr)` 才换成选角曲）。
            // 早先这里多了个 `opened_at.is_none()` ⇒ 门一开就静音三秒（踩过）。
            bgm(&sound, &sounds, mir2_core::sound::BGM_LOGIN);
        } else if mode == 2 && !net.as_ref().is_some_and(|n| n.world.self_dead) && sound.stop_bgm()
        {
            println!("[audio] BGM 停（进图音乐这条路还没接：手上没有 mp3、协议里也没有音乐号）");
        }
        // 怪物"正常声"：鸡叫/鹿鸣…（用户 2026-10-09 第 4 条）。
        // 官方规则与我们的近似写法见 `sfx::monster_ambient` 的说明。
        if mode == 2 {
            if let Some(n) = net.as_ref() {
                monster_ambient(n, &sound, &sounds, Instant::now(), &mut sfx_rng);
            }
        }
        // 脚步：原版在走路动画的**帧 1 / 帧 4** 各响一次（`Actor.pas:2659-2660`），
        // 音色按**自己脚下那一格**定（原版把坐标对齐到偶数格，`Actor.pas:2146-2147`）。
        match (net.as_ref(), map.as_ref()) {
            (Some(n), Some(m)) if n.world.in_world() => match n.self_walk_frame(started) {
                Some((frame, running)) => {
                    if let Some(second_foot) = footstep_of(frame, last_foot_frame) {
                        let (mx, my) = (n.world.self_pos.0, n.world.self_pos.1);
                        let cell = m.at((mx.max(0) / 2 * 2) as usize, (my.max(0) / 2 * 2) as usize);
                        if let Some(c) = cell {
                            let t =
                                mir2_core::sound::terrain(c.bk_img, c.area, c.mid_img, c.fr_img);
                            sfx(
                                &sound,
                                &sounds,
                                mir2_core::sound::footstep(t, running, second_foot),
                            );
                        }
                    }
                    last_foot_frame = Some(frame);
                }
                None => last_foot_frame = None,
            },
            _ => last_foot_frame = None,
        }

        // 进了世界就让相机跟着自己（离线时保持手动镜头）。
        //
        // ⚠️ 跟的是**渲染位置**（补间后的浮点格），不是服务端那一格 —— 见 [`follow_cam`]。
        // 于是"人在屏幕中间不动、地图往前卷"，而不是"人在视口里蹭、镜头一格一跳"。
        if let Some(render) = net.as_ref().and_then(|n| n.self_render(started)) {
            cam = follow_cam(render);
        }

        // 地图镜头夹在合理范围内（允许露出边缘一格）
        if let Some(m) = &map {
            let max_x = (m.width as i32 - (WIN_W as i32 / UNIT_X) + 2).max(0) as f32;
            let max_y = (m.height as i32 - (VIEW_H as i32 / UNIT_Y) + 2).max(0) as f32;
            cam.0 = cam.0.clamp(-2.0, max_x);
            cam.1 = cam.1.clamp(-2.0, max_y);
        }

        // 前景动画的节拍：官方 `m_nAniCount` **每 50 ms 加一**（`PlayScn.pas:963`，
        // 固定定时器、与帧率无关）。所以这里按**真实时间**算，而不是每帧 +1 ——
        // 否则灯会随机器性能忽快忽慢。
        let ani_count = (started.elapsed().as_millis() / 50) as u32;

        canvas.set_draw_color(C_BG);
        canvas.clear();

        // 悬停的那个实体（由 `draw_map_view` 按精灵落点算出来）—— 光标/高亮都用它
        if mode == 2 {
            hover = draw_map_view(
                &mut canvas,
                &tex_creator,
                &mut names,
                &mut ui_texts,
                &mut ui,
                &mut libs,
                &mut tiles,
                &mut draws,
                &mut sprites,
                &asset_dir,
                &map,
                &map_err,
                cam,
                map_i,
                archive.as_ref().map(|a| a.len()).unwrap_or(0),
                debug,
                layers,
                mouse,
                ani_count,
                net.as_ref(),
                combat_target,
            )?;
            // 背包窗画在**世界与 HUD 之上**（它是浮窗；原版也是最后贴）。
            // 素材目录缺失时和 `draw_map_view` 一样什么都不画（那屏已经打了横幅提示）。
            if bag_open {
                if let Some(dir) = asset_dir.as_deref() {
                    hud::draw_bag(
                        &mut canvas,
                        &tex_creator,
                        &mut ui,
                        &mut ui_texts,
                        dir,
                        net.as_ref(),
                        bag_page,
                    )?;
                }
            }
            // 光标跟着悬停状态走：悬停的是**怪**才换准星（原版悬停谁都不换光标，
            // 这是 Crystal 那套；换成"能打的东西"上才有意义）
            let want_cross = hover.is_some_and(|id| {
                net.as_ref().is_some_and(|n| {
                    n.world
                        .entities
                        .get(&id)
                        .is_some_and(|e| e.kind == mir2_core::world::KIND_MONSTER)
                })
            });
            if want_cross != cursor_is_cross {
                cursor_is_cross = want_cross;
                if want_cross {
                    cursor_cross.set();
                } else {
                    cursor_arrow.set();
                }
            }

            // 小地图 / 大地图（原版 `PlayScn.pas:791` 的 `DrawMiniMap`）
            draw_minimaps(
                &mut canvas,
                &tex_creator,
                &mut ui,
                &asset_dir,
                &mut names,
                net.as_ref().filter(|n| n.world.in_world()).map(|n| {
                    (
                        n.world.minimap_index,
                        // ⚠️ 取**补间后**的位置（与画精灵同一份）：服务端位置只在
                        // "到位"时变 ⇒ 拿它当图心就是"跑完一格图才跳一格"（用户报的）。
                        self_render_pos(n.anims.get(&n.world.self_id), n.world.self_pos, started),
                        // 区域标注按**地图显示名**查（服务端下发的 `map_title` 就是它）
                        n.world.map_title.as_str(),
                    )
                }),
                minimap_on,
                bigmap_on,
                (WIN_W, WIN_H),
            )?;
            // 状态窗（F10）画在**小地图之后**：两者都在右上（官方也是贴右缘），
            // 窗口该压在小地图上面，否则标题栏与关闭 X 会被盖住（命中与视觉就不一致了）。
            if status_open {
                if let Some(dir) = asset_dir.as_deref() {
                    status::draw(
                        &mut canvas,
                        &tex_creator,
                        &mut ui,
                        &mut ui_texts,
                        dir,
                        net.as_ref(),
                        status_page,
                    )?;
                }
            }
        } else if mode == 4 {
            if let Some(scene) = select_scene.as_mut() {
                scene.draw(
                    &mut canvas,
                    &tex_creator,
                    &mut ui,
                    &mut ui_texts,
                    &asset_dir,
                    // ⚠️ 给的是**设计尺寸**（800×600）：版式算在设计空间里，画的时候整体乘 1.28
                    UI_WIN,
                    started,
                )?;
            }
        } else if mode == 1 {
            // 开门动画播完 ⇒ **换场景**。原版换到的是**选角**
            //（`ChangeScene(stSelectChr)`，`IntroScn.pas:838-847`），不是地图 ——
            // 早先这里直接 `mode = 2` 是错的（等于把选角整段跳过）。
            if login.door_done() {
                if select_scene.is_some() {
                    mode = 4;
                } else if net.as_ref().is_some_and(|n| n.world.in_world()) {
                    // 认领会话那条路没有选角：门放完直接进地图
                    mode = 2;
                }
                // 两个都没有 ⇒ 留在这一屏等（角色列表马上到）
            }
            login.draw(
                &mut canvas,
                &mut ui,
                &mut ui_texts,
                &tex_creator,
                &asset_dir,
                // ⚠️ 同上：设计尺寸
                UI_WIN,
                started,
            )?;
        } else {
            draw_asset_view(
                &mut canvas,
                &tex_creator,
                &asset_dir,
                &mut loaded,
                &mut sprite_tex,
                &status,
                music_on,
                lib_idx,
                &mut img_idx,
                started,
            )?;
        }

        // 底部那条**开发用**提示条撤了：现在那儿是 HUD 操作面板（字会压在按钮上）。
        // 按键提示改在**进世界时推一行到聊天区**（玩家看得到的那处）。
        if !hint_pushed && mode == 2 {
            if let Some(n) = net.as_mut() {
                if n.entered_once {
                    hint_pushed = true;
                    // 外观诊断（用户 2026-10-09 第 2 条"木剑图不对"）：把自己的外观字节打出来。
                    // `weapon` = 服务端算的 **`Shape*2+性别`**（`ObjBase.pas:20018`）
                    // ⇒ 取图 = `Weapon.wzl[600 * weapon]`（每把武器占男/女两块）。
                    // 手上是木剑（`Shape=1`、男）→ `weapon=2`；若看到 `1`，那是**空块**
                    //（`Shape` 从 1 起），若看到 `4`，那是铁剑。
                    if let Some(f) = n.world.self_feature.as_ref() {
                        println!(
                            "[look] 自己的外观：dress={} weapon={} hair={} race_img={}",
                            f.dress, f.weapon, f.hair, f.race_img
                        );
                    }
                    n.chat.push(trunc(hint_text(2), 30), C_CHAT_SYS);
                    println!("[hud] 按键提示已进聊天区：{}", trunc(hint_text(2), 30));
                }
            }
        }

        let _ = canvas.present();
    }

    println!("[mir2-app] 退出");
    Ok(())
}

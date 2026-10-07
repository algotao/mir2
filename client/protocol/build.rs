//! 由 `../../protocol/*.proto` 生成 Rust 类型（**单一真源**，见 docs/protocol.md §3）。
//!
//! - `protoc` 是**开发期**工具（`brew install protobuf`），不是运行期依赖（C-7）；
//! - 协议版本号也从 `version.txt` **生成**，不在 Rust 侧手抄一份
//!   —— 手抄迟早与 version.txt 漂移，而版本协商正是防漂移的四道防线之一（§9.4）。

use std::env;
use std::fs;
use std::path::{Path, PathBuf};

fn main() {
    let manifest = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    let proto_dir = manifest.join("../../protocol");
    let out_dir = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR"));

    // 收集 .proto：**同一目录全部编进去**（envelope.proto 是消息总目录，缺一条就少一批类型）。
    let mut protos: Vec<PathBuf> = fs::read_dir(&proto_dir)
        .expect("读 protocol/ 目录")
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| p.extension().is_some_and(|x| x == "proto"))
        .collect();
    protos.sort();
    assert!(
        !protos.is_empty(),
        "在 {} 没找到任何 .proto —— 单一真源目录不对？",
        proto_dir.display()
    );

    println!("cargo:rerun-if-changed={}", proto_dir.display());
    for p in &protos {
        println!("cargo:rerun-if-changed={}", p.display());
    }

    // 版本号 -> version.rs
    let ver_path = proto_dir.join("version.txt");
    println!("cargo:rerun-if-changed={}", ver_path.display());
    let ver = fs::read_to_string(&ver_path)
        .expect("读 version.txt")
        .trim()
        .to_string();
    // 必须能被解析成 u32：拼错时在构建期就报错，而不是等到运行期版本协商莫名失败。
    let _: u32 = ver.parse().expect("version.txt 必须是十进制无符号整数");
    fs::write(
        out_dir.join("version.rs"),
        format!("// 由 build.rs 从 protocol/version.txt 生成。不要手改。\npub const VERSION: u32 = {ver};\n"),
    )
    .expect("写 version.rs");

    let mut cfg = prost_build::Config::new();
    // 生成 `impl Message for ...` 的同时保留 doc 注释（IDL 里的注释就是规格）。
    cfg.compile_protos(&protos, &[&proto_dir])
        .expect("protoc 生成失败（是不是没装 protoc？brew install protobuf）");

    // 产物名跟着 package 走：schema 里是 `package mir2;` ⇒ mir2.rs
    let gen = out_dir.join("mir2.rs");
    assert!(
        gen.exists(),
        "prost-build 没有产出 mir2.rs（package 改名了？）"
    );
    let _ = Path::new(&gen);
}

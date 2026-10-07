#!/usr/bin/env bash
# MIR2 协议代码生成（docs/protocol.md §3）
#
# 单一真源 = 本目录下的 .proto。**禁止手写编解码。**
#
# 产物去向：
#   Go   → server/protocol/*.pb.go   （入库；tools/ 与契约测试驱动要引用）
#   Rust → client/protocol/          由 prost-build 在 build.rs 里生成（不在此脚本）
#
# 依赖：protoc（开发期工具，不是运行期依赖）
#   brew install protobuf
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_GO="$ROOT/../server/protocol"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "✗ 缺少 $1" >&2
    echo "  protoc:         brew install protobuf" >&2
    echo "  protoc-gen-go:  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest" >&2
    exit 1
  }
}

need protoc
need protoc-gen-go

echo "== 协议版本 =="
cat "$ROOT/version.txt"

echo "== 生成 Go → $OUT_GO =="
mkdir -p "$OUT_GO"
# shellcheck disable=SC2046
protoc -I "$ROOT" \
  --go_out="$OUT_GO" --go_opt=paths=source_relative \
  $(find "$ROOT" -maxdepth 1 -name '*.proto' | sort)

echo "== 完成 =="
echo "Rust 侧：由 client/protocol/build.rs 的 prost-build 在 cargo build 时生成。"
echo "⚠️ schema 变更 = 两端同时重新生成 + bump version.txt（协议版本协商，protocol.md §9）。"

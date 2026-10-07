#!/usr/bin/env bash
# M2PK 地图容器构建（规格见 docs/assets.md §5，决策见 docs/decisions.md D-11）
#
# 单一真源 = 原始 .map 文件（**不入库**，体量 250+ MB）。
# 本脚本产出 assets/map/maps.m2pk（也是脚本产物，见 assets.md §6）。
#
# 用法：
#   tools/m2pk/build.sh                      # 自动探测源目录
#   MIR2_MAP_SRC=/path/to/map tools/m2pk/build.sh
#   MIR2_MAP_OUT=/tmp/x.m2pk tools/m2pk/build.sh
#
# 依赖：仅 Go 工具链（CGO_ENABLED=0）。
# 打包是离线一次性动作（brotli q11 体积优先，约 30 秒/250 MB），
# 每次跑完都会**逐字节回验**（D-06 硬要求）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${MIR2_MAP_OUT:-$ROOT/assets/map/maps.m2pk}"

# 源目录探测：环境变量 → 与 mir2 平级的两个候选
SRC="${MIR2_MAP_SRC:-}"
if [ -z "$SRC" ]; then
  for c in "$ROOT/../mir2c/map" "$ROOT/../mir2go/data/map"; do
    if [ -d "$c" ]; then SRC="$c"; break; fi
  done
fi

if [ -z "$SRC" ] || [ ! -d "$SRC" ]; then
  echo "✗ 找不到地图源目录" >&2
  echo "  用 MIR2_MAP_SRC=<目录> 显式指定" >&2
  echo "  候选：\$WS/mir2c/map（客户端，770 张）、\$WS/mir2go/data/map（服务端，605 张）" >&2
  exit 1
fi

echo "== 源目录 =="
echo "  $SRC  （$(ls "$SRC" | wc -l | tr -d ' ') 个文件）"
echo "== 输出 =="
echo "  $OUT"

mkdir -p "$(dirname "$OUT")"

echo
echo "== 打包 =="
CGO_ENABLED=0 go run "$ROOT/tools/m2pk" pack -src "$SRC" -out "$OUT" -quiet

echo
echo "== 回验（逐字节）=="
CGO_ENABLED=0 go run "$ROOT/tools/m2pk" verify -in "$OUT" -src "$SRC"

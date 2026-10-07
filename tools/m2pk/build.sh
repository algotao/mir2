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

# 源目录：**唯一真源 = 客户端集**（D-22，SDO 经典版）。
# 刻意**不**自动回退到 mir2go 的那套——它与客户端集不是同一套（5 张内容不同），
# 静默换掉整个游戏世界的地形属于最难排查的一类 bug，宁可报错。
CANON="$ROOT/../mir2c/map"
SRC="${MIR2_MAP_SRC:-$CANON}"

if [ ! -d "$SRC" ]; then
  echo "✗ 地图源目录不存在：${SRC}" >&2
  echo "" >&2
  echo "  唯一真源是客户端集（见 docs/decisions.md D-22）：" >&2
  echo "    ${CANON}" >&2
  echo "  用 MIR2_MAP_SRC=<目录> 可显式覆盖（要有意识地这么做）。" >&2
  exit 1
fi

# 注意：变量后紧跟全角字符时必须用 ${}，否则 bash 会把多字节字符并进变量名。
if [ "$SRC" != "$CANON" ]; then
  echo "⚠️  源目录被显式覆盖为 ${SRC}" >&2
  echo "    唯一真源是 ${CANON}（D-22）；覆盖只应出于对照/实验目的。" >&2
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

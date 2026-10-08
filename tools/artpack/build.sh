#!/usr/bin/env bash
# 美术容器构建（规格见 internal/m2pk/image.go，决策见 docs/decisions.md D-31）
#
# 单一真源 = 原始图库 `mir2c/data/*.wzl` + `*.wzx`（**不入库**，体量 1.7 GB）。
# 本脚本产出 assets/image/images.m2pk（脚本产物，见 assets.md §6）。
#
# 用法：
#   tools/artpack/build.sh                        # 自动探测源目录，默认每组 32 张
#   MIR2_IMAGE_SRC=/path/to/data tools/artpack/build.sh
#   MIR2_IMAGE_OUT=/tmp/x.m2pk MIR2_IMAGE_GROUP=64 tools/artpack/build.sh
#
# 依赖：仅 Go 工具链（CGO_ENABLED=0）。
# 打包是离线一次性动作（brotli q11，1.7 GB 素材约 10 分钟），跑完**逐图回验**
# （记录 + raw 像素，D-06 硬要求）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${MIR2_IMAGE_OUT:-$ROOT/assets/image/images.m2pk}"
GROUP="${MIR2_IMAGE_GROUP:-32}"

# 源目录：唯一真源 = 客户端集（mir2c/data）。刻意不自动回退到别的副本——
# 换了素材却悄悄换了整个游戏的美术，属于最难排查的一类 bug，宁可报错。
CANON="$ROOT/../mir2c/data"
SRC="${MIR2_IMAGE_SRC:-$CANON}"

if [ ! -d "$SRC" ]; then
  echo "✗ 美术源目录不存在：${SRC}" >&2
  echo "" >&2
  echo "  唯一真源是客户端集：" >&2
  echo "    ${CANON}" >&2
  echo "  用 MIR2_IMAGE_SRC=<目录> 可显式覆盖（要有意识地这么做）。" >&2
  exit 1
fi

if [ "$SRC" != "$CANON" ]; then
  echo "⚠️  源目录被显式覆盖为 ${SRC}" >&2
  echo "    唯一真源是 ${CANON}；覆盖只应出于对照/实验目的。" >&2
fi

echo "== 源目录 =="
echo "  $SRC  （$(ls "$SRC"/*.wzl 2>/dev/null | wc -l | tr -d ' ') 个 .wzl）"
echo "== 输出 =="
echo "  $OUT   （每组 ${GROUP} 张）"

mkdir -p "$(dirname "$OUT")"

echo
echo "== 打包 =="
CGO_ENABLED=0 go run "$ROOT/tools/artpack" pack -src "$SRC" -out "$OUT" -group "$GROUP"

echo
echo "== 回验（逐图：记录 + raw 像素）=="
CGO_ENABLED=0 go run "$ROOT/tools/artpack" verify -in "$OUT" -src "$SRC"

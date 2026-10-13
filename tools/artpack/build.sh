#!/usr/bin/env bash
# 美术容器构建（规格见 internal/m2pk/image.go，决策见 docs/decisions.md D-31）
#
# 单一真源 = 原始图库 `mir2c/data/*.wzl` + `*.wzx`（**不入库**，体量 1.7 GB）。
# 本脚本产出 assets/image/images.m2pk（脚本产物，见 assets.md §6）。
#
# 用法：
#   tools/artpack/build.sh                        # 清单驱动，只打包客户端有效素材
#   MIR2_IMAGE_SRC=/path/to/data2 tools/artpack/build.sh
#   MIR2_IMAGE_OUT=/tmp/x.m2pk MIR2_IMAGE_GROUP=64 MIR2_IMAGE_WORKERS=1 tools/artpack/build.sh
# 默认 nice 降优先级、单 worker、brotli q8，避免长期抢满 CPU；都可显式覆写。
#
# 依赖：仅 Go 工具链（CGO_ENABLED=0）。
# 打包是离线一次性动作，默认 q8/单 worker/nice 降优先级；完成后**逐图回验**
# （记录 + raw 像素，D-06 硬要求）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${MIR2_IMAGE_OUT:-$ROOT/assets/image/images.m2pk}"
GROUP="${MIR2_IMAGE_GROUP:-32}"
WORKERS="${MIR2_IMAGE_WORKERS:-1}"
QUALITY="${MIR2_IMAGE_QUALITY:-8}"
NICE="${MIR2_IMAGE_NICE:-15}"

# 源目录：唯一真源 = 客户端集（mir2c/data2）。刻意不自动回退到别的副本——
# 换了素材却悄悄换了整个游戏的美术，属于最难排查的一类 bug，宁可报错。
#
# ⚠️ 2026-10-12：真源从 `mir2c/data` 换成 **`mir2c/data2`**（用户："data2 是最新资源"）。
# 两个目录只差 9 个库，但 `tb-ui` / `ui_dragon` / `ui_n` 在老的 `data` 里是**空壳**
# （64 字节、只有文件头、一个像素都没有）⇒ 打出来的容器缺这一批 UI 素材。
# `data` 先留着做对照，确认无虞后可以删（或把 data2 改名回 data，那时把本行改回来）。
CANON="$ROOT/../mir2c/data2"
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
MANIFEST="$ROOT/client/assets/image-libraries.tsv"
nice -n "$NICE" env CGO_ENABLED=0 go run "$ROOT/tools/artpack" pack -src "$SRC" -out "$OUT" -manifest "$MANIFEST" -group "$GROUP" -workers "$WORKERS" -quality "$QUALITY"

echo
echo "== 回验（逐图：记录 + raw 像素）=="
nice -n "$NICE" env CGO_ENABLED=0 go run "$ROOT/tools/artpack" verify -in "$OUT" -src "$SRC" -manifest "$MANIFEST"

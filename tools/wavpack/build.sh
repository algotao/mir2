#!/usr/bin/env bash
# 音频资产转换（源 = 客户端集的 mir2c/wav，产物 = assets/audio）
# 规格与取舍见 tools/wavpack/main.go 的文件头与 README「音频」。
#
# 单一真源 = mir2c/wav（含 sound.lst）；原始 wav **不入库**。
# 本脚本产出 assets/audio/（也是脚本产物 ⇒ 不入库，见 docs/assets.md §6）。
#
# 用法：
#   tools/wavpack/build.sh
#   MIR2_WAV_SRC=/path/to/wav tools/wavpack/build.sh
#   MIR2_AUDIO_OUT=/tmp/audio tools/wavpack/build.sh
#
# 依赖：仅 Go 工具链（CGO_ENABLED=0）。转换是离线一次性动作（777 个 / 209 MB，
# 几秒钟），每个文件都**读回自检**（头 / 帧数 / 峰值）。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${MIR2_AUDIO_OUT:-$ROOT/assets/audio}"

# 源目录：**唯一真源 = 客户端集**（同 tools/m2pk/build.sh 的做法）。
CANON="$ROOT/../mir2c/wav"
SRC="${MIR2_WAV_SRC:-$CANON}"

if [ ! -d "$SRC" ]; then
  echo "✗ 音频源目录不存在：${SRC}" >&2
  echo "" >&2
  echo "  唯一真源是客户端集：" >&2
  echo "    ${CANON}" >&2
  echo "  用 MIR2_WAV_SRC=<目录> 可显式覆盖。" >&2
  exit 1
fi

if [ "$SRC" != "$CANON" ]; then
  echo "⚠️  源目录被显式覆盖为 ${SRC}" >&2
  echo "    唯一真源是 ${CANON}；覆盖只应出于对照/实验目的。" >&2
fi

echo "== 源目录 =="
echo "  $SRC  （$(ls "$SRC" | wc -l | tr -d ' ') 个文件）"
echo "== 输出 =="
echo "  $OUT"

mkdir -p "$OUT"

echo
echo "== 转换（音效 22.05k 单声道；BGM 原样；原版从不播的不产出）=="
CGO_ENABLED=0 go run "$ROOT/tools/wavpack" pack -src "$SRC" -out "$OUT"

echo
echo "== 量一遍产物 =="
CGO_ENABLED=0 go run "$ROOT/tools/wavpack" survey -src "$OUT"

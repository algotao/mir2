#!/usr/bin/env bash
# 音频资产转换 + 打包（源 = 客户端集的 mir2c/wav，产物 = **一个容器文件**）
# 取舍见 tools/wavpack/main.go 的文件头、docs/assets.md §6b 与 D-29。
#
# 单一真源 = mir2c/wav（含 sound.lst）；原始 wav **不入库**。
# 本脚本产出 assets/audio/sounds.m2pk（也是脚本产物 ⇒ 不入库，见 assets.md §6）。
#
# 为什么是一个文件（而不是一堆 wav）：
#   1. 好分发、好校验（容器里名字是规范化键：小写、去扩展名）；
#   2. **不再受文件名大小写影响** —— 客户端集里"清单写小写、文件写大写"
#      （`game-over2.wav` vs `Game-over2.wav`）那类坑，在容器里根本不存在。
#
# 用法：
#   tools/wavpack/build.sh
#   MIR2_WAV_SRC=/path/to/wav tools/wavpack/build.sh
#   MIR2_AUDIO_OUT=/tmp/sounds.m2pk MIR2_AUDIO_CODEC=pcm tools/wavpack/build.sh
#
# 依赖：仅 Go 工具链（CGO_ENABLED=0）。转换 + 打包一次约 2 秒（777 个 / 209 MB），
# 两条自检都会跑：每个文件解码回来比 SNR；清单里源能播的编号在容器里一个不少。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${MIR2_AUDIO_OUT:-$ROOT/assets/audio/sounds.m2pk}"
CODEC="${MIR2_AUDIO_CODEC:-adpcm}"
# 音乐优先：BGM 保持 16bit PCM（无损），只对音效用 ADPCM（35.2 MB）
BGM_PCM="${MIR2_AUDIO_BGM_PCM:-0}"

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
echo "== 输出（单个容器文件）=="
echo "  $OUT   编码 = $CODEC"

mkdir -p "$(dirname "$OUT")"

echo
echo "== 转换 + 打包（音效 22.05k 单声道；BGM 原样；原版从不播的不产出）=="
# ⚠️ 不要用 `EXTRA=()` + `"${EXTRA[@]}"`：macOS 自带的 bash 3.2 在 `set -u` 下
# 会报 "unbound variable"（这条踩过）—— 老老实实分两条命令写。
if [ "$BGM_PCM" = "1" ]; then
  CGO_ENABLED=0 go run "$ROOT/tools/wavpack" pack -src "$SRC" -out "$OUT" -codec "$CODEC" -bgm-pcm
else
  CGO_ENABLED=0 go run "$ROOT/tools/wavpack" pack -src "$SRC" -out "$OUT" -codec "$CODEC"
fi

echo
echo "== 用 m2pk 的工具复核容器结构 =="
CGO_ENABLED=0 go run "$ROOT/tools/m2pk" info -in "$OUT" | head -8

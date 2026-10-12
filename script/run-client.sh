#!/usr/bin/env bash
# 启动**游戏客户端**（在仓库任意位置都能跑）。
#
# 用法：
#   script/run-client.sh                    # 连本地 gamesvr，进游戏
#   script/run-client.sh --assetview        # 打开素材观察器（找素材用，默认 Prguse）
#   script/run-client.sh --assetview Mon1   # 打开指定图库
#   script/run-client.sh --assetview Items  # 物品图标
#   script/run-client.sh --release          # release 跑（画面更顺，编译慢）
#   script/run-client.sh --status           # 只看服务端在不在（不开客户端）
#
# 环境变量（都有默认值）：
#   MIR2_SERVER    服务端地址              默认 127.0.0.1:7500
#   MIR2_ASSET_DIR 素材目录                默认 ../mir2c/data
#
# ⚠️ 客户端要连服务端 ⇒ 先 `script/restart-dev.sh` 把 gamesvr 起来；
# `--assetview` **不需要**服务端（只是翻素材）。
#
# 素材观察器键位（**没有滚轮**，翻页用键盘 —— 避免误操作）：
#   PageDown/PageUp 翻页，↓/↑ 翻一行，Home/End 到头/到尾
#   `[` `]`（或 `,` `.`）切图库；点图 ⇒ 显示 ID 并复制；C 复制数字、
#   Shift+C 复制 `库名[ID]`；Esc/Q 退出。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLIENT_DIR="$ROOT/client"

DEFAULT_ASSET_DIR="$(cd "$ROOT/.." && pwd)/mir2c/data2"
ASSET_DIR="${MIR2_ASSET_DIR:-$DEFAULT_ASSET_DIR}"
SERVER="${MIR2_SERVER:-127.0.0.1:7500}"

RELEASE=0
ARGS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --release | -r)
            RELEASE=1
            shift
            ;;
        --assetview | -a)
            ARGS+=(--assetview)
            shift
            # 后面跟的若不是开关 ⇒ 当图库名
            if [ $# -gt 0 ] && [ "${1#-}" = "$1" ]; then
                ARGS+=("$1")
                shift
            fi
            ;;
        --status)
            echo "服务端在不在："
            lsof -nP -iTCP -sTCP:LISTEN 2>/dev/null | grep -E ":${SERVER##*:}" || echo "  ${SERVER} 没有在听（先跑 script/restart-dev.sh）"
            exit 0
            ;;
        -h | --help)
            sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            ARGS+=("$1")
            shift
            ;;
    esac
done

BUILD_FLAG=()
if [ "$RELEASE" = "1" ]; then
    BUILD_FLAG=(--release)
fi

if [ ! -d "$ASSET_DIR" ]; then
    echo "素材目录不存在：$ASSET_DIR（设 MIR2_ASSET_DIR 指定）" >&2
    exit 2
fi

# ⚠️ 变量后面紧跟中文要用 ${…}：macOS 自带的 bash 3.2 会把多字节字符
# 当成变量名的一部分 ⇒ "unbound variable"。
if [ ${#ARGS[@]} -gt 0 ]; then
    echo "[run-client] 素材观察器（素材目录 ${ASSET_DIR}）"
else
    echo "[run-client] 连 ${SERVER}（素材目录 ${ASSET_DIR}）"
fi

cd "$CLIENT_DIR"
MIR2_SERVER="$SERVER" MIR2_ASSET_DIR="$ASSET_DIR" \
    exec cargo run ${BUILD_FLAG[@]+"${BUILD_FLAG[@]}"} -p mir2-app -- ${ARGS[@]+"${ARGS[@]}"}

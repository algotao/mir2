#!/usr/bin/env bash
# 开发用的**服务端重启**：重编译 → 杀掉旧进程 → 起新的。
#
# 为什么要这个脚本：每次改完服务端都要敲 6~8 条命令（编译两个二进制、按 pid 杀、
# 起两个服务、`-data ./data` 还是相对路径…），漏一步就是"改了没生效"或者
# "端口被旧进程占着，新的起不来"。这里一次做完，并且**等端口真的在听**才返回。
#
# 用法（在仓库任意位置都能跑）：
#   script/restart-dev.sh              # 全量：编译 + 重启两个服务
#   script/restart-dev.sh --game-only  # 只重编/重启 gamesvr（客户端只连它）
#   script/restart-dev.sh --no-build   # 不编译，直接重启（二进制已经是新的）
#   script/restart-dev.sh --fg         # gamesvr 前台跑（日志直接打在终端，Ctrl+C 停）
#   script/restart-dev.sh --stop       # 只杀进程，不编译也不起
#   script/restart-dev.sh --status     # 看在跑什么、端口在不在
#
# 环境变量（都有默认值，一般不用给）：
#   MIR2_DEV_DIR  运行目录（pid / 日志 / 数据库）  默认 /tmp/mir2dev
#   MIR2_MAP_DIR  地图目录                        默认 ../mir2c/map
#   MIR2_ASSET_DIR 客户端素材目录（只用于结尾那行提示）默认 ../mir2c/data
#   MIR2_DB       数据库文件                      默认 $MIR2_DEV_DIR/mir2go.db
#   MIR2_GAME_PROTO gamesvr 的新协议监听地址       默认 127.0.0.1:7500
#   MIR2_GAME_ADDR  gamesvr 的 legacy 监听地址     默认 :7200
#
# ⚠️ 目录约定：`-data` 是**相对路径**（服务端按当前目录找 `data/`），
# 所以脚本会先 `cd` 到 `server/` 再起进程 —— 换了目录就找不到物品表/NPC 脚本。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_DIR="$ROOT/server"

DEV_DIR="${MIR2_DEV_DIR:-/tmp/mir2dev}"
MAP_DIR="${MIR2_MAP_DIR:-$(cd "$ROOT/.." && pwd)/mir2c/map}"
ASSET_DIR="${MIR2_ASSET_DIR:-$(cd "$ROOT/.." && pwd)/mir2c/data}"
DB="${MIR2_DB:-$DEV_DIR/mir2go.db}"
GAME_PROTO="${MIR2_GAME_PROTO:-127.0.0.1:7500}"
GAME_ADDR="${MIR2_GAME_ADDR:-:7200}"
BIN_DIR="$DEV_DIR/bin"
GAME_PID="$DEV_DIR/game.pid"
ACC_PID="$DEV_DIR/acc.pid"
GAME_LOG="$DEV_DIR/gamesvr.log"
ACC_LOG="$DEV_DIR/accountsvc.log"

DO_BUILD=1
GAME_ONLY=0
FOREGROUND=0
STOP_ONLY=0
STATUS_ONLY=0
for arg in "$@"; do
    case "$arg" in
        --game-only) GAME_ONLY=1 ;;
        --no-build) DO_BUILD=0 ;;
        --fg) FOREGROUND=1 ;;
        --stop) STOP_ONLY=1 ;;
        --status) STATUS_ONLY=1 ;;
        -h | --help)
            sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "未知参数：${arg}（-h 看用法）" >&2; exit 2 ;;
    esac
done

mkdir -p "$BIN_DIR"

# ---------- 状态 ----------
status() {
    echo "=== 进程 ==="
    for f in "$GAME_PID" "$ACC_PID"; do
        [ -f "$f" ] || continue
        pid=$(cat "$f" 2>/dev/null || true)
        if [ -n "$pid" ] && ps -p "$pid" -o pid= >/dev/null 2>&1; then
            ps -o pid,etime,command -p "$pid" | tail -1
        else
            echo "  ($(basename "$f") 指向的 $pid 已不在)"
        fi
    done
    echo "=== 端口 ==="
    for p in 7500 17000 17100 7200; do
        if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
            echo "  $p 在听"
        else
            echo "  $p 没人听"
        fi
    done
}

if [ "$STATUS_ONLY" = 1 ]; then
    status
    exit 0
fi

# ---------- 杀旧进程 ----------
# ⚠️ 只杀 pid 文件里那个，**不用 pkill 模糊匹配**：别把别的 gamesvr（或同名进程）顺手杀了。
# 光 kill 不够：进程可能正握着端口 ⇒ 等它真的没了再往下走。
stop_one() {
    local pidfile="$1" name="$2"
    [ -f "$pidfile" ] || return 0
    local pid
    pid=$(cat "$pidfile" 2>/dev/null || true)
    [ -n "$pid" ] || return 0
    if ps -p "$pid" -o pid= >/dev/null 2>&1; then
        # ⚠️ 用 `${name}` 而不是 `$name`：macOS 自带的 bash 3.2 会把紧跟着的全角
        # `（` 也算进变量名 ⇒ `${name}（pid` 被当成"名叫 name（ 的变量"，直接 unbound。
        echo "停 ${name}（pid ${pid}）…"
        kill "$pid" 2>/dev/null || true
        for _ in $(seq 1 30); do
            ps -p "$pid" -o pid= >/dev/null 2>&1 || break
            sleep 0.1
        done
        # 还没走（极少数：卡在存档）⇒ 强杀
        if ps -p "$pid" -o pid= >/dev/null 2>&1; then
            echo "  $name 没自己退出 ⇒ SIGKILL"
            kill -9 "$pid" 2>/dev/null || true
            sleep 0.5
        fi
    fi
    rm -f "$pidfile"
}

echo "=== 停旧进程 ==="
stop_one "$GAME_PID" gamesvr
if [ "$GAME_ONLY" = 0 ]; then
    stop_one "$ACC_PID" accountsvc
fi

if [ "$STOP_ONLY" = 1 ]; then
    echo "已停（没编译、没起新的）"
    exit 0
fi

# ---------- 编译 ----------
if [ "$DO_BUILD" = 1 ]; then
    echo "=== 编译 ==="
    (cd "$SERVER_DIR" && go build -o "$BIN_DIR/gamesvr" ./cmd/gamesvr)
    if [ "$GAME_ONLY" = 0 ]; then
        (cd "$SERVER_DIR" && go build -o "$BIN_DIR/accountsvc" ./cmd/accountsvc)
    fi
    echo "  → $BIN_DIR/gamesvr$( [ "$GAME_ONLY" = 0 ] && echo " / accountsvc" )"
fi

# ---------- 起新进程 ----------
# ⚠️ 必须 `cd` 到 server/：`-data ./data` 是相对路径（物品表 / NPC 脚本 / 商城定义都在里面）。
cd "$SERVER_DIR"

wait_port() {
    # $1 = 端口；最多等 ~5 秒（地图加载要一会儿）
    local port="$1" i
    for i in $(seq 1 50); do
        if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.1
    done
    return 1
}

if [ "$GAME_ONLY" = 0 ]; then
    echo "=== 起 accountsvc ==="
    nohup "$BIN_DIR/accountsvc" -db "$DB" -data ./data \
        -login-addr :17000 -sel-addr :17100 >"$ACC_LOG" 2>&1 &
    echo $! >"$ACC_PID"
fi

echo "=== 起 gamesvr ==="
if [ "$FOREGROUND" = 1 ]; then
    # 前台：日志直接打在终端，Ctrl+C 就是停（pid 文件照样写，方便 --stop）
    echo "（前台运行；Ctrl+C 停止）"
    "$BIN_DIR/gamesvr" -db "$DB" -data ./data \
        -addr "$GAME_ADDR" -proto-addr "$GAME_PROTO" \
        -map-dir "$MAP_DIR" -map 0 -allow-new-account 2>&1 | tee "$GAME_LOG" &
    echo $! >"$GAME_PID"
    wait
    exit 0
fi

nohup "$BIN_DIR/gamesvr" -db "$DB" -data ./data \
    -addr "$GAME_ADDR" -proto-addr "$GAME_PROTO" \
    -map-dir "$MAP_DIR" -map 0 -allow-new-account >"$GAME_LOG" 2>&1 &
echo $! >"$GAME_PID"

# ---------- 自检：端口真的在听吗 ----------
if wait_port 7500; then
    echo "  7500 在听 ✓"
else
    echo "  ⚠️ 7500 没起来 —— 看日志：$GAME_LOG" >&2
    tail -5 "$GAME_LOG" >&2 || true
    exit 1
fi
if [ "$GAME_ONLY" = 0 ] && wait_port 17000; then
    echo "  17000 在听 ✓"
fi

echo "=== 好了 ==="
echo "  gamesvr   pid $(cat "$GAME_PID")  日志 $GAME_LOG"
[ "$GAME_ONLY" = 0 ] && echo "  accountsvc pid $(cat "$ACC_PID")  日志 $ACC_LOG"
echo "客户端：cd $ROOT/client && MIR2_SERVER=$GAME_PROTO MIR2_ASSET_DIR=$ASSET_DIR cargo run -p mir2-app"

#!/usr/bin/env bash
# **素材观察器**：把 mir2c/data 下的 .wzl 图库摊成缩略图，点一张就显示它的
# ID（窗口最下方，同时复制到剪贴板）—— 用来"找素材"（图号是散在代码里的
# 魔法数字，人眼认不出第 N 张长什么样）。
#
# 用法（在仓库任意位置都能跑）：
#   script/assetview.sh                 # 打开 Prguse（界面图库）
#   script/assetview.sh Mon1            # 打开 Mon1（怪物图库）
#   script/assetview.sh Items           # 打开 Items（物品图标）
#   script/assetview.sh Hum             # 打开 Hum（人物）
#   script/assetview.sh Prguse --release # 用 release 跑（图多时更顺）
#   script/assetview.sh --list          # 只列出目录下有哪些图库，不开窗
#
# 参数：
#   第一个位置参数 = 图库名（默认 Prguse）
#   --dir <目录>   指定素材目录（不给就按 MIR2_ASSET_DIR / MIR2C_DATA /
#                  仓库并列的 mir2c/data 找）
#   --release      用 release 构建（首次编译慢，运行快）
#   --list         只打印图库清单（按张数从大到小）
#
# 环境变量：
#   MIR2_ASSET_DIR  素材目录（与客户端同一个变量）
#   MIR2C_DATA      备用素材目录
#
# 窗内键位：滚轮/PageUp/PageDown 翻页；`[` `]`（或 `,` `.`）切图库；
# 点图 ⇒ 显示 ID 并复制；`C` 复制数字、`Shift+C` 复制 `库名[ID]`；Esc/Q 退出。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLIENT_DIR="$ROOT/client"

# 素材目录：显式 --dir > MIR2_ASSET_DIR > MIR2C_DATA > 仓库并列的 mir2c/data
DEFAULT_ASSET_DIR="$(cd "$ROOT/.." && pwd)/mir2c/data"
ASSET_DIR="${MIR2_ASSET_DIR:-${MIR2C_DATA:-$DEFAULT_ASSET_DIR}}"

LIB="Prguse"
RELEASE=0
LIST_ONLY=0

# ---- 参数解析 ----
while [ $# -gt 0 ]; do
    case "$1" in
        --dir)
            ASSET_DIR="${2:-}"
            shift 2
            ;;
        --release | -r)
            RELEASE=1
            shift
            ;;
        --list | -l)
            LIST_ONLY=1
            shift
            ;;
        -h | --help)
            sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        -*)
            echo "未知参数：$1（用 --help 看用法）" >&2
            exit 2
            ;;
        *)
            LIB="$1"
            shift
            ;;
    esac
done

if [ ! -d "$ASSET_DIR" ]; then
    echo "素材目录不存在：$ASSET_DIR" >&2
    echo "（用 --dir 指定，或设 MIR2_ASSET_DIR）" >&2
    exit 2
fi

# ---- --list：只列图库（按张数降序，先列值得看的那几个）----
if [ "$LIST_ONLY" = "1" ]; then
    echo "素材目录：$ASSET_DIR"
    echo
    # ⚠️ 张数读 `.wzx` 的头：**48 字节头，偏移 44 的 u32 就是 ImageCount**
    # （见 `client/core/src/wzx.rs`）。按文件大小估会差好几倍（扩展格式每项 16 字节）。
    python3 - "$ASSET_DIR" <<'PY'
import glob
import os
import struct

d = os.sys.argv[1]
rows = []
for p in sorted(glob.glob(os.path.join(d, "*.wzx"))):
    try:
        with open(p, "rb") as f:
            head = f.read(48)
        if len(head) < 48:
            continue
        count = struct.unpack("<I", head[44:48])[0]
        rows.append((os.path.basename(p)[:-4], count))
    except OSError:
        continue
rows.sort(key=lambda r: -r[1])
print(f"{'图库':<26}{'张数':>10}")
print("-" * 36)
for name, count in rows:
    print(f"{name:<26}{count:>10}")
print(f"\n共 {len(rows)} 个图库")
PY
    exit 0
fi

BUILD_FLAG=()
if [ "$RELEASE" = "1" ]; then
    BUILD_FLAG=(--release)
fi

# ⚠️ 变量后面紧跟中文时要用 `${…}`：macOS 自带的 bash 3.2 会把多字节字符
# 当成变量名的一部分（`LIB（素材目录` ⇒ "unbound variable"）。
echo "[assetview] 图库 ${LIB}（素材目录 ${ASSET_DIR}）"
cd "$CLIENT_DIR"
exec cargo run ${BUILD_FLAG[@]+"${BUILD_FLAG[@]}"} -p mir2-assetview -- --dir "$ASSET_DIR" --lib "$LIB"

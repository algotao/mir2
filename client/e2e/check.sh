#!/usr/bin/env bash
# 无头渲染自检 —— 可放进 CI（plan §M1「CI 门禁」）。
#
# 依次：构建 → 确认 e2e 没依赖 SDL3 → 多机位跑「平移自检」。
# 平移自检的做法：同一块地图在两个**只差平移**的机位各渲染一遍，
# 重叠区必须逐像素相同。它能抓住"该画却没画"这类**只在镜头移动时暴露**
# 的漏画（2026-10-07 的"高树凭空出现"就是这类）。
#
# 依赖：素材（$MIR2_ASSET_DIR 或仓库旁的 mir2c/data）+ 地图容器
# （先跑 tools/m2pk/build.sh）。可用 MIR2_CHECK_MAP / MIR2_CHECK_CAMS 覆盖范围。
set -euo pipefail

cd "$(dirname "$0")"

echo "== 1/3 构建 =="
cargo build --release -p mir2-e2e

echo
echo "== 2/3 CI 门禁：e2e 不得依赖 SDL3（plan §4.2 / C-8）=="
if cargo tree -p mir2-e2e | grep -q sdl3; then
    echo "✗ mir2-e2e 依赖了 sdl3 —— 它会无法在无显示环境运行"
    exit 1
fi
echo "✓ 无 sdl3"

echo
echo "== 3/3 平移自检（多机位）=="
MAP="${MIR2_CHECK_MAP:-0}"
CAMS="${MIR2_CHECK_CAMS:-340,331 340,333 300,300 200,200 500,500 600,600 400,200}"

fail=0
for cam in $CAMS; do
    # 用 `cargo run` 而不是拼 target 路径：换 target 目录（本仓库把它放在仓库外）也不用改
    if cargo run --quiet --release -p mir2-e2e -- panself -map "$MAP" -cam "$cam" -w 1024 -h 722 \
        | grep -q "一致 ✓"; then
        printf "  %-10s ✓\n" "$cam"
    else
        printf "  %-10s ✗\n" "$cam"
        fail=1
    fi
done

if [ "$fail" -ne 0 ]; then
    echo
    echo "✗ 存在机位未通过：重叠区内容不是单纯的平移 ⇒ 有图块该画没画"
    exit 1
fi
echo
echo "✓ 全部机位通过"

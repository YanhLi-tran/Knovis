#!/usr/bin/env bash
# 跨平台编译 local-agent
# 用法：
#   ./scripts/build-local-agent.sh                          # 默认编译全部目标平台
#   ./scripts/build-local-agent.sh linux-amd64              # 仅编译指定目标
#   ./scripts/build-local-agent.sh -v 0.1.1                 # 指定版本号（写入文件名）
#   ./scripts/build-local-agent.sh -o /tmp/dist linux-amd64 # 自定义输出目录
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/local-agent"
OUTPUT_DIR="dist"
VERSION=""
TARGETS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -v|--version) VERSION="$2"; shift 2 ;;
        -o|--output)  OUTPUT_DIR="$2"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) TARGETS+=("$1"); shift ;;
    esac
done

# 默认目标：Windows/Linux x86_64 + macOS Intel/Apple Silicon
if [[ ${#TARGETS[@]} -eq 0 ]]; then
    TARGETS=(windows-amd64 linux-amd64 darwin-amd64 darwin-arm64)
fi

mkdir -p "$OUTPUT_DIR"
VER_SUFFIX=""
[[ -n "$VERSION" ]] && VER_SUFFIX="-v$VERSION"

TOTAL=${#TARGETS[@]}
IDX=0
FAILED=()

for target in "${TARGETS[@]}"; do
    IDX=$((IDX+1))
    if [[ "$target" != *-* ]]; then
        echo "[SKIP] 无效目标格式: $target (应为 GOOS-GOARCH)"
        continue
    fi
    GOOS="${target%-*}"
    GOARCH="${target#*-}"

    EXT=""
    [[ "$GOOS" == "windows" ]] && EXT=".exe"
    OUT_NAME="local-agent-${target}${VER_SUFFIX}${EXT}"
    OUT_PATH="$OUTPUT_DIR/$OUT_NAME"

    echo ""
    echo "[$IDX/$TOTAL] 编译 $target -> $OUT_NAME"

    if ! GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
         go build -trimpath -ldflags "-s -w" -o "$OUT_PATH" "$PKG"; then
        echo "  [FAIL] 编译失败"
        FAILED+=("$target")
        continue
    fi

    SIZE=$(du -m "$OUT_PATH" | cut -f1)
    echo "  [OK] ${SIZE} MB"
done

echo ""
if [[ ${#FAILED[@]} -gt 0 ]]; then
    echo "[SUMMARY] 失败目标: ${FAILED[*]}"
    exit 1
else
    echo "[SUMMARY] 全部编译成功 ($TOTAL/$TOTAL)"
    ls -lh "$OUTPUT_DIR"/local-agent-* 2>/dev/null | awk '{print $9, $5}'
fi

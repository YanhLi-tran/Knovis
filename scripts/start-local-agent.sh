#!/usr/bin/env bash
# local-agent 一键启动脚本（Linux/macOS）
# 用法：
#   ./scripts/start-local-agent.sh                                # 交互式输入 token
#   ./scripts/start-local-agent.sh --token YOUR_JWT_TOKEN         # 直接传 token
#   ./scripts/start-local-agent.sh --token YOUR_JWT_TOKEN --server ws://1.2.3.4:8001
#
# 环境变量优先级：命令行参数 > 环境变量 > 交互输入
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVER="ws://127.0.0.1:8001"
TOKEN="${AGENT_TOKEN:-}"

# 解析命令行参数
while [[ $# -gt 0 ]]; do
    case "$1" in
        --token)  TOKEN="$2";  shift 2 ;;
        --server) SERVER="$2"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) echo "[ERROR] 未知参数: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$TOKEN" ]]; then
    echo
    echo "============================================"
    echo "  local-agent 启动器"
    echo "============================================"
    echo
    read -r -p "请输入 JWT access token: " TOKEN
    if [[ -z "$TOKEN" ]]; then
        echo "[ERROR] token 不能为空" >&2
        exit 1
    fi
fi

# 选择二进制：按优先级查找
BIN=""
CANDIDATES=(
    "$ROOT/dist/local-agent-$(go env GOOS)-$(go env GOARCH)"
    "$ROOT/local-agent/local-agent"
    "$ROOT/local-agent/local-agent.exe"
)
for c in "${CANDIDATES[@]}"; do
    if [[ -x "$c" ]]; then
        BIN="$c"
        break
    fi
done

if [[ -z "$BIN" ]]; then
    echo "[ERROR] 未找到 local-agent 二进制" >&2
    echo
    echo "请先编译："
    echo "  ./scripts/build-local-agent.sh"
    echo "或："
    echo "  cd local-agent && go build -o local-agent ."
    exit 1
fi

echo
echo "[INFO] 二进制: $BIN"
echo "[INFO] 服务器: $SERVER"
echo "[INFO] 启动中... (Ctrl+C 退出)"
echo

exec "$BIN" -server "$SERVER" -token "$TOKEN"

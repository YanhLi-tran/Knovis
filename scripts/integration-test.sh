#!/usr/bin/env bash
# 集成测试启动器（Linux/macOS/Git Bash on Windows）
# 用法：./scripts/integration-test.sh [--server URL] [--llm-key KEY] [--skip a,b] [--only a,b]
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 优先用 python3，回退 python
if command -v python3 >/dev/null 2>&1; then
    PY=python3
elif command -v python >/dev/null 2>&1; then
    PY=python
else
    echo "[ERROR] 未找到 Python，请先安装 Python 3.7+" >&2
    exit 2
fi

exec "$PY" "$SCRIPT_DIR/integration_test.py" "$@"

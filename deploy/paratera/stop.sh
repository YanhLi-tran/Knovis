#!/bin/bash
# =============================================================================
# Knovis @ 并行智算云容器 - 一键停止脚本
# 用法: bash stop.sh [--keep-db]
#       --keep-db  不停止 MySQL/Redis（只停应用服务）
# =============================================================================
set -e

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TMUX_SESSION="knovis"
KEEP_DB=false

if [ "$1" = "--keep-db" ]; then
    KEEP_DB=true
fi

info "===== 停止 Knovis 服务 ====="

# 1. 杀掉 tmux session（里面跑着所有 Python/Go 服务）
if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    info "停止 tmux session: $TMUX_SESSION"
    tmux kill-session -t "$TMUX_SESSION"
    info "tmux session 已停止"
else
    info "没有运行中的 knovis tmux session"
fi

# 2. 兜底: 按端口杀掉残留进程
for port in 8001 8002 8003 8080; do
    pid=$(lsof -ti:$port 2>/dev/null || true)
    if [ -n "$pid" ]; then
        warn "杀掉端口 $port 残留进程 PID=$pid"
        kill -9 $pid 2>/dev/null || true
    fi
done

# 3. 停止 MySQL/Redis（除非 --keep-db）
if [ "$KEEP_DB" = false ]; then
    info "停止 MySQL..."
    service mysql stop 2>/dev/null || mysqladmin shutdown -uroot -p"knovis123" 2>/dev/null || true

    info "停止 Redis..."
    redis-cli shutdown 2>/dev/null || true

    info "MySQL + Redis 已停止"
else
    info "保留 MySQL + Redis 运行（--keep-db）"
fi

echo ""
info "===== 所有服务已停止 ====="
echo "  提示: 关机前请确保服务已停止并使用'保存环境关机'，避免数据丢失"
echo "  数据持久化在 /root/shared-nvme/，关机不会丢失"

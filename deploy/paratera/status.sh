#!/bin/bash
# =============================================================================
# Knovis @ 并行智算云容器 - 服务状态检查
# 用法: bash status.sh
# =============================================================================

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$PROJECT_DIR/logs"
TMUX_SESSION="knovis"

echo -e "${CYAN}===== Knovis 服务状态 =====${NC}"
echo ""

# tmux session 状态
echo -e "${CYAN}[进程管理]${NC}"
if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    echo -e "  tmux session: ${GREEN}运行中${NC}"
    echo "  窗口列表:"
    tmux list-windows -t "$TMUX_SESSION" 2>/dev/null | while read line; do
        echo "    $line"
    done
else
    echo -e "  tmux session: ${RED}未运行${NC}"
fi
echo ""

# 端口检查
echo -e "${CYAN}[端口监听]${NC}"
check_port() {
    local port=$1 name=$2
    if ss -tlnp 2>/dev/null | grep -q ":$port " || netstat -tlnp 2>/dev/null | grep -q ":$port "; then
        echo -e "  ${GREEN}✅${NC} $name (端口 $port) - 监听中"
        return 0
    else
        echo -e "  ${RED}❌${NC} $name (端口 $port) - 未监听"
        return 1
    fi
}
check_port 3306 "MySQL"
check_port 6379 "Redis"
check_port 8002 "memory-service"
check_port 8003 "doc-service"
check_port 8080 "Knovis 用户服务"
check_port 8001 "agent-go"
echo ""

# HTTP 健康检查
echo -e "${CYAN}[HTTP 健康检查]${NC}"
check_http() {
    local url=$1 name=$2
    local code=$(curl -sf -o /dev/null -w "%{http_code}" --connect-timeout 3 "$url" 2>/dev/null || echo "000")
    if [ "$code" = "200" ] || [ "$code" = "401" ] || [ "$code" = "405" ]; then
        echo -e "  ${GREEN}✅${NC} $name → $url (HTTP $code)"
    elif [ "$code" = "000" ]; then
        echo -e "  ${RED}❌${NC} $name → $url (连接失败)"
    else
        echo -e "  ${YELLOW}⚠️${NC} $name → $url (HTTP $code)"
    fi
}
check_http "http://127.0.0.1:8002/health" "memory-service"
check_http "http://127.0.0.1:8003/health" "doc-service"
check_http "http://127.0.0.1:8001/health" "agent-go"
echo ""

# GPU 状态
echo -e "${CYAN}[GPU 状态]${NC}"
if command -v nvidia-smi &>/dev/null; then
    nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu --format=csv,noheader 2>/dev/null | \
        while IFS=',' read -r name mem_used mem_total util; do
            echo "  GPU: $(echo $name | xargs)"
            echo "  显存: $(echo $mem_used | xargs) / $(echo $mem_total | xargs)"
            echo "  利用率: $(echo $util | xargs)"
        done
else
    echo "  nvidia-smi 不可用"
fi
echo ""

# 内存/磁盘
echo -e "${CYAN}[系统资源]${NC}"
echo "  内存:"
free -h | awk '/Mem:/{printf "    已用 %s / 总计 %s (%.0f%%)\n", $3, $2, $3/$2*100}'
echo "  共享存储 (/root/shared-nvme):"
df -h /root/shared-nvme 2>/dev/null | tail -1 | awk '{printf "    已用 %s / 总计 %s (%s)\n", $3, $2, $5}'
echo "  系统盘:"
df -h / 2>/dev/null | tail -1 | awk '{printf "    已用 %s / 总计 %s (%s)\n", $3, $2, $5}'
echo ""

# 最近日志
echo -e "${CYAN}[最近错误日志(如有)]${NC}"
for log in memory-service doc-service agent-go knovis-user; do
    logfile="$LOG_DIR/$log.log"
    if [ -f "$logfile" ]; then
        errors=$(tail -50 "$logfile" | grep -iE "error|fatal|panic|exception" | tail -3)
        if [ -n "$errors" ]; then
            echo "  ${YELLOW}⚠️ $log:${NC}"
            echo "$errors" | sed 's/^/    /'
        fi
    fi
done
echo ""

# MySQL 数据库状态
echo -e "${CYAN}[数据库]${NC}"
MYSQL_PWD="${DB_PASSWORD:-knovis123}"
for db in agent_go knovis; do
    count=$(mysql -uroot -p"$MYSQL_PWD" -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='$db';" 2>/dev/null || echo "?")
    echo "  $db: $count 张表"
done
echo ""

echo -e "${CYAN}===== 状态检查完毕 =====${NC}"
echo "  实时日志: tmux attach -t knovis"
echo "  日志目录: $LOG_DIR"
echo "  重启服务: bash $SCRIPT_DIR/stop.sh && bash $SCRIPT_DIR/start.sh"

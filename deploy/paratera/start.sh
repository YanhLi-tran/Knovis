#!/bin/bash
# =============================================================================
# Knovis @ 并行智算云容器 - 一键启动脚本
# 用法: bash start.sh
# 说明: 使用 tmux 管理所有服务进程，SSH 断开后服务继续运行
#       服务启动顺序: MySQL → Redis → memory-service → doc-service → Knovis → agent-go
# =============================================================================
set -e

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ===== 路径 =====
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SHARED_DIR="/root/shared-nvme"
DATA_DIR="$SHARED_DIR/data"
MODELS_DIR="$SHARED_DIR/models"
LOG_DIR="$PROJECT_DIR/logs"
BIN_DIR="$PROJECT_DIR/bin"
TMUX_SESSION="knovis"

mkdir -p "$LOG_DIR" "$BIN_DIR"

# ===== 加载环境变量 =====
ENV_FILE="$PROJECT_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    err ".env 文件不存在！请先: cp deploy/paratera/.env.paratera .env 并编辑"
fi
set -a
source "$ENV_FILE"
set +a

# ===== 端口/路径默认值 =====
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-3306}"
DB_USER="${DB_USER:-root}"
DB_PASSWORD="${DB_PASSWORD:-knovis123}"
DB_NAME="${DB_NAME:-agent_go}"
REDIS_HOST="${REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${REDIS_PORT:-6379}"
MEMORY_PORT="${MEMORY_SERVICE_PORT:-8002}"
DOC_PORT="${DOC_SERVICE_PORT:-8003}"
AGENT_PORT="${AGENT_PORT:-8001}"
KNOVIS_PORT="${PORT:-8080}"

cd "$PROJECT_DIR"

# ===== 0. 检查编译产物 =====
if [ ! -f "$BIN_DIR/agent" ]; then
    warn "agent-go 未编译，尝试编译..."
    cd "$PROJECT_DIR/agent-go"
    export GOPROXY=https://goproxy.cn,direct
    go build -ldflags="-s -w" -o "$BIN_DIR/agent" ./cmd/agent
    cd "$PROJECT_DIR"
fi
if [ ! -f "$BIN_DIR/knovis-user-api" ]; then
    warn "knovis-user-api 未编译，尝试编译..."
    cd "$PROJECT_DIR/service/userapi"
    export GOPROXY=https://goproxy.cn,direct
    go build -ldflags="-s -w" -o "$BIN_DIR/knovis-user-api" user.go
    cd "$PROJECT_DIR"
fi

# ===== 1. 启动 MySQL =====
info "[1/6] 启动 MySQL..."
if ! mysqladmin ping -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" --silent 2>/dev/null; then
    service mysql start 2>/dev/null || mysqld_safe --datadir="$DATA_DIR/mysql" &
    sleep 3
    for i in $(seq 1 15); do
        if mysqladmin ping -h"$DB_HOST" -P"$DB_PORT" -u"$DB_USER" -p"$DB_PASSWORD" --silent 2>/dev/null; then
            break
        fi
        sleep 1
    done
fi
info "MySQL 就绪"

# ===== 2. 启动 Redis =====
info "[2/6] 启动 Redis..."
if ! redis-cli -h"$REDIS_HOST" -p"$REDIS_PORT" ping 2>/dev/null | grep -q PONG; then
    redis-server --daemonize yes --bind 0.0.0.0
    sleep 1
fi
info "Redis 就绪"

# ===== 3. 导入数据库表（首次）=====
if ! mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" -e "USE agent_go;" 2>/dev/null; then
    info "创建 agent_go 数据库..."
    mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" -e "CREATE DATABASE IF NOT EXISTS agent_go DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
fi
if ! mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" -e "USE knovis;" 2>/dev/null; then
    info "创建 knovis 数据库..."
    mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" -e "CREATE DATABASE IF NOT EXISTS knovis DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
fi
if [ -f "$PROJECT_DIR/sql/docker-init.sql" ]; then
    TABLE_COUNT=$(mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='agent_go';" 2>/dev/null || echo "0")
    if [ "$TABLE_COUNT" -lt 5 ]; then
        info "导入数据库表结构..."
        mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" < "$PROJECT_DIR/sql/docker-init.sql"
    fi
fi

# 增量迁移（幂等，每次启动都执行）: 旧库补齐新增表/字段（memory_search_metrics + agent_memories 新字段）
if [ -f "$PROJECT_DIR/deploy/paratera/migrate-upgrade.sql" ]; then
    info "执行增量数据库迁移 migrate-upgrade.sql（幂等，可重复执行）..."
    mysql -u"$DB_USER" -p"$DB_PASSWORD" -h"$DB_HOST" < "$PROJECT_DIR/deploy/paratera/migrate-upgrade.sql"
fi

# ===== 4. 停止旧的 tmux session =====
if tmux has-session -t "$TMUX_SESSION" 2>/dev/null; then
    warn "检测到已有 knovis tmux session，正在停止..."
    tmux kill-session -t "$TMUX_SESSION"
    sleep 1
fi

# ===== 5. 创建 tmux session =====
info "[3/6] 创建 tmux session: $TMUX_SESSION"
# 创建 session 并直接命名第一个窗口为 system
tmux new-session -d -s "$TMUX_SESSION" -n "system"
# 依次创建 4 个服务窗口（索引自动递增: 1,2,3,4）
tmux new-window -t "$TMUX_SESSION:" -n "memory-service"
tmux new-window -t "$TMUX_SESSION:" -n "doc-service"
tmux new-window -t "$TMUX_SESSION:" -n "knovis-user"
tmux new-window -t "$TMUX_SESSION:" -n "agent-go"
# 选中 system 窗口
tmux select-window -t "$TMUX_SESSION:system"

# ===== 6. 启动 memory-service (窗口 1)=====
info "[4/6] 启动 memory-service (端口 $MEMORY_PORT)..."
MEM_LOG="$LOG_DIR/memory-service.log"
MEM_CHROMA="$DATA_DIR/chroma-memory"
mkdir -p "$MEM_CHROMA"
tmux send-keys -t "$TMUX_SESSION:memory-service" "cd $PROJECT_DIR/memory-service && \
MEMORY_SERVICE_PORT=$MEMORY_PORT \
MEMORY_SERVICE_HOST=0.0.0.0 \
MEMORY_SERVICE_API_KEY='$MEMORY_SERVICE_API_KEY' \
EMBEDDING_MODEL=${EMBEDDING_MODEL:-$MODELS_DIR/bge-large-zh} \
EMBED_DEVICE=${EMBED_DEVICE:-auto} \
CHROMA_PERSIST_DIR=$MEM_CHROMA \
DB_HOST=$DB_HOST DB_PORT=$DB_PORT DB_USER=$DB_USER DB_PASSWORD=$DB_PASSWORD DB_NAME=$DB_NAME \
BM25_WEIGHT=${BM25_WEIGHT:-0.3} RAG_WEIGHT=${RAG_WEIGHT:-0.7} \
EMBED_BATCH_SIZE=${EMBED_BATCH_SIZE:-64} \
RECALL_TOP_N=${RECALL_TOP_N:-20} FINAL_TOP_K=${FINAL_TOP_K:-5} \
CACHE_ENABLED=${CACHE_ENABLED:-true} CACHE_TTL_RESULT=${CACHE_TTL_RESULT:-60} CACHE_TTL_VECTOR=${CACHE_TTL_VECTOR:-7200} \
REDIS_URL=${REDIS_URL:-redis://127.0.0.1:6379/0} \
METRICS_MYSQL_AGGREGATE=${METRICS_MYSQL_AGGREGATE:-true} \
AB_EXPERIMENT_MODE=${AB_EXPERIMENT_MODE:-false} \
DECAY_GRACE_DAYS=${DECAY_GRACE_DAYS:-30} DECAY_RATE_WEEKLY=${DECAY_RATE_WEEKLY:-3} DECAY_FLOOR=${DECAY_FLOOR:-10} \
TIER_HOT_IMPORTANCE_MIN=${TIER_HOT_IMPORTANCE_MIN:-30} TIER_HOT_ACCESS_DAYS=${TIER_HOT_ACCESS_DAYS:-90} COLD_RECALL_MAX=${COLD_RECALL_MAX:-3} \
MERGE_THRESHOLD=${MERGE_THRESHOLD:-100} MERGE_MAX_CLUSTERS=${MERGE_MAX_CLUSTERS:-3} MERGE_MIN_CLUSTER_SIZE=${MERGE_MIN_CLUSTER_SIZE:-3} \
PRELOAD_MODEL=true \
python3 -u main.py 2>&1 | tee $MEM_LOG" C-m

# 等待 memory-service 就绪
info "等待 memory-service 启动..."
for i in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:$MEMORY_PORT/health" > /dev/null 2>&1; then
        info "memory-service 就绪 (${i}s)"
        break
    fi
    if [ $i -eq 60 ]; then
        warn "memory-service 启动超时，请检查日志: $MEM_LOG"
    fi
    sleep 1
done

# ===== 7. 启动 doc-service (窗口 2)=====
info "[5/6] 启动 doc-service (端口 $DOC_PORT)..."
DOC_LOG="$LOG_DIR/doc-service.log"
DOC_CHROMA="$DATA_DIR/chroma-doc"
mkdir -p "$DOC_CHROMA" "${UPLOAD_DIR:-$DATA_DIR/uploads}"

# 构建 doc-service 环境变量
RERANK_FLAG="${RERANK_ENABLED:-true}"
RERANK_PATH="${RERANK_MODEL_PATH:-$MODELS_DIR/bge-reranker-v2-m3}"
if [ ! -f "$RERANK_PATH/config.json" ]; then
    warn "rerank 模型未找到，自动禁用 rerank"
    RERANK_FLAG="false"
fi

tmux send-keys -t "$TMUX_SESSION:doc-service" "cd $PROJECT_DIR/doc-service && \
DOC_SERVICE_PORT=$DOC_PORT \
DOC_SERVICE_HOST=0.0.0.0 \
DOC_SERVICE_API_KEY='$DOC_SERVICE_API_KEY' \
MEMORY_SERVICE_URL=http://127.0.0.1:$MEMORY_PORT \
CHROMA_PERSIST_DIR=$DOC_CHROMA \
DOC_UPLOAD_DIR=${UPLOAD_DIR:-$DATA_DIR/uploads} \
DB_HOST=$DB_HOST DB_PORT=$DB_PORT DB_USER=$DB_USER DB_PASSWORD=$DB_PASSWORD DB_NAME=$DB_NAME \
RAG_CHUNK_SIZE=${RAG_CHUNK_SIZE:-256} RAG_CHUNK_OVERLAP=${RAG_CHUNK_OVERLAP:-26} \
RAG_BM25_WEIGHT=${RAG_BM25_WEIGHT:-0.3} RAG_RAG_WEIGHT=${RAG_RAG_WEIGHT:-0.7} \
RAG_RECALL_TOP_N=${RAG_RECALL_TOP_N:-20} RAG_FINAL_TOP_K=${RAG_FINAL_TOP_K:-5} \
RAG_SECTION_MAX_LEN=${RAG_SECTION_MAX_LEN:-2000} \
RERANK_ENABLED=$RERANK_FLAG \
RERANK_MODEL_PATH=$RERANK_PATH \
python3 -u main.py 2>&1 | tee $DOC_LOG" C-m

info "等待 doc-service 启动..."
for i in $(seq 1 40); do
    if curl -sf "http://127.0.0.1:$DOC_PORT/health" > /dev/null 2>&1; then
        info "doc-service 就绪 (${i}s)"
        break
    fi
    if [ $i -eq 40 ]; then
        warn "doc-service 启动超时，请检查日志: $DOC_LOG"
    fi
    sleep 1
done

# ===== 8. 启动 Knovis 用户服务 (窗口 3)=====
info "启动 Knovis 用户服务 (端口 $KNOVIS_PORT)..."
KNOVIS_LOG="$LOG_DIR/knovis-user.log"
KNOVIS_UPLOAD="${UPLOAD_DIR:-$DATA_DIR/uploads}"
mkdir -p "$KNOVIS_UPLOAD"

tmux send-keys -t "$TMUX_SESSION:knovis-user" "cd $PROJECT_DIR/service/userapi && \
HOST=0.0.0.0 PORT=$KNOVIS_PORT \
DB_HOST=$DB_HOST DB_PORT=$DB_PORT DB_USER=$DB_USER DB_PASSWORD=$DB_PASSWORD DB_NAME=knovis \
REDIS_HOST=$REDIS_HOST REDIS_PORT=$REDIS_PORT REDIS_PASSWORD=${REDIS_PASSWORD:-} \
JWT_SECRET='$JWT_SECRET' JWT_EXPIRE=${JWT_EXPIRE:-86400} \
JWT_ISSUER=${JWT_ISSUER:-Knovis} JWT_AUDIENCE=${JWT_AUDIENCE:-agent-go} \
SMTP_HOST=${SMTP_HOST:-} SMTP_PORT=${SMTP_PORT:-} SMTP_USER=${SMTP_USER:-} SMTP_PASSWORD=${SMTP_PASSWORD:-} \
UPLOAD_DIR=$KNOVIS_UPLOAD \
$BIN_DIR/knovis-user-api -f etc/user-api.yaml 2>&1 | tee $KNOVIS_LOG" C-m

sleep 2
info "Knvos 用户服务启动中..."

# ===== 9. 启动 agent-go (窗口 4)=====
info "[6/6] 启动 agent-go (端口 $AGENT_PORT)..."
AGENT_LOG="$LOG_DIR/agent-go.log"

tmux send-keys -t "$TMUX_SESSION:agent-go" "cd $PROJECT_DIR/agent-go && \
AGENT_PORT=$AGENT_PORT AGENT_ENV=production \
DB_HOST=$DB_HOST DB_PORT=$DB_PORT DB_USER=$DB_USER DB_PASSWORD=$DB_PASSWORD DB_NAME=$DB_NAME \
REDIS_HOST=$REDIS_HOST REDIS_PORT=$REDIS_PORT REDIS_PASSWORD=${REDIS_PASSWORD:-} REDIS_DB=${REDIS_DB:-0} \
MEMORY_SERVICE_URL=http://127.0.0.1:$MEMORY_PORT \
MEMORY_SERVICE_API_KEY='$MEMORY_SERVICE_API_KEY' \
DOC_SERVICE_URL=http://127.0.0.1:$DOC_PORT \
DOC_SERVICE_API_KEY='$DOC_SERVICE_API_KEY' \
KNOVIS_API_BASE_URL=http://127.0.0.1:$KNOVIS_PORT \
LLM_API_KEY='$LLM_API_KEY' \
LLM_BASE_URL=${LLM_BASE_URL:-https://api.deepseek.com} \
LLM_MODEL=${LLM_MODEL:-deepseek-chat} \
FREE_QUOTA_PER_DAY=${FREE_QUOTA_PER_DAY:-20} \
AUTH_MODE=${AUTH_MODE:-dev} \
JWT_SECRET='$JWT_SECRET' \
JWT_ISSUER=${JWT_ISSUER:-Knovis} JWT_AUDIENCE=${JWT_AUDIENCE:-agent-go} \
MASTER_KEY_V1='$MASTER_KEY_V1' \
$BIN_DIR/agent -f etc/agent-api.yaml 2>&1 | tee $AGENT_LOG" C-m

# ===== 10. 系统监控窗口 =====
tmux send-keys -t "$TMUX_SESSION:system" "echo '===== Knovis 服务控制台 ====='; echo ''; echo '切换窗口: tmux select-window -t knovis:<窗口名>'; echo '窗口列表: system / memory-service / doc-service / knovis-user / agent-go'; echo '查看所有窗口: tmux list-windows -t knovis'; echo '退出tmux(不停止服务): Ctrl+B 然后按 D'; echo ''; watch -n 5 'echo \"[$(date +%H:%M:%S)] 服务健康检查:\"; curl -sf http://127.0.0.1:$MEMORY_PORT/health 2>/dev/null && echo \"  memory-service: OK\" || echo \"  memory-service: FAIL\"; curl -sf http://127.0.0.1:$DOC_PORT/health 2>/dev/null && echo \"  doc-service: OK\" || echo \"  doc-service: FAIL\"; curl -sf http://127.0.0.1:$AGENT_PORT/health 2>/dev/null && echo \"  agent-go: OK\" || echo \"  agent-go: FAIL\"; echo \"\"; echo \"GPU 状态:\"; nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu --format=csv,noheader 2>/dev/null || echo \"  nvidia-smi 不可用\"; echo \"\"; echo \"内存使用:\"; free -h | head -2'" C-m

# ===== 11. 等待所有服务就绪 =====
info "等待所有服务就绪..."
sleep 5

ALL_OK=true
echo ""
echo -e "${GREEN}===== 健康检查 =====${NC}"
for svc in "memory-service:$MEMORY_PORT" "doc-service:$DOC_PORT" "agent-go:$AGENT_PORT"; do
    name="${svc%%:*}"
    port="${svc##*:}"
    if curl -sf "http://127.0.0.1:$port/health" > /dev/null 2>&1; then
        echo -e "  ${GREEN}✅${NC} $name (端口 $port)"
    else
        echo -e "  ${RED}❌${NC} $name (端口 $port) - 启动中或失败，请检查日志"
        ALL_OK=false
    fi
done

# Knovis 没有 /health，检查端口
if curl -sf "http://127.0.0.1:$KNOVIS_PORT/user?page=1&page_size=1" > /dev/null 2>&1; then
    echo -e "  ${GREEN}✅${NC} knovis-user-api (端口 $KNOVIS_PORT)"
else
    echo -e "  ${YELLOW}⏳${NC} knovis-user-api (端口 $KNOVIS_PORT) - 可能需要认证，服务已启动"
fi

echo ""
if [ "$ALL_OK" = true ]; then
    info "===== 🎉 所有核心服务启动成功！====="
else
    warn "部分服务尚未就绪，可能仍在启动中，请稍等后运行 bash deploy/paratera/status.sh 检查"
fi

echo ""
echo -e "${GREEN}===== 访问信息 =====${NC}"
echo "  agent-go (主编排+对话):  http://<平台分配地址>:$AGENT_PORT"
echo "  Knovis 用户服务:         http://<平台分配地址>:$KNOVIS_PORT"
echo "  memory-service:          http://127.0.0.1:$MEMORY_PORT (内部)"
echo "  doc-service:             http://127.0.0.1:$DOC_PORT (内部)"
echo ""
echo -e "${GREEN}===== 常用命令 =====${NC}"
echo "  查看所有服务日志(实时):  tmux attach -t $TMUX_SESSION"
echo "  切换服务窗口:            tmux select-window -t $TMUX_SESSION:<窗口名>"
echo "  窗口名:                  system / memory-service / doc-service / knovis-user / agent-go"
echo "  退出 tmux(不停止服务):   按 Ctrl+B 然后按 D"
echo "  查看服务状态:            bash deploy/paratera/status.sh"
echo "  停止所有服务:            bash deploy/paratera/stop.sh"
echo "  日志目录:                $LOG_DIR"

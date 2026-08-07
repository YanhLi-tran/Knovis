#!/bin/bash
# =============================================================================
# Knovis @ 并行智算云容器 - 首次初始化脚本
# 用法: bash 01-init.sh
# 执行时机: 容器创建后 SSH 进去，首次执行一次即可（关机再开机不需要重新跑）
# 前提: 容器为 PyTorch-25.03-py3 (PyTorch 2.7.0 + Ubuntu 24.04 + Python + CUDA)
# =============================================================================
set -e

# ===== 颜色 =====
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ===== 路径常量 =====
SHARED_DIR="/root/shared-nvme"
PROJECT_DIR="$SHARED_DIR/knovis"
MODELS_DIR="$SHARED_DIR/models"
DATA_DIR="$SHARED_DIR/data"
GO_VERSION="1.22.10"

# ===== 0. 前置检查 =====
info "===== Knovis 容器初始化开始 ====="
if [ ! -d "$SHARED_DIR" ]; then
    err "共享存储目录 $SHARED_DIR 不存在，请确认容器创建时已挂载共享存储"
fi
if [ "$(id -u)" -ne 0 ]; then
    warn "建议以 root 用户执行（容器默认 root），否则 apt 安装可能失败"
fi

# ===== 1. 系统依赖 =====
info "[1/7] 安装系统依赖（MySQL/Redis/Go 编译工具/Git）..."
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    mysql-server redis-server git wget curl tmux build-essential pkg-config \
    > /dev/null
info "系统依赖安装完成"

# ===== 2. 安装 Go =====
info "[2/7] 安装 Go $GO_VERSION ..."
if ! command -v go &>/dev/null || ! go version | grep -q "$GO_VERSION"; then
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -O /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
    export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
fi
go version
info "Go 安装完成"

# ===== 3. 创建目录结构 =====
info "[3/7] 创建共享存储目录结构..."
mkdir -p "$PROJECT_DIR"
mkdir -p "$MODELS_DIR/bge-large-zh"
mkdir -p "$MODELS_DIR/bge-reranker-v2-m3"
mkdir -p "$DATA_DIR/mysql"
mkdir -p "$DATA_DIR/redis"
mkdir -p "$DATA_DIR/chroma-doc"
mkdir -p "$DATA_DIR/chroma-memory"
mkdir -p "$DATA_DIR/uploads"
mkdir -p "$PROJECT_DIR/logs"
info "目录结构创建完成"

# ===== 4. MySQL 初始化 =====
info "[4/7] 配置 MySQL 数据目录到共享存储 + 初始化数据库..."
# 把 MySQL 数据目录迁移到共享存储（30GB 系统盘不够）
if [ ! -L "/var/lib/mysql" ] || [ ! -d "$DATA_DIR/mysql/mysql" ]; then
    service mysql stop 2>/dev/null || true
    # 如果目标目录为空才迁移
    if [ ! -d "$DATA_DIR/mysql/mysql" ]; then
        cp -a /var/lib/mysql/* "$DATA_DIR/mysql/" 2>/dev/null || true
    fi
    rm -rf /var/lib/mysql
    ln -sf "$DATA_DIR/mysql" /var/lib/mysql
    chown -R mysql:mysql "$DATA_DIR/mysql"
fi

# 修改 MySQL 绑定地址允许 0.0.0.0
MYSQL_CNF="/etc/mysql/mysql.conf.d/bind-address.cnf"
cat > "$MYSQL_CNF" <<'EOF'
[mysqld]
bind-address = 0.0.0.0
default-authentication-plugin = mysql_native_password
character-set-server = utf8mb4
collation-server = utf8mb4_unicode_ci
EOF

service mysql start
sleep 2

# 设置 root 密码并创建数据库
MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-knovis123}"
mysql -u root -e "ALTER USER 'root'@'localhost' IDENTIFIED WITH mysql_native_password BY '${MYSQL_PWD}'; FLUSH PRIVILEGES;" 2>/dev/null || true

# 创建数据库（如果 docker-init.sql 还没导入的话）
if ! mysql -u root -p"$MYSQL_PWD" -e "USE agent_go;" 2>/dev/null; then
    info "初始化 agent_go + knovis 数据库..."
    if [ -f "$PROJECT_DIR/sql/docker-init.sql" ]; then
        mysql -u root -p"$MYSQL_PWD" < "$PROJECT_DIR/sql/docker-init.sql"
    else
        mysql -u root -p"$MYSQL_PWD" -e "CREATE DATABASE IF NOT EXISTS agent_go DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
        mysql -u root -p"$MYSQL_PWD" -e "CREATE DATABASE IF NOT EXISTS knovis DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
        warn "项目代码尚未复制到 $PROJECT_DIR，数据库表结构将在代码复制后通过 docker-init.sql 导入"
    fi
fi
info "MySQL 配置完成"

# ===== 5. Redis 配置 =====
info "[5/7] 配置 Redis..."
# Redis 数据目录改到共享存储
REDIS_CONF="/etc/redis/redis.conf"
if [ -f "$REDIS_CONF" ]; then
    sed -i "s|^dir .*|dir $DATA_DIR/redis|" "$REDIS_CONF"
    sed -i "s|^# bind 127.0.0.1|bind 0.0.0.0|" "$REDIS_CONF"
    sed -i "s|^bind 127.0.0.1|bind 0.0.0.0|" "$REDIS_CONF"
    sed -i "s|^ supervised .*|supervised no|" "$REDIS_CONF"
fi
mkdir -p "$DATA_DIR/redis"
chown redis:redis "$DATA_DIR/redis"
service redis-server start 2>/dev/null || redis-server --daemonize yes --dir "$DATA_DIR/redis"
info "Redis 配置完成"

# ===== 6. Python 依赖 =====
info "[6/7] 安装 Python 依赖..."
if [ -f "$PROJECT_DIR/memory-service/requirements.txt" ]; then
    pip install -q -r "$PROJECT_DIR/memory-service/requirements.txt" \
        -i https://pypi.tuna.tsinghua.edu.cn/simple
    pip install -q -r "$PROJECT_DIR/doc-service/requirements.txt" \
        -i https://pypi.tuna.tsinghua.edu.cn/simple
else
    warn "项目代码未找到，请先将代码放到 $PROJECT_DIR 后重新执行此步骤"
fi
info "Python 依赖安装完成"

# ===== 7. Go 依赖 =====
info "[7/7] 编译 Go 服务..."
if [ -f "$PROJECT_DIR/agent-go/go.mod" ]; then
    cd "$PROJECT_DIR/agent-go"
    export GOPROXY=https://goproxy.cn,direct
    go build -ldflags="-s -w" -o "$PROJECT_DIR/bin/agent" ./cmd/agent
    info "agent-go 编译完成 → $PROJECT_DIR/bin/agent"
fi

if [ -f "$PROJECT_DIR/service/userapi/go.mod" ]; then
    cd "$PROJECT_DIR/service/userapi"
    export GOPROXY=https://goproxy.cn,direct
    go build -ldflags="-s -w" -o "$PROJECT_DIR/bin/knovis-user-api" user.go
    info "knovis-user-api 编译完成 → $PROJECT_DIR/bin/knovis-user-api"
fi

# local-agent（可选，服务端不需要）
if [ -f "$PROJECT_DIR/local-agent/go.mod" ]; then
    cd "$PROJECT_DIR/local-agent"
    export GOPROXY=https://goproxy.cn,direct
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$PROJECT_DIR/dist/local-agent-linux-amd64" .
    info "local-agent (linux) 编译完成 → $PROJECT_DIR/dist/local-agent-linux-amd64"
fi

cd "$PROJECT_DIR"
info "===== 初始化完成！====="
echo ""
echo -e "${GREEN}下一步:${NC}"
echo "  1. 把项目代码放到 $PROJECT_DIR（git clone 或 scp 上传）"
echo "  2. bash deploy/paratera/02-setup-models.sh  （下载 embedding/rerank 模型，约 3.3GB）"
echo "  3. cp deploy/paratera/.env.paratera $PROJECT_DIR/.env  （复制环境变量并编辑）"
echo "  4. bash deploy/paratera/start.sh  （一键启动所有服务）"
echo ""
echo -e "  查看服务状态: bash deploy/paratera/status.sh"
echo -e "  停止服务:     bash deploy/paratera/stop.sh"

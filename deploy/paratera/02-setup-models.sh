#!/bin/bash
# =============================================================================
# Knovis @ 并行智算云容器 - 模型下载脚本
# 用法: bash 02-setup-models.sh
# 说明: 从 HuggingFace 下载 bge-large-zh(embedding) 和 bge-reranker-v2-m3(rerank)
#       模型放到共享存储 /root/shared-nvme/models/，关机不丢
#       如果 HuggingFace 下载慢，可设置 HF_ENDPOINT=https://hf-mirror.com 使用镜像
# =============================================================================
set -e

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }

MODELS_DIR="/root/shared-nvme/models"
HF_MIRROR="${HF_ENDPOINT:-}"

# 使用镜像加速（国内网络友好）
if [ -z "$HF_MIRROR" ]; then
    echo "是否使用 HuggingFace 镜像加速(hf-mirror.com)？[Y/n] (默认 Y)"
    read -r use_mirror
    if [ "$use_mirror" != "n" ] && [ "$use_mirror" != "N" ]; then
        export HF_ENDPOINT=https://hf-mirror.com
        info "已启用 hf-mirror.com 镜像加速"
    fi
fi

mkdir -p "$MODELS_DIR"

# ===== 1. bge-large-zh (embedding 模型，必装，约 1.3GB) =====
BGE_DIR="$MODELS_DIR/bge-large-zh"
if [ -f "$BGE_DIR/config.json" ]; then
    info "bge-large-zh 已存在，跳过下载"
else
    info "下载 bge-large-zh (约 1.3GB)..."
    pip install -q huggingface_hub -i https://pypi.tuna.tsinghua.edu.cn/simple
    python3 -c "
from huggingface_hub import snapshot_download
snapshot_download('BAAI/bge-large-zh', local_dir='$BGE_DIR', local_dir_use_symlinks=False)
print('bge-large-zh 下载完成')
"
fi

# ===== 2. bge-reranker-v2-m3 (rerank 模型，推荐装，约 2GB) =====
RERANK_DIR="$MODELS_DIR/bge-reranker-v2-m3"
if [ -f "$RERANK_DIR/config.json" ]; then
    info "bge-reranker-v2-m3 已存在，跳过下载"
else
    echo ""
    echo "是否下载 bge-reranker-v2-m3 (rerank 模型，约 2GB，推荐安装以提升检索质量)？[Y/n] (默认 Y)"
    read -r download_rerank
    if [ "$download_rerank" != "n" ] && [ "$download_rerank" != "N" ]; then
        info "下载 bge-reranker-v2-m3 (约 2GB)..."
        python3 -c "
from huggingface_hub import snapshot_download
snapshot_download('BAAI/bge-reranker-v2-m3', local_dir='$RERANK_DIR', local_dir_use_symlinks=False)
print('bge-reranker-v2-m3 下载完成')
"
    else
        warn "跳过 rerank 模型下载，RERANK_ENABLED 将设为 false"
    fi
fi

echo ""
info "===== 模型准备完成 ====="
echo "  模型目录: $MODELS_DIR"
ls -lh "$MODELS_DIR"
echo ""
info "下一步: 配置 .env 并执行 start.sh 启动服务"

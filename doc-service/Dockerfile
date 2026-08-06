# doc-service（Python 文档 RAG 子服务，端口 8003）
# 提供 PDF 解析 + 分块 + 混合检索（BM25 + 向量 + 段落召回 + rerank）
# 构建：docker build -t knovis-doc-service ./doc-service

FROM python:3.11-slim

# 清华 PyPI 源 + 时区 + 构建 pymupdf 所需的系统库
RUN pip config set global.index-url https://pypi.tuna.tsinghua.edu.cn/simple && \
    apt-get update && apt-get install -y --no-install-recommends \
        tzdata curl && \
    ln -snf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 先装依赖（利用 Docker 缓存层）
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# 复制源码
COPY . .

# 上传目录 + Chroma 数据目录（由 docker-compose 挂载 volume）
RUN mkdir -p /app/data/uploads /app/data/chroma

EXPOSE 8003

# 健康检查
HEALTHCHECK --interval=15s --timeout=5s --start-period=60s --retries=3 \
    CMD curl -sf http://localhost:8003/health || exit 1

CMD ["python", "-u", "main.py"]

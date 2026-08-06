# memory-service（Python 记忆子服务，端口 8002）
# 提供 embedding（bge-large-zh）+ 混合检索（BM25 + Chroma 向量）
# 构建：docker build -t knovis-memory-service ./memory-service

FROM python:3.11-slim

# 清华 PyPI 源 + 时区
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

# 模型目录 + Chroma 数据目录（由 docker-compose 挂载 volume）
RUN mkdir -p /app/models /app/data/chroma

EXPOSE 8002

# 健康检查
HEALTHCHECK --interval=15s --timeout=5s --start-period=60s --retries=3 \
    CMD curl -sf http://localhost:8002/health || exit 1

CMD ["python", "-u", "main.py"]

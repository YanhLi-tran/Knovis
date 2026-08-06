# agent-go 多阶段构建（Go 主服务，端口 8001）
# 构建：docker build -t knovis-agent-go ./agent-go
# 前置：宿主机需有 etc/agent-api.yaml、configs/、static/ 目录（COPY 进镜像）

# ===== Stage 1: 构建 =====
FROM golang:1.25-alpine AS builder

# 国内代理加速
ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0
ENV GOOS=linux

WORKDIR /build

# 先复制依赖文件，利用 Docker 缓存层
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 编译（静态二进制，无 CGO 依赖）
RUN go build -ldflags="-s -w" -o /out/agent ./cmd/agent

# ===== Stage 2: 运行 =====
FROM alpine:3.20

# 时区 + ca-certificates（HTTPS 调用 LLM/Knovis API 需要）
RUN apk add --no-cache tzdata ca-certificates && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 复制二进制
COPY --from=builder /out/agent /app/agent

# 复制配置文件 + 静态资源
COPY etc/ /app/etc/
COPY configs/ /app/configs/
COPY static/ /app/static/

# .env 不打入镜像（含 LLM_API_KEY/JWT_SECRET/MASTER_KEY_V1 等敏感信息）
# 由 docker-compose.yml 的 volume 挂载 ./agent-go/.env:/app/.env:ro 注入
# 或通过 environment 字段注入，避免镜像泄露敏感信息

EXPOSE 8001

# 健康检查（/health 接口）
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD wget -qO- http://localhost:8001/health || exit 1

ENTRYPOINT ["/app/agent"]
CMD ["-f", "etc/agent-api.yaml"]

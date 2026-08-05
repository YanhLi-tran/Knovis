# local-agent

中央 Agent 服务（agent-go）的用户本地执行器。

通过 WebSocket 长连接接收服务端下发的指令，在用户本机执行：

| 指令 | 说明 | 审批 |
|---|---|---|
| `file_read` | 读文件（默认前 10000 字符） | 免审批 |
| `file_list` | 列目录（文件名 + 大小 + 修改时间） | 免审批 |
| `grep` | 正则搜索文件内容 | 免审批 |
| `file_write` | 写文件（覆盖/追加） | 需审批 |
| `sandbox_exec` | 执行白名单 CLI 命令 | 需审批 |

## 安全约束

- **白名单**：sandbox_exec 仅允许 `ls/cat/grep/find/git/go/node/python/curl/docker/kubectl` 等 40+ 命令，白名单外直接拒绝
- **超时**：所有命令 30s 硬上限（exec.CommandContext 强制 kill）
- **环境变量净化**：移除 key/secret/token/password 等敏感变量
- **输出截断**：单次输出 ≤ 4000 字符
- **多用户隔离**：服务端按 userID 路由指令，A 用户无法触发 B 用户的本地 agent
- **审批流**：写操作（file_write）和所有 CLI 执行（sandbox_exec）必须用户在 Web UI 点「批准」后才执行

## 快速开始

### 方式一：直接编译运行

```bash
cd local-agent
go build -o local-agent .

# 拿到 JWT access token（从中央服务 /auth/login 接口获取）
./local-agent -server ws://127.0.0.1:8001 -token YOUR_JWT_TOKEN
```

Windows：

```powershell
cd local-agent
go build -o local-agent.exe .
.\local-agent.exe -server ws://127.0.0.1:8001 -token YOUR_JWT_TOKEN
```

### 方式二：跨平台编译脚本

仓库根目录提供编译脚本，一次性产出 4 个平台的二进制：

```bash
# Linux/macOS
./scripts/build-local-agent.sh

# Windows
powershell -ExecutionPolicy Bypass -File .\scripts\build-local-agent.ps1
```

产物在 `dist/`：

```
dist/local-agent-windows-amd64.exe
dist/local-agent-linux-amd64
dist/local-agent-darwin-amd64       # Intel Mac
dist/local-agent-darwin-arm64       # Apple Silicon
```

### 方式三：一键启动脚本

编译后用启动脚本省去每次输命令：

```bash
# Linux/macOS
./scripts/start-local-agent.sh                                # 交互式输入 token
./scripts/start-local-agent.sh --token YOUR_JWT_TOKEN         # 直接传

# Windows
.\scripts\start-local-agent.bat
.\scripts\start-local-agent.bat --token YOUR_JWT_TOKEN
```

## 命令行参数

```
-server string
      中央服务器 WebSocket 地址（默认 ws://127.0.0.1:8001）
-token string
      JWT access token（也可用 AGENT_TOKEN 环境变量）
-version
      打印版本号并退出
```

## 环境变量

| 变量 | 说明 |
|---|---|
| `AGENT_TOKEN` | JWT access token，与 `-token` 等价（命令行参数优先） |

## 获取 JWT Token

1. 在中央服务注册账号：
   ```bash
   curl -X POST http://127.0.0.1:8001/auth/register \
     -H "Content-Type: application/json" \
     -d '{"username":"alice","password":"your_password"}'
   ```
2. 登录拿 access token：
   ```bash
   curl -X POST http://127.0.0.1:8001/auth/login \
     -H "Content-Type: application/json" \
     -d '{"username":"alice","password":"your_password"}'
   # 返回 {"access_token":"...","refresh_token":"..."}
   ```
3. 用 `access_token` 启动 local-agent

## 行为说明

- **断线重连**：WS 断开后指数退避重连（1s → 2s → 4s → ... → 最大 30s）
- **心跳**：25s 一次文本心跳，服务端用 ping/pong 探活
- **并发**：服务端可并发下发多条指令，客户端按自身能力并行执行
- **日志**：所有事件打 stdout，含 `[INFO]/[WARN]/[ERROR]` 前缀，便于排查

## 故障排查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| `[FATAL] 缺少 token` | 未传 -token 也未设 AGENT_TOKEN | 用 `-token` 或 `export AGENT_TOKEN=...` |
| `[ERROR] 连接失败 status=401` | token 无效/过期 | 重新登录拿新 access token |
| `[ERROR] 连接失败 status=404` | 服务器地址或路径错 | 确认 server URL 是 `ws://host:8001` |
| 频繁断线重连 | 网络抖动或服务端重启 | 看 agent-go 服务端日志 |
| 服务端日志 `客户端报错` | 本地执行命令失败 | 看 local-agent stdout 的 `[ERROR]` 行 |

## 开发

```bash
# 单元测试
cd local-agent
go test ./...

# 跨平台编译验证
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/test-linux .
```

源码：
- `main.go` — 入口、WS 连接、心跳、断线重连
- `handler.go` — 命令分发与执行（file_read/file_write/grep/file_list/sandbox_exec）
- `message.go` — WS 消息协议（与服务端 `agent-go/internal/ws/message.go` 一一对应）
- `handler_test.go` — sandbox 白名单 / 超时 / 输出截断 / 环境净化单测

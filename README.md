# Knovis - 企业级智能知识助手

> Agent 系统（agent-go）+ RAG 文档检索（doc-service）+ 记忆服务（memory-service）+ 用户/动态数据服务（userapi）+ 本地执行器（local-agent）

## 项目概览

**Knovis** 是一个企业级智能知识助手：LLM 自主决策调用工具完成复杂任务，支持多轮 OTACO 编排、SSE 流式对话、引用溯源、人工审批与沙箱执行。

```
┌──────────────────────────────────────────────────────────┐
│  前端 (agent-go/static/index.html, SSE 流式 + 审批/提问)  │
├──────────────────────────────────────────────────────────┤
│  agent-go (:8001)  Go 主服务                              │
│  · OTACO 编排循环 (Observation-Think-Act-Check-Output)    │
│  · 三层工具架构: 13 FC 常驻 + Skill 按需加载 + MCP 预留   │
│  · SSO 只校验 JWT + AES-256-GCM 凭证加密 + 审批/限流/审计 │
│  · LLM 指标: KV 缓存命中率 / TTFT / token 用量            │
├────────────┬────────────┬─────────────┬──────────────────┤
│ doc-service │ memory-    │ userapi     │ local-agent      │
│ (:8003)     │ service    │ (:8080)     │ (用户本机)       │
│ RAG 检索    │ (:8002)    │ 用户/动态   │ file/sandbox     │
│ BM25+向量+  │ embedding+ │ JWT 签发    │ 工具执行         │
│ rerank+     │ 记忆检索+  │ (SSO 上游)  │ (WS 白名单沙箱)  │
│ 段落召回    │ 生命周期   │             │                  │
├────────────┴────────────┴─────────────┴──────────────────┤
│  MySQL (13+2 表) · Redis (缓存/黑名单/限流) · Chroma      │
└──────────────────────────────────────────────────────────┘
```

### 核心特性

- **OTACO 编排**：`[OBSERVE:pass/retry/rollback]` 标记协议 + `[FINAL_ANSWER]` 输出；上下文按需压缩（≥80% LLM 自主 / ≥90% Go 强制）；rollback 用消息长度栈 O(1) 截断
- **三层工具架构**：13 个 FC 常驻工具（schema 实测 1705 tokens）+ Skill 按需加载（元信息仅 45-59 tokens，SKILL.md 文件驱动 + 用户自定义多租户）+ MCP 预留
- **RAG 检索**：pymupdf4llm 解析 + 256/26 标题层级分块 + BM25(jieba 内存索引)+numpy 暴力 cosine 双路召回 + top-5 RRF 精排/加权融合 + 段落级上下文召回（索引=chunk、返回=最小标题小节）+ 引用溯源；15 份 A 股年报全量入库，评测 Recall@20=94.9%
- **记忆系统**：混合提取（每轮 jieba+TF-IDF + 每 5 轮 LLM 深度）→ cosine 0.92 去重 → 衰减/冷热分层/唤起/合并生命周期 → 14 周 TTL 归档（30 天可恢复）；@跨项目引用
- **KV 缓存优化**（2026-08-18/19）：system prompt 完全静态（时间/记忆块/上下文状态均移入 user 消息末尾，落库与发 LLM 一致保证跨请求前缀连续），实测缓存命中率 40% → **98.3%**（同请求重复）/ **93.8%**（同会话跨请求）
- **LLM 指标面板**：`GET /llm/metrics` + 前端 📊 面板（缓存命中率/TTFT P50/P95/token 用量/错误明细，进程内最近 200 次）
- **安全**：SSO JWT 只校验 + AES-256-GCM 多版本密钥轮换 + 沙箱三模式（ask/auto/yolo，yolo 危险操作前备份留痕）+ 审批流 + 限流 + 审计 + 全链路 trace_id（X-Trace-Id 贯穿 4 服务）
- **评测体系**：139 条评测集（RAG 64 + Agent 35 + 记忆 40）+ LLM-as-Judge

### 仓库结构

```
Knovis/
├── agent-go/            # Go 主服务（OTACO 编排 + 工具 + API + 前端）
│   ├── cmd/agent/       # 入口
│   ├── internal/        # orchestrator/llm/tools/memory/storage/auth/ws/api...
│   ├── static/          # 前端单文件应用（SSE + 审批 + 指标面板）
│   └── skills/          # 内置 Skill（knovis / kb_summary，SKILL.md 驱动）
├── doc-service/         # Python RAG 文档服务（FastAPI）
├── memory-service/      # Python 记忆服务（bge-large-zh + Chroma + BM25）
├── service/userapi/     # go-zero 用户/动态服务（SSO 上游，签发 JWT）
├── local-agent/         # 用户本地执行器（WS 客户端，白名单沙箱）
├── deploy/paratera/     # 智算云容器部署脚本（tmux 管理 + 幂等迁移）
├── scripts/             # 评测脚本 / local-agent 构建发布
├── sql/                 # 建表 SQL（docker-init.sql / schema.sql）
└── docker-compose.yml   # 一键部署（6 服务）
```

---

## 一键启动（Docker Compose）

整套系统通过 `docker-compose.yml` 一键拉起 6 个服务（MySQL / Redis / Chroma / memory-service / doc-service / agent-go）。

### 前置准备

```bash
# 1. 复制环境变量配置
cp .env.docker.example .env

# 2. 编辑 .env，按需修改（必改项）：
#    - MYSQL_ROOT_PASSWORD   MySQL 密码
#    - LLM_API_KEY           DeepSeek API Key（官方平台 https://platform.deepseek.com 申请）
#    - JWT_SECRET            JWT 密钥（与 Knovis 服务一致）
#    - MASTER_KEY_V1         AES-256-GCM 主密钥
#    - EMBEDDING_MODEL_PATH  bge-large-zh 模型路径
#    - RERANK_ENABLED        是否启用 rerank（true/false）
#    - RERANK_MODEL_HOST_PATH  rerank 模型路径（RERANK_ENABLED=true 时）

# 3. （可选）若有已有 Chroma 向量数据，复制到对应目录避免重新 embedding：
#    cp -r /path/to/chroma-doc  ./data/chroma-doc     # doc-service 的全量年报向量数据(256/26 分块)
#    cp -r /path/to/chroma-mem  ./data/chroma-memory   # memory-service 的记忆向量
```

### 启动

```bash
# 一键启动全部 6 个服务（首次会自动构建镜像 + 初始化数据库）
docker-compose up -d

# 查看服务状态（等待全部 healthy）
docker-compose ps

# 查看 agent-go 日志
docker-compose logs -f agent-go
```

### 验证

```bash
# 健康检查（三个核心服务）
curl http://localhost:8001/health   # agent-go
curl http://localhost:8002/health   # memory-service
curl http://localhost:8003/health   # doc-service

# RAG 检索测试
curl -X POST http://localhost:8003/rag/search \
  -H "Content-Type: application/json" \
  -d '{"query": "五粮液 2021 营业收入", "top_k": 5}'

# LLM 指标（KV 缓存命中率 / TTFT / token 用量，需 JWT；前端顶栏 📊 面板同源）
curl http://localhost:8001/llm/metrics -H "Authorization: Bearer <token>"
```

### 服务端口

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| agent-go | 8001 | Go 主服务（OTACO 编排 + FC 工具 + SSE 流式对话 + LLM 指标：KV 缓存命中率/TTFT） |
| memory-service | 8002 | Python 记忆服务（embedding + 混合检索 + 记忆生命周期） |
| doc-service | 8003 | Python RAG 文档服务（BM25 + 向量 + rerank + 段落召回） |
| MySQL | 3306 | 数据库（agent_go + knovis 两个库） |
| Redis | 6379 | 缓存 + token 黑名单 + 限流计数 + 分布式锁 |
| Chroma | 8000 | 向量库服务（备用，当前嵌入式模式） |

### 注意事项

- **Knovis 用户服务（8080）** 不入 Docker，需单独启动：`cd service/userapi && go run user.go`
- **local-agent 客户端** 不入 Docker（需访问宿主机文件系统执行 file/sandbox 工具）
- **模型文件** 通过 volume 挂载，不打入镜像（避免镜像过大）
- **年报数据** 挂载到 doc-service 的 `/app/data/uploads`，不自动导入

### 停止与清理

```bash
# 停止全部服务
docker-compose down

# 停止并删除数据卷（⚠️ 清空数据库和向量数据）
docker-compose down -v
```

---

## 智算云 / 裸机部署（deploy/paratera）

针对 GPU 容器（PyTorch + CUDA）的另一套部署路径，与 docker-compose 按环境二选一：

```bash
cd deploy/paratera
cp .env.paratera .env   # 修改 5 个必改值（API keys/JWT_SECRET/MASTER_KEY，脚本顶部有生成命令）
bash 01-init.sh         # 首次初始化（Go 环境 + 共享存储目录）
bash 02-setup-models.sh # 下载 bge-large-zh + rerank 模型（支持 hf-mirror 加速）
bash start.sh           # tmux 管理全部服务进程（SSH 断开继续运行）
```

- `migrate-upgrade.sql` 幂等增量迁移（每次启动自动执行）：旧库补齐 `memory_search_metrics` 表 + `agent_memories` 生命周期 5 字段/2 索引
- ⚠️ **代码更新后必须重新编译**：`start.sh` 只在二进制不存在时才编译，更新代码后先 `rm bin/agent` 再执行 `start.sh`（否则新路由/新功能不生效）
- 详细说明见 `deploy/paratera/README.md`

---

## local-agent（用户本地执行器）

文件读写 / grep / 沙箱执行等工具需要用户**本地机器**运行 local-agent 客户端（服务器只下发指令，实际读写发生在用户本机，受 `--workdir` 工作区沙箱约束）。

### 获取（三选一）

| 方式 | 说明 |
| --- | --- |
| **GitHub Releases 下载**（推荐） | 打 tag 自动构建四平台产物（Windows/Linux/macOS x64 + Apple Silicon），从 Releases 页下载对应平台文件 |
| 源码编译 | `./scripts/build-local-agent.sh`（跨平台）或 `cd local-agent && go build -o local-agent.exe .` |
| 仓库本地 | `local-agent/local-agent.exe`（仅本地，不入库） |

前端登录后会自动激活本机 local-agent；右上角头像 → **本地 Agent** 可查看下载入口、平台文件与启动步骤。

### 启动

```bash
# 本机部署（默认 ws://127.0.0.1:8001，首次运行交互式引导配置）
./local-agent.exe

# 服务器部署：指定服务器地址与工作目录
./local-agent.exe --server ws://<服务器IP>:8001 --workdir E:/agent-workspace
```

登录/注册后前端自动把当前用户 token 推送给 local-agent（`POST 127.0.0.1:17000/activate`），userID 始终与当前登录用户一致。

### 安全约束

- **路径沙箱**：文件工具限制在 `--workdir` 工作区内（`../` 越界拒绝）
- **命令白名单**：sandbox_exec 仅允许 40+ 常用命令（ls/git/go/python 等），白名单外拒绝
- **超时/净化/截断**：30s 硬超时、敏感环境变量净化、输出 4000 字符截断
- **审批 + 备份**：file_write/sandbox_exec 需用户审批（ask 模式）；yolo 模式放行但危险操作前自动备份（snapshot/git）
- **多用户隔离**：服务端按 userID 路由指令，A 用户无法触发 B 用户的本地 agent

---

## Knovis 用户与动态数据服务（service/userapi）

基于 **go-zero** 框架的「用户 + 动态」内容空间后端，作为用户/动态数据的 **owner**：签发 JWT（SSO 上游）、提供注册/登录/动态发布等业务接口；agent-go 只校验 JWT、不签发，通过 REST 查询用户与动态，实现业务数据与 Agent 解耦。

**SSO 对接现状**（已完成）：agent-go 使用相同 `JWT_SECRET` 验签 Knovis 签发的 HS256 token（claims：`userId` 数字 / `iss` / `aud` / `iat` / `exp`）；用户的 Knovis token 以 AES-256-GCM 密文存储于 agent-go（`POST /auth/knovis-token`），knovis Skill 查询时解密透传；`/auth/me` 透传 `GET /api/v1/users/:id`。

## 技术栈（userapi）

- **框架**: go-zero（rest api，goctl 生成）
- **ORM**: go-zero sqlx
- **数据库**: MySQL 8.x（表名 `knovis_user` / `knovis_post`）
- **缓存**: Redis（邮箱验证码，TTL 5 分钟）
- **认证**: JWT（HS256，go-zero 内置 jwt 中间件）
- **邮件**: SMTP（QQ 邮箱 + gomail）
- **文件**: 本地存储（`./uploads`，静态服务 `/uploads`）

## 目录结构（userapi）

```
service/userapi/
├── etc/
│   └── user-api.yaml        # 配置文件(支持 ${ENV} 环境变量展开)
├── internal/
│   ├── config/              # 配置结构(MySQL/Redis/Auth/SMTP)
│   ├── crypto/              # bcrypt 密码哈希
│   ├── email/               # 验证码生成 + SMTP 发送
│   ├── errs/                # 业务错误(带 HTTP 状态码)
│   ├── handler/             # HTTP handler(goctl 生成)
│   ├── logic/               # 业务逻辑
│   ├── model/               # sqlx model(goctl model 生成 + 自定义查询)
│   ├── svc/                 # ServiceContext(依赖注入)
│   ├── types/               # 请求/响应类型(goctl 生成)
│   └── upload/              # 图片/视频校验与保存
└── user.go                  # 入口 main
```

## 快速开始（userapi）

### 1. 准备环境

- MySQL 8.x：创建数据库 `knovis`（或自定义库名）
- Redis

### 2. 配置环境变量

```bash
# 复制示例并修改
cp .env.example .env
```

`.env` 关键项：

| 变量 | 说明 | 示例 |
| --- | --- | --- |
| PORT | 服务端口 | 8080 |
| DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME | MySQL 连接 | 127.0.0.1 / 3306 / root / 123456 / knovis |
| REDIS_HOST / REDIS_PASSWORD | Redis 连接 | 127.0.0.1:6379 |
| JWT_SECRET | JWT HS256 密钥（**Agent 端必须使用相同 Secret 校验**） | 自定义长随机串 |
| JWT_EXPIRE | token 有效期(秒) | 86400 |
| JWT_ISSUER / JWT_AUDIENCE | token 签发者 / 受众 | Knovis / agent-go |
| SMTP_HOST / SMTP_PORT / SMTP_USER / SMTP_PASSWORD | QQ 邮箱 SMTP（Password 为授权码） | smtp.qq.com / 587 / xxx@qq.com / 授权码 |
| UPLOAD_DIR | 上传目录 | ./uploads |

> 说明：配置项在 `service/userapi/etc/user-api.yaml` 中以 `${ENV}` 形式展开；未设置的环境变量使用 main 中的默认值，也可直接改 yaml。

### 3. 建表

```bash
mysql -uroot -p -h127.0.0.1 knovis < sql/schema.sql
```

### 4. 启动

```bash
cd service/userapi
go run user.go
# 或编译
go build -o knovis-user-api.exe user.go && ./knovis-user-api.exe
```

服务默认监听 `0.0.0.0:8080`，上传文件通过 `http://localhost:8080/uploads/xxx` 访问。

### 5. 重新生成代码（可选）

```bash
# 生成 api 代码
goctl api go -api api/user.api -dir service/userapi
# 生成 model 代码
goctl model mysql ddl -src sql/schema.sql -dir service/userapi/internal/model
```

> 注意：重新生成 api 代码后，`internal/handler/createposthandler.go`（文件上传 context 传递）需要按现有实现重新补丁。

## API 文档（userapi）

- **Base URL**: `http://localhost:8080`
- **认证**: 需要登录的接口在 Header 携带 `Authorization: Bearer <token>`
- **错误响应**: `{"code": <业务码=HTTP状态码>, "message": "错误说明"}`（400/401/403/404/409/429/500）

### 用户模块

#### POST /register 注册（公开，暂不强制校验验证码）

```json
{"username":"张三","password":"abc123","confirm_password":"abc123","email":"user@example.com","code":"123456"}
```

→ `{"message":"注册成功","user_id":1}`

> 说明：注册接口的验证码校验目前处于**注释状态**（开发/测试可跳过验证码）；上线前需在 `internal/logic/registerlogic.go` 中启用 `checkCode` 校验。

#### POST /login 登录（公开）

```json
{"email":"user@example.com","password":"abc123"}
```

→ `{"message":"登陆成功","user_id":1,"username":"张三","token":"<jwt>","access_token":"<jwt>"}`

`access_token` 即 JWT（HS256，含 `userId`/`iss`/`aud`/`iat`/`exp`），agent-go 用同一 Secret 验签实现 SSO。

#### POST /send-code 发送邮箱验证码（公开）

```json
{"email":"user@example.com"}
```

验证码存 Redis（key=email，TTL 300s），通过 SMTP 发送。

#### GET /user 用户列表（分页，需登录）

参数：`page`（默认 1）、`page_size`（默认 10，最大 50）

→ `{"total":10,"page":1,"page_size":10,"list":[{...}]}`

#### GET /user/:id 获取用户信息（需登录）

邮箱仅本人可见或用户开启 `email_visible` 时返回。

#### PUT /user/:id 更新用户信息（仅本人）

```json
{"name":"张三","avatar":"/uploads/a.png","bio":"简介","email_visible":true,"likes_visible":true,"favorites_visible":false,"follow_visible":true}
```

> 说明：字段均可选；未传字段按零值覆盖，如需部分更新请传全部字段。

#### PUT /user/password 修改密码（需登录）

```json
{"old_password":"abc123","new_password":"newpass123"}
```

#### PUT /user/email 修改邮箱（需登录）

```json
{"password":"abc123","new_email":"new@example.com","code":"123456"}
```

#### DELETE /user/:id 删除用户（仅本人，可选传密码校验）

```json
{"password":"abc123"}
```

> 注意：此接口只删用户不删动态（会留孤儿数据）；**注销请用下面的 /user/account**（级联删除动态及文件）。

#### DELETE /user/account 注销账号（需登录，级联删除该用户全部动态及文件）

```json
{"password":"abc123"}
```

### 动态模块

#### POST /post 发布动态（需登录，multipart/form-data）

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| type | string | `text` / `image` / `video` |
| content | string | 文字内容（text 必填，≤1000 字） |
| media[] | file | image 类型：1-9 张（jpg/jpeg/png/gif/webp，单张 ≤10MB） |
| video | file | video 类型：单文件（mp4/mov/avi/mkv/webm，≤100MB） |

→ `{"message":"发布成功","post_id":1}`

#### GET /posts 动态列表（广场，公开，分页）

参数：`page`、`page_size`。返回含作者 `user` 信息与 `media_urls` 数组。

#### GET /post/:id 动态详情（公开，浏览数 +1）

#### DELETE /post/:id 删除动态（仅作者，级联删除图片/视频文件）

#### GET /user/:id/posts 用户动态列表（需登录，分页）

#### PUT /post/:id 更新动态设置（仅作者）

```json
{"show_likes":false,"show_favorites":false}
```

> 说明：字段均可选；未传字段按 `false` 覆盖。

---

## 供 Agent 对接的接口清单（userapi → agent-go）

以下为 Knovis 对外暴露的只读接口（供 Agent 系统调用，需携带 Knovis 签发的 JWT，即 `Authorization: Bearer <access_token>`）：

| 方法 | 路径 | 说明 | agent-go 侧消费方 |
| --- | --- | --- | --- |
| GET | `/api/v1/users/:id` | 查询单个用户信息（本人或 `email_visible=true` 才返回邮箱） | `/auth/me` 透传 |
| GET | `/api/v1/feed` | 动态流（分页：`page` / `page_size`） | knovis Skill `knovis_get_feed` |
| GET | `/api/v1/profile` | 当前登录用户资料（返回本人完整信息，含邮箱） | knovis Skill `knovis_get_profile` |
| GET | `/api/v1/profile/:user_id` | 指定用户资料（邮箱可见性规则同上） | knovis Skill `knovis_get_profile` |
| GET | `/api/v1/posts/:id` | 动态详情（浏览数 +1） | knovis Skill `knovis_get_post` |

**JWT 校验约定**：Knovis 登录签发 HS256 token，`Secret`（`JWT_SECRET`）/`Issuer`（`JWT_ISSUER`，默认 Knovis）/`Audience`（`JWT_AUDIENCE`，默认 agent-go）均可配置。agent-go 使用相同 `JWT_SECRET` 验签（`KNOVIS_API_BASE_URL` 指向本服务），不自管签发。

**写操作说明**：Knovis 不实现社交互动模块（点赞/收藏/评论/关注/私信），agent-go 侧 knovis Skill 仅保留上表读操作。

## 验证码说明（userapi）

- 验证码 6 位数字，存 Redis（key=邮箱，TTL 300s），一次性使用。
- 当前**注册不强制校验验证码**（校验逻辑已注释，上线时启用）；`PUT /user/email` 修改邮箱仍强制校验。
- 防滥用：同一邮箱 60 秒内仅允许发送一次；验证码错误超过 5 次自动作废。
- SMTP 使用 QQ 邮箱授权码；未配置 SMTP 时 `/send-code` 会返回 500，但验证码已写入 Redis（开发环境可直接 `redis-cli GET <email>` 取码用于测试）。

# Knovis - 用户与动态数据服务

基于 **go-zero** 框架重写的「用户 + 动态」内容空间后端（仿照 AIWallHub，仅保留用户与动态两个模块）。

Knovis 作为用户/动态数据的 **owner**，通过 REST 接口对外提供数据；Agent 系统（agent-go 项目）只校验 JWT、不签发，通过 REST 查询用户与动态，实现业务数据与 Agent 解耦。

## 技术栈

- **框架**: go-zero（rest api，goctl 生成）
- **ORM**: go-zero sqlx
- **数据库**: MySQL 8.x（表名 `knovis_user` / `knovis_post`）
- **缓存**: Redis（邮箱验证码，TTL 5 分钟）
- **认证**: JWT（HS256，go-zero 内置 jwt 中间件）
- **邮件**: SMTP（QQ 邮箱 + gomail）
- **文件**: 本地存储（`./uploads`，静态服务 `/uploads`）

## 目录结构

```
Knovis/
├── api/
│   └── user.api                     # go-zero api 描述文件(路由 + 类型)
├── sql/
│   └── schema.sql                   # 建表 SQL(MySQL 8.x)
├── service/
│   └── userapi/                     # 服务(goctl api go 生成)
│       ├── etc/
│       │   └── user-api.yaml        # 配置文件(支持 ${ENV} 环境变量展开)
│       ├── internal/
│       │   ├── config/              # 配置结构(MySQL/Redis/Auth/SMTP)
│       │   ├── crypto/              # bcrypt 密码哈希
│       │   ├── email/               # 验证码生成 + SMTP 发送
│       │   ├── errs/                # 业务错误(带 HTTP 状态码)
│       │   ├── handler/             # HTTP handler(goctl 生成)
│       │   ├── logic/               # 业务逻辑
│       │   ├── model/               # sqlx model(goctl model 生成 + 自定义查询)
│       │   ├── svc/                 # ServiceContext(依赖注入)
│       │   ├── types/               # 请求/响应类型(goctl 生成)
│       │   └── upload/              # 图片/视频校验与保存
│       └── user.go                  # 入口 main
├── .env.example                     # 环境变量示例
└── README.md
```

## 快速开始

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

## API 文档

- **Base URL**: `http://localhost:8080`
- **认证**: 需要登录的接口在 Header 携带 `Authorization: Bearer <token>`
- **错误响应**: `{"code": <业务码=HTTP状态码>, "message": "错误说明"}`（400/401/403/404/409/429/500）

### 用户模块

#### POST /register 注册（公开，暂不强制校验验证码）

```json
{"username":"张三","password":"abc123","confirm_password":"abc123","email":"user@example.com","code":"123456"}
```

→ `{"message":"注册成功","user_id":1}`

> 说明：注册接口的验证码校验目前处于**注释状态**（与参考项目一致，开发/测试可跳过验证码）；上线前需在 `internal/logic/registerlogic.go` 中启用 `checkCode` 校验。

#### POST /login 登录（公开）

```json
{"email":"user@example.com","password":"abc123"}
```

→ `{"message":"登陆成功","user_id":1,"username":"张三","token":"<jwt>","access_token":"<jwt>"}`

`access_token` 即 JWT（HS256，含 `userId`/`iss`/`aud`/`iat`/`exp`）。

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

> 说明：字段均可选；未传字段按零值覆盖（与参考项目一致），如需部分更新请传全部字段。

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

> 说明：字段均可选；未传字段按 `false` 覆盖（与参考项目一致）。

---

## 供 Agent 对接的接口清单

以下为 Knovis 对外暴露的只读接口（供 Agent 系统调用，需携带 Knovis 签发的 JWT，即 `Authorization: Bearer <access_token>`）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/users/:id` | 查询单个用户信息（供 Agent 的 `/auth/me` 透传；本人或 `email_visible=true` 才返回邮箱） |
| GET | `/api/v1/feed` | 动态流（分页：`page` / `page_size`） |
| GET | `/api/v1/profile` | 当前登录用户资料（返回本人完整信息，含邮箱） |
| GET | `/api/v1/profile/:user_id` | 指定用户资料（邮箱可见性规则同 `/api/v1/users/:id`） |
| GET | `/api/v1/posts/:id` | 动态详情（浏览数 +1） |

**JWT 校验约定**：Knovis 登录签发 HS256 token，`Secret`（`JWT_SECRET`）/`Issuer`（`JWT_ISSUER`）/`Audience`（`JWT_AUDIENCE`）均可配置。Agent 端使用相同 `JWT_SECRET` 验签即可，无需（也不应）自行签发。

**写操作说明**：Knovis 不实现社交互动模块（点赞/收藏/评论/关注/私信），Agent 侧 Skill 仅保留上表读操作，写操作工具（发帖/评论/点赞/关注等）应在 Agent 侧删除。

## Agent 对接改造清单（供 agent-go 重构参考）

> 基于对 `agent-go`（agent-go 项目）现有实现的调研，列出重构时的对接要点。Knovis 侧接口已就绪，无需改动（除标注"可选"项）。

### 1. JWT：从"自管签发"切到"只校验不签发"

agent-go 当前自管签发 JWT（`internal/auth/jwt.go`，代码注释已预留 SSO 迁移路径）。重构后：

- **停止签发**：删除/停用 `JWTConfig.GenerateToken` 调用链（注册/登录改为引导用户使用 Knovis 的 `/register`、`/login` 获取 token）。
- **配置对齐**（`internal/config/config.go` / `.env`）：
  - `JWT_SECRET` = Knovis 的 `JWT_SECRET`（必须一致）
  - `JWT_ISSUER` = `Knovis`（与 Knovis `JWT_ISSUER` 一致）
  - `JWT_AUDIENCE` = `agent-go`（与 Knovis `JWT_AUDIENCE` 一致）
- **claim 解析差异（关键）**：
  - Knovis 签发 token 的 claims：`userId`（数字）、`iss`、`aud`、`iat`、`exp`；**没有** `user_id`/`username`/`type`
  - agent-go 现有 `Claims.UserID` 从 `user_id`（string）解析 → 需改为从 `userId`（数字）解析，或与 Knovis 协商增加 `user_id` claim（Knovis 可选兼容）
  - `username` 等业务字段不再从 token 拿，改由 `GET /api/v1/users/:id` 或 `/api/v1/profile` 查询

### 2. Skill 端点改造（`internal/tools/skill/skills/aiwallhub.go`）

| 工具 | 现状 | 重构动作 |
| --- | --- | --- |
| `aiwallhub_get_feed` | GET `/api/v1/feed?limit=&cursor=` | 分页参数对齐：改用 `page` / `page_size`（Knovis 现状）；若重构后仍坚持 `limit`/`cursor`，需 Knovis 增加兼容参数（**可选**，二选一） |
| `aiwallhub_get_profile` | GET `/api/v1/profile[/:user_id]` | ✅ 无需改动 |
| `aiwallhub_create_post` / `delete_post` / `comment_post` / `like_post` / `unlike_post` / `follow_user` / `unfollow_user` | 写操作 | **删除**（Knovis 不实现社交模块；发帖/删帖也由用户在 Knovis 前端完成） |
| 无 | — | **新增**读工具 `aiwallhub_get_post`（GET `/api/v1/posts/:id`，动态详情） |
| 无 | — | 可选新增读工具 `aiwallhub_get_user`（GET `/api/v1/users/:id`，供 `/auth/me` 透传） |

### 3. 认证与数据链路

- **token 存储透传**：保留现有 `PUT /auth/aiwallhub-token`（AES-256-GCM 加密存储用户 Knovis token），Skill 调用时解密后作为 `Authorization: Bearer <token>` 透传（Knovis 侧验签）——现有机制无需改动。
- **API 地址**：`AIWALLHUB_API_BASE_URL` 环境变量指向 Knovis（如 `http://127.0.0.1:8080`），当前默认值 `https://api.aiwallhub.com` 需修改。
- **/auth/me**：重构后可改为调用 `GET /api/v1/users/:id`（用 token 中的 `userId`）透传用户资料；当前实现是查 agent-go 本地 user 表，与 Knovis 数据可能不一致。
- **错误响应**：Knovis 错误格式为 `{"code": <HTTP状态码>, "message": "..."}`，agent-go 现有 `client.do` 仅按 `status >= 400` 报错，可解析 `message` 字段增强可读性。

### 4. 数据语义提示（对接时注意）

- **email 可见性**：本人或 `email_visible=true` 才返回邮箱（`/api/v1/users/:id`、`/api/v1/profile/:user_id`）。
- **注销是硬删除**：注销用户后其数据物理消失，`/api/v1/feed` 不会出现已注销用户。
- **userId 类型**：Knovis 的 `userId` 为数字（`int64`），agent-go 内部用户 ID 为字符串，透传时注意类型转换。

## 验证码说明

- 验证码 6 位数字，存 Redis（key=邮箱，TTL 300s），一次性使用。
- 当前**注册不强制校验验证码**（校验逻辑已注释，上线时启用）；`PUT /user/email` 修改邮箱仍强制校验。
- 防滥用：同一邮箱 60 秒内仅允许发送一次；验证码错误超过 5 次自动作废。
- SMTP 使用 QQ 邮箱授权码；未配置 SMTP 时 `/send-code` 会返回 500，但验证码已写入 Redis（开发环境可直接 `redis-cli GET <email>` 取码用于测试）。

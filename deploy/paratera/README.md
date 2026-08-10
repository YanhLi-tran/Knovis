# Knovis @ 并行智算云容器 — 服务器部署手册

> 适用：单机容器（并行智算云），tmux 管理全部服务进程（MySQL / Redis / memory-service / doc-service / knovis-user-api / agent-go）。

## 0. 🔴 部署前必改（5 个占位值）

复制模板并编辑：

```bash
cp deploy/paratera/.env.paratera .env
```

`.env` 中以下 5 个值**必须替换**（不替换会引发安全/功能问题）：

| 变量 | 生成方式 | 不替换的影响 |
|---|---|---|
| `MEMORY_SERVICE_API_KEY` | `openssl rand -hex 16` | 子服务鉴权 key 相同，可互相冒充（安全风险） |
| `DOC_SERVICE_API_KEY` | `openssl rand -hex 16` | 同上；两个 key 必须不同 |
| `JWT_SECRET` | `openssl rand -hex 32` | 与 Knovis 不一致 → agent-go 校验 JWT 失败，登录不可用 |
| `MASTER_KEY_V1` | `openssl rand -base64 32` | AES-256-GCM 主密钥，变更会使已存密钥无法解密 |
| `LLM_API_KEY` | DeepSeek 平台申请（https://platform.deepseek.com/） | 无 key → 对话功能不可用 |

生成示例：

```bash
openssl rand -hex 16 && openssl rand -hex 32 && openssl rand -base64 32
```

> ⚠️ 不要提交任何真实密钥到 git；`.env` 位于项目根目录，勿入库。

## 1. 首次初始化

```bash
bash deploy/paratera/01-init.sh        # 安装 MySQL/Redis/tmux、准备数据目录、初始化数据库
bash deploy/paratera/02-setup-models.sh # 准备 bge-large-zh 等模型（如尚未准备）
```

## 2. 启动 / 停止 / 状态

```bash
bash deploy/paratera/start.sh    # 一键启动全部服务（tmux session: knovis）
bash deploy/paratera/status.sh   # 查看服务状态
bash deploy/paratera/stop.sh     # 停止全部服务
tmux attach -t knovis            # 查看实时日志（Ctrl+B 然后按 D 退出，不停止服务）
```

日志文件：`logs/{memory-service,doc-service,knovis-user,agent-go}.log`

## 3. 环境变量如何生效

- `start.sh` `source .env` 并 export 全部变量；
- agent-go 通过 `conf.UseEnv()` 加载 `agent-go/etc/agent-api.yaml`，其中 `${VAR:-default}` 占位符展开为环境变量（`DB_*` / `REDIS_*` / `MEMORY_SERVICE_URL` / `DOC_SERVICE_URL` / `KNOVIS_API_BASE_URL` / `JWT_SECRET` / `LLM_API_KEY` / `MASTER_KEY_V1` 等）；
- 优先级：系统环境变量 > `.env` > yaml 默认值。

## 4. 数据库初始化与增量迁移

- **首次**（`agent_go` 表数 < 5）：`start.sh` 导入 `sql/docker-init.sql`（全量 DDL）；
- **每次启动**：`start.sh` 无条件执行 `deploy/paratera/migrate-upgrade.sql`（幂等，可重复执行）——已有旧库自动补齐新增对象：
  - `memory_search_metrics` 表（记忆检索监控指标）；
  - `agent_memories` 5 个新字段 `tier` / `effective_importance` / `merged_from` / `merged_at` / `last_decayed_at` + 2 个索引（`idx_tier_project` / `idx_effective_importance`）。

手工执行迁移（可选）：

```bash
mysql -u<user> -p -h<host> < deploy/paratera/migrate-upgrade.sql
```

## 5. 端口与数据目录

| 服务 | 端口 |
|---|---|
| agent-go | 8001 |
| memory-service | 8002 |
| doc-service | 8003 |
| knovis-user-api | 8080 |

- 数据目录：`/root/shared-nvme/data`（MySQL / Redis / Chroma / uploads）
- 模型目录：`/root/shared-nvme/models`（bge-large-zh / bge-reranker-v2-m3）

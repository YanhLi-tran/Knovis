# 企业级智能知识助手 — 开发路线

> Agent 项目秋招方向规划：RAG + Function Call 多工具编排的企业级知识助手
>
> 目标岗位：大厂 AI/算法应用岗
>
> 演示形态：完整 Web 应用（前端 + 后端 + 演示数据）
>
> **2026-08-15 对齐说明**：P1-P4 任务清单已按实际完成情况勾选（含实现位置与原计划的偏差标注）；资产盘点表已更新 docker-compose 状态。

---

## 1. 项目定位

**企业级智能知识助手**：基于 Function Call 多工具编排的企业知识问答与执行平台，融合 RAG 文档检索 / NL2SQL 数据查询 / Web 搜索 / 文件操作 / 沙箱命令执行，支持多轮对话、引用溯源、人工审批与提问澄清。

### 差异化一句话

不是"PDF 问答器"，而是"企业内部大脑 + 执行手"——LLM 自己决定查文档、查数据库、搜外网、跑脚本中的哪个，工具间还能链式协作。

---

## 2. 架构总览

```
┌─────────────────────────────────────────────────┐
│  Web 前端 (SSE 流式 + 引用卡片 + 审批/提问框)    │ ← 已有，含调试面板(trace)
├─────────────────────────────────────────────────┤
│  Agent 编排层 (OTACO = ReAct + 反思)             │ ← 已有，加 Plan-Execute
│  ├─ 工具注册 (Registry + Skill 注册表)           │
│  ├─ 多轮上下文管理 (会话历史 + 按需压缩)          │ ← 已有(2026-08-12 起为按需压缩) │
│  └─ 可观测性 (trace + token 计量)                │ ← 已有(trace),token 计量待补
├─────────────────────────────────────────────────┤
│  工具层                                          │
│  ├─ RAG 检索 (向量 + BM25 + 段落召回 + rerank)   │ ← 已有(rerank 默认关闭)
│  ├─ NL2SQL 查询                       ← 新增    │
│  ├─ Web 搜索 (腾讯 WSA)               ← 已有    │
│  ├─ 文件操作 (read/write/grep/list)   ← 已有    │
│  ├─ 沙箱命令 (sandbox_exec + 审批)    ← 已有    │
│  ├─ ask_user 提问澄清                 ← 已有    │
│  ├─ 记忆系统 (混合检索 + 自动提取)    ← 已有    │
│  └─ MCP 外接                          ← 预留(Layer 3,未实现) │
├─────────────────────────────────────────────────┤
│  基础设施                                        │
│  ├─ Go 主服务 (go-zero rest + SSE)    ← 已有    │
│  ├─ Python 子服务 (memory/doc)        ← 已有    │
│  ├─ DeepSeek / OpenAI 兼容            ← 已有    │
│  ├─ MySQL 会话/审计/记忆              ← 已有    │
│  └─ Knovis user-api (SSO 签发 JWT)    ← 已有    │
└─────────────────────────────────────────────────┘
```

### 现有资产盘点(2026-08-06 已对齐实际)

| 模块 | 状态 | 说明 |
|---|---|---|
| Go 主服务(go-zero rest) | ✅ 已有 | `agent-go` :8001,API + OTACO 编排(原 Gin,2026-08-05 重构为 go-zero) |
| 记忆系统 | ✅ 已有 | MySQL 13 表 + Redis + Chroma,BM25+RAG 混合检索,TTL 归档 |
| RAG 文档系统 | ✅ 已有 | `doc-service` :8003,PDF 摄入 + 多路召回 + 段落召回 + 引用溯源 |
| Embedding 子服务 | ✅ 已有 | `memory-service` :8002,bge-large-zh + BM25 + 关键词提取 |
| 用户/动态服务 | ✅ 已有 | `Knovis user-api` :8080(go-zero),SSO 签发 JWT,agent-go 只校验 |
| 工具注册机制 | ✅ 已有 | 三层架构:13 个常驻 FC + 2 个 Skill(按需加载) |
| 文件操作工具 | ✅ 已有 | file_read/file_write/grep/file_list(走 WS 下发 local-agent) |
| 沙箱命令 | ✅ 已有 | sandbox_exec(白名单 + timeout + 审批流) |
| Web 搜索 | ✅ 已有 | 腾讯云 WSA |
| ask_user 提问工具 | ✅ 已有 | SSE 弹框 + 回灌 |
| 评测体系 | ✅ 已有 | 64 条 RAG + 35 条 Agent 评测集 + LLM-as-Judge |
| 全链路 trace | ✅ 已有 | X-Trace-Id 透传 + 前端调试面板(2026-08-06) |
| 前端 | ✅ 已有 | 单文件 HTML(SSE + 审批框 + 提问框 + 调试面板) |
| MCP 外接 | ❌ 预留 | 设计保留 Layer 3,未实现 |
| NL2SQL / Plan-and-Execute | ❌ 待做 | 见 [04-待补齐项与优先级.md](./04-待补齐项与优先级.md) |
| docker-compose 一键部署 | ✅ 已有 | 2026-08-06 落地:6 容器(MySQL/Redis/Chroma/memory/doc/agent-go)+ .env.docker.example,见根 README |

---

## 3. 差异化亮点（简历重点）

这 5 个点是能在简历上写、面试时讲出深度的地方，多数候选人没有。

### 3.1 RAG 工程深度（不只是 chunk + embed）

- **结构化分块**：Markdown 按标题层级切，PDF 按版面切，代码按函数切，而不是固定 512 token
- **多路召回**：向量检索 + BM25 关键词检索 + 元数据过滤，加权融合
- **Rerank**：接 bge-reranker 或 Cohere rerank，top-20 → top-5
- **引用溯源**：答案里标注 `[来源: doc_name, p3]`，前端渲染成可点击卡片
- **可讲优化故事**：召回率/准确率提升对比（评测集前后对比）

### 3.2 工具编排策略升级

- 现在是**朴素 Function Call**（LLM 决定调哪个工具）
- 升级成 **Plan-and-Execute**：先让 LLM 拆解任务计划 → 逐步执行 → 反思是否需要补查
- 适合复杂问题如"分析本月销售数据并对比上月，生成报告"——LLM 需要规划 4-5 步工具链
- **可讲**：单轮 Function Call vs Plan-Execute 的复杂任务完成率对比

### 3.3 评估体系（大厂最看重但候选人最缺）

- 自建 50-100 条评测集（覆盖单工具/多工具/RAG/多轮）
- 指标：工具选择准确率、参数填充正确率、答案事实准确率（LLM-as-Judge）
- 输出评测报告，对比不同 Prompt / 模型 / 检索策略
- **简历可写**："构建评测体系，优化检索策略使答案准确率从 72% → 89%"

### 3.4 可观测性

- 每次 Agent 调用生成 `trace_id`，记录：每轮思考、工具调用、token 消耗、延迟
- 前端有"调试视图"可展开看完整 trace
- **可讲**：线上 badcase 如何通过 trace 定位到检索召回问题

### 3.5 工程化细节

- 流式 SSE + 中断恢复
- 工具调用失败重试 + 降级
- 会话上下文超长时自动压缩（保留工具调用结果摘要）
- Docker 一键部署（docker-compose.yml）

---

## 4. 开发路线（分 4 阶段，按 ROI 排序）

| 阶段 | 目标 | 核心产出 | 简历价值 |
|---|---|---|---|
| **P1：RAG 基础** | 文档摄入 + 检索 | 向量库 + 分块 + embedding + 检索工具 | 能讲 RAG 基础 |
| **P2：多工具编排** | RAG + 现有工具联动 | 把 RAG 检索注册成工具，LLM 自主选择查文档/查数据库/搜外网 | 能讲 Agent 架构 |
| **P3：差异化深度** | rerank + 引用溯源 + NL2SQL | 多路召回 + rerank、答案引用卡片、SQL 查询工具 | 能讲工程优化 |
| **P4：评估+可观测** | 评测集 + trace | 50 条评测集 + 评测脚本 + trace 视图 + docker-compose | 大厂加分项 |

---

## 5. 各阶段详细任务

### P1：RAG 基础

**目标**：跑通"上传文档 → 提问 → LLM 自动调 rag_search → 引用片段回答"最小闭环。

**任务清单**：

- [x] 选定向量库（Chroma / Milvus Lite）→ **Chroma**（嵌入式持久化）
- [x] 选定 embedding 模型（BGE-M3 本地 / text-embedding-3-small API）→ **bge-large-zh 本地**（1024 维）
- [x] 写文档摄入 pipeline
  - [x] PDF 解析（unstructured / pypdf）→ 实际用 **pymupdf4llm**（保留表格与标题层级，doc-service/parser.py）
  - [x] Markdown 解析（按标题层级切分）
  - [x] 通用分块策略（可配置 chunk_size / overlap；2026-08-10 起最优 256/26）
- [x] 实现 rag_search 工具 → 实际为 **Go 实现**（agent-go/internal/tools/info/rag_search.go，FC 常驻；原计划 Python 路径未采用）
  - [x] 参数：`query`、`top_k`（另有 `doc_ids` 过滤）
  - [x] 返回：检索片段 + 来源元数据（doc_name/page_num/heading_path 引用溯源）
- [x] 前端加文档上传入口（文档管理 Modal，2026-08-13 起上传自动标记私有）
- [x] 后端加 `/documents/ingest` 端点（上传 → 分块 → embedding → 入库，Go 转发 doc-service）
- [x] 跑通端到端演示（2026-08-03：五粮液 2021 营收问答，3 次 rag_search + 引用溯源）

**完成标准**：上传一份 PDF，提问能返回带来源的答案。✅ 已达成

---

### P2：多工具编排

**目标**：RAG 与现有工具联动，LLM 自主决策调用哪个工具。

**任务清单**：

- [x] 把 `rag_search` 正式注册到 ToolRegistry（第 11 个常驻 FC 工具）
- [x] 系统提示词扩展：告诉 LLM 何时用 RAG / 何时用 web_search / 何时用文件工具（OTACO 工作流程 + 工具描述）
- [x] 多轮上下文管理
  - [x] 会话历史持久化（实际用 **MySQL**，非 SQLite；13 张表）
  - [x] 按 session_id 隔离对话（+ owner_id 用户隔离）
- [x] 工具调用链路可视化
  - [x] 前端展示 LLM 选择了哪些工具、按什么顺序（SSE tool_call/tool_result 事件 + 调试面板）
- [x] 复杂场景演示案例（RAG 单独 / RAG+web_search 链式 / RAG+file_write 落盘均已验证）

**完成标准**：同一问题 LLM 能合理选择多个工具组合作答。✅ 已达成

---

### P3：差异化深度

**目标**：把 RAG 从"能跑"提升到"有工程深度"，简历能写优化故事。

**任务清单**：

- [x] 多路召回
  - [x] 向量检索（numpy 暴力 cosine，绕过 Chroma HNSW bug）
  - [x] BM25 关键词检索（rank_bm25；2026-08-10 起为 jieba+rank_bm25 内存索引，替代 MySQL FULLTEXT）
  - [x] 元数据过滤（doc_ids 文档范围过滤 + owner_id 可见性过滤）
  - [x] 加权融合（RRF **和**线性加权均有：top-5 RRF 精排 + top-20 加权融合，2026-08-04 混合策略）
- [x] Rerank
  - [x] 接入 bge-reranker-v2-m3（本地 CrossEncoder，已接入并完成 Before/After 评测）
  - [x] top-20 召回 → top-5 rerank（评测显示当前配置 Recall@5 轻微下降，默认 RERANK_ENABLED=false，调优项见 04 §1.2.1）
- [x] 引用溯源
  - [x] 答案里注入 `[来源: doc_name, p3]` 标记
  - [x] 前端渲染成可点击卡片（展开查看原文/页码/章节）
- [x] 结构化分块升级
  - [x] Markdown 按标题层级切
  - [x] PDF 按标题层级切（pymupdf4llm 转 Markdown 后切，非 unstructured 版面切）
  - [ ] 代码按函数切（未做）
- [ ] NL2SQL 工具（未做）
  - [ ] 准备示例数据库（SQLite + 销售数据集）
  - [ ] sql_query 工具：自然语言 → SQL → 执行 → 结果
  - [ ] SQL 安全校验（只读、白名单表）

**完成标准**：能讲出召回率/准确率提升数据，答案带可点击引用。✅ 已达成（NL2SQL 除外）

---

### P4：评估 + 可观测

**目标**：补齐大厂最看重的评估体系和可观测性，工程化收尾。

**任务清单**：

- [x] 评测体系
  - [x] 构建 50-100 条评测集 → 实际 **64 条 RAG + 35 条 Agent**（无 SQL 类场景，NL2SQL 未做）
    - [x] 单工具场景（RAG / web_search；SQL 类因工具未做暂缺）
    - [x] 多工具编排场景
    - [x] 多轮对话场景
  - [x] 评测脚本（rag_eval.py / agent_eval.py，支持 --category/--strategy）
    - [x] 工具选择准确率
    - [x] 参数填充正确率
    - [x] 答案事实准确率（LLM-as-Judge）
  - [x] 评测报告输出（Markdown + 对比表格 + 分阶段耗时 P50/P95）
- [x] 可观测性
  - [x] 每次 Agent 调用生成 `trace_id`（X-Trace-Id 全链路透传，2026-08-06）
  - [x] 记录：每轮思考、工具调用、token 消耗、延迟
  - [x] 前端"调试视图"展开看完整 trace（调试面板；"逐轮完整 trace 树"待深化）
  - [x] 审计日志表（实际用 **MySQL** agent_audit_logs，非 SQLite）
- [x] 工程化收尾
  - [ ] 流式 SSE 中断恢复（未做）
  - [x] 工具调用失败重试（OTACO retry 决策 + LLM 429/5xx 指数退避，2026-08-10）；降级未做
  - [x] 上下文超长自动压缩（2026-08-12 起为按需压缩：≥80% LLM 自主 / ≥90% Go 强制）
  - [x] `docker-compose.yml` 一键部署（2026-08-06，6 容器）
  - [x] README + 架构图（根 README + docs 各专题文档）

**完成标准**：有评测报告数据、有 trace 演示、docker-compose 一键起。✅ 已达成（SSE 中断恢复除外）

---

## 6. 技术选型

| 组件 | 选型 | 理由 |
|---|---|---|
| 向量库 | **Chroma**（本地）或 **Milvus Lite** | 单文件部署，简历不丢分；Milvus 更大厂向 |
| Embedding | **BGE-M3** 或 **text-embedding-3-small** | BGE 中文好且开源，OpenAI 兼容已有 SDK |
| Rerank | **bge-reranker-v2-m3**（本地）或 Cohere | 本地版能讲部署，云版简单 |
| 文档解析 | **unstructured** + **pypdf** | 支持 PDF/Word/Markdown/HTML |
| BM25 | **rank_bm25** | 轻量纯 Python |
| 数据库（NL2SQL 目标） | **SQLite** + 示例数据集 | 演示用，免部署（规划中） |
| 会话/审计存储 | **MySQL** | 已落地(agent_go 库,13 张表) |
| LLM | **DeepSeek Chat** | 已有，OpenAI 兼容 |
| Web 框架 | **go-zero rest + SSE** | 已有(2026-08-05 从 Gin 重构) |
| 前端 | **单文件 HTML/CSS/JS** | 已有，含引用卡片 + 调试面板 |

---

## 7. 简历关键词

> Agent 架构 / Function Call / RAG / 多路召回 / Rerank / 引用溯源 / Plan-and-Execute / 评估体系 / LLM-as-Judge / SSE 流式 / 工具编排 / 上下文压缩 / Docker 工程化

---

## 8. 演示场景设计（面试用）

准备 3-5 个能覆盖亮点的演示场景，面试时按需展示：

1. **纯 RAG 问答**：上传公司报销政策 PDF → 提问"差旅费报销上限" → 带引用回答
2. **多工具编排**：提问"对比公司差旅政策和行业最新标准" → RAG + web_search 链式调用
3. **NL2SQL**：提问"上个月销售额 top 5 的产品" → SQL 查询 + 图表化结果
4. **执行类**：提问"把刚才的分析结论写成 report.md 保存" → writeFile 工具 + 审批流
5. **澄清提问**：模糊问题"帮我查下数据" → ask_user 弹框追问查什么数据

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| RAG 效果差，答非所问 | P3 阶段上 rerank + 多路召回，用评测集量化改进 |
| 评测集构建耗时 | 先 30 条覆盖核心场景，后续迭代补到 100 条 |
| NL2SQL 安全风险 | 只读限制 + 表白名单 + SQL 预校验 |
| 上下文超长 | P4 阶段做工具结果摘要压缩 |
| Docker 部署依赖外部 API | .env 模板 + 文档说明，演示时用本地模型兜底 |

---

## 10. 里程碑时间节点（建议）

> 时间为参考值，按个人节奏调整。

| 里程碑 | 阶段 | 交付物 |
|---|---|---|
| M1 | P1 完成 | RAG 最小闭环可演示 |
| M2 | P2 完成 | 多工具编排演示 + 会话持久化 |
| M3 | P3 完成 | rerank + 引用溯源 + NL2SQL |
| M4 | P4 完成 | 评测报告 + trace + docker-compose |
| M5 | 简历定稿 | 简历项目描述 + 3 个演示场景录屏 |

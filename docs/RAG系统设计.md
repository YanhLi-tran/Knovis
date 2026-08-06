# RAG 系统设计文档

> 阶段五:企业级文档 RAG 系统(独立于记忆系统,全局共享文档库)
>
> 更新时间:2026-08-03

---

## 1. 目标与定位

构建独立的企业年报 RAG 系统,支持上传 PDF → 结构化分块 → 多路召回 → 段落级上下文召回 → 引用溯源回答。LLM 通过 FC 常驻工具 `rag_search` 自主检索。

**简历亮点**:结构化分块(标题层级)+ 多路召回(BM25 + 向量)+ 段落级上下文召回(索引粒度=chunk,返回粒度=最小标题小节)+ 引用溯源。

**与记忆系统的区别**:
- 记忆系统:按项目隔离,Chroma collection `proj_{project_id}`,语义是"用户/项目记忆"
- RAG 系统:**全局共享**,Chroma 单 collection `doc_global`,语义是"企业文档库"

---

## 2. 核心决策

| 决策点 | 选定方案 | 理由 |
|---|---|---|
| 数据隔离 | 全局共享(单 collection `doc_global`) | 企业文档库全员共享,简化权限 |
| 服务架构 | 新建 doc-service(8003),HTTP 调 memory-service(8002) 的 /embed | 复用已加载的 bge-large-zh,不重复占 1.5GB 内存 |
| PDF 解析 | pymupdf4llm → Markdown(保留表格) | 保留标题层级与表格结构 |
| 分块策略 | 标题层级切分 + chunk_size=800/overlap=64 | 语义完整 + 精确召回 |
| 表格处理 | 保留为 Markdown 表格,作为独立块 | 表格语义不可拆 |
| 召回策略 | 索引粒度=chunk(800字),返回粒度=最小标题小节;小节超 2000 字 fallback 到 chunk + 前后各 1 chunk | 解决单 chunk 命中但上下文断裂痛点 |
| Rerank | 接口完整实现 + bge-reranker-v2-m3 已评测(2026-08-05);`RERANK_ENABLED=false` 默认关闭(评测显示当前配置下轻微下降) | Before/After 数据见《03-评测体系建设.md》§六,调优后再启用 |
| 引用溯源 | 答案标 `[来源: xxx.pdf, p45, 章节]` + 前端卡片 | 可验证、可追溯 |
| 上传方式 | 本地目录扫描导入 + HTTP 上传 | 15 份年报批量导入 + 单文件上传 |
| 元数据提取 | 自动从文件名 `股票代码_年份_公司简称_全称.pdf` 解析,失败则手动填 | 文件名规律明确 |
| 删除级联 | 软删 MySQL + 删 Chroma 向量(事务) | 数据一致性 |
| rag_search 工具 | FC 常驻(不走 Skill) | 高频核心能力 + 简单 schema |

---

## 3. 架构

```
┌─────────────────────────────────────────────────┐
│ 前端 (agent-go/static/index.html)               │
│  文档管理页 + 引用溯源卡片 + 检索调试视图        │
├─────────────────────────────────────────────────┤
│ Go 主服务 (8001)                                │
│  rag_search FC 工具(常驻) + /documents/* API   │
│  + /rag/debug 调试 + /documents/scan 扫描导入   │
├─────────────────────────────────────────────────┤
│ doc-service (8003,新建)                         │
│  /documents/ingest + /documents/scan +          │
│  /rag/search(段落召回) + 列表/删除 + /health    │
├─────────────────────────────────────────────────┤
│ memory-service (8002,已有,不改)                 │
│  POST /embed 复用 bge-large-zh                  │
├─────────────────────────────────────────────────┤
│ 存储                                            │
│  MySQL: agent_documents + agent_document_chunks │
│  Chroma: doc_global collection(全局共享)        │
│  文件系统: doc-service/data/uploads/            │
└─────────────────────────────────────────────────┘
```

---

## 4. 数据表

### agent_documents(文档元信息)
- id / filename / file_path / file_size / total_pages / total_chunks
- status(processing/ready/failed)/ error_msg
- company_code / company_name / report_year / report_type(文件名自动解析)
- manual_meta(JSON,自动解析失败时手动填)
- created_at / updated_at / deleted_at(软删)

### agent_document_chunks(分块内容)
- id / document_id / chunk_index(文档内递增)/ page_num
- heading_path(JSON,如 ["三、财务信息","(一)营业收入"])
- section_id(heading_path 的 hash,段落召回用)
- content / content_length / chunk_type(text/table/heading)
- embedding_status(pending/done/failed)
- FULLTEXT INDEX ft_content(content) WITH PARSER ngram(BM25)
- INDEX(document_id) / INDEX(section_id) / INDEX(page_num)

---

## 5. 段落召回逻辑(简历核心创新点)

```
query → BM25(chunks 表 FULLTEXT) top-20 + RAG(numpy 暴力 cosine) top-20
融合 → top-20 候选 chunk 列表(归一化 + 3:7 加权)
[可选] rerank → top-5(RERANK_ENABLED=true 时)
段落扩展:
  for each chunk in 候选:
    section_chunks = SELECT * FROM chunks WHERE section_id=? ORDER BY chunk_index
    section_text = 拼接 section_chunks.content
    if len(section_text) > 2000:
      # fallback: 返回命中 chunk + 前后各 1 chunk
      section_text = prev_chunk.content + chunk.content + next_chunk.content
    返回 {content, doc_name, page_num, heading_path, score, sources}
去重(同 section_id 只返回一次,取最高分)
返回 top_k(默认 5)
```

**价值**:索引粒度=chunk(精确召回),返回粒度=最小标题小节(语义完整),解决 RAG 经典痛点——单个 chunk 命中但上下文断裂。

### 5.1 向量检索实现:numpy 暴力 cosine(绕过 Chroma HNSW)

**背景**:Chroma 1.5.x 改用 Rust 绑定 HNSW 实现后,搜索召回率异常——所有查询返回相同 distance(如 0.2271),错过真正近邻(手动 cosine 计算 top1=0.84 但 Chroma 返回 0.77)。尝试重建 collection(M=32, construction_ef=256)和提高 n_results=200 均无效,根因是 Rust HNSW 实现 bug。

**解决方案**:在 [store.py](../doc-service/store.py) `rag_search` 中改用 numpy 暴力 cosine 搜索,Chroma 仅用于向量存储(upsert/delete)。

| 环节 | 实现 | 说明 |
|---|---|---|
| 向量存储 | Chroma `doc_global` collection | upsert/delete 不变,metadata 含 document_id/page_num/chunk_index |
| 向量缓存 | `_vec_cache_matrix`(模块级) | 首次查询时从 Chroma 加载全部向量到内存(18801×1024 float32 ≈ 73MB,耗时 1.5s) |
| 搜索 | numpy 矩阵乘法 `embeds @ qvec` | 向量已归一化(bge-large-zh),cosine similarity = dot product,单次 < 50ms |
| top_n 选取 | `np.argpartition` + `np.argsort` | 快速选 top_n 再排序,O(N) 选 + O(n log n) 排序 |
| doc_ids 过滤 | mask 置 -1 | 非目标文档相似度置 -1,过滤后不返回 |
| 缓存失效 | `_invalidate_vec_cache()` | upsert_vectors/delete_doc_vectors 后调用,下次查询自动重新加载 |

**召回率 100%**(暴力搜索无近似),性能可接受(18801 条向量单次查询 < 50ms,首次加载 1.5s)。

---

## 6. API 端点

### doc-service(8003)
| 端点 | 功能 |
|---|---|
| POST /documents/ingest | 上传 PDF → 解析 → 分块 → embed → 入库 |
| POST /documents/scan | 扫描本地目录批量导入 |
| POST /rag/search | 混合检索 + 段落召回 |
| GET /documents | 文档列表(支持过滤) |
| GET /documents/:id | 文档详情 |
| DELETE /documents/:id | 软删 + 级联删 chunks + 删 Chroma(事务) |
| GET /health | 健康检查(含 rerank 状态) |

### Go 主服务(8001)
| 路由 | 方法 | 鉴权 | 说明 |
|---|---|---|---|
| /documents/upload | POST | JWT | 上传 PDF(转发 doc-service) |
| /documents | GET | JWT | 文档列表 |
| /documents/:id | DELETE | JWT | 删除文档(级联) |
| /documents/scan | POST | JWT | 扫描本地目录导入 |
| /rag/debug | POST | JWT | 检索调试 |
| rag_search | FC 工具 | - | LLM 自主调用(query/top_k/doc_ids) |

---

## 7. rag_search FC 工具(常驻)

- 参数:query(必填)、top_k(默认5)、doc_ids(可选过滤)
- 返回:[{content, doc_name, page_num, heading_path, score, sources}]
- 注入位置:system prompt 工具列表(与 web_search/file_read 同级,稳定区,KV Cache 友好)
- 失败处理:返回错误信息给 LLM,不阻断对话

---

## 8. 配置项(.env)

| 变量 | 默认 | 说明 |
|---|---|---|
| DOC_SERVICE_URL | http://127.0.0.1:8003 | doc-service 地址(Go 端) |
| DOC_SERVICE_PORT | 8003 | doc-service 端口 |
| DOC_UPLOAD_DIR | ./data/uploads | PDF 存储目录 |
| RERANK_ENABLED | false | 是否启用 rerank |
| RERANK_MODEL_PATH | (空) | rerank 模型路径,启用时填 |
| MEMORY_SERVICE_URL | http://127.0.0.1:8002 | memory-service 地址(doc-service 调 /embed) |
| RAG_BM25_WEIGHT | 0.3 | BM25 权重 |
| RAG_RAG_WEIGHT | 0.7 | 向量权重 |
| RAG_RECALL_TOP_N | 20 | 每路召回数 |
| RAG_SECTION_MAX_LEN | 2000 | 小节超此长度 fallback |
| RAG_CHUNK_SIZE | 800 | 分块大小 |
| RAG_CHUNK_OVERLAP | 64 | 分块重叠 |

---

## 9. 实现进度

| 模块 | 文件 | 状态 |
|---|---|---|
| 数据层 | storage/doc_models.go + doc_repo.go | ✅ |
| doc-service 摄入 | doc-service/{main,parser,store,reranker,embedder_client}.py | ✅ |
| doc-service 检索 | doc-service/main.py + store.py | ✅ |
| Go rag_search 工具 | tools/info/rag_search.go | ✅ |
| Go 文档 API | api/document_api.go + rag/client.go | ✅ |
| 路由 + 配置 | server.go + config.go + main.go + .env.example | ✅ |
| 前端 | static/index.html(文档管理 Modal + 检索调试 Modal + 引用溯源卡片) | ✅ |
| memory-service 扩展 | memory-service/main.py 新增 /embed_vectors(仅返回向量不 upsert) | ✅ |

---

## 10. 实现清单与验证结果

### 10.1 实现清单(已完成)

**数据层(Go)**
- `agent_documents` 表:文档元信息(文件名/路径/页数/分块数/状态/股票代码/公司名/年份/手动元数据/软删)
- `agent_document_chunks` 表:分块内容(heading_path JSON/section_id/page_num/content/FULLTEXT ngram 索引)
- GORM 模型 + 仓储层 CRUD(doc_models.go + doc_repo.go)
- AutoMigrate 自动建表 + applyPostMigrateIndexes 幂等创建 FULLTEXT 索引

**doc-service(Python,8003)**
- `main.py`:7 个端点(/health、/documents/ingest、/documents/scan、/rag/search、/documents、/documents/:id、DELETE /documents/:id)
- `parser.py`:pymupdf4llm 解析 PDF → Markdown + 标题层级分块(中文编号/Markdown 标题识别 + 800/64 滑动窗口二次切 + 表格独立块)+ 文件名元数据解析
- `store.py`:MySQL chunks 读写 + BM25 FULLTEXT 检索 + Chroma doc_global 向量检索 + 3:7 归一化融合 + 段落召回(section_id 聚合,超 2000 字 fallback 到 chunk+前后各1)
- `reranker.py`:Reranker 接口完整实现(is_enabled/rerank),RERANK_ENABLED=true + RERANK_MODEL_PATH 启用;2026-08-05 已用 bge-reranker-v2-m3 完成 Before/After 评测(见 03 §六),当前默认关闭
- `embedder_client.py`:HTTP 调 memory-service /embed_vectors(分批 32),复用 bge-large-zh 不重复加载

**memory-service 扩展**
- 新增 `POST /embed_vectors`:仅返回向量列表不 upsert Chroma(供 doc-service 调用)

**Go 主服务(8001)**
- `rag/client.go`:DocClient(Search/ListDocuments/DeleteDocument/Scan/UploadFile)
- `tools/info/rag_search.go`:rag_search FC 常驻工具 + formatRAGResults(带引用溯源文本 + 截断保护 1500 字)
- `api/document_api.go`:uploadDocument/listDocuments/deleteDocument/scanDocuments/ragDebug(转发 doc-service)
- `server.go`:路由组 /documents + /rag/debug
- `config.go`:DOC_SERVICE_URL 配置项(默认 http://127.0.0.1:8003)
- `main.go`:docClient 初始化 + RegisterRAGSearchTools 注册

**前端(static/index.html)**
- 顶部栏新增 📄 文档管理 + 🔍 检索调试 两个入口
- 文档管理 Modal:上传 PDF + 扫描目录导入 + 文档列表(公司/年份/状态/分块数/删除级联)
- 检索调试 Modal:query 输入 + BM25/向量/融合/返回/耗时 5 项统计 + 结果明细(章节/命中路数/相关度/内容预览)
- 引用溯源卡片:rag_search 工具结果自动解析为可展开卡片(文档名+页码+章节+命中路数+相关度+原文)

### 10.2 验证结果

| 验证项 | 结果 |
|---|---|
| go build ./... | ✅ 通过 |
| go vet ./... | ✅ 无告警 |
| doc-service 启动 | ✅ /health 返回 ok(rerank_enabled=false, memory_service=ok, collection=doc_global) |
| rag_search 工具注册 | ✅ /tools 返回 12 个工具(含 rag_search,#5) |
| 数据表创建 | ✅ AutoMigrate + FULLTEXT 索引 ft_content(agent_document_chunks) |
| PDF 摄入 pipeline | ✅ 五粮液2021年报:解析 951 chunks(含 OCR)+ embed + Chroma upsert |
| 文件名元数据解析 | ✅ 自动解析 股票代码/年份/公司名(000858/2021/五粮液) |
| page_num 页码解析 | ✅ 修复后 page_num 分布 [105,19,14,38,63] 全部不同(原全为1) |
| **段落召回 + fallback** | ✅ 结果#5 fallback=True,content_len=2208(超2000小节回退chunk+前后各1) |
| **LLM 自主调用 rag_search** | ✅ 提问"五粮液2021营收"→ LLM 调用 rag_search 3次(多轮检索) |
| **引用溯源** | ✅ 答案含 `[来源: 000858_2021_五粮液_2021年年度报告.pdf, p6, 六、主要会计数据和财务指标；p23, (三)2021年经营计划的完成情况]` |
| **答案准确性** | ✅ 营收 66,209,053,612.11 元(约662.09亿,同比增长15.51%)—与年报数据一致 |
| BM25 + RAG 双路召回 | ✅ bm25_count=20, rag_count=20, fused_count=20, 耗时 334-387ms |
| rerank 接口预留 | ✅ RERANK_ENABLED=false 时检索正常返回,不影响功能 |
| 删除级联 | ✅ DELETE /documents/4 → chunks=844 删除 + Chroma 向量清理 |
| Go 端 /rag/debug | ✅ 转发 doc-service 正常,返回各路召回数+融合明细+段落扩展 |
| 本地扫描导入端点 | ✅ POST /documents/scan 创建文档记录(五粮液2021已就绪,2022创建成功) |

### 10.3 关键约束遵守情况

1. ✅ Go 主体 + Python 辅助:Go 做 API/工具注册,Python 做 PDF 解析/检索
2. ✅ doc-service 复用 memory-service embedding:HTTP 调 /embed_vectors,不重复加载 bge-large-zh
3. ✅ rag_search 走 FC 常驻:与 web_search/file_read 同级,buildSystemPrompt 工具列表注入
4. ✅ KV Cache 友好:工具描述在稳定区(工具列表),不插入 memoryBlock
5. ✅ 全局共享:Chroma 单 collection doc_global
6. ✅ 段落召回:索引粒度=chunk(800字),返回粒度=最小标题小节,超 2000 字 fallback
7. ✅ rerank 已接入并评测:bge-reranker-v2-m3,Before/After 数据见 03 §六,默认关闭待调优
8. ✅ 引用溯源:rag_search 返回 doc_name+page_num+heading_path,前端渲染卡片
9. ✅ 删除级联:软删 MySQL + 删 Chroma 向量
10. ✅ 文件名元数据解析:自动从 股票代码_年份_公司简称_全称.pdf 解析
11. ✅ pymupdf4llm 分块:标题层级切分 + 800/64 滑动窗口 + 表格独立块
12. ✅ 不阻断主流程:rag_search 失败返回错误信息给 LLM,不阻断对话
13. ✅ 未改 P1-P4 代码逻辑;未做 docker-compose;未做安全 P0;rerank 已评测(默认关闭)

### 10.4 性能说明

- bge-large-zh 在 CPU 模式下 embedding 约 22 秒/批(32 chunks),单份年报(~950 chunks)摄入约 7-10 分钟
- 15 份年报全量扫描导入预计 2-2.5 小时(含 OCR + embedding)
- 检索阶段 query 向量化 < 1 秒,BM25 + 向量双路召回 + 段落扩展 334-387ms
- LLM 端到端(OTACO 多轮 + 3次 rag_search + 最终答案)约 60-90 秒
- 后续可启用 GPU 加速 embedding 或切换更轻量模型提升摄入速度

### 10.5 端到端验证详情(2026-08-03)

**测试问题**:"五粮液 2021 年营业收入是多少?"

**LLM 行为**:
1. 第1轮:调用 `rag_search(query="五粮液 2021年 营业收入", top_k=5)`
2. 第2轮:调用 `rag_search(query="五粮液 2021年度 主要会计数据 营业收入 合计 元", top_k=5)`
3. 第3轮:调用 `rag_search(query="2021年年度报告 主要会计数据 营业收入 比上年同期增减 归属于上市公司股东的净利润", top_k=5)`
4. 综合三轮检索结果生成最终答案

**最终答案**:
> 五粮液(宜宾五粮液股份有限公司)2021 年营业收入为 **66,209,053,612.11 元**,约合 **662.09 亿元**,同比增长 **15.51%**。
> - 同期归属于上市公司股东的净利润为 233.77 亿元,同比增长 17.15%
> - 2021 年初公司制定的经营目标为"营业总收入两位数增长",实际完成情况符合预期
>
> [来源: 000858_2021_五粮液_2021年年度报告.pdf, p6, 六、主要会计数据和财务指标；p23, (三)2021年经营计划的完成情况]

**SSE 事件分布**:234 个事件(observation:5, thought:221, tool_call:3, tool_result:3, output:1, done:1)


# RAG 文档系统(阶段五)

> 本文件归档:阶段五 RAG 文档系统的设计、实现、验证、GPU 加速与 Chroma HNSW 修复。
> 源自原《项目进度.md》§10。
> 设计文档:[RAG系统设计.md](./RAG系统设计.md)

---

## 一、阶段五概览(2026-08-03 完成)

构建独立的企业年报 RAG 系统,支持上传 PDF → 结构化分块 → 多路召回 → 段落级上下文召回 → 引用溯源回答。LLM 通过 FC 常驻工具 `rag_search` 自主检索。

**简历亮点**:结构化分块(标题层级)+ 多路召回(BM25 + 向量)+ 段落级上下文召回(索引粒度=chunk,返回粒度=最小标题小节)+ 引用溯源。

### 实现清单

**新建 doc-service(Python,8003)**:
- `doc-service/main.py`:7 个端点(/health、/documents/ingest、/documents/scan、/rag/search、/documents、/documents/:id、DELETE /documents/:id)
- `doc-service/parser.py`:pymupdf4llm 解析 PDF → Markdown + 标题层级分块(中文编号/Markdown 标题 + 800/64 滑动窗口二次切 + 表格独立块)+ 文件名元数据解析(股票代码_年份_公司简称_全称.pdf)
- `doc-service/store.py`:MySQL chunks 读写 + BM25 FULLTEXT 检索 + Chroma doc_global 向量检索 + 3:7 归一化融合 + 段落召回(section_id 聚合,超 2000 字 fallback 到 chunk+前后各1)
- `doc-service/reranker.py`:Reranker 接口完整实现,`RERANK_ENABLED=true` + `RERANK_MODEL_PATH` 启用;2026-08-05 已用 bge-reranker-v2-m3 完成 Before/After 评测(见《03-评测体系建设.md》§六),当前默认关闭待调优
- `doc-service/embedder_client.py`:HTTP 调 memory-service /embed_vectors(分批 32),复用 bge-large-zh 不重复加载

**memory-service 扩展**:
- 新增 `POST /embed_vectors`:仅返回向量列表不 upsert Chroma(供 doc-service 调用,避免重复加载 1.5GB 模型)

**Go 主服务(8001)**:
- `internal/rag/client.go`:DocClient(Search/ListDocuments/DeleteDocument/Scan/UploadFile)
- `internal/tools/info/rag_search.go`:rag_search FC 常驻工具 + formatRAGResults(带引用溯源文本 + 1500 字截断保护)
- `internal/api/document_api.go`:uploadDocument/listDocuments/deleteDocument/scanDocuments/ragDebug(转发 doc-service)
- `internal/storage/doc_models.go` + `doc_repo.go`:Document + DocumentChunk GORM 模型 + 仓储层
- 路由:/documents/upload、/documents、/documents/:id、/documents/scan、/rag/debug
- 配置:DOC_SERVICE_URL(默认 http://127.0.0.1:8003)
- rag_search 注册为第 11 个常驻 FC 工具(与 web_search/file_read 同级,注入 system prompt 稳定区)

**前端(static/index.html)**:
- 顶部栏新增 📄 文档管理 + 🔍 检索调试 两个入口
- 文档管理 Modal:上传 PDF + 扫描目录导入 + 文档列表(公司/年份/状态/分块数 + 删除级联)
- 检索调试 Modal:query 输入 + BM25/向量/融合/返回/耗时 5 项统计 + 结果明细
- 引用溯源卡片:rag_search 工具结果自动解析为可展开卡片(文档名+页码+章节+命中路数+相关度+原文)

---

## 二、核心创新:段落级上下文召回

```
query → BM25(chunks FULLTEXT) top-20 + RAG(Chroma 向量) top-20
融合 → top-20 候选(归一化 + 3:7 加权)
[可选] rerank → top-5(RERANK_ENABLED=true 时)
段落扩展:
  for each chunk:
    section_chunks = SELECT * WHERE section_id=? ORDER BY chunk_index
    section_text = 拼接
    if len > 2000: fallback 到 chunk + 前后各 1
去重(同 section_id 取最高分),返回 top_k
```

索引粒度=chunk(精确召回),返回粒度=最小标题小节(语义完整),解决 RAG 经典痛点——单个 chunk 命中但上下文断裂。

---

## 三、验证结果(2026-08-03)

- [x] go build ./... 通过
- [x] go vet ./... 无告警
- [x] doc-service 启动正常(/health 返回 ok,rerank_enabled=false,memory_service=ok,collection=doc_global)
- [x] rag_search 工具注册成功(/tools 返回 12 个工具,rag_search #5)
- [x] 数据表创建(agent_documents + agent_document_chunks + FULLTEXT ft_content ngram 索引)
- [x] PDF 摄入 pipeline 验证(五粮液2021年报:解析 951 chunks 含 OCR + embed + Chroma upsert)
- [x] 文件名元数据自动解析(000858/2021/五粮液)
- [x] page_num 页码解析修复(分布 [105,19,14,38,63] 全不同,原全为 1)
- [x] **段落召回 + fallback**(结果#5 fallback=True,content_len=2208,超 2000 字回退 chunk+前后各1)
- [x] **LLM 自主调用 rag_search**(提问"五粮液2021营收"→ 3 次多轮检索 → 答案 66,209,053,612.11 元)
- [x] **引用溯源**(答案含 `[来源: xxx.pdf, p6, 章节；p23, 章节]`)
- [x] BM25+RAG 双路召回(bm25_count=20, rag_count=20, 耗时 334-387ms)
- [x] rerank 接口预留(RERANK_ENABLED=false 不影响功能)
- [x] 删除级联(DELETE /documents/4 → 844 chunks 删除 + Chroma 清理)
- [x] Go 端 /rag/debug 转发正常
- [x] 本地扫描导入端点验证(POST /documents/scan 创建文档记录)
- [x] 前端文档管理 + 检索调试 + 引用溯源卡片 UI 完成

### 关键约束遵守

- ✅ Go 主体 + Python 辅助;✅ 复用 memory-service embedding 不重复加载模型
- ✅ rag_search 走 FC 常驻(KV Cache 友好,工具列表稳定区);✅ 全局共享 doc_global
- ✅ 段落召回(索引=chunk,返回=小节,超 2000 fallback);✅ rerank 接口预留(不启用)
- ✅ 引用溯源(doc_name+page_num+heading_path + 前端卡片);✅ 删除级联(软删 MySQL + 删 Chroma)
- ✅ 不阻断主流程(rag_search 失败返回错误信息给 LLM);✅ 未改 P1-P4 逻辑;✅ 未做 docker-compose/P0 防护/README

---

## 四、阶段五数据

- 数据库:新增 2 张表(agent_documents + agent_document_chunks),共 11 张表
- 向量库:Chroma 新增 doc_global collection(全局共享,与记忆系统按项目隔离的 collection 分离)
- 已注册工具:13 个常驻 FC(原 5 info + file_write/grep/file_list/file_read/sandbox_exec/summarize_history/ask_user/load_skill + rag_search)+ 2 个 Skill(knovis / kb_summary)
- 服务:doc-service(8003)+ memory-service(8002)+ agent-go(8001)+ Knovis user-api(8080),共 4 个服务 + local-agent 客户端
- 文档库:15 份上市公司年报(5 家公司 × 3 年:五粮液/海康威视/宁德时代/贵州茅台/中国平安)
- 端到端验证:五粮液2021年报(951 chunks)→ 提问营收 → rag_search 3 次调用 → 答案 662.09 亿 + 引用溯源

### 端到端验证详情

**测试问题**:"五粮液 2021 年营业收入是多少?"

**LLM 自主调用 rag_search 3 次**:
1. `rag_search(query="五粮液 2021年 营业收入", top_k=5)`
2. `rag_search(query="五粮液 2021年度 主要会计数据 营业收入 合计 元", top_k=5)`
3. `rag_search(query="2021年年度报告 主要会计数据 营业收入 比上年同期增减 归属于上市公司股东的净利润", top_k=5)`

**最终答案**:五粮液 2021 年营业收入 **66,209,053,612.11 元**(约 662.09 亿,同比增长 15.51%)
**引用溯源**:`[来源: 000858_2021_五粮液_2021年年度报告.pdf, p6, 六、主要会计数据和财务指标；p23, (三)2021年经营计划的完成情况]`
**SSE 事件**:234 个(observation:5, thought:221, tool_call:3, tool_result:3, output:1, done:1)

---

## 五、增强:GPU 加速 + 全量导入 + Chroma HNSW 修复(2026-08-03)

### 5.1 GPU 加速 Embedding

- [memory-service/embedder.py](../memory-service/embedder.py) 新增 `_resolve_device()` 设备解析,支持 `EMBED_DEVICE=auto/cuda/cpu`(默认 auto 自动检测)
- CUDA 可用时自动启用 GPU,batch_size 提升至 128(CPU 模式 32),embedding 速度从 ~1.5 chunks/s 提升至 ~13 chunks/s
- 15 份年报(18801 chunks)全量导入约 30 分钟(CPU 模式需 2-2.5 小时)
- [memory-service/main.py](../memory-service/main.py) `/health` 接口返回 `device` 字段

### 5.2 全量数据导入

- 15 份上市公司年报全量导入完成:600519 贵州茅台(2021-2023)/ 000858 五粮液(2021-2023)/ 002415 海康威视(2021-2023)/ 300750 宁德时代(2021-2023)/ 601318 中国平安(2021-2023)
- 共 18801 chunks,Chroma `doc_global` collection 18801 条向量
- 清理残留向量:对比 Chroma 向量 ID 与 MySQL 有效分块 ID,删除 844 个残留向量(一次性脚本已清理)

### 5.3 Chroma HNSW 检索异常修复(核心)

**问题**:Chroma 1.5.x 改用 Rust 绑定(`RustBindingsAPI`)HNSW 实现后,搜索召回率异常:
- 所有查询结果返回相同 distance(如"贵州茅台 毛利率" top10 全是 0.2271)
- 返回错误文档(doc_id=13 非茅台),错过真正近邻(手动 cosine 计算 top1=0.84 是茅台 doc_id=18)
- 提高n_results=200、重建 collection(M=32, construction_ef=256)均无效

**诊断过程**:
1. 手动 numpy cosine 计算确认向量数据正常(top1 sim=0.8405)
2. 用文档自身向量查询能返回 distance≈0(索引未完全损坏)
3. Chroma 返回的 distance 与手动计算一致(计算逻辑正确),但 HNSW 搜索无法找到真正近邻
4. 根因:Rust HNSW 实现的搜索算法 bug,陷入局部簇

**解决方案**:在 [doc-service/store.py](../doc-service/store.py) 中用 numpy 暴力 cosine 搜索替代 Chroma HNSW:
- 新增 `_load_vec_cache()` / `_invalidate_vec_cache()`:首次查询从 Chroma 加载全部向量到内存缓存(18801×1024 float32 ≈ 73MB,1.5s)
- `rag_search` 改用 `np.argpartition` + 矩阵乘法 dot product(向量已归一化,cosine=dot),单次 < 50ms,召回率 100%
- `upsert_vectors` / `delete_doc_vectors` 调用 `_invalidate_vec_cache()` 失效缓存
- Chroma 仍用于向量存储,仅搜索路径替换

**跨文档检索验证(ALL PASS)**:

| 查询 | 预期公司 | top1 返回 | 结果 |
|---|---|---|---|
| 贵州茅台 营业收入 | 600519 | 600519_2021_贵州茅台 p46 score=0.70 | ✅ |
| 五粮液 营收 | 000858 | 000858_2021_五粮液 p19 score=0.70 | ✅ |
| 海康威视 研发 | 002415 | 002415_2021_海康威视 p15 score=0.70 | ✅ |
| 宁德时代 电池 | 300750 | 300750_2022_宁德时代 p54 score=0.70 | ✅ |

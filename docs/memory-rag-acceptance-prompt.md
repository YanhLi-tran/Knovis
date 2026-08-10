# 任务：检验 Knovis 记忆 RAG 改造结果（验收 agent）

【角色】
你是资深 RAG / 记忆系统工程师，担任**验收方**。本任务是对已完成的改造做**独立检验**——验证改造是否真实生效、是否有隐藏问题、是否引入回归。不要轻信改造报告，用实测数据说话。发现任何问题直接指出。

【项目背景】
Knovis 是 Go 主服务 + Python 子服务的智能助手。memory-service（FastAPI，端口 8002）负责用户记忆的 embedding 与混合检索。Go 主服务（agent-go）每轮对话调用它检索项目记忆注入上下文。

【改造背景（已完成的改造内容）】
原始痛点：
1. BM25 用 MySQL FULLTEXT，中文查询（如"用户喜欢什么"）返回 0 条，融合退化为纯 RAG 单路
2. 检索结果被 keyword 类型（关键词标签）污染
3. score 卡固定值 0.70 无区分度
4. 冷启动首次查询 ~1.1s（embed 首次 429ms）

已做的改造（3 个 git commit，`e9161e3` / `baa895b` / `0a9ced3`）：
1. **memory-service/store.py**：BM25 从 MySQL FULLTEXT 改为 jieba + rank_bm25 全局内存索引（SQL 排除 `memory_type='keyword'`，k1=1.5/b=0.75，upsert/delete 后失效索引惰性重建）；`hybrid_fuse` 加 keyword 自适应降权（有高质量语义命中 top1 rag_raw_score>0.82 时 keyword score×0.1，否则 ×0.6）；融合结果每条加 `rag_raw_score`（RAG 路绝对 cosine）字段
2. **memory-service/main.py**：startup 预加载模型后增加 warmup（主动 embed 一次 forward，把 CUDA 冷启动从用户首次查询挪到启动阶段）；`SearchResult` 模型加 `rag_raw_score` 字段
3. **agent-go/internal/memory/client.go + extractor.go**：`SearchResult` 加 `RAGRawScore`；记忆去重判重从"融合分 score ≥ 0.92"改为"rag_raw_score ≥ 0.92"（融合分受归一化+降权影响，不是绝对相似度）
4. **数据重做**：清空旧记忆，构造实验数据 80 条（project_id=`exp_consumer_research`，fact 25/keyword 20/preference 12/event 10/summary 8/requirement 5）+ 40 条评测 query（`scripts/eval/memory_eval.jsonl`）

【代码位置】
- E:\Knovis\memory-service\store.py（BM25 索引/融合/降权）
- E:\Knovis\memory-service\main.py（API + warmup）
- E:\Knovis\memory-service\.env（DB 配置 + 检索参数）
- E:\Knovis\agent-go\internal\memory\client.go（SearchResult）
- E:\Knovis\agent-go\internal\memory\extractor.go（去重判重）
- E:\Knovis\scripts\eval\memory_eval.py（评测脚本）
- E:\Knovis\scripts\eval\memory_eval.jsonl（40 条评测集）

【数据现状（改造后已确认）】
- MySQL `agent_memories` 中 project_id=`exp_consumer_research`：80 条，全部 embedding_status='done'
- Chroma collection `proj_exp_consumer_research`：80 条向量（MySQL done == Chroma count）
- 评测集 40 条 query，expected_memory_ids 用内容匹配

【改造报告声称的指标（需你验证）】
| 指标 | 改造前 | 声称改造后 |
|---|---|---|
| Recall@5 | 97.5% | 100% |
| Recall@1 | 80.0% | 90.0% |
| MRR | 0.868 | 0.944 |
| P50 延迟 | 59.8ms | 39.0ms |
| top5 keyword 占比 | 23.0% | 1.0% |
| BM25 中文召回 | 0 条 | 20 条候选 |

【验证步骤】

### 步骤 1：代码审查（不启动服务，先读代码）
1. 读 store.py：确认 BM25 索引 SQL 排除了 keyword、`_invalidate_bm25_index` 在 upsert/delete 中被调用、hybrid_fuse 的降权逻辑与 rag_raw_score 透传正确
2. 读 main.py：确认 warmup 在 startup 中触发
3. 读 extractor.go：确认去重用的是 RAGRawScore 而非 Score，且有旧响应缺字段回退逻辑
4. 输出"代码审查发现"：是否有 bug、边界条件遗漏、逻辑错误

### 步骤 2：启动服务验证（memory-service 8002）
```bash
cd E:\Knovis\memory-service
python main.py
```
注意：首次启动会从 HuggingFace 下载 bge-large-zh 模型（约 1.5GB），需要联网；若已缓存则快。观察启动日志确认 warmup 执行（"模型预加载 + warmup 完成"）。

### 步骤 3：检索功能验证
用以下查询测 /search（project_id=`exp_consumer_research`，top_k=5）：
- 中文长 query："贵州茅台2023年营业收入是多少"
- 中文短 query："茅台 营收"
- keyword 类 query："品牌"
- 偏好类 query："用户在对比公司时看重什么"
- 英文 query："What is the revenue"

对每个 query 验证：
1. `bm25_count` > 0（BM25 中文召回已修复）
2. 结果第一条的 `memory_type` 是否合理（fact 类 query 应命中 fact，preference 类应命中 preference）
3. keyword 类型是否被降权（keyword query 之外，keyword 不应占 top5 多数）
4. 每条结果含 `rag_raw_score` 字段（0-1 区间）
5. score 有区分度（不是全卡同一值）

### 步骤 4：评测复现
```bash
cd E:\Knovis
python scripts/eval/memory_eval.py
```
对比输出与改造报告声称的指标。若偏差超过 ±5%，查明原因（是随机性、服务未就绪、还是改造有缺陷）。

### 步骤 5：专项验证（改造声称的修复点）
1. **BM25 中文召回**：改造前"用户喜欢什么"返回 0 条，现在应返回候选（bm25_count>0）
2. **keyword 降权**：跑 40 条评测，top5 keyword 占比应远低于改造前 23%
3. **冷启动**：重启服务后第一次查询，embed 耗时不应再出现 ~400ms（warmup 已把开销挪到启动）
4. **缓存失效**：手动调 /embed 加一条新记忆（或直接操作后），立即 /search 应能命中新内容（验证 _invalidate_bm25_index 生效）；测完删除该测试记忆
5. **多项目隔离**：用不存在的 project_id 查询应返回 0 条（不会串到 exp_consumer_research 项目）

### 步骤 6：Go 端编译验证
```bash
cd E:\Knovis\agent-go
go build ./...
go vet ./...
```
确认 SearchResult 字段同步后编译通过。

【输出要求】
1. 步骤 1-6 每步的验证结果（实测数据，不要引用改造报告的数字）
2. 最终验收结论：
   - ✅ 通过项（附实测数值）
   - ❌ 未通过项（附复现步骤 + 根因分析）
   - ⚠️ 风险项（当前无问题但后续会踩坑的点）
   - 代码审查发现的 bug 或改进建议（按严重程度排序）
3. 特别关注：keyword 降权是否过度（误杀真实信号）、rag_raw_score 去重阈值 0.92 是否仍合理、warmup 是否真的消除了冷启动

【约束】
- 不要修改任何代码/数据（只读验证）；发现问题报告即可
- 不要删除或改动 exp_consumer_research 实验数据
- 评测脚本 memory_eval.py 若发现 bug 可指出，但不要自行修改后宣称通过
- 若服务启动失败或模型下载失败，如实报告环境问题，不要跳过验证

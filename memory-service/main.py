"""Python 记忆子服务（端口 8002）.

提供三个核心能力：
- POST /embed              : 批量文本转向量 + upsert 到 Chroma（bge-large-zh）
- POST /search             : BM25（MySQL FULLTEXT）+ RAG（Chroma）混合检索，3:7 加权融合，top-5
- POST /extract_keywords   : jieba 分词 + TF-IDF 关键词即时提取（每轮异步调）

Chroma 嵌入式单实例，数据存 ./data/chroma/，collection 命名 proj_{project_id}。
"""
import os
import logging
import contextvars
from typing import List, Dict, Any

import uvicorn
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field
from dotenv import load_dotenv
from starlette.middleware.base import BaseHTTPMiddleware

load_dotenv()

# ==================== 全链路 trace_id ====================
# agent-go 通过 X-Trace-Id 头透传 trace_id；中间件存入 contextvar，
# 日志 Filter 自动将其拼入每条日志（无 trace 时显示 "-"），响应头回显便于调用方校验。

_trace_id_var: contextvars.ContextVar[str] = contextvars.ContextVar("trace_id", default="")


class TraceContextMiddleware(BaseHTTPMiddleware):
    """读取 X-Trace-Id 头 → contextvar → 日志 + 响应头回显。"""

    async def dispatch(self, request, call_next):
        trace_id = request.headers.get("X-Trace-Id", "")
        token = _trace_id_var.set(trace_id)
        try:
            response = await call_next(request)
            if trace_id:
                response.headers["X-Trace-Id"] = trace_id
            return response
        finally:
            _trace_id_var.reset(token)


class TraceFilter(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        record.trace_id = _trace_id_var.get() or "-"
        return True


# 日志配置
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s [trace=%(trace_id)s]: %(message)s",
)
logger = logging.getLogger("memory-service")
# TraceFilter 挂到 root handler：任何冒泡到 root 的 record（业务 logger + uvicorn.access 等）
# 都在格式化前补上 trace_id 属性，避免 %(trace_id)s KeyError。
for _handler in logging.getLogger().handlers:
    _handler.addFilter(TraceFilter())

app = FastAPI(title="Agent Memory Service", version="1.0.0")
app.add_middleware(TraceContextMiddleware)


# ==================== 请求/响应模型 ====================

class EmbedItem(BaseModel):
    id: str = Field(..., description="记忆 ID（同 MySQL agent_memories.id，同 Chroma doc id）")
    content: str = Field(..., description="记忆内容")
    memory_type: str = Field(default="", description="类型 fact/preference/summary 等")
    source: str = Field(default="auto_extract", description="来源")
    importance: int = Field(default=50, description="重要度 0-100")


class EmbedRequest(BaseModel):
    project_id: str = Field(..., description="项目 ID")
    items: List[EmbedItem] = Field(..., description="待 embedding 的记忆列表")


class EmbedResponse(BaseModel):
    embedded: int
    ids: List[str]


class SearchRequest(BaseModel):
    project_id: str
    query: str
    top_k: int = Field(default=5, description="融合后返回数量")


class SearchResult(BaseModel):
    id: str
    content: str
    memory_type: str
    source: str
    importance: int
    score: float
    sources: List[str]
    rag_raw_score: float = Field(default=0.0, description="RAG 路绝对 cosine(去重判重用)")


class SearchResponse(BaseModel):
    results: List[SearchResult]
    bm25_count: int
    rag_count: int


class DeleteRequest(BaseModel):
    project_id: str
    ids: List[str]


class ExtractKeywordsRequest(BaseModel):
    texts: List[str] = Field(..., description="文本列表（一轮对话的 user+assistant 等）")
    top_k: int = Field(default=10, description="返回关键词数量")


class Keyword(BaseModel):
    word: str
    weight: float


class ExtractKeywordsResponse(BaseModel):
    keywords: List[Keyword]


# ==================== 路由 ====================

@app.get("/health")
def health():
    """健康检查（含模型加载状态 + 设备信息）。"""
    from embedder import _model, get_model_name, get_device
    return {
        "status": "ok",
        "model": get_model_name(),
        "model_loaded": _model is not None,
        "device": get_device(),
        "chroma_dir": os.getenv("CHROMA_PERSIST_DIR", "./data/chroma"),
    }


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    """批量文本转向量 + upsert 到 Chroma。

    单次批量最大 32 条（防 OOM，由 EMBED_BATCH_SIZE 控制）。
    """
    from embedder import embed as do_embed
    from store import upsert_embeddings

    if not req.items:
        return EmbedResponse(embedded=0, ids=[])

    max_batch = int(os.getenv("EMBED_BATCH_SIZE", "32"))
    if len(req.items) > max_batch:
        raise HTTPException(
            status_code=400,
            detail=f"单次批量上限 {max_batch} 条，收到 {len(req.items)} 条",
        )

    texts = [it.content for it in req.items]
    try:
        vectors = do_embed(texts)
    except Exception as e:
        logger.exception("embedding 生成失败")
        raise HTTPException(status_code=500, detail=f"embedding 生成失败: {e}")

    items_dict = [it.model_dump() for it in req.items]
    try:
        n = upsert_embeddings(req.project_id, items_dict, vectors)
    except Exception as e:
        logger.exception("Chroma upsert 失败")
        raise HTTPException(status_code=500, detail=f"Chroma upsert 失败: {e}")

    return EmbedResponse(embedded=n, ids=[it.id for it in req.items])


@app.post("/search", response_model=SearchResponse)
def search(req: SearchRequest):
    """混合检索：BM25 + RAG，3:7 权重融合，top-k。"""
    from embedder import embed as do_embed
    from store import bm25_search, rag_search, hybrid_fuse

    import time as _t
    _t0 = _t.perf_counter()

    bm25_w = float(os.getenv("BM25_WEIGHT", "0.3"))
    rag_w = float(os.getenv("RAG_WEIGHT", "0.7"))
    recall_n = int(os.getenv("RECALL_TOP_N", "10"))
    top_k = req.top_k if req.top_k > 0 else int(os.getenv("FINAL_TOP_K", "5"))

    # 1) RAG：query → vector，再查 Chroma
    rag_results: List[Dict[str, Any]] = []
    _t1 = _t.perf_counter()
    try:
        qvecs = do_embed([req.query])
        if qvecs:
            rag_results = rag_search(req.project_id, qvecs[0], top_n=recall_n)
    except Exception as e:
        logger.warning("RAG 检索异常（降级仅用 BM25）: %s", e)
    _t2 = _t.perf_counter()
    _t_embed_rag = (_t2 - _t1) * 1000

    # 2) BM25：MySQL FULLTEXT
    bm25_results: List[Dict[str, Any]] = []
    _t3 = _t.perf_counter()
    try:
        bm25_results = bm25_search(req.project_id, req.query, top_n=recall_n)
    except Exception as e:
        logger.warning("BM25 检索异常（降级仅用 RAG）: %s", e)
    _t4 = _t.perf_counter()
    _t_bm25 = (_t4 - _t3) * 1000

    # 3) 融合
    _t5 = _t.perf_counter()
    fused = hybrid_fuse(bm25_results, rag_results, bm25_w, rag_w, top_k)
    results = [SearchResult(**it) for it in fused]
    _t6 = _t.perf_counter()
    _t_fuse = (_t6 - _t5) * 1000
    _t_total = (_t6 - _t0) * 1000
    logger.info(
        "[search] project=%s q=%.20s embed+rag=%.1fms bm25=%.1fms fuse=%.1fms total=%.1fms results=%d bm25_n=%d rag_n=%d",
        req.project_id, req.query, _t_embed_rag, _t_bm25, _t_fuse, _t_total,
        len(results), len(bm25_results), len(rag_results),
    )
    return SearchResponse(
        results=results,
        bm25_count=len(bm25_results),
        rag_count=len(rag_results),
    )


@app.post("/delete")
def delete(req: DeleteRequest):
    """删除 Chroma 中的向量（归档/用户删除记忆时调用）。"""
    from store import delete_embeddings
    n = delete_embeddings(req.project_id, req.ids)
    return {"deleted": n}


@app.post("/extract_keywords", response_model=ExtractKeywordsResponse)
def extract_keywords(req: ExtractKeywordsRequest):
    """关键词即时提取（jieba 分词 + 停用词 + TF-IDF）。

    每轮对话后异步调用，结果存入 agent_memories（memory_type=keyword, importance=30）。
    """
    from keywords import extract_keywords as do_extract

    if not req.texts:
        return ExtractKeywordsResponse(keywords=[])

    top_k = req.top_k if req.top_k > 0 else 10
    try:
        kws = do_extract(req.texts, top_k=top_k)
    except Exception as e:
        logger.exception("关键词提取失败")
        raise HTTPException(status_code=500, detail=f"关键词提取失败: {e}")

    return ExtractKeywordsResponse(
        keywords=[Keyword(**kw) for kw in kws]
    )


@app.delete("/collection/{project_id}")
def delete_collection(project_id: str):
    """删除整个项目 collection（项目删除时）。"""
    from store import delete_collection as _del
    _del(project_id)
    return {"deleted": True}


# ==================== P5: RAG 文档系统支持(纯新增,不改已有逻辑) ====================

class EmbedVectorsRequest(BaseModel):
    """仅返回向量(不 upsert),供 doc-service 检索时获取 query 向量。"""
    texts: List[str] = Field(..., description="待向量化的文本列表")


class EmbedVectorsResponse(BaseModel):
    vectors: List[List[float]]
    dim: int


@app.post("/embed_vectors", response_model=EmbedVectorsResponse)
def embed_vectors(req: EmbedVectorsRequest):
    """纯 embedding 接口:文本 → 向量(不写 Chroma)。

    供 doc-service 复用本服务已加载的 bge-large-zh 模型获取 query 向量,
    避免在 doc-service 重复加载 ~1.5GB 模型。已有 /embed /search /delete 逻辑不变。
    """
    from embedder import embed as do_embed

    if not req.texts:
        return EmbedVectorsResponse(vectors=[], dim=0)

    try:
        vectors = do_embed(req.texts)
    except Exception as e:
        logger.exception("embed_vectors 生成失败")
        raise HTTPException(status_code=500, detail=f"embedding 生成失败: {e}")

    dim = len(vectors[0]) if vectors else 0
    return EmbedVectorsResponse(vectors=vectors, dim=dim)


@app.on_event("startup")
def on_startup():
    """启动时预加载模型 + warmup（避免首次请求慢）.

    get_model() 只加载权重,首次 forward 才触发 CUDA context 初始化(~100ms)
    + cuDNN autotuning(~150ms) + 显存池分配(~50ms),共 ~400ms 冷启动。
    这里主动 embed 一次"warmup",把开销挪到启动阶段。
    """
    if os.getenv("PRELOAD_MODEL", "true").lower() in ("true", "1", "yes"):
        logger.info("启动预加载 embedding 模型 + warmup...")
        try:
            import time as _t
            from embedder import get_model, embed as _do_embed
            get_model()
            _t0 = _t.time()
            _do_embed(["warmup"])
            logger.info("模型预加载 + warmup 完成(耗时 %.1fs)", _t.time() - _t0)
        except Exception as e:
            logger.error("模型预加载失败（将在首次请求时重试）: %s", e)
    else:
        logger.info("跳过模型预加载（PRELOAD_MODEL=false），首次请求时会加载")


if __name__ == "__main__":
    port = int(os.getenv("MEMORY_SERVICE_PORT", "8002"))
    host = os.getenv("MEMORY_SERVICE_HOST", "127.0.0.1")
    logger.info("启动记忆子服务: %s:%d", host, port)
    uvicorn.run(
        "main:app",
        host=host,
        port=port,
        log_level="info",
        # log_config=None：禁用 uvicorn 默认日志配置，让上面 basicConfig 的 [trace=xxx] 格式生效
        log_config=None,
        # workers=1：模型单例，多 worker 会重复加载模型占内存
    )

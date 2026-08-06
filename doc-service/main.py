"""doc-service(Python 文档 RAG 子服务,端口 8003).

端点:
- GET  /health              健康检查(含 rerank + memory-service 状态)
- POST /documents/ingest    上传 PDF → 解析 → 分块 → embed → 入库
- POST /documents/scan      扫描本地目录批量导入
- POST /rag/search          混合检索 + 段落召回(简历核心创新点)
- GET  /documents           文档列表(支持过滤)
- GET  /documents/{doc_id}  文档详情
- DELETE /documents/{doc_id} 级联删除(软删 MySQL + 删 Chroma 向量)

复用 memory-service(8002)的 /embed_vectors 获取向量,不重复加载 bge-large-zh。
"""
import os
import time
import logging
import contextvars
import shutil
from typing import List, Optional, Any

import uvicorn
from fastapi import FastAPI, UploadFile, File, HTTPException, Query, Request
from pydantic import BaseModel, Field
from dotenv import load_dotenv
from starlette.middleware.base import BaseHTTPMiddleware

load_dotenv()

# ==================== 全链路 trace_id ====================
# agent-go 通过 X-Trace-Id 头透传 trace_id；中间件存入 contextvar，
# 日志 Filter 自动拼入每条日志（无 trace 显示 "-"），响应头 X-Trace-Id 回显。

_trace_id_var: contextvars.ContextVar[str] = contextvars.ContextVar("trace_id", default="")


class TraceContextMiddleware(BaseHTTPMiddleware):
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


logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s [trace=%(trace_id)s]: %(message)s",
)
logger = logging.getLogger("doc-service")
# TraceFilter 挂到 root handler：任何冒泡到 root 的 record（业务 logger + uvicorn.access 等）
# 都在格式化前补上 trace_id 属性，避免 %(trace_id)s KeyError。
for _handler in logging.getLogger().handlers:
    _handler.addFilter(TraceFilter())

app = FastAPI(title="Agent Doc Service", version="1.0.0")
app.add_middleware(TraceContextMiddleware)

CHUNK_SIZE = int(os.getenv("RAG_CHUNK_SIZE", "800"))
CHUNK_OVERLAP = int(os.getenv("RAG_CHUNK_OVERLAP", "64"))
BM25_WEIGHT = float(os.getenv("RAG_BM25_WEIGHT", "0.3"))
RAG_WEIGHT = float(os.getenv("RAG_RAG_WEIGHT", "0.7"))
RECALL_TOP_N = int(os.getenv("RAG_RECALL_TOP_N", "20"))
SECTION_MAX_LEN = int(os.getenv("RAG_SECTION_MAX_LEN", "2000"))
UPLOAD_DIR = os.getenv("DOC_UPLOAD_DIR", "./data/uploads")


# ==================== 请求/响应模型 ====================

class RAGSearchRequest(BaseModel):
    query: str = Field(..., description="检索查询")
    top_k: int = Field(default=5, description="返回结果数")
    doc_ids: Optional[List[int]] = Field(default=None, description="可选:限定文档ID范围")
    strategy: str = Field(
        default="fused",
        description="检索策略: fused(默认,融合+段落召回) / bm25_only / rag_only / fused_rerank(强制 rerank)",
    )


class RAGSearchResultItem(BaseModel):
    content: str
    doc_name: str
    page_num: int
    heading_path: List[str]
    section_id: str
    chunk_index: int
    score: float
    sources: List[str]
    fallback: bool
    content_length: int


class CandidateItem(BaseModel):
    """融合前/后的候选条目(段落扩展前),用于评测 Recall@20 / MRR / 多路召回贡献度."""

    chunk_id: int
    document_id: int
    doc_name: str
    page_num: int
    heading_path: List[str]
    section_id: str
    chunk_index: int
    content: str
    score: float
    sources: List[str]
    # RRF 改造后追加:各路原始分,供评测脚本做拒答判断(用 rag_raw_score 而非融合分)
    bm25_raw_score: float = Field(default=0.0, description="BM25 路原始相关性分(未归一化)")
    rag_raw_score: float = Field(default=0.0, description="RAG 路原始 cosine(未归一化,0-1)")


class RAGSearchResponse(BaseModel):
    results: List[RAGSearchResultItem]
    bm25_count: int
    rag_count: int
    fused_count: int
    reranked: bool
    elapsed_ms: int
    # 评测用:融合前完整候选(段落扩展前 top-20)
    fused_candidates: List[CandidateItem] = Field(default_factory=list)
    bm25_candidates: List[CandidateItem] = Field(default_factory=list)
    rag_candidates: List[CandidateItem] = Field(default_factory=list)
    # 评测用:rerank 后的候选(仅 rerank 启用时非空,用于评测 rerank 对排序的影响)
    reranked_candidates: List[CandidateItem] = Field(default_factory=list, description="rerank 后候选(rerank 未启用时为空)")
    # 评测用:分阶段耗时(毫秒),便于定位召回链路性能瓶颈
    embed_ms: int = Field(default=0, description="query 向量化耗时")
    bm25_ms: int = Field(default=0, description="BM25 检索耗时")
    rag_ms: int = Field(default=0, description="向量检索耗时")
    fuse_ms: int = Field(default=0, description="融合耗时")
    rerank_ms: int = Field(default=0, description="rerank 耗长(未启用则为 0)")
    section_ms: int = Field(default=0, description="段落召回耗时")
    total_ms: int = Field(default=0, description="总耗时(= elapsed_ms,冗余字段便于语义清晰)")
    # trace_id:用于跨服务追踪(agent-go → doc-service),非破坏性追加
    trace_id: str = Field(default="", description="trace_id(agent-go 传入,便于跨服务追踪)")


def _to_candidate(c: dict) -> CandidateItem:
    """把检索候选字典转为 CandidateItem(content 截断到 200 字)."""
    content = c.get("content", "") or ""
    if len(content) > 200:
        content = content[:200]
    return CandidateItem(
        chunk_id=int(c.get("chunk_id", 0) or 0),
        document_id=int(c.get("document_id", 0) or 0),
        doc_name=c.get("doc_name", "") or "",
        page_num=int(c.get("page_num", 0) or 0),
        heading_path=c.get("heading_path", []) or [],
        section_id=c.get("section_id", "") or "",
        chunk_index=int(c.get("chunk_index", 0) or 0),
        content=content,
        score=float(c.get("score", 0.0) or 0.0),
        sources=c.get("sources", []) or [],
        bm25_raw_score=float(c.get("bm25_raw_score", 0.0) or 0.0),
        rag_raw_score=float(c.get("rag_raw_score", 0.0) or 0.0),
    )


class ScanRequest(BaseModel):
    dir_path: str = Field(..., description="本地 PDF 目录绝对路径")


# ==================== 健康检查 ====================

@app.get("/health")
def health():
    """健康检查(含 rerank 与 memory-service 状态)."""
    from embedder_client import get_client
    from reranker import get_reranker

    reranker = get_reranker()
    mem_ok = get_client().health()
    return {
        "status": "ok",
        "rerank_enabled": reranker.is_enabled(),
        "memory_service": "ok" if mem_ok else "unreachable",
        "chroma_dir": os.getenv("CHROMA_PERSIST_DIR", "./data/chroma"),
        "collection": "doc_global",
    }


# ==================== 文档摄入 ====================

def _ingest_pdf(pdf_path: str, filename: str, manual_meta: Optional[dict] = None) -> dict:
    """单个 PDF 摄入全流程(供 ingest / scan 复用).

    流程:解析元数据 → 写文档记录(processing)→ PDF 分块 → 写 chunks → embed → upsert Chroma → 标记 ready
    """
    from parser import parse_filename, parse_pdf
    from store import (
        insert_document,
        update_document_status,
        update_document_ready,
        insert_chunks,
        mark_chunks_embedded,
        upsert_vectors,
    )
    from embedder_client import get_client

    # 1) 元数据解析(文件名自动解析,失败用 manual_meta)
    meta = parse_filename(filename)
    company_code = (meta or {}).get("company_code", "")
    company_name = (meta or {}).get("company_name", "")
    report_year = (meta or {}).get("report_year", 0)
    report_type = (meta or {}).get("report_type", "年报")
    if not meta and manual_meta:
        company_code = manual_meta.get("company_code", "")
        company_name = manual_meta.get("company_name", "")
        report_year = int(manual_meta.get("report_year", 0) or 0)

    # 2) 写文档记录(processing)
    file_size = os.path.getsize(pdf_path) if os.path.exists(pdf_path) else 0
    doc_id = insert_document(
        filename=filename,
        file_path=pdf_path,
        file_size=file_size,
        company_code=company_code,
        company_name=company_name,
        report_year=report_year,
        report_type=report_type,
        manual_meta_json=manual_meta,
    )

    try:
        # 3) PDF 解析 + 分块
        chunks, total_pages = parse_pdf(pdf_path, chunk_size=CHUNK_SIZE, overlap=CHUNK_OVERLAP)
        if not chunks:
            update_document_status(doc_id, "failed", "解析后无分块")
            raise RuntimeError("PDF 解析后无分块(可能为空文件或扫描件)")

        # 4) 写 MySQL chunks(拿到 chunk ids)
        chunk_ids = insert_chunks(doc_id, chunks)

        # 5) 调 memory-service /embed_vectors 获取向量
        contents = [c["content"] for c in chunks]
        vectors = get_client().embed_texts(contents)
        if len(vectors) != len(chunk_ids):
            raise RuntimeError(
                f"向量数量({len(vectors)})与 chunks({len(chunk_ids)})不一致"
            )

        # 6) upsert 到 Chroma doc_global
        metas = [
            {
                "page_num": c.get("page_num", 1),
                "chunk_index": c.get("chunk_index", 0),
                "section_id": c.get("section_id", "root"),
            }
            for c in chunks
        ]
        upsert_vectors(chunk_ids, contents, vectors, doc_id, filename, metas)

        # 7) 标记 chunks embedded + 文档 ready
        mark_chunks_embedded(chunk_ids)
        update_document_ready(doc_id, len(chunks), total_pages)

        logger.info("摄入成功 doc=%s filename=%s chunks=%d pages=%d", doc_id, filename, len(chunks), total_pages)
        return {
            "id": doc_id,
            "filename": filename,
            "status": "ready",
            "total_chunks": len(chunks),
            "total_pages": total_pages,
            "company_code": company_code,
            "company_name": company_name,
            "report_year": report_year,
        }
    except Exception as e:
        logger.exception("摄入失败 doc=%s filename=%s", doc_id, filename)
        update_document_status(doc_id, "failed", str(e))
        raise


@app.post("/documents/ingest")
async def ingest(file: UploadFile = File(...)):
    """上传单个 PDF 摄入."""
    if not file.filename or not file.filename.lower().endswith(".pdf"):
        raise HTTPException(status_code=400, detail="仅支持 PDF 文件")

    os.makedirs(UPLOAD_DIR, exist_ok=True)
    save_path = os.path.join(UPLOAD_DIR, file.filename)
    with open(save_path, "wb") as f:
        shutil.copyfileobj(file.file, f)

    try:
        return _ingest_pdf(save_path, file.filename)
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"摄入失败: {e}")


@app.post("/documents/scan")
def scan(req: ScanRequest):
    """扫描本地目录批量导入所有 PDF."""
    dir_path = req.dir_path
    if not os.path.isdir(dir_path):
        raise HTTPException(status_code=400, detail=f"目录不存在: {dir_path}")

    pdf_files = sorted(
        [f for f in os.listdir(dir_path) if f.lower().endswith(".pdf")]
    )
    if not pdf_files:
        return {"total": 0, "success": 0, "failed": 0, "details": []}

    os.makedirs(UPLOAD_DIR, exist_ok=True)
    results = []
    success = 0
    failed = 0
    for fname in pdf_files:
        src = os.path.join(dir_path, fname)
        dst = os.path.join(UPLOAD_DIR, fname)
        try:
            shutil.copy2(src, dst)
            info = _ingest_pdf(dst, fname)
            results.append({**info, "error": None})
            success += 1
        except Exception as e:
            results.append({"filename": fname, "status": "failed", "error": str(e)})
            failed += 1
            logger.error("扫描导入失败 filename=%s: %s", fname, e)

    return {"total": len(pdf_files), "success": success, "failed": failed, "details": results}


# ==================== 文档管理 ====================

@app.get("/documents")
def list_docs(
    status: str = Query(default=""),
    company_code: str = Query(default=""),
):
    """文档列表(支持过滤)."""
    from store import list_documents
    return {"documents": list_documents(status=status, company_code=company_code)}


@app.get("/documents/{doc_id}")
def get_doc(doc_id: int):
    """文档详情(含分块统计)."""
    from store import get_document
    doc = get_document(doc_id)
    if not doc:
        raise HTTPException(status_code=404, detail="文档不存在")
    return doc


@app.delete("/documents/{doc_id}")
def delete_doc(doc_id: int):
    """级联删除:软删文档 + 硬删 chunks + 删 Chroma 向量."""
    from store import delete_document_cascade
    try:
        return delete_document_cascade(doc_id)
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"删除失败: {e}")


# ==================== 检索(简历核心创新点) ====================

@app.post("/rag/search", response_model=RAGSearchResponse)
def rag_search(req: RAGSearchRequest, request: Request):
    """混合检索 + 段落级上下文召回.

    流程:query 向量化 → BM25(chunks FULLTEXT) top-20 + RAG(Chroma) top-20
         → 归一化 3:7 融合 → [可选 rerank] → 段落召回(扩展到最小标题小节)
    """
    from embedder_client import get_client
    from store import bm25_search, rag_search as store_rag, hybrid_fuse, section_recall
    from reranker import get_reranker

    # 接收 trace_id 用于跨服务追踪(agent-go 透传)
    trace_id = request.headers.get("X-Trace-Id", "")

    start = time.perf_counter()
    if not req.query.strip():
        raise HTTPException(status_code=400, detail="query 不能为空")

    top_k = req.top_k if req.top_k > 0 else 5
    doc_ids = req.doc_ids

    # 1) query 向量(复用 memory-service bge-large-zh)
    t0 = time.perf_counter()
    try:
        qvec = get_client().embed_query(req.query)
    except Exception as e:
        raise HTTPException(status_code=503, detail=f"获取 query 向量失败(memory-service 不可用?): {e}")
    embed_ms = int((time.perf_counter() - t0) * 1000)

    # 2) 双路召回
    t0 = time.perf_counter()
    bm25_results = bm25_search(req.query, top_n=RECALL_TOP_N, doc_ids=doc_ids)
    bm25_ms = int((time.perf_counter() - t0) * 1000)

    t0 = time.perf_counter()
    rag_results = store_rag(qvec, top_n=RECALL_TOP_N, doc_ids=doc_ids)
    rag_ms = int((time.perf_counter() - t0) * 1000)

    # 3) 融合
    t0 = time.perf_counter()
    fused = hybrid_fuse(bm25_results, rag_results, BM25_WEIGHT, RAG_WEIGHT, top_n=RECALL_TOP_N)
    fuse_ms = int((time.perf_counter() - t0) * 1000)
    # 保留 rerank 前的融合候选副本(用于评测字段 fused_candidates)
    fused_before_rerank = list(fused)
    reranked_candidates_list = []  # rerank 后候选(用于评测)

    # 4) [可选] rerank
    reranked = False
    rerank_ms = 0
    reranker = get_reranker()
    strategy = (req.strategy or "fused").strip().lower()
    # fused_rerank 策略:rerank 未启用时也标记为 reranked=False(自然降级为 fused)
    if reranker.is_enabled() and fused:
        t0 = time.perf_counter()
        try:
            docs = [c.get("content", "") for c in fused]
            ranked = reranker.rerank(req.query, docs, top_k=min(len(fused), RECALL_TOP_N))
            fused = [fused[r["index"]] for r in ranked]
            for r, f in zip(ranked, fused):
                f["rerank_score"] = r["score"]
            reranked = True
        except Exception as e:
            logger.warning("rerank 失败(降级用融合结果): %s", e)
        rerank_ms = int((time.perf_counter() - t0) * 1000)
        reranked_candidates_list = list(fused)  # 保存 rerank 后的候选

    # 5) 根据 strategy 选择段落召回的输入候选
    #    - bm25_only:仅 BM25 路(测 BM25 召回质量)
    #    - rag_only:仅向量路(测向量召回质量)
    #    - fused_rerank:rerank 后的融合结果(若 rerank 未启用则降级为 fused)
    #    - fused(默认):融合结果(rerank 后若有),保持原有行为
    if strategy == "bm25_only":
        recall_input = bm25_results
    elif strategy == "rag_only":
        recall_input = rag_results
    elif strategy == "fused_rerank":
        recall_input = fused  # rerank 后(若启用)
    else:  # fused(默认)
        recall_input = fused

    # 6) 段落召回(扩展到最小标题小节,超长 fallback)
    t0 = time.perf_counter()
    recalled = section_recall(recall_input, top_k=top_k, max_section_len=SECTION_MAX_LEN)
    section_ms = int((time.perf_counter() - t0) * 1000)

    elapsed_ms = int((time.perf_counter() - start) * 1000)
    items = [RAGSearchResultItem(**r) for r in recalled]
    if trace_id:
        logger.info("[rag/search] trace_id=%s query=%s results=%d bm25=%d rag=%d reranked=%s elapsed=%dms",
                     trace_id, req.query, len(items), len(bm25_results), len(rag_results), reranked, elapsed_ms)
    return RAGSearchResponse(
        results=items,
        bm25_count=len(bm25_results),
        rag_count=len(rag_results),
        fused_count=len(fused),
        reranked=reranked,
        elapsed_ms=elapsed_ms,
        fused_candidates=[_to_candidate(c) for c in fused_before_rerank],
        reranked_candidates=[_to_candidate(c) for c in reranked_candidates_list],
        bm25_candidates=[_to_candidate(c) for c in bm25_results],
        rag_candidates=[_to_candidate(c) for c in rag_results],
        embed_ms=embed_ms,
        bm25_ms=bm25_ms,
        rag_ms=rag_ms,
        fuse_ms=fuse_ms,
        rerank_ms=rerank_ms,
        section_ms=section_ms,
        total_ms=elapsed_ms,
        trace_id=trace_id,
    )


if __name__ == "__main__":
    port = int(os.getenv("DOC_SERVICE_PORT", "8003"))
    host = os.getenv("DOC_SERVICE_HOST", "127.0.0.1")
    logger.info("启动 doc-service: %s:%d", host, port)
    uvicorn.run("main:app", host=host, port=port, log_level="info",
                # log_config=None：禁用 uvicorn 默认日志配置，让上面 basicConfig 的 [trace=xxx] 格式生效
                log_config=None)

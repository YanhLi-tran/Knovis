"""存储与检索:Chroma doc_global + MySQL chunks + BM25 + RAG 融合 + 段落召回(简历核心创新点).

索引粒度=chunk(800字,精确召回),返回粒度=最小标题小节(语义完整);
小节超 RAG_SECTION_MAX_LEN(默认2000)时 fallback 到命中 chunk + 前后各 1 chunk。
解决 RAG 经典痛点:单个 chunk 命中但上下文断裂。
"""
import os
import json
import logging
import time
from typing import List, Dict, Any, Optional, Tuple

import chromadb
import numpy as np
import pymysql
from dbutils.pooled_db import PooledDB
from dotenv import load_dotenv
import jieba
from rank_bm25 import BM25Okapi

load_dotenv()

logger = logging.getLogger("doc-service.store")

# BM25 内存索引参数(可选,有默认值;对齐最优方案 k1=1.5 / b=0.75)
_BM25_K1 = float(os.getenv("BM25_K1", "1.5"))
_BM25_B = float(os.getenv("BM25_B", "0.75"))

# Chroma 持久化目录(doc-service 独立,与 memory-service 隔离)
_CHROMA_DIR = os.getenv("CHROMA_PERSIST_DIR", "./data/chroma")
_chroma_client: Optional[chromadb.api.ClientAPI] = None

# 全局共享 collection 名
DOC_COLLECTION = "doc_global"

# ==================== 向量缓存(暴力搜索,绕过 Chroma HNSW) ====================
# Chroma 1.5.x Rust HNSW 搜索召回率异常(所有结果 distance 相同,错过真正近邻),
# 改用 numpy 暴力 cosine 搜索。18801×1024 float32 ≈ 75MB,单次查询 < 50ms。
_vec_cache_ids: Optional[List[str]] = None          # chunk_id 字符串列表
_vec_cache_matrix: Optional[np.ndarray] = None       # (N, dim) float32, 已归一化
_vec_cache_doc_ids: Optional[np.ndarray] = None      # (N,) int32, document_id per vector


def _get_chroma():
    """获取 Chroma 客户端单例(嵌入式持久化)."""
    global _chroma_client
    if _chroma_client is not None:
        return _chroma_client
    os.makedirs(_CHROMA_DIR, exist_ok=True)
    _chroma_client = chromadb.PersistentClient(path=_CHROMA_DIR)
    logger.info("Chroma 已初始化,持久化目录: %s", _CHROMA_DIR)
    return _chroma_client


def _get_collection():
    """获取全局共享 collection doc_global(cosine 距离).

    HNSW 参数:M=32(默认16,连接数翻倍提升召回),construction_ef=256(默认100,建索引质量更好)。
    仅在首次创建时生效,已存在的 collection 不受影响。
    """
    client = _get_chroma()
    return client.get_or_create_collection(
        name=DOC_COLLECTION,
        metadata={
            "hnsw:space": "cosine",
            "hnsw:M": 32,
            "hnsw:construction_ef": 256,
        },
    )


def _load_vec_cache():
    """从 Chroma 加载全部向量到内存(首次查询时触发,后续复用).

    向量已归一化(bge-large-zh 输出),cosine similarity = dot product。
    """
    global _vec_cache_ids, _vec_cache_matrix, _vec_cache_doc_ids
    if _vec_cache_matrix is not None:
        return
    col = _get_collection()
    total = col.count()
    if total == 0:
        _vec_cache_ids = []
        _vec_cache_matrix = np.zeros((0, 1024), dtype=np.float32)
        _vec_cache_doc_ids = np.zeros((0,), dtype=np.int32)
        return
    t0 = time.time()
    data = col.get(include=["embeddings", "metadatas"], limit=total)
    _vec_cache_ids = list(data["ids"])
    _vec_cache_matrix = np.array(data["embeddings"], dtype=np.float32)
    _vec_cache_doc_ids = np.array(
        [int(m.get("document_id", 0)) for m in data["metadatas"]], dtype=np.int32
    )
    logger.info(
        "向量缓存已加载: %d 条, dim=%d, 耗时 %.2fs, 内存 %.1fMB",
        len(_vec_cache_ids),
        _vec_cache_matrix.shape[1],
        time.time() - t0,
        _vec_cache_matrix.nbytes / 1024 / 1024,
    )


def _invalidate_vec_cache():
    """失效向量缓存(upsert/delete 后调用,下次查询时重新加载).

    级联失效 BM25 内存索引:向量缓存失效意味着数据有变更,BM25 索引也必须重建,
    否则检索到的是旧数据。首次查询时惰性重建(全量 chunks 构建 ~9s,仅一次性开销)。
    """
    global _vec_cache_ids, _vec_cache_matrix, _vec_cache_doc_ids
    _vec_cache_ids = None
    _vec_cache_matrix = None
    _vec_cache_doc_ids = None
    _invalidate_bm25_index()


# ==================== MySQL(连接池,替代每次新建连接) ====================

_mysql_pool: Optional[PooledDB] = None


def _get_mysql_pool() -> PooledDB:
    """获取 MySQL 连接池单例(dbutils PooledDB,省 TCP 握手开销)."""
    global _mysql_pool
    if _mysql_pool is not None:
        return _mysql_pool
    _mysql_pool = PooledDB(
        creator=pymysql,
        mincached=2,
        maxcached=10,
        maxconnections=20,
        blocking=True,
        host=os.getenv("DB_HOST", "127.0.0.1"),
        port=int(os.getenv("DB_PORT", "3306")),
        user=os.getenv("DB_USER", "root"),
        password=os.getenv("DB_PASSWORD", ""),
        database=os.getenv("DB_NAME", "agent_go"),
        charset="utf8mb4",
        cursorclass=pymysql.cursors.DictCursor,
        connect_timeout=5,
        read_timeout=15,
    )
    logger.info("MySQL 连接池已初始化(mincached=2, maxcached=10, maxconnections=20)")
    return _mysql_pool


def _mysql_conn():
    """从连接池获取连接(用完需 close,实际是归还池)."""
    return _get_mysql_pool().connection()


def _parse_heading_path(raw: Any) -> List[str]:
    """heading_path 字段(JSON 字符串)→ list."""
    if not raw:
        return []
    if isinstance(raw, list):
        return raw
    try:
        v = json.loads(raw)
        return v if isinstance(v, list) else []
    except Exception:
        return []


# ==================== 文档元信息 ====================

def get_document(doc_id: int) -> Optional[Dict[str, Any]]:
    """查询单个文档元信息(未软删)."""
    sql = (
        "SELECT id, filename, file_path, file_size, total_pages, total_chunks, "
        "status, company_code, company_name, report_year, report_type, created_at "
        "FROM agent_documents WHERE id = %s AND deleted_at IS NULL"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, (doc_id,))
                return cur.fetchone()
        finally:
            conn.close()
    except Exception as e:
        logger.error("查询文档失败 doc=%s: %s", doc_id, e)
        return None


def list_documents(status: str = "", company_code: str = "", owner_id: str = "") -> List[Dict[str, Any]]:
    """文档列表(支持过滤). owner_id 非空时只返回全局共享+该用户私有文档."""
    sql = (
        "SELECT id, filename, file_size, total_pages, total_chunks, status, "
        "company_code, company_name, report_year, report_type, error_msg, created_at, owner_id "
        "FROM agent_documents WHERE deleted_at IS NULL"
    )
    args: List[Any] = []
    if status:
        sql += " AND status = %s"
        args.append(status)
    if company_code:
        sql += " AND company_code = %s"
        args.append(company_code)
    if owner_id:
        # 全局共享(owner_id IS NULL) + 用户私有(owner_id = ?)
        sql += " AND (owner_id IS NULL OR owner_id = %s)"
        args.append(owner_id)
    sql += " ORDER BY created_at DESC"
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, args)
                return cur.fetchall()
        finally:
            conn.close()
    except Exception as e:
        logger.error("查询文档列表失败: %s", e)
        return []


def get_visible_doc_ids(owner_id: str) -> List[int]:
    """查询用户可见的文档ID列表(全局共享 + 该用户私有),用于检索时 doc_ids 过滤."""
    sql = "SELECT id FROM agent_documents WHERE deleted_at IS NULL AND status = 'ready'"
    args: List[Any] = []
    if owner_id:
        sql += " AND (owner_id IS NULL OR owner_id = %s)"
        args.append(owner_id)
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, args)
                return [int(r["id"]) for r in cur.fetchall()]
        finally:
            conn.close()
    except Exception as e:
        logger.error("查询可见文档ID失败 owner=%s: %s", owner_id, e)
        return []


def update_document_status(doc_id: int, status: str, err_msg: str = "") -> None:
    """更新文档状态."""
    sql = "UPDATE agent_documents SET status = %s, error_msg = %s WHERE id = %s"
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, (status, err_msg, doc_id))
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        logger.error("更新文档状态失败 doc=%s: %s", doc_id, e)


def insert_document(
    filename: str,
    file_path: str,
    file_size: int,
    company_code: str = "",
    company_name: str = "",
    report_year: int = 0,
    report_type: str = "年报",
    manual_meta_json: Optional[dict] = None,
    owner_id: str = "",
) -> int:
    """写入文档元信息记录(status=processing),返回 doc_id. owner_id 空=全局共享."""
    import json as _json

    manual_meta_str = _json.dumps(manual_meta_json, ensure_ascii=False) if manual_meta_json else None
    # owner_id 空字符串转 None(MySQL 存 NULL=全局共享)
    owner = owner_id if owner_id else None
    sql = (
        "INSERT INTO agent_documents "
        "(filename, file_path, file_size, status, company_code, company_name, report_year, report_type, manual_meta, owner_id) "
        "VALUES (%s, %s, %s, 'processing', %s, %s, %s, %s, %s, %s)"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(
                    sql,
                    (filename, file_path, file_size, company_code, company_name, report_year, report_type, manual_meta_str, owner),
                )
            conn.commit()
            return cur.lastrowid
        finally:
            conn.close()
    except Exception as e:
        logger.error("写入文档记录失败 filename=%s: %s", filename, e)
        raise


def update_document_ready(doc_id: int, total_chunks: int, total_pages: int) -> None:
    """摄入完成:更新为 ready + 分块数 + 页数."""
    sql = (
        "UPDATE agent_documents SET status='ready', total_chunks=%s, total_pages=%s, error_msg='' "
        "WHERE id = %s"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, (total_chunks, total_pages, doc_id))
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        logger.error("更新文档 ready 失败 doc=%s: %s", doc_id, e)


# ==================== 分块写入 ====================

def insert_chunks(doc_id: int, chunks: List[Dict[str, Any]]) -> List[int]:
    """批量写入 chunks 到 MySQL,返回 chunk id 列表."""
    if not chunks:
        return []
    sql = (
        "INSERT INTO agent_document_chunks "
        "(document_id, chunk_index, page_num, heading_path, section_id, content, content_length, chunk_type, embedding_status) "
        "VALUES (%s, %s, %s, %s, %s, %s, %s, %s, 'pending')"
    )
    ids: List[int] = []
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                for c in chunks:
                    heading_json = json.dumps(c.get("heading_path", []), ensure_ascii=False)
                    cur.execute(
                        sql,
                        (
                            doc_id,
                            c["chunk_index"],
                            c.get("page_num", 1),
                            heading_json,
                            c.get("section_id", "root"),
                            c["content"],
                            c.get("content_length", len(c["content"])),
                            c.get("chunk_type", "text"),
                        ),
                    )
                    ids.append(cur.lastrowid)
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        logger.error("写入 chunks 失败 doc=%s: %s", doc_id, e)
        raise
    return ids


def mark_chunks_embedded(chunk_ids: List[int]) -> None:
    """标记 chunks 向量状态为 done."""
    if not chunk_ids:
        return
    sql = "UPDATE agent_document_chunks SET embedding_status='done' WHERE id IN (%s)" % ",".join(
        ["%s"] * len(chunk_ids)
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, chunk_ids)
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        logger.error("标记 chunks embedded 失败: %s", e)


# ==================== Chroma 向量 upsert / 删除 ====================

def upsert_vectors(
    chunk_ids: List[int],
    contents: List[str],
    vectors: List[List[float]],
    doc_id: int,
    doc_name: str,
    metas: List[Dict[str, Any]],
) -> int:
    """upsert 向量到 doc_global collection.

    metas: 每个 chunk 的 {page_num, chunk_index, section_id}
    Chroma metadata 仅存可过滤字段(document_id/page_num/chunk_index),完整元数据从 MySQL 查。
    """
    if not chunk_ids or not vectors:
        return 0
    if len(chunk_ids) != len(vectors):
        raise ValueError(f"chunk_ids({len(chunk_ids)}) 与 vectors({len(vectors)}) 数量不一致")

    col = _get_collection()
    ids = [str(cid) for cid in chunk_ids]
    chroma_metas = []
    for cid, m in zip(chunk_ids, metas):
        chroma_metas.append(
            {
                "document_id": int(doc_id),
                "chunk_id": int(cid),
                "chunk_index": int(m.get("chunk_index", 0)),
                "page_num": int(m.get("page_num", 1)),
                "doc_name": str(doc_name),
            }
        )
    col.upsert(ids=ids, embeddings=vectors, documents=contents, metadatas=chroma_metas)
    _invalidate_vec_cache()
    logger.info("upsert %d 条向量到 collection %s (doc=%s)", len(ids), DOC_COLLECTION, doc_id)
    return len(ids)


def delete_doc_vectors(doc_id: int) -> int:
    """删除某文档在 Chroma 的全部分量(按 metadata where document_id)."""
    col = _get_collection()
    try:
        # 先查数量
        peered = col.get(where={"document_id": int(doc_id)}, include=[])
        n = len(peered.get("ids", [])) if peered else 0
        col.delete(where={"document_id": int(doc_id)})
        _invalidate_vec_cache()
        logger.info("从 Chroma 删除 doc=%s 的 %d 条向量", doc_id, n)
        return n
    except Exception as e:
        logger.warning("从 Chroma 删除文档向量失败 doc=%s: %s", doc_id, e)
        return 0


# ==================== 检索:BM25(内存索引) + RAG + 融合 + 段落召回 ====================

def _jieba_tokenize(text: str) -> List[str]:
    """jieba 精确模式分词 + 过滤空白和单字符标点.

    MySQL FULLTEXT 对中文分词基本失效(整段当一个词),弃用 MATCH...AGAINST,
    改用 jieba 分词 + rank_bm25 真 BM25(词频饱和 + 文档长度归一化)。
    """
    if not text:
        return []
    tokens = []
    for tok in jieba.lcut(text, cut_all=False):
        tok = tok.strip().lower()
        if not tok:
            continue
        if len(tok) == 1 and not tok.isalnum():
            continue
        tokens.append(tok)
    return tokens


class _BM25Index:
    """内存 BM25 索引(单例,惰性构建).

    从 MySQL 加载全量 embedding_status='done' 的 chunks,jieba 分词后构建 BM25Okapi。
    数据变更(upsert/delete)后由 _invalidate_bm25_index 置空,下次查询重建。
    """

    def __init__(self):
        self._chunks: List[Dict[str, Any]] = []
        self._tokenized: List[List[str]] = []
        self._bm25: Optional[BM25Okapi] = None
        self._build()

    def _build(self):
        sql = (
            "SELECT c.id AS chunk_id, c.document_id, c.chunk_index, c.page_num, "
            "c.heading_path, c.section_id, c.content, c.chunk_type, "
            "d.filename AS doc_name "
            "FROM agent_document_chunks c "
            "JOIN agent_documents d ON c.document_id = d.id "
            "WHERE d.deleted_at IS NULL AND c.embedding_status = 'done' "
            "ORDER BY c.id ASC"
        )
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql)
                rows = cur.fetchall()
        finally:
            conn.close()
        if not rows:
            return
        t0 = time.time()
        for r in rows:
            r["heading_path"] = _parse_heading_path(r.get("heading_path"))
            r["sources"] = ["bm25"]
            self._chunks.append(r)
            self._tokenized.append(_jieba_tokenize(r.get("content", "")))
        self._bm25 = BM25Okapi(self._tokenized, k1=_BM25_K1, b=_BM25_B)
        logger.info("BM25 内存索引构建完成: %d chunks, 耗时 %.2fs", len(self._chunks), time.time() - t0)

    def search(self, query: str, top_n: int = 20, doc_ids: Optional[List[int]] = None) -> List[Dict[str, Any]]:
        if not self._bm25 or not self._chunks:
            return []
        query_tokens = _jieba_tokenize(query)
        if not query_tokens:
            return []
        scores = self._bm25.get_scores(query_tokens)
        doc_id_set = set(int(d) for d in doc_ids) if doc_ids else None
        n = min(top_n, len(scores))
        if n <= 0:
            return []
        if doc_id_set is not None:
            mask = np.array(
                [int(c["document_id"]) in doc_id_set for c in self._chunks], dtype=bool
            )
            scores = np.where(mask, scores, -np.inf)
        if n < len(scores):
            top_idx = np.argpartition(scores, -n)[-n:]
        else:
            top_idx = np.arange(len(scores))
        top_idx = top_idx[np.argsort(scores[top_idx])[::-1]]
        out: List[Dict[str, Any]] = []
        for idx in top_idx:
            score = float(scores[idx])
            if score <= 0:
                continue
            c = self._chunks[idx]
            out.append(
                {
                    "chunk_id": c["chunk_id"],
                    "document_id": c["document_id"],
                    "chunk_index": c["chunk_index"],
                    "page_num": c["page_num"],
                    "heading_path": list(c["heading_path"]),
                    "section_id": c["section_id"],
                    "content": c["content"],
                    "chunk_type": c["chunk_type"],
                    "doc_name": c["doc_name"],
                    "score": score,
                    "sources": ["bm25"],
                }
            )
            if len(out) >= top_n:
                break
        return out


_bm25_index: Optional[_BM25Index] = None


def _get_bm25_index() -> _BM25Index:
    """获取内存 BM25 索引(惰性单例)."""
    global _bm25_index
    if _bm25_index is None:
        _bm25_index = _BM25Index()
    return _bm25_index


def _invalidate_bm25_index():
    """失效 BM25 内存索引(数据变更后置空,下次查询重建)."""
    global _bm25_index
    _bm25_index = None


def bm25_search(query: str, top_n: int = 20, doc_ids: Optional[List[int]] = None) -> List[Dict[str, Any]]:
    """内存 BM25 检索(替代 MySQL FULLTEXT,真 BM25 + 中文分词).

    返回字段:chunk_id, document_id, chunk_index, page_num, heading_path, section_id,
             content, chunk_type, doc_name, score
    """
    if not query.strip():
        return []
    try:
        return _get_bm25_index().search(query, top_n=top_n, doc_ids=doc_ids)
    except Exception as e:
        logger.error("BM25 检索失败: %s", e)
        return []


def rag_search(
    query_vector: List[float],
    top_n: int = 20,
    doc_ids: Optional[List[int]] = None,
) -> List[Dict[str, Any]]:
    """暴力 cosine 向量检索(绕过 Chroma HNSW,召回率 100%).

    Chroma 1.5.x Rust HNSW 搜索召回率异常,改用 numpy 暴力 dot product。
    向量已归一化(bge-large-zh),cosine similarity = dot product。
    """
    _load_vec_cache()
    if _vec_cache_matrix is None or len(_vec_cache_ids) == 0:
        return []

    qvec = np.array(query_vector, dtype=np.float32)
    # cosine similarity = dot product(向量已归一化)
    sims = _vec_cache_matrix @ qvec  # (N,)

    # doc_ids 过滤:非目标文档的相似度置 -1
    if doc_ids:
        doc_id_set = set(int(d) for d in doc_ids)
        mask = np.array(
            [d in doc_id_set for d in _vec_cache_doc_ids], dtype=bool
        )
        sims = np.where(mask, sims, -1.0)

    # top_n(用 argpartition 快速选 top_n,再排序)
    n = min(top_n, len(sims))
    if n <= 0:
        return []
    if n < len(sims):
        top_idx = np.argpartition(sims, -n)[-n:]
    else:
        top_idx = np.arange(len(sims))
    # 按相似度降序
    top_idx = top_idx[np.argsort(sims[top_idx])[::-1]]

    chunk_ids: List[int] = []
    sims_list: List[float] = []
    for idx in top_idx:
        sim = float(sims[idx])
        if sim < 0:  # 被 doc_ids 过滤掉的
            continue
        chunk_ids.append(int(_vec_cache_ids[idx]))
        sims_list.append(max(0.0, min(1.0, sim)))

    if not chunk_ids:
        return []

    # 回 MySQL 补完整元数据
    enriched = _fetch_chunks_by_ids(chunk_ids)
    by_id = {c["chunk_id"]: c for c in enriched}
    out: List[Dict[str, Any]] = []
    for cid, sim in zip(chunk_ids, sims_list):
        c = by_id.get(cid)
        if not c:
            continue
        c["score"] = sim
        c["sources"] = ["rag"]
        out.append(c)
    return out


def _fetch_chunks_by_ids(chunk_ids: List[int]) -> List[Dict[str, Any]]:
    """按 chunk id 批量查 MySQL 完整元数据(含 doc_name)."""
    if not chunk_ids:
        return []
    placeholders = ",".join(["%s"] * len(chunk_ids))
    sql = (
        "SELECT c.id AS chunk_id, c.document_id, c.chunk_index, c.page_num, c.heading_path, "
        "c.section_id, c.content, c.chunk_type, d.filename AS doc_name "
        "FROM agent_document_chunks c "
        "JOIN agent_documents d ON c.document_id = d.id "
        f"WHERE c.id IN ({placeholders}) AND d.deleted_at IS NULL"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, chunk_ids)
                rows = cur.fetchall()
        finally:
            conn.close()
    except Exception as e:
        logger.error("查询 chunks 元数据失败: %s", e)
        return []
    for r in rows:
        r["heading_path"] = _parse_heading_path(r.get("heading_path"))
    return rows


def _fetch_section_chunks(section_id: str) -> List[Dict[str, Any]]:
    """查询同 section_id 的全部分块(按 chunk_index 排序,段落召回用)."""
    if not section_id:
        return []
    sql = (
        "SELECT id, chunk_index, content, page_num "
        "FROM agent_document_chunks WHERE section_id = %s "
        "ORDER BY chunk_index ASC"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, (section_id,))
                rows = cur.fetchall()
        finally:
            conn.close()
    except Exception as e:
        logger.error("查询 section chunks 失败 sid=%s: %s", section_id, e)
        return []
    return rows


def _normalize_scores(items: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """分数 min-max 归一化到 [0,1](向后兼容,RRF 模式下仅用于诊断字段)."""
    if not items:
        return items
    scores = [float(it.get("score", 0)) for it in items]
    lo, hi = min(scores), max(scores)
    rng = hi - lo
    for it in items:
        s = float(it.get("score", 0))
        it["norm_score"] = (s - lo) / rng if rng > 0 else 1.0
    return items


# RRF 常数(业界标准,来自 Cormack 2009 论文,平衡 top-1 与尾部贡献)
RRF_K = 60
# 混合策略分界点:top-N 之内用 RRF 排序(精排),top-N 之外用加权融合排序(召回广度)
HYBRID_RRF_TOPN = 5


def hybrid_fuse(
    bm25_results: List[Dict[str, Any]],
    rag_results: List[Dict[str, Any]],
    bm25_weight: float,
    rag_weight: float,
    top_n: int = 20,
) -> List[Dict[str, Any]]:
    """混合融合策略:top-5 RRF 精排 + top-20 加权融合召回.

    设计动机(2026-08-04 实验数据):
      - 纯 RRF:Recall@5 +2.2% 但 Recall@20 -6.2%(cross_doc -11.9%),因 RRF 对单路命中 chunk 惩罚过大
      - 纯加权融合:Recall@20=96% 但 top1_score 卡固定 0.7(归一化缺陷)
      - 混合策略:top-5 用 RRF 排序(精排,提升 Precision@5) + top-20 用加权融合排序(召回广度)
                 拒答判断用 rag_raw_score(绝对 cosine,不依赖融合方式)

    算法:
      1. 同时计算每条候选的 weighted_score(min-max 归一化 + 加权)和 rrf_score
      2. 按 weighted_score 降序取 top_n(保证召回广度)
      3. 对 top_n 候选,按 rrf_score 降序取前 HYBRID_RRF_TOPN(5) 个作为精排 top-5
      4. 最终顺序 = [rrf top-5] + [剩余 top_n-5 个按 weighted_score 降序]
      5. score 字段统一用 weighted_score(保持量纲一致,供评测脚本判定 Recall@20)
      6. 追加 rrf_score 字段(供评测/调试分析 top-5 排序依据)

    每条含:chunk_id, document_id, ..., score(weighted), rrf_score,
           sources, bm25_rank, rag_rank, bm25_raw_score, rag_raw_score
    """
    # 各路按 score 降序排(确保 rank 正确)
    bm25_sorted = sorted(bm25_results, key=lambda x: float(x.get("score", 0)), reverse=True)
    rag_sorted = sorted(rag_results, key=lambda x: float(x.get("score", 0)), reverse=True)

    # 构建 chunk_id -> (rank, raw_score) 映射(1-based rank)
    bm25_rank_map: Dict[int, Tuple[int, float]] = {
        it["chunk_id"]: (i + 1, float(it.get("score", 0)))
        for i, it in enumerate(bm25_sorted)
    }
    rag_rank_map: Dict[int, Tuple[int, float]] = {
        it["chunk_id"]: (i + 1, float(it.get("score", 0)))
        for i, it in enumerate(rag_sorted)
    }

    # min-max 归一化(用于 weighted_score)
    _normalize_scores(bm25_sorted)
    _normalize_scores(rag_sorted)
    bm25_norm_map = {it["chunk_id"]: float(it.get("norm_score", 0.0)) for it in bm25_sorted}
    rag_norm_map = {it["chunk_id"]: float(it.get("norm_score", 0.0)) for it in rag_sorted}

    all_cids = set(bm25_rank_map.keys()) | set(rag_rank_map.keys())
    # 取原始字段(优先用 rag 路的完整元数据,因 rag_search 已回 MySQL 补全)
    cid_to_item: Dict[int, Dict[str, Any]] = {}
    for it in rag_sorted:
        cid_to_item[it["chunk_id"]] = dict(it)
    for it in bm25_sorted:
        cid = it["chunk_id"]
        if cid not in cid_to_item:
            cid_to_item[cid] = dict(it)
        else:
            if not cid_to_item[cid].get("content"):
                cid_to_item[cid]["content"] = it.get("content", "")

    # 同时计算 weighted_score 和 rrf_score
    results: List[Dict[str, Any]] = []
    for cid in all_cids:
        base = cid_to_item.get(cid, {})
        bm25_info = bm25_rank_map.get(cid)
        rag_info = rag_rank_map.get(cid)
        bm25_rank = bm25_info[0] if bm25_info else None
        bm25_raw = bm25_info[1] if bm25_info else 0.0
        rag_rank = rag_info[0] if rag_info else None
        rag_raw = rag_info[1] if rag_info else 0.0

        sources = []
        rrf = 0.0
        if bm25_rank is not None:
            rrf += bm25_weight * (1.0 / (RRF_K + bm25_rank))
            sources.append("bm25")
        if rag_rank is not None:
            rrf += rag_weight * (1.0 / (RRF_K + rag_rank))
            sources.append("rag")

        # weighted_score: 归一化加权(保留召回广度)
        weighted = bm25_weight * bm25_norm_map.get(cid, 0.0) + rag_weight * rag_norm_map.get(cid, 0.0)

        item = {
            **base,
            "chunk_id": cid,
            "score": weighted,  # 主分数字段:加权融合分(保证 Recall@20 判定正确)
            "rrf_score": rrf,   # RRF 分(供 top-5 精排 + 调试)
            "sources": sources,
            "bm25_rank": bm25_rank,
            "rag_rank": rag_rank,
            "bm25_raw_score": bm25_raw,
            "rag_raw_score": rag_raw,
            # 向后兼容字段
            "bm25_score": bm25_norm_map.get(cid, 0.0),
            "rag_score": rag_norm_map.get(cid, 0.0),
        }
        results.append(item)

    # Step 1: 按 weighted_score 降序取 top_n(保证召回广度)
    results.sort(key=lambda x: x["score"], reverse=True)
    topn_pool = results[:top_n]

    # Step 2: 对 top_n 候选按 rrf_score 降序取前 HYBRID_RRF_TOPN(精排 top-5)
    rrf_top = sorted(topn_pool, key=lambda x: x["rrf_score"], reverse=True)[:HYBRID_RRF_TOPN]
    rrf_top_ids = {it["chunk_id"] for it in rrf_top}

    # Step 3: 剩余候选按 weighted_score 降序排列
    rest = [it for it in topn_pool if it["chunk_id"] not in rrf_top_ids]
    rest.sort(key=lambda x: x["score"], reverse=True)

    # Step 4: 最终顺序 = [rrf top-5] + [rest by weighted]
    # 注:score 字段保持 weighted 值(量纲一致),top-5 的排序依据是 rrf_score
    return rrf_top + rest


def section_recall(
    candidates: List[Dict[str, Any]],
    top_k: int = 5,
    max_section_len: int = 2000,
) -> List[Dict[str, Any]]:
    """段落级上下文召回(简历核心创新点).

    索引粒度=chunk(精确召回),返回粒度=最小标题小节(语义完整)。
    小节超 max_section_len 时 fallback 到命中 chunk + 同 section 前后各 1 chunk。
    同 section_id 去重(取最高分),返回 top_k。
    """
    if not candidates:
        return []

    # 1) 按 section_id 去重,取最高分代表
    section_best: Dict[str, Dict[str, Any]] = {}
    for c in candidates:
        sid = c.get("section_id") or "root"
        cur_best = section_best.get(sid)
        if cur_best is None or c["score"] > cur_best["score"]:
            section_best[sid] = c

    # 2) 对每个 section 代表做段落扩展
    results: List[Dict[str, Any]] = []
    for sid, best in section_best.items():
        content, used_fallback = _expand_section(best, max_section_len)
        results.append(
            {
                "content": content,
                "doc_name": best.get("doc_name", ""),
                "page_num": best.get("page_num", 1),
                "heading_path": best.get("heading_path", []),
                "section_id": sid,
                "chunk_index": best.get("chunk_index", 0),
                "score": best["score"],
                "sources": best.get("sources", []),
                "fallback": used_fallback,
                "content_length": len(content),
                "rerank_score": best.get("rerank_score", 0.0),
            }
        )

    # 3) 按融合分数排序取 top_k
    results.sort(key=lambda x: x["score"], reverse=True)
    return results[:top_k]


def _expand_section(chunk: Dict[str, Any], max_section_len: int) -> Tuple[str, bool]:
    """扩展到整个小节;超长 fallback 到同 section 前后各 1 chunk.

    Returns: (扩展后文本, 是否 fallback)
    """
    sid = chunk.get("section_id") or "root"
    chunk_id = chunk.get("chunk_id") or chunk.get("id")
    section_chunks = _fetch_section_chunks(sid)
    if not section_chunks:
        return chunk.get("content", ""), False

    section_text = "\n".join(c["content"] for c in section_chunks)
    if len(section_text) <= max_section_len:
        return section_text, False

    # fallback:找到当前 chunk 在 section 中的位置,取前后各 1
    target_idx = None
    for i, c in enumerate(section_chunks):
        if c["id"] == chunk_id:
            target_idx = i
            break
    if target_idx is None:
        # 按 chunk_index 匹配
        for i, c in enumerate(section_chunks):
            if c["chunk_index"] == chunk.get("chunk_index"):
                target_idx = i
                break
    if target_idx is None:
        return chunk.get("content", ""), True

    start = max(0, target_idx - 1)
    end = min(len(section_chunks), target_idx + 2)
    fallback_text = "\n".join(c["content"] for c in section_chunks[start:end])
    return fallback_text, True


# ==================== 删除级联(文档) ====================

def delete_document_cascade(doc_id: int) -> Dict[str, Any]:
    """删除文档:软删 agent_documents + 硬删 agent_document_chunks + 删 Chroma 向量(事务).

    Returns: {deleted: True, chunks: N, vectors: M}
    """
    # 1) 先统计 chunks 数量
    chunk_count = 0
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute("SELECT COUNT(*) AS n FROM agent_document_chunks WHERE document_id = %s", (doc_id,))
                row = cur.fetchone()
                chunk_count = int(row["n"]) if row else 0
        finally:
            conn.close()
    except Exception:
        pass

    # 2) 事务:软删文档 + 硬删 chunks
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute("UPDATE agent_documents SET deleted_at = NOW() WHERE id = %s", (doc_id,))
                cur.execute("DELETE FROM agent_document_chunks WHERE document_id = %s", (doc_id,))
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        logger.error("删除文档级联失败 doc=%s: %s", doc_id, e)
        raise

    # 3) 删 Chroma 向量(事务外,失败不回滚 MySQL:约束允许最终一致)
    vec_deleted = delete_doc_vectors(doc_id)

    logger.info("删除文档级联完成 doc=%s chunks=%s vectors=%s", doc_id, chunk_count, vec_deleted)
    return {"deleted": True, "chunks": chunk_count, "vectors": vec_deleted}

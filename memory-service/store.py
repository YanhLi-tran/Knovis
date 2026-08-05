"""存储与检索封装：Chroma 向量库 + MySQL BM25 + 混合融合."""
import os
import logging
from typing import List, Dict, Any, Optional

import chromadb
import pymysql
from dotenv import load_dotenv

load_dotenv()

logger = logging.getLogger("memory-service.store")

# Chroma 持久化目录
_CHROMA_DIR = os.getenv("CHROMA_PERSIST_DIR", "./data/chroma")
_chroma_client: Optional[chromadb.api.ClientAPI] = None


def _get_chroma():
    """获取 Chroma 客户端单例（嵌入式持久化）。"""
    global _chroma_client
    if _chroma_client is not None:
        return _chroma_client
    os.makedirs(_CHROMA_DIR, exist_ok=True)
    _chroma_client = chromadb.PersistentClient(path=_CHROMA_DIR)
    logger.info("Chroma 已初始化，持久化目录: %s", _CHROMA_DIR)
    return _chroma_client


def _collection_name(project_id: str) -> str:
    """collection 命名规则：proj_{project_id}（Chroma 要求符合 [a-zA-Z0-9_-]+）。"""
    safe = "".join(c if (c.isalnum() or c in "-_") else "_" for c in project_id)
    return f"proj_{safe}"


def get_or_create_collection(project_id: str):
    """获取或创建某项目的 collection（按项目隔离向量）。"""
    client = _get_chroma()
    name = _collection_name(project_id)
    # cosine 距离（bge 用归一化向量，cosine 等价于点积）
    return client.get_or_create_collection(
        name=name,
        metadata={"hnsw:space": "cosine"},
    )


def upsert_embeddings(
    project_id: str,
    items: List[Dict[str, Any]],
    vectors: List[List[float]],
) -> int:
    """批量 upsert 向量到 Chroma（id+content+metadata）。"""
    if not items or not vectors:
        return 0
    if len(items) != len(vectors):
        raise ValueError(f"items({len(items)}) 与 vectors({len(vectors)}) 数量不一致")

    col = get_or_create_collection(project_id)
    ids = [str(it["id"]) for it in items]
    contents = [str(it.get("content", "")) for it in items]
    metadatas = []
    for it in items:
        meta = {
            "memory_type": str(it.get("memory_type", "")),
            "source": str(it.get("source", "")),
            "importance": int(it.get("importance", 50)),
            "project_id": str(project_id),
        }
        metadatas.append(meta)

    # Chroma upsert 幂等（按 id 覆盖）
    col.upsert(ids=ids, embeddings=vectors, documents=contents, metadatas=metadatas)
    return len(ids)


def delete_embeddings(project_id: str, ids: List[str]) -> int:
    """从 Chroma 删除向量（归档时调用）。"""
    if not ids:
        return 0
    try:
        col = get_or_create_collection(project_id)
        col.delete(ids=ids)
        return len(ids)
    except Exception as e:
        logger.warning("从 Chroma 删除向量失败 project=%s: %s", project_id, e)
        return 0


def delete_collection(project_id: str) -> None:
    """删除整个 collection（项目删除时）。"""
    client = _get_chroma()
    name = _collection_name(project_id)
    try:
        client.delete_collection(name=name)
        logger.info("已删除 collection: %s", name)
    except Exception as e:
        logger.warning("删除 collection %s 失败: %s", name, e)


# ==================== MySQL BM25 ====================

def _mysql_conn():
    """建立 MySQL 连接（每次检索新建，连接池交由 pymysql 管理）。"""
    return pymysql.connect(
        host=os.getenv("DB_HOST", "127.0.0.1"),
        port=int(os.getenv("DB_PORT", "3306")),
        user=os.getenv("DB_USER", "root"),
        password=os.getenv("DB_PASSWORD", ""),
        database=os.getenv("DB_NAME", "agent_go"),
        charset="utf8mb4",
        cursorclass=pymysql.cursors.DictCursor,
        connect_timeout=5,
        read_timeout=10,
    )


def bm25_search(project_id: str, query: str, top_n: int = 10) -> List[Dict[str, Any]]:
    """MySQL FULLTEXT 检索（ngram 解析器支持中文）。

    返回字段：id, content, memory_type, source, importance, score
    score 为 MySQL FULLTEXT 相关度（非归一化）
    """
    if not query.strip():
        return []
    sql = (
        "SELECT id, content, memory_type, source, importance, "
        "MATCH(content) AGAINST(%s IN NATURAL LANGUAGE MODE) AS score "
        "FROM agent_memories "
        "WHERE project_id = %s AND deleted_at IS NULL AND embedding_status = 'done' "
        "ORDER BY score DESC, importance DESC, last_accessed_at DESC "
        "LIMIT %s"
    )
    try:
        conn = _mysql_conn()
        try:
            with conn.cursor() as cur:
                cur.execute(sql, (query, project_id, top_n))
                rows = cur.fetchall()
            return rows
        finally:
            conn.close()
    except Exception as e:
        logger.error("BM25 检索失败 project=%s: %s", project_id, e)
        return []


# ==================== RAG 向量检索 ====================

def rag_search(project_id: str, query_vector: List[float], top_n: int = 10) -> List[Dict[str, Any]]:
    """Chroma 向量检索。返回字段：id, content, memory_type, source, importance, score(相似度 0-1)。"""
    col = get_or_create_collection(project_id)
    try:
        res = col.query(
            query_embeddings=[query_vector],
            n_results=top_n,
            include=["documents", "metadatas", "distances"],
        )
    except Exception as e:
        logger.error("RAG 检索失败 project=%s: %s", project_id, e)
        return []

    ids = res.get("ids", [[]])[0]
    docs = res.get("documents", [[]])[0]
    metas = res.get("metadatas", [[]])[0]
    dists = res.get("distances", [[]])[0]  # cosine distance ∈ [0, 2]

    out = []
    for i, _id in enumerate(ids):
        # cosine distance → similarity ∈ [0, 1]（clamp）
        dist = float(dists[i]) if i < len(dists) else 1.0
        sim = max(0.0, min(1.0, 1.0 - dist / 2.0))
        meta = metas[i] if i < len(metas) else {}
        out.append({
            "id": str(_id),
            "content": docs[i] if i < len(docs) else "",
            "memory_type": meta.get("memory_type", ""),
            "source": meta.get("source", ""),
            "importance": int(meta.get("importance", 50)),
            "score": sim,
        })
    return out


# ==================== 混合融合 ====================

def _normalize_scores(items: List[Dict[str, Any]], score_key: str = "score") -> List[Dict[str, Any]]:
    """分数 min-max 归一化到 [0, 1]。"""
    if not items:
        return items
    scores = [float(it.get(score_key, 0)) for it in items]
    lo, hi = min(scores), max(scores)
    rng = hi - lo
    for it in items:
        s = float(it.get(score_key, 0))
        it["norm_score"] = (s - lo) / rng if rng > 0 else 1.0
    return items


def hybrid_fuse(
    bm25_results: List[Dict[str, Any]],
    rag_results: List[Dict[str, Any]],
    bm25_weight: float,
    rag_weight: float,
    top_k: int,
) -> List[Dict[str, Any]]:
    """分数归一化 + 线性加权融合，取 top-k。

    融合后每条记录含：
    - id, content, memory_type, source, importance
    - score: 加权融合分数 ∈ [0, 1]
    - sources: 命中路数 ["bm25", "rag"]（用于调试/可观测）
    """
    bm25_results = _normalize_scores(bm25_results)
    rag_results = _normalize_scores(rag_results)

    merged: Dict[str, Dict[str, Any]] = {}

    for it in bm25_results:
        merged[it["id"]] = {
            "id": it["id"],
            "content": it["content"],
            "memory_type": it.get("memory_type", ""),
            "source": it.get("source", ""),
            "importance": int(it.get("importance", 50)),
            "bm25_score": it.get("norm_score", 0.0),
            "rag_score": 0.0,
            "sources": ["bm25"],
        }

    for it in rag_results:
        if it["id"] in merged:
            merged[it["id"]]["rag_score"] = it.get("norm_score", 0.0)
            merged[it["id"]]["sources"].append("rag")
            # 取较丰富的 content（RAG 文档来自 Chroma，应已存）
            if not merged[it["id"]]["content"]:
                merged[it["id"]]["content"] = it["content"]
        else:
            merged[it["id"]] = {
                "id": it["id"],
                "content": it["content"],
                "memory_type": it.get("memory_type", ""),
                "source": it.get("source", ""),
                "importance": int(it.get("importance", 50)),
                "bm25_score": 0.0,
                "rag_score": it.get("norm_score", 0.0),
                "sources": ["rag"],
            }

    # 线性加权
    results = []
    for it in merged.values():
        final = bm25_weight * it["bm25_score"] + rag_weight * it["rag_score"]
        it["score"] = final
        results.append(it)

    # 排序取 top-k
    results.sort(key=lambda x: x["score"], reverse=True)
    return results[:top_k]

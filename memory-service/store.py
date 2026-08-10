"""存储与检索封装：Chroma 向量库 + MySQL BM25 + 混合融合."""
import os
import logging
from typing import List, Dict, Any, Optional

import chromadb
import pymysql
import numpy as np
from dotenv import load_dotenv
import jieba
from rank_bm25 import BM25Okapi

load_dotenv()

logger = logging.getLogger("memory-service.store")

# BM25 内存索引参数(可选,有默认值)
_BM25_K1 = float(os.getenv("BM25_K1", "1.5"))
_BM25_B = float(os.getenv("BM25_B", "0.75"))

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
    _invalidate_bm25_index()  # 数据变更,失效 BM25 内存索引(下次查询惰性重建)
    return len(ids)


def delete_embeddings(project_id: str, ids: List[str]) -> int:
    """从 Chroma 删除向量（归档时调用）。"""
    if not ids:
        return 0
    try:
        col = get_or_create_collection(project_id)
        col.delete(ids=ids)
        _invalidate_bm25_index()  # 数据变更,失效 BM25 内存索引
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


# ==================== 内存 BM25 索引(jieba + rank_bm25,替代 MySQL FULLTEXT) ====================

def _jieba_tokenize(text: str) -> List[str]:
    """jieba 精确模式分词 + 过滤单字符标点.

    MySQL FULLTEXT(ngram)对中文分词失效(实测中文查询返回 0 条),
    改用 jieba 分词 + rank_bm25 真 BM25。
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
    """全局内存 BM25 索引(单例,惰性构建).

    记忆总量小(数百条),无需按 project 分桶,全局一个索引即可。
    加载时排除 keyword 类型(避免标签干扰语义召回)。
    数据变更(upsert/delete)后由 _invalidate_bm25_index 置空,下次查询重建。
    """

    def __init__(self):
        self._memories: List[Dict[str, Any]] = []
        self._tokenized: List[List[str]] = []
        self._bm25: Optional[BM25Okapi] = None
        self._build()

    def _build(self):
        import time
        t0 = time.time()
        sql = (
            "SELECT id, project_id, content, memory_type, source, importance "
            "FROM agent_memories "
            "WHERE deleted_at IS NULL AND embedding_status='done' "
            "AND memory_type != 'keyword' "
            "ORDER BY created_at ASC"
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
        for r in rows:
            self._memories.append(r)
            self._tokenized.append(_jieba_tokenize(r.get("content", "")))
        self._bm25 = BM25Okapi(self._tokenized, k1=_BM25_K1, b=_BM25_B)
        logger.info("BM25 内存索引构建完成: %d 条记忆(已排除 keyword), 耗时 %.2fs",
                    len(self._memories), time.time() - t0)

    def search(self, project_id: str, query: str, top_n: int = 20) -> List[Dict[str, Any]]:
        if not self._bm25 or not self._memories:
            return []
        query_tokens = _jieba_tokenize(query)
        if not query_tokens:
            return []
        scores = self._bm25.get_scores(query_tokens)
        # 项目隔离:非本项目置 -inf
        mask = np.array([m["project_id"] == project_id for m in self._memories], dtype=bool)
        scores = np.where(mask, scores, -np.inf)
        n = min(top_n, len(scores))
        if n <= 0:
            return []
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
            m = self._memories[idx]
            out.append({
                "id": m["id"], "content": m["content"],
                "memory_type": m["memory_type"], "source": m["source"],
                "importance": int(m.get("importance", 50)),
                "score": score, "sources": ["bm25"],
            })
            if len(out) >= top_n:
                break
        return out


_bm25_index: Optional[_BM25Index] = None


def _get_bm25_index() -> _BM25Index:
    """获取全局 BM25 索引(惰性单例)."""
    global _bm25_index
    if _bm25_index is None:
        _bm25_index = _BM25Index()
    return _bm25_index


def _invalidate_bm25_index():
    """失效全局 BM25 索引(upsert/delete 后置空,下次查询重建)."""
    global _bm25_index
    _bm25_index = None


def bm25_search(project_id: str, query: str, top_n: int = 10) -> List[Dict[str, Any]]:
    """内存 BM25 检索(替代 MySQL FULLTEXT,真 BM25 + 中文分词).

    返回字段：id, content, memory_type, source, importance, score
    score 为 BM25Okapi 原始分(非归一化)
    """
    if not query.strip():
        return []
    try:
        return _get_bm25_index().search(project_id, query, top_n=top_n)
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
    - rag_raw_score: RAG 路绝对 cosine(去重判重用,不受归一化/降权影响)

    keyword 自适应降权：
    - 有高质量语义命中(top1 rag_raw_score > 0.82)时 keyword × 0.1(接近排除)
    - 无语义命中时 keyword × 0.6(保留兜底信号)
    注:score 卡 0.70 是 BM25 返回 0 条的必然结果(0.3×0+0.7×1.0),修好 BM25 自动恢复。
    """
    bm25_results = _normalize_scores(bm25_results)
    rag_results = _normalize_scores(rag_results)

    # rag_raw_score:保留 RAG 路绝对相似度(去重判重用)
    rag_raw_map: Dict[str, float] = {}
    for it in rag_results:
        rag_raw_map[it["id"]] = float(it.get("score", 0.0))

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
            "rag_raw_score": rag_raw_map.get(it["id"], 0.0),
            "sources": ["bm25"],
        }

    for it in rag_results:
        if it["id"] in merged:
            merged[it["id"]]["rag_score"] = it.get("norm_score", 0.0)
            merged[it["id"]]["rag_raw_score"] = float(it.get("score", 0.0))
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
                "rag_raw_score": float(it.get("score", 0.0)),
                "sources": ["rag"],
            }

    # 线性加权
    results = []
    for it in merged.values():
        final = bm25_weight * it["bm25_score"] + rag_weight * it["rag_score"]
        it["score"] = final
        results.append(it)

    # keyword 自适应降权(不排除:保留信号同时降低干扰)
    non_kw = [r for r in results if r.get("memory_type") != "keyword"]
    kw = [r for r in results if r.get("memory_type") == "keyword"]
    # 取非 keyword 结果中最大的 rag_raw_score 作为判断依据
    # (results 此时未排序,不能取 non_kw[0],否则是任意首元素而非最高分)
    max_non_kw_rag = max((r.get("rag_raw_score", 0) for r in non_kw), default=0)
    if max_non_kw_rag > 0.82:
        # 有高质量语义命中 → keyword 降到很低(接近排除)
        for r in kw:
            r["score"] *= 0.1
    elif kw:
        # 无语义命中 → keyword 保留较高权重(兜底)
        for r in kw:
            r["score"] *= 0.6

    # 排序取 top-k
    results.sort(key=lambda x: x["score"], reverse=True)
    return results[:top_k]

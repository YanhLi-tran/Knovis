# -*- coding: utf-8 -*-
"""检索两级缓存(阶段 C P1): Redis L1 向量缓存 + L2 结果缓存.

L1: query 向量缓存(key=qv:{sha256(query)[:16]}, TTL 2h, 不随记忆变更失效)
L2: 检索结果缓存(key=sr:{project_id}:{sha256(query)[:16]}:{top_k}, TTL 60s, upsert/delete 失效)

降级策略:
- Redis 不可用: 静默降级, 所有请求走正常路径, 不报错不中断
- 反序列化失败: 跳过该缓存项
- CACHE_ENABLED=false: 完全绕过
"""
import os
import json
import hashlib
import logging
from typing import Optional, List, Dict, Any

logger = logging.getLogger("memory-service.cache")

_ENABLED = os.getenv("CACHE_ENABLED", "true").lower() in ("true", "1", "yes")
_TTL_RESULT = int(os.getenv("CACHE_TTL_RESULT", "60"))
_TTL_VECTOR = int(os.getenv("CACHE_TTL_VECTOR", "7200"))

_redis_client = None
_redis_failed = False  # Redis 曾失败则本次进程不再重试(避免每次请求都尝试连接)


def _get_redis():
    """获取 Redis 客户端(惰性单例, 失败置 _redis_failed)."""
    global _redis_client, _redis_failed
    if _redis_failed:
        return None
    if _redis_client is not None:
        return _redis_client
    try:
        import redis as redis_lib
        url = os.getenv("REDIS_URL", "redis://127.0.0.1:6379/0")
        # protocol=2: 兼容老版 Redis(不支持 HELLO/RESP3), 如本机 5.x
        _redis_client = redis_lib.Redis.from_url(url, protocol=2, socket_connect_timeout=2, socket_timeout=2)
        _redis_client.ping()
        logger.info("Redis 缓存已连接: %s (protocol=2)", url)
        return _redis_client
    except Exception as e:
        _redis_failed = True
        logger.warning("Redis 连接失败, 缓存降级(本次进程不再重试): %s", e)
        return None


def _key_hash(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:16]


# ==================== L1: 向量缓存 ====================

def get_query_vector(query: str) -> Optional[List[float]]:
    """取 query 向量缓存, 未命中返回 None."""
    if not _ENABLED:
        return None
    r = _get_redis()
    if r is None:
        return None
    try:
        raw = r.get(f"qv:{_key_hash(query)}")
        if raw:
            return json.loads(raw)
        return None
    except Exception as e:
        logger.debug("L1 缓存读取失败(降级): %s", e)
        return None


def set_query_vector(query: str, vector: List[float]):
    """写 query 向量缓存."""
    if not _ENABLED:
        return
    r = _get_redis()
    if r is None:
        return
    try:
        r.set(f"qv:{_key_hash(query)}", json.dumps(vector), ex=_TTL_VECTOR)
    except Exception as e:
        logger.debug("L1 缓存写入失败(降级): %s", e)


# ==================== L2: 结果缓存 ====================

def get_search_result(project_id: str, query: str, top_k: int) -> Optional[str]:
    """取检索结果缓存(返回原始 JSON 字符串, 未命中返回 None)."""
    if not _ENABLED:
        return None
    r = _get_redis()
    if r is None:
        return None
    try:
        return r.get(f"sr:{project_id}:{_key_hash(query)}:{top_k}")
    except Exception as e:
        logger.debug("L2 缓存读取失败(降级): %s", e)
        return None


def set_search_result(project_id: str, query: str, top_k: int, result_json: str):
    """写检索结果缓存."""
    if not _ENABLED:
        return
    r = _get_redis()
    if r is None:
        return
    try:
        r.set(f"sr:{project_id}:{_key_hash(query)}:{top_k}", result_json, ex=_TTL_RESULT)
    except Exception as e:
        logger.debug("L2 缓存写入失败(降级): %s", e)


def invalidate_project_cache(project_id: str):
    """失效某项目全部 L2 结果缓存(upsert/delete/delete_collection 时调用)."""
    if not _ENABLED:
        return
    r = _get_redis()
    if r is None:
        return
    try:
        # SCAN 匹配 sr:{project_id}:* 并删除
        cursor = 0
        pattern = f"sr:{project_id}:*"
        while True:
            cursor, keys = r.scan(cursor=cursor, match=pattern, count=200)
            if keys:
                r.delete(*keys)
            if cursor == 0:
                break
        logger.info("已失效 project=%s 的检索缓存", project_id)
    except Exception as e:
        logger.debug("L2 缓存失效失败(降级): %s", e)

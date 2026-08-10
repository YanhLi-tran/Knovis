# -*- coding: utf-8 -*-
"""在线监控:内存环形缓冲 + MySQL 分钟聚合 + percentiles 计算(阶段 C P0).

/search 每次调用记录一条 metrics entry 到环形缓冲(同步, ~0.01ms),
同分钟聚合写入 MySQL memory_search_metrics(异步线程, 不阻断响应)。
GET /metrics 接口读取实时明细 + 聚合数据。
"""
import os
import time
import logging
import threading
from collections import deque
from datetime import datetime, timedelta
from typing import List, Dict, Any, Optional

logger = logging.getLogger("memory-service.metrics")

# 环形缓冲容量(可配置)
_BUFFER_SIZE = int(os.getenv("METRICS_BUFFER_SIZE", "1000"))


class MetricsBuffer:
    """线程安全环形缓冲,保留最近 N 次检索明细."""

    def __init__(self, size: int = _BUFFER_SIZE):
        self._buf: deque = deque(maxlen=size)
        self._lock = threading.Lock()
        self._flush_lock = threading.Lock()  # 防并发写同一分钟 bucket

    def record(self, entry: dict):
        with self._lock:
            self._buf.append(entry)

    def snapshot(self) -> list:
        with self._lock:
            return list(self._buf)

    def percentiles(self, key: str) -> dict:
        """计算 P50/P95/P99(对指定数值字段)."""
        vals = sorted(float(e.get(key, 0) or 0) for e in self.snapshot())
        if not vals:
            return {"p50": 0, "p95": 0, "p99": 0}
        n = len(vals)
        return {
            "p50": vals[min(int(n * 0.50), n - 1)],
            "p95": vals[min(int(n * 0.95), n - 1)],
            "p99": vals[min(int(n * 0.99), n - 1)],
        }

    def avg(self, key: str) -> float:
        """均值."""
        vals = [float(e.get(key, 0) or 0) for e in self.snapshot()]
        if not vals:
            return 0.0
        return sum(vals) / len(vals)


# 全局单例
_buffer = MetricsBuffer()


def record_search_metric(entry: dict):
    """记录一条检索指标(同步写环形缓冲, 异步写 MySQL)."""
    _buffer.record(entry)
    # 异步写 MySQL 聚合(不阻断响应)
    if os.getenv("METRICS_MYSQL_AGGREGATE", "true").lower() in ("true", "1", "yes"):
        t = threading.Thread(target=_aggregate_write, args=(dict(entry),), daemon=True)
        t.start()


def _aggregate_write(entry: dict):
    """按分钟聚合写入 MySQL(同分钟 upsert 累加)."""
    try:
        import pymysql

        conn = pymysql.connect(
            host=os.getenv("DB_HOST", "127.0.0.1"),
            port=int(os.getenv("DB_PORT", "3306")),
            user=os.getenv("DB_USER", "root"),
            password=os.getenv("DB_PASSWORD", ""),
            database=os.getenv("DB_NAME", "agent_go"),
            charset="utf8mb4",
            cursorclass=pymysql.cursors.DictCursor,
            connect_timeout=3,
        )
        try:
            project_id = entry.get("project_id", "")
            total_ms = float(entry.get("total_ms", 0) or 0)
            bm25_count = float(entry.get("bm25_count", 0) or 0)
            rag_count = float(entry.get("rag_count", 0) or 0)
            cache_hit = entry.get("cache_hit", "")
            kw_deweight = 1 if entry.get("keyword_deweight_triggered") else 0
            rag_fail = 1 if entry.get("rag_failed") else 0
            bucket = datetime.utcnow().replace(second=0, microsecond=0)

            with conn.cursor() as cur:
                # 同分钟 bucket 已存在 → UPDATE 累加
                cur.execute(
                    "SELECT id, request_count, avg_total_ms, avg_bm25_count, avg_rag_count, "
                    "cache_hit_rate, keyword_deweight_rate, rag_fail_count "
                    "FROM memory_search_metrics WHERE project_id=%s AND bucket_minute=%s",
                    (project_id, bucket),
                )
                row = cur.fetchone()
                if row:
                    n = row["request_count"] + 1
                    new_avg_total = (row["avg_total_ms"] * row["request_count"] + total_ms) / n
                    new_avg_bm25 = (row["avg_bm25_count"] * row["request_count"] + bm25_count) / n
                    new_avg_rag = (row["avg_rag_count"] * row["request_count"] + rag_count) / n
                    hit_bits = row["cache_hit_rate"] * row["request_count"] + (1 if cache_hit else 0)
                    kw_rate = (row["keyword_deweight_rate"] * row["request_count"] + kw_deweight) / n
                    cur.execute(
                        "UPDATE memory_search_metrics SET request_count=%s, avg_total_ms=%s, "
                        "avg_bm25_count=%s, avg_rag_count=%s, cache_hit_rate=%s, "
                        "keyword_deweight_rate=%s, rag_fail_count=%s WHERE id=%s",
                        (n, new_avg_total, new_avg_bm25, new_avg_rag,
                         hit_bits / n, kw_rate, row["rag_fail_count"] + rag_fail, row["id"]),
                    )
                else:
                    cur.execute(
                        "INSERT INTO memory_search_metrics (project_id, bucket_minute, request_count, "
                        "avg_total_ms, avg_bm25_count, avg_rag_count, cache_hit_rate, "
                        "keyword_deweight_rate, rag_fail_count) "
                        "VALUES (%s, %s, 1, %s, %s, %s, %s, %s, %s)",
                        (project_id, bucket, total_ms, bm25_count, rag_count,
                         1 if cache_hit else 0, kw_deweight, rag_fail),
                    )
            conn.commit()
        finally:
            conn.close()
    except Exception as e:
        # 监控写入失败不影响主流程(静默降级)
        logger.debug("metrics 聚合写入失败(忽略): %s", e)


def get_realtime_metrics(project_id: str = "") -> dict:
    """实时指标(环形缓冲)."""
    snap = _buffer.snapshot()
    if project_id:
        snap = [e for e in snap if e.get("project_id") == project_id]
    if not snap:
        return {
            "latency_ms": {"p50": 0, "p95": 0, "p99": 0},
            "recent_count": 0,
            "avg_bm25_count": 0,
            "avg_rag_count": 0,
            "cache_hit_rate": {"l1_vector": 0, "l2_result": 0},
            "keyword_deweight_rate": 0,
            "rag_fail_count": 0,
        }
    total_vals = [float(e.get("total_ms", 0) or 0) for e in snap]
    total_vals.sort()
    n = len(total_vals)
    lat = {
        "p50": total_vals[min(int(n * 0.50), n - 1)],
        "p95": total_vals[min(int(n * 0.95), n - 1)],
        "p99": total_vals[min(int(n * 0.99), n - 1)],
    }
    avg_bm25 = sum(float(e.get("bm25_count", 0) or 0) for e in snap) / n
    avg_rag = sum(float(e.get("rag_count", 0) or 0) for e in snap) / n
    l1_hit = sum(1 for e in snap if e.get("cache_hit") == "l1_vector") / n
    l2_hit = sum(1 for e in snap if e.get("cache_hit") == "l2_result") / n
    kw_rate = sum(1 for e in snap if e.get("keyword_deweight_triggered")) / n
    rag_fail = sum(1 for e in snap if e.get("rag_failed"))
    return {
        "latency_ms": lat,
        "recent_count": n,
        "avg_bm25_count": round(avg_bm25, 1),
        "avg_rag_count": round(avg_rag, 1),
        "cache_hit_rate": {"l1_vector": round(l1_hit, 3), "l2_result": round(l2_hit, 3)},
        "keyword_deweight_rate": round(kw_rate, 3),
        "rag_fail_count": rag_fail,
    }


def get_aggregated_metrics(project_id: str = "", range_hours: int = 1) -> list:
    """MySQL 聚合数据(最近 range_hours 小时, 按分钟)."""
    try:
        import pymysql

        conn = pymysql.connect(
            host=os.getenv("DB_HOST", "127.0.0.1"),
            port=int(os.getenv("DB_PORT", "3306")),
            user=os.getenv("DB_USER", "root"),
            password=os.getenv("DB_PASSWORD", ""),
            database=os.getenv("DB_NAME", "agent_go"),
            charset="utf8mb4",
            cursorclass=pymysql.cursors.DictCursor,
            connect_timeout=3,
        )
        try:
            since = datetime.utcnow() - timedelta(hours=range_hours)
            sql = "SELECT * FROM memory_search_metrics WHERE bucket_minute >= %s"
            args: List[Any] = [since]
            if project_id:
                sql += " AND project_id = %s"
                args.append(project_id)
            sql += " ORDER BY bucket_minute DESC LIMIT 200"
            with conn.cursor() as cur:
                cur.execute(sql, args)
                rows = cur.fetchall()
            return [{"bucket_minute": r["bucket_minute"].strftime("%Y-%m-%dT%H:%M:%S"),
                     "request_count": r["request_count"],
                     "avg_total_ms": round(float(r["avg_total_ms"] or 0), 1),
                     "avg_bm25_count": round(float(r["avg_bm25_count"] or 0), 1),
                     "avg_rag_count": round(float(r["avg_rag_count"] or 0), 1),
                     "cache_hit_rate": round(float(r["cache_hit_rate"] or 0), 3),
                     "keyword_deweight_rate": round(float(r["keyword_deweight_rate"] or 0), 3),
                     "rag_fail_count": r["rag_fail_count"]} for r in rows]
        finally:
            conn.close()
    except Exception as e:
        logger.warning("读取聚合指标失败: %s", e)
        return []


def get_capacity_metrics() -> dict:
    """容量指标(MySQL 查询): hot/cold 记忆数 + BM25 索引条数."""
    try:
        import pymysql

        conn = pymysql.connect(
            host=os.getenv("DB_HOST", "127.0.0.1"),
            port=int(os.getenv("DB_PORT", "3306")),
            user=os.getenv("DB_USER", "root"),
            password=os.getenv("DB_PASSWORD", ""),
            database=os.getenv("DB_NAME", "agent_go"),
            charset="utf8mb4",
            cursorclass=pymysql.cursors.DictCursor,
            connect_timeout=3,
        )
        try:
            with conn.cursor() as cur:
                cur.execute("SELECT COUNT(*) AS n FROM agent_memories WHERE deleted_at IS NULL AND tier='hot'")
                hot = cur.fetchone()["n"]
                cur.execute("SELECT COUNT(*) AS n FROM agent_memories WHERE deleted_at IS NULL AND tier='cold'")
                cold = cur.fetchone()["n"]
                cur.execute("SELECT COUNT(*) AS n FROM agent_memories WHERE deleted_at IS NULL AND merged_at IS NOT NULL")
                merged = cur.fetchone()["n"]
            return {
                "hot_memories": hot,
                "cold_memories": cold,
                "merged_memories": merged,
                "bm25_index_size": 0,  # 由 store 侧注入实际值
            }
        finally:
            conn.close()
    except Exception as e:
        logger.warning("读取容量指标失败: %s", e)
        return {"hot_memories": 0, "cold_memories": 0, "merged_memories": 0, "bm25_index_size": 0}

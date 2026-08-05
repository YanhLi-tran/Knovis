"""Embedding 客户端:HTTP 调用 memory-service(8002)的 /embed_vectors 获取向量.

复用 memory-service 已加载的 bge-large-zh 模型,不在 doc-service 重复加载(~1.5GB)。
向量获取后由 doc-service 自主 upsert 到独立 Chroma collection doc_global,
metadata 完整可控(document_id/page_num 等),支持 doc_ids 过滤,且避免与 memory-service 共享目录的并发风险。
"""
import os
import logging
from typing import List

import httpx
from dotenv import load_dotenv

load_dotenv()

logger = logging.getLogger("doc-service.embedder_client")

# 单批 embedding 上限(防 memory-service OOM,与其 EMBED_BATCH_SIZE 对齐)
_EMBED_BATCH = 32


class EmbedderClient:
    """memory-service /embed_vectors HTTP 客户端(仅返回向量,不 upsert)。"""

    def __init__(self, base_url: str = None):
        self.base_url = (base_url or os.getenv("MEMORY_SERVICE_URL", "http://127.0.0.1:8002")).rstrip("/")
        self._client = httpx.Client(timeout=120.0)  # 首次加载模型可能慢

    def embed_texts(self, texts: List[str]) -> List[List[float]]:
        """批量文本 → 向量(分批,每批 ≤32)."""
        if not texts:
            return []
        vectors: List[List[float]] = []
        for i in range(0, len(texts), _EMBED_BATCH):
            batch = texts[i : i + _EMBED_BATCH]
            try:
                resp = self._client.post(
                    f"{self.base_url}/embed_vectors",
                    json={"texts": batch},
                )
                resp.raise_for_status()
                data = resp.json()
                vectors.extend(data.get("vectors", []))
            except Exception as e:
                logger.error("调用 memory-service /embed_vectors 失败(批次 %d-%d): %s", i, i + len(batch), e)
                raise
        logger.info("embed 完成:共 %d 条向量", len(vectors))
        return vectors

    def embed_query(self, query: str) -> List[float]:
        """单条 query → 向量(用于检索)."""
        vecs = self.embed_texts([query])
        if not vecs:
            raise RuntimeError("memory-service /embed_vectors 返回空向量")
        return vecs[0]

    def health(self) -> bool:
        """探活 memory-service。"""
        try:
            resp = self._client.get(f"{self.base_url}/health", timeout=5.0)
            return resp.status_code == 200
        except Exception:
            return False


# 全局单例
_client: EmbedderClient = None


def get_client() -> EmbedderClient:
    global _client
    if _client is None:
        _client = EmbedderClient()
    return _client

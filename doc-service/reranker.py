"""Rerank 预留接口(首版不启用,RERANK_ENABLED=false).

接口全部实现,仅模型加载部分留 TODO。启用时:
1. 设置 RERANK_ENABLED=true
2. 设置 RERANK_MODEL_PATH 为本地 rerank 模型路径(如 bge-reranker-v2-m3)
3. 取消下方 TODO 处的注释,安装 sentence-transformers

简历亮点可讲:多路召回 top-20 → rerank top-5,提升准确率。
"""
import os
import logging
from typing import List, Dict, Any, Optional

logger = logging.getLogger("doc-service.reranker")


class Reranker:
    """预留 rerank 接口,第一版不启用(RERANK_ENABLED=false)."""

    def __init__(self, model_path: Optional[str] = None):
        # 配置项 RERANK_MODEL_PATH
        self.model_path = model_path or os.getenv("RERANK_MODEL_PATH", "")
        self._model = None
        self._loaded = False

    def is_enabled(self) -> bool:
        """是否启用 rerank。"""
        return os.getenv("RERANK_ENABLED", "false").lower() == "true" and bool(self.model_path)

    def _ensure_model(self):
        """懒加载 rerank 模型(启用时才加载,FP16 GPU 优先)."""
        if self._loaded:
            return
        import torch
        from sentence_transformers import CrossEncoder

        device = "cuda" if torch.cuda.is_available() else "cpu"
        self._model = CrossEncoder(
            self.model_path,
            device=device,
            max_length=512,
        )
        # GPU 可用时转 FP16,显存占用减半(~2.6GB)
        if device == "cuda":
            self._model.model.half()
        self._loaded = True
        logger.info("rerank 模型加载完成: %s (device=%s)", self.model_path, device)

    def rerank(
        self,
        query: str,
        documents: List[str],
        top_k: int = 5,
    ) -> List[Dict[str, Any]]:
        """对候选文档重排序。

        Args:
            query: 查询文本
            documents: 候选文档文本列表
            top_k: 返回数量

        Returns:
            排序后的结果列表,每条含 index(原列表索引)和 score(rerank 分数)
            未启用时原样返回(score=1.0),保持接口幂等
        """
        if not self.is_enabled():
            # 未启用:原样返回,不影响融合结果顺序
            return [{"index": i, "score": 1.0} for i in range(len(documents))]

        self._ensure_model()

        pairs = [(query, doc) for doc in documents]
        scores = self._model.predict(pairs)
        ranked = sorted(enumerate(scores), key=lambda x: x[1], reverse=True)[:top_k]
        return [{"index": i, "score": float(s)} for i, s in ranked]


# 全局单例
_reranker: Optional[Reranker] = None


def get_reranker() -> Reranker:
    """获取 reranker 单例。"""
    global _reranker
    if _reranker is None:
        _reranker = Reranker()
    return _reranker

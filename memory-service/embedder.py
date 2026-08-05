"""Embedding 模型封装（bge-large-zh 单例加载）.

启动时加载到内存（约 1.5GB），后续推理零延迟。
首次启动会从 HuggingFace 自动下载模型。

设备选择（EMBED_DEVICE 环境变量）:
- auto(默认):自动检测 CUDA,可用则用 GPU,否则回退 CPU
- cuda:强制 GPU(不可用时报错)
- cpu:强制 CPU
"""
import os
import logging
from typing import List

from dotenv import load_dotenv

load_dotenv()

logger = logging.getLogger("memory-service.embedder")

# 全局单例
_model = None
_model_name = None
_model_device = None  # 实际加载的设备,日志用


def get_model_name() -> str:
    return os.getenv("EMBEDDING_MODEL", "BAAI/bge-large-zh")


def _resolve_device() -> str:
    """解析实际使用的设备.

    EMBED_DEVICE: auto(默认,自动检测)/cuda/cpu
    """
    cfg = os.getenv("EMBED_DEVICE", "auto").lower().strip()
    if cfg == "cpu":
        return "cpu"
    if cfg == "cuda":
        # 强制 cuda,检测可用性
        try:
            import torch

            if not torch.cuda.is_available():
                logger.warning("EMBED_DEVICE=cuda 但 CUDA 不可用,回退 cpu")
                return "cpu"
            return "cuda"
        except ImportError:
            logger.warning("EMBED_DEVICE=cuda 但 torch 未安装,回退 cpu")
            return "cpu"
    # auto:自动检测
    try:
        import torch

        if torch.cuda.is_available():
            logger.info("自动检测到 CUDA 可用: %s", torch.cuda.get_device_name(0))
            return "cuda"
    except ImportError:
        pass
    return "cpu"


def get_model():
    """懒加载 embedding 模型（单例）。"""
    global _model, _model_name, _model_device
    if _model is not None:
        return _model

    from sentence_transformers import SentenceTransformer

    name = get_model_name()
    device = _resolve_device()
    logger.info("正在加载 embedding 模型: %s | device=%s（首次会从 HuggingFace 下载，约 1.5GB）", name, device)
    _model = SentenceTransformer(
        name,
        device=device,
    )
    _model_name = name
    _model_device = device
    logger.info(
        "embedding 模型加载完成，device=%s，向量维度: %d",
        device,
        _model.get_embedding_dimension(),
    )
    return _model


def get_device() -> str:
    """返回当前模型使用的设备(日志/调试用)。"""
    if _model_device is None:
        return _resolve_device()
    return _model_device


def embed(texts: List[str], batch_size: int = 32) -> List[List[float]]:
    """批量文本转向量。

    Args:
        texts: 文本列表
        batch_size: 单批最大条数（防 OOM，默认 32；GPU 模式可调大至 128/256）

    Returns:
        向量列表，顺序与输入一致
    """
    if not texts:
        return []

    model = get_model()
    # GPU 模式默认更大批量提升吞吐(CPU 仍用 32 防止长耗时阻塞)
    default_bs = 128 if _model_device == "cuda" else 32
    bs = min(batch_size, int(os.getenv("EMBED_BATCH_SIZE", str(default_bs))))
    # normalize_embeddings=True：bge 推荐用归一化向量做余弦相似度
    vectors = model.encode(
        texts,
        batch_size=bs,
        normalize_embeddings=True,
        convert_to_numpy=True,
        show_progress_bar=False,
    )
    return vectors.tolist()

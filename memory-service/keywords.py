"""关键词提取：jieba 分词 + TF-IDF.

每轮对话后异步调用，提取 user query + assistant answer 的关键词，
存入 agent_memories（memory_type=keyword, importance=30），走统一 embed + 检索链路。
"""
import os
import logging
from typing import List, Dict

import jieba
import jieba.analyse

logger = logging.getLogger("memory-service.keywords")

# 基础停用词表（单字符/无意义高频词/标点/数字单位）
# 生产可扩展为从文件加载（STOPWORDS_FILE 环境变量）
_STOPWORDS = {
    # 代词
    "我", "你", "他", "她", "它", "我们", "你们", "他们", "她们",
    "自己", "人家", "大家", "咱们", "这", "那", "这个", "那个", "这些", "那些",
    # 助词/副词
    "的", "了", "是", "在", "有", "和", "与", "或", "也", "都", "就", "还", "又",
    "不", "没", "没有", "很", "非常", "太", "最", "更", "比较", "相当", "真", "真的",
    "吗", "呢", "啊", "呀", "吧", "哦", "嗯", "哈", "啦", "嘛", "哎",
    # 量词/数词
    "个", "只", "条", "本", "份", "篇", "次", "回", "下", "上", "中",
    # 连词/介词
    "把", "被", "让", "使", "给", "为", "为了", "对", "于", "由", "从", "向", "往",
    "以", "及", "并", "但是", "不过", "然后", "所以", "因为", "如果", "虽然",
    # 时间副词（部分保留有意义的如"今天"）
    "现在", "刚才", "已经", "正在", "马上", "立刻", "一直", "总是", "经常", "有时",
    # 标点/符号
    "，", "。", "、", "；", "：", "？", "！", "" ", "" '", "（", "）", "《", "》",
    "“", "”", "‘", "’", "…", "—", "·", ".", ",", ";", ":", "?", "!", "(", ")",
    # 通用动词（语义弱）
    "说", "做", "看", "想", "知道", "觉得", "认为", "觉得", "需要", "可以",
    "能", "能够", "会", "要", "想", "应该", "可能",
    # 其他高频
    "什么", "怎么", "为什么", "哪里", "怎样", "如何", "多少",
    "东西", "地方", "时候", "时间", "问题", "事情",
}

# 可选：从文件加载额外停用词
_extra_file = os.getenv("STOPWORDS_FILE", "")
if _extra_file:
    try:
        with open(_extra_file, "r", encoding="utf-8") as f:
            for line in f:
                w = line.strip()
                if w:
                    _STOPWORDS.add(w)
        logger.info("已加载额外停用词: %s", _extra_file)
    except Exception as e:
        logger.warning("加载停用词文件失败 %s: %s", _extra_file, e)

# 自定义词典（项目/领域术语，可选）
_user_dict = os.getenv("JIEBA_USER_DICT", "")
if _user_dict:
    try:
        jieba.load_userdict(_user_dict)
        logger.info("已加载 jieba 自定义词典: %s", _user_dict)
    except Exception as e:
        logger.warning("加载自定义词典失败 %s: %s", _user_dict, e)


def extract_keywords(texts: List[str], top_k: int = 10) -> List[Dict]:
    """从多段文本中提取关键词（jieba TF-IDF + 停用词过滤）.

    Args:
        texts: 文本列表（一轮对话的 user query + assistant answer 等）
        top_k: 返回关键词数量

    Returns:
        [{"word": "关键词", "weight": 0.xxx}, ...] 按 weight 降序
    """
    if not texts:
        return []

    # 合并文本（一轮对话作为一个语义单元提取）
    combined = "\n".join(t for t in texts if t and t.strip())
    if not combined.strip():
        return []

    # jieba.analyse.extract_tags 基于 TF-IDF（内部 IDF 语料库）
    # allowPOS 限定名词性词类，过滤掉纯动词/形容词（保留 n/nr/ns/nt/nz/vn 等）
    try:
        raw = jieba.analyse.extract_tags(
            combined,
            topK=top_k * 3,  # 多取再过滤，保证过滤后仍有足够数量
            withWeight=True,
            allowPOS=(
                "n", "nr", "ns", "nt", "nz", "vn", "vn",
                "ng", "nl", "nrfg", "nrt",
            ),
        )
    except Exception as e:
        logger.exception("jieba TF-IDF 提取失败: %s", e)
        return []

    out: List[Dict] = []
    seen = set()
    for word, weight in raw:
        # 过滤停用词 + 过短词（单字符非名词）+ 去重
        if word in _STOPWORDS:
            continue
        if len(word) < 2:
            continue
        if word in seen:
            continue
        seen.add(word)
        out.append({"word": word, "weight": round(float(weight), 4)})
        if len(out) >= top_k:
            break

    return out

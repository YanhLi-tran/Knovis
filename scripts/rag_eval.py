#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""RAG 专项评测脚本.

加载 scripts/eval/rag_eval.jsonl,逐条调用 doc-service /rag/search,计算 8 项指标 + 召回时间效率:
  1. Recall@5   —— fused_candidates 前 5 是否覆盖 expected_docs
  2. Recall@20  —— fused_candidates 前 20 是否覆盖 expected_docs
  3. MRR        —— 正确文档在 fused_candidates 中的排名倒数平均
  4. 段落召回命中率 —— 最终 results 的 section_id/heading_path 是否覆盖 expected_section
  5. fallback 触发准确率 —— section_fallback 类 case 中 fallback=true 是否合理(章节长度>2000)
  6. 多路召回贡献度 —— bm25_only / rag_only / fused 三路 Recall@20 对比
  7. 引用准确率  —— citation 类 case 校验 doc_name + page_num + heading_path
  8. 拒答率     —— refusal 类 case 检索结果是否均为低相关度(score < 阈值)
  9. 召回时间效率 —— embed/bm25/rag/fuse/rerank/section/total 分阶段耗时 P50/P95/max

用法:
  python rag_eval.py                         # 全量评测(strategy=fused)
  python rag_eval.py --category single_doc   # 按场景过滤
  python rag_eval.py --strategy bm25_only    # A/B 对比检索策略
  python rag_eval.py --top-k 20              # 指定 top_k

依赖: requests (标准库 + requests,不引入 pytest)
"""
import argparse
import json
import os
import sys
import time
from collections import defaultdict
from pathlib import Path

try:
    import requests
except ImportError:
    print("[FATAL] 缺少 requests,请先 pip install requests", file=sys.stderr)
    sys.exit(1)

DOC_SERVICE_URL = os.getenv("DOC_SERVICE_URL", "http://127.0.0.1:8003")
EVAL_FILE = Path(__file__).parent / "eval" / "rag_eval.jsonl"
REPORT_FILE = Path(__file__).parent / "rag_eval_report.md"
REFUSAL_SCORE_THRESHOLD = 0.3  # 兼容旧引用(已弃用,改用 cosine 阈值)
REFUSAL_COSINE_THRESHOLD = 0.86  # 拒答 case:fused top1 的 rag_raw_score(cosine)低于此阈值视为低相关度(基于 bge-large-zh 年报数据分布:refusal 0.83-0.91 vs 正常 0.89-0.92,gap 在 0.86)
HTTP_TIMEOUT = 60


# ==================== 数据加载 ====================

def load_cases(path: Path):
    """加载评测集,跳过 _meta / _schema 行."""
    cases = []
    with open(path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            obj = json.loads(line)
            if "_meta" in obj or "_schema" in obj:
                continue
            cases.append(obj)
    return cases


# ==================== 检索调用 ====================

def call_rag_search(query: str, top_k: int, strategy: str):
    """调用 doc-service /rag/search,返回 JSON dict."""
    body = {"query": query, "top_k": top_k}
    if strategy:
        body["strategy"] = strategy
    try:
        resp = requests.post(
            f"{DOC_SERVICE_URL}/rag/search", json=body, timeout=HTTP_TIMEOUT
        )
        resp.raise_for_status()
        return resp.json()
    except Exception as e:
        return {"_error": str(e)}


# ==================== 指标计算 ====================

def _doc_name_set(candidates):
    """从候选列表提取 doc_name 集合(按出现顺序保前 K)."""
    return [c.get("doc_name", "") for c in candidates]


def recall_at_k(candidates, expected_docs, k):
    """计算单条 case 的 Recall@K(命中 expected_docs 比例)."""
    if not expected_docs:
        return None  # 无期望文档(refusal 类)不计入
    topk = candidates[:k]
    topk_docs = {c.get("doc_name", "") for c in topk}
    hit = sum(1 for d in expected_docs if d in topk_docs)
    return hit / len(expected_docs)


def reciprocal_rank(candidates, expected_docs):
    """计算单条 case 的 RR(正确文档在候选中最靠前排名的倒数)."""
    if not expected_docs:
        return None
    expected_set = set(expected_docs)
    for i, c in enumerate(candidates):
        if c.get("doc_name", "") in expected_set:
            return 1.0 / (i + 1)
    return 0.0


def section_hit(results, expected_section):
    """段落召回命中率:results 中是否有 section_id/heading_path 覆盖 expected_section 关键词."""
    if not expected_section:
        return None
    kw = expected_section.strip()
    for r in results:
        section_id = (r.get("section_id") or "").lower()
        heading = " ".join(r.get("heading_path") or []).lower()
        if kw.lower() in section_id or kw.lower() in heading:
            return True
    # 宽松匹配:expected_section 含分号(多章节)时逐个匹配
    for sub in kw.split(";"):
        sub = sub.strip()
        if not sub:
            continue
        for r in results:
            section_id = (r.get("section_id") or "").lower()
            heading = " ".join(r.get("heading_path") or []).lower()
            if sub.lower() in section_id or sub.lower() in heading:
                return True
    return False


def fallback_check(results):
    """检查段落召回是否触发 fallback 及合理性."""
    has_fallback = any(r.get("fallback") is True for r in results)
    # 合理性:触发 fallback 且 content_length > 2000,或未触发(章节本就较短也算合理)
    long_enough = any(r.get("content_length", 0) > 2000 for r in results)
    return has_fallback, long_enough


def citation_check(results, expected_docs, expected_page, expected_section):
    """引用准确率:doc_name + page_num + heading_path/section 是否匹配."""
    if not expected_docs:
        return None
    expected_doc_set = set(expected_docs)
    expected_page_set = set(expected_page) if expected_page else None
    kw = (expected_section or "").strip().lower()

    for r in results:
        doc_ok = r.get("doc_name", "") in expected_doc_set
        if not doc_ok:
            continue
        page_ok = True
        if expected_page_set:
            page_ok = r.get("page_num") in expected_page_set
        section_ok = True
        if kw:
            section_id = (r.get("section_id") or "").lower()
            heading = " ".join(r.get("heading_path") or []).lower()
            section_ok = kw in section_id or kw in heading or any(
                kw in sub for sub in section_id.split(";")
            )
        if doc_ok and page_ok and section_ok:
            return True
    # 宽松:若 page 未标注(expected_page 空),只校验 doc_name + section
    if expected_page_set is None:
        for r in results:
            doc_ok = r.get("doc_name", "") in expected_doc_set
            section_ok = True
            if kw:
                section_id = (r.get("section_id") or "").lower()
                heading = " ".join(r.get("heading_path") or []).lower()
                section_ok = kw in section_id or kw in heading
            if doc_ok and section_ok:
                return True
    return False


def refusal_check(fused_candidates):
    """拒答率:refusal 类 case 用 RAG 路 raw cosine 判断相关度.

    D 方案改造:不再用融合分(RRF 分数量纲变了,且不反映绝对相关度),
    改用 fused top1 的 rag_raw_score(bge-large-zh cosine similarity).
    cosine < REFUSAL_COSINE_THRESHOLD 视为低相关度,应拒答.
    """
    if not fused_candidates:
        return True  # 无结果,视为正确拒答
    top_rag_raw = float(fused_candidates[0].get("rag_raw_score", 0.0) or 0.0)
    return top_rag_raw < REFUSAL_COSINE_THRESHOLD


def eval_case(case, search_resp, strategy):
    """对单条 case 计算全部指标,返回指标 dict + 诊断信息."""
    fused_cand = search_resp.get("fused_candidates", [])
    bm25_cand = search_resp.get("bm25_candidates", [])
    rag_cand = search_resp.get("rag_candidates", [])
    reranked_cand = search_resp.get("reranked_candidates", [])
    results = search_resp.get("results", [])

    # rerank 评测:当 reranked_candidates 非空时,用 rerank 后的候选计算 recall/MRR
    # 否则用 fused_candidates(兼容 before 场景)
    eval_cand = reranked_cand if reranked_cand else fused_cand

    expected_docs = case.get("expected_docs", [])
    expected_page = case.get("expected_page", [])
    expected_section = case.get("expected_section", "")
    category = case.get("category", "")

    m = {
        "id": case.get("id"),
        "category": category,
        "query": case.get("query", ""),
        "fused_count": len(fused_cand),
        "bm25_count": len(bm25_cand),
        "rag_count": len(rag_cand),
        "reranked_count": len(reranked_cand),
        "results_count": len(results),
        "elapsed_ms": search_resp.get("elapsed_ms", 0),
        "reranked": search_resp.get("reranked", False),
        "top1_score": float(eval_cand[0]["score"]) if eval_cand else 0.0,
        "top1_rag_raw": float(fused_cand[0].get("rag_raw_score", 0.0) or 0.0) if fused_cand else 0.0,
        "top1_doc": eval_cand[0]["doc_name"] if eval_cand else "",
        "top1_section": (eval_cand[0].get("section_id", "") if eval_cand else ""),
        "hit_docs": [],
        "miss_docs": list(expected_docs),
        # 分阶段耗时(毫秒,来自 doc-service /rag/search 响应)
        "embed_ms": int(search_resp.get("embed_ms", 0) or 0),
        "bm25_ms": int(search_resp.get("bm25_ms", 0) or 0),
        "rag_ms": int(search_resp.get("rag_ms", 0) or 0),
        "fuse_ms": int(search_resp.get("fuse_ms", 0) or 0),
        "rerank_ms": int(search_resp.get("rerank_ms", 0) or 0),
        "section_ms": int(search_resp.get("section_ms", 0) or 0),
        "total_ms": int(search_resp.get("total_ms", 0) or search_resp.get("elapsed_ms", 0) or 0),
    }

    # 1) Recall@5 / 2) Recall@20(用 rerank 后候选评测 rerank 影响)
    m["recall@5"] = recall_at_k(eval_cand, expected_docs, 5)
    m["recall@20"] = recall_at_k(eval_cand, expected_docs, 20)
    # 多路 Recall@20(始终用原始路,不受 rerank 影响)
    m["bm25_recall@20"] = recall_at_k(bm25_cand, expected_docs, 20)
    m["rag_recall@20"] = recall_at_k(rag_cand, expected_docs, 20)

    # 命中/未命中文档诊断(用评测候选)
    if expected_docs:
        hit_docs = set()
        topk_docs = {c.get("doc_name", "") for c in eval_cand[:20]}
        for d in expected_docs:
            if d in topk_docs:
                hit_docs.add(d)
        m["hit_docs"] = sorted(hit_docs)
        m["miss_docs"] = sorted(set(expected_docs) - hit_docs)

    # 3) MRR(用 rerank 后候选)
    m["rr"] = reciprocal_rank(eval_cand, expected_docs)

    # 4) 段落召回命中率
    m["section_hit"] = section_hit(results, expected_section)

    # 5) fallback 触发(仅 section_fallback 类有意义,但都记录)
    has_fb, long_enough = fallback_check(results)
    m["fallback_triggered"] = has_fb
    m["fallback_long_enough"] = long_enough
    if category == "section_fallback":
        # 合理 = 触发 fallback 且章节够长,或未触发但章节本身较短(也算正常召回)
        m["fallback_reasonable"] = (has_fb and long_enough) or (not has_fb)
    else:
        m["fallback_reasonable"] = None

    # 6) 引用准确率(仅 citation 类)
    if category == "citation":
        m["citation_ok"] = citation_check(results, expected_docs, expected_page, expected_section)
    else:
        m["citation_ok"] = None

    # 7) 拒答率(仅 refusal 类)
    if category == "refusal":
        m["refusal_ok"] = refusal_check(fused_cand)
    else:
        m["refusal_ok"] = None

    # 综合判定(用于失败 case 列表)
    m["pass"] = _overall_pass(m, category, expected_docs)
    return m


def _overall_pass(m, category, expected_docs):
    """单条 case 综合是否通过(用于失败 case 列表)."""
    if category == "refusal":
        return m["refusal_ok"] is True
    if not expected_docs:
        return m["section_hit"] is not False
    # 文档召回 + 段落命中均需通过
    doc_ok = (m["recall@20"] or 0) >= 1.0
    sec_ok = m["section_hit"] is not False if m["section_hit"] is not None else True
    if category == "citation":
        return doc_ok and m["citation_ok"] is True
    if category == "section_fallback":
        return doc_ok and m["fallback_reasonable"] is not False
    return doc_ok and sec_ok


# ==================== 汇总统计 ====================

def _avg(values):
    valid = [v for v in values if v is not None]
    return sum(valid) / len(valid) if valid else None


def _percentile(values, p):
    """计算分位数(p∈[0,100]);values 为数值列表."""
    valid = sorted([v for v in values if v is not None])
    if not valid:
        return None
    if len(valid) == 1:
        return float(valid[0])
    k = (len(valid) - 1) * (p / 100.0)
    lo = int(k)
    hi = min(lo + 1, len(valid) - 1)
    frac = k - lo
    return float(valid[lo] + (valid[hi] - valid[lo]) * frac)


# 召回时间效率:分阶段耗时字段名 -> 中文标签
TIMING_FIELDS = [
    ("embed_ms", "query 向量化"),
    ("bm25_ms", "BM25 检索"),
    ("rag_ms", "向量检索"),
    ("fuse_ms", "融合"),
    ("rerank_ms", "rerank"),
    ("section_ms", "段落召回"),
    ("total_ms", "总耗时"),
]


def _aggregate_timing(items):
    """聚合分阶段耗时:mean/P50/P95/max."""
    out = {}
    for field, _label in TIMING_FIELDS:
        vals = [i.get(field, 0) for i in items]
        out[field] = {
            "mean": _avg(vals),
            "p50": _percentile(vals, 50),
            "p95": _percentile(vals, 95),
            "max": max(vals) if vals else None,
        }
    return out


def summarize(metrics_list, strategy):
    """按整体 + 按 category 汇总指标."""
    def agg(items):
        out = {}
        out["count"] = len(items)
        out["recall@5"] = _avg([i["recall@5"] for i in items])
        out["recall@20"] = _avg([i["recall@20"] for i in items])
        out["mrr"] = _avg([i["rr"] for i in items])
        out["bm25_recall@20"] = _avg([i["bm25_recall@20"] for i in items])
        out["rag_recall@20"] = _avg([i["rag_recall@20"] for i in items])
        out["avg_elapsed_ms"] = _avg([i["elapsed_ms"] for i in items])
        # 段落召回命中率
        sec_hits = [i["section_hit"] for i in items if i["section_hit"] is not None]
        out["section_hit_rate"] = sum(1 for x in sec_hits if x) / len(sec_hits) if sec_hits else None
        # fallback(section_fallback 子集)
        fb_items = [i for i in items if i["category"] == "section_fallback"]
        if fb_items:
            triggered = sum(1 for i in fb_items if i["fallback_triggered"])
            reasonable = sum(1 for i in fb_items if i["fallback_reasonable"] is True)
            out["fallback_trigger_rate"] = triggered / len(fb_items)
            out["fallback_reasonable_rate"] = reasonable / len(fb_items)
        # citation 子集
        cite_items = [i for i in items if i["category"] == "citation"]
        if cite_items:
            out["citation_acc"] = sum(1 for i in cite_items if i["citation_ok"] is True) / len(cite_items)
        # refusal 子集
        ref_items = [i for i in items if i["category"] == "refusal"]
        if ref_items:
            out["refusal_rate"] = sum(1 for i in ref_items if i["refusal_ok"] is True) / len(ref_items)
        # 通过率
        out["pass_rate"] = sum(1 for i in items if i["pass"]) / len(items) if items else 0
        # 召回时间效率(分阶段)
        out["timing"] = _aggregate_timing(items)
        return out

    overall = agg(metrics_list)
    by_category = {}
    for cat in sorted({i["category"] for i in metrics_list}):
        by_category[cat] = agg([i for i in metrics_list if i["category"] == cat])
    return overall, by_category


# ==================== 报告生成 ====================

def _fmt(v, pct=True):
    if v is None:
        return "-"
    if pct:
        return f"{v*100:.1f}%" if isinstance(v, float) else str(v)
    return f"{v:.1f}" if isinstance(v, float) else str(v)


def _fmt_ms(v):
    """耗时格式化:毫秒,保留 1 位小数."""
    if v is None:
        return "-"
    return f"{v:.1f}"


def generate_report(metrics_list, overall, by_category, strategy, eval_meta):
    """生成 Markdown 报告."""
    lines = []
    lines.append("# RAG 专项评测报告\n")
    lines.append(f"- 评测时间: {time.strftime('%Y-%m-%d %H:%M:%S')}")
    lines.append(f"- 检索策略(strategy): `{strategy}`")
    lines.append(f"- 评测集: `scripts/eval/rag_eval.jsonl`")
    lines.append(f"- Case 总数: {overall['count']}")
    lines.append(f"- doc-service: `{DOC_SERVICE_URL}`")
    lines.append(f"- 融合方式: 混合策略(top-5 RRF 精排 + top-20 加权融合召回, k=60, bm25_weight=0.3, rag_weight=0.7)")
    lines.append(f"- 拒答 cosine 阈值: {REFUSAL_COSINE_THRESHOLD}(fused top1 的 rag_raw_score)\n")

    # 整体指标汇总
    lines.append("## 一、整体指标汇总\n")
    lines.append("| 指标 | 数值 |")
    lines.append("|------|------|")
    lines.append(f"| Case 总数 | {overall['count']} |")
    lines.append(f"| 通过率 | {_fmt(overall['pass_rate'])} |")
    lines.append(f"| Recall@5 | {_fmt(overall['recall@5'])} |")
    lines.append(f"| Recall@20 | {_fmt(overall['recall@20'])} |")
    lines.append(f"| MRR | {_fmt(overall['mrr'], pct=False)} |")
    lines.append(f"| BM25-only Recall@20 | {_fmt(overall['bm25_recall@20'])} |")
    lines.append(f"| RAG-only Recall@20 | {_fmt(overall['rag_recall@20'])} |")
    lines.append(f"| 段落召回命中率 | {_fmt(overall['section_hit_rate'])} |")
    if overall.get("fallback_trigger_rate") is not None:
        lines.append(f"| fallback 触发率 | {_fmt(overall['fallback_trigger_rate'])} |")
        lines.append(f"| fallback 合理率 | {_fmt(overall['fallback_reasonable_rate'])} |")
    if overall.get("citation_acc") is not None:
        lines.append(f"| 引用准确率(citation) | {_fmt(overall['citation_acc'])} |")
    if overall.get("refusal_rate") is not None:
        lines.append(f"| 拒答率(refusal) | {_fmt(overall['refusal_rate'])} |")
    lines.append(f"| 平均耗时(ms) | {_fmt(overall['avg_elapsed_ms'], pct=False)} |")
    t_total = overall.get("timing", {}).get("total_ms", {})
    if t_total:
        lines.append(f"| 总耗时 P50(ms) | {_fmt_ms(t_total.get('p50'))} |")
        lines.append(f"| 总耗时 P95(ms) | {_fmt_ms(t_total.get('p95'))} |")
        lines.append(f"| 总耗时 max(ms) | {_fmt_ms(t_total.get('max'))} |")
    lines.append("")

    # 按场景分类
    lines.append("## 二、按场景分类指标\n")
    lines.append("| 场景 | Case数 | 通过率 | Recall@5 | Recall@20 | MRR | BM25@20 | RAG@20 | 段落命中 |")
    lines.append("|------|--------|--------|----------|-----------|-----|---------|--------|----------|")
    for cat, s in by_category.items():
        lines.append(
            f"| {cat} | {s['count']} | {_fmt(s['pass_rate'])} | {_fmt(s['recall@5'])} | "
            f"{_fmt(s['recall@20'])} | {_fmt(s['mrr'], pct=False)} | {_fmt(s['bm25_recall@20'])} | "
            f"{_fmt(s['rag_recall@20'])} | {_fmt(s['section_hit_rate'])} |"
        )
    lines.append("")

    # 召回时间效率(分阶段)
    lines.append("## 三、召回时间效率(分阶段耗时)\n")
    lines.append("> 单位毫秒。定位召回链路性能瓶颈:embed(向量化)/ BM25 / 向量 / 融合 / rerank / 段落召回 / 总耗时。\n")
    lines.append("| 阶段 | mean | P50 | P95 | max |")
    lines.append("|------|------|-----|-----|-----|")
    for field, label in TIMING_FIELDS:
        t = overall.get("timing", {}).get(field, {})
        if not t:
            continue
        lines.append(
            f"| {label} | {_fmt_ms(t.get('mean'))} | {_fmt_ms(t.get('p50'))} | "
            f"{_fmt_ms(t.get('p95'))} | {_fmt_ms(t.get('max'))} |"
        )
    lines.append("")
    # 按场景的时间效率对比(只看 total_ms)
    lines.append("按场景的总耗时分布:\n")
    lines.append("| 场景 | mean(ms) | P50(ms) | P95(ms) | max(ms) |")
    lines.append("|------|----------|---------|---------|---------|")
    for cat, s in by_category.items():
        t = s.get("timing", {}).get("total_ms", {})
        if not t:
            continue
        lines.append(
            f"| {cat} | {_fmt_ms(t.get('mean'))} | {_fmt_ms(t.get('p50'))} | "
            f"{_fmt_ms(t.get('p95'))} | {_fmt_ms(t.get('max'))} |"
        )
    lines.append("")

    # 多路召回贡献度
    lines.append("## 四、多路召回贡献度对比\n")
    lines.append("> 对比 BM25-only / RAG-only / Fused 三路在 Recall@20 上的表现,定位融合增益。\n")
    lines.append("| 场景 | BM25@20 | RAG@20 | Fused@20 | 融合增益(vs 较强者) |")
    lines.append("|------|---------|--------|----------|---------------------|")
    for cat, s in by_category.items():
        bm = s["bm25_recall@20"] or 0
        rg = s["rag_recall@20"] or 0
        fu = s["recall@20"] or 0
        gain = fu - max(bm, rg)
        gain_str = f"{gain*100:+.1f}%" if gain is not None else "-"
        lines.append(f"| {cat} | {_fmt(bm)} | {_fmt(rg)} | {_fmt(fu)} | {gain_str} |")
    lines.append("")

    # 每条 case 详情
    lines.append("## 五、每条 Case 详情\n")
    lines.append("| ID | 场景 | 查询(截断) | Recall@5 | Recall@20 | MRR | 段落命中 | 总耗时(ms) | 命中文档 | 未命中文档 | 通过 |")
    lines.append("|----|------|-----------|----------|-----------|-----|----------|-----------|----------|------------|------|")
    for m in metrics_list:
        q = m["query"][:30].replace("|", "/")
        hit = ", ".join(m["hit_docs"]) if m["hit_docs"] else "-"
        miss = ", ".join(m["miss_docs"]) if m["miss_docs"] else "-"
        sec = "-" if m["section_hit"] is None else ("是" if m["section_hit"] else "否")
        passed = "是" if m["pass"] else "否"
        lines.append(
            f"| {m['id']} | {m['category']} | {q} | {_fmt(m['recall@5'])} | "
            f"{_fmt(m['recall@20'])} | {_fmt(m['rr'], pct=False)} | {sec} | {m.get('total_ms', m.get('elapsed_ms', 0))} | {hit} | {miss} | {passed} |"
        )
    lines.append("")

    # 失败 case 列表
    fails = [m for m in metrics_list if not m["pass"]]
    lines.append("## 六、失败 Case 列表(便于人工归因)\n")
    if not fails:
        lines.append("无失败 case。\n")
    else:
        lines.append(f"共 {len(fails)} 条失败 case:\n")
        lines.append("| ID | 场景 | 查询 | 失败原因诊断 | top1_doc | top1_rrf | rag_raw |")
        lines.append("|----|------|------|-------------|----------|----------|---------|")
        for m in fails:
            reasons = []
            if m["recall@20"] is not None and m["recall@20"] < 1.0:
                reasons.append(f"文档召回不全(miss={m['miss_docs']})")
            if m["section_hit"] is False:
                reasons.append("段落章节未命中")
            if m["category"] == "citation" and m["citation_ok"] is False:
                reasons.append("引用 doc/page/section 不匹配")
            if m["category"] == "refusal" and m["refusal_ok"] is False:
                reasons.append(f"拒答失败(rag_raw={m['top1_rag_raw']:.3f}≥{REFUSAL_COSINE_THRESHOLD})")
            if m["category"] == "section_fallback" and m["fallback_reasonable"] is False:
                reasons.append("fallback 触发但不合理(章节过短)")
            reason = "; ".join(reasons) if reasons else "综合未通过"
            q = m["query"][:40].replace("|", "/")
            lines.append(
                f"| {m['id']} | {m['category']} | {q} | {reason} | {m['top1_doc'][:30]} | {m['top1_score']:.4f} | {m['top1_rag_raw']:.3f} |"
            )
        lines.append("")

    # 待人工复核
    lines.append("## 七、待人工复核项\n")
    lines.append("- `expected_page=[]` 的 case 需对照原文回填页码(见任务4)")
    lines.append("- tolerance 标注 `approximate` 的数字答案若与检索结果差异大,标注待人工复核而非直接判错")
    lines.append("- refusal 类拒答率反映 RAG 检索层相关度过滤,Agent 层是否真正拒答见 agent_eval\n")

    return "\n".join(lines)


# ==================== 控制台汇总表 ====================

def print_console_summary(overall, by_category, strategy):
    print("\n" + "=" * 70)
    print(f"  RAG 评测汇总  (strategy={strategy})")
    print("=" * 70)
    print(f"  Case 总数        : {overall['count']}")
    print(f"  通过率           : {_fmt(overall['pass_rate'])}")
    print(f"  Recall@5         : {_fmt(overall['recall@5'])}")
    print(f"  Recall@20        : {_fmt(overall['recall@20'])}")
    print(f"  MRR              : {_fmt(overall['mrr'], pct=False)}")
    print(f"  BM25-only@20     : {_fmt(overall['bm25_recall@20'])}")
    print(f"  RAG-only@20      : {_fmt(overall['rag_recall@20'])}")
    print(f"  段落召回命中率    : {_fmt(overall['section_hit_rate'])}")
    if overall.get("fallback_trigger_rate") is not None:
        print(f"  fallback 触发率  : {_fmt(overall['fallback_trigger_rate'])}")
        print(f"  fallback 合理率  : {_fmt(overall['fallback_reasonable_rate'])}")
    if overall.get("citation_acc") is not None:
        print(f"  引用准确率       : {_fmt(overall['citation_acc'])}")
    if overall.get("refusal_rate") is not None:
        print(f"  拒答率           : {_fmt(overall['refusal_rate'])}")
    print(f"  平均耗时(ms)     : {_fmt(overall['avg_elapsed_ms'], pct=False)}")
    t_total = overall.get("timing", {}).get("total_ms", {})
    if t_total:
        print(f"  总耗时 P50/P95   : {_fmt_ms(t_total.get('p50'))} / {_fmt_ms(t_total.get('p95'))} ms")
    # 分阶段耗时 mean
    t = overall.get("timing", {})
    if t:
        stages = []
        for field, label in TIMING_FIELDS:
            v = t.get(field, {}).get("mean")
            if v is not None:
                stages.append(f"{label}={v:.1f}")
        if stages:
            print("  分阶段(mean)    : " + " | ".join(stages))
    print("-" * 70)
    print(f"  {'场景':<18}{'Case数':>6}{'通过率':>8}{'R@5':>8}{'R@20':>8}{'MRR':>8}{'BM25@20':>10}{'RAG@20':>10}")
    print("-" * 70)
    for cat, s in by_category.items():
        print(
            f"  {cat:<18}{s['count']:>6}{_fmt(s['pass_rate']):>8}"
            f"{_fmt(s['recall@5']):>8}{_fmt(s['recall@20']):>8}"
            f"{_fmt(s['mrr'], pct=False):>8}{_fmt(s['bm25_recall@20']):>10}{_fmt(s['rag_recall@20']):>10}"
        )
    print("=" * 70 + "\n")


# ==================== 主流程 ====================

def main():
    parser = argparse.ArgumentParser(description="RAG 专项评测")
    parser.add_argument("--category", default=None, help="过滤场景:single_doc/cross_doc/section_fallback/multi_recall/refusal/citation")
    parser.add_argument("--strategy", default="fused", choices=["bm25_only", "rag_only", "fused", "fused_rerank"], help="检索策略(A/B 对比)")
    parser.add_argument("--top-k", type=int, default=20, help="top_k(默认 20)")
    parser.add_argument("--out", default=str(REPORT_FILE), help="报告输出路径")
    args = parser.parse_args()

    if not EVAL_FILE.exists():
        print(f"[FATAL] 评测集不存在: {EVAL_FILE}", file=sys.stderr)
        sys.exit(1)

    cases = load_cases(EVAL_FILE)
    if args.category:
        cases = [c for c in cases if c.get("category") == args.category]
    print(f"[INFO] 加载 {len(cases)} 条 case, strategy={args.strategy}, top_k={args.top_k}")

    # 健康检查
    try:
        h = requests.get(f"{DOC_SERVICE_URL}/health", timeout=10).json()
        print(f"[INFO] doc-service 健康: rerank={h.get('rerank_enabled')} memory={h.get('memory_service')}")
    except Exception as e:
        print(f"[WARN] doc-service 健康检查失败: {e}(将继续尝试)")

    metrics_list = []
    fail_count = 0
    for idx, case in enumerate(cases, 1):
        case_id = case.get("id")
        query = case.get("query", "")
        print(f"[{idx}/{len(cases)}] id={case_id} cat={case.get('category')} query={query[:40]}", end=" ")
        resp = call_rag_search(query, top_k=args.top_k, strategy=args.strategy)
        if "_error" in resp:
            print(f"-> ERROR: {resp['_error']}")
            fail_count += 1
            continue
        m = eval_case(case, resp, args.strategy)
        metrics_list.append(m)
        status = "PASS" if m["pass"] else "FAIL"
        r20 = m["recall@20"]
        r20_s = f"{r20*100:.0f}%" if r20 is not None else "-"
        print(f"-> {status} R@20={r20_s} top1={m['top1_doc'][:25]}")

    if not metrics_list:
        print("[FATAL] 无有效评测结果", file=sys.stderr)
        sys.exit(1)

    overall, by_category = summarize(metrics_list, args.strategy)
    print_console_summary(overall, by_category, args.strategy)

    report = generate_report(metrics_list, overall, by_category, args.strategy, {})
    out_path = Path(args.out)
    out_path.write_text(report, encoding="utf-8")
    print(f"[INFO] 报告已生成: {out_path}")
    print(f"[INFO] 失败 case: {sum(1 for m in metrics_list if not m['pass'])}/{len(metrics_list)}")

    # 返回码:有失败 case 返回 1(便于 CI),但基线评测允许失败
    return 0


if __name__ == "__main__":
    sys.exit(main())

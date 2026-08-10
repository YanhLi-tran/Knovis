# -*- coding: utf-8 -*-
"""记忆检索 A/B 实验脚本(阶段 C P2).

用法:
  python memory_ab_experiment.py --param BM25_WEIGHT --values 0.2,0.3,0.4,0.5
  python memory_ab_experiment.py --param RECALL_TOP_N --values 10,20,30 --project exp_consumer_research

流程:
  1. 读取 memory_eval.jsonl(40 条评测集)
  2. 对每个参数值, 通过 X-AB-Override header 覆盖参数(需服务 AB_EXPERIMENT_MODE=true)
  3. 跑 40 条 query, 计算 Recall@5 / Recall@1 / MRR / P50 / keyword 占比
  4. 输出对比表(Markdown + JSON)

输出:
  scripts/ab_reports/memory/{timestamp}_{param}/metrics_{value}.json + comparison.md
"""
import argparse
import json
import os
import sys
import io
import time
import statistics
from pathlib import Path

try:
    import requests
except ImportError:
    print("需要 requests 库: pip install requests")
    sys.exit(1)

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8")

MEMORY_URL = os.getenv("MEMORY_URL", "http://127.0.0.1:8002")
EVAL_FILE = Path(__file__).parent / "memory_eval.jsonl"
PROJECT_ID = os.getenv("MEMORY_EVAL_PROJECT", "exp_consumer_research")
OUTPUT_DIR = Path(__file__).parent.parent / "ab_reports" / "memory"

# 可实验参数白名单(防注入)
ALLOWED_PARAMS = {
    "BM25_WEIGHT": float,
    "RAG_WEIGHT": float,
    "RECALL_TOP_N": int,
    "FINAL_TOP_K": int,
}


def load_cases():
    cases = []
    for line in open(EVAL_FILE, encoding="utf-8"):
        line = line.strip()
        if line.startswith("{"):
            cases.append(json.loads(line))
    return cases


def search(query, top_k, ab_override=""):
    """调用 /search, 返回 (latency_ms, resp)."""
    headers = {"Content-Type": "application/json"}
    if ab_override:
        headers["X-AB-Override"] = ab_override
    t0 = time.time()
    r = requests.post(f"{MEMORY_URL}/search",
                      json={"project_id": PROJECT_ID, "query": query, "top_k": top_k},
                      headers=headers, timeout=60)
    r.raise_for_status()
    return (time.time() - t0) * 1000, r.json()


def run_eval(cases, ab_override="", top_k=5):
    """跑完整评测集, 返回指标 dict."""
    recall5, recall1, mrrs, lats = [], [], [], []
    kw_total, kw_hit = 0, 0
    for c in cases:
        q = c["query"]
        expected = set(c.get("expected_memory_ids", []))
        try:
            dt, resp = search(q, top_k, ab_override)
        except Exception as e:
            print(f"  [WARN] query 失败: {q[:20]}... {e}")
            continue
        lats.append(dt)
        results = resp.get("results", [])
        top5_contents = [x.get("content", "") for x in results[:5]]
        top5_types = [x.get("memory_type", "") for x in results[:5]]

        hit5 = any(any(e in tc for tc in top5_contents) for e in expected)
        hit1 = any(any(e in tc for tc in top5_contents[:1]) for e in expected) if top5_contents else False
        recall5.append(1 if hit5 else 0)
        recall1.append(1 if hit1 else 0)
        rr = 0.0
        for rank, tc in enumerate(top5_contents, 1):
            if any(e in tc for e in expected):
                rr = 1.0 / rank
                break
        mrrs.append(rr)
        kw_total += len(top5_types)
        kw_hit += sum(1 for t in top5_types if t == "keyword")

    n = len(recall5) if recall5 else 1
    return {
        "case_count": len(recall5),
        "recall@5": sum(recall5) / n * 100,
        "recall@1": sum(recall1) / n * 100,
        "mrr": statistics.mean(mrrs) if mrrs else 0,
        "p50_ms": statistics.median(lats) if lats else 0,
        "p95_ms": sorted(lats)[min(int(len(lats) * 0.95), len(lats) - 1)] if lats else 0,
        "keyword_ratio": kw_hit / kw_total * 100 if kw_total else 0,
    }


def main():
    parser = argparse.ArgumentParser(description="记忆检索 A/B 实验")
    parser.add_argument("--param", required=True, help="实验参数(BM25_WEIGHT/RAG_WEIGHT/RECALL_TOP_N/FINAL_TOP_K)")
    parser.add_argument("--values", required=True, help="参数值列表, 逗号分隔(如 0.2,0.3,0.4)")
    parser.add_argument("--top-k", type=int, default=5, help="评测返回数量")
    args = parser.parse_args()

    if args.param not in ALLOWED_PARAMS:
        print(f"不允许的参数: {args.param}. 可选: {list(ALLOWED_PARAMS.keys())}")
        sys.exit(1)

    values = [ALLOWED_PARAMS[args.param](v.strip()) for v in args.values.split(",")]
    cases = load_cases()
    print(f"评测集: {len(cases)} 条 | 参数: {args.param} | 值: {values}")
    print("=" * 70)

    results = {}
    for v in values:
        override = f"{args.param}={v}"
        metrics = run_eval(cases, override, args.top_k)
        results[str(v)] = metrics
        print(f"{args.param}={v}: Recall@5={metrics['recall@5']:.1f}% "
              f"Recall@1={metrics['recall@1']:.1f}% MRR={metrics['mrr']:.3f} "
              f"P50={metrics['p50_ms']:.1f}ms keyword={metrics['keyword_ratio']:.1f}%")

    # 输出
    ts = time.strftime("%Y%m%d_%H%M%S")
    out_dir = OUTPUT_DIR / f"{ts}_{args.param}"
    out_dir.mkdir(parents=True, exist_ok=True)

    for v, m in results.items():
        (out_dir / f"metrics_{v}.json").write_text(
            json.dumps(m, ensure_ascii=False, indent=2), encoding="utf-8")

    # comparison.md
    lines = [
        f"# 记忆检索 A/B 实验: {args.param}",
        "",
        f"- 时间: {ts}",
        f"- 评测集: {len(cases)} 条",
        f"- 参数范围: {values}",
        "",
        "| 参数值 | Recall@5 | Recall@1 | MRR | P50(ms) | P95(ms) | keyword% |",
        "|---|---|---|---|---|---|---|",
    ]
    for v, m in results.items():
        lines.append(f"| {args.param}={v} | {m['recall@5']:.1f}% | {m['recall@1']:.1f}% | "
                     f"{m['mrr']:.3f} | {m['p50_ms']:.1f} | {m['p95_ms']:.1f} | {m['keyword_ratio']:.1f}% |")
    lines.append("")
    best = max(results.items(), key=lambda kv: kv[1]["mrr"])
    lines.append(f"**最佳(MRR): {args.param}={best[0]}** (Recall@5={best[1]['recall@5']:.1f}%, P50={best[1]['p50_ms']:.1f}ms)")
    (out_dir / "comparison.md").write_text("\n".join(lines), encoding="utf-8")
    print(f"\n报告已输出: {out_dir}/comparison.md")


if __name__ == "__main__":
    main()

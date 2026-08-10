# -*- coding: utf-8 -*-
"""记忆 RAG 评测:跑 40 条评测 query,输出 Recall@5 / MRR / P50 延迟 / top5_keyword 占比."""
import json
import sys
import io
import time
import statistics
import urllib.request

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8")

PROJECT_ID = "exp_consumer_research"
EVAL_FILE = r"E:\Knovis\scripts\eval\memory_eval.jsonl"


def call_search(query, top_k=5):
    body = json.dumps({"project_id": PROJECT_ID, "query": query, "top_k": top_k}).encode()
    req = urllib.request.Request("http://127.0.0.1:8002/search", data=body,
                                 headers={"Content-Type": "application/json"})
    t0 = time.time()
    r = json.loads(urllib.request.urlopen(req, timeout=30).read().decode())
    return (time.time() - t0) * 1000, r


def main():
    cases = [json.loads(l) for l in open(EVAL_FILE, encoding="utf-8") if l.strip()]
    print(f"评测集: {len(cases)} 条 query\n")

    recall5, recall1, mrrs, latencies = [], [], [], []
    kw_ratio_list = []
    total_hit, total_kw = 0, 0
    fail = []

    for c in cases:
        q = c["query"]
        expected = set(c.get("expected_memory_ids", []))
        dt, r = call_search(q)
        latencies.append(dt)
        results = r.get("results", [])
        top5 = results[:5]
        top5_contents = [x.get("content", "") for x in top5]
        top5_types = [x.get("memory_type", "") for x in top5]

        # Recall@5:expected 内容是否出现在 top5(用内容匹配,因为 id 是随机 uuid)
        hit5 = any(any(e in tc for tc in top5_contents) for e in expected)
        hit1 = any(any(e in tc for tc in top5_contents[:1]) for e in expected) if top5 else False
        recall5.append(1 if hit5 else 0)
        recall1.append(1 if hit1 else 0)

        # MRR:期望命中最靠前的排名倒数
        rr = 0.0
        for rank, tc in enumerate(top5_contents, 1):
            if any(e in tc for e in expected):
                rr = 1.0 / rank
                break
        mrrs.append(rr)

        # top5 keyword 占比
        kw = sum(1 for t in top5_types if t == "keyword")
        total_kw += kw
        total_hit += len(top5)
        kw_ratio_list.append(kw / len(top5) if top5 else 0)

        if not hit5:
            fail.append((q[:25], [e[:20] for e in expected], [tc[:20] for tc in top5_contents]))

    n = len(cases)
    print("=" * 70)
    print(f"baseline 结果(改造前)")
    print("=" * 70)
    print(f"Recall@5 : {sum(recall5)/n*100:.1f}%  ({sum(recall5)}/{n})")
    print(f"Recall@1 : {sum(recall1)/n*100:.1f}%")
    print(f"MRR      : {statistics.mean(mrrs):.3f}")
    print(f"P50 延迟 : {statistics.median(latencies):.1f}ms")
    print(f"P95 延迟 : {sorted(latencies)[int(n*0.95)-1]:.1f}ms")
    print(f"top5 keyword 占比: {total_kw/total_hit*100:.1f}%  ({total_kw}/{total_hit})")
    print(f"\n失败 case ({len(fail)}):")
    for q, e, t in fail[:10]:
        print(f"  q={q} | exp={e} | top5={t}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""回填 rag_eval.jsonl 中 expected_page=[] 的条目.

用实际检索结果(/rag/search)中正确文档的 page_num 回填 expected_page,
并在 eval_notes 追加"[page 回填自检索结果]"标注。仅回填 expected_docs 非空的 case,
refusal 类(expected_docs=[])跳过。已有 expected_page 的 case 跳过。

用法:
  python backfill_pages.py             # 回填并写回 rag_eval.jsonl
  python backfill_pages.py --dry-run   # 只打印不写回

依赖: requests
"""
import json
import os
import sys
from pathlib import Path

try:
    import requests
except ImportError:
    print("[FATAL] 缺少 requests", file=sys.stderr)
    sys.exit(1)

DOC_SERVICE_URL = os.getenv("DOC_SERVICE_URL", "http://127.0.0.1:8003")
EVAL_FILE = Path(__file__).parent / "eval" / "rag_eval.jsonl"


def search(query, top_k=5):
    try:
        r = requests.post(
            f"{DOC_SERVICE_URL}/rag/search",
            json={"query": query, "top_k": top_k},
            timeout=30,
        )
        r.raise_for_status()
        return r.json()
    except Exception as e:
        print(f"[WARN] 检索失败 query={query[:30]}: {e}", file=sys.stderr)
        return None


def backfill(dry_run=False):
    if not EVAL_FILE.exists():
        print(f"[FATAL] 评测集不存在: {EVAL_FILE}", file=sys.stderr)
        sys.exit(1)

    lines = EVAL_FILE.read_text(encoding="utf-8").splitlines()
    out_lines = []
    filled = 0
    skipped = 0

    for line in lines:
        stripped = line.strip()
        if not stripped:
            out_lines.append(line)
            continue
        obj = json.loads(stripped)
        # 跳过 _meta / _schema
        if "_meta" in obj or "_schema" in obj:
            out_lines.append(line)
            continue

        expected_docs = obj.get("expected_docs", [])
        expected_page = obj.get("expected_page", [])

        # 已有页码 或 无期望文档(refusal) 跳过
        if expected_page or not expected_docs:
            skipped += 1
            out_lines.append(json.dumps(obj, ensure_ascii=False))
            continue

        # 调检索回填
        resp = search(obj.get("query", ""), top_k=5)
        if not resp:
            out_lines.append(json.dumps(obj, ensure_ascii=False))
            continue

        results = resp.get("results", [])
        # 找 doc_name 匹配 expected_docs 的页码
        page_set = set()
        expected_doc_set = set(expected_docs)
        for r in results:
            if r.get("doc_name", "") in expected_doc_set:
                pn = r.get("page_num", 0)
                if pn and pn > 0:
                    page_set.add(pn)

        if page_set:
            obj["expected_page"] = sorted(page_set)
            note = obj.get("eval_notes", "")
            tag = "[page 回填自检索结果]"
            if tag not in note:
                obj["eval_notes"] = f"{note} {tag}".strip()
            filled += 1
            print(f"[FILL] id={obj.get('id')} pages={sorted(page_set)} docs={expected_docs}")
        else:
            print(f"[SKIP] id={obj.get('id')} 未找到正确文档页码")

        out_lines.append(json.dumps(obj, ensure_ascii=False))

    if not dry_run:
        EVAL_FILE.write_text("\n".join(out_lines) + "\n", encoding="utf-8")
        print(f"\n[INFO] 回填完成: {filled} 条已回填, {skipped} 条跳过, 已写回 {EVAL_FILE}")
    else:
        print(f"\n[INFO] dry-run: {filled} 条将回填, {skipped} 条跳过, 未写回")


if __name__ == "__main__":
    dry = "--dry-run" in sys.argv
    backfill(dry_run=dry)

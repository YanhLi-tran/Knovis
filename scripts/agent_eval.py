#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""Agent 编排评测脚本.

加载 scripts/eval/agent_eval.jsonl,逐条调用 agent-go /chat/stream(SSE 流),
抓取 tool_call / tool_result / thought / output / done 事件,计算 5 项指标 + 时间效率:
  1. 工具选择准确率    —— expected_tool 是否被调用(多工具需全部命中)
  2. 参数填充正确率    —— expected_params 关键参数是否在 tool_call arguments 中出现
  3. 答案事实准确率    —— output 是否匹配 expected_answer_pattern(LLM-as-Judge, deepseek)
  4. 多工具编排顺序    —— multi_tool 类 case 调用顺序是否符合 expected_params.order
  5. 多轮指代消解      —— multi_turn 类 case follow_ups 是否正确继承上下文
  6. 时间效率指标      —— TtFT(首 token 延迟) / Throughput(字符/s) / TPOT(ms/字符) / 总延迟

用法:
  python agent_eval.py                         # 全量评测
  python agent_eval.py --category single_tool  # 按场景过滤
  python agent_eval.py --no-judge              # 跳过 LLM-as-Judge(加速)
  python agent_eval.py --timeout 180           # SSE 单轮超时(秒)

环境变量:
  AGENT_EVAL_TOKEN   —— JWT access token(必填,Authorization: Bearer)
  AGENT_EVAL_LLM_KEY —— 对话用 LLM key(可选,X-LLM-API-Key;未设则依赖用户存储 key 或 .env 兜底)
  LLM_API_KEY        —— LLM-as-Judge 用 deepseek key(必填当 --judge 开启)
  AGENT_GO_URL       —— agent-go 地址(默认 http://127.0.0.1:8001)
  DEEPSEEK_BASE_URL  —— deepseek API 地址(默认 https://api.deepseek.com)

依赖: requests (标准库 + requests,不引入 pytest)
"""
import argparse
import http.client
import json
import os
import re
import socket
import sys
import time
from pathlib import Path
from urllib.parse import urlparse

try:
    import requests
except ImportError:
    print("[FATAL] 缺少 requests,请先 pip install requests", file=sys.stderr)
    sys.exit(1)

AGENT_GO_URL = os.getenv("AGENT_GO_URL", "http://127.0.0.1:8001")
DEEPSEEK_BASE_URL = os.getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
EVAL_FILE = Path(__file__).parent / "eval" / "agent_eval.jsonl"
REPORT_FILE = Path(__file__).parent / "agent_eval_report.md"
SSE_TIMEOUT_DEFAULT = 180  # 单轮 SSE 读取超时(秒),审批类 case 需较长
KNOWN_TOOLS = {
    "geocode_city", "get_weather", "get_weather_forecast", "web_search",
    "ask_user", "file_read", "file_write", "grep", "file_list",
    "sandbox_exec", "load_skill", "rag_search",
}


# ==================== 数据加载 ====================

def load_cases(path: Path):
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


# ==================== Session 管理 ====================

def create_session(token: str, title: str = "agent-eval"):
    """创建一个无项目 session,返回 session_id."""
    try:
        resp = requests.post(
            f"{AGENT_GO_URL}/sessions",
            headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            json={"title": title},
            timeout=15,
        )
        resp.raise_for_status()
        return resp.json().get("id", "")
    except Exception as e:
        print(f"[WARN] 创建 session 失败: {e}", file=sys.stderr)
        return ""


def delete_session(token: str, session_id: str):
    """评测结束清理 session."""
    if not session_id:
        return
    try:
        requests.delete(
            f"{AGENT_GO_URL}/sessions/{session_id}",
            headers={"Authorization": f"Bearer {token}"},
            timeout=10,
        )
    except Exception:
        pass


# ==================== SSE 对话 ====================

def chat_stream(token: str, query: str, session_id: str, llm_key: str, timeout: int):
    """调用 /chat/stream,逐行读取 SSE,返回事件列表 + 最终 answer + 时间效率指标.

    使用 http.client 逐字节读取 SSE 流,确保不遗漏最后的 done 事件
    (requests.iter_lines 在连接关闭时可能丢失最后几个事件).

    返回: {"events": [...], "answer": str, "tool_calls": [...], "tool_results": [...],
           "thoughts": str, "outputs": str, "error": str|None,
           "timing": {"ttft_ms": float, "total_ms": float, "output_chars": int,
                      "throughput_cps": float, "tpot_ms_per_char": float,
                      "t_request_start": float, "t_first_token": float|None, "t_done": float|None,
                      "event_count": int, "first_event_type": str}}
    """
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
        "Accept": "text/event-stream",
    }
    if llm_key:
        headers["X-LLM-API-Key"] = llm_key
    body_dict = {"query": query}
    if session_id:
        body_dict["session_id"] = session_id
    body = json.dumps(body_dict).encode("utf-8")

    events = []
    tool_calls = []
    tool_results = []
    thoughts_parts = []
    output_parts = []
    answer = ""
    error = None

    # 时间效率采集
    t_request_start = time.perf_counter()
    t_first_event = None
    t_first_token = None  # 首 token:首个 thought(streaming=true) 或 output 事件
    t_done = None
    first_event_type = ""

    # 解析 AGENT_GO_URL 为 host + port
    parsed = urlparse(AGENT_GO_URL)
    host = parsed.hostname or "127.0.0.1"
    port = parsed.port or 80

    conn = None
    try:
        conn = http.client.HTTPConnection(host, port, timeout=timeout)
        conn.request("POST", "/chat/stream", body=body, headers=headers)
        resp = conn.getresponse()
        if resp.status != 200:
            error = f"HTTP {resp.status}: {resp.read(200).decode('utf-8', errors='replace')}"
            return _pack(events, tool_calls, tool_results, thoughts_parts, output_parts, answer, error,
                         _build_timing(t_request_start, t_first_event, t_first_token, t_done, 0, first_event_type, len(events)))

        # 逐字节读取 SSE 流,确保不遗漏最后的 done 事件
        # (requests.iter_lines 在连接关闭时可能丢失最后几个事件)
        buffer = ""
        while True:
            chunk = resp.read(1)
            if not chunk:
                break
            buffer += chunk.decode("utf-8", errors="replace")
            # 处理完整的 SSE 事件(以 \n\n 分隔)
            while "\n\n" in buffer:
                event_str, buffer = buffer.split("\n\n", 1)
                for line in event_str.split("\n"):
                    line = line.strip()
                    if line.startswith(":"):
                        continue
                    if not line.startswith("data:"):
                        continue
                    payload = line[len("data:"):].strip()
                    if not payload:
                        continue
                    try:
                        evt = json.loads(payload)
                    except json.JSONDecodeError:
                        continue
                    now = time.perf_counter()
                    if t_first_event is None:
                        t_first_event = now
                        first_event_type = evt.get("type", "")
                    etype = evt.get("type", "")
                    edata = evt.get("data", {}) or {}
                    events.append({"type": etype, "data": edata})

                    # 首 token 判定:thought(streaming=true) 或 output(content 非空)
                    if t_first_token is None:
                        if etype == "thought" and edata.get("streaming"):
                            t_first_token = now
                        elif etype == "output" and edata.get("content"):
                            t_first_token = now

                    if etype == "tool_call":
                        raw_args = edata.get("arguments", {}) or {}
                        # arguments 可能是 JSON 字符串(OpenAI FC 协议),需解析为 dict
                        if isinstance(raw_args, str):
                            try:
                                raw_args = json.loads(raw_args)
                            except json.JSONDecodeError:
                                raw_args = {}
                        tool_calls.append({
                            "tool_name": edata.get("tool_name", ""),
                            "tool_call_id": edata.get("tool_call_id", ""),
                            "arguments": raw_args,
                        })
                    elif etype == "tool_result":
                        tool_results.append({
                            "tool_name": edata.get("tool_name", ""),
                            "tool_call_id": edata.get("tool_call_id", ""),
                            "content": edata.get("content", ""),
                            "error": edata.get("error", ""),
                        })
                    elif etype == "thought":
                        if edata.get("streaming"):
                            thoughts_parts.append(edata.get("content", ""))
                        elif edata.get("content"):
                            thoughts_parts.append(edata.get("content", ""))
                    elif etype == "output":
                        if edata.get("content"):
                            output_parts.append(edata.get("content", ""))
                    elif etype == "done":
                        answer = edata.get("answer", "") or ""
                        t_done = now
                    elif etype == "error":
                        error = edata.get("message") or edata.get("error") or "unknown error"
                        if t_done is None:
                            t_done = now
    except socket.timeout:
        error = f"SSE 读取超时({timeout}s)"
    except Exception as e:
        error = str(e)
    finally:
        if conn:
            conn.close()

    # 若 done 未给出 answer,用 output 拼接兜底
    if not answer and output_parts:
        answer = "".join(output_parts)
    if t_done is None:
        t_done = time.perf_counter()
    timing = _build_timing(t_request_start, t_first_event, t_first_token, t_done,
                           len(answer), first_event_type, len(events))
    return _pack(events, tool_calls, tool_results, thoughts_parts, output_parts, answer, error, timing)


def _build_timing(t_start, t_first_event, t_first_token, t_done, answer_chars, first_event_type, event_count):
    """计算 TtFT / Throughput / TPOT 等时间效率指标."""
    total_ms = (t_done - t_start) * 1000.0 if (t_done and t_start) else 0.0
    ttft_ms = (t_first_token - t_start) * 1000.0 if (t_first_token and t_start) else None
    first_event_ms = (t_first_event - t_start) * 1000.0 if (t_first_event and t_start) else None
    # Throughput:字符/秒(基于 answer 字符数 / 总耗时)
    throughput_cps = (answer_chars / (total_ms / 1000.0)) if (total_ms > 0 and answer_chars > 0) else None
    # TPOT:ms/字符 = (总耗时 - TtFT) / 字符数
    if ttft_ms is not None and answer_chars > 0 and total_ms > ttft_ms:
        tpot = (total_ms - ttft_ms) / answer_chars
    else:
        tpot = None
    return {
        "ttft_ms": ttft_ms,
        "first_event_ms": first_event_ms,
        "total_ms": total_ms,
        "output_chars": answer_chars,
        "throughput_cps": throughput_cps,
        "tpot_ms_per_char": tpot,
        "t_request_start": t_start,
        "t_first_token": t_first_token,
        "t_done": t_done,
        "event_count": event_count,
        "first_event_type": first_event_type,
    }


def _pack(events, tool_calls, tool_results, thoughts_parts, output_parts, answer, error, timing=None):
    return {
        "events": events,
        "tool_calls": tool_calls,
        "tool_results": tool_results,
        "thoughts": "".join(thoughts_parts),
        "outputs": "".join(output_parts),
        "answer": answer,
        "error": error,
        "timing": timing or {},
    }


# ==================== 参数匹配 ====================

def _extract_keywords(desc):
    """从参数描述中提取引号内关键词,如 \"包含'五粮液'和'2021'\" -> ['五粮液','2021']."""
    if not isinstance(desc, str):
        desc = str(desc)
    # 单引号或双引号内的词
    kws = re.findall(r"[\'\"]([^\'\"]+)[\'\"]", desc)
    if kws:
        return kws
    # 无引号:去掉前缀"包含"/"为"等,整体作为一个关键词
    cleaned = re.sub(r"^(包含|为|是|等于)\s*", "", desc).strip()
    if cleaned:
        return [cleaned]
    return []


def _arg_matches(arg_value, desc):
    """检查单个参数值是否满足描述."""
    if arg_value is None:
        return False
    arg_str = str(arg_value)
    kws = _extract_keywords(desc)
    if not kws:
        return True  # 无关键词约束,视为通过
    return all(k.lower() in arg_str.lower() for k in kws)


def check_params(tool_calls, expected_params):
    """参数填充正确率:expected_params 中每个 key 是否在某个 tool_call arguments 中满足."""
    if not expected_params:
        return True, []  # 无参数约束
    # order 字段单独处理(多工具顺序),不在此校验
    param_keys = {k: v for k, v in expected_params.items() if k != "order"}
    if not param_keys:
        return True, []
    details = []
    all_ok = True
    for key, desc in param_keys.items():
        # 在所有 tool_call 的 arguments 中查找该 key
        matched = False
        for tc in tool_calls:
            args = tc.get("arguments", {})
            if key in args:
                if _arg_matches(args[key], desc):
                    matched = True
                    break
        if not matched:
            all_ok = False
            details.append(f"参数 {key} 未满足({desc})")
    return all_ok, details


def check_tool_order(tool_calls, expected_params):
    """多工具编排顺序:从 expected_params.order 提取工具名顺序,对比实际调用顺序."""
    order_desc = expected_params.get("order", "") if expected_params else ""
    if not order_desc:
        return None, []  # 无顺序约束
    # 从 order 描述中提取已知工具名(按出现顺序)
    expected_order = []
    for t in KNOWN_TOOLS:
        if t in order_desc:
            expected_order.append(t)
    if not expected_order:
        return None, []  # 无法解析
    # 实际调用顺序(去重,保留首次出现)
    actual_order = []
    for tc in tool_calls:
        name = tc.get("tool_name", "")
        if name and name not in actual_order:
            actual_order.append(name)
    # 检查 expected_order 是否为 actual_order 的子序列
    ok = _is_subsequence(expected_order, actual_order)
    return ok, [f"期望顺序{expected_order} 实际{actual_order}"]


def _is_subsequence(short, long):
    it = iter(long)
    return all(item in it for item in short)


# ==================== LLM-as-Judge ====================

def llm_judge(judge_key: str, query: str, expected_pattern: str, answer: str):
    """用 deepseek 判断 answer 是否匹配 expected_pattern.

    返回: (ok: bool, reason: str)
    """
    if not judge_key:
        return False, "LLM_API_KEY 未配置,跳过 Judge"
    if not answer:
        return False, "answer 为空"
    prompt = (
        "你是一个严格的答案评判员。请判断【模型回答】是否满足【预期答案特征】。\n\n"
        f"【用户问题】{query}\n"
        f"【预期答案特征】{expected_pattern}\n"
        f"【模型回答】{answer}\n\n"
        "判断标准:模型回答应包含预期答案特征所要求的关键事实(数字/实体/结论),"
        "数字允许合理近似(±容差范围内)。对于工具未执行成功(如审批未通过)导致的回答缺失,判为否。\n"
        "请只输出一行 JSON: {\"ok\": true/false, \"reason\": \"简短理由\"}"
    )
    headers = {
        "Authorization": f"Bearer {judge_key}",
        "Content-Type": "application/json",
    }
    body = {
        "model": "deepseek-chat",
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0.0,
        "max_tokens": 200,
    }
    try:
        resp = requests.post(
            f"{DEEPSEEK_BASE_URL}/v1/chat/completions",
            headers=headers, json=body, timeout=30,
        )
        resp.raise_for_status()
        content = resp.json()["choices"][0]["message"]["content"].strip()
        # 提取 JSON
        m = re.search(r"\{[^{}]*\}", content)
        if m:
            obj = json.loads(m.group(0))
            return bool(obj.get("ok")), obj.get("reason", "")
        return False, f"Judge 响应解析失败: {content[:100]}"
    except Exception as e:
        return False, f"Judge 调用失败: {e}"


# ==================== 单 case 评测 ====================

def eval_single_turn(case, result, judge_key, use_judge):
    """单轮 case 评测(single_tool / multi_tool)."""
    expected_tools = case.get("expected_tool", [])
    expected_params = case.get("expected_params", {}) or {}
    expected_pattern = case.get("expected_answer_pattern", "")
    category = case.get("category", "")

    tool_calls = result["tool_calls"]
    called_tools = [tc["tool_name"] for tc in tool_calls]
    called_set = set(called_tools)

    m = {
        "id": case.get("id"),
        "category": category,
        "query": case.get("query", ""),
        "called_tools": called_tools,
        "expected_tools": expected_tools,
        "answer": result["answer"],
        "error": result.get("error"),
        "thoughts": result["thoughts"][:300],
        # 时间效率指标(单轮 case 直接用首轮 timing)
        "timing": result.get("timing") or {},
    }

    # 1) 工具选择准确率
    if expected_tools:
        m["tool_select_ok"] = all(t in called_set for t in expected_tools)
        m["tool_select_missing"] = [t for t in expected_tools if t not in called_set]
    else:
        m["tool_select_ok"] = True
        m["tool_select_missing"] = []

    # 2) 参数填充正确率
    param_ok, param_details = check_params(tool_calls, expected_params)
    m["param_ok"] = param_ok
    m["param_details"] = param_details

    # 3) 答案事实准确率(LLM-as-Judge)
    if use_judge and expected_pattern:
        ok, reason = llm_judge(judge_key, case.get("query", ""), expected_pattern, result["answer"])
        m["answer_ok"] = ok
        m["answer_reason"] = reason
    else:
        m["answer_ok"] = None
        m["answer_reason"] = "Judge 未启用" if not use_judge else "无预期答案特征"

    # 4) 多工具编排顺序(仅 multi_tool)
    if category == "multi_tool":
        order_ok, order_details = check_tool_order(tool_calls, expected_params)
        m["order_ok"] = order_ok
        m["order_details"] = order_details
    else:
        m["order_ok"] = None
        m["order_details"] = []

    m["pass"] = _overall_pass(m, category)
    return m


def eval_multi_turn(case, token, llm_key, judge_key, use_judge, timeout):
    """多轮 case 评测(multi_turn):首轮 + follow_ups."""
    expected_tools = case.get("expected_tool", [])
    expected_params = case.get("expected_params", {}) or {}
    follow_ups = case.get("follow_ups", []) or []
    category = case.get("category", "")

    session_id = create_session(token, title=f"eval-multi-{case.get('id')}")
    # 首轮
    r1 = chat_stream(token, case.get("query", ""), session_id, llm_key, timeout)
    called1 = [tc["tool_name"] for tc in r1["tool_calls"]]
    # 多轮 timing 聚合:首轮 + 所有 follow_up
    rounds_timing = [r1.get("timing") or {}]

    m = {
        "id": case.get("id"),
        "category": category,
        "query": case.get("query", ""),
        "called_tools": called1,
        "expected_tools": expected_tools,
        "answer": r1["answer"],
        "error": r1.get("error"),
        "thoughts": r1["thoughts"][:300],
        "follow_ups": [],
        # 时间效率:首轮 timing(主指标)+ 多轮聚合(在 follow_ups 完成后计算)
        "timing": r1.get("timing") or {},
    }

    # 首轮工具选择
    if expected_tools:
        m["tool_select_ok"] = all(t in set(called1) for t in expected_tools)
        m["tool_select_missing"] = [t for t in expected_tools if t not in set(called1)]
    else:
        m["tool_select_ok"] = True
        m["tool_select_missing"] = []

    # 首轮参数
    param_ok, param_details = check_params(r1["tool_calls"], expected_params)
    m["param_ok"] = param_ok
    m["param_details"] = param_details
    m["order_ok"] = None
    m["order_details"] = []

    # 首轮答案 Judge
    if use_judge and case.get("expected_answer_pattern"):
        ok, reason = llm_judge(judge_key, case.get("query", ""), case["expected_answer_pattern"], r1["answer"])
        m["answer_ok"] = ok
        m["answer_reason"] = reason
    else:
        m["answer_ok"] = None
        m["answer_reason"] = "Judge 未启用"

    # 5) 多轮指代消解:逐个 follow_up
    fu_all_ok = True
    for i, fu in enumerate(follow_ups):
        fu_query = fu.get("query", "")
        fu_expected_tools = fu.get("expected_tool", []) or []
        fu_note = fu.get("expected_note", "")
        rf = chat_stream(token, fu_query, session_id, llm_key, timeout)
        fu_called = [tc["tool_name"] for tc in rf["tool_calls"]]
        fu_tool_ok = all(t in set(fu_called) for t in fu_expected_tools) if fu_expected_tools else True
        # follow_up 上下文继承:用 LLM Judge 判断 fu_note 是否满足
        if use_judge and fu_note:
            fu_answer_ok, fu_reason = llm_judge(judge_key, fu_query, fu_note, rf["answer"])
        else:
            fu_answer_ok = None
            fu_reason = "Judge 未启用"
        if not fu_tool_ok or fu_answer_ok is False:
            fu_all_ok = False
        m["follow_ups"].append({
            "round": i + 1,
            "query": fu_query,
            "expected_tools": fu_expected_tools,
            "called_tools": fu_called,
            "tool_ok": fu_tool_ok,
            "answer": rf["answer"],
            "answer_ok": fu_answer_ok,
            "answer_reason": fu_reason,
            "error": rf.get("error"),
            "timing": rf.get("timing") or {},
        })
        rounds_timing.append(rf.get("timing") or {})

    # 多轮 timing 聚合:sum(total_ms) / sum(output_chars)
    m["timing"] = _aggregate_multi_turn_timing(rounds_timing)
    m["multi_turn_ok"] = fu_all_ok
    m["pass"] = _overall_pass(m, category)
    delete_session(token, session_id)
    return m


def _overall_pass(m, category):
    """单条 case 综合是否通过."""
    if category == "multi_tool":
        return bool(m.get("tool_select_ok")) and bool(m.get("param_ok")) and \
               (m.get("order_ok") is None or m.get("order_ok") is True) and \
               (m.get("answer_ok") is not False)
    if category == "multi_turn":
        return bool(m.get("tool_select_ok")) and bool(m.get("multi_turn_ok")) and \
               (m.get("answer_ok") is not False)
    # single_tool
    return bool(m.get("tool_select_ok")) and bool(m.get("param_ok")) and \
           (m.get("answer_ok") is not False)


def _aggregate_multi_turn_timing(rounds_timing):
    """多轮 case 的时间效率聚合:总耗时 = 各轮之和,输出字符 = 各轮之和,TtFT 取首轮."""
    if not rounds_timing:
        return {}
    first = rounds_timing[0] or {}
    total_ms = sum((t.get("total_ms") or 0) for t in rounds_timing)
    output_chars = sum((t.get("output_chars") or 0) for t in rounds_timing)
    ttft_ms = first.get("ttft_ms")
    throughput_cps = (output_chars / (total_ms / 1000.0)) if (total_ms > 0 and output_chars > 0) else None
    if ttft_ms is not None and output_chars > 0 and total_ms > ttft_ms:
        tpot = (total_ms - ttft_ms) / output_chars
    else:
        tpot = None
    return {
        "ttft_ms": ttft_ms,
        "first_event_ms": first.get("first_event_ms"),
        "total_ms": total_ms,
        "output_chars": output_chars,
        "throughput_cps": throughput_cps,
        "tpot_ms_per_char": tpot,
        "event_count": sum((t.get("event_count") or 0) for t in rounds_timing),
        "first_event_type": first.get("first_event_type", ""),
        "rounds": len(rounds_timing),
    }


# ==================== 汇总统计 ====================

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


def _agg_timing(items):
    """聚合 timing 指标:mean/P50/P95/max."""
    def _collect(field):
        return [((i.get("timing") or {}).get(field)) for i in items]
    out = {}
    for field in ("ttft_ms", "total_ms", "throughput_cps", "tpot_ms_per_char", "output_chars"):
        vals = [v for v in _collect(field) if v is not None]
        out[field] = {
            "mean": (sum(vals) / len(vals)) if vals else None,
            "p50": _percentile(vals, 50),
            "p95": _percentile(vals, 95),
            "max": max(vals) if vals else None,
            "count": len(vals),
        }
    return out


def summarize(metrics_list):
    def agg(items):
        out = {}
        out["count"] = len(items)
        out["tool_select_rate"] = sum(1 for i in items if i.get("tool_select_ok")) / len(items) if items else 0
        out["param_rate"] = sum(1 for i in items if i.get("param_ok")) / len(items) if items else 0
        judged = [i for i in items if i.get("answer_ok") is not None]
        out["answer_rate"] = sum(1 for i in judged if i["answer_ok"]) / len(judged) if judged else None
        out["answer_judged"] = len(judged)
        mt = [i for i in items if i.get("multi_turn_ok") is not None]
        out["multi_turn_rate"] = sum(1 for i in mt if i["multi_turn_ok"]) / len(mt) if mt else None
        mo = [i for i in items if i.get("order_ok") is not None]
        out["order_rate"] = sum(1 for i in mo if i["order_ok"]) / len(mo) if mo else None
        out["pass_rate"] = sum(1 for i in items if i.get("pass")) / len(items) if items else 0
        # 时间效率聚合
        out["timing"] = _agg_timing(items)
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


def _fmt_cps(v):
    """吞吐量格式化:字符/秒,保留 1 位小数."""
    if v is None:
        return "-"
    return f"{v:.1f}"


def generate_report(metrics_list, overall, by_category, use_judge):
    lines = []
    lines.append("# Agent 编排评测报告\n")
    lines.append(f"- 评测时间: {time.strftime('%Y-%m-%d %H:%M:%S')}")
    lines.append(f"- agent-go: `{AGENT_GO_URL}`")
    lines.append(f"- 评测集: `scripts/eval/agent_eval.jsonl`")
    lines.append(f"- Case 总数: {overall['count']}")
    lines.append(f"- LLM-as-Judge: {'启用(deepseek)' if use_judge else '未启用'}\n")

    # 整体指标
    lines.append("## 一、整体指标汇总\n")
    lines.append("| 指标 | 数值 |")
    lines.append("|------|------|")
    lines.append(f"| Case 总数 | {overall['count']} |")
    lines.append(f"| 通过率 | {_fmt(overall['pass_rate'])} |")
    lines.append(f"| 工具选择准确率 | {_fmt(overall['tool_select_rate'])} |")
    lines.append(f"| 参数填充正确率 | {_fmt(overall['param_rate'])} |")
    lines.append(f"| 答案事实准确率(Judge) | {_fmt(overall['answer_rate'])} (判定 {overall['answer_judged']} 条) |")
    if overall.get("order_rate") is not None:
        lines.append(f"| 多工具编排顺序 | {_fmt(overall['order_rate'])} |")
    if overall.get("multi_turn_rate") is not None:
        lines.append(f"| 多轮指代消解 | {_fmt(overall['multi_turn_rate'])} |")
    # 时间效率指标(整体 mean)
    t = overall.get("timing", {})
    ttft = t.get("ttft_ms", {})
    total = t.get("total_ms", {})
    tput = t.get("throughput_cps", {})
    tpot = t.get("tpot_ms_per_char", {})
    if ttft.get("count"):
        lines.append(f"| TtFT mean(ms) | {_fmt_ms(ttft.get('mean'))} |")
        lines.append(f"| TtFT P50/P95(ms) | {_fmt_ms(ttft.get('p50'))} / {_fmt_ms(ttft.get('p95'))} |")
    if total.get("count"):
        lines.append(f"| 总延迟 mean(ms) | {_fmt_ms(total.get('mean'))} |")
        lines.append(f"| 总延迟 P50/P95(ms) | {_fmt_ms(total.get('p50'))} / {_fmt_ms(total.get('p95'))} |")
    if tput.get("count"):
        lines.append(f"| Throughput mean(chars/s) | {_fmt_cps(tput.get('mean'))} |")
    if tpot.get("count"):
        lines.append(f"| TPOT mean(ms/char) | {_fmt_ms(tpot.get('mean'))} |")
    lines.append("")

    # 按场景分类
    lines.append("## 二、按场景分类指标\n")
    lines.append("| 场景 | Case数 | 通过率 | 工具选择 | 参数填充 | 答案准确 | 顺序 | 多轮 |")
    lines.append("|------|--------|--------|----------|----------|----------|------|------|")
    for cat, s in by_category.items():
        lines.append(
            f"| {cat} | {s['count']} | {_fmt(s['pass_rate'])} | {_fmt(s['tool_select_rate'])} | "
            f"{_fmt(s['param_rate'])} | {_fmt(s['answer_rate'])} | {_fmt(s['order_rate'])} | {_fmt(s['multi_turn_rate'])} |"
        )
    lines.append("")

    # 时间效率指标(分场景)
    lines.append("## 三、时间效率指标(TtFT / Throughput / TPOT)\n")
    lines.append("> - **TtFT**(Time to First Token):请求发出到首个 token(thought streaming 或 output)的延迟,反映首响速度\n")
    lines.append("> - **Throughput**:输出吞吐量,字符/秒,反映整体生成速度\n")
    lines.append("> - **TPOT**(Time Per Output Token):每输出字符耗时(ms/字符),(总延迟-TtFT)/输出字符数\n")
    lines.append("> - 多轮 case 的总延迟/字符数为各轮之和,TtFT 取首轮\n\n")
    lines.append("| 场景 | TtFT mean(ms) | TtFT P50 | TtFT P95 | 总延迟 mean(ms) | 总延迟 P95 | Throughput(chars/s) | TPOT(ms/char) | 样本数 |")
    lines.append("|------|--------------|----------|----------|----------------|-----------|---------------------|---------------|--------|")
    for cat, s in by_category.items():
        t = s.get("timing", {})
        ttft = t.get("ttft_ms", {})
        total = t.get("total_ms", {})
        tput = t.get("throughput_cps", {})
        tpot = t.get("tpot_ms_per_char", {})
        cnt = ttft.get("count") or total.get("count") or 0
        lines.append(
            f"| {cat} | {_fmt_ms(ttft.get('mean'))} | {_fmt_ms(ttft.get('p50'))} | {_fmt_ms(ttft.get('p95'))} | "
            f"{_fmt_ms(total.get('mean'))} | {_fmt_ms(total.get('p95'))} | "
            f"{_fmt_cps(tput.get('mean'))} | {_fmt_ms(tpot.get('mean'))} | {cnt} |"
        )
    lines.append("")

    # 每条 case 详情
    lines.append("## 四、每条 Case 详情\n")
    lines.append("| ID | 场景 | 查询(截断) | 期望工具 | 实际调用 | 工具选择 | 参数 | 答案 | TtFT(ms) | 总延迟(ms) | chars | 通过 |")
    lines.append("|----|------|-----------|----------|----------|----------|------|------|----------|-----------|-------|------|")
    for m in metrics_list:
        q = m["query"][:25].replace("|", "/")
        exp = ",".join(m["expected_tools"]) or "-"
        act = ",".join(m["called_tools"]) or "-"
        ts = "是" if m.get("tool_select_ok") else "否"
        ps = "是" if m.get("param_ok") else "否"
        ans = "-" if m.get("answer_ok") is None else ("是" if m["answer_ok"] else "否")
        passed = "是" if m.get("pass") else "否"
        tm = m.get("timing") or {}
        ttft_s = _fmt_ms(tm.get("ttft_ms"))
        total_s = _fmt_ms(tm.get("total_ms"))
        chars_s = str(tm.get("output_chars", 0))
        lines.append(f"| {m['id']} | {m['category']} | {q} | {exp} | {act} | {ts} | {ps} | {ans} | {ttft_s} | {total_s} | {chars_s} | {passed} |")
    lines.append("")

    # 多轮 case 详情
    mt_cases = [m for m in metrics_list if m["category"] == "multi_turn" and m.get("follow_ups")]
    if mt_cases:
        lines.append("## 五、多轮指代消解详情\n")
        for m in mt_cases:
            lines.append(f"### Case {m['id']}:{m['query'][:40]}\n")
            lines.append("| 轮次 | 追问 | 期望工具 | 实际调用 | 工具 | 答案 | TtFT(ms) | 总延迟(ms) | 判定 |")
            lines.append("|------|------|----------|----------|------|------|----------|-----------|------|")
            for fu in m["follow_ups"]:
                q = fu["query"][:30].replace("|", "/")
                exp = ",".join(fu["expected_tools"]) or "-"
                act = ",".join(fu["called_tools"]) or "-"
                ts = "是" if fu["tool_ok"] else "否"
                ans = "-" if fu["answer_ok"] is None else ("是" if fu["answer_ok"] else "否")
                ftm = fu.get("timing") or {}
                lines.append(
                    f"| R{fu['round']} | {q} | {exp} | {act} | {ts} | {ans} | "
                    f"{_fmt_ms(ftm.get('ttft_ms'))} | {_fmt_ms(ftm.get('total_ms'))} | {fu['answer_reason'][:30]} |"
                )
            lines.append("")

    # 失败 case 列表
    fails = [m for m in metrics_list if not m.get("pass")]
    lines.append("## 六、失败 Case 列表(便于人工归因)\n")
    if not fails:
        lines.append("无失败 case。\n")
    else:
        lines.append(f"共 {len(fails)} 条失败 case:\n")
        lines.append("| ID | 场景 | 查询 | 失败原因诊断 |")
        lines.append("|----|------|------|-------------|")
        for m in fails:
            reasons = []
            if not m.get("tool_select_ok"):
                reasons.append(f"工具选择错误(miss={m.get('tool_select_missing', [])})")
            if not m.get("param_ok"):
                reasons.append(f"参数填充错误({m.get('param_details', [])})")
            if m.get("answer_ok") is False:
                reasons.append(f"答案不准确({m.get('answer_reason', '')[:40]})")
            if m.get("order_ok") is False:
                reasons.append(f"工具顺序错误({m.get('order_details', [])})")
            if m.get("multi_turn_ok") is False:
                reasons.append("多轮指代消解失败")
            if m.get("error"):
                reasons.append(f"SSE 错误: {m['error'][:40]}")
            reason = "; ".join(reasons) if reasons else "综合未通过"
            q = m["query"][:35].replace("|", "/")
            lines.append(f"| {m['id']} | {m['category']} | {q} | {reason} |")
        lines.append("")

    # 待人工复核
    lines.append("## 七、待人工复核项\n")
    lines.append("- 需审批工具(file_write/sandbox_exec)的 case:若本地客户端未连接或审批超时,工具执行会失败,")
    lines.append("  答案准确率会受影响但工具选择准确率仍可验证,此类 case 标注「待人工复核」")
    lines.append("- load_skill 类 case:依赖 Knovis skill 是否注册,若 skill 未配置则 load_skill 可能失败")
    lines.append("- web_search/get_weather 类 case:依赖外部 API 可用性,失败时需区分工具故障与编排错误")
    lines.append("- LLM-as-Judge 结果受 deepseek 模型能力影响,边界 case 需人工复核\n")

    return "\n".join(lines)


# ==================== 控制台汇总 ====================

def print_console_summary(overall, by_category, use_judge):
    print("\n" + "=" * 70)
    print(f"  Agent 编排评测汇总  (Judge={'on' if use_judge else 'off'})")
    print("=" * 70)
    print(f"  Case 总数        : {overall['count']}")
    print(f"  通过率           : {_fmt(overall['pass_rate'])}")
    print(f"  工具选择准确率   : {_fmt(overall['tool_select_rate'])}")
    print(f"  参数填充正确率   : {_fmt(overall['param_rate'])}")
    print(f"  答案事实准确率   : {_fmt(overall['answer_rate'])} (判定 {overall['answer_judged']} 条)")
    if overall.get("order_rate") is not None:
        print(f"  多工具编排顺序   : {_fmt(overall['order_rate'])}")
    if overall.get("multi_turn_rate") is not None:
        print(f"  多轮指代消解     : {_fmt(overall['multi_turn_rate'])}")
    # 时间效率
    t = overall.get("timing", {})
    ttft = t.get("ttft_ms", {})
    total = t.get("total_ms", {})
    tput = t.get("throughput_cps", {})
    tpot = t.get("tpot_ms_per_char", {})
    if ttft.get("count"):
        print(f"  TtFT mean/P95    : {_fmt_ms(ttft.get('mean'))} / {_fmt_ms(ttft.get('p95'))} ms")
    if total.get("count"):
        print(f"  总延迟 mean/P95  : {_fmt_ms(total.get('mean'))} / {_fmt_ms(total.get('p95'))} ms")
    if tput.get("count"):
        print(f"  Throughput       : {_fmt_cps(tput.get('mean'))} chars/s")
    if tpot.get("count"):
        print(f"  TPOT mean        : {_fmt_ms(tpot.get('mean'))} ms/char")
    print("-" * 70)
    print(f"  {'场景':<14}{'Case数':>6}{'通过率':>8}{'工具选择':>10}{'参数':>8}{'答案':>8}")
    print("-" * 70)
    for cat, s in by_category.items():
        print(
            f"  {cat:<14}{s['count']:>6}{_fmt(s['pass_rate']):>8}"
            f"{_fmt(s['tool_select_rate']):>10}{_fmt(s['param_rate']):>8}{_fmt(s['answer_rate']):>8}"
        )
    print("=" * 70 + "\n")


# ==================== 主流程 ====================

def main():
    parser = argparse.ArgumentParser(description="Agent 编排评测")
    parser.add_argument("--category", default=None, help="过滤场景:single_tool/multi_tool/multi_turn")
    parser.add_argument("--timeout", type=int, default=SSE_TIMEOUT_DEFAULT, help="SSE 单轮超时(秒)")
    parser.add_argument("--no-judge", action="store_true", help="跳过 LLM-as-Judge(加速)")
    parser.add_argument("--out", default=str(REPORT_FILE), help="报告输出路径")
    args = parser.parse_args()

    token = os.getenv("AGENT_EVAL_TOKEN", "").strip()
    if not token:
        print("[FATAL] 未设置 AGENT_EVAL_TOKEN(JWT access token)", file=sys.stderr)
        sys.exit(1)
    llm_key = os.getenv("AGENT_EVAL_LLM_KEY", "").strip()
    judge_key = os.getenv("LLM_API_KEY", "").strip()
    use_judge = (not args.no_judge) and bool(judge_key)
    if not args.no_judge and not judge_key:
        print("[WARN] LLM_API_KEY 未配置,自动关闭 LLM-as-Judge")

    if not EVAL_FILE.exists():
        print(f"[FATAL] 评测集不存在: {EVAL_FILE}", file=sys.stderr)
        sys.exit(1)

    cases = load_cases(EVAL_FILE)
    if args.category:
        cases = [c for c in cases if c.get("category") == args.category]
    print(f"[INFO] 加载 {len(cases)} 条 case, Judge={'on' if use_judge else 'off'}, timeout={args.timeout}s")

    # 健康检查
    try:
        h = requests.get(f"{AGENT_GO_URL}/health", timeout=10)
        print(f"[INFO] agent-go 健康: HTTP {h.status_code}")
    except Exception as e:
        print(f"[WARN] agent-go 健康检查失败: {e}")

    metrics_list = []
    for idx, case in enumerate(cases, 1):
        case_id = case.get("id")
        category = case.get("category", "")
        query = case.get("query", "")
        print(f"[{idx}/{len(cases)}] id={case_id} cat={category} query={query[:35]}", end=" ")

        if category == "multi_turn":
            m = eval_multi_turn(case, token, llm_key, judge_key, use_judge, args.timeout)
        else:
            session_id = create_session(token, title=f"eval-{case_id}")
            result = chat_stream(token, query, session_id, llm_key, args.timeout)
            m = eval_single_turn(case, result, judge_key, use_judge)
            delete_session(token, session_id)

        metrics_list.append(m)
        status = "PASS" if m.get("pass") else "FAIL"
        ts = "OK" if m.get("tool_select_ok") else "MISS"
        print(f"-> {status} tool={ts} called={m.get('called_tools', [])}")

    if not metrics_list:
        print("[FATAL] 无有效评测结果", file=sys.stderr)
        sys.exit(1)

    overall, by_category = summarize(metrics_list)
    print_console_summary(overall, by_category, use_judge)

    report = generate_report(metrics_list, overall, by_category, use_judge)
    out_path = Path(args.out)
    out_path.write_text(report, encoding="utf-8")
    print(f"[INFO] 报告已生成: {out_path}")
    print(f"[INFO] 失败 case: {sum(1 for m in metrics_list if not m.get('pass'))}/{len(metrics_list)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())

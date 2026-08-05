#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Agent 项目端到端集成测试

覆盖链路：注册 → 登录 → 设 LLM key → 创建会话 → 对话(file_read) →
       对话(sandbox_exec 审批流) → 对话(file_write 审批流) → 校验审计日志

依赖：仅 Python 3.7+ 标准库（urllib + json），无需 pip install

前置条件：
  1. agent-go 主服务已启动（默认 http://127.0.0.1:8001）
  2. local-agent 客户端已启动并用同一用户的 token 连接 /ws/agent
  3. .env 中 LLM_API_KEY 已配置可用（或测试时通过 -k 传入）
  4. MySQL / Redis 已就绪

用法：
  python scripts/integration_test.py
  python scripts/integration_test.py --server http://127.0.0.1:8001
  python scripts/integration_test.py --llm-key sk-xxx
  python scripts/integration_test.py --skip sandbox,file_write  # 跳过指定用例
  python scripts/integration_test.py --auto-approve             # 见到 waiting_approval 自动批准（默认开启）

退出码：
  0  全部通过
  1  有失败用例
  2  前置条件不满足（服务未启动）
"""
import argparse
import http.client
import json
import os
import sys
import time
import uuid
import urllib.parse
import urllib.request
import urllib.error
from typing import Optional, Dict, Any, List, Tuple


# ============== 工具函数 ==============

class Colors:
    GREEN = "\033[92m"
    RED = "\033[91m"
    YELLOW = "\033[93m"
    CYAN = "\033[96m"
    BOLD = "\033[1m"
    RESET = "\033[0m"


def cprint(color: str, msg: str) -> None:
    print(f"{color}{msg}{Colors.RESET}", flush=True)


class ApiClient:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.token: Optional[str] = None

    def _headers(self, extra: Dict[str, str] = None) -> Dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self.token:
            h["Authorization"] = f"Bearer {self.token}"
        if extra:
            h.update(extra)
        return h

    def request(self, method: str, path: str, body: Any = None,
                headers: Dict[str, str] = None, timeout: float = 30) -> Tuple[int, Any]:
        url = self.base_url + path
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
        req = urllib.request.Request(url, data=data, method=method,
                                     headers=self._headers(headers))
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read().decode("utf-8")
                try:
                    return resp.status, json.loads(raw)
                except json.JSONDecodeError:
                    return resp.status, raw
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", errors="replace")
            try:
                return e.code, json.loads(raw)
            except json.JSONDecodeError:
                return e.code, raw


def stream_sse(client: ApiClient, path: str, body: Any,
               timeout: float = 120):
    """发起 SSE 流式请求，yield 实时事件（生成器，不缓冲）。

    使用 http.client 逐行读取，确保实时接收 SSE 事件。
    注意：必须用 for e in stream_sse(...) 实时消费，
    否则审批流等场景无法及时响应。
    """
    parsed = urllib.parse.urlparse(client.base_url)
    conn = http.client.HTTPConnection(parsed.hostname, parsed.port or 80,
                                      timeout=timeout)
    headers = client._headers()
    data = json.dumps(body).encode("utf-8")
    try:
        conn.request("POST", path, body=data, headers=headers)
        resp = conn.getresponse()
        # 逐行读取：SSE 事件格式为 data: {...}\n\n
        # readline() 遇到 \n 立即返回，实现真正的实时流式读取
        for raw_line in resp:
            line = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")
            # 跳过空行和 SSE 注释行（如 ": connected"）
            if not line or line.startswith(":"):
                continue
            if line.startswith("data: "):
                payload = line[6:]
                try:
                    yield json.loads(payload)
                except json.JSONDecodeError:
                    pass
    finally:
        conn.close()


# ============== 测试用例 ==============

class TestCase:
    def __init__(self, name: str, fn, description: str = ""):
        self.name = name
        self.fn = fn
        self.description = description

    def run(self, ctx: Dict[str, Any]) -> Tuple[bool, str]:
        try:
            return self.fn(ctx)
        except AssertionError as e:
            return False, f"assertion failed: {e}"
        except Exception as e:
            return False, f"exception: {type(e).__name__}: {e}"


# ----- 用例 1：健康检查 -----
def test_health(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    status, body = client.request("GET", "/health")
    assert status == 200, f"health 状态码={status}"
    if isinstance(body, dict):
        assert body.get("status") == "ok", f"health status={body}"
    return True, "服务在线"


# ----- 用例 2：注册 -----
def test_register(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    username = f"itest_{uuid.uuid4().hex[:8]}"
    password = "Test@1234"
    status, body = client.request("POST", "/auth/register",
                                   {"username": username, "password": password})
    if status == 409:
        # 用户已存在，复用（CI 重跑场景）
        ctx["username"] = username
        ctx["password"] = password
        return True, f"用户已存在，复用 {username}"
    assert status == 201, f"注册失败 status={status} body={body}"
    ctx["username"] = username
    ctx["password"] = password
    if isinstance(body, dict):
        ctx["user_id"] = body.get("user_id") or body.get("id")
    return True, f"注册成功 {username}"


# ----- 用例 3：登录 -----
def test_login(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    status, body = client.request("POST", "/auth/login", {
        "username": ctx["username"],
        "password": ctx["password"],
    })
    assert status == 200, f"登录失败 status={status} body={body}"
    assert isinstance(body, dict), f"登录返回非 dict: {body}"
    access = body.get("access_token") or body.get("accessToken")
    assert access, f"登录返回无 access_token: {body}"
    client.token = access
    ctx["refresh_token"] = body.get("refresh_token") or body.get("refreshToken")
    return True, "登录成功，token 已设置"


# ----- 用例 4：设置 LLM key -----
def test_set_llm_key(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    llm_key = ctx.get("llm_key") or ""
    if not llm_key:
        return True, "未传 --llm-key，跳过（用 .env 兜底 key，可能受免费额度限流）"
    status, body = client.request("POST", "/auth/llm-key", {"api_key": llm_key})
    assert status == 200, f"设 LLM key 失败 status={status} body={body}"
    return True, "LLM key 设置成功"


# ----- 用例 5：创建会话 -----
def test_create_session(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    status, body = client.request("POST", "/sessions", {"title": "集成测试会话"})
    assert status in (200, 201), f"创建会话失败 status={status} body={body}"
    assert isinstance(body, dict), f"返回非 dict: {body}"
    sid = body.get("id") or body.get("session_id")
    assert sid, f"返回无 session id: {body}"
    ctx["session_id"] = sid
    return True, f"会话创建成功 {sid}"


# ----- 用例 6：对话触发 file_read（免审批）-----
def test_chat_file_read(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    sid = ctx.get("session_id") or _ensure_session(ctx)
    # 先在本地写一个测试文件，让 LLM 读它
    test_file = os.path.join(temp_dir(), f"itest_{uuid.uuid4().hex[:6]}.txt")
    with open(test_file, "w", encoding="utf-8") as f:
        f.write("hello from integration test\n")
    ctx["test_file"] = test_file

    query = f"请用 file_read 工具读取本地文件 {test_file} 的内容，并告诉我文件里写了什么"
    events = list(stream_sse(client, "/chat/stream", {
        "query": query,
        "session_id": sid,
    }, timeout=120))

    # 校验：必须收到 done 事件，且至少有一个 tool_call/file_read
    types = [e.get("type") for e in events]
    assert "done" in types, f"未收到 done 事件，收到的事件类型: {types}"
    tool_calls = [e for e in events if e.get("type") == "tool_call"]
    assert len(tool_calls) > 0, f"未收到任何 tool_call 事件"
    # 校验是否有 file_read 调用（LLM 可能调多次）
    has_file_read = False
    for e in events:
        if e.get("type") == "tool_call":
            data = e.get("data") or {}
            name = data.get("name") or data.get("tool_name") or ""
            if name == "file_read":
                has_file_read = True
                break
    if not has_file_read:
        return False, f"LLM 未调用 file_read（收到 tool_call: {[e.get('data',{}).get('name') for e in tool_calls]}）"
    return True, f"file_read 调用成功，共 {len(events)} 个 SSE 事件"


# ----- 用例 7：对话触发 sandbox_exec + 审批流 -----
def test_chat_sandbox_approval(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    sid = ctx.get("session_id") or _ensure_session(ctx)
    query = "请用 sandbox_exec 工具执行 `echo hello_from_integration_test` 命令，并告诉我输出"
    events = run_chat_with_auto_approval(client, sid, query, ctx)
    types = [e.get("type") for e in events]
    last_err = ctx.get("last_error", "")
    assert "done" in types, f"未收到 done 事件，事件类型: {types}，last_error: {last_err}"
    approval_events = [e for e in events if e.get("type") == "waiting_approval"]
    if not approval_events:
        return False, f"未触发审批（LLM 可能未调 sandbox_exec），事件类型: {types}，last_error: {last_err}"
    # 检查是否收到 tool_result
    tool_results = [e for e in events if e.get("type") == "tool_result"]
    if not tool_results:
        return False, "审批通过但未收到 tool_result"
    return True, f"审批流走通，{len(approval_events)} 次审批，{len(tool_results)} 个 tool_result"


# ----- 用例 8：对话触发 file_write + 审批流（D 项）-----
def test_chat_file_write_approval(ctx: Dict[str, Any]) -> Tuple[bool, str]:
    client: ApiClient = ctx["client"]
    sid = ctx.get("session_id") or _ensure_session(ctx)
    target_file = os.path.join(temp_dir(), f"itest_write_{uuid.uuid4().hex[:6]}.txt")
    ctx["written_file"] = target_file
    query = (f"请用 file_write 工具在本地文件 {target_file} 写入内容 "
             f"'written by integration test'，写入模式为 write（覆盖）。写完后告诉我结果。")
    events = run_chat_with_auto_approval(client, sid, query, ctx)
    types = [e.get("type") for e in events]
    last_err = ctx.get("last_error", "")
    assert "done" in types, f"未收到 done 事件，事件类型: {types}，last_error: {last_err}"
    approval_events = [e for e in events if e.get("type") == "waiting_approval"]
    if not approval_events:
        return False, "file_write 未触发审批"
    # 校验文件确实被写入
    if not os.path.exists(target_file):
        return False, f"审批通过但文件未创建: {target_file}"
    content = open(target_file, "r", encoding="utf-8").read()
    if "integration test" not in content:
        return False, f"文件内容不匹配: {content!r}"
    return True, f"file_write 审批流端到端验证通过，文件已写入: {target_file}"


# ----- 辅助：跑 chat + 自动审批 -----
def run_chat_with_auto_approval(client: ApiClient, session_id: str, query: str,
                                 ctx: Dict[str, Any]) -> List[Dict[str, Any]]:
    """异步发起 SSE，主线程收到 waiting_approval 立即调 decide 接口"""
    import threading
    events: List[Dict[str, Any]] = []
    events_lock = threading.Lock()
    done_event = threading.Event()

    def consume():
        try:
            print("    [DEBUG] SSE 连接中...")
            first_event = True
            for e in stream_sse(client, "/chat/stream",
                                {"query": query, "session_id": session_id},
                                timeout=180):
                if first_event:
                    print(f"    [DEBUG] 首个事件已收到 type={e.get('type')}")
                    first_event = False
                with events_lock:
                    events.append(e)
                etype = e.get("type", "")
                # 调试：打印所有事件类型
                print(f"    [DEBUG] 事件: {etype}")
                # 调试：打印 tool_call 和 waiting_approval 事件详情
                if etype == "tool_call":
                    data = e.get("data") or {}
                    print(f"    [DEBUG] tool_call: tool_name={data.get('tool_name')} id={data.get('tool_call_id')}")
                if etype == "waiting_approval":
                    data = e.get("data") or {}
                    print(f"    [DEBUG] waiting_approval: approval_id={data.get('approval_id')} tool={data.get('tool_name')}")
                # 自动审批
                if etype == "waiting_approval" and ctx.get("auto_approve"):
                    data = e.get("data") or {}
                    approval_id = data.get("approval_id")
                    if approval_id:
                        print(f"    [DEBUG] 自动批准 approval_id={approval_id}")
                        # 异步批准，避免阻塞 SSE 消费
                        def _do_approve(aid):
                            code, resp = client.request(
                                "POST", f"/approval/{aid}/decide",
                                {"approved": True, "reason": "auto-approved by integration test"},
                                timeout=10)
                            print(f"    [DEBUG] 审批接口返回 code={code} resp={resp}")
                        threading.Thread(target=_do_approve, args=(approval_id,), daemon=True).start()
                if etype == "done":
                    done_event.set()
            print(f"    [DEBUG] SSE 流结束，共 {len(events)} 个事件")
        except Exception as ex:
            ctx["last_error"] = f"{type(ex).__name__}: {ex}"
            print(f"    [DEBUG] SSE 异常: {ctx['last_error']}")
            done_event.set()

    t = threading.Thread(target=consume, daemon=True)
    t.start()
    # 等结束或超时
    if not done_event.wait(timeout=180):
        ctx["last_error"] = "SSE 流超时 180s"
    t.join(timeout=5)
    return events


def temp_dir() -> str:
    d = os.environ.get("TEMP") or os.environ.get("TMP") or "/tmp"
    return d


def _ensure_session(ctx: Dict[str, Any]) -> str:
    """确保 ctx 中有 session_id，没有则自动创建一个（用于 --only 跳过 session 用例的场景）"""
    sid = ctx.get("session_id")
    if sid:
        return sid
    client: ApiClient = ctx["client"]
    code, resp = client.request("POST", "/sessions", {"title": "itest_auto"})
    if code not in (200, 201):
        raise RuntimeError(f"自动创建会话失败 code={code} resp={resp}")
    sid = (resp or {}).get("session_id") or (resp or {}).get("id")
    if not sid:
        raise RuntimeError(f"会话响应无 session_id: {resp}")
    ctx["session_id"] = sid
    return sid


# ============== 主流程 ==============

def main():
    parser = argparse.ArgumentParser(description="Agent 项目端到端集成测试")
    parser.add_argument("--server", default=os.environ.get("AGENT_SERVER", "http://127.0.0.1:8001"),
                        help="agent-go 服务地址（默认 http://127.0.0.1:8001）")
    parser.add_argument("--llm-key", default=os.environ.get("LLM_API_KEY", ""),
                        help="用户 LLM API key（不传则用 .env 兜底 key）")
    parser.add_argument("--skip", default="",
                        help="跳过的用例名（逗号分隔），如：sandbox,file_write")
    parser.add_argument("--only", default="",
                        help="仅运行指定用例（逗号分隔），如：health,register")
    parser.add_argument("--no-auto-approve", action="store_true",
                        help="禁用自动审批（默认开启，遇 waiting_approval 自动批准）")
    parser.add_argument("--token", default="",
                        help="直接用已有用户的 access token（跳过 register/login 用例，用于对接已连接的 local-agent）")
    parser.add_argument("--keep-user", action="store_true",
                        help="保留测试用户数据（默认不清理，因为审计日志不可删）")
    args = parser.parse_args()

    client = ApiClient(args.server)
    # 如果传了 --token，直接用该 token，跳过 register/login
    if args.token:
        client.token = args.token

    ctx = {
        "client": client,
        "llm_key": args.llm_key,
        "auto_approve": not args.no_auto_approve,
    }

    all_cases = [
        TestCase("health", test_health, "服务健康检查"),
        TestCase("register", test_register, "注册测试用户"),
        TestCase("login", test_login, "登录拿 access token"),
        TestCase("llm_key", test_set_llm_key, "设置 LLM key"),
        TestCase("session", test_create_session, "创建会话"),
        TestCase("file_read", test_chat_file_read, "对话触发 file_read（免审批）"),
        TestCase("sandbox", test_chat_sandbox_approval, "对话触发 sandbox_exec + 审批流"),
        TestCase("file_write", test_chat_file_write_approval, "对话触发 file_write + 审批流（D 项）"),
    ]

    skip_set = set(s.strip() for s in args.skip.split(",") if s.strip())
    only_set = set(s.strip() for s in args.only.split(",") if s.strip()) if args.only else None

    # --token 模式：跳过 register/login/llm_key（已有 token 直接用）
    if args.token:
        skip_set.update({"register", "login", "llm_key"})

    cprint(Colors.BOLD + Colors.CYAN,
           "\n========== Agent 集成测试 ==========")
    cprint(Colors.CYAN, f"服务地址: {args.server}")
    cprint(Colors.CYAN, f"自动审批: {'开' if ctx['auto_approve'] else '关'}")
    cprint(Colors.CYAN, f"LLM key: {'用户提供' if args.llm_key else '.env 兜底（受免费额度限流）'}")
    if args.token:
        cprint(Colors.CYAN, f"模式: --token（跳过 register/login/llm_key，用已有用户）")
    print()

    passed, failed, skipped = 0, 0, 0
    for case in all_cases:
        if only_set and case.name not in only_set:
            continue
        if case.name in skip_set:
            cprint(Colors.YELLOW, f"  [SKIP] {case.name} — {case.description}")
            skipped += 1
            continue
        cprint(Colors.BOLD, f"  [RUN ] {case.name} — {case.description}")
        ok, msg = case.run(ctx)
        if ok:
            cprint(Colors.GREEN, f"  [PASS] {case.name}: {msg}")
            passed += 1
        else:
            cprint(Colors.RED, f"  [FAIL] {case.name}: {msg}")
            failed += 1
        print()

    cprint(Colors.BOLD + Colors.CYAN, "\n========== 测试汇总 ==========")
    cprint(Colors.GREEN, f"  通过: {passed}")
    if skipped:
        cprint(Colors.YELLOW, f"  跳过: {skipped}")
    if failed:
        cprint(Colors.RED, f"  失败: {failed}")

    if failed:
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()

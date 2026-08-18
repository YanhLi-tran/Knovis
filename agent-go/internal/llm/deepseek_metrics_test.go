package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestChatStreamMetricsSuccess 成功路径集成测试：
// mock DeepSeek SSE 流（含 usage 缓存字段），验证 ChatStream 全链路指标采集——
// stream_options 注入、TTFT 采样、usage/缓存命中解析、finish_reason 记录。
func TestChatStreamMetricsSuccess(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		// 首个内容 chunk 前延迟 150ms，用于验证 TTFT 采样
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\n")
		fl.Flush()
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		// DeepSeek 风格 usage chunk（include_usage=true 时最后一个 chunk 携带，choices 为空）
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120,\"prompt_cache_hit_tokens\":80,\"prompt_cache_miss_tokens\":20}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	before := snapshotCount(t)

	p := NewDeepSeekProvider("test-key", srv.URL, "deepseek-chat", "/chat/completions")
	ch := p.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})

	var content string
	var usage *Usage
	finish := ""
	for chunk := range ch {
		content += chunk.DeltaContent
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
	}

	if content != "你好" || finish != "stop" {
		t.Fatalf("流式内容/finish 校验失败: content=%q finish=%q", content, finish)
	}
	if usage == nil || usage.PromptCacheHitTokens != 80 || usage.PromptCacheMissTokens != 20 {
		t.Fatalf("usage 缓存字段解析失败: %+v", usage)
	}
	// 请求体应注入 stream_options.include_usage=true
	if !strings.Contains(gotBody, "\"stream_options\":{\"include_usage\":true}") {
		t.Fatalf("请求体应包含 stream_options.include_usage=true, got: %s", clip(gotBody, 200))
	}

	// 验证指标已落全局缓冲（对比调用前的样本数）
	if snapshotCount(t) != before+1 {
		t.Fatalf("指标缓冲应新增 1 条: before=%d after=%d", before, snapshotCount(t))
	}
	m := latestMetric(t)
	if m.Error != "" {
		t.Fatalf("期望无错误, got error=%s", m.Error)
	}
	if m.TTFTMs < 100 {
		t.Fatalf("TTFT 期望 >=100ms（服务端延迟 150ms）, got %d", m.TTFTMs)
	}
	if m.TotalMs < m.TTFTMs {
		t.Fatalf("TotalMs(%d) 应 >= TTFTMs(%d)", m.TotalMs, m.TTFTMs)
	}
	if m.FinishReason != "stop" {
		t.Fatalf("FinishReason 期望 stop, got %s", m.FinishReason)
	}
	if m.PromptTokens != 100 || m.CompletionTokens != 20 {
		t.Fatalf("token 采集错误: prompt=%d completion=%d", m.PromptTokens, m.CompletionTokens)
	}
	if m.CacheHitRate < 0.79 || m.CacheHitRate > 0.81 {
		t.Fatalf("CacheHitRate 期望 0.8, got %f", m.CacheHitRate)
	}
	s, _ := DefaultMetrics().Snapshot(0)
	if s.CacheHitRate < 0.79 || s.CacheHitRate > 0.81 {
		t.Fatalf("summary 汇总 CacheHitRate 期望 ~0.8, got %f", s.CacheHitRate)
	}
}

// TestChatStreamMetricsHTTPError 错误路径：非 200 响应也应记录指标（TTFT=-1 + error）
func TestChatStreamMetricsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Authentication Fails"}}`)
	}))
	defer srv.Close()

	before := snapshotCount(t)

	p := NewDeepSeekProvider("bad-key", srv.URL, "deepseek-chat", "/chat/completions")
	ch := p.ChatStream(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	for range ch {
		// 消费至结束
	}

	if snapshotCount(t) != before+1 {
		t.Fatalf("错误路径指标缓冲应新增 1 条: before=%d after=%d", before, snapshotCount(t))
	}
	m := latestMetric(t)
	if m.Error != "http_401" {
		t.Fatalf("期望 error=http_401, got %q", m.Error)
	}
	if m.TTFTMs != -1 {
		t.Fatalf("错误路径 TTFT 期望 -1, got %d", m.TTFTMs)
	}
}

// snapshotCount 当前全局缓冲样本数（测试辅助）
func snapshotCount(t *testing.T) int {
	t.Helper()
	s, _ := DefaultMetrics().Snapshot(0)
	return s.SampleCount
}

// latestMetric 最新一条指标（测试辅助）
func latestMetric(t *testing.T) CallMetric {
	t.Helper()
	_, recent := DefaultMetrics().Snapshot(1)
	if len(recent) == 0 {
		t.Fatal("指标缓冲为空")
	}
	return recent[0]
}

// clip 截断长字符串用于错误输出
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

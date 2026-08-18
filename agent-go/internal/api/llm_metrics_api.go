package api

import (
	"net/http"

	"agent-go/internal/llm"
)

// llmMetrics LLM 调用指标（进程内环形缓冲，保留最近 200 次调用）
// 采集内容：DeepSeek KV 缓存命中率（prompt_cache_hit/miss_tokens）、TTFT（首 token 延迟）、
// token 用量、端到端耗时与错误。随每次 ChatStream 调用自动记录，重启清零。
func (s *Server) llmMetrics(c *GinCompat) {
	summary, recent := llm.DefaultMetrics().Snapshot(0)
	c.JSON(http.StatusOK, H{
		"summary": summary,
		"recent":  recent,
	})
}

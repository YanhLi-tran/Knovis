package llm

import (
	"math"
	"sort"
	"sync"
	"time"
)

// metricsCapacity 环形缓冲容量（进程内保留最近 N 次调用明细，重启清零）
const metricsCapacity = 200

// CallMetric 单次 LLM 调用的性能与用量指标
type CallMetric struct {
	Timestamp         time.Time `json:"timestamp"`
	TraceID           string    `json:"trace_id"`
	Model             string    `json:"model"`
	TTFTMs            int64     `json:"ttft_ms"`  // 首 token 延迟（请求发出→首个内容/tool_call delta）；未收到首 token（错误）为 -1
	TotalMs           int64     `json:"total_ms"` // 端到端耗时
	PromptTokens      int       `json:"prompt_tokens"`
	CompletionTokens  int       `json:"completion_tokens"`
	CacheHitTokens    int       `json:"prompt_cache_hit_tokens"`  // DeepSeek 上下文缓存命中 tokens
	CacheMissTokens   int       `json:"prompt_cache_miss_tokens"` // 未命中（需重算）的 prompt tokens
	CacheHitRate      float64   `json:"cache_hit_rate"`           // hit/(hit+miss)；端点不返回缓存字段时为 -1
	FinishReason      string    `json:"finish_reason"`
	Error             string    `json:"error,omitempty"`
}

// MetricsSummary 聚合指标（基于环形缓冲内样本计算）
type MetricsSummary struct {
	SampleCount       int     `json:"sample_count"`
	ErrorCount        int     `json:"error_count"`
	CacheHitRate      float64 `json:"cache_hit_rate"` // token 加权命中率：sum(hit)/sum(hit+miss)；无数据为 -1
	TTFTMeanMs        float64 `json:"ttft_mean_ms"`
	TTFTP50Ms         float64 `json:"ttft_p50_ms"`
	TTFTP95Ms         float64 `json:"ttft_p95_ms"`
	TTFTMaxMs         int64   `json:"ttft_max_ms"`
	TotalMeanMs       float64 `json:"total_mean_ms"`
	PromptTokensSum   int64   `json:"prompt_tokens_sum"`
	CompletionTokens  int64   `json:"completion_tokens_sum"`
	CacheHitTokensSum int64   `json:"cache_hit_tokens_sum"`
	CacheMissTokensSum int64  `json:"cache_miss_tokens_sum"`
}

// metricsBuffer 进程内环形缓冲指标记录器
// 并发安全：Record 来自每次 LLM 流式调用的 goroutine，Snapshot 来自 API handler。
type metricsBuffer struct {
	mu      sync.RWMutex
	entries []CallMetric
	head    int // 下一个写入位置
	size    int // 当前有效样本数
}

// NewMetricsBuffer 创建指定容量的指标缓冲
func NewMetricsBuffer(capacity int) *metricsBuffer {
	if capacity <= 0 {
		capacity = metricsCapacity
	}
	return &metricsBuffer{entries: make([]CallMetric, capacity)}
}

// defaultMetrics 包级单例（DeepSeekProvider 每次请求新建，指标落全局缓冲）
var defaultMetrics = NewMetricsBuffer(metricsCapacity)

// DefaultMetrics 全局指标缓冲（进程生命周期内累计最近 N 次）
func DefaultMetrics() *metricsBuffer { return defaultMetrics }

// Record 记录一次调用
func (b *metricsBuffer) Record(m CallMetric) {
	// 派生字段：单条命中率
	if m.CacheHitTokens+m.CacheMissTokens > 0 {
		m.CacheHitRate = float64(m.CacheHitTokens) / float64(m.CacheHitTokens+m.CacheMissTokens)
	} else {
		m.CacheHitRate = -1
	}

	b.mu.Lock()
	b.entries[b.head] = m
	b.head = (b.head + 1) % len(b.entries)
	if b.size < len(b.entries) {
		b.size++
	}
	b.mu.Unlock()
}

// Snapshot 返回聚合 summary 与最近样本（新→旧，最多 limit 条；limit<=0 返回全部）
func (b *metricsBuffer) Snapshot(limit int) (MetricsSummary, []CallMetric) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	total := make([]CallMetric, 0, b.size)
	// 从最新往回取：head-1 是最近一条
	for i := 0; i < b.size; i++ {
		idx := (b.head - 1 - i + len(b.entries)) % len(b.entries)
		total = append(total, b.entries[idx])
	}

	summary := b.summarize(total)
	if limit > 0 && len(total) > limit {
		total = total[:limit]
	}
	return summary, total
}

// summarize 基于样本集计算聚合指标（调用方需持有锁或使用副本）
func (b *metricsBuffer) summarize(samples []CallMetric) MetricsSummary {
	s := MetricsSummary{CacheHitRate: -1}
	if len(samples) == 0 {
		return s
	}

	ttfts := make([]int64, 0, len(samples))
	var ttftSum, totalSum int64
	for _, m := range samples {
		s.SampleCount++
		if m.Error != "" {
			s.ErrorCount++
		}
		if m.TTFTMs >= 0 {
			ttfts = append(ttfts, m.TTFTMs)
			ttftSum += m.TTFTMs
		}
		totalSum += m.TotalMs
		s.PromptTokensSum += int64(m.PromptTokens)
		s.CompletionTokens += int64(m.CompletionTokens)
		s.CacheHitTokensSum += int64(m.CacheHitTokens)
		s.CacheMissTokensSum += int64(m.CacheMissTokens)
	}

	if n := len(samples); n > 0 {
		s.TotalMeanMs = float64(totalSum) / float64(n)
	}
	if n := len(ttfts); n > 0 {
		s.TTFTMeanMs = float64(ttftSum) / float64(n)
		sort.Slice(ttfts, func(i, j int) bool { return ttfts[i] < ttfts[j] })
		s.TTFTP50Ms = percentileSorted(ttfts, 0.50)
		s.TTFTP95Ms = percentileSorted(ttfts, 0.95)
		s.TTFTMaxMs = ttfts[n-1]
	}
	if s.CacheHitTokensSum+s.CacheMissTokensSum > 0 {
		s.CacheHitRate = float64(s.CacheHitTokensSum) / float64(s.CacheHitTokensSum+s.CacheMissTokensSum)
	}
	return s
}

// percentileSorted 在已升序排序的切片上取分位数（nearest-rank 法）
func percentileSorted(sorted []int64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return float64(sorted[idx])
}

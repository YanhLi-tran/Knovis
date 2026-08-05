package llm

import (
	"math"
)

// TokenEstimator Token 估算器
// 用字数换算比例估算 token 数（不依赖 tokenizer 库）
// DeepSeek 换算比例：英文字符 ≈ 0.3 token，中文字符 ≈ 0.6 token
// 后续适配其他模型时，可为每个模型配置不同的换算比例
type TokenEstimator struct {
	// 中文字符 token 系数（DeepSeek: 0.6）
	cnTokenRatio float64
	// 英文/ASCII 字符 token 系数（DeepSeek: 0.3）
	enTokenRatio float64
}

// NewTokenEstimator 创建 Token 估算器（默认 DeepSeek 换算比例）
func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{
		cnTokenRatio: 0.6, // 中文字符 ≈ 0.6 token
		enTokenRatio: 0.3, // 英文字符 ≈ 0.3 token
	}
}

// Estimate 估算字符串的 token 数
// 按 rune 遍历，非 ASCII 字符（中文等）用 cnTokenRatio，ASCII 字符用 enTokenRatio
func (e *TokenEstimator) Estimate(s string) int {
	if s == "" {
		return 0
	}
	runes := []rune(s)
	tokens := 0.0
	for _, r := range runes {
		if r > 127 { // 非 ASCII（中文、日文等）
			tokens += e.cnTokenRatio
		} else {
			tokens += e.enTokenRatio
		}
	}
	return int(math.Ceil(tokens))
}

// EstimateMessages 估算消息列表的总 token 数
// 包含所有消息的 content + tool_calls + role 标记开销
func (e *TokenEstimator) EstimateMessages(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		// role 标记开销（每条消息约 4 token 的结构开销）
		total += 4
		// content
		total += e.Estimate(m.Content)
		// tool_calls
		for _, tc := range m.ToolCalls {
			total += e.Estimate(tc.Function.Name)
			total += e.Estimate(tc.Function.Arguments)
		}
		// tool_call_id
		if m.ToolCallID != "" {
			total += e.Estimate(m.ToolCallID)
		}
	}
	return total
}

// EstimatePercentage 计算当前 token 占模型上下文的百分比
// maxContextLength <= 0 时返回 0（未配置）
func (e *TokenEstimator) EstimatePercentage(currentTokens, maxContextLength int) float64 {
	if maxContextLength <= 0 {
		return 0
	}
	return float64(currentTokens) / float64(maxContextLength) * 100
}

// ShouldCompress 判断是否需要压缩
// 超过 80% 建议压缩，超过 90% 强制压缩
func (e *TokenEstimator) ShouldCompress(currentTokens, maxContextLength int) (suggest, force bool) {
	pct := e.EstimatePercentage(currentTokens, maxContextLength)
	return pct >= 80, pct >= 90
}

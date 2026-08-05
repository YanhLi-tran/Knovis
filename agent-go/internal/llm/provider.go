package llm

import (
	"context"
)

// Provider LLM 提供商抽象接口
type Provider interface {
	// ChatStream 流式对话，通过 channel 推送 StreamChunk
	// 最后一个 chunk 或 error 通过 channel 返回后关闭
	ChatStream(ctx context.Context, req ChatRequest) <-chan StreamChunk

	// APIKey 返回当前使用的 API key
	APIKey() string

	// Model 返回模型名
	Model() string
}

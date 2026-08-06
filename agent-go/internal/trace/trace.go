// Package trace 提供全链路 trace_id 的生成与 context 传递。
//
// trace_id 生命周期（全链路可追踪）：
//   - 入口：agent-go HTTP 中间件为每个请求生成/透传 trace_id（响应头 X-Trace-Id 回显）
//   - 传递：agent-go → doc-service / memory-service（HTTP 头 X-Trace-Id）、
//     agent-go → LLM（DeepSeek 请求头 X-Trace-Id）、agent-go → local-agent（WS 消息 trace_id）
//   - 载体：context 贯穿 OTACO 编排循环与全部工具执行
package trace

import (
	"context"

	"github.com/google/uuid"
)

// ctxKey 是 context 中 trace_id 的 key 类型（避免与其他包冲突）
type ctxKey struct{}

// WithTraceID 将 trace_id 注入 context
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, traceID)
}

// TraceIDFromContext 从 context 提取 trace_id，不存在则返回空串
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// NewID 生成短 trace_id（uuid 前 8 位，够全链路区分即可）
func NewID() string {
	return uuid.New().String()[:8]
}

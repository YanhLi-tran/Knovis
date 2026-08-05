package tools

import "agent-go/internal/llm"

// SummarizeHistoryDefinition 压缩历史上下文工具定义
// LLM 在上下文占比超 80% 时自主调用，Go 端执行压缩后返回状态
// 压缩范围：窗口外（最近10轮之前）的未压缩消息 + 旧摘要合并重压
// 压缩后旧消息标记 summarized=true，不再加载到上下文
func SummarizeHistoryDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "summarize_history",
			Description: "压缩历史对话上下文。当系统提示上下文占比超过 80% 时，建议调用此工具。压缩后窗口外的旧对话会被合并到摘要中，保留最近10轮完整对话。调用后无需等待，下一轮对话将自动加载新摘要。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "调用此工具的原因（如：上下文占比超过80%，需要释放空间）",
					},
				},
			},
		},
	}
}

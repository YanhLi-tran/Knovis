package tools

import "agent-go/internal/llm"

// SummarizeHistoryDefinition 压缩历史上下文工具定义
// LLM 在上下文占比超 80% 时自主调用，Go 端【同步】压缩全部未压缩历史并返回结果
// 压缩范围：全部未压缩历史消息 + 旧摘要合并重压
// 压缩后历史消息标记 summarized=true，不再加载到上下文（由摘要代表）
func SummarizeHistoryDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "summarize_history",
			Description: "压缩历史对话上下文。当系统提示上下文占比超过 80% 时，应调用此工具释放上下文空间。压缩后全部历史对话会被合并到摘要中（摘要 = 压缩(旧摘要 + 历史内容)），历史消息不再加载进上下文，当前对话内容不受影响。调用会同步执行（耗时约几秒），完成后返回已压缩的消息条数。",
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

package llm

// Role 消息角色
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 对话消息
// 注意：Content 不能用 omitempty——DeepSeek 严格要求每条消息必须有 content 字段
// （assistant 工具调用轮 content 可能为空字符串，omitempty 会整个省略该字段导致 400 missing field content）
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // role=tool 时关联的调用 id
}

// ToolCall LLM 发起的工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // 固定 "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 函数调用详情
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 字符串
}

// ToolDefinition 工具定义（传给 LLM 的 schema）
type ToolDefinition struct {
	Type     string       `json:"type"` // 固定 "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction 工具函数 schema
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolGroup LLM 声明的工具分组（组间串行，组内并行）
type ToolGroup struct {
	ToolCalls []ToolCall `json:"tool_calls"`
}

// ChatRequest LLM 聊天请求
type ChatRequest struct {
	Messages []Message       `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
}

// StreamChunk 流式响应的一个片段
type StreamChunk struct {
	DeltaContent string       // 增量文本
	ToolCalls    []ToolCall   // 工具调用（非流式时完整返回）
	FinishReason string       // stop / tool_calls / length
	Usage        *Usage       // token 用量（最后一个 chunk）
}

// Usage token 用量
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

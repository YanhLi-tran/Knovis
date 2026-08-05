package tools

import (
	"log"
	"sync"

	"agent-go/internal/llm"
)

// QuestionOption 问题选项
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionItem 单个问题（支持批量提问）
type QuestionItem struct {
	Question    string            `json:"question"`
	Header      string            `json:"header,omitempty"`
	Options     []QuestionOption  `json:"options,omitempty"`
	MultiSelect bool              `json:"multi_select,omitempty"`
}

// QuestionPayload 发送给前端的提问负载
type QuestionPayload struct {
	QuestionID string         `json:"question_id"`
	Questions  []QuestionItem `json:"questions"`
}

// Answer 用户回答
type Answer struct {
	QuestionID    string   `json:"question_id"`
	SelectedLabels []string `json:"selected_labels"`
	FreeText       string   `json:"free_text"`
	Reply          string   `json:"reply"` // 组合后的回复文本
}

// QuestionManager 管理等待用户回答的问题
type QuestionManager struct {
	mu        sync.Mutex
	pending   map[string]chan Answer
}

// NewQuestionManager 创建提问管理器
func NewQuestionManager() *QuestionManager {
	return &QuestionManager{pending: make(map[string]chan Answer)}
}

// Register 注册一个问题，返回接收回答的 channel
func (qm *QuestionManager) Register(questionID string) chan Answer {
	ch := make(chan Answer, 1)
	qm.mu.Lock()
	qm.pending[questionID] = ch
	qm.mu.Unlock()
	log.Printf("[INFO][tools] 注册问题: question_id=%s", questionID)
	return ch
}

// Submit 提交用户回答，返回是否成功
func (qm *QuestionManager) Submit(questionID string, answer Answer) bool {
	qm.mu.Lock()
	ch, ok := qm.pending[questionID]
	if ok {
		delete(qm.pending, questionID)
	}
	qm.mu.Unlock()
	if !ok {
		log.Printf("[WARN][tools] 问题不存在或已过期: question_id=%s", questionID)
		return false
	}
	ch <- answer
	return true
}

// AskUserDefinition 返回 ask_user 工具定义（传给 LLM）
func AskUserDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "ask_user",
			Description: "向用户提问以获取更多信息、确认权限或让用户选择路径。支持批量提问（一次问多个问题）。每个问题可包含选项（单选/多选）或自由输入。最后一个问题默认为'其他'，让用户自行补充内容。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"questions": map[string]any{
						"type": "array",
						"description": "问题列表，支持批量提问",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"question": map[string]any{
									"type": "string",
									"description": "问题文本",
								},
								"header": map[string]any{
									"type": "string",
									"description": "问题标题（可选）",
								},
								"options": map[string]any{
									"type": "array",
									"description": "选项列表（可选，无选项则自由输入）",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"label": map[string]any{"type": "string"},
											"description": map[string]any{"type": "string"},
										},
									},
								},
								"multi_select": map[string]any{
									"type": "boolean",
									"description": "是否允许多选（默认 false）",
								},
							},
							"required": []string{"question"},
						},
					},
				},
				"required": []string{"questions"},
			},
		},
	}
}

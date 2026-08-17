package tools

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"agent-go/internal/llm"
)

// ToolHandler 工具处理函数签名
type ToolHandler func(ctx context.Context, args map[string]any) (string, error)

// Tool 工具定义
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     ToolHandler
	Category    string // info / knovis / mcp / system / file / sandbox
	// NeedsApproval 是否需要用户审批（写操作/危险操作）
	// true 时 processToolCalls 在执行 Handler 前先走审批流（SSE waiting_approval），
	// 批准后才执行；审批流放执行层，不占 LLM context
	NeedsApproval bool
}

// Registry 工具注册表
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register 注册工具
func (r *Registry) Register(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
	log.Printf("[INFO][tools] 注册工具: name=%s category=%s", t.Name, t.Category)
}

// Get 获取工具
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 列出所有工具
// 按名称稳定排序：工具列表注入 system prompt、tools 数组参与每轮请求序列化，
// map 无序遍历会导致顺序随机变化，打穿 LLM 服务端 KV 前缀缓存（顺序参与 tokenize）
func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// ToDefinitions 转为 LLM 可用的 ToolDefinition 列表（按名称稳定排序，保证请求间序列化一致，前缀缓存友好）
func (r *Registry) ToDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Function.Name < defs[j].Function.Name })
	return defs
}

// ToolResult 工具执行结果
type ToolResult struct {
	ToolCallID string
	ToolName   string
	Content    string
	Error      error
}

// ExecuteParallel 并行执行同一轮的多个 tool_calls
// 同一轮返回的 tool_calls 无依赖关系，并行执行
// 不同轮的 tool_calls 由 OTACO 循环天然保证串行
func (r *Registry) ExecuteParallel(ctx context.Context, calls []llm.ToolCall) []ToolResult {
	log.Printf("[INFO][tools] 并行执行 %d 个 tool_call", len(calls))
	var wg sync.WaitGroup
	results := make([]ToolResult, len(calls))

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c llm.ToolCall) {
			defer wg.Done()
			results[idx] = r.executeOne(ctx, c)
		}(i, call)
	}

	wg.Wait()
	return results
}

// executeOne 执行单个工具调用
func (r *Registry) executeOne(ctx context.Context, call llm.ToolCall) ToolResult {
	result := ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
	}

	tool, ok := r.Get(call.Function.Name)
	if !ok {
		log.Printf("[WARN][tools] 未知工具: %s", call.Function.Name)
		result.Error = fmt.Errorf("未知工具: %s", call.Function.Name)
		return result
	}

	// 解析参数
	args := map[string]any{}
	if call.Function.Arguments != "" && call.Function.Arguments != "{}" {
		if err := jsonUnmarshal(call.Function.Arguments, &args); err != nil {
			log.Printf("[WARN][tools] 参数解析失败 tool=%s args_len=%d err=%v", call.Function.Name, len(call.Function.Arguments), err)
			result.Error = fmt.Errorf("参数解析失败: %w", err)
			return result
		}
	}

	// 执行
	start := time.Now()
	content, err := tool.Handler(ctx, args)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[ERROR][tools] 工具执行失败 tool=%s args_count=%d elapsed=%s err=%v", call.Function.Name, len(args), elapsed, err)
		result.Error = err
		return result
	}
	log.Printf("[INFO][tools] 工具执行完成 tool=%s args_count=%d elapsed=%s", call.Function.Name, len(args), elapsed)
	result.Content = content
	return result
}

package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"agent-go/internal/llm"
	"agent-go/internal/tools"
)

// loadedTool 已加载的工具（含定义 + Handler，Handler 已绑定 userID）
type loadedTool struct {
	def     llm.ToolDefinition
	handler tools.ToolHandler
}

// loadedSkill 会话级已加载 Skill（含工具集合）
type loadedSkill struct {
	name         string
	instructions string
	tools        map[string]*loadedTool // toolName → tool
}

// Manager Skill 管理器（会话级已加载集合 + 工具执行）
//
// 多用户隔离：sessions 按 sessionID 隔离，每个 session 独立维护已加载 Skill 集合。
// Skill 加载后常驻到对话结束（不卸载）：避免反复加载，且 LLM 一旦学会就能持续使用。
// 工具 Handler 按 userID 绑定（load_skill 时构建闭包），不同用户的 token 互不影响。
type Manager struct {
	registry *Registry
	mu       sync.Mutex
	sessions map[string]map[string]*loadedSkill // sessionID → skillName → loadedSkill
}

// NewManager 创建 Skill 管理器
func NewManager(reg *Registry) *Manager {
	return &Manager{
		registry: reg,
		sessions: make(map[string]map[string]*loadedSkill),
	}
}

// Load 加载 Skill（load_skill 工具调用）
// 按 sessionID 隔离已加载状态，按 userID 构建工具 Handler（绑定用户 token）
// 返回 Instructions 给 LLM（注入 ToolResult，LLM 下一轮看到后知道如何调用 skill 工具）
// 幂等：同一 session 重复加载同一 Skill 直接返回 Instructions
func (m *Manager) Load(ctx context.Context, sessionID, skillName, userID string) (string, error) {
	def, ok := m.registry.Get(skillName)
	if !ok {
		return "", fmt.Errorf("未知 Skill: %s", skillName)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sessionSkills, ok := m.sessions[sessionID]
	if !ok {
		sessionSkills = make(map[string]*loadedSkill)
		m.sessions[sessionID] = sessionSkills
	}

	// 幂等：已加载则直接返回
	if existing, ok := sessionSkills[skillName]; ok {
		log.Printf("[INFO][skill] Skill 已加载，直接返回 sessionID=%s skill=%s", sessionID, skillName)
		return existing.instructions, nil
	}

	// 构建工具（绑定 userID）
	ls := &loadedSkill{
		name:         skillName,
		instructions: def.Instructions,
		tools:        make(map[string]*loadedTool),
	}
	for _, builder := range def.ToolBuilders {
		toolDef, handler, err := builder(ctx, userID)
		if err != nil {
			return "", fmt.Errorf("构建 Skill %s 工具失败: %w", skillName, err)
		}
		ls.tools[toolDef.Function.Name] = &loadedTool{
			def:     toolDef,
			handler: handler,
		}
	}
	sessionSkills[skillName] = ls

	log.Printf("[INFO][skill] Skill 加载完成 sessionID=%s skill=%s tools=%d", sessionID, skillName, len(ls.tools))
	return def.Instructions, nil
}

// GetLoadedToolDefs 获取会话已加载的所有工具定义（注入下一轮 tools 列表）
// OTACO buildTools 调用：常驻 FC 工具 + load_skill + session 已加载 skill 工具
func (m *Manager) GetLoadedToolDefs(sessionID string) []llm.ToolDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionSkills, ok := m.sessions[sessionID]
	if !ok {
		return nil
	}
	var defs []llm.ToolDefinition
	for _, ls := range sessionSkills {
		for _, t := range ls.tools {
			defs = append(defs, t.def)
		}
	}
	return defs
}

// HasTool 检查会话是否有指定工具（processToolCalls 分流用）
func (m *Manager) HasTool(sessionID, toolName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionSkills, ok := m.sessions[sessionID]
	if !ok {
		return false
	}
	for _, ls := range sessionSkills {
		if _, ok := ls.tools[toolName]; ok {
			return true
		}
	}
	return false
}

// ExecuteTool 执行会话级 Skill 工具
// OTACO processToolCalls 分流：主 registry 找不到的工具走此方法
func (m *Manager) ExecuteTool(ctx context.Context, sessionID string, call llm.ToolCall) tools.ToolResult {
	result := tools.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
	}

	m.mu.Lock()
	sessionSkills, ok := m.sessions[sessionID]
	if !ok {
		result.Error = fmt.Errorf("会话未加载任何 Skill")
		return result
	}
	var found *loadedTool
	for _, ls := range sessionSkills {
		if t, ok := ls.tools[call.Function.Name]; ok {
			found = t
			break
		}
	}
	m.mu.Unlock()

	if found == nil {
		result.Error = fmt.Errorf("工具 %s 未加载", call.Function.Name)
		return result
	}

	// 解析参数
	args := map[string]any{}
	if call.Function.Arguments != "" && call.Function.Arguments != "{}" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			result.Error = fmt.Errorf("参数解析失败: %w", err)
			return result
		}
	}

	start := time.Now()
	content, err := found.handler(ctx, args)
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("[ERROR][skill] 工具执行失败 tool=%s elapsed=%s err=%v", call.Function.Name, elapsed, err)
		result.Error = err
		return result
	}
	log.Printf("[INFO][skill] 工具执行完成 tool=%s elapsed=%s", call.Function.Name, elapsed)
	result.Content = content
	return result
}

// LoadSkillDefinition load_skill 工具定义（常驻 FC，每个对话都有）
// 模型看到 system prompt 中 Skill 注册表后，决定要用时调用此工具拉取详细 schema
func LoadSkillDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "load_skill",
			Description: "按需加载 Skill 的详细工具 schema。当用户需要某类低频能力（如社交操作、第三方服务）时调用。加载后该 Skill 的工具在后续对话中持续可用（不卸载）。可用 Skill 见 system prompt 中的 Skill 注册表。仅在用户明确表达需求时调用，不要主动引导。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skill_name": map[string]any{
						"type":        "string",
						"description": "要加载的 Skill 名称（见 system prompt 中的 Skill 注册表）",
					},
				},
				"required": []string{"skill_name"},
			},
		},
	}
}

// HandleLoadSkill 处理 load_skill 工具调用
// OTACO processToolCalls 识别到 load_skill 后调用此方法
// 返回 Instructions 作为 ToolResult content，LLM 下一轮看到后知道如何调用 skill 工具
func (m *Manager) HandleLoadSkill(ctx context.Context, call llm.ToolCall, sessionID, userID string) tools.ToolResult {
	result := tools.ToolResult{
		ToolCallID: call.ID,
		ToolName:   "load_skill",
	}

	// 解析参数
	args := map[string]any{}
	if call.Function.Arguments != "" && call.Function.Arguments != "{}" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			result.Error = fmt.Errorf("参数解析失败: %w", err)
			return result
		}
	}

	skillName, _ := args["skill_name"].(string)
	if skillName == "" {
		result.Error = fmt.Errorf("缺少参数 skill_name")
		return result
	}

	instructions, err := m.Load(ctx, sessionID, skillName, userID)
	if err != nil {
		result.Error = err
		return result
	}

	// 返回 Instructions 给 LLM：包含工具列表 + 使用说明
	// LLM 下一轮看到后，工具定义已在 tools 列表中（GetLoadedToolDefs），可直接调用
	result.Content = fmt.Sprintf(`{"status":"loaded","skill":"%s","instructions":%s}`, skillName, mustJSON(instructions))
	return result
}

// mustJSON 序列化为 JSON 字符串（失败返回空字符串）
func mustJSON(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

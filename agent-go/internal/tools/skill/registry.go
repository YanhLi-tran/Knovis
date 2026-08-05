package skill

import (
	"context"
	"log"
	"sync"

	"agent-go/internal/llm"
	"agent-go/internal/tools"
)

// SkillMetadata Skill 元信息（注入 system prompt，每个约 25 tokens）
// LLM 据此判断是否需要 load_skill。常驻 context 只有元信息，详细 schema 由 load_skill 按需拉取。
type SkillMetadata struct {
	Name        string // 唯一标识：knovis
	Description string // 一句话描述（LLM 据此判断是否需要加载）
}

// SkillDefinition Skill 详细定义
// 启动时注册到全局 Registry；load_skill 调用时按 session 构建具体工具（绑定 userID/token）
type SkillDefinition struct {
	Metadata     SkillMetadata
	Instructions string        // 使用说明（load_skill 返回给 LLM，注入下一轮 context）
	ToolBuilders []ToolBuilder // 工具构建器（load_skill 时调用，返回绑定 userID 的工具定义 + Handler）
}

// ToolBuilder 工具构建器（load_skill 时按 session 调用）
// userID 用于绑定用户 token/凭证（多用户隔离：不同用户的 token 不同，Handler 必须按用户绑定）
// 返回 ToolDefinition（含 schema）+ ToolHandler（含 userID 闭包）
type ToolBuilder func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error)

// Registry Skill 全局注册表（元信息 + 工具构建器）
// 启动时注册所有 Skill 定义；load_skill 时从 Registry 取定义，按 session 构建工具
type Registry struct {
	mu     sync.RWMutex
	skills map[string]*SkillDefinition
}

// NewRegistry 创建 Skill 注册表
func NewRegistry() *Registry {
	return &Registry{skills: make(map[string]*SkillDefinition)}
}

// Register 注册 Skill 定义（启动时调用）
func (r *Registry) Register(def *SkillDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[def.Metadata.Name] = def
	log.Printf("[INFO][skill] 注册 Skill: name=%s tools=%d", def.Metadata.Name, len(def.ToolBuilders))
}

// List 元信息列表（注入 system prompt 的 Skill 注册表）
func (r *Registry) List() []SkillMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]SkillMetadata, 0, len(r.skills))
	for _, def := range r.skills {
		list = append(list, def.Metadata)
	}
	return list
}

// Get 获取 Skill 定义
func (r *Registry) Get(name string) (*SkillDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.skills[name]
	return def, ok
}

// BuildSkillRegistryBlock 构建 Skill 注册表文本（注入 system prompt）
// 格式：每个 Skill 一行，约 25 tokens。注入位置：工具列表之后、记忆块之前（KV Cache 友好）
func BuildSkillRegistryBlock(metaList []SkillMetadata) string {
	if len(metaList) == 0 {
		return ""
	}
	var sb []byte
	sb = append(sb, "\n## Skill 注册表（按需加载）\n"...)
	sb = append(sb, "以下能力可通过 load_skill 工具按需加载。仅在用户明确表达需求时调用 load_skill，不要主动引导。\n\n"...)
	for _, m := range metaList {
		sb = append(sb, "- "...)
		sb = append(sb, m.Name...)
		sb = append(sb, ": "...)
		sb = append(sb, m.Description...)
		sb = append(sb, '\n')
	}
	return string(sb)
}

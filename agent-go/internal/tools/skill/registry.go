package skill

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"agent-go/internal/llm"
	"agent-go/internal/tools"
)

// SkillMetadata Skill 元信息（注入 system prompt，每个约 25-30 tokens）
// LLM 据此判断是否需要 load_skill。常驻 context 只有元信息，详细 schema 由 load_skill 按需拉取。
// Trigger 是显式触发条件：比 Description 更结构化地描述"用户什么需求时应该加载本 Skill"，
// 帮助 LLM 精准判断，避免仅凭描述猜测导致的误加载/漏加载。
type SkillMetadata struct {
	Name        string // 唯一标识：knovis / kb_summary
	Description string // 一句话描述（LLM 据此判断是否需要加载）
	Trigger     string // 显式触发条件（用户表达何种需求时应加载本 Skill）
}

// SkillDefinition Skill 详细定义
// 启动时注册到全局 Registry；load_skill 调用时按 session 构建具体工具（绑定 userID/token）
type SkillDefinition struct {
	Metadata     SkillMetadata
	Instructions string        // 使用说明（load_skill 返回给 LLM，注入下一轮 context；文件型 skill 为 SKILL.md 正文）
	ToolBuilders []ToolBuilder // 工具构建器（load_skill 时调用，返回绑定 userID 的工具定义 + Handler；可选）
	Scripts      []SkillScript // 执行代码（可选，load_skill 时同步到用户本地 workspace/skills/<name>/ 供 sandbox_exec 执行）
	OwnerUserID  string        // 空 = 全局内置 skill；非空 = 用户私有 skill（仅该用户可见/可加载）
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

// List 元信息列表（注入 system prompt 的 Skill 注册表；全局内置 + 用户私有）
// userID 非空时额外包含该用户的私有 skill（多租户隔离：A 用户看不到 B 用户的 skill）
// 按 Name 稳定排序：注册表文本注入 system prompt 稳定区，map 无序遍历会导致
// 顺序随机变化打穿 KV 前缀缓存（与 tools/registry.go List/ToDefinitions 同理）
func (r *Registry) List(userID string) []SkillMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]SkillMetadata, 0, len(r.skills))
	for _, def := range r.skills {
		if def.OwnerUserID != "" && userID != "" && def.OwnerUserID != userID {
			continue // 用户私有 skill 仅 owner 可见
		}
		if def.OwnerUserID != "" && userID == "" {
			continue // 匿名/无身份时不下发用户私有 skill
		}
		list = append(list, def.Metadata)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// Get 获取 Skill 定义
func (r *Registry) Get(name string) (*SkillDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.skills[name]
	return def, ok
}

// LoadFromDir 从目录扫描加载文件型 Skill（全局内置）
// 目录结构：<root>/<skill-name>/SKILL.md + scripts/（见 md.go）
// 目录名与 frontmatter name 不一致时返回错误（保证引用路径一致）
func (r *Registry) LoadFromDir(root string, maxScriptBytes int64) error {
	defs, err := LoadSkillsDir(root, maxScriptBytes)
	if err != nil {
		return err
	}
	for _, def := range defs {
		r.Register(def)
	}
	return nil
}

// AttachToolBuilders 为已注册的文件型 Skill 附加内置 Go 工具（混合模式）
// 适用场景：SKILL.md 描述流程，但执行需要 Go 层能力（如 token 解密调外部 API）。
// 例如 knovis：SKILL.md 引导 + knovis_get_feed 等 Go 工具执行。
// Skill 不存在时返回错误（防止拼写错误静默失效）。
func (r *Registry) AttachToolBuilders(name string, builders []ToolBuilder) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def, ok := r.skills[name]
	if !ok {
		return fmt.Errorf("AttachToolBuilders: Skill %s 未注册", name)
	}
	def.ToolBuilders = append(def.ToolBuilders, builders...)
	return nil
}

// Unregister 移除 Skill（用户删除自己上传的 skill 时调用）
// ownerUserID 非空时仅允许移除该用户自己的 skill（防越权删全局/他人）
func (r *Registry) Unregister(name, ownerUserID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def, ok := r.skills[name]
	if !ok {
		return nil // 不存在视为已删除，幂等
	}
	if ownerUserID != "" && def.OwnerUserID != ownerUserID {
		return fmt.Errorf("无权删除 Skill %s（归属 %s）", name, def.OwnerUserID)
	}
	delete(r.skills, name)
	log.Printf("[INFO][skill] 移除 Skill: name=%s", name)
	return nil
}

// OwnerOf 查询 Skill 归属（api 层删除校验用）：空=全局内置，非空=用户 userID
func (r *Registry) OwnerOf(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.skills[name]
	if !ok {
		return ""
	}
	return def.OwnerUserID
}

// BuildSkillRegistryBlock 构建 Skill 注册表文本（注入 system prompt）
// 格式：每个 Skill 一行（名称 + 描述 + 触发条件），约 25-30 tokens。
// 注入位置：工具列表之后、记忆块之前（KV Cache 友好）
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
		if m.Trigger != "" {
			sb = append(sb, "（触发: "...)
			sb = append(sb, m.Trigger...)
			sb = append(sb, "）"...)
		}
		sb = append(sb, '\n')
	}
	return string(sb)
}

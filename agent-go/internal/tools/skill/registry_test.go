package skill

import (
	"strings"
	"testing"
)

// TestBuildSkillRegistryBlockWithTrigger 注册表块应包含 Trigger（触发条件）
func TestBuildSkillRegistryBlockWithTrigger(t *testing.T) {
	block := BuildSkillRegistryBlock([]SkillMetadata{
		{
			Name:        "kb_summary",
			Description: "总结企业知识库内容",
			Trigger:     "用户要求总结企业知识库时",
		},
		{
			Name:        "knovis",
			Description: "Knovis 用户与动态查询",
			Trigger:     "用户询问 Knovis 数据时",
		},
	})

	if !strings.Contains(block, "kb_summary") || !strings.Contains(block, "knovis") {
		t.Fatal("注册表块应包含所有 skill 名称")
	}
	if !strings.Contains(block, "用户要求总结企业知识库时") {
		t.Fatalf("注册表块应包含 kb_summary 的 trigger，实际:\n%s", block)
	}
	if !strings.Contains(block, "用户询问 Knovis 数据时") {
		t.Fatalf("注册表块应包含 knovis 的 trigger，实际:\n%s", block)
	}
}

// TestBuildSkillRegistryBlockNoTrigger Trigger 为空时不应输出"（触发: ）"占位
func TestBuildSkillRegistryBlockNoTrigger(t *testing.T) {
	block := BuildSkillRegistryBlock([]SkillMetadata{
		{Name: "no-trigger", Description: "无触发条件"},
	})
	if strings.Contains(block, "触发:") {
		t.Fatalf("Trigger 为空时不应输出触发字段，实际:\n%s", block)
	}
}

// TestBuildSkillRegistryBlockEmpty 无 skill 时返回空串（不注入占位块）
func TestBuildSkillRegistryBlockEmpty(t *testing.T) {
	if got := BuildSkillRegistryBlock(nil); got != "" {
		t.Fatalf("空列表应返回空串，实际 %q", got)
	}
}

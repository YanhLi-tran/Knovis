package skills

import (
	"context"
	"strings"
	"testing"

	"agent-go/internal/rag"
)

// TestKBSummaryToolDefs kb_summary Skill 应注册 kb_list_docs 工具且定义符合预期
// 设计（路线 2）：检索复用常驻 rag_search，skill 内只提供确定范围的 kb_list_docs
func TestKBSummaryToolDefs(t *testing.T) {
	def := NewKBSummarySkillDefinition(rag.NewDocClient(""))
	if def.Metadata.Name != "kb_summary" {
		t.Fatalf("Skill 名称应为 kb_summary，实际 %s", def.Metadata.Name)
	}
	if def.Metadata.Trigger == "" {
		t.Fatal("kb_summary 应配置 Trigger 触发条件")
	}
	if len(def.ToolBuilders) != 1 {
		t.Fatalf("路线 2 应有 1 个工具（kb_list_docs），实际 %d", len(def.ToolBuilders))
	}

	// 构建工具定义（无需 user token，直接绑定 docClient）
	toolDef, handler, err := def.ToolBuilders[0](context.Background(), "any-user")
	if err != nil {
		t.Fatalf("构建工具失败: %v", err)
	}
	if toolDef.Function.Name != "kb_list_docs" {
		t.Fatalf("工具名应为 kb_list_docs，实际 %s", toolDef.Function.Name)
	}
	if handler == nil {
		t.Fatal("handler 不应为 nil")
	}
}

// TestKBSummaryInstructions 总结 Instructions 应包含表格输出与不臆想约束
func TestKBSummaryInstructions(t *testing.T) {
	def := NewKBSummarySkillDefinition(rag.NewDocClient(""))
	for _, want := range []string{"表格", "rag_search", "doc_ids", "禁止臆想", "兜底"} {
		if !strings.Contains(def.Instructions, want) {
			t.Fatalf("Instructions 应包含 %q，实际:\n%s", want, def.Instructions)
		}
	}
}

// TestKBListDocsHandler docClient 不可用时 handler 应返回友好错误而非 panic
func TestKBListDocsHandler(t *testing.T) {
	def := NewKBSummarySkillDefinition(rag.NewDocClient("http://127.0.0.1:1")) // 不可达端口
	_, handler, err := def.ToolBuilders[0](context.Background(), "user-1")
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	content, err := handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handler 不应返回 error（错误应转为文本给 LLM），实际: %v", err)
	}
	if !strings.Contains(content, "获取失败") {
		t.Fatalf("docClient 不可用时应返回友好错误文本，实际: %s", content)
	}
}

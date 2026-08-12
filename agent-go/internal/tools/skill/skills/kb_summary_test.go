package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-go/internal/rag"
	"agent-go/internal/tools/skill"
)

// TestKBListDocsToolDef kb_list_docs 工具定义符合预期（内置 Go 工具，附加到 SKILL.md 定义的 kb_summary）
func TestKBListDocsToolDef(t *testing.T) {
	builder := BuildKBListDocs(rag.NewDocClient("", ""))
	toolDef, handler, err := builder(context.Background(), "any-user")
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

// TestKBListDocsHandler docClient 不可用时 handler 应返回友好错误而非 panic
func TestKBListDocsHandler(t *testing.T) {
	builder := BuildKBListDocs(rag.NewDocClient("http://127.0.0.1:1", "")) // 不可达端口
	_, handler, err := builder(context.Background(), "user-1")
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

// TestKBSummarySKILLMD 内置 SKILL.md（skills/kb_summary/）应能被解析且正文含表格输出约束
func TestKBSummarySKILLMD(t *testing.T) {
	mdPath := filepath.Join("..", "..", "..", "..", "skills", "kb_summary", "SKILL.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Skipf("内置 SKILL.md 不存在（%v），跳过", err)
	}
	name, desc, trigger, instructions, err := skill.ParseSKILLMD(string(raw))
	if err != nil {
		t.Fatalf("解析 SKILL.md 失败: %v", err)
	}
	if name != KBSummarySkillName {
		t.Fatalf("name 应为 %s，实际 %s", KBSummarySkillName, name)
	}
	if desc == "" || trigger == "" {
		t.Fatal("description/trigger 不应为空")
	}
	for _, want := range []string{"表格", "rag_search", "doc_ids", "禁止臆想", "兜底", "对比", "file_write", "输出文档", "workspace", "相对路径", "会话标题"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("正文应包含 %q，实际:\n%s", want, instructions)
		}
	}
}

// TestKnovisSKILLMD 内置 SKILL.md（skills/knovis/）应能被解析且正文提到内置工具
func TestKnovisSKILLMD(t *testing.T) {
	mdPath := filepath.Join("..", "..", "..", "..", "skills", "knovis", "SKILL.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		t.Skipf("内置 SKILL.md 不存在（%v），跳过", err)
	}
	name, _, _, instructions, err := skill.ParseSKILLMD(string(raw))
	if err != nil {
		t.Fatalf("解析 SKILL.md 失败: %v", err)
	}
	if name != KnovisSkillName {
		t.Fatalf("name 应为 %s，实际 %s", KnovisSkillName, name)
	}
	for _, want := range []string{"knovis_get_feed", "knovis_get_profile", "knovis_get_post"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("正文应包含 %q，实际:\n%s", want, instructions)
		}
	}
}

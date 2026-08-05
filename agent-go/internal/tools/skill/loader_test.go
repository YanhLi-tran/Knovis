package skill

import (
	"context"
	"errors"
	"sync"
	"testing"

	"agent-go/internal/llm"
	"agent-go/internal/tools"
)

// mockToolBuilder 构造一个总是成功的 ToolBuilder（返回固定工具定义 + handler）
func mockToolBuilder(toolName string) ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		return llm.ToolDefinition{
				Type: "function",
				Function: llm.ToolFunction{Name: toolName},
			}, func(ctx context.Context, args map[string]any) (string, error) {
				return "mock-result-" + userID, nil
			}, nil
	}
}

// failToolBuilder 构造一个总是失败的 ToolBuilder（用于测试构建失败场景）
func failToolBuilder() ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		return llm.ToolDefinition{}, nil, errors.New("mock build failure")
	}
}

// TestLoadUnknownSkill 加载未注册的 Skill 应返回错误
func TestLoadUnknownSkill(t *testing.T) {
	reg := NewRegistry()
	mgr := NewManager(reg)

	_, err := mgr.Load(context.Background(), "session-1", "nonexistent", "user-1")
	if err == nil {
		t.Fatal("加载未注册 Skill 应返回错误，实际返回 nil")
	}
}

// TestLoadIdempotent 同一 session 重复加载同一 Skill 应幂等（不报错，返回相同 instructions）
func TestLoadIdempotent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:     SkillMetadata{Name: "test-skill", Description: "测试"},
		Instructions: "test-instructions",
		ToolBuilders: []ToolBuilder{mockToolBuilder("test_tool")},
	})
	mgr := NewManager(reg)

	// 第一次加载
	inst1, err := mgr.Load(context.Background(), "session-1", "test-skill", "user-1")
	if err != nil {
		t.Fatalf("第一次加载失败: %v", err)
	}
	// 第二次加载（幂等）
	inst2, err := mgr.Load(context.Background(), "session-1", "test-skill", "user-1")
	if err != nil {
		t.Fatalf("第二次加载失败: %v", err)
	}
	if inst1 != inst2 {
		t.Fatal("幂等加载应返回相同 instructions")
	}

	// 验证工具定义只注入一次（GetLoadedToolDefs 不重复）
	defs := mgr.GetLoadedToolDefs("session-1")
	if len(defs) != 1 {
		t.Fatalf("幂等加载后工具数应为 1，实际 %d", len(defs))
	}
}

// TestLoadConcurrent 并发加载同一 Skill（幂等性 + 并发安全）
func TestLoadConcurrent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:     SkillMetadata{Name: "concurrent-skill", Description: "并发测试"},
		Instructions: "concurrent-instructions",
		ToolBuilders: []ToolBuilder{mockToolBuilder("concurrent_tool")},
	})
	mgr := NewManager(reg)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = mgr.Load(context.Background(), "session-concurrent", "concurrent-skill", "user-1")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("并发加载[%d]失败: %v", i, err)
		}
	}

	// 并发加载后只应有一个工具定义
	defs := mgr.GetLoadedToolDefs("session-concurrent")
	if len(defs) != 1 {
		t.Fatalf("并发加载后工具数应为 1，实际 %d", len(defs))
	}
}

// TestLoadBuildFailure ToolBuilder 返回错误时应 propagate
func TestLoadBuildFailure(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:     SkillMetadata{Name: "fail-skill", Description: "构建失败测试"},
		Instructions: "fail-instructions",
		ToolBuilders: []ToolBuilder{failToolBuilder()},
	})
	mgr := NewManager(reg)

	_, err := mgr.Load(context.Background(), "session-1", "fail-skill", "user-1")
	if err == nil {
		t.Fatal("ToolBuilder 失败时应返回错误，实际返回 nil")
	}
}

// TestSessionIsolation 不同 session 的 Skill 加载互相隔离
func TestSessionIsolation(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:     SkillMetadata{Name: "iso-skill", Description: "隔离测试"},
		Instructions: "iso-instructions",
		ToolBuilders: []ToolBuilder{mockToolBuilder("iso_tool")},
	})
	mgr := NewManager(reg)

	// session-A 加载
	_, err := mgr.Load(context.Background(), "session-A", "iso-skill", "user-A")
	if err != nil {
		t.Fatalf("session-A 加载失败: %v", err)
	}

	// session-B 未加载，工具定义应为空
	defsB := mgr.GetLoadedToolDefs("session-B")
	if len(defsB) != 0 {
		t.Fatalf("session-B 未加载 Skill，工具数应为 0，实际 %d", len(defsB))
	}

	// session-A 有工具定义
	defsA := mgr.GetLoadedToolDefs("session-A")
	if len(defsA) != 1 {
		t.Fatalf("session-A 工具数应为 1，实际 %d", len(defsA))
	}

	// HasTool 隔离验证
	if !mgr.HasTool("session-A", "iso_tool") {
		t.Fatal("session-A 应有 iso_tool")
	}
	if mgr.HasTool("session-B", "iso_tool") {
		t.Fatal("session-B 不应有 iso_tool")
	}
}

// TestExecuteTool 执行 Skill 工具（handler 绑定 userID）
func TestExecuteTool(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:     SkillMetadata{Name: "exec-skill", Description: "执行测试"},
		Instructions: "exec-instructions",
		ToolBuilders: []ToolBuilder{mockToolBuilder("exec_tool")},
	})
	mgr := NewManager(reg)

	_, err := mgr.Load(context.Background(), "session-exec", "exec-skill", "user-123")
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	// 执行工具
	call := llm.ToolCall{
		ID: "call-1",
		Function: llm.FunctionCall{
			Name:      "exec_tool",
			Arguments: "{}",
		},
	}
	result := mgr.ExecuteTool(context.Background(), "session-exec", call)
	if result.Error != nil {
		t.Fatalf("工具执行失败: %v", result.Error)
	}
	// handler 返回 "mock-result-" + userID，验证 userID 绑定正确
	if result.Content != "mock-result-user-123" {
		t.Fatalf("工具执行结果应为 mock-result-user-123，实际 %s", result.Content)
	}
}

// TestExecuteToolNotLoaded 执行未加载的工具应返回错误
func TestExecuteToolNotLoaded(t *testing.T) {
	reg := NewRegistry()
	mgr := NewManager(reg)

	call := llm.ToolCall{
		ID: "call-1",
		Function: llm.FunctionCall{Name: "not_loaded_tool"},
	}
	result := mgr.ExecuteTool(context.Background(), "session-1", call)
	if result.Error == nil {
		t.Fatal("执行未加载工具应返回错误")
	}
}

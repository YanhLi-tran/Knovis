package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseSKILLMD 解析完整 SKILL.md（frontmatter + 正文）
func TestParseSKILLMD(t *testing.T) {
	content := `---
name: demo-skill
description: 演示 skill
trigger: 用户要求演示时
---

# 工作流程

1. 第一步
2. 第二步
`
	name, desc, trigger, instructions, err := ParseSKILLMD(content)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if name != "demo-skill" || desc != "演示 skill" || trigger != "用户要求演示时" {
		t.Fatalf("字段解析错误: name=%s desc=%s trigger=%s", name, desc, trigger)
	}
	if instructions != "# 工作流程\n\n1. 第一步\n2. 第二步" {
		t.Fatalf("正文解析错误: %q", instructions)
	}
}

// TestParseSKILLMDMissingFrontmatter 缺少 frontmatter 应报错
func TestParseSKILLMDMissingFrontmatter(t *testing.T) {
	if _, _, _, _, err := ParseSKILLMD("# 没有 frontmatter"); err == nil {
		t.Fatal("缺少 frontmatter 应返回错误")
	}
}

// TestParseSKILLMDMissingName 缺少 name 字段应报错
func TestParseSKILLMDMissingName(t *testing.T) {
	content := "---\ndescription: 无 name\n---\n正文"
	if _, _, _, _, err := ParseSKILLMD(content); err == nil {
		t.Fatal("缺少 name 应返回错误")
	}
}

// TestLoadSkillDir 加载带 scripts 的 skill 目录（含 frontmatter 解析 + scripts 递归读取）
func TestLoadSkillDir(t *testing.T) {
	dir := t.TempDir()
	// 目录名必须等于 name
	skillDir := filepath.Join(dir, "demo-skill")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo-skill\ndescription: 演示\ntrigger: 触发\n---\n\n# 正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "gen.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}

	def, err := LoadSkillDir(skillDir, 0)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if def.Metadata.Name != "demo-skill" {
		t.Fatalf("name 错误: %s", def.Metadata.Name)
	}
	if len(def.Scripts) != 1 || def.Scripts[0].Filename != "scripts/gen.py" {
		t.Fatalf("scripts 加载错误: %+v", def.Scripts)
	}
}

// TestLoadSkillDirNameMismatch 目录名与 frontmatter name 不一致应报错
func TestLoadSkillDirNameMismatch(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "wrong-name")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: right-name\ndescription: x\n---\n正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSkillDir(skillDir, 0); err == nil {
		t.Fatal("目录名与 name 不一致应报错")
	}
}

// TestRegistryListUserFilter 用户私有 skill 仅 owner 可见
func TestRegistryListUserFilter(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:    SkillMetadata{Name: "global-skill", Description: "全局"},
		OwnerUserID: "",
	})
	reg.Register(&SkillDefinition{
		Metadata:    SkillMetadata{Name: "alice-skill", Description: "Alice 私有"},
		OwnerUserID: "alice",
	})
	reg.Register(&SkillDefinition{
		Metadata:    SkillMetadata{Name: "bob-skill", Description: "Bob 私有"},
		OwnerUserID: "bob",
	})

	// 匿名：只见全局
	anon := reg.List("")
	if len(anon) != 1 || anon[0].Name != "global-skill" {
		t.Fatalf("匿名应只见全局 skill，实际 %+v", anon)
	}
	// alice：全局 + 自己的
	alice := reg.List("alice")
	if len(alice) != 2 {
		t.Fatalf("alice 应见 2 个 skill，实际 %d", len(alice))
	}
	for _, m := range alice {
		if m.Name == "bob-skill" {
			t.Fatal("alice 不应看到 bob-skill")
		}
	}
}

// TestRegistryUnregisterOwner 删除 skill 的归属校验
func TestRegistryUnregisterOwner(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&SkillDefinition{
		Metadata:    SkillMetadata{Name: "alice-skill", Description: "x"},
		OwnerUserID: "alice",
	})
	// 他人无权删除
	if err := reg.Unregister("alice-skill", "bob"); err == nil {
		t.Fatal("bob 删除 alice 的 skill 应被拒绝")
	}
	// owner 可删除
	if err := reg.Unregister("alice-skill", "alice"); err != nil {
		t.Fatalf("owner 删除失败: %v", err)
	}
	if _, ok := reg.Get("alice-skill"); ok {
		t.Fatal("删除后不应再存在")
	}
	// 全局 skill 无 owner 可删（OwnerUserID 为空，传入任意 owner 校验）
	reg.Register(&SkillDefinition{Metadata: SkillMetadata{Name: "g", Description: "x"}})
	if err := reg.Unregister("g", "anyone"); err == nil {
		t.Fatal("全局内置 skill 不应被用户删除")
	}
}

package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ===== SKILL.md 文件解析（文件驱动的 Skill 标准格式）=====
//
// 每个 Skill 一个目录，目录名 = name 字段：
//
//	skills/<name>/
//	├── SKILL.md        # frontmatter(name/description/trigger) + 正文流程
//	└── scripts/        # 执行代码（可选，load_skill 时同步到用户本地 workspace 执行）
//
// SKILL.md 格式：
//
//	---
//	name: kb_summary
//	description: 总结企业知识库内容（...）
//	trigger: 用户要求总结/概括企业知识库、公司文档、指定文档的内容时
//	---
//
//	# 正文（工作流程，load_skill 时作为 Instructions 注入 LLM）

// SkillScript skill 执行代码文件（scripts/ 下，按相对路径存储）
// load_skill 时由服务端通过 WS 同步到用户本地 workspace/skills/<name>/<filename>
type SkillScript struct {
	Filename string // 相对 skill 目录的路径，如 scripts/gen.py
	Content  string
}

// frontmatter 正则：---\n<内容>\n---
var frontmatterRe = regexp.MustCompile(`(?s)^\s*---\s*\n(.*?)\n\s*---\s*\n`)

// ParseSKILLMD 解析 SKILL.md 内容为 frontmatter 字段 + 正文（Instructions）
// 正文 = 去掉 frontmatter 后的 markdown 主体
func ParseSKILLMD(content string) (name, description, trigger, instructions string, err error) {
	m := frontmatterRe.FindStringSubmatch(content)
	if m == nil {
		return "", "", "", "", fmt.Errorf("SKILL.md 缺少 frontmatter（--- 包裹的 name/description/trigger）")
	}
	fm := m[1]
	instructions = strings.TrimSpace(content[len(m[0]):])

	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			name = v
		case "description":
			description = v
		case "trigger":
			trigger = v
		}
	}
	if name == "" {
		return "", "", "", "", fmt.Errorf("SKILL.md frontmatter 缺少 name 字段")
	}
	if description == "" {
		return "", "", "", "", fmt.Errorf("SKILL.md frontmatter 缺少 description 字段")
	}
	return name, description, trigger, instructions, nil
}

// LoadSkillDir 从单个 skill 目录加载 Skill 定义
// 目录名必须等于 SKILL.md frontmatter 的 name 字段（强制一致）
// scripts/ 下所有文件递归读取为 SkillScript（相对路径保留，含 scripts/ 前缀）
// maxScriptBytes 单文件大小上限（0 = 不限制），防止大文件撑爆内存
func LoadSkillDir(dir string, maxScriptBytes int64) (*SkillDefinition, error) {
	mdPath := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", mdPath, err)
	}
	name, description, trigger, instructions, err := ParseSKILLMD(string(raw))
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", mdPath, err)
	}

	// 目录名必须等于 name（保证引用路径一致）
	if filepath.Base(dir) != name {
		return nil, fmt.Errorf("skill 目录名 %q 与 frontmatter name %q 不一致", filepath.Base(dir), name)
	}

	def := &SkillDefinition{
		Metadata: SkillMetadata{
			Name:        name,
			Description: description,
			Trigger:     trigger,
		},
		Instructions: instructions,
	}

	// 读取 scripts/ 目录（可选）
	scriptsDir := filepath.Join(dir, "scripts")
	if fi, err := os.Stat(scriptsDir); err == nil && fi.IsDir() {
		err = filepath.Walk(scriptsDir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, rerr := filepath.Rel(dir, p)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return fmt.Errorf("读取脚本 %s 失败: %w", p, rerr)
			}
			if maxScriptBytes > 0 && int64(len(data)) > maxScriptBytes {
				return fmt.Errorf("脚本 %s 超过大小上限 %d bytes", p, maxScriptBytes)
			}
			def.Scripts = append(def.Scripts, SkillScript{Filename: rel, Content: string(data)})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("加载 skill %s scripts 失败: %w", name, err)
		}
	}

	return def, nil
}

// LoadSkillsDir 扫描根目录下所有子目录，逐个按 LoadSkillDir 加载
// 跳过：无 SKILL.md 的目录（容忍参考目录、备份等）
func LoadSkillsDir(root string, maxScriptBytes int64) ([]*SkillDefinition, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // skills 目录不存在不报错
		}
		return nil, err
	}
	var defs []*SkillDefinition
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		def, err := LoadSkillDir(filepath.Join(root, e.Name()), maxScriptBytes)
		if err != nil {
			// 单个目录解析失败不阻断整体（记录后跳过），便于用户修复后重启生效
			return nil, fmt.Errorf("加载 skill 目录 %s 失败: %w", e.Name(), err)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

package api

import (
	"net/http"

	"agent-go/internal/tools/skill"

)

// health 健康检查
func (s *Server) health(c *GinCompat) {
	c.JSON(http.StatusOK, H{
		"status":  "ok",
		"service": "agent-go",
	})
}

// listTools 工具列表
// 列出所有常驻 FC 工具（registry 中的 + ask_user + load_skill）
// Skill 工具（Knovis）不在列表中：未 load_skill 时不可见，加载后才在 tools 列表中
func (s *Server) listTools(c *GinCompat) {
	// 从 registry 获取工具列表（常驻 FC：info/file/sandbox）
	toolList := s.orchestrator.GetRegistry().List()

	result := make([]H, 0, len(toolList)+2)
	for _, t := range toolList {
		result = append(result, H{
			"name":        t.Name,
			"description": t.Description,
			"category":    t.Category,
		})
	}

	// 追加 ask_user（特殊工具，不在 registry 中）
	result = append(result, H{
		"name":        "ask_user",
		"description": "向用户提问以获取更多信息、确认权限或让用户选择路径",
		"category":    "system",
	})

	// P4: 追加 load_skill（常驻 FC，Skill 按需加载入口，不在 registry 中）
	result = append(result, H{
		"name":        "load_skill",
		"description": skill.LoadSkillDefinition().Function.Description,
		"category":    "skill",
	})

	c.JSON(http.StatusOK, H{
		"tools": result,
		"total": len(result),
	})
}

package skills

import (
	"context"
	"fmt"

	"agent-go/internal/auth"
	"agent-go/internal/knovis"
	"agent-go/internal/llm"
	"agent-go/internal/tools"
	"agent-go/internal/tools/skill"
)

// KnovisSkillName Skill 唯一标识（与 skills/knovis/SKILL.md 的 name 一致）
const KnovisSkillName = "knovis"

// KnovisToolBuilders 返回 knovis skill 的内置 Go 工具构建器（混合模式）
// knovis 由 SKILL.md 驱动（skills/knovis/SKILL.md），执行依赖 Go 层能力：
// 用户 Knovis token 走 AES-256-GCM 加密存储（auth.AuthService 管理），
// 明文仅存在于内存闭包（load_skill 时解密），不落库不日志，脚本无法替代。
// main.go 通过 skillReg.AttachToolBuilders 附加到已从 SKILL.md 加载的 knovis 定义上。
func KnovisToolBuilders(authSvc *auth.AuthService, client *knovis.Client) []skill.ToolBuilder {
	return []skill.ToolBuilder{
		BuildKnovisGetFeed(authSvc, client),
		BuildKnovisGetProfile(authSvc, client),
		BuildKnovisGetPost(authSvc, client),
	}
}

// resolveToken 解析用户 Knovis token（load_skill 时调用，明文仅存内存）
func resolveToken(ctx context.Context, authSvc *auth.AuthService, userID string) (string, error) {
	if authSvc == nil {
		return "", fmt.Errorf("鉴权服务未配置，无法获取 Knovis token")
	}
	token, err := authSvc.GetKnovisToken(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("获取 Knovis token 失败: %w", err)
	}
	if token == "" {
		return "", fmt.Errorf("未设置 Knovis token，请先通过 POST /auth/knovis-token 设置")
	}
	return token, nil
}

// ===== 读操作（免审批）=====

// BuildKnovisGetFeed 构建动态流查询工具
func BuildKnovisGetFeed(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		token, err := resolveToken(ctx, authSvc, userID)
		if err != nil {
			return llm.ToolDefinition{}, nil, err
		}
		return llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_feed",
				Description: "获取 Knovis 动态流（读操作）。返回最新动态列表，分页由 page/page_size 控制。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"page": map[string]any{
							"type":        "integer",
							"description": "页码（从 1 开始，默认 1）",
						},
						"page_size": map[string]any{
							"type":        "integer",
							"description": "每页条数（默认 10，最大 50）",
						},
					},
				},
			},
		}, func(ctx context.Context, args map[string]any) (string, error) {
			page := 1
			if p, ok := args["page"].(float64); ok && p >= 1 {
				page = int(p)
			}
			pageSize := 10
			if ps, ok := args["page_size"].(float64); ok && ps >= 1 {
				pageSize = int(ps)
			}
			return client.GetFeed(ctx, token, page, pageSize)
		}, nil
	}
}

// BuildKnovisGetProfile 构建用户资料查询工具
func BuildKnovisGetProfile(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		token, err := resolveToken(ctx, authSvc, userID)
		if err != nil {
			return llm.ToolDefinition{}, nil, err
		}
		return llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_profile",
				Description: "获取 Knovis 用户资料（读操作）。user_id 为空时查当前登录用户。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"user_id": map[string]any{
							"type":        "string",
							"description": "目标用户 ID（为空则查当前登录用户）",
						},
					},
				},
			},
		}, func(ctx context.Context, args map[string]any) (string, error) {
			targetUserID, _ := args["user_id"].(string)
			return client.GetProfile(ctx, token, targetUserID)
		}, nil
	}
}

// BuildKnovisGetPost 构建动态详情查询工具
func BuildKnovisGetPost(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
	return func(ctx context.Context, userID string) (llm.ToolDefinition, tools.ToolHandler, error) {
		token, err := resolveToken(ctx, authSvc, userID)
		if err != nil {
			return llm.ToolDefinition{}, nil, err
		}
		return llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "knovis_get_post",
				Description: "获取 Knovis 动态详情（读操作，浏览数 +1）。",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"post_id": map[string]any{
							"type":        "string",
							"description": "动态 ID",
						},
					},
					"required": []string{"post_id"},
				},
			},
		}, func(ctx context.Context, args map[string]any) (string, error) {
			postID, _ := args["post_id"].(string)
			return client.GetPost(ctx, token, postID)
		}, nil
	}
}

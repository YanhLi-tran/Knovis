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

// KnovisSkillName Skill 唯一标识
const KnovisSkillName = "knovis"

// NewKnovisSkillDefinition 创建 Knovis Skill 定义（只读：Knovis 不实现社交互动模块）
// 低频复杂工具 → 封装 Skill：详细 schema 常驻浪费 context，load_skill 按需拉取。
// 用户不主动询问动态/用户资料时，load_skill 不会触发；触发后常驻到对话结束。
//
// Token 安全：用户 Knovis token 走 AES-256-GCM 加密存储（auth.AuthService 管理），
// load_skill 时解密传入 ToolBuilder，明文仅存在于内存闭包，不落库不日志
func NewKnovisSkillDefinition(authSvc *auth.AuthService, client *knovis.Client) *skill.SkillDefinition {
	instructions := `Knovis 只读查询工具已加载。可用操作：
- knovis_get_feed: 获取动态流（读操作，分页 page/page_size）
- knovis_get_profile: 获取用户资料（读操作，可查自己或他人）
- knovis_get_post: 获取动态详情（读操作）

注意：Knovis 不提供点赞/评论/关注/发帖等社交互动接口，相关写操作请在 Knovis 前端完成。`

	return &skill.SkillDefinition{
		Metadata: skill.SkillMetadata{
			Name:        KnovisSkillName,
			Description: "Knovis 用户与动态查询（查动态流/动态详情/用户资料），用户需已设置 Knovis token",
			Trigger:     "用户询问 Knovis 动态流、帖子、用户资料等社交数据时",
		},
		Instructions: instructions,
		ToolBuilders: []skill.ToolBuilder{
			buildGetFeed(authSvc, client),
			buildGetProfile(authSvc, client),
			buildGetPost(authSvc, client),
		},
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

func buildGetFeed(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
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

func buildGetProfile(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
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

func buildGetPost(authSvc *auth.AuthService, client *knovis.Client) skill.ToolBuilder {
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

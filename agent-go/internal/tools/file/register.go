package file

import (
	"context"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// RegisterFileTools 注册文件操作工具（FC 常驻层）
// 高频 + 简单 schema → 纯 FC 常驻（封装成 Skill 反而多一次加载往返，得不偿失）
// 所有工具通过 WS 下发到用户本地客户端执行（多用户隔离：按 userID 路由）
// 读操作（file_read/grep）免审批，写操作（file_write）需审批
func RegisterFileTools(registry *tools.Registry, hub *ws.Hub) {
	registry.Register(&tools.Tool{
		Name:        "file_read",
		Description: "读取用户本地文件内容。支持指定路径和最大字符数。读操作免审批。需用户本地 agent 在线。",
		Category:    "file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "文件路径（绝对路径或相对客户端工作目录的相对路径）",
				},
				"max_size": map[string]any{
					"type":        "integer",
					"description": "最大返回字符数（默认 10000，超出截断）",
				},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return fileRead(ctx, args, hub)
		},
	})

	registry.Register(&tools.Tool{
		Name:          "file_write",
		Description:   "写入用户本地文件。支持覆盖写和追加写。写操作需用户审批（执行前弹框确认）。需用户本地 agent 在线。",
		Category:      "file",
		NeedsApproval: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "文件路径",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "写入内容",
				},
				"mode": map[string]any{
					"type":        "string",
					"description": "写入模式：write（覆盖，默认）/ append（追加）",
					"enum":        []string{"write", "append"},
				},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return fileWrite(ctx, args, hub)
		},
	})

	registry.Register(&tools.Tool{
		Name:        "grep",
		Description: "在用户本地文件/目录中搜索匹配行（正则表达式）。读操作免审批。需用户本地 agent 在线。",
		Category:    "file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "正则表达式",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "搜索起点（文件或目录路径）",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "文件名匹配模式（如 *.go，默认 * 匹配所有文件）",
				},
			},
			"required": []string{"pattern", "path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return grepFiles(ctx, args, hub)
		},
	})

	// file_list: 列目录内容（高频读操作，免审批）
	// 与 ls/find 互补：file_list 走 WS 通道，sandbox ls 走 CLI 执行
	registry.Register(&tools.Tool{
		Name:        "file_list",
		Description: "列出用户本地目录内容（文件名+大小+修改时间）。读操作免审批。需用户本地 agent 在线。",
		Category:    "file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "目录路径（绝对路径或相对路径）",
				},
				"recursive": map[string]any{
					"type":        "boolean",
					"description": "是否递归列出子目录（默认 false，仅列一层）",
				},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return fileList(ctx, args, hub)
		},
	})
}

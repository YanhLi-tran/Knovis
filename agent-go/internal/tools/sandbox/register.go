package sandbox

import (
	"context"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// RegisterSandboxTools 注册沙箱命令执行工具（FC 常驻层）
// 中频 + 简单 schema → 纯 FC 常驻（审批流放执行层，不占 context）
// 所有 CLI 命令通过 WS 下发到用户本地客户端执行（多用户隔离：按 userID 路由）
// 硬约束：白名单 + 30s timeout + 环境变量净化 + 输出截断 4000 字符（客户端实施）
// 审批流：所有 CLI 命令均需用户审批（NeedsApproval=true），processToolCalls 拦截后走审批
func RegisterSandboxTools(registry *tools.Registry, hub *ws.Hub) {
	registry.Register(&tools.Tool{
		Name:          "sandbox_exec",
		Description:   "在用户本地执行 CLI 命令（白名单内 + 30s 超时 + 环境变量净化 + 输出截断 4000 字符）。所有命令需用户审批后执行。需用户本地 agent 在线。",
		Category:      "sandbox",
		NeedsApproval: true, // 硬约束：CLI 执行前需用户审批
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "命令字符串（支持管道和重定向）。命令名必须在白名单内：ls/cat/head/tail/wc/file/stat/grep/find/awk/sed/jq/sort/uniq/git/go/node/python/python3/pip/npm/curl/wget/ffmpeg/echo/pwd/date/mkdir/touch/cp/mv/dir/type/where/findstr/docker/docker-compose/kubectl/tar/zip/unzip/which/env/hostname",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "工作目录（可选，默认用户家目录）",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "超时秒数（可选，默认 30，最大 30）",
				},
			},
			"required": []string{"command"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return sandboxExec(ctx, args, hub)
		},
	})
}

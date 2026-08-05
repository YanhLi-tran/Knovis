package sandbox

import (
	"context"
	"fmt"
	"time"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// sandboxExec 执行 CLI 命令（通过 WS 下发到用户本地客户端执行）
// 硬约束（实际执行在客户端，服务端只做路由 + 审批拦截）：
//   - 白名单：客户端二次校验，白名单外命令直接拒绝
//   - timeout：30s 上限（硬约束），客户端 exec.CommandContext 强制中断
//   - 环境变量净化：客户端移除含 KEY/SECRET/TOKEN/PASSWORD 的变量
//   - 输出截断：客户端截断到 4000 字符
//
// 审批流：所有 CLI 命令均需用户审批（NeedsApproval=true）
// 审批流放执行层（processToolCalls 拦截），不占 LLM context
func sandboxExec(ctx context.Context, args map[string]any, hub *ws.Hub) (string, error) {
	userID, _ := ctx.Value(tools.CtxKeyUserID).(string)
	if userID == "" {
		return "", fmt.Errorf("缺少用户身份，无法下发指令到本地客户端")
	}

	// 服务端侧 timeout 上限 30s（硬约束）：即使 args 传更大值也截断
	timeout := 30 * time.Second
	if t, ok := args["timeout"].(float64); ok && t > 0 {
		if t > 30 {
			t = 30
		}
		timeout = time.Duration(int(t)) * time.Second
	}
	// 透传 timeout 给客户端（客户端按此值设 exec.CommandContext）
	args["timeout"] = int(timeout.Seconds())

	return hub.SendCommand(ctx, userID, ws.CmdSandboxExec, args, timeout+5*time.Second)
	// 服务端等待上限 = 客户端执行 timeout + 5s 缓冲（避免客户端未超时服务端先超时）
}

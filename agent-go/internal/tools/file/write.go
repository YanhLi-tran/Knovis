package file

import (
	"context"
	"fmt"
	"time"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// fileWrite 写文件（通过 WS 下发到用户本地客户端执行）
// 写操作需审批（NeedsApproval=true）：processToolCalls 在调用此 Handler 前先走审批流，
// 批准后才执行。审批流放执行层，不占 LLM context。
func fileWrite(ctx context.Context, args map[string]any, hub *ws.Hub) (string, error) {
	userID, _ := ctx.Value(tools.CtxKeyUserID).(string)
	if userID == "" {
		return "", fmt.Errorf("缺少用户身份，无法下发指令到本地客户端")
	}
	return hub.SendCommand(ctx, userID, ws.CmdFileWrite, args, 30*time.Second)
}

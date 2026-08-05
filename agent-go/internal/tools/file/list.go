package file

import (
	"context"
	"fmt"
	"time"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// fileList 列目录内容（通过 WS 下发到用户本地客户端执行）
// 读操作免审批，直接下发。userID 从 ctx 取（OTACO Run 注入）。
func fileList(ctx context.Context, args map[string]any, hub *ws.Hub) (string, error) {
	userID, _ := ctx.Value(tools.CtxKeyUserID).(string)
	if userID == "" {
		return "", fmt.Errorf("缺少用户身份，无法下发指令到本地客户端")
	}
	return hub.SendCommand(ctx, userID, "file_list", args, 30*time.Second)
}

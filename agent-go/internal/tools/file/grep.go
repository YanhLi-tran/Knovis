package file

import (
	"context"
	"fmt"
	"time"

	"agent-go/internal/tools"
	"agent-go/internal/ws"
)

// grepFiles 在文件/目录中搜索匹配行（通过 WS 下发到用户本地客户端执行）
// 读操作免审批。grep 可能扫描较多文件，给 60s 超时（比 file_read 长）。
func grepFiles(ctx context.Context, args map[string]any, hub *ws.Hub) (string, error) {
	userID, _ := ctx.Value(tools.CtxKeyUserID).(string)
	if userID == "" {
		return "", fmt.Errorf("缺少用户身份，无法下发指令到本地客户端")
	}
	return hub.SendCommand(ctx, userID, ws.CmdGrep, args, 60*time.Second)
}

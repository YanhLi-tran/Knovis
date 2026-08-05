package audit

import (
	"encoding/json"
	"log"

	"agent-go/internal/storage"
)

// Logger 审计日志记录器（异步写入，不阻断业务）
// 仅记录写操作：create/update/delete + 关键鉴权事件（login/logout/register）
type Logger struct {
	repo *storage.AuditRepository
}

// NewLogger 创建审计日志记录器
func NewLogger(repo *storage.AuditRepository) *Logger {
	return &Logger{repo: repo}
}

// Log 异步记录审计日志
//   - userID: 操作者用户ID
//   - action: 操作类型(create/update/delete/login/logout/register)
//   - resource: 资源类型(user/session/project/memory/llm_key/user_config)
//   - resourceID: 资源ID（可为空）
//   - ip: 操作者IP
//   - authType: 鉴权方式(jwt/client_id)
//   - detail: 操作详情（string 或可 JSON 序列化的结构体）
func (l *Logger) Log(userID, action, resource, resourceID, ip, authType string, detail any) {
	if l == nil || l.repo == nil {
		return
	}
	go func() {
		detailStr := ""
		if detail != nil {
			if s, ok := detail.(string); ok {
				detailStr = s
			} else {
				b, err := json.Marshal(detail)
				if err == nil {
					detailStr = string(b)
				}
			}
		}
		entry := &storage.AuditLog{
			UserID:     userID,
			Action:     action,
			Resource:   resource,
			ResourceID: resourceID,
			Detail:     detailStr,
			IP:         ip,
			AuthType:   authType,
		}
		if err := l.repo.Create(entry); err != nil {
			log.Printf("[WARN][audit] 写入审计日志失败: %v", err)
		}
	}()
}

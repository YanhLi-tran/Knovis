package storage

import (
	"time"

	"gorm.io/gorm"
)

// AuditLog 审计日志（仅记录写操作：create/update/delete + 关键鉴权事件）
type AuditLog struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     string    `gorm:"type:varchar(64);index;comment:操作者用户ID" json:"user_id"`
	Action     string    `gorm:"type:varchar(32);comment:操作类型(create/update/delete/login/logout/register)" json:"action"`
	Resource   string    `gorm:"type:varchar(32);comment:资源类型(user/session/project/memory/llm_key/user_config)" json:"resource"`
	ResourceID string    `gorm:"type:varchar(64);comment:资源ID" json:"resource_id"`
	Detail     string    `gorm:"type:text;comment:操作详情JSON" json:"detail"`
	IP         string    `gorm:"type:varchar(64);comment:操作者IP" json:"ip"`
	AuthType   string    `gorm:"type:varchar(16);comment:鉴权方式(jwt/client_id)" json:"auth_type"`
	CreatedAt  time.Time `gorm:"comment:操作时间;index" json:"created_at"`
}

// TableName 指定表名
func (AuditLog) TableName() string { return "agent_audit_logs" }

// AuditRepository 审计日志数据访问层（只写不删，保证审计完整性）
type AuditRepository struct {
	db *gorm.DB
}

// NewAuditRepository 创建审计日志仓库
func NewAuditRepository(db *gorm.DB) *AuditRepository {
	return &AuditRepository{db: db}
}

// Create 写入审计日志
func (r *AuditRepository) Create(entry *AuditLog) error {
	return r.db.Create(entry).Error
}

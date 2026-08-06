package storage

import (
	"time"

	"gorm.io/gorm"
)

// UserConfig 用户档案（全局记忆：基础信息 + 位置 + 偏好）
// 一对一关联 owner_id（当前为 client_id，P3 升级为 user_id）
type UserConfig struct {
	ID         uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     string         `gorm:"type:varchar(64);uniqueIndex;comment:用户ID（当前为client_id）" json:"user_id"`
	BasicInfo  string         `gorm:"type:text;comment:基础信息JSON（姓名/职业/年龄等）" json:"basic_info"`     // JSON
	Location   string         `gorm:"type:varchar(128);comment:用户位置（城市/时区）" json:"location"`            // 例: Asia/Shanghai
	Preferences string       `gorm:"type:text;comment:偏好JSON（语言/风格/UI等）" json:"preferences"`           // JSON
	RawText    string         `gorm:"type:text;comment:拼好的档案文本（便于直接注入system prompt）" json:"raw_text"` // 冗余字段，避免每次拼装
	// P9: Agent 行为设置（用户可配，覆盖全局默认）
	MaxToolRounds int    `gorm:"default:0;comment:连续调用工具轮数上限（0=用全局默认10，范围1-50）" json:"max_tool_rounds"`
	SandboxMode   string `gorm:"type:varchar(16);default:'';comment:沙箱权限模式 ask/auto/yolo（空=ask）" json:"sandbox_mode"`
	BackupMode    string `gorm:"type:varchar(16);default:'';comment:文件备份模式 snapshot/git（空=snapshot）" json:"backup_mode"`
	CreatedAt  time.Time      `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt  time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"`
}

// TableName 指定表名
func (UserConfig) TableName() string { return "agent_user_config" }

// Project 项目（顶层 session，项目下可创建子 session）
type Project struct {
	ID              string         `gorm:"primaryKey;type:varchar(36);comment:Project UUID" json:"id"`
	OwnerID         string         `gorm:"type:varchar(64);index;comment:归属用户ID" json:"owner_id"`
	Name            string         `gorm:"type:varchar(128);comment:项目名称" json:"name"`
	Description     string         `gorm:"type:text;comment:项目元信息/描述" json:"description"`
	Rules           string         `gorm:"type:text;comment:项目规则（注入system prompt）" json:"rules"`
	Context         string         `gorm:"type:text;comment:项目上下文（背景/目标等）" json:"context"`
	KeyPoints       string         `gorm:"type:text;comment:记忆要点（LLM自动提取累积）" json:"key_points"`
	UserDefined     string         `gorm:"type:text;comment:用户自定义备注" json:"user_defined"`
	MaxContextLength int            `gorm:"default:64000;comment:模型最大上下文长度（token数，项目级配置）" json:"max_context_length"`
	IsArchived      bool           `gorm:"default:false;comment:是否归档" json:"is_archived"`
	LastActiveAt    time.Time      `gorm:"comment:最后活跃时间" json:"last_active_at"`
	CreatedAt       time.Time      `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt       time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"`
}

// TableName 指定表名
func (Project) TableName() string { return "agent_projects" }

// Memory 项目记忆条目（混合检索目标：BM25全文 + Chroma向量）
type Memory struct {
	ID               string         `gorm:"primaryKey;type:varchar(36);comment:Memory UUID（同时作为Chroma doc id）" json:"id"`
	ProjectID        string         `gorm:"type:varchar(36);index;comment:所属项目" json:"project_id"`
	OwnerID          string         `gorm:"type:varchar(64);index;comment:归属用户ID" json:"owner_id"`
	Content          string         `gorm:"type:text;comment:记忆内容" json:"content"`
	MemoryType       string         `gorm:"type:varchar(32);comment:类型(fact/preference/event/summary/requirement等)" json:"memory_type"`
	Source           string         `gorm:"type:varchar(32);default:auto_extract;comment:来源(auto_extract/manual/cross_project)" json:"source"`
	SourceSessionID  string         `gorm:"type:varchar(36);comment:来源Session（自动提取时记录）" json:"source_session_id"`
	SourceRound      int            `gorm:"comment:来源轮次" json:"source_round"`
	Importance       int            `gorm:"default:50;comment:重要度0-100（影响检索排序）" json:"importance"`
	EmbeddingStatus  string         `gorm:"type:varchar(16);default:pending;index;comment:向量状态(pending/done/failed)" json:"embedding_status"`
	LastAccessedAt   time.Time      `gorm:"comment:最后访问时间（LRU用）" json:"last_accessed_at"`
	CreatedAt        time.Time      `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"`
}

// TableName 指定表名
func (Memory) TableName() string { return "agent_memories" }

// MemoryArchive 归档记忆（TTL 14周后从主表迁移过来，30天内可恢复）
type MemoryArchive struct {
	ID                 string    `gorm:"primaryKey;type:varchar(36);comment:原Memory UUID" json:"id"`
	OriginalProjectID  string    `gorm:"type:varchar(36);index;comment:原所属项目" json:"original_project_id"`
	OriginalOwnerID    string    `gorm:"type:varchar(64);index;comment:原归属用户ID" json:"original_owner_id"`
	Content            string    `gorm:"type:text;comment:记忆内容" json:"content"`
	MemoryType         string    `gorm:"type:varchar(32);comment:类型" json:"memory_type"`
	Source             string    `gorm:"type:varchar(32);comment:来源" json:"source"`
	ArchivedAt         time.Time `gorm:"comment:归档时间" json:"archived_at"`
	RestoreExpiresAt   time.Time `gorm:"comment:可恢复截止时间（archived_at+30天）" json:"restore_expires_at"`
	Restored           bool      `gorm:"default:false;comment:是否已恢复" json:"restored"`
	CreatedAt          time.Time `gorm:"comment:原创建时间" json:"created_at"`
}

// TableName 指定表名
func (MemoryArchive) TableName() string { return "agent_memory_archive" }

// CrossProjectGrant 跨项目读取授权
// granter_owner_id: 被读取项目所属用户；grantee_owner_id: 发起读取的当前用户
type CrossProjectGrant struct {
	ID             uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	GranterOwnerID string         `gorm:"type:varchar(64);index;comment:被读取项目所属用户ID" json:"granter_owner_id"`
	GranteeOwnerID string         `gorm:"type:varchar(64);index;comment:发起读取的当前用户ID" json:"grantee_owner_id"`
	ProjectID      string         `gorm:"type:varchar(36);index;comment:被授权访问的项目" json:"project_id"`
	IsActive       bool           `gorm:"default:true;comment:授权是否生效" json:"is_active"`
	GrantedAt      time.Time      `gorm:"comment:授权时间" json:"granted_at"`
	RevokedAt      gorm.DeletedAt `gorm:"index;comment:撤销时间" json:"revoked_at"`
	CreatedAt      time.Time      `gorm:"comment:创建时间" json:"created_at"`
}

// TableName 指定表名
func (CrossProjectGrant) TableName() string { return "agent_cross_project_grants" }

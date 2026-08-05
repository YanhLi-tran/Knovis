package storage

import (
	"time"

	"gorm.io/gorm"
)

// Session 对话会话
type Session struct {
	ID           string         `gorm:"primaryKey;type:varchar(36);comment:Session UUID" json:"id"`
	OwnerID      string         `gorm:"type:varchar(64);index;comment:归属用户ID（Knovis user_id 字符串化）" json:"owner_id"` // SSO 形态：Knovis user_id
	ProjectID    string         `gorm:"type:varchar(36);index;comment:所属项目ID（可空，空=无项目）" json:"project_id"`     // P2: 引入项目概念，session 归属某 project
	Title        string         `gorm:"type:varchar(128);default:'新对话';comment:Session 标题" json:"title"`
	Summary      string         `gorm:"type:text;comment:历史摘要（滑动窗口外内容）" json:"summary"`                    // 超窗部分的摘要，覆盖更新
	Pinned       bool           `gorm:"default:false;comment:是否置顶" json:"pinned"`                              // 置顶标记
	LastActiveAt time.Time      `gorm:"comment:最后活跃时间" json:"last_active_at"`                                  // 排序用
	CreatedAt    time.Time      `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"` // 软删除，7天后硬删除
}

// TableName 指定表名
func (Session) TableName() string { return "agent_sessions" }

// Message 对话消息（全量 OTACO 过程）
type Message struct {
	ID           uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID    string         `gorm:"type:varchar(36);index;comment:所属Session" json:"session_id"`
	Round        int            `gorm:"comment:OTACO 轮次（从1开始）" json:"round"` // 第几轮 OTACO
	Role         string         `gorm:"type:varchar(16);comment:角色(system/user/assistant/tool)" json:"role"`
	Stage        string         `gorm:"type:varchar(16);comment:OTACO阶段(observe/think/act/check/output)" json:"stage"` // 空表示非 OTACO 阶段消息
	Content      string         `gorm:"type:longtext;comment:消息内容" json:"content"`                                  // 文本内容
	ToolCallID   string         `gorm:"type:varchar(64);index;comment:工具调用ID" json:"tool_call_id"`                  // tool 角色消息的工具调用ID
	ToolCalls    string         `gorm:"type:text;comment:assistant发起的工具调用JSON" json:"tool_calls"`                  // JSON 数组
	Decision     string         `gorm:"type:varchar(16);comment:Observation决策(pass/retry/rollback)" json:"decision"` // 仅 observe 阶段
	Reason       string         `gorm:"type:varchar(512);comment:决策理由" json:"reason"`                                // Observation 决策理由
	Summarized   bool           `gorm:"default:false;index;comment:是否已被压缩到摘要（true=跳过加载，不进上下文）" json:"summarized"`
	SummarizedAt *time.Time     `gorm:"comment:压缩到摘要的时间（TTL起算点，nil=未压缩）" json:"summarized_at"`
	RestoredAt   *time.Time     `gorm:"comment:恢复查看的时间（用户主动查看压缩消息时重置，延长7天TTL；nil=未恢复）" json:"restored_at"`
	CreatedAt    time.Time      `gorm:"comment:创建时间" json:"created_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"`
}

// TableName 指定表名
func (Message) TableName() string { return "agent_messages" }

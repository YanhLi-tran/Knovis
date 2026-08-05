package storage

import (
	"errors"
	"log"
	"time"

	"gorm.io/gorm"
)

// User Agent 侧用户凭证（SSO 形态：只校验 Knovis 签发的 JWT，不自管用户身份）
// 用户身份（用户名/密码/资料）归 Knovis，agent-go 不再存；本表只保留 Agent 私有凭证：
// LLM key、Knovis token（AES-256-GCM 加密存储）+ 免费额度配额。
// ID 即 Knovis 的 user_id（数字 ID 字符串化，与 sessions.owner_id 对齐）
type User struct {
	ID                   string    `gorm:"primaryKey;type:varchar(36);comment:Knovis user_id（字符串化）" json:"id"`
	LLMKeyEncrypted      []byte    `gorm:"type:varbinary(512);comment:用户 LLM key 密文（AES-256-GCM）" json:"-"`
	LLMKeyVersion        int       `gorm:"default:1;comment:加密密钥版本（轮换用）" json:"llm_key_version"`
	KnovisTokenEncrypted []byte    `gorm:"column:knovis_token_encrypted;type:varbinary(512);comment:用户 Knovis token 密文（AES-256-GCM）" json:"-"`
	KnovisTokenVersion   int       `gorm:"column:knovis_token_version;default:1;comment:Knovis token 加密密钥版本" json:"-"`
	RateQuotaPerDay      int       `gorm:"default:10;comment:免费额度：每天对话次数（自带 key 不限）" json:"rate_quota_per_day"`
	CreatedAt            time.Time `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt            time.Time `gorm:"comment:更新时间" json:"updated_at"`
}

// TableName 指定表名（agent 侧私有凭证表，与 Knovis 的用户表解耦）
func (User) TableName() string { return "agent_user_credentials" }

// UserRepository 用户凭证数据访问层
type UserRepository struct {
	db *gorm.DB
}

// NewUserRepository 创建用户凭证仓库
func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

// EnsureCredential 确保用户凭证记录存在（Lazy upsert：Knovis 用户首次使用 Agent 时按需创建）
// 不存在则创建一行（默认免费额度 10/天）；存在则幂等返回
func (r *UserRepository) EnsureCredential(id string) error {
	cred := &User{ID: id, RateQuotaPerDay: 10}
	if err := r.db.Where("id = ?", id).FirstOrCreate(cred).Error; err != nil {
		return err
	}
	return nil
}

// GetByID 根据 ID 查询用户凭证
func (r *UserRepository) GetByID(id string) (*User, error) {
	var u User
	if err := r.db.Where("id = ?", id).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// UpdateLLMKey 更新用户 LLM key 密文 + 版本
func (r *UserRepository) UpdateLLMKey(id string, encrypted []byte, version int) error {
	if err := r.db.Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{
			"llm_key_encrypted": encrypted,
			"llm_key_version":   version,
			"updated_at":        time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新用户 LLM key id=%s version=%d", id, version)
	return nil
}

// UpdateKnovisToken 更新用户 Knovis token 密文 + 版本（knovis Skill 调用 Knovis API 时解密使用）
func (r *UserRepository) UpdateKnovisToken(id string, encrypted []byte, version int) error {
	if err := r.db.Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{
			"knovis_token_encrypted": encrypted,
			"knovis_token_version":   version,
			"updated_at":             time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新用户 Knovis token id=%s version=%d", id, version)
	return nil
}

// UpdateRateQuota 更新免费额度
func (r *UserRepository) UpdateRateQuota(id string, quota int) error {
	return r.db.Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{"rate_quota_per_day": quota, "updated_at": time.Now().UTC()}).Error
}

// UpdateFields 按字段更新
func (r *UserRepository) UpdateFields(id string, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	return r.db.Model(&User{}).Where("id = ?", id).Updates(fields).Error
}

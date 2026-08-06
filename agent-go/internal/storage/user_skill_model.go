package storage

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ScriptFile skill 执行代码文件（storage 自有结构，避免 storage→skill 循环依赖）
// 与 skill.SkillScript 字段一致，由 api/main 层负责转换
type ScriptFile struct {
	Filename string `json:"filename"` // 相对 skill 目录的路径，如 scripts/gen.py
	Content  string `json:"content"`
}

// UserSkill 用户上传的私有 Skill（多租户隔离：仅 owner 可见/可加载）
// 内容与文件型内置 skill 对齐：SKILL.md 三字段（name/description/trigger）+ 正文流程 + scripts 执行代码。
// name 全局唯一（跨用户），保证 Skill 注册表 map[name] 不冲突；上传时校验不与内置 skill 重名。
// Scripts 以 JSON 存储（[]ScriptFile），load_skill 时同步到用户本地 workspace。
type UserSkill struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      string    `gorm:"column:user_id;size:64;index;comment:所有者（Knovis user_id）" json:"user_id"`
	Name        string    `gorm:"column:name;size:64;uniqueIndex;comment:skill 唯一标识（全局唯一）" json:"name"`
	Description string    `gorm:"column:description;type:text;comment:一句话描述（注册表注入）" json:"description"`
	Trigger     string    `gorm:"column:trigger;type:text;comment:显式触发条件" json:"trigger"`
	ContentMD   string    `gorm:"column:content_md;type:mediumtext;comment:完整 SKILL.md（frontmatter+正文）" json:"content_md"`
	Scripts     string    `gorm:"column:scripts;type:json;comment:执行代码 []ScriptFile" json:"scripts"`
	CreatedAt   time.Time `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt   time.Time `gorm:"comment:更新时间" json:"updated_at"`
}

// TableName 指定表名
func (UserSkill) TableName() string { return "user_skills" }

// ScriptsList 解析 scripts JSON 为结构化列表
func (s *UserSkill) ScriptsList() ([]ScriptFile, error) {
	if s.Scripts == "" {
		return nil, nil
	}
	var list []ScriptFile
	if err := json.Unmarshal([]byte(s.Scripts), &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SetScripts 序列化 scripts 列表为 JSON
// MySQL JSON 列不接受空字符串，空列表存 "[]"（合法 JSON）
func (s *UserSkill) SetScripts(list []ScriptFile) error {
	if len(list) == 0 {
		s.Scripts = "[]"
		return nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	s.Scripts = string(b)
	return nil
}

// UserSkillRepository 用户 Skill 数据访问层
type UserSkillRepository struct {
	db *gorm.DB
}

// NewUserSkillRepository 创建仓库
func NewUserSkillRepository(db *gorm.DB) *UserSkillRepository {
	return &UserSkillRepository{db: db}
}

// Create 创建用户 Skill（name 唯一冲突返回错误）
func (r *UserSkillRepository) Create(s *UserSkill) error {
	return r.db.Create(s).Error
}

// ListByUser 列出用户自己的 Skill
func (r *UserSkillRepository) ListByUser(userID string) ([]UserSkill, error) {
	var list []UserSkill
	if err := r.db.Where("user_id = ?", userID).Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListAll 列出全部用户 Skill（服务启动时加载到 Registry 缓存用）
func (r *UserSkillRepository) ListAll() ([]UserSkill, error) {
	var list []UserSkill
	if err := r.db.Order("created_at DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// GetByName 按 name 查询（任意用户，用于重名校验与加载）
func (r *UserSkillRepository) GetByName(name string) (*UserSkill, error) {
	var s UserSkill
	if err := r.db.Where("name = ?", name).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// Delete 删除用户自己的 Skill（userID + name 双条件，防越权删他人）
func (r *UserSkillRepository) Delete(userID, name string) error {
	return r.db.Where("user_id = ? AND name = ?", userID, name).Delete(&UserSkill{}).Error
}

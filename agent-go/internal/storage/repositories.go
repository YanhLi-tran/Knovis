package storage

import "gorm.io/gorm"

// Repositories 聚合所有 Repository，便于一次性注入
type Repositories struct {
	DB              *gorm.DB
	Cache           *Cache            // P2: Redis 缓存层（不可用时降级，nil-safe）
	User            *UserRepository   // P3: 用户账号（JWT 鉴权）
	Session         *SessionRepository
	Message         *MessageRepository
	UserConfig      *UserConfigRepository
	Project         *ProjectRepository
	Memory          *MemoryRepository
	MemoryArchive   *MemoryArchiveRepository
	CrossProject    *CrossProjectGrantRepository
	Audit           *AuditRepository  // P3: 审计日志（只写）
	Document        *DocumentRepository        // P5: RAG 文档
	DocumentChunk   *DocumentChunkRepository  // P5: RAG 文档分块
	UserSkill       *UserSkillRepository       // P7: 用户上传的私有 Skill
}

// NewRepositories 初始化所有 Repository（不含缓存，缓存由 main 注入）
func NewRepositories(db *gorm.DB) *Repositories {
	return &Repositories{
		DB:              db,
		User:            NewUserRepository(db),
		Session:         NewSessionRepository(db),
		Message:         NewMessageRepository(db),
		UserConfig:      NewUserConfigRepository(db),
		Project:         NewProjectRepository(db),
		Memory:          NewMemoryRepository(db),
		MemoryArchive:   NewMemoryArchiveRepository(db),
		CrossProject:    NewCrossProjectGrantRepository(db),
		Audit:           NewAuditRepository(db),
		Document:        NewDocumentRepository(db),
		DocumentChunk:   NewDocumentChunkRepository(db),
		UserSkill:       NewUserSkillRepository(db),
	}
}

// WithCache 注入 Redis 缓存（链式）
func (r *Repositories) WithCache(cache *Cache) *Repositories {
	r.Cache = cache
	return r
}

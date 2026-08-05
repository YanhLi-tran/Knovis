package storage

import (
	"errors"
	"log"
	"time"

	"gorm.io/gorm"
)

// SessionRepository Session 数据访问层
type SessionRepository struct {
	db *gorm.DB
}

// NewSessionRepository 创建 Session 仓库
func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

// Create 创建新 Session
func (r *SessionRepository) Create(session *Session) error {
	if session.ID == "" {
		return errors.New("session ID 不能为空")
	}
	if session.LastActiveAt.IsZero() {
		session.LastActiveAt = time.Now().UTC()
	}
	if err := r.db.Create(session).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 创建会话 id=%s owner=%s project=%s", session.ID, session.OwnerID, session.ProjectID)
	return nil
}

// GetByID 根据 ID 查询 Session（不含软删除）
// P3: ownerID != "" 时强制 owner 校验（越权返回 nil）
func (r *SessionRepository) GetByID(id, ownerID string) (*Session, error) {
	var s Session
	q := r.db.Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// ListByOwner 查询某用户的所有 Session（按置顶+最后活跃时间排序）
func (r *SessionRepository) ListByOwner(ownerID string) ([]Session, error) {
	var sessions []Session
	// 置顶在前，最后活跃时间倒序
	err := r.db.Where("owner_id = ?", ownerID).
		Order("pinned DESC, last_active_at DESC").
		Find(&sessions).Error
	return sessions, err
}

// ListAll 查询所有 Session（管理用，按置顶+最后活跃时间排序）
func (r *SessionRepository) ListAll() ([]Session, error) {
	var sessions []Session
	err := r.db.Order("pinned DESC, last_active_at DESC").Find(&sessions).Error
	return sessions, err
}

// UpdateTitle 更新标题
// P3: ownerID != "" 时强制 owner 校验
func (r *SessionRepository) UpdateTitle(id, ownerID, title string) error {
	q := r.db.Model(&Session{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"title": title, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新会话标题 id=%s owner=%s", id, ownerID)
	return nil
}

// UpdatePinned 更新置顶状态
// P3: ownerID != "" 时强制 owner 校验
func (r *SessionRepository) UpdatePinned(id, ownerID string, pinned bool) error {
	q := r.db.Model(&Session{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"pinned": pinned, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新会话置顶 id=%s owner=%s pinned=%v", id, ownerID, pinned)
	return nil
}

// UpdateSummary 更新摘要（覆盖更新）
// P3: ownerID != "" 时强制 owner 校验（内部 OTACO 压缩调用时传 ""）
func (r *SessionRepository) UpdateSummary(id, ownerID, summary string) error {
	q := r.db.Model(&Session{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"summary": summary, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新会话摘要 id=%s owner=%s", id, ownerID)
	return nil
}

// TouchLastActive 更新最后活跃时间
// P3: ownerID != "" 时强制 owner 校验（内部 OTACO 调用时传 ""）
func (r *SessionRepository) TouchLastActive(id, ownerID string) error {
	q := r.db.Model(&Session{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("last_active_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新会话活跃时间 id=%s owner=%s", id, ownerID)
	return nil
}

// SoftDelete 软删除 Session（关联消息一并软删除）
// P3: ownerID != "" 时强制 owner 校验
func (r *SessionRepository) SoftDelete(id, ownerID string) error {
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// 软删除 Session（带 owner 校验）
		sq := tx.Model(&Session{}).Where("id = ?", id)
		if ownerID != "" {
			sq = sq.Where("owner_id = ?", ownerID)
		}
		if err := sq.Update("deleted_at", now).Error; err != nil {
			return err
		}
		// 软删除关联消息（通过子查询限定 owner 范围，防止伪造 session_id 跨用户删消息）
		mq := tx.Model(&Message{}).Where("session_id = ?", id)
		if ownerID != "" {
			mq = mq.Where("session_id IN (SELECT id FROM agent_sessions WHERE owner_id = ? AND id = ?)", ownerID, id)
		}
		if err := mq.Update("deleted_at", now).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	log.Printf("[INFO][storage] 软删除会话 id=%s owner=%s", id, ownerID)
	return nil
}

// CountActive 统计某用户的活跃 Session 数量（不含软删除）
func (r *SessionRepository) CountActive(ownerID string) (int64, error) {
	var count int64
	err := r.db.Model(&Session{}).Where("owner_id = ?", ownerID).Count(&count).Error
	return count, err
}

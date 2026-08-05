package storage

import (
	"errors"
	"log"
	"time"

	"gorm.io/gorm"
)

// MessageRepository Message 数据访问层
type MessageRepository struct {
	db *gorm.DB
}

// NewMessageRepository 创建 Message 仓库
func NewMessageRepository(db *gorm.DB) *MessageRepository {
	return &MessageRepository{db: db}
}

// Create 创建单条消息
func (r *MessageRepository) Create(msg *Message) error {
	if err := r.db.Create(msg).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 创建消息 id=%d session=%s round=%d role=%s", msg.ID, msg.SessionID, msg.Round, msg.Role)
	return nil
}

// CreateBatch 批量创建消息（一轮 OTACO 的多条消息原子写入）
func (r *MessageRepository) CreateBatch(msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&msgs).Error
	}); err != nil {
		return err
	}
	log.Printf("[INFO][storage] 批量创建消息 count=%d session=%s", len(msgs), msgs[0].SessionID)
	return nil
}

// GetBySessionID 查询某 Session 的全部消息（按时间正序，不含软删除）
func (r *MessageRepository) GetBySessionID(sessionID string) ([]Message, error) {
	var msgs []Message
	err := r.db.Where("session_id = ?", sessionID).
		Order("id ASC").
		Find(&msgs).Error
	return msgs, err
}

// GetRecentBySessionID 查询某 Session 最近 N 轮的消息
// round 字段表示 OTACO 轮次，这里按 round 降序取最近 windowRounds 轮的所有消息
func (r *MessageRepository) GetRecentBySessionID(sessionID string, windowRounds int) ([]Message, error) {
	// 先查最大 round
	var maxRound int
	if err := r.db.Model(&Message{}).
		Where("session_id = ?", sessionID).
		Select("COALESCE(MAX(round), 0)").
		Scan(&maxRound).Error; err != nil {
		return nil, err
	}
	if maxRound == 0 {
		return nil, nil
	}

	// 计算起始 round
	startRound := maxRound - windowRounds + 1
	if startRound < 1 {
		startRound = 1
	}

	var msgs []Message
	err := r.db.Where("session_id = ? AND round >= ?", sessionID, startRound).
		Order("id ASC").
		Find(&msgs).Error
	return msgs, err
}

// GetMessagesBeforeRound 查询某 Session 指定 round 之前的消息（用于生成摘要）
func (r *MessageRepository) GetMessagesBeforeRound(sessionID string, beforeRound int) ([]Message, error) {
	var msgs []Message
	err := r.db.Where("session_id = ? AND round < ?", sessionID, beforeRound).
		Order("id ASC").
		Find(&msgs).Error
	return msgs, err
}

// GetUnsummarizedBeforeRound 查询某 Session 指定 round 之前且未被压缩的消息
// 用于滑动窗口压缩：只压缩 summarized=false 的窗口外消息，避免重复压缩
func (r *MessageRepository) GetUnsummarizedBeforeRound(sessionID string, beforeRound int) ([]Message, error) {
	var msgs []Message
	err := r.db.Where("session_id = ? AND round < ? AND summarized = ?", sessionID, beforeRound, false).
		Order("round ASC, id ASC").
		Find(&msgs).Error
	return msgs, err
}

// MarkSummarized 批量标记消息为已压缩（summarized=true + summarized_at=now）
// 压缩成功后调用，后续 loadHistory 不再加载这些消息
// summarized_at 作为 TTL 起算点，7 天后若仍未被用户恢复查看，由消息 TTL 任务软删
func (r *MessageRepository) MarkSummarized(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	if err := r.db.Model(&Message{}).Where("id IN ?", ids).
		Updates(map[string]any{
			"summarized":    true,
			"summarized_at": now,
		}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 标记消息已压缩 count=%d summarized_at=%s", len(ids), now.Format(time.RFC3339))
	return nil
}

// ListSummarizedExpired 查询已压缩且超期未恢复的消息（消息 TTL 任务用）
// 规则（与用户对齐："压缩后一周未恢复的消息软删"）：
//   - summarized = true
//   - restored_at 为 nil（从未恢复查看过）→ summarized_at + 7天 < now（即 summarized_at < threshold）
//   - restored_at 有值（用户曾查看过）→ restored_at + 7天 < now（即 restored_at < threshold）
//     用户每次"恢复查看"会重置 restored_at = now，相当于再给 7 天窗口期
// limit 控制单批数量，避免一次拉太多
func (r *MessageRepository) ListSummarizedExpired(threshold time.Time, limit int) ([]Message, error) {
	var msgs []Message
	q := r.db.Where(
		"summarized = ? AND ((restored_at IS NULL AND summarized_at < ?) OR (restored_at IS NOT NULL AND restored_at < ?))",
		true, threshold, threshold,
	)
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&msgs).Error
	return msgs, err
}

// SoftDeleteByIDs 批量软删除消息（deleted_at = now）
// 消息 TTL 任务调用：超期未恢复的已压缩消息软删
// 软删后 GetBySessionID / GetRecentBySessionID 等查询自动过滤（GORM 默认排除带 deleted_at 的记录）
func (r *MessageRepository) SoftDeleteByIDs(ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.Where("id IN ?", ids).Delete(&Message{})
	if result.Error != nil {
		return 0, result.Error
	}
	log.Printf("[INFO][storage] 软删除超期已压缩消息 count=%d", result.RowsAffected)
	return result.RowsAffected, nil
}

// GetSummarizedBySessionID 查询某 Session 已被压缩的消息（供前端展示+恢复用）
// 按 round 分组，分页加载
func (r *MessageRepository) GetSummarizedBySessionID(sessionID string, limit, offset int) ([]Message, error) {
	var msgs []Message
	q := r.db.Where("session_id = ? AND summarized = ?", sessionID, true).
		Order("round ASC, id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	err := q.Find(&msgs).Error
	return msgs, err
}

// RestoreMessage 恢复单条消息到上下文（标记 restored_at=now，重置 TTL 计时）
// 注意：恢复只是重置 TTL 计时，不会立即把消息加回 loadHistory（summarized 仍为 true）
// 用户若要查看内容，前端从 GetSummarizedBySessionID 加载展示
func (r *MessageRepository) RestoreMessage(id uint) error {
	now := time.Now().UTC()
	if err := r.db.Model(&Message{}).Where("id = ?", id).
		Update("restored_at", now).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 恢复消息到上下文 id=%d restored_at=%s", id, now.Format(time.RFC3339))
	return nil
}

// CountRounds 统计某 Session 的 OTACO 轮次数
func (r *MessageRepository) CountRounds(sessionID string) (int, error) {
	var maxRound int
	err := r.db.Model(&Message{}).
		Where("session_id = ?", sessionID).
		Select("COALESCE(MAX(round), 0)").
		Scan(&maxRound).Error
	return maxRound, err
}

// DeleteBySessionID 软删除某 Session 的所有消息（通常由 Session 软删除触发）
func (r *MessageRepository) DeleteBySessionID(sessionID string) error {
	if err := r.db.Where("session_id = ?", sessionID).Delete(&Message{}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 软删除会话消息 session=%s", sessionID)
	return nil
}

// ErrSessionNotFound Session 不存在
var ErrSessionNotFound = errors.New("session not found")

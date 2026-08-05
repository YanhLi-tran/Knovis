package storage

import (
	"errors"
	"log"
	"time"

	"gorm.io/gorm"
)

// ==================== UserConfig Repository ====================
// （UserConfig 按 user_id 查询，天然隔离，无需改造）

// UserConfigRepository 用户档案数据访问层
type UserConfigRepository struct {
	db *gorm.DB
}

// NewUserConfigRepository 创建用户档案仓库
func NewUserConfigRepository(db *gorm.DB) *UserConfigRepository {
	return &UserConfigRepository{db: db}
}

// GetByUserID 根据用户ID查询档案
func (r *UserConfigRepository) GetByUserID(userID string) (*UserConfig, error) {
	var uc UserConfig
	if err := r.db.Where("user_id = ?", userID).First(&uc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &uc, nil
}

// Upsert 新建或更新用户档案（按 user_id 唯一）
func (r *UserConfigRepository) Upsert(uc *UserConfig) error {
	var existing UserConfig
	err := r.db.Where("user_id = ?", uc.UserID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.Create(uc).Error; err != nil {
			return err
		}
		log.Printf("[INFO][storage] 创建用户档案 id=%d user=%s", uc.ID, uc.UserID)
		return nil
	}
	if err != nil {
		return err
	}
	uc.ID = existing.ID
	uc.CreatedAt = existing.CreatedAt
	if err := r.db.Save(uc).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新用户档案 id=%d user=%s", uc.ID, uc.UserID)
	return nil
}

// Delete 软删除
func (r *UserConfigRepository) Delete(userID string) error {
	if err := r.db.Where("user_id = ?", userID).Delete(&UserConfig{}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 删除用户档案 user=%s", userID)
	return nil
}

// ==================== Project Repository ====================
// P3: owner_id 隔离补全。ownerID=="" 表示不限制（跨用户任务用）

// ProjectRepository 项目数据访问层
type ProjectRepository struct {
	db *gorm.DB
}

// NewProjectRepository 创建项目仓库
func NewProjectRepository(db *gorm.DB) *ProjectRepository {
	return &ProjectRepository{db: db}
}

// Create 创建项目
func (r *ProjectRepository) Create(p *Project) error {
	if p.LastActiveAt.IsZero() {
		p.LastActiveAt = time.Now().UTC()
	}
	if err := r.db.Create(p).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 创建项目 id=%s owner=%s", p.ID, p.OwnerID)
	return nil
}

// GetByID 根据 ID 查询项目
// ownerID != "" 时强制 owner 校验（越权返回 nil）
func (r *ProjectRepository) GetByID(id, ownerID string) (*Project, error) {
	var p Project
	q := r.db.Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// ListByOwner 查询某用户的项目
func (r *ProjectRepository) ListByOwner(ownerID string) ([]Project, error) {
	var projects []Project
	err := r.db.Where("owner_id = ?", ownerID).
		Order("last_active_at DESC").
		Find(&projects).Error
	return projects, err
}

// FindByName 按 owner + 项目名精确查询（@跨项目读取用，仅限自己的项目）
func (r *ProjectRepository) FindByName(ownerID, name string) (*Project, error) {
	var p Project
	err := r.db.Where("owner_id = ? AND name = ?", ownerID, name).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Update 更新项目字段（调用方需先校验 owner）
func (r *ProjectRepository) Update(p *Project) error {
	return r.db.Save(p).Error
}

// UpdateFields 按字段更新
// ownerID != "" 时强制 owner 校验（越权返回零影响，RowsAffected=0）
func (r *ProjectRepository) UpdateFields(id, ownerID string, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	q := r.db.Model(&Project{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(fields).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新项目字段 id=%s owner=%s", id, ownerID)
	return nil
}

// TouchLastActive 更新最后活跃时间
// ownerID != "" 时强制 owner 校验
func (r *ProjectRepository) TouchLastActive(id, ownerID string) error {
	q := r.db.Model(&Project{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	return q.Update("last_active_at", time.Now().UTC()).Error
}

// SoftDelete 软删除项目
// ownerID != "" 时强制 owner 校验
func (r *ProjectRepository) SoftDelete(id, ownerID string) error {
	q := r.db.Model(&Project{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("deleted_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 软删除项目 id=%s owner=%s", id, ownerID)
	return nil
}

// ==================== Memory Repository ====================
// P3: owner_id 隔离补全（project_memory 硬约束：检索时仓储层强制 WHERE owner_id=? 过滤）
// ownerID=="" 表示不限制（TTL 跨用户归档任务等内部场景用）

// MemoryRepository 记忆数据访问层
type MemoryRepository struct {
	db *gorm.DB
}

// NewMemoryRepository 创建记忆仓库
func NewMemoryRepository(db *gorm.DB) *MemoryRepository {
	return &MemoryRepository{db: db}
}

// Create 创建记忆
func (r *MemoryRepository) Create(m *Memory) error {
	if m.LastAccessedAt.IsZero() {
		m.LastAccessedAt = time.Now().UTC()
	}
	if err := r.db.Create(m).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 创建记忆 id=%s project=%s owner=%s", m.ID, m.ProjectID, m.OwnerID)
	return nil
}

// CreateBatch 批量创建记忆
func (r *MemoryRepository) CreateBatch(ms []Memory) error {
	if len(ms) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range ms {
		if ms[i].LastAccessedAt.IsZero() {
			ms[i].LastAccessedAt = now
		}
	}
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&ms).Error
	}); err != nil {
		return err
	}
	log.Printf("[INFO][storage] 批量创建记忆 count=%d project=%s owner=%s", len(ms), ms[0].ProjectID, ms[0].OwnerID)
	return nil
}

// GetByID 根据 ID 查询记忆
// ownerID != "" 时强制 owner 校验（越权返回 nil）
func (r *MemoryRepository) GetByID(id, ownerID string) (*Memory, error) {
	var m Memory
	q := r.db.Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// ListByProject 查询某项目的记忆（按重要度+最近访问排序）
// ownerID != "" 时强制 owner 校验（防止伪造 projectID 跨用户读取）
func (r *MemoryRepository) ListByProject(projectID, ownerID string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	var ms []Memory
	q := r.db.Where("project_id = ?", projectID)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	err := q.Order("importance DESC, last_accessed_at DESC").
		Limit(limit).
		Find(&ms).Error
	return ms, err
}

// ListPendingEmbedding 查询待生成 embedding 的记忆
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) ListPendingEmbedding(projectID, ownerID string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 32
	}
	var ms []Memory
	q := r.db.Where("project_id = ? AND embedding_status = ?", projectID, "pending")
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	err := q.Order("created_at ASC").
		Limit(limit).
		Find(&ms).Error
	return ms, err
}

// CountPendingEmbedding 统计待 embed 的记忆数（用于判断是否达到批量阈值）
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) CountPendingEmbedding(projectID, ownerID string) (int64, error) {
	var count int64
	q := r.db.Model(&Memory{}).Where("project_id = ? AND embedding_status = ?", projectID, "pending")
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	err := q.Count(&count).Error
	return count, err
}

// MarkEmbeddingDone 标记 embedding 完成
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) MarkEmbeddingDone(id, ownerID string) error {
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"embedding_status": "done", "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 标记 embedding 完成 id=%s owner=%s", id, ownerID)
	return nil
}

// MarkEmbeddingFailed 标记 embedding 失败
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) MarkEmbeddingFailed(id, ownerID string) error {
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"embedding_status": "failed", "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 标记 embedding 失败 id=%s owner=%s", id, ownerID)
	return nil
}

// BatchMarkEmbeddingDone 批量标记完成
// ownerID != "" 时强制 owner 校验（仅标记属于该 owner 的）
func (r *MemoryRepository) BatchMarkEmbeddingDone(ids []string, ownerID string) error {
	if len(ids) == 0 {
		return nil
	}
	q := r.db.Model(&Memory{}).Where("id IN ?", ids)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{"embedding_status": "done", "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 批量标记 embedding 完成 count=%d owner=%s", len(ids), ownerID)
	return nil
}

// UpdateContent 更新记忆内容（用户编辑）
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) UpdateContent(id, ownerID, content string) error {
	// 用户编辑后需重新生成 embedding
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(map[string]any{
		"content":          content,
		"embedding_status": "pending",
		"updated_at":       time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新记忆内容 id=%s owner=%s", id, ownerID)
	return nil
}

// UpdateFields 按字段更新记忆（不改 content，不触发重新 embed）
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) UpdateFields(id, ownerID string, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Updates(fields).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新记忆字段 id=%s owner=%s", id, ownerID)
	return nil
}

// TouchLastAccessed 更新最后访问时间（LRU 用）
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) TouchLastAccessed(id, ownerID string) error {
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("last_accessed_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 更新记忆访问时间 id=%s owner=%s", id, ownerID)
	return nil
}

// BatchTouchLastAccessed 批量更新最后访问时间
// ownerID != "" 时强制 owner 校验（仅更新属于该 owner 的）
func (r *MemoryRepository) BatchTouchLastAccessed(ids []string, ownerID string) error {
	if len(ids) == 0 {
		return nil
	}
	q := r.db.Model(&Memory{}).Where("id IN ?", ids)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("last_accessed_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 批量更新记忆访问时间 count=%d owner=%s", len(ids), ownerID)
	return nil
}

// SoftDelete 软删除记忆
// ownerID != "" 时强制 owner 校验
func (r *MemoryRepository) SoftDelete(id, ownerID string) error {
	q := r.db.Model(&Memory{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("deleted_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 软删除记忆 id=%s owner=%s", id, ownerID)
	return nil
}

// SoftDeleteByProject 软删除某项目全部记忆（删除项目时级联用）
// ownerID != "" 时强制 owner 校验（双重保险，防止伪造 projectID）
func (r *MemoryRepository) SoftDeleteByProject(projectID, ownerID string) error {
	q := r.db.Model(&Memory{}).Where("project_id = ?", projectID)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	if err := q.Update("deleted_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 软删除项目全部记忆 project=%s owner=%s", projectID, ownerID)
	return nil
}

// ListExpired 查询超过 TTL 未访问的记忆（TTL 管理，跨用户扫描）
// 此方法为内部 TTL 任务用，不加 owner 过滤（需扫描全表）
func (r *MemoryRepository) ListExpired(before time.Time, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 500
	}
	var ms []Memory
	err := r.db.Joins("LEFT JOIN agent_projects ON agent_projects.id = agent_memories.project_id").
		Where("agent_memories.last_accessed_at < ? AND agent_memories.embedding_status = ? AND agent_memories.importance < ? AND (agent_projects.is_archived = ? OR agent_projects.id IS NULL)",
			before, "done", 80, false).
		Order("agent_memories.last_accessed_at ASC").
		Limit(limit).
		Find(&ms).Error
	return ms, err
}

// HardDeleteByIDs 硬删除记忆（归档后清理主表用，内部任务，不加 owner 过滤）
func (r *MemoryRepository) HardDeleteByIDs(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	result := r.db.Unscoped().Where("id IN ?", ids).Delete(&Memory{})
	if result.Error != nil {
		log.Printf("[ERROR][storage] 硬删除记忆失败 count=%d err=%v", len(ids), result.Error)
		return result.Error
	}
	log.Printf("[INFO][storage] 硬删除记忆 count=%d", result.RowsAffected)
	return nil
}

// BM25Search MySQL FULLTEXT 检索（top-N）
// ownerID != "" 时强制 owner 校验（project_memory 硬约束：检索时仓储层强制 WHERE owner_id=? 过滤）
func (r *MemoryRepository) BM25Search(projectID, ownerID, query string, topN int) ([]Memory, error) {
	if topN <= 0 {
		topN = 10
	}
	var ms []Memory
	q := r.db.Where("project_id = ? AND MATCH(content) AGAINST (? IN NATURAL LANGUAGE MODE)", projectID, query)
	if ownerID != "" {
		q = q.Where("owner_id = ?", ownerID)
	}
	err := q.Order("importance DESC, last_accessed_at DESC").
		Limit(topN).
		Find(&ms).Error
	return ms, err
}

// ==================== MemoryArchive Repository ====================
// P3: owner_id 隔离补全。字段为 original_owner_id。ownerID=="" 表示不限制（TTL 清理任务用）

// MemoryArchiveRepository 归档记忆数据访问层
type MemoryArchiveRepository struct {
	db *gorm.DB
}

// NewMemoryArchiveRepository 创建归档仓库
func NewMemoryArchiveRepository(db *gorm.DB) *MemoryArchiveRepository {
	return &MemoryArchiveRepository{db: db}
}

// Create 归档一条记忆（内部 TTL 任务用，original_owner_id 在模型里）
func (r *MemoryArchiveRepository) Create(a *MemoryArchive) error {
	if err := r.db.Create(a).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 归档记忆 id=%s project=%s owner=%s", a.ID, a.OriginalProjectID, a.OriginalOwnerID)
	return nil
}

// CreateBatch 批量归档（内部 TTL 任务用）
func (r *MemoryArchiveRepository) CreateBatch(as []MemoryArchive) error {
	if len(as) == 0 {
		return nil
	}
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&as).Error
	}); err != nil {
		return err
	}
	log.Printf("[INFO][storage] 批量归档记忆 count=%d", len(as))
	return nil
}

// GetByID 查询归档记录
// ownerID != "" 时强制 original_owner_id 校验（越权返回 nil）
func (r *MemoryArchiveRepository) GetByID(id, ownerID string) (*MemoryArchive, error) {
	var a MemoryArchive
	q := r.db.Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("original_owner_id = ?", ownerID)
	}
	if err := q.First(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// ListRestorable 查询可恢复的归档记录（未恢复且未过期，按 owner 隔离）
func (r *MemoryArchiveRepository) ListRestorable(ownerID string) ([]MemoryArchive, error) {
	var as []MemoryArchive
	err := r.db.Where("original_owner_id = ? AND restored = ? AND restore_expires_at > ?", ownerID, false, time.Now().UTC()).
		Order("archived_at DESC").
		Find(&as).Error
	return as, err
}

// ListByProject 查询某项目的归档记录（含已恢复，用于归档列表页）
// ownerID != "" 时强制 original_owner_id 校验
func (r *MemoryArchiveRepository) ListByProject(projectID, ownerID string) ([]MemoryArchive, error) {
	var as []MemoryArchive
	q := r.db.Where("original_project_id = ?", projectID)
	if ownerID != "" {
		q = q.Where("original_owner_id = ?", ownerID)
	}
	err := q.Order("archived_at DESC").
		Find(&as).Error
	return as, err
}

// MarkRestored 标记已恢复
// ownerID != "" 时强制 original_owner_id 校验
func (r *MemoryArchiveRepository) MarkRestored(id, ownerID string) error {
	q := r.db.Model(&MemoryArchive{}).Where("id = ?", id)
	if ownerID != "" {
		q = q.Where("original_owner_id = ?", ownerID)
	}
	if err := q.Update("restored", true).Error; err != nil {
		return err
	}
	log.Printf("[INFO][storage] 标记归档已恢复 id=%s owner=%s", id, ownerID)
	return nil
}

// DeleteExpired 物理删除超期未恢复的归档记录（内部 TTL 任务，跨用户清理，不加 owner 过滤）
func (r *MemoryArchiveRepository) DeleteExpired(now time.Time) (int64, error) {
	result := r.db.Where("restore_expires_at < ? AND restored = ?", now, false).
		Delete(&MemoryArchive{})
	if result.Error != nil {
		log.Printf("[ERROR][storage] 物理删除过期归档失败 err=%v", result.Error)
		return 0, result.Error
	}
	log.Printf("[INFO][storage] 物理删除过期归档 count=%d", result.RowsAffected)
	return result.RowsAffected, nil
}

// ==================== CrossProjectGrant Repository ====================
// （按 grantee_owner_id 查询，天然隔离，无需改造）

// CrossProjectGrantRepository 跨项目授权数据访问层
type CrossProjectGrantRepository struct {
	db *gorm.DB
}

// NewCrossProjectGrantRepository 创建跨项目授权仓库
func NewCrossProjectGrantRepository(db *gorm.DB) *CrossProjectGrantRepository {
	return &CrossProjectGrantRepository{db: db}
}

// Create 创建授权记录
func (r *CrossProjectGrantRepository) Create(g *CrossProjectGrant) error {
	if g.GrantedAt.IsZero() {
		g.GrantedAt = time.Now().UTC()
	}
	return r.db.Create(g).Error
}

// GetActive 查询生效中的授权
func (r *CrossProjectGrantRepository) GetActive(granteeOwnerID, projectID string) (*CrossProjectGrant, error) {
	var g CrossProjectGrant
	if err := r.db.Where("grantee_owner_id = ? AND project_id = ? AND is_active = ?", granteeOwnerID, projectID, true).
		First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

// Revoke 撤销授权
// ownerID != "" 时校验 granter_owner_id（仅项目所有者可撤销）
func (r *CrossProjectGrantRepository) Revoke(id, granterOwnerID string) error {
	q := r.db.Model(&CrossProjectGrant{}).Where("id = ?", id)
	if granterOwnerID != "" {
		q = q.Where("granter_owner_id = ?", granterOwnerID)
	}
	return q.Updates(map[string]any{"is_active": false, "revoked_at": time.Now().UTC()}).Error
}

// ListByGrantee 查询某用户获得的所有授权
func (r *CrossProjectGrantRepository) ListByGrantee(granteeOwnerID string) ([]CrossProjectGrant, error) {
	var gs []CrossProjectGrant
	err := r.db.Where("grantee_owner_id = ?", granteeOwnerID).
		Order("granted_at DESC").
		Find(&gs).Error
	return gs, err
}

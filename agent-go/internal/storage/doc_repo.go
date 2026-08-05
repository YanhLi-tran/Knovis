package storage

import (
	"gorm.io/gorm"
)

// DocumentRepository RAG 文档仓储(全局共享,无 owner 隔离)
type DocumentRepository struct {
	db *gorm.DB
}

// NewDocumentRepository 创建文档仓储
func NewDocumentRepository(db *gorm.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

// Create 创建文档记录
func (r *DocumentRepository) Create(doc *Document) error {
	return r.db.Create(doc).Error
}

// GetByID 查询单个文档
func (r *DocumentRepository) GetByID(id uint) (*Document, error) {
	var doc Document
	if err := r.db.First(&doc, id).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

// List 文档列表(支持按 status / company_code 过滤)
func (r *DocumentRepository) List(status, companyCode string) ([]Document, error) {
	q := r.db.Model(&Document{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if companyCode != "" {
		q = q.Where("company_code = ?", companyCode)
	}
	var docs []Document
	err := q.Order("created_at DESC").Find(&docs).Error
	return docs, err
}

// Update 更新文档字段(仅非零字段)
func (r *DocumentRepository) Update(doc *Document) error {
	return r.db.Save(doc).Error
}

// UpdateStatus 更新状态(含错误信息)
func (r *DocumentRepository) UpdateStatus(id uint, status, errMsg string) error {
	return r.db.Model(&Document{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "error_msg": errMsg}).Error
}

// UpdateReady 摄入完成:更新为 ready + 分块数 + 页数
func (r *DocumentRepository) UpdateReady(id uint, totalChunks, totalPages int) error {
	return r.db.Model(&Document{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":       "ready",
			"total_chunks": totalChunks,
			"total_pages":  totalPages,
			"error_msg":    "",
		}).Error
}

// SoftDelete 软删文档(级联软删 chunks)
func (r *DocumentRepository) SoftDelete(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// 软删文档
		if err := tx.Where("id = ?", id).Delete(&Document{}).Error; err != nil {
			return err
		}
		// 物理删 chunks(chunks 无软删字段,直接删除)
		// 注:chunks 表无 DeletedAt,删除文档时级联硬删 chunks(向量由 doc-service 删 Chroma)
		if err := tx.Where("document_id = ?", id).Delete(&DocumentChunk{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// DocumentChunkRepository 文档分块仓储
type DocumentChunkRepository struct {
	db *gorm.DB
}

// NewDocumentChunkRepository 创建分块仓储
func NewDocumentChunkRepository(db *gorm.DB) *DocumentChunkRepository {
	return &DocumentChunkRepository{db: db}
}

// BatchCreate 批量创建分块
func (r *DocumentChunkRepository) BatchCreate(chunks []DocumentChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	return r.db.CreateInBatches(chunks, 100).Error
}

// ListByDocument 查询某文档全部分块(按 chunk_index 排序)
func (r *DocumentChunkRepository) ListByDocument(docID uint) ([]DocumentChunk, error) {
	var chunks []DocumentChunk
	err := r.db.Where("document_id = ?", docID).Order("chunk_index ASC").Find(&chunks).Error
	return chunks, err
}

// ListBySection 查询同 section_id 的全部分块(段落召回用,按 chunk_index 排序)
func (r *DocumentChunkRepository) ListBySection(sectionID string) ([]DocumentChunk, error) {
	var chunks []DocumentChunk
	err := r.db.Where("section_id = ?", sectionID).Order("chunk_index ASC").Find(&chunks).Error
	return chunks, err
}

// GetByID 查询单个分块
func (r *DocumentChunkRepository) GetByID(id uint) (*DocumentChunk, error) {
	var c DocumentChunk
	if err := r.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListByIDs 批量查询分块(按 id)
func (r *DocumentChunkRepository) ListByIDs(ids []uint) ([]DocumentChunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var chunks []DocumentChunk
	err := r.db.Where("id IN ?", ids).Find(&chunks).Error
	return chunks, err
}

// CountByDocument 统计文档分块数
func (r *DocumentChunkRepository) CountByDocument(docID uint) (int64, error) {
	var n int64
	err := r.db.Model(&DocumentChunk{}).Where("document_id = ?", docID).Count(&n).Error
	return n, err
}

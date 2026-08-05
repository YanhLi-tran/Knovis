package storage

import (
	"time"

	"gorm.io/gorm"
)

// Document RAG 文档元信息(全局共享,不按用户/项目隔离)
type Document struct {
	ID           uint           `gorm:"primaryKey;autoIncrement" json:"id"`
	Filename     string         `gorm:"type:varchar(255);not null;comment:原始文件名" json:"filename"`
	FilePath     string         `gorm:"type:varchar(512);not null;comment:存储路径" json:"file_path"`
	FileSize     int64          `gorm:"comment:字节" json:"file_size"`
	TotalPages   int            `gorm:"comment:总页数" json:"total_pages"`
	TotalChunks  int            `gorm:"default:0;comment:分块数" json:"total_chunks"`
	Status       string         `gorm:"type:enum('processing','ready','failed');default:'processing';index;comment:状态" json:"status"`
	ErrorMsg     string         `gorm:"type:text;comment:失败原因" json:"error_msg"`
	// 文件名自动解析的元数据(股票代码_年份_公司简称_全称.pdf)
	CompanyCode  string         `gorm:"type:varchar(20);comment:股票代码(如000858)" json:"company_code"`
	CompanyName  string         `gorm:"type:varchar(100);comment:公司名(如五粮液)" json:"company_name"`
	ReportYear   int            `gorm:"comment:年份(如2023)" json:"report_year"`
	ReportType   string         `gorm:"type:varchar(50);default:'年报';comment:报告类型" json:"report_type"`
	// 手动元数据(自动解析失败时填写),JSON
	ManualMeta   string         `gorm:"type:json;comment:手动元数据JSON" json:"manual_meta"`
	CreatedAt    time.Time      `gorm:"comment:创建时间" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"comment:更新时间" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index;comment:软删除时间" json:"deleted_at"`
}

// TableName 指定表名
func (Document) TableName() string { return "agent_documents" }

// DocumentChunk 文档分块(RAG 索引粒度,BM25 + 向量检索目标)
type DocumentChunk struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	DocumentID      uint      `gorm:"not null;index;comment:所属文档" json:"document_id"`
	ChunkIndex      int       `gorm:"not null;comment:块序号(文档内递增)" json:"chunk_index"`
	PageNum         int       `gorm:"index;comment:来源页码" json:"page_num"`
	HeadingPath     string    `gorm:"type:json;comment:标题路径JSON(如[\"三、财务信息\",\"(一)营业收入\"])" json:"heading_path"`
	SectionID       string    `gorm:"type:varchar(128);index;comment:heading_path的hash,段落召回用" json:"section_id"`
	Content         string    `gorm:"type:text;not null;comment:块文本" json:"content"`
	ContentLength   int       `gorm:"comment:字符数" json:"content_length"`
	ChunkType       string    `gorm:"type:enum('text','table','heading');default:'text';comment:块类型" json:"chunk_type"`
	EmbeddingStatus string    `gorm:"type:varchar(16);default:'pending';comment:向量状态(pending/done/failed)" json:"embedding_status"`
	CreatedAt       time.Time `gorm:"comment:创建时间" json:"created_at"`
}

// TableName 指定表名
func (DocumentChunk) TableName() string { return "agent_document_chunks" }

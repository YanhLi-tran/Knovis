package storage

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DBConfig MySQL 数据库配置
type DBConfig struct {
	Host         string
	Port         string
	User         string
	Password     string
	DBName       string
	MaxOpenConns int
	MaxIdleConns int
	MaxLifetime  time.Duration // 连接最大存活时间
}

// LoadDBConfig 从环境变量加载配置
func LoadDBConfig() *DBConfig {
	return &DBConfig{
		Host:         getEnv("DB_HOST", "127.0.0.1"),
		Port:         getEnv("DB_PORT", "3306"),
		User:         getEnv("DB_USER", "root"),
		Password:     getEnv("DB_PASSWORD", ""),
		DBName:       getEnv("DB_NAME", "agent_go"),
		MaxOpenConns: 50,
		MaxIdleConns: 10,
		MaxLifetime:  30 * time.Minute,
	}
}

// InitDB 初始化 MySQL 连接 + 自动建表
func InitDB(cfg *DBConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC&timeout=10s&readTimeout=30s&writeTimeout=30s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.DBName)

	// loc=UTC：数据库时间用 UTC，应用层做时区转换
	gormCfg := &gorm.Config{
		Logger:      logger.Default.LogMode(logger.Warn),
		PrepareStmt: true, // 预编译语句缓存
	}

	db, err := gorm.Open(mysql.Open(dsn), gormCfg)
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	// 连接池配置
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.MaxLifetime)

	// 自动建表（开发期，后续切 golang-migrate）
	// P2: 扩展记忆系统相关表；P3: User 表（JWT 鉴权）
	// 注意:Document/DocumentChunk 表由 docker-init.sql 创建,GORM AutoMigrate 会尝试
	// ALTER TABLE 修改列类型(即使类型已匹配),导致 30s 超时。已从 AutoMigrate 移除。
	if err := db.AutoMigrate(
		&User{},
		&Session{}, &Message{},
		&UserConfig{}, &Project{}, &Memory{}, &MemoryArchive{}, &CrossProjectGrant{},
		&AuditLog{},
		// &Document{}, &DocumentChunk{}, // P5: RAG 文档表(由 docker-init.sql 管理,不自动迁移)
		&UserSkill{}, // P7: 用户上传的私有 Skill
	); err != nil {
		return nil, fmt.Errorf("自动建表失败: %w", err)
	}

	// AutoMigrate 不管理 FULLTEXT 索引和复合索引，单独执行（幂等）
	// 1) agent_memories.content FULLTEXT 索引（BM25 检索基础，InnoDB 支持 ngram 解析中文）
	// 2) agent_messages (session_id, round) 复合索引（历史加载性能，project_memory 记录的优化项）
	if err := applyPostMigrateIndexes(db); err != nil {
		log.Printf("[storage] 应用后置索引失败（可忽略，可能已存在）: %v", err)
	}

	// 自动硬删除超过保留期的软删除记录（启动时执行一次）
	if err := cleanupSoftDeleted(db); err != nil {
		log.Printf("[storage] 清理软删除记录失败: %v", err)
	}

	log.Printf("[storage] MySQL 已连接: %s:%s/%s", cfg.Host, cfg.Port, cfg.DBName)
	return db, nil
}

// applyPostMigrateIndexes 应用 AutoMigrate 不管理的索引（FULLTEXT + 复合索引）
// 幂等：使用 IF NOT EXISTS 或忽略 "Duplicate key name" 错误
func applyPostMigrateIndexes(db *gorm.DB) error {
	statements := []string{
		// FULLTEXT 索引 + ngram 解析器（中文分词），用于 BM25 检索
		`ALTER TABLE agent_memories ADD FULLTEXT INDEX ft_memories_content (content) WITH PARSER ngram`,
		// 历史加载复合索引（session_id + round），project_memory 记录的性能优化项
		`CREATE INDEX idx_agent_messages_session_round ON agent_messages (session_id, round)`,
		// P5: RAG 文档分块 FULLTEXT 索引（BM25 检索基础）
		`ALTER TABLE agent_document_chunks ADD FULLTEXT INDEX ft_content (content) WITH PARSER ngram`,
	}
	for _, sql := range statements {
		if err := db.Exec(sql).Error; err != nil {
			// 忽略"索引已存在"错误（幂等），其余向上抛
			errMsg := err.Error()
			if strings.Contains(errMsg, "Duplicate key name") || strings.Contains(errMsg, "Duplicate key") || strings.Contains(errMsg, "already exists") {
				continue
			}
			return fmt.Errorf("执行 [%s] 失败: %w", sql, err)
		}
	}
	return nil
}

// cleanupSoftDeleted 硬删除超过保留期的软删除记录
// 保留期由环境变量 SESSION_SOFT_DELETE_RETAIN_DAYS 控制，默认 7 天
func cleanupSoftDeleted(db *gorm.DB) error {
	retainDays := 7
	if d := getEnv("SESSION_SOFT_DELETE_RETAIN_DAYS", ""); d != "" {
		if n := atoiSafe(d); n > 0 {
			retainDays = n
		}
	}
	threshold := time.Now().UTC().AddDate(0, 0, -retainDays)

	// 使用 Unscoped 永久删除软删除记录
	result := db.Unscoped().Where("deleted_at IS NOT NULL AND deleted_at < ?", threshold).Delete(&Session{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		log.Printf("[storage] 已清理 %d 条过期软删除 Session", result.RowsAffected)
	}

	result = db.Unscoped().Where("deleted_at IS NOT NULL AND deleted_at < ?", threshold).Delete(&Message{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		log.Printf("[storage] 已清理 %d 条过期软删除 Message", result.RowsAffected)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

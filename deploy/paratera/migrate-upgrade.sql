-- =============================================================================
-- Knovis @ 并行智算云 - 增量数据库迁移脚本（幂等，可重复执行）
-- -----------------------------------------------------------------------------
-- 用途: 旧库升级。sql/docker-init.sql 只在新库(表数<5)时导入，已有旧库不会
--       补齐后续新增的对象。本脚本补充:
--         1) memory_search_metrics 表（记忆检索监控指标）
--         2) agent_memories 表 5 个新字段（tier / effective_importance /
--            merged_from / merged_at / last_decayed_at，记忆生命周期功能）
--         3) agent_memories 表 2 个新索引（idx_tier_project / idx_effective_importance）
-- 兼容: MySQL 5.7 / 8.0。MySQL 不支持 ADD COLUMN IF NOT EXISTS（MariaDB 语法），
--       故用 information_schema 判断 + PREPARE/EXECUTE 动态 SQL 实现幂等。
-- 接入: deploy/paratera/start.sh 每次启动在 docker-init.sql 导入后无条件执行。
-- =============================================================================

USE `agent_go`;

-- 客户端连接使用 utf8mb4（存储过程参数传递中文 COMMENT 必需，否则 CALL 报 Incorrect string value）
SET NAMES utf8mb4;

-- 1) 检索指标聚合表（记忆检索在线监控，按分钟聚合）
CREATE TABLE IF NOT EXISTS `memory_search_metrics` (
  `id` BIGINT AUTO_INCREMENT PRIMARY KEY,
  `project_id` VARCHAR(64) NOT NULL COMMENT '项目ID',
  `bucket_minute` DATETIME NOT NULL COMMENT '聚合到分钟',
  `request_count` INT NOT NULL DEFAULT 0,
  `avg_total_ms` FLOAT DEFAULT 0,
  `p50_total_ms` FLOAT DEFAULT 0,
  `p95_total_ms` FLOAT DEFAULT 0,
  `avg_bm25_count` FLOAT DEFAULT 0,
  `avg_rag_count` FLOAT DEFAULT 0,
  `cache_hit_rate` FLOAT DEFAULT 0,
  `keyword_deweight_rate` FLOAT DEFAULT 0,
  `rag_fail_count` INT DEFAULT 0,
  INDEX `idx_project_minute` (`project_id`, `bucket_minute`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='记忆检索监控指标';

-- 2) 幂等加列辅助过程：字段不存在且表存在时才 ALTER
DROP PROCEDURE IF EXISTS `knovis_migrate_add_column`;
DELIMITER $$
CREATE PROCEDURE `knovis_migrate_add_column`(IN p_table VARCHAR(64), IN p_column VARCHAR(64), IN p_ddl TEXT)
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = p_table
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = p_table AND column_name = p_column
  ) THEN
    SET @sql = CONCAT('ALTER TABLE `', p_table, '` ADD COLUMN ', p_ddl);
    PREPARE stmt FROM @sql;
    EXECUTE stmt;
    DEALLOCATE PREPARE stmt;
  END IF;
END$$
DELIMITER ;

-- 3) 幂等加索引辅助过程：索引不存在且表存在时才 ALTER
DROP PROCEDURE IF EXISTS `knovis_migrate_add_index`;
DELIMITER $$
CREATE PROCEDURE `knovis_migrate_add_index`(IN p_table VARCHAR(64), IN p_index VARCHAR(64), IN p_ddl TEXT)
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = DATABASE() AND table_name = p_table
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.statistics
    WHERE table_schema = DATABASE() AND table_name = p_table AND index_name = p_index
  ) THEN
    SET @sql = CONCAT('ALTER TABLE `', p_table, '` ADD ', p_ddl);
    PREPARE stmt FROM @sql;
    EXECUTE stmt;
    DEALLOCATE PREPARE stmt;
  END IF;
END$$
DELIMITER ;

-- 4) agent_memories 5 个新字段（定义与 sql/docker-init.sql 保持一致）
CALL knovis_migrate_add_column('agent_memories', 'effective_importance',
  '`effective_importance` INT NOT NULL DEFAULT 0 COMMENT ''衰减后的有效重要度(初始=importance,随时间衰减)''');
CALL knovis_migrate_add_column('agent_memories', 'tier',
  '`tier` ENUM(''hot'',''cold'') NOT NULL DEFAULT ''hot'' COMMENT ''冷热分层:hot=在Chroma可检索 cold=仅MySQL不在Chroma''');
CALL knovis_migrate_add_column('agent_memories', 'merged_from',
  '`merged_from` JSON DEFAULT NULL COMMENT ''合并来源:被合并的原记忆ID数组(仅summary类型合并记忆有值)''');
CALL knovis_migrate_add_column('agent_memories', 'merged_at',
  '`merged_at` DATETIME DEFAULT NULL COMMENT ''合并时间(被合并的原记忆标记此字段表示已合并软删除)''');
CALL knovis_migrate_add_column('agent_memories', 'last_decayed_at',
  '`last_decayed_at` DATETIME DEFAULT NULL COMMENT ''上次衰减计算时间''');

-- 5) agent_memories 2 个新索引
CALL knovis_migrate_add_index('agent_memories', 'idx_tier_project',
  'INDEX `idx_tier_project` (`tier`, `project_id`)');
CALL knovis_migrate_add_index('agent_memories', 'idx_effective_importance',
  'INDEX `idx_effective_importance` (`effective_importance`)');

-- 5.1) agent_user_config 用户级上下文大小（P10，用户可自定义；0=项目级/默认64000，上限1M）
CALL knovis_migrate_add_column('agent_user_config', 'max_context_length',
  '`max_context_length` INT NOT NULL DEFAULT 0 COMMENT ''用户自定义上下文大小 token数（0=用项目级/默认64000，范围1000-1048576）''');

-- 6) 清理辅助过程（脚本可重复执行）
DROP PROCEDURE IF EXISTS `knovis_migrate_add_column`;
DROP PROCEDURE IF EXISTS `knovis_migrate_add_index`;

-- Agent Go 数据库 Schema
-- 由 GORM AutoMigrate 自动生成，此文件用于备份和参考
-- DB: agent_go
-- 字符集: utf8mb4

-- 会话表
CREATE TABLE IF NOT EXISTS `agent_sessions` (
  `id` VARCHAR(36) NOT NULL PRIMARY KEY COMMENT 'Session UUID',
  `owner_id` VARCHAR(64) DEFAULT NULL COMMENT '归属用户ID（预留，当前为client_id）',
  `project_id` VARCHAR(36) DEFAULT NULL COMMENT '所属项目ID（可空，空=无项目）',
  `title` VARCHAR(128) DEFAULT '新对话' COMMENT 'Session 标题',
  `summary` TEXT COMMENT '历史摘要（滑动窗口外内容）',
  `pinned` BOOLEAN DEFAULT FALSE COMMENT '是否置顶',
  `last_active_at` DATETIME(3) DEFAULT NULL COMMENT '最后活跃时间',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  INDEX `idx_agent_sessions_owner_id` (`owner_id`),
  INDEX `idx_agent_sessions_project_id` (`project_id`),
  INDEX `idx_agent_sessions_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='对话会话';

-- 消息表（全量 OTACO 过程）
CREATE TABLE IF NOT EXISTS `agent_messages` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `session_id` VARCHAR(36) DEFAULT NULL COMMENT '所属Session',
  `round` INT DEFAULT NULL COMMENT 'OTACO 轮次（从1开始）',
  `role` VARCHAR(16) DEFAULT NULL COMMENT '角色(system/user/assistant/tool)',
  `stage` VARCHAR(16) DEFAULT NULL COMMENT 'OTACO阶段(observe/think/act/check/output)',
  `content` LONGTEXT COMMENT '消息内容',
  `tool_call_id` VARCHAR(64) DEFAULT NULL COMMENT '工具调用ID',
  `tool_calls` TEXT COMMENT 'assistant发起的工具调用JSON',
  `decision` VARCHAR(16) DEFAULT NULL COMMENT 'Observation决策(pass/retry/rollback)',
  `reason` VARCHAR(512) DEFAULT NULL COMMENT '决策理由',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  INDEX `idx_agent_messages_session_id` (`session_id`),
  INDEX `idx_agent_messages_tool_call_id` (`tool_call_id`),
  INDEX `idx_agent_messages_deleted_at` (`deleted_at`),
  INDEX `idx_agent_messages_session_round` (`session_id`, `round`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='对话消息（全量 OTACO 过程）';

-- ===== P2: 记忆系统相关表 =====

-- 用户档案表（全局记忆：基础信息 + 位置 + 偏好）
CREATE TABLE IF NOT EXISTS `agent_user_config` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID（当前为client_id）',
  `basic_info` TEXT COMMENT '基础信息JSON（姓名/职业/年龄等）',
  `location` VARCHAR(128) DEFAULT NULL COMMENT '用户位置（如 Asia/Shanghai）',
  `preferences` TEXT COMMENT '偏好JSON（语言/风格/UI等）',
  `raw_text` TEXT COMMENT '拼好的档案文本（便于直接注入）',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  UNIQUE INDEX `idx_agent_user_config_user_id` (`user_id`),
  INDEX `idx_agent_user_config_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户档案';

-- 项目表（顶层 session，项目下可创建子 session）
CREATE TABLE IF NOT EXISTS `agent_projects` (
  `id` VARCHAR(36) NOT NULL PRIMARY KEY COMMENT 'Project UUID',
  `owner_id` VARCHAR(64) DEFAULT NULL COMMENT '归属用户ID',
  `name` VARCHAR(128) DEFAULT NULL COMMENT '项目名称',
  `description` TEXT COMMENT '项目元信息/描述',
  `rules` TEXT COMMENT '项目规则（注入system prompt）',
  `context` TEXT COMMENT '项目上下文（背景/目标等）',
  `key_points` TEXT COMMENT '记忆要点（LLM自动提取累积）',
  `user_defined` TEXT COMMENT '用户自定义备注',
  `is_archived` BOOLEAN DEFAULT FALSE COMMENT '是否归档',
  `last_active_at` DATETIME(3) DEFAULT NULL COMMENT '最后活跃时间',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  INDEX `idx_agent_projects_owner_id` (`owner_id`),
  INDEX `idx_agent_projects_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='项目';

-- 记忆表（混合检索目标：BM25全文 + Chroma向量）
CREATE TABLE IF NOT EXISTS `agent_memories` (
  `id` VARCHAR(36) NOT NULL PRIMARY KEY COMMENT 'Memory UUID（同时作为Chroma doc id）',
  `project_id` VARCHAR(36) DEFAULT NULL COMMENT '所属项目',
  `owner_id` VARCHAR(64) DEFAULT NULL COMMENT '归属用户ID',
  `content` TEXT COMMENT '记忆内容',
  `memory_type` VARCHAR(32) DEFAULT NULL COMMENT '类型(fact/preference/event/summary/requirement等)',
  `source` VARCHAR(32) DEFAULT 'auto_extract' COMMENT '来源(auto_extract/manual/cross_project)',
  `source_session_id` VARCHAR(36) DEFAULT NULL COMMENT '来源Session（自动提取时记录）',
  `source_round` INT DEFAULT NULL COMMENT '来源轮次',
  `importance` INT DEFAULT 50 COMMENT '重要度0-100',
  `embedding_status` VARCHAR(16) DEFAULT 'pending' COMMENT '向量状态(pending/done/failed)',
  `last_accessed_at` DATETIME(3) DEFAULT NULL COMMENT '最后访问时间（LRU用）',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  INDEX `idx_agent_memories_project_id` (`project_id`),
  INDEX `idx_agent_memories_owner_id` (`owner_id`),
  INDEX `idx_agent_memories_embedding_status` (`embedding_status`),
  INDEX `idx_agent_memories_deleted_at` (`deleted_at`),
  FULLTEXT INDEX `ft_memories_content` (`content`) WITH PARSER ngram
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='记忆条目';

-- 归档记忆表（TTL 14周后迁移，30天内可恢复）
CREATE TABLE IF NOT EXISTS `agent_memory_archive` (
  `id` VARCHAR(36) NOT NULL PRIMARY KEY COMMENT '原Memory UUID',
  `original_project_id` VARCHAR(36) DEFAULT NULL COMMENT '原所属项目',
  `original_owner_id` VARCHAR(64) DEFAULT NULL COMMENT '原归属用户ID',
  `content` TEXT COMMENT '记忆内容',
  `memory_type` VARCHAR(32) DEFAULT NULL COMMENT '类型',
  `source` VARCHAR(32) DEFAULT NULL COMMENT '来源',
  `archived_at` DATETIME(3) DEFAULT NULL COMMENT '归档时间',
  `restore_expires_at` DATETIME(3) DEFAULT NULL COMMENT '可恢复截止时间（archived_at+30天）',
  `restored` BOOLEAN DEFAULT FALSE COMMENT '是否已恢复',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '原创建时间',
  INDEX `idx_agent_memory_archive_original_owner_id` (`original_owner_id`),
  INDEX `idx_agent_memory_archive_original_project_id` (`original_project_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='归档记忆';

-- 跨项目读取授权表
CREATE TABLE IF NOT EXISTS `agent_cross_project_grants` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `granter_owner_id` VARCHAR(64) DEFAULT NULL COMMENT '被读取项目所属用户ID',
  `grantee_owner_id` VARCHAR(64) DEFAULT NULL COMMENT '发起读取的当前用户ID',
  `project_id` VARCHAR(36) DEFAULT NULL COMMENT '被授权访问的项目',
  `is_active` BOOLEAN DEFAULT TRUE COMMENT '授权是否生效',
  `granted_at` DATETIME(3) DEFAULT NULL COMMENT '授权时间',
  `revoked_at` DATETIME(3) DEFAULT NULL COMMENT '撤销时间',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  INDEX `idx_agent_cross_project_grants_granter_owner_id` (`granter_owner_id`),
  INDEX `idx_agent_cross_project_grants_grantee_owner_id` (`grantee_owner_id`),
  INDEX `idx_agent_cross_project_grants_project_id` (`project_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='跨项目读取授权';

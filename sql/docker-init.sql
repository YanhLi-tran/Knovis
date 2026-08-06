-- Docker 初始化脚本：首次启动 MySQL 容器时自动执行
-- 创建 agent_go 和 knovis 两个库，并导入表结构
-- 来源：agent-go/configs/schema.sql + sql/schema.sql

-- ===== 库 1: agent_go（Agent 私有表）=====
CREATE DATABASE IF NOT EXISTS `agent_go` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `agent_go`;

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

-- 用户凭证表（SSO 形态：仅存 Knovis token 等，不自管密码）
CREATE TABLE IF NOT EXISTS `agent_user_credentials` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `user_id` VARCHAR(64) NOT NULL COMMENT '用户ID（Knovis userId）',
  `llm_api_key_enc` TEXT COMMENT 'AES-256-GCM 加密的 LLM API Key',
  `llm_base_url` VARCHAR(255) DEFAULT NULL COMMENT 'LLM BaseURL（用户自定义）',
  `llm_model` VARCHAR(128) DEFAULT NULL COMMENT 'LLM 模型名（用户自定义）',
  `knovis_token_enc` TEXT COMMENT 'AES-256-GCM 加密的 Knovis token',
  `using_own_key` BOOLEAN DEFAULT FALSE COMMENT '是否使用自带 key（true=不限流）',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  UNIQUE INDEX `idx_agent_user_credentials_user_id` (`user_id`),
  INDEX `idx_agent_user_credentials_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='用户凭证（SSO 形态）';

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

-- 文档表（PDF 元信息）
CREATE TABLE IF NOT EXISTS `agent_documents` (
  `id` INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `doc_name` VARCHAR(500) NOT NULL COMMENT '文档名称',
  `doc_type` VARCHAR(20) DEFAULT 'pdf' COMMENT '文档类型',
  `total_pages` INT DEFAULT 0 COMMENT '总页数',
  `total_chunks` INT DEFAULT 0 COMMENT '总分块数',
  `status` VARCHAR(20) DEFAULT 'pending' COMMENT '处理状态',
  `meta` TEXT COMMENT '元信息JSON',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` DATETIME(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` DATETIME(3) DEFAULT NULL COMMENT '软删除时间',
  INDEX `idx_agent_documents_status` (`status`),
  INDEX `idx_agent_documents_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='文档元信息';

-- 文档分块表（BM25 全文检索 + 段落召回）
CREATE TABLE IF NOT EXISTS `agent_document_chunks` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `document_id` INT UNSIGNED NOT NULL COMMENT '所属文档ID',
  `chunk_index` INT NOT NULL COMMENT '分块序号',
  `page_num` INT DEFAULT 0 COMMENT '页码',
  `heading_path` VARCHAR(1000) DEFAULT NULL COMMENT '标题层级路径JSON',
  `section_id` VARCHAR(200) DEFAULT NULL COMMENT '小节ID',
  `content` MEDIUMTEXT COMMENT '分块内容',
  `content_length` INT DEFAULT 0 COMMENT '内容长度',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  INDEX `idx_agent_document_chunks_document_id` (`document_id`),
  INDEX `idx_agent_document_chunks_section_id` (`section_id`),
  FULLTEXT INDEX `ft_document_chunks_content` (`content`) WITH PARSER ngram
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='文档分块';

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

-- 审计日志表
CREATE TABLE IF NOT EXISTS `agent_audit_logs` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `user_id` VARCHAR(64) DEFAULT NULL COMMENT '操作用户ID',
  `action` VARCHAR(64) NOT NULL COMMENT '操作类型(create/update/delete/login/logout等)',
  `resource_type` VARCHAR(64) DEFAULT NULL COMMENT '资源类型',
  `resource_id` VARCHAR(128) DEFAULT NULL COMMENT '资源ID',
  `detail` TEXT COMMENT '操作详情JSON',
  `ip` VARCHAR(64) DEFAULT NULL COMMENT '来源IP',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '创建时间',
  INDEX `idx_agent_audit_logs_user_id` (`user_id`),
  INDEX `idx_agent_audit_logs_action` (`action`),
  INDEX `idx_agent_audit_logs_created_at` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='审计日志';

-- token 黑名单（登出后 JWT 失效）
CREATE TABLE IF NOT EXISTS `agent_token_blacklist` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `token` VARCHAR(512) NOT NULL COMMENT 'JWT token',
  `user_id` VARCHAR(64) DEFAULT NULL COMMENT '用户ID',
  `expired_at` DATETIME(3) DEFAULT NULL COMMENT 'token 原过期时间',
  `created_at` DATETIME(3) DEFAULT NULL COMMENT '拉黑时间',
  INDEX `idx_agent_token_blacklist_token` (`token`(255)),
  INDEX `idx_agent_token_blacklist_expired_at` (`expired_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='JWT 黑名单';

-- ===== 库 2: knovis（用户 + 动态数据）=====
CREATE DATABASE IF NOT EXISTS `knovis` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE `knovis`;

CREATE TABLE IF NOT EXISTS `knovis_user` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `name`              varchar(100)  NOT NULL COMMENT '用户名',
  `email`             varchar(100)  NOT NULL COMMENT '邮箱',
  `password`          varchar(255)  NOT NULL COMMENT '密码(bcrypt)',
  `avatar`            varchar(500)  NOT NULL DEFAULT '' COMMENT '头像地址',
  `bio`               varchar(200)  NOT NULL DEFAULT '用户还没有在此留下足迹哦~' COMMENT '个人简介',
  `email_visible`     tinyint(1)    NOT NULL DEFAULT 0 COMMENT '邮箱是否公开',
  `likes_visible`     tinyint(1)    NOT NULL DEFAULT 0 COMMENT '点赞列表是否公开',
  `favorites_visible` tinyint(1)    NOT NULL DEFAULT 0 COMMENT '收藏列表是否公开',
  `follow_visible`    tinyint(1)    NOT NULL DEFAULT 1 COMMENT '关注列表是否公开',
  `status`            tinyint       NOT NULL DEFAULT 1 COMMENT '1=正常 0=已注销',
  `created_at`        datetime      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_email` (`email`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '用户表';

CREATE TABLE IF NOT EXISTS `knovis_post` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT,
  `user_id`         bigint unsigned NOT NULL COMMENT '作者ID',
  `type`            varchar(20)   NOT NULL COMMENT 'text/image/video',
  `content`         varchar(1000) NOT NULL DEFAULT '' COMMENT '文字内容',
  `media_url`       varchar(500)  NOT NULL DEFAULT '' COMMENT '单图地址(兼容)',
  `media_urls`      text          NULL COMMENT '多图地址 JSON 数组',
  `video_url`       varchar(500)  NOT NULL DEFAULT '' COMMENT '视频地址',
  `video_duration`  int           NOT NULL DEFAULT 0 COMMENT '视频时长(秒)',
  `video_thumbnail` varchar(500)  NOT NULL DEFAULT '' COMMENT '视频封面',
  `views`           int           NOT NULL DEFAULT 0 COMMENT '浏览数',
  `likes`           int           NOT NULL DEFAULT 0 COMMENT '点赞数',
  `comments`        int           NOT NULL DEFAULT 0 COMMENT '评论数',
  `favorites`       int           NOT NULL DEFAULT 0 COMMENT '收藏数',
  `show_likes`      tinyint(1)    NOT NULL DEFAULT 1 COMMENT '是否公开点赞列表',
  `show_favorites`  tinyint(1)    NOT NULL DEFAULT 1 COMMENT '是否公开收藏列表',
  `created_at`      datetime      NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`      datetime      NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_created_at` (`created_at`)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COMMENT = '动态表';

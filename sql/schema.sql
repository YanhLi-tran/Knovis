-- Knovis 建表 SQL（MySQL 8.x）
-- 数据库: knovis（建库: CREATE DATABASE IF NOT EXISTS knovis DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;）

CREATE TABLE IF NOT EXISTS `user` (
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

CREATE TABLE IF NOT EXISTS `post` (
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

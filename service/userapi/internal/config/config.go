package config

import (
	"fmt"

	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/rest"
)

type Config struct {
	rest.RestConf

	MySQL struct {
		Host     string
		Port     int
		User     string
		Password string
		Database string
	}

	Redis redis.RedisConf

	Auth struct {
		AccessSecret string // JWT HS256 签名密钥
		AccessExpire int64  // token 有效期(秒)
		Issuer       string
		Audience     string
	}

	SMTP struct {
		Host     string
		Port     int
		Username string
		Password string
	}

	UploadDir string // 图片/视频文件存储目录
}

// DSN 返回 MySQL 连接串
func (c Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		c.MySQL.User, c.MySQL.Password, c.MySQL.Host, c.MySQL.Port, c.MySQL.Database)
}

package svc

import (
	"knovis/service/userapi/internal/config"
	"knovis/service/userapi/internal/model"

	"github.com/zeromicro/go-zero/core/stores/redis"
	"github.com/zeromicro/go-zero/core/stores/sqlx"
)

type ServiceContext struct {
	Config    config.Config
	UserModel model.UserModel
	PostModel model.PostModel
	Redis     *redis.Redis
}

func NewServiceContext(c config.Config) *ServiceContext {
	conn := sqlx.NewMysql(c.DSN())
	return &ServiceContext{
		Config:    c,
		UserModel: model.NewUserModel(conn),
		PostModel: model.NewPostModel(conn),
		Redis:     redis.MustNewRedis(c.Redis),
	}
}

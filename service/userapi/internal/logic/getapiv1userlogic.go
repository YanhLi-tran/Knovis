package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetApiV1UserLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetApiV1UserLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetApiV1UserLogic {
	return &GetApiV1UserLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetApiV1User 查询单个用户信息(供 Agent 的 /auth/me 透传)
func (l *GetApiV1UserLogic) GetApiV1User(req *types.GetUserReq) (resp *types.UserInfo, err error) {
	return NewGetUserLogic(l.ctx, l.svcCtx).GetUser(req)
}

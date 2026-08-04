package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetApiV1PostLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetApiV1PostLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetApiV1PostLogic {
	return &GetApiV1PostLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetApiV1Post 动态详情(对外契约, 供 Agent 只读调用)
func (l *GetApiV1PostLogic) GetApiV1Post(req *types.GetPostReq) (resp *types.PostInfo, err error) {
	return NewGetPostLogic(l.ctx, l.svcCtx).GetPost(req)
}

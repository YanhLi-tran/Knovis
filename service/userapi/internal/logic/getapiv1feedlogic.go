package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetApiV1FeedLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetApiV1FeedLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetApiV1FeedLogic {
	return &GetApiV1FeedLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetApiV1Feed 动态流(对外契约, 供 Agent 只读调用)
func (l *GetApiV1FeedLogic) GetApiV1Feed(req *types.PostListReq) (resp *types.PostListResp, err error) {
	return NewGetPostsLogic(l.ctx, l.svcCtx).GetPosts(req)
}

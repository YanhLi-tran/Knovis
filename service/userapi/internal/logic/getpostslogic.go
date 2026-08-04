package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetPostsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetPostsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetPostsLogic {
	return &GetPostsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetPosts 动态列表(广场, 分页)
func (l *GetPostsLogic) GetPosts(req *types.PostListReq) (resp *types.PostListResp, err error) {
	page, pageSize := normalizePage(req.Page, req.PageSize)
	offset := int64((page - 1) * pageSize)

	list, total, err := l.svcCtx.PostModel.FindPage(l.ctx, offset, int64(pageSize))
	if err != nil {
		return nil, err
	}

	posts := make([]types.PostInfo, 0, len(list))
	for i := range list {
		posts = append(posts, *buildPostInfo(&list[i], userNameByID(l.ctx, l.svcCtx, list[i].UserId)))
	}

	return &types.PostListResp{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		List:     posts,
	}, nil
}

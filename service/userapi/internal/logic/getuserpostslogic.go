package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUserPostsLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUserPostsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUserPostsLogic {
	return &GetUserPostsLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetUserPosts 用户动态列表(分页)
func (l *GetUserPostsLogic) GetUserPosts(req *types.UserPostsReq) (resp *types.PostListResp, err error) {
	page, pageSize := normalizePage(req.Page, req.PageSize)
	offset := int64((page - 1) * pageSize)

	list, total, err := l.svcCtx.PostModel.FindPageByUserID(l.ctx, uint64(req.ID), offset, int64(pageSize))
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

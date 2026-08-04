package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetPostLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetPostLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetPostLogic {
	return &GetPostLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetPost 动态详情(浏览数+1)
func (l *GetPostLogic) GetPost(req *types.GetPostReq) (resp *types.PostInfo, err error) {
	post, err := l.svcCtx.PostModel.FindOne(l.ctx, uint64(req.ID))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "动态不存在")
		}
		return nil, err
	}

	if err := l.svcCtx.PostModel.IncViews(l.ctx, post.Id); err != nil {
		l.Logger.Errorf("浏览量自增失败 post_id=%d: %v", post.Id, err)
	}

	post.Views++
	return buildPostInfo(post, userNameByID(l.ctx, l.svcCtx, post.UserId)), nil
}

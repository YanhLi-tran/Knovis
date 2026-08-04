package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type UpdatePostLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdatePostLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdatePostLogic {
	return &UpdatePostLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// UpdatePost 更新动态设置(ShowLikes/ShowFavorites, 仅作者)
func (l *UpdatePostLogic) UpdatePost(req *types.UpdatePostReq) (resp *types.UpdatePostResp, err error) {
	uid, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}

	post, err := l.svcCtx.PostModel.FindOne(l.ctx, uint64(req.ID))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "动态不存在")
		}
		return nil, err
	}

	if post.UserId != uint64(uid) {
		return nil, bizErr(http.StatusForbidden, "无权修改他人的动态")
	}

	if err := l.svcCtx.PostModel.UpdateSettings(l.ctx, post.Id, boolToInt(req.ShowLikes), boolToInt(req.ShowFavorites)); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "更新失败")
	}

	return &types.UpdatePostResp{Message: "更新成功"}, nil
}

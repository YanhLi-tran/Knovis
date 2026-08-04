package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type UpdateUserLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateUserLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateUserLogic {
	return &UpdateUserLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// UpdateUser 更新用户信息(仅本人)
func (l *UpdateUserLogic) UpdateUser(req *types.UpdateUserReq) (resp *types.UpdateUserResp, err error) {
	cur, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}
	if cur != req.ID {
		return nil, bizErr(http.StatusForbidden, "无权修改他人信息")
	}

	if _, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(req.ID)); err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "用户不存在")
		}
		return nil, err
	}

	err = l.svcCtx.UserModel.UpdateFields(l.ctx, uint64(req.ID), map[string]interface{}{
		"name":              req.Name,
		"avatar":            req.Avatar,
		"bio":               req.Bio,
		"email_visible":     boolToInt(req.EmailVisible),
		"likes_visible":     boolToInt(req.LikesVisible),
		"favorites_visible": boolToInt(req.FavoritesVisible),
		"follow_visible":    boolToInt(req.FollowVisible),
	})
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "更新失败: "+err.Error())
	}

	return &types.UpdateUserResp{Message: "更新成功"}, nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

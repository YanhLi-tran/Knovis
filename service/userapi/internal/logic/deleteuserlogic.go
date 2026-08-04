package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/crypto"
	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type DeleteUserLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeleteUserLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeleteUserLogic {
	return &DeleteUserLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// DeleteUser 删除用户(仅本人; 传 password 时校验密码防误删)
func (l *DeleteUserLogic) DeleteUser(req *types.DeleteUserReq) (resp *types.DeleteUserResp, err error) {
	cur, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}
	if cur != req.ID {
		return nil, bizErr(http.StatusForbidden, "无权删除他人账户")
	}

	user, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(req.ID))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "用户不存在")
		}
		return nil, err
	}

	if req.Password != "" && !crypto.CheckPassword(req.Password, user.Password) {
		return nil, bizErr(http.StatusUnauthorized, "密码错误，无法删除")
	}

	if err := l.svcCtx.UserModel.Delete(l.ctx, user.Id); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "删除失败: "+err.Error())
	}

	return &types.DeleteUserResp{Message: "成功删除"}, nil
}

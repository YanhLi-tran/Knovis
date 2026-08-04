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

type UpdatePasswordLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdatePasswordLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdatePasswordLogic {
	return &UpdatePasswordLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// UpdatePassword 修改密码
func (l *UpdatePasswordLogic) UpdatePassword(req *types.UpdatePasswordReq) (resp *types.UpdatePasswordResp, err error) {
	uid, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}

	user, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(uid))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "用户不存在")
		}
		return nil, err
	}

	if !crypto.CheckPassword(req.OldPassword, user.Password) {
		return nil, bizErr(http.StatusUnauthorized, "原密码错误")
	}

	if len(req.NewPassword) < 6 {
		return nil, bizErr(http.StatusBadRequest, "新密码至少需要6个字符")
	}

	hashed, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "密码加密失败")
	}

	if err := l.svcCtx.UserModel.UpdateFields(l.ctx, user.Id, map[string]interface{}{"password": hashed}); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "修改密码失败")
	}

	return &types.UpdatePasswordResp{Message: "密码修改成功"}, nil
}

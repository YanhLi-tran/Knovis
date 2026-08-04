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

type UpdateEmailLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewUpdateEmailLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UpdateEmailLogic {
	return &UpdateEmailLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// UpdateEmail 修改邮箱(需密码 + 新邮箱验证码)
func (l *UpdateEmailLogic) UpdateEmail(req *types.UpdateEmailReq) (resp *types.UpdateEmailResp, err error) {
	uid, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}

	// 校验新邮箱验证码(校验通过即一次性删除)
	if err := checkCode(l.ctx, l.svcCtx, req.NewEmail, req.Code); err != nil {
		return nil, err
	}

	user, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(uid))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "用户不存在")
		}
		return nil, err
	}

	if !crypto.CheckPassword(req.Password, user.Password) {
		return nil, bizErr(http.StatusUnauthorized, "密码错误")
	}

	if _, err := l.svcCtx.UserModel.FindOneByEmail(l.ctx, req.NewEmail); err == nil {
		return nil, bizErr(http.StatusConflict, "该邮箱已被注册")
	} else if err != model.ErrNotFound {
		return nil, err
	}

	if err := l.svcCtx.UserModel.UpdateFields(l.ctx, user.Id, map[string]interface{}{"email": req.NewEmail}); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "修改邮箱失败")
	}

	return &types.UpdateEmailResp{Message: "邮箱修改成功"}, nil
}

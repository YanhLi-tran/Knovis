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

type LoginLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewLoginLogic(ctx context.Context, svcCtx *svc.ServiceContext) *LoginLogic {
	return &LoginLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// Login 登录(邮箱+密码) → 返回 JWT(access_token)
func (l *LoginLogic) Login(req *types.LoginReq) (resp *types.LoginResp, err error) {
	user, err := l.svcCtx.UserModel.FindOneByEmail(l.ctx, req.Email)
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusUnauthorized, "邮箱或密码错误")
		}
		return nil, err
	}

	if user.Status == userStatusGone {
		return nil, bizErr(http.StatusUnauthorized, "账号已注销")
	}

	if !crypto.CheckPassword(req.Password, user.Password) {
		return nil, bizErr(http.StatusUnauthorized, "邮箱或密码错误")
	}

	token, err := signToken(l.svcCtx, user.Id)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "生成令牌失败")
	}

	return &types.LoginResp{
		Message:     "登陆成功",
		UserID:      int64(user.Id),
		Username:    user.Name,
		Token:       token,
		AccessToken: token,
	}, nil
}

package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUserLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUserLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUserLogic {
	return &GetUserLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetUser 获取用户信息(自己或 email_visible=1 才返回邮箱)
func (l *GetUserLogic) GetUser(req *types.GetUserReq) (resp *types.UserInfo, err error) {
	user, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(req.ID))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "用户不存在")
		}
		return nil, err
	}

	cur, _ := userIdFromCtx(l.ctx)
	showEmail := uint64(cur) == user.Id || user.EmailVisible == 1
	return buildUserInfo(user, showEmail), nil
}

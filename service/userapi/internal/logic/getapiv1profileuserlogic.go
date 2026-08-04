package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetApiV1ProfileUserLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetApiV1ProfileUserLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetApiV1ProfileUserLogic {
	return &GetApiV1ProfileUserLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetApiV1ProfileUser 指定用户资料(对外契约; 自己或 email_visible=1 才返回邮箱)
func (l *GetApiV1ProfileUserLogic) GetApiV1ProfileUser(req *types.ProfileUserReq) (resp *types.UserInfo, err error) {
	user, err := l.svcCtx.UserModel.FindOne(l.ctx, uint64(req.UserID))
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

package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetApiV1ProfileLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetApiV1ProfileLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetApiV1ProfileLogic {
	return &GetApiV1ProfileLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetApiV1Profile 当前登录用户资料(对外契约; 返回本人完整信息含邮箱)
func (l *GetApiV1ProfileLogic) GetApiV1Profile(req *types.ProfileReq) (resp *types.UserInfo, err error) {
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
	return buildUserInfo(user, true), nil
}

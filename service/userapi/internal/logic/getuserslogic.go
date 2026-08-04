package logic

import (
	"context"

	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type GetUsersLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewGetUsersLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetUsersLogic {
	return &GetUsersLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// GetUsers 用户列表(分页)
func (l *GetUsersLogic) GetUsers(req *types.UserListReq) (resp *types.UserListResp, err error) {
	page, pageSize := normalizePage(req.Page, req.PageSize)
	offset := int64((page - 1) * pageSize)

	list, total, err := l.svcCtx.UserModel.FindPage(l.ctx, offset, int64(pageSize))
	if err != nil {
		return nil, err
	}

	users := make([]types.UserInfo, 0, len(list))
	for _, u := range list {
		users = append(users, *buildUserInfo(&u, u.EmailVisible == 1))
	}

	return &types.UserListResp{
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		List:     users,
	}, nil
}

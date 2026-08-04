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

type DeleteAccountLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeleteAccountLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeleteAccountLogic {
	return &DeleteAccountLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// DeleteAccount 注销账号: 级联删除用户全部动态 + 关联文件
func (l *DeleteAccountLogic) DeleteAccount(req *types.DeleteAccountReq) (resp *types.DeleteAccountResp, err error) {
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

	if !crypto.CheckPassword(req.Password, user.Password) {
		return nil, bizErr(http.StatusUnauthorized, "密码错误")
	}

	// 获取用户全部动态并删除关联文件
	posts, err := l.svcCtx.PostModel.FindByUserID(l.ctx, user.Id)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "注销失败: "+err.Error())
	}
	for _, p := range posts {
		removePostFiles(l.svcCtx.Config.UploadDir, &p)
	}

	// 级联删除动态记录
	if err := l.svcCtx.PostModel.DeleteByUserID(l.ctx, user.Id); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "注销失败: "+err.Error())
	}

	// 删除用户
	if err := l.svcCtx.UserModel.Delete(l.ctx, user.Id); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "注销失败: "+err.Error())
	}

	return &types.DeleteAccountResp{Message: "账号已注销"}, nil
}

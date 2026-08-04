package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type DeletePostLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewDeletePostLogic(ctx context.Context, svcCtx *svc.ServiceContext) *DeletePostLogic {
	return &DeletePostLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// DeletePost 删除动态(仅作者, 级联删除图片/视频文件)
func (l *DeletePostLogic) DeletePost(req *types.DeletePostReq) (resp *types.DeletePostResp, err error) {
	uid, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}

	post, err := l.svcCtx.PostModel.FindOne(l.ctx, uint64(req.ID))
	if err != nil {
		if err == model.ErrNotFound {
			return nil, bizErr(http.StatusNotFound, "动态不存在")
		}
		return nil, err
	}

	if post.UserId != uint64(uid) {
		return nil, bizErr(http.StatusForbidden, "无权删除他人的动态")
	}

	// 删除关联文件
	removePostFiles(l.svcCtx.Config.UploadDir, post)

	if err := l.svcCtx.PostModel.Delete(l.ctx, post.Id); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "删除失败")
	}

	return &types.DeletePostResp{Message: "删除成功"}, nil
}

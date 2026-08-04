// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package handler

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/logic"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"
	"knovis/service/userapi/internal/upload"

	"github.com/zeromicro/go-zero/rest/httpx"
)

func CreatePostHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.CreatePostReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		// 将原始请求放入 context, 供 logic 读取 multipart 上传文件
		ctx := context.WithValue(r.Context(), upload.ReqKey, r)
		l := logic.NewCreatePostLogic(ctx, svcCtx)
		resp, err := l.CreatePost(&req)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
		} else {
			httpx.OkJsonCtx(r.Context(), w, resp)
		}
	}
}

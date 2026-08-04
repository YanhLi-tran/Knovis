package logic

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"
	"knovis/service/userapi/internal/upload"

	"github.com/zeromicro/go-zero/core/logx"
)

type CreatePostLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreatePostLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreatePostLogic {
	return &CreatePostLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// CreatePost 发布动态(text/image/video; image 多图最多9张, video 单文件)
func (l *CreatePostLogic) CreatePost(req *types.CreatePostReq) (resp *types.CreatePostResp, err error) {
	postType := req.Type
	content := strings.TrimSpace(req.Content)

	if postType != "text" && postType != "image" && postType != "video" {
		return nil, bizErr(http.StatusBadRequest, "类型只能是 text、image或 video")
	}

	uid, err := userIdFromCtx(l.ctx)
	if err != nil {
		return nil, bizErr(http.StatusUnauthorized, "请先登录")
	}
	userID := uint64(uid)

	switch postType {
	case "text":
		return l.createText(userID, content)
	case "image":
		return l.createImage(userID, content)
	case "video":
		return l.createVideo(userID, content)
	}
	return nil, bizErr(http.StatusBadRequest, "类型只能是 text、image或 video")
}

func (l *CreatePostLogic) createText(userID uint64, content string) (*types.CreatePostResp, error) {
	if content == "" {
		return nil, bizErr(http.StatusBadRequest, "文字内容不能为空")
	}
	if utf8.RuneCountInString(content) > 1000 {
		return nil, bizErr(http.StatusBadRequest, "文字内容不能超过1000字")
	}

	post := &model.Post{UserId: userID, Type: "text", Content: content, ShowLikes: 1, ShowFavorites: 1}
	ret, err := l.svcCtx.PostModel.Insert(l.ctx, post)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "发布失败")
	}
	id, _ := ret.LastInsertId()
	return &types.CreatePostResp{Message: "发布成功", PostID: id}, nil
}

func (l *CreatePostLogic) createImage(userID uint64, content string) (*types.CreatePostResp, error) {
	r, ok := l.ctx.Value(upload.ReqKey).(*http.Request)
	if !ok {
		return nil, bizErr(http.StatusBadRequest, "请上传图片")
	}

	form := r.MultipartForm
	if form == nil {
		return nil, bizErr(http.StatusBadRequest, "请上传图片")
	}

	files := form.File["media[]"]
	if len(files) == 0 {
		return nil, bizErr(http.StatusBadRequest, "请至少上传一张图片")
	}
	if len(files) > upload.MaxImageCount {
		return nil, bizErr(http.StatusBadRequest, "最多上传9张图片")
	}

	urls, err := upload.SaveImages(userID, files, l.svcCtx.Config.UploadDir)
	if err != nil {
		return nil, bizErr(http.StatusBadRequest, err.Error())
	}

	mediaJSON, _ := json.Marshal(urls)
	post := &model.Post{
		UserId:        userID,
		Type:          "image",
		Content:       content,
		MediaUrl:      urls[0],
		MediaUrls:     sql.NullString{String: string(mediaJSON), Valid: true},
		ShowLikes:     1,
		ShowFavorites: 1,
	}
	ret, err := l.svcCtx.PostModel.Insert(l.ctx, post)
	if err != nil {
		upload.DeleteFiles(urls, l.svcCtx.Config.UploadDir)
		return nil, bizErr(http.StatusInternalServerError, "发布失败")
	}
	id, _ := ret.LastInsertId()
	return &types.CreatePostResp{Message: "发布成功", PostID: id}, nil
}

func (l *CreatePostLogic) createVideo(userID uint64, content string) (*types.CreatePostResp, error) {
	r, ok := l.ctx.Value(upload.ReqKey).(*http.Request)
	if !ok {
		return nil, bizErr(http.StatusBadRequest, "请上传视频")
	}

	f, header, err := r.FormFile("video")
	if err != nil {
		return nil, bizErr(http.StatusBadRequest, "请上传视频")
	}
	f.Close()

	url, err := upload.SaveVideo(userID, header, l.svcCtx.Config.UploadDir)
	if err != nil {
		return nil, bizErr(http.StatusBadRequest, err.Error())
	}

	post := &model.Post{
		UserId:        userID,
		Type:          "video",
		Content:       content,
		VideoUrl:      url,
		ShowLikes:     1,
		ShowFavorites: 1,
	}
	ret, err := l.svcCtx.PostModel.Insert(l.ctx, post)
	if err != nil {
		upload.DeleteFiles([]string{url}, l.svcCtx.Config.UploadDir)
		return nil, bizErr(http.StatusInternalServerError, "发布失败")
	}
	id, _ := ret.LastInsertId()
	return &types.CreatePostResp{Message: "发布成功", PostID: id}, nil
}

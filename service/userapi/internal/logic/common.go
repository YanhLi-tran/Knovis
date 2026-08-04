// Package logic 业务逻辑层。
package logic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"knovis/service/userapi/internal/errs"
	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"
	"knovis/service/userapi/internal/upload"

	"github.com/golang-jwt/jwt/v5"
)

var (
	emailRegex    = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	passwordRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

const (
	codeTTL        = 300          // 验证码有效期(秒)
	codeCoolDown   = 60           // 同一邮箱发送验证码最小间隔(秒)
	maxCodeFails   = 5            // 验证码最大错误次数
	defaultBio     = "用户还没有在此留下足迹哦~"
	userStatusOK   = 1            // 正常
	userStatusGone = 0            // 已注销
	maxPageSize    = 50           // 列表接口单页上限
)

// bizErr 构造带 HTTP 状态码的业务错误, 由 main 注册的错误处理器输出对应状态码
func bizErr(code int, msg string) error {
	return errs.New(code, msg)
}

// userIdFromCtx 从 JWT claims 中提取当前登录用户 ID
// (go-zero jwt 中间件将非标准 claims 以 context value 形式注入)
func userIdFromCtx(ctx context.Context) (int64, error) {
	v := ctx.Value("userId")
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return n.Int64()
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, errors.New("未提供认证令牌或令牌无效")
	}
}

// checkCode 校验邮箱验证码(Redis: key=email, value=code)
// 错误次数累计, 超过 maxCodeFails 后验证码作废
func checkCode(ctx context.Context, svcCtx *svc.ServiceContext, email, code string) error {
	failKey := "code_fail:" + email

	fails, err := svcCtx.Redis.IncrCtx(ctx, failKey)
	if err == nil {
		_ = svcCtx.Redis.ExpireCtx(ctx, failKey, codeTTL)
		if fails > maxCodeFails {
			_, _ = svcCtx.Redis.DelCtx(ctx, email)
			_, _ = svcCtx.Redis.DelCtx(ctx, failKey)
			return errs.New(http.StatusTooManyRequests, "验证码尝试次数过多，请重新获取")
		}
	}

	saved, err := svcCtx.Redis.GetCtx(ctx, email)
	if err != nil || saved != code {
		return errs.New(http.StatusBadRequest, "验证码错误或已过期")
	}

	// 校验通过, 清除失败计数与验证码(一次性)
	_, _ = svcCtx.Redis.DelCtx(ctx, failKey)
	_, _ = svcCtx.Redis.DelCtx(ctx, email)
	return nil
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > maxPageSize {
		pageSize = 10
	}
	return page, pageSize
}

// signToken 签发 HS256 JWT(Secret/Issuer/Audience 走配置)
func signToken(svcCtx *svc.ServiceContext, userID uint64) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"userId": userID,
		"iat":    now.Unix(),
		"exp":    now.Add(time.Duration(svcCtx.Config.Auth.AccessExpire) * time.Second).Unix(),
		"iss":    svcCtx.Config.Auth.Issuer,
		"aud":    svcCtx.Config.Auth.Audience,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(svcCtx.Config.Auth.AccessSecret))
}

func buildUserInfo(u *model.User, showEmail bool) *types.UserInfo {
	email := ""
	if showEmail {
		email = u.Email
	}
	return &types.UserInfo{
		ID:               int64(u.Id),
		Name:             u.Name,
		Email:            email,
		Avatar:           u.Avatar,
		Bio:              u.Bio,
		EmailVisible:     u.EmailVisible == 1,
		LikesVisible:     u.LikesVisible == 1,
		FavoritesVisible: u.FavoritesVisible == 1,
		FollowVisible:    u.FollowVisible == 1,
		Status:           u.Status,
		CreatedAt:        u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        u.UpdatedAt.Format(time.RFC3339),
	}
}

func buildPostInfo(p *model.Post, userName string) *types.PostInfo {
	var mediaURLs []string
	if p.MediaUrls.Valid && p.MediaUrls.String != "" {
		_ = json.Unmarshal([]byte(p.MediaUrls.String), &mediaURLs)
	}
	return &types.PostInfo{
		ID:             int64(p.Id),
		User:           types.PostUser{ID: int64(p.UserId), Name: userName},
		Type:           p.Type,
		Content:        p.Content,
		MediaURL:       p.MediaUrl,
		MediaURLs:      mediaURLs,
		VideoURL:       p.VideoUrl,
		VideoDuration:  p.VideoDuration,
		VideoThumbnail: p.VideoThumbnail,
		Views:          p.Views,
		Likes:          p.Likes,
		Comments:       p.Comments,
		Favorites:      p.Favorites,
		ShowLikes:      p.ShowLikes == 1,
		ShowFavorites:  p.ShowFavorites == 1,
		CreatedAt:      p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      p.UpdatedAt.Format(time.RFC3339),
	}
}

// userNameByID 查询用户名(列表场景 N+1, 数据量小可接受)
func userNameByID(ctx context.Context, svcCtx *svc.ServiceContext, id uint64) string {
	u, err := svcCtx.UserModel.FindOne(ctx, id)
	if err != nil {
		return ""
	}
	return u.Name
}

// removePostFiles 删除动态关联的图片/视频文件(忽略不存在等错误)
func removePostFiles(uploadDir string, p *model.Post) {
	if p.MediaUrls.Valid && p.MediaUrls.String != "" {
		var urls []string
		if json.Unmarshal([]byte(p.MediaUrls.String), &urls) == nil {
			upload.DeleteFiles(urls, uploadDir)
		}
	}
	if p.VideoUrl != "" {
		upload.DeleteFiles([]string{p.VideoUrl}, uploadDir)
	}
}

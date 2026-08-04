package logic

import (
	"context"
	"net/http"

	"knovis/service/userapi/internal/email"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type SendCodeLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSendCodeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SendCodeLogic {
	return &SendCodeLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// SendCode 发送邮箱验证码(存 Redis, TTL 5 分钟; SMTP 发送)
func (l *SendCodeLogic) SendCode(req *types.SendCodeReq) (resp *types.SendCodeResp, err error) {
	if !emailRegex.MatchString(req.Email) {
		return nil, bizErr(http.StatusBadRequest, "邮箱格式不正确")
	}

	// 冷却: 同一邮箱 60 秒内仅允许发送一次
	ok, err := l.svcCtx.Redis.SetnxExCtx(l.ctx, "code_cool:"+req.Email, "1", codeCoolDown)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "验证码存储失败")
	}
	if !ok {
		return nil, bizErr(http.StatusTooManyRequests, "发送过于频繁，请稍后再试")
	}

	code := email.GenerateCode()
	if err := l.svcCtx.Redis.SetexCtx(l.ctx, req.Email, code, codeTTL); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "验证码存储失败")
	}

	if err := email.SendVerificationCode(
		l.svcCtx.Config.SMTP.Host, l.svcCtx.Config.SMTP.Port,
		l.svcCtx.Config.SMTP.Username, l.svcCtx.Config.SMTP.Password,
		req.Email, code,
	); err != nil {
		return nil, bizErr(http.StatusInternalServerError, "发送验证码失败: "+err.Error())
	}

	return &types.SendCodeResp{Message: "验证码已发送"}, nil
}

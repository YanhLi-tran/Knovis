package logic

import (
	"context"
	"net/http"
	"unicode/utf8"

	"knovis/service/userapi/internal/crypto"
	"knovis/service/userapi/internal/model"
	"knovis/service/userapi/internal/svc"
	"knovis/service/userapi/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

type RegisterLogic struct {
	logx.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewRegisterLogic(ctx context.Context, svcCtx *svc.ServiceContext) *RegisterLogic {
	return &RegisterLogic{
		Logger: logx.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

// Register 注册(用户名+密码+确认密码+邮箱+验证码)
func (l *RegisterLogic) Register(req *types.RegisterReq) (resp *types.RegisterResp, err error) {
	if req.Password != req.ConfirmPassword {
		return nil, bizErr(http.StatusBadRequest, "两次输入的密码不一致")
	}

	nameLen := utf8.RuneCountInString(req.Username)
	if nameLen < 1 || nameLen > 20 {
		return nil, bizErr(http.StatusBadRequest, "用户名需要1-20个字符")
	}
	if len(req.Password) < 6 {
		return nil, bizErr(http.StatusBadRequest, "密码至少需要6个字符")
	}
	if !passwordRegex.MatchString(req.Password) {
		return nil, bizErr(http.StatusBadRequest, "密码只能包含大小写字母、数字和下划线_")
	}
	if !emailRegex.MatchString(req.Email) {
		return nil, bizErr(http.StatusBadRequest, "邮箱格式不正确")
	}

	// 校验验证码(Redis; 错误次数超限后作废, 校验通过即一次性删除)
	if err := checkCode(l.ctx, l.svcCtx, req.Email, req.Code); err != nil {
		return nil, err
	}

	// 邮箱唯一性
	if _, err := l.svcCtx.UserModel.FindOneByEmail(l.ctx, req.Email); err == nil {
		return nil, bizErr(http.StatusConflict, "该邮箱已被注册")
	} else if err != model.ErrNotFound {
		return nil, err
	}

	hashed, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "密码加密失败")
	}

	user := &model.User{
		Name:     req.Username,
		Email:    req.Email,
		Password: hashed,
		Bio:      defaultBio,
		Status:   userStatusOK,
	}
	ret, err := l.svcCtx.UserModel.Insert(l.ctx, user)
	if err != nil {
		return nil, bizErr(http.StatusInternalServerError, "注册失败")
	}

	id, _ := ret.LastInsertId()
	return &types.RegisterResp{Message: "注册成功", UserID: id}, nil
}
